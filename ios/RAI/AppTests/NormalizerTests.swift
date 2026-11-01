import XCTest
import Security
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

final class NormalizerTests: XCTestCase {
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
