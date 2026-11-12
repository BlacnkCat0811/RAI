import Foundation

// Running fitness v1 (rai.running_fitness/1). Every number is computed by the
// server; the client only formats it. There is no local prediction, zone or
// score formula: a missing value stays missing.

public struct FitnessRange: Codable, Equatable, Sendable {
  public var low: Double, high: Double
}

public struct FitnessOverall: Codable, Equatable, Sendable {
  public var score: Double?, rawScore: Double?, saturation: String?
  public var range: FitnessRange?
  /// calibrated | estimated | prior_based | conflict | none
  public var basis: String
  public var marathonEquivalentSeconds: Double?
}

public struct FitnessParams: Codable, Equatable, Sendable {
  public var a70MPS: Double?, vTMPS: Double?, v6MPS: Double?, v90sMPS: Double?, bL: Double?, bS: Double?
  public var bLStatus: String, bSStatus: String
}

public struct FitnessDimension: Codable, Equatable, Sendable {
  /// supported | estimated | prior_based | lower_bound_only | insufficient_evidence
  public var status: String
  public var basis: String
  public var speedMPS: Double?, paceSecondsPerKm: Double?, rating: Double?
  public var speedRange: FitnessRange?
  public var lowerBoundMPS: Double?
  public var zone: String
  public var evidenceHint: String?
  public var retention: Double?, bL: Double?, bLStatus: String?
  /// A rating is shown only with direct evidence; a prior is never a measured dimension.
  public var hasRating: Bool { rating != nil && ["supported", "estimated"].contains(status) }
}

public struct FitnessDimensions: Codable, Equatable, Sendable {
  public var endurance: FitnessDimension, threshold: FitnessDimension, speed: FitnessDimension, sprint: FitnessDimension
  public var ordered: [(key: String, value: FitnessDimension)] {
    [("endurance", endurance), ("threshold", threshold), ("speed", speed), ("sprint", sprint)]
  }
}

public struct FitnessPrediction: Codable, Equatable, Sendable, Identifiable {
  public var id: String { distance }
  public var distance: String, distanceM: Double
  public var seconds: Double?
  public var range: FitnessRange?
  /// direct | calibrated | prior_based | insufficient
  public var predictionSupport: String
  public var status: String
  public var targetEligible: Bool
  public var longRunCovered: Bool?
  public var conditions: String
  public var reasonCodes: [String]
}

public struct FitnessRacePace: Codable, Equatable, Sendable, Identifiable {
  public var id: String { distance }
  public var distance: String
  public var paceSecondsPerKm: Double?
  public var paceRange: FitnessRange?
  public var support: String
  public var schedulable: Bool
}

public struct FitnessZone: Codable, Equatable, Sendable, Identifiable {
  public var id: String { name }
  public var name: String, label: String
  public var fastPaceSecondsPerKm: Double?, slowPaceSecondsPerKm: Double?
  public var fastSpeedMPS: Double?, slowSpeedMPS: Double?
  public var fastBoundRange: FitnessRange?
  public var openEnded: Bool?
  public var basis: String, support: String
  public var schedulable: Bool
  public var reasonCodes: [String]
}

public struct FitnessZones: Codable, Equatable, Sendable {
  public var zones: [FitnessZone]
  public var validUntil: String?
  public var reasonCodes: [String]
  public func zone(_ name: String) -> FitnessZone? { zones.first { $0.name == name } }
}

public struct FitnessPreparation: Codable, Equatable, Sendable {
  public var longRuns90Min: Int, longestRunSeconds: Double, effectiveTrainingWeeks: Int
  public var latestRetention: Double?
}

public struct FitnessSupport: Codable, Equatable, Sendable {
  public var independentDays: Int, spanDays: Int, workouts: Int, races: Int
  public var conditionDownweightedShare: Double, effectiveWeight: Double
}

public struct FitnessStability: Codable, Equatable, Sendable {
  public var changeUnconfirmed: Bool
  public var looMaxScoreDelta: Double, looMaxAnchorDelta: Double
}

public struct FitnessSingleBrief: Codable, Equatable, Sendable, Identifiable {
  public var id: String { workoutID }
  public var workoutID: String, day: String
  public var score: Double?
  public var range: FitnessRange?
  public var type: String, status: String
}

public struct RunningFitnessValue: Codable, Equatable, Sendable {
  public var publicationID: String, algorithmVersion: String, ratingPolicy: String, historyView: String
  public var date: String
  /// initial | preliminary | established | race_only | insufficient_evidence | conflict
  public var status: String
  /// fresh | aging | stale | race_only | expired
  public var freshness: String
  public var latestEvidenceDay: String?, validUntil: String?
  public var overall: FitnessOverall
  public var referenceDailyScore: Double?
  public var params: FitnessParams
  public var dimensions: FitnessDimensions
  public var predictions: [FitnessPrediction]
  public var zones: FitnessZones
  public var racePaces: [FitnessRacePace]
  public var preparation: FitnessPreparation
  public var support: FitnessSupport
  public var stability: FitnessStability
  public var singleRuns: [FitnessSingleBrief]
  public var reasonCodes: [String]
}
public typealias RunningFitnessMetric = AthleteMetric<RunningFitnessValue>

// MARK: - Single run (workout detail)

public struct FitnessSegmentUse: Codable, Equatable, Sendable {
  public var kind: String, startUs: Int64, endUs: Int64
  public var hrr: Double?, a70MPS: Double?, weight: Double
  public var status: String, reason: String?, domain: String?, seconds: Double
}

public struct FitnessHRSingle: Codable, Equatable, Sendable {
  public var a70MPS: Double, score: Double, rawScore: Double
  public var saturation: String?
  public var scoreSensitivityRange: FitnessRange, abilitySensitivityRange: FitnessRange
  public var netSupportedSeconds: Double, quality: Double
  public var gradeMode: String, conditionWeight: Double
  public var segments: [FitnessSegmentUse]
}

public struct FitnessRaceSingle: Codable, Equatable, Sendable {
  public var kind: String, distanceM: Double, elapsedSeconds: Double
  public var score: Double, rawScore: Double, quality: Double, status: String
}

public struct FitnessRetention: Codable, Equatable, Sendable { public var retention: Double, drift: Double }

public struct FitnessSingleResult: Codable, Equatable, Sendable {
  public var workoutID: String, day: String
  /// supported | limited | insufficient_evidence | unavailable
  public var status: String
  public var evidenceTypes: [String], reasonCodes: [String]
  public var hrNormalized: FitnessHRSingle?
  public var raceEquivalent: FitnessRaceSingle?
  public var demonstratedV90sMPS: Double?, sprintTestV90sMPS: Double?
  public var retention: FitnessRetention?
  public var runningSeconds: Double
  public var hrMaxSuggestion: Double?
  public var indoor: Bool
  public var effortKind: String?
}

public struct WorkoutRunFitness: Codable, Equatable, Sendable {
  public var single: FitnessSingleResult
  public var referenceDailyScore: Double?, referenceDate: String?, deltaToReference: Double?
  public var publishedDate: String, algorithmVersion: String, ratingPolicy: String
}

// MARK: - History, evidence, re-analysis

public struct RunningFitnessDay: Codable, Equatable, Sendable, Identifiable {
  public var id: String { date }
  public var date: String, status: String, freshness: String
  public var overall: FitnessOverall
  public var referenceDailyScore: Double?
  public var params: FitnessParams
  public var singleRuns: [FitnessSingleBrief]
  public var publicationID: String, publishedAt: Int64, configHash: String
}

public struct RunningFitnessHistory: Codable, Equatable, Sendable {
  public var schemaVersion: String, view: String, algorithmVersion: String, ratingPolicy: String
  public var stackID: String, from: String, to: String
  public var days: [RunningFitnessDay]
  public var nextCursor: String?
}

public struct RunningFitnessReanalysis: Codable, Equatable, Sendable {
  public var operationID: String
  /// queued | running | publishing | completed | failed | cancelled
  public var status: String
  public var view: String, stackID: String, algorithmVersion: String, referencePolicy: String
  public var from: String, through: String
  public var totalDays: Int, completedDays: Int, failedDays: Int
  public var createdAt: Int64
  public var errorCode: String?
  public var finished: Bool { ["completed", "failed", "cancelled"].contains(status) }
}

// MARK: - Runner-facing text (no implementation vocabulary)

public enum RunningFitnessText {
  public static func dimensionName(_ key: String) -> String {
    ["endurance": "有氧耐力", "threshold": "阈值", "speed": "速度", "sprint": "冲刺"][key] ?? "能力"
  }
  public static func dimensionAnchor(_ key: String) -> String {
    ["endurance": "70% 心率储备等效配速", "threshold": "阈值配速（估计）", "speed": "6 分钟全力配速", "sprint": "90 秒全力配速"][key] ?? ""
  }
  public static func evidenceHint(_ key: String) -> String {
    switch key {
    case "endurance": return "持续 20 分钟以上的稳定轻松跑会补充证据"
    case "threshold": return "20–40 分钟阈值测试或比赛可校准"
    case "speed": return "3–12 分钟全力测试或短距离比赛可校准"
    case "sprint": return "在两天以上的训练里完成 90 秒左右的快速重复"
    default: return ""
    }
  }
  public static func dimensionStatus(_ status: String) -> String {
    switch status {
    case "supported": return "有依据"
    case "estimated": return "估计"
    case "prior_based": return "按默认假设"
    case "lower_bound_only": return "仅已展示下限"
    default: return "待补充"
    }
  }
  public static func stage(_ status: String) -> String {
    switch status {
    case "established": return "已建立"
    case "preliminary": return "初步"
    case "initial": return "首次估计"
    case "race_only": return "仅比赛成绩"
    case "conflict": return "证据冲突"
    default: return "积累中"
    }
  }
  public static func freshness(_ raw: String) -> String {
    switch raw {
    case "fresh": return "近期有训练"
    case "aging": return "两周以上没有新证据"
    case "stale": return "超过四周没有新证据，暂停自动调整配速"
    case "race_only": return "只有比赛成绩"
    default: return "证据已过期"
    }
  }
  public static func basis(_ raw: String) -> String {
    switch raw {
    case "calibrated": return "有比赛成绩校准"
    case "estimated": return "由日常心率与配速估计"
    case "prior_based": return "耐力保持采用默认假设"
    case "conflict": return "证据互相矛盾"
    default: return "暂无依据"
    }
  }
  public static func predictionSupport(_ raw: String) -> String {
    switch raw {
    case "direct": return "有同距离成绩"
    case "calibrated": return "有成绩校准"
    case "prior_based": return "理论范围"
    default: return "证据不足"
    }
  }
  public static func distanceName(_ raw: String) -> String {
    ["5k": "5 公里", "10k": "10 公里", "half": "半程马拉松", "marathon": "马拉松"][raw] ?? raw
  }
  public static func zoneName(_ name: String) -> String {
    ["Z1": "恢复", "Z2": "有氧耐力", "Z3": "有氧能力", "Z4": "阈值", "Z5": "无氧耐力", "Z6": "无氧能力"][name] ?? name
  }
  public static func zoneSupport(_ zone: FitnessZone) -> String {
    if zone.reasonCodes.contains("zone_anchor_conflict") { return "有氧与阈值依据冲突，暂不给出" }
    switch zone.support {
    case "supported": return zone.schedulable ? "可用于训练计划" : "仅参考"
    case "estimated": return zone.schedulable ? "估计，计划取保守端" : "估计，仅参考"
    case "limited": return "有限参考"
    case "prior_based": return "按默认假设，仅参考"
    default: return "需要相应训练才能给出"
    }
  }
  public static func singleStatus(_ raw: String) -> String {
    switch raw {
    case "supported": return "可靠"
    case "limited": return "有限"
    case "unavailable": return "无法分析"
    default: return "证据不足"
    }
  }
  /// Why a run produced no single-run score, in runner words.
  public static func singleReason(_ codes: [String]) -> String {
    for code in codes {
      switch code {
      case "hr_reference_missing": return "缺少最大心率设置"
      case "hr_reference_conflict": return "心率设置存在冲突"
      case "distance_mismatch": return "速度来源不可靠"
      case "steady_support_low", "net_support_short": return "没有足够长的稳定跑步片段"
      case "features_unavailable": return "还在分析这次跑步"
      default: continue
      }
    }
    return "缺少可用的心率与配速片段"
  }
  public static func effortName(_ kind: String) -> String {
    ["race": "比赛", "threshold_test": "阈值测试", "speed_test": "速度测试", "sprint_test": "冲刺测试"][kind] ?? "普通训练"
  }
  public static func reanalysisStatus(_ op: RunningFitnessReanalysis) -> String {
    switch op.status {
    case "completed": return "重算完成"
    case "failed": return "部分日期重算失败，已保留原结果"
    case "running": return "重算中 \(op.completedDays)/\(op.totalDays) 天"
    default: return "已排队"
    }
  }
  public static func pace(_ secondsPerKm: Double?) -> String {
    guard let s = secondsPerKm, s.isFinite, s > 0 else { return "--" }
    let total = Int(s.rounded())
    return String(format: "%d′%02d″", total / 60, total % 60)
  }
  public static func raceTime(_ seconds: Double?) -> String {
    guard let seconds, seconds.isFinite, seconds >= 0 else { return "--" }
    let total = Int(seconds.rounded())
    if total >= 3600 { return String(format: "%d:%02d:%02d", total / 3600, (total % 3600) / 60, total % 60) }
    return String(format: "%d:%02d", total / 60, total % 60)
  }
}
