import Foundation
import SyncCore

public enum HRSettingDraft {
  public static func date(_ raw: String) -> Date? {
    let formatter = ISO8601DateFormatter()
    if let date = formatter.date(from: raw) { return date }
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    return formatter.date(from: raw)
  }

  public static func value(_ value: Double, method: String, basis: String, measuredAt: Date?) -> JSON {
    var fields: [String: JSON] = ["value": .number(value), "method": .string(method), "basis": .string(basis), "unit": .string("bpm")]
    if let measuredAt { fields["measuredAt"] = .string(ISO8601DateFormatter().string(from: measuredAt)) }
    return .object(fields)
  }
  public static func patch(maximum: JSON, resting: JSON?, mode: String, originalMaximum: JSON?, originalResting: JSON?, originalMode: String) -> [String: JSON] {
    var patch: [String: JSON] = [:]
    // Compare numeric fields semantically: JSON may decode a whole number as Int.
    func same(_ a: JSON?, _ b: JSON?) -> Bool {
      guard let a, let b else { return a == nil && b == nil }
      var af = a.fields, bf = b.fields
      let av = af.removeValue(forKey: "value"), bv = bf.removeValue(forKey: "value")
      let ad = af.removeValue(forKey: "measuredAt"), bd = bf.removeValue(forKey: "measuredAt")
      // Date pickers display dates; retain an untouched original subsecond value.
      let datesMatch: Bool
      if let x = ad?.text.flatMap(date), let y = bd?.text.flatMap(date) { datesMatch = Int(x.timeIntervalSince1970) == Int(y.timeIntervalSince1970) }
      else { datesMatch = ad == bd }
      return af == bf && av?.numericValue == bv?.numericValue && datesMatch
    }
    if !same(maximum, originalMaximum) { patch["maxHR"] = maximum }
    if mode != originalMode || !same(resting, originalResting) {
      patch["restingHR"] = .object(["mode": .string(mode), "manualValue": resting ?? .null])
    }
    return patch
  }
}
private extension JSON {
  var numericValue: Double? { switch self { case .number(let n): n; case .int(let n): Double(n); default: nil } }
}
public enum HeartRateReferenceText {
  public static func source(_ source: String) -> String {
    switch source {
    case "manual": "固定手填参考"
    case "manual_fallback": "自动数据不足，使用手填备用值"
    case "baseline_28d": "跑前 28 天自动基线"
    case "baseline_sparse": "按跑前稀疏数据估计"
    default: "暂无可用参考"
    }
  }
  public static func reason(_ code: String) -> String? {
    switch code {
    case "rhr_manual_review_due", "hrmax_review_due": "参考较旧，建议复核；仍用于本次计算。"
    case "rhr_measurement_date_unknown": "手填参考的测量日期未知，结果为估计。"
    case "legacy_date_semantics": "旧设置的测量日期可能与生效日期混用，按估计参考处理。"
    case "historical_assumption": "这次历史训练使用你确认的计算设置，并非当时实测。"
    case "rhr_sparse_baseline": "静息数据较稀疏，参考为估计。"
    case "rhr_manual_fallback": "静息数据不足，使用手填备用值。"
    case "rhr_observations_insufficient", "rhr_not_configured": "缺少静息参考，可同步健康数据或设置备用值。"
    case "hrmax_missing": "缺少最大心率参考，请在心率设置中填写。"
    case "reference_conflict": "心率参考存在冲突，请检查设置。"
    default: nil
    }
  }
}
public struct WorkoutHeartRateReference: Codable, Sendable {
  public let schemaVersion: String?
  public let policyVersion: String?
  public let stackID: String?
  public let maxHR: HeartRateReferenceComponent
  public let restingHR: HeartRateReferenceComponent
}
public struct HeartRateReferenceComponent: Codable, Sendable {
  public let value: Double?
  public let source: String
  public let basis: String?
  public let status: String
  public let measuredAt: String?
  public let effectiveFrom: String?
  public let sampleDays: Int?
  public let latestSampleAt: String?
  public let reviewRecommended: Bool?
  public let reasonCodes: [String]?
}
