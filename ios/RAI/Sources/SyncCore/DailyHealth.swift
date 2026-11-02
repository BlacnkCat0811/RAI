import Foundation

public enum DailyHealth {
    public static let hrv = "HKQuantityTypeIdentifierHeartRateVariabilitySDNN"
    public static let rhr = "HKQuantityTypeIdentifierRestingHeartRate"
    public static let sleep = "HKCategoryTypeIdentifierSleepAnalysis"
    public static let types = [hrv, rhr, sleep]
    public static let maxChanges = 128
    public static let maxBytes = 512 * 1024
    public static let lookbackDays = 90
    public static func fingerprint(type: String, startUs: Int64) -> String {
        "sha256:" + Wire.hash(Data("daily-v1|\(type)|\(startUs)".utf8))
    }
}

public struct DailyPending: Codable, Sendable {
    public let batchID: String, file: String, hash: String
    public let candidateAnchor: Data
    public let hasMore: Bool
    public var attempt = 0
    public var nextAttempt = Date.distantPast
}
public struct DailyFlow: Codable, Sendable {
    public let streamID: String, type: String
    public let bootstrapStartUs: Int64
    public var anchor: Data?
    public var sequence: Int64 = 1
    public var previousBatchID: String?
    public var pending: DailyPending?
    public var bootstrapComplete = false
    public var hasMore = true
    public var lastDiscovery: Date?
    public var lastSync: Date?
    public var error: String?
    public var lastReceipt: JSON?
    public var discoveryGeneration: Int64?
    public func discoveryDue(now: Date = Date(), wakeGeneration: Int64) -> Bool {
        guard pending == nil else { return false }
        return hasMore || discoveryGeneration != wakeGeneration || lastDiscovery == nil || now.timeIntervalSince(lastDiscovery!) >= 900
    }
    public var fingerprint: String { DailyHealth.fingerprint(type: type, startUs: bootstrapStartUs) }
    public init(type: String, now: Date) {
        streamID = Wire.id(); self.type = type
        bootstrapStartUs = Int64((now.timeIntervalSince1970 * 1_000_000).rounded()) - Int64(DailyHealth.lookbackDays) * 86_400_000_000
    }
    public func body(scope: Scope, facts: [JSON], deleted: [String], observedAt: Date, hasMore: Bool, batchID: String) throws -> Data {
        guard DailyHealth.types.contains(type), facts.count + deleted.count <= DailyHealth.maxChanges else { throw SyncFailure.invalid("batch_too_large") }
        var fields = scope.bundleFields
        fields.removeValue(forKey: "protocolVersion")
        fields["version"] = .int(1); fields["streamID"] = .string(streamID); fields["batchID"] = .string(batchID)
        fields["clientSeq"] = .int(sequence); fields["previousBatchID"] = previousBatchID.map(JSON.string) ?? .null
        fields["sampleType"] = .string(type); fields["queryFingerprint"] = .string(fingerprint)
        fields["bootstrapStartUs"] = .int(bootstrapStartUs)
        fields["observedAtUs"] = .int(Int64((observedAt.timeIntervalSince1970 * 1_000_000).rounded()))
        fields["hasMore"] = .bool(hasMore); fields["facts"] = .array(facts); fields["deletedUUIDs"] = .array(deleted.map(JSON.string))
        let raw = try JSON.object(fields).data()
        guard raw.count <= DailyHealth.maxBytes else { throw SyncFailure.invalid("batch_too_large") }
        return raw
    }
}

extension OutboxStore {
    static func deferDaily(_ next: inout LocalState, error: any Error, now: Date) {
        let failure = error as? UploadError
        for (type, var flow) in next.daily ?? [:] {
            guard var pending = flow.pending, pending.nextAttempt <= now else { continue }
            pending.attempt += 1
            pending.nextAttempt = now.addingTimeInterval(max(UploadError.backoff(pending.attempt), failure?.retryAfter ?? 0))
            flow.pending = pending; flow.error = failure?.code ?? "daily_context_unavailable"; next.daily?[type] = flow
        }
    }
    public func deferDailyRecovery(_ error: any Error, now: Date = Date()) throws {
        var next = state; Self.deferDaily(&next, error: error, now: now); try commit(next)
    }
    public func markDailyWake() throws {
        var next = state; next.dailyWakePending = true; next.dailyWakeGeneration = (next.dailyWakeGeneration ?? 0) + 1; try commit(next)
    }
    /// Pending work and the oldest acknowledged type get the next shared
    /// upload opportunity. A busy HRV stream cannot starve Sleep under 429.
    public func dailyDeliveryOrder() -> [String] {
        DailyHealth.types.sorted { a, b in
            let left = state.daily?[a], right = state.daily?[b]
            let leftRank = left?.pending != nil ? 0 : left?.hasMore != false ? 1 : 2
            let rightRank = right?.pending != nil ? 0 : right?.hasMore != false ? 1 : 2
            if leftRank != rightRank { return leftRank < rightRank }
            let leftDate = left?.lastSync ?? .distantPast, rightDate = right?.lastSync ?? .distantPast
            if leftDate != rightDate { return leftDate < rightDate }
            return DailyHealth.types.firstIndex(of: a)! < DailyHealth.types.firstIndex(of: b)!
        }
    }
    public func finishDailyWakeIfCaughtUp() throws {
        guard DailyHealth.types.allSatisfy({ state.daily?[$0].map { $0.pending == nil && !$0.hasMore && $0.error == nil && $0.discoveryGeneration == (state.dailyWakeGeneration ?? 0) } == true }) else { return }
        var next = state; next.dailyWakePending = false; try commit(next)
    }
    public func dailyFlow(_ type: String, now: Date = Date()) throws -> DailyFlow {
        guard DailyHealth.types.contains(type) else { throw SyncFailure.invalid("unsupported_type") }
        if let flow = state.daily?[type] { return flow }
        var next = state
        if next.daily == nil { next.daily = [:] }
        let flow = DailyFlow(type: type, now: now); next.daily?[type] = flow; try commit(next); return flow
    }
    /// Write immutable body before publishing the candidate anchor. A pending
    /// page blocks another query of this type; committed anchors never move here.
    public func stageDaily(type: String, previousAnchor: Data?, candidateAnchor: Data, facts: [JSON], deleted: [String], observedAt: Date, hasMore: Bool, wakeGeneration: Int64? = nil) throws {
        guard var flow = state.daily?[type], flow.anchor == previousAnchor, flow.pending == nil, let scope = state.scope else { throw SyncFailure.localState("daily_state_conflict") }
        let batchID = Wire.id()
        let raw = try flow.body(scope: scope, facts: facts, deleted: deleted, observedAt: observedAt, hasMore: hasMore, batchID: batchID)
        let file = try savePayload(raw, suffix: "daily.json")
        flow.pending = DailyPending(batchID: batchID, file: file, hash: Wire.hash(raw), candidateAnchor: candidateAnchor, hasMore: hasMore)
        flow.lastDiscovery = observedAt; flow.discoveryGeneration = wakeGeneration ?? state.dailyWakeGeneration ?? 0; flow.error = nil
        var next = state; next.daily?[type] = flow; try commit(next)
    }
    public func dailyFailure(_ type: String, code: String, retryAfter: TimeInterval? = nil) throws {
        guard var flow = state.daily?[type] else { return }
        flow.error = code
        if var pending = flow.pending {
            pending.attempt += 1; pending.nextAttempt = Date().addingTimeInterval(retryAfter ?? UploadError.backoff(pending.attempt))
            flow.pending = pending
        }
        var next = state; next.daily?[type] = flow; try commit(next)
    }
    public func acknowledgeDaily(type: String, receipt: JSON) throws {
        guard var flow = state.daily?[type], let pending = flow.pending, let scope = state.scope else { throw SyncFailure.localState("daily_pending_missing") }
        guard receipt["version"].integer == 1, receipt["batchID"].text == pending.batchID,
              receipt["datasetID"].text == scope.datasetID, receipt["datasetEpoch"].text == scope.datasetEpoch,
              receipt["collectorEpoch"].text == scope.collectorEpoch, receipt["streamID"].text == flow.streamID,
              receipt["clientSeq"].integer == flow.sequence, receipt["requestSHA256"].text == pending.hash,
              receipt["ingestStatus"].text == "committed", let rev = receipt["commitRevision"].integer, rev > 0,
              receipt["analysisStatus"].text == "awaiting_model_implementation",
              ["none", "deleted_precedence"].contains(receipt["deleteStatus"].text ?? ""),
              ["inserted", "unchanged", "deleted", "tombstoned", "suppressed"].allSatisfy({ (receipt["counts"][$0].integer ?? -1) >= 0 }) else { throw SyncFailure.receiptMismatch }
        // Anchor + ACK + stream chain + pending removal share one atomic state
        // replacement. A kill on either side leaves a recoverable old/new state.
        flow.anchor = pending.candidateAnchor; flow.previousBatchID = pending.batchID; flow.sequence += 1
        flow.bootstrapComplete = flow.bootstrapComplete || !pending.hasMore; flow.hasMore = pending.hasMore
        flow.pending = nil; flow.lastReceipt = receipt; flow.lastSync = Date(); flow.error = nil
        var next = state; next.daily?[type] = flow
        next.reclaimFiles = Array(Set((next.reclaimFiles ?? []) + [pending.file])).sorted()
        try commit(next)
    }
}

public struct DailyHealthDelivery: Sendable {
    let store: OutboxStore
    let api: RAIAPIClient
    public init(store: OutboxStore, api: RAIAPIClient) { self.store = store; self.api = api }
    @discardableResult public func deliver(_ type: String, now: Date = Date()) async throws -> Bool {
        let flow = try await store.dailyFlow(type)
        guard let pending = flow.pending else { return true }
        guard pending.nextAttempt <= now else { return false }
        do {
            try Task.checkCancellation()
            let file = try await store.fileURL(pending.file)
            guard try Wire.hash(Data(contentsOf: file)) == pending.hash else { throw SyncFailure.localState("daily_payload_corrupt") }
            let receipt = try await api.call("POST", "/v2/sync/daily-facts", file: file, identity: pending.batchID)
            try await store.acknowledgeDaily(type: type, receipt: receipt)
            api.discardUploadResult(pending.batchID)
            return true
        } catch {
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            let code = (error as? UploadError)?.code ?? "daily_delivery_failed"
            try await store.dailyFailure(type, code: code, retryAfter: (error as? UploadError)?.retryAfter)
            throw error
        }
    }
}
