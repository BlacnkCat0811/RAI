import Foundation
import ProductCore
import SwiftUI
import SyncCore
import UIKit
import os

enum UserFacingText {
  /// A failure code can carry a raw HealthKit metadata key as a suffix
  /// (`unsupported_workout_metadata_unit:<key>`). The workout detail view
  /// prints the code verbatim and the diagnostic report copies it, so the
  /// suffix is dropped here: the key name never leaves this device.
  static func stableCode(_ code: String) -> String {
    guard let separator = code.firstIndex(of: ":") else { return code }
    return String(code[..<separator])
  }

  /// Two kinds of failure reach the user: the ones that fix themselves (try
  /// again later) and the ones that need an action. Everything else falls back
  /// to a generic line; the exact code stays in the diagnostics.
  static func error(_ code: String) -> String {
    if code.hasPrefix("keychain_") || code.hasPrefix("session_keychain_") { return "钥匙串读写失败" }
    if code.hasPrefix("network_") { return "网络不可用，稍后会自动重试" }
    if code.hasPrefix("unsupported_workout_metadata") || code.hasPrefix("nonfinite_workout_metadata")
      || code.hasPrefix("workout_metadata_limit") || code == "metadata_requires_review"
    {
      return "记录包含暂不支持的健康元数据，本条记录未上传，原始数据仍在健康 App 中"
    }
    return [
      // Retry later
      "connection_unavailable": "无法连接服务器，稍后重试",
      "device_rate_exceeded": "同步请求过多，稍后自动继续",
      "device_upload_capacity_exceeded": "服务繁忙，稍后会自动重试",
      "dataset_rate_exceeded": "同步请求过多，稍后自动继续",
      "workout_ingest_backlog_exceeded": "服务器待处理记录较多，稍后重试",
      "request_retry_limit": "请求重试次数已用尽，稍后重试",
      "interrupted": "上传已中断，可再次同步",
      "sync_already_running": "已有同步任务在进行",
      "analysis_not_ready": "分析尚未就绪",
      // Needs an action
      "configure_development_api_first": "请先连接服务器",
      "authentication_required": "访问令牌已失效，请重新连接服务器",
      "dataset_retired": "此同步空间已停用，请使用新的连接链接",
      "session_not_active": "当前连接尚未启用或已停用",
      "session_handshake_mismatch": "凭证与服务器不匹配，连接未保存",
      "session_identity_conflict": "凭证与已保存的连接不一致，原连接已保留",
      "session_switch_busy": "请等待当前同步结束后再切换服务器",
      "invalid_api_origin": "服务器地址无效",
      "invalid_collector_epoch": "设备标识无效",
      "missing_device_token": "缺少访问令牌",
      "https_required": "服务器地址必须以 https:// 开头",
      "unsupported_handshake": "App 与服务器版本不兼容，请更新",
      "unsupported_protocol": "App 与服务器版本不兼容，请更新 App",
      "healthkit_unavailable": "设备不支持健康数据",
      "healthkit_read_failed": "健康数据读取失败，请检查授权",
      "workout_not_visible": "健康 App 中找不到这条跑步记录",
      "source_policy_excluded": "这条记录不是 Apple Watch 原生跑步，不会上传",
      "source_policy_pending": "来源尚未确认，暂不上传",
      "workout_deleted": "这条跑步记录已从健康 App 删除",
      "bundle_too_large": "这条记录的数据量超过安全上限",
      "workout_ingest_rejected": "服务器未能解析这条记录，可重新上传",
      "workout_identity_conflict": "服务器已有这条跑步的不同内容",
      "sample_identity_conflict": "服务器已有这条跑步的不同数据",
      "route_read_incomplete": "路线读取未完成，本条记录未上传",
      "invalid_time_range": "记录时间范围无效，本条记录未上传",
      "receipt_mismatch": "服务器返回结果异常，请重试",
      "invalid_response": "服务器返回结果异常",
      "invalid_training_override": "训练调整无效",
      "resolve_pending_command_first": "请先重新提交上一次修改",
      "training_not_found": "未找到训练计划",
    ][code] ?? "操作未完成，请稍后重试"
  }

  static func syncFailure(_ failure: SyncFailure) -> String {
    switch failure {
    case .invalid(let code), .localState(let code): return error(code)
    case .receiptMismatch: return "服务器返回结果异常，请重试"
    }
  }

  static func generationFailure(_ code: String) -> String {
    [
      "insufficient_context": "训练基础信息不足",
      "goal_unreachable": "目标与当前基础不匹配",
      "schedule_conflict": "训练日期安排存在冲突",
      "invalid_config": "训练目标配置无效",
      "provider_unavailable": "训练服务暂不可用",
      "timeout": "训练计划生成超时",
    ][code] ?? "训练计划暂未生成"
  }
}

@MainActor @Observable
final class ProductModel {
  var data = ProductSnapshot()
  var loading = false
  var configured = false
  /// Simulator Debug offline preview of a snapshot file; no network at all.
  private(set) var previewOnly = false
  var error: String?
  var conflict = false
  var pending: ProductCommand?
  var pendingModelInput: ProductCommand?
  var execution: JSON = .null
  var executionIDs: [String] = []
  var audit: JSON = .null
  var auditNext: Int64 = 0
  var auditLoading = false
  var auditError: String?
  private var auditVersion = ""
  private var auditLoadingVersion = ""
  private var auditReader: TrainingAuditReader?
  var statisticsRevision = 0
  var statistics: RunningStatistics?
  var statisticsLoading = false
  var statisticsError: String?
  private var statisticsRequests = RunningStatisticsRequests()
  var statisticsTimezone: TimeZone {
    TimeZone(identifier: data.overview.string("timezone")) ?? .current
  }
  func loadStatistics(_ window: RunningStatisticsWindow) async {
    let request = statisticsRequests.begin()
    statisticsLoading = true
    statisticsError = nil
    statistics = nil
    defer { if statisticsRequests.accepts(request) { statisticsLoading = false } }
    await configure()
    guard let api else {
      if statisticsRequests.accepts(request) { statisticsError = "请先连接服务器。" }
      return
    }
    do {
      let result = try await api.runningStatistics(window: window)
      guard statisticsRequests.accepts(request), !Task.isCancelled else { return }
      statistics = result
    } catch is CancellationError {
      return
    } catch {
      guard statisticsRequests.accepts(request), !Task.isCancelled else { return }
      statisticsError = "跑量统计加载失败，请重试。"
    }
  }
  #if DEBUG
  private var localAcceptanceNamespace: String?
  /// Uses an explicit, already-verified local handshake. Never loads or binds
  /// the normal session, its Keychain account, journal or home cache.
  func configureLocalAcceptance(secret: ClientConfiguration.Secret, namespace: UUID, hello: JSON, expectedDatasetSHA256: String) async throws {
    guard hello.string("collectorEpoch") == secret.configuration.collectorEpoch,
      Wire.hash(Data(hello.string("datasetID").utf8)) == expectedDatasetSHA256 else {
      throw SyncFailure.invalid("local_acceptance_identity_mismatch")
    }
    let journalNamespace = "local-acceptance-" + namespace.uuidString.lowercased()
    localAcceptanceNamespace = journalNamespace
    let client = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token)
    try await bindProduct(secret: secret, api: ProductAPI(client: client), hello: hello, namespace: journalNamespace)
  }
  #endif
  private var api: ProductAPI?
  private var journal: ProductJournal?
  private let log = Logger(subsystem: "com.rai.v2.ios", category: "product.performance")
  func configure() async {
    guard api == nil, !previewOnly else { return }
    #if DEBUG
    if localAcceptanceNamespace != nil {
      error = "本机验收连接已关闭，请重新打开验收页面。"
      return
    }
    #endif
    do {
      #if DEBUG && targetEnvironment(simulator)
        // A private test configuration points at a real isolated API. No
        // fixture values or token appear in the app binary or launch args.
        if ProcessInfo.processInfo.environment["RAI_SYNTHETIC_ACCEPTANCE"] == "1",
          let value = ProcessInfo.processInfo.environment["RAI_ACCEPTANCE_CONFIG"]
        {
          // Simulator test bundles cannot read arbitrary host paths from the
          // app sandbox. Accept inline JSON and retain file-path support for
          // local debugging.
          let data: Data
          if value.trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("{") {
            data = Data(value.utf8)
          } else {
            data = try Data(contentsOf: URL(fileURLWithPath: value))
          }
          // The root view starts foreground recovery at the same time as the
          // first product refresh. A session switch must not race that work.
          for _ in 0..<200 where SyncModel.shared.busy {
            try? await Task.sleep(for: .milliseconds(50))
          }
          let secret = try JSONDecoder().decode(ClientConfiguration.Secret.self, from: data)
          if try ClientConfiguration.load()?.token != secret.token {
            try await SyncModel.shared.bindNewSession(
              baseURL: secret.configuration.baseURL.absoluteString,
              collectorEpoch: secret.configuration.collectorEpoch, token: secret.token)
          }
        }
      #endif
      guard let secret = try ClientConfiguration.load() else {
        // Not connected yet is the first-run state, not a fault: the home
        // setup card explains what to do.
        error = nil
        return
      }
      let client = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token)
      let productAPI = ProductAPI(client: client)
      let hello = try await productAPI.get("/v2/handshake")
      try await bindProduct(secret: secret, api: productAPI, hello: hello,
        namespace: try ClientConfiguration.activeDescriptor()?.sessionID ?? "unconfigured")
    } catch let failure as UploadError {
      log.error("product_bootstrap_transport=\(failure.code, privacy: .public)")
      error = "暂时连不上服务器：\(UserFacingText.error(failure.code))。"
    } catch let failure as SyncFailure {
      let code: String
      switch failure {
      case .invalid(let value), .localState(let value): code = value
      case .receiptMismatch: code = "receipt_mismatch"
      }
      log.error("product_bootstrap_failure=\(code, privacy: .public)")
      error = "本地数据读取失败：\(UserFacingText.error(code))。"
    } catch {
      log.error("product_bootstrap_local_code=\((error as NSError).code, privacy: .public)")
      self.error = "暂时连不上服务器，可稍后下拉重试。"
    }
  }
  private func bindProduct(secret: ClientConfiguration.Secret, api productAPI: ProductAPI, hello: JSON, namespace: String) async throws {
    let directory = try FileManager.default.url(
      for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true
    ).appendingPathComponent("RAIV2Product").appendingPathComponent(namespace)
    let store = try ProductJournal(directory: directory)
    try await store.bind(secret.configuration.baseURL.absoluteString + ":" + hello.string("serviceID") + ":"
      + hello.string("datasetID") + ":" + hello.string("datasetEpoch"))
    api = productAPI
    journal = store
    configured = true
    pending = await store.pending()
    pendingModelInput = await store.pendingModelInput()
    executionIDs = await store.executionIDs()
    if let id = executionIDs.last {
      execution = (try? await productAPI.get("/v2/training/executions/" + id)) ?? .null
    }
  }
  func reconnect() async {
    guard !loading else { return }
    statisticsRequests.invalidate()
    statisticsRevision += 1
    statistics = nil
    statisticsLoading = false
    statisticsError = nil
    api = nil
    configured = false
    await refresh()
  }
  private var cacheRestored = false
  private func cacheURL() -> URL? {
    #if DEBUG
    if let namespace = localAcceptanceNamespace {
      return FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first?
        .appendingPathComponent("RAIProductHome-" + namespace + ".json")
    }
    #endif
    guard let session = try? ClientConfiguration.activeDescriptor()?.sessionID,
      let caches = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first
    else { return nil }
    return caches.appendingPathComponent("RAIProductHome-\(session).json")
  }
  /// Encodes and writes the home snapshot off the main actor; the newest
  /// snapshot wins because writes are chained in order.
  private var homeCacheWrite: Task<Void, Never>?
  private func saveHomeCache(_ snapshot: ProductSnapshot) {
    guard let url = cacheURL() else { return }
    let previous = homeCacheWrite
    homeCacheWrite = Task.detached(priority: .utility) {
      await previous?.value
      guard let bytes = ProductSnapshotCache.encode(snapshot) else { return }
      try? bytes.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
  }
  func refresh() async {
    guard !loading else { return }
    loading = true
    defer { loading = false }
    let start = Date()
    // Stale-while-revalidate: paint the last good snapshot first, refresh behind it.
    if !cacheRestored {
      cacheRestored = true
      #if DEBUG && targetEnvironment(simulator)
        // Offline UI preview: paint a product snapshot file and never touch
        // the network, credentials or HealthKit. Simulator Debug builds only.
        if let path = ProcessInfo.processInfo.environment["RAI_PREVIEW_PRODUCT_SNAPSHOT"],
          let bytes = try? Data(contentsOf: URL(fileURLWithPath: path)),
          let cached = ProductSnapshotCache.decode(bytes)
        {
          data = cached
          configured = true
          previewOnly = true
          // Optional synthetic extras the preview cannot fetch: home route
          // thumbnails and the training-load history.
          if let extras = ProcessInfo.processInfo.environment["RAI_PREVIEW_HOME_EXTRAS"],
            let bytes = try? Data(contentsOf: URL(fileURLWithPath: extras))
          {
            struct Extras: Decodable {
              var routes: [String: [ProductCore.RoutePoint]]?
              var trainingLoad: [AthleteHistoryPoint]?
            }
            if let decoded = try? JSONDecoder().decode(Extras.self, from: bytes) {
              homeRoutes = (decoded.routes ?? [:]).mapValues { HomeRouteShape.downsample($0) }
              trainingLoadPoints = TrainingLoadSeries.points(fromAthlete: decoded.trainingLoad ?? [])
            }
          }
        }
      #endif
      if !previewOnly, let url = cacheURL(), let bytes = try? Data(contentsOf: url),
        let cached = ProductSnapshotCache.decode(bytes)
      {
        data = cached
      }
    }
    if previewOnly { return }
    await configure()
    guard let api else { return }
    loading = false
    await resumePendingWrites()
    loading = true
    let fetched = await api.load()
    // A refresh SwiftUI cancelled (the pull gesture ended or the view went
    // away) did not fail: keep what is on screen and record nothing.
    if Task.isCancelled { return }
    let loaded = fetched.keepingLastGood(from: data).keepingPublishedAthlete(from: data)
    if !ProductLoadFailures.needsRetry(loaded.failures) { saveHomeCache(loaded) }
    let stateStart = Date()
    data = loaded
    statisticsRevision += 1
    log.info("state_publication_ms=\(Date().timeIntervalSince(stateStart)*1000,privacy:.public)")
    // Only real faults prompt a retry. "The product is not activated yet" is an
    // expected empty state: retrying cannot change it, so it must not look broken.
    error = ProductLoadFailures.needsRetry(data.failures) ? "部分数据暂不可用，可下拉重试。" : nil
    watchAthleteRecompute()
    log.info("home_load_ms=\(Date().timeIntervalSince(start)*1000,privacy:.public)")
  }
  /// Submits one user-entered model input. The server assigns receipt time and
  /// known-at; the app never edits or deletes an accepted input locally.
  func submitModelInput(kind: String, payload: [String: JSON], effectiveFrom: Date, schemaVersion: String? = nil) async -> String? {
    guard let journal else { return "请先连接服务器。" }
    if pendingModelInput != nil, let failure = await retryModelInput() {
      return failure
    }
    let formatter = ISO8601DateFormatter()
    var fields: [String: JSON] = [
      "id": .string(UUID().uuidString.lowercased()),
      "kind": .string(kind),
      "subjectKey": .string("athlete"),
      "effectiveFrom": .string(formatter.string(from: effectiveFrom)),
      "provenance": .string("ios_user_entered"),
      "payload": .object(payload),
    ]
    if let schemaVersion { fields["schemaVersion"] = .string(schemaVersion) }
    let body = JSON.object(fields)
    let path: String
    switch kind {
    case "hr_reference": path = "/v2/model-inputs/hr-references"
    case "workout_effort": path = "/v2/model-inputs/workout-efforts"
    case "sleep_target": path = "/v2/model-inputs/sleep-targets"
    case "vo2max_source_selection": path = "/v2/model-inputs/vo2max-source-selections"
    case "trimp_parameters": path = "/v2/model-inputs/trimp-parameters"
    default: return "暂不支持此输入类型。"
    }
    do {
      let command = try await journal.stageModelInput(ProductCommand(path: path, body: body))
      pendingModelInput = command
      return await retryModelInput()
    } catch let failure as SyncFailure {
      return UserFacingText.syncFailure(failure)
    } catch {
      return "保存失败，请稍后重试。"
    }
  }
  func retryModelInput(refreshing: Bool = true) async -> String? {
    guard let api, let journal, let command = pendingModelInput else { return nil }
    do {
      let receipt = try await api.sendModelInput(path: command.path, body: command.body)
      guard receipt.string("inputID") == command.body.string("id"),
        !receipt.string("contentSHA256").isEmpty else { throw SyncFailure.receiptMismatch }
      try await journal.finishModelInput(command)
      pendingModelInput = nil
      if refreshing { await refresh() }
      return nil
    } catch let failure as UploadError {
      if [404, 409, 413, 422].contains(failure.status) {
        if (try? await journal.finishModelInput(command)) != nil { pendingModelInput = nil }
      }
      if pendingModelInput != nil { scheduleWriteResume() }
      return "保存失败：" + UserFacingText.error(failure.code)
    } catch {
      scheduleWriteResume()
      return Self.queuedWriteNotice
    }
  }
  /// Apple VO₂ sources of the last year, one per device/app.
  func loadVO2Sources() async -> [VO2SourceOption] {
    guard let api, let response = try? await api.get("/v2/analysis/vo2max-sources") else { return [] }
    return response["sources"].list.compactMap { item in
      guard let epoch = item["sourceEpoch"].text else { return nil }
      return VO2SourceOption(
        sourceEpoch: epoch, bundleIdentifier: item.string("bundleIdentifier"),
        productType: item.string("productType"), latestDate: item.string("latestDate"),
        samples: Int(item["samples"].integer ?? 0))
    }
  }
  /// Suggestion only: the highest 30 s HR of the last year, or nil.
  func loadObservedMaxHR() async -> JSON? {
    guard let api,
      let response = try? await api.get("/v2/analysis/parameters"),
      case .object = response["observedMaxHR"]
    else { return nil }
    return response["observedMaxHR"]
  }
  func loadHRSettingsV3() async -> (JSON?, Bool) {
    guard let api else { return (nil, false) }
    do {
      let hello = try await api.get("/v2/handshake")
      let supported = hello["capabilities"]["hrReferenceInputSchemas"].list.contains { $0.text == "rai.hr_reference/3" }
      return (try await api.get("/v2/analysis/parameters"), supported)
    } catch { return (nil, false) }
  }
  /// The HR reference effective now (`hr_reference/2`), or nil.
  func loadHRReference() async -> JSON? {
    guard let api,
      let response = try? await api.get("/v2/analysis/parameters"),
      case .object = response["hrReference"]
    else { return nil }
    return response["hrReference"]
  }
  /// Own flag on purpose: this must never flip the shared `loading`, which
  /// drives the home spinner and makes `submit()` / `refresh()` bail out.
  private var athleteRefreshing = false
  func refreshAthleteAssignment() async {
    guard !athleteRefreshing, let api else { return }
    athleteRefreshing = true
    defer { athleteRefreshing = false }
    do {
      let snapshot = try await api.athleteSnapshot()
      guard snapshot.supportsPresentation else {
        // Newer server contract: keep the last result, ask for an App update.
        if !Task.isCancelled { data.failures["athlete"] = ProductSnapshot.athleteUpdateRequired }
        return
      }
      if !Task.isCancelled {
        var next = data
        next.athlete = snapshot
        next = next.keepingPublishedAthlete(from: data)
        data.athlete = next.athlete
        data.athleteUpdating = next.athleteUpdating
        data.failures.removeValue(forKey: "athlete")
      }
    } catch let failure as UploadError where failure.code == "not_product_activated" {
      // The only case that really means "no athlete model": clear the card.
      if !Task.isCancelled {
        data.athlete = nil
        data.failures["athlete"] = failure.code
      }
    } catch let failure as UploadError {
      if !Task.isCancelled { data.failures["athlete"] = failure.code }
    } catch {
      // Network or decoding trouble keeps the last good value on screen.
      if !Task.isCancelled { data.failures["athlete"] = "athlete_response_invalid" }
    }
    if !Task.isCancelled, !data.athleteRefreshFailed { saveHomeCache(data) }
  }
  /// Every sync makes the server recompute today's release (about a minute or
  /// two). Re-read the athlete model until the new release is published, then
  /// refresh the whole home once; no fixed polling otherwise.
  private var recomputeWatch: Task<Void, Never>?
  private func watchAthleteRecompute() {
    guard data.athleteUpdating, recomputeWatch == nil else { return }
    recomputeWatch = Task { [weak self] in
      for _ in 0..<16 {
        try? await Task.sleep(for: .seconds(15))
        guard let self, !Task.isCancelled else { return }
        await self.refreshAthleteAssignment()
        if !self.data.athleteUpdating {
          self.recomputeWatch = nil
          await self.refresh()
          await self.loadTrainingLoadHistory()
          return
        }
      }
      self?.recomputeWatch = nil
    }
  }
  /// v2 athlete-model training-load history for the load card and page.
  var trainingLoadPoints: [TrainingLoadPoint] = []
  func loadTrainingLoadHistory() async {
    guard data.athlete?.supportsPresentation == true,
      let page = try? await loadAthleteHistory(metric: "trainingLoad")
    else { return }
    trainingLoadPoints = TrainingLoadSeries.points(fromAthlete: page.points)
  }
  func loadAthleteHistory(metric: String) async throws -> AthleteHistoryPage {
    guard let api else { throw SyncFailure.invalid("configure_development_api_first") }
    return try await api.athleteHistory(metric: metric)
  }
  /// Revised running-fitness history: daily long-term line and single-run points.
  func loadRunningFitnessHistory() async throws -> RunningFitnessHistory {
    guard let api else { throw SyncFailure.invalid("configure_development_api_first") }
    return try await api.runningFitnessHistory()
  }
  func loadAthleteDiagnostics() async {
    guard let api, let assignment = data.athlete?.assignment else {
      os_log("athlete diagnostics skipped: no api or athlete assignment", type: .error)
      return
    }
    do {
      let response = try await api.athleteDiagnostics()
      guard response.assignment == assignment else {
        os_log(
          "athlete diagnostics assignment mismatch: %{public}@ vs %{public}@", type: .error,
          String(describing: response.assignment), String(describing: assignment))
        diagnostics = nil
        diagnosticsError = "模型已更新，请下拉刷新。"
        return
      }
      diagnostics = response
      diagnosticsError = nil
    } catch let failure as UploadError where failure.status == 409 {
      os_log("athlete diagnostics not activated: %{public}@", type: .error, failure.code)
      diagnostics = nil
      diagnosticsError = "暂无进阶指标。"
    } catch {
      os_log("athlete diagnostics unavailable: %{public}@", type: .error, String(describing: error))
      diagnostics = nil
      diagnosticsError = "进阶指标暂不可用，可下拉重试。"
    }
  }
  func loadMore() async {
    guard let api, !data.next.isEmpty else { return }
    do {
      let page = try await api.get("/v2/workouts", query: ["before": data.next])
      let known = Set(data.workouts.map(\.id))
      data.workouts += page["items"].list.map(ProductWorkout.init).filter { !known.contains($0.id) }
      data.next = page.string("next")
    } catch { self.error = "加载更多失败" }
  }
  func loadWorkout(_ id: String) async -> ProductWorkout? {
    guard let api else { return nil }
    do {
      return try await api.get("/v2/workouts/" + id)["items"].list.first.map(ProductWorkout.init)
    } catch { return nil }
  }
  var workoutDetail: WorkoutDetailResponse?
  var workoutSeries: WorkoutSeriesResponse?
  var workoutFreshness: WorkoutFreshness?
  var workoutDetailLoading = false
  var workoutDetailError: String?
  /// The Workout whose pace review is still being computed by the server.
  var paceAnalysisLoadingID: String?
  var diagnostics: AthleteDiagnostics?
  var diagnosticsError: String?
  /// Last loaded detail per Workout, so going back and forth between records
  /// shows content at once while the refresh runs behind it.
  private var detailCache: [String: (detail: WorkoutDetailResponse, series: WorkoutSeriesResponse?, freshness: WorkoutFreshness?)] = [:]
  /// Detail and route thumbnails survive an App restart next to the home cache.
  private func detailDiskCache() -> WorkoutDetailDiskCache? {
    cacheURL().map { WorkoutDetailDiskCache(directory: $0.deletingPathExtension().appendingPathExtension("details")) }
  }
  private func routeDiskCache() -> HomeRouteDiskCache? {
    cacheURL().map { HomeRouteDiskCache(url: $0.deletingPathExtension().appendingPathExtension("routes.json")) }
  }
  private var openDetailID: String?
  func loadWorkoutDetail(_ id: String, refreshPace: Bool = true) async {
    guard let api else {
      workoutDetailError = "请先连接服务器。"
      return
    }
    openDetailID = id
    if workoutDetail?.workout.workoutID != id {
      if let cached = detailCache[id] {
        workoutDetail = cached.detail; workoutSeries = cached.series; workoutFreshness = cached.freshness
      } else {
        workoutDetail = nil; workoutSeries = nil; workoutFreshness = nil
        if let disk = detailDiskCache(), let entry = await Task.detached(operation: { disk.load(id) }).value,
          openDetailID == id, workoutDetail == nil
        {
          workoutDetail = entry.detail; workoutSeries = entry.series
          detailCache[id] = (entry.detail, entry.series, nil)
        }
      }
      workoutDetailError = nil
    }
    workoutDetailLoading = true
    defer { workoutDetailLoading = false }
    // The three reads run in parallel, but the page paints as soon as the
    // detail arrives; charts and freshness fill in when their own reads land.
    async let detailResult = Self.capture { try await api.workoutDetail(id) }
    async let seriesResult = Self.capture { try await api.workoutSeries(id) }
    async let freshnessResult = Self.capture { try await api.workoutFreshness(id) }
    // The pace review replays the running-fitness state and is the slowest
    // read; it fills its card when it lands, never blocking the page.
    let knownPace = detailCache[id]?.detail.paceAnalysis
    let fetchPace = refreshPace || knownPace == nil
    if fetchPace { paceAnalysisLoadingID = id }
    let paceTask = Task { fetchPace ? await Self.capture { try await api.workoutPaceAnalysis(id) } : nil }
    defer { paceTask.cancel() }
    do {
      var detail = try await detailResult.get()
      guard detail.workout.workoutID == id else { throw SyncFailure.invalid("invalid_response") }
      // Servers before 1.17.0 still embed the review; otherwise keep the last
      // known one on screen until the new one arrives.
      if detail.paceAnalysis == nil, let known = detailCache[id]?.detail.paceAnalysis ?? workoutDetail.flatMap({ $0.workout.workoutID == id ? $0.paceAnalysis : nil }) {
        detail.paceAnalysis = known
      }
      workoutDetail = detail
      workoutDetailError = nil
      detailCache[id] = (detail, workoutSeries, workoutFreshness)
      let (series, freshness) = await (seriesResult, freshnessResult)
      // The user may have opened another Workout meanwhile.
      guard workoutDetail?.workout.workoutID == id else { return }
      if let value = try? series.get(), value.workoutID == id { workoutSeries = value }
      if let value = try? freshness.get(), value.workoutID == id { workoutFreshness = value }
      if case .success(let review)? = await paceTask.value, review.workoutID.lowercased() == id.lowercased(),
        workoutDetail?.workout.workoutID == id
      {
        detail.paceAnalysis = review.paceAnalysis
        workoutDetail = detail
      }
      if paceAnalysisLoadingID == id { paceAnalysisLoadingID = nil }
      detailCache[id] = (detail, workoutSeries, workoutFreshness)
      if let disk = detailDiskCache() {
        let entry = WorkoutDetailDiskCache.Entry(detail: detail, series: workoutSeries)
        Task.detached(priority: .utility) { disk.save(id, entry) }
      }
    } catch let failure as UploadError {
      if paceAnalysisLoadingID == id { paceAnalysisLoadingID = nil }
      workoutDetailError = "跑步详情暂不可用：\(UserFacingText.error(failure.code))"
    } catch {
      if paceAnalysisLoadingID == id { paceAnalysisLoadingID = nil }
      workoutDetailError = "跑步详情暂不可用，请稍后重试。"
    }
  }
  /// Downsampled route of a Workout for the home thumbnail. Kept apart from
  /// `workoutDetail` so the home card never disturbs an open detail page.
  /// An empty array means the Workout has no usable route.
  var homeRoutes: [String: [ProductCore.RoutePoint]] = [:]
  private var homeRoutesRestored = false
  func loadHomeRoute(_ id: String) async {
    if !homeRoutesRestored, let disk = routeDiskCache() {
      homeRoutesRestored = true
      let stored = await Task.detached(operation: { disk.load() }).value
      homeRoutes.merge(stored) { current, _ in current }
    }
    guard homeRoutes[id] == nil, let api else { return }
    if let cached = detailCache[id] {
      homeRoutes[id] = HomeRouteShape.downsample(cached.detail.route.points)
      return
    }
    guard let detail = try? await api.workoutDetail(id), detail.workout.workoutID == id else { return }
    let route = HomeRouteShape.downsample(detail.route.points)
    homeRoutes[id] = route
    if let disk = routeDiskCache() { Task.detached(priority: .utility) { disk.save(id, route) } }
    // Opening this Workout then paints the fetched detail at once and only
    // revalidates behind it.
    if detailCache[id] == nil { detailCache[id] = (detail, nil, nil) }
  }
  private nonisolated static func capture<T: Sendable>(_ body: @Sendable () async throws -> T) async -> Result<T, any Error> {
    do { return .success(try await body()) } catch { return .failure(error) }
  }
  /// Polling only asks for freshness; the full detail is fetched again when the
  /// analysis state actually changes.
  func pollWorkoutFreshness(_ id: String) async -> Bool {
    guard let api, workoutDetail?.workout.workoutID == id,
      let fresh = try? await api.workoutFreshness(id)
    else { return false }
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys]
    let changed = (try? encoder.encode(fresh)) != workoutFreshness.flatMap { try? encoder.encode($0) }
    // The pace review only depends on the published analysis; new samples
    // alone refresh the detail and charts.
    let analysisChanged = (try? encoder.encode(fresh.analysis)) != workoutFreshness.flatMap { try? encoder.encode($0.analysis) }
    workoutFreshness = fresh
    if changed { await loadWorkoutDetail(id, refreshPace: analysisChanged) }
    return changed
  }
  func loadAudit() async {
    let plan = data.plan.string("planID")
    let version = data.effective.string("effectiveVersionID")
    guard !version.isEmpty else { return }
    if auditVersion != version {
      auditVersion = version
      auditReader = TrainingAuditReader(plan: plan, version: version)
      audit = .null
      auditNext = 0
      auditError = nil
    }
    await loadMoreAudit()
  }
  func loadMoreAudit() async {
    guard let api, let reader = auditReader, auditLoadingVersion != auditVersion else { return }
    let version = auditVersion
    auditLoadingVersion = version
    auditLoading = true
    defer {
      if auditLoadingVersion == version {
        auditLoadingVersion = ""
        auditLoading = false
      }
    }
    do {
      let page = try await reader.loadMore(using: api)
      guard auditVersion == version else { return }
      audit = .object(["overrides": .array(page.overrides)])
      auditNext = page.next
      auditError = nil
    } catch {
      guard auditVersion == version else { return }
      auditError = "调整历史加载失败，可重试。"
    }
  }
  func command(path: String, fields: [String: JSON], observed: String = "") -> ProductCommand {
    var body = fields
    body["commandID"] = .string(Wire.id())
    return ProductCommand(path: path, body: .object(body), observedEffectiveID: observed)
  }
  @discardableResult func submit(_ proposed: ProductCommand, checkEffective: Bool = false) async
    -> Bool
  {
    guard let api, let journal else { return false }
    guard !loading else { return false }
    // An earlier write the server never acknowledged goes first, on its own;
    // its idempotency key makes the resend safe. Only a conflict stops here.
    if let earlier = pending, earlier != proposed {
      guard !conflict, await submit(earlier) else {
        if !conflict { self.error = "上一次修改还在等待提交，联网后会自动完成。" }
        return false
      }
    }
    let command: ProductCommand
    do { command = try proposed.resolvingPending(pending) } catch {
      self.error = "上一次修改还在等待提交，联网后会自动完成。"
      return false
    }
    loading = true
    error = nil
    conflict = false
    defer { loading = false }
    do {
      if checkEffective && pending == nil {
        let latest = try await api.get("/v2/training/plans/current")
        if latest["effective"].string("effectiveVersionID") != command.observedEffectiveID {
          data.plan = latest
          conflict = true
          error = "训练计划已更新。草稿已保留，请查看最新计划后再次确认。"
          return false
        }
      }
      try await journal.stage(command)
      pending = command
      let start = Date()
      let result = try await api.send(command)
      try await journal.finish(command, result: result)
      pending = nil
      executionIDs = await journal.executionIDs()
      if !result.string("executionID").isEmpty { execution = result }
      if !result.string("effectiveVersionID").isEmpty && result["sessions"] != .null
        && (result.number("version") ?? 0) >= (data.effective.number("version") ?? 0)
      {
        var plan = data.plan.fields
        plan["effective"] = result
        plan["overrideRevision"] = result["overrideRevision"]
        data.plan = .object(plan)
      }
      log.info("command_ack_ms=\(Date().timeIntervalSince(start)*1000,privacy:.public)")
      UIImpactFeedbackGenerator(style: .light).impactOccurred()
      return true
    } catch let e as UploadError {
      conflict = e.status == 409
      if ProductWriteFailure.isDefinitiveRejection(e) {
        do {
          try await journal.discardRejected()
          pending = nil
        } catch {
          self.error = "修改未被接受，请重新打开 App 后再试。"
          return false
        }
        error = "修改未被接受：\(UserFacingText.error(e.code))。请调整后重试。"
      } else {
        error = conflict ? "训练计划已更新。草稿已保留，请查看最新计划后再次确认。" : Self.queuedWriteNotice
        if !conflict { scheduleWriteResume() }
      }
    } catch {
      self.error = Self.queuedWriteNotice
      scheduleWriteResume()
    }
    return false
  }
  static let queuedWriteNotice = "网络不稳定，修改已保存在手机上，联网后会自动提交。"
  private var writeResumeTask: Task<Void, Never>?
  /// Unacknowledged writes are resent by themselves: on every refresh (app
  /// launch and return to foreground) and after a short back-off following a
  /// transient failure. Each command keeps its idempotency key, so a resend
  /// never duplicates. A conflict is never resent; it waits for the user.
  private func scheduleWriteResume() {
    writeResumeTask?.cancel()
    writeResumeTask = Task { [weak self] in
      for delay in [5.0, 20.0, 60.0] {
        try? await Task.sleep(for: .seconds(delay))
        guard !Task.isCancelled, let self else { return }
        if await self.resumePendingWrites() { return }
      }
    }
  }
  /// Returns true when nothing is left to resend.
  @discardableResult func resumePendingWrites() async -> Bool {
    guard api != nil, !loading else { return false }
    if pendingModelInput != nil { _ = await retryModelInput(refreshing: false) }
    if let pending, !conflict { _ = await submit(pending) }
    return pendingModelInput == nil && (pending == nil || conflict)
  }
  func reviewConflict() async {
    guard conflict, let journal, let api else { return }
    do {
      let latest = try await api.get("/v2/training/plans/current")
      if let pending, pending.path.contains("/executions/"),
        !execution.string("executionID").isEmpty
      {
        execution = try await api.get("/v2/training/executions/" + execution.string("executionID"))
      }
      try await journal.discardRejected()
      pending = nil
      data.plan = latest
      conflict = false
      error = nil
    } catch { self.error = "最新计划暂不可用，草稿仍保留。" }
  }
  func override(
    session: String, operation: String, path: String = "", value: JSON = .null,
    manual: JSON = .null, target: String = "", observed: String
  ) async -> Bool {
    let plan = data.plan.string("planID")
    var fields: [String: JSON] = [
      "expectedOverrideRevision": data.plan["overrideRevision"], "overrideID": .string(Wire.id()),
      "sessionID": .string(session), "operation": .string(operation),
    ]
    if !path.isEmpty {
      fields["path"] = .string(path)
      fields["value"] = value
    }
    if operation == "move" {
      fields["path"] = .string("date")
      fields["value"] = value
    }
    if operation == "add" { fields["session"] = manual }
    if operation == "revoke" { fields["targetOverrideID"] = .string(target) }
    let cmd =
      command(
        path: "/v2/training/plans/" + plan + "/overrides", fields: fields, observed: observed)
    return await submit(cmd, checkEffective: true)
  }
  func start(_ session: ProductSession, effective: JSON) async -> Bool {
    await submit(
      command(
        path: "/v2/training/executions",
        fields: [
          "executionID": .string(Wire.id()), "planID": effective["planID"],
          "effectiveVersionID": effective["effectiveVersionID"], "sessionID": .string(session.id),
        ]))
  }
  func changeExecution(_ operation: String, workout: String = "") async {
    let id = execution.string("executionID")
    guard !id.isEmpty else { return }
    var fields: [String: JSON] = [
      "expectedRevision": execution["revision"], "operation": .string(operation),
    ]
    if !workout.isEmpty { fields["workoutID"] = .string(workout) }
    _ = await submit(
      command(path: "/v2/training/executions/" + id + "/changes", fields: fields))
  }
  func refreshExecution() async {
    guard let api else { return }
    do {
      execution = try await api.get("/v2/training/executions/" + execution.string("executionID"))
    } catch { self.error = "训练记录暂不可用" }
  }
  func recalculate() async {
    let p = data.plan
    _ = await submit(
      command(
        path: "/v2/training/plans/" + p.string("planID") + "/changes",
        fields: [
          "operation": .string("recalculate"), "expectedConfigRevision": p["configRevision"],
        ]))
    await refresh()
  }
}

struct VO2SourceOption: Equatable {
  var sourceEpoch: String, bundleIdentifier: String, productType: String, latestDate: String, samples: Int
}
