# 本项目同步 vs RQrun HealthKit 上传：对比分析

> 对比对象：
> - **RQrun**：`docs/RQrun-HealthKit-上传协议.md`（2026-10-31 抓包，iOS 3.7.2 build 4，api.rq.run）。
> - **本项目（RAI）**：`contracts/sync-v6.md`（唯一支持的 Workout 同步协议）及其实现在 `apps/server/internal/{syncing,httpapi,storage}`、`ios/RAI/{App,Sources/SyncCore}`。
>
> 说明：RQrun 服务端为闭源黑盒，结论来自明文 HTTP 抓包与字段推断；RAI 的结论来自契约与源码。凡未实测的项目在文中标注。

## 0. 结论摘要

1. **同一家族**：两者都是「App 本地读 HealthKit → 组装原始 JSON → 一次上传一条 Workout → 服务端异步解析」。RAI 在 `contracts/sync-v6.md:1-6` 明确写着只借用 RQrun 的「单遍读取、按类型打包原始序列、一次上传」原则，不复制其字段名与身份规则——这个定位在实现中成立。
2. **定位不同**：RQrun 是消费级产品的记录归档，载荷面向“服务端解析→出分析报告”；RAI 是契约化的同步平台，v6 载荷面向“服务端生成事实→可重放、可审计、可下载”，workout 只是其中一条协议（另有 daily 事实、VO2max、completeness）。
3. **最大分歧在身份与幂等**：RQrun 用客户端给的 `apple_health_workout_id`（=HKWorkout.uuid）做天然去重键，没有服务端幂等证明；RAI 的 Workout UUID 也是幂等键，但**每个样本/分段的身份由服务端确定性生成**（UUIDv5，`syncing/workout_bundle_v6.go:12-14`），首次接收的原始字节即权威副本，重复上传只重放首次回执。
4. **回执语义不同**：RQrun 只有 `{"syscode":200,"sysmsg":"上传成功","data":"<recordID>"}` 一个信封，解析结果要靠客户端再查状态；RAI 的 200 只承诺「原始载荷已持久接收」，状态查询返回 accepted/committed/rejected/deleted/already_imported 及 `acceptedAt`、`commitRevision`、`analysisJobIDs`，并有 409 细分族区分“已接收”“已删除”“缺失接收行”。
5. **压缩与限额**：RQrun 40 分钟跑步明文 641,476 B、不压缩；RAI 客户端默认 gzip（`WorkoutIngestDelivery.gzipWire`），服务端同时接受明文与 gzip，限额定为 32 MiB wire / 32 MiB decoded / 8 MiB gzip / 5 万条目 / 100 万 series 点。
6. **RQrun 有而 RAI 没有的体验**：逐指标读取重试 3 次后仍上传（把失败写进 `get_*_auth`、`get_data_error` 诊断字段），以及上传前用服务端状态列表跳过已成功 UUID；RAI 客户端对读取失败采取“整条不上传”，重试交给用户再次点同步，但单条 Workout 失败不会中断同批其它条目。
7. **RAI 有而 RQrun 没有的机制**：严格协议校验与 422 错误族、来源策略门禁、确定性事实身份、事务内投影与删除屏障、提交日志/变更页、租约-围栏-幂等的独立分析作业。
8. 抽查发现若干**契约文档与实现不一致**（见 §15），均不阻塞当前使用，但会误导第三方接入方。

## 1. 总览对比表

| 维度 | RQrun | RAI v6 |
|---|---|---|
| 触发 | 用户在 `HealthUploadSelectVC` 勾选记录后由 App 上传（前台） | 用户显式点“同步”，前台逐条上传；启动/回前台/健康数据变化都不自动上传（`contracts/sync-v6.md:10-13`） |
| 粒度 | 1 条 Workout = 1 次 multipart 上传 | 1 条 Workout = 1 次 POST `/v2/sync/workout-ingests/{workoutID}` |
| 载荷 | 顶层 40 字段 + 扁平数组 + 嵌套对象，字段名产品化（含中文诊断） | `{protocolVersion,datasetID,…,source,metadata?,workout,streams[],routes[]}`，类型化单位、相对微秒 |
| 编码 | UTF-8 明文 JSON，`text/plain` | UTF-8 JSON 或 gzip（`application/gzip`），默认 gzip |
| 身份 | 客户端给 `apple_health_workout_id`；样本无 ID | Workout UUID + 服务端对每个事实生成 UUIDv5；线上无样本 ID |
| 幂等 | 客户端侧去重（依赖状态列表），服务端未观测到幂等保证 | 服务端唯一键 (dataset,workout_uuid)，重复接收重放首次 `acceptedAt`，不增 revision |
| 回执 | `{"syscode":200,"sysmsg":…,"data":recordID}` | 200 + JSON 回执（`acceptedAt` 等）；错误 403/409/413/422/429 细分 |
| 异步可见性 | 另查 `code_apple_source_status` 列表 | GET 同一路径查单条状态；不轮询；分析作业单独查询 |
| 校验 | 抓包未见 Schema 校验（服务端行为未知） | 指针字段判缺失、严格版本/必填/来源策略/单位；未知字段保留不报错；坏点跳过并记诊断 |
| 删除 | 未见删除同步 | 客户端不传删除；服务端 tombstone/屏障，已删除 UUID 不可复活 |
| 认证 | `Authorization:<应用级固定密钥>` + `X-USER-TOKEN`；token 还出现在 URL | 每设备 Bearer token（长度 50..256），无 URL token；Bearer 头校验在 `server.go:119-123` |
| 压缩 | 不压缩；文档建议 gzip 可省 75–85% | 客户端默认 gzip；服务端两种都收 |
| 重试 | 读取阶段逐指标重试 3 次；上传失败形态未观测 | 不自动重试；每条 Workout 独立结果码，同批继续 |
| 体积证据 | 40 分钟 641,476 B（实测） | 未实测（仓库 `docs/evidence/workout-bundle-2026-10-31.json` 不是真实载荷）；仅有契约上限 |
| 服务端 | 解析→分析（`status:1`），细节不可见 | 原始载荷落库 → worker 投影为事实（同事务：来源分类/冲突/屏障/变更日志/分析待办/回执）→ 分析作业 |
| 范围 | 只有 Workout 上传 | Workout + daily 事实 + VO2max + completeness，多协议 |

## 2. 触发与调度

**RQrun**：抓包显示上传发生在“健康同步选择”页，说明是用户主动挑选记录，不是后台静默同步。客户端读 HKWorkout → 读全部关联样本（心率/功率/步频/步幅/触地时间/垂直振幅/轨迹）→ 组装 JSON → 上传；文档第 9 节描述为 PromiseKit 串联的单条上传链。是否存在上传队列、断点续传未见证据。

**RAI**：`contracts/sync-v6.md:10-13,299-304` 规定每轮同步先 GET `/v2/handshake` 一次，然后逐条 Workout「读取→组装→发送→记录本轮回执」；没有 Outbox、没有后台 URLSession、没有自动重试、没有分析轮询。`SyncCoordinator.swift:34-53` 的注释与此一致：本路径无本地 Workout 上传队列、无状态预读、无自动重试、无后台传输、无分析轮询。

**差异**：两者都是前台手动，但 RQrun 的上传单元是“用户勾选的一次批量”，RAI 的上传单元是“一条 Workout 一次 POST”，且 RAI 把“本轮”显式建模为取消边界（离开页面/进后台取消本轮，`sync-v6.md:304`）。

## 3. 上传粒度与线格式

**RQrun 载荷形态**（`docs/RQrun-HealthKit-上传协议.md` 第 6–7 节）：

- 40 个顶层字段，扁平+嵌套混合：`map_position_list[]`、`heart_rates_list[]`、`time_distance[]`、`power_list[]`、`cadences_list[]`、`stride_length_list[]`、`contact_time_list[]`、`vertical_list[]`、`activities_list[]`、`workout_evnet_list[]`（原文拼写）、`apple_workout_plan`、`get_*_auth`、`get_data_error`。
- 时间用 Unix 秒/浮点秒，距离用米、功率用瓦、步频用步/分，单位隐含在字段名里。
- 40 分钟跑步的数组规模：轨迹 2283、心率 456、功率 887、步频 887、time_distance 884、步幅 423、触地 428、垂直 432、事件 11、活动 1——基本是“逐样本一条”的平铺，`time_distance` 还重复记录了累计距离。

**RAI v6 载荷形态**（`contracts/sync-v6.md:84-204`）：

- 顶层固定结构：`protocolVersion:6`、dataset 三元组（datasetID/datasetEpoch/collectorEpoch）、`workoutID`、`startUs/endUs`（UTC 微秒）、`source`、可选 `metadata`、`workout`、`streams[]`、`routes[]`。
- stream 分两种：标量 `{type,unit?,status?,error?,samples:[[startDeltaUs,endDeltaUs,value]]}`；多值序列 `{type,unit,samples:[],series:[{startDeltaUs,endDeltaUs,value,points:[[s,e,v]]}]}`，组头是 HealthKit 多值 quantity 父样本自身的值。
- routes 无 routeID：`points:[[recordedAtDeltaUs,lat,lon,alt?,hAcc?,vAcc?,speed?,course?]]`，身份由服务端按数组下标生成。
- 相同输入必须得相同字节（`sync-v6.md:326-330`；客户端 `RawStreamSyncEngine.build` 对 streams 按 type 排序、samples 按 start/end/value 排序）。

**核心差异**：RQrun 是“UI/产品语义优先”的平铺记录（含天气、训练计划、授权状态、重试诊断），偏向服务端出报告；RAI 是“存储语义优先”的类型化序列（显式单位、相对时间、无重复派生值、无产品文案），偏向服务端生成可计算事实。

## 4. 字段对照（RQrun → RAI）

| RQrun 字段 | RAI v6 对应 | 说明 |
|---|---|---|
| `apple_health_workout_id` | `workoutID` | 同为 HKWorkout.uuid 语义 |
| `apple_record_source_bundleIdentifier/source_name` | `source.bundleID/source.name` | RAI 还要求过来源策略 `apple_watch_only` |
| `apple_watch_device{}` | `source{version,productType,operatingSystemVersion,device}` | RAI 字段更少但规范化 |
| `app_version` / `ios_version` | `source.version` / `source.operatingSystemVersion` | 一致 |
| `start_time/end_time` | `startUs/endUs` | 秒 → 微秒 |
| `total_time` | `workout.durationUs` | 秒 → 微秒 |
| `total_distance` | `workout.totalDistance{value,unit}` | 米 → 带单位 |
| `total_calories` / `total_ascent` / `avg_heart_rate` | **无直接对应** | RQrun 上传聚合值；RAI 不传，依赖样本事实/分析推导（未在契约中承诺等价值） |
| `map_position_list` | `routes[].points` | 无 routeID，按下标 |
| `heart_rates_list` | 标量 stream `HeartRate` | RQrun 每条带 start/end；RAI 用相对时间三元组 |
| `power_list` / `cadences_list` / `vertical_list` / `contact_time_list` / `stride_length_list` | series 或标量 stream（Running Power/步频/垂直振幅/触地时间/步幅） | RAI 的 RunningSpeed 保持标量，其余四类走 series 组 |
| `time_distance`（累计距离序列） | 无对应 | RAI 用带时间戳距离样本，不重复累计值；累计由消费端计算 |
| `activities_list` / `workout_evnet_list` | `workout.activities[]`（activity ordinal 生成身份） | RQrun 事件 type=7；RAI 不强绑定事件类型 |
| `apple_workout_plan`、`weather_temperature/humidity` | `metadata`（可选）或未知字段保留 | RAI 契约不定义这些语义 |
| `get_*_auth` / `get_data_error` | 流级 `status`/`error` + 客户端 outcome 错误码 | RQrun 用中文原因串；RAI 用稳定错误码 |
| `swimming_stroke_count` | 若为已知类型则投影，否则未知字段保留 | RAI 未在 v6 中定义游泳字段 |

## 5. 身份与幂等

**RQrun**：唯一键是 `apple_health_workout_id`（HKWorkout.uuid）。样本没有 ID，服务端如何落库不可见。抓包显示 App 会先查状态列表，把已成功的 UUID 跳过（推断，文档第 8 节），说明去重主要发生在客户端；上传接口本身是否幂等未观测。

**RAI**：

- Workout 唯一键 `(dataset_id, workout_uuid)`；首次成功接收的原始字节是该 UUID 的唯一权威副本（`sync-v6.md:41-56`）。
- 重复接收：`accepted_at` 非空时**原样重放** `ingestAcceptedReceipt`，不占新配额、不增 revision、不替换字节（`apps/server/internal/storage/postgres/workout_ingest.go:80-108`）。
- 导入层（`workout_bundles.go`）另有一层幂等：按 `(dataset, workout, bundle_sha256)` 精确重试先查墓碑、再返原回执（`:112, 205-232`）；同 UUID 异字节返回 409 `identity_conflict`（`:137`）。v6 接收层本身不比对字节，靠客户端“相同输入得相同字节”的确定性保证。
- 事实身份：服务端对每个 stream sample、series 组头、route、activity 生成 UUIDv5，namespace 固定 `c9f3e2a1-7b64-5d8e-9a0b-1c2d3e4f5a6b`，名称形如 `rai/v1|<datasetID>|<workoutID>|<sampleType>|sample|series|<ordinal>`，ordinal 是线上数组下标且**被跳过的条目仍占位**——因此重试字节即使不同，只要顺序一致，事实身份也不漂移。
- 客户端不保存待上传状态、不重传；中断后下次重新从 HealthKit 读并组装，靠服务端唯一键去重（`sync-v6.md:63-66`）。

**评价**：RAI 的幂等是服务端证明的；RQrun 的幂等依赖客户端自觉，若 App 被清除状态或并发上传，服务端行为未知。

## 6. 回执、状态与异步可见性

**RQrun**：

- 上传响应 `{"syscode":200,"sysmsg":"上传成功","data":"113467563"}`，`data` 是服务端记录 ID，不携带解析状态。
- 状态接口 `GET /Record/code_apple_source_status?token=&user_id=`（头 `X-USER-TOKEN` + `Authorization`）返回 `data[]`：`apple_health_workout_id`、`file_ext`（`json`/`wjson`）、`status:1`、`remark`、`analytic_msg`、`created_at`；实测 85 条历史。
- 因此 App 需要“上传 → 轮询/拉列表 → 展示解析失败”的三段式；失败原因以中文串回传，未定义机器可读错误码。

**RAI**（`contracts/sync-v6.md:41-56,63-66`）：

- POST 的 200 只代表“原始载荷已持久接收”，不等待事实投影/分析完成；响应体是回执（`acceptedAt` 等）。
- GET 同路径返回单条状态：`accepted` / `committed` / `rejected` / `deleted` / `already_imported`，以及 `acceptedAt`、`commitRevision`、`analysisJobIDs`、`analysisStatus`、`errorCode`（`ios/RAI/Sources/SyncCore/WorkoutIngestDelivery.swift:4-78`）。
- 状态机（`internal/syncing/workout_ingest.go:60-70`、`storage/postgres/workout_bundles.go:300-367`）：`accepted` → `committed`（带回 `commitRevision`/`analysisStatus`/`jobIDs`）或 `rejected`（带 `error_code`，重试 3 次后为 `processing_attempts_exhausted`）；删除 → `deleted`；已有事实但无接收行 → `already_imported`。
- 客户端在上传流程中**不查 GET、不轮询**（`SyncCoordinator.swift:36-40` 注释），状态查询是显式动作；`GET` 状态与 `AnalysisFollowup` 在客户端**只有定义和测试、没有调用点**（`WorkoutIngestDelivery.swift:21-60`、`WorkoutSync.swift:81`）——即“可编排的回执”目前只是服务端能力，UI 未消费。
- 错误族：403（dataset_forbidden）、409（workout_deleted / collection_fenced / dataset_retired / dataset_epoch_changed / workout_ingest_missing_receipt）、413 bundle_too_large、422（unsupported_payload / invalid_workout / source_policy_* / workout_ingest_mismatch）、429（预算/速率）。

**评价**：RAI 的回执更“可编排”（客户端可以据此显示“已接收、后台分析中、已完成”），RQrun 的回执更“可玩”（只有成功/失败，解析结果另查）。

## 7. 校验、未知字段与降级

- **RQrun**：抓包未见任何 Schema/版本协商；字段是产品字段，缺字段后果不可见；读取失败通过 `get_*_auth`（空串=已授权，否则中文原因）与 `get_data_error`（每指标最多 3 次重试的错误串）随包上传，服务端仍接收整包。
- **RAI**：严格但分层（`sync-v6.md:259-285`）：
  - 硬错误（整包 422）：版本缺失/非 6、dataset 三元组缺失、时间区间非法、source 非法、来源策略排除、workout 核心非法、UTF-8 非法/重复键/嵌套>128。
  - 软跳过（保留原文、不阻断）：未知 stream type、重复类型、单位不匹配、非法样本三元组、空系列组、越界点、非法 route 点、未知顶层字段。
  - 单元测试与解码器实现见 `apps/server/internal/syncing/workout_bundle_v6.go:285-341`（`source_policy_excluded`、`invalid_workout`）。
- **客户端降级**：RAI 的 `RawStreamSyncEngine.readStream` 对坏点标 `status=partial` 并继续，对“多值样本被当标量”这类歧义宁可整条失败（`series_type_not_configured`），避免把聚合值伪装成完整流；RQrun 则倾向于“尽力传完+诊断字段”。
- **授权语义（新增对比）**：RAI 客户端**不读真实授权状态、没有 denied 分支**，唯一门槛是 UserDefaults `healthAuthorizationRequested`（`ios/RAI/App/RAIApp.swift:773`）；降级按“覆盖度”划分为 `no_records_observed` / `partial` / `unsupported`（`ios/RAI/Sources/SyncCore/WorkoutSync.swift:20-61`），调试报告明确写 `visible_only_not_authorization_proof`（`ios/RAI/App/HealthKitClient.swift:189`）。因此 RQrun 的 `get_*_auth`（逐项权限被拒的中文原因）在 RAI **没有对应物**：RAI 用“看不到数据”表达，用户无法区分“拒绝授权”与“确实没有数据”。

**评价**：RAI 的模型适合长期存储与合规审计；RQrun 的模型适合快速上线与人工排障。

## 8. 体积、压缩与限额

| 项目 | RQrun | RAI v6 |
|---|---|---|
| 单次实测 | 40 分钟跑步 641,476 B（明文） | 未实测 |
| 估算 | 1 小时 0.9–1.0 MB，每周 5 次约 5 MB，250 MB/年/人 | 契约上限：wire 32 MiB+64 KiB、decoded JSON 32 MiB、gzip 文件 8 MiB、解压比 100:1+64 KiB、每 stream 5 万条目、series 点 100 万、routes ≤16、route 点 100 万 |
| 压缩 | 不压缩（文档建议 gzip 可省 75–85%） | 客户端默认 gzip（`WorkoutIngestDelivery.swift:93-124`）；服务端 gzip/明文都收 |
| 超限行为 | 未知 | 413 `bundle_too_large`，整包失败不截断 |
| 服务端背压 | 未知 | 上传另取 dataset 槽（2 个、30s 租约），满 → 429 `dataset_upload_capacity_exceeded`（`storage/postgres/uploads.go:67`）；积压 ≥200 条或 512 MiB → 429 `workout_ingest_backlog_exceeded`（`Retry-After: 5`，`syncing/workout_ingest.go:9-18,83-84`、`httpapi/server.go:285-286`） |
| 客户端兜底 | — | gzip 后仍超限时改为**明文 multipart**（`WorkoutIngestDelivery.swift:100-115`）；POST 超时 270s、其他请求 60s、响应上限 1 MiB（`Sources/ProductCore/RAIAPIClient.swift:89,37-40`） |

> 注：仓库中 `docs/evidence/workout-bundle-2026-10-31.json` 只有 3,197 B，是环境验证记录（`streams:[]`、`routes:[]`），**不能**当作 RAI 载荷体积或压缩比证据。本报告不给 RAI 体积实测值。

## 9. 删除

- **RQrun**：未见删除上传或删除同步；状态列表里也没有删除状态。
- **RAI**：
  - v4 客户端不上传删除，服务端不根据快照缺席推断删除（`AGENTS.md`；`contracts/sync-v6.md:287-295`）。
  - 删除优先：root Workout 的 tombstone/屏障/管理删除路径保留；v6 重放不能复活已删除 UUID；服务端在接收事务中先查 `entity_tombstones`，命中直接 409 `workout_deleted`（`postgres/workout_ingest.go`）。
  - v3 客户端仍可逐条删除（HKDeletedObject），每次删除推进 commit revision（`contracts/sync-v3.md:173-180`）。

**评价**：这是 RAI 明显强于 RQrun 的部分——删除与“重传复活”问题在 RAI 有明确语义，RQrun 未建这个模型。

## 10. 认证与安全

- **RQrun**：`Authorization:<应用级固定密钥>`（README 级密钥，随包分发）+ `X-USER-TOKEN:<用户 token>`；部分接口 token 直接放在 URL 查询串（会进日志/历史）；部分接口仍是 `http://`；未见签名/防重放；轨迹数据可还原住址。
- **RAI**：每设备 Bearer token（`Authorization: Bearer <token>`，长度 50..256，`server.go:119-123`、`217-239`），失败 401 + `WWW-Authenticate: Bearer`；token 不进 URL；每个请求走 dataset/device 的传输校验、账户生命周期与角色校验；`ingestAuth` 对 UUID 端点跳过数据库请求预算但保留认证与角色（`server.go:213-224`）；预算/租约/围栏机制防止重放刷量（持久预算每 dataset/设备至多 3 行，重试仍计额）。
  - 存储侧只认 token 的 SHA-256（`devices.token_sha256`），并 join `datasets` 确认未吊销（`dbgen/core.sql.go:41-42`、`httpapi/server.go:396-424`）——即数据库泄露不直接暴露可用令牌。
  - 每个 dataset 只允许一个 collector lease（`migrations/00001_core.sql:18-22`），握手下发 `deviceID/collectorEpoch`，多端争用会 `409 collector_fenced`。
  - 客户端把令牌放 Keychain（`Sources/ProductCore/ClientConfiguration.swift:8-26,49-57`），**无登录/刷新流程，401 不续期**（`RAIAPIClient.swift:51-65`）；过期只能靠重新导入凭据。

## 11. 重试、队列与断点

- **RQrun**：读取阶段逐指标最多 3 次重试，失败写入诊断字段后仍上传；上传失败形态未知；App 依赖状态列表避免重复上传。
- **RAI**：
  - 无 Outbox、无后台传输、无自动重试、无分析轮询（契约明确删除，`sync-v6.md:35-37,299-304`）。
  - 同批逐条上传：单条失败记录 `WorkoutUploadOutcome(workoutID,errorCode)` 后继续下一条；取消则立即抛出（`SyncCoordinator.swift:334-380`）。
  - 传输层没有客户端队列，失败后用户再次点同步即可（服务端幂等保证不产生重复事实）。
  - 历史上 v4/v5 有过 Outbox+状态查询+轮询，v6 已整体移除。
  - 客户端**没有主动限流**；服务端返回 429 + `Retry-After` 时由旧 lane 的 drain 逻辑处理（`RAIAPIClient.swift:173-202`），v6 单条上传路径只把错误映射成 `errorCode` 上抛、不做退避。
  - 令牌不续期：401 直接失败（`RAIAPIClient.swift:51-65`），没有刷新流程。

**评价**：RAI 用“服务端幂等 + 用户重试”替代客户端队列，实现简单、状态少；代价是弱网下没有自动续传。RQrun 也没有观察到自动续传，但它用“上传前查列表”减少重复。

## 12. 服务端流水线

**RQrun**（黑盒，文档推断）：接收 multipart → 解析 JSON → 写业务表 → 异步分析（状态列表 `status:1` = 成功；`analytic_msg` 为分析信息）。

**RAI**（源码）：

1. `acceptWorkoutIngest`（`httpapi/server.go:357-388`）：认证/预算 → 读 body → 解码信封 → 校验路径 UUID 与载荷一致 → `store.AcceptWorkoutIngest` → 200 回执。
2. 接收事务（`storage/postgres/workout_ingest.go:110-127`）：只写 `workout_ingests` 一条记录，**不解码事实、不拿 dataset 行锁**；校验 dataset 状态/epoch/collector 租约/设备权限；查 tombstone、已有接收行、已有 fact 行；配额（`MaxWorkoutIngestBacklog` 行数与字节数）；`INSERT … ON CONFLICT DO NOTHING RETURNING accepted_at`。`raw_json` 是**解压后的原始字节（bytea）**，不是 jsonb（`migrations/00050_workout_ingests.sql:1-6`），提交后长期保留。
3. Worker 领取：每 1s 轮询、`FOR UPDATE SKIP LOCKED`、租约 90s、最多 3 次尝试（`storage/postgres/workout_ingest.go:254-282`）；超过 3 次置 `rejected` + `processing_attempts_exhausted`。
4. Worker 解析与投影：`syncing/workout_wire.go:243-269` 解析 core（activityType/durationUs 必需）/statistics/events/activities/streams/routes；`workout_import_projection.go` 从原始字节重建来源表，`workout_imports.payload` 只存“实际投影的身份”，不存任意扩展字段——保证事实事务可提交；source 不可归类只影响 completeness，不阻断导入（`syncing/source_policy.go:13-23`）。
5. 导入事务（`storage/postgres/workout_bundles.go:300-367`）：一次提交写 workout / samples / associations / series / routes / `datasets.commit_rev` / `change_events` / `dirty_scopes` / `jobs` / `workout_imports`，并在 after-hook 把 ingest 置 `committed`；`lockDatasetRow` 对 datasets `FOR UPDATE`（`store.go:154-168`），锁内 `rev+1`（`:186-200`），revision 序即提交序（`:150-158`）。
6. 分析：独立作业表 + 租约/围栏 + 幂等 input_hash + 发布事务，不在上传路径上（`migrations/00012_analysis.sql`、`analysis_input.go`）；发布单事务含 results/terminal run/publication/generation CAS/job complete/下游 dirty。

## 13. 下载水位与变更日志

- **RQrun**：没有下载水位概念——App 每次从 HealthKit 重新读，服务端结果按 workout UUID 关联；不存在“变更页/提交序”。
- **RAI**：
  - 三个坐标严格区分：客户端 `clientSeq`、dataset `commit_rev`、HealthKit `anchor`；下载水位用 `(commitRevision, ordinal)` 升序，**禁止用 sequence 当水位**（`docs/ARCHITECTURE.md:89`、`contracts/sync-v2.md:13`）。
  - 有 `GET /v2/sync/changes?after=&limit=`（`server.go:176-211`），游标是字符串 `dataset:epoch:rev:ordinal`，按 `(rev,ordinal) >` 取增量、单页 ≤100（`syncing/changes.go:7,26-47`、`storage/postgres/changes.go:9-52`），且只通知身份、实体另取；仅供旧协议/其他客户端消费，握手 `capabilities.changefeed=false`，v6 客户端不使用变更页。
  - 每个事实有确定性 UUID，删除推进 commit revision 并写 tombstone，因此“增量下载 + 删除”可被正确表达。

**评价**：这是 RAI 与 RQrun 的架构级差异：RQrun 是“上传协议”，RAI 是“双向同步协议”。

## 14. 范围差异：daily/VO2/completeness 与双轨遗留

- **RQrun**：抓包中只有 Workout 上传与状态查询；日常健康事实（步数、睡眠等）不在该协议内。
- **RAI**：Workout 之外还同步 daily 事实、VO2max、completeness（`openapi-v2.yaml` 多个 `/v2/sync/*` 端点；握手 `dailyFactsVersion=1`、`vo2maxFactsVersion=1`）。
- **实现现状**：客户端实际是双轨——v6 手动 Workout 上传（`SyncCore/WorkoutBundleV6.swift` + `App/RawStreamSyncEngine.swift`），旧 anchor+Outbox 增量流仍在服务 daily/VO2（`OutboxStore`、`BatchBuilder`、`CaptureManifest`、`SyncSession`、`HealthDiscovery`、后台 `URLSession`/`.batch` 通道仍被 `Product.swift:223`、`BackgroundTransfers.swift:9,21-30` 等引用）；旧通道的 `claimIngest`/`acknowledge` 只有测试调用（`OutboxStore.swift:459-534`）；`ForegroundSyncScheduler` 除自身外无引用，疑似停用。这是 RAI 自身的债务点，不是与 RQrun 的协议差异，但会让人误以为 v6 是“全量同步”。

## 15. 发现的契约/实现不一致（建议修复）

1. **openapi 示例版本号矛盾**：`contracts/openapi-v2.yaml:74` 的示例用 `protocolVersion:3`，而握手能力 `workoutPayloadVersion=6`（`:109`）。实际语义是“会话协议 3 / 载荷 6”，文档应写清两个版本号。
2. **200 语义在旧契约与 v6 不一致**：`contracts/sync-v2.md:23` 要求 `ingestStatus=committed` 才算成功，v6 只承诺原始载荷接收（`sync-v6.md:43-45`）。v6 是唯一支持协议，旧文应标注废弃。
3. **`workout-metadata-registry-v2.md:3-5` 仍写“控制面 completeness/v1、Workout Protocol 3”**，与 v6 现状不符。
4. **v6 错误表缺 `workout_deleted`**：POST 错误表未列删除命中，但服务端确实返回 409 `workout_deleted`（`postgres/workout_ingest.go`）；`sync-v4.md:224-227` 有类似情形。
5. **`capabilities.changefeed=false` 但有 `/v2/sync/changes`**：需注明该端点仅服务 v3/v4 或管理路径。

## 16. 建议

### 若要继续对齐 RQrun 的“产品体验”
- 在客户端补“已成功记录不再上传”的本地缓存（或利用服务端 GET 批量状态），减少用户重复点击后的无效请求——但这会重新引入本地状态，需要权衡 v6 “无状态”原则。
- 考虑把读取重试（当前整条失败）降级为“逐指标重试 3 次 + 诊断码”，与 `status=partial` 模型合并；RQrun 的 `get_data_error` 已证明这是可用体验。
- 把已定义但未接线的 `GET /v2/sync/workout-ingests/{id}` 状态查询与 `AnalysisFollowup` 接到 UI（当前只有定义和测试），至少让“已接收但未提交/已拒绝”对用户可见——这正是 RQrun 用状态列表达到的效果，不需要重新引入 Outbox。

### 若要保留 RAI 的“契约化”优势
- 修复 §15 的文档不一致，并在 v6 错误表中补 `workout_deleted`。
- 明确 `total_calories/total_ascent/avg_heart_rate` 是否由服务端分析产出；若产品需要，应在契约中定义派生事实或 `metadata` 投影，而不是依赖“RQrun 有”。
- 给 `metadata` 定义最小语义白名单（天气/训练计划），否则这些字段永远只是原始字节。
- 客户端补两类最小健壮性：对 429 + `Retry-After` 做一次有界退避重试（现在只有旧 lane 处理），以及 401 时给出凭据更新路径（现在直接失败、无刷新）。

### 若要清理债务
- 评估旧 anchor/Outbox 流是否只剩 daily/VO2 使用；若是，重命名为 `DailySyncEngine` 并与 workout v6 分层，避免“双轨”误读。
- 删除或恢复 `ForegroundSyncScheduler`（当前无调用点）。

---

### 附：关键证据位置

| 主题 | 文件 |
|---|---|
| RQrun 协议全文 | `docs/RQrun-HealthKit-上传协议.md` |
| RAI v6 契约 | `contracts/sync-v6.md` |
| 客户端上传编排 | `ios/RAI/App/SyncCoordinator.swift:300-395` |
| 客户端载荷构建 | `ios/RAI/App/RawStreamSyncEngine.swift:47-176` |
| 客户端投递/压缩 | `ios/RAI/Sources/SyncCore/WorkoutIngestDelivery.swift`、`WorkoutPayloadTransport.swift` |
| 服务端解码/身份 | `apps/server/internal/syncing/workout_bundle_v6.go:12-14,285-341` |
| 服务端接收事务 | `apps/server/internal/storage/postgres/workout_ingest.go` |
| 握手/路由/认证 | `apps/server/internal/httpapi/server.go:83-84,119-123,176-224,350-388` |
| 分析作业 | `apps/server/migrations/00012_analysis.sql`、`internal/storage/postgres/analysis_input.go` |
