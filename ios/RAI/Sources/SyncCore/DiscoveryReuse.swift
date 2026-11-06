import Foundation

/// Reuse of an already-completed discovery read.
///
/// A scoped discovery stream that is *provably* exhausted for one Workout and
/// one sample type already holds every archived sample the bundle needs. The
/// bundle path may consume those archives instead of repeating an equivalent
/// full HealthKit read, which is the dominant cost of a first sync that was
/// preceded by discovery.
///
/// The reuse predicate is deliberately strict. It requires a committed anchor,
/// no staged batch, no recorded error and a stream whose last page was not
/// full. Anything less falls back to the existing complete read, so a partial
/// or interrupted scan can never be mistaken for a complete snapshot.
public extension OutboxStore {
    /// Captured facts of one exhausted scoped stream, or nil when the stream
    /// cannot be proven complete.
    ///
    /// Facts are returned as events rather than file names because a fact's
    /// archived bytes now live either in its own file (legacy) or inside a
    /// sealed segment at an offset. `discoveryArchiveBytes(_:)` reads both, and
    /// it reports a reference it cannot verify as nil, which is exactly the
    /// "not reusable" answer this predicate needs.
    func exhaustedDiscoveryFacts(workoutID: String, type: String) -> [DiscoveryEvent]? {
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        guard let flow = state.discovery?.flows[query.key], flow.anchor != nil, flow.staged == nil,
              flow.error == nil, flow.hasMore == false else { return nil }
        guard let events = state.discovery?.events.values else { return nil }
        var facts: [DiscoveryEvent] = []
        for event in events where event.type == type && event.workoutID == workoutID && !event.deleted {
            // A fact without any archive reference cannot be read back, so the
            // stream is not reusable even though its cursor is exhausted.
            guard event.archiveFile != nil || event.hasSegmentReference else { return nil }
            facts.append(event)
        }
        // An exhausted stream with no captured fact is a legitimate empty
        // result only when the flow really completed; the caller still checks
        // the sample count it expects.
        return facts.sorted { $0.uuid < $1.uuid }
    }
    /// The stream keys that are exhausted for this Workout, used for technical
    /// evidence only.
    func exhaustedDiscoveryTypes(workoutID: String) -> [String] {
        guard let flows = state.discovery?.flows else { return [] }
        return flows.values
            .filter { $0.query.workoutID == workoutID && $0.anchor != nil && $0.staged == nil && $0.error == nil && !$0.hasMore }
            .map(\.query.type).sorted()
    }
}
