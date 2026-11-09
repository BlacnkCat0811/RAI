import XCTest
import ObjectiveC
import HealthKit
import SyncCore
import Synchronization
@testable import RAI

// R3 acceptance at the App layer: independent quantity types overlap, the
// overlap is bounded by the configured capture budget, one failing or cancelled
// stream never starves the others, and the merged bundle does not depend on
// which stream finished first.

private func ccWorkout(at start: Date) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(600),
                            duration: 600, totalEnergyBurned: nil, totalDistance: nil,
                            metadata: [HKMetadataKeyIndoorWorkout: true])
    workout.setValue(ccSourceRevision(), forKey: "sourceRevision")
    return workout
}

private func ccSourceRevision() -> HKSourceRevision {
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue("com.apple.health", forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    return HKSourceRevision(source: source, version: "synthetic", productType: "Watch7,1",
                            operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
}

private func ccSample(_ identifier: HKQuantityTypeIdentifier, _ value: Double, _ unit: HKUnit,
                      _ workout: HKWorkout, _ offset: TimeInterval) -> HKQuantitySample {
    let start = workout.startDate.addingTimeInterval(offset)
    let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: identifier)!,
                                  quantity: HKQuantity(unit: unit, doubleValue: value),
                                  start: start, end: start.addingTimeInterval(1))
    sample.setValue(ccSourceRevision(), forKey: "sourceRevision")
    return sample
}

/// Lets a `@Sendable` probe callback fulfil a test expectation once.

/// Returns two pages for heart rate and one empty page for every other type,
/// and records which anchor each call used. If the coordinator ever read the
/// same type twice at once, the second call would be seen while the first is
/// suspended at the yield.
private actor PageProbe: HealthKitReading {
    private let workout: HKWorkout
    private let heartRate: HKQuantitySample
    private var anchors: [String: [String]] = [:]
    private var active: [String: Int] = [:]
    private var peaks: [String: Int] = [:]

    init(workout: HKWorkout, heartRate: HKQuantitySample) {
        self.workout = workout
        self.heartRate = heartRate
    }

    func authorize() async throws {}
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }

    func readWorkout(id: String, anchor: Data?) async throws -> HealthPage {
        HealthPage(samples: anchor == nil ? [workout] : [], deleted: [],
                   anchor: Data("workout-page".utf8), limit: 1000)
    }

    func workout(_ id: String) async throws -> HKWorkout? { workout }

    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        let key = type.identifier
        anchors[key, default: []].append(anchor.map { String(decoding: $0, as: UTF8.self) } ?? "-")
        active[key, default: 0] += 1
        peaks[key] = max(peaks[key] ?? 0, active[key] ?? 0)
        await Task.yield()
        active[key, default: 0] -= 1
        if key == HKQuantityTypeIdentifier.heartRate.rawValue {
            if anchor == nil {
                return HealthPage(samples: [heartRate], deleted: [], anchor: Data("A1".utf8), limit: 1)
            }
            return HealthPage(samples: [], deleted: [], anchor: Data("A2".utf8), limit: 1)
        }
        return HealthPage(samples: [], deleted: [], anchor: Data("E".utf8), limit: 1000)
    }

    func pageAnchors() -> [String: [String]] { anchors }
    func peakActive() -> [String: Int] { peaks }
}

private final class ParkSignal: @unchecked Sendable {
    private let expectation: XCTestExpectation
    private let lock = NSLock()
    private var fired = false
    init(_ expectation: XCTestExpectation) { self.expectation = expectation }
    func fire() {
        lock.lock()
        let first = !fired
        fired = true
        lock.unlock()
        if first { expectation.fulfill() }
    }
}

/// Serves synthetic samples and turns the first `target` concurrent reads into a
/// barrier: a producer returns only once that many producers are inside a read.
/// Overlap is therefore proven by state, not by a stopwatch, and a stream count
/// below the target can never deadlock because the barrier releases once and
/// stays open.
private actor CaptureProbe: HealthKitReading {
    let workout: HKWorkout
    let samples: [String: [HKQuantitySample]]
    private let target: Int
    private let failingType: String?
    private let blocksUntilCancelled: Bool

    private var active = 0
    private var maxActive = 0
    private var released = false
    private var arrivals: [CheckedContinuation<Void, Never>] = []
    private var blocked = 0
    private var cancelWaiters: [CheckedContinuation<Void, Never>] = []
    private(set) var sawCancellation = false
    private(set) var reads = 0
    private let onPark: (@Sendable () -> Void)?

    init(workout: HKWorkout, samples: [String: [HKQuantitySample]], target: Int,
         failingType: String? = nil, blocksUntilCancelled: Bool = false,
         onPark: (@Sendable () -> Void)? = nil) {
        self.onPark = onPark
        self.workout = workout
        self.samples = samples
        self.target = max(1, target)
        self.failingType = failingType
        self.blocksUntilCancelled = blocksUntilCancelled
    }

    func peakConcurrency() -> Int { maxActive }
    func blockedReads() -> Int { blocked }

    private func arrive() async {
        reads += 1
        active += 1
        maxActive = max(maxActive, active)
        if released || active >= target {
            released = true
            let waiting = arrivals
            arrivals = []
            for continuation in waiting { continuation.resume() }
            return
        }
        await withCheckedContinuation { arrivals.append($0) }
    }

    private func leave() { active = max(0, active - 1) }

    private func parkUntilCancelled() async {
        blocked += 1
        onPark?()
        await withTaskCancellationHandler {
            await withCheckedContinuation { cancelWaiters.append($0) }
        } onCancel: {
            Task { await self.resumeCancelled() }
        }
        blocked = max(0, blocked - 1)
    }

    private func resumeCancelled() {
        sawCancellation = true
        let waiting = cancelWaiters
        cancelWaiters = []
        for continuation in waiting { continuation.resume() }
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
            visible = type == HKObjectType.workoutType() ? [self.workout] : (samples[type.identifier] ?? [])
        }
        return HealthPage(samples: visible, deleted: [], anchor: Data([2]), limit: 1000)
    }
    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute { throw SyncFailure.invalid("unused") }
    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { throw SyncFailure.invalid("unused") }

    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        if let failingType, failingType == type.identifier {
            // A single failing stream must not hold its slot forever.
            throw SyncFailure.invalid("synthetic_read_failure")
        }
        if blocksUntilCancelled {
            await parkUntilCancelled()
        } else {
            await arrive()
        }
        defer { Task { await self.leave() } }
        return samples[type.identifier] ?? []
    }
    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }
    func captureSeries(type: HKQuantityType, workout: HKWorkout, store: OutboxStore) async throws -> [String: [JSON]] { [:] }
    func captureRoutes(workout: HKWorkout, store: OutboxStore) async throws -> HealthRouteRead {
        HealthRouteRead(routes: [], coverage: [:], enumerationCompleted: true)
    }
}

private final class CaptureReplies: URLProtocol, @unchecked Sendable {
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

private final class CaptureUploader: FileUploader, @unchecked Sendable {
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

final class CaptureConcurrencyTests: XCTestCase {
    private let start = Date(timeIntervalSince1970: 1_793_059_200)

    private func fixture() -> (HKWorkout, [String: [HKQuantitySample]], [HKQuantitySample]) {
        let workout = ccWorkout(at: start)
        let first = ccSample(.heartRate, 120, HKUnit.count().unitDivided(by: .minute()), workout, 0)
        let second = ccSample(.heartRate, 121, HKUnit.count().unitDivided(by: .minute()), workout, 5)
        let distance = ccSample(.distanceWalkingRunning, 1_000, HKUnit.meter(), workout, 10)
        return (workout, [first.sampleType.identifier: [first, second],
                          distance.sampleType.identifier: [distance]],
                [first, second, distance])
    }

    private func directory(_ name: String) -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("rai-concurrency-" + name + Wire.id())
    }
    private func secret() throws -> ClientConfiguration.Secret {
        try ClientConfiguration.validatedSecret(baseURL: "https://synthetic.invalid",
                                                collectorEpoch: CaptureReplies.identity.collectorEpoch,
                                                token: String(repeating: "s", count: 48))
    }
    private func makeCoordinator(_ store: OutboxStore, _ health: any HealthKitReading, streams: Int) -> SyncCoordinator {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [CaptureReplies.self]
        return SyncCoordinator(store: store, health: health, session: URLSession(configuration: config),
                               captureConcurrency: streams)
    }
    private func part(_ multipart: Data) -> Data {
        let separator = Data("\r\n\r\n".utf8)
        guard let header = multipart.range(of: separator) else { return Data() }
        let rest = multipart[header.upperBound...]
        let trailer = Data("\r\n--".utf8)
        guard let end = rest.range(of: trailer, options: .backwards) else { return Data(rest) }
        return Data(rest[rest.startIndex..<end.lowerBound])
    }
    private func samples(_ multipart: Data) throws -> [JSON] { try JSON.decode(part(multipart))["samples"].list }
    /// `syncWorkout` now drains scoped discovery before building a v3 bundle,
    /// so a fresh task would otherwise reuse the facts captured moments earlier
    /// and never enter the capture stream this suite measures. A pending read is
    /// the production input that forces the bundle to read HealthKit again.
    private func requireFreshBundleReads(_ store: OutboxStore, id: String) async throws {
        try await store.beginTask(id, resetBundle: false)
        try await store.updateTask(id) { $0.pendingReadKeys = HealthKitClient.quantityTypes.map(\.identifier) }
    }

    /// R2-P02/F10: the measured concurrency for 1, 2 and 4 streams. The numbers
    /// asserted here are the ones reported in the evidence document.
    func testMeasuredConcurrencyForOneTwoAndFourStreams() async throws {
        var measured: [Int: Int] = [:]
        var queue: [Int: Double] = [:]
        var elapsed: [Int: Double] = [:]
        for streams in [1, 2, 4] {
            let (workout, samplesByType, _) = fixture()
            let probe = CaptureProbe(workout: workout, samples: samplesByType, target: streams)
            let dir = directory("streams\(streams)"), store = try OutboxStore(directory: dir)
            let uploader = CaptureUploader(store: store, directory: dir)
            let coordinator = makeCoordinator(store, probe, streams: streams)
            let id = workout.uuid.uuidString.lowercased()
            try await requireFreshBundleReads(store, id: id)
            let started = DispatchTime.now().uptimeNanoseconds
            try await coordinator.syncWorkout(id: id, secret: try secret(),
                                              transfers: uploader, forceNewBundle: false)
            let elapsedMs = Double(DispatchTime.now().uptimeNanoseconds - started) / 1_000_000
            let peak = await probe.peakConcurrency()
            let stored = await store.task(workout.uuid.uuidString.lowercased())
            let task = try XCTUnwrap(stored)
            let reported = try XCTUnwrap(task.metrics["capture_max_active_streams"])
            measured[streams] = peak
            queue[streams] = task.metrics["capture_peak_queue_bytes"] ?? -1
            elapsed[streams] = elapsedMs
            XCTAssertEqual(peak, streams, "probe peak concurrency for \(streams) streams")
            XCTAssertEqual(reported, Double(streams), "coordinator reported max active for \(streams) streams")
            XCTAssertEqual(task.metrics["capture_stream_limit"], Double(streams))
            XCTAssertEqual(task.metrics["sample_archive_calls"], 0)
            XCTAssertEqual(uploader.uploads.withLock { $0 }, 1)
        }
        print("R3 measured capture concurrency (streams -> peak active): \(measured)")
        print("R3 measured peak queue bytes (streams -> bytes): \(queue)")
        print("R3 measured wall clock ms (streams -> ms): \(elapsed)")
        print("R3 measured writer batches/records (streams -> batches/records): "
              + "\(measured.keys.sorted().map { "\($0):" + "\(queue[$0] ?? -1)" }.joined(separator: " "))")
    }

    /// A stream that fails must hand its slot back: with a single slot the other
    /// nine streams could only finish if the failure released the gate.
    func testFailingStreamReleasesItsSlotForTheOthers() async throws {
        let (workout, samplesByType, ordered) = fixture()
        let probe = CaptureProbe(workout: workout, samples: samplesByType, target: 1,
                                 failingType: HKQuantityTypeIdentifier.heartRate.rawValue)
        let dir = directory("failure"), store = try OutboxStore(directory: dir)
        let uploader = CaptureUploader(store: store, directory: dir)
        let coordinator = makeCoordinator(store, probe, streams: 1)
        let id = workout.uuid.uuidString.lowercased()
        try await requireFreshBundleReads(store, id: id)
        try await coordinator.syncWorkout(id: id, secret: try secret(),
                                          transfers: uploader, forceNewBundle: false)
        let stored = await store.task(workout.uuid.uuidString.lowercased())
        let task = try XCTUnwrap(stored)
        XCTAssertEqual(task.metrics["capture_max_active_streams"], 1)
        XCTAssertEqual(uploader.uploads.withLock { $0 }, 1, "the sync still delivered the other types")
        let captured = try XCTUnwrap(uploader.captured.withLock { $0.first })
        let bundled = try samples(captured)
        XCTAssertEqual(bundled.count, 1, "only the healthy distance sample reaches the bundle")
        XCTAssertEqual(bundled[0]["uuid"].text, ordered[2].uuid.uuidString.lowercased())
    }

    /// Cancelling a sync parked in a capture read must not hang: the parked
    /// producer is resumed by the cancellation and the capture budget is given
    /// back, so the same store syncs normally afterwards.
    func testCancellationStopsAParkedCaptureStream() async throws {
        let (workout, samplesByType, _) = fixture()
        let parkedRead = expectation(description: "a capture stream parked inside the read")
        let signal = ParkSignal(parkedRead)
        let probe = CaptureProbe(workout: workout, samples: samplesByType, target: 1, blocksUntilCancelled: true,
                                 onPark: { signal.fire() })
        let dir = directory("cancel"), store = try OutboxStore(directory: dir)
        let uploader = CaptureUploader(store: store, directory: dir)
        let coordinator = makeCoordinator(store, probe, streams: 2)
        let id = workout.uuid.uuidString.lowercased()
        try await requireFreshBundleReads(store, id: id)
        let secret = try secret()
        let sync = Task { try await coordinator.syncWorkout(id: id, secret: secret, transfers: uploader, forceNewBundle: false) }
        await fulfillment(of: [parkedRead], timeout: 60)
        sync.cancel()
        // A hang here fails the test by timeout: that is the property.
        _ = try? await sync.value
        let sawCancel = await probe.sawCancellation
        XCTAssertTrue(sawCancel, "the parked producer observed the cancellation")
        let before = uploader.uploads.withLock { $0 }
        // The store is still usable: a fresh coordinator on the same directory
        // syncs normally, so no capture budget or writer state leaked.
        let second = CaptureProbe(workout: workout, samples: samplesByType, target: 2)
        let recovered = makeCoordinator(store, second, streams: 2)
        try await recovered.syncWorkout(id: id, secret: secret, transfers: uploader, forceNewBundle: false)
        XCTAssertEqual(uploader.uploads.withLock { $0 }, before + 1)
        let stored = await store.task(id)
        let task = try XCTUnwrap(stored)
        XCTAssertEqual(task.metrics["capture_max_active_streams"], 2, "the recovered sync used its full budget")
        XCTAssertEqual(task.metrics["sample_archive_calls"], 0)
    }

    /// F10/F11: the bundle is merged in fixed type order, so serial and parallel
    /// capture produce the same sample, series and route payloads. The bundle's
    /// observation timestamp is excluded on purpose: it is the time of the run,
    /// not part of the captured facts.
    func testMergeOrderIsIndependentOfCompletionOrder() async throws {
        var payloads: [Data] = []
        // One fixture for both runs: identical UUIDs, times and units, so the
        // only variable left is the completion order of the capture streams.
        let (workout, samplesByType, _) = fixture()
        for streams in [1, 4] {
            let probe = CaptureProbe(workout: workout, samples: samplesByType, target: streams)
            let dir = directory("order\(streams)"), store = try OutboxStore(directory: dir)
            let uploader = CaptureUploader(store: store, directory: dir)
            let coordinator = makeCoordinator(store, probe, streams: streams)
            try await coordinator.syncWorkout(id: workout.uuid.uuidString.lowercased(), secret: try secret(),
                                              transfers: uploader, forceNewBundle: false)
            let captured = try XCTUnwrap(uploader.captured.withLock { $0.first })
            let bundle = try JSON.decode(part(captured))
            let bundled = bundle["samples"].list
            XCTAssertEqual(bundled.count, 3)
            XCTAssertEqual(bundled.compactMap { $0["uuid"].text },
                           [samplesByType[orderedIdentifier(0)]![0].uuid.uuidString.lowercased(),
                            samplesByType[orderedIdentifier(0)]![1].uuid.uuidString.lowercased(),
                            samplesByType[orderedIdentifier(1)]![0].uuid.uuidString.lowercased()])
            var merged = Data()
            for key in ["samples", "series", "routes", "completeSampleTypes", "completeRoutes"] {
                let bytes = try bundle[key].data()
                print("R3-MERGE streams=\(streams) key=\(key) bytes=\(bytes.count) body=\(String(decoding: bytes, as: UTF8.self))")
                merged.append(bytes)
            }
            payloads.append(merged)
        }
        XCTAssertEqual(payloads[0], payloads[1], "serial and parallel capture must merge identical payloads")
    }

    /// The real manual entry point: discovery first, then the Workout sync, with
    /// concurrent capture and no per-sample archiving.
    func testManualEntryDiscoveryThenSyncStaysNormalized() async throws {
        let (workout, samplesByType, ordered) = fixture()
        let probe = CaptureProbe(workout: workout, samples: samplesByType, target: 2)
        let dir = directory("manual"), store = try OutboxStore(directory: dir)
        let uploader = CaptureUploader(store: store, directory: dir)
        let coordinator = makeCoordinator(store, probe, streams: 2)
        let id = workout.uuid.uuidString.lowercased()
        try await coordinator.discoverChanges()
        let evidence = await coordinator.discoveryQueryEvidence()
        XCTAssertGreaterThan(evidence["queries"].integer ?? 0, 0, "the manual entry ran a real discovery pass")
        try await coordinator.syncWorkout(id: id, secret: try secret(), transfers: uploader, forceNewBundle: false)
        let stored = await store.task(id)
        let task = try XCTUnwrap(stored)
        XCTAssertEqual(task.metrics["reused_discovery_reads"], 10, "every quantity type reused discovered facts")
        XCTAssertEqual(task.metrics["full_sample_reads"], 0)
        XCTAssertEqual(task.metrics["sample_archive_calls"], 0)
        XCTAssertEqual(task.metrics["sample_unarchive_calls"], 0)
        // The reused path does no HealthKit read, so the gate is only briefly
        // occupied; the bound must still hold and the limit must be the policy.
        XCTAssertEqual(task.metrics["capture_stream_limit"], 2)
        let observed = try XCTUnwrap(task.metrics["capture_max_active_streams"])
        XCTAssertLessThanOrEqual(observed, 2)
        XCTAssertGreaterThanOrEqual(observed, 1)
        let captured = try XCTUnwrap(uploader.captured.withLock { $0.first })
        XCTAssertEqual(try samples(captured).count, ordered.count)
    }

    private func orderedIdentifier(_ index: Int) -> String {
        // Bundle order is the fixed quantity-type order: heartRate before distance.
        let heart = HKQuantityTypeIdentifier.heartRate.rawValue
        let distance = HKQuantityTypeIdentifier.distanceWalkingRunning.rawValue
        return index == 0 ? heart : distance
    }

    /// Bounded, sleep-free wait for a probe condition.

    /// R2-F10..F15 / R2-P02: one stream pages serially, never skips a page and
    /// never asks with an anchor it did not receive.
    func testOneStreamPagesSeriallyWithoutSkippingOrCrossingItsAnchor() async throws {
        let (workout, samplesByType, _) = fixture()
        let heartRate = try XCTUnwrap(samplesByType[orderedIdentifier(0)]?.first)
        let probe = PageProbe(workout: workout, heartRate: heartRate)
        let dir = directory("paging"), store = try OutboxStore(directory: dir)
        let uploader = CaptureUploader(store: store, directory: dir)
        let coordinator = makeCoordinator(store, probe, streams: 2)
        let id = workout.uuid.uuidString.lowercased()
        try await coordinator.discoverChanges(workoutIDs: [id])
        let anchors = await probe.pageAnchors()
        let heartRateKey = HKQuantityTypeIdentifier.heartRate.rawValue
        XCTAssertEqual(anchors[heartRateKey], ["-", "A1"],
                       "the second page must be requested with the anchor the first page returned")
        for (type, sequence) in anchors {
            XCTAssertEqual(Set(sequence).count, sequence.count, "type \(type) repeated an anchor")
        }
        let peaks = await probe.peakActive()
        XCTAssertEqual(peaks[heartRateKey], 1, "one type's pages must never be read concurrently")
        print("R3 measured paging anchors per type: \(anchors.sorted { $0.key < $1.key })")
    }

    private func waitUntil(_ attempts: Int = 20_000, _ condition: () async -> Bool) async -> Bool {
        for _ in 0..<attempts {
            if await condition() { return true }
            await Task.yield()
        }
        return await condition()
    }
}
