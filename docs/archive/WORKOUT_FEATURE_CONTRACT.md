# Workout Feature Foundation — M1 contract

2026-11-03（Asia/Shanghai）。算法 `rai.workout_features/v1`；artifact schema `rai.workout_features/1`；input schema `rai.raw_workout_snapshot/1`；policy `feature-policy/1`。与模型设计 §4/§13/§14/§17 配套。旧 `workout.features/v2`、TRIMP/v1、state/v2、recovery/v1、readiness/v1 的名称和语义不变。

本应用候选栈明确只为 running（activityType=37）给资格；其他活动保留 enum 和 primitives，但各运行模型资格为 unsupported_input。

M1 只生成 feature、逐模型资格和候选 artifact。**没有新的 AU、VO₂max、Running Ability、Recovery、Readiness、CS、Endurance 或 Running Economy score**。资格是输入支持门槛，不是模型已实现或生理效度已通过的声明。

## 1. Package boundaries

- `internal/workoutdomain`：有限数校验、球面距离、StepCount/分钟 cadence proxy 的纯原语。旧 analysis/detail 用兼容包装共用相同公式。
- `internal/workoutfeatures`：typed input、canonicalization、pause、source resolution、原生 HR/speed 支持、route grade、分析索引、持续窗口、稳态段、长跑 primitives、qualification。无 DB/HTTP/filesystem/系统时钟/随机数/可变全局 policy。
- `internal/storage/postgres/feature_*`：同一稳定快照的 input loader、artifact 写入、候选读取；复用已有 analysis run/publication transaction。
- `internal/jobs`：注册实际 `analysis.workout_features` handler，使用现有 claim/renew/fencing；没有未来模型的成功空 handler。
- `internal/admin/features.go`：显式单 Workout 排队、候选 inspect、HR reference 追加、promotion gate 预览。在线 import graph 不可到达 admin/migrations。

分析禁止消费 `workoutdetail` DTO、chart points、舍入配速、展示步频、格式化距离或本地化单位。后续 M2/M3/M4 必须消费已发布的本合同 artifact；原始 trace 的时间解释集中于本包。

## 2. RawWorkoutSnapshot

`DatasetID/WorkoutID/Generation/Revision/AsOf`、算法身份、显式 Policy 和 source fact 引用；Workout 的 start/end/source duration/activity type/可空 distance/energy；quantity facts（type/unit/value、可空 category、metadata、source epoch、TimeSupport）；statistics（sum/average/minimum/maximum、区间/单位）；sealed series 与 parent identity；sealed route 的完整 sequence/time/coordinates/accuracy/speed/course；events；activities（可空 end、configuration/metadata/statistics/events）；完整 source/device descriptor；追加 enrichment（source/version/observedAt/metadata/可空 totals/routeAvailable）；有效参数及取代/撤销 lineage；availability 和删除屏障引用。

状态 `State`：`present`、`missing`、`deleted`、`ambiguous`、`condensed`、`series_backed`、`unsupported`。缺失的可选量使用 pointer/null；未观察到 indoor 不等于 false。category 等当前算法不消费的事实仍保存在 typed snapshot，并显式 unsupported，不转成零值参与计算。

loader 使用现有 `LoadAnalysis` 的 **REPEATABLE READ / READ ONLY** transaction，20s transaction/snapshot budget、5s statement budget；同一快照读取 dataset revision、scope generation、Workout 和所有 children。`AsOf` 在第一次 generation/revision 读取后，由同事务 DB clock 捕获；它描述本次稳定可见输入，不提供历史数据库 time travel。参数按 Workout start 的 `[effectiveFrom,effectiveThrough)` 且 recordedAt≤AsOf 解析。旧 generation 只能记录 SUPERSEDED attempt，不把当前 facts 伪装为旧输入。

原始 import 只提供 provenance/coverage/metadata/哈希引用，**不从旧 bundle 恢复已删除的 sample/series/route 值**。上一成功 run 的 fact identities 只用于定位同 identity 删除屏障，使用主键关联，不扫描运动员健康历史。父删除由现有级联/屏障优先规则处理。

### Bounds

| 输入 | 上限；超限结果 |
|---|---|
| quantities + sealed series points | 250,000；`analysis_input_limit` |
| route points | 250,000；同上 |
| events + activities + statistics（包含 activity 内嵌详情） | 2,000；同上 |
| series / routes / enrichments / source epochs | 各 2,000；同上 |
| metadata/configuration/source descriptors 总字节 | 4 MiB；同上 |
| per-Workout observation/enrichment history | 各 2,000 行；同上 |
| 已读 observation JSON / provenance entries | 32 MiB / 250,000；同上 |
| 有效 HR-reference candidates | 256；同上 |
| elapsed / ephemeral second grid | 24h / 86,400 cells；同上 |
| artifact | canonical JSON 和 DB JSONB text 均 ≤128 KiB；显式失败 |

边界通过 limit+1 或累计计数检测。不会截断后计算；失败作业不能清理 generation 或保留 current reference。超预算的大 Workout 不影响旧 active 产品。

## 3. Canonicalization / time / source

Normalize 深拷贝输入，canonical JSON metadata，stable sort，完全相同的 logical record 去重。计算另按 physical time support 去重，因此相同读数的 UUID/series chunk 重分组不会增加积分或覆盖。route 以原始 sequence 排序，保留并排除时间回跳/重复 sequence 的连接。artifact Hash 采用应用层 canonical JSON SHA-256，不使用 JSONB 磁盘编码。

所有时间是绝对 Unix μs，内部区间统一 `[start,end)`。elapsed、source duration、active、paused、observed、reconstructed 分别表达；source duration 不冒充 active。跨午夜/DST 仍按绝对时间积分；本层没有按日评分或设备时区猜测。

每个 source epoch 是完整 canonical source revision/device descriptor 和分类的摘要。来源名称/版本/OS/device 改变可建立不同 epoch，不能解释为 fitness 改变。现有明确支持集合仍为 Apple Watch / associated Apple Watch；第三方或 unknown 原始事实保留但不进入本版支持时间。

同源相同支持函数合并；同源冲突与不同源重叠均排除对应时间，记录 `source_conflict` 和 ambiguous seconds，不静默平均。非重叠的 source epochs 可以存在于同场 Workout，但持续窗口不能跨 epoch，VO₂ Workout 资格拒绝混合 HR epochs。SignalSummary 保存 source epochs、`sole_supported_source` / `non_overlapping_source_epochs` / `conflicting_support_excluded` / `no_supported_source` 选择原因。

## 4. Pause timeline

pause=1 / resume=2 维护 manualPaused；motionPaused=5 / motionResumed=6 维护 motionPaused；只有两者均 false 才 active。lap=3、marker=4、segment=7 和 pauseOrResumeRequest=8 不改变暂停状态。父/活动复制的相同 transition 去重，重复无冲突 pause/resume 幂等。

首个 resume、同刻同状态通道 pause+resume、事件越界/反向、未知 event type、未闭合 pause，或重建 active 与 source duration 差超过 `max(2s,0.5% elapsed)`，均产生 `active_time_uncertain`，active/paused=null、不给出猜测的活动区间。elapsed/source duration 仍保留。手动与 motion 状态分别闭合，不能因为其中一个 resume 提前恢复 active。

## 5. TimeSupport / HR / speed / dynamics

`instant_point`、`interval_observation`、`condensed_parent_summary`、`sealed_series` 明确区分。sealed child 替代恰好自己的 parent；不删除原始 parent，也不把两者累加。

当前 wire 不带 HKQuantitySample.count。PG loader 仅在同 fact 的 source observation 证明该 type 的 series query 已 complete/no_records_observed 且 queryCompleted/localPersisted 时，才将无 child 的 scalar 当作普通时间观测。pending registration、series query failed、无相应证据的 parent 保守为 condensed summary。已有 iOS 会发送这些 coverage；M1 不修改 wire、采集或 ACK。coverage flag 本身不提供连续信号覆盖，只允许解释 sample representation。缺证据的旧记录可能不具备候选资格。

区间观测在与可靠 active 相交部分积分；同一 source、同一个连续 active segment、相邻 gap≤15s 的瞬时点才线性插值，无首尾外推、不跨 pause/source/大 gap。invalid point 是 barrier，不能删除刺点后跨过去插值。HR 的候选支持域为 `[30,220)` bpm；短于等于5s且跳变>30bpm的连接排除。speed 支持域 `[0.5,12]`m/s，≤5s 连接加速度>3m/s² 排除。工程阈值只过滤派生支持，不修改 raw，不是人体极限或诊断。

SignalSummary：state、time-weighted mean、30s 完整同源支持上的 rolling median 最大值（仅 HR）、observedCoverage、reconstructedCoverage、interpolatedFraction、maxGap（包含原生插值 gap 和未支持缺口）、excludedSeconds、ambiguousSeconds、source epochs/reason codes。覆盖分母为可靠 active 秒数；瞬时点数量本身提供零 observed coverage。

StepCount 的区间计数按支持区间换成 steps/min，名称严格为 `cadence_proxy`；不使用 native RunningCadence 名称。RunningPower、GCT、StrideLength、VerticalOscillation 提供同样的独立支持摘要，无 Running Economy score。

## 6. Distance / route / grade

优先 Workout totalDistance；缺失时仅在≥98%支持下使用 RunningSpeed integral，再考虑≥98% route distance。crossCheck 优先未被选作 preferred 的 speed integral，否则 route。保存 selected/cross-check source、差异比例和选择原因；零距离不做除法。速度不缩放成 totalDistance。完整 route 与 preferred 的差异另存 routeDifferenceRatio，>5% 阻止使用 route grade 的表现/VO₂候选。

route 连接要求原始 sequence/time 递增、同一 active segment、gap≤15s、两端 horizontalAccuracy≤20m、几何连接速度≤12m/s。未知/负/超阈值精度不通过；多条重叠路线不相加，标 ambiguous。raw route 永远不改。

grade 只在每条连续有效 route track 的完整、固定、不重叠 **100m** 邻域计算：前25m和后25m各至少5个 altitude/verticalAccuracy≤10m 的点；分别取海拔和距离中位数，距离中位数差≥50m；grade=海拔中位差/距离中位差。边界不外推；没有完整可靠邻域则 grade_unknown，不补0%。固定邻域确保密集/静止点不会触发 O(N²) 重复排序。它是正式设计中局部稳健邻域的本版确定性离散化；没有宣称厘米级高程或真实1%坡度精度。

native speed 与 route grade 支持做精确区间相交，窗口 flat gate 用 **共同支持距离**，不以时间比例代替距离比例。grade 覆盖和 route valid coverage 分开；可比 long-run halves 分别验证地形覆盖。

## 7. AnalysisTimeline / windows / steady / long run

AnalysisTimeline 包含 active intervals、pause boundaries、HR/speed native spans、route/grade support、source epochs 和 gap。1s cells 只是 bounded derived index，原生 span 的边界用解析积分；不存第二份 per-second DB。

固定 durations 为 180/300/600/1200/1800/2400/3600/5400 秒。起点为绝对整秒、终点=start+duration。每种时长只保存 best + 可选 non-overlap backup，先选择合资格支持，再按最大原生距离、最早起点打破平局；没有支持则 null。window 含 start/end/duration/distance/meanSpeed/coverage/quality/source epoch/input hash evidence/qualification。不能跨 pause、unsupported speed region、source/active-segment boundary；performance 额外要求原生 speed interpolation gap≤5s。

`5min hard + 2min recovery + 5min hard` 不会删掉恢复时间拼为10min hard；若提取实际10min elapsed 窗口，其中恢复慢跑的距离和时间仍全部计入。

从 Workout 第10min后按固定5min格提取非重叠 steady segments，暂停重新开始后3min排除，保存共同支持的 HR/speed、speed CV、HR linear trend、grade/coverage、pause contamination、environment 和资格。HR trend 目前要求完整连续同源 HR 支持，比95%门槛保守；M4 不得误把 segment 当成独立 athlete observations。M1 不计算氧耗或 VO₂。

LongRun 保存最长连续 speed duration；在≥60min连续支持中，排除开头10min后提供 elapsed 前后半的速度、CV、HR/coverage、grade/flat distance coverage 和 source epoch。HR epoch 跨越或不足时 HR=null，保留速度 primitives。没有“耐力好/差”解释。

复杂度：canonicalization/sweep/median 为 O(N log N)，grid 为 O(T+原生边界)，window 为固定8种duration的 bounded scans（备选再扫描一次），30s HR median 为常数窗口扫描。每个 Workout 只读自身 facts/有限历史描述，不扫描 athlete Workout history。

## 8. Qualification / reason codes

没有一个全局 `Valid bool`。每个模型分别 `eligible / conditional / ineligible`；每个 window/steady 也有独立资格。`eligible` 只表示本阶段支持门槛，无模型值。

| 模型 | 本版 gate |
|---|---|
| load | active 可定位、唯一有效 HR reference、HR reconstructed≥95%、max gap≤30s、无 ambiguous HR；大于1% active 的异常排除拒绝；持续 HR 与 reference 冲突拒绝 |
| VO₂ | 明确 outdoor、≥25min、热身后至少3个合格5min steady；speed134–300m/min、CV≤5%、HR coverage≥95%且 trend 可识别、trend≤1bpm/min、r=0.55–0.85、前后效率漂移≤5%、grade/flat distance≥90%、pause≤2%；weather缺失 conditional，grade未知 ineligible |
| performance | 至少一个20–90min合格连续窗口，源 distance cross-check≤5%，原生 speed gap≤5s，grade/flat distance≥90%，outdoor context；努力未确认，最高 conditional |
| CS | 至少一个3/5/10min合格窗口；始终保留 effort_unconfirmed/cross_workout_evidence_required，M1 不拟合 CS |
| endurance | ≥60min连续速度，热身后可比 halves、CV≤10%、各半 grade/flat distance≥90%、距离/来源/context门槛；缺HR可保留 pace primitives，资格 conditional |

冻结的机器 reason codes：

```text
missing_hr hr_coverage_low active_time_uncertain speed_gap
route_accuracy_low grade_unknown distance_mismatch pause_contaminated
source_conflict source_unknown duration_too_short environment_unknown
parameter_missing parameter_conflict workout_deleted invalid_observation
signal_outlier input_limit unsupported_input effort_unconfirmed
cross_workout_evidence_required indoor_unsupported future_model
distance_missing steady_support_low
```

future_model 保留给尚未实施的模型说明，不是成功 handler。输入或 artifact 超限为 job failure，不发布假“insufficient success”以掩盖截断。

## 9. Versioned inputs / artifact / evidence

`athlete_model_inputs` 本阶段仅注册 `hr_reference / rai.hr_reference/1 / athlete`。追加有效区间、provenance、ID/hash、supersedes 或 revoked；现有 input 不 UPDATE。supersedes 必须同 dataset/kind/subject 且指向先前存在行，不能自引用，FK + append-only 排除环。未有明确取代链的 overlapping inputs 为 parameter_conflict，不按 UUID/createdAt 选赢家；不把 legacy 最新参数追溯导入。

管理追加限制显式有效范围≤366日、最多1,000个受影响 Workout；同一 dataset 锁内写 input 和候选 dirty。input content hash 覆盖 identity、有效期、取代、provenance、typed payload。createdAt 是 DB 记录时间，进入 snapshot identity。人工参数的可信测量/年龄规则由未来模型另行验证，M1 不从年龄/性别猜 HRmax。

`WorkoutFeatures` 具 identity/activityType/timing（含绝对 start/end）/distance/HR/speed/route、performance windows、steady segments、long-run、running dynamics、environment、qualification/reasons/evidence。artifact 不含完整 HR/GPS/speed trace、chart points、全部滑动窗口或 raw metadata dump。

Evidence：input hash、canonical fact manifest hash/count、source epochs、参数 IDs/hash、config hash、feature version、generation/AsOf。run.lineage 追加 fact identity/hash/state 列表和参数有效期/取代引用；不重复存参数数值。M2 的非线性负荷需在共享 feature 层增补版本化、参数条件的 exposure primitive，不能用 meanHR 冒充积分，也不能另写 raw parser；M1 不提前计算新 dose/score。fact identity 包含 quantity type。不是只存一个无法定位事实的全库摘要；manifest 仅涵盖该 Workout。

`analysis_artifacts` 与既有 run FK 关联；每 run/artifact key 唯一、append-only；只允许该算法 RUNNING run 插入，terminal 后不能追加。结果经应用 JSON schema/version/有限数检查，DB 再检查 schema/dataset/generation/object/size。canonical hash 在应用中生成，读取时重建 typed JSON 校验 hash。

## 10. Candidate / publication / deletion / fencing

新 scope：`model/features-v1-92522339887e/workout/<uuid>`，stack suffix 来自 frozen policy 的 SHA-256；已有 scope 保留。`analysis_model_channels` 每 dataset 最多 active/candidate 各一行。显式 compute provision active=legacy/candidate=feature stack，并在同事务写 `admin_operations(feature_candidate)`；幂等 operation retry 不加 generation。没有全库 bootstrap、自动历史回算或自动部署。

候选 compute 启用后，新 Workout/quantity/association/series/route/metadata/provenance 的既有 Workout dirty 在同一事实事务转发到独立候选 generation；staged series/route registration 也只影响候选 availability。新 trigger 不改 legacy receipt body、commit revision 或原 analysis job identity。Daily scopes 不匹配这些触发条件。

候选 scope generation 增加立即 DELETE current `analysis_publications` 引用，并把显式下游依赖标脏，**不等待 Worker**。quantity delete、parent-series cascade、route delete、Workout delete、相关 parameter change 均覆盖；历史 artifacts 不删除。current inspection 单 SQL 验证 active dataset、candidate assignment/config、scope clean/generation、run succeeded 和 Workout 仍存在，跨 dataset 不可读。

发布复用同一 `PublishAnalysis` 事务和 dataset/job/dirty 锁：artifact → scalar results → publication → downstream dirty → scope clean → run terminal → job/attempt terminal；提交前以 clock_timestamp 重新检查 lease。旧 generation 或 assignment 失效只 SUPERSEDED，不能写 artifact/pointer 或 clean 新 generation。Worker 续租/取消复用原实现。

`analysis_model_dependencies` 指向未来 load/performance/vo2/ability/endurance scopes。raw dirty 与 feature publication 都可使其 dirty/invalidate reference；**不创建未实现模型的作业或成功回执**。

M1 promotion gate 在 DB 约束上固定 active=legacy；`promotion-plan` 返回 promotable=false / feature_only_no_product_consumer。future promotion/rollback 必须在有显式产品 consumer、完整模型 stack、freshness 和审计门槛后扩展；M1 不假装已实现用户指标通道切换。保留旧 active 是当前回退路径，不能直接把过期历史候选指针恢复成 current。

## 11. Administrative use / permissions

使用已有 owner/admin DSN 的离线 CLI，显式 dataset/workout/operation/actor：

```sh
rai analysis features compute --dataset <uuid> --workout <uuid> --operation <uuid> --actor <operator>
rai analysis features inspect --dataset <uuid> --workout <uuid>
rai analysis features hr-reference --dataset <uuid> --input-file <private-bounded-json>
rai analysis features promotion-plan
```

HR-reference 文件为 typed Parameter JSON（ID/effectiveFromUs/effectiveThroughUs/restingHR/maxHR/provenance/可选supersedes/revoked），≤4KiB；命令不回显参数数值。inspect 是显式管理读取，不注册正式 HTTP/iOS DTO。生产 DSN/真实数据操作不属于本阶段授权。

`rai_api` 无候选 artifact/input/channel 写权限，也无 artifact 读权限；通过限定 scope、固定 search_path、不可 PUBLIC EXECUTE 的 SECURITY DEFINER trigger 做候选 invalidation。`rai_worker` 只能 SELECT inputs、INSERT artifacts，不能写 raw、改 channel 或更新 artifact/input history。admin/owner 管 migration 和 inputs。没有新 sequence；没有第二套生产锁。migration 21 不提供破坏性 Down，既有 checksum gate 明确拒绝回滚，原始事实和不可变历史保留。

## 12. Validation limits

合成 unit/property/fuzz、真实隔离 PG18 的 snapshot/事务/权限/删除/fencing/crash、Go/Daily/Workout/training/Swift 回归和性能矩阵见 `M1_RESULT.md` 与机器证据。真实 GPS grade、真实 eligibility 分布、参数测量可信度、生理效度、生产负载、WAN/断电均未验证；本合同不得被用作新用户指标上线许可。
