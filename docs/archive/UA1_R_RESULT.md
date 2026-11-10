# UA1-R Result

**UA1-R RESULT: BLOCKED — R1、R2 已完成真机复验，R3 仍被真实未支持元数据阻塞。**

本报告记录 2026-11-05 的原 iPhone、原 UA1 测试环境和原验收命名空间。最终串行 PostgreSQL 门禁已通过；测试通过不替代缺失的 R3 真机回执。历史 UA0/UA1 报告保留原文。

## 1. Git Baseline

main / `9a8839dff0f58fe93ffc7297e50490c0c88279cc`。入口已有 IMPLEMENTATION 修改、UA0/UA1 文档和 evidence，均保留；[基线](../evidence/acceptance/device/ua1-r-20261105/baseline.json)记录入口状态及 494 份受保护文件 SHA。公开路径以 SHA 表示，原完整路径清单私有留存，保护对象未变化。未执行 reset/restore/clean/stash/rebase、commit/push、生产部署、旧入口切换或真实数据迁移。

## 2. R1 Workout Numeric / Identity Root Cause

持久 JSON 解码后的整数载体与新 HealthKit quantity 的浮点载体具有相同 canonical bytes，Swift enum equality 却不相等，误触发 `workout_core_change_requires_review`。这是比较路径错误，原有 hash 无需改变。

## 3. R1 Implementation

比较现有 canonical codec 生成的 core identity，保留原有 metadata 排除规则，仅在 quantity 比较路径归一 signed zero；不把 Int64、版本、时间或标识统一转为 Double。旧状态可直接重开。额外以完整重建 payload 与原回执 hash 精确匹配，复用原 observation 时间和原请求；只有全部字节一致才 exact replay。

[数值合同](WORKOUT_CORE_NUMERIC_CONTRACT.md)和 property/golden 覆盖整数样 quantity、fraction、单位转换、signed zero、大 Int64、Bool 区分及非有限数拒绝。原 JSON codec、v3 wire/hash 和存储身份未改。

## 4. R1 Real-Device Re-test

**PASS。** 原 Workout、原 namespace、原持久状态两次 relaunch：每次 24/24 discovery queries 使用 committed anchor，新语义事件 0，Workout/import/association/route/point 增量均 0；原 Workout identity、原 bundle 和回执保持不变。自然新增 4 条 HRV 单独计数。[真机证据](../evidence/acceptance/device/ua1-r-20261105/r1.json)。

## 5. R2 VO₂ Native Metadata Root Cause

原 blocked secure archive 中观察到 24 条 VO₂ facts。61 个 integer-semantic 字段值以 NSNumber double carrier（objCType d / CFNumberType 6）出现，均 finite、integral、处于安全整数幅度。旧解析器错误地把 native carrier 限制为整数 NSNumber。原 archive 和未推进 anchor 的 fail-closed 行为正确。

## 6. R2 Typed Metadata Contract

registry 仍为 1，wire 和合法语义集合不变；本地 parser revision 为 2。SyncVersion、AlgorithmVersion 保留 signed Int64 语义，testType 仍仅 1–4，SyncIdentifier 仍为 string。整数 NSNumber 精确保留 Int64；double 仅接受有限、数学整数且绝对值严格小于 2^53，float 对应严格小于 2^24。Bool、fraction、边界/溢出、NaN/Inf、未知 enum 和不支持形状继续拒绝。

[原生类型合同](VO2_NATIVE_METADATA_COMPATIBILITY.md)依据本机 SDK header 与受保护真机检查冻结；Swift/Go 共用合成 typed metadata vectors。没有普通 Double→Int 截断或丢字段兜底。

## 7. R2 Real-Device Re-test

**PASS WITH RISKS。** 原 blocked page 重开并 stage 24 facts 时仍保留 archive、pending 和旧 anchor；只有原请求 durable ACK 才提交 anchor。随后完成空的 anchored continuation。服务端保留 85 个 typed metadata 字段，计数与原页一致。真实 Worker external projection 已运行，正确返回 `source_selection_required` / `source_unknown`，originClass/value 为空；未猜测 Apple 来源、手工选源或加入 HR 参数。[真机证据](../evidence/acceptance/device/ua1-r-20261105/r2.json)。

## 8. R3 Completeness Protocol

新增独立 `workout-sync-completeness/v1`，保留 Workout Protocol 3 facts。固定初始 200 天 lower bound、running-overlap predicate，无移动 upper predicate；capture-cycle start 是 cutoff，不是最新健康事件时间。支持 Workout、现有十个 quantity、route，以及绑定 quantity parent 的 fresh series capture；native cadence/elevation 明确 unsupported。空支持流可 complete-empty，无父 Workout 才 child not-applicable。

每页绑定 fingerprint、连续 sequence、前页/前 anchor SHA、candidate anchor SHA、adds/deletes 和耗尽状态；manifest 绑定 collector、generation、cutoff、inventory、原始事实 durable receipts 和 series digest。事实 ACK 在前，最终 manifest durable ACK 在后；最后一次原子本地提交才推进该完整集合的 committed anchors。离线、kill、lost/corrupt ACK 保留原 immutable body。[完整合同](WORKOUT_SYNC_COMPLETENESS_CONTRACT.md)。

## 9. R3 Server Validation

最高旧 migration 为 27，新增 additive 28。服务端 dataset 行锁、epoch/collector fencing、严格字段/版本/stream set 校验、page/anchor continuity、inventory fold、durable receipt membership、series digest、删除屏障及最新事实 revision 校验后，才在同一事务写入 immutable manifest/receipt、commit log 和 Readiness dirty/job。API 仅 SELECT/INSERT；Worker 仅 SELECT；历史行不可 UPDATE/DELETE。

M6 只读取该服务端验证行及当前事实 revision，旧 collector 或过期事实 lineage 不可沿用。隔离 syntheticCoverage 仍有显式标记，真实验收没有使用。服务器验证的是可信 collector 的传输一致性；HealthKit 不提供远程加密查询证明，不能夸大为原生查询 attestation。

## 10. R3 Real-Device Re-test

**BLOCKED。** 发现 44 个 visible running Workouts；在处理完其中 4 个后，一个 Workout 的 Activity metadata 触发 `metadata_requires_review`。受保护原生检查确认两类不在冻结 allowlist 的 key，共 5 次出现：NSNumber q 2 次、String 3 次。公开记录只保存 key SHA、carrier 和计数。

已保存 63 个查询页、56 个 stream capture、32,709 additions、0 deletions；该 completeness chain generation=0、committed anchors=0、最终 manifest 未生成/提交。原始 capture 和失败状态保留，没有忽略字段、扩大 allowlist、缩短查询范围或重置状态。全流 real caught-up、第二轮真实 completeness 增量和真实新 manifest lost-ACK **均未验证**。[R3 证据](../evidence/acceptance/device/ua1-r-20261105/r3.json)。

服务端当前 5 个 Workouts / 5 个 imports，其中原 UA1 Workout identity 和原 bundle receipt 仍保留。新增项来自本轮实际发现的其他 Workouts，不能把这些合法初次上传当作 R1 重复。

## 11. M6 Re-evaluation

真实 completeness 前置条件尚未满足，不能要求或伪造 `load_sync_incomplete` 消失。实际 Worker 已处理新增事实，M6 仍为 stale，保留 `load_sync_incomplete`、`recent_load_unknown`、`load_baseline_insufficient`、`recovery_stale`；unknownLoads=5、score 为空、syntheticOnly=false。HR references=0，active=legacy、candidate-only。

另行 Simulator→真实 Go HTTP→隔离 PG18→M6 的合成测试已证明：有效非 synthetic transport receipt 能解除该 coverage 阻塞，同时 unknown Workout load 仍保持 unknown。这不替代上述真机缺失证据。

## 12. PostgreSQL

R1 专项 33 PASS / 4 opt-in SKIP；R2 专项 29 PASS / 0 SKIP。R3 当前源码专项 5 个顶层测试 PASS，覆盖 complete nonempty/empty、route absent/pending、sequence/anchor/receipt/series 冲突、旧 collector、roles、immutable retry、原子故障和 backend termination。最后一次专项 21.130 秒，残留库 0、隔离 cluster 已删除。

完整串行 PG18.6/race 门禁：**PASS；integration 305 顶层 PASS / 8 explicit opt-in SKIP，959.027 秒，全部 17 个有测试包 PASS，残留库 0、cluster 已删除，命令退出 0。** [实际回归证据](../evidence/acceptance/device/ua1-r-20261105/validation.json)。前轮 integration 包已 PASS（1207.530 秒），但整轮因四处旧迁移断言和资源竞争下的解压测试超时失败，不计为全量 PASS。旧断言已随 schema 28/54 tables 更新；解压限制未变，独立 race 和最终串行全量均通过。合成多年历史 seed 使用独立事务准备预算，线上查询池仍保留原 2 秒预算。

## 13. Swift / Simulator

完整 Xcode `/Users/ta/Downloads/Xcode.app`：Swift 150 tests / 21 suites PASS。最终 Simulator App XCTest **47 PASS / 3 explicit runner SKIP**，包含 R1 单位转换、R2 原生 NSNumber matrix、R3 lost-ACK/relaunch/committed anchors、pending stream 拒绝及 metadata diagnostics 不输出原 key/value/UUID。

另行真实 Simulator→Go HTTP→隔离 PG18 E2E PASS：2 generations、12 streams、1 import，故意丢一次 ACK，immutable retry，第二轮 12 committed anchors / 0 bootstrap，M6 synthetic=false 且 unknownLoads=1。专用 R3 HTTP 测试的正常套件 skip 已由这个独立入口实际执行覆盖；另两个专用 host skip 未冒充本轮通过。早期并发使用同一 Simulator 导致 test host 被 kill，已改为串行并通过。

## 14. Regression

Go all / race / vet、integration+ua1rsim vet、dependency boundaries、go mod verify、gofmt 和 sqlc 再生成稳定检查通过；原 migration 1–27 checksum 未变。现有 36 个 OpenAPI paths 原样保留，只增加 endpoint/definitions 和两个 optional capability 字段，lint 0 errors / 33 warnings。11 个 M7 typed golden schema 及 Swift fixture identity 通过。52 份既有模型/产品源文件校验未变，M6 domain 仅 coverage 来源注释变化；没有改生理公式或 qualification。

仅原 UA1 cloud test 环境已更新至 schema 28，API/Worker 同镜像、HTTPS 和角色就绪检查通过。保留原镜像/卷/enrollment；无生产部署、备份恢复依赖、旧入口切换或 candidate→active。构建使用 dirty source SHA 留证，未冒充新 git commit。

## 15. Privacy

公开 artifacts 只保留 count/status/hash/reason/timing。发生两次会话输出失误：R1 coverage key 中的 route UUID，以及后续 lock-state 对象中的 device identifier。两者均未重复写入公开 artifacts；输出映射已分别改为 hash/布尔白名单，但不能宣称本次会话零标识披露。

未公开 Workout/VO₂/HRV/RHR 数值、route coordinates、sleep times、source bundles 或 credentials。原 archive/capture 保留在受保护原状态，不提交到 fixture。[隐私记录](../evidence/acceptance/device/ua1-r-20261105/privacy.json)。

## 16. Remaining Risks

R3 首要剩余项是对两类真实 Activity metadata 进行独立语义/字段合同/向后兼容审查；本轮没有把未知字段转成成功。真实 44-Workout 全范围完成、最终 receipt/anchor commit、后续真实增量和 M6 coverage 解除均未发生。首次捕获部分已耗时约 438 秒，本地状态约 25 MB，不能声称大历史容量、功耗或自然后台已验收。

VO₂ 仍需按既有合同完成 source selection。K01/K03/K05、自然后台、自然 Workout delete/update、长离线、多设备、population、physiological validation 和 product activation 均保留未验证/未授权状态。

## 17. UA1 Re-Acceptance Decision

| 项目 | 结论 |
|---|---|
| R1 / Workout repeat | PASS |
| R2 / Real VO₂ compatibility | PASS WITH RISKS |
| R3 / Real Workout all-stream completeness | BLOCKED |
| UA1-R / UA1 re-acceptance | **BLOCKED，仅 R3 未关闭** |
| Real-device engineering | PARTIALLY VERIFIED / NOT ACCEPTED |
| Physiological validation | NOT VERIFIED |
| Product activation | NOT AUTHORIZED |

未满足三个 blocker 全部关闭的升级条件；不得升级 UA1 为 PASS WITH RISKS。保持 active=legacy、candidate-only，不启动 UA2、UA3、UA4、shadow 或 activation。本轮最终回归和留证已完成，至此停止。
