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
    }.tint(RAITheme.orange)
      .task { await model.refresh() }
      .onChange(of: sync.activeSessionID) { _, _ in model = ProductModel(); Task { await model.refresh() } }
      .onChange(of: phase) { _, value in if value == .active { Task { await model.refresh() } } }
      .task(id: phase) {
        guard phase == .active else { return }
        while !Task.isCancelled {
          try? await Task.sleep(for: .seconds(8))
          guard !Task.isCancelled else { return }
          await model.refreshAthleteAssignment()
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
          if let athlete = model.data.athlete, athlete.supportsPresentation {
            AthleteModelCard(snapshot: athlete, summary: true)
          } else {
            BodyStatusCard(data: model.data)
          }
        }.buttonStyle(RAIPressStyle())
          .accessibilityIdentifier(model.data.athlete?.supportsPresentation == true ? "model.home.active" : "home.status")
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
  @State private var showingModelInput = false
  var body: some View {
    ScrollView {
      VStack(spacing: 16) {
        BodyStatusCard(data: model.data)
        NavigationLink {
          TrainingLoadDetailView(model: model)
        } label: {
          TrainingLoadEntryCard(
            data: model.data, points: TrainingLoadSeries.points(from: model.data.history))
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("trainingLoad.entry")
        NavigationLink {
          RunningAbilityDetailView(model: model)
        } label: {
          RunningAbilityEntryCard(snapshot: model.data.athlete)
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("runningAbility.entry")
        if let athlete = model.data.athlete, athlete.supportsPresentation {
          AthleteModelCard(snapshot: athlete)
          NavigationLink("运动模型历史") { AthleteHistoryView(model: model) }.accessibilityIdentifier("model.history.link")
        }
        Button {
          showingModelInput = true
        } label: {
          Label("填写个人参考与目标", systemImage: "square.and.pencil")
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(RAIPressStyle())
        .disabled(!model.configured)
        .accessibilityHint("填写静息心率、最大心率或睡眠目标；保存为服务器上的追加式个人输入")
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
        Text("模型结果来自已发布的分析快照；缺失、过期或同步未完成保持空白，不补零。RAI VO₂ 为实验性次最大强度估计，Apple 外部估计独立展示，二者不相加或互相校准。").font(.footnote)
          .foregroundStyle(RAITheme.secondary)
      }.padding(18)
    }.navigationTitle("分析").raiScreen().refreshable { await model.refresh() }
      .accessibilityIdentifier("screen.analysis")
      .sheet(isPresented: $showingModelInput) { ModelInputSheet(model: model) }
  }
}

private struct ModelInputSheet: View {
  @Bindable var model: ProductModel
  @Environment(\.dismiss) private var dismiss
  @State private var kind = "hr_reference"
  @State private var maxHR = ""
  @State private var restingHR = ""
  @State private var sleepHours = "8"
  @State private var sourceEpoch = ""
  @State private var selectionRevoked = false
  @State private var effectiveDate = Date()
  @State private var busy = false
  @State private var message: String?

  var body: some View {
    NavigationStack {
      Form {
        Section("输入类型") {
          Picker("类型", selection: $kind) {
            Text("心率参考").tag("hr_reference")
            Text("睡眠目标").tag("sleep_target")
            Text("VO₂ 来源").tag("vo2max_source_selection")
          }.pickerStyle(.segmented)
        }
        Section("生效日期") { DatePicker("日期", selection: $effectiveDate, displayedComponents: .date) }
        if kind == "hr_reference" {
          Section("心率参考（bpm）") {
            TextField("最大心率", text: $maxHR)
            TextField("静息心率（可选）", text: $restingHR)
            Text("只接受 30–219 bpm；最大心率必须高于静息心率。不会按年龄推断。")
              .font(.caption).foregroundStyle(RAITheme.secondary)
            Text("这里的心率参考只用于能力模型。训练负荷（TRIMP）请在「我的 → 个人设置 → 静息与最大心率」填写。")
              .font(.caption).foregroundStyle(RAITheme.secondary)
          }
        } else if kind == "sleep_target" {
          Section("睡眠目标（小时）") {
            TextField("目标睡眠时长", text: $sleepHours)
            Text("必须在 0–24 小时之间；用于恢复模型的睡眠目标组件。")
              .font(.caption).foregroundStyle(RAITheme.secondary)
          }
        } else {
          Section("Apple 外部 VO₂ 来源") {
            TextField("来源 epoch（sha256:…）", text: $sourceEpoch)
              .textInputAutocapitalization(.never)
              .autocorrectionDisabled()
            Toggle("追加撤销（不再选择该来源）", isOn: $selectionRevoked)
            Text("只能追加显式来源选择；同一时间仅有一个有效来源，多个有效选择会保持冲突而不自动挑选。")
              .font(.caption).foregroundStyle(RAITheme.secondary)
          }
        }
        if model.pendingModelInput != nil {
          Button("重试待确认的提交") {
            busy = true
            Task {
              if let failure = await model.retryModelInput() { message = failure } else { dismiss() }
              busy = false
            }
          }.disabled(busy)
        }
        if let message { Text(message).foregroundStyle(.red).font(.footnote) }
        Section {
          Button(busy ? "保存中…" : "保存为个人输入") { submit() }
            .disabled(busy || !valid)
        }
      }
      .navigationTitle("个人参考与目标")
      .toolbar { ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } } }
    }
  }

  private var valid: Bool {
    if kind == "hr_reference" {
      guard let max = Double(maxHR), max >= 30, max < 220 else { return false }
      if !restingHR.isEmpty {
        guard let rest = Double(restingHR), rest >= 30, rest < max else { return false }
      }
      return true
    }
    if kind == "vo2max_source_selection" {
      return trimmedSourceEpoch.range(
        of: "^sha256:[0-9a-f]{64}$", options: .regularExpression) != nil
    }
    guard let hours = Double(sleepHours) else { return false }
    return hours > 0 && hours <= 24
  }

  private var trimmedSourceEpoch: String {
    sourceEpoch.trimmingCharacters(in: .whitespacesAndNewlines)
  }

  private func submit() {
    busy = true
    message = nil
    Task {
      let payload: [String: JSON]
      if kind == "hr_reference" {
        var fields: [String: JSON] = [
          "maxHR": .number(Double(maxHR) ?? 0),
          "method": .string("user_entered"),
          "measuredAt": .string(ISO8601DateFormatter().string(from: effectiveDate)),
          "unit": .string("bpm"),
        ]
        if let rest = Double(restingHR), rest > 0 {
          fields["restingHR"] = .number(rest)
          fields["restingMeasuredAt"] = .string(ISO8601DateFormatter().string(from: effectiveDate))
        }
        payload = fields
      } else if kind == "vo2max_source_selection" {
        payload = [
          "sourceEpoch": .string(trimmedSourceEpoch),
          "revoked": .bool(selectionRevoked),
        ]
      } else {
        payload = ["hours": .number(Double(sleepHours) ?? 0)]
      }
      let failure = await model.submitModelInput(
        kind: kind, payload: payload, effectiveFrom: effectiveDate)
      await MainActor.run {
        busy = false
        if let failure { message = failure } else { dismiss() }
      }
    }
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
struct AthleteModelCard: View {
  let snapshot: AthleteModelSnapshot
  var summary = false
  @Environment(\.dynamicTypeSize) private var typeSize
  var body: some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "运动能力模型", icon: "figure.run.circle")
      row("训练准备度", value: snapshot.trainingReadiness.value?.score, unit: "分", metric: snapshot.trainingReadiness.envelope, digits: 0)
      if snapshot.trainingReadiness.envelope.presentationValueAvailable,
         let notice = ProductReasonText.lowReadinessNotice(score: snapshot.trainingReadiness.value?.score) {
        Text(notice).font(.caption).foregroundStyle(RAITheme.secondary).accessibilityIdentifier("model.readiness.lowHint")
      }
      row("跑力", value: snapshot.runningAbility.value?.score, unit: "分", metric: snapshot.runningAbility.envelope, digits: 0)
      if !summary {
      row("生理恢复", value: snapshot.physiologicalRecovery.value?.score, unit: "分", metric: snapshot.physiologicalRecovery.envelope, digits: 0)
      row("急性负荷", value: snapshot.trainingLoad.value?.acute, unit: "AU", metric: snapshot.trainingLoad.envelope, digits: 0)
      if let rai = snapshot.raiVO2Max {
        row(rai.value?.experimental == true ? "RAI VO₂（" + ProductReasonText.experimentalLabel + "）" : "RAI VO₂", value: rai.value?.value, unit: "mL/kg/min", metric: rai.envelope, digits: 1)
      }
      row("Apple 外部 VO₂", value: snapshot.externalVO2Max.value?.value, unit: "mL/kg/min", metric: snapshot.externalVO2Max.envelope, digits: 1)
      Text(ProductReasonText.independenceNotice).font(.caption2).foregroundStyle(RAITheme.secondary)
      }
    }.padding(18).raiGlass()
      .accessibilityIdentifier("model.card")
  }
  @ViewBuilder private func row(_ title: String, value: Double?, unit: String, metric: MetricEnvelope, digits: Int) -> some View {
    let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading)) : AnyLayout(HStackLayout())
    layout {
      Text(title).font(.subheadline)
      if !typeSize.isAccessibilitySize { Spacer(minLength: 8) }
      if metric.presentationValueAvailable, let value, value.isFinite {
        Text(ProductFormat.decimal(value, digits: digits) + " " + unit).monospacedDigit()
      } else {
        Text(AthleteModelCard.reason(metric)).font(.caption).foregroundStyle(RAITheme.secondary)
      }
    }.accessibilityElement(children: .combine)
  }
  static func reason(_ metric: MetricEnvelope) -> String {
    ProductReasonText.text(for: metric.productReasonCodes.first?.rawValue,
                           state: metric.presentationState.rawValue)
  }
}

struct AthleteHistoryView: View {
  @Bindable var model: ProductModel
  @State private var metric: String
  @State private var page: AthleteHistoryPage?
  @State private var failed = false

  init(model: ProductModel, initialMetric: String = "trainingLoad") {
    self.model = model
    _metric = State(initialValue: initialMetric)
  }

  var body: some View {
    List {
      Picker("指标", selection: $metric) {
        Text("急性负荷").tag("trainingLoad")
        Text("跑力").tag("runningAbility")
        Text("生理恢复").tag("physiologicalRecovery")
        Text("训练准备度").tag("trainingReadiness")
        Text("RAI VO₂（实验）").tag("raiVO2Max")
        Text("Apple 外部 VO₂").tag("externalVO2Max")
      }
      if let page, page.metric == metric, page.assignment == model.data.athlete?.assignment {
        if page.points.isEmpty { Text("此时段没有已发布的历史结果") }
        ForEach(page.points) { point in
          LabeledContent(point.date, value: display(point)).accessibilityIdentifier("model.history.point")
        }
      } else if failed {
        Text("历史暂不可用")
      } else {
        ProgressView("正在读取历史")
      }
    }
    .navigationTitle("运动模型历史")
    .task(id: metric + ":" + String(model.data.athlete?.assignment.version ?? 0)) {
      failed = false
      page = nil
      let requested = metric
      do {
        let response = try await model.loadAthleteHistory(metric: requested)
        if !Task.isCancelled, requested == metric, response.assignment == model.data.athlete?.assignment {
          page = response
        }
      } catch {
        if !Task.isCancelled { failed = true }
      }
    }
  }

  private func display(_ p: AthleteHistoryPoint) -> String {
    guard ["available", "partial", "preliminary", "established"].contains(p.state),
      let value = p.value, value.isFinite else { return "暂无数值" }
    switch p.unit {
    case "score_0_100": return ProductFormat.decimal(value) + " 分"
    case "AU/day": return ProductFormat.decimal(value, digits: 1) + " AU/日"
    case "mL/kg/min": return ProductFormat.decimal(value, digits: 1) + " mL/kg/min"
    default: return "暂不支持此单位"
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
        NavigationLink("静息与最大心率") { HeartRateSettingsView(model: model) }
          .accessibilityIdentifier("profile.hr-settings")
      } header: {
        Text("个人设置")
      } footer: {
        Text("用于训练负荷（TRIMP）的个人心率与系数；只追加新版本，不推断年龄或性别。")
      }
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

struct HeartRateSettingsView: View {
  @Bindable var model: ProductModel
  @Environment(\.dismiss) private var dismiss
  @State private var restingHR = ""
  @State private var maxHR = ""
  @State private var coefficientA = "0.64"
  @State private var coefficientB = "1.92"
  @State private var effectiveDate = Date()
  @State private var current: JSON?
  @State private var loading = true
  @State private var busy = false
  @State private var message: String?

  var body: some View {
    Form {
      Section("当前已保存") {
        if let current {
          LabeledContent("参数版本", value: "#\(current["version"].integer ?? 0)")
          LabeledContent("静息心率", value: bpm(current.number("restingHR")))
          LabeledContent("最大心率", value: bpm(current.number("maxHR")))
          LabeledContent("系数 A / B", value: coefficients(current))
          if !current.string("provenance").isEmpty {
            LabeledContent("来源", value: current.string("provenance"))
          }
        } else if loading {
          ProgressView()
        } else {
          Text("尚未保存个人参数，训练负荷会保持不足。").font(.footnote)
            .foregroundStyle(RAITheme.secondary)
        }
      }
      Section {
        TextField("静息心率", text: $restingHR)
          .keyboardType(.numberPad)
          .accessibilityIdentifier("hr-settings.resting")
        TextField("最大心率", text: $maxHR)
          .keyboardType(.numberPad)
          .accessibilityIdentifier("hr-settings.max")
      } header: {
        Text("心率（bpm）")
      } footer: {
        Text("两项都必须在 30–219 bpm 之间，且最大心率高于静息心率。只使用你明确填写的数值，不会按年龄或性别推断。")
      }
      Section {
        TextField("系数 A", text: $coefficientA)
          .keyboardType(.decimalPad)
          .accessibilityIdentifier("hr-settings.coefficient-a")
        TextField("系数 B", text: $coefficientB)
          .keyboardType(.decimalPad)
          .accessibilityIdentifier("hr-settings.coefficient-b")
      } header: {
        Text("TRIMP 系数（高级）")
      } footer: {
        Text("Banister 系数 A、B 必须显式填写，范围 (0, 10]。界面显示的 0.64 / 1.92 是常用起点，请按个人情况确认；服务端不会补默认值。")
      }
      Section {
        DatePicker("生效日期", selection: $effectiveDate, displayedComponents: .date)
        if model.pendingModelInput != nil {
          Button("重试待确认的提交") {
            busy = true
            Task {
              if let failure = await model.retryModelInput() {
                message = failure
              } else {
                await reload()
              }
              busy = false
            }
          }.disabled(busy)
        }
        if let message { Text(message).foregroundStyle(.red).font(.footnote) }
        Button(busy ? "保存中…" : "保存并重算最近跑步") { submit() }
          .disabled(busy || !valid)
          .accessibilityIdentifier("hr-settings.save")
      } footer: {
        Text("保存后服务端会追加一个新参数版本，并重新排队最近 179 天的跑步分析（最多 1000 条）；历史参数不会被改写。")
      }
    }
    .navigationTitle("静息与最大心率")
    .raiScreen()
    .accessibilityIdentifier("screen.hr-settings")
    .task { await reload() }
  }

  private var valid: Bool {
    guard let rest = Double(restingHR), let max = Double(maxHR),
      rest >= 30, rest < 220, max >= 30, max < 220, max > rest,
      let a = Double(coefficientA), let b = Double(coefficientB),
      a > 0, a <= 10, b > 0, b <= 10
    else { return false }
    return true
  }

  private func bpm(_ value: Double?) -> String {
    guard let value else { return "--" }
    return String(format: "%.0f bpm", value)
  }

  private func coefficients(_ value: JSON) -> String {
    guard let a = value.number("coefficientA"), let b = value.number("coefficientB") else {
      return "--"
    }
    return String(format: "%.2f / %.2f", a, b)
  }

  private func reload() async {
    loading = true
    let loaded = await model.loadAnalysisParameters()
    current = loaded
    if let loaded {
      restingHR = text(loaded.number("restingHR"))
      maxHR = text(loaded.number("maxHR"))
      if loaded.number("coefficientA") != nil { coefficientA = text(loaded.number("coefficientA")) }
      if loaded.number("coefficientB") != nil { coefficientB = text(loaded.number("coefficientB")) }
    }
    loading = false
  }

  private func text(_ value: Double?) -> String {
    guard let value else { return "" }
    return value == value.rounded() ? String(format: "%.0f", value) : String(format: "%g", value)
  }

  private func submit() {
    guard valid, let rest = Double(restingHR), let max = Double(maxHR),
      let a = Double(coefficientA), let b = Double(coefficientB)
    else { return }
    busy = true
    message = nil
    Task {
      let failure = await model.submitModelInput(
        kind: "trimp_parameters",
        payload: [
          "restingHR": .number(rest), "maxHR": .number(max),
          "coefficientA": .number(a), "coefficientB": .number(b),
        ],
        effectiveFrom: effectiveDate)
      await MainActor.run {
        busy = false
        if let failure {
          message = failure
        } else {
          dismiss()
        }
      }
    }
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
  @State private var showExcluded = false
  private enum Filter: String, CaseIterable { case all = "全部", pending = "未同步", failed = "失败", synced = "已同步" }

  var body: some View {
    ScrollView {
      LazyVStack(spacing: 12) {
        intro
        if model.loadingWorkouts { ProgressView("正在读取 HealthKit 跑步记录…").font(.caption) }
        selectionBar
        sourcePolicyNote
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
          if model.selected.isEmpty { model.selected = Set(model.workouts.filter { $0.isEligible && status(for: $0) == .notSynced }.map(\.id)) }
          else { model.selected.removeAll() }
        }.font(.caption).disabled(!model.canStartWorkoutSync)
        Spacer()
        Button("同步所选") {
          let ids = model.workouts.map(\.id).filter { model.selected.contains($0) }
          model.syncWorkouts(ids)
        }
        .font(.caption.weight(.semibold))
        .disabled(!model.canStartWorkoutSync || model.selected.isEmpty)
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
        }.buttonStyle(.plain).disabled(!model.canStartWorkoutSync || !workout.isEligible)
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
        if workout.isEligible { statusBadge(phase) } else {
          Text(workout.isExcluded ? "已排除" : "待识别").font(.caption2.weight(.semibold))
            .padding(.horizontal, 8).padding(.vertical, 4)
            .background(RAITheme.secondary.opacity(0.18), in: Capsule())
            .foregroundStyle(RAITheme.secondary)
        }
      }
      HStack(spacing: 10) {
        Text(workout.sourceName ?? "--").font(.caption2).foregroundStyle(RAITheme.secondary)
        if let device = workout.deviceName { Text(device).font(.caption2).foregroundStyle(RAITheme.secondary) }
        Spacer()
        Button("详情") { detail = workout }.font(.caption2)
        if !workout.isEligible {
          Text(workout.isExcluded ? "非 Apple Watch 原生来源，不上传" : "来源待识别，暂不上传")
            .font(.caption2).foregroundStyle(RAITheme.secondary)
        } else {
        switch phase {
        case .notSynced:
          Button("同步") { model.syncWorkouts([workout.id]) }.font(.caption.weight(.semibold)).disabled(!model.canStartWorkoutSync)
        case .failed:
          Button("重试") { model.retry(workout.id) }.font(.caption.weight(.semibold)).disabled(!model.canStartWorkoutSync)
        case .preparing, .uploading:
          Button("继续") { model.retry(workout.id) }.font(.caption2).disabled(!model.canStartWorkoutSync)
        case .accepted:
          // The exact payload is durably accepted; re-sending cannot change the outcome.
          Button("检查状态") { model.checkStatus(workout.id) }.font(.caption2).disabled(model.busy)
        case .acknowledged, .analyzing:
          Button("检查状态") { model.checkStatus(workout.id) }.font(.caption2).disabled(model.busy)
          Button("更新") { model.syncWorkouts([workout.id]) }.font(.caption2).disabled(!model.canStartWorkoutSync)
        case .published, .partial:
          Button("更新") { model.syncWorkouts([workout.id]) }.font(.caption2).disabled(!model.canStartWorkoutSync)
        case .deleted:
          Text("HealthKit 已删除").font(.caption2).foregroundStyle(RAITheme.secondary)
        }
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
      // A record the frozen source policy excludes is never offered for sync.
      // It stays visible only on request, with a plain-language reason.
      if workout.isExcluded && !showExcluded { return false }
      let phase = status(for: workout)
      switch filter {
      case .all: return true
      case .pending: return phase == .notSynced
      case .failed: return phase == .failed
      case .synced: return ![.notSynced, .failed, .deleted].contains(phase)
      }
    }
  }
  /// Plain-language source-policy summary. No bundle identifier, hash, stack
  /// trace or internal error name reaches this line.
  private var sourcePolicyNote: some View {
    let excluded = model.workouts.filter(\.isExcluded).count
    let pending = model.workouts.filter(\.isUnclassified).count
    return Group {
      if excluded > 0 || pending > 0 {
        VStack(alignment: .leading, spacing: 6) {
          if excluded > 0 {
            HStack(spacing: 6) {
              Image(systemName: "applewatch.slash").font(.caption2).foregroundStyle(RAITheme.secondary)
              Text("已隐藏 \(excluded) 条非 Apple Watch 原生跑步记录（第三方 App 或手工录入）")
                .font(.caption2).foregroundStyle(RAITheme.secondary)
              Spacer(minLength: 0)
              Button(showExcluded ? "收起" : "查看") { showExcluded.toggle() }.font(.caption2)
            }
          }
          if pending > 0 {
            Text("\(pending) 条记录的来源暂无法确认，已保留在本机待识别，不会上传")
              .font(.caption2).foregroundStyle(RAITheme.secondary)
          }
        }.padding(12).raiGlass()
          .accessibilityIdentifier("sync.sourcePolicyNote")
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
      .notSynced: "未同步", .preparing: "正在准备", .uploading: "正在上传",
      .accepted: "已接收，等待入库", .acknowledged: "已入库，等待分析", .analyzing: "分析中",
      .published: "分析可查看", .partial: "部分完成", .failed: "失败", .deleted: "已删除",
    ][phase] ?? phase.rawValue
  }

  private func statusColor(_ phase: WorkoutSyncPhase) -> Color {
    switch phase {
    case .notSynced: RAITheme.secondary
    case .preparing, .uploading: RAITheme.amber
    case .accepted, .acknowledged, .analyzing: RAITheme.blue
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
      .notSynced: "未同步", .preparing: "正在准备", .uploading: "正在上传",
      .accepted: "已接收，等待入库", .acknowledged: "已入库，等待分析", .analyzing: "分析中",
      .published: "分析可查看", .partial: "部分完成", .failed: "失败", .deleted: "已删除",
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
          if let accepted = task.ingestAcceptedAt {
            LabeledContent("服务端已接收", value: accepted.formatted(date: .abbreviated, time: .standard))
          }
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
        Button("重新同步（检查迟到数据）") { model.syncWorkouts([workout.id]) }.disabled(!model.canStartWorkoutSync)
      }
    }
    .navigationTitle("同步详情")
    .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("关闭") { dismiss() } } }
    .scrollContentBackground(.hidden).raiScreen()
  }
}

// MARK: - 跑力

struct RunningAbilityEntryCard: View {
  let snapshot: AthleteModelSnapshot?

  var body: some View {
    let metric = snapshot?.runningAbility
    HStack(spacing: 16) {
      ZStack {
        Circle().stroke(.white.opacity(0.14), lineWidth: 7)
        if let score = metric?.presentationValue?.score {
          Circle()
            .trim(from: 0, to: max(0, min(score / 100, 1)))
            .stroke(RAITheme.orange.gradient, style: StrokeStyle(lineWidth: 7, lineCap: .round))
            .rotationEffect(.degrees(-90))
        }
        Text(ProductFormat.decimal(metric?.presentationValue?.score))
          .font(.system(.title2, design: .rounded, weight: .bold)).monospacedDigit()
      }.frame(width: 64, height: 64)
      VStack(alignment: .leading, spacing: 4) {
        HStack(spacing: 6) {
          Text("跑力").font(.headline)
          Text(runningAbilityStateText(metric?.envelope))
            .font(.caption2)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .background(.white.opacity(0.10), in: Capsule())
            .foregroundStyle(RAITheme.secondary)
        }
        Text(runningAbilitySubtitle(metric))
          .font(.caption).foregroundStyle(RAITheme.secondary).lineLimit(2)
      }
      Spacer(minLength: 0)
      Image(systemName: "chevron.right").font(.caption).foregroundStyle(RAITheme.secondary)
    }.padding(16).raiGlass()
  }

  private func runningAbilitySubtitle(_ metric: RunningAbilityMetric?) -> String {
    guard let metric else { return "配速区间、成绩预测与能力分解" }
    guard let value = metric.presentationValue else {
      return ProductReasonText.text(
        for: metric.envelope.productReasonCodes.first?.rawValue,
        state: metric.envelope.presentationState.rawValue)
    }
    guard value.speedUnit.rawValue == "m/s",
      let pace = RunningAbilityMath.paceSecondsPerKm(v60: value.v60)
    else { return "查看跑力详情" }
    return "V60 配速 \(runningPaceText(pace))/km · 配速区间与成绩预测"
  }
}

struct RunningAbilityDetailView: View {
  @Bindable var model: ProductModel

  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 16) {
        if let snapshot = model.data.athlete, snapshot.supportsPresentation {
          let metric = snapshot.runningAbility
          hero(metric)
          if let value = metric.presentationValue {
            abilityGrid(value)
            paceZones(value)
            racePredictions(value)
            evidence(metric)
          } else {
            EmptyCard(
              title: "跑力暂不可用",
              detail: ProductReasonText.text(
                for: metric.envelope.productReasonCodes.first?.rawValue,
                state: metric.envelope.presentationState.rawValue))
          }
          researchDiagnostics()
          NavigationLink("跑力历史") {
            AthleteHistoryView(model: model, initialMetric: "runningAbility")
          }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("runningAbility.history")
        } else {
          EmptyCard(
            title: "跑力尚未启用",
            detail: model.configured
              ? "当前服务已连接，但运动模型尚未激活。完成产品激活门禁后，跑力会自动出现在这里。"
              : "请先在「我的 → 同步与诊断」连接私有服务。")
        }
        Text("跑力由 60 分钟临界速度与耐力保持能力构成；配速区间与成绩预测为训练参考，不是比赛保证。")
          .font(.footnote).foregroundStyle(RAITheme.secondary)
      }.padding(18)
    }.navigationTitle("跑力").raiScreen().refreshable { await model.refresh() }
      .task { await model.loadAthleteDiagnostics() }
      .accessibilityIdentifier("screen.runningAbility")
  }

  private func hero(_ metric: RunningAbilityMetric) -> some View {
    VStack(alignment: .leading, spacing: 14) {
      HStack(alignment: .top) {
        VStack(alignment: .leading, spacing: 4) {
          Text("跑力").font(.subheadline).foregroundStyle(RAITheme.secondary)
          HStack(alignment: .firstTextBaseline, spacing: 4) {
            Text(ProductFormat.decimal(metric.presentationValue?.score))
              .font(.system(size: 64, weight: .bold, design: .rounded)).monospacedDigit()
              .accessibilityIdentifier("runningAbility.score")
            Text("分").font(.title3).foregroundStyle(RAITheme.secondary)
          }
        }
        Spacer(minLength: 8)
        VStack(alignment: .trailing, spacing: 6) {
          Text(runningAbilityStateText(metric.envelope))
            .font(.caption).padding(.horizontal, 8).padding(.vertical, 4)
            .background(.white.opacity(0.10), in: Capsule())
          if let confidence = metric.envelope.confidence {
            Text("证据置信 \(Int((confidence * 100).rounded()))%")
              .font(.caption2).foregroundStyle(RAITheme.secondary)
          }
        }
      }
      if let value = metric.presentationValue {
        HStack(alignment: .top, spacing: 18) {
          heroFact("V60 配速", heroV60Pace(value))
          heroFact("耐力保持", runningRetentionText(value.enduranceRetention))
          heroFact("稳定性", runningStabilityText(value.stability.rawValue))
        }
      }
    }.padding(20).raiGlass()
  }

  private func heroV60Pace(_ value: RunningAbilityValue) -> String {
    guard value.speedUnit.rawValue == "m/s",
      let pace = RunningAbilityMath.paceSecondsPerKm(v60: value.v60)
    else { return "--" }
    return runningPaceText(pace) + "/km"
  }

  private func heroFact(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 3) {
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(value).font(.subheadline.weight(.semibold)).monospacedDigit()
        .lineLimit(1).minimumScaleFactor(0.7)
    }.frame(maxWidth: .infinity, alignment: .leading)
  }

  private func abilityGrid(_ value: RunningAbilityValue) -> some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "能力分解", icon: "chart.bar.doc.horizontal")
      LazyVGrid(
        columns: [GridItem(.flexible(), spacing: 12), GridItem(.flexible(), spacing: 12)],
        spacing: 12
      ) {
        abilityTile(
          "V60 临界速度",
          value: value.v60.map {
            ProductFormat.decimal($0, digits: 2) + " "
              + (value.speedUnit.rawValue.isEmpty ? "m/s" : value.speedUnit.rawValue)
          },
          detail: heroV60Pace(value))
        abilityTile(
          "耐力指数 γ", value: value.gamma.map { ProductFormat.decimal($0, digits: 3) },
          detail: "越高越能维持速度")
        abilityTile(
          "耐力保持", value: runningRetentionText(value.enduranceRetention),
          detail: "长距离后的速度保留")
        abilityTile(
          "稳定性", value: runningStabilityText(value.stability.rawValue),
          detail: "近期表现波动")
      }
    }.padding(18).raiGlass()
  }

  private func abilityTile(_ title: String, value: String?, detail: String) -> some View {
    VStack(alignment: .leading, spacing: 6) {
      Text(title).font(.caption).foregroundStyle(RAITheme.secondary)
      Text(value ?? "--").font(.title3.weight(.semibold)).monospacedDigit()
        .lineLimit(1).minimumScaleFactor(0.7)
      Text(detail).font(.caption2).foregroundStyle(RAITheme.secondary).lineLimit(1)
    }.frame(maxWidth: .infinity, alignment: .leading).padding(12)
      .background(.white.opacity(0.05), in: RoundedRectangle(cornerRadius: 14))
  }

  private func paceZones(_ value: RunningAbilityValue) -> some View {
    let zones = value.speedUnit.rawValue == "m/s"
      ? RunningAbilityMath.paceZones(v60: value.v60) : []
    return VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "配速区间", icon: "speedometer")
      if zones.isEmpty {
        Text("服务未提供可用的 V60 速度，暂不推导配速区间。")
          .font(.caption).foregroundStyle(RAITheme.secondary)
      } else {
        ForEach(zones) { zone in
          HStack(spacing: 12) {
            Text(zone.code).font(.caption.bold())
              .frame(width: 26, height: 26)
              .background(runningZoneColor(zone.code).opacity(0.22), in: Circle())
              .foregroundStyle(runningZoneColor(zone.code))
            VStack(alignment: .leading, spacing: 2) {
              Text(zone.name).font(.subheadline.weight(.semibold))
              Text(zone.purpose).font(.caption2).foregroundStyle(RAITheme.secondary)
            }
            Spacer(minLength: 4)
            Text("\(runningPaceText(zone.fastSecondsPerKm))–\(runningPaceText(zone.slowSecondsPerKm))")
              .font(.subheadline.monospacedDigit()).foregroundStyle(.white.opacity(0.90))
            Text("/km").font(.caption2).foregroundStyle(RAITheme.secondary)
          }.padding(.vertical, 2)
        }
      }
    }.padding(18).raiGlass()
      .accessibilityIdentifier("runningAbility.zones")
  }

  private func racePredictions(_ value: RunningAbilityValue) -> some View {
    let items = value.speedUnit.rawValue == "m/s"
      ? RunningAbilityMath.racePredictions(v60: value.v60) : []
    return VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "成绩预测", icon: "flag.checkered")
      if items.isEmpty {
        Text("服务未提供可用的 V60 速度，暂不推导成绩预测。")
          .font(.caption).foregroundStyle(RAITheme.secondary)
      } else {
        ForEach(items) { item in
          HStack(alignment: .firstTextBaseline) {
            Text(item.name).font(.subheadline)
            Spacer(minLength: 8)
            Text(runningRaceTimeText(item.seconds)).font(.headline.monospacedDigit())
            Text(runningPaceText(item.seconds / (item.distanceM / 1000)) + "/km")
              .font(.caption).foregroundStyle(RAITheme.secondary)
          }
        }
      }
      Text("按 Riegel 耐力模型由 V60 外推；距离越远，误差越大。")
        .font(.caption2).foregroundStyle(RAITheme.secondary)
    }.padding(18).raiGlass()
      .accessibilityIdentifier("runningAbility.predictions")
  }

  private func evidence(_ metric: RunningAbilityMetric) -> some View {
    VStack(alignment: .leading, spacing: 10) {
      CardHeading(title: "证据与限制", icon: "checkmark.seal")
      evidenceRow("证据模式", runningEvidenceModeText(metric.value?.evidenceMode.rawValue))
      evidenceRow("有效跑步", "\(metric.value?.evidenceCount ?? 0) 次")
      evidenceRow("数据质量", runningQualityText(metric.envelope.quality.rawValue))
      evidenceRow("新鲜度", runningFreshnessText(metric.envelope.freshness.rawValue))
      ForEach(metric.envelope.productReasonCodes, id: \.rawValue) { code in
        Text(ProductReasonText.text(
          for: code.rawValue, state: metric.envelope.presentationState.rawValue))
          .font(.caption).foregroundStyle(RAITheme.amber)
      }
    }.padding(18).raiGlass()
  }

  private func evidenceRow(_ title: String, _ value: String) -> some View {
    HStack {
      Text(title).font(.caption).foregroundStyle(RAITheme.secondary)
      Spacer()
      Text(value).font(.caption).monospacedDigit()
    }
  }

  private func researchDiagnostics() -> some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "研究诊断", icon: "waveform.path.ecg")
        .accessibilityIdentifier("runningAbility.diagnostics")
      if let diagnostics = model.diagnostics, diagnostics.supportsPresentation {
        if diagnostics.items.isEmpty {
          Text("研究诊断尚未随当前模型日发布。")
            .font(.caption).foregroundStyle(RAITheme.secondary)
            .accessibilityIdentifier("runningAbility.diagnostics.empty")
        } else {
          ForEach(diagnostics.items) { item in researchRow(item) }
        }
        Text("研究诊断独立版本化，仅供趋势参考，不进入跑力或训练负荷产品指标。")
          .font(.caption2).foregroundStyle(RAITheme.secondary)
      } else if let error = model.diagnosticsError {
        Text(error).font(.caption).foregroundStyle(RAITheme.secondary)
          .accessibilityIdentifier("runningAbility.diagnostics.error")
      } else {
        Text("正在读取研究诊断…").font(.caption).foregroundStyle(RAITheme.secondary)
          .accessibilityIdentifier("runningAbility.diagnostics.loading")
      }
    }.padding(18).raiGlass()
  }

  private func researchRow(_ item: AthleteDiagnosticItem) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      HStack(alignment: .firstTextBaseline, spacing: 8) {
        Text(researchTitle(item.key)).font(.subheadline.weight(.semibold))
        if item.experimental {
          Text("实验").font(.caption2)
            .padding(.horizontal, 5).padding(.vertical, 1)
            .background(.white.opacity(0.10), in: Capsule())
            .foregroundStyle(RAITheme.secondary)
        }
        Spacer(minLength: 6)
        Text(researchStatusText(item.status))
          .font(.caption2).padding(.horizontal, 6).padding(.vertical, 2)
          .background(.white.opacity(0.10), in: Capsule())
          .foregroundStyle(item.isAvailable ? RAITheme.orange : RAITheme.secondary)
      }
      Text(researchDetail(item)).font(.caption).foregroundStyle(RAITheme.secondary)
      if !item.isAvailable, let code = item.reasonCodes.first {
        Text(researchReasonText(code)).font(.caption2).foregroundStyle(RAITheme.amber)
      }
    }
    .frame(maxWidth: .infinity, alignment: .leading)
    .padding(.vertical, 2)
    .accessibilityElement(children: .combine)
    .accessibilityIdentifier("runningAbility.diagnostic." + item.key)
  }
}

private func researchTitle(_ key: String) -> String {
  switch key {
  case "running_ability_maximal_v2": return "最大能力（实验）"
  case "workout_durability": return "长距离耐力"
  case "aerobic_efficiency": return "有氧效率"
  case "critical_speed": return "临界速度"
  case "load_settling": return "负荷回落"
  default: return key
  }
}

private func researchDetail(_ item: AthleteDiagnosticItem) -> String {
  switch item.key {
  case "running_ability_maximal_v2":
    guard let maximal = item.maximal else { return "尚未产生可用结果" }
    var parts: [String] = []
    if let score = maximal.score { parts.append("跑力 \(ProductFormat.decimal(score, digits: 0)) 分") }
    if let v60 = maximal.v60, let pace = RunningAbilityMath.paceSecondsPerKm(v60: v60) {
      parts.append("V60 \(runningPaceText(pace))/km")
    }
    return parts.isEmpty ? "证据不足，未生成曲线" : parts.joined(separator: " · ")
  case "workout_durability":
    guard let value = item.durability else { return "尚未产生可用结果" }
    var parts: [String] = []
    if let decoupling = value.decoupling {
      parts.append("心率漂移 \(ProductFormat.decimal(decoupling * 100, digits: 1))%")
    }
    if let decay = value.paceDecay {
      parts.append("配速衰减 \(ProductFormat.decimal(decay * 100, digits: 1))%")
    }
    if parts.isEmpty, let ef1 = value.ef1 {
      parts.append("效率因子 \(ProductFormat.decimal(ef1, digits: 2))")
    }
    return parts.isEmpty ? "缺乏稳定的心率或配速对照" : parts.joined(separator: " · ")
  case "aerobic_efficiency":
    guard let value = item.efficiency else { return "尚未产生可用结果" }
    var parts: [String] = []
    if let efficiency = value.efficiencyMPerBeat {
      parts.append("效率 \(ProductFormat.decimal(efficiency, digits: 2)) m/beat")
    }
    if let heartRate = value.heartRateBPM {
      parts.append("心率 \(ProductFormat.decimal(heartRate, digits: 0)) bpm")
    }
    return parts.isEmpty ? "缺乏有效心率区间" : parts.joined(separator: " · ")
  case "critical_speed":
    guard let value = item.criticalSpeed else { return "尚未产生可用结果" }
    var parts: [String] = []
    if let cs = value.csMPS { parts.append("CS \(ProductFormat.decimal(cs, digits: 2)) m/s") }
    if let dPrime = value.dPrimeMeters {
      parts.append("D′ \(ProductFormat.decimal(dPrime, digits: 0)) m")
    }
    if parts.isEmpty { parts.append("有效跑步 \(value.workoutCount) 次") }
    return parts.joined(separator: " · ")
  case "load_settling":
    guard let value = item.loadSettling else { return "尚未产生可用结果" }
    var parts: [String] = []
    if let hours = value.hours { parts.append("回落 \(ProductFormat.decimal(hours, digits: 1)) 小时") }
    if let bucket = value.bucket, !bucket.isEmpty { parts.append("区间 \(bucket) 小时") }
    return parts.isEmpty ? "等待负荷回落基线" : parts.joined(separator: " · ")
  default:
    return item.model
  }
}

private func researchStatusText(_ status: String) -> String {
  switch status {
  case "available": return "可用"
  case "partial": return "部分可用"
  case "collecting": return "积累中"
  case "unstable": return "不稳定"
  case "stale": return "已过期"
  case "unavailable": return "暂不可用"
  default: return "证据不足"
  }
}

private func researchReasonText(_ code: String) -> String {
  switch code {
  case "technical_failure": return "结果校验未通过，等待重新计算。"
  case "insufficient_history": return "历史跨度不足，继续训练即可积累。"
  case "insufficient_comparable_runs": return "可比跑步不足。"
  case "too_few_workouts": return "有效跑步次数不足。"
  case "too_few_durations": return "需要覆盖更多不同时长。"
  case "too_few_heart_rate_workouts": return "带心率的跑步不足。"
  case "missing_hr": return "本次跑步缺少有效心率。"
  case "hr_support_low": return "心率覆盖不足。"
  case "steady_support_too_short": return "稳定配速区间过短。"
  case "baseline_missing": return "尚未建立基线。"
  case "coverage_incomplete": return "数据覆盖不完整。"
  case "score_below_threshold": return "质量分数低于阈值。"
  case "degenerate_fit": return "拟合退化，暂不外推。"
  case "loo_cs_unstable": return "留一验证波动过大。"
  case "loo_rmse_high": return "留一验证误差过大。"
  case "source_unknown": return "数据来源未确认。"
  case "terrain_unknown": return "地形信息缺失。"
  case "context_unknown": return "跑步场景未知。"
  case "maximal_annotations_insufficient": return "确认的最大努力次数不足。"
  case "maximal_duration_coverage_insufficient": return "最大努力时长覆盖不足。"
  case "no_qualified_maximal_effort": return "暂无符合条件的最努力跑步。"
  default: return "暂不可用（" + code + "）。"
  }
}

private func runningPaceText(_ secondsPerKm: Double) -> String {
  guard secondsPerKm.isFinite, secondsPerKm > 0 else { return "--" }
  let total = Int(secondsPerKm.rounded())
  return String(format: "%d′%02d″", total / 60, total % 60)
}

private func runningRaceTimeText(_ seconds: Double) -> String {
  guard seconds.isFinite, seconds >= 0 else { return "--" }
  let total = Int(seconds.rounded())
  if total >= 3600 {
    return String(format: "%d:%02d:%02d", total / 3600, (total % 3600) / 60, total % 60)
  }
  return String(format: "%d:%02d", total / 60, total % 60)
}

private func runningRetentionText(_ value: Double?) -> String {
  guard let percent = RunningAbilityMath.retentionPercent(value) else { return "--" }
  return ProductFormat.decimal(percent, digits: 1) + "%"
}

private func runningAbilityStateText(_ envelope: MetricEnvelope?) -> String {
  guard let envelope else { return "待激活" }
  switch envelope.presentationState.rawValue {
  case "available": return "可用"
  case "limited": return "有限"
  case "collecting": return "积累中"
  case "unavailable": return "暂不可用"
  case "stale": return "已过期"
  default: return "未知"
  }
}

private func runningStabilityText(_ raw: String) -> String {
  switch raw {
  case "stable": return "稳定"
  case "moderate": return "中等"
  case "unstable": return "波动"
  case "insufficient_evidence", "insufficient": return "证据不足"
  default: return raw.isEmpty ? "未知" : raw
  }
}

private func runningEvidenceModeText(_ raw: String?) -> String {
  guard let raw, !raw.isEmpty else { return "未知" }
  switch raw {
  case "workout_derived": return "跑步记录推导"
  case "estimated": return "估计"
  case "mixed": return "混合"
  default: return raw
  }
}

private func runningQualityText(_ raw: String) -> String {
  switch raw {
  case "good": return "良好"
  case "fair": return "一般"
  case "limited": return "有限"
  case "poor": return "较差"
  case "unknown": return "未知"
  default: return raw.isEmpty ? "未知" : raw
  }
}

private func runningFreshnessText(_ raw: String) -> String {
  switch raw {
  case "fresh": return "最新"
  case "current": return "当前"
  case "aging": return "渐旧"
  case "stale": return "已过期"
  case "expired": return "已失效"
  case "unknown": return "未知"
  default: return raw.isEmpty ? "未知" : raw
  }
}

private func runningZoneColor(_ code: String) -> Color {
  switch code {
  case "E": return RAITheme.blue
  case "M": return RAITheme.green
  case "T": return RAITheme.orange
  case "I": return RAITheme.amber
  case "R": return RAITheme.purple
  default: return RAITheme.secondary
  }
}

// MARK: - 训练负荷

struct TrainingLoadEntryCard: View {
  let data: ProductSnapshot
  let points: [TrainingLoadPoint]

  var body: some View {
    let latest = TrainingLoadSeries.latest(points)
    HStack(spacing: 14) {
      VStack(alignment: .leading, spacing: 6) {
        HStack(spacing: 6) {
          Text("训练负荷").font(.headline)
          Text("legacy v2").font(.caption2).foregroundStyle(RAITheme.secondary)
        }
        if let latest {
          HStack(spacing: 16) {
            loadFact("体能", latest.fitness, RAITheme.orange)
            loadFact("疲劳", latest.fatigue, RAITheme.purple)
            loadFact("平衡", latest.balance, balanceColor(latest.balance))
          }
        } else {
          Text("暂无可用的负荷历史").font(.caption).foregroundStyle(RAITheme.secondary)
        }
      }
      Spacer(minLength: 0)
      Image(systemName: "chevron.right").font(.caption).foregroundStyle(RAITheme.secondary)
    }.padding(16).raiGlass()
  }

  private func loadFact(_ title: String, _ value: Double?, _ color: Color) -> some View {
    VStack(alignment: .leading, spacing: 2) {
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(ProductFormat.decimal(value)).font(.subheadline.weight(.semibold)).monospacedDigit()
        .foregroundStyle(color)
    }
  }
}

struct TrainingLoadDetailView: View {
  @Bindable var model: ProductModel

  private var points: [TrainingLoadPoint] {
    TrainingLoadSeries.points(from: model.data.history)
  }

  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 16) {
        if let latest = TrainingLoadSeries.latest(points) {
          summary(latest)
          trend
        } else {
          EmptyCard(
            title: "暂无训练负荷",
            detail: "同步跑步后，服务端会按 7/42 天指数核生成体能与疲劳。")
        }
        weekly
        Text(
          "体能（CTL）为 42 天指数核，疲劳（ATL）为 7 天指数核，平衡=体能−疲劳；负荷缺失日不补零。数据来自现行 training_state.bundle/v2，不代表新模型的候选结果。"
        ).font(.caption2).foregroundStyle(RAITheme.secondary)
      }.padding(18)
    }.navigationTitle("训练负荷").raiScreen().refreshable { await model.refresh() }
      .accessibilityIdentifier("screen.trainingLoad")
  }

  private func summary(_ latest: TrainingLoadPoint) -> some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "当前负荷", icon: "chart.line.uptrend.xyaxis")
      HStack(spacing: 10) {
        summaryTile("体能", latest.fitness, "42 天", RAITheme.orange)
        summaryTile("疲劳", latest.fatigue, "7 天", RAITheme.purple)
        summaryTile("平衡", latest.balance, "TSB", balanceColor(latest.balance))
      }
    }.padding(18).raiGlass()
  }

  private func summaryTile(
    _ title: String, _ value: Double?, _ caption: String, _ color: Color
  ) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      Text(title).font(.caption).foregroundStyle(RAITheme.secondary)
      Text(ProductFormat.decimal(value)).font(.title2.bold()).monospacedDigit()
        .foregroundStyle(color)
      Text(caption).font(.caption2).foregroundStyle(RAITheme.secondary)
    }.frame(maxWidth: .infinity, alignment: .leading).padding(12)
      .background(.white.opacity(0.05), in: RoundedRectangle(cornerRadius: 14))
  }

  private var trend: some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "体能与疲劳趋势", icon: "waveform.path.ecg")
      Chart(points) { point in
        if let value = point.fitness {
          LineMark(x: .value("日期", point.day), y: .value("体能", value))
            .foregroundStyle(RAITheme.orange)
            .interpolationMethod(.catmullRom)
        }
        if let value = point.fatigue {
          LineMark(x: .value("日期", point.day), y: .value("疲劳", value))
            .foregroundStyle(RAITheme.purple)
            .interpolationMethod(.catmullRom)
        }
      }.chartXAxis {
        AxisMarks(values: .automatic(desiredCount: 4)) { value in
          AxisValueLabel {
            Text(shortDay(value.as(String.self) ?? "")).font(.caption2)
          }
        }
      }.chartYAxis { AxisMarks(position: .leading) }
        .frame(height: 210)
      HStack(spacing: 16) {
        legend("体能", RAITheme.orange)
        legend("疲劳", RAITheme.purple)
      }
    }.padding(18).raiGlass()
  }

  private var weekly: some View {
    let days = Array(model.data.overview["days"].list.suffix(7))
    return VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "近 7 天负荷", icon: "chart.bar")
      if days.isEmpty {
        Text("暂无周负荷数据").font(.caption).foregroundStyle(RAITheme.secondary)
      } else {
        Chart(Array(days.enumerated()), id: \.offset) { index, day in
          if let load = day.number("load") {
            BarMark(x: .value("日", index), y: .value("负荷", load), width: .ratio(0.55))
              .foregroundStyle(RAITheme.orange.gradient).cornerRadius(3)
          }
        }.chartXAxis {
          AxisMarks(values: Array(0..<days.count)) { value in
            AxisValueLabel {
              Text(shortDay(days[value.as(Int.self) ?? 0].string("date"))).font(.caption2)
            }
          }
        }.chartYAxis(.hidden).frame(height: 130)
      }
    }.padding(18).raiGlass()
  }

  private func legend(_ title: String, _ color: Color) -> some View {
    HStack(spacing: 5) {
      Circle().fill(color).frame(width: 7, height: 7)
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
    }
  }
}

private func shortDay(_ day: String) -> String {
  guard day.count >= 10 else { return day }
  return String(day.suffix(5))
}

private func balanceColor(_ value: Double?) -> Color {
  guard let value else { return RAITheme.secondary }
  return value >= 0 ? RAITheme.green : RAITheme.amber
}
