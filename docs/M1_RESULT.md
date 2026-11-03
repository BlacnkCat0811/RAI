# M1 — Feature Foundation & Publication Substrate

日期：2026-11-03（Asia/Shanghai）。**M1 RESULT: PASS WITH RISKS。**

M1 候选计算、持久化、失效和验证已完成；未发现 P0/P1 阻塞项。没有激活新用户指标，没有开始 M2。完整合同：[WORKOUT_FEATURE_CONTRACT.md](WORKOUT_FEATURE_CONTRACT.md)；重复审计：[M1_FEATURE_DUPLICATION_AUDIT.md](M1_FEATURE_DUPLICATION_AUDIT.md)；机器证据：[m1-feature-foundation-2026-11-03.json](evidence/m1-feature-foundation-2026-11-03.json)。所有 fixture 为合成数据。

## 1. Git Baseline

- HEAD：`bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61`；branch：`main`。
- 开工前 **13 modified + 5 untracked**，完整记录如下。已有文件逐项 SHA-256 保护；IMPLEMENTATION 历史字节前缀保留，仅追加 M1。没有 reset/stash/commit/push；没有修改 `../running`、iOS、Daily 同步/outbox/wire 或 Workout Protocol 3 wire。

```text
 M README.md
 M apps/server/cmd/rai/main.go
 M deploy/local/README.md
 M docs/IMPLEMENTATION.md
 M docs/evidence/local-dev-environment.json
 M docs/evidence/local-dev-tests.json
 M ios/RAI/App/DailyHealthNormalizer.swift
 M ios/RAI/App/RAIApp.swift
 M ios/RAI/App/SyncCoordinator.swift
 M scripts/local/bootstrap.sh
 M scripts/local/lib.sh
 M scripts/local/test.sh
 M scripts/local/verify.sh
?? apps/server/migrations/latest.go
?? docs/M5A_RD_RESULT.md
?? docs/evidence/local-contracts-lint.json
?? docs/evidence/m5a-rd-2026-11-03.json
?? scripts/local/contracts.sh
```

M1 改动为：新增 `workoutfeatures`/`workoutdomain`、feature loader/publisher/query、admin candidate/HR input 命令、Worker 新 handler、migration 21/sqlc/权限/边界检查、合成测试与本合同/报告/证据。旧 analysis/detail 只抽取同公式 finite/cadence proxy/haversine 原语，保持旧算法身份及数值语义。schema readiness、管理验收与对应迁移/恢复测试更新到21；原 1–20 SQL 不改。完整 M1 路径见机器证据 `m1_paths`，不将开工前 iOS/M5A-RD/本地工具链改动归为 M1。

## 2. Architecture

| 层 | 实现 |
|---|---|
| shared pure primitives | `internal/workoutdomain`：Finite、CadenceProxy、Haversine；旧 analysis/detail 兼容包装复用 |
| typed domain | `internal/workoutfeatures`：Normalize、BuildPause、BuildSignal、BuildRoute、BuildAnalysisIndex、ExtractWindows/Steady/LongRun、逐模型 qualification |
| stable loader | `postgres.loadFeatureInput`，从 `LoadAnalysis` 的同一 REPEATABLE READ transaction 读取 revision/generation/Workout/全部明确关联 facts/删除屏障/provenance/parameters |
| execution | 静态注册真实 `analysis.workout_features` handler，复用 claim/renew/lease/fencing/StartAnalysisRun/PublishAnalysis |
| structured artifact | `rai.workout_features/v1`；schema `rai.workout_features/1`；无全量 trace 或 UI points |
| channel | `model/features-v1-92522339887e/workout/<uuid>`；candidate-only，legacy active 保留 |
| inspection | owner/admin CLI `rai analysis features compute/inspect/hr-reference/promotion-plan`；不新增公开首页 DTO 或 iOS 指标 |

后续模型使用本层 facts/time/source 解释和版本化 artifact；不能从展示点或平均 HR 偷换时间积分。M2 需要的参数条件 HR exposure 必须在共享 feature 层另立版本增补，本阶段没有提前实现新剂量/score。

## 3. Feature Contract

`RawWorkoutSnapshot` 正式存在，包含 identity/revision/generation/AsOf/policy/算法身份、Workout timing/type/可空 totals、quantity（含 unsupported category 和 metadata）、statistics、series、routes、events、activities/configuration、enrichments、完整 source/device descriptors、版本化有效参数、availability 和 fact/tombstone references。input 有界、深拷贝、稳定排序/去重；超限失败，不能截断计算。

`WorkoutFeatures` 正式包含 activityType、绝对 start/end 与 elapsed/source/active/paused 时间、距离来源与 cross-check、HR/speed support summaries、route quality、8种持续窗口及可选非重叠备选、5min steady、long-run halves、cadence proxy/功率/GCT/步幅/垂直振幅、environment、qualification/reasons/evidence。非 running enum 保留，但不给 running 栈资格。

`TimeSupport`：`instant_point / interval_observation / condensed_parent_summary / sealed_series`。`State`：`present / missing / deleted / ambiguous / condensed / series_backed / unsupported`。缺失不变0，unknown indoor 不变 false。无法证明 series 已完整读取的 parent 保守为 condensed；sealed child 恰好替代其 parent。

pause/manual 与 motion 分别维护；异常、未闭合、同刻矛盾或 active/source duration 不一致给 `active_time_uncertain`。HR/speed 插值只在同源、同 active segment、gap≤15s；不外推、不跨暂停/大 gap。performance 另有≤5s native speed-gap gate。observed/reconstructed coverage、interpolated fraction、原生/未支持 max gap、excluded/ambiguous seconds 分开保存。

grade 使用完整100m固定稳健邻域的前/后25m海拔中位数，分别至少5个精度合格点，不做端点高差或边界外推。平路 gate 以 native speed 与 grade 的**共同支持距离**加权，long-run halves 分别验证地形，不能用短片段 GPS 认证全场。

`Qualification`：load/VO2/performance/CS/endurance 各自 `eligible / conditional / ineligible`，没有全局 Valid bool。performance/CS 努力程度未确认，M1 最高 conditional。窗口和 steady 自带资格；门槛细节与保守项见正式合同 §8。

冻结 reason codes（来自实际 Go registry）：

```text
missing_hr hr_coverage_low active_time_uncertain speed_gap route_accuracy_low grade_unknown distance_mismatch pause_contaminated source_conflict source_unknown duration_too_short environment_unknown parameter_missing parameter_conflict workout_deleted invalid_observation signal_outlier input_limit unsupported_input effort_unconfirmed cross_workout_evidence_required indoor_unsupported future_model distance_missing steady_support_low
```

版本和 lineages 包含 config hash、feature version、参数 ID/hash/有效期/取代、source epoch、input generation/AsOf、fact identities/hashes。artifact 只保存 manifest hash/count 和明确引用，run lineage 保存该 Workout 的 fact 引用列表，不复制完整健康 timeline。

## 4. Database

新增不可变 **00021_workout_feature_substrate.sql**（SHA-256：`1b436b9a73423f4eee8c738d86527099e8c1c07143b3b1927ab2eb6b911bca5c`）。四张通用表：`analysis_artifacts`、`analysis_model_channels`、`athlete_model_inputs`、`analysis_model_dependencies`；既有 `admin_operations` 扩展 candidate assignment 审计。schema21，public tables52，新 sequence=0。

真实隔离 PostgreSQL **18.6** 验证 empty→latest、20→latest、重复 migration、原有 fact/dataset/audit 保持、受限角色及 publication。fsync/full_page_writes/synchronous_commit/data_checksums 均 on，Unix socket、无 TCP；每个验收 runner 均 leftover DB=0、cluster removed=1。

**1–20 全部 checksum 不变**；M5A migration20 SHA-256：`042a8200cd8f02286f4a5d0f89471ce8ce931adf195f37bad248f010dbd87252`。rollback 测的是既有 checksum gate **明确拒绝破坏性 Down**、保留 immutable history，不冒称已验证 schema21→20 或旧二进制自动回退。

`rai_api` 无 artifact 读写、parameter/channel 写权限；同事实事务的候选失效由 scope 受限、固定 search_path、非 PUBLIC EXECUTE 的 definer trigger 完成。`rai_worker` 可以读 input、追加 artifact，不能改 raw/channel/history。admin owner 执行显式迁移和 input 追加。terminal run 禁止追加 artifact，artifact/input history 禁改。

## 5. Transaction / Fencing

实际验证：

- 单一 RR snapshot 被真实第二事务的新 generation/enrichment 提交穿插后，仍只返回旧一致 input；没有混合两代。
- quantity、series parent、route、Workout 删除在事实提交时立即撤下 candidate current reference；Worker 前不可读，Worker 后也不从旧 import 复活 child。历史 artifact 保留。
- series/route registration、late seal、metadata/enrichment、HR reference 变化能 dirty candidate；参数 overlap 不按 UUID 选赢家，显式 parameter_conflict。
- 旧 generation 只 SUPERSEDED，不写 artifact/pointer、不 clean 新 generation；失效 lease/fencing token 不能发布，回收的新 worker 可完成。
- artifact/results/publication/run/dirty/jobs/attempts 七个表写入点故障注入全部回滚并可重试。
- **artifact写后 / publication前 / dirty clean前** 三个真实 PostgreSQL backend termination，均0部分结果、0部分 publication、未错误 clean；重试成功。advisory lock 仅为测试 barrier，没有第二套生产锁。
- future load/performance/vo2/ability/endurance scopes 只有真实 dirty/invalidation substrate，未实现 handler 不创建假成功 job。

## 6. Tests

计数为实际 Go JSON test instances，**含 subtests，不去重累加成独立场景数量**；不同轮次分别列出。

| 命令/验收 | 实际结果 |
|---|---|
| `go test -json -count=1 ./...` | **426 PASS / 0 FAIL / 4既有opt-in skip**；feature包62个实例通过 |
| `go test -race -json -count=1 ./...` | **426 PASS / 0 FAIL / 4既有opt-in skip** |
| `go vet ./...`；`go vet -tags=integration ./...` | PASS |
| 隔离 PG18：`go test -race -p 1 -count=1 -tags=integration -json ./... -timeout=30m` | **876 PASS / 0 FAIL / 9 opt-in skip**；其中 integration **445 PASS / 5 skip，469.617s** |
| 最终 feature/migration/real-login PG race 定向 | **35 PASS / 0 FAIL**；integration 44.945s，含真实 crash/删除/失效/权限/limit/failed-series |
| `integration,m5aclient` 最终 PG/Swift bridge 补充 | **41 PASS / 0 FAIL**；integration 27.324s |
| 最终非race PG 20/45/120/240m matrix | **5 PASS / 0 FAIL**（父测试+4子项） |
| `swift test --package-path ios/RAI`，使用现有完整 Xcode | **119 Swift Testing / 15 suites PASS**；XCTest 2 pass、3既有opt-in skip |
| input reorder/duplicate property fuzz | **5s / 290 executions PASS**；另有固定种子/解析积分/暂停/缺口/series repartition/删除属性 |
| `scripts/check-boundaries.py` | PASS，251包；feature/analysis/training纯域无DB/HTTP/FS/系统时钟/随机和presentation依赖 |
| sqlc 1.31.1 generate/reproduction | PASS，4个生成文件重复生成 SHA 完全一致；新增字段/查询的预期 diff 保留供审查 |
| `go mod verify`、gofmt、`git diff --check` | PASS |
| 固定 Redocly2.71.1 OpenAPI lint | **0 errors / 31既有warnings**；合同未修改 |

覆盖 easy/tempo/interval/long/short、manual/motion/重复/矛盾/未闭合 pause、HR point/interval/condensed/sealed、source conflicts、speed/GPS gaps、低精度/海拔噪声、距离不一致/零距离、220bpm/vehicle-like spike、deleted children/routes、late seal、forgot-stop、indoor/outdoor/unknown、跨午夜/DST/负和非整秒 epoch、非running enum、参数as-of/取代/冲突、输入上限及 immutable history。

PG full 的5个 skip 是 explicit M1 perf（随后已实跑）、指定真机入口和3个既有 opt-in perf/index/gzip 项；4个 HTTP production-budget opt-ins 在默认包里保留 skip。Swift3个 opt-in XCTest 也未冒称本命令执行，Daily 实际 client probe 由独立 PG harness 执行。

保留并说明早期失败：CLT 缺 Testing，经现有完整 Xcode 复跑通过；更新 schema 测试时误把 HTTP200断言改210，已恢复200并通过真实登录回归；oversized-history fixture 曾重复完整合成 route bundle触发 statement_timeout，改为仅 observation 文档后通过，**没有放宽生产 timeout**。

## 7. Performance

Darwin/amd64、AMD Ryzen5 3600X / 12 logical processors；全部 synthetic、non-race，非生产 SLO。domain每项3次/操作均值；PG每格一次观测。完整 allocations/bytes 与日志摘要在机器证据。

PG workload：3个scalar parents、HR sealed entries每10s、GPS1Hz，未注入个人参数。`snapshot load` 已含 normalization，不能再叠加下一表 normalization。

| duration | snapshot load ms | compute ms | total ms | allocations | allocated MB | artifact bytes |
|---|---:|---:|---:|---:|---:|---:|
| 20m | 20.433 | 5.598 | 26.032 | 34,163 | 8.70 | 8,825 |
| 45m | 34.249 | 9.547 | 43.797 | 70,115 | 19.63 | 13,268 |
| 120m | 87.498 | 29.706 | 117.204 | 183,110 | 57.12 | 25,584 |
| 240m | 167.464 | 60.089 | 227.553 | 363,627 | 114.25 | 42,064 |

Dense pure-domain workload：7 channels每5s interval、GPS1Hz、显式合成HR reference。timeline列不含route，window列包括duration/steady/long-run extraction；total另含lineage/hash/dynamics/qualification，不等于命名子项简单相加。

| duration | normalize ms | timeline ms | route ms | windows ms | serialize ms | total ms | allocations/op | allocated MB/op | artifact bytes |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 20m | 20.681 | 1.836 | 0.915 | 0.272 | 0.073 | 36.346 | 50,458 | 25.84 | 9,012 |
| 45m | 45.517 | 4.262 | 1.803 | 0.805 | 0.105 | 77.148 | 112,196 | 57.89 | 13,223 |
| 120m | 120.332 | 11.181 | 5.654 | 4.097 | 0.149 | 247.454 | 297,229 | 166.06 | 24,910 |
| 240m | 240.421 | 27.495 | 11.224 | 18.497 | 0.213 | 425.899 | 593,236 | 335.80 | 40,360 |

所有实测 artifact <128KiB。窗口使用 prefix integration + 固定8 durations有界扫描，候选只保留best和non-overlap backup；没有每个起点×全部原始sample 的 O(N²) 扫描，也不持久化第二份每秒timeline。4h dense总分配约336MB是累计分配量，**不是peak RSS**；production并发/peak仍未验证。

## 8. Regression

| 范围 | 结果与边界 |
|---|---|
| Workout Protocol3 | route/series/delete/late-arrival/receipt/generation/metadata/provenance/权限/故障完整 Go/PG 回归 PASS；wire与ACK不改 |
| Daily Health | Go/PG PASS；Swift119/15 PASS；真实macOS DailyProbe→HTTP→PG 的上传前/上传中/提交后 SIGKILL 三点均1 fact/1 receipt/anchor restored，真实429公平交付3 facts/3 receipts、Sleep不饥饿 |
| existing analysis | old feature/TRIMP/state 数值、缺失、发布/删除/fencing回归 PASS；candidate不覆盖active |
| training | generation、计划、执行、merge、bounded历史/容量和现有消费版本回归 PASS |

不把上述 macOS synthetic SIGKILL 当 iPhone kill；本轮未读取真机 HealthKit，不重新打开 M5A-RD，不改写其历史结论。

## 9. Risks

- **P0：未发现阻塞项。** raw不改、删除立即撤引用、稳定snapshot、旧run不能publish、candidate不覆盖active、无parent/series双算、暂停/大gap不拼接、instant count不造coverage、冲突不静默平均、超限不截断。
- **P1：未发现阻塞项。** 新版有唯一time/source/distance/qualification解释；grade不可靠时未知，source epoch/参数进入identity；旧detail兼容解释没有被偷偷改名成新模型。
- **P2：** 真实eligibility/grade/生理效度未验；旧或series查询不完整记录会保守不合格；HR trend要求完整连续支持，比95%更保守；4h累计分配及生产peak/concurrency尚需验证。M2非线性剂量需共享feature层的版本化exposure扩展，不能f(meanHR)。新产品stack promotion/rollback仍受消费者与发布门槛限制，M1只提供candidate及legacy回退边界，不声称已激活完整评分栈。
- **P3：** ultra-long/dense输入可能触发明确limit，阈值调整须有实际证据与新版本；legacy detail的完整消费者迁移以后另立版本；schema21部署/角色刷新/旧二进制兼容需独立发布验收，本轮不部署。

## 10. Not Verified

真实Workout资格率、真实GPS/海拔坡度准确性、独立生理效度、参数测量来源真实性、production load/peak RSS/多worker并发、WAN/TLS部署验收、物理断电、M1真实设备feature验收均 NOT VERIFIED。本地 synthetic 数值/数据库/性能通过不能替代这些项目。原方案原文 SHA-256 `6951b1d938133a964cb06d28c3aa744ad05121f25da770e184041211df53e3de`，保留不改。

## 11. Decision

**M1 RESULT: PASS WITH RISKS。** 没有新的用户指标激活，没有自动部署/迁移真实数据、切旧入口、commit或push。

**Recommended Next Phase: M2 — Training Load。** 本轮在M1停止；下一阶段需用户另行启动。
