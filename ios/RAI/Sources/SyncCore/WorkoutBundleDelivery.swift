import Foundation

/// Validates the existing flat v3 receipt. Bundle SHA-256 is the receipt
/// identity; the protocol has no separate receiptID or datasetID response.
public enum WorkoutBundleACK {
    public static func validate(_ receipt: JSON, workoutID: String, bundleHash: String, sampleDeletions: Int = 0, routeDeletions: Int = 0) throws {
        guard receipt["workoutID"].text == workoutID,
              receipt["bundleSHA256"].text == bundleHash,
              bundleHash.count == 64, bundleHash.allSatisfy({ "0123456789abcdef".contains($0) }),
              receipt["ingestStatus"].text == "committed",
              let revision = receipt["commitRevision"].integer, revision > 0,
              case .object(let counts) = receipt["counts"],
              case .bool = counts["workoutInserted"],
              case .array(let jobs) = receipt["analysisJobIDs"],
              let status = receipt["analysisStatus"].text,
              ["queued", "not_required"].contains(status),
              (status == "queued" ? !jobs.isEmpty : jobs.isEmpty) else { throw SyncFailure.receiptMismatch }
        for key in ["samplesInserted", "samplesUnchanged", "samplesDeleted", "associationsInserted",
                    "associationsUnchanged", "associationsDeleted", "seriesInserted", "seriesUnchanged",
                    "routesInserted", "routesUnchanged", "routesDeleted"] {
            guard let value = counts[key]?.integer, value >= 0 else { throw SyncFailure.receiptMismatch }
        }
        guard (counts["samplesDeleted"]?.integer ?? -1) <= sampleDeletions,
              (counts["associationsDeleted"]?.integer ?? -1) <= sampleDeletions,
              (counts["routesDeleted"]?.integer ?? -1) <= routeDeletions else { throw SyncFailure.receiptMismatch }
        for job in jobs {
            guard let value = job.text, let uuid = UUID(uuidString: value), uuid.uuidString.lowercased() == value,
                  value != "00000000-0000-0000-0000-000000000000" else { throw SyncFailure.receiptMismatch }
        }
    }
}

/// Drives the existing WorkoutSyncTask and file Outbox, without a second queue.
/// Claims live in the store actor so two delivery workers share the same guard.
public struct WorkoutBundleDelivery: Sendable {
    public let store: OutboxStore
    public let api: RAIAPIClient
    public init(store: OutboxStore, api: RAIAPIClient) { self.store = store; self.api = api }

    @discardableResult public func deliver(_ id: String, now: Date = Date()) async throws -> Bool {
        try Task.checkCancellation()
        guard let task = try await store.claimBundle(id, now: now) else { return false }
        let hash = task.bundleSHA256!, identity = id + ":" + hash
        do {
            let file = try await store.fileURL(task.bundleFile!)
            let bytes = try Data(contentsOf: file, options: .mappedIfSafe)
            let prefix = Data(("--" + task.bundleBoundary! + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"workout.json\"\r\nContent-Type: application/json\r\n\r\n").utf8)
            let suffix = Data(("\r\n--" + task.bundleBoundary! + "--\r\n").utf8)
            guard bytes.starts(with: prefix), bytes.suffix(suffix.count) == suffix,
                  bytes.count >= prefix.count + suffix.count,
                  Wire.hash(bytes.subdata(in: prefix.count..<(bytes.count - suffix.count))) == hash else {
                throw SyncFailure.localState("payload_corrupt")
            }
            if let scope = await store.snapshot().scope {
                let raw = bytes.subdata(in: prefix.count..<(bytes.count - suffix.count))
                let bundle = try JSON.decode(raw)
                guard bundle["datasetID"].text == scope.datasetID,
                      bundle["datasetEpoch"].text == scope.datasetEpoch,
                      bundle["collectorEpoch"].text == scope.collectorEpoch else { throw SyncFailure.localState("bundle_session_mismatch") }
            }
            // Always verify the original exact JSON hash before deriving or
            // reusing transport bytes. Old pending plain bundles also resume.
            var uploadFile = file
            if let wireName = task.bundleWireFile, let wireHash = task.bundleWireSHA256,
               let wireURL = try? await store.fileURL(wireName),
               let wire = try? Data(contentsOf: wireURL, options: .mappedIfSafe), Wire.hash(wire) == wireHash {
                uploadFile = wireURL
            } else {
                let raw = bytes.subdata(in: prefix.count..<(bytes.count - suffix.count))
                let compressionStart = Date()
                let zipped = try Wire.gzip(raw)
                let gzipMS = Date().timeIntervalSince(compressionStart) * 1000
                if zipped.count <= 8 * 1024 * 1024 {
                    let wire = WorkoutBundleAssembler.multipart(bundle: zipped, boundary: task.bundleBoundary!, compressed: true)
                    let name = try await store.savePayload(wire, suffix: "multipart.gz")
                    try await store.setBundleWire(id, bundleHash: hash, file: name, sha256: Wire.hash(wire), wireBytes: wire.count, gzipMS: gzipMS)
                    uploadFile = try await store.fileURL(name)
                }
            }
            let receipt: JSON
            do {
                let posted = try await api.call("POST", "/v2/sync/workout-bundles", file: uploadFile,
                    mediaType: "multipart/form-data; boundary=" + task.bundleBoundary!, identity: identity)
                try WorkoutBundleACK.validate(posted, workoutID: id, bundleHash: hash, sampleDeletions: task.submittedSampleDeletions ?? 0, routeDeletions: task.submittedRouteDeletions ?? 0)
                receipt = posted
            } catch {
                // Preemption leaves the OS-managed transfer available for
                // reattachment; do not start a receipt lookup while cancelled.
                if error is CancellationError || Task.isCancelled { throw CancellationError() }
                let failure = Self.failure(error)
                // Deterministic request/deletion conflicts never become GET
                // success. Network, timeout and malformed ACK can reconcile.
                guard failure.status != 429, failure.code != "authentication_required", failure.retryable || failure.code == "receipt_mismatch" else { throw failure }
                do {
                    let recovered = try await api.call("GET", "/v2/sync/workout-bundles/" + id, query: ["sha256": hash])
                    try WorkoutBundleACK.validate(recovered, workoutID: id, bundleHash: hash, sampleDeletions: task.submittedSampleDeletions ?? 0, routeDeletions: task.submittedRouteDeletions ?? 0)
                    receipt = recovered
                } catch let lookup as UploadError where lookup.status == 404 {
                    // Preserve 429 Retry-After and original diagnostic details.
                    throw failure
                }
            }
            try await store.acknowledgeBundle(id, receipt: receipt)
            api.discardUploadResult(identity)
            await store.endBundleAttempt(id)
            return true
        } catch {
            // Invalid cached 200 must not trap subsequent attempts forever.
            // Cancellation leaves OS-managed upload/result for reattachment.
            let cancelled = error is CancellationError || Task.isCancelled
            if !cancelled { api.discardUploadResult(identity) }
            let failure = Self.failure(cancelled ? CancellationError() : error)
            do { try await store.failBundle(id, error: failure, now: max(now, Date())) }
            catch { await store.endBundleAttempt(id); throw error }
            await store.endBundleAttempt(id)
            if cancelled { throw CancellationError() }
            throw failure
        }
    }
    static func failure(_ error: any Error) -> UploadError {
        if let error = error as? UploadError {
            if error.status == 401 { return UploadError(code: "authentication_required", retryAfter: 60) }
            if [201, 202, 204].contains(error.status) { return UploadError(code: "invalid_ack_status") }
            return error
        }
        if let error = error as? SyncFailure {
            switch error {
            case .receiptMismatch: return UploadError(code: "receipt_mismatch")
            case .invalid(let code), .localState(let code): return UploadError(code: code, status: 422)
            }
        }
        if error is CancellationError { return UploadError(code: "cancelled") }
        return UploadError(code: "local_or_transport_failure")
    }
}
