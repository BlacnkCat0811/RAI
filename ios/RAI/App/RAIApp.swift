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
    var error: String?
    private var operationBusy = false
    private let syncScheduler: ForegroundSyncScheduler
    var busy: Bool {
        get { operationBusy || syncScheduler.isRunning }
        set { operationBusy = newValue }
    }
    var canStartWorkoutSync: Bool { !operationBusy && syncScheduler.mode != .manual }
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
    var canLoadMoreWorkouts: Bool { hasMoreWorkouts && !loadingMoreWorkouts && !busy && !loadingWorkouts }
    #if DEBUG
    private var dailyRDIsolated = false
    /// Separate Debug-only namespace. Never activates/rebinds the normal session,
    /// reads its credentials, or starts product polling against its backend.
    func runLocalDeviceAcceptance() async {
        let documents = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let configURL = documents.appendingPathComponent("local-device-config.json")
        let reportURL = documents.appendingPathComponent("local-device-report.json")
        let sessionManifestURL = try? ClientConfiguration.sessionRoot().appendingPathComponent("sessions.json")
        let originalManifestHash = sessionManifestURL.flatMap { try? Data(contentsOf: $0) }.map(Wire.hash)
        let started = Date()
        var report: [String: JSON] = ["environment": .string("local-mac-device/v1"), "result": .string("RUNNING"),
            "healthValuesExported": .bool(false)]
        func checkpoint(_ stage: String) throws {
            report["stage"] = .string(stage)
            report["wallMs"] = .number(Date().timeIntervalSince(started) * 1000)
            try OutboxStore.write(JSON.object(report).data(), to: reportURL)
            status = "本机验收：" + stage
        }
        busy = true; UIApplication.shared.isIdleTimerDisabled = true
        defer {
            busy = false; UIApplication.shared.isIdleTimerDisabled = false
            let afterHash = sessionManifestURL.flatMap { try? Data(contentsOf: $0) }.map(Wire.hash)
            report["originalSessionManifestPresent"] = .bool(originalManifestHash != nil)
            report["originalSessionManifestUnchanged"] = .bool(originalManifestHash == afterHash)
            report["wallMs"] = .number(Date().timeIntervalSince(started) * 1000)
            try? OutboxStore.write(JSON.object(report).data(), to: reportURL)
        }
        do {
            try checkpoint("configuration")
            let config = try JSON.decode(Data(contentsOf: configURL))
            try FileManager.default.removeItem(at: configURL)
            guard config["environment"].text == "local-mac-device/v1",
                  let namespace = config["outboxNamespace"].text.flatMap(UUID.init(uuidString:)),
                  let expected = config["datasetSHA256"].text,
                  let command = config["command"].text, ["probe", "single", "list", "structure"].contains(command) else {
                throw SyncFailure.invalid("local_acceptance_config_invalid")
            }
            let secret = try ClientConfiguration.validatedSecret(baseURL: config["baseURL"].text ?? "",
                collectorEpoch: config["collectorEpoch"].text ?? "", token: config["token"].text ?? "")
            let host = secret.configuration.baseURL.host ?? ""
            let octets = host.split(separator: ".").compactMap { Int($0) }
            let privateIPv4 = octets.count == 4 && octets.allSatisfy { (0...255).contains($0) } &&
                (octets[0] == 10 || octets[0] == 192 && octets[1] == 168 || octets[0] == 172 && (16...31).contains(octets[1]) || octets[0] == 169 && octets[1] == 254)
            guard secret.configuration.baseURL.scheme == "http", privateIPv4,
                  host == Bundle.main.object(forInfoDictionaryKey: "RAIDevelopmentHTTPHost") as? String else {
                throw SyncFailure.invalid("local_acceptance_requires_configured_lan_host")
            }
            try checkpoint("handshake")
            let api = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token)
            let hello = try await api.call("GET", "/v2/handshake")
            guard let dataset = hello["datasetID"].text, Wire.hash(Data(dataset.utf8)) == expected,
                  hello["collectorEpoch"].text == secret.configuration.collectorEpoch,
                  hello["protocolVersion"].integer == 3,
                  hello["capabilities"]["workoutPayloadVersion"].integer == 6,
                  hello["capabilities"]["workoutUUIDIngestVersion"].integer == 1 else {
                throw SyncFailure.invalid("local_acceptance_identity_mismatch")
            }
            report["handshakeVerified"] = .bool(true)
            report["command"] = .string(command)
            if command == "probe" { report["result"] = .string("PROBE_VERIFIED"); return }

            let directory = try ClientConfiguration.sessionRoot().appendingPathComponent("local-acceptance-" + namespace.uuidString.lowercased())
            let store = try OutboxStore(directory: directory)
            let transfers = BackgroundTransfers(directory: directory, identifier: "com.rai.v2.local-acceptance." + namespace.uuidString.lowercased())
            let health = HealthKitClient()
            let coordinator = SyncCoordinator(store: store, health: health)
            try checkpoint("read_authorization")
            try await health.authorize()
            if command == "structure" {
                try checkpoint("inspect_structure")
                let path: URL
                if let id = config["workoutID"].text { path = try await health.inspectWorkout(id: id) }
                else { path = try await health.inspectLatestRunningWorkout() }
                let data = try Data(contentsOf: path)
                try OutboxStore.write(data, to: documents.appendingPathComponent("workout-structure.json"))
                report["result"] = .string("STRUCTURE_INSPECTED")
                return
            }
            let watchdog = MainThreadWatchdog()
            if command == "list" {
                try checkpoint("list_workouts")
                await health.resetWorkoutListEvidence()
                let summaries = try await health.recentWorkouts(limit: 25)
                var rows: [JSON] = [], privateRows: [JSON] = []
                for summary in summaries.sorted(by: { $0.start > $1.start }) {
                    let minutes = Int((summary.durationS / 60).rounded())
                    rows.append(.object(["durationMinutes": .int(Int64(minutes)), "indoor": .bool(summary.isIndoor ?? false),
                        "outcome": .string(summary.sourceOutcome)]))
                    if summary.isEligible {
                        privateRows.append(.object(["workoutID": .string(summary.id),
                            "durationMinutes": .int(Int64(minutes)), "indoor": .bool(summary.isIndoor ?? false)]))
                    }
                }
                // Identities stay in the protected App container; the pulled report is counts only.
                try OutboxStore.write(JSON.object(["schema": .string("rai.local-acceptance.eligible-workouts/1"),
                    "identifiersExportedToReport": .bool(false), "workouts": .array(privateRows)]).data(),
                    to: documents.appendingPathComponent("eligible-workouts.json"))
                report["visibleWorkoutCount"] = .int(Int64(summaries.count))
                report["eligibleWorkoutCount"] = .int(Int64(summaries.filter(\.isEligible).count))
                report["excludedWorkoutCount"] = .int(Int64(summaries.filter(\.isExcluded).count))
                report["unclassifiedWorkoutCount"] = .int(Int64(summaries.filter(\.isUnclassified).count))
                report["sourcePolicyVersion"] = .string(WorkoutSourcePolicy.version)
                report["workoutList"] = .array(rows)
                report["listEvidence"] = await health.workoutListEvidence()
                report["result"] = .string("WORKOUT_LIST_OBSERVED")
            } else if command == "single" {
                let selectedURL = directory.appendingPathComponent("selected-workout.json")
                var id = config["workoutID"].text ?? (try? JSON.decode(Data(contentsOf: selectedURL)))?["workoutID"].text
                if id == nil {
                    try checkpoint("select_latest_eligible")
                    for candidate in try await health.recentWorkouts(limit: 25) {
                        guard let workout = try await health.workout(candidate.id),
                              HealthSourcePolicy.classification(Normalizer.source(workout)) == "apple_watch" else { continue }
                        id = candidate.id; break
                    }
                    guard let selected = id else { throw SyncFailure.localState("local_eligible_workout_not_observed") }
                    try OutboxStore.write(JSON.object(["workoutID": .string(selected)]).data(), to: selectedURL)
                }
                guard let id else { throw SyncFailure.localState("local_eligible_workout_not_observed") }
                try checkpoint("single_workout_sync")
                let outcomes = try await coordinator.uploadWorkouts([id], secret: secret, transfers: transfers)
                guard let outcome = outcomes.first else { throw SyncFailure.localState("local_workout_upload_missing") }
                guard outcome.succeeded else { throw SyncFailure.localState(outcome.errorCode ?? "local_workout_upload_failed") }
                report["workoutReceiptVerified"] = .bool(true)
                report["acceptedAtPresent"] = .bool(outcome.acceptedAt != nil)
                report["result"] = .string("SINGLE_WORKOUT_ACCEPTED")
            } else {
                throw SyncFailure.invalid("local_acceptance_command_invalid")
            }
            let stalls = watchdog.finish()
            report["mainThreadMaxStallMs"] = .number(stalls.maxMs)
            report["mainThreadStallsOver250"] = .int(Int64(stalls.over250))
            report["mainThreadTicks"] = .int(Int64(stalls.ticks))
            report["processPeakRssBytes"] = .int(processPeakResidentBytes())
            report["residentAfterSyncBytes"] = .int(currentResidentBytes())
            // A list-only run never commits an Outbox state; absence is not a failure.
            if let attributes = try? FileManager.default.attributesOfItem(atPath: directory.appendingPathComponent("state.json").path) {
                report["stateBytes"] = .int((attributes[.size] as? NSNumber)?.int64Value ?? 0)
            }
            try checkpoint("completed")
        } catch {
            report["result"] = .string("BLOCKED")
            report["reason"] = .string(VO2Max.failureCode(error))
            report["systemCode"] = .int(Int64((error as NSError).code))
            status = "本机验收尚未完成"
        }
    }
    func runUA1RVO2Acceptance() async {
        guard let expected = ProcessInfo.processInfo.environment["RAI_UA1_R_DATASET_SHA256"],
              let descriptor = try? ClientConfiguration.activeDescriptor(),
              Wire.hash(Data(descriptor.datasetID.utf8)) == expected, let store else { return }
        let documents = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let mode = ProcessInfo.processInfo.environment["RAI_UA1_R_VO2"] ?? "inspect"
        var report: [String: JSON] = ["mode": .string(mode), "healthValuesExported": .bool(false)]
        do {
            let flow = try await store.vo2Flow()
            if let blocked = flow.blocked {
                let archive = try Data(contentsOf: await store.fileURL(blocked.file))
                guard Wire.hash(archive) == blocked.hash else { throw SyncFailure.localState("vo2_payload_corrupt") }
                report["blockedArchiveSHA256"] = .string(blocked.hash)
                report["nativeShape"] = try VO2MaxNormalizer.blockedNativeShape(archive)
            } else if mode != "deliver" { throw SyncFailure.localState("vo2_blocked_missing") }
            if mode == "stage" {
                guard let coordinator else { throw SyncFailure.localState("sync_unavailable") }
                try await coordinator.discoverVO2()
            } else if mode == "deliver" {
                guard let coordinator, let transfers, let secret = try ClientConfiguration.load() else {
                    throw SyncFailure.localState("sync_unavailable")
                }
                try await coordinator.syncVO2Only(secret: secret, transfers: transfers)
            }
            let current = try await store.vo2Flow()
            report["anchorCommitted"] = .bool(current.anchor != nil)
            report["sequence"] = .int(current.sequence)
            report["bootstrapComplete"] = .bool(current.bootstrapComplete)
            report["hasMore"] = .bool(current.hasMore)
            report["blockedRetained"] = .bool(current.blocked != nil)
            report["pending"] = .bool(current.pending != nil)
            if let code = current.error { report["errorCode"] = .string(code) }
            if let pending = current.pending {
                let raw = try Data(contentsOf: await store.fileURL(pending.file))
                guard Wire.hash(raw) == pending.hash else { throw SyncFailure.localState("vo2_payload_corrupt") }
                let body = try JSON.decode(raw), facts = body["facts"].list
                report["pendingSHA256"] = .string(pending.hash)
                report["stagedFactCount"] = .int(Int64(facts.count))
                var fields: [String: Int] = [:]
                for fact in facts { for key in fact["metadata"].fields.keys { fields[key, default: 0] += 1 } }
                report["stagedMetadataFieldCounts"] = .object(fields.mapValues { .int(Int64($0)) })
            }
            report["result"] = .string(mode == "inspect" ? "INSPECTED" : (current.error == nil ? "COMPLETED" : "BLOCKED"))
        } catch { report["result"] = .string("BLOCKED"); report["reason"] = .string(VO2Max.failureCode(error)) }
        try? OutboxStore.write(JSON.object(report).data(), to: documents.appendingPathComponent("ua1-r-vo2-report.json"))
    }
    /// Operator-enabled device acceptance. Uses the real App's session,
    /// Outbox, scheduler and Daily delivery; never discovers Workout data.
    func runDailyDeviceAcceptance() async {
        guard DailyDeviceDiagnostics.enabled else { return }
        let documents = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let path = documents.appendingPathComponent("m5a-rd-config.json")
        UIApplication.shared.isIdleTimerDisabled = true
        defer { UIApplication.shared.isIdleTimerDisabled = false }
        do {
            let config = try JSON.decode(Data(contentsOf: path))
            guard let command = config["command"].text, ["prepare", "stage", "offline", "bootstrap", "incremental", "observer", "once"].contains(command),
                  let expected = config["datasetFingerprint"].text else { throw SyncFailure.invalid("rd_config_invalid") }
            try await DailyDeviceDiagnostics.shared.begin(command)
            // Consume the private command/credential file immediately. The
            // normal session Keychain stores credentials after activation.
            try FileManager.default.removeItem(at: path)
            if command == "prepare" {
                DailyDeviceDiagnostics.probeLocalTransport(config["baseURL"].text)
                try await bindNewSession(baseURL: config["baseURL"].text ?? "", collectorEpoch: config["collectorEpoch"].text ?? "", token: config["token"].text ?? "")
            }
            guard let descriptor = try ClientConfiguration.activeDescriptor(), Wire.hash(Data(descriptor.datasetID.utf8)) == expected,
                  let store, let coordinator, let transfers, let secret = try ClientConfiguration.load() else { throw SyncFailure.invalid("rd_isolation_mismatch") }
            dailyRDIsolated = true
            try await DailyDeviceDiagnostics.shared.event("isolated_session_verified")
            try await store.bind(Scope(serviceID: descriptor.serviceID, datasetID: descriptor.datasetID, datasetEpoch: descriptor.datasetEpoch, collectorEpoch: descriptor.collectorEpoch))
            try await DailyDeviceDiagnostics.shared.snapshot("restored", store: store)
            if command == "prepare" {
                guard HKHealthStore.isHealthDataAvailable() else { throw SyncFailure.invalid("healthkit_unavailable") }
                try await DailyDeviceDiagnostics.shared.event("authorization_request_started")
                try await HKHealthStore().requestAuthorization(toShare: [], read: Set(HealthKitClient.dailyTypes.map { $0 as HKObjectType }))
                UserDefaults.standard.set(true, forKey: "healthAuthorizationRequested")
                authorization = "已请求读取权限；数据是否可用由系统管理"
                try await DailyDeviceDiagnostics.shared.event("authorization_request_completed")
            } else if command == "stage" {
                try await store.markDailyWake()
                try await coordinator.discoverDaily()
                try await DailyDeviceDiagnostics.shared.snapshot("discovery_before_upload", store: store)
            } else if command == "offline" {
                do {
                    try await coordinator.syncDaily(secret: secret, transfers: transfers)
                    try await DailyDeviceDiagnostics.shared.event("offline_call_returned")
                } catch {
                    try await DailyDeviceDiagnostics.shared.event("offline_transport_failed", fields: ["systemCode": .int(Int64((error as NSError).code))])
                }
                try await DailyDeviceDiagnostics.shared.snapshot("after_offline_attempt", store: store)
            } else {
                let rounds = command == "bootstrap" ? 90 : 1
                for round in 0..<rounds {
                    let start = Date()
                    if round == 0 {
                        syncDailyNow()
                        await syncScheduler.waitForCompletion()
                    } else {
                        // Continue the same foreground opportunity. Repeated
                        // manual wake generations would create extra empty
                        // coverage pages for already caught-up types.
                        do { try await coordinator.syncDaily(secret: secret, transfers: transfers) }
                        catch { try await DailyDeviceDiagnostics.shared.event("continuation_failed", fields: ["httpStatus": .int(Int64((error as? UploadError)?.status ?? 0))]) }
                    }
                    try await DailyDeviceDiagnostics.shared.event("manual_opportunity_completed", fields: ["round": .int(Int64(round)), "wallMs": .number(Date().timeIntervalSince(start) * 1000)])
                    try await DailyDeviceDiagnostics.shared.snapshot("after_manual_opportunity", store: store)
                    let state = await store.snapshot()
                    if DailyHealth.types.allSatisfy({ state.daily?[$0].map { $0.bootstrapComplete && $0.pending == nil && !$0.hasMore && $0.error == nil } == true }) { break }
                    if command == "bootstrap" { try await Task.sleep(for: .seconds(2)) }
                }
                if command == "observer" {
                    try await coordinator.observeDaily { [weak self] in
                        Task { @MainActor in self?.syncDailyNow() }
                    }
                    try await DailyDeviceDiagnostics.shared.event("observer_registered")
                    try await Task.sleep(for: .seconds(20))
                    for _ in 0..<8 { syncDailyNow() }
                    await syncScheduler.waitForCompletion()
                    try await DailyDeviceDiagnostics.shared.event("manual_burst_completed", fields: ["requested": .int(8)])
                    try await DailyDeviceDiagnostics.shared.snapshot("after_observer_and_manual_burst", store: store)
                }
            }
            let finalState = await store.snapshot()
            apply(finalState)
            if DailyHealth.types.allSatisfy({ finalState.daily?[$0].map { $0.pending == nil && !$0.hasMore && $0.error == nil } == true }) { error = nil }
            try await DailyDeviceDiagnostics.shared.snapshot("command_completed", store: store)
            try await DailyDeviceDiagnostics.shared.event("run_completed", fields: ["command": .string(command)])
            status = "M5A-RD 脱敏验收命令完成"
        } catch {
            let code: String
            switch error {
            case SyncFailure.invalid(let value), SyncFailure.localState(let value): code = value
            case let failure as UploadError: code = failure.code.range(of: #"^[a-z0-9_-]{1,80}$"#, options: .regularExpression) != nil ? failure.code : "rd_upload_failed"
            default: code = "rd_command_failed"
            }
            try? await DailyDeviceDiagnostics.shared.event("run_failed", fields: ["code": .string(code), "systemCode": .int(Int64((error as NSError).code))])
            status = "M5A-RD 验收尚未完成"
        }
    }
    #endif
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
    private(set) var activeSessionID: String?
    private var sessions: SyncSessionStore?
    private var transferRegistry: [String: BackgroundTransfers] = [:]
    private(set) var transfers: BackgroundTransfers?
    private var coordinator: SyncCoordinator?
    private var store: OutboxStore?
    private var task: Task<Void, Never>?
    init(syncScheduler: ForegroundSyncScheduler = ForegroundSyncScheduler()) {
        self.syncScheduler = syncScheduler
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
                Task { try? await bindNewSession(baseURL: value.baseURL, collectorEpoch: value.collectorEpoch, token: value.token) }
            }
            #endif
        } catch { self.error = "本地状态不可用，现有文件已保留。" }
    }
    private func install(_ descriptor: SyncSessionDescriptor) throws {
        guard let sessions else { throw SyncFailure.localState("session_store_unavailable") }
        let directory = try SyncSessionStore.directory(root: sessions.root, sessionID: descriptor.sessionID)
        let store = try OutboxStore(directory: directory)
        let transfers = transferRegistry[descriptor.backgroundIdentifier] ?? BackgroundTransfers(directory: directory, identifier: descriptor.backgroundIdentifier, sessionID: descriptor.sessionID)
        transferRegistry[descriptor.backgroundIdentifier] = transfers
        self.store = store; self.transfers = transfers
        self.coordinator = SyncCoordinator(store: store, sessionStore: sessions, sessionID: descriptor.sessionID)
        activeSessionID = descriptor.sessionID
        workouts = []; selected = []; workoutResults = [:]; batch = nil; currentWorkoutID = nil
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
        // Outbox and credential are durable before the final validating handshake.
        let validation = try await api.call("GET", "/v2/handshake")
        try await sessions.activate(descriptor.sessionID, handshake: validation)
        try install(try await sessions.requireActive(descriptor.sessionID))
        apply(await store!.snapshot()); status = "新同步会话已启用；将重新发现当前可见健康记录"
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
    private func resumePreparedSession() async throws {
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
                try? await coordinator.observeDaily { [weak self] in await self?.dailyWake() }
            } catch { self.error = "健康数据授权请求失败。" }
        }
    }
    func loadWorkouts() async {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled { return }
        #endif
        guard !loadingWorkouts, !busy, let coordinator else { return }
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
            if !busy {
                if workouts.isEmpty { status = "HealthKit 中还没有可见的跑步记录" }
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
    /// Selects only the currently displayed page.
    func selectCurrentPage() {
        let eligible = Set(workouts.filter(\.isEligible).map(\.id))
        selected = Set(pageWorkoutIDs.filter { eligible.contains($0) })
        status = "已选择本页 \(selected.count) 条符合条件记录"
    }
    /// Selects every eligible record across the full visible history. Pages
    /// are read one at a time and no detail query or upload is started.
    func selectAllEligibleWorkouts() async {
        guard !loadingWorkouts, !loadingMoreWorkouts, !busy, let coordinator else { return }
        loadingMoreWorkouts = true; defer { loadingMoreWorkouts = false }
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
            selected = Set(workouts.filter(\.isEligible).map(\.id))
            status = "已选择全部 \(selected.count) 条符合条件记录"
            error = nil
        } catch let failure as SyncFailure {
            error = UserFacingText.syncFailure(failure)
        } catch {
            self.error = "未能读取全部历史记录，已保留当前选择。"
        }
    }
    #if DEBUG
    /// Writes a redacted acceptance snapshot to Documents. It contains only
    /// hashed Workout ids, per-record final states and counts; never
    /// coordinates, heart rate, distance values, tokens or raw HealthKit data.
    @discardableResult
    func exportAcceptanceEvidence() -> URL? {
        let rows: [JSON] = workouts.map { workout in
            let result = workoutResults[workout.id]
            var row: [String: JSON] = [
                "workoutIDHash": .string(Wire.hash(Data(workout.id.utf8))),
                "state": .string(result?.state.rawValue ?? "not_uploaded"),
                "sourceOutcome": .string(workout.sourceOutcome),
                "distancePresent": .bool(workout.distanceM != nil),
                "heartRatePresent": .bool(workout.averageHeartRate != nil),
                "indoor": workout.isIndoor.map(JSON.bool) ?? .null,
            ]
            row["acceptedAt"] = result?.acceptedAt.map { .string(ISO8601DateFormatter().string(from: $0)) } ?? .null
            row["errorCode"] = result?.errorCode.map(JSON.string) ?? .null
            return .object(row)
        }
        let batchRow: JSON
        if let batch {
            batchRow = .object([
                "selectedCount": .int(Int64(batch.ids.count)),
                "succeededCount": .int(Int64(batch.ids.filter { workoutResults[$0]?.state == .succeeded }.count)),
                "failedCount": .int(Int64(batch.ids.filter { workoutResults[$0]?.state == .failed }.count)),
                "startedAt": .string(ISO8601DateFormatter().string(from: batch.startedAt)),
                "finishedAt": batch.finishedAt.map { .string(ISO8601DateFormatter().string(from: $0)) } ?? .null,
                "durationMs": batch.duration.map { .number($0 * 1000) } ?? .null,
            ])
        } else { batchRow = .null }
        let report: JSON = .object([
            "schema": .string("rai.workout-upload.acceptance/v2"),
            "generatedAt": .string(ISO8601DateFormatter().string(from: Date())),
            "appVersion": .string(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""),
            "iOSVersion": .string(UIDevice.current.systemVersion),
            "deviceModel": .string(UIDevice.current.model),
            "loadedWorkoutCount": .int(Int64(workouts.count)),
            "hasMoreWorkouts": .bool(hasMoreWorkouts),
            "selectedCount": .int(Int64(selected.count)),
            "batch": batchRow,
            "rows": .array(rows),
        ])
        do {
            let url = try FileManager.default.url(for: .documentDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
                .appendingPathComponent("per-workout-sync-acceptance.json")
            try OutboxStore.write(try report.data(), to: url)
            return url
        } catch { self.error = "验收证据导出失败"; return nil }
    }
    #endif
    func syncWorkouts(_ ids: [String], owner: UUID = UUID()) {
        guard canStartWorkoutSync else { return }
        let eligible = Set(workouts.filter(\.isEligible).map(\.id))
        var seen = Set<String>(), ordered: [String] = []
        for id in ids where eligible.contains(id) && seen.insert(id).inserted { ordered.append(id) }
        guard !ordered.isEmpty else { return }
        recoveryWake?.cancel()
        // Reserve the selection before the scheduler waits for automatic work
        // to stop. Even a page closed in this handoff window gets final results.
        workoutUploadOwner = owner
        batch = WorkoutBatch(ids: ordered, startedAt: Date())
        for id in ordered { workoutResults[id] = WorkoutUploadResult(state: .pending) }
        syncScheduler.startManual { await self.runWorkoutBatch(ordered, owner: owner) }
    }
    func cancelWorkoutSync(owner: UUID? = nil) {
        guard let activeOwner = workoutUploadOwner, owner == nil || owner == activeOwner else { return }
        syncScheduler.cancel()
        if let batch { finishBatch(batch.ids, interrupted: true) }
        currentWorkoutID = nil
        workoutUploadOwner = nil
    }
    /// One handshake, then one read/build/POST per selected Workout, strictly
    /// sequential. A per-record failure does not stop the batch. Any selected
    /// record without a final outcome after cancellation counts as failed.
    private func runWorkoutBatch(_ ids: [String], owner: UUID) async {
        let round = batch ?? WorkoutBatch(ids: ids, startedAt: Date())
        defer { if workoutUploadOwner == owner { workoutUploadOwner = nil } }
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
        currentWorkoutID = nil
        if outcome.succeeded {
            workoutResults[outcome.workoutID] = WorkoutUploadResult(state: .succeeded, acceptedAt: outcome.acceptedAt)
            recordUpload(at: outcome.acceptedAt ?? Date())
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
    private func apply(_ snapshot: LocalState) {
        // Workout uploads have no persisted task state. Only the daily journal
        // keeps a durable pending page here.
        pendingChanges = (snapshot.daily ?? [:]).values.filter { $0.pending != nil }.count
    }
    /// Foreground discovery timer. It never starts an upload.
    private var recoveryWake: Task<Void, Never>?
    func syncDailyNow() {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled, !dailyRDIsolated { status = "请先完成独立验收会话连接"; return }
        #endif
        guard canStartWorkoutSync else { return }
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
    /// Launch, foreground return and HealthKit observer callbacks only scan and
    /// journal local changes. They never start a Workout or daily upload.
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
        guard !busy else { return }
        syncScheduler.startAutomatic {
            if newWake, let store = self.store { try? await store.markDailyWake() }
            await self.performForegroundScan()
        }
    }
    private func performForegroundScan() async {
        guard !Task.isCancelled else { return }
        do { try await resumePreparedSession() } catch { self.error = "准备中的会话尚未通过握手；现有会话已保留。"; return }
        guard let coordinator, let store else { return }
        error = nil
        apply(await store.snapshot())
        guard UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") else { status = "尚未请求健康数据读取权限"; return }
        do {
            status = "扫描健康变化"
            try await coordinator.discoverDaily()
            apply(await store.snapshot())
            status = "健康变化已记录；Workout 仅在点击同步后上传"
        } catch is CancellationError { status = "已暂停；同步位置已保留" }
        catch { self.error = "日常健康变化扫描尚未完成；同步位置已保留。" }
        if !Task.isCancelled { scheduleDiscoveryScan(await store.snapshot()) }
    }
    private func dailyWake() {
        guard let store else { return }
        Task { self.apply(await store.snapshot()) }
    }
    /// Only a due daily discovery or the 15-minute foreground scan is
    /// scheduled. No pending delivery, Workout task or VO2 upload is timed.
    private func scheduleDiscoveryScan(_ snapshot: LocalState) {
        recoveryWake?.cancel()
        guard UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") else { return }
        var dates = [Date().addingTimeInterval(900)]
        for flow in (snapshot.daily ?? [:]).values
        where flow.error == nil && flow.discoveryDue(wakeGeneration: snapshot.dailyWakeGeneration ?? 0) {
            dates.append(Date().addingTimeInterval(2))
        }
        guard let date = dates.min() else { return }
        recoveryWake = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(max(1, date.timeIntervalSinceNow))) }
            catch { return }
            self?.scanHealthChanges()
        }
    }
    func suspendForegroundWork() {
        cancelWorkoutSync()
        task?.cancel(); syncScheduler.cancel(); recoveryWake?.cancel()
    }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        Task { @MainActor in
            guard let transfers = await SyncModel.shared.backgroundTransfers(identifier: identifier) else { completionHandler(); return }
            // Background URLSession events belong to the daily/VO2 file
            // transfers only. They never resume or start a Workout upload.
            transfers.handleEvents { completionHandler() }
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
            Group {
                #if DEBUG
                if ProcessInfo.processInfo.environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" {
                    VStack(spacing: 16) { Text("RAI 本机独立验收").font(.headline); Text(model.status) }
                }
                else if DailyDeviceDiagnostics.enabled {
                    NavigationStack { SyncView(model: model) }
                        .tint(RAITheme.orange).preferredColorScheme(.dark)
                }
                else { ProductTabs(sync: model) }
                #else
                ProductTabs(sync: model)
                #endif
            }
                .environment(\.locale, Locale(identifier: "zh-Hans-CN"))
                .preferredColorScheme(.dark)
                .task {
                    #if DEBUG
                    if ProcessInfo.processInfo.environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" { await model.runLocalDeviceAcceptance(); return }
                    if ProcessInfo.processInfo.environment["RAI_UA1_R_VO2"] != nil { await model.runUA1RVO2Acceptance(); return }
                    if DailyDeviceDiagnostics.enabled { await model.runDailyDeviceAcceptance(); return }
                    #endif
                    model.scanHealthChanges(newWake: true)
                }
                .onChange(of: phase) { _, phase in
                    if phase == .active {
                        #if DEBUG
                        if ProcessInfo.processInfo.environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" { return }
                        if ProcessInfo.processInfo.environment["RAI_UA1_R_VO2"] != nil { return }
                        if DailyDeviceDiagnostics.enabled {
                            Task { try? await DailyDeviceDiagnostics.shared.event("scene_active") }
                            model.scanHealthChanges(newWake: true)
                            return
                        }
                        #endif
                        model.scanHealthChanges(newWake: true)
                    }
                    else if phase == .background {
                        #if DEBUG
                        if DailyDeviceDiagnostics.enabled { Task { try? await DailyDeviceDiagnostics.shared.event("scene_background") } }
                        #endif
                        model.suspendForegroundWork()
                    }
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
                Button("同步日常健康") { model.syncDailyNow() }.disabled(!model.canStartWorkoutSync)
                Text("同步 Apple 健康中的心率变异性、静息心率与睡眠记录；读取可见性由系统管理。").font(.caption)
                LabeledContent("日常健康待交付记录", value: String(model.pendingChanges))
                if let batch = model.batch {
                    LabeledContent("本轮 Workout", value: "已选 \(batch.ids.count) 条")
                }
                if let error = model.error { Text(error).foregroundStyle(.red).font(.footnote) }
                NavigationLink("健康数据同步（选择记录）") { WorkoutSyncView(model: model) }
                Text("每条跑步记录独立读取、独立上传、独立确认；不会把多条记录合并为一个上传包。").font(.caption)
                Text("启动、回到前台和 HealthKit 变化只扫描本地变化，不会自动上传。Workout 只有在你选择记录并点击“同步”后才发送；中断后再次点击会重新读取并重试。").font(.caption)
            }
            Section("灾难恢复") {
                Button("导入新恢复会话凭证") { importingRecovery = true }.disabled(model.busy)
                Text("旧会话将归档。仅重新导入健康 App 当前可见记录；训练历史不迁移。备份库不得直接续接旧会话。").font(.caption)
            }
            Section("开发接口") {
                TextField("服务地址", text: $apiURL).textInputAutocapitalization(.never).autocorrectionDisabled()
                TextField("采集器周期标识", text: $collector).textInputAutocapitalization(.never).autocorrectionDisabled()
                SecureField("设备访问令牌", text: $token).textInputAutocapitalization(.never).autocorrectionDisabled()
                Button("建立独立同步会话") {
                    configurationStatus = nil
                    Task {
                    do {
                        try await model.bindNewSession(baseURL: apiURL, collectorEpoch: collector, token: token)
                        token = ""; model.error = nil
                        configurationStatus = "会话已启用。请请求读取权限，然后同步。"
                    } catch let failure as SyncFailure {
                        let code: String
                        switch failure {
                        case .invalid(let value), .localState(let value): code = value
                        case .receiptMismatch: code = "receipt_mismatch"
                        }
                        model.error = "配置未保存：\(UserFacingText.error(code))。"
                    } catch { model.error = "会话未切换：连接或握手失败。" }
                    }
                }.disabled(model.busy)
                if let configurationStatus { Text(configurationStatus).font(.footnote).foregroundStyle(.green) }
                Text("请使用管理员新建的数据集凭证。新会话从健康 App 当前可见历史开始；旧会话和旧训练历史保留但不迁移。").font(.caption)
            }
        }.fileImporter(isPresented: $importingRecovery, allowedContentTypes: [.json]) { result in
            Task {
                do {
                    let url = try result.get()
                    let access = url.startAccessingSecurityScopedResource()
                    defer { if access { url.stopAccessingSecurityScopedResource() } }
                    try await model.importRecoverySession(Data(contentsOf: url))
                    model.error = nil
                } catch { model.error = "恢复会话未激活：请保留凭证包并检查身份与连接。" }
            }
        }.navigationTitle("同步与诊断")
            .scrollContentBackground(.hidden)
            .raiScreen()
    }
}


#if DEBUG
private struct AcceptanceConfig: Decodable {
    let baseURL: String
    let collectorEpoch: String
    let token: String
}
#endif

#if DEBUG
/// Debug-only technical instrumentation for the local device acceptance path.
///
/// Records the longest main-thread stall observed while one sync runs, so a
/// "no sustained main-thread blocking" claim is measured on the device instead
/// of assumed. A main-queue timer that fires late by Δ means the main thread was
/// unavailable for Δ; the largest Δ is the worst stall in the window.
@MainActor
final class MainThreadWatchdog {
    private var timer: Timer?
    private var last = Date()
    private var maxMs: Double = 0
    private var over250 = 0
    private var ticks = 0
    private let interval = 0.05

    init() {
        let timer = Timer(timeInterval: interval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.tick() }
        }
        RunLoop.main.add(timer, forMode: .common)
        self.timer = timer
    }

    private func tick() {
        let now = Date()
        let lateness = now.timeIntervalSince(last) - interval
        last = now
        ticks += 1
        guard lateness > 0 else { return }
        let milliseconds = lateness * 1000
        if milliseconds > maxMs { maxMs = milliseconds }
        if milliseconds > 250 { over250 += 1 }
    }

    func finish() -> (maxMs: Double, over250: Int, ticks: Int) {
        timer?.invalidate()
        timer = nil
        return ((maxMs * 10).rounded() / 10, over250, ticks)
    }
}

/// Peak resident size of this process. Darwin reports `ru_maxrss` in bytes.
func processPeakResidentBytes() -> Int64 {
    var usage = rusage()
    guard getrusage(RUSAGE_SELF, &usage) == 0 else { return 0 }
    return Int64(usage.ru_maxrss)
}

func currentResidentBytes() -> Int64 {
    var info = mach_task_basic_info()
    var count = mach_msg_type_number_t(MemoryLayout<mach_task_basic_info>.size / MemoryLayout<natural_t>.size)
    let result = withUnsafeMutablePointer(to: &info) { pointer in
        pointer.withMemoryRebound(to: integer_t.self, capacity: Int(count)) {
            task_info(mach_task_self_, task_flavor_t(MACH_TASK_BASIC_INFO), $0, &count)
        }
    }
    return result == KERN_SUCCESS ? Int64(info.resident_size) : 0
}
#endif
