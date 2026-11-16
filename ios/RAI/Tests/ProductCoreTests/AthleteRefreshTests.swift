import Foundation
import ProductCore
import SyncCore
import Synchronization
import Testing

private final class AthleteRefreshProtocol: URLProtocol, @unchecked Sendable {
  static let response = Mutex((status: 200, body: Data("{}".utf8)))
  override class func canInit(with request: URLRequest) -> Bool { true }
  override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
  override func startLoading() {
    let value = request.url!.path == "/v2/analysis/athlete"
      ? Self.response.withLock { $0 } : (status: 200, body: Data("{}".utf8))
    client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: value.status, httpVersion: nil, headerFields: nil)!, cacheStoragePolicy: .notAllowed)
    client?.urlProtocol(self, didLoad: value.body)
    client?.urlProtocolDidFinishLoading(self)
  }
  override func stopLoading() {}
}

@Suite(.serialized) struct AthleteRefreshTests {
  private func previous() throws -> ProductSnapshot {
    let file = try #require(Bundle.module.resourceURL?.appendingPathComponent("Fixtures/AthleteModel/full.json"))
    var previous = ProductSnapshot()
    previous.athlete = try JSONDecoder().decode(AthleteModelSnapshot.self, from: Data(contentsOf: file))
    return previous
  }

  private func api(status: Int = 200, body: Data) -> ProductAPI {
    AthleteRefreshProtocol.response.withLock { $0 = (status, body) }
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [AthleteRefreshProtocol.self]
    return ProductAPI(client: RAIAPIClient(baseURL: URL(string: "https://synthetic.invalid")!, token: "synthetic", session: URLSession(configuration: config)))
  }

  @Test func malformedSuccessfulResponseKeepsLastGoodAndReportsFailure() async throws {
    let old = try previous()
    let loaded = await api(body: Data("{}".utf8)).load()
    #expect(loaded.failures["athlete"] == "athlete_response_invalid")
    #expect(ProductLoadFailures.needsRetry(loaded.failures))
    let merged = loaded.keepingLastGood(from: old)
    #expect(merged.athlete == old.athlete)
    #expect(merged.athleteRefreshFailed)
    #expect(merged.athleteAbsenceText != "尚未启用")
  }

  @Test func newerServerSchemaKeepsLastGoodAndAsksForAppUpdate() async throws {
    let old = try previous()
    var future = try #require(old.athlete)
    future.schemaVersion = "rai.athlete_model/99"
    let loaded = await api(body: try JSONEncoder().encode(future)).load()
    #expect(loaded.failures["athlete"] == ProductSnapshot.athleteUpdateRequired)
    #expect(!ProductLoadFailures.needsRetry(loaded.failures))
    let merged = loaded.keepingLastGood(from: old)
    #expect(merged.athlete == old.athlete)
    #expect(merged.athleteNeedsAppUpdate)
    #expect(!merged.athleteRefreshFailed)
    var outdated = ProductSnapshot()
    outdated.failures["athlete"] = ProductSnapshot.athleteUpdateRequired
    #expect(outdated.athleteAbsenceText == "有新功能，请更新 App")
  }

  @Test func unknownModelVersionAsksForAppUpdateOnly() throws {
    var envelope = try #require(try previous().athlete).trainingLoad.envelope
    #expect(envelope.modelVersionKnown)
    envelope.modelVersion = "rai.training_load.hrr_integral/v99"
    #expect(!envelope.supportsPresentation)
    #expect(envelope.displayReason?.rawValue == "model_update_required")
    #expect(ProductReasonText.text(for: envelope.displayReason?.rawValue) == "有新功能，请更新 App")
  }

  @Test func explicitNonActivationClearsCachedModel() async throws {
    let old = try previous()
    let loaded = await api(status: 409, body: Data(#"{"code":"not_product_activated"}"#.utf8)).load()
    let merged = loaded.keepingLastGood(from: old)
    #expect(merged.athlete == nil)
    #expect(!merged.athleteRefreshFailed)
    #expect(merged.athleteAbsenceText == "尚未启用")
    #expect(!ProductLoadFailures.needsRetry(merged.failures))
    let cache = try #require(ProductSnapshotCache.encode(merged))
    #expect(ProductSnapshotCache.decode(cache)?.athlete == nil)
  }

  @Test func successfulModelReplacesOldFailureAndHasDistinctReasons() async throws {
    var old = try previous()
    old.failures["athlete"] = "athlete_response_invalid"
    let loaded = await api(body: try JSONEncoder().encode(old.athlete)).load()
    let merged = loaded.keepingLastGood(from: old)
    #expect(merged.athlete == old.athlete)
    #expect(!merged.athleteRefreshFailed)
    #expect(merged.failures["athlete"] == nil)
    #expect(ProductSnapshot().athleteAbsenceText == "正在计算")
    #expect(ProductReasonText.text(for: "model_result_pending") == "正在计算")
    #expect(ProductReasonText.text(for: "model_update_required") == "有新功能，请更新 App")
    #expect(ProductReasonText.text(for: "unsupported") == "服务器正在更新模型，稍后自动恢复")
    #expect(ProductReasonText.text(for: "model_switching") == "服务器正在更新模型，稍后自动恢复")
    #expect(ProductReasonText.text(for: "technical_failure") == "读取失败，下拉重试")
  }
}
