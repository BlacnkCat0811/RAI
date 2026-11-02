import Observation

/// Serializes foreground sync work. User requests cancel automatic work and
/// wait for its cleanup before touching the same Outbox or coordinator.
@MainActor @Observable
public final class ForegroundSyncScheduler {
    public enum Mode { case idle, automatic, manual }
    public private(set) var mode = Mode.idle
    public var isRunning: Bool { mode != .idle }
    @ObservationIgnored private var task: Task<Void, Never>?
    @ObservationIgnored private var generation = 0

    public init() {}

    @discardableResult public func startAutomatic(_ operation: @escaping @MainActor @Sendable () async -> Void) -> Bool {
        guard mode == .idle else { return false }
        start(.automatic, operation: operation)
        return true
    }

    @discardableResult public func startManual(_ operation: @escaping @MainActor @Sendable () async -> Void) -> Bool {
        guard mode != .manual else { return false }
        start(.manual, operation: operation)
        return true
    }

    private func start(_ mode: Mode, operation: @escaping @MainActor @Sendable () async -> Void) {
        let previous = task
        previous?.cancel()
        generation += 1
        let current = generation
        self.mode = mode // Reserve immediately, including while awaiting cleanup.
        task = Task {
            await previous?.value
            if !Task.isCancelled { await operation() }
            if generation == current { self.mode = .idle; task = nil }
        }
    }

    public func cancel() { task?.cancel() }
    public func waitForCompletion() async { await task?.value }
}
