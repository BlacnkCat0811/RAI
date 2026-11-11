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
- `POST /v2/sync/daily-facts` / `GET /v2/sync/daily-facts/coverage` — HRV、静息心率、睡眠
- `POST /v2/sync/vo2max-facts` / `GET /v2/sync/vo2max-facts/coverage` — 外部 VO2
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
  - `/athlete/history?metric=trainingLoad` 的点以急性负荷为 `value`，`components` 携带 `chronic`、`balance`、`dailyLoad`
  - 运动模型的激活、产物缺失与读取故障分别表达，见下方状态表
- 首页：`GET /v2/product/overview`
- 任务：`GET /v2/jobs/{id}`

### 运动模型状态

`GET /v2/analysis/athlete` 的 HTTP 成功表示已返回模型快照，不能据此认定每项指标都有可展示数值。
各项以 `envelope.modelStatus`、`productReasonCodes`、`state`、`freshness` 和 `technicalError` 表达实际状态。

| 条件 | HTTP / 模型状态 | 产品原因码或错误码 | iOS 文案 |
|---|---|---|---|
| 无 active assignment 或 active 为 legacy | 409 | `not_product_activated` | 尚未启用 |
| 当前栈当天无可读发布，且没有该代数的终止失败任务 | 200 / `pending` | `model_result_pending` | 结果尚未就绪 |
| 当前栈无可读发布且该代数任务已失败，或某项产物校验失败 | 200 / `technical_failure` | `technical_failure` | 模型结果读取失败 |
| 支持的旧栈产物缺失或失效，当前 Worker 无法重算 | 200 / `model_update_required` | `model_update_required` | 模型需要更新 |
| 激活栈未匹配冻结发布注册表 | 200 / `unsupported` | `unsupported` | 模型版本需升级 |
| 计算结果证据不足或已过期 | 200 / 对应模型实际状态 | `insufficient_evidence` / `data_stale` 等 | 证据不足 / 数据已过期 |

前五种情况中，200 响应的缺失指标 `value=null`；不会用零补齐。全无 `state=available` 的指标时
`responseState=unavailable_response`，部分为 `partial_response`，六项均可用才为 `complete_response`。
数据库或 assignment 元数据等读取故障可返回 `503 athlete_model_unavailable`，不等同于未激活。

历史接口继续绑定 active assignment：支持的旧栈按该发布配置校验，未知版本返回 `409 model_version_unsupported`；
`view=as_known` 返回 `409 historical_input_unavailable`，不能由当前发布指针重建过去的已知视图。
历史分页游标绑定数据集、指标、栈和 assignment version 等上下文，切换后旧游标返回 `409 history_cursor_mismatch`。

iOS 将响应解码失败或未知顶层 schema 记录为本地 `athlete_response_invalid`，保留上次成功结果并显示刷新失败及结果时间；
首次加载尚无成功结果时显示未就绪或读取失败。只有明确收到 `not_product_activated` 才按未启用处理并清除缓存模型。

## 训练
- 读取：`GET /v2/training/plans/current`、`/plans/{id}`、`/effective[/{version}[/audit|/detail]]`、`/history[/versions]`、`/executions/{id}`
- 写入：`POST /v2/training/plans`、`/plans/{id}/changes`、`/plans/{id}/overrides`、`/executions`、`/executions/{id}/changes`

## 限额
读 / 写 / 上传三条车道，设备与数据集各一个进程内令牌桶（设备默认：读 120/分、写 60/分、上传 30/分；可用 `RAI_RATE_BUDGETS` 覆盖）。
超限返回 `429` 与 `Retry-After`。
