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
}
