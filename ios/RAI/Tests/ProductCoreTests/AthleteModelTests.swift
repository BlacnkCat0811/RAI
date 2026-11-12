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
    #expect(ProductReasonText.runningFitness(modelStatus: "stale", reason: "data_stale", state: "stale") == "数据已过期")
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
    #expect(RunningFitnessText.singleReason(["hr_reference_missing"]) == "缺少最大心率设置")
  }
}
