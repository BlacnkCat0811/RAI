import Foundation
import HealthKit
import OSLog
import SyncCore

enum Metrics {
    static let log = Logger(subsystem: "com.rai.v2", category: "sync-statistics")
    static func value(_ name: String, _ value: Double) { log.info("\(name, privacy: .public)=\(value, privacy: .public)") }
    static func milliseconds(_ name: String, since: Date) { value(name, Date().timeIntervalSince(since) * 1000) }
}

actor SyncCoordinator {
    let store: OutboxStore
    let health: any HealthKitReading
    private let session: URLSession
    private var running = false
    init(store: OutboxStore, health: any HealthKitReading = HealthKitClient(), session: URLSession = .shared) { self.store = store; self.health = health; self.session = session }
    func authorize() async throws { try await health.authorize() }
    func sync(secret: ClientConfiguration.Secret, transfers: any FileUploader, readHealth: Bool = true) async throws {
        guard !running else { return }; running = true; defer { running = false }
        let metrics = SyncMetrics { name, value in Metrics.value(name, value) }
        let api = RAIAPIClient(baseURL: secret.configuration.baseURL, token: secret.token, session: session, uploader: transfers, metrics: metrics)
        let hello = try await api.call("GET", "/v2/handshake")
        guard hello["protocolVersion"].integer == 2, let service = hello["serviceID"].text,
              let dataset = hello["datasetID"].text, let epoch = hello["datasetEpoch"].text else { throw SyncFailure.invalid("unsupported_handshake") }
        let supported = Set(hello["capabilities"]["sampleTypes"].list.compactMap(\.text))
        guard hello["capabilities"]["pageReceipts"] == .bool(true), hello["capabilities"]["routes"] == .bool(true),
              hello["capabilities"]["workoutEnrichment"] == .bool(true), hello["capabilities"]["sampleWorkoutAssociations"] == .bool(true),
              hello["limits"]["events"].integer == 1000, hello["limits"]["wireBytes"].integer == 2_097_152,
              hello["limits"]["decodedBytes"].integer == 8_388_608, hello["limits"]["routePointsPerChunk"].integer == 5000,
              HealthKitClient.quantityTypes.allSatisfy({ supported.contains($0.identifier) }) else { throw SyncFailure.invalid("capability_or_limit_mismatch") }
        let scope = Scope(serviceID: service, datasetID: dataset, datasetEpoch: epoch, collectorEpoch: secret.configuration.collectorEpoch)
        try await store.bind(scope)
        let pipeline = OutboxPipeline(store: store, api: api, metrics: metrics)
        // Pause associations only while reading a quantity catch-up. A saved
        // work drain must retry due associations even without HealthKit reads.
        await pipeline.setAssociationsPaused(readHealth)
        try await pipeline.drain()
        guard readHealth else { return }
        var quantityIncomplete = false
        for type in HealthKitClient.types {
          // When the shared device bucket is exhausted, keep advancing the
          // Workout stream only: the product's newest-run view must not wait
          // behind association backfill. All other reads resume later.
          if await pipeline.pausedUntil != nil && type.identifier != HKObjectType.workoutType().identifier { break }
          do {
            // Workout pages are 25 objects and gate the product's entire
            // newest-run view. Quantity pages are 1,000 samples; allow a
            // single foreground import pass to walk the whole stream instead
            // of stopping after eight pages and requiring repeated taps.
            let isWorkout = type.identifier == HKObjectType.workoutType().identifier
            let pageLimit = isWorkout ? 40 : 500
            var reachedEnd = false
            for _ in 0..<pageLimit {
                try Task.checkCancellation()
                let snapshot = await store.snapshot()
                if snapshot.pages.contains(where: { $0.anchorKey == type.identifier }) { break }
                let stream = try await store.stream(type: type.identifier, query: "rai-v2:all-visible:unfiltered:" + type.identifier)
                let start = Date()
                let page = try await health.read(type: type, anchor: stream.deliveredAnchor, workout: nil)
                Metrics.milliseconds("healthkit_read_ms", since: start)
                let normalizeStart = Date()
                let deletes = Set(page.deleted.map { $0.uuid.uuidString.lowercased() })
                var events: [JSON] = [], supplements: [OutboxOperation] = [], workouts: [WorkoutReference] = []
                for sample in page.samples where !deletes.contains(sample.uuid.uuidString.lowercased()) && !snapshot.tombstones.contains(type.identifier + ":" + sample.uuid.uuidString.lowercased()) {
                    let event = try Normalizer.event(sample)
                    if let workout = sample as? HKWorkout {
                        let id = workout.uuid.uuidString.lowercased()
                        guard !snapshot.deletedWorkouts.contains(id) else { continue }
                        if let previous = snapshot.workouts[id], let current = previous.current {
                            let enrichment = try await enrichment(previous: current, event: event, reference: previous, scope: scope)
                            supplements += enrichment.operations; workouts.append(enrichment.reference)
                        } else {
                            workouts.append(.init(id: id, source: workout.sourceRevision.source.bundleIdentifier, current: event))
                            let w = event["workout"]
                            let detailCount = w["statistics"].list.count + w["events"].list.count + w["activities"].list.reduce(0) { $0 + 1 + $1["statistics"].list.count + $1["events"].list.count }
                            if try event.data().count > 60_000 || w["statistics"].list.count > 128 || w["events"].list.count > 256 || w["activities"].list.count > 128 || detailCount > 2000 {
                                supplements += try await Supplements.workoutPackage(event: event, scope: scope, store: store)
                            } else { events.append(event) }
                        }
                    } else {
                        events.append(event)
                        if let q = sample as? HKQuantitySample, q.count > 1 { supplements += try await health.series(q, scope: scope, store: store) }
                    }
                }
                events += page.deleted.map { Normalizer.deletion($0, type: type.identifier) }
                Metrics.milliseconds("normalization_ms", since: normalizeStart)
                Metrics.value("batch_fact_count", Double(events.count))
                let deletedWorkouts = type == HKObjectType.workoutType() ? Array(deletes) : []
                if events.isEmpty && supplements.isEmpty && page.deleted.isEmpty {
                    // A full page can contain only already-known Workouts. Persist its anchor
                    // locally instead of sealing an empty wire batch, which would consume the
                    // write/upload budget and can pin the stream in a 429 retry loop.
                    let empty = PendingPage(anchorKey: stream.type, candidateAnchor: page.anchor, operations: [],
                        workouts: workouts, deletedWorkouts: deletedWorkouts)
                    try await store.finishEmptyPage(empty)
                    if !page.isFull { reachedEnd = true; break }
                    continue
                }
                let pending = try await BatchBuilder.page(scope: scope, stream: stream, events: events, anchor: page.anchor, store: store,
                    workouts: workouts, deletedWorkouts: deletedWorkouts, supplements: supplements, metrics: metrics)
                try await store.stage(pending); try await pipeline.drain()
                if await pipeline.pausedUntil != nil && type.identifier != HKObjectType.workoutType().identifier { return }
                if !page.isFull { reachedEnd = true; break }
            }
            if !isWorkout && !reachedEnd { quantityIncomplete = true }
            try await store.readResult(key: type.identifier, error: nil)
          } catch is CancellationError { throw CancellationError() }
          catch let error as SyncFailure {
            let code: String
            switch error { case .invalid(let value), .localState(let value): code = value; case .receiptMismatch: code = "receipt_mismatch" }
            try await store.readResult(key: type.identifier, error: code)
          } catch { try await store.readResult(key: type.identifier, error: "healthkit_read_failed") }
        }
        guard await pipeline.pausedUntil == nil else { return }
        // Do not spend the user's foreground time on Workout enrichment,
        // route reads and association queries while quantity streams still
        // have pages to upload. The maintenance cursor is left untouched so
        // that once samples are complete it resumes from the same Workout
        // instead of skipping association backfill for a whole cycle.
        if quantityIncomplete {
            try await pipeline.drain()
            guard await pipeline.pausedUntil == nil else { return }
            try await store.synced()
            Metrics.value("outbox_reclaimed_entries", Double(try await store.collectGarbage(dryRun: false)))
            return
        }
        await pipeline.setAssociationsPaused(false)
        try await pipeline.drain()
        guard await pipeline.pausedUntil == nil else { return }
        // Every relationship query has its own local anchor. This catches
        // sample-first and Workout-first without blocking independent samples.
        let snapshot = await store.snapshot()
        let workouts = snapshot.workouts.values.sorted { $0.id < $1.id }
        if !workouts.isEmpty {
            let count = min(8, workouts.count)
            for offset in 0..<count {
                guard await pipeline.pausedUntil == nil else { return }
                let reference = workouts[(snapshot.maintenanceCursor + offset) % workouts.count]
                guard let workout = try await health.workout(reference.id) else { continue }
                let currentState = await store.snapshot()
                if let previous = reference.current, !currentState.pages.contains(where: { $0.workouts.contains(where: { $0.id == reference.id }) }) {
                    do {
                        let update = try await enrichment(previous: previous, event: Normalizer.event(workout), reference: reference, scope: scope)
                        if !update.operations.isEmpty {
                            let pending = PendingPage(anchorKey: "enrichment:" + reference.id, candidateAnchor: Data(), operations: update.operations, workouts: [update.reference])
                            try await store.stage(pending); try await pipeline.drain()
                            guard await pipeline.pausedUntil == nil else { return }
                        }
                        try await store.readResult(key: "enrichment:" + reference.id, error: nil)
                    } catch is CancellationError { throw CancellationError() }
                    catch let failure as SyncFailure {
                        let code: String
                        switch failure {
                        case .invalid(let value), .localState(let value): code = value
                        case .receiptMismatch: code = "receipt_mismatch"
                        }
                        try await store.readResult(key: "enrichment:" + reference.id, error: code)
                    } catch let failure as UploadError {
                        try await store.readResult(key: "enrichment:" + reference.id, error: failure.code)
                    } catch {
                        try await store.readResult(key: "enrichment:" + reference.id, error: "workout_enrichment_mapping_conflict")
                    }
                }
                do {
                    try await routes(workout: workout, reference: reference, scope: scope, pipeline: pipeline)
                    try await store.readResult(key: "route:" + reference.id, error: nil)
                } catch is CancellationError { throw CancellationError() }
                catch let failure as SyncFailure {
                    let code: String
                    switch failure {
                    case .invalid(let value), .localState(let value): code = value
                    case .receiptMismatch: code = "receipt_mismatch"
                    }
                    try await store.readResult(key: "route:" + reference.id, error: code)
                } catch let failure as UploadError {
                    try await store.readResult(key: "route:" + reference.id, error: failure.code)
                } catch {
                    try await store.readResult(key: "route:" + reference.id, error: "route_read_or_mapping_failed")
                }
                guard await pipeline.pausedUntil == nil else { return }
                for type in HealthKitClient.quantityTypes {
                    try await associations(workout: workout, reference: reference, type: type, scope: scope, pipeline: pipeline)
                    guard await pipeline.pausedUntil == nil else { return }
                }
            }
            try await store.advanceMaintenance(count)
        }
        try await pipeline.drain()
        guard await pipeline.pausedUntil == nil else { return }
        try await store.synced()
        Metrics.value("outbox_reclaimed_entries", Double(try await store.collectGarbage(dryRun: false)))
    }
    private func localPage(key: String, anchor: Data, operations: [OutboxOperation], pipeline: OutboxPipeline) async throws {
        let page = PendingPage(anchorKey: key, candidateAnchor: anchor, operations: operations)
        if operations.isEmpty { try await store.finishEmptyPage(page) }
        else { try await store.stage(page); try await pipeline.drain() }
    }
    private func routes(workout: HKWorkout, reference: WorkoutReference, scope: Scope, pipeline: OutboxPipeline) async throws {
        let key = "route:" + reference.id, snapshot = await store.snapshot()
        guard !snapshot.pages.contains(where: { $0.anchorKey == key }), !snapshot.deletedWorkouts.contains(reference.id) else { return }
        let page = try await health.read(type: HKSeriesType.workoutRoute(), anchor: snapshot.localAnchors[key], workout: workout)
        let deleted = Set(page.deleted.map { $0.uuid.uuidString.lowercased() })
        var operations: [OutboxOperation] = []
        for object in page.samples where !deleted.contains(object.uuid.uuidString.lowercased()) {
            guard let route = object as? HKWorkoutRoute else { throw SyncFailure.invalid("unexpected_route_type") }
            let start = Date(), prepared = try await health.route(route, directory: store.directory)
            Metrics.milliseconds("route_read_ms", since: start); Metrics.value("route_point_count", Double(prepared.pointCount)); Metrics.value("route_chunk_count", Double(prepared.chunks.count))
            operations += try await prepared.operations(scope: scope, routeID: route.uuid.uuidString.lowercased(), workout: reference, source: route.sourceRevision.source.bundleIdentifier, store: store)
        }
        for id in deleted {
            let request: JSON = .object(["protocolVersion": .int(2), "datasetEpoch": .string(scope.datasetEpoch), "collectorEpoch": .string(scope.collectorEpoch), "reason": .string("healthkit_deleted")])
            let file = try await store.savePayload(request.data())
            operations.append(.request(.init(method: "POST", path: "/v2/routes/" + id + "/delete", file: file, expected: ["routeID": .string(id), "ingestStatus": .string("deleted")])))
        }
        try await localPage(key: key, anchor: page.anchor, operations: operations, pipeline: pipeline)
    }
    private func associations(workout: HKWorkout, reference: WorkoutReference, type: HKQuantityType, scope: Scope, pipeline: OutboxPipeline) async throws {
        let key = "association:" + reference.id + ":" + type.identifier, snapshot = await store.snapshot()
        guard !snapshot.pages.contains(where: { $0.anchorKey == key }), !snapshot.deletedWorkouts.contains(reference.id) else { return }
        let page = try await health.read(type: type, anchor: snapshot.localAnchors[key], workout: workout)
        let deleted = Set(page.deleted.map { $0.uuid.uuidString.lowercased() })
        var operations: [OutboxOperation] = []
        for sample in page.samples where !deleted.contains(sample.uuid.uuidString.lowercased()) && !snapshot.tombstones.contains(type.identifier + ":" + sample.uuid.uuidString.lowercased()) {
            let id = Wire.id(); var request = scope.fields
            request.merge(["associationID": .string(id), "sampleType": .string(type.identifier), "sampleUUID": .string(sample.uuid.uuidString.lowercased()),
                           "workoutUUID": .string(reference.id), "source": .object(["bundleID": .string(sample.sourceRevision.source.bundleIdentifier)])]) { _, b in b }
            let file = try await store.savePayload(JSON.object(request).data())
            operations.append(.request(.init(method: "POST", path: "/v2/sync/associations", file: file,
                expected: ["sampleType": .string(type.identifier), "sampleUUID": .string(sample.uuid.uuidString.lowercased()), "workoutUUID": .string(reference.id), "ingestStatus": .string("committed")])))
        }
        try await localPage(key: key, anchor: page.anchor, operations: operations, pipeline: pipeline)
    }
    private func enrichment(previous: JSON, event: JSON, reference: WorkoutReference, scope: Scope) async throws -> (operations: [OutboxOperation], reference: WorkoutReference) {
        if previous == event { return ([], reference) }
        var old = previous.fields, new = event.fields
        var oldWorkout = previous["workout"].fields, newWorkout = event["workout"].fields
        var additions: [String: JSON] = [:]
        for (field, key) in [("totalDistance", "totalDistanceM"), ("totalEnergyBurned", "totalEnergyKcal")] {
            let before = oldWorkout.removeValue(forKey: field), after = newWorkout.removeValue(forKey: field)
            if let after, before != after { guard before == nil else { throw SyncFailure.invalid("workout_total_conflict") }; additions[key] = after["value"] }
        }
        let before = old.removeValue(forKey: "metadata")?.fields ?? [:], after = new.removeValue(forKey: "metadata")?.fields ?? [:]
        var metadata: [String: JSON] = [:]
        for (key, value) in after where before[key] != value { guard before[key] == nil else { throw SyncFailure.invalid("workout_metadata_conflict") }; metadata[key] = value }
        if !metadata.isEmpty { additions["metadata"] = .object(metadata) }
        old["workout"] = .object(oldWorkout); new["workout"] = .object(newWorkout)
        guard old == new else { throw SyncFailure.invalid("immutable_workout_changed") }
        guard !additions.isEmpty else { return ([], reference) }
        let version = reference.enrichmentVersion + 1, id = Wire.id()
        var request = scope.fields
        request.merge(["enrichmentID": .string(id), "workoutID": .string(reference.id), "source": .object(["bundleID": .string(reference.source)]),
            "sourceVersion": .int(version), "observedAtUs": .int(Normalizer.us(Date()))]) { _, b in b }
        request.merge(additions) { _, b in b }
        let file = try await store.savePayload(JSON.object(request).data())
        let operation: OutboxOperation = .request(.init(method: "POST", path: "/v2/sync/workout-enrichments", file: file,
            expected: ["enrichmentID": .string(id), "workoutID": .string(reference.id), "ingestStatus": .string("committed")]))
        var merged = event.fields, mergedWorkout = event["workout"].fields
        for key in ["totalDistance", "totalEnergyBurned"] where mergedWorkout[key] == nil { mergedWorkout[key] = previous["workout"][key] == .null ? nil : previous["workout"][key] }
        merged["workout"] = .object(mergedWorkout)
        let mergedMetadata = before.merging(after) { _, value in value }
        if !mergedMetadata.isEmpty { merged["metadata"] = .object(mergedMetadata) }
        return ([operation], .init(id: reference.id, source: reference.source, current: .object(merged), enrichmentVersion: version))
    }
}
