# RAI Running Ability V1 — frozen candidate contract

日期：2026-11-03。模型身份：`rai.performance_curve/v1`、`rai.running_ability/v1`、`rai.endurance/v1`。共同配置身份为 `performance.ConfigHash()`；M1 stack 和 M1 policy hash 另行绑定。**candidate-only；REAL DEVICE ACCEPTANCE: DEFERRED。**

## Definition and scope

Running Ability 是**在可比平路连续跑条件下，近期运动事实所支持的 60 分钟持续跑表现能力**。V1 是 `demonstrated`、evidence-supported 的已展示表现，不是潜在最大能力、健康等级、比赛概率或 VO₂max。普通 Workout 不能证明 maximal effort；`maximalSupported="unavailable"`。没有已验证的 effort annotation 入口，不根据心率、标题或比赛关键词猜测。

唯一数值链路：M1 WorkoutFeatures → performance evidence → speed-duration relationship → supported v60 → score。Training Load、acute/chronic、balance/ratio、Recovery、Readiness、VO₂max、跑姿、跑步经济性不进入公式或置信度。没有 race/marathon prediction、训练建议或伤病模型。CS/D′ 未实现，也不是退出条件。

## Evidence and M1 boundary

只接受 `rai.workout_features/1`、`rai.workout_features/v1`、当前 feature stack 的完整 typed artifact；artifact SHA-256、dataset、Workout ID、feature run/generation 必须一致。不重新读取 HR、GPS、RunningSpeed、route_points、series_chunks 寻找表现。

M1 全 Workout 的 Performance qualification 与 window qualification 都必须是 `eligible` 或 `conditional`。只取每个 Workout、每个 duration 的 M1 `best`：1200、1800、2400、3600、5400 秒。180/300/600 秒及 non-overlapping backup 不进入拟合或置信度。Backup 保持在原 M1 artifact 中供诊断；M3 不复制它。没有第二套 GPS、pause、distance、source、grade 或 gap filtering。

`PerformanceEvidence` 保存 Workout ID、window 起止、Workout end、duration/distance/mean speed、feature version/run/generation、feature known-at、source epoch、M1 terrain context、qualification/reasons、可解释 quality components、evidence digest、effort mode。不是 M1 artifact 的副本。冲突的同 Workout feature revision 拒绝计算，完全相同的重复输入只保留一次；输入/数据库顺序不影响结果。

每个 evidence day h 的窗口为 dataset IANA calendar 中 `[h−89日零点, h+1日零点)`，只使用当时事件时间不晚于 h 的 evidence。Workout 按结束日归属；age 按真实经过秒数/86400 计算。

初始权重为 `q_i × exp(−age_i/45)`。q 是以下已有 M1 facts 的乘积，不是学习的黑盒分数：

| Component | V1 rule |
|---|---|
| speed coverage | M1 window coverage |
| distance agreement | `max(0,1−abs(M1 differenceRatio))`；缺失依赖 M1 门禁，分量为1 |
| terrain support | M1 已准入的 comparable flat context 为1 |
| source support | window source epoch 已知为1，缺失为0.5 |
| gap support | M1 `supported` 为1，其他已准入 quality 为0.8 |
| qualification support | Workout/window 任一 conditional 为0.9，否则1 |

同一 Workout 有 m 个实际纳入的 duration，每点再除以 m。因此 **每 Workout 总权重≤1**。不同 duration 可以帮助识别曲线，但不能冒充多次独立跑步。置信度独立数按 Workout ID，`n_eff=(ΣW_workout)²/ΣW_workout²`。

## As-known, revised and bounded input

每个 run 固定 `asOfUs`、输入 hash、calendar/timezone、feature run/generation、feature publication known-at 和 Workout first-received-at。Known-at 使用 feature 成功 run 的 finished-at，不用运行开始时间冒充已可用。尚未发布/仍 dirty 的已知 running Workout 是 dependency pending，不能静默缺席。

纯模型支持两种明确视图：

- `as_known`：feature 必须在 target h 的当时已经可用，且不晚于整个请求的 as-of。迟到事实和后来的确认不回填过去 as-known target。
- `revised`：使用当前请求 as-of 已知事实重建过去 event-day target，明确标记 revised。未来事件日 Workout 及未来确认仍不能进入过去 h。当前管理计算采用此模式；旧 run 保持 immutable，不改写成“当时已知”。

Workout 首次到达时已比 event end 晚超过90×24小时，则不进入 current M3，包括显式 rebuild 的 smoothing seed。历史专项 revised 重算没有伪造入口。90日内迟到和 feature revision/delete 触发 bounded recompute。

每次 target 只使用90个日历日 evidence；14日 smoothing 需要额外13日 seed，所以最终使用的 target evidence 范围最多103个日历日。以最新**接受的** evidence day 为锚点，避免无新证据时滑窗变动制造增长。最新候选若被隔离，loader 可以再执行一次有界 Feature 查询修正锚点；这不是 lifetime raw history 扫描。查询每次最多97行，最终上限96个 Workout、最多480个长 duration 点；超限明确 `analysis_input_limit`，不截断后假装成功。初次 enrollment 也有96个 Workout 上限；已有大量不参与 M3 的 Workout 可能需要先有界建立 Feature。

## Outlier and independence protection

按相同 duration 比较其他独立 Workout。至少有3个其他可比 Workout 时，某窗口高于其他速度中位数超过10%，且没有另一个独立合资格 Workout 的速度在该窗口速度±5%内确认，则整次 Workout 进入 `confirmation_required`，权重为0。

同 Workout 的其他窗口/backup 不能确认自己。后续独立 Workout 可以确认新水平；确认只从其自身事件日开始起效，不能泄漏进以前 target。普通 +5% 进步不被10%隔离门槛永久阻挡。被隔离的 Workout 不制造新的有效 target day。

## Curve and deterministic solver

`D(t)=D30×(t/1800)^γ`，D 单位 m，t 单位 s；`v(t)=D(t)/t`。

`x=ln(t/1800)`，`y=ln(D)`，拟合 `y=α+γx`，`D30=exp(α)`。

demonstrated 模式固定使用 p=0.8 weighted quantile regression；不是 mean regression，也不是全人群固定 Riegel exponent。枚举整数 g=700…1000，令 `γ=g/1000`：

1. 计算 `z_i=y_i−γx_i`。
2. α 取 weighted 80% quantile，即排序后累计权重达到80%的第一个 z；这是平坦分位区间中的较小 α。
3. 最小化 `Σ w_i ρ_0.8(y_i−α−γx_i)`，`ρ_p(u)=u×(p−I[u<0])`。
4. loss 差在1e−12以内视为数值 tie：优先较大 γ，再较小 α。所有点和求和次序固定，不用 random optimizer、ML 或通用 optimizer。

最优 γ=.700 返回 `curve_not_identified`，不 clamp 后发布。未加权 median absolute log-distance residual >.06 同样拒绝曲线。精确 γ=1 的平坦速度上界允许。

基本支持门槛：≥5个独立 qualified Workout；history span≥14个本地日历日；20/30min group≥2次独立 Workout；40/60/90min group≥2次独立 Workout；至少一次≥40min。最长 evidence≥2400s，因此3600s目标最大外推比例是1.5；没有长证据为 `insufficient_long_evidence`，不填默认 γ。最近 evidence≤14日才为 fresh，随后使用 freshness 状态。

对每个 Workout 做 Leave-One-Workout-Out 重拟合；任一删除使 v60 相对完整曲线变化>5%，或者 LOO 曲线落在不可识别下边界，则 `unstable`，当前 ability=null。LOO 复用每个 γ 的有序 residual，不引入随机性。普通 easy run 不按均值把模型解释为日常平均速度。

## Endurance and score

`v20=D30×(1200/1800)^γ/1200`；`v60=D30×2^γ/3600`。

`enduranceRetention60to20=v60/v20=3^(γ−1)`，是 performance endurance indicator。不是乳酸阈、VO₂max 或 economy。v60 已含速度锚点与耐力扩展，不再加一次 Endurance score。

`rawScore=100×ln(v60/1.5)/ln(6.0/1.5)`；`score=clamp(rawScore,0,100)`。

1.5m/s→0、6m/s→100 是固定产品 scale，**0不等于没有跑步能力，100不等于人类上限**。不按年龄、性别、地区或用户群 percentile 重新映射。v60=3m/s 精确为50；其他回归锚点按精确公式验证。

内部保留 float64 的 D30/γ/v20/v60/retention/raw score/clipped score/saturation；`displayScore=round(score)` 仅供未来展示，本阶段不激活 UI。

## Targets, smoothing and explanation

只在有新的、实际纳入的 performance evidence 的本地日创建 target；一天内多次 Workout 仍只有一个 target。target 的 recency 统一以 h 的固定日末计算，避免后来计算时同一天的时点差异修改过去 target。as-of 仍控制可见 evidence，不能使用未来数据。

以最新接受 evidence day h 为锚，取 h−13…h 的有效 target，用 `exp(−k/14)` 对 `ln(v60)` 平滑。至少3个有效 target days，且最新 target 本身通过门槛，才产生 current score，否则 collecting/对应失败状态、ability=null。

为保留精确解释，同时平滑 α 和 γ，得到几何 D30 与平均 γ；它们严格重构同一个 smoothed v60。`Result.curve` 是最新未平滑 target；`Result.ability` 中 D30/γ/v20/v60/retention 是最终平滑后的自洽参数，score 由其 v60 单独映射。

`ln(v60)=ln(D30/1800)+(γ−1)ln2`。输出真实的 log 30min speed anchor 与 log endurance extension。变化解释只能来自这两个数学项，不生成 VO₂、cadence、load、recovery 的虚构加减分。

## Confidence, cold start and freshness

输出 evidenceMode、独立 Workout 数、短/长组数量、duration coverage、source epochs、history span、n_eff、mean quality support、freshness、stability、confidence。mean quality 是先在每 Workout 内平均，再在独立 Workout 间平均。

有效 current 的 evidence support score：

`confidence=.55×min(n/10,1)×min(span/28,1)×min(n_eff/8,1)×meanQuality×exp(−age_latest/45)`。

最大 .55；不是准确概率，.8不能解释为80%准确。collecting、curve failure、unstable 或 hidden current 的 confidence=0，保留其支持事实，不用一个低置信度精确分数替代未知。

| Latest accepted evidence age | Current projection |
|---|---|
| <14×24h | fresh |
| ≥14×24h、<28×24h | aging，value不变，confidence下降 |
| ≥28×24h、<42×24h | stale，ability=null |
| ≥42×24h | insufficient_data / evidence_expired，ability=null |

没有 daily M3 target job。管理读取在 immutable 候选结果上计算 freshness，不重新拟合旧 Workout；四天无证据 score不变，六周无证据隐藏，不建立未经验证的 detraining。未上传不证明没有跑步。历史原始结果仍通过显式 run ID 查询。

trend 仅在相差28个本地日历日、同 model/version/config/evidenceMode/view 且两次有值的历史结果间比较 score；没有可比结果为 null。

## Persistence, invalidation and candidate publication

没有新增 duration 表、没有 evidence ledger，也不复制 WorkoutFeatures。复用 analysis_runs/results/artifacts/publications/model_channels/model_dependencies。M3 是一个原子 scope `model/<feature-stack>/athlete/performance`，包括逻辑上的 Evidence→Curve→Endurance/Ability；不拆成可能半完成的多个 current pointers。

同一 run 写5个 typed artifact：performance_evidence、performance_curve（含targets）、running_ability、endurance、performance_state；写3个 typed result：supported_v60、running_ability、endurance_retention。统一 candidate publication family 为 `performance_candidate`。M1 原有 ability/endurance future scope placeholders 不创建虚假成功 handler。

单一 PublishAnalysis 事务包含所有 artifact/result/pointer/clean/run terminal/job terminal/attempt terminal。沿用 dataset 行锁、lease deadline、fencing token、generation、候选 M1 assignment 和 M3 extension config hash 检查。旧 generation、已失租或 config 已变化的 Worker 不能发布；SUPERSEDED 不写 artifact、不 clean N+1。发布时还在 dataset 锁内核对所有捕获 Feature 的 run/generation 与 Workout 存活身份；旧 seed 在尚无 current pointer 时删除也会被发现，并排入新一代 bounded repair。

原事实/删除事务中的 M1 dependency trigger 立即删除 M3 current pointer并使 scope dirty；Worker 尚未运行时就不能继续返回 current。已被当前 run 引用的 Feature 即使事件已老于90日，revision/delete 仍触发失效；无关的90日外迟到 Workout 不能影响 current。旧 runs/artifacts 保持 immutable，可审查。

00023 是唯一新 migration：扩展 candidate channel 的 model config manifest、M3 artifact约束/校验与 dirty函数，新增 bounded end/history 索引；不改00001–00022。无新增 table或sequence。active被数据库约束保留 legacy；不开放 promotion。

管理命令（显式 synthetic/development dataset）：`rai analysis performance compute|recompute --dataset UUID --operation UUID --actor NAME`；`inspect --dataset UUID`；`history --dataset UUID --run UUID`；`promotion-plan --dataset UUID`。Compute/recompute 是同一 bounded rebuild，复用已发布 M1 artifacts；operation retry 不制造新作业。没有HTTP首页/训练消费者或Swift DTO变更。

promotion-plan 返回 promotable=false；rollback substrate 是 legacy active/pointers从未被替换、immutable history保留、candidate config可独立撤销。没有执行正式promotion或schema Down；旧二进制跨schema23回退未验证，不能将保留历史冒称为已验证二进制回滚。

## Validation limits

合成数据、deterministic/property/Golden Athlete、真实隔离 PostgreSQL18、事务故障、实际 backend termination和性能矩阵只能证明实现行为。q、门槛、source epoch策略、confidence和产品scale均未经过真实人群校准。

**REAL DEVICE: DEFERRED；REAL DEVICE ACCEPTANCE: DEFERRED。** NOT VERIFIED：真实GPS精度、speed support/quality、Workout eligibility、平路准入率、实际努力分布、真实比赛、Apple Watch sampling、v20/v60证据密度、单Workout偏差、source epoch效应、实际跑力精度、生理/现实世界有效性、population calibration、production/WAN。

未来真机校准必须逐项登记：real performance eligibility rate；real GPS distance consistency；real speed support quality；real flat-route qualification rate；real effort distribution；real v20/v60 evidence density；real single-workout bias；real source epoch effects。没有真机不阻塞工程实现，但本阶段最多 PASS WITH RISKS。
