import Foundation
import HealthKit
import SyncCore

/// One Workout's prepared v4 payload. The bytes are immutable so a retry
/// uploads the same file instead of re-reading HealthKit.
struct PreparedV4Bundle: Sendable {
    let raw: Data
    let nextAnchor: Data?
}

/// Builds the protocol-4 raw payload: one complete read per quantity type and
/// one object per sample with its own source and metadata. There is no
/// coverage, deletion or completeness machinery; a failed read only marks that
/// stream as failed in the payload and never touches another stream.
struct V4SyncEngine: Sendable {
    let store: OutboxStore
    let health: any HealthKitReading
    let scope: Scope

    private struct StreamRead: Sendable {
        let type: String
        let unit: String
        var samples: [JSON] = []
        var series: [JSON] = []
        var seriesPoints = 0
        var status = "complete"
        var error: String?
    }

    func build(id: String, anchor: Data?) async throws -> PreparedV4Bundle {
        // The anchor is only persisted so a later sync starts from the same
        // place; a deletion event is not a server command in protocol 4.
        let change = try await health.readWorkout(id: id, anchor: anchor)
        guard let workout = try await health.workout(id) else {
            throw SyncFailure.invalid("workout_not_visible")
        }
        let source = Normalizer.source(workout)
        let classification = HealthSourcePolicy.classification(source)
        let outcome = WorkoutSourcePolicy.outcome(classification)
        try await store.recordSourceClassification(workoutID: id, classification: classification,
                                                   evidence: WorkoutSourcePolicy.evidence(source))
        // A Workout whose source is not Apple Watch stays out of the queue.
        // Child sample sources are the server's decision, not this gate.
        guard outcome == WorkoutSourcePolicy.eligible else {
            throw SyncFailure.invalid(outcome == WorkoutSourcePolicy.excluded ? "source_policy_excluded" : "source_policy_pending")
        }

        var streams: [StreamRead] = []
        try await withThrowingTaskGroup(of: StreamRead.self) { group in
            for type in HealthKitClient.quantityTypes {
                group.addTask { try await self.readStream(type: type, workout: workout) }
            }
            for try await stream in group { streams.append(stream) }
        }
        // The task group completes in completion order. Sort by identifier so
        // bundle bytes never depend on scheduling.
        streams.sort { $0.type < $1.type }

        var streamJSON: [JSON] = []
        var seriesJSON: [JSON] = []
        var sampleCount = 0, seriesPointCount = 0
        for stream in streams {
            sampleCount += stream.samples.count
            seriesPointCount += stream.seriesPoints
            guard sampleCount <= WorkoutBundleV4.maxSamples,
                  seriesPointCount <= WorkoutBundleV4.maxSeriesPoints else {
                throw SyncFailure.invalid("bundle_too_large")
            }
            streamJSON.append(WorkoutBundleV4.stream(type: stream.type, unit: stream.unit,
                                                     status: stream.status, error: stream.error,
                                                     samples: stream.samples))
            seriesJSON.append(contentsOf: stream.series)
        }

        var routes: [HealthRouteBundle] = []
        do {
            routes = try await health.bundleRoutes(workout: workout)
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            // Routes are optional: a failed route read does not block the run.
            routes = []
        }
        guard routes.count <= WorkoutBundleV4.maxRoutes else { throw SyncFailure.invalid("bundle_too_large") }
        var routesJSON: [JSON] = []
        var routePointCount = 0
        for route in routes {
            routePointCount += route.points.count
            guard routePointCount <= WorkoutBundleV4.maxRoutePoints else { throw SyncFailure.invalid("bundle_too_large") }
            var points: [JSON] = []
            points.reserveCapacity(route.points.count)
            for point in route.points {
                if let encoded = try? WorkoutBundleV4.routePoint(point) { points.append(encoded) }
            }
            guard !points.isEmpty else { continue }
            routesJSON.append(WorkoutBundleV4.route(routeID: route.id,
                                                    source: route.provenance ?? .object(["bundleID": .string(route.source)]),
                                                    points: points))
        }

        let raw = try WorkoutBundleV4.data(scope: scope, workoutID: id,
                                           startUs: Normalizer.us(workout.startDate),
                                           endUs: Normalizer.us(workout.endDate),
                                           source: source,
                                           metadata: Normalizer.rawMetadata(workout.metadata),
                                           workout: Normalizer.rawWorkout(workout),
                                           streams: streamJSON, series: seriesJSON, routes: routesJSON)
        return PreparedV4Bundle(raw: raw, nextAnchor: change.anchor)
    }

    private func readStream(type: HKQuantityType, workout: HKWorkout) async throws -> StreamRead {
        let unit = Normalizer.units[type.identifier] ?? ""
        var stream = StreamRead(type: type.identifier, unit: unit)
        let samples: [HKSample]
        do {
            samples = try await health.bundleSamples(type: type, workout: workout)
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            stream.status = "failed"
            stream.error = Self.code(error)
            return stream
        }
        for sample in samples {
            try Task.checkCancellation()
            do {
                guard let quantitySample = sample as? HKQuantitySample else {
                    throw SyncFailure.invalid("unsupported_sample")
                }
                let quantity = try Normalizer.quantity(quantitySample.quantity, type: type.identifier)
                guard let value = Self.value(quantity["value"]) else {
                    throw SyncFailure.invalid("unsupported_sample")
                }
                stream.samples.append(WorkoutBundleV4.sample(
                    uuid: quantitySample.uuid.uuidString.lowercased(),
                    startUs: Normalizer.us(quantitySample.startDate),
                    endUs: Normalizer.us(quantitySample.endDate),
                    value: value,
                    source: Normalizer.source(quantitySample),
                    metadata: Normalizer.rawMetadata(quantitySample.metadata)))
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                // The server skips what it cannot use; a sample this build
                // cannot serialize is skipped here so the stream stays
                // uploadable and no other sample is lost.
                stream.status = "partial"
                stream.error = Self.code(error)
            }
        }
        do {
            let bySample = try await health.bundleSeries(type: type, workout: workout)
            for uuid in stream.samples.compactMap({ $0["uuid"].text }) {
                guard let points = bySample[uuid], !points.isEmpty else { continue }
                var triples: [(startUs: Int64, endUs: Int64, value: Double)] = []
                triples.reserveCapacity(points.count)
                for point in points {
                    guard let start = point["startUs"].integer, let end = point["endUs"].integer,
                          let value = Self.value(point["value"]) else { continue }
                    triples.append((start, end, value))
                }
                guard !triples.isEmpty else { continue }
                stream.series.append(WorkoutBundleV4.series(type: type.identifier, sampleUUID: uuid,
                                                            unit: unit, points: triples))
                stream.seriesPoints += triples.count
            }
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            if stream.status == "complete" {
                stream.status = "partial"
                stream.error = Self.code(error)
            }
        }
        return stream
    }

    private static func value(_ value: JSON?) -> Double? {
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
