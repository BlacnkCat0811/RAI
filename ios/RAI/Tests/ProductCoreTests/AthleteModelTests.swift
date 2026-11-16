import Foundation
import Testing
@testable import ProductCore

@Suite struct AthleteModelTests {
  @Test func sharedGoldenDecodeAndSemanticRoundTrip() throws {
    let root = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel"))
    let files = try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil).filter { $0.pathExtension == "json" }
    #expect(files.count == 11)
    for file in files {
      let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
      #expect(snapshot.schemaVersion == "rai.athlete_model/1")
      #expect(snapshot.assignment.channel.rawValue == "candidate")
      let copy = try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONEncoder().encode(snapshot))
      #expect(copy == snapshot)
      #expect(snapshot.trainingReadiness.trend.value == nil)
      if file.lastPathComponent == "new_user.json" {
        #expect(snapshot.trainingLoad.value == nil)
        #expect(snapshot.trainingReadiness.value == nil)
        #expect(snapshot.runningAbility.value == nil)
        #expect(snapshot.physiologicalRecovery.envelope.state.rawValue == "collecting")
        #expect(snapshot.physiologicalRecovery.envelope.presentationState.rawValue == "collecting")
      }
      if file.lastPathComponent == "future_unknown.json" {
        #expect(snapshot.runningAbility.envelope.modelVersion == "rai.running_ability/v99")
        #expect(snapshot.runningAbility.envelope.modelStatus.rawValue == "future_status")
        #expect(snapshot.runningAbility.envelope.quality.rawValue == "future_quality")
        #expect(snapshot.runningAbility.presentationValue == nil)
        #expect(snapshot.runningAbility.envelope.presentationState.rawValue == "limited")
        #expect(snapshot.runningAbility.envelope.reasonLocalizationKey(ProductCode(rawValue: "future_reason")) == "athlete_model.reason.generic")
      }
    }
  }
  @Test func unknownEnumsNeverCrashAndNullNeverBecomesZero() throws {
    let state = try JSONDecoder().decode(ProductMetricState.self, from: Data("\"future_state\"".utf8))
    #expect(state.rawValue == "future_state")
    #expect(!state.isKnown)
    let unit = try JSONDecoder().decode(ProductUnit.self, from: Data("\"future_unit\"".utf8))
    #expect(unit.rawValue == "future_unit")
    let root = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel"))
    let s = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: root.appendingPathComponent("expired_ability.json")))
    #expect(s.runningAbility.presentationValue == nil)
    #expect(s.runningAbility.value == nil)
    #expect(s.runningAbility.envelope.freshness.rawValue == "expired")
  }
  @Test func unknownModelCanChangeValueShape() throws {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel/future_unknown.json"))
    var root = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: file)) as? [String: Any])
    var load = try #require(root["trainingLoad"] as? [String: Any])
    var envelope = try #require(load["envelope"] as? [String: Any])
    envelope["modelVersion"] = "rai.training_load.hrr_integral/v99"
    load["envelope"] = envelope
    load["value"] = ["futureShape": "opaque preserved"]
    root["trainingLoad"] = load
    let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONSerialization.data(withJSONObject: root))
    #expect(snapshot.trainingLoad.presentationValue == nil)
    #expect(snapshot.trainingLoad.unsupportedPayload != nil)
    #expect(snapshot == (try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONEncoder().encode(snapshot))))
  }
}

@Suite struct AthleteModelV2Tests {
  @Test func schema2KeepsRAIAndExternalVO2Independent() throws {
    let root = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel"))
    var object = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: root.appendingPathComponent("full.json"))) as? [String: Any])
    object["schemaVersion"] = "rai.athlete_model/2"
    let envelope: [String: Any] = ["id": "raiVO2Max", "metric": "raiVO2Max", "modelVersion": "rai.vo2max.submax/v1",
      "payloadVersion": "rai.vo2max.submax/1", "mappingVersion": "rai.product_metric_mapping/2", "state": "available",
      "modelStatus": "available", "quality": "available", "evidenceSupport": "experimental_candidate", "evaluationAt": 1,
      "freshness": "current", "reasonCodes": [], "productReasonCodes": [], "lineageSummary": ["dataSources": ["recorded_running"]]]
    let value: [String: Any] = ["value": 52.0, "unit": "mL/kg/min", "experimental": true, "evidenceMode": "submax",
      "stability": "preliminary", "independentWorkoutCount": 3, "source": "rai_owned_submax"]
    object["raiVO2Max"] = ["envelope": envelope, "value": value, "trend": ["kind": "none", "unit": "", "reasonCodes": ["insufficient_serial_history"]],
      "explanation": ["headline": "raiVO2Max.state", "supportingFacts": [], "limitations": ["experimental_submax_estimate"]]]
    let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONSerialization.data(withJSONObject: object))
    #expect(snapshot.schemaVersion == "rai.athlete_model/2")
    #expect(snapshot.raiVO2Max?.presentationValue?.value == 52)
    #expect(snapshot.raiVO2Max?.presentationValue?.experimental == true)
    #expect(snapshot.externalVO2Max.value?.value != nil)
    #expect(snapshot.raiVO2Max?.presentationValue?.source.rawValue == "rai_owned_submax")
  }
}

@Suite struct AthleteModelPresentationStateTests {
  private func base() throws -> AthleteModelSnapshot {
    let root = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel/full.json"))
    return try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: root))
  }
  @Test func allNonAvailableStatesKeepNullAndLocalizeReason() throws {
    let cases: [(String, String, String?)] = [
      ("collecting", "current", nil), ("unavailable", "current", nil),
      ("stale", "stale", nil), ("conflict", "current", nil),
      ("sync_incomplete", "unknown", nil), ("technical_error", "current", "schema_mismatch"),
      ("limited", "current", nil),
    ]
    for (state, freshness, technical) in cases {
      var snapshot = try base()
      var envelope = snapshot.trainingReadiness.envelope
      envelope.state = ProductMetricState(rawValue: state)
      envelope.freshness = ProductCode(rawValue: freshness)
      envelope.modelVersion = "rai.readiness.training/v2"
      envelope.mappingVersion = "rai.product_metric_mapping/2"
      envelope.technicalError = technical.map(ProductCode.init(rawValue:))
      snapshot.trainingReadiness.envelope = envelope
      #expect(snapshot.trainingReadiness.envelope.presentationValueAvailable == (state == "limited"))
      let reason = ProductReasonText.text(for: envelope.productReasonCodes.first?.rawValue, state: envelope.presentationState.rawValue)
      #expect(!reason.isEmpty)
    }
  }
  @Test func primaryReasonDrivesTheShownReason() throws {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel/mixed.json"))
    var snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
    var envelope = snapshot.runningAbility.envelope
    // The frozen list is lexicographic; the server's primary reason wins.
    envelope.productReasonCodes = [ProductCode(rawValue: "data_stale"), ProductCode(rawValue: "not_enough_history")]
    envelope.primaryReason = ProductCode(rawValue: "not_enough_history")
    #expect(envelope.displayReason?.rawValue == "not_enough_history")
    // An older server without primaryReason falls back to the first code.
    envelope.primaryReason = nil
    #expect(envelope.displayReason?.rawValue == "data_stale")
    snapshot.runningAbility.envelope = envelope
    let copy = try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONEncoder().encode(snapshot))
    #expect(copy.runningAbility.envelope.primaryReason == nil)
    #expect(ProductReasonText.runningFitness(modelStatus: "collecting", reason: nil, state: "collecting")
      == "完成一次带心率、5 分钟以上的稳定跑步即可开始估计")
    #expect(ProductReasonText.runningFitness(modelStatus: "stale", reason: "data_stale", state: "stale") == "近期没有新数据")
  }
}

@Suite struct RunningFitnessV4Tests {
  @Test func runningFitnessDecodesWithoutLocalFormulas() throws {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/RunningFitnessV4.json"))
    let s = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
    #expect(s.supportsPresentation)
    #expect(s.schemaVersion == "rai.athlete_model/4")
    #expect(s.runningAbility.value == nil)
    #expect(s.runningAbility.envelope.modelStatus.rawValue == "model_update_required")
    let fitness = try #require(s.runningFitness?.presentationValue)
    #expect(fitness.overall.score != nil)
    #expect(fitness.overall.range != nil)
    #expect(fitness.dimensions.endurance.hasRating)
    // A prior is never shown as a measured speed or sprint rating.
    #expect(!fitness.dimensions.speed.hasRating)
    #expect(!fitness.dimensions.sprint.hasRating)
    #expect(fitness.zones.zones.count == 6)
    #expect(fitness.zones.zone("Z5")?.schedulable == false)
    #expect(fitness.zones.zone("Z6")?.slowPaceSecondsPerKm == nil)
    #expect(fitness.predictions.map(\.distance) == ["5k", "10k", "half", "marathon"])
    #expect(fitness.predictions.allSatisfy { $0.predictionSupport == "prior_based" })
    #expect(s.sessionRecovery?.presentationValue?.text == "约 18–20 小时")
    #expect(s == (try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONEncoder().encode(s))))
  }
  @Test func runnerTextAvoidsImplementationVocabulary() {
    #expect(RunningFitnessText.pace(300) == "5′00″")
    #expect(RunningFitnessText.raceTime(3725) == "1:02:05")
    #expect(RunningFitnessText.dimensionStatus("lower_bound_only") == "仅已展示下限")
    #expect(RunningFitnessText.predictionSupport("prior_based") == "理论范围")
    #expect(RunningFitnessText.singleReason(["hr_reference_missing"]) == "缺少可用心率参考")
  }

  private func snapshot() throws -> AthleteModelSnapshot {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/RunningFitnessV4.json"))
    return try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
  }

  @Test func abilityPaceUsesTrainingBoundsAndRejectsConflicts() throws {
    let value = try #require(snapshot().runningFitness?.presentationValue)
    var zone = try #require(value.zones.zone("Z2"))
    zone.fastPaceSecondsPerKm = 300
    zone.slowPaceSecondsPerKm = 360
    #expect(RunningFitnessText.zonePace(zone) == "5′00″–6′00″ /km")
    zone.fastPaceSecondsPerKm = nil
    #expect(RunningFitnessText.zonePace(zone) == nil)
    zone.openEnded = true
    #expect(RunningFitnessText.zonePace(zone) == "快于 6′00″ /km")
    zone.reasonCodes = ["zone_anchor_conflict"]
    #expect(RunningFitnessText.zonePace(zone) == nil)
    zone.reasonCodes = []
    zone.openEnded = false
    zone.fastPaceSecondsPerKm = 400
    #expect(RunningFitnessText.zonePace(zone) == nil)
    zone.fastPaceSecondsPerKm = .nan
    #expect(RunningFitnessText.zonePace(zone) == nil)
    zone.support = "future_support"
    #expect(RunningFitnessText.zonePace(zone) == nil)
  }

  @Test func ordinaryFastRunningNeverBecomesSprintRating() throws {
    var d = try #require(snapshot().runningFitness?.presentationValue).dimensions.endurance
    d.basis = "daily_fast_running"
    let sprint = d.presentation(for: "sprint")
    #expect(!sprint.hasRating)
    #expect(sprint.lowerBoundMPS == d.speedMPS)
    #expect(sprint.status == "lower_bound_only")
    d.basis = "sprint_test"
    #expect(d.presentation(for: "sprint").hasRating)
    #expect(d.presentation(for: "endurance") == d)
  }

  // Synthetic lactate threshold v1 payload, as the server publishes it.
  private func threshold(bpm: Double?, basis: String, definition: String? = "rai_t40_working_proxy", update: String = "confirmed",
                         freshness: String = "fresh", reasons: [String] = []) throws -> LactateThresholdValue {
    let bpmJSON = bpm.map { String($0) } ?? "null"
    let def = definition.map { "\"\($0)\"" } ?? "null"
    let json = """
    {"publicationID":"p","schemaVersion":"rai.lactate_threshold/1","algorithmVersion":"v1","date":"2026-11-16","definition":"rai_t40_working_proxy",
     "status":"estimated_proxy","updateStatus":"\(update)",
     "pace":{"speedMPS":3.6,"paceSecondsPerKm":277.8,"basis":"estimated","freshness":"fresh","speedModelRange":{"low":3.5,"high":3.7},
             "trainingSpeedRange":{"low":3.45,"high":3.6},"definitionSensitivitySeconds":[2400,3600],"speed60MPS":3.45},
     "heartRate":{"bpm":\(bpmJSON),"basis":"\(basis)","method":"near_anchor_observed","definition":\(def),"freshness":"\(freshness)",
                  "modelRange":{"low":160,"high":172},"rangePartiallyIdentified":false,"independentDays":2,"effectiveDays":1.9,
                  "supportSeconds":1800,"evidenceDay":"2026-11-15","observedHRR":null,"looMaxDeltaBPM":1,"reasonCodes":\(reasons)},
     "trainingUse":{"paceEligible":true,"heartRateEligible":false,"automaticIntensityIncreaseAllowed":false,"reasonCodes":["hr_training_adapter_not_activated"]},
     "evidence":[],"reasonCodes":["working_threshold_not_lab_measurement"]}
    """
    return try JSONDecoder().decode(LactateThresholdValue.self, from: Data(json.utf8))
  }

  @Test func thresholdHeartRateComesOnlyFromThePublishedModel() throws {
    var s = try snapshot()
    #expect(s.runningFitness?.value?.lactateThreshold == nil)
    #expect(FitnessBasicMetric.thresholdHeartRate(nil).note == "暂未提供")
    let supported = try threshold(bpm: 166.4, basis: "supported")
    s.runningFitness?.value?.lactateThreshold = supported
    let metric = FitnessBasicMetric.metrics(snapshot: s)[2]
    #expect(metric.text == "166" && metric.note == "多次稳定跑支持")
    #expect(FitnessBasicMetric.thresholdHeartRate(try threshold(bpm: 170, basis: "preliminary", definition: "field_tt30_last20")).note == "30 分钟计时跑 · 初步估计")
    #expect(FitnessBasicMetric.thresholdHeartRate(try threshold(bpm: 170, basis: "preliminary", update: "pending_confirmation")).note.contains("等待"))
    #expect(FitnessBasicMetric.thresholdHeartRate(try threshold(bpm: 170, basis: "preliminary", freshness: "expired")).note == "证据已过期")
    let circular = FitnessBasicMetric.thresholdHeartRate(try threshold(bpm: nil, basis: "collecting", reasons: ["anchor_prior_based_hr_circular"]))
    #expect(circular.text == "--" && circular.note.contains("计时跑"))
    #expect(FitnessBasicMetric.thresholdHeartRate(try threshold(bpm: 170, basis: "conflict")).text == "--")
    // An older App's decode of the whole value ignores unknown keys.
    let encoded = try JSONEncoder().encode(supported)
    #expect(try JSONDecoder().decode(LactateThresholdValue.self, from: encoded) == supported)
  }

  @Test func basicMetricsRespectAvailabilityAndNeverInventThresholdHeartRate() throws {
    var s = try snapshot()
    let basic = FitnessBasicMetric.metrics(snapshot: s)
    #expect(basic.map(\.id) == ["vo2max", "thresholdPace", "thresholdHeartRate"])
    #expect(basic[1].text == RunningFitnessText.pace(s.runningFitness?.value?.dimensions.threshold.paceSecondsPerKm))
    #expect(basic[1].note.contains("估计"))
    #expect(basic[2].text == "--")
    s.runningFitness?.envelope.freshness = .init(rawValue: "expired")
    s.externalVO2Max.envelope.freshness = .init(rawValue: "expired")
    s.raiVO2Max?.envelope.freshness = .init(rawValue: "expired")
    #expect(FitnessBasicMetric.metrics(snapshot: s).allSatisfy { $0.text == "--" })
    #expect(FitnessBasicMetric.metrics(snapshot: nil).allSatisfy { $0.text == "--" })
    s.schemaVersion = "future"
    #expect(FitnessBasicMetric.metrics(snapshot: s).allSatisfy { $0.text == "--" })
  }

  @Test func vo2SourceIsExplicitAndStaleExternalValuesDoNotWin() throws {
    var s = try snapshot()
    s.externalVO2Max.envelope.modelVersion = "rai.vo2max.external_projection/v1"
    s.externalVO2Max.envelope.state = .init(rawValue: "available")
    s.externalVO2Max.envelope.freshness = .init(rawValue: "fresh")
    s.externalVO2Max.value = .init(value: 51, unit: .init(rawValue: "mL/kg/min"),
      provider: .init(rawValue: "apple"), externalEstimate: true)
    s.raiVO2Max?.envelope.modelVersion = "rai.vo2max.submax/v1"
    s.raiVO2Max?.envelope.state = .init(rawValue: "limited")
    s.raiVO2Max?.envelope.freshness = .init(rawValue: "fresh")
    s.raiVO2Max?.value = .init(value: 48, unit: .init(rawValue: "mL/kg/min"), experimental: true,
      evidenceMode: .init(rawValue: "submax"), stability: .init(rawValue: "stable"), independentWorkoutCount: 5,
      source: .init(rawValue: "rai"))
    #expect(FitnessBasicMetric.metrics(snapshot: s)[0].text == ProductFormat.decimal(51, digits: 1))
    #expect(FitnessBasicMetric.metrics(snapshot: s)[0].note.contains("Apple"))
    s.externalVO2Max.envelope.freshness = .init(rawValue: "stale")
    #expect(FitnessBasicMetric.metrics(snapshot: s)[0].text == ProductFormat.decimal(48, digits: 1))
    #expect(FitnessBasicMetric.metrics(snapshot: s)[0].note.contains("实验性"))
    s.raiVO2Max?.value?.value = -.infinity
    #expect(FitnessBasicMetric.metrics(snapshot: s)[0].text == "--")
  }
}
