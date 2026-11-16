import Foundation
import SyncCore

public enum RunningStatisticsPeriod: String, CaseIterable, Identifiable, Sendable {
  case week, month
  public var id: String { rawValue }
  public var title: String { self == .week ? "周" : "月" }
}

/// All grouping uses one explicit Gregorian calendar; weeks always start Monday.
public struct RunningStatisticsWindow: Equatable, Sendable {
  public let period: RunningStatisticsPeriod
  public let start: Date
  public let end: Date
  public let timezone: String
  public var calendar: Calendar {
    var value = Calendar(identifier: .gregorian)
    value.timeZone = TimeZone(identifier: timezone) ?? .current
    value.firstWeekday = 2
    value.minimumDaysInFirstWeek = 4
    return value
  }
  public init(period: RunningStatisticsPeriod, containing date: Date, timezone: TimeZone) {
    self.period = period
    self.timezone = timezone.identifier
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = timezone
    calendar.firstWeekday = 2
    calendar.minimumDaysInFirstWeek = 4
    let interval = calendar.dateInterval(of: period == .week ? .weekOfYear : .month, for: date)!
    start = interval.start
    end = interval.end
  }
  public func shifted(_ amount: Int) -> Self {
    let date = calendar.date(byAdding: period == .week ? .weekOfYear : .month, value: amount, to: start)!
    return Self(period: period, containing: date, timezone: calendar.timeZone)
  }
  public func contains(_ date: Date) -> Bool { date >= start && date < end }
  public var days: [Date] {
    var result: [Date] = []
    var date = start
    while date < end {
      result.append(date)
      date = calendar.date(byAdding: .day, value: 1, to: date)!
    }
    return result
  }
}

public struct WorkoutDistanceZone: Codable, Sendable {
  public let zone: String
  public let distanceMeters: Double
}

/// Computed on unsampled observations by the server, never from whole-run averages.
public struct WorkoutDistanceDistribution: Codable, Sendable {
  public let policyVersion: String
  public let pace: [WorkoutDistanceZone]
  public let heartRate: [WorkoutDistanceZone]
  public let paceUnknownMeters: Double
  public let heartRateUnknownMeters: Double
  public let heartRateProfileID: String
  public let totalDistanceMeters: Double
}

public struct RunningStatisticsDay: Identifiable, Sendable {
  public let date: Date
  public var distanceMeters: Double = 0
  public var count: Int = 0
  public var missingDistanceCount: Int = 0
  public var id: Date { date }
}
public struct RunningStatisticsZone: Identifiable, Sendable {
  public let id: String
  public let title: String
  public var distanceMeters: Double = 0
}
public struct RunningStatistics: Sendable {
  public let window: RunningStatisticsWindow
  public var days: [RunningStatisticsDay]
  public var distanceMeters: Double = 0
  public var durationSeconds: Double = 0
  public var count: Int = 0
  public var missingDistanceCount: Int = 0
  public var distributionFailureCount: Int = 0
  public var unavailableDistributionCount: Int = 0
  public var heartRateProfiles: Set<String> = []
  public var pace = [
    RunningStatisticsZone(id: "under_4", title: "< 4′00″"),
    RunningStatisticsZone(id: "4_to_5", title: "4′00″–5′00″"),
    RunningStatisticsZone(id: "5_to_6", title: "5′00″–6′00″"),
    RunningStatisticsZone(id: "6_to_7", title: "6′00″–7′00″"),
    RunningStatisticsZone(id: "over_7", title: "≥ 7′00″"),
  ]
  public var heartRate = [
    RunningStatisticsZone(id: "recovery", title: "Z1 · < 60%"),
    RunningStatisticsZone(id: "aerobic_base", title: "Z2 · 60–70%"),
    RunningStatisticsZone(id: "aerobic_power", title: "Z3 · 70–80%"),
    RunningStatisticsZone(id: "lactate_threshold", title: "Z4 · 80–90%"),
    RunningStatisticsZone(id: "anaerobic", title: "Z5 · ≥ 90%"),
  ]
  public var paceUnknownMeters: Double { max(0, distanceMeters - pace.reduce(0) { $0 + $1.distanceMeters }) }
  public var heartRateUnknownMeters: Double { max(0, distanceMeters - heartRate.reduce(0) { $0 + $1.distanceMeters }) }
  public init(window: RunningStatisticsWindow) {
    self.window = window
    days = window.days.map { RunningStatisticsDay(date: $0) }
  }
  public mutating func add(_ workout: ProductWorkout, distribution: WorkoutDistanceDistribution?, failed: Bool = false) {
    guard workout.raw.number("activityType") == 37, window.contains(workout.date) else { return }
    count += 1
    let rawDistance = workout.raw.number("distanceM")
    let distance = rawDistance.flatMap { $0.isFinite && $0 >= 0 ? $0 : nil }
    if distance == nil { missingDistanceCount += 1 }
    let meters = distance ?? 0
    distanceMeters += meters
    if let duration = workout.raw.number("durationS"), duration.isFinite, duration >= 0 { durationSeconds += duration }
    if let index = days.firstIndex(where: { window.calendar.isDate($0.date, inSameDayAs: workout.date) }) {
      days[index].distanceMeters += meters
      days[index].count += 1
      if distance == nil { days[index].missingDistanceCount += 1 }
    }
    if failed { distributionFailureCount += 1 }
    guard let distribution, distribution.policyVersion == "distance-zones/v1" else {
      if !failed { unavailableDistributionCount += 1 }
      return
    }
    // A concurrently changed or malformed response must not overstate coverage.
    guard distribution.totalDistanceMeters.isFinite,
      abs(distribution.totalDistanceMeters - meters) <= max(1, meters * 0.001)
    else { unavailableDistributionCount += 1; return }
    Self.addZones(distribution.pace, to: &pace, maximum: meters)
    Self.addZones(distribution.heartRate, to: &heartRate, maximum: meters)
    if !distribution.heartRateProfileID.isEmpty { heartRateProfiles.insert(distribution.heartRateProfileID) }
  }
  private static func addZones(_ incoming: [WorkoutDistanceZone], to result: inout [RunningStatisticsZone], maximum: Double) {
    var amounts = [String: Double]()
    let ids = Set(result.map(\.id))
    for zone in incoming where ids.contains(zone.zone) {
      guard zone.distanceMeters.isFinite, zone.distanceMeters >= 0 else { return }
      // Duplicate labels make the response ambiguous.
      guard amounts[zone.zone] == nil else { return }
      amounts[zone.zone] = zone.distanceMeters
    }
    guard amounts.values.reduce(0, +) <= maximum + 0.01 else { return }
    for index in result.indices { result[index].distanceMeters += amounts[result[index].id] ?? 0 }
  }
}

/// One Workout's entry in the server's statistics batch.
public struct WorkoutDistanceItem: Codable, Sendable {
  public let workoutID: String
  public let status: String
  public let distanceDistribution: WorkoutDistanceDistribution?
}

public enum RunningStatisticsReader {
  /// Reads the cursor chain back to the window start; the home page cache is
  /// deliberately not a data source. A failed list page fails the request, so
  /// partial totals never look complete. Distributions come from one batch
  /// read when the server offers it (`batch` returns nil otherwise); Workouts
  /// the batch does not cover fall back to their detail.
  public static func load(
    window: RunningStatisticsWindow,
    page: @Sendable (String) async throws -> JSON,
    batch: (@Sendable (RunningStatisticsWindow) async throws -> [WorkoutDistanceItem]?)? = nil,
    distribution: @escaping @Sendable (String) async throws -> WorkoutDistanceDistribution?
  ) async throws -> RunningStatistics {
    var cursor = ""
    var cursors: Set<String> = []
    var ids: Set<String> = []
    var workouts: [ProductWorkout] = []
    // The list is newest first: once a page reaches before the window start,
    // no later page can contain a Workout of this window.
    var reachedStart = false
    repeat {
      try Task.checkCancellation()
      guard cursors.insert(cursor).inserted else { throw SyncFailure.invalid("statistics_cursor_cycle") }
      let response = try await page(cursor)
      guard case .array(let items) = response["items"], case .string(let next) = response["next"] else {
        throw SyncFailure.invalid("statistics_page_invalid")
      }
      for item in items {
        let workout = ProductWorkout(item)
        guard !workout.id.isEmpty, item.number("startUs") != nil else {
          throw SyncFailure.invalid("statistics_workout_invalid")
        }
        if workout.date < window.start { reachedStart = true }
        if ids.insert(workout.id).inserted, item.number("activityType") == 37, window.contains(workout.date) {
          workouts.append(workout)
        }
      }
      cursor = next
    } while !cursor.isEmpty && !reachedStart
    var result = RunningStatistics(window: window)
    if let batch, let items = try? await batch(window) {
      let byID = Dictionary(items.map { ($0.workoutID.lowercased(), $0) }, uniquingKeysWith: { first, _ in first })
      var uncovered: [ProductWorkout] = []
      for workout in workouts {
        guard let item = byID[workout.id.lowercased()] else { uncovered.append(workout); continue }
        result.add(workout, distribution: item.status == "available" ? item.distanceDistribution : nil, failed: item.status == "failed")
      }
      workouts = uncovered
    }
    try Task.checkCancellation()
    // Bound simultaneous detail reads; results only publish when all reads finish.
    for offset in stride(from: 0, to: workouts.count, by: 4) {
      let batch = Array(workouts[offset..<min(offset + 4, workouts.count)])
      let values = await withTaskGroup(of: (ProductWorkout, WorkoutDistanceDistribution?, Bool).self) { group in
        for workout in batch {
          group.addTask {
            do { return (workout, try await distribution(workout.id), false) }
            catch { return (workout, nil, true) }
          }
        }
        var values: [(ProductWorkout, WorkoutDistanceDistribution?, Bool)] = []
        for await value in group { values.append(value) }
        return values
      }
      try Task.checkCancellation()
      for (workout, value, failed) in values { result.add(workout, distribution: value, failed: failed) }
    }
    return result
  }
}

extension ProductAPI {
  public func runningStatistics(window: RunningStatisticsWindow) async throws -> RunningStatistics {
    try await RunningStatisticsReader.load(window: window, page: { cursor in
      var query = ["limit": "50"]
      if !cursor.isEmpty { query["before"] = cursor }
      return try await self.get("/v2/workouts", query: query)
    }, batch: { window in
      // Older servers do not have the batch read: fall back to details.
      do {
        let raw = try await self.get("/v2/workouts/distance-distributions", query: [
          "fromUs": String(Int64((window.start.timeIntervalSince1970 * 1e6).rounded())),
          "toUs": String(Int64((window.end.timeIntervalSince1970 * 1e6).rounded())),
        ])
        struct Page: Decodable { let items: [WorkoutDistanceItem] }
        return try JSONDecoder().decode(Page.self, from: raw.data()).items
      } catch let failure as UploadError where failure.status == 404 || failure.status == 405 {
        return nil
      }
    }, distribution: { id in
      let detail = try await self.workoutDetail(id)
      guard detail.workout.workoutID == id else { throw SyncFailure.invalid("statistics_detail_mismatch") }
      return detail.distanceDistribution
    })
  }
}

/// Main-actor owned request identity prevents late period/session results publishing.
public struct RunningStatisticsRequests: Sendable {
  private var active: UUID?
  public init() {}
  public mutating func begin() -> UUID {
    let value = UUID()
    active = value
    return value
  }
  public mutating func invalidate() { active = nil }
  public func accepts(_ value: UUID) -> Bool { active == value }
}
