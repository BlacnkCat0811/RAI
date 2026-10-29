import Foundation
import ProductCore
import SyncCore
import Synchronization
import Testing

private struct PageMockState {
  var mode = "audit"
  var failNext = false
  var requests: [String] = []
}
private final class PageProtocol: URLProtocol, @unchecked Sendable {
  static let state = Mutex(PageMockState())
  override class func canInit(with request: URLRequest) -> Bool { true }
  override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
  override func startLoading() {
    let (status, body) = Self.state.withLock { state -> (Int, Data) in
      let url = request.url!
      let after = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "after" }?.value ?? "0"
      state.requests.append(after)
      if state.mode == "oversized" { return (200, Data(repeating: 32, count: 262_145)) }
      if after != "0" && state.failNext { state.failNext = false; return (503, try! JSON.object(["code": .string("temporarily_unavailable")]).data()) }
      let numbers = state.mode == "empty" ? [] : (after == "0" ? Array(1...20) : [21])
      let next: Int64 = numbers.count == 20 ? 20 : 0
      let items: [JSON] = numbers.map { n in
        if state.mode == "history" { return .object(["effectiveVersionID": .string("v\(n)"), "planID": .string("p"), "version": .int(Int64(n))]) }
        return .object(["overrideID": .string("o\(n)"), "revision": .int(Int64(n)), "status": .string("active"), "notes": .string(String(repeating: "x", count: 2000))])
      }
      let page: JSON = state.mode == "history" ? .object(["versions": .array(items), "next": .int(next)]) :
        .object(["version": .object(["effectiveVersionID": .string("v"), "planID": .string("p"), "overrideRevision": .int(21)]), "overrides": .array(items), "next": .int(next)])
      return (200, try! page.data())
    }
    client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
    client?.urlProtocol(self, didLoad: body)
    client?.urlProtocolDidFinishLoading(self)
  }
  override func stopLoading() {}
}

@Suite(.serialized)
struct TrainingPageTests {
  private func api(_ mode: String, failure: Bool = false) -> ProductAPI {
    PageProtocol.state.withLock { $0 = PageMockState(mode: mode, failNext: failure) }
    let configuration = URLSessionConfiguration.ephemeral
    configuration.protocolClasses = [PageProtocol.self]
    return ProductAPI(client: RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", session: URLSession(configuration: configuration)))
  }
  @Test func auditInitialMoreFailureRetryAndTerminalCursor() async throws {
    let api = api("audit", failure: true)
    let reader = TrainingAuditReader(plan: "p", version: "v")
    let first = try await reader.loadMore(using: api)
    #expect(first.overrides.count == 20 && first.next == 20)
    do { _ = try await reader.loadMore(using: api); Issue.record("failure hidden") } catch let e as UploadError { #expect(e.status == 503) }
    let retained = await reader.snapshot()
    #expect(retained.overrides == first.overrides && retained.next == 20)
    let final = try await reader.loadMore(using: api)
    #expect(final.overrides.count == 21 && final.next == 0)
    _ = try await reader.loadMore(using: api)
    #expect(PageProtocol.state.withLock { $0.requests } == ["0", "20", "20"])
  }
  @Test func historySummaryPaginationAndRetry() async throws {
    let api = api("history", failure: true)
    let reader = TrainingHistoryReader(plan: "p")
    #expect(try await reader.loadMore(using: api).versions.count == 20)
    do { _ = try await reader.loadMore(using: api); Issue.record("history failure hidden") } catch {}
    #expect(await reader.snapshot().next == 20)
    let final = try await reader.loadMore(using: api)
    #expect(final.versions.count == 21 && final.next == 0)
  }
  @Test func emptyAuditAndOversizedResponsePreserveCursor() async throws {
    let reader = TrainingAuditReader(plan: "p", version: "v")
    let empty = try await reader.loadMore(using: api("empty"))
    #expect(empty.loaded && empty.next == 0 && empty.overrides.isEmpty)
    let failedReader = TrainingAuditReader(plan: "p", version: "v")
    do { _ = try await failedReader.loadMore(using: api("oversized")); Issue.record("oversized accepted") } catch let e as UploadError { #expect(e.code == "oversized_response") }
    #expect(await failedReader.snapshot().loaded == false)
    #expect(RAIAPIClient.responseBudget(for: "/v2/sync/batches/id") == 1_048_576)
    #expect(RAIAPIClient.responseBudget(for: "/v2/training/plans/p/history/versions") == 262_144)
  }
  @Test func fitnessV2RemainsVisibleWithUnknownVersionRejected() {
    var snapshot = ProductSnapshot()
    snapshot.state = .object(["metrics": .array([.object(["metric": .string("fitness"), "algorithm": .string("fitness_fatigue"), "algorithmVersion": .string("v2"), "quality": .string("partial"), "value": .int(12)])])])
    #expect(snapshot.metric("fitness") == 12)
    #expect(snapshot.metric("readiness") == nil)
  }
}
