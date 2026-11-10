# RAI 完整性证明下线方案：Readiness 只认“上传成功”

**状态：已实施（2026-11-10）。** 代码、迁移、契约与测试均已按本文执行；实施证据见 [IMPLEMENTATION.md](IMPLEMENTATION.md) 的“Workout completeness 控制面下线 — 已实施”。本文保留为决策与影响面原文。

决策日期：2026-11-10。决策人：用户。

参照物：[RQrun HealthKit 上传协议](RQrun-HealthKit-上传协议.md)。RQrun 的判据只有一条——**这条记录上传成功了吗**（`POST /Api/Record/upload_record` 回 `syscode=200`，再用 `GET /Record/code_apple_source_status` 查 `status=1`）。它没有“本轮是否把 HealthKit 全部记录都传上去了”的概念，也从不因为集合不全而拒绝分析。

本文按同一判据改造 RAI：**删除整个 Workout 完整性（completeness）控制面，Readiness 不再依赖覆盖证明。**

本文取代以下旧要求，旧文保留为历史：

- [WORKOUT_SYNC_COMPLETENESS_CONTRACT.md](WORKOUT_SYNC_COMPLETENESS_CONTRACT.md) 全文
- [WORKOUT_SYNC_V6_REIMPORT_PLAN.md](WORKOUT_SYNC_V6_REIMPORT_PLAN.md) §2 阶段 C“v6 专用覆盖证明”与 §5
- `contracts/workout-completeness-v2.md`、`contracts/workout-metadata-registry-v2.md` 中关于控制面 completeness 的部分
- 本会话上一轮交付的“新增 `workout-sync-completeness/v3`”方案（C1/C2/C3/C4）——**整体作废，不再实施**

---

## 0. 结论

这条死锁的根因不是“v6 忘了续期证明”，而是**证明这个机制本身不该存在**。删除它比补一个新版本便宜得多：

| 事实 | 证据 |
| --- | --- |
| 客户端从未实现完整性证明 | `ios/` 全量 grep `WorkoutCompleteness` / `workout-sync-completeness` 零命中；`contracts/sync-v6.md:32` 明确 v6 不携带 observation/coverage/completeness |
| 服务端控制面无人写入 | `workout_sync_completeness` 仅有两处 INSERT：`postgres/workout_completeness.go:215`（v1）、`postgres/workout_completeness_v2.go:234`（v2），且都只能由客户端主动 POST 触发 |
| 它现在唯一的作用是**阻塞** | `readiness/v2.go:185` 与 `postgres/model_stack_inputs.go:348` 把零值 Coverage 变成 `load_sync_incomplete` + 清空当日负荷 |
| 删除是纯减法 | 不新增表、不新增协议、不改客户端 |

因此本方案的性质是**撤回一条过期要求**，不是“修复一个 bug”。`docs/IMPLEMENTATION.md:1453` 那条“已知缺陷”在实施后应改写为“已按产品决定取消”，不得写成“已修复”。

---

## 1. 新语义

判据从“集合是否齐全”改为“上传是否成功”。**上传成功 = `workout_ingests.state = 'committed'`**（回执 `IngestStatus:"committed"`，`postgres/workout_ingests` + `migrations/00050_workout_ingests.sql`）。这条证据本来就存在，不需要新端点。

| 场景 | 现状（完整性门槛） | 新行为 |
| --- | --- | --- |
| 全部记录上传成功 | `load_sync_incomplete`（因为无证明） | 正常分析，无 sync 类 reason |
| 实际 5 条只传了 3 条 | `load_sync_incomplete` | 按已提交的 3 条分析（与 RQrun 一致） |
| 从未上传过任何 Workout | `load_sync_incomplete` | 无 sync 类 reason；由 `load_baseline_insufficient`（<5 训练日 / <14 天跨度）如实表达 |
| 某条 Workout 负荷不可用（缺 HR、参数冲突） | 被覆盖门槛掩盖 | `recent_load_unknown` / `load_parameter_conflict`（判据本来就有，无需新增） |
| 今天没有新上传 | 36h 后 `load_sync_incomplete` | 当日不生成 anchor，产品显示“暂无数据”；`candidate_superseded` 保证昨日 anchor 不冒充今天 |

第三条关键：**服务端无法区分“用户只传了 3 条”和“只有 3 条”**。这是原设计存在的全部理由，也是本次删除的代价。该代价已被明确接受（RQrun 同样如此），必须在文档中登记为“已接受的产品决定”，否则下一轮又会被当成缺陷修回来。

---

## 2. 影响面清单

### 2.1 直接删除

| 位置 | 内容 |
| --- | --- |
| `internal/httpapi/server.go:75` | `POST /v2/sync/workout-completeness` 路由 |
| `internal/httpapi/server.go:352` | 能力广告 `workoutSyncCompletenessVersion`、`workoutSyncCompletenessV2Version` |
| `internal/httpapi/workout_completeness.go` | handler 全文件 |
| `internal/syncing/store.go:117-118` | `CommitWorkoutCompleteness`、`CommitWorkoutCompletenessV2` |
| `internal/syncing/workout_completeness.go` | v1 契约 + 校验（`WorkoutCompletenessContract`、`verifyCompletenessWorkout`，含 `:255-260` 的 `>=4 → 409`） |
| `internal/syncing/workout_completeness_v2.go` | v2 契约、分页链、集合哈希比对、排除重分类 |
| `internal/storage/postgres/workout_completeness.go` | v1 提交 + 事实版本计算 |
| `internal/storage/postgres/workout_completeness_v2.go` | v2 提交 |
| `internal/storage/postgres/workout_completeness_coverage.go` | 读侧 `verifiedWorkoutCompleteness{,V2}`（Readiness 唯一覆盖来源） |
| `internal/syncing/workout_completeness_test.go`、`workout_completeness_v2_test.go` | 单测 |
| `integration/workout_completeness_test.go`、`workout_completeness_v2_test.go`、`workout_completeness_simulator_test.go` | 集成用例 |
| `internal/storage/postgres/dbgen/models.go:725` | `WorkoutSyncCompleteness` 生成模型（sqlc 重新生成） |
| `contracts/fixtures/workout-completeness-v1.json`、`-v2.json` | 契约夹具（随测试删除；若要留证移入 `docs/archive/`） |

### 2.2 保留但需改注释/语义

| 位置 | 处理 |
| --- | --- |
| `internal/syncing/source_policy.go` | **保留**。它现在是接收期来源归类（`workout_bundle_v6.go:295` 的 `422 source_policy_excluded`），与完整性无关。仅改 `:13-23` 注释里“不可归类只影响 completeness”的说法，改为“不可归类即拒绝该记录（fail closed）或按策略排除，与覆盖无关”。`WorkoutSourcePolicyVersion` 能力继续广告（客户端用它做本地过滤） |
| `internal/athletemodel/contract.go:63-64` | `load_sync_incomplete` 不再产生，但保留映射（历史 artifact 仍可能带该码，映射成 `data_sync_incomplete` 无害） |
| `change_events` 中 `entity_kind='workout_completeness'` 历史行 | 保留（审计），不删 |
| `workout_imports` / `workout_ingests` / `raw_json` | 全部保留，与本次无关 |

### 2.3 Readiness 侧（核心改造）

| 位置 | 现状 | 改为 |
| --- | --- | --- |
| `readiness/model.go:77-83` | `type Coverage struct{Identity,From,Through,CaughtUp,Synthetic,StreamHash}` | 删除整个类型 |
| `readiness/model.go:123` | `ReadinessSnapshot.Coverage` | 删除字段 |
| `readiness/model.go:175` | `Result.Coverage`（`json:"loadCoverage"`） | 删除字段 |
| `readiness/model.go:43` | `Config.CoverageFreshUs`（36h） | 删除（见 §3.2 为什么不需要替代闸门） |
| `readiness/v2.go:80` | `known := in.Coverage.CaughtUp && in.Coverage.From <= d.Start && in.Coverage.Through >= d.End` | `known := d.End <= in.AsOf` |
| `readiness/v2.go:185-188` | `load_sync_incomplete` + `status=sync_incomplete` | 整块删除 |
| `readiness/calculate.go:65`、`:160-163`、`:225-227` | legacy v1 同三处 | 同改（见 §3.3 取舍） |
| `postgres/readiness_input.go:25` | assignment 里 `Coverage`（JSON `syntheticCoverage`） | 删除字段 |
| `postgres/readiness_input.go:57-79` | `x.Coverage = a.Coverage` → streams manifest 查询 → `Synthetic && StreamHash` 校验 → `verifiedWorkoutCompleteness` 覆盖 → Through clamp | 整段删除 |
| `postgres/model_stack_inputs.go:333-341` | `verifiedWorkoutCompletenessV2` + Through clamp | 删除 |
| `postgres/model_stack_inputs.go:348-354` | 覆盖不成立时清空 `RecordedLoad/Acute/Chronic/Balance/Ratio/Trend7` 并置 `insufficient` | **整块删除**。逐日 `d.Quality` 已由 `tl.CalculateLoadStateV2` 按负荷质量判定，不需要这层全局抹除 |
| `postgres/model_stack_inputs.go:400` | `readyInput.Coverage = coverage` | 删除 |
| `postgres/model_stack_inputs.go:415` | `load_state` 的 `inputHash` 结构体含 `Coverage rd.Coverage` | 删除该字段 |
| `postgres/model_stack_research.go:50`、`:156-175` | `loadSettling(..., coverage)` 用 `coverage.CaughtUp && day.Quality=="complete"` | 改为 `day.Quality == "complete"` |
| `postgres/readiness_publish.go:18` | lineage 含 `"coverage": in.Coverage` | 删除该键 |

### 2.4 迁移

| 迁移 | 内容 |
| --- | --- |
| 新 `00051_drop_workout_completeness.sql` | `DROP TABLE workout_sync_completeness`（连带 `00028` 的不可变触发器 `workout_completeness_immutable`、dirty 触发器 `workout_completeness_dirty`、`00041:113` 的 `model_stack_dirty_proof`、`00029` 的 6 列与 3 个约束）；`DROP FUNCTION rai_workout_completeness_immutable()`、`rai_workout_completeness_dirty()`；`DROP INDEX workout_completeness_fact_revision`（`change_events` 上的索引，仅为证明的事实版本查询而建） |
| 同迁移或 `00052` | `CREATE OR REPLACE FUNCTION rai_readiness_edges()`：去掉 `(dataset,'readiness/load-coverage/v1',scope)` 这条幽灵上游边（`00041:196-210`），并 `DELETE FROM analysis_model_dependencies WHERE upstream_scope='readiness/load-coverage/v1'`（该 scope 从未发布，是 completeness 时代的产物） |

`migrations/latest.go` 的 `LatestVersion()` 相应推进；`migrations/checksums.json` 由工具重新生成。

**注意**：`00028`/`00029`/`00041` 是历史迁移，不得改写（已应用库的校验和会失配）。一切通过新迁移做减法。

---

## 3. 版本、哈希与运维动作（最容易踩的部分）

### 3.1 Config 与 assignment 会失配，必须重签

`postgres/readiness_input.go:44-46` 硬校验 `a.Hash == rd.DefaultConfig(tz).Hash()`，否则返回 `analysis.Invalid()`。删掉 `Config.CoverageFreshUs` 会改变 `Config.Hash()`，于是**部署后所有 dataset 的 readiness 作业立即失败**，产品层会显示 `data_sync_incomplete` / `technical_failure`——比现状更糟。

因此实施顺序必须包含一步运维动作（`admin/readiness_candidate.go:66-75` 已有“config hash 变化即重写 assignment”的逻辑）：

```
rai readiness compute --dataset <id> --operation <op> --actor <actor>
```

每个在用 dataset 执行一次。这是本方案唯一的运维前置动作，必须写进发布清单。

### 3.2 为什么不需要“上传新鲜度”替代闸门

删掉 36h 闸门后，担心的是“用户三个月没同步，模型仍给出今日 readiness”。实际不会：

- readiness 是**当日 anchor**，`ValidBefore = 当日结束`，`FreshenV2` 在 `at >= ValidBefore` 时置 `candidate_superseded`（`readiness/v2.go:230-240`）；
- anchor 由 `rai_readiness_dirty` → 模型栈作业生成，**只有上传才会触发**。没有新上传就没有今日 anchor，产品走“暂无数据”，而不是拿旧数据冒充今天。

所以“有没有新鲜数据”由 anchor 的存在性表达，比一个 36h 常数更准确，也不需要新的判据。

可选增强（非必需、不参与判定）：在 `Result` 增加只读诊断字段 `lastCommitAtUs` / `committedIngests`（来自 `workout_ingests`），供运维与产品“上次上传”文案使用。若采纳，需要为 `(dataset_id, finished_at DESC) WHERE state='committed'` 加索引。

### 3.3 版本升级：建议升到 `rai.readiness.training/3`

`Result` 与 `ReadinessSnapshot` 结构变化会让**旧 artifact 失效**：`athlete_product_v2.go:382`、`training_input.go:110`、`readiness_publish.go:76` 都用“反序列化后重算哈希 == 存储哈希”校验，字段消失即判 invalid。

两个选项：

| 选项 | 做法 | 代价 |
| --- | --- | --- |
| **A（推荐）** | `Model = rai.readiness.training/v3`、`Schema = .../3`、`InputSchema = .../3`；`modelmanifest/manifest.go:74` 的 Readiness 组件同步升版本并把 `digest("readiness:180d;36h;42dB;5d;14d;floor1;allowlist-v2/v2")` 改为去掉 `36h` 的新串；`training_input.go:110`、`athlete_product_v2.go:310/382` 的常量跟随 | 改动点集中、语义可审计；v2 与 v3 artifact 不会混淆 |
| B | 保持 v2 名字，靠“空库切换 + 全量重算”兜底 | 省几个常量，但同名 artifact 前后语义不同，违反“不得伪装成功”的约定 |

选 A。若切换时是空白库（当前 v6 方案的前提），v3 直接生效；若要保留存量数据，必须先 `dirty_scopes` 全量置脏并重跑模型栈作业，旧 v2 artifact 在重算完成前会被产品层判为 invalid——这一点必须写进发布清单。

### 3.4 `DefaultConfig` 是位置字面量

`readiness/model.go:47` 用位置初始化：`Config{tz, "policy", 36*Hour, 180*24*Hour, 42, 5, 14, 14, 1, .55, 24*Hour, 10000}`。删字段必须同步调整位置，否则 `TauUs`/`LookbackUs` 会静默错位。建议顺手改成具名字面量。

---

## 4. 发布顺序

```
S1  代码：删控制面（§2.1）+ 删 Readiness 覆盖（§2.3）+ 升 v3（§3.3）
    迁移：00051 只做「函数/触发器/依赖边」清理，不 DROP TABLE
    部署 → readiness 作业不读不写 workout_sync_completeness
S2  运维：每个 dataset 执行一次 readiness compute（重签 assignment）
S3  验证：v6 单条上传 → worker 提交 → 模型栈 → readiness artifact 无 load_sync_incomplete
S4  迁移：00052 DROP TABLE workout_sync_completeness + 相关索引
S5  文档：§5 全部条目
```

**为什么分两步 DROP**：`readiness_input.go` 每次作业都读该表；若与代码删除同版本 DROP，回滚旧二进制会直接 500。S1 阶段表还在、只是没人碰，回滚是安全的。空库切换场景可以合并 S1+S4，但仅限明确不打算回滚时。

---

## 5. 契约与文档

| 文件 | 处理 |
| --- | --- |
| `contracts/openapi-v2.yaml` | 删除 `/v2/sync/workout-completeness` path 及相关 schema；版本推进（破坏性移除，`1.14.0`） |
| `contracts/CHANGELOG.md` | 记录 removed 端点、能力字段消失、`load_sync_incomplete` 不再产生 |
| `contracts/workout-completeness-v2.md` | 头部加「**已废弃（2026-11-10）**，保留为历史」，正文不改（约定：保留原方案原文） |
| `contracts/workout-metadata-registry-v2.md` | 删除/改写“控制面 completeness/v1、Workout Protocol 3”的过期表述 |
| `contracts/sync-v6.md` | §32/§370 附近补一句：v6 之后不再存在覆盖证明控制面，Readiness 只依赖已提交的 ingest |
| `docs/WORKOUT_SYNC_COMPLETENESS_CONTRACT.md` | 头部标注已废弃，指向本文 |
| `docs/WORKOUT_SYNC_V6_REIMPORT_PLAN.md` | §2 阶段 C、§5 标注“已取消，见本文” |
| `docs/SYNC-vs-RQRUN-对比.md` | §14 范围差异、§15 不一致项、§16 建议：completeness 一项改为“已下线，与 RQrun 对齐” |
| `docs/IMPLEMENTATION.md:1453` | 改写为“已按产品决定取消完整性证明”，并记录本文与实施证据；`1453` 不得留成“已知缺陷” |

---

## 6. 测试与验收

### 6.1 必删/必改

- 删除 §2.1 列出的 5 个测试文件；
- `integration/synthetic_local_seed_test.go`：检查 `syntheticCoverage` / `Synthetic` 用法，随字段删除一并改写（isolated model fixtures 不再需要合成覆盖标记）；
- `scripts/local/test.sh` 中无 completeness 引用（已核实），但 `gate`/`full` 的集成套件会因文件删除自动减少，需确认无悬挂引用。

### 6.2 必增

`readiness` 单测（4 条）：

1. 无任何 committed ingest、也无 Workout → 不产生 sync 类 reason（只有 `load_baseline_insufficient`）；
2. 有 Workout 但某条负荷未知 → `recent_load_unknown`，**不**出现 `load_sync_incomplete`；
3. baseline `known` 覆盖所有已结束的过去日（`d.End <= AsOf`），`UnknownDays` 只统计负荷未知的日；
4. `FreshenV2` 跨日后置 `candidate_superseded`（保证没有新鲜度闸门也不冒充今天）。

集成（真实隔离 PG）：

5. v6 上传 1 条 → `workout_ingests.state='committed'` → 模型栈作业 → `training_readiness` artifact 存在，`Reasons` 不含 `load_sync_incomplete`，`load_state` 当日 `RecordedLoad` 非空；
6. 上传 3 条（实际 5 条）→ 与 5 条上传结果一致（证明不再有集合判据）；
7. `POST /v2/sync/workout-completeness` → 404；握手 capabilities 不含 completeness 字段。

### 6.3 完成门禁

`go test -race ./...`、`go vet ./...`、依赖边界检查；事务变更另加受影响真实隔离 PG 集成测试；`scripts/local/test.sh gate`。真机验收不在本轮自动授权范围。

---

## 7. 风险与明确不做

### 风险

| # | 风险 | 处置 |
| --- | --- | --- |
| R1 | 服务端不再能发现“记录没传全”，模型对缺席跑步无感知 | 登记为**已接受的产品决定**（RQrun 同构），写进 `docs/IMPLEMENTATION.md` 与 `contracts/sync-v6.md`，防止下一轮被当作缺陷修回来 |
| R2 | 部署后 assignment 失配导致 readiness 作业全失败 | 发布清单强制包含 `rai readiness compute`（§3.1），并在验收第 5 条覆盖 |
| R3 | 旧 v2 artifact 失效 | 空库切换无影响；保留数据必须先全量重算（§3.3） |
| R4 | `syntheticCoverage` 通道删除影响 isolated fixtures | 与 `synthetic_local_seed_test.go` 一起改写 |
| R5 | daily / VO2 仍有各自的覆盖判据（`recovery` 的 `daily_coverage_incomplete`、`sleep_coverage_incomplete`；`vo2maxexternal/select.go:206` 的 `Coverage.Complete` + `BootstrapStart` + `QueryCompletedAt`） | **本次不动**。它们来自 daily-facts / vo2max-facts 上传的 coverage 字段，属另一控制面。需另立一项核实：v6 之后这些上传是否仍携带 coverage，否则会出现同类死锁（P2 后续） |

### 明确不做

- 不做服务端推断式覆盖（“库里看到 N 条就算齐了”）——那是伪证明，比没有更糟；
- 不用 client 声明的 boolean、`synthetic` 标记或任何客户端 flag 冒充覆盖；
- 不删除 `change_events` 历史、`raw_json`、`workout_imports`、删除屏障；
- 不动 recovery / daily / VO2 的覆盖语义；
- 不要求客户端任何改动（iOS 从未实现 completeness，`WorkoutDetail.completeness` 是逐条记录的分析完整度产品字段，`workoutdetail/model.go:327`，与本控制面无关，保留）。

---

## 8. 工作量

| 阶段 | 内容 | 估时 |
| --- | --- | --- |
| S1 | 删控制面 + 改 Readiness + 升 v3 + 迁移（不 DROP TABLE） | 2 人日 |
| S2 | 运维重签 assignment + 验收 | 0.5 人日 |
| S3 | 测试（删除/新增/门禁） | 1 人日 |
| S4 | DROP TABLE 迁移 + 文档收尾 | 0.5 人日 |

合计约 4 人日 + 每个在用 dataset 一次 `readiness compute`。
