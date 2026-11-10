# RAI 同步协议 v5：紧凑 Workout 原始载荷

状态：已撤回（2026-11-10）。Workout 同步只保留 protocol 6（`contracts/sync-v6.md`）；
本文描述的 v5 紧凑编码从未有客户端生产者，相关编解码与兼容入口已删除。本文仅保留
历史线格式与语义记录，服务端不再接受该版本，iOS 也不再构建。

## 1. 与 v4 的差异

v4 在每个样本对象里重复 `uuid`、`startUs`、`endUs`、`source` 和 `metadata` 键名，
并把完整来源描述符逐样本携带。v5 只压缩这些重复结构：

- 来源描述符去重到顶层 `sourceTable`，样本和 route 只带整数索引；
- 样本 metadata 去重到顶层 `metadataTable`，样本只带整数索引；
- 每个 stream 声明一个默认来源索引，样本可覆盖；
- 所有样本、series 点、route 点的起止时间改为相对顶层 `startUs` 的微秒增量；
- 样本从对象改为定长/变长数组，去掉逐点重复的键名。

样本 UUID 仍逐点携带。UUID 是 `health_samples (dataset_id, sample_type,
sample_uuid)` 的事实身份，也是 series 的父引用，不是可去重的重复值。时间也是逐点
不同的数据，不能只保留一份；增量编码只是去掉了重复的高位数字。

## 2. 传输

v5 与 v4 使用同一传输：

- `POST /v2/sync/workout-bundles/{workoutID}` 或
  `POST /v2/sync/workout-ingests/{workoutID}`；
- multipart 恰好一个 `file` part，`application/gzip` 或 `application/json`；
- wire body 上限 32 MiB + 64 KiB；decoded JSON 上限 32 MiB；gzip 文件上限 8 MiB；
  解压比上限 100:1 + 64 KiB 启动余量；
- UTF-8、重复对象键、深度 > 128 拒绝；未知顶层字段忽略并保留；
- 相同 Workout UUID + 相同文件字节重试返回原回执，不增加 revision/job；
- v5 不做删除同步；既有删除能力仍只属于 v3 与管理路径。

客户端发送 `X-RAI-Sync-Protocol: 5` 请求 v5。服务端返回
`workoutPayloadVersion=5`、`bundleProtocolVersion=5`、`bundleCompact=true`。
服务端必须同时接受 v4；客户端在服务端未协商到 5 时回退 v4，不得把 v5 字节发给只
支持 v4 的服务端。

## 3. 线格式

```json
{
  "protocolVersion": 5,
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
    "totalDistance": { "value": 8000.0, "unit": "m" }
  },
  "sourceTable": [
    { "bundleID": "com.apple.health", "name": "Apple Watch", "productType": "Watch7,1" }
  ],
  "metadataTable": [
    { "HKMetadataKeyHeartRateMotionContext": 1 }
  ],
  "streams": [
    {
      "type": "HKQuantityTypeIdentifierHeartRate",
      "unit": "count/min",
      "status": "complete",
      "error": null,
      "source": 0,
      "samples": [
        ["<uuid>", 0, 5000000, 120],
        ["<uuid>", 60000000, 65000000, 121, 0],
        ["<uuid>", 120000000, 125000000, 122, null, 0]
      ]
    }
  ],
  "series": [
    {
      "type": "HKQuantityTypeIdentifierHeartRate",
      "sampleUUID": "<uuid>",
      "unit": "count/min",
      "points": [[0, 5000000, 121]]
    }
  ],
  "routes": [
    {
      "routeID": "<uuid>",
      "source": 0,
      "points": [[0, 31.2, 121.4, null, null, null, null, null]]
    }
  ]
}
```

### 3.1 顶层

必需且非 null：`protocolVersion`（必须为 5）、`datasetID`、`datasetEpoch`、
`collectorEpoch`、`workoutID`、`startUs`、`endUs`、`source`、`workout`。
四个身份字段必须是规范 UUID；`startUs ≤ endUs` 且都在合法微秒范围内。

可选：`metadata`、`sourceTable`、`metadataTable`、`streams`、`series`、`routes`。
出现时必须是合法类型；显式 `null` 视为缺席。其他顶层字段忽略并保留在原始载荷中。

`source`、`metadata`、`workout` 的语义与 v4 完全相同。

### 3.2 sourceTable

`HealthKitSource` 描述符数组，索引从 0 开始。字段与 v4 的 `source` 相同：
`bundleID`（必填、1..255）、`name`、`version`、`productType`、
`operatingSystemVersion`（各 ≤255）、`device`
（`manufacturer`/`model`/`hardwareVersion`/`firmwareVersion`/`softwareVersion`）。

- 最多 64 项；超过返回 413 `bundle_too_large`。
- 单一项非法时保留其索引位置，但引用该索引的样本/route 被跳过；不影响其他项。
- 顶层 `source` 必须是 `apple_watch`，否则返回 422 `source_policy_excluded`。
- 样本/route 引用的来源必须分类为 `apple_watch` 或 `associated_apple_watch`，
  否则该事实被跳过。

### 3.3 metadataTable

原始 metadata 对象数组，索引从 0 开始。键和值不设白名单；已知的 `HKTimeZone`
等键与未知键都原样投影到事实 metadata，并随规范化事实保存。保留键
`RAI:vo2maxFactV1` 是服务端 provenance，客户端提交时从投影中移除，原始字节仍保留。

- 最多 50,000 项；超过返回 413 `bundle_too_large`。
- 单一项非法或索引越界时，仅该样本没有 metadata；样本本身仍可导入。
- 同一对象可被多个样本引用；服务端按每个样本复制投影，不共享可变 map。

### 3.4 workout

与 v4 相同。已知字段：`activityType`、`durationUs`、`totalDistance`、
`totalEnergyBurned`、`statistics`、`events`、`activities`。未知字段保留在原始载荷。
`activityType`/`durationUs` 缺失或非法返回 422 `unsupported_payload`。

### 3.5 streams

每个条目一种样本类型：

- `type`（必填）：必须在 `syncing.Units` 白名单且不是
  `HKWorkoutTypeIdentifier`；未知类型跳过该流。
- `unit`（可选）：声明时必须与该类型的规范单位一致，否则跳过整个流；缺省使用
  服务端规范单位。类别流的规范单位是空字符串。
- `status` / `error`（可选）：仅诊断信息，不参与导入判定。
- `source`（可选）：该流样本的默认 `sourceTable` 索引。流内存在没有自己来源索引的
  样本时必须提供；否则这些样本被跳过。
- `samples`（可选数组）：每个样本是数组，长度为 4、5 或 6：
  1. `uuid`（必填，规范 UUID；同一 `type` 内重复时保留第一个）；
  2. `startDeltaUs`（必填，有符号整数，相对顶层 `startUs`）；
  3. `endDeltaUs`（必填，有符号整数，相对顶层 `startUs`）；
  4. `value`（必填，有限数）；规范单位非空时按 quantity 导入，单位为空时要求
     0..5 整数并按 category 导入；
  5. `metadataIndex`（可选，整数或 null）：`metadataTable` 索引；缺省或 null
     表示无 metadata；
  6. `sourceIndex`（可选，整数或 null）：覆盖流默认来源；缺省或 null 使用流
     `source`。

长度不是 4..6、数字不是整数、UUID 非法、时间越界或 `startUs > endUs` 的样本
被跳过。绝对时间为 `startUs + delta`，必须落在合法微秒范围内。

同一 `type` 的重复流只取第一个。所有流的样本总数（含被跳过的原始条目）上限
50,000；超限返回 413 `bundle_too_large`，不静默截断。

### 3.6 series

多值 quantity 样本的子点。每个条目：

- `type`（必填）：必须在白名单且规范单位非空；
- `sampleUUID`（必填）：父样本必须已从同一 bundle 的 `streams` 导入；
- `unit`（可选）：声明时必须与规范单位一致；
- `points`（必填非空数组）：每个点是 `[startDeltaUs, endDeltaUs, value]`，
  时间相对顶层 `startUs`；绝对时间必须落在父样本 `startUs..endUs` 内。

同一 `(type,sampleUUID)` 只取第一个。条目总数上限 50,000，总点数上限 1,000,000；
超限返回 413 `bundle_too_large`。

### 3.7 routes

- `routeID`（必填，规范 UUID 且 bundle 内唯一）；
- `source`（必填）：`sourceTable` 索引；
- `points`（必填非空数组）：与 v4 相同的 3..8 元素数组，但第一个元素
  `recordedAtDeltaUs` 是相对顶层 `startUs` 的有符号增量；其余元素为纬度、经度及
  可空的高度、水平精度、垂直精度、速度、方向。

route 总数上限 16，总点数上限 1,000,000；超限返回 413 `bundle_too_large`。
来源索引非法或来源不合格时跳过该 route；单个点非法时跳过该点。

## 4. 投影

每个可用样本仍转成：

```text
Event{op:"upsert", kind:"sample", type, uuid, startUs, endUs,
      quantity/category, source:{bundleID}, metadata}
```

`sourceTable`/`metadataTable` 只是编码层去重，不产生独立事实，也不改变
`BundleSourceTable`：服务端仍按 `(sampleType, sampleUUID)` 为每个样本记录完整
`HealthKitSource`，分析路径读取同一来源表。series 通过 `sampleUUID` 绑定父样本，
route 通过 `routeID` 保持身份。

## 5. 按 Workout UUID 一次接收

v5 与 v4 共用 `POST /v2/sync/workout-ingests/{workoutID}`：

- 路径 `workoutID` 必须等于载荷顶层 `workoutID`，否则 422
  `workout_ingest_mismatch`；
- 同一 UUID 的重复 POST 返回首次接收回执，即使字节不同；原始载荷与回执均不替换；
- 根删除屏障优先：已删除 UUID 返回 409 `workout_deleted`；
- 非法载荷 422/413；积压超限 429；模式关闭/暂停返回对应 409/503；
- 接收事务只保存精确 `raw_json` 字节和回执；worker 按顶层 `protocolVersion`
  选择 v4 或 v5 解码器，再提交事实、删除屏障、提交日志、分析待办和最终回执。

## 6. 限额与宽容边界

- 顶层身份、Workout 窗口、`workout` 核心和顶层来源策略是硬错误；
- 未知流、未知样本、非法样本/点/route 单项被跳过，原始字节仍保留；
- 资源上限超限返回 413，不静默截断；
- v5 的 `sourceTable`/`metadataTable` 只影响引用它们的可选事实，不改变硬错误边界。

## 7. 兼容性

- 服务端同时实现 v3、v4、v5 解码；`protocolVersion` 不在 {3,4,5} 返回 422
  `unsupported_protocol`。
- v4 客户端继续得到 `bundleProtocolVersion=4`；v5 客户端发送
  `X-RAI-Sync-Protocol: 5` 才得到 5。
- 客户端把已封存的 v4 包按原字节上传，不因升级重新编码；新包才使用 v5。
- UUID ingest 的接收回执、状态查询、分析轮询和管理删除路径与协议版本无关。
