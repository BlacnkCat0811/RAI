import XCTest
import ObjectiveC
import Security
import HealthKit
import SyncCore
import Synchronization
import ProductCore
@testable import RAI

final class NormalizerTests: XCTestCase {
    func testM7SharedProductGoldenContract() throws {
        guard let path = ProcessInfo.processInfo.environment["RAI_M7_GOLDEN_DIR"] else {
            throw XCTSkip("M7 shared golden requires explicit synthetic test runner")
        }
        let root = URL(fileURLWithPath: path)
        let files = try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil).filter { $0.pathExtension == "json" }
        XCTAssertEqual(files.count, 11)
        for file in files {
            let snapshot = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
            XCTAssertEqual(snapshot.schemaVersion, "rai.athlete_model/1")
            XCTAssertEqual(snapshot, try JSONDecoder().decode(AthleteModelSnapshot.self, from: JSONEncoder().encode(snapshot)))
            if file.lastPathComponent == "new_user.json" {
                XCTAssertNil(snapshot.runningAbility.value)
                XCTAssertNil(snapshot.trainingReadiness.value)
            }
            if file.lastPathComponent == "future_unknown.json" {
                XCTAssertEqual(snapshot.runningAbility.envelope.presentationState.rawValue, "limited")
                XCTAssertNil(snapshot.runningAbility.presentationValue)
            }
        }
    }
    func testHeartRateUsesCountPerMinute() throws {
        let q = HKQuantity(unit: .count().unitDivided(by: .minute()), doubleValue: 120)
        let value = try Normalizer.quantity(q, type: HKQuantityTypeIdentifier.heartRate.rawValue)
        XCTAssertEqual(value["unit"], .string("count/min")); XCTAssertEqual(value["value"], .number(120))
    }
    func testDistanceConvertsKilometersToMeters() throws {
        let q = HKQuantity(unit: .meterUnit(with: .kilo), doubleValue: 5)
        let value = try Normalizer.quantity(q, type: HKQuantityTypeIdentifier.distanceWalkingRunning.rawValue)
        XCTAssertEqual(value["unit"], .string("m")); XCTAssertEqual(value["value"], .number(5000))
    }
    func testEnergyConvertsJoulesToKilocalories() throws {
        let q = HKQuantity(unit: .joule(), doubleValue: 4184)
        let value = try Normalizer.quantity(q, type: HKQuantityTypeIdentifier.activeEnergyBurned.rawValue)
        if case .number(let actual) = value["value"] { XCTAssertEqual(actual, 1, accuracy: 0.00001) }
        else { XCTFail("Missing value") }
        XCTAssertEqual(value["unit"], .string("kcal"))
    }
    func testIncompatibleUnitRefused() {
        XCTAssertThrowsError(try Normalizer.quantity(HKQuantity(unit: .meter(), doubleValue: 5), type: HKQuantityTypeIdentifier.heartRate.rawValue))
    }
    func testSpeedConvertsKilometersPerHour() throws {
        let q = HKQuantity(unit: .meterUnit(with: .kilo).unitDivided(by: .hour()), doubleValue: 10.8)
        let value = try Normalizer.quantity(q, type: HKQuantityTypeIdentifier.runningSpeed.rawValue)
        if case .number(let actual) = value["value"] { XCTAssertEqual(actual, 3, accuracy: 0.000001) }
        else { XCTFail("Missing value") }; XCTAssertEqual(value["unit"], .string("m/s"))
    }
    func testPowerRemainsWatts() throws {
        let value = try Normalizer.quantity(HKQuantity(unit: .watt(), doubleValue: 200), type: HKQuantityTypeIdentifier.runningPower.rawValue)
        XCTAssertEqual(value["value"], .number(200)); XCTAssertEqual(value["unit"], .string("W"))
    }
    func testGroundContactTimeConvertsSecondsToMilliseconds() throws {
        let value = try Normalizer.quantity(HKQuantity(unit: .second(), doubleValue: 0.25), type: HKQuantityTypeIdentifier.runningGroundContactTime.rawValue)
        XCTAssertEqual(value["unit"], .string("ms"))
        XCTAssertEqual(value["value"], .number(250))
        XCTAssertThrowsError(try Normalizer.quantity(HKQuantity(unit: .meter(), doubleValue: 1), type: HKQuantityTypeIdentifier.runningGroundContactTime.rawValue))
    }
    func testRunningDynamicsLengthsUseMeters() throws {
        for type in [HKQuantityTypeIdentifier.runningStrideLength, .runningVerticalOscillation] {
            let value = try Normalizer.quantity(HKQuantity(unit: .meterUnit(with: .centi), doubleValue: 8), type: type.rawValue)
            XCTAssertEqual(value["unit"], .string("m"))
            if case .number(let actual) = value["value"] { XCTAssertEqual(actual, 0.08, accuracy: 0.000000001) }
            else { XCTFail("Missing running dynamics value") }
            XCTAssertThrowsError(try Normalizer.quantity(HKQuantity(unit: .second(), doubleValue: 1), type: type.rawValue))
        }
    }
    func testWorkoutInspectionMetadataOmitsValues() throws {
        let data = try WorkoutInspection.metadata(["synthetic_private_note": "DO_NOT_EXPORT_SYNTHETIC_VALUE", HKMetadataKeyWeatherTemperature: HKQuantity(unit: .degreeCelsius(), doubleValue: 23)]).data()
        let text = String(decoding: data, as: UTF8.self)
        XCTAssertTrue(text.contains("synthetic_private_note"))
        XCTAssertTrue(text.contains("quantity"))
        XCTAssertFalse(text.contains("DO_NOT_EXPORT_SYNTHETIC_VALUE"))
        XCTAssertFalse(text.contains("23"))
    }
    func testInspectedWorkoutMetadataQuantitiesArePreserved() throws {
        let mets = HKUnit.kilocalorie().unitDivided(by: .gramUnit(with: .kilo).unitMultiplied(by: .hour()))
        let value = try XCTUnwrap(Normalizer.workoutMetadata([
            HKMetadataKeyTimeZone: "Asia/Shanghai", HKMetadataKeyIndoorWorkout: false,
            HKMetadataKeyAverageMETs: HKQuantity(unit: mets, doubleValue: 8),
            HKMetadataKeyElevationAscended: HKQuantity(unit: .meterUnit(with: .kilo), doubleValue: 0.1),
            HKMetadataKeyElevationDescended: HKQuantity(unit: .meter(), doubleValue: 55),
            HKMetadataKeyWeatherHumidity: HKQuantity(unit: .percent(), doubleValue: 0.6),
            HKMetadataKeyWeatherTemperature: HKQuantity(unit: .degreeFahrenheit(), doubleValue: 68),
            HKMetadataKeyAverageSpeed: HKQuantity(unit: .meter().unitDivided(by: .second()), doubleValue: 3.2),
            HKMetadataKeyMaximumSpeed: HKQuantity(unit: .meterUnit(with: .kilo).unitDivided(by: .hour()), doubleValue: 14.4),
        ]))
        XCTAssertEqual(value.fields.count, 9)
        XCTAssertEqual(value[HKMetadataKeyAverageMETs]["unit"], .string("kcal/(kg*hr)"))
        XCTAssertEqual(value[HKMetadataKeyElevationAscended]["value"], .number(100))
        XCTAssertEqual(value[HKMetadataKeyElevationDescended]["value"], .number(55))
        XCTAssertEqual(value[HKMetadataKeyAverageSpeed]["unit"], .string("m/s"))
        if case .number(let maximumSpeed) = value[HKMetadataKeyMaximumSpeed]["value"] { XCTAssertEqual(maximumSpeed, 4, accuracy: 0.00000001) }
        else { XCTFail("Source maximum speed missing") }
        XCTAssertEqual(value[HKMetadataKeyWeatherHumidity]["value"], .number(0.6))
        if case .number(let temperature) = value[HKMetadataKeyWeatherTemperature]["value"] { XCTAssertEqual(temperature, 20, accuracy: 0.00000001) }
        else { XCTFail("Source temperature missing") }
        XCTAssertThrowsError(try Normalizer.workoutMetadata(["unsupported_synthetic_key": 1]))
        XCTAssertThrowsError(try Normalizer.workoutMetadata(["unsupported_object_key": ["nested": 1]]))
        XCTAssertThrowsError(try Normalizer.workoutMetadata([HKMetadataKeyWeatherTemperature: HKQuantity(unit: .meter(), doubleValue: 1)]))
        XCTAssertEqual(HealthKitClient.quantityTypes.count, 10)
    }
    func testMetadataIsExplicitAllowlist() {
        let value = Normalizer.metadata([HKMetadataKeyIndoorWorkout: true, HKMetadataKeyHeartRateMotionContext: NSNumber(value: 2), "private_unknown": "synthetic"])
        XCTAssertEqual(value?.fields.count, 2)
        XCTAssertEqual(value?[HKMetadataKeyHeartRateMotionContext], .int(2))
        XCTAssertEqual(value?["private_unknown"], .null)
    }
    func testHeartRateMetadataIsPreservedWithoutPrivateFields() throws {
        let date = Date(timeIntervalSince1970: 100)
        let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: .heartRate)!,
                                      quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 120),
                                      start: date, end: date, metadata: [HKMetadataKeyHeartRateMotionContext: NSNumber(value: 2)])
        let event = try Normalizer.integrityEvent(sample)
        XCTAssertEqual(event["metadata"][HKMetadataKeyHeartRateMotionContext], .int(2))
        XCTAssertThrowsError(try Normalizer.integrityEvent(HKQuantitySample(
            type: HKObjectType.quantityType(forIdentifier: .heartRate)!,
            quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 120),
            start: date, end: date, metadata: ["synthetic_private_metadata": "never-upload"])))
    }
    func testCanonicalTimeRetainsMicroseconds() { XCTAssertEqual(Normalizer.us(Date(timeIntervalSince1970: 1.123456)), 1_123_456) }

    func testWorkoutEventIdentityDoesNotCarryClientProvenance() throws {
        let start = Date(timeIntervalSince1970: 1_793_059_200)
        let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(60),
                                duration: 60, totalEnergyBurned: nil, totalDistance: nil,
                                metadata: [HKMetadataKeyIndoorWorkout: true])
        let event = try Normalizer.event(workout)
        let metadata = event["metadata"].fields
        XCTAssertEqual(metadata[HKMetadataKeyIndoorWorkout], .bool(true))
        XCTAssertFalse(metadata.keys.contains { $0.hasPrefix("RAI:") })
        XCTAssertTrue(event["workout"] != .null)
    }

    func testWorkoutProvenanceIsASeparateSupplement() {
        let device = HKDevice(name: "Apple Watch", manufacturer: "Apple Inc.", model: "Watch7,1",
                              hardwareVersion: "Watch7,1", firmwareVersion: nil, softwareVersion: "27.2",
                              localIdentifier: nil, udiDeviceIdentifier: nil)
        let fields = Normalizer.workoutProvenance(version: "27.2", productType: "Watch7,1", device: device)
        XCTAssertEqual(fields["RAI:sourceRevisionVersion"], JSON.string("27.2"))
        XCTAssertEqual(fields["RAI:sourceProductType"], JSON.string("Watch7,1"))
        XCTAssertNil(fields["RAI:sourceDeviceName"])
        XCTAssertEqual(fields["RAI:sourceDeviceSoftwareVersion"], JSON.string("27.2"))
        XCTAssertTrue(Normalizer.workoutProvenance(version: nil, productType: nil, device: nil).isEmpty)
    }

    func testConfigurationTrimsCopiedLineEndings() throws {
        let secret = try ClientConfiguration.validatedSecret(baseURL: " https://synthetic.example/\n", collectorEpoch: " 11111111-1111-4111-8111-111111111111\n", token: " synthetic-token\n")
        XCTAssertEqual(secret.configuration.baseURL.absoluteString, "https://synthetic.example/")
        XCTAssertEqual(secret.configuration.collectorEpoch, "11111111-1111-4111-8111-111111111111")
        XCTAssertEqual(secret.token, "synthetic-token")
    }
    func testConfigurationErrorsDoNotExposeInputs() {
        let epoch = "11111111-1111-4111-8111-111111111111"
        for (origin, collector, token, expected) in [
            ("https://synthetic.example/private", epoch, "synthetic-token", "invalid_api_origin"),
            ("https://synthetic.example", "invalid-synthetic-epoch", "synthetic-token", "invalid_collector_epoch"),
            ("https://synthetic.example", epoch, " \n", "missing_device_token"),
            ("https://synthetic.example", epoch, "synthetic token", "device_token_contains_whitespace"),
            ("http://unconfigured.synthetic.example", epoch, "synthetic-token", "http_requires_configured_debug_host"),
        ] {
            XCTAssertThrowsError(try ClientConfiguration.validatedSecret(baseURL: origin, collectorEpoch: collector, token: token)) { error in
                guard case SyncFailure.invalid(let code) = error else { return XCTFail("Expected configuration validation error") }
                XCTAssertEqual(code, expected)
            }
        }
    }
}


private final class CoordinatorCounters: Sendable { let reads = Mutex(0); let uploads = Mutex(0) }
private struct NoHealthReads: HealthKitReading {
    let counters: CoordinatorCounters
    func authorize() async throws { counters.reads.withLock { $0 += 1 }; throw SyncFailure.invalid("unexpected_health_read") }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage { counters.reads.withLock { $0 += 1 }; throw SyncFailure.invalid("unexpected_health_read") }
    func workout(_ id: String) async throws -> HKWorkout? { counters.reads.withLock { $0 += 1 }; throw SyncFailure.invalid("unexpected_health_read") }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { counters.reads.withLock { $0 += 1 }; throw SyncFailure.invalid("unexpected_health_read") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { counters.reads.withLock { $0 += 1 }; throw SyncFailure.invalid("unexpected_health_read") }
}
private final class CoordinatorReplies: URLProtocol, @unchecked Sendable {
    static let identity = Scope(serviceID: "11111111-1111-4111-8111-111111111111", datasetID: "22222222-2222-4222-8222-222222222222", datasetEpoch: "33333333-3333-4333-8333-333333333333", collectorEpoch: "44444444-4444-4444-8444-444444444444")
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let handshake = request.url!.path == "/v2/handshake"
        let body: JSON = handshake ? .object(["protocolVersion": .int(3), "serviceID": .string(Self.identity.serviceID),
            "datasetID": .string(Self.identity.datasetID), "datasetEpoch": .string(Self.identity.datasetEpoch),
            "capabilities": .object(["workoutBundles": .bool(true), "bundleProtocolVersion": .int(3),
                "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) })])]) : .object(["code": .string("workout_import_not_found")])
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: handshake ? 200 : 404, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! body.data()); client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
private struct BrokenACKUploader: FileUploader {
    let counters: CoordinatorCounters
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) { counters.uploads.withLock { $0 += 1 }; return (Data(), 200, nil) }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        counters.uploads.withLock { $0 += 1 }; return UploadResponse(data: Data(), status: 200, contentType: "application/json")
    }
}
final class WorkoutCoordinatorReliabilityTests: XCTestCase {
    @MainActor
    func testModelAllowsUserSyncDuringAutomaticWorkAndReservesSelectionImmediately() async {
        let scheduler = ForegroundSyncScheduler(), model = SyncModel(syncScheduler: scheduler)
        let (started, continuation) = AsyncStream<Void>.makeStream()
        var stopped = false
        scheduler.startAutomatic {
            continuation.yield(())
            do { try await Task.sleep(for: .seconds(3600)) } catch { stopped = true }
        }
        for await _ in started { break }
        XCTAssertTrue(model.busy)
        XCTAssertTrue(model.canStartWorkoutSync)
        model.syncWorkouts([Wire.id()])
        XCTAssertEqual(scheduler.mode, .manual)
        XCTAssertTrue(model.busy)
        XCTAssertFalse(model.canStartWorkoutSync)
        await scheduler.waitForCompletion()
        XCTAssertTrue(stopped)
        XCTAssertFalse(model.busy)
        XCTAssertTrue(model.canStartWorkoutSync)
    }

    @MainActor
    func testUserRecoveryPreemptsHealthDiscoveryAndPreservesResumableFlow() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-discovery-preemption-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), counters = CoordinatorCounters()
        let (started, continuation) = AsyncStream<Void>.makeStream()
        let health = PausedDiscoveryHealth(counters: counters, started: continuation)
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [CoordinatorReplies.self]
        let coordinator = SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))
        let secret = ClientConfiguration.Secret(configuration: ClientConfiguration(baseURL: URL(string: "https://synthetic.invalid")!, collectorEpoch: CoordinatorReplies.identity.collectorEpoch), token: "synthetic")
        let scheduler = ForegroundSyncScheduler()
        var discoveryCancelled = false, recovered = false
        scheduler.startAutomatic {
            do { try await coordinator.discoverChanges(); XCTFail("paused discovery completed") }
            catch is CancellationError { discoveryCancelled = true }
            catch { XCTFail("unexpected discovery failure") }
        }
        for await _ in started { break }
        XCTAssertTrue(scheduler.startManual {
            do {
                try await coordinator.recoverPending(secret: secret, transfers: BrokenACKUploader(counters: counters))
                recovered = true
            } catch { XCTFail("manual recovery could not acquire coordinator") }
        })
        await scheduler.waitForCompletion()
        XCTAssertTrue(discoveryCancelled && recovered)
        XCTAssertEqual(counters.reads.withLock { $0 }, 1)
        let query = DiscoveryQuery(type: HKObjectType.workoutType().identifier)
        let flow = try await store.discoveryFlow(query)
        XCTAssertNil(flow.anchor); XCTAssertNil(flow.error); XCTAssertTrue(flow.hasMore)
        XCTAssertEqual(scheduler.mode, .idle)
    }

    func testCancelledRecoveryStopsBeforeNextBundleWithoutHealthReads() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-recovery-cancellation-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), counters = CoordinatorCounters()
        var originals: [String: Data] = [:]
        for _ in 0..<2 {
            let id = Wire.id()
            var fields = CoordinatorReplies.identity.bundleFields; fields["workoutID"] = .string(id)
            let raw = try JSON.object(fields).data()
            try await store.beginTask(id)
            let bytes = WorkoutBundleAssembler.multipart(bundle: raw, boundary: "test")
            let file = try await store.savePayload(bytes, suffix: "multipart.json")
            try await store.setBundle(id, file: file, sha256: Wire.hash(raw), boundary: "test")
            originals[file] = bytes
        }
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [CoordinatorReplies.self]
        let coordinator = SyncCoordinator(store: store, health: NoHealthReads(counters: counters), session: URLSession(configuration: config))
        let secret = ClientConfiguration.Secret(configuration: ClientConfiguration(baseURL: URL(string: "https://synthetic.invalid")!, collectorEpoch: CoordinatorReplies.identity.collectorEpoch), token: "synthetic")
        do {
            try await coordinator.recoverPending(secret: secret, transfers: CancelledRecoveryUploader(counters: counters))
            XCTFail("recovery swallowed cancellation")
        } catch is CancellationError { }
        XCTAssertEqual(counters.uploads.withLock { $0 }, 1)
        XCTAssertEqual(counters.reads.withLock { $0 }, 0)
        for (file, original) in originals { XCTAssertEqual(try Data(contentsOf: directory.appendingPathComponent(file)), original) }
        let tasks = await store.allTasks()
        XCTAssertTrue(tasks.allSatisfy { $0.uploadReceipt == nil })
        XCTAssertEqual(tasks.reduce(0) { $0 + ($1.attemptCount ?? 0) }, 1)
    }

    func testRecovery404KeepsExactBundleAndRetryTimeWithoutHealthReads() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-coordinator-test-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), id = Wire.id()
        var bundleFields = CoordinatorReplies.identity.bundleFields; bundleFields["workoutID"] = .string(id)
        let raw = try JSON.object(bundleFields).data()
        try await store.beginTask(id)
        let file = try await store.savePayload(WorkoutBundleAssembler.multipart(bundle: raw, boundary: "test"), suffix: "multipart.json")
        try await store.setBundle(id, file: file, sha256: Wire.hash(raw), boundary: "test")
        let counters = CoordinatorCounters()
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [CoordinatorReplies.self]
        let coordinator = SyncCoordinator(store: store, health: NoHealthReads(counters: counters), session: URLSession(configuration: config))
        let secret = ClientConfiguration.Secret(configuration: ClientConfiguration(baseURL: URL(string: "https://synthetic.invalid")!, collectorEpoch: CoordinatorReplies.identity.collectorEpoch), token: "synthetic")
        let transfers = BrokenACKUploader(counters: counters)
        try await coordinator.recoverPending(secret: secret, transfers: transfers)
        let pending = await store.task(id)
        XCTAssertEqual(pending?.phase, .uploading); XCTAssertNil(pending?.uploadReceipt)
        XCTAssertEqual(pending?.bundleFile, file); XCTAssertNotNil(pending?.nextAttemptAt)
        let failedSnapshot = await store.snapshot()
        XCTAssertNil(failedSnapshot.lastACK); XCTAssertNil(failedSnapshot.lastSync)
        XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent(file).path))
        try await coordinator.recoverPending(secret: secret, transfers: transfers)
        XCTAssertEqual(counters.uploads.withLock { $0 }, 1); XCTAssertEqual(counters.reads.withLock { $0 }, 0)
        do { try await coordinator.checkAnalysis(id: id, secret: secret, transfers: transfers); XCTFail("404 accepted as exact bundle proof") } catch { }
        let after = await store.task(id)
        XCTAssertEqual(after?.phase, .uploading); XCTAssertNil(after?.uploadReceipt)
        XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent(file).path))
    }
}

private struct PausedDiscoveryHealth: HealthKitReading {
    let counters: CoordinatorCounters
    let started: AsyncStream<Void>.Continuation
    func authorize() async throws { }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        counters.reads.withLock { $0 += 1 }; started.yield(())
        try await Task.sleep(for: .seconds(3600))
        throw SyncFailure.invalid("unexpected_completed_read")
    }
    func workout(_ id: String) async throws -> HKWorkout? { throw SyncFailure.invalid("unexpected_health_read") }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unexpected_health_read") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unexpected_health_read") }
}

private struct CancelledRecoveryUploader: FileUploader {
    let counters: CoordinatorCounters
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        counters.uploads.withLock { $0 += 1 }
        throw CancellationError()
    }
}

private actor SyntheticAnchoredHealth: HealthKitReading {
    let value: HKWorkout
    let quantity: HKQuantitySample
    private(set) var calls: [String: Int] = [:]
    init() {
        let start = Date(timeIntervalSince1970: 1_793_059_200)
        value = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(60), duration: 60,
                          totalEnergyBurned: nil, totalDistance: nil, metadata: [HKMetadataKeyIndoorWorkout: true])
        quantity = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: .heartRate)!, quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 1), start: start, end: start)
    }
    func authorize() async throws { }
    func workout(_ id: String) async throws -> HKWorkout? { value }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        calls["workout", default: 0] += 1
        return HealthPage(samples: anchor == nil ? [value] : [], deleted: [], anchor: Data([1]), limit: 25)
    }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        calls[type.identifier, default: 0] += 1
        let samples: [HKSample]
        if type == HKObjectType.workoutType() { samples = anchor == nil ? [value] : [] }
        else { samples = type.identifier == quantity.sampleType.identifier && anchor == nil ? [quantity] : [] }
        return HealthPage(samples: samples, deleted: [], anchor: Data([2]), limit: 1000)
    }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
}
final class Phase3HealthKitIntegrityTests: XCTestCase {
    func testRealHKSecureArchiveAndScopedIncrementalDiscovery() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-hk-discovery-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), health = SyntheticAnchoredHealth()
        let workout = await health.value, id = workout.uuid.uuidString.lowercased()
        let coordinator = SyncCoordinator(store: store, health: health)
        try await coordinator.discoverChanges(workoutIDs: [id])
        let first = await store.discoverySnapshot()
        XCTAssertEqual(first.flows.count, 12)
        XCTAssertEqual(first.events.count, 2)
        let observedSample = await health.quantity
        XCTAssertEqual(first.parents[HKQuantityTypeIdentifier.heartRate.rawValue + ":" + observedSample.uuid.uuidString.lowercased()], id)
        let archive = try XCTUnwrap(first.workoutArchives[id])
        let decoded = try NSKeyedUnarchiver.unarchivedObject(ofClass: HKWorkout.self, from: Data(contentsOf: dir.appendingPathComponent(archive)))
        XCTAssertEqual(decoded?.uuid, workout.uuid)
        let reopened = try OutboxStore(directory: dir)
        let resumed = SyncCoordinator(store: reopened, health: health)
        try await resumed.discoverChanges(workoutIDs: [id])
        try await resumed.discoverChanges(workoutIDs: [id])
        let last = await reopened.discoverySnapshot()
        XCTAssertEqual(last.events.count, first.events.count)
        XCTAssertEqual(last.work[id]?.generation, first.work[id]?.generation)
        XCTAssertTrue(last.flows.values.allSatisfy { $0.anchor != nil && $0.staged == nil })
    }
    func testInvalidSecureAnchorNeverQueriesWithNil() async throws {
        let client = HealthKitClient()
        do {
            _ = try await client.read(type: HKObjectType.workoutType(), anchor: Data([1,2,3]), workout: nil)
            XCTFail("Invalid secure anchor accepted")
        } catch { /* fails before any HealthKit result or replacement anchor */ }
    }
    func testDeviceIdentifiersAndUnreviewedMetadataStayLocal() throws {
        let date = Date(timeIntervalSince1970: 100)
        let device = HKDevice(name: "synthetic-private-device-name", manufacturer: "Apple Inc.", model: "Watch", hardwareVersion: "Watch7,1", firmwareVersion: "1", softwareVersion: "1", localIdentifier: "synthetic-private-local", udiDeviceIdentifier: "synthetic-private-udi")
        let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: .heartRate)!, quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 1), start: date, end: date, device: device, metadata: nil)
        let text = String(decoding: try Normalizer.source(sample).data(), as: UTF8.self)
        XCTAssertFalse(text.contains("synthetic-private"))
        XCTAssertTrue(text.contains("firmwareVersion"))
        let workout = HKWorkout(activityType: .running, start: date, end: date, duration: 0, totalEnergyBurned: nil, totalDistance: nil, metadata: ["synthetic_private_metadata": "never-upload"])
        XCTAssertThrowsError(try Normalizer.integrityEvent(workout))
        let partial = try Normalizer.integrityEvent(HKWorkout(activityType: .running, start: date, end: date, duration: 0, totalEnergyBurned: nil, totalDistance: nil, metadata: nil))
        XCTAssertEqual(partial["metadata"], JSON.null)
    }
}

extension NormalizerTests {
    func testDiscoveryFingerprintIsStableAcrossSecureArchiveRoundTrip() throws {
        let type = HKQuantityType(.heartRate)
        let sample = HKQuantitySample(type: type, quantity: HKQuantity(unit: .count().unitDivided(by: .minute()), doubleValue: 120), start: Date(timeIntervalSince1970: 100), end: Date(timeIntervalSince1970: 101), metadata: [HKMetadataKeyHeartRateMotionContext: 1])
        let bytes = try NSKeyedArchiver.archivedData(withRootObject: sample, requiringSecureCoding: true)
        let decoded = try XCTUnwrap(NSKeyedUnarchiver.unarchivedObject(ofClass: HKQuantitySample.self, from: bytes))
        XCTAssertEqual(try Normalizer.discoveryFingerprint(sample), try Normalizer.discoveryFingerprint(decoded))
    }
}

final class Phase5ASessionPlatformTests: XCTestCase {
    func testSessionKeychainStoresIndependentImmutableAccounts() throws {
        let a = Wire.id(), b = Wire.id(), keychain = SessionKeychain()
        defer {
            for id in [a, b] {
                SecItemDelete([kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "RAI.V3.SyncSession", kSecAttrAccount as String: id] as CFDictionary)
            }
        }
        try keychain.save(token: "synthetic-old", sessionID: a)
        try keychain.save(token: "synthetic-new", sessionID: b)
        XCTAssertEqual(try keychain.load(sessionID: a), "synthetic-old")
        XCTAssertEqual(try keychain.load(sessionID: b), "synthetic-new")
        XCTAssertThrowsError(try keychain.save(token: "replacement", sessionID: a))
        XCTAssertEqual(try keychain.load(sessionID: a), "synthetic-old")
    }
    func testLateOldBackgroundCallbackCannotWriteNewSessionDirectory() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("rai-platform-session-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let a = Wire.id(), b = Wire.id()
        let oldDir = try SyncSessionStore.directory(root: root, sessionID: a), newDir = try SyncSessionStore.directory(root: root, sessionID: b)
        let oldStore = try OutboxStore(directory: oldDir), newStore = try OutboxStore(directory: newDir)
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        try await newStore.bind(scope)
        let before = try Data(contentsOf: newDir.appendingPathComponent("state.json"))
        let old = BackgroundTransfers(directory: oldStore.directory, identifier: "com.rai.v2.sync." + a, sessionID: a)
        let new = BackgroundTransfers(directory: newStore.directory, identifier: "com.rai.v2.sync." + b, sessionID: b)
        let callback = URLSession.shared.dataTask(with: URL(string: "https://synthetic.invalid")!)
        let identity = a + ":workout:synthetic-hash"
        callback.taskDescription = identity
        old.urlSession(.shared, task: callback, didCompleteWithError: nil)
        // Even a misrouted delegate invocation is rejected by the session prefix.
        new.urlSession(.shared, task: callback, didCompleteWithError: nil)
        let result = Wire.hash(Data(identity.utf8)) + ".transfer.json"
        XCTAssertTrue(FileManager.default.fileExists(atPath: oldDir.appendingPathComponent(result).path))
        XCTAssertFalse(FileManager.default.fileExists(atPath: newDir.appendingPathComponent(result).path))
        XCTAssertEqual(try Data(contentsOf: newDir.appendingPathComponent("state.json")), before)
        let file = try await oldStore.savePayload(Data("synthetic-old".utf8))
        do {
            _ = try await new.response(URLRequest(url: URL(string: "https://synthetic.invalid")!), file: oldDir.appendingPathComponent(file), identity: "synthetic")
            XCTFail("old file accepted by new transfer")
        } catch let failure as SyncFailure {
            guard case .localState("upload_file_session_mismatch") = failure else { XCTFail("wrong rejection"); return }
        }
    }
}

private final class PlatformSessionCredentials: SessionCredentialStore, Sendable {
    private let tokens = Mutex<[String: String]>([:])
    func save(token: String, sessionID: String) throws { tokens.withLock { $0[sessionID] = token } }
    func load(sessionID: String) throws -> String? { tokens.withLock { $0[sessionID] } }
}
extension Phase5ASessionPlatformTests {
    func testNewSessionNilAnchorReimportsVisibleHKWorkoutAfterOldDeletion() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("rai-platform-discovery-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let credentials = PlatformSessionCredentials(), sessions = try SyncSessionStore(root: root, credentials: credentials)
        func descriptor() -> SyncSessionDescriptor { .init(datasetID: Wire.id(), serviceID: Wire.id(), datasetEpoch: Wire.id(), deviceID: Wire.id(), collectorEpoch: Wire.id(), baseURL: URL(string: "https://synthetic.invalid")!) }
        func hello(_ d: SyncSessionDescriptor) -> JSON { .object(["protocolVersion": .int(3), "datasetID": .string(d.datasetID), "datasetEpoch": .string(d.datasetEpoch), "serviceID": .string(d.serviceID), "deviceID": .string(d.deviceID), "collectorEpoch": .string(d.collectorEpoch), "datasetStatus": .string("active"), "capabilities": .object(["workoutBundles": .bool(true), "bundleProtocolVersion": .int(3)])]) }
        let a = descriptor(), b = descriptor(), health = SyntheticAnchoredHealth()
        _ = try await sessions.prepare(a, token: "old"); try await sessions.activate(a.sessionID, handshake: hello(a))
        let old = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: a.sessionID))
        let oldCoordinator = SyncCoordinator(store: old, health: health, sessionStore: sessions, sessionID: a.sessionID)
        try await oldCoordinator.discoverChanges()
        let workout = await health.value, id = workout.uuid.uuidString.lowercased(), query = DiscoveryQuery(type: HKObjectType.workoutType().identifier)
        let previous = try await old.discoveryFlow(query).anchor
        let page = try await old.stageDiscovery(query, previousAnchor: previous, anchor: Data([9]), events: [DiscoveryEvent(type: query.type, uuid: id, deleted: true)], hasMore: false)
        try await old.promoteDiscovery(query, batchID: page)
        let oldSnapshot = await old.discoverySnapshot()
        XCTAssertEqual(oldSnapshot.work[id]?.pendingDelete, true)
        _ = try await sessions.prepare(b, token: "new")
        let fresh = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: b.sessionID))
        let coordinator = SyncCoordinator(store: fresh, health: health, sessionStore: sessions, sessionID: b.sessionID)
        do { try await coordinator.discoverChanges(); XCTFail("prepared session discovered") } catch { }
        let nilAnchor = try await fresh.discoveryFlow(query).anchor; XCTAssertNil(nilAnchor)
        try await sessions.activate(b.sessionID, handshake: hello(b))
        try await coordinator.discoverChanges()
        let first = await fresh.discoverySnapshot()
        XCTAssertNotNil(first.workoutArchives[id]); XCTAssertEqual(first.work[id]?.pendingDelete, false)
        XCTAssertEqual(first.events.count, 2)
        try await coordinator.discoverChanges()
        let repeated = await fresh.discoverySnapshot()
        XCTAssertEqual(first.events.count, repeated.events.count)
        XCTAssertEqual(first.work[id]?.generation, repeated.work[id]?.generation)
        let oldAnchor = try await old.discoveryFlow(query).anchor; XCTAssertEqual(oldAnchor, Data([9]))
        do { try await oldCoordinator.discoverChanges(); XCTFail("retired session discovered") } catch { }
    }
    func testBackgroundEventsFinishingBeforeRegistrationStillComplete() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-background-finish-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        _ = try OutboxStore(directory: directory)
        let id = Wire.id(), transfer = BackgroundTransfers(directory: directory, identifier: "com.rai.v2.sync." + Wire.id(), sessionID: id)
        let completed = expectation(description: "late handler receives persisted finish")
        transfer.urlSessionDidFinishEvents(forBackgroundURLSession: .shared)
        transfer.handleEvents { completed.fulfill() }
        await fulfillment(of: [completed], timeout: 2)
    }
}

extension Phase5ASessionPlatformTests {
    func testRecoveryUsesSeparateRealKeychainAccountsAndBlocksArchivedCoordinator() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("rai-recovery-platform-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let keys = SessionKeychain(), sessions = try SyncSessionStore(root: root, credentials: keys)
        func descriptor() -> SyncSessionDescriptor { .init(datasetID: Wire.id(), serviceID: Wire.id(), datasetEpoch: Wire.id(), deviceID: Wire.id(), collectorEpoch: Wire.id(), baseURL: URL(string: "https://synthetic.invalid")!) }
        func hello(_ d: SyncSessionDescriptor) -> JSON { .object(["protocolVersion": .int(3), "datasetID": .string(d.datasetID), "datasetEpoch": .string(d.datasetEpoch), "serviceID": .string(d.serviceID), "deviceID": .string(d.deviceID), "collectorEpoch": .string(d.collectorEpoch), "datasetStatus": .string("active"), "capabilities": .object(["workoutBundles": .bool(true), "bundleProtocolVersion": .int(3)])]) }
        let a = descriptor(), b = descriptor()
        defer {
            for d in [a, b] { SecItemDelete([kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: "RAI.V3.SyncSession", kSecAttrAccount as String: d.sessionID] as CFDictionary) }
        }
        _ = try await sessions.prepare(a, token: "synthetic-A"); try await sessions.activate(a.sessionID, handshake: hello(a))
        let old = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: a.sessionID))
        let coordinator = SyncCoordinator(store: old, health: SyntheticAnchoredHealth(), sessionStore: sessions, sessionID: a.sessionID)
        _ = try await sessions.prepare(b, token: "synthetic-B", recovery: true)
        do { try await coordinator.discoverChanges(); XCTFail("recovery_required coordinator read HealthKit") } catch { }
        XCTAssertEqual(try keys.load(sessionID: a.sessionID), "synthetic-A")
        XCTAssertEqual(try keys.load(sessionID: b.sessionID), "synthetic-B")
        let restarted = try SyncSessionStore(root: root, credentials: keys)
        try await restarted.activate(b.sessionID, handshake: hello(b))
        let fresh = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: b.sessionID))
        let flow = try await fresh.discoveryFlow(DiscoveryQuery(type: HKObjectType.workoutType().identifier))
        XCTAssertNil(flow.anchor)
        let manifest = try await restarted.snapshot()
        XCTAssertEqual(manifest.sessions[a.sessionID]?.status, .retired)
        XCTAssertEqual(manifest.activeSessionID, b.sessionID)
    }
}

private final class RecoveryHandshakeFailures: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let status = Int(request.value(forHTTPHeaderField: "X-Synthetic-Status") ?? "500")!
        if status == 0 { client?.urlProtocol(self, didFailWithError: URLError(.timedOut)); return }
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data("{\"code\":\"synthetic_failure\"}".utf8)); client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
extension Phase5ASessionPlatformTests {
    func testHandshakeTimeout404429And500DoNotDeclareDisaster() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let sessions = try SyncSessionStore(root: root, credentials: PlatformSessionCredentials())
        let d = SyncSessionDescriptor(datasetID: Wire.id(), serviceID: Wire.id(), datasetEpoch: Wire.id(), deviceID: Wire.id(), collectorEpoch: Wire.id(), baseURL: URL(string: "https://synthetic.invalid")!)
        let hello: JSON = .object(["protocolVersion": .int(3), "datasetID": .string(d.datasetID), "datasetEpoch": .string(d.datasetEpoch), "serviceID": .string(d.serviceID), "deviceID": .string(d.deviceID), "collectorEpoch": .string(d.collectorEpoch), "datasetStatus": .string("active"), "capabilities": .object(["workoutBundles": .bool(true), "bundleProtocolVersion": .int(3)])])
        _ = try await sessions.prepare(d, token: "synthetic"); try await sessions.activate(d.sessionID, handshake: hello)
        let store = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: d.sessionID)), counters = CoordinatorCounters()
        let secret = ClientConfiguration.Secret(configuration: .init(baseURL: d.baseURL, collectorEpoch: d.collectorEpoch), token: "synthetic")
        for status in [0, 404, 429, 500] {
            let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [RecoveryHandshakeFailures.self]; config.httpAdditionalHeaders = ["X-Synthetic-Status": String(status)]
            let session = URLSession(configuration: config); defer { session.invalidateAndCancel() }
            let coordinator = SyncCoordinator(store: store, health: NoHealthReads(counters: counters), session: session, sessionStore: sessions, sessionID: d.sessionID)
            do { try await coordinator.recoverPending(secret: secret, transfers: BrokenACKUploader(counters: counters)); XCTFail("failure accepted") } catch { }
            let active = try await sessions.requireActive(d.sessionID)
            XCTAssertEqual(active.status, .active)
        }
        XCTAssertEqual(counters.reads.withLock { $0 }, 0); XCTAssertEqual(counters.uploads.withLock { $0 }, 0)
    }
}

extension NormalizerTests {
    func testDailySDKCategoriesUnitsMetadataAndAnchorArchive() throws {
        XCTAssertEqual(try (0...5).map { try DailyHealthNormalizer.categoryValue($0) }, Array(0...5))
        XCTAssertThrowsError(try DailyHealthNormalizer.categoryValue(99))
        let date = Date(timeIntervalSince1970: 1_790_000_000)
        let hrv = HKQuantitySample(type: HKQuantityType(.heartRateVariabilitySDNN), quantity: HKQuantity(unit: .second(), doubleValue: 0.042), start: date, end: date,
            metadata: [HKMetadataKeyAlgorithmVersion: 2, HKMetadataKeyHeartRateMotionContext: 1, HKMetadataKeyTimeZone: "Asia/Shanghai", HKMetadataKeyWasUserEntered: true])
        let fact = try DailyHealthNormalizer.fact(hrv)
        XCTAssertEqual(fact["quantity"]["unit"], .string("ms")); XCTAssertEqual(fact["quantity"]["value"], .number(42))
        XCTAssertEqual(fact["metadata"]["HKAlgorithmVersion"], .int(2)); XCTAssertEqual(fact["metadata"]["HKWasUserEntered"], .bool(true))
        XCTAssertNotNil(fact["source"]["bundleID"].text)
        let rhr = HKQuantitySample(type: HKQuantityType(.restingHeartRate), quantity: HKQuantity(unit: .count().unitDivided(by: .minute()), doubleValue: 58), start: date, end: date)
        XCTAssertEqual(try DailyHealthNormalizer.fact(rhr)["quantity"]["unit"], .string("count/min"))
        for stage in 0...5 {
            let sleep = HKCategorySample(type: HKCategoryType(.sleepAnalysis), value: stage, start: date, end: date.addingTimeInterval(3600))
            let sleepFact = try DailyHealthNormalizer.fact(sleep)
            XCTAssertEqual(sleepFact["category"], .int(Int64(stage))); XCTAssertEqual(sleepFact["endUs"], .int(Normalizer.us(date.addingTimeInterval(3600))))
        }
        let largeVersion = Int64(9_007_199_254_740_993)
        let precise = HKQuantitySample(type: HKQuantityType(.restingHeartRate), quantity: HKQuantity(unit: .count().unitDivided(by: .minute()), doubleValue: 58), start: date, end: date,
            metadata: [HKMetadataKeySyncIdentifier: "synthetic-revision", HKMetadataKeySyncVersion: NSNumber(value: largeVersion)])
        XCTAssertEqual(try DailyHealthNormalizer.fact(precise)["metadata"][HKMetadataKeySyncVersion], .int(largeVersion))
        let anchor = HKQueryAnchor(fromValue: 123)
        let raw = try NSKeyedArchiver.archivedData(withRootObject: anchor, requiringSecureCoding: true)
        let restored = try XCTUnwrap(NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: raw))
        XCTAssertEqual(anchor, restored)
        XCTAssertThrowsError(try Normalizer.quantity(HKQuantity(unit: .meter(), doubleValue: 1), type: DailyHealth.hrv))
        let unsupported = HKQuantitySample(type: HKQuantityType(.heartRateVariabilitySDNN), quantity: HKQuantity(unit: .second(), doubleValue: 0.04), start: date, end: date, metadata: ["future_metadata": "preserve_by_not_advancing"])
        XCTAssertThrowsError(try DailyHealthNormalizer.fact(unsupported))
    }
    func testDailyHealthSideSyntheticHistoryCost() throws {
        for days in [7, 30, 90, 180] {
            let started = Date(), perDay = 32
            var bytes = 0
            // Synthetic SDK objects only; no HKHealthStore query or real values.
            for i in 0..<(days * perDay) {
                let start = Date(timeIntervalSince1970: 1_790_000_000 + Double(i) * 2700)
                let sample = HKCategorySample(type: HKCategoryType(.sleepAnalysis), value: i % 6, start: start, end: start.addingTimeInterval(1800))
                bytes += try DailyHealthNormalizer.fact(sample).data().count
            }
            print("M5A_HK_FIXTURE days=\(days) facts=\(days * perDay) bytes=\(bytes) processing_ms=\(Date().timeIntervalSince(started) * 1000)")
        }
    }
}

private actor SyntheticDailyHealth: HealthKitReading {
    private(set) var anchors: [String: [Data?]] = [:]
    func authorize() async throws { }
    func workout(_ id: String) async throws -> HKWorkout? { nil }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage { throw SyncFailure.invalid("workout_channel_unexpected") }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
    func readDaily(type: HKSampleType, anchor: Data?, start: Date, limit: Int) async throws -> HealthPage {
        anchors[type.identifier, default: []].append(anchor)
        let date = Date(timeIntervalSince1970: 1_793_059_200)
        let samples: [HKSample]
        if anchor != nil { samples = [] }
        else if type.identifier == DailyHealth.sleep { samples = [HKCategorySample(type: HKCategoryType(.sleepAnalysis), value: 3, start: date, end: date.addingTimeInterval(3600))] }
        else { let quantityType = type as! HKQuantityType; let unit: HKUnit = type.identifier == DailyHealth.hrv ? .secondUnit(with: .milli) : .count().unitDivided(by: .minute()); samples = [HKQuantitySample(type: quantityType, quantity: HKQuantity(unit: unit, doubleValue: 42), start: date, end: date)] }
        let next = HKQueryAnchor(fromValue: anchors[type.identifier]!.count)
        return HealthPage(samples: samples, deleted: [], anchor: try NSKeyedArchiver.archivedData(withRootObject: next, requiringSecureCoding: true), limit: limit)
    }
}
extension NormalizerTests {
    func testDailyCoordinatorOfflinePendingThenACKAndHotQuery() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-daily-hk-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), health = SyntheticDailyHealth()
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        try await store.bind(scope)
        let coordinator = SyncCoordinator(store: store, health: health)
        try await coordinator.discoverDaily(); try await coordinator.discoverDaily()
        let calls = await health.anchors
        XCTAssertEqual(calls.count, 3); XCTAssertTrue(calls.values.allSatisfy { $0.count == 1 })
        for type in DailyHealth.types {
            let flow = try await store.dailyFlow(type)
            XCTAssertNil(flow.anchor)
            let pending = try XCTUnwrap(flow.pending)
            let raw = try Data(contentsOf: await store.fileURL(pending.file)), body = try JSON.decode(raw)
            XCTAssertEqual(body["facts"].list.count, 1)
            var receipt: [String: JSON] = ["version": .int(1), "requestSHA256": .string(Wire.hash(raw)), "ingestStatus": .string("committed"), "commitRevision": .int(1)]
            receipt["analysisStatus"] = .string("awaiting_model_implementation"); receipt["deleteStatus"] = .string("none")
            receipt["counts"] = .object(Dictionary(uniqueKeysWithValues: ["inserted", "unchanged", "deleted", "tombstoned", "suppressed"].map { ($0, JSON.int(0)) }))
            for key in ["batchID", "datasetID", "datasetEpoch", "collectorEpoch", "streamID", "clientSeq"] { receipt[key] = body[key] }
            try await store.acknowledgeDaily(type: type, receipt: .object(receipt))
        }
        let reopened = try OutboxStore(directory: directory), resumed = SyncCoordinator(store: reopened, health: health)
        try await resumed.discoverDaily()
        let cooled = await health.anchors
        XCTAssertTrue(cooled.values.allSatisfy { $0.count == 1 })
        try await reopened.markDailyWake()
        try await resumed.discoverDaily()
        let incremental = await health.anchors
        for type in DailyHealth.types {
            XCTAssertEqual(incremental[type]?.count, 2); XCTAssertNotNil(incremental[type]?[1])
            let flow = try await reopened.dailyFlow(type), pending = try XCTUnwrap(flow.pending)
            let raw = try JSON.decode(Data(contentsOf: await reopened.fileURL(pending.file)))
            XCTAssertTrue(raw["facts"].list.isEmpty)
            XCTAssertEqual(flow.sequence, 2)
        }
    }
}

// Unsaved SDK objects have no persisted source revision. This fixture installs
// an explicitly synthetic descriptor into the in-memory SDK object via KVC;
// production App code never changes sourceRevision and never writes HealthKit.
private func syntheticVO2(_ sample: HKQuantitySample) -> HKQuantitySample {
    let revision = HKSourceRevision(source: HKSource.default(), version: "synthetic", productType: "Synthetic1,1", operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
    sample.setValue(revision, forKey: "sourceRevision")
    return sample
}
private actor SyntheticVO2Health: HealthKitReading {
    private(set) var requestedAnchors: [Data?] = []
    var unknown = false
    private var wake: (@Sendable () async -> Void)?
    func observeVO2(_ handler: @escaping @Sendable () async -> Void) async throws { wake = handler }
    func fireVO2ObserverHint() async { await wake?() }
    func authorize() async throws {}
    func workout(_ id: String) async throws -> HKWorkout? { nil }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage { throw SyncFailure.invalid("unused") }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
    func setUnknown() { unknown = true }
    func readVO2(anchor: Data?, start: Date, limit: Int) async throws -> HealthPage {
        requestedAnchors.append(anchor)
        let date = max(start.addingTimeInterval(1), Date().addingTimeInterval(-3600))
        let metadata: [String: Any] = unknown ? ["futureSyntheticKey": Data([1])] : [HKMetadataKeyVO2MaxTestType: NSNumber(value: 4), HKMetadataKeyAppleDeviceCalibrated: NSNumber(value: true), HKMetadataKeyAlgorithmVersion: NSNumber(value: Int64(9007199254740993))]
        let samples: [HKSample] = anchor == nil ? [syntheticVO2(HKQuantitySample(type: HKQuantityType(.vo2Max), quantity: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 42.123456789012345), start: date, end: date, metadata: metadata))] : []
        return HealthPage(samples: samples, deleted: [], anchor: try NSKeyedArchiver.archivedData(withRootObject: HKQueryAnchor(fromValue: requestedAnchors.count), requiringSecureCoding: true), limit: limit)
    }
}
extension NormalizerTests {
    func testVO2SDKMetadataAndProvenance() throws {
        let date = Date(timeIntervalSince1970: 1793000000)
        for test in [nil, 1, 2, 3, 4] as [Int?] {
            for manual in [nil, false, true] as [Bool?] {
                for calibrated in [nil, false, true] as [Bool?] {
                    var metadata: [String: Any] = [HKMetadataKeyDateOfEarliestDataUsedForEstimate: date,
                        HKMetadataKeyVO2MaxValue: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 41.25)]
                    if let test { metadata[HKMetadataKeyVO2MaxTestType] = NSNumber(value: test) }
                    if let manual { metadata[HKMetadataKeyWasUserEntered] = NSNumber(value: manual) }
                    if let calibrated { metadata[HKMetadataKeyAppleDeviceCalibrated] = NSNumber(value: calibrated) }
                    let d = HKDevice(name: "Synthetic Device", manufacturer: "Synthetic", model: "Fixture", hardwareVersion: nil, firmwareVersion: nil, softwareVersion: nil, localIdentifier: "excluded", udiDeviceIdentifier: "excluded")
                    let sample = syntheticVO2(HKQuantitySample(type: HKQuantityType(.vo2Max), quantity: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 80.12345678901234), start: date, end: date, device: d, metadata: metadata))
                    let fact = try VO2MaxNormalizer.fact(sample)
                    XCTAssertEqual(fact["quantity"]["value"], .number(80.12345678901234))
                    XCTAssertEqual(fact["metadata"][HKMetadataKeyVO2MaxValue]["type"], .string("quantity"))
                    XCTAssertEqual(fact["provenance"]["device"]["name"], .string("Synthetic Device"))
                    XCTAssertEqual(fact["provenance"]["device"]["firmwareVersion"], .null)
                    XCTAssertFalse(String(decoding: try fact.data(), as: UTF8.self).contains("excluded"))
                }
            }
        }
        for integer in [Int64(9007199254740993), .min, .max] {
            let sample = syntheticVO2(HKQuantitySample(type: HKQuantityType(.vo2Max), quantity: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 42), start: date, end: date, metadata: [HKMetadataKeyAlgorithmVersion: NSNumber(value: integer)]))
            XCTAssertEqual(try VO2MaxNormalizer.fact(sample)["metadata"][HKMetadataKeyAlgorithmVersion]["value"], .int(integer))
        }
        for metadata: [String: Any] in [["future": "x"], ["future": Data()], ["future": ["nested": 1]], [HKMetadataKeyVO2MaxTestType: NSNumber(value: 0)], [HKMetadataKeyVO2MaxTestType: NSNumber(value: 5)]] {
            let sample = syntheticVO2(HKQuantitySample(type: HKQuantityType(.vo2Max), quantity: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 42), start: date, end: date))
            // Deliberately invalid future metadata cannot be constructed by the
            // public SDK factory; inject only in this synthetic read fixture.
            sample.setValue(metadata, forKey: "metadata")
            XCTAssertThrowsError(try VO2MaxNormalizer.fact(sample))
        }
        let event = HKCategorySample(type: HKCategoryType(.lowCardioFitnessEvent), value: HKCategoryValueLowCardioFitnessEvent.lowFitness.rawValue, start: date, end: date, metadata: [HKMetadataKeyVO2MaxValue: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 30)])
        XCTAssertThrowsError(try VO2MaxNormalizer.fact(event))
        XCTAssertThrowsError(try VO2MaxNormalizer.quantity(HKQuantity(unit: .liter().unitDivided(by: .minute()), doubleValue: 4)))
    }
    func testVO2IndependentPendingAndBlockedSDKPage() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-vo2-sdk-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), health = SyntheticVO2Health()
        try await store.bind(CoordinatorReplies.identity)
        let coordinator = SyncCoordinator(store: store, health: health)
        try await coordinator.discoverVO2(); try await coordinator.discoverVO2()
        let state = await store.snapshot()
        XCTAssertNil(state.vo2?.anchor); XCTAssertNotNil(state.vo2?.pending)
        let queries = await health.requestedAnchors.count; XCTAssertEqual(queries, 1)
        XCTAssertNil(state.daily)
        try await coordinator.observeDaily()
        await health.fireVO2ObserverHint()
        let observed = await store.snapshot()
        XCTAssertEqual(observed.vo2WakeGeneration, 1)
        XCTAssertNil(observed.dailyWakeGeneration)
        XCTAssertEqual(observed.vo2?.pending?.hash, state.vo2?.pending?.hash)
        XCTAssertNil(observed.vo2?.anchor)
        let pending = try XCTUnwrap(state.vo2?.pending)
        let raw = try Data(contentsOf: await store.fileURL(pending.file))
        XCTAssertEqual(try JSON.decode(raw)["facts"].list.count, 1)
        let other = try OutboxStore(directory: directory.appendingPathComponent("blocked")), badHealth = SyntheticVO2Health()
        try await other.bind(CoordinatorReplies.identity); await badHealth.setUnknown()
        let blocked = SyncCoordinator(store: other, health: badHealth); try await blocked.discoverVO2()
        let bad = await other.snapshot()
        XCTAssertNotNil(bad.vo2?.blocked); XCTAssertNil(bad.vo2?.pending); XCTAssertNil(bad.vo2?.anchor)
    }
    func testVO2SimulatorHTTPPostgresEndToEnd() async throws {
        guard let encoded = ProcessInfo.processInfo.environment["RAI_M4B2_SYNTHETIC"] else { throw XCTSkip("requires isolated PG18 Simulator harness") }
        let config = try JSON.decode(Data(encoded.utf8))
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-vo2-e2e-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), health = SyntheticVO2Health()
        let scope = Scope(serviceID: try XCTUnwrap(config["serviceID"].text), datasetID: try XCTUnwrap(config["datasetID"].text), datasetEpoch: try XCTUnwrap(config["datasetEpoch"].text), collectorEpoch: try XCTUnwrap(config["collectorEpoch"].text))
        try await store.bind(scope)
        let coordinator = SyncCoordinator(store: store, health: health)
        try await coordinator.discoverVO2()
        let staged = await store.snapshot()
        let pending = try XCTUnwrap(staged.vo2?.pending)
        let raw = try Data(contentsOf: await store.fileURL(pending.file))
        let secret = ClientConfiguration.Secret(configuration: .init(baseURL: URL(string: try XCTUnwrap(config["baseURL"].text))!, collectorEpoch: scope.collectorEpoch), token: try XCTUnwrap(config["token"].text))
        let transfers = BackgroundTransfers(directory: directory, identifier: "com.rai.v2.m4b2." + Wire.id())
        for _ in 0..<12 {
            try await coordinator.syncDaily(secret: secret, transfers: transfers)
            let state = await store.snapshot()
            if state.vo2?.anchor != nil { break }
            XCTAssertEqual(state.vo2?.pending?.batchID, pending.batchID)
            let pendingURL = try await store.fileURL(pending.file)
            XCTAssertEqual(try Data(contentsOf: pendingURL), raw)
            let delay = max(1, state.vo2?.pending?.nextAttempt.timeIntervalSinceNow ?? 1)
            try await Task.sleep(for: .seconds(min(delay + 0.2, 10)))
        }
        let final = await store.snapshot()
        XCTAssertNil(final.vo2?.pending); XCTAssertNotNil(final.vo2?.anchor)
        XCTAssertEqual(final.vo2?.sequence, 2)
        XCTAssertEqual(final.vo2?.lastReceipt?["requestSHA256"], .string(Wire.hash(raw)))
        XCTAssertEqual(final.vo2?.lastReceipt?["analysisStatus"], .string("awaiting_model_implementation"))
        let reopened = try OutboxStore(directory: directory)
        let reloaded = await reopened.snapshot()
        XCTAssertEqual(reloaded.vo2?.anchor, final.vo2?.anchor)
    }
    func testVO2SDKNormalizationPerformance() throws {
        let date = Date(timeIntervalSince1970: 1793000000)
        for count in [0,1,128,90] {
            let samples = (0..<count).map { i in syntheticVO2(HKQuantitySample(type: HKQuantityType(.vo2Max), quantity: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 42.123456789012345), start: date.addingTimeInterval(Double(i)*86400), end: date.addingTimeInterval(Double(i)*86400), metadata: [HKMetadataKeyVO2MaxTestType: NSNumber(value: 4)])) }
            let start = Date(); var bytes = 0
            for sample in samples { bytes += try VO2MaxNormalizer.fact(sample).data().count }
            print("M4B2_SYNTHETIC_NORMALIZE count=\(count) bytes=\(bytes) us=\(Date().timeIntervalSince(start)*1_000_000)")
        }
    }
}

private final class VO2OnlyHandshake: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let s = CoordinatorReplies.identity
        let body: JSON = .object(["protocolVersion": .int(3), "serviceID": .string(s.serviceID), "datasetID": .string(s.datasetID), "datasetEpoch": .string(s.datasetEpoch),
            "capabilities": .object(["workoutBundles": .bool(true), "bundleProtocolVersion": .int(3), "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) }),
                "vo2maxFactsVersion": .int(1), "vo2maxMetadataRegistryVersion": .int(1), "vo2maxMaxChanges": .int(128), "vo2maxMaxBytes": .int(524288)])])
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! body.data()); client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
private final class VO2CoordinatorUploader: FileUploader, Sendable {
    let paths = Mutex<[String]>([])
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        paths.withLock { $0.append(request.url!.path) }
        let raw = try Data(contentsOf: file), body = try JSON.decode(raw)
        var fields = body.fields.filter { ["contract", "version", "metadataRegistryVersion", "batchID", "datasetID", "datasetEpoch", "collectorEpoch", "streamID", "clientSeq"].contains($0.key) }
        fields["requestSHA256"] = .string(Wire.hash(raw)); fields["ingestStatus"] = .string("committed"); fields["commitRevision"] = .int(1)
        fields["deleteStatus"] = .string("none"); fields["analysisStatus"] = .string("awaiting_model_implementation")
        fields["counts"] = .object(["inserted": .int(1), "unchanged": .int(0), "deleted": .int(0), "tombstoned": .int(0), "suppressed": .int(0)])
        return UploadResponse(data: try JSON.object(fields).data(), status: 200, contentType: "application/json")
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) { let r = try await response(request, file: file, identity: identity); return (r.data, r.status, r.retryAfter) }
}
extension NormalizerTests {
    func testVO2CapabilityOperatesIndependentlyOfDailyCapability() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-vo2-only-" + Wire.id()); defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir), health = SyntheticVO2Health(), uploader = VO2CoordinatorUploader()
        try await store.bind(CoordinatorReplies.identity)
        let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [VO2OnlyHandshake.self]
        let coordinator = SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))
        try await coordinator.discoverVO2()
        let secret = ClientConfiguration.Secret(configuration: .init(baseURL: URL(string: "https://synthetic.invalid")!, collectorEpoch: CoordinatorReplies.identity.collectorEpoch), token: "synthetic")
        try await coordinator.syncDaily(secret: secret, transfers: uploader)
        let state = await store.snapshot()
        XCTAssertNotNil(state.vo2?.anchor); XCTAssertNil(state.vo2?.pending); XCTAssertNil(state.daily)
        XCTAssertEqual(uploader.paths.withLock { $0 }, ["/v2/sync/vo2max-facts"])
    }
}

extension NormalizerTests {
    func testVO2SDKBootstrapPredicateKeepsOverlappingInterval() throws {
        let lower = Date(timeIntervalSince1970: 1793000000)
        let predicate = HKQuery.predicateForSamples(withStart: lower, end: nil, options: [])
        func sample(_ start: Double, _ end: Double) -> HKQuantitySample {
            HKQuantitySample(type: HKQuantityType(.vo2Max), quantity: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: 42), start: lower.addingTimeInterval(start), end: lower.addingTimeInterval(end))
        }
        XCTAssertTrue(predicate.evaluate(with: sample(-1, 1)))
        XCTAssertTrue(predicate.evaluate(with: sample(1, 2)))
        XCTAssertFalse(predicate.evaluate(with: sample(-2, -1)))
    }
}

// B4 fixture injection ends at HealthKitReading. No HKHealthStore is queried.
// Runtime-allocated SDK descriptors are synthetic and exist only in XCTest.
private actor B4SyntheticHealth: HealthKitReading {
    let recipe: String
    let count: Int
    let now = Date()
    private var offset = 0
    private var query = 0
    init(_ recipe: String, count: Int) { self.recipe = recipe; self.count = count }
    func authorize() async throws {}
    func workout(_ id: String) async throws -> HKWorkout? { nil }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage { throw SyncFailure.invalid("unused") }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
    func readVO2(anchor: Data?, start: Date, limit: Int) async throws -> HealthPage {
        query += 1
        if recipe == "incomplete" && offset > 0 { throw SyncFailure.invalid("unsupported_metadata") }
        let begin = Date()
        var samples: [HKSample] = []
        for i in offset..<min(count, offset + limit) {
            let source = class_createInstance(HKSource.self, 0) as! HKSource
            source.setValue(recipe == "third_party" ? "org.synthetic.other" : "com.apple.health", forKey: "bundleIdentifier")
            source.setValue("Synthetic", forKey: "name")
            let epoch = recipe == "epochs32" || recipe == "epochs" ? i : 0
            let revision = HKSourceRevision(source: source, version: "synthetic-\(epoch)", productType: "Watch6,1", operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
            let age: Double = recipe == "stale" ? 45 * 86400 : recipe == "expired" ? 91 * 86400 : recipe == "bootstrap90" ? Double(i) * 86400 + 3600 : 3600 + (recipe == "ties256" || recipe == "conflict" || recipe == "equivalent" ? 0 : Double(i))
            let date = now.addingTimeInterval(-age)
            let device = HKDevice(name: "Synthetic", manufacturer: "Apple", model: "Watch", hardwareVersion: nil, firmwareVersion: nil, softwareVersion: nil, localIdentifier: nil, udiDeviceIdentifier: nil)
            var metadata: [String: Any] = [HKMetadataKeyVO2MaxTestType: NSNumber(value: recipe == "unclassified" ? 1 : 4), HKMetadataKeyAppleDeviceCalibrated: NSNumber(value: true), HKMetadataKeyAlgorithmVersion: NSNumber(value: Int64(9007199254740993))]
            if recipe == "manual" { metadata[HKMetadataKeyWasUserEntered] = NSNumber(value: true) }
            let value = recipe == "conflict" && i == 1 ? 48.123456789012344 : 47.123456789012344
            let sample = HKQuantitySample(type: HKQuantityType(.vo2Max), quantity: HKQuantity(unit: VO2MaxNormalizer.unit, doubleValue: value), start: date, end: date, device: device, metadata: metadata)
            sample.setValue(revision, forKey: "sourceRevision")
            // Explicitly check the actual SDK quantity before the formal normalizer.
            guard sample.quantity.doubleValue(for: VO2MaxNormalizer.unit).bitPattern == value.bitPattern else { throw SyncFailure.invalid("fixture_precision") }
            samples.append(sample)
        }
        offset += samples.count
        print("M4B4_SYNTHETIC phase=fixture count=\(samples.count) duration_us=\(Int(Date().timeIntervalSince(begin)*1_000_000))")
        return HealthPage(samples: samples, deleted: [], anchor: try NSKeyedArchiver.archivedData(withRootObject: HKQueryAnchor(fromValue: query), requiringSecureCoding: true), limit: limit)
    }
}

extension NormalizerTests {
    func testB4SimulatorFullExternalClosure() async throws {
        guard let encoded = ProcessInfo.processInfo.environment["RAI_M4B4_SYNTHETIC"] else { throw XCTSkip("requires B4 disposable PG18 host") }
        let configs = try JSON.decode(Data(encoded.utf8)).list
        XCTAssertFalse(configs.isEmpty)
        for config in configs {
            let recipe = try XCTUnwrap(config["recipe"].text), count = Int(try XCTUnwrap(config["count"].integer))
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-b4-" + Wire.id())
            defer { try? FileManager.default.removeItem(at: directory) }
            let begin = Date(), store = try OutboxStore(directory: directory), health = B4SyntheticHealth(recipe, count: count)
            let scope = Scope(serviceID: config["serviceID"].text!, datasetID: config["datasetID"].text!, datasetEpoch: config["datasetEpoch"].text!, collectorEpoch: config["collectorEpoch"].text!)
            try await store.bind(scope)
            _ = try await store.vo2Flow(now: Date().addingTimeInterval(-3 * 86400))
            let coordinator = SyncCoordinator(store: store, health: health)
            let stageStart = Date()
            try await coordinator.discoverVO2()
            let initial = await store.snapshot(), pending = try XCTUnwrap(initial.vo2?.pending)
            XCTAssertNil(initial.vo2?.anchor)
            let raw = try Data(contentsOf: await store.fileURL(pending.file))
            XCTAssertEqual(Wire.hash(raw), pending.hash)
            print("M4B4_SYNTHETIC phase=normalize_outbox count=\(count) duration_us=\(Int(Date().timeIntervalSince(stageStart)*1_000_000))")
            let secret = ClientConfiguration.Secret(configuration: .init(baseURL: URL(string: config["baseURL"].text!)!, collectorEpoch: scope.collectorEpoch), token: config["token"].text!)
            let transfers = BackgroundTransfers(directory: directory, identifier: "com.rai.v2.b4." + Wire.id())
            let deliveryStart = Date()
            for _ in 0..<40 {
                try await coordinator.syncDaily(secret: secret, transfers: transfers)
                let state = await store.snapshot()
                if recipe == "incomplete" && state.vo2?.error != nil { break }
                if state.vo2?.bootstrapComplete == true && state.vo2?.pending == nil { break }
                if state.vo2?.pending?.batchID == pending.batchID {
                    XCTAssertNil(state.vo2?.anchor)
                    let retainedURL = try await store.fileURL(pending.file)
                    XCTAssertEqual(try Data(contentsOf: retainedURL), raw)
                }
                try await Task.sleep(for: .seconds(1))
            }
            let final = await store.snapshot()
            print("M4B4_SYNTHETIC phase=delivery_final status=\(final.vo2?.error ?? "none") pending=\(final.vo2?.pending != nil) anchor=\(final.vo2?.anchor != nil)")
            XCTAssertNil(final.vo2?.pending); XCTAssertNotNil(final.vo2?.anchor); XCTAssertEqual(final.vo2?.bootstrapComplete, recipe != "incomplete")
            let reopened = try OutboxStore(directory: directory)
            let restored = await reopened.snapshot()
            XCTAssertEqual(restored.vo2?.anchor, final.vo2?.anchor)
            print("M4B4_SYNTHETIC phase=http_ack_anchor count=\(count) duration_us=\(Int(Date().timeIntervalSince(deliveryStart)*1_000_000))")
            // This private harness checkpoint waits for real admin selection,
            // Worker Claim/Process, candidate read and assertions in the PG host.
            var request = URLRequest(url: URL(string: config["baseURL"].text! + "/__b4/checkpoint")!)
            request.httpMethod = "POST"
            let (_, response) = try await URLSession.shared.data(for: request)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 204)
            if recipe == "fresh" {
                let hot = Date()
                try await store.markVO2Wake()
                try await coordinator.discoverVO2()
                for _ in 0..<15 {
                    try await coordinator.syncDaily(secret: secret, transfers: transfers)
                    let state = await store.snapshot()
                    if state.vo2?.pending == nil { break }
                    try await Task.sleep(for: .seconds(1))
                }
                let hotState = await store.snapshot()
                XCTAssertNil(hotState.vo2?.pending)
                XCTAssertNotEqual(hotState.vo2?.anchor, final.vo2?.anchor)
                let (_, hotResponse) = try await URLSession.shared.data(for: request)
                XCTAssertEqual((hotResponse as? HTTPURLResponse)?.statusCode, 204)
                print("M4B4_SYNTHETIC phase=hot_empty_e2e count=0 duration_us=\(Int(Date().timeIntervalSince(hot)*1_000_000))")
            }

            print("M4B4_SYNTHETIC phase=total_e2e count=\(count) duration_us=\(Int(Date().timeIntervalSince(begin)*1_000_000))")
        }
    }
}

extension NormalizerTests {
    // Component observations complement the separately executed full E2E.
    // ACK here is synthetic: this test never claims HTTP/PG/Worker acceptance.
    func testB4SDKAndOutboxPhaseObservations() async throws {
        for count in [0, 1, 128, 90, 1024, 32, 256] {
            let health = B4SyntheticHealth("component", count: count)
            var samples: [HKSample] = []
            repeat {
                let page = try await health.readVO2(anchor: nil, start: .distantPast, limit: 128)
                samples += page.samples
                if !page.isFull { break }
            } while true
            let normalize = Date(), facts = try samples.map { try VO2MaxNormalizer.fact($0) }
            let normalizeUs = Int(Date().timeIntervalSince(normalize)*1_000_000)
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-b4-phases-" + Wire.id())
            defer { try? FileManager.default.removeItem(at: directory) }
            let store = try OutboxStore(directory: directory)
            try await store.bind(CoordinatorReplies.identity)
            _ = try await store.vo2Flow()
            var persistenceUs = 0, ackUs = 0, anchorUs = 0
            for page in 0..<max(1, (count + 127) / 128) {
                let flow = try await store.vo2Flow(), begin = Date()
                let chunk = Array(facts.dropFirst(page * 128).prefix(128))
                try await store.stageVO2(previousAnchor: flow.anchor, candidateAnchor: Data([UInt8(page + 1)]), facts: chunk, deleted: [], observedAt: Date(), hasMore: (page + 1) * 128 < count, wakeGeneration: 0)
                persistenceUs += Int(Date().timeIntervalSince(begin)*1_000_000)
                let stagedState = await store.snapshot()
                let pending = try XCTUnwrap(stagedState.vo2?.pending)
                let raw = try Data(contentsOf: await store.fileURL(pending.file)), body = try JSON.decode(raw)
                var ack = body.fields.filter { ["contract", "version", "metadataRegistryVersion", "batchID", "datasetID", "datasetEpoch", "collectorEpoch", "streamID", "clientSeq"].contains($0.key) }
                ack["requestSHA256"] = .string(Wire.hash(raw)); ack["ingestStatus"] = .string("committed"); ack["commitRevision"] = .int(Int64(page+1)); ack["deleteStatus"] = .string("none"); ack["analysisStatus"] = .string("awaiting_model_implementation")
                ack["counts"] = .object(["inserted": .int(Int64(chunk.count)), "unchanged": .int(0), "deleted": .int(0), "tombstoned": .int(0), "suppressed": .int(0)])
                let receipt = JSON.object(ack), validate = Date()
                try await store.validateVO2Receipt(receipt)
                ackUs += Int(Date().timeIntervalSince(validate)*1_000_000)
                let commit = Date()
                try await store.acknowledgeVO2(receipt)
                anchorUs += Int(Date().timeIntervalSince(commit)*1_000_000)
            }
            print("M4B4_COMPONENT count=\(count) normalize_us=\(normalizeUs) outbox_us=\(persistenceUs) ack_validation_us=\(ackUs) validated_anchor_commit_us=\(anchorUs)")
        }
    }
}
