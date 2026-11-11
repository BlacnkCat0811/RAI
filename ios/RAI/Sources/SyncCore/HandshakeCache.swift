import Foundation

/// Process-local, credential-scoped negotiation. Concurrent lanes share one
/// request; failures never become cache entries. Activation/recovery handshakes
/// deliberately bypass this cache.
public actor HandshakeCache {
    private struct Entry { let value: JSON; let expires: Date }
    private struct Flight {
        let id: UUID
        let task: Task<Void, Never>
        var waiters: [UUID: CheckedContinuation<JSON, any Error>]
    }
    private var entries: [String: Entry] = [:]
    private var flights: [String: Flight] = [:]
    private let lifetime: TimeInterval
    private let now: @Sendable () -> Date

    public init(lifetime: TimeInterval = 60, now: @escaping @Sendable () -> Date = { Date() }) {
        self.lifetime = lifetime; self.now = now
    }
    public static func key(baseURL: URL, token: String, collectorEpoch: String, sessionID: String?) -> String {
        Wire.hash(Data([baseURL.absoluteString, token, collectorEpoch, sessionID ?? ""].joined(separator: "\n").utf8))
    }
    public func invalidate(_ key: String) {
        entries[key] = nil
        if let flight = flights.removeValue(forKey: key) {
            flight.task.cancel()
            for waiter in flight.waiters.values { waiter.resume(throwing: CancellationError()) }
        }
    }
    private func cancelWaiter(_ id: UUID, key: String) {
        flights[key]?.waiters.removeValue(forKey: id)?.resume(throwing: CancellationError())
    }
    private func finish(_ result: Result<JSON, any Error>, key: String, id: UUID) {
        guard flights[key]?.id == id, let flight = flights.removeValue(forKey: key) else { return }
        if case .success(let value) = result {
            entries[key] = Entry(value: value, expires: now().addingTimeInterval(lifetime))
        }
        for waiter in flight.waiters.values { waiter.resume(with: result) }
    }
    public func value(for key: String, load: @escaping @Sendable () async throws -> JSON) async throws -> JSON {
        try Task.checkCancellation()
        if let entry = entries[key], entry.expires > now() { return entry.value }
        let waiterID = UUID()
        let value: JSON = try await withTaskCancellationHandler(operation: {
            try await withCheckedThrowingContinuation { continuation in
                if Task.isCancelled { continuation.resume(throwing: CancellationError()); return }
                if flights[key] == nil {
                    let id = UUID()
                    let task = Task {
                        let result: Result<JSON, any Error>
                        do { result = .success(try await load()) } catch { result = .failure(error) }
                        self.finish(result, key: key, id: id)
                    }
                    flights[key] = Flight(id: id, task: task, waiters: [:])
                }
                flights[key]?.waiters[waiterID] = continuation
            }
        }, onCancel: { Task { await self.cancelWaiter(waiterID, key: key) } })
        try Task.checkCancellation()
        return value
    }
}
