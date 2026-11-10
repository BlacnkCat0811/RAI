import Foundation
import Synchronization
import Testing
@testable import SyncCore

// MARK: - Transport double

private struct StoredReply: Sendable {
    var status: Int? = 200
    var body = Data("{}".utf8)
    var contentType: String? = "application/json"
}

private struct CapturedCall: Sendable {
    let method: String
    let path: String
    let contentType: String?
    let body: Data
}

/// Captures the single foreground POST and answers it. `status == nil` behaves
/// like a lost connection, which must be final for this attempt.
private final class IngestProtocol: URLProtocol, @unchecked Sendable {
    private struct State: Sendable {
        var reply = StoredReply()
        var calls: [CapturedCall] = []
    }
    private static let state = Mutex(State())

    static func reset(_ reply: StoredReply = StoredReply()) {
        state.withLock { $0 = State(reply: reply) }
    }
    static var calls: [CapturedCall] { state.withLock { $0.calls } }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        let body = Self.readBody(request)
        let reply = Self.state.withLock { state -> StoredReply in
            state.calls.append(CapturedCall(method: request.httpMethod ?? "",
                                            path: request.url?.path ?? "",
                                            contentType: request.value(forHTTPHeaderField: "Content-Type"),
                                            body: body))
            return state.reply
        }
        guard let status = reply.status else {
            client?.urlProtocol(self, didFailWithError: URLError(.timedOut))
            return
        }
        var headers: [String: String] = [:]
        if let contentType = reply.contentType { headers["Content-Type"] = contentType }
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: reply.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    private static func readBody(_ request: URLRequest) -> Data {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 32 * 1024)
        while true {
            let read = stream.read(&buffer, maxLength: buffer.count)
            if read <= 0 { break }
            data.append(buffer, count: read)
        }
        return data
    }
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

private func ingestAPI(metrics: SyncMetrics = .init()) -> RAIAPIClient {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [IngestProtocol.self]
    return RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic",
                        session: URLSession(configuration: config), metrics: metrics)
}

// MARK: - Fixtures

private func ingestPayloadV6(_ id: String, _ scope: Scope, extra: [String: JSON] = [:]) throws -> Data {
    var fields: [String: JSON] = [
        "protocolVersion": .int(6), "workoutID": .string(id),
        "datasetID": .string(scope.datasetID), "datasetEpoch": .string(scope.datasetEpoch),
        "collectorEpoch": .string(scope.collectorEpoch),
        "startUs": .int(1_800_000_000_000_000), "endUs": .int(1_800_000_060_000_000),
        "source": .object(["bundleID": .string("com.apple.health")]),
        "workout": .object(["activityType": .int(37)]),
        "streams": .array([]), "routes": .array([]),
    ]
    for (key, value) in extra { fields[key] = value }
    return try JSON.object(fields).data()
}

private func acceptedReceipt(_ id: String, at: String = "2026-11-09T10:00:00Z") -> JSON {
    .object(["workoutID": .string(id), "ingestStatus": .string("accepted"), "acceptedAt": .string(at)])
}

private func multipartBoundary(in contentType: String?) -> String? {
    guard let contentType, let range = contentType.range(of: "boundary=") else { return nil }
    return String(contentType[range.upperBound...])
}

// MARK: - Tests

@Suite(.serialized)
struct WorkoutIngestTests {
    @Test func deliveryPostsExactlyOneGzipPartForTheExactWorkout() async throws {
        defer { IngestProtocol.reset() }
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        let raw = try ingestPayloadV6(id, scope)
        IngestProtocol.reset(StoredReply(body: try acceptedReceipt(id).data()))
        let recorder = MetricsRecorder()
        let receipt = try await WorkoutIngestDelivery(api: ingestAPI(metrics: recorder.metrics))
            .accept(raw, workoutID: id, scope: scope)

        #expect(receipt.state == .accepted)
        #expect(receipt.acceptedAt != nil)
        let calls = IngestProtocol.calls
        #expect(calls.count == 1)
        let call = try #require(calls.first)
        #expect(call.method == "POST")
        #expect(call.path == "/v2/sync/workout-ingests/" + id)
        let boundary = try #require(multipartBoundary(in: call.contentType))
        #expect(call.contentType == "multipart/form-data; boundary=" + boundary)
        #expect(call.body.starts(with: Data(("--" + boundary + "\r\n").utf8)))
        let part = try #require(ingestFilePart(call.body))
        #expect(part.headers.contains("Content-Disposition: form-data; name=\"file\""))
        #expect(part.headers.contains("filename=\"workout.json.gz\""))
        #expect(part.headers.contains("Content-Type: application/gzip"))
        #expect(part.payload.prefix(2) == Data([0x1F, 0x8B]))
        #expect(part.payload == (try Wire.gzip(raw)))
        #expect(recorder.values("workout_ingest_post_attempt") == [1])
        #expect(recorder.values("workout_ingest_post_success") == [1])
        #expect(recorder.values("workout_ingest_gzip_wire_bytes") == [Double(call.body.count)])
        #expect(recorder.values("workout_ingest_gzip_derive_ms").count == 1)
    }

    @Test func gzipRatioOverflowFallsBackToPlaintextBeforeSending() async throws {
        defer { IngestProtocol.reset() }
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        let raw = try ingestPayloadV6(id, scope, extra: ["unknownPadding": .string(String(repeating: "a", count: 2 << 20))])
        #expect((try? WorkoutPayloadTransport.gzipWire(raw: raw, boundary: "synthetic")) == nil)
        #expect(raw.count <= Wire.GzipBundle.maxDecodedBytes)
        IngestProtocol.reset(StoredReply(body: try acceptedReceipt(id).data()))
        let recorder = MetricsRecorder()
        let receipt = try await WorkoutIngestDelivery(api: ingestAPI(metrics: recorder.metrics))
            .accept(raw, workoutID: id, scope: scope)

        #expect(receipt.state == .accepted)
        let calls = IngestProtocol.calls
        #expect(calls.count == 1)
        let call = try #require(calls.first)
        let boundary = try #require(multipartBoundary(in: call.contentType))
        let part = try #require(ingestFilePart(call.body))
        #expect(part.headers.contains("filename=\"workout.json\""))
        #expect(part.headers.contains("Content-Type: application/json"))
        #expect(part.payload == raw)
        #expect(recorder.values("workout_ingest_gzip_wire_bytes").isEmpty)
        #expect(recorder.values("workout_ingest_post_attempt") == [1])
        #expect(recorder.values("workout_ingest_post_success") == [1])
        #expect(call.body.starts(with: Data(("--" + boundary + "\r\n").utf8)))
    }

    @Test func alreadyImportedWithoutAnAcceptanceIsAnExplicitFailure() async throws {
        defer { IngestProtocol.reset() }
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        let raw = try ingestPayloadV6(id, scope)
        IngestProtocol.reset(StoredReply(body: try JSON.object([
            "workoutID": .string(id), "ingestStatus": .string("already_imported"),
        ]).data()))
        do {
            _ = try await WorkoutIngestDelivery(api: ingestAPI()).accept(raw, workoutID: id, scope: scope)
            Issue.record("an existing fact without an acceptance was reported as uploaded")
        } catch let failure as UploadError {
            #expect(failure.code == "workout_ingest_missing_receipt")
        }
        #expect(IngestProtocol.calls.count == 1)
    }

    @Test func transportFailureIsFinalAndPostsOnce() async throws {
        defer { IngestProtocol.reset() }
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        let raw = try ingestPayloadV6(id, scope)
        IngestProtocol.reset(StoredReply(status: nil))
        let recorder = MetricsRecorder()
        do {
            _ = try await WorkoutIngestDelivery(api: ingestAPI(metrics: recorder.metrics))
                .accept(raw, workoutID: id, scope: scope)
            Issue.record("a lost connection was shown as a durable acceptance")
        } catch let failure as UploadError {
            #expect(failure.code == "network_-1001")
        }
        #expect(IngestProtocol.calls.count == 1)
        #expect(recorder.values("workout_ingest_post_attempt") == [1])
        #expect(recorder.values("workout_ingest_post_success").isEmpty)
    }

    @Test func pausedServicePropagatesWithoutRetry() async throws {
        defer { IngestProtocol.reset() }
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        let raw = try ingestPayloadV6(id, scope)
        IngestProtocol.reset(StoredReply(status: 503, body: try JSON.object([
            "code": .string("workout_uuid_ingest_paused"),
        ]).data()))
        do {
            _ = try await WorkoutIngestDelivery(api: ingestAPI())
                .accept(raw, workoutID: id, scope: scope)
            Issue.record("a paused service was shown as accepted")
        } catch let failure as UploadError {
            #expect(failure.status == 503)
            #expect(failure.code == "workout_uuid_ingest_paused")
        }
        #expect(IngestProtocol.calls.count == 1)
    }

    @Test func mismatchedOrUnreadableReceiptsAreFailures() async throws {
        defer { IngestProtocol.reset() }
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        let raw = try ingestPayloadV6(id, scope)
        let delivery = WorkoutIngestDelivery(api: ingestAPI())

        IngestProtocol.reset(StoredReply(body: try acceptedReceipt(Wire.id()).data()))
        do {
            _ = try await delivery.accept(raw, workoutID: id, scope: scope)
            Issue.record("a receipt for another Workout was accepted")
        } catch let failure as UploadError {
            #expect(failure.code == "receipt_mismatch")
        }
        #expect(IngestProtocol.calls.count == 1)

        IngestProtocol.reset(StoredReply(body: Data()))
        do {
            _ = try await delivery.accept(raw, workoutID: id, scope: scope)
            Issue.record("an empty HTTP 200 was accepted as durable storage")
        } catch let failure as UploadError {
            #expect(failure.code == "invalid_ack_response")
        }
        #expect(IngestProtocol.calls.count == 1)
    }

    @Test func retryPolicyOnlyRetriesTransientFailures() {
        let transient = [UploadError(code: "network_-1001"), UploadError(code: "x", status: 429, retryAfter: nil),
                         UploadError(code: "x", status: 503), UploadError(code: "x", status: 408)]
        for error in transient { #expect(WorkoutIngestDelivery.retryDelay(for: error, attempt: 0) == 1) }
        #expect(WorkoutIngestDelivery.retryDelay(for: transient[0], attempt: 1) == 3)
        #expect(WorkoutIngestDelivery.retryDelay(for: transient[0], attempt: 2) == 8)
        #expect(WorkoutIngestDelivery.retryDelay(for: transient[0], attempt: 3) == nil)
        #expect(WorkoutIngestDelivery.retryDelay(for: UploadError(code: "x", status: 429, retryAfter: 12), attempt: 0) == 12)
        #expect(WorkoutIngestDelivery.retryDelay(for: UploadError(code: "x", status: 429, retryAfter: 900), attempt: 0) == 30)
        for error in [UploadError(code: "workout_deleted", status: 409), UploadError(code: "unsupported_payload", status: 422),
                      UploadError(code: "authentication_required", retryAfter: 60), UploadError(code: "cancelled")] {
            #expect(WorkoutIngestDelivery.retryDelay(for: error, attempt: 0) == nil)
        }
        #expect(WorkoutIngestDelivery.retryDelay(for: SyncFailure.localState("x"), attempt: 0) == nil)
    }

    @Test func acceptWithRetryResendsAfterTransientFailureThenSucceeds() async throws {
        defer { IngestProtocol.reset() }
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        let id = Wire.id()
        let raw = try ingestPayloadV6(id, scope)
        IngestProtocol.reset(StoredReply(status: nil))
        let delays = Mutex<[TimeInterval]>([])
        let delivery = WorkoutIngestDelivery(api: ingestAPI())
        do {
            let receipt = try await delivery.acceptWithRetry(raw, workoutID: id, scope: scope) { delay in
                delays.withLock { $0.append(delay) }
                // The network comes back after the second wait.
                if delays.withLock({ $0.count }) == 2 {
                    IngestProtocol.reset(StoredReply(body: try acceptedReceipt(id).data()))
                }
            }
            #expect(receipt.state == .accepted)
        }
        #expect(delays.withLock { $0 } == [1, 3])
        // The double was reset when the network "came back", so only the final
        // successful POST is still on record.
        #expect(IngestProtocol.calls.count == 1)

        // A permanent failure is not retried.
        IngestProtocol.reset(StoredReply(status: 409, body: Data(#"{"code":"workout_deleted"}"#.utf8)))
        await #expect(throws: UploadError.self) {
            _ = try await delivery.acceptWithRetry(raw, workoutID: id, scope: scope) { _ in }
        }
        #expect(IngestProtocol.calls.count == 1)
        // Retries stop after three waits.
        IngestProtocol.reset(StoredReply(status: nil))
        await #expect(throws: UploadError.self) {
            _ = try await delivery.acceptWithRetry(raw, workoutID: id, scope: scope) { _ in }
        }
        #expect(IngestProtocol.calls.count == 4)
    }

    @Test func batchStatusesReturnEveryAnswerByUUID() async throws {
        defer { IngestProtocol.reset() }
        let committed = Wire.id(), rejected = Wire.id(), missing = Wire.id()
        let reply: JSON = .object(["items": .array([
            .object(["workoutID": .string(committed), "ingestStatus": .string("committed"),
                     "acceptedAt": .string("2026-11-09T10:00:00Z"), "commitRevision": .int(3)]),
            .object(["workoutID": .string(rejected), "ingestStatus": .string("rejected"), "errorCode": .string("unsupported_payload")]),
            .object(["workoutID": .string(missing), "ingestStatus": .string("not_found")]),
            .object(["workoutID": .string(Wire.id()), "ingestStatus": .string("committed")]),
        ])])
        IngestProtocol.reset(StoredReply(body: try reply.data()))
        let result = try await WorkoutIngestDelivery(api: ingestAPI()).statuses([committed, rejected, missing])
        #expect(result.count == 3)
        #expect(result[committed]?.state == .committed)
        #expect(result[committed]?.acceptedAt != nil)
        #expect(result[rejected]?.errorCode == "unsupported_payload")
        #expect(result[missing]?.state == .notFound)
        #expect(IngestProtocol.calls.last?.path == "/v2/sync/workout-ingests/status")
        #expect(IngestProtocol.calls.last?.method == "POST")
    }

    @Test func acceptanceReceiptsAreStrict() throws {
        let id = Wire.id()
        #expect(throws: SyncFailure.receiptMismatch) { try WorkoutIngestStatus(receipt: acceptedReceipt(id), workoutID: Wire.id()) }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(receipt: .object(["workoutID": .string(id), "ingestStatus": .string("accepted")]), workoutID: id)
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(receipt: .object(["workoutID": .string(id), "ingestStatus": .string("committed")]), workoutID: id)
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(receipt: .object(["workoutID": .string(id.uppercased()),
                                                       "ingestStatus": .string("already_imported")]), workoutID: id.uppercased())
        }
        #expect(try WorkoutIngestStatus(receipt: acceptedReceipt(id), workoutID: id).state == .accepted)
        #expect(throws: SyncFailure.localState("workout_ingest_missing_receipt")) {
            try WorkoutIngestStatus(receipt: .object(["workoutID": .string(id),
                                                       "ingestStatus": .string("already_imported")]), workoutID: id)
        }
    }

    @Test func statusParserStillRejectsIncompleteLegacyAnswers() throws {
        let id = Wire.id()
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string(id), "ingestStatus": .string("accepted")]))
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string(id), "ingestStatus": .string("committed"),
                                             "analysisStatus": .string("not_required")]))
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string(id), "ingestStatus": .string("committed"),
                                             "commitRevision": .int(1), "analysisStatus": .string("queued"),
                                             "analysisJobIDs": .array([])]))
        }
        #expect(throws: SyncFailure.receiptMismatch) {
            try WorkoutIngestStatus(.object(["workoutID": .string(id), "ingestStatus": .string("rejected")]))
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
    }
}
