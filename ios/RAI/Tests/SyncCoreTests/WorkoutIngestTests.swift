import Foundation
import Synchronization
import Testing
@testable import SyncCore

// MARK: - Transport doubles

private final class IngestUploader: FileUploader, Sendable {
    struct Call: Sendable {
        let identity: String
        let method: String
        let path: String
        let contentType: String?
        let body: Data
    }
    private let calls = Mutex<[Call]>([])
    private let reply = Mutex<UploadResponse?>(nil)
    private let failure = Mutex<URLError.Code?>(nil)
    private let discards = Mutex<[String]>([])

    var recorded: [Call] { calls.withLock { $0 } }
    var discarded: [String] { discards.withLock { $0 } }
    func answer(_ value: UploadResponse?) { reply.withLock { $0 = value } }
    func fail(with code: URLError.Code?) { failure.withLock { $0 = code } }

    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let body = try Data(contentsOf: file)
        calls.withLock {
            $0.append(Call(identity: identity, method: request.httpMethod ?? "", path: request.url?.path ?? "",
                           contentType: request.value(forHTTPHeaderField: "Content-Type"), body: body))
        }
        if let code = failure.withLock({ $0 }) {
            failure.withLock { $0 = nil }
            throw URLError(code)
        }
        guard let value = reply.withLock({ $0 }) else { throw URLError(.timedOut) }
        return value
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let value = try await response(request, file: file, identity: identity)
        return (value.data, value.status, value.retryAfter)
    }
    func discardCachedResult(_ identity: String) { discards.withLock { $0.append(identity) } }
}

private struct StatusReply: Sendable {
    var status = 200
    var body = Data("{}".utf8)
}

private final class IngestStatusProtocol: URLProtocol, @unchecked Sendable {
    static let reply = Mutex(StatusReply())
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let value = Self.reply.withLock { $0 }
        let response = HTTPURLResponse(url: request.url!, statusCode: value.status, httpVersion: nil,
                                       headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: value.body)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class OfflineProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() { client?.urlProtocol(self, didFailWithError: URLError(.timedOut)) }
    override func stopLoading() {}
}

private final class MetricsRecorder: Sendable {
    private let events = Mutex<[(String, Double)]>([])
    var metrics: SyncMetrics {
        SyncMetrics { [self] name, value in events.withLock { $0.append((name, value)) } }
    }
    func values(_ name: String) -> [Double] { events.withLock { $0.filter { $0.0 == name }.map(\.1) } }
}

/// Splits a multipart body into its part headers and exact payload bytes.
private func ingestFilePart(_ body: Data) -> (headers: String, payload: Data)? {
    guard let separator = body.range(of: Data("\r\n\r\n".utf8)),
          let trailer = body.range(of: Data("\r\n--".utf8), options: .backwards),
          trailer.lowerBound > separator.upperBound else { return nil }
    return (String(decoding: body[body.startIndex..<separator.lowerBound], as: UTF8.self),
            body[separator.upperBound..<trailer.lowerBound])
}

private func ingestAPI(_ uploader: any FileUploader, metrics: SyncMetrics = .init(),
                       protocols: [AnyClass] = []) -> RAIAPIClient {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = protocols
    return RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic",
                        session: URLSession(configuration: config), uploader: uploader, metrics: metrics)
}

// MARK: - Fixtures

private struct IngestFixture: Sendable {
    let directory: URL
    let store: OutboxStore
    let scope: Scope
    let id: String
    let name: String
    let boundary: String
    let raw: Data
}

private func ingestPayload(_ id: String, _ scope: Scope, marker: Int64 = 0) throws -> Data {
    try JSON.object([
        "protocolVersion": .int(4), "workoutID": .string(id),
        "datasetID": .string(scope.datasetID), "datasetEpoch": .string(scope.datasetEpoch),
        "collectorEpoch": .string(scope.collectorEpoch),
        "syntheticMarker": .int(marker), "samples": .array([]),
    ]).data()
}

private func ingestPayloadV6(_ id: String, _ scope: Scope) throws -> Data {
    try JSON.object([
        "protocolVersion": .int(6), "workoutID": .string(id),
        "datasetID": .string(scope.datasetID), "datasetEpoch": .string(scope.datasetEpoch),
        "collectorEpoch": .string(scope.collectorEpoch),
        "startUs": .int(1_800_000_000_000_000), "endUs": .int(1_800_000_060_000_000),
        "source": .object(["bundleID": .string("com.apple.health")]),
        "workout": .object(["activityType": .int(37)]),
        "streams": .array([]), "routes": .array([]),
    ]).data()
}

private func makeIngestFixture(marker: Int64 = 0) async throws -> IngestFixture {
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-ingest-" + Wire.id())
    let store = try OutboxStore(directory: directory)
    let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
    let id = Wire.id()
    try await store.beginTask(id)
    try await store.markIngestLane(id)
    let boundary = "rai.ingest." + Wire.id()
    let raw = try ingestPayload(id, scope, marker: marker)
    let staged = try await store.stageWorkoutIngestPayload(
        id, body: WorkoutBundleAssembler.multipart(bundle: raw, boundary: boundary))
    return IngestFixture(directory: directory, store: store, scope: scope, id: id,
                         name: staged.file, boundary: staged.boundary, raw: raw)
}

private func acceptedReceipt(_ id: String, at: String = "2026-11-09T10:00:00Z") -> JSON {
    .object(["workoutID": .string(id), "ingestStatus": .string("accepted"), "acceptedAt": .string(at)])
}

private func status(_ id: String, _ state: String, extra: [String: JSON] = [:]) throws -> WorkoutIngestStatus {
    var fields: [String: JSON] = ["workoutID": .string(id), "ingestStatus": .string(state)]
    for (key, value) in extra { fields[key] = value }
    return try WorkoutIngestStatus(.object(fields), workoutID: id)
}

// MARK: - Tests

@Suite(.serialized)
struct WorkoutIngestTests {
    @Test func fixedPayloadStagingRecordsExactIdentity() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let task = try #require(await fixture.store.task(fixture.id))
        #expect(fixture.name == "workout-ingest." + fixture.id + ".multipart.json")
        #expect(fixture.boundary == task.bundleBoundary)
        #expect(task.ingestMode == OutboxStore.ingestModeUUID)
        #expect(task.phase == .uploading)
        #expect(task.bundleSHA256 == nil)
        #expect(task.uploadReceipt == nil)
        #expect(task.ingestStatus == nil)
        let url = try await fixture.store.fileURL(fixture.name)
        #expect(FileManager.default.fileExists(atPath: url.path))
        #expect(OutboxStore.ingestPayloadBoundary(file: url) == fixture.boundary)
        // The stored bytes are the plaintext multipart body the server decodes.
        #expect(try WorkoutIngestDelivery.payloadBody(file: url, boundary: fixture.boundary) == fixture.raw)
        // An invalid UUID never produces a fixed payload name.
        #expect(OutboxStore.ingestPayloadName(fixture.id.uppercased()) == nil)
        #expect(OutboxStore.ingestPayloadName("00000000-0000-0000-0000-000000000000") == nil)
    }

    @Test func killAfterRenameBeforeStateCommitIsAdopted() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-ingest-crash-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id(), boundary = "rai.ingest." + Wire.id()
        try await store.beginTask(id)
        let bytes = WorkoutBundleAssembler.multipart(bundle: try ingestPayload(id, scope), boundary: boundary)
        // A kill after the atomic rename, before the task/state commit: only the
        // fixed file exists. Startup reconciliation must adopt it by UUID.
        try bytes.write(to: directory.appendingPathComponent("workout-ingest." + id + ".multipart.json"))
        try await store.reconcileTasks()
        let task = try #require(await store.task(id))
        #expect(task.ingestMode == OutboxStore.ingestModeUUID)
        #expect(task.bundleFile == "workout-ingest." + id + ".multipart.json")
        #expect(task.bundleBoundary == boundary)
        #expect(task.phase == .uploading)

        // The fixed name must never sit in the reclamation list while it is
        // authority again.
        #expect(!(await store.snapshot().reclaimFiles ?? []).contains(task.bundleFile!))
        let file = try await store.fileURL(task.bundleFile!)
        #expect(FileManager.default.fileExists(atPath: file.path))
    }

    @Test func corruptFixedPayloadBecomesRetryableInterruption() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-ingest-corrupt-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let id = Wire.id()
        try await store.beginTask(id)
        try await store.markIngestLane(id)
        try Data("--truncated".utf8).write(to: directory.appendingPathComponent("workout-ingest." + id + ".multipart.json"))
        try await store.reconcileTasks()
        let task = try #require(await store.task(id))
        #expect(task.phase == .failed)
        #expect(task.errorCode == "interrupted")
        #expect(task.bundleFile == nil)
        #expect(try await store.recoverWorkoutIngestPayload(id) == nil)
    }

    @Test func acceptanceCommitsReceiptAndCleanupAuthorizationTogether() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try await fixture.store.acknowledgeWorkoutIngest(fixture.id, receipt: acceptedReceipt(fixture.id))
        let task = try #require(await fixture.store.task(fixture.id))
        #expect(task.ingestMode == OutboxStore.ingestModeUUID)
        #expect(task.phase == .accepted)
        #expect(task.ingestStatus == "accepted")
        #expect(task.ingestAcceptedAt != nil)
        #expect(task.uploadReceipt != nil)
        #expect(task.bundleFile == nil)
        #expect(task.bundleBoundary == nil)
        #expect((await fixture.store.snapshot().reclaimFiles ?? []).contains(fixture.name))
        // The receipt is durable before the process dies.
        let reopened = try OutboxStore(directory: fixture.directory)
        let restored = try #require(await reopened.task(fixture.id))
        #expect(restored.phase == .accepted)
        #expect(restored.ingestStatus == "accepted")
        #expect(restored.ingestAcceptedAt != nil)
        // A released file is not re-adopted from the fixed name.
        try await reopened.reconcileTasks()
        #expect(await reopened.task(fixture.id)?.phase == .accepted)
        #expect(await reopened.task(fixture.id)?.bundleFile == nil)
    }

    @Test func serverAnswersNeverRewind() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let accepted = try status(fixture.id, "accepted", extra: ["acceptedAt": .string("2026-11-09T10:00:00Z")])
        #expect(try await fixture.store.applyWorkoutIngestStatus(fixture.id, status: accepted))
        #expect(try await fixture.store.applyWorkoutIngestStatus(fixture.id, status: accepted) == false)
        let rejected = try status(fixture.id, "rejected", extra: ["errorCode": .string("workout_ingest_mismatch")])
        #expect(try await fixture.store.applyWorkoutIngestStatus(fixture.id, status: rejected))
        #expect(await fixture.store.task(fixture.id)?.phase == .failed)
        #expect(await fixture.store.task(fixture.id)?.errorCode == "workout_ingest_mismatch")
        // committed ranks below rejected and cannot resurrect the task.
        let committed = try status(fixture.id, "committed", extra: [
            "commitRevision": .int(3), "analysisStatus": .string("queued"),
            "analysisJobIDs": .array([.string(Wire.id())]),
        ])
        #expect(try await fixture.store.applyWorkoutIngestStatus(fixture.id, status: committed) == false)
        let deleted = try status(fixture.id, "deleted")
        #expect(try await fixture.store.applyWorkoutIngestStatus(fixture.id, status: deleted))
        #expect(await fixture.store.task(fixture.id)?.phase == .deleted)
        #expect((await fixture.store.snapshot().deletedWorkouts).contains(fixture.id))
        #expect(try await fixture.store.applyWorkoutIngestStatus(fixture.id, status: accepted) == false)
    }

    @Test func committedAndAlreadyImportedCompleteWithoutAnalysisJobs() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let committed = try status(fixture.id, "committed", extra: [
            "commitRevision": .int(9), "analysisStatus": .string("not_required"),
        ])
        #expect(try await fixture.store.applyWorkoutIngestStatus(fixture.id, status: committed))
        let committedTask = try #require(await fixture.store.task(fixture.id))
        #expect(committedTask.phase == .acknowledged)
        #expect(committedTask.uploadReceipt != nil)
        #expect(committedTask.acknowledgedAt != nil)
        #expect(committedTask.bundleFile == nil)

        let other = Wire.id()
        try await fixture.store.beginTask(other)
        try await fixture.store.markIngestLane(other)
        let already = try status(other, "already_imported")
        #expect(try await fixture.store.applyWorkoutIngestStatus(other, status: already))
        let alreadyTask = try #require(await fixture.store.task(other))
        #expect(alreadyTask.phase == .acknowledged)
        #expect(alreadyTask.ingestStatus == "already_imported")
        #expect(alreadyTask.acknowledgedAt != nil)
    }

    @Test func statusValidationRejectsIncompleteAnswers() throws {
        let id = Wire.id()
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string(id), "ingestStatus": .string("accepted")]))
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try status(id, "committed", extra: ["analysisStatus": .string("not_required")])
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try status(id, "committed", extra: [
                "commitRevision": .int(1), "analysisStatus": .string("queued"), "analysisJobIDs": .array([]),
            ])
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try status(id, "rejected")
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string(Wire.id()), "ingestStatus": .string("deleted")]), workoutID: id)
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string(id.uppercased()), "ingestStatus": .string("deleted")]))
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string("00000000-0000-0000-0000-000000000000"),
                                            "ingestStatus": .string("deleted")]))
        }
        // An acceptance receipt may only carry the two acceptance states.
        #expect(throws: SyncFailure.receiptMismatch) { try WorkoutIngestStatus(receipt: acceptedReceipt(id), workoutID: Wire.id()) }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(receipt: .object(["workoutID": .string(id), "ingestStatus": .string("accepted")]), workoutID: id)
        }
        #expect(try WorkoutIngestStatus(receipt: acceptedReceipt(id), workoutID: id).state == .accepted)
        #expect(try WorkoutIngestStatus(receipt: .object(["workoutID": .string(id),
                                                         "ingestStatus": .string("already_imported")]), workoutID: id).state == .alreadyImported)
    }

    @Test func statusFetchDistinguishesAbsentFromUnavailable() async throws {
        defer { IngestStatusProtocol.reply.withLock { $0 = StatusReply() } }
        let api = ingestAPI(IngestUploader(), protocols: [IngestStatusProtocol.self])
        let id = Wire.id()
        IngestStatusProtocol.reply.withLock {
            $0 = StatusReply(status: 404, body: try! JSON.object(["code": .string("workout_ingest_not_found")]).data())
        }
        #expect(try await WorkoutIngestDelivery.status(id, api: api) == nil)
        IngestStatusProtocol.reply.withLock {
            $0 = StatusReply(status: 503, body: try! JSON.object(["code": .string("workout_uuid_ingest_paused")]).data())
        }
        do {
            _ = try await WorkoutIngestDelivery.status(id, api: api)
            Issue.record("a paused service was mistaken for 'not received'")
        } catch let failure as UploadError {
            #expect(failure.status == 503 && failure.code == "workout_uuid_ingest_paused")
        }
        IngestStatusProtocol.reply.withLock {
            $0 = StatusReply(status: 200, body: (try! acceptedReceipt(id).data()))
        }
        let accepted = try #require(try await WorkoutIngestDelivery.status(id, api: api))
        #expect(accepted.state == .accepted)
        #expect(accepted.acceptedAt != nil)
    }

    @Test func deliveryPostsOneGzipPartWithUUIDIdentity() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let uploader = IngestUploader()
        uploader.answer(UploadResponse(data: try acceptedReceipt(fixture.id).data(), status: 200,
                                       contentType: "application/json"))
        let recorder = MetricsRecorder()
        let api = ingestAPI(uploader, metrics: recorder.metrics)
        let accepted = try await WorkoutIngestDelivery(store: fixture.store, api: api)
            .accept(fixture.id, scope: fixture.scope)
        #expect(accepted)
        let calls = uploader.recorded
        #expect(calls.count == 1)
        let call = try #require(calls.first)
        #expect(call.method == "POST")
        #expect(call.path == "/v2/sync/workout-ingests/" + fixture.id)
        #expect(call.identity == "workout-ingest:" + fixture.scope.datasetID + ":" + fixture.id)
        #expect(call.contentType == "multipart/form-data; boundary=" + fixture.boundary)
        #expect(call.body.starts(with: Data(("--" + fixture.boundary + "\r\n").utf8)))
        let part = try #require(ingestFilePart(call.body))
        #expect(part.headers.contains("Content-Disposition: form-data; name=\"file\""))
        #expect(part.headers.contains("filename=\"workout.json.gz\""))
        #expect(part.headers.contains("Content-Type: application/gzip"))
        #expect(part.payload.prefix(2) == Data([0x1F, 0x8B]))
        #expect(part.payload == (try Wire.gzip(fixture.raw)))
        #expect(recorder.values("workout_ingest_post_attempt") == [1])
        #expect(recorder.values("workout_ingest_post_success") == [1])
        #expect(uploader.discarded == [call.identity])
        let task = try #require(await fixture.store.task(fixture.id))
        #expect(task.phase == .accepted)
        #expect(task.ingestStatus == "accepted")
        #expect(task.bundleFile == nil)
        #expect((await fixture.store.snapshot().reclaimFiles ?? []).contains { $0.hasSuffix(".multipart.gz") })
    }

    @Test func retryReusesDerivedWireWithoutReDeriving() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let uploader = IngestUploader()
        uploader.fail(with: .timedOut)
        let recorder = MetricsRecorder()
        // The ambiguous POST triggers one status probe; an offline transport
        // leaves the failure retryable without inventing an answer.
        let api = ingestAPI(uploader, metrics: recorder.metrics, protocols: [OfflineProtocol.self])
        let delivery = WorkoutIngestDelivery(store: fixture.store, api: api)
        await #expect(throws: UploadError.self) { try await delivery.accept(fixture.id, scope: fixture.scope) }
        #expect(await fixture.store.task(fixture.id)?.phase == .uploading)
        #expect(await fixture.store.task(fixture.id)?.bundleFile != nil)

        uploader.answer(UploadResponse(data: try acceptedReceipt(fixture.id).data(), status: 200,
                                       contentType: "application/json"))
        let accepted = try await delivery.accept(fixture.id, scope: fixture.scope,
                                                 now: Date().addingTimeInterval(120))
        #expect(accepted)
        let calls = uploader.recorded
        #expect(calls.count == 2)
        #expect(calls[0].body == calls[1].body)
        #expect(recorder.values("workout_ingest_gzip_derive_ms").count == 1)
        #expect(try await fixture.store.task(fixture.id)?.phase == .accepted)
    }

    @Test func uuidLaneDeliversProtocolSixPayloadAndRecordsItsVersion() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-ingest-v6-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        try await store.beginTask(id)
        try await store.markIngestLane(id)
        let raw = try ingestPayloadV6(id, scope)
        let boundary = "rai.ingest." + Wire.id()
        _ = try await store.stageWorkoutIngestPayload(
            id, body: WorkoutBundleAssembler.multipart(bundle: raw, boundary: boundary), payloadVersion: 6)
        #expect(await store.task(id)?.payloadVersion == 6)

        let uploader = IngestUploader()
        uploader.answer(UploadResponse(data: try acceptedReceipt(id).data(), status: 200,
                                       contentType: "application/json"))
        let accepted = try await WorkoutIngestDelivery(store: store, api: ingestAPI(uploader))
            .accept(id, scope: scope)
        #expect(accepted)
        #expect(uploader.recorded.count == 1)
        #expect(await store.task(id)?.phase == .accepted)
    }

    @Test func recordedPayloadVersionRejectsADifferentWireVersion() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-ingest-v6-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        try await store.beginTask(id)
        try await store.markIngestLane(id)
        let boundary = "rai.ingest." + Wire.id()
        _ = try await store.stageWorkoutIngestPayload(
            id, body: WorkoutBundleAssembler.multipart(bundle: try ingestPayloadV6(id, scope), boundary: boundary),
            payloadVersion: 4)
        let uploader = IngestUploader()
        do {
            _ = try await WorkoutIngestDelivery(store: store, api: ingestAPI(uploader))
                .accept(id, scope: scope)
            Issue.record("v6 bytes recorded as v4 must not be uploaded")
        } catch let failure as UploadError {
            #expect(failure.code == "bundle_session_mismatch")
        }
        #expect(uploader.recorded.isEmpty)
    }

    @Test func claimEnforcesBackoffAndServerAnswerRemovesAuthority() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let first = try #require(await fixture.store.claimIngest(fixture.id))
        #expect(first.phase == .uploading)
        #expect(first.attemptCount == 1)
        // An in-flight claim is exclusive and the next attempt is delayed.
        #expect(try await fixture.store.claimIngest(fixture.id) == nil)
        await fixture.store.endBundleAttempt(fixture.id)
        #expect(try await fixture.store.claimIngest(fixture.id) == nil)
        #expect(try await fixture.store.claimIngest(fixture.id, now: Date().addingTimeInterval(120)) != nil)
        await fixture.store.endBundleAttempt(fixture.id)

        try await fixture.store.failIngest(fixture.id, error: UploadError(code: "network_-1001"))
        #expect(await fixture.store.task(fixture.id)?.phase == .uploading)
        let retryAfter = try #require(await fixture.store.task(fixture.id)?.nextAttemptAt)
        #expect(retryAfter > Date())
        try await fixture.store.failIngest(fixture.id, error: UploadError(code: "workout_uuid_ingest_required", status: 409))
        #expect(await fixture.store.task(fixture.id)?.phase == .failed)
        #expect(await fixture.store.task(fixture.id)?.errorCode == "workout_uuid_ingest_required")
        #expect(try await fixture.store.claimIngest(fixture.id, now: Date().addingTimeInterval(10_000)) == nil)

        try await fixture.store.acknowledgeWorkoutIngest(fixture.id, receipt: acceptedReceipt(fixture.id))
        #expect(try await fixture.store.claimIngest(fixture.id, now: Date().addingTimeInterval(20_000)) == nil)
    }

    @Test func discardOnlyBeforeServerAnswer() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        try await fixture.store.discardIngestPayload(fixture.id)
        let task = try #require(await fixture.store.task(fixture.id))
        #expect(task.phase == .preparing)
        #expect(task.bundleFile == nil)
        #expect((await fixture.store.snapshot().reclaimFiles ?? []).contains(fixture.name))
        let rebuilt = try await fixture.store.stageWorkoutIngestPayload(
            fixture.id, body: WorkoutBundleAssembler.multipart(bundle: fixture.raw, boundary: fixture.boundary))
        #expect(rebuilt.file == fixture.name)
        #expect(!(await fixture.store.snapshot().reclaimFiles ?? []).contains(fixture.name))

        try await fixture.store.acknowledgeWorkoutIngest(fixture.id, receipt: acceptedReceipt(fixture.id))
        await #expect(throws: SyncFailure.self) { try await fixture.store.discardIngestPayload(fixture.id) }
        await #expect(throws: SyncFailure.self) {
            try await fixture.store.setIngestBundle(fixture.id, file: fixture.name, boundary: fixture.boundary)
        }
        // Legacy destructive writers cannot touch the UUID lane.
        await #expect(throws: SyncFailure.self) {
            try await fixture.store.setBundle(fixture.id, file: fixture.name, sha256: "synthetic", boundary: fixture.boundary)
        }
    }

    @Test func uuidLaneReleaseRequiresAnAnswerAndDeliversExactlyOnce() async throws {
        let fixture = try await makeIngestFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        await #expect(throws: SyncFailure.localState("bundle_not_acknowledged")) {
            try await fixture.store.releaseBundle(fixture.id)
        }
        let uploader = IngestUploader()
        uploader.answer(UploadResponse(data: try acceptedReceipt(fixture.id).data(), status: 200,
                                       contentType: "application/json"))
        let delivery = WorkoutIngestDelivery(store: fixture.store, api: ingestAPI(uploader))
        #expect(try await delivery.accept(fixture.id, scope: fixture.scope))
        // A duplicate explicit sync refreshes status only; it cannot build or
        // send a second payload for the same UUID.
        try await fixture.store.beginTask(fixture.id)
        #expect(await fixture.store.task(fixture.id)?.phase == .accepted)
        #expect(await fixture.store.task(fixture.id)?.bundleFile == nil)
        #expect(try await delivery.accept(fixture.id, scope: fixture.scope,
                                          now: Date().addingTimeInterval(120)) == false)
        #expect(uploader.recorded.count == 1)
        try await fixture.store.releaseBundle(fixture.id)
        #expect(await fixture.store.task(fixture.id)?.bundleFile == nil)
    }
}
