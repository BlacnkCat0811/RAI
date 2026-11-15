import Foundation
import SwiftUI
import SyncCore
import ProductCore

struct HeartRateSettingsView: View {
  @Bindable var model: ProductModel
  @Environment(\.dismiss) private var dismiss
  @State private var maximum = ""
  @State private var resting = ""
  @State private var mode = "automatic"
  @State private var effectiveDate = Date()
  @State private var maxDateKnown = false
  @State private var restDateKnown = false
  @State private var maxDate = Date()
  @State private var restDate = Date()
  @State private var settings: JSON?
  @State private var resolved: JSON?
  @State private var originalMax: JSON?
  @State private var originalRest: JSON?
  @State private var originalMode = "automatic"
  @State private var maxBasis = "user_setting"
  @State private var restBasis = "user_setting"
  @State private var maxMethod = "user_entered"
  @State private var restMethod = "user_entered"
  @State private var effectiveChanged = false
  @State private var supported = false
  @State private var loading = true
  @State private var busy = false
  @State private var message: String?
  @State private var observed: JSON?
  @State private var age = ""
  @State private var ageValue = ""

  var body: some View {
    Form {
      Section("已保存的设置") {
        if loading { ProgressView() }
        else if settings != nil {
          LabeledContent("最大心率", value: originalMax?.number("value").map { String(format: "%g bpm", $0) } ?? "未设置")
          LabeledContent("静息模式", value: originalMode == "manual" ? "固定手填值" : "自动计算，手填值作备用")
        } else { Text("尚未设置心率参考") }
      }
      if let resolved {
        Section("当前计算采用的参考") {
          referenceRow("最大心率", resolved["maxHR"])
          referenceRow("静息心率", resolved["restingHR"])
        }
      } else if !loading {
        Section { Text("当前模型尚未提供新版参考证据；保存设置不代表历史分析已经更新。") }
      }
      Section("最大心率") {
        TextField("最大心率", text: $maximum).keyboardType(.numberPad).accessibilityIdentifier("hr-settings.max").onChange(of: maximum) { if maxMethod == "age_estimated" && maximum != ageValue { maxMethod = "user_entered" } }
        Toggle("知道测量日期", isOn: $maxDateKnown)
        if maxDateKnown {
          DatePicker("测量日期", selection: $maxDate, in: ...Date(), displayedComponents: .date)
          Picker("使用依据", selection: $maxBasis) { Text("计算设置（可用于指定历史）").tag("user_setting"); Text("该日实测（不用于更早训练）").tag("measured") }
        }
        if let value = observed?.number("value") {
          Button("使用观测建议 \(Int(value)) bpm") { maximum = String(Int(value)); maxMethod = "wearable_estimate"; maxBasis = "user_setting" }
            .accessibilityIdentifier("hr-settings.use-observed")
        }
        if originalMax == nil {
          TextField("年龄", text: $age).keyboardType(.numberPad).accessibilityIdentifier("hr-settings.age")
          Button("暂用年龄估计（208 − 0.7 × 年龄）") {
            if let age = Double(age), (10...90).contains(age) {
              ageValue = String(Int((208 - 0.7 * age).rounded())); maximum = ageValue; maxMethod = "age_estimated"; maxBasis = "user_setting"
            }
          }.disabled(Double(age).map { !(10...90).contains($0) } ?? true).accessibilityIdentifier("hr-settings.age-estimate")
          Text("年龄估计仅在你选择时采用，结果会标为有限，不据此提高训练强度。")
        }
      }
      Section {
        Picker("静息模式", selection: $mode) {
          Text("自动计算，手填作备用").tag("automatic")
          Text("固定使用手填值").tag("manual")
        }.accessibilityIdentifier("hr-settings.mode")
        TextField(mode == "manual" ? "固定静息心率" : "静息心率（可选备用值）", text: $resting)
          .keyboardType(.numberPad).accessibilityIdentifier("hr-settings.resting")
        if !resting.isEmpty {
          Toggle("知道静息测量日期", isOn: $restDateKnown)
          if restDateKnown {
            DatePicker("静息测量日期", selection: $restDate, in: ...Date(), displayedComponents: .date)
            Picker("静息依据", selection: $restBasis) { Text("计算设置（可用于指定历史）").tag("user_setting"); Text("该日实测（不用于更早训练）").tag("measured") }
          }
        }
      } header: { Text("静息心率") } footer: {
        Text(mode == "manual" ? "固定使用，直到你修改。超过 90 天仅提示复核，不停止计算。" : "优先用跑前 28 天的自动基线；数据稀疏时标为估计，不足时使用手填备用值。留空并保存会清除备用值。")
      }
      Section {
        DatePicker("适用起始日期", selection: $effectiveDate, in: ...Date(), displayedComponents: .date)
          .accessibilityIdentifier("hr-settings.effective-date").onChange(of: effectiveDate) { effectiveChanged = true }
        Text("默认从今天开始。若用于已同步历史，请明确选择起始日期。它与测量日期独立；日期未知或晚于训练的数值只能作为历史计算假设。")
          .font(.footnote)
        if !supported && !loading { Text("服务器暂不支持新版心率设置，请更新服务器后再保存。不会降级提交而丢失模式和日期。") }
        if let message { Text(message).font(.footnote).foregroundStyle(.red) }
        Button(busy ? "保存中…" : "保存") { save() }
          .disabled(!valid || busy || !supported).accessibilityIdentifier("hr-settings.save")
        if model.pendingModelInput != nil {
          Button("重新提交上一次修改") {
            busy = true
            Task { message = await model.retryModelInput(); busy = false; if message == nil { await load() } }
          }.disabled(busy || !supported)
        }
      }
    }.navigationTitle("静息与最大心率").raiScreen().accessibilityIdentifier("screen.hr-settings")
      .task { await load() }
  }
  private var valid: Bool {
    guard let max = Double(maximum), max >= 30, max < 220 else { return false }
    if resting.isEmpty { return mode == "automatic" }
    guard let rest = Double(resting), rest >= 30, rest < max else { return false }
    return true
  }
  private func referenceRow(_ title: String, _ value: JSON) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      LabeledContent(title, value: value.number("value").map { "\(Int($0)) bpm" } ?? "暂无可用参考")
      Text(HeartRateReferenceText.source(value.string("source"))).font(.caption)
      if value.string("status") == "estimated" { Text("估计参考").font(.caption) }
      if let raw = value["effectiveFrom"].text { Text("适用起始：" + String(raw.prefix(10))).font(.caption) }
      if let raw = value["measuredAt"].text { Text("测量日期：" + String(raw.prefix(10))).font(.caption) }
      if let n = value["sampleDays"].integer, n > 0 { Text("有效观测 \(n) 天").font(.caption) }
      if let raw = value["latestSampleAt"].text { Text("最近观测：" + String(raw.prefix(10))).font(.caption) }
      if value["reviewRecommended"] == .bool(true) { Text("建议复核，当前参考仍可使用").font(.caption) }
    }
  }
  private func load() async {
    loading = true
    let (response, canWrite) = await model.loadHRSettingsV3()
    supported = canWrite
    guard let response else { loading = false; return }
    observed = response["observedMaxHR"]
    if case .object = response["savedHRSettings"] {
      let saved = response["savedHRSettings"]; settings = saved
      if case .object = saved["maxHR"] { originalMax = saved["maxHR"] }
      mode = saved["restingHR"].string("mode").isEmpty ? "automatic" : saved["restingHR"].string("mode")
      originalMode = mode
      if case .object = saved["restingHR"]["manualValue"] { originalRest = saved["restingHR"]["manualValue"] }
    } else if case .object = response["hrReference"] {
      let old = response["hrReference"]; settings = old
      originalMax = .object(["value": old["maxHR"], "basis": .string("user_setting"), "method": old["method"], "unit": .string("bpm"), "measuredAt": old["measuredAt"]])
      if let v = old.number("restingHR") { originalRest = .object(["value": .number(v), "basis": .string("user_setting"), "method": .string("user_entered"), "unit": .string("bpm")]); mode = "manual"; originalMode = mode }
    }
    maximum = originalMax?.number("value").map { String(format: "%g", $0) } ?? ""
    resting = originalRest?.number("value").map { String(format: "%g", $0) } ?? ""
    maxMethod = originalMax?.string("method") ?? "user_entered"; restMethod = originalRest?.string("method") ?? "user_entered"
    if maxMethod == "age_estimated" { ageValue = maximum }
    maxBasis = originalMax?.string("basis") ?? "user_setting"; restBasis = originalRest?.string("basis") ?? "user_setting"
    if let raw = originalMax?["measuredAt"].text, let d = HRSettingDraft.date(raw) { maxDateKnown = true; maxDate = d }
    if let raw = originalRest?["measuredAt"].text, let d = HRSettingDraft.date(raw) { restDateKnown = true; restDate = d }
    if case .object = response["resolvedHRReference"] { resolved = response["resolvedHRReference"] }
    loading = false
  }
  private func save() {
    guard valid, let max = Double(maximum) else { return }
    let maxValue = HRSettingDraft.value(max, method: maxMethod, basis: maxDateKnown ? maxBasis : "user_setting", measuredAt: maxDateKnown ? maxDate : nil)
    let restValue = Double(resting).map { HRSettingDraft.value($0, method: restMethod, basis: restDateKnown ? restBasis : "user_setting", measuredAt: restDateKnown ? restDate : nil) }
    var payload = HRSettingDraft.patch(maximum: maxValue, resting: restValue, mode: mode, originalMaximum: originalMax, originalResting: originalRest, originalMode: originalMode)
    if payload.isEmpty && effectiveChanged { payload = ["maxHR": maxValue, "restingHR": .object(["mode": .string(mode), "manualValue": restValue ?? .null])] }
    guard !payload.isEmpty else { message = "设置没有变化。"; return }
    busy = true
    Task {
      message = await model.submitModelInput(kind: "hr_reference", payload: payload, effectiveFrom: effectiveDate, schemaVersion: "rai.hr_reference/3")
      busy = false
      if message == nil { dismiss() }
    }
  }
}
