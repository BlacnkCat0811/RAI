import Foundation
import Testing
import SyncCore
@testable import ProductCore

struct HeartRateReferenceTests {
  @Test func simpleFormPreservesUnchangedDatesAndSavesNewValuesWithoutInventingDates() {
    let date = Date(timeIntervalSince1970: 1_700_000_000)
    let max = HRSettingDraft.value(190, method: "field_max_test", basis: "measured", measuredAt: date)
    let rest = HRSettingDraft.value(50, method: "user_entered", basis: "measured", measuredAt: date)
    let patch = HRSettingDraft.simplePatch(maximum: 195, resting: 50, originalMaximum: max, originalResting: rest, originalMode: "manual", suggestedMaximum: 195)
    #expect(patch["restingHR"] == nil)
    #expect(patch["maxHR"]?["measuredAt"] == .null)
    #expect(patch["maxHR"]?.string("basis") == "user_setting")
    #expect(patch["maxHR"]?.string("method") == "wearable_estimate")
    #expect(HRSettingDraft.simplePatch(maximum: 190, resting: 50, originalMaximum: max, originalResting: rest, originalMode: "manual").isEmpty)
  }
  @Test func filledRestingValueIsFixedAndClearingReturnsToAutomatic() {
    let max = HRSettingDraft.value(190, method: "user_entered", basis: "user_setting", measuredAt: nil)
    let rest = HRSettingDraft.value(50, method: "user_entered", basis: "user_setting", measuredAt: nil)
    let fixed = HRSettingDraft.simplePatch(maximum: 190, resting: 50, originalMaximum: max, originalResting: rest, originalMode: "automatic")
    #expect(fixed["restingHR"]?.string("mode") == "manual")
    let clear = HRSettingDraft.simplePatch(maximum: 190, resting: nil, originalMaximum: max, originalResting: rest, originalMode: "manual")
    #expect(clear["restingHR"]?.string("mode") == "automatic")
    #expect(clear["restingHR"]?["manualValue"] == .null)
    #expect(clear["maxHR"] == nil)
  }
  @Test func suggestionsNeverLowerMaximumOrRepeatInsignificantRestingChanges() {
    let value = JSON.object(["value": .int(190)])
    #expect(HRSettingDraft.maximumSuggestion(value, current: 195) == nil)
    #expect(HRSettingDraft.maximumSuggestion(value, current: 190) == nil)
    #expect(HRSettingDraft.maximumSuggestion(value, current: 185) == 190)
    let resting = JSON.object(["value": .int(52)])
    #expect(HRSettingDraft.restingSuggestion(resting, current: 50, maximum: 190) == nil)
    #expect(HRSettingDraft.restingSuggestion(resting, current: 55, maximum: 190) == 52)
    #expect(HRSettingDraft.restingSuggestion(resting, current: nil, maximum: 50) == nil)
    #expect(HRSettingDraft.maximumSuggestion(.object(["value": .number(220)]), current: nil) == nil)
  }
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
