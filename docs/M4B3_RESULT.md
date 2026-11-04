# M4-B3 — External VO₂max Candidate Projection

日期：2026-11-04，Asia/Shanghai。最终验证计数见 [validation.json](evidence/m4b3/validation.json)。仅合成数据、独立临时 PG18；不部署、不提交、不切换入口、不迁移真实数据，不修改 `../running`。

## 1. Git Baseline

`M4-B3 BASELINE`：`main`，HEAD `380a6fe96d13d1e57f69ceac50e52e72908af2b8`；`git status --short`、`git diff --stat` 均为空；最高 migration **00024**；B1/B2 dirty files：无。完整文件哈希见 [baseline.json](evidence/m4b3/baseline.json)。本次追加 **00025_vo2max_external_projection.sql**，不修改旧 migration、原方案或冻结 B1/B2 文档。

原 schema 已有 raw/coverage、共享 run/result/artifact/publication、candidate extension 和精确依赖边；缺 external allowlist、selection kind、publisher/reader 与 Worker。原 active 受 DB 约束固定 legacy。原 M1 future `athlete/vo2` 仍是未绑定占位，与新 scope 无关。

## 2. Model Definition

`rai.vo2max.external_projection/v1` 仅选择并复制外部 canonical binary64 根值，单位 `mL/kg/min`，method `external_estimate`。**External projection ≠ RAI estimated VO₂max**。无平均、平滑、校正、体重换算、HRR/负荷/能力融合。独立 scope `external/vo2max/v1/athlete/current`、artifact `rai.vo2max.external_projection/1`、metric `vo2max_external_value`、family `vo2max_external_candidate`。

有值时 provider 为 `apple_health`，origin 为 `apple_watch_estimate`；无所选合格事实时 provider `unknown`。confidence、parameterVersion、featureVersion 始终 null；DB quality 有值 partial、无值 insufficient。testType/calibration 是 selected summary/evidence，不是实验室认证或生理概率。Laboratory：NOT AVAILABLE。RAI 自有模型：NOT IMPLEMENTED；没有其 handler、enqueue、成功结果或 publication。

## 3. Input Snapshot

`internal/vo2maxexternal.ExternalVO2Snapshot` 明确绑定 dataset epoch、collector、scope generation、asOf、capture time、knownThroughRevision、config、assignment、selection、facts/tombstones/coverage。存储适配器消费 B2 canonical provenance/typed metadata，调用冻结 `VO2Fact.Origin()`，不修改 B2 Context/Canonical/Origin 实现。

整个 loader 在共享 **REPEATABLE READ / READ ONLY** 事务内；测试在 raw 查询前真实阻塞 loader，同时提交下一 coverage observation，验证旧 generation 与旧 stream sequence 一致。滚动出值域为 `(asOf−90d,asOf]`；额外所选 epoch 最新旧 tie 仅作过期说明，已知撤回 locator 与最新 unknown barrier 仅作证据，不以旧值补 current，不全历史重建。source-scoped SyncIdentifier 的域外 live 内容用索引、有界 lateral 查询补入相同总 refs 预算。

max **1024 sample/tombstone refs / 32 epochs / 256 latest ties**；查询 limit+1，超出 technical `analysis_input_limit`，不能成功发布截断结果；artifact 序列化与 DB 文本均受 128 KiB 限制。选择控制历史另有显式 **128 inputs** 工程容量，达到容量后管理操作拒绝，不能默默截断或删除审计。

当前计算只支持真实 capture 的 revised。纯领域能表达 future-known/future-event 和有完整能力声明的 as_known 输入；持久化入口不声称具备 deleted raw time travel，明确返回 `historical_snapshot_unavailable`。历史审查按 immutable run/artifact，显示后来 withdrawn 提示，绝不读取旧 upload body/备份来重造历史。

## 4. Source Resolution

四类完全复用 B2 origin/1 规则：apple_watch_estimate / third_party_estimate / user_entered / unclassified。epoch 是 canonical provenance/2 描述符的等价组，不是设备认证。默认只接纳 Apple origin；选择第三方/手动/未知 epoch 也不能绕过 origin filter。testType=1 不产生 laboratory result。源名、设备名、bundle、完整 metadata 不复制进 artifact 或默认 inspect。

## 5. Selection

遵循 B1 更严格的**每个 epoch 都需显式选择**规则，包括唯一合法 epoch。append-only `athlete_model_inputs`：kind/schema 严格配对；effective `[from,through)`、DB recordedAt、supersedes、revoked、input UUID/hash，以及 operation/actor 审计。effective time、recorded time、capture fence 都参与解析；未来 control 不能回填过去。重叠未解决返回 source_selection_required，不按最后写入者选择。

同一 epoch 按最新 event end 取完整 tie；start 分歧记录原因并确定性保存最大 start summary，不能掩盖同 end 异值/互斥 support。UUID 仅排序证据，不作业务胜者。等值等 support 是 equivalent evidence；不同数值/support、相关 source-scoped SyncIdentifier 多 live semantic contents 为 conflicting_data。SyncVersion 不覆盖 HealthKit UUID，也不静默淘汰旧版本。

`vo2max_external_candidate` / `vo2max_source_selection` 两种审计操作；相同 operation UUID、相同精确内容返回原操作，不重复 selection/generation/job。不同内容拒绝。审计 hash 与 input hash 绑定；operation retry JSON 用精确整数解析，测试覆盖 year-9999 范围内大于 2^53 的微秒差1。

## 6. Freshness

age = asOf − event end，固定 `<30d available`、`[30d,90d) stale`、`≥90d expired`。测试 **30d−1µs、30d、90d−1µs、90d**。Freshen 在管理 reader 显式 evaluationAt 上运行，不启动 expensive worker、不改 artifact；stale 保留原值，90d 立即 null；拒绝 evaluationAt 早于 persisted asOf。30/90d 是工程展示策略，不是生理有效期。

## 7. Status Decision Table

可执行主表在 `vo2maxexternal/select.go`，golden 测试冻结顺序：

| 顺序 | status | 条件摘要 | 根值 |
| --- | --- | --- | --- |
| 1 | sync_incomplete | 当前身份 coverage 缺失/未完成/窗口不足 | null |
| 2 | no_sample_observed | 无 visible live，亦无所选合格已知撤回 | null |
| 3 | source_selection_required | 未选择、重叠/撤销、无合格所选候选且无合格撤回 | null |
| 4 | conflicting_data | source-scoped sync 或最新同时间数值/support 冲突 | null |
| 5 | invalid_input | 最新非正值或仅 future event | null |
| 6 | deleted | 所选合格事实已撤回，无 current-domain 合法前驱 | null |
| 7 | expired | 合法外部样本 age≥90d | null |
| 8 | stale | 30d≤age<90d | 原值 |
| 9 | available | age<30d | 原值 |

deleted 的所选已知撤回不会被 no_sample/无关第三方遮蔽；unknown/manual tombstone 不捏造 Apple 撤回，future-event withdrawal 不回填过去。所有无值业务状态仍发布明确 null-state pointer。技术损坏、超限、DB/lease 失败不转换成业务成功。

`rai.vo2max.external_reasons/1` 纳入 configHash；原因包括 coverage_incomplete/no_visible_sample/source_selection_required/source_conflict/invalid_input/sample_deleted/sample_expired/sample_stale/user_entered/source_unknown/unsupported_metadata/future_dated_input/future_known_input/withdrawal_fallback。历史 capability 不足是明确错误 `historical_snapshot_unavailable`。

B2 已冻结 transport/DB 只接受正数，本阶段不放宽 raw 合同；非正值领域分支只在纯领域 malformed-input fixtures 验证，不冒称已经通过 B2 transport 保存该值。

## 8. Persistence / Dependency

共享 `analysis_runs/results/artifacts/publications`，独立 allowlist/metric/family/config/assignment。raw aggregate 与 selection 只有两条精确 external dependency，DB 约束拒绝 M1/M2/M3→external 边。一个共享 PublishAnalysis 事务保存 artifact/result/null-state pointer/clean generation/run/job/attempt 终态。

仅创建 candidate 槽位的兼容 metadata 不应隐式 enroll M1；本阶段发现并修复这一共享平台问题：新 external-only 槽位保存显式标记，两处既有 M1 dirty/staging trigger 跳过该标记；明确调用 M1/M2/M3 管理 enrollment 才解除标记，不改变 external entry、不重算 external。既有无标记 M1/M2/M3 行为保持原样。数据库集成测试验证新 workout 不因 external-only enrollment 自动排 M1，以及后续显式 M1 enrollment 不改变 external pointer。

## 9. Transaction / Fencing

raw insert/delete、tombstone、observation、selection、own candidate entry、dataset/collector/device-session 变化在 dataset 锁下立即移除 external pointer 并使 scope dirty；不修改 B2 raw bytes、receipt、download revision 或 raw aggregate generation 语义。未 enroll 时不排 external job。raw commit stamp 绑定 job knownRevision，job payload 同时包含 own config/assignment、selection identity、target external generation。

发布时沿用 dataset→job→dirty 锁，先检查 generation/lease；在同锁内重新加载完整 bounded input，与 captured canonical 输入比较，覆盖无旧 pointer 的首次发布。raw identities/hashes、source冲突组、tombstone、coverage/fingerprint/stream sequence、selection、own config/assignment、dataset/collector session 都被重验。独立无关数据提交可推进全 dataset revision，不污染 external 数值/依赖。失效为 SUPERSEDED，不写 artifact/pointer、不 clean N+1。提交前再次以 wall clock 检查 lease。

删除后立即无 current；worker 先发布则随后的删除事务移除 pointer。reader 同一稳定 snapshot 验证 clean/generation/run、assignment、dataset/collector/device、artifact hash、所有 captured raw refs 存活/hash，以及 evaluationAt 的实际 source selection，再 Freshen。预先写入的控制跨 effectiveFrom/effectiveThrough 时立即拒绝旧 pointer；共享 Worker 轮询以精确 Int64 微秒比较控制边界，至多20个 dataset 增代/排队，重复轮询不重复排工作。计算跨边界则 SUPERSEDED 并排新代。仅 freshness 跨界不排 projection。无效 collector/device session 是 retryable technical pending，不伪装 sync_incomplete 成功。产品不直接读取旧 analysis_results 标量。

## 10. PostgreSQL

真实 PostgreSQL **18.6**，独立 socket-only disposable cluster，fsync/full_page_writes/synchronous_commit/data_checksums 均 on，runner 清理 database 和 cluster。empty→25、24→25、重复迁移、旧 Daily/Workout 与 B2 writes 由 migration/全量回归验证。旧 migration SHA 不变。

API 不可读 artifact、写 selection/channel/publication；Worker 可读 raw/coverage/selection，append run/artifact/result/publication，不可修改 raw/input/channel/artifact。真实受限角色完成 worker 与 API 删除失效；不是 owner 模拟权限。新 devices 权限仅身份/可采集/撤销列，不授予 token hash；sync_batches 仅授予快照绑定所需列。

6 个新增 partial/expression 索引的自然 planner `EXPLAIN ANALYZE`：window、epoch/end、sync key、withdrawal、withdrawal epoch、unknown delete 均命中对应索引，无 enable_seqscan 强制。使用 owner-created synthetic index-cost rows，未用这些批量索引夹具冒充合法 raw transport；不输出 Index Cond/健康标识。具体时间见验证日志。

## 11. Property / Fault Tests

领域 permutation 100 次、精确 value preservation、duplicates/null confidence、future event/known/control、source isolation、freshness/nullability、1024/32/256 与 +1。真实 PG 随机 48 步 insert/delete/late arrival/replay/source switch/revoke，与显式 bounded rebuild 比较 semantic state；允许 generation、run、capture execution identity 不同。

真实发布注入 **artifact/result/publication/dirty-clean/run-terminal/job-terminal/attempt-terminal** 7 阶段失败：全部无部分 artifact/result/pointer/clean/终态，移除故障后相同 run 可重试。真实 **pg_terminate_backend**：after artifact / before publication / before dirty clean 三处均回滚、重试成功。

首次无 pointer 删除、发布前删除、新事实、selection switch/revoke、config、assignment、collector、expired lease/接管、发布后立即删除；旧 worker 不得发布。snapshot barrier 验证 coverage 不混代。另验证 expired/third-party/manual/maxExercise/no-stream/empty/equivalent/cross-epoch old sync conflict/unknown tombstone。

## 12. Performance

本机 synthetic 非 race 专项单次观测（不是分位数、生产 SLO 或 WAN）：

| refs | snapshot | selection + serialize | publish | artifact bytes |
| --- | --- | --- | --- | --- |
| 0（已知删除） | 6.163 ms | 0.027 ms | 8.554 ms | 2,014 |
| 1 | 5.377 ms | 0.023 ms | 7.722 ms | 2,179 |
| 128 | 21.909 ms | 0.112 ms | 40.628 ms | 15,899 |
| 1024 | 148.644 ms | 2.020 ms | 211.193 ms | 112,669 |
| 1025 | fail closed | 不发布 | 不发布 | 无 |

补充真实 PG18 race/debug 观测（与上表非 race 不直接比较）：0 refs 的 no_sample_observed、32 epochs、256 ties 均完成 snapshot/selection/serialize/publish；33 epochs、257 ties fail closed。32 epochs：snapshot 58.358ms、selection+serialize 0.522ms、publish 68.533ms、artifact 5,528 bytes；256 ties：398.371ms / 2.294ms / 431.836ms、29,723 bytes。见 [pg-epoch-tie-performance.log](evidence/m4b3/pg-epoch-tie-performance.log)。

Go 0/1/128/1024 refs benchmark：约 8.1/9.9/86.0/1007 µs/op；1024 为约 2.15 MB/op、105 allocs/op。独立 canonicalize/selection/freshness/serialize 及 32 epochs/256 ties 见 [domain-performance.log](evidence/m4b3/domain-performance.log)。Freshen 约 13 ns/op、0 allocs/op。最大 artifact 低于 128 KiB；refs/control 组合仍另受 artifact size 上限，超限报技术失败，不截断。

## 13. Regression

Go unit 与普通 race 各 **552 PASS / 4 既有 opt-in skip / 0 fail**；最终树 `go test ./...`、`go test -race ./...`、普通及 integration vet、build 全部 exit 0。真实 PG18 race 全量 **1142 PASS / 11 既有 opt-in skip / 0 fail**（含 Workout Protocol 3、Daily v1、VO₂ B2、M1、M2、M3、training），额外 empty/epoch/tie 矩阵 **6 PASS**；不把重复专项计数相加冒称独立测试总数。sqlc1.31.1、module verify、依赖边界、253个非 ignored Go 文件 gofmt、diff check 全通过。OpenAPI 未改；不把无改动接口另报为新 API 验收。

Swift **129 tests / 16 suites PASS**。B3 无 Swift/客户端/fixture 改动，**B2 client contract unchanged**。131 个受保护文件逐字节与 baseline 哈希相同，包括旧 migrations、iOS/contracts、B2 VO₂ raw/helper、原方案与冻结文档，见 [freeze-check.json](evidence/m4b3/freeze-check.json)。B2 wire bytes、raw/identity hash、receipt 的原测试继续作为真实回归，不以 mock 代替。旧 B2 restricted-role 测试原先断言 Worker 不可 SELECT coverage；按 B3 明示批准的只读权限，将该项改为必须可读，原 INSERT/UPDATE 禁令仍逐项实测；这是权限期望更新，不改 transport 或 coverage 语义。新 batch receipt 的原 analysisStatus/immutable retry 也不改写，candidate 状态由独立分析管理入口检查。

## 14. Privacy

默认 `rai analysis vo2max-external inspect/history` 只输出 status/quality/reasons/asOf/evaluationAt/是否有值/withdrawn；不输出根健康值、full UUID、epoch、device name、bundle、完整 metadata。内部 typed storage reader 供受控测试/管理代码检查合成值。promotion-plan 固定 promotable=false，reasons 包含 product_not_activated、real_device_acceptance_deferred。

产品 Home / metrics history / workouts / workout detail / training plan DTO 与 training state reader 真实测试确认无 candidate family/model/metric/artifact 泄漏。无新产品 HTTP endpoint/UI 卡片，无 home/training/coach 消费变更。API SQL 权限不允许读 candidate artifact。

## 15. Risks

最终门禁已完成，计数与 skip 原因见 validation.json。

- **P0：无已知未解决工程项。P1：无已知未解决工程项。** 删除仍 current、旧代/错源发布、跨源融合或 candidate 产品泄漏的阻断测试均通过；不将工程通过升级为生理或真实设备验证。
- P2：同时间 typed metadata support 采用严格等价规则，差异会 fail closed；未来兼容互补 metadata 需另审，不能按 UUID 偷选。descriptor epoch 不代表物理设备身份；新 metadata/source 规则需审核；只有服务器最后 committed observation 的 coverage，不能观察手机尚未上报的 blocked/pending page。历史 as-known raw rebuild 不可用；控制审计容量128、输入/epoch/tie/artifact 上限会显式拒绝超限，需要后续另审容量演进，不静默丢弃。
- P3：本机 synthetic benchmark 非生产容量承诺，未测 WAN/长期磁盘与能耗；旧 opt-in performance/device/socket 门禁的 skip 必须单列，不能冒称执行。

## 16. Not Verified

```text
REAL DEVICE ACCEPTANCE: DEFERRED
real Apple Watch source distribution: NOT VERIFIED
real HealthKit update/delete behavior: NOT VERIFIED
production: NOT VERIFIED
WAN: NOT VERIFIED
physiological validity: NOT VERIFIED
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
RAI OWN VO2MAX MODEL: NOT IMPLEMENTED
```

本阶段不重新证明 HealthKit discovery；Swift regression 与 PostgreSQL 合成测试不能升级为真实健康库、Simulator HealthKit 或真机生理验收。

## 17. Decision

```text
M4-B3 RESULT: PASS WITH RISKS
B3 CLOSED FOR DEVELOPMENT
Recommended Next Phase: M4-B4 — Simulator End-to-End & Engineering Closure
B4: NOT STARTED
REAL DEVICE ACCEPTANCE: DEFERRED
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
RAI OWN VO2MAX MODEL: NOT IMPLEMENTED
```

未自动开始 B4、部署或激活产品。整个阶段的根值不变量为：External VO₂max = selected external raw fact。
