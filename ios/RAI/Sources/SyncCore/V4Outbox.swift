import Foundation

/// Per-Workout identity sidecar for protocol v4. It proves which sample and
/// route identities the last successfully staged bundle contained, so a later
/// sync can emit D2 deletions only after a complete read of the same stream.
public actor V4Outbox {
    public struct Index: Codable, Sendable, Equatable {
        public var datasetID: String
        public var datasetEpoch: String
        public var collectorEpoch: String
        public var samples: [String]
        public var routes: [String]

        public init(datasetID: String, datasetEpoch: String, collectorEpoch: String,
                    samples: [String], routes: [String]) {
            self.datasetID = datasetID
            self.datasetEpoch = datasetEpoch
            self.collectorEpoch = collectorEpoch
            self.samples = samples
            self.routes = routes
        }
    }

    private let directory: URL

    public init(directory: URL) {
        self.directory = directory
    }

    public func load(id: String, scope: Scope) -> Index? {
        guard let url = try? fileURL(id), let data = try? Data(contentsOf: url),
              let index = try? JSONDecoder().decode(Index.self, from: data) else { return nil }
        guard index.datasetID == scope.datasetID,
              index.datasetEpoch == scope.datasetEpoch,
              index.collectorEpoch == scope.collectorEpoch else { return nil }
        return index
    }

    public func record(_ index: Index, id: String) throws {
        let data = try JSONEncoder().encode(index)
        try OutboxStore.write(data, to: fileURL(id))
    }

    /// Stage the next index under the immutable bundle hash before upload. If
    /// the process dies after the server commits but before the ACK is
    /// persisted, the same bundle can still be promoted on the next attempt.
    public func savePending(_ index: Index, bundleSHA256: String) throws {
        let data = try JSONEncoder().encode(index)
        try OutboxStore.write(data, to: pendingURL(bundleSHA256))
    }

    /// Promote a staged index only after the bundle receipt is durable. A
    /// missing staging file (for example a bundle prepared by an older build)
    /// is a no-op, never an implicit empty index that could authorize deletes.
    @discardableResult
    public func promotePending(bundleSHA256: String, id: String, scope: Scope) throws -> Bool {
        let url = try pendingURL(bundleSHA256)
        guard let data = try? Data(contentsOf: url),
              let index = try? JSONDecoder().decode(Index.self, from: data) else { return false }
        guard index.datasetID == scope.datasetID,
              index.datasetEpoch == scope.datasetEpoch,
              index.collectorEpoch == scope.collectorEpoch else {
            throw SyncFailure.localState("invalid_v4_index_scope")
        }
        try record(index, id: id)
        try? FileManager.default.removeItem(at: url)
        return true
    }

    private func fileURL(_ id: String) throws -> URL {
        guard UUID(uuidString: id) != nil else { throw SyncFailure.localState("invalid_v4_index_id") }
        return directory.appendingPathComponent("v4-index-" + id.lowercased() + ".json")
    }

    private func pendingURL(_ hash: String) throws -> URL {
        let lower = hash.lowercased()
        guard lower.count == 64, lower.allSatisfy({ $0.isHexDigit }) else {
            throw SyncFailure.localState("invalid_v4_bundle_hash")
        }
        return directory.appendingPathComponent("v4-pending-" + lower + ".json")
    }
}
