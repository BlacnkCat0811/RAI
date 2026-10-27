import Foundation
import HealthKit
import CoreLocation
import SyncCore

struct HealthPage: Sendable {
    let samples: [HKSample]
    let deleted: [HKDeletedObject]
    let anchor: Data
    let limit: Int
    var isFull: Bool { samples.count + deleted.count >= limit }
}

protocol HealthKitReading: Sendable {
    func authorize() async throws
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage
    func workout(_ id: String) async throws -> HKWorkout?
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation]
}

actor HealthKitClient: HealthKitReading {
    private let health = HKHealthStore()
    // No derived cadence: the current SDK has no HealthKit running-cadence
    // quantity identifier. Only real supported quantities are requested.
    static let identifiers: [HKQuantityTypeIdentifier] = [.heartRate, .distanceWalkingRunning, .activeEnergyBurned, .runningSpeed, .runningPower, .stepCount]
    static let quantityTypes = identifiers.compactMap { HKObjectType.quantityType(forIdentifier: $0) }
    static let types: [HKSampleType] = [HKObjectType.workoutType()] + quantityTypes

    func authorize() async throws {
        guard HKHealthStore.isHealthDataAvailable() else { throw SyncFailure.invalid("healthkit_unavailable") }
        try await health.requestAuthorization(toShare: [], read: Set(Self.types.map { $0 as HKObjectType } + [HKSeriesType.workoutRoute()]))
    }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout? = nil) async throws -> HealthPage {
        let previous: HKQueryAnchor?
        if let anchor {
            guard let decoded = try NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: anchor) else { throw SyncFailure.localState("invalid_healthkit_anchor") }
            previous = decoded
        } else { previous = nil }
        // Apple's async descriptor uses the anchored query mechanism and stops
        // its underlying query on cancellation. Never substitutes a zero anchor.
        let limit = type == HKObjectType.workoutType() ? 25 : (type == HKSeriesType.workoutRoute() ? 1 : 250)
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

enum Normalizer {
    static func us(_ date: Date) -> Int64 { Int64((date.timeIntervalSince1970 * 1_000_000).rounded()) }
    static let units: [String: String] = [
        "HKQuantityTypeIdentifierHeartRate": "count/min", "HKQuantityTypeIdentifierRestingHeartRate": "count/min",
        "HKQuantityTypeIdentifierWalkingHeartRateAverage": "count/min", "HKQuantityTypeIdentifierHeartRateVariabilitySDNN": "ms",
        "HKQuantityTypeIdentifierStepCount": "count", "HKQuantityTypeIdentifierDistanceWalkingRunning": "m",
        "HKQuantityTypeIdentifierActiveEnergyBurned": "kcal", "HKQuantityTypeIdentifierBasalEnergyBurned": "kcal",
        "HKQuantityTypeIdentifierVO2Max": "mL/kg/min", "HKQuantityTypeIdentifierBodyMass": "kg",
        "HKQuantityTypeIdentifierRespiratoryRate": "count/min", "HKQuantityTypeIdentifierOxygenSaturation": "1",
        "HKQuantityTypeIdentifierRunningSpeed": "m/s", "HKQuantityTypeIdentifierWalkingSpeed": "m/s", "HKQuantityTypeIdentifierRunningPower": "W"
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
    static func metadata(_ metadata: [String: Any]?) -> JSON? {
        var output: [String: JSON] = [:]
        // Explicit allowlist. No arbitrary HealthKit metadata serialization.
        if let zone = metadata?[HKMetadataKeyTimeZone] as? String { output[HKMetadataKeyTimeZone] = .string(zone) }
        if let indoor = metadata?[HKMetadataKeyIndoorWorkout] as? NSNumber { output[HKMetadataKeyIndoorWorkout] = .bool(indoor.boolValue) }
        return output.isEmpty ? nil : .object(output)
    }
    static func event(_ sample: HKSample) throws -> JSON {
        var object: [String: JSON] = ["op": .string("upsert"), "kind": .string(sample is HKWorkout ? "workout" : "sample"),
            "type": .string(sample.sampleType.identifier), "uuid": .string(sample.uuid.uuidString.lowercased()),
            "startUs": .int(us(sample.startDate)), "endUs": .int(us(sample.endDate)),
            "source": .object(["bundleID": .string(sample.sourceRevision.source.bundleIdentifier)])]
        if let m = metadata(sample.metadata) { object["metadata"] = m }
        if let workout = sample as? HKWorkout { object["workout"] = try self.workout(workout) }
        else if let sample = sample as? HKQuantitySample { object["quantity"] = try quantity(sample.quantity, type: sample.quantityType.identifier) }
        else { throw SyncFailure.invalid("unsupported_sample") }
        return .object(object)
    }
    static func deletion(_ object: HKDeletedObject, type: String) -> JSON {
        .object(["op": .string("delete"), "kind": .string(type == HKObjectType.workoutType().identifier ? "workout" : "sample"), "type": .string(type), "uuid": .string(object.uuid.uuidString.lowercased())])
    }
    static func statistics(_ values: [HKQuantityType: HKStatistics]) throws -> [JSON] {
        try values.sorted { $0.key.identifier < $1.key.identifier }.map { type, stat in
            var object: [String: JSON] = ["type": .string(type.identifier), "startUs": .int(us(stat.startDate)), "endUs": .int(us(stat.endDate))]
            for (key, quantity) in [("sum", stat.sumQuantity()), ("average", stat.averageQuantity()), ("minimum", stat.minimumQuantity()), ("maximum", stat.maximumQuantity())] {
                if let quantity { object[key] = try self.quantity(quantity, type: type.identifier) }
            }
            guard object.count > 3 else { throw SyncFailure.invalid("empty_workout_statistic") }
            return .object(object)
        }
    }
    static func events(_ events: [HKWorkoutEvent]?) -> [JSON] {
        (events ?? []).map { event in
            var object: [String: JSON] = ["type": .int(Int64(event.type.rawValue)), "startUs": .int(us(event.dateInterval.start)), "endUs": .int(us(event.dateInterval.end))]
            if let m = metadata(event.metadata) { object["metadata"] = m }; return .object(object)
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
        let events = events(w.workoutEvents)
        if !events.isEmpty { object["events"] = .array(events) }
        let activities: [JSON] = try w.workoutActivities.map { a in
            let c = a.workoutConfiguration
            var config: [String: JSON] = ["activityType": .int(Int64(c.activityType.rawValue)), "locationType": .int(Int64(c.locationType.rawValue)), "swimmingLocationType": .int(Int64(c.swimmingLocationType.rawValue))]
            if let length = c.lapLength { config["lapLength"] = try quantity(length, type: HKQuantityTypeIdentifier.distanceWalkingRunning.rawValue) }
            var activity: [String: JSON] = ["uuid": .string(a.uuid.uuidString.lowercased()), "startUs": .int(us(a.startDate)),
                "durationUs": .int(Int64((a.duration * 1_000_000).rounded())), "configuration": .object(config)]
            if let end = a.endDate { activity["endUs"] = .int(us(end)) }
            if let m = metadata(a.metadata) { activity["metadata"] = m }
            let stats = try statistics(a.allStatistics); if !stats.isEmpty { activity["statistics"] = .array(stats) }
            let events = self.events(a.workoutEvents); if !events.isEmpty { activity["events"] = .array(events) }
            return .object(activity)
        }
        if !activities.isEmpty { object["activities"] = .array(activities) }
        return .object(object)
    }
}
