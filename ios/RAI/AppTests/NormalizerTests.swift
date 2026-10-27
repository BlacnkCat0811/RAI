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
    func testMetadataIsExplicitAllowlist() {
        let value = Normalizer.metadata([HKMetadataKeyIndoorWorkout: true, "private_unknown": "synthetic"])
        XCTAssertEqual(value?.fields.count, 1); XCTAssertEqual(value?["private_unknown"], .null)
    }
    func testCanonicalTimeRetainsMicroseconds() { XCTAssertEqual(Normalizer.us(Date(timeIntervalSince1970: 1.123456)), 1_123_456) }
}
