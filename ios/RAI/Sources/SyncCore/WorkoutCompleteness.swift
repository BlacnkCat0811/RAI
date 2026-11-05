import Foundation

public enum WorkoutCompleteness {
    public static func failureCode(_ error: any Error) -> String {
        if let upload = error as? UploadError { return upload.code }
        switch error {
        case SyncFailure.invalid(let code), SyncFailure.localState(let code): return code
        case SyncFailure.receiptMismatch: return "invalid_receipt"
        default: return "completeness_failed"
        }
    }
    public static let contract = "workout-sync-completeness/v1"
    public static let workoutType = "HKWorkoutTypeIdentifier", routeType = "HKWorkoutRouteTypeIdentifier"
    public static let quantities = ["HKQuantityTypeIdentifierHeartRate", "HKQuantityTypeIdentifierDistanceWalkingRunning",
        "HKQuantityTypeIdentifierActiveEnergyBurned", "HKQuantityTypeIdentifierRunningSpeed", "HKQuantityTypeIdentifierRunningPower",
        "HKQuantityTypeIdentifierStepCount", "HKQuantityTypeIdentifierBasalEnergyBurned", "HKQuantityTypeIdentifierRunningGroundContactTime",
        "HKQuantityTypeIdentifierRunningStrideLength", "HKQuantityTypeIdentifierRunningVerticalOscillation"]
    public static let unsupported = ["native_running_cadence", "elevation_sample"]
    public static var types: [String] { ([workoutType, routeType] + quantities).sorted() }
    public static func key(type: String, workoutID: String?) -> String {
        type == workoutType ? "workouts" : "workout:" + (workoutID ?? "") + ":" + type
    }
    public static func fingerprint(from: Int64, key: String) -> String {
        "sha256:" + Wire.hash(Data((contract + "|stream-set:1|running-overlap-v1|" + String(from) + "|" + key).utf8))
    }
    public static func seriesDigest(_ values: [String: [JSON]]) throws -> (hash: String, count: Int) {
        let capture = try seriesCapture(values)
        return (Wire.hash(capture.data), capture.count)
    }
    public static func seriesCapture(_ values: [String: [JSON]]) throws -> (data: Data, count: Int) {
        let items = values.keys.sorted().filter { !(values[$0] ?? []).isEmpty }.map {
            JSON.object(["sampleUUID": .string($0), "points": .array(values[$0]!)])
        }
        return (try JSON.array(items).data(), values.values.reduce(0) { $0 + $1.count })
    }
}

public struct WorkoutCompletenessAnchor: Codable, Sendable {
    public let anchor: Data
    public let sequence: Int64
    public let pageSHA256: String
    public let queryFingerprint: String
}
public struct WorkoutCompletenessCapture: Codable, Sendable {
    public let page: JSON
    public let candidateAnchor: Data
    public let archiveFile: String
    public let archiveSHA256: String
}
public struct WorkoutCompletenessCycle: Codable, Sendable {
    public let manifestID: String
    public let generation: Int64
    public let cutoffUs: Int64
    public var captures: [String: [WorkoutCompletenessCapture]] = [:]
    public var inventory: [String] = []
    public var deletedRoots: [String] = []
    public var proofs: [String: JSON] = [:]
    public var completedWorkouts: [String] = []
    public var seriesFiles: [String: String] = [:]
    public var pendingFile: String?
    public var pendingSHA256: String?
    public var committedAnchorQueries = 0
    public var stagedCursorQueries = 0
    public var bootstrapQueries = 0
}
public struct WorkoutCompletenessState: Codable, Sendable {
    public let windowFromUs: Int64
    public var generation: Int64 = 0
    public var lastManifestSHA256: String?
    public var lastCutoffUs: Int64?
    public var anchors: [String: WorkoutCompletenessAnchor] = [:]
    public var inventory: [String] = []
    public var proofs: [String: JSON] = [:]
    public var cycle: WorkoutCompletenessCycle?
    public var lastReceipt: JSON?
    public var lastQueryCounts: [String: Int]?
    public var errorCode: String?
}
public struct WorkoutCompletenessQuery: Sendable {
    public let type: String
    public let workoutID: String?
    public init(type: String, workoutID: String? = nil) { self.type = type; self.workoutID = workoutID }
    public var key: String { WorkoutCompleteness.key(type: type, workoutID: workoutID) }
}

extension OutboxStore {
    public func claimWorkoutCompleteness() throws -> WorkoutCompletenessCycle? {
        guard activeCompletenessManifest == nil, let cycle = state.workoutCompleteness?.cycle,
              cycle.pendingFile != nil else { return nil }
        activeCompletenessManifest = cycle.manifestID; return cycle
    }
    public func endWorkoutCompletenessAttempt(_ id: String) {
        if activeCompletenessManifest == id { activeCompletenessManifest = nil }
    }
    @discardableResult public func beginWorkoutCompleteness(now: Date = Date()) throws -> WorkoutCompletenessState {
        guard state.scope != nil else { throw SyncFailure.localState("completeness_scope_missing") }
        let us = try VO2Max.microseconds(now)
        // Body-before-state crashes can only orphan files that were never sent.
        let existing = state.workoutCompleteness?.cycle
        var roots = Set([existing?.pendingFile].compactMap { $0 })
        roots.formUnion(existing?.captures.values.flatMap { $0.map(\.archiveFile) } ?? [])
        roots.formUnion(existing?.seriesFiles.keys.map { $0 } ?? [])
        let orphaned = try FileManager.default.contentsOfDirectory(atPath: directory.path).filter {
            ($0.hasSuffix(".completeness.json") || $0.hasSuffix(".completeness.archive")) && !roots.contains($0)
        }
        if !orphaned.isEmpty {
            var next = state; next.reclaimFiles = Array(Set((next.reclaimFiles ?? []) + orphaned)).sorted(); try commit(next)
            _ = try? collectGarbage(dryRun: false)
        }
        var flow = state.workoutCompleteness ?? WorkoutCompletenessState(windowFromUs: us - 200 * 86_400_000_000)
        if flow.cycle == nil {
            flow.cycle = WorkoutCompletenessCycle(manifestID: Wire.id(), generation: flow.generation + 1,
                cutoffUs: us, inventory: flow.inventory)
            flow.errorCode = nil
            var next = state; next.workoutCompleteness = flow; try commit(next)
        }
        return flow
    }
    public func completenessCursor(_ query: WorkoutCompletenessQuery) throws -> (anchor: Data?, exhausted: Bool) {
        guard let flow = state.workoutCompleteness, let cycle = flow.cycle else { throw SyncFailure.localState("completeness_cycle_missing") }
        if let captured = cycle.captures[query.key]?.last {
            let archive = try Data(contentsOf: fileURL(captured.archiveFile))
            guard Wire.hash(archive) == captured.archiveSHA256 else { throw SyncFailure.localState("completeness_capture_corrupt") }
            return (captured.candidateAnchor, captured.page["hasMore"] == .bool(false))
        }
        return (flow.anchors[query.key]?.anchor, false)
    }
    public func captureCompletenessPage(_ query: WorkoutCompletenessQuery, previousAnchor: Data?, candidateAnchor: Data,
        additions: [String], deletions: [String], hasMore: Bool, startedAt: Date, completedAt: Date, archive: Data) throws {
        guard var flow = state.workoutCompleteness, var cycle = flow.cycle, cycle.pendingFile == nil,
              !candidateAnchor.isEmpty, additions.count + deletions.count <= 1000 else { throw SyncFailure.localState("completeness_state_conflict") }
        let cursor = try completenessCursor(query)
        guard !cursor.exhausted, cursor.anchor == previousAnchor else { throw SyncFailure.localState("completeness_anchor_conflict") }
        let prior = cycle.captures[query.key]?.last
        let head = flow.anchors[query.key]
        let sequence = (prior?.page["sequence"].integer ?? head?.sequence ?? 0) + 1
        let previousPageHash = try prior.map { Wire.hash(try $0.page.data()) } ?? head?.pageSHA256
        let page: JSON = .object(["pageID": .string(Wire.id()), "sequence": .int(sequence),
            "previousPageSHA256": previousPageHash.map(JSON.string) ?? .null,
            "previousAnchorSHA256": previousAnchor.map { .string(Wire.hash($0)) } ?? .null,
            "candidateAnchorSHA256": .string(Wire.hash(candidateAnchor)),
            "queryStartedAtUs": .int(try VO2Max.microseconds(startedAt)), "queryCompletedAtUs": .int(try VO2Max.microseconds(completedAt)),
            "additions": .array(additions.map(JSON.string)), "deletions": .array(deletions.map(JSON.string)), "hasMore": .bool(hasMore)])
        guard page["queryStartedAtUs"].integer! >= cycle.cutoffUs else { throw SyncFailure.localState("completeness_cutoff_conflict") }
        let file = try savePayload(archive, suffix: "completeness.archive")
        cycle.captures[query.key, default: []].append(WorkoutCompletenessCapture(page: page, candidateAnchor: candidateAnchor,
            archiveFile: file, archiveSHA256: Wire.hash(archive)))
        if prior != nil { cycle.stagedCursorQueries += 1 }
        else if previousAnchor != nil { cycle.committedAnchorQueries += 1 }
        else { cycle.bootstrapQueries += 1 }
        if query.type == WorkoutCompleteness.workoutType {
            var inventory = Set(cycle.inventory); inventory.formUnion(additions); inventory.subtract(deletions)
            cycle.inventory = inventory.sorted(); cycle.deletedRoots = Array(Set(cycle.deletedRoots + deletions)).sorted()
            guard cycle.inventory.count <= 256 else { throw SyncFailure.invalid("completeness_workout_limit") }
        }
        flow.cycle = cycle; var next = state; next.workoutCompleteness = flow; try commit(next)
    }
    public func completenessExcludeDeletedWorkout(_ id: String) throws {
        guard var flow = state.workoutCompleteness, var cycle = flow.cycle, cycle.pendingFile == nil else { throw SyncFailure.localState("completeness_state_conflict") }
        cycle.inventory.removeAll { $0 == id }; cycle.proofs.removeValue(forKey: id)
        flow.cycle = cycle; var next = state; next.workoutCompleteness = flow; try commit(next)
    }
    public func completenessProof(_ id: String, proof: JSON) throws {
        guard var flow = state.workoutCompleteness, var cycle = flow.cycle, cycle.pendingFile == nil,
              cycle.inventory.contains(id), proof["workoutID"].text == id else { throw SyncFailure.localState("completeness_state_conflict") }
        cycle.proofs[id] = proof; cycle.completedWorkouts = Array(Set(cycle.completedWorkouts + [id])).sorted()
        flow.cycle = cycle; var next = state; next.workoutCompleteness = flow; try commit(next)
    }
    public func persistCompletenessSeries(_ data: Data) throws {
        guard var flow = state.workoutCompleteness, var cycle = flow.cycle, cycle.pendingFile == nil else { throw SyncFailure.localState("completeness_state_conflict") }
        let file = try savePayload(data, suffix: "completeness.archive")
        cycle.seriesFiles[file] = Wire.hash(data); flow.cycle = cycle
        var next = state; next.workoutCompleteness = flow; try commit(next)
    }
    public func stageWorkoutCompleteness(now: Date = Date()) throws {
        guard var flow = state.workoutCompleteness, var cycle = flow.cycle, let scope = state.scope else { throw SyncFailure.localState("completeness_cycle_missing") }
        if cycle.pendingFile != nil { return }
        for (file, hash) in cycle.seriesFiles {
            guard Wire.hash(try Data(contentsOf: fileURL(file))) == hash else { throw SyncFailure.localState("completeness_capture_corrupt") }
        }
        let roots = cycle.captures["workouts"] ?? []
        guard roots.last?.page["hasMore"] == .bool(false), cycle.inventory.allSatisfy({ cycle.proofs[$0] != nil }) else { throw SyncFailure.localState("completeness_stream_pending") }
        var streams: [JSON] = []
        let queries = [WorkoutCompletenessQuery(type: WorkoutCompleteness.workoutType)] + cycle.inventory.flatMap { id in
            (WorkoutCompleteness.quantities + [WorkoutCompleteness.routeType]).map { WorkoutCompletenessQuery(type: $0, workoutID: id) }
        }
        for query in queries.sorted(by: { $0.key < $1.key }) {
            guard let captures = cycle.captures[query.key], captures.last?.page["hasMore"] == .bool(false) else { throw SyncFailure.localState("completeness_stream_pending") }
            for capture in captures { guard Wire.hash(try Data(contentsOf: fileURL(capture.archiveFile))) == capture.archiveSHA256 else { throw SyncFailure.localState("completeness_capture_corrupt") } }
            streams.append(.object(["key": .string(query.key), "sampleType": .string(query.type), "workoutID": query.workoutID.map(JSON.string) ?? .null,
                "queryFingerprint": .string(WorkoutCompleteness.fingerprint(from: flow.windowFromUs, key: query.key)), "pages": .array(captures.map(\.page))]))
        }
        let body: JSON = .object(["contract": .string(WorkoutCompleteness.contract), "protocolVersion": .int(1),
            "datasetID": .string(scope.datasetID), "datasetEpoch": .string(scope.datasetEpoch), "collectorEpoch": .string(scope.collectorEpoch),
            "manifestID": .string(cycle.manifestID), "generation": .int(cycle.generation), "previousManifestSHA256": flow.lastManifestSHA256.map(JSON.string) ?? .null,
            "streamSetVersion": .int(1), "windowFromUs": .int(flow.windowFromUs), "knownCutoffUs": .int(cycle.cutoffUs),
            "createdAtUs": .int(try VO2Max.microseconds(now)), "supportedStreams": .array(WorkoutCompleteness.types.map(JSON.string)),
            "unsupportedStreams": .array(WorkoutCompleteness.unsupported.map(JSON.string)),
            "notApplicableStreams": .array((cycle.inventory.isEmpty ? (WorkoutCompleteness.quantities + [WorkoutCompleteness.routeType]).sorted() : []).map(JSON.string)),
            "streams": .array(streams), "workouts": .array(cycle.inventory.map { cycle.proofs[$0]! })])
        let raw = try body.data(); guard raw.count <= 16 * 1024 * 1024 else { throw SyncFailure.invalid("completeness_manifest_limit") }
        let pages = queries.flatMap { cycle.captures[$0.key] ?? [] }
        guard pages.count <= 8192,
              pages.reduce(0, { $0 + $1.page["additions"].list.count + $1.page["deletions"].list.count }) <= 250000 else { throw SyncFailure.invalid("completeness_manifest_limit") }
        cycle.pendingFile = try savePayload(raw, suffix: "completeness.json"); cycle.pendingSHA256 = Wire.hash(raw)
        flow.cycle = cycle; var next = state; next.workoutCompleteness = flow; try commit(next)
    }
    public func acknowledgeWorkoutCompleteness(_ receipt: JSON, transferCacheFile: String? = nil) throws {
        guard var flow = state.workoutCompleteness, let cycle = flow.cycle, let scope = state.scope,
              let file = cycle.pendingFile, let hash = cycle.pendingSHA256,
              Wire.hash(try Data(contentsOf: fileURL(file))) == hash,
              receipt["contract"].text == WorkoutCompleteness.contract, receipt["protocolVersion"].integer == 1,
              receipt["datasetID"].text == scope.datasetID, receipt["datasetEpoch"].text == scope.datasetEpoch,
              receipt["collectorEpoch"].text == scope.collectorEpoch, receipt["manifestID"].text == cycle.manifestID,
              receipt["generation"].integer == cycle.generation, receipt["manifestSHA256"].text == hash,
              receipt["ingestStatus"].text == "committed", let revision = receipt["commitRevision"].integer, revision > 0 else { throw SyncFailure.receiptMismatch }
        let body = try JSON.decode(Data(contentsOf: fileURL(file)))
        guard receipt["streamCount"].integer == Int64(body["streams"].list.count), receipt["workoutCount"].integer == Int64(cycle.inventory.count) else { throw SyncFailure.receiptMismatch }
        var anchors: [String: WorkoutCompletenessAnchor] = [:]
        for stream in body["streams"].list {
            guard let key = stream["key"].text, let capture = cycle.captures[key]?.last,
                  capture.page["hasMore"] == .bool(false), let sequence = capture.page["sequence"].integer,
                  let fingerprint = stream["queryFingerprint"].text,
                  try capture.page.data() == stream["pages"].list.last?.data(),
                  capture.page["candidateAnchorSHA256"].text == Wire.hash(capture.candidateAnchor) else { throw SyncFailure.receiptMismatch }
            anchors[key] = WorkoutCompletenessAnchor(anchor: capture.candidateAnchor, sequence: sequence,
                pageSHA256: Wire.hash(try capture.page.data()), queryFingerprint: fingerprint)
        }
        let proofs = body["workouts"].list
        let inventory = proofs.compactMap { $0["workoutID"].text }.sorted()
        guard inventory == cycle.inventory.sorted() else { throw SyncFailure.receiptMismatch }
        flow.anchors = anchors; flow.inventory = inventory
        flow.proofs = Dictionary(uniqueKeysWithValues: proofs.map { ($0["workoutID"].text!, $0) })
        flow.generation = cycle.generation; flow.lastManifestSHA256 = hash; flow.lastReceipt = receipt
        flow.lastCutoffUs = cycle.cutoffUs
        flow.lastQueryCounts = ["committedAnchor": cycle.committedAnchorQueries, "stagedCursor": cycle.stagedCursorQueries, "bootstrap": cycle.bootstrapQueries]
        flow.cycle = nil; flow.errorCode = nil
        var next = state; next.workoutCompleteness = flow
        // Archive filenames have their own managed suffix and become reclaimable
        // only in this same anchor/receipt transition.
        next.reclaimFiles = Array(Set((next.reclaimFiles ?? []) + [file] + cycle.captures.values.flatMap { $0.map(\.archiveFile) } + Array(cycle.seriesFiles.keys) + [transferCacheFile].compactMap { $0 })).sorted()
        try commit(next)
    }
    public func workoutCompletenessFailure(_ error: any Error) throws {
        guard var flow = state.workoutCompleteness else { return }; flow.errorCode = WorkoutCompleteness.failureCode(error)
        var next = state; next.workoutCompleteness = flow; try commit(next)
    }
}

public struct WorkoutCompletenessDelivery: Sendable {
    public let store: OutboxStore
    public let api: RAIAPIClient
    public init(store: OutboxStore, api: RAIAPIClient) { self.store = store; self.api = api }
    @discardableResult public func deliver() async throws -> Bool {
        guard let cycle = try await store.claimWorkoutCompleteness() else { return false }
        do {
        guard let file = cycle.pendingFile,
              let hash = cycle.pendingSHA256 else { throw SyncFailure.localState("completeness_pending_missing") }
        let url = try await store.fileURL(file)
        guard Wire.hash(try Data(contentsOf: url)) == hash else { throw SyncFailure.localState("completeness_payload_corrupt") }
            let receipt = try await api.call("POST", "/v2/sync/workout-completeness", file: url, identity: cycle.manifestID)
            try await store.acknowledgeWorkoutCompleteness(receipt, transferCacheFile: api.cachedUploadResultFilename(cycle.manifestID))
            api.discardUploadResult(cycle.manifestID); _ = try? await store.collectGarbage(dryRun: false)
            await store.endWorkoutCompletenessAttempt(cycle.manifestID)
            return true
        } catch {
            await store.endWorkoutCompletenessAttempt(cycle.manifestID)
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            if ["invalid_receipt", "invalid_ack_response", "invalid_response", "oversized_response"].contains(WorkoutCompleteness.failureCode(error)) { api.discardUploadResult(cycle.manifestID) }
            try await store.workoutCompletenessFailure(error); throw error
        }
    }
}
