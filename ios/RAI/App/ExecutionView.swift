import ProductCore
import SwiftUI
import SyncCore

struct ExecutionView: View {
  @Bindable var model: ProductModel
  @State private var candidates = false
  @State private var currentStep = 0
  var session: ProductSession { ProductSession(model.execution["snapshot"]) }
  var body: some View {
    ScrollView {
      VStack(alignment: .leading, spacing: 20) {
        VStack(alignment: .leading, spacing: 14) {
          Text(statusText(model.execution.string("status"))).font(.subheadline).foregroundStyle(
            RAITheme.amber)
          Text(session.title).font(.system(.largeTitle, design: .rounded, weight: .bold))
          Text(prescription(session)).font(.title2).monospacedDigit()
          Text("已冻结的训练处方").font(.caption).foregroundStyle(RAITheme.secondary)
          if model.execution.string("status") == "started" {
            TimelineView(.periodic(from: .now, by: 1)) { context in
              Text(elapsed(at: context.date)).font(
                .system(.largeTitle, design: .rounded, weight: .bold)
              ).monospacedDigit()
            }
            Text("这是训练执行记录；运动测量来自健康数据。").font(.caption).foregroundStyle(RAITheme.secondary)
          }
        }.padding(24).frame(maxWidth: .infinity, alignment: .leading).raiGlass()
        let steps = session.raw["steps"].list
        if !steps.isEmpty && model.execution.string("status") == "started" {
          let index = min(currentStep, steps.count - 1)
          let step = steps[index]
          VStack(alignment: .leading, spacing: 12) {
            Text("当前步骤 · 手动切换").font(.subheadline).foregroundStyle(RAITheme.secondary)
            Text(stepTitle(step.string("kind"))).font(.title.bold())
            Text(
              "目标 \(ProductFormat.duration(step.number("durationS"))) · 自感用力 \(Int(step.number("effort") ?? 0))"
            ).monospacedDigit()
            if index + 1 < steps.count {
              Button("下一步：" + stepTitle(steps[index + 1].string("kind"))) {
                currentStep += 1
                UserDefaults.standard.set(
                  currentStep, forKey: "executionStep:" + model.execution.string("executionID"))
              }
            }
          }.padding(22).raiGlass()
        }
        if !steps.isEmpty {
          VStack(alignment: .leading, spacing: 12) {
            CardHeading(title: "训练步骤 · 按处方执行", icon: "list.number")
            ForEach(Array(steps.enumerated()), id: \.offset) { index, step in
              Text(
                "\(index+1). \(stepTitle(step.string("kind"))) · \(ProductFormat.duration(step.number("durationS"))) · 自感用力 \(Int(step.number("effort") ?? 0))"
              )
              if let count = step.number("repeats"), count > 0 {
                Text("\(Int(count)) 次，每次恢复 \(ProductFormat.duration(step.number("recoveryS")))")
                  .font(.caption)
              }
            }
          }.padding(22).raiGlass()
        }
        if model.execution.string("status") == "started" {
          OrangeButton(title: "完成训练", icon: "checkmark") {
            Task { await model.changeExecution("complete") }
          }.disabled(model.loading).accessibilityIdentifier("execution.complete")
        }
        if model.execution.string("status") == "completed" {
          if model.execution.string("workoutID").isEmpty {
            EmptyCard(title: "等待运动记录", detail: "运动同步后，请明确选择并关联此次运动。不会自动匹配。")
            Button("选择运动记录") { candidates = true }.accessibilityIdentifier("execution.associate")
          }
          FeedbackCard(feedback: model.execution["feedback"].list.last ?? .null)
        }
        Button("刷新执行与反馈", systemImage: "arrow.clockwise") {
          Task { await model.refreshExecution() }
        }.accessibilityIdentifier("execution.refresh")
        if let error = model.error { Text(error).font(.footnote).foregroundStyle(RAITheme.amber) }
      }.padding(20)
    }.navigationTitle("训练执行").navigationBarTitleDisplayMode(.inline).raiScreen().task {
      await model.refreshExecution()
    }.sheet(isPresented: $candidates) {
      NavigationStack {
        ScrollView {
          VStack(spacing: 12) {
            Text("请确认这条运动属于此次训练").font(.footnote)
            ForEach(model.data.workouts) { w in
              Button {
                Task {
                  await model.changeExecution("associate", workout: w.id)
                  if !model.execution.string("workoutID").isEmpty { candidates = false }
                }
              } label: {
                VStack(alignment: .leading, spacing: 8) {
                  ActivityRow(workout: w)
                  Text("关联此次运动").font(.caption).foregroundStyle(RAITheme.orange)
                }.padding(18).raiGlass()
              }.accessibilityIdentifier("associate.candidate")
            }
            if !model.data.next.isEmpty { Button("更多运动") { Task { await model.loadMore() } } }
            if model.data.workouts.isEmpty { Text("运动尚未到达，请先同步。") }
          }.padding(18)
        }.raiScreen().navigationTitle("关联运动").toolbar { Button("关闭") { candidates = false } }
      }
    }.accessibilityIdentifier("screen.execution")
  }
  func elapsed(at date: Date) -> String {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    let start =
      f.date(from: model.execution.string("startedAt"))
      ?? ISO8601DateFormatter().date(from: model.execution.string("startedAt"))
    return ProductFormat.duration(start.map { max(0, date.timeIntervalSince($0)) })
  }
}
struct FeedbackCard: View {
  let feedback: JSON
  @Environment(\.dynamicTypeSize) private var typeSize
  var body: some View {
    VStack(alignment: .leading, spacing: 14) {
      CardHeading(title: "训练反馈", icon: "checkmark.circle")
      Text(feedbackStatus(feedback.string("status"))).font(.subheadline)
        .foregroundStyle(RAITheme.secondary)
      LazyVGrid(
        columns: [GridItem(.adaptive(minimum: typeSize.isAccessibilitySize ? 280 : 120))],
        alignment: .leading, spacing: 18
      ) {
        item("计划距离", ProductFormat.distance(feedback["distance"].number("planned")) + " 公里")
        item("实际距离", ProductFormat.distance(feedback["distance"].number("actual")) + " 公里")
        item(
          "距离完成度",
          ProductFormat.decimal(feedback["distance"].number("ratio").map { $0 * 100 }) + "%")
        item("训练负荷", ProductFormat.decimal(feedback.number("actualLoad")) + " 负荷单位")
        item("计划时长", ProductFormat.duration(feedback["duration"].number("planned")))
        item("实际时长", ProductFormat.duration(feedback["duration"].number("actual")))
      }
      Text("数值来自服务端训练反馈；缺失数据会等待运动分析发布。").font(.caption).foregroundStyle(RAITheme.secondary)
    }.padding(22).raiGlass().accessibilityIdentifier("feedback.card")
  }
  func item(_ title: String, _ value: String) -> some View {
    VStack(alignment: .leading, spacing: 5) {
      Text(title).font(.caption).foregroundStyle(RAITheme.secondary)
        .fixedSize(horizontal: false, vertical: true)
      Text(value).font(.title3.bold()).monospacedDigit()
        .fixedSize(horizontal: false, vertical: true)
    }.frame(maxWidth: .infinity, alignment: .leading)
  }
}

func feedbackStatus(_ status: String) -> String {
  [
    "completed": "已完成，等待可比较数据", "under_target": "低于计划", "on_target": "达到计划", "over_target": "高于计划",
    "mixed": "距离与时长表现不同", "skipped": "已跳过",
  ][status] ?? "等待分析反馈"
}
