import Foundation
import HealthKit
import SyncCore
import CoreFoundation

/// Only a genuine root VO2 quantity sample produces a fact. Event metadata
/// (including HKVO2MaxValue) cannot create a second fact.
enum VO2MaxNormalizer {
    static func blockedPage(_ archive: Data) throws -> (facts: [JSON], deleted: [String]) {
        guard let objects = try NSKeyedUnarchiver.unarchivedObject(
            ofClasses: [NSArray.self, HKSample.self, HKDeletedObject.self], from: archive) as? [NSObject],
              objects.allSatisfy({ $0 is HKSample || $0 is HKDeletedObject }) else {
            throw SyncFailure.localState("vo2_blocked_archive_invalid")
        }
        return (try objects.compactMap { $0 as? HKSample }.map(fact),
            objects.compactMap { $0 as? HKDeletedObject }.map { $0.uuid.uuidString.lowercased() })
    }
    static let unit = HKUnit.literUnit(with: .milli).unitDivided(by: .gramUnit(with: .kilo)).unitDivided(by: .minute())
    static func quantity(_ quantity: HKQuantity) throws -> Double {
        guard quantity.is(compatibleWith: unit) else { throw SyncFailure.invalid("unsupported_unit") }
        let value = quantity.doubleValue(for: unit)
        guard value.isFinite else { throw SyncFailure.invalid("invalid_value") }
        return value == 0 ? 0 : value
    }
    static func fact(_ sample: HKSample) throws -> JSON {
        guard sample.sampleType.identifier == VO2Max.type, let sample = sample as? HKQuantitySample,
              sample.count == 1 else { throw SyncFailure.invalid("unsupported_type") }
        let value = try quantity(sample.quantity)
        let start = try VO2Max.microseconds(sample.startDate), end = try VO2Max.microseconds(sample.endDate)
        guard value > 0, end >= start else { throw SyncFailure.invalid("invalid_value") }
        var metadata: [String: JSON] = [:]
        guard (sample.metadata?.count ?? 0) <= 64 else { throw SyncFailure.invalid("unsupported_metadata") }
        for (key, value) in sample.metadata ?? [:] {
            if key == HKMetadataKeyVO2MaxValue {
                guard let q = value as? HKQuantity else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = try VO2Max.quantity(value: quantity(q))
            } else { metadata[key] = try VO2Max.metadata(key: key, value: value) }
        }
        guard (metadata[HKMetadataKeySyncIdentifier] == nil) == (metadata[HKMetadataKeySyncVersion] == nil) else { throw SyncFailure.invalid("unsupported_metadata") }
        let p = try provenance(sample)
        let context: JSON = .object(["schemaVersion": .string("rai.healthkit.vo2max_fact/1"), "provenance": p,
            "sourceEpochID": .string("sha256:" + Wire.hash(try p.data())), "typedMetadata": .object(metadata)])
        guard try context.data().count <= VO2Max.maxContextBytes else { throw SyncFailure.invalid("batch_too_large") }
        return .object(["uuid": .string(sample.uuid.uuidString.lowercased()), "startUs": .int(start), "endUs": .int(end),
            "quantity": .object(["value": .number(value), "unit": .string("mL/kg/min")]), "provenance": p, "metadata": .object(metadata)])
    }
    static func provenance(_ sample: HKSample) throws -> JSON {
        func text(_ value: String?) throws -> JSON {
            guard let value else { return .null }
            guard value.utf8.count <= 255 else { throw SyncFailure.invalid("source_invalid") }
            return .string(value)
        }
        let r = sample.sourceRevision, os = r.operatingSystemVersion
        guard !r.source.bundleIdentifier.isEmpty, os.majorVersion >= 0, os.minorVersion >= 0, os.patchVersion >= 0 else { throw SyncFailure.invalid("source_invalid") }
        var device: JSON = .null
        if let d = sample.device {
            device = try .object(["name": text(d.name), "manufacturer": text(d.manufacturer), "model": text(d.model),
                "hardwareVersion": text(d.hardwareVersion), "firmwareVersion": text(d.firmwareVersion), "softwareVersion": text(d.softwareVersion)])
        }
        return try .object(["schemaVersion": .string("rai.healthkit.provenance/2"), "device": device,
            "sourceRevision": .object(["source": .object(["bundleIdentifier": text(r.source.bundleIdentifier), "name": text(r.source.name)]),
                "version": text(r.version), "productType": text(r.productType), "operatingSystemVersion": .object([
                    "major": .int(Int64(os.majorVersion)), "minor": .int(Int64(os.minorVersion)), "patch": .int(Int64(os.patchVersion))])])])
    }
}

#if DEBUG
extension VO2MaxNormalizer {
    /// No quantities, UUIDs, source descriptors or metadata values leave this
    /// protected inspection. Native numeric checks are evaluated in memory.
    static func blockedNativeShape(_ archive: Data) throws -> JSON {
        guard let objects = try NSKeyedUnarchiver.unarchivedObject(
            ofClasses: [NSArray.self, HKSample.self, HKDeletedObject.self], from: archive) as? [NSObject] else {
            throw SyncFailure.localState("vo2_blocked_archive_invalid")
        }
        var shapes: [String: Int] = [:], checks: [String: Int] = [:]
        for sample in objects.compactMap({ $0 as? HKSample }) {
            for (key, value) in sample.metadata ?? [:] {
                let field = key.hasPrefix("HK") && key.utf8.count <= 128 ? key : "unrecognized_field"
                let carrier: String
                if let number = value as? NSNumber {
                    if CFGetTypeID(number) == CFBooleanGetTypeID() {
                        carrier = "boolean"; checks["boolean", default: 0] += 1
                    } else {
                        let type = String(cString: number.objCType)
                        let cf = CFGetTypeID(number) == CFNumberGetTypeID()
                            ? String(CFNumberGetType(unsafeBitCast(number, to: CFNumber.self)).rawValue) : "not_CFNumber"
                        carrier = "NSNumber/objCType:" + type + "/CFNumberType:" + cf
                        let x = number.doubleValue
                        checks[x.isFinite ? "finite" : "nonfinite", default: 0] += 1
                        if x.isFinite {
                            checks[x.rounded(.towardZero) == x ? "integral" : "fractional", default: 0] += 1
                            checks[abs(x) < 9_007_199_254_740_992 ? "inside_safe_double_integer_magnitude" : "outside_safe_double_integer_magnitude", default: 0] += 1
                        }
                    }
                } else if value is String { carrier = "string" }
                else { carrier = "other" }
                shapes[field + "|" + carrier, default: 0] += 1
            }
        }
        return .object(["sampleCount": .int(Int64(objects.filter { $0 is HKSample }.count)),
            "deletedCount": .int(Int64(objects.filter { $0 is HKDeletedObject }.count)),
            "fieldCarrierCounts": .object(shapes.mapValues { .int(Int64($0)) }),
            "numericCheckCounts": .object(checks.mapValues { .int(Int64($0)) })])
    }
}
#endif
