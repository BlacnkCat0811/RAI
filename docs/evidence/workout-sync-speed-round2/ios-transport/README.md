# R4 — 客户端 WorkoutBundle 传输层改为 gzip-only（证据）

执行者：ios-transport（R4 工作流）。仅合成数据；未 commit / push / 部署；未读生产数据；未记录凭证。
本文件只记录真实执行过的命令与输出；未执行项明确标 NOT RUN。

## 1. 改动文件（全部在授权范围内）

| 文件 | 改动 |
| --- | --- |
| `ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift` | 删除「压缩后 >8MiB 回退明文 `uploadFile = file`」；所有 bundle POST 一律 gzip；新增 `bundle_gzip_unsupported` 能力判定、gzip 派生/复用/封存、`gzipWire`/`isGzipMultipart`、计数器 |
| `ios/RAI/Sources/SyncCore/RAIAPIClient.swift` | 新增 `BundleGzipCapability`、`HandshakeCapabilities`（`Mutex` 盒，跨值类型拷贝与 401 重建存活）、`bundleGzipCapability`；`performCall` 记录 `GET /v2/handshake` 的 `capabilities`；`metrics` 改为 internal 供传输层计数 |
| `ios/RAI/Sources/SyncCore/Wire.swift` | 新增 `Wire.GzipBundle`：32MiB 解压上限、8MiB 压缩上限、100:1 + 64KiB 比例上限，与服务端常量一致 |
| `ios/RAI/Tests/SyncCoreTests/GzipTransportTests.swift` | **新增**，18 个测试覆盖 R2-F16..R2-F20 |
| `ios/RAI/Tests/SyncCoreTests/APITests.swift` | 新增握手能力记录测试、`Content-Encoding` 仅限 batch lane 测试 |
| `ios/RAI/Tests/SyncCoreTests/WireEncodingTests.swift` | 新增 gzip wire 单成员/确定性/ISIZE 与限额镜像测试 |

未改动（明确越界禁区）：`SyncCoordinator.swift`、`HealthKitClient.swift`、`OutboxStore.swift`、`CaptureSegments.swift`、`CaptureManifest.swift`、`HealthDiscovery.swift`、`WorkoutSync.swift`、其他 SyncCoreTests/AppTests 文件、`ios/RAI/Package.swift`、`apps/server/**`、`contracts/**`、`scripts/**`。
（工作区里这些文件的 `M`/`??` 状态属于队友 ios-capture / go-server / lead，本轮 R4 未触碰。）

## 2. 命令与结果

### 2.1 Swift 基线（改动前）

```
scripts/local/test.sh swift --scratch-path .local/swift-build/transport
→ ✔ Test run with 198 tests in 28 suites passed after 7.370 seconds
```

### 2.2 Swift 全量（改动后）

```
cd /Users/ta/RAI
export DEVELOPER_DIR="$HOME/Downloads/Xcode.app/Contents/Developer"
scripts/local/test.sh swift --scratch-path .local/swift-build/transport
→ ✔ Test run with 218 tests in 29 suites passed after 4.921 seconds
→ LOCAL CHECKS: PASS profile=swift elapsed_seconds=7

# 全部测试定稿后的最终一次全量：
→ ✔ Test run with 220 tests in 29 suites passed after 5.043 seconds
→ LOCAL CHECKS: PASS profile=swift elapsed_seconds=7
```

（220 = 基线 198 + GzipTransportSuite 18 + APITests 2 + WireEncodingTests 2。0 failed / 0 skipped。）

### 2.3 定向套件

```
xcrun swift test --package-path ios/RAI --scratch-path .local/swift-build/transport --filter GzipTransportSuite
→ ✔ Test run with 18 tests in 1 suite passed after 1.169 seconds

xcrun swift test --package-path ios/RAI --scratch-path .local/swift-build/transport \
  --filter "WireEncodingTests|APITests|GzipTransportSuite"
→ ✔ Test run with 37 tests in 3 suites passed after 6.170 seconds
```

> 构建隔离：`RAIAPIClient.init` 增加了带默认值的尾参数，mangled symbol 变化，旧增量 scratch 会因 `ProductCoreTests.o` 引用旧 init 而 `Undefined symbols`；需一次性 `rm -rf .local/swift-build/transport` 后重建（全新 scratch 不受影响）。已转告 lead 通知 ios-capture。

### 2.4 服务端 gzip 拒绝语义回归（服务器未改动，只验证）

```
cd apps/server
GOTOOLCHAIN=local GOPATH=<repo>/.local/gopath GOCACHE=<repo>/.local/gocache \
  PATH="<repo>/.local/toolchain/go/bin:$PATH" \
  go test ./internal/httpapi -run 'TestBundleGzip|TestBundleCompressed|TestBundleWireLimits' -v
→ --- PASS: TestBundleWireLimitsAndStrictMultipart (0.05s)   [9 subtests，含 /plain]
→ --- PASS: TestBundleGzipBoundaries (0.48s)
      valid / truncated / crc / garbage / concatenated / bomb / decoded-limit-before-allocation 全 PASS
→ --- PASS: TestBundleCompressedFileIndependentLimit (0.03s)
→ ok  rai/server/internal/httpapi  0.592s
```

### 2.5 真实 socket + 真实 PostgreSQL 18 集成（Swift 客户端 → HTTP → PG）

```
scripts/local/test.sh integration -tags=integration,swiftclient \
  -run '^TestWorkoutReliabilitySwiftClientPostgres$' ./integration
```

结果见 §6。该 profile 由 R4 独占；与队友 go-server 的 syncperf PG 任务**不同时运行**（串行排队，见 §6）。

## 3. 三个关键计数：可观测性与测点

三个计数都是 `SyncMetrics` 真实事件，由 `WorkoutBundleDelivery.deliver` 发出，测试用 `MetricsRecorder`（`SyncMetrics` sink）断言实际收到的值，不是"设计上应该有"。

| 计数器 | 语义 | 发出位置 | 断言所在测试 | 实测值 |
| --- | --- | --- | --- | --- |
| `bundle_gzip_post_attempt` | 每次真正发出的 gzip bundle POST | POST 前 | `freshUploadSendsOneGzipFilePart`、`compressedOverEightMiB…`、`compressionRatioCap…`、`handshakeWithoutGzipCapability…` | 成功路径 `[1]`；失败/超限/不兼容路径 `[]`（0 次发送） |
| `bundle_gzip_post_success` | 通过 `WorkoutBundleACK.validate` 的成功 POST | ACK 校验通过后 | `freshUploadSendsOneGzipFilePart`、`retryReusesTheWireArtifactWithoutRecompressing` | `[1]` |
| `bundle_plain_fallback` | 明文回退次数（**必须恒为 0**） | 每次尝试开头（含失败尝试） | 全部 18 个测试 | `[0]` / `[0, 0]`，无一次非零 |
| `bundle_healthkit_reread` | 已备原包重试时重读 HealthKit 次数（**必须恒为 0**） | 每次尝试开头（含失败尝试） | `oldPlainPendingTaskResumesAsGzipWithoutTouchingTheOriginal`、重试/超限/不兼容测试 | `[0]` / `[0, 0]` |
| `bundle_gzip_derive_ms` | 单次 gzip 派生耗时 | 派生后 | `retryReusesTheWireArtifactWithoutRecompressing`（复用时不新增） | 仅首次派生出现 |
| `bundle_gzip_wire_bytes` | gzip wire 字节数 | 派生后 | 同上 | 记录 |

**gzip 成功 POST 比例 = 100%**：`expectGzipPOST` 对每一次真实 POST 断言：method/path、`Content-Type: multipart/form-data; boundary=…`、顶层**无** `Content-Encoding`、file part 头为 `name="file"` + `filename="workout.json.gz"` + `Content-Type: application/gzip`、且不含 `filename="workout.json"`/`application/json`、payload 前两字节 `1f 8b`、payload 与 `Wire.gzip(raw)` 逐字节相同（`Wire.gzip` 确定：mtime 0 / OS 255）。四类请求（首传、重试、旧 plain pending 恢复、显式删除子包）都走同一断言。

**明文 fallback 计数 = 0**：代码里已无 `uploadFile = file` 这条路径；`isGzipMultipart` 使任何非 gzip 的缓存 wire 都不可复用，只能重新派生或失败。

## 4. 失败场景的实际错误输出

```
EVIDENCE_OVER_LIMIT code=bundle_compressed_too_large status=422 retryable=false
EVIDENCE_RATIO      code=bundle_compression_ratio_exceeded status=422 retryable=false
EVIDENCE_UNSUPPORTED code=bundle_gzip_unsupported status=422 retryable=false
```

（上述为这三个测试运行时打印的真实 `UploadError` 值；打印语句已在取证后移除，测试文件中无遗留调试输出。）

对应断言：

- `compressedOverEightMiBKeepsTheBundleAndSendsNothing`：9MiB 不可压缩合成字节 → 压缩后 >8MiB → 0 次网络请求、原包与 hash 保留、`phase == .failed`、`errorCode == bundle_compressed_too_large`、`uploadReceipt == nil`、无 `.multipart.gz` 残留、`bundle_plain_fallback == [0]`、`bundle_healthkit_reread == [0]`；+3600s 后再次 `deliver` 返回 `false`（无无限热重试）。
- `compressionRatioCapRejectsWithoutPlaintextFallback`：4MiB 单字节 → 比例超 100:1+64KiB → `bundle_compression_ratio_exceeded`，0 次发送，`uploadReceipt == nil`。
- `handshakeWithoutGzipCapabilityFailsDiagnosablyAndKeepsTheTask`：握手 `capabilities` 缺少 `bundleGzipPart` → `bundle_gzip_unsupported`，线上只有握手请求，任务保留且 `bundleFile`/`bundleSHA256` 不变，`uploadReceipt == nil`。
- `compressionFailureMapsToADiagnosableNonRetryableError`：`gzip_failed`（C shim 压缩失败）、`gzip_wire_invalid`、`bundle_too_large`、`bundle_compressed_too_large`、`bundle_compression_ratio_exceeded`、`bundle_gzip_unsupported` 六个错误码都映射为 `status 422` + `retryable == false`，即"明确失败、不热重试、不降级"。

## 5. 「服务器未声明 gzip 能力」的落点与依据

依据（均为现有服务端代码，本轮未改动）：

- `apps/server/internal/httpapi/server.go:313` 握手 `capabilities` 已声明 `"bundleGzipPart": true`（同段还有 `workoutBundles: true`、`bundleProtocolVersion: 3`），并声明 `limits.bundleBytes/bundleWireBytes/bundleCompressedBytes`。也就是说**能力协商通道本来就存在，只是客户端此前从未读取**。
- `apps/server/internal/syncing/protocol.go` 只有 batch/page 的 `ProtocolVersion = 2` 与 `Envelope.Validate`（要求 `ContentEncoding == "gzip"`），**没有** bundle 传输的能力协商/降级逻辑；bundle 线的约束全部在 `bundle_body.go`：`MaxBundleCompressedBytes = 8 << 20`（第 20 行）、`MaxBundleFramingBytes = 64 << 10`（第 18 行）、顶层 `Content-Encoding` → 415 `unsupported_content_encoding`（第 77 行）、`gz.Multistream(false)`（第 115 行，单成员）、比例 413 `compression_ratio_exceeded`（第 134 行）、422 `invalid_gzip`（第 156/216 行，拒绝截断/串联成员/尾部垃圾）。

因此本轮的处理是：客户端读取 `GET /v2/handshake` 的 `capabilities.bundleGzipPart`——

| 观测 | 客户端行为 |
| --- | --- |
| `.declared(true)` | 正常发 gzip（唯一路径） |
| `.declared(false)`（握手跑过但没声明） | 抛出可诊断的 `SyncFailure.localState("bundle_gzip_unsupported")` → `UploadError(code: "bundle_gzip_unsupported", status: 422, retryable: false)`；任务保留（`bundleFile`/`bundleSHA256`/boundary 不变），`phase == .failed` + `errorCode` 可诊断；0 次 bundle POST、0 明文 |
| `.unknown`（本进程未观测到握手） | 仍然发 gzip —— 明文从来不是选项，也不因"未知"而降级 |

未新增端点、未改协议版本、未改服务器。

## 6. 真实 socket + PG 集成执行记录（R2-F16 / R2-F17 / R2-F20 的 "真实 socket / 真实 PG" 证据）

命令（R4 独占 profile）：

```
scripts/local/test.sh integration -tags=integration,swiftclient \
  -run '^TestWorkoutReliabilitySwiftClientPostgres$' -v ./integration
```

串行说明：启动前用 `kill -0 75259` / `pgrep -f TestWorkoutSyncPerfServerHTTP` 轮询等待队友的 syncperf PG 任务退出（clear at 2026-11-06T10:44:35Z），**该次 PG 集成测试与 syncperf 不同时运行**；其间 go-server 的 `TestWorkoutWriteCandidates` 也结束之后才启动 verbose 这一轮。全程只用 `test-postgres.sh` 的一次性 `/tmp/rai-pg.*` 集群，未占用共享实例。

结果：**PASS**，18/18 场景，全部由**真实 Swift 客户端（本轮已改为 gzip-only）→ 真实 loopback HTTP → 真实 PG18** 跑通：

```
--- PASS: TestWorkoutReliabilitySwiftClientPostgres (118.93s)
    --- PASS: .../before_send (52.43s)            --- PASS: .../status_500 (3.27s)
    --- PASS: .../during_send (7.70s)             --- PASS: .../status_502 (4.11s)
    --- PASS: .../before_commit_timeout (3.04s)   --- PASS: .../status_503 (3.48s)
    --- PASS: .../lost_ack (3.42s)                --- PASS: .../auth_refresh (3.17s)
    --- PASS: .../corrupt_ack (3.71s)             --- PASS: .../two_workers (4.35s)
    --- PASS: .../commit_404 (3.90s)              --- PASS: .../duplicate_post (4.05s)
    --- PASS: .../validation_missing (3.22s)      --- PASS: .../phase3_observation (3.45s)
    --- PASS: .../tombstone (3.33s)               --- PASS: .../phase3_child_delete (4.37s)
    --- PASS: .../rate_limit (2.98s)              --- PASS: .../phase3_deleted_parent (4.32s)
PASS
ok  	rai/server/integration	119.990s
RAI_TEST_LEFTOVER_DATABASES=0
RAI_TEST_CLUSTER_REMOVED=1
LOCAL CHECKS: PASS profile=integration elapsed_seconds=130
```

每个场景的客户端侧断言（`ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift`，本轮未改动该文件）通过，说明：
- 真实 PG 里只落一条 import（`workout_imports/health_samples/jobs` 各 1），即服务器对 gzip 解压后的原始 JSON 计算出与客户端相同的 `bundleSHA256` → **R2-F16「服务器验证解压 hash」成立**；
- `lost_ack` / `corrupt_ack` / `commit_404` / `rate_limit` / `status_5xx` / `two_workers` 等重试与恢复场景里，重试仍复用同一原包（`XCTAssertEqual(try Data(contentsOf: file), body)`：原 plain multipart 文件逐字节未变）→ **R2-F17「原 JSON 不变、无 HealthKit 读取」成立**；
- `duplicate_post` 场景由 Go 侧重新 POST **原始明文 multipart**，仍返回同一条回执 → **R2-F20「旧 plain 客户端兼容有效 / 同 JSON 返回原回执」在真实 PG 上成立**；
- `phase3_child_delete` / `phase3_deleted_parent` 通过 → 显式删除子包同样以 gzip 走通并受删除屏障约束。

（另一轮不带 `-v` 的同命令也 PASS：`ok rai/server/integration 130.094s`，`RAI_TEST_LEFTOVER_DATABASES=0`，`RAI_TEST_CLUSTER_REMOVED=1`。）

## 7. 自检

- `grep -nE "print\(|debugPrint|TODO|FIXME|XXX"` 于 6 个改动文件 → 全部 clean（取证用的临时打印已移除）。
- `WorkoutBundleDelivery.swift` 只 `import Foundation`，全文无 `HealthKitClient` 引用（唯一 "HealthKit" 出现在注释里）→ "已备原包重试 0 次 HK 重读" 不只是计数自证，而是该层结构上无法访问 HealthKit。
- `git status --short` 中属于 R4 的只有：`RAIAPIClient.swift`、`Wire.swift`、`WorkoutBundleDelivery.swift`、`APITests.swift`、`WireEncodingTests.swift`（M）、`GzipTransportTests.swift`（??）、`docs/evidence/workout-sync-speed-round2/ios-transport/`（??）。
- 未执行任何 `git reset/clean/stash/checkout`；未触碰 `/Users/ta/running`；未读生产数据；测试数据全部为合成字节（`{"synthetic":true}`、xorshift64 随机块、0x41 重复块）。

## 8. 未完成 / 受限项

- **真机上传验收**：NOT RUN — 本会话无真机授权，按约定由 lead 记 NOT RUN。
- **gzip 派生文件"不可变"的实现方式**：靠内容寻址校验（复用时必须 `Wire.hash(wire) == bundleWireSHA256` 且仍是 gzip file part；重新派生写入新文件、绝不覆盖旧文件），**不是**文件系统只读位——`savePayload` 属 `OutboxStore`（本轮越界文件），且既有测试 `deterministicGzipArtifactSurvivesRestartCorruptionAndACK` 需要改写该文件以验证重建，故不设 0o400。标 PARTIAL（语义封存成立，物理只读位未加）。
