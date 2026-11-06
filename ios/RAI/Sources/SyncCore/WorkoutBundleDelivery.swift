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
///
/// Transport contract (round 2): every WorkoutBundle POST — first upload, every
/// retry, an old plain pending task resumed after an upgrade, and a deletion-only
/// child bundle — is a multipart body with exactly one `file` part whose media
/// type is `application/gzip` and whose filename is `workout.json.gz`. There is
/// no plaintext fallback: `bundleSHA256` stays bound to the exact decompressed
/// JSON bytes, the gzip wire hash is recorded separately, and any failure to
/// derive a legal gzip artifact fails the attempt while keeping the original
/// bundle, hash, boundary and recovery state.
public struct WorkoutBundleDelivery: Sendable {
    public let store: OutboxStore
    public let api: RAIAPIClient
    public init(store: OutboxStore, api: RAIAPIClient) { self.store = store; self.api = api }

    @discardableResult public func deliver(_ id: String, now: Date = Date()) async throws -> Bool {
        try Task.checkCancellation()
        guard let task = try await store.claimBundle(id, now: now) else { return false }
        let hash = task.bundleSHA256!, identity = id + ":" + hash
        do {
            // Observability invariants, recorded on every attempt including the
            // failing ones: a bundle POST is always a gzip part, the plaintext
            // fallback counter must stay at zero, and preparing or retrying a
            // bundle never re-reads HealthKit.
            api.metrics.value("bundle_plain_fallback", 0)
            api.metrics.value("bundle_healthkit_reread", 0)
            // A handshake that ran and did not advertise `bundleGzipPart` makes
            // the gzip-only contract impossible. Fail diagnosably and keep the
            // task instead of inventing an endpoint or sending plaintext. An
            // unobserved handshake (`unknown`) still sends gzip, because plain
            // is never an option on the wire.
            if api.bundleGzipCapability == .declared(false) { throw SyncFailure.localState("bundle_gzip_unsupported") }
            let file = try await store.fileURL(task.bundleFile!)
            let bytes = try Data(contentsOf: file, options: .mappedIfSafe)
            let prefix = Data(("--" + task.bundleBoundary! + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"workout.json\"\r\nContent-Type: application/json\r\n\r\n").utf8)
            let suffix = Data(("\r\n--" + task.bundleBoundary! + "--\r\n").utf8)
            guard bytes.starts(with: prefix), bytes.suffix(suffix.count) == suffix,
                  bytes.count >= prefix.count + suffix.count,
                  Wire.hash(bytes.subdata(in: prefix.count..<(bytes.count - suffix.count))) == hash else {
                throw SyncFailure.localState("payload_corrupt")
            }
            let raw = bytes.subdata(in: prefix.count..<(bytes.count - suffix.count))
            if let scope = await store.snapshot().scope {
                let bundle = try JSON.decode(raw)
                guard bundle["datasetID"].text == scope.datasetID,
                      bundle["datasetEpoch"].text == scope.datasetEpoch,
                      bundle["collectorEpoch"].text == scope.collectorEpoch else { throw SyncFailure.localState("bundle_session_mismatch") }
            }
            // The original multipart stays the local authority and is verified
            // above; it is never uploaded. Only a sealed gzip artifact is sent,
            // and a cached one is reused only while its bytes still hash and it
            // is still a gzip file part.
            let uploadFile: URL
            if let wireName = task.bundleWireFile, let wireHash = task.bundleWireSHA256,
               let wireURL = try? await store.fileURL(wireName),
               let wire = try? Data(contentsOf: wireURL, options: .mappedIfSafe),
               Wire.hash(wire) == wireHash, Self.isGzipMultipart(wire, boundary: task.bundleBoundary!) {
                uploadFile = wireURL
            } else {
                let derived = try Self.gzipWire(raw: raw, boundary: task.bundleBoundary!)
                let name = try await store.savePayload(derived.wire, suffix: "multipart.gz")
                try await store.setBundleWire(id, bundleHash: hash, file: name, sha256: Wire.hash(derived.wire),
                                              wireBytes: derived.wire.count, gzipMS: derived.milliseconds)
                api.metrics.value("bundle_gzip_derive_ms", derived.milliseconds)
                api.metrics.value("bundle_gzip_wire_bytes", Double(derived.wire.count))
                uploadFile = try await store.fileURL(name)
            }
            // A bundle POST is always a gzip part.
            api.metrics.value("bundle_gzip_post_attempt", 1)
            let receipt: JSON
            do {
                let posted = try await api.call("POST", "/v2/sync/workout-bundles", file: uploadFile,
                    mediaType: "multipart/form-data; boundary=" + task.bundleBoundary!, identity: identity)
                try WorkoutBundleACK.validate(posted, workoutID: id, bundleHash: hash, sampleDeletions: task.submittedSampleDeletions ?? 0, routeDeletions: task.submittedRouteDeletions ?? 0)
                api.metrics.value("bundle_gzip_post_success", 1)
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
    /// One sealed gzip wire artifact plus the compression wall clock it took.
    struct GzipWire { let wire: Data; let milliseconds: Double }

    /// Derives the only legal wire artifact from the already-verified original
    /// JSON: a multipart body with one `file` part, `Content-Type:
    /// application/gzip`, filename `workout.json.gz`. Over-limit input throws so
    /// the caller keeps the original bundle and reports an explicit failure; it
    /// never pads, splits, or falls back to the plaintext part.
    static func gzipWire(raw: Data, boundary: String) throws -> GzipWire {
        guard raw.count <= Wire.GzipBundle.maxDecodedBytes else { throw SyncFailure.localState("bundle_too_large") }
        let compressionStart = Date()
        let zipped = try Wire.gzip(raw)
        let milliseconds = Date().timeIntervalSince(compressionStart) * 1000
        try Wire.GzipBundle.validate(decoded: raw.count, compressed: zipped.count)
        let wire = WorkoutBundleAssembler.multipart(bundle: zipped, boundary: boundary, compressed: true)
        guard isGzipMultipart(wire, boundary: boundary) else { throw SyncFailure.localState("gzip_wire_invalid") }
        return GzipWire(wire: wire, milliseconds: milliseconds)
    }

    /// A cached wire artifact is only reusable while it is still a single gzip
    /// file part; a plaintext part (or any non-gzip body) must never be sent.
    static func isGzipMultipart(_ wire: Data, boundary: String) -> Bool {
        let opener = Data(("--" + boundary + "\r\n").utf8)
        guard wire.starts(with: opener) else { return false }
        guard let separator = wire.range(of: Data("\r\n\r\n".utf8)) else { return false }
        let headers = wire[wire.startIndex..<separator.lowerBound]
        guard String(decoding: headers, as: UTF8.self).contains("Content-Type: application/gzip"),
              String(decoding: headers, as: UTF8.self).contains("filename=\"workout.json.gz\"") else { return false }
        let payload = wire.distance(from: wire.startIndex, to: separator.upperBound)
        guard payload + 2 <= wire.count else { return false }
        return wire[wire.startIndex + payload] == 0x1F && wire[wire.startIndex + payload + 1] == 0x8B
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
