## 2026-11-10 — 状态快照语义与产品空态澄清 / OpenAPI 1.13.2

- `GET /v2/sync/workout-ingests/{workoutID}` 的 `analysisStatus`/`analysisJobIDs` 明确为接收提交时的快照，Worker 完成后不变；实时状态读 `/v2/jobs/{id}`，已发布结果读 `/v2/analysis/workouts/{id}`。服务端行为不变（回执与历史语义保留），只澄清合同文本。
- iOS 首页把 `not_product_activated` 归入预期空态：不再显示"可下拉重试"的故障横幅，身体状态/训练负荷按空态呈现。同步徽标改为读取持久化的最近上传时间，重启后不再回到"尚未同步"；最近运动行在缺少距离数据时不再渲染"-- 公里 · --/公里"。

## 2026-11-10 — Workout 手动上传审查修复 / OpenAPI 1.13.1

- POST 的成功回执仅允许 `accepted`，且必须包含首次 `acceptedAt`。已有 Workout 却没有接收行时返回 409 `workout_ingest_missing_receipt`；GET 的历史 `already_imported` 状态继续保留，客户端将其视为一致性错误。
- v6 原始字节继续精确保存在 `workout_ingests.raw_json`；`workout_imports.payload` 改为内部来源及事实身份投影，避免未知扩展超出 JSONB 表示范围时阻断事实事务。未知 `observation` 不参与 v6 分析解释。
- 页面退出、取消和进入后台结束所属上传轮次；历史分页使用开始时间游标，并完整读取边界处同开始时间的记录。

## 2026-11-10 — Workout v6 前台手动上传 / OpenAPI 1.13.0

- Workout 上传只由用户显式点击同步触发；启动、回到前台、HealthKit 变化、后台任务完成都不自动上传。每轮先握手一次，然后逐条 Workout 读取、组装并发送；一个 Workout 一次 POST，批量结果只统计本轮成功/失败及原因。
- 客户端移除 Workout 上传队列、Outbox 恢复、后台 URLSession、上传前状态查询、自动重试和分析轮询。200 表示原始字节已持久接收；成功不等待投影或分析，训练详情需要时另行读取服务端结果。
- 已开始的 metric 查询报错、route 读取中断或限额溢出使该 Workout 本轮失败，不上传部分结果；室内无路线与合法空流可上传，已知坏点可跳过并记录诊断。顶层 `workoutID` 仍是唯一上传的 HealthKit 对象 UUID。
- `contracts/sync-v6.md` 第 2、3.4、5.3、7、8 节改为前台手动上传语义；`contracts/openapi-v2.yaml` 升为 1.13.0，ingest 端点说明只允许显式前台单次请求，GET 状态不属于上传流程。

## 2026-11-10 — v6 HealthKit 对象 UUID 仅保留顶层 Workout ID

- iOS 的 `workoutID` 是载荷中唯一的 HealthKit 对象标识；活动分段移除残留的 `uuid`。标量、series 和路线继续不携带对象 UUID。dataset 与 epoch 会话身份保持独立。
- 服务端为活动按 dataset、Workout 和原数组下标生成确定性内部 UUID；缺少客户端 UUID 的活动仍正常投影与入库。额外活动 `uuid` 仅保留在原文，不影响投影。无需 schema migration。

## 2026-11-10 — Workout 同步只保留 v6（硬切）/ OpenAPI 1.12.0

- 服务端与 iOS 只保留 protocol 6：删除 v3/v4/v5 Workout bundle 解码器、`POST/GET/DELETE /v2/sync/workout-bundles` 入口、`X-RAI-Sync-Protocol` 协商、`RAI_WORKOUT_PAYLOAD_V6` 与 `RAI_WORKOUT_UUID_INGEST_MODE` 开关。握手固定返回 `workoutPayloadVersion=6`、`workoutUUIDIngestVersion=1`、`workoutUUIDIngestAccepting=true`，不再返回 `workoutBundles`/`bundleProtocolVersion`/`bundleObservationVersion`/`bundleCompact`/`explicitChildDeletions`。
- v6 唯一的 Workout 接收入口是 `POST /v2/sync/workout-ingests/{workoutID}`；状态读取仍按 `deleted > accepted/committed/rejected > already_imported > 404` 排序，重复 UUID 只重放首次 `acceptedAt`。
- 客户端删除同步移除：iOS 不再生成 sample/route/Workout DELETE，服务端删除能力只保留管理路径。旧版本已确认回执保留为只读历史；未确认的旧任务在重建索引时释放载荷并标记 `protocol_upgrade_required`，显式重试时从 HealthKit 重建 v6。
- `contracts/openapi-v2.yaml` 升为 1.12.0：删除 workout-bundles 路径与 v3/v4/v5 bundle schema，握手能力枚举固定为 6，ingest 说明只描述 protocol 6；`contracts/sync-v6.md` 删除回退与兼容章节，记录硬切及旧任务处理。`contracts/sync-v3.md`、`sync-v4.md`、`sync-v5.md` 状态改为“已撤回”，原文保留为历史记录，不再作为实现合同。

## 2026-11-09 — Workout 同步协议 v6 原始序列一次打包 / OpenAPI 1.11.0

- 新增 `contracts/sync-v6.md`：一个 Workout 一个 JSON、一次 multipart + gzip 上传。流只带类型、单位、诊断状态和 `[[startDeltaUs,endDeltaUs,value],...]` 三元组；series 以父样本表头加同型点子点分组；route 点改为 3..8 元素元组。v6 不发送逐样本 UUID、metadata 或 source，也不发送 v4 的 `sourceTable`/`metadataTable`、`observation`、删除数组。
- 事实身份由服务端确定性生成：UUIDv5(SHA-1)，命名空间 `c9f3e2a1-7b64-5d8e-9a0b-1c2d3e4f5a6b`，名称 `rai/v1|<datasetID>|<workoutID>|<sampleType>|sample|series/route|<wire 序号>`；序号包含被跳过的条目，重放同一字节得到同一身份，不随机、不按时间、不按数据库序列。合同记录三组 test vectors。
- 全包来源继承：所有投影事实使用 Workout 顶层 `source`；Workout 来源仍须通过冻结的 `rai.workout.source_policy.apple_watch_only/v1`，混合 Watch+iPhone 子样本不在逐样本层面区分。v6 不做删除同步，v3 删除能力只保留给 v3 与管理路径。
- 限额沿用并明确：样本+series 组原始条目 50,000、series 点 1,000,000、route 16 条、route 点 1,000,000、包 32 MiB；超限 413 `bundle_too_large`，不截断。未知流类型、单位不匹配、非法样本/组/点是软跳过；身份、协议版本、时间范围、来源、Workout 核心、JSON 结构限制是硬错误。
- 握手新增客户端请求 `X-RAI-Sync-Protocol: 6`；服务端仅在 `RAI_WORKOUT_PAYLOAD_V6=1` 时回 `workoutPayloadVersion=6`/`bundleProtocolVersion=6`，否则按 4 回退，客户端用 v4 builder 生成原字节。iOS `workoutPayloadV6` 默认关闭。UUID 接收路径接受 v4/v6，任务记录实际载荷版本，重投绝不把 v6 字节当 v4 校验。
- `contracts/openapi-v2.yaml` 升为 1.11.0，握手能力枚举加入 6，ingest 端点说明覆盖 protocol 4/5/6。

## 2026-11-09 — Workout 同步协议 v5 紧凑载荷 / OpenAPI 1.10.0

- 新增 `contracts/sync-v5.md`：在 v4 原始载荷语义不变的前提下，把逐样本重复的完整 `source` 去重到顶层 `sourceTable`，非空 `metadata` 去重到 `metadataTable`，样本改为 4..6 元素列式数组，series/route/样本时间改为相对顶层 `startUs` 的微秒增量；样本 UUID 保留为 `health_samples` 事实身份。
- 握手新增 `X-RAI-Sync-Protocol: 5`，返回 `workoutPayloadVersion=5`、`bundleProtocolVersion=5`、`bundleCompact=true`；服务端同时接受 v4，客户端未协商到 5 时回退 v4，已封存的 v4 包按原字节上传。
- 服务端 v5 解码与 v4 共用事实投影、来源分类、series 父引用和 route 身份；`sourceTable`/`metadataTable` 只做编码去重，不产生新事实。分析/特征/完整性读取按 `protocolVersion` 选择 v4 或 v5 来源表提取器。
- `contracts/openapi-v2.yaml` 升为 1.10.0，握手能力版本枚举加入 5。

## 2026-11-09 — Workout UUID 一次接收 / schema 50 / OpenAPI 1.9.0

- 新增 `POST /v2/sync/workout-ingests/{workoutID}`：首次成功接收的 protocol-4 原始载荷按 `(dataset_id, workout_uuid)` 唯一保存，是唯一权威副本；重复 UUID 即使字节不同也重放首次 `acceptedAt`，不替换原始字节。已有 Workout 但无接收记录返回 `already_imported`，根删除屏障返回 409 `workout_deleted`。`POST /v2/sync/workout-bundles` 的 v4 路径在 `uuid` 模式返回 409 `workout_uuid_ingest_required`，`uuid_paused` 返回 503；v3 与管理删除路径不变。
- 新增 `GET /v2/sync/workout-ingests/{workoutID}`：单快照状态，优先级 `deleted` > `accepted`/`committed`/`rejected` > `already_imported` > 404 `workout_ingest_not_found`；`committed` 带 `commitRevision`/`analysisStatus`/`analysisJobIDs`，`rejected` 带 `errorCode`；严格按 dataset 隔离。
- 握手新增 `workoutUUIDIngestVersion=1` 与 `workoutUUIDIngestAccepting`；服务端模式由 `RAI_WORKOUT_UUID_INGEST_MODE=legacy|uuid|uuid_paused` 控制，默认 `legacy`。
- Schema 50 新增 `workout_ingests`（原始字节、载荷大小、接收设备、epochs、状态、接收/完成时间、提交回执、错误码、尝试次数、下次尝试与租约）。每 dataset 待处理配额 200 行 / 512 MiB；Worker 单解析循环以 90 秒租约、60 秒处理预算和认领令牌把事实、历史、分析待办与 `committed` 状态放在同一事务；永久错误直接 `rejected`，暂态错误最多 3 次，过期末次认领终结为 `rejected`。
- 显式 Workout 删除清空待处理原始载荷并终止接收行，但保留接收身份与既有回执。
- `contracts/sync-v4.md` 新增第 7 节；`contracts/openapi-v2.yaml` 升为 1.9.0。



- 原地重写 `contracts/sync-v4.md`：v4 改为单 Workout 原始 JSON 上传（顶层 `source`/`metadata`/`workout`/`streams`/`series`/`routes`，逐样本内联 `source`/`metadata`）；移除 `sources` 表、`kind`、`uuids`/`src` 平行数组、`metadataRegistryVersion`、`observation`/`coverage`、`deletions`/`routeDeletions`。未知字段合法并保留在 `workout_imports.payload` 精确字节中；服务端只投影已知事实，坏流/样本/点跳过；样本/route/series 上限仍返回 413 `bundle_too_large`。
- 不做删除同步：v4 客户端不再生成 sample/route 删除或 DELETE，HealthKit 删除事件不改变服务端事实；v3 删除语义不变。
- 服务端移除 `SYNC_V4_ENABLED`：请求头 `X-RAI-Sync-Protocol: 4` 直接协商 `workoutPayloadVersion=4`、`bundleProtocolVersion=4`、`bundleCompact=true`。客户端 v4 默认开启，`syncProtocolV4=false` 可回退 v3。
- v4 在此前从未部署，因此不保留旧列式 v4 兼容或数据迁移；同 Workout 同字节重试仍返回原回执。
- 服务端修复：v4 Workout 核心（`activityType`/`durationUs`）缺失或非法此前返回非 `*Error`，HTTP 层会变成 500；现在统一 422 `unsupported_payload`。

## 2026-11-08 — Workout Sync v4 / model completion / schema 46

- 新增 `contracts/sync-v4.md`：单 Workout 列式 bundle（protocolVersion=4），显式 sample/route deletions、observation coverage、32 MiB wire / 8 MiB gzip / 100:1 解压比与严格 JSON 限制。
- 握手在 `X-RAI-Sync-Protocol: 4` 时返回 `workoutPayloadVersion=4`、`bundleProtocolVersion=4`、`bundleCompact=true`；未协商时保持 v3，服务端 `SYNC_V4_ENABLED` 可关闭。
- OpenAPI 握手能力改为允许 3/4；v4 请求体以 `sync-v4.md` 为准（OpenAPI 的 `WorkoutBundle` schema 仍描述 v3）。
- Schema 45 新增 `analysis_published_results` 只读视图，API 不再直接读取 `analysis_results`；Schema 46 将 `rai_training_dirty` 改为 SECURITY DEFINER，撤销 API 直接更新 jobs 的授权；00030/00032 补齐 `vo2max_source_selection` 输入 kind/schema。

## 2026-11-05 — UA1-R3B / workout-sync-completeness/v2 / source policy v1

- 新增 `rai.workout.source_policy.apple_watch_only/v1` 与共享 golden vectors（Swift/Go 同源）。
- 新增 `workout-sync-completeness/v2`：三层 inventory（visible = eligible + excluded + unclassified）、
  exclusion evidence、策略版本绑定与服务器侧策略重算；v1 语义与 wire facts 不变。
- schema 00029 为 `workout_sync_completeness` 增加 v2 列与协议/策略一致性约束；v1 行保持原语义。
- 握手新增 `workoutSyncCompletenessV2Version` 与 `workoutSourcePolicyVersion` 能力。

## 2026-11-01 — Phase 5A / OpenAPI 1.5.0 / Protocol 3 / Schema 18

- operator-only `rai admin new-dataset`：全新五项身份、初始 revision 0、source 原地退役；创建/retire/审计同事务，operation 幂等，0600 一次凭证文件支持响应丢失恢复，dry-run 严格只读。
- 所有事实/训练/Worker 持久写入核验 retired 生命周期；`409 dataset_retired`，旧数据和旧 pending job 保留。握手增加 deviceID、collectorEpoch、datasetStatus。
- iOS 持久 Session、独立 Keychain/Outbox/ProductJournal、成功握手后原子激活、后台 callback 按原 Session 路由、nil Anchor 重新发现可见历史；不复制训练历史/旧 tombstone，不导入旧 SQLite。
- 正式升级/灾难恢复采用 New Dataset + New Session。历史 reset 描述已失效：`LEGACY_UNSAFE_UPGRADE_PATH / DO NOT USE FOR PRODUCTION UPGRADE`。下面历史记录保留当时口径，不是现行操作指令。

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

## 2026-11-01 Phase 5B

Protocol 3 wire unchanged. Operator-only `rai init` now requires operation/actor/base-url/private credential-file, never emits a token to stdout, and resumes a committed operation with the same package. `admin new-dataset --operation-type disaster_recovery` identifies source-available recovery. Schema 19 expands only existing administrative audit constraints. Restored databases are archive/quarantine; active recovery always uses a fresh Dataset and client Session.

## 1.6.0 — M5A Daily Facts Sync v1 (2026-11-02)

Independent HRV SDNN, RHR and raw Sleep intervals use `/v2/sync/daily-facts`,
with per-type streams, immutable bounded batches, complete source descriptors,
delete precedence and received-discovery coverage. Existing Workout Protocol 3
wire semantics remain frozen. See `docs/DAILY_HEALTH_SYNC_CONTRACT.md`.
