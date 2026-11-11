import Foundation
import Testing
@testable import SyncCore

struct WorkoutAutoSyncPolicyTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func at(_ minutesAgo: Double) -> Date { now.addingTimeInterval(-minutesAgo * 60) }
    private let policy = WorkoutAutoSyncPolicy()

    @Test func freshWorkoutWaitsForTheWatchToFinishSyncing() {
        let result = policy.due(candidates: [.init(id: "a", end: at(0.5))], accepted: [:], now: now)
        #expect(result.isEmpty)
        // One minute is enough: the run is on the server while the runner is
        // still looking at the app, and the second copy picks up late data.
        #expect(policy.due(candidates: [.init(id: "a", end: at(1))], accepted: [:], now: now) == ["a"])
    }

    @Test func backgroundWakeSendsWithoutWaiting() {
        var immediate = policy
        immediate.settle = 0
        #expect(immediate.due(candidates: [.init(id: "a", end: now)], accepted: [:], now: now) == ["a"])
    }

    @Test func nextCheckIsWhenTheRunSettlesOrItsSecondCopyIsDue() {
        let fresh = WorkoutAutoSyncPolicy.Candidate(id: "a", end: at(0.5))
        #expect(policy.nextCheck(candidates: [fresh], accepted: [:], now: now) == fresh.end.addingTimeInterval(60))
        // Sent two minutes after the end, just now: repeat in 30 minutes.
        let early = WorkoutAutoSyncPolicy.Candidate(id: "b", end: at(2))
        #expect(policy.nextCheck(candidates: [early], accepted: ["b": now], now: now) == now.addingTimeInterval(30 * 60))
        // The earliest of several pending records wins.
        #expect(policy.nextCheck(candidates: [fresh, early], accepted: ["b": now], now: now) == fresh.end.addingTimeInterval(60))
        // Already due, sent late, rejected or out of the look-back: nothing to wait for.
        let settled = WorkoutAutoSyncPolicy.Candidate(id: "c", end: at(90))
        #expect(policy.nextCheck(candidates: [settled], accepted: [:], now: now) == nil)
        #expect(policy.nextCheck(candidates: [settled], accepted: ["c": at(5)], now: now) == nil)
        #expect(policy.nextCheck(candidates: [fresh], accepted: [:], rejected: ["a"], now: now) == nil)
        #expect(policy.nextCheck(candidates: [.init(id: "old", end: at(60 * 24 * 15))], accepted: [:], now: now) == nil)
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
