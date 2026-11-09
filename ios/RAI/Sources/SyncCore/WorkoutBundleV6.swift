import Foundation

/// One scalar observation in a protocol-6 stream.
public struct WorkoutSampleV6: Sendable, Equatable {
    public let startUs: Int64
    public let endUs: Int64
    public let value: Double
    public init(startUs: Int64, endUs: Int64, value: Double) {
        self.startUs = startUs; self.endUs = endUs; self.value = value
    }
}

/// One series parent and its points. The parent header carries the sample's
/// own start, end and aggregate value; the points are the series observations.
public struct WorkoutSeriesGroupV6: Sendable, Equatable {
    public let startUs: Int64
    public let endUs: Int64
    public let value: Double
    public let points: [WorkoutSampleV6]
    public init(startUs: Int64, endUs: Int64, value: Double, points: [WorkoutSampleV6]) {
        self.startUs = startUs; self.endUs = endUs; self.value = value; self.points = points
    }
}

/// One metric stream. A scalar type fills `samples`; a series type fills
/// `series`. No per-sample UUID, metadata or source is ever encoded.
public struct WorkoutStreamV6: Sendable, Equatable {
    public let type: String
    public let unit: String
    public let status: String
    public let error: String?
    public let samples: [WorkoutSampleV6]
    public let series: [WorkoutSeriesGroupV6]
    public init(type: String, unit: String, status: String, error: String?,
                samples: [WorkoutSampleV6], series: [WorkoutSeriesGroupV6]) {
        self.type = type; self.unit = unit; self.status = status; self.error = error
        self.samples = samples; self.series = series
    }
}

/// The protocol-6 raw wire encoder. One Workout is one immutable bundle with a
/// top-level source, per-type scalar triples and series groups relative to the
/// Workout start, and routes without IDs. The server owns every fact identity.
public enum WorkoutBundleV6 {
    public static let maxSamples = 50_000
    public static let maxSeriesPoints = 1_000_000
    public static let maxRoutePoints = 1_000_000
    public static let maxRoutes = 16
    public static let maxBytes = 32 * 1_024 * 1_024

    public static func data(
        scope: Scope,
        workoutID: String,
        startUs: Int64,
        endUs: Int64,
        source: JSON,
        metadata: JSON?,
        workout: JSON,
        streams: [WorkoutStreamV6],
        routes: [[RoutePoint]]
    ) throws -> Data {
        guard startUs <= endUs else { throw SyncFailure.invalid("invalid_time_range") }
        var entries = 0, seriesPoints = 0, routePoints = 0
        var streamJSON: [JSON] = []
        streamJSON.reserveCapacity(streams.count)
        for stream in streams.sorted(by: { $0.type < $1.type }) {
            entries += stream.samples.count + stream.series.count
            guard entries <= maxSamples else { throw SyncFailure.invalid("bundle_too_large") }
            let samples = try stream.samples.sorted(by: Self.order).map { try triple($0, base: startUs) }
            let groups = try stream.series.sorted(by: Self.order).map { group -> JSON in
                seriesPoints += group.points.count
                guard seriesPoints <= maxSeriesPoints else { throw SyncFailure.invalid("bundle_too_large") }
                return try seriesGroup(group, base: startUs)
            }
            streamJSON.append(.object([
                "type": .string(stream.type), "unit": .string(stream.unit),
                "status": .string(stream.status), "error": stream.error.map(JSON.string) ?? .null,
                "samples": .array(samples), "series": .array(groups),
            ]))
        }
        guard routes.count <= maxRoutes else { throw SyncFailure.invalid("bundle_too_large") }
        var routesJSON: [JSON] = []
        routesJSON.reserveCapacity(routes.count)
        for route in routes {
            routePoints += route.count
            guard routePoints <= maxRoutePoints else { throw SyncFailure.invalid("bundle_too_large") }
            var points: [JSON] = []
            points.reserveCapacity(route.count)
            for point in route.sorted(by: Self.routeOrder) { points.append(try routePoint(point, base: startUs)) }
            guard !points.isEmpty else { continue }
            routesJSON.append(.object(["points": .array(points)]))
        }
        var fields: [String: JSON] = [
            "protocolVersion": .int(6),
            "datasetID": .string(scope.datasetID),
            "datasetEpoch": .string(scope.datasetEpoch),
            "collectorEpoch": .string(scope.collectorEpoch),
            "workoutID": .string(workoutID),
            "startUs": .int(startUs),
            "endUs": .int(endUs),
            "source": source,
            "workout": workout,
            "streams": .array(streamJSON),
            "routes": .array(routesJSON),
        ]
        if let metadata, !metadata.fields.isEmpty { fields["metadata"] = metadata }
        let raw = try JSON.object(fields).data()
        guard raw.count <= maxBytes else { throw SyncFailure.invalid("bundle_too_large") }
        return raw
    }

    public static func sample(_ sample: WorkoutSampleV6, base: Int64) throws -> JSON {
        try triple(sample, base: base)
    }

    public static func seriesGroup(_ group: WorkoutSeriesGroupV6, base: Int64) throws -> JSON {
        guard !group.points.isEmpty else { throw SyncFailure.invalid("invalid_series_group") }
        let points = try group.points.sorted(by: order).map { point -> JSON in
            guard point.startUs >= group.startUs, point.endUs <= group.endUs else {
                throw SyncFailure.invalid("invalid_series_point")
            }
            return try triple(point, base: base)
        }
        return .object([
            "startDeltaUs": .int(try delta(group.startUs, base: base)),
            "endDeltaUs": .int(try delta(group.endUs, base: base)),
            "value": .number(try finite(group.value)),
            "points": .array(points),
        ])
    }

    /// Protocol-6 route points are three to eight element tuples. Trailing
    /// unavailable measurements are omitted instead of written as null.
    public static func routePoint(_ point: RoutePoint, base: Int64) throws -> JSON {
        guard point.latitude.isFinite, point.longitude.isFinite else {
            throw SyncFailure.invalid("nonfinite_route_coordinate")
        }
        var items: [JSON] = [.int(try delta(point.recordedAtUs, base: base)),
                             .number(point.latitude), .number(point.longitude)]
        let optional: [Double?] = [point.altitude, point.horizontalAccuracy, point.verticalAccuracy,
                                   point.speed, point.course]
        var last = -1
        for index in optional.indices where optional[index] != nil { last = index }
        if last >= 0 {
            for index in 0...last {
                guard let value = optional[index] else { items.append(.null); continue }
                guard value.isFinite else { throw SyncFailure.invalid("nonfinite_route_measurement") }
                items.append(.number(value))
            }
        }
        return .array(items)
    }

    static func delta(_ timestampUs: Int64, base: Int64) throws -> Int64 {
        let (value, overflow) = timestampUs.subtractingReportingOverflow(base)
        guard !overflow else { throw SyncFailure.invalid("invalid_time_range") }
        return value
    }

    private static func triple(_ sample: WorkoutSampleV6, base: Int64) throws -> JSON {
        guard sample.startUs <= sample.endUs else { throw SyncFailure.invalid("invalid_sample_range") }
        return .array([.int(try delta(sample.startUs, base: base)),
                       .int(try delta(sample.endUs, base: base)),
                       .number(try finite(sample.value))])
    }

    private static func finite(_ value: Double) throws -> Double {
        guard value.isFinite else { throw SyncFailure.invalid("nonfinite_quantity") }
        return value
    }

    private static func order(_ lhs: WorkoutSampleV6, _ rhs: WorkoutSampleV6) -> Bool {
        if lhs.startUs != rhs.startUs { return lhs.startUs < rhs.startUs }
        if lhs.endUs != rhs.endUs { return lhs.endUs < rhs.endUs }
        return lhs.value < rhs.value
    }

    private static func order(_ lhs: WorkoutSeriesGroupV6, _ rhs: WorkoutSeriesGroupV6) -> Bool {
        if lhs.startUs != rhs.startUs { return lhs.startUs < rhs.startUs }
        if lhs.endUs != rhs.endUs { return lhs.endUs < rhs.endUs }
        return lhs.value < rhs.value
    }

    /// Route order is part of the immutable bytes. Time is primary; the
    /// remaining measurements only break ties so equal timestamps are stable.
    private static func routeOrder(_ lhs: RoutePoint, _ rhs: RoutePoint) -> Bool {
        if lhs.recordedAtUs != rhs.recordedAtUs { return lhs.recordedAtUs < rhs.recordedAtUs }
        if lhs.latitude != rhs.latitude { return lhs.latitude < rhs.latitude }
        if lhs.longitude != rhs.longitude { return lhs.longitude < rhs.longitude }
        let optional: [(Double?, Double?)] = [
            (lhs.altitude, rhs.altitude),
            (lhs.horizontalAccuracy, rhs.horizontalAccuracy),
            (lhs.verticalAccuracy, rhs.verticalAccuracy),
            (lhs.speed, rhs.speed),
            (lhs.course, rhs.course),
        ]
        for (left, right) in optional {
            switch (left, right) {
            case (nil, nil): continue
            case (nil, _): return true
            case (_, nil): return false
            case let (left?, right?):
                if left != right { return left < right }
            }
        }
        return false
    }
}
