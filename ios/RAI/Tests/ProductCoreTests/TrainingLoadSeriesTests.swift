import Foundation
import SyncCore
import Testing
@testable import ProductCore

@Suite struct TrainingLoadSeriesTests {
  private func row(_ day: String, _ metric: String, _ value: Double) -> JSON {
    .object([
      "day": .string(day),
      "metric": .string(metric),
      "value": .number(value),
    ])
  }

  @Test func mergesRowsByDayAndSorts() {
    let history: JSON = .object([
      "metrics": .array([
        row("2026-11-08", "balance", -1.7),
        row("2026-11-07", "fitness", 1.5),
        row("2026-11-08", "fitness", 1.8),
        row("2026-11-07", "fatigue", 3.2),
        row("2026-11-08", "daily_load", 95.5),
        row("2026-11-08", "unknown", 9),
      ]),
    ])
    let points = TrainingLoadSeries.points(from: history)
    #expect(points.map(\.day) == ["2026-11-07", "2026-11-08"])
    #expect(points[0].fitness == 1.5)
    #expect(points[0].fatigue == 3.2)
    #expect(points[0].balance == nil)
    #expect(points[1].fitness == 1.8)
    #expect(abs((points[1].balance ?? 0) + 1.7) < 0.0001)
    #expect(points[1].dailyLoad == 95.5)
    #expect(points[1].hasLoad)
  }

  @Test func latestSkipsEmptyDays() {
    let history: JSON = .object([
      "metrics": .array([
        row("2026-11-06", "fitness", 1.2),
        .object(["day": .string("2026-11-07"), "metric": .string("fitness"), "value": .null]),
      ]),
    ])
    let points = TrainingLoadSeries.points(from: history)
    #expect(points.count == 1)
    #expect(TrainingLoadSeries.latest(points)?.day == "2026-11-06")
  }

  @Test func malformedRowsAreIgnored() {
    let history: JSON = .object([
      "metrics": .array([
        .object(["day": .string(""), "metric": .string("fitness"), "value": .number(1)]),
        .object(["metric": .string("fitness"), "value": .number(1)]),
        .object(["day": .string("2026-11-08"), "metric": .string("fitness"), "value": .string("x")]),
      ]),
    ])
    #expect(TrainingLoadSeries.points(from: history).isEmpty)
  }
}
