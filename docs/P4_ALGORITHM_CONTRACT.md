# P4 Algorithm Contract

冻结于 2026-10-28；P4_BASE_COMMIT=8e40573e027ac6dd9aac871fb755051cb31c4657。
P3 REAL DEVICE ACCEPTANCE: DEFERRED BY USER（仍 NOT VERIFIED）。
P4 DEVELOPMENT EXCEPTION: AUTHORIZED BY USER。

## 执行与身份

Go 纯函数；显式版本；float64，UTC 微秒输入，输出不允许 NaN/Inf。canonical JSON（有序 slices、Go map key 排序）SHA-256 只覆盖 scope 输入及 lineage，不摘要全库。输入在同一 REPEATABLE READ 事务读取；target generation 必须等于该快照代际，不能将新事实标成旧代际。run 完成后及 result 不可修改，重试使用独立 attempt/run；逻辑 identity 是 dataset/scope/algorithm/version/generation/input_hash，唯一约束。当前结果只经 publication 查询。

## 算法

| name | version | inputs | outputs / units | window |
|---|---|---|---|---|
| workout.features | v1 | durable Workout、approved enrichment、显式关联 quantity/已 sealed series、sealed route | duration s、distance m、speed m/s、pace s/m、HR count/min、cadence count/min、power W、coverage ratio；route distance/elevation m | 一个 Workout |
| training_load.trimp | v1 | features、显式 parameter set：resting HR、max HR、A、B、provenance | load AU、method、missing components | 一个 Workout |
| fitness_fatigue | v1 | published TRIMP、dataset timezone、daily window | daily load AU、fitness/fatigue AU/day、balance AU/day | 每日有限 180 天核 |
| recovery | v1 | 同日 fitness/fatigue | load recovery ratio（0..1） | 同上 |
| readiness | v1 | fitness/fatigue、recovery、缺失组件 | load-only score（0..100），组件和缺失项 | 同上 |
| dependency.invalidate | v1 | sample/series scope | 已验证依赖传播的执行记录；不生成健康评分 | 当前 scope |

Workout 与 TRIMP 同一 handler/run bundle（workout.bundle/v1）原子发布，结果每项带实际算法版本；state bundle（training_state.bundle/v1）发布日序列。只有 bundle 固定所列版本，不能混用不同版本而保留 bundle v1。

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

P3 输入 blocker：原 sample 删除级联 association 后未 dirty Workout；sealed series 未 dirty 关联 Workout。新增 migration 仅补依赖 dirty/job，保留 wire/anchor/batch/route-seal/receipt 身份。样本批次原每 UUID 建 job，1000 sample 产生1000 jobs；改为同 type/UTC 日合并依赖任务，回执仍为 queued + 原 job IDs（数量不固定，旧回执不变）。不增加第二队列。

只保存稳定 error codes。暂保留所有 runs，不做 retention。没有 P5 计划、教练或排程。
