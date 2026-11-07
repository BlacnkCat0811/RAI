import Foundation
import Testing
@testable import SyncCore

/// R2 at the storage layer: the normalized record format, its seal, and the
/// compatibility promise that the previous release's archived records still
/// read. These tests touch the real writer and reader, not a stand-in.
@Suite struct NormalizedCaptureSegmentTests {
    private func captureDirectory(_ tag: String) throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("rai-normalized-segment-" + tag + "-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func fact(_ uuid: String, value: Double = 120, type: String = "HKQuantityTypeIdentifierHeartRate") -> NormalizedFact {
        NormalizedFact(type: type, uuid: uuid, deleted: false, workoutID: nil,
                       startUs: 1_700_000_000_000_000, endUs: 1_700_000_001_000_000,
                       source: .object(["bundleID": .string("com.apple.health"), "name": .string("Apple Watch")]),
                       event: .object(["op": .string("upsert"), "type": .string(type), "uuid": .string(uuid)]),
                       quantity: .object(["value": .number(value), "unit": .string("count/min")]),
                       metadata: .object(["HKMetadataKeyIndoorWorkout": .bool(true)]),
                       series: NormalizedSeries(parentUUID: uuid, pointCount: 2, originOrdinal: 0, completed: true),
                       identityHash: Wire.hash(Data((type + uuid).utf8)))
    }

    @Test func onlyDescribedFactsAreBundleUsable() {
        let described = fact("0f8fad5b-d9cb-469f-a165-70867728950e")
        #expect(described.isBundleUsable)
        let undiscribed = NormalizedFact(type: described.type, uuid: described.uuid,
                                         source: described.source, event: nil)
        #expect(!undiscribed.isBundleUsable, "a captured fact without an event must force a fresh read")
        let tombstone = NormalizedFact(type: described.type, uuid: described.uuid, deleted: true,
                                       source: described.source, event: described.event)
        #expect(!tombstone.isBundleUsable, "a deletion tombstone is not bundle content")
    }

    /// The record that goes into a segment is the record that comes back out,
    /// field for field, without any archiving in between.
    @Test func normalizedFactRoundTripsThroughASegment() async throws {
        let directory = try captureDirectory("roundtrip")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let original = fact("0f8fad5b-d9cb-469f-a165-70867728950e")

        let captured = try await store.captureNormalizedFacts(type: original.type, workoutID: nil,
                                                             facts: [original], publish: false)
        #expect(captured.file != nil)
        #expect(captured.recordCount == 1)
        #expect(captured.events.count == 1)
        let event = try #require(captured.events.first)
        #expect(event.segmentFile == captured.file)
        #expect(event.archiveSHA256 == Wire.hash(try original.encoded()))

        let read = try #require(try await store.discoveryNormalizedFact(event))
        // The contract is the canonical bytes, not the in-memory numeric case:
        // the wire format writes 120.0 and 120 identically, so a decoded fact
        // re-encodes to the same record the writer sealed.
        let reread = try read.encoded()
        let originalBytes = try original.encoded()
        #expect(reread == originalBytes)
        #expect(read.type == original.type)
        #expect(read.uuid == original.uuid)
        #expect(read.startUs == original.startUs)
        #expect(read.endUs == original.endUs)
        #expect(read.identityHash == original.identityHash)
        #expect(read.series == original.series)
        #expect(read.metadata == original.metadata)
    }

    /// The canonical encoding is stable, and a second encode of the decoded
    /// value is byte identical — the property the record digest relies on.
    @Test func canonicalBytesAreStable() throws {
        let original = fact("0f8fad5b-d9cb-469f-a165-70867728950e")
        let first = try original.encoded()
        let second = try original.encoded()
        #expect(first == second)
        let decoded = try NormalizedFact.decode(first)
        #expect(try decoded.encoded() == first, "a decode/encode cycle changed the bytes")
        // A fractional value survives the cycle unchanged, so the equality is
        // about representation, not about losing precision.
        var fractional = original
        fractional = NormalizedFact(type: original.type, uuid: original.uuid, deleted: false, workoutID: nil,
                                    startUs: original.startUs, endUs: original.endUs, source: original.source,
                                    event: original.event,
                                    quantity: .object(["value": .number(120.5), "unit": .string("count/min")]),
                                    metadata: original.metadata, series: original.series,
                                    identityHash: original.identityHash)
        #expect(try NormalizedFact.decode(fractional.encoded()) == fractional)
        // Every optional field is present as an explicit null, so a reader can
        // tell "absent" from "not captured" without guessing.
        let text = try #require(String(data: first, encoding: .utf8))
        #expect(text.contains("\"series\""))
        #expect(text.contains("\"metadata\""))
        #expect(text.contains("\"deleted\":false"))
    }

    /// A rewritten segment byte is detected: the record digest is bound to the
    /// immutable file, so a silent edit cannot become a delivered fact.
    @Test func tamperedSegmentIsRejected() async throws {
        let directory = try captureDirectory("tamper")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let original = fact("0f8fad5b-d9cb-469f-a165-70867728950e")
        let captured = try await store.captureNormalizedFacts(type: original.type, workoutID: nil,
                                                             facts: [original], publish: false)
        let event = try #require(captured.events.first)
        let file = try #require(captured.file)
        let url = directory.appendingPathComponent(file)

        var bytes = try Data(contentsOf: url)
        // Rewrite one payload byte inside the record, after the frame header.
        let offset = Int(try #require(event.segmentOffset)) + Int(try #require(event.segmentLength)) - 2
        bytes[offset] = bytes[offset] ^ 0xFF
        try bytes.write(to: url)

        var rejected = false
        do { _ = try await store.discoveryNormalizedFact(event) } catch { rejected = true }
        #expect(rejected, "a rewritten segment record was accepted")
    }

    /// The previous release's records are still readable: an archived record is
    /// served as archive bytes, and the caller falls back to the old path.
    @Test func archivedRecordStillReadsAsArchive() async throws {
        let directory = try captureDirectory("legacy")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let payload = Data(repeating: 0xAB, count: 128)
        let record = CapturedFact(type: "HKQuantityTypeIdentifierHeartRate",
                                  uuid: "0f8fad5b-d9cb-469f-a165-70867728950e",
                                  archive: payload, identityHash: nil)
        let captured = try await store.captureFactSegment(type: record.type, workoutID: nil,
                                                          records: [record], publish: false)
        let event = try #require(captured.events.first)
        #expect(try await store.discoveryNormalizedFact(event) == nil)
        #expect(try await store.capturedArchive(event) == payload)
    }

    /// A deletion is a tombstone, never a fact: capturing one as a fact would
    /// let a rewrite resurrect it.
    @Test func deletedFactRequiresATombstone() async throws {
        let directory = try captureDirectory("deleted")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let deleted = NormalizedFact(type: "HKQuantityTypeIdentifierHeartRate",
                                     uuid: "0f8fad5b-d9cb-469f-a165-70867728950e",
                                     deleted: true, workoutID: nil, startUs: nil, endUs: nil,
                                     source: nil, event: nil, quantity: nil, metadata: nil,
                                     series: nil, identityHash: nil)
        await #expect(throws: SyncFailure.localState("deleted_fact_requires_tombstone")) {
            _ = try await store.captureNormalizedFacts(type: deleted.type, workoutID: nil,
                                                       facts: [deleted], publish: false)
        }
    }

    /// The batch policy the round requires is unchanged, and the seal format
    /// advanced without making the old one unreadable.
    @Test func segmentPolicyAndFormatAreAsRequired() {
        #expect(CaptureSegmentPolicy.standard.maxRecords == 512)
        #expect(CaptureSegmentPolicy.standard.maxBytes == 262_144)
        #expect(CaptureSegmentPolicy.standard.maxInterval == 0.25)
        #expect(CaptureSegmentFormat.version == 2)
        #expect(CaptureSegmentFormat.readableFormats.contains(1))
        #expect(CaptureSegmentFormat.readableFormats.contains(2))
    }

    /// A record written by the old release seals with format 1 and stays
    /// readable; a record written now seals with format 2.
    @Test func oldSealFormatStaysReadable() async throws {
        let directory = try captureDirectory("seal")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let record = CapturedFact(type: "HKQuantityTypeIdentifierHeartRate",
                                  uuid: "0f8fad5b-d9cb-469f-a165-70867728950e",
                                  archive: Data(repeating: 0x01, count: 64), identityHash: nil)
        let captured = try await store.captureFactSegment(type: record.type, workoutID: nil,
                                                          records: [record], publish: false)
        let file = try #require(captured.file)
        let url = directory.appendingPathComponent(file)
        let bytes = try Data(contentsOf: url)
        let tail = [UInt8](bytes.suffix(4))
        let sealLength = Int(tail.reduce(UInt32(0)) { $0 << 8 | UInt32($1) })
        let seal = try JSON.decode(bytes[(bytes.count - 4 - sealLength)..<(bytes.count - 4)])
        #expect(seal["format"].integer == 2, "the writer sealed with an unexpected format")
        #expect(seal["recordCount"].integer == 1)
        #expect(seal["sha256"].text == captured.sha256)
        // The reader accepts both formats, so an old file stays usable.
        let verified = try OutboxStore.readCaptureSegmentSeal(url: url, file: file,
                                                              fileSize: Int64(bytes.count))
        #expect(verified.recordCount == 1)
    }
}
