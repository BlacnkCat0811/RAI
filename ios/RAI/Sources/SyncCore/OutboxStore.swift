import Foundation

public struct Stream: Codable, Sendable {
    public let id: String, type: String, fingerprint: String
    public var deliveredAnchor: Data?
    public var nextSequence: Int64 = 1
    public var lastBatchID: String?
    public init(type: String, query: String) {
        id = Wire.id(); self.type = type; fingerprint = "sha256:" + Wire.hash(Data(query.utf8))
    }
}

public struct UploadError: Error, Codable, Sendable, Equatable {
    public let code: String
    public let status: Int
    public let retryAfter: TimeInterval?
    public let field: String?
    public let reason: String?
    public init(code: String, status: Int = 0, retryAfter: TimeInterval? = nil) { self.init(code: code, status: status, retryAfter: retryAfter, field: nil, reason: nil) }
    public init(code: String, status: Int, retryAfter: TimeInterval?, field: String?, reason: String?) { self.code = code; self.status = status; self.retryAfter = retryAfter; self.field = field; self.reason = reason }
    public var retryable: Bool { status == 0 || status == 408 || status == 429 || status >= 500 }
    public static func backoff(_ attempt: Int) -> TimeInterval { min(60, pow(2, Double(min(max(attempt - 1, 0), 6)))) }
}

public struct RequestOperation: Codable, Sendable {
    public let method: String, path: String
    public let file: String?
    public let mediaType: String
    public let expected: [String: JSON]
    public let gzip: Bool
    public init(method: String, path: String, file: String? = nil, mediaType: String = "application/json", expected: [String: JSON] = [:], gzip: Bool = false) {
        self.method = method; self.path = path; self.file = file; self.mediaType = mediaType; self.expected = expected; self.gzip = gzip
    }
    public func validate(_ response: JSON) throws {
        func contains(_ response: JSON, _ expected: JSON) -> Bool {
            if case .object(let object) = expected {
                return object.allSatisfy { contains(response[$0.key], $0.value) }
            }
            return response == expected
        }
        guard contains(response, .object(expected)) else { throw SyncFailure.receiptMismatch }
    }
}
public enum OutboxOperation: Codable, Sendable {
    case request(RequestOperation)
    case batch(Envelope, file: String)
}
public struct WorkoutReference: Codable, Sendable, Equatable {
    public let id: String, source: String
    public let current: JSON?
    public let enrichmentVersion: Int64
    /// Non-nil once the client provenance supplement (source revision/device
    /// descriptors) has a durable ACK. Provenance is never part of `current`.
    public let provenanceVersion: Int64?
    public init(id: String, source: String, current: JSON? = nil, enrichmentVersion: Int64 = 0, provenanceVersion: Int64? = nil) {
        self.id = id; self.source = source; self.current = current
        self.enrichmentVersion = enrichmentVersion; self.provenanceVersion = provenanceVersion
    }
}
public struct PendingPage: Codable, Sendable {
    public let id: String, anchorKey: String
    public let candidateAnchor: Data
    public let operations: [OutboxOperation]
    public let workouts: [WorkoutReference]
    public let deletedWorkouts: [String]
    public let deletedFacts: Set<String>
    public let createdAt: Date
    /// Nil for legacy/global-stream pages. A per-Workout task page carries the
    /// Workout UUID so one Workout's failure can never block another's package.
    public var taskID: String?
    public var isLegacy: Bool { taskID == nil }
    public var cursor = 0
    public var retryCount = 0
    public var nextAttempt = Date.distantPast
    public var error: UploadError?
    public var uploading = false
    public var waitingForFactGeneration: Int64?
    public init(anchorKey: String, candidateAnchor: Data, operations: [OutboxOperation], workouts: [WorkoutReference] = [], deletedWorkouts: [String] = [], deletedFacts: Set<String> = [], taskID: String? = nil) {
        id = Wire.id(); self.anchorKey = anchorKey; self.candidateAnchor = candidateAnchor
        self.operations = operations; self.workouts = workouts; self.deletedWorkouts = deletedWorkouts; createdAt = Date()
        self.deletedFacts = deletedFacts; self.taskID = taskID
    }
}
public struct LocalState: Codable, Sendable {
    public var version = 1
    public var scope: Scope?
    public var streams: [String: Stream] = [:]
    public var localAnchors: [String: Data] = [:]
    public var pages: [PendingPage] = []
    public var workouts: [String: WorkoutReference] = [:]
    public var deletedWorkouts: Set<String> = []
    public var tombstones: Set<String> = []
    public var cancellations: [String: String] = [:]
    public var receipts: [String: JSON] = [:]
    public var lastACK: Date?
    public var lastSync: Date?
    public var maintenanceCursor = 0
    public var readErrors: [String: String] = [:]
    public var factGeneration: Int64 = 0
    /// Local projection only. The server continues to own facts, receipts and
    /// analysis; absence means the Workout has never been queued on this device.
    public var tasks: [String: WorkoutSyncTask]?
    public var discovery: DiscoveryState?
    public var routeCaptures: [String: RouteCaptureCheckpoint]?
    public var factCaptureFiles: [String]?
    public var daily: [String: DailyFlow]?
    public var vo2: VO2Flow?
    public var vo2WakeGeneration: Int64?
    public var dailyWakePending: Bool?
    public var dailyWakeGeneration: Int64?
    /// Server-advertised daily page size from the last handshake; nil = 128.
    public var dailyMaxChanges: Int?
    // Optional for v1 state compatibility. Only fully delivered pages may mark
    // files; orphan/cancelled-page reclamation requires a separate proof.
    public var reclaimFiles: [String]?
}

// All authoritative transitions replace this one file. Payload files precede
// the state commit, so a crash can leave an orphan, never an anchor without body.
public actor OutboxStore {
    public nonisolated let directory: URL
    var state: LocalState
    private var activeBundleAttempts: Set<String> = []
    var vo2OrphansReconciled = false
    /// Observable no-op guard for state writes that a producer may repeat.
    var commitSkippedNoop = 0
    public func noopCommitSkipCount() -> Int { commitSkippedNoop }
    /// Segment ids this process is still appending to. A file with a live writer
    /// is never an orphan, so GC can prove "no in-flight writer" (§6.3).
    var activeCaptureSegments: Set<String> = []
    /// Verified seal facts per segment file name, invalidated by a size change.
    /// Sealed segments are immutable, so a hit needs no re-hash.
    var verifiedCaptureSegments: [String: VerifiedCaptureSegment] = [:]
    public init(directory: URL) throws {
        self.directory = directory
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        var dir = directory
        var values = URLResourceValues(); values.isExcludedFromBackup = true
        try dir.setResourceValues(values)
        let file = directory.appendingPathComponent("state.json")
        if FileManager.default.fileExists(atPath: file.path) {
            state = try JSONDecoder().decode(LocalState.self, from: Data(contentsOf: file))
            guard state.version == 1 else { throw SyncFailure.localState("unsupported_local_version") }
        } else { state = LocalState() }
        for index in state.pages.indices { state.pages[index].uploading = false }
    }
    public func snapshot() -> LocalState { state }
    func commit(_ next: LocalState) throws {
        let bytes = try JSONEncoder().encode(next)
        try Self.write(bytes, to: directory.appendingPathComponent("state.json"))
        state = next
    }
    public static func write(_ data: Data, to url: URL) throws {
        #if os(iOS)
        try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        #else
        try data.write(to: url, options: .atomic)
        #endif
    }
    public func savePayload(_ data: Data, suffix: String = "json") throws -> String {
        let name = Wire.id() + "." + suffix
        try Self.write(data, to: directory.appendingPathComponent(name))
        return name
    }
    public func fileURL(_ name: String) throws -> URL {
        guard name == URL(fileURLWithPath: name).lastPathComponent, !name.hasPrefix(".") else { throw SyncFailure.localState("invalid_payload_path") }
        return directory.appendingPathComponent(name)
    }
    public func bind(_ scope: Scope) throws {
        guard state.scope == nil || state.scope == scope else { throw SyncFailure.localState("dataset_or_collector_changed") }
        var next = state; next.scope = scope; try commit(next)
    }
    public func stream(type: String, query: String) throws -> Stream {
        try stream(key: type, type: type, query: query)
    }
    /// Streams are keyed by an opaque client key, not by sample type. The
    /// per-Workout task uses `workout:<uuid>:<type>`; legacy global pages keep
    /// the old type key so an upgraded app can still drain them.
    public func stream(key: String, type: String, query: String) throws -> Stream {
        if let stream = state.streams[key] { return stream }
        var next = state; let stream = Stream(type: type, query: query); next.streams[key] = stream; try commit(next); return stream
    }
    public func stage(_ page: PendingPage) throws {
        guard !state.pages.contains(where: { $0.anchorKey == page.anchorKey }) else { throw SyncFailure.localState("pending_page_exists") }
        // Validate every referenced file before exposing the candidate anchor.
        for operation in page.operations {
            let file: String?
            switch operation { case .request(let r): file = r.file; case .batch(_, let f): file = f }
            if let file { guard FileManager.default.fileExists(atPath: try fileURL(file).path) else { throw SyncFailure.localState("missing_payload") } }
        }
        var next = state; next.pages.append(page); try commit(next)
    }
    public func begin(_ id: String) throws {
        guard let index = state.pages.firstIndex(where: { $0.id == id }) else { throw SyncFailure.localState("unknown_page") }
        var next = state; next.pages[index].uploading = true; try commit(next)
    }
    public func fail(_ id: String, error: UploadError) throws {
        guard let index = state.pages.firstIndex(where: { $0.id == id }) else { return }
        var next = state
        next.pages[index].uploading = false; next.pages[index].retryCount += 1; next.pages[index].error = error
        // A 429 already tells us how long the shared bucket needs. Exponential
        // backoff on top of it would leave the bucket idle for up to a minute
        // per page and stretch an initial sync into hours.
        let delay: TimeInterval = error.status == 429
            ? max(error.retryAfter ?? 1, 1.2)
            : max(error.retryAfter ?? 0, UploadError.backoff(next.pages[index].retryCount))
        next.pages[index].nextAttempt = Date().addingTimeInterval(delay)
        // Only an unavailable parent waits for a later fact generation. Any
        // other failure (for example the shared 429 bucket) must respect the
        // normal backoff; otherwise a stale waiting marker would make this
        // page due on every pass and produce a 429 storm.
        next.pages[index].waitingForFactGeneration =
            error.code == "association_unavailable" ? state.factGeneration : nil
        try commit(next)
    }
    public func retryBlocked() throws {
        var next = state
        for i in next.pages.indices { next.pages[i].error = nil; next.pages[i].nextAttempt = .distantPast; next.pages[i].waitingForFactGeneration = nil }
        try commit(next)
    }
    /// Retry only the pages that belong to one Workout. Unlike retryBlocked(),
    /// this cannot wake another failing Workout's work.
    public func retryBlocked(taskID: String) throws {
        var next = state
        for i in next.pages.indices where next.pages[i].taskID == taskID {
            next.pages[i].error = nil; next.pages[i].nextAttempt = .distantPast; next.pages[i].waitingForFactGeneration = nil
        }
        try commit(next)
    }
    public func pages(taskID: String) -> [PendingPage] { state.pages.filter { $0.taskID == taskID } }
    public func acknowledge(_ id: String, cursor: Int, receipt: JSON) throws {
        guard let index = state.pages.firstIndex(where: { $0.id == id }), state.pages[index].cursor == cursor else { throw SyncFailure.localState("stale_operation") }
        var next = state
        let page = next.pages[index]
        guard cursor < page.operations.count else { throw SyncFailure.localState("invalid_cursor") }
        if case .batch(let envelope, _) = page.operations[cursor] {
            try envelope.validateReceipt(receipt)
            guard var stream = next.streams[page.anchorKey], stream.nextSequence == envelope.clientSeq,
                  stream.lastBatchID == envelope.previousBatchID else { throw SyncFailure.localState("local_sequence_mismatch") }
            stream.nextSequence += 1; stream.lastBatchID = envelope.batchID; next.streams[page.anchorKey] = stream
            let counts = receipt["counts"]
            if (counts["inserted"].integer ?? 0) + (counts["deleted"].integer ?? 0) + (counts["tombstoned"].integer ?? 0) > 0 { next.factGeneration += 1 }
        } else if case .request(let request) = page.operations[cursor] { try request.validate(receipt) }
        next.pages[index].cursor += 1; next.pages[index].uploading = false; next.pages[index].error = nil
        next.pages[index].waitingForFactGeneration = nil
        next.receipts[page.id + ":" + String(cursor)] = receipt; next.lastACK = Date()
        if next.pages[index].cursor == page.operations.count {
            finish(page, in: &next); next.pages.remove(at: index)
            markDelivered(page, in: &next)
            // An explicit HealthKit Workout deletion invalidates its pending
            // routes and associations. Preserve files; do not send stale work.
            for id in page.deletedWorkouts {
                next.pages.removeAll { $0.anchorKey == "route:" + id || $0.anchorKey == "enrichment:" + id || $0.anchorKey.hasPrefix("association:" + id + ":") }
                next.readErrors = next.readErrors.filter { $0.key != "route:" + id && $0.key != "enrichment:" + id && !$0.key.hasPrefix("association:" + id + ":") }
            }
            let cancelled = next.pages.filter { candidate in
                candidate.anchorKey.hasPrefix("association:") && candidate.operations.contains { operation in
                    guard case .request(let request) = operation, let type = request.expected["sampleType"]?.text,
                          let id = request.expected["sampleUUID"]?.text else { return false }
                    return next.tombstones.contains(type + ":" + id)
                }
            }
            for candidate in cancelled { next.cancellations[candidate.id] = "source_fact_deleted; anchor retained for rediscovery" }
            let ids = Set(cancelled.map(\.id)); next.pages.removeAll { ids.contains($0.id) }
        }
        try commit(next)
    }
    /// Persist verified series successes, including out-of-order groups. Move
    /// the operation cursor only across a contiguous prefix of receipts. A
    /// trailing page-receipt request must still succeed before anchor promotion.
    public func acknowledgeSeries(_ id: String, receipts: [Int: JSON]) throws {
        guard let index = state.pages.firstIndex(where: { $0.id == id }) else { throw SyncFailure.localState("unknown_page") }
        let page = state.pages[index]
        var next = state
        for (cursor, receipt) in receipts {
            guard cursor >= page.cursor, cursor < page.operations.count,
                  case .request(let request) = page.operations[cursor],
                  request.path == "/v2/sync/series" || request.path.hasPrefix("/v2/sync/series/") else {
                throw SyncFailure.localState("invalid_series_receipt")
            }
            try request.validate(receipt)
            next.receipts[page.id + ":" + String(cursor)] = receipt
        }
        while next.pages[index].cursor < page.operations.count,
              next.receipts[page.id + ":" + String(next.pages[index].cursor)] != nil {
            next.pages[index].cursor += 1
        }
        next.pages[index].uploading = false
        next.pages[index].error = nil
        next.pages[index].waitingForFactGeneration = nil
        next.lastACK = Date()
        if next.pages[index].cursor == page.operations.count {
            finish(page, in: &next)
            next.pages.remove(at: index)
            markDelivered(page, in: &next)
        }
        try commit(next)
    }
    public func finishEmptyPage(_ page: PendingPage) throws {
        guard page.operations.isEmpty else { throw SyncFailure.localState("nonempty_page") }
        var next = state; finish(page, in: &next); try commit(next)
    }
    private func markDelivered(_ page: PendingPage, in next: inout LocalState) {
        var marked = next.reclaimFiles ?? []
        for (cursor, operation) in page.operations.enumerated() {
            let identity = page.id + ":" + String(cursor)
            switch operation {
            case .request(let request): if let file = request.file { marked.append(file) }
            case .batch(_, let file): marked.append(file)
            }
            marked.append(Wire.hash(Data(identity.utf8)) + ".transfer.json")
            next.receipts.removeValue(forKey: identity)
        }
        next.reclaimFiles = Array(Set(marked)).sorted()
    }
    // Mark was persisted atomically with delivered anchor and page removal.
    // Recheck pending references, sweep at most limit entries, then persist the
    // remaining queue. Death before/after unlink is idempotent on reopening.
    @discardableResult public func collectGarbage(limit: Int = 128, dryRun: Bool = true) throws -> Int {
        guard (1...512).contains(limit) else { throw SyncFailure.localState("invalid_gc_limit") }
        let candidates = Array((state.reclaimFiles ?? []).prefix(limit))
        var referenced: Set<String> = []
        for page in state.pages {
            for (cursor, operation) in page.operations.enumerated() {
                switch operation {
                case .request(let request): if let file = request.file { referenced.insert(file) }
                case .batch(_, let file): referenced.insert(file)
                }
                referenced.insert(Wire.hash(Data((page.id + ":" + String(cursor)).utf8)) + ".transfer.json")
            }
        }
        referenced.formUnion(state.factCaptureFiles ?? [])
        referenced.formUnion((state.daily ?? [:]).values.compactMap { $0.pending?.file })
        referenced.formUnion([state.vo2?.pending?.file, state.vo2?.blocked?.file].compactMap { $0 })
        for capture in (state.routeCaptures ?? [:]).values { referenced.insert(capture.file) }
        for event in (state.discovery?.events ?? [:]).values {
            if let file = event.archiveFile { referenced.insert(file) }
            // A segment is referenced as a whole: one committed fact keeps every
            // record in it alive, so a shared segment is never partially swept.
            if let file = event.segmentFile { referenced.insert(file) }
        }
        for task in (state.tasks ?? [:]).values {
            if let file = task.bundleFile { referenced.insert(file) }
            if let file = task.bundleWireFile { referenced.insert(file) }
        }
        guard candidates.allSatisfy({ !referenced.contains($0) }) else { throw SyncFailure.localState("gc_referenced_file") }
        for file in candidates {
            guard file.hasSuffix(".json") || file.hasSuffix(".gz") || file.hasSuffix(".completeness.archive") ||
                    file.hasSuffix(CaptureSegmentFormat.suffix) || file.hasSuffix(CaptureSegmentFormat.temporarySuffix) else { throw SyncFailure.localState("gc_unmanaged_file") }
            let url = try fileURL(file)
            guard file != "state.json" else { throw SyncFailure.localState("gc_authority_file") }
            // A file with a live writer is not an orphan, even when no committed
            // event references it yet. Never unlink under an in-flight capture.
            if let id = Self.captureSegmentID(file) { guard !activeCaptureSegments.contains(id) else { throw SyncFailure.localState("gc_inflight_file") } }
            if FileManager.default.fileExists(atPath: url.path) {
                let values = try url.resourceValues(forKeys: [.isRegularFileKey, .isSymbolicLinkKey])
                guard values.isRegularFile == true, values.isSymbolicLink != true else { throw SyncFailure.localState("gc_unsafe_file") }
            }
        }
        if dryRun { return candidates.count }
        for file in candidates {
            let url = try fileURL(file)
            if FileManager.default.fileExists(atPath: url.path) { try FileManager.default.removeItem(at: url) }
        }
        if !candidates.isEmpty {
            var next = state; next.reclaimFiles = Array((next.reclaimFiles ?? []).dropFirst(candidates.count)); try commit(next)
        }
        return candidates.count
    }
    private func finish(_ page: PendingPage, in next: inout LocalState) {
        next.tombstones.formUnion(page.deletedFacts)
        if var stream = next.streams[page.anchorKey] { stream.deliveredAnchor = page.candidateAnchor; next.streams[page.anchorKey] = stream }
        else { next.localAnchors[page.anchorKey] = page.candidateAnchor }
        for workout in page.workouts where !next.deletedWorkouts.contains(workout.id) { next.workouts[workout.id] = workout }
        for id in page.deletedWorkouts { next.deletedWorkouts.insert(id); next.workouts.removeValue(forKey: id) }
    }
    public func synced() throws { var next = state; next.lastSync = Date(); try commit(next) }
    public func advanceMaintenance(_ count: Int) throws { var next = state; next.maintenanceCursor += count; try commit(next) }
    public func readResult(key: String, error: String?) throws { var next = state; next.readErrors[key] = error; try commit(next) }
}
