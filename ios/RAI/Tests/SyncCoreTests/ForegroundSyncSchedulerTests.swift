import Foundation
import Testing
@testable import SyncCore

@MainActor private final class SyncGate {
    private var opened = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    func wait() async {
        if opened { return }
        await withCheckedContinuation { waiters.append($0) }
    }
    func open() {
        opened = true
        let saved = waiters; waiters = []
        for waiter in saved { waiter.resume() }
    }
}

@Suite(.timeLimit(.minutes(1))) @MainActor struct ForegroundSyncSchedulerTests {
    @Test func manualSyncWaitsForAutomaticCleanupAndReservesPriority() async {
        let scheduler = ForegroundSyncScheduler()
        let started = SyncGate(), cancelled = SyncGate(), cleanup = SyncGate()
        var events: [String] = []
        #expect(scheduler.startAutomatic {
            events.append("automatic")
            started.open()
            do { try await Task.sleep(for: .seconds(3600)) }
            catch { events.append("cancelled") }
            cancelled.open()
            await cleanup.wait()
            events.append("cleanup")
        })
        await started.wait()
        #expect(scheduler.startManual { events.append("manual") })
        #expect(scheduler.mode == .manual && scheduler.isRunning)
        await cancelled.wait()
        #expect(events == ["automatic", "cancelled"])
        #expect(!scheduler.startAutomatic { events.append("unexpected automatic") })
        #expect(!scheduler.startManual { events.append("unexpected manual") })
        cleanup.open()
        await scheduler.waitForCompletion()
        #expect(events == ["automatic", "cancelled", "cleanup", "manual"])
        #expect(scheduler.mode == .idle && !scheduler.isRunning)
        #expect(scheduler.startAutomatic { events.append("resumed") })
        await scheduler.waitForCompletion()
        #expect(events.last == "resumed")
    }

    @Test func backgroundCancellationDoesNotStartQueuedManualWork() async {
        let scheduler = ForegroundSyncScheduler()
        let started = SyncGate(), cancelled = SyncGate(), cleanup = SyncGate()
        var ranManual = false
        scheduler.startAutomatic {
            started.open()
            do { try await Task.sleep(for: .seconds(3600)) } catch { }
            cancelled.open()
            await cleanup.wait()
        }
        await started.wait()
        scheduler.startManual { ranManual = true }
        await cancelled.wait()
        scheduler.cancel()
        #expect(scheduler.isRunning) // Cleanup still owns the shared store.
        cleanup.open()
        await scheduler.waitForCompletion()
        #expect(!ranManual && scheduler.mode == .idle)
    }
}
