import Foundation
import Synchronization
import Testing
@testable import SyncCore

private func completenessACK(_ raw: Data) throws -> JSON {
    let body = try JSON.decode(raw)
    var receipt = body.fields.filter { ["contract", "protocolVersion", "datasetID", "datasetEpoch", "collectorEpoch", "manifestID", "generation"].contains($0.key) }
    receipt["manifestSHA256"] = .string(Wire.hash(raw)); receipt["ingestStatus"] = .string("committed")
    receipt["commitRevision"] = .int(1); receipt["streamCount"] = .int(Int64(body["streams"].list.count)); receipt["workoutCount"] = .int(Int64(body["workouts"].list.count))
    return .object(receipt)
}
private final class CompletenessUploader: FileUploader, Sendable {
    let mode = Mutex(0), bodies = Mutex<[Data]>([])
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let raw = try Data(contentsOf: file); bodies.withLock { $0.append(raw) }
        let mode = mode.withLock { $0 }
        if mode == 0 { throw URLError(.networkConnectionLost) }
        return UploadResponse(data: try (mode == 1 ? JSON.object([:]) : completenessACK(raw)).data(), status: 200, contentType: "application/json")
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let reply = try await response(request, file: file, identity: identity); return (reply.data, reply.status, reply.retryAfter)
    }
}
private func completenessStore() async throws -> (OutboxStore, URL, Date) {
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-completeness-" + Wire.id())
    let store = try OutboxStore(directory: directory)
    try await store.bind(Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id()))
    let date = Date(timeIntervalSince1970: 1_793_000_000)
    _ = try await store.beginWorkoutCompleteness(now: date)
    return (store, directory, date)
}
private let completenessRoot = WorkoutCompletenessQuery(type: WorkoutCompleteness.workoutType)
private let fixturesDirectory = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    .deletingLastPathComponent().appendingPathComponent("contracts/fixtures")
private func completenessACKv2(_ raw: Data) throws -> JSON {
    let body = try JSON.decode(raw)
    var receipt = body.fields.filter { ["contract", "protocolVersion", "datasetID", "datasetEpoch", "collectorEpoch", "manifestID", "generation"].contains($0.key) }
    receipt["manifestSHA256"] = .string(Wire.hash(raw)); receipt["ingestStatus"] = .string("committed")
    receipt["commitRevision"] = .int(1); receipt["streamCount"] = .int(Int64(body["streams"].list.count))
    receipt["workoutCount"] = .int(Int64(body["workouts"].list.count))
    receipt["sourcePolicyVersion"] = body["sourcePolicyVersion"]
    for key in ["visibleCount", "eligibleCount", "excludedCount", "unclassifiedCount", "visibleSHA256", "eligibleSHA256", "excludedSHA256"] {
        receipt[key] = body["inventory"][key]
    }
    return .object(receipt)
}
private func v2CompletenessStore() async throws -> (OutboxStore, URL, Date) {
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-completeness-v2-" + Wire.id())
    let store = try OutboxStore(directory: directory)
    try await store.bind(Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id()))
    let date = Date(timeIntervalSince1970: 1_793_000_000)
    _ = try await store.beginWorkoutCompleteness(now: date, contract: WorkoutCompleteness.v2Contract)
    return (store, directory, date)
}
private func syntheticEvidence(_ bundle: String, product: String = "", manufacturer: String = "", model: String = "") -> JSON {
    WorkoutSourcePolicy.evidence(.object(["bundleID": .string(bundle), "productType": .string(product),
        "device": .object(["manufacturer": .string(manufacturer), "model": .string(model)])]))
}
private func syntheticProof(_ id: String) -> JSON {
    .object(["workoutID": .string(id), "bundleSHA256": .string(String(repeating: "a", count: 64)), "routeState": .string("absent"),
        "series": .array(WorkoutCompleteness.quantities.map { .object(["sampleType": .string($0), "sha256": .string(String(repeating: "b", count: 64)), "pointCount": .int(0), "queryCompletedAtUs": .int(1)]) })])
}

@Suite struct WorkoutCompletenessTests {
    @Test func sharedSwiftGoCanonicalPageAndManifestGolden() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("contracts/fixtures")
        let fixture = try JSON.decode(Data(contentsOf: root.appendingPathComponent("workout-completeness-v1.json")))
        let pageHash = try String(contentsOf: root.appendingPathComponent("workout-completeness-page-v1.sha256"), encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        let manifestHash = try String(contentsOf: root.appendingPathComponent("workout-completeness-v1.sha256"), encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        #expect(try Wire.hash(fixture["streams"].list[0]["pages"].list[0].data()) == pageHash)
        #expect(try Wire.hash(fixture.data()) == manifestHash)
        #expect(WorkoutCompleteness.fingerprint(from: fixture["windowFromUs"].integer!, key: "workouts") == fixture["streams"].list[0]["queryFingerprint"].text)
        let series = try JSON.decode(Data(contentsOf: root.appendingPathComponent("workout-completeness-series-v1.json")))
        let expectedSeries = try String(contentsOf: root.appendingPathComponent("workout-completeness-series-v1.sha256"), encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        let values = Dictionary(uniqueKeysWithValues: series.list.map { ($0["sampleUUID"].text!, $0["points"].list) })
        let digest = try WorkoutCompleteness.seriesDigest(values)
        #expect(digest.hash == expectedSeries && digest.count == 3)
    }
    @Test func lostACKOfflineCorruptACKAndRelaunchNeverAdvanceAnchorsEarly() async throws {
        let (store, directory, date) = try await completenessStore(); defer { try? FileManager.default.removeItem(at: directory) }
        try await store.captureCompletenessPage(completenessRoot, previousAnchor: nil, candidateAnchor: Data([1]), additions: [], deletions: [], hasMore: false, startedAt: date, completedAt: date.addingTimeInterval(1), archive: Data("synthetic".utf8))
        try await store.stageWorkoutCompleteness(now: date.addingTimeInterval(2))
        let pending = try #require(await store.snapshot().workoutCompleteness?.cycle), file = try #require(pending.pendingFile)
        let raw = try Data(contentsOf: await store.fileURL(file)), uploader = CompletenessUploader()
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", uploader: uploader)
        for mode in [0, 1] {
            uploader.mode.withLock { $0 = mode }
            let reopened = try OutboxStore(directory: directory)
            do { try await WorkoutCompletenessDelivery(store: reopened, api: api).deliver(); Issue.record("Failed ACK committed") } catch {}
            #expect(await reopened.snapshot().workoutCompleteness?.generation == 0)
            #expect(await reopened.snapshot().workoutCompleteness?.anchors.isEmpty == true)
            #expect(await reopened.snapshot().workoutCompleteness?.cycle?.pendingSHA256 == Wire.hash(raw))
        }
        let reopened = try OutboxStore(directory: directory); uploader.mode.withLock { $0 = 2 }
        try await WorkoutCompletenessDelivery(store: reopened, api: api).deliver()
        let complete = try #require(await reopened.snapshot().workoutCompleteness)
        #expect(complete.generation == 1 && complete.cycle == nil && complete.anchors["workouts"]?.anchor == Data([1]))
        #expect(uploader.bodies.withLock { $0.count == 3 && $0.allSatisfy { $0 == raw } })
        _ = try await reopened.beginWorkoutCompleteness(now: date.addingTimeInterval(10))
        let cursor = try await reopened.completenessCursor(completenessRoot)
        #expect(cursor.anchor == Data([1]) && !cursor.exhausted)
        try await reopened.captureCompletenessPage(completenessRoot, previousAnchor: cursor.anchor, candidateAnchor: Data([2]), additions: [], deletions: [], hasMore: false, startedAt: date.addingTimeInterval(10), completedAt: date.addingTimeInterval(11), archive: Data("synthetic empty continuation".utf8))
        let next = try #require(await reopened.snapshot().workoutCompleteness?.cycle)
        #expect(next.bootstrapQueries == 0 && next.committedAnchorQueries == 1)
        #expect(next.captures["workouts"]?.last?.page["sequence"] == .int(2))
        #expect(await reopened.snapshot().workoutCompleteness?.anchors["workouts"]?.anchor == Data([1]))
    }
    @Test func pendingStreamCannotFinalizeAndStagedCursorIsNotCommitted() async throws {
        let (store, directory, date) = try await completenessStore(); defer { try? FileManager.default.removeItem(at: directory) }
        try await store.captureCompletenessPage(completenessRoot, previousAnchor: nil, candidateAnchor: Data([1]), additions: [], deletions: [], hasMore: true, startedAt: date, completedAt: date, archive: Data("synthetic".utf8))
        do { try await store.stageWorkoutCompleteness(now: date); Issue.record("Pending query finalized") } catch {}
        #expect(await store.snapshot().workoutCompleteness?.anchors.isEmpty == true)
        let reopened = try OutboxStore(directory: directory), cursor = try await reopened.completenessCursor(completenessRoot)
        #expect(cursor.anchor == Data([1]) && !cursor.exhausted)
        try await reopened.captureCompletenessPage(completenessRoot, previousAnchor: cursor.anchor, candidateAnchor: Data([2]), additions: [], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic second page".utf8))
        try await reopened.stageWorkoutCompleteness(now: date)
        #expect(await reopened.snapshot().workoutCompleteness?.cycle?.stagedCursorQueries == 1)
        #expect(await reopened.snapshot().workoutCompleteness?.anchors.isEmpty == true)
    }
    @Test func payloadCorruptionAndStatePublishFailurePreserveAuthority() async throws {
        let (store, directory, date) = try await completenessStore(); defer { try? FileManager.default.removeItem(at: directory) }
        try await store.captureCompletenessPage(completenessRoot, previousAnchor: nil, candidateAnchor: Data([1]), additions: [], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic".utf8))
        try await store.stageWorkoutCompleteness(now: date)
        let cycle = try #require(await store.snapshot().workoutCompleteness?.cycle), file = try #require(cycle.pendingFile)
        let raw = try Data(contentsOf: await store.fileURL(file)), ack = try completenessACK(raw)
        let authority = directory.appendingPathComponent("state.json"), backup = directory.appendingPathComponent("saved-state")
        try FileManager.default.moveItem(at: authority, to: backup); try FileManager.default.createDirectory(at: authority, withIntermediateDirectories: false)
        do { try await store.acknowledgeWorkoutCompleteness(ack); Issue.record("Failed replacement committed") } catch {}
        #expect(await store.snapshot().workoutCompleteness?.generation == 0)
        try FileManager.default.removeItem(at: authority); try FileManager.default.moveItem(at: backup, to: authority)
        try OutboxStore.write(Data("corrupt synthetic payload".utf8), to: await store.fileURL(file))
        do { try await store.acknowledgeWorkoutCompleteness(ack); Issue.record("Corrupt body accepted") } catch {}
        #expect(await store.snapshot().workoutCompleteness?.anchors.isEmpty == true)
    }

    @Test func sharedSwiftGoCanonicalV2ManifestGolden() throws {
        let fixture = try JSON.decode(Data(contentsOf: fixturesDirectory.appendingPathComponent("workout-completeness-v2.json")))
        let pageHash = try String(contentsOf: fixturesDirectory.appendingPathComponent("workout-completeness-page-v2.sha256"), encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        let manifestHash = try String(contentsOf: fixturesDirectory.appendingPathComponent("workout-completeness-v2.sha256"), encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        #expect(try Wire.hash(fixture.data()) == manifestHash)
        #expect(fixture["contract"].text == WorkoutCompleteness.v2Contract && fixture["protocolVersion"].integer == 2)
        #expect(fixture["sourcePolicyVersion"].text == WorkoutSourcePolicy.version)
        #expect(fixture["sourcePolicySHA256"].text == WorkoutSourcePolicy.sha256)
        #expect(fixture["classificationVersion"].integer == WorkoutSourcePolicy.classificationVersion)
        let inventory = fixture["inventory"]
        #expect(inventory["visibleCount"].integer == inventory["eligibleCount"].integer! + inventory["excludedCount"].integer! + inventory["unclassifiedCount"].integer!)
        #expect(try Wire.hash(fixture["streams"].list[0]["pages"].list[0].data()) == pageHash)
        for stream in fixture["streams"].list {
            #expect(stream["queryFingerprint"].text == WorkoutCompleteness.fingerprint(from: fixture["windowFromUs"].integer!,
                key: stream["key"].text!, contract: WorkoutCompleteness.v2Contract))
        }
        for exclusion in fixture["exclusions"].list {
            #expect(exclusion["evidenceSHA256"].text == Wire.hash(try exclusion["sourceEvidence"].data()))
            #expect(WorkoutSourcePolicy.outcome(exclusion["classification"].text!) == exclusion["outcome"].text)
        }
    }
    @Test func v2ManifestBindsPolicyAndExcludesWithoutReceipts() async throws {
        let (store, directory, date) = try await v2CompletenessStore(); defer { try? FileManager.default.removeItem(at: directory) }
        let eligible = UUID().uuidString.lowercased(), excluded = UUID().uuidString.lowercased()
        try await store.captureCompletenessPage(completenessRoot, previousAnchor: nil, candidateAnchor: Data([1]),
            additions: [eligible, excluded], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic roots".utf8))
        try await store.completenessClassify(eligible, classification: "apple_watch",
            evidence: syntheticEvidence("com.apple.health", product: "Watch7,3", manufacturer: "Apple", model: "Watch7,3"))
        try await store.completenessClassify(excluded, classification: "third_party",
            evidence: syntheticEvidence("com.strava.Strava", product: "iPhone14,5"))
        for type in WorkoutCompleteness.quantities + [WorkoutCompleteness.routeType] {
            try await store.captureCompletenessPage(WorkoutCompletenessQuery(type: type, workoutID: eligible), previousAnchor: nil,
                candidateAnchor: Data([2]), additions: [], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic child".utf8))
        }
        try await store.completenessProof(eligible, proof: syntheticProof(eligible))
        try await store.stageWorkoutCompleteness(now: date.addingTimeInterval(1))
        let cycle = try #require(await store.snapshot().workoutCompleteness?.cycle)
        let raw = try Data(contentsOf: await store.fileURL(try #require(cycle.pendingFile)))
        let body = try JSON.decode(raw)
        #expect(body["contract"].text == WorkoutCompleteness.v2Contract && body["protocolVersion"].integer == 2)
        #expect(body["sourcePolicyVersion"].text == WorkoutSourcePolicy.version)
        #expect(body["sourcePolicySHA256"].text == WorkoutSourcePolicy.sha256)
        #expect(body["classificationVersion"].integer == WorkoutSourcePolicy.classificationVersion)
        #expect(body["inventory"]["visibleCount"].integer == 2 && body["inventory"]["eligibleCount"].integer == 1)
        #expect(body["inventory"]["excludedCount"].integer == 1 && body["inventory"]["unclassifiedCount"].integer == 0)
        #expect(body["inventory"]["visibleSHA256"].text == WorkoutSourcePolicy.inventorySHA256([eligible, excluded]))
        #expect(body["inventory"]["excludedSHA256"].text == WorkoutSourcePolicy.inventorySHA256([excluded]))
        #expect(body["workouts"].list.map { $0["workoutID"].text } == [eligible])
        #expect(body["exclusions"].list.map { $0["workoutIdentitySHA256"].text } == [WorkoutSourcePolicy.identitySHA256(excluded)])
        // Only the eligible Workout has receipt streams.
        #expect(body["streams"].list.count == 12)
        #expect(body["streams"].list.allSatisfy { $0["workoutID"].text == nil || $0["workoutID"].text == eligible })
        #expect(await store.snapshot().workoutCompleteness?.anchors.isEmpty == true)
        let ack = try completenessACKv2(raw)
        try await store.acknowledgeWorkoutCompleteness(ack)
        let flow = try #require(await store.snapshot().workoutCompleteness)
        #expect(flow.generation == 1 && flow.cycle == nil && flow.errorCode == nil)
        #expect(flow.inventory == [eligible, excluded].sorted())
        #expect(flow.proofs.keys.sorted() == [eligible])
        #expect(flow.anchors.count == 12)
    }
    @Test func v2UnclassifiedInventoryBlocksAndBadReceiptIsRejected() async throws {
        let (store, directory, date) = try await v2CompletenessStore(); defer { try? FileManager.default.removeItem(at: directory) }
        let unknown = UUID().uuidString.lowercased()
        try await store.captureCompletenessPage(completenessRoot, previousAnchor: nil, candidateAnchor: Data([1]),
            additions: [unknown], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic roots".utf8))
        try await store.completenessClassify(unknown, classification: "source_unknown", evidence: syntheticEvidence("com.apple.Fitness"))
        do { try await store.stageWorkoutCompleteness(now: date); Issue.record("Unclassified inventory committed") } catch {}
        #expect(await store.snapshot().workoutCompleteness?.cycle?.pendingFile == nil)
        #expect(await store.snapshot().workoutCompleteness?.anchors.isEmpty == true)
    }
    @Test func v2ReceiptMustRestateInventoryAndExclusionEvidence() async throws {
        let (store, directory, date) = try await v2CompletenessStore(); defer { try? FileManager.default.removeItem(at: directory) }
        let eligible = UUID().uuidString.lowercased(), excluded = UUID().uuidString.lowercased()
        try await store.captureCompletenessPage(completenessRoot, previousAnchor: nil, candidateAnchor: Data([1]),
            additions: [eligible, excluded], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic roots".utf8))
        try await store.completenessClassify(eligible, classification: "apple_watch", evidence: syntheticEvidence("com.apple.health", product: "Watch7,3", manufacturer: "Apple", model: "Watch7,3"))
        try await store.completenessClassify(excluded, classification: "third_party", evidence: syntheticEvidence("com.garmin.connect"))
        for type in WorkoutCompleteness.quantities + [WorkoutCompleteness.routeType] {
            try await store.captureCompletenessPage(WorkoutCompletenessQuery(type: type, workoutID: eligible), previousAnchor: nil,
                candidateAnchor: Data([2]), additions: [], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic child".utf8))
        }
        try await store.completenessProof(eligible, proof: syntheticProof(eligible))
        try await store.stageWorkoutCompleteness(now: date.addingTimeInterval(1))
        let cycle = try #require(await store.snapshot().workoutCompleteness?.cycle)
        let raw = try Data(contentsOf: await store.fileURL(try #require(cycle.pendingFile)))
        for tamper in ["excludedCount", "visibleSHA256", "sourcePolicyVersion", "protocolVersion"] {
            var receipt = try completenessACKv2(raw).fields
            switch tamper {
            case "excludedCount": receipt["excludedCount"] = .int(0)
            case "visibleSHA256": receipt["visibleSHA256"] = .string(String(repeating: "c", count: 64))
            case "sourcePolicyVersion": receipt["sourcePolicyVersion"] = .string("rai.workout.source_policy.all_sources/v1")
            default: receipt["protocolVersion"] = .int(1)
            }
            do { try await store.acknowledgeWorkoutCompleteness(.object(receipt)); Issue.record("Tampered receipt committed: \(tamper)") } catch {}
            #expect(await store.snapshot().workoutCompleteness?.generation == 0)
            #expect(await store.snapshot().workoutCompleteness?.anchors.isEmpty == true)
        }
        try await store.acknowledgeWorkoutCompleteness(try completenessACKv2(raw))
        #expect(await store.snapshot().workoutCompleteness?.generation == 1)
    }
    @Test func reviewedV2DiscardsOnlyUnsubmittedPageState() async throws {
        let (store, directory, date) = try await completenessStore(); defer { try? FileManager.default.removeItem(at: directory) }
        try await store.captureCompletenessPage(completenessRoot, previousAnchor: nil, candidateAnchor: Data([1]),
            additions: [], deletions: [], hasMore: false, startedAt: date, completedAt: date, archive: Data("synthetic v1 page".utf8))
        let upgraded = try await store.beginWorkoutCompleteness(now: date.addingTimeInterval(1), contract: WorkoutCompleteness.v2Contract)
        let cycle = try #require(upgraded.cycle)
        #expect(cycle.contract == WorkoutCompleteness.v2Contract)
        #expect(cycle.captures.isEmpty && cycle.proofs.isEmpty && cycle.inventory.isEmpty)
        #expect(upgraded.anchors.isEmpty && upgraded.generation == 0)
    }
}
