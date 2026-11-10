import Foundation
import HealthKit
import OSLog
import SyncCore
#if DEBUG
import UIKit
#endif

enum Metrics {
    static let log = Logger(subsystem: "com.rai.v2", category: "sync-statistics")
    static func value(_ name: String, _ value: Double) { log.info("\(name, privacy: .public)=\(value, privacy: .public)") }
    static func milliseconds(_ name: String, since: Date) { value(name, Date().timeIntervalSince(since) * 1000) }
}

/// One Workout's foreground result. Success means the server durably accepted
/// the raw bytes; it does not wait for projection or analysis.
struct WorkoutUploadOutcome: Sendable, Equatable {
    let workoutID: String
    let acceptedAt: Date?
    let errorCode: String?
    var succeeded: Bool { errorCode == nil }
}

/// One HKWorkout = one protocol-6 payload built and sent in the foreground.
/// There is no local Workout upload queue, status pre-read, automatic retry,
/// background transfer or analysis polling in this path.
actor SyncCoordinator {
    let store: OutboxStore
    let health: any HealthKitReading
    private let session: URLSession
    private let sessionStore: SyncSessionStore?
    private let sessionID: String?
    private var workoutBatchRunning = false
    private var dailyRunning = false
    private var supportsDaily = false
    private var vo2Running = false
    private var vo2Capabilities: JSON = .null

    /// Observer work is local and bounded. Offline/pending pages keep their
    /// old committed anchor and exact immutable bytes until the server ACK.
    func discoverDaily() async throws {
        if let sessionStore, let sessionID { _ = try await sessionStore.requireActive(sessionID) }
        guard !dailyRunning else { return }
        dailyRunning = true; defer { dailyRunning = false }
        for type in HealthKitClient.dailyTypes {
            try Task.checkCancellation()
            do { try await discoverDailyPage(type) }
            catch is CancellationError { throw CancellationError() }
            catch {
                let failure: String
                switch error { case SyncFailure.invalid(let code), SyncFailure.localState(let code): failure = code; default: failure = "daily_query_failed" }
                try await store.dailyFailure(type.identifier, code: failure)
            }
        }
        try await discoverVO2()
    }
    private func discoverDailyPage(_ type: HKSampleType) async throws {
        let flow = try await store.dailyFlow(type.identifier)
        let snapshot = await store.snapshot(), wakeGeneration = snapshot.dailyWakeGeneration ?? 0
        guard flow.discoveryDue(wakeGeneration: wakeGeneration) else { return }
        guard flow.error != "invalid_healthkit_anchor" else { throw SyncFailure.localState("invalid_healthkit_anchor") }
        guard await store.snapshot().scope != nil else { return }
        var limit = DailyHealth.maxChanges
        while true {
            try Task.checkCancellation()
            #if DEBUG
            let queryStarted = Date()
            #endif
            let page = try await health.readDaily(type: type, anchor: flow.anchor, start: Date(timeIntervalSince1970: Double(flow.bootstrapStartUs) / 1_000_000), limit: limit)
            #if DEBUG
            let queryMs = Date().timeIntervalSince(queryStarted) * 1000, normalizationStarted = Date()
            #endif
            let facts = try page.samples.map { try DailyHealthNormalizer.fact($0) }
            #if DEBUG
            let normalizationMs = Date().timeIntervalSince(normalizationStarted) * 1000
            #endif
            do {
                try await store.stageDaily(type: type.identifier, previousAnchor: flow.anchor, candidateAnchor: page.anchor,
                    facts: facts, deleted: page.deleted.map { $0.uuid.uuidString.lowercased() }, observedAt: Date(), hasMore: page.isFull, wakeGeneration: wakeGeneration)
                #if DEBUG
                if DailyDeviceDiagnostics.enabled {
                    var sources: [String: Int] = [:], categories: [String: Int] = [:]
                    let names = ["inBed", "asleepUnspecified", "awake", "core", "deep", "REM"]
                    for fact in facts {
                        let source = HealthSourcePolicy.classification(fact["source"]); sources[source, default: 0] += 1
                        if let category = fact["category"].integer, (0..<6).contains(category) { categories[names[Int(category)], default: 0] += 1 }
                    }
                    try? await DailyDeviceDiagnostics.shared.event("query_staged", fields: ["type": .string(type.identifier),
                        "additions": .int(Int64(page.samples.count)), "deletions": .int(Int64(page.deleted.count)), "hasMore": .bool(page.isFull),
                        "usedCommittedAnchor": .bool(flow.anchor != nil), "candidateAnchorProduced": .bool(!page.anchor.isEmpty),
                        "queryMs": .number(queryMs), "normalizationMs": .number(normalizationMs),
                        "sourceClasses": .object(sources.mapValues { .int(Int64($0)) }), "categories": .object(categories.mapValues { .int(Int64($0)) }),
                        "sourceRevisionCount": .int(Int64(facts.filter { $0["source"]["version"] != .null }.count)),
                        "deviceContextCount": .int(Int64(facts.filter { $0["source"]["device"] != .null }.count)),
                        "identityPreserved": .bool(zip(page.samples, facts).allSatisfy { $0.uuid.uuidString.lowercased() == $1["uuid"].text }),
                        "intervalsPreserved": .bool(zip(page.samples, facts).allSatisfy { Normalizer.us($0.startDate) == $1["startUs"].integer && Normalizer.us($0.endDate) == $1["endUs"].integer })])
                    try? await DailyDeviceDiagnostics.shared.snapshot("page_staged", store: store)
                }
                #endif
                return
            } catch SyncFailure.invalid("batch_too_large") where limit > 1 { limit = max(1, limit / 2) }
        }
    }
    func observeDaily(_ onWake: @escaping @Sendable () async -> Void = {}) async throws {
        try await health.observeDaily { [weak self] in
            guard let self else { return }
            // Persist the wake hint before returning HealthKit's completion.
            // Exact discovery can outlive the callback; suspension leaves a
            // durable hint and the old committed anchors for foreground retry.
            #if DEBUG
            let hintDurable: Bool
            do { try await self.store.markDailyWake(); hintDurable = true } catch { hintDurable = false }
            if DailyDeviceDiagnostics.enabled {
                let appState = await MainActor.run { UIApplication.shared.applicationState == .background ? "background" : UIApplication.shared.applicationState == .active ? "active" : "inactive" }
                try? await DailyDeviceDiagnostics.shared.event("observer_wake_after_journal", fields: ["durable": .bool(hintDurable), "appState": .string(appState)])
                try? await DailyDeviceDiagnostics.shared.snapshot("observer_wake", store: self.store)
            }
            #else
            try? await self.store.markDailyWake()
            #endif
            Task { try? await self.discoverDaily(); await onWake() }
        }
        try await health.observeVO2 { [weak self] in
            guard let self else { return }
            try? await self.store.markVO2Wake()
            Task { try? await self.discoverVO2(); await onWake() }
        }
    }
    func syncDaily(secret: ClientConfiguration.Secret, transfers: any FileUploader) async throws {
        guard !dailyRunning else { return }
        dailyRunning = true; defer { dailyRunning = false }
        let api: RAIAPIClient
        do {
            (api, _) = try await context(secret: secret, transfers: transfers, metrics: SyncMetrics { name, value in Metrics.value(name, value) })
            guard supportsDaily || VO2Max.supported(vo2Capabilities) else { throw SyncFailure.invalid("unsupported_daily_contract") }
        } catch {
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            try await store.deferDailyRecovery(error)
            try await store.deferVO2Recovery(error)
            throw error
        }
        if !supportsDaily { try await syncVO2(api: api); return }
        let delivery = DailyHealthDelivery(store: store, api: api)
        // Finite foreground opportunity. Other types proceed after a per-type
        // failure; pages beyond the budget resume on the next opportunity.
        for name in await store.healthDeliveryOrder() {
            if name == VO2Max.type { try await syncVO2(api: api); continue }
            guard let type = HealthKitClient.dailyTypes.first(where: { $0.identifier == name }) else { continue }
            for _ in 0..<4 {
                try Task.checkCancellation()
                do {
                    if try await store.dailyFlow(type.identifier).pending != nil {
                        guard try await deliverDaily(delivery, type: type.identifier) else { break }
                    } else { try await discoverDailyPage(type) }
                    if try await store.dailyFlow(type.identifier).pending != nil { _ = try await deliverDaily(delivery, type: type.identifier) }
                    if !(try await store.dailyFlow(type.identifier).hasMore) { break }
                } catch is CancellationError { throw CancellationError() }
                catch {
                    if try await store.dailyFlow(type.identifier).error == nil {
                        let failure: String
                        switch error { case SyncFailure.invalid(let code), SyncFailure.localState(let code): failure = code; default: failure = (error as? UploadError)?.code ?? "daily_sync_failed" }
                        try await store.dailyFailure(type.identifier, code: failure)
                    }
                    break
                }
            }
        }
        try await store.finishDailyWakeIfCaughtUp()
    }

    private func deliverDaily(_ delivery: DailyHealthDelivery, type: String) async throws -> Bool {
        #if DEBUG
        let started = Date()
        do {
            let delivered = try await delivery.deliver(type)
            if DailyDeviceDiagnostics.enabled {
                try? await DailyDeviceDiagnostics.shared.event("delivery_returned", fields: ["type": .string(type), "delivered": .bool(delivered), "wallMs": .number(Date().timeIntervalSince(started) * 1000)])
                try? await DailyDeviceDiagnostics.shared.snapshot("after_delivery", store: store)
            }
            return delivered
        } catch {
            if DailyDeviceDiagnostics.enabled { try? await DailyDeviceDiagnostics.shared.event("delivery_failed", fields: ["type": .string(type), "httpStatus": .int(Int64((error as? UploadError)?.status ?? 0)), "wallMs": .number(Date().timeIntervalSince(started) * 1000)]) }
            throw error
        }
        #else
        return try await delivery.deliver(type)
        #endif
    }

    init(store: OutboxStore, health: any HealthKitReading = HealthKitClient(), session: URLSession = .shared, sessionStore: SyncSessionStore? = nil, sessionID: String? = nil) {
        self.store = store; self.health = health; self.session = session
        self.sessionStore = sessionStore; self.sessionID = sessionID
    }

    func authorize() async throws { try await health.authorize() }
    func recentWorkouts(limit: Int = 100) async throws -> [HealthWorkoutSummary] { try await health.recentWorkouts(limit: limit) }
    func workoutPage(before: Date? = nil, limit: Int = 100) async throws -> [HealthWorkoutSummary] {
        try await health.workoutPage(before: before, limit: limit)
    }

    /// Wakes the app when HealthKit saves a Workout. The handler only decides
    /// whether an automatic round is due; nothing is read inside the callback.
    func observeWorkouts(_ handler: @escaping @Sendable () async -> Void) async throws { try await health.observeWorkouts(handler) }

    /// What the server holds for these UUIDs, one request per 200.
    func workoutStatuses(_ ids: [String], secret: ClientConfiguration.Secret,
                         transfers: any FileUploader) async throws -> [String: WorkoutRemoteStatus] {
        guard !ids.isEmpty else { return [:] }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let (api, _) = try await context(secret: secret, transfers: transfers, metrics: metrics)
        return try await WorkoutIngestDelivery(api: api).statuses(ids)
    }

    private func context(secret: ClientConfiguration.Secret, transfers: any FileUploader, metrics: SyncMetrics) async throws -> (RAIAPIClient, Scope) {
        let api = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token, session: session, uploader: transfers, metrics: metrics)
        let hello = try await api.call("GET", "/v2/handshake")
        if let sessionStore, let sessionID {
            try await sessionStore.validateActiveHandshake(sessionID, handshake: hello)
        }
        let supported = Set(hello["capabilities"]["sampleTypes"].list.compactMap(\.text))
        // Protocol 6 is the only Workout payload this client can produce or
        // deliver. A server that still advertises another version is not
        // compatible; there is no downgrade and no legacy upload path.
        guard hello["protocolVersion"].integer == 3, let service = hello["serviceID"].text,
              let dataset = hello["datasetID"].text, let epoch = hello["datasetEpoch"].text,
              hello["datasetStatus"].text == "active",
              hello["capabilities"]["workoutPayloadVersion"].integer == 6,
              hello["capabilities"]["workoutUUIDIngestVersion"].integer == 1,
              !supported.isEmpty,
              HealthKitClient.quantityTypes.allSatisfy({ supported.contains($0.identifier) }) else {
            throw SyncFailure.invalid("unsupported_handshake")
        }
        supportsDaily = hello["capabilities"]["dailyFactsVersion"].integer == 1 && Set(hello["capabilities"]["dailyFactTypes"].list.compactMap(\.text)) == Set(DailyHealth.types)
        vo2Capabilities = hello["capabilities"]
        let scope = Scope(serviceID: service, datasetID: dataset, datasetEpoch: epoch, collectorEpoch: secret.configuration.collectorEpoch)
        try await store.bind(scope)
        return (api, scope)
    }

    /// Uploads the selected Workouts in the foreground, one after another.
    /// The handshake happens once per batch; each Workout is read, built and
    /// sent exactly once. A per-Workout failure is reported through `progress`
    /// and does not stop the remaining items; only cancellation ends the round
    /// early. The returned outcomes are exactly the Workouts whose final
    /// result was reached before the call returned.
    func uploadWorkouts(_ ids: [String], secret: ClientConfiguration.Secret,
                        transfers: any FileUploader,
                        started: @escaping @MainActor @Sendable (String) -> Void = { _ in },
                        progress: @escaping @MainActor @Sendable (WorkoutUploadOutcome) -> Void = { _ in }) async throws -> [WorkoutUploadOutcome] {
        guard !ids.isEmpty else { return [] }
        guard !workoutBatchRunning else { throw SyncFailure.localState("sync_already_running") }
        workoutBatchRunning = true; defer { workoutBatchRunning = false }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let (api, scope) = try await context(secret: secret, transfers: transfers, metrics: metrics)
        let delivery = WorkoutIngestDelivery(api: api)
        var outcomes: [WorkoutUploadOutcome] = []
        outcomes.reserveCapacity(ids.count)
        for id in ids {
            try Task.checkCancellation()
            await started(id)
            let buildStarted = Date()
            do {
                let raw = try await RawStreamSyncEngine(health: health, scope: scope).build(id: id)
                metrics.value("v6_build_ms", Date().timeIntervalSince(buildStarted) * 1000)
                let receipt = try await delivery.acceptWithRetry(raw, workoutID: id, scope: scope)
                metrics.value("workout_sync_ms", Date().timeIntervalSince(buildStarted) * 1000)
                let outcome = WorkoutUploadOutcome(workoutID: id, acceptedAt: receipt.acceptedAt, errorCode: nil)
                outcomes.append(outcome)
                await progress(outcome)
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                if Task.isCancelled { throw CancellationError() }
                let outcome = WorkoutUploadOutcome(workoutID: id, acceptedAt: nil, errorCode: Self.uploadCode(error))
                outcomes.append(outcome)
                await progress(outcome)
            }
        }
        return outcomes
    }

    // Failure codes reach the workout detail row and the diagnostic report, so
    // a keyed metadata code is reduced to its stable prefix first.
    private nonisolated static func uploadCode(_ error: any Error) -> String {
        if let failure = error as? SyncFailure {
            switch failure {
            case .invalid(let code), .localState(let code): return UserFacingText.stableCode(code)
            case .receiptMismatch: return "receipt_mismatch"
            }
        }
        if let failure = error as? UploadError { return UserFacingText.stableCode(failure.code) }
        return "sync_failed"
    }

    private nonisolated func code(_ failure: SyncFailure) -> String {
        switch failure {
        case .invalid(let value), .localState(let value): return UserFacingText.stableCode(value)
        case .receiptMismatch: return "receipt_mismatch"
        }
    }
}


extension SyncCoordinator {
    func syncVO2Only(secret: ClientConfiguration.Secret, transfers: any FileUploader) async throws {
        guard !dailyRunning else { throw SyncFailure.localState("sync_already_running") }
        dailyRunning = true; defer { dailyRunning = false }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let (api, _) = try await context(secret: secret, transfers: transfers, metrics: metrics)
        try await syncVO2(api: api)
    }
    func discoverVO2() async throws {
        if let sessionStore, let sessionID { _ = try await sessionStore.requireActive(sessionID) }
        guard !vo2Running else { return }; vo2Running = true; defer { vo2Running = false }
        do { try await discoverVO2Page() }
        catch is CancellationError { throw CancellationError() }
        catch {
            let code: String
            switch error { case SyncFailure.invalid(let c), SyncFailure.localState(let c): code = c; default: code = "vo2_query_failed" }
            try await store.vo2Failure(code: code)
        }
    }
    private func discoverVO2Page() async throws {
        let flow = try await store.vo2Flow(), snapshot = await store.snapshot()
        if snapshot.scope != nil, let blocked = flow.blocked, flow.pending == nil,
           blocked.category == "unsupported_metadata",
           (blocked.parserRevision ?? 1) < VO2Max.nativeMetadataParserRevision {
            let archive = try Data(contentsOf: await store.fileURL(blocked.file))
            guard Wire.hash(archive) == blocked.hash else { throw SyncFailure.localState("vo2_payload_corrupt") }
            let page: (facts: [JSON], deleted: [String])
            do { page = try VO2MaxNormalizer.blockedPage(archive) }
            catch {
                try await store.recordBlockedVO2ParserAttempt(expectedHash: blocked.hash, code: VO2Max.failureCode(error))
                throw error
            }
            try await store.stageBlockedVO2(expectedHash: blocked.hash, facts: page.facts, deleted: page.deleted, observedAt: Date())
            return
        }
        guard snapshot.scope != nil, flow.discoveryDue(wakeGeneration: snapshot.vo2WakeGeneration ?? 0) else { return }
        guard flow.error != "invalid_healthkit_anchor" else { throw SyncFailure.localState("invalid_healthkit_anchor") }
        var limit = VO2Max.maxChanges
        while true {
            try Task.checkCancellation()
            let page = try await health.readVO2(anchor: flow.anchor, start: Date(timeIntervalSince1970: Double(flow.bootstrapStartUs) / 1_000_000), limit: limit)
            do {
                let facts = try page.samples.map { try VO2MaxNormalizer.fact($0) }
                try await store.stageVO2(previousAnchor: flow.anchor, candidateAnchor: page.anchor, facts: facts,
                    deleted: page.deleted.map { $0.uuid.uuidString.lowercased() }, observedAt: Date(), hasMore: page.isFull,
                    wakeGeneration: snapshot.vo2WakeGeneration ?? 0)
                return
            } catch SyncFailure.invalid("batch_too_large") where limit > 1 { limit = max(1, limit / 2) }
            catch {
                let category: String
                switch error { case SyncFailure.invalid(let c): category = c; default: throw error }
                // Protected local SDK archive only; never emitted to diagnostics.
                let archive = try NSKeyedArchiver.archivedData(withRootObject: (page.samples as [NSObject]) + (page.deleted as [NSObject]), requiringSecureCoding: true)
            // Whole-page archive: the completeness and VO2 streams still store
            // one blob per page. Counted apart from the per-sample meter.
            SampleArchiveMeter.recordObjectArchive()
                try await store.blockVO2(previousAnchor: flow.anchor, candidateAnchor: page.anchor, archive: archive, category: category, hasMore: page.isFull)
                throw error
            }
        }
    }
    private func syncVO2(api: RAIAPIClient) async throws {
        guard !vo2Running else { return }; vo2Running = true; defer { vo2Running = false }
        guard VO2Max.supported(vo2Capabilities) else {
            try await store.vo2Failure(code: "unsupported_capability"); return
        }
        let delivery = VO2MaxDelivery(store: store, api: api)
        // Bounded pages; failures remain local to this flow. Existing Daily
        // and Workout opportunities continue on the same foreground scheduler.
        for _ in 0..<4 {
            do {
                if try await store.vo2Flow().pending == nil { try await discoverVO2Page() }
                guard try await delivery.deliver(capabilities: vo2Capabilities) else { break }
                if !(try await store.vo2Flow().hasMore) { break }
            } catch is CancellationError { throw CancellationError() }
            catch {
                let code: String
                switch error { case SyncFailure.invalid(let c), SyncFailure.localState(let c): code = c; default: code = (error as? UploadError)?.code ?? "vo2_sync_failed" }
                if await store.snapshot().vo2?.error == nil { try await store.vo2Failure(code: code) }
                break
            }
        }
    }
}
