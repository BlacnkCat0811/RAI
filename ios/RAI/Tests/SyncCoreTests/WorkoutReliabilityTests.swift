import Foundation
import Synchronization
import Testing
@testable import SyncCore

func durableWorkoutACK(_ id: String, _ hash: String) -> JSON {
    var counts: [String: JSON] = ["workoutInserted": .bool(true)]
    for key in ["samplesInserted", "samplesUnchanged", "samplesDeleted", "associationsInserted", "associationsUnchanged",
                "associationsDeleted", "seriesInserted", "seriesUnchanged", "routesInserted", "routesUnchanged", "routesDeleted"] { counts[key] = .int(0) }
    return .object(["workoutID": .string(id), "bundleSHA256": .string(hash), "ingestStatus": .string("committed"),
                    "commitRevision": .int(1), "counts": .object(counts), "analysisStatus": .string("queued"), "analysisJobIDs": .array([.string(Wire.id())])])
}

private struct Reply: Sendable {
    var status = 200
    var body = Data()
    var contentType: String? = "application/json"
    var retryAfter: String?
    var network: URLError.Code?
}
private final class ReliabilityProtocol: URLProtocol, @unchecked Sendable {
    struct State { var replies: [Reply] = []; var requests: [URLRequest] = [] }
    static let state = Mutex(State())
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let reply = Self.state.withLock { state in
            state.requests.append(request)
            return state.replies.isEmpty ? Reply(status: 404, body: Data("{\"code\":\"workout_import_not_found\"}".utf8)) : state.replies.removeFirst()
        }
        if let code = reply.network { client?.urlProtocol(self, didFailWithError: URLError(code)); return }
        var headers: [String: String] = [:]
        headers["Content-Type"] = reply.contentType; headers["Retry-After"] = reply.retryAfter
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: nil, headerFields: headers)!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: reply.body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
private func deliveryFixture() async throws -> (OutboxStore, URL, String, String, URL, RAIAPIClient) {
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-reliability-" + Wire.id())
    let store = try OutboxStore(directory: directory), id = Wire.id(), bundle = Data("{\"synthetic\":true}".utf8), hash = Wire.hash(bundle)
    try await store.beginTask(id)
    let file = try await store.savePayload(WorkoutBundleAssembler.multipart(bundle: bundle, boundary: "test"), suffix: "multipart.json")
    try await store.setBundle(id, file: file, sha256: hash, boundary: "test")
    let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [ReliabilityProtocol.self]
    let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", session: URLSession(configuration: config))
    ReliabilityProtocol.state.withLock { $0 = .init() }
    return (store, directory, id, hash, try await store.fileURL(file), api)
}

@Suite(.serialized)
struct WorkoutReliabilityTests {
    @Test func cancelledDeliveryKeepsBundleAndCachedResponseForReattachment() async throws {
        let (store, directory, id, hash, file, _) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let original = try Data(contentsOf: file)
        let uploader = CancelledBundleUploader()
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [ReliabilityProtocol.self]
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic",
                               session: URLSession(configuration: config), uploader: uploader)
        do {
            try await WorkoutBundleDelivery(store: store, api: api).deliver(id)
            Issue.record("cancellation was swallowed")
        } catch is CancellationError { }
        let pending = try #require(await store.task(id))
        #expect(pending.phase == .uploading && pending.errorCode == "cancelled")
        #expect(pending.uploadReceipt == nil && pending.bundleSHA256 == hash)
        #expect(try Data(contentsOf: file) == original)
        #expect(uploader.discarded.withLock { $0 } == 0)
        #expect(ReliabilityProtocol.state.withLock { $0.requests.isEmpty }) // No GET after cancellation.
        let reopened = try OutboxStore(directory: directory)
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(body: try! durableWorkoutACK(id, hash).data())] }
        let resumed = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", session: URLSession(configuration: config))
        #expect(try await WorkoutBundleDelivery(store: reopened, api: resumed).deliver(id, now: try #require(pending.nextAttemptAt)))
        #expect(await reopened.task(id)?.uploadReceipt != nil)
        #expect(try Data(contentsOf: file) == original)
    }

    @Test(arguments: [URLError.notConnectedToInternet, .networkConnectionLost, .timedOut])
    func networkThen404RetriesOriginalBundle(_ code: URLError.Code) async throws {
        let (store, directory, id, hash, file, api) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let original = try Data(contentsOf: file), now = Date()
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(network: code)] }
        do { try await WorkoutBundleDelivery(store: store, api: api).deliver(id, now: now); Issue.record("accepted uncertain upload") } catch { }
        let pending = try #require(await store.task(id))
        #expect(pending.phase == .uploading && pending.attemptCount == 1)
        #expect(pending.uploadReceipt == nil && pending.acknowledgedAt == nil)
        #expect(try Data(contentsOf: file) == original)
        #expect(pending.bundleSHA256 == hash && pending.nextAttemptAt! > now)
        let reopened = try OutboxStore(directory: directory); try await reopened.reconcileTasks()
        #expect(try await !WorkoutBundleDelivery(store: reopened, api: api).deliver(id, now: now))
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(body: try! durableWorkoutACK(id, hash).data())] }
        #expect(try await WorkoutBundleDelivery(store: reopened, api: api).deliver(id, now: pending.nextAttemptAt!))
        #expect(await reopened.task(id)?.phase == .acknowledged)
        #expect(await reopened.task(id)?.uploadReceipt != nil)
        #expect(try Data(contentsOf: file) == original) // authorized cleanup is separate
        _ = try await reopened.collectGarbage(dryRun: false)
        #expect(!FileManager.default.fileExists(atPath: file.path))
        let requests = ReliabilityProtocol.state.withLock { $0.requests }
        #expect(requests.map(\.httpMethod) == ["POST", "GET", "POST"])
        #expect(requests[1].url?.query == "sha256=" + hash)
    }

    @Test(arguments: ["empty", "json", "trailing_json", "null", "empty_jobs", "bad_job", "missing_id", "wrong_id", "wrong_receipt", "zero_rev", "missing_rev", "null_rev", "type_rev", "status", "missing_jobs", "null_jobs", "counts_type", "counts_missing", "counts_negative", "delete_ack", "201", "202", "204", "500", "502", "503", "content_type", "missing_content_type"])
    func invalidACKPreservesSource(_ variant: String) async throws {
        let (store, directory, id, hash, file, api) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        var ack = durableWorkoutACK(id, hash).fields, reply = Reply()
        switch variant {
        case "empty_jobs": ack["analysisJobIDs"] = .array([])
        case "bad_job": ack["analysisJobIDs"] = .array([.int(1)])
        case "missing_id": ack.removeValue(forKey: "workoutID")
        case "wrong_id": ack["workoutID"] = .string(Wire.id())
        case "wrong_receipt": ack["bundleSHA256"] = .string(String(repeating: "b", count: 64))
        case "zero_rev": ack["commitRevision"] = .int(0)
        case "missing_rev": ack.removeValue(forKey: "commitRevision")
        case "null_rev": ack["commitRevision"] = .null
        case "type_rev": ack["commitRevision"] = .string("1")
        case "status": ack["ingestStatus"] = .string("accepted")
        case "missing_jobs": ack.removeValue(forKey: "analysisJobIDs")
        case "null_jobs": ack["analysisJobIDs"] = .null
        case "counts_type": ack["counts"] = .object(["workoutInserted": .int(1)])
        case "counts_missing": ack["counts"] = .object([:])
        case "counts_negative": var counts = ack["counts"]!.fields; counts["samplesInserted"] = .int(-1); ack["counts"] = .object(counts)
        case "delete_ack": ack["ingestStatus"] = .string("deleted"); ack["alreadyDeleted"] = .bool(false)
        case "201", "202", "204", "500", "502", "503": reply.status = Int(variant)!
        case "content_type": reply.contentType = "text/html"
        case "missing_content_type": reply.contentType = nil
        default: break
        }
        reply.body = try JSON.object(ack).data()
        if variant == "empty" { reply.body = Data() }
        if variant == "json" { reply.body = Data("{broken".utf8) }
        if variant == "trailing_json" { reply.body += Data(" {}".utf8) }
        if variant == "null" { reply.body = Data("null".utf8) }
        ReliabilityProtocol.state.withLock { $0.replies = [reply] }
        do { try await WorkoutBundleDelivery(store: store, api: api).deliver(id); Issue.record("invalid ACK accepted: \(variant)") } catch { }
        #expect(FileManager.default.fileExists(atPath: file.path))
        let task = try #require(await store.task(id))
        #expect(task.uploadReceipt == nil && task.acknowledgedAt == nil && task.bundleFile != nil)
        #expect(task.phase == .uploading)
        #expect(try await store.collectGarbage(dryRun: false) == 0)
    }

    @Test(arguments: ["lost", "corrupt"])
    func committedResponseRecoveredByExactHash(_ mode: String) async throws {
        let (store, directory, id, hash, file, api) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let ack = durableWorkoutACK(id, hash)
        ReliabilityProtocol.state.withLock { $0.replies = [mode == "lost" ? Reply(network: .networkConnectionLost) : Reply(body: Data("broken".utf8)), Reply(body: try! ack.data())] }
        #expect(try await WorkoutBundleDelivery(store: store, api: api).deliver(id))
        #expect(await store.task(id)?.uploadReceipt == ack)
        #expect(FileManager.default.fileExists(atPath: file.path))
        _ = try await store.collectGarbage(dryRun: false)
        #expect(!FileManager.default.fileExists(atPath: file.path))
    }

    @Test(arguments: [408, 429, 500, 502, 503, 401, 403, 400, 422, 409])
    func retryAndBlockedClassification(_ status: Int) async throws {
        let (store, directory, id, _, file, api) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let now = Date(), code = status == 409 ? "workout_deleted" : "synthetic_failure"
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(status: status, body: Data(("{\"code\":\"" + code + "\"}").utf8), retryAfter: status == 429 ? "120" : nil)] }
        do { try await WorkoutBundleDelivery(store: store, api: api).deliver(id, now: now) } catch { }
        let task = try #require(await store.task(id))
        #expect(task.phase == ([400, 403, 422, 409].contains(status) ? .failed : .uploading))
        #expect(FileManager.default.fileExists(atPath: file.path))
        if status == 429 { #expect(task.nextAttemptAt! >= now.addingTimeInterval(120)) }
        if status == 409 {
            #expect(task.errorCode == "workout_deleted" && task.deletionReceipt == nil)
            do { try await store.beginTask(id); Issue.record("deleted conflict restarted") } catch { }
        }
        #expect(try await !WorkoutBundleDelivery(store: store, api: api).deliver(id, now: now))
    }

    @Test func authenticationRefreshOnceAndRetryAfterDate() async throws {
        let (store, directory, id, hash, file, _) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [ReliabilityProtocol.self]
        let refreshes = Mutex(0)
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "expired", session: URLSession(configuration: config), refreshToken: {
            refreshes.withLock { $0 += 1 }; return "refreshed"
        })
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(status: 401), Reply(body: try! durableWorkoutACK(id, hash).data())] }
        #expect(try await WorkoutBundleDelivery(store: store, api: api).deliver(id))
        #expect(refreshes.withLock { $0 } == 1)
        #expect(ReliabilityProtocol.state.withLock { $0.requests.last?.value(forHTTPHeaderField: "Authorization") } == "Bearer refreshed")
        #expect(FileManager.default.fileExists(atPath: file.path))
        #expect(RAIAPIClient.retryAfter("Sun, 01 Nov 2026 00:02:00 GMT", now: Date(timeIntervalSince1970: 1_793_491_200)) == 120)
    }

    @Test func twoWorkersOneClaimAndRestartReleasesInFlight() async throws {
        let (store, directory, id, _, file, _) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let now = Date()
        async let first = store.claimBundle(id, now: now)
        async let second = store.claimBundle(id, now: now)
        let claims = try await [first, second]
        #expect(claims.compactMap { $0 }.count == 1)
        let reopened = try OutboxStore(directory: directory)
        #expect(try await reopened.claimBundle(id, now: now) == nil)
        #expect(try await reopened.claimBundle(id, now: now.addingTimeInterval(61)) != nil)
        #expect(FileManager.default.fileExists(atPath: file.path))
    }

    @Test func stateWriteFailureDuplicateACKAndCleanupFailure() async throws {
        let (store, directory, id, hash, file, _) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let ack = durableWorkoutACK(id, hash), authority = directory.appendingPathComponent("state.json")
        let saved = try Data(contentsOf: authority)
        try FileManager.default.removeItem(at: authority)
        try FileManager.default.createDirectory(at: authority, withIntermediateDirectories: false)
        do { try await store.acknowledgeBundle(id, receipt: ack); Issue.record("state failure ignored") } catch { }
        #expect(await store.task(id)?.uploadReceipt == nil)
        #expect(FileManager.default.fileExists(atPath: file.path))
        #expect(try await store.collectGarbage(dryRun: false) == 0)
        try FileManager.default.removeItem(at: authority); try saved.write(to: authority)
        let reopened = try OutboxStore(directory: directory)
        try await reopened.acknowledgeBundle(id, receipt: ack)
        try await reopened.acknowledgeBundle(id, receipt: ack)
        // Crash after state persistence, before unlink: reopen still owns ACK.
        let afterCrash = try OutboxStore(directory: directory)
        #expect(await afterCrash.task(id)?.uploadReceipt == ack)
        #expect(FileManager.default.fileExists(atPath: file.path))
        // A non-file at the authorized GC path models a persistent cleanup error.
        let bytes = try Data(contentsOf: file)
        try FileManager.default.removeItem(at: file); try FileManager.default.createDirectory(at: file, withIntermediateDirectories: false)
        do { _ = try await afterCrash.collectGarbage(dryRun: false); Issue.record("unsafe cleanup accepted") } catch { }
        #expect(await afterCrash.task(id)?.uploadReceipt == ack)
        #expect(await afterCrash.task(id)?.phase == .acknowledged)
        try FileManager.default.removeItem(at: file); try bytes.write(to: file)
        _ = try await afterCrash.collectGarbage(dryRun: false)
        #expect(!FileManager.default.fileExists(atPath: file.path))
        try await afterCrash.acknowledgeBundle(id, receipt: ack)
    }

    @Test func explicitRescanAndProoflessLegacyACKCannotReleaseBundle() async throws {
        let (store, directory, id, hash, file, _) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        try await store.beginTask(id, resetBundle: true)
        #expect(await store.task(id)?.bundleSHA256 == hash)
        do { try await store.releaseBundle(id); Issue.record("source released without proof") } catch { }
        try await store.updateTask(id) { $0.phase = .acknowledged; $0.acknowledgedAt = Date() }
        let reopened = try OutboxStore(directory: directory); try await reopened.reconcileTasks()
        #expect(await reopened.task(id)?.phase == .uploading)
        #expect(FileManager.default.fileExists(atPath: file.path))
    }
    @Test func handshakeFailureBackoffSurvivesRestart() async throws {
        let (store, directory, id, _, file, _) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let now = Date()
        try await store.deferBundleRecovery(UploadError(code: "unauthorized", status: 401), now: now)
        let pending = try #require(await store.task(id))
        #expect(pending.phase == .uploading && pending.errorCode == "authentication_required")
        #expect(pending.nextAttemptAt! >= now.addingTimeInterval(60))
        let reopened = try OutboxStore(directory: directory)
        #expect(try await reopened.claimBundle(id, now: now) == nil)
        for attempt in 2...12 {
            let instant = now.addingTimeInterval(Double(attempt) * 3600)
            try await reopened.deferBundleRecovery(UploadError(code: "unavailable", status: 503), now: instant)
            let task = try #require(await reopened.task(id))
            #expect(task.attemptCount == attempt)
            #expect(task.nextAttemptAt == instant.addingTimeInterval(min(900, pow(2, Double(attempt - 1)))))
        }
        try await reopened.deferBundleRecovery(UploadError(code: "forbidden", status: 403))
        #expect(await reopened.task(id)?.phase == .failed)
        #expect(FileManager.default.fileExists(atPath: file.path))
    }
    @Test func concurrentDeliveryAndLateDeletionCallbacks() async throws {
        let (store, directory, id, hash, file, api) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let ack = durableWorkoutACK(id, hash)
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(body: try! ack.data())] }
        async let first = WorkoutBundleDelivery(store: store, api: api).deliver(id)
        async let second = WorkoutBundleDelivery(store: store, api: api).deliver(id)
        let outcomes = try await [first, second]
        #expect(outcomes.filter { $0 }.count == 1)
        #expect(ReliabilityProtocol.state.withLock { $0.requests.count } == 1)
        #expect(FileManager.default.fileExists(atPath: file.path))
        let deleted = JSON.object(["workoutID": .string(id), "ingestStatus": .string("deleted"), "commitRevision": .int(2), "alreadyDeleted": .bool(false)])
        try await store.acknowledgeWorkoutDeletion(id, receipt: deleted)
        try await store.failBundle(id, error: UploadError(code: "late_network_failure"))
        do { try await store.acknowledgeBundle(id, receipt: ack); Issue.record("POST ACK replaced durable deletion") } catch { }
        #expect(await store.task(id)?.phase == .deleted)
        #expect(await store.task(id)?.deletionReceipt == deleted)
        #expect(FileManager.default.fileExists(atPath: file.path))
    }

}

private final class CancelledBundleUploader: FileUploader, Sendable {
    let discarded = Mutex(0)
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) { throw CancellationError() }
    func discardCachedResult(_ identity: String) { discarded.withLock { $0 += 1 } }
}

extension WorkoutReliabilityTests {
    @Test func deterministicGzipArtifactSurvivesRestartCorruptionAndACK() async throws {
        let (store, directory, id, hash, originalFile, api) = try await deliveryFixture()
        defer { try? FileManager.default.removeItem(at: directory) }
        let original = try Data(contentsOf: originalFile), now = Date()
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(network: .networkConnectionLost)] }
        do { _ = try await WorkoutBundleDelivery(store: store, api: api).deliver(id, now: now) } catch {}
        let first = try #require(await store.task(id)), wireName = try #require(first.bundleWireFile)
        let wireURL = try await store.fileURL(wireName), wire = try Data(contentsOf: wireURL)
        #expect(first.bundleSHA256 == hash && first.bundleWireSHA256 == Wire.hash(wire))
        #expect(String(decoding: wire.prefix(130), as: UTF8.self).contains("application/gzip"))
        #expect(try Data(contentsOf: originalFile) == original)
        let reopened = try OutboxStore(directory: directory)
        try Data("corrupt wire".utf8).write(to: wireURL)
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(network: .timedOut)] }
        let later = now.addingTimeInterval(1000)
        do { _ = try await WorkoutBundleDelivery(store: reopened, api: api).deliver(id, now: later) } catch {}
        let recovered = try #require(await reopened.task(id)), recoveredName = try #require(recovered.bundleWireFile)
        #expect(recoveredName != wireName)
        #expect(try Data(contentsOf: await reopened.fileURL(recoveredName)) == wire)
        #expect(try Data(contentsOf: originalFile) == original)
        _ = try await reopened.collectGarbage(dryRun: false)
        #expect(FileManager.default.fileExists(atPath: originalFile.path))
        ReliabilityProtocol.state.withLock { $0.replies = [Reply(body: try! durableWorkoutACK(id, hash).data())] }
        #expect(try await WorkoutBundleDelivery(store: reopened, api: api).deliver(id, now: later.addingTimeInterval(1000)))
        #expect(await reopened.task(id)?.bundleWireFile == nil)
        _ = try await reopened.collectGarbage(dryRun: false)
        #expect(!FileManager.default.fileExists(atPath: originalFile.path))
    }
    @Test func gzipHasFixedHeadersAndBytes() throws {
        let raw = Data(repeating: 65, count: 20000), a = try Wire.gzip(raw), b = try Wire.gzip(raw)
        #expect(a == b)
        #expect(Array(a.prefix(10)) == [31,139,8,0,0,0,0,0,0,255])
    }
}
