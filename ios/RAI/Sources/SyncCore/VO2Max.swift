import Foundation
import CoreFoundation

public enum VO2Max {
    public static let contract = "vo2max-facts/v1"
    public static let type = "HKQuantityTypeIdentifierVO2Max"
    public static let maxChanges = 128, maxBytes = 524288, maxContextBytes = 65536
    public static let windowUs: Int64 = 90 * 86_400_000_000
    /// Local parser revision, not a wire registry/schema version.
    public static let nativeMetadataParserRevision = 2
    public static func fingerprint(startUs: Int64) -> String {
        "sha256:" + Wire.hash(Data("\(contract)|\(type)|\(startUs)|overlap-v1".utf8))
    }
    public static func supported(_ capabilities: JSON) -> Bool {
        capabilities["vo2maxFactsVersion"].integer == 1 && capabilities["vo2maxMetadataRegistryVersion"].integer == 1 &&
        capabilities["vo2maxMaxChanges"].integer == Int64(maxChanges) && capabilities["vo2maxMaxBytes"].integer == Int64(maxBytes)
    }
    public static func blocksAutomaticDelivery(_ code: String?) -> Bool {
        guard let code else { return false }
        return ["unsupported_version", "unsupported_metadata", "unsupported_metadata_type", "unsupported_test_type", "source_invalid", "unsupported_unit", "invalid_value", "invalid_payload", "batch_identity_conflict", "sample_identity_conflict", "stale_batch", "stream_identity_conflict", "generation_conflict", "collector_fenced", "upload_lease_lost", "dataset_retired", "vo2_payload_corrupt", "invalid_healthkit_anchor"].contains(code)
    }
    public static func failureCode(_ error: any Error) -> String {
        if let upload = error as? UploadError { return upload.code }
        switch error {
        case SyncFailure.invalid(let code), SyncFailure.localState(let code): return code
        case SyncFailure.receiptMismatch: return "invalid_receipt"
        default: return "vo2_delivery_failed"
        }
    }
    public static func microseconds(_ date: Date) throws -> Int64 {
        let us = (date.timeIntervalSince1970 * 1_000_000).rounded()
        guard us.isFinite, us >= -62135596800000000, us < 253402300800000000 else { throw SyncFailure.invalid("invalid_value") }
        return Int64(us)
    }
    public static func finiteNumber(_ value: Double) throws -> JSON {
        guard value.isFinite else { throw SyncFailure.invalid("invalid_value") }
        return .object(["type": .string("number"), "value": .number(value == 0 ? 0 : value)])
    }
    public static func quantity(value: Double) throws -> JSON {
        guard value.isFinite else { throw SyncFailure.invalid("invalid_value") }
        return .object(["type": .string("quantity"), "value": .number(value == 0 ? 0 : value), "unit": .string("mL/kg/min")])
    }
    /// Registry v1. HKQuantity conversion is performed by the SDK normalizer;
    /// arbitrary number/string/object keys never gain admission via this codec.
    public static func metadata(key: String, value: Any) throws -> JSON {
        let unsupported = SyncFailure.invalid("unsupported_metadata")
        let tag: String, payload: JSON
        switch key {
        case "HKVO2MaxTestType", "HKMetadataKeySyncVersion", "HKAlgorithmVersion":
            guard let n = value as? NSNumber, CFGetTypeID(n) != CFBooleanGetTypeID(),
                  !(n is NSDecimalNumber) else { throw unsupported }
            let carrier = String(cString: n.objCType)
            let integer: Int64
            if carrier.count == 1, carrier.first.map({ "cCsSiIlLqQ".contains($0) }) == true {
                guard let exact = Int64(n.stringValue) else { throw unsupported }
                integer = exact
            } else if carrier == "d" || carrier == "f" {
                let x = n.doubleValue
                // Exclude the rounding boundary too: floating (2^53 + 1)
                // has already rounded to 2^53 and cannot recover its origin.
                let boundary: Double = carrier == "f" ? 16_777_216 : 9_007_199_254_740_992
                guard x.isFinite, x.rounded(.towardZero) == x, abs(x) < boundary,
                      let exact = Int64(exactly: x) else { throw unsupported }
                integer = exact
            } else { throw unsupported }
            if key == "HKVO2MaxTestType", !(1...4).contains(integer) { throw SyncFailure.invalid("unsupported_test_type") }
            tag = "int64"; payload = .int(integer)
        case "HKWasUserEntered", "HKMetadataKeyAppleDeviceCalibrated":
            guard let n = value as? NSNumber, CFGetTypeID(n) == CFBooleanGetTypeID() else { throw unsupported }
            tag = "boolean"; payload = .bool(n.boolValue)
        case "HKMetadataKeySyncIdentifier", "HKTimeZone":
            guard let text = value as? String, !text.isEmpty,
                  text.utf8.count <= (key == "HKTimeZone" ? 128 : 255) else { throw unsupported }
            if key == "HKTimeZone", TimeZone(identifier: text) == nil { throw unsupported }
            tag = "string"; payload = .string(text)
        case "HKDateOfEarliestDataUsedForEstimate":
            guard let date = value as? Date else { throw unsupported }
            return .object(["type": .string("date"), "epochUs": .int(try microseconds(date))])
        default: throw unsupported
        }
        return .object(["type": .string(tag), "value": payload])
    }
}

public struct VO2Pending: Codable, Sendable {
    public let batchID: String, file: String, hash: String
    public let candidateAnchor: Data
    public let hasMore: Bool
    public var attempt = 0
    public var nextAttempt = Date.distantPast
    public var sourceBlockedHash: String?
}
public struct VO2Blocked: Codable, Sendable {
    public let file: String, hash: String, category: String
    public let candidateAnchor: Data
    public var hasMore: Bool?
    public var parserRevision: Int?
}
public struct VO2Flow: Codable, Sendable {
    public let streamID: String
    public let flowCreatedAtUs: Int64, bootstrapStartUs: Int64
    public var anchor: Data?
    public var sequence: Int64 = 1
    public var previousBatchID: String?
    public var pending: VO2Pending?
    public var blocked: VO2Blocked?
    public var bootstrapComplete = false
    public var hasMore = true
    public var lastDiscovery: Date?
    public var lastSync: Date?
    public var error: String?
    public var lastReceipt: JSON?
    public var discoveryGeneration: Int64?
    public var fingerprint: String { VO2Max.fingerprint(startUs: bootstrapStartUs) }
    public func nextOpportunity(now: Date, wakeGeneration: Int64) -> Date? {
        guard !VO2Max.blocksAutomaticDelivery(error) else { return nil }
        if let pending { return pending.nextAttempt }
        if error == nil && discoveryDue(now: now, wakeGeneration: wakeGeneration) { return now.addingTimeInterval(2) }
        return nil
    }
    public init(now: Date) throws {
        streamID = Wire.id(); flowCreatedAtUs = try VO2Max.microseconds(now)
        bootstrapStartUs = flowCreatedAtUs - VO2Max.windowUs
    }
    public func discoveryDue(now: Date = Date(), wakeGeneration: Int64) -> Bool {
        pending == nil && blocked == nil && (hasMore || discoveryGeneration != wakeGeneration || lastDiscovery == nil || now.timeIntervalSince(lastDiscovery!) >= 900)
    }
    public func body(scope: Scope, facts: [JSON], deleted: [String], observedAt: Date, hasMore: Bool, batchID: String) throws -> Data {
        guard facts.count + deleted.count <= VO2Max.maxChanges else { throw SyncFailure.invalid("batch_too_large") }
        var fields = scope.bundleFields; fields.removeValue(forKey: "protocolVersion")
        fields["contract"] = .string(VO2Max.contract); fields["version"] = .int(1); fields["metadataRegistryVersion"] = .int(1)
        fields["streamID"] = .string(streamID); fields["batchID"] = .string(batchID); fields["clientSeq"] = .int(sequence)
        fields["previousBatchID"] = previousBatchID.map(JSON.string) ?? .null; fields["sampleType"] = .string(VO2Max.type)
        fields["flowCreatedAtUs"] = .int(flowCreatedAtUs); fields["bootstrapStartUs"] = .int(bootstrapStartUs)
        fields["queryFingerprint"] = .string(fingerprint); fields["queryCompletedAtUs"] = .int(try VO2Max.microseconds(observedAt))
        fields["hasMore"] = .bool(hasMore); fields["facts"] = .array(facts); fields["deletedUUIDs"] = .array(deleted.map(JSON.string))
        let raw = try JSON.object(fields).data()
        guard raw.count <= VO2Max.maxBytes else { throw SyncFailure.invalid("batch_too_large") }
        return raw
    }
}

extension OutboxStore {
    public func vo2Flow(now: Date = Date()) throws -> VO2Flow {
        if !vo2OrphansReconciled { try reconcileVO2Orphans(); vo2OrphansReconciled = true }
        // Reclamation is bounded and idempotent. Failure retains the durable
        // queue; published pending/blocked roots are rechecked by the collector.
        _ = try? collectGarbage(dryRun: false)
        if let flow = state.vo2 { return flow }
        let flow = try VO2Flow(now: now); var next = state; next.vo2 = flow; try commit(next); return flow
    }
    public func healthDeliveryOrder() -> [String] {
        (DailyHealth.types + [VO2Max.type]).sorted { a, b in
            func rank(_ type: String) -> (Int, Date) {
                if type == VO2Max.type { return (state.vo2?.pending != nil ? 0 : state.vo2?.hasMore != false ? 1 : 2, state.vo2?.lastSync ?? .distantPast) }
                let flow = state.daily?[type]
                return (flow?.pending != nil ? 0 : flow?.hasMore != false ? 1 : 2, flow?.lastSync ?? .distantPast)
            }
            let l = rank(a), r = rank(b)
            if l.0 != r.0 { return l.0 < r.0 }; if l.1 != r.1 { return l.1 < r.1 }
            return (DailyHealth.types + [VO2Max.type]).firstIndex(of: a)! < (DailyHealth.types + [VO2Max.type]).firstIndex(of: b)!
        }
    }
    static func deferVO2(_ next: inout LocalState, error: any Error, now: Date) {
        guard var flow = next.vo2, !VO2Max.blocksAutomaticDelivery(flow.error), var pending = flow.pending, pending.nextAttempt <= now else { return }
        let failure = error as? UploadError
        pending.attempt += 1
        pending.nextAttempt = now.addingTimeInterval(max(UploadError.backoff(pending.attempt), failure?.retryAfter ?? 0))
        flow.pending = pending; flow.error = failure?.code ?? "vo2_context_unavailable"; next.vo2 = flow
    }
    public func deferVO2Recovery(_ error: any Error, now: Date = Date()) throws {
        var next = state; Self.deferVO2(&next, error: error, now: now); try commit(next)
    }
    public func markVO2Wake() throws {
        var next = state; next.vo2WakeGeneration = (next.vo2WakeGeneration ?? 0) + 1; try commit(next)
    }
    public func stageVO2(previousAnchor: Data?, candidateAnchor: Data, facts: [JSON], deleted: [String], observedAt: Date, hasMore: Bool, wakeGeneration: Int64) throws {
        guard var flow = state.vo2, flow.anchor == previousAnchor, flow.pending == nil, flow.blocked == nil,
              !candidateAnchor.isEmpty, let scope = state.scope else { throw SyncFailure.localState("vo2_state_conflict") }
        let batchID = Wire.id()
        let raw = try flow.body(scope: scope, facts: facts, deleted: deleted, observedAt: observedAt, hasMore: hasMore, batchID: batchID)
        let file = try savePayload(raw, suffix: "vo2.json")
        flow.pending = VO2Pending(batchID: batchID, file: file, hash: Wire.hash(raw), candidateAnchor: candidateAnchor, hasMore: hasMore)
        flow.lastDiscovery = observedAt; flow.discoveryGeneration = wakeGeneration; flow.error = nil
        var next = state; next.vo2 = flow
        do { try commit(next) } catch { vo2OrphansReconciled = false; throw error }
    }
    public func blockVO2(previousAnchor: Data?, candidateAnchor: Data, archive: Data, category: String, hasMore: Bool? = nil) throws {
        guard var flow = state.vo2, flow.anchor == previousAnchor, flow.pending == nil, flow.blocked == nil else { throw SyncFailure.localState("vo2_state_conflict") }
        let file = try savePayload(archive, suffix: "vo2-blocked.json")
        flow.blocked = VO2Blocked(file: file, hash: Wire.hash(archive), category: category, candidateAnchor: candidateAnchor,
            hasMore: hasMore, parserRevision: VO2Max.nativeMetadataParserRevision)
        flow.error = category; var next = state; next.vo2 = flow
        do { try commit(next) } catch { vo2OrphansReconciled = false; throw error }
    }
    /// One atomic archive -> pending transition. The archive remains a GC root
    /// until a validated durable ACK commits its candidate anchor.
    public func stageBlockedVO2(expectedHash: String, facts: [JSON], deleted: [String], observedAt: Date) throws {
        guard var flow = state.vo2, let blocked = flow.blocked, blocked.hash == expectedHash,
              flow.pending == nil, let scope = state.scope,
              Wire.hash(try Data(contentsOf: fileURL(blocked.file))) == expectedHash else {
            throw SyncFailure.localState("vo2_state_conflict")
        }
        // Older archives did not retain query-fullness. Conservatively require
        // a fresh anchored continuation after their ACK before claiming end.
        let hasMore = blocked.hasMore ?? true, batchID = Wire.id()
        let raw = try flow.body(scope: scope, facts: facts, deleted: deleted, observedAt: observedAt,
            hasMore: hasMore, batchID: batchID)
        let file = try savePayload(raw, suffix: "vo2.json")
        flow.pending = VO2Pending(batchID: batchID, file: file, hash: Wire.hash(raw),
            candidateAnchor: blocked.candidateAnchor, hasMore: hasMore, sourceBlockedHash: blocked.hash)
        flow.lastDiscovery = observedAt; flow.error = nil
        var next = state; next.vo2 = flow
        do { try commit(next) } catch { vo2OrphansReconciled = false; throw error }
    }
    public func recordBlockedVO2ParserAttempt(expectedHash: String, code: String) throws {
        guard var flow = state.vo2, var blocked = flow.blocked, blocked.hash == expectedHash,
              flow.pending == nil else { throw SyncFailure.localState("vo2_state_conflict") }
        blocked.parserRevision = VO2Max.nativeMetadataParserRevision
        flow.blocked = blocked; flow.error = code
        var next = state; next.vo2 = flow; try commit(next)
    }
    public func vo2Failure(code: String, retryAfter: TimeInterval? = nil, now: Date = Date()) throws {
        guard var flow = state.vo2 else { return }
        if VO2Max.blocksAutomaticDelivery(flow.error), code != flow.error { return }
        flow.error = code
        if var pending = flow.pending {
            pending.attempt += 1; pending.nextAttempt = now.addingTimeInterval(max(retryAfter ?? 0, UploadError.backoff(pending.attempt))); flow.pending = pending
        }
        var next = state; next.vo2 = flow; try commit(next)
    }
    public func validateVO2Receipt(_ receipt: JSON) throws {
        guard let flow = state.vo2, let pending = flow.pending, let scope = state.scope else { throw SyncFailure.localState("vo2_pending_missing") }
        guard receipt["contract"].text == VO2Max.contract, receipt["version"].integer == 1, receipt["metadataRegistryVersion"].integer == 1,
              receipt["batchID"].text == pending.batchID, receipt["datasetID"].text == scope.datasetID,
              receipt["datasetEpoch"].text == scope.datasetEpoch, receipt["collectorEpoch"].text == scope.collectorEpoch,
              receipt["streamID"].text == flow.streamID, receipt["clientSeq"].integer == flow.sequence,
              receipt["requestSHA256"].text == pending.hash, receipt["ingestStatus"].text == "committed",
              let revision = receipt["commitRevision"].integer, revision > 0,
              receipt["analysisStatus"].text == "awaiting_model_implementation",
              ["none", "deleted_precedence"].contains(receipt["deleteStatus"].text ?? "") else { throw SyncFailure.receiptMismatch }
        let raw = try Data(contentsOf: fileURL(pending.file))
        guard Wire.hash(raw) == pending.hash else { throw SyncFailure.localState("vo2_payload_corrupt") }
        let body = try JSON.decode(raw)
        var remaining = Int64(body["facts"].list.count + body["deletedUUIDs"].list.count)
        for key in ["inserted", "unchanged", "deleted", "tombstoned", "suppressed"] {
            guard let count = receipt["counts"][key].integer, count >= 0, count <= remaining else { throw SyncFailure.receiptMismatch }
            remaining -= count
        }
    }
    public func acknowledgeVO2(_ receipt: JSON, transferCacheFile: String? = nil) throws {
        try validateVO2Receipt(receipt)
        guard var flow = state.vo2, let pending = flow.pending else { throw SyncFailure.localState("vo2_pending_missing") }
        var archiveFiles: [String] = []
        if let blocked = flow.blocked {
            guard pending.sourceBlockedHash == blocked.hash else { throw SyncFailure.localState("vo2_state_conflict") }
            archiveFiles = [blocked.file]; flow.blocked = nil
        } else if pending.sourceBlockedHash != nil { throw SyncFailure.localState("vo2_state_conflict") }
        flow.anchor = pending.candidateAnchor; flow.previousBatchID = pending.batchID; flow.sequence += 1
        flow.bootstrapComplete = flow.bootstrapComplete || !pending.hasMore; flow.hasMore = pending.hasMore
        flow.pending = nil; flow.lastReceipt = receipt; flow.lastSync = Date(); flow.error = nil
        var next = state; next.vo2 = flow; next.reclaimFiles = Array(Set((next.reclaimFiles ?? []) + [pending.file] + archiveFiles + [transferCacheFile].compactMap { $0 })).sorted(); try commit(next)
    }
    /// Reconcile interrupted body-before-state writes on restart, bounded to
    /// this flow's private filenames. Published pending and blocked pages root GC.
    func reconcileVO2Orphans() throws {
        let roots = Set([state.vo2?.pending?.file, state.vo2?.blocked?.file].compactMap { $0 })
        let files = try FileManager.default.contentsOfDirectory(atPath: directory.path).filter {
            ($0.hasSuffix(".vo2.json") || $0.hasSuffix(".vo2-blocked.json")) && !roots.contains($0)
        }
        if !files.isEmpty { var next = state; next.reclaimFiles = Array(Set((next.reclaimFiles ?? []) + files)).sorted(); try commit(next) }
    }
}

public struct VO2MaxDelivery: Sendable {
    let store: OutboxStore, api: RAIAPIClient
    public init(store: OutboxStore, api: RAIAPIClient) { self.store = store; self.api = api }
    @discardableResult public func deliver(capabilities: JSON, now: Date = Date()) async throws -> Bool {
        guard VO2Max.supported(capabilities) else { throw SyncFailure.invalid("unsupported_capability") }
        let flow = try await store.vo2Flow()
        guard !VO2Max.blocksAutomaticDelivery(flow.error) else { return false }
        guard flow.blocked == nil || flow.pending?.sourceBlockedHash == flow.blocked?.hash else { return false }
        guard let pending = flow.pending else { return true }; guard pending.nextAttempt <= now else { return false }
        do {
            try Task.checkCancellation()
            let file = try await store.fileURL(pending.file)
            guard Wire.hash(try Data(contentsOf: file)) == pending.hash else { throw SyncFailure.localState("vo2_payload_corrupt") }
            let receipt = try await api.call("POST", "/v2/sync/vo2max-facts", file: file, identity: pending.batchID)
            try await store.acknowledgeVO2(receipt, transferCacheFile: api.cachedUploadResultFilename(pending.batchID)); api.discardUploadResult(pending.batchID)
            _ = try? await store.collectGarbage(dryRun: false)
            return true
        } catch {
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            // A cached 200 response that failed receipt validation is not an
            // authority. Discard that response only, preserving the original
            // body/identity so the server can replay its durable receipt.
            let code = VO2Max.failureCode(error)
            if ["invalid_receipt", "invalid_ack_response", "invalid_response", "oversized_response"].contains(code) { api.discardUploadResult(pending.batchID) }
            try await store.vo2Failure(code: code, retryAfter: (error as? UploadError)?.retryAfter)
            throw error
        }
    }
}
