import Foundation
import Testing
@testable import SyncCore

struct WorkoutAutoSyncPolicyTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func at(_ minutesAgo: Double) -> Date { now.addingTimeInterval(-minutesAgo * 60) }
    private let policy = WorkoutAutoSyncPolicy()

    @Test func freshWorkoutWaitsForTheWatchToFinishSyncing() {
        let result = policy.due(candidates: [.init(id: "a", end: at(3))], accepted: [:], now: now)
        #expect(result.isEmpty)
        #expect(policy.due(candidates: [.init(id: "a", end: at(11))], accepted: [:], now: now) == ["a"])
    }

    @Test func newestFirstAndBoundedPerRound() {
        let candidates = (0..<12).map { WorkoutAutoSyncPolicy.Candidate(id: "w\($0)", end: at(Double(20 + $0 * 60))) }
        let result = policy.due(candidates: candidates, accepted: [:], now: now)
        #expect(result.count == 8)
        #expect(result.first == "w0")
    }

    @Test func historyBeyondTheLookBackIsLeftToManualBackfill() {
        let old = WorkoutAutoSyncPolicy.Candidate(id: "old", end: at(60 * 24 * 15))
        #expect(policy.due(candidates: [old], accepted: [:], now: now).isEmpty)
    }

    @Test func earlyUploadIsRepeatedOnceAfterThirtyMinutes() {
        let workout = WorkoutAutoSyncPolicy.Candidate(id: "a", end: at(60))
        // Sent 5 minutes after the run ended, 55 minutes ago: due for one more copy.
        #expect(policy.due(candidates: [workout], accepted: ["a": at(55)], now: now) == ["a"])
        // Sent only 10 minutes ago: too soon.
        #expect(policy.due(candidates: [workout], accepted: ["a": at(10)], now: now).isEmpty)
        // The repeat itself was sent long after the end, so it is not repeated again.
        #expect(policy.due(candidates: [workout], accepted: ["a": at(2)], now: now).isEmpty)
        // A copy sent two hours after the end is complete enough.
        let later = WorkoutAutoSyncPolicy.Candidate(id: "b", end: at(300))
        #expect(policy.due(candidates: [later], accepted: ["b": at(180)], now: now).isEmpty)
    }

    @Test func rejectedRecordsAreNotRetriedAutomatically() {
        let workout = WorkoutAutoSyncPolicy.Candidate(id: "a", end: at(60))
        #expect(policy.due(candidates: [workout], accepted: [:], rejected: ["a"], now: now).isEmpty)
    }
}
