import Foundation
import Synchronization
import Testing
@testable import SyncCore

private func dailyACK(_ raw: Data) throws -> JSON {
    let body = try JSON.decode(raw)
    var fields: [String: JSON] = ["version": .int(1), "requestSHA256": .string(Wire.hash(raw)), "ingestStatus": .string("committed"), "commitRevision": .int(1)]
    fields["analysisStatus"] = .string("awaiting_model_implementation"); fields["deleteStatus"] = .string("none")
    fields["counts"] = .object(Dictionary(uniqueKeysWithValues: ["inserted", "unchanged", "deleted", "tombstoned", "suppressed"].map { ($0, JSON.int(0)) }))
    for key in ["batchID", "datasetID", "datasetEpoch", "collectorEpoch", "streamID", "clientSeq"] { fields[key] = body[key] }
    return .object(fields)
}
final class DailyUploader: FileUploader, Sendable {
    enum Mode: Sendable { case success, offline, lostACK, internalError, wrongACK }
    struct State { var mode: Mode = .success; var hashes: [String] = []; var discarded: [String] = [] }
    let state = Mutex(State())
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let raw = try Data(contentsOf: file)
        let mode = state.withLock { value in value.hashes.append(Wire.hash(raw)); return value.mode }
        switch mode {
        case .offline, .lostACK: throw URLError(.networkConnectionLost)
        case .internalError: return UploadResponse(data: Data("{\"code\":\"internal_error\"}".utf8), status: 500, contentType: "application/json")
        case .wrongACK: return UploadResponse(data: try JSON.object(["ingestStatus": .string("committed")]).data(), status: 200, contentType: "application/json")
        case .success: return UploadResponse(data: try dailyACK(raw).data(), status: 200, contentType: "application/json")
        }
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) { let r = try await response(request, file: file, identity: identity); return (r.data, r.status, r.retryAfter) }
    func discardCachedResult(_ identity: String) { state.withLock { $0.discarded.append(identity) } }
}
private func dailyFixture() async throws -> (OutboxStore, URL) {
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-daily-" + Wire.id())
    let store = try OutboxStore(directory: directory)
    try await store.bind(Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id()))
    return (store, directory)
}

@Suite struct DailyHealthTests {
    @Test func perTypeAnchorsPendingAndDeletesSurviveRestart() async throws {
        let (store, directory) = try await dailyFixture(); defer { try? FileManager.default.removeItem(at: directory) }
        for (index,type) in DailyHealth.types.enumerated() {
            _ = try await store.dailyFlow(type)
            try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([UInt8(index)]), facts: [], deleted: [Wire.id()], observedAt: Date(), hasMore: false)
            #expect(try await store.dailyFlow(type).anchor == nil)
        }
        let restored = try OutboxStore(directory: directory)
        let uploader = DailyUploader(), api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", uploader: uploader)
        for (index,type) in DailyHealth.types.enumerated() {
            let pending = try #require(try await restored.dailyFlow(type).pending)
            let raw = try Data(contentsOf: await restored.fileURL(pending.file)), body = try JSON.decode(raw)
            #expect(body["deletedUUIDs"].list.count == 1 && body["facts"].list.isEmpty)
            #expect(body["version"].integer == 1 && body["protocolVersion"] == .null)
            #expect(try await DailyHealthDelivery(store: restored, api: api).deliver(type))
            #expect(try await restored.dailyFlow(type).anchor == Data([UInt8(index)]))
            #expect(try await restored.dailyFlow(type).bootstrapComplete)
        }
        let reopened = try OutboxStore(directory: directory)
        #expect(try await reopened.dailyFlow(DailyHealth.hrv).sequence == 2)
        #expect(try await reopened.dailyFlow(DailyHealth.sleep).lastReceipt != nil)
    }
    @Test(arguments: [DailyUploader.Mode.offline, .lostACK, .internalError, .wrongACK])
    func failedUploadAndLostACKKeepExactBytes(_ mode: DailyUploader.Mode) async throws {
        let (store, directory) = try await dailyFixture(); defer { try? FileManager.default.removeItem(at: directory) }
        let type = DailyHealth.hrv; _ = try await store.dailyFlow(type)
        try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([42]), facts: [], deleted: [], observedAt: Date(), hasMore: true)
        let pending = try #require(try await store.dailyFlow(type).pending)
        let uploader = DailyUploader(); uploader.state.withLock { $0.mode = mode }
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", uploader: uploader)
        do { _ = try await DailyHealthDelivery(store: store, api: api).deliver(type); Issue.record("uncertain ACK accepted") } catch { }
        #expect(try await store.dailyFlow(type).anchor == nil)
        let reopened = try OutboxStore(directory: directory)
        #expect(try await reopened.dailyFlow(type).pending?.hash == pending.hash)
        #expect(uploader.state.withLock { $0.discarded.isEmpty })
        uploader.state.withLock { $0.mode = .success }
        #expect(try await DailyHealthDelivery(store: reopened, api: api).deliver(type, now: .distantFuture))
        #expect(try await reopened.dailyFlow(type).anchor == Data([42]))
        #expect(uploader.state.withLock { Set($0.hashes).count == 1 && $0.hashes.count == 2 })
    }
    @Test func killAfterServerCommitAndFailedAnchorSaveRecoverOldOrNewState() async throws {
        let (store, directory) = try await dailyFixture(); defer { try? FileManager.default.removeItem(at: directory) }
        let type = DailyHealth.rhr; _ = try await store.dailyFlow(type)
        try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([7]), facts: [], deleted: [], observedAt: Date(), hasMore: false)
        let pending = try #require(try await store.dailyFlow(type).pending)
        let raw = try Data(contentsOf: await store.fileURL(pending.file)), receipt = try dailyACK(raw)
        // The exact durable server ACK exists, but no local anchor transition.
        let restart = try OutboxStore(directory: directory)
        #expect(try await restart.dailyFlow(type).anchor == nil)
        let stateURL = directory.appendingPathComponent("state.json"), saved = directory.appendingPathComponent("old-state.json")
        try FileManager.default.moveItem(at: stateURL, to: saved)
        try FileManager.default.createDirectory(at: stateURL, withIntermediateDirectories: false)
        do { try await restart.acknowledgeDaily(type: type, receipt: receipt); Issue.record("failed atomic save succeeded") } catch { }
        #expect(try await restart.dailyFlow(type).anchor == nil)
        try FileManager.default.removeItem(at: stateURL); try FileManager.default.moveItem(at: saved, to: stateURL)
        let afterFailure = try OutboxStore(directory: directory)
        try await afterFailure.acknowledgeDaily(type: type, receipt: receipt)
        let final = try OutboxStore(directory: directory)
        #expect(try await final.dailyFlow(type).anchor == Data([7]))
        #expect(try await final.dailyFlow(type).pending == nil)
    }
    @Test func boundedBodyAndImmutablePending() async throws {
        let (store, directory) = try await dailyFixture(); defer { try? FileManager.default.removeItem(at: directory) }
        let type = DailyHealth.sleep, flow = try await store.dailyFlow(type)
        #expect(flow.bootstrapStartUs < Int64(Date().timeIntervalSince1970 * 1_000_000))
        do { try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data(), facts: [], deleted: (0...DailyHealth.maxChanges).map { _ in Wire.id() }, observedAt: Date(), hasMore: false); Issue.record("oversize accepted") } catch { }
        #expect(try await store.dailyFlow(type).pending == nil)
        try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([1]), facts: [], deleted: [], observedAt: Date(), hasMore: false)
        do { try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([2]), facts: [], deleted: [], observedAt: Date(), hasMore: false); Issue.record("pending overwritten") } catch { }
        #expect(try await store.dailyFlow(type).pending?.candidateAnchor == Data([1]))
    }
    @Test func pendingSleepGetsCapacityBeforeFreshEmptyQueries() async throws {
        let (store, directory) = try await dailyFixture(); defer { try? FileManager.default.removeItem(at: directory) }
        let now = Date()
        for type in DailyHealth.types {
            _ = try await store.dailyFlow(type)
            try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([1]), facts: [], deleted: [], observedAt: now, hasMore: false)
            if type != DailyHealth.sleep {
                let pending = try #require(try await store.dailyFlow(type).pending)
                let raw = try Data(contentsOf: await store.fileURL(pending.file))
                try await store.acknowledgeDaily(type: type, receipt: dailyACK(raw))
            } else { try await store.dailyFailure(type, code: "device_rate_exceeded", retryAfter: 2) }
        }
        let reopened = try OutboxStore(directory: directory)
        #expect(await reopened.dailyDeliveryOrder().first == DailyHealth.sleep)
        #expect(try await !reopened.dailyFlow(DailyHealth.hrv).discoveryDue(now: now.addingTimeInterval(2), wakeGeneration: 0))
        #expect(try await !reopened.dailyFlow(DailyHealth.rhr).discoveryDue(now: now.addingTimeInterval(2), wakeGeneration: 0))
        #expect(try await reopened.dailyFlow(DailyHealth.hrv).discoveryDue(now: now.addingTimeInterval(901), wakeGeneration: 0))
    }
    @Test func observerDuringQueryRetainsNewerWakeGeneration() async throws {
        let (store, directory) = try await dailyFixture(); defer { try? FileManager.default.removeItem(at: directory) }
        for type in DailyHealth.types {
            _ = try await store.dailyFlow(type)
            try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([1]), facts: [], deleted: [], observedAt: Date(), hasMore: false, wakeGeneration: 0)
        }
        try await store.markDailyWake()
        for type in DailyHealth.types {
            let pending = try #require(try await store.dailyFlow(type).pending)
            try await store.acknowledgeDaily(type: type, receipt: dailyACK(Data(contentsOf: await store.fileURL(pending.file))))
        }
        try await store.finishDailyWakeIfCaughtUp()
        #expect(await store.snapshot().dailyWakePending == true)
        let reopened = try OutboxStore(directory: directory)
        #expect(try await reopened.dailyFlow(DailyHealth.sleep).discoveryDue(wakeGeneration: 1))
    }
    @Test func offlineHandshakeDefersDailyAlongsideExistingRecovery() async throws {
        let (store, directory) = try await dailyFixture(); defer { try? FileManager.default.removeItem(at: directory) }
        let now = Date(), type = DailyHealth.hrv
        _ = try await store.dailyFlow(type)
        try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([1]), facts: [], deleted: [], observedAt: now, hasMore: false)
        let pending = try #require(try await store.dailyFlow(type).pending)
        let reopened = try OutboxStore(directory: directory), deferred = try await reopened.dailyFlow(type)
        #expect(deferred.anchor == nil && deferred.pending?.hash == pending.hash)
        try await reopened.deferDailyRecovery(UploadError(code: "offline"), now: now.addingTimeInterval(1))
        #expect(try await reopened.dailyFlow(type).pending?.attempt == 1)
    }
}
