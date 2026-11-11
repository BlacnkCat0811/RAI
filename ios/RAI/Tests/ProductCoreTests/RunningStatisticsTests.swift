import Foundation
import SyncCore
import Testing
@testable import ProductCore

@Suite struct RunningStatisticsTests {
  private let utc = TimeZone(secondsFromGMT: 0)!
  private func date(_ value: String) -> Date { ISO8601DateFormatter().date(from: value)! }
  private var window: RunningStatisticsWindow {
    RunningStatisticsWindow(period: .week, containing: date("2026-11-11T12:00:00Z"), timezone: utc)
  }
  private func workout(_ id: String, _ start: String = "2026-11-11T12:00:00Z", distance: Double? = 5000, activity: Int64 = 37) -> JSON {
    .object([
      "workoutID": .string(id), "startUs": .number(date(start).timeIntervalSince1970 * 1e6),
      "activityType": .int(activity), "durationS": .number(1800),
      "distanceM": distance.map(JSON.number) ?? .null,
      "metrics": .array([.object(["metric": .string("averageHeartRateBpm"), "value": .number(170)])]),
    ])
  }
  private func distribution(pace: [(String, Double)] = [("4_to_5", 1000), ("5_to_6", 2000)], heart: [(String, Double)] = [("aerobic_base", 1500)], total: Double = 5000) -> WorkoutDistanceDistribution {
    WorkoutDistanceDistribution(policyVersion: "distance-zones/v1",
      pace: pace.map { WorkoutDistanceZone(zone: $0.0, distanceMeters: $0.1) },
      heartRate: heart.map { WorkoutDistanceZone(zone: $0.0, distanceMeters: $0.1) },
      paceUnknownMeters: 2000, heartRateUnknownMeters: 3500,
      heartRateProfileID: "hrr:50:190", totalDistanceMeters: total)
  }
  @Test func mondayWeeksAndCalendarMonthsUseDatasetTimezone() {
    let shanghai = TimeZone(identifier: "Asia/Shanghai")!
    let week = RunningStatisticsWindow(period: .week, containing: date("2026-11-08T17:00:00Z"), timezone: shanghai)
    #expect(week.start == date("2026-11-08T16:00:00Z"))
    #expect(week.days.count == 7)
    #expect(week.contains(week.start))
    #expect(!week.contains(week.end))
    let february = RunningStatisticsWindow(period: .month, containing: date("2028-02-11T12:00:00Z"), timezone: shanghai)
    #expect(february.days.count == 29)
    #expect(february.shifted(1).days.count == 31)
    #expect(february.shifted(-1).shifted(1) == february)
    let dst = RunningStatisticsWindow(period: .week, containing: date("2026-03-08T12:00:00Z"), timezone: TimeZone(identifier: "America/New_York")!)
    #expect(dst.days.count == 7)
    #expect(dst.end.timeIntervalSince(dst.start) == 167 * 3600)
  }
  @Test func sumsDistanceRatherThanTimeFractionsAndPreservesUnknown() {
    var value = RunningStatistics(window: window)
    value.add(ProductWorkout(workout("a")), distribution: distribution())
    value.add(ProductWorkout(workout("b")), distribution: nil)
    #expect(value.count == 2)
    #expect(value.distanceMeters == 10000)
    #expect(value.durationSeconds == 3600)
    #expect(value.pace[1].distanceMeters == 1000)
    #expect(value.pace[2].distanceMeters == 2000)
    #expect(value.paceUnknownMeters == 7000)
    #expect(value.heartRateUnknownMeters == 8500)
    #expect(value.unavailableDistributionCount == 1)
    #expect(value.days.reduce(0) { $0 + $1.distanceMeters } == value.distanceMeters)
  }
  @Test func missingOrFailedFactsDoNotInventZonesAndNonRunsDoNotCount() {
    var value = RunningStatistics(window: window)
    value.add(ProductWorkout(workout("a", distance: nil)), distribution: nil, failed: true)
    value.add(ProductWorkout(workout("b", activity: 1)), distribution: distribution())
    value.add(ProductWorkout(workout("c", "2026-11-16T00:00:00Z")), distribution: distribution())
    #expect(value.count == 1)
    #expect(value.missingDistanceCount == 1)
    #expect(value.distributionFailureCount == 1)
    #expect(value.heartRate.allSatisfy { $0.distanceMeters == 0 })
  }
  @Test func invalidOrMismatchedZonesRemainUnknown() {
    var value = RunningStatistics(window: window)
    value.add(ProductWorkout(workout("a")), distribution: distribution(pace: [("4_to_5", 6000)]))
    value.add(ProductWorkout(workout("b")), distribution: distribution(total: 8000))
    value.add(ProductWorkout(workout("c")), distribution: distribution(pace: [("4_to_5", 100), ("4_to_5", 100)]))
    #expect(value.paceUnknownMeters == 15000)
    #expect(value.unavailableDistributionCount == 1)
  }
  @Test func completePaginationDeduplicatesAndOnlyLoadsSelectedRuns() async throws {
    let rows = [workout("a"), workout("old", "2026-10-01T00:00:00Z"), workout("cycle", activity: 13)]
    let page1: JSON = .object(["items": .array(rows), "next": .string("second")])
    let page2: JSON = .object(["items": .array([workout("a"), workout("b")]), "next": .string("third")])
    let page3: JSON = .object(["items": .array([workout("c")]), "next": .string("")])
    let recorder = StatisticsRecorder()
    let value = try await RunningStatisticsReader.load(window: window, page: { cursor in
      await recorder.page(cursor)
      return cursor.isEmpty ? page1 : cursor == "second" ? page2 : page3
    }, distribution: { id in
      await recorder.detail(id)
      if id == "b" { throw SyncFailure.invalid("synthetic_failure") }
      return nil
    })
    #expect(value.count == 3)
    #expect(value.distanceMeters == 15000)
    #expect(value.distributionFailureCount == 1)
    #expect(await recorder.pages == ["", "second", "third"])
    #expect(await Set(recorder.details) == ["a", "b", "c"])
  }
  @Test func failedOrCyclicPaginationCannotPublishPartialTotals() async {
    let first: JSON = .object(["items": .array([workout("a")]), "next": .string("again")])
    await #expect(throws: (any Error).self) {
      try await RunningStatisticsReader.load(window: window, page: { _ in first }, distribution: { _ in nil })
    }
    await #expect(throws: (any Error).self) {
      try await RunningStatisticsReader.load(window: window, page: { cursor in
        if !cursor.isEmpty { throw SyncFailure.invalid("synthetic_page_failure") }
        return first
      }, distribution: { _ in nil })
    }
  }
  @Test func stalePeriodAndSessionResponsesCannotPublish() {
    var requests = RunningStatisticsRequests()
    let week = requests.begin()
    let month = requests.begin()
    #expect(!requests.accepts(week))
    #expect(requests.accepts(month))
    requests.invalidate()
    #expect(!requests.accepts(month))
    let newSession = requests.begin()
    #expect(!requests.accepts(week))
    #expect(!requests.accepts(month))
    #expect(requests.accepts(newSession))
  }
}

private actor StatisticsRecorder {
  var pages: [String] = []
  var details: [String] = []
  func page(_ value: String) { pages.append(value) }
  func detail(_ value: String) { details.append(value) }
}
