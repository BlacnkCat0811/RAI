import XCTest
import ObjectiveC
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

/// P04/P05 at the App layer: a Workout that already has a durable ACK, or an
/// already prepared immutable bundle, must not re-read HealthKit or post a
/// second identical bundle. These tests count the reads and uploads directly
/// instead of timing them, so they hold on any machine.
private func reuseWorkout(_ bundleID: String = "com.apple.health",
                          _ productType: String? = "Watch7,1",
                          at start: Date) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(60), duration: 60,
                            totalEnergyBurned: nil, totalDistance: nil, metadata: [HKMetadataKeyIndoorWorkout: true])
    workout.setValue(reuseSourceRevision(bundleID, productType), forKey: "sourceRevision")
    return workout
}

private func reuseSample(_ workout: HKWorkout) -> HKQuantitySample {
    let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: .heartRate)!,
                                  quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 120),
                                  start: workout.startDate, end: workout.endDate)
    // A child fact carries the same Apple Watch provenance as its parent, so
    // the sample is eligible and the read is complete rather than partial.
    sample.setValue(reuseSourceRevision(), forKey: "sourceRevision")
    return sample
}

private func reuseSourceRevision(_ bundleID: String = "com.apple.health", _ productType: String? = "Watch7,1") -> HKSourceRevision {
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue(bundleID, forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    return HKSourceRevision(source: source, version: "synthetic", productType: productType,
                            operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
}

/// Counts every detailed read so the tests can assert zero, not merely fast.
private actor ReuseHealth: HealthKitReading {
    struct Entry: Sendable { let workout: HKWorkout; let sample: HKQuantitySample }
    let entries: [Entry]
    private(set) var childReads: [String] = []
    private(set) var bundleSampleReads: [String] = []
    private(set) var seriesReads: [String] = []
    private(set) var routeReads = 0
    init(_ entries: [Entry]) { self.entries = entries }
    func authorize() async throws {}
    func workout(_ id: String) async throws -> HKWorkout? {
        entries.first { $0.workout.uuid.uuidString.lowercased() == id }?.workout
    }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        let samples: [HKSample] = anchor == nil
            ? entries.filter { $0.workout.uuid.uuidString.lowercased() == id }.map { $0.workout } : []
        return HealthPage(samples: samples, deleted: [], anchor: Data([1]), limit: 25)
    }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        childReads.append(type.identifier + "|" + (workout?.uuid.uuidString.lowercased() ?? "library"))
        var samples: [HKSample] = []
        if anchor == nil {
            if type == HKObjectType.workoutType() {
                samples = entries.map { $0.workout }
            } else if let workout {
                samples = entries.filter { $0.workout.uuid == workout.uuid }
                    .flatMap { $0.sample.sampleType.identifier == type.identifier ? [$0.sample] : [] }
            }
        }
        return HealthPage(samples: samples, deleted: [], anchor: Data([2]), limit: 1000)
    }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        bundleSampleReads.append(type.identifier)
        return entries.filter { $0.workout.uuid == workout.uuid }
            .flatMap { $0.sample.sampleType.identifier == type.identifier ? [$0.sample] : [] }
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
    func captureSeries(type: HKQuantityType, workout: HKWorkout, store: OutboxStore) async throws -> [String: [JSON]] {
        seriesReads.append(type.identifier)
        return [:]
    }
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead {
        routeReads += 1
        return HealthRouteRead(routes: [], coverage: [:], enumerationCompleted: true)
    }
}

/// Answers the handshake and analysis lookup; the Workout bundle upload is
/// served by the uploader below, never by this protocol.
private final class ReuseReplies: URLProtocol, @unchecked Sendable {
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
            "capabilities": .object(["workoutBundles": .bool(true), "bundleGzipPart": .bool(true), "bundleProtocolVersion": .int(3),
                "bundleObservationVersion": .int(1), "explicitChildDeletions": .bool(true),
                "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) })])])
            : .object(["code": .string("analysis_not_ready")])
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: handshake ? 200 : 404,
            httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! body.data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// Returns a receipt the client can validate, or a retryable network failure.
private final class ReuseUploader: FileUploader, @unchecked Sendable {
    let uploads = Mutex(0)
    private let fails: Bool
    init(fails: Bool = false) { self.fails = fails }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        uploads.withLock { $0 += 1 }
        if fails { throw UploadError(code: "network_-1004") }
        // identity is "<workoutID>:<bundleSHA256>"; the ACK must echo the hash
        // the client actually sent.
        let hash = identity.split(separator: ":", maxSplits: 1).last.map(String.init) ?? ""
        let workoutID = identity.split(separator: ":", maxSplits: 1).first.map(String.init) ?? ""
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

final class WorkoutSyncReuseTests: XCTestCase {
    private func entry() -> ReuseHealth.Entry {
        let workout = reuseWorkout(at: Date(timeIntervalSince1970: 1_793_059_200))
        return ReuseHealth.Entry(workout: workout, sample: reuseSample(workout))
    }
    private func secret() throws -> ClientConfiguration.Secret {
        try ClientConfiguration.validatedSecret(baseURL: "https://synthetic.invalid",
                                                collectorEpoch: ReuseReplies.identity.collectorEpoch,
                                                token: String(repeating: "s", count: 48))
    }
    private func directory(_ name: String) -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("rai-reuse-" + name + Wire.id())
    }
    private func coordinator(_ store: OutboxStore, _ health: ReuseHealth) -> SyncCoordinator {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ReuseReplies.self]
        return SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))
    }

    /// P04: an unchanged repeat sync stops before HealthKit and before the
    /// network, and the first sync already reused the exhausted discovery
    /// archives instead of repeating the complete sample read.
    func testUnchangedRepeatSyncReadsNothingAndPostsNothing() async throws {
        let dir = directory("unchanged"), only = entry()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let health = ReuseHealth([only])
        let coordinator = coordinator(store, health)
        let id = only.workout.uuid.uuidString.lowercased()

        try await coordinator.discoverChanges()
        let discoveryReads = await health.childReads.count

        let uploader = ReuseUploader()
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader, forceNewBundle: false)
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1)

        // The bundle reused the archived discovery facts: no complete read.
        let bundleReads = await health.bundleSampleReads
        XCTAssertEqual(bundleReads, [], "the bundle repeated a complete sample read")
        let first = try await store.task(id)
        XCTAssertEqual(first?.metrics["reused_discovery_reads"], 10)
        XCTAssertEqual(first?.metrics["full_sample_reads"], 0)
        XCTAssertNotNil(first?.uploadReceipt)

        // A second sync with no new facts must not read HealthKit or post.
        let readsBefore = await health.childReads.count
        let seriesBefore = await health.seriesReads.count
        let routesBefore = await health.routeReads
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader, forceNewBundle: false)

        let readsAfter = await health.childReads.count
        let seriesAfter = await health.seriesReads.count
        let routesAfter = await health.routeReads
        let bundleReadsAfter = await health.bundleSampleReads
        let skipped = try await store.task(id)?.metrics["workout_sync_skipped_unchanged"]
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1, "an unchanged sync posted the bundle again")
        XCTAssertEqual(readsAfter, readsBefore)
        XCTAssertEqual(seriesAfter, seriesBefore)
        XCTAssertEqual(routesAfter, routesBefore)
        XCTAssertEqual(bundleReadsAfter, [])
        XCTAssertEqual(skipped, 1)
        XCTAssertGreaterThan(discoveryReads, 0)
    }

    /// A new discovery generation re-opens the ordinary path, so the skip can
    /// never hide late samples (F18).
    func testNewDiscoveryGenerationReopensTheSync() async throws {
        let dir = directory("generation"), only = entry()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let health = ReuseHealth([only])
        let coordinator = coordinator(store, health)
        let id = only.workout.uuid.uuidString.lowercased()

        try await coordinator.discoverChanges()
        let uploader = ReuseUploader()
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader, forceNewBundle: false)
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1)

        // A late sample arrives through the ordinary discovery path.
        let late = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: .heartRate)!,
                                    quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 121),
                                    start: only.workout.startDate.addingTimeInterval(30),
                                    end: only.workout.startDate.addingTimeInterval(30))
        let type = HKObjectType.quantityType(forIdentifier: .heartRate)!
        let query = DiscoveryQuery(type: type.identifier, workoutID: id)
        let archived = try NSKeyedArchiver.archivedData(withRootObject: late, requiringSecureCoding: true)
        let archiveFile = try await store.savePayload(archived, suffix: "healthkit.archive")
        let event = DiscoveryEvent(type: type.identifier, uuid: late.uuid.uuidString.lowercased(), deleted: false,
                                   archiveFile: archiveFile, archiveHash: Wire.hash(archived), workoutID: id)
        let previous = try await store.discoveryFlow(query).anchor
        let batchID = try await store.stageDiscovery(query, previousAnchor: previous, anchor: Data([9]),
                                                    events: [event], hasMore: false)
        try await store.promoteDiscovery(query, batchID: batchID)

        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader, forceNewBundle: false)
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 2, "a new generation was skipped as unchanged")
    }

    /// P05: an already prepared immutable bundle is retried without any
    /// HealthKit read and without re-encoding.
    func testPreparedBundleRetrySkipsHealthKit() async throws {
        let dir = directory("retry"), only = entry()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let health = ReuseHealth([only])
        let coordinator = coordinator(store, health)
        let id = only.workout.uuid.uuidString.lowercased()

        try await coordinator.discoverChanges()
        let failing = ReuseUploader(fails: true)
        do {
            try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: failing, forceNewBundle: false)
            XCTFail("a retryable upload failure was reported as success")
        } catch {}
        let prepared = try await store.task(id)
        XCTAssertNotNil(prepared?.bundleFile, "the immutable bundle was discarded on a retryable failure")
        XCTAssertNil(prepared?.uploadReceipt)
        let hashBefore = prepared?.bundleSHA256
        let readsBefore = await health.childReads.count
        let seriesBefore = await health.seriesReads.count
        let bundleReadsBefore = await health.bundleSampleReads

        // A retryable failure defers delivery with a persistent backoff, so the
        // retry must wait for the next attempt rather than hot-loop.
        try await Task.sleep(for: .milliseconds(1_200))
        let uploader = ReuseUploader()
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader, forceNewBundle: false)
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1)
        let readsAfter = await health.childReads.count
        let seriesAfter = await health.seriesReads.count
        let bundleReadsAfter = await health.bundleSampleReads
        XCTAssertEqual(readsAfter, readsBefore, "the retry re-read HealthKit")
        XCTAssertEqual(seriesAfter, seriesBefore)
        XCTAssertEqual(bundleReadsAfter, bundleReadsBefore)
        let delivered = try await store.task(id)
        XCTAssertEqual(delivered?.bundleSHA256, hashBefore, "the retry replaced the prepared bundle")
        XCTAssertNotNil(delivered?.uploadReceipt)
    }
}
