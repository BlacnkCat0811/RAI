import Foundation

/// Which load failures are worth a retry prompt. A code in `expectedAbsence`
/// means the server has no product yet (no published model, no training plan);
/// retrying changes nothing, so those codes must never surface as a fault.
public enum ProductLoadFailures {
  public static let expectedAbsence: Set<String> = [
    "analysis_not_ready", "training_not_found", "not_product_activated",
  ]
  public static func needsRetry(_ failures: [String: String]) -> Bool {
    failures.values.contains { !expectedAbsence.contains($0) }
  }
}

/// Home sync badge. A Workout the server already accepted stays uploaded across
/// App restarts, so the persisted last-upload time outranks the per-round
/// in-memory results (which are empty after a relaunch).
public enum ProductSyncBadge: Equatable, Sendable {
  case syncing, attention, uploaded, pending, never
  public static func resolve(
    busy: Bool, hasError: Bool, hasFailedResult: Bool, hasSucceededResult: Bool,
    lastUploadAt: Date?, pendingChanges: Int
  ) -> Self {
    if busy { return .syncing }
    if hasError || hasFailedResult { return .attention }
    if hasSucceededResult || lastUploadAt != nil { return .uploaded }
    if pendingChanges > 0 { return .pending }
    return .never
  }
  public var text: String {
    switch self {
    case .syncing: return "同步中"
    case .attention: return "需处理"
    case .uploaded: return "已上传"
    case .pending: return "待同步"
    case .never: return "尚未同步"
    }
  }
}

extension ProductWorkout {
  /// One line for the activity row. Distance and pace appear only when the
  /// server actually has them; a Workout without distance must not render as
  /// "-- 公里 · --/公里", and a pace without distance is meaningless.
  public var detailLine: String {
    var parts: [String] = []
    let distance = raw.number("distanceM")
    let hasDistance = distance.map { $0.isFinite && $0 > 0 } ?? false
    if hasDistance, let distance { parts.append(ProductFormat.distance(distance) + " 公里") }
    if let duration = raw.number("durationS"), duration.isFinite, duration > 0 {
      parts.append(ProductFormat.duration(duration))
    }
    if hasDistance, let pace = metric("average_pace"), pace.isFinite, pace > 0 {
      parts.append(ProductFormat.pace(pace) + "/公里")
    }
    return parts.joined(separator: " · ")
  }
}
