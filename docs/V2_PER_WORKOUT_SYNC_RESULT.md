# RAI V2 单条 Workout 独立同步：结果报告

> 历史报告：本文件记录 Protocol 2 的 per-Workout page/stream 方案，已被 [Protocol 3 Workout Bundle](../contracts/sync-v3.md) 取代。文中“没有修改 OpenAPI/没有新增 migration/服务端 Go 未改/schema 13”等结论只适用于当时的基线，不代表当前工作树。

日期：2026-10-31（Asia/Shanghai）
基线：`aeb769c82f6af6c47d7cac82874f48ff37f635fc`
只读审查：[V2_PER_WORKOUT_SYNC_AUDIT.md](V2_PER_WORKOUT_SYNC_AUDIT.md)
机器证据：[docs/evidence/per-workout-sync-acceptance.json](evidence/per-workout-sync-acceptance.json)

## 最终状态

```text
PER-WORKOUT SYNC: PASS WITH RISKS
ONE WORKOUT ONE LOGICAL PACKAGE: PASS
MULTI-WORKOUT ISOLATION: PASS
IDEMPOTENCY: PASS
ANCHOR SAFETY: PASS
OUTBOX RECOVERY: PASS
ROUTE / SAMPLE ASSOCIATION: PASS
DURABLE ACK: PASS
P4 PUBLICATION: PASS
REAL DEVICE: NOT VERIFIED
```

说明：**REAL DEVICE 不是 PASS。** 用户在本轮收尾时明确表示“真机暂时无法验收”，因此没有执行 iPhone 12 Pro Max 的真实 HealthKit 读取、真实网络上传或真机 UI 验收。上述 PASS 均来自静态审查、Swift/SyncCore 测试、隔离 PostgreSQL/Go 集成、合成 SyncProbe HTTP→API→PG 链路和模拟器 UI 测试；不得解读为真机通过。

## 本轮范围与未改动项

- 没有修改冻结的 `docs/V2_PHASE3_SYNC_CONTRACT.md`、`contracts/sync-v2.md`、`contracts/openapi-v2.yaml`。
- 没有新增或修改数据库 migration；隔离验收数据库使用现有 schema 13。
- 没有修改 Go 生产代码；服务端复用现有 stream/batch/page/route/association/enrichment/workout-package 协议。
- 没有修改旧 `../running` 工程、旧 `rai-postgres` 或设备验收持久 volume。
- 没有自动 commit、push、部署或切换入口。

## 实现内容

### 1. 客户端任务模型（SyncCore）

新增 `WorkoutSyncTask`、`WorkoutCoverage`、`WorkoutSyncPhase`、`HealthWorkoutSummary`（`ios/RAI/Sources/SyncCore/WorkoutSync.swift`）。

- `NOT_SYNCED / PREPARING / UPLOADING / SERVER_ACKNOWLEDGED / ANALYZING / PUBLISHED / PARTIAL / FAILED` 是本地投影，不替代服务端 batch/page/receipt/analysis 状态。
- `PendingPage.taskID` 把每个 page 绑定到唯一 Workout；`OutboxStore` 增加 task 持久化、task 级重试、启动恢复和 stagedAll 完成门禁。
- `stagedAll` 只在一次 `syncWorkout` 把所有 page 都 stage 后设为 true；进程恢复/后台 drain 只有在 `stagedAll==true` 且该 task 无剩余 page 时才把任务标为 SERVER_ACKNOWLEDGED，避免 Workout 页先完成就误判整包完成。

### 2. 逐 Workout HealthKit 读取与包组装

`ios/RAI/App/HealthKitClient.swift`：

- `recentWorkouts(limit:)` 只读列出真实 HealthKit running Workout，返回日期、室内/户外、距离、时长、平均心率、来源/设备；缺失保持 nil，由 UI 显示 `--`。
- `readWorkout(id:anchor:)` 使用只匹配该 UUID 的 `HKAnchoredObjectQueryDescriptor`，limit 1。
- 样本仍使用 `HKAnchoredObjectQuery`，但 predicate 为 `HKQuery.predicateForObjects(from: workout)`；每种类型、每条 Workout 一个 stream key。
- 来源 `sourceRevision`/`HKDevice` 的**标量 metadata**（`RAI:sourceRevisionVersion`、`RAI:sourceProductType`、`RAI:sourceDevice*`）不包含 serial/UDID；2026-10-31 收敛修正后改为经 `/v2/sync/workout-enrichments` 发送，不进入不可变 Workout 事件。

`ios/RAI/App/SyncCoordinator.swift`：

- `syncWorkout(id:secret:transfers:)` 对单条 Workout 执行：
  - `workout:<uuid>:HKWorkoutTypeIdentifier` 的 Workout 事实批（只含一个 Workout 事件；超大走现有 `workout-packages`）；
  - `workout:<uuid>:<sampleType>` 的样本批/删除事件；
  - 现有 sample series（多值 sample）；
  - 现有 `POST /v2/sync/associations` 的逐样本关联；
  - 现有 route register/chunk/seal；
  - 现有 enrichment（迟到 metadata）。
- 任何请求只属于一条 Workout；关联页、route 页、enrichment 页都带 `taskID`。
- 单个类型读取/关联失败记录 coverage，不使整条 Workout 失败；Workout 事实本身失败才是 FAILED。
- `syncWorkout` 只由用户显式触发的 `SyncModel.syncWorkouts` 调用；没有新增静默后台 HealthKit 读取。

### 3. 同步选择 UI

`ios/RAI/App/ProductViews.swift`（新增 `WorkoutSyncView` / `WorkoutSyncDetail`）和 `RAIApp.swift`：

- 首页同步徽标、我的 → 健康数据同步、同步与诊断页均可进入。
- 列表显示真实 HealthKit 跑步记录的日期时间、室内/户外、距离、时长、平均配速、平均心率、同步状态；缺失显示 `--`。
- 多选后“同步所选”，逐条独立任务执行；每条行有同步/重试/继续/检查状态/更新操作。
- 详情展示 coverage（缺失不等于 0）、task 阶段、Durable ACK 时间、分阶段耗时；DEBUG 下可导出脱敏验收 JSON。
- 旧的“立即同步”全局按钮已从 UI 移除；旧的全局 `SyncCoordinator.sync` 保留用于兼容 drain 与回归，不再由产品 UI 触发全量 HealthKit 读取。

## 验证证据

### Swift

| 门禁 | 结果 |
|---|---|
| `swift test`（`ios/RAI`，SyncCore package） | **PASS**：60 tests / 8 suites |
| `xcodebuild test -only-testing:RAITests`（iPhone 17 Pro 模拟器，iOS 26.5） | **PASS** |
| `xcodebuild test -only-testing:RAIUITests/WorkoutSyncScreenUITests` | **PASS**（12.552s）：入口、屏幕、独立同步文案、权限按钮 |
| `xcodebuild build -configuration Debug -destination generic/platform=iOS` | **PASS** |
| `xcodebuild build -configuration Debug -destination id=iPhone 12 Pro Max`（签名） | **PASS**：签名构建成功；未安装/运行，因用户明确无法进行真机验收 |
| `xcodebuild build -configuration Release -destination generic/platform=iOS` | **PASS**（未签名编译） |

### Go / PostgreSQL

| 门禁 | 结果 |
|---|---|
| `go test -race ./...`（fnOS N100） | **PASS** |
| `go vet ./...`、`go vet -tags=integration ./...` | **PASS** |
| `gofmt -l .` | 通过（无输出） |
| 依赖边界检查 | **PASS**：249 packages，analysis/training 纯域边界通过 |
| `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...`（fnOS 一次性 PG 18.6） | **PASS**：integration 478.500s |
| 新增 `TestPerWorkoutIndependentStreamsAndIsolation` | **PASS**（2.896s）：两条 Workout 独立 stream、独立 sample/association/route、独立 analysis scope、精确重试不新增事实/job、跨 Workout association 冲突被拒且不改变事实 |

新增测试文件：`apps/server/integration/per_workout_sync_test.go`。生产 Go 代码未改。

### 合成 SyncProbe（HTTP → API → 隔离 PostgreSQL → ACK/P4）

`SyncProbe per-workout` 使用与 App 相同的 `OutboxStore`/`BatchBuilder`/`OutboxPipeline`/`RAIPIClient`，对隔离验收 API 上传两条独立 Workout：

```text
workouts=2
samples=2
associations=2
routes=1, sealed=1
route_chunks=1, route_points=10
batches=4, committed=4
pages=4, sealed=4
workout_scopes=2
workout_publications=2
jobs=16, failed_jobs=0
```

两条 Workout 各自有独立的 `workout:HKWorkoutTypeIdentifier:<uuid>` analysis scope 和 publication；一条有 route，另一条没有 route，未发生交叉。隔离数据库为新库 `rai_v2_perworkout`（schema 13），API image ID `sha256:00d54e814eb70e347ff732f1d9e4aebacc2a8c159e8dfd7c525d19b19e1a0b5b`，Worker 在线。

### 真机

**NOT VERIFIED。** 用户明确表示真机暂时无法验收；未执行 iPhone 12 Pro Max 的 HealthKit 授权、真实 Workout 列表、真实样本/route 上传、断网恢复、强杀恢复或真机性能。没有用模拟器/合成数据冒充真机结果。

## 功能验收矩阵（按任务 §10）

| 测试 | 结论 | 证据/说明 |
|---|---|---|
| A 单条记录 | PASS（合成协议）/ 真机 NOT VERIFIED | SyncProbe 每条 Workout 独立 stream/page；Go 集成验证一条 Workout 只有一条事实身份；真机未执行 |
| B 连续两条记录 | PASS（合成协议） | SyncProbe 两 Workout、两独立 sample/association、两条独立 analysis scope、两个 publication；无混合 batch |
| C 重复同步 | PASS（自动） | Go 集成：同 UUID 重试 `unchanged=1`、无新 job/新事实；Swift task/Outbox 重试用原 batch identity |
| D 网络失败 | PASS（现有 P3/P3.5 + 新 task 测试） | 原 Outbox 保留原 batch/envelope/gzip；task 级 retry 不清其他 Workout；真机断网未执行 |
| E 部分数据 | PASS（覆盖模型 + 合成 route 缺失） | 某类型无样本 → `unavailable`/`error:*`；其他类型仍上传；不补 0；真机部分数据未验证 |
| F 迟到 Route/Sample | PASS（设计 + 现有 P3 测试） | per-Workout anchored stream 再次查询；enrichment/association/route 复用；无真实迟到案例时未称真机 |
| G 多条隔离 | PASS（Go 集成 + task 队列） | 一条 stream 冲突/失败不改变另一条 Workout 的 facts/route；UI 循环继续下一条 |

## 性能

- App 任务记录：`workout_read_ms`、`sample_query_ms`、`package_build_ms`、`workout_upload_ms`、`sample_upload_ms`、`association_upload_ms`、`route_query_ms`、`route_upload_ms`、`upload_wire_bytes`、`workout_sync_ms`、`publication_ms`；详情页展示。
- **真实 iPhone 数据的分阶段耗时、实际上传字节和 P4 发布耗时：NOT MEASURED**（真机不可用）。不得从合成 SyncProbe 或模拟器推断真机性能。
- 已知性能风险：真实 31 分钟跑步约 3,800 个单值 samples（历史脱敏结构报告），现有协议每个 sample 一个 association 请求；默认写预算下同步时间会显著长于 RQrun 的单请求 641 KB。隔离验收环境使用提高的写预算完成合成链路；生产默认预算的吞吐未验证，未通过修改 wire contract 掩盖。

## 风险与未验证项

1. **真实 HealthKit 未验证**：授权、Workout 列表、`predicateForObjects(from:)` 的样本/删除行为、真实 route、后台/强杀/断网均未执行；`SyncCoordinator.syncWorkout` 的 HealthKit 适配层只完成编译与代码审查，没有用 mock 伪造 HealthKit 结果来冒充执行。
2. **真实删除检测**：per-Workout anchored query 对删除 sample 的返回行为未在真机验证；客户端不用空查询推断删除。
3. **关联请求量**：上述性能风险；生产可能需要后续经用户确认的批量 association 契约（本轮未改 frozen wire contract）。
4. **Running Cadence**：当前 SDK/构建没有可用 `HKQuantityTypeIdentifierRunningCadence`；不推导，coverage 标 `unsupported`。
5. **旧全局路径**：保留为兼容 drain/回归；产品 UI 已不再触发全量 HealthKit 读取。
6. **UI 真机视觉/无障碍**：只做模拟器屏幕测试，未做真机截图/动态字体/性能。
7. **服务端生产代码未改**，但隔离验收镜像不是当前 HEAD Go 编译产物；当前 HEAD Go 源码已通过完整 race/integration 测试；两者差异需在真机验收时用当前 HEAD 镜像复核。

## 最终结论

- **ONE WORKOUT ONE LOGICAL PACKAGE: PASS**：客户端任务按 Workout UUID 聚合；Workout 事件批只含一个 Workout；样本/关联/route/enrichment/package 全部按单 Workout 归属；服务端复用现有事实/回执/分析，不新增第二套系统。
- **MULTI-WORKOUT ISOLATION: PASS**：独立 stream、独立 page、独立 receipt、独立 analysis scope；单条失败不改变另一条事实。
- **IDEMPOTENCY / DURABLE ACK / OUTBOX RECOVERY / ANCHOR SAFETY: PASS（自动/静态）**：沿用 P3 合同和既有测试，并新增 per-Workout task/page 测试。
- **REAL DEVICE: NOT VERIFIED**：用户本轮无法提供真机验收；不伪造 PASS。
- **不开始 P8，不自动 commit/push/deploy。**
