// Synthetic integration harness for the same SyncCore used by the iPhone App.
// This is a command-line test tool; it never accesses HealthKit or real data.
import Foundation
import SyncCore

struct ProbeConfiguration: Decodable { let baseURL: URL; let token: String; let collectorEpoch: String }
actor DropCommittedResponse: FileUploader {
    let suffix: String
    var dropped = false
    init(suffix: String) { self.suffix = suffix }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let (data, response) = try await URLSession.shared.upload(for: request, fromFile: file)
        guard let response = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        if response.statusCode == 200, request.url!.path.hasSuffix(suffix), !dropped {
            dropped = true
            // The actual fnOS request committed, then its response is withheld
            // from SyncCore. A second process must recover the original receipt.
            throw URLError(.networkConnectionLost)
        }
        return (data, response.statusCode, response.value(forHTTPHeaderField: "Retry-After").flatMap(Double.init))
    }
}

@main struct Probe {
    static func require(_ condition: Bool, _ code: String) throws { if !condition { throw SyncFailure.invalid(code) } }
    static func drain(_ store: OutboxStore, _ api: RAIAPIClient, stopAfterLostResponse: Bool = false) async throws {
        let pipeline = OutboxPipeline(store: store, api: api)
        for _ in 0..<24 {
            try await pipeline.drain()
            let state = await store.snapshot()
            if state.pages.isEmpty { return }
            if stopAfterLostResponse, state.pages.contains(where: { $0.error?.code == "network_-1005" }) { return }
            if let error = state.pages.compactMap(\.error).first, !error.retryable { throw error }
            if let next = state.pages.map(\.nextAttempt).min() { try await Task.sleep(for: .seconds(max(0.1, next.timeIntervalSinceNow))) }
        }
        throw SyncFailure.invalid("probe_retry_budget_exhausted")
    }
    static func holdForKill() async throws {
        try FileHandle.standardOutput.write(contentsOf: Data("PROBE_KILL_READY=synthetic-macOS-process-only\n".utf8))
        while true { try await Task.sleep(for: .seconds(30)) }
    }
    static func main() async {
        do { try await run() }
        catch let error as UploadError { print("PROBE_FAIL " + error.code); exit(1) }
        catch let error as SyncFailure { print("PROBE_FAIL " + String(describing: error)); exit(1) }
        catch { print("PROBE_FAIL local_or_transport_failure"); exit(1) }
    }
    static func run() async throws {
        let args = CommandLine.arguments
        guard args.count == 4 else { throw SyncFailure.invalid("usage: SyncProbe mode private-config state-directory") }
        let mode = args[1], directory = URL(fileURLWithPath: args[3], isDirectory: true)
        let config = try JSONDecoder().decode(ProbeConfiguration.self, from: Data(contentsOf: URL(fileURLWithPath: args[2])))
        let api = RAIAPIClient(baseURL: config.baseURL, token: config.token)
        let hello = try await api.call("GET", "/v2/handshake")
        let scope = Scope(serviceID: hello["serviceID"].text!, datasetID: hello["datasetID"].text!, datasetEpoch: hello["datasetEpoch"].text!, collectorEpoch: config.collectorEpoch)
        let store = try OutboxStore(directory: directory); try await store.bind(scope)
        let type = "HKQuantityTypeIdentifierHeartRate", workoutType = "HKWorkoutTypeIdentifier"
        switch mode {
        case "prepare", "prepare-kill":
            let stream = try await store.stream(type: type, query: "synthetic-probe-only")
            let time: Int64 = 1793059200000000
            let sample: JSON = .object(["op": .string("upsert"), "kind": .string("sample"), "type": .string(type), "uuid": .string(Wire.id()),
                "startUs": .int(time), "endUs": .int(time), "quantity": .object(["value": .int(120), "unit": .string("count/min")]), "source": .object(["bundleID": .string("synthetic.p35")])])
            try OutboxStore.write(JSON.object(["sampleUUID": sample["uuid"]]).data(), to: directory.appendingPathComponent("synthetic-identities.json"))
            let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [sample], anchor: Data("synthetic-sample-A".utf8), store: store)
            try await store.stage(page)
            let drop = DropCommittedResponse(suffix: "/body")
            let faultAPI = RAIAPIClient(baseURL: config.baseURL, token: config.token, uploader: drop)
            try await OutboxPipeline(store: store, api: faultAPI).drain()
            let failed = await store.snapshot()
            try require(failed.pages.count == 1 && failed.streams[type]?.deliveredAnchor == nil && failed.pages[0].cursor == 1, "response_loss_not_preserved")
            print("PROBE_PREPARE=PASS committed-response-lost; anchor retained; immutable batch preserved")
            if mode == "prepare-kill" { try await holdForKill() }
        case "resume":
            try await store.retryBlocked(); try await drain(store, api)
            try require(await store.snapshot().pages.isEmpty, "resume_failed")
            try require(await store.snapshot().streams[type]?.deliveredAnchor == Data("synthetic-sample-A".utf8), "anchor_not_promoted")
            print("PROBE_RESUME=PASS same batch original receipt; anchor promoted after page seal")
        case "workout":
            let id = Wire.id(), start: Int64 = 1793059200000000, stream = try await store.stream(type: workoutType, query: "synthetic-probe-only")
            let event: JSON = .object(["op": .string("upsert"), "kind": .string("workout"), "type": .string(workoutType), "uuid": .string(id),
                "startUs": .int(start), "endUs": .int(start + 6000_000_000), "source": .object(["bundleID": .string("synthetic.p35")]),
                "workout": .object(["activityType": .int(37), "durationUs": .int(6000_000_000)])])
            let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [event], anchor: Data("synthetic-workout-A".utf8), store: store,
                workouts: [.init(id: id, source: "synthetic.p35", current: event)])
            try await store.stage(page); try await drain(store, api)
            try require(await store.snapshot().pages.isEmpty, "workout_upload_failed")
            print("PROBE_WORKOUT=PASS durable independent Workout")
        case "route", "route-kill":
            let workout = await store.snapshot().workouts.values.first!
            let spool = try RouteSpool(directory: directory, chunkSize: 1000)
            for i in 0..<5001 { try spool.append([.init(recordedAtUs: 1793059200000000 + Int64(i) * 1_000_000, latitude: 31.2 + Double(i) * 0.000001, longitude: 121.4, horizontalAccuracy: 5)]) }
            let route = try spool.finish()
            let operations = try await route.operations(scope: scope, routeID: Wire.id(), workout: workout, source: "synthetic.p35", store: store)
            let page = PendingPage(anchorKey: "route:" + workout.id, candidateAnchor: Data("synthetic-route-A".utf8), operations: operations)
            try await store.stage(page)
            let faultAPI = RAIAPIClient(baseURL: config.baseURL, token: config.token, uploader: DropCommittedResponse(suffix: "/chunks/2"))
            try await drain(store, faultAPI, stopAfterLostResponse: true)
            let failed = await store.snapshot()
            if let error = failed.pages.first?.error { print("PROBE_ROUTE_PENDING_CODE=" + error.code) }
            try require(failed.pages.first?.cursor == 3 && failed.localAnchors[page.anchorKey] == nil && failed.pages.first?.error?.code == "network_-1005", "route_recovery_not_staged")
            print("PROBE_ROUTE=PASS 5001 points; chunk 3 committed response lost; route anchor retained")
            if mode == "route-kill" { try await holdForKill() }
        case "route-resume":
            try await store.retryBlocked(); try await drain(store, api)
            let snapshot = await store.snapshot()
            if let error = snapshot.pages.first?.error { print("PROBE_ROUTE_PENDING_CODE=" + error.code) }
            try require(snapshot.pages.isEmpty && snapshot.localAnchors.values.contains(Data("synthetic-route-A".utf8)), "route_resume_failed")
            print("PROBE_ROUTE_RESUME=PASS exact chunk retry; six chunks sealed; local route anchor promoted")
        case "empty":
            let stream = await store.snapshot().streams[type]!
            let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [], anchor: stream.deliveredAnchor!, store: store)
            try await store.stage(page); try await drain(store, api)
            try require(await store.snapshot().pages.isEmpty, "empty_page_failed")
            print("PROBE_EMPTY=PASS zero new facts; sealed empty page")
        case "relationships":
            let workout = await store.snapshot().workouts.values.first!
            let identities = try JSON.decode(Data(contentsOf: directory.appendingPathComponent("synthetic-identities.json")))
            var association = scope.fields
            association.merge(["associationID": .string(Wire.id()), "sampleType": .string(type), "sampleUUID": identities["sampleUUID"],
                "workoutUUID": .string(workout.id), "source": .object(["bundleID": .string("synthetic.p35")])]) { _, b in b }
            let file = try await store.savePayload(JSON.object(association).data())
            let expected: [String: JSON] = ["sampleType": .string(type), "sampleUUID": identities["sampleUUID"], "workoutUUID": .string(workout.id), "ingestStatus": .string("committed")]
            let original: OutboxOperation = .request(.init(method: "POST", path: "/v2/sync/associations", file: file, expected: expected))
            association["associationID"] = .string(Wire.id())
            let sameRelation = try await store.savePayload(JSON.object(association).data())
            var enrichment = scope.fields; let enrichmentID = Wire.id()
            enrichment.merge(["enrichmentID": .string(enrichmentID), "workoutID": .string(workout.id), "source": .object(["bundleID": .string("synthetic.p35")]),
                "sourceVersion": .int(1), "observedAtUs": .int(1793059200000000), "totalDistanceM": .int(5000)]) { _, b in b }
            let enrichmentFile = try await store.savePayload(JSON.object(enrichment).data())
            let supplement: OutboxOperation = .request(.init(method: "POST", path: "/v2/sync/workout-enrichments", file: enrichmentFile,
                expected: ["enrichmentID": .string(enrichmentID), "workoutID": .string(workout.id), "ingestStatus": .string("committed")]))
            let operations = [original, original, .request(.init(method: "POST", path: "/v2/sync/associations", file: sameRelation, expected: expected)), supplement, supplement]
            let page = PendingPage(anchorKey: "probe-association-enrichment", candidateAnchor: Data("synthetic-only".utf8), operations: operations)
            try await store.stage(page); try await drain(store, api)
            try require(await store.snapshot().pages.isEmpty, "relationship_or_enrichment_failed")
            print("PROBE_RELATIONSHIPS=PASS sample-first late association; same-relation replay; immutable enrichment replay")
        default: throw SyncFailure.invalid("unknown_probe_mode")
        }
    }
}
