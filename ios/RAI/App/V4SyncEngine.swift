import Foundation
import HealthKit
import SyncCore

/// One Workout's v4 build result. The coordinator stages these exact bytes;
/// retries never re-read HealthKit or re-encode a prepared bundle.
struct PreparedV4Bundle: Sendable {
    let raw: Data
    let coverage: [String: WorkoutCoverage]
    let sampleDeletions: Int
    let routeDeletions: Int
    let nextAnchor: Data?
    let index: V4Outbox.Index
}

/// Builds protocol v4 columnar bundles from the three complete HealthKit reads
/// (`bundleSamples`, `bundleSeries`, `bundleRoutes`). It deliberately has no
/// page/capture/anchor machinery: the stream is the unit of read coverage and
/// a failed stream only makes that stream partial.
struct V4SyncEngine: Sendable {
    let store: OutboxStore
    let health: any HealthKitReading
    let scope: Scope
    let outbox: V4Outbox

    enum BuildResult: Sendable {
        case deleted
        case prepared(PreparedV4Bundle)
    }

    private struct BuiltStream: Sendable {
        let type: String
        let kind: String
        let unit: String
        var starts: [Int64] = []
        var ends: [Int64] = []
        var values: [JSON] = []
        var uuids: [String] = []
        var keys: [String] = []
        var sources: [JSON] = []
        var metadata: [JSON] = []
        var series: [[JSON]] = []
        var coverage: [String: WorkoutCoverage] = [:]
        var readComplete = false
        var readError: String?
    }

    func build(id: String, anchor: Data?) async throws -> BuildResult {
        let change = try await health.readWorkout(id: id, anchor: anchor)
        if change.deleted.contains(where: { $0.uuid.uuidString.lowercased() == id }) {
            return .deleted
        }
        guard let workout = try await health.workout(id) else {
            throw SyncFailure.invalid("workout_not_visible")
        }
        let source = Normalizer.source(workout)
        let classification = HealthSourcePolicy.classification(source)
        let outcome = WorkoutSourcePolicy.outcome(classification)
        try await store.recordSourceClassification(workoutID: id, classification: classification,
                                                   evidence: WorkoutSourcePolicy.evidence(source))
        guard outcome == WorkoutSourcePolicy.eligible else {
            throw SyncFailure.invalid(outcome == WorkoutSourcePolicy.excluded ? "source_policy_excluded" : "source_policy_pending")
        }
        let observedWorkout = try Normalizer.integrityEvent(workout)
        guard case .object(let workoutObject) = observedWorkout["workout"] else {
            throw SyncFailure.invalid("missing_workout")
        }

        var coverage: [String: WorkoutCoverage] = [:]
        coverage["workout"] = .result(count: 1)

        var streams: [BuiltStream] = []
        try await withThrowingTaskGroup(of: BuiltStream.self) { group in
            for type in HealthKitClient.quantityTypes {
                group.addTask { try await self.readStream(type: type, workout: workout, parentSource: source) }
            }
            for try await stream in group { streams.append(stream) }
        }
        // The task group completes in completion order. Sort by identifier so
        // bundle bytes never depend on scheduling.
        streams.sort { $0.type < $1.type }

        var table = WorkoutBundleV4.SourceTable()
        let workoutSourceIndex = try table.index(of: source)
        var streamJSON: [JSON] = []
        var seriesJSON: [JSON] = []
        var totalSamples = 0
        var totalSeriesPoints = 0
        for (index, stream) in streams.enumerated() {
            totalSamples += stream.uuids.count
            guard totalSamples <= WorkoutBundleV4.maxSamples else { throw SyncFailure.invalid("bundle_too_large") }
            var src: [Int] = []
            for sampleSource in stream.sources { src.append(try table.index(of: sampleSource)) }
            var fields: [String: JSON] = [
                "type": .string(stream.type),
                "kind": .string(stream.kind),
                "unit": .string(stream.unit),
                "samples": .array(zip(zip(stream.starts, stream.ends), stream.values).map { pair, value in
                    .array([.int(pair.0), .int(pair.1), value])
                }),
                "uuids": .array(stream.uuids.map(JSON.string)),
                "source": .int(Int64(workoutSourceIndex)),
                "src": .array(src.map { .int(Int64($0)) }),
            ]
            if stream.metadata.contains(where: { $0.fields.isEmpty == false }) {
                fields["metadata"] = .array(stream.metadata)
            }
            streamJSON.append(.object(fields))
            for (sampleIndex, points) in stream.series.enumerated() where !points.isEmpty {
                totalSeriesPoints += points.count
                guard totalSeriesPoints <= WorkoutBundleV4.maxSeriesPoints else { throw SyncFailure.invalid("bundle_too_large") }
                var triples: [JSON] = []
                triples.reserveCapacity(points.count)
                for point in points {
                    guard let start = point["startUs"].integer, let end = point["endUs"].integer,
                          let unit = point["unit"].text, unit == stream.unit,
                          let value = Self.value(point["value"]) else {
                        throw SyncFailure.invalid("quantity_series_incomplete")
                    }
                    triples.append(.array([.int(start), .int(end), value]))
                }
                seriesJSON.append(.object(["stream": .int(Int64(index)), "sample": .int(Int64(sampleIndex)), "points": .array(triples)]))
            }
            for (key, value) in stream.coverage { coverage[key] = value }
        }

        var routesJSON: [JSON] = []
        var routePointCount = 0
        var routeReadSucceeded = false
        var routeDeletionComplete = false
        do {
            let routes = try await health.bundleRoutes(workout: workout)
            routeReadSucceeded = true
            // A route list is deletion authority only when every route has a
            // complete, query-completed coverage record. bundleRoutes returns
            // partial results instead of throwing when one route query fails;
            // treating those omissions as deletions would lose health data.
            let routeCoverage = routes.compactMap(\.coverage)
            let completeRoutes = routes.isEmpty ? true : (routeCoverage.count == routes.count &&
                routeCoverage.allSatisfy { $0.state == .complete && $0.queryCompleted == true && $0.errorCode == nil })
            routeDeletionComplete = completeRoutes
            if routes.isEmpty {
                coverage["route"] = .result(count: 0)
            } else {
                for route in routes {
                    let sourceJSON = route.provenance ?? .object(["bundleID": .string(route.source)])
                    let sourceIndex = try table.index(of: sourceJSON)
                    routePointCount += route.points.count
                    guard routePointCount <= WorkoutBundleV4.maxRoutePoints else { throw SyncFailure.invalid("bundle_too_large") }
                    routesJSON.append(.object([
                        "routeID": .string(route.id),
                        "source": .int(Int64(sourceIndex)),
                        "points": .array(try route.points.map(WorkoutBundleV4.routePoint)),
                    ]))
                }
                if completeRoutes {
                    coverage["route"] = .result(count: routePointCount)
                } else {
                    var value = WorkoutCoverage.result(count: routePointCount, completed: false)
                    value.errorCode = routeCoverage.first(where: { $0.errorCode != nil })?.errorCode ?? "route_partial"
                    value.diagnostics = routeCoverage.flatMap { $0.diagnostics ?? [] }
                    coverage["route"] = value
                }
            }
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            coverage["route"] = WorkoutCoverage(state: .failed, sampleCount: 0, errorCode: Self.code(error))
        }

        // D2: deletions are a sidecar diff, never an inference from a failed
        // read. The previous index is scoped to this dataset/collector. A first
        // sync has no index and therefore never emits a deletion.
        let previous = await outbox.load(id: id, scope: scope)
        let completeTypes = Set(streams.filter(\.readComplete).map(\.type))
        let currentKeys = Set(streams.flatMap { $0.readComplete ? $0.keys : [] })
        var deletedSampleKeys: [String] = []
        if let previous {
            deletedSampleKeys = previous.samples.filter { key in
                guard let colon = key.firstIndex(of: ":") else { return false }
                let type = String(key[..<colon])
                return completeTypes.contains(type) && !currentKeys.contains(key)
            }
        }
        var deletionsJSON: [JSON] = []
        for key in deletedSampleKeys.sorted() {
            guard let colon = key.firstIndex(of: ":") else { continue }
            let type = String(key[..<colon]), uuid = String(key[key.index(after: colon)...])
            guard UUID(uuidString: uuid) != nil else { continue }
            deletionsJSON.append(.object(["type": .string(type), "uuid": .string(uuid)]))
        }
        guard deletionsJSON.count <= WorkoutBundleV4.maxDeletions else {
            throw SyncFailure.invalid("too_many_deletions")
        }
        var routeDeletions: [String] = []
        if routeDeletionComplete, let previous {
            let currentRoutes = Set(routesJSON.compactMap { $0["routeID"].text })
            routeDeletions = previous.routes.filter { !currentRoutes.contains($0) }.sorted()
        }
        let nextIndex = V4Outbox.Index(datasetID: scope.datasetID, datasetEpoch: scope.datasetEpoch,
                                       collectorEpoch: scope.collectorEpoch,
                                       samples: {
                                           let oldSamples: [String] = previous?.samples ?? []
                                           let previousByType = Dictionary(grouping: oldSamples, by: { key in
                                               String(key[..<(key.firstIndex(of: ":") ?? key.endIndex)])
                                           })
                                           // A failed stream keeps its old identities so a later
                                           // successful read can still prove a real deletion.
                                           var merged = currentKeys
                                           for type in Set(streams.filter { !$0.readComplete }.map(\.type)) {
                                               merged.formUnion(previousByType[type] ?? [])
                                           }
                                           return merged.sorted()
                                       }(),
                                       routes: routeReadSucceeded ? routesJSON.compactMap { $0["routeID"].text } : (previous?.routes ?? []))
        for key in coverage.keys { coverage[key]?.localPersisted = true }
        let observation: JSON = .object([
            "version": .int(1), "observedAtUs": .int(Normalizer.us(Date())),
            "metadata": observedWorkout["metadata"] == .null ? .object([:]) : observedWorkout["metadata"],
            "provenance": .object(["workout": source, "samples": .array([]), "routes": .array([])]),
            "coverage": .object(coverage.mapValues(\.wire)),
        ])
        let raw = try WorkoutBundleV4.data(scope: scope, workoutID: id,
                                           startUs: Normalizer.us(workout.startDate), endUs: Normalizer.us(workout.endDate),
                                           workout: .object(workoutObject), sources: table.sources,
                                           streams: streamJSON, series: seriesJSON, routes: routesJSON,
                                           deletions: deletionsJSON, routeDeletions: routeDeletions,
                                           observation: observation)
        guard raw.count <= 32 * 1_024 * 1_024 else { throw SyncFailure.invalid("bundle_too_large") }
        return .prepared(PreparedV4Bundle(raw: raw, coverage: coverage, sampleDeletions: deletionsJSON.count,
                                          routeDeletions: routeDeletions.count, nextAnchor: change.anchor, index: nextIndex))
    }

    private func readStream(type: HKQuantityType, workout: HKWorkout, parentSource: JSON) async throws -> BuiltStream {
        let unit = Normalizer.units[type.identifier] ?? ""
        var stream = BuiltStream(type: type.identifier, kind: unit.isEmpty ? "category" : "quantity", unit: unit)
        let samples: [HKSample]
        do {
            samples = try await health.bundleSamples(type: type, workout: workout)
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            stream.readError = Self.code(error)
            stream.coverage[type.identifier] = WorkoutCoverage(state: .failed, sampleCount: 0, errorCode: stream.readError)
            return stream
        }
        var skipped = 0
        for sample in samples {
            guard let quantitySample = sample as? HKQuantitySample else {
                skipped += 1
                continue
            }
            let sampleType = quantitySample.quantityType.identifier
            guard sampleType == type.identifier else { skipped += 1; continue }
            // Record every identity the complete read returned, including a
            // source-excluded or metadata-rejected sample. Otherwise a later
            // complete read would mistake it for a deletion.
            let uuid = quantitySample.uuid.uuidString.lowercased()
            stream.keys.append(type.identifier + ":" + uuid)
            let sampleSource = Normalizer.source(quantitySample)
            let eligibility = HealthSourcePolicy.classification(sampleSource, associatedWorkout: parentSource)
            guard ["apple_watch", "associated_apple_watch"].contains(eligibility) else {
                skipped += 1
                continue
            }
            do {
                let event = try Normalizer.integrityEvent(quantitySample)
                guard let start = event["startUs"].integer, let end = event["endUs"].integer,
                      let value = Self.value(event["quantity"]["value"]) else {
                    throw SyncFailure.invalid("unsupported_sample")
                }
                stream.starts.append(start); stream.ends.append(end); stream.values.append(value)
                stream.uuids.append(uuid)
                stream.sources.append(sampleSource)
                stream.metadata.append(event["metadata"] == .null ? .object([:]) : event["metadata"])
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                // A single unencodable sample makes this stream partial; it
                // must never discard the other streams or fabricate deletions.
                skipped += 1
                stream.readError = Self.code(error)
            }
        }
        // Deletion authority requires a complete, fully encodable read. A
        // skipped sample or an unencodable sample makes the stream partial and
        // keeps its previous identities in the sidecar, so a later short read
        // can never delete it.
        stream.readComplete = skipped == 0 && stream.readError == nil
        var typeCoverage = WorkoutCoverage.result(count: stream.uuids.count)
        if skipped > 0 {
            typeCoverage.state = .partial
            typeCoverage.errorCode = stream.readError
            typeCoverage.diagnostics = ["source_policy_pending"]
        }
        stream.coverage[type.identifier] = typeCoverage
        var seriesBySample: [String: [JSON]] = [:]
        do {
            seriesBySample = try await health.bundleSeries(type: type, workout: workout)
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            stream.coverage["series:" + type.identifier] = WorkoutCoverage(state: .failed, sampleCount: 0, errorCode: Self.code(error))
            return stream
        }
        var seriesCount = 0
        for uuid in stream.uuids {
            let points = seriesBySample[uuid] ?? []
            seriesCount += points.count
            stream.series.append(points)
        }
        stream.coverage["series:" + type.identifier] = .result(count: seriesCount)
        return stream
    }

    private static func value(_ value: JSON?) -> JSON? {
        switch value {
        case .number(let number): return number.isFinite ? .number(number) : nil
        case .int(let integer): return .int(integer)
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
