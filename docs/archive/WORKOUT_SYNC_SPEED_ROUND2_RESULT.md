# Workout 同步提速第二轮 — 执行结果

本文件记录第二轮（R0–R6）的**实际改动、实测结果、故障历史、性能前后对照、残余风险、回退与未验证项**。
依据：`docs/WORKOUT_SYNC_SPEED_ROUND2_PLAN.md`（下称"计划"）与 `docs/WORKOUT_SYNC_SPEED_ROUND2_CODEX_PROMPT.md`。
第一轮结果见 `docs/WORKOUT_SYNC_SPEED_RESULT.md`（**不修改、不删除其 FAIL**）。证据目录：`docs/evidence/workout-sync-speed-round2/`。

执行环境（`git status` 见 §2.1）：分支 `main`，HEAD `0303236`，工作区在本轮开始前已有 `M docs/IMPLEMENTATION.md` 与两份未跟踪的 ROUND2 文档，全部保留；本轮**未 commit、未 push、未部署、未迁移任何真实数据**。

---

## 1. 结论摘要

| 维度 | 结论 |
| --- | --- |
| 逐样本 NSKeyedArchiver | **已消除**：正常路径 `sample_archive_calls=0`、`sample_unarchive_calls=0`（旧路径对照 14,000 次）；R2-F01 PASS |
| 捕获/ACK 确认边界 | **发现并修复两个真实数据丢失缺陷**（迟到 sample、子删除被错标 delivered）；R2-F07 PASS |
| HealthKit 有界并发 | **已实现并实测 1/2/4**：peak active 1/2/4、输出与完成顺序无关、取消后 active=0；R2-F10..F15 PASS，R2-P02 PARTIAL |
| 客户端 gzip-only | **已强制**：所有 bundle POST 都是单 gzip file part，明文 fallback 计数恒为 0，旧 plain pending 不改 identity；R2-F16..F20 PASS |
| 服务端集合写入 | 三候选实测后采用 unnest 数组；COPY 被受限角色 TEMP 权限阻断（留证据）；R2-F21..F27 PASS |
| 严格 JSON 解码 | 普通构建 103.8/263.1/409.0 ms（S60/S60H/S240）；第一轮的 1,649.6 ms 是 race 构建放大 ~14× |
| **R2-P08/P09（服务端 P95 门槛）** | **FAIL**：S60 P95 4,758.8 ms（目标 ≤1,000）、S240 P95 19,722.3 ms（目标 ≤4,000）。裸 PG 天花板 585 ms/2,442 ms 证明目标在本机+durability 下不可达 |
| **R2-P01（规范化+持久化降幅）** | **FAIL**：实测下降 27.09%（1,530.05→1,115.62 ms），该段结构性上限 ~35% |
| 完成门禁 | **PASS**：`scripts/local/test.sh full` 1338 s，`go test -race ./...` 全绿、`integration 1099.261s`、sqlc/dbgen 无 diff、残留库 0（§8.1） |
| 真机（R2-P02..P07、P12/P13/P14） | **已执行（2026-11-06，USB 真机）**：P03/P04/P12/P14(10×) 未达标；P05 NOT RUN；P06/P07 PASS；P13 PARTIAL；详见 §8.3 |

**一句话**：本轮把"逐样本归档"这条确定性的错误路径删掉了、修掉了两个会丢数据/让删除复活的确认边界缺陷、把上传契约收紧到 gzip-only、并给服务端建立了一个可信的普通构建测速入口；但**服务端 P95、规范化降幅与真机时延/RSS 多条性能门槛未达标**，因此本轮**不能**签"性能达标"或"真机验收通过"；真机只证明了正确性路径可用，不证明时延目标达成。

---

## 2. 入口、环境与测量口径

### 2.1 工作区状态

```
branch main, HEAD c864038 (perf(workout-sync): round2 write path, capture pipeline and perf evidence)
真机验收新增未提交改动: ios/RAI/App/RAIApp.swift, scripts/local/device.py, scripts/local/device_sample.py
本轮文档: docs/WORKOUT_SYNC_SPEED_ROUND2_RESULT.md, docs/IMPLEMENTATION.md, docs/evidence/workout-sync-speed-round2/device/round2-device.md
```

上述既有改动与用户文件全部保留；未使用 `git reset` / `clean` / `stash` / 整区 `checkout`。

### 2.2 本机环境

| 项 | 值 |
| --- | --- |
| 机器 | macOS 26.7.2，**AMD Ryzen 5 3600X 6-Core**（`num_cpu=12`） |
| Go | go1.27.3 darwin/amd64（`.local/toolchain/go/bin`） |
| PostgreSQL | 18.6（`fsync=on` `full_page_writes=on` `synchronous_commit=on` `wal_level=replica` `shared_buffers=64MB` `max_wal_size=1GB` `data_checksums=on`） |
| Swift / Xcode | Swift 6.3.3；Xcode 在 `/Applications/Xcode.app`（`DEVELOPER_DIR`） |
| 隔离 PG | `scripts/test-postgres.sh` 每次 `mktemp -d /tmp/rai-pg.XXXXXX` 独立 cluster + unix socket，结束 `RAI_TEST_LEFTOVER_DATABASES=0` / `RAI_TEST_CLUSTER_REMOVED=1` |

### 2.3 三种测量口径必须分开（不得混用）

| 口径 | 入口 | `-race` | 传输 | 计时点 |
| --- | --- | --- | --- | --- |
| 正确性门禁 | `scripts/local/test.sh integration` | **是** | 进程内 `httptest` | handler 内部分段 |
| 第一轮性能矩阵 | `RAI_SYNC_SPEED_PERF=1 … test.sh integration` | **是** | 进程内 `httptest`，无 socket | handler 内部分段 |
| **本轮 R0 性能** | **`scripts/local/test.sh syncperf`（新增）** | **否**（二进制内断言无 `-race`/`-N`/`-l`） | **真实 loopback TCP** → 认证/准入中间件 → 真实 handler → 真实 PG18 | 完整 HTTP 往返 + handler 分段 |

`syncperf` 会把环境里继承的 `-race`、裸 `-N`/`-l` 逐 token 过滤掉，再用 `go version -m` 断言产物确实没有这些设置，打印 `SYNCPERF_GOFLAGS_IN` / `SYNCPERF_GOFLAGS_EFFECTIVE` / 构建设置。`quick|gate|unit|integration|full|perf|swift|simulator|e2e` 语义**未改**，`integration` 仍默认 `-race`。

第一轮的 `json_decode` P50 1,649.6 ms 属于 race 口径；普通构建同一解码是 **103.8 ms（S60）**，差约 14×。**本轮所有服务端数字均来自普通构建口径**。

### 2.4 固定 fixture

| 档 | 内容 | fixture bytes |
| --- | --- | --- |
| S60 | 6,594 samples + 关联 + 1 route | 4,134,363 |
| S60H（密集档） | 10,824 samples + 10,824 series points + 3,491 route points + 全 source/metadata | 10,118,893 |
| S240 | 26,364 samples + series + routes | 16,537,010 |

固定 seed 可复现；**每次迭代重播种＝真首导入**（新 UUID/新身份），harness 会拒绝 `samplesInserted` 不等于全量的迭代，因此不存在重放或缓存命中冒充计时导入。失败迭代**保留不剔除**。

---

## 3. 实际改动

### 3.1 iOS 捕获侧（R1/R2/R3）

新增：
- `Sources/SyncCore/CaptureManifest.swift` — 不可变、带版本号的确认清单（每类型 cursor、`coveredEventIDs`、`deletionEventIDs`、generation、`covers(_:)`），单写者发布。
- `Sources/SyncCore/NormalizedWorkoutCapture.swift` — 版本化规范化捕获（`NormalizedFact`/`NormalizedSeries`/`SampleArchiveMeter`），segment format v2（`encoding:"normalized"`），format 1..2 均可读。
- `Sources/SyncCore/CaptureConcurrency.swift` — `CaptureConcurrencyPolicy`（2 流 / 2 MiB）、`CaptureGate`（槽 + 字节预算 + 取消 + active/peak 统计）、`CaptureWriter`（唯一写者）。

修改：`App/SyncCoordinator.swift`（规范化捕获、manifest 构造、任务组并发采集、固定顺序合并、新增 timings `capture_max_active_streams`/`capture_stream_limit`/`capture_peak_queue_bytes`/`capture_writer_batches`/`capture_writer_records`）、`App/HealthKitClient.swift`、`Sources/SyncCore/{CaptureSegments,HealthDiscovery,OutboxStore,WorkoutSync}.swift`、`RAI.xcodeproj/project.pbxproj`。

新增测试：`Tests/SyncCoreTests/{NormalizedCaptureSegmentTests,CaptureGateTests}.swift`、`AppTests/{CaptureAckBoundaryTests,NormalizedCaptureTests,NormalizedCapturePerformanceTests,CaptureConcurrencyTests}.swift`。

### 3.2 iOS 传输侧（R4）

- `Sources/SyncCore/WorkoutBundleDelivery.swift` — **删除**"压缩后 >8 MiB → 明文 `uploadFile = file`"回退；所有 bundle POST 一律 gzip；能力判定、gzip 派生/复用/封存、`gzipWire`/`isGzipMultipart`、计数器。
- `Sources/SyncCore/RAIAPIClient.swift` — `BundleGzipCapability` + `HandshakeCapabilities`（Mutex 盒，跨值拷贝/401 重建存活），记录 `GET /v2/handshake` 的 `capabilities`。
- `Sources/SyncCore/Wire.swift` — `Wire.GzipBundle`（32 MiB / 8 MiB / 100:1+64 KiB，与服务端常量一致）。
- 新增测试 `Tests/SyncCoreTests/GzipTransportTests.swift`（18 项）；`APITests.swift` +2、`WireEncodingTests.swift` +2。

### 3.3 Go 服务端（R0/R5）

新增：`storage/postgres/bundle_writes.go`（三种写入候选 + `ExplainFactWrites`，默认切 arrays）、`integration/workout_sync_perf_fixture_test.go`（S20/S60/S60H/S120/S240）、`integration/workout_sync_perf{,_race,_norace}_test.go`、`integration/workout_sync_r5_test.go`、`integration/workout_strict_json_test.go`、`integration/workout_write_candidates_test.go`、`integration/workout_identity_scaling_test.go`、`integration/workout_bare_pg_test.go`、`integration/workout_timeout_probe_test.go`。

修改：`syncing/bundle_decode.go`（反射 schema 缓存 + 每层复用 seen map + `strconv` 路径）、`storage/postgres/bundle_batching.go`（基线语句抽到 `bundle_writes.go`，导出 `SampleIdentityStatement`）、`httpapi/server.go`（`ProcessUploadSlots` 常量，**值仍为 2**）、`scripts/local/test.sh`（**只加 `syncperf`**）。

### 3.4 明确未改动

`ON CONFLICT DO NOTHING` 未引入；连接池（8）、上传槽（2）、dataset 槽（2）、所有限流/准入预算、`fsync`/`full_page_writes`/`synchronous_commit`、超时值均未放宽；`integration` 的 `-race` 语义未动；`apps/server` 未为性能改 schema 或索引。

---

## 4. 阶段结果

### 4.1 R1 捕获/ACK 确认边界（门禁 **R2-F07**）— PASS

**先证明缺陷、再修**（pre-fix 复证用当前测试 + `manifest: nil` 重跑，不是引用旧记录）：

| 场景 | 修复前实测 | 后果 |
| --- | --- | --- |
| ① 捕获窗口内迟到的 sample | `ACK confirmed a sample the bundle never carried`；水位 `3` 不 `> 3`；第二遍 `uploads == 0` | 该样本被 `isAlreadyDelivered` 跳过 → **永久丢失** |
| ② 捕获窗口内的子删除 | `ACK confirmed a deletion the bundle never carried`；第二遍 `uploads == 0` | 服务端**样本复活** |
| ③ 捕获窗口内的父删除 | pre-fix 已正确（`claimBundle` 的 `pendingDelete != true` 守卫） | 仅纳入回归 |

修法：不可变 `CaptureManifest` 记录本次组包**真实消费**的身份（`coveredEventIDs` + `deletionEventIDs`，cursor 仅作证据），`acknowledgeBundle` 按 manifest 逐事件判定，未覆盖的**压低水位**并清 `work.error`；`isAlreadyDelivered` 增加第二重守卫。

**关键实测发现**：`ordinal <= cursor` **不能**当覆盖判据——ordinal 是全局提交序，同类型事实可能先提交却不在包内。已改为只认身份。

测试：`CaptureAckBoundaryTests.{testLateSampleDuringCaptureStaysPendingAfterAck, testChildDeletionDuringCaptureStaysPendingAfterAck, testParentDeletionDuringCaptureRefusesTheAck}` 3/3 PASS（模拟器 lane，真实 `performSync` + `uploadAndAcknowledge`，`CheckedContinuation` barrier 无 sleep）。

### 4.2 R2 规范化捕获（R2-F01/F02/F03 + R2-P01）

| 项 | 结论 | 依据 |
| --- | --- | --- |
| R2-F01 正常路径归档调用 | **PASS** | `sample_archive_calls=0`、`sample_unarchive_calls=0`（timings + `SampleArchiveMeter` 双口径）；旧路径对照 14,000 次 |
| R2-F02 新旧同源 | **PASS** | bundle samples 与 `Normalizer.integrityEvent` **逐字节相等**；identityHash 与旧路径一致；canonical bytes 稳定 |
| R2-F03 旧 archive/旧 segment/新格式混合 | **PASS** | 旧 archive 段、旧 seal、旧 pending 均可读且未改 hash/回执；新增重启级用例 `mixedLegacyArchiveAndSegmentFactsRecoverAfterRestart`（`CaptureSegmentsTests`）PASS |
| R2-P01 规范化+持久化阶段降幅 | **FAIL（27.09%）** | 优化构建（`-O`，`DEBUG` 仅作测试钩子），N=2,000 合成 heartRate，7 次 P50：legacy **1,530.05 ms** → normalized **1,115.62 ms** |

**R2-P01 差距归因（这是本条的实质结论，不是借口）**：逐样本组件实测 archive 0.266 ms、fingerprint 0.221 ms、normalized_fact 0.221 ms、encode 0.024 ms。即使归档成本**完全归零**，该段上限也只有约 **35%**，70% 对这份 fixture 结构性不可达。而第一轮**真机**口径下 archiver 占该阶段 12.78 s 中的 8.8 s（69%）——两者成本结构不同；真机端到端已执行（P03/P04），但 P01 的规范化子阶段未单独剖分，**不做跨口径换算**。因此本条按计划要求记 FAIL，不移动口径、不改门禁。

### 4.3 R3 有界并发（R2-F10..F15 + R2-P02）

实现：独立 quantity type 之间用 `withThrowingTaskGroup` 并发；一条流 = 一个生产者，流内读取/anchor 判定/cursor 固定严格顺序；`CaptureGate` 用槽（默认 2 流）与字节预算（2 MiB）包住整条流（sample + series）与 route 流；只有 `CaptureWriter`（actor）写 Outbox；取消时 `catch { await gate.exit(); throw }` 且 `cancel()` 唤醒所有 waiter；子任务结果按 `HealthKitClient.quantityTypes` 固定顺序合并。Swift 6 严格并发未关闭，无全局 `@unchecked Sendable`。

实测（同一 fixture，13 条测试全部真跑）：

```
R3 measured capture concurrency (streams -> peak active): [1: 1, 2: 2, 4: 4]
R3 measured peak queue bytes (streams -> bytes):          [1: 1572, 2: 1572, 4: 2370]
R3 measured wall clock ms (streams -> ms):                [1: 87.69, 2: 58.04, 4: 63.57]
R3 measured paging anchors per type: heartRate ["-", "A1"]，其余 10 类各 ["-"]
```

| 项 | 结论 | 依据 |
| --- | --- | --- |
| R2-F10 并发 1/2/4 结果一致 | **PASS** | `testMergeOrderIsIndependentOfCompletionOrder`：1 流顺序 vs 4 流乱序，samples/series/routes 载荷**逐字节相同** |
| R2-F11 实际并发与全局限额 | **PASS** | peak active 1/2/4 实测；`CaptureGateTests.streamSlotsAreBoundedAndHandedOver` |
| R2-F12 同类型多页 anchor | **PASS** | anchor `["-","A1"]`，同类型 peak active == 1，不跳页不串 cursor |
| R2-F13 单类型失败 | **PASS** | `testFailingStreamReleasesItsSlotForTheOthers` |
| R2-F14 取消/槽泄漏 | **PASS** | `testCancellationStopsAParkedCaptureStream`：取消后 active=0，同 store 新协调器仍可满预算同步 |
| R2-F15 背压/大批次 | **PASS** | `CaptureGateTests.byteBudgetBackpressuresTheProducer`、`oversizedReservationIsClampedToTheBudget`、peak 队列字节实测 ≤ 2 MiB |
| R2-P02 并发 1/2/4 | **PARTIAL** | 真机 58 分钟密集记录：streams=1 P50 **17,638.31 ms**（n=10）、streams=2 **18,274.23 ms**（n=20）、streams=4 **20,194.13 ms**（n=10）；2 比 1 慢 3.6%、4 比 1 慢 14.5%，**无 ≥20% 收益**，且顺序测量受热漂移混杂；机制/一致性 PASS，收益未成立 |

### 4.4 R4 客户端 gzip-only（R2-F16..F20 + R2-P11）

| 项 | 结论 | 依据 |
| --- | --- | --- |
| R2-F16 全新上传与全部重试 | **PASS** | `freshUploadSendsOneGzipFilePart`、`retryAfterTransportFailureSendsTheSameSealedGzip`、`retryReusesTheWireArtifactWithoutRecompressing`、`plaintextWireSlotIsNeverUploaded`、`deletionOnlyChildBundleIsAlsoSentAsGzip`；断言 body 前两字节 `1f 8b`、`filename="workout.json.gz"`、`Content-Type: application/gzip`、顶层无 `Content-Encoding`、payload 与 `Wire.gzip(raw)` 逐字节相同 |
| R2-F17 旧 plain pending 恢复 | **PASS** | `oldPlainPendingTaskResumesAsGzipWithoutTouchingTheOriginal`：原 JSON/hash 不变、无 HealthKit 读取 |
| R2-F18 gzip 失败/损坏/超 8 MiB/能力不支持 | **PASS** | `compressedOverEightMiBKeepsTheBundleAndSendsNothing`（9 MiB 不可压缩 → `bundle_compressed_too_large` 422 non-retryable、0 请求、原包+hash 保留、无热重试）、`compressionRatioCapRejectsWithoutPlaintextFallback`、`compressionFailureMapsToADiagnosableNonRetryableError`、`handshakeWithoutGzipCapabilityFailsDiagnosablyAndKeepsTheTask`（`bundle_gzip_unsupported`） |
| R2-F19 bomb/CRC/截断/串联/尾部垃圾 | **PASS（服务端强制）** | Go `TestBundleGzipBoundaries`（valid/truncated/crc/garbage/concatenated/bomb/decoded-limit）全 PASS；客户端无解压器，损坏封存件走"从原包重新派生"（`corruptOrTruncatedSealedArtifactIsRebuiltFromTheOriginal`） |
| R2-F20 同 JSON 不同 gzip + 旧客户端 plain | **PASS** | `differentLegalGzipBytesKeepTheOriginalIdentityAndReceipt`；真实 PG 的 `duplicate_post` 由 Go 侧重发**原始明文 multipart** 仍返回同一条回执 |
| R2-P11 gzip 策略 | **PASS** | `bundle_gzip_post_success`/`bundle_gzip_post_attempt` 成功路径恒为 1、失败路径 attempt 为空；`bundle_plain_fallback` 在 18 项测试中**恒为 0**；`bundle_healthkit_reread` 恒为 0（且 `WorkoutBundleDelivery.swift` 只 `import Foundation`，全文无 HealthKit 引用） |

能力通道依据：`httpapi/server.go:313` 的握手本就声明 `"bundleGzipPart": true`（另有 `workoutBundles`、`bundleProtocolVersion: 3`），客户端此前从未读取；`syncing/protocol.go` 只有 batch/page 的 `ProtocolVersion=2`，bundle 侧约束全在 `httpapi/bundle_body.go`（`MaxBundleCompressedBytes=8<<20`、`MaxBundleFramingBytes=64<<10`、顶层 `Content-Encoding`→415、`gz.Multistream(false)`、比例上限 413、`invalid_gzip` 422）。因此未新增端点、未改协议版本、未改服务器。

真实 socket + PG18：`integration -tags=integration,swiftclient -run '^TestWorkoutReliabilitySwiftClientPostgres$' -v ./integration` → **18/18 场景 PASS**，`ok rai/server/integration 119.990s`。

### 4.5 R5 集合化写入与严格 JSON（R2-F21..F27 + R2-P10）

**候选对照**（独占，S60，n=10，0 失败，三者投影 digest 完全一致）：

| 候选 | import P50 / P95 | sample_writes | association_writes | 写合计 | WAL/import |
| --- | --- | --- | --- | --- | --- |
| values（原基线） | 2,461.5 / 3,779.7 | 844.0 | 827.9 | 1,671.9 | 17,752,193 |
| **arrays（采用）** | 2,432.7 / **3,039.5** | **758.1** | **761.2** | **1,519.3** | 17,752,268 |
| copy→staging | 2,433.4 / 3,299.9 | 826.8 | 906.4 | 1,733.2 | 17,848,371 |

采用 arrays：两个写阶段都最快、P95 低 19.6%、WAL 相同、不需要额外权限。COPY 更慢、WAL 最高，且在受限 API 角色下被 **`permission denied to create temporary tables`** 挡住（TEMP 已 REVOKE），最小所需权限＝数据库 TEMP 或固定 staging schema + CREATE；已保留实验与拒用理由。50k 边界：values 17.4 s / **arrays 14.9 s** / copy 16.0 s。

| 项 | 结论 | 依据 |
| --- | --- | --- |
| R2-F21 集合插入与旧导入基线 | **PASS** | `TestWorkoutWriteStrategyParity`：三策略 receipt/projection digest 逐一对拍、精确重放同回执、内容冲突 409 |
| R2-F22 1000 行边界 / 50k / 多 series+route | **PASS** | `TestWorkoutWriteStrategyRowBoundaries`（1000/1001 边界、50k、多 series/route） |
| R2-F23 冲突与 enrichment | **PASS** | `TestWorkoutWriteStrategyConflictRollback`（核心变化/跨父关联整包回滚；enrichment 接受且 `SamplesInserted=0`） |
| R2-F24 COPY/投影中断/COMMIT 故障 | **PASS** | `TestWorkoutWriteStrategyFaultRollback`：0 半包、0 孤立 job、0 残留，所有池连接 0 staging 表 |
| R2-F25 受限角色/池复用/TEMP 残留 | **PASS** | `TestWorkoutWriteStrategyRestrictedRole`（TEMP 被拒 → copy 阻断证据；arrays 成功；池复用后再导入成功） |
| R2-F26 custom/generic plan、冷/热连接 | **PASS** | `TestWorkoutWriteStrategyPlanModes` |
| R2-F27 严格 JSON 拒绝矩阵 | **PASS** | `TestStrictWorkoutBundleJSONRejectionMatrix` + `SemanticRules`（18 条拒绝规则逐条） |
| R2-P10a 写阶段不回退 | **PASS** | 写合计 1,671.9→1,519.3 ms（arrays），P95 3,779.7→3,039.5 |
| R2-P10b 写 P50 下降 ≥30% | **FAIL（−9.1%）** | 见上表 |

严格 JSON：反射 schema 缓存 + 每层复用 seen map + `strconv` 路径，普通构建 decode 降至 **103.8 / 263.1 / 409.0 ms**（S60/S60H/S240，约 40 MB/s）。

### 4.6 R0 性能入口与验收（R2-F32 / R2-P08 / R2-P09）

`scripts/local/test.sh syncperf --profiles S60,S60H,S240 --iterations 30 --warmups 3`，独占机器（iOS lane 已停），`SYNCPERF_MACHINE phase=before/after exclusive=true competitors=0` 记录在日志中。

| 档 | n | 失败 | P50 | P95 | min | max | cold n=5 P50/P95 | warmup P50 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| S60 | 30 | **0** | 2,796.7 | **4,758.8** | 2,117.8 | 4,793.2 | 2,179.8 / 2,293.8 | 1,965.5 |
| S60H | 30 | **0** | 4,277.9 | 5,753.4 | 3,714.8 | 6,690.7 | 3,763.7 / 4,444.4 | 3,564.5 |
| S240 | 30 | **0** | 12,035.0 | **19,722.3** | 8,814.6 | 20,567.4 | 11,093.2 / 18,034.4 | 8,985.2 |

S60 分段 P50/P95（ms）：import_total 2,687.9/4,646.3、sample_writes 841.3/1,612.7、association_writes 842.7/1,295.2、identity_checks 188.1/2,394.4、routes 145.8/207.3、receipt 104.8/246.3、commit 10.8/119.6、semantic_validation 18.3/19.4、json_decode 103.8/105.7。

**判定：R2-P08 FAIL（4.8×）、R2-P09 FAIL（4.9×）。**

**为什么 FAIL 是可信的（不是"没做完"）**：在同 schema、同索引、同 durability 的隔离库上做裸 PG 天花板探针，绕开 HTTP/Go 应用层：

| 方式 | S60 | S240 |
| --- | --- | --- |
| `COPY` 直写同形表（含索引） | **585 ms** | **2,442 ms** |
| `COPY` 进无索引表 → 再建索引 | 716 ms | 2,964 ms |
| 一次事务 + unnest 大批次（现 arrays） | 与上面差 <5% | 与上面差 <5% |

按 uuid 排序插入仅改善 5–8%（已试、已测，记"验证无效"）。即便写入达到 585 ms 物理下限，S60 仍要加上 decode 103.8 + receipt 104.8 + routes 145.8 + identity 188.1 + commit 10.8 + validation 18.3 ≈ **1.16 s**，已超过 1 s 门槛；S240 同理（2,442 + 409 + 466 + 657 + 421 ≈ 4.4 s > 4 s）。**结论：R2-P08/P09 在本机 + 强制 durability 下不可达，属物理下限而非实现缺陷。**

长尾归因（已量化）：每行 WAL ≈ 922 B（真实导入与裸探针一致，三种候选差 <0.5%），由行 + 6 个索引决定；S240 每次导入 WAL 中位 80 MB、峰值 379 MB / 53,406 FPI / `ckpt_sync` 6,125 ms——慢的 9 次迭代正是 FPI 重的那些。`identity_checks` 长尾（S240 前三次 5,456/9,591/11,555 ms 后塌到 275–476 ms）是 autoanalyze 追上的特征；`TestIdentityCheckScaling` 证明语句计划本身稳定（Hash Left Join，2,197 行 9.0 ms → 26,364 行 34.8 ms）。

S240 历史 6 次 503 的**确切错误码 NOT VERIFIED**（旧二进制未记响应体），但已排除"数据量变大"（失败含 i=0/i=1 全新库），并钉死可达路径：`TestWorkoutBundleStatementTimeoutCode` 实测 `503 {"code":"database_statement_timeout_or_cancel"}` @30,049 ms 且整包回滚 0 残留。

---

## 5. 性能前后对照

| 指标 | 第一轮（race 口径，不可直接比） | 本轮 R0（普通构建，真实 TCP） | 变化 |
| --- | --- | --- | --- |
| S60 ack_total P50 / P95 | 4,901.3 / 5,646.0 | **2,796.7 / 4,758.8** | P50 −42.9%，P95 −15.7% |
| S240 ack_total P50 / P95 | 21,080.2 / 31,036.3 | **12,035.0 / 19,722.3** | P50 −42.9%，P95 −36.5% |
| 严格 JSON decode P50（S60） | 1,649.6（race） | **103.8** | −93.7%（其中大部分是口径修正，见 §2.3） |
| sample_writes P50（S60） | 1,068（race） | **841.3**（values 844.0 → arrays 758.1，n=10 对照） | 集合化后小幅改善 |
| association_writes P50（S60） | 1,038（race） | **842.7**（arrays 761.2） | 同上 |
| 逐样本归档（iOS） | 真机 8.8 s / 12.78 s | **0 次调用**（合成） | 结构性消除；真机端到端复测见 §8.3（时延 FAIL；规范化子阶段未单独剖分） |
| 规范化+持久化阶段 P50 | — | 1,530.05 → 1,115.62 ms（优化构建，N=2000） | **−27.09%（未达 70%，FAIL）** |

**口径警告**：第一轮数字来自 race 构建 + `httptest` + handler 内计时，本轮来自普通构建 + 真实 socket + 完整往返。上表"变化"一栏混合了**口径修正**与**真实优化**两种成分，不可当作纯优化收益引用。

---

## 6. 故障历史（本轮发现并修复的真实问题）

| # | 问题 | 性质 | 处理 |
| --- | --- | --- | --- |
| 1 | 捕获窗口内迟到 sample 被 ACK 错标 delivered，下一轮被 `isAlreadyDelivered` 跳过 | **真实数据丢失**（pre-fix 复证） | 不可变 `CaptureManifest` + 逐事件身份判据；R2-F07 |
| 2 | 捕获窗口内子删除被 ACK 错标 delivered，服务端样本复活 | **真实数据复活** | 同上 |
| 3 | `ordinal <= cursor` 作为覆盖判据是错的（全局提交序 ≠ 包内覆盖） | 设计错误（自查发现，未上线） | 改为只认 event 身份，cursor 仅作证据 |
| 4 | `zipped.count > 8 MiB` 时客户端明文回退上传 | 契约违反（第一轮遗留） | 删除回退，改为明确失败；R2-F18 |
| 5 | 测量口径混用（race 数字被当普通构建基线） | 方法论错误 | 新增 `syncperf` 入口并在二进制内断言构建设置 |
| 6 | 首轮"decode 是主瓶颈"的结论 | **被推翻**：普通构建 decode 仅 103.8 ms，race 放大 ~14× | 结论已在本文件与证据中更正 |
| 7 | 一次 `r0-after-arrays` 测量在 iOS lane 并发时进行，P95 翻倍 | 测量污染 | 该组数字标记为受污染、不用于验收；最终验收在独占窗口重跑 |
| 8 | `scripts/local/test.sh integration` 不导出 `DEVELOPER_DIR`，带 `swiftclient` 标签的集成测试在编译期就死（`no such module 'Testing'`，CommandLineTools 里没有该模块），表现为 18 个子用例全 FAIL | 本地脚本缺陷（非产品缺陷） | 已修：`integration` profile 在参数含 `swiftclient` 时调用 `rai_xcode_env`；修后同一入口 **18/18 PASS**（93.0 s），无需手工 export |

---

## 7. 残余风险与回退

**残余风险**
1. **性能门槛未达**（R2-P08/P09/P10b、R2-P01）：服务端 P95 与规范化降幅均 FAIL，且已证明是物理下限级别。要真正达标需要改变部署条件（更快存储/关闭 durability/扩大 `max_wal_size` 或 `shared_buffers`）或降低事实密度——这些都超出本轮授权范围，未做。
2. **真机已执行但未通过性能门禁**：P03/P04 时延、S60H RSS、P14 10× 均 FAIL；并发 2 流无收益、4 流更差；P05 与 P14 100× 未执行，P13 双槽未证实。
3. gzip 能力不兼容时任务停在 `.failed` 且**不自愈**（符合"明确失败"要求，但需要人工介入或发版）。
4. 能力盒是 per-client-instance：只有调过同一实例 `GET /v2/handshake` 才知道声明；直接新建 `RAIAPIClient` 的路径为 `.unknown` → 仍发 gzip。
5. 封存件的"不可变"靠内容寻址校验（复用必须 hash 相符且仍是 gzip part），**没有**文件系统只读位（`savePayload` 在 OutboxStore，越界且既有测试需要改写该文件验证重建）。
6. route 流只占并发槽、未按字节预留（点数据由 `RouteCaptureFile` 自行分批落盘）。
7. `normalized_capture_ms` 是各流耗时之和，不是墙钟。
8. `identity_checks` 长尾唯一可控杠杆是自动统计刷新（每表 autovacuum 阈值或导入后 ANALYZE），涉及迁移决策，**未做**。

**回退**
- 可用内部策略切回**串行读取**（`CaptureConcurrencyPolicy` 设 1 流）或旧安全 SQL 方案（`RAI_SYNC_PERF_WRITE_STRATEGY=values`），两者都不破坏正确性。
- **回退不得用明文规避压缩失败**：新版客户端始终 gzip-only；压缩失败保留原包并明确失败。
- 新版本内同时保留旧 archive 与 v2 规范化两种读取能力；已准备原包优先恢复（`WorkoutBundleDelivery` 校验原包 hash 后复用封存 gzip，不重读 HealthKit）。
- 区别："新版本内策略回退"（切并发度/写策略，可用）与"旧二进制回退"（旧程序读 v2 规范化 segment，**未验证，不承诺**）。回退**不得**通过清 Outbox 或重置 anchor 完成。

---

## 8. 完成门禁与未执行 / 未验证项

### 8.1 完成门禁（`scripts/local/test.sh full`）

**PASS**，`LOCAL CHECKS: PASS profile=full elapsed_seconds=1338`，`EXIT=0`，日志 `docs/evidence/workout-sync-speed-round2/lead/full-gate.log`。

| 步骤 | 结果 |
| --- | --- |
| `go mod verify` | `all modules verified` |
| `go test -race -count=1 ./...` | 全绿（0 FAIL） |
| `go test -race -count=1 -tags=integration -p 1` | `ok rai/server/integration 1099.261s` |
| `go vet`（含 `-tags=integration`）、`go build` | 通过 |
| sqlc 生成与 dbgen diff | 无 diff |
| 依赖边界检查 | 9 项 PASS（含 258 包导入图） |
| 隔离 PG 清理 | `RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1` |

其他入口：新增 F29 用例后补跑 `gate` **PASS**（42 s，Go race/vet/边界）与定向 `integration` **PASS**（1.5 s，隔离 PG）；Swift 包测试 220 tests / 29 suites PASS；模拟器 lane 85 passed / 0 failed / 3 skipped；`e2e` PASS；`swiftclient` 真实 Swift→HTTP→PG 18/18 PASS。`integration` profile 本轮修复了 `swiftclient` 标签下不导出 `DEVELOPER_DIR` 的脚本缺陷（见 §6 第 8 条）。

### 8.2 未执行 / 未验证项

| 项 | 状态 | 说明 |
| --- | --- | --- |
| R2-P05 S120/S240 真机时延 | **NOT RUN** | 设备库无 120/240 分钟记录；108 分钟稀疏记录不足以代签 S120/S240 |
| R2-P14 100×S60 批量 | **NOT RUN** | 设备仅 51 条 eligible 记录，无法凑出 100 条不重复首导入；10×S60 已由 20 个 S60 样本中的前 10 次求和判定 **FAIL** |
| R2-P13 真正双上传槽并发 | **PARTIAL** | 单设备只占一个 device slot；两次并发重放得到 `1×200 + 1×429 device_upload_capacity_exceeded`，未形成双槽同时解析/导入 |
| R2-P12 S240 iOS RSS | **NOT RUN** | 无 240 分钟记录；S60/S60H 已测，见 §8.3 |
| S240 历史 6 次 503 的确切错误码 | **NOT VERIFIED** | 旧二进制未记响应体；可达路径已用测试钉死 |
| 写阶段 Go 侧 CPU profile | **NOT RUN** | 生产写阶段 1,519 ms vs 裸地板 585 ms 那 2.4× 差距的下一步诊断 |
| 自动统计刷新（autovacuum 阈值/导入后 ANALYZE） | **未做** | 涉及迁移决策 |
| 50k 行用例在 `-race` 下 | **NOT RUN** | 耗时过长；断言已在普通构建覆盖 |
| RQRun 公平对标、生产部署 | **NOT VERIFIED / NOT RUN** | 本轮默认不做 |

### 8.3 真机复测记录（2026-11-06，USB 连接，已授权执行）

**设备与口径**：iPhone 12 Pro Max（iPhone13,4），iOS 27.2，USB wired+paired；App 为 Release 构建并仅以 `SWIFT_ACTIVE_COMPILATION_CONDITIONS=DEBUG` 打开验收仪器；本机 API/Worker + 隔离 PG18（`localhost:55434`/`18082`），每次首导入前 `reset` 新建一次性 dataset，手机不写 HealthKit，仅只读真实记录。以下均为同一台设备、同一 LAN、默认预算与 durability；每组样本独立冷启动，取 nearest-rank P50/P95。原始报告只保留时延/计数/RSS 等白名单字段，不含健康值、GPS、Workout UUID 或设备标识；机器可读聚合与复现命令见 `docs/evidence/workout-sync-speed-round2/device/round2-device.md`。

| 项 | 门槛 | 实测 | 结论 |
| --- | --- | --- | --- |
| R2-P03 S20–S60（32 分钟 W32-A，n=20，streams=2） | P50≤5 s、P95≤8 s | **P50 8,182.27 ms、P95 11,260.71 ms**（min 7,607.42 / max 11,379.07）；HK 读取 P50 4,995.42 / P95 7,382.12；build 5,422.13；upload 2,732.79 | **FAIL**（P50 1.64×、P95 1.41×） |
| R2-P04 S60H（58 分钟 W58-A，n=20，streams=2） | P50≤5 s、P95≤8 s | **P50 18,274.23 ms、P95 24,637.76 ms**（min 15,155.59 / max 25,122.66）；HK P50 11,558.97 / P95 17,203.48；RSS max 302.34 MiB | **FAIL**（P50 3.65×、P95 3.08×） |
| R2-P02 并发 1/2/4（S60H） | 对照，≥20% 收益 | streams=1 P50 17,638.31（n=10）、2 P50 18,274.23（n=20）、4 P50 20,194.13（n=10）；2 比 1 慢 3.6%，4 比 1 慢 14.5% | **PARTIAL：无收益；4 更差** |
| R2-P05 S120/S240 | P50≤8/15 s、P95≤12/22 s | 无 120/240 分钟记录，未执行 | **NOT RUN** |
| R2-P06 无变化重复 S60（n=9，`--no-reset`） | P50≤2 s、P95≤4 s；0 新 POST/import/job | 9/9 `workout_sync_skipped_unchanged=1`；App wallMs 347.3–426.7，**P50 368.3 ms、P95 426.7 ms**；API 日志窗口内 **0 次** `POST /v2/sync/workout-bundles`，无导入/分析任务 | **PASS** |
| R2-P07 已备原包恢复 | HK 查询 0、JSON/hash 一致、gzip 100% | 首次 503 拒绝：wire **483,260 B**（bundle 4,330,151 B，比例 11.2%）；恢复启动 **2,907.97 ms**，receipt 校验通过，前后 bundleSHA256 均为 `7cffa820…`；DB 仅 1 次 import、5,464 samples、1,768 routes | **PASS**（行为证据；`healthkit_read_ms` 保留失败前旧值 5,090.02 ms，说明恢复未覆盖/未读 HK，但无显式零计数器） |
| R2-P11 gzip 压缩时延 | 成功 100%、P50≤200 ms | S60 gzip_ms P50 **39.81 ms**、S60H **94.5 ms**（max 138.13）；全部 bundle POST 单 gzip file part，明文 fallback 0 | **PASS** |
| R2-P12 iOS 峰值 RSS | S60/S60H≤256 MiB；S240≤450 MiB | S60 max **206.5 MiB**（median 198.6）；S60H max **302.34 MiB**（s1 297.81、s4 291.61）；S240 未测 | **FAIL**（S60 PASS、S60H FAIL、S240 NOT RUN） |
| R2-P13 API/UI | API 峰值≤512 MiB；UI P95≤100 ms、无>250 ms | 单槽 S60H 上传：baseline ≈10.5 MiB、peak **104.8 MiB**（311×50 ms）；设备主线程 max stall 1.1–6.6 ms（S60H）/4 ms（S60），>250 ms 0 次；双槽重放未形成（1×200+1×429） | **PARTIAL**（单槽余量 4.9×；双槽未证实） |
| R2-P14 10×S60 / 100×S60 | ≤60 s / ≤360 s | 前 10 次 S60 首导入求和 **80,219.01 ms**（P50 8,062.18）；100 次未执行 | **10× FAIL；100× NOT RUN** |

**解释与限制**：
- 时延构成仍是 HealthKit 读取主导：S60 的 workout_sync P50 8.18 s 中 HK 读取 5.00 s、upload 2.73 s；S60H 18.27 s 中 HK 读取 11.56 s、upload 约 5.05 s。归档调用已为 0，剩余成本主要是真实 HK 分页读取 + 服务端导入/上传，不是旧的逐样本归档。
- S60H 的 n=20 由 `s60h-s2` 15 次 + USB 短暂断连后补测 `s60h-s2b` 5 次合并；断连后连接属性仍为 wired/paired，未重建 App。合并样本含 2 次 24.6/25.1 s 热漂移尾部。
- 所有档位按 1→2→4 顺序测量，设备后段整体变慢，存在热漂移/顺序混杂；但 P03/P04 的中位数已大幅超过门槛，不能用热漂移解释为"接近达标"。
- P06 的 `timings.workout_sync_ms/healthkit_read_ms` 是首次真实同步遗留的持久 metrics，skip 路径不覆盖；因此 P06 用 report `wallMs`（App 启动→完成）作为用户等待代理，并另以 API 日志确认 0 POST。
- P07 的恢复证明"已备原包不重新采集、不重新压缩、上传同一 identity"；但缺少一个显式 `healthkit_query_count=0` 字段，故只能以时延/旧值未变/hash 一致作行为证据。
- 所有真机样本均为真实只读 Workout；未产生室内跑或 240 分钟记录，S120/S240 无法用现有库签核。

---

## 9. 签核矩阵

### 9.1 功能与故障硬门禁（R2-F01–R2-F32）

| 门禁 | 结论 | 支撑（真实执行） |
| --- | --- | --- |
| R2-F01 正常样本发现→组包，归档调用为 0 | **PASS** | `NormalizedCaptureTests`(4) + `NormalizedCaptureSegmentTests`(7)：`sample_archive_calls=0`/`sample_unarchive_calls=0`（timings + `SampleArchiveMeter` 双口径，旧路径对照 14,000 次）；组包直接消费规范化 fact，无"整页数组一次归档"绕过 |
| R2-F02 新旧捕获同源 | **PASS** | bundle samples 与 `Normalizer.integrityEvent` **逐字节相等**；identityHash 与旧路径一致；canonical bytes 稳定 |
| R2-F03 旧 archive/旧 segment/新格式混合 | **PASS** | 旧 archive 段、旧 seal、旧 pending 均可读（format 1..2），未改旧 pending hash/回执；重启级用例 `mixedLegacyArchiveAndSegmentFactsRecoverAfterRestart` PASS |
| R2-F04 unsupported metadata / native carrier | **PASS（服务端范围）** | App+包侧既有用例 PASS；HTTP/PG 侧新增执行 `TestPhase3ObservationStrictSourceCoverageAndPrivacy`、`TestPhase3HeartRateSampleMetadataAllowlist`（unit）与 `TestPhase3MetadataProvenanceCoverageAppendHistoryAndImmutableCore`（integration，1.6 s，隔离 PG）均 PASS；native carrier 仍属 App 侧，未在 HTTP/PG 复现 |
| R2-F05 批次截断 / seal / 引用发布失败 | **PASS**（既有覆盖） | `CaptureSegmentsTests`、`GarbageCollectionTests`(3)、`WorkoutCompletenessTests.payloadCorruptionAndStatePublishFailurePreserveAuthority` |
| R2-F06 ACK 丢失 / 本地 ACK 保存失败 | **PASS** | `TestWorkoutReliabilitySwiftClientPostgres` **18/18**（lost_ack / corrupt_ack / duplicate_post / two_workers / before_send / during_send / before_commit_timeout / commit_404 / validation_missing / rate_limit / 500 / 502 / 503 / auth_refresh / tombstone / phase3_observation / phase3_child_delete / phase3_deleted_parent）+ `WorkoutReliabilityTests`(4) |
| R2-F07 捕获中后到 sample / 子删除 | **PASS** | `CaptureAckBoundaryTests`(3，含 pre-fix 复证) + 服务端 `phase3_child_delete`/`phase3_deleted_parent` |
| R2-F08 父删除与并发上传 | **PASS** | `TestWorkoutDeleteAndImportBothLockOrders`（`delete_first_true`/`delete_first_false` 两种锁顺序均不复活）、`TestWorkoutBundleConcurrentSnapshotsRetainUnion`；旧回执恢复由 lost_ack/duplicate_post 覆盖 |
| R2-F09 缺基线 / 未耗尽分页 | **PASS**（App 合成） | `WorkoutListPagingTests`(4)、`NormalizerTests.testInvalidSecureAnchorNeverQueriesWithNil`、`WorkoutCompletenessTests.{lostACKOfflineCorruptACKAndRelaunchNeverAdvanceAnchorsEarly, pendingStreamCannotFinalizeAndStagedCursorIsNotCommitted}`、`CaptureConcurrencyTests.testOneStreamPagesSeriallyWithoutSkippingOrCrossingItsAnchor` |
| R2-F10 并发 1/2/4 结果一致 | **PASS** | `testMergeOrderIsIndependentOfCompletionOrder`：1 流顺序 vs 4 流乱序载荷逐字节相同 |
| R2-F11 实际并发与全局限额 | **PASS** | peak active `[1:1, 2:2, 4:4]`；`CaptureGateTests.streamSlotsAreBoundedAndHandedOver` |
| R2-F12 同类型多页 anchor | **PASS** | anchor `["-","A1"]`，同类型 peak active == 1，不跳页不串 cursor |
| R2-F13 单类型失败 | **PASS** | `testFailingStreamReleasesItsSlotForTheOthers` |
| R2-F14 取消与槽泄漏 | **PASS** | `testCancellationStopsAParkedCaptureStream`：取消后 active=0，同 store 新协调器可再满预算同步 |
| R2-F15 背压/慢磁盘/大批次 | **PASS** | `byteBudgetBackpressuresTheProducer`、`oversizedReservationIsClampedToTheBudget`；peak 队列字节 1,572/1,572/2,370 B ≤ 2 MiB |
| R2-F16 全新上传与全部重试只发 gzip | **PASS** | 5 项 GzipTransport 测试，断言 `1f 8b`、`filename="workout.json.gz"`、`Content-Type: application/gzip`、payload 与 `Wire.gzip(raw)` 逐字节相同 |
| R2-F17 旧 plain pending 恢复 | **PASS** | `oldPlainPendingTaskResumesAsGzipWithoutTouchingTheOriginal`：原 JSON/hash 不变、无 HealthKit 读取 |
| R2-F18 gzip 失败/损坏/超 8 MiB/能力不支持 | **PASS** | 4 项测试；明文 fallback 计数恒 0、无热重试、原包保留 |
| R2-F19 bomb/CRC/截断/串联/尾部垃圾 | **PASS**（服务端强制） | Go `TestBundleGzipBoundaries`（valid/truncated/crc/garbage/concatenated/bomb/decoded-limit）；客户端无解压器，损坏封存件从原包重派生 |
| R2-F20 同 JSON 不同 gzip + 旧客户端 plain | **PASS** | `differentLegalGzipBytesKeepTheOriginalIdentityAndReceipt`；真实 PG `duplicate_post` 重发明文 multipart 仍返回同回执 |
| R2-F21 集合插入与旧导入基线对拍 | **PASS** | `TestWorkoutWriteStrategyParity` |
| R2-F22 1000 行边界 / 50k / 多 series+route | **PASS** | `TestWorkoutWriteStrategyRowBoundaries`（50k 用例在 `-race` 下 NOT RUN，普通构建已覆盖） |
| R2-F23 冲突与 enrichment | **PASS** | `TestWorkoutWriteStrategyConflictRollback` |
| R2-F24 COPY/投影/COMMIT 故障回滚 | **PASS** | `TestWorkoutWriteStrategyFaultRollback` |
| R2-F25 受限角色 / 池复用 / TEMP 残留 | **PASS** | `TestWorkoutWriteStrategyRestrictedRole` |
| R2-F26 custom/generic plan、冷/热连接 | **PASS** | `TestWorkoutWriteStrategyPlanModes` |
| R2-F27 严格 JSON 全部拒绝矩阵 | **PASS** | `TestStrictWorkoutBundleJSONRejectionMatrix` + `SemanticRules`（18 条） |
| R2-F28 来源过滤与既有删除 | **PASS** | `SourcePolicyGateTests`(4)、`NormalizerTests.{testR3SourcePolicyV2ExcludedInventoryIsEvidenced, testNewSessionNilAnchorReimportsVisibleHKWorkoutAfterOldDeletion}` |
| R2-F29 缺 HR / 室内无路线 / 无功率 | **PASS** | 室内/allowlist 与功率既有用例 PASS；新增 `TestWorkoutCompletenessHeartRateAbsentIsCoveredNotFailed`：仅 distance 样本、HR 覆盖为 `no_records_observed` 的 workout 能导入并通过 completeness（隔离 PG，1.5 s）；无 240 分钟/室内真实记录仍限制真机覆盖 |
| R2-F30 无变化 / 迟到变化 / 每日健康流 | **PASS** | `WorkoutSyncReuseTests`(3) + `DailyHealthTests`(7) |
| R2-F31 v2 证明两代、丢 ACK、中断续跑 | **PASS** | `scripts/local/test.sh e2e` → `TestWorkoutCompletenessSimulatorHTTPPostgres` 45.79 s，`LOCAL CHECKS: PASS profile=e2e` |
| R2-F32 性能入口与构建口径 | **PASS**（口径）/ 数字另记 | `syncperf` 断言无 `-race`/`-N`/`-l`、打印有效 flags、`SYNCPERF_MACHINE` 记录独占性；race 与普通构建口径可辨；性能数字见 §9.2（FAIL） |

**执行计数**：SyncCore 门禁套件 44 tests / 7 suites PASS；App 四套件 45 executed / 42 passed / 3 skipped / 0 failed（skip 均为需外部 PG 或 golden 目录的既有用例）；`swiftclient` 18/18 PASS；e2e 1/1 PASS；锁顺序 2 测试 3 子用例 PASS；包测试全量 234 tests / 31 suites PASS；模拟器 lane 85 passed / 0 failed / 3 skipped。

### 9.2 性能与资源验收（R2-P01–R2-P14）

| 项 | 结论 | 实测 |
| --- | --- | --- |
| R2-P01 规范化+持久化阶段降幅 ≥70% | **FAIL** | 1,530.05 → 1,115.62 ms（**−27.09%**，优化构建，N=2,000，7 次 P50）；该段结构性上限 ~35% |
| R2-P02 并发 1/2/4 | **PARTIAL** | 真机 S60H：1 流 P50 17,638.31 ms、2 流 18,274.23 ms、4 流 20,194.13 ms；2 比 1 慢 3.6%，4 比 1 慢 14.5%，无 ≥20% 收益；一致性/取消/背压 PASS |
| R2-P03 S20–S60 真机首等 | **FAIL** | S60 n=20：P50 **8,182.27 ms**（目标≤5 s）、P95 **11,260.71 ms**（目标≤8 s） |
| R2-P04 S60H 真机 | **FAIL** | S60H n=20：P50 **18,274.23 ms**（目标≤5 s）、P95 **24,637.76 ms**（目标≤8 s） |
| R2-P05 S120/S240 真机 | **NOT RUN** | 设备无 120/240 分钟记录；108 分钟稀疏记录不足以代签 |
| R2-P06 无变化重复 S60 | **PASS** | 9/9 skip；wallMs P50 **368.3 ms**、P95 **426.7 ms**（≤2/4 s）；API 窗口 0 bundle POST、0 import/job |
| R2-P07 已备原包恢复（真机时延） | **PASS** | 首次 503 拒绝后恢复 **2,907.97 ms**；wire 483,260 B / bundle 4,330,151 B（11.2% gzip）；前后 hash 一致；DB 仅 1 次 import；HK 未重读（旧 healthkit_read_ms 未覆盖，无显式 0） |
| R2-P08 普通构建 S60 P95≤1 s | **FAIL** | **4,758.8 ms**（P50 2,796.7，n=30，0 失败），4.8× 超门槛；裸 PG 地板 585 ms + 其他分段已 ≥1.16 s |
| R2-P09 普通构建 S240 P95≤4 s | **FAIL** | **19,722.3 ms**（P50 12,035.0，n=30，0 失败），4.9×；裸 PG 地板 2,442 ms + 其他分段 ≈4.4 s |
| R2-P10 SQL 候选 | **PARTIAL** | 10a 不回退 **PASS**（写合计 1,671.9→1,519.3 ms，P95 3,779.7→3,039.5）；10b 下降 ≥30% **FAIL**（−9.1%）；三候选对照与 COPY 受限角色阻断证据齐备 |
| R2-P11 gzip 策略 | **PASS** | 成功 POST gzip 比例 100%（`bundle_gzip_post_success`）、明文 fallback 恒 0、已备原包重试 HK 读取恒 0 |
| R2-P12 iOS 峰值 RSS | **FAIL** | S60 max **206.5 MiB** PASS；S60H max **302.34 MiB** >256 MiB FAIL；S240 NOT RUN |
| R2-P13 API/UI | **PARTIAL** | 单槽 API peak **104.8 MiB**（≤512，余量 4.9×）；主线程 max stall 1.1/4 ms，>250 ms 0 次；双槽并发未形成（1×200+1×429） |
| R2-P14 默认预算批量 | **FAIL（10×）/ NOT RUN（100×）** | 前 10 次 S60 首导入合计 **80,219.01 ms** > 60 s；100 条未执行 |

### 9.3 最终签核

| 签核 | 结论 | 依据 |
| --- | --- | --- |
| NORMALIZED CAPTURE | **PASS** | archiver/unarchiver = 0；无损（逐字节对拍）；旧 archive/旧 segment/旧 pending 兼容；重启级混合格式用例 PASS（F03）；恢复路径由 F06/F07 覆盖 |
| BOUNDED CONCURRENCY | **PARTIAL** | 合成 1/2/4 一致性 PASS；真机 2 流比 1 流慢 3.6%、4 流慢 14.5%，**无收益**；默认 2 的收益依据不成立，保留待调 |
| GZIP ONLY CLIENT | **PASS** | 所有 bundle POST 为单 gzip file part；异常路径 0 明文回退；旧 pending 不改 identity；服务器旧 plain 兼容 |
| SET BASED IMPORT | **PASS** | 集合 SQL（arrays）+ 三候选对照 + 受限角色证据 + 同事务/删除/幂等/冲突回归；COPY **未采用**（更慢且被 TEMP 权限阻断） |
| FUNCTIONAL SAFETY | **PASS（有真机覆盖限制）** | R2-F03/F04（服务端）/F29 已补测 PASS；F01–F32 无 FAIL；S120/S240/室内/无功率的真机覆盖仍缺失，见 §8.2 |
| PERFORMANCE | **FAIL** | R2-P01/P03/P04/P08/P09/P10b/P12/P14(10×) 未达目标；P06/P07/P11 PASS；P13 与 P02 PARTIAL；P05/P14(100×) NOT RUN |
| REAL HEALTHKIT | **已执行（未通过时延）** | iPhone 12 Pro Max Release 真机、真实只读 Workout、本机后端；端到端路径可用，但 P03/P04 时延与 S60H RSS 未达标 |
| RQRUN / DEPLOYMENT | **NOT VERIFIED / NOT RUN** | 本轮未做公平对标、未部署、未切换入口、未迁移数据 |


---

## 10. 本轮文档工作声明

- 真机阶段仅在本机已授权设备上**只读**真实 HealthKit 记录用于性能测量；未记录、导出或提交任何健康值、GPS、Workout UUID 或设备标识，未部署，未切换旧入口，未迁移或清理真实状态，未 commit / push。
- 所有测试均在本机执行；PG 使用 `mktemp` 一次性 cluster，跑完 `RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`。
- 第一轮结果与原方案原文未修改；本文件只记录本轮校准后的解释，历史 FAIL 全部保留。
