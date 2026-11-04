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
