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
