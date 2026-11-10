# RAI Sync Protocol 4 — 原始 Workout 上传

状态：已撤回（2026-11-10）。Workout 同步只保留 protocol 6（`contracts/sync-v6.md`）；本文描述的 v4 原始载荷、`workout-bundles` 入口与 v4 解码器均已删除。本文仅保留历史线格式与语义记录，服务端不再接受该版本，iOS 也不再构建。

## 1. 设计目标

每个 Workout 是一个不可变导入单元。客户端不构造 source 表、coverage 状态机、metadata registry 声明、删除差集或 200 天完整性证明；它只把一次 HealthKit 读取得到的 Workout、样本、series 与 route 按双方共享的规范单位原样序列化。

```text
HKWorkout + 关联 samples + series points + route points
  → 一个 protocolVersion=4 原始 JSON（source/workout/metadata/streams/series/routes）
  → 一次 POST /v2/sync/workout-bundles（multipart/form-data，字段 file）
  → 服务端解释已知字段并投影为规范化事实；未知字段只留在原始载荷
  → 仍由一个事务提交全部事实、回执、change event、dirty scope 与 analysis job
  → 客户端 GET 回执 / 轮询分析
```

不变量：

- `workout_imports.payload` 是客户端提交的精确 JSON 字节，服务端不改写、不注入字段；它就是未知字段的权威副本。
- 服务端能识别的字段投影到 `workouts` / `health_samples` / `workout_samples` / `workout_series` / `workout_routes`；无法识别的流、样本或点只留在原始载荷中，不阻断整包。
- 单位、时间、UUID、来源分类由服务端解释；客户端只负责按规范单位序列化 HealthKit 值。
- 不做删除同步：v4 客户端不上传删除列表，也不根据 HealthKit 删除事件调用 DELETE。服务端保留 v3/管理路径的既有删除能力，但 v4 上传不产生删除。
- 无 coverage、observation、metadata registry 版本或完整性 manifest；同一 Workout 的读取失败只体现在被省略/标记失败的流中。

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
- JSON 必须是 UTF-8；对象内重复键、非法嵌套（深度 > 128）拒绝。未知字段合法，保留在原始载荷中。
- 相同 Workout UUID + 相同文件字节重试返回原回执，不增加 revision/job。

## 3. 线格式

```json
{
  "protocolVersion": 4,
  "datasetID": "<uuid>",
  "datasetEpoch": "<uuid>",
  "collectorEpoch": "<uuid>",
  "workoutID": "<HKWorkout.uuid>",
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
          "uuid": "<uuid>",
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
      "sampleUUID": "<uuid>",
      "unit": "count/min",
      "points": [[1790000000000000, 1790000000500000, 121]]
    }
  ],
  "routes": [
    {
      "routeID": "<uuid>",
      "source": { "bundleID": "com.apple.health", "productType": "Watch7,1" },
      "points": [[1790000000000000, 31.2, 121.4, null, null, null, null, null]]
    }
  ]
}
```

### 3.1 顶层

必需且非 null：`protocolVersion`（必须为 4）、`datasetID`、`datasetEpoch`、`collectorEpoch`、`workoutID`、`startUs`、`endUs`、`source`、`workout`。四个身份字段必须是规范 UUID；`startUs ≤ endUs` 且都在合法微秒范围内。

可选：`metadata`、`streams`、`series`、`routes`。出现时必须是合法类型；显式 `null` 视为缺席。其他任何顶层字段都忽略并保留在原始载荷中。

### 3.2 source

Workout 来源，一个 `HealthKitSource` 描述符。允许字段：`bundleID`（必填、1..255）、`name`、`version`、`productType`、`operatingSystemVersion`（各 ≤255）、`device`（`manufacturer`/`model`/`hardwareVersion`/`firmwareVersion`/`softwareVersion`）。

服务端重新分类该来源：`bundleID=com.apple.health` 且带 Apple Watch `productType` 时为 `apple_watch`；其他可识别为关联 Watch 的描述符为 `associated_apple_watch`。顶层来源分类必须为 `apple_watch`，否则返回 422 `source_policy_excluded`。

### 3.3 metadata

Workout 级原始 metadata 对象。键和值不设白名单；已知的 `HKTimeZone` 等键与未知键都原样投影到 Workout Event 的 metadata，并随规范化 Workout 保存。保留键 `RAI:vo2maxFactV1` 是服务端 provenance，客户端提交时从投影中移除，但原始字节仍保留。

### 3.4 workout

Workout 核心对象。已知字段：

- `activityType`（必填，正整数）；
- `durationUs`（必填，非负且不超过 `startUs..endUs` 窗口）；
- `totalDistance`（可选 `{value,unit:"m"}`）、`totalEnergyBurned`（可选 `{value,unit:"kcal"}`）；
- `statistics`、`events`、`activities`：已知结构逐项校验，未知 statistic 类型或单项非法时跳过该项，不影响 Workout 导入。

未知的 Workout 字段忽略并保留在原始载荷。`activityType`/`durationUs` 缺失或非法返回 422 `unsupported_payload`（`field=workout`，`reason=invalid_workout`）。

### 3.5 streams

每个条目一种样本类型：

- `type`（必填）：必须在 `syncing.Units` 白名单且不是 `HKWorkoutTypeIdentifier`；未知类型跳过该流。
- `unit`（可选）：声明时必须与该类型的规范单位一致，否则跳过整个流；缺省使用服务端规范单位。类别流（如 `HKCategoryTypeIdentifierSleepAnalysis`）的规范单位是空字符串。
- `status` / `error`（可选）：仅诊断信息，不参与导入判定。
- `samples`（可选数组）：每个样本是独立对象：
  - `uuid`（必填，规范 UUID；同一 `type` 内重复时保留第一个）；
  - `startUs` / `endUs`（必填，`startUs ≤ endUs`）；
  - `value`（必填，有限数）；规范单位非空时按 quantity 导入，单位为空时要求 0..5 整数并按 category 导入；
  - `source`（必填）：该样本自己的 `HealthKitSource`；无法解析或分类不是 `apple_watch`/`associated_apple_watch` 时跳过该样本；
  - `metadata`（可选）：对象原样投影到样本 metadata。

同一 `type` 的重复流只取第一个。所有流的样本总数（含被跳过的原始条目）上限 50,000；超限返回 413 `bundle_too_large`，不静默截断。

### 3.6 series

多值 quantity 样本的子点。每个条目：

- `type`（必填）：必须是已注册的 quantity 类型（规范单位非空）；
- `sampleUUID`（必填）：父样本必须已从同一 bundle 的 `streams` 导入；
- `unit`（可选）：声明时必须与规范单位一致；
- `points`（必填数组）：`[startUs, endUs, value]` 三元组，必须落在父样本 `[startUs,endUs]` 内；非法点跳过。

同一 `(type,sampleUUID)` 只取第一个。条目总数上限 50,000，总点数上限 1,000,000；超限返回 413 `bundle_too_large`。

### 3.7 routes

- `routeID`（必填，规范 UUID 且 bundle 内唯一）；
- `source`（必填）：该 route 自己的 `HealthKitSource`，分类必须为 `apple_watch`/`associated_apple_watch`；
- `points`（必填数组）：客户端固定发送 8 元组 `[recordedAtUs, latitude, longitude, altitude, horizontalAccuracy, verticalAccuracy, speed, course]`，后 5 项可为 `null`。服务端也容忍 3..8 个元素，缺失的后缀按 `null` 处理；非法坐标/数值的点跳过，全部点非法的 route 跳过。

route 数上限 16，总点数上限 1,000,000；超限返回 413 `bundle_too_large`。`sequence` 由服务端按数组下标生成，不接受客户端提供。

## 4. 服务端投影

v4 解码器把原始载荷投影为现有 `syncing.WorkoutBundle`：

- Workout Event 的 `startUs/endUs` 取顶层；`Source.BundleID` 取顶层 `source.bundleID`；Workout metadata 取顶层 `metadata`（移除保留键）。
- 每个 stream 的每个可用样本转成 `Event{op:"upsert", kind:"sample", type, uuid, startUs, endUs, quantity/category, source:{bundleID}, metadata}`。
- 每个可用 series 点转成 `WorkoutBundleSeries`；每个可用 route 点转成 `WorkoutBundlePoint`。
- 逐 fact 的完整来源描述存入 `BundleSourceTable{Workout,Samples,Routes}`，供回执分类、分析和 feature 输入复用；它不进入 ACK。
- `Observation`、`CompleteSampleTypes`、`CompleteRoutes`、`SampleDeletions`、`RouteDeletions` 对 v4 恒为空；v4 不生成 observation，也不授权删除。
- 转换后的对象仍走 `WorkoutBundle.Validate()` 和现有 `ImportWorkoutBundle` 单事务写路径；数据库表、receipt、幂等 digest、analysis job 与 v3 相同。
- 原始 v4 字节写入 `workout_imports.payload`。分析/feature 输入在需要逐 fact 来源时调用 `WorkoutBundleV4SourceTable(payload)` 从原始字节重建；v4 不进入旧 200 天完整性扫描。
- v3 的 metadata 白名单、coverage、provenance 校验与导入语义保持不变；v4 只是不产生这些 sidecar。

宽容边界的作用域仅限“可选事实”：未知流/样本/点/route 被跳过；包身份（四个 UUID、Workout 窗口、`workout` 核心）和顶层 `source` 策略仍是硬错误。资源上限超限返回 413，不静默截断。

## 5. 客户端状态与重试

- 每个 Workout 的 `state.json` 继续记录 phase、payload 文件路径、bundleSHA256、attempts、nextAttemptAtUs、receipt、lastError。
- `payloads/<workoutID>.json.gz` 是已生成但未确认的不可变 bundle；重试复用同一文件字节，不重新读取 HealthKit、不重新编码。
- 没有 sample/route identity sidecar、没有 `v4-pending-*` 暂存、没有 coverage 或 pending-delete 状态。
- 读取失败只把该流标为 `status=failed`/`error`，其余流仍上传；本次读取中被跳过的样本不会出现在 bundle 中。
- 删除 HealthKit 记录不产生任何上传或 DELETE；服务端保留已导入事实。
- ACK 语义与 v3 bundle 回执一致（`workoutID`、`bundleSHA256`、`ingestStatus=committed`、`commitRevision`、analysis 状态）。ACK 成功前保留原 payload；成功后仅清理本地待上传状态。

## 6. 灰度

服务端：请求头 `X-RAI-Sync-Protocol: 4` 时 handshake 无条件返回 `workoutPayloadVersion=4`、`bundleProtocolVersion=4`、`bundleCompact=true`；`SYNC_V4_ENABLED` 已移除。未发送该头的 v3 客户端继续得到原 v3 capability 集。为兼容旧客户端，capability 中仍可能出现 `bundleObservationVersion=1` 与 `explicitChildDeletions=true`，v4 客户端不使用它们。

客户端：`ClientConfiguration.v4Enabled` 默认为 true；只有 handshake 返回 `bundleProtocolVersion=4` 且 `bundleCompact=true` 时才走 v4。显式 `syncProtocolV4=false` 强制回退 v3。

v4 在此次重定义前从未部署，因此不保留旧列式 v4 兼容路径，也不迁移旧 v4 本地文件；旧本地 v4 状态按新协议丢弃重建，服务端事实不受影响。

## 7. 按 Workout UUID 一次接收

服务端提供一条可独立启停的接收入口，使同一个 `HKWorkout.uuid` 在每个 dataset 中只被接收一次；首次成功接收的原始 v4 载荷是唯一权威副本。该入口不替代 v3，也不改变 `workout-bundles` 的 v3 语义。

### 7.1 端点

```http
POST /v2/sync/workout-ingests/{workoutID}
Authorization: Bearer <device-token>
Content-Type: multipart/form-data; boundary=...

<与第 2 节完全相同的单 file part 原始 v4 JSON>
```

- 路径 `workoutID` 必须是规范 UUID，且必须等于载荷顶层 `workoutID`，否则 422 `workout_ingest_mismatch`。
- 校验 Bearer、dataset、dataset/collector epoch、UTF-8、重复键、嵌套深度、`protocolVersion=4` 与 wire 大小；不解析样本/route 语义。
- 首次接收成功返回 200 `{workoutID, ingestStatus:"accepted", acceptedAt}`，且响应晚于数据库提交。该回执冻结首次记录值；之后转为 `committed`/`rejected` 仍重放同一 `acceptedAt`。
- 同一 UUID 的重复 POST 返回首次接收回执，即使字节不同；原始载荷与回执均不替换。
- 已存在但无接收记录的 Workout 返回 `{workoutID, ingestStatus:"already_imported"}`。
- 根删除屏障优先：已删除 UUID 返回 409 `workout_deleted`。
- 非法载荷 422/413；积压超限 429 并带 `Retry-After`；模式关闭时 409 `workout_uuid_ingest_disabled`，暂停时 503 `workout_uuid_ingest_paused`。

```http
GET /v2/sync/workout-ingests/{workoutID}
Authorization: Bearer <device-token>
```

单次数据库快照返回状态，优先级固定为：

1. `deleted`（存在根 tombstone）
2. `accepted` / `committed` / `rejected`（接收记录）
3. `already_imported`（仅存在 Workout 事实）
4. 404 `workout_ingest_not_found`

`committed` 附带 `commitRevision`、`analysisStatus` 与 `analysisJobIDs`；`rejected` 附带 `errorCode`。查询只作用于调用者自己的 dataset；同一 UUID 在其他 dataset 的状态不可见。

### 7.2 服务端状态机

- 接收行按 `(dataset_id, workout_uuid)` 唯一，保存精确 `raw_json` 字节、载荷大小、接收设备、epochs、状态、`accepted_at`、`commit_receipt`、`finished_at`、`error_code`、`attempts`、`next_attempt_at`、`lease_until`。
- 每个 dataset 的待处理积压上限为 200 行 / 512 MiB；并发进程通过主键 `ON CONFLICT DO NOTHING` 与读回解决。
- 单个 Worker 解析循环：90 秒租约、60 秒处理预算；解码 v4 原始字节后走与同步导入相同的事实写核心。事实、历史、分析待办与 `committed` 状态在同一事务提交，并以认领令牌（`attempts`）和有效租约守护；任一守护失败则整个事务回滚。
- 永久错误（解码失败、身份不匹配、删除、fencing、生命周期）写 `rejected` 并停止自动重试；暂态错误指数退避，最多 3 次；最后一次认领在租约过期后终结为 `rejected`。
- 提交成功或解析失败后仍保留原始载荷；显式 Workout 删除会清空原始载荷但保留接收身份与既有回执，使待处理行立即终止为 `rejected`。
- `workout-bundles` 的 v4 POST 在 `uuid` 模式返回 409 `workout_uuid_ingest_required`，在 `uuid_paused` 模式返回 503 `workout_uuid_ingest_paused`；GET 回执、v3 与管理删除路径继续可用。

### 7.3 模式与握手

服务端环境变量 `RAI_WORKOUT_UUID_INGEST_MODE` 取值 `legacy`（默认）、`uuid`、`uuid_paused`。握手恒返回 `workoutUUIDIngestVersion=1` 与 `workoutUUIDIngestAccepting`；客户端只有在该值为 `true` 时才使用本条入口。

客户端在详细 HealthKit 发现前先按 UUID 判断：本地 Outbox 已有已接收回执则不再上传；否则查询服务端状态后再决定上传或轮询。接收回执与清理授权原子持久化；新路径不引入 uploadID/ingestID/载荷哈希身份。
