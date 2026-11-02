import Foundation
import SyncCore
import Darwin

// Isolated synthetic acceptance executable. Credentials arrive only via the
// parent test's environment and are never printed or persisted in the Outbox.
@main struct DailyProbe {
    static func main() async throws {
        let args = CommandLine.arguments
        guard args.count == 3, let config = ProcessInfo.processInfo.environment["RAI_M5A_SYNTHETIC"],
              let value = config.data(using: .utf8) else { throw SyncFailure.invalid("synthetic_config_required") }
        let c = try JSON.decode(value), directory = URL(fileURLWithPath: args[1]), phase = args[2]
        let scope = Scope(serviceID: c["serviceID"].text!, datasetID: c["datasetID"].text!, datasetEpoch: c["datasetEpoch"].text!, collectorEpoch: c["collectorEpoch"].text!)
        let store = try OutboxStore(directory: directory); try await store.bind(scope)
        let api = RAIAPIClient(baseURL: URL(string: c["baseURL"].text!)!, token: c["token"].text!)
        if phase == "fairness" {
            let delivery = DailyHealthDelivery(store: store, api: api)
            for _ in 0..<16 {
                for name in DailyHealth.types {
                    let flow = try await store.dailyFlow(name)
                    if flow.discoveryDue(wakeGeneration: 0) {
                        let stamp = Int64((Date().timeIntervalSince1970 * 1_000_000).rounded())
                        var fields: [String: JSON] = ["uuid": .string(Wire.id()), "startUs": .int(stamp), "endUs": .int(stamp), "source": .object(["bundleID": .string("synthetic.fairness")])]
                        if name == DailyHealth.sleep { fields["category"] = .int(3) }
                        else { fields["quantity"] = .object(["value": .number(42), "unit": .string(name == DailyHealth.hrv ? "ms" : "count/min")]) }
                        try await store.stageDaily(type: name, previousAnchor: flow.anchor, candidateAnchor: Data([1]), facts: [.object(fields)], deleted: [], observedAt: Date(), hasMore: false)
                    }
                }
                for name in await store.dailyDeliveryOrder() { _ = try? await delivery.deliver(name) }
                let state = await store.snapshot()
                if DailyHealth.types.allSatisfy({ state.daily?[$0]?.anchor != nil && state.daily?[$0]?.pending == nil }) { print("M5A_FAIRNESS=PASS"); return }
                try await Task.sleep(for: .milliseconds(600))
            }
            throw SyncFailure.localState("daily_type_starved")
        }
        let type = DailyHealth.hrv
        let flow = try await store.dailyFlow(type)
        if flow.pending == nil && flow.anchor == nil {
            let stamp = Int64((Date().timeIntervalSince1970 * 1_000_000).rounded())
            let fact = JSON.object(["uuid": .string(Wire.id()), "startUs": .int(stamp), "endUs": .int(stamp),
                "quantity": .object(["value": .number(42), "unit": .string("ms")]),
                "source": .object(["bundleID": .string("synthetic.probe"), "version": .string("1"), "device": .object(["model": .string("Synthetic")])]),
                "metadata": .object(["HKAlgorithmVersion": .int(2), "HKTimeZone": .string("Asia/Shanghai")])])
            try await store.stageDaily(type: type, previousAnchor: nil, candidateAnchor: Data([1,2,3]), facts: [fact], deleted: [], observedAt: Date(), hasMore: false)
        }
        if phase == "before-upload" { try OutboxStore.write(Data(), to: directory.appendingPathComponent("ready")); while true { pause() } }
        if phase == "after-commit" {
            let pending = try await store.dailyFlow(type).pending!
            _ = try await api.call("POST", "/v2/sync/daily-facts", file: await store.fileURL(pending.file))
            try OutboxStore.write(Data(), to: directory.appendingPathComponent("ready")); while true { pause() }
        }
        _ = try await DailyHealthDelivery(store: store, api: api).deliver(type, now: .distantFuture)
        guard try await store.dailyFlow(type).anchor == Data([1,2,3]), try await store.dailyFlow(type).pending == nil else { throw SyncFailure.localState("anchor_not_committed") }
        print("M5A_SYNTHETIC_CLIENT=PASS")
    }
}
