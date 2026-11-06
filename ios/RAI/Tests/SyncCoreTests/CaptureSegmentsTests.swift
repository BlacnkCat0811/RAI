import Foundation
import Testing
@testable import SyncCore

private func captureDirectory(_ tag: String) throws -> URL {
    let path = FileManager.default.temporaryDirectory.appendingPathComponent("rai-capture-\(tag)-" + Wire.id())
    try FileManager.default.createDirectory(at: path, withIntermediateDirectories: true)
    return path
}

private func syntheticArchives(_ count: Int, tag: String = "synthetic") -> [Data] {
    (0..<count).map { index in Data("\(tag)-\(index)-".utf8) + Data(repeating: UInt8(index % 251), count: 200 + index) }
}

/// P2 batch capture: one sealed segment carries many captured objects, and the
/// crash points of §6.3 never turn a partial file into a committed fact.
@Suite struct CaptureSegmentsTests {
    @Test func sealedSegmentRoundTripsExactArchiveBytesAndEventIdentity() async throws {
        let directory = try captureDirectory("segment"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), parent = Wire.id()
        let archives = syntheticArchives(5)
        let records = archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0, identityHash: Wire.hash($0)) }
        let result = try await store.captureFactSegment(type: "heartRate", workoutID: parent, records: records)
        #expect(result.recordCount == 5)
        #expect(result.published.count == 5)
        #expect(result.events.count == 5)
        guard let file = result.file else { Issue.record("segment not sealed"); return }
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
        let seal = try await store.verifiedCaptureSegment(file)
        #expect(seal.recordCount == 5)
        #expect(seal.byteLength == result.byteLength)
        #expect(seal.sha256 == result.sha256)
        for (index, event) in result.events.enumerated() {
            #expect(event.segmentFile == file)
            #expect(event.segmentLength != nil && event.segmentLength! > 0)
            #expect(event.archiveSHA256 == Wire.hash(archives[index]))
            // The archive bytes are stored verbatim, never re-encoded.
            #expect(try await store.capturedArchive(event) == archives[index])
            // Segment and legacy forms of the same fact share one identity.
            let legacy = DiscoveryEvent(type: "heartRate", uuid: event.uuid, deleted: false, archiveFile: "unused.healthkit.archive",
                                        archiveHash: Wire.hash(archives[index]), workoutID: parent, identityHash: Wire.hash(archives[index]))
            #expect(legacy.id == event.id)
            try await store.verifyDiscoveryArchive(event)
        }
        // One commit for the whole batch: one generation bump per new fact, as
        // `recordCapturedFacts` has always done.
        #expect(await store.discoverySnapshot().events.count == 5)
        #expect(await store.discoverySnapshot().work[parent]?.generation == 5)
        #expect(await store.discoverySnapshot().events[result.events[0].id]?.status == "pending")
    }

    @Test func unsealedBatchPublishesNoFactAndKeepsItsWriterExclusive() async throws {
        let directory = try captureDirectory("unsealed"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), parent = Wire.id()
        let archives = syntheticArchives(3)
        let writer = try await store.openCaptureSegment(CaptureSegmentPolicy(maxRecords: 512, maxBytes: 262_144, maxInterval: 3600))
        for archive in archives {
            try writer.append(CapturedFact(type: "heartRate", uuid: Wire.id(), archive: archive))
        }
        #expect(writer.recordCount == 3)
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(writer.temporaryFileName).path))
        // §6.3: a batch that was never sealed is not a committed fact, and no
        // event can reference it.
        #expect(await store.discoverySnapshot().events.isEmpty)
        #expect(try await store.reclaimOrphanCaptureSegments() == 0)
        let forged = DiscoveryEvent(type: "heartRate", uuid: Wire.id(), deleted: false, archiveHash: Wire.hash(archives[0]),
                                    workoutID: parent, segmentFile: writer.fileName, segmentOffset: 0, segmentLength: 16)
        await #expect(throws: (any Error).self) { try await store.verifyDiscoveryArchive(forged) }
        // Sealing publishes the file, but with no reference written it is still
        // an orphan rather than a fact.
        let seal = try writer.seal()
        await store.abandonCaptureSegment(writer.id)
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(seal.file).path))
        #expect(!FileManager.default.fileExists(atPath: directory.appendingPathComponent(writer.temporaryFileName).path))
        #expect(await store.discoverySnapshot().events.isEmpty)
        #expect(try await store.reclaimOrphanCaptureSegments() == 1)
    }

    @Test func sealedOrphanIsReclaimedButPublishedSegmentSurvives() async throws {
        let directory = try captureDirectory("orphan"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), parent = Wire.id()
        let archives = syntheticArchives(4)
        let records = archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0) }
        // Sealed but unpublished: a crash between seal and journal write.
        let orphan = try await store.captureFactSegment(type: "heartRate", workoutID: parent, records: records, publish: false)
        guard let orphanFile = orphan.file else { Issue.record("orphan not sealed"); return }
        #expect(orphan.published.isEmpty)
        #expect(await store.discoverySnapshot().events.isEmpty)
        try await store.verifyDiscoveryArchive(orphan.events[0])
        // Published: referenced by committed facts.
        let kept = try await store.captureFactSegment(type: "heartRate", workoutID: parent, records: records)
        guard let keptFile = kept.file else { Issue.record("kept not sealed"); return }
        #expect(try await store.reclaimOrphanCaptureSegments() == 1)
        #expect(try await store.collectGarbage(dryRun: true) == 1)
        #expect(try await store.collectGarbage(dryRun: false) == 1)
        #expect(!FileManager.default.fileExists(atPath: directory.appendingPathComponent(orphanFile).path))
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(keptFile).path))
        #expect(try await store.capturedArchive(kept.events[0]) == archives[0])
        #expect(await store.discoverySnapshot().events.count == 4)
    }

    @Test func referencedSegmentCannotBeSweptEvenWhenQueued() async throws {
        let directory = try captureDirectory("referenced"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), parent = Wire.id()
        let archives = syntheticArchives(2)
        let kept = try await store.captureFactSegment(type: "heartRate", workoutID: parent,
                                                      records: archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0) })
        guard let file = kept.file else { Issue.record("segment not sealed"); return }
        // Queue the still-referenced file by hand: the GC re-check must refuse.
        var state = await store.snapshot(); state.reclaimFiles = [file]
        try JSONEncoder().encode(state).write(to: directory.appendingPathComponent("state.json"))
        let reopened = try OutboxStore(directory: directory)
        await #expect(throws: SyncFailure.self) { try await reopened.collectGarbage(dryRun: false) }
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
        #expect(try await reopened.capturedArchive(kept.events[0]) == archives[0])
    }

    @Test func corruptSegmentIsNeverAcceptedAsAFact() async throws {
        let directory = try captureDirectory("corrupt"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), parent = Wire.id()
        let archives = syntheticArchives(3)
        let captured = try await store.captureFactSegment(type: "heartRate", workoutID: parent,
                                                          records: archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0) })
        guard let file = captured.file else { Issue.record("segment not sealed"); return }
        let url = directory.appendingPathComponent(file)
        let sealed = try Data(contentsOf: url)
        // A single flipped archive byte breaks the whole-segment digest.
        var flipped = sealed; flipped[20] ^= 0xFF
        try flipped.write(to: url)
        let reopened = try OutboxStore(directory: directory)
        await #expect(throws: (any Error).self) { try await reopened.capturedArchive(captured.events[0]) }
        // A truncated tail is not a seal either.
        try sealed.prefix(sealed.count - 8).write(to: url)
        let truncated = try OutboxStore(directory: directory)
        await #expect(throws: (any Error).self) { try await truncated.verifyDiscoveryArchive(captured.events[0]) }
        // An offset/length outside the sealed boundary is rejected.
        try sealed.write(to: url)
        let valid = try OutboxStore(directory: directory)
        await #expect(throws: (any Error).self) {
            try await valid.capturedArchive(file: file, offset: captured.byteLength - 4, length: 64)
        }
        #expect(try await valid.capturedArchive(file: file, offset: captured.events[1].segmentOffset!,
                                                length: captured.events[1].segmentLength!) == archives[1])
    }

    @Test func unreadableSegmentBlocksPromotionAndKeepsOldAuthority() async throws {
        let directory = try captureDirectory("unreadable"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), query = DiscoveryQuery(type: "heartRate")
        _ = try await store.discoveryFlow(query)
        let parent = Wire.id()
        let archives = syntheticArchives(2)
        let captured = try await store.captureFactSegment(type: "heartRate", workoutID: parent,
                                                          records: archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0) },
                                                          publish: false)
        guard let file = captured.file else { Issue.record("segment not sealed"); return }
        let first = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: captured.events, hasMore: true)
        try await store.promoteDiscovery(query, batchID: first)
        #expect(await store.discoverySnapshot().flows[query.key]?.anchor == Data([1]))
        // The protected file becomes unreadable: replaced by a directory, the way
        // a locked or unmounted container looks to the reader.
        let url = directory.appendingPathComponent(file)
        try FileManager.default.removeItem(at: url)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: false)
        // Re-verifying the same facts cannot stage or promote, and the old anchor
        // stays authoritative: no successful step is faked.
        await #expect(throws: (any Error).self) {
            try await store.stageDiscovery(query, previousAnchor: Data([1]), anchor: Data([2]), events: captured.events, hasMore: false)
        }
        #expect(await store.discoverySnapshot().flows[query.key]?.anchor == Data([1]))
        #expect(await store.discoverySnapshot().flows[query.key]?.staged == nil)
        #expect(await store.discoverySnapshot().events.count == 2)
        await #expect(throws: (any Error).self) { try await store.capturedArchive(captured.events[0]) }
        #expect(try await store.discoveryArchiveBytes(captured.events[0]) == nil)
        // A legacy event whose file is gone reports "not reusable" the same way.
        let gone = DiscoveryEvent(type: "heartRate", uuid: Wire.id(), deleted: false, archiveFile: "absent.healthkit.archive",
                                  archiveHash: Wire.hash(Data([1])), workoutID: parent)
        #expect(try await store.discoveryArchiveBytes(gone) == nil)
        // GC refuses a queued path it cannot prove is a regular file.
        var state = await store.snapshot(); state.reclaimFiles = [file]
        try JSONEncoder().encode(state).write(to: directory.appendingPathComponent("state.json"))
        let reopened = try OutboxStore(directory: directory)
        await #expect(throws: SyncFailure.self) { try await reopened.collectGarbage(dryRun: false) }
    }

    @Test func mixedLegacyArchiveAndSegmentFactsRecoverAfterRestart() async throws {
        let directory = try captureDirectory("mixed"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), query = DiscoveryQuery(type: "heartRate")
        _ = try await store.discoveryFlow(query)
        let parent = Wire.id()
        let legacyBytes = Data("legacy-archive".utf8)
        let legacyFile = try await store.savePayload(legacyBytes)
        let legacy = DiscoveryEvent(type: "heartRate", uuid: Wire.id(), deleted: false, archiveFile: legacyFile, archiveHash: Wire.hash(legacyBytes))
        let archives = syntheticArchives(3)
        let captured = try await store.captureFactSegment(type: "heartRate", workoutID: parent,
                                                          records: archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0) },
                                                          publish: false)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]),
                                                   events: [legacy] + captured.events, hasMore: false)
        let reopened = try OutboxStore(directory: directory)
        try await reopened.promoteDiscovery(query, batchID: batch)
        #expect(await reopened.discoverySnapshot().events.count == 4)
        #expect(await reopened.discoverySnapshot().flows[query.key]?.anchor == Data([1]))
        // Both representations stay readable, and the legacy bytes are untouched.
        #expect(try await reopened.capturedArchive(legacy) == legacyBytes)
        #expect(try Data(contentsOf: directory.appendingPathComponent(legacyFile)) == legacyBytes)
        for (index, event) in captured.events.enumerated() {
            #expect(try await reopened.capturedArchive(event) == archives[index])
            try await reopened.verifyDiscoveryArchive(event)
        }
        #expect(await reopened.discoverySnapshot().events[legacy.id]?.archiveFile == legacyFile)
        #expect(await reopened.discoverySnapshot().events[captured.events[0].id]?.segmentFile == captured.file)
    }

    @Test func legacyStateFileWithoutSegmentFieldsStillDecodes() async throws {
        let directory = try captureDirectory("legacy-schema"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), query = DiscoveryQuery(type: "heartRate")
        _ = try await store.discoveryFlow(query)
        let bytes = Data("legacy-archive".utf8), file = try await store.savePayload(bytes)
        let event = DiscoveryEvent(type: "heartRate", uuid: Wire.id(), deleted: false, archiveFile: file, archiveHash: Wire.hash(bytes), workoutID: Wire.id())
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [event], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batch)
        // The on-disk schema only grows when a fact uses a segment: a legacy
        // state.json carries no segment keys at all (F13).
        let state = try Data(contentsOf: directory.appendingPathComponent("state.json"))
        #expect(!String(decoding: state, as: UTF8.self).contains("segmentFile"))
        let reopened = try OutboxStore(directory: directory)
        #expect(await reopened.discoverySnapshot().events.count == 1)
        #expect(await reopened.discoverySnapshot().events[event.id]?.segmentFile == nil)
        #expect(try await reopened.capturedArchive(event) == bytes)
        try await reopened.verifyDiscoveryArchive(event)
        // A pre-P2 file (keys absent entirely) decodes the same way.
        var legacyState = try JSON.decode(state)
        #expect(legacyState["discovery"]["events"][event.id]["segmentFile"] == .null)
    }

    @Test func stagedSegmentBatchReplaysIdempotentlyAfterRestart() async throws {
        let directory = try captureDirectory("journal"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), query = DiscoveryQuery(type: "heartRate")
        _ = try await store.discoveryFlow(query)
        let parent = Wire.id()
        let archives = syntheticArchives(3)
        let captured = try await store.captureFactSegment(type: "heartRate", workoutID: parent,
                                                          records: archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0) },
                                                          publish: false)
        let batch = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: captured.events, hasMore: false)
        // Crash before the cursor is published: the journal survives, the anchor does not.
        let reopened = try OutboxStore(directory: directory)
        #expect(await reopened.discoverySnapshot().flows[query.key]?.anchor == nil)
        let generation = await reopened.discoverySnapshot().work[parent]?.generation
        try await reopened.promoteDiscovery(query, batchID: batch)
        #expect(await reopened.discoverySnapshot().flows[query.key]?.anchor == Data([1]))
        // Replaying the same facts on the next page must not duplicate work.
        let replay = try await reopened.stageDiscovery(query, previousAnchor: Data([1]), anchor: Data([2]), events: captured.events, hasMore: false)
        try await reopened.promoteDiscovery(query, batchID: replay)
        #expect(await reopened.discoverySnapshot().events.count == 3)
        #expect(await reopened.discoverySnapshot().work[parent]?.generation == generation)
        #expect(await reopened.discoverySnapshot().flows[query.key]?.anchor == Data([2]))
    }

    @Test func captureRulesKeepGenerationParentBindingAndBatchLimit() async throws {
        let directory = try captureDirectory("rules"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), parent = Wire.id(), other = Wire.id()
        let archives = syntheticArchives(2)
        let records = archives.map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: $0) }
        let first = try await store.captureFactSegment(type: "heartRate", workoutID: parent, records: records)
        let generation = await store.discoverySnapshot().work[parent]?.generation
        // Idempotent replay: same identities, no new generation.
        let again = try await store.captureFactSegment(type: "heartRate", workoutID: parent, records: records)
        #expect(again.published.isEmpty)
        #expect(again.events.map(\.id) == first.events.map(\.id))
        #expect(await store.discoverySnapshot().events.count == 2)
        #expect(await store.discoverySnapshot().work[parent]?.generation == generation)
        // A fact cannot be rebound to another parent.
        await #expect(throws: SyncFailure.self) {
            _ = try await store.captureFactSegment(type: "heartRate", workoutID: other, records: records)
        }
        #expect(await store.discoverySnapshot().work[other] == nil)
        // A mixed-type batch and an oversized batch are refused before any write.
        await #expect(throws: SyncFailure.self) {
            _ = try await store.captureFactSegment(type: "heartRate", workoutID: parent,
                                                   records: records + [CapturedFact(type: "stepCount", uuid: Wire.id(), archive: Data([1]))])
        }
        let tooMany = (0...1000).map { CapturedFact(type: "heartRate", uuid: Wire.id(), archive: Data([UInt8($0 % 251)])) }
        await #expect(throws: SyncFailure.self) {
            _ = try await store.captureFactSegment(type: "heartRate", workoutID: parent, records: tooMany)
        }
        // A deletion cannot carry a segment reference.
        let deletion = DiscoveryEvent(type: "heartRate", uuid: Wire.id(), deleted: true, workoutID: parent,
                                      segmentFile: first.file, segmentOffset: 0, segmentLength: 8)
        await #expect(throws: (any Error).self) { try await store.verifyDiscoveryArchive(deletion) }
    }

    @Test func segmentWriterFlushCountTracksBatchesNotRecords() throws {
        let directory = try captureDirectory("segment-flush"); defer { try? FileManager.default.removeItem(at: directory) }
        let archives = syntheticArchives(250)
        func capture(_ count: Int) throws -> (CaptureSegmentWriter, CaptureSegmentSeal) {
            let writer = try CaptureSegmentWriter(directory: directory, policy: CaptureSegmentPolicy(maxRecords: 100, maxBytes: 1 << 22, maxInterval: 3600), clock: Date.init)
            for archive in archives.prefix(count) { try writer.append(CapturedFact(type: "heartRate", uuid: Wire.id(), archive: archive)) }
            return (writer, try writer.seal())
        }
        let (dense, denseSeal) = try capture(250)
        let (sparse, sparseSeal) = try capture(25)
        #expect(denseSeal.recordCount == 250)
        #expect(sparseSeal.recordCount == 25)
        // 250 records at 100 per batch: two threshold flushes plus the seal flush.
        #expect(dense.flushes == 3)
        #expect(dense.synchronizations == 4)
        #expect(sparse.flushes == 1)
        #expect(sparse.synchronizations == 2)
        // 10x the records must not be 10x the syncs: it tracks the batch count.
        #expect(dense.synchronizations < 10)
        #expect(dense.synchronizations <= sparse.synchronizations * 10)
    }

    @Test func segmentWriterStaysWithinItsByteBound() throws {
        let directory = try captureDirectory("segment-bytes"); defer { try? FileManager.default.removeItem(at: directory) }
        let writer = try CaptureSegmentWriter(directory: directory, policy: CaptureSegmentPolicy(maxRecords: 1000, maxBytes: 4096, maxInterval: 3600), clock: Date.init)
        let archive = Data(repeating: 7, count: 500)
        for _ in 0..<50 {
            try writer.append(CapturedFact(type: "heartRate", uuid: Wire.id(), archive: archive))
            // Bounded memory: the buffered tail never exceeds one batch plus the
            // record that crossed the limit.
            #expect(writer.pendingByteCount <= 4096 + 600)
            #expect(writer.pendingRecordCount <= 1000)
        }
        let seal = try writer.seal()
        #expect(seal.recordCount == 50)
        #expect(writer.flushes > 1)
        writer.discard()
    }
}

/// Route capture batching (F14/F15): the file bytes are unchanged, only the
/// durability granularity is, and the durable prefix is never rewritten.
@Suite struct RouteCaptureBatchTests {
    private func point(_ index: Int) -> RoutePoint { RoutePoint(recordedAtUs: Int64(1000 + index), latitude: 0.5, longitude: -0.25) }

    @Test func flushedBytesAreExactlyTheUnbatchedFormat() throws {
        let directory = try captureDirectory("route-format"); defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("points.ndjson")
        let capture = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 512, maxBytes: 262_144, maxInterval: 3600))
        for index in 0..<7 { try capture.append(point(index)) }
        try capture.finish(expected: 7)
        let lines = try Data(contentsOf: url).split(separator: 10)
        #expect(lines.count == 7)
        for (index, line) in lines.enumerated() {
            // Byte-identical to the per-point writer's output: canonical JSON + LF.
            #expect(Data(line) == (try point(index).canonical(sequence: Int64(index))))
        }
        #expect(capture.pointsWritten == 7)
        #expect(capture.flushes == 1)
        #expect(capture.synchronizations == 2) // one batch sync + one finish sync
    }

    @Test func tenfoldDensityKeepsFlushCountOnTheBatchOrder() throws {
        let directory = try captureDirectory("route-density"); defer { try? FileManager.default.removeItem(at: directory) }
        func capture(_ count: Int) throws -> (RouteCaptureFile, Int) {
            let url = directory.appendingPathComponent("points-\(count).ndjson")
            let capture = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 50, maxBytes: 1 << 22, maxInterval: 3600))
            for index in 0..<count { try capture.append(point(index)) }
            try capture.finish(expected: count)
            return (capture, try Data(contentsOf: url).split(separator: 10).count)
        }
        let (sparse, sparseLines) = try capture(60)
        let (dense, denseLines) = try capture(600)
        #expect(sparseLines == 60)
        #expect(denseLines == 600)
        // 60 points: flush at 50, then the 10 buffered points at finish.
        #expect(sparse.flushes == 2)
        // 600 points: exactly 12 batches of 50, nothing left for finish.
        #expect(dense.flushes == 12)
        #expect(dense.synchronizations == 13)
        // 10x the points is 6x the batches here, and never one sync per point.
        #expect(dense.flushes <= sparse.flushes * 10)
        #expect(dense.synchronizations < denseLines / 10)
        #expect(dense.pointsWritten == 600)
    }

    @Test func interruptedBatchLosesOnlyTheUnflushedTail() throws {
        let directory = try captureDirectory("route-tail"); defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("points.ndjson")
        let capture = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 4, maxBytes: 1 << 22, maxInterval: 3600))
        for index in 0..<6 { try capture.append(point(index)) }
        // The fourth point crossed the batch limit; the last two are still only
        // in memory, which is exactly what a crash would drop.
        #expect(capture.count == 6)
        #expect(capture.flushes == 1)
        #expect(capture.pendingPointCount == 2)
        let durable = try Data(contentsOf: url)
        #expect(durable.split(separator: 10).count == 4)
        // A restart replays HealthKit from the start: the durable prefix matches
        // line by line and the remaining points are appended, never rewritten.
        let reopened = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 4, maxBytes: 1 << 22, maxInterval: 3600))
        for index in 0..<6 { try reopened.append(point(index)) }
        try reopened.finish(expected: 6)
        let complete = try Data(contentsOf: url)
        #expect(complete.split(separator: 10).count == 6)
        #expect(complete.starts(with: durable))
        #expect(reopened.pointsWritten == 2)
    }

    @Test func truncatedTailIsRejectedAndAMismatchedPrefixIsNeverOverwritten() throws {
        let directory = try captureDirectory("route-truncate"); defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("points.ndjson")
        let capture = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 512, maxBytes: 262_144, maxInterval: 3600))
        for index in 0..<4 { try capture.append(point(index)) }
        try capture.flush()
        let sealedPrefix = try Data(contentsOf: url)
        #expect(sealedPrefix.split(separator: 10).count == 4)
        // Death mid-write leaves an unterminated tail line: still a hard error,
        // never a silent truncation.
        try sealedPrefix.dropLast(6).write(to: url)
        let torn = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 512, maxBytes: 262_144, maxInterval: 3600))
        #expect(throws: SyncFailure.self) { for index in 0..<4 { try torn.append(point(index)) } }
        // A different HealthKit version of the same workout must conflict where
        // the stored prefix already has a point, not be spliced onto it (F14).
        try sealedPrefix.write(to: url)
        let conflict = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 512, maxBytes: 262_144, maxInterval: 3600))
        try conflict.append(point(0))
        #expect(throws: SyncFailure.self) { try conflict.append(RoutePoint(recordedAtUs: 9999, latitude: 0, longitude: 0)) }
        #expect(try Data(contentsOf: url) == sealedPrefix)
        // Truncation on a line boundary keeps a usable prefix that replay extends.
        let firstLine = (sealedPrefix.firstIndex(of: 10).map { $0 + 1 }) ?? 0
        try sealedPrefix.prefix(firstLine).write(to: url)
        let partial = try Data(contentsOf: url)
        #expect(partial.split(separator: 10).count == 1)
        let continued = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 512, maxBytes: 262_144, maxInterval: 3600))
        for index in 0..<4 { try continued.append(point(index)) }
        try continued.finish(expected: 4)
        #expect(try Data(contentsOf: url) == sealedPrefix)
        #expect(continued.pointsWritten == 3)
    }

    @Test func abandonedCaptureKeepsAnUnfinishedPrefixAndFinishValidates() throws {
        let directory = try captureDirectory("route-cancel"); defer { try? FileManager.default.removeItem(at: directory) }
        let url = directory.appendingPathComponent("points.ndjson")
        do {
            let capture = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 512, maxBytes: 262_144, maxInterval: 3600))
            for index in 0..<3 { try capture.append(point(index)) }
        } // cancelled: no finish, teardown makes the batch durable
        let cancelled = try Data(contentsOf: url)
        #expect(cancelled.split(separator: 10).count == 3)
        let capture = try RouteCaptureFile(url: url, policy: RouteCaptureBatchPolicy(maxPoints: 512, maxBytes: 262_144, maxInterval: 3600))
        for index in 0..<3 { try capture.append(point(index)) }
        // A short capture is never reported complete, and a rejected count leaves
        // the stored prefix untouched.
        #expect(throws: SyncFailure.self) { try capture.finish(expected: 4) }
        #expect(try Data(contentsOf: url) == cancelled)
        try capture.finish(expected: 3)
        #expect(try Data(contentsOf: url) == cancelled)
        // Every replayed point matched a stored line, so no new bytes were
        // written: finish only re-syncs what is already durable.
        #expect(capture.flushes == 0)
        #expect(capture.pointsWritten == 0)
        #expect(capture.synchronizations == 1)
    }
}
