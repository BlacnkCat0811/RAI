import Foundation
import SyncCore

/// One day of training-load history: fatigue (ATL, 7 d), fitness (CTL, 42 d),
/// balance (CTL − ATL) and the day's recorded load.
public struct TrainingLoadPoint: Identifiable, Equatable, Sendable {
  public var id: String { day }
  public let day: String
  public var fitness: Double?
  public var fatigue: Double?
  public var balance: Double?
  public var dailyLoad: Double?

  public init(
    day: String, fitness: Double? = nil, fatigue: Double? = nil,
    balance: Double? = nil, dailyLoad: Double? = nil
  ) {
    self.day = day
    self.fitness = fitness
    self.fatigue = fatigue
    self.balance = balance
    self.dailyLoad = dailyLoad
  }

  public var hasLoad: Bool {
    fitness != nil || fatigue != nil || balance != nil || dailyLoad != nil
  }
}

/// Pure parsing for the training-load chart. The product envelope remains the
/// authority for whether a value may be shown; this only shapes rows that the
/// server already returned.
public enum TrainingLoadSeries {
  /// Legacy `training_state.bundle/v2` rows (no longer fetched by the app).
  public static func points(from history: JSON) -> [TrainingLoadPoint] {
    var byDay: [String: TrainingLoadPoint] = [:]
    for item in history["metrics"].list {
      guard let day = item["day"].text, !day.isEmpty,
        let value = item["value"].double
      else { continue }
      var point = byDay[day] ?? TrainingLoadPoint(day: day)
      switch item["metric"].text ?? "" {
      case "fitness": point.fitness = value
      case "fatigue": point.fatigue = value
      case "balance": point.balance = value
      case "daily_load": point.dailyLoad = value
      default: continue
      }
      byDay[day] = point
    }
    return byDay.values.sorted { $0.day < $1.day }
  }

  /// Maps the v2 athlete-model `trainingLoad` history: the point value is the
  /// acute load, the components carry chronic, balance and the daily load.
  public static func points(fromAthlete history: [AthleteHistoryPoint]) -> [TrainingLoadPoint] {
    history.filter { $0.metric == "trainingLoad" }.map { point in
      TrainingLoadPoint(
        day: point.date, fitness: point.components?["chronic"], fatigue: point.value,
        balance: point.components?["balance"], dailyLoad: point.components?["dailyLoad"])
    }.sorted { $0.day < $1.day }
  }

  public static func latest(_ points: [TrainingLoadPoint]) -> TrainingLoadPoint? {
    points.last { $0.hasLoad }
  }
}
