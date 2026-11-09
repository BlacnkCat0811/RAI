# RAI 同步协议 v6：RQrun 式原始序列一次打包

状态：实现合同。v6 只借 RQrun“单遍读取、按类型打包原始序列、一次上传”的原则，
不复制其字段名或身份规则。客户端提交 HealthKit 观察到的原始 Workout、标量序列、
series 分组和 route；服务端只解释已知字段，未知字段与坏点保留或跳过，不阻断整包。
运行时以 `GET /v2/handshake` 返回的 `workoutPayloadVersion=6`、
`bundleProtocolVersion=6`、`bundleCompact=true` 和实际限额为准。协议 3、4、5 继续
保留给未升级客户端。

## 1. 设计目标与与 v4/v5 的差异

RQrun 的打包过程是：读一次 Workout，按 series 循环，每个点只写 `heart_rate`、
`start_time`、`end_time`，追加到数组，顶层写一次设备/来源，最后序列化。它是单遍、
无状态、无映射、无基线的。v6 采用同样的数据流，但保留 RAI 自己的合同边界：

- **逐样本 UUID 不再上传**：服务端用 Workout UUID + 类型 + 事实种类 + 线上数组下标
  生成确定性 UUIDv5。相同字节重放得到相同事实身份。
- **逐样本 metadata / source 不再上传**：一个 Workout 只有一个顶层 `source`，所有
  投影事实继承它。混源（Watch + iPhone）不再逐样本区分来源。
- **不做 series 父 UUID 关联**：series 分组内嵌在 stream 中，父样本的 start/end/value
  就是组头；组头本身不再出现在 `samples` 数组里。
- **不做删除同步**：v6 客户端不上传删除，服务端不根据快照缺席推断删除；既有删除
  能力仍只属于 v3 与管理路径。
- **不携带 observation / coverage / completeness / 锚点 / CaptureManifest**：v6 是
  原始序列载荷，不是采集证明。

与 v4 相比，v6 去掉了 `uuid`、`source`、`metadata`、`sourceTable`、`metadataTable`、
顶层 `series` 和 `routeID`。与 v5 相比，v6 去掉了逐样本 UUID、来源/元数据索引和
series 父引用；时间增量、三元素点数组和顶层来源仍然保留。

## 2. 传输与协商

v6 与 v4/v5 使用同一传输：

- `POST /v2/sync/workout-ingests/{workoutID}`（UUID 一次接收通道），或
  `POST /v2/sync/workout-bundles`；
- multipart 恰好一个 `file` part，`application/json` 或带 gzip 协商的
  `application/gzip`；
- wire body 上限 32 MiB + 64 KiB；decoded JSON 上限 32 MiB；gzip 文件上限 8 MiB；
  解压比上限 100:1 + 64 KiB 启动余量；
- UTF-8、重复对象键、深度 > 128 拒绝；未知顶层字段忽略并保留在原始载荷中；
- 相同 Workout UUID + 相同文件字节重试返回原回执，不增加 revision/job。

客户端发送 `X-RAI-Sync-Protocol: 6` 请求 v6。服务端只有在
`RAI_WORKOUT_PAYLOAD_V6=1` 时才返回
`workoutPayloadVersion=6`、`bundleProtocolVersion=6`、`bundleCompact=true`；
否则同一个请求返回 v4。客户端看到 4 时必须回退 v4 构建器，不得把 v6 字节发给
只支持 v4 的服务端。服务端默认关闭 v6。

v6 字节仍进入“原始载荷落库、服务端只解释已知字段、未知字段保留不丢”的 UUID
接收通道：首次成功接收的字节是唯一权威副本，后续重复 UUID 即使字节不同也只重放
首次 `acceptedAt`。

## 3. 线格式

### 3.1 顶层包

```json
{
  "protocolVersion": 6,
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
  "metadata": { "HKTimeZone": "Asia/Shanghai" },
  "workout": {
    "activityType": 37,
    "durationUs": 1800000000,
    "totalDistance": { "value": 8000.0, "unit": "m" }
  },
  "streams": [ ... ],
  "routes": [ ... ]
}
```

必填：`protocolVersion`、`datasetID`、`datasetEpoch`、`collectorEpoch`、
`workoutID`、`startUs`、`endUs`、`source`、`workout`。`metadata`、`streams`、
`routes` 可省略；省略等同于空。所有时间是 UTC 微秒。

`source` 必须是合法的 HealthKit 来源描述符，且按冻结的
`rai.workout.source_policy.apple_watch_only/v1` 分类为 `apple_watch`。被排除或无法
分类的 Workout 整包拒绝（`source_policy_excluded` / `source_policy_pending`）。

`workout` 是 v4 相同的 Workout 核心：至少包含合法的 `activityType` 与
`durationUs`；未知字段保留在原始载荷中。

### 3.2 标量 stream

```json
{
  "type": "HKQuantityTypeIdentifierHeartRate",
  "unit": "count/min",
  "status": "complete",
  "error": null,
  "samples": [
    [0, 1000000, 120],
    [1000000, 2000000, 121]
  ]
}
```

- `type` 必填，必须是服务端 `units` 表中的已知类型，且不能是
  `HKWorkoutTypeIdentifier`；同一类型只投影第一个 stream。
- `unit` 可选；提供时必须与 `type` 的 canonical unit 完全一致。
- `samples` 是 `[startDeltaUs, endDeltaUs, value]` 三元素数组，相对顶层 `startUs`。
  `start <= end`，`value` 必须是有限数。
- `status` / `error` 是客户端诊断字段，服务端按原样保留在原始载荷中；v6 不据此
  伪造完整性或阻止投影。
- `series` 与 `samples` 可在同一 stream 中同时出现；标量类型通常只填 `samples`。

### 3.3 series 分组

```json
{
  "type": "HKQuantityTypeIdentifierRunningPower",
  "unit": "W",
  "status": "complete",
  "error": null,
  "samples": [],
  "series": [
    {
      "startDeltaUs": 2000000,
      "endDeltaUs": 3000000,
      "value": 250,
      "points": [
        [2100000, 2500000, 260],
        [2600000, 2900000, 240]
      ]
    }
  ]
}
```

- 每个组对应 HealthKit 的一个多值 quantity 父样本。组头的 start/end/value 就是
  父样本自己的起止与聚合值；服务端不自行编造组头。
- `points` 是组内 series 观察值，三元素数组格式与标量样本相同。每个点必须落在
  父组 `[startDeltaUs, endDeltaUs]` 范围内。
- 组头本身投影为一条 `health_samples` 事实，其身份使用 `series` 种类；points
  作为该样本的 series 点保存。组头不会重复出现在 `samples` 中。
- 空组不投影；组头非有限或点越界只跳过该组/该点，并在客户端诊断中记录。

### 3.4 routes

```json
{
  "routes": [
    {
      "points": [
        [0, 31.2, 121.4],
        [4000000, 31.2001, 121.4001, 10.0, null, null, 3.0, 90.0]
      ]
    }
  ]
}
```

- 每个 route 只有 `points`，没有 `routeID`、`source` 或 `workoutID`；服务端按数组
  下标生成 route 身份，并继承顶层 source。
- 点数组为 3..8 元素：
  `[recordedAtDeltaUs, latitude, longitude, altitude?, horizontalAccuracy?,
  verticalAccuracy?, speed?, course?]`。尾部不可用值可省略；中间不可用值写
  `null`。经纬度必须有限；可选值出现时必须有限。
- 空 route 不投影。route 读取失败时客户端可上传空 `routes`，不阻断 Workout。

## 4. 服务端投影与事实身份

服务端把每个 stream 的 `samples`、每个 series 组头和每个 route 投影为普通 RAI
事实，仍然经过来源分类、UUID 冲突检查、根删除屏障、变更日志、分析待办和事务内
回执。未知字段、未知 stream、错误 unit 和坏点保留在原始载荷中，不阻断整包。

事实身份是服务端生成的确定性 UUIDv5（SHA-1），命名空间固定为：

```
c9f3e2a1-7b64-5d8e-9a0b-1c2d3e4f5a6b
```

名称格式：

```
rai/v1|<datasetID>|<workoutID>|<sampleType>|sample|<ordinal>
rai/v1|<datasetID>|<workoutID>|<sampleType>|series|<ordinal>
rai/v1|<datasetID>|<workoutID>|route|<ordinal>
```

- `ordinal` 是该事实在客户端线上数组中的下标（从 0 开始）。
- 即使某个条目被服务端跳过，它仍占用一个 ordinal；后续身份不会前移。
- 身份生成不使用随机数、时间、序列、全局计数器或数据库状态。相同 Workout 的
  相同字节重放得到完全相同的事实身份。
- 事实 UUID 仍受根删除屏障约束：已删除的 Workout 不能被 v6 重放复活。

测试向量（dataset `11111111-1111-4111-8111-111111111111`，workout
`22222222-2222-4222-8222-222222222222`，类型
`HKQuantityTypeIdentifierHeartRate`）：

| 事实 | ordinal | UUID |
| --- | --- | --- |
| sample | 0 | `34635001-1772-53c6-b1be-0d0ee6697213` |
| series | 0 | `e5920676-90e1-5960-9e25-7a82deac2d69` |
| route（类型为空） | 0 | `c78232c5-3611-5aa0-97fd-ecc1f7ffb71c` |

## 5. 限额与错误

### 5.1 限额

| 项目 | 上限 | 超限结果 |
| --- | --- | --- |
| `samples + series 组数`（逐 stream 累计，含被跳过条目） | 50,000 | 413 `bundle_too_large` |
| series 点数（逐 stream 累计，含被跳过点） | 1,000,000 | 413 `bundle_too_large` |
| routes 数 | 16 | 413 `bundle_too_large` |
| route 点数（全部 route 累计） | 1,000,000 | 413 `bundle_too_large` |
| 解压后 JSON | 32 MiB | 413 `bundle_too_large` |
| multipart wire body | 32 MiB + 64 KiB | 413 `bundle_too_large` |
| gzip part | 8 MiB | 413 `bundle_too_large` |

超限必须整包失败，不得静默截断。

### 5.2 硬错误

整包拒绝，原始字节不入库或按既有接收通道语义处理：

- `protocolVersion` 缺失/不是 6 → 422 `unsupported_payload`（入口分发可能返回
  `unsupported_protocol`）；
- `datasetID`、`datasetEpoch`、`collectorEpoch`、`workoutID` 缺失或非法 → 422；
- `startUs > endUs` 或时间越界 → 422；
- `source` 非法 → 422；来源策略排除 → 422 `source_policy_excluded`；
- `workout` 核心非法 → 422 `invalid_workout`；
- UTF-8 非法、重复对象键、嵌套 > 128 → 422；
- 超过 5.1 任一限额 → 413 `bundle_too_large`。

### 5.3 软跳过

以下条目被跳过，其余事实继续投影：

- 未知 stream `type`、`HKWorkoutTypeIdentifier`、重复类型；
- 与 canonical unit 不一致的 `unit`；
- 非法 sample 三元组、非有限值、倒置时间；
- 空 series 组、越过父组范围的点、非有限父值；
- 非法 route 点、空 route；
- 未知顶层字段（保留在原始载荷中，不报错）。

客户端可在 `status=partial` / `failed` 和 `error` 中记录本端跳过的原因；这些诊断
不改变服务端对已知字段的解释。

## 6. 来源、混源与删除

- 一个 Workout 只有一个顶层 `source`，所有标量样本、series 组头和 route 都继承它。
  v6 不区分“Watch 记录的心率”和“iPhone 记录的心率”，也不上传逐样本来源。
- Workout 顶层来源必须通过冻结策略；混源子事实不改变根 Workout 的准入门槛。
- v6 不上传 sample/route 删除，服务端不根据缺失条目推断删除。
- 根 Workout 删除仍优先：删除屏障、tombstone 和管理删除路径保持既有语义；v6
  重放不能复活已删除 UUID。
- 既有 v3 删除能力、v4 封存包原字节上传和 v5 兼容路径均保留。

## 7. 客户端构建规则（iOS）

- 每个 Workout 只读一次 `HKWorkout`；顶层 source 只取一次。
- 先用既有 `WorkoutSourcePolicy` 做来源门禁；被排除或被判定 pending 的 Workout
  不进行任何 quantity/series/route 读取。
- 标量类型各执行一次 sample 查询，输出三元素数组。
- series 类型各执行一次 `HKQuantitySeriesSampleQueryDescriptor`（含父样本），在内存中
  按父 UUID 分组；父 UUID 只作内存键，绝不序列化。组头取父样本自己的 start/end/
  quantity。当前 series 类型至少包括 RunningPower、RunningGroundContactTime、
  RunningStrideLength、RunningVerticalOscillation；RunningSpeed 按既有 fixture
  保持标量。
- 不做逐样本查询、不落盘往返、不生成逐样本 UUID/来源/元数据，不调用
  `Normalizer.source(sample)`。
- 排序确定：streams 按 type；samples 按 start/end/value；series 组按 start/end/
  value；组内点与 route 点按 start/end。相同输入必须得到相同字节。
- 单个 metric 查询失败只标记该 stream 的 `status:"failed"` 与 `error`，其余 stream
  继续。series 组失败不阻断其他组；route 读取失败上传空 routes。
- 一次组装 JSON、一次序列化；最终字节写入 Outbox 后，重试必须复用同一文件，不得
  重新读取 HealthKit 生成不同字节。
- 传输沿用 multipart `file` + gzip 协商 + 后台传输。投递前先查
  `GET /v2/sync/workout-ingests/{uuid}`：`accepted`/`committed` 不再读取或重发；
  只有 `not_found` 才读取一次并冻结字节。
- 任务记录 payload 版本（4 或 6），投递时不得把 v6 字节按 v4 校验或反向混淆。

## 8. 兼容与回退

- v3：旧 Outbox/capture/completeness/删除路径不变。
- v4：既有原始载荷、封存字节和投递逻辑不变；v6 客户端回退时构建 v4 字节。
- v5：未提交的 v5 编码和合同保持原样；v6 不修改 v5 文件。
- v6 不新增锚点、删除状态、完整性清单或迁移；旧的 v4 任务字段仍可解码，已封存
  的 v4 包按原字节上传。
- 服务端 v6 默认关闭（`RAI_WORKOUT_PAYLOAD_V6=1` 才在握手声明 6）。客户端
  `v6Enabled` 默认关闭；只有握手返回 6 才使用 v6。

## 9. 验收与测试入口

- 服务端单元测试：`apps/server/internal/syncing/workout_bundle_v6_test.go`；
  HTTP 协商测试：`apps/server/internal/httpapi/workout_ingest_test.go`；
  真实隔离 PostgreSQL 集成测试：`apps/server/integration/workout_ingest_v6_test.go`。
- iOS SwiftPM：`ios/RAI/Tests/SyncCoreTests/WorkoutBundleV6Tests.swift`；
  App XCTest：`ios/RAI/AppTests/RawStreamSyncEngineTests.swift`。
- 本机入口：`scripts/local/test.sh swift` 与 `scripts/local/test.sh simulator`。
- 全部测试只使用合成数据；不得读取生产数据库、真实健康值或凭证，不得自动部署。
