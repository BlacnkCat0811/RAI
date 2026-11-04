import Foundation
import Synchronization
import Testing
@testable import SyncCore

private let vo2Capabilities: JSON = .object(["vo2maxFactsVersion": .int(1), "vo2maxMetadataRegistryVersion": .int(1), "vo2maxMaxChanges": .int(128), "vo2maxMaxBytes": .int(524288)])
private func vo2ACK(_ raw: Data) throws -> JSON {
    let body = try JSON.decode(raw)
    var fields: [String: JSON] = ["contract": .string(VO2Max.contract), "version": .int(1), "metadataRegistryVersion": .int(1),
        "requestSHA256": .string(Wire.hash(raw)), "ingestStatus": .string("committed"), "commitRevision": .int(1),
        "analysisStatus": .string("awaiting_model_implementation"), "deleteStatus": .string("none"),
        "counts": .object(Dictionary(uniqueKeysWithValues: ["inserted", "unchanged", "deleted", "tombstoned", "suppressed"].map { ($0, .int(0)) }))]
    for key in ["batchID", "datasetID", "datasetEpoch", "collectorEpoch", "streamID", "clientSeq"] { fields[key] = body[key] }
    return .object(fields)
}
private final class VO2Uploader: FileUploader, Sendable {
    let mode = Mutex(200), bodies = Mutex<[Data]>([]), discarded = Mutex<[String]>([])
    func discardCachedResult(_ identity: String) { discarded.withLock { $0.append(identity) } }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let raw = try Data(contentsOf: file); bodies.withLock { $0.append(raw) }
        let status = mode.withLock { $0 }
        if status == -1 { throw URLError(.networkConnectionLost) }
        let ack = status == 201 ? JSON.object([:]) : try vo2ACK(raw)
        return UploadResponse(data: try ack.data(), status: status == 201 ? 200 : status, retryAfter: 1, contentType: "application/json")
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) { let r = try await response(request, file: file, identity: identity); return (r.data, r.status, r.retryAfter) }
}
private func vo2Store() async throws -> (OutboxStore, URL) {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-vo2-" + Wire.id())
    let store = try OutboxStore(directory: dir)
    try await store.bind(Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id()))
    _ = try await store.vo2Flow()
    return (store, dir)
}
@Suite struct VO2MaxTests {
    @Test func fixedFingerprintAndOldStateCompatibility() async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        for type in DailyHealth.types {
            _ = try await store.dailyFlow(type)
            try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([2]), facts: [], deleted: [], observedAt: Date(), hasMore: false)
        }
        let old = await store.snapshot()
        var json = try JSON.decode(Data(contentsOf: dir.appendingPathComponent("state.json"))).fields
        json.removeValue(forKey: "vo2"); json.removeValue(forKey: "vo2WakeGeneration")
        try OutboxStore.write(JSON.object(json).data(), to: dir.appendingPathComponent("state.json"))
        let reopened = try OutboxStore(directory: dir)
        #expect(await reopened.snapshot().vo2 == nil)
        for type in DailyHealth.types {
            let current = try await reopened.dailyFlow(type)
            #expect(current.pending?.hash == old.daily?[type]?.pending?.hash)
            #expect(current.fingerprint == old.daily?[type]?.fingerprint && current.anchor == old.daily?[type]?.anchor)
        }
        let flow = try await reopened.vo2Flow(now: Date(timeIntervalSince1970: 1793000000))
        #expect(flow.flowCreatedAtUs - flow.bootstrapStartUs == VO2Max.windowUs)
        let later = try await reopened.vo2Flow(now: Date(timeIntervalSince1970: 1893000000))
        #expect(flow.bootstrapStartUs == later.bootstrapStartUs && flow.fingerprint == later.fingerprint)
    }
    @Test(arguments: [-1, 500, 429, 201]) func failuresAndLostACKKeepPending(status: Int) async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        try await store.stageVO2(previousAnchor: nil, candidateAnchor: Data([42]), facts: [], deleted: [], observedAt: Date(), hasMore: false, wakeGeneration: 0)
        let pending = try #require(await store.snapshot().vo2?.pending), raw = try Data(contentsOf: await store.fileURL(pending.file))
        let uploader = VO2Uploader(); uploader.mode.withLock { $0 = status }
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", uploader: uploader)
        do { _ = try await VO2MaxDelivery(store: store, api: api).deliver(capabilities: vo2Capabilities); Issue.record("failed ACK committed") } catch {}
        #expect(uploader.discarded.withLock { $0.count } == (status == 201 ? 1 : 0))
        let reopened = try OutboxStore(directory: dir)
        #expect(await reopened.snapshot().vo2?.anchor == nil)
        #expect(await reopened.snapshot().vo2?.pending?.hash == pending.hash)
        uploader.mode.withLock { $0 = 200 }
        #expect(try await VO2MaxDelivery(store: reopened, api: api).deliver(capabilities: vo2Capabilities, now: Date.distantFuture))
        #expect(await reopened.snapshot().vo2?.anchor == Data([42]))
        #expect(uploader.bodies.withLock { $0.allSatisfy { $0 == raw } })
    }
    @Test func noCapabilityNoFallbackAndACKReplacementFailure() async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        try await store.stageVO2(previousAnchor: nil, candidateAnchor: Data([1]), facts: [], deleted: [], observedAt: Date(), hasMore: false, wakeGeneration: 0)
        let uploader = VO2Uploader(), api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", uploader: uploader)
        do { _ = try await VO2MaxDelivery(store: store, api: api).deliver(capabilities: .object([:])); Issue.record("missing capability uploaded") } catch {}
        #expect(uploader.bodies.withLock { $0.isEmpty })
        let pending = try #require(await store.snapshot().vo2?.pending), raw = try Data(contentsOf: await store.fileURL(pending.file))
        let stateFile = dir.appendingPathComponent("state.json"), saved = dir.appendingPathComponent("saved-state")
        try FileManager.default.moveItem(at: stateFile, to: saved); try FileManager.default.createDirectory(at: stateFile, withIntermediateDirectories: false)
        do { try await store.acknowledgeVO2(vo2ACK(raw)); Issue.record("replacement failure accepted") } catch {}
        let failedState = await store.snapshot()
        #expect(failedState.vo2?.anchor == nil && failedState.vo2?.pending != nil)
        try FileManager.default.removeItem(at: stateFile); try FileManager.default.moveItem(at: saved, to: stateFile)
        let restored = try OutboxStore(directory: dir); try await restored.acknowledgeVO2(vo2ACK(raw))
        #expect(await restored.snapshot().vo2?.anchor == Data([1]))
    }
    @Test func blockedPageAndOrphansAreGCRoots() async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        try await store.blockVO2(previousAnchor: nil, candidateAnchor: Data([9]), archive: Data("synthetic-only".utf8), category: "unsupported_metadata")
        let blocked = try #require(await store.snapshot().vo2?.blocked)
        let orphan = try await store.savePayload(Data(), suffix: "vo2.json")
        let restored = try OutboxStore(directory: dir); _ = try await restored.vo2Flow()
        #expect(!FileManager.default.fileExists(atPath: dir.appendingPathComponent(orphan).path))
        #expect(await restored.snapshot().reclaimFiles?.contains(blocked.file) != true)
        #expect(await restored.snapshot().vo2?.anchor == nil)
        var malicious = await restored.snapshot(); malicious.reclaimFiles = [blocked.file]
        try await restored.commit(malicious)
        do { _ = try await restored.collectGarbage(dryRun: false); Issue.record("blocked page was collected") } catch {}
        #expect(try Data(contentsOf: await restored.fileURL(blocked.file)) == Data("synthetic-only".utf8))
    }
    @Test func bodyBeforeFailedStatePublishIsReclaimed() async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("state.json"), backup = dir.appendingPathComponent("before-state")
        try FileManager.default.moveItem(at: file, to: backup); try FileManager.default.createDirectory(at: file, withIntermediateDirectories: false)
        do {
            try await store.stageVO2(previousAnchor: nil, candidateAnchor: Data([4]), facts: [], deleted: [], observedAt: Date(), hasMore: false, wakeGeneration: 0)
            Issue.record("failed state publish succeeded")
        } catch {}
        #expect(await store.snapshot().vo2?.pending == nil)
        try FileManager.default.removeItem(at: file); try FileManager.default.moveItem(at: backup, to: file)
        let reopened = try OutboxStore(directory: dir); _ = try await reopened.vo2Flow()
        let state = await reopened.snapshot()
        #expect(state.vo2?.anchor == nil && state.vo2?.pending == nil)
        #expect((state.reclaimFiles ?? []).isEmpty)
        #expect(try FileManager.default.contentsOfDirectory(atPath: dir.path).filter { $0.hasSuffix(".vo2.json") }.isEmpty)
    }
    @Test func transferCacheIsReclaimedWithACK() async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        try await store.stageVO2(previousAnchor: nil, candidateAnchor: Data([2]), facts: [], deleted: [], observedAt: Date(), hasMore: false, wakeGeneration: 0)
        let pending = try #require(await store.snapshot().vo2?.pending), body = try Data(contentsOf: await store.fileURL(pending.file))
        let cache = Wire.hash(Data(pending.batchID.utf8)) + ".transfer.json"
        try OutboxStore.write(Data(), to: dir.appendingPathComponent(cache))
        let started = Date()
        try await store.acknowledgeVO2(vo2ACK(body), transferCacheFile: cache)
        print("M4B2_SYNTHETIC_STATE_COMMIT bytes=\(body.count) duration_us=\(Date().timeIntervalSince(started) * 1_000_000)")
        #expect(await store.snapshot().reclaimFiles?.contains(cache) == true)
        #expect(try await store.collectGarbage(dryRun: false) == 2)
    }
    @Test func sharedContextBackoffAndSchedulerOpportunitySurviveRestart() async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        try await store.stageVO2(previousAnchor: nil, candidateAnchor: Data([8]), facts: [], deleted: [], observedAt: Date(), hasMore: false, wakeGeneration: 0)
        let before = try #require(await store.snapshot().vo2?.pending), now = Date()
        try await store.deferVO2Recovery(UploadError(code: "request_budget_exceeded", status: 429, retryAfter: 60), now: now)
        let reopened = try OutboxStore(directory: dir), flow = try await reopened.vo2Flow()
        #expect(flow.pending?.batchID == before.batchID && flow.pending?.hash == before.hash)
        #expect(flow.pending!.nextAttempt >= now.addingTimeInterval(60))
        #expect(flow.nextOpportunity(now: now, wakeGeneration: 0) == flow.pending?.nextAttempt)
        #expect(flow.anchor == nil)
        #expect(await reopened.healthDeliveryOrder().first == VO2Max.type)
    }
    @Test(arguments: ["collector_fenced", "generation_conflict", "sample_identity_conflict", "unsupported_metadata"])
    func permanentRejectionStopsAutomaticWritesWithoutDiscardingPending(code: String) async throws {
        let (store, dir) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: dir) }
        try await store.stageVO2(previousAnchor: nil, candidateAnchor: Data([7]), facts: [], deleted: [], observedAt: Date(), hasMore: false, wakeGeneration: 0)
        let before = try #require(await store.snapshot().vo2?.pending)
        try await store.vo2Failure(code: code)
        try await store.deferVO2Recovery(UploadError(code: "network_error", status: 503))
        let uploader = VO2Uploader(), api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", uploader: uploader)
        #expect(try await !VO2MaxDelivery(store: store, api: api).deliver(capabilities: vo2Capabilities, now: .distantFuture))
        let flow = try await store.vo2Flow()
        #expect(flow.pending?.batchID == before.batchID && flow.pending?.hash == before.hash && flow.error == code)
        #expect(flow.nextOpportunity(now: Date(), wakeGeneration: 0) == nil)
        #expect(uploader.bodies.withLock { $0.isEmpty })
        #expect(flow.anchor == nil)
    }
    @Test func canonicalGoldenAndLimits() async throws {
        let url = try #require(Bundle.module.url(forResource: "vo2max-provenance.canonical", withExtension: "json", subdirectory: "Fixtures"))
        let raw = try Data(contentsOf: url), p = try JSON.decode(raw)
        #expect(Wire.hash(try p.data()) == "0c754f1c852ccb0ed4d84126cc2b9a3957bee2ea540148e686e627e51fed652f")
        let (store, directory) = try await vo2Store(); defer { try? FileManager.default.removeItem(at: directory) }
        let flow = try await store.vo2Flow(), scope = try #require(await store.snapshot().scope)
        for n in [0, 1, 128] {
            let body = try flow.body(scope: scope, facts: [], deleted: (0..<n).map { _ in Wire.id() }, observedAt: Date(), hasMore: false, batchID: Wire.id())
            #expect(try JSON.decode(body)["deletedUUIDs"].list.count == n)
        }
        for n in [129, 1024, 1025] {
            #expect(throws: (any Error).self) { try flow.body(scope: scope, facts: [], deleted: (0..<n).map { _ in Wire.id() }, observedAt: Date(), hasMore: false, batchID: Wire.id()) }
        }
        #expect(throws: (any Error).self) { try flow.body(scope: scope, facts: [.string(String(repeating: "x", count: VO2Max.maxBytes))], deleted: [], observedAt: Date(), hasMore: false, batchID: Wire.id()) }
    }
    @Test func precisionAndRegistry() throws {
        for n in [Int64(9007199254740993), .min, .max] {
            let m = try VO2Max.metadata(key: "HKAlgorithmVersion", value: NSNumber(value: n))
            #expect(m["value"].integer == n)
            #expect(try JSON.decode(m.data())["value"].integer == n)
        }
        for n in [true, false] { #expect(try VO2Max.metadata(key: "HKWasUserEntered", value: NSNumber(value: n))["value"] == .bool(n)) }
        for n in 1...4 { #expect(try VO2Max.metadata(key: "HKVO2MaxTestType", value: NSNumber(value: n))["value"] == .int(Int64(n))) }
        for n in [0,5] { #expect(throws: (any Error).self) { try VO2Max.metadata(key: "HKVO2MaxTestType", value: NSNumber(value: n)) } }
        #expect(throws: (any Error).self) { try VO2Max.metadata(key: "HKWasUserEntered", value: NSNumber(value: 0)) }
        for value: Any in [Data(), ["x": 1], [1,2], 1.25] { #expect(throws: (any Error).self) { try VO2Max.metadata(key: "future", value: value) } }
        #expect(throws: (any Error).self) { try VO2Max.metadata(key: "HKAlgorithmVersion", value: NSNumber(value: 1.25)) }
        #expect(try VO2Max.finiteNumber(1.2345678901234567)["type"] == .string("number"))
        #expect(throws: (any Error).self) { try VO2Max.finiteNumber(.nan) }
        #expect(try VO2Max.quantity(value: 1.2345678901234567)["value"] == .number(1.2345678901234567))
        #expect(throws: (any Error).self) { try VO2Max.quantity(value: .infinity) }
        #expect(try VO2Max.metadata(key: "HKDateOfEarliestDataUsedForEstimate", value: Date(timeIntervalSince1970: 1.25))["epochUs"] == .int(1250000))
    }
}
