// Synthetic acceptance tool. The native App uses these same clients and DTOs.
import Foundation
import ProductCore
import SyncCore

struct Configuration: Codable { let baseURL: URL, token: String, collectorEpoch: String }
@main struct ProductProbe {
  static func require(_ condition: Bool, _ code: String) throws {
    if !condition { throw SyncFailure.invalid(code) }
  }
  static func main() async {
    do { try await run() } catch let e as UploadError {
      print("P6_PROBE_FAIL=" + e.code)
      exit(1)
    } catch let e as SyncFailure {
      print("P6_PROBE_FAIL=" + String(describing: e))
      exit(1)
    } catch {
      print("P6_PROBE_FAIL=local_or_transport")
      exit(1)
    }
  }
  static func run() async throws {
    let args = CommandLine.arguments
    guard args.count >= 4 else {
      throw SyncFailure.invalid("usage ProductProbe seed|verify private-config state-dir")
    }
    let config = try JSONDecoder().decode(
      Configuration.self, from: Data(contentsOf: URL(fileURLWithPath: args[2])))
    let client = RAIAPIClient(baseURL: config.baseURL, token: config.token)
    let api = ProductAPI(client: RAIAPIClient(baseURL: config.baseURL, token: config.token))
    let dir = URL(fileURLWithPath: args[3])
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    if args[1] == "performance" {
      let timings = ProbeTimings()
      let measured = ProductAPI(
        client: RAIAPIClient(
          baseURL: config.baseURL, token: config.token,
          metrics: SyncMetrics { name, value in timings.record(name, value) }))
      var operations: [String: [Double]] = [:]
      for _ in 0..<3 {
        for (name, path) in [
          ("Training", "/v2/training/plans/current"), ("Analysis", "/v2/analysis/state"),
          ("Activities", "/v2/workouts"), ("Weekly", "/v2/product/overview"),
          ("History", "/v2/analysis/history"),
        ] {
          let start = Date()
          _ = try await measured.get(path)
          operations[name, default: []].append(Date().timeIntervalSince(start) * 1000)
        }
        let start = Date()
        let value = await measured.load()
        try require(value.failures.isEmpty, "performance_read_failed")
        operations["Home", default: []].append(Date().timeIntervalSince(start) * 1000)
      }
      let report: [String: Any] = [
        "scope": "macOS Swift / actual fnOS API / synthetic only", "samples": 3,
        "operationsMS": operations, "clientTimingsMS": timings.snapshot(),
      ]
      try OutboxStore.write(
        JSONSerialization.data(withJSONObject: report, options: [.sortedKeys]),
        to: dir.appendingPathComponent("performance.json"))
      print("P6_API_PERFORMANCE=PASS (3 samples; HTTP+decode, no health values)")
      return
    }
    let hello = try await client.call("GET", "/v2/handshake")
    if args[1] == "pagination-read" {
      try await verifyPagination(api, plan: try await api.get("/v2/training/plans/current"))
      return
    }
    if args[1] == "pagination" {
      let current = try await api.get("/v2/training/plans/current")
      var config = current["config"].fields
      config["weeks"] = .int(16)
      _ = try await api.send(command("/v2/training/plans", ["planID": .string(Wire.id()), "config": .object(config)]))
      var plan = try await waitPlan(api) { $0["effective"] != .null && $0.string("generationState") == "succeeded" }
      let session = plan["effective"]["sessions"].list[0].string("sessionID")
      for _ in 0..<256 {
        let effective = try await api.send(override(plan: plan, session: session, operation: "edit", path: "notes", value: .string(String(repeating: "x", count: 2000))))
        var fields = plan.fields
        fields["effective"] = effective
        fields["overrideRevision"] = effective["overrideRevision"]
        plan = .object(fields)
      }
      try await verifyPagination(api, plan: plan)
      return
    }
    if args[1] == "seed" {
      let scope = Scope(
        serviceID: hello.string("serviceID"), datasetID: hello.string("datasetID"),
        datasetEpoch: hello.string("datasetEpoch"), collectorEpoch: config.collectorEpoch)
      let outbox = try OutboxStore(directory: dir.appendingPathComponent("sync"))
      try await outbox.bind(scope)
      let workoutType = "HKWorkoutTypeIdentifier"
      let heartType = "HKQuantityTypeIdentifierHeartRate"
      let stream = try await outbox.stream(type: workoutType, query: "synthetic-p6-acceptance")
      var workouts: [JSON] = []
      var samples: [JSON] = []
      var relations: [(String, String)] = []
      for n in 0..<7 {
        let start =
          Int64(Date().addingTimeInterval(-Double(n) * 86400 - 7200).timeIntervalSince1970)
          * 1_000_000
        let end = start + 3600_000_000
        let wid = Wire.id()
        let sid = Wire.id()
        relations.append((wid, sid))
        let source: JSON = .object(["bundleID": .string("synthetic.p6.acceptance")])
        workouts.append(
          .object([
            "op": .string("upsert"), "kind": .string("workout"), "type": .string(workoutType),
            "uuid": .string(wid), "source": source, "startUs": .int(start), "endUs": .int(end),
            "workout": .object([
              "activityType": .int(37), "durationUs": .int(3600_000_000),
              "totalDistance": .object(["value": .int(Int64(6000 + n * 500)), "unit": .string("m")]
              ),
            ]),
          ]))
        samples.append(
          .object([
            "op": .string("upsert"), "kind": .string("sample"), "type": .string(heartType),
            "uuid": .string(sid), "source": source, "startUs": .int(start), "endUs": .int(end),
            "quantity": .object(["value": .int(Int64(115 + n)), "unit": .string("count/min")]),
          ]))
      }
      try await outbox.stage(
        BatchBuilder.page(
          scope: scope, stream: stream, events: workouts, anchor: Data("synthetic-workouts".utf8),
          store: outbox))
      try await drain(outbox, client)
      let hrStream = try await outbox.stream(type: heartType, query: "synthetic-p6-acceptance")
      try await outbox.stage(
        BatchBuilder.page(
          scope: scope, stream: hrStream, events: samples, anchor: Data("synthetic-hr".utf8),
          store: outbox))
      try await drain(outbox, client)
      for (wid, sid) in relations {
        var fields = scope.fields
        fields.merge([
          "associationID": .string(Wire.id()), "sampleType": .string(heartType),
          "sampleUUID": .string(sid), "workoutUUID": .string(wid),
          "source": .object(["bundleID": .string("synthetic.p6.acceptance")]),
        ]) { _, v in v }
        _ = try await request(
          client, "POST", "/v2/sync/associations", body: JSON.object(fields).data())
      }
      var cal = Calendar(identifier: .gregorian)
      cal.timeZone = TimeZone(secondsFromGMT: 0)!
      let now = Date()
      let mon = cal.date(
        byAdding: .day, value: -((cal.component(.weekday, from: now) + 5) % 7), to: now)!
      let f = DateFormatter()
      f.calendar = cal
      f.timeZone = cal.timeZone
      f.dateFormat = "yyyy-MM-dd"
      let config: JSON = .object([
        "startDate": .string(f.string(from: mon)), "weeks": .int(8),
        "goal": .object([
          "type": .string("running"),
          "date": .string(f.string(from: cal.date(byAdding: .day, value: 55, to: mon)!)),
          "distanceM": .int(21098),
        ]), "availableDays": .array([1, 3, 5, 7].map { .int($0) }), "longRunDay": .int(7),
        "baselineWeeklyM": .int(30000), "previousLongM": .int(12000),
      ])
      let p = try await request(
        client,
        "POST", "/v2/training/plans",
        body: JSON.object([
          "commandID": .string(Wire.id()), "planID": .string(Wire.id()), "config": config,
        ]).data())
      try await waitPlan(api) {
        $0["effective"] != .null && $0.string("generationState") == "succeeded"
      }
      try OutboxStore.write(
        JSON.object(["planID": p["planID"], "workoutID": .string(relations[0].0)]).data(),
        to: dir.appendingPathComponent("ids.json"))
      print(
        "P6_SYNTHETIC_SEED=PASS (7 workouts, explicit HR relationships, real P3 durable ACK, actual Worker)"
      )
      return
    }
    if args[1] == "mutate" {
      let plan = try await api.get("/v2/training/plans/current")
      let session = plan["effective"]["sessions"].list.first {
        $0.number("type") == nil && $0.string("type") != "REST"
      }!
      _ = try await api.send(
        override(
          plan: plan, session: session.string("sessionID"), operation: "edit", path: "notes",
          value: .string("synthetic concurrent edit")))
      print("P6_CONCURRENT_MUTATION=PASS")
      return
    }
    let start = Date()
    let snapshot = await api.load()
    try require(snapshot.failures.isEmpty, "read_api_failure")
    try require(
      snapshot.metric("readiness") != nil && snapshot.workouts.count == 7,
      "published_analysis_missing")
    try require(!snapshot.history["metrics"].list.isEmpty, "history_missing")
    let homeElapsed = Date().timeIntervalSince(start) * 1000
    print("P6_HOME_REAL_API=PASS load_ms=\(homeElapsed)")
    var plan = snapshot.plan
    let monday = plan["effective"]["sessions"].list.first { $0.number("slot") == 0 }!
    let sunday = plan["effective"]["sessions"].list.first { $0.number("slot") == 6 }!
    let saturday = plan["effective"]["sessions"].list.first { $0.number("slot") == 5 }!
    let journal = try ProductJournal(directory: dir.appendingPathComponent("commands"))
    try await journal.bind(hello.string("datasetID"))
    let edit = override(
      plan: plan, session: monday.string("sessionID"), operation: "edit", path: "distanceM",
      value: .int(7000))
    try await journal.stage(edit)
    let receipt = try await api.send(edit)  // Deliberately withhold the response from the journal.
    let reopened = try ProductJournal(directory: dir.appendingPathComponent("commands"))
    let saved = await reopened.pending()!
    let retry = try await api.send(saved)
    try require(receipt == retry, "lost_ack_duplicate")
    try await reopened.finish(saved, result: retry)
    print("P6_LOST_ACK=PASS same durable command, identical receipt, journal reopened")
    let stale = override(
      plan: plan, session: monday.string("sessionID"), operation: "edit", path: "notes",
      value: .string("preserved synthetic draft"))
    try await reopened.stage(stale)
    do {
      _ = try await api.send(stale)
      throw SyncFailure.invalid("stale_revision_accepted")
    } catch let e as UploadError { try require(e.status == 409, "wrong_conflict") }
    try require(await reopened.pending() == stale, "draft_lost")
    plan = try await api.get("/v2/training/plans/current")
    try await reopened.discardRejected()
    let reviewed = override(
      plan: plan, session: monday.string("sessionID"), operation: "edit", path: "notes",
      value: stale.body["value"])
    try await reopened.stage(reviewed)
    let reviewedResult = try await api.send(reviewed)
    try await reopened.finish(reviewed, result: reviewedResult)
    print("P6_CONFLICT=PASS 409, draft retained, fetch latest, explicit review and new command")
    plan = try await api.get("/v2/training/plans/current")
    let moved = try await api.send(
      override(
        plan: plan, session: sunday.string("sessionID"), operation: "move", path: "date",
        value: saturday["date"]))
    let execution = try await api.send(
      command(
        "/v2/training/executions",
        [
          "executionID": .string(Wire.id()), "planID": plan["planID"],
          "effectiveVersionID": moved["effectiveVersionID"], "sessionID": monday["sessionID"],
        ]))
    var updatedConfig = plan["config"].fields
    updatedConfig["baselineWeeklyM"] = .int(12000)
    _ = try await api.send(
      command(
        "/v2/training/plans/" + plan.string("planID") + "/changes",
        [
          "operation": .string("update_config"), "expectedConfigRevision": plan["configRevision"],
          "config": .object(updatedConfig),
        ]))
    let next = try await waitPlan(api) {
      $0["effective"].string("baseVersionID") != moved.string("baseVersionID")
        && $0.string("generationState") == "succeeded"
    }
    let mondayNext = next["effective"]["sessions"].list.first {
      $0["sessionID"] == monday["sessionID"]
    }!
    let sunNext = next["effective"]["sessions"].list.first {
      $0["sessionID"] == sunday["sessionID"]
    }!
    try require(
      mondayNext.number("distanceM") == 7000 && sunNext["date"] == saturday["date"]
        && sunNext.number("distanceM") != sunday.number("distanceM"), "override_survival")
    var ex = try await api.get("/v2/training/executions/" + execution.string("executionID"))
    try require(
      ex["snapshot"] == execution["snapshot"]
        && ex["effectiveVersionID"] == moved["effectiveVersionID"], "snapshot_changed")
    print(
      "P6_REGENERATION=PASS Monday user distance survives, Sunday move survives, untouched distance follows Base; snapshot frozen"
    )
    ex = try await api.send(
      command(
        "/v2/training/executions/" + ex.string("executionID") + "/changes",
        ["operation": .string("complete"), "expectedRevision": ex["revision"]]))
    try require(ex.string("workoutID").isEmpty, "unexpected_auto_association")
    let ids = try JSON.decode(Data(contentsOf: dir.appendingPathComponent("ids.json")))
    ex = try await api.send(
      command(
        "/v2/training/executions/" + ex.string("executionID") + "/changes",
        [
          "operation": .string("associate"), "expectedRevision": ex["revision"],
          "workoutID": ids["workoutID"],
        ]))
    try require(ex["feedback"].list.last?["distance"]["actual"].double != nil, "feedback_missing")
    print("P6_FEEDBACK=PASS complete before explicit association; server planned/actual/ratio")
    let report: JSON = .object([
      "closedLoopElapsedMS": .number(Date().timeIntervalSince(start) * 1000),
      "publishedAnalysis": .bool(true), "lostACK": .bool(true), "conflictDraft": .bool(true),
      "overrideSurvival": .bool(true), "snapshot": .bool(true), "feedback": .bool(true),
    ])
    try OutboxStore.write(report.data(), to: dir.appendingPathComponent("report.json"))
  }
  static func verifyPagination(_ api: ProductAPI, plan: JSON) async throws {
    let reader = TrainingAuditReader(plan: plan.string("planID"), version: plan["effective"].string("effectiveVersionID"))
    var auditPages = 0
    repeat { _ = try await reader.loadMore(using: api); auditPages += 1 } while await reader.snapshot().next != 0
    let audit = await reader.snapshot()
    try require(audit.overrides.count == 256 && auditPages == 13, "actual_audit_pagination_incomplete")
    let history = TrainingHistoryReader(plan: plan.string("planID"))
    var historyPages = 0
    repeat { _ = try await history.loadMore(using: api); historyPages += 1 } while await history.snapshot().next != 0
    let versions = await history.snapshot()
    try require(versions.versions.count == 257 && historyPages == 13, "actual_history_pagination_incomplete")
    var cursor: Int64 = 0
    var prescriptions = 0
    repeat {
      let detail = try await api.trainingDetail(plan: plan.string("planID"), version: plan["effective"].string("effectiveVersionID"), section: .effectiveSessions, after: cursor)
      prescriptions += detail.items.count
      cursor = detail.next
    } while cursor != 0
    try require(prescriptions == 112, "actual_detail_pagination_incomplete")
    print("FULL_REVIEW_SWIFT_PAGINATION=PASS audit=256/13pages history=257/13pages prescriptions=112 weeks=16 actual_HTTP_decode=true")
  }
  static func request(_ client: RAIAPIClient, _ method: String, _ path: String, body: Data)
    async throws -> JSON
  {
    for attempt in 0..<16 {
      do { return try await client.call(method, path, body: body) } catch let e as UploadError
        where e.status == 429 && attempt < 15
      { try await Task.sleep(for: .seconds(max(1, e.retryAfter ?? 2))) }
    }
    throw SyncFailure.invalid("seed_rate_limit")
  }
  static func drain(_ store: OutboxStore, _ api: RAIAPIClient) async throws {
    for _ in 0..<30 {
      try await OutboxPipeline(store: store, api: api).drain()
      let s = await store.snapshot()
      if s.pages.isEmpty { return }
      if let e = s.pages.compactMap(\.error).first, !e.retryable { throw e }
      try await Task.sleep(for: .seconds(2))
    }
    throw SyncFailure.invalid("outbox_timeout")
  }
  @discardableResult static func waitPlan(_ api: ProductAPI, _ predicate: (JSON) -> Bool)
    async throws -> JSON
  {
    for _ in 0..<120 {
      let p = try await api.get("/v2/training/plans/current")
      if predicate(p) { return p }
      try await Task.sleep(for: .seconds(2))
    }
    throw SyncFailure.invalid("publication_timeout")
  }
  static func command(_ path: String, _ fields: [String: JSON]) -> ProductCommand {
    var fields = fields
    fields["commandID"] = .string(Wire.id())
    return ProductCommand(path: path, body: .object(fields))
  }
  static func override(plan: JSON, session: String, operation: String, path: String, value: JSON)
    -> ProductCommand
  {
    command(
      "/v2/training/plans/" + plan.string("planID") + "/overrides",
      [
        "expectedOverrideRevision": plan["overrideRevision"], "overrideID": .string(Wire.id()),
        "sessionID": .string(session), "operation": .string(operation), "path": .string(path),
        "value": value,
      ])
  }
}

// The lock protects every access from the client's Sendable metrics callback.
final class ProbeTimings: @unchecked Sendable {
  private let lock = NSLock()
  private var values: [String: [Double]] = [:]
  func record(_ name: String, _ value: Double) {
    lock.lock()
    defer { lock.unlock() }
    values[name, default: []].append(value)
  }
  func snapshot() -> [String: [Double]] {
    lock.lock()
    defer { lock.unlock() }
    return values
  }
}
