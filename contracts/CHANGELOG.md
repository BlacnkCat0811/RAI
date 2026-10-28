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
