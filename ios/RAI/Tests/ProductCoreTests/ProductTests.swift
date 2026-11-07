import Foundation
import ProductCore
import SyncCore
import Testing

struct ProductTests {
  @Test func pendingIntentIncludesTheEntireSemanticPayload() {
    let baseline = ProcessInfo.processInfo.environment["RAI_MAINTENANCE_BASELINE"] == "1"
    let original = ProductCommand(
      path: "/v2/training/plans/p/overrides",
      body: .object([
        "commandID": .string("original"), "overrideID": .string("override"),
        "operation": .string("edit"), "sessionID": .string("s"), "path": .string("distanceM"),
        "value": .int(5000), "expectedOverrideRevision": .int(0),
      ]))
    #expect(original.hasSameIntent(as: original))
    for (key, value) in [
      ("value", JSON.int(8000)), ("targetOverrideID", .string("other")),
      (
        "session",
        .object(["distanceM": .int(8000), "date": .string("2026-10-30"), "type": .string("LONG")])
      ),
    ] {
      var changed = original.body.fields
      changed[key] = value
      let proposal = ProductCommand(path: original.path, body: .object(changed))
      #expect(original.hasSameIntent(as: proposal) == baseline)
    }
    for (operation, key, old, new) in [
      ("move", "value", JSON.string("2026-10-29"), JSON.string("2026-10-30")),
      ("revoke", "targetOverrideID", .string("X"), .string("Y")),
      ("add", "session", .object(["distanceM": .int(5000)]), .object(["distanceM": .int(8000)])),
    ] {
      var a = original.body.fields
      a["operation"] = .string(operation)
      a[key] = old
      var b = a
      b[key] = new
      #expect(
        ProductCommand(path: original.path, body: .object(a)).hasSameIntent(
          as:
            ProductCommand(path: original.path, body: .object(b))) == baseline)
    }
  }
  @Test func pendingSubmissionRequiresTheExactOriginalIdentity() async throws {
    let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
    defer { try? FileManager.default.removeItem(at: dir) }
    let original = ProductCommand(
      path: "/v2/training/plans/p/overrides",
      body: .object([
        "commandID": .string(Wire.id()), "overrideID": .string(Wire.id()),
        "operation": .string("edit"), "sessionID": .string("s"), "path": .string("distanceM"),
        "value": .int(5000), "expectedOverrideRevision": .int(0),
      ]))
    let journal = try ProductJournal(directory: dir)
    try await journal.stage(original)
    let reopened = try ProductJournal(directory: dir)
    let pending = await reopened.pending()
    #expect(try original.resolvingPending(pending) == original)
    var draft = original.body.fields
    draft["commandID"] = .string(Wire.id())
    draft["overrideID"] = .string(Wire.id())
    draft["value"] = .number(5000)
    let sameIntent = ProductCommand(path: original.path, body: .object(draft))
    #expect(original.hasSameIntent(as: sameIntent))
    #expect(throws: SyncFailure.self) { try sameIntent.resolvingPending(pending) }
    draft["value"] = .int(8000)
    let changed = ProductCommand(path: original.path, body: .object(draft))
    #expect(throws: SyncFailure.self) { try changed.resolvingPending(pending) }
    #expect(await reopened.pending() == original)
    // Confirming the old receipt releases the pending identity. A new draft
    // remains a separate command, even after restart or a definitive rejection.
    try await reopened.finish(original, result: .object([:]))
    #expect(try changed.resolvingPending(await reopened.pending()) == changed)
    #expect(changed.id != original.id && changed.body["value"] == .int(8000))
    try await reopened.stage(changed)
    try await reopened.discardRejected()
    #expect(changed.body["value"] == .int(8000))
  }
  @Test func manualAndCreateDraftChangesAreDistinct() {
    let manual: JSON = .object([
      "distanceM": .int(5000), "type": .string("EASY"), "date": .string("2026-10-29"),
    ])
    let add = ProductCommand(
      path: "/p/overrides", body: .object(["operation": .string("add"), "session": manual]))
    for (key, value) in [
      ("distanceM", JSON.int(8000)), ("type", .string("LONG")), ("date", .string("2026-10-30")),
    ] {
      var session = manual.fields
      session[key] = value
      var body = add.body.fields
      body["session"] = .object(session)
      let changed = ProductCommand(path: add.path, body: .object(body))
      #expect(!add.hasSameIntent(as: changed))
      #expect(throws: SyncFailure.self) { try changed.resolvingPending(add) }
    }
    let create = ProductCommand(
      path: "/v2/training/plans", body: .object(["config": .object(["weeks": .int(8)])]))
    let changed = ProductCommand(
      path: create.path, body: .object(["config": .object(["weeks": .int(12)])]))
    #expect(!create.hasSameIntent(as: changed))
    #expect(throws: SyncFailure.self) { try changed.resolvingPending(create) }
  }
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
    #expect(
      ProductWriteFailure.isDefinitiveRejection(
        UploadError(code: "training_request_too_large", status: 413)))
    #expect(
      ProductWriteFailure.isDefinitiveRejection(
        UploadError(code: "unsupported_media_type", status: 415)))
    #expect(
      ProductWriteFailure.isDefinitiveRejection(
        UploadError(code: "unsupported_content_encoding", status: 415)))
    #expect(
      !ProductWriteFailure.isDefinitiveRejection(
        UploadError(code: "training_response_requires_pagination", status: 413)))
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

@Test func productFormattingAndReasonTextAreDeterministic() {
  #expect(ProductFormat.decimal(72.6, digits: 0) == "73")
  #expect(ProductFormat.decimal(51.46, digits: 1) == "51.5")
  #expect(ProductFormat.decimal(Double.infinity, digits: 1) == "--")
  #expect(ProductReasonText.text(for: "data_stale") == "数据已过期")
  #expect(ProductReasonText.text(for: "sensitivity_unbounded") == "敏感性范围不可定义")
  #expect(ProductReasonText.text(for: nil, state: "collecting") == "积累中")
  #expect(ProductReasonText.independenceNotice.contains("独立来源"))
  #expect(!ProductReasonText.independenceNotice.contains("最大能力"))
  #expect(ProductReasonText.experimentalLabel == "实验")
}

@Suite struct ReadinessLowNoticeTests {
  @Test func lowNoticeRequiresFiniteKnownLowValue() {
    #expect(ProductReasonText.lowReadinessNotice(score: nil) == nil)
    #expect(ProductReasonText.lowReadinessNotice(score: .nan) == nil)
    #expect(ProductReasonText.lowReadinessNotice(score: 50) == nil)
    #expect(ProductReasonText.lowReadinessNotice(score: 40)?.contains("不会自动改写") == true)
  }
}
