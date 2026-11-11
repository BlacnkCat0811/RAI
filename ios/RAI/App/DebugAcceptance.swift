import SwiftUI
import SyncCore

/// What the root view and the scene hooks do in a normal build, and what they
/// hand over to the Debug-only acceptance runners in a Debug build.
struct RootContent: View {
    @Bindable var model: SyncModel
    var body: some View {
        #if DEBUG
        if ProcessInfo.processInfo.environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" {
            VStack(spacing: 16) { Text("RAI 本机独立验收").font(.headline); Text(model.status) }
        } else if DailyDeviceDiagnostics.enabled {
            NavigationStack { SyncView(model: model) }.tint(RAITheme.orange).preferredColorScheme(.light)
        } else {
            ProductTabs(sync: model)
        }
        #else
        ProductTabs(sync: model)
        #endif
    }
}

extension SyncModel {
    /// True when a Debug acceptance run took over the launch.
    func runDebugAcceptanceIfRequested() async -> Bool {
        #if DEBUG
        let environment = ProcessInfo.processInfo.environment
        if environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" { await runLocalDeviceAcceptance(); return true }
        if environment["RAI_UA1_R_VO2"] != nil { await runUA1RVO2Acceptance(); return true }
        if DailyDeviceDiagnostics.enabled { await runDailyDeviceAcceptance(); return true }
        #endif
        return false
    }
    /// Normal scene activation, unless an acceptance run owns the app.
    func sceneBecameActive() {
        #if DEBUG
        let environment = ProcessInfo.processInfo.environment
        if environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" || environment["RAI_UA1_R_VO2"] != nil { return }
        if DailyDeviceDiagnostics.enabled { Task { try? await DailyDeviceDiagnostics.shared.event("scene_active") } }
        #endif
        scanHealthChanges(newWake: true)
    }
    func sceneEnteredBackground() {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled { Task { try? await DailyDeviceDiagnostics.shared.event("scene_background") } }
        #endif
        suspendForegroundWork()
    }
}

#if DEBUG
import HealthKit
import SyncCore
import UIKit

/// Debug-only acceptance and inspection entry points, kept out of the product
/// path. Real-device and Simulator scripts under scripts/local drive these.
extension SyncModel {
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
}

struct AcceptanceConfig: Decodable {
    let baseURL: String
    let collectorEpoch: String
    let token: String
}

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
