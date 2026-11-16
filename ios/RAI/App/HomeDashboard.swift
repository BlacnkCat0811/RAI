import Charts
import ProductCore
import SwiftUI
import SyncCore

// Chart-first home cards. Every number comes from a value the server already
// published (athlete envelope `presentationValue`, overview days, workouts);
// a missing value draws an empty gauge, never a guessed one.

enum HomePalette {
  static let red = Color(red: 0.74, green: 0.25, blue: 0.20)
  static let amber = Color(red: 0.90, green: 0.58, blue: 0.16)
  static let green = Color(red: 0.24, green: 0.62, blue: 0.36)
  static let blue = Color(red: 0.20, green: 0.50, blue: 0.80)
  static let purple = Color(red: 0.52, green: 0.36, blue: 0.82)
  static let track = RAITheme.primary.opacity(0.08)
  /// 0–100 score bands shared by readiness and recovery gauges.
  static let scoreBands: [(Double, Color)] = [(0.33, red), (0.66, amber), (1.0, green)]
  static func scoreColor(_ score: Double?) -> Color {
    guard let score, score.isFinite else { return RAITheme.secondary }
    return score < 33 ? red : score < 66 ? amber : green
  }
}

// MARK: - gauges

/// Segmented arc gauge: a dim track per band, the filled portion in the band's
/// colour and a marker at the value. `sweep` 270 is a ring, 180 a half dial.
struct ArcGauge<Center: View>: View {
  let progress: Double?
  var bands: [(Double, Color)] = HomePalette.scoreBands
  var sweep: Double = 270
  var lineWidth: CGFloat = 9
  @ViewBuilder var center: () -> Center
  @State private var shown = 0.0
  @Environment(\.accessibilityReduceMotion) private var reduced

  var body: some View {
    GeometryReader { geo in
      let side = min(geo.size.width, sweep > 180 ? geo.size.height : geo.size.height * 2)
      let radius = side / 2 - lineWidth / 2
      let start = sweep > 180 ? 90 + (360 - sweep) / 2 : 180
      let mid = CGPoint(x: geo.size.width / 2, y: sweep > 180 ? geo.size.height / 2 : geo.size.height - lineWidth / 2)
      ZStack {
        ForEach(Array(bands.enumerated()), id: \.offset) { index, band in
          let low = index == 0 ? 0 : bands[index - 1].0
          let gap = index == 0 ? 0 : 0.012
          segment(low + gap, band.0).stroke(band.1.opacity(0.18), style: stroke)
          let filled = min(band.0, shown)
          if filled > low + gap {
            segment(low + gap, filled).stroke(band.1, style: stroke)
          }
        }
        if progress != nil {
          let angle = Angle.degrees(start + shown * sweep).radians
          Circle().fill(RAITheme.surface).frame(width: lineWidth + 5, height: lineWidth + 5)
            .overlay { Circle().stroke(color(at: shown), lineWidth: 3) }
            .position(x: mid.x + radius * cos(angle), y: mid.y + radius * sin(angle))
        }
        center().position(x: mid.x, y: sweep > 180 ? mid.y : mid.y - radius * 0.32)
      }.frame(width: geo.size.width, height: geo.size.height)
    }
    .onAppear { animate() }
    .onChange(of: progress) { _, _ in animate() }
    .accessibilityHidden(true)
  }

  private var stroke: StrokeStyle { StrokeStyle(lineWidth: lineWidth, lineCap: .round) }

  private func animate() {
    let target = max(0, min(1, progress ?? 0))
    withAnimation(reduced ? nil : .easeOut(duration: 0.7)) { shown = target }
  }

  private func color(at value: Double) -> Color {
    bands.first { value <= $0.0 }?.1 ?? bands.last?.1 ?? RAITheme.orange
  }

  private func segment(_ from: Double, _ to: Double) -> some Shape {
    GaugeArc(from: from, to: to, sweep: sweep, inset: lineWidth / 2)
  }
}

/// Arc between two fractions of the gauge sweep, inset by half the stroke.
struct GaugeArc: Shape {
  var from: Double, to: Double, sweep: Double, inset: CGFloat
  var animatableData: AnimatablePair<Double, Double> {
    get { AnimatablePair(from, to) }
    set { from = newValue.first; to = newValue.second }
  }
  func path(in rect: CGRect) -> Path {
    let ring = sweep > 180
    let side = min(rect.width, ring ? rect.height : rect.height * 2)
    let radius = side / 2 - inset
    let center = CGPoint(x: rect.midX, y: ring ? rect.midY : rect.maxY - inset)
    let start = ring ? 90 + (360 - sweep) / 2 : 180
    var path = Path()
    path.addArc(
      center: center, radius: radius, startAngle: .degrees(start + from * sweep),
      endAngle: .degrees(start + to * sweep), clockwise: false)
    return path
  }
}

/// Small square tile used in the two-column metric grid.
struct DashboardTile<Content: View>: View {
  let title: String, icon: String
  var tint: Color = RAITheme.orange
  @ViewBuilder var content: () -> Content
  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      HStack(spacing: 6) {
        Image(systemName: icon).font(.caption.weight(.bold)).foregroundStyle(tint)
        Text(title).font(.subheadline.weight(.semibold)).lineLimit(1).minimumScaleFactor(0.8)
      }
      content()
    }.padding(14).frame(maxWidth: .infinity, minHeight: 196, alignment: .topLeading)
      .raiGlass(radius: 22)
  }
}

private func scoreCenter(_ score: Double?, caption: String) -> some View {
  VStack(spacing: 0) {
    Text(ProductFormat.decimal(score)).font(.system(size: 30, weight: .bold, design: .rounded))
      .monospacedDigit().contentTransition(.numericText())
    Text(caption).font(.caption2).foregroundStyle(RAITheme.secondary)
  }
}

// MARK: - metric grid

/// Two-column grid of gauges, the visual summary of the athlete model.
struct HomeMetricGrid: View {
  let model: ProductModel
  let snapshot: AthleteModelSnapshot
  @Environment(\.dynamicTypeSize) private var typeSize

  var body: some View {
    let columns = typeSize.isAccessibilitySize
      ? [GridItem(.flexible())] : [GridItem(.flexible(), spacing: 12), GridItem(.flexible(), spacing: 12)]
    // Each tile opens its own metric page, not a shared overview.
    LazyVGrid(columns: columns, spacing: 12) {
      link("home.readiness") { TrainingReadinessDetailView(model: model) } label: { readiness }
      link("home.recovery") { PhysiologicalRecoveryDetailView(model: model) } label: { recovery }
      link("home.fitness") { RunningFitnessDetailView(model: model) } label: { fitness }
      link("home.sessionRecovery") { SessionRecoveryDetailView(model: model) } label: { sessionRecovery }
    }
  }

  private func link<D: View, L: View>(
    _ id: String, @ViewBuilder destination: @escaping () -> D, @ViewBuilder label: () -> L
  ) -> some View {
    NavigationLink(destination: destination, label: label).buttonStyle(RAIPressStyle()).accessibilityIdentifier(id)
  }

  private var readiness: some View {
    let metric = snapshot.trainingReadiness
    let score = metric.presentationValue?.score
    return DashboardTile(title: "训练准备度", icon: "bolt.heart.fill", tint: HomePalette.scoreColor(score)) {
      ArcGauge(progress: score.map { $0 / 100 }) { scoreCenter(score, caption: "分") }
        .frame(height: 104).frame(maxWidth: .infinity)
      footer(score.map(Self.readinessLabel) ?? AthleteModelCard.reason(metric.envelope), color: HomePalette.scoreColor(score))
    }.accessibilityElement(children: .ignore).accessibilityLabel("训练准备度")
      .accessibilityValue(score.map { ProductFormat.decimal($0) + " 分，" + Self.readinessLabel($0) } ?? AthleteModelCard.reason(metric.envelope))
  }

  /// Status panel rather than a second ring: the score on a level bar, then
  /// one row per input relative to the personal baseline.
  private var recovery: some View {
    let metric = snapshot.physiologicalRecovery
    let value = metric.presentationValue
    let score = value?.score
    let color = HomePalette.scoreColor(score)
    return DashboardTile(title: "生理恢复", icon: "heart.fill", tint: color) {
      HStack(alignment: .firstTextBaseline, spacing: 3) {
        Text(ProductFormat.decimal(score)).font(.system(size: 30, weight: .bold, design: .rounded)).monospacedDigit()
        Text("分").font(.caption).foregroundStyle(RAITheme.secondary)
        Spacer(minLength: 4)
        if let level = ProductReasonText.recoveryLevel(value?.deviationLevel) {
          Text(level).font(.caption2.weight(.semibold))
            .foregroundStyle(["normal": HomePalette.green, "mild_deviation": HomePalette.amber, "marked_deviation": HomePalette.red][value?.deviationLevel ?? ""] ?? RAITheme.secondary)
            .lineLimit(1).minimumScaleFactor(0.7)
        }
      }
      ScoreLevelBar(score: score).frame(height: 14)
      if let value {
        VStack(spacing: 7) {
          componentRow("HRV", value.hrv)
          componentRow("静息心率", value.rhr)
          componentRow("睡眠", value.sleep)
        }.padding(.top, 4)
      } else {
        footer(AthleteModelCard.reason(metric.envelope), color: RAITheme.secondary)
      }
    }.accessibilityElement(children: .ignore).accessibilityLabel("生理恢复")
      .accessibilityValue(score.map { ProductFormat.decimal($0) + " 分" } ?? AthleteModelCard.reason(metric.envelope))
  }

  private var fitness: some View {
    let metric = snapshot.runningFitness
    let value = metric?.presentationValue
    let score = value?.overall.score
    let bands: [(Double, Color)] = [(0.4, HomePalette.amber.opacity(0.75)), (0.7, HomePalette.amber), (1.0, RAITheme.orange)]
    return DashboardTile(title: "跑力", icon: "figure.run", tint: RAITheme.orange) {
      ArcGauge(progress: score.map { $0 / 100 }, bands: bands, sweep: 180, lineWidth: 10) {
        Text(ProductFormat.decimal(score, digits: 1)).font(.system(size: 28, weight: .bold, design: .rounded))
          .monospacedDigit()
      }.frame(height: 78).frame(maxWidth: .infinity).padding(.top, 14)
      HStack {
        Text("0"); Spacer(); Text("100")
      }.font(.caption2.monospacedDigit()).foregroundStyle(RAITheme.secondary).padding(.horizontal, 4)
      footer(
        value.map { RunningFitnessText.stage($0.status) }
          ?? metric.map { AthleteModelCard.reason($0.envelope) } ?? "正在计算",
        color: RAITheme.secondary)
    }.accessibilityElement(children: .ignore).accessibilityLabel("跑力")
      .accessibilityValue(score.map { ProductFormat.decimal($0, digits: 1) + " 分，满分 100" } ?? "暂无")
  }

  private var sessionRecovery: some View {
    let value = snapshot.sessionRecovery?.presentationValue
    let horizon = 72.0
    let ready = value.map { !$0.overflow && ($0.range?.high ?? $0.remainingHours ?? 1) <= 0 } ?? false
    return DashboardTile(title: "高强度恢复", icon: "timer", tint: HomePalette.blue) {
      Spacer(minLength: 0)
      if let value {
        HStack(alignment: .firstTextBaseline, spacing: 3) {
          Text(ready ? "已就绪" : value.overflow ? ">168" : hoursText(value))
            .font(.system(size: ready ? 26 : 30, weight: .bold, design: .rounded)).monospacedDigit()
            .minimumScaleFactor(0.6).lineLimit(1)
          if !ready { Text("小时").font(.caption).foregroundStyle(RAITheme.secondary) }
        }
        HoursRangeBar(range: value.range, remaining: value.remainingHours, horizon: horizon, overflow: value.overflow)
          .frame(height: 22)
        HStack {
          Text("现在"); Spacer(); Text("\(Int(horizon)) h")
        }.font(.caption2.monospacedDigit()).foregroundStyle(RAITheme.secondary)
        footer("距下一次高强度训练", color: RAITheme.secondary)
      } else {
        Text("--").font(.system(size: 30, weight: .bold, design: .rounded))
        footer(snapshot.sessionRecovery.map { AthleteModelCard.reason($0.envelope) } ?? "同步跑步后估计", color: RAITheme.secondary)
      }
      Spacer(minLength: 0)
    }.accessibilityElement(children: .ignore).accessibilityLabel("距下一次高强度训练")
      .accessibilityValue(value?.text ?? "暂无")
  }

  private func hoursText(_ value: SessionRecoveryValue) -> String {
    guard let range = value.range else { return ProductFormat.decimal(value.remainingHours) }
    let low = Int(range.low.rounded(.down)), high = Int(range.high.rounded(.up))
    return low == high ? "\(high)" : "\(low)–\(high)"
  }

  static func readinessLabel(_ score: Double) -> String {
    score >= 66 ? "状态充沛" : score >= 50 ? "可正常训练" : "恢复偏低"
  }

  private func footer(_ text: String, color: Color) -> some View {
    Text(text).font(.caption).foregroundStyle(color).lineLimit(2).frame(maxWidth: .infinity, alignment: .center)
      .multilineTextAlignment(.center)
  }

  private func componentRow(_ name: String, _ state: RecoveryComponentState) -> some View {
    let muted = RAITheme.secondary.opacity(0.4)
    let (color, text): (Color, String) = {
      switch state.relativeToBaseline.rawValue {
      case "no_adverse_deviation": return (HomePalette.green, "正常")
      case "unknown", "": return (muted, state.availability.rawValue.hasPrefix("missing") ? "缺失" : "未知")
      default: return (HomePalette.amber, "偏离")
      }
    }()
    let textColor = text == "正常" || text == "偏离" ? color : RAITheme.secondary
    return HStack(spacing: 6) {
      RoundedRectangle(cornerRadius: 2).fill(color).frame(width: 4, height: 14)
      Text(name).font(.caption).foregroundStyle(RAITheme.secondary)
      Spacer(minLength: 4)
      Text(text).font(.caption.weight(.semibold)).foregroundStyle(textColor)
    }
  }
}

/// 0–100 score on the three-band track with a marker, the linear sibling of
/// the readiness ring.
struct ScoreLevelBar: View {
  let score: Double?
  var body: some View {
    GeometryReader { geo in
      let w = geo.size.width
      ZStack(alignment: .leading) {
        HStack(spacing: 2) {
          ForEach(Array(HomePalette.scoreBands.enumerated()), id: \.offset) { i, band in
            let low = i == 0 ? 0 : HomePalette.scoreBands[i - 1].0
            Capsule().fill(band.1.opacity(0.8)).frame(width: max(0, (band.0 - low) * w - 2))
          }
        }.frame(height: 6)
        if let score, score.isFinite {
          Circle().fill(RAITheme.surface).overlay(Circle().stroke(HomePalette.scoreColor(score), lineWidth: 3))
            .frame(width: 13, height: 13).offset(x: CGFloat(max(0, min(100, score)) / 100) * w - 6.5)
        }
      }.frame(maxHeight: .infinity)
    }.accessibilityHidden(true)
  }
}

/// Hours until the next hard session on a fixed horizon: the model range as a
/// band, the central estimate as a tick.
struct HoursRangeBar: View {
  let range: ModelRange?
  let remaining: Double?
  let horizon: Double
  let overflow: Bool
  var body: some View {
    GeometryReader { geo in
      let w = geo.size.width
      let x = { (h: Double) in CGFloat(max(0, min(horizon, h)) / horizon) * w }
      ZStack(alignment: .leading) {
        Capsule().fill(HomePalette.track).frame(height: 8)
        if overflow {
          Capsule().fill(HomePalette.blue.opacity(0.5)).frame(height: 8)
        } else if let range {
          Capsule().fill(HomePalette.blue.opacity(0.35))
            .frame(width: max(8, x(range.high) - x(range.low)), height: 8).offset(x: x(range.low))
        }
        if let remaining, !overflow {
          Capsule().fill(HomePalette.blue).frame(width: 4, height: 18).offset(x: max(0, x(remaining) - 2))
        }
      }.frame(maxHeight: .infinity)
    }
  }
}

// MARK: - training load

/// Seven-day load with the acute/chronic balance and the 30-day trend.
struct HomeLoadCard: View {
  let overview: JSON
  let load: TrainingLoadMetric
  let points: [TrainingLoadPoint]

  private var days: [JSON] { HomeDates.rollingWeek(overview) }
  private var weekTotal: Double? {
    let values = days.compactMap { $0.number("load") }
    return values.isEmpty ? nil : values.reduce(0, +)
  }

  var body: some View {
    let value = load.presentationValue
    VStack(alignment: .leading, spacing: 14) {
      HStack {
        CardHeading(title: "训练负荷", icon: "chart.bar.fill")
        Spacer()
        Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(RAITheme.secondary)
      }
      HStack(alignment: .bottom, spacing: 16) {
        VStack(alignment: .leading, spacing: 2) {
          Text(ProductFormat.decimal(weekTotal)).font(.system(size: 40, weight: .bold, design: .rounded))
            .monospacedDigit()
          Text("近 7 天负荷").font(.caption).foregroundStyle(RAITheme.secondary)
        }.frame(minWidth: 92, alignment: .leading)
        weekBars.frame(height: 72)
      }
      if let value, let acute = value.acute, let chronic = value.chronic {
        Divider()
        HStack(spacing: 0) {
          stat("短期", ProductFormat.decimal(acute), HomePalette.purple)
          Divider().frame(height: 34)
          stat("长期", ProductFormat.decimal(chronic), RAITheme.orange)
          Divider().frame(height: 34)
          stat("负荷比", chronic > 0 ? ProductFormat.decimal(acute / chronic * 100) + "%" : "--", ratioColor(acute, chronic))
        }
        if chronic > 0 { LoadRatioBar(ratio: acute / chronic) }
      }
      if points.filter({ $0.fitness != nil || $0.fatigue != nil }).count >= 2 { trend }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }

  private var weekBars: some View {
    HomeWeekBars(days: days, key: "load", color: RAITheme.orange).accessibilityLabel("近 7 天每日负荷")
  }

  private var trend: some View {
    VStack(alignment: .leading, spacing: 6) {
      HStack(spacing: 12) {
        Text("近 30 天").font(.caption.weight(.semibold))
        Spacer()
        legend("长期", RAITheme.orange)
        legend("短期", HomePalette.purple)
      }
      Chart(points) { point in
        if let v = point.fitness {
          AreaMark(x: .value("日期", point.day), y: .value("长期", v))
            .foregroundStyle(LinearGradient(colors: [RAITheme.orange.opacity(0.25), RAITheme.orange.opacity(0.02)], startPoint: .top, endPoint: .bottom))
            .interpolationMethod(.catmullRom)
          LineMark(x: .value("日期", point.day), y: .value("长期", v), series: .value("系列", "长期"))
            .foregroundStyle(RAITheme.orange).interpolationMethod(.catmullRom)
        }
        if let v = point.fatigue {
          LineMark(x: .value("日期", point.day), y: .value("短期", v), series: .value("系列", "短期"))
            .foregroundStyle(HomePalette.purple).lineStyle(StrokeStyle(lineWidth: 1.6, dash: [4, 3]))
            .interpolationMethod(.catmullRom)
        }
      }.chartXAxis(.hidden).chartYAxis(.hidden).frame(height: 70)
        .accessibilityLabel("近 30 天长期与短期负荷趋势")
    }
  }

  private func stat(_ title: String, _ value: String, _ color: Color) -> some View {
    VStack(spacing: 2) {
      Text(value).font(.system(.title3, design: .rounded, weight: .bold)).monospacedDigit().foregroundStyle(color)
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
    }.frame(maxWidth: .infinity)
  }

  private func legend(_ title: String, _ color: Color) -> some View {
    HStack(spacing: 4) {
      Capsule().fill(color).frame(width: 12, height: 3)
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
    }
  }

  private func ratioColor(_ acute: Double, _ chronic: Double) -> Color {
    guard chronic > 0 else { return RAITheme.secondary }
    return LoadRatioBar.zone(acute / chronic).color
  }

}

/// Acute ÷ chronic on a zoned track (low / optimal / high / very high).
struct LoadRatioBar: View {
  let ratio: Double
  static let lower = 0.4, upper = 1.8
  static let zones: [(Double, Color, String)] = [
    (0.8, HomePalette.blue, "偏低"), (1.3, HomePalette.green, "适宜"), (1.5, HomePalette.amber, "偏高"), (upper, HomePalette.red, "过高"),
  ]
  static func zone(_ ratio: Double) -> (color: Color, label: String) {
    let z = zones.first { ratio < $0.0 } ?? zones[zones.count - 1]
    return (z.1, z.2)
  }
  var body: some View {
    VStack(alignment: .leading, spacing: 4) {
      GeometryReader { geo in
        let w = geo.size.width
        let x = { (r: Double) in CGFloat((max(Self.lower, min(Self.upper, r)) - Self.lower) / (Self.upper - Self.lower)) * w }
        ZStack(alignment: .leading) {
          HStack(spacing: 2) {
            ForEach(Array(Self.zones.enumerated()), id: \.offset) { i, z in
              let low = i == 0 ? Self.lower : Self.zones[i - 1].0
              Capsule().fill(z.1.opacity(0.75)).frame(width: max(0, x(z.0) - x(low) - 2))
            }
          }.frame(height: 6)
          Circle().fill(RAITheme.surface).overlay(Circle().stroke(RAITheme.primary, lineWidth: 2.5))
            .frame(width: 13, height: 13).offset(x: x(ratio) - 6.5)
        }.frame(height: 14)
      }.frame(height: 14)
      HStack {
        ForEach(Array(Self.zones.enumerated()), id: \.offset) { _, z in
          Text(z.2).font(.caption2).foregroundStyle(Self.zone(ratio).label == z.2 ? z.1 : RAITheme.secondary)
            .fontWeight(Self.zone(ratio).label == z.2 ? .bold : .regular)
            .frame(maxWidth: .infinity)
        }
      }
    }.accessibilityElement(children: .ignore).accessibilityLabel("负荷比")
      .accessibilityValue(ProductFormat.decimal(ratio * 100) + "%，" + Self.zone(ratio).label)
  }
}

// MARK: - load focus

/// Composition of recent load as horizontal bars, one per intensity class.
struct HomeLoadFocusCard: View {
  let window: LoadFocusWindow
  var body: some View {
    let b = window.buckets
    let rows: [(String, Double, Color)] = [
      ("低强度有氧", b.lowAerobicAU, HomePalette.blue), ("高强度有氧", b.highAerobicAU, HomePalette.amber),
      ("无氧", b.anaerobicProxyAU, HomePalette.purple),
    ] + (b.unclassifiedAU > 0 ? [("未分类", b.unclassifiedAU, RAITheme.secondary.opacity(0.4))] : [])
    let top = max(rows.map(\.1).max() ?? 0, 1)
    let total = rows.reduce(0) { $0 + $1.1 }
    VStack(alignment: .leading, spacing: 12) {
      HStack {
        CardHeading(title: "负荷构成", icon: "circle.hexagongrid.fill")
        Spacer()
        Text("近 \(window.windowDays) 天").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if let lead = rows.max(by: { $0.1 < $1.1 }), lead.1 > 0, lead.0 != "未分类" {
        Text(lead.0 + "为主").font(.system(.title3, design: .rounded, weight: .bold))
      }
      ForEach(rows, id: \.0) { row in
        HStack(spacing: 10) {
          Text(row.0).font(.caption).foregroundStyle(RAITheme.secondary).frame(width: 72, alignment: .leading)
          GeometryReader { geo in
            ZStack(alignment: .leading) {
              Capsule().fill(HomePalette.track)
              Capsule().fill(row.2.gradient).frame(width: max(row.1 > 0 ? 6 : 0, geo.size.width * row.1 / top))
            }
          }.frame(height: 12)
          Text(ProductFormat.decimal(row.1)).font(.caption.weight(.semibold)).monospacedDigit()
            .frame(width: 44, alignment: .trailing)
        }.accessibilityElement(children: .combine)
          .accessibilityValue(total > 0 ? "\(Int((row.1 / total * 100).rounded()))%" : "")
      }
      if window.unknownWorkouts > 0 {
        Text("\(window.unknownWorkouts) 次跑步负荷未知，未计入").font(.caption2).foregroundStyle(RAITheme.secondary)
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }
}

/// Seven thin capsule bars on a track, labelled by the weekday of each date.
struct HomeWeekBars: View {
  let days: [JSON]
  let key: String
  let color: Color
  var height: CGFloat = 52
  @State private var appeared = false
  @Environment(\.accessibilityReduceMotion) private var reduced
  var body: some View {
    let top = max(days.compactMap { $0.number(key) }.max() ?? 0, 1)
    HStack(alignment: .bottom, spacing: 6) {
      ForEach(Array(days.enumerated()), id: \.offset) { _, day in
        let v = day.number(key) ?? 0
        VStack(spacing: 4) {
          ZStack(alignment: .bottom) {
            Capsule().fill(HomePalette.track)
            if v > 0 {
              Capsule().fill(color.gradient).frame(height: appeared ? max(6, height * v / top) : 6)
            }
          }.frame(width: 8, height: height)
          Text(HomeDates.weekday(day.string("date"))).font(.caption2).foregroundStyle(RAITheme.secondary)
        }.frame(maxWidth: .infinity)
      }
    }.onAppear { if !appeared { withAnimation(reduced ? nil : .easeOut(duration: 0.5)) { appeared = true } } }
  }
}

// MARK: - recent workout with route

struct HomeRecentWorkoutCard: View {
  let workout: ProductWorkout
  let route: [ProductCore.RoutePoint]?
  var body: some View {
    let distance = workout.raw.number("distanceM")
    let duration = workout.raw.number("durationS")
    let pace = workout.metric("average_pace")
    HStack(alignment: .center, spacing: 14) {
      VStack(alignment: .leading, spacing: 6) {
        HStack(spacing: 6) {
          Image(systemName: "figure.run").font(.caption.weight(.bold)).foregroundStyle(.white)
            .frame(width: 22, height: 22).background(HomePalette.amber, in: Circle())
          Text(workout.title).font(.subheadline.weight(.semibold))
          Text(workout.date, format: .dateTime.month().day().hour().minute()).font(.caption2)
            .foregroundStyle(RAITheme.secondary)
        }
        HStack(alignment: .firstTextBaseline, spacing: 3) {
          Text(ProductFormat.distance(distance)).font(.system(size: 34, weight: .bold, design: .rounded)).monospacedDigit()
          Text("公里").font(.caption).foregroundStyle(RAITheme.secondary)
        }
        HStack(spacing: 12) {
          Label(ProductFormat.duration(duration), systemImage: "clock").labelStyle(.titleAndIcon)
          if let pace, (distance ?? 0) > 0 {
            Label(ProductFormat.pace(pace) + "/公里", systemImage: "speedometer")
          }
        }.font(.caption.monospacedDigit()).foregroundStyle(RAITheme.secondary)
      }
      Spacer(minLength: 0)
      RouteThumbnail(points: route).frame(width: 112, height: 96)
    }.foregroundStyle(RAITheme.primary)
  }
}

/// Route drawn as a plain polyline on a quiet tile; start and finish marked.
struct RouteThumbnail: View {
  let points: [ProductCore.RoutePoint]?
  var body: some View {
    ZStack {
      RoundedRectangle(cornerRadius: 16, style: .continuous).fill(RAITheme.surfaceMuted.opacity(0.7))
      if let points, points.count > 1 {
        GeometryReader { geo in
          let rect = geo.frame(in: .local).insetBy(dx: 12, dy: 12)
          let mapped = HomeRouteShape.project(points, into: rect)
          HomeRouteShape(points: points, inset: 12)
            .stroke(RAITheme.primary.opacity(0.75), style: StrokeStyle(lineWidth: 2.4, lineCap: .round, lineJoin: .round))
          if let first = mapped.first, let last = mapped.last {
            Circle().fill(HomePalette.green).overlay(Circle().stroke(.white, lineWidth: 1.5))
              .frame(width: 9, height: 9).position(first)
            Circle().fill(HomePalette.red).overlay(Circle().stroke(.white, lineWidth: 1.5))
              .frame(width: 9, height: 9).position(last)
          }
        }
      } else if points == nil {
        ProgressView().controlSize(.small)
      } else {
        Image(systemName: "map").font(.title2).foregroundStyle(RAITheme.secondary.opacity(0.5))
      }
    }.accessibilityLabel(points?.isEmpty == false ? "跑步轨迹" : "无轨迹")
  }
}

struct HomeRouteShape: Shape {
  let points: [ProductCore.RoutePoint]
  var inset: CGFloat = 0
  func path(in rect: CGRect) -> Path {
    let mapped = Self.project(points, into: rect.insetBy(dx: inset, dy: inset))
    var path = Path()
    guard let first = mapped.first else { return path }
    path.move(to: first)
    for p in mapped.dropFirst() { path.addLine(to: p) }
    return path
  }

  /// Equirectangular projection scaled to fit `rect` with the aspect kept.
  static func project(_ points: [ProductCore.RoutePoint], into rect: CGRect) -> [CGPoint] {
    guard points.count > 1 else { return [] }
    let lat0 = points.map(\.latitude).reduce(0, +) / Double(points.count)
    let k = cos(lat0 * .pi / 180)
    let xs = points.map { $0.longitude * k }, ys = points.map(\.latitude)
    guard let minX = xs.min(), let maxX = xs.max(), let minY = ys.min(), let maxY = ys.max() else { return [] }
    let spanX = max(maxX - minX, 1e-9), spanY = max(maxY - minY, 1e-9)
    let scale = min(Double(rect.width) / spanX, Double(rect.height) / spanY)
    let offX = (Double(rect.width) - spanX * scale) / 2, offY = (Double(rect.height) - spanY * scale) / 2
    return zip(xs, ys).map { x, y in
      CGPoint(x: Double(rect.minX) + offX + (x - minX) * scale, y: Double(rect.minY) + offY + (maxY - y) * scale)
    }
  }

  /// Keeps at most `limit` points (evenly strided, last point kept) so the
  /// thumbnail stays cheap for long runs.
  static func downsample(_ points: [ProductCore.RoutePoint], limit: Int = 240) -> [ProductCore.RoutePoint] {
    let valid = points.filter { $0.latitude.isFinite && $0.longitude.isFinite && abs($0.latitude) <= 90 && abs($0.longitude) <= 180 }
    guard valid.count > limit else { return valid }
    let step = Double(valid.count - 1) / Double(limit - 1)
    return (0..<limit).map { valid[Int((Double($0) * step).rounded())] }
  }
}

// MARK: - today

struct TodayTrainingStrip: View {
  let session: ProductSession
  var body: some View {
    HStack(spacing: 14) {
      Image(systemName: session.type == "REST" ? "bed.double.fill" : "figure.run")
        .font(.title3.weight(.semibold)).foregroundStyle(RAITheme.primary)
        .frame(width: 48, height: 48).background(RAITheme.lemon, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
      VStack(alignment: .leading, spacing: 3) {
        Text("今日训练").font(.caption).foregroundStyle(RAITheme.secondary)
        Text(session.title).font(.headline).lineLimit(1)
        Text(prescription(session)).font(.caption).monospacedDigit().foregroundStyle(RAITheme.secondary).lineLimit(1)
      }
      Spacer(minLength: 0)
      if session.runnable {
        Image(systemName: "play.fill").font(.subheadline).foregroundStyle(RAITheme.primary)
          .frame(width: 38, height: 38).background(RAITheme.lemon, in: Circle())
      } else {
        Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(RAITheme.secondary)
      }
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass(radius: 22)
      .foregroundStyle(RAITheme.primary)
  }
}

enum HomeDates {
  /// The seven calendar days ending today. The overview spans last Monday
  /// through this Sunday, so a plain `suffix(7)` would be the calendar week and
  /// drop yesterday's runs every Monday.
  static func rollingWeek(_ overview: JSON) -> [JSON] {
    let today = overview.string("today")
    guard !today.isEmpty else { return Array(overview["days"].list.suffix(7)) }
    return Array(overview["days"].list.filter { $0.string("date") <= today }.suffix(7))
  }
  static let parser: DateFormatter = {
    let f = DateFormatter()
    f.calendar = Calendar(identifier: .gregorian)
    f.locale = Locale(identifier: "en_US_POSIX")
    f.timeZone = TimeZone(identifier: "UTC")
    f.dateFormat = "yyyy-MM-dd"
    return f
  }()
  static func weekday(_ date: String) -> String {
    guard let d = parser.date(from: date) else { return "" }
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = TimeZone(identifier: "UTC")!
    return ["日", "一", "二", "三", "四", "五", "六"][calendar.component(.weekday, from: d) - 1]
  }
  static let header: DateFormatter = {
    let f = DateFormatter()
    f.locale = Locale(identifier: "zh_Hans_CN")
    f.dateFormat = "M月d日 EEEE"
    return f
  }()
}

// MARK: - metric detail pages

/// Shared frame of the single-metric pages opened from the home tiles: the
/// metric's own content, its history and the explainer link.
private struct MetricDetailPage<Content: View>: View {
  let model: ProductModel
  let title: String, historyMetric: String, id: String
  @ViewBuilder var content: () -> Content
  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 16) {
        AthleteRefreshNotice(snapshot: model.data)
        content()
        NavigationLink {
          AthleteHistoryView(model: model, initialMetric: historyMetric)
        } label: {
          RAIRowLabel(title: title + "历史", icon: "calendar", detail: "按天查看")
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier(id + ".history")
        MetricsExplainerLink().frame(maxWidth: .infinity)
      }.padding(18)
    }.navigationTitle(title).navigationBarTitleDisplayMode(.inline).raiScreen()
      .refreshable { await model.refresh() }
      .accessibilityIdentifier("screen." + id)
  }
}

/// One labelled value row inside a detail card.
private struct DetailRow: View {
  let title: String, value: String
  var note: String?
  var emphasized = false
  var body: some View {
    VStack(alignment: .leading, spacing: 2) {
      HStack(alignment: .firstTextBaseline) {
        Text(title).font(.subheadline)
        Spacer(minLength: 8)
        Text(value).font(.body.weight(emphasized ? .bold : .semibold)).monospacedDigit()
          .foregroundStyle(emphasized ? RAITheme.orange : RAITheme.primary)
      }
      if let note { Text(note).font(.caption2).foregroundStyle(RAITheme.secondary) }
    }.accessibilityElement(children: .combine)
  }
}

@MainActor private func detailCard<Content: View>(_ title: String, icon: String, @ViewBuilder _ content: () -> Content) -> some View {
  VStack(alignment: .leading, spacing: 12) {
    CardHeading(title: title, icon: icon)
    content()
  }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
}

@MainActor private func notes(_ lines: [String]) -> some View {
  VStack(alignment: .leading, spacing: 4) {
    ForEach(lines, id: \.self) { Text($0).font(.caption).foregroundStyle(RAITheme.secondary) }
  }
}

/// Training readiness: the score, then the three caps it is the minimum of.
struct TrainingReadinessDetailView: View {
  let model: ProductModel
  var body: some View {
    MetricDetailPage(model: model, title: "训练准备度", historyMetric: "trainingReadiness", id: "readiness") {
      if let snapshot = model.data.athlete, snapshot.supportsPresentation {
        let metric = snapshot.trainingReadiness
        if let value = metric.presentationValue {
          hero(value, metric.envelope)
          caps(value)
          inputs(value)
        } else {
          EmptyCard(title: "训练准备度暂不可用", detail: AthleteModelCard.reason(metric.envelope))
          if metric.envelope.mode == "workout_only", let range = metric.value?.range {
            detailCard("可能范围", icon: "arrow.left.and.right") {
              DetailRow(title: "按训练负荷估计", value: "\(ProductFormat.decimal(range.low))–\(ProductFormat.decimal(range.high)) 分")
              notes(["范围过宽，暂不给出单一分数。"])
            }
          }
        }
      } else {
        EmptyCard(title: "训练准备度暂不可用", detail: model.data.athleteAbsenceText)
      }
    }
  }

  private func hero(_ value: ReadinessValue, _ envelope: MetricEnvelope) -> some View {
    let score = value.score
    var lines: [String] = []
    if let notice = ProductReasonText.lowReadinessNotice(score: score) { lines.append(notice) }
    if envelope.mode == "estimated" { lines.append("部分输入为估计值，结果仅供参考。") }
    lines.append("只反映已记录的跑步训练，是参考信息而非训练指令。")
    return VStack(spacing: 10) {
      ArcGauge(progress: score.map { $0 / 100 }) {
        VStack(spacing: 0) {
          Text(ProductFormat.decimal(score)).font(.system(size: 44, weight: .bold, design: .rounded)).monospacedDigit()
          Text("分").font(.caption).foregroundStyle(RAITheme.secondary)
        }
      }.frame(height: 150).frame(maxWidth: .infinity)
      if let score {
        Text(HomeMetricGrid.readinessLabel(score)).font(.headline).foregroundStyle(HomePalette.scoreColor(score))
      }
      notes(lines).frame(maxWidth: .infinity, alignment: .leading)
    }.padding(18).frame(maxWidth: .infinity).raiGlass().accessibilityElement(children: .combine)
  }

  private func caps(_ value: ReadinessValue) -> some View {
    let items: [(String, Double?, String)] = [
      ("生理恢复", value.physiologicalCap, "昨夜 HRV、静息心率与睡眠"),
      ("近期训练疲劳", value.sessionCap, "最近几次训练尚未消散的疲劳"),
      ("本周负荷", value.weeklyCap, "近 7 天负荷相对长期水平"),
    ]
    let present = items.compactMap(\.1).filter(\.isFinite)
    let limiting = present.min()
    return detailCard("由什么决定", icon: "slider.horizontal.3") {
      notes(["准备度取下面各项上限中的最低值，最低的一项就是今天的限制因素。"])
      ForEach(items, id: \.0) { item in
        DetailRow(
          title: item.0, value: item.1.map { ProductFormat.decimal($0) + " 分" } ?? "暂无", note: item.2,
          emphasized: item.1 != nil && item.1 == limiting && present.count > 1)
      }
    }
  }

  @ViewBuilder private func inputs(_ value: ReadinessValue) -> some View {
    if value.recentRecordedLoad != nil || value.loadMultiplier != nil {
      detailCard("训练输入", icon: "figure.run") {
        if let load = value.recentRecordedLoad {
          DetailRow(title: "近期已记录负荷", value: ProductFormat.decimal(load))
        }
        if let factor = value.loadMultiplier {
          DetailRow(title: "负荷系数", value: ProductFormat.decimal(factor, digits: 2))
        }
      }
    }
  }
}

/// Physiological recovery: the score and each input against its baseline.
struct PhysiologicalRecoveryDetailView: View {
  let model: ProductModel
  var body: some View {
    MetricDetailPage(model: model, title: "生理恢复", historyMetric: "physiologicalRecovery", id: "recovery") {
      if let snapshot = model.data.athlete, snapshot.supportsPresentation {
        let metric = snapshot.physiologicalRecovery
        if let value = metric.presentationValue {
          hero(value, metric.envelope)
          components(value, isV4: metric.envelope.modelVersion == "rai.recovery.physiological/v4")
        } else {
          EmptyCard(title: "生理恢复暂不可用", detail: AthleteModelCard.reason(metric.envelope))
        }
      } else {
        EmptyCard(title: "生理恢复暂不可用", detail: model.data.athleteAbsenceText)
      }
    }
  }

  private func hero(_ value: RecoveryValue, _ envelope: MetricEnvelope) -> some View {
    var lines: [String] = []
    if envelope.mode == "estimated" {
      lines.append("数据有限：近期 \(value.trendNights ?? 0) 晚 · 基线 \(value.baselineNights ?? 0) 晚")
    }
    if let missing = value.missingComponents, !missing.isEmpty {
      lines.append("昨夜数据不足：" + missing.map { Self.names[$0] ?? $0 }.joined(separator: "、"))
    }
    if value.sleepRelativeToHabit == true { lines.append("睡眠按你的个人习惯评估") }
    return VStack(alignment: .leading, spacing: 10) {
      HStack(alignment: .firstTextBaseline, spacing: 4) {
        Text(ProductFormat.decimal(value.score)).font(.system(size: 44, weight: .bold, design: .rounded)).monospacedDigit()
        Text("分").font(.subheadline).foregroundStyle(RAITheme.secondary)
        Spacer(minLength: 8)
        if let level = ProductReasonText.recoveryLevel(value.deviationLevel) {
          Text(level).font(.subheadline.weight(.semibold)).foregroundStyle(HomePalette.scoreColor(value.score))
        }
      }
      ScoreLevelBar(score: value.score).frame(height: 16)
      if !lines.isEmpty { notes(lines) }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass().accessibilityElement(children: .combine)
  }

  private static let names = ["hrv": "HRV", "rhr": "静息心率", "sleep": "睡眠"]

  private func components(_ value: RecoveryValue, isV4: Bool) -> some View {
    let rows: [(String, RecoveryComponentState)] = [("hrv", value.hrv), ("rhr", value.rhr), ("sleep", value.sleep)]
    return detailCard("三项输入", icon: "waveform.path.ecg") {
      notes(["与你自己的近期基线比较，不和他人比较。"])
      ForEach(rows, id: \.0) { key, state in
        componentRow(key, state, value.components?[key], isV4: isV4)
        if key != "sleep" { Divider() }
      }
    }
  }

  private func componentRow(_ key: String, _ state: RecoveryComponentState, _ detail: RecoveryV3Component?, isV4: Bool) -> some View {
    let (color, status): (Color, String) = {
      switch state.relativeToBaseline.rawValue {
      case "no_adverse_deviation": return (HomePalette.green, "正常")
      case "unknown", "": return (RAITheme.secondary, state.availability.rawValue.hasPrefix("missing") ? "缺失" : "未知")
      default: return (HomePalette.amber, "偏离基线")
      }
    }()
    let unit = ["hrv": "ms", "rhr": "bpm", "sleep": "小时"][key] ?? ""
    let digits = key == "sleep" ? 1 : 0
    var facts: [String] = []
    if let observed = detail?.observed, observed.isFinite {
      facts.append("昨夜 " + ProductFormat.decimal(observed, digits: digits) + " " + unit)
    }
    if isV4, let trend = detail?.trend, trend.isFinite, let n = detail?.trendNights, n > 0 {
      facts.append("近 \(n) 次均值 " + ProductFormat.decimal(trend, digits: digits) + " " + unit)
    }
    let source = value(for: key)
    return VStack(alignment: .leading, spacing: 4) {
      HStack {
        RoundedRectangle(cornerRadius: 2).fill(color).frame(width: 4, height: 16)
        Text(Self.names[key] ?? key).font(.subheadline.weight(.semibold))
        Spacer(minLength: 8)
        Text(status).font(.subheadline.weight(.semibold)).foregroundStyle(color)
      }
      if !facts.isEmpty { Text(facts.joined(separator: " · ")).font(.caption).monospacedDigit().foregroundStyle(RAITheme.secondary) }
      if let source { Text(source).font(.caption2).foregroundStyle(RAITheme.secondary) }
    }.accessibilityElement(children: .combine)
  }

  private func value(for key: String) -> String? {
    guard let value = model.data.athlete?.physiologicalRecovery.presentationValue else { return nil }
    let prefix = (Self.names[key] ?? key) + "："
    return value.inputSourceDescriptions.first { $0.hasPrefix(prefix) }.map { "计算采用" + $0.dropFirst(prefix.count) }
  }
}

/// Hours until the next hard session is reasonable, with its range.
struct SessionRecoveryDetailView: View {
  let model: ProductModel
  var body: some View {
    MetricDetailPage(model: model, title: "高强度恢复", historyMetric: "sessionRecovery", id: "sessionRecovery") {
      if let snapshot = model.data.athlete, snapshot.supportsPresentation, let metric = snapshot.sessionRecovery {
        if let value = metric.presentationValue {
          hero(value)
          detailCard("怎么估计", icon: "info.circle") {
            notes([
              "根据已同步跑步的强度与时长，估计这些训练的疲劳何时基本消散。",
              "显示的是可能范围，不是概率；范围上沿到 0 时视为已就绪。",
              "只看跑步训练，不包含睡眠和其他运动。",
            ])
          }
        } else {
          EmptyCard(title: "高强度恢复暂不可用", detail: AthleteModelCard.reason(metric.envelope))
        }
      } else {
        EmptyCard(title: "高强度恢复暂不可用", detail: model.data.athlete == nil ? model.data.athleteAbsenceText : "同步跑步后估计")
      }
    }
  }

  private func hero(_ value: SessionRecoveryValue) -> some View {
    let ready = !value.overflow && (value.range?.high ?? value.remainingHours ?? 1) <= 0
    return VStack(alignment: .leading, spacing: 12) {
      Text("距下一次高强度训练").font(.subheadline).foregroundStyle(RAITheme.secondary)
      Text(ready ? "已就绪" : value.text).font(.system(size: 34, weight: .bold, design: .rounded)).monospacedDigit()
      HoursRangeBar(range: value.range, remaining: value.remainingHours, horizon: 72, overflow: value.overflow)
        .frame(height: 24)
      HStack { Text("现在"); Spacer(); Text("72 h") }.font(.caption2.monospacedDigit()).foregroundStyle(RAITheme.secondary)
      if let readyAfter = value.readyAfter, !ready {
        DetailRow(title: "预计就绪", value: Date(timeIntervalSince1970: TimeInterval(readyAfter) / Self.unitsPerSecond(readyAfter))
          .formatted(.dateTime.month().day().weekday().hour().minute()))
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass().accessibilityElement(children: .combine)
  }

  /// readyAfter is an epoch in microseconds on current servers; tolerate seconds/ms.
  private static func unitsPerSecond(_ v: Int64) -> Double {
    v > 100_000_000_000_000 ? 1_000_000 : v > 100_000_000_000 ? 1_000 : 1
  }
}
