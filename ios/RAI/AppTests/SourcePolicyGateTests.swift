import XCTest
import ObjectiveC
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

/// The frozen source policy is applied to the root record before any detailed
/// child read. These tests pin that ordering at the App layer: an excluded or
/// undecidable source must cost zero quantity/series/route queries and zero
/// uploads, while the visible inventory and its deletion evidence are kept.
private func gateSample(_ workout: HKWorkout) -> HKQuantitySample {
    HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: .heartRate)!,
                     quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 120),
                     start: workout.startDate, end: workout.endDate)
}

private func gateRevision(_ bundleID: String, _ productType: String?) -> HKSourceRevision {
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue(bundleID, forKey: "bundleIdentifier")
    // A device nickname is not evidence: only the descriptor is classified.
    source.setValue("Apple Watch", forKey: "name")
    return HKSourceRevision(source: source, version: "synthetic", productType: productType,
                            operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
}

private func gateWorkout(_ bundleID: String, _ productType: String?, at start: Date) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(60), duration: 60,
                            totalEnergyBurned: nil, totalDistance: nil, metadata: [HKMetadataKeyIndoorWorkout: true])
    workout.setValue(gateRevision(bundleID, productType), forKey: "sourceRevision")
    return workout
}

private actor GateHealth: HealthKitReading {
    struct Entry: Sendable { let workout: HKWorkout; let sample: HKQuantitySample }
    let entries: [Entry]
    private(set) var childReads: [String] = []
    private(set) var rootReads = 0
    init(_ entries: [Entry]) { self.entries = entries }
    func authorize() async throws {}
    func workout(_ id: String) async throws -> HKWorkout? {
        entries.first { $0.workout.uuid.uuidString.lowercased() == id }?.workout
    }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        rootReads += 1
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
        entries.filter { $0.workout.uuid == workout.uuid }
            .flatMap { $0.sample.sampleType.identifier == type.identifier ? [$0.sample] : [] }
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead {
        HealthRouteRead(routes: [], coverage: [:], enumerationCompleted: true)
    }
}

private final class GateReplies: URLProtocol, @unchecked Sendable {
    static let identity = Scope(serviceID: "11111111-1111-4111-8111-111111111111",
                                datasetID: "22222222-2222-4222-8222-222222222222",
                                datasetEpoch: "33333333-3333-4333-8333-333333333333",
                                collectorEpoch: "44444444-4444-4444-8444-444444444444")
    static let requests = Mutex<[String]>([])
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let path = request.url!.path
        Self.requests.withLock { $0.append(path) }
        let handshake = path == "/v2/handshake"
        let body: JSON = handshake ? .object(["protocolVersion": .int(3),
            "serviceID": .string(Self.identity.serviceID), "datasetID": .string(Self.identity.datasetID),
            "datasetEpoch": .string(Self.identity.datasetEpoch),
            "capabilities": .object(["workoutBundles": .bool(true), "bundleGzipPart": .bool(true), "bundleProtocolVersion": .int(3),
                "bundleObservationVersion": .int(1), "explicitChildDeletions": .bool(true),
                "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) })])])
            : .object(["code": .string("workout_import_not_found")])
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: handshake ? 200 : 404,
            httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! body.data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class GateUploader: FileUploader, @unchecked Sendable {
    let uploads = Mutex(0)
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        uploads.withLock { $0 += 1 }; return (Data(), 200, nil)
    }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        uploads.withLock { $0 += 1 }; return UploadResponse(data: Data(), status: 200, contentType: "application/json")
    }
    func discardCachedResult(_ identity: String) {}
    func cachedResultFilename(_ identity: String) -> String? { nil }
}

final class SourcePolicyGateTests: XCTestCase {
    private func entries() -> (native: GateHealth.Entry, thirdParty: GateHealth.Entry, undecided: GateHealth.Entry) {
        let start = Date(timeIntervalSince1970: 1_793_059_200)
        func entry(_ bundle: String, _ product: String?, _ offset: TimeInterval) -> GateHealth.Entry {
            let workout = gateWorkout(bundle, product, at: start.addingTimeInterval(offset))
            return GateHealth.Entry(workout: workout, sample: gateSample(workout))
        }
        return (entry("com.apple.health", "Watch7,1", 0),
                entry("com.example.thirdparty", nil, -3600),
                entry("com.apple.health", "iPhone14,2", -7200))
    }
    private func id(_ entry: GateHealth.Entry) -> String { entry.workout.uuid.uuidString.lowercased() }
    private func secret() throws -> ClientConfiguration.Secret {
        try ClientConfiguration.validatedSecret(baseURL: "https://synthetic.invalid",
                                                collectorEpoch: GateReplies.identity.collectorEpoch,
                                                token: String(repeating: "s", count: 48))
    }
    private func directory(_ name: String) -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("rai-source-gate-" + name + Wire.id())
    }

    func testExcludedAndUndecidedSourcesSkipEveryDetailedRead() async throws {
        let dir = directory("discovery"), all = entries()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let health = GateHealth([all.native, all.thirdParty, all.undecided])
        try await SyncCoordinator(store: store, health: health).discoverChanges()

        // The library-wide root stream is unscoped; every scoped child stream
        // belongs to the one eligible Workout.
        let reads = await health.childReads.filter { !$0.hasSuffix("|library") }
        XCTAssertFalse(reads.isEmpty)
        XCTAssertTrue(reads.allSatisfy { $0.hasSuffix("|" + id(all.native)) }, "\(reads)")
        XCTAssertTrue(reads.contains(HKQuantityTypeIdentifier.heartRate.rawValue + "|" + id(all.native)))

        // Each decision is persisted with its policy identity, never an identity leak.
        let native = await store.sourceClassification(workoutID: id(all.native))
        let third = await store.sourceClassification(workoutID: id(all.thirdParty))
        let undecided = await store.sourceClassification(workoutID: id(all.undecided))
        XCTAssertEqual(native?["classification"], .string("apple_watch"))
        XCTAssertEqual(native?["outcome"], .string(WorkoutSourcePolicy.eligible))
        XCTAssertEqual(third?["classification"], .string("third_party"))
        XCTAssertEqual(third?["outcome"], .string(WorkoutSourcePolicy.excluded))
        XCTAssertEqual(undecided?["classification"], .string("source_unknown"))
        XCTAssertEqual(undecided?["outcome"], .string(WorkoutSourcePolicy.unclassified))
        for record in [native, third, undecided] {
            XCTAssertEqual(record?["sourcePolicyVersion"], .string(WorkoutSourcePolicy.version))
            XCTAssertNil(record?.fields["workoutID"])
            let text = record.flatMap { try? $0.data() }.map { String(decoding: $0, as: UTF8.self) } ?? ""
            XCTAssertFalse(text.contains(id(all.thirdParty)))
        }

        // Visibility is not deletion: every root record stays in the inventory.
        let snapshot = await store.discoverySnapshot()
        for entry in [all.native, all.thirdParty, all.undecided] {
            XCTAssertNotNil(snapshot.workoutArchives[id(entry)])
        }
    }

    func testExcludedWorkoutNeverStartsAnUpload() async throws {
        let dir = directory("excluded"), all = entries()
        defer { try? FileManager.default.removeItem(at: dir) }
        GateReplies.requests.withLock { $0.removeAll() }
        let store = try OutboxStore(directory: dir)
        let health = GateHealth([all.native, all.thirdParty])
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [GateReplies.self]
        let uploader = GateUploader()
        let coordinator = SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))

        do {
            try await coordinator.syncWorkout(id: id(all.thirdParty), secret: try secret(),
                                              transfers: uploader, forceNewBundle: true)
            XCTFail("excluded source started a sync")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("source_policy_excluded"))
        }
        let excludedReads = await health.childReads.filter { $0.hasSuffix("|" + id(all.thirdParty)) }
        let excludedOutcome = await store.sourceClassification(workoutID: id(all.thirdParty))?["outcome"]
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 0)
        XCTAssertEqual(excludedReads, [])
        XCTAssertFalse(GateReplies.requests.withLock { $0 }.contains { $0.contains("workout-bundles") })
        XCTAssertEqual(excludedOutcome, .string(WorkoutSourcePolicy.excluded))
    }

    func testUndecidedSourceStaysPendingInsteadOfGuessing() async throws {
        let dir = directory("pending"), all = entries()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let health = GateHealth([all.native, all.undecided])
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [GateReplies.self]
        let uploader = GateUploader()
        let coordinator = SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))

        do {
            try await coordinator.syncWorkout(id: id(all.undecided), secret: try secret(),
                                              transfers: uploader, forceNewBundle: true)
            XCTFail("undecidable source started a sync")
        } catch let failure as SyncFailure {
            XCTAssertEqual(failure, .invalid("source_policy_pending"))
        }
        let undecidedReads = await health.childReads.filter { $0.hasSuffix("|" + id(all.undecided)) }
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 0)
        XCTAssertEqual(undecidedReads, [])
    }

    /// A Workout that already has local work keeps collecting after its source
    /// descriptor changes. Skipping it would strand an explicit HealthKit
    /// deletion of facts that are already committed, so the gate only skips
    /// Workouts that have no local task yet.
    func testExistingWorkKeepsCollectingAfterItsSourceChanges() async throws {
        let dir = directory("source-change"), all = entries()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let health = GateHealth([all.native])
        let coordinator = SyncCoordinator(store: store, health: health)

        try await coordinator.discoverChanges(workoutIDs: [id(all.native)])
        let before = await health.childReads.filter { $0.hasSuffix("|" + id(all.native)) }
        XCTAssertFalse(before.isEmpty)

        // Local work for this Workout exists (an earlier sync committed facts).
        try await store.beginTask(id(all.native))

        // The descriptor now reports a third-party writer.
        all.native.workout.setValue(gateRevision("com.example.thirdparty", nil), forKey: "sourceRevision")
        try await coordinator.discoverChanges(workoutIDs: [id(all.native)])

        let after = await health.childReads.filter { $0.hasSuffix("|" + id(all.native)) }
        XCTAssertGreaterThan(after.count, before.count,
                             "a Workout with local work must keep collecting so deletions are not lost")
        let outcome = await store.sourceClassification(workoutID: id(all.native))?["outcome"]
        XCTAssertEqual(outcome, .string(WorkoutSourcePolicy.excluded))
    }
}
