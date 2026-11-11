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
          HStack(spacing: 10) { ProgressView(); Text("正在读取完整统计…").font(.subheadline) }
            .frame(maxWidth: .infinity, minHeight: 150).foregroundStyle(RAITheme.secondary)
        } else if let error = model.statisticsError {
          VStack(alignment: .leading, spacing: 12) {
            Text(error).foregroundStyle(RAITheme.secondary)
            Button("重试") { Task { await model.loadStatistics(window) } }
              .buttonStyle(.bordered).accessibilityIdentifier("statistics.retry")
          }.frame(maxWidth: .infinity, alignment: .leading).padding(20).background(RAITheme.surface, in: RoundedRectangle(cornerRadius: 20))
        } else if let current {
          totals(current)
          volumeChart(current)
          distribution(current)
        }
        NavigationLink {
          AnalysisView(model: model)
        } label: {
          HStack {
            Image(systemName: "waveform.path.ecg")
            Text("运动模型").font(.headline)
            Spacer()
            Image(systemName: "chevron.right").font(.caption.weight(.bold))
          }.foregroundStyle(RAITheme.primary).padding(20)
            .background(RAITheme.surface, in: RoundedRectangle(cornerRadius: 20))
        }.accessibilityIdentifier("statistics.analysis")
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
      HStack {
        Text("\(window.timezone) · 按跑步开始日期计入").font(.caption)
        Spacer()
        if !window.contains(Date()) {
          Button("回到本\(period.title)") { selectedDate = Date() }.font(.caption.weight(.semibold))
        }
      }.foregroundStyle(RAITheme.secondary)
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
    }.foregroundStyle(RAITheme.primary).padding(20).background(RAITheme.surface, in: RoundedRectangle(cornerRadius: 20))
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
        Text("尚无可用的个人心率分区；可在「我的」完善心率参数。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if value.heartRateProfiles.count > 1 && showHeartRate {
        Text("本期含 \(value.heartRateProfiles.count) 组历史个人参数，按各次训练当时参数归区。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if value.distributionFailureCount > 0 {
        Text("\(value.distributionFailureCount) 次分区读取失败，其里程暂列未分类。下拉可重试。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if value.unavailableDistributionCount > 0 {
        Text("\(value.unavailableDistributionCount) 次跑步暂无可用分区结果，其里程列为未分类。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      DisclosureGroup("统计口径") {
        VStack(alignment: .leading, spacing: 8) {
          Text(showHeartRate
            ? "按每次跑步当时的个人心率储备（HRR）分区；速度与心率原始区间对齐后计算距离。缺少参数、心率或速度的里程不归类。"
            : "按原始速度区间计算跑量，单位 min/km；边界值归入较慢一档。无可靠速度覆盖的里程不归类。")
          Text("占比以本期已知总里程为分母，未分类也计入。未来日期不作实测零值展示。")
        }.font(.caption).foregroundStyle(RAITheme.secondary).padding(.top, 8)
      }.font(.subheadline).tint(RAITheme.secondary)
    }.foregroundStyle(RAITheme.primary).padding(20).background(RAITheme.surface, in: RoundedRectangle(cornerRadius: 20))
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
