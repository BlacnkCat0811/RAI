import SwiftUI
import UIKit
import SyncCore

@MainActor @Observable
final class SyncModel {
    static let shared = SyncModel()
    var authorization = UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") ? "已请求读取权限；数据是否可用由系统管理" : "尚未请求"
    var status = "空闲"
    var pending = 0
    var parkedAssociations = 0
    var lastACK: Date?
    var lastSync: Date?
    var error: String?
    var busy = false
    #if DEBUG
    var inspectionStatus: String?
    func inspectRunningWorkout() {
        guard !busy else { return }
        busy = true
        inspectionStatus = "正在只读检查跑步结构…"
        task = Task {
            defer { busy = false }
            do {
                _ = try await HealthKitClient().inspectLatestRunningWorkout()
                inspectionStatus = "脱敏结构报告已生成；没有推进同步位置。"
            } catch let failure as SyncFailure {
                switch failure {
                case .invalid(let code), .localState(let code): inspectionStatus = "结构检查失败：\(UserFacingText.error(code))。"
                case .receiptMismatch: inspectionStatus = "结构检查失败。"
                }
            } catch { inspectionStatus = "结构检查失败，错误代码 \((error as NSError).code)。" }
        }
    }
    #endif
    private(set) var transfers: BackgroundTransfers?
    private var coordinator: SyncCoordinator?
    private var store: OutboxStore?
    private var task: Task<Void, Never>?
    private var retryTask: Task<Void, Never>?
    // A manual sync keeps reading HealthKit across rate-limit retries; an
    // automatic foreground drain only uploads what is already staged.
    private var retryReadHealth = false
    init() {
        do {
            let directory = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true).appendingPathComponent("RAIV2Sync", isDirectory: true)
            let store = try OutboxStore(directory: directory)
            self.store = store; self.coordinator = SyncCoordinator(store: store)
            self.transfers = BackgroundTransfers(directory: directory)
        } catch { self.error = "本地状态不可用，现有文件已保留。" }
    }
    func authorize() {
        guard !busy, let coordinator else { return }
        busy = true
        task = Task {
            defer { busy = false }
            do {
                try await coordinator.authorize()
                authorization = "已请求读取权限；数据是否可用由系统管理"
                UserDefaults.standard.set(true, forKey: "healthAuthorizationRequested")
            } catch { self.error = "健康数据授权请求失败。" }
        }
    }
    func start(readHealth: Bool = true) {
        guard !busy else { return }
        retryTask?.cancel(); retryTask = nil
        retryReadHealth = readHealth
        task = Task { await run(readHealth: readHealth) }
    }
    private func apply(_ snapshot: LocalState) {
        // A 409 association_unavailable page waits for its sample stream to
        // catch up. It is durable work, but it is not a fact-sync failure and
        // must not keep the product's "待同步页数" pinned forever.
        parkedAssociations = snapshot.pages.filter { $0.error?.code == "association_unavailable" }.count
        pending = snapshot.pages.count - parkedAssociations
        lastACK = snapshot.lastACK; lastSync = snapshot.lastSync
    }
    func run(readHealth: Bool) async {
        guard !busy, let coordinator, let store, let transfers else { return }
        busy = true; error = nil; status = "同步中"
        // The initial import can now walk the entire quantity backlog in one
        // pass. Keep the screen awake so iOS does not suspend HealthKit reads
        // mid-import; the flag is restored on every exit path.
        UIApplication.shared.isIdleTimerDisabled = true
        let saved = await store.snapshot()
        apply(saved)
        defer {
            busy = false
            UIApplication.shared.isIdleTimerDisabled = false
        }
        do {
            guard let secret = try ClientConfiguration.load() else { throw SyncFailure.invalid("configure_development_api_first") }
            try await coordinator.sync(secret: secret, transfers: transfers, readHealth: readHealth)
            let snapshot = await store.snapshot()
            apply(snapshot)
            let retryablePages = snapshot.pages.filter { $0.error?.retryable == true }
            let retryable = retryablePages.compactMap(\.error)
            if let failure = snapshot.pages.compactMap(\.error).first { error = UserFacingText.error(failure.code) }
            // The workout stream is the gate for the product's newest-run
            // display; show its read failure before unrelated enrichment noise.
            let readFailure = snapshot.readErrors["HKWorkoutTypeIdentifier"]
                ?? snapshot.readErrors.sorted(by: { $0.key < $1.key }).first?.value
            if let readFailure { error = UserFacingText.error(readFailure) }
            if let date = retryablePages.map(\.nextAttempt).min() {
                // The server's device bucket is shared by every page. A short
                // retry here resumes exactly one page at a time instead of
                // immediately re-walking all pending pages.
                let delay = max(1, min(60, date.timeIntervalSinceNow))
                let limited = retryable.contains { $0.code == "device_rate_exceeded" }
                status = limited ? "服务限流，正在按顺序上传 · 剩余 \(pending) 页" : "自动重试中 · 剩余 \(pending) 页"
                retryTask = Task {
                    try? await Task.sleep(for: .seconds(delay))
                    guard !Task.isCancelled else { return }
                    await run(readHealth: retryReadHealth)
                }
            } else {
                if pending == 0 { retryReadHealth = false }
                status = error != nil
                    ? "部分数据流需要处理"
                    : (pending == 0
                        ? (parkedAssociations > 0 ? "运动数据已同步；历史关联后台补全中" : "本轮限量同步已完成")
                        : "仍有已保存的数据待同步")
            }
        } catch let failure as SyncFailure {
            error = UserFacingText.syncFailure(failure); status = "已停止；本地状态已保留" }
        catch let failure as UploadError {
            error = UserFacingText.error(failure.code); status = "等待重试；本地状态已保留"
            if failure.retryable {
                retryTask = Task { try? await Task.sleep(for: .seconds(max(1, min(failure.retryAfter ?? 8, 60)))); guard !Task.isCancelled else { return }; await run(readHealth: retryReadHealth) }
            }
        } catch is CancellationError { status = "已暂停；本地状态已保留" }
        catch { self.error = "健康数据读取或本地编码失败；原同步位置已保留。"; status = "需要检查" }
    }
    func retrySaved() {
        guard !busy, let store else { return }
        Task { do { try await store.retryBlocked(); start() } catch { self.error = "本地重试状态保存失败。" } }
    }
    func suspendForegroundWork() { task?.cancel(); retryTask?.cancel() }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        guard identifier == BackgroundTransfers.identifier, let transfers = SyncModel.shared.transfers else { completionHandler(); return }
        // Reconnect to the stable session immediately. The delegate persists
        // each result before this callback drains/reconciles local receipts.
        transfers.handleEvents {
            Task { @MainActor in
                await SyncModel.shared.run(readHealth: false)
                completionHandler()
            }
        }
    }
}

@main
struct RAIApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) var delegate
    @State private var model = SyncModel.shared
    @Environment(\.scenePhase) private var phase
    var body: some Scene {
        WindowGroup {
            ProductTabs(sync: model)
                .environment(\.locale, Locale(identifier: "zh-Hans-CN"))
                .onChange(of: phase) { _, phase in
                    if phase == .active { model.start(readHealth: false) }
                    else if phase == .background { model.suspendForegroundWork() }
                }
        }
    }
}

struct SyncView: View {
    @Bindable var model: SyncModel
    @State private var apiURL = ""
    @State private var collector = ""
    @State private var token = ""
    @State private var configurationStatus: String?
    var body: some View {
        Form {
            Section("健康数据授权") {
                Text(model.authorization).font(.footnote)
                Button("请求读取权限") { model.authorize() }.disabled(model.busy)
                #if DEBUG
                Button("检查最近跑步结构（脱敏）") { model.inspectRunningWorkout() }.disabled(model.busy)
                if let status = model.inspectionStatus { Text(status).font(.footnote) }
                #endif
            }
            Section("同步") {
                LabeledContent("状态", value: model.status)
                LabeledContent("待同步页数", value: String(model.pending))
                if model.parkedAssociations > 0 {
                    LabeledContent("历史关联待补全", value: "\(model.parkedAssociations) 页（后台自动进行）")
                }
                if let date = model.lastSync { LabeledContent("上次同步") { Text(date, style: .time) } }
                if let date = model.lastACK { LabeledContent("上次确认") { Text(date, style: .time) } }
                if let error = model.error { Text(error).foregroundStyle(.red).font(.footnote) }
                Button("立即同步") { model.start() }.disabled(model.busy)
                Button("重试已保存数据") { model.retrySaved() }.disabled(model.busy)
            }
            Section("开发接口") {
                TextField("服务地址", text: $apiURL).textInputAutocapitalization(.never).autocorrectionDisabled()
                TextField("采集器周期标识", text: $collector).textInputAutocapitalization(.never).autocorrectionDisabled()
                SecureField("设备访问令牌", text: $token).textInputAutocapitalization(.never).autocorrectionDisabled()
                Button("保存到钥匙串") {
                    configurationStatus = nil
                    do {
                        try ClientConfiguration.save(baseURL: apiURL, collectorEpoch: collector, token: token)
                        token = ""; model.error = nil
                        configurationStatus = "已保存到钥匙串。请请求读取权限，然后同步。"
                    } catch let failure as SyncFailure {
                        let code: String
                        switch failure {
                        case .invalid(let value), .localState(let value): code = value
                        case .receiptMismatch: code = "receipt_mismatch"
                        }
                        model.error = "配置未保存：\(UserFacingText.error(code))。"
                    } catch { model.error = "配置未保存：配置编码失败。" }
                }.disabled(model.busy)
                if let configurationStatus { Text(configurationStatus).font(.footnote).foregroundStyle(.green) }
                Text("请使用隔离的数据集。已保存的数据集或采集器身份不能静默更改。").font(.caption)
            }
        }.navigationTitle("同步与诊断")
            .scrollContentBackground(.hidden)
            .raiScreen()
    }
}
