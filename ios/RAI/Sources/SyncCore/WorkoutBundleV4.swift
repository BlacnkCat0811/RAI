import Foundation

/// The protocol-4 raw wire encoder. One Workout is one immutable bundle: the
/// HealthKit workout object, its metadata, one object per sample with its own
/// source and metadata, and the route points. The client serializes what it
/// observed in canonical units; the server decides which fields it understands
/// and preserves the exact bytes either way.
public enum WorkoutBundleV4 {
    public static let maxSamples = 50_000
    public static let maxSeriesPoints = 1_000_000
    public static let maxRoutePoints = 1_000_000
    public static let maxRoutes = 16

    public static func data(
        scope: Scope,
        workoutID: String,
        startUs: Int64,
        endUs: Int64,
        source: JSON,
        metadata: JSON?,
        workout: JSON,
        streams: [JSON],
        series: [JSON],
        routes: [JSON]
    ) throws -> Data {
        var fields: [String: JSON] = [
            "protocolVersion": .int(4),
            "datasetID": .string(scope.datasetID),
            "datasetEpoch": .string(scope.datasetEpoch),
            "collectorEpoch": .string(scope.collectorEpoch),
            "workoutID": .string(workoutID),
            "startUs": .int(startUs),
            "endUs": .int(endUs),
            "source": source,
            "workout": workout,
            "streams": .array(streams),
            "series": .array(series),
            "routes": .array(routes),
        ]
        if let metadata, !metadata.fields.isEmpty { fields["metadata"] = metadata }
        let raw = try JSON.object(fields).data()
        guard raw.count <= 32 * 1_024 * 1_024 else { throw SyncFailure.invalid("bundle_too_large") }
        return raw
    }

    public static func sample(uuid: String, startUs: Int64, endUs: Int64, value: Double,
                              source: JSON, metadata: JSON?) -> JSON {
        var fields: [String: JSON] = [
            "uuid": .string(uuid), "startUs": .int(startUs), "endUs": .int(endUs),
            "value": .number(value), "source": source,
        ]
        if let metadata, !metadata.fields.isEmpty { fields["metadata"] = metadata }
        return .object(fields)
    }

    public static func stream(type: String, unit: String, status: String, error: String?,
                              samples: [JSON]) -> JSON {
        .object([
            "type": .string(type), "unit": .string(unit), "status": .string(status),
            "error": error.map(JSON.string) ?? .null, "samples": .array(samples),
        ])
    }

    public static func series(type: String, sampleUUID: String, unit: String,
                              points: [(startUs: Int64, endUs: Int64, value: Double)]) -> JSON {
        .object([
            "type": .string(type), "sampleUUID": .string(sampleUUID), "unit": .string(unit),
            "points": .array(points.map { .array([.int($0.startUs), .int($0.endUs), .number($0.value)]) }),
        ])
    }

    public static func route(routeID: String, source: JSON, points: [JSON]) -> JSON {
        .object(["routeID": .string(routeID), "source": source, "points": .array(points)])
    }

    /// v4 routes are eight-element arrays with nullable quality components.
    public static func routePoint(_ point: RoutePoint) throws -> JSON {
        func optional(_ value: Double?) throws -> JSON {
            guard let value else { return .null }
            guard value.isFinite else { throw SyncFailure.invalid("nonfinite_route_measurement") }
            return .number(value)
        }
        guard point.latitude.isFinite, point.longitude.isFinite else {
            throw SyncFailure.invalid("nonfinite_route_coordinate")
        }
        return .array([
            .int(point.recordedAtUs), .number(point.latitude), .number(point.longitude),
            try optional(point.altitude), try optional(point.horizontalAccuracy), try optional(point.verticalAccuracy),
            try optional(point.speed), try optional(point.course),
        ])
    }
}
