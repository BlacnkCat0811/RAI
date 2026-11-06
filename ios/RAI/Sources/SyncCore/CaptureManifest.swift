import Foundation

/// What one prepared Workout bundle provably contains.
///
/// The acknowledgement boundary used to be a bare generation watermark: after
/// capture finished, `performSync` read the *newest* discovery generation and
/// the ACK marked every event at or below it as delivered. Anything a producer
/// committed while the bundle was still being built — a late sample of a type
/// that had already been read, a child deletion, or the Workout's own deletion —
/// therefore inherited a generation the bundle "covered" even though its bytes
/// were never encoded, and the durable receipt silently swallowed it. A lost
/// child deletion resurrects a sample on the server; a lost sample leaves a
/// permanent hole.
///
/// A manifest replaces that watermark with the identities the bundle really
/// consumed. `OutboxStore` is the single writer and assigns every journal event
/// a monotonic `ordinal` when it commits it; the per-type cursor records the
/// highest ordinal this bundle covered and is kept as evidence of what the
/// stream encoded.
///
/// Coverage rules:
/// - an upsert is covered only when the bundle lists its exact event id. A
///   cursor comparison is not sound on its own: ordinals are global commit
///   order, so a fact of the same type can be committed *before* the facts the
///   bundle read and still be absent from it (a sample saved while the query
///   was in flight). "ordinal <= cursor" would confirm bytes the bundle never
///   carried, which is the defect this manifest exists to close;
/// - a deletion is covered only when the bundle listed its exact event id;
/// - anything else stays `pending`, and the delivered watermark stops below the
///   lowest uncovered generation so the next pass picks the work up.
public struct CaptureManifest: Codable, Sendable, Equatable {
    public static let version: Int64 = 1
    public var version: Int64
    public var workoutID: String
    /// Workout generation observed when the manifest was pinned. Evidence only:
    /// coverage is decided by `cursors` and the explicit id lists, never by it.
    public var generation: Int64
    /// Highest covered journal ordinal per sample type.
    public var cursors: [String: Int64]
    /// Facts covered by identity because a type cursor cannot express them, such
    /// as the Workout root event.
    public var coveredEventIDs: [String]
    /// Deletions the bundle explicitly carried.
    public var deletionEventIDs: [String]
    /// Types the bundle read in full this pass.
    public var completeTypes: [String]
    /// Number of samples the bundle encoded. Evidence only.
    public var coveredFacts: Int
    public init(workoutID: String, generation: Int64, cursors: [String: Int64],
                coveredEventIDs: [String] = [], deletionEventIDs: [String] = [],
                completeTypes: [String] = [], coveredFacts: Int = 0) {
        self.version = Self.version
        self.workoutID = workoutID; self.generation = generation
        self.cursors = cursors; self.coveredEventIDs = coveredEventIDs
        self.deletionEventIDs = deletionEventIDs; self.completeTypes = completeTypes
        self.coveredFacts = coveredFacts
    }

    /// True when this bundle provably carried, or explicitly deleted, the event.
    public func covers(_ event: DiscoveryEvent) -> Bool {
        guard event.workoutID == workoutID else { return false }
        if deletionEventIDs.contains(event.id) { return true }
        return coveredEventIDs.contains(event.id)
    }

    /// Wire/metrics projection. Contains identities and counts only, never a
    /// health value.
    public var summary: JSON {
        .object(["version": .int(version), "generation": .int(generation),
                 "coveredFacts": .int(Int64(coveredFacts)),
                 "cursors": .int(Int64(cursors.count)),
                 "deletions": .int(Int64(deletionEventIDs.count)),
                 "completeTypes": .int(Int64(completeTypes.count))])
    }
}
