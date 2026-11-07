# RAI Sync Protocol 4 — 单 Workout 列式 Bundle 上传

状态：实现合同。运行时以 `GET /v2/handshake` 返回的 `workoutPayloadVersion=4`、`bundleProtocolVersion=4`、`bundleCompact=true` 和实际限额为准。协议 3 继续保留给未升级客户端。

## 1. 设计目标

每个 Workout 仍是一个不可变导入单元，但样本改为按类型列式编码：一个 `streams[]` 条目携带该类型全部样本的三元组、UUID 和来源下标，而不是逐 sample 的 JSON 对象。目标是降低 JSON 体积、减少编码/解码分配，并让一次 Workout 的读取覆盖直接成为提交依据。

```text
HKWorkout + 关联 samples + series points + route points
  → 一个 protocolVersion=4 WorkoutBundle JSON（sources/streams/series/routes）
  → 一次 POST /v2/sync/workout-bundles（multipart/form-data，字段 file）
  → 服务端转换为现有 v3 内存结构，仍由一个事务提交全部事实
  → 一个 analysis.workout job
  → 客户端 GET 回执 / 轮询分析
```

**D1（已接受）**：不发送、不生成、不校验 200 天完整性证明或 manifest。`observation.coverage` 是每个 Workout 自己的读取覆盖证明；`completeSampleTypes`/`completeRoutes` 仅由该覆盖推导，不再承担全局完整性证明职责。服务端旧完整性端点对 v4 回执明确返回 `409 completeness_not_supported_v4`。

**D2（保留）**：sample / route 的显式删除仍由客户端 sidecar diff 生成，经 `deletions[]` / `routeDeletions[]` 提交。普通快照缺席、读取失败、权限失败都不产生删除。首次同步没有 sidecar 时不生成删除；只有完整读取的类型才参与差集。

## 2. 请求

与 v3 相同的 multipart 合同：

```http
POST /v2/sync/workout-bundles
Authorization: Bearer <device-token>
Content-Type: multipart/form-data; boundary=...

--boundary
Content-Disposition: form-data; name="file"; filename="workout.json.gz"
Content-Type: application/gzip

<gzip bytes of the JSON document>
--boundary--
```

- 恰好一个 `file` part；额外 part 返回 422 `unsupported_payload`。
- 顶层 `Content-Encoding` 不接受；单个 file part 可为 `application/json`、`application/octet-stream` 或 `application/gzip`。
- wire body 上限 32 MiB + 64 KiB；decoded JSON 上限 32 MiB；gzip 文件上限 8 MiB；解压比上限 100:1 + 64 KiB 启动余量；framing 最多 64 KiB。
- 严格 JSON：未知、重复、缺失必填、显式 `null` 都拒绝（route 可选质量分量为 `null` 除外）。不做静默丢弃。
- 相同 Workout UUID + 相同文件字节重试返回原回执，不增加 revision/job。

## 3. WorkoutBundle v4

```json
{
  "protocolVersion": 4,
  "metadataRegistryVersion": 2,
  "datasetID": "<uuid>",
  "datasetEpoch": "<uuid>",
  "collectorEpoch": "<uuid>",
  "workoutID": "<HKWorkout.uuid>",
  "startUs": 0,
  "endUs": 0,
  "workout": { "activityType": 37, "durationUs": 0, "...": "..." },
  "sources": [
    {
      "bundleID": "com.apple.health",
      "name": "Apple Watch",
      "version": "10.0",
      "productType": "Watch7,1",
      "operatingSystemVersion": "10.0"
    }
  ],
  "streams": [
    {
      "type": "HKQuantityTypeIdentifierHeartRate",
      "kind": "quantity",
      "unit": "count/min",
      "samples": [[0, 1000000, 120], [1000000, 2000000, 121]],
      "uuids": ["<uuid-a>", "<uuid-b>"],
      "source": 0,
      "src": [0, 0],
      "metadata": [{}, {}]
    }
  ],
  "series": [
    { "stream": 0, "sample": 0, "points": [[0, 500000, 118], [500000, 1000000, 121]] }
  ],
  "routes": [
    {
      "routeID": "<uuid>",
      "source": 0,
      "points": [[0, 31.2, 121.4, null, null, null, null, null]]
    }
  ],
  "deletions": [
    { "type": "HKQuantityTypeIdentifierHeartRate", "uuid": "<uuid>" }
  ],
  "routeDeletions": ["<route-uuid>"],
  "observation": {
    "version": 1,
    "observedAtUs": 0,
    "metadata": {},
    "provenance": {
      "workout": {
        "bundleID": "com.apple.health",
        "name": "Apple Watch",
        "productType": "Watch7,1",
        "operatingSystemVersion": "10.0",
        "device": { "manufacturer": "Apple Inc.", "model": "Watch" }
      },
      "samples": [],
      "routes": []
    },
    "coverage": {
      "workout": { "state": "complete", "sampleCount": 1, "queryCompleted": true, "localPersisted": true },
      "HKQuantityTypeIdentifierHeartRate": { "state": "complete", "sampleCount": 2, "queryCompleted": true, "localPersisted": true },
      "series:HKQuantityTypeIdentifierHeartRate": { "state": "complete", "sampleCount": 2, "queryCompleted": true, "localPersisted": true },
      "route": { "state": "no_records_observed", "sampleCount": 0, "queryCompleted": true, "localPersisted": true }
    }
  }
}
```

### 3.1 顶层

必填且非 null：`protocolVersion`（必须为 4）、`metadataRegistryVersion`、`datasetID`、`datasetEpoch`、`collectorEpoch`、`workoutID`、`startUs`、`endUs`、`workout`、`sources`、`streams`、`observation`。`series`、`routes`、`deletions`、`routeDeletions` 可省略，但出现时不得为 `null`。

### 3.2 sources

最多 16 条。每条只允许 `bundleID`（必填、1..255）、`name`、`version`、`productType`、`operatingSystemVersion`（各 ≤255）。`device` 不在 source 表中；workout 的完整来源在 `observation.provenance.workout` 中保留。

### 3.3 streams

最多 32 条，同一 `type` 不得重复。

- `type` 必须在 `syncing.Units` 白名单且不是 `HKWorkoutTypeIdentifier`。
- `kind` 为 `quantity` 或 `category`；`unit` 必须与该类型的规范单位一致。类别流（如 SleepAnalysis）的 `unit` 是空字符串。
- `samples` 是 `[startUs, endUs, value]` 三元组数组；`kind=quantity` 时 value 为有限数字，`kind=category` 时 value 为 0..5 整数。
- `uuids` 与 `samples` 等长，UUID 规范且 bundle 内 (type, uuid) 唯一。
- `source` 是 `sources` 的下标（该流的默认来源）；`src` 可选，出现时与 `samples` 等长，逐样本覆盖来源下标。
- `metadata` 可选，出现时与 `samples` 等长；未知 metadata key 拒绝。
- 所有流样本总数不超过 50,000。

### 3.4 series

最多 50,000 个 series 条目，总点数不超过 1,000,000。每项 `{stream, sample, points}`：

- `stream` 是 `streams` 下标，`sample` 是该流内样本下标。
- `points` 是 `[startUs, endUs, value]`，必须落在父 sample 的 `[startUs,endUs]` 内，单位继承 stream 的 `unit`。

### 3.5 routes

最多 16 条，总点数不超过 1,000,000。每项 `{routeID, source, points}`：

- `routeID` 唯一且为 UUID；`source` 是 `sources` 下标。
- 每个点固定为 8 元组 `[recordedAtUs, latitude, longitude, altitude, horizontalAccuracy, verticalAccuracy, speed, course]`；后 6 项可为 `null`。
- `sequence` 由数组下标生成（从 0 开始），不发送也不接受客户端提供的 sequence。

### 3.6 deletions / routeDeletions

- `deletions` 最多 10,000 项 `{type, uuid}`，type 必须在白名单且不是 workout；uuid 规范。
- `routeDeletions` 最多 256 个 route UUID。
- 删除项不得与同一 bundle 的 samples/routes 冲突。
- 删除是显式 sidecar diff 的结果，不是快照缺席推断；读取失败或权限失败绝不生成删除。

### 3.7 observation

必填。`version=1`，`observedAtUs` 在合法时间范围；`metadata` 最多 64 项；`coverage` 最多 128 项。

- `provenance.workout` 必须是完整 `HealthKitSource`，`bundleID` 与 `sources`/Workout 来源一致，分类必须为 `apple_watch`。
- `provenance.samples` 与 `provenance.routes` 必须是空数组（v4 的逐样本来源由 streams/routes 的下标表达）。
- `coverage` 的 key 只允许 `workout`、`route`、`Units` 中的类型、`series:<type>`；不允许 `route:<id>`。
- 每个 coverage 必须包含 `state`、`sampleCount`、`queryCompleted`、`localPersisted`；可选 `expectedCount`、`errorCode`、`diagnostics`。
- state 枚举：`not_queried`、`observed`、`no_records_observed`、`partial`、`complete`、`failed`、`unsupported`、`unknown`。
- `complete` 要求 `queryCompleted=true`、`localPersisted=true`、`sampleCount>0`，且与该 Workout 实际计数精确一致；`no_records_observed` 要求 `queryCompleted=true`、`sampleCount=0`。非 complete 的 `sampleCount` 不得超过实际计数。

## 4. 与服务端 v3 内部结构的关系

v4 解码器在严格校验后转换为现有 `syncing.WorkoutBundle`：

- `Workout` Event 的 `startUs/endUs` 取顶层；`Source.BundleID` 取 `observation.provenance.workout.bundleID`。
- 每个 stream 的每个样本转成一个 `Event{op:"upsert", kind:"sample", type, uuid, startUs, endUs, quantity/category, source:{bundleID}, metadata}`。
- `CompleteSampleTypes` 仅由 `coverage[type].state=="complete"` 且该流有样本生成；`CompleteRoutes` 仅由 `coverage.route.state=="complete"` 且有 route 生成。它们只描述读取覆盖，不授权删除。
- 转换后的对象仍走 `WorkoutBundle.Validate()` 和现有 `ImportWorkoutBundle` 单事务写路径；数据库表、receipt、幂等 digest、analysis job 与 v3 相同。
- 原始 v4 字节写入 `workout_imports.payload`，analysis 读取 v4 `sources/streams/routes` 还原逐样本来源；v4 不进入旧 200 天完整性扫描。

## 5. 客户端状态与重试

客户端使用独立 v4 outbox：

- `state.json`：每个 Workout 的 phase、payload 文件、bundleSHA256、attempts、nextAttemptAtUs、receipt、lastError。
- `payloads/<workoutID>.json.gz`：已生成但未确认的不可变 bundle。
- `v4-index-<uuid>.json`：已提交 bundle 的 sample/route identity sidecar，仅用于下一次 D2 差集。
- `v4-pending-<bundleSHA256>.json`：上传前暂存的同一 index；仅当回执持久化后才提升为 `v4-index-<uuid>.json`，避免 ACK 后崩溃丢失 sidecar 或未提交 bundle 推进删除权限。

规则：

- 当前实现对 10 个 quantity 类型并发读取（结果按 type 排序以保证字节稳定）；某个流失败只标该流 `partial`/`failed`，不丢弃其它流，不伪造删除。
- 超过 50,000 samples 整 Workout 失败，不截断。
- 同一 payload 文件字节重试，不重新编码；成功后仅当 receipt 已提交才推进 sidecar。
- ACK 的语义要求是 `workoutID`、`bundleSHA256`、`ingestStatus=committed`、`commitRevision>0`、`analysisStatus∈{queued,not_required}`；当前客户端复用共享的 v3 receipt 校验，因此也会校验服务端返回的 counts。
- 首次无 sidecar 不生成 sample 删除；读取失败的类型保留旧 identity；权限失败绝不生成删除。

## 6. 灰度

服务端：`SYNC_V4_ENABLED=1` 才在 handshake 中接受 `X-RAI-Sync-Protocol: 4` 并返回 v4 capability；默认关闭时完全保持 v3 行为，v4 上传返回 422 `unsupported_protocol`。
客户端：`ClientConfiguration.syncProtocolV4` 或环境变量 `RAI_SYNC_V4=1` 开启；只有 handshake 同时返回 `bundleProtocolVersion=4`、`bundleCompact=true`、`bundleObservationVersion=1` 时才走 v4，否则继续 v3。
