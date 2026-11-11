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
   （跑完 ≥1 分钟、14 天内、未上传或早传过一次需补传、非服务端 rejected）。后台观察者唤醒不等这 1 分钟，
   直接上传当时可见的数据。跑完 30 分钟内传过的记录，在首传 30 分钟后再传一次以带上晚到的心率与路线
   （字节不同服务端才替换并重新解析；每次运行每条记录只补传一次）。还没到时间的记录由一次性定时器到点再查，
   不用等 15 分钟扫描。
   **跑步与日常健康是两条独立通道**：跑步批次用 `workoutScheduler`，日常健康用 `syncScheduler`，互不取消、互不等待；
   服务端同一设备有 2 个跑步槽和 1 个日常健康槽，进程总上限 3（`httpapi.acquireUpload`），握手公告各自额度。
   同一会话的状态查询、跑步与日常健康共享 60 秒内存握手缓存及在途请求；凭证/服务地址/会话变化隔离缓存，
   鉴权或身份冲突清除缓存，每次使用仍检查本地 active 状态。激活与恢复始终实时握手。
2. **构建**：`RawStreamSyncEngine.build` 一次读全 HealthKit，生成 protocol-6 JSON（顶层来源 + 流 + 路线）。
3. **上传**：最多 2 个 worker 各自读取、构建一条记录；旧服务端未公告 `workoutUploads` 时使用 1 个 worker。
   `WorkoutUploadJournal` 先原子保存 multipart 文件、边界、内容哈希和请求身份，`BackgroundTransfers` 再通过后台
   URLSession 的文件上传发 `POST /v2/sync/workout-ingests/{uuid}`。网络错误 / 408 / 429 / 5xx 按 1s、3s、8s 重试，
   更长 `Retry-After` 的单次等待上限 30 秒，所有尝试复用同一文件与身份。Workout 不占用 Daily/VO2 的 GCRA 节流器。
   挂起/取消进程内等待不会取消已交给系统的传输；回调原子落盘后验证 UUID/回执，再持久化 journal 中的 accepted
   回执并回收正文与传输结果。重启/回前台只消费已有回执；再次同步未完成项时接回原文件，不重新读取 HealthKit。
   选择列表不会全部预先生成文件；后台构建受 iOS 执行时间限制，尚未构建的历史在后续前台轮次继续。
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
- **恢复 v2.3**：每晚按来源优先级（Apple Watch > 其他 Apple 设备 > 第三方 App）只取一个来源，同来源多个静息心率取最晚；
  夜间 SDNN 取前一晚 20:00 至当日 12:00 内与睡眠重叠的样本。分数算法不变，另给三档 `deviationLevel`
  （normal / mild_deviation / marked_deviation，连续三晚偏离升一档），App 在“生理恢复”下显示。
  删除记录（墓碑）不再阻断出分：被删样本本就不参与计算，同步中途的改写由日覆盖度门槛拦住。
  只有在当前视图已生效、且与评估日窗口（前一晚 20:00，或当晚睡眠段开始时间如果更早，到当日结束）重叠的墓碑，
  才记 `input_deleted` 并把置信度乘以 0.7；只涉及历史的墓碑只通过剩余事实影响基线。所有墓碑仍进入 `TombstoneRefs` 与输入哈希。
- **跑力 v2.2**：性能窗口用 Minetti 坡度代价换算为平地等效速度（坡度 ≤8%），不再要求 90% 平路；
  每个时长只取最快 3 条训练做中位数拟合，其余标为 `below_envelope`。读取投影 `Freshen` 只对曾有曲线的结果在证据满
  28 天时改为 `stale`、满 42 天时改为 `insufficient_data`；从未有曲线的 `insufficient_long_evidence` / `collecting`
  保持原状态，只更新 `freshness` 并追加 `stale` / `evidence_expired` 原因，产品状态显示“积累中”而不是“已过期”。
  运动详情的 `modelQualification` 给出每次跑步是否被采用为跑力证据和已知负荷及原因。
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

### 模型发布兼容性

发布读取配置保存在 `internal/modelrelease/releases.json`，通过 `modelrelease.Lookup` 按完整 manifest 哈希与
StackID 匹配。当前注册范围如下；注册表之外的版本明确返回不支持，不会尝试套用最新配置。

| 来源提交 | StackID | 读取能力 |
|---|---|---|
| `caca7f4` | `rai-v2-e58051bacdee98c9` | 冻结配置读取 |
| `975b912` | `rai-v2-654b005210f66d0e` | 冻结配置读取 |
| `ccbe328` | `rai-v2-4a543690d2c37290` | 冻结配置读取 |
| 恢复 v2.3（评估日墓碑） | `rai-v2-b2d165ffd1b0254a` | 当前构建的计算配置及读取 |

产品当前值、历史和训练输入按 active stack 的 `model/<stack>/athlete/day/v2/<date>` 路径定位产物，校验
该发布的真实计算配置、manifest、dataset、generation 和内容哈希。只读取发布指针与脏范围代数一致的干净结果；
已失效的发布不能继续提供数值。当前值读取当天日发布，历史读取指定日期范围；当天产物缺失不会自动用前一天冒充。

**失效与历史重算（迁移 55）**：日常健康事实逐行写入，但失效按“数据集 + 事务”合并——行触发器只把时间范围并入
`model_stack_pending_invalidations`，提交时由延迟触发器在同一事务内执行一次 `rai_model_stack_invalidate`，所以一页
（最多 500 条）只产生一次失效和一个当天任务。覆盖度行（每页都会改写，含空页心跳）只重排当天，不再让过去 366 天失效。
晚到的事实（昨晚的睡眠、第二天才上传的跑步）仍会让已发布的历史日失效；Worker 每 15 秒的推进在该数据集没有排队或
运行中的日任务时，调用 `rai_model_stack_rebuild_history` 把最近 365 天内曾发布过、现已失效的日范围按从新到旧每次最多
10 个重新排队，算完后历史恢复可读。只重算当前构建的栈；从未发布过的日期不会被创建。
准备度投影使用对应版本的配置和有效期，旧结果不改标为新模型。注册表冻结的是读取契约，Worker 仍只计算当前构建的栈；
旧栈缺失或失效的产物提示 `model_update_required`，不能用新算法写回旧栈。

部署镜像不改变 active assignment。模型升级沿用以下管理路径，均不由 App 或 API 自动执行：

1. 用 `rai analysis model-stack identity` 获取当前构建的 StackID 与完整 manifest 哈希。
2. 经既有数据集、栈及 manifest 绑定的所有者授权，用 `prepare-real` 注册候选栈并排队当天日模型任务。
3. 等待负荷状态、跑力、RAI VO₂、外部 VO₂、生理恢复、训练准备度六项产物发布；即使某项证据不足，也必须有真实计算结果。
4. 用 `activate-real` 走现有授权与确认参数。事务内验证候选产物及重建状态，再原子切换 active 并保留 previous；
   未就绪或计算失败时拒绝切换，候选产物不能提前进入产品读取。
5. `rollback-real` 同样需要授权，并按目标旧版本校验当天干净产物。保留 previous 不代表它永远满足回滚条件。

候选计算独立于同步，不要求客户端重传已提交事实。新增配置必须增加新的不可变发布描述，保留已支持条目；
`modelrelease` 测试约束当前 manifest 与实际计算配置一致。旧提交生成的合成载荷夹具验证 JSONB 重排后的哈希兼容性；
真实隔离 PostgreSQL 回归覆盖候选隔离、未就绪拒绝激活、切换、旧栈回滚及训练输入读取。

已激活模型的缺失、失败与不支持状态见 [API 状态表](API.md#运动模型状态)。iOS 先展示缓存再刷新；
网络、解析或未知顶层 schema 导致的刷新失败保留上次成功结果，并显示该结果的时间和重试提示。
明确的 `not_product_activated` 会清除缓存模型，避免重启后恢复已撤销的展示。上述自动化回归不代替真机与线上验收。

## 日常健康与 VO2

HRV、静息心率、睡眠、VO2 走 HealthKit 锚点 + 本地 Outbox（`OutboxStore`）。启动、回前台、HealthKit 观察者唤醒时
扫描变化，有待交付记录就自动上传日常健康（后台 URLSession，见 `BackgroundTransfers`）；「同步日常健康」按钮仍可手动触发。

- **一轮交付**（`SyncCoordinator.syncDaily`）：按时间预算运行（前台 60 秒，观察者后台唤醒 20 秒），HRV、静息心率、睡眠、VO2
  轮流各交付一页，长时间的 HRV 补传不会饿死睡眠。每次上传前经 `UploadPacer`（与服务端相同的 GCRA，按握手
  `limits.requests.upload.device` 配置，留 5% 余量）；收到 429 时按 `Retry-After` 推后节流器，并在预算内原地等待后用同一
  batchID 和同一文件重试，超出预算才留到下一轮。
- **续传调度**（`RAIApp.scheduleDiscoveryScan`）：取各流 `nextOpportunity` 的最早时间——有待交付页就取其 `nextAttempt`
  （即 Retry-After），有到期发现就 2 秒后，否则 15 分钟兜底。限流、容量、超时、网络错误属于暂时性错误
  （`DailyHealth.isTransient`），只推迟该页、不阻断快速续传；其他错误仍等 15 分钟扫描。
- **分页**：页大小取握手 `dailyMaxChanges` 并持久化（服务端 500，未握手按 128）。500 条睡眠页约 257 KB，本机 PG 18 上
  500 条新增约 0.6 秒、500 条删除约 0.5 秒提交，远低于 5 秒事务预算（`integration/daily_test.go`）；已启用模型栈且存在
  120 天日范围时，跨 90 天的 500 条历史页约 0.5 秒、只排一个日任务（`integration/model_stack_invalidation_test.go`）。迁移 54 把
  `daily_health_coverage` 的计数约束放宽到 500。
- **空页心跳**：已完成 bootstrap、没有新增或删除的查询，距上次服务端 ACK 不足 6 小时就只在本地记录发现时间、不上传；
  到 6 小时才上传空页刷新覆盖度（恢复要求覆盖度在 24 小时内）。锚点不前移，下次从同一锚点继续查。
- **回溯**：仍为 90 天。恢复窗口只要 30 天，但恢复历史和较早跑步的静息心率回退（训练前 28 天中位数）需要更早的数据。
- **进度**：同步页按类型显示“历史补传中/等待交付/已同步”和最近一页时间；暂时性错误显示“稍后自动继续”。

## 服务端目录

- `cmd/rai`：入口与运行模式；`internal/httpapi`：路由；`internal/syncing`：协议、校验、限额（内存令牌桶 `Limiter`）；
  `internal/storage/postgres`：事务与查询；`internal/jobs`：Worker；其余为分析算法与训练计划（`trainingload`、`recovery`、`readiness`、`vo2max`、`training` …）。
- 管理工具（迁移、新建数据集、运维）只在 `internal/admin`，不得被 `application/httpapi` 导入（`scripts/check-boundaries.py` 检查）。

## 约束

- 原始载荷落库、服务端只解释已知字段、未知字段保留不丢。
- 不做删除同步：v4+ 客户端不上传删除，服务端不根据快照缺席推断删除。例外：日常健康 `daily-facts`（HRV、静息心率、睡眠）
  继续上传 HealthKit 锚点查询返回的删除，服务端写墓碑并隐藏对应事实；恢复模型对墓碑的处理见上文“恢复 v2.3”。
- 同步不依赖备份、恢复、迁移、全库摘要或全量重算。
- 个人自用：单用户、单服务器；请求限额只在进程内存里，重启即重置。
