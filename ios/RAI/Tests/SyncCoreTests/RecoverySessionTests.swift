import Foundation
import Testing
@testable import SyncCore

private func recoveryDescriptor() -> SyncSessionDescriptor {
    .init(datasetID: Wire.id(), serviceID: Wire.id(), datasetEpoch: Wire.id(), deviceID: Wire.id(), collectorEpoch: Wire.id(), baseURL: URL(string: "https://synthetic.invalid")!)
}
private func recoveryHello(_ d: SyncSessionDescriptor) -> JSON {
    .object(["protocolVersion": .int(3), "datasetID": .string(d.datasetID), "serviceID": .string(d.serviceID), "datasetEpoch": .string(d.datasetEpoch), "deviceID": .string(d.deviceID), "collectorEpoch": .string(d.collectorEpoch), "datasetStatus": .string("active"), "capabilities": .object(["workoutPayloadVersion": .int(6), "workoutUUIDIngestVersion": .int(1)])])
}
@Suite struct RecoverySessionTests {
    @Test func OldSessionBecomesRecoveryRequiredAndOnlyExplicitIdentityProofTriggersIt() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let keys = SyntheticSessionCredentials(), a = recoveryDescriptor(), b = recoveryDescriptor()
        let sessions = try SyncSessionStore(root: root, credentials: keys)
        _ = try await sessions.prepare(a, token: "synthetic-A"); try await sessions.activate(a.sessionID, handshake: recoveryHello(a))
        await #expect(throws: (any Error).self) { try await sessions.validateActiveHandshake(a.sessionID, handshake: recoveryHello(b)) }
        let m = try await sessions.snapshot()
        #expect(m.activeSessionID == nil && m.sessions[a.sessionID]?.status == .recoveryRequired)
        await #expect(throws: (any Error).self) { try await sessions.activate(a.sessionID, handshake: recoveryHello(a)) }
        _ = try await sessions.prepare(b, token: "synthetic-B", recovery: true)
        try await sessions.activate(b.sessionID, handshake: recoveryHello(b))
        #expect(try await sessions.snapshot().sessions[a.sessionID]?.status == .retired)
    }
    @Test func RecoverySessionDoesNotReuseOldACKOutboxDeletedSetOrKeychainAccountAndCanRepeat() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let keys = SyntheticSessionCredentials(), descriptors = [recoveryDescriptor(), recoveryDescriptor(), recoveryDescriptor()]
        let sessions = try SyncSessionStore(root: root, credentials: keys)
        let query = DiscoveryQuery(type: "HKWorkoutTypeIdentifier"), workout = Wire.id()
        var previous: [(URL, Data)] = []
        for (index, d) in descriptors.enumerated() {
            _ = try await sessions.prepare(d, token: "synthetic-\(index)", recovery: index > 0)
            if index > 0 {
                #expect(try await sessions.snapshot().activeSessionID == nil)
                #expect(try await sessions.snapshot().sessions[descriptors[index-1].sessionID]?.status == .recoveryRequired)
            }
            let restarted = try SyncSessionStore(root: root, credentials: keys) // Kill B / C
            try d.validateHandshake(recoveryHello(d)) // completed handshake, pointer still uncommitted
            #expect(try await restarted.snapshot().sessions[d.sessionID]?.status == .prepared)
            try await restarted.activate(d.sessionID, handshake: recoveryHello(d))
            let again = try SyncSessionStore(root: root, credentials: keys) // Kill D
            #expect(try await again.requireActive(d.sessionID).sessionID == d.sessionID)
            let directory = try SyncSessionStore.directory(root: root, sessionID: d.sessionID)
            let store = try OutboxStore(directory: directory)
            let state = await store.snapshot()
            #expect(state.receipts.isEmpty && state.deletedWorkouts.isEmpty && state.tombstones.isEmpty && state.localAnchors.isEmpty && state.tasks == nil)
            #expect(try await store.discoveryFlow(query).anchor == nil)
            for (path, bytes) in previous { #expect(try Data(contentsOf: path) == bytes) }
            // The same currently visible workout is rediscovered despite the old ACK/GC.
            let archive = try await store.savePayload(Data("synthetic-visible-workout".utf8))
            let event = DiscoveryEvent(type: query.type, uuid: workout, deleted: false, archiveFile: archive, archiveHash: Wire.hash(Data("synthetic-visible-workout".utf8)))
            let page = try await store.stageDiscovery(query, previousAnchor: nil, anchor: Data([UInt8(index+1)]), events: [event], hasMore: false)
            let reopened = try OutboxStore(directory: directory) // Kill E
            #expect(try await reopened.discoveryFlow(query).anchor == nil)
            try await reopened.promoteDiscovery(query, batchID: page)
            #expect(await reopened.discoverySnapshot().work[workout]?.generation == 1)
            try await reopened.beginTask(workout)
            let payload = Data("synthetic-new-session-bundle-\(index)".utf8)
            try await reopened.markIngestLane(workout)
            let body = WorkoutPayloadTransport.multipart(bundle: payload, boundary: "synthetic")
            let staged = try await reopened.stageWorkoutIngestPayload(workout, body: body)
            let pending = try OutboxStore(directory: directory) // Kill F
            #expect(await pending.task(workout)?.bundleFile == staged.file)
            #expect(try Data(contentsOf: pending.directory.appendingPathComponent(staged.file)) == body)
            try await pending.acknowledgeWorkoutIngest(workout, receipt: .object([
                "workoutID": .string(workout), "ingestStatus": .string("accepted"),
                "acceptedAt": .string("2026-11-09T10:00:00Z")]))
            _ = try await pending.collectGarbage(dryRun: false)
            #expect(!FileManager.default.fileExists(atPath: directory.appendingPathComponent(staged.file).path))
            var dirty = await pending.snapshot(); dirty.receipts["synthetic"] = .string("ack"); dirty.deletedWorkouts = [workout]; dirty.tombstones = [workout]; dirty.localAnchors["synthetic"] = Data([99]); try await pending.commit(dirty)
            let path = directory.appendingPathComponent("state.json"); previous.append((path, try Data(contentsOf: path)))
            #expect(try keys.load(sessionID: d.sessionID) == "synthetic-\(index)")
        }
        #expect(Set(descriptors.map(\.sessionID)).count == 3)
        #expect(try await sessions.snapshot().sessions.values.filter { $0.status == .retired }.count == 2)
        // A late archived callback changes only A's namespace; C is byte-for-byte stable.
        let currentPath = previous[2].0, currentBytes = try Data(contentsOf: currentPath)
        let old = try #require(try await sessions.resolveBackground(descriptors[0].backgroundIdentifier))
        let oldDir = try SyncSessionStore.directory(root: root, sessionID: old.sessionID)
        try OutboxStore.write(Data("late callback".utf8), to: oldDir.appendingPathComponent("old.transfer.json"))
        #expect(try Data(contentsOf: currentPath) == currentBytes)
    }
    @Test func RecoverySessionRequiresEveryHandshakeFieldAndActivationIsAtomic() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(Wire.id())
        defer { try? FileManager.default.removeItem(at: root) }
        let keys = SyntheticSessionCredentials(), a = recoveryDescriptor(), b = recoveryDescriptor()
        let sessions = try SyncSessionStore(root: root, credentials: keys)
        _ = try await sessions.prepare(a, token: "A"); try await sessions.activate(a.sessionID, handshake: recoveryHello(a))
        _ = try await sessions.prepare(b, token: "B", recovery: true)
        for field in ["protocolVersion", "datasetID", "datasetEpoch", "deviceID", "collectorEpoch", "serviceID", "datasetStatus"] {
            guard case .object(var fields) = recoveryHello(b) else { throw SyncFailure.invalid("fixture") }; fields[field] = .null
            await #expect(throws: (any Error).self) { try await sessions.activate(b.sessionID, handshake: .object(fields)) }
            #expect(try await sessions.snapshot().sessions[b.sessionID]?.status == .prepared)
        }
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: root.path)
        await #expect(throws: (any Error).self) { try await sessions.activate(b.sessionID, handshake: recoveryHello(b)) }
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: root.path)
        let m = try SyncSessionStore.read(root: root)
        #expect(m.activeSessionID == nil && m.sessions[a.sessionID]?.status == .recoveryRequired && m.sessions[b.sessionID]?.status == .prepared)
        try await sessions.activate(b.sessionID, handshake: recoveryHello(b))
        #expect(try SyncSessionStore.read(root: root).activeSessionID == b.sessionID)
    }
}
