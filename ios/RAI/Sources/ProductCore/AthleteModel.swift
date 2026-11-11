import Foundation
import SyncCore

// Raw server vocabulary is preserved, including future status/reason/unit
// values. Unknown vocabulary never participates in an unsafe numeric fallback.
public struct ProductCode: RawRepresentable, Codable, Hashable, Sendable {
  public var rawValue: String
  public init(rawValue: String) { self.rawValue = rawValue }
  public init(from decoder: Decoder) throws { rawValue = try decoder.singleValueContainer().decode(String.self) }
  public func encode(to encoder: Encoder) throws { var c = encoder.singleValueContainer(); try c.encode(rawValue) }
}
public struct ProductMetricState: RawRepresentable, Codable, Hashable, Sendable {
  public var rawValue: String
  public init(rawValue: String) { self.rawValue = rawValue }
  public init(from decoder: Decoder) throws { rawValue = try decoder.singleValueContainer().decode(String.self) }
  public func encode(to encoder: Encoder) throws { var c = encoder.singleValueContainer(); try c.encode(rawValue) }
  public var isKnown: Bool { ["available", "limited", "collecting", "unavailable", "stale"].contains(rawValue) }
}
public struct ProductUnit: RawRepresentable, Codable, Hashable, Sendable {
  public var rawValue: String
  public init(rawValue: String) { self.rawValue = rawValue }
  public init(from decoder: Decoder) throws { rawValue = try decoder.singleValueContainer().decode(String.self) }
  public func encode(to encoder: Encoder) throws { var c = encoder.singleValueContainer(); try c.encode(rawValue) }
}
public struct MetricLineageSummary: Codable, Equatable, Sendable { public var dataSources: [ProductCode] }
public struct MetricEnvelope: Codable, Equatable, Sendable {
  public var id: String, metric: String, modelVersion: String, payloadVersion: String, mappingVersion: String
  public var state: ProductMetricState
  public var modelStatus: ProductCode, quality: ProductCode
  public var confidence: Double?
  public var evidenceSupport: ProductCode
  public var asOf: Int64?, evaluationAt: Int64
  public var freshness: ProductCode
  public var updatedAt: Int64?
  public var reasonCodes: [ProductCode], productReasonCodes: [ProductCode]
  public var lineageSummary: MetricLineageSummary
  public var technicalError: ProductCode?
  public var supportsPresentation: Bool {
    let versions = ["trainingLoad": ["rai.training_load.hrr_integral/v1", "rai.training_load.hrr_integral/v2"],
      "runningAbility": ["rai.running_ability/v1", "rai.running_ability/v2"],
      "externalVO2Max": ["rai.vo2max.external_projection/v1"],
      "physiologicalRecovery": ["rai.recovery.physiological/v1", "rai.recovery.physiological/v2"],
      "trainingReadiness": ["rai.readiness.training/v1", "rai.readiness.training/v2", "rai.readiness.training/v3"],
      "raiVO2Max": ["rai.vo2max.submax/v1"]]
    return versions[metric]?.contains(modelVersion) == true && state.isKnown
      && ["rai.product_metric_mapping/1", "rai.product_metric_mapping/2"].contains(mappingVersion)
      && ["fresh", "current", "aging", "stale", "expired", "unknown"].contains(freshness.rawValue)
  }
  public var presentationValueAvailable: Bool {
    supportsPresentation && ["available", "limited"].contains(state.rawValue)
      && !["stale", "expired"].contains(freshness.rawValue) && technicalError == nil
  }
  public var presentationState: ProductMetricState {
    if modelVersion.isEmpty && state.isKnown && ["rai.product_metric_mapping/1", "rai.product_metric_mapping/2"].contains(mappingVersion) { return state }
    return supportsPresentation ? state : ProductMetricState(rawValue: "limited")
  }
  public var stateLocalizationKey: String { "athlete_model.state." + presentationState.rawValue }
  // Confidence is evidence support; V1 exposes no probability/percentage label.
  public var evidenceLocalizationKey: String {
    let known = ["not_presented", "limited_evidence", "moderate_evidence", "stronger_evidence"]
    return "athlete_model.evidence." + (known.contains(evidenceSupport.rawValue) ? evidenceSupport.rawValue : "not_presented")
  }
  public func reasonLocalizationKey(_ reason: ProductCode) -> String {
    let known = ["not_enough_history", "insufficient_evidence", "data_sync_incomplete", "source_conflict", "source_selection_required", "data_stale",
      "missing_heart_rate", "recovery_data_incomplete", "training_load_unknown", "no_sample", "deleted", "not_product_activated"]
    return "athlete_model.reason." + (known.contains(reason.rawValue) ? reason.rawValue : "generic")
  }
}
public struct MetricFact: Codable, Equatable, Sendable { public var code: ProductCode; public var value: Double?; public var unit: ProductUnit }
public struct MetricExplanation: Codable, Equatable, Sendable { public var headline: ProductCode; public var supportingFacts: [MetricFact]; public var limitations: [ProductCode] }
public struct MetricTrend: Codable, Equatable, Sendable { public var kind: ProductCode; public var value: Double?; public var unit: ProductUnit; public var reasonCodes: [ProductCode] }
public struct AthleteMetric<Value: Codable & Equatable & Sendable>: Codable, Equatable, Sendable {
  public var envelope: MetricEnvelope
  public var value: Value?
  public var trend: MetricTrend
  public var explanation: MetricExplanation
  public private(set) var unsupportedPayload: JSON?
  private enum CodingKeys: String, CodingKey { case envelope, value, trend, explanation }
  public init(from decoder: Decoder) throws {
    let c = try decoder.container(keyedBy: CodingKeys.self)
    envelope = try c.decode(MetricEnvelope.self, forKey: .envelope)
    trend = try c.decode(MetricTrend.self, forKey: .trend)
    explanation = try c.decode(MetricExplanation.self, forKey: .explanation)
    if envelope.supportsPresentation {
      value = try c.decodeIfPresent(Value.self, forKey: .value)
      unsupportedPayload = nil
    } else {
      value = try? c.decodeIfPresent(Value.self, forKey: .value)
      unsupportedPayload = value == nil ? try c.decodeIfPresent(JSON.self, forKey: .value) : nil
    }
  }
  public func encode(to encoder: Encoder) throws {
    var c = encoder.container(keyedBy: CodingKeys.self)
    try c.encode(envelope, forKey: .envelope)
    try c.encode(trend, forKey: .trend)
    try c.encode(explanation, forKey: .explanation)
    if let unsupportedPayload { try c.encode(unsupportedPayload, forKey: .value) }
    else { try c.encode(value, forKey: .value) }
  }
  // An unknown model payload can still be decoded/round-tripped and inspected.
  // Existing UI must use presentationValue and the server state mapping.
  public var presentationValue: Value? {
    guard envelope.supportsPresentation, ["available", "limited"].contains(envelope.state.rawValue),
      !["stale", "expired"].contains(envelope.freshness.rawValue), envelope.technicalError == nil else { return nil }
    return value
  }
}
public struct TrainingLoadValue: Codable, Equatable, Sendable {
  public var latestWorkoutLoad: Double?, latestWorkoutAsOf: Int64?
  public var acute: Double?, chronic: Double?, balance: Double?, trend7: Double?
  public var knownness: ProductCode, unit: ProductUnit, stateUnit: ProductUnit, stateModelVersion: String
}
public struct RunningAbilityValue: Codable, Equatable, Sendable {
  public var score: Double?, v60: Double?, gamma: Double?, enduranceRetention: Double?
  public var stability: ProductCode
  public var evidenceMode: ProductCode, evidenceCount: Int, speedUnit: ProductUnit, scoreUnit: ProductUnit
}
public struct RAIVO2Value: Codable, Equatable, Sendable {
  public var value: Double?
  public var unit: ProductUnit
  public var experimental: Bool
  public var evidenceMode: ProductCode
  public var stability: ProductCode
  public var independentWorkoutCount: Int
  public var sensitivityLow: Double?
  public var sensitivityHigh: Double?
  public var sensitivityRelativeWidth: Double?
  public var source: ProductCode
}
public struct ExternalVO2Value: Codable, Equatable, Sendable {
  public var value: Double?
  public var unit: ProductUnit
  public var provider: ProductCode
  public var origin: ProductCode?
  public var sampleDate: Int64?
  public var externalEstimate: Bool
}
public struct RecoveryComponentState: Codable, Equatable, Sendable { public var availability: ProductCode, relativeToBaseline: ProductCode }
public struct RecoveryValue: Codable, Equatable, Sendable {
  public var score: Double?, unit: ProductUnit, baselineState: ProductCode
  public var hrv: RecoveryComponentState, rhr: RecoveryComponentState, sleep: RecoveryComponentState
  /// normal / mild_deviation / marked_deviation; absent without a score.
  public var deviationLevel: String?
}
public struct ReadinessValue: Codable, Equatable, Sendable {
  public var score: Double?, recoveryScore: Double?, loadMultiplier: Double?, recentRecordedLoad: Double?
  public var unit: ProductUnit, scope: ProductCode
}
public typealias TrainingLoadMetric = AthleteMetric<TrainingLoadValue>
public typealias RunningAbilityMetric = AthleteMetric<RunningAbilityValue>
public typealias ExternalVO2Metric = AthleteMetric<ExternalVO2Value>
public typealias RAIVO2Metric = AthleteMetric<RAIVO2Value>
public typealias RecoveryMetric = AthleteMetric<RecoveryValue>
public typealias ReadinessMetric = AthleteMetric<ReadinessValue>
public struct AthleteProductAssignment: Codable, Equatable, Sendable { public var channel: ProductCode, stackID: String, version: Int64 }
public struct AthleteModelSnapshot: Codable, Equatable, Sendable {
  public var schemaVersion: String, asOf: Int64, evaluationAt: Int64, datasetRevision: Int64
  public var timezone: String, calendarDate: String, assignment: AthleteProductAssignment, responseState: ProductCode
  public var presentationOrder: [ProductCode], notices: [ProductCode]
  public var trainingLoad: TrainingLoadMetric, runningAbility: RunningAbilityMetric, externalVO2Max: ExternalVO2Metric
  public var physiologicalRecovery: RecoveryMetric, trainingReadiness: ReadinessMetric
  public var raiVO2Max: RAIVO2Metric?
  public var supportsPresentation: Bool { ["rai.athlete_model/1", "rai.athlete_model/2"].contains(schemaVersion) }
}


public struct AthleteHistoryPoint: Codable, Equatable, Sendable, Identifiable {
  public var date: String, metric: String, state: String, unit: String, modelVersion: String, payloadVersion: String
  public var value: Double?
  public var reasonCodes: [String]
  /// Other known scalars of the metric (trainingLoad: chronic, balance, dailyLoad).
  public var components: [String: Double]?
  public var id: String { date }
}
public struct AthleteHistoryPage: Codable, Equatable, Sendable {
  public var metric: String, view: String
  public var points: [AthleteHistoryPoint]
  public var nextCursor: String?
  public var assignment: AthleteProductAssignment
}

// Read-only product projection of research diagnostics. Values are display
// scalars only; evidence arrays and source epochs never leave the server.
public struct AthleteDiagnosticSupport: Codable, Equatable, Sendable {
  public var level: String, fraction: Double, count: Int
}
public struct AthleteDiagnosticMaximal: Codable, Equatable, Sendable {
  public var score: Double?, v60: Double?, gamma: Double?, enduranceRetention: Double?
  public var confidence: Double, anchorDay: String?
}
public struct AthleteDiagnosticDurability: Codable, Equatable, Sendable {
  public var workoutID: String
  public var durationSeconds: Double, analyzedSeconds: Double
  public var ef1: Double?, ef2: Double?, decoupling: Double?, paceDecay: Double?
}
public struct AthleteDiagnosticEfficiency: Codable, Equatable, Sendable {
  public var workoutID: String
  public var efficiencyMPerBeat: Double?, speedMPerMin: Double?, heartRateBPM: Double?
  public var validSeconds: Double
}
public struct AthleteDiagnosticCriticalSpeed: Codable, Equatable, Sendable {
  public var csMPS: Double?, dPrimeMeters: Double?, r2: Double?
  public var workoutCount: Int
}
public struct AthleteDiagnosticSettling: Codable, Equatable, Sendable {
  public var hours: Double?, bucket: String?
  public var load: Double, baseline: Double, score: Double
}
public struct AthleteDiagnosticItem: Codable, Equatable, Sendable, Identifiable {
  public var key: String, model: String, version: String, schemaVersion: String
  public var experimental: Bool
  public var status: String
  public var reasonCodes: [String]
  public var support: AthleteDiagnosticSupport
  public var asOfUs: Int64, publishedAtUs: Int64
  public var day: String
  public var maximal: AthleteDiagnosticMaximal?
  public var durability: AthleteDiagnosticDurability?
  public var efficiency: AthleteDiagnosticEfficiency?
  public var criticalSpeed: AthleteDiagnosticCriticalSpeed?
  public var loadSettling: AthleteDiagnosticSettling?
  public var id: String { key }
  public var isAvailable: Bool { ["available", "partial"].contains(status) }
}
public struct AthleteDiagnostics: Codable, Equatable, Sendable {
  public var schemaVersion: String
  public var asOfUs: Int64
  public var calendarDate: String
  public var assignment: AthleteProductAssignment
  public var experimental: Bool
  public var items: [AthleteDiagnosticItem]
  public var supportsPresentation: Bool { schemaVersion == "rai.athlete_diagnostics/1" }
  public func item(_ key: String) -> AthleteDiagnosticItem? { items.first { $0.key == key } }
}
