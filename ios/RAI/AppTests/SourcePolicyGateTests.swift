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
                "limits": .object(["workoutUploads": .int(2), "requests": .object(["upload": .object(["device": .object(["perMinute": .int(100000), "burst": .int(100)])])])]),
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
        let reply = try await response(request, file: file, identity: identity)
        return (reply.data, reply.status, reply.retryAfter)
    }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        GateReplies.requests.withLock { $0.append(request.url!.path) }
        XCTAssertTrue(FileManager.default.fileExists(atPath: file.path))
        XCTAssertTrue(identity.hasPrefix("workout-ingest:" + request.url!.lastPathComponent + ":"))
        return UploadResponse(data: try JSON.object(["workoutID": .string(request.url!.lastPathComponent),
            "ingestStatus": .string("accepted"), "acceptedAt": .string("2026-11-09T10:00:00Z")]).data(),
            status: 200, contentType: "application/json")
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

    func testEligibleSourceReadsAndPostsExactlyOnceThroughFileUploader() async throws {
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
        XCTAssertTrue(tasks.isEmpty, "the file journal must not reuse legacy capture task state")
    }
}

private actor TrackedGateUploader: FileUploader {
    private var active = 0
    private(set) var peak = 0
    private(set) var identities: [String] = []
    private(set) var bodies: [Data] = []
    let delay: Duration
    let reject: String?
    let entered: (@Sendable () -> Void)?
    init(delay: Duration = .milliseconds(80), reject: String? = nil, entered: (@Sendable () -> Void)? = nil) {
        self.delay = delay; self.reject = reject; self.entered = entered
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let reply = try await response(request, file: file, identity: identity)
        return (reply.data, reply.status, reply.retryAfter)
    }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        active += 1; peak = max(peak, active)
        defer { active -= 1 }
        identities.append(identity); bodies.append(try Data(contentsOf: file)); entered?()
        try await Task.sleep(for: delay)
        if request.url!.lastPathComponent == reject {
            return UploadResponse(data: try JSON.object(["code": .string("synthetic_rejection")]).data(), status: 422, contentType: "application/json")
        }
        return UploadResponse(data: try JSON.object(["workoutID": .string(request.url!.lastPathComponent),
            "ingestStatus": .string("accepted"), "acceptedAt": .string("2026-11-09T10:00:00Z")]).data(),
            status: 200, contentType: "application/json")
    }
}

extension SourcePolicyGateTests {
    func testHistoryUsesTwoWorkersDeduplicatesAndContinuesAfterOneFailure() async throws {
        let dir = directory("parallel")
        defer { try? FileManager.default.removeItem(at: dir) }
        let entries = (0..<5).map { n in
            let workout = gateWorkout("com.apple.health", "Watch7,1", at: Date(timeIntervalSince1970: 1_793_059_200 + Double(n) * 3600))
            return GateHealth.Entry(workout: workout, sample: gateSample(workout))
        }
        let ids = entries.map(id)
        let uploader = TrackedGateUploader(reject: ids[1])
        let outcomes = try await coordinator(GateHealth(entries), store: OutboxStore(directory: dir))
            .uploadWorkouts(ids + [ids[0]], secret: secret(), transfers: uploader)
        let peak = await uploader.peak, uploads = await uploader.identities
        XCTAssertEqual(peak, 2)
        XCTAssertEqual(uploads.count, 5)
        XCTAssertEqual(outcomes.map(\.workoutID), ids)
        XCTAssertEqual(outcomes.filter(\.succeeded).count, 4)
        XCTAssertEqual(outcomes[1].errorCode, "synthetic_rejection")
    }

    func testCancelledUploadReopensExactFileWithoutAnotherHealthRead() async throws {
        let dir = directory("resume"), entry = entries().native
        defer { try? FileManager.default.removeItem(at: dir) }
        let ready = expectation(description: "file handed to uploader")
        let slow = TrackedGateUploader(delay: .seconds(30), entered: { ready.fulfill() })
        let store = try OutboxStore(directory: dir)
        let original = coordinator(GateHealth([entry]), store: store)
        let secret = try secret(), workoutID = id(entry)
        let upload = Task { try await original.uploadWorkouts([workoutID], secret: secret, transfers: slow) }
        await fulfillment(of: [ready], timeout: 3)
        upload.cancel()
        do { _ = try await upload.value; XCTFail("cancelled batch succeeded") }
        catch is CancellationError {} catch { XCTFail("wrong cancellation: \(error)") }
        let resumedHealth = GateHealth([]), resumedUploader = TrackedGateUploader()
        let resumed = coordinator(resumedHealth, store: try OutboxStore(directory: dir))
        let outcomes = try await resumed.uploadWorkouts([workoutID], secret: secret, transfers: resumedUploader)
        XCTAssertTrue(try XCTUnwrap(outcomes.first).succeeded)
        let oldIDs = await slow.identities, newIDs = await resumedUploader.identities
        let oldBodies = await slow.bodies, newBodies = await resumedUploader.bodies
        XCTAssertEqual(newIDs, oldIDs); XCTAssertEqual(newBodies, oldBodies)
        let reads = await resumedHealth.childReads
        XCTAssertTrue(reads.isEmpty)
        let recovered = try await resumed.reconcileWorkoutUploads(transfers: resumedUploader)
        XCTAssertEqual(recovered, outcomes)
    }

    func testStatusAndUploadShareHandshakeWithinOneSession() async throws {
        let dir = directory("cache"), entry = entries().native
        defer { try? FileManager.default.removeItem(at: dir) }
        GateReplies.requests.withLock { $0.removeAll() }
        let coordinator = coordinator(GateHealth([entry]), store: try OutboxStore(directory: dir))
        let secret = try secret()
        _ = try await coordinator.workoutStatuses([id(entry)], secret: secret, transfers: GateUploader())
        _ = try await coordinator.uploadWorkouts([id(entry)], secret: secret, transfers: GateUploader())
        _ = try await coordinator.workoutStatuses([id(entry)], secret: secret, transfers: GateUploader())
        XCTAssertEqual(GateReplies.requests.withLock { $0.filter { $0 == "/v2/handshake" }.count }, 1)
    }
}

private final class CachedGateUploader: FileUploader, Sendable {
    private let replies = Mutex<[String: UploadResponse]>([:])
    func set(_ identity: String, _ response: UploadResponse) { replies.withLock { $0[identity] = response } }
    func cachedResponse(_ identity: String) throws -> UploadResponse? { replies.withLock { $0[identity] } }
    func discardCachedResult(_ identity: String) { replies.withLock { $0[identity] = nil } }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        XCTFail("receipt reconciliation must never start a request")
        throw SyncFailure.invalid("unexpected_upload")
    }
}

extension SourcePolicyGateTests {
    func testBackgroundReconciliationValidatesReceiptBeforeRemovingFile() async throws {
        let dir = directory("background-receipt"), workoutID = Wire.id()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        try await store.bind(GateReplies.identity)
        let prepared = try await WorkoutUploadJournal(directory: dir).prepare(Data("synthetic bytes".utf8),
            workoutID: workoutID, scope: GateReplies.identity)
        let uploader = CachedGateUploader()
        func receipt(_ id: String) throws -> UploadResponse {
            UploadResponse(data: try JSON.object(["workoutID": .string(id), "ingestStatus": .string("accepted"),
                "acceptedAt": .string("2026-11-09T10:00:00Z")]).data(), status: 200, contentType: "application/json")
        }
        uploader.set(prepared.identity, try receipt(Wire.id()))
        let coordinator = coordinator(GateHealth([]), store: store)
        let invalid = try await coordinator.reconcileWorkoutUploads(transfers: uploader)
        XCTAssertTrue(invalid.isEmpty)
        XCTAssertTrue(FileManager.default.fileExists(atPath: dir.appendingPathComponent(prepared.file).path))
        uploader.set(prepared.identity, try receipt(workoutID))
        let valid = try await coordinator.reconcileWorkoutUploads(transfers: uploader)
        XCTAssertEqual(valid.map(\.workoutID), [workoutID])
        XCTAssertTrue(try XCTUnwrap(valid.first).succeeded)
        XCTAssertFalse(FileManager.default.fileExists(atPath: dir.appendingPathComponent(prepared.file).path))
        XCTAssertNil(try uploader.cachedResponse(prepared.identity))
        let reopened = self.coordinator(GateHealth([]), store: try OutboxStore(directory: dir))
        let replayed = try await reopened.reconcileWorkoutUploads(transfers: uploader)
        XCTAssertEqual(replayed, valid)
    }
}
