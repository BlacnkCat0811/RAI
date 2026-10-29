import ProductCore
import SwiftUI
import SyncCore

struct TrainingView: View {
  @Bindable var model: ProductModel
  @State private var date = ""
  @State private var create = false
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
          EmptyCard(title: "创建你的训练计划", detail: "设置目标、训练日和当前跑量，获取服务端生成的训练建议。")
          OrangeButton(title: "设置训练目标", icon: "plus") { create = true }.accessibilityIdentifier(
            "training.create")
        } else {
          VStack(alignment: .leading, spacing: 10) {
            CardHeading(title: "当前训练计划", icon: "flag.checkered")
            MetricText(
              value: ProductFormat.distance(model.data.plan["config"]["goal"].number("distanceM")),
              unit: "km 目标")
            Text("目标日 \(model.data.plan["config"]["goal"].string("date"))").font(.subheadline)
              .foregroundStyle(RAITheme.secondary)
            if model.data.plan.string("generationState") == "succeeded" {
              Text("计划已更新").font(.caption).foregroundStyle(RAITheme.green)
            } else {
              Label("后台正在更新，保留当前训练安排", systemImage: "arrow.triangle.2.circlepath").font(.caption)
                .foregroundStyle(RAITheme.amber)
            }
            if let code = model.data.generationFailure {
              Text("生成未完成：\(code)").font(.caption).foregroundStyle(RAITheme.amber)
            }
            Text(
              "\(Int(model.data.plan["config"].number("weeks") ?? 0)) 周 · 有效计划 v\(Int(model.data.effective.number("version") ?? 0))"
            ).font(.caption).foregroundStyle(RAITheme.secondary)
          }.frame(maxWidth: .infinity, alignment: .leading).padding(22).raiGlass()
          ScrollView(.horizontal) {
            HStack(spacing: 8) {
              ForEach(dates, id: \.self) { day in
                Button {
                  date = day
                  UISelectionFeedbackGenerator().selectionChanged()
                } label: {
                  VStack(spacing: 6) {
                    Text(String(day.suffix(5)))
                    Circle().fill(day == selected ? RAITheme.orange : .white.opacity(0.15)).frame(
                      width: 5, height: 5)
                  }.font(.caption).padding(12).background(
                    day == selected ? RAITheme.orange.opacity(0.18) : .white.opacity(0.04),
                    in: RoundedRectangle(cornerRadius: 18))
                }.buttonStyle(RAIPressStyle()).accessibilityIdentifier("training.date." + day)
              }
            }
          }.scrollIndicators(.hidden)
          ForEach(model.data.sessions.filter { $0.date == selected }) { s in
            Button {
              selectedSession = s
            } label: {
              SessionCard(session: s)
            }.buttonStyle(RAIPressStyle()).matchedTransitionSource(id: s.id, in: transition)
              .accessibilityIdentifier("session." + String(s.raw.number("slot") ?? -1))
          }
          HStack {
            Button("添加训练", systemImage: "plus") { add = true }.accessibilityIdentifier(
              "training.add")
            Spacer()
            Button("重算计划", systemImage: "arrow.clockwise") { Task { await model.recalculate() } }
              .disabled(model.loading).accessibilityIdentifier("training.recalculate")
          }.font(.subheadline).padding(.vertical, 8)
          if !model.execution.string("executionID").isEmpty {
            NavigationLink("继续执行 / 查看反馈") { ExecutionView(model: model) }.accessibilityIdentifier(
              "training.execution")
          }
          ForEach(model.data.effective["recommendations"].list.compactMap(\.text), id: \.self) {
            reason in Text(reasonText(reason)).font(.footnote).foregroundStyle(RAITheme.secondary)
          }
        }
        if let error = model.error { Text(error).font(.footnote).foregroundStyle(RAITheme.amber) }
      }.padding(18)
    }.navigationTitle("训练").raiScreen().refreshable { await model.refresh() }
      .sheet(isPresented: $create) { CreatePlanView(model: model) }
      .sheet(isPresented: $add) {
        SessionEditor(model: model, session: nil, operation: "add", effective: model.data.effective)
      }.navigationDestination(item: $selectedSession) { session in
        SessionDetail(model: model, session: session, effective: model.data.effective).modifier(
          RAIDetailTransition(id: session.id, namespace: transition))
      }.accessibilityIdentifier("screen.training")
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
        Text(session.date).font(.caption).foregroundStyle(RAITheme.secondary)
        Spacer()
        if session.modified {
          Text("已调整").font(.caption2.weight(.semibold)).foregroundStyle(RAITheme.amber)
        }
      }
      Text(session.title).font(.title2.bold())
      Text(prescription(session)).font(.subheadline).monospacedDigit()
      HStack {
        Text("RPE \(Int(session.raw.number("effort") ?? 0))")
        Spacer()
        Text(statusText(session.raw.string("status")))
      }.font(.caption).foregroundStyle(RAITheme.secondary)
    }.frame(maxWidth: .infinity, alignment: .leading).padding(22).raiGlass().foregroundStyle(.white)
  }
}
func statusText(_ status: String) -> String {
  ["scheduled": "已安排", "started": "进行中", "completed": "已完成", "skipped": "已跳过", "cancelled": "已取消"][
    status] ?? status
}
func reasonText(_ reason: String) -> String {
  if reason.hasSuffix("_override_incompatible_with_session_structure/v1") {
    return fieldTitle(reason.components(separatedBy: "_override")[0])
      + "的历史调整与新课程结构不兼容；当前保留自动处方，请修改或撤销调整。"
  }
  return [
    "aerobic_base": "建立有氧耐力", "long_run_progression": "循序增加长距离训练",
    "minimum_recovery": "安排休息，控制训练负荷", "goal_specific_tempo": "目标相关节奏训练",
    "goal_specific_interval": "目标相关间歇训练", "load_only_advisory": "训练状态仅基于负荷估算",
    "user_override_review": "包含用户调整，请结合实际状态确认", "readiness_adjustment": "近期负荷偏高，自动建议已降低",
    "goal_risk": "当前基础与目标存在差距，请确认比赛安排", "target_time_not_optimized": "目标时间尚未用于配速优化",
    "feedback_adjustment": "根据已完成训练反馈保守调整", "duration_override_unstructured": "时长已调整，当前采用无分段处方",
    "cold_start_conservative": "训练历史不足，采用你明确填写的基础保守安排",
    "readiness_unavailable": "准备度数据不足，未用于自动训练调整",
    "weekly_load_constraint": "按周跑量预算安排训练", "weekly_progression": "按计划渐进增加周跑量",
    "recovery_week": "本周降低训练量", "recovery_after_hard_session": "关键训练后安排恢复",
    "base_session_removed_kept": "自动课程已移除，保留你的调整",
    "override_rebase_conflict/v1": "历史调整需要确认；原调整仍保留在历史中。",
  ][reason] ?? "训练建议详情暂不可用"
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
          Text(current.date).font(.subheadline)
          Text(current.title).font(.system(.largeTitle, design: .rounded, weight: .bold))
          Text(prescription(current)).font(.title2).monospacedDigit()
          Text("RPE \(Int(current.raw.number("effort") ?? 0))").font(.subheadline)
          if current.modified {
            Text("已调整 · 仅保护你修改的字段").font(.caption).foregroundStyle(RAITheme.amber)
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
          CardHeading(title: "训练处方", icon: "list.bullet")
          if let hr = current.raw.number("targetHR") {
            Text("目标心率 \(ProductFormat.decimal(hr)) bpm")
          }
          if let pace = current.raw.number("paceSPerKM") {
            Text("目标配速 \(ProductFormat.duration(pace))/km")
          }
          ForEach(Array(current.raw["steps"].list.enumerated()), id: \.offset) { _, step in
            Text(
              "\(stepTitle(step.string("kind"))) · \(ProductFormat.duration(step.number("durationS"))) · RPE \(Int(step.number("effort") ?? 0))"
            )
            if let repeats = step.number("repeats"), repeats > 0 {
              Text(
                "重复 \(Int(repeats)) 次 · 每次恢复 \(ProductFormat.duration(step.number("recoveryS")))"
              ).font(.caption).foregroundStyle(RAITheme.secondary)
            }
          }
          if current.raw["steps"].list.isEmpty {
            Text("按距离 / 时长与 RPE 执行；当前处方没有独立热身、主训练和放松分段。").font(.subheadline).foregroundStyle(
              RAITheme.secondary)
          }
          if !current.raw.string("notes").isEmpty { Text(current.raw.string("notes")) }
          ForEach(current.raw["reasonCodes"].list.compactMap(\.text), id: \.self) {
            Text(reasonText($0)).font(.footnote).foregroundStyle(RAITheme.secondary)
          }
          ForEach(current.raw["conflicts"].list.compactMap(\.text), id: \.self) {
            Text(reasonText($0)).font(.footnote).foregroundStyle(RAITheme.amber)
          }
        }.padding(22).raiGlass()
        if !activeOverrides.isEmpty {
          VStack(alignment: .leading, spacing: 12) {
            CardHeading(title: "用户调整", icon: "slider.horizontal.3")
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
          }.padding(22).raiGlass()
        }
        if model.auditLoading { ProgressView("正在加载调整历史") }
        if let error = model.auditError {
          Text(error).font(.footnote).foregroundStyle(RAITheme.amber)
        }
        if model.auditNext > 0 || model.auditError != nil {
          Button(model.auditError == nil ? "加载更多调整" : "重试调整历史") {
            Task { await model.loadMoreAudit() }
          }.frame(minHeight: 44).disabled(model.auditLoading).accessibilityIdentifier(
            "session.auditMore")
        }
        if !model.auditLoading && model.auditError == nil && model.audit["overrides"].list.isEmpty {
          Text("暂无用户调整").font(.footnote).foregroundStyle(RAITheme.secondary)
        }
        if let error = model.error { Text(error).foregroundStyle(RAITheme.amber).font(.footnote) }
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
        .background(.white.opacity(0.08), in: Capsule()).contentShape(Capsule())
    }.buttonStyle(.plain).accessibilityIdentifier("session." + operation)
  }
}
struct EditorOperation: Identifiable { let id: String }
struct PendingCommandNotice: View {
  @Bindable var model: ProductModel
  var onConfirmed: () -> Void = {}
  var body: some View {
    if model.pending != nil {
      Section("待确认的提交") {
        Text("上一次修改结果尚未确认。请先重试原操作或确认服务器状态。当前草稿已保留，确认前暂不能编辑或保存。")
          .font(.footnote).accessibilityIdentifier("pending.message")
        Button("重试原操作") { Task { if await model.retryPending() { onConfirmed() } } }
          .disabled(model.loading).accessibilityIdentifier("pending.retry")
        if model.conflict {
          Button("查看最新状态并保留草稿") { Task { await model.reviewConflict() } }
            .disabled(model.loading).accessibilityIdentifier("pending.review")
        }
      }
    }
  }
}
func stepTitle(_ kind: String) -> String {
  ["warmup": "热身", "work": "主训练", "repeat": "间歇", "cooldown": "放松"][kind] ?? kind
}
func fieldTitle(_ field: String) -> String {
  [
    "type": "训练类型", "durationS": "时长（秒）", "distanceM": "距离（米）", "effort": "RPE",
    "targetHR": "目标心率（bpm）", "paceSPerKM": "配速（秒/公里）", "notes": "备注", "move": "日期", "skip": "跳过",
    "add": "新增训练", "date": "日期",
  ][field] ?? field
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
            Text(operation == "add" ? "新课程将加入同一份有效计划。" : "只修改选定字段；其余字段继续随自动计划更新。").font(.footnote)
          }
          if operation == "edit" {
            Section {
              Picker("字段", selection: $field) {
                ForEach(fields, id: \.self) { Text(fieldTitle($0)).tag($0) }
              }.accessibilityIdentifier("edit.field")
              if field == "type" {
                typePicker
              } else {
                TextField(
                  fieldTitle(field), text: $value, axis: field == "notes" ? .vertical : .horizontal
                ).keyboardType(field == "notes" ? .default : .decimalPad).accessibilityIdentifier(
                  "edit.value")
              }
              if ["targetHR", "paceSPerKM"].contains(field) { Text("留空可清除该可选目标").font(.caption) }
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
            Section { Text("此次课程将保留，并标记为已跳过。").foregroundStyle(RAITheme.amber) }
          }
          if operation == "add" {
            Section("训练处方") {
              typePicker
              TextField("距离（米）", text: $distance).keyboardType(.decimalPad).accessibilityIdentifier(
                "add.distance")
              TextField("时长（秒）", text: $duration).keyboardType(.decimalPad)
              Stepper("RPE \(effort)", value: $effort, in: 0...10)
              TextField("备注", text: $notes)
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
            Text("草稿：\(value)")
            Button("查看最新计划并保留草稿") {
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
            Text("草稿仍保留；确认后提交到最新版本。").font(.footnote)
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
    }.tint(RAITheme.orange).preferredColorScheme(.dark).onAppear {
      observed = effective.string("effectiveVersionID")
      if session?.type == "REST" { field = "notes" }
      fillValue()
      if let d = session?.date { date = DateOnly.parse(d) ?? Date() }
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
    value =
      session.raw[field].text ?? session.raw.number(field).map {
        ($0.rounded() == $0 ? String(Int($0)) : String($0))
      } ?? ""
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
      } else if let n = Double(value), n.isFinite {
        v = .number(n)
      } else {
        validation = "请输入有效数值"
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
        "durationS": .number(t), "distanceM": .number(d), "effort": .int(Int64(effort)),
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
      value = body["value"].text ?? body["value"].double.map { String($0) } ?? ""
      if field == "type" { type = body.string("value") }
    }
    if operation == "move", let d = DateOnly.parse(body.string("value")) { date = d }
    if operation == "add" {
      let s = body["session"]
      type = s.string("type")
      notes = s.string("notes")
      distance = s.number("distanceM").map { String($0) } ?? ""
      duration = s.number("durationS").map { String($0) } ?? ""
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
  static func parse(_ value: String) -> Date? { formatter().date(from: value) }
}
struct CreatePlanView: View {
  @Bindable var model: ProductModel
  @Environment(\.dismiss) private var dismiss
  @State private var start = Date()
  @State private var goal = Date()
  @State private var weeks = 8
  @State private var days: Set<Int> = [1, 3, 5, 7]
  @State private var longDay = 7
  @State private var target = ""
  @State private var baseline = ""
  @State private var longest = ""
  @State private var error: String?
  var body: some View {
    NavigationStack {
      Form {
        PendingCommandNotice(model: model, onConfirmed: { dismiss() })
        Group {
          Section("训练目标") {
            TextField("目标距离（km）", text: $target).keyboardType(.decimalPad).accessibilityIdentifier(
              "create.target")
            DatePicker("开始日（周一）", selection: $start, displayedComponents: .date)
            DatePicker("目标日", selection: $goal, displayedComponents: .date)
            Stepper("\(weeks) 周", value: $weeks, in: 4...16)
          }
          Section("当前基础 · 请明确填写") {
            TextField("当前每周跑量（km）", text: $baseline).keyboardType(.decimalPad)
            TextField("最近最长距离（km）", text: $longest).keyboardType(.decimalPad)
          }
          Section("每周可训练日（2–6 天）") {
            ForEach(1...7, id: \.self) { day in
              Toggle(
                "周" + ["一", "二", "三", "四", "五", "六", "日"][day - 1],
                isOn: Binding(
                  get: { days.contains(day) },
                  set: { if $0 { days.insert(day) } else { days.remove(day) } }))
            }
            Picker("长跑日", selection: $longDay) {
              ForEach(days.sorted(), id: \.self) {
                Text("周" + ["一", "二", "三", "四", "五", "六", "日"][$0 - 1]).tag($0)
              }
            }
          }
        }.disabled(model.loading || model.pending != nil)
        if let e = error ?? model.error { Text(e).foregroundStyle(RAITheme.amber) }
      }.navigationTitle("训练目标").toolbar {
        ToolbarItem(placement: .cancellationAction) { Button("关闭") { dismiss() } }
        ToolbarItem(placement: .confirmationAction) {
          Button("创建") { Task { await create() } }.disabled(model.loading || model.pending != nil)
            .accessibilityIdentifier("create.save")
        }
      }
    }.preferredColorScheme(.dark).onAppear {
      var cal = Calendar(identifier: .gregorian)
      cal.firstWeekday = 2
      let weekday = cal.component(.weekday, from: Date())
      start = cal.date(byAdding: .day, value: -((weekday + 5) % 7), to: Date()) ?? Date()
      goal = cal.date(byAdding: .day, value: 55, to: start) ?? Date()
      if let pending = model.pending, pending.path == "/v2/training/plans" {
        let c = pending.body["config"]
        start = DateOnly.parse(c.string("startDate")) ?? start
        goal = DateOnly.parse(c["goal"].string("date")) ?? goal
        weeks = Int(c.number("weeks") ?? 8)
        days = Set(c["availableDays"].list.compactMap { $0.integer.map(Int.init) })
        longDay = Int(c.number("longRunDay") ?? 7)
        target = c["goal"].number("distanceM").map { String($0 / 1000) } ?? ""
        baseline = c.number("baselineWeeklyM").map { String($0 / 1000) } ?? ""
        longest = c.number("previousLongM").map { String($0 / 1000) } ?? ""
      }
    }
  }
  func create() async {
    guard model.pending == nil else { return }
    guard let d = Double(target), let b = Double(baseline), let l = Double(longest), d.isFinite,
      b.isFinite, l.isFinite
    else {
      error = "请填写目标距离、当前跑量和最长跑。"
      return
    }
    let config: JSON = .object([
      "startDate": .string(DateOnly.string(start)), "weeks": .int(Int64(weeks)),
      "goal": .object([
        "type": .string("running"), "date": .string(DateOnly.string(goal)),
        "distanceM": .number(d * 1000),
      ]), "availableDays": .array(days.sorted().map { .int(Int64($0)) }),
      "longRunDay": .int(Int64(longDay)), "baselineWeeklyM": .number(b * 1000),
      "previousLongM": .number(l * 1000),
    ])
    if await model.submit(
      model.command(
        path: "/v2/training/plans", fields: ["planID": .string(Wire.id()), "config": config]))
    {
      await model.refresh()
      dismiss()
    }
  }
}
