# RAI V2 单条 Workout 独立同步：Phase A 只读架构审查

> 历史审查：本文件针对 Protocol 2 的 page/stream 方案，已被 [Protocol 3 Workout Bundle](../contracts/sync-v3.md) 取代；其中“无需 migration/客户端保留 page 管线”等结论不再适用于当前工作树。

日期：2026-10-31（Asia/Shanghai）
仓库：`/Users/tp/rai`（任务文本中的 `/Users/ta/rai` 在本机不存在）
基线：`aeb769c82f6af6c47d7cac82874f48ff37f635fc`（`main`，工作区除新增的 RQrun 参考文档外无改动）
性质：**只读审查**。本文件不修改冻结合同、migration、服务端代码或真机验收环境。

---

## 0. 结论摘要

| 问题 | 结论 |
|---|---|
| 当前是否已经做到单条 Workout 独立同步？ | **否。** 当前是“按 HealthKit stream 的查询页/批次”同步；一个 Workout 页/批可以包含多条 Workout，样本流是全局类型流，跨 Workout 混合。 |
| 当前哪些层仍采用批量/混合上传？ | iOS Workout 读取页（limit 25）→ `BatchBuilder` 单页；样本类型全局 stream（limit 1000）；服务端普通 batch（最多 1000 events）允许同批多条 Workout；只有超大 Workout package、route、association、enrichment 已经是单对象请求。 |
| 能否在不改变 P3 wire contract 的情况下实现？ | **能。** 服务端 stream 身份唯一键是 `(dataset_id, collector_epoch, sample_type, query_fingerprint)`，允许同类型多个 stream；batch/page/route/association/enrichment/package 语义均不需要变。最小改造是**客户端把读取范围和 stream 作用域改为单条 Workout**，并让每个 Workout 事件批只含一个 Workout。 |
| 是否需要数据库 migration？ | **不需要。** 现有 `sync_streams`、`sync_batches`、`sync_pages`、`workouts`、`health_samples`、`workout_samples`、`workout_routes`、`route_chunks`、`workout_packages`、`jobs`、`analysis_*` 已覆盖。 |
| 需要修改哪些 Swift 文件？ | `SyncCore/OutboxStore.swift`（stream key、task 状态、task 级重试、page→task 归属）、`SyncCore/BatchBuilder.swift`（保持单 Workout 批）、`SyncCore/Wire.swift`（可选：task/coverage 类型）、`App/HealthKitClient.swift`（Workout 列表、per-workout anchored 读取）、`App/SyncCoordinator.swift`（per-workout 任务编排）、`App/RAIApp.swift`（SyncModel、导航）、`App/ProductViews.swift`（入口）、新增 `App/WorkoutSyncView.swift`；测试补齐 `AppTests/NormalizerTests.swift` 的 mock 与 `Tests/SyncCoreTests`。 |
| 需要修改哪些 Go 文件？ | **生产代码不需要。** 只新增隔离 PostgreSQL 集成回归（`apps/server/integration/per_workout_sync_test.go` 或并入现有文件）。 |
| 如何保证 Anchor 和 Outbox 不丢数据？ | 新流程每个锚定查询只覆盖一条 Workout：stream key 为 `workout:<uuid>:<sampleType>`，queryFingerprint 绑定 Workout UUID + 类型。每页返回的 upsert/delete 全部进入 Outbox；页面/分片/回执全部 durable 后才推进该 stream 的 delivered anchor。没有“Workout A 成功就推进包含 B 的页”这种跨对象水位。 |
| 如何处理迟到 Route / Samples？ | 每条 Workout 的每种类型保留独立 `HKAnchoredObjectQuery` anchor；再次同步同一条 Workout 时以原 anchor 查询，新 Sample/Route 作为新页追加；元数据变化走现有 enrichment；关联走现有 `POST /v2/sync/associations`。不创建新 Workout 身份。 |
| 如何实现单条失败与其他记录隔离？ | 每条 Workout 独立 `WorkoutSyncTask`、独立 stream、独立 batch/route/package 身份；任务队列逐条执行，单条错误只标记该任务，后续任务继续。服务端 ACK 也只覆盖该条 Workout 的 facts。 |

**Phase A 判定：不需要修改冻结的 wire contract，不需要 migration。可以进入 Phase B 最小实现。**

---

## 1. 审查范围与方法

已阅读并对照：

- `docs/V2_PHASE3_SYNC_CONTRACT.md`（FROZEN）
- `contracts/sync-v2.md`、`contracts/openapi-v2.yaml`、`contracts/fixtures/*`
- `docs/IMPLEMENTATION.md`、`docs/V2_PHASE3_RESULT.md`、`docs/V2_PHASE4_RESULT.md`、`docs/V2_PHASE6_RESULT.md`
- `docs/V2_DEVICE_ACCEPTANCE_RESULT_2026-10-30.md`（真机历史、保留环境、已知未验证项）
- `docs/RQrun-HealthKit-上传协议.md`（仅作产品行为与上传粒度参考；不复制其固定密钥、URL token、接口地址与不安全字段设计）
- iOS：`SyncCore/*`、`App/SyncCoordinator.swift`、`App/HealthKitClient.swift`、`App/RAIApp.swift`、`App/ProductModel.swift`、`App/ProductViews.swift`、`Tests/*`、`AppTests/*`
- Go：`internal/syncing/*`、`internal/storage/postgres/*`、`internal/httpapi/server.go`、`internal/product/*`、`internal/analysis/*`、`migrations/00001–00013`
- 现有自动化：`go test ./...` 服务端测试结构、`ios/RAI` Swift package 52 项基线测试（本机 `swift test` 通过）

---

## 2. 当前实现的真实上传粒度

### 2.1 身份与状态（已存在，必须保留）

| 层 | 当前实现 | 位置 |
|---|---|---|
| Workout 业务身份 | `(dataset_id, workout_uuid)`，UUID 不可变，内容首次提交后不可变 | `migrations/00003_workouts.sql:16`；`V2_PHASE3_SYNC_CONTRACT.md §4` |
| Sample 身份 | `(dataset_id, sample_type, sample_uuid)` | `V2_PHASE3_SYNC_CONTRACT.md §5.1` |
| Stream 身份 | `(dataset_id, id)`；唯一约束 `(dataset_id, collector_epoch, sample_type, query_fingerprint)` | `migrations/00001_core.sql:34` |
| Batch 身份 | `batchID` + canonical envelope + payload SHA-256；重试返回原 receipt | `V2_PHASE3_SYNC_CONTRACT.md §3.2` |
| 页回执 | pageID/fragmentIndex/fragmentCount 绑定 envelope，最后一片封页 | `contracts/sync-v2.md` 页交付回执 |
| Route | `(routeID, chunkIndex)` + wire SHA-256 + seal manifest | `V2_PHASE3_SYNC_CONTRACT.md §6` |
| Workout package | 单 Workout 的 core + 详情分块 + seal；不占 Workout stream sequence | `contracts/sync-v2.md` schema 9 |
| Analysis | 每个 Workout 的 `analysis.workout` job + dirty scope + publication | `migrations/00012_analysis.sql`、`analysis/*` |

### 2.2 iOS 当前读取路径

`App/SyncCoordinator.swift:sync` 的做法：

1. 逐个 `HealthKitClient.types`（Workout + 10 个 quantity 类型）读取。
2. 每个类型用自己的全局 stream（`store.stream(type: type.identifier, query: ...)`，`SyncCoordinator.swift:59`）。
3. Workout 页 `limit = 25`（`HealthKitClient.read`），一页最多 25 个 `HKWorkout`。
4. 页内所有事件（多 Workout + 多 Sample + 多 DeletedObject）合并进同一个 `events` 数组（`SyncCoordinator.swift:62–91`）。
5. `BatchBuilder.page` 把这一页编码为最多 1000 events/分片的 NDJSON 批次（`BatchBuilder.swift`），因此**一个 batch/页可以包含多条 Workout**。
6. 只有超大 Workout（`>60_000` 字节或 details 超限）走 `Supplements.workoutPackage` 的单 Workout 分块包（`SyncCoordinator.swift:76–79`）。
7. Route、association、enrichment 已经是单 Workout/单对象本地页（`routes`、`associations`、`enrichment`）。

### 2.3 服务端当前准入

- `syncing.StreamRequest.Validate` 只校验 `streamID`、`sampleType`、`queryFingerprint` 形状，不要求“每个 sampleType 全局唯一 stream”（`protocol.go:130–145`）。
- `sync_streams` 唯一键包含 `query_fingerprint`（`00001_core.sql:34`），因此同一 sample type 的不同查询范围可以是不同 stream。
- `Store.CommitUpload` 逐事件校验 `e.Type == stream.SampleType`（`store.go:355–357`），但对同批 Workout 数量没有上限（只受 `MaxEvents=1000`、`MaxWorkoutDetails=2000` 约束）。
- 服务端完全支持“一个批次只含一个 Workout”，也支持“多条 Workout 各自的独立 stream”。**服务端不需要知道客户端选择了哪条 Workout。**

### 2.4 结论

当前没有“单条 Workout 独立上传”的产品语义。它有的是：

```text
HealthKit type stream page (可能含多条 Workout/Sample)
        ↓
一个不可变 page + N 个 fragment batch
        ↓
一个 durable page receipt
```

RQrun 的“一条 Workout 一个载荷”在 RAI 当前实现中只在超大 Workout package 路径局部存在，不是主路径。

---

## 3. 需求 → 现状 → 差距映射

| 需求 | 现状 | 差距/结论 |
|---|---|---|
| 一条 Workout = 一个同步任务 | 无 Workout 级任务；只有 stream/page | 需新增客户端 `WorkoutSyncTask` 聚合 |
| 一条 Workout = 一个逻辑上传包 | 页/批可混合多 Workout | 需把 Workout 读取页限制为单 Workout，并把样本流按 Workout 作用域 |
| 用户选择一条/多条 | 无选择界面；“立即同步”走全局类型流 | 需新增 HealthKit Workout 列表与多选任务队列 |
| 单条状态 NOT_SYNCED…FAILED | 只有页级 pending/error、全局 lastACK | 需在本地 Outbox 增加 task 状态投影，映射现有 receipt/analysis 状态 |
| 只重试失败记录 | `retryBlocked()` 全量重试 | 需 task 级重试 |
| 查看已同步记录 | 服务端 `/v2/workouts` 可查；客户端仅首页/运动页 | 需把 HealthKit 列表与服务端列表合并/标注 |
| 重新检查服务端状态 | 分析接口存在 `GET /v2/analysis/workouts/{id}` | 需 UI 查询并映射 PUBLISHED/PARTIAL |
| 单条失败隔离 | 页级失败互不影响，但同页多 Workout 共享 page | 需 per-Workout stream/task，彻底隔离 |
| Anchor 不能因选中一条而推进整页 | 当前页可含多条 Workout，页 ACK 才推进；若只上传一条，其他事件仍在同一页无法独立 ACK | 需 per-Workout 锚定查询，页天然只属于一条 Workout |
| 迟到 Sample/Route | 已有每 Workout 的 association/route 查询与 anchor | 复用；Sample 迟到需 per-Workout sample anchor |
| 不新增第二套同步系统 | — | 复用同一 `OutboxStore`、`OutboxPipeline`、batch/page/route/association/enrichment/package 协议；只改变读取作用域与任务聚合 |
| 不修改冻结 wire contract | — | 不需要：现有 stream 身份、batch、page、receipt、route、association、enrichment、package 均可复用 |
| 不新增 migration | — | 不需要 |

---

## 4. 最小兼容改造方案（Phase B 设计）

### 4.1 核心概念：WorkoutSyncTask（本地聚合，不是新服务端实体）

```text
WorkoutSyncTask(workoutID)
  ├─ workout fact page      (workout:<uuid>:HKWorkoutTypeIdentifier)
  ├─ sample pages           (workout:<uuid>:<sampleType> × N types)
  ├─ association pages      (association:<uuid>:<type>)
  ├─ route page             (route:<uuid>)
  └─ enrichment page        (enrichment:<uuid>)
```

- 任务只存客户端 Outbox（`LocalState.workoutTasks`），不需要服务端新表/新接口。
- 每个 page 增加 `taskID` 字段；任务完成 = 该 taskID 的所有 page 都已 durable receipt 且从 Outbox 移除。
- 任务的“上传包”是这些 page 的并集；**每个 HTTP 请求只属于一条 Workout**。
- Workout 事实仍用现有 batch/page（一个 batch 一个 Workout 事件）；超大 Workout 仍用现有 `workout-packages`；样本仍用现有 sample batch；路线仍用 route register/chunk/seal；关联仍用 association；迟到元数据仍用 enrichment。

### 4.2 Stream/查询作用域

新增 stream key（仅客户端字符串，服务端不解释）：

```text
workout:<workoutUUID>:HKWorkoutTypeIdentifier
workout:<workoutUUID>:HKQuantityTypeIdentifierHeartRate
workout:<workoutUUID>:HKQuantityTypeIdentifierRunningPower
...
```

`queryFingerprint = sha256("rai-v2:per-workout:<workoutUUID>:<sampleType>:visible")`。

- `streamID` 随机生成后持久化在 Outbox；重试复用原 streamID/queryFingerprint。
- 每个 stream 独立 `clientSeq`、`lastBatchID`、`deliveredAnchor`。
- 服务端 `sync_streams` 的唯一键允许同一 sampleType 的多个 queryFingerprint；不触发 `stream_identity_conflict`。
- 这不是“每个 sample UUID 一个 stream”，也不是“每个 route 一个 stream”；是“每条用户选择的 Workout × 类型”一个查询流，符合“单条 Workout 独立”的最小语义。

### 4.3 Workout 事实

- 首次同步：`HKAnchoredObjectQuery`，`predicateForObject(with: workout.uuid)`，limit 1，anchor 为该 stream 的 delivered anchor。
- 首次页返回该 `HKWorkout`；`Normalizer.event(workout)` 生成事件；`BatchBuilder.page` 只放这一条事件。
- 再次同步：同一 anchor 查询只返回新增/删除对象；无变化时 `finishEmptyPage` 本地推进 anchor，不产生空 batch（沿用现有空页策略）。
- Workout 删除：锚定查询返回 `HKDeletedObject` 时生成 `delete` 事件（现有四字段 wire），由服务端 tombstone；不根据“读不到”推断删除。
- 超大 Workout：沿用现有 `Supplements.workoutPackage`；任务完成条件是 package seal receipt，不是注册/分块 receipt。

### 4.4 样本

- 对 `HealthKitClient.quantityTypes` 中每个类型：
  - `HKAnchoredObjectQuery`，`predicateForObjects(from: workout)`，limit 1000，anchor 为该 Workout×类型 stream 的 delivered anchor。
  - 页内每个 `HKQuantitySample` → 现有 `Normalizer.event`；多值 sample（`count > 1`）→ 现有 `health.series` 分块/封口。
  - 删除对象 → 现有 `Normalizer.deletion` 四字段事件。
  - 页未满则结束；页满继续下一页（同一 stream 顺序）。
- 某类型无样本：`finishEmptyPage` 本地推进 anchor，并把 coverage 标为 `unavailable`；不补 0。
- 某类型查询失败：记录 `error:<code>`，**不使整条 Workout 失败**；继续其他类型。
- 不把 cadence 从 step/time 推导：当前 SDK/构建没有 `HKQuantityTypeIdentifierRunningCadence` 可用类型（`HealthKitClient.swift` 注释与 `identifiers` 列表），coverage 明确标为 `unsupported`，不造值。

### 4.5 关联（sample ↔ workout）

- 复用现有 `POST /v2/sync/associations`，每个 `(sampleType, sampleUUID, workoutUUID)` 一个请求；`associationID` 持久化在 Outbox 的 request 文件中，重试不变。
- 在同一 Workout 的样本批次 durable 后执行；`association_unavailable` 视为等待新事实（现有 pipeline 的 `waitingForFactGeneration`）。
- 关联页属于该 Workout 的 taskID；其他 Workout 不共享。
- 已知性能约束：一条 30–40 分钟跑步可能有数千个样本，因此关联请求数量大。Phase D 使用隔离验收环境已有的提高写预算（当前 `rai-v2-device` compose 配置 `write.device.perMinute=600`）记录真实耗时；生产默认预算下的吞吐风险在结果报告如实列出。**本次不为此改 wire contract。**

### 4.6 Route

- 复用现有 `routes(workout:reference:scope:pipeline:)`：
  - `HKAnchoredObjectQuery` on `HKSeriesType.workoutRoute()`，`predicateForObjects(from: workout)`，limit 1；
  - `HKWorkoutRouteQueryDescriptor` 读点；
  - 现有 `RouteSpool` 分块（1000 点/块，最大 5000）；
  - 现有 register/chunk/seal。
- 无 route：本地空页推进 route anchor，coverage `unavailable`；后续新增 route 由同 anchor 查询发现。
- 不把坐标写日志/证据；只记录点数、chunk 数、hash、耗时。

### 4.7 迟到数据 / 补充 / 删除

| 场景 | 机制 |
|---|---|
| Workout 先出现，Route 后出现 | 同一 Workout route anchor 再次查询；新 route → 新 register/chunk/seal；Workout 已存在，route seal 合法 |
| Workout 先出现，Samples 后出现 | 同一 Workout×类型 sample anchor 再次查询；新 sample → 新 batch；父 Workout 已存在 |
| 部分 Sample 暂不可用 | coverage `unavailable`/`error:*`；不阻塞其他类型；任务仍可 ACK |
| 同 Workout 后续新 Metadata | 再次读取 Workout 对象；现有 `enrichment(previous:event:reference:)` 只允许 nil→value/同值，冲突不覆盖 |
| 用户删除 Workout | 锚定 Workout 查询的 `HKDeletedObject` → delete 事件；服务端 tombstone + 级联 route/association |
| 用户删除 Sample | Workout×类型锚定查询的 `HKDeletedObject` → 四字段 delete 事件；旧 upsert 被 tombstone 抑制。若 HealthKit 对 `predicateForObjects(from:)` 的删除对象不返回，客户端无法凭“查询为空”推断删除；该边界在真机验收中单独记录，不用合成结果冒充 |

### 4.8 状态投影（不新建服务端状态机）

| UI 状态 | 依据 |
|---|---|
| `NOT_SYNCED` | 本地无 task，且服务端 `/v2/workouts` 无该 UUID |
| `PREPARING` | task 已创建，尚未有 durable receipt 的页 |
| `UPLOADING` | task 存在 pending page（retryable 或正在上传） |
| `SERVER_ACKNOWLEDGED` | task 的所有 page receipt 均为 committed/sealed，且无缺失 coverage |
| `ANALYZING` | 已 ACK；`GET /v2/analysis/workouts/{id}` 返回 `404 analysis_not_ready` 或 job queued/running |
| `PUBLISHED` | 分析 publication 存在且指标 quality 均为 available |
| `PARTIAL` | 已 ACK，但 coverage 有 unavailable/error，或 publication 指标 quality 为 partial/insufficient |
| `FAILED` | 任一 page 非 retryable error，或 Workout 不可读/不可见 |

底层协议状态（registered/committed/sealed/queued/published）不被重命名或替换；UI 只是投影。

### 4.9 失败隔离与任务队列

- `SyncModel.sync(ids:)` 对用户选择顺序逐条执行 `syncWorkout`；每条独立 task。
- 单条 Workout 的 read/build/upload 错误只更新该 task；循环继续下一条。
- 同一条内某类型失败只记录 coverage；只有 Workout 事实本身失败才 FAILED。
- 网络失败：pending page 保留原 batchID/envelope/gzip 文件；task 状态 UPLOADING/FAILED(retryable)，`重试` 只重置该 task 的页。
- 进程重启：Outbox state 保留 pages/tasks/streams；启动 drain 恢复原身份；PREPARING/UPLOADING 无 page 的残任务标记为可重试失败。

### 4.10 Anchor 安全论证（逐条对应任务 §8）

1. HealthKit 返回新 anchor → 只写入对应 Workout×类型的 `PendingPage`。
2. 查询结果（upsert + delete）全部持久化在 Outbox；不允许只保存用户选中的事件而丢弃同页其他事件——因为页的查询谓词已限定到单条 Workout，不存在“其他 Workout 的同页事件”。
3. 每个事件都有明确归属：样本/删除属于 `workout:<uuid>:<type>` stream；Workout 属于 `workout:<uuid>:HKWorkoutTypeIdentifier`。
4. 单条 Workout 可独立上传：其 stream 的 `clientSeq` 从 1 开始，不受其他 Workout 影响。
5. 其他 Workout 保持 `NOT_SYNCED` 或自己的 task 状态；不会被读取/上传/推进 anchor。
6. 页面/分片/回执仍按 P3 合同校验（manifest、seal、receipt、sequence）。
7. Delivered Anchor 只在该 stream 的最后一个分片 committed + 页 sealed 后推进（现有 `OutboxStore.finish`）。
8. 不存在“上传 Workout A 成功 → 推进包含 B 的整页”的路径，因为页/stream 不含 B。

### 4.11 幂等与不可变

- Workout 重试复用 `workout:<uuid>:...` stream、原 batchID、原 envelope、原 gzip 文件；服务端按 `(dataset_id, workout_uuid)` 返回原回执/unchanged。
- 样本重试复用原 stream 的 `clientSeq`/batchID/payload；服务端按 `(dataset_id,type,uuid)` 幂等。
- 路由/关联/补充的 ID 首次生成后写入 Outbox payload 文件；重试不重新生成。
- pending 页内容永不修改；新的合法版本走 enrichment/新 route/新 sample batch，不覆盖 pending 包。
- 无本地草稿编辑功能；如未来增加草稿，必须在 `stage` 之前完成，`stage` 后视为不可变。

---

## 5. Wire contract / migration 差异评估

### 5.1 Wire contract

**无差异。** 复用：

- `POST/GET/PUT /v2/sync/streams`、`/v2/sync/batches`、`/v2/sync/pages`
- `POST /v2/sync/associations`
- `POST/GET /v2/sync/workout-enrichments`
- `POST/GET/PUT /v2/routes` + chunk + seal + delete
- `POST/GET/PUT /v2/sync/workout-packages` + seal
- `GET /v2/workouts`、`GET /v2/analysis/workouts/{id}`、`GET /v2/jobs/{id}`

客户端只改变 `streamID`/`queryFingerprint` 的取值和读取谓词；这些字段在合同里本来就是客户端生成的不透明身份。服务端无需为“单条 Workout”新增字段。

### 5.2 PostgreSQL migration

**无。** 所需表/约束全部已存在：

- `sync_streams` 已允许同 sampleType 多 fingerprint；
- `sync_batches`/`sync_pages` 不绑定 Workout；
- `workouts`/`health_samples`/`workout_samples` 已按 UUID 幂等；
- `workout_routes`/`route_chunks`/`route_points` 已按 routeID 绑定 Workout；
- `workout_packages` 已按 workoutID 绑定；
- `jobs`/`dirty_scopes`/`analysis_*` 已按 Workout scope。

不新增 migration，不动 `00001–00013`。

### 5.3 服务端 Go

**生产代码不改。** 现有通用 batch/page/route/association/enrichment/package 已足够。Phase C 只新增隔离 PostgreSQL 集成测试，证明：

- 同一 sampleType 两个不同 fingerprint 的 stream 可独立提交；
- 两条 Workout 分属两个 stream，互不阻塞；
- 每条 Workout 的 batch 只含一个 Workout 事件；
- 两条 Workout 的 sample/association/route 不交叉；
- 重复上传同 UUID 返回原 receipt/unchanged；
- 单条失败不影响另一条的 jobs/publication；
- late sample/route、tombstone、ACK、anchor 语义不变。

---

## 6. 受影响文件清单（Phase B 预估）

### Swift

| 文件 | 修改 |
|---|---|
| `ios/RAI/Sources/SyncCore/OutboxStore.swift` | stream 以 key 索引；`PendingPage.taskID`；`WorkoutTask` 持久化；task 级 retry/reconcile |
| `ios/RAI/Sources/SyncCore/Wire.swift` | 可选：`WorkoutTask`/`Coverage`/状态枚举（若放 SyncCore） |
| `ios/RAI/Sources/SyncCore/BatchBuilder.swift` | 保持接收显式 stream；不改变 batch wire；增加单 Workout 断言/测试钩子（可选） |
| `ios/RAI/App/HealthKitClient.swift` | `recentRunningWorkouts`；`readWorkout(id:anchor:)`；per-workout read 已存在；coverage 错误归类 |
| `ios/RAI/App/SyncCoordinator.swift` | `syncWorkout(id:)`、task 编排、per-workout stream key、coverage/metrics、analysis 查询 |
| `ios/RAI/App/RAIApp.swift` | `SyncModel` 列表/选择/队列/状态；导航到新 View；诊断页去掉“立即同步”全局按钮 |
| `ios/RAI/App/ProductViews.swift` | Profile/Home 入口 |
| `ios/RAI/App/WorkoutSyncView.swift`（新增） | 健康同步选择页、状态、重试、检查状态 |
| `ios/RAI/AppTests/NormalizerTests.swift` | mock 协议新增方法默认实现，保持旧测试 |
| `ios/RAI/Tests/SyncCoreTests/*`（新增用例） | task 状态、stream key、task retry、单 Workout 批约束、restart 恢复 |

### Go

| 文件 | 修改 |
|---|---|
| `apps/server/integration/per_workout_sync_test.go`（新增） | 隔离 PostgreSQL 集成测试；生产代码不变 |

### 文档

| 文件 | 修改 |
|---|---|
| `docs/V2_PER_WORKOUT_SYNC_AUDIT.md` | 本文件 |
| `docs/V2_PER_WORKOUT_SYNC_RESULT.md` | Phase E 结果 |
| `docs/evidence/per-workout-sync-acceptance.json` | Phase E 机器证据 |
| `docs/IMPLEMENTATION.md` | 阶段状态与真实证据/未验证项 |

---

## 7. 风险与限制（必须在结果报告中保留）

1. **关联请求量**：一条真实 31 分钟跑步有约 3,800 个单值样本（历史脱敏结构报告：HR 349、distance 704、energy 693、power 667、speed 695、step 706）。现有协议每个 sample 一个 association 请求，默认写预算下会显著拉长同步时间。隔离验收环境已配置较高写预算（600/min），结果报告记录真实耗时；生产默认预算的吞吐是已知风险，不通过修改 wire contract 来掩盖。
2. **Running Cadence**：当前构建/SDK 没有可用的 `HKQuantityTypeIdentifierRunningCadence`；客户端不推导步频，coverage 标记 `unsupported`。这与 RQrun 抓包中的 `cadences_list` 不同——RQrun 可能自行计算，RAI 不复制第三方推导值。
3. **Sample 删除检测**：per-workout anchored query 是否对已删除 sample 返回 `HKDeletedObject` 取决于 HealthKit 谓词行为；必须真机验证。若不可用，结果报告明确“该边界未验证”，不根据空查询推断删除。
4. **Stream 行增长**：每 Workout×类型一个 stream（约 10–11 行/Workout）。316 条 Workout 约 3,500 行；属于可接受规模，但长期需要清理/归档策略。无 migration，不影响正确性。
5. **旧全局管线**：`SyncCoordinator.sync` 的全局类型流代码保留用于兼容/回归，但产品 UI 不再触发全量 HealthKit 读取；“立即同步”改为打开单条选择页。旧 pending 页仍由同一 pipeline drain，不会丢数据。
6. **服务端没有“同步任务”实体**：单条任务状态是客户端投影 + 服务端 receipt/analysis 查询；这不是第二套服务端状态机。服务端仍只认 batch/receipt/analysis。
7. **真机性能**：历史真机验收曾因设备不可用未完成；本次设备已连接（iPhone 12 Pro Max），但真实 HealthKit 授权、网络、后台/强杀、删除场景仍需逐项执行。不得用合成数据冒充。

---

## 8. Phase B/C/D 验收映射

| 测试 | 实现/证据 |
|---|---|
| A 单条记录 | UI 选一条真实 Workout → 一个 task；workout batch 1 条事件；样本按类型分批；route seal；DB 一行 workout；analysis publication |
| B 连续两条 | 两个 task、两个 Workout stream、两个 UUID、两组 receipt、两个 publication；无混合 batch |
| C 重复同步 | 原 anchor 查询无新增；不产生新 batch；同 UUID receipt/unchanged；`GET /v2/workouts/{id}` 稳定 |
| D 网络失败 | 断网/停 API 后 Outbox 保留 pending page；恢复后原 batchID/envelope/gzip 重试；无重复 facts |
| E 部分数据 | 真实 Workout 缺 route 或某类型样本；coverage unavailable；其他类型/Workout ACK |
| F 迟到 Route/Sample | 真机观察；无真实案例则隔离自动测试 + 明确标注“合成/隔离”，不称真机 |
| G 多条隔离 | 一条注入失败、一条成功；失败 task 不影响成功 task 的 receipts/publication |
| 回归 | 现有 Go/Swift/PG 测试全部继续通过；新增 per-workout 集成测试 |

---

## 9. 决策记录

- **DECISION-1**：不新增服务端 “workout sync task” 表/接口；任务只存在于客户端 Outbox。理由：服务端已有 facts + receipt + analysis，新增任务实体会产生第二套状态系统，且无必要。
- **DECISION-2**：不修改 batch/page/route/association/enrichment/package wire；只改变客户端 stream 作用域与 Workout 事件批组成。理由：服务端唯一约束和校验已允许。
- **DECISION-3**：不新增 migration；若 Phase B 发现必须新增，停止并提交差异设计，等待用户确认。
- **DECISION-4**：保留旧全局同步代码作为兼容/回归路径，但产品默认流程只走用户选择的单条任务；不在后台静默读取 HealthKit。
