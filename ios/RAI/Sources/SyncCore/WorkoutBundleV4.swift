import Foundation

/// The protocol-4 wire encoder. One Workout is one immutable bundle: the
/// workout object, a source table and columnar sample streams. No per-sample
/// objects, no pages, no 200-day proof block.
public enum WorkoutBundleV4 {
    public static let maxSamples = 50_000
    public static let maxSeriesPoints = 1_000_000
    public static let maxRoutePoints = 1_000_000
    public static let maxRoutes = 16
    public static let maxSources = 16
    public static let maxDeletions = 10_000

    /// Deduplicated source table. `sources[]` deliberately omits device detail;
    /// the workout provenance in `observation` keeps it.
    public struct SourceTable: Sendable {
        public private(set) var sources: [JSON] = []
        public init() {}
        public mutating func index(of source: JSON) throws -> Int {
            let normalized = Self.normalize(source)
            if let existing = sources.firstIndex(of: normalized) { return existing }
            guard sources.count < maxSources else { throw SyncFailure.invalid("too_many_sources") }
            sources.append(normalized)
            return sources.count - 1
        }
        private static func normalize(_ source: JSON) -> JSON {
            var fields: [String: JSON] = [:]
            for key in ["bundleID", "name", "version", "productType", "operatingSystemVersion"] {
                if let text = source[key].text, !text.isEmpty { fields[key] = .string(text) }
            }
            return .object(fields)
        }
    }

    public static func data(
        scope: Scope,
        workoutID: String,
        startUs: Int64,
        endUs: Int64,
        workout: JSON,
        sources: [JSON],
        streams: [JSON],
        series: [JSON],
        routes: [JSON],
        deletions: [JSON],
        routeDeletions: [String],
        observation: JSON
    ) throws -> Data {
        var fields: [String: JSON] = [
            "protocolVersion": .int(4),
            "metadataRegistryVersion": .int(WorkoutMetadataRegistry.version),
            "datasetID": .string(scope.datasetID),
            "datasetEpoch": .string(scope.datasetEpoch),
            "collectorEpoch": .string(scope.collectorEpoch),
            "workoutID": .string(workoutID),
            "startUs": .int(startUs),
            "endUs": .int(endUs),
            "workout": workout,
            "sources": .array(sources),
            "streams": .array(streams),
            "series": .array(series),
            "routes": .array(routes),
            "deletions": .array(deletions),
            "observation": observation,
        ]
        if !routeDeletions.isEmpty { fields["routeDeletions"] = .array(routeDeletions.map(JSON.string)) }
        let bundle = JSON.object(fields)
        let raw = try bundle.data()
        guard raw.count <= 32 * 1_024 * 1_024 else { throw SyncFailure.invalid("bundle_too_large") }
        return raw
    }

    /// v4 routes are eight-element arrays with nullable quality components.
    public static func routePoint(_ point: RoutePoint) throws -> JSON {
        func optional(_ value: Double?) -> JSON { value.map(JSON.number) ?? .null }
        return .array([
            .int(point.recordedAtUs), .number(point.latitude), .number(point.longitude),
            optional(point.altitude), optional(point.horizontalAccuracy), optional(point.verticalAccuracy),
            optional(point.speed), optional(point.course),
        ])
    }
}
