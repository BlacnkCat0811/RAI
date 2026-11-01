import Foundation
import XCTest
import Synchronization
@testable import SyncCore

/// Launched only by TestWorkoutReliabilitySwiftClientPostgres against its
/// disposable PG18-backed loopback server. No HealthKit or real device input.
final class WorkoutReliabilityLiveTests: XCTestCase {
    func testActualClientHTTPDatabaseRecovery() async throws {
        let env = ProcessInfo.processInfo.environment
        guard env["RAI_PHASE2_ISOLATED"] == "synthetic-pg18", let address = env["RAI_PHASE2_URL"],
              let url = URL(string: address), url.host == "127.0.0.1", let token = env["RAI_PHASE2_TOKEN"],
              let input = env["RAI_PHASE2_BUNDLE"], let mode = env["RAI_PHASE2_MODE"] else {
            throw XCTSkip("Run via isolated PostgreSQL Go integration harness")
        }
        let bundle = try Data(contentsOf: URL(fileURLWithPath: input)), value = try JSON.decode(bundle)
        let id = value["workoutID"].text!, hash = Wire.hash(bundle)
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-live-outbox-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = try OutboxStore(directory: dir)
        try await store.beginTask(id)
        let body = WorkoutBundleAssembler.multipart(bundle: bundle, boundary: "live-synthetic")
        let name = try await store.savePayload(body, suffix: "multipart.json")
        if mode == "phase3_deleted_parent" {
            let deletion = try JSON.decode(Data((env["RAI_PHASE3_PARENT_DELETE_ACK"] ?? "").utf8))
            try await store.acknowledgeWorkoutDeletion(id, receipt: deletion)
            try await store.setDeletedParentBundle(id, file: name, hash: hash, boundary: "live-synthetic", generation: 1, deletionIDs: [], sampleCount: value["sampleDeletions"].list.count, routeCount: value["routeDeletions"].list.count)
            do { try await store.releaseBundle(id); XCTFail("Parent DELETE ACK released child POST") } catch { }
        } else {
            try await store.setBundle(id, file: name, sha256: hash, boundary: "live-synthetic", sampleDeletions: value["sampleDeletions"].list.count, routeDeletions: value["routeDeletions"].list.count)
        }
        let file = try await store.fileURL(name)
        let refreshes = Mutex(0)
        let refresher: @Sendable () async throws -> String = { refreshes.withLock { $0 += 1 }; return token }
        let api = RAIAPIClient(baseURL: url, token: mode == "auth_refresh" ? "synthetic-expired" : token,
                              refreshToken: mode == "auth_refresh" ? refresher : nil)
        let started = Date(), first: Bool
        do {
            if mode == "two_workers" {
                async let a = WorkoutBundleDelivery(store: store, api: api).deliver(id)
                async let b = WorkoutBundleDelivery(store: store, api: api).deliver(id)
                let outcomes = try await [a, b]
                XCTAssertEqual(outcomes.filter { $0 }.count, 1)
                first = outcomes.contains(true)
            } else { first = try await WorkoutBundleDelivery(store: store, api: api).deliver(id) }
        } catch { first = false }
        let controlAPI = RAIAPIClient(baseURL: url, token: token)
        let evidence = try await controlAPI.call("GET", "/v2/test-evidence")
        let savedTask = await store.task(id)
        let task = try XCTUnwrap(savedTask)
        // Even successful delivery only marks GC; the bytes still exist here.
        XCTAssertEqual(try Data(contentsOf: file), body)
        if ["lost_ack", "corrupt_ack", "auth_refresh", "two_workers", "duplicate_post", "phase3_observation", "phase3_child_delete", "phase3_deleted_parent"].contains(mode) {
            XCTAssertTrue(first); XCTAssertNotNil(task.uploadReceipt)
            XCTAssertEqual(evidence["imports"].integer, mode == "phase3_deleted_parent" ? 0 : (mode == "phase3_child_delete" ? 2 : 1))
        } else {
            XCTAssertFalse(first); XCTAssertNil(task.uploadReceipt); XCTAssertNil(task.acknowledgedAt)
            XCTAssertNotNil(task.bundleFile)
            XCTAssertEqual(evidence["imports"].integer, mode == "commit_404" ? 1 : 0)
        }
        if mode == "rate_limit" { XCTAssertGreaterThanOrEqual(task.nextAttemptAt!, started.addingTimeInterval(120)) }
        if mode == "auth_refresh" { XCTAssertEqual(refreshes.withLock { $0 }, 1) }
        if mode == "two_workers" { XCTAssertEqual(evidence["posts"].integer, 1) }
        if mode == "duplicate_post" {
            let replay = try await api.call("POST", "/v2/sync/workout-bundles", file: file,
                mediaType: "multipart/form-data; boundary=live-synthetic", identity: id + ":" + hash)
            XCTAssertEqual(replay, task.uploadReceipt)
            try await store.acknowledgeBundle(id, receipt: replay)
            XCTAssertEqual(try Data(contentsOf: file), body)
        }
        if ["validation_missing", "tombstone"].contains(mode) {
            XCTAssertEqual(task.phase, .failed)
            XCTAssertEqual(evidence["samples"].integer, 0)
            XCTAssertEqual(evidence["jobs"].integer, 0)
            XCTAssertEqual(evidence["tombstones"].integer, mode == "tombstone" ? 1 : 0)
            return
        }
        if !first {
            let reopened = try OutboxStore(directory: dir)
            let due = try XCTUnwrap(task.nextAttemptAt)
            let finished = try await WorkoutBundleDelivery(store: reopened, api: api).deliver(id, now: due)
            XCTAssertTrue(finished)
            let reopenedTask = await reopened.task(id)
            let restored = try XCTUnwrap(reopenedTask)
            XCTAssertEqual(restored.phase, .acknowledged)
            XCTAssertNotNil(restored.uploadReceipt)
            XCTAssertEqual(restored.bundleSHA256, hash)
            _ = try await reopened.collectGarbage(dryRun: false)
        } else { _ = try await store.collectGarbage(dryRun: false) }
        XCTAssertFalse(FileManager.default.fileExists(atPath: file.path))
        let final = try await controlAPI.call("GET", "/v2/test-evidence")
        XCTAssertEqual(final["imports"].integer, mode == "phase3_deleted_parent" ? 0 : (mode == "phase3_child_delete" ? 2 : 1))
        XCTAssertEqual(final["samples"].integer, ["phase3_child_delete", "phase3_deleted_parent"].contains(mode) ? 0 : 1)
        XCTAssertEqual(final["jobs"].integer, mode == "phase3_deleted_parent" ? 2 : (mode == "phase3_child_delete" ? 3 : 1))
        XCTAssertEqual(final["tombstones"].integer, mode == "phase3_deleted_parent" ? 3 : (mode == "phase3_child_delete" ? 2 : 0))
        if mode == "phase3_deleted_parent" {
            let finalTask = await store.task(id)
            XCTAssertEqual(finalTask?.phase, .deleted); XCTAssertNotNil(finalTask?.deletionReceipt)
        }
        let recovered = try await api.call("GET", "/v2/sync/workout-bundles/" + id, query: ["sha256": hash])
        try WorkoutBundleACK.validate(recovered, workoutID: id, bundleHash: hash, sampleDeletions: value["sampleDeletions"].list.count, routeDeletions: value["routeDeletions"].list.count)
    }
}
