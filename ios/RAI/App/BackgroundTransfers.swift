import Foundation
import Synchronization
import UIKit
import SyncCore

// Background tasks are identified by durable outbox page/cursor, not memory
// object identity. Results survive a process restart before local ACK handling.
final class BackgroundTransfers: NSObject, URLSessionDataDelegate, FileUploader, Sendable {
    static let identifier = "com.rai.v2.sync.uploads.v1"
    private struct Result: Codable, Sendable { let body: Data; let status: Int; let retryAfter: Double?; let error: Int? }
    private struct State {
        var session: URLSession?
        var buffers: [Int: Data] = [:]
        var waiters: [String: CheckedContinuation<(Data, Int, TimeInterval?), any Error>] = [:]
        var completion: (@MainActor @Sendable () -> Void)?
    }
    private let state = Mutex(State())
    private let directory: URL
    init(directory: URL) {
        self.directory = directory
        super.init()
        let config = URLSessionConfiguration.background(withIdentifier: Self.identifier)
        config.sessionSendsLaunchEvents = true; config.isDiscretionary = false
        config.timeoutIntervalForResource = 3600; config.httpMaximumConnectionsPerHost = 1
        let queue = OperationQueue(); queue.maxConcurrentOperationCount = 1
        let session = URLSession(configuration: config, delegate: self, delegateQueue: queue)
        state.withLock { $0.session = session }
    }
    private func resultURL(_ identity: String) -> URL { directory.appendingPathComponent(Wire.hash(Data(identity.utf8)) + ".transfer.json") }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let url = resultURL(identity)
        if FileManager.default.fileExists(atPath: url.path) {
            let result = try JSONDecoder().decode(Result.self, from: Data(contentsOf: url))
            // Only success is cached. Retryable failures allow a new task using
            // exactly the same file and outbox operation identity.
            if result.status == 200, result.error == nil { return (result.body, result.status, result.retryAfter) }
        }
        let session = state.withLock { $0.session! }
        let tasks = await session.allTasks
        return try await withTaskCancellationHandler(operation: {
          try await withCheckedThrowingContinuation { continuation in
            state.withLock { state in
                if Task.isCancelled { continuation.resume(throwing: CancellationError()); return }
                guard state.waiters[identity] == nil else { continuation.resume(throwing: SyncFailure.localState("duplicate_upload_waiter")); return }
                // Recheck under the callback lock: a task may have completed
                // while allTasks was being enumerated.
                if let bytes = try? Data(contentsOf: url), let result = try? JSONDecoder().decode(Result.self, from: bytes), result.status == 200, result.error == nil {
                    continuation.resume(returning: (result.body, result.status, result.retryAfter)); return
                }
                state.waiters[identity] = continuation
                if let existing = tasks.first(where: { $0.taskDescription == identity && $0.state != .completed }) { existing.resume() }
                else { let task = session.uploadTask(with: request, fromFile: file); task.taskDescription = identity; task.resume() }
            }
          }
        }, onCancel: {
            // Stop the in-process wait, leaving the OS-managed file upload
            // alive. A foreground/relaunch pipeline can reconnect to it.
            self.state.withLock { $0.waiters.removeValue(forKey: identity)?.resume(throwing: CancellationError()) }
        })
    }
    func handleEvents(completion: @escaping @MainActor @Sendable () -> Void) { state.withLock { $0.completion = completion } }
    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        state.withLock { state in
            var buffer = state.buffers[dataTask.taskIdentifier] ?? Data()
            if buffer.count + data.count > 1_048_576 { dataTask.cancel() }
            else { buffer += data; state.buffers[dataTask.taskIdentifier] = buffer }
        }
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        guard let identity = task.taskDescription else { return }
        state.withLock { state in
            let http = task.response as? HTTPURLResponse
            let result = Result(body: state.buffers.removeValue(forKey: task.taskIdentifier) ?? Data(), status: http?.statusCode ?? 0,
                                retryAfter: http?.value(forHTTPHeaderField: "Retry-After").flatMap(Double.init), error: (error as NSError?)?.code)
            do {
                try OutboxStore.write(JSONEncoder().encode(result), to: resultURL(identity))
                if let waiter = state.waiters.removeValue(forKey: identity) {
                    if let error { waiter.resume(throwing: error) }
                    else { waiter.resume(returning: (result.body, result.status, result.retryAfter)) }
                }
            } catch { state.waiters.removeValue(forKey: identity)?.resume(throwing: error) }
        }
    }
    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        let completion = state.withLock { let completion = $0.completion; $0.completion = nil; return completion }
        if let completion { Task { @MainActor in completion() } }
        // The completion is safe now: responses are on disk. On foreground or
        // relaunch, the pipeline validates them and commits delivered state.
    }
}
