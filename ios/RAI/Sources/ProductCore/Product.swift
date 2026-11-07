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
    [
      "REST": "休息日", "EASY": "基础跑", "LONG": "长距离跑", "TEMPO": "节奏跑", "INTERVAL": "间歇训练",
      "RECOVERY": "恢复跑",
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
    guard let metric = state["metrics"].list.first(where: { $0.string("metric") == name }),
      ["available", "partial"].contains(metric.string("quality")),
      metric.string("algorithmVersion") == "v1"
        || (metric.string("algorithmVersion") == "v2"
          && metric.string("algorithm") == "fitness_fatigue")
    else { return nil }
    return metric.number("value")
  }
}
public actor ProductAPI {
  public let client: RAIAPIClient
  public init(client: RAIAPIClient) { self.client = client }
  public func load() async -> ProductSnapshot {
    // Bounded sequential reads fit the existing device admission budget.
    var out = ProductSnapshot()
    for (key, path) in [
      ("plan", "/v2/training/plans/current"), ("state", "/v2/analysis/state"),
      ("overview", "/v2/product/overview"), ("workouts", "/v2/workouts"),
      ("history", "/v2/analysis/history"), ("athlete", "/v2/analysis/athlete"),
    ] {
      do {
        let value = try await get(path)
        switch key {
        case "athlete":
          out.athlete = try? JSONDecoder().decode(AthleteModelSnapshot.self, from: value.data())
        case "plan": out.plan = value
        case "state": out.state = value
        case "overview": out.overview = value
        case "history": out.history = value
        default:
          out.workouts = value["items"].list.map(ProductWorkout.init)
          out.next = value.string("next")
        }
      } catch let e as UploadError { out.failures[key] = e.code } catch {
        out.failures[key] = "connection_unavailable"
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
    _ method: String, _ path: String, body: Data? = nil, query: [String: String] = [:]
  ) async throws -> JSON {
    for attempt in 0..<4 {
      do { return try await client.call(method, path, body: body, query: query) } catch let error
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
