import Foundation
import Synchronization
import Testing
@testable import SyncCore

// R4 transport contract: every WorkoutBundle POST carries exactly one `file`
// part with `Content-Type: application/gzip` and filename `workout.json.gz`.
// There is no plaintext fallback on any failure path. All fixtures below are
// synthetic; no HealthKit, device or credential value is used.

// MARK: - Synthetic transport

private struct TransportReply: Sendable {
    var status = 200
    var body = Data()
    var contentType: String? = "application/json"
    var retryAfter: String?
    var network: URLError.Code?
    var path = "/v2/sync/workout-bundles"
}

/// Records the exact bytes the client handed to the transfer and answers from a
/// path-keyed queue, so a test can assert the real request body.
private final class BundleTransportProtocol: URLProtocol, @unchecked Sendable {
    struct Exchange: Sendable {
        let method: String
        let path: String
        let contentType: String?
        let contentEncoding: String?
        let body: Data
    }
    private static let queue = Mutex<[TransportReply]>([])
    private static let log = Mutex<[Exchange]>([])

    static var exchanges: [Exchange] { log.withLock { $0 } }
    static func reset() { queue.withLock { $0 = [] }; log.withLock { $0 = [] } }
    static func enqueue(_ replies: [TransportReply]) { queue.withLock { $0 = replies } }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let path = request.url?.path ?? ""
        let body = Self.drain(request)
        let reply = Self.queue.withLock { queued -> TransportReply in
            if let index = queued.firstIndex(where: { $0.path == path }) { return queued.remove(at: index) }
            return TransportReply(status: 404, body: Data("{\"code\":\"workout_import_not_found\"}".utf8), path: path)
        }
        Self.log.withLock {
            $0.append(Exchange(method: request.httpMethod ?? "", path: path,
                               contentType: request.value(forHTTPHeaderField: "Content-Type"),
                               contentEncoding: request.value(forHTTPHeaderField: "Content-Encoding"),
                               body: body))
        }
        if let code = reply.network {
            client?.urlProtocol(self, didFailWithError: URLError(code))
            return
        }
        var headers: [String: String] = [:]
        if let contentType = reply.contentType { headers["Content-Type"] = contentType }
        if let retryAfter = reply.retryAfter { headers["Retry-After"] = retryAfter }
        let response = HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: "HTTP/1.1", headerFields: headers)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: reply.body)
        client?.urlProtocolDidFinishLoading(self)
    }

    private static func drain(_ request: URLRequest) -> Data {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 64 << 10)
        while true {
            let read = stream.read(&buffer, maxLength: buffer.count)
            if read <= 0 { break }
            data.append(contentsOf: buffer[0..<read])
        }
        return data
    }
}

/// Counts the metric names the delivery lane emits, so the "plaintext fallback
/// is always 0" and "retry never re-reads HealthKit" invariants are asserted
/// from the same counters the app reports.
private final class MetricsRecorder: Sendable {
    private let events = Mutex<[(String, Double)]>([])
    var metrics: SyncMetrics {
        SyncMetrics { [self] name, value in events.withLock { $0.append((name, value)) } }
    }
    func values(_ name: String) -> [Double] { events.withLock { $0.filter { $0.0 == name }.map(\.1) } }
    func names() -> [String] { events.withLock { $0.map(\.0) } }
}

/// A `FileUploader` that records the identity string and the exact file bytes,
/// which is the identity/byte binding the server receipt is checked against.
private final class RecordingUploader: FileUploader, Sendable {
    struct Call: Sendable {
        let identity: String
        let method: String
        let path: String
        let contentType: String?
        let contentEncoding: String?
        let body: Data
    }
    private let calls = Mutex<[Call]>([])
    private let next = Mutex<TransportReply>(TransportReply())

    var recorded: [Call] { calls.withLock { $0 } }
    func reply(_ value: TransportReply) { next.withLock { $0 = value } }

    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let body = try Data(contentsOf: file)
        calls.withLock {
            $0.append(Call(identity: identity, method: request.httpMethod ?? "", path: request.url?.path ?? "",
                           contentType: request.value(forHTTPHeaderField: "Content-Type"),
                           contentEncoding: request.value(forHTTPHeaderField: "Content-Encoding"), body: body))
        }
        let reply = next.withLock { $0 }
        if let code = reply.network { throw URLError(code) }
        return UploadResponse(data: reply.body, status: reply.status,
                              retryAfter: reply.retryAfter.flatMap { Double($0) }, contentType: reply.contentType)
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let response = try await response(request, file: file, identity: identity)
        return (response.data, response.status, response.retryAfter)
    }
}

// MARK: - Fixtures

private struct BundleFixture: Sendable {
    let directory: URL
    let store: OutboxStore
    let api: RAIAPIClient
    let recorder: MetricsRecorder
    let id: String
    let hash: String
    let boundary: String
    let file: URL
    /// The original plain multipart file: the local authority, never uploaded.
    let original: Data
    /// The original JSON bytes the bundle hash is bound to.
    let raw: Data
}

private let transportBoundary = "rai.transport.boundary"

private func bundleACK(_ id: String, _ hash: String, samplesDeleted: Int64 = 0) -> JSON {
    .object([
        "workoutID": .string(id), "bundleSHA256": .string(hash),
        "ingestStatus": .string("committed"), "commitRevision": .int(1),
        "counts": .object([
            "workoutInserted": .bool(true),
            "samplesInserted": .int(0), "samplesUnchanged": .int(0), "samplesDeleted": .int(samplesDeleted),
            "associationsInserted": .int(0), "associationsUnchanged": .int(0), "associationsDeleted": .int(0),
            "seriesInserted": .int(0), "seriesUnchanged": .int(0),
            "routesInserted": .int(0), "routesUnchanged": .int(0), "routesDeleted": .int(0),
        ]),
        "analysisStatus": .string("queued"),
        "analysisJobIDs": .array([.string(Wire.id())]),
    ])
}

/// A pending task exactly as the pre-R4 client left it: plain multipart payload,
/// no derived wire file, no receipt.
private func makeBundleFixture(raw: Data = Data("{\"synthetic\":true,\"samples\":[]}".utf8),
                               uploader: (any FileUploader)? = nil) async throws -> BundleFixture {
    BundleTransportProtocol.reset()
    let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-gzip-transport-" + Wire.id())
    let store = try OutboxStore(directory: directory)
    let id = Wire.id(), hash = Wire.hash(raw)
    try await store.beginTask(id)
    let original = WorkoutBundleAssembler.multipart(bundle: raw, boundary: transportBoundary)
    let name = try await store.savePayload(original, suffix: "multipart.json")
    try await store.setBundle(id, file: name, sha256: hash, boundary: transportBoundary)
    let recorder = MetricsRecorder()
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [BundleTransportProtocol.self]
    let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic",
                           session: URLSession(configuration: config), uploader: uploader, metrics: recorder.metrics)
    return BundleFixture(directory: directory, store: store, api: api, recorder: recorder, id: id, hash: hash,
                         boundary: transportBoundary, file: try await store.fileURL(name),
                         original: original, raw: raw)
}

// MARK: - Wire assertions

/// Splits a multipart body into its part headers and the exact part payload.
private func filePart(_ body: Data) -> (headers: String, payload: Data)? {
    guard let separator = body.range(of: Data("\r\n\r\n".utf8)),
          let trailer = body.range(of: Data("\r\n--".utf8), options: .backwards),
          trailer.lowerBound > separator.upperBound else { return nil }
    return (String(decoding: body[body.startIndex..<separator.lowerBound], as: UTF8.self),
            body[separator.upperBound..<trailer.lowerBound])
}

private func gzipFileCount(_ directory: URL) throws -> Int {
    try FileManager.default.contentsOfDirectory(atPath: directory.path).filter { $0.hasSuffix(".multipart.gz") }.count
}

/// The single assertion set every bundle POST must satisfy.
private func expectGzipPOST(_ exchange: BundleTransportProtocol.Exchange, raw: Data, payload expected: Data? = nil) {
    #expect(exchange.method == "POST")
    #expect(exchange.path == "/v2/sync/workout-bundles")
    #expect(exchange.contentType == "multipart/form-data; boundary=" + transportBoundary)
    // A top-level Content-Encoding is rejected by the server; the compression
    // lives inside the file part only.
    #expect(exchange.contentEncoding == nil)
    let part = filePart(exchange.body)
    #expect(part?.headers.contains("Content-Disposition: form-data; name=\"file\"") == true)
    #expect(part?.headers.contains("filename=\"workout.json.gz\"") == true)
    #expect(part?.headers.contains("Content-Type: application/gzip") == true)
    #expect(part?.headers.contains("filename=\"workout.json\"") == false)
    #expect(part?.headers.contains("application/json") == false)
    // gzip magic 1f 8b.
    #expect(part.map { $0.payload.prefix(2) == Data([0x1F, 0x8B]) } == true)
    // The gzip is deterministic (mtime 0, OS 255), so byte identity proves the
    // part holds exactly gzip(original JSON) — the bytes the server decompresses
    // and hashes as `bundleSHA256`. A caller-supplied payload covers the legal
    // "same JSON, different gzip header bytes" case, where only the framing
    // (name/media type) and the identity binding are fixed.
    #expect(part?.payload == (expected ?? (try? Wire.gzip(raw))))
}

private func expectNoPlaintextPart(_ exchanges: [BundleTransportProtocol.Exchange]) {
    for exchange in exchanges where exchange.method == "POST" {
        let part = filePart(exchange.body)
        #expect(part.map { $0.payload.prefix(2) == Data([0x1F, 0x8B]) } == true)
    }
}

// MARK: - R2-F16 first upload and every retry

extension GzipTransportSuite {
    @Test func freshUploadSendsOneGzipFilePart() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        BundleTransportProtocol.enqueue([TransportReply(body: try bundleACK(fixture.id, fixture.hash).data())])

        #expect(try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id))

        let exchanges = BundleTransportProtocol.exchanges
        #expect(exchanges.count == 1)
        try expectGzipPOST(#require(exchanges.first), raw: fixture.raw)
        expectNoPlaintextPart(exchanges)
        // The local authority file is untouched.
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
        #expect(await fixture.store.task(fixture.id)?.bundleSHA256 == fixture.hash)
        #expect(await fixture.store.task(fixture.id)?.uploadReceipt != nil)
        // Counters: one gzip success, zero plaintext fallbacks, zero HK reads.
        #expect(fixture.recorder.values("bundle_gzip_post_success") == [1])
        #expect(fixture.recorder.values("bundle_gzip_post_attempt") == [1])
        #expect(fixture.recorder.values("bundle_plain_fallback") == [0])
        #expect(fixture.recorder.values("bundle_healthkit_reread") == [0])
    }

    @Test func retryAfterTransportFailureSendsTheSameSealedGzip() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        BundleTransportProtocol.enqueue([TransportReply(network: .networkConnectionLost),
                                         TransportReply(body: try bundleACK(fixture.id, fixture.hash).data())])
        let delivery = WorkoutBundleDelivery(store: fixture.store, api: fixture.api)

        await #expect(throws: UploadError.self) { try await delivery.deliver(fixture.id) }
        let sealed = try #require(await fixture.store.task(fixture.id)?.bundleWireFile)
        let sealedURL = try await fixture.store.fileURL(sealed)
        let sealedBytes = try Data(contentsOf: sealedURL)
        #expect(await fixture.store.task(fixture.id)?.bundleWireSHA256 == Wire.hash(sealedBytes))
        #expect(try gzipFileCount(fixture.directory) == 1)

        #expect(try await delivery.deliver(fixture.id, now: Date().addingTimeInterval(120)))

        let exchanges = BundleTransportProtocol.exchanges
        let posts = exchanges.filter { $0.method == "POST" }
        #expect(posts.count == 2)
        for post in posts { try expectGzipPOST(post, raw: fixture.raw) }
        // The sealed artifact is immutable and reused: identical bytes, no
        // second derivation, and no plaintext body was ever sent.
        #expect(try Data(contentsOf: sealedURL) == sealedBytes)
        #expect(try gzipFileCount(fixture.directory) == 1)
        #expect(posts[0].body == posts[1].body)
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
        #expect(fixture.recorder.values("bundle_plain_fallback") == [0, 0])
        #expect(fixture.recorder.values("bundle_healthkit_reread") == [0, 0])
        #expect(fixture.recorder.values("bundle_gzip_post_success") == [1])
    }

    @Test func retryReusesTheWireArtifactWithoutRecompressing() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        BundleTransportProtocol.enqueue([TransportReply(network: .timedOut)])
        let delivery = WorkoutBundleDelivery(store: fixture.store, api: fixture.api)
        await #expect(throws: UploadError.self) { try await delivery.deliver(fixture.id) }
        let gzipMS = try #require(await fixture.store.task(fixture.id)?.metrics["gzip_ms"])
        let deriveEvents = fixture.recorder.values("bundle_gzip_derive_ms").count

        BundleTransportProtocol.enqueue([TransportReply(network: .timedOut)])
        await #expect(throws: UploadError.self) { try await delivery.deliver(fixture.id, now: Date().addingTimeInterval(120)) }

        // A retry of an already prepared bundle compresses nothing new.
        #expect(fixture.recorder.values("bundle_gzip_derive_ms").count == deriveEvents)
        #expect(await fixture.store.task(fixture.id)?.metrics["gzip_ms"] == gzipMS)
        #expect(try gzipFileCount(fixture.directory) == 1)
    }
}

// MARK: - R2-F17 old plain pending resume

extension GzipTransportSuite {
    @Test func oldPlainPendingTaskResumesAsGzipWithoutTouchingTheOriginal() async throws {
        // Fixture is exactly what the pre-R4 client persisted: plain multipart,
        // no derived wire file, no receipt, no HealthKit access afterwards.
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        #expect(await fixture.store.task(fixture.id)?.bundleWireFile == nil)
        #expect(await fixture.store.task(fixture.id)?.bundleBoundary == transportBoundary)
        BundleTransportProtocol.enqueue([TransportReply(body: try bundleACK(fixture.id, fixture.hash).data())])

        #expect(try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id))

        let exchanges = BundleTransportProtocol.exchanges
        #expect(exchanges.count == 1)
        try expectGzipPOST(#require(exchanges.first), raw: fixture.raw)
        // Original JSON, hash, UUID and boundary are unchanged; only a derived
        // gzip artifact was added.
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
        #expect(await fixture.store.task(fixture.id)?.bundleSHA256 == fixture.hash)
        #expect(fixture.recorder.values("bundle_healthkit_reread") == [0])
        #expect(fixture.recorder.values("bundle_plain_fallback") == [0])
    }

    @Test func plaintextWireSlotIsNeverUploaded() async throws {
        // A wire slot poisoned with a plaintext multipart (as an older build
        // could have written) must be rebuilt as gzip, never sent.
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let legacy = WorkoutBundleAssembler.multipart(bundle: fixture.raw, boundary: transportBoundary)
        let legacyName = try await fixture.store.savePayload(legacy, suffix: "multipart.json")
        try await fixture.store.setBundleWire(fixture.id, bundleHash: fixture.hash, file: legacyName,
                                              sha256: Wire.hash(legacy), wireBytes: legacy.count)
        BundleTransportProtocol.enqueue([TransportReply(body: try bundleACK(fixture.id, fixture.hash).data())])

        #expect(try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id))

        let exchanges = BundleTransportProtocol.exchanges
        #expect(exchanges.count == 1)
        let sent = try #require(exchanges.first)
        try expectGzipPOST(sent, raw: fixture.raw)
        #expect(sent.body != legacy)
        #expect(fixture.recorder.values("bundle_plain_fallback") == [0])
    }

    @Test func corruptOrTruncatedSealedArtifactIsRebuiltFromTheOriginal() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        BundleTransportProtocol.enqueue([TransportReply(network: .timedOut)])
        let delivery = WorkoutBundleDelivery(store: fixture.store, api: fixture.api)
        await #expect(throws: UploadError.self) { try await delivery.deliver(fixture.id) }
        let sealed = try #require(await fixture.store.task(fixture.id)?.bundleWireFile)
        let sealedURL = try await fixture.store.fileURL(sealed)
        let good = try Data(contentsOf: sealedURL)
        // Truncated wire bytes: the cached artifact no longer hashes.
        try good.prefix(good.count / 2).write(to: sealedURL)

        BundleTransportProtocol.enqueue([TransportReply(body: try bundleACK(fixture.id, fixture.hash).data())])
        #expect(try await delivery.deliver(fixture.id, now: Date().addingTimeInterval(120)))

        let posts = BundleTransportProtocol.exchanges.filter { $0.method == "POST" }
        #expect(posts.count == 2)
        let sent = try #require(posts.last)
        try expectGzipPOST(sent, raw: fixture.raw)
        #expect(sent.body == good)
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
    }
}

// MARK: - R2-F17 deletion-only child bundle

extension GzipTransportSuite {
    @Test func deletionOnlyChildBundleIsAlsoSentAsGzip() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-gzip-transport-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        BundleTransportProtocol.reset()
        let store = try OutboxStore(directory: directory)
        let id = Wire.id()
        try await store.beginTask(id)
        try await store.acknowledgeWorkoutDeletion(id, receipt: .object([
            "workoutID": .string(id), "ingestStatus": .string("deleted"),
            "commitRevision": .int(1), "alreadyDeleted": .bool(false),
        ]))
        let scope = Scope(serviceID: "synthetic", datasetID: "synthetic-dataset",
                          datasetEpoch: "epoch-1", collectorEpoch: "collector-1")
        let raw = try WorkoutBundleAssembler.data(
            scope: scope, workoutID: id,
            workout: .object(["workoutID": .string(id), "activityType": .int(37)]),
            samples: [], series: [], routes: [], completeSampleTypes: [], completeRoutes: false,
            sampleDeletions: [.object(["sampleType": .string("HKQuantityTypeIdentifierHeartRate"),
                                       "sampleUUID": .string(Wire.id())])],
            deletionOnly: true)
        let hash = Wire.hash(raw)
        let body = WorkoutBundleAssembler.multipart(bundle: raw, boundary: transportBoundary)
        let name = try await store.savePayload(body, suffix: "multipart.json")
        try await store.setDeletedParentBundle(id, file: name, hash: hash, boundary: transportBoundary,
                                               generation: 1, deletionIDs: [], sampleCount: 1, routeCount: 0)
        let recorder = MetricsRecorder()
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [BundleTransportProtocol.self]
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic",
                               session: URLSession(configuration: config), metrics: recorder.metrics)
        BundleTransportProtocol.enqueue([TransportReply(body: try bundleACK(id, hash, samplesDeleted: 1).data())])

        #expect(try await WorkoutBundleDelivery(store: store, api: api).deliver(id))

        let exchanges = BundleTransportProtocol.exchanges
        #expect(exchanges.count == 1)
        try expectGzipPOST(#require(exchanges.first), raw: raw)
        #expect(await store.task(id)?.childDeletionOnly == true)
        #expect(await store.task(id)?.uploadReceipt != nil)
        #expect(recorder.values("bundle_plain_fallback") == [0])
    }
}

// MARK: - R2-F18 gzip failure, over-limit and capability

extension GzipTransportSuite {
    @Test func compressedOverEightMiBKeepsTheBundleAndSendsNothing() async throws {
        // Incompressible payload whose gzip exceeds the 8 MiB compressed limit.
        // Deterministic incompressible bytes (xorshift64), so the gzip output
        // stays above the 8 MiB compressed limit without any real data.
        var seed: UInt64 = 0x9E37_79B9_7F4A_7C15
        var bytes = [UInt8](repeating: 0, count: 9 << 20)
        var index = 0
        while index < bytes.count {
            seed ^= seed << 13; seed ^= seed >> 7; seed ^= seed << 17
            withUnsafeBytes(of: seed) { raw in
                for byte in raw where index < bytes.count { bytes[index] = byte; index += 1 }
            }
        }
        let fixture = try await makeBundleFixture(raw: Data(bytes))
        defer { try? FileManager.default.removeItem(at: fixture.directory) }

        let error = await #expect(throws: UploadError.self) {
            try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id)
        }
        #expect(error?.code == "bundle_compressed_too_large")
        #expect(error?.retryable == false)
        // Nothing reached the wire, the original bundle and hash survive.
        #expect(BundleTransportProtocol.exchanges.isEmpty)
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
        #expect(await fixture.store.task(fixture.id)?.bundleFile != nil)
        #expect(await fixture.store.task(fixture.id)?.bundleSHA256 == fixture.hash)
        #expect(await fixture.store.task(fixture.id)?.phase == WorkoutSyncPhase.failed)
        #expect(await fixture.store.task(fixture.id)?.errorCode == "bundle_compressed_too_large")
        // No wrongly-successful ACK was ever recorded.
        #expect(await fixture.store.task(fixture.id)?.uploadReceipt == nil)
        #expect(try gzipFileCount(fixture.directory) == 0)
        #expect(fixture.recorder.values("bundle_plain_fallback") == [0])
        #expect(fixture.recorder.values("bundle_healthkit_reread") == [0])
        #expect(fixture.recorder.values("bundle_gzip_post_attempt").isEmpty)
        // No infinite hot retry: the failed task is not claimable again.
        #expect(try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id, now: Date().addingTimeInterval(3600)) == false)
        #expect(BundleTransportProtocol.exchanges.isEmpty)
    }

    @Test func compressionRatioCapRejectsWithoutPlaintextFallback() async throws {
        // 4 MiB of one byte compresses far past the 100:1 (+64 KiB) server cap.
        let fixture = try await makeBundleFixture(raw: Data(repeating: 0x41, count: 4 << 20))
        defer { try? FileManager.default.removeItem(at: fixture.directory) }

        let error = await #expect(throws: UploadError.self) {
            try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id)
        }
        #expect(error?.code == "bundle_compression_ratio_exceeded")
        #expect(error?.retryable == false)
        #expect(BundleTransportProtocol.exchanges.isEmpty)
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
        #expect(await fixture.store.task(fixture.id)?.errorCode == "bundle_compression_ratio_exceeded")
        #expect(await fixture.store.task(fixture.id)?.uploadReceipt == nil)
        #expect(fixture.recorder.values("bundle_plain_fallback") == [0])
        #expect(fixture.recorder.values("bundle_gzip_post_attempt").isEmpty)
    }

    @Test func handshakeWithoutGzipCapabilityFailsDiagnosablyAndKeepsTheTask() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        // A handshake that ran and did not advertise capabilities.bundleGzipPart.
        BundleTransportProtocol.enqueue([
            TransportReply(body: Data("{\"protocolVersion\":3,\"capabilities\":{\"workoutBundles\":true,\"bundleProtocolVersion\":3}}".utf8), path: "/v2/handshake"),
        ])
        _ = try await fixture.api.call("GET", "/v2/handshake")
        #expect(fixture.api.bundleGzipCapability == .declared(false))

        let error = await #expect(throws: UploadError.self) {
            try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id)
        }
        #expect(error?.code == "bundle_gzip_unsupported")
        #expect(error?.retryable == false)
        #expect(BundleTransportProtocol.exchanges.map(\.path) == ["/v2/handshake"])
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
        #expect(await fixture.store.task(fixture.id)?.bundleFile != nil)
        #expect(await fixture.store.task(fixture.id)?.bundleSHA256 == fixture.hash)
        #expect(await fixture.store.task(fixture.id)?.phase == WorkoutSyncPhase.failed)
        #expect(await fixture.store.task(fixture.id)?.errorCode == "bundle_gzip_unsupported")
        #expect(await fixture.store.task(fixture.id)?.uploadReceipt == nil)
        #expect(fixture.recorder.values("bundle_plain_fallback") == [0])
        #expect(fixture.recorder.values("bundle_gzip_post_attempt").isEmpty)
    }

    @Test func declaredGzipCapabilityAllowsTheGzipUpload() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        BundleTransportProtocol.enqueue([
            TransportReply(body: Data("{\"protocolVersion\":3,\"capabilities\":{\"workoutBundles\":true,\"bundleGzipPart\":true,\"bundleProtocolVersion\":3}}".utf8), path: "/v2/handshake"),
            TransportReply(body: try bundleACK(fixture.id, fixture.hash).data()),
        ])
        _ = try await fixture.api.call("GET", "/v2/handshake")
        #expect(fixture.api.bundleGzipCapability == .declared(true))

        #expect(try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id))
        let posts = BundleTransportProtocol.exchanges.filter { $0.method == "POST" }
        #expect(posts.count == 1)
        try expectGzipPOST(#require(posts.first), raw: fixture.raw)
    }

    @Test func unobservedHandshakeStillSendsGzip() async throws {
        // No handshake observed (`unknown`): plain is never the alternative.
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        #expect(fixture.api.bundleGzipCapability == .unknown)
        BundleTransportProtocol.enqueue([TransportReply(body: try bundleACK(fixture.id, fixture.hash).data())])
        #expect(try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id))
        try expectGzipPOST(#require(BundleTransportProtocol.exchanges.first), raw: fixture.raw)
    }
}

// MARK: - R2-F20 same JSON, different gzip bytes, same identity

extension GzipTransportSuite {
    @Test func differentLegalGzipBytesKeepTheOriginalIdentityAndReceipt() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let delivery = WorkoutBundleDelivery(store: fixture.store, api: fixture.api)
        BundleTransportProtocol.enqueue([TransportReply(network: .timedOut)])
        await #expect(throws: UploadError.self) { try await delivery.deliver(fixture.id) }

        let sealed = try #require(await fixture.store.task(fixture.id)?.bundleWireFile)
        let canonical = try Wire.gzip(fixture.raw)
        #expect(try Data(contentsOf: try await fixture.store.fileURL(sealed)) ==
                WorkoutBundleAssembler.multipart(bundle: canonical, boundary: transportBoundary, compressed: true))

        // A different compressor: same JSON, different gzip header timestamp
        // (MTIME is not covered by the member CRC), so the wire bytes differ
        // while the decompressed bytes and their SHA-256 are identical.
        var alternate = canonical
        alternate[4] = 0x11; alternate[5] = 0x22; alternate[6] = 0x33; alternate[7] = 0x44
        #expect(alternate != canonical)
        let alternateWire = WorkoutBundleAssembler.multipart(bundle: Data(alternate), boundary: transportBoundary, compressed: true)
        let alternateName = try await fixture.store.savePayload(alternateWire, suffix: "multipart.gz")
        try await fixture.store.setBundleWire(fixture.id, bundleHash: fixture.hash, file: alternateName,
                                              sha256: Wire.hash(alternateWire), wireBytes: alternateWire.count)

        let originalReceipt = try bundleACK(fixture.id, fixture.hash)
        BundleTransportProtocol.enqueue([TransportReply(body: try originalReceipt.data())])
        #expect(try await delivery.deliver(fixture.id, now: Date().addingTimeInterval(120)))

        let posts = BundleTransportProtocol.exchanges.filter { $0.method == "POST" }
        #expect(posts.count == 2)
        let sent = try #require(posts.last)
        try expectGzipPOST(sent, raw: fixture.raw, payload: Data(alternate))
        // Different wire bytes on the second attempt, same identity binding.
        #expect(sent.body == alternateWire)
        #expect(sent.body != WorkoutBundleAssembler.multipart(bundle: canonical, boundary: transportBoundary, compressed: true))
        #expect(await fixture.store.task(fixture.id)?.uploadReceipt == originalReceipt)
        #expect(await fixture.store.task(fixture.id)?.bundleSHA256 == fixture.hash)
        #expect(try Data(contentsOf: fixture.file) == fixture.original)
    }

    @Test func identityIsBoundToTheDecompressedJSONBytes() async throws {
        let uploader = RecordingUploader()
        let fixture = try await makeBundleFixture(uploader: uploader)
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        uploader.reply(TransportReply(body: try bundleACK(fixture.id, fixture.hash).data()))

        #expect(try await WorkoutBundleDelivery(store: fixture.store, api: fixture.api).deliver(fixture.id))

        let call = try #require(uploader.recorded.first)
        // Identity stays `<workoutID>:<sha256(original JSON)>` even though the
        // bytes on the wire are gzip.
        #expect(call.identity == fixture.id + ":" + Wire.hash(fixture.raw))
        #expect(call.method == "POST")
        #expect(call.contentType == "multipart/form-data; boundary=" + transportBoundary)
        #expect(call.contentEncoding == nil)
        let part = try #require(filePart(call.body))
        #expect(part.payload == (try Wire.gzip(fixture.raw)))
        #expect(part.headers.contains("filename=\"workout.json.gz\""))
        #expect(part.headers.contains("Content-Type: application/gzip"))
    }
}

// MARK: - Limits and gzip member structure

extension GzipTransportSuite {
    @Test func gzipBundleLimitsMirrorTheServerContract() throws {
        #expect(Wire.GzipBundle.maxDecodedBytes == 32 << 20)
        #expect(Wire.GzipBundle.maxCompressedBytes == 8 << 20)
        #expect(Wire.GzipBundle.ratioCap == 100)
        #expect(Wire.GzipBundle.ratioSlack == 64 << 10)
        try Wire.GzipBundle.validate(decoded: 32 << 20, compressed: 8 << 20)
        try Wire.GzipBundle.validate(decoded: 32 << 20, compressed: (32 << 20) / 100)
        #expect(throws: SyncFailure.self) { try Wire.GzipBundle.validate(decoded: (32 << 20) + 1, compressed: 1 << 20) }
        #expect(throws: SyncFailure.self) { try Wire.GzipBundle.validate(decoded: 1 << 20, compressed: (8 << 20) + 1) }
        #expect(throws: SyncFailure.self) {
            try Wire.GzipBundle.validate(decoded: 100 * (64 << 10) + (64 << 10) + 1, compressed: 64 << 10)
        }
    }

    @Test func derivedArtifactIsOneGzipMemberWithCRCAndISIZE() async throws {
        let raw = Data("{\"synthetic\":\"\(String(repeating: "a", count: 4096))\"}".utf8)
        let derived = try WorkoutBundleDelivery.gzipWire(raw: raw, boundary: transportBoundary)
        let part = try #require(filePart(derived.wire))
        // Single member: the derived payload is exactly the deterministic gzip.
        #expect(part.payload == (try Wire.gzip(raw)))
        #expect(part.payload.count > 18)
        // Trailer: CRC32 (4 bytes) then ISIZE (decoded length, little endian).
        let trailer = part.payload.suffix(8)
        let isize = trailer.suffix(4).reversed().reduce(UInt32(0)) { ($0 << 8) | UInt32($1) }
        #expect(Int(isize) == raw.count)
        #expect(part.payload.prefix(10) == Data([0x1F, 0x8B, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xFF]))
        // And no second member / trailing garbage after the trailer.
        #expect(derived.wire.suffix(4) == Data("--\r\n".utf8))
        #expect(part.payload.suffix(8) != Data([0x1F, 0x8B, 0x08, 0x00]))
        #expect(WorkoutBundleDelivery.isGzipMultipart(derived.wire, boundary: transportBoundary))
        #expect(!WorkoutBundleDelivery.isGzipMultipart(WorkoutBundleAssembler.multipart(bundle: raw, boundary: transportBoundary), boundary: transportBoundary))
        #expect(!WorkoutBundleDelivery.isGzipMultipart(WorkoutBundleAssembler.multipart(bundle: raw, boundary: "other.boundary"), boundary: transportBoundary))
    }
}

@Suite(.serialized)
struct GzipTransportSuite {}

// MARK: - R2-F17 GC keeps the original and its derived gzip

extension GzipTransportSuite {
    /// Garbage collection must protect the original bundle *and* the sealed
    /// gzip artifact while the task still owns them, and may reclaim both only
    /// after a durable receipt releases the bundle.
    @Test func garbageCollectionProtectsTheOriginalAndTheDerivedGzip() async throws {
        let fixture = try await makeBundleFixture()
        defer { try? FileManager.default.removeItem(at: fixture.directory) }
        let delivery = WorkoutBundleDelivery(store: fixture.store, api: fixture.api)
        BundleTransportProtocol.enqueue([TransportReply(network: .timedOut)])
        await #expect(throws: UploadError.self) { try await delivery.deliver(fixture.id) }

        let wireName = try #require(await fixture.store.task(fixture.id)?.bundleWireFile)
        #expect(wireName.hasSuffix(".multipart.gz"))
        let wireURL = try await fixture.store.fileURL(wireName)
        #expect(FileManager.default.fileExists(atPath: wireURL.path))
        // Nothing is reclaimable yet: both files are still referenced.
        #expect(try await fixture.store.collectGarbage(dryRun: false) == 0)
        #expect(FileManager.default.fileExists(atPath: fixture.file.path))
        #expect(FileManager.default.fileExists(atPath: wireURL.path))

        BundleTransportProtocol.enqueue([TransportReply(body: try bundleACK(fixture.id, fixture.hash).data())])
        #expect(try await delivery.deliver(fixture.id, now: Date().addingTimeInterval(120)))
        #expect(await fixture.store.task(fixture.id)?.bundleWireFile == nil)
        // The receipt released both; GC may now reclaim them.
        _ = try await fixture.store.collectGarbage(dryRun: false)
        #expect(!FileManager.default.fileExists(atPath: wireURL.path))
        #expect(!FileManager.default.fileExists(atPath: fixture.file.path))
    }
}

// MARK: - R2-F18 compression failure semantics

extension GzipTransportSuite {
    /// The C shim reports a compression failure as `SyncFailure.invalid`; the
    /// delivery lane must turn that into a diagnosable, non-retryable failure so
    /// the task keeps its bundle instead of being retried hot or sent plain.
    @Test func compressionFailureMapsToADiagnosableNonRetryableError() throws {
        let cases: [(String, SyncFailure)] = [
            ("gzip_failed", .invalid("gzip_failed")),
            ("gzip_wire_invalid", .localState("gzip_wire_invalid")),
            ("bundle_too_large", .localState("bundle_too_large")),
            ("bundle_compressed_too_large", .localState("bundle_compressed_too_large")),
            ("bundle_compression_ratio_exceeded", .localState("bundle_compression_ratio_exceeded")),
            ("bundle_gzip_unsupported", .localState("bundle_gzip_unsupported")),
        ]
        for (code, failure) in cases {
            let mapped = WorkoutBundleDelivery.failure(failure)
            #expect(mapped.code == code)
            #expect(mapped.status == 422)
            #expect(mapped.retryable == false, "\(code) must not be retried hot")
        }
    }
}
