# RAI V2 — RQrun Phase 3 数据完整性实施与验收

日期：2026-11-01（Asia/Shanghai）。本报告覆盖 RQ-004、RQ-007、RQ-011、RQ-012、RQ-013。

## 1. 执行摘要

**自动化：PASS。真机：指定单 Workout 闭环 PASS；整套真机验收 PARTIAL（剩余场景 BLOCKED / 未验证）。总体：PASS WITH RISKS。**

- 自动化反例、Phase 1/2 回归、真实隔离 PostgreSQL 18.6、Swift Testing、iOS App 单元测试、Debug/Release 构建、OpenAPI lint 和依赖边界均通过。
- iPhone 12 Pro Max 最终复验使用新的验收 Outbox 命名空间，保留旧 Outbox；只读取用户指定的 2026-10-26 约19:50跑步。5466 个发现事件、12 个独立 scoped anchor、范围外 anchor=0；重复无变化查询无新 work，组包期间无虚假 generation，ACK 后无待交付 generation。来源为 `apple_watch`。
- 一个 Workout、一次 multipart、一个 import：5464 条 Sample（其中 HeartRate 349）；GPS 原始采集/上传包/数据库 chunk/投影均为1768点，1个chunk且已seal；本地采集与 PostgreSQL 的双 manifest 均为 `af054182ed15a0551542f3af13a84c045635c01189ad5795ee47e9cc22c9957b`，projection 的 sequence、时间及坐标/可选值与 chunk 逐项一致。bundle SHA-256 为 `2c7ae413f32026f9f2c23121365f3d9d7ecf736c4381dd89a39f5f6f19bd2a44`。仅在授权的隔离后端处理真实记录，证据不含健康值、GPS 坐标、完整 UUID 或 token。
- 真机验收范围限于一个已授权的指定测试 Workout；尚未单独完成后台强杀、断网恢复、真实删除、真实 ACK 丢失或物理断电的真机场景，故总体仍为 PASS WITH RISKS。
- 入口 HEAD：`aeb769c82f6af6c47d7cac82874f48ff37f635fc`。初次执行记录了 Git 五项输出及670文件临时 SHA-256 基线；续跑时该临时目录已不可用，因此不声称现在能复核初始670文件全量比较。续跑重新保存681文件 SHA-256、Git五项输出并持久化为 [工作区保护证据](../evidence/rqrun-phase3-workspace-baseline.json)，未发现续跑前文件被删除。00014/00015和原方案与 Phase1 已保存 SHA-256 一致。未执行 reset/clean/checkout/restore/stash/commit/push，未操作生产数据库，未修改旧 `running`。
- Phase 3 未改变整体同步架构：仍是一个 Workout 一个逻辑 bundle、一次 multipart、Outbox、Durable ACK、P4 Raw → Dirty → Job → Analysis → Publication。没有恢复逐 Sample HTTP，也没有第二套数据库或 Route 系统。
- 本阶段唯一新增 migration 为 `00016_route_timestamp_envelope.sql`；schema 从 15 升为 16，`protocolVersion` 仍为 3。00014/00015 及更早 migration 未改写。

机器可读证据：[总证据](../evidence/rqrun-phase3-2026-11-01.json)、[真机与数据库比较](../evidence/rqrun-phase3-device-2026-11-01.json)、[最终完整回归日志](../evidence/rqrun-phase3-logs/all.log)。

## 2. RQ-004：HealthKit Anchored Discovery

### 2.1 原实现问题与根因

源码审计确认入口已有 `HKAnchoredObjectQueryDescriptor` 和 `HKQueryAnchor` 安全归档，但 `SyncCoordinator` 只用单 UUID `readWorkout` 做删除检查；没有正式的全局发现日志、逐 Query 流 anchor、未知父删除事件队列，也没有把发现日志与 Outbox 恢复边界放在同一持久化边界。`recentWorkouts(limit:)` 是 UI 投影，不能作为增量同步依据。若先推进 anchor 再保存发现对象，进程崩溃后 HealthKit 不会再次返回这些变化，会形成永久数据丢失。

### 源码审计、依赖与修改计划

以下是实施前对现有入口的结论；Phase1/2 已修复项保留，不重复重建。

| RQ | 真实代码入口 / 根因 | 依赖与计划文件 |
|---|---|---|
| 004 | HealthKitClient 已有 anchored descriptor/secure anchor；SyncCoordinator 缺少正式全局发现、逐流持久化及未知父删除日志 | HealthKitClient、SyncCoordinator、RAIApp、OutboxStore；新增 HealthDiscovery 和测试；先日志/意图，再anchor |
| 007 | Normalizer 的 bundle source 不能充分表达 SourceRevision/HKDevice；不能用设备注册的 iPhone 身份替代 Watch 来源 | 新增 bundle_observation；Normalizer/source policy、bundle decode、import/history、analysis_input；复用 workout_imports |
| 011 | WorkoutSyncTask 上传/分析阶段不能代表各类型查询与持久化结果 | WorkoutCoverage、assembler、Outbox ACK、observation；独立记录查询/本地/服务端状态 |
| 012 | HealthKit Route 耗尽、route.count、已采点持久化与 bundle seal 没有统一可恢复边界 | HealthKitClient captureRoutes、HealthIntegrity、RouteBuilder、原 route manifest/chunk/seal；保留服务器完整性机制 |
| 013 | Bundle Route 的单调时间校验混淆 timestamp 与 sequence，且数据库极端微秒转换舍入 | WorkoutBundle 校验、RouteBuilder min/max、diagnostics、原投影函数；仅新增00016修复精度 |

已修复证据：快照缺席保护、未知 DELETE 屏障、dataset 锁/P4 dirty/job、严格 ACK、精确原包退避和崩溃恢复均已有实现；本阶段运行原 `workout_safety_test.go`、`workout_reliability_test.go` 和跨语言 harness 验证，没有重复另建系统。

### 2.2 Anchor 架构

`ios/RAI/Sources/SyncCore/HealthDiscovery.swift` 新增：

- `DiscoveryQuery`：身份由 `contract`、HealthKit `type`、可选 `workoutID` 和 `initialization` 组成。不同类型/Workout 不使用混合 anchor。
- `DiscoveryFlow`：独立保存类型、范围、anchor、staged batch、最近处理时间、`hasMore` 和错误。`contract != 1` 或未知初始化策略会被拒绝，不会静默复用旧 anchor。
- `DiscoveryEvent`：保存类型、UUID、upsert/delete、原始 HK 对象档案路径和 SHA-256、所属 Workout、generation 和状态。upsert 事件身份使用可支持事实及其原始 provenance 的确定性摘要，档案 SHA-256 独立用于恢复校验；避免安全归档对象图差异制造新 work。无法正规化的对象保守回退档案摘要并留待审查；delete 使用稳定 `type:uuid:delete` 身份。
- `DiscoveryState`：保存所有 flow、事件、`factKey -> workoutID` 父索引、work generation、Workout 档案索引、扫描游标和受控重新发现覆盖表。

`HealthKitClient` 使用真正的 anchored query：

- `HKAnchoredObjectQueryDescriptor`、持久化 `HKQueryAnchor`（`NSKeyedArchiver` + secure coding），无内存 anchor。
- 一个全局 `HKWorkoutType` 流；每个已知 Workout 的 scoped Workout 流；每个 Workout 的 10 个已支持 Quantity 类型流；每个 Workout 的 `HKWorkoutRoute` 流。当前产品没有启用 Category 样本类型，明确报告为 unsupported，不偷偷扩展睡眠等类型。
- 全局 Workout 每批 25，Quantity 每批最多 1000，Route 每批 1；`SyncCoordinator` 对每个流最多连续处理 4 个有界批次，下一轮从持久 anchor 继续。
- `HKObserverQuery` + background delivery 只负责唤醒；通知不包含事实，也绝不作为删除证据。空结果不表示权限拒绝，也不表示 HealthKit 绝对无数据。

### 2.3 持久化顺序

正确顺序已落在源码和测试中：

```text
HKAnchoredObjectQuery
  -> 新增对象 / HKDeletedObject
  -> 解析 Workout / Sample / Route 关系
  -> 写 HK 原始对象档案（原子文件）
  -> stageDiscovery：发现事件 + work generation + Outbox 可恢复意图
  -> 同一 state.json 原子替换提交
  -> promoteDiscovery：再次核验 batch/events/档案
  -> 同一 state.json 原子替换推进 anchor
```

`stageDiscovery` 不推进旧 anchor；`promoteDiscovery` 要求 `flow.anchor == batch.previousAnchor`、`flow.staged == batch.id` 且所有事件仍存在。若 stage 或 anchor commit 失败，旧 anchor 保留，启动时先 promote staged batch 再查询。崩溃只允许重复发现，不允许漏发现；重复由稳定事件身份和现有 Workout 任务幂等消除。

### 2.4 恢复与异常策略

- 查询前 App 退出：没有 stage，无 anchor 推进；下次重查。
- 数据已 stage、anchor 未推进：重启先执行 `promoteDiscovery`。
- anchor 已推进、上传未完成：Outbox bundle 或发现档案保留，`recoverPending` 继续。
- 同一批次重复返回：事件 ID 去重；generation 不重复增加。
- anchor 解码失败：`HealthKitClient` 在查询前抛 `invalid_healthkit_anchor`；`SyncCoordinator` 不以 nil 查询，不自动清空。`OutboxStore.controlledRediscovery(query:recoveryID:)` 提供显式受控新命名空间，旧 flow、旧事件和旧 Outbox 不清除。
- 查询错误/取消：只记录该流错误，其他流继续；不推进该流 anchor。
- 后台延迟/长时间未启动：全局和 scoped anchor 从持久位置继续；首次仍为可见历史，无自设日期截断。

### 2.5 删除事件

- `HKDeletedObject` 的 Workout 事件产生 `pendingDelete`；`claimBundle` 阻止旧 bundle 上传，真实 DELETE ACK 后才写入本地 deletedWorkouts 并释放原包。
- Sample/Route 删除事件在 scoped 查询中带 Workout 父身份写入发现日志；后续通过 bundle 的 `sampleDeletions` / `routeDeletions` 表达，不产生逐 Sample HTTP。
- 未知父删除保留 `unresolved`，只能由显式 `associateDiscoveredFacts(workoutID:keys:)` 解析；绝不按时间接近猜测父，也不把未知子删除升级为 Workout 删除。
- 已删除 Workout 的迟到子删除使用 `deletionOnly` bundle、原 Workout 档案和既有 `change_events`/屏障回执；不复活父 Workout。

### 2.6 查询范围与“不全库扫描”

- 同步发现使用 anchor；`recentWorkouts(limit:100)` 仅用于 UI 展示，不参与 `discoverChanges` 的同步水位。
- 已知 Workout 的扫描游标持久化，每轮最多 4 个 Workout；下一轮继续，不会每次从 nil anchor 重扫全库。
- 首次可见历史初始化是显式的 `all_visible_history` 合同，无未经确认的截断日期；Category 和当前不支持的字段不会伪造。

### 2.7 新增/修改文件

- 新增：`ios/RAI/Sources/SyncCore/HealthDiscovery.swift`、`ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift`。
- 修改：`ios/RAI/App/HealthKitClient.swift`、`ios/RAI/App/SyncCoordinator.swift`、`ios/RAI/App/RAIApp.swift`、`ios/RAI/Sources/SyncCore/OutboxStore.swift`、`ios/RAI/Sources/SyncCore/WorkoutSync.swift`。

## 3. RQ-007：Metadata / Provenance Enrichment

### 3.1 Provenance 字段

Bundle 新增可选 `observation` v1，包含：

- `provenance.workout`：`bundleID`、`name`、`version`、`productType`、`operatingSystemVersion`，以及可选 `device` 的 `manufacturer/model/hardwareVersion/firmwareVersion/softwareVersion`。
- `provenance.samples[]`：每个新增 Sample 的 `sampleType/sampleUUID/source`。
- `provenance.routes[]`：每个新增 Route 的 `routeID/source`。
- `metadata`：仅允许显式列出的 HealthKit metadata key。
- `coverage`：逐类型读取/持久化状态。

以上均来自实际 `HKSource`、`HKSourceRevision`、`HKDevice`、`HKWorkout`、`HKSample`、`HKMetadata`。缺席字段保持省略，没有默认值、没有虚构设备、没有把 RAI 生成字段伪装成 HealthKit 原始字段。

### 3.2 Apple Watch 来源判定

客户端 `HealthSourcePolicy` 与服务端 `HealthKitSource.Classification` 使用同一政策：

- 原生写入者：`com.apple.health`、`com.apple.Workout`，或严格 UUID 后缀的 `com.apple.health.<UUID>`。
- 必须有真实 `productType` 匹配 `Watch数字,数字`；设备命名空间还要求真实 Apple manufacturer/Watch model。
- 名称从不作为证明；“Apple Health”名称、iPhone productType、第三方 writer、缺失 productType 均不判为 Apple Watch。
- 明确父 Workout 关联且子事实自身 bundleID/version 与已确认 Watch Workout 一致时，只标记 `associated_apple_watch`；子事实缺失的 device/productType 保持缺失。
- 第三方 writer 为 `third_party`；其余为 `source_unknown`。`source_unknown` 不进入正式分析，P4 明确失败 `analysis_source_unclassified`，原始包仍保留。

### 3.3 隐私

- 不采集/上传 `device.name`、`device.localIdentifier`、UDI、序列号；`HKDevice` 只允许来源描述字段。
- `metadata` 使用两类 allowlist。Workout：`HKTimeZone`、`HKIndoorWorkout`、`HKAverageMETs`、`HKElevationAscended`、`HKElevationDescended`、`HKWeatherHumidity`、`HKWeatherTemperature`、`HKAverageSpeed`、`HKMaximumSpeed`；Sample 另允许 `HKMetadataKeyHeartRateMotionContext`、`HKMetadataKeyHeartRateSensorLocation`、`HKMetadataKeyUserMotionContext`、`HKMetadataKeyDevicePlacementSide`（整数 0...10）。
- 真机验收发现心率 sample 携带精确 key `HKMetadataKeyHeartRateMotionContext`；客户端与服务端此前的错误拼写已修正。该修正仍拒绝未知/私有 metadata，不把枚举值当作设备唯一标识。
- 未知 metadata、未知类型或不兼容单位不会上传：原始 HK 对象留在本地发现档案，上传明确 `metadata_requires_review`，不静默丢弃。
- 日志、HTTP 请求日志、Metrics、验收报告均不打印健康值、GPS 坐标、完整 Workout UUID、设备唯一标识或 token。

### 3.4 服务端存储

- 复用 `workout_imports` 的 append-only `(dataset_id, workout_uuid, bundle_sha256)` 历史；observation 随原 bundle 保存，不覆盖原 Workout/sample payload。
- `workout_imports` 的 JSONB receipt 保存 `SourceClassification`、观察摘要和 coverage；P4 选取最新 observation，旧观察仍可追溯。
- 服务端重算来源政策、逐成员校验 provenance 一一对应、拒绝重复 provenance 行、拒绝已知子事实归属另一 Workout；不创建伪造的 Watch device registry 行。
- 设备/来源描述变化产生新观察历史并触发 dirty/job；相同语义只变观察时间不重复创建分析任务。
- metadata-only Sample 补充只进入新 bundle 历史及观察语义摘要；health_samples 的原 metadata/content_sha256、原回执保持不变。数值（含浮点位模式）、时间、单位、category、source bundle 的核心冲突继续拒绝；Workout 核心比较保留整数微秒精度。
- P4 逐项核验当前保留 Sample/sealed Route 的 provenance，允许复用历史合格证据，禁止仅靠父 Workout 来源将未知/第三方旧子事实纳入分析；证据不足为 analysis_source_unclassified，raw 保留。

## 4. RQ-011：Workout Data Coverage

### 4.1 字段矩阵

`WorkoutCoverage` 为每个 Workout 保存：

- `workout`：Workout 读取/持久化。
- 10 个已支持 Quantity 类型：heart rate、distance、active/basal energy、running speed/power、step count、ground contact time、stride length、vertical oscillation。
- `cadence`：当前 HealthKit SDK/产品无直接 running cadence 类型，明确 `unsupported`，不用派生值或 Workout statistics 伪造 Sample。
- `elevation_sample`：当前未启用独立 elevation sample 类型，明确 `unsupported`。
- `series:<type>`：多值 Sample 关联和持久化。
- `route:<routeID>`：声明点数、实收点数、完成/错因。
- `route`：Route 枚举汇总。
- `metadata`：本合同来源/设备白名单字段的提取与持久化；缺失字段仍省略。complete 不表示 SDK 每个元数据字段都存在。

### 4.2 状态语义

状态为 `not_queried | observed | no_records_observed | partial | complete | failed | unsupported | unknown`，另有 `queryCompleted`、`localPersisted`、`serverCommitted`、`expectedCount`、`errorCode`、`diagnostics`。

- `no_records_observed` 只表示本次查询成功且没有看到记录，不等于读取权限拒绝，也不等于 HealthKit 绝对无数据。
- `complete` 只表示本客户端定义的查询范围和本地处理完整结束；不宣称 HealthKit 向应用公开了所有潜在数据。
- 查询完成、本地持久化、Outbox 构建、服务端提交、Route seal、P4 分析是不同阶段；`serverCommitted` 只在严格 ACK 后推进，不把分析完成当作采集完整。
- Coverage 不参与删除；缺席、partial、no_records 都不会生成 Sample 或 tombstone。

### 4.3 客户端/服务端一致性

- `performSync` 逐类型读取，记录 query 成功/失败/空结果、series 点数、Route 声明/实收/完成状态；`WorkoutBundleAssembler` 将 coverage 随 observation 放入同一个 bundle。
- 服务端校验 coverage 状态、计数、expected/actual、queryCompleted/localPersisted 和 `complete` 的真实性；`workout_imports` 保留版本历史。
- 重复上传同 hash 返回原 receipt；后续补充数据生成新 bundle/observation 并更新 coverage；Coverage 缺失旧包仍合法上传，但不会进入正式分类分析。
- `pendingReadKeys` 让 partial/failed 的读取有界重试；`DiscoveryWork.hasAutomaticDeliveryWork` 最多自动重试 3 次，新 HealthKit 变化会重新打开，避免无限自动循环。

## 5. RQ-012：Route Completeness

- `HealthKitClient.captureRoutes` 先枚举 Workout 的全部 `HKWorkoutRoute`，最多 16 条；超过上限明确失败，不静默丢弃。
- 每条 Route 建立 append-only `RouteCaptureFile` 和 `RouteCaptureCheckpoint`；每个点以稳定 sequence 写入并 `synchronize`，前缀在重启后逐字重放。声明点数来自 `HKWorkoutRoute.count`，实收点数来自回调；只有 descriptor 正常耗尽、尾部完整且 `实收 == 声明 > 0` 才 `completed`。
- 回调中断、取消、最后回调失败、点数不足、尾部残行、前缀改变、声明点数改变均保留文件并标记 `partial`/错误；不会伪造 completed，也不会覆盖已采集前缀。
- Bundle 使用既有 route manifest/chunk/seal 双 hash 路径；服务端每 5000 点分块，校验连续 chunk ordinal、点数、顺序、points hash、wire hash、manifest、payload manifest 和 seal 完整性。
- 重复 Route 查询/上传复用同一 routeID 和内容 hash；不重复创建路线；seal 只在全部 chunk 后执行。Route 删除走独立 tombstone/`rai_route_delete`，不删除 Workout。
- 客户端不把 Route 查询失败扩大到其他 Workout；失败 Route 保留已采集点，Coverage 为 partial，后续恢复继续。

## 6. RQ-013：GPS Timestamp Rollback

- HealthKit Route 返回数组顺序是原始事实；`RouteCaptureFile` 按到达顺序写点，`WorkoutBundlePoint` 不携带时间排序，服务端 `bundleRouteChunks` 按数组位置派生连续 sequence。没有按 timestamp 排序、去重或重写数组。
- `recordedAtUs` 原样转 Unix 微秒；不插值、不修正、不改 lat/lon。相同时间戳保留；局部回退保留；多个连续回退保留；回退后恢复递增也保留。
- `RouteTimeDiagnostics` 检测并记录 `timestamp_equal`、`timestamp_regression`、`before_workout`、`after_workout`；诊断与查询/传输完整性分离。没有自行发明“回退阈值”，也不因回退丢点。
- 非法年份/坐标、非有限测量值、非法 course/accuracy 仍拒绝；合法 year 1..9999 的时间戳允许。
- 服务端 `rai_route_append_chunk` 和 projection 触发器改为按整数秒 + 微秒拆分构造 `timestamptz`，并用 numeric epoch 微秒比较包络，修复 PostgreSQL 将 year-9999 末微秒舍入到 year-10000 的问题。migration `00016` 只替换两个函数，不新增事实表、不改变 wire 字段。
- P4 的 route 输入仍按 sequence 读取，窗口外点只在分析阶段过滤；原始 Route/Route points 不被分析结果覆盖或回写。

## 7. 数据安全证明

1. **Anchor 是否可能先推进导致永久丢失？** 不会。发现事件、原始档案、work intent 先在同一 `state.json` 原子替换中提交；anchor 在第二步 promote 且核验 batch/event/档案后才推进。失败和重启由 staged batch 重放。
2. **App 被杀死后能否恢复？** 自动化覆盖进程重启/持久状态恢复，真机中途强杀尚未验证。`OutboxStore` 持久化 staged flow、事件、work、route capture、bundle/receipt；启动先 promote staged batch，再恢复 bundle/上传。
3. **HealthKit 删除事件是否可能丢失？** 不会静默丢失。删除 `HKDeletedObject` 先形成持久事件；已知父进入显式 bundle 删除，未知父保留 `unresolved`；旧包不能按普通快照缺席删除。
4. **Sample 是否可能关联到错误 Workout？** scoped `HKQuery.predicateForObjects(from:)` 建立父关系；本地父索引冲突会显式失败；未知父不猜测。服务端还会拒绝把已知子事实删除到另一 Workout。
5. **Route 是否可能静默截断？** 当前路径与反例测试均要求显式失败或 partial。声明点数、实收点数、前缀重放、文件尾部完整性和 descriptor 完成信号共同校验；任何不符为 partial，保留已采集点，不 seal 为 complete。
6. **GPS 时间回跳是否会导致数据丢失？** 不会。点序不改、时间不改、不去重不排序；回跳只产生诊断，传输继续或在非法数据处明确失败。
7. **来源不明的数据是否会被伪装成 Apple Watch？** 不会。分类需要真实 productType/device 证据；名称、iPhone productType、第三方 writer 均不通过；未知/第三方保留为 source_unknown/third_party，不进入正式分析。
8. **Coverage 是否可能错误声明完整？** 在已验证查询范围内，`complete` 要求查询完成、本地持久化、非零/or expected 匹配；服务端重算 counts；空结果是 no_records_observed，partial/failed 不能伪 complete。
9. **ACK 异常是否仍然保留 Outbox？** 是。原 bundle 文件/hash/boundary 在 POST 前持久化；只有严格验证的 ACK 才释放；GET 404、malformed ACK、网络/超时/5xx 都保留并重试。
10. **Phase 1 删除屏障是否仍然有效？** 是。完整 integration 和 Phase 1/2 回归通过；未知 Workout DELETE 也建屏障；dataset 锁顺序、tombstone fencing、P4 dirty/job 和旧提交回放均未破坏。

## 8. 测试证据

自动化测试均使用合成 HealthKit/Workout/GPS 数据；真机验收只记录计数、哈希和状态，不记录健康值、真实坐标、完整 UUID 或 token。

| 门禁 | 命令/环境 | 结果 |
|---|---|---|
| Go unit | `go test ./...` | PASS |
| Go race | `go test -race -count=1 ./...` | PASS |
| Go vet | `go vet ./...` 和 `go vet -tags=integration,swiftclient ./...` | PASS |
| PostgreSQL integration | `PGBIN=<pg18> scripts/test-postgres.sh go test -race -count=1 -tags=integration,swiftclient -p 1 -timeout=30m ./...`；一次性 PG18.6、Unix socket、fsync/full_page_writes/synchronous_commit/data_checksums=on | PASS；integration 239.882s；`RAI_TEST_LEFTOVER_DATABASES=0`，`RAI_TEST_CLUSTER_REMOVED=1` |
| Phase 3 targeted PG | `go test -race -count=1 -tags=integration -run TestPhase3 -v ./integration` | PASS：route clock/count/chunk/hash/replay、microseconds、provenance/coverage history、explicit child deletion、rollback、legacy Sample metadata、未知历史子来源阻断与历史合格证据复用 |
| Cross-language | Swift Outbox → URLSession → Go HTTP → isolated PG18；`-run TestWorkoutReliabilitySwiftClientPostgres/phase3 -v` | PASS：最终完整套件实际执行18场景（原15场景＋`phase3_observation`、`phase3_child_delete`、`phase3_deleted_parent`）；普通 Swift 跳过不替代这些实际执行 |
| Swift package | `swift test`（macOS host） | PASS：96 tests / 11 suites；live database XCTest 在无 Go harness 时明确 1 skipped，未计作通过 |
| iOS app unit | `xcodebuild ... -only-testing:RAITests test`（iPhone 12 Pro Max simulator，iOS 26.5） | PASS：22/22 |
| iOS Debug | `xcodebuild ... -configuration Debug -destination 'generic/platform=iOS' CODE_SIGNING_ALLOWED=NO build` | PASS |
| iOS Release | `xcodebuild ... -configuration Release -destination 'generic/platform=iOS' CODE_SIGNING_ALLOWED=NO build` | PASS |
| OpenAPI | `npm exec --yes --package=@redocly/cli@2.71.1 -- redocly lint contracts/openapi-v2.yaml` | PASS；31 条既有 warning |
| Boundary | `python3 scripts/check-boundaries.py` | PASS；249 packages |
| Migration | 空库 fresh + 同一隔离库连续执行 `rai migrate` 两次 | PASS；schema 16 |
| Real device | signed 验收 App，仅指定 Workout；全新本地验收命名空间及一次性PG18.6 | 单记录闭环 PASS：5464 samples、1768 source/bundle/chunk/projection points、12 scoped anchors、0范围外anchor；source/数据库 manifest、逐点投影与 bundle hash 一致。完整后台/强杀/断网/删除场景未验证 |

补充：`swift test` 的一次早期 Xcode 链接失败由既有 DerivedData 陈旧引起；清理仅 build artifacts 并使用 fresh DerivedData 后通过，没有跳过或放宽测试。初次全 scheme 测试中的 UI tests 未配置 live test host，不计入 Phase 3 结果；后续只运行 `RAITests`，22/22 通过。

### 逐项测试名称索引

以下文件内列出的用例均包含在上述最终门禁中，使用合成数据；真机只使用单独设备证据。

`ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift`：

- `twoPhaseAnchorCrashReplayAndIndependentTypes`
- `boundedHistoricalBatchesSurviveEveryRestart`
- `writeFailuresCannotPromoteOrLoseJournal`
- `unresolvedDeletionResolvedOnlyByExplicitIndex`
- `invalidAnchorAndContractStayQuarantined`
- `deletionBlocksOldUploadWithoutFalseACK`
- `childDeleteParksExactUnacknowledgedBytes`
- `parentDeleteACKCannotReleaseLaterChildDeletionBundle`
- `existingLocalTaskKeepsLateChildChangesSelected`
- `partialReadACKKeepsCoverageAndRetriesBounded`
- `equivalentFactsDoNotCreateWorkWhenArchiveBytesDiffer`

`ios/RAI/Tests/SyncCoreTests/HealthIntegrityTests.swift`：

- `provenanceUsesEvidenceAndNeverNames`
- `coverageEmptyFailedUnknownAndProcessingAreDistinct`
- `routeCaptureReplaysPrefixKeepsAllTimestampsAndInterruptedBytes`
- `routeIncompleteTailAndCountNeverComplete`
- `routeSpoolChunkBoundaryRegressionAndIllegalValues`
- `deviceScopedNativeWatchRequiresProductAndDevice`

`apps/server/internal/syncing/bundle_integrity_test.go`：

- `TestPhase3ObservationStrictSourceCoverageAndPrivacy`
- `TestPhase3HeartRateSampleMetadataAllowlist`
- `TestPhase3DuplicateProvenanceCannotHideAnUnattributedMember`
- `TestPhase3GPSRetainsOriginalOrderAndTimeEnvelopeDiagnostics`
- `TestPhase3ExplicitDeletionPresenceAndConflicts`
- `TestPhase3DeviceScopedAppleHealthSourceRequiresRealWatchEvidence`
- `TestPhase3QuantityMetadataRetainsStrictJSONNumbers`
- `TestPhase3MetadataEnrichmentCannotHideMicrosecondCoreChange`
- `TestPhase3CoverageOptionalFieldSpellingIsExact`

`apps/server/integration/workout_integrity_test.go`：

- `TestPhase3RouteClockRawOrderCountsChunksHashesAndReplay`
- `TestPhase3MetadataProvenanceCoverageAppendHistoryAndImmutableCore`
- `TestPhase3ExplicitChildDeletesAreAtomicIdempotentAndScoped`
- `TestPhase3ChildDeleteObservationCommitFailureRollsBack`
- `TestPhase3LegacyImportAcceptedButUnclassifiedAnalysisBlocked`
- `TestPhase3LateChildDeletionAfterWorkoutBarrierDoesNotResurrectParent`
- `TestPhase3RouteMicrosecondsRemainExactAcrossAllowedRange`
- `TestPhase3LegacySampleMetadataEnrichmentRetainsFrozenFactAndOldReceipt`
- `TestPhase3WorkoutSourceCannotReclassifyUnprovenLegacyChildren`

`apps/server/integration/workout_safety_test.go`：

- `TestWorkoutBundleConcurrentSnapshotsRetainUnion`
- `TestWorkoutBundleExplicitChildDeletionBoundary`
- `TestWorkoutDeleteBeforeUploadDurableStableAndIsolated`
- `TestWorkoutDeleteFencingBeforeBarrierReplay`
- `TestWorkoutDeleteAndImportBothLockOrders`
- `TestWorkoutDeleteInvalidatesPublishedP4AndKeepsHistory`
- `TestWorkoutDeleteSupersedesInFlightWorkers`
- `TestWorkoutDeleteRollbackIncludingCommitFailure`
- `TestHTTPWorkoutDeleteBeforeUploadAndRetry`
- `TestWorkoutBundleSnapshotStillRejectsIdentityConflicts`

`apps/server/integration/workout_reliability_test.go`：

- `TestWorkoutBundleRequiredFieldsHTTPAtomic`
- `TestWorkoutBundleZeroAndLegacyOptionalHTTP`
- `TestWorkoutBundleNonfiniteAndMalformedHTTP`
- `TestWorkoutBundleCommitFailureHasNoACK`
- `TestWorkoutBundleLostACKAnd404ThenIdempotentPOST`

## 9. Git 状态

以下为整个既有脏工作区，不代表全部改动都由 Phase3 引入。

```text
$ git status --short --branch
## main...origin/main
 M README.md
 M apps/server/cmd/rai/main.go
 M apps/server/integration/admission_test.go
 M apps/server/integration/p3_sync_test.go
 M apps/server/integration/p7_security_test.go
 M apps/server/integration/page_test.go
 M apps/server/integration/series_test.go
 M apps/server/integration/sync_test.go
 M apps/server/integration/upload_test.go
 M apps/server/integration/workout_package_test.go
 M apps/server/internal/admin/operations.go
 M apps/server/internal/httpapi/server.go
 M apps/server/internal/storage/postgres/analysis_input.go
 M apps/server/internal/storage/postgres/store.go
 M apps/server/internal/syncing/protocol.go
 M apps/server/internal/syncing/store.go
 M apps/server/migrations/checksums.json
 M contracts/CHANGELOG.md
 M contracts/openapi-v2.yaml
 M deploy/postgres/roles.sql
 M docs/ARCHITECTURE.md
 M docs/IMPLEMENTATION.md
 M docs/P7_BACKUP_RESTORE.md
 M docs/P7_DEPLOYMENT.md
 M docs/P7_OPERATIONS.md
 M docs/P7_RELEASE_CHECKLIST.md
 M docs/RAI_MONTHLY_BACKUP.md
 M ios/RAI/App/BackgroundTransfers.swift
 M ios/RAI/App/HealthKitClient.swift
 M ios/RAI/App/ProductModel.swift
 M ios/RAI/App/ProductViews.swift
 M ios/RAI/App/RAIApp.swift
 M ios/RAI/App/SyncCoordinator.swift
 M ios/RAI/AppTests/NormalizerTests.swift
 M ios/RAI/Config/RAI.entitlements
 M ios/RAI/README.md
 M ios/RAI/Sources/SyncCore/BatchBuilder.swift
 M ios/RAI/Sources/SyncCore/OutboxStore.swift
 M ios/RAI/Sources/SyncCore/RAIAPIClient.swift
 M ios/RAI/Sources/SyncCore/RouteBuilder.swift
 M ios/RAI/Sources/SyncCore/Wire.swift
 M ios/RAI/Tests/SyncCoreTests/APITests.swift
 M ios/RAI/Tools/SyncProbe/Probe.swift
 M ios/RAI/UITests/ProductUITests.swift
 M scripts/rai-backup.py
 M scripts/rai-restore.py
 M scripts/test-monthly-restore.sh
?? apps/server/integration/per_workout_sync_test.go
?? apps/server/integration/workout_bundle_test.go
?? apps/server/integration/workout_integrity_test.go
?? apps/server/integration/workout_reliability_swift_test.go
?? apps/server/integration/workout_reliability_test.go
?? apps/server/integration/workout_safety_test.go
?? apps/server/internal/httpapi/bundle_admission_test.go
?? apps/server/internal/storage/postgres/bundle_integrity.go
?? apps/server/internal/storage/postgres/workout_bundles.go
?? apps/server/internal/syncing/bundle_decode.go
?? apps/server/internal/syncing/bundle_decode_test.go
?? apps/server/internal/syncing/bundle_integrity_test.go
?? apps/server/internal/syncing/bundle_observation.go
?? apps/server/internal/syncing/workout_bundle.go
?? apps/server/internal/syncing/workout_bundle_test.go
?? apps/server/internal/syncing/workout_delete.go
?? apps/server/migrations/00014_workout_imports.sql
?? apps/server/migrations/00015_workout_import_history.sql
?? apps/server/migrations/00016_route_timestamp_envelope.sql
?? contracts/sync-v3.md
?? "docs/RQrun-HealthKit-\344\270\212\344\274\240\345\215\217\350\256\256.md"
?? docs/RQrun_PHASE1_RESULT.md
?? docs/V2_PER_WORKOUT_SYNC_AUDIT.md
?? docs/V2_PER_WORKOUT_SYNC_RESULT.md
?? docs/audits/
?? docs/evidence/per-workout-sync-acceptance.json
?? docs/evidence/rqrun-phase1-2026-11-01.json
?? docs/evidence/rqrun-phase2-2026-11-01.json
?? docs/evidence/rqrun-phase2-failed-runs.txt
?? docs/evidence/rqrun-phase3-2026-11-01.json
?? docs/evidence/rqrun-phase3-device-2026-11-01.json
?? docs/evidence/rqrun-phase3-workspace-baseline.json
?? docs/evidence/workout-bundle-2026-10-31.json
?? ios/RAI/Sources/SyncCore/HealthDiscovery.swift
?? ios/RAI/Sources/SyncCore/HealthIntegrity.swift
?? ios/RAI/Sources/SyncCore/WorkoutBundle.swift
?? ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift
?? ios/RAI/Sources/SyncCore/WorkoutSync.swift
?? ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift
?? ios/RAI/Tests/SyncCoreTests/HealthIntegrityTests.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutBundleTests.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift
?? scripts/rai-reset-v2-data.sh
```

```text
$ git diff --stat
 README.md                                          |    4 +-
 apps/server/cmd/rai/main.go                        |    2 +-
 apps/server/integration/admission_test.go          |  168 ---
 apps/server/integration/p3_sync_test.go            |  210 ----
 apps/server/integration/p7_security_test.go        |   31 +-
 apps/server/integration/page_test.go               |   94 --
 apps/server/integration/series_test.go             |   80 --
 apps/server/integration/sync_test.go               |  166 ---
 apps/server/integration/upload_test.go             |  209 ----
 apps/server/integration/workout_package_test.go    |   73 --
 apps/server/internal/admin/operations.go           |    2 +-
 apps/server/internal/httpapi/server.go             |  129 +-
 .../internal/storage/postgres/analysis_input.go    |   25 +
 apps/server/internal/storage/postgres/store.go     |    2 +-
 apps/server/internal/syncing/protocol.go           |   13 +
 apps/server/internal/syncing/store.go              |    3 +
 apps/server/migrations/checksums.json              |    5 +-
 contracts/CHANGELOG.md                             |   35 +
 contracts/openapi-v2.yaml                          | 1233 +++-----------------
 deploy/postgres/roles.sql                          |    6 +-
 docs/ARCHITECTURE.md                               |   27 +-
 docs/IMPLEMENTATION.md                             |  122 +-
 docs/P7_BACKUP_RESTORE.md                          |    2 +-
 docs/P7_DEPLOYMENT.md                              |    4 +-
 docs/P7_OPERATIONS.md                              |    4 +-
 docs/P7_RELEASE_CHECKLIST.md                       |    2 +-
 docs/RAI_MONTHLY_BACKUP.md                         |    6 +-
 ios/RAI/App/BackgroundTransfers.swift              |   23 +-
 ios/RAI/App/HealthKitClient.swift                  |  355 +++++-
 ios/RAI/App/ProductModel.swift                     |   24 +
 ios/RAI/App/ProductViews.swift                     |  284 ++++-
 ios/RAI/App/RAIApp.swift                           |  506 ++++++--
 ios/RAI/App/SyncCoordinator.swift                  |  686 +++++++----
 ios/RAI/AppTests/NormalizerTests.swift             |  225 +++-
 ios/RAI/Config/RAI.entitlements                    |    2 +-
 ios/RAI/README.md                                  |    2 +
 ios/RAI/Sources/SyncCore/BatchBuilder.swift        |    5 +-
 ios/RAI/Sources/SyncCore/OutboxStore.swift         |  372 +++++-
 ios/RAI/Sources/SyncCore/RAIAPIClient.swift        |   88 +-
 ios/RAI/Sources/SyncCore/RouteBuilder.swift        |   11 +-
 ios/RAI/Sources/SyncCore/Wire.swift                |    1 +
 ios/RAI/Tests/SyncCoreTests/APITests.swift         |   93 +-
 ios/RAI/Tools/SyncProbe/Probe.swift                |   50 +
 ios/RAI/UITests/ProductUITests.swift               |   19 +
 scripts/rai-backup.py                              |    4 +-
 scripts/rai-restore.py                             |    2 +-
 scripts/test-monthly-restore.sh                    |    2 +-
 47 files changed, 2848 insertions(+), 2563 deletions(-)
```

```text
$ git diff --cached --stat
(empty)
```

本阶段真正新增：`HealthDiscovery.swift`、`HealthIntegrity.swift`、对应两个测试文件；`bundle_observation.go`、`bundle_integrity_test.go`、PostgreSQL `bundle_integrity.go`、`workout_integrity_test.go`、migration00016、本报告及 Phase3 证据。

修改现有（含原来 untracked 的 Phase1/2 文件）：HealthKitClient、SyncCoordinator、RAIApp、BackgroundTransfers、OutboxStore、WorkoutSync、RouteBuilder、WorkoutBundle/Delivery、RAIAPIClient、Go bundle DTO/decode/import、analysis_input、回归夹具、握手、角色读权限、schema引用、协议/架构文档。`WorkoutBundle.swift`、`WorkoutBundleDelivery.swift`、`workout_bundle.go`、`workout_delete.go`、migration00014/00015等属于继承文件，不把它们列为 Phase3 新建。完整最终哈希与续跑差异见机器证据。

## 10. 未解决问题与风险

- **真机覆盖边界**：已完成指定单 Workout 隔离验收（HealthKit 读取、source provenance、HeartRate、1768 点 Route、POST、Durable ACK）。尚未单独完成真实后台 delivery/强杀、断网恢复、真实 HealthKit 删除、真实 ACK 丢失或物理断电；不能把本次单 Workout 通过扩写为这些场景已通过。
- **基线保留限制**：初次670文件临时快照续跑时已不可用；本报告保留其历史事实但不将其作为当前可复核附件。续跑681文件快照已存入仓库证据，既有文件删除为0；原方案和00014/00015另以 Phase1 持久哈希核验。
- **本地状态升级**：保留旧验收 App 本地状态重跑时曾出现 `workout_core_change_requires_review`，干净安装后通过。说明核心事实 guard 会在检出核心变化时保守阻断；生产升级/受控重建路径仍需产品化。
- **磁盘损坏边界**：写入失败/不完整尾行明确阻断完成并保留原字节；物理掉电、撕裂写入及损坏后的人工恢复未真机验收。
- **Route/Series 内存上限**：Route 查询和 series 捕获仍可能在本地构造大数组；bundle 32 MiB、1,000,000 points 上限会在超限时明确失败并保留原始 capture，但未在真机验证超大 Route 的内存/耗时。
- **P4 输入上限**：分析 route 输入有单独上限；超限会明确失败而不是伪造结果，尚未做生产规模 SLO。
- **Category/SDK 新类型**：当前产品没有启用 Category 类型；running cadence 在当前 SDK 无直接 HealthKit 标识，标记 unsupported，不使用 Workout statistics 替代。
- **受控重新发现**：API 已实现并测试，但 App UI 没有专门的“用户确认后重置 anchor”按钮；生产运维需明确操作入口。
- **旧包/未知 metadata**：为兼容 Phase 2，旧无 observation bundle 仍可上传并被保留，但不会进入正式分析；新包未知 metadata 停止上传待审。该 pending 状态的展示/人工处置流程仍需产品化。
- **迁移 00016**：只修复 route 函数微秒边界，不改变协议 wire；仍需在正式发布前用实际备份/恢复和 fnOS/生产隔离验证。未执行生产迁移。
- **最后分析来源屏障**：真机上传/入库闭环后补充的逐子事实 provenance 屏障由合成 PostgreSQL 反例和最终全量回归验证，没有将其宣称为真机历史混合来源验收。
- **UI tests**：未在本阶段配置 live test host，不能把 `RAITests` 结果当作 UI 验收。

## 11. 停止条件

报告完成。未 commit、未 push、未部署、未执行生产迁移、未连接生产数据库、未操作生产数据；未删除用户原有文件或旧 `running` 工程。停止，等待下一阶段指令。
