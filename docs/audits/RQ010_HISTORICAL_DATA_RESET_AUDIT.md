# RQ-010 — Historical Data Upgrade & Reset Safety Audit

**审计日期：** 2026-11-01（Asia/Shanghai）
**任务模式：** READ-ONLY INVESTIGATION
**审计对象：** RAI V2（`/Users/tp/rai`）
**基线：** `main`；Feature `dfbc2774986bb2c404f8d442e53dff2725507ced`；Documentation `816e26d1a2eb2e09021eed56594f77d57d5e2a5d`；schema 17；protocol 3；migrations 00001–00017。
**最终状态：** `AUDIT COMPLETE — RQ-010 IMPLEMENTATION BLOCKED`

> 本轮没有连接任何数据库，没有执行 migration、Reset 脚本或任何删除操作，没有读取或导出真实 HealthKit 健康值、GPS 坐标、Token、密码或设备凭证。所有结论来自源码、migration、合同、历史审查报告和现有测试的只读核对。报告中的 `<dataset-id>`、`<workout-id>` 等均为占位符，不代表任何真实标识。

---

## 1. Executive Summary

### 1.1 一句话结论

RQ-010 的真实问题不是“旧 RAI 数据迁移”，而是：**当前 v2→v3 升级路径要求用一个破坏性脚本清空旧事实，该脚本同时删除了删除屏障、删除回执、导入回执和用户训练事实，却保留 `datasets`、`devices`、`collector_leases` 及其 epoch。** 这会让旧 bundle 在 Reset 后被重新接受、让已删除 Workout 有复活窗口，并让“已同步”的客户端继续相信服务器仍持有它已经删掉的数据。

### 1.2 已核实的关键事实

| 结论 | 证据等级 | 关键证据 |
|---|---|---|
| RQ-010 原文标题为“必须清空旧事实的升级路径会移除屏障和用户训练事实，却保留 epoch” | VERIFIED | `docs/audits/RQrun_WORKOUT_SYNC_REVIEW.md:242` |
| 当前合同把 `scripts/rai-reset-v2-data.sh` 作为 v2→v3 的唯一升级出口 | VERIFIED | `contracts/sync-v3.md:18`、`docs/IMPLEMENTATION.md:456`、`contracts/CHANGELOG.md:32` |
| 脚本 TRUNCATE 42 张表，包含 `entity_tombstones`、`change_events`、`route_deletions`、`workout_imports` 与全部 training 表 | VERIFIED | `scripts/rai-reset-v2-data.sh:1-63` |
| 脚本保留 `datasets`、`devices`、`collector_leases`，因此不轮换 dataset epoch / collector epoch | VERIFIED | 同上；`scripts/rai-reset-v2-data.sh:1-63`（保留表未出现在 TRUNCATE 清单） |
| 正常导入路径的 dataset 行锁、epoch/collector fencing、tombstone 优先、稳定回执、重复 DELETE 幂等、迟到 POST 拒绝均存在 | VERIFIED | `apps/server/internal/storage/postgres/store.go:135-175`、`workout_bundles.go:19-200`、`workout_bundles.go:417-537`；集成测试见第 11 节 |
| 删除 Workout 会级联删除 `workout_imports` 的原始 bundle payload 与回执，只留下 tombstone + `change_events` 删除回执 | VERIFIED | `apps/server/migrations/00014_workout_imports.sql:16`、`00003_workouts.sql:25/33` |
| 当前没有任何 HTTP API 或 Go CLI 的 Dataset Reset / Truncate / Purge 入口 | VERIFIED | `apps/server/internal/httpapi/server.go:44-77`、`apps/server/internal/admin/operations.go:16-24`、`apps/server/cmd/rai/main.go:37` |
| 当前 iOS 没有用户可见的 Reset/Clear 入口；只有 `purgeLegacyPages()` 与 `beginTask(resetBundle:)` 等局部动作 | VERIFIED | `ios/RAI/App/RAIApp.swift:212`、`SyncCoordinator.swift:135/191/228/479`、`OutboxStore.swift:266/455` |
| 现有测试没有覆盖 reset 脚本、Dataset Reset、Anchor Reset、Outbox 全清、重装 App、真实 pg_dump 恢复 | VERIFIED（grep 与测试清单） | `apps/server/integration/`、`ios/RAI/Tests/SyncCoreTests/`；见第 11 节 |
| 当前灾难恢复重同步为 BLOCKED，不能拿 Reset Anchor/Outbox 代替 | VERIFIED | `docs/RAI_DISASTER_RECOVERY.md:3/22-27/44-50`、`docs/RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md:5/11-19/41` |
| 旧 RAI SQLite 数据迁移不在 RQ-010 原始范围，也不在 ADR-002 范围 | VERIFIED | `docs/adr/002-fresh-healthkit-dataset.md:3/7/15` |

### 1.3 三个独立结论

```text
A. 是否需要历史数据升级？
REQUIRED
（仅指：现有非空 V2 数据集若要继续保留并使用 v3，必须有一条受控升级路径。
  旧 RAI SQLite → V2：NOT REQUIRED；schema 17 → 18：当前 NOT REQUIRED；
  具体采用“兼容叠加”还是“新 dataset 重导”：NEEDS_PRODUCT_DECISION。）

B. 是否需要 Reset 功能？
NEEDS_PRODUCT_DECISION
（不建议实现通用、面向用户的 Dataset Reset。若选择新 dataset，
  需要一次性、operator-only 的“新 dataset + 新 epoch + 新设备 + 新客户端会话”；
  若选择兼容叠加，则不需要 Reset。RESET_SYNC_CURSOR / REBUILD_ANALYSIS
  没有明确业务需求前不建议实现。）

C. 是否允许进入 RQ-010 实施阶段？
BLOCKED
（数据保留范围、数据权威性、恢复策略均未由用户确认；现有 reset 脚本
  破坏删除屏障与训练事实；没有 epoch 轮换、客户端重绑定和 reset 测试证据。）
```

### 1.4 立即建议

1. **不得执行** `scripts/rai-reset-v2-data.sh`，尤其不得对任何需要保留的 V2 数据集执行。
2. 先由用户决定：保留现有 V2 数据集（走兼容叠加/定点转换），还是新建 dataset 并从 HealthKit 重导（走新会话）。
3. 在合成非空 v2 数据集上验证 `WorkoutCoreEqual` / sample 身份兼容性之前，不能声称“原地叠加安全”。
4. 任何破坏性操作都必须改变 dataset epoch 与 collector epoch、撤销旧设备、要求客户端新会话，并保留 tombstone / receipt / 训练历史。
5. RQ-010 实施前必须补齐 reset 并发、epoch 轮换、Outbox 丢失、非空 v2→v3、真实备份恢复的测试。

---

## 2. RQ-010 原始需求

### 2.1 原文与位置

`docs/audits/RQrun_WORKOUT_SYNC_REVIEW.md:242`：

> **RQ-010 · P1 · 必须清空旧事实的升级路径会移除屏障和用户训练事实，却保留 epoch**
>
> 位置：`contracts/sync-v3.md` 设计目标、`docs/IMPLEMENTATION.md:456`、`scripts/rai-reset-v2-data.sh:1–75`、`SyncCore/OutboxStore.swift:298–319`。
>
> 触发：按当前文档将非空 v2 数据集切到 v3，并运行显式重置脚本。
>
> 影响：脚本不只删可重算数据，还 TRUNCATE `training_overrides`、`training_executions`、`training_commands`、参数、tombstone 和回执；保留 `datasets`/`devices`/collector leases，也就保留旧 dataset/collector epoch；旧文件可再次被接受；客户端已完成任务仍可能显示已同步；自动 purge 旧页又移除尚未交付的数据；不能证明无损升级或旧请求 fencing。
>
> 证据：只读核对脚本 TRUNCATE 清单与保留清单即可确认；本轮没有执行重置脚本，真实升级/客户端对账 NOT RUN。此项是发布阻断项。
>
> 修复建议：首选版本兼容读取/定点转换或独立全新 dataset，在明确保留用户训练事实的前提下切换；任何批准的破坏性重置必须改变 dataset/collector 代次、fence 旧文件，并明确客户端重新绑定与人工确认。

同报告 `:430` 的下一步第 6 条：

> **解决升级出口（RQ-010）**：明确新 dataset/兼容策略、用户训练事实保留、epoch fencing 和客户端对账。未完成前不将 reset 脚本当通用升级方案，禁止自动执行。

### 2.2 七个问题的回答

**1. RQ-010 原始问题是什么？**

不是“要不要迁移历史数据”，而是“**现有 v2 事实升级到 v3 时，唯一被文档认可的路径是破坏性清空；该路径会删除删除屏障、删除回执、导入回执和用户训练事实，却保留旧 epoch，从而无法证明旧请求被 fence、也无法证明升级无损**”。这是一个**升级出口的安全缺陷**，不是数据迁移功能需求。

**2. 为什么需要历史数据升级？**

因为 Protocol 3 的 `POST /v2/sync/workout-bundles` 与旧 v2 的逐 sample/page/batch/series/route 上传路径不兼容；旧路由已从 `httpapi.New` 移除（`docs/IMPLEMENTATION.md:456`）。现有非空 V2 数据集若要继续作为 v3 后端的数据集使用，必须让其中的 Workout/样本/Route 能被 v3 客户端和 v3 导入事务正确处理。这个“升级”可以是：

- 兼容读取/定点转换（在同一 dataset 内叠加 v3 导入）；
- 或新建 dataset，让 v3 客户端重新导入；
- 或（不推荐）破坏性清空后重导。

**3. Reset 指的是客户端 Reset、Dataset Reset，还是整个数据库 Reset？**

原始 RQ-010 指的是**Dataset 级破坏性 Reset**：`scripts/rai-reset-v2-data.sh` 对当前连接的数据库执行 `TRUNCATE`，清空 fact/analysis/training 行，但保留 `datasets`/`devices`/`collector_leases`。它不是 iOS 客户端本地 Reset，也不是完整的 PostgreSQL 数据库重建。

但当前文档和口语中的“Reset”把多个场景混在一起：

- 客户端清空 Anchor / Outbox / `state.json`；
- 单个 Workout 重同步；
- 只重算分析；
- Dataset 级破坏性清空；
- 开发环境 `pg.sh reset` 的整库 DROP/CREATE；
- 从备份恢复。

这些场景的**数据权威性、可恢复性和安全边界完全不同**，不能共用一个模糊入口。

**4. 是否要求迁移旧版 RAI 数据？**

**不要求。** `docs/adr/002-fresh-healthkit-dataset.md:3` 记录用户明确要求“不迁移旧数据，开发完成部署后，直接手机重新同步导入历史健康数据”；`:7` 明确“不实现或执行本次上线所需的旧 SQLite exporter/importer，不接续旧服务端数据，也不复用旧协议的回执／anchor 作为新数据集已交付证明”；`:15` 明确旧服务端人工修改、训练计划/执行、模型激活、旧回执、旧删除记录不在接续范围。旧 RAI 目录 `../running` 不在本工程修改范围。

**5. 是否要求清理现有 V2 历史数据？**

**原始 RQ-010 没有把“清理”本身作为产品需求；它指出的是“如果必须清理，当前清理方式不安全”。** 是否需要清理，取决于用户是否保留现有 V2 数据集。当前文档把“部署前必须运行 reset 脚本”写成硬性步骤（`contracts/sync-v3.md:18`、`docs/IMPLEMENTATION.md:456`），但这是**实现合同的选择**，不是产品需求；它没有回答“现有 V2 的 Workout、分析、训练历史是否允许丢失”。

**6. 是否要求从 HealthKit 重新发现和同步全部历史 Workout？**

**不是 v3 协议的普遍要求。** 正常 v3 客户端只对已发现/待同步的 Workout 生成 bundle；`completeSampleTypes` / `completeRoutes` 只描述读取覆盖，不授权删除，也不要求全历史一次性重读（`contracts/sync-v3.md:3`）。只有选择“新 dataset”或“清空后重导”时，才需要从 nil anchor 重新发现当前 HealthKit 可见对象；这仍然**不能**恢复已删除、不可读、权限外或客户端不支持的历史，也不能替代数据库备份（`docs/adr/002-fresh-healthkit-dataset.md:15`）。

**7. 是否存在多个不同 Reset 场景被混为一谈？**

**是。** 至少六种：

| 场景 | 作用对象 | 是否删除原始事实 | 是否保留删除屏障 | 是否可恢复 |
|---|---|---|---|---|
| 开发环境 `pg.sh reset` | 本地 `rai_dev` 整库 | 是（DROP DATABASE） | 否 | 仅开发数据，可重建 |
| 客户端同步状态 Reset | `state.json` / Anchor / Outbox | 可能丢失未 ACK bundle | 本地删除集可能丢失 | 条件性，见第 7 节 |
| 单个 Workout 重同步 | 一个 Workout 的 bundle | 否 | 是（服务端 tombstone 优先） | 是 |
| 只重算分析 | analysis 表 | 否 | 是 | 是，但需 bump generation |
| Dataset 破坏性 Reset | 一个 dataset 的 fact/analysis/training | 是 | **否（当前脚本）** | 仅备份/重导 |
| 整库重建 | PostgreSQL 全部表 | 是 | 否 | 仅备份 |

### 2.3 范围标记

- **RQ-010 原始缺陷定义完整**，不标 `RQ010_SCOPE_UNCLEAR`。
- 但 Phase 5 任务标题中的“历史数据升级”比 RQ-010 原文更宽。如果把它解释为“旧 RAI SQLite → V2 迁移”，则**超出 ADR-002 和 RQ-010 原文范围**，应视为范围扩大并回到用户确认；本轮不将其纳入实施建议。
- 真正不清楚的是**现有 V2 数据集的保留决策**（保留 / 放弃 / 部分保留），列入第 13 节“必须由用户决定的问题”。

---

## 3. 当前 Git / Schema / Protocol 基线

### 3.1 Git

| 项目 | 实际值 | 与任务预期 |
|---|---|---|
| 分支 | `main` | 一致 |
| HEAD | `816e26d1a2eb2e09021eed56594f77d57d5e2a5d` | 一致（Documentation commit） |
| Feature commit | `dfbc2774986bb2c404f8d442e53dff2725507ced` | 一致（HEAD 的父提交） |
| 上游 | `origin/main`，本地 ahead 2 | 未 push |
| 工作树 | 仅 `?? docs/evidence/rqrun-phase4-logs/` 未跟踪；`git diff` / `git diff --cached` 为空 | 冻结状态干净 |
| 本轮新增 | 仅本报告与机器证据 JSON | 符合限制 |

执行前已运行并记录：

```bash
git status --short --branch
git rev-parse HEAD
git log --oneline -5
git diff --stat
git diff --cached --stat
```

未执行 `git add`、`commit`、`push`、`reset`、`clean`、`checkout`、`restore`、`stash`、`rebase`、`merge`。

### 3.2 Schema / Protocol

| 项目 | 实际值 | 证据 |
|---|---|---|
| Schema version | 17 | `apps/server/cmd/rai/main.go:44`；`apps/server/migrations/00017_analysis_sample_lookup.sql` |
| Protocol version | 3 | `contracts/sync-v3.md:3`；handshake 代码 |
| OpenAPI | 1.4.0 | `apps/server/cmd/rai/main.go:44` |
| Migrations | 00001–00017，共 17 个 | `apps/server/migrations/` |
| 表数量 | 45 | migration 全量汇总；`docs/RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md:27` 也记录 45 表 |
| checksum | 17/17 与 `checksums.json` 一致 | `shasum -a 256` 逐项比对；本轮只读 |

**文档漂移：** `docs/IMPLEMENTATION.md:5` 仍写“schema 16”，但实际 migration 已到 17、CLI 版本输出为 17。该漂移不影响本报告结论，但部署/验收文档应以实际 migration 与 `rai` 版本输出为准。

### 3.3 现有非空数据集的状态

`docs/IMPLEMENTATION.md:359` 记录：

- 用户选择保留 fnOS 上的 `rai-v2-device` 数据集并把后端升级到当前源码；
- 升级前后计数不变（1 dataset / 2 devices / 20,750 samples / 316 Workouts / 263 batches / 263 pages / 14 routes / 496 publications / 0 training plans）；
- 手机 App 原先绑定的是另一个已被本地重置的旧数据集，经用户确认清除 App 本地状态并重装；旧 Outbox/Anchor 丢弃，fnOS 服务端数据保持不变；
- 当时握手 protocol 2，说明该数据集仍是 v2 事实。

`docs/IMPLEMENTATION.md:467` 记录 fnOS 隔离环境后来以 `protocolVersion=3` / `workoutBundles=true` 握手，但**没有记录执行过 `rai-reset-v2-data.sh`**。`docs/evidence/workout-bundle-2026-10-31.json` 的 `knownLimitations` 仍写：

> Protocol 3 is not compatible with existing v2 facts; `scripts/rai-reset-v2-data.sh --confirm-delete-v2-facts` must be run explicitly before using a non-empty v2 dataset.

因此：

- **该数据集是否仍是非空 v2 事实：PARTIALLY_VERIFIED（历史文档计数与 protocol 2 握手）；**
- **是否执行过 reset：UNKNOWN；**
- **v3 导入是否已在该非空 v2 数据集上实际运行：UNKNOWN；**
- 本轮没有连接 fnOS 或任何数据库，不能确认当前状态。

这是第 13 节必须让用户确认的第一个问题。

---

## 4. 当前数据架构

### 4.1 端到端数据流

```text
iPhone
  |
  |  HKAnchoredObjectQuery（Workout / Sample / Route / Deleted Objects）
  v
HealthKit
  |
  |  DiscoveryQuery{contract=1, type, workoutID?, initialization}
  |  stageDiscovery: 事件 + secure archive 先写入 state.json
  |  promoteDiscovery: 再次校验后才推进 anchor
  v
WorkoutBundle（不可变 JSON；canonical 字节 + SHA-256 + boundary）
  |
  |  Persistent Outbox（state.json 原子替换；payload 文件先落盘）
  |  bundleFile / bundleWireFile(gzip 派生) / uploadReceipt / deletionReceipt
  v
HTTP Upload
  |  POST /v2/sync/workout-bundles  (multipart, 字段 file)
  |  GET  /v2/sync/workout-bundles/{workoutID}?sha256=...
  |  DELETE /v2/sync/workout-bundles/{workoutID}
  v
Go API（httpapi）
  |  lockDataset: datasets 行锁 + dataset epoch + collector lease
  |  单事务：tombstone 检查 → Workout/样本/series/route → commit_rev+1
  |           → dirty scope → 1 个 job → 1 个 change event → 回执
  v
PostgreSQL Raw Facts
  |  workouts / health_samples / workout_samples / series_* /
  |  workout_routes / route_chunks / route_points / workout_imports
  v
Durable ACK（workout_imports.receipt；客户端严格校验后持久化）
  |
  v
Dirty / Job（dirty_scopes → jobs → job_attempts）
  |
  v
Snapshot（analysis 输入快照，按 generation/revision）
  |
  v
Analysis（analysis_runs / analysis_results）
  |
  v
Publication（analysis_publications，generation CAS）
```

关键架构原则（`AGENTS.md`）：

- 原始事实、删除屏障、提交日志、分析待办及最终回执必须在同一事务提交；
- dataset 行锁产生提交顺序；禁止用 sequence 当下载水位；
- 上传不可变、重试返回原回执；删除优先且旧上传不得复活 UUID；
- 同步不得依赖备份、恢复、迁移、全库摘要或全量重算；
- 管理工具不可被 `application/httpapi` 导入。

### 4.2 HealthKit 层

| 检查项 | 实现 | 证据 | 结论 |
|---|---|---|---|
| HKAnchoredObjectQuery | `HealthDiscovery` 用 `DiscoveryQuery` 驱动锚定查询；`initialization="all_visible_history"` 首次全量可见历史 | `ios/RAI/Sources/SyncCore/HealthDiscovery.swift:4-30` | VERIFIED |
| Workout Discovery | 按 Workout 类型查询；`workoutID` 可限定单 Workout | 同上；`SyncCoordinator.swift:76-110` | VERIFIED |
| Sample Discovery | 按 `sampleType` 独立 query；`completeSampleTypes` 仅覆盖说明 | `contracts/sync-v3.md:3`；`HealthDiscovery.swift:30-70` | VERIFIED |
| Route Discovery | 独立 Route 查询；route archive 先校验 SHA 再入 state | `HealthDiscovery.swift`；`SyncCoordinator.swift` route 路径 | VERIFIED |
| Deleted Objects | 只有收到 `HKDeletedObject` 才产生删除事件；普通快照缺席不删除 | `contracts/sync-v3.md:3`；`WorkoutSync.swift:67-109` | VERIFIED |
| Anchor 保存方式 | `DiscoveryFlow.anchor` 存在 `state.json` 的 `discovery.flows`；query key 包含 contract/type/workoutID/initialization | `HealthDiscovery.swift:4-30` | VERIFIED |
| Anchor 与 Workout 关联 | `DiscoveryEvent.workoutID` / `parents` 绑定父 Workout；`deletedWorkouts` 抑制旧事件 | `HealthDiscovery.swift:30-98` | VERIFIED |
| Anchor 恢复机制 | 启动时先恢复 `staged` batch，再执行下一个 HealthKit 查询；`promoteDiscovery` 要求 `flow.anchor == batch.previousAnchor` | `HealthDiscovery.swift:98-157`；`SyncCoordinator.swift:76-110` | VERIFIED |
| Anchor 持久化时机 | `stageDiscovery` 先提交事件与 archive；`promoteDiscovery` 再推进 anchor；两者都替换 `state.json` | `HealthDiscovery.swift:98-157` | VERIFIED |
| Anchor 与上传 ACK 的关系 | **anchor 在事件落盘后即推进，不等待 bundle 上传 ACK**；ACK 只把事件标 `delivered` | `SyncCoordinator.swift:76-110`；`OutboxStore.swift:376-420` | VERIFIED |

**必须明确：** HealthKit 是原始事实来源之一，但 Anchor 是**发现进度**，不是服务器持久化证明。Anchor 已推进只能证明“本地已把发现事件与 archive 写入 state.json”，不能证明服务端已收到 bundle，也不能证明服务端仍保有对应事实。`RecoveryBoundaryTests.deliveredPayloadMayBeUnavailableAfterServerRollback()` 已用合成测试证明：服务器回滚后，已 ACK 且 GC 的本地数据不可恢复，而 anchor 仍前进。

### 4.3 iOS Outbox 层

| 检查项 | 实现 | 证据 | 结论 |
|---|---|---|---|
| Bundle 文件保存 | `WorkoutSyncTask.bundleFile` 持久化 exact canonical JSON；`bundleWireFile` 为 gzip 派生 | `ios/RAI/Sources/SyncCore/WorkoutSync.swift:67-109` | VERIFIED |
| Gzip 派生文件 | `setBundleWire` 要求 canonical bundle 存在、SHA 匹配、无 uploadReceipt；旧 wire 进 `reclaimFiles` | `OutboxStore.swift:266-376` | VERIFIED |
| Bundle identity | `bundleSHA256` + `bundleBoundary`；`setBundle` 只允许首次或完全相同，否则 `pending_bundle_immutable` | `OutboxStore.swift:266-376` | VERIFIED |
| SHA-256 | canonical JSON 字节的 SHA-256；gzip 不改变 bundle hash | `contracts/sync-v3.md:37-39`；`WorkoutBundleDelivery.swift:40-106` | VERIFIED |
| 上传状态 | `phase`、`uploadReceipt`、`deletionReceipt`、`attemptCount`、`nextAttemptAt`、`lastUploadError` | `WorkoutSync.swift:67-109` | VERIFIED |
| Retry | 指数退避；网络/408/429/5xx 可重试；401 保留原包等待配置更新；`workout_deleted` 终态不伪装成功 | `WorkoutBundleDelivery.swift:107`；`WorkoutReliabilityTests.swift` | VERIFIED |
| ACK | 只有严格 `WorkoutBundleACK.validate` 通过才 `acknowledgeBundle`；失败时对 retryable/receipt_mismatch 用 GET 原 hash 复核 | `WorkoutBundleDelivery.swift:5-31/40-106`；`OutboxStore.swift:376-420` | VERIFIED |
| 文件清理 | `acknowledgeBundle` 后 `releaseBundleFile` 进 `reclaimFiles`；`collectGarbage` 只回收未被引用的 `.json`/`.gz` | `OutboxStore.swift:376-420/626-680` | VERIFIED |
| 崩溃恢复 | `state.json` 单文件原子替换；payload 先落盘，崩溃最多留孤儿，不会出现“有 anchor 无 body” | `OutboxStore.swift:1-120` | VERIFIED |
| Outbox 与 Anchor 协调 | anchor 先推进；ACK 后事件标 delivered；未 ACK 原包在 `retainedBundles` 中永不 GC | `HealthDiscovery.swift:98-157`；`OutboxStore.swift:211/626-680` | VERIFIED |

**如果清空 Outbox：**

- 未 ACK 的 bundle 原字节**只存在于本地**（服务端没有）；清空即永久丢失，除非 HealthKit 仍能重新读出同一对象并重建，但**不能保证重建出相同字节/hash**（`WorkoutSync.swift:67` 注释明确要求重试复用原文件，而不是重读 HealthKit）。
- 已 ACK 但本地未收到回执：服务端 `workout_imports` 有 `(dataset, workout, bundle_sha256)` 回执，`GET /v2/sync/workout-bundles/{id}?sha256=...` 可以恢复原回执；前提是本地还记得 hash，且服务端 receipt 未被删除。
- 已 ACK 且本地已 GC：服务端 `workout_imports.payload` 是唯一源文档留存；客户端没有“下载原始 bundle”的 API，只能依赖服务端已有事实。
- 清空 Outbox 同时会丢失 `deletedWorkouts`、`tombstones`、`discovery.flows`、`tasks`、`recoveryOverrides`；这些不是 HealthKit 能完整重建的。

### 4.4 PostgreSQL 层

以下表名、主键、外键均以实际 migration 为准；没有假设表名。

#### 4.4.1 Raw Facts / Metadata

| 表 | 数据性质 | 所属实体 | 主键/身份 | 可重建 | Reset 风险 | 证据 |
|---|---|---|---|---|---|---|
| `workouts` | Raw Fact | Dataset / Workout | PK `(dataset_id, workout_uuid)`；`content_sha256`；`entity_revision` | 有条件（HealthKit 仍可读） | 高 | `00003_workouts.sql` |
| `health_samples` | Raw Fact | Dataset / Sample | PK `(dataset_id, sample_type, sample_uuid)`；`content_sha256` | 有条件 | 高 | `00001_core.sql` |
| `workout_samples` | Raw Association | Dataset / Workout / Sample | PK `(dataset_id, sample_type, sample_uuid)`；FK 到 `health_samples` 与 `workouts` | 有条件（需 bundle 关系） | 高 | `00010_route_foundation.sql`；`00011_p3_sync_v2.sql` |
| `workout_statistics` | Raw Aggregate | Dataset / Workout | PK `(dataset_id, workout_uuid, sample_type)`；FK CASCADE | 有条件 | 高 | `00003_workouts.sql` |
| `workout_events` | Raw Fact | Dataset / Workout | PK `(dataset_id, workout_uuid, ordinal)`；FK CASCADE | 有条件 | 高 | `00003_workouts.sql` |
| `workout_activities` | Raw Fact | Dataset / Workout | PK `(dataset_id, workout_uuid, ordinal)`；FK CASCADE | 有条件 | 高 | `00007_workout_activities.sql` |
| `series_uploads` | Raw Fact (multi-value) | Dataset / Sample | PK `(dataset_id, series_id)`；FK→`health_samples` CASCADE | 有条件 | 高 | `00008_quantity_series.sql` |
| `series_chunks` | Raw Fact | Dataset / Series | PK `(dataset_id, series_id, chunk_index)`；FK CASCADE | 有条件 | 高 | `00008_quantity_series.sql` |
| `workout_routes` | Raw Fact | Dataset / Workout / Route | PK `(dataset_id, route_uuid)`；FK→`workouts` CASCADE | 有条件 | 高 | `00010_route_foundation.sql`；`00011_p3_sync_v2.sql` |
| `route_chunks` | Raw Fact | Dataset / Route | PK `(dataset_id, route_uuid, chunk_index)`；FK CASCADE | 有条件 | 高 | 同上 |
| `route_points` | Raw Fact | Dataset / Route | PK `(dataset_id, route_uuid, sequence)`；FK→route chunk CASCADE | 有条件 | 高 | 同上 |
| `workout_enrichments` | Raw/Metadata | Dataset / Workout | PK `(dataset_id, enrichment_id)`；FK CASCADE | 有条件 | 中高 | `00011_p3_sync_v2.sql` |
| `workout_imports` | Raw Source + Receipt | Dataset / Workout | PK `(dataset_id, workout_uuid, bundle_sha256)`；FK→`workouts` CASCADE | **否**（精确字节不可从 HealthKit 保证重建） | **高** | `00014_workout_imports.sql`；`00015_workout_import_history.sql` |
| `workout_packages` | v2 Raw/Staging | Dataset / Workout | PK `(dataset_id, workout_uuid)`；FK CASCADE | 有条件（v2 旧格式） | 中高 | `00009_workout_packages.sql` |
| `workout_package_chunks` | v2 Raw/Staging | Dataset / Workout | PK `(dataset_id, workout_uuid, chunk_index)`；FK CASCADE | 有条件（v2 旧格式） | 中高 | `00009_workout_packages.sql` |

#### 4.4.2 Sync State / Identity

| 表 | 数据性质 | 所属实体 | 主键/身份 | 可重建 | Reset 风险 | 证据 |
|---|---|---|---|---|---|---|
| `datasets` | Identity / Metadata | Dataset | PK `id`；`epoch`；`commit_rev` | **否**（epoch/ID 为身份） | **高** | `00001_core.sql` |
| `devices` | Identity / Credential | Dataset / Device | PK `(dataset_id, id)`；`token_sha256 UNIQUE` | **否**（token 仅存 hash） | **高** | `00001_core.sql` |
| `collector_leases` | Identity / Fencing | Dataset / Device | PK `dataset_id`；`collector_epoch UNIQUE` | **否** | **高** | `00001_core.sql` |
| `sync_streams` | Sync State | Dataset / Collector | PK `(dataset_id, id)`；UNIQUE `(dataset, epoch, type, fingerprint)` | 否（旧协议历史） | 中 | `00001_core.sql`；`00010` |
| `sync_batches` | Sync State + Receipt | Dataset / Stream | PK `(dataset_id, batch_id)`；UNIQUE `(dataset, stream, client_seq)` | 否 | 中 | `00001_core.sql`；`00004` |
| `sync_pages` | Sync State | Dataset / Stream | PK `(dataset_id, page_id)` | 否 | 中 | `00004_page_receipts.sql` |
| `sync_request_budgets` | Temp / Rate State | Dataset / Device | 复合 PK | 可重建 | 低 | `00005_request_budgets.sql` |
| `sync_upload_slots` | Temp / Lease | Dataset | PK `(dataset_id, slot)` | 可重建 | 低 | `00006_upload_slots.sql` |
| `workout_imports` receipt 部分 | Durable Receipt | Dataset / Workout | 同上 | **否** | **高** | `00014`；`00015` |

#### 4.4.3 Delete / Anti-Resurrection State

| 表 | 数据性质 | 所属实体 | 主键/身份 | 可重建 | Reset 风险 | 证据 |
|---|---|---|---|---|---|---|
| `entity_tombstones` | Delete Barrier | Dataset / Entity | PK `(dataset_id, sample_type, sample_uuid)`；`deleted_rev > 0` | **否** | **高** | `00001_core.sql`；`00011` 中 `rai_route_delete` |
| `route_deletions` | Durable Delete Receipt | Dataset / Route | PK `(dataset_id, route_uuid)`；`deleted_rev >= 0` | **否** | **高** | `00011_p3_sync_v2.sql` |
| `change_events` | Commit Log + Delete Receipt | Dataset | PK `(dataset_id, commit_rev, ordinal)`；`event_id UNIQUE` | **否** | **高** | `00001_core.sql`；`workout_bundles.go:417-537` |
| `entity_changes` | Legacy Change Log | Dataset / Batch | PK `(dataset_id, batch_id, ordinal)` | 否（v2 历史） | 中 | `00001_core.sql` |
| `datasets.commit_rev` | Revision / Ordering | Dataset | `bigint >= 0` | **否**（回退会重排） | **高** | `00001_core.sql` |

#### 4.4.4 Derived / Analysis / Training

| 表 | 数据性质 | 所属实体 | 主键/身份 | 可重建 | Reset 风险 | 证据 |
|---|---|---|---|---|---|---|
| `dirty_scopes` | Derived Queue State | Dataset / Scope | PK `(dataset_id, scope_key)`；`generation` / `clean_generation` | 可重建（bump generation） | 中 | `00002_dirty_generations.sql` |
| `jobs` | Derived Work State | Dataset / Scope | PK `id`；UNIQUE `(dataset, kind, scope, target_generation)` | 可重建（从 dirty） | 中 | `00001_core.sql` |
| `job_attempts` | Temp / Audit | Job | PK `(job_id, fencing_token)` | 可重建/可归档 | 低 | `00001_core.sql` |
| `analysis_parameter_sets` | Config / Parameter | Dataset | PK `(dataset_id, version)` | 部分否（用户/模型参数） | 中高 | `00012_analysis.sql` |
| `analysis_runs` | Derived Lineage | Dataset / Scope | PK `(dataset_id, id)`；FK job；UNIQUE identity | 可重建（新 run） | 中 | `00012_analysis.sql` |
| `analysis_results` | Derived Result | Dataset / Run | PK `(dataset_id, run_id, day, metric)` | 可重建 | 中 | `00012_analysis.sql` |
| `analysis_publications` | Derived Pointer | Dataset / Scope | PK `(dataset_id, scope_key, metric_family, day)`；FK run | 可重建（CAS） | 中 | `00012_analysis.sql` |
| `training_parameter_sets` | User/Service Config | Dataset | PK `(dataset_id, version)` | **否** | **高** | `00013_training.sql` |
| `training_plans` | User/Service State | Dataset | PK `(dataset_id, id)`；多级 FK | **否** | **高** | `00013_training.sql` |
| `training_configs` | User/Service Config | Dataset / Plan | 复合 PK | **否** | **高** | `00013_training.sql` |
| `training_sessions` | User/Service State | Dataset / Plan | 复合 PK | **否** | **高** | `00013_training.sql` |
| `training_generation_runs` | User/Service State | Dataset | 复合 PK | **否** | **高** | `00013_training.sql` |
| `training_base_versions` | User/Service State | Dataset | 复合 PK | **否** | **高** | `00013_training.sql` |
| `training_overrides` | **User Fact** | Dataset / Plan / Session | 复合 PK；`command_id` FK | **否** | **高** | `00013_training.sql` |
| `training_effective_versions` | User/Service State | Dataset | 复合 PK | **否** | **高** | `00013_training.sql` |
| `training_executions` | **User Fact** | Dataset / Plan | 复合 PK | **否** | **高** | `00013_training.sql` |
| `training_feedback` | **User Fact** | Dataset / Execution | 复合 PK | **否** | **高** | `00013_training.sql` |
| `training_commands` | Command Journal | Dataset | 复合 PK | **否** | **高** | `00013_training.sql` |

### 4.5 事务与锁

- `ImportWorkoutBundle` 在 45s context + `SET LOCAL transaction_timeout='45s'` 内执行；`lockDataset` 先 `SELECT epoch::text, commit_rev FROM datasets WHERE id=$1 FOR UPDATE`（`store.go:135-146`），再校验 `dataset_epoch` 与 `collector_lease`（`:148-175`）。
- Workout 导入在一个事务内写 Workout、`health_samples`、`workout_samples`、series、routes；失败全回滚（`contracts/sync-v3.md:4`；`workout_bundles.go:19-200`）。
- 删除 Workout 在 dataset 行锁下先查 tombstone + `change_events` 回执；重复 DELETE 返回原回执；否则递增 `commit_rev`、写 tombstone、逐 route 写 `route_deletions`、级联删 Workout、写 dirty scope 与 change event，然后更新 `datasets.commit_rev`（`workout_bundles.go:417-537`）。
- `change_events` 的 `(dataset_id, commit_rev, ordinal)` 主键把提交顺序与回执绑定；不能用 sequence 代替下载水位（`AGENTS.md`）。
- `analysis_publications` 使用 generation CAS 更新；旧 generation 不能覆盖新 publication（`analysis_publish.go:130-141`；`TestAnalysisOldGenerationCannotReplaceNewPublication`）。

---

## 5. 数据权威性与可恢复性矩阵

### 5.1 分类

| 类别 | 数据 | 权威来源 | 是否可重建 | 丢失影响 | 推荐恢复方式 |
|---|---|---|---|---|---|
| A. 原始事实 | Workout、Heart Rate、Running Cadence、Running Power、GPS Route、其他 HealthKit Sample、`workout_statistics/events/activities`、`workout_imports.payload` | HealthKit / 手机原始 bundle | **有条件**：仅当前可见、可读、受支持的对象；精确字节不保证 | 训练历史、详情、分析、审计永久缺失 | 从 HealthKit 重新生成 bundle；保留服务端 raw payload；不得用分析结果替代 |
| B. 同步状态 | `datasets.epoch`、`devices`、`collector_leases`、`sync_streams`、`sync_batches`、`sync_pages`、`workout_imports.receipt`、iOS `state.json` / Anchor / Outbox / task receipt | 服务端身份 + 客户端本地 journal | **否** | 设备无法认证、旧请求无法 fence、已 ACK 无法核对、客户端重复/漏传 | 备份；保留；新会话时显式轮换并隔离旧状态 |
| C. 删除与防复活 | `entity_tombstones`、`route_deletions`、`change_events`（删除回执）、`datasets.commit_rev`、客户端 `deletedWorkouts` / `tombstones` | 服务端删除事务 + 客户端删除事件 | **否** | 已删除 Workout/Route/Sample 复活；旧 DELETE 重放语义丢失 | 永久保留；备份；任何 Reset 都不得删除；若必须清，只能在新 dataset + 新 epoch 下不继承 |
| D. 派生数据 | `dirty_scopes`、`jobs`、`analysis_parameter_sets`、`analysis_runs/results/publications`、Snapshot | Raw Facts + 参数 + 算法版本 | **是**（保留参数与算法版本时） | 当前分析/发布缺失；历史 run 丢失 | bump generation → 新 job → 新 run → CAS 发布；保留历史 run |
| D'. 用户/服务端专属状态 | `training_plans/configs/sessions/generation_runs/base_versions/overrides/effective_versions/executions/feedback/commands` | 用户输入 + 服务端命令 | **否**（HealthKit 不包含） | 训练计划、人工 override、执行/反馈永久丢失 | 数据库备份；原样保留；不得用重算伪造 |
| E. 临时状态 | `sync_request_budgets`、`sync_upload_slots`、`job_attempts`、worker lease/fencing runtime、缓存 | 运行时 | 是 | 限流/重试短期异常 | 仅在无活跃 lease / 已过期时清理；不删历史审计 |

### 5.2 关键不可逆点

1. **`workout_imports.payload` 是原始 bundle 的唯一服务端副本。** Workout 删除会级联删除它（`00014_workout_imports.sql:16`）；删除后只剩 tombstone + `change_events` 删除回执，无法再取回原始字节。
2. **`entity_tombstones` 是防复活的唯一持久屏障。** 它不是缓存，也不是可重建的派生数据；删除后，任何仍持有旧 bundle 的客户端/备份都可能让已删除 Workout 重新出现。
3. **`datasets.epoch` 与 `collector_leases.collector_epoch` 是 fencing 的身份。** 保留它们而清空事实，等于让旧客户端继续使用“合法”身份写一个已经被清空的数据集。
4. **`training_overrides/executions/feedback` 是用户专属事实。** 它们不能从 HealthKit 重建，也不能由分析结果推导；任何 Dataset Reset 都必须显式回答“这些是否保留”。
5. **`analysis_publications` 是指向 run 的当前视图。** 删除 run 受 FK 限制；删除 results/runs 而不 bump dirty generation，不会自动重建 job。

### 5.3 允许删除 / 必须保留 / 条件删除

| 判定 | 数据 | 条件 |
|---|---|---|
| **可以删除** | `sync_request_budgets`、`sync_upload_slots`、过期 `job_attempts`、缓存、已 GC 的临时文件 | 无活跃 lease、无未完成 job、无审计要求 |
| **必须保留** | `entity_tombstones`、`route_deletions`、`change_events` 删除回执、`workout_imports` 回执与 payload、`datasets`/`devices`/`collector_leases`、训练用户事实 | 任何情况下，除非用户明确放弃整个 dataset 并接受不可恢复 |
| **条件删除** | `analysis_*` | 有参数/算法版本、能重建、保留历史 run；先 bump generation，再删旧结果 |
| **条件删除** | v2 `sync_streams/batches/pages`、`workout_packages` | v3 不再使用；若确认无审计/回执需求，可归档后删除；不能与 tombstone 一起清 |
| **禁止在保留 dataset 上删除** | Raw Facts、tombstone、receipt、training 用户事实 | 只能通过新 dataset + 新 epoch + 新会话隔离 |

---

## 6. Reset 入口清单

### 6.1 全量清单

| Reset / 清理入口 | 位置 | 作用范围 | 是否危险 | 当前是否可用 |
|---|---|---|---|---|
| `scripts/rai-reset-v2-data.sh --confirm-delete-v2-facts` | `scripts/rai-reset-v2-data.sh:1-63` | 当前 `RAI_DATABASE_URL` 指向的库：42 张表 TRUNCATE；保留 datasets/devices/collector_leases | **极高**：删 tombstone、receipt、training facts；不改 epoch、不审计、不校验环境 | 可用（但本轮禁止执行） |
| `scripts/local/pg.sh reset` | `scripts/local/pg.sh:65-75` | 仅本地 loopback `rai_dev`：DROP DATABASE + createdb | 低（开发环境）；`lib.sh:24-27` 清除生产 DSN 环境变量 | 可用（开发） |
| `scripts/local/verify.sh` | `scripts/local/verify.sh:27/76-78` | 调 `pg.sh reset`；验证后 DROP 临时库 | 低（开发/测试） | 可用 |
| `scripts/p7-restore.sh` | `scripts/p7-restore.sh` | 仅恢复到显式 `rai.restore-target=true` 且 `public` 表为 0 的隔离容器；`--single-transaction --exit-on-error --no-owner --no-acl`；无 `--clean` | 低（隔离恢复）；若被改指向生产则极高 | 可用（隔离） |
| `deploy/postgres/bootstrap.sh` | `deploy/postgres/bootstrap.sh` | 首次创建角色；不建库、不删数据 | 低 | 首次初始化 |
| `rai init` / `admin.Init` | `apps/server/internal/admin/admin.go:208-245`；`cmd/rai/main.go:37` | 仅空 `datasets` 表创建首个 dataset/device/collector/token | 中：已有 dataset 时拒绝；不能在已有库新建第二个 dataset | 可用（仅空库） |
| `rai migrate` | `admin.go:68` | 应用 migration；不删事实 | 低-中（00017 锁表，见第 12 节） | 可用 |
| `rai migrate-down` | `admin.go:101-168` | 回退 migration；`reversibleMigrations` 为空 → 拒绝 | 低（当前不可回退） | 实际不可用（拒绝） |
| `rai ops snapshot` / `verify` / `retention-dry-run` | `apps/server/internal/admin/operations.go:16-24` | 只读；`RepeatableRead` + `ReadOnly`；`retention-dry-run` 不删 | 低 | 可用 |
| iOS `purgeLegacyPages()` | `OutboxStore.swift:455`；`RAIApp.swift:212`；`SyncCoordinator.swift:191` | 删除本地 v2 page 文件/receipt；不触碰服务端 | 中：只应清 v2 legacy pages；若被误用可能丢弃未交付 page | 自动（启动/同步） |
| iOS `beginTask(id, resetBundle:)` | `OutboxStore.swift:266`；`SyncCoordinator.swift:135/228/479` | 单个 Workout task：重置 uploadReceipt/hash（仅当无 bundleFile）；不删 bundle 文件 | 中：不会删除未 ACK 原包；但会重置重试身份 | 可用（单 Workout） |
| iOS `controlledRediscovery(query, recoveryID)` | `HealthDiscovery.swift:234` | 为损坏/旧 anchor 建新 namespace；旧 queues/anchors 完整保留 | 中：新 namespace 会重新枚举可见历史；不删旧状态 | 可用（显式；无 UI） |
| iOS `parkBundleForExplicitChildDeletion` | `HealthDiscovery.swift:211` | 未 ACK 原包移入 `retainedBundles`，永不 GC | 低（保护性） | 可用 |
| iOS `reconcileTasks()` | `OutboxStore.swift` | 无 durable page 且从未到服务器的 task 转可重试失败 | 中：不会静默成功 | 可用 |
| 手动删除 `RAIV2Sync/state.json` 或整个目录 | 非代码入口 | 本地全部 Anchor/Outbox/receipt/deleted sets | **极高**：未 ACK bundle 永久丢失；无审计 | 不支持，禁止作为方案 |
| `DELETE /v2/sync/workout-bundles/{id}` | `httpapi/server.go:52`；`workout_bundles.go:417-537` | 单个 Workout 显式删除：tombstone + change event + 级联删 Workout/imports | 高（业务删除，不是 Reset） | 可用；有幂等/回执/测试 |
| `TRUNCATE datasets CASCADE`（手工 SQL） | 非代码入口 | 所有 FK 引用表（含 devices/leases/training） | **极高**；不可恢复 | 禁止 |
| `DELETE FROM datasets`（手工 SQL） | 非代码入口 | 受 FK 阻止；需先清子表 | 极高 | 禁止 |
| `scripts/test-*.sh` 的 `down --volumes` | `scripts/` | 仅测试 Compose 项目卷 | 低（测试） | 可用 |
| `scripts/remote-test.sh` | `scripts/remote-test.sh` | 仅 owned test PG volume；不 prune | 低（测试） | 可用 |
| `TestFreshDatabaseSchemaRebuild` | `apps/server/integration/schema_rebuild_test.go:19` | 空库全量 schema 对比 | 低（测试） | 测试 |

### 6.2 看起来像“Reset Sync”但实际危险的操作

1. **`rai-reset-v2-data.sh`**：名字是“reset v2 data”，实际删除 `entity_tombstones`、`change_events`、`route_deletions`、`workout_imports` 和全部 training 表；它不是“同步状态重置”，而是**数据集事实/屏障/回执/训练历史的整体清除**。
2. **手动删除 `state.json`**：看起来只是“清本地同步状态”，实际同时丢失 `deletedWorkouts`、`tombstones`、未 ACK bundle、pending read keys、recovery overrides；客户端会从 nil anchor 重扫，但服务端 tombstone 若仍在还能挡住旧 UUID，若 tombstone 已被 reset 清掉则可能复活。
3. **`purgeLegacyPages()`**：只删 v2 legacy pages；如果误以为它是“清理旧同步队列”，会丢弃尚未交付的 v2 page/receipt；v3 没有 page 队列，故只应在启动迁移时调用。
4. **`beginTask(resetBundle:true)`**：只重置单个 task 的上传 receipt/hash；不会删除服务端事实，也不会删除本地 bundleFile；但它会让一个“已 ACK 但本地状态异常”的 task 重新准备，必须保留 tombstone 检查。
5. **`controlledRediscovery`**：不是 Reset，而是新 namespace；旧 anchor/queue 保留；若被当作“修复 anchor”的通用按钮，会造成重复枚举与重复 bundle。

### 6.3 结论

- **HTTP API 没有 Reset/Dataset 删除入口**（`httpapi/server.go:44-77`；OpenAPI 无 reset/purge/clear 路径）。
- **Go CLI 没有 Reset**；`admin.Operations` 明确只读，`Init` 只对空库。
- **iOS 没有用户可见 Reset**；只有局部清理/重试。
- 唯一现存的 dataset 级破坏性入口是 `scripts/rai-reset-v2-data.sh`，它正是 RQ-010 的缺陷对象。

---

## 7. 七种 Reset / 恢复场景分析

> 以下均为只读推演，没有执行任何场景。所有“可恢复”判断都以“服务端 receipt/tombstone 仍在、HealthKit 对象仍可见且可读”为前提；不满足时为 UNKNOWN 或不可恢复。

### 场景 A：仅清空 HealthKit Anchor

**当前实现：**
- 没有公开 API 只清 anchor；`HealthDiscovery.DiscoveryFlow.anchor` 存在 `state.json`；`controlledRediscovery` 会新建 namespace 并保留旧 anchor/queue（`HealthDiscovery.swift:234`）。
- 若手工把 `flow.anchor` 设为 nil / 删除 flow，下一次 `discoverPage` 会以 nil anchor 查询当前 HealthKit 可见对象；`stageDiscovery` 要求 `flow.anchor == previousAnchor`（nil 对 nil 可行），事件按 `factKey` 去重，archive 校验后 `promoteDiscovery` 推进。
- 服务端导入仍先查 Workout tombstone；已删除 Workout 的 UUID 会被 `workout_deleted` 拒绝；普通快照缺席不会创建 tombstone（`contracts/sync-v3.md:3`）。

**潜在风险：**
- 全历史重扫，可能生成大量新 bundle；若 canonical 字节与旧 bundle 不同（读取顺序、metadata、gzip 无关但 JSON 可能不同），`bundle_sha256` 不同，产生新的 `workout_imports` 回执；不会重复事实（`changed=false` 时不推进 revision/job），但回执表会膨胀。
- 若本地 `deletedWorkouts` / `tombstones` 一并丢失，已删除 Workout 可能被重新发现？HealthKit 不会返回已删除对象，所以通常不会；但**如果服务端 tombstone 也已被 reset 清空**，旧 bundle/旧备份重放会让它复活。
- 大量重复读取可能触发限流/长时间“同步中”；`device_rate_exceeded` 是预期节流，不是数据损坏。

**是否可恢复：** 条件性。当前可见、可读、受支持的 Workout 可以重建；已删除/不可读/权限外的历史不能保证。

**恢复前置条件：** 保留 Outbox 与 `deletedWorkouts`；服务端 tombstone 完整；不删除 `workout_imports` receipt；有界查询；接受重复回执。

**推荐方案：** 不提供“清 anchor”按钮。仅对损坏 anchor 使用 `controlledRediscovery`（新 namespace，旧状态保留），并在 UI 明确“重新发现当前可见历史”，不承诺恢复已删除数据。

**需要新增的保护：** anchor namespace/代际、重复发现去重、全量重扫速率限制、删除集合并发保护、恢复完成条件（不能以“查询为空”证明删除）。

### 场景 B：仅清空 iOS Outbox

**当前实现：**
- 没有 API；只有手动删除 `RAIV2Sync` 目录/`state.json`，或卸载 App（容器删除）。`OutboxStore.bind` 在 `scope == nil` 时接受新 scope；若 Keychain 配置仍在，会重新绑定同一 dataset/collector。
- 未 ACK bundle 原字节只在本地；`retainedBundles` 中的原包永不 GC（`OutboxStore.swift:626-680`）。
- 已 ACK 未收到回执：服务端 `workout_imports` 有按 `(dataset, workout, bundle_sha256)` 的回执；`GET ...?sha256=` 可恢复（`workout_bundles.go:362-390`）。
- 已 ACK 且已 GC：服务端 `workout_imports.payload` 是唯一源文档；客户端没有下载原始 bundle 的 API。

**潜在风险：**
- 未 ACK bundle 永久丢失（服务端从未收到）。
- 已 ACK 但本地未持久化 ACK：若还记得 hash，可 GET 回执；若 hash 也丢失，只能重读 HealthKit 生成新 bundle，字节/hash 不保证相同。
- 删除意图（`deletedWorkouts`、`pendingDelete`）丢失；已删除 Workout 不会因为 HealthKit 不返回而自动补发 DELETE，可能留下服务端旧事实（如果之前未删除）。
- 重装/清空后从 nil anchor 全量重扫，重复回执与限流。

**是否可恢复：** 条件性；未 ACK 部分不可恢复。

**恢复前置条件：** 服务端 receipt/payload 完整；HealthKit 对象仍可见；Workout UUID 不变；本地仍记得 bundle hash（或能接受新 bundle）；服务端 tombstone 完整。

**推荐方案：** 不提供“清空 Outbox”。提供导出/导入 pending bundle 的运维工具；卸载/重装前先让用户确认未 ACK 任务；保留 `state.json` 备份。

**需要新增的保护：** Outbox 清单/导出、未 ACK bundle 保护、重装检测、session descriptor、ACK 恢复 UI、删除意图持久化。

### 场景 C：仅清空 PostgreSQL Raw Facts

**当前实现：**
- 没有受支持的“只清 raw facts”入口；`rai-reset-v2-data.sh` 会同时清 analysis/training/tombstone/receipt；手工 `TRUNCATE` 同样危险。
- 若只清 `workouts/health_samples/...` 而保留 `datasets.commit_rev`、`devices`、`collector_leases`：iPhone anchor 已推进，不会自动重传历史；`workout_imports` 若被清则没有回执；`entity_tombstones` 若被清则删除屏障消失。
- `datasets.commit_rev` 不会回退；新导入会从当前值继续递增；旧客户端可能仍持有更高 revision 的本地状态，GET 会 404/不匹配。

**潜在风险：**
- 历史 Workout 永久缺失，客户端不知道（anchor 已前进）。
- 已删除 Workout 的 tombstone 丢失 → 旧 bundle/备份可复活。
- 分析 publication 指向已删除 raw facts → 结果陈旧/不一致。
- 旧 collector 仍可用（epoch 未变）→ 旧请求被接受。

**是否可恢复：** 仅当 HealthKit 仍可读 + 客户端能强制全量重扫 + 服务端重建 tombstone（但无法重建已删除历史）；否则不可恢复。

**恢复前置条件：** 备份；新 dataset 或新 epoch；客户端新会话；重新导入；不能只清 raw facts。

**推荐方案：** 禁止“只清 raw facts”。若必须重建，使用新 dataset + 新 epoch/device + 新客户端会话；旧 dataset 保留只读。

**需要新增的保护：** raw fact 删除禁令、dataset 级保护、epoch 轮换、外部备份校验、删除屏障导出。

### 场景 D：清空 Dataset

**当前实现：**
- 没有受支持的 API。`DELETE FROM datasets` 受 FK 阻止；`TRUNCATE datasets CASCADE` 会连带清空所有引用表（包括 devices/collector_leases/training）。
- `admin.Init` 只对空 `datasets` 表创建首个 dataset；已有 dataset 时返回 `dataset already initialized`（`admin.go:208-245`）。
- 若手工按依赖顺序删子表再删 dataset，会丢失 device token hash（不可恢复）与 collector epoch；必须重新 `Init` 才能生成新凭证。

**逐项删除/保留关系：**

| 数据 | 清空 Dataset 时 | 建议 |
|---|---|---|
| Workout / Samples / Routes | 删除 | 若保留历史，不应做 |
| Import History / Receipts | 删除（级联） | 必须保留；否则无法核对 ACK |
| Tombstones / Route deletions / change_events | 删除 | **绝对不可**在保留 dataset 时删除 |
| Jobs / Dirty | 删除 | 可重建，但需重新排队 |
| Analysis runs/results/publications | 删除（FK 顺序） | 可重建；历史丢失 |
| Training plans/overrides/executions/feedback | 删除 | **用户事实，不可重建** |
| datasets/devices/collector_leases | 若 TRUNCATE CASCADE 则删除 | 必须重新 Init；token 不可恢复 |
| Outbox / Anchor | 不在服务端 | 客户端必须新会话；否则旧 anchor 领先 |

**潜在风险：** 极高；等同于放弃该 dataset 的全部历史、身份和训练记录。

**是否可恢复：** 仅从备份恢复；HealthKit 只能恢复当前可见原始事实，不能恢复训练/回执/删除历史。

**恢复前置条件：** 完整备份、用户明确放弃、停机、客户端新会话、新 dataset/epoch/device。

**推荐方案：** 不实现“清空 Dataset”原地操作。若业务要求重建，创建**新 dataset**（新 ID、新 epoch、新 device、新 collector epoch），旧 dataset 保留只读/归档；客户端使用新会话目录与新 token。

**需要新增的保护：** 新 dataset 创建工具、epoch/device 轮换、旧 dataset 退役标记、客户端 session 切换、审计、双人确认。

### 场景 E：只清除分析结果

**当前实现：**
- 没有 API；`admin.Operations` 的 `retention-dry-run` 只报告，不删除。
- 手工 `DELETE FROM analysis_publications` → `analysis_results` → `analysis_runs` 需按 FK 顺序；`analysis_runs` 还被 `training_feedback` / `training_base_versions` 引用，删除可能被阻止。
- `dirty_scopes` 若已 clean（`generation == clean_generation`），删除 analysis 表不会自动创建 job；`rai_analysis_dirty` 只在显式调用或新事实时 bump generation 并插入 job（`00012_analysis.sql:87-99`）。
- `analysis_publications` 是 current 视图；删除后重新发布需要新 run + generation CAS。

**潜在风险：**
- 删了 results 但没 bump generation → 页面空白且不重算。
- 删了 run 但 publication 仍指向 → FK 阻止或留下孤儿（取决于删除顺序）。
- 删了历史 run → 丢失审计/对照；违反“分析结果可重建但不等于可无条件删除历史”的原则。

**是否可恢复：** 是，前提是 raw facts、参数、算法版本完整；重建需要显式 dirty generation + job + 新 run + publication。

**恢复前置条件：** raw facts 不变；`analysis_parameter_sets` 保留；历史 run 不删或归档；bump `dirty_scopes.generation`；worker 可用；publication CAS 保护。

**推荐方案：** 不删除 analysis 表；实现 `REBUILD_ANALYSIS` 作为 job 级操作：保留历史 run/publication，标记旧 run `SUPERSEDED`，新 run 成功后 CAS 切换 publication；失败保留旧 publication。

**需要新增的保护：** dirty generation bump 工具、run 历史保留、publication CAS、rebuild 审计、失败回滚。

### 场景 F：重新安装 iOS App

**当前实现：**
- 卸载 App 删除容器（`state.json`、Outbox、bundle 文件、`RAIV2Sync`）；Keychain 通常在同设备保留（`ClientConfiguration` 使用 `kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`，`ClientConfiguration.swift:7-27`）。
- 重装后若 Keychain 有 token/collectorEpoch，`bind(scope)` 以 nil scope 接受同一 dataset；v3 `HealthDiscovery` 从 nil anchor 全量扫描；服务端 `workout_imports` 幂等/回执仍在。
- 若 Keychain 丢失（新设备/抹除），没有生产级重新配对 API；`admin.Init` 不能给已有 dataset 加 device；同步 BLOCKED。
- 服务端 device identity 不变（同一 token hash），collector epoch 不变；不会自动重新注册。

**潜在风险：**
- 全历史重扫，重复 bundle/receipt；大量流量与时间。
- 未 ACK Outbox 丢失；旧删除意图丢失；本地 `deletedWorkouts` 丢失。
- 若服务端 tombstone 完整，已删除 Workout 不会复活（HealthKit 不返回）；若 tombstone 被 reset 清空，旧备份/旧 bundle 可复活。
- 若 Keychain 丢失，无法认证；不能把“重装”当恢复方案。

**是否可恢复：** 当前可见事实可恢复；未 ACK/训练本地状态不可恢复；Keychain 丢失时不可恢复（无配对入口）。

**恢复前置条件：** Keychain token 可用；同一 dataset；服务端 tombstone/receipt 完整；用户接受全量重扫；网络/限流。

**推荐方案：** 把“重装”定义为**新客户端会话**，而不是透明恢复：显式确认、保存旧 Outbox 备份、使用新 session 目录/Keychain 项、从 nil anchor 有界重扫、显示进度与“仅当前可见历史”限制。

**需要新增的保护：** session descriptor、Keychain 迁移/重新配对、旧 Outbox 归档、重装检测、删除集保留、全量重扫限速。

### 场景 G：数据库从备份恢复

**当前实现：**
- `scripts/p7-restore.sh` 只允许恢复到显式 `rai.restore-target=true` 且 `public` 表为 0 的隔离容器；`pg_restore --single-transaction --exit-on-error --no-owner --no-acl`；拒绝非空/非隔离目标，无 `--clean`。
- 灾难恢复文档明确 `DISASTER RECOVERY RESYNC: BLOCKED`；不能通过 reset Anchor、清空 Outbox、重装 App、删 `state.json`、改 dataset epoch 替代；没有持久 recovery generation/旧代际 fencing/独立恢复流（`docs/RAI_DISASTER_RECOVERY.md:3/22-27/44-50`）。
- 恢复旧备份会同时恢复 `datasets.epoch`、`devices`、`collector_leases`、`commit_rev`；手机 Anchor/nextSequence/candidate 不会回退（`docs/RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md:11-19`）。

**潜在风险：**
- **revision 回退**：备份点后的 `commit_rev` 丢失；恢复库从旧值继续递增，可能重用已存在的 revision；客户端见过更高 revision，可能把新事件当旧/重复。
- **Anchor 领先**：iPhone 已推进到备份点之后；握手身份相同，客户端不会检测数据库回退（`OutboxStore.bind` 不比较 revision）。
- **已 ACK Bundle 重提交**：客户端已 ACK 且 GC 的 bundle 不会重传；服务端备份点后的事实缺失，客户端不知道。若旧 bundle 文件仍在，可能重传，但服务端可能返回旧 receipt 或 404。
- **Tombstone 丢失**：备份点后的删除屏障丢失；`Create → Backup → Delete → Restore` 会让已删除 Workout 重新存在（`RAI_DISASTER_RECOVERY.md:24`）。
- **Route/series/association**：sealed 第一版不支持 replacement；恢复后不能只重传 Workout 就认定 Route 完成。
- **Training 历史**：备份点后的人工 override/execution/feedback 永久丢失；HealthKit 不能重建。

**是否可恢复：** 当前 BLOCKED；只能恢复到隔离实例做人工查看，不能安全恢复手机写入。

**恢复前置条件：** 停机；保留损坏实例与手机全部状态；验证备份 SHA/来源；隔离 PG18 恢复；核对 schema/FK/45 表/回执/tombstone/publication/training；撤销/轮换备份点后设备 token；用户批准；新 recovery generation 或新 dataset 会话。

**推荐方案：** 短期采用 `docs/RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md` 的方案 B（新 dataset + 独立本地会话），保留旧备份文件供人工查看；不实现“旧库原地接续”。长期若必须接续，先实现 recovery generation + 旧 collector fencing + 客户端恢复状态机，并做真机验收。

**需要新增的保护：** recovery generation、旧 epoch fencing、恢复标记 CLI、客户端恢复状态机、删除覆盖证明、Route/coverage 完成协议、备份后训练历史损失提示、真实 pg_dump 恢复测试。

---

## 8. Tombstone / Revision / Epoch 安全审查

### 8.1 设计不变量（正常路径）

| 不变量 | 实现 | 证据 | 结论 |
|---|---|---|---|
| Dataset + Workout UUID 删除屏障 | `entity_tombstones` 通用表；Workout 用 `sample_type='HKWorkoutTypeIdentifier'`；`deleted_rev > 0` | `00001_core.sql`；`workout_bundles.go:53-58/417-537` | VERIFIED |
| DELETE revision | 删除时 `nextRev = rev + 1`，写 tombstone `deleted_rev`，更新 `datasets.commit_rev` | `workout_bundles.go:470-520` | VERIFIED |
| change_events | 删除写 `entity_kind='workout'`、`operation='delete'`、payload 含 receipt；PK `(dataset, commit_rev, ordinal)` | `workout_bundles.go:520-537`；`00001_core.sql` | VERIFIED |
| 稳定回执 | 重复 DELETE 先查 tombstone + `change_events`，返回原 receipt；无 v3 receipt 时用 tombstone revision 构造稳定 ACK | `workout_bundles.go:430-470` | VERIFIED |
| Dataset 行锁 | `SELECT ... FROM datasets WHERE id=$1 FOR UPDATE` | `store.go:135-146` | VERIFIED |
| Device / Collector fencing | `checkCollectorLease` 校验 `collector_epoch`、`can_collect`、`revoked_at IS NULL`；失败 409 `collector_fenced` | `store.go:148-175` | VERIFIED |
| 迟到 POST 拒绝 | 导入先查 Workout/sample/route tombstone；tombstone 优先于 prior receipt；`workout_deleted`/`sample_deleted` | `workout_bundles.go:19-200`；`contracts/sync-v3.md:3` | VERIFIED |
| 重复 DELETE 幂等 | 已存在 tombstone + change_event → `AlreadyDeleted:true` 原 receipt | `workout_bundles.go:430-470`；`TestWorkoutDeleteBundleIsExplicitAndIdempotent` | VERIFIED |
| Route 删除回执 | `route_deletions` 独立于 tombstone；`rai_route_delete` 返回 `route_existed`/`tombstone_created` | `00011_p3_sync_v2.sql:328-365` | VERIFIED |
| 旧 collector 写入拒绝 | 旧 `collector_epoch` 的 POST/DELETE 返回 `collector_fenced`；旧 `dataset_epoch` 返回 `dataset_epoch_changed` | `store.go:148-175`；`TestIdentitySequenceAndFencing`、`TestWorkoutDeleteFencingBeforeBarrierReplay` | VERIFIED |

### 8.2 Reset 后的破坏路径

`scripts/rai-reset-v2-data.sh` 同时：

```sql
TRUNCATE TABLE
  workout_imports, workout_routes, route_points, route_chunks, workout_samples,
  series_chunks, series_uploads, workout_statistics, workout_events,
  workout_activities, workouts, health_samples, workout_package_chunks,
  workout_packages, workout_enrichments, route_deletions, sync_pages,
  sync_batches, sync_streams, sync_upload_slots, sync_request_budgets,
  entity_changes, entity_tombstones, change_events, dirty_scopes,
  job_attempts, jobs, analysis_publications, analysis_results, analysis_runs,
  analysis_parameter_sets, training_feedback, training_executions,
  training_effective_versions, training_overrides, training_base_versions,
  training_generation_runs, training_sessions, training_configs,
  training_plans, training_commands, training_parameter_sets
CASCADE;
```

保留：`datasets`、`devices`、`collector_leases`。

因此：

1. **删除屏障被清空**：`entity_tombstones`、`route_deletions`、`change_events` 全删。
2. **Anchor 不回退**：iPhone `state.json` 仍在，anchor 继续领先；客户端不会自动重传历史。
3. **旧 Bundle 仍被接受**：`datasets.epoch` 与 `collector_leases.collector_epoch` 未变，旧 collector 的 bundle 通过 `lockDataset`；`workout_imports` 清空后没有 prior receipt 可返回，旧 bundle 会被当作新导入。
4. **已删除 Workout 可复活**：tombstone 清空后，旧 bundle/旧备份中的 Workout 可重新插入；`change_events` 删除回执也没了。
5. **旧 Collector 使用过期 epoch 写入**：epoch 未变，`collector_fenced` 不会触发；旧设备 token 仍有效（`devices` 保留）。
6. **Revision 回退/重用风险**：`datasets.commit_rev` 保留，但所有 `change_events`/`entity_changes` 被清空；新导入从旧 `commit_rev` 继续，旧客户端可能把新 revision 误认为旧操作；若同时从备份恢复 `commit_rev`，会重用已存在的 revision。
7. **训练事实丢失**：`training_overrides/executions/feedback/commands` 等全删；HealthKit 无法重建。
8. **ACK 无法核对**：`workout_imports` 全删；已 ACK 但本地未持久化回执的客户端无法 GET 原 hash；已 ACK 且 GC 的客户端更无法证明。

### 8.3 结论

- 正常路径的 tombstone/revision/epoch 机制是**强**的，测试覆盖也较好。
- 但 `rai-reset-v2-data.sh` **不满足**以下原则：
  - 原始事实、删除屏障、提交日志、回执必须在同一事务提交：脚本把它们一起删，却不提供任何重建/审计；
  - 删除优先且旧上传不得复活 UUID：脚本清空屏障，直接允许复活；
  - 旧 Collector 不能绕过 Epoch fencing：脚本保留 epoch，等于让旧 collector 继续合法；
  - 数据库恢复不能让已删除 Workout 复活：脚本本身就会造成复活窗口。
- 如果必须清除 tombstone，只能在**新 dataset + 新 epoch + 新 device + 新客户端会话**下，不继承旧事实；不能在保留 dataset 的原地 Reset 中清除。

---

## 9. 历史数据升级方案比较

### 9.1 四种方案

| 方案 | 内容 | 是否删除原始事实 | 是否可逆 | 适用前提 | 结论 |
|---|---|---|---|---|---|
| **A. 只执行 PostgreSQL Schema Migration** | `schema 17 → 18`，只做 DDL/索引/回填 | 否 | 取决于 migration；当前 CLI 无回退 allowlist | 仅 schema 变更；不解决 v2 事实兼容 | **当前 NOT REQUIRED**；未来如新增审计/epoch 字段再做，且必须 expand/contract |
| **B. 既有 V2 数据的结构性升级** | 在同一 dataset 内兼容读取/定点转换/回填 metadata；v3 bundle 叠加 | 否（理想） | 需要备份；可回退到 v2 只读 | 必须用合成非空 v2 数据集证明 `WorkoutCoreEqual`、sample hash、source_bundle_id、association 兼容 | **首选待验证方案**；未实测，PARTIALLY_VERIFIED/INFERRED |
| **C. 历史 HealthKit 数据重新同步** | 新 anchor/新 dataset，从 nil anchor 重读当前可见对象 | 否（旧 dataset 可保留） | 可重复导入；依赖 HealthKit 可见性 | 用户接受只恢复当前可见历史；新客户端会话；限流/长时间 | **可行但非无损**；不能恢复已删除/不可读/不支持的数据；不能替代备份 |
| **D. 旧 RAI 系统数据导入** | 旧 SQLite/旧服务端数据迁移 | — | — | **ADR-002 明确不做** | **NOT REQUIRED**；不实现 exporter/importer |

### 9.2 为什么 B 不是“已经安全”

`workout_bundles.go:66-95` 对已存在 Workout 的处理是：

- 若 `workoutExists` 且 `bundle_sha256` 不同，但 `bundle.Observation != nil` 且 `WorkoutCoreEqual(prior, bundle.Workout)` 为真，则允许继续；
- 否则 409 `workout_identity_conflict`。

`WorkoutCoreEqual` 比较 Event 的 op/kind/type/uuid/startUs/endUs/quantity/category/source/workout，忽略 metadata（`bundle_observation.go:251-277`）。这意味着：

- v2 Workout 的 `payload` 与 v3 `Event` 的 core 字段必须一致；v2 可能缺少 `Observation`、`source`、`workout` 细节，或者字段命名不同；未实测不能假定。
- v2 sample 的 `content_sha256` 与 v3 重算可能不同；`sameCore` 还包含 `source_bundle_id`（HealthKit Source.BundleID，不是上传 bundle hash）；若不一致，可能 409 `sample_identity_conflict`。
- v2 series/route 的旧表与 v3 新表可能重叠但不完全对应；`completeSampleTypes`/`completeRoutes` 不授权删除，旧数据不会被自动替换。

因此 B 需要：

1. 用**合成非空 v2 数据集**（不是真实数据）构造 v2 Workout + samples + series + routes + tombstone + receipt + training facts；
2. 用 v3 客户端/bundle 重导同一 Workout；
3. 断言：无 409、无重复事实、无 revision 误增、无 tombstone 丢失、无 training 丢失、旧 receipt 可查、分析 job 正确；
4. 覆盖 v2 payload 缺失字段、metadata 差异、source_bundle_id 差异、旧 sample hash 差异、旧 Route 分块；
5. 失败则回退到 C（新 dataset）。

本轮没有这样的测试；`grep` 未发现非空 v2→v3 升级测试。

### 9.3 推荐顺序

1. **B（兼容叠加）优先验证**：保留现有 dataset、tombstone、training、receipt；不删除事实。
2. 若 B 不能通过：**C + 新 dataset**（新 `datasets` 行、新 epoch、新 device、新 collector epoch、新客户端 session）；旧 dataset 保留只读/归档。
3. **A** 只在需要新增 schema 字段/索引时作为配套，不作为数据升级本身。
4. **D** 不实现。

---

## 10. Reset 设计建议

> 本节是设计建议，不是实现；本轮不写代码、不建表、不加 API。

### 10.1 五种操作必须分开

| 操作 | 作用 | 是否删除事实 | 是否改变身份 | 建议 |
|---|---|---|---|---|
| `RESET_SYNC_CURSOR` | 只重置客户端发现/上传游标 | 否 | 否 | **不实现为通用按钮**；仅损坏 anchor 时用 `controlledRediscovery` 新 namespace，旧状态保留 |
| `REBUILD_SINGLE_WORKOUT` | 单个 Workout 重新读取/重传 | 否 | 否 | 复用现有 `beginTask(forceNewBundle)`；保留 tombstone；只在用户明确选择时强制新 bundle |
| `REBUILD_ANALYSIS` | bump generation、重跑分析 | 否 | 否 | 实现为 job 级操作；保留历史 run/publication；新 run 成功后 CAS |
| `RESET_DATASET` | 放弃一个 dataset 的全部事实/状态 | 是 | **必须**新 dataset ID + epoch + device + collector | **不实现原地 Reset**；实现 operator-only “new dataset + new session”，旧 dataset 只读保留 |
| `RESET_DATABASE` | 开发环境整库 DROP/CREATE | 是 | 是 | 仅 `scripts/local/pg.sh reset`（loopback `rai_dev`）；生产禁止 |

### 10.2 如果实现新 dataset / 新会话（推荐方向）

**服务端 operator CLI（不进 HTTP API）：**

```text
rai admin new-dataset
  --source-dataset <uuid>          # 只读源，不删除
  --new-dataset <uuid>             # 或由服务端生成
  --confirm-dataset <uuid>         # 二次确认，必须与 source 匹配
  --confirm-phrase NEW-DATASET     # 显式短语
  --dry-run                        # 只打印计划/计数/依赖
  --backup-manifest <path>         # 先验证备份存在且 SHA 匹配
  --rotate-epoch                   # 必选：新 dataset epoch
  --new-device-token               # 一次性返回，库中只存 SHA-256
  --new-collector-epoch            # 必选
  --audit-actor <id>               # 审计
```

**事务顺序：**

1. `pg_advisory_xact_lock` + 锁 source dataset 行；
2. 校验 source 无活跃 job/lease（或显式 `--force` + 审计）；
3. 校验备份 manifest / 目标为空；
4. 创建新 `datasets`（新 ID、新 epoch、新 service_id）；
5. 创建新 `devices`（新 token hash、`can_collect=true`）与 `collector_leases`（新 collector epoch）；
6. 旧 dataset 标记 `retired_at`（需要 migration 18 新列）或写入审计表；不删除旧事实；
7. 提交；返回一次性 token 与客户端配置说明；
8. 审计表记录 actor、时间、source/target、计数、备份 SHA、批准单号。

**客户端：**

- 新 session UUID、独立目录、独立 Keychain 项、独立 background identifier；
- 旧 session 目录/Anchor/Outbox 原样封存，不删除；
- 新 session 从 nil anchor 有界导入；
- 旧后台回调只能落旧目录，不能推进新 anchor；
- 切换是原子 `active session` 指针；失败保留 prepared 状态。

**如果用户选择 B（兼容叠加）：**

- 不新增 dataset；只做一次受控的 v3 重导；
- 保留 `entity_tombstones`、`route_deletions`、`change_events`、`workout_imports`、training；
- 不做任何 `TRUNCATE`；
- 先在合成非空 v2 数据集验证；
- 失败回退到 C。

### 10.3 通用护栏（任何破坏性操作）

1. **显式作用范围**：dataset ID、表清单、行数预估；默认 dry-run。
2. **二次确认**：输入完整 dataset ID + 短语；不接受 `--yes` 单独确认。
3. **Dataset 身份**：目标必须是预期 dataset；禁止 `RAI_DATABASE_URL` 隐式指向生产。
4. **Device / Collector Epoch**：必须轮换；旧 token 撤销；旧 collector 写入被 fence。
5. **Revision**：不得回退；新 dataset 从 0 开始；旧 dataset 保留原 revision。
6. **删除屏障保护**：tombstone/receipt 永不删除；若必须清，只能新 dataset 不继承。
7. **原始事实保护**：Raw Facts 不删除；只允许新 dataset 重导。
8. **Outbox 保护**：先导出/封存未 ACK bundle；不得清空。
9. **事务原子性**：身份创建、审计、旧 dataset 退役在同一事务；事实不删。
10. **可恢复性**：操作前备份 + SHA 验证；操作后校验；失败可回滚到旧 dataset（只读）。
11. **幂等性**：相同请求重复执行返回同一结果/同一审计记录；不重复创建 dataset。
12. **审计记录**：actor、时间、范围、计数、备份、批准、结果；不可变。
13. **故障注入测试**：崩溃在 commit 前/后、并发 POST/DELETE/worker、epoch 不匹配、tombstone 重放、Outbox 丢失、备份恢复。
14. **不做通用 Reset API**：管理工具不得被 `application/httpapi` 导入（`AGENTS.md`）；不新增公开未授权管理 HTTP。

### 10.4 明确不建议实现

- `RESET_SYNC_CURSOR` 用户按钮：没有业务需求，且容易被误用为灾难恢复；现有 `controlledRediscovery` 已覆盖损坏 anchor 的受控场景。
- `RESET_DATASET` 原地 truncate：无法保留删除屏障/训练事实；新 dataset 更安全。
- `RESET_DATABASE` 生产入口：生产应使用新库/恢复流程，不应暴露 DROP/CREATE。
- 旧 RAI importer/exporter：ADR-002 明确不做。

---

## 11. 测试覆盖审查

### 11.1 覆盖矩阵

| 场景 | 是否已有测试 | 测试位置 | 覆盖是否充分 | 建议新增测试 |
|---|---|---|---|---|
| Anchor 重置 | 部分 | `HealthDiscoveryTests.invalidAnchorAndContractStayQuarantined`（隔离/controlledRediscovery）；`RecoveryBoundaryTests.deliveredPayloadMayBeUnavailableAfterServerRollback`（证明不可恢复） | 不足：没有“清 anchor 后重扫”的完整恢复测试 | `AnchorResetDoesNotDuplicateFactsOrResurrectDeletedWorkout`；`AnchorResetAfterServerRollbackIsBlocked` |
| Outbox 丢失 | 部分 | `RecoveryBoundaryTests.deliveredPayloadMayBeUnavailableAfterServerRollback`；`WorkoutTaskTests.bundleFileIsDurableAndReclaimedAfterAck` | 不足：只证明边界，不是恢复 | `OutboxLossWithUnackedBundleFailsClosed`；`OutboxExportImportPreservesExactBundle` |
| ACK 丢失 | 是 | `APITests.commitResponseLossAndProcessReconstruction`；`TestWorkoutBundleLostACKAnd404ThenIdempotentPOST`；`WorkoutReliabilityTests` | 充分（正常路径） | 保持；增加 reset 后 ACK 恢复 |
| 重复上传 | 是 | `TestWorkoutBundleOneRequestAtomicImport`；`TestWorkoutBundleReceiptHistoryKeepsEveryBundle`；`TestWorkoutBundleLateSampleAndConflicts` | 充分（正常路径） | 增加非空 v2 叠加重复上传 |
| 重复 DELETE | 是 | `TestWorkoutDeleteBundleIsExplicitAndIdempotent`；`TestUnknownDeleteAndSamePageDeleteWins`；`TestWorkoutDeleteBeforeUploadDurableStableAndIsolated` | 充分（正常路径） | 增加 reset 后重复 DELETE / 旧 receipt 重放 |
| Tombstone 保留 | 是 | `TestCommitRetryAndDeletionBarrier`；`TestWorkoutDeleteBeforeUploadDurableStableAndIsolated`；`TestRouteDeleteAndStaleReplayBarrier` | 充分（正常路径）；**无 reset 后保留测试** | `ResetMustNotDeleteTombstones`；`DatasetResetPreservesBarrierOrUsesNewDataset` |
| 旧 Bundle 迟到 | 是 | `TestWorkoutBundleLateSampleAndConflicts`；`TestWorkoutDeleteFencingBeforeBarrierReplay`；`TestWorkoutBundleLateCompleteSnapshotRetainsChildren` | 充分（正常路径） | 增加 reset 后旧 bundle 拒绝 |
| Epoch 失效 | 是 | `TestIdentitySequenceAndFencing`；`TestWorkoutDeleteFencingBeforeBarrierReplay` | 充分（正常路径）；**无 epoch 轮换测试** | `DatasetEpochRotationRejectsAllOldBundles`；`CollectorEpochRotationRejectsOldCollector` |
| Dataset Reset | 否 | — | **无** | `DatasetResetRequiresNewEpochAndDevice`；`DatasetResetDoesNotDeleteSourceDataset` |
| Worker 中断 | 是 | `TestConcurrentJobClaimAndCrashRecovery`；`TestJobFailureRetryAndAttemptLimit`；`TestWorkoutDeleteSupersedesInFlightWorkers`；`TestPhase4SnapshotCancelLockFailureAndWorkerRetry` | 充分（正常路径） | 增加 reset/new-dataset 与 worker 并发 |
| 数据库恢复 | 部分 | `TestDisasterRecoveryManualHistoryBoundary`；`TestDisasterRecoveryExistingContractBoundary`（模拟旧快照，不是 pg_dump） | 不足 | 真实 `pg_dump`/`pg_restore` + v3 客户端 + tombstone 缺失场景 |
| 分析重新发布 | 是 | `TestAnalysisPublishImmutableQueryAndDelete`；`TestAnalysisGenerationCASAndLostFence`；`TestAnalysisOldGenerationCannotReplaceNewPublication` | 充分（正常路径） | 增加“删 analysis 后无 dirty generation 不重算”的反例；`REBUILD_ANALYSIS` 保留历史 |
| 非空 v2 → v3 升级 | 否 | — | **无** | `NonEmptyV2DatasetV3OverlayPreservesFactsAndBarriers`；`V2PayloadMismatchFailsClosed` |
| 重装 App | 否 | — | **无** | `ReinstallWithKeychainResumesSameScopeWithoutDuplicateFacts`；`ReinstallWithoutKeychainRequiresRePair` |
| Reset 并发 POST/DELETE/Worker | 否 | — | **无** | `ResetConcurrentWithImportDeleteAndWorker`（故障注入） |
| Reset 脚本本身 | 否 | — | **无** | `ResetScriptRefusesNonDevTarget`；`ResetScriptDryRunCountsAndAudit`；`ResetScriptRefusesWithoutConfirmation` |

### 11.2 结论

- **正常路径的删除、fencing、ACK、分析发布测试较好**；已有测试能证明“在正常协议下，tombstone 优先、重复导入幂等、ACK 丢失可 GET 核对、旧 epoch 被拒”。
- **Reset/升级路径几乎没有测试**：没有 reset 脚本测试、没有 Dataset Reset、没有非空 v2→v3、没有重装、没有真实备份恢复、没有 reset 与并发写入的竞态。
- 按任务原则：“没有测试证据时不能声称安全”。因此 RQ-010 当前**不能**判定为可安全实施。
- 特别关注：Reset 发生在并发 POST/DELETE/Worker 执行过程中时，当前脚本没有 `LOCK TABLE`/`lock_timeout`/审计；`TRUNCATE` 会取 `ACCESS EXCLUSIVE` 锁，可能阻塞/被阻塞，且无法证明与在途事务的顺序。**没有测试覆盖这一点。**

---

## 12. Migration 与部署风险

### 12.1 当前 migration 状态

| 项目 | 结论 | 证据 |
|---|---|---|
| 00001–00017 是否存在 | 是，17 个 | `apps/server/migrations/` |
| checksum 是否一致 | **17/17 一致** | `shasum -a 256` vs `checksums.json` |
| 历史 migration 是否可变 | 否；`checksums.json` + 00001–00016 注释 “No Down” | migration 文件头 |
| migration 17 索引 | `health_samples_analysis_identity`，普通 `CREATE INDEX`（非 CONCURRENTLY） | `00017_analysis_sample_lookup.sql` |
| 是否需要 migration 18 | 当前 RQ-010 不需要纯 schema 18；若新增审计表/epoch 轮换列则需要 | 本报告第 10 节 |
| 回滚策略 | `reversibleMigrations = {}`；`migrate-down` 拒绝所有版本 | `admin.go:27-31/101-168` |
| 生产是否已部署 | 否；fnOS 为隔离开发环境 | `docs/P7_OPERATIONS.md:3`；`docs/IMPLEMENTATION.md:359/467` |

### 12.2 00017 的生产锁表风险（必须保留）

`00017_analysis_sample_lookup.sql` 使用的是：

```sql
CREATE INDEX health_samples_analysis_identity
ON health_samples(dataset_id, sample_uuid, sample_type)
INCLUDE (event_start, event_end, value_numeric, unit, content_sha256);
```

不是 `CREATE INDEX CONCURRENTLY`。因此：

- 在 PostgreSQL 中，普通 `CREATE INDEX` 会对 `health_samples` 取 `SHARE` 锁，**阻塞 INSERT/UPDATE/DELETE**（读取通常不阻塞），直到索引构建完成；
- 构建期间产生 WAL、I/O、磁盘临时空间；大表可能持续数分钟到数十分钟；
- 迁移由 `goose` 执行，`withMigrationLock` 只持有 advisory lock，不设置 `statement_timeout`/`lock_timeout`；没有“构建超时自动取消”的保护；
- 00017 已写入 `checksums.json`，**不可修改**；不能把 `CREATE INDEX` 改成 `CONCURRENTLY`；
- 若未来需要在线建索引，应新建 migration 18，使用 `-- +goose NO TRANSACTION` + `CREATE INDEX CONCURRENTLY`，并配套 `pg_index` 有效性检查、失败重试、回滚（DROP INDEX）与维护窗口；
- 本轮不得运行生产迁移，也不得修改 00017。

### 12.3 未来 migration 18 的设计约束

如果 RQ-010 需要新增：

- `datasets.retired_at` / `dataset_generation`；
- `admin_operations` 审计表；
- `recovery_generation` / `old_collector_fencing`；
- 或 v2→v3 兼容回填字段；

则：

1. 使用 **expand/contract**：先加可空列/新表，不阻塞写入；再回填；最后加约束。
2. 大表回填分批（按 dataset/time/主键范围），每批短事务；可中断/续跑；记录进度。
3. 索引使用 `CONCURRENTLY`（独立 migration，no transaction），并验证 `indisvalid`。
4. 不删除旧列/旧表；不 `TRUNCATE`；不修改历史 migration。
5. 提供 Down 或明确“不可回退”的理由；`reversibleMigrations` 只对经审查的 migration 放行。
6. 迁移前备份 + SHA 校验；迁移后核对 45 表、FK、关键计数、回执/tombstone。
7. 故障注入：迁移中断、并发写入、锁等待、磁盘满、WAL 膨胀。

### 12.4 部署风险

- 生产尚未创建/部署（`docs/P7_OPERATIONS.md:3`）；fnOS 是隔离开发环境，不是生产。
- 不能用 fnOS 的 schema 14/15 状态推断生产；文档 `docs/IMPLEMENTATION.md:5` 写 schema 16，实际 17，说明文档需要同步。
- `00017` 的锁表风险必须在发布 runbook 中保留；不得以“本地 109ms 查询”推断生产索引构建时间。
- 任何 reset 脚本都不得进入自动 migration/部署流水线；当前 `docs/IMPLEMENTATION.md:456` 已写“不得作为自动 migration”，应保留并在发布检查表中强制。

---

## 13. 必须由用户决定的问题

以下问题不解决，RQ-010 不能进入实施。

### Q1. 是否保留现有非空 V2 数据集？

- **选项 A：保留**（推荐先验证兼容叠加）——保留 `workouts/health_samples/routes/training/receipts/tombstones`，在同一 dataset 内验证 v3 叠加。
- **选项 B：放弃旧在线数据集，新建 dataset 重导**——旧 dataset 只读归档；新 dataset 从 HealthKit 当前可见历史导入；接受旧训练/删除历史不进入新库。
- **选项 C：原地破坏性 reset**——不推荐；会丢 tombstone/training/receipt；若用户坚持，必须先接受第 10 节的全部护栏与新 dataset 替代方案。

**需要用户明确：** 现有 fnOS `rai-v2-device` 数据集是否仍是非空 v2 事实；是否已执行过 reset；316 Workouts / 20,750 samples / 14 routes / 496 publications / 0 training plans 是否仍需保留。

### Q2. 是否接受“兼容叠加”的验证成本？

- 选项 A：授权用**合成非空 v2 数据集**做 v3 叠加试验（不碰真实数据）。
- 选项 B：不做叠加，直接新 dataset 重导。
- 选项 C：暂不决定，RQ-010 保持 BLOCKED。

### Q3. 训练用户事实是否必须保留？

- `training_overrides`、`training_executions`、`training_feedback`、`training_plans` 等不能从 HealthKit 重建。
- 当前开发数据集 0 training plans，但生产策略必须明确：保留 / 放弃 / 迁移到新 dataset（需要单独导入工具，不能从 HealthKit 重建）。

### Q4. 是否允许轮换 dataset epoch / collector epoch 并要求客户端重新绑定？

- 任何安全的破坏性操作都需要。
- 当前 `OutboxStore.bind` 拒绝不同 Scope；没有生产级重新配对 UI。
- 需要用户确认：是否接受一次性人工重新配置/重装/新 session。

### Q5. 是否需要实现新的 operator-only 管理工具？

- 选项 A：只做一次性部署 runbook，不新增 CLI。
- 选项 B：新增 `rai admin new-dataset`（推荐，若选择新 dataset）。
- 选项 C：新增 `RESET_DATASET` 原地工具（不推荐）。

### Q6. 灾难恢复策略选 A 还是 B？

- A：恢复旧库并接续——需要 recovery generation、旧 collector fencing、删除覆盖证明；当前 BLOCKED。
- B：新 dataset + 独立本地会话——推荐以后验证；当前客户端会话切换未实现。
- 需要用户确认是否接受 B 的“旧在线训练历史不保留，仅保留备份文件”。

### Q7. 是否允许 migration 18 新增审计/epoch 字段？

- 需要生产维护窗口、备份、expand/contract、回滚计划。
- 如果 Q5 选 A（只做 runbook），可能不需要 migration 18。

### Q8. 旧 RAI 数据是否确认不迁移？

- ADR-002 已明确“不迁移旧数据”；请确认该决策未变化。
- 若用户现在要求迁移旧 RAI SQLite，则属于新的范围，需单独立项；不能塞进 RQ-010。

---

## 14. RQ-010 实施建议

### 14.1 推荐路线（按顺序）

**阶段 0：决策（本轮之后，仍不写代码）**

1. 用户回答第 13 节 Q1–Q8。
2. 若 Q1 选“保留”，进入阶段 1B；若选“新 dataset”，进入阶段 1C；若选“原地 reset”，先拒绝并解释风险，要求重新选择。

**阶段 1B：兼容叠加可行性验证（合成数据，不碰真实数据）**

1. 构造合成非空 v2 数据集：Workout + scalar samples + series + routes + tombstone + `workout_imports` receipt + training override/execution/feedback + analysis publication。
2. 用 v3 客户端/bundle 重导同一 Workout。
3. 断言：
   - 无 `workout_identity_conflict` / `sample_identity_conflict`；
   - `health_samples`/`workouts` 计数不重复；
   - `commit_rev` 仅在 changed 时递增；
   - tombstone 仍阻止已删除 UUID；
   - `workout_imports` 保留旧回执，新回执按 hash 追加；
   - training facts 不变；
   - analysis job 正确入队/发布。
4. 覆盖 v2 payload 缺字段、metadata 差异、`source_bundle_id` 差异、旧 hash 差异、Route 分块。
5. 若全部通过：形成“兼容叠加”实施设计；若失败：转阶段 1C。

**阶段 1C：新 dataset + 新会话设计（若选择放弃旧在线数据集）**

1. 设计 `rai admin new-dataset`（第 10.2 节），operator-only，不进 HTTP。
2. 设计客户端 session descriptor / 目录 / Keychain / background identifier 隔离。
3. 设计旧 dataset 只读归档 / `retired_at`（migration 18）。
4. 设计审计表与备份校验。
5. 合成测试：新 dataset 初始化、旧 token 401、旧 collector fenced、旧 bundle 拒绝、新客户端从 nil anchor 导入、崩溃恢复、并发 worker。

**阶段 2：实现（只有用户批准后）**

- 仅实现被批准的路径；不实现通用 Reset API。
- 任何 migration 18 使用 expand/contract；00017 不动。
- 任何破坏性操作先 dry-run + 备份 + 审计 + 双人确认。
- 测试必须包含故障注入与并发。

**阶段 3：验收**

- 隔离 PG18 + 合成数据：migration、导入、删除、分析、publication、restore。
- 真实 iPhone/HealthKit：仅当前可见历史；不导出健康值/GPS。
- 不在生产执行，直到用户明确批准。

### 14.2 明确不做

- 不执行 `scripts/rai-reset-v2-data.sh`。
- 不修改任何 migration/checksums/生产代码/测试。
- 不实现旧 RAI importer/exporter。
- 不实现通用 `RESET_DATASET` HTTP API。
- 不把 Reset 作为 Schema Migration 的替代。
- 不把“重新同步全部 HealthKit”当作清理数据库的默认手段。

### 14.3 成功标准

- 用户明确数据保留策略；
- 现有 V2 数据集要么通过合成验证被兼容升级，要么被新 dataset 隔离替代；
- tombstone/receipt/training 在升级后仍可验证；
- 旧 epoch/旧 token 被 fence；
- 客户端有明确 session 切换与恢复路径；
- 测试覆盖 reset 并发、epoch 轮换、Outbox 丢失、非空 v2→v3、真实备份恢复；
- 生产迁移 runbook 包含 00017 锁表提示与回滚/备份方案。

---

## 15. 最终验收结论

### 15.1 三项结论

```text
A. 是否需要历史数据升级？
REQUIRED
（现有非空 V2 数据集若保留，必须升级；旧 RAI 数据迁移 NOT REQUIRED；
  schema 17→18 当前 NOT REQUIRED；具体方式 NEEDS_PRODUCT_DECISION。）

B. 是否需要 Reset 功能？
NEEDS_PRODUCT_DECISION
（不需要通用 Reset；若选择新 dataset，需要一次性 operator-only
  “新 dataset + 新 epoch + 新 device + 新客户端会话”；若兼容叠加则不需要 Reset。）

C. 是否允许进入 RQ-010 实施阶段？
BLOCKED
```

### 15.2 阻断原因

1. **数据删除范围未定**：是否保留现有 V2 数据集、训练历史、删除屏障，用户未确认。
2. **数据权威性未验证**：v2→v3 原地叠加的 `WorkoutCoreEqual` / sample hash / source_bundle_id / association 兼容性没有合成非空 v2 测试。
3. **恢复策略未定**：灾难恢复 BLOCKED；没有 epoch 轮换、客户端重绑定、recovery generation。
4. **现有 reset 脚本违反核心原则**：删除 tombstone/receipt/training，保留 epoch，无审计/环境校验/并发保护。
5. **测试证据不足**：没有 reset/升级/重装/真实备份恢复测试；没有 reset 与并发 POST/DELETE/Worker 的竞态测试。

### 15.3 解除 BLOCKED 的最小条件

- 用户回答第 13 节 Q1–Q8，尤其是 Q1（保留 vs 新 dataset）和 Q3（训练事实）。
- 若保留：完成合成非空 v2→v3 叠加试验，证明事实不重复、tombstone/receipt/training 不变。
- 若新 dataset：完成 operator-only 新 dataset/新会话设计 + 合成测试。
- 任何破坏性路径：必须包含 epoch 轮换、旧设备撤销、客户端 session 切换、备份/审计/双人确认。
- 补齐第 11 节列出的 reset/升级/恢复测试。

### 15.4 本轮停止状态

```text
RQ-010 READ-ONLY AUDIT: COMPLETE
IMPLEMENTATION: NOT STARTED
RESET EXECUTED: NO
MIGRATION EXECUTED: NO
DATABASE CONNECTED: NO
CODE/TEST/MIGRATION MODIFIED: NO
GIT COMMIT/PUSH: NO
PRODUCTION DEPLOYED: NO
NEXT: WAIT FOR USER DECISION ON RQ-010 SCOPE
```

---

## Appendix A — 证据索引

| # | 结论 | 文件 / 位置 | 类型 | 等级 |
|---|---|---|---|---|
| E1 | RQ-010 原始定义 | `docs/audits/RQrun_WORKOUT_SYNC_REVIEW.md:242`、`:430` | 审查报告 | VERIFIED |
| E2 | v2→v3 reset 合同 | `contracts/sync-v3.md:18`；`contracts/CHANGELOG.md:32`；`docs/IMPLEMENTATION.md:456` | 合同/文档 | VERIFIED |
| E3 | reset 脚本 TRUNCATE 清单与保留清单 | `scripts/rai-reset-v2-data.sh:1-63` | 脚本 | VERIFIED |
| E4 | 45 表 schema | `apps/server/migrations/00001_core.sql` … `00017_analysis_sample_lookup.sql` | migration | VERIFIED |
| E5 | checksum 17/17 | `apps/server/migrations/checksums.json` | 配置 | VERIFIED |
| E6 | dataset 行锁 / epoch / collector fencing | `apps/server/internal/storage/postgres/store.go:135-175` | Go 源码 | VERIFIED |
| E7 | Workout 导入 tombstone/receipt 逻辑 | `apps/server/internal/storage/postgres/workout_bundles.go:19-200` | Go 源码 | VERIFIED |
| E8 | Workout DELETE 幂等/回执/change event | `workout_bundles.go:417-537` | Go 源码 | VERIFIED |
| E9 | `workout_imports` 级联删除 | `apps/server/migrations/00014_workout_imports.sql:16` | migration | VERIFIED |
| E10 | `entity_tombstones` 通用屏障 | `apps/server/migrations/00001_core.sql`；`00011_p3_sync_v2.sql:328-365` | migration | VERIFIED |
| E11 | iOS Anchor 两阶段推进 | `ios/RAI/Sources/SyncCore/HealthDiscovery.swift:98-157` | Swift 源码 | VERIFIED |
| E12 | iOS Outbox 原子状态 / ACK / GC | `ios/RAI/Sources/SyncCore/OutboxStore.swift:1-120/266/376/455/626` | Swift 源码 | VERIFIED |
| E13 | iOS ACK 严格校验 / GET 核对 | `ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift:5-31/40-107` | Swift 源码 | VERIFIED |
| E14 | iOS task exact bundle identity | `ios/RAI/Sources/SyncCore/WorkoutSync.swift:67-109` | Swift 源码 | VERIFIED |
| E15 | iOS 重装/Keychain/Scope 约束 | `ClientConfiguration.swift:7-27`；`OutboxStore.swift:153-155` | Swift 源码 | VERIFIED |
| E16 | 无 HTTP Reset 入口 | `apps/server/internal/httpapi/server.go:44-77`；`contracts/openapi-v2.yaml`（无 reset 路径） | Go/合同 | VERIFIED |
| E17 | Admin 只读 / Init 仅空库 | `apps/server/internal/admin/operations.go:16-24`；`admin.go:208-245` | Go 源码 | VERIFIED |
| E18 | migration 17 普通 CREATE INDEX | `apps/server/migrations/00017_analysis_sample_lookup.sql` | migration | VERIFIED |
| E19 | 灾难恢复 BLOCKED | `docs/RAI_DISASTER_RECOVERY.md:3/22-27/44-50` | 审查报告 | VERIFIED |
| E20 | 最小恢复 A/B 方案 BLOCKED | `docs/RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md:5/11-19/41` | 审查报告 | VERIFIED |
| E21 | 旧 RAI 不迁移 | `docs/adr/002-fresh-healthkit-dataset.md:3/7/15` | ADR | VERIFIED |
| E22 | 正常路径测试覆盖 | `apps/server/integration/*`；`ios/RAI/Tests/SyncCoreTests/*` | 测试 | VERIFIED |
| E23 | 无 reset/升级测试 | grep `Test*Reset*`、`DatasetReset`、`OutboxLoss`、`DatabaseRestore` 等无命中 | 测试清单 | VERIFIED（缺失证据） |
| E24 | v2→v3 原地叠加代码可能性 | `workout_bundles.go:66-95/107/186-190`；`bundle_observation.go:251-277` | Go 源码 | PARTIALLY_VERIFIED / INFERRED（未实测） |
| E25 | 现有 V2 数据集非空且可能未 reset | `docs/IMPLEMENTATION.md:359/467`；`docs/evidence/workout-bundle-2026-10-31.json` knownLimitations | 文档/证据 | PARTIALLY_VERIFIED；reset 是否执行 UNKNOWN |

### Appendix A.1 — 关键结论的结构化证据

| 关键结论 | 文件 / 位置 | migration | 现有测试 | 等级 |
|---|---|---|---|---|
| RQ-010 是 v2→v3 破坏性 reset 缺陷，不是旧 RAI 迁移 | `docs/audits/RQrun_WORKOUT_SYNC_REVIEW.md:242` | 00014/00015（v3 receipt） | 无专项测试 | VERIFIED |
| reset 脚本 TRUNCATE 42 表，含 tombstone/receipt/training | `scripts/rai-reset-v2-data.sh:1-63` | 00001–00017 全部受影响 | 无 reset 脚本测试 | VERIFIED |
| reset 保留 datasets/devices/collector_leases 与 epoch | `scripts/rai-reset-v2-data.sh:1-63` | 00001（datasets/devices/collector_leases） | `TestIdentitySequenceAndFencing` 只覆盖正常 fencing | VERIFIED |
| Workout DELETE 写 tombstone + change_event + 稳定回执 | `apps/server/internal/storage/postgres/workout_bundles.go:417-537` | 00001/00011/00014 | `TestWorkoutDeleteBeforeUploadDurableStableAndIsolated`；`TestWorkoutDeleteBundleIsExplicitAndIdempotent` | VERIFIED |
| 删除 Workout 级联删除 `workout_imports` payload/receipt | `apps/server/migrations/00014_workout_imports.sql:16` | 00014 | `TestWorkoutBundleReceiptHistoryKeepsEveryBundle` | VERIFIED |
| Anchor 在事件落盘后推进，不等待上传 ACK | `ios/RAI/App/SyncCoordinator.swift:76-110` | n/a | `RecoveryBoundaryTests.deliveredPayloadMayBeUnavailableAfterServerRollback` | VERIFIED |
| Outbox 以 exact bundle 字节为恢复权威，ACK 后才 GC | `ios/RAI/Sources/SyncCore/OutboxStore.swift:266/376/626` | n/a | `WorkoutTaskTests.bundleFileIsDurableAndReclaimedAfterAck`；`GarbageCollectionTests.*` | VERIFIED |
| 无 HTTP/CLI Dataset Reset 入口 | `apps/server/internal/httpapi/server.go:44-77`；`admin/operations.go:16-24` | n/a | 无 reset 路由测试 | VERIFIED |
| v2→v3 原地叠加在代码层可能，但未实测 | `workout_bundles.go:66-95/107/186-190`；`bundle_observation.go:251-277` | 00003/00014/00015 | 无非空 v2 叠加测试 | PARTIALLY_VERIFIED / INFERRED |
| 灾难恢复重同步 BLOCKED | `docs/RAI_DISASTER_RECOVERY.md:3/22-27/44-50` | n/a | `TestDisasterRecoveryManualHistoryBoundary`；`TestDisasterRecoveryExistingContractBoundary` | VERIFIED |
| 00017 普通 CREATE INDEX 有锁表风险 | `apps/server/migrations/00017_analysis_sample_lookup.sql` | 00017 | `TestMigrationsIdempotentAndInitializationProtected`（不测锁时长） | VERIFIED（锁语义）/ INFERRED（生产时长） |
| 现有 V2 数据集非空且可能未 reset | `docs/IMPLEMENTATION.md:359/467` | n/a | 无 | PARTIALLY_VERIFIED；reset 执行 UNKNOWN |

## Appendix B — 证据等级说明

| 等级 | 含义 |
|---|---|
| VERIFIED | 本轮直接读取源码/migration/合同/测试并核对；结论可复现 |
| PARTIALLY_VERIFIED | 部分证据存在，但缺少关键实测或环境确认 |
| INFERRED | 由源码结构/历史文档推断，未直接验证 |
| UNKNOWN | 没有足够证据；不得当作事实 |

## Appendix C — 本轮限制

- 未连接 PostgreSQL、fnOS、生产或任何持久数据库；未执行 `SELECT`/`EXPLAIN`/`pg_dump`。
- 未执行 `scripts/rai-reset-v2-data.sh`、`pg.sh reset`、`p7-restore.sh`、任何 `TRUNCATE`/`DELETE`。
- 未运行 Go/Swift 测试（避免产生数据库/设备副作用）；测试覆盖结论来自源码与测试清单的只读检查。
- 未读取或导出真实 HealthKit 健康值、GPS、Token、密码、设备凭证。
- 未验证真实 iOS 重装、真实 HealthKit 删除、真实后台强杀、真实 pg_dump 恢复。
- 未修改任何生产代码、测试、migration、checksums、协议、部署配置。
- 未 commit/push/deploy。

---

**报告结束。等待用户确认 RQ-010 实施范围。**
