import SwiftUI
import UIKit
import SyncCore
import UniformTypeIdentifiers
#if DEBUG
import HealthKit
#endif

/// One Workout's result in the current manual upload round. Only `succeeded`
/// and `failed` are final; intermediate states exist while the batch runs.
struct WorkoutUploadResult: Sendable, Equatable {
    enum State: String, Sendable, Equatable { case pending, uploading, succeeded, failed }
    var state: State
    var acceptedAt: Date?
    var errorCode: String?
    var reason: String?
    var isFinished: Bool { state == .succeeded || state == .failed }
}

/// The exact user-selected set for one foreground round. Its summary counts
/// the whole selection, including records never started after an interruption.
struct WorkoutBatch: Sendable, Equatable {
    let ids: [String]
    let startedAt: Date
    var finishedAt: Date?
    var duration: TimeInterval? { finishedAt.map { $0.timeIntervalSince(startedAt) } }
}

@MainActor @Observable
final class SyncModel {
    static let shared = SyncModel()
    static let workoutPageSize = 100
    var authorization = UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") ? "已请求读取权限；数据是否可用由系统管理" : "尚未请求"
    var status = "空闲"
    var pendingChanges = 0
    /// One line per daily type: backfilling / synced, with the last page time.
    var dailyProgress: [String] = []
    var error: String?
    private var operationBusy = false
    /// Daily health (HRV, resting HR, sleep, VO2): automatic scans and the
    /// manual "同步日常健康" round.
    let syncScheduler: ForegroundSyncScheduler
    /// Workout upload batches. A separate lane, so a new run never waits for a
    /// daily backfill round and a Workout batch never stops daily delivery.
    let workoutScheduler: ForegroundSyncScheduler
    /// Anything in flight. Session switches and authorization wait for this.
    var busy: Bool {
        get { operationBusy || syncScheduler.isRunning || workoutScheduler.isRunning }
        set { operationBusy = newValue }
    }
    /// The Workout list and its selection only wait for Workout work.
    var workoutBusy: Bool { operationBusy || workoutScheduler.isRunning }
    var canStartWorkoutSync: Bool { !operationBusy && workoutScheduler.mode != .manual }
    var canStartDailySync: Bool { !operationBusy && syncScheduler.mode != .manual }
    var workouts: [HealthWorkoutSummary] = []
    var selected: Set<String> = []
    var loadingWorkouts = false
    var loadingMoreWorkouts = false
    var hasMoreWorkouts = false
    var workoutResults: [String: WorkoutUploadResult] = [:]
    var batch: WorkoutBatch?
    var currentWorkoutID: String?
    var workoutsRefreshedAt: Date?
    /// Last server acceptance of a Workout. The per-round results above live in
    /// memory only, so without this a relaunch would claim "尚未同步" even
    /// though the server already holds the record.
    private(set) var lastUploadAt: Date? = UserDefaults.standard.object(forKey: SyncModel.lastUploadKey) as? Date
    private static let lastUploadKey = "workoutLastUploadAt"
    private var workoutCursor: Date?
    private var pageWorkoutIDs: [String] = []
    private var workoutUploadOwner: UUID?
    /// Workouts the server has accepted, persisted per session so the list and
    /// its filters survive a relaunch. `workoutResults` is seeded from this.
    private var acceptedWorkouts: [String: Date] = [:]
    private var acceptedWorkoutsFile: URL?
    private static let acceptedWorkoutsName = "accepted-workouts.json"
    /// Keeps a running Workout upload alive for the short grace period iOS gives
    /// a backgrounded app, instead of cancelling it the moment the app leaves.
    private var backgroundUploadTask: UIBackgroundTaskIdentifier = .invalid
    var canLoadMoreWorkouts: Bool { hasMoreWorkouts && !loadingMoreWorkouts && !workoutBusy && !loadingWorkouts }
    #if DEBUG
    var dailyRDIsolated = false
    var inspectionStatus: String?
    #endif
    private(set) var activeSessionID: String?
    var sessions: SyncSessionStore?
    var transferRegistry: [String: BackgroundTransfers] = [:]
    private(set) var transfers: BackgroundTransfers?
    var coordinator: SyncCoordinator?
    var store: OutboxStore?
    var task: Task<Void, Never>?
    init(syncScheduler: ForegroundSyncScheduler = ForegroundSyncScheduler(),
         workoutScheduler: ForegroundSyncScheduler = ForegroundSyncScheduler()) {
        self.syncScheduler = syncScheduler
        self.workoutScheduler = workoutScheduler
        #if DEBUG
        if ProcessInfo.processInfo.environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" { return }
        #endif
        do {
            let directory = try ClientConfiguration.sessionRoot()
            let sessions = try SyncSessionStore(root: directory, credentials: SessionKeychain())
            self.sessions = sessions
            if let descriptor = try ClientConfiguration.activeDescriptor() {
                try install(descriptor)
            } else {
                // Preserve the pre-session Outbox at its original path. It is never uploaded or discovered.
                self.store = try OutboxStore(directory: directory)
            }
            #if DEBUG
            // Device-acceptance bootstrap only. The value is supplied through
            // the launch environment by the local acceptance runner and is
            // never written to logs, source, build settings or evidence.
            if let raw = ProcessInfo.processInfo.environment["RAI_ACCEPTANCE_CONFIG_JSON"],
               let data = raw.data(using: .utf8),
               let value = try? JSONDecoder().decode(AcceptanceConfig.self, from: data) {
                Task {
                    await waitUntilIdle()
                    try? await bindNewSession(baseURL: value.baseURL, collectorEpoch: value.collectorEpoch, token: value.token)
                }
            }
            #endif
        } catch { self.error = "本地状态不可用，现有文件已保留。" }
    }
    private func install(_ descriptor: SyncSessionDescriptor) throws {
        guard let sessions else { throw SyncFailure.localState("session_store_unavailable") }
        ClientConfiguration.invalidateCache()
        let directory = try SyncSessionStore.directory(root: sessions.root, sessionID: descriptor.sessionID)
        let store = try OutboxStore(directory: directory)
        let transfers = transferRegistry[descriptor.backgroundIdentifier] ?? BackgroundTransfers(directory: directory, identifier: descriptor.backgroundIdentifier, sessionID: descriptor.sessionID)
        transferRegistry[descriptor.backgroundIdentifier] = transfers
        self.store = store; self.transfers = transfers
        self.coordinator = SyncCoordinator(store: store, sessionStore: sessions, sessionID: descriptor.sessionID)
        activeSessionID = descriptor.sessionID
        acceptedWorkoutsFile = directory.appendingPathComponent(Self.acceptedWorkoutsName)
        acceptedWorkouts = Self.readAcceptedWorkouts(acceptedWorkoutsFile)
        workouts = []; selected = []; batch = nil; currentWorkoutID = nil
        workoutResults = acceptedWorkouts.mapValues { WorkoutUploadResult(state: .succeeded, acceptedAt: $0) }
        workoutCursor = nil; pageWorkoutIDs = []; hasMoreWorkouts = false; pendingChanges = 0
    }
    func bindNewSession(baseURL: String, collectorEpoch: String, token: String) async throws {
        guard !busy, !loadingWorkouts, let sessions else { throw SyncFailure.localState("session_switch_busy") }
        busy = true; defer { busy = false }
        recoveryWake?.cancel()
        let secret = try ClientConfiguration.validatedSecret(baseURL: baseURL, collectorEpoch: collectorEpoch, token: token)
        let api = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token)
        // Probe supplies operator-enrolled identity; it never creates a dataset.
        let hello = try await api.call("GET", "/v2/handshake")
        guard let dataset = hello["datasetID"].text, let epoch = hello["datasetEpoch"].text,
              let service = hello["serviceID"].text, let device = hello["deviceID"].text,
              hello["collectorEpoch"].text == secret.configuration.collectorEpoch else { throw SyncFailure.invalid("session_handshake_mismatch") }
        if let legacy = try ClientConfiguration.loadLegacy(), legacy.token == secret.token || legacy.configuration.collectorEpoch == secret.configuration.collectorEpoch {
            throw SyncFailure.invalid("legacy_session_identity_reused")
        }
        let manifest = try await sessions.snapshot()
        let existing = manifest.sessions.values.first { $0.datasetID == dataset && $0.deviceID == device }
        let descriptor = existing ?? SyncSessionDescriptor(datasetID: dataset, serviceID: service, datasetEpoch: epoch, deviceID: device,
            collectorEpoch: secret.configuration.collectorEpoch, baseURL: secret.configuration.baseURL)
        if existing == nil { _ = try await sessions.prepare(descriptor, token: token) }
        else {
            guard descriptor.baseURL == secret.configuration.baseURL, try SessionKeychain().load(sessionID: descriptor.sessionID) == secret.token else { throw SyncFailure.invalid("session_identity_conflict") }
        }
        try await sessions.activate(descriptor.sessionID, handshake: hello)
        try install(try await sessions.requireActive(descriptor.sessionID))
        apply(await store!.snapshot()); status = "已连接，等待同步"
    }
    /// A `rai://connect?url=…&epoch=…&token=…` link only proposes a server; the
    /// user confirms it by name before anything is stored or contacted.
    struct PendingConnection: Equatable {
        let baseURL: String, collectorEpoch: String, token: String
        var host: String { URL(string: baseURL)?.host ?? baseURL }
    }
    var pendingConnection: PendingConnection?
    func receiveConnectLink(_ link: URL) {
        guard link.scheme == "rai", link.host == "connect",
              let items = URLComponents(url: link, resolvingAgainstBaseURL: false)?.queryItems else { return }
        func value(_ name: String) -> String { items.first { $0.name == name }?.value ?? "" }
        guard (try? ClientConfiguration.validatedSecret(baseURL: value("url"), collectorEpoch: value("epoch"), token: value("token"))) != nil else {
            connectionNotice = "连接链接无效，请重新生成。"; return
        }
        pendingConnection = PendingConnection(baseURL: value("url"), collectorEpoch: value("epoch"), token: value("token"))
    }
    /// Result of the last connection attempt, shown as an alert wherever the
    /// user happens to be: a link can arrive on any screen.
    var connectionNotice: String?
    /// A foreground scan started at launch holds `busy` for a moment; a
    /// connection confirmed right then waits for it instead of failing.
    func waitUntilIdle() async {
        for _ in 0..<100 where busy || loadingWorkouts {
            try? await Task.sleep(for: .milliseconds(100))
        }
    }
    func confirmPendingConnection() async {
        guard let pending = pendingConnection else { return }
        pendingConnection = nil
        await waitUntilIdle()
        do {
            try await bindNewSession(baseURL: pending.baseURL, collectorEpoch: pending.collectorEpoch, token: pending.token)
            error = nil
            status = "已连接到 \(pending.host)"
            connectionNotice = UserDefaults.standard.bool(forKey: "healthAuthorizationRequested")
                ? "已连接到 \(pending.host)。"
                : "已连接到 \(pending.host)。下一步：在「我的 → 服务器与健康授权」中授权读取健康数据。"
        } catch let failure as SyncFailure {
            connectionNotice = "连接未保存：" + UserFacingText.syncFailure(failure) + "。"
        } catch { connectionNotice = "连接未保存：无法访问服务器，或凭证与服务器不匹配。" }
    }
    func importRecoverySession(_ data: Data) async throws {
        guard !busy, !loadingWorkouts, let sessions else { throw SyncFailure.localState("session_switch_busy") }
        let package = try JSONDecoder().decode(RecoveryCredentialPackage.self, from: data)
        let descriptor = try package.descriptor()
        _ = try ClientConfiguration.validatedSecret(baseURL: package.baseURL, collectorEpoch: descriptor.collectorEpoch, token: package.enrollment.token)
        busy = true; defer { busy = false }
        recoveryWake?.cancel()
        _ = try await sessions.prepare(descriptor, token: package.enrollment.token, recovery: true)
        activeSessionID = nil; coordinator = nil; transfers = nil
        let hello = try await RAIAPIClient(baseURL: descriptor.baseURL, token: package.enrollment.token).call("GET", "/v2/handshake")
        try await sessions.activate(descriptor.sessionID, handshake: hello)
        try install(try await sessions.requireActive(descriptor.sessionID))
        apply(await store!.snapshot()); status = "恢复会话已启用；将从当前可见健康历史重新发现"
    }
    /// Both lanes call this before they touch the session; the second caller
    /// waits for the first instead of activating the same session twice.
    private var sessionResume: Task<Void, any Error>?
    private func resumePreparedSession() async throws {
        if let running = sessionResume { try await running.value; return }
        let task = Task { try await self.activatePreparedSession() }
        sessionResume = task
        defer { sessionResume = nil }
        try await task.value
    }
    private func activatePreparedSession() async throws {
        guard let sessions else { return }
        let manifest = try await sessions.snapshot()
        let prepared = manifest.sessions.values.filter { $0.status == .prepared }
        guard prepared.count == 1, let descriptor = prepared.first,
              let secret = try ClientConfiguration.load(sessionID: descriptor.sessionID) else { return }
        let hello = try await RAIAPIClient(baseURL: descriptor.baseURL, token: secret.token).call("GET", "/v2/handshake")
        try await sessions.activate(descriptor.sessionID, handshake: hello)
        try install(try await sessions.requireActive(descriptor.sessionID))
    }
    func backgroundTransfers(identifier: String) async -> BackgroundTransfers? {
        if let transfer = transferRegistry[identifier] { return transfer }
        guard let sessions else { return nil }
        if identifier == BackgroundTransfers.identifier {
            // The fixed legacy identifier always resolves to the original directory.
            let transfer = BackgroundTransfers(directory: sessions.root)
            transferRegistry[identifier] = transfer; return transfer
        }
        guard let descriptor = try? await sessions.resolveBackground(identifier),
              let directory = try? SyncSessionStore.directory(root: sessions.root, sessionID: descriptor.sessionID) else { return nil }
        let transfer = BackgroundTransfers(directory: directory, identifier: identifier, sessionID: descriptor.sessionID)
        transferRegistry[identifier] = transfer; return transfer
    }
    func reconcileWorkoutTransfers(identifier: String? = nil) async {
        guard let coordinator, let transfers,
              identifier == nil || identifier == transfersIdentifier else { return }
        let owner = activeSessionID
        guard let outcomes = try? await coordinator.reconcileWorkoutUploads(transfers: transfers),
              activeSessionID == owner else { return }
        for outcome in outcomes {
            if let accepted = outcome.acceptedAt, acceptedWorkouts[outcome.workoutID].map({ $0 >= accepted }) == true { continue }
            applyUploadOutcome(outcome)
        }
    }
    private var transfersIdentifier: String? {
        transferRegistry.first { $0.value === transfers }?.key
    }
    func authorize() {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled, !dailyRDIsolated { status = "请先完成独立验收会话连接"; return }
        #endif
        guard !busy, let coordinator else { return }
        busy = true
        task = Task {
            defer { busy = false }
            do {
                try await coordinator.authorize()
                authorization = "已请求读取权限；数据是否可用由系统管理"
                UserDefaults.standard.set(true, forKey: "healthAuthorizationRequested")
                // Observers only mark local discovery work. They never start an
                // upload; a Workout is sent only by the explicit sync button.
                await installObservers()
            } catch { self.error = "健康数据授权请求失败。" }
        }
    }
    func loadWorkouts() async {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled { return }
        #endif
        guard !loadingWorkouts, !workoutBusy, let coordinator else { return }
        loadingWorkouts = true; defer { loadingWorkouts = false }
        do {
            if let store { apply(await store.snapshot()) }
            let page = try await coordinator.workoutPage(before: nil, limit: Self.workoutPageSize)
            workouts = page
            workoutCursor = page.last?.start
            pageWorkoutIDs = page.map(\.id)
            hasMoreWorkouts = page.count >= Self.workoutPageSize
            selected = selected.intersection(Set(page.map(\.id)))
            workoutsRefreshedAt = Date()
            // One request settles every row (also after a reinstall or a new phone).
            let visible = page.map(\.id)
            Task { await refreshRemoteStatuses(visible) }
            if !workoutBusy {
                if workouts.isEmpty { status = "健康 App 中还没有可读取的跑步记录" }
                error = nil
            }
        } catch let failure as SyncFailure {
            error = UserFacingText.syncFailure(failure)
        } catch {
            self.error = "健康记录列表读取失败，请检查授权。"
        }
    }
    /// Appends one older page without touching the current selection. The
    /// cursor is the last loaded start date, so pages never overlap.
    func loadMoreWorkouts() async {
        guard canLoadMoreWorkouts, let coordinator else { return }
        loadingMoreWorkouts = true; defer { loadingMoreWorkouts = false }
        do {
            let page = try await coordinator.workoutPage(before: workoutCursor, limit: Self.workoutPageSize)
            let loaded = Set(workouts.map(\.id))
            workouts.append(contentsOf: page.filter { !loaded.contains($0.id) })
            let advanced = page.last.map { $0.start != workoutCursor } ?? false
            if let last = page.last?.start { workoutCursor = last }
            pageWorkoutIDs = page.map(\.id)
            hasMoreWorkouts = page.count >= Self.workoutPageSize && advanced
            error = nil
        } catch let failure as SyncFailure {
            error = UserFacingText.syncFailure(failure)
        } catch {
            self.error = "更早的健康记录读取失败，请稍后重试。"
        }
    }
    /// Reads the whole visible history page by page, then uploads every eligible
    /// record the server does not have yet (or rejected), oldest last.
    func backfillUnsyncedWorkouts() async {
        guard !loadingWorkouts, !loadingMoreWorkouts, !workoutBusy, let coordinator else { return }
        loadingMoreWorkouts = true
        do {
            while hasMoreWorkouts {
                let page = try await coordinator.workoutPage(before: workoutCursor, limit: Self.workoutPageSize)
                let loaded = Set(workouts.map(\.id))
                workouts.append(contentsOf: page.filter { !loaded.contains($0.id) })
                guard let last = page.last?.start, last != workoutCursor, page.count >= Self.workoutPageSize else {
                    hasMoreWorkouts = false
                    break
                }
                workoutCursor = last
            }
            error = nil
        } catch let failure as SyncFailure {
            loadingMoreWorkouts = false
            error = UserFacingText.syncFailure(failure); return
        } catch {
            loadingMoreWorkouts = false
            self.error = "未能读取全部历史记录。"; return
        }
        loadingMoreWorkouts = false
        await refreshRemoteStatuses(workouts.filter(\.isEligible).map(\.id))
        let ids = workouts.filter { $0.isEligible && workoutResults[$0.id]?.state != .succeeded }.map(\.id)
        guard !ids.isEmpty else { status = "所有符合条件的记录都已同步"; return }
        syncWorkouts(ids)
    }
    func syncWorkouts(_ ids: [String]) {
        guard canStartWorkoutSync else { return }
        let eligible = Set(workouts.filter(\.isEligible).map(\.id))
        var seen = Set<String>(), ordered: [String] = []
        for id in ids where eligible.contains(id) && seen.insert(id).inserted { ordered.append(id) }
        guard !ordered.isEmpty else { return }
        startBatch(ordered)
    }
    private func startBatch(_ ordered: [String]) {
        // The token only lets a stale round ignore progress that is not its own.
        let owner = UUID()
        // Reserve the selection before the round starts, so even a page closed
        // right away gets final results. Daily delivery keeps running on its
        // own lane and its continuation timer is left alone.
        workoutUploadOwner = owner
        batch = WorkoutBatch(ids: ordered, startedAt: Date())
        for id in ordered { workoutResults[id] = WorkoutUploadResult(state: .pending) }
        workoutScheduler.startManual { await self.runWorkoutBatch(ordered, owner: owner) }
    }
    func cancelWorkoutSync() {
        guard workoutUploadOwner != nil else { return }
        workoutScheduler.cancel()
        if let batch { finishBatch(batch.ids, interrupted: true) }
        currentWorkoutID = nil
        workoutUploadOwner = nil
    }
    /// Cached negotiation, then bounded file builds and background uploads.
    /// A per-record failure does not stop the batch. Any selected
    /// record without a final outcome after cancellation counts as failed.
    private func runWorkoutBatch(_ ids: [String], owner: UUID) async {
        let round = batch ?? WorkoutBatch(ids: ids, startedAt: Date())
        defer {
            if workoutUploadOwner == owner { workoutUploadOwner = nil }
            endBackgroundUploadTask()
        }
        error = nil; currentWorkoutID = nil
        var interrupted = false
        do {
            try Task.checkCancellation()
            // A prepared session install resets per-round UI state, so it must
            // happen before this round's results are initialized.
            try await resumePreparedSession()
            guard !Task.isCancelled else { throw CancellationError() }
            if batch == nil { batch = round }
            for id in ids { workoutResults[id] = WorkoutUploadResult(state: .pending) }
            UIApplication.shared.isIdleTimerDisabled = true
            defer {
                currentWorkoutID = nil
                UIApplication.shared.isIdleTimerDisabled = false
            }
            guard let coordinator, let transfers else {
                markBatchFailed(ids, code: "sync_unavailable", reason: "同步尚未准备完成。")
                finishBatch(ids, interrupted: false)
                return
            }
            guard let secret = try? ClientConfiguration.load() else {
                markBatchFailed(ids, code: "configure_development_api_first",
                                reason: UserFacingText.error("configure_development_api_first"))
                finishBatch(ids, interrupted: false)
                return
            }
            let total = ids.count
            status = total > 1 ? "正在上传 0/\(total)" : "正在上传跑步记录"
            _ = try await coordinator.uploadWorkouts(ids, secret: secret, transfers: transfers,
                started: { [weak self] id in
                    guard let self, self.workoutUploadOwner == owner else { return }
                    self.markUploading(id)
                },
                progress: { [weak self] outcome in
                    guard let self, self.workoutUploadOwner == owner else { return }
                    self.applyUploadOutcome(outcome)
                })
        } catch is CancellationError {
            interrupted = true
        } catch {
            let code = Self.uploadCode(error)
            markBatchFailed(ids, code: code, reason: UserFacingText.error(code))
        }
        if batch == nil { batch = round }
        finishBatch(ids, interrupted: interrupted || Task.isCancelled)
        scheduleStatusChecks(ids.filter { workoutResults[$0]?.state == .succeeded })
        if let store { apply(await store.snapshot()) }
    }
    private func markUploading(_ id: String) {
        currentWorkoutID = id
        workoutResults[id] = WorkoutUploadResult(state: .uploading)
        if let batch {
            let done = batch.ids.filter { workoutResults[$0]?.isFinished == true }.count
            status = "正在上传 \(min(done + 1, batch.ids.count))/\(batch.ids.count)"
        }
    }
    private func applyUploadOutcome(_ outcome: WorkoutUploadOutcome) {
        if currentWorkoutID == outcome.workoutID {
            currentWorkoutID = batch?.ids.first { $0 != outcome.workoutID && workoutResults[$0]?.state == .uploading }
        }
        if outcome.succeeded {
            workoutResults[outcome.workoutID] = WorkoutUploadResult(state: .succeeded, acceptedAt: outcome.acceptedAt)
            if acceptedWorkouts[outcome.workoutID] != nil { repeatedWorkouts.insert(outcome.workoutID) }
            recordUpload(at: outcome.acceptedAt ?? Date())
            recordAcceptedWorkout(outcome.workoutID, at: outcome.acceptedAt ?? Date())
        } else {
            let code = outcome.errorCode ?? "sync_failed"
            workoutResults[outcome.workoutID] = WorkoutUploadResult(state: .failed, errorCode: code,
                                                                   reason: UserFacingText.error(code))
        }
    }
    /// Persists the newest server acceptance so the home badge still reads
    /// "已上传" after a relaunch. Never moves backwards: a replay of an older
    /// receipt keeps the later time.
    private func recordUpload(at date: Date) {
        if let current = lastUploadAt, current >= date { return }
        lastUploadAt = date
        UserDefaults.standard.set(date, forKey: Self.lastUploadKey)
    }
    private func recordAcceptedWorkout(_ id: String, at date: Date) {
        acceptedWorkouts[id] = date
        remoteRejected[id] = nil
        // Best effort: losing this file only makes old rows read "未上传" again.
        persistAcceptedWorkouts()
    }
    private static func readAcceptedWorkouts(_ file: URL?) -> [String: Date] {
        guard let file, let data = try? Data(contentsOf: file) else { return [:] }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return (try? decoder.decode([String: Date].self, from: data)) ?? [:]
    }
    // MARK: Server status and automatic sync

    /// Server-side parse failures by Workout, refreshed from the batch status
    /// endpoint. Automatic sync never resends these by itself.
    private var remoteRejected: [String: String] = [:]
    private var autoSyncRunning = false
    private var lastAutoSyncAt: Date?
    private var workoutObserverInstalled = false
    private let autoPolicy = WorkoutAutoSyncPolicy()
    /// One-shot timer for a run that is still settling or waiting for its
    /// second copy; without it the next look would be the 15-minute scan.
    private var workoutRecheck: Task<Void, Never>?
    /// Workouts sent again after an earlier acceptance. The server replays the
    /// original receipt (and its time) for identical bytes, so the acceptance
    /// time alone cannot tell that the second copy has already gone out.
    private var repeatedWorkouts: Set<String> = []

    /// Folds one batch of server answers into the local picture. The server is
    /// authoritative: a record it does not know is not "uploaded", whatever the
    /// local file says, and a record it rejected is shown as retryable.
    func applyRemoteStatuses(_ remote: [String: WorkoutRemoteStatus]) {
        var changed = false
        for (id, status) in remote {
            switch status.state {
            case .accepted, .committed, .alreadyImported:
                remoteRejected[id] = nil
                let at = status.acceptedAt ?? acceptedWorkouts[id] ?? Date()
                if acceptedWorkouts[id] != at { acceptedWorkouts[id] = at; changed = true }
                if workoutResults[id]?.state != .uploading, workoutResults[id]?.state != .pending {
                    workoutResults[id] = WorkoutUploadResult(state: .succeeded, acceptedAt: at)
                }
            case .rejected:
                let code = status.errorCode ?? "workout_ingest_rejected"
                remoteRejected[id] = code
                if acceptedWorkouts.removeValue(forKey: id) != nil { changed = true }
                if workoutResults[id]?.state != .uploading, workoutResults[id]?.state != .pending {
                    workoutResults[id] = WorkoutUploadResult(state: .failed, errorCode: code,
                                                            reason: "服务端未能解析这条记录，可重新上传。")
                }
            case .notFound:
                remoteRejected[id] = nil
                if acceptedWorkouts.removeValue(forKey: id) != nil { changed = true }
                if workoutResults[id]?.state == .succeeded { workoutResults[id] = nil }
            case .deleted:
                break
            }
        }
        if changed { persistAcceptedWorkouts() }
    }
    /// One request for the whole visible list; failures are silent because the
    /// local cache is still a fair guess.
    func refreshRemoteStatuses(_ ids: [String]) async {
        guard !ids.isEmpty, let coordinator, let transfers, let secret = try? ClientConfiguration.load() else { return }
        if let remote = try? await coordinator.workoutStatuses(ids, secret: secret, transfers: transfers) {
            applyRemoteStatuses(remote)
        }
    }
    /// The server parses an upload after it answers, so a rejection only shows
    /// up a little later. Look again twice to surface it.
    private func scheduleStatusChecks(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        Task { [weak self] in
            for delay in [20, 90] {
                try? await Task.sleep(for: .seconds(delay))
                await self?.refreshRemoteStatuses(ids)
            }
        }
    }
    /// Sends the newest unsent Workouts without a tap: on launch, on return to
    /// the foreground and when HealthKit reports a new Workout. Old history is
    /// left to the manual backfill.
    ///
    /// `scheduled` marks the model's own timed re-check, which is exempt from
    /// the one-minute throttle that absorbs bursts of observer callbacks.
    func autoSyncWorkouts(fromBackground: Bool = false, scheduled: Bool = false) async {
        guard !autoSyncRunning, canStartWorkoutSync, let coordinator, let transfers,
              UserDefaults.standard.bool(forKey: "healthAuthorizationRequested"),
              let secret = try? ClientConfiguration.load() else { return }
        if !scheduled, let last = lastAutoSyncAt, Date().timeIntervalSince(last) < 60 { return }
        lastAutoSyncAt = Date()
        autoSyncRunning = true; defer { autoSyncRunning = false }
        if fromBackground { beginBackgroundUploadTask() }
        do {
            let page = try await coordinator.workoutPage(before: nil, limit: 30)
            let eligible = page.filter(\.isEligible)
            guard !eligible.isEmpty else { endBackgroundIfIdle(); return }
            let remote = try await coordinator.workoutStatuses(eligible.map(\.id), secret: secret, transfers: transfers)
            applyRemoteStatuses(remote)
            // A background wake cannot wait: it sends what HealthKit holds now
            // and leaves late data to the second copy.
            var policy = autoPolicy
            if fromBackground { policy.settle = 0 }
            let candidates = eligible.filter { !repeatedWorkouts.contains($0.id) }
                .map { WorkoutAutoSyncPolicy.Candidate(id: $0.id, end: $0.end) }
            let rejected = Set(remoteRejected.keys), now = Date()
            var due = policy.due(candidates: candidates, accepted: acceptedWorkouts, rejected: rejected, now: now)
            // The system only grants a short window to a background wake.
            if fromBackground { due = Array(due.prefix(2)) }
            // Everything in `due` is accepted (or failed) by the time the timer
            // could matter, so the next look is for what is not due yet.
            var expected = acceptedWorkouts
            for id in due { expected[id] = now }
            scheduleWorkoutRecheck(policy.nextCheck(candidates: candidates, accepted: expected, rejected: rejected, now: now))
            guard !due.isEmpty, canStartWorkoutSync else { endBackgroundIfIdle(); return }
            startBatch(due)
        } catch {
            endBackgroundIfIdle()
        }
    }
    /// Looks again when the next run has settled or its second copy is due.
    /// The timer only lives while the app runs; a later launch, foreground
    /// return or observer wake covers the rest.
    private func scheduleWorkoutRecheck(_ date: Date?) {
        workoutRecheck?.cancel()
        guard let date else { workoutRecheck = nil; return }
        workoutRecheck = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(max(1, date.timeIntervalSinceNow) + 1)) }
            catch { return }
            await self?.workoutScheduler.waitForCompletion()
            await self?.autoSyncWorkouts(scheduled: true)
        }
    }
    private func endBackgroundIfIdle() {
        if workoutUploadOwner == nil, UIApplication.shared.applicationState != .active { endBackgroundUploadTask() }
    }
    /// HKObserverQuery registrations do not survive a relaunch, so they are
    /// re-created whenever read access has been requested.
    private func installObservers() async {
        guard let coordinator, UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") else { return }
        try? await coordinator.observeDaily { [weak self] in await self?.dailyWake() }
        guard !workoutObserverInstalled else { return }
        do {
            try await coordinator.observeWorkouts { [weak self] in
                await self?.autoSyncWorkouts(fromBackground: UIApplication.shared.applicationState != .active)
            }
            workoutObserverInstalled = true
        } catch { workoutObserverInstalled = false }
    }
    private func persistAcceptedWorkouts() {
        guard let file = acceptedWorkoutsFile else { return }
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        if let data = try? encoder.encode(acceptedWorkouts) { try? OutboxStore.write(data, to: file) }
    }
    private func beginBackgroundUploadTask() {
        guard backgroundUploadTask == .invalid else { return }
        backgroundUploadTask = UIApplication.shared.beginBackgroundTask(withName: "rai.workout-upload") { [weak self] in
            // Time is up: stop cleanly. Unfinished records are marked interrupted
            // and can be sent again from the list.
            MainActor.assumeIsolated {
                self?.cancelWorkoutSync()
                self?.endBackgroundUploadTask()
            }
        }
    }
    private func endBackgroundUploadTask() {
        guard backgroundUploadTask != .invalid else { return }
        UIApplication.shared.endBackgroundTask(backgroundUploadTask)
        backgroundUploadTask = .invalid
    }
    private func markBatchFailed(_ ids: [String], code: String, reason: String) {
        for id in ids where workoutResults[id]?.isFinished != true {
            workoutResults[id] = WorkoutUploadResult(state: .failed, errorCode: code, reason: reason)
        }
    }
    /// Counts the whole selection. Records never started because the round was
    /// interrupted are failures with the fixed interruption reason.
    private func finishBatch(_ ids: [String], interrupted: Bool) {
        markBatchFailed(ids, code: "interrupted", reason: "本次上传已中断")
        let succeeded = ids.filter { workoutResults[$0]?.state == .succeeded }.count
        let failed = ids.count - succeeded
        batch?.finishedAt = Date()
        status = functionalStatus(succeeded: succeeded, failed: failed, interrupted: interrupted)
        selected = Set(ids.filter { workoutResults[$0]?.state != .succeeded })
        error = failed > 0 ? "本次上传成功 \(succeeded) 条，失败 \(failed) 条；失败记录已保持选中，可再次点击同步。" : nil
    }
    private func functionalStatus(succeeded: Int, failed: Int, interrupted: Bool) -> String {
        let summary = "上传成功 \(succeeded) 条，上传失败 \(failed) 条"
        return interrupted ? "本次上传已中断；" + summary : summary
    }
    private static func uploadCode(_ error: any Error) -> String {
        if let failure = error as? SyncFailure {
            switch failure {
            case .invalid(let code), .localState(let code): return UserFacingText.stableCode(code)
            case .receiptMismatch: return "receipt_mismatch"
            }
        }
        if let failure = error as? UploadError { return UserFacingText.stableCode(failure.code) }
        return "sync_failed"
    }
    func apply(_ snapshot: LocalState) {
        // Workout files/receipts have their own journal; this badge counts the
        // daily pages in the Outbox.
        pendingChanges = (snapshot.daily ?? [:]).values.filter { $0.pending != nil }.count
        dailyProgress = Self.dailyProgress(snapshot)
    }
    static func dailyProgress(_ snapshot: LocalState) -> [String] {
        let names = [DailyHealth.hrv: "心率变异性", DailyHealth.rhr: "静息心率", DailyHealth.sleep: "睡眠"]
        let time = Date.FormatStyle(date: .omitted, time: .shortened)
        return DailyHealth.types.compactMap { type in
            guard let flow = snapshot.daily?[type], let name = names[type] else { return nil }
            let last = flow.lastSync.map { "，最近一页 " + $0.formatted(time) } ?? ""
            if flow.pending == nil && !flow.hasMore && flow.bootstrapComplete && flow.error == nil { return name + "：已同步" + last }
            var line = name + "：" + (flow.bootstrapComplete ? "等待交付" : "历史补传中") + last
            if let code = flow.error {
                line += DailyHealth.isTransient(code) ? "（稍后自动继续）" : "（" + UserFacingText.error(code) + "）"
            }
            return line
        }
    }
    /// Background wakes (HealthKit observers) get a short round; the
    /// foreground keeps going longer. Pages left over resume at nextAttempt.
    private var dailyRoundBudget: TimeInterval { UIApplication.shared.applicationState == .active ? 60 : 20 }
    /// Foreground discovery timer. It never starts an upload.
    var recoveryWake: Task<Void, Never>?
    func syncDailyNow() {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled, !dailyRDIsolated { status = "请先完成独立验收会话连接"; return }
        #endif
        guard canStartDailySync else { return }
        syncScheduler.startManual {
            guard let coordinator = self.coordinator, let store = self.store, let transfers = self.transfers else { return }
            do {
                guard UserDefaults.standard.bool(forKey: "healthAuthorizationRequested"), let secret = try ClientConfiguration.load() else { self.error = "请先配置同步并请求健康数据读取权限。"; return }
                self.error = nil
                try await store.markDailyWake()
                try await coordinator.discoverDaily()
                try await coordinator.syncDaily(secret: secret, transfers: transfers)
                self.apply(await store.snapshot())
                self.status = self.pendingChanges > 0 ? "日常健康记录仍待交付；请稍后再次点击同步" : "日常健康同步完成"
                if self.pendingChanges > 0 { self.error = "日常健康同步尚未完成；可稍后再次点击同步。" }
            } catch is CancellationError { self.status = "已暂停；同步位置已保留" }
            catch { self.error = "日常健康同步尚未完成；请稍后再次点击同步。" }
            self.apply(await store.snapshot())
        }
    }
    /// Launch, foreground return and HealthKit observer callbacks scan local
    /// changes and then deliver pending daily health (HRV, resting HR, sleep),
    /// so recovery and readiness stay current without a manual sync.
    func scanHealthChanges(newWake: Bool = false) {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled {
            // Same scheduler and Daily state machine, scoped to the verified
            // test identity. Acceptance must not discover unrelated Workouts.
            guard dailyRDIsolated, !busy else { return }
            syncScheduler.startAutomatic {
                guard let coordinator = self.coordinator, let store = self.store else { return }
                do {
                    if newWake { try await store.markDailyWake() }
                    try await DailyDeviceDiagnostics.shared.event("foreground_daily_opportunity", fields: ["newWake": .bool(newWake)])
                    try await coordinator.discoverDaily()
                } catch is CancellationError { return }
                catch { self.error = "日常健康变化扫描尚未完成；同步位置已保留。" }
                self.apply(await store.snapshot())
                try? await DailyDeviceDiagnostics.shared.snapshot("foreground_opportunity_completed", store: store)
            }
            return
        }
        #endif
        guard !operationBusy else { return }
        syncScheduler.startAutomatic {
            if newWake, let store = self.store { try? await store.markDailyWake() }
            await self.performForegroundScan()
        }
    }
    private func performForegroundScan() async {
        guard !Task.isCancelled else { return }
        do { try await resumePreparedSession() } catch { dailyError("准备中的会话尚未通过握手；现有会话已保留。"); return }
        guard let coordinator, let store else { return }
        await reconcileWorkoutTransfers()
        dailyError(nil)
        apply(await store.snapshot())
        guard UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") else { dailyStatus("尚未请求健康数据读取权限"); return }
        // New runs go out on the Workout lane right away, in parallel with the
        // daily scan below; a long daily backfill round does not hold them back.
        Task { [weak self] in
            await self?.installObservers()
            await self?.autoSyncWorkouts(fromBackground: UIApplication.shared.applicationState != .active)
        }
        do {
            dailyStatus("扫描健康变化")
            try await coordinator.discoverDaily()
            apply(await store.snapshot())
            dailyStatus("健康变化已记录")
            let vo2Pending = await store.snapshot().vo2?.pending != nil
            if pendingChanges > 0 || vo2Pending, let transfers, let secret = try ClientConfiguration.load() {
                dailyStatus("同步日常健康")
                try await coordinator.syncDaily(secret: secret, transfers: transfers, budget: dailyRoundBudget)
                apply(await store.snapshot())
                dailyStatus(pendingChanges > 0 ? "日常健康补传中，稍后自动继续" : "日常健康已同步")
            }
        } catch is CancellationError { dailyStatus("已暂停；同步位置已保留") }
        catch { dailyError("日常健康同步尚未完成；稍后自动重试。") }
        if !Task.isCancelled { scheduleDiscoveryScan(await store.snapshot()) }
    }
    /// The status line and error text are shared with the Workout lane. While a
    /// Workout batch is running they belong to it, so a daily scan finishing in
    /// the middle does not replace "正在上传 2/5" or clear its failure summary.
    private func dailyStatus(_ text: String) { if workoutUploadOwner == nil { status = text } }
    private func dailyError(_ text: String?) { if workoutUploadOwner == nil { error = text } }
    /// A HealthKit observer wake for HRV, resting HR or sleep: record it and
    /// run the same scan-and-deliver round as a foreground return.
    func dailyWake() {
        guard store != nil else { return }
        scanHealthChanges(newWake: true)
    }
    /// The next foreground scan: the earliest daily/VO2 opportunity (a pending
    /// page's nextAttempt, i.e. the server's Retry-After, or a due discovery),
    /// else the 15-minute scan. Rate limits never push a page to 15 minutes;
    /// only non-transient errors wait for the regular scan. No Workout is timed.
    private func scheduleDiscoveryScan(_ snapshot: LocalState) {
        recoveryWake?.cancel()
        guard UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") else { return }
        let now = Date()
        var dates = [now.addingTimeInterval(900)]
        for flow in (snapshot.daily ?? [:]).values {
            if let next = flow.nextOpportunity(now: now, wakeGeneration: snapshot.dailyWakeGeneration ?? 0) { dates.append(next) }
        }
        if let next = snapshot.vo2?.nextOpportunity(now: now, wakeGeneration: snapshot.vo2WakeGeneration ?? 0) { dates.append(next) }
        guard let earliest = dates.min() else { return }
        let date = max(earliest, now.addingTimeInterval(1)).addingTimeInterval(Double.random(in: 0...0.5))
        recoveryWake = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(max(1, date.timeIntervalSinceNow))) }
            catch { return }
            self?.scanHealthChanges()
        }
    }
    func suspendForegroundWork() {
        task?.cancel(); recoveryWake?.cancel(); workoutRecheck?.cancel()
        // A Workout upload in flight is not cancelled by leaving the app: ask
        // iOS for background time and let the current batch keep going.
        if workoutUploadOwner != nil { beginBackgroundUploadTask() }
        syncScheduler.cancel()
    }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        Task { @MainActor in
            guard let transfers = await SyncModel.shared.backgroundTransfers(identifier: identifier) else { completionHandler(); return }
            transfers.handleEvents {
                Task { @MainActor in
                    await SyncModel.shared.reconcileWorkoutTransfers(identifier: identifier)
                    completionHandler()
                }
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
            RootContent(model: model)
                .environment(\.locale, Locale(identifier: "zh-Hans-CN"))
                .preferredColorScheme(.light)
                .onOpenURL { model.receiveConnectLink($0) }
                .alert("连接到这台服务器？", isPresented: Binding(
                    get: { model.pendingConnection != nil }, set: { if !$0 { model.pendingConnection = nil } })) {
                    Button("连接") { Task { await model.confirmPendingConnection() } }
                    Button("取消", role: .cancel) { model.pendingConnection = nil }
                } message: {
                    Text("你的跑步与健康数据会上传到 \(model.pendingConnection?.host ?? "")。只连接你自己部署的服务器。")
                }
                .alert("服务器连接", isPresented: Binding(
                    get: { model.connectionNotice != nil }, set: { if !$0 { model.connectionNotice = nil } })) {
                    Button("好") { model.connectionNotice = nil }
                } message: { Text(model.connectionNotice ?? "") }
                .task {
                    if await model.runDebugAcceptanceIfRequested() { return }
                    model.scanHealthChanges(newWake: true)
                }
                .onChange(of: phase) { _, phase in
                    if phase == .active { model.sceneBecameActive() }
                    else if phase == .background { model.sceneEnteredBackground() }
                }
        }
    }
}

struct SyncView: View {
    @Bindable var model: SyncModel
    @State private var apiURL = ""
    @State private var collector = ""
    @State private var token = ""
    @State private var importingRecovery = false
    @State private var configurationStatus: String?
    @State private var manualExpanded = false
    @AppStorage("developerOptions") private var developerOptions = false
    private var serverHost: String? {
        guard model.activeSessionID != nil else { return nil }
        return (try? ClientConfiguration.load())?.configuration.baseURL.host
    }
    var body: some View {
        Form {
            Section {
                if let serverHost {
                    LabeledContent("已连接", value: serverHost)
                } else {
                    Label("尚未连接服务器", systemImage: "wifi.slash").foregroundStyle(RAITheme.secondary)
                }
                Button("粘贴连接链接") { pasteLink() }.buttonStyle(RAIFormButtonStyle()).disabled(model.busy)
                DisclosureGroup("手动填写", isExpanded: $manualExpanded) {
                    TextField("服务器地址（https://…）", text: $apiURL).textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                    TextField("设备标识", text: $collector).textInputAutocapitalization(.never).autocorrectionDisabled()
                    SecureField("访问令牌", text: $token).textInputAutocapitalization(.never).autocorrectionDisabled()
                    Button("连接") { connect() }.buttonStyle(RAIFormButtonStyle())
                        .disabled(model.busy || apiURL.isEmpty || collector.isEmpty || token.isEmpty)
                }
                if let configurationStatus { Text(configurationStatus).font(.footnote).foregroundStyle(RAITheme.green) }
            } header: {
                Text("服务器")
            } footer: {
                Text("在电脑上运行 scripts/connect-link.py 生成 rai:// 链接，发到手机后直接点开，或复制后在这里粘贴。三项手动信息都在服务器生成的凭证文件里。")
            }
            Section {
                LabeledContent("读取权限", value: UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") ? "已请求" : "未请求")
                Button("请求读取权限") { model.authorize() }.buttonStyle(RAIFormButtonStyle()).disabled(model.busy || model.activeSessionID == nil)
            } header: {
                Text("健康数据授权")
            } footer: {
                Text("RAI 只读取跑步、心率、睡眠等数据。系统不会告诉 App 你具体允许了哪些项目，可在「设置 → 健康 → 数据访问与设备」中查看和修改。")
            }
            Section {
                LabeledContent("状态", value: model.status)
                if model.pendingChanges > 0 { LabeledContent("等待上传", value: "\(model.pendingChanges) 条") }
                ForEach(model.dailyProgress, id: \.self) { Text($0).font(.caption).foregroundStyle(RAITheme.secondary) }
                Button("立即同步") { model.syncDailyNow() }.buttonStyle(RAIFormButtonStyle()).disabled(!model.canStartDailySync || model.activeSessionID == nil)
            } header: {
                Text("日常健康数据")
            } footer: {
                Text("心率变异性、静息心率和睡眠用于计算恢复与准备度，App 在前台时会自动同步。")
            }
            Section {
                NavigationLink("跑步记录同步") { WorkoutSyncView(model: model) }
            } footer: {
                Text("新的跑步会自动上传；更早的历史记录可在里面补传。")
            }
            if let error = model.error { Section { RAINotice(text: error) } }
            #if DEBUG
            Section {
                Toggle("开发者选项", isOn: $developerOptions)
                if developerOptions {
                    Button("检查最近跑步结构（脱敏）") { model.inspectRunningWorkout() }.disabled(model.busy)
                    if let status = model.inspectionStatus { Text(status).font(.footnote) }
                    Button("导入新恢复会话凭证") { importingRecovery = true }.disabled(model.busy)
                    Text("旧会话将归档，仅重新导入健康 App 当前可见记录。").font(.caption)
                }
            } footer: {
                Text("仅调试构建可见，日常使用无需打开。")
            }
            #endif
        }
        #if DEBUG
        .fileImporter(isPresented: $importingRecovery, allowedContentTypes: [.json]) { result in
            Task {
                do {
                    let url = try result.get()
                    let access = url.startAccessingSecurityScopedResource()
                    defer { if access { url.stopAccessingSecurityScopedResource() } }
                    try await model.importRecoverySession(Data(contentsOf: url))
                    model.error = nil
                } catch { model.error = "恢复会话未激活：请保留凭证包并检查身份与连接。" }
            }
        }
        #endif
        .navigationTitle("服务器与健康授权")
        .navigationBarTitleDisplayMode(.inline)
            .scrollContentBackground(.hidden)
            .raiScreen()
    }
    private func pasteLink() {
        configurationStatus = nil
        guard let text = UIPasteboard.general.string?.trimmingCharacters(in: .whitespacesAndNewlines),
              let url = URL(string: text), url.scheme == "rai" else {
            model.error = "剪贴板里没有 rai:// 连接链接。"
            return
        }
        model.error = nil
        model.receiveConnectLink(url)
    }
    private func connect() {
        configurationStatus = nil
        Task {
            await model.waitUntilIdle()
            do {
                try await model.bindNewSession(baseURL: apiURL, collectorEpoch: collector, token: token)
                token = ""; model.error = nil
                configurationStatus = "已连接。接下来请授权读取健康数据。"
            } catch let failure as SyncFailure {
                let code: String
                switch failure {
                case .invalid(let value), .localState(let value): code = value
                case .receiptMismatch: code = "receipt_mismatch"
                }
                model.error = "连接未保存：\(UserFacingText.error(code))。"
            } catch { model.error = "连接失败：无法访问服务器或凭证不匹配。" }
        }
    }
}
