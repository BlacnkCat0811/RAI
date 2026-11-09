# P4 Algorithm Contract

冻结于 2026-10-28；P4_BASE_COMMIT=8e40573e027ac6dd9aac871fb755051cb31c4657。
P3 REAL DEVICE ACCEPTANCE: DEFERRED BY USER（仍 NOT VERIFIED）。
P4 DEVELOPMENT EXCEPTION: AUTHORIZED BY USER。

## 执行与身份

Go 纯函数；显式版本；float64，UTC 微秒输入，输出不允许 NaN/Inf。canonical JSON（有序 slices、Go map key 排序）SHA-256 只覆盖 scope 输入及 lineage，不摘要全库。输入在同一 REPEATABLE READ 事务读取；target generation 必须等于该快照代际，不能将新事实标成旧代际。run 完成后及 result 不可修改，重试使用独立 attempt/run；逻辑 identity 是 dataset/scope/algorithm/version/generation/input_hash，唯一约束。当前结果只经 publication 查询。

## 算法

| name | version | inputs | outputs / units | window |
|---|---|---|---|---|
| workout.features | v2 | durable Workout、approved enrichment、显式关联 quantity/已 sealed series、sealed route | duration s、distance m、speed m/s、pace s/m、HR count/min、cadence count/min、power W、coverage ratio；route distance/elevation m | 一个 Workout |
| training_load.trimp | v1 | features、显式 parameter set：resting HR、max HR、A、B、provenance | load AU、method、missing components | 一个 Workout |
| fitness_fatigue | v2 | published TRIMP、dataset timezone、daily window | daily load AU、fitness/fatigue AU/day、balance AU/day | 每日有限 180 天核 |
| recovery | v1 | 同日 fitness/fatigue | load recovery ratio（0..1） | 同上 |
| readiness | v1 | fitness/fatigue、recovery、缺失组件 | load-only score（0..100），组件和缺失项 | 同上 |
| dependency.invalidate | v1 | sample/series scope | 已验证依赖传播的执行记录；不生成健康评分 | 当前 scope |

Workout 与 TRIMP 同一 handler/run bundle（workout.bundle/v2）原子发布，结果每项带实际算法版本；state bundle（training_state.bundle/v2）发布日序列。当前 bundle 固定所列组件版本；旧 v1 历史保留，不能改变组件语义却沿用旧 bundle 标签。

## Features / Missing / Quality

缺失 value=null / quality=insufficient，绝不填零。duration 来自 durationUs；distance 优先原始 totalDistance，其次一致的 append-only enrichment；不从步数推断。speed=distance/duration，pace=duration/distance，仅正分母。summary statistics 的 average/maximum 可用但 coverage 未知（partial），不冒充完整轨迹。

显式关联 samples 为轨迹输入；不使用时间重叠猜测关系。sealed series 替代其 parent summary，pending series 不进入分析。每个 quantity interval 裁剪到 Workout 范围，瞬时点只代表该点，coverage=0；不外推。不同来源/重叠 intervals 为 ambiguous，均值仅算互不重叠数据；有重叠则该轨迹均值 unavailable（不悄悄合并）。coverage 为区间并集/Workout elapsed time，完整阈值 0.9；部分轨迹不能外推整个 Workout 的 TRIMP。summary average 可用于展示，但 TRIMP 仍要求 >=0.9 HR coverage。无 cadence/power/route 均 unavailable。

route 仅 sealed，按 route identity、sequence 排序；每条内计算球面 haversine（R=6371008.8m）。只连接时间严格增加且相邻 <=30s、horizontalAccuracy 均存在且 <=25m 的点。高度差仅当两点 verticalAccuracy 均存在且 <=10m；未做地形平滑，不称测绘爬升。coverage=有效连接秒数/elapsed，封口不代表 GPS 全覆盖；缺精度信息不假装可靠。不跨 route 连接。不实现 splits、HR zones（无可靠 zone 配置）。

## TRIMP

公式：D_minutes × r × A × exp(B×r)，r=(meanHR−restHR)/(maxHR−restHR)。不隐式推断 sex，不使用年龄预测 HRmax。A/B 必须由显式管理命令及来源提供，参数版本进入 hash/lineage。个人参数缺失、HR coverage<0.9、均值缺失、active duration 与 elapsed 不一致（尚未按暂停事件切分心率）则 unavailable，稳定 missing reason；HR outside [rest,max] 是 invalid_input，不 clamp、不改 raw。不提供隐藏 fallback。

公式来源类别：公开训练剂量模型；[原始研究使用 TRIMP 的公式说明](https://journals.physiology.org/doi/full/10.1152/japplphysiol.00482.2003)、[Banister 等跑步双响应模型](https://pubmed.ncbi.nlm.nih.gov/2246166/)。这里只采用公式类别，以下时间常数与评分是 RAI 自有工程定义，未经个人生理效度验证，与 Garmin/Firstbeat 无等价声明。

## State / Recovery / Readiness

使用 dataset.timezone（IANA，禁止 OS 隐式 timezone），Workout 按 start 所在日归属。不声称覆盖 HealthKit 未读历史。每日 load 为该日所有 durable Workout 的 published TRIMP 之和；任一缺 load，则该日 load unknown，受影响 180 天所有高层值 null。无记录的日=0 recorded load（明确没有导入完整性保证）。窗口无任何 Workout 则 insufficient，不能输出 readiness=100。

有限核：F(d)=Σ[k=0..179] (1−exp(−1/42)) exp(−k/42) L(d−k)；A(d) 类似 tau=7；balance=F−A。180 天外贡献按模型定义为零，这不是无限 EWMA 的近似续算。previousState 明示 not_used_finite_kernel；计算通过有限核保证任意增量/分块/全算一致，不依赖旧算法 checkpoint。已知完整 load 的输出 quality=partial（个人生理模型与导入覆盖尚未校准）。

recovery=max(0,min(1, F/max(F,A)))，只有 F>0；表示相对负荷恢复比例，不能称完整身体恢复。readiness=100×recovery；provenance 同时保存 F/A/balance/recovery 与 missing sleep/HRV/resting-HR trend、coverage unverified。这个 load-only 指标不是训练处方或疾病判断，不因只有距离上次运动的小时数生成评分。

增量：T 的事实变更只影响 T..T+179，截断至显式 as-of 当天；日常按 dirty range，输入再向前最多179天。管理 recompute 显式 dataset/scope/algorithm/from/through（最多366输出日），不默认全库；state 若仍有 pending dirty，显式范围必须覆盖其受影响输出日，否则拒绝，不能清掉范围外待办。较长历史通过显式多个范围完成。daily roll-forward 由 Worker 有界维护现有 training-state scope，每个 dataset 一日最多一个新 generation。首次导入不自动全历史回算，只发布当前180日模型；历史查询需 explicit recompute。

## 事务与故障

结果写入、run terminal、publication pointer、generation clean、job/attempt complete、下游 dirty 在同一事务。dataset → job → dirty 的锁顺序；publication 用当前有效 fencing token 与 clock_timestamp 验租约，最后再次校验。新 generation 到达旧 run 可存 SUPERSEDED，但不能发布/clean。Workout 删除查询立即隐藏旧结果；依赖变脏时 state 查询隐藏旧发布。

P3 输入 blocker：原 sample 删除级联 association 后未 dirty Workout；sealed series 未 dirty 关联 Workout。新增 migration 仅补依赖 dirty/job，保留 wire/anchor/batch/route-seal/receipt 身份。样本批次原每 UUID 建 job，1000 sample 产生1000 jobs；当前新增及已知删除（含混合页、全删除页）按同 type/UTC 日合并依赖任务；未知删除无可用旧范围时只建屏障，回执仍为 queued + 原 job IDs（数量不固定，旧回执不变）。不增加第二队列。

只保存稳定 error codes。暂保留所有 runs，不做 retention。没有 P5 计划、教练或排程。


## 2026-10-29 Correctness addendum — workout.bundle/v2

新发布使用 workout.bundle/v2，workout.features/v2；training_load.trimp/v1 不变。P3 raw point、sequence、chunk、manifest、seal 与 receipt 不变。坐标/范围/有限数值仍校验；同一 route 的相邻时间不递增仅使该 connection 不参与距离/高差/coverage，绝不排序、删除或改写 raw。其他合法连接继续累计；route_distance 有合法连接时 partial，否则 null/insufficient。details 暴露 validConnections 与 skippedNonIncreasingTimeConnections，coverage 的 0 是已观测有效连接比例，不代替缺失距离。Workout 的 duration/distance/HR/load 独立计算并正常 publication；旧 pointer 仍遵循 dirty 隐藏规则，成功新代际发布后恢复 current，不重写旧 run。相同 raw 可能得到不同结果，因此升级 bundle/features；旧 v1 历史保留。显式 CLI 只接受当前版本，旧请求需明确改用 v2，不自动以旧版本标签执行新语义；不自动全历史重算。

## 2026-10-29 Correctness addendum — training_state.bundle/v2

新 state publication 使用 training_state.bundle/v2、fitness_fatigue/v2；recovery/v1、readiness/v1 公式不变。daily_load 仅由当天 durable Workout 的 published load 决定：当天任一缺失则 null，无记录为 0 recorded load（不证明导入覆盖完整）。历史 180 日 unknown 继续使 fitness/fatigue/balance/recovery/readiness unavailable；窗口完全无 Workout 也不生成恢复/准备度。日与窗口可用性相互独立；历史缺失解决后按原有限核重新计算。相同 raw/lineage 的 daily_load 可能改变，因此升级 bundle 与组件，不复用 v1 标签；旧结果和 pointer 历史不重写。CLI 需明确当前 v2，不自动重算历史。旧 v1 golden 保留为已知数值基线，v2 对原完整输入的数值继续比对。

## 2026-11-09 Correctness addendum — workout.bundle/v3

新发布使用 workout.bundle/v3、workout.features/v3；training_load.trimp/v1 公式不变，training_state.bundle/v2 不变（其 Workout 输入同时接受 features v1/v2/v3）。升级原因：HealthKit 的 HR/Power 样本通常是瞬时点（start=end），v2 只按区间长度累计 coverage，完整同步也会得到 0 coverage，进而把平均 HR/Power 与 TRIMP 标成 partial/insufficient。v3 修正该语义，不伪造原始事实，也不改变缺失即 null 的规则。

- 瞬时采样支持区间：只有点的数值，没有持续时间。v3 将同一 Workout 内按时间排序的瞬时点，从当前点延伸到下一点，仅当下一点间隔 ≤15 s 时计为支持区间；更宽的空隙不外推。同一时刻出现不同数值仍视为歧义，均值保持 null。区间的 mean 仍按持续时间加权；只有点而无区间时按点均值；coverage 为支持/观测区间并集除以 Workout 时间跨度，封顶 0..1。该规则只影响 coverage 与加权，不读取、排序或改写 raw。
- 步频代理：HealthKit SDK 没有跑步步频 quantity 标识，原生 `HKQuantityTypeIdentifierRunningCadence` 仅在未来/第三方数据出现时使用。无原生步频时，v3 用关联的 StepCount 区间计算平均 steps/min：每个区间按与 Workout 重叠比例计入步数与秒数，均值=总步数/总秒数×60；coverage 取步数区间的并集。`details.method=step_count_proxy`、`details.derivedFrom=HKQuantityTypeIdentifierStepCount` 明确标注代理来源，不冒充原生步频；存在原生步频时不使用代理。
- 路线质量：`route_distance` 在存在合法连接且路线 coverage ≥0.9 时为 available；`elevation_gain`/`elevation_loss` 还需至少 90% 的合法距离连接同时具备合法海拔与垂直精度，否则 partial；无合法连接/海拔时为 insufficient。`route_coverage` 始终是其自身测量值（available），不作为距离可用的替代。details 新增 `coverage` 与 `validElevationConnections`；不做 GPS 平滑、插值或路线改写。
- TRIMP 仍需显式 personal parameters（resting/max HR、A/B、provenance）：v3 只修复 heart_rate_coverage 这一缺失分量；没有 parameters 时 `workout_load` 继续为 insufficient 并列出 `personal_parameters`，不引入人口默认值或隐式性别系数。
- 版本与重算：显式 CLI 只接受当前 workout.bundle/v3；旧 v2 请求被拒绝，旧 run/publication 原样保留；不自动全历史重算。相同 raw 在 v3 下可得到不同 coverage、cadence、route quality，因此必须升级 bundle/features 标签。

## 2026-11-09 Addendum — personal TRIMP parameter input

`training_load.trimp/v1` 的公式与数值语义不变。新增个人参数入口把 resting/max HR 与 Banister A/B 作为显式追加输入：`POST /v2/model-inputs/trimp-parameters`（kind `trimp_parameters`，schema `rai.trimp_parameters/1`）。payload 必须同时包含 `restingHR`、`maxHR`、`coefficientA`、`coefficientB`；范围 30≤RHR<HRmax<220、(0,10]；缺失任一字段、非法值、带 through/supersedes 或撤销请求均拒绝，不补默认、不按年龄/性别推断。

`rai_commit_typed_model_input` 在同一事务追加 `analysis_parameter_sets` 新版本，并为最近 179 天（最多 1000 条）Workout 重排 `analysis.workout` job；参数集 append-only，历史版本不被改写，trimp 输入不使 v2 模型栈变脏。`GET /v2/analysis/parameters` 只读返回最新显式版本或 null；`rai_api` 仍无参数表写权限。App 的「心率参考」继续只进入模型 pipeline；训练负荷入口为「我的 → 个人设置 → 静息与最大心率」。
