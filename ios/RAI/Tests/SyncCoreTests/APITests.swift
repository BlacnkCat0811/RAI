import Foundation
import Synchronization
import Testing
@testable import SyncCore

private struct MockState {
    var envelope: JSON = .null
    var committed = false
    var failUpload = true
    var uploadCount = 0
    var lostAfterCommit = true
    var controlStatus = 200
    var controlCode = "temporarily_unavailable"
    var associationUnavailable = false
    var replies: [String: JSON] = [:]
    var requests: [String] = []
    var activeSeries = 0
    var peakSeries = 0
    var failOnce: String?
    var rejectPageReceipt = false
}
// URLProtocol's superclass requires this conformance. Mutable test state is
// protected by Mutex; production code does not use unchecked Sendable.
private final class MockProtocol: URLProtocol, @unchecked Sendable {
    static let state = Mutex(MockState())
    private let finished = Mutex(false)
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let (status, json) = Self.state.withLock { state -> (Int, JSON) in
            if state.controlStatus != 200 { return (state.controlStatus, .object(["code": .string(state.controlCode)])) }
            let path = request.url!.path
            state.requests.append(request.httpMethod! + " " + path)
            if path.hasPrefix("/v2/sync/series") {
                state.activeSeries += 1
                state.peakSeries = max(state.peakSeries, state.activeSeries)
                if path == state.failOnce { state.failOnce = nil; return (429, .object(["code": .string("device_rate_exceeded")])) }
            }
            if path.contains("/sync/pages/"), state.rejectPageReceipt { return (503, .object(["code": .string("temporarily_unavailable")])) }
            if path == "/v2/sync/series" {
                var body = request.httpBody ?? Data()
                if body.isEmpty, let input = request.httpBodyStream {
                    input.open(); defer { input.close() }
                    var buffer = [UInt8](repeating: 0, count: 1024)
                    while input.hasBytesAvailable {
                        let count = input.read(&buffer, maxLength: buffer.count)
                        if count <= 0 { break }; body.append(contentsOf: buffer.prefix(count))
                    }
                }
                return (200, (try? JSON.decode(body)) ?? .null)
            }
            if path == "/v2/sync/associations", state.associationUnavailable { return (409, .object(["code": .string("association_unavailable")])) }
            if request.httpMethod == "POST", path == "/v2/sync/batches" {
                var body = request.httpBody ?? Data()
                if body.isEmpty, let input = request.httpBodyStream {
                    input.open(); defer { input.close() }
                    var buffer = [UInt8](repeating: 0, count: 1024)
                    while input.hasBytesAvailable {
                        let count = input.read(&buffer, maxLength: buffer.count)
                        if count <= 0 { break }; body.append(contentsOf: buffer.prefix(count))
                    }
                }
                state.envelope = (try? JSON.decode(body)) ?? .null
                state.committed = false
                return (200, .object(["envelope": state.envelope, "ingestStatus": .string("registered")]))
            }
            if path.hasPrefix("/v2/sync/batches/") {
                guard state.envelope != .null, request.url!.lastPathComponent == state.envelope["batchID"].text else { return (404, .object(["code": .string("batch_not_found")])) }
                return (200, .object(["envelope": state.envelope, "ingestStatus": .string(state.committed ? "committed" : "registered"), "receipt": mockReceipt(state.envelope)]))
            }
            return (200, state.replies[path] ?? .object([:]))
        }
        let send: @Sendable () -> Void = {
            guard self.finishOnce() else { return }
            if self.request.url!.path.hasPrefix("/v2/sync/series") { Self.state.withLock { $0.activeSeries -= 1 } }
            let response = HTTPURLResponse(url: self.request.url!, statusCode: status, httpVersion: nil, headerFields: ["Content-Type": "application/json", "Retry-After": "2"])!
            self.client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            self.client?.urlProtocol(self, didLoad: (try? json.data()) ?? Data())
            self.client?.urlProtocolDidFinishLoading(self)
        }
        if request.url!.path.hasPrefix("/v2/sync/series") { DispatchQueue.global().asyncAfter(deadline: .now() + .milliseconds(20), execute: send) }
        else { send() }
    }
    private func finishOnce() -> Bool {
        finished.withLock { value in
            guard !value else { return false }
            value = true
            return true
        }
    }
    override func stopLoading() {
        if finishOnce(), request.url!.path.hasPrefix("/v2/sync/series") { Self.state.withLock { $0.activeSeries -= 1 } }
    }
}
private func mockReceipt(_ envelope: JSON) -> JSON {
    .object(["batchID": envelope["batchID"], "datasetEpoch": envelope["datasetEpoch"], "clientSeq": envelope["clientSeq"], "payloadSHA256": envelope["payloadSHA256"],
             "commitRevision": .int(1), "ingestStatus": .string("committed"), "counts": .object(["inserted": envelope["eventCount"],
                "unchanged": .int(0), "deleted": .int(0), "tombstoned": .int(0), "suppressed": .int(0)]),
             "analysisStatus": .string("not_required"), "analysisJobIDs": .array([])])
}
private struct MockUploader: FileUploader {
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        try MockProtocol.state.withLock { state in
            let bytes = try Data(contentsOf: file)
            guard Wire.hash(bytes) == state.envelope["payloadSHA256"].text else { throw SyncFailure.invalid("test_payload_changed") }
            state.uploadCount += 1
            if state.failUpload && !state.lostAfterCommit { state.failUpload = false; throw URLError(.timedOut) }
            state.committed = true
            if state.failUpload { state.failUpload = false; throw URLError(.networkConnectionLost) }
            return (try mockReceipt(state.envelope).data(), 200, nil)
        }
    }
}

@Suite(.serialized)
struct APITests {
    @Test func workoutDeletionRequiresDurableACK() async throws {
        let (_, directory, api, _) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        let id = Wire.id()
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        MockProtocol.state.withLock { $0.controlStatus = 404; $0.controlCode = "workout_not_found" }
        do {
            _ = try await api.deleteWorkoutBundle(workoutID: id, scope: scope)
            Issue.record("404 was accepted as a durable deletion")
        } catch let failure as UploadError {
            #expect(failure.status == 404 && failure.code == "workout_not_found")
        }

        let path = "/v2/sync/workout-bundles/" + id
        let ack: [String: JSON] = ["workoutID": .string(id), "ingestStatus": .string("deleted"),
                                   "alreadyDeleted": .bool(false), "commitRevision": .int(7)]
        // A successful HTTP response with an invalid/mismatched receipt also
        // cannot confirm deletion or authorize releasing the staged bundle.
        for invalid in [JSON.object([:]), .object(ack.merging(["workoutID": .string(Wire.id())]) { _, new in new }),
                        .object(ack.merging(["ingestStatus": .string("committed")]) { _, new in new }),
                        .object(ack.merging(["commitRevision": .int(0)]) { _, new in new }),
                        .object(ack.merging(["alreadyDeleted": .null]) { _, new in new })] {
            MockProtocol.state.withLock { $0.controlStatus = 200; $0.replies[path] = invalid }
            do {
                _ = try await api.deleteWorkoutBundle(workoutID: id, scope: scope)
                Issue.record("invalid deletion ACK was accepted")
            } catch SyncFailure.receiptMismatch { }
        }
        for alreadyDeleted in [false, true] {
            let expected = JSON.object(ack.merging(["alreadyDeleted": .bool(alreadyDeleted)]) { _, new in new })
            MockProtocol.state.withLock { $0.replies[path] = expected }
            let result = try await api.deleteWorkoutBundle(workoutID: id, scope: scope)
            #expect(result == expected)
        }
    }

    private func setup(lostAfterCommit: Bool) async throws -> (OutboxStore, URL, RAIAPIClient, PendingPage) {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-api-test-" + Wire.id())
        let store = try OutboxStore(directory: directory)
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let stream = try await store.stream(type: "HKQuantityTypeIdentifierHeartRate", query: "synthetic")
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [], anchor: Data([42]), store: store)
        try await store.stage(page)
        MockProtocol.state.withLock { state in
            state = MockState(); state.lostAfterCommit = lostAfterCommit
            for op in page.operations { if case .request(let request) = op { state.replies[request.path] = .object(request.expected) } }
        }
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [MockProtocol.self]
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic-test-token", session: URLSession(configuration: config), uploader: MockUploader())
        return (store, directory, api, page)
    }
    @Test func commitResponseLossAndProcessReconstruction() async throws {
        let (store, directory, api, original) = try await setup(lostAfterCommit: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        try await OutboxPipeline(store: store, api: api).drain()
        let failed = await store.snapshot()
        #expect(failed.pages[0].id == original.id); #expect(failed.pages[0].cursor == 1)
        #expect(failed.streams.values.first?.deliveredAnchor == nil)
        #expect(MockProtocol.state.withLock { $0.committed && $0.uploadCount == 1 })
        let reopened = try OutboxStore(directory: directory); try await reopened.retryBlocked()
        try await OutboxPipeline(store: reopened, api: api).drain()
        let delivered = await reopened.snapshot()
        #expect(delivered.pages.isEmpty); #expect(delivered.streams.values.first?.deliveredAnchor == Data([42]))
        #expect(delivered.streams.values.first?.nextSequence == 2)
        #expect(MockProtocol.state.withLock { $0.uploadCount == 1 }) // GET receipt, no second PUT.
    }
    @Test func timeoutBeforeCommitRetriesSameBodyAndIdentity() async throws {
        let (store, directory, api, original) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        try await OutboxPipeline(store: store, api: api).drain()
        #expect(!MockProtocol.state.withLock { $0.committed })
        #expect(await store.snapshot().pages[0].id == original.id)
        try await store.retryBlocked(); try await OutboxPipeline(store: store, api: api).drain()
        #expect(await store.snapshot().pages.isEmpty)
        #expect(MockProtocol.state.withLock { $0.uploadCount == 2 })
    }
    @Test func missingReferencedPayloadCannotAdvanceAnchorAndRetrySurvivesGC() async throws {
        let (store, directory, api, page) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        guard case .batch(_, let file) = page.operations[1] else { Issue.record("missing batch"); return }
        let path = directory.appendingPathComponent(file), bytes = try Data(contentsOf: path)
        try FileManager.default.removeItem(at: path)
        try await OutboxPipeline(store: store, api: api).drain()
        let blocked = await store.snapshot()
        #expect(blocked.streams.values.first?.deliveredAnchor == nil)
        #expect(blocked.streams.values.first?.nextSequence == 1)
        #expect(!MockProtocol.state.withLock { $0.committed })
        #expect(try await store.collectGarbage(dryRun: false) == 0)
        try bytes.write(to: path)
        try await store.retryBlocked(); try await OutboxPipeline(store: store, api: api).drain()
        // First transport fails before commit; its original file/identity stays.
        try await store.retryBlocked(); try await OutboxPipeline(store: store, api: api).drain()
        #expect(await store.snapshot().streams.values.first?.deliveredAnchor == Data([42]))
        _ = try await store.collectGarbage(dryRun: false)
        try await OutboxPipeline(store: store, api: api).drain()
        #expect(await store.snapshot().pages.isEmpty)
        #expect(MockProtocol.state.withLock { $0.uploadCount == 2 })
    }
    @Test func serviceUnavailablePreservesCandidateAndBackoff() async throws {
        let (store, directory, api, _) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        MockProtocol.state.withLock { $0.controlStatus = 503 }
        try await OutboxPipeline(store: store, api: api).drain()
        let failed = await store.snapshot()
        #expect(failed.pages[0].error?.retryable == true); #expect(failed.pages[0].nextAttempt > Date())
        #expect(failed.streams.values.first?.deliveredAnchor == nil)
    }
    @Test func rateLimitStopsPassInsteadOfHammeringEveryPage() async throws {
        let (store, directory, api, _) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        MockProtocol.state.withLock { $0.controlStatus = 429 }
        let pipeline = OutboxPipeline(store: store, api: api)
        try await pipeline.drain()
        let paused = await pipeline.pausedUntil
        #expect(paused != nil && paused! > Date())
        let failed = await store.snapshot()
        // The pass stopped at the first shared-bucket 429; the page keeps its
        // exact cursor and can resume without a second request in this pass.
        #expect(failed.pages[0].cursor == 0)
        #expect(failed.pages[0].retryCount == 1)
        #expect(MockProtocol.state.withLock { $0.uploadCount == 0 })
    }
    @Test func conflictDoesNotAutomaticallyRetry() async throws {
        let (store, directory, api, _) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        MockProtocol.state.withLock { $0.controlStatus = 409 }
        let pipeline = OutboxPipeline(store: store, api: api)
        try await pipeline.drain(); try await pipeline.drain()
        let failed = await store.snapshot()
        #expect(failed.pages[0].retryCount == 1); #expect(failed.pages[0].error?.retryable == false)
        #expect(failed.streams.values.first?.nextSequence == 1)
    }
    @Test func wrongSealedManifestCannotAdvanceAnchor() async throws {
        let (store, directory, api, page) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        MockProtocol.state.withLock { state in
            state.failUpload = false
            for op in page.operations {
                if case .request(let request) = op, request.path.contains("/pages/") {
                    var response = request.expected, receipt = response["receipt"]!.fields
                    receipt["manifestSHA256"] = .string(String(repeating: "0", count: 64)); response["receipt"] = .object(receipt)
                    state.replies[request.path] = .object(response)
                }
            }
        }
        try await OutboxPipeline(store: store, api: api).drain()
        let state = await store.snapshot()
        #expect(state.streams.values.first?.nextSequence == 2); #expect(state.streams.values.first?.deliveredAnchor == nil)
        #expect(state.pages[0].error?.retryable == false)
    }
    @Test func seriesConcurrencyKeepsReceiptsAndWaitsForPageSeal() async throws {
        let (store, directory, api, original) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        // Finish the setup page, then build a real BatchBuilder page with its
        // ordinary trailing GET /pages receipt and eight independent series.
        MockProtocol.state.withLock { $0.failUpload = false }
        try await OutboxPipeline(store: store, api: api).drain()
        let stream = await store.snapshot().streams.values.first!
        let scope = Scope(serviceID: Wire.id(), datasetID: MockProtocol.state.withLock { $0.envelope["datasetID"].text! }, datasetEpoch: MockProtocol.state.withLock { $0.envelope["datasetEpoch"].text! }, collectorEpoch: MockProtocol.state.withLock { $0.envelope["collectorEpoch"].text! })
        let ids = (0..<8).map { _ in Wire.id() }
        var supplements: [OutboxOperation] = []
        for id in ids {
            for (method, path) in [("POST", "/v2/sync/series"), ("PUT", "/v2/sync/series/" + id + "/chunks/0"), ("POST", "/v2/sync/series/" + id + "/seal")] {
                let file = path == "/v2/sync/series" ? try await store.savePayload(JSON.object(["seriesID": .string(id)]).data()) : nil
                supplements.append(.request(.init(method: method, path: path, file: file, expected: ["seriesID": .string(id)])))
            }
        }
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [], anchor: Data([43]), store: store, supplements: supplements)
        try await store.stage(page)
        // Registrations share a URL; the mock echoes their request bodies.
        // Per-ID paths return the same frozen receipt on every attempt.
        MockProtocol.state.withLock { state in
            state.requests = []; state.rejectPageReceipt = true
            state.failOnce = "/v2/sync/series/" + ids[0] + "/chunks/0"
            for op in page.operations { if case .request(let r) = op, r.path != "/v2/sync/series" { state.replies[r.path] = .object(r.expected) } }
        }
        let pipeline = OutboxPipeline(store: store, api: api)
        try await pipeline.drain()
        var saved = await store.snapshot()
        #expect(await pipeline.pausedUntil != nil)
        #expect(saved.streams[stream.type]?.deliveredAnchor == original.candidateAnchor)
        #expect(saved.pages.count == 1 && saved.pages[0].cursor >= 2)
        #expect(MockProtocol.state.withLock { $0.peakSeries > 1 && $0.peakSeries <= 8 })
        let persisted = saved.receipts
        let reopened = try OutboxStore(directory: directory)
        try await reopened.retryBlocked()
        try await OutboxPipeline(store: reopened, api: api).drain()
        saved = await reopened.snapshot()
        #expect(saved.pages.first?.cursor == page.operations.count - 1)
        #expect(saved.streams[stream.type]?.deliveredAnchor == original.candidateAnchor)
        // Every successful series response from the first attempt was reused.
        for (index, op) in page.operations.enumerated() {
            if case .request(let r) = op, r.path != "/v2/sync/series", r.path.contains("/series/"), persisted[page.id + ":" + String(index)] != nil {
                #expect(MockProtocol.state.withLock { $0.requests.filter { $0 == r.method + " " + r.path }.count == 1 })
            }
        }
        MockProtocol.state.withLock { $0.rejectPageReceipt = false }
        try await reopened.retryBlocked()
        try await OutboxPipeline(store: reopened, api: api).drain()
        saved = await reopened.snapshot()
        #expect(saved.pages.isEmpty)
        #expect(saved.streams[stream.type]?.deliveredAnchor == Data([43]))
        guard case .request(let pageReceipt) = page.operations.last! else { Issue.record("missing page receipt"); return }
        #expect(MockProtocol.state.withLock { $0.requests.last == "GET " + pageReceipt.path })
    }
    @Test func lateAssociationWakesOnlyAfterNewDurableFacts() async throws {
        let (store, directory, api, _) = try await setup(lostAfterCommit: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        let expected: [String: JSON] = ["sampleType": .string("HKQuantityTypeIdentifierHeartRate"), "sampleUUID": .string(Wire.id()), "workoutUUID": .string(Wire.id()), "ingestStatus": .string("committed")]
        let key = "association:synthetic-workout:heart-rate"
        let dependent = PendingPage(anchorKey: key, candidateAnchor: Data([99]), operations: [.request(.init(method: "POST", path: "/v2/sync/associations", expected: expected))])
        try await store.stage(dependent)
        MockProtocol.state.withLock { $0.failUpload = false; $0.associationUnavailable = true; $0.replies["/v2/sync/associations"] = .object(expected) }
        let pipeline = OutboxPipeline(store: store, api: api)
        try await pipeline.drain()
        let waiting = await store.snapshot()
        #expect(waiting.pages.first?.waitingForFactGeneration == waiting.factGeneration)
        MockProtocol.state.withLock { $0.associationUnavailable = false }
        try await pipeline.drain()
        #expect(await store.snapshot().localAnchors[key] == nil)
        let state = await store.snapshot(), stream = state.streams.values.first!
        let fixtureScope = Scope(serviceID: Wire.id(), datasetID: MockProtocol.state.withLock { $0.envelope["datasetID"].text! }, datasetEpoch: MockProtocol.state.withLock { $0.envelope["datasetEpoch"].text! }, collectorEpoch: MockProtocol.state.withLock { $0.envelope["collectorEpoch"].text! })
        let fact: JSON = .object(["op": .string("upsert"), "kind": .string("sample"), "type": .string(stream.type), "uuid": expected["sampleUUID"]!,
            "startUs": .int(1), "endUs": .int(1), "source": .object(["bundleID": .string("synthetic")]), "quantity": .object(["value": .int(120), "unit": .string("count/min")])])
        let next = try await BatchBuilder.page(scope: fixtureScope, stream: stream, events: [fact], anchor: Data([43]), store: store)
        try await store.stage(next)
        MockProtocol.state.withLock { state in for operation in next.operations { if case .request(let request) = operation { state.replies[request.path] = .object(request.expected) } } }
        try await pipeline.drain()
        #expect(await store.snapshot().pages.isEmpty)
        #expect(await store.snapshot().localAnchors[key] == Data([99]))
    }

    @Test func taskScopedDrainLeavesOtherTasksAndLegacyPagesUntouched() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-task-drain-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let type = "HKWorkoutTypeIdentifier"
        let streamA = try await store.stream(key: "workout:A:" + type, type: type, query: "per-workout:A")
        let streamB = try await store.stream(key: "workout:B:" + type, type: type, query: "per-workout:B")
        let eventA: JSON = .object(["op": .string("upsert"), "kind": .string("workout"), "type": .string(type), "uuid": .string(Wire.id()),
            "startUs": .int(1), "endUs": .int(2), "source": .object(["bundleID": .string("synthetic")]),
            "workout": .object(["activityType": .int(37), "durationUs": .int(1)])])
        let eventB: JSON = .object(["op": .string("upsert"), "kind": .string("workout"), "type": .string(type), "uuid": .string(Wire.id()),
            "startUs": .int(3), "endUs": .int(4), "source": .object(["bundleID": .string("synthetic")]),
            "workout": .object(["activityType": .int(37), "durationUs": .int(1)])])
        let pageA = try await BatchBuilder.page(scope: scope, stream: streamA, events: [eventA], anchor: Data([1]), store: store,
            pageKey: "workout:A:" + type, taskID: "A")
        let pageB = try await BatchBuilder.page(scope: scope, stream: streamB, events: [eventB], anchor: Data([2]), store: store,
            pageKey: "workout:B:" + type, taskID: "B")
        let legacy = PendingPage(anchorKey: "legacy:page", candidateAnchor: Data([3]), operations: [
            .request(.init(method: "POST", path: "/v2/synthetic", expected: ["ok": .bool(true)]))
        ])
        try await store.stage(pageA); try await store.stage(pageB); try await store.stage(legacy)
        MockProtocol.state.withLock { state in
            state = MockState(); state.failUpload = false
            for operation in pageA.operations {
                if case .request(let request) = operation, request.path != "/v2/sync/batches" {
                    state.replies[request.path] = .object(request.expected)
                }
            }
            state.replies["/v2/synthetic"] = .object(["ok": .bool(true)])
        }
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [MockProtocol.self]
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic-test-token",
                               session: URLSession(configuration: config), uploader: MockUploader())
        try await OutboxPipeline(store: store, api: api).drain(taskID: "A")
        var state = await store.snapshot()
        #expect(state.pages.count == 2)
        #expect(!state.pages.contains { $0.taskID == "A" })
        #expect(state.pages.contains { $0.taskID == "B" })
        #expect(state.pages.contains { $0.isLegacy })
        let untouchedB = try #require(state.pages.first { $0.taskID == "B" })
        #expect(untouchedB.cursor == 0 && untouchedB.error == nil)
        MockProtocol.state.withLock { state in
            for operation in pageB.operations {
                if case .request(let request) = operation, request.path != "/v2/sync/batches" {
                    state.replies[request.path] = .object(request.expected)
                }
            }
        }
        try await OutboxPipeline(store: store, api: api).drain()
        state = await store.snapshot()
        #expect(state.pages.isEmpty)
    }
}

// MARK: - R4 transport lane: gzip capability and Content-Encoding placement

/// Records the exact request the transport lane builds so the header
/// placement can be asserted (compression lives in the file part only).
private final class HeaderRecordingUploader: FileUploader, Sendable {
    let box = Mutex<[URLRequest]>([])
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        box.withLock { $0.append(request) }
        return UploadResponse(data: Data("{\"synthetic\":true}".utf8), status: 200, contentType: "application/json")
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        box.withLock { $0.append(request) }
        return (Data("{\"synthetic\":true}".utf8), 200, nil)
    }
}

extension APITests {
    /// The handshake declares `bundleGzipPart`; the client must remember it so
    /// the bundle lane can fail diagnosably when a server cannot accept gzip.
    @Test func handshakeRecordsBundleGzipCapability() async throws {
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [MockProtocol.self]
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic-test-token",
                               session: URLSession(configuration: config))
        // Before any handshake the capability is unknown, not "unsupported".
        #expect(api.bundleGzipCapability == .unknown)

        MockProtocol.state.withLock { state in
            state = MockState()
            state.replies["/v2/handshake"] = .object(["capabilities": .object([
                "workoutBundles": .bool(true), "bundleProtocolVersion": .int(3), "bundleGzipPart": .bool(true)])])
        }
        let hello = try await api.call("GET", "/v2/handshake")
        #expect(hello["capabilities"]["bundleGzipPart"] == .bool(true))
        #expect(api.bundleGzipCapability == .declared(true))
        #expect(api.capabilities.declared?["workoutBundles"] == .bool(true))

        // A handshake that ran but omits the key is a declared "no".
        let other = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic-test-token",
                                 session: URLSession(configuration: config))
        MockProtocol.state.withLock { state in
            state = MockState()
            state.replies["/v2/handshake"] = .object(["capabilities": .object(["workoutBundles": .bool(true)])])
        }
        _ = try await other.call("GET", "/v2/handshake")
        #expect(other.bundleGzipCapability == .declared(false))
        MockProtocol.state.withLock { $0 = MockState() }
    }

    /// `Content-Encoding: gzip` belongs to the batch lane only; a bundle POST
    /// carries multipart framing and compresses inside the file part, because
    /// the server rejects a top-level Content-Encoding with 415.
    @Test func gzipContentEncodingStaysOnTheBatchLane() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-api-gzip-" + Wire.id())
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("payload.bin")
        try Data("{\"synthetic\":true}".utf8).write(to: file)

        MockProtocol.state.withLock { $0 = MockState() }
        let uploader = HeaderRecordingUploader()
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [MockProtocol.self]
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic-test-token",
                               session: URLSession(configuration: config), uploader: uploader)
        _ = try? await api.call("POST", "/v2/sync/workout-bundles", file: file,
                                mediaType: "multipart/form-data; boundary=rai.test", identity: "bundle")
        _ = try? await api.call("PUT", "/v2/sync/batches/synthetic", file: file,
                                mediaType: "application/json", gzip: true, identity: "batch")

        let requests = uploader.box.withLock { $0 }
        #expect(requests.count == 2)
        let bundle = try #require(requests.first)
        #expect(bundle.value(forHTTPHeaderField: "Content-Encoding") == nil)
        #expect(bundle.value(forHTTPHeaderField: "Content-Type") == "multipart/form-data; boundary=rai.test")
        let batch = try #require(requests.last)
        #expect(batch.value(forHTTPHeaderField: "Content-Encoding") == "gzip")
        MockProtocol.state.withLock { $0 = MockState() }
    }
}
