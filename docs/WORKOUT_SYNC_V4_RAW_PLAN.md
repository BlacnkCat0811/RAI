# Workout 同步 v4 原始载荷重写计划（2026-11-09）

## 1. 背景与目标

当前 v4 是 v3 的紧凑列式投影：客户端先做来源分类、metadata 白名单、coverage 状态机、删除 diff 和 source 表，服务端再做严格校验。它对“快速把 Apple Watch 跑步记录导进来、服务端解析展示”这个个人使用目标过重：

- 任一流读取失败、任一 metadata 键未知、任一样本无法编码，都可能让整包失败或需要人工重试。
- 删除、coverage、200 天完整性证明与个人使用无关，却占据上传前后的大量状态和等待时间。
- 客户端承担了本应由服务端解释的语义，协议形状也更像“可审计导入”而不是“原始记录上传”。

本轮把 v4 重写为“原始记录上传”：

> **原始载荷落库；服务端只解释已知字段；未知字段保留不丢；不做删除同步。**

v4 此前未在任何部署中开启（服务端 `SYNC_V4_ENABLED` 未配置，客户端需显式 opt-in），所以直接原地重定义，不新增协议版本、不做回滚、不迁移历史数据。

## 2. 设计原则

1. `workout_imports.payload` 仍是客户端提交的精确 JSON 字节，服务端不改写、不注入字段；它就是未知字段的权威副本。
2. 服务端把能识别的字段投影到 `workouts` / `health_samples` / `workout_samples` / `workout_series` / `workout_routes`；无法识别的字段、流、样本只留在原始载荷中，不阻断整包。
3. 单位、时间、UUID、来源分类由服务端解释；客户端只负责把 HealthKit 值按双方共享的规范单位序列化。
4. 不做删除同步：客户端不上传 `deletions`/`routeDeletions`，也不根据 HealthKit 删除事件调用 DELETE；服务端保留既有删除能力供 v3/管理路径使用，但 v4 不产生删除。
5. 不引入 coverage 状态机、observation 块、metadata registry 版本、source 表或 200 天完整性 manifest。
6. 事务边界不变：原始载荷、样本/关联/series/route、回执、change event、dirty scope、analysis job 仍在同一事务提交；同一字节重试仍返回原回执。

## 3. 新 v4 线格式

一个 Workout 一个不可变 bundle，沿用现有 multipart + gzip + `bundleSHA256` 回执协议。

```json
{
  "protocolVersion": 4,
  "datasetID": "uuid",
  "datasetEpoch": "uuid",
  "collectorEpoch": "uuid",
  "workoutID": "uuid",
  "startUs": 1790000000000000,
  "endUs": 1790003600000000,
  "source": {
    "bundleID": "com.apple.health",
    "name": "Apple Watch",
    "version": "11.0",
    "productType": "Watch7,1",
    "operatingSystemVersion": "11.0",
    "device": { "manufacturer": "Apple", "model": "Watch" }
  },
  "metadata": { "HKTimeZone": "Asia/Shanghai", "unknownKey": "preserved" },
  "workout": {
    "activityType": 37,
    "durationUs": 1800000000,
    "totalDistance": { "value": 8000.0, "unit": "m" },
    "statistics": [],
    "events": [],
    "activities": []
  },
  "streams": [
    {
      "type": "HKQuantityTypeIdentifierHeartRate",
      "unit": "count/min",
      "status": "complete",
      "error": null,
      "samples": [
        {
          "uuid": "uuid",
          "startUs": 1790000000000000,
          "endUs": 1790000005000000,
          "value": 120,
          "source": { "bundleID": "com.apple.health", "productType": "Watch7,1" },
          "metadata": { "HKTimeZone": "Asia/Shanghai", "unknownKey": "preserved" }
        }
      ]
    }
  ],
  "series": [
    {
      "type": "HKQuantityTypeIdentifierHeartRate",
      "sampleUUID": "uuid",
      "unit": "count/min",
      "points": [[1790000000000000, 1790000000500000, 121]]
    }
  ],
  "routes": [
    {
      "routeID": "uuid",
      "source": { "bundleID": "com.apple.health", "productType": "Watch7,1" },
      "points": [[1790000000000000, 31.2, 121.4, null, null, null, null, null]]
    }
  ]
}
```

字段规则：

| 字段 | 必需 | 服务端解释 |
| --- | --- | --- |
| `protocolVersion` | 是 | 必须为 4 |
| `datasetID` / `datasetEpoch` / `collectorEpoch` / `workoutID` | 是 | UUID 身份与 fencing |
| `startUs` / `endUs` | 是 | Workout 窗口 |
| `source` | 是 | Workout 来源；分类必须为 `apple_watch` |
| `workout` | 是 | 已知字段投影；未知字段忽略并保留在原始载荷 |
| `metadata` | 否 | Workout 级原始 metadata；按原 map 存回 `workouts.payload` |
| `streams` | 否 | 已注册 quantity/category 类型才导入 |
| `streams[].unit` | 否 | 缺省用服务端规范单位；声明值必须匹配，否则跳过该流 |
| `streams[].status` / `error` | 否 | 仅诊断，不参与导入判定 |
| `streams[].samples[]` | 否 | 每个样本独立校验；非法样本跳过 |
| `samples[].source` | 是 | 每个样本自己的来源；不可分类或非 Apple Watch 的样本跳过 |
| `samples[].metadata` | 否 | 原样存入 `health_samples.metadata` |
| `series[]` | 否 | 父样本必须已导入；非法点跳过 |
| `routes[]` | 否 | 非法点跳过；来源非 Apple Watch 的 route 跳过 |

明确移除：`metadataRegistryVersion`、`sources` 表、`kind`、`uuids` 平行数组、`src` 索引、`deletions`、`routeDeletions`、`observation`、`coverage`、source 索引。未知顶层/对象字段一律忽略，不报错。

## 4. 服务端改动

### 4.1 解码器

`apps/server/internal/syncing/workout_bundle_v4.go` 重写：

- 只对 UTF-8、重复键、顶层必需字段和已知字段类型做边界检查；不再 `DisallowUnknownFields`，不再 `RejectNulls`。
- 顶层 `source`、`metadata`、每个 stream 的 `type`/`unit`/`samples`，每个 sample 的 `uuid`/`startUs`/`endUs`/`value`/`source`/`metadata`，series 的 `type`/`sampleUUID`/`points`，routes 的 `routeID`/`source`/`points`。
- 宽容规则：
  - 未知 stream 类型、未知 unit、重复 stream 类型 → 跳过该流。
  - 单个样本 UUID 非法、时间越界、start>end、value 非有限数、来源无法解析 → 跳过该样本；重复 UUID 保留第一个。
  - 未知 `workout.statistics` 类型跳过；已知类型按 `Units` 过滤后按类型排序，保证不因一个未知统计项失败。
  - series 父样本不存在、unit 不匹配、点非法 → 跳过该点/该 series。
  - route 来源不合格、routeID 非法/重复、点非法 → 跳过该 route/点。
  - 样本数/点数/route 数超限 → 413 `bundle_too_large`（保护资源，不静默截断）。
- Workout 核心（`activityType`、`durationUs` 等已知字段）仍严格校验；缺失或非法直接 422 `invalid_workout`。
- 新建 `BundleSourceTable`（`Workout`/`Samples`/`Routes` 完整 source 描述），挂在 `WorkoutBundle` 上，供回执分类、分析和 feature 输入复用；完整 source 描述不进入 normalized facts 之外的 ACK。
- 服务端自行生成最少量的来源证据：不回写原始载荷，不合成 observation；下游按需从原始载荷解析。

### 4.2 校验

`apps/server/internal/syncing/workout_bundle.go` / `bundle_observation.go`：

- `validateMetadataRegistryVersion()` 对 protocol 4 直接返回 nil（v4 没有 registry 版本）。
- `validateObservation()` 对 protocol 4 直接返回 nil（v4 没有 observation）。
- v3 的 metadata 白名单、coverage、provenance 校验保持不变，避免破坏旧客户端与历史数据。
- `Validate()` 仍检查 ID、workout/sample/series/route 数量和已知字段；v4 的 `CompleteSampleTypes`/`CompleteRoutes` 留空。

### 4.3 导入与回执

`apps/server/internal/storage/postgres/workout_bundles.go`：

- 事务、digest、原回执重放、dataset 行锁、tombstone/身份冲突逻辑不变。
- v4 原始包没有 observation，`bundle.Observation == nil` 时：
  - Workout 身份冲突改用 `WorkoutCoreEqual` 判定（等价于 v3 的 metadata 容忍），metadata/未知字段变化不产生 `workout_identity_conflict`。
  - 样本身份冲突改用 `sameSampleCore` 判定；metadata 变化不冲突、不改写旧行。
- 回执 `sourceClassification` 从 `bundle.Sources.Workout` 计算；`sourceObservation` 对 v4 省略。
- `changed` 不再依赖 observation；新增样本/关联/series/route 或 Workout 首次导入即推进 revision。
- `workout_imports.payload` 继续保存原始字节；`workout_imports.receipt` 保存回执。

### 4.4 分析与 feature 输入

`apps/server/internal/storage/postgres/analysis_input.go`：

- `loadWorkoutInput` 取该 Workout 最新一条 `workout_imports`（按 `imported_at DESC`），不再要求 `payload ? 'observation'`。
- payload 有 observation：走现有 v3 校验。
- payload 是 protocol 4 原始包：
  - 顶层 `source` 分类必须为 `apple_watch`；
  - 用原始包内每个样本/route 的内联 source 校验已存储子事实的来源声明；
  - lineage 使用原始 payload 的 digest。

`apps/server/internal/storage/postgres/bundle_integrity.go`：

- `validateAnalysisProvenance` 增加原始 v4 解析：按 `type:uuid` / `routeID` 匹配原始包内联 source，要求 bundleID 一致且分类为 `apple_watch`/`associated_apple_watch`。
- 删除旧列式 v4 结构解析；v3 observation 历史路径保留。
- 无删除时 `validateBundleDeletionEvidence`/`checkBundleDeletions` 仍是 no-op。

`apps/server/internal/storage/postgres/feature_input.go`：

- 观察历史优先；没有 observation 时解析最新 v4 原始包的内联 source，填充 `factSources`/`allSources` 和 availability，使 workout features 的来源解析、路线/信号质量和可达性不因无 observation 退化。
- Workout metadata 继续从 `workouts.payload` 的 Event 读取，v4 的 `Event.Metadata` 保留未知键。

### 4.5 默认开启

- `httpapi.handshake`：请求头 `X-RAI-Sync-Protocol: 4` 时无条件返回 `workoutPayloadVersion=4`、`bundleProtocolVersion=4`、`bundleCompact=true`；未协商的旧客户端继续得到 v3。
- `httpapi.importWorkoutBundle`：`protocolVersion == 4` 直接走新解码器，不再依赖 `SYNC_V4_ENABLED`。
- 删除 `httpapi.Options.SyncV4` 与 `cmd/rai/main.go` 的环境变量读取。
- `contracts/CHANGELOG.md` 记录 capability 行为变化；`contracts/sync-v4.md` 全文重写。

## 5. 客户端改动

### 5.1 序列化

`ios/RAI/Sources/SyncCore/WorkoutBundleV4.swift` 重写：

- 新 envelope（第 3 节）；无 `SourceTable`、无 `deletions`、无 `observation`、无 `metadataRegistryVersion`。
- 保留 8 元组 route point。
- 仍由客户端选择规范单位（HealthKit `HKQuantity` 没有 intrinsic unit），服务端校验单位。

`ios/RAI/App/HealthKitClient.swift`：

- 新增 `Normalizer.rawMetadata(_:)`：已知 HealthKit metadata 键沿用现有类型化编码；未知键递归保留 String/Bool/整数/浮点/Date/Data/HKQuantity/数组/字典；HKQuantity 作为不透明字符串保留。
- 新增 `Normalizer.rawWorkout(_:)`：已知字段严格编码；未知 statistic 类型跳过，统计量编码失败跳过该项，不再让整条记录失败。
- 现有 v3 `metadata`/`workoutMetadata`/`integrityEvent` 保持不变。

### 5.2 构建

`ios/RAI/App/V4SyncEngine.swift` 重写：

- 每个 quantity 类型一次完整读取；每个样本输出 `uuid/startUs/endUs/value/source/metadata`；不再按来源过滤，不再记录 keys，不再维护 coverage。
- 读取失败只把该流标为 `status=failed`/`error`，不产生删除、不影响其他流。
- series 按 `type + sampleUUID` 输出；route 按 `routeID + source + points` 输出，不再计算 route coverage 或删除。
- 顶层 `source` 来自 Workout；`metadata` 用 `rawMetadata`。
- 删除 `V4Outbox`、`PreparedV4Bundle.sampleDeletions/routeDeletions/index/coverage`、`HealthPage.deleted` 分支。
- 仍保留 Workout 来源门（非 Apple Watch 不入队）；样本来源交给服务端过滤。

### 5.3 上传与状态

`ios/RAI/App/SyncCoordinator.swift`：

- `syncV4` 只保存 multipart、上传、ACK；不再 save/promote sidecar，不再 set coverage/pendingReadKeys。
- `performSync` 的 v4 分支先于 `supportsIntegrity` 判定；`bundleCompact` 是 v4 的唯一能力信号，不再要求 `bundleObservationVersion`。
- `syncWorkout` / `syncDiscovered` / `recoverPending` 移除 `pendingDelete`/`.deleted` 分支和 `deliverDeletedParentChildren`；不再调用 `deleteWorkout`。
- `isAlreadyDelivered` 不再看 `pendingDelete`/pendingReadKeys；以 ACK + discovery generation 为准。
- `health.readWorkout` 只用于拿到 anchor/可见性；Workout 不存在时报 `workout_not_visible`，不发送 DELETE。

`ios/RAI/Sources/SyncCore/HealthDiscovery.swift`：

- 不再因 `HKDeletedObject` 设置 `pendingDelete`；删除事件只影响本地发现列表，不产生服务端命令。
- `deletedWorkouts` 仅用于避免重新入队，不作为删除上传依据。

`ios/RAI/App/RAIApp.swift`：

- 移除同步流程中自动调用 `syncWorkoutCompleteness`；v4 不再做 200 天完整性证明。
- 多 Workout 手动同步改为有界并发（默认 3）：每个 Workout 独立准备/上传/ACK，失败互不影响；状态显示批量进度。
- 保留单条重试和上传文件复用；并发只发生在不同 workoutID 之间。

`ios/RAI/App/ClientConfiguration.swift`：

- `v4Enabled` 默认 true；仍允许 `syncProtocolV4 == false` 强制回退 v3。

## 6. 明确不做

- 不做删除同步、不做删除回滚、不迁移旧 v4 数据（v4 从未部署）。
- 不新增数据库迁移；未知字段只存在原始 payload 与 `health_samples.metadata`/`workouts.payload`。
- 不改变 v3 上传/ACK/删除语义；旧客户端继续工作。
- 不新增 Web/Watch/桌面客户端，不修改 `../running`。

## 7. 验收

### 7.1 Go

- `workout_bundle_v4_test.go` 重写：新 envelope、内联 source/metadata、未知顶层/嵌套字段、未知 stream、坏样本跳过、无删除、无 observation。
- `workout_bundle_v4_client_shape_test.go` 重写为 Swift 实际输出形状。
- `go test ./internal/syncing ./internal/storage/postgres ./internal/httpapi`。
- 真实隔离 PostgreSQL 集成：
  - v4 原始包导入、样本/关联/series/route 落库；
  - 同字节重试返回原回执；
  - metadata-only 变化不冲突、不改写旧样本行；
  - 未知 stream/样本/字段不阻断整包；
  - 分析输入可从原始包解析来源并通过 provenance；
  - v3 既有集成回归。
- 完成门禁：`go test -race ./...`、`go vet ./...`、依赖边界检查。

### 7.2 Swift

- 重写 `WorkoutBundleV4Tests`、`V4SyncEngineTests`：原始 envelope、未知 metadata 保留、无 sidecar/coverage/deletion。
- `swift test` / Xcode 测试；iPhoneOS Debug 无签名构建。
- 模拟器手动验证：选择多条跑步记录并发上传、进度显示、失败隔离、重复同步跳过。

### 7.3 真机（未完成前不得宣称）

- 真实 Apple Watch 跑步：Workout + 10 类样本 + series + route 一次上传落库。
- 重复同步不上传新包；部分流读取失败仍成功导入其余数据。
- 删除 HealthKit 记录后服务端保留原记录（符合“不做删除”）。

## 8. 文档同步

- `contracts/sync-v4.md`：按新线格式全文重写。
- `contracts/CHANGELOG.md`：v4 raw 与默认开启。
- `docs/WORKOUT_SYNC_V4_RESULT.md`：记录实现、证据、未验证项。
- `docs/IMPLEMENTATION.md`：追加本轮章节（保留历史原文）。
- `AGENTS.md`：同步规则改为“原始载荷落库、服务端只解释已知字段、未知字段保留不丢”；删除同步语义改为“不做删除同步”。
- 保留 `docs/WORKOUT_SYNC_V4_SIMPLE_PLAN.md` 原文，不覆盖历史计划。

## 9. 风险与取舍

| 风险 | 处理 |
| --- | --- |
| 未知字段只在原始载荷，不在 normalized 表 | 详情/分析需要时从 payload 读取；先保证不丢 |
| 客户端通用 metadata 序列化对 HKQuantity 只能保留描述字符串 | 已知键走类型化编码；未知键不丢原文（字符串） |
| 服务端跳过非 Apple Watch 样本 | 原始载荷仍保留；分析只使用可分类来源 |
| v4 无 observation，旧分析代码依赖它 | 本轮同步改 `loadWorkoutInput`/provenance/feature input |
| 并发上传与 actor 状态 | 按 workoutID 隔离，有界并发，失败独立 |
| 旧 v4 本地文件/回执 | v4 未部署；旧文件按 `bundleProtocolVersion` 丢弃重建，不删除服务端事实 |
