import ProductCore
import SwiftUI
import XCTest
@testable import RAI

final class RunningFitnessLayoutTests: XCTestCase {
  @MainActor func testSyntheticAbilityCardsAtNormalAndAccessibilitySizes() throws {
    guard let raw = ProcessInfo.processInfo.environment["RAI_FITNESS_SYNTHETIC_SNAPSHOT"] else {
      throw XCTSkip("Supply the synthetic RunningFitnessV4 fixture to render the cards")
    }
    let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(raw.utf8))
    let value = try XCTUnwrap(snapshot.runningFitness?.presentationValue)
    for (name, size) in [("normal", DynamicTypeSize.large), ("large-text", .accessibility3)] {
      let content = VStack(alignment: .leading, spacing: 16) {
        VStack(alignment: .leading, spacing: 16) {
          Text("跑步能力").font(.headline)
          FitnessScoreGauge(score: value.overall.score)
          Text("能力分析").font(.headline)
          ForEach(value.dimensions.ordered, id: \.key) { item in
            FitnessAbilityRow(key: item.key, dimension: item.value, zone: value.zones.zone(item.value.zone))
            if item.key != "sprint" { Divider() }
          }
        }.padding(20).raiGlass()
        FitnessBasicMetricsCard(metrics: FitnessBasicMetric.metrics(snapshot: snapshot))
      }.padding(18).frame(width: 390).background(RAITheme.background)
        .foregroundStyle(RAITheme.primary)
        .environment(\.locale, Locale(identifier: "zh-Hans-CN"))
        .environment(\.dynamicTypeSize, size)
      let renderer = ImageRenderer(content: content)
      let image = try XCTUnwrap(renderer.uiImage)
      XCTAssertEqual(image.size.width, 390)
      XCTAssertGreaterThan(image.size.height, 900)
      let attachment = XCTAttachment(image: image)
      attachment.name = "running-fitness-" + name
      attachment.lifetime = .keepAlways
      add(attachment)
    }
  }

  /// Synthetic training-effect gauges: complete, estimated, overreaching and
  /// unavailable ("—", never 0) at normal and accessibility text sizes.
  @MainActor func testTrainingEffectGaugesRenderAllStates() throws {
    let cases: [(String, TrainingEffectScore, TrainingEffectScore)] = [
      ("complete", TrainingEffectScore(value: 3.7, label: "improving", status: "complete", reasonCodes: []),
       TrainingEffectScore(value: nil, label: "", status: "unavailable", reasonCodes: ["speed_reference_unavailable"])),
      ("intervals", TrainingEffectScore(value: 5.0, label: "overreaching", status: "complete", reasonCodes: []),
       TrainingEffectScore(value: 2.1, label: "maintaining", status: "estimated", reasonCodes: ["short_sprint_uncorroborated"])),
    ]
    for (name, aerobic, anaerobic) in cases {
      for (sizeName, size) in [("normal", DynamicTypeSize.large), ("large-text", .accessibility3)] {
        let content = VStack(alignment: .leading, spacing: 12) {
          CardHeading(title: "训练效果", icon: "flame")
          HStack(spacing: 12) {
            TrainingEffectGauge(title: "有氧", score: aerobic)
            TrainingEffectGauge(title: "无氧", score: anaerobic)
          }
          ForEach(TrainingEffectText.reasons(anaerobic), id: \.self) { Text($0).font(.caption) }
          Text(TrainingEffectText.disclaimer).font(.caption2).fixedSize(horizontal: false, vertical: true)
        }.padding(18).frame(width: 390).background(RAITheme.background)
          .foregroundStyle(RAITheme.primary)
          .environment(\.locale, Locale(identifier: "zh-Hans-CN"))
          .environment(\.dynamicTypeSize, size)
        let renderer = ImageRenderer(content: content)
        renderer.scale = 2
        let image = try XCTUnwrap(renderer.uiImage)
        XCTAssertEqual(image.size.width, 390)
        XCTAssertGreaterThan(image.size.height, 150)
        if let dir = ProcessInfo.processInfo.environment["RAI_LAYOUT_OUTPUT"], let png = image.pngData() {
          try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("training-effect-\(name)-\(sizeName).png"))
        }
        let attachment = XCTAttachment(image: image)
        attachment.name = "training-effect-\(name)-\(sizeName)"
        attachment.lifetime = .keepAlways
        add(attachment)
      }
    }
  }
  @MainActor func testPaceZoneCardsRenderAllStates() throws {
    let statuses = ["available", "threshold_pace_unavailable", "profile_expired", "zone_anchor_conflict", "speed_unavailable", "zone_evidence_insufficient", "timing_unavailable", "input_unavailable"]
    for status in statuses {
      let bands: [[String: Any]] = (1...6).map { i in
        ["zone": "Z\(i)", "label": "synthetic", "support": i == 6 ? "insufficient_evidence" : (i == 4 ? "estimated" : "supported"),
         "fastPaceSecondsPerKm": 420 - i * 25, "slowPaceSecondsPerKm": 445 - i * 25]
      }
      let exposure: [[String: Any]] = (1...6).map { i in
        ["zone": "Z\(i)", "durationSeconds": i == 2 ? 1200 : 0, "distanceMeters": i == 2 ? 3600 : 0, "fraction": i == 2 ? 1 : 0]
      }
      let data: [String: Any] = ["zoneProfileID": "", "stimulusType": "unclassified", "secondaryStimulusTypes": [],
        "heartRateCoverage": 0, "paceCoverage": 0.8, "heartRateExposure": [], "paceExposure": exposure,
        "stimulusScores": [], "explanationCodes": ["pace_zone_evidence_insufficient", "pace_zones_not_fully_supported"],
        "paceZoneStatus": status, "paceZoneProfileDate": "2026-11-14", "paceZones": bands,
        "paceUnknownSeconds": 300, "paceReconstructedSeconds": 1200, "paceZonePolicyVersion": "pace-zones/2"]
      let zones = try JSONDecoder().decode(WorkoutZoneSummary.self, from: JSONSerialization.data(withJSONObject: data))
      XCTAssertFalse(try XCTUnwrap(zones.paceBand("Z6")).canClassify)
      for (name, size) in [("normal", DynamicTypeSize.large), ("accessibility", .accessibility3)] {
        let content = WorkoutPaceZoneCard(zones: zones).padding(16).frame(width: 320)
          .background(RAITheme.background).foregroundStyle(RAITheme.primary)
          .environment(\.locale, Locale(identifier: "zh-Hans-CN")).environment(\.dynamicTypeSize, size)
        let renderer = ImageRenderer(content: content)
        renderer.scale = 2
        let image = try XCTUnwrap(renderer.uiImage)
        XCTAssertEqual(image.size.width, 320)
        XCTAssertGreaterThan(image.size.height, 40)
        if let dir = ProcessInfo.processInfo.environment["RAI_LAYOUT_OUTPUT"], let png = image.pngData() {
          try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("pace-\(status)-\(name).png"))
        }
        let attachment = XCTAttachment(image: image)
        attachment.name = "pace-\(status)-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
      }
    }
  }

}
