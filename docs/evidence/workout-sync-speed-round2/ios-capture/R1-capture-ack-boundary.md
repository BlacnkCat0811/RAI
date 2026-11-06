# R1 — 捕获/ACK 确认边界（**R2-F07**）

状态：**PASS**（合成数据 + 模拟器，真实 `performSync` + `uploadAndAcknowledge` 路径）。
真机/真 HealthKit：**NOT RUN**（本会话无真机授权）。

门禁编号说明：本项对应计划 §9 的 **R2-F07**（"捕获中后到 sample / 子删除 → 不把未入包的变化标为 delivered；重启后继续交付"）。
先前版本误标为 R2-F19（gzip bomb / CRC / 截断 / 串联成员 / 尾部垃圾），已更正。
与 **R2-F05** 的关系：本项**未**覆盖 seal 写入或引用发布失败的相关性（那是 segment seal / publish 失败路径，由 `CaptureSegmentsTests` 与 R2 的 segment 测试覆盖），故不并入门禁签核。

## 1. 先证明的现状缺陷（pre-fix 实测，不是推测）

旧实现的水位规则是：组包结束时再读一次当前最大 generation，把它当作 `discoveryGeneration` 写进 task；ACK 时把 `generation <= 该水位` 的事件全部标 `delivered`。
组包过程中提交的事实（捕获窗口内发生的迟到样本、子删除）因此被算进水位，**但它们从未进包**。

用 `ios/RAI/AppTests/CaptureAckBoundaryTests.swift`（可注入的假 `HealthKitReading` + 真实 `OutboxStore` + `CheckedContinuation` barrier，无 sleep）在修复前跑出的原始失败：

```
testLateSampleDuringCaptureStaysPendingAfterAck
  ("delivered") is equal to ("delivered") - ACK confirmed a sample the bundle never carried
  ("3") is not greater than ("3") - the delivered watermark covered an undelivered fact
  第二遍 sync: uploads == 0  - the undelivered fact was skipped as unchanged
testChildDeletionDuringCaptureStaysPendingAfterAck
  ("delivered") is equal to ("delivered") - ACK confirmed a deletion the bundle never carried
  第二遍 sync: uploads == 0 - the undelivered deletion was skipped as unchanged
testParentDeletionDuringCaptureRefusesTheAck
  PASS（pre-fix 就通过）
```

结论分三条，必须分开看：

| 场景 | 现状（修复前） | 依据 |
| --- | --- | --- |
| ① 迟到 sample（捕获窗口内提交的新样本） | **真实缺陷**：被错标 delivered，且下一轮 `isAlreadyDelivered` 直接跳过同步 → 该样本永久丢失 | `testLateSampleDuringCaptureStaysPendingAfterAck` 失败，第二遍 uploads == 0 |
| ② 子删除（样本被删） | **真实缺陷**：删除事件被错标 delivered，服务端永远不会收到这次删除 → 样本复活 | `testChildDeletionDuringCaptureStaysPendingAfterAck` 失败，第二遍 uploads == 0 |
| ③ 父删除（整个 workout 被删） | **现状本已正确**：`OutboxStore.claimBundle` 有 `discovery.work[id].pendingDelete != true` 守卫，`WorkoutBundleDelivery.deliver` 返回 false 时不发请求、不 ACK | `testParentDeletionDuringCaptureRefusesTheAck` pre-fix 通过 |

即：R2-F07 的缺陷存在于"事实级"（sample / 子删除），不存在于"父级删除"；父级路径的守卫是既有正确行为，本次只是把它纳入回归测试。

> 上面的 pre-fix 输出是**当前版本测试**在把 `setBundle(manifest: nil)`（回退到旧水位规则）后重跑得到的，不是旧稿遗留记录：R3 并发化后迟到样本的提交序可能早于该类型的读取，这条 pre-fix 失败仍然复现，说明测试仍然真的咬住缺陷。

## 2. 修法（只改确认边界，不改传输/服务端契约）

- 新增 `ios/RAI/Sources/SyncCore/CaptureManifest.swift`：不可变、带版本号的 `CaptureManifest`（`version = 1`），记录本次组包**真实消费**的身份：
  - `coveredEventIDs` — 本次包内真实编码的事实事件 id（含 root workout 事件）；
  - `deletionEventIDs` — 本次包内显式提交的删除事件 id；
  - `cursors: [type: ordinal]` — 每个 producer 本次成功消费到的最高 ordinal（**证据**，记录"这个流编到哪"，不是覆盖判据）；
  - `generation` / `coveredFacts` / `completeTypes`。
  - `covers(_ event:)` 是 ACK 的唯一判据：删除只认 `deletionEventIDs`，事实只认 `coveredEventIDs`，其余一律未覆盖。
- **为什么不用 `ordinal <= cursor` 判定覆盖**（本轮实测发现）：`ordinal` 是**全局提交序**，同类型的一个事实可以在本包读取的事实之前提交、却不在本包里（样本在查询飞行途中被写入）。用 cursor 比较会把这种事实一并确认，正是本项要关掉的缺陷。因此 cursor 只作证据保留，覆盖判据改为**逐事件身份**。`CaptureManifest.covers` 的文档注释记录了这条理由。
- `DiscoveryEvent` 新增 `ordinal: Int64?`，`DiscoveryState` 新增单调 `sequence`，在 4 处 generation 赋值点同时发 ordinal（stage / recordCapturedFacts / associateDiscoveredFacts / publishCapturedFacts）。ordinal 是**提交序**，与完成先后无关。
- `WorkoutSyncTask` 新增 `captureManifest: CaptureManifest?`（Optional，旧 state.json 仍可解码）。
- `OutboxStore.acknowledgeBundle`：按 manifest 逐事件判定；被覆盖的才标 `delivered`，未被覆盖的**压低水位**到 `lowestUncovered - 1`，并清 `work.error`。`pendingReadKeys` 非空时行为不变（整体拒绝 ACK，`partial_read_pending`）。
- `SyncCoordinator.performSync`：每个类型流按"本次实际消费的 factKey 集合"在 journal 中定位到对应事件，收进 manifest 的 `coveredEventIDs`，并把最高 ordinal 记为 cursor；组包末尾用这些身份构造 manifest，不再用"末尾最大 generation"当水位。
- `isAlreadyDelivered` 增加第二重守卫：只要该 workout 还有未 `delivered`、也未被 `suppressed_by_workout_delete` 的事件，就不允许 skip。

未改：`Wire.swift` / `RAIAPIClient.swift` / `WorkoutBundleDelivery.swift`（ios-transport 领地）、`apps/server/**`、`Package.swift`。

## 3. 验证（真实执行）

构建口径：模拟器 lane（Debug，`RAI Local Tests` / iPhone 12 Pro Max / iOS 26.5 x86_64，derived `.local/testing/simulator-derived`）。

```
cd /Users/ta/RAI && scripts/local/test.sh simulator
LOCAL CHECKS: PASS profile=simulator elapsed_seconds=102
xcresult summary: passedTests 85, failedTests 0, skippedTests 3
（3 个 skip 是需要外部隔离 PG 的既有用例：
  testB4SimulatorFullExternalClosure / testVO2SimulatorHTTPPostgresEndToEnd / testR3SimulatorHTTPPostgres）
```

| 验收项 | 测试 | 结果 |
| --- | --- | --- |
| 迟到 sample 不得被 ACK 覆盖；下一轮必须重传并交付 | `CaptureAckBoundaryTests.testLateSampleDuringCaptureStaysPendingAfterAck` | PASS |
| 捕获中的子删除不得被 ACK 覆盖；下一轮作为显式删除提交 | `CaptureAckBoundaryTests.testChildDeletionDuringCaptureStaysPendingAfterAck` | PASS |
| 捕获中的父删除必须拒绝 ACK，并转为 DELETE | `CaptureAckBoundaryTests.testParentDeletionDuringCaptureRefusesTheAck` | PASS |
| 包测试无回归 | `scripts/local/test.sh swift --scratch-path .local/swift-build/capture` | PASS — `Test run with 234 tests in 31 suites passed after 4.948 seconds` |

测试断言的关键量（合成数据，无健康值）：
- 迟到样本：`status == "pending"`、`work.generation > work.deliveredGeneration`、`manifest.coveredFacts == 2`、`manifest.coveredEventIDs.count == 2`（只列真实进包的两条事实）；第二遍 `uploads == 1` 且该事件转 `delivered`、`generation == deliveredGeneration`。
- 子删除：`manifest.deletionEventIDs.count == 0`、`generation > deliveredGeneration`；第二遍 `submittedSampleDeletions == 1`、事件 `delivered`。
- 父删除：第一遍 `uploadReceipt == nil`、`pendingDelete == true`；第二遍发出 DELETE、`phase == .deleted`。

## 4. 顺带发现的两条真实行为（已写进测试注释，供后续阶段注意）

1. **partial 读会整体拒绝 ACK**：`pendingReadKeys` 非空时 `acknowledgeBundle` 走 `partial_read_pending`，一个事件都不确认。所以"读成 partial"不是部分交付，而是零交付 + 退避。
2. **upsert 与 delete 共享 `type + uuid` 但身份不同**：查 journal 状态必须同时按 `deleted` 过滤，否则会读到另一半。测试 helper 已按此实现。

## 5. 与 R3 的相互作用（本轮实测）

R3 让独立类型并发后，"在某类型暂停时向**另一个**类型注入迟到样本"不再是确定的"迟到"场景（另一类型可能在该注入之后才开始读，从而合法地把样本带进包）。测试因此改为**暂停被注入类型自身**的读取：该流的 fact 列表在暂停点之前已固定，注入的事实不可能进包，场景与断言重新变得确定。这条改动只影响测试编排，不影响 R2-F07 的结论。

## 6. 未完成 / 未验证

- 真机 + 真 HealthKit 的同类边界（需要真机授权）：**NOT RUN**。
- 与真实 PostgreSQL 后端的端到端（含服务端对迟到删除的处理）：**NOT RUN**（lead 要求等 PG 性能任务结束再跑）。
- 未测量本阶段性能影响（R1 只加了一次 journal 读 + 事件遍历，R2 会给出规范化/持久化阶段的前后对比）。
