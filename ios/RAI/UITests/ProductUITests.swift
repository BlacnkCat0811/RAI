import XCTest

@MainActor
final class ProductUITests: XCTestCase {
  private let privateRoot = "/tmp/rai-p6-native-acceptance"
  private var app: XCUIApplication!
  private var timings: [String: Double] = [:]
  private func launchApp(expectHomeHero: Bool = true) throws {
    continueAfterFailure = false
    XCTAssertTrue(
      FileManager.default.fileExists(atPath: privateRoot + "/config-app.json"),
      "Start the isolated P6 host and synthetic driver first")
    app = XCUIApplication()
    app.launchEnvironment["RAI_SYNTHETIC_ACCEPTANCE"] = "1"
    app.launchEnvironment["RAI_ACCEPTANCE_CONFIG"] = privateRoot + "/config-app.json"
    app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
    let launchStart = Date()
    app.launch()
    let ready = expectHomeHero ? "home.hero" : "screen.home"
    guard element(ready).waitForExistence(timeout: 60) else {
      throw UIFlowFailure.missing(ready + " after live API bootstrap")
    }
    timings["process_launch_to_live_home_ms"] = Date().timeIntervalSince(launchStart) * 1000
  }
  private func element(_ id: String) -> XCUIElement {
    app.descendants(matching: .any).matching(identifier: id).firstMatch
  }
  private func tap(_ id: String) throws {
    let e = element(id)
    guard e.waitForExistence(timeout: 15) else { throw UIFlowFailure.missing(id) }
    let ready = XCTNSPredicateExpectation(
      predicate: NSPredicate(format: "enabled == true"), object: e)
    guard XCTWaiter.wait(for: [ready], timeout: 20) == .completed else {
      throw UIFlowFailure.disabled(id)
    }
    if !e.isHittable { app.swipeUp() }
    guard e.isHittable else { throw UIFlowFailure.missing(id + ": not hittable") }
    let start = Date()
    e.tap()
    timings["tap_" + id + "_ms"] = Date().timeIntervalSince(start) * 1000
  }
  private func tab(_ title: String) {
    let start = Date()
    app.tabBars.buttons[title].tap()
    timings["tab_" + title + "_ms"] = Date().timeIntervalSince(start) * 1000
  }
  private func scroll(_ screen: String, up: Bool = true) {
    // Use the content margin so large button labels do not own the gesture.
    let view = app.scrollViews.matching(identifier: screen).firstMatch
    let start = view.coordinate(withNormalizedOffset: CGVector(dx: 0.98, dy: up ? 0.80 : 0.20))
    let end = view.coordinate(withNormalizedOffset: CGVector(dx: 0.98, dy: up ? 0.20 : 0.80))
    start.press(forDuration: 0.05, thenDragTo: end)
  }
  private func auditVisibleText() throws {
    do {
      try app.performAccessibilityAudit(for: .textClipped)
    } catch let error as NSError
      where error.domain == "com.apple.xcode.xctest.accessibilityAudit" && error.code == -56
    {
      // A busy simulator can time out before producing any audit result.
      // Retry that infrastructure error once; clipping findings are not ignored.
      try app.performAccessibilityAudit(for: .textClipped)
    }
  }
  private func screen(_ name: String) throws {
    let shot = app.screenshot()
    let attachment = XCTAttachment(screenshot: shot)
    attachment.name = "P6-" + name
    attachment.lifetime = .keepAlways
    add(attachment)
    let directory = URL(fileURLWithPath: privateRoot).appendingPathComponent("screens")
      .appendingPathComponent(
        ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"] ?? "simulator")
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try shot.pngRepresentation.write(to: directory.appendingPathComponent(name + ".png"))
    try JSONSerialization.data(
      withJSONObject: [
        "scope": "simulator UI automation; includes XCTest overhead", "timings": timings,
      ], options: [.sortedKeys]
    ).write(to: directory.appendingPathComponent("ui-timings.json"))
  }
  func test01NavigationVisualBaselineAndDiagnostics() throws {
    try launchApp()
    try screen("Home")
    tab("运动")
    XCTAssertTrue(element("screen.activities").exists)
    try screen("Activities")
    tab("分析")
    XCTAssertTrue(element("screen.analysis").exists)
    try screen("Analysis")
    tab("训练")
    XCTAssertTrue(element("screen.training").exists)
    try screen("Training")
    tab("我的")
    try tap("profile.sync")
    XCTAssertTrue(app.buttons["Request Read Access"].exists || app.buttons["请求读取权限"].exists)
    tab("首页")
    try tap("home.hero")
    XCTAssertTrue(element("session.edit").waitForExistence(timeout: 10))
    try screen("SessionDetail")
  }
  func test02EditConflictMoveSkipAddRevokeExecutionFeedback() async throws {
    try await freshPlan()
    try launchApp()
    try tap("home.hero")
    try tap("session.edit")
    let value = app.textFields["edit.value"]
    XCTAssertTrue(value.waitForExistence(timeout: 10))
    value.tap()
    value.press(forDuration: 1.2)
    if app.menuItems["全选"].exists { app.menuItems["全选"].tap() }
    let old = value.value as? String ?? ""
    value.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: old.count) + "7100")
    try await concurrentOverride()
    try tap("edit.save")
    XCTAssertTrue(element("edit.review").waitForExistence(timeout: 15))
    XCTAssertEqual(value.value as? String, "7100")
    try screen("Conflict")
    try tap("edit.review")
    try tap("edit.save")
    XCTAssertTrue(element("session.edit").waitForExistence(timeout: 20))
    app.swipeUp()
    try tap("session.revoke")
    app.swipeDown()
    try tap("session.move")
    try tap("edit.nextDay")
    try tap("edit.save")
    XCTAssertTrue(element("session.start").waitForExistence(timeout: 15))
    try tap("session.start")
    XCTAssertTrue(element("execution.complete").waitForExistence(timeout: 15))
    try screen("ExecutionStarted")
    try tap("execution.complete")
    XCTAssertTrue(element("execution.associate").waitForExistence(timeout: 15))
    try tap("execution.associate")
    try tap("associate.candidate")
    XCTAssertTrue(element("feedback.card").waitForExistence(timeout: 15))
    try tap("execution.refresh")
    try screen("Feedback")
    try screen("Execution")
    app.navigationBars.buttons.firstMatch.tap()
    app.navigationBars.buttons.firstMatch.tap()
    tab("训练")
    app.swipeUp()
    try tap("training.add")
    let distance = app.textFields["add.distance"]
    XCTAssertTrue(distance.waitForExistence(timeout: 10))
    distance.tap()
    distance.typeText("3200")
    try tap("edit.save")
    XCTAssertTrue(element("screen.training").waitForExistence(timeout: 15))
    tab("首页")
    app.swipeDown()
    try tap("home.hero")
    try screen("BeforeSkip")
    try tap("session.skip")
    try screen("Skip")
    try tap("edit.save")
    XCTAssertTrue(element("screen.session").waitForExistence(timeout: 15))
  }
  func test03LaunchAndHomeScrollPerformance() async throws {
    try await freshPlan()
    try launchApp()
    let originalY = element("home.hero").frame.minY
    scroll("screen.home")
    XCTAssertLessThan(element("home.hero").frame.minY, originalY - 20)
    scroll("screen.home", up: false)
    let options = XCTMeasureOptions()
    options.iterationCount = 3
    measure(metrics: [XCTClockMetric(), XCTMemoryMetric(application: app)], options: options) {
      scroll("screen.home")
      scroll("screen.home", up: false)
    }
  }
  func test04AccessibilityLayout() throws {
    try launchApp()
    tab("我的")
    let flags = element("profile.a11y")
    if !flags.isHittable { app.swipeUp() }
    XCTAssertTrue(flags.waitForExistence(timeout: 10))
    guard flags.label.contains("accessibility") else {
      throw XCTSkip("Run the dedicated matrix with an Accessibility Dynamic Type size")
    }
    XCTAssertTrue(flags.label.contains("motion=true"), flags.label)
    XCTAssertTrue(flags.label.contains("transparency=true"), flags.label)
    XCTAssertTrue(flags.label.contains("accessibility"), flags.label)
    tab("首页")
    try auditVisibleText()
    try screen("HomeAccessibility")
    try tap("home.hero")
    try auditVisibleText()
    try screen("SessionDetailAccessibility")
    scroll("screen.session")
    try auditVisibleText()
    try screen("SessionActionsAccessibility")
    app.navigationBars.buttons.firstMatch.tap()
    scroll("screen.home")
    try auditVisibleText()
    try screen("HomeAccessibilityScrolled")
    let weekly = app.staticTexts["本周概览"].firstMatch
    for _ in 0..<8 {
      if weekly.isHittable { break }
      scroll("screen.home")
    }
    XCTAssertTrue(
      weekly.isHittable, "Weekly heading must be visible, not only present in the hierarchy")
    try auditVisibleText()
    try screen("WeeklyAccessibility")
    scroll("screen.home")
    try auditVisibleText()
    try screen("WeeklyLoadAccessibility")
    tab("训练")
    try auditVisibleText()
    try screen("TrainingAccessibility")
    tab("运动")
    try auditVisibleText()
    try screen("ActivitiesAccessibility")
    tab("分析")
    try auditVisibleText()
    try screen("AnalysisAccessibility")
    scroll("screen.analysis")
    try auditVisibleText()
    try screen("AnalysisTrendAccessibility")
    tab("训练")
    for _ in 0..<6 {
      if element("training.execution").isHittable { break }
      scroll("screen.training")
    }
    try tap("training.execution")
    try auditVisibleText()
    try screen("ExecutionAccessibility")
    scroll("screen.execution")
    try auditVisibleText()
    try screen("FeedbackAccessibility")
  }
  func test05VisualRegressionCandidate() throws {
    try test01NavigationVisualBaselineAndDiagnostics()
    app.navigationBars.buttons.firstMatch.tap()
    tab("训练")
    app.swipeUp()
    try tap("training.execution")
    XCTAssertTrue(element("feedback.card").waitForExistence(timeout: 15))
    try screen("Execution")
  }
  func test06AuditPagination() async throws {
    try await freshPlan()
    var plan = try await call("/v2/training/plans/current")
    let overview = try await call("/v2/product/overview")
    let effective = plan["effective"] as! [String: Any]
    let sessions = effective["sessions"] as! [[String: Any]]
    let session = sessions.first { $0["date"] as? String == overview["today"] as? String }!
    for _ in 0..<21 {
      let result = try await call(
        "/v2/training/plans/" + (plan["planID"] as! String) + "/overrides",
        body: [
          "commandID": UUID().uuidString.lowercased(), "overrideID": UUID().uuidString.lowercased(),
          "sessionID": session["sessionID"]!, "expectedOverrideRevision": plan["overrideRevision"]!,
          "operation": "edit", "path": "notes", "value": "synthetic audit pagination",
        ])
      plan["overrideRevision"] = result["overrideRevision"]
    }
    try launchApp()
    try tap("home.hero")
    let more = element("session.auditMore")
    if !more.isHittable { app.swipeUp() }
    XCTAssertTrue(more.waitForExistence(timeout: 20))
    try tap("session.auditMore")
    let finished = XCTNSPredicateExpectation(
      predicate: NSPredicate(format: "exists == false"), object: more)
    XCTAssertEqual(XCTWaiter.wait(for: [finished], timeout: 20), .completed)
    XCTAssertTrue(element("session.revoke").exists)
    try screen("AuditPagination")
  }
  func test07PendingSessionLocksDraftAndRetriesOriginalAfterRestart() async throws {
    try await freshPlan(includingToday: true)
    let plan = try await call("/v2/training/plans/current")
    let overview = try await call("/v2/product/overview")
    let effective = plan["effective"] as! [String: Any]
    let sessions = effective["sessions"] as! [[String: Any]]
    let session = sessions.first { $0["date"] as? String == overview["today"] as? String }!
    let planID = plan["planID"] as! String
    // The loopback fault proxy forwards to the actual server, then loses its
    // successful ACK. The journal must keep the immutable original request.
    _ = try await call(
      "/__maintenance/drop-next", body: ["path": "/v2/training/plans/" + planID + "/overrides"])
    try launchApp()
    try tap("home.hero")
    try tap("session.edit")
    var value = app.textFields["edit.value"]
    replace(value, with: "5000")
    try tap("edit.save")
    XCTAssertTrue(element("pending.message").waitForExistence(timeout: 20))
    XCTAssertFalse(value.isEnabled)
    XCTAssertFalse(element("edit.save").isEnabled)
    XCTAssertEqual(value.value as? String, "5000")
    app.terminate()
    try launchApp()
    try tap("home.hero")
    try tap("session.edit")
    value = app.textFields["edit.value"]
    XCTAssertTrue(element("pending.retry").waitForExistence(timeout: 20))
    XCTAssertFalse(value.isEnabled)
    XCTAssertEqual(Double(value.value as? String ?? ""), 5000)
    try tap("pending.retry")
    XCTAssertTrue(element("session.edit").waitForExistence(timeout: 30))
    let confirmed = try await call("/v2/training/plans/current")
    XCTAssertEqual(confirmed["overrideRevision"] as? Int, 1)
    try tap("session.edit")
    value = app.textFields["edit.value"]
    replace(value, with: "8000")
    try tap("edit.save")
    XCTAssertTrue(element("session.edit").waitForExistence(timeout: 20))
    let current = try await call("/v2/training/plans/current")
    let updated = (current["effective"] as! [String: Any])["sessions"] as! [[String: Any]]
    XCTAssertEqual(
      updated.first { $0["sessionID"] as? String == session["sessionID"] as? String }?["distanceM"]
        as? Double, 8000)
    XCTAssertEqual(
      current["overrideRevision"] as? Int, 2,
      "Retry must not create another override; changed draft must create a new command")
  }
  func test08CreatePlanPendingCannotSubmitAnotherDraft() async throws {
    // Run first on the fresh synthetic dataset, before the native seed. The
    // acceptance runner supplies its private configuration.
    _ = try await call("/__maintenance/drop-next", body: ["path": "/v2/training/plans"])
    try launchApp(expectHomeHero: false)
    tab("训练")
    try tap("training.create")
    let target = app.textFields["create.target"]
    replace(target, with: "21")
    let baseline = app.textFields["当前每周跑量（km）"]
    replace(baseline, with: "30")
    let longest = app.textFields["最近最长距离（km）"]
    replace(longest, with: "12")
    XCTAssertEqual(target.value as? String, "21")
    XCTAssertEqual(baseline.value as? String, "30")
    XCTAssertEqual(longest.value as? String, "12")
    try screen("MaintenanceCreateBeforeSave")
    let createButton = app.buttons["创建"].firstMatch
    XCTAssertTrue(createButton.waitForExistence(timeout: 15))
    XCTAssertTrue(createButton.isEnabled)
    createButton.tap()
    try screen("MaintenanceCreateAfterSave")
    XCTAssertTrue(element("pending.message").waitForExistence(timeout: 20))
    XCTAssertFalse(target.isEnabled)
    XCTAssertFalse(element("create.save").isEnabled)
    let accepted = try await call("/v2/training/plans/current")
    XCTAssertNotNil(accepted["planID"] as? String)
    try tap("pending.retry")
    let closed = XCTNSPredicateExpectation(
      predicate: NSPredicate(format: "exists == false"), object: target)
    XCTAssertEqual(XCTWaiter.wait(for: [closed], timeout: 30), .completed)
    let replayed = try await call("/v2/training/plans/current")
    XCTAssertEqual(replayed["planID"] as? String, accepted["planID"] as? String)
  }
  private func replace(_ field: XCUIElement, with text: String) {
    XCTAssertTrue(field.waitForExistence(timeout: 10))
    field.tap()
    let old = field.value as? String ?? ""
    field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: old.count) + text)
  }
  private func call(_ path: String, body: [String: Any]? = nil) async throws -> [String: Any] {
    let bytes = try Data(contentsOf: URL(fileURLWithPath: privateRoot + "/config.json"))
    let config = try JSONSerialization.jsonObject(with: bytes) as! [String: String]
    var request = URLRequest(url: URL(string: config["baseURL"]! + path)!)
    request.setValue("Bearer " + config["token"]!, forHTTPHeaderField: "Authorization")
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    if let body {
      request.httpMethod = "POST"
      request.httpBody = try JSONSerialization.data(withJSONObject: body)
    }
    for attempt in 0..<5 {
      let (data, response) = try await URLSession.shared.data(for: request)
      let http = response as! HTTPURLResponse
      if http.statusCode == 429 && attempt < 4 {
        try await Task.sleep(
          for: .seconds(max(1, Double(http.value(forHTTPHeaderField: "Retry-After") ?? "1") ?? 1)))
        continue
      }
      XCTAssertEqual(http.statusCode, 200)
      return try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }
    throw UIFlowFailure.missing("HTTP retry limit")
  }
  private func freshPlan(includingToday: Bool = false) async throws {
    let current = try await call("/v2/training/plans/current")
    var config = current["config"] as! [String: Any]
    if includingToday { config["availableDays"] = [1, 2, 3, 4, 5, 7] }
    _ = try await call(
      "/v2/training/plans",
      body: [
        "commandID": UUID().uuidString.lowercased(), "planID": UUID().uuidString.lowercased(),
        "config": config,
      ])
    for _ in 0..<45 {
      let plan = try await call("/v2/training/plans/current")
      if plan["effective"] is [String: Any] && plan["generationState"] as? String == "succeeded" {
        return
      }
      try await Task.sleep(for: .seconds(2))
    }
    XCTFail("Actual Worker did not publish a fresh synthetic Effective plan")
  }
  private func concurrentOverride() async throws {
    let plan = try await call("/v2/training/plans/current")
    let effective = plan["effective"] as! [String: Any]
    let sessions = effective["sessions"] as! [[String: Any]]
    let session = sessions.first { $0["type"] as? String != "REST" }!
    _ = try await call(
      "/v2/training/plans/" + (plan["planID"] as! String) + "/overrides",
      body: [
        "commandID": UUID().uuidString.lowercased(), "overrideID": UUID().uuidString.lowercased(),
        "sessionID": session["sessionID"]!, "expectedOverrideRevision": plan["overrideRevision"]!,
        "operation": "edit", "path": "notes", "value": "synthetic UI concurrent update",
      ])
  }
}

private enum UIFlowFailure: Error {
  case missing(String)
  case disabled(String)
}
