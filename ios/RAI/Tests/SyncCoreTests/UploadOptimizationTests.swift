import Foundation
import Synchronization
import Testing
@testable import SyncCore

private actor NegotiationLoader {
    var calls = 0
    var failure = false
    func load() async throws -> JSON {
        calls += 1
        try await Task.sleep(for: .milliseconds(20))
        if failure { throw URLError(.timedOut) }
        return .object(["revision": .int(Int64(calls))])
    }
    func fail(_ value: Bool) { failure = value }
}

struct HandshakeCacheTests {
    @Test func concurrentLanesShareNegotiationExpiryAndInvalidation() async throws {
        let time = Mutex(Date(timeIntervalSince1970: 1000))
        let cache = HandshakeCache(lifetime: 60, now: { time.withLock { $0 } })
        let loader = NegotiationLoader()
        let values = try await withThrowingTaskGroup(of: JSON.self) { group in
            for _ in 0..<10 { group.addTask { try await cache.value(for: "a") { try await loader.load() } } }
            var results: [JSON] = []
            for try await value in group { results.append(value) }
            return results
        }
        #expect(values.count == 10)
        #expect(values.allSatisfy { $0["revision"] == .int(1) })
        #expect(await loader.calls == 1)
        _ = try await cache.value(for: "a") { try await loader.load() }
        #expect(await loader.calls == 1)
        time.withLock { $0 = $0.addingTimeInterval(61) }
        _ = try await cache.value(for: "a") { try await loader.load() }
        #expect(await loader.calls == 2)
        await cache.invalidate("a")
        _ = try await cache.value(for: "a") { try await loader.load() }
        #expect(await loader.calls == 3)
    }
    @Test func failuresAndCredentialsCannotReuseAnEntry() async throws {
        let cache = HandshakeCache(), loader = NegotiationLoader()
        let url = URL(string: "https://synthetic.invalid")!
        let a = HandshakeCache.key(baseURL: url, token: "a", collectorEpoch: "c", sessionID: "s")
        let changed = [HandshakeCache.key(baseURL: url, token: "b", collectorEpoch: "c", sessionID: "s"),
            HandshakeCache.key(baseURL: url, token: "a", collectorEpoch: "d", sessionID: "s"),
            HandshakeCache.key(baseURL: url, token: "a", collectorEpoch: "c", sessionID: "t"),
            HandshakeCache.key(baseURL: URL(string: "https://other.invalid")!, token: "a", collectorEpoch: "c", sessionID: "s")]
        #expect(changed.allSatisfy { $0 != a })
        await loader.fail(true)
        await #expect(throws: URLError.self) { _ = try await cache.value(for: a) { try await loader.load() } }
        await loader.fail(false)
        _ = try await cache.value(for: a) { try await loader.load() }
        _ = try await cache.value(for: changed[0]) { try await loader.load() }
        #expect(await loader.calls == 3)
    }
}

private actor JournalUploader: FileUploader {
    var identities: [String] = []
    var bodies: [Data] = []
    var contentTypes: [String?] = []
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let result = try await response(request, file: file, identity: identity)
        return (result.data, result.status, result.retryAfter)
    }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        identities.append(identity); bodies.append(try Data(contentsOf: file))
        contentTypes.append(request.value(forHTTPHeaderField: "Content-Type"))
        if identities.count == 1 { return UploadResponse(data: Data(), status: 503, contentType: "application/json") }
        let receipt: JSON = .object(["workoutID": .string(request.url!.lastPathComponent),
            "ingestStatus": .string("accepted"), "acceptedAt": .string("2026-11-09T10:00:00Z")])
        return UploadResponse(data: try receipt.data(), status: 200, contentType: "application/json")
    }
}

struct WorkoutUploadJournalTests {
    private func scope() -> Scope { Scope(serviceID: Wire.id(), datasetID: Wire.id(), datasetEpoch: Wire.id(), collectorEpoch: Wire.id()) }
    @Test func restartRetriesExactFileAndPersistsReceiptBeforeReclaiming() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-upload-journal-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: dir) }
        _ = try OutboxStore(directory: dir)
        let id = Wire.id(), scope = scope()
        let raw = try JSON.object(["workoutID": .string(id), "unknown": .string("synthetic retained bytes")]).data()
        let original = try await WorkoutUploadJournal(directory: dir).prepare(raw, workoutID: id, scope: scope)
        let journal = WorkoutUploadJournal(directory: dir)
        let prepared = try #require(await journal.pending(id, scope: scope))
        #expect(prepared == original)
        #expect(prepared.boundary.utf8.count <= 70)
        let uploader = JournalUploader()
        let api = RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", uploader: uploader)
        let receipt = try await WorkoutIngestDelivery(api: api).acceptWithRetry(prepared, directory: dir,
            pacer: UploadPacer(perMinute: 100000, burst: 100), sleep: { _ in })
        #expect(await uploader.identities == [prepared.identity, prepared.identity])
        let bodies = await uploader.bodies
        #expect(bodies.count == 2 && bodies[0] == bodies[1])
        #expect(Wire.hash(bodies[0]) == prepared.hash)
        #expect(await uploader.contentTypes == Array(repeating: "multipart/form-data; boundary=" + prepared.boundary, count: 2))
        try await journal.complete(prepared, receipt: receipt.raw)
        #expect(!FileManager.default.fileExists(atPath: dir.appendingPathComponent(prepared.file).path))
        // Simulate a death after the receipt commit but before file unlink.
        try bodies[0].write(to: dir.appendingPathComponent(prepared.file))
        let reopened = WorkoutUploadJournal(directory: dir)
        #expect(try await reopened.pending(id, scope: scope) == nil)
        let stored = try await reopened.all(scope: scope)
        #expect(stored.first?.receipt == receipt.raw)
        #expect(!FileManager.default.fileExists(atPath: dir.appendingPathComponent(prepared.file).path))
    }
    @Test func corruptedPayloadWrongScopeAndWrongReceiptCannotAdvanceJournal() async throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("rai-upload-corrupt-" + Wire.id())
        defer { try? FileManager.default.removeItem(at: dir) }
        _ = try OutboxStore(directory: dir)
        let journal = WorkoutUploadJournal(directory: dir), scope = scope(), id = Wire.id()
        let prepared = try await journal.prepare(Data("synthetic".utf8), workoutID: id, scope: scope)
        let other = self.scope()
        await #expect(throws: (any Error).self) { _ = try await journal.pending(id, scope: other) }
        await #expect(throws: SyncFailure.receiptMismatch) {
            try await journal.complete(prepared, receipt: .object(["workoutID": .string(Wire.id())]))
        }
        #expect(try await journal.pending(id, scope: scope) != nil)
        try Data("corrupt".utf8).write(to: dir.appendingPathComponent(prepared.file))
        await #expect(throws: (any Error).self) { _ = try await journal.pending(id, scope: scope) }
    }
}

private actor BlockedNegotiation {
    private var reply: CheckedContinuation<JSON, Never>?
    private var observers: [CheckedContinuation<Void, Never>] = []
    private(set) var calls = 0
    func load() async -> JSON {
        calls += 1
        return await withCheckedContinuation { continuation in
            reply = continuation
            for observer in observers { observer.resume() }
            observers.removeAll()
        }
    }
    func started() async {
        if reply != nil { return }
        await withCheckedContinuation { observers.append($0) }
    }
    func release() { reply?.resume(returning: .int(1)); reply = nil }
}

extension HandshakeCacheTests {
    @Test func cancellingOneLaneDoesNotWaitForOrCancelSharedHandshake() async throws {
        let cache = HandshakeCache(), loader = BlockedNegotiation()
        let cancelled = Task { try await cache.value(for: "session") { await loader.load() } }
        await loader.started()
        let survivor = Task { try await cache.value(for: "session") { await loader.load() } }
        cancelled.cancel()
        // The network is still blocked: cancellation must finish immediately.
        await #expect(throws: CancellationError.self) { _ = try await cancelled.value }
        await loader.release()
        #expect(try await survivor.value == .int(1))
        #expect(await loader.calls == 1)
    }
}
