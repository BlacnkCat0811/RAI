import ProductCore
import SwiftUI
import SyncCore

struct TrainingView: View {
  @Bindable var model: ProductModel
  @State private var date = ""
  @State private var create = false
  @State private var editGoal = false
  @State private var add = false
  @State private var selectedSession: ProductSession?
  @Namespace private var transition
  var dates: [String] { Array(Set(model.data.sessions.map(\.date))).sorted() }
  var selected: String {
    date.isEmpty ? (dates.contains(model.data.today) ? model.data.today : dates.first ?? "") : date
  }
  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 16) {
        if model.data.plan == .null {
          EmptyCard(
            title: "创建你的训练计划",
            detail: "选择每周可训练的日子即可开始。有比赛时按比赛日期倒排各阶段；没有比赛时根据你的训练情况每周自动安排，每次跑步不少于 30 分钟。")
          OrangeButton(title: "开始自适应训练", icon: "plus") { create = true }.accessibilityIdentifier(
            "training.create")
        } else if AdaptivePlan(plan: model.data.plan).isAdaptive {
          AdaptivePlanCard(model: model, onEditGoal: { editGoal = true })
        } else {
          VStack(alignment: .leading, spacing: 10) {
            CardHeading(title: "当前训练计划", icon: "flag.checkered")
            MetricText(
              value: ProductFormat.distance(model.data.plan["config"]["goal"].number("distanceM")),
              unit: "公里目标")
            Text("目标日 \(DateOnly.display(model.data.plan["config"]["goal"].string("date"), pattern: "yyyy年M月d日"))").font(.subheadline)
              .foregroundStyle(RAITheme.secondary)
            Text("共 \(Int(model.data.plan["config"].number("weeks") ?? 0)) 周").font(.caption)
              .foregroundStyle(RAITheme.secondary)
            if model.data.plan.string("generationState") != "succeeded" {
              Label("计划更新中，暂时沿用当前安排", systemImage: "arrow.triangle.2.circlepath").font(.caption)
                .foregroundStyle(RAITheme.amber)
            }
            if let code = model.data.generationFailure {
              Text("计划没有更新成功：\(UserFacingText.generationFailure(code))").font(.caption).foregroundStyle(RAITheme.amber)
            }
            Button("改用自适应计划", systemImage: "wand.and.stars") { create = true }
              .font(.subheadline).accessibilityIdentifier("training.upgrade")
          }.frame(maxWidth: .infinity, alignment: .leading).padding(22).raiGlass()
        }
        if model.data.plan != .null {
          ScrollViewReader { proxy in
          ScrollView(.horizontal) {
            HStack(spacing: 8) {
              ForEach(dates, id: \.self) { day in
                Button {
                  date = day
                  UISelectionFeedbackGenerator().selectionChanged()
                } label: {
                  VStack(spacing: 4) {
                    Text(DateOnly.display(day, pattern: "EEE")).font(.caption2)
                      .foregroundStyle(RAITheme.secondary)
                    Text(DateOnly.display(day, pattern: "d")).font(.headline).monospacedDigit()
                    Text(day == model.data.today ? "今天" : DateOnly.display(day, pattern: "M月"))
                      .font(.caption2)
                      .foregroundStyle(day == model.data.today ? RAITheme.orange : RAITheme.secondary)
                  }.frame(minWidth: 44).padding(.vertical, 10).padding(.horizontal, 6).background(
                    day == selected ? RAITheme.orange.opacity(0.18) : RAITheme.primary.opacity(0.04),
                    in: RoundedRectangle(cornerRadius: 18))
                }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("training.date." + day)
                  .id(day)
              }
            }
          }.scrollIndicators(.hidden)
            // Weeks into a plan, today sits far to the right of the strip.
            .task(id: dates.count) { proxy.scrollTo(selected, anchor: .center) }
          }
          ForEach(model.data.sessions.filter { $0.date == selected }) { s in
            Button {
              selectedSession = s
            } label: {
              SessionCard(session: s)
            }.buttonStyle(RAIPressStyle()).matchedTransitionSource(id: s.id, in: transition)
              .accessibilityIdentifier("session." + String(s.raw.number("slot") ?? -1))
          }
          if model.data.sessions.filter({ $0.date == selected }).isEmpty {
            Text("这一天没有训练安排").font(.subheadline).foregroundStyle(RAITheme.secondary)
              .frame(maxWidth: .infinity).padding(.vertical, 12)
          }
          HStack {
            Button("添加训练", systemImage: "plus") { add = true }.accessibilityIdentifier(
              "training.add")
            Spacer()
            Button("重新生成", systemImage: "arrow.clockwise") { Task { await model.recalculate() } }
              .disabled(model.loading).accessibilityIdentifier("training.recalculate")
          }.buttonStyle(RAISecondaryButtonStyle())
          if !model.execution.string("executionID").isEmpty {
            NavigationLink {
              ExecutionView(model: model)
            } label: {
              RAIRowLabel(title: "最近一次训练与反馈", icon: "checkmark.circle")
            }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("training.execution")
          }
          let notes = model.data.effective["recommendations"].list.compactMap(\.text).map(reasonText).filter { !$0.isEmpty }
          if !notes.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
              CardHeading(title: "计划说明", icon: "text.bubble")
              ForEach(notes, id: \.self) { note in
                Label(note, systemImage: "circle.fill").labelStyle(BulletLabelStyle())
              }
            }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
          }
        }
        if let error = model.error { RAINotice(text: error) }
      }.padding(18)
    }.navigationTitle("训练").raiScreen().refreshable { await model.refresh() }
      // Plans are generated by the Worker after a create, a goal change, an
      // override or a new day. Poll briefly so the new version replaces the
      // "updating" notice without a manual pull.
      .task(id: model.data.plan.string("generationState") + model.data.plan.string("configRevision")
        + model.data.effective.string("effectiveVersionID"))
      {
        for _ in 0..<20 {
          guard model.data.plan != .null,
            ["pending", "queued", "running"].contains(model.data.plan.string("generationState"))
          else { return }
          try? await Task.sleep(for: .seconds(2))
          if Task.isCancelled { return }
          await model.refresh()
        }
      }
      .sheet(isPresented: $create) { CreatePlanView(model: model) }
      .sheet(isPresented: $editGoal) { CreatePlanView(model: model, editing: true) }
      .sheet(isPresented: $add) {
        SessionEditor(model: model, session: nil, operation: "add", effective: model.data.effective)
      }.navigationDestination(item: $selectedSession) { session in
        SessionDetail(model: model, session: session, effective: model.data.effective).modifier(
          RAIDetailTransition(id: session.id, namespace: transition))
      }.accessibilityIdentifier("screen.training")
  }
}
/// Header of an adaptive plan: the goal (or fitness mode), this week's state
/// and progress, next week's preview and, for a race, the phase outline.
struct AdaptivePlanCard: View {
  @Bindable var model: ProductModel
  var onEditGoal: () -> Void
  var plan: AdaptivePlan { AdaptivePlan(plan: model.data.plan) }
  var body: some View {
    VStack(alignment: .leading, spacing: 12) {
      if let goal = plan.goal, plan.isRace {
        CardHeading(title: "比赛目标", icon: "flag.checkered")
        MetricText(value: AdaptivePlanText.category(goal.category), unit: ProductFormat.distance(goal.distanceM) + " 公里")
        Text(
          "比赛日 \(DateOnly.display(goal.date, pattern: "yyyy年M月d日")) · "
            + (goal.daysToRace > 0 ? "还有 \(goal.daysToRace) 天" : goal.daysToRace == 0 ? "就是今天" : "已结束")
        ).font(.subheadline).foregroundStyle(RAITheme.secondary)
        Text("当前阶段：" + AdaptivePlanText.phase(goal.phase)).font(.subheadline.weight(.semibold))
          .accessibilityIdentifier("training.phase")
        if let target = goal.targetTimeS {
          Text(
            "目标 \(AdaptivePlanText.clock(target))"
              + (goal.predictedTimeS.map { " · 当前预测 " + AdaptivePlanText.clock($0) } ?? "")
          ).font(.subheadline).monospacedDigit()
        } else if let predicted = goal.predictedTimeS {
          Text("当前预测 " + AdaptivePlanText.clock(predicted)).font(.subheadline).monospacedDigit()
        }
        let assessment = AdaptivePlanText.assessment(goal.assessment)
        if !assessment.isEmpty {
          Text(assessment).font(.caption).foregroundStyle(goal.assessment == "out_of_reach" ? RAITheme.amber : RAITheme.secondary)
        }
        if let pace = goal.racePace {
          Text("比赛配速课 \(ProductFormat.duration(pace))/公里").font(.caption).foregroundStyle(RAITheme.secondary)
        }
      } else {
        CardHeading(title: "自适应训练", icon: "figure.run")
        Text("没有设置比赛目标，根据你最近的训练情况每周自动安排。").font(.subheadline)
          .foregroundStyle(RAITheme.secondary)
        if let goal = plan.goal, goal.completed {
          Text("目标比赛已结束，已回到日常训练。").font(.caption).foregroundStyle(RAITheme.secondary)
        }
      }
      if let week = plan.current {
        Divider()
        HStack {
          Text("本周 · " + AdaptivePlanText.state(week.state)).font(.headline)
            .accessibilityIdentifier("training.week")
          Spacer()
          Text("\(week.actualMinutes ?? 0) / \(week.plannedMinutes) 分钟").font(.subheadline).monospacedDigit()
            .foregroundStyle(RAITheme.secondary)
        }
        ProgressView(value: week.progress).tint(RAITheme.orange)
        let detail = AdaptivePlanText.stateDetail(week.state)
        if !detail.isEmpty { Text(detail).font(.caption).foregroundStyle(RAITheme.secondary) }
      }
      if let next = plan.next {
        Text("下周预览：\(AdaptivePlanText.state(next.state)) · 约 \(next.plannedMinutes) 分钟")
          .font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if plan.isRace {
        let phases = phaseRuns
        if phases.count > 1 {
          ScrollView(.horizontal) {
            HStack(spacing: 6) {
              ForEach(Array(phases.enumerated()), id: \.offset) { i, run in
                Text("\(AdaptivePlanText.phase(run.phase)) \(run.weeks)周")
                  .font(.caption2.weight(i == 0 ? .semibold : .regular))
                  .padding(.horizontal, 8).padding(.vertical, 4)
                  .background(i == 0 ? RAITheme.orange.opacity(0.18) : RAITheme.primary.opacity(0.06), in: Capsule())
              }
            }
          }.scrollIndicators(.hidden)
        }
      }
      if model.data.plan.string("generationState") != "succeeded" {
        Label("计划更新中，暂时沿用当前安排", systemImage: "arrow.triangle.2.circlepath").font(.caption)
          .foregroundStyle(RAITheme.amber)
      }
      if let code = model.data.generationFailure {
        Text("计划没有更新成功：\(UserFacingText.generationFailure(code))").font(.caption).foregroundStyle(RAITheme.amber)
      }
      Button(plan.hasConfiguredGoal ? "修改目标与训练日" : "设置比赛目标", systemImage: "slider.horizontal.3") {
        onEditGoal()
      }.font(.subheadline).accessibilityIdentifier("training.editGoal")
    }.frame(maxWidth: .infinity, alignment: .leading).padding(22).raiGlass()
  }
  /// Consecutive weeks of the same phase from this week on.
  var phaseRuns: [(phase: String, weeks: Int)] {
    var out: [(phase: String, weeks: Int)] = []
    for w in plan.weeks where ["current", "next", "outline"].contains(w.detail) {
      if let last = out.last, last.phase == w.phase {
        out[out.count - 1].weeks += 1
      } else {
        out.append((w.phase, 1))
      }
    }
    return out
  }
}
struct BulletLabelStyle: LabelStyle {
  func makeBody(configuration: Configuration) -> some View {
    HStack(alignment: .firstTextBaseline, spacing: 8) {
      configuration.icon.font(.system(size: 5)).foregroundStyle(RAITheme.secondary)
      configuration.title.font(.footnote).foregroundStyle(RAITheme.secondary)
        .fixedSize(horizontal: false, vertical: true)
    }
  }
}
struct RAIDetailTransition: ViewModifier {
  let id: String, namespace: Namespace.ID
  @Environment(\.accessibilityReduceMotion) private var reduced
  func body(content: Content) -> some View {
    if reduced || !RAIRenderer.metalAvailable {
      content
    } else {
      content.navigationTransition(.zoom(sourceID: id, in: namespace))
    }
  }
}
struct SessionCard: View {
  let session: ProductSession
  var body: some View {
    VStack(alignment: .leading, spacing: 10) {
      HStack {
        Text(DateOnly.display(session.date)).font(.caption).foregroundStyle(RAITheme.secondary)
        Spacer()
        if let outcome = AdaptivePlanText.outcome(session.outcome) {
          Text(outcome).font(.caption2.weight(.semibold))
            .foregroundStyle(session.outcome == "missed" ? RAITheme.amber : RAITheme.secondary)
        }
        if session.modified {
          Text("已调整").font(.caption2.weight(.semibold)).foregroundStyle(RAITheme.amber)
        } else if session.adapted {
          Text("自动调整").font(.caption2.weight(.semibold)).foregroundStyle(RAITheme.amber)
        }
      }
      Text(session.title).font(.title2.bold())
      Text(prescription(session)).font(.subheadline).monospacedDigit()
      let purpose = AdaptivePlanText.purpose(session.purpose)
      if !purpose.isEmpty {
        Text("目的：" + purpose).font(.caption).foregroundStyle(RAITheme.secondary)
      }
      HStack {
        Text("自感用力 \(Int(session.raw.number("effort") ?? 0))")
        Spacer()
        Text(statusText(session.raw.string("status")))
      }.font(.caption).foregroundStyle(RAITheme.secondary)
    }.frame(maxWidth: .infinity, alignment: .leading).padding(22).raiGlass().foregroundStyle(RAITheme.primary)
  }
}
func statusText(_ status: String) -> String {
  ["scheduled": "已安排", "planned": "已计划", "started": "进行中", "completed": "已完成", "skipped": "已跳过", "cancelled": "已取消"][
    status] ?? "状态未知"
}
func reasonText(_ reason: String) -> String {
  if reason.hasSuffix("_override_incompatible_with_session_structure/v1") {
    return fieldTitle(reason.components(separatedBy: "_override")[0])
      + "的手动调整与新计划不兼容，目前按自动计划执行；请修改或撤销这项调整。"
  }
  return [
    "aerobic_base": "建立有氧耐力", "long_run_progression": "循序增加长距离训练",
    "minimum_recovery": "安排休息，控制训练负荷", "goal_specific_tempo": "目标相关节奏训练",
    "goal_specific_interval": "目标相关间歇训练", "load_only_advisory": "训练状态仅按训练负荷估算",
    "user_override_review": "包含你的手动调整，请结合实际状态确认", "readiness_adjustment": "近期负荷偏高，自动建议已降低",
    "goal_risk": "当前基础与目标存在差距，请确认比赛安排", "target_time_not_optimized": "目标时间尚未用于配速优化",
    "feedback_adjustment": "根据已完成训练反馈保守调整", "duration_override_unstructured": "时长已调整，这次训练不再分段",
    "cold_start_conservative": "训练历史不足，按你填写的当前基础保守安排",
    "readiness_unavailable": "准备度数据不足，未用于自动训练调整",
    "weekly_load_constraint": "按周跑量预算安排训练", "weekly_progression": "按计划渐进增加周跑量",
    "recovery_week": "本周降低训练量", "recovery_after_hard_session": "关键训练后安排恢复",
    "taper": "赛前减量：降低跑量，保留强度", "goal_event": "目标比赛日",
    "post_event_recovery": "赛后恢复",
    "base_session_removed_kept": "计划中已没有这次训练，保留了你的调整",
    "override_rebase_conflict/v1": "计划更新后，之前的手动调整需要你重新确认。",
    "explicit_skip": "本次训练已跳过", "analysis_pending": "跑步分析还没完成",
    "no_comparable_target": "没有可比较的计划目标", "duration_distance_disagree": "距离与时长表现不一致",
    "user_added": "你手动添加的训练",
    // Adaptive plan (training_plan/v4).
    "mode_fitness": "没有比赛目标：根据近期训练情况每周自动安排", "mode_race": "按比赛日期倒排训练阶段",
    "week_state_start": "本周按近期训练量起步", "week_state_progress": "上周完成良好，本周小幅加量",
    "week_state_hold": "本周维持训练量", "week_state_recover": "本周为恢复周，降低训练量",
    "week_state_regress": "上周完成较少，回到近期实际水平", "week_state_return": "训练中断后逐步恢复",
    "week_state_taper": "赛前减量", "week_state_race": "比赛周", "week_state_post": "赛后恢复",
    "phase_base": "基础期：打好有氧基础", "phase_build": "提升期：加入针对性的强度课",
    "phase_peak": "巅峰期：比赛专项训练", "phase_taper": "减量期：降低跑量、保留强度",
    "phase_post": "赛后恢复期", "phase_pre": "距备赛开始还早，先按日常训练积累",
    "phase_peak_hold": "巅峰期维持训练量，不再加量", "planned_recovery_week": "连续加量三周后安排恢复周",
    "load_ramp_guard": "近期负荷上升过快，暂停加量", "readiness_week_low": "近期准备度偏低，本周降低训练量",
    "week_preview": "下周安排会在下周一按本周完成情况确认",
    "cold_start_user_baseline": "训练记录不足，按你填写的每周跑步时长安排",
    "minimum_session_floor": "每次跑步不少于 30 分钟，训练量少时减少次数而不是缩短时长",
    "low_base_floor": "近期训练量很少，本周先安排 1 次 30 分钟",
    "session_count_by_budget": "本周训练量有限，这一天安排休息",
    "missed_key_session_moved": "错过的重点课已顺延到本周后面", "missed_key_session_dropped": "错过的重点课本周已无合适日期，不再补",
    "extra_volume_rebalanced": "本周已多跑，后面的轻松跑相应减少",
    "daily_readiness_reschedule": "今天恢复不足，重点课后移，今天改为恢复跑", "readiness_rest": "今天准备度很低，建议休息",
    "session_recovery_pending": "上次训练的恢复还没结束，暂不安排强度课", "session_recovery_reschedule": "按恢复时间后移强度课",
    "load_focus_low_aerobic_shortage": "近期低强度有氧不足，本周以轻松跑为主",
    "load_focus_high_aerobic_shortage": "近期高强度有氧不足，安排阈值或最大摄氧课",
    "load_focus_anaerobic_shortage": "近期无氧刺激不足，轻松跑末段加入加速跑",
    "load_focus_balanced": "近期负荷分布均衡", "load_focus_unavailable": "负荷焦点数据不足，按固定轮换安排强度课",
    "quality_tempo": "节奏跑：提升有氧能力上限", "quality_threshold": "阈值跑：提升乳酸阈",
    "quality_vo2max": "间歇：提升最大摄氧量", "quality_race_pace": "按比赛配速练习",
    "quality_race_wakeup": "赛前短时唤醒", "strides_neuromuscular": "末段加速跑，提升速度感",
    "pre_race_rest": "比赛前一天休息", "plan_not_started": "计划开始前", "quality_session_not_placed": "部分强度课因间隔要求未安排",
    "goal_short_notice": "距比赛时间较短，计划已压缩", "goal_prep_not_started": "距备赛开始还早",
    "goal_prediction_unavailable": "跑力预测不足，无法评估目标成绩", "goal_pace_capped_to_ability": "目标配速快于当前能力，配速课按当前能力安排",
    "goal_completed_auto_fitness": "比赛已结束，回到日常自适应训练", "pace_zone_unavailable_hr_only": "暂无可用配速区间，按心率执行",
  ][reason] ?? ""
}
/// Why the adaptive plan changed this day, in the user's words.
func adaptationText(_ s: ProductSession) -> String? {
  guard s.adapted else { return nil }
  let c = s.changedFrom
  let was = ProductSession(.object(["type": c["type"], "focus": c["focus"]])).title
  let minutes = Int(((c.number("durationS") ?? 0) / 60).rounded())
  let original = c.string("date") == s.date ? "" : "（原定 \(DateOnly.display(c.string("date"), pattern: "M月d日"))）"
  switch c.string("reasonCode") {
  case "missed_key_session_moved": return "错过的训练顺延到这一天\(original)，替换了原来的\(was)。"
  case "daily_readiness_reschedule":
    return c.string("date") == s.date ? "今天恢复不足，原定的\(was)已后移。" : "因恢复不足从\(original.isEmpty ? "当天" : original)后移到这一天。"
  case "session_recovery_reschedule":
    return c.string("date") == s.date ? "上次训练的恢复还没结束，原定的\(was)已调整。" : "按恢复时间后移到这一天\(original)。"
  case "extra_volume_rebalanced": return "本周已多跑，这次训练由 \(minutes) 分钟调整。"
  default: return nil
  }
}
struct SessionDetail: View {
  @Bindable var model: ProductModel
  let session: ProductSession, effective: JSON
  @State private var editor: String?
  @State private var execution = false
  @Environment(\.dynamicTypeSize) private var typeSize
  var current: ProductSession { model.data.sessions.first { $0.id == session.id } ?? session }
  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 18) {
        VStack(alignment: .leading, spacing: 12) {
          Text(DateOnly.display(current.date)).font(.subheadline)
          Text(current.title).font(.system(.largeTitle, design: .rounded, weight: .bold))
          Text(prescription(current)).font(.title2).monospacedDigit()
          Text("自感用力 \(Int(current.raw.number("effort") ?? 0))").font(.subheadline)
          let purpose = AdaptivePlanText.purpose(current.purpose)
          if !purpose.isEmpty { Text("目的：" + purpose).font(.subheadline) }
          if current.modified {
            Text("已手动调整").font(.caption).foregroundStyle(RAITheme.amber)
          }
          if let note = adaptationText(current) {
            Text(note).font(.caption).foregroundStyle(RAITheme.amber).accessibilityIdentifier("session.adapted")
          }
        }.padding(24).frame(maxWidth: .infinity, minHeight: 215, alignment: .leading).background {
          HeroScene().clipShape(RoundedRectangle(cornerRadius: 28))
        }.raiGlass()
        if current.runnable {
          OrangeButton(title: "开始训练", icon: "play.fill") {
            Task {
              if await model.start(current, effective: model.data.effective) { execution = true }
            }
          }.disabled(model.loading).accessibilityIdentifier("session.start")
        }
        if typeSize.isAccessibilitySize {
          VStack(spacing: 10) { actions }
        } else {
          HStack(spacing: 10) { actions }
        }
        VStack(alignment: .leading, spacing: 12) {
          CardHeading(title: "训练内容", icon: "list.bullet")
          if let hr = current.raw.number("targetHR") {
            Text("目标心率 \(ProductFormat.decimal(hr)) 次/分")
          }
          if let pace = current.raw.number("paceSPerKM") {
            Text("目标配速 \(ProductFormat.duration(pace))/公里")
          }
          ForEach(Array(current.raw["steps"].list.enumerated()), id: \.offset) { _, step in
            HStack(alignment: .firstTextBaseline) {
              Text(stepTitle(step.string("kind"))).font(.subheadline.weight(.semibold))
              Spacer(minLength: 8)
              Text("\(ProductFormat.duration(step.number("durationS"))) · 自感用力 \(Int(step.number("effort") ?? 0))")
                .font(.subheadline).monospacedDigit().foregroundStyle(RAITheme.secondary)
            }
            if let repeats = step.number("repeats"), repeats > 0 {
              Text(
                "重复 \(Int(repeats)) 次 · 每次恢复 \(ProductFormat.duration(step.number("recoveryS")))"
              ).font(.caption).foregroundStyle(RAITheme.secondary)
            }
          }
          if current.raw["steps"].list.isEmpty {
            Text("按上面的距离或时长，以对应的自感用力完成即可。").font(.subheadline).foregroundStyle(
              RAITheme.secondary)
          }
          if !current.raw.string("notes").isEmpty { Text(current.raw.string("notes")) }
          let reasons = current.raw["reasonCodes"].list.compactMap(\.text).map(reasonText).filter { !$0.isEmpty }
          if !reasons.isEmpty {
            Divider()
            Text("为什么这样安排").font(.caption.weight(.semibold)).foregroundStyle(RAITheme.secondary)
            ForEach(reasons, id: \.self) {
              Label($0, systemImage: "circle.fill").labelStyle(BulletLabelStyle())
            }
          }
          ForEach(current.raw["conflicts"].list.compactMap(\.text).map(reasonText).filter { !$0.isEmpty }, id: \.self) {
            Text($0).font(.footnote).foregroundStyle(RAITheme.amber)
          }
        }.padding(22).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
        if !activeOverrides.isEmpty {
          VStack(alignment: .leading, spacing: 12) {
            CardHeading(title: "我的调整", icon: "slider.horizontal.3")
            ForEach(activeOverrides, id: \.self) { id in
              if let o = model.audit["overrides"].list.first(where: {
                $0.string("overrideID") == id
              }) {
                HStack {
                  Text(
                    fieldTitle(o.string("path").isEmpty ? o.string("operation") : o.string("path"))
                  ).font(.subheadline)
                  Spacer()
                  Button("撤销") {
                    Task {
                      _ = await model.override(
                        session: current.id, operation: "revoke", target: id,
                        observed: model.data.effective.string("effectiveVersionID"))
                      await model.loadAudit()
                    }
                  }.accessibilityIdentifier("session.revoke")
                }
              }
            }
          }.padding(22).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
        }
        if model.auditLoading { ProgressView().frame(maxWidth: .infinity) }
        if let error = model.auditError { RAINotice(text: error) }
        if model.auditNext > 0 || model.auditError != nil {
          Button(model.auditError == nil ? "加载更多调整" : "重试") {
            Task { await model.loadMoreAudit() }
          }.buttonStyle(RAISecondaryButtonStyle()).disabled(model.auditLoading).accessibilityIdentifier(
            "session.auditMore")
        }
        if let error = model.error { RAINotice(text: error) }
      }.padding(18)
    }.navigationTitle("训练详情").navigationBarTitleDisplayMode(.inline).toolbar(
      .visible, for: .navigationBar
    ).raiScreen()
      .sheet(
        item: Binding(get: { editor.map { EditorOperation(id: $0) } }, set: { editor = $0?.id })
      ) { op in
        SessionEditor(
          model: model, session: current, operation: op.id, effective: model.data.effective)
      }
      .navigationDestination(isPresented: $execution) { ExecutionView(model: model) }
      .task(id: model.data.effective.string("effectiveVersionID")) { await model.loadAudit() }
      .accessibilityIdentifier("screen.session")
  }
  var activeOverrides: [String] {
    model.audit["overrides"].list.filter {
      $0.string("sessionID") == current.id && $0.string("status") == "active"
        && $0.string("operation") != "revoke"
    }.map { $0.string("overrideID") }
  }
  @ViewBuilder private var actions: some View {
    action("编辑", "slider.horizontal.3", "edit")
    action("改期", "calendar", "move")
    action("跳过", "forward.end", "skip")
  }
  private func action(_ title: String, _ icon: String, _ operation: String) -> some View {
    Button {
      editor = operation
    } label: {
      Label(title, systemImage: icon).font(.subheadline)
        .frame(maxWidth: .infinity, minHeight: 44)
        .background(RAITheme.primary.opacity(0.08), in: Capsule()).contentShape(Capsule())
    }.buttonStyle(.plain).accessibilityIdentifier("session." + operation)
  }
}
struct EditorOperation: Identifiable { let id: String }
struct PendingCommandNotice: View {
  @Bindable var model: ProductModel
  var onConfirmed: () -> Void = {}
  var body: some View {
    if model.pending != nil {
      Section("上一次修改还没保存成功") {
        Text("没有收到服务器的确认。请先重新提交，成功后才能继续编辑；重新提交不会产生重复记录。")
          .font(.footnote).accessibilityIdentifier("pending.message")
        Button("重新提交") { Task { if await model.retryPending() { onConfirmed() } } }
          .disabled(model.loading).accessibilityIdentifier("pending.retry")
        if model.conflict {
          Button("查看最新计划") { Task { await model.reviewConflict() } }
            .disabled(model.loading).accessibilityIdentifier("pending.review")
        }
      }
    }
  }
}
func stepTitle(_ kind: String) -> String {
  ["warmup": "热身", "work": "主训练", "repeat": "间歇", "cooldown": "放松"][kind] ?? "训练步骤"
}
func fieldTitle(_ field: String) -> String {
  [
    "type": "训练类型", "durationS": "时长", "distanceM": "距离", "effort": "自感用力",
    "targetHR": "目标心率", "paceSPerKM": "目标配速", "notes": "备注", "move": "日期", "skip": "跳过",
    "add": "新增训练", "date": "日期", "edit": "编辑", "revoke": "撤销",
  ][field] ?? "其他"
}
/// The server stores seconds, metres and seconds-per-kilometre. Nobody thinks
/// in those, so the editor shows minutes, kilometres and m:ss and converts.
enum SessionFieldFormat {
  static func hint(_ field: String) -> String {
    ["durationS": "分钟", "distanceM": "公里", "effort": "0–10", "targetHR": "次/分", "paceSPerKM": "分:秒 每公里，如 5:30"][field] ?? ""
  }
  static func display(_ field: String, _ stored: Double) -> String {
    switch field {
    case "durationS": return trim(stored / 60)
    case "distanceM": return trim(stored / 1000)
    case "paceSPerKM":
      let total = Int(stored.rounded())
      return String(format: "%d:%02d", total / 60, total % 60)
    default: return trim(stored)
    }
  }
  /// Nil when the text is not a valid value for the field.
  static func stored(_ field: String, _ text: String) -> Double? {
    let text = text.trimmingCharacters(in: .whitespaces).replacingOccurrences(of: "：", with: ":")
    switch field {
    case "durationS": return number(text).map { ($0 * 60).rounded() }
    case "distanceM": return number(text).map { ($0 * 1000).rounded() }
    case "paceSPerKM":
      let parts = text.split(separator: ":", omittingEmptySubsequences: false)
      if parts.count == 2, let minutes = Double(parts[0]), let seconds = Double(parts[1]),
        minutes >= 0, seconds >= 0, seconds < 60 { return minutes * 60 + seconds }
      // A bare number is read as minutes per kilometre (5.5 = 5:30).
      return parts.count == 1 ? number(text).map { ($0 * 60).rounded() } : nil
    default: return number(text)
    }
  }
  private static func number(_ text: String) -> Double? {
    guard let value = Double(text), value.isFinite, value >= 0 else { return nil }
    return value
  }
  private static func trim(_ value: Double) -> String {
    let rounded = (value * 100).rounded() / 100
    return rounded == rounded.rounded() ? String(Int(rounded)) : String(rounded)
  }
}
struct SessionEditor: View {
  @Bindable var model: ProductModel
  let session: ProductSession?, operation: String, effective: JSON
  @Environment(\.dismiss) private var dismiss
  @State private var field = "distanceM"
  @State private var value = ""
  @State private var type = "EASY"
  @State private var date = Date()
  @State private var distance = ""
  @State private var duration = ""
  @State private var notes = ""
  @State private var effort = 3
  @State private var observed = ""
  @State private var reviewed = false
  @State private var validation: String?
  /// Seeds the form before the first frame. State assigned in onAppear did not
  /// reach the first render of a sheet (seen on the plan form's date pickers).
  init(model: ProductModel, session: ProductSession?, operation: String, effective: JSON) {
    _model = Bindable(model)
    self.session = session
    self.operation = operation
    self.effective = effective
    let first = session?.type == "REST" ? "notes" : "distanceM"
    _field = State(initialValue: first)
    _value = State(initialValue: Self.displayValue(session, first))
    _type = State(initialValue: session?.type ?? "EASY")
    _date = State(initialValue: session.flatMap { DateOnly.parse($0.date) } ?? Date())
    _observed = State(initialValue: effective.string("effectiveVersionID"))
  }
  static func displayValue(_ session: ProductSession?, _ field: String) -> String {
    guard let session else { return "" }
    return session.raw[field].text ?? session.raw.number(field).map {
      SessionFieldFormat.display(field, $0)
    } ?? ""
  }
  var fields: [String] {
    session?.type == "REST"
      ? ["notes"] : ["distanceM", "durationS", "type", "effort", "targetHR", "paceSPerKM", "notes"]
  }
  var body: some View {
    NavigationStack {
      Form {
        PendingCommandNotice(model: model, onConfirmed: { dismiss() })
        Group {
          Section {
            Text(operation == "add" ? "新增的训练会加入当前计划。" : "只改你选的这一项，其余内容仍随计划自动调整。").font(.footnote)
          }
          if operation == "edit" {
            Section {
              Picker("修改项", selection: $field) {
                ForEach(fields, id: \.self) { Text(fieldTitle($0)).tag($0) }
              }.accessibilityIdentifier("edit.field")
              if field == "type" {
                typePicker
              } else {
                LabeledContent(fieldTitle(field)) {
                  TextField(
                    SessionFieldFormat.hint(field), text: $value, axis: field == "notes" ? .vertical : .horizontal
                  ).multilineTextAlignment(field == "notes" ? .leading : .trailing)
                    .keyboardType(field == "notes" ? .default : field == "paceSPerKM" ? .numbersAndPunctuation : .decimalPad)
                    .accessibilityIdentifier("edit.value")
                }
                if !SessionFieldFormat.hint(field).isEmpty {
                  Text("单位：" + SessionFieldFormat.hint(field)).font(.caption).foregroundStyle(RAITheme.secondary)
                }
              }
              if ["targetHR", "paceSPerKM"].contains(field) { Text("留空表示不设这项目标").font(.caption).foregroundStyle(RAITheme.secondary) }
              if field == "durationS", let s = SessionFieldFormat.stored(field, value), s > 0, s < 1800 {
                Text("自动安排的跑步课不少于 30 分钟；你仍可以保存更短的时长。").font(.caption)
                  .foregroundStyle(RAITheme.amber).accessibilityIdentifier("edit.shortWarning")
              }
            }
          }
          if operation == "move" || operation == "add" {
            Section {
              DatePicker("训练日期", selection: $date, displayedComponents: .date)
                .accessibilityIdentifier("edit.date")
              if operation == "move" {
                HStack {
                  Button("前一天", systemImage: "chevron.left") {
                    date = Calendar.current.date(byAdding: .day, value: -1, to: date) ?? date
                  }.accessibilityIdentifier("edit.previousDay")
                  Spacer()
                  Button("后一天", systemImage: "chevron.right") {
                    date = Calendar.current.date(byAdding: .day, value: 1, to: date) ?? date
                  }.accessibilityIdentifier("edit.nextDay")
                }.buttonStyle(.borderless)
              }
            }
          }
          if operation == "skip" {
            Section { Text("这次训练会保留在计划里，并标记为已跳过。").foregroundStyle(RAITheme.amber) }
          }
          if operation == "add" {
            Section {
              typePicker
              LabeledContent("距离") {
                TextField("公里", text: $distance).multilineTextAlignment(.trailing).keyboardType(.decimalPad)
                  .accessibilityIdentifier("add.distance")
              }
              LabeledContent("时长") {
                TextField("分钟", text: $duration).multilineTextAlignment(.trailing).keyboardType(.decimalPad)
              }
              Stepper("自感用力 \(effort)", value: $effort, in: 0...10)
              TextField("备注", text: $notes)
            } header: {
              Text("训练内容")
            } footer: {
              Text("距离和时长至少填一项。")
            }
          }
        }.disabled(model.loading || model.pending != nil)
        if let error = validation ?? model.error {
          Section {
            Text(error).foregroundStyle(RAITheme.amber).accessibilityIdentifier("edit.error")
          }
        }
        if model.conflict {
          Section("训练计划已更新") {
            if !value.isEmpty { Text("你填写的内容：\(value)") }
            Button("查看最新计划") {
              Task {
                await model.reviewConflict()
                observed = model.data.effective.string("effectiveVersionID")
                reviewed = true
              }
            }.accessibilityIdentifier("edit.review")
          }
        }
        if reviewed {
          Section("最新计划") {
            if let current = model.data.sessions.first(where: { $0.id == session?.id }) {
              Text(current.title + " · " + prescription(current))
            }
            Text("你填写的内容还在，点「确认」即可提交到最新计划。").font(.footnote)
          }
        }
      }.navigationTitle(
        operation == "add"
          ? "添加训练" : operation == "move" ? "调整日期" : operation == "skip" ? "跳过训练" : "编辑训练"
      )
      .toolbar {
        ToolbarItem(placement: .cancellationAction) { Button("关闭") { dismiss() } }
        ToolbarItem(placement: .confirmationAction) {
          Button("确认") { Task { await save() } }
            .disabled(model.loading || model.conflict || model.pending != nil)
            .accessibilityIdentifier("edit.save")
        }
      }
    }.tint(RAITheme.orange).preferredColorScheme(.light).onAppear {
      restorePendingDraft()
    }.onChange(of: field) { _, _ in if model.pending == nil { fillValue() } }
      .onChange(of: model.pending) { old, current in
        if old != nil && current == nil && !model.conflict {
          observed = model.data.effective.string("effectiveVersionID")
        }
      }.accessibilityIdentifier("screen.editor")
  }
  var typePicker: some View {
    Picker("训练类型", selection: $type) {
      ForEach(["EASY", "LONG", "TEMPO", "INTERVAL", "RECOVERY"], id: \.self) {
        Text(ProductSession(.object(["type": .string($0)])).title).tag($0)
      }
    }
  }
  func fillValue() {
    guard let session else { return }
    type = session.type
    value = Self.displayValue(session, field)
  }
  func save() async {
    validation = nil
    guard model.pending == nil else { return }
    var v: JSON = .null
    if operation == "edit" {
      if field == "type" {
        v = .string(type)
      } else if field == "notes" {
        v = .string(value)
      } else if value.isEmpty && ["targetHR", "paceSPerKM"].contains(field) {
        v = .null
      } else if let n = SessionFieldFormat.stored(field, value) {
        v = .number(n)
      } else {
        validation = field == "paceSPerKM" ? "请按 分:秒 填写配速，例如 5:30" : "请输入有效数值"
        return
      }
    }
    if operation == "move" { v = .string(DateOnly.string(date)) }
    var manual: JSON = .null
    let id = session?.id ?? Wire.id()
    if operation == "add" {
      guard let d = Double(distance.isEmpty ? "0" : distance),
        let t = Double(duration.isEmpty ? "0" : duration), d.isFinite, t.isFinite, d > 0 || t > 0
      else {
        validation = "请填写距离或时长"
        return
      }
      manual = .object([
        "sessionID": .string(id), "date": .string(DateOnly.string(date)), "type": .string(type),
        "durationS": .number((t * 60).rounded()), "distanceM": .number((d * 1000).rounded()), "effort": .int(Int64(effort)),
        "notes": .string(notes),
      ])
    }
    if await model.override(
      session: id, operation: operation, path: operation == "edit" ? field : "", value: v,
      manual: manual, observed: observed)
    {
      dismiss()
    }
  }
  func restorePendingDraft() {
    guard let pending = model.pending, pending.path.hasSuffix("/overrides"),
      pending.body.string("operation") == operation,
      operation == "add" || pending.body.string("sessionID") == session?.id
    else { return }
    let body = pending.body
    if operation == "edit" {
      field = body.string("path")
      value = body["value"].text ?? body["value"].double.map { SessionFieldFormat.display(field, $0) } ?? ""
      if field == "type" { type = body.string("value") }
    }
    if operation == "move", let d = DateOnly.parse(body.string("value")) { date = d }
    if operation == "add" {
      let s = body["session"]
      type = s.string("type")
      notes = s.string("notes")
      distance = s.number("distanceM").flatMap { $0 > 0 ? SessionFieldFormat.display("distanceM", $0) : nil } ?? ""
      duration = s.number("durationS").flatMap { $0 > 0 ? SessionFieldFormat.display("durationS", $0) : nil } ?? ""
      effort = Int(s.number("effort") ?? 3)
      if let d = DateOnly.parse(s.string("date")) { date = d }
    }
  }
}
enum DateOnly {
  static func formatter() -> DateFormatter {
    let f = DateFormatter()
    f.calendar = Calendar(identifier: .gregorian)
    f.locale = Locale(identifier: "en_US_POSIX")
    f.timeZone = .current
    f.dateFormat = "yyyy-MM-dd"
    return f
  }
  static func string(_ date: Date) -> String { formatter().string(from: date) }
  /// "2026-11-11" → "11月11日 周三"; an unparseable value is shown as it is.
  static func display(_ value: String, pattern: String = "M月d日 EEE") -> String {
    guard let date = parse(value) else { return value }
    let f = DateFormatter()
    f.calendar = Calendar(identifier: .gregorian)
    f.locale = Locale(identifier: "zh_CN")
    f.timeZone = .current
    f.dateFormat = pattern
    return f.string(from: date)
  }
  static func parse(_ value: String) -> Date? { formatter().date(from: value) }
}
/// Creates an adaptive plan, or edits the goal and training days of the
/// current one. Only training days and the long-run day are required; a race
/// goal and the weekly running time are optional.
struct CreatePlanView: View {
  @Bindable var model: ProductModel
  var editing = false
  @Environment(\.dismiss) private var dismiss
  @State private var hasGoal = false
  @State private var distance = 21097.5
  @State private var customKm = ""
  @State private var raceDate = Calendar.current.date(byAdding: .day, value: 84, to: Date()) ?? Date()
  @State private var target = ""
  @State private var days: Set<Int> = [2, 4, 6, 7]
  @State private var longDay = 7
  @State private var baseline = ""
  @State private var error: String?
  static let distances: [(String, Double)] = [("5 公里", 5000), ("10 公里", 10000), ("半马", 21097.5), ("全马", 42195), ("自定义", 0)]
  /// Seeds the form before the first frame: the current plan when editing.
  init(model: ProductModel, editing: Bool = false) {
    _model = Bindable(model)
    self.editing = editing
    guard editing else { return }
    let c = model.data.plan["config"]
    _days = State(initialValue: Set(c["availableDays"].list.compactMap { $0.integer.map(Int.init) }))
    _longDay = State(initialValue: Int(c.number("longRunDay") ?? 7))
    let g = c["goal"]
    if let d = DateOnly.parse(g.string("date")), let m = g.number("distanceM") {
      _hasGoal = State(initialValue: true)
      _raceDate = State(initialValue: d)
      if Self.distances.contains(where: { $0.1 == m }) {
        _distance = State(initialValue: m)
      } else {
        _distance = State(initialValue: 0)
        _customKm = State(initialValue: SessionFieldFormat.display("distanceM", m))
      }
      _target = State(initialValue: g.number("targetTimeS").map(AdaptivePlanText.clock) ?? "")
    }
    _baseline = State(initialValue: c.number("baselineWeeklyS").map { String(Int($0 / 60)) } ?? "")
  }
  static func monday(of date: Date) -> Date {
    var cal = Calendar(identifier: .gregorian)
    cal.timeZone = .current
    let day = cal.startOfDay(for: date)
    return cal.date(byAdding: .day, value: -((cal.component(.weekday, from: day) + 5) % 7), to: day) ?? day
  }
  var weekday: [String] { ["一", "二", "三", "四", "五", "六", "日"] }
  var body: some View {
    NavigationStack {
      Form {
        PendingCommandNotice(model: model, onConfirmed: { dismiss() })
        Group {
          Section {
            Toggle("我有目标比赛", isOn: $hasGoal).accessibilityIdentifier("create.hasGoal")
            if hasGoal {
              Picker("比赛距离", selection: $distance) {
                ForEach(Self.distances, id: \.1) { Text($0.0).tag($0.1) }
              }.pickerStyle(.segmented).accessibilityIdentifier("create.distance")
              if distance == 0 {
                LabeledContent("距离") {
                  TextField("公里", text: $customKm).multilineTextAlignment(.trailing).keyboardType(.decimalPad)
                    .accessibilityIdentifier("create.target")
                }
              }
              DatePicker("比赛日期", selection: $raceDate, in: Date()..., displayedComponents: .date)
                .accessibilityIdentifier("create.raceDate")
              LabeledContent("目标成绩") {
                TextField("可选，如 1:45:00", text: $target).multilineTextAlignment(.trailing)
                  .keyboardType(.numbersAndPunctuation).accessibilityIdentifier("create.targetTime")
              }
            }
          } header: {
            Text("目标")
          } footer: {
            Text(
              hasGoal
                ? "按比赛日期倒排基础、提升、巅峰、减量各阶段；目标成绩会与跑力预测比较，配速课不会超出当前能力。"
                : "没有比赛时，根据你最近的训练完成情况、负荷和恢复每周自动安排。")
          }
          Section {
            ForEach(1...7, id: \.self) { day in
              Toggle(
                "周" + weekday[day - 1],
                isOn: Binding(
                  get: { days.contains(day) },
                  set: { if $0 { days.insert(day) } else { days.remove(day) } }))
            }
            Picker("长距离日", selection: $longDay) {
              ForEach(days.sorted(), id: \.self) { Text("周" + weekday[$0 - 1]).tag($0) }
            }
          } header: {
            Text("每周可训练日（至少 2 天）")
          } footer: {
            Text("每次跑步课程不少于 30 分钟。训练量少时会减少次数，而不是缩短单次时长。")
          }
          Section {
            LabeledContent("每周跑步时长") {
              TextField("可选，分钟", text: $baseline).multilineTextAlignment(.trailing).keyboardType(.numberPad)
                .accessibilityIdentifier("create.baseline")
            }
          } header: {
            Text("当前基础")
          } footer: {
            Text("不填时按已同步的跑步记录估算；没有记录时从每周 2 次、每次 30 分钟开始。")
          }
        }.disabled(model.loading || model.pending != nil)
        if let e = error ?? model.error { RAINotice(text: e) }
      }.navigationTitle(editing ? "修改训练设置" : "自适应训练").navigationBarTitleDisplayMode(.inline).toolbar {
        ToolbarItem(placement: .cancellationAction) { Button("关闭") { dismiss() } }
        ToolbarItem(placement: .confirmationAction) {
          Button(editing ? "保存" : "创建") { Task { await save() } }.disabled(model.loading || model.pending != nil)
            .accessibilityIdentifier("create.save")
        }
      }
    }.preferredColorScheme(.light)
      .onChange(of: days) { _, value in
        if !value.contains(longDay), let last = value.max() { longDay = last }
      }
      .onAppear(perform: restorePending)
  }
  func restorePending() {
    guard let pending = model.pending,
      pending.path == "/v2/training/plans" || pending.path.hasSuffix("/changes")
    else { return }
    let c = pending.body["config"]
    guard c.string("version") == AdaptivePlan.configVersion else { return }
    days = Set(c["availableDays"].list.compactMap { $0.integer.map(Int.init) })
    longDay = Int(c.number("longRunDay") ?? 7)
    hasGoal = c["goal"] != .null
    if let d = DateOnly.parse(c["goal"].string("date")) { raceDate = d }
    if let m = c["goal"].number("distanceM") {
      if Self.distances.contains(where: { $0.1 == m }) { distance = m } else {
        distance = 0
        customKm = SessionFieldFormat.display("distanceM", m)
      }
    }
    target = c["goal"].number("targetTimeS").map(AdaptivePlanText.clock) ?? ""
    baseline = c.number("baselineWeeklyS").map { String(Int($0 / 60)) } ?? ""
  }
  func save() async {
    guard model.pending == nil else { return }
    var goal: AdaptivePlanConfig.Goal?
    if hasGoal {
      let meters = distance > 0 ? distance : (Double(customKm).map { $0 * 1000 } ?? 0)
      var time: Double?
      if !target.trimmingCharacters(in: .whitespaces).isEmpty {
        guard let t = AdaptivePlanText.seconds(target) else {
          error = "目标成绩请按 时:分:秒 或 分:秒 填写，例如 1:45:00。"
          return
        }
        time = t
      }
      goal = .init(distanceM: meters, date: DateOnly.string(raceDate), targetTimeS: time)
    }
    let minutes: Double? = baseline.isEmpty ? nil : Double(baseline)
    if !baseline.isEmpty && minutes == nil {
      error = "每周跑步时长请填写分钟数。"
      return
    }
    if let problem = AdaptivePlanConfig.problem(
      days: days, longDay: longDay, goal: goal, today: DateOnly.string(Date()), baselineMinutes: minutes)
    {
      error = problem
      return
    }
    error = nil
    let p = model.data.plan
    let monday = editing ? p["config"].string("startDate") : DateOnly.string(Self.monday(of: Date()))
    let config = AdaptivePlanConfig.make(monday: monday, days: Array(days), longDay: longDay, goal: goal, baselineMinutes: minutes)
    let command =
      editing
      ? model.command(
        path: "/v2/training/plans/" + p.string("planID") + "/changes",
        fields: ["operation": .string("update_config"), "expectedConfigRevision": p["configRevision"], "config": config])
      : model.command(path: "/v2/training/plans", fields: ["planID": .string(Wire.id()), "config": config])
    if await model.submit(command) {
      await model.refresh()
      dismiss()
    }
  }
}
