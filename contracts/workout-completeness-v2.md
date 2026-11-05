# Workout Sync Completeness V2

控制面版本：`workout-sync-completeness/v2`（`protocolVersion = 2`）

`streamSetVersion`：`1`（流集合本身没有变化）

源策略身份：`rai.workout.source_policy.apple_watch_only/v1`

分类版本：`1`

端点：`POST /v2/sync/workout-completeness`（与 v1 同一端点，由 body 的 `contract` 选择语义）

## 1. 为什么新增 v2 而不是改 v1

`workout-sync-completeness/v1` 已冻结为“每一条 HealthKit 可见 running Workout 都必须有
Protocol 3 receipt”。真实 inventory 中存在第三方 Workout，而 RAI source policy 规定它们
不可进入 RAI 数据域。两个合同无法同时满足。

因此 v1 语义**不原地修改**：v1 继续按原语义接受与校验；v2 用新的合同表达新的 inventory 语义。

## 2. 三层 inventory

```text
VisibleRunningInventory = EligibleRunningInventory + ExcludedRunningInventory + UnclassifiedRunningInventory
```

- **Visible**：当前 anchored discovery 实际可见的 running Workout，查询层不得先过滤第三方。
- **Eligible**：符合冻结 source policy（`apple_watch`）的 Workout，仍按既有 Protocol 3 上传。
- **Excluded**：明确识别为 `third_party` / `user_entered` 的 Workout；不需要 receipt、
  不需要 samples/route 上传、不产生分析任务，但必须产生 exclusion evidence。
- **Unclassified**：无法安全分类；只要非零即 BLOCKED。

只有 `unclassifiedCount == 0` 且 `pendingClassificationCount == 0` 时三层关系成立。

## 3. Manifest

相对 v1 新增：

- `sourcePolicyVersion`、`sourcePolicySHA256`、`classificationVersion`
- `inventory{visibleCount,eligibleCount,excludedCount,unclassifiedCount,pendingClassificationCount,visibleSHA256,eligibleSHA256,excludedSHA256}`
- `exclusions[]`：每条 `{workoutIdentitySHA256,classification,outcome,reason,classificationVersion,sourcePolicyVersion,evidenceSHA256,sourceEvidence}`

`workouts[]` 在 v2 中**只表示 eligible 集合**：每条仍带 `bundleSHA256`、`routeState` 与十条 series digest。
`streams` 只包含 `workouts` 流与每条 eligible Workout 的 10 quantity + route 流。

`queryFingerprint` 由 v2 合同字符串计算，因此 v1 的 anchor 不可能被 v2 复用。

## 4. 服务器验证

按顺序至少重验：

1. `contract` / `protocolVersion` / `streamSetVersion`
2. 策略身份三件套（版本、digest、分类版本）
3. `visibleCount == eligible + excluded + unclassified`，且 unclassified / pending 为零
4. 每条 exclusion：策略重算一致、evidence digest 一致、outcome/reason 一致、evidence 有界
5. 三条 inventory digest 与成员集合重算一致（identity 为 Workout UUID 的 SHA-256，按序排序后取 digest）
6. page chain 推导出的可见集合 == eligible ∪ excluded
7. 已落库 running Workout 集合 == eligible 集合（excluded 不得进入事实域）
8. 每条 eligible Workout 的 v3 receipt、流覆盖、series digest、tombstone 一致性
9. 代数、`previousManifestSHA256`、cutoff、窗口、时间边界

计数本身不能提交：没有成员 digest 与 evidence 的 manifest 一律拒绝。

## 5. 错误码

| code | status | 含义 |
| --- | --- | --- |
| `unsupported_version` | 422 | 未知 contract |
| `source_policy_mismatch` | 422 | 策略版本/digest/分类版本不匹配 |
| `inventory_invariant_violation` | 422 | 三层关系不成立或存在 unclassified/pending |
| `exclusion_policy_mismatch` | 422 | 声明分类不是策略意义上的 exclusion |
| `exclusion_evidence_invalid` | 422 | evidence 重算结果或 digest 不一致 |
| `inventory_identity_conflict` | 422 | 同一 identity 同时出现在 eligible 与 excluded |
| `inventory_digest_mismatch` | 422 | 成员 digest 与计数/集合不一致 |
| `completeness_inventory_conflict` | 409 | page chain 可见集合与声明 inventory 不一致 |
| `completeness_known_workout_missing` | 409 | 已落库事实域与 eligible 集合不一致 |
| `manifest_generation_conflict` | 409 | v1 lineage 被当作 v2 续写，或代数非法 |
| `manifest_identity_conflict` | 409 | 同一 manifestID 提交了不同 body |
| `stale_manifest` | 409 | 代数/cutoff/前序 digest 不匹配 |

## 6. 回执与状态迁移

- 回执是独立的不可变 receipt，重放相同 body 返回相同逻辑回执（lost ACK 安全）。
- 客户端只有在收到回执后推进已提交 anchor；断网、App kill 都保留 pending body 与 identity。
- v1 的已保存状态（generation 0、0 anchors、无 manifest）允许显式迁移为 reviewed-v2：
  未提交的本地 page chain 丢弃并重新捕获，已提交的 anchor 与 inventory 不动。
- 已提交的 v1 manifest 不会被重新解释为 v2；同一 lineage 上 v2 只续写 v2。
