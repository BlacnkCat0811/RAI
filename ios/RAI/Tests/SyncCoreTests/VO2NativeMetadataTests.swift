import Foundation
import Testing
@testable import SyncCore

@Suite struct VO2NativeMetadataTests {
    @Test func integerSemanticKeysAcceptOnlyExactNativeCarriers() throws {
        for key in ["HKVO2MaxTestType", "HKMetadataKeySyncVersion", "HKAlgorithmVersion"] {
            for i in 1...4 {
                for n in [NSNumber(value: i), NSNumber(value: Int64(i)), NSNumber(value: Double(i)), NSNumber(value: Float(i))] {
                    #expect(try VO2Max.metadata(key: key, value: n) == .object(["type": .string("int64"), "value": .int(Int64(i))]))
                }
            }
            for n in [NSNumber(value: 1.25), NSNumber(value: true), NSNumber(value: false),
                NSNumber(value: Double.nan), NSNumber(value: Double.infinity), NSNumber(value: -Double.infinity),
                NSNumber(value: Double(9_007_199_254_740_992)), NSNumber(value: Double(9_007_199_254_740_993)),
                NSNumber(value: UInt64.max), NSDecimalNumber(string: "2")] {
                #expect(throws: (any Error).self) { try VO2Max.metadata(key: key, value: n) }
            }
            for n: Int64 in [-1, 0, 5, 9_007_199_254_740_993, .min, .max] {
                if key == "HKVO2MaxTestType" {
                    #expect(throws: (any Error).self) { try VO2Max.metadata(key: key, value: NSNumber(value: n)) }
                } else {
                    #expect(try VO2Max.metadata(key: key, value: NSNumber(value: n))["value"] == .int(n))
                }
            }
            for value: Any in ["2", Data(), ["value": 2]] {
                #expect(throws: (any Error).self) { try VO2Max.metadata(key: key, value: value) }
            }
        }
    }

    @Test func safeFloatingVersionsPreserveExistingSignedIntegerDomainProperty() throws {
        for key in ["HKMetadataKeySyncVersion", "HKAlgorithmVersion"] {
            for i in -512...512 {
                let native = try VO2Max.metadata(key: key, value: NSNumber(value: Double(i)))
                #expect(native["value"] == .int(Int64(i)))
                #expect(try JSON.decode(native.data()) == native)
            }
            let safe = Int64(9_007_199_254_740_991)
            #expect(try VO2Max.metadata(key: key, value: NSNumber(value: Double(safe)))["value"] == .int(safe))
            #expect(try VO2Max.metadata(key: key, value: NSNumber(value: Double(-safe)))["value"] == .int(-safe))
        }
    }

    @Test func booleanAndStringKeysKeepTheirOwnTypes() throws {
        let numeric: [NSNumber] = [NSNumber(value: 1), NSNumber(value: Int64.max), NSNumber(value: 1.0),
            NSNumber(value: 1.25), NSNumber(value: Double.nan), NSNumber(value: Double.infinity), NSNumber(value: -1),
            NSNumber(value: Double(9_007_199_254_740_992)), NSNumber(value: Double(9_007_199_254_740_993))]
        for key in ["HKWasUserEntered", "HKMetadataKeyAppleDeviceCalibrated"] {
            for bool in [true, false] { #expect(try VO2Max.metadata(key: key, value: NSNumber(value: bool))["value"] == .bool(bool)) }
            for n in numeric { #expect(throws: (any Error).self) { try VO2Max.metadata(key: key, value: n) } }
        }
        for key in ["HKMetadataKeySyncIdentifier", "HKTimeZone"] {
            for n in numeric + [NSNumber(value: true), NSNumber(value: false)] {
                #expect(throws: (any Error).self) { try VO2Max.metadata(key: key, value: n) }
            }
        }
        #expect(try VO2Max.metadata(key: "HKMetadataKeySyncIdentifier", value: "synthetic-sync-identity")["value"] == .string("synthetic-sync-identity"))
        #expect(throws: (any Error).self) { try VO2Max.metadata(key: "future", value: NSNumber(value: 2.0)) }
    }

    @Test func sharedNativeCarrierGolden() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let fixture = try JSON.decode(Data(contentsOf: root.appendingPathComponent("contracts/fixtures/vo2max-native-metadata-v1.json")))
        for vector in fixture["cases"].list {
            var metadata: [String: JSON] = [:]
            for (key, native) in vector["native"].fields {
                let value: Any
                switch native["carrier"].text {
                case "double": value = NSNumber(value: Double(native["value"].text!)!)
                case "int64": value = NSNumber(value: Int64(native["value"].text!)!)
                case "boolean": value = NSNumber(value: native["value"] == .bool(true))
                case "string": value = native["value"].text!
                default: Issue.record("Unknown synthetic carrier"); continue
                }
                metadata[key] = try VO2Max.metadata(key: key, value: value)
            }
            #expect(try JSON.object(metadata).data() == vector["canonicalMetadata"].data())
        }
    }
}
