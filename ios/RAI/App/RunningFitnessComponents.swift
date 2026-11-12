import ProductCore
import SwiftUI

/// A visual scale only: scores and training bounds come from the publication.
struct FitnessScoreGauge: View {
  let score: Double?

  var body: some View {
    VStack(spacing: 0) {
      GeometryReader { geometry in
        let radius = min(geometry.size.width / 2 - 12, geometry.size.height - 16)
        let center = CGPoint(x: geometry.size.width / 2, y: geometry.size.height - 12)
        let progress = max(0, min(1, (score ?? 0) / 100))
        ZStack(alignment: .bottom) {
          FitnessScoreArc(progress: 1).stroke(RAITheme.primary.opacity(0.08), style: StrokeStyle(lineWidth: 12, lineCap: .round))
          if let score, score.isFinite {
            FitnessScoreArc(progress: progress)
              .stroke(RAITheme.orange.gradient, style: StrokeStyle(lineWidth: 12, lineCap: .round))
            Circle().fill(RAITheme.surface).frame(width: 10, height: 10)
              .overlay { Circle().stroke(RAITheme.orange, lineWidth: 3) }
              .position(x: center.x + radius * cos(.pi * (1 + progress)),
                        y: center.y + radius * sin(.pi * (1 + progress)))
          }
          VStack(spacing: 0) {
            Text(ProductFormat.decimal(score, digits: 1))
              .font(.system(size: 58, weight: .bold, design: .rounded)).monospacedDigit()
              .contentTransition(.numericText())
              .accessibilityIdentifier("runningFitness.score")
            Text("当前跑力").font(.caption).foregroundStyle(RAITheme.secondary)
          }.padding(.bottom, 4)
        }
      }.frame(height: 166)
      HStack {
        Text("0")
        Spacer()
        Text("100")
      }.font(.caption2.monospacedDigit()).foregroundStyle(RAITheme.secondary).padding(.horizontal, 8)
        .accessibilityHidden(true)
    }.frame(maxWidth: 320).frame(maxWidth: .infinity)
      .accessibilityElement(children: .ignore)
      .accessibilityLabel("当前跑力")
      .accessibilityValue(ProductFormat.decimal(score, digits: 1) + " 分，满分 100 分")
      .accessibilityIdentifier("runningFitness.score")
  }
}

private struct FitnessScoreArc: Shape {
  var progress: Double

  func path(in rect: CGRect) -> Path {
    let radius = min(rect.width / 2 - 12, rect.height - 16)
    var path = Path()
    path.addArc(center: CGPoint(x: rect.midX, y: rect.maxY - 12), radius: radius,
                startAngle: .degrees(180), endAngle: .degrees(180 + 180 * progress), clockwise: false)
    return path
  }
}

/// 0-5 training-effect arc. An unavailable score renders "—", never a 0.
struct TrainingEffectGauge: View {
  let title: String
  let score: TrainingEffectScore?

  var body: some View {
    let value = score?.value.flatMap { $0.isFinite ? $0 : nil }
    let over = score.map(TrainingEffectText.isOverreaching) ?? false
    let tint = over ? RAITheme.amber : RAITheme.orange
    VStack(spacing: 4) {
      ZStack(alignment: .bottom) {
        FitnessScoreArc(progress: 1).stroke(RAITheme.primary.opacity(0.08), style: StrokeStyle(lineWidth: 9, lineCap: .round))
        if let value {
          FitnessScoreArc(progress: max(0, min(1, value / 5)))
            .stroke(tint.gradient, style: StrokeStyle(lineWidth: 9, lineCap: .round))
        }
        Text(value.map { ProductFormat.decimal($0, digits: 1) } ?? "—")
          .font(.system(size: 30, weight: .bold, design: .rounded)).monospacedDigit()
          .padding(.bottom, 4)
      }.frame(height: 86)
      HStack(spacing: 4) {
        Text(title).font(.subheadline.weight(.semibold))
        if score?.isEstimated == true {
          Text("估算").font(.caption2).padding(.horizontal, 5).padding(.vertical, 1)
            .background(RAITheme.primary.opacity(0.08), in: Capsule())
        }
      }
      Text(value == nil ? "暂无" : score.map(TrainingEffectText.label) ?? "暂无")
        .font(.caption.weight(.medium)).foregroundStyle(value == nil ? RAITheme.secondary : tint)
    }.frame(maxWidth: .infinity)
      .accessibilityElement(children: .ignore)
      .accessibilityLabel(title + "训练效果")
      .accessibilityValue(value.map { ProductFormat.decimal($0, digits: 1) + "，" + (score.map(TrainingEffectText.label) ?? "") + (score?.isEstimated == true ? "，估算" : "") } ?? "暂无")
  }
}

struct FitnessAbilityRow: View {
  let key: String
  let dimension: FitnessDimension
  let zone: FitnessZone?

  var body: some View {
    let d = dimension.presentation(for: key)
    let zone = key == "sprint" && d.status == "lower_bound_only" ? nil : zone
    let pace = RunningFitnessText.zonePace(zone)
    VStack(alignment: .leading, spacing: 8) {
      HStack(alignment: .firstTextBaseline) {
        Text(RunningFitnessText.dimensionName(key)).font(.subheadline.weight(.semibold))
        Spacer(minLength: 8)
        Text(d.hasRating ? ProductFormat.decimal(d.rating, digits: 1) : "待补充")
          .font(.system(.title3, design: .rounded, weight: .bold)).monospacedDigit()
          .foregroundStyle(d.hasRating ? RAITheme.primary : RAITheme.secondary)
      }
      Text(pace ?? "配速区间待补充").font(.subheadline.monospacedDigit())
        .foregroundStyle(pace == nil ? RAITheme.secondary : RAITheme.orange)
        .fixedSize(horizontal: false, vertical: true)
        .accessibilityIdentifier("runningFitness.pace." + key)
      FitnessAbilityBar(rating: d.hasRating ? d.rating : nil).accessibilityHidden(true)
      if let zone, pace != nil {
        Text(zone.name + " · " + RunningFitnessText.zoneSupport(zone))
          .font(.caption2).foregroundStyle(RAITheme.secondary)
          .fixedSize(horizontal: false, vertical: true)
      } else {
        Text(RunningFitnessText.evidenceHint(key)).font(.caption2).foregroundStyle(RAITheme.secondary)
          .fixedSize(horizontal: false, vertical: true)
      }
      if d.hasRating, let anchor = d.paceSecondsPerKm {
        Text(RunningFitnessText.dimensionAnchor(key) + " " + RunningFitnessText.pace(anchor) + " /km")
          .font(.caption2).foregroundStyle(RAITheme.secondary)
          .fixedSize(horizontal: false, vertical: true)
      } else if let lower = d.lowerBoundMPS {
        Text("日常跑已展示 " + RunningFitnessText.pace(lower.isFinite && lower > 0 ? 1000 / lower : nil) + " /km，尚非冲刺测试")
          .font(.caption2).foregroundStyle(RAITheme.secondary)
          .fixedSize(horizontal: false, vertical: true)
      }
      if key == "endurance", let retention = d.retention {
        Text("长跑效率保持 \(ProductFormat.decimal(retention * 100, digits: 1))%")
          .font(.caption2).foregroundStyle(RAITheme.secondary)
      }
    }.accessibilityElement(children: .combine)
      .accessibilityIdentifier("runningFitness.dimension." + key)
  }
}

private struct FitnessAbilityBar: View {
  let rating: Double?

  var body: some View {
    GeometryReader { geometry in
      ZStack(alignment: .leading) {
        Capsule().fill(RAITheme.primary.opacity(0.08))
        if let rating, rating.isFinite {
          Capsule().fill(RAITheme.orange.gradient)
            .frame(width: geometry.size.width * max(0, min(1, rating / 100)))
        }
        HStack(spacing: 0) {
          ForEach(0..<5) { _ in
            Spacer(minLength: 0)
            Rectangle().fill(RAITheme.surface).frame(width: 3)
          }
        }.padding(.trailing, geometry.size.width / 6)
      }
    }.frame(height: 7)
  }
}

struct FitnessBasicMetricsCard: View {
  let metrics: [FitnessBasicMetric]
  @Environment(\.dynamicTypeSize) private var dynamicTypeSize

  var body: some View {
    VStack(alignment: .leading, spacing: 20) {
      CardHeading(title: "基础指标", icon: "waveform.path.ecg")
      if dynamicTypeSize.isAccessibilitySize {
        ForEach(metrics) { metric in
          metricView(metric)
          if metric.id != metrics.last?.id { Divider() }
        }
      } else {
        HStack(alignment: .top, spacing: 12) {
          ForEach(metrics) { metric in
            metricView(metric).frame(maxWidth: .infinity, alignment: .leading)
          }
        }
      }
    }.padding(20).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("runningFitness.basics")
  }

  private func metricView(_ metric: FitnessBasicMetric) -> some View {
    VStack(alignment: .leading, spacing: 5) {
      Text(metric.text).font(.system(.title2, design: .rounded, weight: .bold)).monospacedDigit()
        .lineLimit(1).minimumScaleFactor(0.75)
      Text(metric.unit).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(metric.title).font(.caption.weight(.medium)).fixedSize(horizontal: false, vertical: true)
      Text(metric.note).font(.caption2).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
    }.accessibilityElement(children: .combine)
      .accessibilityIdentifier("runningFitness.basic." + metric.id)
  }
}

#Preview("基础指标 · 数据不足") {
  FitnessBasicMetricsCard(metrics: FitnessBasicMetric.metrics(snapshot: nil)).padding().raiScreen()
}

#Preview("跑力刻度") {
  VStack(spacing: 24) {
    FitnessScoreGauge(score: 61.9)
    FitnessScoreGauge(score: nil)
  }.padding().raiGlass().padding().raiScreen()
}
