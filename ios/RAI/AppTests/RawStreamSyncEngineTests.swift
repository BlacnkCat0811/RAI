import XCTest
import HealthKit
import SyncCore
@testable import RAI

private func v6Workout(_ start: Date, duration: TimeInterval = 60,
                       bundleID: String = "com.apple.health") -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(duration),
                            duration: duration, totalEnergyBurned: nil, totalDistance: nil,
                            metadata: [HKMetadataKeyIndoorWorkout: true])
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue(bundleID, forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    let revision = HKSourceRevision(source: source, version: "synthetic", productType: "Watch7,1",
                                    operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
    workout.setValue(revision, forKey: "sourceRevision")
    return workout
}

private func v6Quantity(_ workout: HKWorkout, type: HKQuantityTypeIdentifier, unit: HKUnit,
                        value: Double, offset: TimeInterval, length: TimeInterval = 1) -> HKQuantitySample {
    let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: type)!,
                                  quantity: HKQuantity(unit: unit, doubleValue: value),
                                  start: workout.startDate.addingTimeInterval(offset),
                                  end: workout.startDate.addingTimeInterval(offset + length))
    sample.setValue(workout.sourceRevision, forKey: "sourceRevision")
    return sample
}

private func numeric(_ value: JSON) -> Double? {
    switch value {
    case .int(let integer): return Double(integer)
    case .number(let number): return number
    default: return nil
    }
}

private actor V6Health: HealthKitReading {
    var workoutValue: HKWorkout
    var samplesByType: [String: [HKSample]] = [:]
    var seriesByType: [String: [HealthSeriesGroup]] = [:]
    var routes: [HealthRouteBundle] = []
    var routeFailure: Error?
    var sampleFailures: [String: Error] = [:]
    var seriesFailures: [String: Error] = [:]
    private(set) var sampleReads: [String: Int] = [:]
    private(set) var seriesReads: [String: Int] = [:]
    private(set) var routeReads = 0
    private(set) var workoutReads = 0

    init(workout: HKWorkout) { workoutValue = workout }

    func authorize() async throws {}
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        HealthPage(samples: [], deleted: [], anchor: Data([0]), limit: 10)
    }
    func workout(_ id: String) async throws -> HKWorkout? {
        workoutReads += 1
        return workoutValue.uuid.uuidString.lowercased() == id ? workoutValue : nil
    }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute {
        throw SyncFailure.invalid("unused")
    }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { [] }
    func recentWorkouts(limit: Int) async throws -> [HealthWorkoutSummary] { [] }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        HealthPage(samples: [], deleted: [], anchor: Data([1]), limit: 10)
    }
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        sampleReads[type.identifier, default: 0] += 1
        if let failure = sampleFailures[type.identifier] { throw failure }
        return samplesByType[type.identifier] ?? []
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
    func bundleSeriesGroups(type: HKQuantityType, workout: HKWorkout) async throws -> [HealthSeriesGroup] {
        seriesReads[type.identifier, default: 0] += 1
        if let failure = seriesFailures[type.identifier] { throw failure }
        return seriesByType[type.identifier] ?? []
    }
    func bundleRoutes(workout: HKWorkout) async throws -> [HealthRouteBundle] {
        routeReads += 1
        if let routeFailure { throw routeFailure }
        return routes
    }

    func setRoutes(_ value: [HealthRouteBundle]) { routes = value }
    func setRouteFailure(_ value: Error?) { routeFailure = value }
    func setSamples(_ value: [HKSample], for type: HKQuantityTypeIdentifier) {
        samplesByType[type.rawValue] = value
    }
    func setSeries(_ value: [HealthSeriesGroup], for type: HKQuantityTypeIdentifier) {
        seriesByType[type.rawValue] = value
    }
    func setSampleFailure(_ value: Error?, for type: HKQuantityTypeIdentifier) {
        sampleFailures[type.rawValue] = value
    }
    func setSeriesFailure(_ value: Error?, for type: HKQuantityTypeIdentifier) {
        seriesFailures[type.rawValue] = value
    }
    func counters() -> (samples: [String: Int], series: [String: Int], routes: Int, workouts: Int) {
        (sampleReads, seriesReads, routeReads, workoutReads)
    }
}

final class RawStreamSyncEngineTests: XCTestCase {
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

    func testBuildEmitsProtocolSixWithoutPerSampleIdentity() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let heart = v6Quantity(workout, type: .heartRate, unit: HKUnit.count().unitDivided(by: .minute()),
                               value: 120, offset: 0)
        await health.setSamples([heart], for: .heartRate)
        let startUs = Normalizer.us(workout.startDate)
        await health.setSeries([HealthSeriesGroup(
            startUs: startUs + 2_000_000, endUs: startUs + 3_000_000, value: 250,
            points: [HealthSeriesPoint(startUs: startUs + 2_100_000, endUs: startUs + 2_500_000, value: 260)])],
            for: .runningPower)
        let route = HealthRouteBundle(id: UUID().uuidString.lowercased(), source: "com.apple.health",
                                      points: [RoutePoint(recordedAtUs: startUs + 1_000_000, latitude: 31.2,
                                                          longitude: 121.4, altitude: 10, speed: 3)])
        await health.setRoutes([route])

        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let built = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built.raw)
        XCTAssertEqual(json["protocolVersion"].integer, 6)
        XCTAssertEqual(json["workoutID"].text, workout.uuid.uuidString.lowercased())
        XCTAssertEqual(json["source"]["bundleID"].text, "com.apple.health")
        XCTAssertTrue(json.fields["metadata"] == nil)
        XCTAssertTrue(json.fields["sourceTable"] == nil)
        XCTAssertTrue(json.fields["series"] == nil)

        let streams = json["streams"].list
        XCTAssertEqual(streams.count, HealthKitClient.quantityTypes.count)
        let heartStream = try XCTUnwrap(streams.first { $0["type"].text == HKQuantityTypeIdentifier.heartRate.rawValue })
        XCTAssertEqual(heartStream["status"].text, "complete")
        XCTAssertEqual(heartStream["samples"].list.count, 1)
        let sample = heartStream["samples"].list[0]
        XCTAssertEqual(sample.list.count, 3)
        XCTAssertEqual(sample.list[0].integer, 0)
        XCTAssertEqual(sample.list[1].integer, 1_000_000)
        XCTAssertEqual(numeric(sample.list[2]), 120)
        for key in ["uuid", "metadata", "source"] {
            XCTAssertTrue(sample.fields[key] == nil, "v6 samples must not carry \(key)")
        }

        let powerStream = try XCTUnwrap(streams.first { $0["type"].text == HKQuantityTypeIdentifier.runningPower.rawValue })
        let group = try XCTUnwrap(powerStream["series"].list.first)
        XCTAssertEqual(group["startDeltaUs"].integer, 2_000_000)
        XCTAssertEqual(numeric(group["value"]), 250)
        XCTAssertEqual(group["points"].list[0].list[0].integer, 2_100_000)

        let encodedRoute = try XCTUnwrap(json["routes"].list.first)
        XCTAssertEqual(encodedRoute.fields.keys.sorted(), ["points"])
        XCTAssertTrue(encodedRoute.fields["routeID"] == nil)
        XCTAssertTrue(encodedRoute.fields["source"] == nil)
    }

    func testEachTypeIsReadOnceAndSeriesTypesUseOneSeriesQuery() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        _ = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let counts = await health.counters()
        XCTAssertEqual(counts.workouts, 1)
        XCTAssertEqual(counts.routes, 1)
        for type in HealthKitClient.quantityTypes {
            if RawStreamSyncEngine.seriesTypes.contains(type.identifier) {
                XCTAssertEqual(counts.series[type.identifier], 1, type.identifier)
                XCTAssertNil(counts.samples[type.identifier], type.identifier)
            } else {
                XCTAssertEqual(counts.samples[type.identifier], 1, type.identifier)
                XCTAssertNil(counts.series[type.identifier], type.identifier)
            }
        }
    }

    func testSingleSeriesFailureDoesNotBlockOtherStreams() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        await health.setSampleFailure(SyncFailure.invalid("healthkit_query_failed"), for: .stepCount)
        await health.setSeriesFailure(SyncFailure.invalid("series_query_failed"), for: .runningPower)
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let built = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let streams = try JSON.decode(built.raw)["streams"].list
        let failedSample = try XCTUnwrap(streams.first { $0["type"].text == HKQuantityTypeIdentifier.stepCount.rawValue })
        XCTAssertEqual(failedSample["status"].text, "failed")
        XCTAssertEqual(failedSample["error"].text, "healthkit_query_failed")
        let failedSeries = try XCTUnwrap(streams.first { $0["type"].text == HKQuantityTypeIdentifier.runningPower.rawValue })
        XCTAssertEqual(failedSeries["status"].text, "failed")
        XCTAssertEqual(failedSeries["error"].text, "series_query_failed")
        let healthy = try XCTUnwrap(streams.first { $0["type"].text == HKQuantityTypeIdentifier.heartRate.rawValue })
        XCTAssertEqual(healthy["status"].text, "complete")
    }

    func testRouteFailureStillUploadsTheRun() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        await health.setRouteFailure(SyncFailure.invalid("route_query_failed"))
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let built = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built.raw)
        XCTAssertEqual(json["routes"].list.count, 0)
        XCTAssertTrue(json["streams"].list.allSatisfy { $0["status"].text == "complete" })
    }

    func testIncompleteRouteReadUploadsNoRoutes() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let startUs = Normalizer.us(workout.startDate)
        var coverage = WorkoutCoverage.result(count: 1, completed: false, expected: 2)
        coverage.errorCode = "route_query_failed"
        let route = HealthRouteBundle(
            id: UUID().uuidString.lowercased(), source: "com.apple.health",
            points: [RoutePoint(recordedAtUs: startUs + 1_000_000, latitude: 31.2, longitude: 121.4)],
            coverage: coverage)
        await health.setRoutes([route])
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let built = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built.raw)
        XCTAssertEqual(json["routes"].list.count, 0)
        XCTAssertTrue(json["streams"].list.allSatisfy { $0["status"].text == "complete" })
    }

    func testUnsupportedSampleIsMarkedWithoutFailingTheStream() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let category = HKCategorySample(type: HKObjectType.categoryType(forIdentifier: .sleepAnalysis)!,
                                        value: 0, start: workout.startDate, end: workout.endDate)
        await health.setSamples([category], for: .heartRate)
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let built = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let stream = try XCTUnwrap(try JSON.decode(built.raw)["streams"].list
            .first { $0["type"].text == HKQuantityTypeIdentifier.heartRate.rawValue })
        XCTAssertEqual(stream["status"].text, "partial")
        XCTAssertEqual(stream["error"].text, "unsupported_sample")
        XCTAssertEqual(stream["samples"].list.count, 0)
    }

    func testRetryReusesTheStagedBytesWithoutRebuilding() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        await health.setSamples([v6Quantity(workout, type: .heartRate,
                                           unit: HKUnit.count().unitDivided(by: .minute()),
                                           value: 120, offset: 0)], for: .heartRate)
        let id = workout.uuid.uuidString.lowercased()
        try await store.beginTask(id)
        try await store.markIngestLane(id)
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let built = try await engine.build(id: id)
        let boundary = "rai.test." + UUID().uuidString
        let staged = try await store.stageWorkoutIngestPayload(
            id, body: WorkoutBundleAssembler.multipart(bundle: built.raw, boundary: boundary),
            payloadVersion: 6)
        let stagedVersion = await store.task(id)?.payloadVersion
        XCTAssertEqual(stagedVersion, 6)

        // A relaunch replays the persisted file, not a fresh HealthKit read.
        await health.setSamples([], for: .heartRate)
        let reopened = try OutboxStore(directory: directory)
        let reopenedTask = await reopened.task(id)
        let task = try XCTUnwrap(reopenedTask)
        XCTAssertEqual(task.bundleFile, staged.file)
        XCTAssertEqual(task.bundleBoundary, boundary)
        XCTAssertEqual(task.payloadVersion, 6)
        let url = try await reopened.fileURL(staged.file)
        XCTAssertEqual(try Data(contentsOf: url), WorkoutBundleAssembler.multipart(bundle: built.raw, boundary: boundary))
    }

    func testBuildBytesAreDeterministic() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        await health.setSamples([
            v6Quantity(workout, type: .heartRate, unit: HKUnit.count().unitDivided(by: .minute()),
                       value: 122, offset: 2),
            v6Quantity(workout, type: .heartRate, unit: HKUnit.count().unitDivided(by: .minute()),
                       value: 120, offset: 0),
        ], for: .heartRate)
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let first = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let second = try await engine.build(id: workout.uuid.uuidString.lowercased())
        XCTAssertEqual(first.raw, second.raw)
        let samples = try JSON.decode(first.raw)["streams"].list
            .first { $0["type"].text == HKQuantityTypeIdentifier.heartRate.rawValue }?["samples"].list
        XCTAssertEqual(samples?.map { $0.list[0].integer }, [0, 2_000_000])
    }

    func testFortyMinuteSyntheticRunCountsAndSize() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000), duration: 40 * 60)
        let health = V6Health(workout: workout)
        var heart: [HKSample] = []
        heart.reserveCapacity(2_400)
        for index in 0..<2_400 {
            heart.append(v6Quantity(workout, type: .heartRate,
                                    unit: HKUnit.count().unitDivided(by: .minute()),
                                    value: 120 + Double(index % 10), offset: TimeInterval(index)))
        }
        await health.setSamples(heart, for: .heartRate)
        let startUs = Normalizer.us(workout.startDate)
        let endUs = startUs + 40 * 60 * 1_000_000
        for type in [HKQuantityTypeIdentifier.runningPower, .runningGroundContactTime,
                     .runningStrideLength, .runningVerticalOscillation] {
            var points: [HealthSeriesPoint] = []
            points.reserveCapacity(600)
            for index in 0..<600 {
                let offset = Int64(index) * 40_000
                points.append(HealthSeriesPoint(startUs: startUs + offset,
                                                endUs: startUs + offset + 30_000, value: 250))
            }
            await health.setSeries([HealthSeriesGroup(startUs: startUs, endUs: endUs, value: 250,
                                                      points: points)], for: type)
        }
        var routePoints: [RoutePoint] = []
        routePoints.reserveCapacity(600)
        for index in 0..<600 {
            let offset = Int64(index) * 4_000_000
            routePoints.append(RoutePoint(recordedAtUs: startUs + offset, latitude: 31.2,
                                          longitude: 121.4, altitude: 10, speed: 3))
        }
        await health.setRoutes([HealthRouteBundle(id: UUID().uuidString.lowercased(),
                                                  source: "com.apple.health", points: routePoints)])
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        let built = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built.raw)
        let streams = json["streams"].list
        let heartStream = try XCTUnwrap(streams.first { $0["type"].text == HKQuantityTypeIdentifier.heartRate.rawValue })
        XCTAssertEqual(heartStream["samples"].list.count, 2_400)
        XCTAssertEqual(json["routes"].list[0]["points"].list.count, 600)
        for type in [HKQuantityTypeIdentifier.runningPower, .runningGroundContactTime,
                     .runningStrideLength, .runningVerticalOscillation] {
            let stream = try XCTUnwrap(streams.first { $0["type"].text == type.rawValue })
            XCTAssertEqual(stream["series"].list[0]["points"].list.count, 600)
        }
        XCTAssertLessThan(built.raw.count, WorkoutBundleV6.maxBytes)
        print("v6 40-minute builder payload bytes=\(built.raw.count)")
    }

    func testRouteCountOverLimitThrowsWithoutTruncation() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let route = HealthRouteBundle(id: UUID().uuidString.lowercased(), source: "com.apple.health",
                                      points: [RoutePoint(recordedAtUs: 1, latitude: 31.2, longitude: 121.4)])
        await health.setRoutes(Array(repeating: route, count: WorkoutBundleV6.maxRoutes + 1))
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        do {
            _ = try await engine.build(id: workout.uuid.uuidString.lowercased())
            XCTFail("over-limit routes must not be truncated")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("bundle_too_large"))
        }
    }

    func testThirdPartySourceIsExcludedBeforeAnyRead() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000), bundleID: "com.example.thirdparty")
        let health = V6Health(workout: workout)
        let engine = RawStreamSyncEngine(store: store, health: health, scope: scope)
        do {
            _ = try await engine.build(id: workout.uuid.uuidString.lowercased())
            XCTFail("third-party workout must not be uploaded")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("source_policy_excluded"))
        }
        let counts = await health.counters()
        XCTAssertEqual(counts.workouts, 1)
        XCTAssertEqual(counts.routes, 0)
        XCTAssertTrue(counts.samples.isEmpty)
        XCTAssertTrue(counts.series.isEmpty)
    }

    func testHandshakeFallsBackToV4WhenServerAdvertisesFour() {
        let fallback = SyncCoordinator.negotiate(wantsV4: true, wantsV6: true, bundleProtocol: 4)
        XCTAssertTrue(fallback.rawLane)
        XCTAssertEqual(fallback.payloadVersion, 4)
        let negotiated = SyncCoordinator.negotiate(wantsV4: true, wantsV6: true, bundleProtocol: 6)
        XCTAssertEqual(negotiated.payloadVersion, 6)
        XCTAssertEqual(SyncCoordinator.negotiate(wantsV4: true, wantsV6: false, bundleProtocol: 6).payloadVersion, 4)
        let legacy = SyncCoordinator.negotiate(wantsV4: false, wantsV6: true, bundleProtocol: 6)
        XCTAssertFalse(legacy.rawLane)
        XCTAssertEqual(legacy.payloadVersion, 4)
        XCTAssertFalse(SyncCoordinator.negotiate(wantsV4: true, wantsV6: true, bundleProtocol: 3).rawLane)
    }
}
