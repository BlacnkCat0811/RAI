import Foundation

/// Multipart framing, gzip wire sealing and transport error mapping for the
/// protocol-6 Workout payload. The payload version itself is validated by the
/// ingest delivery, never inferred here.
public enum WorkoutPayloadTransport {
    public static func multipart(bundle: Data, boundary: String, compressed: Bool = false) -> Data {
        let filename = compressed ? "workout.json.gz" : "workout.json"
        let mediaType = compressed ? "application/gzip" : "application/json"
        var body = Data(("--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + filename + "\"\r\nContent-Type: " + mediaType + "\r\n\r\n").utf8)
        body.append(bundle)
        body.append(Data(("\r\n--" + boundary + "--\r\n").utf8))
        return body
    }

    struct GzipWire { let wire: Data; let milliseconds: Double }

    /// Derives the only wire artifact this lane sends: one `file` part with
    /// `Content-Type: application/gzip`. Over-limit input throws so the caller
    /// keeps the original payload and reports an explicit failure.
    static func gzipWire(raw: Data, boundary: String) throws -> GzipWire {
        guard raw.count <= Wire.GzipBundle.maxDecodedBytes else { throw SyncFailure.localState("bundle_too_large") }
        let compressionStart = Date()
        let zipped = try Wire.gzip(raw)
        let milliseconds = Date().timeIntervalSince(compressionStart) * 1000
        try Wire.GzipBundle.validate(decoded: raw.count, compressed: zipped.count)
        let wire = multipart(bundle: zipped, boundary: boundary, compressed: true)
        guard isGzipMultipart(wire, boundary: boundary) else { throw SyncFailure.localState("gzip_wire_invalid") }
        return GzipWire(wire: wire, milliseconds: milliseconds)
    }

    /// A cached wire artifact is only reusable while it is still a single gzip
    /// file part; a plaintext part (or any non-gzip body) is never sent.
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
