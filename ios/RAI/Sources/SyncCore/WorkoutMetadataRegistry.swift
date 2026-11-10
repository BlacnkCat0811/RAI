import Foundation

/// Versioned, scope-separated Workout metadata contract.
///
/// Version 1 is the original nine-key HealthKit allowlist. Version 2 adds two
/// real WorkoutActivity metadata keys that Apple does not publish in the
/// iPhoneOS SDK; their meaning is undocumented, so they are preserved exactly
/// (native carrier type and value) and are never interpreted, aggregated,
/// re-derived or used in any model computation.
///
/// Adding a key here is a semantic registry expansion and bumps `version`.
/// It is never described as a parser bugfix. A key that is unknown, or known
/// but presented at the wrong scope, stops the upload; nothing is dropped,
/// stringified or coerced.
public enum WorkoutMetadataRegistry {
    public static let version: Int64 = 2
    public static let maxKeysPerObject = 64
    public static let textMaxBytes = 64
    public static let timeZoneMaxBytes = 128

    public enum Scope: String, CaseIterable, Sendable {
        case workout, activity, activityEvent, workoutEvent, route, observation
    }

    public enum Kind: Equatable, Sendable {
        case text(maxBytes: Int)
        case boolean
        case quantity(unit: String)
        case signedInteger
    }

    public struct Entry: Equatable, Sendable {
        public let key: String
        public let kind: Kind
        public let scopes: Set<Scope>
        /// Registry version that introduced the key. Anything above 1 is an
        /// opaque-preserved key: exact carrier, no interpretation.
        public let introducedIn: Int64
    }

    /// Workout-level, Activity-level and event-level metadata share the
    /// version 1 HealthKit key set. Route metadata is narrower.
    private static let healthKitScopes: Set<Scope> = [.workout, .activity, .activityEvent, .workoutEvent]
    private static let routeScopes: Set<Scope> = [.route]

    public static let entries: [Entry] = [
        Entry(key: "HKTimeZone", kind: .text(maxBytes: timeZoneMaxBytes), scopes: healthKitScopes.union(routeScopes), introducedIn: 1),
        Entry(key: "HKIndoorWorkout", kind: .boolean, scopes: healthKitScopes.union(routeScopes), introducedIn: 1),
        Entry(key: "HKAverageMETs", kind: .quantity(unit: "kcal/(kg*hr)"), scopes: healthKitScopes, introducedIn: 1),
        Entry(key: "HKElevationAscended", kind: .quantity(unit: "m"), scopes: healthKitScopes, introducedIn: 1),
        Entry(key: "HKElevationDescended", kind: .quantity(unit: "m"), scopes: healthKitScopes, introducedIn: 1),
        Entry(key: "HKWeatherHumidity", kind: .quantity(unit: "1"), scopes: healthKitScopes, introducedIn: 1),
        Entry(key: "HKWeatherTemperature", kind: .quantity(unit: "degC"), scopes: healthKitScopes, introducedIn: 1),
        Entry(key: "HKAverageSpeed", kind: .quantity(unit: "m/s"), scopes: healthKitScopes, introducedIn: 1),
        Entry(key: "HKMaximumSpeed", kind: .quantity(unit: "m/s"), scopes: healthKitScopes, introducedIn: 1),
        // Version 2. Observed only on WorkoutActivity metadata of one real
        // Workout; absent from the iPhoneOS 26.5 SDK and from HealthKit's own
        // string tables, so the semantic meaning stays unverified. Preserved
        // exactly, never used for anything else, and accepted at Activity
        // scope only.
        Entry(key: "WOIntervalStepKeyPath", kind: .text(maxBytes: textMaxBytes), scopes: [.activity], introducedIn: 2),
        Entry(key: "WOIntervalStepSuccessful", kind: .signedInteger, scopes: [.activity], introducedIn: 2),
    ]

    public static func entry(_ key: String, scope: Scope) -> Entry? {
        entries.first { $0.key == key && $0.scopes.contains(scope) }
    }


    /// Exact text preservation: no trimming, no case folding, no Unicode
    /// normalization. Only bounded, well-formed, control-free content passes.
    public static func validateText(_ text: String, maxBytes: Int) throws {
        guard !text.isEmpty, text.utf8.count <= maxBytes else { throw SyncFailure.invalid("unsupported_workout_metadata_type") }
        for scalar in text.unicodeScalars where scalar.value < 0x20 || scalar.value == 0x7f {
            throw SyncFailure.invalid("unsupported_workout_metadata_type")
        }
    }

    /// The wire form of each kind is fixed, so the same semantic value always
    /// produces the same canonical bytes regardless of input dictionary order.
    public static func json(_ entry: Entry, text: String) -> JSON { .string(text) }
}
