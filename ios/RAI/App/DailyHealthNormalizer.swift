import Foundation
import CoreFoundation
import HealthKit
import SyncCore

enum DailyHealthNormalizer {
    static func categoryValue(_ value: Int) throws -> Int {
        let supported: Set<Int> = [HKCategoryValueSleepAnalysis.inBed.rawValue, HKCategoryValueSleepAnalysis.asleepUnspecified.rawValue,
            HKCategoryValueSleepAnalysis.awake.rawValue, HKCategoryValueSleepAnalysis.asleepCore.rawValue,
            HKCategoryValueSleepAnalysis.asleepDeep.rawValue, HKCategoryValueSleepAnalysis.asleepREM.rawValue]
        guard supported.contains(value) else { throw SyncFailure.invalid("invalid_category") }
        return value
    }
    static let integerKeys = [HKMetadataKeyAlgorithmVersion, HKMetadataKeySyncVersion, HKMetadataKeyHeartRateMotionContext,
                              HKMetadataKeyHeartRateSensorLocation, HKMetadataKeyUserMotionContext, HKMetadataKeyDevicePlacementSide]
    static func fact(_ sample: HKSample) throws -> JSON {
        let type = sample.sampleType.identifier
        guard DailyHealth.types.contains(type) else { throw SyncFailure.invalid("unsupported_type") }
        var fact: [String: JSON] = ["uuid": .string(sample.uuid.uuidString.lowercased()), "startUs": .int(Normalizer.us(sample.startDate)),
                                    "endUs": .int(Normalizer.us(sample.endDate)), "source": Normalizer.source(sample)]
        if let quantity = sample as? HKQuantitySample, type != DailyHealth.sleep {
            guard quantity.count == 1 else { throw SyncFailure.invalid("daily_series_requires_version") }
            fact["quantity"] = try Normalizer.quantity(quantity.quantity, type: type)
        } else if let category = sample as? HKCategorySample, type == DailyHealth.sleep {
            // Read symbols from the current SDK, retaining raw values on wire.
            fact["category"] = .int(Int64(try categoryValue(category.value)))
        } else { throw SyncFailure.invalid("invalid_payload") }
        var metadata: [String: JSON] = [:]
        for (key, value) in sample.metadata ?? [:] {
            if integerKeys.contains(key) {
                guard let number = value as? NSNumber, CFGetTypeID(number) != CFBooleanGetTypeID(), number.doubleValue.isFinite else { throw SyncFailure.invalid("unsupported_metadata") }
                // Preserve Int64 metadata beyond Double's exact integer range.
                guard let integer = Int64(number.stringValue) ?? Int64(exactly: number.doubleValue) else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .int(integer)
            } else if key == HKMetadataKeyTimeZone {
                guard let text = value as? String, !text.isEmpty, TimeZone(identifier: text) != nil else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .string(text)
            } else if key == HKMetadataKeyWasUserEntered {
                guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .bool(number.boolValue)
            } else if key == HKMetadataKeySyncIdentifier {
                guard let text = value as? String, !text.isEmpty else { throw SyncFailure.invalid("unsupported_metadata") }
                metadata[key] = .string(text)
            } else { throw SyncFailure.invalid("unsupported_metadata") }
        }
        guard (metadata[HKMetadataKeySyncIdentifier] == nil) == (metadata[HKMetadataKeySyncVersion] == nil) else { throw SyncFailure.invalid("unsupported_metadata") }
        if !metadata.isEmpty { fact["metadata"] = .object(metadata) }
        return .object(fact)
    }
}
