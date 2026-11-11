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

## 读取
- 运动：`GET /v2/workouts`、`/v2/workouts/{id}`、`/detail`、`/series`、`/freshness`
- 分析：`GET /v2/analysis/parameters`、`/vo2max-sources`、`/athlete`、`/athlete/history`、`/athlete/diagnostics`
  （旧链路的 `/v2/analysis/state`、`/history`、`/workouts/{id}` 已删除，返回 404）
  - 运动列表的 `metrics` 只含 active stack 的 `workout_load`（单位 `RAI_AU`，`details.qualityTier`/`method`）；未发布时 `analysisStatus=pending`
  - 训练回执的 `analysisStatus=queued` 表示 v2 日模型栈已失效待算，`analysisJobIDs` 为空
  - `/parameters` 返回 `parameters`（旧 TRIMP 参数）、`hrReference`（当前生效的 `hr_reference`，用户本人录入值）与 `observedMaxHR`（近一年 30 秒心率中位数最大值，仅作建议）
  - `/vo2max-sources` 返回近一年的 Apple VO₂ 来源，每个设备/App 一项，`sourceEpoch` 即选择时提交的值
  - `/athlete` 缺省按 schema2 契约读取；唯一接受的查询是 `schemaVersion=rai.athlete_model/3`（返回 schema3 快照），
    其他取值、重复或多余参数返回 `422 invalid_athlete_query`。active 为旧栈时 schema3 请求降级按 schema2 输出；
    active 为 schema3 栈而按 schema2 读取时，无法按旧语义安全输出的指标返回既有可解码的 `model_update_required`，
    不把新 estimated 塞进旧 complete。iOS 已按 schema3 请求
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
    schema3 栈另支持 `metric=sessionRecovery`/`loadFocus`/`abilityEstimate`，旧栈对这三个 metric 返回 `409 model_version_unsupported`
  - 运动模型的激活、产物缺失与读取故障分别表达，见下方状态表
  - `/detail` 可选 `modelQualification`：`runningAbility` / `trainingLoad` 各含 `status` 与 `reasonCodes`，说明 active stack
    是否采用这次跑步。跑力来自特征资格（v2 用坡度换算后的资格）：`eligible` / `conditional` / `ineligible`；负荷来自 v2 负荷结果：
    `eligible`（完整心率）/ `conditional`（估算）/ `ineligible`（未知）/ `pending`（尚未算出）。入选只是必要条件，
    跑力还只取每个时长最快的几次。栈尚未算到这次跑步时省略该字段
  - schema3 栈的 `/detail` 另含可选 `loadFocus`：这次课次的 `knownAU`、四桶 `buckets`、区间秒数 `seconds`、
    跨午夜按日切分 `days`、`classificationMethod` 与 `reasonCodes`；旧栈或栈尚未算到这次跑步时省略，
    负荷未知时 `knownAU=null`（原因 `load_unknown`），不把未知当低强度
- 首页：`GET /v2/product/overview`
- 任务：`GET /v2/jobs/{id}`

### 运动模型状态

`GET /v2/analysis/athlete` 的 HTTP 成功表示已返回模型快照，不能据此认定每项指标都有可展示数值。
各项以 `envelope.modelStatus`、`productReasonCodes`、`state`、`freshness` 和 `technicalError` 表达实际状态。
`productReasonCodes` 保持冻结的字典序；`primaryReason`（可省略）按明确优先级给出应展示的那一个：
未启用/版本/读取故障 → 待计算 → 来源 → 同步未完成 → 历史不足 → 具体输入（心率、负荷、恢复）→ 数据已过期 → 证据不足。
iOS 优先显示 `primaryReason`，旧服务端没有该字段时取 `productReasonCodes` 第一个。
状态 `collecting`、`collecting_baseline`、`insufficient_long_evidence` 优先于过期：从未有过曲线的跑力即使最新证据已超过
28 天，仍为 `state=collecting`、`modelStatus=insufficient_long_evidence`（`freshness` 照常为 stale/expired），App 显示
“证据不足：需要至少 2 次 40 分钟以上的合格跑步”。

| 条件 | HTTP / 模型状态 | 产品原因码或错误码 | iOS 文案 |
|---|---|---|---|
| 无 active assignment 或 active 为 legacy | 409 | `not_product_activated` | 尚未启用 |
| 当前栈当天无可读发布，且没有该代数的终止失败任务 | 200 / `pending` | `model_result_pending` | 结果尚未就绪 |
| 当前栈无可读发布且该代数任务已失败，或某项产物校验失败 | 200 / `technical_failure` | `technical_failure` | 模型结果读取失败 |
| 支持的旧栈产物缺失或失效，当前 Worker 无法重算 | 200 / `model_update_required` | `model_update_required` | 模型需要更新 |
| 激活栈未匹配冻结发布注册表 | 200 / `unsupported` | `unsupported` | 模型版本需升级 |
| 计算结果证据不足或已过期 | 200 / 对应模型实际状态 | `insufficient_evidence` / `data_stale` 等 | 证据不足 / 数据已过期 |

前五种情况中，200 响应的缺失指标 `value=null`；不会用零补齐。全无 `state=available` 的指标时
`responseState=unavailable_response`，部分为 `partial_response`，全部可用才为 `complete_response`（schema2 六项、schema3 八项）。
数据库或 assignment 元数据等读取故障可返回 `503 athlete_model_unavailable`，不等同于未激活。

schema3 的 `envelope.mode` 与技术状态正交：`estimated` 表示输入缺失下的有限估计（恢复总分≤75、置信度≤0.40；
准备度区间宽度≤10 才给点估计并取保守端），`workout_only` 表示仅有训练负担信息（准备度 `score=null`，疲劳/负荷仍可独立展示）。
计算成功的产物可以因证据不足没有数值；读失败不能叫 estimated。模型级新原因码（`night_data_missing`、`night_sparse`、
`recent_trend_used`、`estimate_expired`、`regularity_collecting`、`session_intensity_unknown`、`submax_prior_used`、
`ability_source_disagreement`、`load_baseline_insufficient`、`short_repetition_unsupported` 等）保留在 `reasonCodes` 供审计；
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
  不自动增加强度或里程；workout_only 不推断恢复正常）。负荷焦点目前只作事实描述，不参与目标建议

## 限额
读 / 写 / 上传三条车道，设备与数据集各一个进程内令牌桶（设备默认：读 120/分、写 60/分、上传 30/分（突发 2）；可用 `RAI_RATE_BUDGETS` 覆盖，
真机验收后端 `scripts/local/device.py` 把上传放宽到 120/分、突发 12）。设备桶先判、数据集桶后判，两者都允许才同时扣额；
任一拒绝都不扣额，所以单设备超额只返回 `device_rate_exceeded`，不会顺带耗尽数据集余额。超限返回 `429` 与 `Retry-After`。
握手 `limits.requests` 返回生效预算，iOS 按其中的上传预算节流日常健康与 VO2 上传。Workout UUID ingest 沿用
独立并发槽与积压额度，不套用日常健康的每分钟预算；遇到 429 按 `Retry-After` 重试。
同步握手在进程内缓存 60 秒，键包含服务 URL、令牌摘要、collector epoch 和会话 ID，并合并并发请求；失败不缓存，
401/403/409 使缓存失效。每次使用仍检查本地会话为 active；连接、激活与恢复校验始终实时握手。
