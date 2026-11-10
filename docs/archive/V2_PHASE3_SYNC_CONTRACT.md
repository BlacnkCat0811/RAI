# RAI V2 Phase 3 同步合同（FROZEN）

状态：**FROZEN**（实施前冻结）。实施范围仅 HealthKit 增量同步、事实耐久、路线发布、删除屏障、迟到到达、回执恢复和异步任务入队；不包含分析算法、训练计划或 Web。

本文件是 P3 新增/变更语义的规范。既有 [v2 同步合同](../contracts/sync-v2.md) 继续约束 batch、页、series、Workout package、预算和租约；发生冲突时，本文件对 P3 新语义优先，既有字段和回执不被重写。

## 0. 协议版本

- 线上 `protocolVersion` 继续使用 **2**。这是 P2 已冻结且已有 fixture/测试覆盖的 envelope 版本，不另建 P3 平行版本。
- P3 新增的 JSON 请求同样显式携带 `protocolVersion: 2`；缺失或不等于 2 返回 `422 unsupported_protocol`。
- 不使用 `protocol_version: 1` 的第二套字段，避免同一协议出现两个版本号。
- HealthKit anchor 的二进制格式不进入协议，服务端不接收、不解释、不持久化 `HKQueryAnchor`。

## 1. 身份与本地/服务端状态

| 状态 | 所在位置 | 作用 |
|---|---|---|
| HealthKit anchor | iOS 本地 | 下一次 `HKAnchoredObjectQuery` 的查询起点；只在 durable ACK 后标记 delivered |
| next client sequence | iOS 本地 | 每个 logical stream 的下一个 `clientSeq` |
| pending batch | iOS 本地 | 已生成、未确认 durable 的不可变 batch |
| delivered batch | iOS 本地 | durable ACK 后可用于推进对应 anchor 的批次 |
| dataset / stream / batch | PostgreSQL | 服务端同步身份与提交顺序 |
| stream committed sequence | PostgreSQL | 服务端严格连续提交水位，不是下载水位，也不是 HealthKit anchor |
| receipt | PostgreSQL | 不可变最终回执，可由 batch ID 恢复 |
| route state | PostgreSQL | pending staging 或 sealed published |
| entity tombstone | PostgreSQL | 永久删除屏障，优先于任何旧 replay |

服务端不保存 HealthKit anchor。客户端在收到 `200` 且 `ingestStatus=committed` 的 receipt 之前，不得将对应 anchor 永久前移；未知结果先查原 batch 回执。

## 2. Logical streams

每条 stream 独立维护 `clientSeq` 和 `lastBatchID`。第一版要求：

- 每个 HealthKit sample type 使用独立 stream（由客户端 `queryFingerprint` 固定查询范围）。
- Workout 使用 `sampleType=HKWorkoutTypeIdentifier` 的独立 stream。
- 删除事件属于产生它的对象 stream，不另建“deletions stream”。
- Route、series、Workout package 和 sample-workout association 不占用普通 stream sequence；它们各自有独立身份与幂等 receipt。
- 不为每个 route 或每个 sample UUID 建立 stream。

`clientSeq` 从 1 开始，严格连续。服务端只接受 `lastClientSeq+1`；重复同 batch 返回原回执；缺口返回 `409 sequence_gap` 和 `expectedSeq`。第一版不实现乱序缓冲，客户端不得跳过失败批次。

## 3. Batch 合同

### 3.1 上传

沿用：

```text
POST /v2/sync/batches
PUT  /v2/sync/batches/{batchID}/body
GET  /v2/sync/batches/{batchID}
```

`POST` 只登记不可变 envelope。`PUT` 携带 gzip NDJSON，只有事务 COMMIT 后返回 `200 / ingestStatus=committed`。页可以有多片；每片自己 durable，最后一片才封页。

### 3.2 幂等和冲突

相同 `batchID` + 相同 canonical envelope + 相同 body hash：

- 不重复插入 sample/workout/route/association/job；
- 不重复推进 stream；
- 不重写最终 receipt；
- 返回原 receipt 字节语义。

相同 `batchID` + 不同 envelope 或不同 body hash：`409 batch_identity_conflict` / `payload_hash_mismatch`，不得覆盖。

### 3.3 原子提交

普通事实批次在同一事务至少提交：

```text
facts
+ workout child projections
+ tombstones
+ entity_changes
+ change_events
+ dirty_scopes
+ jobs
+ batch receipt
+ stream committed position
+ page fragment progress / final page receipt
```

任何一步失败全部回滚。`stream.last_client_seq` 不得领先于 durable facts 或 receipt；receipt 写入前失败时 batch 仍为 `registered`，客户端用同一身份重试。

### 3.4 回执

普通 receipt 至少包含 `batchID`、`ingestStatus`、`datasetEpoch`、`clientSeq`、`payloadSHA256`、`commitRevision`、`counts`、`analysisStatus`、`analysisJobIDs`。计数是 `inserted/unchanged/deleted/tombstoned/suppressed`。回执不包含分析结果。分析任务只表示已耐久排队，Worker 停止不影响事实 ACK。

## 4. Workout

### 4.1 不可变身份

沿用 P2 identity：

```text
(dataset_id, workout_uuid)
```

不可变字段：

- `workout_uuid`（HealthKit UUID）
- `startUs`、`endUs`
- `activityType`
- `durationUs`
- `source.bundleID`
- 第一次已提交的完整 Workout 内容（包括活动、统计、事件）

不同内容使用同一 UUID：冲突，不更新。删除后旧 replay：被 tombstone 抑制。

### 4.2 补充事实

允许在不修改 `workouts.payload` 的前提下追加**不可变补充事实**。字段分三类：

| 类别 | 字段 | 规则 |
|---|---|---|
| Identity / immutable |
| | workoutUUID, startUs, endUs, activityType, durationUs, source | 只能出现在首次 Workout；之后不同值冲突 |
| Supplementable |
| | totalDistanceM, totalEnergyKcal, metadata 中首次出现的键, routeAvailable | 只能 nil→value 或同值确认；不能覆盖已有不同值 |
| Never server-writable |
| | HealthKit anchor, 任意 SQL/内部修订号, 分析结果 | 不属于同步载荷 |

补充协议：

```text
POST /v2/sync/workout-enrichments
GET  /v2/sync/workout-enrichments/{enrichmentID}
```

请求：

```json
{
  "protocolVersion": 2,
  "datasetID": "...",
  "datasetEpoch": "...",
  "collectorEpoch": "...",
  "enrichmentID": "...",
  "workoutID": "...",
  "source": {"bundleID": "com.example.app"},
  "sourceVersion": 1,
  "observedAtUs": 1793059200000000,
  "totalDistanceM": 5230.5,
  "totalEnergyKcal": 320.2,
  "metadata": {"weather": "synthetic"},
  "routeAvailable": true
}
```

至少一个 `totalDistanceM`、`totalEnergyKcal`、`metadata`、`routeAvailable` 存在。所有数值必须有限且非负。`metadata` 必须是 JSON object，不能覆盖既有键的不同值。

规则：

- 同 `enrichmentID` 同 canonical 内容：返回原 receipt。
- 同 `enrichmentID` 不同内容：`409 workout_enrichment_conflict`。
- `observedAtUs` 早于该 Workout 已接受的最高 `observedAtUs`：`409 workout_enrichment_stale`；不得因为请求晚到而用旧版本覆盖新版本。
- 同 source 的 `sourceVersion` 必须严格递增；同版本不同 enrichment 冲突。
- 新补充事实在提交事务中更新 dataset revision、Workout dirty scope、`analysis.workout` job 和 change event。
- 补充事实不修改 Workout 的不可变主体；查询 Workout 的客户端/分析层按 `observedAtUs`、`sourceVersion` 读取当前补充视图。
- Workout 已删除或 tombstoned：`409 workout_unavailable`。

`routeAvailable=false` 不等于永久“无路线”结论；路线是否存在仍以 route 注册/删除状态为准。route seal 会另产生 Workout dirty scope。

## 5. Samples 与关联

### 5.1 Sample identity 和单位

沿用：

```text
(dataset_id, sample_type, sample_uuid)
```

无稳定 UUID 的样本在 P3 不支持。客户端不得用时间/值 hash 伪造身份。

规范单位与现有白名单一致，例如 `heartRate → count/min`、`distance → m`、`speed → m/s`、`power → W`、`energy → kcal`、`cadence → count/min`。未知 type 返回 `422 unsupported_payload`；已知 type 的错误/未声明单位返回 `422 unsupported_unit`。服务端不静默转换未知单位。

删除只允许显式来源的 delete event；重复 delete 幂等。旧 upsert 被 tombstone 抑制。

### 5.2 Sample ↔ Workout 关联

关系复用 `workout_samples`，一个 sample 最多关联一个 Workout。接口：

```text
POST /v2/sync/associations
```

```json
{
  "protocolVersion": 2,
  "datasetID": "...",
  "datasetEpoch": "...",
  "collectorEpoch": "...",
  "associationID": "...",
  "sampleType": "HKQuantityTypeIdentifierHeartRate",
  "sampleUUID": "...",
  "workoutUUID": "...",
  "source": {"bundleID": "com.example.app"}
}
```

支持四种顺序：

```text
Workout → sample
sample → Workout
Workout 和 sample 同 batch
sample 后补关联到 Workout
```

最终收敛到同一关系表。规则：

- 同关系重复提交：幂等，返回已有 receipt；不新增 job 或 revision。
- 同 sample 被提交到另一 Workout：`409 sample_association_conflict`，不静默重绑。
- sample 或 Workout 不存在：`409 association_unavailable`。
- 任一父对象 tombstoned：`409 association_unavailable`，不得复活。
- 新关系提交事务写 relation、dataset revision、Workout dirty scope、`analysis.workout` job 和 change event。
- 父删除由现有 FK 级联删除关系，不删除另一父事实。

## 6. Route

### 6.1 生命周期

```text
register manifest
  ↓
upload immutable chunks (0..N-1)
  ↓
seal (validate manifest + counts + hashes + continuity)
  ↓
sealed / durable / published
```

`pending` 是 staging，不是可读取的完整 route。只有 `sealed` receipt 是 durable route 证明。

Route 独立于 Workout 上传：Workout 可以先 durable ACK，route 在之后任意时间上传和 seal。没有 route 的 Workout 合法（室内跑、treadmill、HealthKit 无 location）。

### 6.2 注册 manifest

```text
POST /v2/routes
```

```json
{
  "protocolVersion": 2,
  "datasetID": "...",
  "datasetEpoch": "...",
  "collectorEpoch": "...",
  "routeID": "...",
  "workoutID": "...",
  "source": {"bundleID": "com.example.app"},
  "expectedChunkCount": 3,
  "expectedPointCount": 14400,
  "firstRecordedAtUs": 1793059200000000,
  "lastRecordedAtUs": 1793073599000000,
  "expectedPayloadManifestSHA256": "optional 64 hex"
}
```

服务端校验 identity、Workout 已 durable、dataset/epoch、collector、tombstone、计数与时间范围。重复相同 manifest 返回原 state；不同 manifest 返回 `409 route_identity_conflict`。

### 6.3 Chunk

```text
PUT /v2/routes/{routeID}/chunks/{index}
Content-Type: application/x-rai-health-route+ndjson
Content-Encoding: gzip
```

一行一个点：

```json
{"sequence":0,"recordedAtUs":1793059200000000,"latitude":31.2304,"longitude":121.4737,"altitude":4.2,"horizontalAccuracy":5.0,"verticalAccuracy":6.0,"speed":2.8,"course":91.5}
```

字段：`sequence` 全局从 0 连续；`recordedAtUs` 位于 manifest 首末时间之间；纬经度合法；可选 `altitude/horizontalAccuracy/verticalAccuracy/speed/course` 缺省不补零，显式 null 拒绝。服务器按来源顺序保存，不排序、不合并、不去重。

限额（协议限制）：每 chunk 最多 **5000** 点、wire 2 MiB、decoded 8 MiB、单行 64 KiB；每 route 最多 1000 chunks、1,000,000 点。5000 同时是当前实现上限。

规则：

- 同 `(routeID,index)` 且相同 exact wire SHA-256：返回原 chunk receipt。
- 同 index 不同 wire hash：`409 route_chunk_conflict`，不得覆盖。
- index 不连续：`409 route_chunk_gap` + `expectedChunkIndex`。
- seal 后上传新的 index/内容：`409 route_already_sealed`；已提交 chunk 的相同 wire hash 精确重试返回原 receipt，不改状态。
- route tombstoned/不存在：`409 route_unavailable` / `404 route_not_found`；旧 chunk 不得复活。
- chunk 事务写 immutable chunk、点投影、pending 计数和 hash chain，不分配业务 revision、不创建 job。

点数组 canonical digest：

```text
chunkSHA256 = SHA256(canonical JSON array of points)
pointsManifest(0) = 32 zero bytes
pointsManifest(i+1) = SHA256(pointsManifest(i) || chunkSHA256(i))
```

wire hash chain（可选 total payload hash 的实现）：

```text
payloadManifest(0) = 32 zero bytes
payloadManifest(i+1) = SHA256(payloadManifest(i) || wireSHA256(i))
```

### 6.4 Seal

```text
POST /v2/routes/{routeID}/seal
```

```json
{
  "protocolVersion": 2,
  "chunkCount": 3,
  "totalPoints": 14400,
  "manifestSHA256": "...",
  "payloadManifestSHA256": "..."
}
```

Seal 必须验证：

- route 属于请求 dataset 和已有 Workout；
- dataset/epoch/collector 未变；
- 所有 chunk 存在且 index 为 0..chunkCount-1；
- 点 sequence 全局 0..totalPoints-1、无缺号/重复；
- 每个 chunk 的 point_count、canonical hash、manifest chain 一致；
- route 注册的 expected counts 和首末时间一致；
- `manifestSHA256`、`payloadManifestSHA256` 与服务器链一致；
- 没有 tombstone、conflict 或已 sealed 的不同声明。

成功事务原子写：route `sealed` + receipt + dataset revision + Workout dirty scope + `analysis.workout` job + change event。失败不部分发布。相同 seal 重试返回原 receipt；不同声明 `409 route_already_sealed` / `409 route_identity_conflict`。

### 6.5 发布后不可变

已 sealed route：

- 不能替换/追加 chunk；
- 不能修改 point 或重排；
- 不能移动到另一个 Workout/dataset；
- 第一版不支持 generation/replacement；HealthKit 后续更新必须作为客户端显式待办，不能静默重建。

### 6.6 删除

```text
POST /v2/routes/{routeID}/delete
```

```json
{"protocolVersion": 2, "datasetEpoch": "...", "collectorEpoch": "...", "reason": "healthkit_deleted"}
```

- 明确删除才允许；空查询/权限变化不推断删除。
- 写 `entity_tombstones`（route type + route UUID）并删除 route/chunks/points；重复删除幂等。
- 已知 route 删除时 bump revision，产生 Workout dirty scope、job 和 change event；未知 route 只建立屏障，不伪造分析范围。
- Workout 删除必须为每个现存 route 建立 tombstone，再随 FK 级联清理。
- tombstone 后旧 chunk/register/seal 重试必须失败，不得恢复 route。

### 6.7 Projection integrity

`route_chunks.points` 是原始 canonical 内容，`route_points` 是同事务生成的有界投影。数据库触发器和最小权限保证：

- API role 不能直接 INSERT/UPDATE/DELETE `route_points`；
- API role 不能直接设置 route sealed 状态或 receipt；
- chunk 写入只能通过受控函数，由函数生成投影并推进 hash chain；
- seal 只能通过受控函数，函数验证完整性后设置状态；
- 集成测试验证无法绕过正式路径发布或伪造 projection。

## 7. 删除、迟到和 delete-wins

永久屏障：`entity_tombstones`。规则：

```text
delete version > stale replay
```

必须收敛：

| 场景 | 预期 |
|---|---|
| Workout 删除后旧 sample 迟到 | sample 可独立存储，但不得重新关联/复活 Workout |
| Workout 删除后旧 route 迟到 | route 注册/ chunk / seal 失败，Workout 不复活 |
| sample 删除后旧 batch 重试 | sample 被 suppressed/tombstoned，不插入 |
| route 删除后旧 chunk 重试 | 失败，不恢复 |
| sample 删除后同 UUID 带不同内容 | 冲突或被屏障抑制，绝不更新 |
| Workout 删除后同 UUID 不同内容 | 冲突/屏障，不插入 |

删除 receipt 可重复查询；不因重复 delete 返回不可恢复错误。

## 8. 同步事务与 durable ACK

DURABLE 定义：

```text
PostgreSQL transaction COMMIT
+ 相同 batch 重试不会重复事实
+ 服务端已有 receipt/stream 状态可恢复后续处理
```

不得把“已读入内存”“已写 staging registration”“已发到 queue”当作 durable。Route seal 的 durable 点是 sealed receipt COMMIT；route chunk 的 durable 点是 chunk+projection+manifest COMMIT，但仍不是完整 route。

事实事务不等待 Worker。Worker 离线时，`jobs` 行、dirty scope 和 receipt 仍随事实提交，API 必须 ACK。

## 9. 错误合同

错误响应固定 JSON object，至少 `code`；可带 `expectedSeq`、`expectedChunkIndex`、`eventIndex`、`retryAfterSeconds`。不返回 SQL、DSN、健康值或原始 payload。

| HTTP | code | 客户端动作 |
|---|---|---|
| 400 | invalid_request | 修正请求 |
| 401 | unauthorized | 保留本地状态，修复凭证 |
| 403 | dataset_forbidden / read_only_device | 停止越权写入 |
| 404 | batch_not_found / route_not_found / stream_not_found | 核对身份，不推进 anchor |
| 409 | sequence_gap、previous_batch_mismatch | 查询前批，保持顺序 |
| 409 | batch_identity_conflict、payload_hash_mismatch | 保留原文件，报告冲突 |
| 409 | sample_identity_conflict、workout_identity_conflict | 不覆盖既有事实 |
| 409 | sample_association_conflict、association_unavailable | 不重新绑定 |
| 409 | workout_enrichment_conflict、workout_enrichment_stale、workout_unavailable | 保留本地待办，不用旧值覆盖 |
| 409 | route_identity_conflict、route_chunk_conflict、route_chunk_gap、route_already_sealed、route_incomplete、route_manifest_mismatch、route_unavailable | 核对 manifest/chunk/父对象，不重建旧身份 |
| 413 | batch_too_large、route_chunk_too_large、route_too_large | 按协议拆批/分块 |
| 415 | unsupported_media_type、unsupported_content_encoding | 修复传输头 |
| 422 | unsupported_protocol、unsupported_payload、unsupported_unit | 保留本地待办，不静默丢弃 |
| 429 | *_rate_exceeded、*_capacity_exceeded | 按 Retry-After 重试原身份 |
| 503 | temporarily_unavailable | 状态可能未知；先查原 batch/route，再重试 |

状态码稳定：validation 400/409/413/415/422；conflict 409；temporary 503；success/replay 2xx。

## 10. 限额和压缩

- 普通 batch：沿用 1000 events、2 MiB wire、8 MiB decoded、64 KiB line、16 KiB registration JSON；响应/请求超限明确拒绝。
- Route chunk：5000 points、2 MiB wire、8 MiB decoded、64 KiB line、注册 JSON 16 KiB。
- Series/Workout package 保留既有上限。
- 第一版使用 gzip + JSON/NDJSON，不引入 protobuf、Redis、消息队列、PostGIS 或 TimescaleDB。

## 11. 客户端不变量

iOS 每个 logical stream 独立持久化：

```text
HealthKit anchor
next client sequence
pending batch identities + immutable files
delivered batch IDs + original receipts
retry count / next retry time
```

流程：

```text
anchor A
  → HKAnchoredObjectQuery
  → encode immutable RAI batch
  → upload
  → durable receipt
  → mark batch delivered
  → advance local anchor/page_seq
```

- timeout/5xx/connection lost/app killed：不得丢弃 pending batch。
- 同一 batch 重试必须复用原 batch ID、envelope 和 gzip 文件。
- 收到 durable receipt 后才能推进对应本地 anchor；服务器 commit 但 response 丢失时，GET batch 恢复。
- 初始历史导入和日常增量使用同一 contract；历史数据分多个有界 batch，低优先级/有限并发，不建第二套 importer。
- 同时有新 Workout 和 backfill 时，客户端优先最新增量。
- 后台任务只用于触发；correctness 不依赖 BGProcessingTask 一定执行。

## 12. 验收矩阵（P3 必须覆盖）

Batch：first upload、exact retry、conflict retry、sequence gap、concurrent same batch、response lost after commit。

Workout：insert、duplicate、enrichment、stale enrichment、delete、stale replay。

Sample：insert、duplicate、unsupported unit、delete、late association、association conflict。

Route：register、chunk upload、duplicate chunk、chunk conflict、gap、missing chunk seal、seal、duplicate seal、post-seal mutation reject、late arrival、delete、stale chunk replay、concurrent chunk/seal。

Transaction：registration rollback after batch; fact rollback after Workout/sample; job failure rollback; receipt failure rollback and retry; route seal failure rollback.

Concurrency：duplicate requests、concurrent same batch、concurrent route chunk、concurrent seal、worker + sync contention。

Recovery：API restart、PostgreSQL unavailable/restart、app kill、network loss、response loss、Worker offline。

性能：samples 250/500/1000/2000（2000 通过两个 1000 连续批次验证）、route 500/1000/5000 points/chunk，记录 wire/gzip、validation、DB transaction、total server latency。ACK SLO 在实测后冻结，P3 不承诺亚 100 ms。

## 13. 不在 P3 范围

不实现 TRIMP、fitness、fatigue、recovery、readiness、running score、recommendation、ML、training plan、coach、override、execution、Web/Vite、Redis/Kafka/RabbitMQ/NATS/TimescaleDB/PostGIS。Worker 只回收租约，不执行分析；P4 才定义算法。
