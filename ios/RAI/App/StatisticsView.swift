import Charts
import ProductCore
import SwiftUI

struct StatisticsView: View {
  @Bindable var model: ProductModel
  @State private var period: RunningStatisticsPeriod = .week
  @State private var selectedDate = Date()
  @State private var showHeartRate = false
  @ScaledMetric(relativeTo: .largeTitle) private var distanceFontSize = 64.0

  private var window: RunningStatisticsWindow {
    RunningStatisticsWindow(period: period, containing: selectedDate, timezone: model.statisticsTimezone)
  }
  private var requestID: String { "\(ObjectIdentifier(model)):\(model.statisticsRevision):\(period.rawValue):\(window.start.timeIntervalSince1970):\(window.timezone)" }
  private var current: RunningStatistics? { model.statistics.flatMap { $0.window == window ? $0 : nil } }

  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 24) {
        Picker("统计周期", selection: $period) {
          ForEach(RunningStatisticsPeriod.allCases) { Text($0.title).tag($0) }
        }.pickerStyle(.segmented).accessibilityIdentifier("statistics.period")
        periodNavigation
        if model.statisticsLoading {
          HStack(spacing: 10) { ProgressView(); Text("正在读取…").font(.subheadline) }
            .frame(maxWidth: .infinity, minHeight: 150).foregroundStyle(RAITheme.secondary)
        } else if let error = model.statisticsError {
          VStack(alignment: .leading, spacing: 12) {
            Text(error).foregroundStyle(RAITheme.secondary)
            Button("重试") { Task { await model.loadStatistics(window) } }
              .buttonStyle(.bordered).accessibilityIdentifier("statistics.retry")
          }.frame(maxWidth: .infinity, alignment: .leading).padding(20).raiGlass()
        } else if let current {
          totals(current)
          volumeChart(current)
          distribution(current)
        }
        NavigationLink {
          AnalysisView(model: model)
        } label: {
          RAIRowLabel(title: "运动模型", icon: "waveform.path.ecg", detail: "准备度、跑力、训练负荷")
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("statistics.analysis")
      }.padding(.horizontal, 20).padding(.vertical, 16)
    }.navigationTitle("统计").raiScreen().accessibilityIdentifier("screen.statistics")
      .task(id: requestID) { await model.loadStatistics(window) }
      .refreshable { await model.loadStatistics(window) }
  }

  private var periodNavigation: some View {
    VStack(alignment: .leading, spacing: 8) {
      HStack {
        Button { selectedDate = window.shifted(-1).start } label: {
          Image(systemName: "chevron.left").frame(width: 40, height: 44)
        }.accessibilityLabel(period == .week ? "上一周" : "上个月").accessibilityIdentifier("statistics.previous")
        Spacer(minLength: 0)
        Text(periodTitle).font(.headline).monospacedDigit().minimumScaleFactor(0.7).lineLimit(1)
        Spacer(minLength: 0)
        Button { selectedDate = window.shifted(1).start } label: {
          Image(systemName: "chevron.right").frame(width: 40, height: 44)
        }.disabled(window.end > Date()).accessibilityLabel(period == .week ? "下一周" : "下个月")
          .accessibilityIdentifier("statistics.next")
      }.foregroundStyle(RAITheme.primary)
      if !window.contains(Date()) {
        Button("回到本\(period.title)") { selectedDate = Date() }.font(.caption.weight(.semibold))
          .foregroundStyle(RAITheme.orange).frame(maxWidth: .infinity)
      }
    }
  }
  private var periodTitle: String {
    if period == .month { return formatted(window.start, "yyyy 年 M 月") }
    let last = window.calendar.date(byAdding: .day, value: -1, to: window.end)!
    return formatted(window.start, "yyyy.M.d") + " – " + formatted(last, "M.d")
  }
  private func formatted(_ date: Date, _ format: String) -> String {
    let formatter = DateFormatter()
    formatter.calendar = window.calendar
    formatter.timeZone = window.calendar.timeZone
    formatter.locale = Locale(identifier: "zh_CN")
    formatter.dateFormat = format
    return formatter.string(from: date)
  }
  private func totals(_ value: RunningStatistics) -> some View {
    VStack(alignment: .leading, spacing: 18) {
      Text("总跑量").font(.subheadline).foregroundStyle(RAITheme.secondary)
      HStack(alignment: .firstTextBaseline, spacing: 8) {
        Text(value.missingDistanceCount == value.count && value.count > 0 ? "—" : String(format: "%.1f", value.distanceMeters / 1000))
          .font(.system(size: distanceFontSize, weight: .bold, design: .rounded)).monospacedDigit().lineLimit(1).minimumScaleFactor(0.5)
        Text("km").font(.title3).foregroundStyle(RAITheme.secondary)
      }.accessibilityElement(children: .combine)
      ViewThatFits(in: .horizontal) {
        HStack(spacing: 32) { secondaryTotals(value) }
        VStack(alignment: .leading, spacing: 10) { secondaryTotals(value) }
      }.font(.subheadline).foregroundStyle(RAITheme.secondary)
      if value.count == 0 { Text("本期暂无跑步记录。").font(.caption).foregroundStyle(RAITheme.secondary) }
      if value.missingDistanceCount > 0 {
        Text("\(value.missingDistanceCount) 次跑步缺少距离；里程仅含已知数据。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
    }.foregroundStyle(RAITheme.primary).frame(maxWidth: .infinity, alignment: .leading)
  }
  private func volumeChart(_ value: RunningStatistics) -> some View {
    VStack(alignment: .leading, spacing: 16) {
      HStack { Text("每日跑量").font(.headline); Spacer(); Text("km").font(.caption).foregroundStyle(RAITheme.secondary) }
      if RAIRenderer.metalAvailable {
        Chart(Array(value.days.enumerated()), id: \.offset) { index, day in
          if !isFuture(day.date) {
            if day.count > 0 && day.missingDistanceCount == day.count {
              PointMark(x: .value("日期", index), y: .value("公里", 0))
                .foregroundStyle(RAITheme.secondary).symbol(.diamond)
                .annotation(position: .top) { Text("?").font(.caption2).foregroundStyle(RAITheme.secondary) }
                .accessibilityLabel(formatted(day.date, "M月d日") + "距离未知")
            } else {
              BarMark(x: .value("日期", index), y: .value("公里", day.distanceMeters / 1000))
                .foregroundStyle(RAITheme.orange).cornerRadius(3)
                .annotation(position: .top) {
                  if day.missingDistanceCount > 0 { Text("部分").font(.caption2).foregroundStyle(RAITheme.secondary) }
                }
                .accessibilityLabel(formatted(day.date, "M月d日"))
                .accessibilityValue(dayAccessibility(day))
            }
          }
        }.chartXScale(domain: -0.5...Double(value.days.count) - 0.5)
          .chartYScale(domain: 0...max(1, (value.days.map(\.distanceMeters).max() ?? 0) / 1000 * 1.15))
          .chartXAxis {
            AxisMarks(values: Array(stride(from: 0, to: value.days.count, by: period == .week ? 1 : 7))) { axis in
              AxisValueLabel {
                if let index = axis.as(Int.self), value.days.indices.contains(index) {
                  Text(formatted(value.days[index].date, period == .week ? "EEEEE" : "d"))
                }
              }
            }
          }.chartYAxis { AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) }
          .frame(height: 180).accessibilityIdentifier("statistics.volumeChart")
      } else {
        VStack(spacing: 8) {
          ForEach(value.days) { day in
            HStack(spacing: 10) {
              Text(formatted(day.date, "M/d")).font(.caption).frame(width: 40, alignment: .leading)
              GeometryReader { proxy in
                RoundedRectangle(cornerRadius: 3).fill(RAITheme.orange)
                  .frame(width: isFuture(day.date) ? 0 : proxy.size.width * day.distanceMeters / max(1, value.days.map(\.distanceMeters).max() ?? 1))
              }.frame(height: 8).accessibilityHidden(true)
              Text(isFuture(day.date) ? "—" : day.missingDistanceCount == day.count && day.count > 0 ? "未知" : String(format: "%.1f%@", day.distanceMeters / 1000, day.missingDistanceCount > 0 ? "*" : ""))
                .font(.caption).monospacedDigit().frame(width: 44, alignment: .trailing)
            }.accessibilityElement(children: .ignore).accessibilityLabel(formatted(day.date, "M月d日") + " " + dayAccessibility(day))
          }
        }.accessibilityIdentifier("statistics.volumeChart")
      }
      if value.missingDistanceCount > 0 {
        Text("? 距离未知 · 部分 / * 表示当日有记录缺少距离").font(.caption).foregroundStyle(RAITheme.secondary)
      }
    }.foregroundStyle(RAITheme.primary).padding(20).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }
  private func distribution(_ value: RunningStatistics) -> some View {
    let zones = showHeartRate ? value.heartRate : value.pace
    let unknown = showHeartRate ? value.heartRateUnknownMeters : value.paceUnknownMeters
    return VStack(alignment: .leading, spacing: 18) {
      Text("跑量分布").font(.headline)
      Picker("分区指标", selection: $showHeartRate) {
        Text("配速").tag(false)
        Text("心率").tag(true)
      }.pickerStyle(.segmented).accessibilityIdentifier("statistics.distribution")
      ForEach(zones) { zone in zoneRow(zone.title, meters: zone.distanceMeters, total: value.distanceMeters, unknown: false) }
      zoneRow("未分类", meters: unknown, total: value.distanceMeters, unknown: true)

      if showHeartRate && value.heartRateProfiles.isEmpty && value.count > 0 {
        Text("还没有心率区间：请在「我的 → 静息与最大心率」填写最大心率。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if value.heartRateProfiles.count > 1 && showHeartRate {
        Text("这段时间你调整过心率设置，每次跑步按当时的设置划分区间。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if value.distributionFailureCount > 0 {
        Text("\(value.distributionFailureCount) 次跑步的区间读取失败，暂列未分类，下拉可重试。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if value.unavailableDistributionCount > 0 {
        Text("\(value.unavailableDistributionCount) 次跑步还没有区间结果，暂列未分类。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      DisclosureGroup("怎么算的") {
        VStack(alignment: .leading, spacing: 8) {
          Text(showHeartRate
            ? "按每次跑步当时的最大心率和静息心率划分心率区间，再统计各区间内跑过的距离。缺少心率或速度的里程列为未分类。"
            : "按配速统计各档跑过的距离，刚好落在分界上的归入较慢一档。没有可靠配速的里程列为未分类。")
          Text("占比按本期总里程计算；跑步按开始日期计入。")
        }.font(.caption).foregroundStyle(RAITheme.secondary).padding(.top, 8)
      }.font(.subheadline).tint(RAITheme.secondary)
    }.foregroundStyle(RAITheme.primary).padding(20).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }
  @ViewBuilder private func secondaryTotals(_ value: RunningStatistics) -> some View {
    Label("\(value.count) 次跑步", systemImage: "figure.run")
    Label(ProductFormat.duration(value.durationSeconds), systemImage: "clock")
  }
  private func isFuture(_ date: Date) -> Bool { date > window.calendar.startOfDay(for: Date()) }
  private func dayAccessibility(_ day: RunningStatisticsDay) -> String {
    if isFuture(day.date) { return "未来日期" }
    if day.missingDistanceCount > 0 {
      return String(format: "已知 %.2f 公里，%d 次跑步，%d 次缺少距离", day.distanceMeters / 1000, day.count, day.missingDistanceCount)
    }
    return String(format: "%.2f 公里，%d 次跑步", day.distanceMeters / 1000, day.count)
  }
  private func zoneRow(_ title: String, meters: Double, total: Double, unknown: Bool) -> some View {
    VStack(spacing: 7) {
      HStack {
        Text(title).font(.subheadline).fixedSize(horizontal: false, vertical: true)
        Spacer()
        Text(String(format: "%.1f km", meters / 1000)).font(.subheadline).monospacedDigit().lineLimit(1).minimumScaleFactor(0.7)
        Text(total > 0 ? String(format: "%.0f%%", meters / total * 100) : "—")
          .font(.caption).monospacedDigit().foregroundStyle(RAITheme.secondary).frame(width: 38, alignment: .trailing)
      }
      GeometryReader { proxy in
        ZStack(alignment: .leading) {
          Capsule().fill(RAITheme.surfaceMuted)
          Capsule().fill(unknown ? RAITheme.secondary : RAITheme.orange)
            .frame(width: proxy.size.width * (total > 0 ? min(1, max(0, meters / total)) : 0))
        }
      }.frame(height: 7).accessibilityHidden(true)
    }.accessibilityElement(children: .combine)
  }
}
