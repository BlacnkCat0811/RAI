import Charts
import MapKit
import ProductCore
import SwiftUI

/// Native training detail. Facts are shown as soon as they are durable; derived
/// values appear only after an analysis publication. The page never renders a
/// placeholder zero as a score.
struct WorkoutDetail: View {
  @Bindable var model: ProductModel
  let workout: ProductWorkout
  @Environment(\.scenePhase) private var scenePhase
  @Environment(\.accessibilityReduceMotion) private var reducedMotion
  @State private var summaryExpanded = false
  @State private var effortMessage: String?
  @State private var effortBusy = false
  @State private var confirmEffort = false
  @State private var reanalyzing = false
  @AppStorage("developerOptions") private var developerOptions = false

  private var detail: WorkoutDetailResponse? {
    guard model.workoutDetail?.workout.workoutID == workout.id else { return nil }
    return model.workoutDetail
  }
  private var series: WorkoutSeriesResponse? {
    guard model.workoutSeries?.workoutID == workout.id else { return nil }
    return model.workoutSeries
  }
  private var freshness: WorkoutFreshness? {
    guard model.workoutFreshness?.workoutID == workout.id else { return nil }
    return model.workoutFreshness
  }
  private var taskKey: String {
    workout.id + "|" + (scenePhase == .active ? "active" : "inactive")
  }

  var body: some View {
    ScrollView {
      if let detail {
        LazyVStack(alignment: .leading, spacing: 16) {
          if let banner = DetailFreshnessBanner.make(detail: detail, freshness: freshness) {
            bannerCard(banner)
          }
          routePanel(detail)
          summaryPanel(detail)
          trainingEffectPanel(detail)
          effectPanel(detail)
          if let feature = detail.analysis, feature.schemaVersion == "rai.workout_features/1" {
            VStack(alignment: .leading, spacing: 8) {
              CardHeading(title: "数据覆盖", icon: "waveform.path")
              LabeledContent("有效运动时长", value: ProductFormat.duration(feature.activeSeconds)).accessibilityIdentifier("model.workout.active")
              LabeledContent("心率覆盖", value: feature.heartRateCoverage.formatted(.percent.precision(.fractionLength(0))))
              LabeledContent("速度覆盖", value: feature.speedCoverage.formatted(.percent.precision(.fractionLength(0))))
            }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
          }
          ForEach(WorkoutMetricKind.allCases) { metric in
            VStack(alignment: .leading, spacing: 8) {
              WorkoutSeriesChart(
                metric: metric,
                series: metric.series(series?.series ?? .empty),
                average: metric.average(detail.workout),
                durationSeconds: detail.workout.elapsedS,
                emptyNote: emptyNote(metric)
              )
              if metric == .heartRate, let zones = detail.zoneSummary {
                WorkoutZoneCard(title: "心率区间", coverage: zones.heartRateCoverage, exposure: zones.heartRateExposure, isHeartRate: true)
              }
              if metric == .pace, let zones = detail.zoneSummary {
                WorkoutZoneCard(title: "配速区间", coverage: zones.paceCoverage, exposure: zones.paceExposure, isHeartRate: false, unavailable: zones.paceZoneStatus == "threshold_pace_unavailable")
              }
            }.padding(.vertical, 6)
              .accessibilityIdentifier("workout.metric.\(metric.rawValue)")
          }
          segmentsOrSplits(detail: detail, series: series)
          if let dynamics = series?.dynamics { dynamicsPanel(dynamics) }
          qualityPanel(detail)
          runFitnessPanel(detail.runFitness)
          effortPanel(detail.runFitness?.single.effortKind)
          if let override = detail.override { overridePanel(override) }
        }.padding(.horizontal, 18).padding(.vertical, 14).padding(.bottom, 24)
      } else if let error = model.workoutDetailError {
        VStack(spacing: 14) {
          EmptyCard(title: "跑步详情暂不可用", detail: error)
          OrangeButton(title: "重新加载", icon: "arrow.clockwise") {
            Task { await model.loadWorkoutDetail(workout.id) }
          }
        }.padding(18)
      } else {
        ProgressView("正在读取").padding(.top, 80)
      }
    }
    .navigationTitle("跑步详情")
    .navigationBarTitleDisplayMode(.inline)
    .toolbar(.visible, for: .navigationBar)
    .raiScreen()
    .task(id: taskKey) {
      guard scenePhase == .active else { return }
      await model.loadWorkoutDetail(workout.id)
      await pollFreshness()
    }
    .refreshable { await model.loadWorkoutDetail(workout.id) }
    .accessibilityIdentifier("screen.workout.detail")
  }

  /// This run's single-run fitness: score or range, analysed minutes, evidence
  /// type and conditions, compared with the same-policy baseline from before
  /// the run. Never presented as a marathon time this run could achieve.
  @ViewBuilder private func runFitnessPanel(_ fitness: WorkoutRunFitness?) -> some View {
    VStack(alignment: .leading, spacing: 8) {
      CardHeading(title: "单次跑力", icon: "gauge.with.dots.needle.67percent")
      if let fitness {
        let single = fitness.single
        if let hr = single.hrNormalized {
          HStack(alignment: .firstTextBaseline, spacing: 6) {
            Text(num(hr.score, digits: 1)).font(.title2.weight(.bold)).monospacedDigit()
              .accessibilityIdentifier("workout.runFitness.score")
            Text("分").foregroundStyle(RAITheme.secondary)
            Text(RunningFitnessText.singleStatus(single.status)).font(.caption)
              .padding(.horizontal, 6).padding(.vertical, 2)
              .background(RAITheme.primary.opacity(0.10), in: Capsule())
            Spacer()
            if let delta = fitness.deltaToReference {
              Text((delta >= 0 ? "+" : "") + num(delta, digits: 1) + " 较训练前基准").font(.caption).monospacedDigit()
            }
          }
          Text("范围 \(num(hr.scoreSensitivityRange.low, digits: 1))–\(num(hr.scoreSensitivityRange.high, digits: 1)) 分 · 有效分析 \(Int((hr.netSupportedSeconds / 60).rounded())) 分钟 · 心率与配速")
            .font(.caption).foregroundStyle(RAITheme.secondary)
          if hr.gradeMode != "adjusted" {
            Text(hr.gradeMode == "indoor" ? "跑步机：速度没有路线核对，只作有限参考" : "坡度未知：按未校正配速估计")
              .font(.caption2).foregroundStyle(RAITheme.secondary)
          }
          if hr.conditionWeight < 1 {
            Text("天气条件不同，这次跑步在长期跑力里降权").font(.caption2).foregroundStyle(RAITheme.secondary)
          }
        }
        if let race = single.raceEquivalent {
          Text("\(RunningFitnessText.effortName(race.kind))成绩折算 \(num(race.score, digits: 1)) 分 · \(RunningFitnessText.raceTime(race.elapsedSeconds))")
            .font(.subheadline)
        }
        if single.hrNormalized == nil && single.raceEquivalent == nil {
          Text("这次跑步没有单次跑力：" + RunningFitnessText.singleReason(single.reasonCodes))
            .font(.caption).foregroundStyle(RAITheme.secondary)
        }
        if let retention = single.retention {
          Text("长跑后段效率保持 \(num(retention.retention * 100, digits: 1))%").font(.caption2).foregroundStyle(RAITheme.secondary)
        }
        if let suggestion = single.hrMaxSuggestion {
          Text("这次跑步持续 15 秒以上心率达到 \(Int(suggestion)) bpm，高于当前最大心率设置；可在「我的 → 静息与最大心率」确认是否更新。")
            .font(.caption2).foregroundStyle(RAITheme.amber)
        }
        Text("单次分按统一的默认耐力假设换算，不代表这次能跑出的全马成绩。")
          .font(.caption2).foregroundStyle(RAITheme.secondary)
      } else {
        Text("跑力还在计算，或这次跑步不在当前模型的范围内。").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      Button(reanalyzing ? "已提交重新分析" : "按当前设置重新分析这次跑步") {
        reanalyzing = true
        Task { await model.requestFitnessReanalysis(workoutIDs: [workout.id]) }
      }.font(.caption).disabled(reanalyzing).accessibilityIdentifier("workout.runFitness.reanalyze")
      if reanalyzing, let op = model.fitnessReanalysis {
        Text(RunningFitnessText.reanalysisStatus(op)).font(.caption2).foregroundStyle(RAITheme.secondary)
      }
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("workout.runFitness")
  }

  /// Labels this run as a race or a structured test. Labels change only the
  /// model input; HealthKit data is never modified. Everyday runs need none.
  private func effortPanel(_ current: String?) -> some View {
    VStack(alignment: .leading, spacing: 8) {
      CardHeading(title: "这是比赛或测试吗？", icon: "flame")
      Text("比赛和全力测试会校准速度、阈值和长距离能力；日常训练不用标记。")
        .font(.caption).foregroundStyle(RAITheme.secondary)
      if let current {
        Text("当前标注：" + RunningFitnessText.effortName(current)).font(.caption)
      }
      if let effortMessage {
        Text(effortMessage).font(.caption).foregroundStyle(RAITheme.secondary)
          .accessibilityIdentifier("workout.effort.message")
      }
      Button(effortBusy ? "正在提交…" : "标记为比赛或测试") { confirmEffort = true }
        .buttonStyle(RAISecondaryButtonStyle())
        .disabled(effortBusy)
        .accessibilityIdentifier("workout.effort.mark")
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .confirmationDialog("这次跑步是？", isPresented: $confirmEffort, titleVisibility: .visible) {
        ForEach(["race", "threshold_test", "speed_test", "sprint_test"], id: \.self) { kind in
          Button(RunningFitnessText.effortName(kind)) { submitEffort(kind) }
        }
      }
  }

  private func submitEffort(_ kind: String) {
    effortBusy = true
    Task {
      let failure = await model.submitModelInput(
        kind: "workout_effort",
        payload: ["workoutID": .string(workout.id.lowercased()), "mode": .string(kind), "userConfirmed": .bool(true)],
        effectiveFrom: Date())
      effortMessage = failure ?? "已标记为\(RunningFitnessText.effortName(kind))，跑力更新后生效。"
      effortBusy = false
    }
  }

  private func pollFreshness() async {
    guard freshness?.completeness != "complete" else { return }
    if freshness?.analysisState == "published",
       (freshness?.series ?? []).allSatisfy({ $0.state != "not_seen_yet" }) {
      return
    }
    for delay in [2, 5, 10, 15] {
      try? await Task.sleep(for: .seconds(delay))
      guard !Task.isCancelled, scenePhase == .active else { return }
      _ = await model.pollWorkoutFreshness(workout.id)
      if freshness?.completeness == "complete" { return }
    }
  }

  private func emptyNote(_ metric: WorkoutMetricKind) -> String {
    let key = metric.rawValue
    if freshness?.seriesState(key) == "not_seen_yet" {
      return "\(metric.title)数据还没有同步过来。"
    }
    return "这次跑步没有\(metric.title)数据"
  }

  private func bannerCard(_ banner: DetailFreshnessBanner.Banner) -> some View {
    HStack(alignment: .top, spacing: 10) {
      Image(systemName: banner.icon).font(.subheadline.weight(.semibold)).foregroundStyle(banner.color).accessibilityHidden(true)
      VStack(alignment: .leading, spacing: 4) {
        Text(banner.title).font(.subheadline.weight(.semibold))
        Text(banner.detail).font(.caption).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
      }
      Spacer(minLength: 0)
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("workout.freshnessStatus")
  }

  @ViewBuilder private func routePanel(_ detail: WorkoutDetailResponse) -> some View {
    if detail.route.points.count > 1 {
      VStack(alignment: .leading, spacing: 5) {
        WorkoutRouteMap(route: detail.route)
          .accessibilityIdentifier("workout.route")
      }
    } else {
      Label(detail.workout.indoor == true ? "室内跑步，没有路线" : "这次跑步没有路线", systemImage: "map")
        .font(.footnote).foregroundStyle(RAITheme.secondary)
        .frame(maxWidth: .infinity, minHeight: 72).raiGlass()
        .accessibilityIdentifier("workout.route.empty")
    }
  }

  private func summaryPanel(_ detail: WorkoutDetailResponse) -> some View {
    let w = detail.workout
    return VStack(alignment: .leading, spacing: 10) {
      HStack(spacing: 8) {
        Text("概览").font(.headline)
        Spacer(minLength: 8)
        Text(sessionName(w.sessionType)).font(.caption).foregroundStyle(RAITheme.secondary)
        if w.indoor == true { badge("室内") }
        Image(systemName: summaryExpanded ? "chevron.up" : "chevron.down")
          .font(.caption.weight(.semibold)).foregroundStyle(RAITheme.amber).accessibilityHidden(true)
      }
      Text(w.startDate.formatted(date: .abbreviated, time: .shortened))
        .font(.caption.monospacedDigit()).foregroundStyle(RAITheme.secondary)
      HStack(spacing: 22) {
        VStack(alignment: .leading, spacing: 5) {
          Text(ProductFormat.decimal(w.distanceKM, digits: 2))
            .font(.system(.largeTitle, design: .rounded, weight: .bold)).monospacedDigit()
          Text("距离（公里）").font(.caption).foregroundStyle(RAITheme.secondary)
        }
        VStack(alignment: .leading, spacing: 5) {
          Text(ProductFormat.duration(w.durationS))
            .font(.system(.title2, design: .rounded, weight: .bold)).monospacedDigit()
          Text("运动时间").font(.caption).foregroundStyle(RAITheme.secondary)
        }
        Spacer(minLength: 0)
      }
      environmentRow(detail.environment)
      LazyVGrid(columns: [GridItem(.adaptive(minimum: 96), alignment: .leading)], alignment: .leading, spacing: 14) {
        summaryMetric("平均配速", paceValue(detail.summary.averagePaceSPerKm))
        summaryMetric("坡度修正配速", paceValue(detail.summary.gradeAdjustedPaceSPerKm), accent: RAITheme.blue)
        summaryMetric("平均心率", value(w.averageHeartRateBpm, digits: 0, unit: "bpm"))
        summaryMetric("累计上升", value(detail.summary.elevationGainM, digits: 0, unit: "m"))
        summaryMetric("能量消耗", value(w.activeEnergyKcal, digits: 0, unit: "kcal"))
        summaryMetric("训练负荷", num(detail.trainingLoad?.load, digits: 0))
      }
      if detail.summary.totalSplits > 0, detail.summary.coveredSplits < detail.summary.totalSplits {
        Text(detail.summary.coveredSplits > 0
             ? "坡度修正配速仅覆盖 \(detail.summary.coveredSplits)/\(detail.summary.totalSplits) 段"
             : "没有坡度数据，无法计算坡度修正配速")
          .font(.caption2).foregroundStyle(RAITheme.secondary)
      }
      if let env = detail.environment, env.status != "pending" { environmentSummary(env) }
      if summaryExpanded {
        Divider().overlay(RAITheme.primary.opacity(0.12))
        Text("更多指标").font(.subheadline.weight(.medium))
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 96), alignment: .leading)], alignment: .leading, spacing: 14) {
          summaryMetric("经过时间", ProductFormat.duration(w.elapsedS))
          summaryMetric("平均步频", value(w.averageCadenceSpm, digits: 0, unit: "spm"))
          summaryMetric("平均功率", value(w.averagePowerW, digits: 0, unit: "W"))
          summaryMetric("平均步幅", value(w.averageStrideLengthM, digits: 2, unit: "m"))
          summaryMetric("垂直振幅", value(w.averageVerticalOscillationCm, digits: 1, unit: "cm"))
          summaryMetric("触地时间", value(w.averageGroundContactTimeMs, digits: 0, unit: "ms"))
          summaryMetric("数据质量", num(w.qualityScore, digits: 2))
        }.accessibilityIdentifier("workout.summary.metrics")
        if let adjusted = detail.environment?.adjusted {
          summaryMetric("环境等效用时", ProductFormat.duration(adjusted.durationS)).accessibilityIdentifier("workout.summary.adjustedDuration")
        }
        if let load = detail.trainingLoad {
          HStack(spacing: 14) {
            summaryMetric("负荷强度", num(load.intensity, digits: 1))
            summaryMetric("负荷可信度", percent(load.confidence))
          }
          Text("主要刺激：\(stimulusName(load.stimulusType))").font(.caption).foregroundStyle(RAITheme.secondary)
          Text("负荷计算方式：\(loadSourceName(load.source))").font(.caption2).foregroundStyle(RAITheme.secondary)
        }
        if let focus = detail.loadFocus {
          Text("负荷构成：低强度有氧 \(num(focus.buckets.lowAerobicAU, digits: 0)) · 高强度有氧 \(num(focus.buckets.highAerobicAU, digits: 0)) · 无氧 \(num(focus.buckets.anaerobicProxyAU, digits: 0)) · 未分类 \(num(focus.buckets.unclassifiedAU, digits: 0))")
            .font(.caption2).foregroundStyle(RAITheme.secondary)
        }
        if let zones = detail.zoneSummary {
          Text("区间覆盖：心率 \(percent(zones.heartRateCoverage)) · 配速 \(percent(zones.paceCoverage))")
            .font(.caption).foregroundStyle(RAITheme.secondary)
        }
        if let q = detail.modelQualification {
          VStack(alignment: .leading, spacing: 2) {
            Text(WorkoutQualificationText.trainingLoad(q.trainingLoad))
          }.font(.caption2).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier("workout.summary.modelQualification")
        }
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .contentShape(Rectangle()).onTapGesture { withAnimation(reducedMotion ? nil : .easeInOut(duration: 0.2)) { summaryExpanded.toggle() } }
      .accessibilityElement(children: .contain)
      .accessibilityAddTraits(.isButton)
      .accessibilityValue(summaryExpanded ? "已展开" : "已收起")
      .accessibilityIdentifier("workout.summary")
  }

  @ViewBuilder private func environmentRow(_ env: WorkoutEnvironment?) -> some View {
    // Indoor runs and old records carry no weather: an all-empty row is noise.
    if env?.weatherCondition != nil || env?.temperatureC != nil || env?.humidityPercent != nil {
      weatherRow(env)
    }
  }
  private func weatherRow(_ env: WorkoutEnvironment?) -> some View {
    HStack(spacing: 12) {
      Label("天气 \(weatherName(env?.weatherCondition))", systemImage: "cloud.sun")
      Label("温度 \(value(env?.temperatureC, digits: 1, unit: "°C"))", systemImage: "thermometer.medium")
      Label("湿度 \(value(env?.humidityPercent, digits: 0, unit: "%"))", systemImage: "humidity")
    }.font(.caption.monospacedDigit()).foregroundStyle(RAITheme.secondary)
      .accessibilityIdentifier("workout.summary.weather")
  }

  @ViewBuilder private func environmentSummary(_ env: WorkoutEnvironment) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      HStack {
        Text("环境修正").font(.caption).foregroundStyle(RAITheme.secondary)
        Spacer(minLength: 8)
        Text(environmentStatus(env.status)).font(.caption.weight(.medium))
          .foregroundStyle(env.status == "applied" ? RAITheme.green : RAITheme.secondary)
      }
      if let total = env.correction?.total {
        Text("修正量 \(num(total * 100, digits: 1))%").font(.caption2.monospacedDigit()).foregroundStyle(RAITheme.secondary)
      }
      if !env.reasonCodes.isEmpty {
        Text(env.reasonCodes.map(environmentReason).joined(separator: " · "))
          .font(.caption2).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
      }
    }
    .accessibilityIdentifier("workout.summary.environment")
  }

  /// Aerobic and anaerobic training effect, each 0.0-5.0. Unavailable scores
  /// show "—" with an actionable reason; the stimulus-type card stays separate.
  @ViewBuilder private func trainingEffectPanel(_ detail: WorkoutDetailResponse) -> some View {
    VStack(alignment: .leading, spacing: 12) {
      CardHeading(title: "训练效果", icon: "flame")
      if let effect = detail.trainingEffect {
        HStack(spacing: 12) {
          TrainingEffectGauge(title: "有氧", score: effect.aerobic)
            .accessibilityIdentifier("workout.trainingEffect.aerobic")
          TrainingEffectGauge(title: "无氧", score: effect.anaerobic)
            .accessibilityIdentifier("workout.trainingEffect.anaerobic")
        }
        if [effect.aerobic, effect.anaerobic].contains(where: { $0.map(TrainingEffectText.isOverreaching) ?? false }) {
          Label("刺激过量，注意恢复", systemImage: "exclamationmark.triangle")
            .font(.caption.weight(.medium)).foregroundStyle(RAITheme.amber)
        }
        let notes = trainingEffectNotes(effect)
        ForEach(notes, id: \.self) { note in
          Text(note).font(.caption).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
        }
        if ([effect.aerobic, effect.anaerobic].compactMap { $0?.reasonCodes }.joined()).contains("hr_reference_missing") {
          NavigationLink {
            HeartRateSettingsView(model: model)
          } label: {
            Label("填写最大心率", systemImage: "heart")
          }.font(.caption.weight(.medium)).tint(RAITheme.blue)
            .accessibilityIdentifier("workout.trainingEffect.hrSettings")
        }
        DisclosureGroup("计算依据") {
          VStack(alignment: .leading, spacing: 4) {
            LabeledContent("EPOC 峰值（代理值）", value: value(effect.epocPeak, digits: 0, unit: "ml/kg"))
            LabeledContent("训练等级", value: num(effect.trainingLevel, digits: 1) + " · " + TrainingEffectText.levelStatus(effect.trainingLevelStatus))
            LabeledContent("高强度段", value: "\(effect.bouts ?? 0) 段 · " + ProductFormat.duration(effect.highIntensitySeconds))
          }.font(.caption.monospacedDigit()).padding(.top, 4)
        }.font(.caption).tint(RAITheme.blue)
        Text(TrainingEffectText.disclaimer).font(.caption2).foregroundStyle(RAITheme.secondary)
          .fixedSize(horizontal: false, vertical: true)
      } else {
        Text(detail.workout.analysisStatus == "published" ? "当前模型版本尚未计算这次跑步的训练效果。" : "分析完成后显示训练效果。")
          .font(.footnote).foregroundStyle(RAITheme.secondary)
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityElement(children: .contain)
      .accessibilityIdentifier("workout.trainingEffect")
  }

  private func trainingEffectNotes(_ effect: WorkoutTrainingEffect) -> [String] {
    var seen = Set<String>()
    return [effect.aerobic, effect.anaerobic].compactMap { $0 }.flatMap(TrainingEffectText.reasons).filter { seen.insert($0).inserted }
  }

  @ViewBuilder private func effectPanel(_ detail: WorkoutDetailResponse) -> some View {
    let zones = detail.zoneSummary
    let scores = (zones?.stimulusScores ?? []).filter { $0.qualifyingSeconds > 0 || $0.score > 0 }
      .sorted { $0.score == $1.score ? $0.stimulusType < $1.stimulusType : $0.score > $1.score }
    VStack(alignment: .leading, spacing: 10) {
      HStack {
        Text("刺激类型").font(.headline)
        Spacer()
        if let confidence = zones?.stimulusConfidence ?? detail.workout.stimulusConfidence {
          Text("可信度 " + percent(confidence)).font(.caption).foregroundStyle(RAITheme.secondary)
        }
      }
      if let primary = scores.first {
        HStack(alignment: .bottom, spacing: 12) {
          VStack(alignment: .leading, spacing: 4) {
            Text("主要效果").font(.caption).foregroundStyle(RAITheme.secondary)
            Text(stimulusName(primary.stimulusType)).font(.title3.weight(.semibold))
            Text("有效时长 \(ProductFormat.duration(primary.qualifyingSeconds)) / 目标 \(ProductFormat.duration(primary.targetSeconds))")
              .font(.caption2.monospacedDigit()).foregroundStyle(RAITheme.secondary)
          }
          Spacer(minLength: 8)
          VStack(alignment: .trailing, spacing: 3) {
            Text(percent(primary.score)).font(.title2.weight(.semibold).monospacedDigit()).foregroundStyle(RAITheme.amber)
            Text("目标完成度").font(.caption2).foregroundStyle(RAITheme.secondary)
          }
        }
      } else if detail.workout.analysisStatus != "published" {
        Text("分析完成后显示刺激类型。")
          .font(.footnote).foregroundStyle(RAITheme.secondary)
      } else if zones?.stimulusType == "unclassified" {
        Text("这次跑步暂时无法归类刺激类型：还没有可用的阈值配速。")
          .font(.footnote).foregroundStyle(RAITheme.secondary)
      } else {
        Text("这次跑步暂无刺激类型数据").font(.footnote).foregroundStyle(RAITheme.secondary)
      }
      if let zones, !zones.secondaryStimulusTypes.isEmpty {
        Text("次要效果：\(zones.secondaryStimulusTypes.map(stimulusName).joined(separator: "、"))")
          .font(.caption).foregroundStyle(RAITheme.secondary)
      }
      ForEach(scores) { score in
        WorkoutStatLine(label: stimulusName(score.stimulusType), value: "\(percent(score.score)) · \(ProductFormat.duration(score.qualifyingSeconds))",
                        fraction: score.score, note: "可信度 \(percent(score.confidence))")
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("workout.training-effect")
  }

  @ViewBuilder private func segmentsOrSplits(detail: WorkoutDetailResponse, series: WorkoutSeriesResponse?) -> some View {
    if let series, !series.segments.isEmpty {
      VStack(alignment: .leading, spacing: 12) {
        HStack {
          Text(segmentHeading(series)).font(.headline)
          Spacer(); Text("\(series.segments.count) 段").font(.caption).foregroundStyle(RAITheme.secondary)
        }
        ForEach(series.segments) { segment in
          VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
              Text(String(format: "%02d", segment.index)).font(.caption.monospacedDigit().weight(.semibold)).foregroundStyle(segmentTint(segment.purpose))
              Text(segment.label).font(.headline)
              Spacer(minLength: 0)
              Text(ProductFormat.duration(segment.durationSeconds)).font(.caption.monospacedDigit()).foregroundStyle(RAITheme.secondary)
            }
            if let alert = segmentAlert(segment) { Text(alert).font(.caption).foregroundStyle(segmentTint(segment.purpose)) }
            LazyVGrid(columns: [GridItem(.flexible(), alignment: .leading), GridItem(.flexible(), alignment: .leading)], alignment: .leading, spacing: 8) {
              segmentMetric("实际距离", segment.distanceMeters > 0 ? ProductFormat.decimal(segment.distanceMeters / 1000, digits: 2) + " 公里" : "--")
              segmentMetric("平均配速", paceValue(segment.averagePaceSPerKm))
              segmentMetric("平均心率", value(segment.averageHeartRateBpm, digits: 0, unit: "bpm"))
              segmentMetric("平均步频", value(segment.averageCadenceSpm, digits: 0, unit: "spm"))
            }
          }.padding(14).frame(maxWidth: .infinity, alignment: .leading)
            .background(RAITheme.primary.opacity(0.04), in: RoundedRectangle(cornerRadius: 18))
        }
        if let plan = series.segmentPlan {
          DisclosureGroup("分段来源") {
            Text("按 \(plan.localDate) 的训练计划（\(sessionName(plan.kind))）分段 · 匹配度 \(percent(plan.explainedFraction))")
              .font(.caption).foregroundStyle(RAITheme.secondary)
            if !series.segmentNote.isEmpty { Text(series.segmentNote).font(.caption).foregroundStyle(RAITheme.secondary) }
          }.font(.caption).tint(RAITheme.blue)
        } else if !series.segmentNote.isEmpty {
          Text(series.segmentNote).font(.caption).foregroundStyle(RAITheme.secondary)
        }
      }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass().accessibilityIdentifier("workout.segments")
    } else {
      splitPanel(series)
    }
  }

  private func segmentHeading(_ series: WorkoutSeriesResponse) -> String {
    if series.segmentPlan != nil { return "按计划分段" }
    return series.hasMeasuredWorkoutSteps ? "手表训练分段" : "手表分段"
  }

  private func splitPanel(_ series: WorkoutSeriesResponse?) -> some View {
    let splits = series?.splits ?? []
    let maxPace = max(splits.map(\.averagePaceSPerKm).max() ?? 1, 1)
    return VStack(alignment: .leading, spacing: 4) {
      HStack {
        Text("每公里").font(.headline)
        Spacer(); Text("\(splits.count) 段").font(.caption).foregroundStyle(RAITheme.secondary)
      }
      if let note = series?.segmentNote, !note.isEmpty {
        Text(note).font(.caption).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
      }
      if splits.isEmpty {
        Text("暂无分段：需要完整的配速和距离数据。")
          .font(.footnote).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
      } else {
        ForEach(splits) { split in
          VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 8) {
              Text(split.distanceM < 999.5 ? "尾段" : "\(split.index) km").font(.subheadline.weight(.semibold)).frame(width: 52, alignment: .leading)
              GeometryReader { g in
                Capsule().fill(RAITheme.primary.opacity(0.10)).frame(height: 6).overlay(alignment: .leading) {
                  Capsule().fill(RAITheme.orange).frame(width: max(4, g.size.width * min(1, split.averagePaceSPerKm / maxPace)), height: 6)
                }
              }.frame(height: 6)
              Text(paceValue(split.averagePaceSPerKm)).font(.caption.weight(.bold).monospacedDigit())
              Text(value(split.averageHeartRateBpm, digits: 0, unit: "bpm")).font(.caption.monospacedDigit()).foregroundStyle(RAITheme.secondary)
            }
            if let grade = split.grade?.gradePercent {
              Text("坡度 \(num(grade, digits: 1))%").font(.caption2.monospacedDigit()).foregroundStyle(RAITheme.secondary)
            }
          }.padding(.vertical, 7).overlay(alignment: .bottom) { Rectangle().fill(RAITheme.primary.opacity(0.08)).frame(height: 1) }
        }
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass().accessibilityIdentifier("workout.splits")
  }

  private func dynamicsPanel(_ dynamics: RunningDynamics) -> some View {
    VStack(alignment: .leading, spacing: 10) {
      HStack {
        Text("跑姿").font(.headline)
        Spacer()
      }
      Text("对比前后半程在相同配速下的跑姿变化，数据来自手表估计。")
        .font(.caption).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
      if !dynamics.analysable {
        Text(dynamics.note.isEmpty ? "这次跑步没有跑姿数据" : dynamics.note)
          .font(.footnote).foregroundStyle(RAITheme.secondary)
      } else {
        LazyVGrid(columns: [GridItem(.flexible(), alignment: .leading), GridItem(.flexible(), alignment: .leading)], alignment: .leading, spacing: 12) {
          ForEach(dynamics.metrics) { metric in
            VStack(alignment: .leading, spacing: 2) {
              Text(metric.label).font(.caption).foregroundStyle(RAITheme.secondary)
              Text(dynamicsAverage(metric)).font(.subheadline.weight(.semibold)).monospacedDigit()
              Text(dynamicsDrift(metric)).font(.caption2).foregroundStyle(RAITheme.secondary)
              Text("覆盖 \(percent(metric.coverage))").font(.caption2).foregroundStyle(RAITheme.secondary)
              if !metric.reasonCodes.isEmpty {
                Text(metric.reasonCodes.map(dynamicsReason).joined(separator: "；"))
                  .font(.caption2).foregroundStyle(RAITheme.amber).fixedSize(horizontal: false, vertical: true)
              }
            }
          }
        }
        ForEach(dynamics.flags) { flag in
          Text(flag.detail)
            .font(.caption).foregroundStyle(RAITheme.amber).fixedSize(horizontal: false, vertical: true)
        }
        if !dynamics.note.isEmpty { Text(dynamics.note).font(.caption2).foregroundStyle(RAITheme.secondary) }
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass().accessibilityIdentifier("workout.dynamics")
  }

  private func dynamicsAverage(_ metric: RunningDynamicsMetric) -> String {
    guard let average = metric.average else { return "--" }
    return "\(num(average, digits: metric.unit == "spm" ? 0 : 1)) \(metric.unit)"
  }
  private func dynamicsDrift(_ metric: RunningDynamicsMetric) -> String {
    guard let drift = metric.driftFraction else { return "无法对比前后半程" }
    let sign = drift > 0 ? "+" : ""
    return "后半程 \(sign)\(num(drift * 100, digits: 1))%"
  }

  private func qualityPanel(_ detail: WorkoutDetailResponse) -> some View {
    let pending = detail.workout.analysisStatus != "published"
    return VStack(alignment: .leading, spacing: 10) {
      HStack {
        Text("数据质量").font(.headline)
        Spacer()
        if pending { RAITag(text: "分析中", color: RAITheme.blue) }
        else if developerOptions { Text(detail.workout.modelVersion).font(.caption2).foregroundStyle(RAITheme.secondary) }
      }
      if pending {
        Text("分析完成后显示。")
          .font(.footnote).foregroundStyle(RAITheme.secondary)
      } else {
        HStack(spacing: 8) {
          qualityTile("心率覆盖", percent(detail.workout.heartRateCoverage))
          qualityTile("数据可信度", percent(detail.workout.sourceConfidence))
          qualityTile("效果可信度", percent(detail.workout.stimulusConfidence))
        }
        HStack(spacing: 14) {
          captionMetric("分析状态", assessmentName(detail.workout.assessmentStatus))
          captionMetric("心率样本", "\(detail.workout.heartRateSampleCount)")
          captionMetric("路线质量", percent(detail.workout.routeQuality))
        }
        if !detail.workout.missingReasons.isEmpty {
          Text("部分数据缺失，以上结果的可信度会降低。")
            .font(.footnote).foregroundStyle(RAITheme.amber)
        }
        if developerOptions {
          Text((detail.workout.missingReasons + detail.workout.explanationCodes).joined(separator: " · "))
            .font(.caption2).foregroundStyle(RAITheme.secondary)
        }
      }
    }.padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass().accessibilityIdentifier("workout.quality")
  }

  private func overridePanel(_ override: WorkoutOverride) -> some View {
    VStack(alignment: .leading, spacing: 7) {
      HStack {
        Text("手动标注").font(.headline)
        Spacer()
      }
      if let value = override.eligibilityOverride { Text("纳入状态：\(value)") }
      if let value = override.statisticsParticipation { Text("统计参与：\(value)") }
      if let value = override.sessionType { Text("训练类型：\(sessionName(value))") }
      if let value = override.performanceProtocol { Text("测试类型：\(value)") }
      if let rpe = override.rpe { Text("训练感受：自感用力 \(Int(rpe))") }
      if !override.conditionTags.isEmpty { Text("状态标签：\(override.conditionTags.joined(separator: "、"))") }
      if let note = override.note, !note.isEmpty { Text(note) }
    }.font(.footnote).foregroundStyle(RAITheme.secondary)
      .padding(18).frame(maxWidth: .infinity, alignment: .leading).raiGlass().accessibilityIdentifier("workout.override")
  }

  private func summaryMetric(_ title: String, _ value: String, accent: Color = RAITheme.primary) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      Text(value).font(.subheadline.weight(.semibold).monospacedDigit()).foregroundStyle(accent).fixedSize(horizontal: false, vertical: true)
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary).fixedSize(horizontal: false, vertical: true)
    }.frame(maxWidth: .infinity, alignment: .leading)
  }
  private func qualityTile(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 4) {
      Text(value).font(.subheadline.weight(.semibold).monospacedDigit())
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
    }.frame(maxWidth: .infinity, alignment: .leading)
  }
  private func captionMetric(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 2) {
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(value).font(.caption.weight(.semibold).monospacedDigit())
    }
  }
  private func segmentMetric(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 3) {
      Text(title).font(.caption2).foregroundStyle(RAITheme.secondary)
      Text(value).font(.subheadline.weight(.semibold).monospacedDigit())
    }
  }
  private func badge(_ text: String) -> some View {
    Text(text).font(.caption2.weight(.semibold)).padding(.horizontal, 8).padding(.vertical, 3)
      .background(RAITheme.primary.opacity(0.10), in: Capsule())
  }
  private func segmentTint(_ purpose: String) -> Color {
    switch purpose {
    case "warmup": RAITheme.amber
    case "work": RAITheme.blue
    case "recovery", "cooldown": RAITheme.green
    default: RAITheme.secondary
    }
  }
  private func segmentAlert(_ segment: WorkoutSegment) -> String? {
    guard let lower = segment.alertLower, let upper = segment.alertUpper else { return nil }
    if segment.alertKind == "pace" {
      return "配速提醒 \(paceValue(lower))–\(paceValue(upper))"
    }
    return "心率提醒 \(Int(lower))–\(Int(upper)) bpm"
  }
}

// MARK: - formatting

func paceValue(_ secondsPerKm: Double?) -> String {
  guard let n = secondsPerKm, n.isFinite, n > 0 else { return "--" }
  let s = Int(n.rounded())
  return String(format: "%d′%02d″/km", s / 60, s % 60)
}
func num(_ value: Double?, digits: Int) -> String { ProductFormat.decimal(value, digits: digits) }
func value(_ value: Double?, digits: Int, unit: String) -> String {
  guard let value, value.isFinite else { return "--" }
  return "\(ProductFormat.decimal(value, digits: digits)) \(unit)"
}
func percent(_ value: Double?) -> String {
  guard let value, value.isFinite else { return "--" }
  return ProductFormat.decimal(value * 100, digits: 0) + "%"
}
func sessionName(_ type: String) -> String {
  [
    "REST": "休息", "EASY": "基础跑", "LONG": "长距离", "TEMPO": "节奏跑", "INTERVAL": "间歇",
    "RECOVERY": "恢复跑", "RACE": "比赛", "RUN": "跑步", "published": "已完成", "pending": "分析中", "failed": "分析失败",
  ][type] ?? "跑步"
}
func assessmentName(_ status: String) -> String {
  [
    "published": "已完成", "pending": "分析中", "failed": "失败", "assessed": "已完成",
    "limited": "数据有限", "insufficient": "数据不足", "excluded": "未纳入",
  ][status] ?? (status.isEmpty ? "--" : "已完成")
}
func stimulusName(_ type: String) -> String {
  [
    "recovery": "恢复", "aerobic_base": "有氧基础", "aerobic_power": "有氧动力",
    "lactate_threshold": "乳酸阈值", "vo2max": "最大摄氧", "anaerobic": "无氧耐力",
    "anaerobic_endurance": "无氧耐力", "sprint": "冲刺", "unclassified": "未分类",
  ][type] ?? "未分类"
}
func weatherName(_ code: Int?) -> String {
  guard let code else { return "未记录" }
  switch code {
  case 1, 2: return "晴"; case 3, 4: return "多云"; case 5: return "阴"; case 6: return "雾"
  case 7: return "霾"; case 8, 9: return "大风"; case 12: return "雪"; case 21: return "小雨"
  case 22, 23: return "阵雨"; case 24: return "雷雨"; default: return "已记录"
  }
}
func environmentStatus(_ status: String) -> String {
  ["applied": "已应用", "not_needed": "无需修正", "unavailable": "不可用", "pending": "等待分析完成"][status] ?? "不可用"
}
func environmentReason(_ code: String) -> String {
  [
    "temperature_missing": "温度未记录", "humidity_missing": "湿度未记录",
    "elevation_missing": "缺少爬升数据", "reference_conditions": "接近标准条件", "analysis_pending": "等待分析完成",
    "heat_humidity_adjustment": "热湿修正", "elevation_adjustment": "爬升修正",
  ][code] ?? "其他因素"
}
func dynamicsReason(_ code: String) -> String {
  ["no_comparable_pace_samples": "前后半程没有相同配速的片段"][code] ?? "数据不足"
}
func loadSourceName(_ source: String) -> String {
  if source.hasPrefix("training_load.trimp") { return "按心率计算" }
  if source.hasPrefix("planned_effort_fallback") { return "按计划强度估算" }
  if source.hasPrefix("session_type_fallback") { return "按训练类型估算" }
  return "其他"
}

// MARK: - freshness

enum DetailFreshnessBanner {
  struct Banner {
    let title: String, detail: String, icon: String, color: Color
  }
  static func make(detail: WorkoutDetailResponse, freshness: WorkoutFreshness?) -> Banner? {
    if freshness?.analysisState == "failed" || detail.workout.analysisStatus == "failed" {
      return Banner(title: "分析失败", detail: "跑步记录本身完好，但分析结果暂时没有生成，可稍后下拉刷新。", icon: "exclamationmark.circle", color: RAITheme.amber)
    }
    if detail.workout.analysisStatus != "published" {
      return Banner(title: "分析中", detail: "记录已保存，分析完成后会自动补全评分和配速分析，通常需要 1–2 分钟。", icon: "hourglass", color: RAITheme.blue)
    }
    let missing = (freshness?.series ?? []).filter { $0.state == "not_seen_yet" }.map(\.metric)
    if !missing.isEmpty {
      return Banner(title: "数据仍在补齐", detail: "这些数据还没从手表传过来：\(missing.map(metricName).joined(separator: "、"))。稍后会自动补上。", icon: "clock.arrow.circlepath", color: RAITheme.blue)
    }
    return nil
  }
  private static func metricName(_ key: String) -> String {
    WorkoutMetricKind(rawValue: key)?.title ?? key
  }
}

// MARK: - route map

struct WorkoutRouteMap: View {
  let route: WorkoutRoute
  @State private var interactive = false

  private var coordinates: [CLLocationCoordinate2D] {
    route.points.map { CLLocationCoordinate2D(latitude: $0.latitude, longitude: $0.longitude) }
      .filter { CLLocationCoordinate2DIsValid($0) }
  }
  private var region: MKCoordinateRegion {
    let lats = coordinates.map(\.latitude), lons = coordinates.map(\.longitude)
    guard let minLat = lats.min(), let maxLat = lats.max(), let minLon = lons.min(), let maxLon = lons.max() else {
      return MKCoordinateRegion(center: CLLocationCoordinate2D(latitude: 0, longitude: 0), span: MKCoordinateSpan(latitudeDelta: 1, longitudeDelta: 1))
    }
    let center = CLLocationCoordinate2D(latitude: (minLat + maxLat) / 2, longitude: (minLon + maxLon) / 2)
    return MKCoordinateRegion(center: center, span: MKCoordinateSpan(
      latitudeDelta: max((maxLat - minLat) * 1.4, 0.003),
      longitudeDelta: max((maxLon - minLon) * 1.4, 0.003)))
  }

  var body: some View {
    ZStack(alignment: .bottomTrailing) {
      Map(initialPosition: .region(region), interactionModes: interactive ? .all : []) {
        MapPolyline(coordinates: coordinates)
          .stroke(RAITheme.orange, style: StrokeStyle(lineWidth: 3, lineCap: .round, lineJoin: .round))
        if let first = coordinates.first {
          Annotation("起点", coordinate: first) {
            Circle().fill(RAITheme.green).frame(width: 12, height: 12).overlay(Circle().stroke(.black.opacity(0.5), lineWidth: 2))
          }
        }
        if let last = coordinates.last {
          Annotation("终点", coordinate: last) {
            Circle().fill(RAITheme.orange).frame(width: 12, height: 12).overlay(Circle().stroke(.black.opacity(0.5), lineWidth: 2))
          }
        }
        ForEach(route.kilometerMarkers) { marker in
          Annotation("\(marker.kilometer) 公里", coordinate: CLLocationCoordinate2D(latitude: marker.latitude, longitude: marker.longitude)) {
            Text("\(marker.kilometer)").font(.system(size: 9, weight: .bold)).foregroundStyle(.black)
              .frame(width: 17, height: 17).background(RAITheme.lemon, in: Circle())
          }
        }
      }
      .mapStyle(.standard(elevation: .flat))
      .mapControlVisibility(.hidden)
      .allowsHitTesting(interactive)
      if !interactive {
        Color.clear.contentShape(Rectangle()).onTapGesture { interactive = true }
          .accessibilityElement().accessibilityAddTraits(.isButton)
          .accessibilityLabel("地图已锁定，轻点解锁缩放与拖动")
      }
      if interactive {
        Button { interactive = false } label: {
          Label("锁定", systemImage: "lock.fill").font(.caption.weight(.semibold))
            .padding(.horizontal, 10).padding(.vertical, 7).foregroundStyle(RAITheme.primary).background(RAITheme.surface, in: Capsule())
        }.padding(8).accessibilityLabel("锁定地图，恢复上下滑动翻页")
      }
    }
    .frame(height: 230)
    .clipShape(RoundedRectangle(cornerRadius: 22, style: .continuous))
    .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).stroke(RAITheme.primary.opacity(0.18), lineWidth: 1))
    .accessibilityValue("公里标记：\(route.kilometerMarkers.map { "\($0.kilometer) 公里" }.joined(separator: "、"))")
  }
}

// MARK: - charts

struct WorkoutSeriesChart: View {
  let metric: WorkoutMetricKind
  let series: WorkoutMetricSeries
  let average: Double?
  let durationSeconds: Double
  let emptyNote: String
  @State private var selected: Double?
  @ScaledMetric(relativeTo: .caption) private var height: CGFloat = 180

  private var accent: Color {
    switch metric {
    case .pace: RAITheme.blue
    case .heartRate: RAITheme.orange
    case .power: RAITheme.amber
    case .elevation: RAITheme.green
    default: RAITheme.blue
    }
  }
  private var selectedPoint: WorkoutMetricPoint? {
    guard let selected else { return nil }
    return series.points.min { abs($0.minutes - selected) < abs($1.minutes - selected) }
  }
  private var minimum: Double? { series.points.map(\.value).min() }
  private var maximum: Double? { series.points.map(\.value).max() }

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      HStack {
        Text(metric.title).font(.headline)
        Spacer()
        Text(metric == .elevation ? metric.unit : "平均 \(display(average))").font(.caption).foregroundStyle(accent)
      }
      if !series.points.isEmpty {
        HStack {
          Text("\(metric == .pace ? "最快" : "最低") \(display(minimum))")
          Spacer(minLength: 8)
          Text("\(metric == .pace ? "最慢" : "最高") \(display(maximum))")
        }.font(.caption.monospacedDigit()).foregroundStyle(RAITheme.secondary)
      }
      if series.points.isEmpty {
        Text(average == nil ? emptyNote : "只有平均值，没有变化曲线。")
          .font(.footnote).foregroundStyle(RAITheme.secondary)
          .frame(maxWidth: .infinity, minHeight: 64).fixedSize(horizontal: false, vertical: true)
      } else {
        Chart {
          ForEach(series.points) { point in
            LineMark(x: .value("分钟", point.minutes), y: .value(metric.title, point.value))
              .foregroundStyle(accent).interpolationMethod(.linear).lineStyle(StrokeStyle(lineWidth: 2))
            if series.points.count == 1 {
              PointMark(x: .value("分钟", point.minutes), y: .value(metric.title, point.value)).foregroundStyle(accent)
            }
          }
          if let selectedPoint {
            RuleMark(x: .value("分钟", selectedPoint.minutes)).foregroundStyle(RAITheme.secondary).lineStyle(StrokeStyle(lineWidth: 1, dash: [4]))
            PointMark(x: .value("分钟", selectedPoint.minutes), y: .value(metric.title, selectedPoint.value)).foregroundStyle(RAITheme.primary).symbolSize(38)
          }
        }
        .chartXScale(domain: 0...max(1.0 / 60, durationSeconds.isFinite ? durationSeconds / 60 : 0, series.points.last?.minutes ?? 0))
        .chartYScale(domain: .automatic(includesZero: false, reversed: metric == .pace))
        .chartXAxis { AxisMarks(values: .automatic(desiredCount: 4)) { AxisValueLabel().foregroundStyle(RAITheme.secondary); AxisGridLine().foregroundStyle(RAITheme.primary.opacity(0.08)) } }
        .chartYAxis { AxisMarks(position: .leading, values: .automatic(desiredCount: 3)) { axis in
          AxisValueLabel { if let v = axis.as(Double.self) { Text(display(v)).foregroundStyle(RAITheme.secondary) } }
          AxisGridLine().foregroundStyle(RAITheme.primary.opacity(0.08))
        } }
        .chartXAxisLabel("min").chartYAxisLabel(metric == .pace ? "min/km" : metric.unit)
        .frame(height: height)
        .chartOverlay { proxy in
          GeometryReader { geometry in
            Color.clear.contentShape(Rectangle()).onTapGesture { location in
              guard let frame = proxy.plotFrame else { return }
              let plot = geometry[frame]
              guard plot.contains(location) else { return }
              selected = proxy.value(atX: location.x - plot.minX, as: Double.self)
            }
          }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(metric.title)时序")
        .accessibilityValue(selectedPoint.map { "\(num($0.minutes, digits: 1)) 分钟，\(display($0.value))" } ?? "最低 \(display(minimum))，最高 \(display(maximum))")
        .accessibilityAdjustableAction { direction in adjust(direction) }
      }
      if let selectedPoint {
        Text("\(num(selectedPoint.minutes, digits: 1)) min · \(display(selectedPoint.value))")
          .font(.subheadline.monospacedDigit())
      }
    }.padding(16).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
      .accessibilityIdentifier("workout.series.\(metric.rawValue)")
  }

  private func display(_ value: Double?) -> String {
    guard let value, value.isFinite else { return "--" }
    return metric == .pace ? paceValue(value) : "\(num(value, digits: metric.digits)) \(metric.unit)"
  }
  private func adjust(_ direction: AccessibilityAdjustmentDirection) {
    guard !series.points.isEmpty else { return }
    let current = selectedPoint?.minutes ?? (direction == .increment ? series.points[0].minutes : series.points.last!.minutes)
    let next = series.points.min { abs($0.minutes - current) < abs($1.minutes - current) }?.minutes ?? current
    let candidates = series.points.map(\.minutes)
    selected = direction == .increment ? candidates.first { $0 > next } ?? next : candidates.last { $0 < next } ?? next
  }
}

struct WorkoutZoneCard: View {
  let title: String
  let coverage: Double
  let exposure: [ZoneExposure]
  let isHeartRate: Bool
  var unavailable = false

  var body: some View {
    VStack(alignment: .leading, spacing: 6) {
      HStack {
        Text("\(title) · 有效覆盖 \(percent(coverage))").font(.caption).foregroundStyle(RAITheme.secondary)
        Spacer()
      }
      if unavailable {
        Text("还没有阈值配速，暂无配速区间").font(.caption2).foregroundStyle(RAITheme.secondary)
      } else {
        ForEach(exposure) { zone in
          if zone.durationSeconds > 0 {
            WorkoutStatLine(label: isHeartRate ? heartRateZoneName(zone.zone) : paceZoneName(zone.zone),
                            value: "\(percent(zone.fraction)) · \(ProductFormat.duration(zone.durationSeconds))",
                            fraction: zone.fraction)
          }
        }
      }
    }.padding(.horizontal, 4).accessibilityIdentifier("workout.zones.\(isHeartRate ? "heartRate" : "pace")")
  }
  private func heartRateZoneName(_ zone: String) -> String {
    ["recovery": "恢复区", "aerobic_base": "有氧基础", "aerobic_power": "有氧动力", "lactate_threshold": "乳酸阈值", "anaerobic": "无氧"][zone] ?? "其他"
  }
  private func paceZoneName(_ zone: String) -> String {
    ["easy": "轻松区", "marathon": "马拉松区", "lactate_threshold": "阈值区", "anaerobic_endurance": "无氧耐力区", "sprint": "冲刺区"][zone] ?? "其他"
  }
}

struct WorkoutStatLine: View {
  let label: String, value: String
  var fraction: Double?
  var note: String?

  var body: some View {
    VStack(alignment: .leading, spacing: 4) {
      HStack {
        Text(label).font(.caption).foregroundStyle(RAITheme.secondary)
        Spacer()
        Text(value).font(.caption.monospacedDigit())
      }
      if let fraction {
        GeometryReader { g in
          Capsule().fill(RAITheme.primary.opacity(0.10)).frame(height: 4).overlay(alignment: .leading) {
            Capsule().fill(RAITheme.blue).frame(width: max(2, g.size.width * min(1, max(0, fraction))), height: 4)
          }
        }.frame(height: 4)
      }
      if let note { Text(note).font(.caption2).foregroundStyle(RAITheme.secondary) }
    }.padding(.vertical, 2)
  }
}
