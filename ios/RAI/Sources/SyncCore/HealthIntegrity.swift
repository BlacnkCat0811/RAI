import Foundation

/// RAI classification, separate from the HealthKit-provided fields. Names are
/// descriptive only. A third-party writer on Watch is never native Apple data.
///
/// UA1-R3B freezes this classifier as the source of the Workout source policy.
/// It adds exactly one deterministic class, `user_entered`, and no new eligible
/// class. An empty descriptor field means "HealthKit did not report it"; the Go
/// server reads the same bytes the same way, and both implementations are held
/// to contracts/fixtures/workout-source-policy-v1.json.
public enum HealthSourcePolicy {
    static func descriptor(_ value: JSON) -> String? { value.text.flatMap { $0.isEmpty ? nil : $0 } }
    public static func classification(_ source: JSON, associatedWorkout: JSON? = nil) -> String {
        guard let bundle = descriptor(source["bundleID"]) else { return "source_unknown" }
        // Data entered by hand in the Health app reports no product type and no
        // device. It is a deterministic exclusion, never an unresolved source.
        if bundle == "com.apple.Health", descriptor(source["productType"]) == nil,
           source["device"].fields.values.allSatisfy({ descriptor($0) == nil }) { return "user_entered" }
        let prefix = "com.apple.health."
        let suffix = bundle.hasPrefix(prefix) ? String(bundle.dropFirst(prefix.count)) : ""
        let scoped = UUID(uuidString: suffix).map { $0.uuidString != "00000000-0000-0000-0000-000000000000" } ?? false
        guard ["com.apple.health", "com.apple.Workout"].contains(bundle) || scoped else {
            return bundle.hasPrefix("com.apple.") ? "source_unknown" : "third_party"
        }
        let product = descriptor(source["productType"])
        let parentMatches = associatedWorkout.map { parent in
            classification(parent) == "apple_watch" && parent["bundleID"] == source["bundleID"]
                && descriptor(source["version"]) != nil && descriptor(source["version"]) == descriptor(parent["version"])
        } ?? false
        if let product, product.range(of: #"^Watch[0-9]+,[0-9]+$"#, options: .regularExpression) == nil {
            // A paired iPhone can carry the child samples of a Watch workout.
            // The parent association and the device-scoped bundle identity are
            // stronger evidence than the productType label on the child.
            return parentMatches ? "associated_apple_watch" : "source_unknown"
        }
        let manufacturer = descriptor(source["device"]["manufacturer"]), model = descriptor(source["device"]["model"])
        if let manufacturer, !["Apple", "Apple Inc."].contains(manufacturer) { return "source_unknown" }
        if let model, !model.lowercased().contains("watch") { return "source_unknown" }
        if product != nil {
            // Device-scoped Apple Health writers require actual Watch device
            // evidence, or the separately proven parent association below.
            if !scoped || manufacturer != nil && model != nil { return "apple_watch" }
        }
        if let parent = associatedWorkout, classification(parent) == "apple_watch",
           parent["bundleID"] == source["bundleID"], let version = descriptor(source["version"]),
           version == descriptor(parent["version"]) {
            return "associated_apple_watch"
        }
        return "source_unknown"
    }
}

public struct RouteCaptureCheckpoint: Codable, Sendable {
    public let file: String
    public var expectedCount: Int
    public var pointCount: Int = 0
    public var completed = false
    public var error: String?
}

/// Batching policy for a streamed capture. The first limit reached wins:
/// buffered points are written and synchronized in one batch, never per point.
/// The interval is evaluated when a point arrives (no timer thread), so an idle
/// tail is made durable by the next point, by `flush()` or by `finish`.
public struct RouteCaptureBatchPolicy: Sendable, Equatable {
    public var maxPoints: Int
    public var maxBytes: Int
    public var maxInterval: TimeInterval
    public static let standard = RouteCaptureBatchPolicy()
    public init(maxPoints: Int = 512, maxBytes: Int = 262_144, maxInterval: TimeInterval = 0.25) {
        self.maxPoints = maxPoints; self.maxBytes = maxBytes; self.maxInterval = maxInterval
    }
    var isValid: Bool { maxPoints > 0 && maxBytes > 0 && maxInterval > 0 }
}

/// Append-only capture is a referenced Outbox artifact before querying starts.
/// On restart HealthKit is queried again, comparing the saved prefix byte for
/// byte. No source points are sorted, replaced, deduplicated or rewritten.
///
/// The file bytes are unchanged: every point is `point.canonical(sequence:)`
/// followed by a newline. Only the durability granularity changed. Points are
/// buffered and written with one `synchronize()` per batch (512 points /
/// 256 KiB / 250 ms, whichever comes first). A crash loses only the unwritten
/// tail; the durable prefix is never rewritten, so the replayed HealthKit
/// sequence still compares line by line against it. A partially written tail
/// line stays a hard `route_capture_incomplete_line` error, never a silent
/// truncation. Cancellation must not call `finish()`; teardown writes the
/// buffered tail as an unfinished prefix instead.
public final class RouteCaptureFile {
    private let writer: FileHandle
    private let reader: FileHandle
    private let policy: RouteCaptureBatchPolicy
    private let clock: @Sendable () -> Date
    private var readBuffer = Data()
    private var reachedEOF = false
    private var pending = Data()
    private var pendingPoints = 0
    private var lastFlush: Date
    private var sealed = false
    private var poisoned = false
    public private(set) var count = 0
    /// Batched write+sync operations. Grows with the number of batches, not the
    /// number of points, and is the counter P0 evidence should record.
    public private(set) var flushes = 0
    /// Every `synchronize()` performed by this writer, including the final one.
    public private(set) var synchronizations = 0
    /// Points this writer actually appended to the file (a replayed prefix that
    /// matched the saved bytes writes nothing).
    public private(set) var pointsWritten = 0
    public var pendingPointCount: Int { pendingPoints }
    public var pendingByteCount: Int { pending.count }
    public let url: URL
    public init(url: URL, policy: RouteCaptureBatchPolicy = .standard, clock: @escaping @Sendable () -> Date = Date.init) throws {
        self.url = url; self.policy = policy; self.clock = clock; self.lastFlush = clock()
        guard policy.isValid else { throw SyncFailure.invalid("invalid_route_capture_policy") }
        if !FileManager.default.fileExists(atPath: url.path) { try OutboxStore.write(Data(), to: url) }
        writer = try FileHandle(forWritingTo: url); try writer.seekToEnd()
        reader = try FileHandle(forReadingFrom: url)
    }
    // §6.1: cancellation and normal end both make the accumulated batch durable.
    // Teardown therefore writes the buffered tail once, without a `finish`
    // marker: the checkpoint stays incomplete, so the capture is still reported
    // as unfinished and the next query replays against this exact prefix. A real
    // process death runs no deinit, so a crash loses only the unwritten tail.
    deinit { try? flush(); try? writer.close(); try? reader.close() }
    private func checkUsable() throws {
        guard !poisoned else { throw SyncFailure.localState("route_capture_flush_failed") }
        guard !sealed else { throw SyncFailure.invalid("route_capture_finished") }
    }
    private func savedLine() throws -> Data? {
        while true {
            if let end = readBuffer.firstIndex(of: 10) {
                let line = Data(readBuffer[..<end]); readBuffer.removeSubrange(...end); return line
            }
            if reachedEOF {
                guard readBuffer.isEmpty else { throw SyncFailure.localState("route_capture_incomplete_line") }
                return nil
            }
            let bytes = try reader.read(upToCount: 65536) ?? Data()
            if bytes.isEmpty { reachedEOF = true } else { readBuffer += bytes }
        }
    }
    public func append(_ point: RoutePoint) throws {
        try checkUsable()
        guard count < 1_000_000 else { throw SyncFailure.invalid("route_point_limit") }
        let bytes = try point.canonical(sequence: Int64(count))
        if let previous = try savedLine() {
            guard previous == bytes else { throw SyncFailure.localState("route_capture_changed") }
        } else {
            try stage(bytes)
        }
        count += 1
    }
    public func append(contentsOf points: [RoutePoint]) throws { for point in points { try append(point) } }
    private func stage(_ line: Data) throws {
        if pendingPoints > 0, clock().timeIntervalSince(lastFlush) >= policy.maxInterval { try flush() }
        pending.append(line); pending.append(10); pendingPoints += 1
        if pendingPoints >= policy.maxPoints || pending.count >= policy.maxBytes { try flush() }
    }
    /// Write and synchronize the buffered tail once. Safe to call at any time.
    public func flush() throws {
        try checkUsable()
        guard !pending.isEmpty else { return }
        do {
            try writer.write(contentsOf: pending)
            try writer.synchronize()
        } catch {
            // Bytes may already be on disk. Stop touching the file so a later
            // call cannot duplicate a partial line; the durable prefix stands.
            poisoned = true; pending.removeAll(); pendingPoints = 0
            throw SyncFailure.localState("route_capture_flush_failed")
        }
        pointsWritten += pendingPoints
        pending.removeAll(keepingCapacity: true); pendingPoints = 0
        flushes += 1; synchronizations += 1; lastFlush = clock()
    }
    public func finish(expected: Int) throws {
        try checkUsable()
        guard try savedLine() == nil, count == expected, count > 0 else { throw SyncFailure.invalid("route_count_incomplete") }
        // Validation first: a rejected count leaves the stored prefix untouched.
        if !pending.isEmpty { try flush() }
        try writer.synchronize()
        synchronizations += 1; lastFlush = clock(); sealed = true
    }
}

extension OutboxStore {
    public func beginFactCapture() throws -> String {
        let file = try savePayload(Data(), suffix: "fact-capture.ndjson")
        var next = state; next.factCaptureFiles = (next.factCaptureFiles ?? []) + [file]; try commit(next); return file
    }
    public func routeCapture(_ routeID: String, expected: Int) throws -> RouteCaptureCheckpoint {
        guard (0...1_000_000).contains(expected) else { throw SyncFailure.invalid("route_point_limit") }
        if let capture = state.routeCaptures?[routeID] {
            if capture.expectedCount == expected { return capture }
            guard !capture.completed, expected >= capture.pointCount else { throw SyncFailure.localState("route_count_changed") }
            var extended = capture; extended.expectedCount = expected
            var next = state; next.routeCaptures?[routeID] = extended; try commit(next)
            return extended
        }
        let file = try savePayload(Data(), suffix: "route-capture.ndjson")
        let capture = RouteCaptureCheckpoint(file: file, expectedCount: expected)
        var next = state; if next.routeCaptures == nil { next.routeCaptures = [:] }; next.routeCaptures?[routeID] = capture
        try commit(next); return capture
    }
    public func updateRouteCapture(_ routeID: String, count: Int, completed: Bool, error: String? = nil) throws {
        var next = state
        guard var capture = next.routeCaptures?[routeID], count >= 0, !completed || count == capture.expectedCount && count > 0 else { throw SyncFailure.localState("invalid_route_capture") }
        capture.pointCount = count; capture.completed = completed; capture.error = error
        next.routeCaptures?[routeID] = capture; try commit(next)
    }
    public func updateRouteCaptureIfPresent(_ routeID: String, count: Int, error: String) throws {
        guard state.routeCaptures?[routeID] != nil else { return }
        try updateRouteCapture(routeID, count: count, completed: false, error: error)
    }
}
