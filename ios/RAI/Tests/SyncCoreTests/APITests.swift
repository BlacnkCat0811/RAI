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
    var associationUnavailable = false
    var replies: [String: JSON] = [:]
}
// URLProtocol's superclass requires this conformance. Mutable test state is
// protected by Mutex; production code does not use unchecked Sendable.
private final class MockProtocol: URLProtocol, @unchecked Sendable {
    static let state = Mutex(MockState())
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let (status, json) = Self.state.withLock { state -> (Int, JSON) in
            if state.controlStatus != 200 { return (state.controlStatus, .object(["code": .string("temporarily_unavailable")])) }
            let path = request.url!.path
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
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: ["Content-Type": "application/json", "Retry-After": "2"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: (try? json.data()) ?? Data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
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
}
