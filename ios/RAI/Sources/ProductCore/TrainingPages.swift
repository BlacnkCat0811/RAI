import Foundation
import SyncCore

public struct TrainingAuditSnapshot: Sendable {
  public var overrides: [JSON] = []
  public var next: Int64 = 0
  public var loaded = false
  public init() {}
}

public struct TrainingVersionPage: Sendable {
  public let versions: [JSON]
  public let next: Int64
}
public enum TrainingDetailSection: String, Sendable, CaseIterable {
  case effectiveSessions = "effective_sessions", baseSessions = "base_sessions"
  case contextWorkouts = "context_workouts", contextSignals = "context_signals", contextFeedback = "context_feedback"
}
public struct TrainingDetailPage: Sendable {
  public let metadata: JSON
  public let items: [JSON]
  public let next: Int64
}

extension ProductAPI {
  public func trainingHistory(plan: String, after: Int64 = 0, limit: Int = 20) async throws -> TrainingVersionPage {
    let page = try await get("/v2/training/plans/" + plan + "/history/versions", query: ["after": String(after), "limit": String(limit)])
    guard case .array(let items) = page["versions"], let next = page["next"].integer,
      next == 0 || (next > after && next == items.last?["version"].integer)
    else { throw UploadError(code: "invalid_history_page", status: 422) }
    var previous = after
    var identities = Set<String>()
    for item in items {
      guard let number = item["version"].integer, number > previous, item.string("planID") == plan,
        !item.string("effectiveVersionID").isEmpty, identities.insert(item.string("effectiveVersionID")).inserted, item["sessions"] == .null
      else { throw UploadError(code: "invalid_history_page", status: 422) }
      previous = number
    }
    return TrainingVersionPage(versions: items, next: next)
  }
  public func trainingDetail(plan: String, version: String, section: TrainingDetailSection, after: Int64 = 0, limit: Int = 20) async throws -> TrainingDetailPage {
    let page = try await get("/v2/training/plans/" + plan + "/effective/" + version + "/detail", query: ["section": section.rawValue, "after": String(after), "limit": String(limit)])
    guard page["version"].string("effectiveVersionID") == version, page["version"].string("planID") == plan,
      page.string("section") == section.rawValue, case .object = page["metadata"], case .array(let items) = page["items"],
      let next = page["next"].integer, next == 0 || (next > after && next == after + Int64(items.count))
    else { throw UploadError(code: "invalid_detail_page", status: 422) }
    return TrainingDetailPage(metadata: page["metadata"], items: items, next: next)
  }
}

// One reader is bound to an immutable version. It commits a cursor only after
// validation, preserves loaded rows on failure, and never loops over all pages.
public actor TrainingAuditReader {
  public let plan: String
  public let version: String
  private var state = TrainingAuditSnapshot()
  private var loading = false
  private var pinnedRevision: Int64?
  public init(plan: String, version: String) { self.plan = plan; self.version = version }
  public func snapshot() -> TrainingAuditSnapshot { state }
  public func loadMore(using api: ProductAPI) async throws -> TrainingAuditSnapshot {
    guard !loading, !state.loaded || state.next != 0 else { return state }
    loading = true
    defer { loading = false }
    let after = state.loaded ? state.next : 0
    let page = try await api.get("/v2/training/plans/" + plan + "/effective/" + version + "/audit",
                                 query: ["after": String(after), "limit": "20"])
    guard page["version"].string("effectiveVersionID") == version,
      page["version"].string("planID") == plan,
      case .array(let items) = page["overrides"], let next = page["next"].integer,
      let bound = page["version"]["overrideRevision"].integer,
      pinnedRevision == nil || pinnedRevision == bound,
      next == 0 || (next > after && next == items.last?["revision"].integer)
    else { throw UploadError(code: "invalid_audit_page", status: 422) }
    var previous = after
    var known = Set(state.overrides.map { $0.string("overrideID") })
    for item in items {
      guard let revision = item["revision"].integer, revision > previous, revision <= bound,
        !item.string("overrideID").isEmpty, known.insert(item.string("overrideID")).inserted,
        ["active", "revoked"].contains(item.string("status"))
      else { throw UploadError(code: "invalid_audit_page", status: 422) }
      previous = revision
    }
    try Task.checkCancellation()
    state.overrides += items
    state.next = next
    state.loaded = true
    pinnedRevision = bound
    return state
  }
}

public actor TrainingHistoryReader {
  public let plan: String
  private var versions: [JSON] = []
  private var next: Int64 = 0
  private var loaded = false
  private var loading = false
  public init(plan: String) { self.plan = plan }
  public func snapshot() -> TrainingVersionPage { TrainingVersionPage(versions: versions, next: next) }
  public func loadMore(using api: ProductAPI) async throws -> TrainingVersionPage {
    guard !loading, !loaded || next != 0 else { return snapshot() }
    loading = true
    defer { loading = false }
    let page = try await api.trainingHistory(plan: plan, after: loaded ? next : 0)
    let known = Set(versions.map { $0.string("effectiveVersionID") })
    guard page.versions.allSatisfy({ !known.contains($0.string("effectiveVersionID")) }) else { throw UploadError(code: "invalid_history_page", status: 422) }
    try Task.checkCancellation()
    versions += page.versions
    next = page.next
    loaded = true
    return snapshot()
  }
}
