# Workout Sync v4 实施结果

日期：2026-11-08
计划：`docs/WORKOUT_SYNC_V4_SIMPLE_PLAN.md`
合同：`contracts/sync-v4.md`

## 1. 结论

v4 的单 Workout 列式 bundle 已实现并完成本机单元/集成验证：

- 一个 Workout 一个不可变 bundle，`protocolVersion=4`。
- 样本按 `streams[]` 列式编码，`series` 用 `[stream,sample]` 下标，route 点固定 8 元组。
- 服务端严格解码后转换为现有 `WorkoutBundle` 内存结构，复用既有单事务导入、receipt、analysis job 和幂等路径；数据库表与写路径未改。
- **D1 已接受**：v4 不发送/不生成/不校验 200 天完整性证明；每个 Workout 的 `observation.coverage` 即读取覆盖证明。旧完整性端点遇到 v4 回执返回 `409 completeness_not_supported_v4`。
- **D2 保留**：sample/route 显式删除由客户端 sidecar diff 生成，经 `deletions[]`/`routeDeletions[]` 提交；首次无 sidecar、失败读取和权限失败都不生成删除。
- v3 默认路径保持不变；v4 需要服务端 `SYNC_V4_ENABLED=1` 且客户端 `RAI_SYNC_V4=1`（或 `ClientConfiguration.syncProtocolV4`）并显式发送 `X-RAI-Sync-Protocol: 4`。

## 2. 服务端变更

- 新增 `apps/server/internal/syncing/workout_bundle_v4.go`：v4 envelope、严格 top-level/嵌套解码、定长数组、v4→v3 转换。
- `workout_bundle.go`：`Validate()` 接受 protocol 3/4；v4 routeDeletions 使用 256 上限。
- `bundle_decode.go`：`bundleValues` 接受 3/4。
- `bundle_observation.go`：v4 coverage key 白名单、空 provenance 数组、sampleCount 与流计数约束。
- `workout_delete.go`：删除请求接受 3/4。
- `httpapi/server.go`：`Options{SyncV4}`、`NewWithOptions`、按 header 的 handshake capability、按 protocolVersion 分派导入。
- `cmd/rai/main.go`：`SYNC_V4_ENABLED=1`。
- `storage/postgres/workout_bundles.go`：receipt tombstone 检查覆盖 v4 `streams[].uuids`。
- `storage/postgres/bundle_integrity.go`：analysis provenance 从 v4 `sources/streams/routes` 还原逐样本来源。
- `storage/postgres/workout_completeness.go`：v4 回执不进入旧完整性扫描，明确 409。

## 3. 客户端变更

- 新增 `ios/RAI/Sources/SyncCore/WorkoutBundleV4.swift`：v4 编码器、source 表、route 八元组。
- 新增 `ios/RAI/Sources/SyncCore/V4Outbox.swift`：按 scope 隔离的 sample/route identity sidecar；上传前以 `v4-pending-<bundleSHA256>` 暂存，回执持久化后才提升，避免 ACK 后崩溃丢 sidecar。
- 新增 `ios/RAI/App/V4SyncEngine.swift`：HealthKit 样本/ series / route 读取、覆盖记录、D2 sidecar diff、v4 组装。
- `SyncCoordinator`：协商 v4、调用引擎、保存 multipart、复用 `WorkoutBundleDelivery` 上传/ACK；只有 ACK 成功后才推进 sidecar。
- `ClientConfiguration`：`syncProtocolV4`/`RAI_SYNC_V4` 开关。
- `SyncSession.validateHandshake` 允许 `bundleProtocolVersion` 3/4。
- `RAIAPIClient`：支持自定义请求 header（v4 handshake）。

## 4. 验证证据

已执行：

- `go test ./...`：全部 Go 包 PASS。
- `go test ./internal/syncing ./internal/httpapi`：v4 解码/校验/HTTP 分派 PASS。
- 真实隔离 PostgreSQL：`scripts/local/test.sh integration -run TestWorkoutBundleV4ImportAndIdempotency ./integration` PASS；v4 导入 receipt committed，samples/associations=1，相同字节重试幂等。
- `swift test`：SyncCore 全量 250 tests / 35 suites PASS；`WorkoutBundleV4Tests` 5 tests PASS。
- `xcodebuild ... test -only-testing:RAITests/V4SyncEngineTests`：2 tests PASS（两次 build 删除、路线失败 partial）。
- `swift build --target SyncCore` 与 Simulator `xcodebuild build CODE_SIGNING_ALLOWED=NO`：BUILD SUCCEEDED。
- `go test -race ./...`：全部 Go 包 PASS（exit 0）。
- `go vet ./...`：PASS；`scripts/check-boundaries.py`：全部边界规则 PASS。

## 5. 未验证 / 未执行

- 未运行真机 HealthKit 读取与上传；没有真实 Watch 数据样本。
- 未执行 S60/S60H/200 Workout 的性能基准（P1–P8），不能声称性能门槛已达成。
- 未运行完整 Simulator UI/E2E 套件；只运行了 v4 定向单元测试和 App 构建。
- 旧 v3 completeness/discovery 代码尚未删除：v4 路径已不依赖，旧端点对 v4 明确拒绝；S6 的净删除留待 v3 排空后执行，不在本轮伪装完成。
- v4 ACK 复用共享 v3 receipt 校验（包含 counts）；合同只把 5 个字段列为语义必需，服务端仍返回完整 receipt。

## 6. 关键设计事实

- v4 的 `observation.provenance.samples/routes` 必须为空数组；逐样本来源由 `streams[].source/src[]` 和 `routes[].source` 表达。
- complete/no_records 的 coverage 计数必须与实际流/route 计数一致；partial 计数不得超过实际值。
- source 表最多 16 条，去掉 `device`；workout 的完整来源保留在 observation provenance。
- route 点后 6 项可为 `null`；服务端生成连续 `sequence`。
- 普通快照缺席不删除；只有显式 `deletions[]`/`routeDeletions[]` 才删除。
