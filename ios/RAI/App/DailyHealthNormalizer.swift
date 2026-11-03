import Foundation
import CoreFoundation
import HealthKit
import SyncCore

enum DailyHealthNormalizer {
    static func categoryValue(_ value: Int) throws -> Int {
        let supported: Set<Int> = [HKCategoryValueSleepAnalysis.inBed.rawValue, HKCategoryValueSleepAnalysis.asleepUnspecified.rawValue,
            HKCategoryValueSleepAnalysis.awake.rawValue, HKCategoryValueSleepAnalysis.asleepCore.rawValue,
            HKCategoryValueSleepAnalysis.asleepDeep.rawValue, HKCategoryValueSleepAnalysis.asleepREM.rawValue]
        guard supported.contains(value) else { throw SyncFailure.invalid("invalid_category") }
        return value
    }
    static let integerKeys = [HKMetadataKeyAlgorithmVersion, HKMetadataKeySyncVersion, HKMetadataKeyHeartRateMotionContext,
                              HKMetadataKeyHeartRateSensorLocation, HKMetadataKeyUserMotionContext, HKMetadataKeyDevicePlacementSide]
    static func fact(_ sample: HKSample) throws -> JSON {
        let type = sample.sampleType.identifier
        guard DailyHealth.types.contains(type) else { throw SyncFailure.invalid("unsupported_type") }
        var fact: [String: JSON] = ["uuid": .string(sample.uuid.uuidString.lowercased()), "startUs": .int(Normalizer.us(sample.startDate)),
                                    "endUs": .int(Normalizer.us(sample.endDate)), "source": Normalizer.source(sample)]
        if let quantity = sample as? HKQuantitySample, type != DailyHealth.sleep {
            guard quantity.count == 1 else { throw SyncFailure.invalid("daily_series_requires_version") }
            fact["quantity"] = try Normalizer.quantity(quantity.quantity, type: type)
        } else if let category = sample as? HKCategorySample, type == DailyHealth.sleep {
            // Read symbols from the current SDK, retaining raw values on wire.
            fact["category"] = .int(Int64(try categoryValue(category.value)))
        } else { throw SyncFailure.invalid("invalid_payload") }
        var metadata: [String: JSON] = [:]
        for (key, value) in sample.metadata ?? [:] {
            if integerKeys.contains(key) {
                guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(), number.doubleValue.isFinite else { throw SyncFailure.invalid("unsupported_metadata") }
                // Preserve Int64 metadata beyond Double's exact integer range.
                guard let integer = Int64(number.stringValue) ?? Int64(exactly: number.doubleValue) else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .int(integer)
            } else if key == HKMetadataKeyTimeZone {
                guard let text = value as? String, !text.isEmpty, TimeZone(identifier: text) != nil else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .string(text)
            } else if key == HKMetadataKeyWasUserEntered {
                guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .bool(number.boolValue)
            } else if key == HKMetadataKeySyncIdentifier {
                guard let text = value as? String, !text.isEmpty else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .string(text)
            } else { throw SyncFailure.invalid("unsupported_metadata") }
        }
        guard (metadata[HKMetadataKeySyncIdentifier] == nil) == (metadata[HKMetadataKeySyncVersion] == nil) else { throw SyncFailure.invalid("unsupported_metadata") }
        if !metadata.isEmpty { fact["metadata"] = .object(metadata) }
        return .object(fact)
    }
}

#if DEBUG
import Network
/// Explicit M5A-RD diagnostics only. Never accepts a fact, UUID, interval,
/// metadata dictionary, credential or HK object; callers supply aggregates.
actor DailyDeviceDiagnostics {
    static let shared = DailyDeviceDiagnostics()
    static var enabled: Bool { ProcessInfo.processInfo.environment["RAI_M5A_RD_ACCEPTANCE"] == "1" }
    nonisolated static func probeLocalTransport(_ origin: String?) {
        guard enabled, let origin, let url = URL(string: origin), let host = url.host,
              let port = NWEndpoint.Port(rawValue: UInt16(url.port ?? 80)) else { return }
        let connection = NWConnection(host: NWEndpoint.Host(host), port: port, using: .tcp)
        connection.stateUpdateHandler = { state in
            let label: String
            switch state { case .ready: label = "ready"; case .waiting: label = "waiting"; case .failed: label = "failed"; default: return }
            let reason = connection.currentPath.map { String(describing: $0.unsatisfiedReason) } ?? "unknown"
            var fields: [String: JSON] = ["state": .string(label), "pathReason": .string(reason)]
            if case .waiting(let error) = state {
                switch error {
                case .dns(let value): fields["dnsCode"] = .int(Int64(value))
                case .posix(let value): fields["posixCode"] = .int(Int64(value.rawValue))
                case .tls(let value): fields["tlsCode"] = .int(Int64(value))
                @unknown default: break
                }
            }
            let record = fields
            Task { try? await shared.event("local_transport", fields: record) }
        }
        connection.start(queue: DispatchQueue(label: "com.rai.m5ard.transport"))
        DispatchQueue.global().asyncAfter(deadline: .now() + 5) { connection.cancel() }
    }
    private var events: [JSON] = []
    private var path: URL {
        FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0].appendingPathComponent("m5a-rd-report.json")
    }
    func begin(_ command: String) throws {
        if let data = try? Data(contentsOf: path), let report = try? JSON.decode(data) { events = report["events"].list }
        try event("run_started", fields: ["command": .string(command), "healthKitAvailable": .bool(HKHealthStore.isHealthDataAvailable()),
            "appVersion": .string(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "unknown"),
            "appBuild": .string(Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "unknown")])
    }
    func event(_ name: String, fields: [String: JSON] = [:]) throws {
        guard Self.enabled else { return }
        var item = fields; item["event"] = .string(name); item["index"] = .int(Int64(events.count))
        events.append(.object(item))
        // Bounded diagnostics. Never deletes or mutates sync state.
        if events.count > 4096 { events.removeFirst(events.count - 4096) }
        try OutboxStore.write(JSON.object(["version": .int(1), "healthValuesExported": .bool(false), "events": .array(events)]).data(), to: path)
    }
    func snapshot(_ label: String, store: OutboxStore) async throws {
        let state = await store.snapshot()
        var flows: [String: JSON] = [:]
        for type in DailyHealth.types {
            guard let flow = state.daily?[type] else { flows[type] = .object(["exists": .bool(false)]); continue }
            var fields: [String: JSON] = ["exists": .bool(true), "sequence": .int(flow.sequence), "bootstrapComplete": .bool(flow.bootstrapComplete),
                "hasMore": .bool(flow.hasMore), "anchorPresent": .bool(flow.anchor != nil), "pending": .bool(flow.pending != nil),
                "anchorFingerprint": flow.anchor.map { .string(Wire.hash($0)) } ?? .null,
                "bootstrapWindowFingerprint": .string(Wire.hash(Data(String(flow.bootstrapStartUs).utf8))),
                "receiptCommitted": .bool(flow.lastReceipt?["ingestStatus"].text == "committed"), "hasError": .bool(flow.error != nil)]
            if let error = flow.error, error.range(of: #"^[a-z0-9_-]{1,80}$"#, options: .regularExpression) != nil { fields["errorCode"] = .string(error) }
            if let pending = flow.pending {
                fields["batchFingerprint"] = .string(Wire.hash(Data(pending.batchID.utf8)))
                fields["bodyFingerprint"] = .string(pending.hash)
                fields["candidateAnchorFingerprint"] = .string(Wire.hash(pending.candidateAnchor))
                fields["retryAttempt"] = .int(Int64(pending.attempt))
                let file = try await store.fileURL(pending.file)
                fields["batchBytes"] = .int(Int64((try? file.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0))
            }
            flows[type] = .object(fields)
        }
        try event("state", fields: ["label": .string(label), "flows": .object(flows), "wakeGeneration": .int(state.dailyWakeGeneration ?? 0)])
    }
}
#endif
