import Foundation
import Testing
@testable import SyncCore

/// Reusing a discovery read is only sound when the stream is provably complete
/// for that Workout and type. These tests pin the predicate: a staged batch, a
/// recorded error, a missing anchor or a full last page all forbid reuse, so a
/// partial scan can never be mistaken for a complete snapshot.
@Suite struct DiscoveryReuseTests {
    func directory() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("rai-reuse-test-" + Wire.id()) }
    let workoutID = "8f2f6a44-0c1b-4b7c-9d0e-1a2b3c4d5e6f"
    let type = "HKQuantityTypeIdentifierHeartRate"

    func event(_ store: OutboxStore, _ uuid: String, _ payload: String, deleted: Bool = false,
               workout: String? = nil) async throws -> DiscoveryEvent {
        let archive = Data(payload.utf8), file = try await store.savePayload(archive)
        return DiscoveryEvent(type: type, uuid: uuid, deleted: deleted, archiveFile: deleted ? nil : file,
                              archiveHash: deleted ? "" : Wire.hash(archive), workoutID: workout ?? workoutID)
    }

    @Test func exhaustedStreamExposesItsFacts() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        _ = try await store.discoveryFlow(query)
        let first = try await event(store, "a", "one"), second = try await event(store, "b", "two")
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]),
                                                   events: [first, second], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)

        let facts = await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type)
        #expect(facts?.count == 2)
        #expect(Set(facts?.map(\.uuid) ?? []) == ["a", "b"])
        #expect(await store.exhaustedDiscoveryTypes(workoutID: workoutID) == [type])
    }

    /// A fact captured into a sealed segment is just as reusable as a legacy
    /// single-file archive: the reuse set carries events, not file names.
    @Test func segmentedFactsAreReusable() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        _ = try await store.discoveryFlow(query)
        let records = [CapturedFact(type: type, uuid: "a", archive: Data("one".utf8)),
                       CapturedFact(type: type, uuid: "b", archive: Data("two".utf8))]
        let captured = try await store.captureFactSegment(type: type, workoutID: workoutID, records: records, publish: false)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]),
                                                   events: captured.events, hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)

        let facts = await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type)
        #expect(facts?.count == 2)
        #expect(facts?.allSatisfy { $0.hasSegmentReference } == true)
        // Both representations read back through the same entry point.
        for fact in facts ?? [] {
            #expect(try await store.discoveryArchiveBytes(fact) != nil)
        }
    }

    /// A full last page means more pages may exist: the read is not complete.
    @Test func morePagesForbidReuse() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        _ = try await store.discoveryFlow(query)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]),
                                                   events: [try await event(store, "a", "one")], hasMore: true)
        try await store.promoteDiscovery(query, batchID: batch)
        #expect(await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type) == nil)
        #expect(await store.exhaustedDiscoveryTypes(workoutID: workoutID).isEmpty)
    }

    /// A staged but unpromoted batch has not advanced the anchor yet.
    @Test func stagedBatchForbidsReuse() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        _ = try await store.discoveryFlow(query)
        let first = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]),
                                                   events: [try await event(store, "a", "one")], hasMore: false)
        try await store.promoteDiscovery(query, batchID: first)
        _ = try await store.stageDiscovery(query, previousAnchor: Data([1]), anchor: Data([2]),
                                            events: [try await event(store, "b", "two")], hasMore: false)
        #expect(await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type) == nil)
    }

    /// A recorded error means the scan stopped early.
    @Test func recordedErrorForbidsReuse() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        _ = try await store.discoveryFlow(query)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]),
                                                   events: [try await event(store, "a", "one")], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)
        #expect(await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type) != nil)
        try await store.discoveryError(query, code: "healthkit_read_failed")
        #expect(await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type) == nil)
    }

    /// Deleted facts never leak into a reuse set, and a whole-library scan is
    /// never mistaken for a scoped, reusable snapshot of one Workout.
    @Test func reuseIsScopedToLiveFactsOfOneWorkout() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        _ = try await store.discoveryFlow(query)
        let live = try await event(store, "a", "one")
        let removed = try await event(store, "b", "two", deleted: true)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]),
                                                   events: [live, removed], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)
        #expect(await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type)?.map(\.uuid) == ["a"])

        // The same facts under the library-wide query are a different contract:
        // they say nothing about what one Workout contains.
        let wide = DiscoveryQuery(type: type)
        _ = try await store.discoveryFlow(wide)
        let wideBatch = try await store.stageDiscovery(wide, previousAnchor: nil, anchor: Data([9]),
                                                       events: [try await event(store, "c", "three", workout: "11111111-2222-3333-4444-555555555555")], hasMore: false)
        try await store.promoteDiscovery(wide, batchID: wideBatch)
        #expect(await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type)?.map(\.uuid) == ["a"])
        #expect(await store.exhaustedDiscoveryTypes(workoutID: workoutID) == [type])
    }

    /// A stream that was never opened for this Workout offers nothing to reuse.
    @Test func absentStreamIsNotAReusableSnapshot() async throws {
        let dir = directory(); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        #expect(await store.exhaustedDiscoveryFacts(workoutID: workoutID, type: type) == nil)
        #expect(await store.exhaustedDiscoveryTypes(workoutID: workoutID).isEmpty)
    }
}
