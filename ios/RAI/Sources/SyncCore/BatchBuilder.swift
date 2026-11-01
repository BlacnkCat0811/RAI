import Foundation

public enum BatchBuilder {
    public static func page(scope: Scope, stream: Stream, events: [JSON], anchor: Data, store: OutboxStore,
                            workouts: [WorkoutReference] = [], deletedWorkouts: [String] = [], supplements: [OutboxOperation] = [], metrics: SyncMetrics = .init(),
                            pageKey: String? = nil, taskID: String? = nil) async throws -> PendingPage {
        let encodeStart = Date()
        var chunks: [Data] = [], counts: [Int] = []
        var data = Data(), count = 0, details = 0
        for event in events {
            let line = try event.data() + Data([10])
            guard line.count - 1 <= 65_536 else { throw SyncFailure.invalid("event_requires_workout_package") }
            let w = event["workout"]
            let detailCount = w["statistics"].list.count + w["events"].list.count + w["activities"].list.reduce(0) { $0 + 1 + $1["statistics"].list.count + $1["events"].list.count }
            guard detailCount <= 2000 else { throw SyncFailure.invalid("workout_requires_package") }
            // <=1 MiB decoded also bounds deflate worst-case wire below 2 MiB.
            if count > 0 && (count == 1000 || data.count + line.count > 1_048_576 || details + detailCount > 2000) {
                chunks.append(data); counts.append(count); data = Data(); count = 0; details = 0
            }
            data += line; count += 1; details += detailCount
        }
        chunks.append(data); counts.append(count)
        metrics.elapsed("encode_ms", since: encodeStart)
        guard chunks.count <= 10_000 else { throw SyncFailure.invalid("page_too_large") }
        let pageID = Wire.id()
        var manifest = Data(repeating: 0, count: 32), previous = stream.lastBatchID
        var operations: [OutboxOperation] = []
        let register = JSON.object(["datasetID": .string(scope.datasetID), "datasetEpoch": .string(scope.datasetEpoch), "collectorEpoch": .string(scope.collectorEpoch),
                                    "streamID": .string(stream.id), "sampleType": .string(stream.type), "queryFingerprint": .string(stream.fingerprint)])
        let registerFile = try await store.savePayload(register.data())
        operations.append(.request(.init(method: "POST", path: "/v2/sync/streams", file: registerFile, expected: ["streamID": .string(stream.id), "sampleType": .string(stream.type), "collectorEpoch": .string(scope.collectorEpoch), "queryFingerprint": .string(stream.fingerprint)])))
        for i in chunks.indices {
            let compressionStart = Date()
            let wire = try Wire.gzip(chunks[i])
            metrics.elapsed("compression_ms", since: compressionStart)
            metrics.value("uncompressed_bytes", Double(chunks[i].count)); metrics.value("compressed_bytes", Double(wire.count))
            metrics.value("batch_fact_count", Double(counts[i]))
            guard wire.count <= 2_097_152 else { throw SyncFailure.invalid("batch_too_large") }
            let envelope = Envelope(protocolVersion: 2, datasetID: scope.datasetID, datasetEpoch: scope.datasetEpoch, collectorEpoch: scope.collectorEpoch,
                                    streamID: stream.id, batchID: Wire.id(), clientSeq: stream.nextSequence + Int64(i), previousBatchID: previous,
                                    pageID: pageID, fragmentIndex: i, fragmentCount: chunks.count, queryFingerprint: stream.fingerprint,
                                    contentEncoding: "gzip", wireBytes: wire.count, decodedBytes: chunks[i].count, eventCount: counts[i], payloadSHA256: Wire.hash(wire))
            let file = try await store.savePayload(wire, suffix: "gz")
            manifest = Wire.chain(manifest, Wire.digest(try envelope.canonical()))
            operations.append(.batch(envelope, file: file)); previous = envelope.batchID
        }
        operations += supplements
        // The page response nests the original sealed receipt. Pipeline also
        // verifies its manifest before the final local anchor transaction.
        let sealCheck = JSON.object(["pageID": .string(pageID), "streamID": .string(stream.id), "datasetEpoch": .string(scope.datasetEpoch),
            "collectorEpoch": .string(scope.collectorEpoch), "queryFingerprint": .string(stream.fingerprint), "ingestStatus": .string("sealed"),
            "fragmentCount": .int(Int64(chunks.count)), "firstClientSeq": .int(stream.nextSequence), "lastClientSeq": .int(stream.nextSequence + Int64(chunks.count) - 1),
            "lastBatchID": .string(previous!), "manifestSHA256": .string(Wire.hex(manifest))])
        operations.append(.request(.init(method: "GET", path: "/v2/sync/pages/" + pageID, expected: [
            "pageID": .string(pageID), "streamID": .string(stream.id), "datasetEpoch": .string(scope.datasetEpoch),
            "collectorEpoch": .string(scope.collectorEpoch), "queryFingerprint": .string(stream.fingerprint),
            "ingestStatus": .string("sealed"), "fragmentCount": .int(Int64(chunks.count)), "committedFragments": .int(Int64(chunks.count)), "receipt": sealCheck
        ])))
        let deletedFacts = Set(events.filter { $0["op"].text == "delete" }.compactMap { event -> String? in
            guard let type = event["type"].text, let id = event["uuid"].text else { return nil }; return type + ":" + id
        })
        return PendingPage(anchorKey: pageKey ?? stream.type, candidateAnchor: anchor, operations: operations, workouts: workouts, deletedWorkouts: deletedWorkouts, deletedFacts: deletedFacts, taskID: taskID)
    }
}
