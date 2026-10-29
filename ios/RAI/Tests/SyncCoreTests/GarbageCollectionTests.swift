import Foundation
import Testing
@testable import SyncCore

private func gcDirectory() throws -> URL {
    let path = FileManager.default.temporaryDirectory.appendingPathComponent("rai-gc-synthetic-" + Wire.id())
    try FileManager.default.createDirectory(at: path, withIntermediateDirectories: true)
    return path
}

@Suite(.serialized) struct GarbageCollectionTests {
    @Test func durableAnchorPrecedesSweepAndPendingFilesSurvive() async throws {
        let directory = try gcDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let file = try await store.savePayload(Data("synthetic".utf8))
        let orphan = try await store.savePayload(Data("orphan".utf8))
        let page = PendingPage(anchorKey: "synthetic", candidateAnchor: Data([8]), operations: [.request(.init(method: "POST", path: "/v2/test", file: file, expected: ["durable": .bool(true)]))])
        try await store.stage(page)
        #expect(try await store.collectGarbage(dryRun: false) == 0)
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
        await #expect(throws: SyncFailure.self) { try await store.acknowledge(page.id, cursor: 0, receipt: .object([:])) }
        #expect(await store.snapshot().localAnchors["synthetic"] == nil)
        try await store.acknowledge(page.id, cursor: 0, receipt: .object(["durable": .bool(true)]))
        let reopened = try OutboxStore(directory: directory)
        #expect(await reopened.snapshot().localAnchors["synthetic"] == Data([8]))
        #expect(await reopened.snapshot().receipts.isEmpty)
        #expect(try await reopened.collectGarbage() == 2) // dry run changes nothing
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
        #expect(try await reopened.collectGarbage(limit: 1, dryRun: false) == 1)
        let again = try OutboxStore(directory: directory)
        _ = try await again.collectGarbage(dryRun: false)
        #expect(try await again.collectGarbage(dryRun: false) == 0)
        #expect(!FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(orphan).path))
        #expect(await again.snapshot().localAnchors["synthetic"] == Data([8]))
    }

    @Test func failedAuthorityCommitAndInterruptedSweepRecover() async throws {
        let directory = try gcDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let file = try await store.savePayload(Data([1]))
        let page = PendingPage(anchorKey: "x", candidateAnchor: Data([2]), operations: [.request(.init(method: "POST", path: "/v2/test", file: file))])
        try await store.stage(page)
        let authority = directory.appendingPathComponent("state.json"), saved = try Data(contentsOf: authority)
        try FileManager.default.removeItem(at: authority)
        try FileManager.default.createDirectory(at: authority, withIntermediateDirectories: false)
        await #expect(throws: (any Error).self) { try await store.acknowledge(page.id, cursor: 0, receipt: .object([:])) }
        #expect(await store.snapshot().localAnchors["x"] == nil)
        #expect(try await store.collectGarbage(dryRun: false) == 0)
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
        try FileManager.default.removeItem(at: authority); try saved.write(to: authority)
        try await store.acknowledge(page.id, cursor: 0, receipt: .object([:]))
        // Inject death-equivalent interruption after unlink and before the
        // queue commit: an absent marked file is safe to re-sweep after reopen.
        try FileManager.default.removeItem(at: directory.appendingPathComponent(file))
        let reopened = try OutboxStore(directory: directory)
        #expect(await reopened.snapshot().localAnchors["x"] == Data([2]))
        _ = try await reopened.collectGarbage(dryRun: false)
        #expect(await reopened.snapshot().reclaimFiles?.isEmpty == true)
    }

    @Test func referencedOrAuthorityFilesCannotBeSwept() async throws {
        let directory = try gcDirectory(); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let file = try await store.savePayload(Data([1]))
        let page = PendingPage(anchorKey: "x", candidateAnchor: Data([2]), operations: [.request(.init(method: "POST", path: "/v2/test", file: file))])
        try await store.stage(page)
        var state = await store.snapshot(); state.reclaimFiles = [file]
        try JSONEncoder().encode(state).write(to: directory.appendingPathComponent("state.json"))
        let reopened = try OutboxStore(directory: directory)
        await #expect(throws: SyncFailure.self) { try await reopened.collectGarbage(dryRun: false) }
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
        state.reclaimFiles = ["state.json"]
        try JSONEncoder().encode(state).write(to: directory.appendingPathComponent("state.json"))
        let authority = try OutboxStore(directory: directory)
        await #expect(throws: SyncFailure.self) { try await authority.collectGarbage(dryRun: false) }
        try FileManager.default.removeItem(at: directory.appendingPathComponent(file))
        #expect(await authority.snapshot().localAnchors["x"] == nil)
        // Missing referenced files never authorize an ACK or advance anchors.
        await #expect(throws: SyncFailure.self) { try await authority.stage(page) }
    }
}
