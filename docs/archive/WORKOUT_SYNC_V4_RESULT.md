# Workout Sync v4 实施结果（原始载荷重写）

日期：2026-11-09
计划：`docs/WORKOUT_SYNC_V4_RAW_PLAN.md`
合同：`contracts/sync-v4.md`

## 1. 结论

v4 已按用户决定原地重定义为“原始记录上传”，并完成本机单元/集成验证：

- 一个 Workout 一个不可变原始 JSON，`protocolVersion=4`。
- 客户端提交 HealthKit 观察到的 Workout、逐样本内联 `source`/`metadata`、series 三元组和 route 八元组；不构造 source 表、coverage、observation、metadata registry 或删除差集。
- 服务端只解释已知字段并投影到现有规范化表；未知字段、未知流、坏样本、坏点只留在原始载荷中，不阻断整包。原始字节仍是 `workout_imports.payload` 的权威副本。
- 不做删除同步：v4 客户端不发送 `deletions`/`routeDeletions`，不根据 HealthKit 删除事件调用 DELETE；v3 删除语义和管理端路径不变。
- 服务端 `SYNC_V4_ENABLED` 已移除：`X-RAI-Sync-Protocol: 4` 直接协商 v4；客户端 v4 默认开启，`syncProtocolV4=false` 可回退 v3。
- 导入事务边界不变：原始载荷、规范化事实、receipt、change event、dirty scope 与 analysis job 仍在同一事务提交；同字节重试返回原回执。
- v4 此前从未部署，因此不保留旧列式 v4 兼容路径，不做数据迁移或回滚。

## 2. 服务端变更

- `apps/server/internal/syncing/workout_bundle_v4.go`：全文重写为原始载荷解码器。
  - 严格边界：UTF-8、重复键、嵌套深度、四个身份 UUID、`startUs/endUs`、顶层 `source`（分类必须 `apple_watch`，否则 422 `source_policy_excluded`）、Workout 核心（`activityType`/`durationUs`）。
  - 宽容投影：未知 stream 类型、unit 不匹配、重复 stream 类型、非法样本、越界点、非 Apple Watch 子来源、未知 series/route 逐项跳过；重复样本/流保留第一个。
  - 上限：50,000 样本、16 routes、1,000,000 route 点、50,000 series、1,000,000 series 点；超限 413 `bundle_too_large`，不静默截断。
  - 修复 Workout 核心缺失或非法时返回非 `*Error` 的问题；HTTP 层此前会按 500 处理，现在统一 422 `unsupported_payload`（`field=workout`、`reason=invalid_workout`）。
- `WorkoutBundle.SourceTable` / `BundleSourceTable` / `WorkoutBundleV4SourceTable(payload)`：保存并从原始字节重建逐 fact 来源，供 receipt 分类、分析输入、feature 输入与 integrity 校验复用。
- `httpapi/server.go`：`protocolVersion == 4` 直接走原始解码器；handshake 对 `X-RAI-Sync-Protocol: 4` 返回 `workoutPayloadVersion=4`、`bundleProtocolVersion=4`、`bundleCompact=true`，不再依赖环境变量。
- `storage/postgres` 的 `analysis_input.go`、`feature_input.go`、`bundle_integrity.go`：v4 没有 observation 时从最新原始载荷的内联来源解析 provenance；v3 observation 历史路径保持不变。
- v4 的 `Observation`、`CompleteSampleTypes`、`CompleteRoutes`、`SampleDeletions`、`RouteDeletions` 恒为空；旧 200 天完整性扫描不处理 v4。

## 3. 客户端变更

- `ios/RAI/Sources/SyncCore/WorkoutBundleV4.swift`：原始 envelope 编码器（`source`/`metadata`/`workout`/`streams`/`series`/`routes`），无 source 表、无 `uuids`/`src`、无 `kind`、无 `observation`、无删除字段。
- `ios/RAI/App/V4SyncEngine.swift`：逐类型完整读取；每个样本输出 `uuid/startUs/endUs/value/source/metadata`；series 按 `type+sampleUUID` 输出；route 输出 `routeID+source+points`。读取失败只标记该流 `status=failed`/`error`，不影响其它流，也不产生删除。
- `Normalizer.rawMetadata` / `Normalizer.rawWorkout`：已知字段类型化编码，未知 metadata 键递归保留，未知 statistic/单项失败跳过而不使整个 Workout 失败。
- `SyncCoordinator`：v4 路径只保存 multipart、上传、ACK；不再维护 sidecar、coverage、pendingReadKeys 或 `pendingDelete`；不调用 `deleteWorkout`。
- `HealthDiscovery` / `OutboxStore`：移除 `pendingDelete` 及其 claim 阻塞和 ACK 推进逻辑；v3 旧删除 receipt 机制保留但不再由 v4 自动触发。
- `RAIApp`：移除同步流程中的自动完整性证明；手动多 Workout 同步改为最多 3 条有界并发，每个 Workout 独立准备/上传/ACK，失败互不影响，同一 Workout 不会并发出两次。
- `ClientConfiguration.v4Enabled` 默认 true；显式 `syncProtocolV4=false` 保留 v3 回退。

## 4. 验证证据（本机 2026-11-09）

已执行并通过：

- `go test -count=1 ./internal/syncing/`：PASS。
- v4 定向 Go 单测：原始 envelope round-trip、实际 Swift JSON 形状、未知/重复字段、严格包身份、宽容跳过、category 流、route 八元组、metadata 保留与 VO₂ 保留键剥离、未知顶层 section 忽略、413 样本/route 上限、第三方 Workout 来源 422。
- 真实隔离 PostgreSQL 18.6：`scripts/local/test.sh integration -run '^TestWorkoutBundleV4ImportAndIdempotency$' -v ./integration` PASS（test 2.208 s，profile 12 s；receipt `committed`，samples/associations=1，同字节重试返回原回执；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`）。
- `scripts/local/test.sh gate` PASS（27 s；Go race/vet/依赖边界/controller 安全检查）。
- Swift：`swift test --filter WorkoutBundleV4Tests` 6/6 PASS；SyncCore 全量 `swift test` PASS。
- 模拟器 `V4SyncEngineTests` 6/6 PASS；带签名的模拟器 `RAITests` 94 passed / 0 failed / 4 skipped；Simulator 与 iPhoneOS 构建 PASS。

## 5. 未验证 / 未执行

- 未运行真机 HealthKit 读取与上传；没有真实 Watch 跑步数据验收。
- 未执行 v4 性能基准；不能声称吞吐或时延门槛已达成。
- 未部署本轮改动；云端仍运行旧版本，v4 新线格式尚未在云端生效。
- 未运行 `full` 全量集成回归；本轮只跑了受影响的 v4 集成用例与完成门禁。
- 真实 HealthKit 删除事件下的“服务端保留原记录”只有代码路径与单元/模拟器覆盖，未做真机验证。

## 6. 关键设计事实

- `workout_imports.payload` 是精确请求字节；未知字段只在原始载荷中，normalized 表只保存服务端理解的已知事实。
- Workout 窗口与四个身份 UUID、顶层 source 策略、Workout 核心是硬错误；可选 stream/sample/series/route 的坏数据只跳过。
- route 点客户端固定 8 元组，服务端容忍 3..8 个元素；`sequence` 由服务端生成。
- 顶层 `metadata` 与样本 `metadata` 的未知键原样保留；`RAI:vo2maxFactV1` 是服务端 provenance，服务端投影时移除，原始字节仍保留。
- 重复流/样本保留第一个，不覆盖、不报错；超限返回 413，不截断。
- 不做删除、不做回滚、不迁移旧 v4 数据；v4 上传不进入旧完整性端点。
