# RAI 同步协议 v6：RQrun 式原始序列一次打包

状态：实现合同。v6 只借 RQrun“单遍读取、按类型打包原始序列、一次上传”的原则，
不复制其字段名或身份规则。客户端提交 HealthKit 观察到的原始 Workout、标量序列、
series 分组和 route；服务端只解释已知字段，未知字段与坏点保留或跳过，不阻断整包。
运行时以 `GET /v2/handshake` 返回的 `workoutPayloadVersion=6`、
`workoutUUIDIngestVersion=1` 和实际限额为准。v6 是唯一支持的 Workout 同步协议；
v3/v4/v5 的构建器、解码器与兼容入口已删除，旧客户端必须升级。

Workout v6 采用前台手动上传：只有用户显式触发“同步”才读取 HealthKit 并发送；
启动、回到前台、健康数据变化都不自动上传。每轮先完成一次握手，然后逐条 Workout
读取、组装、发送并展示本轮回执结果。客户端没有 Workout 上传队列、Outbox 恢复、
后台 URLSession、自动重试或分析轮询。

## 1. 设计目标（历史差异说明）

RQrun 的打包过程是：读一次 Workout，按 series 循环，每个点只写 `heart_rate`、
`start_time`、`end_time`，追加到数组，顶层写一次设备/来源，最后序列化。它是单遍、
无状态、无映射、无基线的。v6 采用同样的数据流，但保留 RAI 自己的合同边界：

- **逐样本 UUID 不再上传**：服务端用 Workout UUID + 类型 + 事实种类 + 线上数组下标
  生成确定性 UUIDv5。相同字节重放得到相同事实身份。
- **HealthKit 对象 UUID 只在顶层记录一次**：`workoutID` 是唯一上传的 HealthKit 对象
  标识。Workout 核心、活动分段、标量、series 父组与点、路线与轨迹点不携带对象 UUID，
  也不重复 `workoutID`。`datasetID`、`datasetEpoch`、`collectorEpoch` 是独立的会话身份字段。
- **逐样本 metadata / source 不再上传**：一个 Workout 只有一个顶层 `source`，所有
  投影事实继承它。混源（Watch + iPhone）不再逐样本区分来源。
- **不做 series 父 UUID 关联**：series 分组内嵌在 stream 中，父样本的 start/end/value
  就是组头；组头本身不再出现在 `samples` 数组里。
- **不做删除同步**：客户端不上传删除，服务端不根据快照缺席推断删除；删除能力只
  保留在服务端管理路径。
- **不携带 observation / coverage / completeness / 锚点 / CaptureManifest**：v6 是
  原始序列载荷，不是采集证明。

与 v4 相比，v6 去掉了逐样本的 `uuid`、`source`、`metadata`，以及 `sourceTable` /
`metadataTable` 索引表、顶层 `series` 和逐样本 `routeID`；顶层 `source` 与顶层
`metadata` 都保留。与 v5 相比，v6 去掉了逐样本 UUID、来源/元数据索引和 series 父
引用；时间增量、三元素点数组和顶层来源仍然保留。

## 2. 传输与协商

v6 只有一个接收入口：

- `POST /v2/sync/workout-ingests/{workoutID}`：首次成功接收的原始字节是该 UUID 的
  唯一权威副本。客户端在用户显式同步时每 Workout 发送一次；返回 200 就表示原始
  载荷已被持久接收，成功与否只取决于该响应，不等待事实投影或分析完成；
- `GET` 同路径只用于显式读取服务端状态，不属于上传流程：上传前不查、成功后不轮询；
- multipart 恰好一个 `file` part，`application/json`，或按握手 `bundleGzipPart`
  发送 `application/gzip`；
- wire body 上限 32 MiB + 64 KiB；decoded JSON 上限 32 MiB；gzip 文件上限 8 MiB；
  解压比上限 100:1 + 64 KiB 启动余量；
- UTF-8、重复对象键、深度 > 128 拒绝；未知顶层字段忽略并保留在原始载荷中；
- 相同 Workout UUID 的重复接收只重放首次 `acceptedAt`，不增加 revision/job，也
  不替换原字节。
- POST 只将带有效 `acceptedAt` 的 `accepted` 回执视为成功。已有 Workout、没有接收行
  时返回 409 `workout_ingest_missing_receipt`；GET 的旧 `already_imported` 状态不代表
  本次原始字节已可靠接收。

GET 的 `commitRevision`、`analysisStatus` 与 `analysisJobIDs` 是接收提交那一刻的快照：
`analysisStatus=queued` 只表示提交时已入队分析任务，Worker 完成后该字段不会改变。需要
实时进度或结果时读取 `/v2/jobs/{id}` 与 `/v2/analysis/workouts/{id}`；不要把快照当作
当前分析状态，也不要为等待分析而轮询该状态端点（上传流程本来就不轮询）。

客户端不因重复 UUID 重新上传已成功的 Workout；服务端重放保证同一 UUID 再次提交
也不会产生新事实或新分析任务。客户端不保存待上传 Workout 状态，也不在上传中断后
自动恢复：本轮中断即结束，下一次用户点击同步会重新读取 HealthKit 并重新构建 v6
字节，服务端按 UUID 去重。

服务端无条件声明 `workoutPayloadVersion=6` 与 `workoutUUIDIngestVersion=1`；没有
版本协商、回退开关或旧协议构建器。客户端握手缺少这两个能力（或返回其他载荷版本）
时立即失败，绝不把 v6 字节发给其他协议，也绝不生成 v3/v4/v5 字节。

v6 字节仍遵循“原始载荷落库、服务端只解释已知字段、未知字段保留不丢”的 UUID
接收通道。`workoutUUIDIngestAccepting=false` 时新上传暂停（`workout_uuid_ingest_paused`），
已接收 UUID 的状态读取与重放不受影响。

原始 JSON 字节保存在 `workout_ingests.raw_json`，其 digest 始终来自原文。
`workout_imports.payload` 对新 v6 记录只保存服务端内部来源/事实身份投影，
不把原文强转 JSONB；未知扩展、超出后续数值解释范围的数值和 JSONB 不支持的
字符串转义仍完整保留在原文，不阻断其他已知事实。该内部投影不是上传线格式。
读取既有 v6 JSONB 历史时也不按旧 observation 类型解释扩展。

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

`workout` 至少包含合法的 `activityType` 与 `durationUs`；未知字段保留在原始载荷中。
可选 `activities` 保留分段的时间、时长、configuration、统计和事件，但不携带 `uuid`。
服务端按原数组下标生成活动的内部身份；额外传入的活动 `uuid` 仅保留在原文，不参与投影。

#### 3.1.1 顶层 `metadata`

线格式上 `metadata` 可以省略，但对 iOS 采集的 Apple Watch 跑步它是分析必填。服务端
`workoutfeatures` 的 environment 投影只从这里读室内/室外与天气（`HKIndoorWorkout`、
`HKWeatherTemperature`、`HKWeatherHumidity`）。省略不是“暂时缺省”：它让
`environment.state` 变成 `missing`，随后 load、performance、CS、endurance、VO2 全部
按 `environment_unknown` 降级。首次成功接收的原始字节是该 `workoutID` 的唯一权威副本，
事后重新读取或服务端补写都无法恢复，因此客户端必须上传。

- 允许的 key 由 `contracts/workout-metadata-registry-v2.md` 的 workout scope 决定。
- 数量型 key 写作 `{ "value": <number>, "unit": "<registry unit>" }`；文本与布尔是标量。
- 客户端 fail-open：未登记的 key 以 JSON-safe 形式照原样带上，不阻断整包（与逐样本/活动
  metadata 的既有处理和本节“未知字段不拒绝整包”一致）。登记过的 key 一律按 registry 类型
  编码，未知 key 不会被丢弃也不影响 environment 投影。
- 服务端 `projectMetadata` 只保留可 JSON 投影的值，其余静默丢弃，不因此拒绝整包。
- 共享黄金向量：`contracts/fixtures/workout-bundle-v6-metadata.json`。iOS 的编码结果与
  服务端的投影/分析结果都对着它断言。

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
- 空 route 不投影。室内或没有路线的 Workout 上传空 `routes`/省略 `routes` 是合法的；
  但客户端已经开始读取路线后发生错误或中断，必须让该 Workout 上传失败，不得用空
  `routes` 掩盖读取缺失。

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
rai/v1|<datasetID>|<workoutID>|activity|<ordinal>
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

客户端可在 `status=partial` 和 `error` 中记录本端跳过的坏点/坏组；这些诊断不改变
服务端对已知字段的解释。查询级失败（已开始的类型读取报错、route 读取中断、超过
限额）不是软跳过：该 Workout 不上传，客户端报告失败原因。

## 6. 来源、混源与删除

- 一个 Workout 只有一个顶层 `source`，所有标量样本、series 组头和 route 都继承它。
  v6 不区分“Watch 记录的心率”和“iPhone 记录的心率”，也不上传逐样本来源。
- Workout 顶层来源必须通过冻结策略；混源子事实不改变根 Workout 的准入门槛。
- v6 不上传 sample/route 删除，服务端不根据缺失条目推断删除。
- 根 Workout 删除仍优先：删除屏障、tombstone 和管理删除路径保持既有语义；v6
  重放不能复活已删除 UUID。
- 服务端删除接口只供管理路径使用；客户端没有删除上传，旧上传也不能复活已删除 UUID。

## 7. 客户端构建与上传规则（iOS）

- Workout 上传只由用户在同步页面显式点击触发。启动、回到前台、HealthKit 观察回调、
  后台任务完成都不自动读取或上传 Workout；日常事实/VO2 的自动路径与本协议无关。
- 每轮同步先完成一次握手，然后逐条 Workout 依次执行“读取 → 组装 → 发送 → 记录本轮回执”。
  用户可中途取消；取消后不再开始下一条，已完成条目的结果保留。
- 关闭发起上传的列表页/详情页或进入后台时取消本轮；关闭其他详情页不影响列表页
  已发起的批量操作。取消覆盖尚未开始的所选项，最终计为上传失败。
- 历史列表按开始时间分页，边界时间相同的记录全部纳入同页（因此页数可超过请求
  条数）；下一页只查询更早的开始时间，不按结束时间筛选。
- 每个 Workout 只读一次 `HKWorkout`；顶层 source 只取一次。
- `HKWorkout.uuid` 只序列化为顶层 `workoutID`；`HKWorkoutActivity.uuid`、样本 UUID、
  series 父 UUID 和路线 UUID 均不序列化，明细也不重复写入 Workout UUID。活动只保留
  时间、时长、configuration、统计、事件与 metadata。
- 先用既有 `WorkoutSourcePolicy` 做来源门禁；被排除或被判定 pending 的 Workout
  不进行任何 quantity/series/route 读取，直接记录该条失败原因。
- 标量类型各执行一次 sample 查询，输出三元素数组。
- series 类型各执行一次 `HKQuantitySeriesSampleQueryDescriptor`（含父样本），在内存中
  按父 UUID 分组；父 UUID 只作内存键，绝不序列化。组头取父样本自己的 start/end/
  quantity。当前 series 类型至少包括 RunningPower、RunningGroundContactTime、
  RunningStrideLength、RunningVerticalOscillation；RunningSpeed 按既有 fixture
  保持标量。
- 路线读取一次；室内或没有路线的 Workout 允许空 `routes`。
- 顶层 `metadata` 用 `Normalizer.rawMetadata(workout.metadata, scope: .workout)` 编码：
  登记 key 走 registry 类型化编码（数量写作 `{value, unit}`），未登记 key 以 JSON-safe
  形式保留、不阻断上传；空 metadata 不写该字段（省略等同空）。不使用会抛
  `metadata_requires_review` 的 `workoutMetadata`：一个陌生 key 不应让整条记录传不上去。
- 不做逐样本查询、不落盘往返、不生成逐样本 UUID/来源/元数据，不调用
  `Normalizer.source(sample)`。
- 排序确定：streams 按 type；samples 按 start/end/value；series 组按 start/end/
  value；组内点与 route 点按 start/end。相同输入得到相同字节；确定性用于服务端事实
  身份稳定和测试，不是客户端重试复用字节的依赖。
- 已开始的任一 metric 查询报错、route 读取报错/中断或任一限额超限，该 Workout 本轮
  失败，不上传部分结果；未知字段、未知 stream、已知坏点仍按第 3、5 节处理。合法空
  stream、室内无路线可以上传。
- 一次组装 JSON、一次序列化；gzip 与明文在发送前只选择一次，发送后不在本轮内换用
  另一种表示。每个 Workout 只发一次 POST；不预先 `GET` 状态、不自动重试、不轮询、
  不使用后台 URLSession、不写 Outbox。
- 成功只表示服务端 200 持久接收（首次接收或同 UUID 重放）；不等待事实投影或分析完成。
  失败显示服务端错误或本端读取/构建原因。批量同步只统计本轮成功/失败条数，不保存
  跨轮待办；训练详情如需分析结果，另行读取服务端数据。
- 不生成 sample/route/Workout 删除；客户端没有删除上传。

## 8. 硬切与旧状态处理

- 服务端只解码 `protocolVersion=6`；v3/v4/v5 解码器、旧 bundle 入口和
  `RAI_WORKOUT_PAYLOAD_V6` 开关均已删除。
- iOS 只构建 v6；握手不满足 v6 能力时以 `unsupported_handshake` 失败。
- 旧版本遗留的 Workout 上传任务、冻结载荷、Outbox 恢复与后台传输不再续传或自动
  上传；重建索引时释放这些载荷。用户再次显式同步时从 HealthKit 重新读取并构建 v6，
  服务端按 UUID 重放首次接收，不产生重复事实。
- 旧版本已确认的任务与回执保留为只读历史；客户端不重新上传该 Workout 的旧字节，
  也不自动读取其分析状态。
- 新流程没有成功后的状态恢复路径，也不在任何失败路径上回退到 v3/v4/v5。
- completion/completeness 控制面与日常事实/VO2 是独立协议；它们不属于 Workout
  v6 载荷，也不恢复客户端删除或快照缺失推断。

## 9. 验收与测试入口

- 服务端单元测试：`apps/server/internal/syncing/workout_bundle_v6_test.go`；顶层
  metadata 的跨语言契约（客户端夹具字节 → 解码 → environment 投影）：
  `apps/server/internal/syncing/workout_bundle_v6_metadata_test.go`；
  HTTP 能力与入口测试：`apps/server/internal/httpapi/workout_ingest_test.go`；
  真实隔离 PostgreSQL 集成测试：`apps/server/integration/workout_ingest_v6_test.go`。
- iOS SwiftPM：`ios/RAI/Tests/SyncCoreTests/WorkoutBundleV6Tests.swift` 与
  `ios/RAI/Tests/SyncCoreTests/WorkoutIngestTests.swift`（前台单次 POST、gzip/明文
  回退、回执校验、无状态查询/重试）；
- App XCTest：`ios/RAI/AppTests/RawStreamSyncEngineTests.swift`；
- 本机入口：`scripts/local/test.sh swift` 与 `scripts/local/test.sh simulator`。
- 全部测试只使用合成数据；不得读取生产数据库、真实健康值或凭证，不得自动部署。
