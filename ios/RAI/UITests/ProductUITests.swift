import XCTest

@MainActor
final class ProductUITests: XCTestCase {
  private let privateRoot = "/tmp/rai-p6-native-acceptance"
  private var app: XCUIApplication!
  private func launchApp() {
    continueAfterFailure = false
    XCTAssertTrue(
      FileManager.default.fileExists(atPath: privateRoot + "/config-app.json"),
      "Start the isolated P6 host and synthetic driver first")
    app = XCUIApplication()
    app.launchEnvironment["RAI_SYNTHETIC_ACCEPTANCE"] = "1"
    app.launchEnvironment["RAI_ACCEPTANCE_CONFIG"] = privateRoot + "/config-app.json"
    app.launchArguments = ["-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
    app.launch()
    XCTAssertTrue(element("home.hero").waitForExistence(timeout: 60))
  }
  private func element(_ id: String) -> XCUIElement {
    app.descendants(matching: .any).matching(identifier: id).firstMatch
  }
  private func tap(_ id: String) {
    let e = element(id)
    XCTAssertTrue(e.waitForExistence(timeout: 15), id)
    if !e.isHittable { app.swipeUp() }
    e.tap()
  }
  private func tab(_ title: String) { app.tabBars.buttons[title].tap() }
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
  }
  func test01NavigationVisualBaselineAndDiagnostics() throws {
    launchApp()
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
    tap("profile.sync")
    XCTAssertTrue(app.buttons["Request Read Access"].exists || app.buttons["请求读取权限"].exists)
    tab("首页")
    tap("home.hero")
    XCTAssertTrue(element("session.edit").waitForExistence(timeout: 10))
    try screen("SessionDetail")
  }
  func test02EditConflictMoveSkipAddRevokeExecutionFeedback() async throws {
    launchApp()
    tap("home.hero")
    tap("session.edit")
    let value = app.textFields["edit.value"]
    XCTAssertTrue(value.waitForExistence(timeout: 10))
    value.tap()
    value.press(forDuration: 1.2)
    if app.menuItems["全选"].exists { app.menuItems["全选"].tap() }
    let old = value.value as? String ?? ""
    value.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: old.count) + "7100")
    try await concurrentOverride()
    tap("edit.save")
    XCTAssertTrue(element("edit.review").waitForExistence(timeout: 15))
    XCTAssertEqual(value.value as? String, "7100")
    try screen("Conflict")
    tap("edit.review")
    tap("edit.save")
    XCTAssertTrue(element("session.edit").waitForExistence(timeout: 20))
    app.swipeUp()
    if element("session.revoke").waitForExistence(timeout: 10) { tap("session.revoke") }
    app.swipeDown()
    tap("session.move")
    tap("edit.save")
    XCTAssertTrue(element("session.start").waitForExistence(timeout: 15))
    tap("session.start")
    XCTAssertTrue(element("execution.complete").waitForExistence(timeout: 15))
    try screen("Execution")
    tap("execution.complete")
    XCTAssertTrue(element("execution.associate").waitForExistence(timeout: 15))
    tap("execution.associate")
    tap("associate.candidate")
    XCTAssertTrue(element("feedback.card").waitForExistence(timeout: 15))
    try screen("Feedback")
    tab("训练")
    app.swipeUp()
    tap("training.add")
    let distance = app.textFields["add.distance"]
    XCTAssertTrue(distance.waitForExistence(timeout: 10))
    distance.tap()
    distance.typeText("3200")
    tap("edit.save")
    XCTAssertTrue(element("screen.training").waitForExistence(timeout: 15))
    tab("首页")
    app.swipeDown()
    tap("home.hero")
    tap("session.skip")
    tap("edit.save")
    XCTAssertTrue(element("screen.session").waitForExistence(timeout: 15))
  }
  func test03LaunchAndHomeScrollPerformance() {
    launchApp()
    let options = XCTMeasureOptions()
    options.iterationCount = 3
    measure(metrics: [XCTClockMetric(), XCTMemoryMetric(application: app)], options: options) {
      app.swipeUp()
      app.swipeDown()
    }
  }
  private func concurrentOverride() async throws {
    let bytes = try Data(contentsOf: URL(fileURLWithPath: privateRoot + "/config.json"))
    let config = try JSONSerialization.jsonObject(with: bytes) as! [String: String]
    func call(_ path: String, body: [String: Any]? = nil) async throws -> [String: Any] {
      var r = URLRequest(url: URL(string: config["baseURL"]! + path)!)
      r.setValue("Bearer " + config["token"]!, forHTTPHeaderField: "Authorization")
      r.setValue("application/json", forHTTPHeaderField: "Content-Type")
      if let body {
        r.httpMethod = "POST"
        r.httpBody = try JSONSerialization.data(withJSONObject: body)
      }
      let (data, response) = try await URLSession.shared.data(for: r)
      XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
      return try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }
    let plan = try await call("/v2/training/plans/current")
    let effective = plan["effective"] as! [String: Any]
    let sessions = effective["sessions"] as! [[String: Any]]
    let s = sessions.first { $0["type"] as? String != "REST" }!
    _ = try await call(
      "/v2/training/plans/" + (plan["planID"] as! String) + "/overrides",
      body: [
        "commandID": UUID().uuidString.lowercased(), "overrideID": UUID().uuidString.lowercased(),
        "sessionID": s["sessionID"]!, "expectedOverrideRevision": plan["overrideRevision"]!,
        "operation": "edit", "path": "notes", "value": "synthetic UI concurrent update",
      ])
  }
}
