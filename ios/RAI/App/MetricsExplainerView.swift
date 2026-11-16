import ProductCore
import SwiftUI

/// Per-viewer switch for experimental metrics (RAI VO₂, advanced running
/// diagnostics). Off by default; it only changes what is shown.
enum ExperimentalMetrics {
  static let key = "showExperimentalMetrics"
}

/// The one place that explains how the numbers are made and what they are
/// not. Cards elsewhere show a value and a one-line conclusion only.
struct MetricsExplainerView: View {
  var body: some View {
    List {
      Section("总的原则") {
        Text("所有结果都由你自己的服务器根据已同步的数据计算。数据不够或太旧时会留空，不会用 0 或平均值代替。")
        Text("修改心率设置、标注比赛或补传历史后，相关结果会自动重新计算，不需要手动操作。")
      }
      Section("跑力") {
        Text("跑力把你的能力换算成平坦路线、适宜天气下的马拉松等效水平：8 小时为 0 分，4 小时为 50 分，2 小时为 100 分。它是 RAI 自己的刻度，和其他品牌的分数不能直接比较。")
        Text("单次跑力只看这一次跑步，长期跑力综合最近的训练。两者的差值不等于当天状态。炎热、寒冷或缺少天气记录的跑步在长期跑力里权重较低。")
        Text("显示的范围是模型在不同假设下的可能范围，不是概率。")
      }
      Section("成绩预测与训练配速") {
        Text("成绩预测假设平坦路线、适宜天气、合理配速与补给，不是完赛承诺。没有长距离成绩时，半马与全马主要依据默认耐力假设。")
        Text("训练配速按长期跑力生成；训练计划只使用标为「计划可用」的区间，其余课次只给心率目标。")
      }
      Section("训练效果") {
        Text(TrainingEffectText.disclaimer)
      }
      Section("最大摄氧量") {
        Text("Apple VO₂ 估计来自健康 App，默认使用最近一直在记录的 Apple Watch。")
        Text(ProductReasonText.independenceNotice + " RAI VO₂ 为实验性估计，可在「我的 → 显示实验指标」中打开。")
      }
    }.navigationTitle("指标说明").raiScreen().accessibilityIdentifier("screen.explainer")
  }
}

/// A single quiet link to the explainer, placed at the bottom of metric pages.
struct MetricsExplainerLink: View {
  var body: some View {
    NavigationLink {
      MetricsExplainerView()
    } label: {
      Label("这些指标怎么算", systemImage: "info.circle").font(.footnote)
    }.tint(RAITheme.secondary).accessibilityIdentifier("metrics.explainer")
  }
}
