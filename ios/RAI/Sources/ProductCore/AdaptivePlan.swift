import Foundation
import SyncCore

/// The adaptive (training_plan/v4) view of a plan. The server owns every
/// decision; this type only reads the published effective version.
public struct AdaptivePlan: Sendable {
  public static let configVersion = "training_config/v2"
  public let effective: JSON
  public let config: JSON
  public init(plan: JSON) {
    effective = plan["effective"]
    config = plan["config"]
  }
  /// True for a plan created with the adaptive config.
  public var isAdaptive: Bool { config.string("version") == Self.configVersion }
  public var mode: String { effective.string("mode") }
  public var isRace: Bool { mode == "race" }
  public var weeks: [AdaptiveWeek] { effective["weeks"].list.map(AdaptiveWeek.init) }
  public var current: AdaptiveWeek? { weeks.first { $0.detail == "current" } }
  public var next: AdaptiveWeek? { weeks.first { $0.detail == "next" } }
  /// Weeks ahead of next week (race plans only): phase and projected volume.
  public var outline: [AdaptiveWeek] { weeks.filter { $0.detail == "outline" } }
  public var goal: AdaptiveGoal? {
    effective["goalStatus"] == .null ? nil : AdaptiveGoal(raw: effective["goalStatus"])
  }
  /// The goal set in the config, even before a version reflecting it is out.
  public var configuredGoal: JSON { config["goal"] }
  public var hasConfiguredGoal: Bool { !configuredGoal.string("date").isEmpty }
}

public struct AdaptiveWeek: Identifiable, Sendable {
  public let raw: JSON
  public init(_ raw: JSON) { self.raw = raw }
  public var id: Int { Int(raw.number("index") ?? 0) }
  public var start: String { raw.string("startDate") }
  public var detail: String { raw.string("detail") }
  public var phase: String { raw.string("phase") }
  public var state: String { raw.string("state") }
  public var budgetMinutes: Int { Int(((raw.number("budgetS") ?? 0) / 60).rounded()) }
  public var plannedMinutes: Int { Int(((raw.number("plannedS") ?? 0) / 60).rounded()) }
  public var actualMinutes: Int? { raw.number("actualS").map { Int(($0 / 60).rounded()) } }
  public var hardFocus: [String] { raw["hardFocus"].list.compactMap(\.text) }
  public var reasons: [String] { raw["reasonCodes"].list.compactMap(\.text) }
  /// Share of the planned minutes already run, 0...1.
  public var progress: Double {
    guard let actual = actualMinutes, plannedMinutes > 0 else { return 0 }
    return min(1, Double(actual) / Double(plannedMinutes))
  }
}

public struct AdaptiveGoal: Sendable {
  public let raw: JSON
  public var date: String { raw.string("date") }
  public var distanceM: Double { raw.number("distanceM") ?? 0 }
  public var category: String { raw.string("category") }
  public var phase: String { raw.string("phase") }
  public var daysToRace: Int { Int(raw.number("daysToRace") ?? 0) }
  public var assessment: String { raw.string("assessment") }
  public var targetTimeS: Double? { raw.number("targetTimeS") }
  public var predictedTimeS: Double? { raw.number("predictedTimeS") }
  public var racePace: Double? { raw.number("racePaceSPerKM") }
  public var completed: Bool { raw["completed"].flag }
  public var reasons: [String] { raw["reasonCodes"].list.compactMap(\.text) }
}

extension ProductSession {
  public var focus: String { raw.string("focus") }
  public var purpose: String { raw.string("purpose") }
  public var outcome: String { raw.string("outcome") }
  public var changedFrom: JSON { raw["changedFrom"] }
  /// True when the server adapted this day (missed session, readiness, extra
  /// volume, session recovery) rather than the user editing it.
  public var adapted: Bool { changedFrom != .null }
}

public enum AdaptivePlanText {
  public static func phase(_ p: String) -> String {
    [
      "fitness": "自适应训练", "pre": "备赛前基础", "base": "基础期", "build": "提升期", "peak": "巅峰期",
      "taper": "减量期", "race": "比赛周", "post": "赛后恢复",
    ][p] ?? "训练"
  }
  public static func state(_ s: String) -> String {
    [
      "start": "起始周", "progress": "进阶周", "hold": "保持周", "recover": "恢复周", "regress": "回调周",
      "return": "回归周", "taper": "减量周", "race": "比赛周", "post": "赛后恢复周", "untracked": "计划前",
      "projected": "预计",
    ][s] ?? "训练周"
  }
  public static func stateDetail(_ s: String) -> String {
    [
      "start": "按近期训练量起步", "progress": "上周完成得好，本周小幅加量", "hold": "本周维持上周训练量",
      "recover": "降低训练量，让身体吸收训练", "regress": "上周完成较少，回到近期实际水平",
      "return": "中断后逐步恢复，只安排轻松跑", "taper": "降低跑量、保留强度，迎接比赛",
      "race": "比赛周，少量唤醒训练", "post": "赛后以轻松跑恢复",
    ][s] ?? ""
  }
  public static func purpose(_ p: String) -> String {
    [
      "promote_recovery": "促进恢复", "aerobic_base": "巩固有氧基础", "neuromuscular_speed": "提升步频与速度感",
      "endurance": "提升耐力", "race_specific_endurance": "比赛专项耐力", "aerobic_capacity": "提升有氧能力上限",
      "lactate_threshold": "提升乳酸阈", "vo2max": "提升最大摄氧量", "race_specific_speed": "熟悉比赛配速",
      "race_sharpening": "赛前唤醒", "goal_event": "目标比赛",
    ][p] ?? ""
  }
  public static func focusTitle(_ focus: String) -> String? {
    [
      "recovery": "恢复跑", "aerobic_base": "轻松跑", "strides": "轻松跑 + 加速跑", "long_aerobic": "长距离跑",
      "long_race_pace": "长距离（含比赛配速）", "tempo": "节奏跑", "threshold": "阈值跑", "vo2max": "最大摄氧间歇",
      "race_pace": "比赛配速训练", "race_wakeup": "赛前唤醒", "goal_event": "比赛",
    ][focus]
  }
  public static func assessment(_ a: String) -> String {
    [
      "on_track": "目标与当前能力相符", "ambitious": "目标有挑战，需要坚持训练", "out_of_reach": "目标明显快于当前能力，配速课按当前能力安排",
      "finish": "以完赛为目标", "unknown": "跑力预测不足，暂时无法评估目标",
    ][a] ?? ""
  }
  public static func category(_ c: String) -> String {
    ["5k": "5 公里", "10k": "10 公里", "half": "半程马拉松", "marathon": "全程马拉松"][c] ?? "比赛"
  }
  public static func outcome(_ o: String) -> String? {
    ["done": "已完成", "partial": "部分完成", "missed": "未完成", "extra": "计划外训练"][o]
  }
  /// "1:45:00" or "45:00" → seconds; nil when the text is not a duration.
  public static func seconds(_ text: String) -> Double? {
    let parts = text.trimmingCharacters(in: .whitespaces).replacingOccurrences(of: "：", with: ":")
      .split(separator: ":", omittingEmptySubsequences: false).map { Int($0) }
    guard (2...3).contains(parts.count), parts.allSatisfy({ $0 != nil && $0! >= 0 }) else { return nil }
    let v = parts.map { $0! }
    let (h, m, s) = v.count == 3 ? (v[0], v[1], v[2]) : (0, v[0], v[1])
    guard m < 60 || v.count == 2, s < 60 else { return nil }
    let total = Double(h * 3600 + m * 60 + s)
    return total >= 60 && total <= 86400 ? total : nil
  }
  public static func clock(_ seconds: Double) -> String {
    let t = Int(seconds.rounded())
    return t >= 3600 ? String(format: "%d:%02d:%02d", t / 3600, t / 60 % 60, t % 60) : String(format: "%d:%02d", t / 60, t % 60)
  }
}

/// Builds the training_config/v2 body the server validates.
public enum AdaptivePlanConfig {
  public struct Goal: Sendable, Equatable {
    public var distanceM: Double, date: String, targetTimeS: Double?
    public init(distanceM: Double, date: String, targetTimeS: Double?) {
      self.distanceM = distanceM; self.date = date; self.targetTimeS = targetTimeS
    }
  }
  public static func make(monday: String, days: [Int], longDay: Int, goal: Goal?, baselineMinutes: Double?) -> JSON {
    var c: [String: JSON] = [
      "version": .string(AdaptivePlan.configVersion), "startDate": .string(monday),
      "availableDays": .array(days.sorted().map { .int(Int64($0)) }), "longRunDay": .int(Int64(longDay)),
    ]
    if let goal {
      var g: [String: JSON] = ["type": .string("running"), "date": .string(goal.date), "distanceM": .number(goal.distanceM)]
      if let t = goal.targetTimeS { g["targetTimeS"] = .number(t) }
      c["goal"] = .object(g)
    }
    if let m = baselineMinutes, m > 0 { c["baselineWeeklyS"] = .number((m * 60).rounded()) }
    return .object(c)
  }
  /// Problems the server would reject, in the user's words; nil when valid.
  public static func problem(days: Set<Int>, longDay: Int, goal: Goal?, today: String, baselineMinutes: Double?) -> String? {
    if days.count < 2 { return "每周至少选择 2 个训练日。" }
    if !days.contains(longDay) { return "长距离日需要是训练日之一。" }
    if let goal {
      if goal.date < today { return "比赛日期不能早于今天。" }
      if !(1000...42195).contains(goal.distanceM) { return "比赛距离需在 1–42.195 公里之间。" }
    }
    if let m = baselineMinutes, !(10...1200).contains(m) { return "每周跑步时长请填写 10–1200 分钟。" }
    return nil
  }
}
