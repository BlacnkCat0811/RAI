import XCTest
import HealthKit
import SyncCore
@testable import RAI

/// Opt-in live test for the foreground protocol-6 upload path.
///
/// It runs the same `SyncCoordinator.uploadWorkouts` the App uses, over a real
/// `URLSession` and real TCP, against a locally simulated cloud (isolated Go API
/// + PostgreSQL 18). HealthKit is replaced by an in-process synthetic reader, so
/// nothing here touches real health data.
///
/// Skipped unless the runner injects all three variables:
/// `RAI_LIVE_SYNC_BASE_URL`, `RAI_LIVE_SYNC_TOKEN`, `RAI_LIVE_SYNC_COLLECTOR_EPOCH`.
final class WorkoutUploadLiveTests: XCTestCase {
    private struct Live: Sendable {
        let baseURL: String
        let token: String
        let collectorEpoch: String
    }

    private func live() throws -> Live {
        let environment = ProcessInfo.processInfo.environment
        let baseURL = environment["RAI_LIVE_SYNC_BASE_URL"] ?? ""
        let token = environment["RAI_LIVE_SYNC_TOKEN"] ?? ""
        let collectorEpoch = environment["RAI_LIVE_SYNC_COLLECTOR_EPOCH"] ?? ""
        guard !baseURL.isEmpty, !token.isEmpty, !collectorEpoch.isEmpty else {
            throw XCTSkip("live sync target not configured")
        }
        return Live(baseURL: baseURL, token: token, collectorEpoch: collectorEpoch)
    }

    private func secret(_ live: Live) throws -> ClientConfiguration.Secret {
        try ClientConfiguration.validatedSecret(baseURL: live.baseURL,
                                               collectorEpoch: live.collectorEpoch,
                                               token: live.token)
    }

    private func directory(_ name: String) -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("rai-live-upload-" + name + Wire.id())
    }

    private func coordinator(_ health: LiveHealth, store: OutboxStore) -> SyncCoordinator {
        // No URLProtocol classes: this is the real transport.
        let configuration = URLSessionConfiguration.ephemeral
        configuration.timeoutIntervalForRequest = 60
        configuration.timeoutIntervalForResource = 300
        return SyncCoordinator(store: store, health: health, session: URLSession(configuration: configuration))
    }

    private func report(_ label: String, _ outcome: WorkoutUploadOutcome) {
        let accepted = outcome.acceptedAt.map { ISO8601DateFormatter().string(from: $0) } ?? "nil"
        print("LIVE_UPLOAD \(label) workout=\(outcome.workoutID) acceptedAt=\(accepted) errorCode=\(outcome.errorCode ?? "nil")")
    }

    /// One Workout with quantities, one series group and one route goes through
    /// build → gzip multipart → single POST, and the receipt is durable.
    func testUploadsProtocolSixWorkoutToLocalCloud() async throws {
        let live = try live()
        let dir = directory("single")
        defer { try? FileManager.default.removeItem(at: dir) }
        let start = Date(timeIntervalSince1970: 1_793_059_200)
        let workout = liveWorkout(at: start)
        let health = LiveHealth(workouts: [workout])
        let workoutID = workout.uuid.uuidString.lowercased()
        await health.attach(rich: workout)

        let outcomes = try await coordinator(health, store: try OutboxStore(directory: dir))
            .uploadWorkouts([workoutID], secret: try secret(live), transfers: LiveUploader())

        let outcome = try XCTUnwrap(outcomes.first)
        report("first", outcome)
        XCTAssertTrue(outcome.succeeded, "live upload failed: \(outcome.errorCode ?? "?")")
        XCTAssertEqual(outcome.workoutID, workoutID)
        let acceptedAt = try XCTUnwrap(outcome.acceptedAt)
        // A server clock far from the client clock would invalidate the receipt.
        XCTAssertLessThan(abs(acceptedAt.timeIntervalSinceNow), 600)

        let counters = await health.counters()
        print("LIVE_UPLOAD child-reads workout=\(counters.workouts) sampleTypes=\(counters.samples) routes=\(counters.routes)")
        XCTAssertEqual(counters.workouts, 1, "one Workout must be read exactly once")
        XCTAssertGreaterThan(counters.samples, 0, "the bundle must read quantity types for this Workout")
        XCTAssertEqual(counters.routes, 1)
    }

    /// A second upload of the same UUID is a replay: the server returns the
    /// original receipt, never a new acceptance and never a rejection.
    func testSecondUploadOfSameUUIDReplaysOriginalReceipt() async throws {
        let live = try live()
        let dir = directory("replay")
        defer { try? FileManager.default.removeItem(at: dir) }
        let workout = liveWorkout(at: Date(timeIntervalSince1970: 1_793_063_000))
        let workoutID = workout.uuid.uuidString.lowercased()
        let store = try OutboxStore(directory: dir)

        let first = try await coordinator(LiveHealth(workouts: [workout]), store: store)
            .uploadWorkouts([workoutID], secret: try secret(live), transfers: LiveUploader())
        let firstOutcome = try XCTUnwrap(first.first)
        report("replay-first", firstOutcome)
        let firstAcceptedAt = try XCTUnwrap(firstOutcome.acceptedAt)

        let second = try await coordinator(LiveHealth(workouts: [workout]), store: store)
            .uploadWorkouts([workoutID], secret: try secret(live), transfers: LiveUploader())
        let secondOutcome = try XCTUnwrap(second.first)
        report("replay-second", secondOutcome)

        XCTAssertTrue(secondOutcome.succeeded, "replay failed: \(secondOutcome.errorCode ?? "?")")
        XCTAssertEqual(secondOutcome.acceptedAt, firstAcceptedAt)
    }

    /// The handshake itself rejects a device token the dataset never issued.
    func testHandshakeRejectsUnknownToken() async throws {
        let live = try live()
        let dir = directory("token")
        defer { try? FileManager.default.removeItem(at: dir) }
        let workout = liveWorkout(at: Date(timeIntervalSince1970: 1_793_066_000))
        var forged = live
        forged = Live(baseURL: live.baseURL,
                      token: String(repeating: "z", count: 43),
                      collectorEpoch: live.collectorEpoch)

        do {
            _ = try await coordinator(LiveHealth(workouts: [workout]), store: try OutboxStore(directory: dir))
                .uploadWorkouts([workout.uuid.uuidString.lowercased()], secret: try secret(forged),
                                transfers: LiveUploader())
            XCTFail("an unissued token must not upload")
        } catch let error as UploadError {
            print("LIVE_UPLOAD forged-token status=\(error.status) code=\(error.code)")
            XCTAssertEqual(error.status, 401)
        }
    }
}

// MARK: - Synthetic HealthKit

private func liveRevision(_ bundleID: String, _ productType: String?) -> HKSourceRevision {
    let source = class_createInstance(HKSource.self, 0) as! HKSource
    source.setValue(bundleID, forKey: "bundleIdentifier")
    source.setValue("Apple Watch", forKey: "name")
    return HKSourceRevision(source: source, version: "synthetic", productType: productType,
                            operatingSystemVersion: OperatingSystemVersion(majorVersion: 26, minorVersion: 0, patchVersion: 0))
}

private func liveWorkout(at start: Date) -> HKWorkout {
    let workout = HKWorkout(activityType: .running, start: start, end: start.addingTimeInterval(600), duration: 600,
                            totalEnergyBurned: nil, totalDistance: nil,
                            metadata: [HKMetadataKeyIndoorWorkout: true])
    workout.setValue(liveRevision("com.apple.health", "Watch7,1"), forKey: "sourceRevision")
    return workout
}

private func liveQuantity(_ workout: HKWorkout, type: HKQuantityTypeIdentifier, unit: HKUnit,
                          value: Double, offset: TimeInterval) -> HKQuantitySample {
    let sample = HKQuantitySample(type: HKObjectType.quantityType(forIdentifier: type)!,
                                  quantity: HKQuantity(unit: unit, doubleValue: value),
                                  start: workout.startDate.addingTimeInterval(offset),
                                  end: workout.startDate.addingTimeInterval(offset + 1))
    sample.setValue(workout.sourceRevision, forKey: "sourceRevision")
    return sample
}

/// A synthetic `HealthKitReading`: one Workout, read exactly once per upload.
private actor LiveHealth: HealthKitReading {
    private let workouts: [HKWorkout]
    private var samples: [String: [HKSample]] = [:]
    private var series: [String: [HealthSeriesGroup]] = [:]
    private var routes: [HealthRouteBundle] = []
    private var workoutReads = 0, sampleReads = 0, routeReads = 0

    init(workouts: [HKWorkout]) { self.workouts = workouts }

    func attach(rich workout: HKWorkout) {
        let startUs = Normalizer.us(workout.startDate)
        samples[HKQuantityTypeIdentifier.heartRate.rawValue] = [
            liveQuantity(workout, type: .heartRate, unit: HKUnit.count().unitDivided(by: .minute()),
                         value: 132, offset: 30),
            liveQuantity(workout, type: .heartRate, unit: HKUnit.count().unitDivided(by: .minute()),
                         value: 148, offset: 120),
        ]
        samples[HKQuantityTypeIdentifier.runningPower.rawValue] = [
            liveQuantity(workout, type: .runningPower, unit: HKUnit.watt(), value: 268, offset: 60),
        ]
        series[HKQuantityTypeIdentifier.runningPower.rawValue] = [
            HealthSeriesGroup(startUs: startUs + 60_000_000, endUs: startUs + 61_000_000, value: 268,
                              points: [HealthSeriesPoint(startUs: startUs + 60_100_000, endUs: startUs + 60_500_000,
                                                         value: 270)]),
        ]
        routes = [HealthRouteBundle(id: Wire.id(), source: "com.apple.health",
                                    points: [RoutePoint(recordedAtUs: startUs + 1_000_000, latitude: 31.2304,
                                                        longitude: 121.4737, altitude: 12, speed: 3.1),
                                             RoutePoint(recordedAtUs: startUs + 2_000_000, latitude: 31.2305,
                                                        longitude: 121.4738, altitude: 12.4, speed: 3.2)])]
    }

    func counters() -> (workouts: Int, samples: Int, routes: Int) { (workoutReads, sampleReads, routeReads) }

    func authorize() async throws {}

    func workout(_ id: String) async throws -> HKWorkout? {
        workoutReads += 1
        return workouts.first { $0.uuid.uuidString.lowercased() == id }
    }

    func read(type: HKSampleType, anchor: Data?, workout: HKWorkout?) async throws -> HealthPage {
        HealthPage(samples: [], deleted: [], anchor: Data([3]), limit: 1000)
    }

    func route(_ route: HKWorkoutRoute, directory: URL) async throws -> PreparedRoute {
        throw SyncFailure.invalid("unused")
    }

    func series(_ sample: HKQuantitySample, scope: Scope, store: OutboxStore) async throws -> [OutboxOperation] { [] }

    func bundleSamples(type: HKQuantityType, workout: HKWorkout) async throws -> [HKSample] {
        sampleReads += 1
        return samples[type.identifier] ?? []
    }

    func bundleSeries(type: HKQuantityType, workout: HKWorkout) async throws -> [String: [JSON]] { [:] }

    func bundleSeriesGroups(type: HKQuantityType, workout: HKWorkout) async throws -> [HealthSeriesGroup] {
        series[type.identifier] ?? []
    }

    func bundleRoutes(workout: HKWorkout) async throws -> [HealthRouteBundle] {
        routeReads += 1
        return routes
    }
}

/// File transport over real TCP. The live tests below also exercise the actual
/// BackgroundTransfers delegate/session, rather than only this passthrough.
private struct LiveUploader: FileUploader {
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let result = try await response(request, file: file, identity: identity)
        return (result.data, result.status, result.retryAfter)
    }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        let (data, response) = try await URLSession.shared.upload(for: request, fromFile: file)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        return UploadResponse(data: data, status: http.statusCode,
            retryAfter: RAIAPIClient.retryAfter(http.value(forHTTPHeaderField: "Retry-After")),
            contentType: http.value(forHTTPHeaderField: "Content-Type"))
    }
}

extension WorkoutUploadLiveTests {
    func testBackgroundSessionUploadsHistoryAndReopensReceipts() async throws {
        let live = try live(), dir = directory("background-history")
        defer { try? FileManager.default.removeItem(at: dir) }
        let workouts = (0..<4).map { liveWorkout(at: Date(timeIntervalSince1970: 1_793_059_200 + Double($0) * 3600)) }
        let store = try OutboxStore(directory: dir)
        let transfers = BackgroundTransfers(directory: dir, identifier: "com.rai.tests." + Wire.id())
        let health = LiveHealth(workouts: workouts)
        let coordinator = coordinator(health, store: store)
        let ids = workouts.map { $0.uuid.uuidString.lowercased() }
        let outcomes = try await coordinator.uploadWorkouts(ids, secret: secret(live), transfers: transfers)
        XCTAssertEqual(outcomes.map(\.workoutID), ids)
        XCTAssertTrue(outcomes.allSatisfy(\.succeeded), "file-based history upload failed")
        let reopened = self.coordinator(LiveHealth(workouts: []), store: try OutboxStore(directory: dir))
        let receipts = try await reopened.reconcileWorkoutUploads(transfers: transfers)
        XCTAssertEqual(Set(receipts.map(\.workoutID)), Set(ids))
        XCTAssertTrue(receipts.allSatisfy(\.succeeded))
    }
}
