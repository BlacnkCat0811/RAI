import Foundation
import Synchronization

public struct UploadResponse: Sendable {
    public let data: Data
    public let status: Int
    public let retryAfter: TimeInterval?
    public let contentType: String?
    public init(data: Data, status: Int, retryAfter: TimeInterval? = nil, contentType: String?) {
        self.data = data; self.status = status; self.retryAfter = retryAfter; self.contentType = contentType
    }
}

public protocol FileUploader: Sendable {
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?)
    /// Drop a durable transfer result after the caller has durably consumed it.
    /// Implementations that do not cache results may ignore this.
    func discardCachedResult(_ identity: String)
    func cachedResultFilename(_ identity: String) -> String?
}
public extension FileUploader {
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let (data, status, delay) = try await upload(request, file: file, identity: identity)
        return UploadResponse(data: data, status: status, retryAfter: delay, contentType: nil)
    }
    func discardCachedResult(_ identity: String) {}
    func cachedResultFilename(_ identity: String) -> String? { nil }
}
public struct RAIAPIClient: Sendable {
    public static let defaultSession: URLSession = {
        let config = URLSessionConfiguration.default
        config.timeoutIntervalForRequest = 60
        config.timeoutIntervalForResource = 300
        return URLSession(configuration: config)
    }()
    public static func responseBudget(for path: String) -> Int {
        if path.hasPrefix("/v2/training/plans/") && ["/history/versions", "/audit", "/detail"].contains(where: path.hasSuffix) { return 262_144 }
        return 1_048_576
    }
    public let baseURL: URL
    private let token: String
    private let session: URLSession
    private let uploader: (any FileUploader)?
    let metrics: SyncMetrics
    private let refreshToken: (@Sendable () async throws -> String)?
    public init(baseURL: URL, token: String, session: URLSession = RAIAPIClient.defaultSession, uploader: (any FileUploader)? = nil, metrics: SyncMetrics = .init(), refreshToken: (@Sendable () async throws -> String)? = nil) {
        self.baseURL = baseURL; self.token = token; self.session = session; self.uploader = uploader; self.metrics = metrics; self.refreshToken = refreshToken
    }

    public func call(_ method: String, _ path: String, body: Data? = nil, file: URL? = nil,
                     mediaType: String = "application/json", gzip: Bool = false, identity: String = "", query: [String: String] = [:],
                     headers: [String: String] = [:]) async throws -> JSON {
        do {
            return try await performCall(method, path, body: body, file: file, mediaType: mediaType, gzip: gzip, identity: identity, query: query, headers: headers)
        } catch let error as UploadError where error.status == 401 {
            // Device bearer tokens have no refresh endpoint. Integrators with a
            // credential refresher can retry once; otherwise retain the Outbox
            // and let the existing configuration/Keychain flow replace it.
            guard let refreshToken else { throw error }
            let refreshed = try await refreshToken()
            let client = RAIAPIClient(baseURL: baseURL, token: refreshed, session: session, uploader: uploader, metrics: metrics)
            return try await client.call(method, path, body: body, file: file, mediaType: mediaType, gzip: gzip, identity: identity, query: query, headers: headers)
        }
    }
    public func cachedUploadResultFilename(_ identity: String) -> String? { uploader?.cachedResultFilename(identity) }
    public func discardUploadResult(_ identity: String) { uploader?.discardCachedResult(identity) }
    public static func retryAfter(_ value: String?, now: Date = Date()) -> TimeInterval? {
        guard let value else { return nil }
        if let seconds = Double(value), seconds.isFinite { return max(0, seconds) }
        let formatter = DateFormatter(); formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(secondsFromGMT: 0); formatter.dateFormat = "EEE, dd MMM yyyy HH:mm:ss z"
        return formatter.date(from: value).map { max(0, $0.timeIntervalSince(now)) }
    }
    private func performCall(_ method: String, _ path: String, body: Data?, file: URL?,
                             mediaType: String, gzip: Bool, identity: String, query: [String: String],
                             headers: [String: String] = [:]) async throws -> JSON {
        let durableACK = path.hasPrefix("/v2/sync/workout-ingests") || path == "/v2/sync/daily-facts" || path == "/v2/sync/vo2max-facts"
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
        var request = URLRequest(url: url, timeoutInterval: method == "POST" && path.hasPrefix("/v2/sync/workout-ingests/") ? 270 : 60)
        request.httpMethod = method; request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        request.setValue(mediaType, forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue(Wire.id(), forHTTPHeaderField: "X-Request-ID")
        for (name, value) in headers.sorted(by: { $0.key < $1.key }) { request.setValue(value, forHTTPHeaderField: name) }
        if gzip { request.setValue("gzip", forHTTPHeaderField: "Content-Encoding") }
        do {
            let data: Data, status: Int, retryAfter: TimeInterval?, contentType: String?
            if let file, let uploader {
                let response = try await uploader.response(request, file: file, identity: identity)
                data = response.data; status = response.status; retryAfter = response.retryAfter; contentType = response.contentType
            } else {
                let response: URLResponse
                if let file { (data, response) = try await session.upload(for: request, fromFile: file) }
                else { request.httpBody = body; (data, response) = try await session.data(for: request) }
                guard let http = response as? HTTPURLResponse else { throw UploadError(code: "invalid_http_response") }
                status = http.statusCode; retryAfter = Self.retryAfter(http.value(forHTTPHeaderField: "Retry-After")); contentType = http.value(forHTTPHeaderField: "Content-Type")
            }
            guard data.count <= Self.responseBudget(for: path) else { throw UploadError(code: "oversized_response", status: durableACK ? 0 : 422) }
            guard status == 200 else {
                let decoded = try? JSON.decode(data)
                let code = decoded?["code"].text
                throw UploadError(code: code ?? "unexpected_http_status", status: status, retryAfter: retryAfter, field: decoded?["field"].text, reason: decoded?["reason"].text)
            }
            if durableACK {
                let media = contentType?.split(separator: ";", maxSplits: 1).first?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
                guard !data.isEmpty, media == "application/json" else { throw UploadError(code: "invalid_ack_response") }
            }
            let decodeStart = Date()
            let json = try JSON.decode(data)
            metrics.elapsed("response_decode_ms", since: decodeStart)
            return json
        } catch let error as UploadError { throw error }
        catch let error as URLError {
            if error.code == .cancelled, Task.isCancelled { throw CancellationError() }
            throw UploadError(code: "network_" + String(error.code.rawValue))
        }
        catch is CancellationError { throw CancellationError() }
        catch { throw UploadError(code: "invalid_response", status: durableACK ? 0 : 422) }
    }
}

public actor OutboxPipeline {
    private let store: OutboxStore
    private let api: RAIAPIClient
    private let metrics: SyncMetrics
    private var draining = false
    /// While an initial quantity import is still catching up, association
    /// Set when the server's device bucket is exhausted. The bucket is shared
    /// by every page and every request, so continuing this pass can only
    /// produce more 429s and grow each page's retry counter. The coordinator
    /// stops reading HealthKit until the caller retries after this instant.
    public private(set) var pausedUntil: Date?
    public init(store: OutboxStore, api: RAIAPIClient, metrics: SyncMetrics = .init()) { self.store = store; self.api = api; self.metrics = metrics }
    private let seriesConcurrency = 8
    /// Drains every staged page when `taskID` is nil (startup/background
    /// recovery), or only pages owned by one Workout task. A task-scoped pass
    /// can never upload another task's page or mark it acknowledged.
    public func drain(taskID: String? = nil) async throws {
        guard !draining else { return }; draining = true; defer { draining = false }
        pausedUntil = nil
        let snapshot = await store.snapshot()
        let pages = snapshot.pages.filter { taskID == nil || $0.taskID == taskID }.sorted { first, second in
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
                let remainingCursor = try await executeSeriesPage(page)
                for cursor in remainingCursor..<page.operations.count {
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
                // The device budget is shared by all lanes/pages. Stop this
                // pass instead of hammering the remaining pages.
                if mapped.status == 429 {
                    pausedUntil = Date().addingTimeInterval(max(1, mapped.retryAfter ?? 1))
                    return
                }
                // Preserve other independent streams; failure blocks this page.
            }
        }
    }
    /// Run a contiguous series block concurrently, then let drain validate the
    /// final page receipt. Individual successes survive failures and restarts;
    /// only that final receipt may promote the delivered HealthKit anchor.
    private func executeSeriesPage(_ page: PendingPage) async throws -> Int {
        let operations = page.operations
        var seriesStart = page.cursor
        while seriesStart < operations.count && !isSeriesOperation(operations[seriesStart]) { seriesStart += 1 }
        var seriesEnd = seriesStart
        while seriesEnd < operations.count && isSeriesOperation(operations[seriesEnd]) { seriesEnd += 1 }
        guard seriesStart < seriesEnd,
              let groups = seriesGroups(operations, from: seriesStart, to: seriesEnd) else { return page.cursor }
        for cursor in page.cursor..<seriesStart {
            try Task.checkCancellation()
            let receipt = try await execute(operations[cursor], identity: page.id + ":" + String(cursor))
            try await store.acknowledge(page.id, cursor: cursor, receipt: receipt)
        }
        let savedReceipts = await store.snapshot().receipts
        for start in stride(from: 0, to: groups.count, by: seriesConcurrency) {
            try Task.checkCancellation()
            let end = min(start + seriesConcurrency, groups.count)
            let wave = Array(groups[start..<end])
            try await withThrowingTaskGroup(of: SeriesOutcome.self) { group in
                for item in wave {
                    group.addTask { await self.executeSeriesGroup(item, pageID: page.id, saved: savedReceipts) }
                }
                var failure: (any Error)?
                for try await outcome in group {
                    if !outcome.receipts.isEmpty {
                        try await store.acknowledgeSeries(page.id, receipts: outcome.receipts)
                    }
                    if let error = outcome.error {
                        if failure == nil || (error as? UploadError)?.status == 429 { failure = error }
                        group.cancelAll()
                    }
                }
                if let failure { throw failure }
            }
        }
        return seriesEnd
    }
    private struct SeriesOutcome: Sendable {
        var receipts: [Int: JSON] = [:]
        var error: (any Error)?
    }
    private func executeSeriesGroup(_ group: [(Int, OutboxOperation)], pageID: String, saved: [String: JSON]) async -> SeriesOutcome {
        var outcome = SeriesOutcome()
        do {
            for (cursor, operation) in group {
                try Task.checkCancellation()
                let identity = pageID + ":" + String(cursor)
                if let receipt = saved[identity], case .request(let request) = operation {
                    try request.validate(receipt)
                    continue
                }
                outcome.receipts[cursor] = try await execute(operation, identity: identity)
            }
        } catch { outcome.error = error }
        return outcome
    }
    private func isSeriesOperation(_ operation: OutboxOperation) -> Bool {
        guard case .request(let request) = operation else { return false }
        return request.path == "/v2/sync/series" || request.path.hasPrefix("/v2/sync/series/")
    }
    private func seriesGroups(_ operations: [OutboxOperation], from start: Int, to end: Int) -> [[(Int, OutboxOperation)]]? {
        var groups: [[(Int, OutboxOperation)]] = []
        var current: [(Int, OutboxOperation)] = []
        var currentID: String?
        for index in start..<end {
            guard case .request(let request) = operations[index], let id = seriesID(for: request) else { return nil }
            if request.path == "/v2/sync/series" {
                if !current.isEmpty { groups.append(current) }
                current = [(index, operations[index])]; currentID = id
            } else {
                if currentID == nil { currentID = id }
                guard currentID == id else { return nil }
                current.append((index, operations[index]))
            }
        }
        if !current.isEmpty { groups.append(current) }
        for (index, group) in groups.enumerated() {
            guard case .request(let first) = group.first!.1,
                  index == 0 || first.path == "/v2/sync/series",
                  case .request(let last) = group.last!.1, last.path.hasSuffix("/seal") else { return nil }
        }
        return groups
    }
    private func seriesID(for request: RequestOperation) -> String? {
        if request.path == "/v2/sync/series" { return request.expected["seriesID"]?.text }
        let parts = request.path.split(separator: "/")
        guard parts.count >= 4, parts[0] == "v2", parts[1] == "sync", parts[2] == "series" else { return nil }
        return String(parts[3])
    }
    private func execute(_ operation: OutboxOperation, identity: String) async throws -> JSON {
        switch operation {
        case .request(let request):
            let file: URL?
            if let name = request.file { file = try await store.fileURL(name) } else { file = nil }
            // Associations are the only high-volume request pages. Send them
            // through the background URLSession so a locked screen does not
            // strand thousands of tiny writes mid-page. Batch bodies already
            // use the same session via the `.batch` case.
            let background = request.path == "/v2/sync/associations" || request.path == "/v2/sync/workout-enrichments"
            let data = background || request.gzip ? nil : try file.map { try Data(contentsOf: $0) }
            let response = try await api.call(request.method, request.path, body: data,
                                              file: background || request.gzip ? file : nil, mediaType: request.mediaType, gzip: request.gzip, identity: identity)
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
