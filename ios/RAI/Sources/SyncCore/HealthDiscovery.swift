import Foundation

/// Query identity includes the exact scope and contract. Never reuse an anchor
/// for another predicate or silently reset an invalid/old contract.
public struct DiscoveryQuery: Codable, Sendable, Equatable {
    public var type: String
    public var workoutID: String?
    public var contract = 1
    public var initialization = "all_visible_history"
    public init(type: String, workoutID: String? = nil) { self.type = type; self.workoutID = workoutID }
    public var key: String { "discovery/v\(contract)/\(type)/\(workoutID ?? "all")/\(initialization)" }
}
public struct DiscoveryEvent: Codable, Sendable, Equatable {
    public let id: String
    public let type: String
    public let uuid: String
    public let deleted: Bool
    public let archiveFile: String?
    public let archiveSHA256: String?
    public var generation: Int64?
    public var workoutID: String?
    public var status: String
    // P2 batch capture. A fact's archive lives either in its own file
    // (`archiveFile`, legacy) or inside a sealed segment at an offset. All three
    // fields are optional with defaults, so a `state.json` written before P2
    // still decodes and a legacy event keeps its original verification path.
    public var segmentFile: String? = nil
    public var segmentOffset: Int64? = nil
    public var segmentLength: Int64? = nil
    /// Monotonic capture ordinal assigned by the single writer when this event
    /// is committed to the journal. A bundle pins the highest ordinal it really
    /// encoded per type (`CaptureManifest.cursors`), so a fact captured while a
    /// bundle is being built keeps a higher ordinal and is never mistaken for
    /// delivered work. Optional so a pre-R1 `state.json` still decodes.
    public var ordinal: Int64? = nil
    public init(type: String, uuid: String, deleted: Bool, archiveFile: String? = nil, archiveHash: String = "", workoutID: String? = nil, identityHash: String? = nil,
                segmentFile: String? = nil, segmentOffset: Int64? = nil, segmentLength: Int64? = nil) {
        self.type = type; self.uuid = uuid; self.deleted = deleted; self.archiveFile = archiveFile; self.archiveSHA256 = archiveHash.isEmpty ? nil : archiveHash; self.workoutID = workoutID
        self.segmentFile = segmentFile; self.segmentOffset = segmentOffset; self.segmentLength = segmentLength
        id = Wire.hash(Data((type + ":" + uuid + ":" + (deleted ? "delete" : "upsert:" + (identityHash ?? archiveHash))).utf8))
        status = workoutID == nil ? "unresolved" : "pending"
    }
    public var factKey: String { type + ":" + uuid }
    /// True when the archive bytes live inside a sealed segment.
    public var hasSegmentReference: Bool { segmentFile != nil }
}
public struct DiscoveryBatch: Codable, Sendable {
    public let id: String
    public let previousAnchor: Data?
    public let anchor: Data
    public let events: [String]
    public let hasMore: Bool
}
public struct DiscoveryFlow: Codable, Sendable {
    public let query: DiscoveryQuery
    public var anchor: Data?
    public var staged: DiscoveryBatch?
    public var lastProcessedAt: Date?
    public var hasMore = true
    public var error: String?
    public init(query: DiscoveryQuery) { self.query = query }
}
public struct DiscoveryWork: Codable, Sendable {
    public var generation: Int64 = 0
    public var preparedGeneration: Int64 = 0
    public var deliveredGeneration: Int64 = 0
    public var pendingDelete = false
    public var selected = false
    public var error: String?
    public var nextAttemptAt: Date?
    public var attempt = 0

    /// A partial-read ACK is retryable only a bounded number of times. A new
    /// HealthKit generation always reopens work; source-policy review does not
    /// turn into an endless autonomous loop.
    public var hasAutomaticDeliveryWork: Bool {
        pendingDelete || (selected && ((generation > deliveredGeneration && (error == nil || attempt <= 3)) ||
            (error == "partial_read_pending" && attempt <= 3)))
    }
    public func needsDelivery(now: Date = Date()) -> Bool {
        hasAutomaticDeliveryWork && (nextAttemptAt ?? .distantPast) <= now
    }
}
public struct DiscoveryState: Codable, Sendable {
    public var flows: [String: DiscoveryFlow] = [:]
    public var events: [String: DiscoveryEvent] = [:]
    public var parents: [String: String] = [:]
    public var work: [String: DiscoveryWork] = [:]
    public var workoutArchives: [String: String] = [:]
    public var scanCursor = 0
    public var cycleRemaining = 0
    public var recoveryOverrides: [String: DiscoveryQuery]?
    /// Single-writer capture sequence. Only `OutboxStore` advances it, inside
    /// the same commit that appends the event, so ordinals are gap-free and
    /// strictly ordered by commit.
    public var sequence: Int64 = 0
    public init() {}
    /// Advance the capture sequence and return the new ordinal.
    mutating func nextOrdinal() -> Int64 { sequence += 1; return sequence }
}

extension OutboxStore {
    /// A fact's archive must exist before its journal entry can be committed.
    /// A segment reference (P2) is verified by `verifyCaptureSegmentRecord`;
    /// a legacy `archiveFile` keeps exactly its original logic, so old and new
    /// events can coexist in one state and one batch.
    func verifyDiscoveryArchive(_ event: DiscoveryEvent) throws {
        if let file = event.archiveFile {
            let url = try fileURL(file)
            guard FileManager.default.fileExists(atPath: url.path) else { throw SyncFailure.localState("missing_discovery_archive") }
            if let digest = event.archiveSHA256 { guard Wire.hash(try Data(contentsOf: url)) == digest else { throw SyncFailure.localState("corrupt_discovery_archive") } }
        } else if event.hasSegmentReference {
            try verifyCaptureSegmentRecord(event)
        } else if !event.deleted { throw SyncFailure.localState("missing_discovery_archive") }
    }
    public func discoverySnapshot() -> DiscoveryState { state.discovery ?? DiscoveryState() }
    public func activeDiscoveryQuery(_ query: DiscoveryQuery) -> DiscoveryQuery { state.discovery?.recoveryOverrides?[query.key] ?? query }
    public func discoveryFlow(_ query: DiscoveryQuery) throws -> DiscoveryFlow {
        guard query.contract == 1, (query.initialization == "all_visible_history" || query.initialization.hasPrefix("controlled:")) else { throw SyncFailure.localState("discovery_contract_requires_controlled_rediscovery") }
        if let flow = state.discovery?.flows[query.key] { return flow }
        var next = state; var discovery = next.discovery ?? DiscoveryState()
        let flow = DiscoveryFlow(query: query); discovery.flows[query.key] = flow
        next.discovery = discovery; try commit(next); return flow
    }
    /// First boundary: all raw archives already exist. Journal + durable work
    /// commit while the old anchor remains authoritative. Replay is harmless.
    @discardableResult public func stageDiscovery(_ query: DiscoveryQuery, previousAnchor: Data?, anchor: Data, events: [DiscoveryEvent], hasMore: Bool) throws -> String {
        guard var discovery = state.discovery, var flow = discovery.flows[query.key], flow.query == query,
              flow.anchor == previousAnchor, flow.staged == nil, !anchor.isEmpty, events.count <= 1000 else { throw SyncFailure.localState("stale_discovery_batch") }
        for event in events {
            guard event.type == query.type else { throw SyncFailure.localState("discovery_type_mismatch") }
            try verifyDiscoveryArchive(event)
            var event = event
            if let scoped = query.workoutID {
                guard event.workoutID == nil || event.workoutID == scoped else { throw SyncFailure.localState("discovery_predicate_mismatch") }
                event.workoutID = scoped
            }
            if event.workoutID == nil { event.workoutID = discovery.parents[event.factKey] }
            if event.type == "HKWorkoutTypeIdentifier" { event.workoutID = event.uuid }
            // An association proven by a fixed HealthKit workout predicate is
            // retained even after deletion. It is never inferred from time.
            if let parent = event.workoutID {
                if let known = discovery.parents[event.factKey], known != parent { throw SyncFailure.localState("discovery_association_conflict") }
                discovery.parents[event.factKey] = parent
                event.status = "pending"
                if discovery.events[event.id] == nil || discovery.events[event.id]?.workoutID == nil {
                    var work = discovery.work[parent] ?? DiscoveryWork()
                    // Upgrade compatibility: a Workout already present in the
                    // local task/reference ledger remains selected, so a late
                    // child deletion is not stranded as "new, unselected" work.
                    if state.tasks?[parent] != nil || state.workouts[parent] != nil { work.selected = true }
                    if state.deletedWorkouts.contains(parent) && !event.deleted {
                        event.status = "suppressed_by_workout_delete"
                    } else { work.generation += 1; event.generation = work.generation; event.ordinal = discovery.nextOrdinal() }
                    if state.deletedWorkouts.contains(parent) && event.deleted && event.type != "HKWorkoutTypeIdentifier" { work.selected = true }
                    work.error = nil; work.nextAttemptAt = nil
                    if event.deleted && event.type == "HKWorkoutTypeIdentifier" { work.pendingDelete = true }
                    discovery.work[parent] = work
                }
                if !event.deleted && event.type == "HKWorkoutTypeIdentifier", let file = event.archiveFile,
                   discovery.events[event.id] == nil || discovery.events[event.id]?.archiveFile == nil {
                    discovery.workoutArchives[parent] = file
                }
            }
            if discovery.events[event.id] == nil || discovery.events[event.id]?.workoutID == nil { discovery.events[event.id] = event }
        }
        let batch = DiscoveryBatch(id: Wire.id(), previousAnchor: previousAnchor, anchor: anchor, events: events.map(\.id), hasMore: hasMore)
        flow.staged = batch; flow.error = nil; discovery.flows[query.key] = flow
        var next = state; next.discovery = discovery; try commit(next); return batch.id
    }
    /// Second boundary: promotion only after a recoverable journal/work commit.
    /// Startup resumes staged batches before executing another HealthKit query.
    public func promoteDiscovery(_ query: DiscoveryQuery, batchID: String) throws {
        guard var discovery = state.discovery, var flow = discovery.flows[query.key], let batch = flow.staged,
              batch.id == batchID, flow.anchor == batch.previousAnchor,
              batch.events.allSatisfy({ discovery.events[$0] != nil }) else { throw SyncFailure.localState("unrecoverable_discovery_batch") }
        for id in batch.events { if let event = discovery.events[id] { try verifyDiscoveryArchive(event) } }
        flow.anchor = batch.anchor; flow.staged = nil; flow.hasMore = batch.hasMore; flow.lastProcessedAt = Date(); flow.error = nil
        discovery.flows[query.key] = flow; var next = state; next.discovery = discovery; try commit(next)
    }
    public func discoveryError(_ query: DiscoveryQuery, code: String?) throws {
        guard var discovery = state.discovery, var flow = discovery.flows[query.key] else { return }
        flow.error = code; discovery.flows[query.key] = flow; var next = state; next.discovery = discovery; try commit(next)
    }
    /// Full bundle reads can observe facts beyond the anchored page budget.
    /// Archive them too, without moving any HealthKit cursor.
    public func recordCapturedFacts(_ events: [DiscoveryEvent]) throws {
        guard events.count <= 1000 else { throw SyncFailure.localState("discovery_batch_limit") }
        var next = state; var discovery = next.discovery ?? DiscoveryState()
        for var event in events {
            try verifyDiscoveryArchive(event)
            guard let parent = event.workoutID else { throw SyncFailure.localState("captured_fact_parent_missing") }
            if let known = discovery.parents[event.factKey], known != parent { throw SyncFailure.localState("discovery_association_conflict") }
            discovery.parents[event.factKey] = parent
            if discovery.events[event.id] == nil {
                var work = discovery.work[parent] ?? DiscoveryWork(); work.generation += 1
                work.error = nil; work.nextAttemptAt = nil; work.attempt = 0
                event.generation = work.generation; event.ordinal = discovery.nextOrdinal()
                discovery.work[parent] = work; event.status = "pending"; discovery.events[event.id] = event
            }
        }
        next.discovery = discovery; try commit(next)
    }
    public func selectDiscoveredWorkout(_ id: String) throws {
        var next = state; var discovery = next.discovery ?? DiscoveryState(); var work = discovery.work[id] ?? DiscoveryWork()
        guard !work.pendingDelete, !next.deletedWorkouts.contains(id) else { throw SyncFailure.localState("workout_deleted") }
        work.selected = true; work.error = nil; work.nextAttemptAt = nil; work.attempt = 0
        discovery.work[id] = work; next.discovery = discovery; try commit(next)
    }
    public func associateDiscoveredFacts(workoutID: String, keys: [String]) throws {
        var next = state; var discovery = next.discovery ?? DiscoveryState()
        // Index unresolved events once. Scanning the entire historical journal
        // for every sample made a K-sample Workout cost K * E comparisons.
        // Preserve the input-key order and the existing generation semantics.
        let requested = Set(keys)
        var unresolved: [String: [String]] = [:]
        for (id, event) in discovery.events where event.workoutID == nil && requested.contains(event.factKey) {
            unresolved[event.factKey, default: []].append(id)
        }
        var changed = false
        for key in keys {
            if let known = discovery.parents[key] {
                if known != workoutID { throw SyncFailure.localState("discovery_association_conflict") }
            } else {
                discovery.parents[key] = workoutID; changed = true
            }
            for id in unresolved[key] ?? [] {
                guard var event = discovery.events[id], event.workoutID == nil else { continue }
                event.workoutID = workoutID; event.status = "pending"; discovery.events[id] = event
                var work = discovery.work[workoutID] ?? DiscoveryWork(); work.generation += 1
                work.error = nil; work.nextAttemptAt = nil; work.attempt = 0
                event.generation = work.generation; event.ordinal = discovery.nextOrdinal()
                discovery.events[id] = event; discovery.work[workoutID] = work; changed = true
            }
        }
        // A repeated association call with no new parent and no unresolved
        // event is a no-op. It must not re-encode the whole state or move
        // `updatedAt` on every producer pass.
        guard changed else { commitSkippedNoop += 1; return }
        next.discovery = discovery; try commit(next)
    }
    public func discoveryPrepared(_ id: String, generation: Int64) throws {
        var next = state
        if var work = next.discovery?.work[id] { work.preparedGeneration = generation; next.discovery?.work[id] = work }
        if var task = next.tasks?[id] { task.discoveryGeneration = generation; next.tasks?[id] = task }
        try commit(next)
    }
    public func discoveryFailure(_ id: String, code: String) throws {
        var next = state
        if var work = next.discovery?.work[id] {
            work.error = code; work.attempt += 1
            work.nextAttemptAt = Date().addingTimeInterval(min(900, pow(2, Double(min(work.attempt, 9)))))
            next.discovery?.work[id] = work
        }
        try commit(next)
    }
    public func parkBundleForExplicitChildDeletion(_ id: String) throws {
        guard let task = state.tasks?[id], let hash = task.bundleSHA256, task.bundleFile != nil, task.uploadReceipt == nil,
              state.discovery?.events.values.contains(where: { $0.workoutID == id && $0.deleted && $0.type != "HKWorkoutTypeIdentifier" && !(task.bundleDeletionEventIDs ?? []).contains($0.id) }) == true else { return }
        // Original unacknowledged bytes remain referenced forever until an
        // explicit resolution. They cannot be GC'd or silently replaced.
        var next = state
        if next.retainedBundles == nil { next.retainedBundles = [:] }
        next.retainedBundles?[hash] = task
        var successor = task
        successor.bundleFile = nil; successor.bundleSHA256 = nil; successor.bundleBoundary = nil
        successor.bundleWireFile = nil; successor.bundleWireSHA256 = nil
        successor.phase = .preparing; successor.nextAttemptAt = nil; successor.lastUploadError = nil; successor.errorCode = nil
        next.tasks?[id] = successor; try commit(next)
    }
    public func discoveryAdvanceCursor(_ count: Int, total: Int) throws {
        var next = state
        let remaining = next.discovery?.cycleRemaining ?? 0
        next.discovery?.scanCursor += count
        next.discovery?.cycleRemaining = max(0, (remaining == 0 ? total : remaining) - count)
        try commit(next)
    }
    /// A corrupt/old anchor is quarantined. This explicit API creates a new
    /// namespace for controlled rediscovery; old queues/anchors stay intact.
    public func controlledRediscovery(_ query: DiscoveryQuery, recoveryID: String) throws -> DiscoveryQuery {
        guard UUID(uuidString: recoveryID) != nil else { throw SyncFailure.localState("invalid_recovery_id") }
        var recovered = query; recovered.initialization = "controlled:" + recoveryID
        var next = state; var discovery = next.discovery ?? DiscoveryState()
        if discovery.flows[recovered.key] == nil { discovery.flows[recovered.key] = DiscoveryFlow(query: recovered) }
        if discovery.recoveryOverrides == nil { discovery.recoveryOverrides = [:] }
        discovery.recoveryOverrides?[query.key] = recovered
        next.discovery = discovery; try commit(next); return recovered
    }
}
