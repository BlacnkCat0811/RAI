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
- 首页：`GET /v2/product/overview`
- 任务：`GET /v2/jobs/{id}`

## 训练
- 读取：`GET /v2/training/plans/current`、`/plans/{id}`、`/effective[/{version}[/audit|/detail]]`、`/history[/versions]`、`/executions/{id}`
- 写入：`POST /v2/training/plans`、`/plans/{id}/changes`、`/plans/{id}/overrides`、`/executions`、`/executions/{id}/changes`

## 限额
读 / 写 / 上传三条车道，设备与数据集各一个进程内令牌桶（设备默认：读 120/分、写 60/分、上传 30/分；可用 `RAI_RATE_BUDGETS` 覆盖）。
超限返回 `429` 与 `Retry-After`。
