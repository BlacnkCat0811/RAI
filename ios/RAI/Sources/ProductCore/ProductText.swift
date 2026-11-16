public enum ProductReasonText {
  /// The one prompt that asks for an App update: the server has something
  /// this build cannot show. Everything server-side reads differently.
  public static let updateApp = "有新功能，请更新 App"
  public static let independenceNotice = "RAI VO₂ 与外部 VO₂ 为独立来源；外部值不是 RAI 模型的标签或生理真值。"
  public static let experimentalLabel = "实验"
  public static func lowReadinessNotice(score: Double?) -> String? {
    guard let score, score.isFinite, score < 50 else { return nil }
    return "恢复偏低：训练建议将保持保守，不会自动改写已排训练。"
  }

  /// Three-step presentation of the recovery deviation from the personal baseline.
  public static func recoveryLevel(_ level: String?) -> String? {
    switch level {
    case "normal": return "与个人基线一致"
    case "mild_deviation": return "轻度偏离基线"
    case "marked_deviation": return "明显偏离基线"
    default: return nil
    }
  }

  /// 跑力 without a value: one qualified run with heart rate is enough to
  /// start, so say what is missing instead of asking for long runs.
  public static func runningFitness(modelStatus: String, reason: String?, state: String?) -> String {
    if modelStatus == "collecting" || state == "collecting" { return "完成一次带心率、5 分钟以上的稳定跑步即可开始估计" }
    return text(for: reason, state: state)
  }

  public static func text(for code: String?, state: String? = nil) -> String {
    if let code {
      switch code {
      case "not_enough_history": return "历史不足"
      case "insufficient_evidence": return "数据还不够"
      case "data_sync_incomplete": return "同步未完成"
      case "model_result_pending": return "正在计算"
      case "model_update_required": return ProductReasonText.updateApp
      case "unsupported", "model_switching": return "服务器正在更新模型，稍后自动恢复"
      case "technical_failure": return "读取失败，下拉重试"
      case "source_conflict": return "多个设备的数据不一致"
      case "source_selection_required": return "正在确定数据来源"
      case "data_stale": return "近期没有新数据"
      case "missing_heart_rate": return "缺少心率"
      case "recovery_data_incomplete": return "恢复数据不完整"
      case "training_load_unknown": return "缺少训练负荷"
      case "not_product_activated": return "尚未启用"
      case "deleted": return "已删除"
      case "sensitivity_unbounded": return "数据还不够"
      default: return "暂不可用"
      }
    }
    return state == "collecting" ? "积累中" : "暂不可用"
  }
}

/// Copy for the per-run training effect card.
public enum TrainingEffectText {
  public static let disclaimer = "基于心率与速度的估算，表示训练刺激大小，不代表能力已提升；与手表厂商的评分不可直接比较。"

  /// Band name for a 0-5 score; the server label wins, the value is the fallback.
  public static func label(_ score: TrainingEffectScore) -> String {
    let names = ["none": "无效果", "minor": "轻微", "maintaining": "维持", "improving": "提升",
                 "highly_improving": "大幅提升", "overreaching": "过量"]
    if let label = score.label, let name = names[label] { return name }
    guard let v = score.value, v.isFinite else { return "暂无" }
    switch v {
    case ..<1: return "无效果"
    case ..<2: return "轻微"
    case ..<3: return "维持"
    case ..<4: return "提升"
    case ..<5: return "大幅提升"
    default: return "过量"
    }
  }

  public static func isOverreaching(_ score: TrainingEffectScore) -> Bool {
    score.label == "overreaching" || (score.value ?? 0) >= 5
  }

  /// One actionable line explaining why a score is missing or limited.
  public static func reason(_ code: String) -> String? {
    [
      "hr_reference_estimated": "心率参考为估计，训练效果也为估计",
      "hr_reference_missing": "缺少可用的最大／静息心率参考，请检查心率设置",
      "hr_exposure_insufficient": "心率数据不足，无法计算有氧训练效果",
      "speed_reference_unavailable": "缺少这次跑步之前可用的速度能力基准",
      "effort_features_missing": "结果更新中，稍后自动显示",
      "speed_missing": "缺少速度数据，无法计算无氧评分",
      "short_sprint_uncorroborated": "短冲刺缺少功率或步频佐证，未计入",
      "bout_uncorroborated": "部分快速段心率没有响应，未计入",
      "speed_point_unsupported": "部分速度点不可靠，未计入",
      "high_intensity_unsupported": "高强度段佐证不足，为估算值",
      "indoor_speed": "室内跑速度来自手表估算",
      "speed_reference_estimated": "跑力参数为估算值",
      "ability_reference_exceeded": "本次表现超过当前跑力参数，跑力可能偏低",
      "hr_exposure_estimated": "心率覆盖不完整，为估算值",
      "training_level_estimated": "近期训练记录较少，训练等级为估算",
      "training_level_default": "训练记录不足，按默认训练等级计算",
      "invalid_input": "数据异常，无法计算",
    ][code]
  }

  public static func reasons(_ score: TrainingEffectScore) -> [String] {
    (score.reasonCodes ?? []).compactMap(reason)
  }

  public static func levelStatus(_ status: String?) -> String {
    ["observed": "近 28 天", "estimated": "近 90 天估算", "default": "默认"][status ?? ""] ?? "—"
  }
}
