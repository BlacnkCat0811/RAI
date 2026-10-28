import Foundation

public protocol FileUploader: Sendable {
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?)
}
public struct RAIAPIClient: Sendable {
    public let baseURL: URL
    private let token: String
    private let session: URLSession
    private let uploader: (any FileUploader)?
    private let metrics: SyncMetrics
    public init(baseURL: URL, token: String, session: URLSession = .shared, uploader: (any FileUploader)? = nil, metrics: SyncMetrics = .init()) {
        self.baseURL = baseURL; self.token = token; self.session = session; self.uploader = uploader; self.metrics = metrics
    }
    public func call(_ method: String, _ path: String, body: Data? = nil, file: URL? = nil,
                     mediaType: String = "application/json", gzip: Bool = false, identity: String = "", query: [String: String] = [:]) async throws -> JSON {
        let networkStart = Date()
        defer {
            metrics.elapsed("network_ms", since: networkStart)
            if path.hasPrefix("/v2/routes/") && path.hasSuffix("/seal") { metrics.elapsed("route_seal_ms", since: networkStart) }
            if path.hasPrefix("/v2/routes/") && path.contains("/chunks/") { metrics.elapsed("route_chunk_upload_ms", since: networkStart) }
        }
        guard path.hasPrefix("/v2/"), var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false) else { throw SyncFailure.invalid("invalid_api_path") }
        guard !path.contains("?"), !path.contains("#") else { throw SyncFailure.invalid("invalid_api_path") }
        components.path = path; components.queryItems = query.isEmpty ? nil : query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }; components.fragment = nil
        guard let url = components.url else { throw SyncFailure.invalid("invalid_api_url") }
        var request = URLRequest(url: url, timeoutInterval: 60)
        request.httpMethod = method; request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        request.setValue(mediaType, forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(Wire.id(), forHTTPHeaderField: "X-Request-ID")
        if gzip { request.setValue("gzip", forHTTPHeaderField: "Content-Encoding") }
        do {
            let data: Data, status: Int, retryAfter: TimeInterval?
            if let file, let uploader {
                (data, status, retryAfter) = try await uploader.upload(request, file: file, identity: identity)
            } else {
                let response: URLResponse
                if let file { (data, response) = try await session.upload(for: request, fromFile: file) }
                else { request.httpBody = body; (data, response) = try await session.data(for: request) }
                guard let http = response as? HTTPURLResponse else { throw UploadError(code: "invalid_http_response") }
                status = http.statusCode; retryAfter = http.value(forHTTPHeaderField: "Retry-After").flatMap(Double.init)
            }
            guard data.count <= 1_048_576 else { throw UploadError(code: "oversized_response", status: 422) }
            guard status == 200 else {
                let decoded = try? JSON.decode(data)
                let code = decoded?["code"].text
                throw UploadError(code: code ?? "unexpected_http_status", status: status, retryAfter: retryAfter)
            }
            let decodeStart = Date()
            let json = try JSON.decode(data)
            metrics.elapsed("response_decode_ms", since: decodeStart)
            return json
        } catch let error as UploadError { throw error }
        catch let error as URLError { throw UploadError(code: "network_" + String(error.code.rawValue)) }
        catch is CancellationError { throw CancellationError() }
        catch { throw UploadError(code: "invalid_response", status: 422) }
    }
}

public actor OutboxPipeline {
    private let store: OutboxStore
    private let api: RAIAPIClient
    private let metrics: SyncMetrics
    private var draining = false
    public init(store: OutboxStore, api: RAIAPIClient, metrics: SyncMetrics = .init()) { self.store = store; self.api = api; self.metrics = metrics }
    public func drain() async throws {
        guard !draining else { return }; draining = true; defer { draining = false }
        let snapshot = await store.snapshot()
        let pages = snapshot.pages.sorted { first, second in
            let firstIsFact = snapshot.streams[first.anchorKey] != nil, secondIsFact = snapshot.streams[second.anchorKey] != nil
            if firstIsFact != secondIsFact { return firstIsFact }
            return first.createdAt < second.createdAt
        }
        for page in pages {
            let generation = await store.snapshot().factGeneration
            let dependencyBecameAvailable = page.waitingForFactGeneration.map { generation > $0 } ?? false
            if let error = page.error, !error.retryable, !dependencyBecameAvailable { continue }
            guard dependencyBecameAvailable || page.nextAttempt <= Date() else { continue }
            do {
                metrics.value("retry_count", Double(page.retryCount))
                try await store.begin(page.id)
                for cursor in page.cursor..<page.operations.count {
                    try Task.checkCancellation()
                    let receipt = try await execute(page.operations[cursor], identity: page.id + ":" + String(cursor))
                    try await store.acknowledge(page.id, cursor: cursor, receipt: receipt)
                }
                if page.anchorKey.hasPrefix("route:"), page.operations.contains(where: { operation in
                    if case .request(let request) = operation { return request.path.hasSuffix("/seal") }; return false
                }) {
                    // Total wall time from durable staging through seal,
                    // including retries, suspension and queue wait.
                    metrics.elapsed("route_upload_ms", since: page.createdAt)
                }
            } catch is CancellationError { throw CancellationError() }
            catch {
                let mapped: UploadError
                if let error = error as? UploadError { mapped = error }
                else if let error = error as? SyncFailure {
                    switch error {
                    case .receiptMismatch: mapped = UploadError(code: "receipt_mismatch", status: 422)
                    case .invalid(let code), .localState(let code): mapped = UploadError(code: code, status: 422)
                    }
                } else { mapped = UploadError(code: "local_persistence_failed", status: 422) }
                try await store.fail(page.id, error: mapped)
                // Preserve other independent streams; failure blocks this page.
            }
        }
    }
    private func execute(_ operation: OutboxOperation, identity: String) async throws -> JSON {
        switch operation {
        case .request(let request):
            let file: URL?
            if let name = request.file { file = try await store.fileURL(name) } else { file = nil }
            let data = try file.map { try Data(contentsOf: $0) }
            let response = try await api.call(request.method, request.path, body: request.gzip ? nil : data,
                                              file: request.gzip ? file : nil, mediaType: request.mediaType, gzip: request.gzip, identity: identity)
            try request.validate(response)
            return response
        case .batch(let envelope, let name):
            let ackStart = Date()
            defer { metrics.elapsed("ack_total_ms", since: ackStart) }
            let file = try await store.fileURL(name)
            // Validate immutable bytes, including after process death. Never
            // recompress a file to retry a registered batch.
            let bytes = try Data(contentsOf: file, options: .mappedIfSafe)
            guard bytes.count == envelope.wireBytes, Wire.hash(bytes) == envelope.payloadSHA256 else { throw SyncFailure.localState("payload_corrupt") }
            var state: JSON?
            do { state = try await api.call("GET", "/v2/sync/batches/" + envelope.batchID) }
            catch let error as UploadError where error.status == 404 && error.code == "batch_not_found" { }
            if state == nil { state = try await api.call("POST", "/v2/sync/batches", body: envelope.canonical()) }
            guard let state, state["envelope"] == (try JSON.decode(envelope.canonical())) else { throw SyncFailure.receiptMismatch }
            if state["ingestStatus"].text == "committed" {
                try envelope.validateReceipt(state["receipt"]); return state["receipt"]
            }
            guard state["ingestStatus"].text == "registered" else { throw SyncFailure.receiptMismatch }
            let receipt = try await api.call("PUT", "/v2/sync/batches/" + envelope.batchID + "/body", file: file,
                                             mediaType: "application/x-rai-health-delta+ndjson", gzip: true, identity: identity)
            try envelope.validateReceipt(receipt); return receipt
        }
    }
}
