import Charts
import ProductCore
import SwiftUI
import SyncCore

struct ProductTabs: View {
  @State private var model = ProductModel()
  @Bindable var sync: SyncModel
  @State private var tab = 0
  @Environment(\.scenePhase) private var phase
  var body: some View {
    TabView(selection: $tab) {
      NavigationStack { HomeView(model: model, sync: sync, tab: $tab) }.tabItem {
        Label("首页", systemImage: "house.fill")
      }.tag(0)
      NavigationStack { ActivitiesView(model: model) }.tabItem {
        Label("运动", systemImage: "figure.run")
      }.tag(1)
      NavigationStack { TrainingView(model: model) }.tabItem {
        Label("训练", systemImage: "list.bullet.clipboard")
      }.tag(2)
      NavigationStack { AnalysisView(model: model) }.tabItem {
        Label("分析", systemImage: "chart.bar.xaxis")
      }.tag(3)
      NavigationStack { ProfileView(model: model, sync: sync) }.tabItem {
        Label("我的", systemImage: "person.crop.circle")
      }.tag(4)
    }.tint(RAITheme.orange).preferredColorScheme(.dark)
      .task { await model.refresh() }
      .onChange(of: phase) { _, value in if value == .active { Task { await model.refresh() } } }
      .task(id: phase) {
        guard phase == .active else { return }
        while !Task.isCancelled {
          try? await Task.sleep(for: .seconds(20))
          guard !Task.isCancelled else { return }
          await model.refresh()
        }
      }
  }
}
struct HomeView: View {
  @Bindable var model: ProductModel
  @Bindable var sync: SyncModel
  @Binding var tab: Int
  @AppStorage("runnerName") private var name = "跑者"
  @Namespace private var hero
  @State private var selectedSession: ProductSession?
  @Environment(\.dynamicTypeSize) private var typeSize
  @Environment(\.accessibilityReduceMotion) private var reduced
  var body: some View {
    ScrollView {
      VStack(spacing: 12) {
        greeting.padding(.bottom, 4)
        if let session = model.data.todaySession {
          Button {
            selectedSession = session
          } label: {
            TrainingHero(session: session)
          }.buttonStyle(RAIPressStyle()).matchedTransitionSource(id: session.id, in: hero)
            .accessibilityIdentifier("home.hero")
        } else {
          EmptyCard(title: "今日训练", detail: model.configured ? "今天没有已发布的训练安排。" : "连接私有服务，查看今天的训练。")
        }
        Button {
          tab = 3
        } label: {
          BodyStatusCard(data: model.data)
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("home.status")
        WeeklyCard(overview: model.data.overview)
        VStack(alignment: .leading, spacing: 14) {
          HStack {
            CardHeading(title: "最近运动", icon: "clock")
            Spacer()
            Button("查看更多") { tab = 1 }.font(.caption).foregroundStyle(RAITheme.secondary)
          }
          if let workout = model.data.workouts.first {
            NavigationLink {
              WorkoutDetail(model: model, workout: workout)
            } label: {
              ActivityRow(workout: workout)
            }
          } else {
            Text("暂无已同步的运动记录").font(.subheadline).foregroundStyle(RAITheme.secondary)
          }
        }.padding(18).raiGlass()
        if model.loading { ProgressView("正在更新").font(.caption) }
        if let error = model.error {
          Text(error).font(.footnote).foregroundStyle(RAITheme.amber).accessibilityIdentifier(
            "product.error")
        }
      }.padding(.horizontal, 18).padding(.top, 8).padding(.bottom, 22)
    }.raiScreen().refreshable { await model.refresh() }.toolbar(.hidden, for: .navigationBar)
      .navigationDestination(item: $selectedSession) { session in
        SessionDetail(model: model, session: session, effective: model.data.effective).modifier(
          RAIDetailTransition(id: session.id, namespace: hero))
      }
      .accessibilityIdentifier("screen.home")
  }
  private var greeting: some View {
    Group {
      if typeSize.isAccessibilitySize {
        VStack(alignment: .leading, spacing: 12) {
          identity
          syncBadge
        }.frame(maxWidth: .infinity, alignment: .leading)
      } else {
        HStack(spacing: 12) {
          identity
          Spacer(minLength: 2)
          syncBadge
        }
      }
    }
  }
  private var identity: some View {
    HStack(alignment: .center, spacing: 12) {
      Image(systemName: "person.crop.circle.fill").font(.system(size: 46, weight: .light))
        .foregroundStyle(RAITheme.amber).accessibilityHidden(true)
      VStack(alignment: .leading, spacing: 2) {
        Text(greetingText).font(.subheadline).foregroundStyle(RAITheme.secondary)
        Text(name).font(.system(.title, design: .rounded, weight: .bold))
        Text("每一次奔跑，都是更好的自己").font(.caption).foregroundStyle(RAITheme.secondary)
      }
    }
  }
  private var syncBadge: some View {
    NavigationLink {
      WorkoutSyncView(model: sync)
    } label: {
      VStack(spacing: 4) {
        Image(systemName: sync.busy ? "arrow.triangle.2.circlepath" : "cloud")
        Text(syncLabel).font(.caption2)
      }.foregroundStyle(sync.error == nil ? RAITheme.secondary : RAITheme.amber).padding(10)
        .background(.white.opacity(0.06), in: Capsule())
    }.buttonStyle(.plain).accessibilityIdentifier("home.sync")
  }
  private var syncLabel: String {
    sync.busy
      ? "同步中"
      : sync.error != nil
        ? "需处理" : (sync.pending + sync.legacyPending) > 0 ? "待同步" : sync.lastSync != nil ? "本轮已同步" : "尚未同步"
  }
  private var greetingText: String {
    let h = Calendar.current.component(.hour, from: Date())
    return h < 11 ? "早上好，" : h < 18 ? "下午好，" : "晚上好，"
  }
}
struct TrainingHero: View {
  let session: ProductSession
  var body: some View {
    VStack(alignment: .leading, spacing: 12) {
      HStack {
        Label("今日训练", systemImage: "figure.run").font(.subheadline.weight(.semibold))
        Image(systemName: "chevron.right").font(.caption)
        Spacer()
        Image(systemName: "ellipsis").padding(8).background(.white.opacity(0.08), in: Circle())
      }
      Spacer(minLength: 4)
      Text(session.title).font(.system(.largeTitle, design: .rounded, weight: .bold))
      Text(prescription(session)).font(.title3).monospacedDigit()
      HStack {
        Text(trainingTag(session.type))
        if session.type != "REST" { Text("自感用力 \(Int(session.raw.number("effort") ?? 0))") }
        if session.modified { Text("已调整").foregroundStyle(RAITheme.amber) }
      }.font(.caption).foregroundStyle(.white.opacity(0.85))
      HStack {
        Spacer()
        Label(
          session.runnable ? "查看并开始训练" : "查看训练安排",
          systemImage: session.runnable ? "play.fill" : "chevron.right")
        Spacer()
      }.font(.headline).padding(.vertical, 14).background(.white.opacity(0.12), in: Capsule())
        .overlay(Capsule().strokeBorder(.white.opacity(0.40), lineWidth: 0.5))
    }.padding(20).frame(minHeight: 235, alignment: .leading).background {
      HeroScene().clipShape(RoundedRectangle(cornerRadius: RAITheme.radius))
    }.raiGlass().foregroundStyle(.white)
  }
}
func prescription(_ s: ProductSession) -> String {
  if s.type == "REST" { return "给身体一点恢复时间" }
  var parts: [String] = []
  if s.distance > 0 { parts.append(ProductFormat.distance(s.distance) + " 公里") }
  if s.duration > 0 { parts.append(ProductFormat.decimal(s.duration / 60) + " 分钟") }
  return parts.joined(separator: " · ")
}
struct BodyStatusCard: View {
  let data: ProductSnapshot
  @Environment(\.dynamicTypeSize) private var size
  var body: some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "身体状态", icon: "waveform.path.ecg")
      ViewThatFits(in: .horizontal) {
        HStack(spacing: 20) {
          ReadinessRing(value: data.metric("readiness")).frame(
            width: size.isAccessibilitySize ? 210 : 118,
            height: size.isAccessibilitySize ? 210 : 118)
          metrics.frame(minWidth: 140)
        }
        VStack(spacing: 16) {
          ReadinessRing(value: data.metric("readiness")).frame(
            width: size.isAccessibilitySize ? 210 : 138,
            height: size.isAccessibilitySize ? 210 : 138)
          metrics
        }
      }
      Text("负荷状态估算 · 体能与疲劳：负荷单位/日，同尺度").font(.caption2).foregroundStyle(RAITheme.secondary)
    }.padding(18).raiGlass().foregroundStyle(.white)
  }
  private func relative(_ name: String) -> Double? {
    guard let value = data.metric(name) else { return nil }
    let scale = [data.metric("fitness"), data.metric("fatigue")].compactMap { $0 }.max() ?? value
    return scale > 0 ? value / scale : 0
  }
  private var metrics: some View {
    VStack(spacing: 12) {
      MetricBar(
        name: "体能", symbol: "figure.run", value: data.metric("fitness"),
        color: RAITheme.orange, ratio: relative("fitness"))
      MetricBar(
        name: "疲劳", symbol: "moon.zzz", value: data.metric("fatigue"), color: RAITheme.purple,
        ratio: relative("fatigue"))
      MetricBar(
        name: "恢复", symbol: "leaf", value: data.metric("recovery").map { $0 * 100 },
        color: RAITheme.green, ratio: data.metric("recovery"))
    }
  }
}
struct ReadinessRing: View {
  let value: Double?
  var body: some View {
    ZStack {
      Circle().stroke(.white.opacity(0.16), lineWidth: 9)
      if let value {
        Circle().trim(from: 0, to: max(0, min(value / 100, 1))).stroke(
          AngularGradient(
            colors: [RAITheme.blue, RAITheme.orange, RAITheme.amber], center: .center,
            startAngle: .degrees(0), endAngle: .degrees(300)),
          style: StrokeStyle(lineWidth: 9, lineCap: .round)
        ).rotationEffect(.degrees(-90))
      }
      VStack(spacing: 1) {
        Text(ProductFormat.decimal(value)).font(
          .system(.largeTitle, design: .rounded, weight: .bold)
        ).monospacedDigit()
        Text(value == nil ? "暂无数据" : "准备度").font(.caption)
      }
    }.padding(6).accessibilityElement(children: .ignore).accessibilityLabel(
      "准备度 \(ProductFormat.decimal(value))，训练负荷估算")
  }
}
struct MetricBar: View {
  let name: String, symbol: String, value: Double?, color: Color, ratio: Double?
  @ScaledMetric(relativeTo: .title3) private var symbolWidth = 25.0
  var body: some View {
    HStack(spacing: 8) {
      Image(systemName: symbol).foregroundStyle(color).font(.title3).frame(width: symbolWidth)
      VStack(spacing: 4) {
        HStack {
          Text(name).font(.caption)
          Spacer()
          Text(ProductFormat.decimal(value)).font(.subheadline.weight(.semibold)).monospacedDigit()
        }
        GeometryReader { g in
          Capsule().fill(.white.opacity(0.12)).overlay(alignment: .leading) {
            if let ratio {
              Capsule().fill(color.gradient).frame(width: g.size.width * max(0, min(ratio, 1)))
            }
          }
        }.frame(height: 4).accessibilityHidden(true)
      }
    }
  }
}
struct WeeklyCard: View {
  @Environment(\.dynamicTypeSize) private var typeSize
  @ScaledMetric(relativeTo: .caption2) private var fallbackChartHeight = 56.0
  let overview: JSON
  @State private var appeared = false
  @Environment(\.accessibilityReduceMotion) private var reduced
  var days: [JSON] { Array(overview["days"].list.suffix(7)) }
  func total(_ key: String) -> Double? {
    let valid = days.filter { $0.string("date") <= overview.string("today") }
    guard !valid.isEmpty, valid.allSatisfy({ $0.number(key) != nil }) else { return nil }
    return valid.reduce(0) { $0 + ($1.number(key) ?? 0) }
  }
  var body: some View {
    VStack(alignment: .leading, spacing: 12) {
      if typeSize.isAccessibilitySize {
        VStack(alignment: .leading, spacing: 8) { heading }
      } else {
        HStack { heading }
      }
      if typeSize.isAccessibilitySize {
        VStack(spacing: 18) {
          column("跑步距离", "distanceM", ProductFormat.distance(total("distanceM")), "公里")
          column("训练负荷", "load", ProductFormat.decimal(total("load")), "负荷单位")
        }
      } else {
        ViewThatFits(in: .horizontal) {
          HStack(alignment: .top, spacing: 20) {
            column("跑步距离", "distanceM", ProductFormat.distance(total("distanceM")), "公里")
            column("训练负荷", "load", ProductFormat.decimal(total("load")), "负荷单位")
          }
          VStack(spacing: 18) {
            column("跑步距离", "distanceM", ProductFormat.distance(total("distanceM")), "公里")
            column("训练负荷", "load", ProductFormat.decimal(total("load")), "负荷单位")
          }
        }
      }
      Text("仅统计已同步跑步；缺失负荷不补零").font(.caption2).foregroundStyle(RAITheme.secondary)
    }.padding(18).raiGlass().onAppear {
      if !appeared { withAnimation(reduced ? nil : .easeOut(duration: 0.3)) { appeared = true } }
    }
  }
  @ViewBuilder private var heading: some View {
    CardHeading(title: "本周概览", icon: "chart.bar")
    if !typeSize.isAccessibilitySize { Spacer() }
    Text("\(days.filter{($0.number("count") ?? 0)>0}.count)/7 天").font(.caption)
      .foregroundStyle(RAITheme.secondary)
  }
  private func column(_ label: String, _ key: String, _ value: String, _ unit: String) -> some View
  {
    VStack(alignment: .leading, spacing: 4) {
      Text(label).font(.caption).foregroundStyle(RAITheme.secondary)
      HStack(alignment: .firstTextBaseline, spacing: 3) {
        Text(value).font(.title2.bold()).monospacedDigit()
        Text(unit).font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if !RAIRenderer.metalAvailable {
        RecordedBars(
          values: Array(days.enumerated()).map { index, day in
            (["一", "二", "三", "四", "五", "六", "日"][index], day.number(key))
          }
        ).frame(height: fallbackChartHeight)
      } else {
        Chart(Array(days.enumerated()), id: \.offset) { index, day in
          if let n = day.number(key) {
            BarMark(x: .value("日", index), y: .value(label, appeared ? n : 0), width: .ratio(0.46))
              .foregroundStyle(
                LinearGradient(
                  colors: [RAITheme.amber, RAITheme.orange], startPoint: .top, endPoint: .bottom)
              ).cornerRadius(3)
          }
        }.chartXAxis {
          AxisMarks(values: Array(0...6)) { v in
            AxisValueLabel {
              Text(["一", "二", "三", "四", "五", "六", "日"][v.as(Int.self) ?? 0]).font(.caption2)
            }
          }
        }.chartYAxis(.hidden).frame(height: 48)
      }
    }.frame(minWidth: 130, maxWidth: .infinity, alignment: .leading)
  }
}
struct ActivityRow: View {
  let workout: ProductWorkout
  var body: some View {
    HStack(spacing: 12) {
      Image(systemName: "figure.run").font(.title).foregroundStyle(RAITheme.amber).frame(
        width: 48, height: 52
      ).background(.white.opacity(0.05), in: RoundedRectangle(cornerRadius: 14))
      VStack(alignment: .leading, spacing: 4) {
        Text(workout.title).font(.headline)
        Text(
          "\(ProductFormat.distance(workout.raw.number("distanceM"))) 公里 · \(ProductFormat.duration(workout.raw.number("durationS"))) · \(ProductFormat.pace(workout.metric("average_pace")))/公里"
        ).font(.caption).monospacedDigit()
        Text(workout.date, format: .dateTime.month().day().hour().minute()).font(.caption2)
          .foregroundStyle(RAITheme.secondary)
      }
      Spacer(minLength: 0)
      Image(systemName: "chevron.right").font(.caption).foregroundStyle(RAITheme.secondary)
    }.foregroundStyle(.white)
  }
}
struct ActivitiesView: View {
  @Bindable var model: ProductModel
  var body: some View {
    ScrollView {
      LazyVStack(spacing: 12) {
        if model.data.workouts.isEmpty {
          EmptyCard(title: "还没有运动记录", detail: "健康数据同步后，记录会出现在这里。")
        }
        ForEach(model.data.workouts) { w in
          NavigationLink {
            WorkoutDetail(model: model, workout: w)
          } label: {
            VStack(alignment: .leading, spacing: 8) {
              ActivityRow(workout: w)
              Text(w.raw.string("analysisStatus") == "published" ? "分析已发布" : "分析待处理").font(
                .caption2
              ).foregroundStyle(RAITheme.secondary)
            }.padding(16).raiGlass()
          }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("workout.row")
        }
        if !model.data.next.isEmpty { Button("加载更多") { Task { await model.loadMore() } } }
      }.padding(18)
    }.navigationTitle("运动").raiScreen().refreshable { await model.refresh() }
      .accessibilityIdentifier("screen.activities")
  }
}
struct AnalysisView: View {
  @Bindable var model: ProductModel
  var body: some View {
    ScrollView {
      VStack(spacing: 16) {
        BodyStatusCard(data: model.data)
        ForEach(["daily_load", "fitness", "fatigue", "recovery", "readiness"], id: \.self) { name in
          NavigationLink {
            MetricDetail(model: model, name: name)
          } label: {
            VStack(alignment: .leading, spacing: 12) {
              CardHeading(title: metricTitle(name), icon: "chart.xyaxis.line")
              TrendChart(metrics: model.data.history["metrics"].list, name: name).frame(height: 120)
            }.padding(20).raiGlass()
          }.buttonStyle(RAIPressStyle())
        }
        Text("恢复与准备度是仅基于训练负荷的训练状态估算。仅使用近期已发布训练负荷，未纳入睡眠、心率变异性或静息心率趋势。历史缺失保持空白。").font(.footnote)
          .foregroundStyle(RAITheme.secondary)
      }.padding(18)
    }.navigationTitle("分析").raiScreen().refreshable { await model.refresh() }
      .accessibilityIdentifier("screen.analysis")
  }
}
func metricTitle(_ name: String) -> String {
  [
    "daily_load": "训练负荷", "fitness": "体能", "fatigue": "疲劳",
    "recovery": "负荷恢复", "readiness": "准备度",
  ][name] ?? "训练指标"
}
struct TrendChart: View {
  let metrics: [JSON], name: String
  var body: some View {
    let points = metrics.filter { $0.string("metric") == name }
    if points.contains(where: { $0.number("value") != nil }) {
      if !RAIRenderer.metalAvailable {
        RecordedBars(
          values: points.map { (String($0.string("day").suffix(2)), $0.number("value")) },
          color: name == "fatigue" ? RAITheme.purple : RAITheme.orange)
      } else {
        Chart(Array(points.enumerated()), id: \.offset) { index, m in
          if let value = m.number("value") {
            PointMark(x: .value("日期", m.string("day")), y: .value("值", value)).foregroundStyle(
              name == "fatigue" ? RAITheme.purple : RAITheme.orange)
          }
        }.chartXAxis(.hidden).chartYAxis { AxisMarks(position: .leading) }.accessibilityLabel(
          metricTitle(name) + "，已发布历史趋势")
      }
    } else {
      ContentUnavailableView("暂无已发布趋势", systemImage: "chart.xyaxis.line").font(.caption)
    }
  }
}
struct MetricDetail: View {
  @Bindable var model: ProductModel
  let name: String
  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 20) {
        MetricText(
          value: ProductFormat.decimal(model.data.metric(name)),
          unit: name == "recovery" ? "比例" : "")
        TrendChart(metrics: model.data.history["metrics"].list, name: name).frame(height: 220)
        Text("基于已发布的训练负荷。缺失值不补零；恢复与准备度并非医疗诊断。算法与质量标记随服务器发布结果返回。").foregroundStyle(
          RAITheme.secondary)
      }.padding(22).raiGlass().padding(18)
    }.navigationTitle(metricTitle(name)).raiScreen()
  }
}
struct ProfileView: View {
  @Environment(\.accessibilityReduceMotion) private var reduced
  @Environment(\.accessibilityReduceTransparency) private var opaque
  @Environment(\.dynamicTypeSize) private var typeSize
  @Bindable var model: ProductModel
  @Bindable var sync: SyncModel
  @AppStorage("runnerName") private var name = "跑者"
  var body: some View {
    Form {
      Section("个人资料") { TextField("称呼", text: $name) }
      Section {
        NavigationLink("健康数据同步") { WorkoutSyncView(model: sync) }.accessibilityIdentifier("profile.workout-sync")
        NavigationLink("同步与诊断") { SyncView(model: sync) }.accessibilityIdentifier("profile.sync")
        Button("重新连接并刷新") { Task { await model.reconnect() } }
      }
      Section("训练执行") {
        ForEach(model.executionIDs, id: \.self) { id in
          Button(id == model.execution.string("executionID") ? "查看最近执行" : "查看执行记录") {
            model.execution = .object(["executionID": .string(id)])
            Task { await model.refreshExecution() }
          }
        }
        if !model.execution.string("executionID").isEmpty {
          NavigationLink("执行与反馈") { ExecutionView(model: model) }
        }
      }
      if let pending = model.pending {
        Section("待确认的提交") {
          Text("提交结果尚未确认。重试会使用原命令标识。").font(.footnote)
          if model.conflict { Button("查看最新状态并解除冲突") { Task { await model.reviewConflict() } } }
          Button("恢复原提交") {
            Task {
              _ = await model.submit(pending)
              await model.refresh()
            }
          }.disabled(model.loading)
        }
      }
      #if DEBUG && targetEnvironment(simulator)
        if ProcessInfo.processInfo.environment["RAI_SYNTHETIC_ACCEPTANCE"] == "1" {
          Section("验收诊断") {
            Text(
              "图形加速=\(RAIRenderer.metalAvailable ? "可用" : "不可用")，动态效果=\(reduced ? "减弱" : "正常")，透明度=\(opaque ? "降低" : "正常")，字号=\(typeSize.isAccessibilitySize ? "辅助功能字号" : "标准字号")"
            )
            .accessibilityIdentifier("profile.a11y")
          }
        }
      #endif
      Section("关于") {
        Text("RAI · 原生跑步训练")
        Text("健康数据只读；训练排程暂未开放。").font(.footnote)
        Text("训练建议与负荷状态估算来自私有 RAI 服务。").font(.footnote)
      }
      if let error = model.error { Section { Text(error).foregroundStyle(RAITheme.amber) } }
    }.scrollContentBackground(.hidden).navigationTitle("我的").raiScreen().accessibilityIdentifier(
      "screen.profile")
  }
}

func trainingTag(_ type: String) -> String {
  [
    "REST": "恢复与休息", "EASY": "有氧耐力", "LONG": "长距离耐力", "TEMPO": "节奏耐力", "INTERVAL": "间歇训练",
    "RECOVERY": "低强度恢复",
  ][type] ?? "训练安排"
}

import ProductCore
import SwiftUI
import SyncCore

/// User-facing HealthKit Workout selection screen. One row is one HKWorkout;
/// selecting several only enqueues several independent one-Workout tasks.
struct WorkoutSyncView: View {
  @Bindable var model: SyncModel
  @State private var filter: Filter = .all
  @State private var detail: HealthWorkoutSummary?
  @State private var exportStatus: String?
  private enum Filter: String, CaseIterable { case all = "全部", pending = "未同步", failed = "失败", synced = "已同步" }

  var body: some View {
    ScrollView {
      LazyVStack(spacing: 12) {
        intro
        if model.loadingWorkouts { ProgressView("正在读取 HealthKit 跑步记录…").font(.caption) }
        selectionBar
        ForEach(filtered) { workout in
          row(workout)
        }
        if filtered.isEmpty && !model.loadingWorkouts {
          EmptyCard(title: "没有符合条件的记录", detail: "只显示真实 HealthKit 跑步记录；缺失数据以 -- 显示。")
        }
        if let error = model.error {
          Text(error).font(.footnote).foregroundStyle(RAITheme.amber)
        }
      }.padding(18)
    }
    .raiScreen()
    .navigationTitle("健康数据同步")
    .toolbar {
      #if DEBUG
      ToolbarItem(placement: .topBarLeading) {
        Button("导出证据") {
          exportStatus = model.exportAcceptanceEvidence()?.lastPathComponent
        }.disabled(model.busy)
      }
      #endif
      ToolbarItem(placement: .topBarTrailing) {
        Button("刷新") { Task { await model.loadWorkouts() } }.disabled(model.busy || model.loadingWorkouts)
      }
    }
    .refreshable { await model.loadWorkouts() }
    .task { if model.workouts.isEmpty { await model.loadWorkouts() } }
    .sheet(item: $detail) { workout in
      NavigationStack { WorkoutSyncDetail(model: model, workout: workout, status: status(for: workout)) }
    }
    .accessibilityIdentifier("screen.workout-sync")
  }

  private var intro: some View {
    VStack(alignment: .leading, spacing: 10) {
      HStack {
        Image(systemName: "square.stack.3d.up.fill").foregroundStyle(RAITheme.amber)
        Text("一条跑步 = 一个独立上传任务").font(.headline)
      }
      Text("进入此页不会自动上传。选择一条或多条真实 HealthKit 跑步记录后，每条记录独立读取、独立排队、独立确认；不会合并成一个大批次。")
        .font(.caption).foregroundStyle(RAITheme.secondary)
      HStack(spacing: 10) {
        Label(model.authorization, systemImage: "checkmark.shield").font(.caption2).foregroundStyle(RAITheme.secondary)
        Spacer()
        Button("请求读取权限") { model.authorize() }.font(.caption).disabled(model.busy)
      }
      if model.busy {
        HStack(spacing: 8) {
          ProgressView().controlSize(.small)
          Text(model.currentWorkoutID == nil ? model.status : "\(model.status) · \(shortID(model.currentWorkoutID ?? ""))")
            .font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if let exportStatus { Text("已导出：\(exportStatus)").font(.caption2).foregroundStyle(RAITheme.green) }
    }.padding(16).raiGlass()
  }

  private var selectionBar: some View {
    VStack(spacing: 10) {
      Picker("筛选", selection: $filter) {
        ForEach(Filter.allCases, id: \.self) { Text($0.rawValue).tag($0) }
      }.pickerStyle(.segmented)
      HStack {
        Button(model.selected.isEmpty ? "选择全部未同步" : "清除选择（\(model.selected.count)）") {
          if model.selected.isEmpty { model.selected = Set(model.workouts.filter { status(for: $0) == .notSynced }.map(\.id)) }
          else { model.selected.removeAll() }
        }.font(.caption).disabled(model.busy)
        Spacer()
        Button("同步所选") {
          let ids = model.workouts.map(\.id).filter { model.selected.contains($0) }
          model.syncWorkouts(ids)
        }
        .font(.caption.weight(.semibold))
        .disabled(model.busy || model.selected.isEmpty)
        .accessibilityIdentifier("sync.selected")
      }
    }.padding(14).raiGlass()
  }

  private func row(_ workout: HealthWorkoutSummary) -> some View {
    let phase = status(for: workout)
    let selected = model.selected.contains(workout.id)
    return VStack(alignment: .leading, spacing: 10) {
      HStack(alignment: .top, spacing: 10) {
        Button {
          if selected { model.selected.remove(workout.id) } else { model.selected.insert(workout.id) }
        } label: {
          Image(systemName: selected ? "checkmark.circle.fill" : "circle")
            .font(.title3).foregroundStyle(selected ? RAITheme.amber : RAITheme.secondary)
        }.buttonStyle(.plain).disabled(model.busy)
          .accessibilityIdentifier("sync.select")
        VStack(alignment: .leading, spacing: 3) {
          HStack(spacing: 6) {
            Text(workout.isIndoor == true ? "室内跑步" : workout.isIndoor == false ? "户外跑步" : "跑步（类型 --）").font(.headline)
            Text(workout.start, format: .dateTime.year().month().day().hour().minute()).font(.caption).foregroundStyle(RAITheme.secondary)
          }
          HStack(spacing: 12) {
            metric("距离", workout.distanceM == nil ? "--" : ProductFormat.distance(workout.distanceM) + " km")
            metric("时长", ProductFormat.duration(workout.durationS))
            metric("配速", workout.averagePaceSecondsPerM == nil ? "--" : ProductFormat.pace(workout.averagePaceSecondsPerM) + "/km")
            metric("心率", workout.averageHeartRate.map { String(format: "%.0f", $0) } ?? "--")
          }
        }
        Spacer(minLength: 0)
        statusBadge(phase)
      }
      HStack(spacing: 10) {
        Text(workout.sourceName ?? "--").font(.caption2).foregroundStyle(RAITheme.secondary)
        if let device = workout.deviceName { Text(device).font(.caption2).foregroundStyle(RAITheme.secondary) }
        Spacer()
        Button("详情") { detail = workout }.font(.caption2)
        switch phase {
        case .notSynced:
          Button("同步") { model.syncWorkouts([workout.id]) }.font(.caption.weight(.semibold)).disabled(model.busy)
        case .failed:
          Button("重试") { model.retry(workout.id) }.font(.caption.weight(.semibold)).disabled(model.busy)
        case .preparing, .uploading:
          Button("继续") { model.retry(workout.id) }.font(.caption2).disabled(model.busy)
        case .acknowledged, .analyzing:
          Button("检查状态") { model.checkStatus(workout.id) }.font(.caption2).disabled(model.busy)
          Button("更新") { model.syncWorkouts([workout.id]) }.font(.caption2).disabled(model.busy)
        case .published, .partial:
          Button("更新") { model.syncWorkouts([workout.id]) }.font(.caption2).disabled(model.busy)
        case .deleted:
          Text("HealthKit 已删除").font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
    }.padding(16).raiGlass()
      .overlay(RoundedRectangle(cornerRadius: RAITheme.radius).strokeBorder(selected ? RAITheme.amber.opacity(0.65) : .clear, lineWidth: 1))
      .accessibilityIdentifier("sync.workout.row")
  }

  private func metric(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 1) {
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(value).font(.caption.monospacedDigit()).foregroundStyle(.white)
    }
  }

  private func statusBadge(_ phase: WorkoutSyncPhase) -> some View {
    Text(statusLabel(phase)).font(.caption2.weight(.semibold))
      .padding(.horizontal, 8).padding(.vertical, 4)
      .background(statusColor(phase).opacity(0.18), in: Capsule())
      .foregroundStyle(statusColor(phase))
  }

  private var filtered: [HealthWorkoutSummary] {
    model.workouts.filter { workout in
      let phase = status(for: workout)
      switch filter {
      case .all: return true
      case .pending: return phase == .notSynced
      case .failed: return phase == .failed
      case .synced: return ![.notSynced, .failed, .deleted].contains(phase)
      }
    }
  }

  private func status(for workout: HealthWorkoutSummary) -> WorkoutSyncPhase {
    if let task = model.tasks[workout.id] { return task.phase }
    if model.serverPresent.contains(workout.id) {
      return model.serverStatuses[workout.id] == "published" ? .published : .acknowledged
    }
    return .notSynced
  }

  private func statusLabel(_ phase: WorkoutSyncPhase) -> String {
    [
      .notSynced: "未同步", .preparing: "准备中", .uploading: "上传中",
      .acknowledged: "服务器已确认", .analyzing: "分析中", .published: "已发布",
      .partial: "部分完成", .failed: "失败", .deleted: "已删除",
    ][phase] ?? phase.rawValue
  }

  private func statusColor(_ phase: WorkoutSyncPhase) -> Color {
    switch phase {
    case .notSynced: RAITheme.secondary
    case .preparing, .uploading: RAITheme.amber
    case .acknowledged, .analyzing: RAITheme.blue
    case .published: RAITheme.green
    case .partial: RAITheme.orange
    case .failed: Color.red
    case .deleted: RAITheme.secondary
    }
  }

  private func shortID(_ value: String) -> String { String(value.prefix(8)) }
}

struct WorkoutSyncDetail: View {
  @Bindable var model: SyncModel
  let workout: HealthWorkoutSummary
  let status: WorkoutSyncPhase
  @Environment(\.dismiss) private var dismiss
  private func phaseLabel(_ phase: WorkoutSyncPhase) -> String {
    [
      .notSynced: "未同步", .preparing: "准备中", .uploading: "上传中",
      .acknowledged: "服务器已确认", .analyzing: "分析中", .published: "已发布",
      .partial: "部分完成", .failed: "失败", .deleted: "已删除",
    ][phase] ?? phase.rawValue
  }

  var body: some View {
    Form {
      Section("记录") {
        LabeledContent("开始", value: workout.start.formatted(date: .abbreviated, time: .standard))
        LabeledContent("时长", value: ProductFormat.duration(workout.durationS))
        LabeledContent("距离", value: workout.distanceM == nil ? "--" : ProductFormat.distance(workout.distanceM) + " km")
        LabeledContent("平均配速", value: workout.averagePaceSecondsPerM == nil ? "--" : ProductFormat.pace(workout.averagePaceSecondsPerM) + "/km")
        LabeledContent("平均心率", value: workout.averageHeartRate.map { String(format: "%.0f bpm", $0) } ?? "--")
        LabeledContent("状态", value: phaseLabel(status))
      }
      if let task = model.tasks[workout.id] {
        Section("独立同步任务") {
          LabeledContent("阶段", value: phaseLabel(task.phase))
          LabeledContent("创建", value: task.createdAt.formatted(date: .abbreviated, time: .standard))
          if let acknowledged = task.acknowledgedAt {
            LabeledContent("Durable ACK", value: acknowledged.formatted(date: .abbreviated, time: .standard))
          }
          if let error = task.errorCode { LabeledContent("错误", value: error) }
          ForEach(task.missing, id: \.self) { Text($0).font(.caption) }
        }
        Section("Coverage（缺失不等于 0）") {
          if task.coverage.isEmpty { Text("尚无读取结果").font(.caption) }
          ForEach(task.coverage.keys.sorted(), id: \.self) { key in
            let item = task.coverage[key]!
            LabeledContent(key) {
              Text("\(item.stateText) · \(item.sampleCount)").font(.caption.monospacedDigit())
            }
          }
        }
        Section("分阶段耗时（毫秒）") {
          ForEach(task.metrics.keys.sorted(), id: \.self) { key in
            LabeledContent(key, value: String(format: "%.1f", task.metrics[key]!))
          }
        }
      }
      Section {
        Button("检查服务端状态") { model.checkStatus(workout.id) }.disabled(model.busy)
        Button("重新同步（检查迟到数据）") { model.syncWorkouts([workout.id]) }.disabled(model.busy)
      }
    }
    .navigationTitle("同步详情")
    .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("关闭") { dismiss() } } }
    .scrollContentBackground(.hidden).raiScreen()
  }
}
