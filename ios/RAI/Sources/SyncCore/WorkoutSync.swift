import Foundation

/// Product-facing projection of one independently uploaded HealthKit Workout.
/// The underlying protocol states stay batch/page/receipt/analysis; this type
/// never replaces or renames them.
public enum WorkoutSyncPhase: String, Codable, Sendable, CaseIterable {
    case notSynced = "NOT_SYNCED"
    case preparing = "PREPARING"
    case uploading = "UPLOADING"
    /// The exact payload is durably accepted server-side but not yet imported.
    case accepted = "SERVER_ACCEPTED"
    case acknowledged = "SERVER_ACKNOWLEDGED"
    case analyzing = "ANALYZING"
    case published = "PUBLISHED"
    case partial = "PARTIAL"
    case failed = "FAILED"
    case deleted = "DELETED"
}

public struct WorkoutCoverage: Codable, Sendable, Equatable {
    public enum State: String, Codable, Sendable {
        case available, unavailable, unsupported, error // decode legacy state
        case notQueried = "not_queried"
        case observed, partial, complete, failed, unknown
        case noRecordsObserved = "no_records_observed"
    }
    public var state: State
    public var sampleCount: Int
    public var errorCode: String?
    public var queryCompleted: Bool?
    public var localPersisted: Bool?
    public var serverCommitted: Bool?
    public var expectedCount: Int?
    public var diagnostics: [String]?
    public init(state: State, sampleCount: Int = 0, errorCode: String? = nil) {
        self.state = state; self.sampleCount = sampleCount; self.errorCode = errorCode
    }
    public var stateText: String {
        switch state {
        case .available, .complete: return "本次读取完成"
        case .unavailable, .unknown: return "尚无法判断"
        case .unsupported: return "当前不支持"
        case .error, .failed: return "读取失败"
        case .notQueried: return "尚未查询"
        case .observed: return "已观察到数据"
        case .noRecordsObserved: return "本次未观察到记录"
        case .partial: return "部分数据待处理"
        }
    }
    public var isAvailable: Bool { state == .available || state == .complete }
    /// A stream is satisfied when another retry cannot add data. `.unsupported`
    /// is a final answer from this SDK, not an incomplete read; treating it as
    /// missing made `cadence`/`elevation_sample` keep every Workout `.partial`
    /// forever. `.noRecordsObserved` is likewise a completed empty read.
    public var isSatisfied: Bool {
        switch state {
        case .available, .complete, .noRecordsObserved, .unsupported: return true
        case .unavailable, .error, .notQueried, .observed, .partial, .failed, .unknown: return false
        }
    }
    public var needsRetry: Bool { !isSatisfied }
    public var wire: JSON {
        var fields: [String: JSON] = ["state": .string(state.rawValue), "sampleCount": .int(Int64(sampleCount)),
            "queryCompleted": .bool(queryCompleted ?? false), "localPersisted": .bool(localPersisted ?? false)]
        if let errorCode { fields["errorCode"] = .string(errorCode) }
        if let expectedCount { fields["expectedCount"] = .int(Int64(expectedCount)) }
        if let diagnostics, !diagnostics.isEmpty { fields["diagnostics"] = .array(diagnostics.map(JSON.string)) }
        return .object(fields)
    }
    public static func result(count: Int, completed: Bool = true, expected: Int? = nil) -> WorkoutCoverage {
        var value = WorkoutCoverage(state: completed ? (count == 0 ? .noRecordsObserved : .complete) : .partial, sampleCount: count)
        value.queryCompleted = completed; value.expectedCount = expected; value.localPersisted = false
        return value
    }
}

/// Bounded follow-up for an acknowledged Workout whose analysis is still
/// asynchronous. The first refresh is immediate; later attempts use the
/// server-visible backoff. The total budget stays below ten minutes and the
/// closed phase set stops the loop as soon as publication is final.
public enum AnalysisFollowup {
    public static let delays: [TimeInterval] = [2, 5, 15, 30, 60, 120, 300]
    /// A repeated unchanged sync may schedule one refresh when the last check
    /// is older than this; it never starts an unbounded poll.
    public static let staleRefreshInterval: TimeInterval = 30
    public static func shouldContinue(_ phase: WorkoutSyncPhase) -> Bool {
        phase == .accepted || phase == .acknowledged || phase == .analyzing
    }
    public static func delay(forAttempt attempt: Int) -> TimeInterval? {
        guard attempt >= 0, attempt < delays.count else { return nil }
        return delays[attempt]
    }
    public static var totalBudget: TimeInterval { delays.reduce(0, +) }
}

/// A local aggregate for one HKWorkout import task. Protocol 3 sends the
/// complete Workout as one bundle and one HTTP request; the task no longer
/// owns page, batch or association operations.
public struct WorkoutSyncTask: Codable, Sendable, Equatable {
    public var workoutID: String
    public var phase: WorkoutSyncPhase
    public var createdAt: Date
    public var updatedAt: Date
    public var acknowledgedAt: Date?
    public var analysisCheckedAt: Date?
    public var errorCode: String?
    public var coverage: [String: WorkoutCoverage]
    public var metrics: [String: Double]
    public var missing: [String]
    /// Durable exact-byte upload identity. A retry or recovery must reuse this
    /// file rather than re-reading HealthKit and rebuilding different bytes.
    public var bundleFile: String?
    public var bundleSHA256: String?
    public var bundleBoundary: String?
    /// UUID ingest lane marker. `"uuid"` means the fixed per-session payload is
    /// authoritative and the old bundle upload must never be retried for this
    /// Workout. A missing value on an old task keeps the legacy lane.
    public var ingestMode: String?
    /// Server acceptance of the exact payload; the file may already be gone.
    public var ingestAcceptedAt: Date?
    /// Last observed ingest status (`accepted`, `committed`, `rejected`, ...).
    public var ingestStatus: String?
    /// Derived gzip multipart; canonical bundleFile remains recovery authority.
    public var bundleWireFile: String?
    public var bundleWireSHA256: String?
    /// Optional fields keep already persisted v1 Outbox state decodable.
    public var attemptCount: Int?
    public var nextAttemptAt: Date?
    public var lastUploadError: UploadError?
    public var uploadReceipt: JSON?
    public var deletionReceipt: JSON?
    /// The HealthKit Workout anchored-query anchor, used only to discover an
    /// explicit workout deletion. Missing children never authorize deletion.
    public var workoutAnchor: Data?
    public var discoveryGeneration: Int64?
    public var originalWorkout: JSON?
    public var submittedSampleDeletions: Int?
    public var submittedRouteDeletions: Int?
    public var bundleDeletionEventIDs: [String]?
    public var pendingReadKeys: [String]?
    /// R1 ACK boundary: the exact journal identities this bundle provably
    /// encoded. A receipt confirms only what the manifest covers; anything a
    /// producer committed later stays pending for the next delivery.
    public var captureManifest: CaptureManifest?
    public var childDeletionOnly: Bool?
    /// Set only after the bundle receipt has been validated and the task is
    /// acknowledged. Retained for decoding older local state.
    public var stagedAll: Bool?
    public init(workoutID: String, now: Date = Date()) {
        self.workoutID = workoutID; phase = .preparing; createdAt = now; updatedAt = now
        coverage = [:]; metrics = [:]; missing = []
    }
}

/// HealthKit Workout summary for the selection screen. Contains no GPS and no
/// raw health values beyond the aggregate values already provided by HKWorkout.
public struct HealthWorkoutSummary: Identifiable, Sendable, Equatable {
    public let id: String
    public let start: Date
    public let end: Date
    public let durationS: Double
    public let distanceM: Double?
    public let averageHeartRate: Double?
    public let isIndoor: Bool?
    public let activityType: Int
    public let sourceName: String?
    public let deviceName: String?
    /// Frozen source-policy classification of this Workout (`apple_watch`,
    /// `third_party`, `user_entered`, `source_unknown`, ...). The selection list
    /// must never offer a record that policy excludes, and an excluded record
    /// must never start a detailed child read.
    public let sourceClassification: String
    /// `eligible` / `excluded` / `unclassified` from `WorkoutSourcePolicy`.
    /// Unknown is never silently treated as either of the other two.
    public let sourceOutcome: String
    public init(id: String, start: Date, end: Date, durationS: Double, distanceM: Double?,
                averageHeartRate: Double?, isIndoor: Bool?, activityType: Int,
                sourceName: String?, deviceName: String?,
                sourceClassification: String = "source_unknown",
                sourceOutcome: String = WorkoutSourcePolicy.unclassified) {
        self.id = id; self.start = start; self.end = end; self.durationS = durationS
        self.distanceM = distanceM; self.averageHeartRate = averageHeartRate
        self.isIndoor = isIndoor; self.activityType = activityType
        self.sourceName = sourceName; self.deviceName = deviceName
        self.sourceClassification = sourceClassification; self.sourceOutcome = sourceOutcome
    }
    /// Only an eligible Workout may be selected for upload.
    public var isEligible: Bool { sourceOutcome == WorkoutSourcePolicy.eligible }
    /// Proven third-party / user-entered exclusion: hidden by default and never
    /// collected.
    public var isExcluded: Bool { sourceOutcome == WorkoutSourcePolicy.excluded }
    /// Not yet decidable. Blocks that one Workout instead of guessing either way.
    public var isUnclassified: Bool { sourceOutcome == WorkoutSourcePolicy.unclassified }
    public var averagePaceSecondsPerM: Double? {
        guard let distanceM, distanceM > 0, durationS > 0 else { return nil }
        return durationS / distanceM
    }
}
