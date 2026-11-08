import Foundation

/// One training pace band derived from the athlete's 60-minute critical
/// velocity (v60). Values are seconds per kilometre: the slow end is the
/// larger number, the fast end the smaller number.
public struct RunningPaceZone: Identifiable, Equatable, Sendable {
  public var id: String { code }
  public let code: String
  public let name: String
  public let purpose: String
  public let slowSecondsPerKm: Double
  public let fastSecondsPerKm: Double

  public init(
    code: String, name: String, purpose: String,
    slowSecondsPerKm: Double, fastSecondsPerKm: Double
  ) {
    self.code = code
    self.name = name
    self.purpose = purpose
    self.slowSecondsPerKm = slowSecondsPerKm
    self.fastSecondsPerKm = fastSecondsPerKm
  }
}

/// A race-time projection derived from v60 with Riegel's endurance model.
public struct RunningRacePrediction: Identifiable, Equatable, Sendable {
  public var id: String { name }
  public let name: String
  public let distanceM: Double
  public let seconds: Double

  public init(name: String, distanceM: Double, seconds: Double) {
    self.name = name
    self.distanceM = distanceM
    self.seconds = seconds
  }
}

/// Pure derivations for the running-ability screen. These are deliberately
/// independent of server payload versions: callers must only pass a v60 that
/// the product metric envelope already allows them to present.
public enum RunningAbilityMath {
  /// Seconds per kilometre at the 60-minute critical velocity, in m/s.
  public static func paceSecondsPerKm(v60: Double?) -> Double? {
    guard let v60, v60.isFinite, v60 > 0 else { return nil }
    return 1000 / v60
  }

  /// Five pace bands. Speed fractions of v60 are converted to pace so the
  /// bands remain monotonic even when v60 is close to zero.
  public static func paceZones(v60: Double?) -> [RunningPaceZone] {
    guard let v60, v60.isFinite, v60 > 0 else { return [] }
    let specs: [(String, String, String, Double, Double)] = [
      ("E", "轻松跑", "有氧基础与恢复", 0.60, 0.75),
      ("M", "马拉松配速", "比赛专项耐力", 0.78, 0.85),
      ("T", "阈值跑", "提升乳酸阈", 0.87, 0.92),
      ("I", "间歇跑", "提升最大摄氧", 0.95, 1.00),
      ("R", "冲刺跑", "速度与跑步经济性", 1.03, 1.10),
    ]
    return specs.map { code, name, purpose, slow, fast in
      RunningPaceZone(
        code: code, name: name, purpose: purpose,
        slowSecondsPerKm: 1000 / (v60 * slow),
        fastSecondsPerKm: 1000 / (v60 * fast))
    }
  }

  /// Standard race distances projected from the 60-minute effort using
  /// Riegel's exponent 1.06. Distances shorter than the base effort are
  /// intentionally included: they are the races most runners compare first.
  public static func racePredictions(v60: Double?) -> [RunningRacePrediction] {
    guard let v60, v60.isFinite, v60 > 0 else { return [] }
    let baseDistance = v60 * 3600
    let races: [(String, Double)] = [
      ("5 公里", 5_000),
      ("10 公里", 10_000),
      ("半程马拉松", 21_097.5),
      ("马拉松", 42_195),
    ]
    return races.map { name, distance in
      let seconds = 3600 * pow(distance / baseDistance, 1.06)
      return RunningRacePrediction(name: name, distanceM: distance, seconds: seconds)
    }
  }

  /// Server payloads may express retention as a 0–1 fraction or as a
  /// percentage. Normalize both to percent without inventing a value when the
  /// payload is absent.
  public static func retentionPercent(_ value: Double?) -> Double? {
    guard let value, value.isFinite, value >= 0 else { return nil }
    return value <= 1 ? value * 100 : value
  }
}
