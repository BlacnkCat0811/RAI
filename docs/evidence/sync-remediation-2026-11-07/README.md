# 同步链修复证据 — P0 完成、P2 客户端复用被 v2 合同阻塞（2026-11-07）

本目录只记录本次实际执行过的测试。P2 客户端 proof 跨周期复用经真实 e2e 证明与冻结的 v2 合同冲突，已撤回；P1、P2 服务端缓存、P3、P4 未实施，均不视为通过。

## 1. 构建与环境

| 项 | 值 |
| --- | --- |
| 基准 commit | `18eab55`（本轮改动尚未提交，最终测试对象为 2026-11-07 20:55 CST 的工作树） |
| 主机 | macOS 26.7.2 (25G332), x86_64 |
| Xcode | 26.6 (17F113) |
| Swift | 6.3.3 |
| 模拟器 | `RAI Local Tests` (90D0F6B2-E036-4AFD-9AE1-046703F475B1), iOS 26.5 (23F77) |
| 数据 | 仅合成 fixture；未读取真实 HealthKit、未连接生产数据库、未使用真机 |

## 2. 最终结果（撤回 P2 复用后的工作树）

| 命令 | 结果 | 证据 |
| --- | --- | --- |
| `scripts/local/test.sh swift` | PASS：245 tests / 34 suites，0 fail，5 s | `final/swift.log` |
| `scripts/local/test.sh simulator` | PASS：89 passed / 0 failed / 3 skipped，97 s | `final/simulator.log`、`final/simulator-summary.json` |
| `scripts/local/test.sh e2e` | PASS：App→本地 Go HTTP→一次性 PG18，93 s；`contract=v2 manifests=2 streams=12 imports=1 lost_ack=true` | `final/e2e.log` |
| `scripts/local/test.sh gate` | PASS：Go tests/vet/边界 + controller safety，2 s（热缓存） | `final/gate.log` |

`gate` 的脚本输出明确限定范围：不包含 PostgreSQL、iOS 与真机验收；本目录不据此声称这些项通过。`e2e` 使用一次性真实 PG18，但只覆盖完整性 v2 与模型栈两条端到端路径，不等于全量 PG 集成回归。

P0 阶段（P2 尚未实施时）的模拟器日志保留在 `p0-correctness/`，用于对照；最终结论以 `final/` 为准。

## 3. 已完成的 P0 修复与对应测试

### P0-1 multipart coverage 解析

- 调用点：`ios/RAI/App/SyncCoordinator.swift` 的 `coverageFromBundle` 只接受匹配 boundary 的 multipart，并解析 `observation.coverage`；错误 boundary、截断 body、裸 JSON 均返回 nil，由调用方显式失败，不再回退陈旧 `task.coverage`。
- 测试：`ios/RAI/AppTests/WorkoutSyncReuseTests.swift` `testCoverageIsReadFromMultipartFramingOrNotAtAll`（模拟器中执行）。
- 恢复路径回归：`NormalizerTests` 中取消恢复、404 恢复用例为合成 bundle 补齐 `observation.coverage`，模拟器 PASS。

### P0-2 `.unsupported` / `.noRecordsObserved` 完成语义

- `WorkoutCoverage.isSatisfied/needsRetry`、`task.missing`、`pendingReadKeys` 使用同一语义。
- 测试：`ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift` `unsupportedAndEmptyStreamsAreSatisfiedButFailureIsNot`（swift 245 内执行）。
- 未验证：计划要求的 UI 状态文案模拟器断言未单独执行。

### P0-3 per-Workout 完整性故障隔离

- 每个 Workout 独立 `do/catch`，失败写 `blocked` 退避记录，已完成 proof 保留；有 blocked 时不 stage manifest。
- 测试：`NormalizerTests.testBlockedWorkoutKeepsOtherProofsAndRetriesAlone`（3 个 Workout，1 个 `metadata_requires_review`；断言 2 个 proof 保留、无 manifest、无 generation 前进、退避期内零 HealthKit 查询），模拟器 PASS。
- 未验证：计划要求的 200-Workout fixture 未执行。

### P0-4 ACK 清理与回执保留

- `acknowledgeBundle` 清空 `captureManifest`、`bundleDeletionEventIDs`、空 `pendingReadKeys`，保留 `uploadReceipt`、`bundleSHA256`、`coverage`。
- 测试：`WorkoutTaskTests.acknowledgementReleasesCaptureScaffoldingAndKeepsReceiptIdentity`（ACK 后编码字节数下降、`captureManifest == nil`、重复 ACK 返回原回执）。
- 未验证：未断言固定的 2 KiB 上限，只断言相对下降。

### P0-5 无变化发现关联不提交

- `associateDiscoveredFacts` 检测无变化后直接返回；`noopCommitSkipCount()` 可观测计数。
- 测试：`HealthDiscoveryTests.repeatedAssociationWithoutNewFactsDoesNotCommit`。

### P0-6 分析状态有界跟进

- `AnalysisFollowup.delays = [2,5,15,30,60,120,300]`，总预算 ≤ 600 s；完成/失败/删除、取消和后台切换停止；耗尽记录 `analysis_followup_exhausted`。
- 测试：`WorkoutTaskTests.analysisFollowupIsBoundedAndStopsOnFinalPhase`。
- 未验证：未运行“合成服务端先 analysis_not_ready 后 published”的模拟器自动转 published 用例；未测量 10 分钟停止的实际时序。

### 保留的安全优化（不属于 proof 复用）

- 新 cycle 沿用已提交的 `classification/exclusions`：被排除 Workout 不再做 HealthKit 查询或子流读取。
- 无结构变化时只读一次 series 判断是否需要重建；series 未变化不强制新 bundle；`syncWorkout` 后不再重复读 series。

## 4. P2 客户端复用的尝试与撤回

曾实现：新 cycle 以已提交 `proofs/classification/exclusions` 为种子，仅查询根流，子流用上一周期 anchor 生成 no-op 分页补齐 12 个 contract stream，ACK 合并未发送流的 anchor，测试中也相应断言第二轮 `committedAnchor=1`。

真实 `scripts/local/test.sh e2e`（App→Go HTTP→PG18）在第二轮完整性提交失败：

```
UploadError(code: "invalid_completeness", status: 422, retryAfter: nil, field: nil, reason: nil)
```

证据：`contract-blocker/v2-proof-reuse-422-summary.json`、`contract-blocker/v2-proof-reuse-422-summary-first-attempt.json`、`contract-blocker/v2-proof-reuse-422-xcode.log`。

合同冲突原因（服务端代码为冻结契约）：

- `apps/server/internal/syncing/workout_completeness_v2.go` 要求每个 eligible Workout 的根、10 个 quantity、route 共 12 个 stream 全部存在，且每个 proof series 的 `queryCompletedAtUs >= knownCutoffUs`。
- 同一文件要求每个 stream 分页的 `queryStartedAtUs >= knownCutoffUs`。
- `apps/server/internal/storage/postgres/workout_completeness_v2.go` 要求新 `knownCutoffUs` 严格大于上一周期。
- 因此“复用上一周期 proof 时间戳”与“本周期 cutoff 必须前进”不能同时成立；仅补 no-op 分页仍会因 proof series 时间过旧被拒绝。

处理：撤回 proof 种子、`clearCompletenessProof`、carried no-op 分页与 ACK 合并 anchor 逻辑；新 cycle 重新观察全部 12 个流。测试改为 `WorkoutCompletenessTests.newCycleRequiresEveryChildStreamBeforeStage`（根流捕获后 stage 失败，补齐 11 个子流与 proof 后才成功），并把增量预期恢复为第二轮 `committedAnchor=12`、12 个 anchors。撤回后 e2e PASS（`final/e2e.log`）。

结论：P2 客户端 proof 复用当前不可交付；要满足计划 §8.2，必须先由服务端提供 proof 缓存/v3 携带语义或调整冻结合同，不能靠客户端单方面跳过观察。

## 5. 验收矩阵状态（仅针对本次证据）

| ID | 状态 | 说明 |
| --- | --- | --- |
| A-01 | PASS | 单元 + 模拟器恢复路径 |
| A-02 | PARTIAL | 单元语义 PASS；UI 文案模拟器断言未执行 |
| A-03 | PARTIAL | 3-Workout 隔离 PASS；200-Workout fixture 未执行 |
| A-04 | PARTIAL | ACK 清理、原回执幂等 PASS；固定字节上限未断言 |
| A-05 | PASS | 第二次关联 0 提交 |
| A-06 | NOT RUN | 未运行上传幂等 PG 集成 |
| A-07 | NOT RUN | 未运行删除优先 PG 集成 |
| A-08 | NOT RUN | P1 SQLite 未实现（仍是 `state.json`） |
| A-09 | NOT RUN | P2 服务端 proof 缓存未实现；客户端复用已被 v2 合同阻塞并撤回 |
| A-10 | PARTIAL | 有界退避单元 PASS；合成服务端时序与 10 分钟停止未执行 |
| A-11 | NOT RUN | 未执行 |
| A-12 | NOT RUN | P4 ETag/304 未实现 |
| B-01..B-17 | NOT RUN | 未建立 Phase 0 计数器与 200-Workout 性能基线；P1/P3/P4 未实现，不能推断 |

里程碑：M1 要求 A-01～A-06、A-10 全部通过；A-02/A-03/A-04/A-10 仅 PARTIAL，A-06 未运行，因此 M1 未达成。M2～M5 未开始或未验证。

## 6. 未实现/未验证（不得视为通过）

- P1：SQLite 本地持久化、写放大治理、迁移、裁剪/压缩/GC 全部未实现。
- P2 服务端：proof 验证缓存、锁外准备、raw payload 热路径治理未实现；本轮复用尝试已撤回。
- P3：锁预算、上传准入、Worker 分道未实现。
- P4：changefeed、ETag/304、`cache.sqlite`、首页/详情请求收敛未实现。
- Phase 0：计数器与固定 200-Workout 负载未建立，B 类指标没有测量数据。
- 真机、`go test -race -tags=integration` 全量 PG 回归、部署验收均未在本轮执行。

## 7. 复现命令

```bash
scripts/local/test.sh swift
scripts/local/test.sh simulator
scripts/local/test.sh e2e
scripts/local/test.sh gate
```

证据日志来自上述命令的原始 stdout/stderr；`simulator-summary.json` 由 `xcrun xcresulttool get test-results summary` 从对应 `.xcresult` 导出。
