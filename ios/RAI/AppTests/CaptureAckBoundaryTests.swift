import XCTest
import ObjectiveC
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

// R1 (R2-F19): the durable ACK may only confirm work the uploaded bundle
// provably carried, or an explicit deletion it carried. Everything else stays
// pending, even when it was captured while the bundle was still being built.
//
// These tests drive the real `performSync` + `uploadAndAcknowledge` path with a
// synthetic `HealthKitReading`, a real `OutboxStore` and a deterministic
// barrier: the fake query suspends inside the capture loop, the test commits a
// concurrent producer mutation, and only then is the loop released. No sleeps,
// no timing guesses.

/// One-way handshake between the capture loop and the test: the loop arrives,
/// the test acts, the test releases.
private actor CaptureBarrier {
    private var arrived: CheckedContinuation<Void, Never>?
    private var released: CheckedContinuation<Void, Never>?
    private var hasArrived = false
    private var isReleased = false

    func arrive() async {
        hasArrived = true
        arrived?.resume(); arrived = nil
        if isReleased { return }
        await withCheckedContinuation { released = $0 }
    }
    func waitForArrival() async {
        if hasArrived { return }
        await withCheckedContinuation { arrived = $0 }
    }
    func release() {
        isReleased = true
        released?.resume(); released = nil
    }
}

/// Synthetic Workout/sample builders. No real HealthKit read and no real health
/// value is involved: every number here is a constant chosen by the test.
private func boundaryWorkout(at start: Date = Date(timeIntervalSince1970: 1_793_059_200)) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(60), duration: 60,
                            totalEnergyBurned: nil, totalDistance: nil, metadata: [HKMetadataKeyIndoorWorkout: true])
    workout.setValue(boundarySourceRevision(), forKey: "sourceRevision")
    return workout
}

private func boundarySample(_ type: HKQuantityTypeIdentifier, _ workout: HKWorkout,
                            offset: TimeInterval) -> HKQuantitySample {
    let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: type)!,
                                  quantity: HKQuantity(unit: HKUnit.count().unitDivided(by: .minute()), doubleValue: 120),
                                  start: workout.startDate.addingTimeInterval(offset),
                                  end: workout.startDate.addingTimeInterval(offset))
    sample.setValue(boundarySourceRevision(), forKey: "sourceRevision")
    return sample
}

private func boundarySourceRevision() -> HKSourceRevision {
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue("com.apple.health", forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    return HKSourceRevision(source: source, version: "synthetic", productType: "Watch7,1",
                            operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
}

/// A synthetic reader whose capture loop can be suspended at one exact type.
private actor BarrierHealth: HealthKitReading {
    let workout: HKWorkout
    private var samples: [String: [HKSample]]
    private let pauseAt: String?
    private let barrier: CaptureBarrier
    private(set) var bundleSampleReads: [String] = []
    private(set) var seriesReads = 0
    private(set) var routeReads = 0

    init(workout: HKWorkout, samples: [String: [HKSample]], pauseAt: String? = nil, barrier: CaptureBarrier) {
        self.workout = workout; self.samples = samples; self.pauseAt = pauseAt; self.barrier = barrier
    }
    func authorize() async throws {}
    func workout(_ id: String) async throws -> HKWorkout? {
        workout.uuid.uuidString.lowercased() == id ? workout : nil
    }
    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        HealthPage(samples: [], deleted: [], anchor: Data([1]), limit: 25)
    }
    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        HealthPage(samples: [], deleted: [], anchor: Data([2]), limit: 1000)
    }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }
    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        bundleSampleReads.append(type.identifier)
        if type.identifier == pauseAt { await barrier.arrive() }
        return samples[type.identifier] ?? []
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
    func captureSeries(type: HKQuantityType, workout: HKWorkout, store: OutboxStore) async throws -> [String: [JSON]] {
        seriesReads += 1
        return [:]
    }
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead {
        routeReads += 1
        return HealthRouteRead(routes: [], coverage: [:], enumerationCompleted: true)
    }
}

private final class BoundaryReplies: URLProtocol, @unchecked Sendable {
    static let identity = Scope(serviceID: "11111111-1111-4111-8111-111111111111",
                                datasetID: "22222222-2222-4222-8222-222222222222",
                                datasetEpoch: "33333333-3333-4333-8333-333333333333",
                                collectorEpoch: "44444444-4444-4444-8444-444444444444")
    static let deletions = Mutex(0)
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        let isHandshake = url.path == "/v2/handshake"
        let isDelete = request.httpMethod == "DELETE"
        let body: JSON
        if isHandshake {
            body = .object(["protocolVersion": .int(3),
                "serviceID": .string(Self.identity.serviceID), "datasetID": .string(Self.identity.datasetID),
                "datasetEpoch": .string(Self.identity.datasetEpoch),
                "capabilities": .object(["workoutBundles": .bool(true), "bundleProtocolVersion": .int(3),
                    "bundleObservationVersion": .int(1), "explicitChildDeletions": .bool(true),
                    "bundleGzipPart": .bool(true),
                    "sampleTypes": .array(HealthKitClient.quantityTypes.map { .string($0.identifier) })])])
        } else if isDelete {
            Self.deletions.withLock { $0 += 1 }
            let id = url.lastPathComponent
            body = .object(["workoutID": .string(id), "ingestStatus": .string("deleted"),
                            "commitRevision": .int(2), "alreadyDeleted": .bool(false)])
        } else {
            body = .object(["code": .string("analysis_not_ready")])
        }
        let status = isHandshake || isDelete ? 200 : 404
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: url, statusCode: status, httpVersion: nil,
            headerFields: ["Content-Type": "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! body.data())
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

private final class BoundaryUploader: FileUploader, @unchecked Sendable {
    let uploads = Mutex(0)
    /// Number of sample upserts the ACK echoes, so a bundle that carried one
    /// sample and a bundle that carried two are distinguishable.
    let samplesInserted: Mutex<Int>
    init(samplesInserted: Int = 1) { self.samplesInserted = Mutex(samplesInserted) }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        uploads.withLock { $0 += 1 }
        let parts = identity.split(separator: ":", maxSplits: 1)
        let workoutID = parts.first.map(String.init) ?? ""
        let hash = parts.last.map(String.init) ?? ""
        let inserted = samplesInserted.withLock { $0 }
        let counts: [String: JSON] = ["workoutInserted": .bool(true),
            "samplesInserted": .int(Int64(inserted)), "samplesUnchanged": .int(0), "samplesDeleted": .int(0),
            "associationsInserted": .int(Int64(inserted)), "associationsUnchanged": .int(0), "associationsDeleted": .int(0),
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

final class CaptureAckBoundaryTests: XCTestCase {
    private let heartRate = HKObjectType.quantityType(forIdentifier: .heartRate)!.identifier
    private let distance = HKObjectType.quantityType(forIdentifier: .distanceWalkingRunning)!.identifier

    private func secret() throws -> ClientConfiguration.Secret {
        try ClientConfiguration.validatedSecret(baseURL: "https://synthetic.invalid",
                                                collectorEpoch: BoundaryReplies.identity.collectorEpoch,
                                                token: String(repeating: "s", count: 48))
    }
    private func directory(_ name: String) -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("rai-ack-" + name + Wire.id())
    }
    private func makeCoordinator(_ store: OutboxStore, _ health: BarrierHealth) -> SyncCoordinator {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [BoundaryReplies.self]
        return SyncCoordinator(store: store, health: health, session: URLSession(configuration: config))
    }

    /// Commits one concurrent producer fact for an already-read type: the
    /// archive bytes are synthetic, exactly as a discovery batch would stage
    /// them while the bundle loop is still running.
    private func captureLateSample(store: OutboxStore, workoutID: String, uuid: UUID) async throws {
        let archive = Data("synthetic-late-sample".utf8)
        _ = try await store.captureFactSegment(type: heartRate, workoutID: workoutID,
            records: [CapturedFact(type: heartRate, uuid: uuid.uuidString.lowercased(), archive: archive,
                                   identityHash: "late-" + uuid.uuidString.lowercased())])
    }

    /// Commits one concurrent producer deletion through the ordinary discovery
    /// journal, so the event carries a real generation and ordinal.
    private func stageDeletion(store: OutboxStore, workoutID: String, type: String, uuid: String) async throws {
        let query = DiscoveryQuery(type: type, workoutID: workoutID)
        let flow = try await store.discoveryFlow(query)
        let event = DiscoveryEvent(type: type, uuid: uuid, deleted: true, workoutID: workoutID)
        let batch = try await store.stageDiscovery(query, previousAnchor: flow.anchor, anchor: Data([7]),
                                                   events: [event], hasMore: true)
        try await store.promoteDiscovery(query, batchID: batch)
    }

    /// One journal entry's status. An upsert and its deletion share type and
    /// uuid but not identity, so the query names the deletion flag too.
    private func status(store: OutboxStore, workoutID: String, type: String, uuid: String,
                        deleted: Bool = false) async -> String? {
        await store.discoverySnapshot().events.values
            .first { $0.workoutID == workoutID && $0.type == type && $0.uuid == uuid && $0.deleted == deleted }?.status
    }

    /// R2-F19 / R1: a sample that arrives for an already-read type while other
    /// types are still being captured must not be confirmed by the ACK of a
    /// bundle that never encoded it.
    func testLateSampleDuringCaptureStaysPendingAfterAck() async throws {
        let dir = directory("late"), workout = boundaryWorkout()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let id = workout.uuid.uuidString.lowercased()
        let first = boundarySample(.heartRate, workout, offset: 0)
        let second = boundarySample(.heartRate, workout, offset: 10)
        let lateSample = boundarySample(.heartRate, workout, offset: 20)
        let late = lateSample.uuid
        let barrier = CaptureBarrier()
        // Pause the stream that owns the late sample: its fact list is fixed
        // before the pause, so the fact committed while it is in flight cannot
        // be in the bundle. Pausing another type would let this stream start
        // after the injection and legitimately carry it (R3 runs types
        // concurrently), which would make the R1 assertion vacuous.
        let health = BarrierHealth(workout: workout,
            samples: [heartRate: [first, second], distance: []], pauseAt: heartRate, barrier: barrier)
        let coordinator = makeCoordinator(store, health)
        let uploader = BoundaryUploader(samplesInserted: 2)
        let secret = try secret()

        let sync = Task { try await coordinator.syncWorkout(id: id, secret: secret, transfers: uploader, forceNewBundle: false) }
        await barrier.waitForArrival()
        // Concurrent producer: a late sample of the type the bundle already read.
        try await captureLateSample(store: store, workoutID: id, uuid: late)
        await barrier.release()
        try await sync.value

        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1)
        let task = try await store.task(id)
        XCTAssertNotNil(task?.uploadReceipt, "the bundle was not acknowledged")
        let snapshotWork = await store.discoverySnapshot().work[id]
        let work = try XCTUnwrap(snapshotWork)
        let lateStatus = await status(store: store, workoutID: id, type: heartRate, uuid: late.uuidString.lowercased())
        XCTAssertNotEqual(lateStatus, "delivered",
                          "ACK confirmed a sample the bundle never carried")
        XCTAssertEqual(lateStatus, "pending")
        XCTAssertGreaterThan(work.generation, work.deliveredGeneration,
                             "the delivered watermark covered an undelivered fact")
        // P0-4: the large capture manifest is scaffolding, not a receipt. Its
        // facts are recorded as metrics before ACK and the manifest is released.
        XCTAssertEqual(task?.metrics["capture_manifest_facts"], 2,
                       "the manifest must list the exact facts the bundle carried")
        XCTAssertNil(task?.captureManifest)

        // The next pass must actually deliver it rather than skip as unchanged.
        let resumed = BarrierHealth(workout: workout,
            samples: [heartRate: [first, second, lateSample], distance: []], barrier: CaptureBarrier())
        let second_ = makeCoordinator(store, resumed)
        let uploader2 = BoundaryUploader(samplesInserted: 3)
        try await second_.syncWorkout(id: id, secret: secret, transfers: uploader2, forceNewBundle: false)
        XCTAssertEqual(uploader2.uploads.withLock { $0 }, 1, "the undelivered fact was skipped as unchanged")
        let resumedSnapshotWork = await store.discoverySnapshot().work[id]
        let resumedWork = try XCTUnwrap(resumedSnapshotWork)
        XCTAssertEqual(resumedWork.generation, resumedWork.deliveredGeneration)
        let lateStatusAfter = await status(store: store, workoutID: id, type: heartRate,
                                           uuid: late.uuidString.lowercased())
        XCTAssertEqual(lateStatusAfter, "delivered")
    }

    /// R2-F19 / R1: a child deletion that arrives mid-capture must not be
    /// swallowed by the ACK of a bundle that listed no deletion for it.
    func testChildDeletionDuringCaptureStaysPendingAfterAck() async throws {
        let dir = directory("childdelete"), workout = boundaryWorkout()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let id = workout.uuid.uuidString.lowercased()
        let first = boundarySample(.heartRate, workout, offset: 0)
        let barrier = CaptureBarrier()
        let health = BarrierHealth(workout: workout,
            samples: [heartRate: [first], distance: []], pauseAt: distance, barrier: barrier)
        let coordinator = makeCoordinator(store, health)
        let uploader = BoundaryUploader(samplesInserted: 1)
        let secret = try secret()

        let sync = Task { try await coordinator.syncWorkout(id: id, secret: secret, transfers: uploader, forceNewBundle: false) }
        await barrier.waitForArrival()
        try await stageDeletion(store: store, workoutID: id, type: heartRate,
                                uuid: first.uuid.uuidString.lowercased())
        await barrier.release()
        try await sync.value

        let task = try await store.task(id)
        XCTAssertNotNil(task?.uploadReceipt)
        let deletionStatus = await status(store: store, workoutID: id, type: heartRate,
                                          uuid: first.uuid.uuidString.lowercased(), deleted: true)
        XCTAssertNotEqual(deletionStatus, "delivered",
                          "ACK confirmed a deletion the bundle never carried")
        XCTAssertEqual(deletionStatus, "pending")
        XCTAssertEqual(task?.metrics["capture_manifest_deletions"], 0,
                       "the manifest must only list deletions the bundle really encoded")
        XCTAssertNil(task?.captureManifest)
        let deletionSnapshotWork = await store.discoverySnapshot().work[id]
        let work = try XCTUnwrap(deletionSnapshotWork)
        XCTAssertGreaterThan(work.generation, work.deliveredGeneration)

        // A second pass carries the explicit child deletion. HealthKit no
        // longer returns the deleted sample, so the read is complete, not
        // partial — a partial read would (correctly) refuse the ACK entirely.
        let resumed = BarrierHealth(workout: workout, samples: [heartRate: [], distance: []],
                                    barrier: CaptureBarrier())
        let uploader2 = BoundaryUploader(samplesInserted: 1)
        try await makeCoordinator(store, resumed).syncWorkout(id: id, secret: secret, transfers: uploader2, forceNewBundle: false)
        XCTAssertEqual(uploader2.uploads.withLock { $0 }, 1, "the undelivered deletion was skipped as unchanged")
        let task2 = try await store.task(id)
        XCTAssertEqual(task2?.submittedSampleDeletions, 1)
        let deletionAfter = await status(store: store, workoutID: id, type: heartRate,
                                         uuid: first.uuid.uuidString.lowercased(), deleted: true)
        XCTAssertEqual(deletionAfter, "delivered",
                       "phase=\(String(describing: task2?.phase)) upload=\(String(describing: task2?.lastUploadError))")
    }

    /// R2-F19 / R1: a Workout deletion that arrives mid-capture must not be
    /// acknowledged as sample work. The bundle is never confirmed and the
    /// deletion outranks it on the next pass.
    func testParentDeletionDuringCaptureRefusesTheAck() async throws {
        let dir = directory("parentdelete"), workout = boundaryWorkout()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        let id = workout.uuid.uuidString.lowercased()
        let first = boundarySample(.heartRate, workout, offset: 0)
        let barrier = CaptureBarrier()
        let health = BarrierHealth(workout: workout,
            samples: [heartRate: [first], distance: []], pauseAt: distance, barrier: barrier)
        let coordinator = makeCoordinator(store, health)
        let uploader = BoundaryUploader(samplesInserted: 1)
        let secret = try secret()

        let sync = Task { try await coordinator.syncWorkout(id: id, secret: secret, transfers: uploader, forceNewBundle: false) }
        await barrier.waitForArrival()
        try await stageDeletion(store: store, workoutID: id, type: HKObjectType.workoutType().identifier, uuid: id)
        await barrier.release()
        do { try await sync.value } catch {}

        let task = try await store.task(id)
        XCTAssertNil(task?.uploadReceipt, "a Workout deletion was acknowledged as sample work")
        let parentSnapshotWork = await store.discoverySnapshot().work[id]
        let work = try XCTUnwrap(parentSnapshotWork)
        XCTAssertTrue(work.pendingDelete, "the parent deletion was lost")
        XCTAssertGreaterThan(work.generation, work.deliveredGeneration)

        // The next pass issues the explicit DELETE instead of re-uploading.
        let before = BoundaryReplies.deletions.withLock { $0 }
        try await coordinator.syncWorkout(id: id, secret: secret, transfers: uploader, forceNewBundle: false)
        let deletedCount = BoundaryReplies.deletions.withLock { $0 }
        XCTAssertEqual(deletedCount, before + 1, "the Workout deletion was not sent")
        let finalTask = try await store.task(id)
        XCTAssertEqual(finalTask?.phase, .deleted)
        let finalSnapshotWork = await store.discoverySnapshot().work[id]
        let finalWork = try XCTUnwrap(finalSnapshotWork)
        XCTAssertFalse(finalWork.pendingDelete)
    }
}
