import Foundation
import ProductCore
import SwiftUI
import SyncCore
import UIKit
import os

enum UserFacingText {
  static func error(_ code: String) -> String {
    if code.hasPrefix("keychain_") || code.hasPrefix("session_keychain_") { return "钥匙串读写失败" }
    if code.hasPrefix("unsupported_workout_metadata_types") { return "运动记录包含暂不支持的健康元数据" }
    if code.hasPrefix("unsupported_workout_quantity_types") { return "运动记录包含暂不支持的健康统计类型" }
    return [
      "device_rate_exceeded": "服务限流，正在等待自动重试",
      "dataset_rate_exceeded": "私有服务繁忙，正在等待自动重试",
      "association_unavailable": "关联样本尚未到达，稍后自动重试",
      "analysis_not_ready": "分析尚未就绪",
      "training_not_found": "未找到训练计划",
      "connection_unavailable": "无法连接私有服务",
      "configure_development_api_first": "请先配置私有服务",
      "product_dataset_changed": "私有数据集已更改",
      "dataset_retired": "此同步空间已停用，请使用管理员提供的新凭证建立会话",
      "session_not_active": "此会话尚未启用或已经归档",
      "session_handshake_mismatch": "新凭证与服务身份不匹配，会话未切换",
      "session_identity_reused": "请使用管理员新建的独立身份",
      "legacy_session_identity_reused": "旧身份不能用于新会话，请使用新数据集凭证",
      "session_identity_conflict": "凭证与已保存的会话不一致，原会话已保留",
      "session_switch_busy": "请等待当前同步结束后再切换会话",
      "bundle_session_mismatch": "上传包属于另一会话，原文件已保留",
      "upload_file_session_mismatch": "上传文件属于另一会话，原文件已保留",
      "resolve_pending_command_first": "请先处理待确认的提交",
      "command_identity_mismatch": "提交标识不匹配",
      "invalid_api_origin": "服务地址无效",
      "invalid_collector_epoch": "采集器周期标识无效",
      "missing_device_token": "缺少设备访问令牌",
      "device_token_contains_whitespace": "设备访问令牌不能包含空白",
      "http_requires_configured_debug_host": "调试环境服务地址未配置",
      "https_required": "服务地址必须使用加密连接",
      "source_policy_pending": "来源尚未确认是 Apple Watch 原始采集，原记录待处理",
      "source_policy_excluded": "这条记录不是 Apple Watch 原生跑步，已按来源策略排除，不会上传",
      "metadata_requires_review": "记录包含未审核的元数据，原始数据已留在本机待处理",
      "unsupported_integrity_contract": "服务尚未支持数据完整性协议",
      "workout_core_change_requires_review": "运动原始主体发生变化，需要核对",
      "route_count_incomplete": "路线读取尚未完整，已采集点已保留",
      "route_capture_changed": "路线与本机已保存的点不一致，需要核对",
      "analysis_source_unclassified": "数据来源待确认，尚未进入正式分析",
      "child_workout_mismatch": "删除事件与已有运动关联不一致，需要核对",
      "unsupported_handshake": "服务协议不兼容",
      "capability_or_limit_mismatch": "服务能力或限制不匹配",
      "request_retry_limit": "请求重试次数已用尽",
      "healthkit_unavailable": "设备不支持健康数据",
      "no_visible_running_workout": "没有可读取的跑步记录",
      "invalid_healthkit_anchor": "健康数据同步位置无效",
      "healthkit_read_failed": "健康数据读取失败",
      "workout_not_visible": "HealthKit 中找不到这条跑步记录",
      "workout_identity_conflict": "服务端已有同一条跑步的不同内容，不能覆盖",
      "sample_identity_conflict": "服务端已有同一样本的不同内容，不能覆盖",
      "sample_association_conflict": "该样本已关联到另一条跑步记录",
      "series_identity_conflict": "该样本的明细与已保存记录不一致",
      "route_identity_conflict": "路线与已保存记录不一致",
      "workout_import_not_found": "服务端还没有这条跑步的导入回执",
      "workout_ingest_not_found": "服务端尚未收到这条跑步记录",
      "workout_ingest_conflict": "服务端已有这条跑步的不同接收记录，需要核对",
      "workout_ingest_mismatch": "服务返回的接收结果与本地记录不一致",
      "workout_ingest_rejected": "服务未能解析这条跑步记录，原始数据已保留在本机",
      "workout_ingest_missing_after_acceptance": "服务端确认接收后又找不到这条记录，需要人工核对",
      "workout_ingest_backlog_exceeded": "服务端待入库记录较多，正在等待自动重试",
      "workout_ingest_immutable": "本机已固定的上传文件不能被覆盖",
      "workout_uuid_ingest_required": "服务要求按记录逐条上传，请更新同步",
      "workout_uuid_ingest_paused": "服务暂时停止接收新的跑步记录，稍后自动重试",
      "workout_uuid_ingest_disabled": "服务尚未开启逐条接收，稍后自动重试",
      "unsupported_protocol": "服务协议版本不兼容，请更新应用",
      "payload_corrupt": "本机上传文件已损坏，将重新读取生成",
      "bundle_too_large": "本次上传数据超过安全上限",
      "quantity_series_incomplete": "样本明细读取不完整",
      "sample_deleted": "样本已从 HealthKit 删除",
      "route_deleted": "路线已从 HealthKit 删除",
      "workout_deleted": "这条跑步记录已从 HealthKit 删除",
      "interrupted": "上次同步被中断，可重试该记录",
      "sync_already_running": "已有同步任务在进行",
      "sample_page_limit": "样本分页超过安全上限",
      "association_page_limit": "关联分页超过安全上限",
      "workout_enrichment_mapping_conflict": "运动记录补充信息冲突",
      "workout_total_conflict": "运动总距离或总能量与已保存记录不一致",
      "workout_metadata_conflict": "运动元数据与已保存记录不一致",
      "immutable_workout_changed": "运动记录内容发生变化",
      "workout_enrichment_conflict": "服务端已有不同的补充信息",
      "workout_enrichment_stale": "补充信息版本已过期",
      "unsupported_workout_metadata_types": "运动元数据类型暂不支持",
      "route_read_or_mapping_failed": "路线读取失败",
      "receipt_mismatch": "服务返回结果与请求不一致",
      "invalid_response": "服务返回结果异常",
      "oversized_response": "服务返回结果过大",
      "invalid_http_response": "服务返回了无效响应",
      "invalid_training_override": "训练调整无效",
      "training_request_too_large": "训练修改内容过大",
      "unsupported_media_type": "不支持的数据格式",
      "unsupported_content_encoding": "不支持的数据编码",
      "training_response_requires_pagination": "训练结果需要分页读取",
    ][code] ?? "操作未完成，请稍后重试"
  }

  static func syncFailure(_ failure: SyncFailure) -> String {
    switch failure {
    case .invalid(let code), .localState(let code): return error(code)
    case .receiptMismatch: return "服务返回结果与请求不一致"
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
  private var api: ProductAPI?
  private var journal: ProductJournal?
  private let log = Logger(subsystem: "com.rai.v2.ios", category: "product.performance")
  func configure() async {
    guard api == nil else { return }
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
        error = "请在「我的 → 同步与诊断」配置私有服务。"
        return
      }
      let client = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token)
      let productAPI = ProductAPI(client: client)
      let hello = try await productAPI.get("/v2/handshake")
      let directory = try FileManager.default.url(
        for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true
      ).appendingPathComponent("RAIV2Product").appendingPathComponent(try ClientConfiguration.activeDescriptor()?.sessionID ?? "unconfigured")
      let store = try ProductJournal(directory: directory)
      try await store.bind(
        secret.configuration.baseURL.absoluteString + ":" + hello.string("serviceID") + ":"
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
    } catch let failure as UploadError {
      log.error("product_bootstrap_transport=\(failure.code, privacy: .public)")
      error = "服务暂不可用：\(UserFacingText.error(failure.code))。请在同步与诊断中检查连接。"
    } catch let failure as SyncFailure {
      let code: String
      switch failure {
      case .invalid(let value), .localState(let value): code = value
      case .receiptMismatch: code = "receipt_mismatch"
      }
      log.error("product_bootstrap_failure=\(code, privacy: .public)")
      error = "产品配置暂不可用：\(UserFacingText.error(code))。原文件已保留。"
    } catch {
      log.error("product_bootstrap_local_code=\((error as NSError).code, privacy: .public)")
      self.error = "服务连接或本地命令恢复失败；原文件已保留。"
    }
  }
  func reconnect() async {
    guard !loading else { return }
    api = nil
    configured = false
    await refresh()
  }
  func refresh() async {
    guard !loading else { return }
    loading = true
    defer { loading = false }
    let start = Date()
    await configure()
    guard let api else { return }
    let loaded = await api.load()
    let stateStart = Date()
    data = loaded
    log.info("state_publication_ms=\(Date().timeIntervalSince(stateStart)*1000,privacy:.public)")
    error =
      data.failures.values.contains(where: {
        !["analysis_not_ready", "training_not_found"].contains($0)
      }) ? "部分数据暂不可用，可下拉重试。" : nil
    log.info("home_load_ms=\(Date().timeIntervalSince(start)*1000,privacy:.public)")
  }
  /// Submits one user-entered model input. The server assigns receipt time and
  /// known-at; the app never edits or deletes an accepted input locally.
  func submitModelInput(kind: String, payload: [String: JSON], effectiveFrom: Date) async -> String? {
    guard let journal else { return "请先在「我的 → 同步与诊断」配置私有服务。" }
    let formatter = ISO8601DateFormatter()
    let body = JSON.object([
      "id": .string(UUID().uuidString.lowercased()),
      "kind": .string(kind),
      "subjectKey": .string("athlete"),
      "effectiveFrom": .string(formatter.string(from: effectiveFrom)),
      "provenance": .string("ios_user_entered"),
      "payload": .object(payload),
    ])
    let path: String
    switch kind {
    case "hr_reference": path = "/v2/model-inputs/hr-references"
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
      return "保存前无法记录提交身份，请稍后重试。"
    }
  }
  func retryModelInput() async -> String? {
    guard let api, let journal, let command = pendingModelInput else { return "没有待确认的个人输入。" }
    do {
      let receipt = try await api.sendModelInput(path: command.path, body: command.body)
      guard receipt.string("inputID") == command.body.string("id"),
        !receipt.string("contentSHA256").isEmpty else { throw SyncFailure.receiptMismatch }
      try await journal.finishModelInput(command)
      pendingModelInput = nil
      await refresh()
      return nil
    } catch let failure as UploadError {
      if [404, 409, 413, 422].contains(failure.status) {
        if (try? await journal.finishModelInput(command)) != nil { pendingModelInput = nil }
      }
      return "保存失败：" + UserFacingText.error(failure.code)
    } catch {
      return "提交结果待确认，重试会使用原提交身份。"
    }
  }
  /// Latest append-only TRIMP parameter set, or nil when none was saved yet.
  func loadAnalysisParameters() async -> JSON? {
    guard let api else { return nil }
    guard let response = try? await api.get("/v2/analysis/parameters"),
      case .object = response["parameters"]
    else { return nil }
    return response["parameters"]
  }
  func refreshAthleteAssignment() async {
    guard !loading, let api else { return }
    loading = true
    defer { loading = false }
    do {
      let json = try await api.get("/v2/analysis/athlete")
      let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: json.data())
      if !Task.isCancelled { data.athlete = snapshot }
    } catch let failure as UploadError where failure.code == "not_product_activated" {
      if !Task.isCancelled { data.athlete = nil }
    } catch {
      if !Task.isCancelled { data.athlete = nil }
    }
  }
  func loadAthleteHistory(metric: String) async throws -> AthleteHistoryPage {
    guard let api else { throw SyncFailure.invalid("configure_development_api_first") }
    return try await api.athleteHistory(metric: metric)
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
        diagnosticsError = "模型栈已切换，请刷新。"
        return
      }
      diagnostics = response
      diagnosticsError = nil
    } catch let failure as UploadError where failure.status == 409 {
      os_log("athlete diagnostics not activated: %{public}@", type: .error, failure.code)
      diagnostics = nil
      diagnosticsError = "研究诊断尚未激活。"
    } catch {
      os_log("athlete diagnostics unavailable: %{public}@", type: .error, String(describing: error))
      diagnostics = nil
      diagnosticsError = "研究诊断暂不可用，可下拉重试。"
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
  var diagnostics: AthleteDiagnostics?
  var diagnosticsError: String?
  func loadWorkoutDetail(_ id: String) async {
    guard let api else {
      workoutDetailError = "请先配置私有服务。"
      return
    }
    if workoutDetail?.workout.workoutID != id {
      workoutDetail = nil
      workoutSeries = nil
      workoutFreshness = nil
      workoutDetailError = nil
    }
    workoutDetailLoading = true
    defer { workoutDetailLoading = false }
    do {
      let detail = try await api.workoutDetail(id)
      guard detail.workout.workoutID == id else { throw SyncFailure.invalid("invalid_response") }
      workoutDetail = detail
      workoutDetailError = nil
    } catch let failure as UploadError {
      workoutDetailError = "训练详情暂不可用：\(UserFacingText.error(failure.code))"
      return
    } catch {
      workoutDetailError = "训练详情暂不可用，请稍后重试。"
      return
    }
    workoutSeries = try? await api.workoutSeries(id)
    workoutFreshness = try? await api.workoutFreshness(id)
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
    let command: ProductCommand
    do { command = try proposed.resolvingPending(pending) } catch {
      self.error = "上一次修改结果尚未确认。当前草稿已保留，请先重试原操作或确认服务器状态。"
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
          self.error = "被拒绝的请求仍保留，请重新打开后检查。"
          return false
        }
        error = "训练处方未被接受：\(UserFacingText.error(e.code))。请修改草稿后重试。"
      } else {
        error = conflict ? "训练计划已更新。草稿已保留，请查看最新计划后再次确认。" : "提交结果尚未确认：\(UserFacingText.error(e.code))。请重试原命令。"
      }
    } catch { self.error = "命令已保留，请恢复原提交。" }
    return false
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
  @discardableResult func retryPending() async -> Bool {
    guard let pending else { return false }
    guard await submit(pending) else { return false }
    await refresh()
    return true
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
    } catch { self.error = "执行记录暂不可用" }
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
