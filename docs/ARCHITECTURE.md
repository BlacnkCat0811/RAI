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
   在同一事务里写入事实、脏范围、`analysis.workout` 任务并把行标为 `committed`；暂时性错误按指数退避（上限 10 分钟）重试，
   语义错误立即 `rejected`。
6. **分析**：Worker 处理 13 种 job kind（负荷、体能疲劳、恢复、准备度、VO2、训练计划等）。每次领取任务和每 15 秒的日推进都重新读取时钟，
   所以跨午夜后“今天”会正常推进。
7. **展示**：App 读取 `/v2/product/overview`、`/v2/workouts`、`/v2/analysis/*` 等；首页并发读取 6 个接口，并缓存上一次快照。

## 日常健康与 VO2

HRV、静息心率、睡眠、VO2 走 HealthKit 锚点 + 本地 Outbox（`OutboxStore`），观察者只记录“唤醒标记”，
上传由「同步日常健康」触发（后台 URLSession 上传，见 `BackgroundTransfers`）。

## 服务端目录

- `cmd/rai`：入口与运行模式；`internal/httpapi`：路由；`internal/syncing`：协议、校验、限额（内存令牌桶 `Limiter`）；
  `internal/storage/postgres`：事务与查询；`internal/jobs`：Worker；其余为分析算法与训练计划（`trainingload`、`recovery`、`readiness`、`vo2max`、`training` …）。
- 管理工具（迁移、新建数据集、运维）只在 `internal/admin`，不得被 `application/httpapi` 导入（`scripts/check-boundaries.py` 检查）。

## 约束

- 原始载荷落库、服务端只解释已知字段、未知字段保留不丢。
- 不做删除同步：v4+ 客户端不上传删除，服务端不根据快照缺席推断删除。
- 同步不依赖备份、恢复、迁移、全库摘要或全量重算。
- 个人自用：单用户、单服务器；请求限额只在进程内存里，重启即重置。
