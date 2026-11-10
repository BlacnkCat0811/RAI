# UA1-R3B Result — Source Policy & Eligible Inventory Contract

状态：**BLOCKED**（工程实现与合成/服务器侧验证完成；真机 R3 v2 验收因设备不可用未执行，用户当次授权跳过）

本阶段回答的唯一问题是：**可见 HealthKit Workout inventory ≠ RAI eligible Workout inventory** 时，completeness 如何仍然可证明。答案是 reviewed contract 变更：新增 `workout-sync-completeness/v2` 与冻结的 source policy 身份，把 inventory 从「每条可见 Workout 都要 receipt」改为「每条可见 Workout 都必须被确定解释为 eligible 或 explicitly excluded」。v1 语义未被原地修改。

## 1. Git Baseline

- 分支 `main`，HEAD `b826b36ac986ed9ea57bf7c7e7c42cdb58380ad6`（`feat(ua1): add workout sync completeness, VO2 native metadata and unified acceptance`）。
- 工作树同时包含 UA1-R3A 与本阶段未提交改动（`dirtySource: true`）；本阶段**未** commit、未 push、未部署生产、未切换 legacy 入口、未修改 `../running`。
- 隔离云测试环境沿用 UA1 的既有环境与数据血统（同一 host、同一 dataset/collector、`activeStack = legacy`）。

## 2. Source Policy Decision

冻结身份：`rai.workout.source_policy.apple_watch_only/v1`，classification version `1`，policy 文档哈希 `63713b4f82a33c6e9544de37fbd0701c42a29b71f2ca54588c7fc9cf463e0654`（[fixture](../contracts/fixtures/workout-source-policy-v1.json) · [合同](../contracts/workout-source-policy-v1.md)）。

| classification | outcome | reason |
| --- | --- | --- |
| `apple_watch` | eligible | `eligible_source_policy` |
| `third_party` | excluded | `excluded_by_source_policy` |
| `user_entered` | excluded | `excluded_by_source_policy` |
| `associated_apple_watch` | unclassified | `unclassified_source` |
| `source_unknown`（含未列入表的一切） | unclassified | `unclassified_source` |

- `user_entered` 为确定性分类：来源 bundle 恰为 Health 应用自身、无 productType、device 描述全空（具体标识符只存在于受限 fixture 与服务器证据中）。
- 未放宽既有 apple_watch 判定：仍要求 watch productType 形态、Apple manufacturer、watch model（scoped bundle 时 manufacturer/model 必须存在）。
- unclassified 不是 excluded：任何未分类条目都使 completeness BLOCKED。

## 3. Visible / Eligible / Excluded Inventory

三层定义与不变量：

```
visible = eligible + excluded + unclassified
```

且提交前必须 `unclassifiedCount == 0`、`pendingClassificationCount == 0`。

- 可见 inventory 来自未按来源过滤的 anchored discovery（查询层不预先剔除第三方）。
- eligible 才进入 Protocol 3 capture / samples / route / associations / M1 / M2 / M3 / M6 load domain。
- excluded 不产生 Protocol 3 receipt、不上传 samples/route、不产生分析任务，但必须产生 source-policy exclusion evidence。

真实 inventory 本阶段**未能重新读取**（真机不可用，见 §11）。最后一次真实观测为 UA1-R3A：visible 44 / apple_watch 31 / third_party 13 / unclassified 0（[inventory.json](../evidence/acceptance/device/ua1-r3b-20261105/inventory.json)，已标注「本阶段未复核」）。

## 4. Completeness V2 Contract

`workout-sync-completeness/v2`（[合同](../contracts/workout-completeness-v2.md)）在 v1 wire facts 之上新增：

- 版本身份：`sourcePolicyVersion`、`sourcePolicySHA256`、`classificationVersion`。
- `inventory`：`visibleCount / eligibleCount / excludedCount / unclassifiedCount / pendingClassificationCount` + `visibleSHA256 / eligibleSHA256 / excludedSHA256`。
- `exclusions[]`：每条 `workoutIdentitySHA256 / classification / outcome / reason / classificationVersion / sourcePolicyVersion / evidenceSHA256 / sourceEvidence`。
- `workouts[]` 语义收窄为 **eligible 集合**（每条仍带 bundleSHA256、routeState、10 条 series）。
- `notApplicableStreams` 仅在 eligible 为空时列出 quantities+route。

不是 count-only：inventory 身份以有界哈希集合给出（`workoutIdentitySHA256 = sha256(lowercase UUID)`，集合摘要对排列不变），服务器因此能知道**是哪几条**被排除，而无需公开 UUID。

## 5. Versioning

- 新增 `workout-sync-completeness/v2`；v1 合同、v1 错误码、v1 行语义、v1 receipt 保持字节不变并继续被接受（同一 endpoint，按 body 内 `contract` 分派，未知 contract → 422 `unsupported_version`）。
- `streamSetVersion` 保持 `1`：stream 集合本身未变；语义变化由 `protocolVersion 2` + `sourcePolicyVersion` 表达，不借用 streamSetVersion。
- anchor 域分离：`CompletenessFingerprintV2` 以 v2 contract 字符串前缀，v1 anchor 不可能被 v2 复用。
- 服务端版本身份进入 manifest 校验、行约束（`protocol_version`、policy 列）、change_events payload 与 receipt。

## 6. State Migration

- 客户端：`beginWorkoutCompleteness(contract:)` 在存在未提交 cycle 且 contract 不同时执行显式迁移 —— 保留 committed anchors 与 inventory，丢弃未提交的 page/proof/classification 状态，重播种 inventory；提交幂等、可恢复，旧 cycle 的 contract 为 nil 时仍按 v1 处理。
- 服务器：prior 行 `protocol_version != 2` 时拒绝（409 `manifest_generation_conflict`），v1 血统不会被静默重解释为 v2。
- 已捕获文件不删除、不重编码、不改内容；v2 只建立新引用。
- R3A 保存状态（generation 0、committed anchors 0、manifest none）原样保留，未被删除。

## 7. Swift

- 新增 `Sources/SyncCore/WorkoutSourcePolicy.swift`（身份、outcome 表、identity/inventory 哈希、exclusion evidence 构造）。
- `WorkoutCompleteness.swift`：v2 staging（eligible-only streams、exclusions、inventory 摘要、未分类拒发）、双 contract ack（v2 receipt 必须复述 policy 身份、四个计数与三个摘要）、`completenessClassify`。
- `SyncCoordinator`：能力协商选择 v2，per-Workout 分类，excluded 直接 `continue`（不读子流、不上传、不建分析任务），unclassified 抛错。
- `swift test`：**163 tests / 23 suites 全绿**；含跨语言 golden（Swift 规范编码复现 fixture 摘要与 v2 fingerprint）。
- 模拟器 App XCTest：**60 例 / 57 通过 / 0 失败 / 3 显式外部环境跳过**；新增 `testR3SourcePolicyV2ExcludedInventoryIsEvidenced` 断言「2 可见 = 1 eligible + 1 excluded、excluded 无任何 bundle 上传、evidence 可被重新分类」。

## 8. Go / Server Validation

- `gofmt` clean、`go mod verify`、`go vet ./...` 与 `-tags=integration` clean、`go build ./...` ok、`go test -count=1 ./...` 全绿、`sqlc generate` 仅预期模型列变化、依赖边界检查 PASS、OpenAPI lint PASS（0 error / 36 warning）。
- 隔离 PG18 `-race -tags=integration`：`ok rai/server/integration` **1021.528s**（`-timeout 40m`；默认 10m 在该 host 上不足，属既有环境事实，R3A 门禁日志同样如此，非产品超时变更）。
- 服务器侧重验（v2 事务内）：contract/版本、policy 身份与摘要、id/摘要/时间边界、inventory 不变量、`visibleSHA256` 与 page-chain inventory 一致、`eligibleSHA256` 与已入库 running Workout 集合一致、每条 exclusion 用上传的 canonical source evidence **重跑同一冻结分类器**并校验 `evidenceSHA256`、stream key/fingerprint/page 链、`verifyCompletenessWorkout`（receipt、commit_rev、tombstone、stream coverage、series 摘要）、fact revision、immutable receipt、anchor 仅在 ACK 后推进。
- 不信任 `excluded=true`：无 evidence 或 evidence 重分类不符 → 422 `exclusion_evidence_invalid` / `exclusion_policy_mismatch`。
- 合成矩阵覆盖：31/13、全 eligible、全 excluded、third-party-only、无可见 Workout、classification change、unclassified 存在即拒绝、policy 版本不匹配、inventory 计数篡改、丢失 exclusion。

## 9. PostgreSQL

- 加法迁移 `00029_workout_completeness_v2.sql`（编号未硬编码，由 `migrations.LatestVersion()` 推导；`RequiredSchemaVersion` 28 → 29）：新增 `protocol_version`、`source_policy_version`、`source_policy_sha256`、`classification_version`、`inventory_summary`、`exclusions`，并加 `workout_completeness_protocol_version`、`workout_completeness_v1_identity`（v1 行 policy 列必须全 NULL）、`workout_completeness_v2_identity`（v2 行 policy 列非空、摘要格式、inventory/exclusions 有界）约束。
- 沿用既有不可变触发器（UPDATE/DELETE → `RA045`）与 `rai_workout_completeness_dirty()`。
- 不新建 `eligible_workouts_copy` / `excluded_workouts_copy`：exclusion 作为不可变 manifest 行的有界 jsonb 保存。
- 云环境实测：schema 28 → 29，v2 列 6 个，权限 `apiInsert=true / apiUpdate=false / workerSelect=true / workerInsert=false`，`activeStack=legacy`。
- 集成测试覆盖：v2 提交、lost-ACK 重放返回同一 receipt、page/inventory 冲突、policy mismatch、evidence 无效、v1 血统拒绝、行不可变、collector fencing、角色权限。

## 10. Simulator

- 合成矩阵套件：60 例 / 57 通过 / 0 失败 / 3 跳过（跳过项均为需要外部一次性 PG18 host 的既有 E2E）。
- **Simulator → 真实 Go HTTP → 真实 PG18 v2 E2E PASS**：客户端以 v2 提交 manifest，服务器校验通过并落库；PG 中 `workout_sync_completeness` 恰有 2 行（generation 1、2），均为 `protocol_version = 2`、`source_policy_version = rai.workout.source_policy.apple_watch_only/v1`、`classification_version = 1`，`inventory_summary` 五计数与三摘要齐备，`exclusions` 为空数组。

## 11. Real-Device Resume

**未执行。** 真机（唐攀的iPhone，iPhone 12 Pro Max / iPhone13,4）在 `devicectl` 中状态为 `unavailable`：`device info details` 可读，但 `device install` 与 `lockState` 均返回 CoreDevice error 1011，无法安装、启动与取证。已完成的准备：device 标识从陈旧 `ecid_…` 刷新为 coredevice UUID；`generic/platform=iOS` 构建成功（RAI.app 已产出）；云验收环境已升级到 schema 29 且 handshake 广告 v2 能力。用户当次决定「先跳过真机，仅完成其余交付」。

因此本阶段不宣称真机恢复、真机 completeness、真机 lost-ACK 或真机第二增量成立。

## 12. Full Eligible Completeness

**未验证（真机侧）。** 合成侧已证明：31 eligible / 13 excluded / 44 visible 的 v2 manifest 结构合法，inventory 不变量成立，excluded 条目必须携带可重分类 evidence；但真实 31 条 eligible 的 facts/streams 全量补齐未在真机上执行，因此「真实 eligible-workout completeness verified」不能宣称。

## 13. Second Incremental

- 合成侧已验证：第二次 sync 复用 committed anchors，`bootstrapQueries = 0`，generation 1 → 2，客户端 bundle 读取次数不变，服务器两代 manifest 并存且均不可变。
- 真机第二增量：**未执行**。

## 14. Manifest Lost ACK

- 服务器侧：同 `manifest_id` 同字节重放返回**同一逻辑 receipt**（集成测试断言）。
- 客户端侧：`WorkoutCompletenessDelivery` 在 ACK 丢失时保留 pending，重试同一不可变 body；模拟器套件 `testR3AllSupportedStreamsLostACKRelaunchAndIncrementalAnchors` 覆盖「ACK 丢失 → 重启 → 同 body 重试 → 同 receipt → 仅 ACK 后推进 anchor」。
- 真机真实 lost-ACK 验收钩子：**未执行**。

## 15. M6 Re-evaluation

**未运行。** M6 重跑的前提是真实 v2 manifest 成功提交；真机未执行，故不重跑、不评估 `load_sync_incomplete` 是否消失。M6 合同语义（RAI-eligible、Apple-Watch 限定、running-focused 的 recorded load 域）已在合同层冻结，未新增 UI 字段。

## 16. R1/R2 Regression

- R1 core identity 未变：`WorkoutCoreIdentity.canonical` 与 v1 路径未改动；`user_entered` 分支只影响 classification，不影响 core facts。
- R2 VO2 路径未改动；`vo2maxFactsVersion`、metadata registry v2 与 allowlist 未扩大（第三个未知 metadata 仍 `metadata_requires_review`）。
- 本阶段 Swift 163 tests、Go 全量单测、PG18 集成、模拟器 60 例均通过，说明既有 R1/R2 契约未回归。
- 真机 R1/R2 复跑：**未执行**（设备不可用）。

## 17. Privacy

- `scripts/check-ua1-r3b-privacy.py` → **PASS**（扫描 3 个公开产物，0 命中）。
- 公开 evidence 只输出 count / hash / classification 分布 / reason；不输出 Workout UUID、route UUID、device identifier、source bundle、raw metadata key 或任何健康值。
- source evidence 仅存在于上传载荷与受限服务器数据库（`exclusions` jsonb），不进入公开证据；policy 合同文档刻意不写具体 bundle 标识。
- 本阶段日志/输出中未打印 device identifier（device.py 只输出布尔白名单字段）。

## 18. Risks

1. **真机未验证**（最高）：真实 44/31/13 inventory 的 v2 分类、eligible 全量补齐、真实 lost-ACK、第二增量、M6 重评均未在真机上执行；合成侧等价物已通过，但不能替代真机。
2. inventory 数量漂移：真机恢复后可能自然新增 Workout，届时以实际值为准（不得假设仍为 31/13）。
3. `user_entered` 判定依赖「来源为 Health 应用自身 bundle 且无 productType/device 描述」；若真实设备存在描述被部分填充的手工录入 Workout，会落到 unclassified 而 BLOCKED（设计上宁阻塞不误纳）。
4. 既有 Go 集成包在 `-race` 下需要 `-timeout 40m`，属环境预算事实；后续 CI 若不调整会误报失败。
5. v2 与 v1 并存增加分派面：未知 contract → 422，客户端需保持 pending 不推进 anchor（已有覆盖）。

## 19. UA1 Re-Acceptance Decision

```
UA1-R3B RESULT:
BLOCKED (real-device acceptance not executed)

R1 WORKOUT REPEAT:
PASS (unchanged contract; real-device rerun not executed this stage)

R2 REAL VO₂ COMPATIBILITY:
PASS WITH RISKS (unchanged)

R3 REAL ELIGIBLE-WORKOUT ALL-STREAM COMPLETENESS:
NOT VERIFIED (synthetic + server-side PASS, real device not executed)

UA1 RE-ACCEPTANCE:
BLOCKED

REAL-DEVICE ENGINEERING:
PARTIALLY VERIFIED / NOT ACCEPTED

REAL DATA AVAILABILITY:
NOT ESTABLISHED THIS STAGE

PHYSIOLOGICAL VALIDATION:
NOT VERIFIED

PRODUCT ACTIVATION:
NOT AUTHORIZED
```

真机恢复后必须继续执行：completeness #1（真实 inventory 复核 + eligible 全量 facts/streams + excluded evidence）→ 真实 lost-ACK 钩子 → completeness #2（committed anchor 复用、无 200 天 bootstrap）→ M6 重评。在此之前不建议开始 UA2。
