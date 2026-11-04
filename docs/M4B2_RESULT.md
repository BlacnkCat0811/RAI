# M4-B2 Result

日期：2026-11-04（Asia/Shanghai）。阶段：**M4-B2 — Apple Health VO₂max Raw Facts Transport**。**M4-B2 RESULT: PASS WITH RISKS；B2 CLOSED FOR DEVELOPMENT。** 本结果是 raw transport 开发验收，不是上线或模型/生理有效性验收。B1 两份冻结原文不改写。B3/B4 未启动。

## 1. Git Baseline

- HEAD：`46968332380f9145945bc989325b98bc40454d9a`；branch：`main`。
- 开工时 tracked diff 为空；pre-existing dirty 仅 `docs/M4B_IMPLEMENTATION_PLAN.md`、`docs/VO2MAX_EXTERNAL_CONTRACT.md` 两份 untracked B1 文档。
- 重新核对最高 migration 为 00023，新增 **00024_vo2max_facts.sql**。不修改 00001–00023。
- B2 工作保留在当前工作区；未 reset/restore/clean/stash/rebase/commit/push，未修改 `../running`。
- 完整变更清单与校验摘要在 `docs/evidence/m4b2/`；原始临时日志不作为证据替身。

## 2. Protocol

独立 `vo2max-facts/v1`，`version=1`、`metadataRegistryVersion=1`。新增 POST `/v2/sync/vo2max-facts` 和 GET `/v2/sync/vo2max-facts/coverage`，握手增加 `vo2maxFactsVersion`、registry version、128 changes / 524288 bytes。旧服务器缺 capability 时保留 pending，不上传、不降级。VO₂ capability 不依赖 Daily capability。

独立 flow/stream/secure HKQueryAnchor。首次 query 前落盘 `flowCreatedAtUs` 与固定 `bootstrapStartUs=flowCreatedAtUs−90×24h`；后续不滚动、不随时区重算。fingerprint 为 `sha256(vo2max-facts/v1|HKQuantityTypeIdentifierVO2Max|<固定下界>|overlap-v1)`，带 `sha256:` 前缀。成功 receipt 后才原子提交 anchor 和 sequence 链。Observer 仅为持久 wake hint，不是事实或固定后台执行承诺。

## 3. HealthKit Contract

只读 `.vo2Max`；授权保持 `toShare=[]`。通过 `HKAnchoredObjectQueryDescriptor` 读取新增、删除和 new anchor，满页保守继续查询。新增独立 `VO2MaxNormalizer`，原 Daily normalizer 不改。

根 quantity 仅来自真正的 VO₂ quantity sample，count=1，先验证 compatible unit，再转换为 `mL/kg/min`，保留 finite binary64，不 round/clamp/二次除体重。拒绝非有限值、非正值、逆序时间、错误类型/单位。**非正值规则按本次启动指令收紧**；B1 §4.1 允许非正有限值的原文保留，这一差异不隐瞒。80/1000 等超过 provider 文档范围的合法正值不会因人群范围被截断或拒绝。

`provenance/2` 保留完整约定 sourceRevision 字段与 Device 六字段（含 name），可缺字段明确 null，不推断 Watch。不保存 localIdentifier/UDI/serial，不声称完整 HKDevice archive。服务器按 canonical provenance 产生 sourceEpochID。四种来源分类只描述 evidence，不产生 projection；Apple 分类需 bundle/productType/manufacturer/model 组合，手动优先，testType=1 不产生 laboratory 身份。

metadata registry v1 严格限定九个 SDK wire key；tagged union 支持 string/boolean/int64/number/date/quantity 的冻结编码，registry v1 没有批准 number key。Int64 不经 Double；日期是 UTC Int64 微秒。testType 原整数 1–4，未引用无 guard 的 iOS 26 新 enum symbol。0/5/future、unknown key、Data、嵌套、错误 tag/unit 均阻塞整页。`HKVO2MaxValue` 只保留附带 quantity，不创建第二条事实；SyncIdentifier 不代替 UUID。

## 4. Outbox

`LocalState` 仅新增 optional VO₂ 状态，原 Workout/HRV/RHR/Sleep 可继续加载。先写 immutable body，再发布 body hash、batch ID、candidate anchor 与 pending 引用；网络投递一直使用原文件字节。blocked 页保留本地受保护 SDK archive、candidate、hash 与错误类别；不上传删字段版本。

receipt 校验 contract/version/registry、dataset/epoch/collector/stream/batch/sequence、原字节 SHA、正 revision、counts、delete status、committed 与 awaiting_model_implementation。ACK、anchor、previous batch、sequence、bootstrap 与 pending removal 同一次原子文件替换。状态替换失败保留旧内存/磁盘状态。

pending/blocked 为 GC root。成功 ACK 将 body 和后台 transfer cache 同时列入回收队列；有界自动 GC 在校验 roots 后回收，重开时识别 body-before-state orphan。热路径不重复扫描整个 Outbox 目录；失败发布会重新触发 orphan reconciliation。物理断电的目录持久性不冒充已验证。

VO₂ 参加既有 scheduler 的有界公平轮转，错误留在本流；共享握手失败持久退避，Retry-After 得到保留，前台 timer 包含 VO₂ pending。四流真实共享 admission/503/429 验证与 Workout 既有回归分别记录，不把四流测试说成五流真机后台验收。

## 5. Server

独立 Go DTO/API/storage；strict decode 拒绝未知/重复键、缺失必填字段、错误 provenance/union、reserved key 伪造、超限与错误 identity。沿用 B1 错误码族，如 unsupported_version、unsupported_metadata/type/test_type、unsupported_unit、invalid_value、source_invalid、batch_too_large、stale_batch、stream/batch/sample_identity_conflict、generation_conflict、collector_fenced、upload_lease_lost、internal_error。

Raw identity 为 dataset/type/HealthKit UUID；同内容 unchanged、不同合法内容整批 409；错误单位先在 wire 边界拒绝。delete-before-create、同批 delete wins、永久 tombstone suppress、旧 upload 的 lost-ACK replay 返回原 receipt；不做 last-write-wins。通用旧入口拒绝客户端 `RAI:vo2maxFactV1`。

coverage 独立 stream，epoch/collector 过滤，保存固定窗口、观察时间、bootstrap、hasMore、anchor、计数、stream sequence、最近非空 source summary 与 summary sequence。empty 不表示拒绝授权；`readAuthorization=not_determinable`。lastFactAt 包含已观察 additions，不能解释成当前有效值。通知继续使用同一 dataset epoch + commit revision/ordinal cursor。

## 6. PostgreSQL

复用 `health_samples/entity_tombstones/sync_streams/sync_batches/entity_changes/change_events/dirty_scopes/daily_health_coverage`。无平行 raw 表、projection 表、模型 handler、selection input、candidate result、额外 sequence 或新 index。

00024 只增加 reserved raw context 检查、nullable coverage 字段、nullable tombstone provenance 及删除触发器。known delete 在 live 行移除前保存 descriptors/epoch/hash/interval/三个 origin inputs，**不复制 VO₂ 数值或 HKVO2MaxValue**；unknown delete 保持 null。此共享触发点覆盖已有合法 tombstone 入口，非 SECURITY DEFINER，固定 search_path，REVOKE PUBLIC EXECUTE。

一个 batch 同事务提交事实、屏障、entity/change log、独立 `external/vo2max/facts/v1` dirty、coverage、stream、dataset revision 和 receipt；dataset 行锁分配提交序。每个新 observation（含空页和 unknown delete）都会 dirty；没有 job 假装完成分析。原始事务 context 5s；提交前重验 wall-clock upload lease。

API/Worker 不增加 grants；实际使用受限 rai_api/rai_worker 等价组与 owner 验证。B2 Worker 不获得 coverage/projection 写权限。runtime readiness 更新为 schema 24，owner 侧 test 防止其与 embedded migrations 脱节，online 不导入 migrations/admin/goose。

## 7. Compatibility

Workout Protocol 3 与 Daily Facts v1 的载荷、旧三类型、fingerprint、identity hash、receipt/anchor 规则保留。旧 LocalState 的三类 pending/hash/anchor 可加载；旧 schema 23 coverage 升级后原列不变，新 VO₂ 列为 null；empty→latest / 23→latest / repeat migration 及旧写入回归见验证摘要。

M1 Workout Features、M2 Training Load、M3 Running Ability 的算法/参数/模型身份源码没有修改，也不消费 VO₂ raw/dirty。raw 新增不产生这些模型的新 VO₂ 结果。schema 24 的版本门禁修正属于兼容接入，不是模型变化。

## 8. Tests

真实命令与逐项摘要见 [validation.json](evidence/m4b2/validation.json) 和 [复跑说明](evidence/m4b2/README.md)。passed 与 skipped 分列，重复专项不累加进全量计数。

| 实际验证 | 通过 / skip / failure |
| --- | --- |
| `go test -count=1 -json ./...` | 523 tests/subtests PASS；4 skip；0 fail；147 passed top-level。 |
| `go test -race -count=1 -json ./...` | 523 PASS；4 skip；0 fail。 |
| 独立 PG18：`go test -race -p 1 -count=1 -timeout=30m -json -tags=integration ./...` | **1049 PASS；11 skip；0 fail**；390 passed top-level。 |
| VO₂ / schema gate PG race 专项 | 50 PASS / 14 top-level；0 skip/fail。 |
| 九个 registry key / Int64 两端 / >2^53 / date / quantity 的额外 PG round-trip | 1 PASS；0 skip/fail；数据库仍接受超 provider 文档范围的合法正值。 |
| 最后来源 namespace 分类边界补强后的受影响 syncing race | 264 PASS / 48 top-level；0 skip/fail；不修改 raw hash/事务或模型。 |
| `swift test --package-path ios/RAI`，Xcode toolchain | **129 tests / 16 suites PASS**，0 skip/fail；Swift Testing 输出计数，不把内部组合矩阵再计成独立 tests。 |
| Simulator iOS26.5 正式 App XCTest | **39 PASS**，0 failure/runtime skip；专用 E2E 从该通用 suite 显式排除，另实际执行。 |
| Simulator → 正式 App normalizer/outbox/background file uploader → HTTP → PG18 | **1 PASS**；实际丢弃 committed ACK；事实/receipt/dirty 各1；App 验证 committed anchor 和 Int64 >2^53。 |
| USB iPhone12ProMax / iOS27.2，独立签名合成 App | **4 PASS**，0 skip/fail；含 typed metadata/provenance、pending/blocked/observer hint、SDK predicate overlap、normalization performance。不是实际健康库读取。测试包已清理，正式 App sandbox 不复用。 |
| macOS 真 SIGKILL + PG18 | 6 个阶段全部 PASS；每阶段1事实/1receipt/anchor恢复；不是 iPhone kill。 |
| 共享上传 budget | 四流真实 429 + VO₂ 两次503后均完成，重试 bytes 不变；不是五流真机后台承诺。 |
| vet / integration+m4b2client+m4b2sim vet / module verify | 全部 PASS。 |
| dependency boundaries / sqlc / OpenAPI / diff | 253 packages 五条边界 PASS；sqlc1.31.1 六个生成文件二次生成字节稳定；Redocly2.71.1 0 error / 原有31 warnings；diff check PASS。 |

4 个普通 skip 为原有真实 TCP pacing / 180s body deadline / ACK write deadline / throttled upload 的显式 opt-in 项。PG 全量另7个 skip 为既有 M1/M2/M3 performance matrix、旧 Phase4 designated-device backend 及三项 Phase4 性能/索引成本矩阵；不是新 VO₂ 测试 skip。完整名字和原因保留在 validation.json，不能将当前4个硬件合成测试冒称已执行旧 designated-device backend。

PG 各成功 runner 的 fsync/full_page_writes/synchronous_commit/data_checksums 均 on、socket-only；最终 leftover databases=0 / cluster removed=1。8 个事务阶段异常注入（fact/tombstone/entity log/change log/dirty/coverage/stream/receipt）和3处真实 pg_terminate_backend（after fact/after tombstone/before receipt）均验证全回滚与恢复后全提交。

已发现并修正的验证问题：runtime/owner 硬编码 schema 23；migration fixture 缺 collector；未保存 SDK fixture 的 sourceRevision；非法嵌套 SDK 构造与 lowCardioFitnessEvent 错误 category；unsigned Simulator Keychain entitlement。synthetic sourceRevision/非法未来 metadata 的 KVC 注入仅在 AppTests，生产 normalizer 不改写 HealthKit 来源、不调用 save/delete。

首轮全量 PG 达到默认 10min timeout，临时 cluster 最终删除；中间一轮 50k Workout worker 留在 queued，单独实际复跑通过。随后完整串行 PG race 回归已经通过（1049 PASS / 11 skip / 0 fail）；不以单独复跑代替最终全量门禁。EXPLAIN JSON 的 Actual Rows 是数值，测试采集器修正其类型，不更改数据库结果。

## 9. Performance

只报告 synthetic 本机数据，不作为生产 SLO。包含 0/1/128、90 日每日一条 bootstrap、hot empty；129/1024/1025 等超限显式失败，B2 不实现 B3 refs 算法。记录 normalization、payload bytes、parse、PG transaction、HTTP ACK、local state commit 与 Go allocations。Swift allocations、WAN 和设备功耗未测。

本机真实观测（不是分位数或 SLO；race/debug/准入等待会影响耗时）：

| Synthetic 场景 | 字节与时间 |
| --- | --- |
| HTTP 0 / 1 / 128 additions | body 660 / 1236 / 70197 bytes；ACK 15.543 / 13.621 / 2421.280 ms；128场景包含实际429/Retry-After等待。 |
| 90 日每日1条 bootstrap，90 additions | body49529 bytes；parse153.045ms；PG事务142.749ms；合计295.931ms（race路径）。 |
| hot empty incremental | body694 bytes；parse0.684ms；PG事务3.850ms；合计4.629ms。 |
| 128-fact Go decode benchmark | 19.900071 ms/op；4567459 B/op；68421 allocs/op。 |
| Simulator normalization 0 / 1 / 128 / 90 facts | 0.048 / 2.270 / 530.525 / 368.382 ms；fact JSON合计0 / 492 / 62976 / 44280 bytes（不是完整 batch bytes）。 |
| USB 硬件合成 normalization 0 / 1 / 128 / 90 facts | 0.008 / 0.593 / 54.685 / 36.039 ms；fact JSON合计0 / 503 / 64384 / 45270 bytes。 |
| empty-page 本地 ACK 状态提交 | 660-byte body；单次4.980ms。不是物理断电保证。 |

UUID/tombstone/stream-sequence/coverage 真实 EXPLAIN ANALYZE 均为 Index Scan，分别使用既有 health_samples_analysis_identity、entity_tombstones_pkey、sync_batches_dataset_id_stream_id_client_seq_key、daily_health_coverage_pkey；本次观测13/9/26/11µs（miss lookup）。只保留 node/index/row count/执行时间，不导出含 UUID 的条件文本。没有凭猜测添加 B3 epoch/end 索引。完整 aggregate records 见 [performance.json](evidence/m4b2/performance.json)。

## 10. Privacy

测试 facts、UUID、metadata、来源与设备描述均为合成；USB 真机运行合成对象，不读取真实健康样本，不写健康库。未读生产 DB、未部署 API/Worker、未切旧入口、未迁移真实数据。repo evidence 仅留 count/status/hash/duration/error category；不复制原始 HealthKit payload、完整样本 UUID、来源 bundle、设备名、令牌、签名账号或设备标识。

## 11. Risks

- **P0：无已知未解决项。P1：无已知未解决工程 blocker。** 身份冲突、删除复活、提前 anchor、Daily 回归硬门禁均通过；真实健康库/生理证据仍延期，不升级为已验证。
- P2：现实 metadata/testType/source 变化会 fail closed；描述符分组不证明物理设备身份；固定 bootstrap/读取可见窗口/离线删除保留不能解释为全健康历史。50k legacy worker 的时间预算观察保留，不放宽 runtime budget 或更改模型以掩盖失败。
- P3：本地 benchmark 不是 SLO；物理断电、WAN、生产、旧二进制回滚、长期磁盘/能耗未验证。

## 12. Not Verified

**REAL DEVICE ACCEPTANCE: DEFERRED（完整真实 HealthKit 验收）**。USB iPhone 12 Pro Max / iOS 27.2 上的合成 SDK tests 单列，不能升级成真实 Apple Watch 数据同步结论。

仍未验证：真实 Apple Watch source/metadata 分布、真实 VO₂ availability、真实历史 delete/update/UUID 行为、真实授权可见性、iOS 后台 delivery/进程恢复时机、长期离线删除保留、WAN、production、physiological validity。RAI OWN VO2MAX MODEL: NOT IMPLEMENTED；PHYSIOLOGICAL VALIDATION: NOT VERIFIED。

## 13. Decision

```text
M4-B2 RESULT: PASS WITH RISKS
B2 CLOSED FOR DEVELOPMENT
Recommended Next Phase: M4-B3 — External VO₂max Candidate Projection
B3: NOT STARTED
B4: NOT STARTED
REAL DEVICE ACCEPTANCE: DEFERRED (real HealthKit/background scope)
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
RAI OWN VO2MAX MODEL: NOT IMPLEMENTED
```

本阶段证明的是 HealthKit fact → immutable pending → strict transport → durable PostgreSQL fact/tombstone/coverage → receipt → committed anchor。没有 external current、candidate result、RAI estimated VO₂max 或生理置信度。完成后停止，未自动开始 B3。
