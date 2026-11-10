import Foundation

/// Decides which of the most recent HealthKit Workouts the app should send on
/// its own. It is pure so the rules can be tested without HealthKit.
///
/// - A Workout that ended moments ago is held back: the Watch is usually still
///   handing heart rate, route and running dynamics to the iPhone.
/// - A new Workout inside the look-back window is sent once it has settled.
/// - A Workout that was sent shortly after it ended is sent once more later,
///   because the first copy may have been missing late data. The server treats
///   identical bytes as a no-op, and this happens at most once per record.
/// - History older than the look-back window is never sent automatically; the
///   manual backfill covers it.
public struct WorkoutAutoSyncPolicy: Sendable, Equatable {
    public var settle: TimeInterval = 10 * 60
    public var recheck: TimeInterval = 30 * 60
    public var recheckWindow: TimeInterval = 24 * 60 * 60
    public var lookBack: TimeInterval = 14 * 24 * 60 * 60
    public var maxPerRound = 8
    public init() {}

    public struct Candidate: Sendable, Equatable {
        public let id: String
        public let end: Date
        public init(id: String, end: Date) { self.id = id; self.end = end }
    }

    /// `accepted` maps a Workout UUID to the time the server accepted it.
    /// `rejected` holds UUIDs the server refused; they are not retried by
    /// themselves, since the same bytes would fail the same way.
    public func due(candidates: [Candidate], accepted: [String: Date], rejected: Set<String> = [],
                    now: Date) -> [String] {
        var out: [(Date, String)] = []
        for item in candidates {
            let age = now.timeIntervalSince(item.end)
            guard age >= settle, age <= lookBack, !rejected.contains(item.id) else { continue }
            if let at = accepted[item.id] {
                let early = at.timeIntervalSince(item.end) < recheck
                if early, now.timeIntervalSince(at) >= recheck, age <= recheckWindow { out.append((item.end, item.id)) }
            } else {
                out.append((item.end, item.id))
            }
        }
        return out.sorted { $0.0 > $1.0 }.prefix(maxPerRound).map(\.1)
    }
}
