import Foundation

/// Comparison identity only. Never substitute these bytes for the immutable
/// event, upload payload or stored content hash. Native quantities have already
/// been converted to their canonical units by the HealthKit normalizer.
public enum WorkoutCoreIdentity {
    public static func canonical(_ event: JSON) throws -> Data {
        func core(_ value: JSON) -> JSON {
            switch value {
            case .object(let fields):
                var result = fields.filter { $0.key != "metadata" }.mapValues(core)
                // Signed zero is the same physical quantity. Restrict this
                // equivalence to quantity values; exact integer fields and
                // opaque/version identifiers are never converted to Double.
                if fields.count == 2, fields["unit"]?.text != nil,
                   case .number(let number) = fields["value"], number == 0 {
                    result["value"] = .int(0)
                }
                return .object(result)
            case .array(let values): return .array(values.map(core))
            default: return value
            }
        }
        // The frozen canonical codec already gives 42 and 42.0 identical
        // bytes, without rounding Int64 or changing global JSON equality.
        return try core(event).data()
    }
}

/// RAI classification, separate from the HealthKit-provided fields. Names are
/// descriptive only. A third-party writer on Watch is never native Apple data.
public enum HealthSourcePolicy {
    public static func classification(_ source: JSON, associatedWorkout: JSON? = nil) -> String {
        guard let bundle = source["bundleID"].text else { return "source_unknown" }
        let prefix = "com.apple.health."
        let suffix = bundle.hasPrefix(prefix) ? String(bundle.dropFirst(prefix.count)) : ""
        let scoped = UUID(uuidString: suffix).map { $0.uuidString != "00000000-0000-0000-0000-000000000000" } ?? false
        guard ["com.apple.health", "com.apple.Workout"].contains(bundle) || scoped else {
            return bundle.hasPrefix("com.apple.") ? "source_unknown" : "third_party"
        }
        let product = source["productType"].text
        if let product, product.range(of: #"^Watch[0-9]+,[0-9]+$"#, options: .regularExpression) == nil { return "source_unknown" }
        if let manufacturer = source["device"]["manufacturer"].text,
           !["Apple", "Apple Inc."].contains(manufacturer) { return "source_unknown" }
        if let model = source["device"]["model"].text, !model.lowercased().contains("watch") { return "source_unknown" }
        if product != nil {
            // Device-scoped Apple Health writers require actual Watch device
            // evidence, or the separately proven parent association below.
            if !scoped || source["device"]["manufacturer"].text != nil && source["device"]["model"].text != nil { return "apple_watch" }
        }
        if let parent = associatedWorkout, classification(parent) == "apple_watch",
           parent["bundleID"] == source["bundleID"], let version = source["version"].text,
           version == parent["version"].text {
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

/// Append-only capture is a referenced Outbox artifact before querying starts.
/// On restart HealthKit is queried again, comparing the saved prefix byte for
/// byte. No source points are sorted, replaced, deduplicated or rewritten.
public final class RouteCaptureFile {
    private let writer: FileHandle
    private let reader: FileHandle
    private var buffer = Data()
    private var reachedEOF = false
    public private(set) var count = 0
    public let url: URL
    public init(url: URL) throws {
        self.url = url
        if !FileManager.default.fileExists(atPath: url.path) { try OutboxStore.write(Data(), to: url) }
        writer = try FileHandle(forWritingTo: url); try writer.seekToEnd()
        reader = try FileHandle(forReadingFrom: url)
    }
    deinit { try? writer.close(); try? reader.close() }
    private func savedLine() throws -> Data? {
        while true {
            if let end = buffer.firstIndex(of: 10) {
                let line = Data(buffer[..<end]); buffer.removeSubrange(...end); return line
            }
            if reachedEOF {
                guard buffer.isEmpty else { throw SyncFailure.localState("route_capture_incomplete_line") }
                return nil
            }
            let bytes = try reader.read(upToCount: 65536) ?? Data()
            if bytes.isEmpty { reachedEOF = true } else { buffer += bytes }
        }
    }
    public func append(_ point: RoutePoint) throws {
        guard count < 1_000_000 else { throw SyncFailure.invalid("route_point_limit") }
        let bytes = try point.canonical(sequence: Int64(count))
        if let previous = try savedLine() {
            guard previous == bytes else { throw SyncFailure.localState("route_capture_changed") }
        } else {
            var line = bytes; line.append(10); try writer.write(contentsOf: line)
            // Durable before the next query element is consumed.
            try writer.synchronize()
        }
        count += 1
    }
    public func finish(expected: Int) throws {
        guard try savedLine() == nil, count == expected, count > 0 else { throw SyncFailure.invalid("route_count_incomplete") }
        try writer.synchronize()
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
    public func originalWorkout(_ id: String, event: JSON) throws -> JSON {
        guard let task = state.tasks?[id] else { throw SyncFailure.localState("unknown_workout_task") }
        if let saved = task.originalWorkout {
            guard try WorkoutCoreIdentity.canonical(saved) == WorkoutCoreIdentity.canonical(event) else {
                throw SyncFailure.localState("workout_core_change_requires_review")
            }
            return saved
        }
        try updateTask(id) { $0.originalWorkout = event }; return event
    }
}
