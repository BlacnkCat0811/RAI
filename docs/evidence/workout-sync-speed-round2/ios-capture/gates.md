# 门禁补齐（PG / HTTP / App / 性能入口）— 映射与真实执行记录

日期：2026-11-06 · 机器：本机 Mac（模拟器 "RAI Local Tests" / iPhone 12 Pro Max / iOS 26.5 x86_64，
udid `5DA14A13-5EFA-4E08-90F4-4B90FAD0A6B9`）+ 本机 Go HTTP + 一次性 PG 18（Postgres.app 18.6）。
全部合成数据；无真机、无真实健康值、无凭证。串行执行：同一时刻只有一个 PG 实例 / 一个 xcodebuild。

**所有 PG 运行结束时均确认 `RAI_TEST_LEFTOVER_DATABASES=0`（e2e 另有 `RAI_TEST_CLUSTER_REMOVED=1`）。**

## 0. 一条必须知道的环境事实（不是代码缺陷）

`scripts/local/test.sh integration` **不会**导出 `DEVELOPER_DIR`（只有 `swift` / `simulator` / `e2e` profile 走
`rai_xcode_env`）。本机 `xcode-select -p` 指向 `/Library/Developer/CommandLineTools`，那里的 `swift` 没有
`Testing` 模块，于是 Swift 客户端集成测试会在**编译阶段**就失败：

```
swiftclient 首跑（未导出 DEVELOPER_DIR）：
  ios/RAI/Tests/SyncCoreTests/APITests.swift:3:8: error: no such module 'Testing'
  workout_reliability_swift_test.go:178: Swift client failed: exit status 1
  --- FAIL: TestWorkoutReliabilitySwiftClientPostgres (150.23s)   ← 18 个子用例全失败（同一编译错误）
```

修法（不改代码、不改别人的脚本）：跑 integration 前显式导出 Xcode 工具链，之后 **18/18 子用例全 PASS**：

```
export DEVELOPER_DIR=$HOME/Downloads/Xcode.app/Contents/Developer
```

## 1. R2-F31 / R2-F06 关键路径 — `scripts/local/test.sh e2e`

```
cd /Users/ta/RAI && scripts/local/test.sh e2e
```

结果：**PASS**（`LOCAL CHECKS: PASS profile=e2e elapsed_seconds=69`）

```
=== RUN   TestWorkoutCompletenessSimulatorHTTPPostgres
--- PASS: TestWorkoutCompletenessSimulatorHTTPPostgres (45.79s)
PASS   ok  rai/server/integration  46.826s
RAI_TEST_LEFTOVER_DATABASES=0
RAI_TEST_CLUSTER_REMOVED=1
```

口径：`build-for-testing`（`RAI_UA1_R_BUILD_ONLY=1`）→ 真实模拟器 App → 本机 Go HTTP → 一次性 PG18，
`-tags=integration,ua1rsim -run '^TestWorkoutCompletenessSimulatorHTTPPostgres$'`。
覆盖 v2 两代清单/回执、丢 ACK、中断续跑（模拟器侧驱动，服务端断言 revision/job 不重复）。

## 2. R2-F06 / R2-F08 / R2-F20 — 真实 Swift → HTTP → PG

```
export DEVELOPER_DIR=$HOME/Downloads/Xcode.app/Contents/Developer
scripts/local/test.sh integration -tags=integration,swiftclient \
  -run '^TestWorkoutReliabilitySwiftClientPostgres$' -v ./integration
```

结果：**PASS**（`ok rai/server/integration 105.517s`），18/18 子用例：

| 子用例 | 覆盖 |
| --- | --- |
| before_send / during_send / before_commit_timeout | 发送前后中断，原包保留 |
| **lost_ack** | ACK 丢失 → 重发原包、原 hash，事实/revision/job 不重复（R2-F06 核心） |
| **corrupt_ack** | 损坏回执被拒，不释放包 |
| commit_404 / validation_missing / rate_limit / status_500 / status_502 / status_503 | 失败分类与退避，不伪造成功 |
| **two_workers** / **duplicate_post** | 两写者一 claim、重复 POST 返回原回执（R2-F06/F08） |
| auth_refresh | 令牌刷新后重试一次 |
| tombstone | 删除墓碑不复活 |
| phase3_observation / phase3_child_delete / phase3_deleted_parent | 观察、子删除、父删除导入语义（R2-F07/F08 的服务端侧） |

## 3. R2-F08 — 父删除与并发上传的**两种锁顺序**

```
export DEVELOPER_DIR=$HOME/Downloads/Xcode.app/Contents/Developer
scripts/local/test.sh integration \
  -run '^(TestWorkoutDeleteAndImportBothLockOrders|TestWorkoutBundleConcurrentSnapshotsRetainUnion)$' -v ./integration
```

结果：**PASS**（`ok rai/server/integration 4.699s`）

```
--- PASS: TestWorkoutBundleConcurrentSnapshotsRetainUnion (1.35s)
--- PASS: TestWorkoutDeleteAndImportBothLockOrders (2.31s)
    --- PASS: .../delete_first_true     ← 先删后传
    --- PASS: .../delete_first_false    ← 先传后删
```

两个子用例就是"两种锁顺序"，两种顺序下都不复活（旧回执恢复另由上一节的 `lost_ack`/`duplicate_post` 覆盖）。
→ **R2-F08 PASS**（用既有真实 PG 用例，未临时拼假并发测试）。

## 4. SyncCore 侧门禁套件

```
scripts/local/test.sh swift --scratch-path .local/swift-build/capture \
  --filter 'WorkoutReliabilityTests|RecoveryBoundaryTests|WorkoutCompletenessTests|GarbageCollectionTests|DailyHealthTests|WorkoutPerformanceTests|WorkoutSourcePolicyTests|DiscoveryReuseTests'
```

结果：**PASS** — `✔ Test run with 44 tests in 7 suites passed after 0.530 seconds`
（`WorkoutPerformanceTests` 是 XCTest 且被 env 门禁 skip，见第 6 节，单独跑）。

覆盖到的门禁：
- R2-F06：`WorkoutReliabilityTests.{cancelledDeliveryKeepsBundleAndCachedResponseForReattachment, stateWriteFailureDuplicateACKAndCleanupFailure, explicitRescanAndProoflessLegacyACKCannotReleaseBundle, deterministicGzipArtifactSurvivesRestartCorruptionAndACK}`
- R2-F09：`WorkoutCompletenessTests.{lostACKOfflineCorruptACKAndRelaunchNeverAdvanceAnchorsEarly, pendingStreamCannotFinalizeAndStagedCursorIsNotCommitted}`
- R2-F05（既有覆盖，非本轮新增）：`GarbageCollectionTests.{durableAnchorPrecedesSweepAndPendingFilesSurvive, referencedOrAuthorityFilesCannotBeSwept, failedAuthorityCommitAndInterruptedSweepRecover}`、`CaptureSegmentsTests`（seal/publish/`gc_inflight_file`）
- R2-F30（每日健康流）：`DailyHealthTests`（7 项，含 `perTypeAnchorsPendingAndDeletesSurviveRestart`、`killAfterServerCommitAndFailedAnchorSaveRecoverOldOrNewState`、`observerDuringQueryRetainsNewerWakeGeneration`）
- R2-F04（策略侧）：`WorkoutSourcePolicyTests`、`DiscoveryReuseTests`
- 恢复边界：`RecoveryBoundaryTests.{deliveredPayloadMayBeUnavailableAfterServerRollback, pendingAndPartiallyAcknowledgedJournalSurvivesRestart}`

## 5. App 侧门禁套件

```
export DEVELOPER_DIR=$HOME/Downloads/Xcode.app/Contents/Developer
xcodebuild test-without-building -project ios/RAI/RAI.xcodeproj -scheme RAI \
  -destination "platform=iOS Simulator,id=5DA14A13-5EFA-4E08-90F4-4B90FAD0A6B9" \
  -derivedDataPath .local/testing/simulator-derived -parallel-testing-enabled NO \
  -only-testing:RAITests/NormalizerTests -only-testing:RAITests/WorkoutListPagingTests \
  -only-testing:RAITests/SourcePolicyGateTests -only-testing:RAITests/WorkoutSyncReuseTests
```

结果：**4 个套件全 PASS** — `Executed 45 tests, with 3 tests skipped and 0 failures`（42 passed）。

3 个 skip 都是需要外部隔离 PG / golden 目录的既有用例：
`NormalizerTests.{testB4SimulatorFullExternalClosure, testM7SharedProductGoldenContract, testVO2SimulatorHTTPPostgresEndToEnd}`。

## 6. R2-F32 — 性能入口口径（真实数字）

两个 perf 用例被 env 门禁 skip（默认不跑，避免污染日常 lane）：

```
RAI_LOCAL_ASSOCIATION_PERF=1 RAI_PHASE4_PERF=after \
  scripts/local/test.sh swift --scratch-path .local/swift-build/capture --filter 'WorkoutPerformanceTests'
```

结果：**PASS** — `Executed 2 tests, with 0 failures`（1.413s / 4.179s）

```
LOCAL_ASSOCIATION_PERF events=10000 keys=1000 elapsed_ms=101.36 environment=macOS_Swift_Debug_synthetic
PHASE4_SWIFT minutes=20  samples=2197  gps=1141  json_bytes=1378707  gzip_bytes=144857  build_ms=13.76  encode_ms=138.43  gzip_ms=15.20  process_peak_rss_bytes=68087808
PHASE4_SWIFT minutes=40  samples=4397  gps=2283  json_bytes=2757810  gzip_bytes=286299  build_ms=26.86  encode_ms=270.59  gzip_ms=27.64  process_peak_rss_bytes=73379840
PHASE4_SWIFT minutes=60  samples=6594  gps=3424  json_bytes=4135099  gzip_bytes=426376  build_ms=39.00  encode_ms=414.91  gzip_ms=40.52  process_peak_rss_bytes=76042240
PHASE4_SWIFT minutes=120 samples=13191 gps=6849  json_bytes=8270561  gzip_bytes=850858  build_ms=82.78  encode_ms=839.18  gzip_ms=84.34  process_peak_rss_bytes=114966528
```

口径必须这样读（用例自己就打印了 scope）：
`environment=macOS Swift package Debug; synthetic; not iPhone`、
`rss_scope=macOS test process; process peak includes earlier tiers; not iPhone`。
即：**这是 macOS 测试进程的峰值 RSS，不是 iPhone 上一次同步的 RSS**，不能当 R2-P12 的设备口径。

捕获阶段的口径与数字见 `R2-normalized-capture.md` 的 R2-P01 专节（`NormalizedCapturePerformanceTests`，
优化构建 `-O`，2000 样本 × 7 次 P50，实测 27.09% → **FAIL**）。

## 7. 逐项结论（映射表）

| 门禁 | 结论 | 支撑（真实执行） |
| --- | --- | --- |
| R2-F04 unsupported metadata / native carrier | **PARTIAL** | App+包侧 PASS：`NormalizerTests.{testBlockedWorkoutMetadataDiagnosticsRedactKeysAndValues, testMetadataIsExplicitAllowlist, testDeviceIdentifiersAndUnreviewedMetadataStayLocal, testR2FutureMetadataStillRetainsOriginalBlockedArchive, testR2OldBlockedNativeArchiveReopensWithoutHealthQueryOrEarlyAnchor, testIncompatibleUnitRefused}`、`WorkoutSourcePolicyTests`、`WorkoutMetadataRegistryTests`；**HTTP/PG 侧未跑** |
| R2-F05 批次截断 / seal / 引用发布失败 | **PASS（既有覆盖，非本轮新增）** | `CaptureSegmentsTests`、`GarbageCollectionTests`（3 项）、`WorkoutCompletenessTests.payloadCorruptionAndStatePublishFailurePreserveAuthority` |
| R2-F06 ACK 丢失 / 本地 ACK 保存失败 | **PASS** | `TestWorkoutReliabilitySwiftClientPostgres/{lost_ack, corrupt_ack, duplicate_post, two_workers}`（真实 PG）+ `WorkoutReliabilityTests.{cancelledDelivery…, stateWriteFailureDuplicateACKAndCleanupFailure, …}` |
| R2-F07 捕获中后到 sample / 子删除 | **PASS** | `CaptureAckBoundaryTests` 3 项（App barrier，pre-fix 复证）+ 服务端 `phase3_child_delete` / `phase3_deleted_parent` |
| R2-F08 父删除与并发上传 / 两种锁顺序 | **PASS** | `TestWorkoutDeleteAndImportBothLockOrders`（delete_first_true/false）、`TestWorkoutBundleConcurrentSnapshotsRetainUnion`、`lost_ack`/`duplicate_post`（旧回执恢复） |
| R2-F09 缺基线 / 未耗尽分页 | **PASS（App 合成）** | `WorkoutListPagingTests` 4 项、`NormalizerTests.testInvalidSecureAnchorNeverQueriesWithNil`、`WorkoutCompletenessTests.{lostACK…NeverAdvanceAnchorsEarly, pendingStreamCannotFinalize…}`、`CaptureConcurrencyTests.testOneStreamPagesSeriallyWithoutSkippingOrCrossingItsAnchor` |
| R2-F28 来源过滤 + 既有删除 | **PASS** | `SourcePolicyGateTests` 4 项（含 `testExistingWorkKeepsCollectingAfterItsSourceChanges`）、`NormalizerTests.{testR3SourcePolicyV2ExcludedInventoryIsEvidenced, testNewSessionNilAnchorReimportsVisibleHKWorkoutAfterOldDeletion}` |
| R2-F29 缺 HR / 室内 / 无功率 | **PARTIAL** | 室内与 allowlist：`NormalizerTests.{testMetadataIsExplicitAllowlist, testInspectedWorkoutMetadataQuantitiesArePreserved}`；功率单位：`testPowerRemainsWatts`；**"workout 完全没有心率样本 → coverage no_records_observed"没有专门用例 → NOT RUN** |
| R2-F30 无变化 / 迟到变化 / 每日健康流 | **PASS** | `WorkoutSyncReuseTests.{testUnchangedRepeatSyncReadsNothingAndPostsNothing, testNewDiscoveryGenerationReopensTheSync, testPreparedBundleRetrySkipsHealthKit}`、`DailyHealthTests` 7 项 |
| R2-F31 v2 两代 / 丢 ACK / 中断续跑 | **PASS** | e2e `TestWorkoutCompletenessSimulatorHTTPPostgres` + `WorkoutCompletenessTests` 9 项（v2 golden/manifest/receipt） |
| R2-F32 性能入口口径 | **PASS（口径）/ R2-P01 FAIL（数字）** | `WorkoutPerformanceTests` 2 项（env 门禁，数字见 §6）、`NormalizedCapturePerformanceTests`（R2-P01 27.09% FAIL） |

## 8. 未完成 / 未验证

- **真机 / 真 HealthKit**：全部 NOT RUN（本会话无真机授权）。
- **R2-F29 的"无心率 workout"场景**：无既有用例，本轮未新增（不为了签 PASS 临时拼测试）。
- **R2-F04 的 HTTP/PG 侧**：未跑，故整体 PARTIAL。
- **R2-P01**：FAIL（27.09%），真机同口径复测 NOT RUN；按 lead 决定不改门禁口径。
- **设备口径 RSS**：没有。唯一 RSS 数字是 macOS 测试进程峰值（§6），明确不可当设备口径。
