import Foundation
import HealthKit
import SyncCore

/// Builds one protocol-6 payload from one complete HealthKit read: one query
/// per metric type, a top-level source inherited by every fact, scalar triples,
/// series groups with a parent header, and routes without IDs. There is no
/// per-sample UUID, metadata or source, no Outbox staging and no retry bytes.
struct RawStreamSyncEngine: Sendable {
    let health: any HealthKitReading
    let scope: Scope

    /// Types whose HealthKit quantity samples carry multiple values. A series
    /// query is the only read for these; every other type uses one sample
    /// query. Running speed stays scalar: Apple Health exposes it as separate
    /// observations, not as a multi-value sample.
    static let seriesTypes: Set<String> = [
        HKQuantityTypeIdentifier.runningPower.rawValue,
        HKQuantityTypeIdentifier.runningGroundContactTime.rawValue,
        HKQuantityTypeIdentifier.runningStrideLength.rawValue,
        HKQuantityTypeIdentifier.runningVerticalOscillation.rawValue,
    ]

    private struct StreamRead: Sendable {
        let type: String
        let unit: String
        var samples: [WorkoutSampleV6] = []
        var series: [WorkoutSeriesGroupV6] = []
        var status = "complete"
        var error: String?

        mutating func incomplete(_ code: String) {
            if status == "complete" { status = "partial"; error = code }
        }
    }

    func build(id: String) async throws -> Data {
        guard let workout = try await health.workout(id) else {
            throw SyncFailure.invalid("workout_not_visible")
        }
        let source = Normalizer.source(workout)
        let classification = HealthSourcePolicy.classification(source)
        let outcome = WorkoutSourcePolicy.outcome(classification)
        guard outcome == WorkoutSourcePolicy.eligible else {
            throw SyncFailure.invalid(outcome == WorkoutSourcePolicy.excluded ? "source_policy_excluded" : "source_policy_pending")
        }
        let startUs = Normalizer.us(workout.startDate), endUs = Normalizer.us(workout.endDate)
        guard startUs <= endUs else { throw SyncFailure.invalid("invalid_time_range") }
        let streamTypes = HealthKitClient.quantityTypes

        var streams: [StreamRead] = []
        try await withThrowingTaskGroup(of: StreamRead.self) { group in
            for type in streamTypes {
                group.addTask { try await self.readStream(type: type, workout: workout) }
            }
            for try await stream in group { streams.append(stream) }
        }
        // The task group completes in completion order. Sort by identifier so
        // bundle bytes never depend on scheduling.
        streams.sort { $0.type < $1.type }

        var entries = 0, seriesPoints = 0
        for stream in streams {
            entries += stream.samples.count + stream.series.count
            seriesPoints += stream.series.reduce(0) { $0 + $1.points.count }
        }
        guard entries <= WorkoutBundleV6.maxSamples, seriesPoints <= WorkoutBundleV6.maxSeriesPoints else {
            throw SyncFailure.invalid("bundle_too_large")
        }

        // A route read that was started must finish. Empty routes are legal only
        // when HealthKit reports none (indoor run, no route attached).
        let read = try await health.bundleRoutes(workout: workout)
        guard read.count <= WorkoutBundleV6.maxRoutes else { throw SyncFailure.invalid("bundle_too_large") }
        var routePoints = 0
        var routes: [[RoutePoint]] = []
        for route in read.sorted(by: { $0.id < $1.id }) {
            if let coverage = route.coverage,
               coverage.queryCompleted == false || coverage.errorCode != nil {
                throw SyncFailure.invalid(coverage.errorCode ?? "route_read_incomplete")
            }
            routePoints += route.points.count
            guard routePoints <= WorkoutBundleV6.maxRoutePoints else {
                throw SyncFailure.invalid("bundle_too_large")
            }
            guard !route.points.isEmpty else { continue }
            routes.append(route.points)
        }

        // v6 clients send no metadata; the optional top-level field exists for
        // other producers and server-side diagnostics.
        return try WorkoutBundleV6.data(
            scope: scope, workoutID: id, startUs: startUs, endUs: endUs,
            source: source, metadata: nil,
            workout: Normalizer.rawWorkout(workout),
            streams: streams.map { stream in
                WorkoutStreamV6(type: stream.type, unit: stream.unit, status: stream.status,
                                error: stream.error, samples: stream.samples, series: stream.series)
            },
            routes: routes)
    }

    private func readStream(type: HKQuantityType, workout: HKWorkout) async throws -> StreamRead {
        let unit = Normalizer.units[type.identifier] ?? ""
        var stream = StreamRead(type: type.identifier, unit: unit)
        if Self.seriesTypes.contains(type.identifier) {
            let groups = try await health.bundleSeriesGroups(type: type, workout: workout)
            for group in groups {
                var points: [WorkoutSampleV6] = []
                points.reserveCapacity(group.points.count)
                for point in group.points {
                    guard point.value.isFinite, point.startUs <= point.endUs,
                          point.startUs >= group.startUs, point.endUs <= group.endUs else {
                        stream.incomplete("invalid_series_point")
                        continue
                    }
                    points.append(WorkoutSampleV6(startUs: point.startUs, endUs: point.endUs, value: point.value))
                }
                guard !points.isEmpty else { continue }
                guard group.value.isFinite else { stream.incomplete("invalid_series_parent"); continue }
                stream.series.append(WorkoutSeriesGroupV6(startUs: group.startUs, endUs: group.endUs,
                                                          value: group.value, points: points))
            }
            return stream
        }

        let samples = try await health.bundleSamples(type: type, workout: workout)
        stream.samples.reserveCapacity(samples.count)
        for sample in samples {
            try Task.checkCancellation()
            do {
                guard let quantitySample = sample as? HKQuantitySample else {
                    throw SyncFailure.invalid("unsupported_sample")
                }
                guard quantitySample.count <= 1 else {
                    // A multi-value sample in a scalar stream means this type
                    // needs a series read this build does not perform. Mark it
                    // instead of uploading only the aggregate as if it were
                    // the complete stream.
                    throw SyncFailure.invalid("series_type_not_configured")
                }
                let quantity = try Normalizer.quantity(quantitySample.quantity, type: type.identifier)
                guard let value = Self.number(quantity["value"]) else {
                    throw SyncFailure.invalid("nonfinite_quantity")
                }
                let start = Normalizer.us(quantitySample.startDate)
                let end = Normalizer.us(quantitySample.endDate)
                guard start <= end else { throw SyncFailure.invalid("invalid_sample_range") }
                stream.samples.append(WorkoutSampleV6(startUs: start, endUs: end, value: value))
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                // The server skips what it cannot use; a sample this build
                // cannot serialize is skipped here so the stream stays
                // uploadable, and the stream status records the loss.
                stream.incomplete(Self.code(error))
            }
        }
        return stream
    }

    private static func number(_ value: JSON?) -> Double? {
        switch value {
        case .number(let number) where number.isFinite: return number
        case .int(let integer): return Double(integer)
        default: return nil
        }
    }

    private static func code(_ error: Error) -> String {
        if let failure = error as? SyncFailure {
            switch failure {
            case .invalid(let code), .localState(let code): return code
            case .receiptMismatch: return "receipt_mismatch"
            }
        }
        return error is CancellationError ? "cancelled" : "healthkit_read_failed"
    }
}
