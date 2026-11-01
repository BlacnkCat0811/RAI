import Foundation
import Testing
@testable import SyncCore

private func temporaryDirectory() throws -> URL {
    let url = FileManager.default.temporaryDirectory.appendingPathComponent("rai-workout-task-test-" + Wire.id())
    try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
    return url
}
private let scope = Scope(serviceID: "11111111-1111-4111-8111-111111111111", datasetID: "22222222-2222-4222-8222-222222222222",
    datasetEpoch: "33333333-3333-4333-8333-333333333333", collectorEpoch: "44444444-4444-4444-8444-444444444444")
private let workoutType = "HKWorkoutTypeIdentifier"
private let workoutA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
private let workoutB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

private func workoutEvent(_ id: String) throws -> JSON {
    .object(["op": .string("upsert"), "kind": .string("workout"), "type": .string(workoutType), "uuid": .string(id),
             "startUs": .int(1_793_059_200_000_000), "endUs": .int(1_793_061_600_000_000),
             "source": .object(["bundleID": .string("com.example.synthetic")]),
             "workout": .object(["activityType": .int(37), "durationUs": .int(2_400_000_000)])])
}

@Suite(.serialized)
struct WorkoutTaskTests {
    @Test func perWorkoutStreamsAreIndependent() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let keyA = "workout:" + workoutA + ":" + workoutType
        let keyB = "workout:" + workoutB + ":" + workoutType
        let a = try await store.stream(key: keyA, type: workoutType, query: "per-workout:" + workoutA + ":visible")
        let b = try await store.stream(key: keyB, type: workoutType, query: "per-workout:" + workoutB + ":visible")
        #expect(a.id != b.id)
        #expect(a.fingerprint != b.fingerprint)
        let again = try await store.stream(key: keyA, type: workoutType, query: "per-workout:" + workoutA + ":visible")
        #expect(again.id == a.id)
        let state = await store.snapshot()
        #expect(state.streams[keyA]?.id == a.id)
        #expect(state.streams[keyB]?.id == b.id)
        #expect(state.streams[workoutType] == nil)
    }

    @Test func oneWorkoutPageCarriesOneWorkoutEventAndTaskIdentity() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let key = "workout:" + workoutA + ":" + workoutType
        let stream = try await store.stream(key: key, type: workoutType, query: "per-workout:" + workoutA + ":visible")
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [try workoutEvent(workoutA)], anchor: Data([1]),
            store: store, pageKey: key, taskID: workoutA)
        #expect(page.taskID == workoutA)
        #expect(page.anchorKey == key)
        #expect(page.operations.count >= 2)
        if case .batch(let envelope, _) = page.operations[1] {
            #expect(envelope.eventCount == 1)
            #expect(envelope.streamID == stream.id)
        } else { Issue.record("expected batch operation") }
    }

    @Test func taskRetryOnlyTouchesItsOwnPages() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA); try await store.beginTask(workoutB)
        let pageA = PendingPage(anchorKey: "a", candidateAnchor: Data([1]), operations: [], taskID: workoutA)
        let pageB = PendingPage(anchorKey: "b", candidateAnchor: Data([2]), operations: [], taskID: workoutB)
        try await store.stage(pageA); try await store.stage(pageB)
        try await store.fail(pageA.id, error: UploadError(code: "network", status: 0))
        try await store.fail(pageB.id, error: UploadError(code: "network", status: 0))
        try await store.retryBlocked(taskID: workoutA)
        let state = await store.snapshot()
        let a = try #require(state.pages.first { $0.taskID == workoutA })
        let b = try #require(state.pages.first { $0.taskID == workoutB })
        #expect(a.error == nil); #expect(a.nextAttempt == .distantPast)
        #expect(b.error?.code == "network"); #expect(b.nextAttempt > Date())
        #expect(await store.pages(taskID: workoutA).count == 1)
        #expect(await store.pages(taskID: workoutB).count == 1)
    }

    @Test func taskStateProjectionAndRestart() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA)
        try await store.updateTask(workoutA) { task in
            task.phase = .partial
            task.coverage = [workoutType: WorkoutCoverage(state: .available, sampleCount: 1),
                             "HKQuantityTypeIdentifierHeartRate": WorkoutCoverage(state: .unavailable)]
            task.metrics = ["workout_read_ms": 12.5]
            task.acknowledgedAt = Date()
        }
        let restored = try OutboxStore(directory: directory)
        let task = try #require(await restored.task(workoutA))
        #expect(task.phase == .partial)
        #expect(task.coverage[workoutType]?.sampleCount == 1)
        #expect(task.missing == ["HKQuantityTypeIdentifierHeartRate"])
        #expect(task.metrics["workout_read_ms"] == 12.5)
    }

    @Test func stagedTaskAcknowledgesOnlyAfterAllPagesDeliver() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA)
        let operation = RequestOperation(method: "POST", path: "/v2/synthetic", expected: ["ingestStatus": .string("committed")])
        let page = PendingPage(anchorKey: "synthetic-task-page", candidateAnchor: Data([9]), operations: [.request(operation)], taskID: workoutA)
        try await store.stage(page)
        try await store.updateTask(workoutA) { $0.stagedAll = true }
        try await store.acknowledge(page.id, cursor: 0, receipt: .object(operation.expected))
        let task = try #require(await store.task(workoutA))
        #expect(task.phase == .acknowledged)
        #expect(task.acknowledgedAt != nil)
    }

    @Test func earlyPageCannotMarkWholeTaskAcknowledged() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA)
        let operation = RequestOperation(method: "POST", path: "/v2/synthetic", expected: ["ingestStatus": .string("committed")])
        let page = PendingPage(anchorKey: "synthetic-early-page", candidateAnchor: Data([9]), operations: [.request(operation)], taskID: workoutA)
        try await store.stage(page)
        try await store.acknowledge(page.id, cursor: 0, receipt: .object(operation.expected))
        let task = try #require(await store.task(workoutA))
        #expect(task.phase == .preparing)
    }

    @Test func permanentlyRejectedTaskPageSettlesAsFailed() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA)
        let operation = RequestOperation(method: "POST", path: "/v2/synthetic", expected: ["ingestStatus": .string("committed")])
        let page = PendingPage(anchorKey: "synthetic-rejected-page", candidateAnchor: Data([9]), operations: [.request(operation)], taskID: workoutA)
        try await store.stage(page)
        try await store.fail(page.id, error: UploadError(code: "sample_identity_conflict", status: 409))
        try await store.reconcileTasks()
        let task = try #require(await store.task(workoutA))
        #expect(task.phase == .failed)
        #expect(task.errorCode == "sample_identity_conflict")
        #expect(await store.pages(taskID: workoutA).count == 1)
    }

    @Test func interruptedPreparationBecomesRetryableFailure() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA)
        try await store.reconcileTasks()
        let task = try #require(await store.task(workoutA))
        #expect(task.phase == .failed)
        #expect(task.errorCode == "interrupted")
    }

    @Test func rejectedIdentityPageCanBeDiscardedBeforeItsBatchCommits() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.bind(scope)
        try await store.beginTask(workoutA)
        let key = "workout:" + workoutA + ":" + workoutType
        let stream = try await store.stream(key: key, type: workoutType, query: "per-workout:" + workoutA + ":visible")
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [try workoutEvent(workoutA)], anchor: Data([1]),
            store: store, pageKey: key, taskID: workoutA)
        try await store.stage(page)
        try await store.fail(page.id, error: UploadError(code: "sample_identity_conflict", status: 409))
        #expect(try await store.discardRejectedTaskPages(taskID: workoutA, errorCode: "sample_identity_conflict") == 1)
        #expect(await store.pages(taskID: workoutA).isEmpty)
        #expect(await store.snapshot().pages.isEmpty)
    }

    @Test func oldWorkoutReferenceStillDecodesWithoutProvenanceVersion() throws {
        let data = Data("""
        {"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","source":"com.example.synthetic","current":null,"enrichmentVersion":2}
        """.utf8)
        let reference = try JSONDecoder().decode(WorkoutReference.self, from: data)
        #expect(reference.enrichmentVersion == 2)
        #expect(reference.provenanceVersion == nil)
    }

    @Test func provenanceVersionCanBeRecordedForOneWorkout() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.recordWorkout(WorkoutReference(id: workoutA, source: "com.example.synthetic", provenanceVersion: 123))
        #expect(await store.snapshot().workouts[workoutA]?.provenanceVersion == 123)
    }

    @Test func legacyRelationshipPageIsAdoptedByExactlyOneTask() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let key = "association:" + workoutA + ":HKQuantityTypeIdentifierHeartRate"
        let page = PendingPage(anchorKey: key, candidateAnchor: Data([7]), operations: [])
        try await store.stage(page)
        #expect(await store.snapshot().pages.first?.isLegacy == true)
        #expect(try await store.adoptLegacyPage(anchorKey: key, taskID: workoutA) == true)
        let adopted = try #require(await store.snapshot().pages.first)
        #expect(adopted.taskID == workoutA)
        #expect(adopted.operations.isEmpty)
        #expect(await store.pages(taskID: workoutA).count == 1)
        #expect(try await store.adoptLegacyPage(anchorKey: key, taskID: workoutB) == false)
    }

    @Test func legacyStateWithoutTasksStillDecodes() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA)
        let encoded = try Data(contentsOf: directory.appendingPathComponent("state.json"))
        var object = try JSONSerialization.jsonObject(with: encoded) as! [String: Any]
        object.removeValue(forKey: "tasks")
        let stripped = try JSONSerialization.data(withJSONObject: object)
        try stripped.write(to: directory.appendingPathComponent("state.json"))
        let restored = try OutboxStore(directory: directory)
        #expect(await restored.allTasks().isEmpty)
    }
}

    @Test func bundleFileIsDurableAndReclaimedAfterAck() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        try await store.beginTask(workoutA)
        let file = try await store.savePayload(Data("multipart".utf8), suffix: "multipart.json")
        try await store.setBundle(workoutA, file: file, sha256: String(repeating: "a", count: 64), boundary: "b")
        let task = try #require(await store.task(workoutA))
        #expect(task.bundleFile == file)
        let url = try await store.fileURL(file)
        #expect(FileManager.default.fileExists(atPath: url.path))
        _ = try await store.collectGarbage(dryRun: false)
        #expect(FileManager.default.fileExists(atPath: url.path))
        do { try await store.releaseBundle(workoutA); Issue.record("unacknowledged bundle released") } catch { }
        #expect(FileManager.default.fileExists(atPath: url.path))
        try await store.acknowledgeBundle(workoutA, receipt: durableWorkoutACK(workoutA, String(repeating: "a", count: 64)))
        _ = try await store.collectGarbage(dryRun: false)
        #expect(!FileManager.default.fileExists(atPath: url.path))
        #expect(await store.task(workoutA)?.bundleFile == nil)
    }

    @Test func legacyPagesArePurgedWithTheirFiles() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let file = try await store.savePayload(Data("legacy".utf8))
        let operation = RequestOperation(method: "POST", path: "/v2/synthetic", file: file, expected: [:])
        let page = PendingPage(anchorKey: "legacy", candidateAnchor: Data([1]), operations: [.request(operation)])
        try await store.stage(page)
        let removed = try await store.purgeLegacyPages()
        #expect(removed == 1)
        #expect(await store.snapshot().pages.isEmpty)
        _ = try await store.collectGarbage(dryRun: false)
        #expect(!FileManager.default.fileExists(atPath: try await store.fileURL(file).path))
    }
