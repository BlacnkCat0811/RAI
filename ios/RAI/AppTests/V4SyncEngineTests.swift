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
    var routes: [HealthRouteBundle] = []
    var routeError: Error?
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
        samples.filter { $0.quantityType == type }
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
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

    func testEmptyReadIsNotDeletionAndPreservesIdentity() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let sample = v4Sample(workout)
        let health = V4Health(workout: workout, samples: [sample])
        let engine = V4SyncEngine(store: store, health: health, scope: scope,
                                  outbox: V4Outbox(directory: directory))
        guard case .prepared(let first) = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil) else {
            return XCTFail("expected prepared bundle")
        }
        let json = try JSON.decode(first.raw)
        XCTAssertEqual(json["protocolVersion"].integer, 4)
        XCTAssertEqual(json["streams"].list.count, 10)
        let heart = json["streams"].list.first { $0["type"].text == "HKQuantityTypeIdentifierHeartRate" }
        XCTAssertEqual(heart?["samples"].list.count, 1)
        XCTAssertEqual(json["observation"]["coverage"]["HKQuantityTypeIdentifierHeartRate"]["state"].text, "complete")
        XCTAssertEqual(first.sampleDeletions, 0)
        try await V4Outbox(directory: directory).record(first.index, id: workout.uuid.uuidString.lowercased())

        await health.setSamples([])
        guard case .prepared(let second) = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: Data([1])) else {
            return XCTFail("expected prepared bundle")
        }
        let secondJSON = try JSON.decode(second.raw)
        XCTAssertEqual(second.sampleDeletions, 0)
        XCTAssertEqual(secondJSON["deletions"].list.count, 0)
        XCTAssertEqual(secondJSON["observation"]["coverage"]["HKQuantityTypeIdentifierHeartRate"]["state"].text, "partial")
        XCTAssertEqual(secondJSON["observation"]["coverage"]["HKQuantityTypeIdentifierHeartRate"]["errorCode"].text,
                       "empty_read_not_deletion")
        XCTAssertTrue(second.index.samples.contains("HKQuantityTypeIdentifierHeartRate:\(sample.uuid.uuidString.lowercased())"))
    }

    func testRouteFailureIsPartialAndNeverEmitsRouteDeletion() async throws {
        let workout = v4Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V4Health(workout: workout, samples: [])
        let route = HealthRouteBundle(id: UUID().uuidString.lowercased(), source: "com.apple.health",
                                      points: [RoutePoint(recordedAtUs: 1_800_000_000_000_000, latitude: 31.2, longitude: 121.4)])
        await health.setRoutes([route])
        let engine = V4SyncEngine(store: store, health: health, scope: scope,
                                  outbox: V4Outbox(directory: directory))
        guard case .prepared(let first) = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: nil) else {
            return XCTFail("expected prepared bundle")
        }
        try await V4Outbox(directory: directory).record(first.index, id: workout.uuid.uuidString.lowercased())

        await health.setRouteError(SyncFailure.invalid("route_query_failed"))
        guard case .prepared(let failed) = try await engine.build(id: workout.uuid.uuidString.lowercased(), anchor: Data([1])) else {
            return XCTFail("expected prepared bundle")
        }
        let json = try JSON.decode(failed.raw)
        XCTAssertEqual(json["observation"]["coverage"]["route"]["state"].text, "failed")
        XCTAssertEqual(failed.routeDeletions, 0)
        XCTAssertEqual(failed.index.routes.count, 1)
    }
}

private extension V4Health {
    func setSamples(_ samples: [HKQuantitySample]) { self.samples = samples }
    func setRoutes(_ routes: [HealthRouteBundle]) { self.routes = routes }
    func setRouteError(_ error: Error?) { self.routeError = error }
}
