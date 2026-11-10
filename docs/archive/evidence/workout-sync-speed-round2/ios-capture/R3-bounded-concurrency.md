# R3 有界并发（iOS 捕获侧）

状态：**PASS**（合成数据 + 模拟器，12 条测试全部真实执行通过，1/2/4 数字已实测）。
真机 / 真 HealthKit：**NOT RUN**（本会话无真机授权）。

构建口径：模拟器 lane（Debug，`RAI Local Tests` / iPhone 12 Pro Max / iOS 26.5 x86_64，
derived `.local/testing/simulator-derived`）+ SwiftPM 包测试（Debug）。
数据全部合成，无真实健康值、无设备标识。

## 1. 要求与实现对应

| 要求（§3 R3 / R2-F10..F15 / R2-P02） | 实现位置 |
| --- | --- |
| 独立 quantity type 之间并发 | `SyncCoordinator.performSync` 用 `withThrowingTaskGroup` 对 `HealthKitClient.quantityTypes` 起子任务 |
| 同一流的分页/anchor 必须串行 | 一条流 = 一个 `captureQuantityStream` 生产者；流内读取、anchor 判定、cursor 固定都是顺序语句 |
| 全局 capture 预算（含 series/route） | `CaptureGate`（`streams` 槽 + `queueBytes` 字节）包住整条流（sample 读 + series）以及 route 流 |
| producer→writer 队列 2MiB 字节背压 | `CaptureWriter.append` 先 `gate.reserve(bytes:)`（按批字节数），写完 `release`；`CaptureConcurrencyPolicy.standard.queueBytes == 2 << 20` |
| Outbox 单写者 | 只有 `CaptureWriter` 调 `store.captureFactSegment`，它是 actor，天然串行；`performSync` 不再直接写段 |
| 取消时释放槽与预算 | `captureQuantityStream`/`captureRouteStream` 的 `catch { await gate.exit(); throw }` + 正常路径 `exit`；`CaptureGate.cancel()` 唤醒所有 waiter 抛 `CancellationError` |
| merge 按固定顺序，不受完成先后影响 | 子任务结果进 `outcomes` 字典，随后**按 `HealthKitClient.quantityTypes` 顺序**合并 |
| 默认并发度 2，实测 1/2/4 | `CaptureConcurrencyPolicy.standard.streams == 2`；`testMeasuredConcurrencyForOneTwoAndFourStreams` 实测并打印 |
| Swift 6 严格并发不关、无全局 `@unchecked Sendable` | `Package.swift` 的 `swiftLanguageModes: [.v6]` 未改；新增类型全部 `Sendable` 值类型，`CaptureGate`/`CaptureWriter` 是 actor |

## 2. 新增/改动文件

- 新增 `ios/RAI/Sources/SyncCore/CaptureConcurrency.swift`：`CaptureConcurrencyPolicy`、`CaptureGate`、`CaptureWriter`。
- 改 `ios/RAI/App/SyncCoordinator.swift`：`captureStreams` 存储属性与 `init(captureConcurrency:)` 尾参；`performSync` 的 quantity 循环改成任务组 + 固定顺序合并；route 阶段改走 `captureRouteStream`；`factsForBundle`/`freshFacts`/`code` 标 `nonisolated`；新增 `QuantityStreamOutcome`/`RouteStreamOutcome`、`captureQuantityStream`/`captureQuantityBody`/`captureRouteStream`/`captureRouteBody`；新增 timings `capture_max_active_streams`、`capture_stream_limit`、`capture_peak_queue_bytes`、`capture_writer_batches`、`capture_writer_records`。
- 新增测试 `ios/RAI/Tests/SyncCoreTests/CaptureGateTests.swift`（7 项）、`ios/RAI/AppTests/CaptureConcurrencyTests.swift`（5 项）。

## 3. 测试清单（恢复测量后逐条记录 Passed/Failed）

| 测试 | 断言什么 | 结果 |
| --- | --- | --- |
| `CaptureGateTests.policyDefaultsMatchTheChosenConcurrency` | 默认 2 流 / 2MiB，且不会退化成 0 | PASS |
| `CaptureGateTests.streamSlotsAreBoundedAndHandedOver` | 槽满时第 3 个生产者挂起、`maxActive` 不超过策略、`exit` 后交接 | PASS |
| `CaptureGateTests.byteBudgetBackpressuresTheProducer` | 字节预算满时生产者挂起，`release` 后放行 | PASS |
| `CaptureGateTests.oversizedReservationIsClampedToTheBudget` | 单批超预算仍能前进（钳到预算）而不是永久等待 | PASS |
| `CaptureGateTests.cancellationReleasesEveryWaiterAndTheSlots` | `cancel()` 让全部 waiter 抛 `CancellationError`，持有者 `exit` 后 active=0 | PASS |
| `CaptureGateTests.theWriterIsTheOnlyCallerAndSerializesSegments` | 4 个并发生产者 → 4 个独立段、20 条事件、generation=20、预算归零 | PASS |
| `CaptureGateTests.anEmptyBatchWritesNothing` | 空批不写盘、不占预算 | PASS |
| `CaptureConcurrencyTests.testMeasuredConcurrencyForOneTwoAndFourStreams` | 1/2/4 的实测 peak active 与 timings 一致 | PASS |
| `CaptureConcurrencyTests.testFailingStreamReleasesItsSlotForTheOthers` | 单槽下某类型读失败后其余类型仍完成并交付 | PASS |
| `CaptureConcurrencyTests.testCancellationStopsAParkedCaptureStream` | 取消挂起的流不悬挂；同 store 新协调器仍能满预算同步 | PASS |
| `CaptureConcurrencyTests.testMergeOrderIsIndependentOfCompletionOrder` | 串行（1）与并发（4）的 samples/series/routes 载荷字节一致 | PASS |
| `CaptureConcurrencyTests.testManualEntryDiscoveryThenSyncStaysNormalized` | 真实手动入口先 discovery 再 sync：10 类型复用、归档计数 0、并发 2 | PASS |
| `CaptureConcurrencyTests.testOneStreamPagesSeriallyWithoutSkippingOrCrossingItsAnchor` | 同类型多页：anchor 顺序 `nil → A1`、不跳页不重复、同类型 peak active == 1 | PASS |

## 4. 并发度实测数字（1/2/4）

口径：同一 fixture（2 个 heartRate + 1 个 distance 合成样本，10 个 quantity 类型全部读取），Debug，模拟器 lane；
`peak` 来自合成 reader 的 barrier（前 `streams` 个生产者互相等待才放行，读到第 `streams` 个同时在内才继续），
`gate` 来自 `WorkoutSyncTask.metrics["capture_max_active_streams"]`（`CaptureGate` 自身统计）。两者必须一致才算通过。

`testMeasuredConcurrencyForOneTwoAndFourStreams` 原始输出：

```
R3 measured capture concurrency (streams -> peak active): [1: 1, 2: 2, 4: 4]
R3 measured peak queue bytes (streams -> bytes): [1: 1572.0, 2: 1572.0, 4: 2370.0]
R3 measured wall clock ms (streams -> ms): [1: 87.69, 2: 58.04, 4: 63.57]
```

| streams | probe peak active | gate maxActive | peak queue bytes | 墙钟 (ms) | 备注 |
| --- | --- | --- | --- | --- | --- |
| 1 | 1 | 1 | 1572 | 87.69 | 串行基线 |
| 2 | 2 | 2 | 1572 | 58.04 | **默认值**，本 fixture 最快 |
| 4 | 4 | 4 | 2370 | 63.57 | 无额外收益（只有 2 个类型有数据） |

读法（如实）：

- **并发确实发生**：peak active 与请求的流数一一对应（1/2/4），说明槽位真的并行持有，不是串行伪并发。
- **默认 2 的依据**：本 fixture 只有 2 个类型有样本，2 流已能覆盖；4 流多出的槽没有可并行的工作，
  反而让 10 条流抢 4 个槽增加了调度与写段交错（墙钟略升）。真机 10 个 quantity 类型都有数据时
  4 流可能更好，但**本会话无真机，不能下这个结论** → 默认保守取 2。
- **2MiB 背压未被触发**：fixture 每批只有 KB 级，peak queue bytes 远小于 2 MiB（1572 / 2370）。
  背压本身由 `CaptureGateTests.byteBudgetBackpressuresTheProducer` 与
  `oversizedReservationIsClampedToTheBudget` 用受控预算直接验证（挂起 → release → 放行）。
- **墙钟抖动大**（87.69 / 58.04 / 63.57），只作记录，不作断言、不据此声称提速。

## 5. 其它验收项的真实证据

- **不同完成顺序 → 字节相同**：`testMergeOrderIsIndependentOfCompletionOrder` 用**同一份 fixture 实例**
  跑 streams=1（顺序完成）与 streams=4（乱序完成），逐键比较 `samples`/`series`/`routes` 的 UTF-8 字节，
  全部相等（781 bytes 对 781 bytes 等）。此前一次"875 vs 875 不等"的失败已定位为**测试自身**每次新建
  `HKQuantitySample` 导致 UUID 不同，改为复用同一 fixture 后通过——merge 顺序本身从未有问题。
- **取消后 active=0 且预算释放**：`testCancellationStopsAParkedCaptureStream` 用
  `withTaskCancellationHandler` 让某类型真的挂在读取里（`XCTestExpectation` 确认"确实挂住了"，
  不靠 sleep），取消后断言流结束、`gate` 的 active 归零、预算归零，并用同一个 store 起新协调器
  再做一次完整同步成功（证明没有被泄漏的槽卡住）。
- **失败隔离**：`testFailingStreamReleasesItsSlotForTheOthers` 单槽下某类型抛 `synthetic_read_failure`，
  其余类型仍完成并交付（该类型 coverage 记 failed，不污染其它类型）。
- **同类型多页不跳页不串 cursor**：`testOneStreamPagesSeriallyWithoutSkippingOrCrossingItsAnchor`
  用 `PageProbe` 让 heartRate 返回两页（anchor nil → 1 条 + anchor "A1" + isFull；anchor "A1" → 0 条 + "A2"），
  断言 anchor 序列为 `["-", "A1"]`（不跳页、不重复 anchor）、每个类型内 anchor 序列无重复、
  且 heartRate 的 peak active == 1（同类型从不并发读）。
- **真实手动入口**：`testManualEntryDiscoveryThenSyncStaysNormalized` 走真实 `discoverChanges()`（前置 discovery）
  再 `syncWorkout`，断言 `reused_discovery_reads == 10`、`full_sample_reads == 0`、
  `sample_archive_calls == 0`、`sample_unarchive_calls == 0`、`capture_stream_limit == 2`。

## 6. 未完成 / 风险

- 真机：NOT RUN（本会话无真机授权，lead 侧汇总）。
- 4 流在真机（10 个类型都有数据）是否优于 2 流：**NOT VERIFIED**，故默认保守取 2。
- route 流的字节预算：route 点数据由 `HealthIntegrity.RouteCaptureFile` 自己分批落盘，`CaptureGate` 只给它流槽（计入"并发 capture 预算"），没有额外按字节预留；这一点在汇报里如实说明。
- `normalized_capture_ms` 现在是各流 capture 耗时之和（流间重叠），不是墙钟；墙钟看 `healthkit_read_ms`。
