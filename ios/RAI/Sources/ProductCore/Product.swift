import Foundation
import SyncCore

extension JSON {
  public var double: Double? {
    switch self {
    case .int(let n): Double(n)
    case .number(let n): n.isFinite ? n : nil
    default: nil
    }
  }
  public var flag: Bool { if case .bool(let b) = self { b } else { false } }
  public func string(_ key: String) -> String { self[key].text ?? "" }
  public func number(_ key: String) -> Double? { self[key].double }
}
public struct ProductSession: Identifiable, Hashable, Sendable {
  public let raw: JSON
  public init(_ raw: JSON) { self.raw = raw }
  public var id: String { raw.string("sessionID") }
  public var date: String { raw.string("date") }
  public var type: String { raw.string("type") }
  public var title: String {
    if let t = AdaptivePlanText.focusTitle(raw.string("focus")) { return t }
    return [
      "REST": "休息日", "EASY": "基础跑", "LONG": "长距离跑", "TEMPO": "节奏跑", "INTERVAL": "间歇训练",
      "RECOVERY": "恢复跑", "RACE": "比赛",
    ][type] ?? "训练"
  }
  public var distance: Double { raw.number("distanceM") ?? 0 }
  public var duration: Double { raw.number("durationS") ?? 0 }
  public var modified: Bool { raw["modifiedByUser"].flag }
  public var runnable: Bool { type != "REST" && raw.string("status") == "scheduled" }
  public static func == (a: Self, b: Self) -> Bool { a.raw == b.raw }
  public func hash(into hasher: inout Hasher) { hasher.combine(id) }
}
public struct ProductWorkout: Identifiable, Hashable, Sendable {
  public let raw: JSON
  public init(_ raw: JSON) { self.raw = raw }
  public var id: String { raw.string("workoutID") }
  public var title: String { raw.number("activityType") == 37 ? "跑步" : "运动" }
  public var date: Date { Date(timeIntervalSince1970: (raw.number("startUs") ?? 0) / 1_000_000) }
  public func metric(_ name: String) -> Double? {
    raw["metrics"].list.first { $0.string("metric") == name }?.number("value")
  }
  public static func == (a: Self, b: Self) -> Bool { a.raw == b.raw }
  public func hash(into hasher: inout Hasher) { hasher.combine(id) }
}
public struct ProductSnapshot: Sendable {
  public var plan: JSON = .null, state: JSON = .null, overview: JSON = .null, history: JSON = .null
  public var athlete: AthleteModelSnapshot?
  public var workouts: [ProductWorkout] = []
  public var next = ""
  public var failures: [String: String] = [:]
  public var athleteAbsenceText: String {
    if failures["athlete"] == "not_product_activated" { return "尚未启用" }
    return failures["athlete"] == nil ? "结果尚未就绪" : "模型结果读取失败，请稍后重试"
  }
  public var athleteRefreshFailed: Bool {
    failures["athlete"].map { $0 != "not_product_activated" } ?? false
  }
  public init() {}
  public var effective: JSON { plan["effective"] }
  public var sessions: [ProductSession] { effective["sessions"].list.map(ProductSession.init) }
  public var today: String { overview.string("today") }
  public var generationFailure: String? {
    guard ["failed", "repair_required"].contains(plan.string("generationState")),
      let code = plan["lastErrorCode"].text, !code.isEmpty
    else { return nil }
    return code
  }
  public var todaySession: ProductSession? {
    sessions.first { $0.date == today && $0.type != "REST" } ?? sessions.first { $0.date == today }
  }
  public func metric(_ name: String) -> Double? {
    // The server owns algorithm versions; the home page only needs a usable value.
    guard let metric = state["metrics"].list.first(where: { $0.string("metric") == name }),
      ["available", "partial"].contains(metric.string("quality"))
    else { return nil }
    return metric.number("value")
  }
}
extension ProductSnapshot {
  /// A read that failed this time keeps the last good value instead of blanking
  /// its card; the failure is still reported through `failures`.
  public func keepingLastGood(from previous: ProductSnapshot) -> ProductSnapshot {
    var out = self
    for key in failures.keys {
      switch key {
      case "plan": out.plan = previous.plan
      case "state": out.state = previous.state
      case "overview": out.overview = previous.overview
      case "history": out.history = previous.history
      case "athlete":
        if failures[key] != "not_product_activated" { out.athlete = previous.athlete }
      case "workouts": out.workouts = previous.workouts; out.next = previous.next
      default: break
      }
    }
    return out
  }
}
/// Last good home snapshot, kept on disk so a cold start paints immediately and
/// refreshes in the background.
public enum ProductSnapshotCache {
  private struct Stored: Codable {
    var plan: JSON, state: JSON, overview: JSON, history: JSON
    var athlete: AthleteModelSnapshot?
    var workouts: [JSON]
    var next: String
  }
  public static func encode(_ snapshot: ProductSnapshot) -> Data? {
    try? JSONEncoder().encode(Stored(
      plan: snapshot.plan, state: snapshot.state, overview: snapshot.overview, history: snapshot.history,
      athlete: snapshot.athlete, workouts: snapshot.workouts.map(\.raw), next: snapshot.next))
  }
  public static func decode(_ data: Data) -> ProductSnapshot? {
    guard let stored = try? JSONDecoder().decode(Stored.self, from: data) else { return nil }
    var out = ProductSnapshot()
    out.plan = stored.plan; out.state = stored.state; out.overview = stored.overview; out.history = stored.history
    out.athlete = stored.athlete; out.workouts = stored.workouts.map(ProductWorkout.init); out.next = stored.next
    return out
  }
}
public actor ProductAPI {
  public let client: RAIAPIClient
  public init(client: RAIAPIClient) { self.client = client }
  /// The four home reads are independent, so they run concurrently. The result
  /// is assembled in a fixed order after all of them finish. The legacy
  /// training-state endpoints were removed from the server; the v2 athlete
  /// model replaces them.
  public func load() async -> ProductSnapshot {
    let reads = [
      ("plan", "/v2/training/plans/current"), ("overview", "/v2/product/overview"),
      ("workouts", "/v2/workouts"), ("athlete", "/v2/analysis/athlete"),
    ]
    var results: [String: Result<JSON, any Error>] = [:]
    await withTaskGroup(of: (String, Result<JSON, any Error>).self) { group in
      for (key, path) in reads {
        group.addTask { [self] in
          do { return (key, .success(try await get(path, query: key == "athlete" ? ["schemaVersion": "rai.athlete_model/4"] : [:]))) } catch { return (key, .failure(error)) }
        }
      }
      for await (key, result) in group { results[key] = result }
    }
    var out = ProductSnapshot()
    for (key, _) in reads {
      switch results[key] {
      case .success(let value):
        switch key {
        case "athlete":
          do {
            let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: value.data())
            guard snapshot.supportsPresentation else { throw SyncFailure.invalid("unsupported_athlete_schema") }
            out.athlete = snapshot
          } catch {
            out.failures[key] = "athlete_response_invalid"
          }
        case "plan": out.plan = value
        case "state": out.state = value
        case "overview": out.overview = value
        case "history": out.history = value
        default:
          out.workouts = value["items"].list.map(ProductWorkout.init)
          out.next = value.string("next")
        }
      case .failure(let error as UploadError): out.failures[key] = error.code
      default: out.failures[key] = "connection_unavailable"
      }
    }
    return out
  }
  public func get(_ path: String, query: [String: String] = [:]) async throws -> JSON {
    try await request("GET", path, query: query)
  }
  public func athleteHistory(metric: String, cursor: String? = nil) async throws -> AthleteHistoryPage {
    var query = ["metric": metric, "limit": "30", "view": "revised"]
    if let cursor { query["cursor"] = cursor }
    let raw = try await get("/v2/analysis/athlete/history", query: query)
    return try JSONDecoder().decode(AthleteHistoryPage.self, from: raw.data())
  }
  /// The revised daily history of running fitness (newest first).
  public func runningFitnessHistory(from: String? = nil, to: String? = nil, cursor: String? = nil) async throws -> RunningFitnessHistory {
    var query = ["view": "revised", "limit": "90"]
    if let from { query["from"] = from }
    if let to { query["to"] = to }
    if let cursor { query["cursor"] = cursor }
    let raw = try await get("/v2/analysis/running-fitness/history", query: query)
    return try JSONDecoder().decode(RunningFitnessHistory.self, from: raw.data())
  }
  /// Asks the server to recompute running fitness with the active algorithm.
  /// The same key always returns the same operation; 202 only means queued.
  public func requestRunningFitnessReanalysis(workoutIDs: [String] = [], from: String? = nil, to: String? = nil, key: String) async throws -> RunningFitnessReanalysis {
    var body: [String: JSON] = ["view": .string("revised"), "referencePolicy": .string("current_effective"), "algorithmVersion": .string("v1")]
    if !workoutIDs.isEmpty { body["workoutIDs"] = .array(workoutIDs.map { .string($0.lowercased()) }) }
    if let from { body["from"] = .string(from) }
    if let to { body["to"] = .string(to) }
    let raw = try await request("POST", "/v2/analysis/running-fitness/reanalyses", body: JSON.object(body).data(), headers: ["Idempotency-Key": key])
    return try JSONDecoder().decode(RunningFitnessReanalysis.self, from: raw.data())
  }
  public func runningFitnessReanalysis(id: String) async throws -> RunningFitnessReanalysis {
    let raw = try await get("/v2/analysis/running-fitness/reanalyses/" + id)
    return try JSONDecoder().decode(RunningFitnessReanalysis.self, from: raw.data())
  }
  public func athleteDiagnostics() async throws -> AthleteDiagnostics {
    let raw = try await get("/v2/analysis/athlete/diagnostics")
    return try JSONDecoder().decode(AthleteDiagnostics.self, from: raw.data())
  }
  public func send(_ command: ProductCommand) async throws -> JSON {
    let result = try await request("POST", command.path, body: command.body.data())
    try command.validate(result)
    return result
  }
  /// Sends a typed model input. Unlike training commands, the response is an
  /// append-only input receipt; callers render the server reason on failure.
  public func sendModelInput(path: String, body: JSON) async throws -> JSON {
    try await request("POST", path, body: body.data())
  }
  private func request(
    _ method: String, _ path: String, body: Data? = nil, query: [String: String] = [:], headers: [String: String] = [:]
  ) async throws -> JSON {
    for attempt in 0..<4 {
      do { return try await client.call(method, path, body: body, query: query, headers: headers) } catch let error
        as UploadError where error.status == 429 && attempt < 3
      {
        try await Task.sleep(for: .seconds(max(1, error.retryAfter ?? 2)))
      }
    }
    throw SyncFailure.invalid("request_retry_limit")
  }
}
public struct ProductCommand: Codable, Equatable, Sendable, Identifiable {
  public var id: String { body.string("commandID") }
  public let path: String
  public let body: JSON
  public let observedEffectiveID: String
  public init(path: String, body: JSON, observedEffectiveID: String = "") {
    self.path = path
    self.body = body
    self.observedEffectiveID = observedEffectiveID
  }
  public func hasSameIntent(as other: ProductCommand) -> Bool {
    // Compare the existing canonical JSON representation, including every
    // semantic field. Only per-submission command/override identities differ.
    func intent(_ command: ProductCommand) -> Data? {
      try? JSON.object(
        command.body.fields.filter {
          !["commandID", "overrideID"].contains($0.key)
        }
      ).data()
    }
    guard path == other.path, let a = intent(self), let b = intent(other) else { return false }
    return a == b
  }
  public func resolvingPending(_ pending: ProductCommand?) throws -> ProductCommand {
    guard pending == nil || pending == self else {
      throw SyncFailure.localState("resolve_pending_command_first")
    }
    return self
  }
  public func validate(_ result: JSON) throws {
    func validID(_ key: String) -> Bool { result[key].text.flatMap(UUID.init(uuidString:)) != nil }
    if path.hasSuffix("/overrides") {
      guard validID("effectiveVersionID"),
        result.string("planID") == path.split(separator: "/").dropLast().last.map(String.init),
        let expected = body["expectedOverrideRevision"].integer,
        result["overrideRevision"].integer == expected + 1,
        case .array = result["sessions"]
      else { throw SyncFailure.receiptMismatch }
    } else if path.contains("/executions") {
      let id =
        body["executionID"].text ?? path.split(separator: "/").dropLast().last.map(String.init)
      guard result.string("executionID") == id, validID("effectiveVersionID"), validID("sessionID"),
        case .object = result["snapshot"],
        result["snapshot"]["sessionID"] == result["sessionID"],
        body["sessionID"] == .null || body["sessionID"] == result["sessionID"],
        body["effectiveVersionID"] == .null
          || body["effectiveVersionID"] == result["effectiveVersionID"],
        result["revision"].integer == (body["expectedRevision"].integer ?? 0) + 1,
        ["scheduled", "started", "completed", "skipped"].contains(result.string("status"))
      else { throw SyncFailure.receiptMismatch }
    } else {
      let id = body["planID"].text ?? path.split(separator: "/").dropLast().last.map(String.init)
      guard result.string("planID") == id, validID("planID"),
        result["configRevision"].integer != nil
      else { throw SyncFailure.receiptMismatch }
    }
  }
}
// Kept separately from P3 outbox. Atomic, backup-excluded, protected files hold
// immutable write identities before transport. Unknown outcomes cannot be edited.
public actor ProductJournal {
  struct State: Codable {
    var binding = ""
    var command: ProductCommand?
    var modelInput: ProductCommand?
    var executions: [String] = []
  }
  private let file: URL
  private var state: State
  public init(directory: URL) throws {
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    var url = directory
    var values = URLResourceValues()
    values.isExcludedFromBackup = true
    try url.setResourceValues(values)
    file = directory.appendingPathComponent("product-commands.json")
    state =
      FileManager.default.fileExists(atPath: file.path)
      ? try JSONDecoder().decode(State.self, from: Data(contentsOf: file)) : State()
  }
  private func persist(_ next: State) throws {
    try OutboxStore.write(JSONEncoder().encode(next), to: file)
    state = next
  }
  public func bind(_ identity: String) throws {
    guard state.binding.isEmpty || state.binding == identity else {
      throw SyncFailure.invalid("product_dataset_changed")
    }
    var next = state
    next.binding = identity
    try persist(next)
  }
  public func pending() -> ProductCommand? { state.command }
  public func stage(_ command: ProductCommand) throws {
    guard state.command == nil || state.command == command else {
      throw SyncFailure.localState("resolve_pending_command_first")
    }
    var next = state
    next.command = command
    try persist(next)
  }
  public func finish(_ command: ProductCommand, result: JSON) throws {
    guard state.command == command else {
      throw SyncFailure.localState("command_identity_mismatch")
    }
    var next = state
    next.command = nil
    let id = result.string("executionID")
    if !id.isEmpty && !next.executions.contains(id) { next.executions.append(id) }
    try persist(next)
  }
  public func discardRejected() throws {
    var next = state
    next.command = nil
    try persist(next)
  }
  public func pendingModelInput() -> ProductCommand? { state.modelInput }
  public func stageModelInput(_ command: ProductCommand) throws -> ProductCommand {
    if let existing = state.modelInput {
      let current = existing.body.fields.filter { $0.key != "id" }
      let proposed = command.body.fields.filter { $0.key != "id" }
      guard existing.path == command.path, current == proposed else {
        throw SyncFailure.localState("resolve_pending_command_first")
      }
      return existing
    }
    var next = state
    next.modelInput = command
    try persist(next)
    return command
  }
  public func finishModelInput(_ command: ProductCommand) throws {
    guard state.modelInput == command else {
      throw SyncFailure.localState("command_identity_mismatch")
    }
    var next = state
    next.modelInput = nil
    try persist(next)
  }
  public func executionIDs() -> [String] { state.executions }
}
public enum ProductFormat {
  public static func decimal(_ n: Double?, digits: Int = 0) -> String {
    guard let n, n.isFinite else { return "--" }
    return n.formatted(.number.precision(.fractionLength(digits)))
  }
  public static func distance(_ n: Double?) -> String { decimal(n.map { $0 / 1000 }, digits: 1) }
  public static func duration(_ n: Double?) -> String {
    guard let n, n.isFinite, n >= 0 else { return "--" }
    let s = Int(n)
    return String(format: "%d:%02d", s / 60, s % 60)
  }
  public static func pace(_ secondsPerM: Double?) -> String {
    guard let n = secondsPerM, n.isFinite, n > 0 else { return "--" }
    let s = Int((n * 1000).rounded())
    return String(format: "%d′%02d″", s / 60, s % 60)
  }
}

public enum ProductWriteFailure {
  public static func isDefinitiveRejection(_ error: UploadError) -> Bool {
    if error.status == 413 && error.code == "training_request_too_large" { return true }
    if error.status == 415
      && ["unsupported_media_type", "unsupported_content_encoding"].contains(error.code)
    {
      return true
    }
    return error.status == 422
      && !["invalid_response", "oversized_response", "invalid_http_response"].contains(error.code)
  }
}
