import Foundation

// Used for the frozen quantity-series and oversized Workout protocols. Only a
// bounded chunk is held in memory; each gzip file becomes immutable on disk.
public struct DetailSpool: Sendable {
    private let directory: URL, limit: Int
    private var lines: [Data] = []
    private var bytes = 0
    public private(set) var chunks: [RouteChunkFile] = []
    public private(set) var count: Int64 = 0
    private var manifest = Data(repeating: 0, count: 32)
    public init(directory: URL, limit: Int = 1000) { self.directory = directory; self.limit = limit }
    public mutating func append(_ canonical: Data) throws {
        guard canonical.count <= 65_536, count < 1_000_000 else { throw SyncFailure.invalid("detail_limit_exceeded") }
        if lines.count == limit || bytes + canonical.count + 1 > 1_048_576 { try flush() }
        lines.append(canonical); bytes += canonical.count + 1; count += 1
    }
    public mutating func flush() throws {
        guard !lines.isEmpty else { return }
        guard chunks.count < 1000 else { throw SyncFailure.invalid("too_many_detail_chunks") }
        var canonical = Data([91]), ndjson = Data()
        for (index, line) in lines.enumerated() { if index > 0 { canonical.append(44) }; canonical += line; ndjson += line; ndjson.append(10) }
        canonical.append(93)
        let digest = Wire.digest(canonical), wire = try Wire.gzip(ndjson)
        guard wire.count <= 2_097_152 else { throw SyncFailure.invalid("detail_chunk_too_large") }
        manifest = Wire.chain(manifest, digest)
        let name = Wire.id() + ".detail.gz"; try OutboxStore.write(wire, to: directory.appendingPathComponent(name))
        chunks.append(.init(file: name, pointCount: lines.count, payloadHash: Wire.hash(wire), pointsHash: Wire.hex(digest), manifest: Wire.hex(manifest), payloadManifest: ""))
        lines.removeAll(keepingCapacity: true); bytes = 0
    }
    public func uploadOperations(path: String, mediaType: String, idKey: String, id: String, digestKey: String) -> [OutboxOperation] {
        chunks.enumerated().map { index, chunk in .request(.init(method: "PUT", path: path + "/chunks/" + String(index), file: chunk.file,
            mediaType: mediaType, expected: [idKey: .string(id), "chunkIndex": .int(Int64(index)), "payloadSHA256": .string(chunk.payloadHash),
            digestKey: .string(chunk.pointsHash), "manifestSHA256": .string(chunk.manifest), "committedChunks": .int(Int64(index + 1))], gzip: true)) }
    }
}

public enum Supplements {
    public static func canonicalDetail(_ kind: String, _ value: JSON) throws -> Data {
        let order: [String]
        switch kind {
        case "quantity": order = ["value", "unit"]
        case "statistic": order = ["type", "startUs", "endUs", "sum", "average", "minimum", "maximum"]
        case "event": order = ["type", "startUs", "endUs", "metadata"]
        case "configuration": order = ["activityType", "locationType", "swimmingLocationType", "lapLength"]
        case "activity": order = ["uuid", "startUs", "endUs", "durationUs", "configuration", "metadata", "statistics", "events"]
        default: throw SyncFailure.invalid("unknown_detail_kind")
        }
        // Nested objects need Go's declared field order, whereas metadata maps
        // use sorted keys. Compose bytes directly rather than re-encoding JSON.
        var fields: [String] = []
        for key in order where value[key] != .null {
            let v = value[key], data: Data
            switch key {
            case "sum", "average", "minimum", "maximum", "lapLength": data = try canonicalDetail("quantity", v)
            case "configuration": data = try canonicalDetail("configuration", v)
            case "statistics", "events":
                if v.list.isEmpty { continue }
                let children = try v.list.map { try canonicalDetail(key == "statistics" ? "statistic" : "event", $0) }
                data = Data([91]) + children.enumerated().reduce(into: Data()) { result, item in if item.offset > 0 { result.append(44) }; result += item.element } + Data([93])
            default: data = try v.data()
            }
            fields.append("\"" + key + "\":" + String(decoding: data, as: UTF8.self))
        }
        return Data(("{" + fields.joined(separator: ",") + "}").utf8)
    }
    public static func workoutPackage(event: JSON, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] {
        let id = event["uuid"].text!, path = "/v2/sync/workout-packages/" + id
        let workout = event["workout"]
        var request = scope.fields; request.removeValue(forKey: "protocolVersion")
        var core = workout.fields
        for key in ["activities", "statistics", "events"] { core.removeValue(forKey: key) }
        request.merge(["workoutID": .string(id), "startUs": event["startUs"], "endUs": event["endUs"], "source": event["source"], "workout": .object(core)]) { _, b in b }
        if event["metadata"] != .null { request["metadata"] = event["metadata"] }
        let registration = try JSON.object(request).data()
        guard registration.count <= 16_384 else { throw SyncFailure.invalid("workout_core_too_large") }
        var archive = DetailSpool(directory: store.directory)
        var detailBudget = 0
        for (key, kind) in [("activities", "activity"), ("statistics", "statistic"), ("events", "event")] {
            for (ordinal, value) in workout[key].list.enumerated() {
                detailBudget += 1 + (kind == "activity" ? value["statistics"].list.count + value["events"].list.count : 0)
                guard detailBudget <= 20_000 else { throw SyncFailure.invalid("workout_package_too_large") }
                let detail = try canonicalDetail(kind, value)
                let line = Data(("{\"kind\":\"" + kind + "\",\"ordinal\":" + String(ordinal) + ",\"" + kind + "\":").utf8) + detail + Data([125])
                try archive.append(line)
            }
        }
        try archive.flush()
        guard let last = archive.chunks.last, archive.chunks.count <= 100 else { throw SyncFailure.invalid("workout_package_invalid_chunks") }
        let file = try await store.savePayload(registration)
        var operations: [OutboxOperation] = [.request(.init(method: "POST", path: "/v2/sync/workout-packages", file: file, expected: ["workoutID": .string(id)]))]
        operations += archive.uploadOperations(path: path, mediaType: "application/x-rai-health-workout-detail+ndjson", idKey: "workoutID", id: id, digestKey: "detailsSHA256")
        let seal: JSON = .object(["chunkCount": .int(Int64(archive.chunks.count)), "totalDetails": .int(Int64(detailBudget)), "manifestSHA256": .string(last.manifest)])
        let sealFile = try await store.savePayload(seal.data())
        operations.append(.request(.init(method: "POST", path: path + "/seal", file: sealFile, expected: ["workoutID": .string(id), "ingestStatus": .string("sealed"), "manifestSHA256": .string(last.manifest)])))
        return operations
    }
}
