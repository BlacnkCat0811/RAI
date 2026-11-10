import Foundation
import Synchronization
import Testing
@testable import SyncCore

final class SyntheticSessionCredentials: SessionCredentialStore, Sendable {
    let values = Mutex<[String: String]>([:])
    func save(token: String, sessionID: String) throws { values.withLock { $0[sessionID] = token } }
    func load(sessionID: String) throws -> String? { values.withLock { $0[sessionID] } }
}
private func descriptor() -> SyncSessionDescriptor {
    .init(datasetID: Wire.id(), serviceID: Wire.id(), datasetEpoch: Wire.id(), deviceID: Wire.id(), collectorEpoch: Wire.id(), baseURL: URL(string: "https://synthetic.invalid")!)
}
private func handshake(_ d: SyncSessionDescriptor) -> JSON {
    .object(["protocolVersion": .int(3), "datasetID": .string(d.datasetID), "serviceID": .string(d.serviceID),
        "datasetEpoch": .string(d.datasetEpoch), "deviceID": .string(d.deviceID), "collectorEpoch": .string(d.collectorEpoch),
        "datasetStatus": .string("active"), "capabilities": .object(["workoutPayloadVersion": .int(6), "workoutUUIDIngestVersion": .int(1)])])
}
@Suite struct SyncSessionTests {
    func root() -> URL { FileManager.default.temporaryDirectory.appendingPathComponent("rai-session-test-" + Wire.id()) }
    @Test func handshakeAcceptsTheNegotiatedProtocolSixBundleVersion() throws {
        let d = descriptor()
        let hello: JSON = .object(["protocolVersion": .int(3), "datasetID": .string(d.datasetID),
            "serviceID": .string(d.serviceID), "datasetEpoch": .string(d.datasetEpoch),
            "deviceID": .string(d.deviceID), "collectorEpoch": .string(d.collectorEpoch),
            "datasetStatus": .string("active"),
            "capabilities": .object(["workoutPayloadVersion": .int(6), "workoutUUIDIngestVersion": .int(1)])])
        try d.validateHandshake(hello)
    }
    @Test func newSessionUsesIndependentDirectoryKeychainAndEmptyOutbox() async throws {
        let root = root(); defer { try? FileManager.default.removeItem(at: root) }
        let credentials = SyntheticSessionCredentials(), a = descriptor(), b = descriptor()
        let sessions = try SyncSessionStore(root: root, credentials: credentials)
        _ = try await sessions.prepare(a, token: "synthetic-old")
        try await sessions.activate(a.sessionID, handshake: handshake(a))
        let old = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: a.sessionID))
        let workout = Wire.id(), q = DiscoveryQuery(type: "HKWorkoutTypeIdentifier")
        try await old.beginTask(workout)
        try await old.markIngestLane(workout)
        _ = try await old.stageWorkoutIngestPayload(workout,
            body: WorkoutPayloadTransport.multipart(bundle: Data("synthetic-old-bundle".utf8), boundary: "rai.old.boundary"))
        var state = await old.snapshot()
        state.receipts["old"] = .string("synthetic-receipt"); state.localAnchors["old"] = Data([9])
        state.deletedWorkouts = [workout]; state.tombstones = [workout]
        try await old.commit(state)
        _ = try await old.discoveryFlow(q)
        let batch = try await old.stageDiscovery(q, previousAnchor: nil, anchor: Data([9]), events: [DiscoveryEvent(type: q.type, uuid: workout, deleted: true)], hasMore: false)
        try await old.promoteDiscovery(q, batchID: batch)
        let oldBytes = try Data(contentsOf: old.directory.appendingPathComponent("state.json"))
        _ = try await sessions.prepare(b, token: "synthetic-new")
        try await sessions.activate(b.sessionID, handshake: handshake(b))
        let fresh = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: b.sessionID)), next = await fresh.snapshot()
        #expect(old.directory != fresh.directory)
        #expect(try credentials.load(sessionID: a.sessionID) == "synthetic-old")
        #expect(try credentials.load(sessionID: b.sessionID) == "synthetic-new")
        #expect(next.tasks == nil && next.receipts.isEmpty && next.pages.isEmpty)
        #expect(next.deletedWorkouts.isEmpty && next.tombstones.isEmpty && next.localAnchors.isEmpty)
        #expect(next.discovery == nil && next.scope == b.scope)
        #expect(try await fresh.discoveryFlow(q).anchor == nil)
        #expect(!FileManager.default.fileExists(atPath: fresh.directory.appendingPathComponent("workout-ingest." + workout + ".multipart.json").path))
        #expect(try Data(contentsOf: old.directory.appendingPathComponent("state.json")) == oldBytes)
        #expect(try await sessions.snapshot().sessions[a.sessionID]?.status == .retired)
        await #expect(throws: (any Error).self) { try await sessions.requireActive(a.sessionID) }
        #expect(try await sessions.requireActive(b.sessionID).datasetID == b.datasetID)
        let json = try String(contentsOf: root.appendingPathComponent("sessions.json"), encoding: .utf8)
        #expect(!json.contains("synthetic-old") && !json.contains("synthetic-new"))
    }
    @Test func preparedSessionSurvivesRestartAndHandshakeMustPassBeforeAtomicActivation() async throws {
        let root = root(); defer { try? FileManager.default.removeItem(at: root) }
        let credentials = SyntheticSessionCredentials(), a = descriptor(), b = descriptor()
        let store = try SyncSessionStore(root: root, credentials: credentials)
        _ = try await store.prepare(a, token: "old"); try await store.activate(a.sessionID, handshake: handshake(a))
        _ = try await store.prepare(b, token: "new")
        let reopened = try SyncSessionStore(root: root, credentials: credentials)
        #expect(try await reopened.snapshot().sessions[b.sessionID]?.status == .prepared)
        await #expect(throws: (any Error).self) { try await reopened.requireActive(b.sessionID) }
        await #expect(throws: (any Error).self) { try await reopened.activate(b.sessionID, handshake: handshake(a)) }
        #expect(try await reopened.snapshot().activeSessionID == a.sessionID)
        // Fail the atomic manifest replacement. The previous file remains the sole authority.
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: root.path)
        await #expect(throws: (any Error).self) { try await reopened.activate(b.sessionID, handshake: handshake(b)) }
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: root.path)
        #expect(try await reopened.snapshot().activeSessionID == a.sessionID)
        try await reopened.activate(b.sessionID, handshake: handshake(b))
        let final = try SyncSessionStore.read(root: root)
        #expect(final.activeSessionID == b.sessionID && final.sessions[a.sessionID]?.status == .retired && final.sessions[b.sessionID]?.status == .active)
        _ = try await reopened.prepare(b, token: "new")
        #expect(try await reopened.snapshot().sessions.count == 2)
    }
    @Test func oldBackgroundCallbackResolvesOnlyItsOwnSession() async throws {
        let root = root(); defer { try? FileManager.default.removeItem(at: root) }
        let credentials = SyntheticSessionCredentials(), a = descriptor(), b = descriptor()
        let sessions = try SyncSessionStore(root: root, credentials: credentials)
        _ = try await sessions.prepare(a, token: "old"); try await sessions.activate(a.sessionID, handshake: handshake(a))
        _ = try await sessions.prepare(b, token: "new"); try await sessions.activate(b.sessionID, handshake: handshake(b))
        let old = try #require(try await sessions.resolveBackground(a.backgroundIdentifier))
        #expect(old.sessionID == a.sessionID && old.status == .retired)
        #expect(a.backgroundIdentifier != b.backgroundIdentifier)
        let oldDir = try SyncSessionStore.directory(root: root, sessionID: old.sessionID)
        let newDir = try SyncSessionStore.directory(root: root, sessionID: b.sessionID)
        try OutboxStore.write(Data("late-old-callback".utf8), to: oldDir.appendingPathComponent("result.transfer.json"))
        #expect(!FileManager.default.fileExists(atPath: newDir.appendingPathComponent("result.transfer.json").path))
        #expect(try await sessions.resolveBackground("com.rai.v2.sync.unknown") == nil)
    }
    @Test func nilAnchorDiscoversVisibleHistoryWithoutInheritingOldDeletionAndRepeatedDiscoveryIsIdempotent() async throws {
        let root = root(); defer { try? FileManager.default.removeItem(at: root) }
        let credentials = SyntheticSessionCredentials(), a = descriptor(), b = descriptor(), workout = Wire.id()
        let sessions = try SyncSessionStore(root: root, credentials: credentials)
        _ = try await sessions.prepare(a, token: "old")
        let old = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: a.sessionID))
        let query = DiscoveryQuery(type: "HKWorkoutTypeIdentifier")
        _ = try await old.discoveryFlow(query)
        let deletion = try await old.stageDiscovery(query, previousAnchor: nil, anchor: Data([99]), events: [DiscoveryEvent(type: query.type, uuid: workout, deleted: true)], hasMore: false)
        try await old.promoteDiscovery(query, batchID: deletion)
        _ = try await sessions.prepare(b, token: "new"); try await sessions.activate(b.sessionID, handshake: handshake(b))
        let fresh = try OutboxStore(directory: SyncSessionStore.directory(root: root, sessionID: b.sessionID))
        #expect(try await fresh.discoveryFlow(query).anchor == nil)
        let data = Data("synthetic-visible-workout".utf8), file = try await fresh.savePayload(data)
        let visible = DiscoveryEvent(type: query.type, uuid: workout, deleted: false, archiveFile: file, archiveHash: Wire.hash(data))
        let page = try await fresh.stageDiscovery(query, previousAnchor: nil, anchor: Data([1]), events: [visible], hasMore: false)
        try await fresh.promoteDiscovery(query, batchID: page)
        let replay = try await fresh.stageDiscovery(query, previousAnchor: Data([1]), anchor: Data([2]), events: [visible], hasMore: false)
        try await fresh.promoteDiscovery(query, batchID: replay)
        let next = await fresh.discoverySnapshot()
        #expect(next.work[workout]?.generation == 1 && next.work[workout]?.selected == false)
        #expect(next.events.count == 1 && next.workoutArchives[workout] != nil)
        #expect(await old.discoverySnapshot().flows[query.key]?.anchor == Data([99]))
        #expect(await fresh.snapshot().deletedWorkouts.isEmpty)
    }
    @Test func failedOutboxInitializationResumesSamePreparedIdentity() async throws {
        let root = root(); defer { try? FileManager.default.removeItem(at: root) }
        let credentials = SyntheticSessionCredentials(), d = descriptor()
        let sessions = try SyncSessionStore(root: root, credentials: credentials)
        let directory = try SyncSessionStore.directory(root: root, sessionID: d.sessionID)
        try Data("synthetic-blocker".utf8).write(to: directory)
        await #expect(throws: (any Error).self) { try await sessions.prepare(d, token: "prepared") }
        let reopened = try SyncSessionStore(root: root, credentials: credentials)
        #expect(try await reopened.snapshot().sessions[d.sessionID]?.status == .prepared)
        #expect(try credentials.load(sessionID: d.sessionID) == "prepared")
        try FileManager.default.removeItem(at: directory)
        try await reopened.activate(d.sessionID, handshake: handshake(d))
        #expect(try await reopened.requireActive(d.sessionID).datasetID == d.datasetID)
        let outbox = try OutboxStore(directory: directory)
        #expect(await outbox.snapshot().discovery == nil)
    }
    @Test func legacyOutboxCannotBeReboundAsNewSession() async throws {
        let root = root(); defer { try? FileManager.default.removeItem(at: root) }
        let credentials = SyntheticSessionCredentials(), old = descriptor()
        let legacy = try OutboxStore(directory: root); try await legacy.bind(old.scope)
        let bytes = try Data(contentsOf: root.appendingPathComponent("state.json"))
        let sessions = try SyncSessionStore(root: root, credentials: credentials)
        await #expect(throws: (any Error).self) { try await sessions.prepare(old, token: "new") }
        _ = try await sessions.prepare(descriptor(), token: "new")
        #expect(try Data(contentsOf: root.appendingPathComponent("state.json")) == bytes)
    }
    @Test func reusedTokenOrDatasetCannotPrepareAnotherSession() async throws {
        let root = root(); defer { try? FileManager.default.removeItem(at: root) }
        let credentials = SyntheticSessionCredentials(), a = descriptor()
        let store = try SyncSessionStore(root: root, credentials: credentials)
        _ = try await store.prepare(a, token: "old")
        await #expect(throws: (any Error).self) { try await store.prepare(descriptor(), token: "old") }
        let copied = SyncSessionDescriptor(datasetID: a.datasetID, serviceID: Wire.id(), datasetEpoch: Wire.id(), deviceID: Wire.id(), collectorEpoch: Wire.id(), baseURL: a.baseURL)
        await #expect(throws: (any Error).self) { try await store.prepare(copied, token: "new") }
    }
}
