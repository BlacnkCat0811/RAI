## 2026-11-01 Phase 4 Delivery Freeze

- `00017_analysis_sample_lookup.sql` 的 `health_samples_analysis_identity` 使用普通 `CREATE INDEX`，生产升级期间会阻塞 `health_samples` 写入；执行需维护窗口。本冻结点不改写为 `CONCURRENTLY`，本阶段不执行生产 migration。

## 2026-11-01 — OpenAPI 1.3.0 / Protocol 3 Phase 3

## 2026-11-01 Phase 4

- protocol3保持；schema17增加有EXPLAIN证据的覆盖身份索引。整个wire/body、gzip/decoded/domain独立限额；支持gzip part且SHA256继续标识原JSON字节。
- HTTP分阶段有限预算与可重试timeout；Outbox保留原JSON和可重建压缩文件，COMMIT后ACK和DELETE fencing保持。

- metadata-only Sample 补充写不可变观察历史，不改旧 Sample 行/hash/回执；严格保留核心身份冲突。正式分析逐项核验保留子事实的当前/历史 provenance，父 Workout 来源不替未知子事实背书。

- 可选 bundle observation v1：HealthKit provenance allowlist、来源政策、逐类型 coverage 和不可变观察历史；新能力由握手声明。缺失 provenance 保持 source_unknown，v3 原始上传合法，但正式分析明确 pending/failure。
- 可选 sampleDeletions/routeDeletions 复用原事务、屏障和回执；旧包仍不得按缺席删除；ACK 删除计数受本包删除数量约束。
- 保留 GPS 原数组序号和非单调时间，真实 min/max manifest 包络；诊断与查询/seal 完整性独立。新增 migration 00016 仅替换 route append/projection 函数的微秒转换，修复 PostgreSQL 对 year-9999 末微秒向上舍入到 10000 的拒绝；schema 更新为16，协议版本仍为3，原始时间不修正。
- iOS 持久发现日志先于 anchor；完整路线 capture 前缀恢复，不新增逐 Sample/Route HTTP。缺失 provenance 的旧包继续合法上传，但分析等待来源证据；显式删除计数只能来自 `HKDeletedObject` 与持久父索引。

## 2026-11-01 — RQrun Phase 1 删除安全修复

- RQ-001：取消普通快照缺席推断删除；完整性标记不建立 Sample / Route tombstone。保留原事实、冲突与显式删除屏障。
- RQ-002：未知 Workout DELETE 同样持久化屏障，COMMIT 后 ACK；重复命令返回首次原回执和原 revision。`alreadyDeleted` 描述首次命令的状态，不随重试改变。客户端 404 或无效 ACK 不再确认本地删除。
- RQ-003：已知删除保存旧范围并在同事务登记 P4 dirty/job，立即隐藏旧 Workout 分析与 Training State；Worker 沿用 generation CAS 和历史 run/result 语义。
- Protocol 3、schema 15、现有锁和上传事务边界保持。复用 entity_tombstones / change_events 保存时间、fencing 和删除回执，不新增迁移，不改 00014/00015 或 checksum。Sample / Route 的可靠客户端显式删除捕获仍待后续完善。

# 合同变更记录

## 2026-10-31 — Protocol 3 Workout Bundle

将健康同步上传替换为 RQrun 式单请求模型：一条 Workout 对应一个 JSON bundle、一次 `multipart/form-data`（字段 `file`）上传、一个服务端事务和一个 Workout 级分析任务。旧 `/v2/sync/streams`、`/v2/sync/batches`、`/v2/sync/pages`、`/v2/sync/series`、`/v2/sync/associations`、`/v2/sync/workout-enrichments`、`/v2/sync/workout-packages`、`/v2/routes` 上传路径不再注册。

- 本版本不兼容已有 v2 事实。升级前必须运行显式破坏性重置 `scripts/rai-reset-v2-data.sh --confirm-delete-v2-facts`；重置保留 datasets/devices/collector leases，删除 v2 fact/analysis/training-derived 行。不得把该命令作为自动 migration。
- 新增 `POST /v2/sync/workout-bundles`、`GET /v2/sync/workout-bundles/{workoutID}?sha256=...` 和显式 `DELETE /v2/sync/workout-bundles/{workoutID}`；bundle 含 Workout、全部 scalar samples（成员关系即关联）、多值 quantity series 和 routes。
- 新增 schema 15（00014 建表，00015 扩展主键）`workout_imports`：按 `(dataset, workout, bundle_sha256)` append-only 保存精确 bundle SHA-256、验证后的 JSONB 源文档和原回执；同一 Workout 可有多个 bundle 回执，旧回执不被覆盖。
- bundle 的 `completeSampleTypes`/`completeRoutes` 仅描述读取覆盖，缺席子事实保留（2026-11-01 RQ-001 修正）。Workout 删除只能由客户端收到该 UUID 的 `HKDeletedObject` 后显式 DELETE。
- 一次事务写入 Workout、samples、`workout_samples`、`series_uploads`/`series_chunks`、`workout_routes`/`route_chunks`/`route_points`、dataset revision、dirty scope、一个 `analysis.workout` job 和 change event；内容未变化不推进 revision、不生成 job。
- `protocolVersion` 升为 3；handshake 声明 `workoutBundles=true`、`bundleProtocolVersion=3`、`routes=true`，不再声明 page/batch/association/enrichment/quantitySeries/workoutPackages 能力。
- 限额：bundle 32 MiB、50,000 samples、16 routes、1,000,000 route points、1,000,000 series points；UploadLane 请求预算 120 秒。旧 batch/page/route chunk 限额不再属于客户端合同。
- 新增 `workout_imports` 的 append-only 回执历史；`GET` 按 `sha256` 精确恢复，省略时返回最新回执。

## 0.9.0 — 2026-10-27

## 2026-10-28 — P4 analysis

新增两个只读 current analysis 查询及 DTO，handshake analysis=true；保持 protocolVersion=2 和所有 P3 上传身份、sequence、seal、durable receipt 字段不变。analysisJobIDs 数量不固定；sample 批次按 type/UTC 日聚合依赖任务，旧回执不重写。结果缺失为 null/insufficient，状态为 load-only、有显式算法版本和输入谱系。

- Phase 3 同步合同扩展（schema 11）：route manifest、不可变分块、点投影触发器、双 hash chain、原子 seal、sealed 后不可变和独立删除屏障。
- 新增 append-only Workout enrichment 与 sample↔Workout 迟到关联；Workout 主体仍不可变，旧源版本或不同值不得覆盖新值。
- 新增 `POST /v2/routes`、`GET /v2/routes/{id}`、`PUT /v2/routes/{id}/chunks/{index}`、`POST /v2/routes/{id}/seal`、`POST /v2/routes/{id}/delete`、`POST /v2/sync/associations`、`POST/GET /v2/sync/workout-enrichments`。
- handshake `capabilities.routes` 置为 true，并增加 `sampleWorkoutAssociations`、`workoutEnrichment`；新增 route 限额。
- 新增稳定错误：`unsupported_protocol`、`unsupported_unit`、`route_identity_conflict`、`route_chunk_conflict`、`route_chunk_gap`、`route_already_sealed`、`route_incomplete`、`route_manifest_mismatch`、`route_unavailable`、`route_not_found`、`route_chunk_too_large`、`route_point_too_large`、`route_too_large`、`sample_association_conflict`、`association_unavailable`、`workout_enrichment_conflict`、`workout_enrichment_stale`、`workout_unavailable`、`workout_enrichment_not_found`。
- 仅新增 migration 00011；既有 batch/page/series/Workout package 身份和回执不变。`protocolVersion` 保持 2。

# 合同变更记录

## 2026-10-30 Device Acceptance — RunningGroundContactTime

真实 Workout 先后因触地时间及步幅 statistics 被旧 Swift/Go 白名单拒绝。显式增加 HKQuantityTypeIdentifierRunningGroundContactTime（ms）、RunningStrideLength（m）及同组 SDK 定义的 RunningVerticalOscillation（m，从 cm 显式转换）。保留所有原始统计聚合。未知类型及错误单位继续拒绝，错误一次列出当前 Workout 的全部未知 statistic 类型；不跳过字段、不增加全局 HealthKit 读取权限或 stream。协议2、schema13、不可变身份/回执/删除事务和分析算法均保持；P4 不新增跑步动态派生算法。

后续按用户要求在手机完整盘点一条跑步后，确认10类统计、6个metadata键及关联样本读取范围。客户端另将已支持的基础能量和三种跑步动态纳入只读授权/独立全局stream/原Workout关联路径，须在手机重新请求这4项权限；既有6条stream身份及anchor不变。Workout/event/activity 的已观察 METs、累计爬升、湿度、温度通过既有 metadata JSON 保存 `{value,unit}`，规范单位分别 kcal/(kg*hr)、m、1、degC；未知Workout metadata键或不兼容单位明确停止，不丢字段。已提交独立sample的metadata映射保持原合同，避免重写其不可变身份；不增加route metadata载荷、分析指标或schema。

## 0.8.0 — 2026-10-27

- 新增超大 Workout 细节分块包协议（schema 9）：独立 `workoutID`、核心 Workout 登记、连续不可变分块、固定摘要链和显式 seal；seal 前不创建 Workout 事实，seal 才在同一事务发布事实、规范化细节、业务 revision、脏范围、`analysis.workout` 待办和 change event。
- `GET /v2/handshake` 增加 `workoutPackageChunks`、`workoutPackageDetails`、`workoutPackageDetailsPerChunk`，并把 `capabilities.workoutPackages` 置为 true；routes／changefeed／analysis 仍为 false。该能力不表示 Workout 已完整采集，也不替代 Workout 发现。
- 新增稳定错误：`workout_package_not_found`、`workout_package_identity_conflict`、`workout_package_unavailable`、`workout_package_conflict`、`workout_package_chunk_conflict`、`workout_package_chunk_gap`、`workout_package_sealed`、`workout_package_incomplete`、`workout_package_manifest_mismatch`、`workout_package_chunk_too_large`、`workout_detail_too_large`、`workout_package_too_large`。
- 仅新增 migration 00009 与包表／删除触发器；既有批／页／series 回执、事实、流位置和预算语义未改。未改变 `protocolVersion` 或普通 Workout 载荷版本。

## 0.7.0 — 2026-10-27

- 新增数量 series 细项协议（schema 8）：父样本绑定、独立 `seriesID`、连续不可变分块、固定摘要链与显式 seal；分块只做耐久 staging，seal 才发布 `analysis.series` 待办并分配业务 revision。
- `GET /v2/handshake` 增加 `seriesChunks`、`seriesPoints`、`seriesPointsPerChunk`，并把 `capabilities.quantitySeries` 置为 true；routes／changefeed／analysis 仍为 false。该能力不表示服务端已知手机全部 series，也不表示完整 HealthKit 导入。
- 新增稳定错误：`series_parent_unavailable`、`series_identity_conflict`、`series_chunk_conflict`、`series_chunk_gap`、`series_sealed`、`series_incomplete`、`series_manifest_mismatch`、`series_not_found`、`series_chunk_too_large`、`series_point_too_large`、`series_too_large`；`Error` 增加 `expectedChunkIndex`。
- 仅新增 migration 00008 与 series 表；既有批／页回执、事实、流位置和预算语义未改。未改变 `protocolVersion` 或 Workout 载荷版本。

## 0.6.0 — 2026-10-27

- 修正 OpenAPI 能力口径：当前为 sample 与有界多活动 Workout v2（schema 7）；不带 activities 的旧 Workout v1 载荷仍兼容。
- 将 `GET /v2/handshake` 的 `limits`、`capabilities` 从泛型对象收紧为显式 schema，并保留运行时 handshake 作为能力和限额的权威来源。
- 仅文档与合同形状变更；未改变 `protocolVersion`、`workoutPayloadVersion`、端点、字段、错误码或限额值。

## P5 Training Plan / Coach Closed Loop（2026-10-28）

- 新增训练Plan/config、field override、Effective/history/audit、frozen execution和versioned feedback API；正常客户端默认使用Effective。
- command UUID不可变幂等；config/override/execution各有expected revision，冲突409；缺失分析不当作0。
- schema13新建训练领域，P3 protocolVersion2、上传/回执/anchor合同不变；未开始WorkoutKit或P6 UI。

## 2026-10-28 P6 read projections

`GET /v2/workouts[/{id}]`, `/v2/product/overview` and `/v2/analysis/history` add authenticated, dataset-scoped native product reads. Workout paging uses an opaque descending `(event_start, UUID)` cursor (max 50); overview covers two dataset calendar weeks; history reads at most 90 days of clean published state. Missing derived values stay null. No route point read API is advertised. No P3 upload, anchor, receipt, ingestion or migration contract changes.

## 1.1.0 — 2026-10-29 Full Review Correctness Fix

新增 bounded Training history summaries、immutable audit/detail section pagination；旧聚合入口小响应兼容，超限413并提供迁移Link。新页256 KiB，原snapshot/同步1 MiB不变；新命令/事件预算在COMMIT前检查。Training请求严格校验required/精确拼写/duplicate/null/type/unknown，合法HR/pace清空和command hash保留。新Effective保存training_merge/v2，旧缺省v1可重放；冲突事实保留、合法投影和可解释reason/conflict。P4升级workout.bundle/features v2和training_state.bundle/fitness_fatigue v2，其他组件v1不变；P3 wire protocol2、raw顺序、seal、receipt、墓碑和迁移00001–00013保持。

## 2026-10-29 Final Maintenance（OpenAPI 1.1.0）

合法 sample upsert/delete 混合页与全删除页按 type/UTC 日聚合依赖任务，旧回执不重写。Training 五类 JSON 写接口明确 application/json、无 Content-Encoding、64 KiB 和415/413/422错误边界，required/duplicate/case/null/unknown严格规则及合法命令hash不变。Route/series/Workout package 的每次PUT（含exact retry）均校验collector及upload lease；有效租约返回原回执，无效租约409 upload_lease_lost。客户端pending锁定可冲突编辑，独立重试原命令，不用旧payload替换新草稿。protocolVersion2、schema13、P4/P5算法、原事实/屏障/回执事务及历史保持。

## 2026-10-30 Native Workout Detail Read Projections

新增三个认证、dataset 范围的只读投影：`GET /v2/workouts/{id}/detail`、`GET /v2/workouts/{id}/series`、`GET /v2/workouts/{id}/freshness`。detail 返回事实头、环境观测/修正、Minetti 等强配速摘要、已发布负荷/区间、采样路线（≤500 点）与执行快照覆盖；series 返回八条展示时序（每项 ≤600 点，保留首尾/极值）、公里分段、实测 activities 分段、坡度和跑姿动态；freshness 逐指标返回 `present | not_seen_yet`。未发布分析保持 `null`/pending，不以 0 冒充。路线每点速度/海拔/时间戳和时序 `totalSamples` 保留在接口但不逐点渲染。无 migration，P3 上传/回执/anchor、schema 13、protocolVersion 2 不变。

## 2026-10-31 Workout Detail Correctness Fixes

路线 ±5 秒偏差保留原始事实，仅在分析输入和派生投影中排除窗口外点。活动分段要求至少两个连续活动，事件回退显式支持 HealthKit lap=3 / segment=7。心率区间去除同值重复、排除冲突重叠时间。未发布分析时仅返回事实及原始展示时序，环境观测保留而修正状态为 pending。执行快照 Effort 明确为计划强度：负荷回退来源改为 planned_effort_fallback/v1（置信度 0.4），实际 RPE 不填充。无 migration，不改写历史事实、回执、已发布结果或下载水位。

## 2026-11-01 — RQrun Phase 2（RQ-005 / RQ-006 / RQ-009）

补齐 v3 嵌套 required/null/合法零值校验与扁平 field/reason 错误；OpenAPI 增加完整 WorkoutBundle 解码 schema。上传 ACK 正 revision 和字段类型明确化，协议版本、hash、合法客户端编码不变。GET 404 为可重试未确认，客户端持久退避及原文件保留；原回执/确认状态/回收授权原子持久化。DELETE ACK 与 Phase 1 删除屏障不变。详见 sync-v3 第 10 节。
