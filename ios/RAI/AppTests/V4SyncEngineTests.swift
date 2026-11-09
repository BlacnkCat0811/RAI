import XCTest
import HealthKit
import SyncCore
@testable import RAI

private func v4Workout(_ start: Date, bundleID: String = "com.apple.health") -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(60), duration: 60,
                            totalEnergyBurned: nil, totalDistance: nil, metadata: [HKMetadataKeyIndoorWorkout: true])
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue(bundleID, forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    let revision = HKSourceRevision(source: source, version: "synthetic", productType: "Watch7,1",
                                    operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
    workout.setValue(revision, forKey: "sourceRevision")
    return workout
}

private func numeric(_ value: JSON) -> Double? {
    switch value {
    case .int(let integer): return Double(integer)
    case .number(let number): return number
    default: return nil
    }
}

private func v4Sample(_ workout: HKWorkout) -> HKQuantitySample {
    let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: .heartRate)!,
                                  quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 120),
                                  start: workout.startDate, end: workout.endDate)
    sample.setValue(workout.sourceRevision, forKey: "sourceRevision")
    return sample
}

private actor V4Health: HealthKitReading {
    var workout: HKWorkout
    var samples: [HKQuantitySample]
    var seriesValues: [String: [JSON]] = [:]
    var routes: [HealthRouteBundle] = []
    var routeError: Error?
    var sampleError: Error?
    init(workout: HKWorkout, samples: [HKQuantitySample]) {
        self.workout = workout
        self.samples = samples
    }
    func authorize() async throws {}
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage { HealthPage(samples: [], deleted: [], anchor: Data([0]), limit: 10) }
    func workout(_ id: String) async throws -> HKWorkout? { workout.uuid.uuidString.lowercased() == id ? workout : nil }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { [] }
    func recentWorkouts(limit: Int) async throws -> [HealthWorkoutSummary] { [] }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        HealthPage(samples: anchor == nil ? [workout] : [], deleted: [], anchor: Data([1]), limit: 10)
    }
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        if let sampleError { throw sampleError }
        return samples.filter { $0.quantityType == type }
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] {
        var output: [String: [JSON]] = [:]
        for (key, points) in seriesValues where key.hasPrefix(type.identifier + ":") {
            output[String(key.dropFirst(type.identifier.count + 1))] = points
        }
        return output
    }
    func bundleRoutes(workout: HKWorkout) async throws -> [HealthRouteBundle] {
        if let routeError { throw routeError }
        return routes
    }
}

final class V4SyncEngineTests: XCTestCase {
    private var directory: URL!
    private var store: OutboxStore!
    private var scope: Scope!

    override func setUp() async throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        store = try OutboxStore(directory: directory)
        scope = Scope(serviceID: UUID().uuidString.lowercased(), datasetID: UUID().uuidString.lowercased(),
                      datasetEpoch: UUID().uuidString.lowercased(), collectorEpoch: UUID().uuidString.lowercased())
        try await store.bind(scope)
    }

    override func tearDown() async throws {
        try? FileManager.default.removeItem(at: directory)
    }

    func testRawBundleCarriesWorkoutSamplesAndInlineMetadata() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let sample = v4Sample(workout)
        let health = V4Health(workout: workout, samples: [sample])
        let engine = V4SyncEngine(store: store, health: health, scope: scope)
        let bundle = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil)
        let json = try JSON.decode(bundle.raw)
        XCTAssertEqual(json["protocolVersion"].integer, 4)
        XCTAssertEqual(json["workoutID"].text, workout.uuid.uuidString.lowercased())
        XCTAssertEqual(json["workout"]["activityType"].integer, Int64(HKWorkoutActivityType.running.rawValue))
        XCTAssertEqual(json["workout"]["durationUs"].integer, 60_000_000)
        XCTAssertEqual(json["metadata"][HKMetadataKeyIndoorWorkout], .bool(true))
        XCTAssertEqual(json["streams"].list.count, HealthKitClient.quantityTypes.count)
        let heart = json["streams"].list.first { $0["type"].text == HKObjectType.quantityType(forIdentifier: .heartRate)!.identifier }
        XCTAssertEqual(heart?["status"].text, "complete")
        XCTAssertEqual(heart?["samples"].list.count, 1)
        let encoded = heart?["samples"].list.first
        XCTAssertEqual(encoded?["uuid"].text, sample.uuid.uuidString.lowercased())
        XCTAssertEqual(numeric(encoded?["value"] ?? .null), 120)
        XCTAssertEqual(encoded?["source"]["bundleID"].text, "com.apple.health")
        XCTAssertEqual(json["series"].list.count, 0)
        XCTAssertEqual(json["routes"].list.count, 0)
        // The raw wire has no observation, source table, coverage or deletion lanes.
        for key in ["observation", "sources", "deletions", "routeDeletions", "metadataRegistryVersion"] {
            XCTAssertTrue(json.fields[key] == nil, "unexpected raw key \(key)")
        }
        XCTAssertEqual(bundle.nextAnchor, Data([1]))
    }

    func testSeriesAreKeyedByParentSampleUUIDAndTriples() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let sample = v4Sample(workout)
        let health = V4Health(workout: workout, samples: [sample])
        await health.setSeries(["HKQuantityTypeIdentifierHeartRate:\(sample.uuid.uuidString.lowercased())": [
            .object(["startUs": .int(1_800_000_000_000_000), "endUs": .int(1_800_000_000_500_000),
                     "value": .number(121), "unit": .string("count/min")]),
        ]])
        let engine = V4SyncEngine(store: store, health: health, scope: scope)
        let bundle = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil)
        let json = try JSON.decode(bundle.raw)
        let series = json["series"].list.first
        XCTAssertEqual(series?["type"].text, "HKQuantityTypeIdentifierHeartRate")
        XCTAssertEqual(series?["sampleUUID"].text, sample.uuid.uuidString.lowercased())
        XCTAssertEqual(series?["unit"].text, "count/min")
        let points = series?["points"].list.first?.list ?? []
        XCTAssertEqual(points.count, 3)
        XCTAssertEqual(numeric(points[2]), 121)
    }

    func testRouteReadFailureStillUploadsTheRun() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V4Health(workout: workout, samples: [])
        let route = HealthRouteBundle(id: UUID().uuidString.lowercased(), source: "com.apple.health",
                                      points: [RoutePoint(recordedAtUs: 1_800_000_000_000_000, latitude: 31.2, longitude: 121.4)])
        await health.setRoutes([route])
        await health.setRouteError(SyncFailure.invalid("route_query_failed"))
        let engine = V4SyncEngine(store: store, health: health, scope: scope)
        let bundle = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil)
        let json = try JSON.decode(bundle.raw)
        XCTAssertEqual(json["routes"].list.count, 0)
        XCTAssertTrue(json["streams"].list.allSatisfy { $0["status"].text == "complete" })
    }

    func testRoutePointsAreEightElementTuplesWithSource() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V4Health(workout: workout, samples: [])
        let route = HealthRouteBundle(id: UUID().uuidString.lowercased(), source: "com.apple.health",
                                      points: [RoutePoint(recordedAtUs: 1_800_000_000_000_000, latitude: 31.2, longitude: 121.4,
                                                          altitude: 10, speed: 3)],
                                      provenance: .object(["bundleID": .string("com.apple.health")]))
        await health.setRoutes([route])
        let engine = V4SyncEngine(store: store, health: health, scope: scope)
        let bundle = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil)
        let json = try JSON.decode(bundle.raw)
        let encoded = json["routes"].list.first
        XCTAssertEqual(encoded?["routeID"].text, route.id)
        XCTAssertEqual(encoded?["source"]["bundleID"].text, "com.apple.health")
        let point = encoded?["points"].list.first?.list
        XCTAssertEqual(point?.count, 8)
        XCTAssertEqual(numeric(point?[3] ?? .null), 10)
        XCTAssertEqual(numeric(point?[6] ?? .null), 3)
        XCTAssertEqual(point?[4], .null)
    }

    func testFailedQuantityReadIsReportedWithoutDroppingTheRun() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V4Health(workout: workout, samples: [])
        await health.setSampleError(SyncFailure.invalid("healthkit_query_failed"))
        let engine = V4SyncEngine(store: store, health: health, scope: scope)
        let bundle = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil)
        let json = try JSON.decode(bundle.raw)
        XCTAssertEqual(json["streams"].list.count, HealthKitClient.quantityTypes.count)
        XCTAssertTrue(json["streams"].list.allSatisfy {
            $0["status"].text == "failed" && $0["error"].text == "healthkit_query_failed"
        })
    }

    func testThirdPartySourceIsExcludedBeforeAnyRead() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000), bundleID: "com.example.thirdparty")
        let health = V4Health(workout: workout, samples: [])
        let engine = V4SyncEngine(store: store, health: health, scope: scope)
        do {
            _ = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil)
            XCTFail("third-party workout must not be uploaded")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("source_policy_excluded"))
        }
    }
}

private extension V4Health {
    func setSeries(_ values: [String: [JSON]]) { self.seriesValues = values }
    func setRoutes(_ routes: [HealthRouteBundle]) { self.routes = routes }
    func setRouteError(_ error: Error?) { self.routeError = error }
    func setSampleError(_ error: Error?) { self.sampleError = error }
}
