import Foundation
import Synchronization
import UIKit
import SyncCore

// Background tasks are identified by durable outbox page/cursor, not memory
// object identity. Results survive a process restart before local ACK handling.
final class BackgroundTransfers: NSObject, URLSessionDataDelegate, FileUploader, Sendable {
    static let identifier = "com.rai.v2.sync.uploads.v1"
    private struct Result: Codable, Sendable { let body: Data; let status: Int; let retryAfter: Double?; let error: Int?; let contentType: String? }
    private struct State {
        var session: URLSession?
        var buffers: [Int: Data] = [:]
        var waiters: [String: CheckedContinuation<UploadResponse, any Error>] = [:]
        var completion: (@MainActor @Sendable () -> Void)?
        var finishedEvents = false
    }
    private let state = Mutex(State())
    private let directory: URL
    private let sessionID: String?
    init(directory: URL, identifier: String = BackgroundTransfers.identifier, sessionID: String? = nil) {
        self.directory = directory; self.sessionID = sessionID
        super.init()
        let config = URLSessionConfiguration.background(withIdentifier: identifier)
        config.sessionSendsLaunchEvents = true; config.isDiscretionary = false
        config.timeoutIntervalForRequest = 270; config.timeoutIntervalForResource = 3600; config.httpMaximumConnectionsPerHost = 3
        let queue = OperationQueue(); queue.maxConcurrentOperationCount = 1
        let session = URLSession(configuration: config, delegate: self, delegateQueue: queue)
        state.withLock { $0.session = session }
    }
    private func resultURL(_ identity: String) -> URL { directory.appendingPathComponent(Wire.hash(Data(identity.utf8)) + ".transfer.json") }
    func cachedResultFilename(_ identity: String) -> String? {
        resultURL(sessionID.map { $0 + ":" + identity } ?? identity).lastPathComponent
    }
    func discardCachedResult(_ identity: String) {
        let scoped = sessionID.map { $0 + ":" + identity } ?? identity
        try? FileManager.default.removeItem(at: resultURL(scoped))
    }
    func cachedResponse(_ identity: String) throws -> UploadResponse? {
        let scoped = sessionID.map { $0 + ":" + identity } ?? identity
        let url = resultURL(scoped)
        guard FileManager.default.fileExists(atPath: url.path) else { return nil }
        let result = try JSONDecoder().decode(Result.self, from: Data(contentsOf: url))
        guard result.status == 200, result.error == nil else { return nil }
        return UploadResponse(data: result.body, status: result.status, retryAfter: result.retryAfter, contentType: result.contentType)
    }
    func upload(_ request: URLRequest, file: URL, identity: String) async throws -> (Data, Int, TimeInterval?) {
        let result = try await response(request, file: file, identity: identity)
        return (result.data, result.status, result.retryAfter)
    }
    func response(_ request: URLRequest, file: URL, identity: String) async throws -> UploadResponse {
        guard file.standardizedFileURL.deletingLastPathComponent() == directory.standardizedFileURL else { throw SyncFailure.localState("upload_file_session_mismatch") }
        let identity = sessionID.map { $0 + ":" + identity } ?? identity
        let url = resultURL(identity)
        if FileManager.default.fileExists(atPath: url.path) {
            let result = try JSONDecoder().decode(Result.self, from: Data(contentsOf: url))
            // Only success is cached. Retryable failures allow a new task using
            // exactly the same file and outbox operation identity.
            if result.status == 200, result.error == nil { return UploadResponse(data: result.body, status: result.status, retryAfter: result.retryAfter, contentType: result.contentType) }
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
                    continuation.resume(returning: UploadResponse(data: result.body, status: result.status, retryAfter: result.retryAfter, contentType: result.contentType)); return
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
    func handleEvents(completion: @escaping @MainActor @Sendable () -> Void) {
        let finished = state.withLock { state in
            if state.finishedEvents { state.finishedEvents = false; return true }
            state.completion = completion; return false
        }
        if finished { Task { @MainActor in completion() } }
    }
    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        state.withLock { state in
            var buffer = state.buffers[dataTask.taskIdentifier] ?? Data()
            if buffer.count + data.count > 1_048_576 { dataTask.cancel() }
            else { buffer += data; state.buffers[dataTask.taskIdentifier] = buffer }
        }
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        guard let identity = task.taskDescription, sessionID == nil || identity.hasPrefix(sessionID! + ":") else { return }
        state.withLock { state in
            let http = task.response as? HTTPURLResponse
            let result = Result(body: state.buffers.removeValue(forKey: task.taskIdentifier) ?? Data(), status: http?.statusCode ?? 0,
                                retryAfter: RAIAPIClient.retryAfter(http?.value(forHTTPHeaderField: "Retry-After")), error: (error as NSError?)?.code, contentType: http?.value(forHTTPHeaderField: "Content-Type"))
            do {
                try OutboxStore.write(JSONEncoder().encode(result), to: resultURL(identity))
                if let waiter = state.waiters.removeValue(forKey: identity) {
                    if let error { waiter.resume(throwing: error) }
                    else { waiter.resume(returning: UploadResponse(data: result.body, status: result.status, retryAfter: result.retryAfter, contentType: result.contentType)) }
                }
            } catch { state.waiters.removeValue(forKey: identity)?.resume(throwing: error) }
        }
    }
    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        let completion = state.withLock { state in
            let completion = state.completion; state.completion = nil
            if completion == nil { state.finishedEvents = true }
            return completion
        }
        if let completion { Task { @MainActor in completion() } }
        // The completion is safe now: responses are on disk. On foreground or
        // relaunch, the pipeline validates them and commits delivered state.
    }
}
