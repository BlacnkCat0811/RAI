import XCTest
import ObjectiveC
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

/// The frozen source policy is applied to the root record before any detailed
/// child read. These tests pin that ordering in the foreground upload path: an
/// excluded or undecidable source must cost zero quantity/series/route queries
/// and zero POSTs, while an eligible Workout reads and posts exactly once.
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
    init(_ entries: [Entry]) { self.entries = entries }
    func authorize() async throws {}
    func workout(_ id: String) async throws -> HKWorkout? {
        entries.first { $0.workout.uuid.uuidString.lowercased() == id }?.workout
    }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        HealthPage(samples: [], deleted: [], anchor: Data([2]), limit: 1000)
    }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        let id = workout.uuid.uuidString.lowercased()
        childReads.append(type.identifier + "|" + id)
        return entries.filter { $0.workout.uuid == workout.uuid }
            .flatMap { $0.sample.sampleType.identifier == type.identifier ? [$0.sample] : [] }
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
    func bundleSeriesGroups(type: HKQuantityType, workout: HKWorkout) async throws -> [HealthSeriesGroup] {
        childReads.append(type.identifier + "|" + workout.uuid.uuidString.lowercased())
        return []
    }
    func bundleRoutes(workout: HKWorkout) async throws -> [HealthRouteBundle] {
        childReads.append("routes|" + workout.uuid.uuidString.lowercased())
        return []
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
        let status: Int
        let body: JSON
        if path == "/v2/handshake" {
            status = 200
            body = .object(["protocolVersion": .int(3),
                "serviceID": .string(Self.identity.serviceID), "datasetID": .string(Self.identity.datasetID),
                "datasetEpoch": .string(Self.identity.datasetEpoch), "datasetStatus": .string("active"),
                "capabilities": .object(["workoutPayloadVersion": .int(6),
                    "workoutUUIDIngestVersion": .int(1), "workoutUUIDIngestAccepting": .bool(true),
                    "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) })])])
        } else if path.hasPrefix("/v2/sync/workout-ingests/") {
            status = 200
            body = .object(["workoutID": .string(request.url!.lastPathComponent),
                            "ingestStatus": .string("accepted"),
                            "acceptedAt": .string("2026-11-09T10:00:00Z")])
        } else {
            status = 404
            body = .object(["code": .string("workout_ingest_not_found")])
        }
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: status,
            httpVersion: nil, headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! body.data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private struct GateUploader: FileUploader {
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        (Data(), 200, nil)
    }
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
    private func coordinator(_ health: GateHealth, store: OutboxStore) -> SyncCoordinator {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [GateReplies.self]
        return SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))
    }

    func testExcludedAndUndecidedSourcesSkipEveryDetailedReadAndPost() async throws {
        let dir = directory("gate"), all = entries()
        defer { try? FileManager.default.removeItem(at: dir) }
        GateReplies.requests.withLock { $0.removeAll() }
        let store = try OutboxStore(directory: dir)
        let health = GateHealth([all.native, all.thirdParty, all.undecided])
        let outcomes = try await coordinator(health, store: store)
            .uploadWorkouts([id(all.thirdParty), id(all.undecided)], secret: try secret(),
                            transfers: GateUploader())

        XCTAssertEqual(outcomes.map(\.errorCode), ["source_policy_excluded", "source_policy_pending"])
        XCTAssertTrue(outcomes.allSatisfy { !$0.succeeded && $0.acceptedAt == nil })
        let detailed = await health.childReads.filter {
            $0.hasSuffix("|" + id(all.thirdParty)) || $0.hasSuffix("|" + id(all.undecided))
        }
        XCTAssertEqual(detailed, [])
        let paths = GateReplies.requests.withLock { $0 }
        XCTAssertEqual(paths.filter { $0 == "/v2/handshake" }.count, 1)
        XCTAssertTrue(paths.filter { $0.hasPrefix("/v2/sync/workout-ingests/") }.isEmpty)
        let tasks = await store.snapshot().tasks ?? [:]
        XCTAssertTrue(tasks.isEmpty, "a source-policy rejection must not create a Workout task")
    }

    func testEligibleSourceReadsAndPostsExactlyOnceWithoutStoredTaskState() async throws {
        let dir = directory("eligible"), all = entries()
        defer { try? FileManager.default.removeItem(at: dir) }
        GateReplies.requests.withLock { $0.removeAll() }
        let store = try OutboxStore(directory: dir)
        let health = GateHealth([all.native])
        let outcomes = try await coordinator(health, store: store)
            .uploadWorkouts([id(all.native)], secret: try secret(), transfers: GateUploader())

        let outcome = try XCTUnwrap(outcomes.first)
        XCTAssertTrue(outcome.succeeded)
        XCTAssertNotNil(outcome.acceptedAt)
        let paths = GateReplies.requests.withLock { $0 }
        XCTAssertEqual(paths.filter { $0 == "/v2/handshake" }.count, 1)
        XCTAssertEqual(paths.filter { $0.hasPrefix("/v2/sync/workout-ingests/") },
                       ["/v2/sync/workout-ingests/" + id(all.native)])
        let reads = await health.childReads
        XCTAssertFalse(reads.isEmpty)
        XCTAssertTrue(reads.allSatisfy { $0.hasSuffix("|" + id(all.native)) })
        let tasks = await store.snapshot().tasks ?? [:]
        XCTAssertTrue(tasks.isEmpty, "the foreground flow must not persist a Workout upload task")
    }
}
