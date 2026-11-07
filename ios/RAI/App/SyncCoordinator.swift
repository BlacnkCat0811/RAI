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

/// One HKWorkout = one immutable bundle upload. The exact bundle file is
/// persisted in the local task before the first HTTP request, so a retry,
/// relaunch or lost response always reuses the same bytes. There is no page,
/// batch, stream, series or association loop in this path.
actor SyncCoordinator {
    let store: OutboxStore
    let health: any HealthKitReading
    /// Capture producers allowed to run at the same time. Independent quantity
    /// types overlap; one stream's paging and anchor stay serial.
    let captureStreams: Int
    private let session: URLSession
    private let sessionStore: SyncSessionStore?
    private let sessionID: String?
    private var running = false
    /// Publication checks are asynchronous follow-ups, never part of the wait
    /// for a Workout's durable acknowledgement.
    private var analysisRefreshes: [String: Task<Void, Never>] = [:]
    private var supportsIntegrity = false
    private var syncProtocolV4 = false
    private var dailyRunning = false
    private var supportsDaily = false
    private var vo2Running = false
    private var completenessRunning = false
    private var vo2Capabilities: JSON = .null
    #if DEBUG
    private var acceptanceDiscoveryQueryCount = 0
    private var acceptanceAnchoredDiscoveryQueryCount = 0
    private var acceptanceSourceExcludedCount = 0
    private var acceptanceSourceUnclassifiedCount = 0
    private var acceptanceChildQuerySkipCount = 0
    func discoveryQueryEvidence() -> JSON {
        .object(["queries": .int(Int64(acceptanceDiscoveryQueryCount)),
            "queriesUsingCommittedAnchor": .int(Int64(acceptanceAnchoredDiscoveryQueryCount)),
            "sourceExcludedWorkouts": .int(Int64(acceptanceSourceExcludedCount)),
            "sourceUnclassifiedWorkouts": .int(Int64(acceptanceSourceUnclassifiedCount)),
            "skippedDetailedWorkouts": .int(Int64(acceptanceChildQuerySkipCount))])
    }
    #endif

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

    init(store: OutboxStore, health: any HealthKitReading = HealthKitClient(), session: URLSession = .shared, sessionStore: SyncSessionStore? = nil, sessionID: String? = nil, captureConcurrency: Int = CaptureConcurrencyPolicy.standard.streams) {
        self.store = store; self.health = health; self.session = session
        self.sessionStore = sessionStore; self.sessionID = sessionID
        self.captureStreams = max(1, captureConcurrency)
    }

    func authorize() async throws { try await health.authorize() }
    func recentWorkouts(limit: Int = 100) async throws -> [HealthWorkoutSummary] { try await health.recentWorkouts(limit: limit) }

    /// Offline discovery is independent of upload recovery. Queries are
    /// bounded and checkpointed; foreground/observer wakeups resume anchors.
    func discoverChanges(workoutIDs: [String]? = nil) async throws {
        if let sessionStore, let sessionID { _ = try await sessionStore.requireActive(sessionID) }
        guard !running else { return }
        running = true; defer { running = false }
        if workoutIDs == nil {
            let query = DiscoveryQuery(type: HKObjectType.workoutType().identifier)
            for _ in 0..<4 {
                try Task.checkCancellation()
                if !(try await discoverPage(query: query, type: HKObjectType.workoutType(), workout: nil)) { break }
            }
        }
        let snapshot = await store.discoverySnapshot()
        let known = workoutIDs ?? snapshot.workoutArchives.keys.sorted()
        guard !known.isEmpty else { return }
        let start = workoutIDs == nil ? snapshot.scanCursor % known.count : 0
        let ids = (0..<min(known.count, workoutIDs == nil ? 4 : known.count)).map { known[(start + $0) % known.count] }
        for id in ids {
            try Task.checkCancellation()
            do {
                let workoutQuery = DiscoveryQuery(type: HKObjectType.workoutType().identifier, workoutID: id)
                for _ in 0..<4 {
                    try Task.checkCancellation()
                    if !(try await discoverPage(query: workoutQuery, type: HKObjectType.workoutType(), workout: nil)) { break }
                }
                var visible = try await health.workout(id)
                if visible == nil, let file = snapshot.workoutArchives[id] {
                    visible = try NSKeyedUnarchiver.unarchivedObject(ofClass: HKWorkout.self, from: Data(contentsOf: await store.fileURL(file)))
                }
                guard let workout = visible else { continue } // visibility is not deletion
                // Source policy is decided from the root record, before any
                // detailed child query. An excluded Workout never starts
                // quantity/series/route collection; an undecidable one blocks
                // only itself and stays retryable. A Workout that already has
                // local work keeps collecting, so an explicit HealthKit
                // deletion of already-committed facts is never lost.
                let source = Normalizer.source(workout)
                let classification = HealthSourcePolicy.classification(source)
                let outcome = WorkoutSourcePolicy.outcome(classification)
                try await store.recordSourceClassification(workoutID: id, classification: classification,
                                                           evidence: WorkoutSourcePolicy.evidence(source))
                if outcome != WorkoutSourcePolicy.eligible, await store.task(id) == nil {
                    #if DEBUG
                    if outcome == WorkoutSourcePolicy.excluded { acceptanceSourceExcludedCount += 1 } else { acceptanceSourceUnclassifiedCount += 1 }
                    acceptanceChildQuerySkipCount += 1
                    #endif
                    continue
                }
                for type in HealthKitClient.quantityTypes + [HKSeriesType.workoutRoute()] {
                    do {
                        let scoped = DiscoveryQuery(type: type.identifier, workoutID: id)
                        for _ in 0..<4 {
                            try Task.checkCancellation()
                            if !(try await discoverPage(query: scoped, type: type, workout: workout)) { break }
                        }
                    }
                    catch is CancellationError { throw CancellationError() }
                    catch { /* this stream retains its anchor and error; others proceed */ }
                }
            } catch is CancellationError { throw CancellationError() }
            catch { try await store.discoveryFailure(id, code: "discovery_query_failed") }
        }
        if workoutIDs == nil { try await store.discoveryAdvanceCursor(ids.count, total: known.count) }
    }
    @discardableResult private func discoverPage(query requested: DiscoveryQuery, type: HKSampleType, workout: HKWorkout?) async throws -> Bool {
        let query = await store.activeDiscoveryQuery(requested)
        var flow = try await store.discoveryFlow(query)
        if let staged = flow.staged {
            try await store.promoteDiscovery(query, batchID: staged.id)
            flow = try await store.discoveryFlow(query)
        }
        // Invalid anchors remain quarantined. No catch path substitutes nil.
        if flow.error == "invalid_healthkit_anchor" { throw SyncFailure.localState("invalid_healthkit_anchor") }
        do {
            let page: HealthPage
            #if DEBUG
            acceptanceDiscoveryQueryCount += 1
            if flow.anchor != nil { acceptanceAnchoredDiscoveryQueryCount += 1 }
            #endif
            if type == HKObjectType.workoutType(), let id = query.workoutID { page = try await health.readWorkout(id: id, anchor: flow.anchor) }
            else { page = try await health.read(type: type, anchor: flow.anchor, workout: workout) }
            var events: [DiscoveryEvent] = []
            // The Workout root record stays a plain archive file: it is the
            // visible inventory (`workoutArchives`), the identity a later scan
            // uses to enumerate scoped streams, and the fallback when a root is
            // no longer visible. Its volume is one object per Workout, so it is
            // not where the per-sample cost lives.
            let rootType = type == HKObjectType.workoutType()
            // Child upserts are archived into bounded capture segments: one
            // sealed file per batch instead of one atomic file per sample. The
            // events are returned unstaged and committed together with the
            // journal and the anchor by `stageDiscovery`, so a crash before
            // that point leaves an unreferenced orphan, never a fact.
            for start in stride(from: 0, to: page.samples.count, by: 1000) {
                try Task.checkCancellation()
                let chunk = page.samples[start..<min(start + 1000, page.samples.count)]
                if rootType {
                    for sample in chunk {
                        let archive = try NSKeyedArchiver.archivedData(withRootObject: sample, requiringSecureCoding: true)
                        SampleArchiveMeter.recordObjectArchive()
                        let file = try await store.savePayload(archive, suffix: "healthkit.archive")
                        events.append(DiscoveryEvent(type: type.identifier, uuid: sample.uuid.uuidString.lowercased(),
                            deleted: false, archiveFile: file, archiveHash: Wire.hash(archive), workoutID: query.workoutID))
                    }
                    continue
                }
                var records: [CapturedFact] = []
                records.reserveCapacity(chunk.count)
                for sample in chunk {
                    // Normalized capture (P4): the fact is stored in the shape
                    // the bundle needs, so no per-sample archive is produced.
                    let fact = Normalizer.capturedFact(sample, type: type.identifier, workoutID: query.workoutID)
                    records.append(CapturedFact(type: type.identifier, uuid: fact.uuid, archive: try fact.encoded(),
                                                identityHash: fact.identityHash, encoding: .normalized))
                }
                let captured = try await store.captureFactSegment(type: type.identifier, workoutID: query.workoutID,
                                                                 records: records, publish: false)
                events.append(contentsOf: captured.events)
            }
            // Deletions keep their own archive file: a deletion event is a
            // tombstone marker, not a fact whose bytes are ever reused, and the
            // segment reference form is explicitly rejected for them.
            for object in page.deleted {
                try Task.checkCancellation()
                let archive = try NSKeyedArchiver.archivedData(withRootObject: object, requiringSecureCoding: true)
                SampleArchiveMeter.recordObjectArchive()
                let file = try await store.savePayload(archive, suffix: "healthkit.archive")
                events.append(DiscoveryEvent(type: type.identifier, uuid: object.uuid.uuidString.lowercased(), deleted: true,
                    archiveFile: file, archiveHash: Wire.hash(archive), workoutID: query.workoutID))
            }
            let batchID = try await store.stageDiscovery(query, previousAnchor: flow.anchor, anchor: page.anchor, events: events, hasMore: page.isFull)
            try await store.promoteDiscovery(query, batchID: batchID)
            return page.isFull
        } catch {
            // Preemption is not a failed discovery stream. Keep the previous
            // anchor and hasMore so the automatic scheduler can resume later.
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            let failure = (error as? SyncFailure).map(code) ?? (error is CancellationError ? "cancelled" : "healthkit_query_failed")
            try await store.discoveryError(query, code: failure)
            throw error
        }
    }
    func observeDiscovery(_ handler: @escaping @Sendable () async -> Void) async throws { try await health.observeChanges(handler) }

    /// Previously selected Workouts receive later facts through the same
    /// immutable bundle pipeline. New discoveries still respect user selection.
    func syncDiscovered(secret: ClientConfiguration.Secret, transfers: any FileUploader) async throws {
        guard !running else { return }
        running = true; defer { running = false }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let (api, scope) = try await context(secret: secret, transfers: transfers, metrics: metrics)
        let discovery = await store.discoverySnapshot()
        for (id, work) in discovery.work.sorted(by: { $0.key < $1.key }) where work.needsDelivery() {
            try Task.checkCancellation()
            do {
                if work.pendingDelete { try await deleteWorkout(id: id, api: api, scope: scope); continue }
                if await store.task(id)?.phase == .deleted {
                    try await deliverDeletedParentChildren(id: id, scope: scope, api: api)
                    continue
                }
                try await store.parkBundleForExplicitChildDeletion(id)
                if let task = await store.task(id), task.bundleFile != nil {
                    if task.phase == .failed { continue }
                    _ = try await WorkoutBundleDelivery(store: store, api: api).deliver(id)
                    continue
                }
                try await store.beginTask(id, resetBundle: true)
                try await performSync(id: id, api: api, scope: scope, transfers: transfers, metrics: metrics, forceNewBundle: true, recovery: false)
            } catch is CancellationError { throw CancellationError() }
            catch { try await store.discoveryFailure(id, code: (error as? SyncFailure).map(code) ?? (error as? UploadError)?.code ?? "discovery_work_failed") }
        }
    }

    private func deliverDeletedParentChildren(id: String, scope: Scope, api: RAIAPIClient) async throws {
        guard supportsIntegrity, let task = await store.task(id) else { throw SyncFailure.localState("unsupported_integrity_contract") }
        if task.bundleFile != nil { _ = try await WorkoutBundleDelivery(store: store, api: api).deliver(id); return }
        let discovery = await store.discoverySnapshot()
        let deletions = discovery.events.values.filter { $0.workoutID == id && $0.deleted && $0.type != HKObjectType.workoutType().identifier }.sorted { $0.id < $1.id }
        guard !deletions.isEmpty else { return }
        var original = task.originalWorkout
        if original == nil, let archive = discovery.workoutArchives[id], let workout = try NSKeyedUnarchiver.unarchivedObject(ofClass: HKWorkout.self, from: Data(contentsOf: await store.fileURL(archive))) { original = try Normalizer.integrityEvent(workout) }
        guard let original else { throw SyncFailure.localState("deleted_parent_workout_archive_missing") }
        let samples = deletions.filter { $0.type != HKSeriesType.workoutRoute().identifier }.map { JSON.object(["sampleType": .string($0.type), "sampleUUID": .string($0.uuid)]) }
        let routes = deletions.filter { $0.type == HKSeriesType.workoutRoute().identifier }.map(\.uuid)
        let raw = try WorkoutBundleAssembler.data(scope: scope, workoutID: id, workout: original, samples: [], series: [], routes: [], completeSampleTypes: [], completeRoutes: false, sampleDeletions: samples, routeDeletions: routes, deletionOnly: true)
        let boundary = "rai.boundary." + Wire.id(), hash = Wire.hash(raw)
        let file = try await store.savePayload(WorkoutBundleAssembler.multipart(bundle: raw, boundary: boundary), suffix: "multipart.json")
        try await store.setDeletedParentBundle(id, file: file, hash: hash, boundary: boundary, generation: discovery.work[id]?.generation ?? 0, deletionIDs: deletions.map(\.id), sampleCount: samples.count, routeCount: routes.count)
        _ = try await WorkoutBundleDelivery(store: store, api: api).deliver(id)
    }

    private func context(secret: ClientConfiguration.Secret, transfers: any FileUploader, metrics: SyncMetrics) async throws -> (RAIAPIClient, Scope) {
        let api = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token, session: session, uploader: transfers, metrics: metrics)
        // v4 is requested explicitly. A server with the capability disabled
        // ignores the header and keeps returning 3, which is a safe fallback.
        let wantsV4 = secret.configuration.v4Enabled
        let hello = try await api.call("GET", "/v2/handshake",
                                       headers: wantsV4 ? ["X-RAI-Sync-Protocol": "4"] : [:])
        if let sessionStore, let sessionID {
            try await sessionStore.validateActiveHandshake(sessionID, handshake: hello)
        }
        let supported = Set(hello["capabilities"]["sampleTypes"].list.compactMap(\.text))
        let bundleProtocol = hello["capabilities"]["bundleProtocolVersion"].integer
        guard hello["protocolVersion"].integer == 3, let service = hello["serviceID"].text,
              let dataset = hello["datasetID"].text, let epoch = hello["datasetEpoch"].text,
              hello["capabilities"]["workoutBundles"] == .bool(true),
              [3, 4].contains(bundleProtocol),
              !supported.isEmpty,
              HealthKitClient.quantityTypes.allSatisfy({ supported.contains($0.identifier) }) else {
            throw SyncFailure.invalid("unsupported_handshake")
        }
        syncProtocolV4 = wantsV4 && bundleProtocol == 4
            && hello["capabilities"]["bundleCompact"] == .bool(true)
            && hello["capabilities"]["bundleObservationVersion"].integer == 1
        supportsIntegrity = hello["capabilities"]["bundleObservationVersion"].integer == 1 && hello["capabilities"]["explicitChildDeletions"] == .bool(true)
        supportsDaily = hello["capabilities"]["dailyFactsVersion"].integer == 1 && Set(hello["capabilities"]["dailyFactTypes"].list.compactMap(\.text)) == Set(DailyHealth.types)
        vo2Capabilities = hello["capabilities"]
        let scope = Scope(serviceID: service, datasetID: dataset, datasetEpoch: epoch, collectorEpoch: secret.configuration.collectorEpoch)
        try await store.bind(scope)
        return (api, scope)
    }

    /// Startup/foreground recovery. It never reads HealthKit and never starts a
    /// new Workout task: it uploads an already persisted bundle, or asks the
    /// user to retry when the prepared file is absent.
    func recoverPending(secret: ClientConfiguration.Secret, transfers: any FileUploader) async throws {
        guard !running else { return }
        running = true; defer { running = false }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let api: RAIAPIClient, scope: Scope
        do { (api, scope) = try await context(secret: secret, transfers: transfers, metrics: metrics) }
        catch {
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            try await store.deferBundleRecovery(error)
            throw error
        }
        _ = try? await store.purgeLegacyPages()
        try await store.reconcileTasks()
        for task in await store.allTasks() {
            try Task.checkCancellation()
            switch task.phase {
            case .preparing, .uploading:
                guard task.bundleFile != nil, task.bundleSHA256 != nil, task.bundleBoundary != nil else {
                    try? await store.updateTask(task.workoutID) {
                        $0.phase = .failed; $0.errorCode = "interrupted"; $0.updatedAt = Date()
                    }
                    continue
                }
                do { try await performSync(id: task.workoutID, api: api, scope: scope, transfers: transfers, metrics: metrics, forceNewBundle: false, recovery: true) }
                catch is CancellationError { throw CancellationError() }
                catch { /* task records the retryable failure */ }
            case .acknowledged, .analyzing:
                do { try await refreshAnalysis(id: task.workoutID, api: api) }
                catch is CancellationError { throw CancellationError() }
                catch { /* analysis failures do not invalidate a durable ACK */ }
                try? await store.releaseBundle(task.workoutID)
                if await store.task(task.workoutID)?.phase == .analyzing {
                    scheduleAnalysisRefresh(id: task.workoutID, api: api)
                }
            case .published, .partial, .deleted:
                try? await store.releaseBundle(task.workoutID)
            case .failed where task.errorCode == "interrupted":
                if task.bundleFile != nil {
                    do { try await performSync(id: task.workoutID, api: api, scope: scope, transfers: transfers, metrics: metrics, forceNewBundle: false, recovery: true) }
                    catch is CancellationError { throw CancellationError() }
                    catch { }
                }
            default:
                break
            }
        }
        try Task.checkCancellation()
        _ = try? await store.collectGarbage(dryRun: false)
    }

    func syncWorkout(id: String, secret: ClientConfiguration.Secret, transfers: any FileUploader, forceNewBundle: Bool) async throws {
        guard !running else { throw SyncFailure.localState("sync_already_running") }
        running = true; defer { running = false }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let (api, scope) = try await context(secret: secret, transfers: transfers, metrics: metrics)
        if await store.discoverySnapshot().work[id]?.pendingDelete == true { try await deleteWorkout(id: id, api: api, scope: scope); return }
        try await store.selectDiscoveredWorkout(id)
        if !forceNewBundle, await isAlreadyDelivered(id) {
            // The durable ACK already covers every fact local discovery knows
            // about, so there is nothing to read, encode or post. A later
            // discovery generation (new samples, series, routes or an explicit
            // deletion) re-opens this path; an unchanged repeat sync must not
            // re-read HealthKit or re-upload an identical bundle.
            try await store.setTaskMetrics(id, metrics: ["workout_sync_skipped_unchanged": 1])
            Metrics.value("workout_sync_skipped_unchanged", 1)
            // An old ACK can still be waiting for publication. Reuse the same
            // bounded follow-up rather than leaving it to the next recovery.
            if let task = await store.task(id), task.phase == .analyzing,
               (task.analysisCheckedAt ?? .distantPast) < Date().addingTimeInterval(-AnalysisFollowup.staleRefreshInterval) {
                scheduleAnalysisRefresh(id: id, api: api)
            }
            return
        }
        try await store.beginTask(id, resetBundle: forceNewBundle)
        try await performSync(id: id, api: api, scope: scope, transfers: transfers, metrics: metrics, forceNewBundle: forceNewBundle, recovery: false)
    }

    /// True when this Workout's durable ACK already covers the local discovery
    /// state, so an unchanged repeat sync has nothing left to deliver.
    ///
    /// The test is deliberately conservative: a receipt for the current
    /// generation, no pending deletion, no outstanding partial read and no new
    /// discovery generation. Anything else keeps the ordinary prepare path.
    private func isAlreadyDelivered(_ id: String) async -> Bool {
        guard let task = await store.task(id), task.uploadReceipt != nil,
              task.bundleFile == nil,
              (task.pendingReadKeys ?? []).isEmpty,
              [.acknowledged, .analyzing, .published, .partial].contains(task.phase) else { return false }
        let snapshot = await store.discoverySnapshot()
        guard let work = snapshot.work[id], !work.pendingDelete,
              work.generation == work.deliveredGeneration else { return false }
        // Any journal entry that is neither delivered nor explicitly
        // suppressed outranks the skip, whether it is a deletion or an upsert
        // that arrived after the last manifest pinned its cursor. The manifest
        // keeps the watermark honest; this is the independent second guard.
        return !snapshot.events.values.contains {
            $0.workoutID == id && $0.status != "delivered" && $0.status != "suppressed_by_workout_delete"
        }
    }

    /// Facts the bundle needs for one type.
    ///
    /// A scoped discovery stream that is provably exhausted for this Workout
    /// already captured every fact, so the bundle reuses those records instead
    /// of repeating an equivalent full read. The predicate lives in
    /// `OutboxStore.exhaustedDiscoveryFacts` and rejects any stream with a
    /// staged batch, an error, no committed anchor or a full last page; those
    /// cases keep the existing complete read, which also captures what it reads.
    ///
    /// A normalized record (format 2) is decoded straight into a fact. A format
    /// 1 record or a legacy single-file archive is the only thing that still
    /// needs an unarchive, and that call is counted. Reused facts are ordered the
    /// way the complete read orders them (`startUs`, then uuid) so the bundle
    /// bytes stay stable.
    private nonisolated func factsForBundle(id: String, type: HKQuantityType, workout: HKWorkout, forceFresh: Bool = false) async throws
        -> (facts: [NormalizedFact], reused: Bool) {
        guard !forceFresh else {
            return (try await freshFacts(type: type, workout: workout, workoutID: id), false)
        }
        guard let stored = await store.exhaustedDiscoveryFacts(workoutID: id, type: type.identifier) else {
            return (try await freshFacts(type: type, workout: workout, workoutID: id), false)
        }
        var facts: [NormalizedFact] = []
        facts.reserveCapacity(stored.count)
        for event in stored {
            if let fact = try await store.discoveryNormalizedFact(event) {
                facts.append(fact)
                continue
            }
            // Legacy representation: read the archived object once and
            // normalize it. Any fact that cannot be read back disqualifies the
            // whole stream, because a silently short bundle is worse than a
            // full read.
            guard let data = try await store.discoveryArchiveBytes(event),
                  let sample = (try? NSKeyedUnarchiver.unarchivedObject(ofClasses: [HKQuantitySample.self], from: data)) as? HKSample else {
                return (try await freshFacts(type: type, workout: workout, workoutID: id), false)
            }
            SampleArchiveMeter.recordUnarchive()
            facts.append(try Normalizer.normalizedFact(sample, type: type.identifier, workoutID: id))
        }
        facts.sort { lhs, rhs in
            let left = lhs.startUs ?? 0, right = rhs.startUs ?? 0
            return left == right ? lhs.uuid < rhs.uuid : left < right
        }
        // A discovery record written by an older build may decode to a fact
        // without an event (the sample was captured but not describable at the
        // time). Such a record can anchor discovery, but it must never be
        // uploaded as if it were a complete bundle stream; re-read the exact
        // HealthKit objects instead.
        guard facts.allSatisfy(\.isBundleUsable) else {
            return (try await freshFacts(type: type, workout: workout, workoutID: id), false)
        }
        return (facts, true)
    }

    /// The complete HealthKit read for one type, normalized on the way in.
    private nonisolated func freshFacts(type: HKQuantityType, workout: HKWorkout, workoutID: String) async throws -> [NormalizedFact] {
        let samples = try await health.bundleSamples(type: type, workout: workout)
        return try samples.map { try Normalizer.normalizedFact($0, type: type.identifier, workoutID: workoutID) }
    }

    /// What one quantity stream contributed. Streams finish in any order; the
    /// merge walks `HealthKitClient.quantityTypes`, so the bundle bytes are the
    /// same whatever the completion order was.
    private struct QuantityStreamOutcome: Sendable {
        var type: String
        var events: [JSON] = []
        var provenance: [JSON] = []
        var series: [JSON] = []
        var coverage: [String: WorkoutCoverage] = [:]
        var cursor: Int64 = 0
        var coveredFacts = 0
        var coveredEventIDs: Set<String> = []
        var complete = false
        var reused = false
        var fullRead = false
        var captureWrites = 0
        var captureMs = 0.0
        var seriesMs = 0.0
    }

    private struct RouteStreamOutcome: Sendable {
        var routes: [JSON] = []
        var provenance: [JSON] = []
        var pointCount = 0
        var complete = false
        var coverage: [String: WorkoutCoverage] = [:]
    }

    /// Runs one quantity stream under the capture gate.
    ///
    /// The slot is always released, including on cancellation, so a cancelled
    /// sync hands its budget back instead of starving the next one. Paging and
    /// the anchor inside a stream are sequential because a stream is a single
    /// producer; only distinct types overlap.
    private nonisolated func captureQuantityStream(
        id: String, type: HKQuantityType, workout: HKWorkout, source: JSON,
        deletedKeys: Set<String>, gate: CaptureGate, writer: CaptureWriter, forceFresh: Bool
    ) async throws -> QuantityStreamOutcome {
        try await gate.enter()
        do {
            let outcome = try await captureQuantityBody(id: id, type: type, workout: workout, source: source,
                                                        deletedKeys: deletedKeys, writer: writer, forceFresh: forceFresh)
            await gate.exit()
            return outcome
        } catch {
            await gate.exit()
            throw error
        }
    }

    private nonisolated func captureQuantityBody(
        id: String, type: HKQuantityType, workout: HKWorkout, source: JSON,
        deletedKeys: Set<String>, writer: CaptureWriter, forceFresh: Bool
    ) async throws -> QuantityStreamOutcome {
        var outcome = QuantityStreamOutcome(type: type.identifier)
        do {
            try Task.checkCancellation()
            let (facts, reused) = try await factsForBundle(id: id, type: type, workout: workout, forceFresh: forceFresh)
            var events: [JSON] = []
            if reused {
                outcome.reused = true
            } else {
                outcome.fullRead = true
                let captureStart = Date()
                var capturedIDs = Set(await store.discoverySnapshot().events.keys)
                for start in stride(from: 0, to: facts.count, by: 1000) {
                    var records: [CapturedFact] = []
                    for fact in facts[start..<min(start + 1000, facts.count)] {
                        // No archive: the normalized bytes are the record.
                        let payload = try fact.encoded()
                        let eventID = DiscoveryEvent(type: type.identifier, uuid: fact.uuid, deleted: false,
                                                     archiveHash: Wire.hash(payload), workoutID: id,
                                                     identityHash: fact.identityHash).id
                        if capturedIDs.contains(eventID) { continue }
                        capturedIDs.insert(eventID)
                        records.append(CapturedFact(type: type.identifier, uuid: fact.uuid, archive: payload,
                                                    identityHash: fact.identityHash, encoding: .normalized))
                    }
                    guard !records.isEmpty else { continue }
                    // One sealed segment file per batch replaces one atomic file
                    // per sample; the writer is the only store caller.
                    let captured = try await writer.append(type: type.identifier, workoutID: id, records: records)
                    if captured.file != nil { outcome.captureWrites += 1 }
                }
                outcome.captureMs += Date().timeIntervalSince(captureStart) * 1000
            }
            let associationKeys = facts.map(\.factKey)
            try await store.associateDiscoveredFacts(workoutID: id, keys: associationKeys)
            // Pin the type cursor from the journal *as it is now* but only for
            // the fact keys this bundle actually read. A late sample for this
            // same type is absent from the read, so it keeps a higher ordinal
            // and stays pending.
            let journal = await store.discoverySnapshot()
            let covered = Set(associationKeys)
            var highest: Int64 = 0
            for event in journal.events.values
            where event.type == type.identifier && event.workoutID == id && !event.deleted && covered.contains(event.factKey) {
                // Identity is what the ACK may confirm; the cursor is the
                // evidence of the highest ordinal this stream encoded.
                outcome.coveredEventIDs.insert(event.id)
                if let ordinal = event.ordinal, ordinal > highest { highest = ordinal }
            }
            outcome.cursor = highest
            outcome.coveredFacts = facts.count
            if let root = journal.events.values.first(where: { $0.type == HKObjectType.workoutType().identifier && $0.uuid == id && !$0.deleted }) {
                outcome.coveredEventIDs.insert(root.id)
            }
            var eligible: [NormalizedFact] = []
            var typeProvenance: [JSON] = []
            for fact in facts where !deletedKeys.contains(fact.factKey) {
                guard let provenance = fact.source, let event = fact.event else {
                    // Captured but not describable (unsupported metadata). The
                    // archived path failed here too: the fact stays local and the
                    // stream is reported failed.
                    throw SyncFailure.invalid("metadata_requires_review")
                }
                if ["apple_watch", "associated_apple_watch"].contains(HealthSourcePolicy.classification(provenance, associatedWorkout: source)) {
                    events.append(event); eligible.append(fact)
                    typeProvenance.append(.object(["sampleType": .string(type.identifier), "sampleUUID": .string(fact.uuid), "source": provenance]))
                }
            }
            outcome.events = events
            outcome.provenance = typeProvenance
            outcome.coverage[type.identifier] = .result(count: events.count)
            if eligible.count != facts.count { outcome.coverage[type.identifier]?.state = .partial; outcome.coverage[type.identifier]?.diagnostics = ["source_policy_pending"] }

            // Series failure must not discard the scalar samples that were
            // already read for this type. Completeness describes read coverage
            // only; absent children never prove deletion.
            do {
                let seriesStart = Date()
                let seriesBySample = try await health.captureSeries(type: type, workout: workout, store: store)
                outcome.seriesMs += Date().timeIntervalSince(seriesStart) * 1000
                var seriesCount = 0
                var captured: [JSON] = []
                for fact in eligible {
                    guard let parent = fact.series, parent.pointCount > 1 else { continue }
                    guard let points = seriesBySample[parent.parentUUID],
                          Int64(points.count) == parent.pointCount else {
                        throw SyncFailure.invalid("quantity_series_incomplete")
                    }
                    seriesCount += points.count
                    captured.append(.object(["sampleType": .string(type.identifier),
                                             "sampleUUID": .string(parent.parentUUID),
                                             "points": .array(points)]))
                }
                outcome.series = captured
                outcome.coverage["series:" + type.identifier] = .result(count: seriesCount)
                if !events.isEmpty && eligible.count == facts.count { outcome.complete = true }
            } catch is CancellationError { throw CancellationError() }
            catch let failure as SyncFailure {
                outcome.coverage["series:" + type.identifier] = WorkoutCoverage(state: .failed, sampleCount: events.count, errorCode: code(failure))
                outcome.coverage[type.identifier]?.state = .partial
            } catch {
                outcome.coverage["series:" + type.identifier] = WorkoutCoverage(state: .failed, sampleCount: events.count, errorCode: "healthkit_series_read_failed")
                outcome.coverage[type.identifier]?.state = .partial
            }
        } catch is CancellationError { throw CancellationError() }
        catch let failure as SyncFailure {
            outcome.coverage[type.identifier] = WorkoutCoverage(state: .failed, errorCode: code(failure))
        } catch {
            outcome.coverage[type.identifier] = WorkoutCoverage(state: .failed, errorCode: "healthkit_read_failed")
        }
        return outcome
    }

    /// Runs the route stream under the same gate.
    private nonisolated func captureRouteStream(
        id: String, workout: HKWorkout, source: JSON, routeDeletions: [String], gate: CaptureGate
    ) async throws -> RouteStreamOutcome {
        try await gate.enter()
        do {
            let outcome = try await captureRouteBody(id: id, workout: workout, source: source, routeDeletions: routeDeletions)
            await gate.exit()
            return outcome
        } catch {
            await gate.exit()
            throw error
        }
    }

    private nonisolated func captureRouteBody(
        id: String, workout: HKWorkout, source: JSON, routeDeletions: [String]
    ) async throws -> RouteStreamOutcome {
        var outcome = RouteStreamOutcome()
        do {
            try Task.checkCancellation()
            let read = try await health.captureRoutes(workout: workout, store: store)
            outcome.coverage.merge(read.coverage) { _, new in new }
            try await store.associateDiscoveredFacts(workoutID: id, keys: read.routes.map { HKSeriesType.workoutRoute().identifier + ":" + $0.id })
            for route in read.routes where !routeDeletions.contains(route.id) {
                guard let provenance = route.provenance, ["apple_watch", "associated_apple_watch"].contains(HealthSourcePolicy.classification(provenance, associatedWorkout: source)) else {
                    outcome.coverage["route:" + route.id] = WorkoutCoverage(state: .partial, errorCode: "source_policy_pending"); continue
                }
                outcome.provenance.append(.object(["routeID": .string(route.id), "source": provenance]))
                outcome.pointCount += route.points.count
                outcome.routes.append(.object([
                    "routeID": .string(route.id),
                    "source": .object(["bundleID": .string(route.source)]),
                    "points": .array(route.points.map { point in
                        var fields: [String: JSON] = ["recordedAtUs": .int(point.recordedAtUs),
                            "latitude": .number(point.latitude), "longitude": .number(point.longitude)]
                        for (key, value) in [("altitude", point.altitude), ("horizontalAccuracy", point.horizontalAccuracy),
                                             ("verticalAccuracy", point.verticalAccuracy), ("speed", point.speed), ("course", point.course)] {
                            if let value { fields[key] = .number(value) }
                        }
                        return .object(fields)
                    })
                ]))
            }
            outcome.complete = read.enumerationCompleted && (read.routes.isEmpty ? read.coverage.isEmpty :
                !outcome.routes.isEmpty && read.coverage.values.allSatisfy { $0.queryCompleted == true && $0.state == .complete } && outcome.routes.count == read.routes.count)
            outcome.coverage["route"] = .result(count: outcome.pointCount, completed: read.enumerationCompleted && (read.routes.isEmpty ? read.coverage.isEmpty : outcome.complete))
        } catch is CancellationError { throw CancellationError() }
        catch let failure as SyncFailure { outcome.coverage["route"] = WorkoutCoverage(state: .failed, errorCode: code(failure)) }
        catch { outcome.coverage["route"] = WorkoutCoverage(state: .failed, errorCode: "route_read_or_mapping_failed") }
        return outcome
    }

    /// Reads the exact coverage block that will be validated with the bundle.
    /// The task projection can be stale after a failed attempt; the payload is
    /// immutable and therefore authoritative for the upload about to happen.
    static func coverageFromBundle(_ url: URL, boundary: String) -> [String: WorkoutCoverage]? {
        // The durable bundle is multipart. Decoding it as bare JSON (or
        // decoding all samples just to read the header) is exactly the silent
        // fallback this path must never take again.
        guard let body = try? Data(contentsOf: url, options: .mappedIfSafe),
              let raw = WorkoutBundleAssembler.multipartPayload(body, boundary: boundary),
              let observationData = JSON.topLevelValue("observation", in: raw),
              let observation = try? JSON.decode(observationData) else { return nil }
        let fields = observation["coverage"].fields
        guard !fields.isEmpty else { return nil }
        var result: [String: WorkoutCoverage] = [:]
        for (key, value) in fields {
            guard let state = value["state"].text.flatMap(WorkoutCoverage.State.init(rawValue:)) else { return nil }
            var coverage = WorkoutCoverage(state: state, sampleCount: Int(value["sampleCount"].integer ?? 0))
            if case .bool(let completed) = value["queryCompleted"] { coverage.queryCompleted = completed }
            if case .bool(let persisted) = value["localPersisted"] { coverage.localPersisted = persisted }
            coverage.expectedCount = value["expectedCount"].integer.map(Int.init)
            coverage.errorCode = value["errorCode"].text
            let diagnostics = value["diagnostics"].list.compactMap(\.text)
            coverage.diagnostics = diagnostics.isEmpty ? nil : diagnostics
            result[key] = coverage
        }
        return result
    }

    private func performSync(id: String, api: RAIAPIClient, scope: Scope, transfers: any FileUploader,
                             metrics: SyncMetrics, forceNewBundle: Bool, recovery: Bool) async throws {
        let started = Date()
        var coverage: [String: WorkoutCoverage] = [:]
        var timings: [String: Double] = [:]
        do {
            var existing = await store.task(id)
            let forceFreshTypes = Set(existing?.pendingReadKeys ?? [])
            if let task = existing,
               let file = task.bundleFile, let hash = task.bundleSHA256, let boundary = task.bundleBoundary,
               let url = try? await store.fileURL(file), FileManager.default.fileExists(atPath: url.path) {
                // A prepared bundle is the durable source of truth for what was
                // actually read. Earlier failed attempts may have left the
                // task's live coverage at a stale failed/partial snapshot; the
                // bundle's observation block is the exact payload the server
                // will validate. Never upload a complete bundle while telling
                // the local task it has no samples.
                if let durableCoverage = Self.coverageFromBundle(url, boundary: boundary) {
                    try await store.updateTask(id) { task in
                        task.coverage = durableCoverage
                        task.pendingReadKeys = durableCoverage.filter { $0.value.needsRetry }
                            .map(\.key).filter { $0 != "metadata" }
                    }
                    try await uploadAndAcknowledge(id: id, file: url, bundleHash: hash, boundary: boundary,
                                                    coverage: durableCoverage, timings: task.metrics,
                                                    started: started, api: api, transfers: transfers)
                    if syncProtocolV4, await store.task(id)?.uploadReceipt != nil {
                        _ = try? await V4Outbox(directory: store.directory).promotePending(
                            bundleSHA256: hash, id: id, scope: scope)
                    }
                    return
                }
                Metrics.value("bundle_coverage_parse_failures", 1)
                // Recovery never reads HealthKit and never rewrites an
                // unacknowledged identity behind the user's back.
                guard !recovery, task.uploadReceipt == nil else {
                    try await store.updateTask(id) { $0.errorCode = "bundle_observation_corrupt" }
                    throw SyncFailure.localState("bundle_observation_corrupt")
                }
                // The prepared bytes are not a legal upload authority. Drop
                // them and rebuild from the same discovery facts; the old
                // attempt's stale coverage is never used.
                try await store.discardUnreadableBundle(id)
                existing = await store.task(id)
            }
            if recovery {
                throw SyncFailure.localState("bundle_not_prepared")
            }
            if let existing, existing.bundleFile != nil { throw SyncFailure.localState("missing_payload") }

            guard supportsIntegrity else { throw SyncFailure.invalid("unsupported_integrity_contract") }
            if syncProtocolV4 {
                try await syncV4(id: id, api: api, scope: scope, transfers: transfers, metrics: metrics,
                                 existing: existing, started: started)
                return
            }
            try await updateTask(id, phase: .preparing, coverage: coverage)

            // A Workout deletion is never inferred from an absent sample or a
            // failed read. Only an anchored HKDeletedObject for this exact
            // UUID can issue the explicit DELETE command. Keep the candidate
            // anchor until the bundle is acknowledged, so a failed read/upload
            // does not hide a later deletion.
            let workoutAnchor = existing?.workoutAnchor
            timings["workout_query_used_committed_anchor"] = workoutAnchor == nil ? 0 : 1
            var nextWorkoutAnchor: Data?
            let rootStart = Date()
            do {
                let change = try await health.readWorkout(id: id, anchor: workoutAnchor)
                nextWorkoutAnchor = change.anchor
                if change.deleted.contains(where: { $0.uuid.uuidString.lowercased() == id }) {
                    try await deleteWorkout(id: id, api: api, scope: scope)
                    return
                }
            }

            guard let workout = try await health.workout(id) else { throw SyncFailure.invalid("workout_not_visible") }
            timings["workout_query_ms"] = Date().timeIntervalSince(rootStart) * 1000
            let classifyStart = Date()
            let source = Normalizer.source(workout)
            let classification = HealthSourcePolicy.classification(source)
            let outcome = WorkoutSourcePolicy.outcome(classification)
            try await store.recordSourceClassification(workoutID: id, classification: classification,
                                                       evidence: WorkoutSourcePolicy.evidence(source))
            // Entry guard for a source that changed after the list was read, or
            // for an old queued task. A decided exclusion is terminal for this
            // task; an undecidable source stays pending instead of guessing.
            guard outcome == WorkoutSourcePolicy.eligible else {
                throw SyncFailure.invalid(outcome == WorkoutSourcePolicy.excluded ? "source_policy_excluded" : "source_policy_pending")
            }
            let observedWorkout = try Normalizer.integrityEvent(workout)
            timings["source_classify_ms"] = Date().timeIntervalSince(classifyStart) * 1000
            let workoutEvent = try await store.originalWorkout(id, event: observedWorkout)
            let discovery = await store.discoverySnapshot()
            let generation = discovery.work[id]?.generation ?? 0
            let deletions = discovery.events.values.filter { $0.workoutID == id && $0.deleted }
            let sampleDeletions: [JSON] = deletions.filter { $0.type != HKObjectType.workoutType().identifier && $0.type != HKSeriesType.workoutRoute().identifier }.map {
                .object(["sampleType": .string($0.type), "sampleUUID": .string($0.uuid)])
            }
            let routeDeletions = deletions.filter { $0.type == HKSeriesType.workoutRoute().identifier }.map(\.uuid).sorted()
            let deletedKeys = Set(deletions.map(\.factKey))
            var sampleProvenance: [JSON] = []
            var routeProvenance: [JSON] = []
            coverage["workout"] = .result(count: 1)
            coverage["metadata"] = .result(count: source.fields.count)
            let missingSource = ["version", "productType", "operatingSystemVersion", "device"].filter { source[$0] == .null }
            if !missingSource.isEmpty { coverage["metadata"]?.state = .partial; coverage["metadata"]?.diagnostics = missingSource.map { "missing_" + $0 } }
            // No native running-cadence type is exposed by this SDK. Elevation
            // metadata is not a substitute for raw elevation samples.
            coverage["cadence"] = WorkoutCoverage(state: .unsupported)
            coverage["elevation_sample"] = WorkoutCoverage(state: .unsupported)
            for type in HealthKitClient.quantityTypes { coverage[type.identifier] = WorkoutCoverage(state: .notQueried); coverage["series:" + type.identifier] = WorkoutCoverage(state: .notQueried) }

            var samples: [JSON] = []
            var series: [JSON] = []
            var completeSampleTypes: [String] = []
            var reusedReads = 0, fullReads = 0, captureWrites = 0
            var captureMs = 0.0, seriesMs = 0.0
            // Serializer accounting for this sync: the normalized path must not
            // archive or unarchive a single sample.
            let meterStart = SampleArchiveMeter.snapshot()
            // R1: what this bundle provably consumes, type by type. A cursor is
            // the highest journal ordinal among the facts the bundle really
            // encoded for that type, so work a producer commits later keeps a
            // higher ordinal and is never confirmed by this receipt.
            var captureCursors: [String: Int64] = [:]
            var coveredEventIDs: Set<String> = []
            var coveredFacts = 0
            let quantityStart = Date()
            // R3: independent quantity streams overlap behind one bounded gate,
            // and their results are merged in the fixed `quantityTypes` order, so
            // the bytes do not depend on which stream finished first.
            let gate = CaptureGate(policy: CaptureConcurrencyPolicy(streams: captureStreams))
            let writer = CaptureWriter(store: store, gate: gate)
            var outcomes: [String: QuantityStreamOutcome] = [:]
            try await withThrowingTaskGroup(of: QuantityStreamOutcome.self) { group in
                for type in HealthKitClient.quantityTypes {
                    group.addTask {
                        try await self.captureQuantityStream(id: id, type: type, workout: workout, source: source,
                                                             deletedKeys: deletedKeys, gate: gate, writer: writer,
                                                             forceFresh: forceFreshTypes.contains(type.identifier))
                    }
                }
                for try await outcome in group { outcomes[outcome.type] = outcome }
            }
            for type in HealthKitClient.quantityTypes {
                guard let outcome = outcomes[type.identifier] else { continue }
                samples += outcome.events
                sampleProvenance += outcome.provenance
                series += outcome.series
                for (key, value) in outcome.coverage { coverage[key] = value }
                captureCursors[type.identifier] = outcome.cursor
                coveredFacts += outcome.coveredFacts
                coveredEventIDs.formUnion(outcome.coveredEventIDs)
                if outcome.complete { completeSampleTypes.append(type.identifier) }
                if outcome.reused { reusedReads += 1 }
                if outcome.fullRead { fullReads += 1 }
                captureWrites += outcome.captureWrites
                captureMs += outcome.captureMs
                seriesMs += outcome.seriesMs
            }
            let gateTotals = await gate.snapshot()
            let writerTotals = await writer.totals()
            timings["capture_max_active_streams"] = Double(gateTotals.maxActive)
            timings["capture_stream_limit"] = Double(captureStreams)
            timings["capture_peak_queue_bytes"] = Double(gateTotals.peakReservedBytes)
            timings["capture_writer_batches"] = Double(writerTotals.batches)
            timings["capture_writer_records"] = Double(writerTotals.records)
            timings["healthkit_read_ms"] = Date().timeIntervalSince(quantityStart) * 1000
            timings["reused_discovery_reads"] = Double(reusedReads)
            timings["full_sample_reads"] = Double(fullReads)
            timings["normalized_capture_ms"] = captureMs
            timings["normalized_segment_writes"] = Double(captureWrites)
            let meterEnd = SampleArchiveMeter.snapshot()
            timings["sample_archive_calls"] = Double(meterEnd.archive - meterStart.archive)
            timings["sample_unarchive_calls"] = Double(meterEnd.unarchive - meterStart.unarchive)
            timings["object_archive_calls"] = Double(meterEnd.objectArchive - meterStart.objectArchive)
            timings["series_read_ms"] = seriesMs

            var routes: [JSON] = []
            var routePointCount = 0
            var completeRoutes = false
            let routeStart = Date()
            // The route stream is a capture producer as well: it takes a stream
            // slot so routes, series and sample reads share one capture budget.
            let routeOutcome = try await captureRouteStream(id: id, workout: workout, source: source,
                                                            routeDeletions: routeDeletions, gate: gate)
            routes += routeOutcome.routes
            routeProvenance += routeOutcome.provenance
            routePointCount += routeOutcome.pointCount
            completeRoutes = routeOutcome.complete
            for (key, value) in routeOutcome.coverage { coverage[key] = value }
            timings["route_read_ms"] = Date().timeIntervalSince(routeStart) * 1000
            // Batch evidence: flush and synchronization counts must track
            // batches, not individual points (F15/P11).
            let batches = await health.captureBatchCounters()
            timings["series_capture_flushes"] = Double(batches.seriesFlushes)
            timings["series_capture_synchronizations"] = Double(batches.seriesSynchronizations)
            timings["series_capture_points"] = Double(batches.seriesPoints)
            timings["route_capture_flushes"] = Double(batches.routeFlushes)
            timings["route_capture_synchronizations"] = Double(batches.routeSynchronizations)
            timings["route_capture_points"] = Double(batches.routePoints)

            guard samples.count <= 50_000, series.count <= 50_000,
                  series.reduce(0, { $0 + $1["points"].list.count }) <= 1_000_000,
                  routes.count <= 16,
                  routes.reduce(0, { $0 + $1["points"].list.count }) <= 1_000_000 else {
                throw SyncFailure.invalid("bundle_too_large")
            }
            for key in coverage.keys { coverage[key]?.localPersisted = true }
            try await store.setTaskCoverage(id, coverage: coverage)
            let observation: JSON = .object(["version": .int(1), "observedAtUs": .int(Normalizer.us(Date())),
                "metadata": observedWorkout["metadata"] == .null ? .object([:]) : observedWorkout["metadata"],
                "provenance": .object(["workout": source, "samples": .array(sampleProvenance), "routes": .array(routeProvenance)]),
                "coverage": .object(coverage.mapValues(\.wire))])
            let encodeStart = Date()
            var bundle = try WorkoutBundleAssembler.data(scope: scope, workoutID: id, workout: workoutEvent,
                                                          samples: samples, series: series, routes: routes,
                                                          completeSampleTypes: completeSampleTypes, completeRoutes: completeRoutes,
                                                          observation: observation, sampleDeletions: sampleDeletions, routeDeletions: routeDeletions)
            timings["bundle_encode_ms"] = Date().timeIntervalSince(encodeStart) * 1000
            let persistStart = Date()
            if existing?.originalWorkout != nil {
                var prior: JSON?
                do {
                    prior = try await api.call("GET", "/v2/sync/workout-bundles/" + id)
                } catch is CancellationError { throw CancellationError() }
                catch {
                    // Offline/unsupported lookup still stages immutable bytes
                    // for ordinary delivery; it never manufactures an ACK.
                    try Task.checkCancellation()
                }
                if let prior, let replay = try WorkoutBundleAssembler.exactReplay(rebuilt: bundle, receipt: prior) {
                    bundle = replay; timings["workout_reused_exact_bundle"] = 1
                }
            }
            guard bundle.count <= 32 * 1_024 * 1_024 else { throw SyncFailure.invalid("bundle_too_large") }
            let bundleHash = Wire.hash(bundle)
            let boundary = "rai.boundary." + Wire.id()
            let multipart = WorkoutBundleAssembler.multipart(bundle: bundle, boundary: boundary)
            let fileName = try await store.savePayload(multipart, suffix: "multipart.json")
            let file = try await store.fileURL(fileName)
            // The delivered generation is the one this bundle covers, which
            // includes the facts this pass just captured. Reading the snapshot
            // taken before capture would leave `deliveredGeneration` one
            // generation behind, so an unchanged repeat sync could never be
            // recognised as already delivered.
            // R1: the receipt may only confirm the work this bundle really
            // carried. The manifest pins the per-type capture cursors and the
            // exact deletion ids encoded above; the observed generation is kept
            // as evidence and for the legacy watermark of an old pending task.
            let manifestGeneration = await store.discoverySnapshot().work[id]?.generation ?? generation
            let manifest = CaptureManifest(workoutID: id, generation: manifestGeneration, cursors: captureCursors,
                                           coveredEventIDs: coveredEventIDs.sorted(), deletionEventIDs: deletions.map(\.id),
                                           completeTypes: completeSampleTypes, coveredFacts: coveredFacts)
            timings["capture_manifest_facts"] = Double(manifest.coveredFacts)
            timings["capture_manifest_types"] = Double(manifest.cursors.count)
            timings["capture_manifest_deletions"] = Double(manifest.deletionEventIDs.count)
            try await store.setBundle(id, file: fileName, sha256: bundleHash, boundary: boundary, sampleDeletions: sampleDeletions.count, routeDeletions: routeDeletions.count, discoveryGeneration: manifestGeneration, deletionEventIDs: deletions.map(\.id), pendingReadKeys: coverage.filter { $0.value.needsRetry }.map(\.key).filter { $0 != "metadata" }, manifest: manifest)
            timings["bundle_bytes"] = Double(bundle.count)
            timings["bundle_persist_ms"] = Date().timeIntervalSince(persistStart) * 1000
            timings["bundle_build_ms"] = Date().timeIntervalSince(started) * 1000
            try await updateTask(id, phase: .uploading, coverage: coverage, metrics: timings)
            try await uploadAndAcknowledge(id: id, file: file, bundleHash: bundleHash, boundary: boundary,
                                            coverage: coverage, timings: timings, started: started,
                                            api: api, transfers: transfers)
            if let nextWorkoutAnchor { try? await store.setWorkoutAnchor(id, anchor: nextWorkoutAnchor) }
        } catch {
            // Delivery owns durable retry state; analysis cannot downgrade ACK.
            if let task = await store.task(id), task.uploadReceipt == nil, task.lastUploadError == nil {
                let value = (error as? UploadError)?.code ?? (error as? SyncFailure).map(code) ?? "sync_failed"
                try? await store.updateTask(id) { $0.phase = .failed; $0.errorCode = value }
            }
            throw error
        }
    }

    /// Protocol v4 path: one complete HealthKit read per stream, one columnar
    /// bundle, one immutable file. It shares the delivery/ACK machinery with v3
    /// but never touches capture pages, anchors from the page protocol or the
    /// completeness proof.
    private func syncV4(id: String, api: RAIAPIClient, scope: Scope, transfers: any FileUploader,
                        metrics: SyncMetrics, existing: WorkoutSyncTask?, started: Date) async throws {
        let buildStart = Date()
        do {
            try await updateTask(id, phase: .preparing, coverage: [:])
            let engine = V4SyncEngine(store: store, health: health, scope: scope,
                                      outbox: V4Outbox(directory: store.directory))
            switch try await engine.build(id: id, anchor: existing?.workoutAnchor) {
            case .deleted:
                try await deleteWorkout(id: id, api: api, scope: scope)
            case .prepared(let bundle):
                let boundary = "rai.boundary." + Wire.id()
                let body = WorkoutBundleAssembler.multipart(bundle: bundle.raw, boundary: boundary)
                let file = try await store.savePayload(body, suffix: "multipart.json")
                let hash = Wire.hash(bundle.raw)
                let outbox = V4Outbox(directory: store.directory)
                try await outbox.savePending(bundle.index, bundleSHA256: hash)
                try await store.setBundle(id, file: file, sha256: hash, boundary: boundary,
                                          sampleDeletions: bundle.sampleDeletions, routeDeletions: bundle.routeDeletions,
                                          pendingReadKeys: bundle.coverage.filter { $0.value.needsRetry }.map { $0.key })
                try await store.setTaskCoverage(id, coverage: bundle.coverage)
                try await updateTask(id, phase: .uploading, coverage: bundle.coverage)
                let timings = ["v4_build_ms": Date().timeIntervalSince(buildStart) * 1_000]
                try await uploadAndAcknowledge(id: id, file: try await store.fileURL(file), bundleHash: hash,
                                               boundary: boundary, coverage: bundle.coverage, timings: timings,
                                               started: started, api: api, transfers: transfers)
                // D2 deletion authority comes only from a committed read. A
                // failed upload must not advance the local sidecar, otherwise
                // a later short read could delete samples the server never
                // received. The receipt is set by the durable ACK.
                if await store.task(id)?.uploadReceipt != nil {
                    _ = try? await outbox.promotePending(bundleSHA256: hash, id: id, scope: scope)
                }
                if let nextAnchor = bundle.nextAnchor { try? await store.setWorkoutAnchor(id, anchor: nextAnchor) }
            }
        } catch {
            if let task = await store.task(id), task.uploadReceipt == nil, task.lastUploadError == nil {
                let value = (error as? UploadError)?.code ?? (error as? SyncFailure).map(code) ?? "sync_failed"
                try? await store.updateTask(id) { $0.phase = .failed; $0.errorCode = value }
            }
            throw error
        }
    }

    private func uploadAndAcknowledge(id: String, file: URL, bundleHash: String, boundary: String,
                                      coverage: [String: WorkoutCoverage], timings: [String: Double], started: Date,
                                      api: RAIAPIClient, transfers: any FileUploader) async throws {
        let uploadStart = Date()
        guard try await WorkoutBundleDelivery(store: store, api: api).deliver(id) else { return }
        var timings = (await store.task(id))?.metrics.merging(timings) { _, new in new } ?? timings
        timings["upload_ms"] = Date().timeIntervalSince(uploadStart) * 1000
        timings["workout_sync_ms"] = Date().timeIntervalSince(started) * 1000
        try await store.setTaskMetrics(id, metrics: timings)
        try? await store.synced()
        _ = try? await store.collectGarbage(dryRun: false)
        // A durable ACK is this Workout's completion point. Publication of the
        // analysis is a separate, asynchronous state: the next upload must not
        // wait for it, and a slow or missing publication never downgrades the
        // acknowledgement.
        scheduleAnalysisRefresh(id: id, api: api)
    }

    /// Refreshes the analysis state of one Workout without extending the
    /// user's wait. A newer refresh for the same Workout replaces the pending
    /// one; failures stay recorded on the task and never touch the ACK.
    ///
    /// A single refresh was too short for a slow publication and left the task
    /// at `analyzing` until the next recovery. This loop is bounded by
    /// `AnalysisFollowup`: it stops at the first final phase, on cancellation
    /// (background/user switch) and after the documented budget, then records
    /// `analysis_followup_exhausted` for the changefeed or the next recovery.
    private func scheduleAnalysisRefresh(id: String, api: RAIAPIClient) {
        analysisRefreshes[id]?.cancel()
        analysisRefreshes[id] = Task { [api, store] in
            for attempt in 0...AnalysisFollowup.delays.count {
                if Task.isCancelled { return }
                if attempt > 0 {
                    guard let delay = AnalysisFollowup.delay(forAttempt: attempt - 1) else { break }
                    do { try await Task.sleep(for: .seconds(delay)) } catch { return }
                }
                try? await refreshAnalysis(id: id, api: api)
                guard let task = await store.task(id), AnalysisFollowup.shouldContinue(task.phase) else { return }
            }
            if Task.isCancelled { return }
            Metrics.value("analysis_followup_exhausted", 1)
        }
    }
    /// Backgrounding cancels publication follow-ups. The durable ACK and the
    /// last observed analysis state stay untouched.
    func stopAnalysisFollowups() {
        for task in analysisRefreshes.values { task.cancel() }
        analysisRefreshes.removeAll()
    }

    private func deleteWorkout(id: String, api: RAIAPIClient, scope: Scope) async throws {
        let receipt = try await api.deleteWorkoutBundle(workoutID: id, scope: scope)
        try await store.acknowledgeWorkoutDeletion(id, receipt: receipt)
        try? await store.synced()
        _ = try? await store.collectGarbage(dryRun: false)
    }

    func checkAnalysis(id: String, secret: ClientConfiguration.Secret, transfers: any FileUploader) async throws {
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let (api, _) = try await context(secret: secret, transfers: transfers, metrics: metrics)
        if let task = await store.task(id), task.bundleFile != nil, task.uploadReceipt == nil {
            guard let hash = task.bundleSHA256 else { throw SyncFailure.localState("bundle_not_prepared") }
            // An older analysis publication is not proof for this exact bundle.
            let receipt = try await api.call("GET", "/v2/sync/workout-bundles/" + id, query: ["sha256": hash])
            try WorkoutBundleACK.validate(receipt, workoutID: id, bundleHash: hash, sampleDeletions: task.submittedSampleDeletions ?? 0, routeDeletions: task.submittedRouteDeletions ?? 0)
            try await store.acknowledgeBundle(id, receipt: receipt)
            _ = try? await store.collectGarbage(dryRun: false)
        }
        if await store.task(id) == nil {
            try await store.beginTask(id, resetBundle: false)
            try await store.updateTask(id) { $0.phase = .acknowledged; $0.acknowledgedAt = Date() }
        }
        try await refreshAnalysis(id: id, api: api)
    }

    private func refreshAnalysis(id: String, api: RAIAPIClient) async throws {
        if let task = await store.task(id), task.bundleFile != nil, task.uploadReceipt == nil { return }
        do {
            let value = try await api.call("GET", "/v2/analysis/workouts/" + id)
            let metrics = value["metrics"].list
            let partialMetric = metrics.contains { ($0["quality"].text ?? "") != "available" }
            let missingCoverage = !(await store.task(id)?.missing.isEmpty ?? true)
            var timings: [String: Double] = [:]
            if let ack = await store.task(id)?.acknowledgedAt { timings["publication_ms"] = Date().timeIntervalSince(ack) * 1000 }
            try await updateTask(id, phase: metrics.isEmpty ? .analyzing : ((partialMetric || missingCoverage) ? .partial : .published), metrics: timings)
        } catch let error as UploadError where error.status == 404 && error.code == "analysis_not_ready" {
            try await updateTask(id, phase: .analyzing)
        } catch let error as UploadError where error.status == 404 && error.code == "workout_not_found" {
            try await updateTask(id, phase: .failed, error: "workout_not_found")
        } catch let error as UploadError where error.status == 409 && ["sample_deleted", "route_deleted", "workout_deleted"].contains(error.code) {
            try await updateTask(id, phase: .partial, error: error.code)
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            // Analysis is asynchronous; a transient read failure must not
            // downgrade a durable ACK. The user can re-check later.
        }
        try await store.updateTask(id) { $0.analysisCheckedAt = Date() }
    }

    private func updateTask(_ id: String, phase: WorkoutSyncPhase, coverage: [String: WorkoutCoverage]? = nil,
                            error: String? = nil, metrics: [String: Double] = [:]) async throws {
        try await store.updateTask(id) { task in
            task.phase = phase
            if let coverage { task.coverage = coverage }
            task.errorCode = error
            if !metrics.isEmpty { task.metrics.merge(metrics) { _, new in new } }
            if [.acknowledged, .analyzing, .published, .partial].contains(phase), task.acknowledgedAt == nil { task.acknowledgedAt = Date() }
            task.missing = task.coverage.filter { $0.value.needsRetry }.keys.sorted()
        }
    }

    private nonisolated func code(_ failure: SyncFailure) -> String {
        switch failure { case .invalid(let value), .localState(let value): return value; case .receiptMismatch: return "receipt_mismatch" }
    }
}


extension SyncCoordinator {
    /// A separate control-plane cycle. Pending exact manifests recover without
    /// HealthKit reads; committed anchors move only after the final receipt.
    func syncWorkoutCompleteness(secret: ClientConfiguration.Secret, transfers: any FileUploader) async throws {
        guard !completenessRunning else { throw SyncFailure.localState("completeness_already_running") }
        completenessRunning = true; defer { completenessRunning = false }
        do {
            let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
            let (api, scope) = try await context(secret: secret, transfers: transfers, metrics: metrics)
            guard vo2Capabilities["workoutStreamSetVersion"].integer == 1 else { throw SyncFailure.invalid("unsupported_completeness_capability") }
            // v2 is the reviewed contract: visible inventory is explained as
            // eligible (receipts) plus explicit source-policy exclusions.
            let reviewed = vo2Capabilities["workoutSyncCompletenessV2Version"].integer == 2
            guard reviewed || vo2Capabilities["workoutSyncCompletenessVersion"].integer == 1 else { throw SyncFailure.invalid("unsupported_completeness_capability") }
            let contract = reviewed ? WorkoutCompleteness.v2Contract : WorkoutCompleteness.contract
            let initial = try await store.beginWorkoutCompleteness(contract: contract)
            if initial.cycle?.pendingFile != nil {
                try await WorkoutCompletenessDelivery(store: store, api: api).deliver(); return
            }
            try await captureCompletenessStream(type: HKObjectType.workoutType(), workout: nil)
            let roots = await store.snapshot().workoutCompleteness?.cycle?.deletedRoots ?? []
            for id in roots { try Task.checkCancellation(); try await deleteWorkout(id: id, api: api, scope: scope) }
            let inventory = await store.snapshot().workoutCompleteness?.cycle?.inventory ?? []
            // P0-3: one Workout's metadata/capture failure is recorded against
            // that Workout and never aborts the rest of the inventory. Only a
            // round in which every eligible Workout has a proof may stage a
            // manifest; completed proofs are never discarded.
            for id in inventory {
                try Task.checkCancellation()
                let snapshot = await store.snapshot()
                guard let flow = snapshot.workoutCompleteness, let cycle = flow.cycle else {
                    throw SyncFailure.localState("completeness_cycle_missing")
                }
                if cycle.completedWorkouts.contains(id) { continue }
                let nowUs = try VO2Max.microseconds(Date())
                if let blocked = flow.blocked?[id], !blocked.isDue(nowUs: nowUs) {
                    metrics.value("completeness_blocked_deferred", 1)
                    continue
                }
                if reusableCompletenessExclusion(id: id, cycle: cycle, reviewed: reviewed) { continue }
                let previousProof = flow.proofs[id]
                var failedType: String?
                do {
                    failedType = "workout"
                    guard let workout = try await health.workout(id) else { throw SyncFailure.localState("completeness_workout_not_visible") }
                    if reviewed {
                        // Classification is evidence, not a filter: an excluded
                        // Workout leaves a durable exclusion entry and no receipt.
                        failedType = "classification"
                        let source = Normalizer.source(workout)
                        let classification = HealthSourcePolicy.classification(source)
                        let outcome = WorkoutSourcePolicy.outcome(classification)
                        try await store.completenessClassify(id, classification: classification,
                            evidence: WorkoutSourcePolicy.evidence(source))
                        if outcome == WorkoutSourcePolicy.excluded { continue }
                        guard outcome == WorkoutSourcePolicy.eligible else { throw SyncFailure.invalid("inventory_unclassified") }
                    }
                    for type in HealthKitClient.quantityTypes + [HKSeriesType.workoutRoute()] {
                        failedType = type.identifier
                        try await captureCompletenessStream(type: type, workout: workout)
                    }
                    let captured = await store.snapshot()
                    guard let capturedFlow = captured.workoutCompleteness, let capturedCycle = capturedFlow.cycle else {
                        throw SyncFailure.localState("completeness_cycle_missing")
                    }
                    let rootChanged = (capturedCycle.captures["workouts"] ?? []).contains {
                        $0.page["additions"].list.contains(.string(id))
                    }
                    let childrenChanged = (WorkoutCompleteness.quantities + [WorkoutCompleteness.routeType]).contains { type in
                        (capturedCycle.captures[WorkoutCompleteness.key(type: type, workoutID: id)] ?? []).contains {
                            !$0.page["additions"].list.isEmpty || !$0.page["deletions"].list.isEmpty
                        }
                    }
                    let task = captured.tasks?[id]
                    let work = captured.discovery?.work[id]
                    let undelivered = (work?.generation ?? 0) > (work?.deliveredGeneration ?? 0)
                    let structuralChange = rootChanged || childrenChanged || undelivered ||
                        task?.uploadReceipt == nil || !(task?.pendingReadKeys ?? []).isEmpty
                    let previousSeries = previousProof?["series"].list ?? []
                    failedType = "series"
                    var series: [JSON] = []
                    var rebuild = structuralChange
                    var needsRefresh = structuralChange
                    if !structuralChange {
                        // No structural change: one series read decides whether
                        // the stored proof still describes the same data.
                        series = try await completenessSeries(workout)
                        needsRefresh = series.contains { current in
                            !previousSeries.contains {
                                $0["sampleType"] == current["sampleType"] && $0["sha256"] == current["sha256"] &&
                                    $0["pointCount"] == current["pointCount"]
                            }
                        }
                        rebuild = needsRefresh
                    }
                    if needsRefresh {
                        // Existing discovery owns explicit child deletion facts;
                        // drain its bounded pages before using the unchanged v3 path.
                        failedType = "discovery"
                        var drained = false
                        for _ in 0..<64 {
                            try await discoverChanges(workoutIDs: [id])
                            let discovery = await store.discoverySnapshot()
                            let flows = discovery.flows.values.filter { $0.query.workoutID == id }
                            if flows.contains(where: { $0.error != nil }) { throw SyncFailure.localState("completeness_discovery_pending") }
                            if flows.allSatisfy({ !$0.hasMore }) { drained = true; break }
                            try Task.checkCancellation()
                        }
                        guard drained else { throw SyncFailure.localState("completeness_discovery_pending") }
                        failedType = "bundle"
                        do { try await syncWorkout(id: id, secret: secret, transfers: transfers, forceNewBundle: rebuild) }
                        catch let failure as UploadError where failure.code == "workout_deleted" {
                            try await store.completenessExcludeDeletedWorkout(id); continue
                        }
                        failedType = "series"
                        series = try await completenessSeries(workout)
                    }
                    failedType = "proof"
                    guard let committed = await store.task(id), committed.uploadReceipt != nil, let hash = committed.bundleSHA256 else { throw SyncFailure.localState("completeness_fact_not_durable") }
                    for type in WorkoutCompleteness.quantities + [WorkoutCompleteness.routeType] {
                        let key = type == WorkoutCompleteness.routeType ? "route" : type
                        guard let coverage = committed.coverage[key], coverage.queryCompleted == true,
                              coverage.serverCommitted == true, [.complete, .noRecordsObserved].contains(coverage.state) else { throw SyncFailure.localState("completeness_stream_pending") }
                        if type != WorkoutCompleteness.routeType {
                            guard let coverage = committed.coverage["series:" + type], coverage.queryCompleted == true,
                                  coverage.serverCommitted == true, [.complete, .noRecordsObserved].contains(coverage.state) else { throw SyncFailure.localState("completeness_series_pending") }
                        }
                    }
                    let route = committed.coverage["route"]!
                    let proof: JSON = .object(["workoutID": .string(id), "bundleSHA256": .string(hash),
                        "routeState": .string(route.sampleCount == 0 ? "absent" : "present"), "series": .array(series)])
                    try await store.completenessProof(id, proof: proof)
                } catch is CancellationError {
                    throw CancellationError()
                } catch let failure as UploadError where failure.code == "workout_deleted" {
                    try await store.completenessExcludeDeletedWorkout(id)
                } catch {
                    let code = (error as? UploadError)?.code ?? (error as? SyncFailure).map(self.code) ?? "completeness_failed"
                    try await store.blockWorkoutCompleteness(id, code: code, failedType: failedType)
                    metrics.value("completeness_blocked", 1)
                }
            }
            let blocked = try await store.recordWorkoutCompletenessBlockedSummary()
            if let blocked {
                metrics.value("completeness_blocked_count", Double(blocked["count"].integer ?? 0))
                throw SyncFailure.localState("completeness_blocked")
            }
            try await store.stageWorkoutCompleteness()
            try await WorkoutCompletenessDelivery(store: store, api: api).deliver()
        } catch {
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            try? await store.workoutCompletenessFailure(error); throw error
        }
    }
    private func completenessRootAdditions(_ cycle: WorkoutCompletenessCycle) -> Set<String> {
        Set((cycle.captures["workouts"] ?? []).flatMap { $0.page["additions"].list.compactMap(\.text) })
    }
    /// An excluded Workout keeps its committed evidence and needs no HealthKit
    /// lookup or child query while the root stream does not re-add it.
    private func reusableCompletenessExclusion(id: String, cycle: WorkoutCompletenessCycle, reviewed: Bool) -> Bool {
        guard reviewed, let classification = cycle.classification?[id],
              WorkoutSourcePolicy.outcome(classification) == WorkoutSourcePolicy.excluded,
              cycle.exclusions?[id] != nil else { return false }
        return !completenessRootAdditions(cycle).contains(id) && !cycle.deletedRoots.contains(id)
    }
    private func captureCompletenessStream(type: HKSampleType, workout: HKWorkout?) async throws {
        let query = WorkoutCompletenessQuery(type: type.identifier, workoutID: workout?.uuid.uuidString.lowercased())
        for _ in 0..<8192 {
            try Task.checkCancellation()
            let cursor = try await store.completenessCursor(query)
            if cursor.exhausted { return }
            guard let from = await store.snapshot().workoutCompleteness?.windowFromUs else { throw SyncFailure.localState("completeness_cycle_missing") }
            let started = Date()
            let page = try await health.readCompleteness(type: type, workout: workout, anchor: cursor.anchor,
                from: Date(timeIntervalSince1970: Double(from) / 1_000_000))
            let completed = Date()
            let archive = try NSKeyedArchiver.archivedData(withRootObject: (page.samples as [NSObject]) + (page.deleted as [NSObject]), requiringSecureCoding: true)
            // Whole-page archive: the completeness and VO2 streams still store
            // one blob per page. Counted apart from the per-sample meter.
            SampleArchiveMeter.recordObjectArchive()
            try await store.captureCompletenessPage(query, previousAnchor: cursor.anchor, candidateAnchor: page.anchor,
                additions: page.samples.map { $0.uuid.uuidString.lowercased() }, deletions: page.deleted.map { $0.uuid.uuidString.lowercased() },
                hasMore: page.isFull, startedAt: started, completedAt: completed, archive: archive)
        }
        throw SyncFailure.localState("completeness_page_limit")
    }
    private func completenessSeries(_ workout: HKWorkout) async throws -> [JSON] {
        var witnesses: [JSON] = []
        for type in HealthKitClient.quantityTypes {
            try Task.checkCancellation()
            let points = try await health.bundleSeries(type: type, workout: workout)
            let completed = try VO2Max.microseconds(Date()), capture = try WorkoutCompleteness.seriesCapture(points)
            try await store.persistCompletenessSeries(capture.data)
            witnesses.append(.object(["sampleType": .string(type.identifier), "sha256": .string(Wire.hash(capture.data)),
                "pointCount": .int(Int64(capture.count)), "queryCompletedAtUs": .int(completed)]))
        }
        return witnesses
    }

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
