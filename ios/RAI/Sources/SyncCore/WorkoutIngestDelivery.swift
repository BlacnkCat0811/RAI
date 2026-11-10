import Foundation

/// One server answer for a Workout UUID. The order is the server's own status
/// priority (`deleted` > accepted/committed/rejected > already_imported); a
/// later local answer may only move forward, never rewind.
public enum WorkoutIngestState: String, Codable, Sendable, CaseIterable {
    case accepted, committed, rejected, deleted
    case alreadyImported = "already_imported"
    public var rank: Int {
        switch self {
        case .accepted: return 1
        case .committed, .alreadyImported: return 2
        case .rejected: return 3
        case .deleted: return 4
        }
    }
}

/// Validated `GET /v2/sync/workout-ingests/{uuid}` answer. `analysisJobIDs`
/// may be absent for `not_required` because the server omits an empty list.
public struct WorkoutIngestStatus: Sendable, Equatable {
    public let workoutID: String
    public let state: WorkoutIngestState
    public let acceptedAt: Date?
    public let commitRevision: Int64?
    public let analysisStatus: String?
    public let analysisJobIDs: [String]
    public let errorCode: String?
    public let raw: JSON

    public init(_ value: JSON, workoutID: String? = nil) throws {
        guard let id = value["workoutID"].text, Self.validID(id) else { throw SyncFailure.receiptMismatch }
        if let workoutID, id != workoutID { throw SyncFailure.receiptMismatch }
        guard let rawState = value["ingestStatus"].text, let state = WorkoutIngestState(rawValue: rawState) else {
            throw SyncFailure.receiptMismatch
        }
        let acceptedAt = Self.date(value["acceptedAt"])
        let jobs = value["analysisJobIDs"].list.compactMap(\.text)
        guard jobs.count == value["analysisJobIDs"].list.count else { throw SyncFailure.receiptMismatch }
        switch state {
        case .accepted:
            guard acceptedAt != nil else { throw SyncFailure.receiptMismatch }
        case .committed:
            guard let revision = value["commitRevision"].integer, revision > 0,
                  let analysis = value["analysisStatus"].text, ["queued", "not_required"].contains(analysis) else {
                throw SyncFailure.receiptMismatch
            }
            guard jobs.allSatisfy(Self.validID), analysis != "queued" || !jobs.isEmpty else { throw SyncFailure.receiptMismatch }
        case .rejected:
            guard !(value["errorCode"].text ?? "").isEmpty else { throw SyncFailure.receiptMismatch }
        case .deleted, .alreadyImported:
            break
        }
        self.workoutID = id; self.state = state; self.acceptedAt = acceptedAt
        commitRevision = value["commitRevision"].integer
        analysisStatus = value["analysisStatus"].text
        analysisJobIDs = jobs
        errorCode = value["errorCode"].text
        raw = value
    }

    /// Only a durable first acceptance (or its replay) confirms this upload.
    public init(receipt: JSON, workoutID: String) throws {
        guard Self.validID(workoutID), let id = receipt["workoutID"].text, Self.validID(id), id == workoutID,
              let rawState = receipt["ingestStatus"].text, let state = WorkoutIngestState(rawValue: rawState),
              state == .accepted || state == .alreadyImported else { throw SyncFailure.receiptMismatch }
        guard state != .alreadyImported else { throw SyncFailure.localState("workout_ingest_missing_receipt") }
        let acceptedAt = Self.date(receipt["acceptedAt"])
        guard acceptedAt != nil else { throw SyncFailure.receiptMismatch }
        self.workoutID = workoutID; self.state = state; self.acceptedAt = acceptedAt
        commitRevision = nil; analysisStatus = nil; analysisJobIDs = []
        errorCode = receipt["errorCode"].text
        raw = receipt
    }

    static func date(_ value: JSON) -> Date? {
        guard let text = value.text else { return nil }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: text) { return date }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: text)
    }

    static func validID(_ value: String) -> Bool {
        value.count == 36 && value == value.lowercased() && value != "00000000-0000-0000-0000-000000000000"
            && UUID(uuidString: value) != nil
    }
}

/// What the server currently holds for one Workout UUID, as answered by the
/// batch status endpoint. `notFound` is a normal answer, not an error.
public struct WorkoutRemoteStatus: Sendable, Equatable {
    public enum State: String, Sendable {
        case accepted, committed, rejected, deleted, notFound = "not_found"
        case alreadyImported = "already_imported"
    }
    public let workoutID: String
    public let state: State
    public let acceptedAt: Date?
    public let errorCode: String?
    public init(workoutID: String, state: State, acceptedAt: Date? = nil, errorCode: String? = nil) {
        self.workoutID = workoutID; self.state = state; self.acceptedAt = acceptedAt; self.errorCode = errorCode
    }
}

/// One foreground POST for one freshly built protocol-6 payload. The Workout
/// UUID is the identity and a 200 is the upload result; transient failures can
/// be retried with `acceptWithRetry`, and `statuses` reads back what the
/// server holds for many UUIDs at once.
public struct WorkoutIngestDelivery: Sendable {
    public static let protocolVersion = 6
    public static let retryDelays: [TimeInterval] = [1, 3, 8]
    public static let maxRetryAfter: TimeInterval = 30
    public let api: RAIAPIClient
    public init(api: RAIAPIClient) { self.api = api }

    /// Network trouble, 408, 429 and 5xx are worth another try; everything else
    /// (4xx, local validation, authentication) is not.
    static func retryDelay(for error: any Error, attempt: Int) -> TimeInterval? {
        guard attempt < retryDelays.count, let failure = error as? UploadError, failure.retryable,
              !["authentication_required", "cancelled", "bundle_too_large"].contains(failure.code) else { return nil }
        let base = retryDelays[attempt]
        guard let advised = failure.retryAfter else { return base }
        return min(max(base, advised), maxRetryAfter)
    }

    /// `accept` with up to three retries (1 s, 3 s, 8 s, or the server's
    /// `Retry-After` when longer). Re-sending identical bytes is idempotent.
    public func acceptWithRetry(_ raw: Data, workoutID: String, scope: Scope,
                                sleep: @Sendable (TimeInterval) async throws -> Void = { try await Task.sleep(for: .seconds($0)) }) async throws -> WorkoutIngestStatus {
        var attempt = 0
        while true {
            do {
                return try await accept(raw, workoutID: workoutID, scope: scope)
            } catch {
                guard let delay = Self.retryDelay(for: error, attempt: attempt) else { throw error }
                attempt += 1
                try await sleep(delay)
            }
        }
    }

    /// One batch read of the server's state, 200 UUIDs per request. The answer
    /// order does not matter; every requested UUID is present in the result.
    public func statuses(_ ids: [String]) async throws -> [String: WorkoutRemoteStatus] {
        var out: [String: WorkoutRemoteStatus] = [:]
        var index = 0
        while index < ids.count {
            try Task.checkCancellation()
            let chunk = Array(ids[index..<min(index + 200, ids.count)])
            index += chunk.count
            let body = try JSON.object(["workoutIDs": .array(chunk.map(JSON.string))]).data()
            let reply = try await api.call("POST", "/v2/sync/workout-ingests/status", body: body)
            for item in reply["items"].list {
                guard let id = item["workoutID"].text, chunk.contains(id),
                      let raw = item["ingestStatus"].text, let state = WorkoutRemoteStatus.State(rawValue: raw) else { continue }
                out[id] = WorkoutRemoteStatus(workoutID: id, state: state, acceptedAt: WorkoutIngestStatus.date(item["acceptedAt"]),
                                              errorCode: item["errorCode"].text)
            }
        }
        return out
    }

    /// Sends the exact bytes built for this Workout once. Gzip is chosen once
    /// before the request; an over-limit gzip falls back to the legal plaintext
    /// multipart body instead of producing a partial upload.
    public func accept(_ raw: Data, workoutID: String, scope: Scope) async throws -> WorkoutIngestStatus {
        try Task.checkCancellation()
        let boundary = "rai.boundary." + Wire.id()
        let body: Data
        if let wire = try? WorkoutPayloadTransport.gzipWire(raw: raw, boundary: boundary) {
            body = wire.wire
            api.metrics.value("workout_ingest_gzip_derive_ms", wire.milliseconds)
            api.metrics.value("workout_ingest_gzip_wire_bytes", Double(wire.wire.count))
        } else {
            guard raw.count <= Wire.GzipBundle.maxDecodedBytes else { throw SyncFailure.localState("bundle_too_large") }
            body = WorkoutPayloadTransport.multipart(bundle: raw, boundary: boundary)
        }
        api.metrics.value("workout_ingest_post_attempt", 1)
        do {
            let receipt = try await api.call("POST", "/v2/sync/workout-ingests/" + workoutID,
                                             body: body, mediaType: "multipart/form-data; boundary=" + boundary)
            let status = try WorkoutIngestStatus(receipt: receipt, workoutID: workoutID)
            api.metrics.value("workout_ingest_post_success", 1)
            return status
        } catch {
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            throw WorkoutPayloadTransport.failure(error)
        }
    }
}
