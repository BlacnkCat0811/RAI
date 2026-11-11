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
    func cachedResponse(_ identity: String) throws -> UploadResponse?
}
public extension FileUploader {
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let (data, status, delay) = try await upload(request, file: file, identity: identity)
        return UploadResponse(data: data, status: status, retryAfter: delay, contentType: nil)
    }
    func discardCachedResult(_ identity: String) {}
    func cachedResultFilename(_ identity: String) -> String? { nil }
    func cachedResponse(_ identity: String) throws -> UploadResponse? { nil }
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
    private let invalidateNegotiation: (@Sendable () async -> Void)?
    private let refreshToken: (@Sendable () async throws -> String)?
    public init(baseURL: URL, token: String, session: URLSession = RAIAPIClient.defaultSession, uploader: (any FileUploader)? = nil, metrics: SyncMetrics = .init(), invalidateNegotiation: (@Sendable () async -> Void)? = nil, refreshToken: (@Sendable () async throws -> String)? = nil) {
        self.baseURL = baseURL; self.token = token; self.session = session; self.uploader = uploader; self.metrics = metrics; self.refreshToken = refreshToken; self.invalidateNegotiation = invalidateNegotiation
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
            if path != "/v2/handshake", [401, 403, 409].contains(status) { await invalidateNegotiation?() }
            guard status == 200 else {
                let decoded = try? JSON.decode(data)
                let code = decoded?["code"].text
                throw UploadError(code: code ?? "unexpected_http_status", status: status, retryAfter: retryAfter, field: decoded?["field"].text, reason: decoded?["reason"].text)
            }
            if durableACK {
                let media = contentType?.split(separator: ";", maxSplits: 1).first?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
                guard !data.isEmpty, media == "application/json" else {
                    uploader?.discardCachedResult(identity)
                    throw UploadError(code: "invalid_ack_response")
                }
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
        catch {
            if durableACK { uploader?.discardCachedResult(identity) }
            throw UploadError(code: "invalid_response", status: durableACK ? 0 : 422)
        }
    }
}
