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
            EmptyCard(title: "运动模型", detail: model.configured ? model.data.athleteAbsenceText : "连接私有服务后查看运动模型。")
          }
        }.buttonStyle(RAIPressStyle())
          .accessibilityIdentifier(model.data.athlete?.supportsPresentation == true ? "model.home.active" : "home.status")
        AthleteRefreshNotice(snapshot: model.data)
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
        // Only the very first load shows a spinner; later refreshes are silent.
        if model.loading && model.data.workouts.isEmpty && model.data.athlete == nil {
          ProgressView("正在更新").font(.caption)
        }
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
    ProductSyncBadge.resolve(
      busy: sync.busy,
      hasError: sync.error != nil,
      hasFailedResult: sync.workoutResults.values.contains { $0.state == .failed },
      hasSucceededResult: sync.workoutResults.values.contains { $0.state == .succeeded },
      lastUploadAt: sync.lastUploadAt,
      pendingChanges: sync.pendingChanges
    ).text
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
        Text(workout.detailLine.isEmpty ? "暂无数据" : workout.detailLine)
          .font(.caption).monospacedDigit()
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
        NavigationLink {
          TrainingLoadDetailView(model: model)
        } label: {
          TrainingLoadEntryCard(points: model.trainingLoadPoints)
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("trainingLoad.entry")
        NavigationLink {
          RunningAbilityDetailView(model: model)
        } label: {
          RunningAbilityEntryCard(snapshot: model.data.athlete, absenceText: model.data.athleteAbsenceText)
        }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("runningAbility.entry")
        if let athlete = model.data.athlete, athlete.supportsPresentation {
          AthleteModelCard(snapshot: athlete)
          NavigationLink("运动模型历史") { AthleteHistoryView(model: model) }.accessibilityIdentifier("model.history.link")
        }
        AthleteRefreshNotice(snapshot: model.data)
        Button {
          showingModelInput = true
        } label: {
          Label("填写个人参考与目标", systemImage: "square.and.pencil")
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(RAIPressStyle())
        .disabled(!model.configured)
        .accessibilityHint("填写睡眠目标或 Apple VO₂ 来源；心率在「我的 → 静息与最大心率」填写")
        Text("模型结果来自已发布的分析快照；缺失、过期或同步未完成保持空白，不补零。RAI VO₂ 为实验性次最大强度估计，Apple 外部估计独立展示，二者不相加或互相校准。").font(.footnote)
          .foregroundStyle(RAITheme.secondary)
      }.padding(18)
    }.navigationTitle("分析").raiScreen()
      .refreshable { await model.refresh(); await model.loadTrainingLoadHistory() }
      .task { await model.loadTrainingLoadHistory() }
      .accessibilityIdentifier("screen.analysis")
      .sheet(isPresented: $showingModelInput) { ModelInputSheet(model: model) }
  }
}

private struct ModelInputSheet: View {
  @Bindable var model: ProductModel
  @Environment(\.dismiss) private var dismiss
  @State private var kind = "sleep_target"
  @State private var sleepHours = "8"
  @State private var sourceEpoch = ""
  @State private var vo2Sources: [VO2SourceOption] = []
  @State private var selectionRevoked = false
  @State private var effectiveDate = Date()
  @State private var busy = false
  @State private var message: String?

  var body: some View {
    NavigationStack {
      Form {
        Section("输入类型") {
          Picker("类型", selection: $kind) {
            Text("睡眠目标").tag("sleep_target")
            Text("VO₂ 来源").tag("vo2max_source_selection")
          }.pickerStyle(.segmented)
        }
        Section("生效日期") { DatePicker("日期", selection: $effectiveDate, displayedComponents: .date) }
        if kind == "sleep_target" {
          Section("睡眠目标（小时）") {
            TextField("目标睡眠时长", text: $sleepHours)
            Text("必须在 0–24 小时之间；用于恢复模型的睡眠目标组件。")
              .font(.caption).foregroundStyle(RAITheme.secondary)
          }
        } else {
          Section("Apple 外部 VO₂ 来源") {
            if vo2Sources.isEmpty {
              Text("近一年没有同步到 Apple VO₂ 估计。先在「同步与诊断」同步 VO₂。")
                .font(.caption).foregroundStyle(RAITheme.secondary)
            } else {
              Picker("来源", selection: $sourceEpoch) {
                ForEach(vo2Sources, id: \.sourceEpoch) { source in
                  Text(vo2SourceLabel(source)).tag(source.sourceEpoch)
                }
              }.pickerStyle(.inline)
            }
            Toggle("撤销该来源", isOn: $selectionRevoked)
            Text("同一设备升级系统后仍是同一来源，无需重新选择。")
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
      .task {
        vo2Sources = await model.loadVO2Sources()
        if sourceEpoch.isEmpty { sourceEpoch = vo2Sources.first?.sourceEpoch ?? "" }
      }
      .navigationTitle("个人参考与目标")
      .toolbar { ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } } }
    }
  }

  private var valid: Bool {
    if kind == "vo2max_source_selection" {
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
      if kind == "vo2max_source_selection" {
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
struct AthleteRefreshNotice: View {
  let snapshot: ProductSnapshot
  var body: some View {
    if snapshot.athleteRefreshFailed {
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
      if snapshot.physiologicalRecovery.envelope.presentationValueAvailable,
         let level = ProductReasonText.recoveryLevel(snapshot.physiologicalRecovery.value?.deviationLevel) {
        Text(level).font(.caption).foregroundStyle(RAITheme.secondary).accessibilityIdentifier("model.recovery.level")
      }
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
    metric.metric == "runningAbility"
      ? ProductReasonText.runningAbility(modelStatus: metric.modelStatus.rawValue, reason: metric.displayReason?.rawValue,
                                         state: metric.presentationState.rawValue)
      : ProductReasonText.text(for: metric.displayReason?.rawValue, state: metric.presentationState.rawValue)
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
        Text("训练负荷、准备度、VO₂ 和心率区间共用的最大/静息心率；只追加新版本，不推断年龄或性别。")
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
  /// Defaults half a year back so the reference covers the synced history the
  /// models evaluate; a later entry overrides it from its own date on.
  @State private var effectiveDate = Calendar.current.date(byAdding: .day, value: -180, to: Date()) ?? Date()
  @State private var current: JSON?
  @State private var observed: JSON?
  @State private var method = "user_entered"
  @State private var loading = true
  @State private var busy = false
  @State private var message: String?

  /// The observed 30 s HR maximum, when it exceeds (or replaces a missing)
  /// saved maximum. Only a suggestion: nothing is saved until the user does.
  private var suggestion: Double? {
    guard let value = observed?.number("value") else { return nil }
    if let saved = current?.number("maxHR"), value <= saved { return nil }
    return value
  }

  var body: some View {
    Form {
      if let suggestion {
        Section("建议") {
          Text("你在 \(observed?.string("date") ?? "") 的跑步中持续 30 秒心率达到 \(Int(suggestion)) bpm，高于当前最大心率设置。")
            .font(.footnote)
          Button("使用 \(Int(suggestion)) bpm 作为最大心率") {
            maxHR = String(Int(suggestion))
            method = "wearable_estimate"
          }.accessibilityIdentifier("hr-settings.use-observed")
        }
      }
      Section("当前生效") {
        if let current {
          LabeledContent("最大心率", value: bpm(current.number("maxHR")))
          LabeledContent("静息心率", value: current.number("restingHR") == nil ? "按近 28 天自动计算" : bpm(current.number("restingHR")))
          LabeledContent("生效日期", value: String(current.string("effectiveFrom").prefix(10)))
        } else if loading {
          ProgressView()
        } else {
          Text("尚未填写最大心率，训练负荷、准备度和 VO₂ 都无法计算。").font(.footnote)
            .foregroundStyle(RAITheme.secondary)
        }
      }
      Section {
        TextField("最大心率", text: $maxHR)
          .keyboardType(.numberPad)
          .accessibilityIdentifier("hr-settings.max")
        TextField("静息心率（可选）", text: $restingHR)
          .keyboardType(.numberPad)
          .accessibilityIdentifier("hr-settings.resting")
      } header: {
        Text("心率（bpm）")
      } footer: {
        Text("30–219 bpm，最大心率高于静息心率。静息心率留空时按 Apple 健康近 28 天的静息心率中位数自动计算。不按年龄推断。")
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
        Button(busy ? "保存中…" : "保存") { submit() }
          .disabled(busy || !valid)
          .accessibilityIdentifier("hr-settings.save")
      } footer: {
        Text("训练负荷、准备度、VO₂ 和训练详情的心率区间都使用这里的数值。生效日期之后的训练（包括已同步的历史）按新值计算；之前的训练保留旧值。")
      }
    }
    .navigationTitle("静息与最大心率")
    .raiScreen()
    .accessibilityIdentifier("screen.hr-settings")
    .task { await reload() }
  }

  private var valid: Bool {
    guard let max = Double(maxHR), max >= 30, max < 220 else { return false }
    if restingHR.isEmpty { return true }
    guard let rest = Double(restingHR), rest >= 30, rest < max else { return false }
    return true
  }

  private func bpm(_ value: Double?) -> String {
    guard let value else { return "--" }
    return String(format: "%.0f bpm", value)
  }

  private func reload() async {
    loading = true
    let loaded = await model.loadHRReference()
    current = loaded
    observed = await model.loadObservedMaxHR()
    if let loaded {
      maxHR = text(loaded.number("maxHR"))
      restingHR = text(loaded.number("restingHR"))
    }
    loading = false
  }

  private func text(_ value: Double?) -> String {
    guard let value else { return "" }
    return value == value.rounded() ? String(format: "%.0f", value) : String(format: "%g", value)
  }

  private func submit() {
    guard valid, let max = Double(maxHR) else { return }
    busy = true
    message = nil
    let measured = ISO8601DateFormatter().string(from: effectiveDate)
    var payload: [String: JSON] = [
      "maxHR": .number(max), "method": .string(method),
      "measuredAt": .string(measured), "unit": .string("bpm"),
    ]
    if let rest = Double(restingHR) {
      payload["restingHR"] = .number(rest)
      payload["restingMeasuredAt"] = .string(measured)
    }
    Task {
      let failure = await model.submitModelInput(kind: "hr_reference", payload: payload, effectiveFrom: effectiveDate)
      await MainActor.run {
        busy = false
        if let failure { message = failure } else { dismiss() }
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

/// One HKWorkout = one protocol-6 payload. A round is strictly sequential and
/// ends with only two final per-record states: 上传成功 or 上传失败.
struct WorkoutSyncView: View {
  @Bindable var model: SyncModel
  @State private var filter: Filter = .all
  @State private var detail: HealthWorkoutSummary?
  @State private var exportStatus: String?
  @State private var showExcluded = false
  private enum Filter: String, CaseIterable { case all = "全部", unsynced = "未上传", failed = "失败", succeeded = "成功" }

  var body: some View {
    ScrollView {
      LazyVStack(spacing: 12) {
        intro
        if model.loadingWorkouts { ProgressView("正在读取 HealthKit 跑步记录…").font(.caption) }
        batchSummary
        selectionBar
        sourcePolicyNote
        ForEach(filtered) { workout in
          row(workout)
        }
        if filtered.isEmpty && !model.loadingWorkouts {
          EmptyCard(title: "没有符合条件的记录", detail: "只显示真实 HealthKit 跑步记录；缺失数据以 -- 显示。")
        }
        if model.hasMoreWorkouts {
          Button {
            Task { await model.loadMoreWorkouts() }
          } label: {
            HStack {
              if model.loadingMoreWorkouts { ProgressView().controlSize(.small) }
              Text(model.loadingMoreWorkouts ? "正在读取更早记录…" : "加载更早记录")
            }.frame(maxWidth: .infinity)
          }.font(.caption)
            .disabled(!model.canLoadMoreWorkouts)
            .accessibilityIdentifier("sync.loadMore")
        } else if !model.workouts.isEmpty {
          Text("已加载全部历史记录（\(model.workouts.count) 条）")
            .font(.caption2).foregroundStyle(RAITheme.secondary)
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
    // The upload belongs to SyncModel, not to this page: leaving the page (or
    // closing the detail sheet) must not stop it. Only the cancel button does.
    .sheet(item: $detail) { workout in
      NavigationStack { WorkoutSyncDetail(model: model, workout: workout) }
    }
    .accessibilityIdentifier("screen.workout-sync")
  }

  private var selectionEditable: Bool {
    !model.busy && !model.loadingWorkouts && !model.loadingMoreWorkouts
  }

  private var intro: some View {
    let succeeded = model.workoutResults.values.filter { $0.state == .succeeded }.count
    let failed = model.workoutResults.values.filter { $0.state == .failed }.count
    return VStack(alignment: .leading, spacing: 10) {
      HStack {
        Image(systemName: "arrow.triangle.2.circlepath").foregroundStyle(RAITheme.amber)
        Text("跑步记录同步").font(.headline)
      }
      Text("新的跑步记录会在跑完约 10 分钟后自动上传。")
        .font(.caption).foregroundStyle(RAITheme.secondary)
      HStack(spacing: 12) {
        Text("已同步 \(succeeded) 条").font(.caption.weight(.semibold))
        if failed > 0 { Text("失败 \(failed) 条（可重试）").font(.caption.weight(.semibold)).foregroundStyle(.red) }
        Spacer()
        if let last = model.lastUploadAt {
          Text("最近 \(last, format: .dateTime.month().day().hour().minute())").font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      HStack(spacing: 10) {
        Label(model.authorization, systemImage: "checkmark.shield").font(.caption2).foregroundStyle(RAITheme.secondary)
        Spacer()
        Button("请求读取权限") { model.authorize() }.font(.caption).disabled(model.busy)
      }
      if model.busy {
        HStack(spacing: 8) {
          ProgressView().controlSize(.small)
          Text(model.status).font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if let exportStatus { Text("已导出：\(exportStatus)").font(.caption2).foregroundStyle(RAITheme.green) }
    }.padding(16).raiGlass()
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
          Button("取消上传") { model.cancelWorkoutSync() }.font(.caption)
        }
      }.padding(14).raiGlass()
        .accessibilityIdentifier("sync.batchSummary")
    }
  }

  private var selectionBar: some View {
    VStack(spacing: 10) {
      Picker("筛选", selection: $filter) {
        ForEach(Filter.allCases, id: \.self) { Text($0.rawValue).tag($0) }
      }.pickerStyle(.segmented)
      HStack {
        Text("更早的历史记录不会自动上传。").font(.caption2).foregroundStyle(RAITheme.secondary)
        Spacer()
        Button("补传历史记录") { Task { await model.backfillUnsyncedWorkouts() } }
          .font(.caption.weight(.semibold))
          .disabled(!model.canStartWorkoutSync || !selectionEditable)
          .accessibilityIdentifier("sync.backfill")
      }
    }.padding(14).raiGlass()
  }

  private func row(_ workout: HealthWorkoutSummary) -> some View {
    let result = model.workoutResults[workout.id]
    return VStack(alignment: .leading, spacing: 10) {
      HStack(alignment: .top, spacing: 10) {
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
        if workout.isEligible { resultBadge(result) } else {
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
        if workout.isEligible {
          Button(result == nil ? "同步" : "重新同步") { model.syncWorkouts([workout.id]) }
            .font(.caption.weight(.semibold))
            .disabled(!model.canStartWorkoutSync || result?.state == .pending || result?.state == .uploading)
        } else {
          Text(workout.isExcluded ? "非 Apple Watch 原生来源，不上传" : "来源待识别，暂不上传")
            .font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
      if let result, result.state == .failed, let reason = result.reason {
        Text(reason).font(.caption2).foregroundStyle(.red)
      }
    }.padding(16).raiGlass()
      .accessibilityIdentifier("sync.workout.row")
  }

  private func metric(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 1) {
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(value).font(.caption.monospacedDigit()).foregroundStyle(.white)
    }
  }

  private func resultBadge(_ result: WorkoutUploadResult?) -> some View {
    let label: String, color: Color
    switch result?.state {
    case .none: label = "未上传"; color = RAITheme.secondary
    case .pending: label = "等待上传"; color = RAITheme.secondary
    case .uploading: label = "正在上传"; color = RAITheme.amber
    case .succeeded: label = "上传成功"; color = RAITheme.green
    case .failed: label = "失败，可重试"; color = .red
    }
    return Text(label).font(.caption2.weight(.semibold))
      .padding(.horizontal, 8).padding(.vertical, 4)
      .background(color.opacity(0.18), in: Capsule())
      .foregroundStyle(color)
      .accessibilityIdentifier("sync.result")
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
              Text("已加载记录中有 \(excluded) 条非 Apple Watch 原生跑步记录（第三方 App 或手工录入）")
                .font(.caption2).foregroundStyle(RAITheme.secondary)
              Spacer(minLength: 0)
              Button(showExcluded ? "收起" : "查看") { showExcluded.toggle() }.font(.caption2)
            }
          }
          if pending > 0 {
            Text("已加载记录中有 \(pending) 条来源暂无法确认，不会上传")
              .font(.caption2).foregroundStyle(RAITheme.secondary)
          }
        }.padding(12).raiGlass()
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
  private var result: WorkoutUploadResult? { model.workoutResults[workout.id] }

  private var resultLabel: String {
    switch result?.state {
    case .none: return "未上传"
    case .pending: return "等待上传"
    case .uploading: return "正在上传"
    case .succeeded: return "上传成功"
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
      Section("同步结果") {
        LabeledContent("状态", value: resultLabel)
        if let acceptedAt = result?.acceptedAt {
          LabeledContent("服务端接收", value: acceptedAt.formatted(date: .abbreviated, time: .standard))
        }
        if let errorCode = result?.errorCode { LabeledContent("错误代码", value: errorCode) }
        if let reason = result?.reason { Text(reason).font(.caption).foregroundStyle(.red) }
      }
      if !workout.isEligible {
        Section("来源策略") {
          Text(workout.isExcluded
            ? "这条记录不是 Apple Watch 原生来源，按来源策略不会上传。"
            : "这条记录的来源暂无法确认，不会上传。")
            .font(.caption)
        }
      }
      Section {
        Button(result == nil ? "同步这条记录" : "重新同步") {
          model.syncWorkouts([workout.id])
        }.disabled(!model.canStartWorkoutSync || !workout.isEligible)
        Text("上传成功表示服务端已耐久接收原始载荷；不等待入库或分析完成，也不会自动检查后续状态。")
          .font(.caption)
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
  var absenceText = "结果尚未就绪"

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
          Text(metric.map { runningAbilityStateText($0.envelope) } ?? absenceText)
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
      return ProductReasonText.runningAbility(
        modelStatus: metric.envelope.modelStatus.rawValue, reason: metric.envelope.displayReason?.rawValue,
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
        AthleteRefreshNotice(snapshot: model.data)
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
              detail: ProductReasonText.runningAbility(
                modelStatus: metric.envelope.modelStatus.rawValue, reason: metric.envelope.displayReason?.rawValue,
                state: metric.envelope.presentationState.rawValue))
          }
          researchDiagnostics()
          NavigationLink("跑力历史") {
            AthleteHistoryView(model: model, initialMetric: "runningAbility")
          }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("runningAbility.history")
        } else {
          EmptyCard(
            title: "跑力暂不可用",
            detail: model.configured
              ? model.data.athleteAbsenceText
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

  /// Zones and projections prefer the experimental maximal ability (built
  /// only from runs the user marked as all-out); otherwise the demonstrated
  /// ability, which is usually slower than race capacity.
  private func projectionBasis(_ value: RunningAbilityValue) -> (v60: Double?, gamma: Double?, maximal: Bool) {
    if let maximal = model.diagnostics?.items.compactMap(\.maximal).first, let v60 = maximal.v60 {
      return (v60, maximal.gamma, true)
    }
    return (value.speedUnit.rawValue == "m/s" ? value.v60 : nil, value.gamma, false)
  }

  private func basisNote(_ maximal: Bool) -> String {
    maximal ? "依据：你标记为全力的跑步（实验）。" : "依据：日常训练表现，通常慢于比赛能力；在跑步详情中标记全力跑可改用全力表现。"
  }

  private func paceZones(_ value: RunningAbilityValue) -> some View {
    let basis = projectionBasis(value)
    let zones = RunningAbilityMath.paceZones(v60: basis.v60)
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
        Text(basisNote(basis.maximal)).font(.caption2).foregroundStyle(RAITheme.secondary)
      }
    }.padding(18).raiGlass()
      .accessibilityIdentifier("runningAbility.zones")
  }

  private func racePredictions(_ value: RunningAbilityValue) -> some View {
    let basis = projectionBasis(value)
    let items = RunningAbilityMath.racePredictions(v60: basis.v60, gamma: basis.gamma)
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
      Text("按你的耐力指数 γ 由 V60 外推（γ 缺失时用 Riegel 1.06）；距离越远，误差越大。" + basisNote(basis.maximal))
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
  guard let envelope else { return "结果尚未就绪" }
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
            detail: "同步跑步后，服务端会按 7/42 天指数核生成体能与疲劳。")
        }
        weekly
        Text(
          "体能（CTL）为 42 天指数核，疲劳（ATL）为 7 天指数核，平衡=体能−疲劳；来自 RAI 运动模型（v2）已发布的每日结果，少量未知负荷按典型训练日估算并标注。"
        ).font(.caption2).foregroundStyle(RAITheme.secondary)
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
