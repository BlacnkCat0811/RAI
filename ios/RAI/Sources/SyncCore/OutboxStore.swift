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
    public var retainedBundles: [String: WorkoutSyncTask]?
    public var factCaptureFiles: [String]?
    public var daily: [String: DailyFlow]?
    public var vo2: VO2Flow?
    public var workoutCompleteness: WorkoutCompletenessState?
    public var vo2WakeGeneration: Int64?
    public var dailyWakePending: Bool?
    public var dailyWakeGeneration: Int64?
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
    var activeCompletenessManifest: String?
    var vo2OrphansReconciled = false
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
    /// Upgrade repair for pages staged by the old client that put provenance
    /// into the immutable Workout event. A page can only be discarded before
    /// its first batch was acknowledged; the server never committed that
    /// event, so the task can rebuild the frozen payload without losing facts.
    @discardableResult public func discardRejectedTaskPages(taskID: String, errorCode: String? = nil) throws -> Int {
        var next = state
        var discardIDs: Set<String> = []
        for page in next.pages where page.taskID == taskID && (errorCode == nil || page.error?.code == errorCode) {
            guard let firstBatch = page.operations.firstIndex(where: { operation in
                if case .batch = operation { return true }
                return false
            }), page.cursor <= firstBatch else { continue }
            discardIDs.insert(page.id)
            var reclaim = next.reclaimFiles ?? []
            for (cursor, operation) in page.operations.enumerated() {
                switch operation {
                case .request(let request): if let file = request.file { reclaim.append(file) }
                case .batch(_, let file): reclaim.append(file)
                }
                reclaim.append(Wire.hash(Data((page.id + ":" + String(cursor)).utf8)) + ".transfer.json")
                next.receipts.removeValue(forKey: page.id + ":" + String(cursor))
            }
            next.reclaimFiles = Array(Set(reclaim)).sorted()
        }
        guard !discardIDs.isEmpty else { return 0 }
        next.pages.removeAll { discardIDs.contains($0.id) }
        try commit(next)
        return discardIDs.count
    }
    /// Persist a locally observed Workout reference after a supplement ACK.
    /// It never creates a fact by itself; it only lets the client remember
    /// that another supplement (for example provenance) is already durable.
    public func recordWorkout(_ reference: WorkoutReference) throws {
        guard !state.deletedWorkouts.contains(reference.id) else { return }
        var next = state
        next.workouts[reference.id] = reference
        try commit(next)
    }
    /// Upgrade migration: a legacy page was created by the old global path.
    /// When its key names exactly one Workout relationship, the per-Workout
    /// task may adopt it so the page cannot block or be stranded outside the
    /// task. Page bytes and operation identities are not changed.
    public func adoptLegacyPage(anchorKey: String, taskID: String) throws -> Bool {
        guard let index = state.pages.firstIndex(where: { $0.anchorKey == anchorKey && $0.taskID == nil }) else { return false }
        var next = state
        next.pages[index].taskID = taskID
        try commit(next)
        return true
    }
    public func allTasks() -> [WorkoutSyncTask] { (state.tasks ?? [:]).values.sorted { $0.createdAt < $1.createdAt } }
    public func task(_ id: String) -> WorkoutSyncTask? { state.tasks?[id] }
    public func beginTask(_ id: String, resetBundle: Bool = true) throws {
        var next = state
        var tasks = next.tasks ?? [:]
        if var task = tasks[id] {
            guard task.phase != .deleted, task.errorCode != "workout_deleted" else { throw SyncFailure.localState("workout_deleted") }
            // Unacknowledged source bytes survive even an explicit rescan.
            if task.bundleFile == nil, resetBundle { task.uploadReceipt = nil; task.bundleSHA256 = nil }
            if task.bundleFile != nil {
                task.phase = .uploading
                if task.lastUploadError?.retryable == false { task.lastUploadError = nil; task.errorCode = nil }
            } else {
                task.phase = .preparing; task.errorCode = nil; task.stagedAll = false
            }
            task.updatedAt = Date()
            tasks[id] = task
        } else {
            tasks[id] = WorkoutSyncTask(workoutID: id)
        }
        next.tasks = tasks; try commit(next)
    }
    public func setBundle(_ id: String, file: String, sha256: String, boundary: String, sampleDeletions: Int = 0, routeDeletions: Int = 0, discoveryGeneration: Int64? = nil, deletionEventIDs: [String] = [], pendingReadKeys: [String] = []) throws {
        guard let task = state.tasks?[id], task.phase != .deleted,
              task.bundleFile == nil || (task.bundleFile == file && task.bundleSHA256 == sha256 && task.bundleBoundary == boundary) else {
            throw SyncFailure.localState("pending_bundle_immutable")
        }
        try updateTask(id) { task in
            task.bundleFile = file; task.bundleSHA256 = sha256; task.bundleBoundary = boundary
            task.submittedSampleDeletions = sampleDeletions; task.submittedRouteDeletions = routeDeletions; task.discoveryGeneration = discoveryGeneration
            task.bundleDeletionEventIDs = deletionEventIDs; task.pendingReadKeys = pendingReadKeys
            task.uploadReceipt = nil; task.acknowledgedAt = nil; task.stagedAll = false
            task.attemptCount = nil; task.nextAttemptAt = nil; task.lastUploadError = nil
        }
    }
    public func setBundleWire(_ id: String, bundleHash: String, file: String, sha256: String, wireBytes: Int = 0, gzipMS: Double = 0) throws {
        guard let task = state.tasks?[id], task.bundleFile != nil, task.bundleSHA256 == bundleHash,
              task.uploadReceipt == nil else { throw SyncFailure.localState("bundle_not_prepared") }
        var next = state
        if let old = task.bundleWireFile, old != file { next.reclaimFiles = Array(Set((next.reclaimFiles ?? []) + [old])).sorted() }
        next.tasks?[id]?.bundleWireFile = file; next.tasks?[id]?.bundleWireSHA256 = sha256
        next.tasks?[id]?.metrics["gzip_multipart_bytes"] = Double(wireBytes)
        next.tasks?[id]?.metrics["gzip_ms"] = gzipMS
        try commit(next)
    }
    public func setDeletedParentBundle(_ id: String, file: String, hash: String, boundary: String, generation: Int64, deletionIDs: [String], sampleCount: Int, routeCount: Int) throws {
        guard let task = state.tasks?[id], task.phase == .deleted, let deletion = task.deletionReceipt, task.bundleFile == nil, sampleCount + routeCount > 0 else { throw SyncFailure.localState("deleted_parent_bundle_not_allowed") }
        try Self.validateDeletion(deletion, id: id)
        guard FileManager.default.fileExists(atPath: try fileURL(file).path) else { throw SyncFailure.localState("missing_payload") }
        try updateTask(id) {
            $0.bundleFile = file; $0.bundleSHA256 = hash; $0.bundleBoundary = boundary; $0.childDeletionOnly = true
            $0.uploadReceipt = nil; $0.discoveryGeneration = generation; $0.bundleDeletionEventIDs = deletionIDs
            $0.submittedSampleDeletions = sampleCount; $0.submittedRouteDeletions = routeCount
            $0.attemptCount = nil; $0.nextAttemptAt = nil; $0.lastUploadError = nil; $0.errorCode = nil; $0.pendingReadKeys = []
        }
    }
    public func releaseBundle(_ id: String) throws {
        var next = state
        var tasks = next.tasks ?? [:]
        guard var task = tasks[id] else { return }
        if task.childDeletionOnly == true && task.bundleFile != nil && task.uploadReceipt == nil { throw SyncFailure.localState("bundle_not_acknowledged") }
        if let receipt = task.uploadReceipt, let hash = task.bundleSHA256 {
            try WorkoutBundleACK.validate(receipt, workoutID: id, bundleHash: hash, sampleDeletions: task.submittedSampleDeletions ?? 0, routeDeletions: task.submittedRouteDeletions ?? 0)
        } else if let receipt = task.deletionReceipt {
            try Self.validateDeletion(receipt, id: id)
        } else { throw SyncFailure.localState("bundle_not_acknowledged") }
        releaseBundleFile(&task, in: &next)
        tasks[id] = task
        next.tasks = tasks
        try commit(next)
    }
    /// Claim and retry time commit before network I/O. In-process claims are
    /// shared by all workers; restart releases claims but preserves the delay.
    public func claimBundle(_ id: String, now: Date = Date()) throws -> WorkoutSyncTask? {
        guard let task = state.tasks?[id], task.bundleFile != nil, task.bundleSHA256 != nil,
              task.bundleBoundary != nil, task.uploadReceipt == nil, (task.deletionReceipt == nil || task.childDeletionOnly == true),
              (task.phase != .deleted || task.childDeletionOnly == true), task.phase != .failed,
              task.childDeletionOnly != true || task.lastUploadError?.retryable != false,
              state.discovery?.work[id]?.pendingDelete != true,
              !activeBundleAttempts.contains(id), (task.nextAttemptAt ?? .distantPast) <= now else { return nil }
        try updateTask(id) {
            $0.phase = $0.childDeletionOnly == true ? .deleted : .uploading; $0.attemptCount = min(($0.attemptCount ?? 0) + 1, 1_000_000)
            $0.nextAttemptAt = now.addingTimeInterval(60)
        }
        activeBundleAttempts.insert(id)
        return state.tasks?[id]
    }
    public func endBundleAttempt(_ id: String) { activeBundleAttempts.remove(id) }
    public func failBundle(_ id: String, error: UploadError, now: Date = Date()) throws {
        guard let task = state.tasks?[id], task.uploadReceipt == nil, (task.deletionReceipt == nil || task.childDeletionOnly == true), (task.phase != .deleted || task.childDeletionOnly == true) else { return }
        try updateTask(id) { task in
            task.phase = task.childDeletionOnly == true ? .deleted : (error.retryable ? .uploading : .failed)
            task.errorCode = error.code; task.lastUploadError = error
            let delay = min(900, pow(2, Double(min(max((task.attemptCount ?? 1) - 1, 0), 10))))
            task.nextAttemptAt = now.addingTimeInterval(max(delay, error.retryAfter ?? 0))
        }
    }
    /// Handshake/authentication can fail before a per-task claim. Persist that
    /// shared failure too, so foreground/relaunch does not bypass backoff.
    public func deferBundleRecovery(_ error: any Error, now: Date = Date()) throws {
        let failure = WorkoutBundleDelivery.failure(error)
        var next = state
        for (id, var task) in next.tasks ?? [:] where task.bundleFile != nil && task.uploadReceipt == nil && (task.deletionReceipt == nil || task.childDeletionOnly == true) && task.phase != .failed && (task.phase != .deleted || task.childDeletionOnly == true) {
            task.attemptCount = min((task.attemptCount ?? 0) + 1, 1_000_000)
            let delay = min(900, pow(2, Double(min(max((task.attemptCount ?? 1) - 1, 0), 10))))
            task.nextAttemptAt = max(task.nextAttemptAt ?? .distantPast, now.addingTimeInterval(max(delay, failure.retryAfter ?? 0)))
            task.phase = task.childDeletionOnly == true ? .deleted : (failure.retryable ? .uploading : .failed)
            task.errorCode = failure.code; task.lastUploadError = failure; task.updatedAt = now
            next.tasks?[id] = task
        }
        Self.deferDaily(&next, error: error, now: now)
        Self.deferVO2(&next, error: error, now: now)
        try commit(next)
    }
    public func acknowledgeBundle(_ id: String, receipt: JSON) throws {
        guard let task = state.tasks?[id], let hash = task.bundleSHA256 else { throw SyncFailure.receiptMismatch }
        if task.childDeletionOnly == true {
            guard let deletion = task.deletionReceipt, task.phase == .deleted else { throw SyncFailure.receiptMismatch }
            try Self.validateDeletion(deletion, id: id)
        } else {
            guard task.deletionReceipt == nil, task.phase != .deleted, task.errorCode != "workout_deleted" else { throw SyncFailure.receiptMismatch }
        }
        try WorkoutBundleACK.validate(receipt, workoutID: id, bundleHash: hash, sampleDeletions: task.submittedSampleDeletions ?? 0, routeDeletions: task.submittedRouteDeletions ?? 0)
        if let saved = task.uploadReceipt {
            guard saved == receipt else { throw SyncFailure.receiptMismatch }
            return
        }
        // Receipt, acknowledged phase and GC authorization commit atomically.
        var next = state; var acknowledged = task
        acknowledged.phase = task.childDeletionOnly == true ? .deleted : .acknowledged; acknowledged.uploadReceipt = receipt
        acknowledged.acknowledgedAt = Date(); acknowledged.updatedAt = Date()
        acknowledged.stagedAll = true; acknowledged.errorCode = nil
        acknowledged.lastUploadError = nil; acknowledged.nextAttemptAt = nil
        releaseBundleFile(&acknowledged, in: &next)
        if var work = next.discovery?.work[id] {
            if (task.pendingReadKeys ?? []).isEmpty {
                work.deliveredGeneration = max(work.deliveredGeneration, task.discoveryGeneration ?? 0)
                work.error = nil; work.attempt = 0; work.nextAttemptAt = nil
                for (eventID,var event) in next.discovery?.events ?? [:] where event.workoutID == id && (event.generation ?? 0) <= work.deliveredGeneration {
                    event.status = "delivered"; next.discovery?.events[eventID] = event
                }
            } else {
                work.error = "partial_read_pending"; work.attempt += 1
                work.nextAttemptAt = Date().addingTimeInterval(min(900, 30 * pow(2, Double(min(work.attempt, 5)))))
            }
            next.discovery?.work[id] = work
        }
        for key in acknowledged.coverage.keys { acknowledged.coverage[key]?.serverCommitted = true }
        next.tasks?[id] = acknowledged; next.lastACK = Date()
        try commit(next)
    }
    private static func validateDeletion(_ receipt: JSON, id: String) throws {
        guard receipt["workoutID"].text == id, receipt["ingestStatus"].text == "deleted",
              let revision = receipt["commitRevision"].integer, revision > 0,
              case .bool = receipt["alreadyDeleted"] else { throw SyncFailure.receiptMismatch }
    }
    public func acknowledgeWorkoutDeletion(_ id: String, receipt: JSON) throws {
        try Self.validateDeletion(receipt, id: id)
        var next = state
        var task = next.tasks?[id] ?? WorkoutSyncTask(workoutID: id)
        if next.tasks == nil { next.tasks = [:] }
        task.phase = .deleted; task.deletionReceipt = receipt; task.errorCode = nil
        task.uploadReceipt = nil; task.acknowledgedAt = nil; task.updatedAt = Date()
        releaseBundleFile(&task, in: &next)
        next.tasks?[id] = task; next.deletedWorkouts.insert(id)
        if var work = next.discovery?.work[id] {
            work.pendingDelete = false
            let childDeletes = (next.discovery?.events ?? [:]).values.contains { $0.workoutID == id && $0.deleted && $0.type != "HKWorkoutTypeIdentifier" && $0.status != "delivered" }
            if childDeletes { work.selected = true }
            else { work.deliveredGeneration = work.generation }
            next.discovery?.work[id] = work
            for (eventID,var event) in next.discovery?.events ?? [:] where event.workoutID == id && (!event.deleted || event.type == "HKWorkoutTypeIdentifier") {
                event.status = event.deleted ? "delivered" : "suppressed_by_workout_delete"; next.discovery?.events[eventID] = event
            }
        }
        try commit(next)
    }
    public func setWorkoutAnchor(_ id: String, anchor: Data?) throws {
        try updateTask(id) { $0.workoutAnchor = anchor }
    }
    private func releaseBundleFile(_ task: inout WorkoutSyncTask, in next: inout LocalState) {
        guard let file = task.bundleFile else { return }
        var reclaim = next.reclaimFiles ?? []
        reclaim.append(file)
        if let wire = task.bundleWireFile { reclaim.append(wire) }
        next.reclaimFiles = Array(Set(reclaim)).sorted()
        task.bundleFile = nil
        task.bundleBoundary = nil
        task.bundleWireFile = nil; task.bundleWireSHA256 = nil
    }
    /// Protocol 3 has no page queue. Any pages left by a pre-bundle client
    /// cannot be drained through the removed legacy endpoints; retain this
    /// explicit one-time cleanup so their files and receipts are not stranded.
    @discardableResult public func purgeLegacyPages() throws -> Int {
        guard !state.pages.isEmpty else { return 0 }
        var next = state
        var reclaim = next.reclaimFiles ?? []
        for page in next.pages {
            for (cursor, operation) in page.operations.enumerated() {
                switch operation {
                case .request(let request): if let file = request.file { reclaim.append(file) }
                case .batch(_, let file): reclaim.append(file)
                }
                reclaim.append(Wire.hash(Data((page.id + ":" + String(cursor)).utf8)) + ".transfer.json")
                next.receipts.removeValue(forKey: page.id + ":" + String(cursor))
            }
        }
        let removed = next.pages.count
        next.pages.removeAll()
        next.reclaimFiles = Array(Set(reclaim)).sorted()
        try commit(next)
        return removed
    }
    public func updateTask(_ id: String, _ mutate: (inout WorkoutSyncTask) -> Void) throws {
        var next = state
        var tasks = next.tasks ?? [:]
        guard var task = tasks[id] else { throw SyncFailure.localState("unknown_workout_task") }
        mutate(&task); task.updatedAt = Date()
        task.missing = task.coverage.filter { !$0.value.isAvailable }.map(\.key).sorted()
        tasks[id] = task
        next.tasks = tasks; try commit(next)
    }
    public func setTaskCoverage(_ id: String, coverage: [String: WorkoutCoverage]) throws {
        try updateTask(id) { $0.coverage = coverage }
    }
    public func setTaskMetrics(_ id: String, metrics: [String: Double]) throws {
        try updateTask(id) { $0.metrics.merge(metrics) { _, new in new } }
    }
    /// Startup reconciliation: a task with no durable page left and never
    /// reached the server is not silently treated as synced. It becomes a
    /// retryable failure so the user can explicitly retry. A task whose only
    /// remaining pages are permanently rejected (for example 409) is settled
    /// as FAILED instead of being projected as "uploading" forever.
    public func reconcileTasks() throws {
        guard var tasks = state.tasks else { return }
        var changed = false
        for (id, var task) in tasks {
            if task.bundleFile != nil, task.uploadReceipt == nil, task.deletionReceipt == nil,
               (task.phase != .failed || ["interrupted", "workout_import_not_found", "receipt_mismatch", "invalid_response"].contains(task.errorCode ?? "")), task.phase != .deleted {
                task.phase = .uploading; tasks[id] = task; changed = true
            }
            guard [.preparing, .uploading].contains(task.phase) else { continue }
            let pages = state.pages.filter { $0.taskID == id }
            if let failure = pages.compactMap(\.error).first(where: { !$0.retryable }) {
                task.phase = .failed; task.errorCode = failure.code; task.updatedAt = Date()
                tasks[id] = task; changed = true
                continue
            }
            if !pages.isEmpty { continue }
            // Protocol 3 tasks are resumable only if their exact bundle file
            // survived. Without it, do not silently read HealthKit on recovery.
            if task.bundleFile == nil || task.bundleSHA256 == nil {
                task.phase = .failed
                task.errorCode = "interrupted"
                task.updatedAt = Date()
                tasks[id] = task
                changed = true
            } else if task.phase == .preparing {
                task.phase = .uploading
                task.updatedAt = Date()
                tasks[id] = task
                changed = true
            }
        }
        if changed { var next = state; next.tasks = tasks; try commit(next) }
    }
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
            completeTaskIfStaged(page.taskID, in: &next)
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
    private func completeTaskIfStaged(_ taskID: String?, in next: inout LocalState) {
        guard let taskID, var task = next.tasks?[taskID], task.stagedAll == true,
              [.preparing, .uploading].contains(task.phase),
              !next.pages.contains(where: { $0.taskID == taskID }) else { return }
        task.phase = .acknowledged
        if task.acknowledgedAt == nil { task.acknowledgedAt = Date() }
        task.updatedAt = Date()
        next.tasks?[taskID] = task
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
        referenced.formUnion([state.workoutCompleteness?.cycle?.pendingFile].compactMap { $0 })
        referenced.formUnion(state.workoutCompleteness?.cycle?.seriesFiles.keys.map { $0 } ?? [])
        for captures in state.workoutCompleteness?.cycle?.captures.values ?? Dictionary<String, [WorkoutCompletenessCapture]>().values {
            referenced.formUnion(captures.map(\.archiveFile))
        }
        for capture in (state.routeCaptures ?? [:]).values { referenced.insert(capture.file) }
        for event in (state.discovery?.events ?? [:]).values {
            if let file = event.archiveFile { referenced.insert(file) }
            // A segment is referenced as a whole: one committed fact keeps every
            // record in it alive, so a shared segment is never partially swept.
            if let file = event.segmentFile { referenced.insert(file) }
        }
        for task in Array((state.tasks ?? [:]).values) + Array((state.retainedBundles ?? [:]).values) {
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
