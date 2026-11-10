import XCTest
import HealthKit
import SyncCore
@testable import RAI

private func v6Workout(_ start: Date, duration: TimeInterval = 60,
                       bundleID: String = "com.apple.health",
                       metadata: [String: Any]? = [HKMetadataKeyIndoorWorkout: true]) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(duration),
                            duration: duration, totalEnergyBurned: nil, totalDistance: nil,
                            metadata: metadata)
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
    private var scope: Scope!

    override func setUp() async throws {
        scope = Scope(serviceID: UUID().uuidString.lowercased(), datasetID: UUID().uuidString.lowercased(),
                      datasetEpoch: UUID().uuidString.lowercased(), collectorEpoch: UUID().uuidString.lowercased())
    }

    func testBuildEmitsProtocolSixWithoutPerSampleIdentity() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let configuration = HKWorkoutConfiguration()
        configuration.activityType = .running
        configuration.locationType = .indoor
        let activities = [
            HKWorkoutActivity(workoutConfiguration: configuration, start: workout.startDate,
                              end: workout.startDate.addingTimeInterval(30), metadata: nil),
            HKWorkoutActivity(workoutConfiguration: configuration, start: workout.startDate.addingTimeInterval(30),
                              end: workout.endDate, metadata: nil),
        ]
        workout.setValue(activities, forKey: "subActivities")
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

        let engine = RawStreamSyncEngine(health: health, scope: scope)
        let built = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built)
        XCTAssertEqual(json["protocolVersion"].integer, 6)
        XCTAssertEqual(json["workoutID"].text, workout.uuid.uuidString.lowercased())
        XCTAssertEqual(json["source"]["bundleID"].text, "com.apple.health")
        XCTAssertEqual(json["metadata"], .object([HKMetadataKeyIndoorWorkout: .bool(true)]))
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

        let encodedActivities = json["workout"]["activities"].list
        XCTAssertEqual(encodedActivities.count, 2)
        for (index, activity) in encodedActivities.enumerated() {
            XCTAssertNil(activity.fields["uuid"])
            XCTAssertEqual(activity["startUs"].integer, startUs + Int64(index) * 30_000_000)
            XCTAssertEqual(activity["endUs"].integer, startUs + Int64(index + 1) * 30_000_000)
            XCTAssertEqual(activity["durationUs"].integer, 30_000_000)
            XCTAssertEqual(activity["configuration"]["activityType"].integer, 37)
        }
        let text = String(decoding: built, as: UTF8.self)
        XCTAssertEqual(text.components(separatedBy: workout.uuid.uuidString.lowercased()).count - 1, 1)
        XCTAssertFalse(text.contains("\"uuid\""))
        XCTAssertFalse(text.contains(heart.uuid.uuidString.lowercased()))
        XCTAssertFalse(text.contains(route.id))
        for activity in activities { XCTAssertFalse(text.contains(activity.uuid.uuidString.lowercased())) }
    }

    func testEachTypeIsReadOnceAndSeriesTypesUseOneSeriesQuery() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let engine = RawStreamSyncEngine(health: health, scope: scope)
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

    func testAnySampleOrSeriesQueryFailureFailsTheWholeWorkout() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let sampleFailure = V6Health(workout: workout)
        await sampleFailure.setSampleFailure(SyncFailure.invalid("healthkit_query_failed"), for: .stepCount)
        do {
            _ = try await RawStreamSyncEngine(health: sampleFailure, scope: scope)
                .build(id: workout.uuid.uuidString.lowercased())
            XCTFail("a started sample query error must fail this Workout")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("healthkit_query_failed"))
        }

        let seriesFailure = V6Health(workout: workout)
        await seriesFailure.setSeriesFailure(SyncFailure.invalid("series_query_failed"), for: .runningPower)
        do {
            _ = try await RawStreamSyncEngine(health: seriesFailure, scope: scope)
                .build(id: workout.uuid.uuidString.lowercased())
            XCTFail("a started series query error must fail this Workout")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("series_query_failed"))
        }
    }

    func testRouteFailureFailsTheWholeWorkoutInsteadOfSendingAnEmptyRouteList() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        await health.setRouteFailure(SyncFailure.invalid("route_query_failed"))
        do {
            _ = try await RawStreamSyncEngine(health: health, scope: scope)
                .build(id: workout.uuid.uuidString.lowercased())
            XCTFail("a started route read error must fail this Workout")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("route_query_failed"))
        }
        let counts = await health.counters()
        XCTAssertEqual(counts.routes, 1)
    }

    func testIncompleteRouteReadFailsTheWorkout() async throws {
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
        do {
            _ = try await RawStreamSyncEngine(health: health, scope: scope)
                .build(id: workout.uuid.uuidString.lowercased())
            XCTFail("an incomplete route read must not be treated as no route")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("route_query_failed"))
        }
    }

    func testIndoorRunWithoutRoutesAndEmptyStreamsIsUploadable() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let built = try await RawStreamSyncEngine(health: health, scope: scope)
            .build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built)
        XCTAssertEqual(json["routes"].list.count, 0)
        XCTAssertEqual(json["streams"].list.count, HealthKitClient.quantityTypes.count)
        XCTAssertTrue(json["streams"].list.allSatisfy { $0["status"].text == "complete" })
    }

    func testUnsupportedSampleIsMarkedWithoutFailingTheStream() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let category = HKCategorySample(type: HKObjectType.categoryType(forIdentifier: .sleepAnalysis)!,
                                        value: 0, start: workout.startDate, end: workout.endDate)
        await health.setSamples([category], for: .heartRate)
        let built = try await RawStreamSyncEngine(health: health, scope: scope)
            .build(id: workout.uuid.uuidString.lowercased())
        let stream = try XCTUnwrap(try JSON.decode(built)["streams"].list
            .first { $0["type"].text == HKQuantityTypeIdentifier.heartRate.rawValue })
        XCTAssertEqual(stream["status"].text, "partial")
        XCTAssertEqual(stream["error"].text, "unsupported_sample")
        XCTAssertEqual(stream["samples"].list.count, 0)
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
        let engine = RawStreamSyncEngine(health: health, scope: scope)
        let first = try await engine.build(id: workout.uuid.uuidString.lowercased())
        let second = try await engine.build(id: workout.uuid.uuidString.lowercased())
        XCTAssertEqual(first, second)
        let samples = try JSON.decode(first)["streams"].list
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
        let built = try await RawStreamSyncEngine(health: health, scope: scope)
            .build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built)
        let streams = json["streams"].list
        let heartStream = try XCTUnwrap(streams.first { $0["type"].text == HKQuantityTypeIdentifier.heartRate.rawValue })
        XCTAssertEqual(heartStream["samples"].list.count, 2_400)
        XCTAssertEqual(json["routes"].list[0]["points"].list.count, 600)
        for type in [HKQuantityTypeIdentifier.runningPower, .runningGroundContactTime,
                     .runningStrideLength, .runningVerticalOscillation] {
            let stream = try XCTUnwrap(streams.first { $0["type"].text == type.rawValue })
            XCTAssertEqual(stream["series"].list[0]["points"].list.count, 600)
        }
        XCTAssertLessThan(built.count, WorkoutBundleV6.maxBytes)
        print("v6 40-minute builder payload bytes=\(built.count)")
    }

    func testRouteCountOverLimitThrowsWithoutTruncation() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000))
        let health = V6Health(workout: workout)
        let route = HealthRouteBundle(id: UUID().uuidString.lowercased(), source: "com.apple.health",
                                      points: [RoutePoint(recordedAtUs: 1, latitude: 31.2, longitude: 121.4)])
        await health.setRoutes(Array(repeating: route, count: WorkoutBundleV6.maxRoutes + 1))
        do {
            _ = try await RawStreamSyncEngine(health: health, scope: scope)
                .build(id: workout.uuid.uuidString.lowercased())
            XCTFail("over-limit routes must not be truncated")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("bundle_too_large"))
        }
    }

    func testThirdPartySourceIsExcludedBeforeAnyRead() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000), bundleID: "com.example.thirdparty")
        let health = V6Health(workout: workout)
        do {
            _ = try await RawStreamSyncEngine(health: health, scope: scope)
                .build(id: workout.uuid.uuidString.lowercased())
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

    // MARK: - Top-level metadata contract

    /// The fixture is the shared golden vector: the Go decoder test asserts the
    /// same object survives projection and reaches `environment.state=present`.
    private func metadataFixture() throws -> JSON {
        let root = URL(filePath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
        let path = root.appending(path: "contracts/fixtures/workout-bundle-v6-metadata.json")
        return try JSON.decode(Data(contentsOf: path))
    }

    func testTopLevelMetadataMatchesTheContractFixture() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000), duration: 2_400, metadata: [
            HKMetadataKeyIndoorWorkout: false,
            HKMetadataKeyWeatherHumidity: HKQuantity(unit: HKUnit.percent(), doubleValue: 0.5),
            HKMetadataKeyWeatherTemperature: HKQuantity(unit: HKUnit.degreeCelsius(), doubleValue: 16),
        ])
        let health = V6Health(workout: workout)
        let built = try await RawStreamSyncEngine(health: health, scope: scope)
            .build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built)

        XCTAssertEqual(json["metadata"], try metadataFixture()["metadata"],
                       "client bytes must carry the metadata the server analysis reads")
        XCTAssertEqual(json["metadata"]["HKWeatherTemperature"]["unit"].text, "degC")
        XCTAssertEqual(json["metadata"]["HKWeatherHumidity"]["unit"].text, "1")
    }

    func testUnknownWorkoutMetadataKeyIsCarriedWithoutBlockingTheUpload() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000),
                                metadata: [HKMetadataKeyIndoorWorkout: true, "HKUnreviewedSyntheticKey": "x"])
        let health = V6Health(workout: workout)
        let built = try await RawStreamSyncEngine(health: health, scope: scope)
            .build(id: workout.uuid.uuidString.lowercased())
        let metadata = try XCTUnwrap(JSON.decode(built)["metadata"].fields)
        XCTAssertEqual(metadata[HKMetadataKeyIndoorWorkout], .bool(true),
                       "registered keys keep their typed encoding")
        XCTAssertEqual(metadata["HKUnreviewedSyntheticKey"], .string("x"),
                       "an unregistered key is carried JSON-safely instead of blocking the upload")
        let counts = await health.counters()
        XCTAssertFalse(counts.samples.isEmpty, "an unregistered key must not stop the stream reads")
    }

    func testWorkoutWithoutMetadataOmitsTheTopLevelField() async throws {
        let workout = v6Workout(Date(timeIntervalSince1970: 1_800_000_000), metadata: nil)
        let health = V6Health(workout: workout)
        let built = try await RawStreamSyncEngine(health: health, scope: scope)
            .build(id: workout.uuid.uuidString.lowercased())
        let json = try JSON.decode(built)
        XCTAssertTrue(json.fields["metadata"] == nil, "an empty metadata object must stay off the wire")
    }

    func testMetadataFailureCodesStayStableForTheUI() {
        // The detail row prints the code verbatim and the diagnostic report
        // copies it, so a keyed code must never carry the key off the device.
        XCTAssertEqual(UserFacingText.stableCode("unsupported_workout_metadata_unit:HKSyntheticKey"),
                       "unsupported_workout_metadata_unit")
        XCTAssertEqual(UserFacingText.stableCode("metadata_requires_review"), "metadata_requires_review")

        let fallback = UserFacingText.error("code_without_a_message")
        for code in ["metadata_requires_review", "unsupported_workout_metadata_type",
                     "unsupported_workout_metadata_unit", "nonfinite_workout_metadata",
                     "workout_metadata_limit"] {
            XCTAssertNotEqual(UserFacingText.error(code), fallback, "\(code) needs a user-facing reason")
        }
        XCTAssertTrue(UserFacingText.error("metadata_requires_review").contains("未上传"))
    }
}
