import Foundation
import ProductCore
import SwiftUI
import SyncCore
import UIKit
import os

@MainActor @Observable
final class ProductModel {
  var data = ProductSnapshot()
  var loading = false
  var configured = false
  var error: String?
  var conflict = false
  var pending: ProductCommand?
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
          let path = ProcessInfo.processInfo.environment["RAI_ACCEPTANCE_CONFIG"]
        {
          let secret = try JSONDecoder().decode(
            ClientConfiguration.Secret.self, from: Data(contentsOf: URL(fileURLWithPath: path)))
          try ClientConfiguration.save(
            baseURL: secret.configuration.baseURL.absoluteString,
            collectorEpoch: secret.configuration.collectorEpoch, token: secret.token)
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
      ).appendingPathComponent("RAIV2Product")
      let store = try ProductJournal(directory: directory)
      try await store.bind(
        secret.configuration.baseURL.absoluteString + ":" + hello.string("serviceID") + ":"
          + hello.string("datasetID") + ":" + hello.string("datasetEpoch"))
      api = productAPI
      journal = store
      configured = true
      pending = await store.pending()
      executionIDs = await store.executionIDs()
      if let id = executionIDs.last {
        execution = (try? await productAPI.get("/v2/training/executions/" + id)) ?? .null
      }
    } catch let failure as UploadError {
      log.error("product_bootstrap_transport=\(failure.code, privacy: .public)")
      error = "服务暂不可用，请在同步与诊断中检查连接。"
    } catch let failure as SyncFailure {
      let code: String
      switch failure {
      case .invalid(let value), .localState(let value): code = value
      case .receiptMismatch: code = "receipt_mismatch"
      }
      log.error("product_bootstrap_failure=\(code, privacy: .public)")
      error = "产品配置暂不可用；原文件已保留。"
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
        error = "处方未被接受（\(e.code)），请修改草稿后重试。"
      } else {
        error = conflict ? "训练计划已更新。草稿已保留，请查看最新计划后再次确认。" : "提交未确认（\(e.code)）；请重试原命令。"
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
