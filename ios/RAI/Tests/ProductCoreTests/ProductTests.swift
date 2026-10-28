import Foundation
import ProductCore
import SyncCore
import Testing

struct ProductTests {
  @Test func missingAndQualityAreNotZero() {
    var state = ProductSnapshot()
    #expect(state.metric("readiness") == nil)
    state.state = .object([
      "metrics": .array([
        .object([
          "metric": .string("readiness"), "value": .int(0), "quality": .string("partial"),
          "algorithmVersion": .string("v1"),
        ])
      ])
    ])
    #expect(state.metric("readiness") == 0)
    state.state = .object([
      "metrics": .array([
        .object([
          "metric": .string("readiness"), "value": .int(72), "quality": .string("insufficient"),
          "algorithmVersion": .string("v1"),
        ])
      ])
    ])
    #expect(state.metric("readiness") == nil)
    #expect(ProductFormat.distance(nil) == "--")
    #expect(ProductFormat.pace(nil) == "--")
    #expect(ProductFormat.pace(0.3) == "5′00″")
  }
  @Test func effectivePresentationUsesServerFields() {
    let raw: JSON = .object([
      "sessionID": .string("slot"), "type": .string("EASY"), "distanceM": .int(7000),
      "durationS": .int(0), "status": .string("scheduled"), "modifiedByUser": .bool(true),
    ])
    let session = ProductSession(raw)
    #expect(session.runnable && session.modified)
    #expect(session.distance == 7000 && session.duration == 0)
    var snapshot = ProductSnapshot()
    snapshot.plan = .object(["effective": .object(["sessions": .array([raw])])])
    #expect(snapshot.sessions == [session])
  }
  @Test func immutableCommandSurvivesReopenAndLostACK() async throws {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: dir) }
    let store = try ProductJournal(directory: dir)
    try await store.bind("synthetic-dataset")
    let cmd = ProductCommand(
      path: "/v2/training/plans/p/overrides",
      body: .object([
        "commandID": .string(Wire.id()), "overrideID": .string(Wire.id()), "value": .int(7000),
      ]), observedEffectiveID: "v2")
    try await store.stage(cmd)
    let reopened = try ProductJournal(directory: dir)
    #expect(await reopened.pending() == cmd)
    try await reopened.stage(cmd)
    await #expect(throws: SyncFailure.self) {
      try await reopened.stage(
        ProductCommand(path: cmd.path, body: .object(["commandID": .string(Wire.id())])))
    }
    await #expect(throws: SyncFailure.self) { try await reopened.bind("other-dataset") }
    try await reopened.finish(cmd, result: .object(["executionID": .string("synthetic-execution")]))
    let final = try ProductJournal(directory: dir)
    #expect(await final.pending() == nil)
    #expect(await final.executionIDs() == ["synthetic-execution"])
  }
  @Test func rejectedCommandRetainsDraftUntilExplicitReview() async throws {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: dir) }
    let store = try ProductJournal(directory: dir)
    let cmd = ProductCommand(
      path: "/v2/training/plans/p/overrides",
      body: .object([
        "commandID": .string(Wire.id()), "expectedOverrideRevision": .int(2), "value": .int(7000),
      ]), observedEffectiveID: "v2")
    try await store.stage(cmd)
    #expect(await store.pending()?.body["value"] == .int(7000))
    try await store.discardRejected()
    #expect(await store.pending() == nil)
  }
  @Test func commandReceiptCannotAcknowledgeAnotherExecution() throws {
    let eid = Wire.id()
    let sid = Wire.id()
    let version = Wire.id()
    let command = ProductCommand(
      path: "/v2/training/executions",
      body: .object([
        "executionID": .string(eid), "sessionID": .string(sid),
        "effectiveVersionID": .string(version),
      ]))
    let result: JSON = .object([
      "executionID": .string(eid), "sessionID": .string(sid),
      "effectiveVersionID": .string(version), "revision": .int(1), "status": .string("started"),
      "snapshot": .object(["sessionID": .string(sid)]),
    ])
    try command.validate(result)
    var wrong = result.fields
    wrong["sessionID"] = .string(Wire.id())
    #expect(throws: SyncFailure.receiptMismatch) { try command.validate(.object(wrong)) }
    wrong = result.fields
    wrong["revision"] = .int(2)
    #expect(throws: SyncFailure.receiptMismatch) { try command.validate(.object(wrong)) }
  }
  @Test func regeneratedEffectiveProjectionKeepsServerValues() {
    let monday: JSON = .object([
      "sessionID": .string("mon"), "type": .string("EASY"), "date": .string("2026-10-26"),
      "distanceM": .int(7000), "modifiedByUser": .bool(true),
    ])
    let saturday: JSON = .object([
      "sessionID": .string("sun"), "type": .string("LONG"), "date": .string("2026-10-31"),
      "distanceM": .int(10000), "modifiedByUser": .bool(true),
    ])
    var value = ProductSnapshot()
    value.plan = .object(["effective": .object(["sessions": .array([monday, saturday])])])
    #expect(value.sessions[0].distance == 7000)
    #expect(value.sessions[1].date == "2026-10-31" && value.sessions[1].distance == 10000)
  }

  @Test func malformedTransportResponseKeepsUnknownCommit() {
    #expect(
      !ProductWriteFailure.isDefinitiveRejection(UploadError(code: "invalid_response", status: 422))
    )
    #expect(
      !ProductWriteFailure.isDefinitiveRejection(
        UploadError(code: "oversized_response", status: 422)))
    #expect(
      ProductWriteFailure.isDefinitiveRejection(
        UploadError(code: "invalid_training_override", status: 422)))
  }

  @Test func successfulGenerationDoesNotShowAnErrorSentinel() {
    var data = ProductSnapshot()
    data.plan = .object([
      "generationState": .string("succeeded"), "lastErrorCode": .string("SUCCEEDED"),
    ])
    #expect(data.generationFailure == nil)
    data.plan = .object([
      "generationState": .string("queued"), "lastErrorCode": .string("old_failure"),
    ])
    #expect(data.generationFailure == nil)
    data.plan = .object([
      "generationState": .string("failed"), "lastErrorCode": .string("insufficient_context"),
    ])
    #expect(data.generationFailure == "insufficient_context")
  }

}
