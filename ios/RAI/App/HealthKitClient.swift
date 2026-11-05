import Foundation
import HealthKit
import CoreLocation
import CryptoKit
import Metal
import SyncCore

struct HealthPage: Sendable {
    let samples: [HKSample]
    let deleted: [HKDeletedObject]
    let anchor: Data
    let limit: Int
    var isFull: Bool { samples.count + deleted.count >= limit }
}
struct HealthRouteBundle: Sendable {
    let id: String
    let source: String
    let points: [RoutePoint]
    var provenance: JSON? = nil
}
struct HealthRouteRead: Sendable {
    var routes: [HealthRouteBundle] = []
    var coverage: [String: WorkoutCoverage] = [:]
    var enumerationCompleted = false
}

protocol HealthKitReading: Sendable {
    func authorize() async throws
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage
    func workout(_ id: String) async throws -> HKWorkout?
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation]
    func recentWorkouts(limit: Int) async throws -> [HealthWorkoutSummary]
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample]
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]]
    func bundleRoutes(workout: HKWorkout) async throws -> [HealthRouteBundle]
    func captureSeries(type: HKQuantityType, workout: HKWorkout, store: OutboxStore) async throws -> [String: [JSON]]
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead
    func observeChanges(_ handler: @escaping @Sendable () async -> Void) async throws
    func readVO2(anchor: Data?, start: Date, limit: Int) async throws -> HealthPage
    func readCompleteness(type: HKSampleType, workout: HKWorkout?, anchor: Data?, from: Date) async throws -> HealthPage
    func observeVO2(_ handler: @escaping @Sendable () async -> Void) async throws
    func readDaily(type: HKSampleType, anchor: Data?, start: Date, limit: Int) async throws -> HealthPage
    func observeDaily(_ handler: @escaping @Sendable () async -> Void) async throws
}

extension HealthKitReading {
    func readCompleteness(type: HKSampleType, workout: HKWorkout?, anchor: Data?, from: Date) async throws -> HealthPage { throw SyncFailure.invalid("unsupported_completeness_query") }
    func readVO2(anchor: Data?, start: Date, limit: Int) async throws -> HealthPage { throw SyncFailure.invalid("healthkit_unavailable") }
    func observeVO2(_ handler: @escaping @Sendable () async -> Void) async throws {}
    func readDaily(type: HKSampleType, anchor: Data?, start: Date, limit: Int) async throws -> HealthPage { throw SyncFailure.invalid("healthkit_unavailable") }
    func observeDaily(_ handler: @escaping @Sendable () async -> Void) async throws { }
    func captureSeries(type: HKQuantityType, workout: HKWorkout, store: OutboxStore) async throws -> [String: [JSON]] { try await bundleSeries(type: type, workout: workout) }
    func observeChanges(_ handler: @escaping @Sendable () async -> Void) async throws { }
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead {
        let routes = try await bundleRoutes(workout: workout)
        return HealthRouteRead(routes: routes, coverage: [:], enumerationCompleted: true)
    }
    // Test doubles that only drain saved Outbox pages do not need HealthKit.
    func recentWorkouts(limit: Int) async throws -> [HealthWorkoutSummary] { throw SyncFailure.invalid("healthkit_unavailable") }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage { throw SyncFailure.invalid("healthkit_unavailable") }
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] { throw SyncFailure.invalid("healthkit_unavailable") }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { throw SyncFailure.invalid("healthkit_unavailable") }

    func bundleRoutes(workout: HKWorkout) async throws -> [HealthRouteBundle] { throw SyncFailure.invalid("healthkit_unavailable") }
}

actor HealthKitClient: HealthKitReading {
    private let health = HKHealthStore()
    private var observers: [HKObserverQuery] = []
    private var dailyObservers: [HKObserverQuery] = []
    private var vo2Observer: HKObserverQuery?
    static let vo2Type = HKObjectType.quantityType(forIdentifier: .vo2Max)!
    // No derived cadence: the current SDK has no HealthKit running-cadence
    // quantity identifier. Only real supported quantities are requested.
    static let identifiers: [HKQuantityTypeIdentifier] = [.heartRate, .distanceWalkingRunning, .activeEnergyBurned, .runningSpeed, .runningPower, .stepCount, .basalEnergyBurned, .runningGroundContactTime, .runningStrideLength, .runningVerticalOscillation]
    static let quantityTypes = identifiers.compactMap { HKObjectType.quantityType(forIdentifier: $0) }
    static let types: [HKSampleType] = [HKObjectType.workoutType()] + quantityTypes
    static let dailyTypes: [HKSampleType] = [HKObjectType.quantityType(forIdentifier: .heartRateVariabilitySDNN)!, HKObjectType.quantityType(forIdentifier: .restingHeartRate)!, HKObjectType.categoryType(forIdentifier: .sleepAnalysis)!]

    func authorize() async throws {
        guard HKHealthStore.isHealthDataAvailable() else { throw SyncFailure.invalid("healthkit_unavailable") }
        try await health.requestAuthorization(toShare: [], read: Set((Self.types + Self.dailyTypes + [Self.vo2Type]).map { $0 as HKObjectType } + [HKSeriesType.workoutRoute()]))
    }
    #if DEBUG
    // Explicit read-only inspection. This does not use or advance sync anchors.
    func inspectLatestRunningWorkout() async throws -> URL {
        let query = HKSampleQueryDescriptor(predicates: [.workout(HKQuery.predicateForWorkouts(with: .running))], sortDescriptors: [SortDescriptor(\HKWorkout.startDate, order: .reverse)], limit: 1)
        guard let workout = try await query.result(for: health).first else { throw SyncFailure.invalid("no_visible_running_workout") }
        var types = Set(workout.allStatistics.keys)
        for activity in workout.workoutActivities { types.formUnion(activity.allStatistics.keys) }
        let preferred = (try? await health.preferredUnits(for: types)) ?? [:]
        var report = WorkoutInspection.shape(workout, preferredUnits: preferred).fields
        var associations: [JSON] = []
        for type in types.sorted(by: { $0.identifier < $1.identifier }) {
            do {
                let samples = try await HKSampleQueryDescriptor(predicates: [.sample(type: type, predicate: HKQuery.predicateForObjects(from: workout))], sortDescriptors: [], limit: 1001).result(for: health)
                let quantities = samples.compactMap { $0 as? HKQuantitySample }
                associations.append(.object(["type": .string(type.identifier), "visibleSampleCount": .int(Int64(samples.count)), "countCappedAt1001": .bool(samples.count == 1001), "multiPointSampleCount": .int(Int64(quantities.filter { $0.count > 1 }.count)), "visibleSeriesPointCount": .int(quantities.reduce(Int64(0)) { $0 + Int64($1.count) }), "status": .string("visible_only_not_authorization_proof")]))
            } catch { associations.append(.object(["type": .string(type.identifier), "status": .string("query_failed"), "errorCode": .int(Int64((error as NSError).code))])) }
        }
        report["associatedSamples"] = .array(associations)
        do {
            let routes = try await HKSampleQueryDescriptor(predicates: [.sample(type: HKSeriesType.workoutRoute(), predicate: HKQuery.predicateForObjects(from: workout))], sortDescriptors: [], limit: 100).result(for: health).compactMap { $0 as? HKWorkoutRoute }
            report["routes"] = .object(["visibleRouteCount": .int(Int64(routes.count)), "countCappedAt100": .bool(routes.count == 100), "declaredPointCount": .int(routes.reduce(Int64(0)) { $0 + Int64($1.count) }), "coordinatesRead": .bool(false), "metadata": .array(routes.map { WorkoutInspection.metadata($0.metadata) })])
        } catch { report["routes"] = .object(["status": .string("query_failed"), "errorCode": .int(Int64((error as NSError).code))]) }
        let path = try FileManager.default.url(for: .cachesDirectory, in: .userDomainMask, appropriateFor: nil, create: true).appendingPathComponent("RAIWorkoutStructure.json")
        try OutboxStore.write(JSON.object(report).data(), to: path)
        return path
    }
    #endif
    private static func decodeAnchor(_ archive: Data?) throws -> HKQueryAnchor? {
        guard let archive else { return nil }
        do {
            guard let value = try NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: archive) else { throw SyncFailure.localState("invalid_healthkit_anchor") }
            return value
        } catch { throw SyncFailure.localState("invalid_healthkit_anchor") }
    }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout? = nil) async throws -> HealthPage {
        let previous = try Self.decodeAnchor(anchor)
        // Apple's async descriptor uses the anchored query mechanism and stops
        // its underlying query on cancellation. Never substitutes a zero anchor.
        let limit = type == HKObjectType.workoutType() ? 25 : (type == HKSeriesType.workoutRoute() ? 1 : 1000)
        let query = HKAnchoredObjectQueryDescriptor(predicates: [.sample(type: type, predicate: workout.map { HKQuery.predicateForObjects(from: $0) })], anchor: previous, limit: limit)
        let result = try await query.result(for: health)
        return HealthPage(samples: result.addedSamples, deleted: result.deletedObjects,
                          anchor: try NSKeyedArchiver.archivedData(withRootObject: result.newAnchor, requiringSecureCoding: true), limit: limit)
    }
    func workout(_ id: String) async throws -> HKWorkout? {
        guard let uuid = UUID(uuidString: id) else { throw SyncFailure.invalid("invalid_workout_uuid") }
        let query = HKSampleQueryDescriptor(predicates: [.workout(HKQuery.predicateForObject(with: uuid))], sortDescriptors: [], limit: 1)
        return try await query.result(for: health).first
    }
    func readCompleteness(type: HKSampleType, workout: HKWorkout?, anchor: Data?, from: Date) async throws -> HealthPage {
        let previous = try Self.decodeAnchor(anchor)
        let predicate: NSPredicate
        if type == HKObjectType.workoutType(), workout == nil {
            predicate = NSCompoundPredicate(andPredicateWithSubpredicates: [HKQuery.predicateForWorkouts(with: .running),
                HKQuery.predicateForSamples(withStart: from, end: nil, options: [])])
        } else {
            guard let workout, (Self.quantityTypes + [HKSeriesType.workoutRoute()]).contains(type) else { throw SyncFailure.invalid("unsupported_completeness_query") }
            predicate = HKQuery.predicateForObjects(from: workout)
        }
        let limit = type == HKObjectType.workoutType() ? 25 : 1000
        let query = HKAnchoredObjectQueryDescriptor(predicates: [.sample(type: type, predicate: predicate)], anchor: previous, limit: limit)
        let result = try await query.result(for: health)
        return HealthPage(samples: result.addedSamples, deleted: result.deletedObjects,
            anchor: try NSKeyedArchiver.archivedData(withRootObject: result.newAnchor, requiringSecureCoding: true), limit: limit)
    }
    func readDaily(type: HKSampleType, anchor: Data?, start: Date, limit: Int) async throws -> HealthPage {
        guard Self.dailyTypes.contains(type), (1...DailyHealth.maxChanges).contains(limit) else { throw SyncFailure.invalid("unsupported_type") }
        let previous = try Self.decodeAnchor(anchor)
        // The lower bound never rolls forward. Use the identical predicate on
        // bootstrap and every hot query, including intervals crossing the bound.
        let predicate = HKQuery.predicateForSamples(withStart: start, end: nil, options: [])
        let query = HKAnchoredObjectQueryDescriptor(predicates: [.sample(type: type, predicate: predicate)], anchor: previous, limit: limit)
        let result = try await query.result(for: health)
        return HealthPage(samples: result.addedSamples, deleted: result.deletedObjects,
                          anchor: try NSKeyedArchiver.archivedData(withRootObject: result.newAnchor, requiringSecureCoding: true), limit: limit)
    }
    func readVO2(anchor: Data?, start: Date, limit: Int) async throws -> HealthPage {
        guard (1...VO2Max.maxChanges).contains(limit) else { throw SyncFailure.invalid("batch_too_large") }
        let previous = try Self.decodeAnchor(anchor)
        let predicate = HKQuery.predicateForSamples(withStart: start, end: nil, options: [])
        let descriptor = HKAnchoredObjectQueryDescriptor(predicates: [.sample(type: Self.vo2Type, predicate: predicate)], anchor: previous, limit: limit)
        let result = try await descriptor.result(for: health)
        return HealthPage(samples: result.addedSamples, deleted: result.deletedObjects,
            anchor: try NSKeyedArchiver.archivedData(withRootObject: result.newAnchor, requiringSecureCoding: true), limit: limit)
    }
    func observeVO2(_ handler: @escaping @Sendable () async -> Void) async throws {
        guard vo2Observer == nil else { return }
        let query = HKObserverQuery(sampleType: Self.vo2Type, predicate: nil) { _, completion, error in
            let delivery = HealthDeliveryCompletion(completion)
            Task { if error == nil { await handler() }; delivery.finish() }
        }
        health.execute(query); vo2Observer = query
        try await health.enableBackgroundDelivery(for: Self.vo2Type, frequency: .immediate)
    }
    func observeDaily(_ handler: @escaping @Sendable () async -> Void) async throws {
        guard dailyObservers.isEmpty else { return }
        var failed = false
        for type in Self.dailyTypes {
            let query = HKObserverQuery(sampleType: type, predicate: nil) { _, completion, error in
                let delivery = HealthDeliveryCompletion(completion)
                // The handler only journals one bounded page per type. No
                // network request is held inside HealthKit's completion window.
                Task { if error == nil { await handler() }; delivery.finish() }
            }
            health.execute(query); dailyObservers.append(query)
            do { try await health.enableBackgroundDelivery(for: type, frequency: .immediate) }
            catch { failed = true }
        }
        if failed { throw SyncFailure.localState("background_delivery_unavailable") }
    }
    /// Read-only list for the user-selected sync screen. This query does not
    /// use or advance any sync anchor; only a later explicit sync reads facts.
    func recentWorkouts(limit: Int = 100) async throws -> [HealthWorkoutSummary] {
        let bounded = min(max(limit, 1), 200)
        let query = HKSampleQueryDescriptor(predicates: [.workout(HKQuery.predicateForWorkouts(with: .running))],
                                            sortDescriptors: [SortDescriptor(\HKWorkout.startDate, order: .reverse)], limit: bounded)
        let workouts = try await query.result(for: health)
        let heartRate = HKObjectType.quantityType(forIdentifier: .heartRate)
        let distanceType = HKObjectType.quantityType(forIdentifier: .distanceWalkingRunning)
        return workouts.map { workout in
            var distance = workout.totalDistance?.doubleValue(for: .meter())
            if distance == nil, let distanceType { distance = workout.statistics(for: distanceType)?.sumQuantity()?.doubleValue(for: .meter()) }
            var averageHeartRate: Double?
            if let heartRate { averageHeartRate = workout.statistics(for: heartRate)?.averageQuantity()?.doubleValue(for: HKUnit.count().unitDivided(by: .minute())) }
            let indoor: Bool?
            if let value = workout.metadata?[HKMetadataKeyIndoorWorkout] as? NSNumber { indoor = value.boolValue }
            else if let location = workout.workoutActivities.first?.workoutConfiguration.locationType { indoor = location == .indoor }
            else { indoor = nil }
            return HealthWorkoutSummary(id: workout.uuid.uuidString.lowercased(), start: workout.startDate, end: workout.endDate,
                durationS: workout.duration, distanceM: distance, averageHeartRate: averageHeartRate, isIndoor: indoor,
                activityType: Int(workout.workoutActivityType.rawValue), sourceName: workout.sourceRevision.source.name,
                deviceName: workout.device?.name)
        }
    }
    /// Anchored query scoped to exactly one Workout UUID. A page from this
    /// query can never contain another Workout, so the Outbox cannot discard a
    /// different Workout when this stream advances.
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        guard let uuid = UUID(uuidString: id) else { throw SyncFailure.invalid("invalid_workout_uuid") }
        let previous = try Self.decodeAnchor(anchor)
        let query = HKAnchoredObjectQueryDescriptor(predicates: [.sample(type: HKObjectType.workoutType(), predicate: HKQuery.predicateForObject(with: uuid))], anchor: previous, limit: 1)
        let result = try await query.result(for: health)
        return HealthPage(samples: result.addedSamples, deleted: result.deletedObjects,
                          anchor: try NSKeyedArchiver.archivedData(withRootObject: result.newAnchor, requiringSecureCoding: true), limit: 1)
    }
    /// Complete sample read scoped to one Workout. This is the only read used
    /// by the bundle path; it returns every visible sample, not a page.
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        let query = HKSampleQueryDescriptor(predicates: [.sample(type: type, predicate: HKQuery.predicateForObjects(from: workout))],
                                            sortDescriptors: [SortDescriptor(\.startDate)], limit: 50_001)
        let samples = try await query.result(for: health)
        guard samples.count <= 50_000 else { throw SyncFailure.invalid("bundle_too_large") }
        return samples
    }

    /// One query per quantity type returns every visible child point and its
    /// parent sample. This is the batched equivalent of RQrun's per-metric
    /// arrays; it never issues one HealthKit query per sample.
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] {
        let query = HKQuantitySeriesSampleQueryDescriptor(predicate: .quantitySample(type: type,
            predicate: HKQuery.predicateForObjects(from: workout)), options: [.includeSample])
        var bySample: [String: [JSON]] = [:]
        for try await value in query.results(for: health) {
            guard let sample = value.sample else { throw SyncFailure.localState("series_parent_missing") }
            let quantity = try Normalizer.quantity(value.quantity, type: type.identifier)
            bySample[sample.uuid.uuidString.lowercased(), default: []].append(.object([
                "startUs": .int(Normalizer.us(value.dateInterval.start)),
                "endUs": .int(Normalizer.us(value.dateInterval.end)),
                "value": quantity["value"],
                "unit": quantity["unit"],
            ]))
        }
        return bySample
    }

    func captureSeries(type: HKQuantityType, workout: HKWorkout, store: OutboxStore) async throws -> [String: [JSON]] {
        let name = try await store.beginFactCapture(), url = try await store.fileURL(name)
        let file = try FileHandle(forWritingTo: url); defer { try? file.close() }
        var bySample: [String: [JSON]] = [:], count = 0
        let query = HKQuantitySeriesSampleQueryDescriptor(predicate: .quantitySample(type: type, predicate: HKQuery.predicateForObjects(from: workout)), options: [.includeSample])
        for try await value in query.results(for: health) {
            try Task.checkCancellation()
            guard count < 1_000_000 else { throw SyncFailure.invalid("series_point_limit") }
            let quantity = try Normalizer.quantity(value.quantity, type: type.identifier)
            let point: JSON = .object(["startUs": .int(Normalizer.us(value.dateInterval.start)), "endUs": .int(Normalizer.us(value.dateInterval.end)), "value": quantity["value"], "unit": quantity["unit"]])
            var capture: [String: JSON] = ["point": point, "type": .string(type.identifier), "workoutID": .string(workout.uuid.uuidString.lowercased())]
            if let sample = value.sample { capture["sampleUUID"] = .string(sample.uuid.uuidString.lowercased()) }
            var bytes = try JSON.object(capture).data(); bytes.append(10)
            try file.write(contentsOf: bytes); try file.synchronize(); count += 1
            guard let sample = value.sample else { throw SyncFailure.localState("series_parent_missing") }
            bySample[sample.uuid.uuidString.lowercased(), default: []].append(point)
        }
        return bySample
    }

    func bundleRoutes(workout: HKWorkout) async throws -> [HealthRouteBundle] {
        throw SyncFailure.localState("route_capture_store_required")
    }
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead {
        // limit+1 detects truncation at the actual bundle contract (16).
        let query = HKSampleQueryDescriptor(predicates: [.sample(type: HKSeriesType.workoutRoute(), predicate: HKQuery.predicateForObjects(from: workout))], sortDescriptors: [], limit: 17)
        let objects = try await query.result(for: health)
        guard objects.count <= 16, objects.allSatisfy({ $0 is HKWorkoutRoute }) else { throw SyncFailure.invalid("route_count_limit") }
        var result = HealthRouteRead(enumerationCompleted: true)
        for object in objects {
            guard let route = object as? HKWorkoutRoute else { throw SyncFailure.invalid("unsupported_route") }
            let id = route.uuid.uuidString.lowercased(), key = "route:" + id
            var points: [RoutePoint] = []
            do {
                let checkpoint = try await store.routeCapture(id, expected: route.count)
                let capture = try RouteCaptureFile(url: await store.fileURL(checkpoint.file))
                // Successful exhaustion is the descriptor's completion signal.
                // Cancellation/error never executes finish or marks complete.
                for try await location in HKWorkoutRouteQueryDescriptor(route).results(for: health) {
                    try Task.checkCancellation()
                    let point = try Self.point(location)
                    try capture.append(point); points.append(point)
                }
                try capture.finish(expected: route.count)
                try await store.updateRouteCapture(id, count: points.count, completed: true)
                result.routes.append(HealthRouteBundle(id: id, source: route.sourceRevision.source.bundleIdentifier, points: points, provenance: Normalizer.source(route)))
                var coverage = WorkoutCoverage.result(count: points.count, expected: route.count)
                coverage.localPersisted = true
                var issues: Set<String> = []
                for index in points.indices {
                    let point = points[index]
                    if point.recordedAtUs < Normalizer.us(workout.startDate) { issues.insert("before_workout") }
                    if point.recordedAtUs > Normalizer.us(workout.endDate) { issues.insert("after_workout") }
                    if index > 0 {
                        if point.recordedAtUs == points[index-1].recordedAtUs { issues.insert("timestamp_equal") }
                        if point.recordedAtUs < points[index-1].recordedAtUs { issues.insert("timestamp_regression") }
                    }
                }
                coverage.diagnostics = issues.sorted(); result.coverage[key] = coverage
            } catch {
                let code = (error as? SyncFailure).map { failure -> String in
                    switch failure { case .invalid(let code), .localState(let code): return code; case .receiptMismatch: return "receipt_mismatch" }
                } ?? (error is CancellationError ? "cancelled" : "route_query_failed")
                try await store.updateRouteCaptureIfPresent(id, count: points.count, error: code)
                var coverage = WorkoutCoverage.result(count: points.count, completed: false, expected: route.count)
                coverage.localPersisted = true; coverage.errorCode = code; result.coverage[key] = coverage
                if error is CancellationError { throw error }
            }
        }
        return result
    }
    private static func point(_ location: CLLocation) throws -> RoutePoint {
        let seconds = location.timestamp.timeIntervalSince1970
        guard seconds.isFinite, seconds >= -62135596800, seconds < 253402300800 else { throw SyncFailure.invalid("invalid_route_timestamp") }
        return RoutePoint(recordedAtUs: Normalizer.us(location.timestamp), latitude: location.coordinate.latitude, longitude: location.coordinate.longitude,
            altitude: location.verticalAccuracy >= 0 ? location.altitude : nil,
            horizontalAccuracy: location.horizontalAccuracy >= 0 ? location.horizontalAccuracy : nil,
            verticalAccuracy: location.verticalAccuracy >= 0 ? location.verticalAccuracy : nil,
            speed: location.speed >= 0 ? location.speed : nil, course: location.course >= 0 && location.course < 360 ? location.course : nil)
    }
    func observeChanges(_ handler: @escaping @Sendable () async -> Void) async throws {
        guard observers.isEmpty else { return }
        var backgroundFailed = false
        for type in Self.types + [HKSeriesType.workoutRoute()] {
            let query = HKObserverQuery(sampleType: type, predicate: nil) { _, completion, error in
                // Notifications contain no facts. The handler journals bounded
                // anchored results; periodic foreground catch-up remains needed.
                let delivery = HealthDeliveryCompletion(completion)
                Task { await handler(); delivery.finish() }
            }
            health.execute(query); observers.append(query)
            do { try await health.enableBackgroundDelivery(for: type, frequency: .immediate) }
            catch { backgroundFailed = true }
        }
        if backgroundFailed { throw SyncFailure.localState("background_delivery_unavailable") }
    }

    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute {
        let spool = try RouteSpool(directory: directory)
        for try await location in HKWorkoutRouteQueryDescriptor(route).results(for: health) {
            try Task.checkCancellation()
            // Negative accuracy/speed/course are Apple's unavailable sentinels.
            let point = RoutePoint(recordedAtUs: Normalizer.us(location.timestamp), latitude: location.coordinate.latitude, longitude: location.coordinate.longitude,
                altitude: location.verticalAccuracy >= 0 ? location.altitude : nil,
                horizontalAccuracy: location.horizontalAccuracy >= 0 ? location.horizontalAccuracy : nil,
                verticalAccuracy: location.verticalAccuracy >= 0 ? location.verticalAccuracy : nil,
                speed: location.speed >= 0 ? location.speed : nil, course: location.course >= 0 && location.course < 360 ? location.course : nil)
            try spool.append([point])
        }
        return try spool.finish()
    }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] {
        let id = Wire.id(), type = sample.quantityType.identifier
        let path = "/v2/sync/series/" + id
        var archive = DetailSpool(directory: store.directory)
        let query = HKQuantitySeriesSampleQueryDescriptor(predicate: .quantitySample(type: sample.quantityType, predicate: HKQuery.predicateForObject(with: sample.uuid)))
        for try await value in query.results(for: health) {
            let q = try Normalizer.quantity(value.quantity, type: type)
            try archive.append(Wire.object([("startUs", .int(Normalizer.us(value.dateInterval.start))), ("endUs", .int(Normalizer.us(value.dateInterval.end))), ("value", q["value"]), ("unit", q["unit"])]))
        }
        try archive.flush()
        guard archive.count == sample.count, let last = archive.chunks.last else { throw SyncFailure.invalid("quantity_series_incomplete") }
        var registration = scope.fields; registration.removeValue(forKey: "protocolVersion")
        registration.merge(["seriesID": .string(id), "sampleType": .string(type), "sampleUUID": .string(sample.uuid.uuidString.lowercased())]) { _, b in b }
        let file = try await store.savePayload(JSON.object(registration).data())
        var operations: [OutboxOperation] = [.request(.init(method: "POST", path: "/v2/sync/series", file: file, expected: ["seriesID": .string(id), "sampleType": .string(type), "sampleUUID": .string(sample.uuid.uuidString.lowercased())]))]
        operations += archive.uploadOperations(path: path, mediaType: "application/x-rai-health-series+ndjson", idKey: "seriesID", id: id, digestKey: "chunkSHA256")
        let seal: JSON = .object(["chunkCount": .int(Int64(archive.chunks.count)), "totalPoints": .int(archive.count), "manifestSHA256": .string(last.manifest)])
        let sealFile = try await store.savePayload(seal.data())
        operations.append(.request(.init(method: "POST", path: path + "/seal", file: sealFile, expected: ["seriesID": .string(id), "ingestStatus": .string("sealed"), "manifestSHA256": .string(last.manifest), "totalPoints": .int(archive.count)])))
        return operations
    }
}

#if DEBUG
enum WorkoutInspection {
    static func fingerprint(_ value: String) -> String { SHA256.hash(data: Data(value.utf8)).map { String(format: "%02x", $0) }.joined() }
    /// Review diagnostics contain key digests and carrier counts only. Even
    /// arbitrary metadata keys may contain identifying text, so never emit them.
    static func blockedMetadataShape(_ workout: HKWorkout) -> JSON {
        let allowed = Set([HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout, HKMetadataKeyAverageMETs,
            HKMetadataKeyElevationAscended, HKMetadataKeyElevationDescended, HKMetadataKeyWeatherHumidity,
            HKMetadataKeyWeatherTemperature, HKMetadataKeyAverageSpeed, HKMetadataKeyMaximumSpeed])
        var counts: [String: Int] = [:]
        func inspect(_ context: String, _ metadata: [String: Any]?) {
            for (key, value) in metadata ?? [:] where !allowed.contains(key) {
                let carrier: String
                if let number = value as? NSNumber { carrier = "NSNumber:" + String(cString: number.objCType) }
                else if value is HKQuantity { carrier = "HKQuantity" }
                else if value is String { carrier = "String" }
                else if value is Date { carrier = "Date" }
                else if value is Data { carrier = "Data" }
                else if value is [Any] { carrier = "Array" }
                else { carrier = "Other" }
                counts[context + "|key-sha256:" + fingerprint(key) + "|" + carrier, default: 0] += 1
            }
        }
        inspect("workout", workout.metadata)
        for activity in workout.workoutActivities {
            inspect("activity", activity.metadata)
            for event in activity.workoutEvents { inspect("activity_event", event.metadata) }
        }
        for event in workout.workoutEvents ?? [] { inspect("workout_event", event.metadata) }
        return .object(["unsupportedFieldCount": .int(Int64(counts.values.reduce(0, +))),
            "contextKeyCarrierCounts": .object(counts.mapValues { .int(Int64($0)) })])
    }
    static func metadata(_ values: [String: Any]?) -> JSON {
        .array((values ?? [:]).sorted { $0.key < $1.key }.map { key, value in
            let kind: String
            switch value {
            case is HKQuantity: kind = "quantity"
            case is String: kind = "string"
            case is NSNumber: kind = "number"
            case is Date: kind = "date"
            case is [Any]: kind = "array"
            case is Data: kind = "data"
            default: kind = "other"
            }
            return .object(["key": .string(key), "valueType": .string(kind), "normalizerAllowlisted": .bool([HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout, HKMetadataKeyAverageMETs, HKMetadataKeyElevationAscended, HKMetadataKeyElevationDescended, HKMetadataKeyWeatherHumidity, HKMetadataKeyWeatherTemperature, HKMetadataKeyAverageSpeed, HKMetadataKeyMaximumSpeed].contains(key))])
        })
    }
    static func statistics(_ values: [HKQuantityType: HKStatistics], preferredUnits: [HKQuantityType: HKUnit]) -> JSON {
        .array(values.sorted { $0.key.identifier < $1.key.identifier }.map { type, stat in
            .object(["type": .string(type.identifier), "preferredDisplayUnit": preferredUnits[type].map { .string($0.unitString) } ?? .null, "canonicalUnitInBuild": Normalizer.units[type.identifier].map(JSON.string) ?? .null, "durationS": .number(stat.endDate.timeIntervalSince(stat.startDate)), "aggregatesPresent": .array([("sum", stat.sumQuantity()), ("average", stat.averageQuantity()), ("minimum", stat.minimumQuantity()), ("maximum", stat.maximumQuantity())].compactMap { $0.1 == nil ? nil : .string($0.0) })])
        })
    }
    static func events(_ values: [HKWorkoutEvent]?) -> JSON {
        .array((values ?? []).map { .object(["eventType": .int(Int64($0.type.rawValue)), "durationS": .number($0.dateInterval.duration), "metadata": metadata($0.metadata)]) })
    }
    static func shape(_ workout: HKWorkout, preferredUnits: [HKQuantityType: HKUnit]) -> JSON {
        .object([
            "reportVersion": .int(1), "scope": .string("latest_visible_running_workout_complete_structure; associated query counts bounded; no health measurement values"),
            "metalDeviceAvailable": .bool(MTLCreateSystemDefaultDevice() != nil),
            "workoutFingerprint": .string(fingerprint(workout.uuid.uuidString)), "sourceFingerprint": .string(fingerprint(workout.sourceRevision.source.bundleIdentifier)),
            "activityType": .int(Int64(workout.workoutActivityType.rawValue)), "durationS": .number(workout.duration), "elapsedS": .number(workout.endDate.timeIntervalSince(workout.startDate)),
            "fieldsPresent": .object(["startDate": .bool(true), "endDate": .bool(true), "totalDistance": .bool(workout.totalDistance != nil), "device": .bool(workout.device != nil)]),
            "metadata": metadata(workout.metadata), "statistics": statistics(workout.allStatistics, preferredUnits: preferredUnits), "events": events(workout.workoutEvents),
            "activities": .array(workout.workoutActivities.enumerated().map { index, activity in
                .object(["ordinal": .int(Int64(index)), "activityType": .int(Int64(activity.workoutConfiguration.activityType.rawValue)), "endDatePresent": .bool(activity.endDate != nil), "durationS": .number(activity.duration), "lapLengthPresent": .bool(activity.workoutConfiguration.lapLength != nil), "metadata": metadata(activity.metadata), "statistics": statistics(activity.allStatistics, preferredUnits: preferredUnits), "events": events(activity.workoutEvents)])
            }),
            "healthValuesIncluded": .bool(false), "gpsIncluded": .bool(false), "fullUUIDsIncluded": .bool(false), "anchorsReadOrAdvanced": .bool(false),
        ])
    }
}
#endif

enum Normalizer {
    static func us(_ date: Date) -> Int64 { Int64((date.timeIntervalSince1970 * 1_000_000).rounded()) }
    static let units: [String: String] = [
        "HKQuantityTypeIdentifierHeartRate": "count/min", "HKQuantityTypeIdentifierRestingHeartRate": "count/min",
        "HKQuantityTypeIdentifierWalkingHeartRateAverage": "count/min", "HKQuantityTypeIdentifierHeartRateVariabilitySDNN": "ms",
        "HKQuantityTypeIdentifierStepCount": "count", "HKQuantityTypeIdentifierDistanceWalkingRunning": "m",
        "HKQuantityTypeIdentifierActiveEnergyBurned": "kcal", "HKQuantityTypeIdentifierBasalEnergyBurned": "kcal",
        "HKQuantityTypeIdentifierVO2Max": "mL/kg/min", "HKQuantityTypeIdentifierBodyMass": "kg",
        "HKQuantityTypeIdentifierRespiratoryRate": "count/min", "HKQuantityTypeIdentifierOxygenSaturation": "1",
        "HKQuantityTypeIdentifierRunningSpeed": "m/s", "HKQuantityTypeIdentifierWalkingSpeed": "m/s", "HKQuantityTypeIdentifierRunningPower": "W",
        "HKQuantityTypeIdentifierRunningGroundContactTime": "ms",
        "HKQuantityTypeIdentifierRunningStrideLength": "m", "HKQuantityTypeIdentifierRunningVerticalOscillation": "m",
        "HKQuantityTypeIdentifierDistanceCycling": "m", "HKQuantityTypeIdentifierDistanceSwimming": "m",
        "HKQuantityTypeIdentifierDistanceWheelchair": "m", "HKQuantityTypeIdentifierDistanceDownhillSnowSports": "m",
        "HKQuantityTypeIdentifierSwimmingStrokeCount": "count", "HKQuantityTypeIdentifierFlightsClimbed": "count",
        "HKQuantityTypeIdentifierCyclingPower": "W", "HKQuantityTypeIdentifierCyclingSpeed": "m/s",
        "HKQuantityTypeIdentifierCyclingCadence": "count/min", "HKQuantityTypeIdentifierWalkingCadence": "count/min",
        "HKQuantityTypeIdentifierRunningCadence": "count/min", "HKQuantityTypeIdentifierHeartRateRecoveryOneMinute": "count/min"
    ]
    static func hkUnit(_ canonical: String) throws -> HKUnit {
        switch canonical {
        case "count/min": .count().unitDivided(by: .minute())
        case "m": .meter()
        case "m/s": .meter().unitDivided(by: .second())
        case "kcal": .kilocalorie()
        case "W": .watt()
        case "count": .count()
        case "ms": .secondUnit(with: .milli)
        case "kg": .gramUnit(with: .kilo)
        case "1": .percent()
        case "mL/kg/min": HKUnit.literUnit(with: .milli).unitDivided(by: .gramUnit(with: .kilo).unitMultiplied(by: .minute()))
        default: throw SyncFailure.invalid("unsupported_unit")
        }
    }
    static func quantity(_ q: HKQuantity, type: String) throws -> JSON {
        guard let unit = units[type] else { throw SyncFailure.invalid("unsupported_quantity_type:" + type) }
        let hk = try hkUnit(unit)
        guard q.is(compatibleWith: hk) else { throw SyncFailure.invalid("unsupported_unit") }
        let value = q.doubleValue(for: hk)
        guard value.isFinite else { throw SyncFailure.invalid("nonfinite_quantity") }
        return .object(["value": .number(value), "unit": .string(unit)])
    }
    static let sampleMetadataEnumKeys = [HKMetadataKeyHeartRateMotionContext, HKMetadataKeyHeartRateSensorLocation,
                                         HKMetadataKeyUserMotionContext, HKMetadataKeyDevicePlacementSide]
    static func metadata(_ metadata: [String: Any]?) -> JSON? {
        var output: [String: JSON] = [:]
        // Explicit allowlist. No arbitrary HealthKit metadata serialization.
        if let zone = metadata?[HKMetadataKeyTimeZone] as? String { output[HKMetadataKeyTimeZone] = .string(zone) }
        if let indoor = metadata?[HKMetadataKeyIndoorWorkout] as? NSNumber { output[HKMetadataKeyIndoorWorkout] = .bool(indoor.boolValue) }
        for key in sampleMetadataEnumKeys {
            if let value = metadata?[key] as? NSNumber { output[key] = .int(Int64(value.intValue)) }
        }
        return output.isEmpty ? nil : .object(output)
    }
    // Keep the existing sample mapping immutable. The inspected Workout's
    // additional source quantities use the existing JSON metadata field.
    static func workoutMetadata(_ metadata: [String: Any]?) throws -> JSON? {
        let values = metadata ?? [:]
        let units: [String: (HKUnit, String)] = [
            HKMetadataKeyAverageMETs: (.kilocalorie().unitDivided(by: .gramUnit(with: .kilo).unitMultiplied(by: .hour())), "kcal/(kg*hr)"),
            HKMetadataKeyElevationAscended: (.meter(), "m"),
            HKMetadataKeyElevationDescended: (.meter(), "m"),
            HKMetadataKeyWeatherHumidity: (.percent(), "1"),
            HKMetadataKeyWeatherTemperature: (.degreeCelsius(), "degC"),
            HKMetadataKeyAverageSpeed: (.meter().unitDivided(by: .second()), "m/s"),
            HKMetadataKeyMaximumSpeed: (.meter().unitDivided(by: .second()), "m/s"),
        ]
        let allowed = Set(units.keys).union([HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout])
        if let zone = values[HKMetadataKeyTimeZone] {
            guard let text = zone as? String, !text.isEmpty else { throw SyncFailure.invalid("unsupported_workout_metadata_type") }
        }
        if let indoor = values[HKMetadataKeyIndoorWorkout], !(indoor is NSNumber) {
            throw SyncFailure.invalid("unsupported_workout_metadata_type")
        }
        var output = self.metadata(values)?.fields ?? [:]
        for (key, (unit, label)) in units where values[key] != nil {
            guard let quantity = values[key] as? HKQuantity, quantity.is(compatibleWith: unit) else { throw SyncFailure.invalid("unsupported_workout_metadata_unit:" + key) }
            let value = quantity.doubleValue(for: unit)
            guard value.isFinite else { throw SyncFailure.invalid("nonfinite_workout_metadata") }
            output[key] = .object(["value": .number(value), "unit": .string(label)])
        }
        // Strict allowlist. Unknown metadata remains in the secure HealthKit
        // discovery archive and stops this upload for explicit review; it is
        // never copied into an arbitrary server JSON map.
        guard Set(values.keys).isSubset(of: allowed) else { throw SyncFailure.invalid("metadata_requires_review") }
        guard output.count <= 64 else { throw SyncFailure.invalid("workout_metadata_limit") }
        return output.isEmpty ? nil : .object(output)
    }

    /// Client provenance is an enrichment, not part of the immutable Workout
    /// identity event. Keeping it separate preserves byte-for-byte retries of
    /// Workouts that were uploaded by earlier client builds.
    static func workoutProvenance(version: String?, productType: String?, device: HKDevice?) -> [String: JSON] {
        var fields: [String: JSON] = [:]
        if let version { fields["RAI:sourceRevisionVersion"] = .string(version) }
        if let productType { fields["RAI:sourceProductType"] = .string(productType) }
        if let device {
            for (key, value) in [("RAI:sourceDeviceManufacturer", device.manufacturer),
                                 ("RAI:sourceDeviceModel", device.model), ("RAI:sourceDeviceHardwareVersion", device.hardwareVersion),
                                 ("RAI:sourceDeviceSoftwareVersion", device.softwareVersion)] {
                if let value { fields[key] = .string(value) }
            }
        }
        return fields
    }

    static func source(_ object: HKObject) -> JSON {
        let revision = object.sourceRevision
        var fields: [String: JSON] = ["bundleID": .string(revision.source.bundleIdentifier), "name": .string(revision.source.name)]
        if let version = revision.version { fields["version"] = .string(version) }
        if let product = revision.productType { fields["productType"] = .string(product) }
        let os = revision.operatingSystemVersion
        if os.majorVersion > 0 { fields["operatingSystemVersion"] = .string("\(os.majorVersion).\(os.minorVersion).\(os.patchVersion)") }
        if let device = object.device {
            var descriptor: [String: JSON] = [:]
            for (key,value) in [("manufacturer",device.manufacturer),("model",device.model),("hardwareVersion",device.hardwareVersion),("firmwareVersion",device.firmwareVersion),("softwareVersion",device.softwareVersion)] {
                if let value { descriptor[key] = .string(value) }
            }
            if !descriptor.isEmpty { fields["device"] = .object(descriptor) }
        }
        return .object(fields)
    }
    /// Secure archives prove recovery, while this supported-fact fingerprint
    /// avoids treating archive object graph differences as new HealthKit facts.
    /// Unsupported metadata falls back to the full archive identity and remains
    /// available for review instead of being dropped by a partial fingerprint.
    static func discoveryFingerprint(_ sample: HKSample) throws -> String {
        let event: JSON
        if let route = sample as? HKWorkoutRoute {
            guard Set((route.metadata ?? [:]).keys).isSubset(of: [HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout]) else { throw SyncFailure.invalid("metadata_requires_review") }
            event = .object(["uuid": .string(route.uuid.uuidString.lowercased()), "type": .string(route.sampleType.identifier),
                "startUs": .int(us(route.startDate)), "endUs": .int(us(route.endDate)), "pointCount": .int(Int64(route.count)),
                "metadata": try workoutMetadata(route.metadata) ?? .object([:])])
        } else { event = try integrityEvent(sample) }
        return Wire.hash(try JSON.object(["fact": event, "source": source(sample)]).data())
    }
    static func integrityEvent(_ sample: HKSample) throws -> JSON {
        // Unknown metadata stays in the secure local discovery archive. It is
        // not serialized into an arbitrary server JSON map.
        let workoutAllowed = Set([HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout, HKMetadataKeyAverageMETs,
            HKMetadataKeyElevationAscended, HKMetadataKeyElevationDescended, HKMetadataKeyWeatherHumidity,
            HKMetadataKeyWeatherTemperature, HKMetadataKeyAverageSpeed, HKMetadataKeyMaximumSpeed])
        let sampleAllowed = Set([HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout] + sampleMetadataEnumKeys)
        func validate(_ metadata: [String: Any]?, allowed: Set<String>) throws {
            guard Set((metadata ?? [:]).keys).isSubset(of: allowed) else { throw SyncFailure.invalid("metadata_requires_review") }
        }
        if sample is HKWorkout {
            try validate(sample.metadata, allowed: workoutAllowed)
        } else {
            try validate(sample.metadata, allowed: sampleAllowed)
        }
        if let zone = sample.metadata?[HKMetadataKeyTimeZone], !(zone is String) { throw SyncFailure.invalid("unsupported_sample_metadata_type") }
        if let indoor = sample.metadata?[HKMetadataKeyIndoorWorkout], !(indoor is NSNumber) { throw SyncFailure.invalid("unsupported_sample_metadata_type") }
        for key in sampleMetadataEnumKeys {
            if let value = sample.metadata?[key] {
                guard let number = value as? NSNumber, (0...10).contains(number.intValue) else { throw SyncFailure.invalid("unsupported_sample_metadata_type") }
            }
        }
        if let workout = sample as? HKWorkout {
            for activity in workout.workoutActivities {
                try validate(activity.metadata, allowed: workoutAllowed)
                for event in activity.workoutEvents { try validate(event.metadata, allowed: workoutAllowed) }
            }
            for event in workout.workoutEvents ?? [] { try validate(event.metadata, allowed: workoutAllowed) }
        }
        return try event(sample)
    }

    static func event(_ sample: HKSample) throws -> JSON {
        var object: [String: JSON] = ["op": .string("upsert"), "kind": .string(sample is HKWorkout ? "workout" : "sample"),
            "type": .string(sample.sampleType.identifier), "uuid": .string(sample.uuid.uuidString.lowercased()),
            "startUs": .int(us(sample.startDate)), "endUs": .int(us(sample.endDate)),
            "source": .object(["bundleID": .string(sample.sourceRevision.source.bundleIdentifier)])]
        // Workout identity is frozen: only HealthKit's own workout metadata is
        // part of this event. Client provenance (source revision and device
        // descriptors) is a supplement and must be sent through
        // /v2/sync/workout-enrichments, never through this immutable payload.
        if sample is HKWorkout {
            if let m = try workoutMetadata(sample.metadata) { object["metadata"] = m }
        } else if let m = metadata(sample.metadata) { object["metadata"] = m }
        if let workout = sample as? HKWorkout { object["workout"] = try self.workout(workout) }
        else if let sample = sample as? HKQuantitySample { object["quantity"] = try quantity(sample.quantity, type: sample.quantityType.identifier) }
        else { throw SyncFailure.invalid("unsupported_sample") }
        return .object(object)
    }
    static func deletion(_ object: HKDeletedObject, type: String) -> JSON {
        .object(["op": .string("delete"), "kind": .string(type == HKObjectType.workoutType().identifier ? "workout" : "sample"), "type": .string(type), "uuid": .string(object.uuid.uuidString.lowercased())])
    }
    static func statistics(_ values: [HKQuantityType: HKStatistics]) throws -> [JSON] {
        let unsupported = values.keys.map(\.identifier).filter { units[$0] == nil }.sorted()
        guard unsupported.isEmpty else { throw SyncFailure.invalid("unsupported_workout_quantity_types:" + unsupported.joined(separator: ",")) }
        return try values.sorted { $0.key.identifier < $1.key.identifier }.map { type, stat in
            var object: [String: JSON] = ["type": .string(type.identifier), "startUs": .int(us(stat.startDate)), "endUs": .int(us(stat.endDate))]
            for (key, quantity) in [("sum", stat.sumQuantity()), ("average", stat.averageQuantity()), ("minimum", stat.minimumQuantity()), ("maximum", stat.maximumQuantity())] {
                if let quantity { object[key] = try self.quantity(quantity, type: type.identifier) }
            }
            guard object.count > 3 else { throw SyncFailure.invalid("empty_workout_statistic") }
            return .object(object)
        }
    }
    static func events(_ events: [HKWorkoutEvent]?) throws -> [JSON] {
        try (events ?? []).map { event in
            var object: [String: JSON] = ["type": .int(Int64(event.type.rawValue)), "startUs": .int(us(event.dateInterval.start)), "endUs": .int(us(event.dateInterval.end))]
            if let m = try workoutMetadata(event.metadata) { object["metadata"] = m }; return .object(object)
        }
    }
    static func workout(_ w: HKWorkout) throws -> JSON {
        var object: [String: JSON] = ["activityType": .int(Int64(w.workoutActivityType.rawValue)), "durationUs": .int(Int64((w.duration * 1_000_000).rounded()))]
        if let distance = w.totalDistance { object["totalDistance"] = try quantity(distance, type: HKQuantityTypeIdentifier.distanceWalkingRunning.rawValue) }
        if let energy = w.statistics(for: HKObjectType.quantityType(forIdentifier: .activeEnergyBurned)!)?.sumQuantity() {
            object["totalEnergyBurned"] = try quantity(energy, type: HKQuantityTypeIdentifier.activeEnergyBurned.rawValue)
        }
        let stats = try statistics(w.allStatistics)
        if !stats.isEmpty { object["statistics"] = .array(stats) }
        let events = try events(w.workoutEvents)
        if !events.isEmpty { object["events"] = .array(events) }
        let activities: [JSON] = try w.workoutActivities.map { a in
            let c = a.workoutConfiguration
            var config: [String: JSON] = ["activityType": .int(Int64(c.activityType.rawValue)), "locationType": .int(Int64(c.locationType.rawValue)), "swimmingLocationType": .int(Int64(c.swimmingLocationType.rawValue))]
            if let length = c.lapLength { config["lapLength"] = try quantity(length, type: HKQuantityTypeIdentifier.distanceWalkingRunning.rawValue) }
            var activity: [String: JSON] = ["uuid": .string(a.uuid.uuidString.lowercased()), "startUs": .int(us(a.startDate)),
                "durationUs": .int(Int64((a.duration * 1_000_000).rounded())), "configuration": .object(config)]
            if let end = a.endDate { activity["endUs"] = .int(us(end)) }
            if let m = try workoutMetadata(a.metadata) { activity["metadata"] = m }
            let stats = try statistics(a.allStatistics); if !stats.isEmpty { activity["statistics"] = .array(stats) }
            let events = try self.events(a.workoutEvents); if !events.isEmpty { activity["events"] = .array(events) }
            return .object(activity)
        }
        if !activities.isEmpty { object["activities"] = .array(activities) }
        return .object(object)
    }
}

private final class HealthDeliveryCompletion: @unchecked Sendable {
    private let completion: () -> Void
    init(_ completion: @escaping () -> Void) { self.completion = completion }
    func finish() { completion() }
}

#if DEBUG
extension HealthKitClient {
    /// Device acceptance only: fixed user-authorized test window, never a
    /// history enumeration. After locating one object, all reads use its UUID.
    func phase3TestWorkout() async throws -> HKWorkout {
        let start = ISO8601DateFormatter().date(from: "2026-10-26T11:35:00Z")!
        let end = ISO8601DateFormatter().date(from: "2026-10-26T12:05:00Z")!
        let predicate = NSCompoundPredicate(andPredicateWithSubpredicates: [HKQuery.predicateForWorkouts(with: .running), HKQuery.predicateForSamples(withStart: start, end: end, options: .strictStartDate)])
        let values = try await HKSampleQueryDescriptor(predicates: [.workout(predicate)], sortDescriptors: [], limit: 3).result(for: health)
        guard values.count == 1 else { throw SyncFailure.localState(values.isEmpty ? "test_workout_not_observed" : "test_workout_ambiguous") }
        return values[0]
    }
}
#endif
