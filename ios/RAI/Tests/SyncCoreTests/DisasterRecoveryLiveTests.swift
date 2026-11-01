import Foundation
import XCTest
@testable import SyncCore

final class DisasterRecoveryLiveTests: XCTestCase {
    func testRecoveryAcrossActualSwiftGoPostgres() async throws {
        let env = ProcessInfo.processInfo.environment
        guard env["RAI_PHASE5B_ISOLATED"] == "synthetic-pg18", let path = env["RAI_PHASE5B_PRIVATE_CONFIG"] else { throw XCTSkip("Run via disposable PostgreSQL recovery harness") }
        let config = try JSON.decode(Data(contentsOf: URL(fileURLWithPath: path)))
        let url = try XCTUnwrap(URL(string: config["url"].text!))
        XCTAssertEqual(url.host, "127.0.0.1")
        let a = config["oldEnrollment"]
        let old = SyncSessionDescriptor(datasetID: a["datasetID"].text!, serviceID: a["serviceID"].text!, datasetEpoch: a["datasetEpoch"].text!, deviceID: a["deviceID"].text!, collectorEpoch: a["collectorEpoch"].text!, baseURL: url)
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let keys = SyntheticSessionCredentials(), sessions = try SyncSessionStore(root: root, credentials: keys)
        _ = try await sessions.prepare(old, token: a["token"].text!)
        let apiA = RAIAPIClient(baseURL: url, token: a["token"].text!)
        try await sessions.activate(old.sessionID, handshake: apiA.call("GET", "/v2/handshake"))
        let oldStore = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: old.sessionID))
        let raw = try Data(contentsOf: URL(fileURLWithPath: config["oldBundle"].text!)), value = try JSON.decode(raw), id = value["workoutID"].text!
        try await oldStore.beginTask(id)
        let file = try await oldStore.savePayload(WorkoutBundleAssembler.multipart(bundle: raw, boundary: "old"))
        try await oldStore.setBundle(id, file: file, sha256: Wire.hash(raw), boundary: "old")
        let deliveredA = try await WorkoutBundleDelivery(store: oldStore, api: apiA).deliver(id)
        XCTAssertTrue(deliveredA)
        _ = try await oldStore.collectGarbage(dryRun: false)
        XCTAssertFalse(FileManager.default.fileExists(atPath: oldStore.directory.appendingPathComponent(file).path))
        let oldState = try Data(contentsOf: oldStore.directory.appendingPathComponent("state.json"))
        _ = try await apiA.call("POST", "/v2/test-disaster")
        let package = try JSONDecoder().decode(RecoveryCredentialPackage.self, from: Data(contentsOf: URL(fileURLWithPath: config["targetPackage"].text!)))
        let next = try package.descriptor()
        _ = try await sessions.prepare(next, token: package.enrollment.token, recovery: true)
        let apiB = RAIAPIClient(baseURL: url, token: package.enrollment.token)
        try await sessions.activate(next.sessionID, handshake: apiB.call("GET", "/v2/handshake"))
        let fresh = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: next.sessionID))
        let initial = await fresh.snapshot(); XCTAssertTrue(initial.receipts.isEmpty); XCTAssertNil(initial.tasks)
        let query = DiscoveryQuery(type: "HKWorkoutTypeIdentifier")
        let flow = try await fresh.discoveryFlow(query); XCTAssertNil(flow.anchor)
        let visible = try JSON.decode(Data(contentsOf: URL(fileURLWithPath: config["visibleFacts"].text!)))
        // The synthetic current HealthKit read is assembled anew; old JSON bytes remain immutable.
        let newBundle = try WorkoutBundleAssembler.data(scope: next.scope, workoutID: visible["workoutID"].text!, workout: visible["workout"], samples: visible["samples"].list, series: visible["series"].list, routes: visible["routes"].list, completeSampleTypes: visible["completeSampleTypes"].list.compactMap(\.text), completeRoutes: visible["completeRoutes"] == .bool(true), observation: visible["observation"])
        XCTAssertNotEqual(Wire.hash(newBundle), Wire.hash(raw))
        try await fresh.beginTask(id)
        let newFile = try await fresh.savePayload(WorkoutBundleAssembler.multipart(bundle: newBundle, boundary: "new"))
        try await fresh.setBundle(id, file: newFile, sha256: Wire.hash(newBundle), boundary: "new")
        let deliveredB = try await WorkoutBundleDelivery(store: fresh, api: apiB).deliver(id)
        XCTAssertTrue(deliveredB) // server commit + dropped ACK -> exact status recovery
        let newTask = await fresh.task(id); XCTAssertNotNil(newTask?.uploadReceipt)
        let replay = root.appendingPathComponent("old-replay.multipart")
        try WorkoutBundleAssembler.multipart(bundle: raw, boundary: "replay").write(to: replay)
        do { _ = try await apiB.call("POST", "/v2/sync/workout-bundles", file: replay, mediaType: "multipart/form-data; boundary=replay"); XCTFail("Old bundle accepted") }
        catch let error as UploadError { XCTAssertEqual(error.code, "dataset_forbidden") }
        do { _ = try await apiA.call("GET", "/v2/handshake"); XCTFail("Old token accepted") }
        catch let error as UploadError { XCTAssertEqual(error.status, 401) }
        XCTAssertEqual(try Data(contentsOf: oldStore.directory.appendingPathComponent("state.json")), oldState)
        let manifest = try await sessions.snapshot(); XCTAssertEqual(manifest.sessions[old.sessionID]?.status, .retired); XCTAssertEqual(manifest.activeSessionID, next.sessionID)
    }
}
