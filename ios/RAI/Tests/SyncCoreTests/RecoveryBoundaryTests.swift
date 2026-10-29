import Foundation
import Testing
@testable import SyncCore

// Evidence for why a reset-anchor recovery implementation cannot be built on
// the existing journal contract alone. These are synthetic boundary tests,
// not claims that HealthKit disaster resync has been implemented.
@Suite(.serialized) struct RecoveryBoundaryTests {
    @Test func deliveredPayloadMayBeUnavailableAfterServerRollback() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-recovery-boundary-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let scope = Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id())
        try await store.bind(scope)
        let file = try await store.savePayload(Data("synthetic immutable page".utf8))
        let page = PendingPage(anchorKey: "synthetic", candidateAnchor: Data([100]), operations: [
            .request(.init(method: "POST", path: "/v2/test", file: file, expected: ["durable": .bool(true)]))
        ])
        try await store.stage(page)
        try await store.acknowledge(page.id, cursor: 0, receipt: .object(["durable": .bool(true)]))
        _ = try await store.collectGarbage(dryRun: false)
        let reopened = try OutboxStore(directory: directory)
        // A restored pre-disaster database advertises the same Scope today.
        try await reopened.bind(scope)
        let state = await reopened.snapshot()
        #expect(state.localAnchors["synthetic"] == Data([100]))
        #expect(state.pages.isEmpty && state.receipts.isEmpty)
        #expect(!FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
    }

    @Test func pendingAndPartiallyAcknowledgedJournalSurvivesRestart() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("rai-recovery-journal-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let file = try await store.savePayload(Data("synthetic".utf8))
        let operation: OutboxOperation = .request(.init(method: "POST", path: "/v2/test", file: file, expected: ["durable": .bool(true)]))
        let page = PendingPage(anchorKey: "synthetic", candidateAnchor: Data([100]), operations: [operation, operation])
        try await store.stage(page)
        try await store.begin(page.id) // Includes uploaded but no ACK ambiguity.
        let pending = try OutboxStore(directory: directory)
        #expect(await pending.snapshot().pages.first?.cursor == 0)
        #expect(await pending.snapshot().pages.first?.uploading == false)
        try await pending.acknowledge(page.id, cursor: 0, receipt: .object(["durable": .bool(true)]))
        let partial = try OutboxStore(directory: directory)
        #expect(await partial.snapshot().localAnchors["synthetic"] == nil)
        #expect(await partial.snapshot().pages.first?.candidateAnchor == Data([100]))
        #expect(await partial.snapshot().pages.first?.cursor == 1)
        #expect(FileManager.default.fileExists(atPath: directory.appendingPathComponent(file).path))
    }
}
