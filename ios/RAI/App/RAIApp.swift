import SwiftUI
import UIKit
import SyncCore

@MainActor @Observable
final class SyncModel {
    static let shared = SyncModel()
    var authorization = UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") ? "已请求读取权限；数据是否可用由系统管理" : "尚未请求"
    var status = "空闲"
    var pending = 0
    var legacyPending = 0
    var parkedAssociations = 0
    var lastACK: Date?
    var lastSync: Date?
    var error: String?
    var busy = false
    var workouts: [HealthWorkoutSummary] = []
    var tasks: [String: WorkoutSyncTask] = [:]
    var serverStatuses: [String: String] = [:]
    var serverPresent: Set<String> = []
    var selected: Set<String> = []
    var loadingWorkouts = false
    var currentWorkoutID: String?
    var workoutsRefreshedAt: Date?
    #if DEBUG
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
            try await coordinator.discoverChanges(workoutIDs: [id])
            let first = await store.discoverySnapshot()
            try await coordinator.discoverChanges(workoutIDs: [id])
            let second = await store.discoverySnapshot()
            report["discoveryEventCount"] = .int(Int64(second.events.count))
            report["independentAnchors"] = .int(Int64(second.flows.count))
            report["outOfScopeAnchors"] = .int(Int64(second.flows.values.filter { $0.query.workoutID != id }.count))
            report["unchangedReadCreatesNoWork"] = .bool(first.events.count == second.events.count && first.work[id]?.generation == second.work[id]?.generation)
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
    private(set) var transfers: BackgroundTransfers?
    private var coordinator: SyncCoordinator?
    private var store: OutboxStore?
    private var task: Task<Void, Never>?
    init() {
        do {
            let directory = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true).appendingPathComponent("RAIV2Sync", isDirectory: true)
            let store = try OutboxStore(directory: directory)
            self.store = store; self.coordinator = SyncCoordinator(store: store)
            self.transfers = BackgroundTransfers(directory: directory)
            #if DEBUG
            // Device-acceptance bootstrap only. The value is supplied through
            // the launch environment by the local acceptance runner and is
            // never written to logs, source, build settings or evidence.
            if let raw = ProcessInfo.processInfo.environment["RAI_ACCEPTANCE_CONFIG_JSON"],
               let data = raw.data(using: .utf8),
               let value = try? JSONDecoder().decode(AcceptanceConfig.self, from: data) {
                try? ClientConfiguration.save(baseURL: value.baseURL, collectorEpoch: value.collectorEpoch, token: value.token)
            }
            #endif
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
                // Observer registration is best-effort; it never substitutes
                // for the durable anchored read that follows.
                try? await coordinator.observeDiscovery { [weak self] in await self?.discoveryWake() }
                do { try await coordinator.discoverChanges(); if let store { apply(await store.snapshot()) } }
                catch { self.error = "健康变化尚未完成发现；原始日志和同步位置已保留。" }
            } catch { self.error = "健康数据授权请求失败。" }
        }
    }
    func loadWorkouts() async {
        guard !loadingWorkouts, let coordinator, let store else { return }
        loadingWorkouts = true; defer { loadingWorkouts = false }
        do {
            _ = try? await store.purgeLegacyPages()
            try await store.reconcileTasks()
            apply(await store.snapshot())
            workouts = try await coordinator.recentWorkouts(limit: 100)
            workoutsRefreshedAt = Date()
            if UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") {
                // UI refresh also advances the durable anchored journals; the
                // recent-workout list itself is only a display projection.
                try? await coordinator.discoverChanges()
                apply(await store.snapshot())
            }
            await refreshServerStatuses()
            if workouts.isEmpty { status = "HealthKit 中还没有可见的跑步记录" }
            error = nil
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
        guard !busy, let store else { return }
        Task {
            let task = await store.task(id)
            let nonResumable = ["sample_identity_conflict", "workout_identity_conflict",
                                "series_identity_conflict", "route_identity_conflict",
                                "sample_deleted", "route_deleted", "workout_deleted",
                                "workout_not_visible", "receipt_mismatch", "bundle_too_large"]
            let canResume = task?.bundleFile != nil && task?.bundleSHA256 != nil &&
                task?.bundleBoundary != nil && !nonResumable.contains(task?.errorCode ?? "")
            await runWorkouts([id], forceNewBundle: !canResume)
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
        guard !busy, !ids.isEmpty else { return }
        task?.cancel()
        task = Task { await runWorkouts(ids, forceNewBundle: true) }
    }
    private func runWorkouts(_ ids: [String], forceNewBundle: Bool) async {
        guard !busy, let coordinator, let store, let transfers else { return }
        busy = true; error = nil; currentWorkoutID = nil
        UIApplication.shared.isIdleTimerDisabled = true
        defer {
            busy = false; currentWorkoutID = nil
            UIApplication.shared.isIdleTimerDisabled = false
        }
        guard let secret = try? ClientConfiguration.load() else {
            self.error = UserFacingText.error("configure_development_api_first"); return
        }
        var failures = 0
        for (index, id) in ids.enumerated() {
            if Task.isCancelled { status = "已暂停；本地状态已保留"; break }
            currentWorkoutID = id
            var round = 0
            var stopped = false
            while !Task.isCancelled && !stopped {
                round += 1
                status = round == 1
                    ? "正在同步第 \(index + 1)/\(ids.count) 条跑步记录"
                    : "正在继续第 \(index + 1)/\(ids.count) 条（第 \(round) 轮）"
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
                    status = "已暂停；本地状态已保留"; break
                } catch let failure as UploadError {
                    failures += 1; error = UserFacingText.error(failure.code); break
                } catch let failure as SyncFailure {
                    failures += 1; error = UserFacingText.syncFailure(failure); break
                } catch {
                    failures += 1; self.error = "该条记录同步失败，原文件与上传身份已保留。"; break
                }
                let snapshot = await store.snapshot()
                apply(snapshot)
                guard let current = snapshot.tasks?[id],
                      current.phase == .uploading || current.phase == .preparing else { break }
                if round >= 120 { self.error = "该记录仍未完成，请稍后重试"; break }
                // A retryable page failure (429/503) is persisted with a
                // nextAttempt; wait for it instead of leaving the row stuck at
                // "上传中" with no scheduler.
                let next = current.nextAttemptAt
                let wait = max(next?.timeIntervalSinceNow ?? 1, 1)
                do { try await Task.sleep(for: .seconds(wait)) }
                catch { stopped = true }
            }
        }
        if !Task.isCancelled { status = failures == 0 ? "所选记录已独立同步" : "部分记录需要处理，其余记录未受影响" }
        await refreshServerStatuses()
        scheduleRecovery(await store.snapshot())
    }
    private func apply(_ snapshot: LocalState) {
        // A 409 association_unavailable page waits for its sample stream to
        // catch up. It is durable work, but it is not a fact-sync failure and
        // must not keep the product's "当前任务待恢复页" pinned forever.
        // Bundle uploads have no page queue. Count the durable task phases
        // that can still be resumed; legacy pages remain visible only to the
        // old recovery path and must not masquerade as current work.
        pending = (snapshot.tasks ?? [:]).values.filter { [.preparing, .uploading].contains($0.phase) }.count
        legacyPending = snapshot.pages.count
        parkedAssociations = snapshot.pages.filter { $0.error?.code == "association_unavailable" }.count
        lastACK = snapshot.lastACK; lastSync = snapshot.lastSync
        tasks = snapshot.tasks ?? [:]
    }
    /// Automatic recovery only. It never reads HealthKit, never creates a
    /// page and never starts a Workout task; facts are produced exclusively by
    /// syncWorkouts -> SyncCoordinator.syncWorkout.
    private var recoveryWake: Task<Void, Never>?
    func recoverPending() {
        if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" || ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { return }
        guard !busy else { return }
        task?.cancel()
        task = Task { await performRecovery() }
    }
    func performRecovery() async {
        if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" || ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { return }
        guard !busy, let coordinator, let store, let transfers else { return }
        busy = true; error = nil; status = "恢复待处理上传"
        defer { busy = false }
        do {
            guard let secret = try? ClientConfiguration.load() else { status = "未配置同步"; return }
            if UserDefaults.standard.bool(forKey: "healthAuthorizationRequested") {
                do { try await coordinator.discoverChanges() }
                catch is CancellationError { throw CancellationError() }
                catch { self.error = "部分健康变化仍待发现；原始日志和同步位置已保留。" }
                try? await coordinator.observeDiscovery { [weak self] in await self?.discoveryWake() }
            }
            try await coordinator.recoverPending(secret: secret, transfers: transfers)
            try await coordinator.syncDiscovered(secret: secret, transfers: transfers)
            let snapshot = await store.snapshot()
            apply(snapshot)
            if let failure = snapshot.pages.compactMap(\.error).first {
                error = UserFacingText.error(failure.code)
                status = "仍有待恢复上传；下次启动会继续"
            } else if pending > 0 {
                status = "仍有 \(pending) 条上传待恢复"
            } else if (snapshot.tasks ?? [:]).values.contains(where: { $0.phase == .failed && $0.errorCode == "interrupted" }) {
                status = "有记录需要重新准备；请在“健康数据同步”中重试"
            } else {
                status = "没有待恢复的上传"
            }
        } catch let failure as SyncFailure {
            error = UserFacingText.syncFailure(failure); status = "恢复失败；本地状态已保留"
        } catch let failure as UploadError {
            error = UserFacingText.error(failure.code); status = "恢复中断；本地状态已保留"
        } catch is CancellationError { status = "已暂停；本地状态已保留" }
        catch { self.error = "恢复待处理上传失败；本地状态已保留"; status = "需要检查" }
        scheduleRecovery(await store.snapshot())
    }
    private func discoveryWake() { recoverPending() }
    private func scheduleRecovery(_ snapshot: LocalState) {
        recoveryWake?.cancel()
        let pending = (snapshot.tasks ?? [:]).values.filter { $0.phase == .uploading && $0.bundleFile != nil }
        var dates = pending.map { $0.nextAttemptAt ?? Date() }
        if (snapshot.discovery?.cycleRemaining ?? 0) > 0 { dates.append(Date().addingTimeInterval(2)) }
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
    func suspendForegroundWork() { task?.cancel(); recoveryWake?.cancel() }
}

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        guard identifier == BackgroundTransfers.identifier, let transfers = SyncModel.shared.transfers else { completionHandler(); return }
        // Reconnect to the stable session immediately. The delegate persists
        // each result before this callback drains/reconciles local receipts.
        transfers.handleEvents {
            Task { @MainActor in
                await SyncModel.shared.performRecovery()
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
                .task {
                    #if DEBUG
                    if ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { await model.runPhase3Acceptance(phase: 4); return }
                    if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" { await model.runPhase3Acceptance(); return }
                    #endif
                    model.recoverPending()
                }
                .onChange(of: phase) { _, phase in
                    if phase == .active {
                        #if DEBUG
                        if ProcessInfo.processInfo.environment["RAI_PHASE3_ACCEPTANCE"] == "1" || ProcessInfo.processInfo.environment["RAI_PHASE4_ACCEPTANCE"] == "1" { return }
                        #endif
                        model.recoverPending()
                    }
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
                LabeledContent("当前任务待恢复页", value: String(model.pending))
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
                Text("启动或回到前台只会重发已准备并保存的 bundle；不会重新读取 HealthKit。单条记录请在“健康数据同步”中操作。").font(.caption)
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


#if DEBUG
private struct AcceptanceConfig: Decodable {
    let baseURL: String
    let collectorEpoch: String
    let token: String
}
#endif
