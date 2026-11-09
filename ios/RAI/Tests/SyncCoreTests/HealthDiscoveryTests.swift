import Foundation
import Testing
@testable import SyncCore

@Suite struct HealthDiscoveryTests {
    func directory() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("rai-discovery-test-" + Wire.id()) }
    @Test func twoPhaseAnchorCrashReplayAndIndependentTypes() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let query = DiscoveryQuery(type: "HKWorkoutTypeIdentifier"), other = DiscoveryQuery(type: "heartRate")
        #expect(try await store.discoveryFlow(query).anchor == nil)
        _ = try await store.discoveryFlow(other)
        let archive = Data("synthetic".utf8), file = try await store.savePayload(Data("synthetic".utf8))
        let event = DiscoveryEvent(type: query.type, uuid: Wire.id(), deleted: false, archiveFile: file, archiveHash: Wire.hash(archive))
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [event], hasMore: true)
        #expect(await store.discoverySnapshot().flows[query.key]?.anchor == nil)
        #expect(await store.discoverySnapshot().work[event.uuid]?.generation == 1)
        let reopened = try OutboxStore(directory: dir)
        try await reopened.promoteDiscovery(query, batchID: batch)
        #expect(await reopened.discoverySnapshot().flows[query.key]?.anchor == Data([1]))
        #expect(await reopened.discoverySnapshot().flows[other.key]?.anchor == nil)
        let replay = try await reopened.stageDiscovery(query, previousAnchor: Data([1]), anchor: Data([2]), events: [event], hasMore: false)
        try await reopened.promoteDiscovery(query, batchID: replay)
        #expect(await reopened.discoverySnapshot().work[event.uuid]?.generation == 1)
        #expect(await reopened.discoverySnapshot().events.count == 1)
        // A second unchanged page advances its own cursor with no new work.
        let empty = try await reopened.stageDiscovery(query, previousAnchor: Data([2]), anchor: Data([2]), events: [], hasMore: false)
        try await reopened.promoteDiscovery(query, batchID: empty)
        #expect(await reopened.discoverySnapshot().events.count == 1)
    }
    @Test func boundedHistoricalBatchesSurviveEveryRestart() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let query = DiscoveryQuery(type: "HKWorkoutTypeIdentifier")
        var previous: Data?
        for page in 0..<8 {
            let store = try OutboxStore(directory: dir); _ = try await store.discoveryFlow(query)
            let archive = Data("synthetic".utf8), file = try await store.savePayload(Data("synthetic".utf8))
            let events = (0..<25).map { _ in DiscoveryEvent(type: query.type, uuid: Wire.id(), deleted: false, archiveFile: file, archiveHash: Wire.hash(archive)) }
            let anchor = Data([UInt8(page + 1)])
            let batch = try await store.stageDiscovery(query, previousAnchor: previous, anchor: anchor, events: events, hasMore: page < 7)
            let reopened = try OutboxStore(directory: dir); try await reopened.promoteDiscovery(query, batchID: batch)
            previous = anchor
        }
        let store = try OutboxStore(directory: dir)
        #expect(await store.discoverySnapshot().events.count == 200)
        #expect(await store.discoverySnapshot().flows[query.key]?.anchor == Data([8]))
    }
    @Test func writeFailuresCannotPromoteOrLoseJournal() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), query = DiscoveryQuery(type: "sample")
        _ = try await store.discoveryFlow(query)
        let file = dir.appendingPathComponent("state.json"), saved = dir.appendingPathComponent("saved-state.json")
        try FileManager.default.moveItem(at: file, to: saved); try FileManager.default.createDirectory(at: file, withIntermediateDirectories: false)
        await #expect(throws: (any Error).self) { try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [], hasMore: false) }
        #expect(await store.discoverySnapshot().flows[query.key]?.anchor == nil)
        try FileManager.default.removeItem(at: file); try FileManager.default.moveItem(at: saved, to: file)
        let deletion = DiscoveryEvent(type: query.type, uuid: Wire.id(), deleted: true)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [deletion], hasMore: false)
        try FileManager.default.moveItem(at: file, to: saved); try FileManager.default.createDirectory(at: file, withIntermediateDirectories: false)
        await #expect(throws: (any Error).self) { try await store.promoteDiscovery(query, batchID: batch) }
        #expect(await store.discoverySnapshot().events[deletion.id] != nil)
        #expect(await store.discoverySnapshot().flows[query.key]?.anchor == nil)
        try FileManager.default.removeItem(at: file); try FileManager.default.moveItem(at: saved, to: file)
        let reopened = try OutboxStore(directory: dir); try await reopened.promoteDiscovery(query, batchID: batch)
        #expect(await reopened.discoverySnapshot().events[deletion.id]?.status == "unresolved")
    }
    @Test func unresolvedDeletionResolvedOnlyByExplicitIndex() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), query = DiscoveryQuery(type: "sample"), parent = Wire.id()
        _ = try await store.discoveryFlow(query)
        let event = DiscoveryEvent(type: query.type, uuid: Wire.id(), deleted: true)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [event], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)
        #expect(await store.discoverySnapshot().work.isEmpty == true)
        try await store.associateDiscoveredFacts(workoutID: parent, keys: [event.factKey])
        #expect(await store.discoverySnapshot().events[event.id]?.workoutID == parent)
        #expect(await store.discoverySnapshot().work[parent]?.selected == false)
        await #expect(throws: (any Error).self) { try await store.associateDiscoveredFacts(workoutID: Wire.id(), keys: [event.factKey]) }
    }
    @Test func repeatedAssociationWithoutNewFactsDoesNotCommit() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), parent = Wire.id(), key = "HKQuantityTypeIdentifierHeartRate:" + Wire.id()
        try await store.associateDiscoveredFacts(workoutID: parent, keys: [key])
        #expect(await store.discoverySnapshot().parents[key] == parent)
        #expect(await store.noopCommitSkipCount() == 0)
        let before = await store.snapshot()
        try await store.associateDiscoveredFacts(workoutID: parent, keys: [key])
        let after = await store.snapshot()
        #expect(await store.noopCommitSkipCount() == 1)
        #expect(before.discovery?.parents[key] == after.discovery?.parents[key])
        #expect(before.discovery?.events == after.discovery?.events)
        // A conflict still fails rather than being reported as a no-op.
        await #expect(throws: (any Error).self) {
            try await store.associateDiscoveredFacts(workoutID: Wire.id(), keys: [key])
        }
    }

    @Test func invalidAnchorAndContractStayQuarantined() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), query = DiscoveryQuery(type: "sample")
        _ = try await store.discoveryFlow(query)
        try await store.discoveryError(query, code: "invalid_healthkit_anchor")
        let reopened = try OutboxStore(directory: dir)
        #expect(try await reopened.discoveryFlow(query).error == "invalid_healthkit_anchor")
        let fresh = try await reopened.controlledRediscovery(query, recoveryID: Wire.id())
        #expect(try await reopened.discoveryFlow(fresh).anchor == nil)
        #expect(try await reopened.discoveryFlow(query).error == "invalid_healthkit_anchor")
        var next = query; next.contract = 2
        await #expect(throws: (any Error).self) { try await reopened.discoveryFlow(next) }
    }
    @Test func batchedAssociationsPreserveRevisionsDuplicatesConflictsAndRestart() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), query = DiscoveryQuery(type: "sample"), parent = Wire.id(), sample = Wire.id()
        _ = try await store.discoveryFlow(query)
        let archive = Data("synthetic".utf8), file = try await store.savePayload(archive)
        let add = DiscoveryEvent(type: query.type, uuid: sample, deleted: false, archiveFile: file, archiveHash: Wire.hash(archive))
        let deletion = DiscoveryEvent(type: query.type, uuid: sample, deleted: true)
        let unrelated = DiscoveryEvent(type: query.type, uuid: Wire.id(), deleted: true)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [add, deletion, unrelated], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)
        try await store.associateDiscoveredFacts(workoutID: parent, keys: [add.factKey, add.factKey])
        let reopened = try OutboxStore(directory: dir), snapshot = await reopened.discoverySnapshot()
        #expect(snapshot.work[parent]?.generation == 2)
        #expect(snapshot.events[add.id]?.workoutID == parent)
        #expect(snapshot.events[deletion.id]?.workoutID == parent)
        #expect(snapshot.events[unrelated.id]?.workoutID == nil)
        #expect(Set([snapshot.events[add.id]?.generation, snapshot.events[deletion.id]?.generation]) == Set([1, 2]))
        try await reopened.associateDiscoveredFacts(workoutID: parent, keys: [add.factKey])
        #expect(await reopened.discoverySnapshot().work[parent]?.generation == 2)
        // A later conflict must roll back earlier changes from this same call.
        await #expect(throws: (any Error).self) {
            try await reopened.associateDiscoveredFacts(workoutID: Wire.id(), keys: [unrelated.factKey, add.factKey])
        }
        #expect(await reopened.discoverySnapshot().parents[unrelated.factKey] == nil)
        #expect(await reopened.discoverySnapshot().events[unrelated.id]?.workoutID == nil)
    }
}

extension HealthDiscoveryTests {
    @Test func parentDeleteACKCannotReleaseLaterChildDeletionBundle() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), id = Wire.id()
        try await store.beginTask(id)
        let deletion: JSON = .object(["workoutID": .string(id), "ingestStatus": .string("deleted"), "commitRevision": .int(1), "alreadyDeleted": .bool(false)])
        try await store.acknowledgeWorkoutDeletion(id, receipt: deletion)
        let file = try await store.savePayload(Data("synthetic-child-delete".utf8)), hash = String(repeating: "a", count: 64)
        try await store.setDeletedParentBundle(id, file: file, hash: hash, boundary: "test", generation: 1, deletionIDs: [], sampleCount: 1, routeCount: 0)
        await #expect(throws: (any Error).self) { try await store.releaseBundle(id) }
        #expect(FileManager.default.fileExists(atPath: dir.appendingPathComponent(file).path))
        #expect(try await store.claimBundle(id) != nil)
        #expect(await store.task(id)?.phase == .deleted)
        await store.endBundleAttempt(id)
        try await store.failBundle(id, error: UploadError(code: "synthetic_network_error"))
        #expect(await store.task(id)?.deletionReceipt == deletion)
        #expect(await store.task(id)?.uploadReceipt == nil)
    }

    @Test func existingLocalTaskKeepsLateChildChangesSelected() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), id = Wire.id(), query = DiscoveryQuery(type: "HKWorkoutTypeIdentifier")
        try await store.beginTask(id)
        _ = try await store.discoveryFlow(query)
        let archive = Data("synthetic-existing-workout".utf8), file = try await store.savePayload(archive)
        let event = DiscoveryEvent(type: query.type, uuid: id, deleted: false, archiveFile: file, archiveHash: Wire.hash(archive))
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [event], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)
        #expect(await store.discoverySnapshot().work[id]?.selected == true)
    }

    @Test func partialReadACKKeepsCoverageAndRetriesBounded() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), id = Wire.id(), type = "HKWorkoutTypeIdentifier"
        let query = DiscoveryQuery(type: type)
        _ = try await store.discoveryFlow(query)
        let archive = Data("synthetic-workout".utf8), archiveFile = try await store.savePayload(archive)
        let event = DiscoveryEvent(type: type, uuid: id, deleted: false, archiveFile: archiveFile, archiveHash: Wire.hash(archive))
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [event], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)
        try await store.selectDiscoveredWorkout(id)
        try await store.beginTask(id)
        var partial = WorkoutCoverage(state: .partial, sampleCount: 2)
        partial.queryCompleted = true; partial.localPersisted = true
        try await store.setTaskCoverage(id, coverage: ["route": partial])
        let bundle = Data("synthetic-partial-bundle".utf8), hash = Wire.hash(bundle)
        let file = try await store.savePayload(bundle, suffix: "multipart.json")
        try await store.setBundle(id, file: file, sha256: hash, boundary: "test", discoveryGeneration: 1, pendingReadKeys: ["route"])
        try await store.acknowledgeBundle(id, receipt: durableWorkoutACK(id, hash))
        let task = try #require(await store.task(id))
        let work = try #require(await store.discoverySnapshot().work[id])
        #expect(task.phase == .acknowledged)
        #expect(task.coverage["route"]?.serverCommitted == true)
        #expect(work.error == "partial_read_pending")
        #expect(work.deliveredGeneration == 0)
        #expect(work.hasAutomaticDeliveryWork)
        #expect(work.needsDelivery(now: try #require(work.nextAttemptAt)))
        #expect(!work.needsDelivery(now: Date.distantPast))
    }
}

extension HealthDiscoveryTests {
    @Test func equivalentFactsDoNotCreateWorkWhenArchiveBytesDiffer() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), parent = Wire.id(), id = Wire.id()
        let query = DiscoveryQuery(type: "sample", workoutID: parent)
        _ = try await store.discoveryFlow(query)
        let a = Data("synthetic archive representation A".utf8), b = Data("synthetic archive representation B".utf8)
        let fileA = try await store.savePayload(a), fileB = try await store.savePayload(b)
        let first = DiscoveryEvent(type: query.type, uuid: id, deleted: false, archiveFile: fileA, archiveHash: Wire.hash(a), workoutID: parent, identityHash: "same-supported-fact")
        let second = DiscoveryEvent(type: query.type, uuid: id, deleted: false, archiveFile: fileB, archiveHash: Wire.hash(b), workoutID: parent, identityHash: "same-supported-fact")
        #expect(first.id == second.id)
        #expect(first.archiveSHA256 != second.archiveSHA256)
        let staged = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [first], hasMore: false)
        try await store.promoteDiscovery(query, batchID: staged)
        let replay = try await store.stageDiscovery(query, previousAnchor: Data([1]), anchor: Data([2]), events: [second], hasMore: false)
        try await store.promoteDiscovery(query, batchID: replay)
        #expect(await store.discoverySnapshot().work[parent]?.generation == 1)
        #expect(await store.discoverySnapshot().events.count == 1)
        #expect(await store.discoverySnapshot().events[first.id]?.archiveSHA256 == Wire.hash(a))
    }
}
