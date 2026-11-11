# 当前架构（以代码为准）

## 总览

| 层 | 内容 |
|---|---|
| iOS | SwiftUI，iOS 18，5 个 Tab（首页 / 运动 / 训练 / 分析 / 我的）。`ios/RAI/App` 界面与同步编排；`Sources/SyncCore` 同步协议与本地状态；`Sources/ProductCore` 产品读取与展示模型。只读 HealthKit。 |
| 服务端 | Go 模块化单体（`apps/server`）。`api` 与 `worker` 同镜像。API 只做鉴权、原始字节入库和读取；Worker 解析原始载荷并运行分析任务。PostgreSQL 18，pgx/v5，sqlc。 |
| 部署 | `deploy/release/compose.yaml`：postgres + api + worker + admin。 |

## 一条跑步记录的路径

1. **发现**：App 启动、回前台、HealthKit 的 Workout 观察者唤醒时，`SyncModel.autoSyncWorkouts` 读取最近 30 条 Workout，
   用 `POST /v2/sync/workout-ingests/status` 与服务端对账，再按 `WorkoutAutoSyncPolicy` 选出要传的记录
   （跑完 ≥10 分钟、14 天内、未上传或早传过一次需补传、非服务端 rejected）。
2. **构建**：`RawStreamSyncEngine.build` 一次读全 HealthKit，生成 protocol-6 JSON（顶层来源 + 流 + 路线）。
3. **上传**：`WorkoutIngestDelivery.acceptWithRetry` 以 gzip multipart 发 `POST /v2/sync/workout-ingests/{uuid}`；
   网络错误 / 408 / 429 / 5xx 按 1s、3s、8s 重试（尊重 `Retry-After`）。
4. **接收**：API 把原始字节原样写入 `workout_ingests`，返回回执（同一事务）。字节相同的重复提交重放原回执；
   被拒绝或字节不同的新快照会替换原文并重新解析。
5. **解析**：Worker 单槽领取 `accepted` 行，只投影已知字段为事实（样本、关联、序列、路线），
   在同一事务里写入事实、递增该训练的脏范围代数（`workout:HKWorkoutTypeIdentifier:<id>`，触发 v2 日模型栈失效）
   并把行标为 `committed`；不再排队任何旧分析任务；暂时性错误按指数退避（上限 10 分钟）重试，
   语义错误立即 `rejected`。
6. **分析**：Worker 只处理两种 job kind：`analysis.model_stack_v2`（日模型栈）与 `training.plan`。其余旧 kind 领取即以
   `analysis_unsupported_algorithm` 失败；迁移 53 把遗留的排队任务标为 `retired_pipeline`。每次领取任务和每 15 秒的日推进都重新读取时钟，
   所以跨午夜后“今天”会正常推进。
7. **展示**：App 读取 `/v2/product/overview`、`/v2/workouts`、`/v2/analysis/athlete` 等；首页并发读取 4 个接口，并缓存上一次快照。
   所有读取（运动列表与详情的单次负荷、首页周负荷、训练计划输入）都来自 active 通道的 v2 模型栈。

旧分析链路（`analysis.workout` / `training_state` TRIMP、v1 候选模型任务、`analysis.sample`/`series`）已在服务端删除
（迁移 53）；`trainingload`、`recovery`、`readiness`、`performance` 包内 v1 纯函数保留为冻结基线及其测试，不再被任何任务调用。

## 模型（v2 日模型栈）

`analysis.model_stack_v2` 每个自然日一个任务（`storage/postgres/model_stack_inputs.go` `computeModelStackDay`），
读取近 186 天跑步与日健康事实，在同一事务里发布负荷状态、跑力、VO2、恢复、准备度。身份由
`modelmanifest.Current()` 的哈希（`StackID`）决定；算法或策略变化即新 stack，需要用 `rai analysis model-stack`
的 `prepare-real` / `activate-real`（回滚 `rollback-real`）切换，旧 stack 结果保留。

- **单条训练隔离**：某条训练自身事实无法解析、或参考行无效时，只把这条记为未知负荷
  （`workout_invalid_input` / `reference_invalid`），当天其余产物照常发布。
- **来源身份**：v2 栈的训练与日健康来源 epoch 取 `HealthKitSource.StableIdentity()`（bundle、productType、
  硬件），不含 App/系统/固件版本与 HRV 样本时长，watchOS 升级不会重置基线或拆分跑力证据。
- **心率参考**：`hr_reference` 按修订视图生效——只要在计算时刻已录入，就覆盖其生效日期之后的历史训练。
  多条参考时生效日期最新的那条覆盖之前的（同一时刻值不同才算冲突）；最大心率不过期，测量超过 365 天只给
  `hrmax_review_due`。未填静息心率时，每条训练用其前 28 天的静息心率中位数（单独按训练窗口加载）。
- **负荷 v2（有界策略）**：HRR 截断到 [0,1]（超最大心率按 1 计、低于静息心率按 0 计并记原因码），
  质量分级 `qualityTier`：complete（覆盖 ≥0.98 且最长断点 ≤30 s）/ estimated（≥0.80 且 ≤180 s）/
  approximate（全程均值心率）/ unknown。负荷状态中未知日权重不超过 ATL 0.15、CTL 0.10 时按 180 天环内
  已知训练日均值插补（`imputed`），超过才为空。
- **准备度 v3.2**：任一质量等级的已知负荷都算已知；未知训练按典型训练日负荷估值，衰减后折合不超过
  0.15 个典型训练日才出分（置信度相应降低），否则 `recent_load_unknown`。负荷乘数与“习惯负担”比较：
  近 28 天同一时刻衰减负荷的中位数（下限 0.5 个典型训练日），不高于习惯时为 1，每高出一个习惯减半，下限 0.35。
- **恢复 v2.2**：每晚按来源优先级（Apple Watch > 其他 Apple 设备 > 第三方 App）只取一个来源，同来源多个静息心率取最晚；
  夜间 SDNN 取前一晚 20:00 至当日 12:00 内与睡眠重叠的样本。分数算法不变，另给三档 `deviationLevel`
  （normal / mild_deviation / marked_deviation，连续三晚偏离升一档），App 在“生理恢复”下显示。
- **跑力 v2.2**：性能窗口用 Minetti 坡度代价换算为平地等效速度（坡度 ≤8%），不再要求 90% 平路；
  每个时长只取最快 3 条训练做中位数拟合，其余标为 `below_envelope`。
- **RAI VO2**：窗口平均坡度 ≤5% 时按同一坡度代价换算；0–30 °C 内保留证据，5–25 °C 外权重 ×0.7，湿度 >85% ×0.8。
- **Apple VO2**：来源选择按稳定来源身份匹配（`VO2Provenance.StableEpochID`），系统升级不需要重新选择；App 从
  `/v2/analysis/vo2max-sources` 列表中选择。
- **无心率负荷**：心率缺失或覆盖不足的跑步，在有跑力 v60 且近 90 天有 ≥5 条完整心率负荷可标定时，按
  `k · 活动分钟 · (平均速度/v60)²` 估算（`approximate`，置信度 0.5）。
- **心率设置**：App 只在「我的 → 静息与最大心率」录入 `hr_reference`（默认生效日期为半年前以覆盖历史），负荷、准备度、
  VO2、训练详情心率区间共用；有观测最大心率高于设置时给出一键采用建议。
- **单次结果缓存（`model_workout_results`）**：每条跑步按 (数据集, stack, 训练) 存特征、心率暴露与最新 v2 负荷，
  以该训练脏范围代数 `fact_generation` 为键。代数相同且特征 schema/StackID 匹配即直接复用，不再读原始采样；
  事实变化递增代数后下一次发布重算。负荷只在发布“今天”时写入。运动列表、详情、训练计划、观测最大心率读这张表。
- **负荷状态增量**：当天 `load_state` 产物带 `checkpoint`（ATL/CTL 核与前 7 天值、[D-185, D] 负荷账本哈希）。
  下一天若前一天 checkpoint 存在且账本哈希一致，只推进一天；否则按 186 天全量重建。两者结果一致（单测 1e-9）。
- **训练计划（`training_plan/v2`）**：只读 v2 信号。课次带配速目标（v60 的 E/T/I 区间）与心率目标（心率储备比例）；
  急性/慢性 >1.3 时第二周不递增。**当日重排**：v2 准备度低于 50 或急性/慢性 >1.5 时，若今天是 TEMPO/INTERVAL/LONG，
  今天改为 RECOVERY（距离取被替换的 EASY 日），原课次移到本周之后第一个“次日不是高强度”的 EASY 日；
  找不到则取消该强度课；两天都带 `daily_readiness_reschedule`，其余日期不变。active stack 每次发布今天的结果都会
  标记训练计划重算，所以早上恢复/准备度数据到达后课表随之调整。
- **日模型栈内存**：只保留近 76 天（VO2 窗口）训练的原始采样，更早的训练算完特征即释放，2M 点上限只按保留部分计。
- **可用性统计**：`rai analysis model-stack health --dataset <id> [--days 90] [--channel active|candidate]`
  只输出各产物的发布天数、状态/原因码计数，以及按当前代码对窗口内训练和日健康的试算计数，不输出任何健康数值或标识。

## 日常健康与 VO2

HRV、静息心率、睡眠、VO2 走 HealthKit 锚点 + 本地 Outbox（`OutboxStore`）。启动、回前台、HealthKit 观察者唤醒时
扫描变化，有待交付记录就自动上传日常健康（后台 URLSession，见 `BackgroundTransfers`）；「同步日常健康」按钮仍可手动触发。

## 服务端目录

- `cmd/rai`：入口与运行模式；`internal/httpapi`：路由；`internal/syncing`：协议、校验、限额（内存令牌桶 `Limiter`）；
  `internal/storage/postgres`：事务与查询；`internal/jobs`：Worker；其余为分析算法与训练计划（`trainingload`、`recovery`、`readiness`、`vo2max`、`training` …）。
- 管理工具（迁移、新建数据集、运维）只在 `internal/admin`，不得被 `application/httpapi` 导入（`scripts/check-boundaries.py` 检查）。

## 约束

- 原始载荷落库、服务端只解释已知字段、未知字段保留不丢。
- 不做删除同步：v4+ 客户端不上传删除，服务端不根据快照缺席推断删除。
- 同步不依赖备份、恢复、迁移、全库摘要或全量重算。
- 个人自用：单用户、单服务器；请求限额只在进程内存里，重启即重置。
