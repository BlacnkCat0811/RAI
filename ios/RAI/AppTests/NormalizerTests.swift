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
            HKMetadataKeyWeatherHumidity: HKQuantity(unit: .percent(), doubleValue: 0.6),
            HKMetadataKeyWeatherTemperature: HKQuantity(unit: .degreeFahrenheit(), doubleValue: 68),
        ]))
        XCTAssertEqual(value.fields.count, 6)
        XCTAssertEqual(value[HKMetadataKeyAverageMETs]["unit"], .string("kcal/(kg*hr)"))
        XCTAssertEqual(value[HKMetadataKeyElevationAscended]["value"], .number(100))
        XCTAssertEqual(value[HKMetadataKeyWeatherHumidity]["value"], .number(0.6))
        if case .number(let temperature) = value[HKMetadataKeyWeatherTemperature]["value"] { XCTAssertEqual(temperature, 20, accuracy: 0.00000001) }
        else { XCTFail("Source temperature missing") }
        XCTAssertThrowsError(try Normalizer.workoutMetadata(["unsupported_synthetic_key": 1]))
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
