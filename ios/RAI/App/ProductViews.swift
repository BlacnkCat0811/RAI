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
      NavigationStack { StatisticsView(model: model) }.tabItem {
        Label("统计", systemImage: "chart.bar.xaxis")
      }.tag(3)
      NavigationStack { ProfileView(model: model, sync: sync) }.tabItem {
        Label("我的", systemImage: "person.crop.circle")
      }.tag(4)
    }.tint(RAITheme.orange)
      .task { await model.refresh() }
      .onChange(of: sync.activeSessionID) { _, _ in model = ProductModel(); Task { await model.refresh() } }
      .onChange(of: phase) { _, value in if value == .active { Task { await model.refresh() } } }
      // No fixed polling. After a fresh upload the analysis needs a little time, so
      // refresh once at ~30 s and once more at ~2 min; the task is cancelled if
      // another upload lands or the tabs go away.
      .task(id: sync.lastUploadAt) {
        guard let uploadedAt = sync.lastUploadAt, Date().timeIntervalSince(uploadedAt) < 180 else { return }
        for delay in [30, 90] {
          try? await Task.sleep(for: .seconds(delay))
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
  @Namespace private var hero
  @State private var selectedSession: ProductSession?
  @Environment(\.dynamicTypeSize) private var typeSize
  @Environment(\.accessibilityReduceMotion) private var reduced
  var body: some View {
    ScrollView {
      VStack(spacing: 12) {
        greeting.padding(.bottom, 8)
        if needsSetup {
          SetupCard(sync: sync, connected: sync.activeSessionID != nil, authorized: authorized)
        }
        if connected {
          if let session = model.data.todaySession {
            Button {
              selectedSession = session
            } label: {
              TodayTrainingStrip(session: session)
            }.buttonStyle(RAIPressStyle()).matchedTransitionSource(id: session.id, in: hero)
              .accessibilityIdentifier("home.hero")
          } else if model.data.plan == .null {
            Button { tab = 2 } label: {
              RAIRowLabel(title: "今日训练", icon: "figure.run", detail: "还没有训练计划，去「训练」创建一个。")
            }.buttonStyle(RAIPressStyle())
          }
          if let athlete = model.data.athlete, athlete.supportsPresentation {
            HomeMetricGrid(model: model, snapshot: athlete)
              .accessibilityElement(children: .contain).accessibilityIdentifier("model.home.active")
          } else {
            NavigationLink {
              AnalysisView(model: model)
            } label: {
              EmptyCard(title: "运动模型", detail: athleteAbsenceDetail(model.data))
            }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("home.status")
          }
          if let athlete = model.data.athlete, athlete.supportsPresentation {
            NavigationLink {
              TrainingLoadDetailView(model: model)
            } label: {
              HomeLoadCard(overview: model.data.overview, load: athlete.trainingLoad, points: model.trainingLoadPoints)
            }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("home.load")
            if let window = athlete.loadFocus?.presentationValue?.windows.max(by: { $0.windowDays < $1.windowDays }) {
              HomeLoadFocusCard(window: window)
            }
          }
          VStack(alignment: .leading, spacing: 14) {
            HStack {
              CardHeading(title: "最近运动", icon: "clock")
              Spacer()
              Button("全部") { tab = 1 }.font(.caption.weight(.semibold)).foregroundStyle(RAITheme.orange)
            }
            if let workout = model.data.workouts.first {
              NavigationLink {
                WorkoutDetail(model: model, workout: workout)
              } label: {
                HomeRecentWorkoutCard(workout: workout, route: model.homeRoutes[workout.id] ?? (model.previewOnly ? [] : nil))
              }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("home.recent")
                .task(id: workout.id) { await model.loadHomeRoute(workout.id) }
            } else {
              Text("还没有已同步的跑步记录").font(.subheadline).foregroundStyle(RAITheme.secondary)
            }
          }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
          AthleteRefreshNotice(snapshot: model.data)
        }
        // Only the very first load shows a spinner; later refreshes are silent.
        if model.loading && model.data.workouts.isEmpty && model.data.athlete == nil {
          ProgressView("正在更新").font(.caption)
        }
        if let error = model.error {
          RAINotice(text: error).accessibilityIdentifier("product.error")
        }
      }.padding(.horizontal, 18).padding(.top, 8).padding(.bottom, 22)
    }.raiScreen().refreshable { await model.refresh(); await model.loadTrainingLoadHistory() }
      .toolbar(.hidden, for: .navigationBar)
      .task(id: model.data.athlete?.assignment.version) { await model.loadTrainingLoadHistory() }
      // The bar is hidden, so nothing keeps scrolled cards out of the status bar.
      .safeAreaInset(edge: .top, spacing: 0) {
        Color.clear.frame(height: 0).background(RAITheme.background).accessibilityHidden(true)
      }
      .navigationDestination(item: $selectedSession) { session in
        SessionDetail(model: model, session: session, effective: model.data.effective).modifier(
          RAIDetailTransition(id: session.id, namespace: hero))
      }
      .accessibilityIdentifier("screen.home")
  }
  /// HealthKit never reveals whether read access was granted, only that the
  /// request was made; `authorization` changes when the user asks in-app.
  private var authorized: Bool {
    sync.authorization != "尚未请求" || UserDefaults.standard.bool(forKey: "healthAuthorizationRequested")
  }
  private var needsSetup: Bool { !model.previewOnly && (sync.activeSessionID == nil || !authorized) }
  private var connected: Bool { sync.activeSessionID != nil || model.previewOnly }
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
      VStack(alignment: .leading, spacing: 2) {
        Text(HomeDates.header.string(from: Date())).font(.subheadline).foregroundStyle(RAITheme.secondary)
        Text("今天").font(.system(.largeTitle, design: .rounded, weight: .bold))
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
        .background(RAITheme.primary.opacity(0.06), in: Capsule())
    }.buttonStyle(.plain).accessibilityIdentifier("home.sync")
  }
  private var syncLabel: String {
    ProductSyncBadge.resolve(
      busy: sync.busy,
      hasError: sync.error != nil,
      hasFailedResult: sync.workoutResults.values.contains { $0.state == .failed },
      hasSucceededResult: sync.workoutResults.values.contains { $0.state == .succeeded },
      lastUploadAt: sync.lastUploadAt,
      pendingChanges: sync.pendingChanges
    ).text
  }
}
/// First-run guide: the three things needed before the app shows anything.
struct SetupCard: View {
  @Bindable var sync: SyncModel
  let connected: Bool, authorized: Bool
  var body: some View {
    VStack(alignment: .leading, spacing: 16) {
      VStack(alignment: .leading, spacing: 4) {
        Text("开始使用").font(.system(.title2, design: .rounded, weight: .bold))
        Text("完成下面几步，RAI 就能读取你的跑步并给出分析。").font(.subheadline)
          .foregroundStyle(RAITheme.secondary)
      }
      step(1, "连接你的服务器", connected ? "已连接" : "打开服务器生成的 rai:// 链接，或手动填写。", done: connected)
      step(2, "授权读取健康数据", authorized ? "已请求授权" : "只读取跑步、心率和睡眠，不会写入。", done: authorized)
      step(3, "填写最大心率", "在「我的 → 静息与最大心率」填写，用于计算训练负荷。", done: false, optional: true)
      if !connected {
        NavigationLink {
          SyncView(model: sync)
        } label: {
          Text("去连接").font(.headline).frame(maxWidth: .infinity).padding(.vertical, 14)
            .background(RAITheme.lemon, in: Capsule()).foregroundStyle(RAITheme.primary)
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("setup.connect")
      } else if !authorized {
        Button {
          sync.authorize()
        } label: {
          Text("授权读取健康数据").font(.headline).frame(maxWidth: .infinity).padding(.vertical, 14)
            .background(RAITheme.lemon, in: Capsule()).foregroundStyle(RAITheme.primary)
        }.buttonStyle(RAIPressStyle()).disabled(sync.busy).accessibilityIdentifier("setup.authorize")
      }
    }.padding(20).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("home.setup")
  }
  private func step(_ index: Int, _ title: String, _ detail: String, done: Bool, optional: Bool = false) -> some View {
    HStack(alignment: .top, spacing: 12) {
      ZStack {
        Circle().fill(done ? RAITheme.orange : RAITheme.primary.opacity(0.08)).frame(width: 26, height: 26)
        if done {
          Image(systemName: "checkmark").font(.caption.weight(.bold)).foregroundStyle(.white)
        } else {
          Text("\(index)").font(.caption.weight(.bold)).foregroundStyle(RAITheme.secondary)
        }
      }.accessibilityHidden(true)
      VStack(alignment: .leading, spacing: 2) {
        Text(title).font(.subheadline.weight(.semibold))
        Text(detail).font(.caption).foregroundStyle(RAITheme.secondary)
          .fixedSize(horizontal: false, vertical: true)
      }
    }.accessibilityElement(children: .combine)
      .accessibilityValue(done ? "已完成" : "未完成")
  }
}
func prescription(_ s: ProductSession) -> String {
  if s.type == "REST" { return "给身体一点恢复时间" }
  var parts: [String] = []
  // Adaptive sessions are prescribed by time; the distance is an estimate
  // from the pace zone. A race stays distance-first.
  if !s.focus.isEmpty && s.type != "RACE" {
    if s.duration > 0 { parts.append(ProductFormat.decimal(s.duration / 60) + " 分钟") }
    if s.distance > 0 { parts.append("约 " + ProductFormat.distance(s.distance) + " 公里") }
    return parts.joined(separator: " · ")
  }
  if s.distance > 0 { parts.append(ProductFormat.distance(s.distance) + " 公里") }
  if s.duration > 0 { parts.append(ProductFormat.decimal(s.duration / 60) + " 分钟") }
  return parts.joined(separator: " · ")
}
/// Why there is no athlete model yet, in terms of what the runner can do.
func athleteAbsenceDetail(_ snapshot: ProductSnapshot) -> String {
  switch snapshot.athleteAbsenceText {
  case "尚未启用": return "服务器上还没有启用运动模型，启用后这里会显示准备度、跑力和训练负荷。"
  case "正在计算": return "还在计算。同步几次跑步并填写最大心率后，这里会显示准备度、跑力和训练负荷。"
  default: return snapshot.athleteAbsenceText
  }
}
struct ActivityRow: View {
  let workout: ProductWorkout
  var pending = false
  var body: some View {
    HStack(spacing: 12) {
      Image(systemName: "figure.run").font(.title).foregroundStyle(RAITheme.amber).frame(
        width: 48, height: 52
      ).background(RAITheme.lemon, in: RoundedRectangle(cornerRadius: 14))
      VStack(alignment: .leading, spacing: 4) {
        HStack(spacing: 8) {
          Text(workout.title).font(.headline)
          if workout.raw.string("analysisStatus") == "failed" {
            RAITag(text: "分析失败", color: RAITheme.amber)
          } else if pending { RAITag(text: "分析中", color: RAITheme.blue) }
        }
        Text(workout.detailLine.isEmpty ? "暂无数据" : workout.detailLine)
          .font(.caption).monospacedDigit()
        Text(workout.date, format: .dateTime.month().day().hour().minute()).font(.caption2)
          .foregroundStyle(RAITheme.secondary)
      }
      Spacer(minLength: 0)
      Image(systemName: "chevron.right").font(.caption).foregroundStyle(RAITheme.secondary)
    }.foregroundStyle(RAITheme.primary)
  }
}
struct ActivitiesView: View {
  @Bindable var model: ProductModel
  var body: some View {
    ScrollView {
      LazyVStack(spacing: 12) {
        if model.data.workouts.isEmpty {
          EmptyCard(title: "还没有跑步记录", detail: "跑步同步到服务器后会出现在这里。")
        }
        ForEach(model.data.workouts) { w in
          NavigationLink {
            WorkoutDetail(model: model, workout: w)
          } label: {
            ActivityRow(workout: w, pending: w.raw.string("analysisStatus") != "published")
              .padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
          }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("workout.row")
        }
        if !model.data.next.isEmpty {
          Button("加载更多") { Task { await model.loadMore() } }.buttonStyle(RAISecondaryButtonStyle())
        }
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
        NavigationLink {
          TrainingLoadDetailView(model: model)
        } label: {
          TrainingLoadEntryCard(points: model.trainingLoadPoints)
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("trainingLoad.entry")
        NavigationLink {
          RunningFitnessDetailView(model: model)
        } label: {
          RunningFitnessEntryCard(snapshot: model.data.athlete, absenceText: model.data.athleteAbsenceText)
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("runningFitness.entry")
        if let athlete = model.data.athlete, athlete.supportsPresentation {
          AthleteModelCard(snapshot: athlete)
          NavigationLink {
            AthleteHistoryView(model: model)
          } label: {
            RAIRowLabel(title: "历史记录", icon: "calendar", detail: "按天查看各项指标")
          }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("model.history.link")
        }
        AthleteRefreshNotice(snapshot: model.data)
        MetricsExplainerLink()
      }.padding(18)
    }.navigationTitle("运动模型").raiScreen()
      .refreshable { await model.refresh(); await model.loadTrainingLoadHistory() }
      .task { await model.loadTrainingLoadHistory() }
      .accessibilityIdentifier("screen.analysis")
  }
}

/// The two user inputs that are not heart rate. Each has its own sheet; the
/// value applies from today.
enum ModelInputKind: String, Identifiable {
  case sleepTarget = "sleep_target"
  case vo2Source = "vo2max_source_selection"
  var id: String { rawValue }
}

private struct ModelInputSheet: View {
  @Bindable var model: ProductModel
  let kind: ModelInputKind
  @Environment(\.dismiss) private var dismiss
  @State private var sleepHours = "8"
  @State private var sourceEpoch = ""
  @State private var vo2Sources: [VO2SourceOption] = []
  @State private var selectionRevoked = false
  @State private var busy = false
  @State private var message: String?

  var body: some View {
    NavigationStack {
      Form {
        if kind == .sleepTarget {
          Section("睡眠目标（小时）") {
            TextField("目标睡眠时长", text: $sleepHours)
            Text("你希望每晚睡多久，用于评估恢复情况。")
              .font(.caption).foregroundStyle(RAITheme.secondary)
          }
        } else {
          Section("Apple VO₂ 估计的来源") {
            if vo2Sources.isEmpty {
              Text("近一年没有同步到 Apple 的 VO₂ 估计。")
                .font(.caption).foregroundStyle(RAITheme.secondary)
            } else {
              Picker("来源", selection: $sourceEpoch) {
                ForEach(vo2Sources, id: \.sourceEpoch) { source in
                  Text(vo2SourceLabel(source)).tag(source.sourceEpoch)
                }
              }.pickerStyle(.inline)
            }
            Toggle("不再使用该来源", isOn: $selectionRevoked)
            Text("通常会自动使用最近一直在记录的 Apple Watch；只有多个设备数据冲突时才需要在这里确认。")
              .font(.caption).foregroundStyle(RAITheme.secondary)
          }
        }
        if model.pendingModelInput != nil {
          Text("上一次修改正在提交，联网后会自动完成。").font(.footnote).foregroundStyle(RAITheme.secondary)
        }
        if let message { Text(message).foregroundStyle(.red).font(.footnote) }
        Section {
          Button(busy ? "保存中…" : "保存") { submit() }
            .disabled(busy || !valid)
        }
      }
      .task {
        guard kind == .vo2Source else { return }
        vo2Sources = await model.loadVO2Sources()
        if sourceEpoch.isEmpty { sourceEpoch = vo2Sources.first?.sourceEpoch ?? "" }
      }
      .navigationTitle(kind == .sleepTarget ? "睡眠目标" : "Apple VO₂ 来源")
      .navigationBarTitleDisplayMode(.inline)
      .toolbar { ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } } }
    }
  }

  private var valid: Bool {
    if kind == .vo2Source {
      return trimmedSourceEpoch.range(
        of: "^sha256:[0-9a-f]{64}$", options: .regularExpression) != nil
    }
    guard let hours = Double(sleepHours) else { return false }
    return hours > 0 && hours <= 24
  }

  private func vo2SourceLabel(_ source: VO2SourceOption) -> String {
    let device = source.productType.hasPrefix("Watch") ? "Apple Watch" : (source.productType.isEmpty ? source.bundleIdentifier : source.productType)
    return "\(device) · 最近 \(source.latestDate) · \(source.samples) 次"
  }

  private var trimmedSourceEpoch: String {
    sourceEpoch.trimmingCharacters(in: .whitespacesAndNewlines)
  }

  private func submit() {
    busy = true
    message = nil
    Task {
      let payload: [String: JSON]
      if kind == .vo2Source {
        payload = [
          "sourceEpoch": .string(trimmedSourceEpoch),
          "revoked": .bool(selectionRevoked),
        ]
      } else {
        payload = ["hours": .number(Double(sleepHours) ?? 0)]
      }
      let failure = await model.submitModelInput(
        kind: kind.rawValue, payload: payload, effectiveFrom: Date())
      await MainActor.run {
        busy = false
        if let failure { message = failure } else { dismiss() }
      }
    }
  }
}
struct AthleteRefreshNotice: View {
  let snapshot: ProductSnapshot
  var body: some View {
    if snapshot.athleteNeedsAppUpdate {
      Label(ProductReasonText.updateApp, systemImage: "arrow.down.app")
        .font(.caption).foregroundStyle(RAITheme.blue)
        .accessibilityIdentifier("model.update-app")
    } else if snapshot.athleteRefreshFailed {
      VStack(alignment: .leading, spacing: 4) {
        Text("运动模型刷新失败，可下拉重试。")
        if let athlete = snapshot.athlete {
          Text("当前显示上次结果：" + Date(timeIntervalSince1970: Double(athlete.evaluationAt) / 1_000_000).formatted(date: .abbreviated, time: .shortened))
        }
      }.font(.caption).foregroundStyle(RAITheme.secondary)
        .accessibilityIdentifier("model.refresh.failure")
    }
  }
}

/// Load composition of one window as a single stacked bar; unknown runs are
/// counted separately, never drawn as low intensity.
struct LoadFocusBar: View {
  let window: LoadFocusWindow
  var body: some View {
    let b = window.buckets
    let parts: [(String, Double, Color)] = [("低强度有氧", b.lowAerobicAU, RAITheme.blue), ("高强度有氧", b.highAerobicAU, RAITheme.orange),
      ("无氧", b.anaerobicProxyAU, RAITheme.purple), ("未分类", b.unclassifiedAU, RAITheme.secondary.opacity(0.4))]
    let total = parts.reduce(0) { $0 + $1.1 }
    VStack(alignment: .leading, spacing: 6) {
      HStack {
        Text("近 \(window.windowDays) 天构成").font(.caption)
        Spacer()
        if let coverage = window.classifiedCoverage {
          Text("可归类 \(Int((coverage * 100).rounded()))%").font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if total > 0 {
        GeometryReader { geo in
          HStack(spacing: 2) {
            ForEach(parts.filter { $0.1 > 0 }, id: \.0) { part in
              RoundedRectangle(cornerRadius: 3).fill(part.2)
                .frame(width: max(4, (geo.size.width - 6) * part.1 / total))
            }
          }
        }.frame(height: 10)
        HStack(spacing: 10) {
          ForEach(parts.filter { $0.1 > 0 }, id: \.0) { part in
            HStack(spacing: 4) {
              Circle().fill(part.2).frame(width: 7, height: 7)
              Text("\(part.0) \(Int((part.1 / total * 100).rounded()))%").font(.caption2).foregroundStyle(RAITheme.secondary)
            }
          }
        }
      } else {
        Text("没有已知负荷").font(.caption2).foregroundStyle(RAITheme.secondary)
      }
      if window.unknownWorkouts > 0 {
        Text("\(window.unknownWorkouts) 次跑步负荷未知，未计入").font(.caption2).foregroundStyle(RAITheme.secondary)
      }
    }.accessibilityElement(children: .combine)
  }
}

struct AthleteModelCard: View {
  let snapshot: AthleteModelSnapshot
  var summary = false
  @AppStorage(ExperimentalMetrics.key) private var showExperimental = false
  @Environment(\.dynamicTypeSize) private var typeSize
  var body: some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: summary ? "运动模型" : "恢复与准备度", icon: summary ? "figure.run.circle" : "heart.text.square")
      row("训练准备度", value: snapshot.trainingReadiness.value?.score, unit: "分", metric: snapshot.trainingReadiness.envelope, digits: 0)
      if snapshot.trainingReadiness.envelope.presentationValueAvailable,
         let notice = ProductReasonText.lowReadinessNotice(score: snapshot.trainingReadiness.value?.score) {
        Text(notice).font(.caption).foregroundStyle(RAITheme.secondary).accessibilityIdentifier("model.readiness.lowHint")
      }
      if snapshot.trainingReadiness.envelope.mode == "workout_only" {
        Text("昨夜恢复数据不足，仅按训练负荷估计").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if let session = snapshot.sessionRecovery, let value = session.presentationValue {
        Text("距下一次高强度训练：" + value.text).font(.caption).foregroundStyle(RAITheme.secondary)
        Text("按已同步的跑步估计").font(.caption2).foregroundStyle(RAITheme.secondary)
      }
      if summary, let fitness = snapshot.runningFitness {
        row("跑力", value: fitness.value?.overall.score, unit: "分", metric: fitness.envelope, digits: 1)
      }
      if !summary {
      row("生理恢复", value: snapshot.physiologicalRecovery.value?.score, unit: "分", metric: snapshot.physiologicalRecovery.envelope, digits: 0)
      if snapshot.physiologicalRecovery.envelope.presentationValueAvailable,
         let level = ProductReasonText.recoveryLevel(snapshot.physiologicalRecovery.value?.deviationLevel) {
        Text(level).font(.caption).foregroundStyle(RAITheme.secondary).accessibilityIdentifier("model.recovery.level")
      }
      if snapshot.physiologicalRecovery.envelope.mode == "estimated", let value = snapshot.physiologicalRecovery.value {
        Text("数据有限：近期 \(value.trendNights ?? 0) 晚 · 基线 \(value.baselineNights ?? 0) 晚").font(.caption2).foregroundStyle(RAITheme.secondary)
        if let missing = value.missingComponents, !missing.isEmpty {
          Text("昨夜数据不足：" + missing.map { ["hrv":"HRV", "rhr":"静息心率", "sleep":"睡眠"][$0] ?? $0 }.joined(separator: "、")).font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if let value = snapshot.physiologicalRecovery.presentationValue {
        ForEach(value.inputSourceDescriptions, id: \.self) { description in
          Text(description).font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if snapshot.physiologicalRecovery.value?.sleepRelativeToHabit == true { Text("睡眠按你的个人习惯评估").font(.caption2).foregroundStyle(RAITheme.secondary) }

      sectionDivider("训练负荷")
      row("近 7 天日均负荷", value: snapshot.trainingLoad.value?.acute, unit: "", metric: snapshot.trainingLoad.envelope, digits: 0)
      if let focus = snapshot.loadFocus?.presentationValue {
        ForEach(focus.windows, id: \.windowDays) { window in
          LoadFocusBar(window: window)
        }
      }

      sectionDivider("最大摄氧量")
      if showExperimental, let rai = snapshot.raiVO2Max {
        row(rai.value?.experimental == true ? "RAI VO₂（" + ProductReasonText.experimentalLabel + "）" : "RAI VO₂", value: rai.value?.value, unit: "mL/kg/min", metric: rai.envelope, digits: 1)
      }
      row("Apple VO₂ 估计", value: snapshot.externalVO2Max.value?.value, unit: "mL/kg/min", metric: snapshot.externalVO2Max.envelope, digits: 1)
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("model.card")
  }
  private func sectionDivider(_ title: String) -> some View {
    VStack(alignment: .leading, spacing: 10) {
      Divider().padding(.top, 4)
      Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(RAITheme.secondary)
    }
  }
  @ViewBuilder private func row(_ title: String, value: Double?, unit: String, metric: MetricEnvelope, digits: Int) -> some View {
    let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading)) : AnyLayout(HStackLayout())
    layout {
      Text(title + (metric.mode == "estimated" ? "（估计）" : "")).font(.subheadline)
      if !typeSize.isAccessibilitySize { Spacer(minLength: 8) }
      if metric.presentationValueAvailable, let value, value.isFinite {
        Text((ProductFormat.decimal(value, digits: digits) + " " + unit).trimmingCharacters(in: .whitespaces)).font(.body.weight(.semibold)).monospacedDigit()
      } else {
        Text(AthleteModelCard.reason(metric)).font(.caption).foregroundStyle(RAITheme.secondary)
      }
    }.accessibilityElement(children: .combine)
  }
  static func reason(_ metric: MetricEnvelope) -> String {
    metric.metric == "runningFitness"
      ? ProductReasonText.runningFitness(modelStatus: metric.modelStatus.rawValue, reason: metric.displayReason?.rawValue,
                                         state: metric.presentationState.rawValue)
      : ProductReasonText.text(for: metric.displayReason?.rawValue, state: metric.presentationState.rawValue)
  }
}

struct AthleteHistoryView: View {
  @Bindable var model: ProductModel
  @AppStorage(ExperimentalMetrics.key) private var showExperimental = false
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
        Text("训练负荷").tag("trainingLoad")
        if model.data.athlete?.schemaVersion == "rai.athlete_model/4" {
          Text("跑力").tag("runningFitness")
        }
        if ["rai.athlete_model/3", "rai.athlete_model/4"].contains(model.data.athlete?.schemaVersion ?? "") {
          Text("训练后恢复").tag("sessionRecovery")
          Text("负荷构成").tag("loadFocus")
        }
        Text("生理恢复").tag("physiologicalRecovery")
        Text("训练准备度").tag("trainingReadiness")
        if showExperimental { Text("RAI VO₂（实验）").tag("raiVO2Max") }
        Text("Apple VO₂ 估计").tag("externalVO2Max")
      }
      if let page, page.metric == metric, page.assignment == model.data.athlete?.assignment {
        if page.points.isEmpty { Text("这段时间没有历史结果").foregroundStyle(RAITheme.secondary) }
        ForEach(page.points) { point in
          LabeledContent(point.date, value: display(point)).accessibilityIdentifier("model.history.point")
        }
      } else if failed {
        Text("历史暂不可用")
      } else {
        ProgressView("正在读取历史")
      }
    }
    .scrollContentBackground(.hidden)
    .raiScreen()
    .navigationTitle("历史记录")
    .navigationBarTitleDisplayMode(.inline)
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
    case "AU/day": return ProductFormat.decimal(value, digits: 1) + " /日"
    case "mL/kg/min": return ProductFormat.decimal(value, digits: 1) + " mL/kg/min"
    default: return ProductFormat.decimal(value, digits: 1)
    }
  }
}

struct ProfileView: View {
  @Environment(\.accessibilityReduceMotion) private var reduced
  @Environment(\.accessibilityReduceTransparency) private var opaque
  @Environment(\.dynamicTypeSize) private var typeSize
  @Bindable var model: ProductModel
  @Bindable var sync: SyncModel
  @AppStorage("runnerName") private var name = "跑者"
  @State private var modelInput: ModelInputKind?
  @State private var hasHeartRateSuggestion = false
  @AppStorage(ExperimentalMetrics.key) private var showExperimental = false
  var body: some View {
    Form {
      Section("个人资料") {
        LabeledContent("称呼") {
          TextField("跑者", text: $name).multilineTextAlignment(.trailing)
        }
      }
      Section {
        NavigationLink {
          HeartRateSettingsView(model: model)
        } label: {
          VStack(alignment: .leading, spacing: 4) {
            Label("静息与最大心率", systemImage: "heart")
            if hasHeartRateSuggestion {
              Text("发现新的心率建议，点此确认").font(.caption).foregroundStyle(RAITheme.blue)
                .accessibilityIdentifier("profile.hr-suggestion")
            }
          }
        }.accessibilityIdentifier("profile.hr-settings")
        Button {
          modelInput = .sleepTarget
        } label: {
          Label("睡眠目标", systemImage: "bed.double")
        }.disabled(!model.configured).accessibilityIdentifier("profile.model-input")
        if let env = model.data.athlete?.externalVO2Max.envelope,
          ([env.modelStatus.rawValue] + env.reasonCodes.map(\.rawValue) + env.productReasonCodes.map(\.rawValue))
            .contains(where: { ["source_selection_required", "source_conflict"].contains($0) }) {
          // Normally chosen automatically; only an unresolved conflict asks.
          Button {
            modelInput = .vo2Source
          } label: {
            VStack(alignment: .leading, spacing: 4) {
              Label("Apple VO₂ 来源", systemImage: "applewatch")
              Text("多个设备的 VO₂ 数据不一致，点此确认使用哪一个").font(.caption).foregroundStyle(RAITheme.blue)
            }
          }.disabled(!model.configured).accessibilityIdentifier("profile.vo2-source")
        }
      } header: {
        Text("训练参数")
      } footer: {
        Text("最大心率用于计算训练负荷、准备度和心率区间，建议最先填写。")
      }
      Section {
        NavigationLink {
          WorkoutSyncView(model: sync)
        } label: {
          Label("跑步记录同步", systemImage: "figure.run")
        }.accessibilityIdentifier("profile.workout-sync")
        NavigationLink {
          SyncView(model: sync)
        } label: {
          Label("服务器与健康授权", systemImage: "server.rack")
        }.accessibilityIdentifier("profile.sync")
        Button {
          Task { await model.reconnect() }
        } label: {
          Label(model.loading ? "正在刷新…" : "重新连接并刷新", systemImage: "arrow.clockwise")
        }.disabled(model.loading)
      } header: {
        Text("数据与同步")
      } footer: {
        Text(sync.activeSessionID == nil ? "尚未连接服务器。" : "新的跑步会在结束约 1 分钟后自动上传。")
      }
      if !model.execution.string("executionID").isEmpty {
        Section("训练执行") {
          NavigationLink {
            ExecutionView(model: model)
          } label: {
            Label("最近一次训练与反馈", systemImage: "checkmark.circle")
          }
        }
      }
      if model.pending != nil && model.conflict {
        Section("训练计划已更新") {
          Text("有一次修改和最新计划冲突，没有保存。").font(.footnote)
          Button("查看最新计划") { Task { await model.reviewConflict() } }
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
      Section {
        NavigationLink {
          MetricsExplainerView()
        } label: {
          Label("指标说明", systemImage: "info.circle")
        }.accessibilityIdentifier("profile.explainer")
        Toggle("显示实验指标", isOn: $showExperimental)
          .accessibilityIdentifier("profile.experimental")
        LabeledContent("版本", value: appVersion)
      } header: {
        Text("关于")
      } footer: {
        Text("RAI 只读取健康数据，不会写入或删除健康 App 中的记录。分析与训练建议由你自己的服务器计算。")
      }
      if let error = model.error { Section { RAINotice(text: error) } }
    }.scrollContentBackground(.hidden).navigationTitle("我的").raiScreen().accessibilityIdentifier(
      "screen.profile")
      .sheet(item: $modelInput) { ModelInputSheet(model: model, kind: $0) }
      .task(id: model.statisticsRevision) {
        let (response, supported) = await model.loadHRSettingsV3()
        guard let response, supported else { hasHeartRateSuggestion = false; return }
        let saved = response["savedHRSettings"]
        let maximum = saved["maxHR"].number("value") ?? response["hrReference"].number("maxHR")
        let resting = saved["restingHR"]["manualValue"].number("value") ?? response["hrReference"].number("restingHR")
        hasHeartRateSuggestion = HRSettingDraft.maximumSuggestion(response["observedMaxHR"], current: maximum) != nil
          || HRSettingDraft.restingSuggestion(response["observedRestingHR"], current: resting, maximum: maximum) != nil
      }
  }
  private var appVersion: String {
    let info = Bundle.main.infoDictionary
    let short = info?["CFBundleShortVersionString"] as? String ?? "--"
    let build = info?["CFBundleVersion"] as? String ?? "--"
    return "\(short)（\(build)）"
  }
}


func trainingTag(_ type: String) -> String {
  [
    "REST": "恢复与休息", "EASY": "有氧耐力", "LONG": "长距离耐力", "TEMPO": "节奏耐力", "INTERVAL": "间歇训练",
    "RECOVERY": "低强度恢复", "RACE": "目标比赛",
  ][type] ?? "训练安排"
}

import ProductCore
import SwiftUI
import SyncCore

/// One HKWorkout = one protocol-6 payload. A round is strictly sequential and
/// ends with only two final per-record states: 上传成功 or 上传失败.
struct WorkoutSyncView: View {
  @Bindable var model: SyncModel
  @State private var filter: Filter = .all
  @State private var detail: HealthWorkoutSummary?
  @State private var exportStatus: String?
  @State private var showExcluded = false
  @AppStorage("developerOptions") private var developerOptions = false
  private enum Filter: String, CaseIterable { case all = "全部", unsynced = "未上传", failed = "失败", succeeded = "已上传" }

  var body: some View {
    ScrollView {
      LazyVStack(spacing: 12) {
        intro
        if model.loadingWorkouts { ProgressView("正在读取跑步记录…").font(.caption) }
        batchSummary
        selectionBar
        sourcePolicyNote
        ForEach(filtered) { workout in
          row(workout)
        }
        if filtered.isEmpty && !model.loadingWorkouts {
          EmptyCard(
            title: model.workouts.isEmpty ? "还没有读到跑步记录" : "没有符合条件的记录",
            detail: model.workouts.isEmpty
              ? "连接服务器并授权读取健康数据后，健康 App 中的跑步会出现在这里。"
              : "换一个筛选条件试试。")
        }
        if model.hasMoreWorkouts {
          Button {
            Task { await model.loadMoreWorkouts() }
          } label: {
            HStack {
              if model.loadingMoreWorkouts { ProgressView().controlSize(.small) }
              Text(model.loadingMoreWorkouts ? "正在读取更早记录…" : "加载更早记录")
            }.frame(maxWidth: .infinity)
          }.buttonStyle(RAISecondaryButtonStyle())
            .disabled(!model.canLoadMoreWorkouts)
            .accessibilityIdentifier("sync.loadMore")
        } else if !model.workouts.isEmpty {
          Text("已加载全部历史记录（\(model.workouts.count) 条）")
            .font(.caption2).foregroundStyle(RAITheme.secondary)
        }
        if let error = model.error {
          RAINotice(text: error)
        }
      }.padding(18)
    }
    .raiScreen()
    .navigationTitle("跑步记录同步")
    .navigationBarTitleDisplayMode(.inline)
    .toolbar {
      #if DEBUG
      if developerOptions {
        ToolbarItem(placement: .topBarLeading) {
          Button("导出证据") {
            exportStatus = model.exportAcceptanceEvidence()?.lastPathComponent
          }.disabled(model.busy)
        }
      }
      #endif
      ToolbarItem(placement: .topBarTrailing) {
        Button("刷新") { Task { await model.loadWorkouts() } }.disabled(model.workoutBusy || model.loadingWorkouts)
      }
    }
    .refreshable { await model.loadWorkouts() }
    .task { if model.workouts.isEmpty { await model.loadWorkouts() } }
    // The upload belongs to SyncModel, not to this page: leaving the page (or
    // closing the detail sheet) must not stop it. Only the cancel button does.
    .sheet(item: $detail) { workout in
      NavigationStack { WorkoutSyncDetail(model: model, workout: workout) }
    }
    .accessibilityIdentifier("screen.workout-sync")
  }

  private var selectionEditable: Bool {
    !model.workoutBusy && !model.loadingWorkouts && !model.loadingMoreWorkouts
  }

  private var intro: some View {
    let succeeded = model.workoutResults.values.filter { $0.state == .succeeded }.count
    let failed = model.workoutResults.values.filter { $0.state == .failed }.count
    return VStack(alignment: .leading, spacing: 10) {
      HStack(alignment: .firstTextBaseline) {
        Text("已上传 \(succeeded) 条").font(.system(.title3, design: .rounded, weight: .bold))
        if failed > 0 { RAITag(text: "\(failed) 条失败", color: .red) }
        Spacer()
        if let last = model.lastUploadAt {
          Text("最近 \(last, format: .dateTime.month().day().hour().minute())").font(.caption).foregroundStyle(RAITheme.secondary)
        }
      }
      Text("新的跑步会在结束约 1 分钟后自动上传，半小时后再补传一次晚到的数据。")
        .font(.caption).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
      if !UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") && model.authorization == "尚未请求" {
        Button("请求读取权限") { model.authorize() }.buttonStyle(RAISecondaryButtonStyle())
          .disabled(model.busy || model.activeSessionID == nil)
      }
      if model.busy {
        HStack(spacing: 8) {
          ProgressView().controlSize(.small)
          Text(model.status).font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if let exportStatus { Text("已导出：\(exportStatus)").font(.caption2).foregroundStyle(RAITheme.green) }
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }

  @ViewBuilder private var batchSummary: some View {
    if let batch = model.batch {
      let succeeded = batch.ids.filter { model.workoutResults[$0]?.state == .succeeded }.count
      let failed = batch.ids.filter { model.workoutResults[$0]?.state == .failed }.count
      VStack(alignment: .leading, spacing: 7) {
        HStack {
          Text(batch.finishedAt == nil ? "本轮上传进行中" : "本轮结果").font(.subheadline.weight(.semibold))
          Spacer()
          if let duration = batch.duration { Text(String(format: "%.1f 秒", duration)).font(.caption2).foregroundStyle(RAITheme.secondary) }
        }
        Text("已选 \(batch.ids.count) 条 · 上传成功 \(succeeded) 条 · 上传失败 \(failed) 条").font(.caption)
        if batch.finishedAt == nil {
          ProgressView(value: Double(succeeded + failed), total: Double(max(batch.ids.count, 1)))
          Button("取消上传") { model.cancelWorkoutSync() }.buttonStyle(RAISecondaryButtonStyle())
        }
      }.padding(14).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
        .accessibilityIdentifier("sync.batchSummary")
    }
  }

  private var selectionBar: some View {
    VStack(spacing: 10) {
      Picker("筛选", selection: $filter) {
        ForEach(Filter.allCases, id: \.self) { Text($0.rawValue).tag($0) }
      }.pickerStyle(.segmented)
      HStack {
        Text("两周前的历史记录不会自动上传。").font(.caption).foregroundStyle(RAITheme.secondary)
        Spacer()
        Button("补传历史记录") { Task { await model.backfillUnsyncedWorkouts() } }
          .buttonStyle(RAISecondaryButtonStyle())
          .disabled(!model.canStartWorkoutSync || !selectionEditable)
          .accessibilityIdentifier("sync.backfill")
      }
    }.padding(14).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }

  private func row(_ workout: HealthWorkoutSummary) -> some View {
    let result = model.workoutResults[workout.id]
    return VStack(alignment: .leading, spacing: 10) {
      HStack(alignment: .top, spacing: 10) {
        VStack(alignment: .leading, spacing: 3) {
          HStack(spacing: 6) {
            Text(workout.isIndoor == true ? "室内跑步" : workout.isIndoor == false ? "户外跑步" : "跑步").font(.headline)
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
        if workout.isEligible { resultBadge(result) } else {
          RAITag(text: workout.isExcluded ? "不上传" : "来源待确认")
        }
      }
      HStack(spacing: 10) {
        Text(workout.sourceName ?? "--").font(.caption2).foregroundStyle(RAITheme.secondary)
        if let device = workout.deviceName { Text(device).font(.caption2).foregroundStyle(RAITheme.secondary) }
        Spacer()
        Button("详情") { detail = workout }.font(.caption.weight(.semibold)).foregroundStyle(RAITheme.secondary)
        if workout.isEligible {
          Button(result == nil ? "上传" : "重新上传") { model.syncWorkouts([workout.id]) }
            .font(.caption.weight(.semibold)).foregroundStyle(RAITheme.orange)
            .disabled(!model.canStartWorkoutSync || result?.state == .pending || result?.state == .uploading)
        } else {
          Text(workout.isExcluded ? "不是 Apple Watch 记录的跑步" : "来源暂时无法确认")
            .font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if let result, result.state == .failed, let reason = result.reason {
        Text(reason).font(.caption2).foregroundStyle(.red)
      }
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("sync.workout.row")
  }

  private func metric(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 1) {
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(value).font(.caption.monospacedDigit()).foregroundStyle(RAITheme.primary)
    }
  }

  private func resultBadge(_ result: WorkoutUploadResult?) -> some View {
    let label: String, color: Color
    switch result?.state {
    case .none: label = "未上传"; color = RAITheme.secondary
    case .pending: label = "等待上传"; color = RAITheme.secondary
    case .uploading: label = "正在上传"; color = RAITheme.amber
    case .succeeded: label = "已上传"; color = RAITheme.green
    case .failed: label = "上传失败"; color = .red
    }
    return RAITag(text: label, color: color).accessibilityIdentifier("sync.result")
  }

  private var filtered: [HealthWorkoutSummary] {
    model.workouts.filter { workout in
      // A record the frozen source policy excludes is never offered for sync.
      // It stays visible only on request, with a plain-language reason.
      if workout.isExcluded && !showExcluded { return false }
      switch filter {
      case .all: return true
      case .unsynced:
        let state = model.workoutResults[workout.id]?.state
        return state == nil || state == .pending || state == .uploading
      case .failed: return model.workoutResults[workout.id]?.state == .failed
      case .succeeded: return model.workoutResults[workout.id]?.state == .succeeded
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
              Text("有 \(excluded) 条不是 Apple Watch 记录的跑步（第三方 App 或手动添加），不会上传")
                .font(.caption2).foregroundStyle(RAITheme.secondary)
              Spacer(minLength: 0)
              Button(showExcluded ? "收起" : "查看") { showExcluded.toggle() }.font(.caption2)
            }
          }
          if pending > 0 {
            Text("有 \(pending) 条跑步的来源暂时无法确认，不会上传")
              .font(.caption2).foregroundStyle(RAITheme.secondary)
          }
        }.padding(12).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
          .accessibilityIdentifier("sync.sourcePolicyNote")
      }
    }
  }

  private func shortID(_ value: String) -> String { String(value.prefix(8)) }
}

struct WorkoutSyncDetail: View {
  @Bindable var model: SyncModel
  let workout: HealthWorkoutSummary
  @Environment(\.dismiss) private var dismiss
  @AppStorage("developerOptions") private var developerOptions = false
  private var result: WorkoutUploadResult? { model.workoutResults[workout.id] }

  private var resultLabel: String {
    switch result?.state {
    case .none: return "未上传"
    case .pending: return "等待上传"
    case .uploading: return "正在上传"
    case .succeeded: return "已上传"
    case .failed: return "上传失败"
    }
  }

  var body: some View {
    Form {
      Section("记录") {
        LabeledContent("开始", value: workout.start.formatted(date: .abbreviated, time: .standard))
        LabeledContent("时长", value: ProductFormat.duration(workout.durationS))
        LabeledContent("距离", value: workout.distanceM == nil ? "--" : ProductFormat.distance(workout.distanceM) + " km")
        LabeledContent("平均配速", value: workout.averagePaceSecondsPerM == nil ? "--" : ProductFormat.pace(workout.averagePaceSecondsPerM) + "/km")
        LabeledContent("平均心率", value: workout.averageHeartRate.map { String(format: "%.0f bpm", $0) } ?? "--")
      }
      Section("上传状态") {
        LabeledContent("状态", value: resultLabel)
        if let acceptedAt = result?.acceptedAt {
          LabeledContent("上传时间", value: acceptedAt.formatted(date: .abbreviated, time: .standard))
        }
        if developerOptions, let errorCode = result?.errorCode { LabeledContent("错误代码", value: errorCode) }
        if let reason = result?.reason { Text(reason).font(.caption).foregroundStyle(.red) }
      }
      if !workout.isEligible {
        Section("来源") {
          Text(workout.isExcluded
            ? "这条跑步不是 Apple Watch 记录的，RAI 只分析手表记录的跑步，因此不会上传。"
            : "这条跑步的来源暂时无法确认，不会上传。")
            .font(.caption)
        }
      }
      Section {
        Button(result == nil ? "上传这条记录" : "重新上传") {
          model.syncWorkouts([workout.id])
        }.disabled(!model.canStartWorkoutSync || !workout.isEligible)
        Text("上传后通常 1–2 分钟内完成分析，结果会出现在「运动」中。")
          .font(.caption)
      }
    }
    .navigationTitle("上传详情")
    .navigationBarTitleDisplayMode(.inline)
    .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("关闭") { dismiss() } } }
    .scrollContentBackground(.hidden).raiScreen()
  }
}

// MARK: - 跑力

struct RunningFitnessEntryCard: View {
  let snapshot: AthleteModelSnapshot?
  var absenceText = "正在计算"

  var body: some View {
    let metric = snapshot?.runningFitness
    let value = metric?.presentationValue
    HStack(spacing: 16) {
      ZStack {
        Circle().stroke(RAITheme.primary.opacity(0.14), lineWidth: 7)
        if let score = value?.overall.score {
          Circle()
            .trim(from: 0, to: max(0, min(score / 100, 1)))
            .stroke(RAITheme.orange.gradient, style: StrokeStyle(lineWidth: 7, lineCap: .round))
            .rotationEffect(.degrees(-90))
        }
        Text(ProductFormat.decimal(value?.overall.score, digits: 1))
          .font(.system(.title3, design: .rounded, weight: .bold)).monospacedDigit()
      }.frame(width: 64, height: 64)
      VStack(alignment: .leading, spacing: 4) {
        HStack(spacing: 6) {
          Text("跑力").font(.headline)
          Text(value.map { RunningFitnessText.stage($0.status) } ?? absenceText)
            .font(.caption2)
            .padding(.horizontal, 6).padding(.vertical, 2)
            .background(RAITheme.primary.opacity(0.10), in: Capsule())
            .foregroundStyle(RAITheme.secondary)
        }
        Text(subtitle(metric)).font(.caption).foregroundStyle(RAITheme.secondary).lineLimit(2)
      }
      Spacer(minLength: 0)
      Image(systemName: "chevron.right").font(.caption).foregroundStyle(RAITheme.secondary)
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }

  private func subtitle(_ metric: RunningFitnessMetric?) -> String {
    guard let metric else { return "单次与长期跑力、四项能力、成绩预测与配速区间" }
    guard let value = metric.presentationValue else {
      return ProductReasonText.runningFitness(
        modelStatus: metric.envelope.modelStatus.rawValue, reason: metric.envelope.displayReason?.rawValue,
        state: metric.envelope.presentationState.rawValue)
    }
    if let range = value.overall.range {
      return "范围 \(ProductFormat.decimal(range.low, digits: 0))–\(ProductFormat.decimal(range.high, digits: 0)) 分 · " + RunningFitnessText.basis(value.overall.basis)
    }
    return RunningFitnessText.basis(value.overall.basis)
  }
}

struct RunningFitnessDetailView: View {
  @Bindable var model: ProductModel
  @AppStorage(ExperimentalMetrics.key) private var showExperimental = false

  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 16) {
        AthleteRefreshNotice(snapshot: model.data)
        if let snapshot = model.data.athlete, snapshot.supportsPresentation, let metric = snapshot.runningFitness {
          if let value = metric.presentationValue {
            hero(value)
            dimensions(value)
            FitnessBasicMetricsCard(metrics: FitnessBasicMetric.metrics(snapshot: snapshot))
            predictions(value)
            zones(value)
            singleRuns(value)
          } else {
            EmptyCard(
              title: "跑力暂不可用",
              detail: ProductReasonText.runningFitness(
                modelStatus: metric.envelope.modelStatus.rawValue, reason: metric.envelope.displayReason?.rawValue,
                state: metric.envelope.presentationState.rawValue))
            FitnessBasicMetricsCard(metrics: FitnessBasicMetric.metrics(snapshot: snapshot))
          }
          if showExperimental { researchDiagnostics() }
          NavigationLink {
            RunningFitnessHistoryView(model: model)
          } label: {
            RAIRowLabel(title: "跑力历史", icon: "calendar")
          }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("runningFitness.history")
        } else {
          EmptyCard(
            title: "跑力暂不可用",
            detail: model.configured
              ? (model.data.athlete == nil ? model.data.athleteAbsenceText : "服务器正在更新模型，稍后自动恢复。")
              : "请先连接服务器。")
        }
        MetricsExplainerLink()
      }.padding(18)
    }.navigationTitle("跑力").raiScreen().refreshable { await model.refresh() }
      .task { await model.loadAthleteDiagnostics() }
      .accessibilityIdentifier("screen.runningFitness")
  }

  private func hero(_ value: RunningFitnessValue) -> some View {
    VStack(alignment: .leading, spacing: 12) {
      HStack {
        Text("跑步能力").font(.headline)
        Spacer(minLength: 8)
        Text(RunningFitnessText.stage(value.status))
          .font(.caption).padding(.horizontal, 8).padding(.vertical, 4)
          .background(RAITheme.primary.opacity(0.08), in: Capsule())
      }
      FitnessScoreGauge(score: value.overall.score)
      Text(RunningFitnessText.freshness(value.freshness)).font(.caption).foregroundStyle(RAITheme.secondary)
      if let score = value.overall.score, let range = value.overall.range {
        FitnessRangeBar(score: score, range: range)
      }
      Text(RunningFitnessText.basis(value.overall.basis) + (value.overall.range.map { range in
        range.high - range.low > 25 ? "，证据还少，范围较宽" : "" } ?? ""))
        .font(.caption).foregroundStyle(RAITheme.secondary)
      if value.stability.changeUnconfirmed {
        Text("最近一次训练让结果变化较大，等待另一次训练确认。").font(.caption).foregroundStyle(RAITheme.amber)
      }
    }.padding(20).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }

  private func dimensions(_ value: RunningFitnessValue) -> some View {
    VStack(alignment: .leading, spacing: 18) {
      CardHeading(title: "能力分析", icon: "chart.bar.doc.horizontal")
      Text("当前跑力下，四类能力对应的训练配速区间。")
        .font(.caption).foregroundStyle(RAITheme.secondary)
      ForEach(value.dimensions.ordered, id: \.key) { item in
        FitnessAbilityRow(key: item.key, dimension: item.value, zone: value.zones.zone(item.value.zone))
        if item.key != "sprint" { Divider() }
      }
    }.padding(20).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("runningFitness.dimensions")
  }

  private func predictions(_ value: RunningFitnessValue) -> some View {
    VStack(alignment: .leading, spacing: 10) {
      CardHeading(title: "成绩预测", icon: "flag.checkered")
      ForEach(value.predictions) { p in
        VStack(alignment: .leading, spacing: 2) {
          HStack(alignment: .firstTextBaseline) {
            Text(RunningFitnessText.distanceName(p.distance)).font(.subheadline)
            Spacer(minLength: 8)
            Text(RunningFitnessText.raceTime(p.seconds)).font(.headline.monospacedDigit())
          }
          HStack {
            if let range = p.range {
              Text(RunningFitnessText.raceTime(range.low) + "–" + RunningFitnessText.raceTime(range.high)).font(.caption2).monospacedDigit()
            }
            Spacer()
            Text((p.seconds.map { RunningFitnessText.pace($0 / p.distanceM * 1000) + "/km · " } ?? "")
              + RunningFitnessText.predictionSupport(p.predictionSupport)).font(.caption2).monospacedDigit()
          }.foregroundStyle(RAITheme.secondary)
          if p.longRunCovered == false {
            Text("最近长跑时长还不足以覆盖这个距离").font(.caption2).foregroundStyle(RAITheme.amber)
          }
        }
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("runningFitness.predictions")
  }

  private func zones(_ value: RunningFitnessValue) -> some View {
    VStack(alignment: .leading, spacing: 10) {
      CardHeading(title: "训练配速", icon: "speedometer")
      ForEach(value.zones.zones) { zone in
        HStack(spacing: 10) {
          Text(zone.name).font(.caption.bold()).frame(width: 34, height: 26)
            .background(fitnessZoneColor(zone.name).opacity(zone.slowPaceSecondsPerKm == nil ? 0.08 : 0.2), in: Capsule())
            .foregroundStyle(zone.slowPaceSecondsPerKm == nil ? RAITheme.secondary : fitnessZoneColor(zone.name))
          VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
              Text(RunningFitnessText.zoneName(zone.name)).font(.subheadline.weight(.semibold))
              if zone.schedulable {
                Text("计划可用").font(.caption2).padding(.horizontal, 5).padding(.vertical, 1)
                  .background(RAITheme.primary.opacity(0.12), in: Capsule()).foregroundStyle(RAITheme.primary)
              }
            }
            Text(RunningFitnessText.zoneSupport(zone)).font(.caption2).foregroundStyle(RAITheme.secondary)
          }
          Spacer(minLength: 4)
          if let pace = RunningFitnessText.zonePace(zone) {
            Text(pace).font(.subheadline.monospacedDigit())
          } else {
            Text("--").font(.subheadline).foregroundStyle(RAITheme.secondary)
          }
        }
      }
      let paces = value.racePaces.filter { $0.paceSecondsPerKm != nil && $0.schedulable }
      if !paces.isEmpty {
        Divider()
        Text("比赛配速参考").font(.caption).foregroundStyle(RAITheme.secondary)
        ForEach(paces) { p in
          HStack {
            Text(RunningFitnessText.distanceName(p.distance)).font(.caption)
            Spacer()
            Text(RunningFitnessText.pace(p.paceSecondsPerKm) + "/km").font(.caption.monospacedDigit())
            Text(p.schedulable ? "可排课" : "仅参考").font(.caption2).foregroundStyle(RAITheme.secondary)
          }
        }
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("runningFitness.zones")
  }

  private func singleRuns(_ value: RunningFitnessValue) -> some View {
    VStack(alignment: .leading, spacing: 8) {
      CardHeading(title: "近期单次跑力", icon: "chart.xyaxis.line")
      if let reference = value.referenceDailyScore {
        Text("同口径日常基准 \(ProductFormat.decimal(reference, digits: 1)) 分").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      let scored = value.singleRuns.filter { $0.score != nil }
      if scored.isEmpty {
        Text("近 42 天没有可计算单次跑力的跑步。").font(.caption).foregroundStyle(RAITheme.secondary)
      } else {
        FitnessSinglesChart(runs: scored, reference: value.referenceDailyScore)
      }
      let runs = scored.suffix(3).reversed()
      ForEach(Array(runs)) { run in
        HStack {
          Text(String(run.day.dropFirst(5)).replacingOccurrences(of: "-", with: "/")).font(.caption.monospacedDigit())
          Spacer()
          Text(ProductFormat.decimal(run.score, digits: 1)).font(.subheadline.monospacedDigit())
          if let range = run.range {
            Text("(\(ProductFormat.decimal(range.low, digits: 0))–\(ProductFormat.decimal(range.high, digits: 0)))").font(.caption2).foregroundStyle(RAITheme.secondary)
          }
          Text(RunningFitnessText.singleStatus(run.status)).font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("runningFitness.singles")
  }

  private func researchDiagnostics() -> some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "进阶指标（实验）", icon: "waveform.path.ecg")
        .accessibilityIdentifier("runningFitness.diagnostics")
      if let diagnostics = model.diagnostics, diagnostics.supportsPresentation {
        if diagnostics.items.isEmpty {
          Text("今天还没有进阶指标。")
            .font(.caption).foregroundStyle(RAITheme.secondary)
        } else {
          ForEach(diagnostics.items) { item in researchRow(item) }
        }
        Text("实验性指标，仅供观察趋势，不参与跑力和训练负荷的计算。")
          .font(.caption2).foregroundStyle(RAITheme.secondary)
      } else if let error = model.diagnosticsError {
        Text(error).font(.caption).foregroundStyle(RAITheme.secondary)
      } else {
        Text("正在读取…").font(.caption).foregroundStyle(RAITheme.secondary)
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }

  private func researchRow(_ item: AthleteDiagnosticItem) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      HStack(alignment: .firstTextBaseline, spacing: 8) {
        Text(researchTitle(item.key)).font(.subheadline.weight(.semibold))
        Spacer(minLength: 6)
        Text(researchStatusText(item.status))
          .font(.caption2).padding(.horizontal, 6).padding(.vertical, 2)
          .background(RAITheme.primary.opacity(0.10), in: Capsule())
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
    .accessibilityIdentifier("runningFitness.diagnostic." + item.key)
  }
}

/// Daily long-term line and single-run points; missing days stay empty.
struct RunningFitnessHistoryView: View {
  @Bindable var model: ProductModel
  @State private var history: RunningFitnessHistory?
  @State private var failed = false

  var body: some View {
    List {
      if let history {
        if history.days.isEmpty { Text("这段时间没有跑力结果").foregroundStyle(RAITheme.secondary) }
        ForEach(history.days) { day in
          VStack(alignment: .leading, spacing: 2) {
            HStack {
              Text(day.date).font(.subheadline.monospacedDigit())
              Spacer()
              Text(ProductFormat.decimal(day.overall.score, digits: 1) + " 分").font(.subheadline.monospacedDigit())
            }
            HStack {
              Text(RunningFitnessText.stage(day.status)).font(.caption2)
              if let ref = day.referenceDailyScore { Text("日常基准 " + ProductFormat.decimal(ref, digits: 1)).font(.caption2) }
              Spacer()
              ForEach(day.singleRuns.filter { $0.score != nil }) { run in
                Text("单次 " + ProductFormat.decimal(run.score, digits: 1)).font(.caption2)
              }
            }.foregroundStyle(RAITheme.secondary)
          }.accessibilityIdentifier("runningFitness.history.day")
        }
        Text("按当前算法重算后的历史；重算前的结果不与新刻度混合显示。").font(.caption2).foregroundStyle(RAITheme.secondary)
      } else if failed {
        Text("历史暂不可用")
      } else {
        ProgressView("正在读取历史")
      }
    }
    .scrollContentBackground(.hidden)
    .raiScreen()
    .navigationTitle("跑力历史")
    .navigationBarTitleDisplayMode(.inline)
    .task {
      do { history = try await model.loadRunningFitnessHistory() } catch { failed = true }
    }
  }
}

private func researchTitle(_ key: String) -> String {
  switch key {
  case "workout_durability": return "长距离耐力"
  case "aerobic_efficiency": return "有氧效率"
  case "critical_speed": return "临界速度"
  case "load_settling": return "负荷回落"
  default: return "其他指标"
  }
}

private func researchDetail(_ item: AthleteDiagnosticItem) -> String {
  switch item.key {
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
    if let cs = value.csMPS { parts.append("临界速度 \(ProductFormat.decimal(cs, digits: 2)) m/s") }
    if let dPrime = value.dPrimeMeters {
      parts.append("无氧储备 \(ProductFormat.decimal(dPrime, digits: 0)) m")
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
    return "暂无说明"
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
  default: return "数据还不够"
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
  default: return "暂不可用。"
  }
}

// MARK: - 训练负荷

struct TrainingLoadEntryCard: View {
  let points: [TrainingLoadPoint]

  var body: some View {
    let latest = TrainingLoadSeries.latest(points)
    HStack(spacing: 14) {
      VStack(alignment: .leading, spacing: 6) {
        HStack(spacing: 6) {
          Text("训练负荷").font(.headline)
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
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
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

  private var points: [TrainingLoadPoint] { model.trainingLoadPoints }

  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 16) {
        if let latest = TrainingLoadSeries.latest(points) {
          summary(latest)
          trend
        } else {
          EmptyCard(
            title: "暂无训练负荷",
            detail: "同步跑步并填写最大心率后，这里会显示体能与疲劳的变化。")
        }
        weekly
        Text(
          "体能反映近 6 周的训练积累，疲劳反映近 1 周的训练压力。平衡 = 体能 − 疲劳：为正说明身体已恢复，为负说明还在消化训练。"
        ).font(.caption).foregroundStyle(RAITheme.secondary)
      }.padding(18)
    }.navigationTitle("训练负荷").raiScreen()
      .refreshable { await model.refresh(); await model.loadTrainingLoadHistory() }
      .task { await model.loadTrainingLoadHistory() }
      .accessibilityIdentifier("screen.trainingLoad")
  }

  private func summary(_ latest: TrainingLoadPoint) -> some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "当前负荷", icon: "chart.line.uptrend.xyaxis")
      HStack(spacing: 10) {
        summaryTile("体能", latest.fitness, "近 6 周", RAITheme.orange)
        summaryTile("疲劳", latest.fatigue, "近 1 周", RAITheme.purple)
        summaryTile("平衡", latest.balance, "体能 − 疲劳", balanceColor(latest.balance))
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
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
      .background(RAITheme.primary.opacity(0.05), in: RoundedRectangle(cornerRadius: 14))
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
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
  }

  private var weekly: some View {
    let days = HomeDates.rollingWeek(model.data.overview)
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
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
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

private func fitnessZoneColor(_ name: String) -> Color {
  switch name {
  case "Z1": return RAITheme.secondary
  case "Z2": return RAITheme.blue
  case "Z3": return RAITheme.green
  case "Z4": return RAITheme.amber
  case "Z5": return RAITheme.orange
  default: return RAITheme.purple
  }
}

/// Score marker on the 0–100 scale with the model range drawn around it.
struct FitnessRangeBar: View {
  let score: Double
  let range: FitnessRange
  var body: some View {
    VStack(alignment: .leading, spacing: 4) {
      GeometryReader { geo in
        let w = geo.size.width
        let x = { (v: Double) in CGFloat(max(0, min(100, v)) / 100) * w }
        ZStack(alignment: .leading) {
          Capsule().fill(RAITheme.primary.opacity(0.08)).frame(height: 8)
          Capsule().fill(RAITheme.orange.opacity(0.35))
            .frame(width: max(6, x(range.high) - x(range.low)), height: 8).offset(x: x(range.low))
          Circle().fill(RAITheme.orange).overlay(Circle().stroke(.white, lineWidth: 2))
            .frame(width: 16, height: 16).offset(x: x(score) - 8)
        }.frame(height: 16)
      }.frame(height: 16)
      HStack {
        Text("0").font(.caption2)
        Spacer()
        Text("范围 \(ProductFormat.decimal(range.low, digits: 0))–\(ProductFormat.decimal(range.high, digits: 0)) 分").font(.caption2.weight(.semibold))
        Spacer()
        Text("100").font(.caption2)
      }.foregroundStyle(RAITheme.secondary).monospacedDigit()
    }.accessibilityElement(children: .ignore)
      .accessibilityLabel("范围 \(Int(range.low.rounded())) 到 \(Int(range.high.rounded())) 分")
  }
}

/// Single-run scores with their sensitivity ranges and the same-policy
/// baseline. Days without a score are simply absent.
struct FitnessSinglesChart: View {
  let runs: [FitnessSingleBrief]
  let reference: Double?
  private static let parser: DateFormatter = {
    let f = DateFormatter()
    f.calendar = Calendar(identifier: .gregorian)
    f.locale = Locale(identifier: "en_US_POSIX")
    f.timeZone = TimeZone(identifier: "UTC")
    f.dateFormat = "yyyy-MM-dd"
    return f
  }()
  private func yDomain(_ values: [(Double, FitnessRange?)]) -> ClosedRange<Double> {
    var lo = reference ?? 50, hi = reference ?? 50
    for (score, range) in values {
      lo = min(lo, range?.low ?? score, score)
      hi = max(hi, range?.high ?? score, score)
    }
    lo = max(0, (lo - 5).rounded(.down))
    hi = min(100, (hi + 5).rounded(.up))
    return lo...max(hi, lo + 10)
  }
  var body: some View {
    let points = runs.compactMap { run -> (Date, Double, FitnessRange?, String)? in
      guard let score = run.score, let day = Self.parser.date(from: run.day) else { return nil }
      return (day, score, run.range, run.workoutID)
    }
    Chart {
      if let reference {
        RuleMark(y: .value("日常基准", reference))
          .foregroundStyle(RAITheme.secondary.opacity(0.6))
          .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
      }
      ForEach(points, id: \.3) { point in
        if let range = point.2 {
          RuleMark(x: .value("日期", point.0), yStart: .value("低", range.low), yEnd: .value("高", range.high))
            .foregroundStyle(RAITheme.orange.opacity(0.3)).lineStyle(StrokeStyle(lineWidth: 4, lineCap: .round))
        }
        PointMark(x: .value("日期", point.0), y: .value("单次跑力", point.1))
          .foregroundStyle(RAITheme.orange).symbolSize(40)
      }
    }
    .chartYScale(domain: yDomain(points.map { ($0.1, $0.2) }))
    .chartXAxis { AxisMarks(values: .automatic(desiredCount: 4)) { _ in AxisGridLine(); AxisValueLabel(format: .dateTime.month(.defaultDigits).day(), centered: true) } }
    .chartYAxis { AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) }
    .frame(height: 160)
    .accessibilityLabel("近期单次跑力图，共 \(points.count) 次")
  }
}
