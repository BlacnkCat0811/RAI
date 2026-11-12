import Foundation
import SyncCore

// Typed native detail contracts. Every derived field is optional because an
// unpublished analysis legitimately has no value; the UI must distinguish
// "not computed yet" from a measured zero.
public struct WorkoutDetailResponse: Codable, Sendable {
  public let workout: WorkoutFacts
  public let analysis: WorkoutFeatureAnalysis?
  public let environment: WorkoutEnvironment?
  public let summary: WorkoutSummary
  public let trainingLoad: WorkoutLoad?
  public let zoneSummary: WorkoutZoneSummary?
  public let distanceDistribution: WorkoutDistanceDistribution?
  public let route: WorkoutRoute
  public let override: WorkoutOverride?
  /// Whether the active model admits this run as known training load; absent
  /// until the model stack has computed it (and from older servers).
  public let modelQualification: WorkoutModelQualification?
  public let loadFocus: WorkoutLoadFocus?
  /// This run's single-run fitness from the published day state, with the
  /// fixed-policy reference of the day before it; absent until published.
  public let runFitness: WorkoutRunFitness?
}

public struct WorkoutModelQualification: Codable, Sendable, Equatable {
  public let trainingLoad: WorkoutQualification
}

public struct WorkoutQualification: Codable, Sendable, Equatable {
  public let status: String
  public let reasonCodes: [String]
}

/// One line per model: admitted or not, and why not.
public enum WorkoutQualificationText {
  public static func trainingLoad(_ q: WorkoutQualification) -> String {
    let status = ["eligible": "完整心率", "conditional": "估算", "ineligible": "未知", "pending": "计算中"][q.status] ?? "未知"
    return line("负荷", status, q.reasonCodes)
  }
  public static func reason(_ code: String) -> String {
    [
      "missing_hr": "缺少心率", "hr_missing": "缺少心率", "hr_coverage_low": "心率覆盖不足", "active_time_uncertain": "运动时间不确定",
      "speed_gap": "速度数据断档", "route_accuracy_low": "路线精度低", "grade_unknown": "坡度未知", "distance_mismatch": "距离不一致",
      "distance_missing": "缺少距离", "pause_contaminated": "暂停影响分段", "source_conflict": "来源冲突", "source_unknown": "来源未知",
      "duration_too_short": "时长过短", "environment_unknown": "环境未知", "parameter_missing": "缺少心率参数",
      "parameter_conflict": "心率参数冲突", "invalid_observation": "数据异常", "signal_outlier": "信号异常",
      "unsupported_input": "不支持的数据", "effort_unconfirmed": "未确认为全力", "cross_workout_evidence_required": "需要其他训练印证",
      "indoor_unsupported": "室内跑不计入", "steady_support_low": "稳定配速段不足",
    ][code] ?? code
  }
  static func line(_ name: String, _ status: String, _ reasons: [String]) -> String {
    let why = reasons.map(reason)
    return name + "：" + status + (why.isEmpty ? "" : "（" + why.joined(separator: "、") + "）")
  }
}

public struct WorkoutFeatureAnalysis: Codable, Sendable {
  public let schemaVersion: String
  public let activeSeconds: Double?
  public let heartRateCoverage: Double
  public let speedCoverage: Double
  public let routeCoverage: Double
  public let reasonCodes: [String]
}

public struct WorkoutFacts: Codable, Sendable {
  public let workoutID: String
  public let startedAtUs: Int64
  public let endedAtUs: Int64
  public let durationS: Double
  public let elapsedS: Double
  public let distanceM: Double?
  public let activityType: Int64
  public let activeEnergyKcal: Double?
  public let averagePaceSPerKm: Double?
  public let averageHeartRateBpm: Double?
  public let averageCadenceSpm: Double?
  public let averagePowerW: Double?
  public let averageStrideLengthM: Double?
  public let averageVerticalOscillationCm: Double?
  public let averageGroundContactTimeMs: Double?
  public let indoor: Bool?
  public let sessionType: String
  public let sourceCategory: String
  public let analysisStatus: String
  public let analysisAvailable: Bool
  public let modelVersion: String
  public let assessmentStatus: String
  public let qualityScore: Double?
  public let heartRateCoverage: Double?
  public let heartRateSampleCount: Int
  public let routeQuality: Double?
  public let routePointCount: Int
  public let sourceConfidence: Double?
  public let stimulusConfidence: Double?
  public let stimulusType: String
  public let missingReasons: [String]
  public let explanationCodes: [String]

  public var startDate: Date { Date(timeIntervalSince1970: Double(startedAtUs) / 1_000_000) }
  public var endDate: Date { Date(timeIntervalSince1970: Double(endedAtUs) / 1_000_000) }
  public var distanceKM: Double? { distanceM.map { $0 / 1000 } }
}

public struct WorkoutEnvironment: Codable, Sendable {
  public let weatherCondition: Int?
  public let temperatureC: Double?
  public let humidityPercent: Double?
  public let status: String
  public let policyVersion: String
  public let reasonCodes: [String]
  public let correction: EnvironmentCorrection?
  public let adjusted: EnvironmentAdjusted?
}

public struct EnvironmentCorrection: Codable, Sendable {
  public let environment: Double?
  public let elevation: Double?
  public let total: Double?
}

public struct EnvironmentAdjusted: Codable, Sendable {
  public let durationS: Double?
  public let paceSPerKm: Double?
}

public struct WorkoutSummary: Codable, Sendable {
  public let averagePaceSPerKm: Double?
  public let gradeAdjustedPaceSPerKm: Double?
  public let coveredSplits: Int
  public let totalSplits: Int
  public let elevationGainM: Double?
  public let routeQuality: Double?
}

public struct WorkoutLoad: Codable, Sendable {
  public let load: Double?
  public let intensity: Double?
  public let confidence: Double?
  public let source: String
  public let stimulusType: String
  public let rpe: Double?
  public let missingReasons: [String]
}

public struct ZoneExposure: Codable, Sendable, Identifiable {
  public let zone: String
  public let durationSeconds: Double
  public let distanceMeters: Double
  public let fraction: Double
  public var id: String { zone }
}

public struct StimulusScore: Codable, Sendable, Identifiable {
  public let stimulusType: String
  public let qualifyingSeconds: Double
  public let targetSeconds: Double
  public let score: Double
  public let confidence: Double
  public var id: String { stimulusType }
}

public struct WorkoutZoneSummary: Codable, Sendable {
  public let zoneProfileID: String
  public let stimulusType: String
  public let secondaryStimulusTypes: [String]
  public let stimulusConfidence: Double?
  public let heartRateCoverage: Double
  public let paceCoverage: Double
  public let heartRateExposure: [ZoneExposure]
  public let paceExposure: [ZoneExposure]
  public let stimulusScores: [StimulusScore]
  public let explanationCodes: [String]
  public let paceZoneStatus: String
}

public struct RoutePoint: Codable, Sendable, Identifiable {
  public let latitude: Double
  public let longitude: Double
  public let elevationM: Double?
  public let speedMPS: Double?
  public let timestampUs: Int64
  public var id: Int64 { timestampUs }
}

public struct KilometerMarker: Codable, Sendable, Identifiable {
  public let kilometer: Int
  public let latitude: Double
  public let longitude: Double
  public let offsetS: Double
  public var id: Int { kilometer }
}

public struct WorkoutRoute: Codable, Sendable {
  public let points: [RoutePoint]
  public let kilometerMarkers: [KilometerMarker]
  public let pointCount: Int
}

public struct WorkoutOverride: Codable, Sendable {
  public let revision: Int64
  public let eligibilityOverride: String?
  public let statisticsParticipation: String?
  public let sessionType: String?
  public let performanceProtocol: String?
  public let rpe: Double?
  public let conditionTags: [String]
  public let note: String?
  public let createdAt: String
  public let source: String
}

public struct WorkoutMetricPoint: Codable, Sendable, Identifiable {
  public let offsetS: Double
  public let value: Double
  public var id: Double { offsetS }
  public var minutes: Double { offsetS / 60 }
}

public struct WorkoutMetricSeries: Codable, Sendable {
  public let unit: String
  public let totalSamples: Int
  public let points: [WorkoutMetricPoint]
}

public struct WorkoutSeriesBundle: Codable, Sendable {
  public let pace: WorkoutMetricSeries
  public let heartRate: WorkoutMetricSeries
  public let cadence: WorkoutMetricSeries
  public let strideLength: WorkoutMetricSeries
  public let power: WorkoutMetricSeries
  public let groundContactTime: WorkoutMetricSeries
  public let verticalOscillation: WorkoutMetricSeries
  public let elevation: WorkoutMetricSeries
}

public struct WorkoutSplitGrade: Codable, Sendable {
  public let policyVersion: String
  public let elevationChangeMeters: Double?
  public let gainMeters: Double?
  public let lossMeters: Double?
  public let gradePercent: Double?
  public let gradeAdjustedPaceSPerKm: Double?
  public let reasonCodes: [String]
}

public struct WorkoutSplit: Codable, Sendable, Identifiable {
  public let index: Int
  public let distanceM: Double
  public let durationS: Double
  public let averagePaceSPerKm: Double
  public let averageHeartRateBpm: Double?
  public let averageCadenceSpm: Double?
  public let grade: WorkoutSplitGrade?
  public var id: Int { index }
}

public struct WorkoutGradeSummary: Codable, Sendable {
  public let policyVersion: String
  public let coveredSplits: Int
  public let totalSplits: Int
  public let gainMeters: Double?
  public let lossMeters: Double?
  public let gradeAdjustedPaceSPerKm: Double?
  public let reasonCodes: [String]
}

public struct WorkoutSegment: Codable, Sendable, Identifiable {
  public let index: Int
  public let label: String
  public let purpose: String
  public let goalType: String
  public let goalMeters: Double?
  public let goalSeconds: Double?
  public let alertKind: String
  public let alertLower: Double?
  public let alertUpper: Double?
  public let startOffsetSeconds: Double
  public let durationSeconds: Double
  public let distanceMeters: Double
  public let averagePaceSPerKm: Double?
  public let averageHeartRateBpm: Double?
  public let averageCadenceSpm: Double?
  public var id: Int { index }
}

public struct WorkoutSegmentPlanMatch: Codable, Sendable {
  public let localDate: String
  public let kind: String
  public let revision: Int64
  public let matchedBy: String
  public let plannedDistanceM: Double?
  public let explainedFraction: Double
  public let reason: String
}

public struct RunningDynamicsMetric: Codable, Sendable, Identifiable {
  public let key: String
  public let label: String
  public let unit: String
  public let average: Double?
  public let driftFraction: Double?
  public let coverage: Double
  public let samples: Int
  public let reasonCodes: [String]
  public var id: String { key }
}

public struct RunningDynamicsFlag: Codable, Sendable, Identifiable {
  public let code: String
  public let detail: String
  public let comparableSamples: Int
  public var id: String { code }
}

public struct RunningDynamics: Codable, Sendable {
  public let policyVersion: String
  public let analysable: Bool
  public let metrics: [RunningDynamicsMetric]
  public let flags: [RunningDynamicsFlag]
  public let note: String
}

public struct WorkoutSeriesResponse: Codable, Sendable {
  public let workoutID: String
  public let startedAtUs: Int64
  public let endedAtUs: Int64
  public let durationS: Double
  public let splits: [WorkoutSplit]
  public let grade: WorkoutGradeSummary?
  public let segments: [WorkoutSegment]
  public let segmentPlan: WorkoutSegmentPlanMatch?
  public let segmentNote: String
  public let hasMeasuredWorkoutSteps: Bool
  public let dynamics: RunningDynamics?
  public let series: WorkoutSeriesBundle
}

public struct WorkoutFreshnessFacts: Codable, Sendable {
  public let committedAt: String
  public let lastBatchAt: String
  public let batches: Int
}

public struct WorkoutFreshnessAnalysis: Codable, Sendable {
  public let status: String
  public let available: Bool
  public let jobID: String
  public let publishedAt: String
}

public struct WorkoutFreshnessMetric: Codable, Sendable, Identifiable {
  public let metric: String
  public let samples: Int
  public let state: String
  public let lastAvailableAt: String
  public var id: String { metric }
}

public struct WorkoutFreshness: Codable, Sendable {
  public let workoutID: String
  public let facts: WorkoutFreshnessFacts
  public let analysis: WorkoutFreshnessAnalysis
  public let series: [WorkoutFreshnessMetric]
  public let completeness: String
  public let deliveryState: String
  public let analysisState: String
  public let stabilityState: String

  public func seriesState(_ key: String) -> String {
    series.first { $0.metric == key }?.state ?? "unknown"
  }
}

public enum WorkoutMetricKind: String, CaseIterable, Identifiable, Sendable {
  case pace, heartRate, cadence, strideLength, power, groundContactTime, verticalOscillation, elevation

  public var id: String { rawValue }
  public var title: String {
    switch self {
    case .pace: "配速"
    case .heartRate: "心率"
    case .cadence: "步频"
    case .strideLength: "步幅"
    case .power: "跑步功率"
    case .groundContactTime: "触地时间"
    case .verticalOscillation: "垂直振幅"
    case .elevation: "海拔"
    }
  }
  public var unit: String {
    switch self {
    case .pace: "s/km"
    case .heartRate: "bpm"
    case .cadence: "spm"
    case .strideLength, .elevation: "m"
    case .power: "W"
    case .groundContactTime: "ms"
    case .verticalOscillation: "cm"
    }
  }
  public var digits: Int {
    switch self {
    case .strideLength: 2
    case .verticalOscillation: 1
    default: 0
    }
  }
  public func series(_ s: WorkoutSeriesBundle) -> WorkoutMetricSeries {
    switch self {
    case .pace: s.pace
    case .heartRate: s.heartRate
    case .cadence: s.cadence
    case .strideLength: s.strideLength
    case .power: s.power
    case .groundContactTime: s.groundContactTime
    case .verticalOscillation: s.verticalOscillation
    case .elevation: s.elevation
    }
  }
  public func average(_ w: WorkoutFacts) -> Double? {
    switch self {
    case .pace: w.averagePaceSPerKm
    case .heartRate: w.averageHeartRateBpm
    case .cadence: w.averageCadenceSpm
    case .strideLength: w.averageStrideLengthM
    case .power: w.averagePowerW
    case .groundContactTime: w.averageGroundContactTimeMs
    case .verticalOscillation: w.averageVerticalOscillationCm
    case .elevation: nil
    }
  }
}

extension ProductAPI {
  public func workoutDetail(_ id: String) async throws -> WorkoutDetailResponse {
    try decode(await get("/v2/workouts/" + id + "/detail"))
  }
  public func workoutSeries(_ id: String) async throws -> WorkoutSeriesResponse {
    try decode(await get("/v2/workouts/" + id + "/series"))
  }
  public func workoutFreshness(_ id: String) async throws -> WorkoutFreshness {
    try decode(await get("/v2/workouts/" + id + "/freshness"))
  }
  private func decode<T: Decodable>(_ value: JSON) throws -> T {
    try JSONDecoder().decode(T.self, from: value.data())
  }
}

extension WorkoutSeriesBundle {
  public static let empty = WorkoutSeriesBundle(
    pace: WorkoutMetricSeries(unit: "s/km", totalSamples: 0, points: []),
    heartRate: WorkoutMetricSeries(unit: "count/min", totalSamples: 0, points: []),
    cadence: WorkoutMetricSeries(unit: "spm", totalSamples: 0, points: []),
    strideLength: WorkoutMetricSeries(unit: "m", totalSamples: 0, points: []),
    power: WorkoutMetricSeries(unit: "W", totalSamples: 0, points: []),
    groundContactTime: WorkoutMetricSeries(unit: "ms", totalSamples: 0, points: []),
    verticalOscillation: WorkoutMetricSeries(unit: "cm", totalSamples: 0, points: []),
    elevation: WorkoutMetricSeries(unit: "m", totalSamples: 0, points: []))
}
