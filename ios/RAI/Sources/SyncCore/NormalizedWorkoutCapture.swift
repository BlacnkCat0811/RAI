import Foundation

// P4 normalized capture.
//
// The pre-P4 capture path stored one `NSKeyedArchiver` blob per HealthKit
// sample and then, to build a bundle, read those blobs back with
// `NSKeyedUnarchiver`, only to convert the resulting objects straight into the
// same JSON the bundle is made of. That is a full serialize/deserialize round
// trip per sample, and it was the dominant cost of a Workout sync.
//
// A `NormalizedFact` is the fact itself: the identity, the exact range, the
// original unit and value, the supported metadata, the native carrier and the
// source descriptor, already in the shape the bundle needs. It is stored as
// canonical JSON bytes inside the same immutable capture segment (format 2) and
// read back by decoding those bytes — no archiving, no unarchiving.
//
// Compatibility: nothing here replaces the archive representation. A format 1
// record keeps its exact bytes and its `DiscoveryEvent.id`, so old segments,
// old single-file archives and old pending bundles stay readable and unchanged.

/// Counts every HealthKit-object archive and unarchive the app performs.
///
/// The counter is deliberately global and monotonic: an acceptance run reads it
/// before and after a sync, and the difference is the number of samples that
/// still went through the serializer. It is a measurement, not a switch — no
/// code path may skip a call just to keep the number low.
public enum SampleArchiveMeter {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var archiveCalls = 0
    nonisolated(unsafe) private static var unarchiveCalls = 0
    nonisolated(unsafe) private static var objectArchiveCalls = 0

    /// One `NSKeyedArchiver.archivedData` of a HealthKit object.
    public static func recordArchive() {
        lock.lock(); archiveCalls += 1; lock.unlock()
    }
    /// One `NSKeyedUnarchiver.unarchivedObject` of a HealthKit object.
    public static func recordUnarchive() {
        lock.lock(); unarchiveCalls += 1; lock.unlock()
    }
    /// One `NSKeyedArchiver.archivedData` of a non-sample object or of a whole
    /// page (a Workout root record, a deletion tombstone, a completeness page).
    /// Counted separately: those are one object per Workout or per page, not the
    /// per-sample cost this measurement is about.
    public static func recordObjectArchive() {
        lock.lock(); objectArchiveCalls += 1; lock.unlock()
    }
    public static func snapshot() -> (archive: Int, unarchive: Int, objectArchive: Int) {
        lock.lock(); defer { lock.unlock() }
        return (archiveCalls, unarchiveCalls, objectArchiveCalls)
    }
    public static func reset() {
        lock.lock(); archiveCalls = 0; unarchiveCalls = 0; objectArchiveCalls = 0; lock.unlock()
    }
}

/// Series provenance of one multi-value sample: which parent it belongs to, how
/// many points HealthKit reported, where the points sit in the origin order, and
/// whether the points were actually captured into this bundle.
public struct NormalizedSeries: Sendable, Equatable {
    public let parentUUID: String
    public let pointCount: Int64
    public let originOrdinal: Int64
    public let completed: Bool
    public init(parentUUID: String, pointCount: Int64, originOrdinal: Int64 = 0, completed: Bool = false) {
        self.parentUUID = parentUUID; self.pointCount = pointCount
        self.originOrdinal = originOrdinal; self.completed = completed
    }
}

/// One captured fact in the shape the bundle is assembled from.
///
/// `event` is the exact integrity event the bundle carries for this fact, so
/// assembling a bundle from normalized facts is a concatenation, not a
/// re-derivation. `source` is the source descriptor the policy gate classifies,
/// kept separately because it is also needed for facts whose event is not
/// eligible. `startUs`/`endUs` keep microsecond precision and drive the same
/// ordering the archived path used (`startDate`, then uuid).
public struct NormalizedFact: Sendable, Equatable {
    /// Version of this normalized shape. A record with any other version is
    /// refused rather than guessed at.
    public static let version: Int64 = 1

    public let version: Int64
    public let type: String
    public let uuid: String
    public let deleted: Bool
    public let workoutID: String?
    public let startUs: Int64?
    public let endUs: Int64?
    /// Source descriptor: bundleID, name, version, productType, OS, device.
    public let source: JSON?
    /// The integrity event carried in the bundle (nil for a deletion tombstone).
    public let event: JSON?
    /// Original quantity: value in its original unit.
    public let quantity: JSON?
    public let metadata: JSON?
    public let series: NormalizedSeries?
    public let identityHash: String?

    public var factKey: String { type + ":" + uuid }
    /// A fact is usable in an uploaded bundle only when both its source and
    /// its integrity event were described. Discovery may retain a captured
    /// fact with no event for later diagnosis; that record is an anchor, not
    /// bundle content, and the bundle path must re-read the native object.
    public var isBundleUsable: Bool { !deleted && source != nil && event != nil }

    public init(type: String, uuid: String, deleted: Bool = false, workoutID: String? = nil,
                startUs: Int64? = nil, endUs: Int64? = nil, source: JSON? = nil, event: JSON? = nil,
                quantity: JSON? = nil, metadata: JSON? = nil, series: NormalizedSeries? = nil,
                identityHash: String? = nil) {
        self.version = Self.version
        self.type = type
        self.uuid = uuid
        self.deleted = deleted
        self.workoutID = workoutID
        self.startUs = startUs
        self.endUs = endUs
        self.source = source
        self.event = event
        self.quantity = quantity
        self.metadata = metadata
        self.series = series
        self.identityHash = identityHash
    }

    /// Canonical form. Every key is present (absent values are `null`) so the
    /// encoded bytes depend only on the fact, never on which fields happened to
    /// be populated.
    public var canonical: JSON {
        .object([
            "deleted": .bool(deleted),
            "endUs": endUs.map(JSON.int) ?? .null,
            "event": event ?? .null,
            "identityHash": identityHash.map(JSON.string) ?? .null,
            "metadata": metadata ?? .null,
            "quantity": quantity ?? .null,
            "series": series.map { .object(["completed": .bool($0.completed),
                                            "originOrdinal": .int($0.originOrdinal),
                                            "parentUUID": .string($0.parentUUID),
                                            "pointCount": .int($0.pointCount)]) } ?? .null,
            "source": source ?? .null,
            "startUs": startUs.map(JSON.int) ?? .null,
            "type": .string(type),
            "uuid": .string(uuid),
            "version": .int(version),
            "workoutID": workoutID.map(JSON.string) ?? .null,
        ])
    }

    /// The bytes stored in a format 2 capture segment.
    public func encoded() throws -> Data {
        var buffer: [UInt8] = []
        buffer.reserveCapacity(384)
        try canonical.write(into: &buffer)
        return Data(buffer)
    }

    /// Decode the bytes of a format 2 capture segment record.
    public static func decode(_ data: Data) throws -> NormalizedFact {
        let json: JSON
        do { json = try JSON.decode(data) } catch { throw SyncFailure.localState("corrupt_normalized_fact") }
        guard json["version"].integer == version else { throw SyncFailure.localState("unsupported_normalized_fact_version") }
        guard let type = json["type"].text, !type.isEmpty, let uuid = json["uuid"].text, !uuid.isEmpty else {
            throw SyncFailure.localState("corrupt_normalized_fact")
        }
        guard case .bool(let deleted) = json["deleted"] else { throw SyncFailure.localState("corrupt_normalized_fact") }
        let seriesJSON = json["series"]
        let series: NormalizedSeries?
        if case .object = seriesJSON, let parent = seriesJSON["parentUUID"].text, let points = seriesJSON["pointCount"].integer {
            series = NormalizedSeries(parentUUID: parent, pointCount: points,
                                      originOrdinal: seriesJSON["originOrdinal"].integer ?? 0,
                                      completed: seriesJSON["completed"] == .bool(true))
        } else { series = nil }
        func optional(_ key: String) -> JSON? { json[key] == .null ? nil : json[key] }
        return NormalizedFact(type: type, uuid: uuid, deleted: deleted, workoutID: json["workoutID"].text,
                              startUs: json["startUs"].integer, endUs: json["endUs"].integer,
                              source: optional("source"), event: optional("event"),
                              quantity: optional("quantity"), metadata: optional("metadata"),
                              series: series, identityHash: json["identityHash"].text)
    }

    /// Small, value-free description for metrics and evidence.
    public var summary: JSON {
        .object(["deleted": .bool(deleted), "type": .string(type), "uuid": .string(uuid),
                 "version": .int(version),
                 "hasEvent": .bool(event != nil), "hasSource": .bool(source != nil),
                 "hasSeries": .bool(series != nil),
                 "pointCount": series.map { .int($0.pointCount) } ?? .null])
    }
}
