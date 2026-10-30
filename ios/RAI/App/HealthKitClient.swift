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
    static let identifiers: [HKQuantityTypeIdentifier] = [.heartRate, .distanceWalkingRunning, .activeEnergyBurned, .runningSpeed, .runningPower, .stepCount, .basalEnergyBurned, .runningGroundContactTime, .runningStrideLength, .runningVerticalOscillation]
    static let quantityTypes = identifiers.compactMap { HKObjectType.quantityType(forIdentifier: $0) }
    static let types: [HKSampleType] = [HKObjectType.workoutType()] + quantityTypes

    func authorize() async throws {
        guard HKHealthStore.isHealthDataAvailable() else { throw SyncFailure.invalid("healthkit_unavailable") }
        try await health.requestAuthorization(toShare: [], read: Set(Self.types.map { $0 as HKObjectType } + [HKSeriesType.workoutRoute()]))
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

#if DEBUG
enum WorkoutInspection {
    static func fingerprint(_ value: String) -> String { SHA256.hash(data: Data(value.utf8)).map { String(format: "%02x", $0) }.joined() }
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
            return .object(["key": .string(key), "valueType": .string(kind), "normalizerAllowlisted": .bool([HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout, HKMetadataKeyAverageMETs, HKMetadataKeyElevationAscended, HKMetadataKeyWeatherHumidity, HKMetadataKeyWeatherTemperature].contains(key))])
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
        "HKQuantityTypeIdentifierRunningStrideLength": "m", "HKQuantityTypeIdentifierRunningVerticalOscillation": "m"
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
    // Keep the existing sample mapping immutable. The inspected Workout's
    // additional source quantities use the existing JSON metadata field.
    static func workoutMetadata(_ metadata: [String: Any]?) throws -> JSON? {
        let values = metadata ?? [:]
        let units: [String: (HKUnit, String)] = [
            HKMetadataKeyAverageMETs: (.kilocalorie().unitDivided(by: .gramUnit(with: .kilo).unitMultiplied(by: .hour())), "kcal/(kg*hr)"),
            HKMetadataKeyElevationAscended: (.meter(), "m"),
            HKMetadataKeyWeatherHumidity: (.percent(), "1"),
            HKMetadataKeyWeatherTemperature: (.degreeCelsius(), "degC"),
        ]
        let allowed = Set(units.keys).union([HKMetadataKeyTimeZone, HKMetadataKeyIndoorWorkout])
        let unsupported = values.keys.filter { !allowed.contains($0) }.sorted()
        guard unsupported.isEmpty else { throw SyncFailure.invalid("unsupported_workout_metadata_keys:" + unsupported.joined(separator: ",")) }
        var output = self.metadata(values)?.fields ?? [:]
        for (key, (unit, label)) in units where values[key] != nil {
            guard let quantity = values[key] as? HKQuantity, quantity.is(compatibleWith: unit) else { throw SyncFailure.invalid("unsupported_workout_metadata_unit:" + key) }
            let value = quantity.doubleValue(for: unit)
            guard value.isFinite else { throw SyncFailure.invalid("nonfinite_workout_metadata") }
            output[key] = .object(["value": .number(value), "unit": .string(label)])
        }
        return output.isEmpty ? nil : .object(output)
    }
    static func event(_ sample: HKSample) throws -> JSON {
        var object: [String: JSON] = ["op": .string("upsert"), "kind": .string(sample is HKWorkout ? "workout" : "sample"),
            "type": .string(sample.sampleType.identifier), "uuid": .string(sample.uuid.uuidString.lowercased()),
            "startUs": .int(us(sample.startDate)), "endUs": .int(us(sample.endDate)),
            "source": .object(["bundleID": .string(sample.sourceRevision.source.bundleIdentifier)])]
        if let m = try (sample is HKWorkout ? workoutMetadata(sample.metadata) : metadata(sample.metadata)) { object["metadata"] = m }
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
