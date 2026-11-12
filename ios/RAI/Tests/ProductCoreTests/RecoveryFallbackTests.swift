import Foundation
import Testing
@testable import ProductCore

@Suite struct RecoveryFallbackTests {
  @Test func recoveryV4ShowsEstimateSourcesAndKeepsLegacyDecoding() throws {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel/full.json"))
    var root = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: file)) as? [String: Any])
    var metric = try #require(root["physiologicalRecovery"] as? [String: Any])
    var envelope = try #require(metric["envelope"] as? [String: Any])
    var value = try #require(metric["value"] as? [String: Any])
    envelope["modelVersion"] = "rai.recovery.physiological/v4"
    envelope["payloadVersion"] = "rai.recovery.physiological/4"
    envelope["mode"] = "estimated"
    func component(_ basis: String, count: Int) -> [String: Any] {
      ["state": "historical_mean", "inputBasis": basis, "meanSamples": count,
       "trendNights": count, "baselineNights": 0, "sourceEpoch": "synthetic"]
    }
    value["components"] = [
      "hrv": component("recent_mean", count: 3),
      "rhr": component("user_entered", count: 0),
      "sleep": component("default_baseline", count: 0)
    ]
    metric["envelope"] = envelope
    metric["value"] = value
    root["physiologicalRecovery"] = metric
    let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONSerialization.data(withJSONObject: root))
    #expect(snapshot.physiologicalRecovery.envelope.supportsPresentation)
    let recovery = try #require(snapshot.physiologicalRecovery.presentationValue)
    #expect(recovery.inputSourceDescriptions == [
      "HRV：最近 3 次有效观测均值", "静息心率：用户填写值", "睡眠：默认基准估计"
    ])
    let original = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
    #expect(original.physiologicalRecovery.value?.inputSourceDescriptions.isEmpty == true)
  }
}
