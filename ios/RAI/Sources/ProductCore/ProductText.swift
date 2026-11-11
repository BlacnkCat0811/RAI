public enum ProductReasonText {
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

  public static func text(for code: String?, state: String? = nil) -> String {
    if let code {
      switch code {
      case "not_enough_history": return "历史不足"
      case "insufficient_evidence": return "证据不足"
      case "data_sync_incomplete": return "同步未完成"
      case "source_conflict": return "来源冲突"
      case "source_selection_required": return "需要选择来源"
      case "data_stale": return "数据已过期"
      case "missing_heart_rate": return "缺少心率"
      case "recovery_data_incomplete": return "恢复数据不完整"
      case "training_load_unknown": return "负荷未知"
      case "not_product_activated": return "尚未启用"
      case "deleted": return "已删除"
      case "sensitivity_unbounded": return "敏感性范围不可定义"
      default: return "暂不可用"
      }
    }
    return state == "collecting" ? "积累中" : "暂不可用"
  }
}
