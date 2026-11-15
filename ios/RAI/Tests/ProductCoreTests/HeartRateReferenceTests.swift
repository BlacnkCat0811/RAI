import Foundation
import Testing
import SyncCore
@testable import ProductCore

struct HeartRateReferenceTests {
  @Test func maximumOnlyChangeDoesNotResubmitRestingMeasurement() {
    let date = Date(timeIntervalSince1970: 1_700_000_000)
    let max = HRSettingDraft.value(190, method: "user_entered", basis: "user_setting", measuredAt: nil)
    let rest = HRSettingDraft.value(50, method: "user_entered", basis: "measured", measuredAt: date)
    let update = HRSettingDraft.value(195, method: "user_entered", basis: "user_setting", measuredAt: nil)
    let patch = HRSettingDraft.patch(maximum: update, resting: rest, mode: "manual", originalMaximum: max, originalResting: rest, originalMode: "manual")
    #expect(patch["maxHR"] == update)
    #expect(patch["restingHR"] == nil)
    #expect(rest["measuredAt"].text != nil)
  }
  @Test func automaticAndExplicitClearAreRetained() {
    let max = HRSettingDraft.value(190, method: "user_entered", basis: "user_setting", measuredAt: nil)
    let rest = HRSettingDraft.value(50, method: "user_entered", basis: "user_setting", measuredAt: nil)
    let patch = HRSettingDraft.patch(maximum: max, resting: nil, mode: "automatic", originalMaximum: max, originalResting: rest, originalMode: "manual")
    #expect(patch["maxHR"] == nil)
    #expect(patch["restingHR"]?["mode"] == .string("automatic"))
    #expect(patch["restingHR"]?["manualValue"] == .null)
    #expect(max["measuredAt"] == .null)
  }
  @Test func decodedIntegerValuesDoNotCauseAnUnrelatedUpdate() throws {
    let original = try JSON.decode(Data(#"{"value":190,"basis":"user_setting","method":"user_entered","unit":"bpm"}"#.utf8))
    let value = HRSettingDraft.value(190, method: "user_entered", basis: "user_setting", measuredAt: nil)
    #expect(HRSettingDraft.patch(maximum: value, resting: nil, mode: "automatic", originalMaximum: original, originalResting: nil, originalMode: "automatic").isEmpty)
  }
}
