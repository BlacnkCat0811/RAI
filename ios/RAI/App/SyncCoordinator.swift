import Foundation
import HealthKit
import OSLog
import SyncCore

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
    private let session: URLSession
    private let sessionStore: SyncSessionStore?
    private let sessionID: String?
    private var running = false
    private var supportsIntegrity = false

    init(store: OutboxStore, health: any HealthKitReading = HealthKitClient(), session: URLSession = .shared, sessionStore: SyncSessionStore? = nil, sessionID: String? = nil) {
        self.store = store; self.health = health; self.session = session
        self.sessionStore = sessionStore; self.sessionID = sessionID
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
            if type == HKObjectType.workoutType(), let id = query.workoutID { page = try await health.readWorkout(id: id, anchor: flow.anchor) }
            else { page = try await health.read(type: type, anchor: flow.anchor, workout: workout) }
            var events: [DiscoveryEvent] = []
            for sample in page.samples {
                let archive = try NSKeyedArchiver.archivedData(withRootObject: sample, requiringSecureCoding: true)
                let file = try await store.savePayload(archive, suffix: "healthkit.archive")
                events.append(DiscoveryEvent(type: type.identifier, uuid: sample.uuid.uuidString.lowercased(), deleted: false,
                    archiveFile: file, archiveHash: Wire.hash(archive), workoutID: query.workoutID, identityHash: try? Normalizer.discoveryFingerprint(sample)))
            }
            for object in page.deleted {
                let archive = try NSKeyedArchiver.archivedData(withRootObject: object, requiringSecureCoding: true)
                let file = try await store.savePayload(archive, suffix: "healthkit.archive")
                events.append(DiscoveryEvent(type: type.identifier, uuid: object.uuid.uuidString.lowercased(), deleted: true,
                    archiveFile: file, archiveHash: Wire.hash(archive), workoutID: query.workoutID))
            }
            let batchID = try await store.stageDiscovery(query, previousAnchor: flow.anchor, anchor: page.anchor, events: events, hasMore: page.isFull)
            try await store.promoteDiscovery(query, batchID: batchID)
            return page.isFull
        } catch {
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
        let hello = try await api.call("GET", "/v2/handshake")
        if let sessionStore, let sessionID {
            try await sessionStore.validateActiveHandshake(sessionID, handshake: hello)
        }
        let supported = Set(hello["capabilities"]["sampleTypes"].list.compactMap(\.text))
        guard hello["protocolVersion"].integer == 3, let service = hello["serviceID"].text,
              let dataset = hello["datasetID"].text, let epoch = hello["datasetEpoch"].text,
              hello["capabilities"]["workoutBundles"] == .bool(true),
              hello["capabilities"]["bundleProtocolVersion"].integer == 3,
              !supported.isEmpty,
              HealthKitClient.quantityTypes.allSatisfy({ supported.contains($0.identifier) }) else {
            throw SyncFailure.invalid("unsupported_handshake")
        }
        supportsIntegrity = hello["capabilities"]["bundleObservationVersion"].integer == 1 && hello["capabilities"]["explicitChildDeletions"] == .bool(true)
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
            try await store.deferBundleRecovery(error)
            throw error
        }
        _ = try? await store.purgeLegacyPages()
        try await store.reconcileTasks()
        for task in await store.allTasks() {
            switch task.phase {
            case .preparing, .uploading:
                guard task.bundleFile != nil, task.bundleSHA256 != nil, task.bundleBoundary != nil else {
                    try? await store.updateTask(task.workoutID) {
                        $0.phase = .failed; $0.errorCode = "interrupted"; $0.updatedAt = Date()
                    }
                    continue
                }
                do { try await performSync(id: task.workoutID, api: api, scope: scope, transfers: transfers, metrics: metrics, forceNewBundle: false, recovery: true) }
                catch { /* task records the retryable failure */ }
            case .acknowledged, .analyzing:
                try? await refreshAnalysis(id: task.workoutID, api: api)
                try? await store.releaseBundle(task.workoutID)
            case .published, .partial, .deleted:
                try? await store.releaseBundle(task.workoutID)
            case .failed where task.errorCode == "interrupted":
                if task.bundleFile != nil {
                    do { try await performSync(id: task.workoutID, api: api, scope: scope, transfers: transfers, metrics: metrics, forceNewBundle: false, recovery: true) }
                    catch { }
                }
            default:
                break
            }
        }
        _ = try? await store.collectGarbage(dryRun: false)
    }

    func syncWorkout(id: String, secret: ClientConfiguration.Secret, transfers: any FileUploader, forceNewBundle: Bool) async throws {
        guard !running else { throw SyncFailure.localState("sync_already_running") }
        running = true; defer { running = false }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let (api, scope) = try await context(secret: secret, transfers: transfers, metrics: metrics)
        if await store.discoverySnapshot().work[id]?.pendingDelete == true { try await deleteWorkout(id: id, api: api, scope: scope); return }
        try await store.selectDiscoveredWorkout(id)
        try await store.beginTask(id, resetBundle: forceNewBundle)
        try await performSync(id: id, api: api, scope: scope, transfers: transfers, metrics: metrics, forceNewBundle: forceNewBundle, recovery: false)
    }

    private func performSync(id: String, api: RAIAPIClient, scope: Scope, transfers: any FileUploader,
                             metrics: SyncMetrics, forceNewBundle: Bool, recovery: Bool) async throws {
        let started = Date()
        var coverage: [String: WorkoutCoverage] = [:]
        var timings: [String: Double] = [:]
        do {
            let existing = await store.task(id)
            if let existing,
               let file = existing.bundleFile, let hash = existing.bundleSHA256, let boundary = existing.bundleBoundary,
               let url = try? await store.fileURL(file), FileManager.default.fileExists(atPath: url.path) {
                try await uploadAndAcknowledge(id: id, file: url, bundleHash: hash, boundary: boundary,
                                                coverage: existing.coverage, timings: existing.metrics,
                                                started: started, api: api, transfers: transfers)
                return
            }
            if recovery {
                throw SyncFailure.localState("bundle_not_prepared")
            }
            if let existing, existing.bundleFile != nil { throw SyncFailure.localState("missing_payload") }

            guard supportsIntegrity else { throw SyncFailure.invalid("unsupported_integrity_contract") }
            try await updateTask(id, phase: .preparing, coverage: coverage)

            // A Workout deletion is never inferred from an absent sample or a
            // failed read. Only an anchored HKDeletedObject for this exact
            // UUID can issue the explicit DELETE command. Keep the candidate
            // anchor until the bundle is acknowledged, so a failed read/upload
            // does not hide a later deletion.
            let workoutAnchor = existing?.workoutAnchor
            var nextWorkoutAnchor: Data?
            do {
                let change = try await health.readWorkout(id: id, anchor: workoutAnchor)
                nextWorkoutAnchor = change.anchor
                if change.deleted.contains(where: { $0.uuid.uuidString.lowercased() == id }) {
                    try await deleteWorkout(id: id, api: api, scope: scope)
                    return
                }
            }

            guard let workout = try await health.workout(id) else { throw SyncFailure.invalid("workout_not_visible") }
            let source = Normalizer.source(workout)
            guard HealthSourcePolicy.classification(source) == "apple_watch" else { throw SyncFailure.invalid("source_policy_pending") }
            let observedWorkout = try Normalizer.integrityEvent(workout)
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
            let quantityStart = Date()
            for type in HealthKitClient.quantityTypes {
                try Task.checkCancellation()
                do {
                    let read = try await health.bundleSamples(type: type, workout: workout)
                    var events: [JSON] = []
                    var capturedIDs = Set(await store.discoverySnapshot().events.keys)
                    for start in stride(from: 0, to: read.count, by: 1000) {
                        var captured: [DiscoveryEvent] = []
                        for sample in read[start..<min(start + 1000, read.count)] {
                            let archive = try NSKeyedArchiver.archivedData(withRootObject: sample, requiringSecureCoding: true)
                            let hash = Wire.hash(archive)
                            let identity = try? Normalizer.discoveryFingerprint(sample)
                            let eventID = DiscoveryEvent(type: type.identifier, uuid: sample.uuid.uuidString.lowercased(), deleted: false, archiveHash: hash, workoutID: id, identityHash: identity).id
                            if capturedIDs.contains(eventID) { continue }
                            capturedIDs.insert(eventID)
                            let file = try await store.savePayload(archive, suffix: "healthkit.archive")
                            captured.append(DiscoveryEvent(type: type.identifier, uuid: sample.uuid.uuidString.lowercased(), deleted: false, archiveFile: file, archiveHash: hash, workoutID: id, identityHash: identity))
                        }
                        try await store.recordCapturedFacts(captured)
                    }
                    try await store.associateDiscoveredFacts(workoutID: id, keys: read.map { type.identifier + ":" + $0.uuid.uuidString.lowercased() })
                    var eligible: [HKSample] = []
                    var typeProvenance: [JSON] = []
                    for sample in read where !deletedKeys.contains(type.identifier + ":" + sample.uuid.uuidString.lowercased()) {
                        let provenance = Normalizer.source(sample)
                        if ["apple_watch", "associated_apple_watch"].contains(HealthSourcePolicy.classification(provenance, associatedWorkout: source)) {
                            events.append(try Normalizer.integrityEvent(sample)); eligible.append(sample)
                            typeProvenance.append(.object(["sampleType": .string(type.identifier), "sampleUUID": .string(sample.uuid.uuidString.lowercased()), "source": provenance]))
                        }
                    }
                    samples += events; sampleProvenance += typeProvenance
                    coverage[type.identifier] = .result(count: events.count)
                    if eligible.count != read.count { coverage[type.identifier]?.state = .partial; coverage[type.identifier]?.diagnostics = ["source_policy_pending"] }

                    // Series failure must not discard the scalar samples that
                    // were already read for this type. Completeness describes
                    // read coverage only; absent children never prove deletion,
                    // including when an older bundle arrives after a newer one.
                    do {
                        let seriesBySample = try await health.captureSeries(type: type, workout: workout, store: store)
                        var seriesCount = 0
                        for sample in eligible {
                            guard let quantity = sample as? HKQuantitySample, quantity.count > 1 else { continue }
                            guard let points = seriesBySample[quantity.uuid.uuidString.lowercased()],
                                  Int64(points.count) == quantity.count else {
                                throw SyncFailure.invalid("quantity_series_incomplete")
                            }
                            seriesCount += points.count
                            series.append(.object(["sampleType": .string(type.identifier),
                                "sampleUUID": .string(quantity.uuid.uuidString.lowercased()),
                                "points": .array(points)]))
                        }
                        coverage["series:" + type.identifier] = .result(count: seriesCount)
                        if !events.isEmpty && eligible.count == read.count { completeSampleTypes.append(type.identifier) }
                    } catch is CancellationError { throw CancellationError() }
                    catch let failure as SyncFailure {
                        coverage["series:" + type.identifier] = WorkoutCoverage(state: .failed, sampleCount: events.count, errorCode: code(failure))
                        coverage[type.identifier]?.state = .partial
                    } catch {
                        coverage["series:" + type.identifier] = WorkoutCoverage(state: .failed, sampleCount: events.count, errorCode: "healthkit_series_read_failed")
                        coverage[type.identifier]?.state = .partial
                    }
                } catch is CancellationError { throw CancellationError() }
                catch let failure as SyncFailure {
                    coverage[type.identifier] = WorkoutCoverage(state: .failed, errorCode: code(failure))
                } catch {
                    coverage[type.identifier] = WorkoutCoverage(state: .failed, errorCode: "healthkit_read_failed")
                }
            }
            timings["healthkit_read_ms"] = Date().timeIntervalSince(quantityStart) * 1000

            var routes: [JSON] = []
            var routePointCount = 0
            var completeRoutes = false
            let routeStart = Date()
            do {
                let read = try await health.captureRoutes(workout: workout, store: store)
                coverage.merge(read.coverage) { _, new in new }
                try await store.associateDiscoveredFacts(workoutID: id, keys: read.routes.map { HKSeriesType.workoutRoute().identifier + ":" + $0.id })
                for route in read.routes where !routeDeletions.contains(route.id) {
                    guard let provenance = route.provenance, ["apple_watch", "associated_apple_watch"].contains(HealthSourcePolicy.classification(provenance, associatedWorkout: source)) else {
                        coverage["route:" + route.id] = WorkoutCoverage(state: .partial, errorCode: "source_policy_pending"); continue
                    }
                    routeProvenance.append(.object(["routeID": .string(route.id), "source": provenance]))
                    routePointCount += route.points.count
                    routes.append(.object([
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
                completeRoutes = read.enumerationCompleted && !routes.isEmpty && read.coverage.values.allSatisfy { $0.queryCompleted == true && $0.state == .complete } && routes.count == read.routes.count
                coverage["route"] = .result(count: routePointCount, completed: read.enumerationCompleted && (read.routes.isEmpty ? read.coverage.isEmpty : completeRoutes))
            } catch is CancellationError { throw CancellationError() }
            catch let failure as SyncFailure { coverage["route"] = WorkoutCoverage(state: .failed, errorCode: code(failure)) }
            catch { coverage["route"] = WorkoutCoverage(state: .failed, errorCode: "route_read_or_mapping_failed") }
            timings["route_read_ms"] = Date().timeIntervalSince(routeStart) * 1000

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
            let bundle = try WorkoutBundleAssembler.data(scope: scope, workoutID: id, workout: workoutEvent,
                                                          samples: samples, series: series, routes: routes,
                                                          completeSampleTypes: completeSampleTypes, completeRoutes: completeRoutes,
                                                          observation: observation, sampleDeletions: sampleDeletions, routeDeletions: routeDeletions)
            guard bundle.count <= 32 * 1_024 * 1_024 else { throw SyncFailure.invalid("bundle_too_large") }
            let bundleHash = Wire.hash(bundle)
            let boundary = "rai.boundary." + Wire.id()
            let multipart = WorkoutBundleAssembler.multipart(bundle: bundle, boundary: boundary)
            let fileName = try await store.savePayload(multipart, suffix: "multipart.json")
            let file = try await store.fileURL(fileName)
            try await store.setBundle(id, file: fileName, sha256: bundleHash, boundary: boundary, sampleDeletions: sampleDeletions.count, routeDeletions: routeDeletions.count, discoveryGeneration: generation, deletionEventIDs: deletions.map(\.id), pendingReadKeys: coverage.filter { $0.value.state == .failed || $0.value.state == .partial }.map(\.key).filter { $0 != "metadata" })
            timings["bundle_bytes"] = Double(bundle.count)
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
        try await refreshAnalysis(id: id, api: api)
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
            task.missing = task.coverage.filter { !$0.value.isAvailable }.keys.sorted()
        }
    }

    private func code(_ failure: SyncFailure) -> String {
        switch failure { case .invalid(let value), .localState(let value): return value; case .receiptMismatch: return "receipt_mismatch" }
    }
}
