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

/// A race-time projection derived from v60 and the fitted endurance exponent.
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

  /// Five pace bands. v60 is the speed sustainable for 60 minutes, which is
  /// close to threshold pace; band multipliers are speed fractions of v60
  /// (E 0.68–0.80, M 0.90–0.94, T 0.97–1.02, I 1.06–1.11, R 1.15–1.21),
  /// converted to pace so the bands stay monotonic.
  public static func paceZones(v60: Double?) -> [RunningPaceZone] {
    guard let v60, v60.isFinite, v60 > 0 else { return [] }
    let specs: [(String, String, String, Double, Double)] = [
      ("E", "轻松跑", "有氧基础与恢复", 0.68, 0.80),
      ("M", "马拉松配速", "比赛专项耐力", 0.90, 0.94),
      ("T", "阈值跑", "提升乳酸阈", 0.97, 1.02),
      ("I", "间歇跑", "提升最大摄氧", 1.06, 1.11),
      ("R", "冲刺跑", "速度与跑步经济性", 1.15, 1.21),
    ]
    return specs.map { code, name, purpose, slow, fast in
      RunningPaceZone(
        code: code, name: name, purpose: purpose,
        slowSecondsPerKm: 1000 / (v60 * slow),
        fastSecondsPerKm: 1000 / (v60 * fast))
    }
  }

  /// The endurance exponent used for projections: the athlete's fitted γ
  /// (distance ∝ time^γ), clamped to [0.88, 0.97]; Riegel's 1.06 (γ ≈ 0.943)
  /// when the server provided none.
  public static func projectionGamma(_ gamma: Double?) -> Double {
    guard let gamma, gamma.isFinite else { return 1 / 1.06 }
    return min(0.97, max(0.88, gamma))
  }

  /// Standard race distances projected from the 60-minute effort with the
  /// power law the ability model fitted: t = 3600 · (D / D60)^(1/γ).
  public static func racePredictions(v60: Double?, gamma: Double? = nil) -> [RunningRacePrediction] {
    guard let v60, v60.isFinite, v60 > 0, let gamma, gamma.isFinite, gamma > 0 else { return [] }
    let baseDistance = v60 * 3600
    let exponent = 1 / projectionGamma(gamma)
    let races: [(String, Double)] = [
      ("5 公里", 5_000),
      ("10 公里", 10_000),
      ("半程马拉松", 21_097.5),
      ("马拉松", 42_195),
    ]
    return races.map { name, distance in
      let seconds = 3600 * pow(distance / baseDistance, exponent)
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
