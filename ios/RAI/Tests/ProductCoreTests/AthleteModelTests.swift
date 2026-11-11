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
    var ability = try #require(root["runningAbility"] as? [String: Any])
    ability["value"] = ["futureShape": "opaque preserved"]
    root["runningAbility"] = ability
    let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONSerialization.data(withJSONObject: root))
    #expect(snapshot.runningAbility.presentationValue == nil)
    #expect(snapshot.runningAbility.unsupportedPayload != nil)
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
    #expect(ProductReasonText.runningAbility(modelStatus: "insufficient_long_evidence", reason: "data_stale", state: "collecting")
      == "证据不足：需要至少 2 次 40 分钟以上的合格跑步")
    #expect(ProductReasonText.runningAbility(modelStatus: "stale", reason: "data_stale", state: "stale") == "数据已过期")
  }
}

@Suite struct AthleteModelV3Tests {
  @Test func estimatesRemainExplicitAndDoNotInventEndurance() throws {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/ModelOptimizationV3.json"))
    let s = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
    #expect(s.supportsPresentation)
    #expect(s.physiologicalRecovery.envelope.mode == "estimated")
    #expect(s.physiologicalRecovery.presentationValue?.missingComponents == ["hrv"])
    #expect(s.trainingReadiness.presentationValue?.range?.low == 70)
    #expect(s.sessionRecovery?.presentationValue?.text == "约 18–20 小时")
    #expect(s.loadFocus?.presentationValue?.windows.first?.unknownWorkouts == 1)
    #expect(s.runningAbility.presentationValue?.selectedMode == "submax_estimated")
    #expect(s.runningAbility.presentationValue?.gamma == nil)
    #expect(s.runningAbility.presentationValue?.enduranceRetention == nil)
    #expect(s == (try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONEncoder().encode(s))))
  }
}
