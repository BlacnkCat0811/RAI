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
