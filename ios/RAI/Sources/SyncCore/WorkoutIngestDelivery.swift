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

    /// Acceptance answers only ever use these two states; anything else is not
    /// a receipt for this exact payload.
    public init(receipt: JSON, workoutID: String) throws {
        guard receipt["workoutID"].text == workoutID,
              let rawState = receipt["ingestStatus"].text, let state = WorkoutIngestState(rawValue: rawState),
              state == .accepted || state == .alreadyImported else { throw SyncFailure.receiptMismatch }
        let acceptedAt = Self.date(receipt["acceptedAt"])
        guard state == .alreadyImported || acceptedAt != nil else { throw SyncFailure.receiptMismatch }
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

/// One POST for the exact multipart body persisted by the Outbox. There is no
/// payload hash, upload id or ingest id in this lane: the UUID is the identity
/// and the first accepted bytes are authoritative server-side.
public struct WorkoutIngestDelivery: Sendable {
    public let store: OutboxStore
    public let api: RAIAPIClient
    public init(store: OutboxStore, api: RAIAPIClient) { self.store = store; self.api = api }

    /// Returns false when the store's retry delay has not elapsed yet.
    @discardableResult
    public func accept(_ id: String, scope: Scope, now: Date = Date()) async throws -> Bool {
        try Task.checkCancellation()
        guard let task = try await store.claimIngest(id, now: now) else { return false }
        let boundary = task.bundleBoundary!
        let identity = "workout-ingest:" + scope.datasetID + ":" + id
        do {
            let file = try await store.fileURL(task.bundleFile!)
            let uploadFile: URL
            if let wireName = task.bundleWireFile, let wireURL = try? await store.fileURL(wireName),
               let wire = try? Data(contentsOf: wireURL, options: .mappedIfSafe),
               WorkoutBundleDelivery.isGzipMultipart(wire, boundary: boundary) {
                // The payload file is immutable for this task, so a derived wire
                // from an earlier attempt stays valid without re-reading or
                // re-hashing the raw body.
                uploadFile = wireURL
            } else {
                let raw = try Self.payloadBody(file: file, boundary: boundary)
                let bundle = try JSON.decode(raw)
                guard bundle["protocolVersion"].integer == 4,
                      bundle["workoutID"].text == id,
                      bundle["datasetID"].text == scope.datasetID,
                      bundle["datasetEpoch"].text == scope.datasetEpoch,
                      bundle["collectorEpoch"].text == scope.collectorEpoch else {
                    throw SyncFailure.localState("bundle_session_mismatch")
                }
                let derived = try WorkoutBundleDelivery.gzipWire(raw: raw, boundary: boundary)
                let name = try await store.savePayload(derived.wire, suffix: "multipart.gz")
                try await store.setIngestWire(id, file: name, wireBytes: derived.wire.count, gzipMS: derived.milliseconds)
                api.metrics.value("workout_ingest_gzip_derive_ms", derived.milliseconds)
                api.metrics.value("workout_ingest_gzip_wire_bytes", Double(derived.wire.count))
                uploadFile = try await store.fileURL(name)
            }
            api.metrics.value("workout_ingest_post_attempt", 1)
            do {
                let receipt = try await api.call("POST", "/v2/sync/workout-ingests/" + id, file: uploadFile,
                                                 mediaType: "multipart/form-data; boundary=" + boundary,
                                                 identity: identity)
                try await store.acknowledgeWorkoutIngest(id, receipt: receipt)
                api.metrics.value("workout_ingest_post_success", 1)
            } catch {
                // Preemption leaves the OS-managed transfer for reattachment.
                if error is CancellationError || Task.isCancelled { throw CancellationError() }
                let failure = WorkoutBundleDelivery.failure(error)
                // Credentials are shared by both calls; a second 401 explains
                // nothing new and would only add delay.
                guard failure.code != "authentication_required" else { throw failure }
                // A lost response after commit is the common ambiguous case.
                // The status answer decides; the same bytes are never re-sent
                // just to learn that the first acceptance exists.
                let recovered = try? await Self.status(id, api: api)
                guard let recovered else { throw failure }
                try await store.applyWorkoutIngestStatus(id, status: recovered)
                api.metrics.value("workout_ingest_post_reconciled", 1)
            }
            api.discardUploadResult(identity)
            await store.endBundleAttempt(id)
            return true
        } catch {
            let cancelled = error is CancellationError || Task.isCancelled
            if !cancelled { api.discardUploadResult(identity) }
            let failure = WorkoutBundleDelivery.failure(cancelled ? CancellationError() : error)
            do { try await store.failIngest(id, error: failure, now: max(now, Date())) }
            catch { await store.endBundleAttempt(id); throw error }
            await store.endBundleAttempt(id)
            if cancelled { throw CancellationError() }
            throw failure
        }
    }

    /// `nil` means the exact 404 `workout_ingest_not_found`; every other error
    /// propagates so a network failure is never mistaken for "not received".
    public static func status(_ id: String, api: RAIAPIClient) async throws -> WorkoutIngestStatus? {
        do {
            let value = try await api.call("GET", "/v2/sync/workout-ingests/" + id)
            return try WorkoutIngestStatus(value, workoutID: id)
        } catch let error as UploadError where error.status == 404 && error.code == "workout_ingest_not_found" {
            return nil
        }
    }

    /// The stored file is exactly the plaintext multipart body; the JSON bytes
    /// between its framing are the payload the server would receive.
    static func payloadBody(file: URL, boundary: String) throws -> Data {
        let bytes = try Data(contentsOf: file, options: .mappedIfSafe)
        let opener = Data(("--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"workout.json\"\r\nContent-Type: application/json\r\n\r\n").utf8)
        let suffix = Data(("\r\n--" + boundary + "--\r\n").utf8)
        guard bytes.count >= opener.count + suffix.count, bytes.starts(with: opener), bytes.suffix(suffix.count) == suffix else {
            throw SyncFailure.localState("payload_corrupt")
        }
        return bytes.subdata(in: opener.count..<(bytes.count - suffix.count))
    }
}
