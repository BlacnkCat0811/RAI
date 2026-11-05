# RAI Sync Protocol 3 — 单 Workout Bundle 上传

状态：实现合同。运行时以 `GET /v2/handshake` 返回的 `protocolVersion=3`、`workoutBundles=true` 和实际限额为准。

## 1. 设计目标

每个 WorkoutBundle 是一个不可变导入单元；同一 Workout 的迟到数据会形成新的 bundle 回执，但不覆盖旧回执：

```text
HKWorkout + 关联 samples + 多值 sample 的 series points + HKWorkoutRoute points
  → 一个 WorkoutBundle JSON
  → 一次 POST /v2/sync/workout-bundles（multipart/form-data，字段 file）
  → 服务端一个事务提交全部事实
  → 一个 analysis.workout job
  → 客户端 GET 回执 / 轮询分析
```

这与旧协议的逐 sample association、page、batch、series 注册/分块/seal、route 注册/分块/seal 相区分。旧上传路径不再提供写能力。本版本不兼容旧 v2 事实；正式升级采用 New Dataset + New Session，旧空间只读退役且不清空。`scripts/rai-reset-v2-data.sh` 标记为 `LEGACY_UNSAFE_UPGRADE_PATH / DO NOT USE FOR PRODUCTION UPGRADE`。

## 2. 请求

```http
POST /v2/sync/workout-bundles
Authorization: Bearer <device-token>
Content-Type: multipart/form-data; boundary=...

--boundary
Content-Disposition: form-data; name="file"; filename="workout.json"
Content-Type: application/json

{ ... WorkoutBundle ... }
--boundary--
```

- 字段必须恰好一个 `file`；额外 part 返回 422 `unsupported_payload`。
- 顶层 `Content-Encoding` 仍不接受；单个 file part 可为 `application/json`（兼容 `application/octet-stream`）或 `application/gzip`。gzip 文件名推荐 `workout.json.gz`，无其他 part/header 编码。
- 整个 wire body 上限 32 MiB + 64 KiB；file 的 decoded JSON 上限 32 MiB；gzip file 独立上限 8 MiB；framing/preamble/epilogue 合计最多 64 KiB。gzip 解压比最多 100:1 + 64 KiB 启动余量。
- 严格 JSON：未知、重复、缺失必填、显式 `null` 全部拒绝；不做静默丢弃。
- 相同 Workout UUID + 相同文件字节重试返回原回执，不增加 revision/job。

## 3. WorkoutBundle

```json
{
  "protocolVersion": 3,
  "datasetID": "<uuid>",
  "datasetEpoch": "<uuid>",
  "collectorEpoch": "<uuid>",
  "workoutID": "<HKWorkout.uuid>",
  "workout": { "op": "upsert", "kind": "workout", "type": "HKWorkoutTypeIdentifier", "uuid": "...", "startUs": 0, "endUs": 0, "source": {"bundleID": "..."}, "workout": { "...": "..." } },
  "samples": [
    { "op": "upsert", "kind": "sample", "type": "HKQuantityTypeIdentifierHeartRate", "uuid": "...", "startUs": 0, "endUs": 0, "source": {"bundleID": "..."}, "quantity": {"value": 120, "unit": "count/min"} }
  ],
  "series": [
    { "sampleType": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": "...", "points": [{"startUs": 0, "endUs": 0, "value": 120, "unit": "count/min"}] }
  ],
  "routes": [
    { "routeID": "...", "source": {"bundleID": "..."}, "points": [{"recordedAtUs": 0, "latitude": 31.2, "longitude": 121.4}] }
  ],
  "completeSampleTypes": ["HKQuantityTypeIdentifierHeartRate"],
  "completeRoutes": true
}
```

`Samples` 的成员关系就是 Workout 关联，没有独立 association 请求。`Series` 只能引用 `Samples` 中已经出现的父 sample；`Routes` 的点顺序由数组顺序决定，服务端生成从 0 开始的连续 `sequence`。

`completeSampleTypes` 和 `completeRoutes` 仅描述客户端读取覆盖，不提供删除授权或快照顺序。无论它们的值如何，普通快照中 Sample / Route 缺席都保留已有事实、关联、series 和路线点，不创建 tombstone；旧请求迟到不能删除新包已提交的数据。同一 UUID 的不同事实内容仍返回身份冲突，明确删除建立的屏障仍优先于原回执重放。

Phase 3 新增可选显式子删除和观察合同，见第11节；普通快照缺席仍不产生删除。旧 HTTP 逐子事实入口不恢复。

## 4. 服务端事务

同一个 PostgreSQL 事务内：

1. 锁 dataset 行，验证 dataset epoch、collector epoch。
2. 先检查 Workout 永久删除屏障，再验证 Workout、samples、series、routes 身份和内容哈希及子事实屏障；已有不同内容返回冲突。
3. 插入或确认 Workout、`health_samples`、`workout_samples`。
4. 写入 sealed `series_uploads`/`series_chunks`。
5. 写入并 seal `workout_routes`/`route_chunks`/`route_points`。
6. 保留快照中未出现的旧子事实；完整性标记不能触发删除。
7. 若有新事实：`commit_rev+1`、更新 Workout dirty scope、插入一个 `analysis.workout` job、插入一个 change event。
8. 追加一条不可变 `workout_imports` 回执（按 `bundle_sha256` 区分；同一 Workout 可有多条历史 bundle 回执）。
9. COMMIT 后返回 200。

任一步失败全部回滚，不留下半条 Workout、孤立 association、部分 route 或伪造回执。内容未变化时返回原回执语义，不推进 revision、不生成 job。

## 5. 回执

```json
{
  "workoutID": "<uuid>",
  "bundleSHA256": "<64 hex>",
  "ingestStatus": "committed",
  "commitRevision": 123,
  "counts": {
    "workoutInserted": true,
    "samplesInserted": 5464,
    "samplesUnchanged": 0,
    "samplesDeleted": 0,
    "associationsInserted": 5464,
    "associationsUnchanged": 0,
    "associationsDeleted": 0,
    "seriesInserted": 909,
    "seriesUnchanged": 0,
    "routesInserted": 1,
    "routesUnchanged": 0,
    "routesDeleted": 0
  },
  "analysisStatus": "queued",
  "analysisJobIDs": ["<uuid>"]
}
```

`bundleSHA256` 始终是原始、解压后 JSON 文件字节的精确 SHA-256。它不是 gzip 或 multipart 的哈希，也不重新 canonicalize；语义身份沿用原有精确 payload identity。相同 JSON 的 plain/gzip、不同 gzip header/压缩实现必须返回同一回执。`analysisStatus=queued` 只表示分析待办已持久化；最终结果继续通过 `GET /v2/analysis/workouts/{id}` 查询。`not_required` 表示本次没有新事实。

## 6. 状态恢复

```http
GET /v2/sync/workout-bundles/{workoutID}?sha256=<bundleSHA256>
```

按 bundle 哈希返回该次上传的原始回执；省略 `sha256` 时返回该 Workout 最新回执。上传响应丢失后客户端应先按同一哈希 GET 恢复，而不是重新读取 HealthKit 或重建不同字节。404 `workout_import_not_found` 仅表示当前查询未找到该回执，不能证明删除或授权丢弃上传；原包继续持久退避和重试。

同一 Workout 的多个不同 bundle 各自保留源 JSON 和回执；旧 bundle 回执不会被新 bundle 覆盖。

## 7. 限额

| 项 | 上限 |
|---|---:|
| 整个 HTTP wire body | 32 MiB + 64 KiB |
| multipart framing（含 preamble/epilogue） | 64 KiB |
| gzip file | 8 MiB |
| 解压后 JSON / plain file | 32 MiB |
| gzip 解压比 | 100:1 + 64 KiB |
| samples | 50,000 |
| series 条目 | 50,000 |
| series points | 1,000,000 |
| routes | 16 |
| route points | 1,000,000 |

上述为安全上限，不是吞吐承诺。客户端仍应在一次读取中完成一条 Workout，不应按 sample 重新拆成多次请求。

### 7.1 准入范围与上传槽

v3 的单设备上传槽是**当前 API 进程内**的运行时状态：`processUploads=2`、`processDeviceUploads=1`。它不是跨 API instance 的全局租约；多实例部署时每个进程各自计数，不能据此外推全局单设备互斥。持久化的 `sync_request_budgets` 只限制 dataset/device 请求速率，不代表上传槽。

请求的准入顺序固定为：认证 → 持久请求预算 → 进程上传槽 → 读取 body。认证失败、预算拒绝、存储准入失败或进程槽忙碌都不会读取 body，也不会创建 Import、Raw Facts 或 ACK；客户端可按 `Retry-After` 安全重试。

## 8. 失败语义

| HTTP/code | 含义 |
|---|---|
| 409 `workout_identity_conflict` | 同一 Workout UUID 的核心 Workout 内容不同 |
| 409 `sample_identity_conflict` | 同一 sample UUID 的内容不同 |
| 409 `sample_association_conflict` | sample 已关联到另一个 Workout |
| 409 `series_identity_conflict` | 同一父 sample 的 series 内容不同 |
| 409 `route_identity_conflict` | 同一 route UUID 的内容不同 |
| 409 `workout_deleted` / `sample_deleted` / `route_deleted` | 删除屏障优先；当前上传的终态拒绝，同 UUID 改哈希或 metadata 重试不能复活 |
| 413 `bundle_too_large` | 超过请求/事件/点数限额 |
| 422 `unsupported_payload` | 严格 schema、单位、时间或字段错误 |
| 429 | dataset/device 请求预算或进程上传槽耗尽 |
| 404 `workout_import_not_found` | 指定 bundle 哈希没有成功回执 |

## 9. 显式 Workout 删除

```http
DELETE /v2/sync/workout-bundles/{workoutID}
Content-Type: application/json

{"protocolVersion":3,"datasetID":"...","datasetEpoch":"...","collectorEpoch":"...","workoutID":"..."}
```

删除只在客户端收到该 Workout UUID 的 `HKDeletedObject` 后调用；空读取、权限撤销或网络失败绝不能触发删除。DELETE 和 POST 共用 dataset 行锁 → epoch/collector fencing → 删除屏障的顺序；数据库事务串行化两种请求。

同一事务内：

1. 已有屏障：读取首次删除回执，成功结束事务后返回原回执；不推进 revision、不新增日志/dirty/job。fencing 在重放前检查。
2. 没有屏障：为该 dataset + Workout UUID 写 `entity_tombstones`。未知 Workout 同样建立屏障，不返回 `workout_not_found`；其 `previous_start/previous_end` 为 NULL，不创建分析范围或任务。
3. 已知 Workout：删除前读取并在 tombstone 保存旧时间范围；删除 Workout 和路线、为路线建立屏障。通过 `rai_analysis_dirty` 登记旧范围的 Workout generation/job，现有依赖触发器在本事务使 Training State 失效。
4. 推进 dataset `commit_rev`，写 `change_events` 和原始删除回执。tombstone 的 `(dataset_id, deleted_rev)` 关联日志；日志 `created_at` 保存删除时间，payload 保存有效 `datasetEpoch`、`collectorEpoch` 和 receipt。复用现有 PK/索引/FK；无新增表、迁移或 checksum 变化。
5. 只有 COMMIT 成功才返回 200 删除 ACK；失败时屏障、事实、日志、dirty/job 和回执全部回滚。

```json
{"workoutID":"<uuid>","ingestStatus":"deleted","alreadyDeleted":false,"commitRevision":123}
```

重复 DELETE 返回首次持久化的相同回执，包括原 `commitRevision` 和 `alreadyDeleted`；后者描述首次命令是否遇到既存屏障，不表示每次重试的实时状态。历史内部删除已建立屏障但没有 v3 receipt 时，稳定返回该屏障的 `deleted_rev` 和 `alreadyDeleted=true`。

删除提交后，旧 Workout 分析及 Training State 不再作为 clean current 返回（`analysis_not_ready`）。Worker 按已删除输入移除 Workout 的 current publication 指针，再依现有依赖链重新发布不含该 Workout 的 Training State；删除前已读取输入的 Worker 受 generation CAS 约束，不能重新发布旧结果。保留历史 run/result，`analysis_publications` 继续采用既有可变 current 指针语义。

客户端仅在 200 且 Workout ID、`ingestStatus=deleted`、正数 `commitRevision`、布尔 `alreadyDeleted` 均匹配后确认本地删除并释放 bundle；404、5xx 或无效 ACK 均不能完成删除确认。删除响应丢失时重试同一个 DELETE；GET 上传回执的 404 不能证明删除屏障已持久化。上传的 5xx 仍先按原 bundle 哈希 GET 回执，再决定是否重试同一文件。

## 10. Phase 2 字段存在性合同与恢复（2026-11-01）

本节补齐 v3 已声明的严格解码行为；不增加 wire 字段、不改变 exact-byte hash，也不新增 schema 版本。实际版本字段为 `protocolVersion=3`，没有 `schema_version`。已有持久 Outbox 不重新序列化。可选指标继续允许省略；设备和 sourceRevision 不成为必填，RQ-007 不在本阶段。

以下矩阵中所有“必填”均拒绝 missing/null/错误类型；所有“可选”均以省略表示无数据，显式 null 拒绝。字段存在且为零与缺失严格区分。缺少可选对象时不生成默认对象或健康值。

| 对象 | 字段 | 合同、零值与省略语义 |
|---|---|---|
| Bundle | protocolVersion | 必填整数，恰为 3 |
| Bundle | datasetID / datasetEpoch / collectorEpoch / workoutID | 必填规范小写非零 UUID；Workout 身份必须一致 |
| Bundle | workout / samples | 必填对象/数组；samples 允许空数组；无样本不生成默认值 |
| Bundle | series / routes | 可选数组，省略等价无此包新增子事实；绝不删除已存事实 |
| Bundle | completeSampleTypes / completeRoutes | 可选数组/布尔；省略为无覆盖声明，false 合法；不授权删除 |
| Workout / Sample Event | op / kind / type / uuid | 必填；upsert、workout/sample、支持的 HealthKit type、规范非零 UUID |
| Workout / Sample Event | startUs / endUs | 必填 int64 Unix 微秒，允许 epoch 0；范围 year 1–9999，start ≤ end；不静默修正 |
| Workout / Sample Event / Route | source.bundleID | 必填 1–255 字节字符串；没有额外设备必填项 |
| Event / Activity / Workout Event | metadata | 可选对象，原有 JSON 内容保留，递归拒绝 null；省略不合成 provenance |
| Workout Event | workout | 必填结构；quantity/category 不允许与 Workout 混用 |
| Workout payload | activityType / durationUs | 必填；activityType 1…int32 max，durationUs ≥0 且不超过原始时间范围，0 时长合法 |
| Workout payload | totalDistance / totalEnergyBurned | 可选 Quantity；存在时 value/unit 必填；非负有限数，单位 m/kcal；0 合法 |
| Workout payload | statistics / events / activities | 可选数组；沿用 128/256/128 及总 detail budget |
| Activity | uuid / startUs / durationUs / configuration | 必填；时间位于父范围，非负 duration；duration=0 合法 |
| Activity | endUs | 可选；无值保持缺失，仅以父 end 作验证上界 |
| Configuration | activityType / locationType / swimmingLocationType | 必填整数；activityType ≥1，另两个原始枚举允许 0 |
| Configuration | lapLength | 可选 Quantity；存在时非负 m，0 合法 |
| Statistic | type / startUs / endUs | 必填；受支持 quantity type，排序唯一，父范围内 |
| Statistic | sum / average / minimum / maximum | 可选 Quantity，至少一个存在；不填补缺席聚合 |
| Workout Event 子对象 | type / startUs / endUs | 必填；type ≥1，父 Workout 范围内 |
| Quantity Sample | quantity.value / quantity.unit | quantity 对象和值/单位必填；有限浮点，0 合法，单位严格匹配 Units 注册表 |
| Category Sample | category | 必填整数 0…5；不允许伪造 quantity |
| Series | sampleType / sampleUUID / points | 必填；必须引用本包 Samples 父对象；points 非空 |
| Series Point | startUs / endUs / value / unit | 全必填；0 数值合法，单位匹配父 type，时间包含于父 sample |
| Route | routeID / source / points | 必填；UUID，非空 points；序列由原数组顺序生成 |
| Route Point | recordedAtUs / latitude / longitude | 全必填；微秒时间范围同上，纬度 [-90,90]，经度 [-180,180]；0、0,0 合法 |
| Route Point | altitude / horizontalAccuracy / verticalAccuracy / speed / course | 可选有限数；altitude 可负，其余 ≥0，course <360；0 合法 |

Raw scalar/series/statistic quantity 沿用 v3 的“有限源数值 + 精确单位”合同：心率/步频 count/min、功率 W、距离 m 等各按 Units 保存，不统一施加 `>0`，不以临床合理性筛掉源值；例如恢复心率差可能有符号。Workout 总距离/总能量/lapLength 的非负语义与原校验保持一致。NaN、Inf、JSON 类型错误和溢出拒绝。Sample 仅检查自身 start/end，不新加“必须完全位于 Workout 内”的条件；Series 检查父 Sample 边界，沿用旧合法请求语义。GPS 时间回跳规则保持原校验，RQ-013 不在本阶段。

错误继续使用既有扁平 DTO，例如 `{"code":"unsupported_payload","field":"routes[0].points[0].latitude","reason":"required"}`，HTTP 422 为确定性错误，保留本地源数据待处理。`field` 使用零基下标，文档级为 `$`；`reason` 为 required、null_not_allowed、invalid_type、invalid_json_or_schema、unsupported_unit、invalid_time_range、out_of_range 等稳定类别。不含健康值。HTTP 入库前完成校验；数据库事务保持全有或全无。

### ACK、重试和本地生命周期

- POST/GET 上传 ACK 只接受 **200 + application/json**（允许 charset）+ 完整 JSON；必须校验当前 Workout UUID、精确 bundleSHA256、`ingestStatus=committed`、**commitRevision >0**、所有 count 的布尔/非负整数类型、必填 job 数组和 queued/not_required 状态。queued 有有效 job UUID，not_required 空数组。删除计数必须为 0（Phase 1）。v3 没有额外 receiptID、datasetID 响应；身份为绑定的 dataset 上下文 + Workout UUID + bundleSHA256。
- 首次上传必然创建 Workout 或引用已存在的正 revision；因此正 revision 收紧不会拒绝服务端曾合法生成的回执。历史合法 v3 客户端编码不变；缺少 quantity.value/坐标的历史损坏请求现在明确拒绝，绝不默认 0。原 OpenAPI minimum=0 是允许不可能回执的遗漏，现修正为 1。
- 网络/超时/408/5xx/异常 ACK：保留原始 multipart 和哈希，可按相同 sha256 GET 恢复。GET 404 **仅为当前查询未找到**，不等于删除，也不终止上传；按持久退避重 POST 相同文件。状态成功也必须验证同一 ACK。
- 429 遵循 Retry-After 秒数或 HTTP-date，且不早于指数退避；401 在提供凭据刷新回调时仅刷新一次，否则保留为 authentication_required 待退避/现有配置更新（当前静态设备 token 没有 refresh endpoint）。403、400/422、身份冲突进入 FAILED 待处理并保留文件；409 workout_deleted 是删除冲突终态，不是 Synced，也不是本地 Deleted。
- 复用 WorkoutSyncTask：PREPARING → UPLOADING（包含待重试）→ SERVER_ACKNOWLEDGED；不增加第二套队列。可选持久 attemptCount/nextAttemptAt/lastUploadError/uploadReceipt/deletionReceipt 兼容旧本地状态。指数退避 1、2、4…秒，封顶 900 秒，服务端 Retry-After 可超过上限；请求前写 60 秒恢复时间，崩溃不会永久卡在 Uploading 或重启立即重传。Store actor 共享 claim 防双 Worker；当前单 App 进程共用唯一 Store。前台调度到期恢复，后台仍遵循 iOS 调度；不会创建新 HealthKit 工作。
- 只有验证成功后，**同一次 state.json 原子替换**持久化原回执、acknowledged 状态及回收标记，随后单独幂等清理文件。状态写失败不能清理文件；清理失败保留回执和回收队列，重启续清理；重复 ACK 幂等。没有证明的旧 acknowledged 文件重新核对，显式 rescan 也不能替换待确认原包。
- DELETE 仍独立校验 200、UUID、deleted、正 revision、布尔 alreadyDeleted；删除回执与本地 Deleted/回收授权同次持久化。上传 ACK 与删除 ACK 不能互换。GET 404 仍不能确认删除。

## 11. Phase 3 可选观察与显式子删除（2026-11-01）

协议仍为3，schema升为16；00014/00015不修改，00016只以 `CREATE OR REPLACE`
修正 route append/projection 函数对 year-9999 末微秒的 PostgreSQL 舍入，
不新增 wire 字段或事实表。握手新增可选能力
`bundleObservationVersion=1`、`explicitChildDeletions=true`；新客户端只向声明
这两项能力的服务端生成新结构。旧合法 bundle/原 Outbox 字节继续接受并重试；
缺少 provenance 明示 `sourceClassification=source_unknown`，不推断 Apple Watch。
这样的 v3 导入不会进入正式分析：Worker 明确失败 `analysis_source_unclassified`，
原始事实与回执保留，后续合法来源观察可以使其 dirty 并重试。旧 v2 分析夹具和
算法本身不改变；不自动迁移或清除历史。

新增可选顶层字段：

```json
{
  "sampleDeletions": [{"sampleType":"HKQuantityTypeIdentifierHeartRate","sampleUUID":"<uuid>"}],
  "routeDeletions": ["<route uuid>"],
  "observation": {
    "version": 1,
    "observedAtUs": 0,
    "metadata": {"HKIndoorWorkout": false},
    "provenance": {
      "workout": {"bundleID":"com.apple.health","version":"<source version>","productType":"Watch7,1"},
      "samples": [],
      "routes": []
    },
    "coverage": {
      "HKQuantityTypeIdentifierRunningPower": {"state":"no_records_observed","sampleCount":0,"queryCompleted":true,"localPersisted":true}
    }
  }
}
```

示例中的 samples/routes provenance 数组只适用于 bundle 无新增子事实。
每个新增 Sample 必须有独立 `{sampleType,sampleUUID,source}`；每个 Route 必须有
`{routeID,source}`，恰与本包成员一一对应。不能借父 Workout 伪造子事实的 device。

### 来源与 metadata

`source` allowlist：必填 bundleID，可选 name/version/productType/
operatingSystemVersion、device；device 只允许 manufacturer/model/hardwareVersion/
firmwareVersion/softwareVersion。字段来自 HKSource/HKSourceRevision/HKDevice，
缺席省略，没有默认值。设备名（可能含个人姓名）、localIdentifier、UDI、序列号
不采集上传；不建立伪造的 Watch registry 记录。现有 devices 是采集 iPhone 身份，
与源 Watch 描述分开。相同描述复用相同 JSON 语义，不创建重复设备注册行。

RAI 的来源判定是独立政策：原生写入者 allowlist `com.apple.health`、
`com.apple.Workout`，以及严格 UUID 后缀的 `com.apple.health.<UUID>` 设备来源；同时有真实 `productType` 匹配 `Watch数字,数字`，才为
apple_watch；非 Apple writer 为 third_party，其余 source_unknown。若 device
明确给出非 Apple manufacturer 或非 Watch model，不通过。设备命名空间额外要求真实 Apple manufacturer/Watch model，缺席时只能依明确父关联判定；不凭命名空间前缀单独认定。名称从不提供判定证明。明确关联的子事实
可在自身 bundleID/version 与已确认 Watch Workout 相同的情况下判为
associated_apple_watch；自身缺失的 device/productType 保持缺失，关联由 bundle
成员关系和本地 HK workout predicate 证明。新客户端将未知/第三方 Workout 留在
耐久待处理日志；不合格子数据保留日志、标记 partial，不混入上传/分析。服务端重算
上述规则，带 observation 的不合格新包返回409 source_policy_pending。

新路径 metadata 只允许明确映射的 HKTimeZone、HKIndoorWorkout、HKAverageMETs、
HKElevationAscended/Descended、HKWeatherHumidity/Temperature、HKAverageSpeed、
HKMaximumSpeed；不允许任意第三方 scalar metadata。未知 metadata 保留安全本地
对象归档并要求审查，不能静默丢弃后声称完整。原冻结 Workout payload/hash 不更新；
客户端原 Workout 事件重用，当前 metadata 放 observation。若当前核心字段发生
非 metadata 变化，客户端明确待审查；服务端仍拒绝核心身份冲突。

Sample 的 allowlist 另含原 SDK key：`HKMetadataKeyHeartRateMotionContext`、
`HKMetadataKeyHeartRateSensorLocation`、`HKMetadataKeyUserMotionContext`、
`HKMetadataKeyDevicePlacementSide`（当前接受 0…10 的整数枚举）。带 observation v1
的同 UUID Sample 允许 metadata 补充：原 health_samples 行、content_sha256 和旧回执
保持不变，新增 metadata 留在该次不可变 bundle 历史中，并参与观察语义及 P4 lineage。
数值（含浮点位模式）、时间、单位、category、类型、UUID 或 source bundle 改变仍为
sample_identity_conflict；不带 observation 的旧客户端保留原严格身份规则。

正式分析还逐项核验服务器保留的 Sample 与 sealed Route 的来源证据，不能用新 Workout
来源替历史子事实补造来源。允许复用同 dataset/Workout 的历史 observation 中匹配
类型、UUID 和原 source bundle 的合格证据；缺失时返回 analysis_source_unclassified，
保留 raw facts，不输出伪造的分析结果。

### Coverage 与历史

observation.version 固定1；observedAtUs 是 **RAI 观察时间**，不是伪装的 HealthKit
时间。provenance 是 HealthKit 原描述，classification 是 RAI 政策，coverage 是
本次查询/耐久处理范围的观察。每个 coverage item 必填 state/sampleCount/
queryCompleted/localPersisted；可选 expectedCount/errorCode/diagnostics。

| state | 语义 |
|---|---|
| not_queried | 尚未执行 |
| observed | 已看到数据，未完成处理 |
| no_records_observed | 查询成功但未观察到记录；不证明拒绝授权或绝对不存在 |
| partial | 读取/来源/series/路线范围尚未完整 |
| complete | 已在定义的可见查询范围结束并耐久保存；不证明全 HealthKit 授权/全历史 |
| failed | 查询或处理失败，错误码不含健康值 |
| unsupported | 当前产品没有支持此类型 |
| unknown | 证据不足 |

客户端兼容解码旧 available/unavailable/error；新 observation 不发送旧枚举。
complete 必须非零计数、queryCompleted/localPersisted 都为 true，若声明 expectedCount
则必须相等。空记录的成功查询使用 no_records_observed。未查询、无记录、失败、
本地持久化、封包、serverCommitted、Route seal、P4 publication 都独立。
本地 serverCommitted 只有严格 ACK 持久化后设置，不由请求 self-assert。

每个 bundle 的 observation 原样追加在 workout_imports.payload，PK 为
(dataset_id,workout_uuid,bundle_sha256)，复用现有索引。相同字节返回同一原回执。
新 observation 不覆盖旧版本；读取最新按 observedAtUs DESC、bundle_sha256 DESC，
旧观察迟到不会覆盖新观察。相同观察内容仅观察时间不同不生成额外 dirty/job。
不同来源版本、设备描述、metadata 或覆盖改变会推进 dirty/job；观察语义摘要进入
P4 snapshot lineage/input hash，不修改 raw。ACK/按hash GET 回执可带有
sourceClassification、sourceObservation（版本、时间、摘要、Workout source、metadata、
coverage）和 routeDiagnostics；它们不替代原 ACK 必填字段。完整子 provenance 保存在
该次源 JSON，避免将百万量级源描述回显到受限 ACK。

### 显式子删除事务与 ACK

sampleDeletions 和 routeDeletions 仅从真实 HKDeletedObject + 持久父索引产生；
允许空/省略，永不由快照缺席、空查询或 coverage 产生。相同身份在一包中不能同时
upsert/delete，不能重复。已知属于另一个 Workout 的子 UUID 返回409
child_workout_mismatch，事务不变。未知子 UUID 可建屏障，但客户端未知父关系先留
unresolved 日志，不猜测。Sample 删除类型和 UUID 均必填；Route 删除不会删除父。

dataset 锁和 fencing 后，所有显式屏障、实际删除/关联级联、Route 删除函数、
change log、P4 dirty/jobs 和不可变 import receipt 在同一事务提交。重复事件只确认
原屏障；原 bundle 精确重放返回首次回执。相同字段的普通快照缺席仍无任何删除效果。
ACK 删除计数是实际删除行数，未知屏障计数可以为0；客户端只接受不超过本包明确
删除数量的计数。旧无删除包仍要求三个删除计数为0。Workout DELETE ACK 独立校验。

### Route 与时间

HealthKit 路线枚举用16+1识别超限，每条 Route 比较 HKWorkoutRoute.count 和实收点数；
成功耗尽 async descriptor 才是查询完成，零点/缺点/错误/取消均不能声称 complete。
先登记受保护的 append-only Outbox capture，逐点持久化；进程恢复重新读取同一
Route，逐点核对保存前缀，匹配后续写，绝不覆盖旧点。未完成 capture 的声明点数
增长可以在前缀核验后继续；已完成 Route 不接受身份内容替换。损坏尾行/前缀不同
明确待处理，保留文件。只将完整 Route 放入原单 Workout multipart；部分 Route 保留
本地恢复，其他完整路线/Workout 不被一个失败项阻塞。

内部继续5000点/chunk、连续 sequence、双 hash chain 和原 rai_route_append_chunk /
rai_route_seal；客户端不新增独立 Route HTTP。各上限保持原值，超限明确 partial/
bundle_too_large，不能截断后 seal。原数组序号就是点序，不按 recordedAtUs 排序，
不去重、不插值或改写坐标/时间。相同时间、局部/连续回退、跨 chunk 回退、Workout
范围外的合法年份时间均保留。manifest 时间包络取实际 min/max，内部列名仍为
first_recorded_at/last_recorded_at；点的 recordedAtUs 不变。

诊断 timestamp_equal/timestamp_regression/before_workout/after_workout 独立于
原始传输完整性，不发明阈值。非法 year1–9999 之外时间、非有限/越界坐标等仍拒绝。
P4 workout.features/v2 已有跳过不递增连接的规则，继续按原 sequence 消费；范围外点
可使分析明确 analysis_invalid_input，raw/sealed Route 仍保留，不假装分析完成。

### 本地发现和恢复

全局 Workout 查询 nil predicate /25条；每个已发现 Workout 的10种 Quantity 类型和
Workout Route 用 `predicateForObjects(from: workout)`，分别独立 anchor（1000/1条）。
单UUID Workout 流继续捕获删除。没有可靠反向关联 API，所以不靠时间匹配。
当前 Workout 产品不采集 Category；明确 unsupported，不把服务端支持睡眠误称
客户端已启用。首次范围沿用ADR-002当前可见历史，没有擅设截断日期；后续绝不重置nil。

发现档案先保存安全 HKSample/HKDeletedObject 归档和SHA256，再以同一 state.json
原子替换写入发现日志、父索引、待处理代际和 candidate batch，旧 anchor 不动；第二次
提交核验归档仍存在/摘要匹配后才提升 anchor。下一页必须等待提升成功。任一失败
可以重放；UUID/类型/操作/内容摘要去重。启动先提升已耐久 batch，上传与 anchor 坐标
独立。查询错误/取消不推进；损坏 anchor 隔离，不静默清空；显式受控 rediscovery
创建新命名空间并保留旧 anchor/Outbox/历史。Observer 仅唤醒有界查询，延迟或丢通知由
前台轮转查询补偿，不能证明实时后台执行。

后续变化保留一个 Workout 的稳定任务身份，只有已选择任务自动补传。真实未知
Workout DELETE 也可创建服务端屏障。未确认旧包遇到明确子删除时可停放到耐久
retainedBundles，原文件仍引用、禁止GC；新删除包不复活旧事实。一般失败仍只重试原包。
ACK 不完整数据不清理发现待办，按持久退避重新读取；无记录结果不伪装完整或删除。

### 父 Workout 删除后的迟到子删除

子类型查询可以使用安全归档的原 HKWorkout 保持原关联谓词；当前 Workout 不可见
不能导致这些 anchor 失效或被丢弃。父 DELETE 的 ACK 只完成父删除，不能消费仍待提交
的 Sample/Route 删除意图。新可选 `deletionOnly=true` bundle 只用于已有父 tombstone 后
的子删除：必须有非空显式删除，无 samples/series/routes/observation；保留合法原 Workout
事件作协议上下文，服务端绝不插入/更新父事实，不按该事件创建分析范围。没有父屏障时
拒绝 deletion_only_requires_workout_barrier。普通旧 upsert 仍返回 workout_deleted。

原 workout_imports 有父 FK，父删除按原合同级联删除该历史，不能为补回执而复活父。
因此这种删除包使用现有 change_events：RAI 根据 dataset/workout/bundle hash 派生唯一
命令 event_id（不冒充 HK UUID），使用现有 UNIQUE event_id 索引恢复首次回执；payload
保留显式删除、完整 bundle hash、回执对象和原回执字节。精确重试不推进 revision。
首次新命令与实际子屏障/删除同事务写新 revision；无父范围、不伪造 P4 工作。
按 hash GET 可恢复该原回执；父不存在且省略 hash 时仍可能404。不重新保存已删除的父
原始载荷。客户端保持 DELETED 投影，同时持久保存父 DELETE ACK 与新 POST ACK；
前者不能释放后者未确认的原包。无新增 migration，原FK/历史checksum不修改。

补充实现证据：Quantity series descriptor 必须显式 `.includeSample`；默认 options=[]
不会提供父 Sample，不能继续忽略 sample=nil。新路径先将每个实际 series point 写入
已被 Outbox 引用的保护文件，再核对父身份与声明数量；错误/取消保留采集文件。完整
sample 查询超出已捕获 anchor 页范围时也先补存原对象档案，不靠内存承担恢复。


## 12. Phase 4 传输与 SQL 预算（2026-11-01）

协议仍为3；schema17新增一个覆盖样本身份索引。00001–00016内容不修改。

| 阶段 | 有限预算 |
|---|---:|
| TLS/header | 服务端 ReadHeaderTimeout 5s；客户端连接由 request/resource 预算涵盖 |
| 上传 body | 180s 绝对读 deadline，包含 multipart 读取与有界解压，不是可无限续期的 idle timeout |
| 解压 | 5s，逐块检查；CRC/ISIZE 必须读至 EOF；拒绝串联成员与尾随垃圾 |
| JSON decode | 15s 检测；32 MiB + lexical domain count 限额；超时不进入 Import |
| domain validation | 5s 检测；不降低严格字段/必填/重复/null 检查 |
| body 后 processing context | 60s |
| Import | context/PG18 transaction_timeout 45s，statement_timeout 5s，lock_timeout 500ms |
| ACK write | COMMIT 后5s独立 write deadline；失败留原回执可 GET/重 POST |
| UploadLane handler | 245s；server fallback Read185s/Write255s，路由按阶段重设 |
| API fast SQL | statement2s；fast handler20s |
| Worker snapshot | statement5s / transaction20s / context20s |
| Worker publication | statement5s / transaction10s / context10s |
| iOS POST request / foreground resource | 270s / 300s |
| Background URLSession | request270s / resource3600s（含系统排程，单个服务器请求仍受245s约束） |

严格词法边界同时限制 JSON 深度64、每个object字段256；数组/总点数在typed分配前检查，领域验证仍保留。

JSON decode/validation 为同步 CPU 过程，时间预算在阶段结束检测；并不声称能在任意指令中抢占。
网络读取超时为408 upload_read_timeout；context超时为503 request_timeout；解压/解码/校验预算超时为503，保持可重试。
400 body_read_failed用于损坏multipart/非超时读失败，413用于有限资源超限，422 invalid_gzip用于损坏CRC/header/截断/串联gzip。
客户端408/429/5xx/网络超时继续 Phase2退避，GET404不能丢弃原包。

Outbox使用原有单一队列：原JSON multipart为权威文件；gzip multipart和其wire SHA256为可重建派生文件。
先持久保存原包再压缩，压缩失败保留原包；重启验证原JSON hash及wire hash。
派生文件损坏从原包重建，不重新读取HealthKit；大于8MiB的gzip可使用受32MiB上限保护的plain兼容路径。
有效ACK或显式删除ACK与两种文件的GC授权同一次state提交；失败或未确认时均保留引用，停放旧包时两种引用一起保留。
重试使用同一boundary和已持久化gzip；gzip固定level6、mtime0、OS255，无随机字段。

`workout_bundle_transport`记录wire/file/decoded bytes、multipart/decompression/decode/validation/import/ACK分阶段耗时及停止阶段；
`workout_import_phase`记录samples/associations、route projection、dirty/job、原包回执commit及transaction耗时；
Worker记录snapshot/algorithm/publication耗时。均不记录健康值、坐标、token或SQL参数。
反向代理应允许至少255s upstream response和180s body；fnOS/公网/TLS代理未实测不能由本地测试外推。


## Phase 5A — New Dataset + New Session（2026-11-01）

冻结产品决策：`Dataset=new / Training History=discard / Old RAI Data=no_migrate / Disaster Recovery=new_session`。schema 18，protocolVersion 仍为 3，OpenAPI 1.5.0。00001–00017 不修改。

Dataset 创建仅由 owner/operator CLI `rai admin new-dataset` 执行，无公开 HTTP 创建入口、无设备自助创建。新 Dataset ID、Dataset Epoch、Device ID、32-byte CSPRNG token、Collector Epoch 全新，commit_rev 默认 0；所有事实、训练、tombstone、receipt/import 初始为空。不复制服务器旧空间、旧 RAI SQLite、旧 Running、XML 或训练生成历史。

命令要求 source ID、同值 confirm-source、`NEW-DATASET` 确认短语、operation ID、actor 和绝对 credential-file。先原子输出一次 0600 凭证文件（应保存在仓库和 evidence 外），再在一个事务中锁 source、创建目标/设备/collector、退役 source 并写 committed 审计。数据库只保存 token hash；stdout 只有公开身份，token 为空。相同 operation 重试不创建第三个 Dataset，source/actor 不同则冲突。凭证准备后失败保留文件并用同 operation/file 恢复。提交后 stdout 丢失仍可从原文件绑定 App；文件丢失不能恢复 token 明文，不得重新生成并伪称原 token。可读取幂等目标身份，后续凭证重新签发不在本阶段范围。

`--dry-run` 使用 REPEATABLE READ READ ONLY，只输出 source 存在/状态、workout/sample/route/pending-job/device 数量和 collector 是否存在，以及“将由服务端生成全新身份”的计划；不产生目标 UUID、token、审计或任何行。

Dataset 身份核验后检查退役，再检查 collector fencing。retired 写返回稳定 `409 {"code":"dataset_retired"}`；已移除的旧写协议遇到有效 retired token 同样拒绝。活动 Dataset 的已移除协议仍不可用。事务内再次检查 Dataset 锁，覆盖 auth 后并发退役；原凭证不撤销，仍可读原历史。退役后不得继续事实/删除/训练/分析发布，旧 job 保留暂停。跨 Dataset 仍为 `dataset_forbidden`，Epoch 不符为 `dataset_epoch_changed`，collector 不符为 `collector_fenced`。

握手增加 `deviceID / collectorEpoch / datasetStatus`。reader 无 collector 时 collectorEpoch 为空；新 Session 激活必须核对非空完整主采集身份、协议/能力及 active 状态。Session Descriptor 不含 token；每 Session Keychain account、Outbox、gzip wire、receipt、discovery、deletedWorkouts/tombstones/recoveryOverrides 全部独立。只有 active Session 可以 discovery/upload。旧 Bundle 即使被误放到新目录，发送前核对 bundle Dataset/Epoch/Collector；服务端还按 bearer Dataset 和 collector 拒绝旧包。

ACK wire 结构保持不变，没有虚构 datasetID 字段。ACK 的 bundleSHA256 绑定包含 Dataset/Epoch/Collector 的原始 JSON 字节；验证该 hash、握手 scope 和独立 Outbox，回执及 analysis job/publication 都属于新 Dataset。

新 Session 的 nil Anchor 是 **fresh HealthKit import**。旧 tombstone 不复制，故旧服务器已删、但 HealthKit 当前仍可见的 UUID 可以重新导入新空间；重复发现与重试保持幂等。旧 Session 不删除、不覆盖、不移动；预备、Keychain、Outbox 和成功握手之后才原子替换 active pointer。迟到后台 callback 使用自己的 session identifier 与 taskDescription，只能写自己的目录。

## UA1-R3B — Source Policy & Eligible Inventory Contract（2026-11-05）

冻结 `RAI Workout Source Policy V1`（`rai.workout.source_policy.apple_watch_only/v1`）：
只有 Apple Watch native running Workout 是 eligible；明确第三方或用户录入的 Workout 是
`excluded_by_source_policy`；无法安全分类一律 BLOCKED。

新增控制面 `workout-sync-completeness/v2`（`protocolVersion = 2`，`streamSetVersion` 仍为 1），
与 v1 共用 `POST /v2/sync/workout-completeness`，由 body 的 `contract` 选择语义。
v1 语义不原地修改：`workout-sync-completeness/v1` 仍是“每条可见 Workout 都需要 receipt”，
v2 才是“每条可见 Workout 都必须被确定解释为 eligible 或 explicitly excluded”。

Workout Protocol 3 的 wire facts 完全不变；eligible Workout 仍按原方式上传。
excluded Workout 不产生 receipt、不产生 samples/route 上传、不产生分析任务，只产生
不可变 exclusion evidence（identity hash + 分类 + 原因 + 策略版本 + evidence digest + 最小 source evidence）。
服务器重新执行同一冻结策略校验 evidence，不接受 `excluded=true` 之类的自证。

inventory 不变量：`visible = eligible + excluded + unclassified`，且 `unclassified = 0`、
`pendingClassification = 0`；三条 inventory digest 证明成员身份，计数本身不能提交。
M1/M2/M3/M6 只消费 eligible Workout 域；产品文案不得称 all/total training load。

细节见 `contracts/workout-completeness-v2.md` 与 `contracts/workout-source-policy-v1.md`。
