import Foundation
import ProductCore
import SyncCore
import Testing

@Suite struct ProductPresentationTests {
  private func workout(_ json: String) throws -> ProductWorkout {
    ProductWorkout(try JSON.decode(Data(json.utf8)))
  }

  @Test func expectedAbsenceNeverPromptsRetry() {
    #expect(ProductLoadFailures.needsRetry([:]) == false)
    for code in ProductLoadFailures.expectedAbsence {
      #expect(ProductLoadFailures.needsRetry(["athlete": code]) == false)
    }
    #expect(ProductLoadFailures.needsRetry(["athlete": "connection_unavailable"]) == true)
    #expect(ProductLoadFailures.needsRetry(["plan": "analysis_not_ready", "workouts": "http_500"]) == true)
  }

  @Test func notActivatedAthleteAloneKeepsHomeClean() {
    // The exact shape the local simulated cloud returns before any model is
    // published: only the athlete read fails, with 409 not_product_activated.
    let failures = ["athlete": "not_product_activated"]
    #expect(ProductLoadFailures.needsRetry(failures) == false)
  }

  @Test func badgePrefersLiveRoundStateOverPersistedTime() {
    let past = Date(timeIntervalSince1970: 1_800_000_000)
    #expect(ProductSyncBadge.resolve(busy: true, hasError: false, hasFailedResult: false,
                                     hasSucceededResult: false, lastUploadAt: past, pendingChanges: 0) == .syncing)
    #expect(ProductSyncBadge.resolve(busy: false, hasError: true, hasFailedResult: false,
                                     hasSucceededResult: true, lastUploadAt: past, pendingChanges: 0) == .attention)
    #expect(ProductSyncBadge.resolve(busy: false, hasError: false, hasFailedResult: true,
                                     hasSucceededResult: false, lastUploadAt: nil, pendingChanges: 0) == .attention)
  }

  @Test func badgeSurvivesRelaunchWithNoRoundResults() {
    // After a relaunch the in-memory results are empty; the persisted upload
    // time alone must keep the badge honest instead of "尚未同步".
    let resolved = ProductSyncBadge.resolve(busy: false, hasError: false, hasFailedResult: false,
                                            hasSucceededResult: false,
                                            lastUploadAt: Date(timeIntervalSince1970: 1_800_000_000),
                                            pendingChanges: 0)
    #expect(resolved == .uploaded)
    #expect(resolved.text == "已上传")
    #expect(ProductSyncBadge.resolve(busy: false, hasError: false, hasFailedResult: false,
                                     hasSucceededResult: false, lastUploadAt: nil, pendingChanges: 2) == .pending)
    #expect(ProductSyncBadge.resolve(busy: false, hasError: false, hasFailedResult: false,
                                     hasSucceededResult: false, lastUploadAt: nil, pendingChanges: 0) == .never)
  }

  @Test func detailLineOmitsMissingDistanceAndPace() throws {
    // A Workout the server stored without distance metrics.
    let row = try workout(#"{"workoutID":"a","activityType":37,"startUs":1800000000000000,"durationS":600,"metrics":[]}"#)
    #expect(row.detailLine == "10:00")
  }

  @Test func detailLineShowsDistancePaceAndDurationWhenPresent() throws {
    let row = try workout(#"""
    {"workoutID":"b","activityType":37,"startUs":1800000000000000,"durationS":600,"distanceM":2000,
     "metrics":[{"metric":"average_pace","value":0.3}]}
    """#)
    #expect(row.detailLine == "2.0 公里 · 10:00 · 5′00″/公里")
  }

  @Test func detailLineDropsPaceWithoutDistance() throws {
    // Pace without distance is not a usable pair; keep duration only.
    let row = try workout(#"{"workoutID":"c","activityType":37,"startUs":1800000000000000,"durationS":600,"metrics":[{"metric":"average_pace","value":300}]}"#)
    #expect(row.detailLine == "10:00")
  }

  @Test func detailLineIsEmptyWhenNothingIsKnown() throws {
    let row = try workout(#"{"workoutID":"d","activityType":37,"startUs":1800000000000000,"metrics":[]}"#)
    #expect(row.detailLine.isEmpty)
  }
}
