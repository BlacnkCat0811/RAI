import Foundation
import Testing
@testable import SyncCore

private func gateDirectory(_ tag: String) throws -> URL {
    let path = FileManager.default.temporaryDirectory.appendingPathComponent("rai-gate-\(tag)-" + Wire.id())
    try FileManager.default.createDirectory(at: path, withIntermediateDirectories: true)
    return path
}

/// Minimal Sendable counters. A `Task` closure is a `sending` parameter, so the
/// observed state lives in an actor instead of a captured `Mutex` borrow.
private actor Counter {
    private(set) var value = 0
    func bump() { value += 1 }
    func current() -> Int { value }
}

private actor Flag {
    private(set) var raised = false
    func raise() { raised = true }
    func isRaised() -> Bool { raised }
}

/// Polls a condition without sleeping, so the assertions observe state rather
/// than guess a duration. A short bounded spin is enough: the gate mutates its
/// counters synchronously inside the actor.
private func eventually(_ attempts: Int = 5_000, _ condition: () async -> Bool) async -> Bool {
    for _ in 0..<attempts {
        if await condition() { return true }
        await Task.yield()
    }
    return await condition()
}

/// R3 admission control: the capture budget is a stream count plus a byte
/// budget, cancellation releases both, and one writer serializes the store.
@Suite struct CaptureGateTests {
    @Test func policyDefaultsMatchTheChosenConcurrency() {
        #expect(CaptureConcurrencyPolicy.standard.streams == 2)
        #expect(CaptureConcurrencyPolicy.standard.queueBytes == 2 << 20)
        // A policy never admits zero streams or zero bytes: both would deadlock.
        #expect(CaptureConcurrencyPolicy(streams: 0, queueBytes: 0).streams == 1)
        #expect(CaptureConcurrencyPolicy(streams: 0, queueBytes: 0).queueBytes == 1)
    }

    @Test func streamSlotsAreBoundedAndHandedOver() async throws {
        let gate = CaptureGate(policy: CaptureConcurrencyPolicy(streams: 2))
        try await gate.enter()
        try await gate.enter()
        #expect(await gate.snapshot().active == 2)
        #expect(await gate.snapshot().maxActive == 2)

        // The third producer waits instead of running: it never returns while
        // both slots are held.
        let admitted = Flag()
        let waiter = Task {
            try await gate.enter()
            await admitted.raise()
        }
        #expect(await eventually { await gate.snapshot().waitingStreams == 1 })
        #expect(await admitted.isRaised() == false)

        await gate.exit()
        #expect(await eventually { await admitted.isRaised() })
        #expect(await gate.snapshot().active == 2)
        #expect(await gate.snapshot().maxActive == 2)

        await gate.exit()
        await gate.exit()
        #expect(await gate.snapshot().active == 0)
        _ = try? await waiter.value
    }

    @Test func byteBudgetBackpressuresTheProducer() async throws {
        let gate = CaptureGate(policy: CaptureConcurrencyPolicy(streams: 4, queueBytes: 100))
        try await gate.reserve(bytes: 80)
        #expect(await gate.snapshot().reservedBytes == 80)

        let reserved = Flag()
        let waiter = Task {
            try await gate.reserve(bytes: 80)
            await reserved.raise()
        }
        #expect(await eventually { await gate.snapshot().waitingBytes == 1 })
        #expect(await reserved.isRaised() == false)

        await gate.release(bytes: 80)
        #expect(await eventually { await reserved.isRaised() })
        #expect(await gate.snapshot().reservedBytes == 80)
        #expect(await gate.snapshot().peakReservedBytes == 80)
        await gate.release(bytes: 80)
        #expect(await gate.snapshot().reservedBytes == 0)
        _ = try? await waiter.value
    }

    @Test func oversizedReservationIsClampedToTheBudget() async throws {
        // A single batch larger than the whole budget must still make progress;
        // it takes the whole budget instead of waiting forever for space.
        let gate = CaptureGate(policy: CaptureConcurrencyPolicy(streams: 1, queueBytes: 1_000))
        try await gate.reserve(bytes: 50_000)
        #expect(await gate.snapshot().reservedBytes == 1_000)
        await gate.release(bytes: 50_000)
        #expect(await gate.snapshot().reservedBytes == 0)
    }

    @Test func cancellationReleasesEveryWaiterAndTheSlots() async throws {
        let gate = CaptureGate(policy: CaptureConcurrencyPolicy(streams: 1))
        try await gate.enter()

        let failures = Counter()
        let waiters = (0..<3).map { _ in
            Task {
                do {
                    try await gate.enter()
                    await gate.exit()
                } catch is CancellationError {
                    await failures.bump()
                } catch {
                    Issue.record("unexpected error \(error)")
                }
            }
        }
        #expect(await eventually { await gate.snapshot().waitingStreams == 3 })
        await gate.cancel()
        #expect(await eventually { await failures.current() == 3 })
        #expect(await gate.snapshot().waitingStreams == 0)

        // The holder still gives its slot back, and the gate stays closed.
        await gate.exit()
        #expect(await gate.snapshot().active == 0)
        await #expect(throws: (any Error).self) { try await gate.enter() }
        await #expect(throws: (any Error).self) { try await gate.reserve(bytes: 1) }
        for waiter in waiters { _ = await waiter.value }
    }

    @Test func theWriterIsTheOnlyCallerAndSerializesSegments() async throws {
        let directory = try gateDirectory("writer"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory), parent = Wire.id()
        let gate = CaptureGate(policy: CaptureConcurrencyPolicy(streams: 4))
        let writer = CaptureWriter(store: store, gate: gate)

        // Four producers append at once. Every record must land exactly once and
        // every sealed segment must be readable, which is what a shared writer
        // guarantees and a racy one does not.
        let batches = 4, perBatch = 5
        try await withThrowingTaskGroup(of: CaptureSegmentResult.self) { group in
            for batch in 0..<batches {
                group.addTask {
                    let records = (0..<perBatch).map { index in
                        let payload = Data("gate-\(batch)-\(index)".utf8)
                        return CapturedFact(type: "heartRate", uuid: Wire.id(), archive: payload,
                                            identityHash: Wire.hash(payload), encoding: .archive)
                    }
                    return try await writer.append(type: "heartRate", workoutID: parent, records: records)
                }
            }
            var files = Set<String>()
            for try await result in group {
                #expect(result.recordCount == perBatch)
                if let file = result.file { files.insert(file) }
            }
            #expect(files.count == batches)
        }
        let totals = await writer.totals()
        #expect(totals.batches == batches)
        #expect(totals.records == batches * perBatch)
        let snapshot = await store.discoverySnapshot()
        #expect(snapshot.events.count == batches * perBatch)
        #expect(snapshot.work[parent]?.generation == Int64(batches * perBatch))
        // The byte budget is back to zero once every batch has been written.
        #expect(await gate.snapshot().reservedBytes == 0)
        #expect(await gate.snapshot().peakReservedBytes > 0)
    }

    @Test func anEmptyBatchWritesNothing() async throws {
        let directory = try gateDirectory("empty"); defer { try? FileManager.default.removeItem(at: directory) }
        let store = try OutboxStore(directory: directory)
        let gate = CaptureGate(policy: .standard)
        let writer = CaptureWriter(store: store, gate: gate)
        let result = try await writer.append(type: "heartRate", workoutID: Wire.id(), records: [])
        #expect(result.isEmpty)
        #expect(await writer.totals().batches == 0)
        #expect(await gate.snapshot().peakReservedBytes == 0)
    }
}
