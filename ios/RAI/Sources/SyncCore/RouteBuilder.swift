import Foundation
import Synchronization

public struct RoutePoint: Sendable {
    public let recordedAtUs: Int64
    public let latitude: Double, longitude: Double
    public var altitude: Double?, horizontalAccuracy: Double?, verticalAccuracy: Double?, speed: Double?, course: Double?
    public init(recordedAtUs: Int64, latitude: Double, longitude: Double, altitude: Double? = nil, horizontalAccuracy: Double? = nil,
                verticalAccuracy: Double? = nil, speed: Double? = nil, course: Double? = nil) {
        self.recordedAtUs = recordedAtUs; self.latitude = latitude; self.longitude = longitude; self.altitude = altitude
        self.horizontalAccuracy = horizontalAccuracy; self.verticalAccuracy = verticalAccuracy; self.speed = speed; self.course = course
    }
    public func canonical(sequence: Int64) throws -> Data {
        guard sequence >= 0, (-62135596800000000...253402300799999999).contains(recordedAtUs), (-90...90).contains(latitude), (-180...180).contains(longitude) else { throw SyncFailure.invalid("invalid_route_coordinate") }
        var fields: [(String, JSON)] = [("sequence", .int(sequence)), ("recordedAtUs", .int(recordedAtUs)), ("latitude", .number(latitude)), ("longitude", .number(longitude))]
        for (key, value) in [("altitude", altitude), ("horizontalAccuracy", horizontalAccuracy), ("verticalAccuracy", verticalAccuracy), ("speed", speed), ("course", course)] {
            if let value {
                guard key == "altitude" || (value >= 0 && (key != "course" || value < 360)) else { throw SyncFailure.invalid("invalid_route_measurement") }
                fields.append((key, .number(value)))
            }
        }
        return try Wire.object(fields)
    }
}

public struct RouteChunkFile: Sendable {
    public let file: String, pointCount: Int, payloadHash: String, pointsHash: String, manifest: String, payloadManifest: String
}
public struct PreparedRoute: Sendable {
    public let chunks: [RouteChunkFile]
    public let pointCount: Int64, first: Int64, last: Int64
    public let points: [RoutePoint]
    public func operations(scope: Scope, routeID: String, workout: WorkoutReference, source: String, store: OutboxStore) async throws -> [OutboxOperation] {
        guard let end = chunks.last else { throw SyncFailure.invalid("empty_route") }
        var manifest = scope.fields
        manifest.merge(["routeID": .string(routeID), "workoutID": .string(workout.id), "source": .object(["bundleID": .string(source)]),
                        "expectedChunkCount": .int(Int64(chunks.count)), "expectedPointCount": .int(pointCount), "firstRecordedAtUs": .int(first),
                        "lastRecordedAtUs": .int(last), "expectedPayloadManifestSHA256": .string(end.payloadManifest)]) { _, b in b }
        let register = try await store.savePayload(JSON.object(manifest).data())
        var operations: [OutboxOperation] = [.request(.init(method: "POST", path: "/v2/routes", file: register,
            expected: ["routeID": .string(routeID), "workoutID": .string(workout.id), "expectedChunkCount": .int(Int64(chunks.count)), "expectedPointCount": .int(pointCount)]))]
        var total: Int64 = 0
        for (index, chunk) in chunks.enumerated() {
            total += Int64(chunk.pointCount)
            operations.append(.request(.init(method: "PUT", path: "/v2/routes/" + routeID + "/chunks/" + String(index), file: chunk.file,
                mediaType: "application/x-rai-health-route+ndjson", expected: ["routeID": .string(routeID), "chunkIndex": .int(Int64(index)),
                "payloadSHA256": .string(chunk.payloadHash), "chunkSHA256": .string(chunk.pointsHash), "manifestSHA256": .string(chunk.manifest),
                "payloadManifestSHA256": .string(chunk.payloadManifest), "committedChunks": .int(Int64(index + 1)), "totalPoints": .int(total)], gzip: true)))
        }
        let seal: [String: JSON] = ["protocolVersion": .int(2), "chunkCount": .int(Int64(chunks.count)), "totalPoints": .int(pointCount),
                                  "manifestSHA256": .string(end.manifest), "payloadManifestSHA256": .string(end.payloadManifest)]
        let file = try await store.savePayload(JSON.object(seal).data())
        var expected = seal; expected.removeValue(forKey: "protocolVersion")
        expected["routeID"] = .string(routeID); expected["workoutID"] = .string(workout.id); expected["ingestStatus"] = .string("sealed")
        operations.append(.request(.init(method: "POST", path: "/v2/routes/" + routeID + "/seal", file: file, expected: expected)))
        return operations
    }
}

// HealthKit may deliver callbacks on different queues. Mutex makes the bounded
// spool Sendable without weakening concurrency checks. Coordinates only enter
// protected app files, never logs or evidence.
public final class RouteSpool: Sendable {
    private struct State {
        var lines: [Data] = []
        var bytes = 0
        var count: Int64 = 0
        var first: Int64?
        var last: Int64?
        var chunks: [RouteChunkFile] = []
        var points: [RoutePoint] = []
        var manifest = Data(repeating: 0, count: 32)
        var payloadManifest = Data(repeating: 0, count: 32)
        var finished = false
    }
    private let state = Mutex(State())
    private let directory: URL
    private let chunkSize: Int
    public init(directory: URL, chunkSize: Int = 1000) throws {
        guard (1...5000).contains(chunkSize) else { throw SyncFailure.invalid("invalid_route_chunk_size") }
        self.directory = directory; self.chunkSize = chunkSize
    }
    public func append(_ points: [RoutePoint]) throws {
        try state.withLock { state in
            guard !state.finished else { throw SyncFailure.invalid("route_already_finished") }
            for point in points {
                guard state.count < 1_000_000 else { throw SyncFailure.invalid("route_point_limit") }
                let line = try point.canonical(sequence: state.count)
                if !state.lines.isEmpty && (state.lines.count == chunkSize || state.bytes + line.count + 1 > 1_048_576) { try flush(&state) }
                state.lines.append(line); state.bytes += line.count + 1
                state.points.append(point)
                state.first = min(state.first ?? point.recordedAtUs, point.recordedAtUs); state.last = max(state.last ?? point.recordedAtUs, point.recordedAtUs); state.count += 1
            }
        }
    }
    public func finish() throws -> PreparedRoute {
        try state.withLock { state in
            guard !state.finished, let first = state.first, let last = state.last else { throw SyncFailure.invalid("empty_or_finished_route") }
            try flush(&state); state.finished = true
            return PreparedRoute(chunks: state.chunks, pointCount: state.count, first: first, last: last, points: state.points)
        }
    }
    private func flush(_ state: inout State) throws {
        guard !state.lines.isEmpty else { return }
        guard state.chunks.count < 1000 else { throw SyncFailure.invalid("route_too_large") }
        var canonical = Data([91]), ndjson = Data()
        for (i, line) in state.lines.enumerated() {
            if i > 0 { canonical.append(44) }; canonical += line; ndjson += line; ndjson.append(10)
        }
        canonical.append(93)
        let wire = try Wire.gzip(ndjson), digest = Wire.digest(canonical), wireDigest = Wire.digest(wire)
        guard wire.count <= 2_097_152 else { throw SyncFailure.invalid("route_chunk_too_large") }
        let name = Wire.id() + ".route.gz"
        try OutboxStore.write(wire, to: directory.appendingPathComponent(name))
        state.manifest = Wire.chain(state.manifest, digest); state.payloadManifest = Wire.chain(state.payloadManifest, wireDigest)
        state.chunks.append(RouteChunkFile(file: name, pointCount: state.lines.count, payloadHash: Wire.hex(wireDigest), pointsHash: Wire.hex(digest),
                                          manifest: Wire.hex(state.manifest), payloadManifest: Wire.hex(state.payloadManifest)))
        state.lines.removeAll(keepingCapacity: true); state.bytes = 0
    }
}
