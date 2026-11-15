import Foundation
import SwiftUI
import SyncCore
import ProductCore

struct HeartRateSettingsView: View {
  @Bindable var model: ProductModel
  @Environment(\.dismiss) private var dismiss
  @State private var maximum = ""
  @State private var resting = ""
  @State private var originalMax: JSON?
  @State private var originalRest: JSON?
  @State private var originalMode = "automatic"
  @State private var observedMax: JSON?
  @State private var observedRest: JSON?
  @State private var adoptedMax: Double?
  @State private var adoptedRest: Double?
  @State private var supported = false
  @State private var loading = true
  @State private var busy = false
  @State private var loadFailed = false
  @State private var message: String?

  var body: some View {
    Form {
      Section {
        heartRateField("最大心率", placeholder: "填写最大心率", value: $maximum, id: "hr-settings.max")
        heartRateField("静息心率", placeholder: "可留空", value: $resting, id: "hr-settings.resting")
      } header: { Text("心率设置") } footer: {
        Text("填写后固定使用，直到你再次修改。静息心率留空时，自动参考健康数据。保存后从今天起用于计算。")
      }
      if loading {
        Section { ProgressView("正在读取设置…") }
      } else if loadFailed {
        Section {
          Text("暂时无法读取心率设置，请重试。")
          Button("重新读取") { Task { await load() } }
        }
      }
      if maximumSuggestion != nil || restingSuggestion != nil {
        Section {
          if let value = maximumSuggestion {
            VStack(alignment: .leading, spacing: 8) {
              Text("最大心率建议 · \(Int(value)) bpm").font(.headline)
              Text("跑步中观测到更高的连续 30 秒心率中位数，可作为最大心率参考。")
                .font(.footnote).foregroundStyle(.secondary)
              Button("采用为最大心率") { maximum = String(Int(value)); adoptedMax = value }
                .accessibilityIdentifier("hr-settings.use-observed")
            }
          }
          if let value = restingSuggestion {
            VStack(alignment: .leading, spacing: 8) {
              Text("静息心率建议 · \(Int(value)) bpm").font(.headline)
              Text("近期健康数据中有 \(observedRest?["sampleDays"].integer ?? 0) 天静息心率观测，数值较稳定。")
                .font(.footnote).foregroundStyle(.secondary)
              Button("采用为静息心率") { resting = String(Int(value)); adoptedRest = value }
                .accessibilityIdentifier("hr-settings.use-resting-observed")
            }
          }
        } header: { Text("心率建议") } footer: {
          Text("采用建议只会填入数值，点保存后生效。")
        }
      }
      Section {
        if !loading && !loadFailed && !supported { Text("服务器暂不支持心率设置，请更新服务器后再保存。") }
        if let validationMessage { Text(validationMessage).font(.footnote).foregroundStyle(.red) }
        if let message { Text(message).font(.footnote) }
        Button(busy ? "保存中…" : "保存") { save() }
          .disabled(!valid || busy || loading || !supported || loadFailed || model.pendingModelInput != nil)
          .accessibilityIdentifier("hr-settings.save")
        if model.pendingModelInput != nil {
          Button("重新提交上一次修改") {
            busy = true
            Task { message = await model.retryModelInput(); busy = false; if message == nil { await load() } }
          }.disabled(busy || loading || !supported)
        }
      }
    }.navigationTitle("静息与最大心率").raiScreen().accessibilityIdentifier("screen.hr-settings")
      .disabled(busy)
      .task { await load() }
  }

  private func heartRateField(_ title: String, placeholder: String, value: Binding<String>, id: String) -> some View {
    LabeledContent(title) {
      HStack {
        TextField(placeholder, text: value).keyboardType(.numberPad).multilineTextAlignment(.trailing)
          .accessibilityLabel(title).accessibilityIdentifier(id)
        Text("bpm").foregroundStyle(.secondary)
      }
    }.disabled(loading || loadFailed)
  }
  private var maximumSuggestion: Double? {
    HRSettingDraft.maximumSuggestion(observedMax, current: Double(maximum))
  }
  private var restingSuggestion: Double? {
    HRSettingDraft.restingSuggestion(observedRest, current: Double(resting), maximum: Double(maximum))
  }
  private var validationMessage: String? {
    if !maximum.isEmpty, Double(maximum).map({ !$0.isFinite || $0 < 30 || $0 >= 220 }) ?? true { return "最大心率须为 30–219 bpm。" }
    if !resting.isEmpty {
      guard let rest = Double(resting), rest.isFinite, rest >= 30, rest < 220 else { return "静息心率须为 30–219 bpm。" }
      if let max = Double(maximum), rest >= max { return "静息心率须低于最大心率。" }
    }
    return nil
  }
  private var valid: Bool { Double(maximum) != nil && validationMessage == nil }

  private func load() async {
    loading = true
    let (response, canWrite) = await model.loadHRSettingsV3()
    supported = canWrite
    loadFailed = response == nil
    guard let response else { loading = false; return }
    originalMax = nil; originalRest = nil; originalMode = "automatic"
    observedMax = response["observedMaxHR"]; observedRest = response["observedRestingHR"]
    if case .object = response["savedHRSettings"] {
      let saved = response["savedHRSettings"]
      if case .object = saved["maxHR"] { originalMax = saved["maxHR"] }
      originalMode = saved["restingHR"].string("mode").isEmpty ? "automatic" : saved["restingHR"].string("mode")
      if case .object = saved["restingHR"]["manualValue"] { originalRest = saved["restingHR"]["manualValue"] }
    } else if case .object = response["hrReference"] {
      let old = response["hrReference"]
      originalMax = .object(["value": old["maxHR"], "basis": .string("user_setting"), "method": old["method"], "unit": .string("bpm"), "measuredAt": old["measuredAt"]])
      if let value = old.number("restingHR") {
        originalRest = HRSettingDraft.value(value, method: "user_entered", basis: "user_setting", measuredAt: nil)
        originalMode = "manual"
      }
    }
    maximum = originalMax?.number("value").map { String(format: "%g", $0) } ?? ""
    resting = originalRest?.number("value").map { String(format: "%g", $0) } ?? ""
    adoptedMax = nil; adoptedRest = nil
    loading = false
  }
  private func save() {
    guard valid, let max = Double(maximum) else { return }
    let payload = HRSettingDraft.simplePatch(maximum: max, resting: Double(resting), originalMaximum: originalMax,
      originalResting: originalRest, originalMode: originalMode, suggestedMaximum: adoptedMax, suggestedResting: adoptedRest)
    guard !payload.isEmpty else { message = "设置没有变化。"; return }
    busy = true
    Task {
      message = await model.submitModelInput(kind: "hr_reference", payload: payload,
        effectiveFrom: Calendar.current.startOfDay(for: Date()), schemaVersion: "rai.hr_reference/3")
      busy = false
      if message == nil { dismiss() }
    }
  }
}
