# API 清单（以 `apps/server/internal/httpapi/server.go` 为准）

机器可读契约：`contracts/openapi-v2.yaml`；同步载荷格式：`contracts/sync-v6.md`。
所有 `/v2/*` 需要 `Authorization: Bearer <设备令牌>`。

## 健康检查
`GET /livez` `/health` `/readyz` `/ready`

## 同步
- `GET /v2/handshake` — 身份、数据集周期与能力
- `POST /v2/sync/workout-ingests/{uuid}` — 上传一条 protocol-6 Workout（multipart `file`，gzip 或明文 JSON）
- `GET /v2/sync/workout-ingests/{uuid}` — 单条接收状态
- `POST /v2/sync/workout-ingests/status` — 批量状态，body `{"workoutIDs":[…≤200]}`，返回 `items`（含 `not_found`）
- `POST /v2/sync/daily-facts` / `GET /v2/sync/daily-facts/coverage` — HRV、静息心率、睡眠；每页新增+删除合计不超过握手
  `capabilities.dailyMaxChanges`（schema 54 起为 500，此前 128），请求体不超过 512 KiB。客户端保存最近一次握手的值，
  未握手时按 128 分页。删除（`deletedUUIDs`）照常上传并写墓碑，是“不做删除同步”的例外
- `POST /v2/sync/vo2max-facts` / `GET /v2/sync/vo2max-facts/coverage` — 外部 VO2
- 上传并发：同一设备同时最多两个 Workout 上传和一个日常健康（daily / VO2）上传，分别由握手
  `limits.workoutUploads=2` / `healthUploads=1` 公告；超过各自额度返回 `429 device_upload_capacity_exceeded`。
  进程内总并发由 `limits.processUploads=3` 限制。iOS 对旧服务端缺失 `workoutUploads` 时退回单路上传
- `GET /v2/sync/changes` — 变更通知（能力位 `changefeed=false`）
- `POST /v2/model-inputs/{kind}`、`POST /v2/model-inputs/{id}/revocations` — 个人参考值与目标
  （App 使用 `hr-references`、`sleep-targets`、`vo2max-source-selections`、`workout-efforts`；`trimp-parameters` 仍接受但 App 已不再写入）
  - `workout-efforts` 的 `mode` 为 `race` / `threshold_test` / `speed_test` / `sprint_test`（旧值 `maximal_supported` 按 `race` 读取），
    须 `userConfirmed=true`；撤销用 `revocations`。标注只改变模型输入，不修改 HealthKit 事实
  - `hr-references` 的 `method` 新增 `age_estimated`：仅在用户显式选择“暂用年龄估计”时提交，跑力据此一律为有限、敏感性范围加宽

## 读取
- 运动：`GET /v2/workouts`、`/v2/workouts/{id}`、`/detail`、`/series`、`/freshness`
  - `/detail` 的心率覆盖及分析卡速度覆盖使用有效连续支持（包含受限插值），不使用瞬时样本数量除以秒数。优先取已发布特征的 `reconstructedCoverage`（活动时间口径，保留暂停、来源冲突和异常过滤）；`observedCoverage` 保留为原始区间观测语义。旧详情无特征时，心率显示按相邻有效瞬时点间隔 ≤15 秒计连续支持，不外推首尾、不跨越长断点；旧心率分区按线性插值的区间边界累计。此修复不修改原始载荷或模型版本，也不自动发布、部署或重算。
  - `/detail` 新增可选 `distanceDistribution`（`policyVersion=distance-zones/v1`），由原始速度／心率时间区间积分距离，
    独立于模型发布与旧 `zoneSummary` 的时长占比。返回 `totalDistanceMeters`、`pace`、`heartRate`（各项 `zone` / `distanceMeters`）、
    `paceUnknownMeters`、`heartRateUnknownMeters`、`heartRateProfileID` 与 `reasonCodes`。
    配速固定为 `<4:00`、`[4:00,5:00)`、`[5:00,6:00)`、`[6:00,7:00)`、`≥7:00 min/km`，
    对应 `under_4`、`4_to_5`、`5_to_6`、`6_to_7`、`over_7`；心率按训练开始时有效的个人 HRR 参数分为
    `<60%`、`[60%,70%)`、`[70%,80%)`、`[80%,90%)`、`≥90%`，不回退到当前参数。
    重复观测只计一次，冲突、暂停和缺失区间不归类；积分超过记录总距离时统一缩小，少于总距离时不放大补齐。
    旧服务未提供该字段或分区读取失败时，客户端保留总跑量，将相应距离列为未分类。
  - `/detail` 的 `zoneSummary` 配速区间（`paceExposure` / `paceCoverage`）按跑力六区间 Z1–Z6 划分，边界取**运动开始日之前、7 天内最近一个本地日**
    已发布的跑力状态（`paceZoneProfileDate`；前一日缺发布时向前回退，区间 `validUntil` 早于运动日则不用），本次跑步不会用它自己塑造的边界给自己归类；`paceZones` 回显所用快端／慢端配速
    （秒/公里，Z1 慢端、Z6 快端为开放端，`support` 为跑力原支持级别）。`paceExposure` 各项给出时长、距离与时长占比，
    `paceCoverage` 为计入速度时长占移动时长（总历时扣除暂停，重叠暂停只扣一次）之比。只用原始 `RunningSpeed` 区间：不做坡度修正、不外推瞬时点，暂停、静止（速度 0）
    与冲突重叠不计入，相同重叠只计一次。`paceZoneStatus`：`available`；`threshold_pace_unavailable`（训练前无已发布跑力区间，
    包括冷启动或证据不足）；`zone_anchor_conflict`（Z1–Z5 快端缺失或不严格递增，不排序冒充）；`speed_unavailable`（无可用速度区间）。
    非 `available` 时 `paceExposure` 为空。刺激类型评分仍只用心率，配速暴露仅作描述；无心率但配速可用时也返回 `zoneSummary`
    （此时 `heartRateExposure` 为空，iOS 不显示心率区间卡，刺激类型说明为缺少心率区间）。
  - iOS 周／月统计完整读取列表游标链，仅请求所选周期跑步的详情；使用 overview 的数据集时区，周一为周起点，
    跨午夜跑步按开始日计入。分区百分比以本期已知总距离为分母，包含未分类距离，不是心率时长占比。
- 分析：`GET /v2/analysis/parameters`、`/vo2max-sources`、`/athlete`、`/athlete/history`、`/athlete/diagnostics`、
  `/running-fitness/current`、`/running-fitness/history`、`/running-fitness/evidence`；`POST /running-fitness/reanalyses`、
  `GET /running-fitness/reanalyses/{operationID}`（见下方“跑力”）
  （旧链路的 `/v2/analysis/state`、`/history`、`/workouts/{id}` 已删除，返回 404）
  - 运动列表的 `metrics` 只含 active stack 的 `workout_load`（单位 `RAI_AU`，`details.qualityTier`/`method`）；尚未产出结果时 `analysisStatus=pending`；当前事实代次的单条输入分析失败时为 `failed`，已发布为 `published`。失败不会作为等待状态展示，也不覆盖原始记录；成功重新分析会清除失败状态（迁移 59，已于 2026-11-12 部署）
  - 训练回执的 `analysisStatus=queued` 表示 v2 日模型栈已失效待算，`analysisJobIDs` 为空
  - `/parameters` 返回 `parameters`（旧 TRIMP 参数）、`hrReference`（当前生效的 `hr_reference`，用户本人录入值）与 `observedMaxHR`（近一年 30 秒心率中位数最大值，仅作建议）
  - `/vo2max-sources` 返回近一年的 Apple VO₂ 来源，每个设备/App 一项，`sourceEpoch` 即选择时提交的值
  - `/athlete` 缺省按 schema2 契约读取；接受的查询只有 `schemaVersion=rai.athlete_model/3` 或 `rai.athlete_model/4`，
    其他取值、重复或多余参数返回 `422 invalid_athlete_query`。请求的 schema 新于 active 栈时按该栈自己的契约输出；
    active 栈新于请求的 schema 时，所有指标返回既有可解码的 `model_update_required`，不把新语义塞进旧字段名。
    iOS 已按 schema4 请求
  - schema4（跑力 v1 栈）新增 `runningFitness` 指标（`modelVersion=rai.running_fitness/v1`，`mappingVersion=rai.product_metric_mapping/4`，
    `mode` 为阶段 `initial`/`preliminary`/`established`/`race_only`/`insufficient_evidence`/`conflict`）；旧 `runningAbility` 槽位保留可解码，
    但在任何栈上都只返回 `model_update_required`、`value=null`（旧 v60 跑力已退出运行链路，旧栈的旧跑力产物只作审计行保留）
  - schema3 新增 `sessionRecovery`（`remainingHours`/`range`/`readyAfter`/`overflow`/`scope`，估计的下次高强度前恢复时间）
    与 `loadFocus`（7/28 天 `windows`：`lowAerobicAU`/`highAerobicAU`/`anaerobicProxyAU`/`unclassifiedAU`、
    `unknownWorkouts`、`classifiedCoverage`、`classificationMethod`）；`runningAbility.value` 携带
    `demonstrated`/`maximalSupported`/`submaxEstimated` 三分支、`selectedMode` 与积累 `progress`
    （workouts/spanDays/shortWorkouts/longWorkouts/targetDays），估计分支没有 gamma；
    `physiologicalRecovery.value` 增加 `components`/`missingComponents`/`baselineNights`/`trendNights`，
    `trainingReadiness.value` 增加 `range`/`physiologicalCap`/`sessionCap`/`weeklyCap`；
    envelope 增加 `mode`（数值性质，如 `complete`/`estimated`/`workout_only`/`pending`，随指标而异）、
    `evidenceAt`、`validBefore`。schema3 的 `complete_response` 要求八项指标均 available
  - `/athlete/history?metric=trainingLoad` 的点以急性负荷为 `value`，`components` 携带 `chronic`、`balance`、`dailyLoad`；
    schema3/4 栈另支持 `metric=sessionRecovery`/`loadFocus`，schema4 栈支持 `metric=runningFitness`（`value` 为长期总分，`components`
    含 `referenceDailyScore`、`a70MPS`、`vTMPS`、`v6MPS`、`v90sMPS`、`bL`、`rangeLow`、`rangeHigh`）；
    `metric=runningAbility`/`abilityEstimate` 在任何栈上都返回 `409 model_version_unsupported`
  - 运动模型的激活、产物缺失与读取故障分别表达，见下方状态表
  - `/detail` 可选 `modelQualification.trainingLoad`（`status` 与 `reasonCodes`）：`eligible`（完整心率）/ `conditional`（估算）/
    `ineligible`（未知）/ `pending`（尚未算出）。旧的 `runningAbility` 资格字段已退出
  - schema4 栈的 `/detail` 可选 `runFitness`：`single`（单次跑力：`status`、`evidenceTypes`、`hrNormalized` 的 `a70MPS`/`score`/
    `scoreSensitivityRange`/`abilitySensitivityRange`/`netSupportedSeconds`/`gradeMode`/`segments`，确认比赛的 `raceEquivalent`，
    `retention`、`hrMaxSuggestion`、`reasonCodes`），`referenceDailyScore` / `referenceDate`（训练日前一天长期状态按同一固定政策的分数）
    与 `deltaToReference`。来自该训练本地日的已发布日状态；当天尚未（重新）发布时省略。冷启动无基准时不给差值
  - 含训练效果组件的栈（`rai-v2-69efefa730b47818` 起）的 `/detail` 可选 `trainingEffect`（`rai.training_effect/1`）：
    `aerobic` / `anaerobic` 各为 `{value, label, status, reasonCodes}`，`value` 0.0–5.0（一位小数）或 `null`（不可用，`label` 为空、
    `status=unavailable`，绝不以 0 冒充），`label` ∈ `none|minor|maintaining|improving|highly_improving|overreaching`，
    `status` ∈ `complete|estimated|unavailable`；另含 `epocPeak`（ml/kg 当量代理值）、`epocPeakAtUs`、`trainingLevel`、
    `trainingLevelStatus`（`observed|estimated|default`）、`anaerobicWorkM`、`anaerobicReserveM`、`anaerobicPeakM`、
    `highIntensitySeconds`、`unsupportedSeconds`、`bouts`、`policy`、`inputHash`。常见原因码：`hr_reference_missing`、
    `hr_exposure_insufficient`、`speed_reference_unavailable`、`effort_features_missing`、`short_sprint_uncorroborated`、
    `bout_uncorroborated`、`indoor_speed`、`ability_reference_exceeded`、`training_level_estimated|default`。
    旧栈、未分析或栈尚未算到这次跑步时省略。RAI 自有模型，不是手表厂商的训练效果
  - schema3 栈的 `/detail` 另含可选 `loadFocus`：这次课次的 `knownAU`、四桶 `buckets`、区间秒数 `seconds`、
    跨午夜按日切分 `days`、`classificationMethod` 与 `reasonCodes`；旧栈或栈尚未算到这次跑步时省略，
    负荷未知时 `knownAU=null`（原因 `load_unknown`），不把未知当低强度
- 首页：`GET /v2/product/overview`

### 跑力（schema4 栈，`rai.running_fitness/1`）

所有数值由服务端计算；iOS 不再本地推算配速区间或成绩。active 不是 schema4 栈时这些接口返回 `409 model_version_unsupported`，
未启用返回 `409 not_product_activated`。

- `GET /v2/analysis/running-fitness/current` — `{schemaVersion, assignment, runningFitness}`，`runningFitness` 与 `/athlete?schemaVersion=rai.athlete_model/4`
  中的同名指标相同：`overall`（`score`/`rawScore`/`range`/`basis`/`marathonEquivalentSeconds`）、`referenceDailyScore`、
  `params`（`a70MPS`/`vTMPS`/`v6MPS`/`v90sMPS`/`bL`/`bS` 及识别状态）、`dimensions`（`endurance`/`threshold`/`speed`/`sprint`，
  各含 `status`、物理锚点、`rating`、对应区间 `zone`、`evidenceHint`）、`predictions`（5k/10k/half/marathon 的 `seconds`、`range`、
  `predictionSupport`、`targetEligible`、`longRunCovered`、`conditions`）、`zones`（Z1–Z6 的快/慢配速、`support`、`schedulable`、
  `reasonCodes`）、`racePaces`、`preparation`、`support`、`stability`、`singleRuns`（近 42 天单次点）与 `reasonCodes`。
  范围为确定性角点“模型范围”，不是概率
  - iOS 跑力详情用半圆刻度展示总分，并将四项能力按 `dimension.zone` 对应到 Z2/Z4/Z5/Z6 的训练配速区间；
    不把 `speedRange` 的模型不确定范围当训练区间，不在客户端补算缺失边界或评分。冲突、缺失区间显示待补充，
    默认假设区间明确标为仅参考。基础指标独立读取当前可展示的外部 VO₂（优先）或 RAI VO₂（标实验性），
    乳酸阈值配速来自 `dimensions.threshold.paceSecondsPerKm`（估计须注明）；当前契约没有乳酸阈心率，显示 `-- / 暂未提供`，
    不用最大心率比例冒充该指标
- `GET /v2/analysis/running-fitness/history?from=&to=&view=&cursor=&limit=` — 逐日 `days`（长期总分、同政策日常基准、参数、
  当日单次点、`publicationID`）。`view=revised`（缺省，当前算法与输入下的历史，可含后来补传、事件在评估日前的训练）；
  `view=published`（每天在当天结束前最后一次发布的不可变结果，即使后来被重算替换）；`view=as_known` 返回
  `409 historical_input_unavailable`。范围最长 366 天，缺失日不插值；游标绑定数据集、栈、视图与范围
- `GET /v2/analysis/running-fitness/evidence?date=&dimension=` — 某个已发布日状态的证据清单（训练、日期、类型、维度、权重、物理量）
- `POST /v2/analysis/running-fitness/reanalyses` — 头 `Idempotency-Key`（1–128 字符）必填；正文
  `{"workoutIDs":[…≤100]}` 或 `{"from":"YYYY-MM-DD","to":"YYYY-MM-DD"}`，另需 `"view":"revised"`、`"referencePolicy":"current_effective"`、
  `"algorithmVersion":"v1"`（只能指定 active 栈的算法，不能选任意代码）。同键同请求返回同一操作，同键不同请求 `409 idempotency_conflict`，
  其他非法请求 `422 invalid_reanalysis_request`。成功返回 `202`，只表示已排队：受影响范围为起点到终点 +89 天（不超过今天），
  只重排其中已经发布过的日发布，最早一年内
- `GET /v2/analysis/running-fitness/reanalyses/{operationID}` — `status`（`queued`/`running`/`completed`/`failed`）、
  `totalDays`/`completedDays`/`failedDays`。状态由各日发布的代数推导；失败保留原发布，不置零、不伪报完成
- 任务：`GET /v2/jobs/{id}`

### 运动模型状态

`GET /v2/analysis/athlete` 的 HTTP 成功表示已返回模型快照，不能据此认定每项指标都有可展示数值。
各项以 `envelope.modelStatus`、`productReasonCodes`、`state`、`freshness` 和 `technicalError` 表达实际状态。
`productReasonCodes` 保持冻结的字典序；`primaryReason`（可省略）按明确优先级给出应展示的那一个：
未启用/版本/读取故障 → 待计算 → 来源 → 同步未完成 → 历史不足 → 具体输入（心率、负荷、恢复）→ 数据已过期 → 证据不足。
iOS 优先显示 `primaryReason`，旧服务端没有该字段时取 `productReasonCodes` 第一个。
状态 `collecting`、`collecting_baseline` 优先于过期。跑力 v1 不再要求长跑：一次带心率、5 分钟以上且有 ≥180 秒合格稳定片段的跑步
即可产生单次跑力与初步长期估计；没有任何证据时为 `collecting`，App 提示完成一次稳定跑步即可开始估计。

| 条件 | HTTP / 模型状态 | 产品原因码或错误码 | iOS 文案 |
|---|---|---|---|
| 无 active assignment 或 active 为 legacy | 409 | `not_product_activated` | 尚未启用 |
| 当前栈当天无可读发布，且没有该代数的终止失败任务 | 200 / `pending` | `model_result_pending` | 结果尚未就绪 |
| 当前栈无可读发布且该代数任务已失败，或某项产物校验失败 | 200 / `technical_failure` | `technical_failure` | 模型结果读取失败 |
| 支持的旧栈产物缺失或失效，当前 Worker 无法重算 | 200 / `model_update_required` | `model_update_required` | 模型需要更新 |
| 激活栈未匹配冻结发布注册表 | 200 / `unsupported` | `unsupported` | 模型版本需升级 |
| 计算结果证据不足或已过期 | 200 / 对应模型实际状态 | `insufficient_evidence` / `data_stale` 等 | 证据不足 / 数据已过期 |

前五种情况中，200 响应的缺失指标 `value=null`；不会用零补齐。全无 `state=available` 的指标时
`responseState=unavailable_response`，部分为 `partial_response`，全部可用才为 `complete_response`。
数据库或 assignment 元数据等读取故障可返回 `503 athlete_model_unavailable`，不等同于未激活。

保留的 schema3 栈中，`envelope.mode` 与技术状态正交：`estimated` 表示输入缺失下的有限估计（恢复总分≤75、置信度≤0.40；
准备度区间宽度≤10 才给点估计并取保守端），`workout_only` 表示仅有训练负担信息（准备度 `score=null`，疲劳/负荷仍可独立展示）。
计算成功的产物可以因证据不足没有数值；读失败不能叫 estimated。模型级新原因码（`night_data_missing`、`night_sparse`、
`recent_trend_used`、`estimate_expired`、`regularity_collecting`、`session_intensity_unknown`、`submax_prior_used`、
`ability_source_disagreement`、`load_baseline_insufficient`、`short_repetition_unsupported` 等）保留在 `reasonCodes` 供审计；
恢复 v4（`rai.recovery.physiological/4`，需要迁移 58，新栈尚未部署）沿用恢复组件结构，新增可选 `inputBasis`、`meanSamples`、
`baselineCenter`、`baselineScale`。`inputBasis` 为 `recent_mean`（最近最多 7 个有效夜／日的算术均值）、`observed`
（当晚睡眠）、`user_entered`（静息心率）或 `default_baseline`（HRV 50 ms／RHR 60 bpm／睡眠 8h）。历史查询有界为
180 天；`observed=null` 保留当晚缺测语义，`used` 才是实际计算值。缺测可返回 `available` + `mode=estimated`，
取消估计模式的 75 分封顶及每缺失组件扣 5 分，分数使用同一 0–100 公式，估计状态与置信度独立表达证据质量。
健康数据缺失不再要求最近 7 天或 48h 内有证据；日界到期仍返回 expired。训练准备度模型保持 v4，当前 v4.1 policy
也取消估计模式 75 分封顶，继续遵守训练负荷与课次疲劳可用性要求。新 configHash/stack 区分策略修订，
旧恢复 v3 及原带封顶 v4 的发布产物按其冻结配置读取，不重算或改写历史分数。

`productReasonCodes` 经 `MapReasons` 映射到冻结字典（恢复相关新码 → `recovery_data_incomplete`、
`ability_source_disagreement` → `source_conflict`、`load_baseline_insufficient` → `not_enough_history` 等），iOS 用既有文案本地化。

历史接口继续绑定 active assignment：支持的旧栈按该发布配置校验，未知版本返回 `409 model_version_unsupported`；
`view=as_known` 返回 `409 historical_input_unavailable`，不能由当前发布指针重建过去的已知视图。
历史分页游标绑定数据集、指标、栈和 assignment version 等上下文，切换后旧游标返回 `409 history_cursor_mismatch`。

iOS 将响应解码失败或未知顶层 schema 记录为本地 `athlete_response_invalid`，保留上次成功结果并显示刷新失败及结果时间；
首次加载尚无成功结果时显示未就绪或读取失败。只有明确收到 `not_product_activated` 才按未启用处理并清除缓存模型。

## 训练
- 读取：`GET /v2/training/plans/current`、`/plans/{id}`、`/effective[/{version}[/audit|/detail]]`、`/history[/versions]`、`/executions/{id}`
- 写入：`POST /v2/training/plans`、`/plans/{id}/changes`、`/plans/{id}/overrides`、`/executions`、`/executions/{id}/changes`
- 训练输入适配器按 active 栈版本绑定：schema2 栈用 `schema2-v2`（只消费 complete 准备度，低准备度阈值取计划参数
  `lowReadiness`，默认 50）；schema3 栈用 `schema3-v1`（消费 v4 complete 与 estimated 准备度，阈值固定 60；estimated 只走保守下调，
  不自动增加强度或里程；workout_only 不推断恢复正常）；schema4 栈用 `schema4-v1`（准备度同 schema3，另消费跑力的可排课区间配速）。
  生成器 `training_plan/v3`：轻松跑→Z2、长跑→Z2、恢复跑→Z1、节奏跑→Z4、间歇→Z5，取区间中点配速；区间不可排课时该课只给心率目标并在
  `reasons` 记 `pace_zone_unavailable_hr_only`，不回退到其他区间或旧公式。旧栈不再提供配速目标。负荷焦点在 v3 中只作事实描述
- 自适应计划（迁移 60）：`POST /v2/training/plans` 的 `config` 带 `"version":"training_config/v2"` 时必填 `startDate`（本周周一）、
  `availableDays`（2–7 天）、`longRunDay`；`goal`（`type/date/distanceM`，可选 `targetTimeS`）与 `baselineWeeklyS` 可选，不接受
  `weeks`、`baselineWeeklyM`、`previousLongM`。由 `training_plan/v4` + `coach_parameters/v2` 生成。`/changes` 的 `update_config`
  可设置／更换／清除目标，版本与 `startDate` 不可改，新比赛日不得早于今天（否则 `422 invalid_training_config`）。
  覆盖日期须在当前窗口内（`422 training_date_out_of_range`），v4 计划的覆盖会排队重算。
- v4 有效版本新增：`asOf`、`windowStart`、`mode`（`race`／`fitness`）、`weeks[]`（`index/startDate/detail(past|current|next|outline)/
  phase/state/levelS/budgetS/plannedS/actualS/hardFocus/reasonCodes` 等）、`goalStatus`（`category/phase/daysToRace/prepStartDate/
  targetTimeS/predictedTimeS/gapRatio/assessment/racePaceSPerKM/completed/reasonCodes`）；课次新增 `focus`、`purpose`、
  `changedFrom`（`date/type/focus/durationS/reasonCode`）、`outcome`。生成的跑步课 `durationS ≥ 1800`，`distanceM` 为按区间配速的估计；
  轻松跑与长距离也可带 `steps`。字段均为增量，v3 计划的响应不变

## 限额
读 / 写 / 上传三条车道，设备与数据集各一个进程内令牌桶（设备默认：读 120/分、写 60/分、上传 30/分（突发 2）；可用 `RAI_RATE_BUDGETS` 覆盖，
真机验收后端 `scripts/local/device.py` 把上传放宽到 120/分、突发 12）。设备桶先判、数据集桶后判，两者都允许才同时扣额；
任一拒绝都不扣额，所以单设备超额只返回 `device_rate_exceeded`，不会顺带耗尽数据集余额。超限返回 `429` 与 `Retry-After`。
握手 `limits.requests` 返回生效预算，iOS 按其中的上传预算节流日常健康与 VO2 上传。Workout UUID ingest 沿用
独立并发槽与积压额度，不套用日常健康的每分钟预算；遇到 429 按 `Retry-After` 重试。
同步握手在进程内缓存 60 秒，键包含服务 URL、令牌摘要、collector epoch 和会话 ID，并合并并发请求；失败不缓存，
401/403/409 使缓存失效。每次使用仍检查本地会话为 active；连接、激活与恢复校验始终实时握手。
