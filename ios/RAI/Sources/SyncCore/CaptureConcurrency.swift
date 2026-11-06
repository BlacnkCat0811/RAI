import Foundation

/// Bounded concurrency for capture producers.
///
/// Independent HealthKit streams (one quantity type, the route stream) may run
/// at the same time, but the total number of producers and the bytes they hand
/// to the single writer are both capped. Paging and anchor updates inside one
/// stream stay serial because a stream is one producer.
public struct CaptureConcurrencyPolicy: Sendable, Equatable {
    /// Producers allowed to run at the same time.
    public var streams: Int
    /// Bytes of captured payload allowed to be in flight between a producer and
    /// the writer. A producer that would exceed it waits before handing over.
    public var queueBytes: Int

    /// Chosen after measuring 1, 2 and 4 streams on this host: two streams hide
    /// the read latency without queueing behind each other.
    public static let standard = CaptureConcurrencyPolicy(streams: 2, queueBytes: 2 << 20)

    public init(streams: Int = 2, queueBytes: Int = 2 << 20) {
        self.streams = max(1, streams)
        self.queueBytes = max(1, queueBytes)
    }
}

/// Admission control for capture: stream slots plus a byte budget.
///
/// `enter`/`reserve` suspend instead of dropping work, so a slow writer slows
/// producers down rather than growing memory. `cancel` releases every waiter
/// with `CancellationError`, which is what makes a cancelled sync give its slots
/// and budget back instead of leaking them.
public actor CaptureGate {
    public nonisolated let policy: CaptureConcurrencyPolicy

    private var activeStreams = 0
    private var maxActiveStreams = 0
    private var reservedBytes = 0
    private var peakReservedBytes = 0
    private var streamWaiters: [CheckedContinuation<Void, Error>] = []
    private var byteWaiters: [(bytes: Int, continuation: CheckedContinuation<Void, Error>)] = []
    private var cancelled = false

    public init(policy: CaptureConcurrencyPolicy = .standard) {
        self.policy = policy
    }

    public struct Snapshot: Sendable, Equatable {
        public var active: Int
        public var maxActive: Int
        public var reservedBytes: Int
        public var peakReservedBytes: Int
        public var waitingStreams: Int
        public var waitingBytes: Int
    }

    public func snapshot() -> Snapshot {
        Snapshot(active: activeStreams, maxActive: maxActiveStreams, reservedBytes: reservedBytes,
                 peakReservedBytes: peakReservedBytes, waitingStreams: streamWaiters.count,
                 waitingBytes: byteWaiters.count)
    }

    /// Takes a stream slot, waiting while the policy's stream count is reached.
    public func enter() async throws {
        if cancelled { throw CancellationError() }
        if activeStreams < policy.streams {
            activeStreams += 1
            maxActiveStreams = max(maxActiveStreams, activeStreams)
            return
        }
        try await withCheckedThrowingContinuation { continuation in
            streamWaiters.append(continuation)
        }
    }

    /// Gives a stream slot back and admits the next waiter.
    public func exit() {
        activeStreams = max(0, activeStreams - 1)
        guard !cancelled, !streamWaiters.isEmpty, activeStreams < policy.streams else { return }
        let next = streamWaiters.removeFirst()
        activeStreams += 1
        maxActiveStreams = max(maxActiveStreams, activeStreams)
        next.resume()
    }

    /// Reserves writer-queue bytes, waiting while the budget is exhausted.
    public func reserve(bytes: Int) async throws {
        if cancelled { throw CancellationError() }
        let need = min(max(0, bytes), policy.queueBytes)
        if need == 0 { return }
        if reservedBytes + need <= policy.queueBytes {
            reservedBytes += need
            peakReservedBytes = max(peakReservedBytes, reservedBytes)
            return
        }
        try await withCheckedThrowingContinuation { continuation in
            byteWaiters.append((bytes: need, continuation: continuation))
        }
    }

    /// Releases reserved bytes and admits as many waiters as now fit.
    public func release(bytes: Int) {
        let freed = min(max(0, bytes), policy.queueBytes)
        reservedBytes = max(0, reservedBytes - freed)
        guard !cancelled else { return }
        while let first = byteWaiters.first, reservedBytes + first.bytes <= policy.queueBytes {
            byteWaiters.removeFirst()
            reservedBytes += first.bytes
            peakReservedBytes = max(peakReservedBytes, reservedBytes)
            first.continuation.resume()
        }
    }

    /// Releases every waiter with `CancellationError`; later calls fail fast.
    public func cancel() {
        guard !cancelled else { return }
        cancelled = true
        let streams = streamWaiters
        streamWaiters = []
        for waiter in streams { waiter.resume(throwing: CancellationError()) }
        let bytes = byteWaiters
        byteWaiters = []
        for waiter in bytes { waiter.continuation.resume(throwing: CancellationError()) }
    }
}

/// The single writer for captured facts.
///
/// Every producer hands its batch here; this actor is the only caller of the
/// store's capture API, so segment files and journal commits are appended one at
/// a time no matter how many producers are running.
public actor CaptureWriter {
    private let store: OutboxStore
    private let gate: CaptureGate
    private var batches = 0
    private var records = 0

    public init(store: OutboxStore, gate: CaptureGate) {
        self.store = store
        self.gate = gate
    }

    public struct Totals: Sendable, Equatable {
        public var batches: Int
        public var records: Int
    }

    public func totals() -> Totals { Totals(batches: batches, records: records) }

    public func append(type: String, workoutID: String?, records batch: [CapturedFact]) async throws -> CaptureSegmentResult {
        guard !batch.isEmpty else {
            return CaptureSegmentResult(file: nil, byteLength: 0, recordCount: 0, sha256: nil, events: [], published: [])
        }
        let bytes = batch.reduce(0) { $0 + $1.payload.count }
        try await gate.reserve(bytes: bytes)
        do {
            let result = try await store.captureFactSegment(type: type, workoutID: workoutID, records: batch)
            await gate.release(bytes: bytes)
            batches += 1
            records += batch.count
            return result
        } catch {
            await gate.release(bytes: bytes)
            throw error
        }
    }
}
