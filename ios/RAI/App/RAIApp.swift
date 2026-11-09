import SwiftUI
import UIKit
import SyncCore
import UniformTypeIdentifiers
#if DEBUG
import HealthKit
#endif

@MainActor @Observable
final class SyncModel {
    static let shared = SyncModel()
    var authorization = UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") ? "已请求读取权限；数据是否可用由系统管理" : "尚未请求"
    var status = "空闲"
    var pending = 0
    var discoveryRemaining = 0
    var discoveryQueries = 0
    var pendingChanges = 0
    var legacyPending = 0
    var parkedAssociations = 0
    var lastACK: Date?
    var lastSync: Date?
    var error: String?
    var completenessState: String?
    var completenessBlocked = 0
    private var operationBusy = false
    private let syncScheduler: ForegroundSyncScheduler
    var busy: Bool {
        get { operationBusy || syncScheduler.isRunning }
        set { operationBusy = newValue }
    }
    var canStartWorkoutSync: Bool { !operationBusy && syncScheduler.mode != .manual }
    var workouts: [HealthWorkoutSummary] = []
    var tasks: [String: WorkoutSyncTask] = [:]
    var serverStatuses: [String: String] = [:]
    var serverPresent: Set<String> = []
    var selected: Set<String> = []
    var loadingWorkouts = false
    var currentWorkoutID: String?
    var workoutsRefreshedAt: Date?
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
                  let command = config["command"].text, ["probe", "single", "completeness", "list", "structure"].contains(command) else {
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
                  hello["capabilities"]["workoutSyncCompletenessV2Version"].integer == 2 else {
                throw SyncFailure.invalid("local_acceptance_identity_mismatch")
            }
            report["handshakeVerified"] = .bool(true)
            report["command"] = .string(command)
            if command == "probe" { report["result"] = .string("PROBE_VERIFIED"); return }

            let directory = try ClientConfiguration.sessionRoot().appendingPathComponent("local-acceptance-" + namespace.uuidString.lowercased())
            let store = try OutboxStore(directory: directory)
            let transfers = BackgroundTransfers(directory: directory, identifier: "com.rai.v2.local-acceptance." + namespace.uuidString.lowercased())
            let health = HealthKitClient()
            // Acceptance-only bounded-concurrency selector (1/2/4) so the real
            // device can compare the same Workout under each policy. Absent or
            // out-of-range values keep the product default.
            let requestedStreams = Int(config["captureConcurrency"].integer ?? 0)
            let captureStreams = (1...4).contains(requestedStreams) ? requestedStreams : CaptureConcurrencyPolicy.standard.streams
            let coordinator = SyncCoordinator(store: store, health: health, captureConcurrency: captureStreams)
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
                try await coordinator.syncWorkout(id: id, secret: secret, transfers: transfers, forceNewBundle: false)
                guard let task = await store.task(id), task.uploadReceipt != nil else { throw SyncFailure.localState("local_workout_receipt_missing") }
                report["timings"] = .object(task.metrics.mapValues(JSON.number))
                report["workoutReceiptVerified"] = .bool(true)
                report["result"] = .string("SINGLE_WORKOUT_ACKNOWLEDGED")
            } else {
                try checkpoint("completeness_sync")
                try await coordinator.syncWorkoutCompleteness(secret: secret, transfers: transfers)
                let flow = await store.snapshot().workoutCompleteness
                guard let flow, flow.generation > 0, flow.cycle == nil, flow.lastReceipt != nil else { throw SyncFailure.localState("local_completeness_not_acknowledged") }
                report["generation"] = .int(flow.generation)
                report["committedAnchorCount"] = .int(Int64(flow.anchors.count))
                report["queryCounts"] = .object((flow.lastQueryCounts ?? [:]).mapValues { .int(Int64($0)) })
                report["result"] = .string("COMPLETENESS_ACKNOWLEDGED")
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
            report["reason"] = .string(WorkoutCompleteness.failureCode(error))
            report["systemCode"] = .int(Int64((error as NSError).code))
            status = "本机验收尚未完成"
        }
    }
    func runUA1RCompletenessAcceptance() async {
        let documents = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let reportURL = documents.appendingPathComponent("ua1-r-completeness-report.json")
        var report: [String: JSON] = ["result": .string("RUNNING"), "healthValuesExported": .bool(false)]
        try? OutboxStore.write(JSON.object(report).data(), to: reportURL)
        let started = Date()
        busy = true; UIApplication.shared.isIdleTimerDisabled = true
        defer {
            busy = false; UIApplication.shared.isIdleTimerDisabled = false
            report["wallMs"] = .number(Date().timeIntervalSince(started) * 1000)
            try? OutboxStore.write(JSON.object(report).data(), to: reportURL)
        }
        do {
            guard let expected = ProcessInfo.processInfo.environment["RAI_UA1_R_DATASET_SHA256"],
                  let descriptor = try ClientConfiguration.activeDescriptor(), Wire.hash(Data(descriptor.datasetID.utf8)) == expected,
                  let baseStore = store else { throw SyncFailure.localState("completeness_isolation_mismatch") }
            let configURL = documents.appendingPathComponent("ua1-r-completeness-config.json")
            let config = try JSON.decode(Data(contentsOf: configURL))
            try FileManager.default.removeItem(at: configURL)
            guard let namespace = config["outboxNamespace"].text.flatMap(UUID.init(uuidString:)) else { throw SyncFailure.invalid("completeness_config_invalid") }
            let store = try OutboxStore(directory: baseStore.directory.appendingPathComponent("acceptance-" + namespace.uuidString.lowercased()))
            if ["inspect", "inspect-keys"].contains(ProcessInfo.processInfo.environment["RAI_UA1_R_COMPLETENESS"] ?? "") {
                let health = HealthKitClient(), tasks = await store.snapshot().tasks ?? [:]
                var shapes: [JSON] = [], inventory: [JSON] = []
                for (id, task) in tasks where task.errorCode == "metadata_requires_review" {
                    if let workout = try await health.workout(id) {
                        shapes.append(WorkoutInspection.blockedMetadataShape(workout))
                        inventory.append(WorkoutInspection.keyInventory(workout))
                    }
                }
                report["blockedWorkoutCount"] = .int(Int64(shapes.count))
                report["metadataShapes"] = .array(shapes)
                let flow = await store.snapshot().workoutCompleteness
                var classificationCounts: [String: Int] = [:], productCounts: [String: Int] = [:]
                var nonWatch: [JSON] = []
                for id in flow?.cycle?.inventory ?? flow?.inventory ?? [] {
                    guard let workout = try await health.workout(id) else { continue }
                    let shape = WorkoutInspection.sourceShape(workout)
                    let classification = shape["classification"].text ?? "unknown"
                    classificationCounts[classification, default: 0] += 1
                    productCounts[shape["productClass"].text ?? "unknown", default: 0] += 1
                    if classification != "apple_watch", nonWatch.count < 8 { nonWatch.append(shape) }
                }
                report["sourceSurveyTotal"] = .int(Int64(classificationCounts.values.reduce(0, +)))
                report["sourceSurveyClassifications"] = .object(classificationCounts.mapValues { .int(Int64($0)) })
                report["sourceSurveyProductClasses"] = .object(productCounts.mapValues { .int(Int64($0)) })
                report["sourceSurveyNonWatchShapes"] = .array(nonWatch)
                if ProcessInfo.processInfo.environment["RAI_UA1_R_COMPLETENESS"] == "inspect-keys" {
                    // Raw key names remain inside the protected App container;
                    // the device report above stays digest-only.
                    try OutboxStore.write(JSON.object(["schema": .string("rai.ua1-r3a.metadata-keys/1"),
                        "healthValuesExported": .bool(false), "workouts": .array(inventory)]).data(),
                        to: documents.appendingPathComponent("ua1-r-metadata-keys.json"))
                    report["keyInventoryWorkouts"] = .int(Int64(inventory.count))
                }
                report["result"] = .string("INSPECTED")
                return
            }
            let transfers = BackgroundTransfers(directory: store.directory, identifier: "com.rai.v2.phase3." + namespace.uuidString.lowercased())
            let coordinator = SyncCoordinator(store: store, health: HealthKitClient())
            let secret = try ClientConfiguration.validatedSecret(baseURL: config["baseURL"].text ?? "", collectorEpoch: config["collectorEpoch"].text ?? "", token: config["token"].text ?? "")
            do { try await coordinator.syncWorkoutCompleteness(secret: secret, transfers: transfers) }
            catch {
                let code = VO2Max.failureCode(error)
                report["reason"] = .string(code.range(of: #"^[a-z0-9_:-]{1,128}$"#, options: .regularExpression) != nil ? code : "completeness_failed")
            }
            guard let flow = await store.snapshot().workoutCompleteness else { throw SyncFailure.localState("completeness_not_started") }
            report["generation"] = .int(flow.generation)
            report["manifestSHA256"] = flow.lastManifestSHA256.map(JSON.string) ?? .null
            report["committedAnchorCount"] = .int(Int64(flow.anchors.count))
            report["committedWorkoutCount"] = .int(Int64(flow.inventory.count))
            report["cyclePending"] = .bool(flow.cycle != nil)
            report["manifestPending"] = .bool(flow.cycle?.pendingFile != nil)
            let cutoff = flow.cycle?.cutoffUs ?? flow.lastCutoffUs
            report["cutoffSHA256"] = cutoff.map { .string(Wire.hash(Data(String($0).utf8))) } ?? .null
            let counts = flow.cycle.map { ["committedAnchor": $0.committedAnchorQueries, "stagedCursor": $0.stagedCursorQueries, "bootstrap": $0.bootstrapQueries] } ?? flow.lastQueryCounts ?? [:]
            report["queryCounts"] = .object(counts.mapValues { .int(Int64($0)) })
            report["capturedWorkoutCount"] = .int(Int64(flow.cycle?.inventory.count ?? flow.inventory.count))
            report["processedWorkoutCount"] = .int(Int64(flow.cycle?.completedWorkouts.count ?? flow.inventory.count))
            if let cycle = flow.cycle, cycle.contract == WorkoutCompleteness.v2Contract {
                // Counts only: no Workout identity, descriptor or health value
                // leaves the device in this report.
                let known = cycle.classification ?? [:]
                var classes: [String: Int] = [:]
                for id in cycle.inventory { classes[known[id] ?? "unclassified", default: 0] += 1 }
                func count(_ outcome: String) -> Int64 {
                    Int64(cycle.inventory.filter { WorkoutSourcePolicy.outcome(known[$0] ?? "") == outcome }.count)
                }
                report["sourcePolicyVersion"] = .string(WorkoutSourcePolicy.version)
                report["visibleWorkoutCount"] = .int(Int64(cycle.inventory.count))
                report["eligibleWorkoutCount"] = .int(count(WorkoutSourcePolicy.eligible))
                report["excludedWorkoutCount"] = .int(Int64((cycle.exclusions ?? [:]).count))
                report["unclassifiedWorkoutCount"] = .int(count(WorkoutSourcePolicy.unclassified))
                report["sourceClassificationCounts"] = .object(classes.mapValues { .int(Int64($0)) })
            }
            report["result"] = .string(flow.generation > 0 && flow.cycle == nil && flow.errorCode == nil ? "ACKNOWLEDGED" : "BLOCKED")
        } catch { report["result"] = .string("BLOCKED"); report["reason"] = .string(VO2Max.failureCode(error)) }
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
    func runPhase3Acceptance(phase: Int = 3) async {
        let configName = phase == 4 ? "phase4-config.json" : "phase3-config.json"
        let reportName = phase == 4 ? "phase4-device-report.json" : "phase3-device-report.json"
        guard let baseStore = store else { return }
        let documents = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        // A dedicated local namespace preserves all previous Outbox packets and
        // keeps this user-authorized one-Workout test separate from normal sync.
        guard let configData = try? Data(contentsOf: documents.appendingPathComponent(configName)),
              let config = try? JSON.decode(configData),
              let namespace = config["outboxNamespace"].text.flatMap(UUID.init(uuidString:)),
              let store = try? OutboxStore(directory: baseStore.directory.appendingPathComponent("acceptance-" + namespace.uuidString.lowercased())) else { return }
        let transfers = BackgroundTransfers(directory: store.directory, identifier: "com.rai.v2.phase3." + namespace.uuidString.lowercased())
        busy = true; status = "Phase 3 专用记录验收"
        UIApplication.shared.isIdleTimerDisabled = true
        var report: [String: JSON] = ["device": .string("iPhone 12 Pro Max"), "scope": .string("user_designated_workout_only"), "realDevice": .bool(true), "healthValuesExported": .bool(false), "gpsExported": .bool(false)]
        let health = HealthKitClient(), coordinator = SyncCoordinator(store: store, health: health)
        defer {
            busy = false; UIApplication.shared.isIdleTimerDisabled = false
            try? OutboxStore.write(JSON.object(report).data(), to: documents.appendingPathComponent(reportName))
            if phase == 4 { try? FileManager.default.removeItem(at: documents.appendingPathComponent(configName)) }
        }
        do {
            report["stage"] = .string("authorization_request")
            try? OutboxStore.write(JSON.object(report).data(), to: documents.appendingPathComponent(reportName))
            if ProcessInfo.processInfo.environment["RAI_PHASE3_AUTH_DONE"] != "1" { try await health.authorize() }
            report["stage"] = .string("locate_designated_workout")
            try? OutboxStore.write(JSON.object(report).data(), to: documents.appendingPathComponent(reportName))
            let workout = try await health.phase3TestWorkout(), id = workout.uuid.uuidString.lowercased()
            report["workoutIDHash"] = .string(Wire.hash(Data(id.utf8)))
            let source = Normalizer.source(workout)
            report["sourceClassification"] = .string(HealthSourcePolicy.classification(source))
            let bundleID = source["bundleID"].text ?? ""
            let suffix = bundleID.hasPrefix("com.apple.health.") ? String(bundleID.dropFirst("com.apple.health.".count)) : ""
            report["sourceEvidence"] = .object(["appleHealthDeviceNamespace": .bool(!suffix.isEmpty), "deviceNamespaceUUID": .bool(UUID(uuidString: suffix) != nil), "productType": source["productType"], "manufacturer": source["device"]["manufacturer"], "model": source["device"]["model"]])
            report["metadataKeys"] = .array((workout.metadata ?? [:]).keys.sorted().map { .string($0.hasPrefix("HK") ? $0 : "unrecognized_key") })
            report["sourceFieldsPresent"] = .object(["version": .bool(source["version"] != .null), "productType": .bool(source["productType"] != .null), "os": .bool(source["operatingSystemVersion"] != .null), "device": .bool(source["device"] != .null)])
            report["stage"] = .string("first_anchored_discovery")
            let beforeDiscovery = await store.discoverySnapshot()
            try await coordinator.discoverChanges(workoutIDs: [id])
            let first = await store.discoverySnapshot()
            try await coordinator.discoverChanges(workoutIDs: [id])
            let second = await store.discoverySnapshot()
            report["discoveryEventCount"] = .int(Int64(second.events.count))
            report["independentAnchors"] = .int(Int64(second.flows.count))
            report["outOfScopeAnchors"] = .int(Int64(second.flows.values.filter { $0.query.workoutID != id }.count))
            report["unchangedReadCreatesNoWork"] = .bool(first.events.count == second.events.count && first.work[id]?.generation == second.work[id]?.generation)
            report["discoveryQueryEvidence"] = await coordinator.discoveryQueryEvidence()
            report["newSemanticEvents"] = .int(Int64(second.events.keys.filter { beforeDiscovery.events[$0] == nil }.count))
            report["stage"] = .string("load_isolated_config")
            let secret = try ClientConfiguration.validatedSecret(baseURL: config["baseURL"].text ?? "", collectorEpoch: config["collectorEpoch"].text ?? "", token: config["token"].text ?? "")
            report["stage"] = .string("single_workout_upload")
            try await coordinator.syncWorkout(id: id, secret: secret, transfers: transfers, forceNewBundle: true)
            var snapshot = await store.snapshot()
            if let current = snapshot.tasks?[id], current.uploadReceipt != nil, !(current.pendingReadKeys ?? []).isEmpty {
                // An earlier acceptance build captured a partial bundle. Only
                // after its real ACK may a new logical version add late facts.
                try await coordinator.syncWorkout(id: id, secret: secret, transfers: transfers, forceNewBundle: true)
                snapshot = await store.snapshot()
            }
            let task = snapshot.tasks?[id]
            report["uploadAcknowledged"] = .bool(task?.uploadReceipt != nil)
            report["transportMetrics"] = .object((task?.metrics ?? [:]).mapValues { .number($0) })
            report["bundleSHA256"] = task?.bundleSHA256.map(JSON.string) ?? .null
            report["coverage"] = .object((task?.coverage ?? [:]).mapValues(\.wire))
            report["routeCaptures"] = try await phase3RouteSummary(store: store, captures: snapshot.routeCaptures ?? [:])
            let afterUpload = await store.discoverySnapshot()
            report["noSpuriousFactGenerationDuringAssembly"] = .bool(afterUpload.work[id]?.generation == second.work[id]?.generation)
            report["noPendingDiscoveryAfterACK"] = .bool(afterUpload.work[id]?.deliveredGeneration == afterUpload.work[id]?.generation)
            report["result"] = .string(task?.uploadReceipt == nil ? "PENDING" : "ACKNOWLEDGED")
            status = "Phase 3 验收证据已保存"
        } catch {
            let value: String
            if let failure = error as? SyncFailure {
                switch failure { case .invalid(let code), .localState(let code): value = code; case .receiptMismatch: value = "receipt_mismatch" }
            } else if let failure = error as? UploadError { value = failure.code }
            else { value = "device_acceptance_failed" }
            report["result"] = .string("BLOCKED"); report["errorCode"] = .string(value)
            let failedSnapshot = await store.snapshot()
            if let current = failedSnapshot.tasks?.values.first {
                report["coverage"] = .object(current.coverage.mapValues(\.wire))
                report["outboxRetained"] = .bool(current.bundleFile != nil)
                report["bundleSHA256"] = current.bundleSHA256.map(JSON.string) ?? .null
            }
            report["routeCaptures"] = .array((failedSnapshot.routeCaptures ?? [:]).map { routeID, capture in
                .object(["routeIDHash": .string(Wire.hash(Data(routeID.utf8))), "declaredPoints": .int(Int64(capture.expectedCount)), "capturedPoints": .int(Int64(capture.pointCount)), "completed": .bool(capture.completed)])
            })
            if let failure = error as? UploadError {
                if let field = failure.field, field.range(of: #"^[a-zA-Z0-9_.$\[\]]{1,256}$"#, options: .regularExpression) != nil { report["validationField"] = .string(field) }
                if let reason = failure.reason, reason.range(of: #"^[a-z_]{1,128}$"#, options: .regularExpression) != nil { report["validationReason"] = .string(reason) }
            }
            report["systemErrorCode"] = .int(Int64((error as NSError).code))
            report["systemErrorDomain"] = .string((error as NSError).domain)
            status = "Phase 3 需检查：" + value
        }
    }
    private func phase3RouteSummary(store: OutboxStore, captures: [String: RouteCaptureCheckpoint]) async throws -> JSON {
        var summaries: [JSON] = []
        for (id, capture) in captures.sorted(by: { $0.key < $1.key }) {
            let file = try FileHandle(forReadingFrom: await store.fileURL(capture.file))
            defer { try? file.close() }
            var pending = Data(), chunk = Data("[".utf8), manifest = Data(repeating: 0, count: 32)
            var points = 0, inChunk = 0, chunks = 0
            func finishChunk() {
                guard inChunk > 0 else { return }
                chunk.append(93); manifest = Wire.chain(manifest, Wire.digest(chunk))
                chunks += 1; inChunk = 0; chunk = Data("[".utf8)
            }
            while true {
                let bytes = try file.read(upToCount: 65536) ?? Data()
                if bytes.isEmpty { break }; pending += bytes
                while let end = pending.firstIndex(of: 10) {
                    if inChunk > 0 { chunk.append(44) }
                    chunk += pending[..<end]; pending.removeSubrange(...end)
                    points += 1; inChunk += 1
                    if inChunk == 5000 { finishChunk() }
                }
            }
            guard pending.isEmpty, points == capture.pointCount else { throw SyncFailure.localState("route_capture_count_mismatch") }
            finishChunk()
            summaries.append(.object(["routeIDHash": .string(Wire.hash(Data(id.utf8))), "declaredPoints": .int(Int64(capture.expectedCount)),
                "capturedPoints": .int(Int64(points)), "completed": .bool(capture.completed), "chunkCount": .int(Int64(chunks)),
                "sourceManifestSHA256": .string(manifest.map { String(format: "%02x", $0) }.joined())]))
        }
        return .array(summaries)
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
        tasks = [:]; selected = []; serverPresent = []; serverStatuses = [:]; workouts = []
        pending = 0; discoveryRemaining = 0; discoveryQueries = 0; pendingChanges = 0
        lastACK = nil; lastSync = nil
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
    func isActiveBackground(_ identifier: String) -> Bool {
        activeSessionID.map { "com.rai.v2.sync." + $0 == identifier } ?? false
    }
    func authorize() {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled, !dailyRDIsolated { status = "请先完成独立验收会话连接"; return }
        #endif
        guard !busy, let coordinator else { return }
        busy = true
        task = Task {
            defer { busy = false; if !Task.isCancelled { recoverPending() } }
            do {
                try await coordinator.authorize()
                authorization = "已请求读取权限；数据是否可用由系统管理"
                UserDefaults.standard.set(true, forKey: "healthAuthorizationRequested")
                // Reads run through the preemptible foreground scheduler.
                try? await coordinator.observeDiscovery { [weak self] in await self?.discoveryWake() }
                try? await coordinator.observeDaily { [weak self] in await self?.discoveryWake() }
            } catch { self.error = "健康数据授权请求失败。" }
        }
    }
    func loadWorkouts() async {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled { return }
        #endif
        guard !loadingWorkouts, let coordinator, let store else { return }
        loadingWorkouts = true; defer { loadingWorkouts = false }
        do {
            // Reconciliation belongs to scheduled recovery: a list refresh
            // must not mark a concurrently preparing user task interrupted.
            apply(await store.snapshot())
            workouts = try await coordinator.recentWorkouts(limit: 100)
            workoutsRefreshedAt = Date()
            await refreshServerStatuses()
            if !busy {
                if workouts.isEmpty { status = "HealthKit 中还没有可见的跑步记录" }
                error = nil
            }
            // List reads never run an untracked scan alongside user uploads.
            if UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") { recoverPending() }
        } catch let failure as SyncFailure {
            error = UserFacingText.syncFailure(failure)
        } catch {
            self.error = "健康记录列表读取失败，请检查授权。"
        }
    }
    /// Server presence is only a read projection over existing product
    /// workouts; it never creates or advances a sync task.
    private func refreshServerStatuses() async {
        guard let secret = try? ClientConfiguration.load() else { return }
        let api = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token)
        var before = "", statuses: [String: String] = [:], present: Set<String> = []
        do {
            for _ in 0..<8 {
                var query = ["limit": "50"]
                if !before.isEmpty { query["before"] = before }
                let page = try await api.call("GET", "/v2/workouts", query: query)
                for item in page["items"].list {
                    guard let id = item["workoutID"].text else { continue }
                    present.insert(id); statuses[id] = item["analysisStatus"].text ?? "pending"
                }
                let next = page["next"].text ?? ""
                if next.isEmpty || next == before { break }
                before = next
            }
            serverPresent = present; serverStatuses = statuses
        } catch {
            // Keep the last known server projection; local tasks remain valid.
        }
    }
    func checkStatus(_ id: String) {
        guard !busy, let coordinator, let transfers else { return }
        busy = true
        task = Task {
            defer { busy = false }
            do {
                guard let secret = try ClientConfiguration.load() else { throw SyncFailure.invalid("configure_development_api_first") }
                try await coordinator.checkAnalysis(id: id, secret: secret, transfers: transfers)
                if let store { apply(await store.snapshot()) }
                await refreshServerStatuses()
            } catch let failure as SyncFailure {
                error = UserFacingText.syncFailure(failure)
            } catch let failure as UploadError {
                error = UserFacingText.error(failure.code)
            } catch { self.error = "状态检查失败，请稍后重试。" }
        }
    }
    func retry(_ id: String) {
        guard canStartWorkoutSync, let store else { return }
        recoveryWake?.cancel()
        syncScheduler.startManual {
            let task = await store.task(id)
            let nonResumable = ["sample_identity_conflict", "workout_identity_conflict",
                                "series_identity_conflict", "route_identity_conflict",
                                "sample_deleted", "route_deleted", "workout_deleted",
                                "workout_not_visible", "receipt_mismatch", "bundle_too_large"]
            let canResume = task?.bundleFile != nil && task?.bundleSHA256 != nil &&
                task?.bundleBoundary != nil && !nonResumable.contains(task?.errorCode ?? "")
            await self.runWorkouts([id], forceNewBundle: !canResume)
        }
    }
    #if DEBUG
    /// Writes a redacted acceptance snapshot to Documents. It contains only
    /// hashed Workout ids, status/counts and timing metrics; never coordinates,
    /// heart rate, distance values, tokens or raw HealthKit payloads.
    @discardableResult
    func exportAcceptanceEvidence() -> URL? {
        let rows: [JSON] = workouts.map { workout in
            let task = tasks[workout.id]
            let coverage: [String: JSON] = (task?.coverage ?? [:]).mapValues { item in
                .object(["state": .string(item.state.rawValue), "sampleCount": .int(Int64(item.sampleCount)), "errorCode": item.errorCode.map(JSON.string) ?? .null])
            }
            var row: [String: JSON] = [
                "workoutIDHash": .string(Wire.hash(Data(workout.id.utf8))),
                "phase": .string(task?.phase.rawValue ?? (serverPresent.contains(workout.id) ? "SERVER_ACKNOWLEDGED" : "NOT_SYNCED")),
                "distancePresent": .bool(workout.distanceM != nil),
                "heartRatePresent": .bool(workout.averageHeartRate != nil),
                "indoor": workout.isIndoor.map(JSON.bool) ?? .null,
                "sourceNamePresent": .bool(workout.sourceName != nil),
                "deviceNamePresent": .bool(workout.deviceName != nil),
                "coverage": .object(coverage),
            ]
            if let task {
                row["metrics"] = .object(task.metrics.mapValues { .number($0) })
                row["missing"] = .array(task.missing.map(JSON.string))
                row["errorCode"] = task.errorCode.map(JSON.string) ?? .null
                row["acknowledgedAt"] = task.acknowledgedAt.map { .string(ISO8601DateFormatter().string(from: $0)) } ?? .null
                row["analysisCheckedAt"] = task.analysisCheckedAt.map { .string(ISO8601DateFormatter().string(from: $0)) } ?? .null
            }
            return .object(row)
        }
        let report: JSON = .object([
            "schema": .string("rai.per-workout-sync.acceptance/v1"),
            "generatedAt": .string(ISO8601DateFormatter().string(from: Date())),
            "appVersion": .string(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? ""),
            "iOSVersion": .string(UIDevice.current.systemVersion),
            "deviceModel": .string(UIDevice.current.model),
            "healthKitWorkoutCount": .int(Int64(workouts.count)),
            "serverWorkoutCount": .int(Int64(serverPresent.count)),
            "taskCount": .int(Int64(tasks.count)),
            "pendingPages": .int(Int64(pending)),
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
    func syncWorkouts(_ ids: [String]) {
        guard canStartWorkoutSync, !ids.isEmpty else { return }
        recoveryWake?.cancel()
        syncScheduler.startManual { await self.runWorkouts(ids, forceNewBundle: true) }
    }
    /// Distinct Workouts are independent uploads. Three run at a time so a
    /// backlog drains quickly without starving HealthKit or the network; the
    /// same Workout never runs twice, and one failure leaves the others
    /// untouched.
    private func runWorkouts(_ ids: [String], forceNewBundle: Bool) async {
        guard !Task.isCancelled else { return }
        do { try await resumePreparedSession() } catch { self.error = "准备中的会话尚未通过握手；现有会话已保留。"; return }
        guard coordinator != nil, let store, transfers != nil else { return }
        error = nil; currentWorkoutID = nil
        UIApplication.shared.isIdleTimerDisabled = true
        defer {
            currentWorkoutID = nil
            UIApplication.shared.isIdleTimerDisabled = false
        }
        guard let secret = try? ClientConfiguration.load() else {
            self.error = UserFacingText.error("configure_development_api_first"); return
        }
        let total = ids.count
        var completed = 0
        var failures = 0
        status = total > 1 ? "正在同步 \(total) 条跑步记录（最多 3 条并发）" : "正在同步跑步记录"
        await withTaskGroup(of: Bool.self) { group in
            var next = 0
            while next < min(3, total) {
                let id = ids[next]; next += 1
                group.addTask { await self.syncOne(id: id, forceNewBundle: forceNewBundle, secret: secret) }
            }
            for await ok in group {
                completed += 1
                if !ok { failures += 1 }
                if Task.isCancelled { group.cancelAll(); continue }
                if total > 1 { status = "已完成 \(completed)/\(total) 条跑步记录" }
                if next < total {
                    let id = ids[next]; next += 1
                    group.addTask { await self.syncOne(id: id, forceNewBundle: forceNewBundle, secret: secret) }
                }
            }
        }
        if Task.isCancelled { status = "已暂停；本地状态已保留" }
        else { status = failures == 0 ? "所选记录已独立同步" : "部分记录需要处理，其余记录未受影响" }
        apply(await store.snapshot())
        if !Task.isCancelled { await refreshServerStatuses() }
        if !Task.isCancelled { scheduleRecovery(await store.snapshot(), resumeDiscovery: true) }
    }

    /// Returns false only for a real failure; cancellation and pause are not
    /// failures because the resumable task keeps its bytes and identity.
    private func syncOne(id: String, forceNewBundle: Bool, secret: ClientConfiguration.Secret) async -> Bool {
        guard let coordinator, let store, let transfers else { return false }
        currentWorkoutID = id
        var round = 0
        var stopped = false
        while !Task.isCancelled && !stopped {
            round += 1
            do {
                // Journal scoped raw changes before building any bundle.
                try await coordinator.discoverChanges(workoutIDs: [id])
                var attempt = 0
                while true {
                    do {
                        // The first attempt of an explicit user sync rebuilds
                        // from HealthKit; transient retries reuse the exact
                        // bytes already persisted in the task.
                        try await coordinator.syncWorkout(id: id, secret: secret, transfers: transfers,
                                                          forceNewBundle: forceNewBundle && attempt == 0)
                        break
                    } catch let failure as UploadError where failure.retryable && attempt < 5 {
                        attempt += 1
                        let persisted = await store.task(id)?.nextAttemptAt
                        let wait = max(persisted?.timeIntervalSinceNow ?? failure.retryAfter ?? UploadError.backoff(attempt), 1)
                        status = "网络暂时不可用，\(Int(wait)) 秒后重试（第 \(attempt)/5 次）"
                        try await Task.sleep(for: .seconds(wait))
                    }
                }
            } catch is CancellationError {
                return true
            } catch let failure as UploadError {
                error = UserFacingText.error(failure.code); return false
            } catch let failure as SyncFailure {
                error = UserFacingText.syncFailure(failure); return false
            } catch {
                self.error = "该条记录同步失败，原文件与上传身份已保留。"; return false
            }
            let snapshot = await store.snapshot()
            apply(snapshot)
            guard let current = snapshot.tasks?[id],
                  current.phase == .uploading || current.phase == .preparing else { return true }
            if round >= 120 { self.error = "该记录仍未完成，请稍后重试"; return false }
            // A retryable page failure (429/503) is persisted with a
            // nextAttempt; wait for it instead of leaving the row stuck at
            // "上传中" with no scheduler.
            let next = current.nextAttemptAt
            let wait = max(next?.timeIntervalSinceNow ?? 1, 1)
            do { try await Task.sleep(for: .seconds(wait)) }
            catch { stopped = true }
        }
        return true
    }
    private func apply(_ snapshot: LocalState) {
        // Bundle tasks and HealthKit discovery are independent kinds of work.
        pending = (snapshot.tasks ?? [:]).values.filter { [.preparing, .uploading].contains($0.phase) }.count
        let discovery = snapshot.discovery ?? DiscoveryState()
        let scoped = Set(discovery.flows.values.compactMap { $0.query.workoutID })
        discoveryRemaining = max(discovery.cycleRemaining, Set(discovery.workoutArchives.keys).subtracting(scoped).count)
        discoveryQueries = discovery.flows.values.filter { $0.hasMore && $0.error == nil }.count
        pendingChanges = discovery.work.values.filter(\.hasAutomaticDeliveryWork).count + (snapshot.daily ?? [:]).values.filter { $0.pending != nil }.count
        if (snapshot.daily ?? [:]).values.contains(where: { $0.error != nil }) { error = "日常健康同步尚未完成；同步位置已保留。" }
        legacyPending = snapshot.pages.count
        parkedAssociations = snapshot.pages.filter { $0.error?.code == "association_unavailable" }.count
        lastACK = snapshot.lastACK; lastSync = snapshot.lastSync
        completenessBlocked = Int(snapshot.workoutCompleteness?.lastBlockedSummary?["count"].integer ?? 0)
        tasks = snapshot.tasks ?? [:]
    }
    /// Automatic catch-up is preemptible by an explicit Workout sync. Journal
    /// discovered deletions before resuming uploads and delivering changes.
    private var recoveryWake: Task<Void, Never>?
    /// Explicit user retry for the completeness lane. It zeroes only the
    /// per-Workout backoff; completed proofs and the cycle stay untouched.
    func retryCompleteness() {
        guard canStartWorkoutSync, let store else { return }
        Task {
            do {
                let cleared = try await store.retryWorkoutCompletenessBlocked()
                status = cleared > 0 ? "已请求重新核验 \(cleared) 条记录的训练历史完整性" : "将重新核验训练历史完整性"
                recoverPending()
            } catch { self.error = "无法重试完整性核验；本地进度已保留。" }
        }
    }
    func syncDailyNow() {        #if DEBUG
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
                self.status = self.pendingChanges > 0 ? "日常健康记录仍待交付" : "日常健康同步检查完成"
            } catch is CancellationError { self.status = "已暂停；本地状态已保留" }
            catch { self.error = "日常健康同步尚未完成；同步位置已保留。" }
            let snapshot = await store.snapshot(); self.apply(snapshot)
            if !Task.isCancelled { self.scheduleRecovery(snapshot) }
        }
    }
    func recoverPending(discoverDaily: Bool = false) {
        #if DEBUG
        if DailyDeviceDiagnostics.enabled {
            // Same scheduler and Daily state machine, scoped to the verified
            // test identity. Acceptance must not discover unrelated Workouts.
            guard dailyRDIsolated, !busy else { return }
            syncScheduler.startAutomatic {
                guard let coordinator = self.coordinator, let store = self.store, let transfers = self.transfers,
                      let secret = try? ClientConfiguration.load() else { return }
                do {
                    if discoverDaily { try await store.markDailyWake() }
                    try await DailyDeviceDiagnostics.shared.event("foreground_daily_opportunity", fields: ["newWake": .bool(discoverDaily)])
                    try await coordinator.discoverDaily()
                    try await coordinator.syncDaily(secret: secret, transfers: transfers)
                } catch is CancellationError { return }
                catch { self.error = "日常健康同步尚未完成；同步位置已保留。" }
                let snapshot = await store.snapshot(); self.apply(snapshot)
                if DailyHealth.types.allSatisfy({ snapshot.daily?[$0].map { $0.pending == nil && !$0.hasMore && $0.error == nil } == true }) { self.error = nil }
                try? await DailyDeviceDiagnostics.shared.snapshot("foreground_opportunity_completed", store: store)
                if !Task.isCancelled { self.scheduleRecovery(snapshot) }
            }
            return
        }
        #endif
        if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" || ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { return }
        guard !busy else { return }
        syncScheduler.startAutomatic {
            if discoverDaily, let store = self.store { try? await store.markDailyWake() }
            await self.performRecovery()
        }
    }
    private func performRecovery() async {
        if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" || ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { return }
        guard !Task.isCancelled else { return }
        do { try await resumePreparedSession() } catch { self.error = "准备中的会话尚未通过握手；现有会话已保留。"; return }
        guard let coordinator, let store, let transfers else { return }
        error = nil
        apply(await store.snapshot())
        status = "检查健康变化与待处理上传"
        do {
            guard let secret = try? ClientConfiguration.load() else { status = "未配置同步"; return }
            try Task.checkCancellation()
            if UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") {
                status = "扫描健康记录变化（可优先同步所选记录）"
                do { try await coordinator.discoverChanges() }
                catch is CancellationError { throw CancellationError() }
                catch { self.error = "部分健康变化仍待发现；原始日志和同步位置已保留。" }
                try? await coordinator.observeDiscovery { [weak self] in await self?.discoveryWake() }
                try? await coordinator.observeDaily { [weak self] in await self?.discoveryWake() }
                try await coordinator.discoverDaily()
                apply(await store.snapshot())
            }
            try Task.checkCancellation()
            status = "接续已保存的上传与分析状态"
            try await coordinator.recoverPending(secret: secret, transfers: transfers)
            if UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") {
                do { try await coordinator.syncDaily(secret: secret, transfers: transfers) }
                catch is CancellationError { throw CancellationError() }
                catch { self.error = "日常健康同步尚未完成；本地批次与同步位置已保留。" }
            }
            apply(await store.snapshot())
            try Task.checkCancellation()
            status = "交付已发现的健康变化"
            try await coordinator.syncDiscovered(secret: secret, transfers: transfers)
            try Task.checkCancellation()
            let snapshot = await store.snapshot()
            apply(snapshot)
            if let failure = snapshot.pages.compactMap(\.error).first {
                error = UserFacingText.error(failure.code)
                status = "仍有待恢复上传；下次启动会继续"
            } else if pending > 0 {
                status = "仍有 \(pending) 条上传待恢复"
            } else if discoveryRemaining > 0 || discoveryQueries > 0 {
                status = "本轮检查完成；健康变化扫描将继续"
            } else if pendingChanges > 0 {
                status = "本轮检查完成；仍有健康变化待交付"
            } else if (snapshot.tasks ?? [:]).values.contains(where: { $0.phase == .failed && $0.errorCode == "interrupted" }) {
                status = "有记录需要重新准备；请在“健康数据同步”中重试"
            } else {
                status = "没有待恢复的上传"
            }
            // Protocol 4 uploads the recorded run itself; there is no separate
            // completeness proof to schedule after delivery.
            completenessState = nil
        } catch let failure as SyncFailure {
            error = UserFacingText.syncFailure(failure); status = "恢复失败；本地状态已保留"
        } catch let failure as UploadError {
            error = UserFacingText.error(failure.code); status = "恢复中断；本地状态已保留"
        } catch is CancellationError { status = "已暂停；本地状态已保留" }
        catch { self.error = "恢复待处理上传失败；本地状态已保留"; status = "需要检查" }
        let snapshot = await store.snapshot()
        apply(snapshot)
        if !Task.isCancelled { scheduleRecovery(snapshot) }
    }
    private func discoveryWake() { recoverPending() }
    private func scheduleRecovery(_ snapshot: LocalState, resumeDiscovery: Bool = false) {
        recoveryWake?.cancel()
        let pending = (snapshot.tasks ?? [:]).values.filter { $0.phase == .uploading && $0.bundleFile != nil }
        var dates = pending.map { $0.nextAttemptAt ?? Date() }
        // A cancelled read may have retained hasMore=false from its previous
        // successful pass. Recheck once after user work even without new facts.
        if resumeDiscovery, UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") {
            dates.append(Date().addingTimeInterval(2))
        }
        if (snapshot.discovery?.cycleRemaining ?? 0) > 0 { dates.append(Date().addingTimeInterval(2)) }
        for flow in (snapshot.daily ?? [:]).values {
            if let pending = flow.pending { dates.append(pending.nextAttempt) }
            else if flow.error == nil && flow.discoveryDue(wakeGeneration: snapshot.dailyWakeGeneration ?? 0) { dates.append(Date().addingTimeInterval(2)) }
        }
        if let date = snapshot.vo2?.nextOpportunity(now: Date(), wakeGeneration: snapshot.vo2WakeGeneration ?? 0) { dates.append(date) }
        // Foreground opportunity only; background suspension cancels this timer.
        if UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") { dates.append(Date().addingTimeInterval(900)) }
        if snapshot.discovery?.flows.values.contains(where: { $0.hasMore && $0.error == nil }) == true { dates.append(Date().addingTimeInterval(2)) }
        if let discovery = snapshot.discovery {
            let known = discovery.workoutArchives.count
            let scoped = Set(discovery.flows.values.compactMap { $0.query.workoutID }).count
            if scoped < known { dates.append(Date().addingTimeInterval(2)) }
            dates += discovery.work.values.filter(\.hasAutomaticDeliveryWork).map { $0.nextAttemptAt ?? Date().addingTimeInterval(2) }
        }
        guard let date = dates.min() else { return }
        recoveryWake = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(max(1, date.timeIntervalSinceNow))) }
            catch { return }
            self?.recoverPending()
        }
    }
    func suspendForegroundWork() {
        task?.cancel(); syncScheduler.cancel(); recoveryWake?.cancel()
        if let coordinator { Task { await coordinator.stopAnalysisFollowups() } }
    }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        Task { @MainActor in
            guard let transfers = await SyncModel.shared.backgroundTransfers(identifier: identifier) else { completionHandler(); return }
            transfers.handleEvents {
                Task { @MainActor in
                    if SyncModel.shared.isActiveBackground(identifier) { SyncModel.shared.recoverPending() }
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
                    if ProcessInfo.processInfo.environment["RAI_UA1_R_COMPLETENESS"] != nil { await model.runUA1RCompletenessAcceptance(); return }
                    if ProcessInfo.processInfo.environment["RAI_UA1_R_VO2"] != nil { await model.runUA1RVO2Acceptance(); return }
                    if DailyDeviceDiagnostics.enabled { await model.runDailyDeviceAcceptance(); return }
                    if ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { await model.runPhase3Acceptance(phase: 4); return }
                    if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" { await model.runPhase3Acceptance(); return }
                    #endif
                    model.recoverPending(discoverDaily: true)
                }
                .onChange(of: phase) { _, phase in
                    if phase == .active {
                        #if DEBUG
                        if ProcessInfo.processInfo.environment["RAI_LOCAL_DEVICE_ACCEPTANCE"] == "1" { return }
                        if ProcessInfo.processInfo.environment["RAI_UA1_R_COMPLETENESS"] != nil { return }
                        if ProcessInfo.processInfo.environment["RAI_UA1_R_VO2"] != nil { return }
                        if DailyDeviceDiagnostics.enabled {
                            Task { try? await DailyDeviceDiagnostics.shared.event("scene_active") }
                            model.recoverPending(discoverDaily: true)
                            return
                        }
                        if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" || ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { return }
                        #endif
                        model.recoverPending(discoverDaily: true)
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
                if let proof = model.completenessState { LabeledContent("历史完整性", value: proof) }
                if model.completenessBlocked > 0 {
                    LabeledContent("待重新核验记录", value: String(model.completenessBlocked))
                    Button("重试历史完整性核验") { model.retryCompleteness() }.disabled(!model.canStartWorkoutSync)
                }
                Button("同步日常健康") { model.syncDailyNow() }.disabled(!model.canStartWorkoutSync)
                Text("同步 Apple 健康中的心率变异性、静息心率与睡眠记录；读取可见性由系统管理。").font(.caption)
                LabeledContent("待恢复上传任务", value: String(model.pending))
                LabeledContent("健康变化待扫描记录", value: String(model.discoveryRemaining))
                LabeledContent("健康变化未完成查询", value: String(model.discoveryQueries))
                LabeledContent("健康变化待交付记录", value: String(model.pendingChanges))
                if model.legacyPending > 0 {
                    LabeledContent("历史遗留页", value: "\(model.legacyPending) 页（旧协议，不会上传）")
                }
                if model.parkedAssociations > 0 {
                    LabeledContent("历史关联待补全", value: "\(model.parkedAssociations) 页（旧协议，不会上传）")
                }
                if let date = model.lastSync { LabeledContent("上次同步") { Text(date, style: .time) } }
                if let date = model.lastACK { LabeledContent("上次确认") { Text(date, style: .time) } }
                if let error = model.error { Text(error).foregroundStyle(.red).font(.footnote) }
                NavigationLink("健康数据同步（单条记录）") { WorkoutSyncView(model: model) }
                Text("每条跑步记录独立读取、独立上传、独立确认；不会把多条记录合并为一个上传包。").font(.caption)
                Text("启动或回到前台会分轮扫描健康变化、接续已保存的上传，并更新已选择记录。手动同步所选记录会优先执行；原始日志和上传包会保留。").font(.caption)
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
