import Foundation
import SyncCore
import Darwin

// Synthetic macOS process-kill harness. This is not an iPhone kill test.
@main struct VO2Probe {
    static func main() async throws {
        guard CommandLine.arguments.count == 3, let encoded = ProcessInfo.processInfo.environment["RAI_M4B2_SYNTHETIC"] else { throw SyncFailure.invalid("synthetic_config_required") }
        let c = try JSON.decode(Data(encoded.utf8)), directory = URL(fileURLWithPath: CommandLine.arguments[1]), phase = CommandLine.arguments[2]
        let scope = Scope(serviceID: c["serviceID"].text!, datasetID: c["datasetID"].text!, datasetEpoch: c["datasetEpoch"].text!, collectorEpoch: c["collectorEpoch"].text!)
        let store = try OutboxStore(directory: directory); try await store.bind(scope)
        let flow = try await store.vo2Flow()
        func stop() throws -> Never { try OutboxStore.write(Data(), to: directory.appendingPathComponent("ready")); while true { pause() } }
        let candidate = Data([1,2,3])
        if phase == "after-candidate" { try stop() }
        if flow.pending == nil && flow.anchor == nil {
            let now = Date(), stamp = try VO2Max.microseconds(now)
            let p: JSON = .object(["schemaVersion": .string("rai.healthkit.provenance/2"), "device": .null,
                "sourceRevision": .object(["source": .object(["bundleIdentifier": .string("org.synthetic.vo2"), "name": .string("Synthetic")]), "version": .null, "productType": .null,
                    "operatingSystemVersion": .object(["major": .int(26), "minor": .int(0), "patch": .int(0)])])])
            let fact: JSON = .object(["uuid": .string(Wire.id()), "startUs": .int(stamp), "endUs": .int(stamp),
                "quantity": .object(["value": .number(42.125), "unit": .string("mL/kg/min")]), "provenance": p,
                "metadata": .object(["HKVO2MaxTestType": .object(["type": .string("int64"), "value": .int(2)])])])
            if phase == "after-body" {
                let body = try flow.body(scope: scope, facts: [fact], deleted: [], observedAt: now, hasMore: false, batchID: Wire.id())
                _ = try await store.savePayload(body, suffix: "vo2.json"); try stop()
            }
            try await store.stageVO2(previousAnchor: nil, candidateAnchor: candidate, facts: [fact], deleted: [], observedAt: now, hasMore: false, wakeGeneration: 0)
        }
        if phase == "before-upload" { try stop() }
        let api = RAIAPIClient(baseURL: URL(string: c["baseURL"].text!)!, token: c["token"].text!)
        if phase == "after-commit" || phase == "after-ack-validate" {
            let pending = try await store.vo2Flow().pending!
            let ack = try await api.call("POST", "/v2/sync/vo2max-facts", file: await store.fileURL(pending.file), identity: pending.batchID)
            if phase == "after-ack-validate" { try await store.validateVO2Receipt(ack) }
            try stop()
        }
        let caps: JSON = .object(["vo2maxFactsVersion": .int(1), "vo2maxMetadataRegistryVersion": .int(1), "vo2maxMaxChanges": .int(128), "vo2maxMaxBytes": .int(524288)])
        if phase == "fairness" {
            for type in DailyHealth.types {
                let d = try await store.dailyFlow(type), stamp = try VO2Max.microseconds(Date())
                var fields: [String: JSON] = ["uuid": .string(Wire.id()), "startUs": .int(stamp), "endUs": .int(stamp), "source": .object(["bundleID": .string("org.synthetic.daily")])]
                if type == DailyHealth.sleep { fields["category"] = .int(3) }
                else { fields["quantity"] = .object(["value": .number(42), "unit": .string(type == DailyHealth.hrv ? "ms" : "count/min")]) }
                try await store.stageDaily(type: type, previousAnchor: d.anchor, candidateAnchor: candidate, facts: [.object(fields)], deleted: [], observedAt: Date(), hasMore: false)
            }
            for _ in 0..<40 {
                for type in await store.healthDeliveryOrder() {
                    if type == VO2Max.type { _ = try? await VO2MaxDelivery(store: store, api: api).deliver(capabilities: caps) }
                    else { _ = try? await DailyHealthDelivery(store: store, api: api).deliver(type) }
                }
                let state = await store.snapshot()
                if state.vo2?.anchor != nil && DailyHealth.types.allSatisfy({ state.daily?[$0]?.anchor != nil }) {
                    print("M4B2_SHARED_FLOW_FAIRNESS=PASS"); return
                }
                try await Task.sleep(for: .milliseconds(600))
            }
            throw SyncFailure.localState("health_flow_starved")
        }
        _ = try await VO2MaxDelivery(store: store, api: api).deliver(capabilities: caps, now: .distantFuture)
        let final = await store.snapshot()
        guard final.vo2?.anchor == candidate, final.vo2?.pending == nil else { throw SyncFailure.localState("anchor_not_committed") }
        _ = try await store.collectGarbage(dryRun: false)
        print("M4B2_SYNTHETIC_CLIENT=PASS")
    }
}
