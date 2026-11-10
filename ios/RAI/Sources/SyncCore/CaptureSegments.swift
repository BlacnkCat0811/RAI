import Foundation
import CryptoKit

// P2 batch capture primitives.
//
// A segment is one immutable, self-describing Outbox file holding many captured
// HealthKit objects instead of one small file per sample. Its layout is:
//
//   frame  := uint32 BE headerLength | header JSON | payload bytes
//   seal   := seal JSON | uint32 BE sealLength
//   seal JSON := {"byteLength":B,"format":2,"recordCount":N,"sha256":"<hex>"}
//
// A payload is either the exact NSKeyedArchiver output of one HealthKit object
// (header key `encoding` absent, format 1 segments) or the canonical JSON bytes
// of one `NormalizedFact` (header `"encoding":"normalized"`, format 2). Both are
// immutable, self-verifying and read back through the same frame parser; a
// reader accepts either format so an upgraded build keeps every older segment.
//
// `sha256` covers exactly the frame region `[0, B)`; the seal trailer is not
// part of its own digest. A file without a readable trailer is not sealed and
// can never be referenced by a committed fact, so a crash during capture or
// during sealing leaves an unreferenced orphan rather than a partial fact.
//
// Nothing here re-encodes a payload: an archive record stores the exact
// NSKeyedArchiver output, so a fact keeps the same identity hash and the same
// `DiscoveryEvent.id` whether it is referenced as an old `archiveFile` or as a
// segment record. A normalized record stores the canonical JSON of the fact
// itself, which is what the bundle is assembled from — no archive is produced
// or consumed on that path at all.

/// How a captured record's bytes are to be interpreted.
public enum CaptureRecordEncoding: String, Sendable, Equatable, Codable {
    /// Exact `NSKeyedArchiver` output of one HealthKit object (format 1).
    case archive
    /// Canonical JSON bytes of one `NormalizedFact` (format 2).
    case normalized
}

/// One captured object on its way into a segment.
public struct CapturedFact: Sendable, Equatable {
    public let type: String
    public let uuid: String
    /// The record payload. Named `archive` for source compatibility with the
    /// archive-only format; read `payload` to avoid implying a serialization.
    public let archive: Data
    public let identityHash: String?
    public let encoding: CaptureRecordEncoding
    public var payload: Data { archive }
    public init(type: String, uuid: String, archive: Data, identityHash: String? = nil,
                encoding: CaptureRecordEncoding = .archive) {
        self.type = type; self.uuid = uuid; self.archive = archive; self.identityHash = identityHash
        self.encoding = encoding
    }
}

/// Batching policy for a segment writer. The first limit reached wins; the
/// interval is evaluated when a record arrives, so no timer thread is needed.
public struct CaptureSegmentPolicy: Sendable, Equatable {
    public var maxRecords: Int
    public var maxBytes: Int
    public var maxInterval: TimeInterval
    /// Batched policy, or the per-record policy when the P2 rollback switch is
    /// set for an acceptance baseline.
    public static var standard: CaptureSegmentPolicy {
        AcceptanceSwitches.legacyCaptureIO
            ? CaptureSegmentPolicy(maxRecords: 1, maxBytes: 1, maxInterval: 1)
            : CaptureSegmentPolicy()
    }
    public init(maxRecords: Int = 512, maxBytes: Int = 262_144, maxInterval: TimeInterval = 0.25) {
        self.maxRecords = maxRecords; self.maxBytes = maxBytes; self.maxInterval = maxInterval
    }
    var isValid: Bool { maxRecords > 0 && maxBytes > 0 && maxInterval > 0 }
}

/// Where one record lives inside a segment.
public struct SegmentRecordLocation: Sendable, Equatable {
    public let offset: Int64
    public let length: Int64
    public let archiveSHA256: String
    public init(offset: Int64, length: Int64, archiveSHA256: String) {
        self.offset = offset; self.length = length; self.archiveSHA256 = archiveSHA256
    }
}

/// Sealed boundary of one segment: total frame bytes, record count and digest.
public struct CaptureSegmentSeal: Codable, Sendable, Equatable {
    public let file: String
    public let byteLength: Int64
    public let recordCount: Int
    public let sha256: String
    public init(file: String, byteLength: Int64, recordCount: Int, sha256: String) {
        self.file = file; self.byteLength = byteLength; self.recordCount = recordCount; self.sha256 = sha256
    }
}

public enum CaptureSegmentFormat {
    public static let suffix = ".capture-segment"
    public static let temporarySuffix = ".capture-segment.tmp"
    /// Format written by this build. Readers accept every format in
    /// `readableFormats`, so a v1 segment stays readable after an upgrade.
    public static let version: Int64 = 2
    static let readableFormats: ClosedRange<Int64> = 1...2
    static let maximumHeaderBytes = 1 << 20
    static let maximumSealBytes = 1 << 16
}

/// Bounded, append-only segment writer. Records are buffered and written with
/// one `synchronize()` per batch (512 records / 256 KiB / 250 ms by default).
/// `seal()` writes the trailer and publishes the final file name; the writer is
/// then spent. A writer that is never sealed leaves an unreferenced temporary
/// file, which is exactly the recoverable "batch not sealed" state.
///
/// `@unchecked Sendable`: the writer is owned by one capture task at a time (the
/// HealthKit query loop). It is handed across an actor boundary only to be used
/// by that single owner, and `OutboxStore` keeps its id in
/// `activeCaptureSegments` so GC never sweeps a file under a live writer.
public final class CaptureSegmentWriter: @unchecked Sendable {
    public let id: String
    public let policy: CaptureSegmentPolicy
    private let directory: URL
    private let handle: FileHandle
    private let clock: @Sendable () -> Date
    private var pending = Data()
    private var pendingRecords = 0
    private var hasher = SHA256()
    private var lastFlush: Date
    private var sealed = false
    private var poisoned = false
    public private(set) var recordCount = 0
    /// Logical length of every frame appended so far, including buffered ones.
    public private(set) var logicalLength: Int64 = 0
    /// Length actually written and synchronized to disk.
    public private(set) var durableLength: Int64 = 0
    public private(set) var flushes = 0
    public private(set) var synchronizations = 0
    public var pendingRecordCount: Int { pendingRecords }
    public var pendingByteCount: Int { pending.count }
    public var fileName: String { id + CaptureSegmentFormat.suffix }
    public var temporaryFileName: String { id + CaptureSegmentFormat.temporarySuffix }
    init(directory: URL, policy: CaptureSegmentPolicy, clock: @escaping @Sendable () -> Date) throws {
        self.directory = directory; self.policy = policy; self.clock = clock; self.lastFlush = clock()
        id = Wire.id()
        guard policy.isValid else { throw SyncFailure.invalid("invalid_capture_segment_policy") }
        let temporary = directory.appendingPathComponent(id + CaptureSegmentFormat.temporarySuffix)
        try OutboxStore.write(Data(), to: temporary)
        handle = try FileHandle(forWritingTo: temporary)
    }
    // A pending tail is intentionally not flushed here: an abandoned writer must
    // not publish records the caller never sealed.
    deinit { try? handle.close() }
    private func checkWritable() throws {
        guard !poisoned else { throw SyncFailure.localState("capture_segment_writer_failed") }
        guard !sealed else { throw SyncFailure.invalid("capture_segment_sealed") }
    }
    @discardableResult
    public func append(_ fact: CapturedFact) throws -> SegmentRecordLocation {
        try checkWritable()
        guard !fact.archive.isEmpty else { throw SyncFailure.invalid("empty_capture_archive") }
        guard recordCount < 100_000 else { throw SyncFailure.invalid("capture_segment_limit") }
        let digest = Wire.hash(fact.archive)
        var fields: [(String, JSON)] = [
            ("archiveLength", .int(Int64(fact.archive.count))), ("archiveSHA256", .string(digest)),
            ("type", .string(fact.type)), ("uuid", .string(fact.uuid)),
        ]
        if let identityHash = fact.identityHash { fields.append(("identityHash", .string(identityHash))) }
        // Absent means archive, so a format 1 header keeps its exact bytes.
        if fact.encoding != .archive { fields.append(("encoding", .string(fact.encoding.rawValue))) }
        let header = try Wire.object(fields)
        guard header.count <= CaptureSegmentFormat.maximumHeaderBytes else { throw SyncFailure.invalid("capture_segment_header_too_large") }
        var frame = Data()
        frame.appendBigEndian(UInt32(header.count)); frame += header; frame += fact.archive
        if pendingRecords > 0, clock().timeIntervalSince(lastFlush) >= policy.maxInterval { try flush() }
        let location = SegmentRecordLocation(offset: logicalLength, length: Int64(frame.count), archiveSHA256: digest)
        pending += frame; pendingRecords += 1; recordCount += 1; logicalLength += Int64(frame.count)
        hasher.update(data: frame)
        if pendingRecords >= policy.maxRecords || pending.count >= policy.maxBytes { try flush() }
        return location
    }
    public func append(contentsOf facts: [CapturedFact]) throws { for fact in facts { try append(fact) } }
    /// Write and synchronize the buffered tail once.
    public func flush() throws {
        try checkWritable()
        guard !pending.isEmpty else { return }
        do {
            try handle.write(contentsOf: pending)
            try handle.synchronize()
        } catch {
            // Bytes may already be on disk. Stop touching the file so a later
            // call cannot duplicate a partial frame; this writer is spent.
            poisoned = true; pending.removeAll(); pendingRecords = 0
            throw SyncFailure.localState("capture_segment_write_failed")
        }
        durableLength += Int64(pending.count)
        pending.removeAll(keepingCapacity: true); pendingRecords = 0
        flushes += 1; synchronizations += 1; lastFlush = clock()
    }
    /// Write the seal trailer, synchronize, and publish the final file name.
    @discardableResult
    public func seal() throws -> CaptureSegmentSeal {
        try checkWritable()
        guard recordCount > 0 else { throw SyncFailure.invalid("empty_capture_segment") }
        if !pending.isEmpty { try flush() }
        let digest = Wire.hex(Data(hasher.finalize()))
        let trailer = try Wire.object([("byteLength", .int(logicalLength)), ("format", .int(CaptureSegmentFormat.version)),
                                       ("recordCount", .int(Int64(recordCount))), ("sha256", .string(digest))])
        guard trailer.count <= CaptureSegmentFormat.maximumSealBytes else { throw SyncFailure.invalid("capture_segment_header_too_large") }
        var tail = trailer; tail.appendBigEndian(UInt32(trailer.count))
        do {
            try handle.write(contentsOf: tail)
            try handle.synchronize()
        } catch {
            poisoned = true
            throw SyncFailure.localState("capture_segment_write_failed")
        }
        synchronizations += 1; sealed = true
        try? handle.close()
        let seal = CaptureSegmentSeal(file: fileName, byteLength: logicalLength, recordCount: recordCount, sha256: digest)
        do {
            try FileManager.default.moveItem(at: directory.appendingPathComponent(temporaryFileName),
                                            to: directory.appendingPathComponent(fileName))
        } catch {
            // The sealed bytes stay under the temporary name. They are not
            // publishable and no event may reference them.
            throw SyncFailure.localState("capture_segment_publish_failed")
        }
        return seal
    }
    /// Drop the temporary file of an abandoned writer. Never touches a sealed
    /// file: sealed bytes are reclaimed only through the GC sweep.
    public func discard() {
        try? handle.close()
        try? FileManager.default.removeItem(at: directory.appendingPathComponent(temporaryFileName))
    }
}

/// Verified segment identity, cached per store instance and invalidated by a
/// size change. Sealed files are immutable by contract.
struct VerifiedCaptureSegment: Sendable, Equatable {
    let file: String
    let byteLength: Int64
    let recordCount: Int
    let sha256: String
    let fileSize: Int64
    let fileModified: Date?
}

struct CaptureSegmentRecord: Sendable, Equatable {
    let type: String
    let uuid: String
    let archiveSHA256: String
    let archive: Data
    let identityHash: String?
    let encoding: CaptureRecordEncoding
}

/// Outcome of one batch capture. `events` always reference durable bytes: an
/// already-committed fact is returned in its stored representation.
public struct CaptureSegmentResult: Sendable {
    public let file: String?
    public let byteLength: Int64
    public let recordCount: Int
    public let sha256: String?
    public let events: [DiscoveryEvent]
    public let published: [String]
    public var isEmpty: Bool { file == nil }
}

extension OutboxStore {
    /// Open a bounded writer for a new segment. The file is registered as
    /// in-flight until it is sealed or abandoned, so GC can prove that no writer
    /// is still appending to it.
    public func openCaptureSegment(_ policy: CaptureSegmentPolicy = .standard,
                                   clock: @escaping @Sendable () -> Date = Date.init) throws -> CaptureSegmentWriter {
        guard policy.isValid else { throw SyncFailure.invalid("invalid_capture_segment_policy") }
        let writer = try CaptureSegmentWriter(directory: directory, policy: policy, clock: clock)
        activeCaptureSegments.insert(writer.id)
        return writer
    }
    public func abandonCaptureSegment(_ id: String) { activeCaptureSegments.remove(id) }
    /// Capture one bounded batch of objects of one type into one sealed segment
    /// and, by default, commit their facts in a single atomic state write.
    ///
    /// `publish: false` seals the segment and returns events for the caller to
    /// hand to `stageDiscovery` (the discovery-cursor path), which commits them
    /// together with the journal. The events must then be staged promptly: until
    /// they are committed, the segment is an unreferenced orphan.
    @discardableResult
    /// `workoutID` is optional because the library-wide discovery stream
    /// archives facts before their parent Workout is known; those events are
    /// returned unresolved and bound later by `associateDiscoveredFacts`.
    public func captureFactSegment(type: String, workoutID: String?, records: [CapturedFact],
                                   publish: Bool = true, policy: CaptureSegmentPolicy = .standard) throws -> CaptureSegmentResult {
        guard records.count <= 1000 else { throw SyncFailure.localState("discovery_batch_limit") }
        guard records.allSatisfy({ $0.type == type }) else { throw SyncFailure.localState("discovery_type_mismatch") }
        guard !records.isEmpty else {
            return CaptureSegmentResult(file: nil, byteLength: 0, recordCount: 0, sha256: nil, events: [], published: [])
        }
        let writer = try openCaptureSegment(policy)
        defer { activeCaptureSegments.remove(writer.id) }
        var locations: [SegmentRecordLocation] = []
        locations.reserveCapacity(records.count)
        for record in records { locations.append(try writer.append(record)) }
        let seal = try writer.seal()
        let events = zip(records, locations).map { record, location in
            DiscoveryEvent(type: record.type, uuid: record.uuid, deleted: false, archiveFile: nil,
                           archiveHash: location.archiveSHA256, workoutID: workoutID, identityHash: record.identityHash,
                           segmentFile: seal.file, segmentOffset: location.offset, segmentLength: location.length)
        }
        guard publish else {
            return CaptureSegmentResult(file: seal.file, byteLength: seal.byteLength, recordCount: seal.recordCount,
                                        sha256: seal.sha256, events: events, published: [])
        }
        let published = try publishCapturedFacts(events)
        let committed = Set(published)
        let stored = events.map { event in committed.contains(event.id) ? event : (state.discovery?.events[event.id] ?? event) }
        return CaptureSegmentResult(file: seal.file, byteLength: seal.byteLength, recordCount: seal.recordCount,
                                    sha256: seal.sha256, events: stored, published: published)
    }
    /// One atomic commit for already-archived facts. This is the shared body of
    /// `recordCapturedFacts`: same generation, parent-binding and conflict rules.
    @discardableResult
    func publishCapturedFacts(_ events: [DiscoveryEvent]) throws -> [String] {
        guard events.count <= 1000 else { throw SyncFailure.localState("discovery_batch_limit") }
        var next = state; var discovery = next.discovery ?? DiscoveryState()
        var published: [String] = []
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
                published.append(event.id)
            }
        }
        next.discovery = discovery; try commit(next)
        return published
    }
    /// Capture normalized facts into one sealed segment (format 2 records).
    ///
    /// This is the capture entry point the bundle path uses: the fact is
    /// already the shape the bundle needs, so nothing is serialized per sample.
    @discardableResult
    public func captureNormalizedFacts(type: String, workoutID: String?, facts: [NormalizedFact],
                                       publish: Bool = true,
                                       policy: CaptureSegmentPolicy = .standard) throws -> CaptureSegmentResult {
        guard facts.allSatisfy({ $0.type == type }) else { throw SyncFailure.localState("discovery_type_mismatch") }
        guard facts.allSatisfy({ !$0.deleted }) else { throw SyncFailure.localState("deleted_fact_requires_tombstone") }
        var records: [CapturedFact] = []
        records.reserveCapacity(facts.count)
        for fact in facts {
            records.append(CapturedFact(type: fact.type, uuid: fact.uuid, archive: try fact.encoded(),
                                        identityHash: fact.identityHash, encoding: .normalized))
        }
        return try captureFactSegment(type: type, workoutID: workoutID, records: records,
                                      publish: publish, policy: policy)
    }

    /// The normalized fact behind one committed event, when it is stored as a
    /// format 2 record.
    ///
    /// Nil means the event carries an archive instead (a format 1 segment or a
    /// legacy single file); the caller may still read that through
    /// `discoveryArchiveBytes`. A corrupt or mismatched record throws rather
    /// than returning nil, so a damaged fact is never silently skipped.
    public func discoveryNormalizedFact(_ event: DiscoveryEvent) throws -> NormalizedFact? {
        guard let file = event.segmentFile, let offset = event.segmentOffset, let length = event.segmentLength else {
            return nil
        }
        let record = try readCaptureSegmentRecord(file: file, offset: offset, length: length)
        guard record.type == event.type, record.uuid == event.uuid else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        if let digest = event.archiveSHA256, digest != record.archiveSHA256 {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        guard record.encoding == .normalized else { return nil }
        let fact = try NormalizedFact.decode(record.archive)
        guard fact.type == event.type, fact.uuid == event.uuid else {
            throw SyncFailure.localState("corrupt_normalized_fact")
        }
        return fact
    }

    /// Segment half of `verifyDiscoveryArchive`. The old `archiveFile` path keeps
    /// its original logic, so legacy and segment facts can be mixed freely.
    func verifyCaptureSegmentRecord(_ event: DiscoveryEvent) throws {
        guard let file = event.segmentFile, let offset = event.segmentOffset, let length = event.segmentLength else {
            throw SyncFailure.localState("missing_discovery_archive")
        }
        guard !event.deleted else { throw SyncFailure.localState("invalid_discovery_archive_reference") }
        let seal = try verifiedCaptureSegment(file)
        guard offset >= 0, length > 0, offset + length <= seal.byteLength else { throw SyncFailure.localState("corrupt_discovery_archive") }
        let record = try readCaptureSegmentRecord(file: file, offset: offset, length: length)
        guard record.type == event.type, record.uuid == event.uuid else { throw SyncFailure.localState("corrupt_discovery_archive") }
        if let digest = event.archiveSHA256, digest != record.archiveSHA256 { throw SyncFailure.localState("corrupt_discovery_archive") }
    }
    /// Read the exact archived bytes of a fact, for both representations.
    public func capturedArchive(_ event: DiscoveryEvent) throws -> Data {
        if let file = event.segmentFile, let offset = event.segmentOffset, let length = event.segmentLength {
            return try capturedArchive(file: file, offset: offset, length: length, expectedSHA256: event.archiveSHA256)
        }
        guard let file = event.archiveFile else { throw SyncFailure.localState("missing_discovery_archive") }
        let data = try Data(contentsOf: try fileURL(file))
        if let digest = event.archiveSHA256, Wire.hash(data) != digest { throw SyncFailure.localState("corrupt_discovery_archive") }
        return data
    }
    /// Bytes of one discovery event's archived object, from either the legacy
    /// single file or a sealed segment. Nil when the reference cannot be
    /// verified (missing file, digest mismatch, unsealed/absent segment).
    /// Callers that can fall back to a fresh HealthKit read should treat nil as
    /// "not reusable" rather than as an error.
    public func discoveryArchiveBytes(_ event: DiscoveryEvent) throws -> Data? {
        guard event.archiveFile != nil || event.hasSegmentReference else { return nil }
        do { return try capturedArchive(event) } catch { return nil }
    }
    public func capturedArchive(file: String, offset: Int64, length: Int64, expectedSHA256: String? = nil) throws -> Data {
        let seal = try verifiedCaptureSegment(file)
        guard offset >= 0, length > 0, offset + length <= seal.byteLength else { throw SyncFailure.localState("corrupt_discovery_archive") }
        let record = try readCaptureSegmentRecord(file: file, offset: offset, length: length)
        if let expectedSHA256, expectedSHA256 != record.archiveSHA256 { throw SyncFailure.localState("corrupt_discovery_archive") }
        return record.archive
    }
    /// Seal, count and whole-segment digest, verified against the bytes on disk.
    func verifiedCaptureSegment(_ file: String) throws -> VerifiedCaptureSegment {
        let url = try fileURL(file)
        guard FileManager.default.fileExists(atPath: url.path) else { throw SyncFailure.localState("missing_discovery_archive") }
        guard let attributes = try? FileManager.default.attributesOfItem(atPath: url.path),
              let size = (attributes[.size] as? NSNumber)?.int64Value else { throw SyncFailure.localState("missing_discovery_archive") }
        let modified = attributes[.modificationDate] as? Date
        // Sealed segments are immutable, so a verified seal is cached. The key
        // includes size and modification date: an in-place rewrite of the same
        // size invalidates the cache instead of being trusted.
        if let cached = verifiedCaptureSegments[file], cached.fileSize == size, cached.fileModified == modified { return cached }
        let verified = try Self.readCaptureSegmentSeal(url: url, file: file, fileSize: size, fileModified: modified)
        verifiedCaptureSegments[file] = verified
        return verified
    }
    private func readCaptureSegmentRecord(file: String, offset: Int64, length: Int64) throws -> CaptureSegmentRecord {
        try Self.readCaptureSegmentRecord(url: try fileURL(file), offset: offset, length: length)
    }
    /// Queue sealed-but-unreferenced segments and abandoned temporary files for
    /// the existing GC sweep. Nothing is deleted here and no judgement is
    /// loosened: a file is queued only when no committed event references it and
    /// no writer in this process is still appending to it, and `collectGarbage`
    /// re-checks references and file safety before unlinking.
    @discardableResult
    public func reclaimOrphanCaptureSegments(limit: Int = 128) throws -> Int {
        guard (1...512).contains(limit) else { throw SyncFailure.localState("invalid_gc_limit") }
        let referenced = Set((state.discovery?.events ?? [:]).values.compactMap(\.segmentFile))
        let names = (try? FileManager.default.contentsOfDirectory(atPath: directory.path)) ?? []
        var queued: [String] = []
        for name in names.sorted() where queued.count < limit {
            let isTemporary = name.hasSuffix(CaptureSegmentFormat.temporarySuffix)
            guard let id = Self.captureSegmentID(name) else { continue }
            guard !activeCaptureSegments.contains(id) else { continue }
            let isSealed = !isTemporary
            if isSealed, referenced.contains(name) { continue }
            queued.append(name)
        }
        guard !queued.isEmpty else { return 0 }
        var next = state
        next.reclaimFiles = Array(Set((next.reclaimFiles ?? []) + queued)).sorted()
        try commit(next)
        return queued.count
    }
    /// Segment id of a segment or temporary file name, or nil for other files.
    static func captureSegmentID(_ file: String) -> String? {
        if file.hasSuffix(CaptureSegmentFormat.temporarySuffix) { return String(file.dropLast(CaptureSegmentFormat.temporarySuffix.count)) }
        if file.hasSuffix(CaptureSegmentFormat.suffix) { return String(file.dropLast(CaptureSegmentFormat.suffix.count)) }
        return nil
    }
    static func readCaptureSegmentSeal(url: URL, file: String, fileSize: Int64, fileModified: Date? = nil) throws -> VerifiedCaptureSegment {
        guard fileSize > 4 else { throw SyncFailure.localState("corrupt_discovery_archive") }
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        try handle.seek(toOffset: UInt64(fileSize - 4))
        guard let lengthBytes = try handle.read(upToCount: 4), lengthBytes.count == 4 else { throw SyncFailure.localState("corrupt_discovery_archive") }
        let sealLength = Int64(lengthBytes.bigEndianUInt32)
        guard sealLength > 0, sealLength <= Int64(CaptureSegmentFormat.maximumSealBytes), fileSize >= sealLength + 4 else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        try handle.seek(toOffset: UInt64(fileSize - 4 - sealLength))
        guard let sealBytes = try handle.read(upToCount: Int(sealLength)), Int64(sealBytes.count) == sealLength else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        let seal = try decodeSegmentJSON(sealBytes)
        guard let format = seal["format"].integer, CaptureSegmentFormat.readableFormats.contains(format),
              let byteLength = seal["byteLength"].integer,
              let recordCount = seal["recordCount"].integer, let digest = seal["sha256"].text,
              byteLength >= 0, recordCount >= 0, byteLength == fileSize - 4 - sealLength else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        try handle.seek(toOffset: 0)
        var hasher = SHA256()
        var remaining = byteLength
        while remaining > 0 {
            guard let chunk = try handle.read(upToCount: Int(min(remaining, 65_536))), !chunk.isEmpty else {
                throw SyncFailure.localState("corrupt_discovery_archive")
            }
            hasher.update(data: chunk); remaining -= Int64(chunk.count)
        }
        guard Wire.hex(Data(hasher.finalize())) == digest else { throw SyncFailure.localState("corrupt_discovery_archive") }
        return VerifiedCaptureSegment(file: file, byteLength: byteLength, recordCount: Int(recordCount), sha256: digest,
                                      fileSize: fileSize, fileModified: fileModified)
    }
    static func readCaptureSegmentRecord(url: URL, offset: Int64, length: Int64) throws -> CaptureSegmentRecord {
        guard offset >= 0, length > 8 else { throw SyncFailure.localState("corrupt_discovery_archive") }
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        try handle.seek(toOffset: UInt64(offset))
        guard let lengthBytes = try handle.read(upToCount: 4), lengthBytes.count == 4 else { throw SyncFailure.localState("corrupt_discovery_archive") }
        let headerLength = Int64(lengthBytes.bigEndianUInt32)
        guard headerLength > 0, headerLength <= Int64(CaptureSegmentFormat.maximumHeaderBytes), 4 + headerLength < length else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        guard let headerBytes = try handle.read(upToCount: Int(headerLength)), Int64(headerBytes.count) == headerLength else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        let header = try decodeSegmentJSON(headerBytes)
        guard let archiveLength = header["archiveLength"].integer, archiveLength > 0,
              let digest = header["archiveSHA256"].text, let type = header["type"].text, let uuid = header["uuid"].text,
              4 + headerLength + archiveLength == length else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        guard let archive = try handle.read(upToCount: Int(archiveLength)), Int64(archive.count) == archiveLength else {
            throw SyncFailure.localState("corrupt_discovery_archive")
        }
        guard Wire.hash(archive) == digest else { throw SyncFailure.localState("corrupt_discovery_archive") }
        let encoding = header["encoding"].text.flatMap(CaptureRecordEncoding.init(rawValue:)) ?? .archive
        return CaptureSegmentRecord(type: type, uuid: uuid, archiveSHA256: digest, archive: archive,
                                    identityHash: header["identityHash"].text, encoding: encoding)
    }
    private static func decodeSegmentJSON(_ data: Data) throws -> JSON {
        do { return try JSON.decode(data) } catch { throw SyncFailure.localState("corrupt_discovery_archive") }
    }
}

private extension Data {
    mutating func appendBigEndian(_ value: UInt32) {
        var big = value.bigEndian
        Swift.withUnsafeBytes(of: &big) { append(contentsOf: $0) }
    }
    var bigEndianUInt32: UInt32 {
        guard count == 4 else { return 0 }
        return withUnsafeBytes { $0.loadUnaligned(as: UInt32.self) }.bigEndian
    }
}
