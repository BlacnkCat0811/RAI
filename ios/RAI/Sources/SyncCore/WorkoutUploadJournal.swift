import Foundation

public struct PreparedWorkoutUpload: Codable, Sendable, Equatable {
    public let workoutID: String
    public let scope: Scope
    public let boundary: String
    public let hash: String
    public let file: String
    public var identity: String { "workout-ingest:" + workoutID + ":" + hash }
    public var receipt: JSON?
}

/// Only prepared uploads are journaled. A selected history is never eagerly
/// materialized: at most the bounded workers build files. The exact multipart
/// bytes and identity survive suspension, cancellation and process restarts.
public actor WorkoutUploadJournal {
    public nonisolated let directory: URL
    private var entries: [String: PreparedWorkoutUpload]?
    public init(directory: URL) { self.directory = directory }
    private var index: URL { directory.appendingPathComponent("workout-uploads.json") }
    private func load() throws -> [String: PreparedWorkoutUpload] {
        if let entries { return entries }
        let value = FileManager.default.fileExists(atPath: index.path)
            ? try JSONDecoder().decode([String: PreparedWorkoutUpload].self, from: Data(contentsOf: index)) : [:]
        guard value.allSatisfy({ key, entry in
            WorkoutIngestStatus.validID(key) && key == entry.workoutID && entry.hash.count == 64 &&
                entry.hash.allSatisfy { "0123456789abcdef".contains($0) } && entry.boundary.utf8.count <= 70 && entry.file == entry.hash + ".workout-upload"
        }) else {
            throw SyncFailure.localState("workout_upload_journal_corrupt")
        }
        entries = value
        return value
    }
    private func commit(_ value: [String: PreparedWorkoutUpload]) throws {
        try OutboxStore.write(JSONEncoder().encode(value), to: index)
        entries = value
    }
    public func all(scope: Scope) throws -> [PreparedWorkoutUpload] {
        let values = try load().values.filter { $0.scope == scope }.sorted { $0.workoutID < $1.workoutID }
        // Replay reclamation if the process died after committing a receipt.
        for entry in values where entry.receipt != nil { reclaim(entry) }
        return values
    }
    private func reclaim(_ entry: PreparedWorkoutUpload) {
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(entry.file))
    }
    public func pending(_ id: String, scope: Scope) throws -> PreparedWorkoutUpload? {
        guard let entry = try load()[id], entry.receipt == nil else { return nil }
        guard entry.scope == scope else { throw SyncFailure.localState("workout_upload_scope_mismatch") }
        try verify(entry)
        return entry
    }
    public func verify(_ entry: PreparedWorkoutUpload) throws {
        guard entry.file == entry.hash + ".workout-upload",
              Wire.hash(try Data(contentsOf: directory.appendingPathComponent(entry.file))) == entry.hash else {
            throw SyncFailure.localState("workout_upload_payload_corrupt")
        }
    }
    public func prepare(_ raw: Data, workoutID: String, scope: Scope) throws -> PreparedWorkoutUpload {
        guard WorkoutIngestStatus.validID(workoutID) else { throw SyncFailure.invalid("invalid_workout_id") }
        if let entry = try pending(workoutID, scope: scope) { return entry }
        // Multipart boundaries are limited to 70 characters (RFC 2046).
        let boundary = "rai." + Wire.hash(raw)
        let body: Data
        if let wire = try? WorkoutPayloadTransport.gzipWire(raw: raw, boundary: boundary) { body = wire.wire }
        else {
            guard raw.count <= Wire.GzipBundle.maxDecodedBytes else { throw SyncFailure.localState("bundle_too_large") }
            body = WorkoutPayloadTransport.multipart(bundle: raw, boundary: boundary)
        }
        let hash = Wire.hash(body)
        let entry = PreparedWorkoutUpload(workoutID: workoutID, scope: scope, boundary: boundary,
                                          hash: hash, file: hash + ".workout-upload")
        try OutboxStore.write(body, to: directory.appendingPathComponent(entry.file))
        var next = try load(); next[workoutID] = entry; try commit(next)
        return entry
    }
    public func complete(_ entry: PreparedWorkoutUpload, receipt: JSON) throws {
        _ = try WorkoutIngestStatus(receipt: receipt, workoutID: entry.workoutID)
        var next = try load()
        guard next[entry.workoutID]?.identity == entry.identity else { throw SyncFailure.localState("workout_upload_identity_mismatch") }
        var accepted = entry; accepted.receipt = receipt
        next[entry.workoutID] = accepted
        // Commit the validated receipt before reclaiming either the body or the
        // transfer result. A death at either point is safe to replay locally.
        try commit(next)
        reclaim(entry)
    }
}
