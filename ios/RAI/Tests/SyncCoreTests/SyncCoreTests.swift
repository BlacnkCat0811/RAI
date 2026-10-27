import Foundation
import Synchronization
import Testing
@testable import SyncCore

private func temporaryDirectory() throws -> URL {
    let url = FileManager.default.temporaryDirectory.appendingPathComponent("rai-synthetic-test-" + Wire.id())
    try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
    return url
}
private let scope = Scope(serviceID: "11111111-1111-4111-8111-111111111111", datasetID: "22222222-2222-4222-8222-222222222222",
    datasetEpoch: "33333333-3333-4333-8333-333333333333", collectorEpoch: "44444444-4444-4444-8444-444444444444")
private let sampleType = "HKQuantityTypeIdentifierHeartRate"
private func receipt(_ e: Envelope) -> JSON {
    .object(["batchID": .string(e.batchID), "ingestStatus": .string("committed"), "datasetEpoch": .string(e.datasetEpoch), "clientSeq": .int(e.clientSeq),
        "payloadSHA256": .string(e.payloadSHA256), "commitRevision": .int(1), "counts": .object(["inserted": .int(Int64(e.eventCount))]), "analysisJobIDs": .array([])])
}
@Suite(.serialized)
struct SyncCoreTests {
    @Test func canonicalEnvelopeFixture() throws {
        let url = Bundle.module.url(forResource: "envelope.canonical", withExtension: "json", subdirectory: "Fixtures")!
        let original = try Data(contentsOf: url)
        let envelope = try JSONDecoder().decode(Envelope.self, from: original)
        #expect(try envelope.canonical() == original)
        #expect(try JSON.decode(envelope.canonical())["previousBatchID"] == .null)
    }
    @Test func pageManifestFixture() throws {
        let fixture = try JSON.decode(Data(contentsOf: Bundle.module.url(forResource: "page-manifest", withExtension: "json", subdirectory: "Fixtures")!))
        var chain = Data(repeating: 0, count: 32)
        for fragment in fixture["fragments"].list {
            let e = try JSONDecoder().decode(Envelope.self, from: fragment["envelope"].data())
            let bytes = try e.canonical()
            #expect(Wire.hash(bytes) == fragment["envelopeSHA256"].text)
            chain = Wire.chain(chain, Wire.digest(bytes))
            #expect(Wire.hex(chain) == fragment["manifestSHA256"].text)
        }
    }
    @Test func goNumberBoundaries() throws {
        for (v, expected) in [(1.0, "1"), (-0.0, "-0"), (1e-6, "0.000001"), (1e-7, "1e-7"), (1e20, "100000000000000000000"), (1e21, "1e+21"), (31.2304, "31.2304"), (-123.00005, "-123.00005")] {
            #expect(try Wire.goNumber(v) == expected)
        }
        #expect(throws: SyncFailure.self) { try Wire.goNumber(.infinity) }
    }
    @Test func goRouteCanonicalAndHashVectors() throws {
        struct Point: Decodable { let sequence: Int64; let recordedAtUs: Int64; let latitude: Double; let longitude: Double; let altitude: Double? }
        struct Vector: Decodable { let points: [Point]; let canonical: String; let sha256: String; let manifest: String }
        struct Fixture: Decodable { let cases: [Vector] }
        let fixture = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: Bundle.module.url(forResource: "route-vectors", withExtension: "json", subdirectory: "Fixtures")!))
        #expect(fixture.cases.count == 108)
        var chain = Data(repeating: 0, count: 32)
        for vector in fixture.cases {
            let lines = try vector.points.map { try RoutePoint(recordedAtUs: $0.recordedAtUs, latitude: $0.latitude, longitude: $0.longitude, altitude: $0.altitude).canonical(sequence: $0.sequence) }
            let canonical = Data([91]) + lines[0] + Data([93])
            #expect(String(decoding: canonical, as: UTF8.self) == vector.canonical)
            #expect(Wire.hash(canonical) == vector.sha256)
            chain = Wire.chain(chain, Wire.digest(canonical)); #expect(Wire.hex(chain) == vector.manifest)
        }
    }
    @Test func goStringEscapingAndSeriesCanonical() throws {
        #expect(String(decoding: try JSON.string("m/s<&>").data(), as: UTF8.self) == "\"m/s\\u003c\\u0026\\u003e\"")
        let stat: JSON = .object(["type": .string("HKQuantityTypeIdentifierRunningSpeed"), "startUs": .int(1), "endUs": .int(2),
            "average": .object(["value": .number(3.5), "unit": .string("m/s")])])
        #expect(String(decoding: try Supplements.canonicalDetail("statistic", stat), as: UTF8.self) == "{\"type\":\"HKQuantityTypeIdentifierRunningSpeed\",\"startUs\":1,\"endUs\":2,\"average\":{\"value\":3.5,\"unit\":\"m/s\"}}")
    }
    @Test func gzipStableAndEmpty() throws {
        let bytes = Data("synthetic\n".utf8)
        #expect(try Wire.gzip(bytes) == Wire.gzip(bytes))
        #expect(try Wire.gzip(Data()).prefix(2) == Data([0x1f, 0x8b]))
    }
    @Test func candidatePersistsWithoutAdvancingDelivered() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory); try await store.bind(scope)
        let stream = try await store.stream(type: sampleType, query: "all")
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [], anchor: Data([5]), store: store)
        try await store.stage(page); try await store.begin(page.id)
        let restored = try OutboxStore(directory: directory), state = await restored.snapshot()
        #expect(state.streams[sampleType]?.deliveredAnchor == nil)
        #expect(state.streams[sampleType]?.nextSequence == 1)
        #expect(state.pages[0].candidateAnchor == Data([5])); #expect(!state.pages[0].uploading)
        for (a, b) in zip(page.operations, state.pages[0].operations) {
            if case .batch(let e, let name) = a, case .batch(let restored, let restoredName) = b {
                #expect(e == restored); #expect(name == restoredName)
                #expect(try Wire.hash(Data(contentsOf: directory.appendingPathComponent(name))) == e.payloadSHA256)
            }
        }
    }
    @Test func receiptCannotAdvanceWrongBatchOrHash() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let stream = try await store.stream(type: sampleType, query: "all")
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [], anchor: Data([9]), store: store)
        try await store.stage(page)
        if case .request(let request) = page.operations[0] { try await store.acknowledge(page.id, cursor: 0, receipt: .object(request.expected)) }
        do { try await store.acknowledge(page.id, cursor: 1, receipt: .object(["ingestStatus": .string("committed")])); Issue.record("bad receipt accepted") }
        catch { }
        let state = await store.snapshot(); #expect(state.pages[0].cursor == 1); #expect(state.streams[sampleType]?.nextSequence == 1)
        #expect(state.streams[sampleType]?.deliveredAnchor == nil)
    }
    @Test func batchACKAdvancesSequenceButWaitsForPageSeal() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let stream = try await store.stream(type: sampleType, query: "all")
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [], anchor: Data([7]), store: store)
        try await store.stage(page)
        for (i, operation) in page.operations.enumerated() {
            switch operation {
            case .request(let r): try await store.acknowledge(page.id, cursor: i, receipt: .object(r.expected))
            case .batch(let e, _):
                try await store.acknowledge(page.id, cursor: i, receipt: receipt(e))
                let restored = try OutboxStore(directory: directory), state = await restored.snapshot()
                #expect(state.streams[sampleType]?.nextSequence == 2)
                #expect(state.streams[sampleType]?.lastBatchID == e.batchID)
                #expect(state.streams[sampleType]?.deliveredAnchor == nil)
            }
        }
        let final = await store.snapshot()
        #expect(final.streams[sampleType]?.deliveredAnchor == Data([7])); #expect(final.pages.isEmpty)
    }
    @Test func missingPayloadAndSecondPendingPageRefused() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let broken = PendingPage(anchorKey: "x", candidateAnchor: Data([1]), operations: [.request(.init(method: "POST", path: "/v2/test", file: "missing.json"))])
        await #expect(throws: SyncFailure.self) { try await store.stage(broken) }
        let valid = PendingPage(anchorKey: "x", candidateAnchor: Data([1]), operations: [.request(.init(method: "GET", path: "/v2/test"))])
        try await store.stage(valid)
        await #expect(throws: SyncFailure.self) { try await store.stage(valid) }
        #expect(await store.snapshot().localAnchors["x"] == nil)
    }
    @Test func failedAtomicWriteCannotAdvanceMemoryOrDisk() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let stream = try await store.stream(type: sampleType, query: "all")
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [], anchor: Data([6]), store: store)
        try await store.stage(page)
        let file = directory.appendingPathComponent("state.json"), saved = try Data(contentsOf: file)
        try FileManager.default.removeItem(at: file); try FileManager.default.createDirectory(at: file, withIntermediateDirectories: false)
        if case .request(let request) = page.operations[0] {
            await #expect(throws: (any Error).self) { try await store.acknowledge(page.id, cursor: 0, receipt: .object(request.expected)) }
        }
        #expect(await store.snapshot().pages[0].cursor == 0)
        try FileManager.default.removeItem(at: file); try saved.write(to: file, options: .atomic)
        let reopened = try OutboxStore(directory: directory)
        #expect(await reopened.snapshot().pages[0].cursor == 0); #expect(await reopened.snapshot().streams[sampleType]?.deliveredAnchor == nil)
    }
    @Test func multiFragmentPageCannotPromoteAnchorAfterFirstACK() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), stream = try await store.stream(type: sampleType, query: "all")
        let events: [JSON] = (0..<1001).map { .object(["op": .string("delete"), "kind": .string("sample"), "type": .string(sampleType), "uuid": .string(String(format: "00000000-0000-4000-8000-%012d", $0 + 1))]) }
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: events, anchor: Data([8]), store: store)
        try await store.stage(page)
        var sizes: [Int] = []
        for (index, operation) in page.operations.enumerated() {
            switch operation {
            case .request(let request): try await store.acknowledge(page.id, cursor: index, receipt: .object(request.expected))
            case .batch(let envelope, _):
                sizes.append(envelope.eventCount); try await store.acknowledge(page.id, cursor: index, receipt: receipt(envelope))
                #expect(await store.snapshot().streams[sampleType]?.deliveredAnchor == nil)
            }
        }
        #expect(sizes == [1000, 1]); #expect(await store.snapshot().streams[sampleType]?.nextSequence == 3)
        #expect(await store.snapshot().streams[sampleType]?.deliveredAnchor == Data([8]))
    }
    @Test func sourceDeletionCancelsStaleAssociationWithoutForgingReceipt() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), stream = try await store.stream(type: sampleType, query: "all")
        let id = Wire.id(), key = "association:synthetic-workout:" + sampleType
        let obsolete = PendingPage(anchorKey: key, candidateAnchor: Data([42]), operations: [.request(.init(method: "POST", path: "/v2/sync/associations", expected: ["sampleType": .string(sampleType), "sampleUUID": .string(id)]))])
        try await store.stage(obsolete)
        let deletion: JSON = .object(["op": .string("delete"), "kind": .string("sample"), "type": .string(sampleType), "uuid": .string(id)])
        let page = try await BatchBuilder.page(scope: scope, stream: stream, events: [deletion], anchor: Data([8]), store: store)
        try await store.stage(page)
        for (index, operation) in page.operations.enumerated() {
            switch operation {
            case .request(let request): try await store.acknowledge(page.id, cursor: index, receipt: .object(request.expected))
            case .batch(let envelope, _): try await store.acknowledge(page.id, cursor: index, receipt: receipt(envelope))
            }
        }
        let restored = try OutboxStore(directory: directory), state = await restored.snapshot()
        #expect(state.tombstones.contains(sampleType + ":" + id)); #expect(state.pages.isEmpty)
        #expect(state.localAnchors[key] == nil); #expect(state.cancellations[obsolete.id] != nil)
        #expect(state.receipts[obsolete.id + ":0"] == nil)
    }
    @Test func scopeSwitchAndCorruptStateRefused() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory); try await store.bind(scope)
        await #expect(throws: SyncFailure.self) { try await store.bind(.init(serviceID: Wire.id(), datasetID: scope.datasetID, datasetEpoch: scope.datasetEpoch, collectorEpoch: scope.collectorEpoch)) }
        try Data("broken".utf8).write(to: directory.appendingPathComponent("state.json"))
        #expect(throws: (any Error).self) { try OutboxStore(directory: directory) }
    }
    @Test func retryClassificationAndCap() {
        for status in [0, 408, 429, 500, 503] { #expect(UploadError(code: "test", status: status).retryable) }
        for status in [400, 401, 403, 404, 409, 413, 415, 422] { #expect(!UploadError(code: "test", status: status).retryable) }
        #expect((1...8).map(UploadError.backoff) == [1, 2, 4, 8, 16, 32, 60, 60])
    }
    @Test func routeSpoolBoundsHashesAndRestartFiles() throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let spool = try RouteSpool(directory: directory, chunkSize: 1000)
        for index in 0..<5001 { try spool.append([.init(recordedAtUs: Int64(index), latitude: 31.2, longitude: 121.4)]) }
        let route = try spool.finish()
        #expect(route.chunks.map(\.pointCount) == [1000, 1000, 1000, 1000, 1000, 1]); #expect(route.pointCount == 5001)
        var wireChain = Data(repeating: 0, count: 32)
        for chunk in route.chunks {
            let bytes = try Data(contentsOf: directory.appendingPathComponent(chunk.file))
            #expect(Wire.hash(bytes) == chunk.payloadHash)
            wireChain = Wire.chain(wireChain, Wire.digest(bytes)); #expect(Wire.hex(wireChain) == chunk.payloadManifest)
        }
        #expect(throws: SyncFailure.self) { try spool.append([]) }
    }
    @Test func routeRejectsInvalidMeasurements() throws {
        #expect(throws: SyncFailure.self) { try RoutePoint(recordedAtUs: 1, latitude: 91, longitude: 0).canonical(sequence: 0) }
        #expect(throws: SyncFailure.self) { try RoutePoint(recordedAtUs: 1, latitude: 0, longitude: 0, speed: -1).canonical(sequence: 0) }
        let line = try JSON.decode(RoutePoint(recordedAtUs: 1, latitude: 0, longitude: 0).canonical(sequence: 0))
        #expect(line["speed"] == .null); #expect(line["altitude"] == .null)
    }
    @Test func routeManifestSealAndDeletePayloads() async throws {
        let directory = try temporaryDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), spool = try RouteSpool(directory: directory)
        try spool.append([.init(recordedAtUs: 10, latitude: 0, longitude: 0), .init(recordedAtUs: 11, latitude: 0, longitude: 0)])
        let route = try spool.finish(), id = Wire.id(), workout = WorkoutReference(id: Wire.id(), source: "synthetic")
        let operations = try await route.operations(scope: scope, routeID: id, workout: workout, source: "synthetic", store: store)
        #expect(operations.count == 3)
        if case .request(let registration) = operations[0] {
            let manifest = try JSON.decode(Data(contentsOf: directory.appendingPathComponent(registration.file!)))
            #expect(manifest["expectedPointCount"].integer == 2); #expect(manifest["workoutID"].text == workout.id)
        }
        if case .request(let seal) = operations[2] { #expect(seal.expected["ingestStatus"] == .string("sealed")); #expect(seal.expected["manifestSHA256"] == .string(route.chunks[0].manifest)) }
    }
    @Test func deleteWireHasOnlyFrozenFourFields() throws {
        let deletion: JSON = .object(["op": .string("delete"), "kind": .string("sample"), "type": .string(sampleType), "uuid": .string(Wire.id())])
        #expect(try JSON.decode(deletion.data()).fields.count == 4)
    }
}
