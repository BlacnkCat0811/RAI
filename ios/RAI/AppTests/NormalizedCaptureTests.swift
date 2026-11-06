import XCTest
import ObjectiveC
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

// R2 acceptance at the App layer. These tests are the only place that can
// observe the real capture and bundle path, so they assert the two things the
// round asks for: no per-sample archiving happens on the supported path, and
// the bytes the server receives are the same facts the archived path produced.

private func ncWorkout(at start: Date) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(600),
                            duration: 600, totalEnergyBurned: nil, totalDistance: nil,
                            metadata: [HKMetadataKeyIndoorWorkout: true])
    workout.setValue(ncSourceRevision(), forKey: "sourceRevision")
    return workout
}

private func ncSourceRevision() -> HKSourceRevision {
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue("com.apple.health", forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    return HKSourceRevision(source: source, version: "synthetic", productType: "Watch7,1",
                            operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
}

private func ncSample(_ identifier: HKQuantityTypeIdentifier, _ value: Double, _ unit: HKUnit,
                      _ workout: HKWorkout, _ offset: TimeInterval) -> HKQuantitySample {
    let start = workout.startDate.addingTimeInterval(offset)
    let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: identifier)!,
                                  quantity: HKQuantity(unit: unit, doubleValue: value),
                                  start: start, end: start.addingTimeInterval(1))
    sample.setValue(ncSourceRevision(), forKey: "sourceRevision")
    return sample
}

private func ncHeartRateUnit() -> HKUnit { HKUnit.count().unitDivided(by: .minute()) }

private func ncNumber(_ json: JSON) -> Double? {
    if case .number(let value) = json { return value }
    if case .int(let value) = json { return Double(value) }
    return nil
}

/// Serves fixed synthetic samples per type and records every complete read, so
/// a test can assert that a stored fact was used instead of HealthKit.
private actor NormalizedHealth: HealthKitReading {
    let workout: HKWorkout
    let samples: [String: [HKQuantitySample]]
    private(set) var bundleSampleReads: [String] = []

    init(workout: HKWorkout, samples: [String: [HKQuantitySample]]) {
        self.workout = workout
        self.samples = samples
    }
    func authorize() async throws {}
    func workout(_ id: String) async throws -> HKWorkout? {
        workout.uuid.uuidString.lowercased() == id ? workout : nil
    }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        let visible: [HKSample] = anchor == nil && workout.uuid.uuidString.lowercased() == id ? [workout] : []
        return HealthPage(samples: visible, deleted: [], anchor: Data([1]), limit: 25)
    }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        var visible: [HKSample] = []
        if anchor == nil {
            if type == HKObjectType.workoutType() {
                visible = [self.workout]
            } else {
                visible = samples[type.identifier] ?? []
            }
        }
        return HealthPage(samples: visible, deleted: [], anchor: Data([2]), limit: 1000)
    }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        bundleSampleReads.append(type.identifier)
        return samples[type.identifier] ?? []
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
    func captureSeries(type: HKQuantityType, workout: HKWorkout, store: OutboxStore) async throws -> [String: [JSON]] { [:] }
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead {
        HealthRouteRead(routes: [], coverage: [:], enumerationCompleted: true)
    }
}

private final class NormalizedReplies: URLProtocol, @unchecked Sendable {
    static let identity = Scope(serviceID: "11111111-1111-4111-8111-111111111111",
                                datasetID: "22222222-2222-4222-8222-222222222222",
                                datasetEpoch: "33333333-3333-4333-8333-333333333333",
                                collectorEpoch: "44444444-4444-4444-8444-444444444444")
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let handshake = request.url!.path == "/v2/handshake"
        let body: JSON = handshake ? .object(["protocolVersion": .int(3),
            "serviceID": .string(Self.identity.serviceID), "datasetID": .string(Self.identity.datasetID),
            "datasetEpoch": .string(Self.identity.datasetEpoch),
            "capabilities": .object(["workoutBundles": .bool(true), "bundleGzipPart": .bool(true),
                "bundleProtocolVersion": .int(3), "bundleObservationVersion": .int(1),
                "explicitChildDeletions": .bool(true),
                "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) })])])
            : .object(["code": .string("analysis_not_ready")])
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: handshake ? 200 : 404,
            httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! body.data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// Captures the plain multipart the client handed to the transfer layer. The
/// multipart is read from the store's immutable bundle file, so the bytes are
/// exactly the ones the archived path used to produce, before gzip.
private final class NormalizedUploader: FileUploader, @unchecked Sendable {
    let uploads = Mutex(0)
    let captured = Mutex<[Data]>([])
    private let directory: URL
    private let store: OutboxStore
    init(store: OutboxStore, directory: URL) {
        self.store = store
        self.directory = directory
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        uploads.withLock { $0 += 1 }
        let workoutID = identity.split(separator: ":", maxSplits: 1).first.map(String.init) ?? ""
        let hash = identity.split(separator: ":", maxSplits: 1).last.map(String.init) ?? ""
        if let name = await store.task(workoutID)?.bundleFile,
           let bytes = try? Data(contentsOf: directory.appendingPathComponent(name)) {
            captured.withLock { $0.append(bytes) }
        }
        let counts: [String: JSON] = ["workoutInserted": .bool(true),
            "samplesInserted": .int(1), "samplesUnchanged": .int(0), "samplesDeleted": .int(0),
            "associationsInserted": .int(1), "associationsUnchanged": .int(0), "associationsDeleted": .int(0),
            "seriesInserted": .int(0), "seriesUnchanged": .int(0),
            "routesInserted": .int(0), "routesUnchanged": .int(0), "routesDeleted": .int(0)]
        let receipt: JSON = .object(["workoutID": .string(workoutID), "bundleSHA256": .string(hash),
            "ingestStatus": .string("committed"), "commitRevision": .int(1),
            "counts": .object(counts), "analysisJobIDs": .array([]), "analysisStatus": .string("not_required")])
        return (try receipt.data(), 200, nil)
    }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let (data, status, delay) = try await upload(request, file: file, identity: identity)
        return UploadResponse(data: data, status: status, retryAfter: delay, contentType: "application/json")
    }
    func discardCachedResult(_ identity: String) {}
    func cachedResultFilename(_ identity: String) -> String? { nil }
}

final class NormalizedCaptureTests: XCTestCase {
    private let start = Date(timeIntervalSince1970: 1_793_059_200)

    private func fixture() -> (HKWorkout, [String: [HKQuantitySample]], [HKQuantitySample]) {
        let workout = ncWorkout(at: start)
        let first = ncSample(.heartRate, 120, ncHeartRateUnit(), workout, 0)
        let second = ncSample(.heartRate, 121, ncHeartRateUnit(), workout, 5)
        let distance = ncSample(.distanceWalkingRunning, 1_000, HKUnit.meter(), workout, 10)
        // Bundle order is the fixed quantity-type order, then read order.
        let ordered = [first, second, distance]
        return (workout, [first.sampleType.identifier: [first, second],
                          distance.sampleType.identifier: [distance]], ordered)
    }

    private func directory(_ name: String) -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("rai-normalized-" + name + Wire.id())
    }
    private func secret() throws -> ClientConfiguration.Secret {
        try ClientConfiguration.validatedSecret(baseURL: "https://synthetic.invalid",
                                                collectorEpoch: NormalizedReplies.identity.collectorEpoch,
                                                token: String(repeating: "s", count: 48))
    }
    private func coordinator(_ store: OutboxStore, _ health: NormalizedHealth) -> SyncCoordinator {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [NormalizedReplies.self]
        return SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))
    }

    /// The multipart body of the single `workout.json` part.
    private func part(_ multipart: Data) -> Data {
        let separator = Data("\r\n\r\n".utf8)
        guard let header = multipart.range(of: separator) else { return Data() }
        let rest = multipart[header.upperBound...]
        let trailer = Data("\r\n--".utf8)
        guard let end = rest.range(of: trailer, options: .backwards) else { return Data(rest) }
        return Data(rest[rest.startIndex..<end.lowerBound])
    }

    private func samples(_ multipart: Data) throws -> [JSON] {
        try JSON.decode(part(multipart))["samples"].list
    }

    /// R2-F01/F02: the supported capture path archives no sample and unarchives
    /// no sample. The counters are real: they wrap every NSKeyedArchiver call
    /// that touches a sample, so a regression here is visible, not silent.
    func testNormalizedCaptureNeverArchivesASample() async throws {
        let (workout, samplesByType, ordered) = fixture()
        let dir = directory("zero"), store = try OutboxStore(directory: dir)
        defer { try? FileManager.default.removeItem(at: dir) }
        let health = NormalizedHealth(workout: workout, samples: samplesByType)
        let coordinator = coordinator(store, health)
        let id = workout.uuid.uuidString.lowercased()

        let meterBefore = SampleArchiveMeter.snapshot()
        let uploader = NormalizedUploader(store: store, directory: dir)
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader,
                                          forceNewBundle: true)
        let meterAfter = SampleArchiveMeter.snapshot()

        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1)
        let saved = await store.task(id)
        let task = try XCTUnwrap(saved)
        XCTAssertNotNil(task.uploadReceipt, "the bundle was not acknowledged")
        XCTAssertEqual(task.metrics["sample_archive_calls"], 0,
                       "a sample was archived on the supported capture path")
        XCTAssertEqual(task.metrics["sample_unarchive_calls"], 0,
                       "a sample was unarchived to build the bundle")
        XCTAssertEqual(meterAfter.archive - meterBefore.archive, 0,
                       "per-sample archiving still happens outside the counted path")
        XCTAssertEqual(meterAfter.unarchive - meterBefore.unarchive, 0,
                       "per-sample unarchiving still happens outside the counted path")
        XCTAssertGreaterThanOrEqual(task.metrics["normalized_segment_writes"] ?? 0, 1)

        let captured = try XCTUnwrap(uploader.captured.withLock { $0.first })
        let sent = try samples(captured)
        XCTAssertEqual(sent.count, ordered.count, "the bundle dropped samples")
    }

    /// R2-F03/F04: the facts the server receives are the facts the archived
    /// path produced — same order, same UUIDs, same microsecond range, same
    /// value and original unit, same supported metadata.
    func testNormalizedFactsMatchTheArchivedSemantics() async throws {
        let (workout, samplesByType, ordered) = fixture()
        let dir = directory("parity"), store = try OutboxStore(directory: dir)
        defer { try? FileManager.default.removeItem(at: dir) }
        let health = NormalizedHealth(workout: workout, samples: samplesByType)
        let coordinator = coordinator(store, health)
        let id = workout.uuid.uuidString.lowercased()

        let uploader = NormalizedUploader(store: store, directory: dir)
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader,
                                          forceNewBundle: true)
        let captured = try XCTUnwrap(uploader.captured.withLock { $0.first })
        let sent = try samples(captured)

        // The archived path appended Normalizer.integrityEvent(sample) to the
        // bundle; the normalized path must produce the same JSON, byte for byte.
        let expected = try ordered.map { try Normalizer.integrityEvent($0) }
        XCTAssertEqual(sent.count, expected.count)
        for (index, event) in expected.enumerated() {
            XCTAssertEqual(try sent[index].data(), try event.data(),
                           "bundle sample \(index) differs from the archived path")
            XCTAssertEqual(sent[index]["uuid"].text, event["uuid"].text)
            XCTAssertEqual(sent[index]["startUs"].integer, event["startUs"].integer)
            XCTAssertEqual(sent[index]["endUs"].integer, event["endUs"].integer)
            XCTAssertEqual(ncNumber(sent[index]["quantity"]["value"]), ncNumber(event["quantity"]["value"]))
            XCTAssertEqual(sent[index]["quantity"]["unit"].text, event["quantity"]["unit"].text)
            XCTAssertEqual(sent[index]["source"]["bundleID"].text, event["source"]["bundleID"].text)
        }
        // The discovery identity is unchanged: the normalized fact hashes the
        // same canonical event/source pair the per-sample builder produced.
        for sample in ordered {
            let fact = try Normalizer.normalizedFact(sample, type: sample.sampleType.identifier, workoutID: nil)
            XCTAssertEqual(fact.identityHash, try Normalizer.discoveryFingerprint(sample),
                           "the normalized identity differs from the discovery fingerprint")
        }
        // Explicit spot checks so the parity claim is not only self-referential.
        XCTAssertEqual(sent.map { $0["uuid"].text },
                       ordered.map { $0.uuid.uuidString.lowercased() })
        XCTAssertEqual(sent[0]["quantity"]["unit"].text, "count/min")
        XCTAssertEqual(ncNumber(sent[0]["quantity"]["value"]), 120)
        XCTAssertEqual(sent[2]["quantity"]["unit"].text, "m")
        XCTAssertEqual(ncNumber(sent[2]["quantity"]["value"]), 1_000)
    }

    /// R2-F05: after discovery captured the facts, the bundle is assembled from
    /// the normalized segments — no complete sample read, no unarchive.
    func testBundleIsAssembledFromNormalizedSegments() async throws {
        let (workout, samplesByType, ordered) = fixture()
        let dir = directory("reuse"), store = try OutboxStore(directory: dir)
        defer { try? FileManager.default.removeItem(at: dir) }
        let health = NormalizedHealth(workout: workout, samples: samplesByType)
        let coordinator = coordinator(store, health)
        let id = workout.uuid.uuidString.lowercased()

        try await coordinator.discoverChanges()
        let meterBefore = SampleArchiveMeter.snapshot()
        let uploader = NormalizedUploader(store: store, directory: dir)
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader,
                                          forceNewBundle: false)
        let meterAfter = SampleArchiveMeter.snapshot()

        let bundleReads = await health.bundleSampleReads
        XCTAssertEqual(bundleReads, [], "the bundle repeated the complete read")
        let saved = await store.task(id)
        let task = try XCTUnwrap(saved)
        XCTAssertEqual(task.metrics["reused_discovery_reads"], 10)
        XCTAssertEqual(task.metrics["full_sample_reads"], 0)
        XCTAssertEqual(task.metrics["sample_archive_calls"], 0)
        XCTAssertEqual(task.metrics["sample_unarchive_calls"], 0)
        XCTAssertEqual(meterAfter.unarchive - meterBefore.unarchive, 0)

        let captured = try XCTUnwrap(uploader.captured.withLock { $0.first })
        let sent = try samples(captured)
        XCTAssertEqual(sent.map { $0["uuid"].text }, ordered.map { $0.uuid.uuidString.lowercased() })
        let expected = try ordered.map { try Normalizer.integrityEvent($0) }
        for (index, event) in expected.enumerated() {
            XCTAssertEqual(try sent[index].data(), try event.data(),
                           "reused sample \(index) differs from the archived path")
        }
    }

    /// R2-F06: a segment written by the old archived encoding still feeds the
    /// bundle, and the unarchive it needs is counted rather than hidden.
    func testLegacyArchiveSegmentStillFeedsTheBundle() async throws {
        let (workout, _, ordered) = fixture()
        let sample = try XCTUnwrap(ordered.first)
        let dir = directory("legacy"), store = try OutboxStore(directory: dir)
        defer { try? FileManager.default.removeItem(at: dir) }
        let id = workout.uuid.uuidString.lowercased()

        // Exactly what the previous release wrote: one archived sample per
        // record, published as a discovery fact.
        let archive = try NSKeyedArchiver.archivedData(withRootObject: sample, requiringSecureCoding: true)
        let fact = CapturedFact(type: sample.sampleType.identifier,
                                uuid: sample.uuid.uuidString.lowercased(),
                                archive: archive,
                                identityHash: try Normalizer.discoveryFingerprint(sample))
        _ = try await store.captureFactSegment(type: sample.sampleType.identifier, workoutID: id,
                                               records: [fact], publish: true)
        // Mark that stream exhausted, as a finished discovery pass would.
        let query = DiscoveryQuery(type: sample.sampleType.identifier, workoutID: id)
        let flow = try await store.discoveryFlow(query)
        let batchID = try await store.stageDiscovery(query, previousAnchor: flow.anchor,
                                                     anchor: Data([7]), events: [], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batchID)

        let health = NormalizedHealth(workout: workout, samples: [:])
        let coordinator = coordinator(store, health)
        let meterBefore = SampleArchiveMeter.snapshot()
        let uploader = NormalizedUploader(store: store, directory: dir)
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader,
                                          forceNewBundle: false)
        let meterAfter = SampleArchiveMeter.snapshot()

        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1, "the legacy segment did not feed the bundle")
        // The archived fact is used for its own type; the other streams have no
        // exhausted discovery pass yet, so they read HealthKit as before.
        let bundleReads = await health.bundleSampleReads
        XCTAssertFalse(bundleReads.contains(sample.sampleType.identifier),
                       "the legacy fact was re-read from HealthKit")
        let saved = await store.task(id)
        let task = try XCTUnwrap(saved)
        XCTAssertEqual(task.metrics["reused_discovery_reads"], 1)
        XCTAssertEqual(task.metrics["sample_unarchive_calls"], 1,
                       "the legacy record must be unarchived, and counted")
        XCTAssertEqual(meterAfter.unarchive - meterBefore.unarchive, 1)
        XCTAssertEqual(task.metrics["sample_archive_calls"], 0)

        let captured = try XCTUnwrap(uploader.captured.withLock { $0.first })
        let sent = try samples(captured)
        XCTAssertEqual(sent.count, 1)
        XCTAssertEqual(try sent[0].data(), try Normalizer.integrityEvent(sample).data(),
                       "the legacy sample changed on the way to the bundle")
    }
}
