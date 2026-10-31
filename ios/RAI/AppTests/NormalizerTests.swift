import XCTest
import HealthKit
import SyncCore
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
        let scalar = try XCTUnwrap(Normalizer.workoutMetadata(["unsupported_synthetic_key": 1]))
        XCTAssertEqual(scalar["unsupported_synthetic_key"], .number(1))
        XCTAssertThrowsError(try Normalizer.workoutMetadata(["unsupported_object_key": ["nested": 1]]))
        XCTAssertThrowsError(try Normalizer.workoutMetadata([HKMetadataKeyWeatherTemperature: HKQuantity(unit: .meter(), doubleValue: 1)]))
        XCTAssertEqual(HealthKitClient.quantityTypes.count, 10)
    }
    func testMetadataIsExplicitAllowlist() {
        let value = Normalizer.metadata([HKMetadataKeyIndoorWorkout: true, "private_unknown": "synthetic"])
        XCTAssertEqual(value?.fields.count, 1); XCTAssertEqual(value?["private_unknown"], .null)
    }
    func testCanonicalTimeRetainsMicroseconds() { XCTAssertEqual(Normalizer.us(Date(timeIntervalSince1970: 1.123456)), 1_123_456) }

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

private actor NoHealthReads: HealthKitReading {
    private(set) var queried = false
    private func unexpected() throws -> Never { queried = true; throw SyncFailure.invalid("unexpected_health_read") }
    func authorize() async throws { try unexpected() }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage { try unexpected() }
    func workout(_ id: String) async throws -> HKWorkout? { try unexpected() }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { try unexpected() }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { try unexpected() }
}
private struct NoBackgroundUpload: FileUploader {
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) { throw SyncFailure.invalid("unexpected_background_upload") }
}
private final class SavedAssociationProtocol: URLProtocol, @unchecked Sendable {
    static let service = "11111111-1111-4111-8111-111111111111"
    static let dataset = "22222222-2222-4222-8222-222222222222"
    static let epoch = "33333333-3333-4333-8333-333333333333"
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "synthetic.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let path = request.url!.path
        let json: JSON
        if path == "/v2/handshake" {
            json = .object(["protocolVersion": .int(2), "serviceID": .string(Self.service), "datasetID": .string(Self.dataset), "datasetEpoch": .string(Self.epoch),
                "capabilities": .object(["pageReceipts": .bool(true), "routes": .bool(true), "workoutEnrichment": .bool(true), "sampleWorkoutAssociations": .bool(true), "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) })]),
                "limits": .object(["events": .int(1000), "wireBytes": .int(2_097_152), "decodedBytes": .int(8_388_608), "routePointsPerChunk": .int(5000)])])
        } else if path == "/v2/sync/associations" { json = .object(["ingestStatus": .string("committed")]) }
        else { client?.urlProtocol(self, didFailWithError: SyncFailure.invalid("unexpected_api_path")); return }
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! json.data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
extension NormalizerTests {
    func testSavedAssociationDrainDoesNotRequireHealthReads() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-saved-association-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let page = PendingPage(anchorKey: "association:synthetic:heart-rate", candidateAnchor: Data([99]), operations: [.request(.init(method: "POST", path: "/v2/sync/associations", expected: ["ingestStatus": .string("committed")]))])
        try await store.stage(page)
        try await store.fail(page.id, error: UploadError(code: "device_rate_exceeded", status: 429))
        try await store.retryBlocked()
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [SavedAssociationProtocol.self]
        let session = URLSession(configuration: config)
        defer { session.invalidateAndCancel() }
        let health = NoHealthReads()
        let coordinator = SyncCoordinator(store: store, health: health, session: session)
        let secret = ClientConfiguration.Secret(configuration: .init(baseURL: URL(string: "https://synthetic.invalid")!, collectorEpoch: Wire.id()), token: "synthetic-token")
        try await coordinator.sync(secret: secret, transfers: NoBackgroundUpload(), readHealth: false)
        let state = await store.snapshot(), queried = await health.queried
        XCTAssertTrue(state.pages.isEmpty)
        XCTAssertEqual(state.localAnchors[page.anchorKey], Data([99]))
        XCTAssertFalse(queried)
    }
}
