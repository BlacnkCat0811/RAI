# v2 增量同步合同：样本、有界／超大 Workout v2 与数量 series 细项

对应 [OpenAPI](openapi-v2.yaml) 和 [协议代码](../apps/server/internal/syncing/protocol.go)。OpenAPI 只描述机器可读形状；能力和限额以 `GET /v2/handshake` 的运行时返回为准，本文是语义规范。所有内容仅为合成数据／新工程，尚未与真实 HealthKit 真机对接。

## 能力和认证

所有 `/v2` 路由使用独立设备 bearer token；数据集由 token 决定，body 的 datasetID 必须相符。只读设备可查询握手、自己的流／批次／任务，不能写入。撤销 token 后认证失败；写事务内再次检查主要采集器及设备权限。新的采集器交接与凭证轮换管理协议尚未实现。

`GET /v2/handshake` 返回稳定 serviceID、datasetID、datasetEpoch、业务版本、时区、限额和支持类型。当前支持 sample、有界 Workout v1 字段及 v2 多活动扩展、已提交数量样本的 series 细项分块，以及超大 Workout 的独立细节分块包；pageReceipts=true、workouts=true、workoutPayloadVersion=2、workoutActivities=true、workoutPackages=true、quantitySeries=true，workoutType=HKWorkoutTypeIdentifier。routes/changefeed/analysis 仍为 false。workoutPayloadVersion 表示最高载荷能力，不是 envelope protocolVersion（仍为 2），也不是完整 HealthKit 导入保证；quantitySeries 只表示细项协议可用，不表示服务端知道手机有哪些 series，也不表示全部历史已采集。不支持的类型、单位、不支持的 series 载荷字段及未知字段返回 `422 unsupported_payload`，不忽略后提交。

## 变更通知（部分能力）

`GET /v2/sync/changes?after=0&limit=50` 返回本 dataset 已提交的变更通知，按 `(commitRevision, ordinal)` 升序；`limit` 为 1–100，默认 50。每项包含 revision、ordinal、eventID、kind、key、operation，不包含事实载荷。`nextCursor` 是最后一条的位置，空页保持请求 cursor；`hasMore` 表示当前查询时后面还有记录，客户端以后仍可从末尾 cursor 继续。初始 cursor 为 `0`，后续位置绑定 dataset ID 和 epoch，格式及位置不合法返回 `422 invalid_change_cursor`。不支持的查询参数、重复参数或非法限额均拒绝。只读设备可访问。日志不使用时间戳或 sequence 排序；同一 revision 可跨页。

此接口仅是提交通知：series 点和大 Workout 明细的下载尚未实现，不能靠它恢复完整事实或把通知当作 HealthKit 采集完成的证明。因此握手的 `changefeed` 仍为 false，P3 不得将它作为完整数据下载能力使用。

## 上传顺序

1. `POST /v2/sync/streams` 登记固定 sampleType、collectorEpoch、queryFingerprint 和 streamID。请求重试身份不变，变更任一字段产生冲突。
2. 将 NDJSON 编码为一次生成、耐久保存的 gzip 文件。按**实际压缩文件字节**计算 payloadSHA256，重试必须复用该文件。
3. `POST /v2/sync/batches` 提交不可变 envelope。sampleType 为样本类型或独立的 HKWorkoutTypeIdentifier；首版仅允许登记流内 `lastClientSeq+1`；同序号的已登记批次会保留身份，不能另建 batch 掩盖失败。
4. `PUT /v2/sync/batches/{batchID}/body`，Content-Type 为 `application/x-rai-health-delta+ndjson`，Content-Encoding 为 `gzip`。
5. 只有 `200` 且 `ingestStatus=committed` 才表示事实已提交。断网或状态未知先 `GET /v2/sync/batches/{batchID}` 查询；必要时重发原文件。GET 返回 BatchState，其中 receipt 是原最终回执。

序号从 1 开始，首批 previousBatchID 必须显式为 null，后续精确指向上一已确认 batch。登记不推进序号，只有事务提交后才推进。登记失败或上传错误不允许自动跳过该序号。首版没有自动撤销／supersede 管理协议，编码错误需保留原数据并修复。

envelope 字段顺序按 [canonical fixture](fixtures/envelope.canonical.json) 固定，所有字段都存在，previousBatchID 为空时显式 null，不带空白和末尾换行；SHA-256 见 [摘要](fixtures/envelope.sha256)。服务器解码后按固定 DTO 字段顺序重编码计算 envelope 身份，不使用请求 JSON 的空白或属性顺序。UUID 和摘要采用小写。跨语言实现必须通过这些 fixture。

## 页交付回执（schema 4）

pageID / fragmentIndex / fragmentCount 绑定在不可变批次身份中。每流同时最多一个未封口页；页从 fragmentIndex=0 开始，各分片占用连续 clientSeq，`clientSeq = firstClientSeq + fragmentIndex`。登记第 0 片时固定 datasetEpoch、collectorEpoch、streamID、queryFingerprint、fragmentCount 和 firstClientSeq；后续不能更改。上一片 committed 后才可登记下一片，不能交错另一页、跳片或重用片号；重复登记原 batch 仍返回原状态。每页最多 10000 片；无法编码的已登记批次继续阻塞，不自动跳过、撤销或替换。

登记创建 pending 页；`GET /v2/sync/pages/{pageID}` 返回页绑定、fragmentCount、firstClientSeq、committedFragments 和 ingestStatus（pending / sealed）。只有全部声明分片依序 committed 后才 sealed；最后一片在**事实、删除屏障、日志、待办、批回执、流位置的同一事务**内固化页回执。单片页也走相同逻辑。空查询页用一个包含空 NDJSON 的合法 gzip 批次表示；不接受 fragmentCount=0。纯 no-op 和空片仍推进页交付计数，不单独增加业务 commitRevision。

sealed 页 receipt 包含 pageID、streamID、datasetEpoch、collectorEpoch、queryFingerprint、fragmentCount、firstClientSeq、lastClientSeq、lastBatchID、manifestSHA256、commitRevision 及 ingestStatus=sealed。commitRevision 是最后一片提交时数据集版本，不是新分配的封口版本，也不是下载水位。receipt 原字节单独存储，后续批次／任务不改写。最终 PUT 响应丢失时，查询 batch 获取原批回执，并查询 page 获取封口结果。

manifestSHA256 绑定有序 envelope：初值 H0 为 **32 个零字节**，对第 i 片计算 `H(i+1) = SHA256(H(i) || SHA256(canonicalEnvelope(i)))`。拼接的是两个 32 字节原始摘要，最终才编码为小写 hex。envelope 自身含页身份、片号、数量、前批、载荷摘要等全部语义字段。独立 Python/Go 对照见 [page-manifest fixture](fixtures/page-manifest.json)。增量摘要和计数同事务更新，只访问该页主键及未封口索引，不在最终片扫描整页或历史事实。

页回执只证明**客户端声明的这些分片已交付**；不证明 HealthKit 查询完整、权限完整、路线／series 完整或分析已完成。各片事实独立提交，pending 页的早期分片事实已可见；不会把整页延迟到最后一片才提交。客户端必须先耐久保存整页、固定分片数，再登记；由 P3 SQLite 队列验证本地 page_seq 连续后推进 delivered_anchor，服务器不接收 anchor。

schema 3 及更早批次保留原 envelope、序号和回执，schema 4 不扫描／回填历史来伪造页封口；它们的 page 查询返回 404 page_not_found，历史 registered 批次仍可按原身份完成上传。新登记批次才建立页关联；历史页 ID 由专用索引保留，不能重用于新页。尚未登记的历史多片页剩余部分不能据此补造 sealed 回执，必须保留本地原页并单独处理兼容。此兼容仅针对新工程的 schema 升级，不实现旧系统数据迁移。

## 数量 series 耐久分块与完整标记（schema 8）

数量样本的 series 细项使用独立的登记／分块／封口接口，不占用样本流的 `clientSeq`，不推进 `stream.lastClientSeq` 或任何下载水位。父样本必须先以普通 sample 批次提交；`POST /v2/sync/series` 以 `(dataset,sampleType,sampleUUID)` 绑定唯一 `seriesID`。父样本不存在或已有 tombstone 返回 `409 series_parent_unavailable`；用不同 `seriesID` 绑定同一父返回 `409 series_identity_conflict`。登记只建立 pending 容器，不分配业务 revision，也不证明手机已经发现全部 series。

`PUT /v2/sync/series/{seriesID}/chunks/{index}` 使用 `application/x-rai-health-series+ndjson` 与 gzip；分块 PUT 共用 upload lane、进程槽和 schema 6 持久上传租约，登记／seal 使用 write lane，GET 使用 read lane。`index` 从 0 开始连续提交；每片最多 **5000 点**、压缩 2 MiB、解压 8 MiB、单行 64 KiB。每行严格为 `{startUs,endUs,value,unit}`：四个字段都必填、不能为 null；`unit` 必须是父数量类型的精确单位；点区间必须位于父样本 `startUs`/`endUs` 内；值必须是有限 JSON 数值。服务端保留来源顺序，不排序、不合并、不去重、不把父容器单值冒充点。空片、未知／重复／大小写别名属性、非 UTF-8、非法 gzip 和非有限数值均拒绝。

分块不可变：相同 `index` 的原始压缩字节重试返回原分块回执；不同字节返回 `409 series_chunk_conflict`；跳号返回 `409 series_chunk_gap`（带 `expectedChunkIndex`）；封口后的新分片返回 `409 series_sealed`，已提交分片的相同字节重试仍返回原回执。分块事务只提交该片事实、原字节回执和 pending 计数／摘要链，**不增加 commitRevision，也不创建分析任务**；它是耐久 staging，不是可独立发布的完整 series。

分块摘要链与页 manifest 使用同一固定算法：初值 H0 为 **32 个零字节**，`H(i+1) = SHA256(H(i) || SHA256(canonicalPoints(i)))`；`canonicalPoints` 是按来源顺序排列的点对象数组，字段顺序固定为 `startUs,endUs,value,unit`，无空白。分块回执包含 `payloadSHA256`（实际压缩字节摘要）、`chunkSHA256`（canonical 点数组摘要）、`manifestSHA256` 和 `committedChunks`。独立 Python／Go 对照见 [series-manifest fixture](fixtures/series-manifest.json)。

`POST /v2/sync/series/{seriesID}/seal` 提交 `chunkCount`、`totalPoints` 和 `manifestSHA256`。服务端只读取至多 1000 行的本系列分块索引计数／点数总和，不重新读取点载荷、不扫描无关历史；计数或总点数不符返回 `409 series_incomplete`，摘要不符返回 `409 series_manifest_mismatch`。成功时在同一事务写 sealed 状态、原始 seal 回执、dataset `commit_rev+1`、`series:<type>:<uuid>` 脏范围、`analysis.series` queued 待办和一条 `change_event`；`analysisStatus=queued` 只表示待办已耐久保存，当前 Worker 没有算法；changefeed 仍为 false，seal 的 change_event 仅是提交日志，不提供 series 点下载接口。相同 seal 重试返回原字节回执；不同声明返回 `409 series_identity_conflict`。分块事务不分配 revision，seal 是本系列唯一的业务发布。

父样本删除通过外键级联删除 series 行和全部分块，并保留父 tombstone；迟到分块或封口返回 `404 series_not_found`，不能用旧 `seriesID` 或新 `seriesID` 复活已删除 UUID。series 不改变父样本的不可变身份、流序号、页回执或已有分析待办；未知父删除仍只有父屏障，不伪造 series 行。

初始限额为 `limits.seriesChunks=1000`、`limits.seriesPoints=1000000`、`limits.seriesPointsPerChunk=5000`，这是安全上限，不是手机历史导入吞吐或生产调优。一个父样本最多一个 series identity；超限或编码失败时客户端必须保留本地待办，不能拆成第二个同父 series、截断点或把不完整容器冒充完整。series 只适用于握手白名单中的数量类型；睡眠类别和 Workout 不是 series 父对象。

schema 7→8 只追加 `series_uploads`／`series_chunks`，不回填、不重写既有批次／页回执、事实、流位置或预算债务。API 权限组须由 owner 显式补充两表 SELECT／INSERT 及 `series_uploads` 的列级 UPDATE；父删除级联不需要 API 直接 DELETE series。部署须先停旧 API、显式 migrate／授权、再启动 schema 8；schema 7 二进制会被 `readyz` 拒绝。回滚保留新 schema、chunks 和已封口事实，不通过 Down 删除 series 降级语义。

## 超大 Workout 耐久分块与完整标记（schema 9）

当单个 Workout 的完整来源对象无法放入普通单行 Workout 事件时，使用独立的 `POST /v2/sync/workout-packages`、`PUT /v2/sync/workout-packages/{workoutID}/chunks/{index}`、`POST /v2/sync/workout-packages/{workoutID}/seal` 和 `GET /v2/sync/workout-packages/{workoutID}`。该包不占用 Workout 流的 `clientSeq`，不推进 `stream.lastClientSeq`、页回执或下载水位；它只负责把一个超大对象拆成不可变细节分块，并在显式 seal 时原子发布一个普通 Workout 事实。包不能替代 Workout 发现、查询完整性或 HealthKit 权限判断。客户端必须把包 seal 回执作为该 Workout 的独立交付状态：Workout 发现流已推进、包已登记或分块已提交都不能单独证明 Workout 已交付，只有 seal 成功才可标记该 Workout 事实可用。

登记请求把 `workoutID`（即最终 Workout UUID）绑定到 dataset、epoch、collector、startUs/endUs、source 和 metadata，并提供只含核心字段的 `workout`：`activityType`、`durationUs`、可选总量；`statistics`、`events`、`activities` 必须缺省或为空。核心对象受普通 16 KiB JSON 登记请求上限约束（超出仍返回普通 `413 batch_too_large`）；超出的活动、统计和事件必须走分块。登记只建立 pending 包，不创建 Workout 事实、不分配业务 revision，也不证明手机已经发现全部细节。

分块体为 `application/x-rai-health-workout-detail+ndjson` 与 gzip；`index` 从 0 开始连续提交。每片最多 **1000 个顶层细节条目**、压缩 2 MiB、解压 8 MiB、单行 64 KiB；整个包最多 **100 片**和 **20000 个细节行**（活动条目把其内部统计／事件也计入细节行）。每个 NDJSON 行严格为以下三种之一：

```jsonl
{"kind":"activity","ordinal":0,"activity":{"uuid":"...","startUs":...,"durationUs":...,"configuration":{...},"statistics":[...],"events":[...]}}
{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":...,"endUs":...,"average":{"value":130,"unit":"count/min"}}}
{"kind":"event","ordinal":0,"event":{"type":1,"startUs":...,"endUs":...,"metadata":{}}}
```

`ordinal` 是该 kind 在所有包分块中的零基连续序号；服务端不排序、不合并、不去重。活动条目保留其内部统计／事件及来源顺序；父级统计必须按 type 严格升序；事件保留来源顺序。未知／重复／大小写别名属性、显式 null、字段组合多于一个、空片、非法 gzip、非有限数值、父范围越界和单行超限均拒绝。单个活动条目自身仍受 64 KiB 单行限制；超出的单一活动不属于本协议，客户端必须保留本地待办，不能裁剪内部细节。

分块不可变：相同 `index` 的原始压缩字节重试返回原分块回执；不同字节返回 `409 workout_package_chunk_conflict`；跳号返回 `409 workout_package_chunk_gap`（含 `expectedChunkIndex`）；封口后新分片返回 `409 workout_package_sealed`。分块事务只提交本片原始字节摘要、规范细节摘要、pending 计数／预算和摘要链，**不创建 Workout 事实、脏范围、分析任务，也不增加 commitRevision**。

分块摘要链与页／series 使用同一固定算法：H0 为 32 个零字节，`H(i+1)=SHA256(H(i)||SHA256(canonicalDetails(i)))`；`canonicalDetails` 是按来源顺序的细节对象数组，字段顺序固定为 `kind,ordinal,activity|statistic|event`，无空白。分块回执包含 `payloadSHA256`（实际压缩字节）、`detailsSHA256`（规范细节数组）、`manifestSHA256` 和 `committedChunks`／`totalDetails`。

`POST .../seal` 提交 `chunkCount`、`totalDetails` 和 `manifestSHA256`。服务端先核对至多 100 个分块索引行的计数／摘要，再在**同一事务**中读取这些已提交分块、把核心与全部细节组装并完整验证为一个 Workout Event，写入 `workouts` 及 `workout_statistics`／`workout_events`／`workout_activities`，同时写 dataset `commit_rev+1`、`workout:<type>:<uuid>` 脏范围、`analysis.workout` queued 待办、change event 和原始 seal 回执。计数或总细节行不符返回 `409 workout_package_incomplete`，摘要不符返回 `409 workout_package_manifest_mismatch`；相同 seal 重试返回原字节回执，不同声明返回 `409 workout_package_identity_conflict`。seal 前不存在 Workout 事实，因此不会暴露半成品；只有 seal 后该 UUID 才成为普通不可变 Workout，后续普通 Workout 或第二个包不能再覆盖。

普通 Workout 与包共享 `(dataset,HKWorkoutTypeIdentifier,uuid)` 身份。已有 Workout 事实或不同核心的包登记返回 `409 workout_package_identity_conflict`；删除屏障存在返回 `409 workout_package_unavailable`；pending/sealed 包存在时普通 Workout upsert 返回 `409 workout_package_conflict`。删除 Workout 通过外键／删除触发器级联清除包和分块，迟到分块或 seal 返回 `404 workout_package_not_found`，不能用旧 UUID 复活已删除对象。包不改变父 Workout 之外的流、页、预算或既有回执；未知删除仍只有普通 Workout 屏障。

初始限额由握手返回：`workoutPackageChunks=100`、`workoutPackageDetails=20000`、`workoutPackageDetailsPerChunk=1000`；这些是安全上限，不是手机超大 Workout 的实测吞吐或生产调优。schema 8→9 只追加 `workout_packages`／`workout_package_chunks` 与删除触发器，不回填、不重写既有批次／页／series 回执、事实和预算债务；API 权限组须由 owner 显式补两表 SELECT／INSERT、`workout_packages` 列级 UPDATE 和 DELETE，再停旧 API、显式 migrate、启动 schema 9。回滚保留新 schema、seal 事实和 staging，不通过 Down 删除包降级语义。尚无 Swift 编码器、真机、百万级 Workout 或生产吞吐证据。

## 有界解析

每批最多 1000 个输入事件、压缩 2 MiB、解压 8 MiB，每行最多 64 KiB；超出任一限额均失败。JSON 登记请求最多 16 KiB。支持最后一行无换行；不接受空白行、重复属性、非 UTF-8、未知属性、大小写别名属性、尾随 JSON、缺少必填字段或不合法 gzip 校验。属性名必须与合同逐字一致（包括嵌套对象），不能利用 Go JSON 的大小写宽松匹配覆盖字段。服务器不信任 Content-Length。

每进程最多两个并发 body 解析／上传，同一 dataset/device 最多一个；schema 6 另以持久租约限制跨副本每 dataset 两个有效上传、每 device 一个。多余请求返回 429。进程槽从解析前占用到响应结束；成功或失败均释放。持久槽尽力释放，崩溃或释放故障由固定期限回收；持续速率预算见下节。入库事务总预算 5 秒，普通语句 2 秒、锁等待 500 ms。HTTP 读超时 15 秒、写超时 25 秒。

网络读取、SHA、解压、结构验证在事务和 dataset 行锁之外执行。随后以 dataset → stream 的锁顺序，原子提交本批事实、墓碑、旧／新范围、提交日志、dirty scopes、jobs、最终回执和连续序号。COMMIT 成功才回 200。

## 持久请求预算（schema 5）

已认证 `/v2` 请求在读取请求体和执行业务之前，按服务端设备身份分别扣除 dataset 和 device 的持续预算；未认证请求、只读设备的写入及公开健康检查不创建预算。读取（GET/HEAD）、JSON 登记（POST）和文件尝试（PUT）使用独立预算，上传超限仍可查询原批／页回执，查询自身也有限额。握手 `limits.requests` 返回以下固定初始配置：

| lane | 每 device：每分钟速率／突发数 | 每 dataset：每分钟速率／突发数 |
|---|---|---|
| read | 120 / 30 | 240 / 60 |
| write | 60 / 10 | 120 / 20 |
| upload | 30 / 2 | 60 / 4 |

预算使用数据库时钟和连续补充的 GCRA（虚拟下一可用时间），不使用固定分钟窗口；闲置最多恢复突发数。每次 upload 尝试保守预留整个 2 MiB 压缩／8 MiB 解压预算，不信任 Content-Length，也不按客户端声明的小文件优惠。因此单 device 长期接受上限为 60 MiB 压缩／240 MiB 解压每分钟，另允许表中初始突发量；这不是吞吐实测或配额利用率保证。

`sync_request_budgets` 只保存至多每 dataset 三行、每已注册设备三行；路径、batchID、页、token 均不成为预算键。短事务先扣 dataset，再扣 device；任一拒绝或数据库错误则回滚两者，拒绝不延长等待时间。已获准尝试随后即使解析、并发槽或业务失败也不退额，重试仍计额；重新启动 API、增加副本或轮换同设备 token 不重置余额。持久预算跨进程共享；schema 6 的跨进程上传租约见下节。

这是一笔在业务处理前独立结束的运行配额事务，不改事实／回执／序号／任务，不增加 commitRevision，不进入 changefeed。FK 检查可能短暂锁定已登记身份；事实事务仍保持 dataset → stream 加锁并整体提交。预算存储失败返回 503，不放行未检查请求；若准入 COMMIT 响应丢失，可能已计额，但业务尚未执行。

超限返回 `429 device_rate_exceeded` 或 `dataset_rate_exceeded`；同设备活跃上传返回 `device_upload_capacity_exceeded`，进程满载返回 `upload_capacity_exceeded`。所有 429 均含整数秒 `Retry-After` 和相同的 JSON `retryAfterSeconds`；这是最早建议重试延迟，竞争者仍可能先占余额。客户端保留原不可变身份、文件及 anchor，按延迟并附加 jitter 重试；不得换 batch、token 或 page 规避配额，状态未知先查询原回执。

初始数值尚未通过手机历史导入、N100 或大规模压测调优。跨副本租约已有本地真实 PG 正确性验证，fnOS ingress 的未认证连接／带宽限流与实际多副本部署仍待 P7 验证；本层不能替代入口保护。schema 4→5 只增加空预算表，原批／页回执不变。现有权限组须由数据库 owner 显式补上 [roles.sql](../deploy/postgres/roles.sql) 的预算 SELECT/INSERT 与 available_at UPDATE 权限，再启动对应 schema 的 API；应用不会自行提权或升级。

## 跨副本上传租约（schema 6）

PUT 依次执行认证／持续扣额、路径及媒体检查、进程槽、持久租约，再查询批次／流并读取 body。握手 `limits.datasetUploads=2`、`deviceUploads=1`、`uploadLeaseSeconds=30`；原有 processUploads=2、processDeviceUploads=1 仍适用。持久上限按 dataset 聚合所有 API 副本；没有实现跨 dataset 的部署总并发上限。

每 dataset 仅有两个 `sync_upload_slots` 行，以固定 slot 顺序锁定；无请求／batch／token 历史行增长。短事务使用数据库时钟判定到期并分配随机 token，提交后释放连接，再读取／校验 body，不在网络读取期间持有事务或 dataset 业务锁。同设备有效租约先返回 `429 device_upload_capacity_exceeded`；两个槽均占用返回 `429 dataset_upload_capacity_exceeded`。Retry-After 和 retryAfterSeconds 是对应租约剩余时间向上取整，至少 1 秒；进程槽拒绝仍建议 1 秒。竞争、提前释放及独立速率预算可能改变实际重试时机。已扣速率预算不因槽拒绝退还。

租约固定 30 秒，无客户端 token、续租或强制解锁接口。HTTP 请求 context 最长 20 秒，现有网络读超时 15 秒；context 结束主动关闭 body 以中断阻塞读取。完成后以独立、至多 1 秒的 context 尽力释放，匹配 dataset/device/token，重复或迟到释放不会清除新租约。数据库不可用则在读取 body 前返回 503；释放故障不把已提交的回执改成失败。进程崩溃、准入 COMMIT 响应丢失或释放故障最多占用至租约到期；恢复不需要 Worker 扫描。GET 原批／页回执不占上传槽。

事实提交仍按 dataset → stream → 对应 slot 加锁，验证 token 与数据库到期时间；持有 slot 行锁直至事务结束，并在事实／删除屏障／日志／待办／批回执／页封口均写入后、COMMIT 前再检查有效性。失效返回 `409 upload_lease_lost`，整笔业务事务回滚，原登记及不可变文件保留；客户端查询原批后按预算重试。token 不进入 envelope、manifest、回执或 changefeed，不产生业务 revision。事务中的有效性检查及行锁是提交保护，不承诺 COMMIT 响应早于墙钟到期时刻。

租约不能强制停止被操作系统挂起后恢复的旧进程。到期回收允许新请求解析，旧解析器可能短暂与它重叠；旧 token 无法提交，单进程解析仍受本地槽限制。因此这里保证有效租约数和事实提交 fencing，不宣称任何故障情况下的物理 CPU／网络绝对并发上限。入口连接／带宽保护、全部署资源预算、真实多主机及 N100 负载仍未验收。

schema 5→6 只追加空运行槽表，不改原事实、页／批回执或持续预算。已有权限组由 owner 显式追加 slots 的 SELECT/INSERT 和 device_id/token/expires_at UPDATE 权限，API 不能删除槽或更改固定 slot。迁移和权限变更不由在线应用执行。旧 schema 5 二进制不能参与本协议：升级时须先停旧 API，再显式 migrate／授权，启动 schema 6 API；不能让未检查租约的旧副本继续接收 PUT。尚未执行此部署验收。

## 样本

身份为 `(dataset,type,uuid)`，kind 固定 sample。upsert 必须提供整数微秒 startUs/endUs、来源 bundleID，以及 quantity 或 category 两者之一。保留完整微秒精度；服务端 UTC 存储，业务时区显式为 Asia/Shanghai。metadata 只能放受单行预算限制的扩展字段，不能把路线或 series 伪装为标量。

| HealthKit 类型后缀 | 单位 |
|---|---|
| HeartRate、RestingHeartRate、WalkingHeartRateAverage、RespiratoryRate | count/min |
| HeartRateVariabilitySDNN | ms |
| StepCount | count |
| DistanceWalkingRunning | m |
| ActiveEnergyBurned、BasalEnergyBurned | kcal |
| VO2Max | mL/kg/min |
| BodyMass | kg |
| OxygenSaturation | 1（比例值） |
| RunningSpeed | m/s |
| RunningPower | W |
| HKCategoryTypeIdentifierSleepAnalysis | category 整数 0–5，无 quantity |

数量类型完整名称以 `HKQuantityTypeIdentifier` 开头。握手白名单是实际准入依据；类型与单位适配尚需后续 Swift 端和真机校验。

delete 只允许 op/kind/type/uuid 四个字段。必须源于明确删除事件，不能从空查询、缺席或权限变化推断删除。同批 UUID 先聚合，delete 优先；未删除的重复 upsert 内容必须相同。同 UUID 不同内容返回冲突，不能覆盖已保存事实。

事件身份以固定字段顺序编码，metadata 键排序，空 metadata 归一为缺省，数量使用 Go JSON 的有限数值表示；metadata 中 JSON 数字保留其词法精度，编码器应在持久化前固定表达。delete 与 upsert 不混用同一身份版本。已存在 tombstone 的旧 upsert 记录 suppressed，不复活。

## Workout v1 字段及 v2 活动扩展（schema 7）

独立流使用 `sampleType=HKWorkoutTypeIdentifier`，事件 `kind=workout`、`type=HKWorkoutTypeIdentifier`。身份、流序号、删除屏障、整批冲突回滚与 sample 相同；delete 仍严格只允许 op/kind/type/uuid。历史兼容错误码 `sample_identity_conflict` 同时用于 Workout UUID 的不可变内容冲突。

upsert 具有 startUs/endUs/source、可选 metadata 和必填 `workout` 对象；不允许 quantity/category。合成示例见 [workout.ndjson](fixtures/workout.ndjson)。`workout` 接受以下字段（v1 字段原有顺序及省略规则不变）：

| 字段 | 合同 |
|---|---|
| activityType | HealthKit 原始正整数标识，1–2147483647；只保留来源，不由服务器猜测运动语义 |
| durationUs | 必填整数微秒，0 到 endUs-startUs，表示活动时长 |
| totalDistance | 可选 quantity，非负且单位严格为 m |
| totalEnergyBurned | 可选 quantity，非负且单位严格为 kcal |
| statistics | 可选数组，最多 128 项；按 type 严格升序且唯一 |
| events | 可选数组，最多 256 项；保留原顺序，包括同时间事件 |
| activities | v2 可选数组，最多 128 项；保存来源顺序，活动 UUID 在本 Workout 内唯一；空数组与省略归一 |

统计项包含 type/startUs/endUs，以及 sum/average/minimum/maximum 中至少一个 quantity；只接受样本白名单中的数量类型及精确单位，不接受睡眠类别或 series 类型。事件包含 type/startUs/endUs 和可选 metadata；type 为原始正整数标识，1–2147483647。统计与事件区间必须位于父 Workout 内。省略的字段不伪造零值；不接受显式 null、缺失 quantity.value、未知或重复字段。可选空数组与缺省数组编码为同一身份。统计项顺序由生产者固定，事件顺序不可重排。

一批输入事件的活动行、Workout 级及活动级统计项与事件总数合计最多 **2000**，超出在 dataset 加锁前返回 413 batch_too_large；仍同时受 1000 行、64 KiB 单行及压缩／解压预算限制。此预算不能当作最大批次性能保证，5 秒事务超时仍可能拒绝过重批次。客户端必须在不可变批次登记前按预算拆分。

主体以完整验证后的 JSON 保存，统计与事件另有同事务子表投影。删除使用父表外键级联清理子表；永久 tombstone 仍按 `(dataset,type,uuid)` 保留。账本保留删除前区间；已知范围产生 `analysis.workout` 待办，Worker 尚不执行算法。尚未实现 zone、显式样本关联、路线或高频细项耐久分块；未知结构返回 422，不接受把它们降级塞入 metadata。客户端如读到这些尚未支持的内容，必须保留本地待办并报告不支持，不能丢字段后提交、截断事件或宣称完整导入。

v2 的每个 activity 必填 uuid、startUs、durationUs 和 configuration；可选 endUs、metadata、statistics、events。uuid 为来源 UUID，不新造身份；startUs 位于父 Workout 内，提供 endUs 时须在 activity startUs 与父 endUs 之间。endUs 不可显式 null；来源缺失时省略并原样保留，服务器仅用父 endUs 检查 durationUs／细项边界，不将其回填为活动结束时间。durationUs 是来源整数微秒，非负且不超过活动区间（无活动 endUs 时使用父 endUs 上界）。不按活动时长总和校正父时长，不猜测间隙、重叠或运动语义，不重新按 UUID／时间排序。

configuration 必填 activityType（1–2147483647）、locationType／swimmingLocationType（0–2147483647），均只保留原始标识；位置 0 表示来源未指定。可选 lapLength 为非负 m quantity，省略不补零。活动级 statistics／events 沿用父字段白名单、顺序及 128／256 单活动上限，区间受本活动边界约束；所有层级同时计入批细项和单行字节预算。未知活动／配置／zone／series 字段、重复键、大小写别名、缺失值及显式 null 均拒绝。完整合成载荷与独立 Python 摘要见 [活动 fixture](fixtures/workout-activities.ndjson)／[SHA-256](fixtures/workout-activities.sha256)。该摘要针对无末尾换行的 canonical Event JSON。

新增 workout_activities 保存有界活动投影，主键为 dataset／workout／ordinal，UUID 在父对象内唯一；配置、metadata、统计及事件完整保存于活动 JSON，父 Workout 的完整 JSON 同样保留。它们随父事实、日志、脏范围、任务、回执与页封口一并提交；删除父对象级联清除活动，永久 UUID 删除屏障不变。没有独立活动上传或更新入口，不创建第二套事实修订。

活动属于不可变 Workout 身份。schema 6→7 不回填活动、不改历史 envelope／事实摘要／回执；未带活动的 v1 文件重试仍返回原字节，既有 registered v1 批次可完成。给已存在的 v1 UUID 增补活动会返回 sample_identity_conflict，不能覆盖；有界协议不是后续详情补写协议。省略／空 activities 不证明来源只有一个活动或已经完整采集；客户端不能裁剪原活动、细项或不支持字段后提交，也不能靠拆成多个同 UUID 事件绕过单行限制。超大对象保留原待办，等待专项分块协议，不推进已交付 anchor。

schema 7 只追加新表。owner 显式 migrate 后，按 [roles.sql](../deploy/postgres/roles.sql) 为既有 API 权限组补充 workout_activities SELECT／INSERT；API 无直接 UPDATE／DELETE 子表权限，父 DELETE 的 FK cascade 按 PostgreSQL 约束执行。先停旧 API、迁移／授权、再启动 schema 7；旧二进制不能接收新活动载荷。回滚须先停写并保留 schema 7 与已提交事实，不通过 Down 或删除活动降回旧语义；部署／回滚过程仍待现场验收。

字段边界也参考 [HKWorkoutActivity](https://developer.apple.com/documentation/healthkit/hkworkoutactivity) 和 [HKWorkoutConfiguration](https://developer.apple.com/documentation/healthkit/hkworkoutconfiguration)；这是一份有界服务端子集，没有 Swift／真机端到端证据。

字段设计参考 [Apple HKWorkout](https://developer.apple.com/documentation/healthkit/hkworkout) 的 duration、统计及事件能力；这份合同是本工程当前子集，不等于所有 SDK 字段已实现，尚无 Swift／真机端到端证据。

## 回执及任务

计数按同批聚合后的唯一 UUID 统计，不保证相加等于输入行数。inserted 是新增事实，unchanged 是相同事实／重复删除，deleted 是删除已有事实，tombstoned 是未知 UUID 的新删除屏障，suppressed 是被屏障阻止的旧 upsert。

确有事实或新屏障才增加 commitRevision；纯重试、相同事实、空批仍提交回执和连续序号。最终回执保存原始 JSON 字节，后续任务完成不能改写它。

范围已知的插入／删除在同一事务产生 `analysis.sample`／`analysis.workout` 待办和实体 scope；未知删除没有贡献范围，不伪造任务。`analysisStatus=queued` 只表示待办已耐久保存，**当前没有可执行分析算法**。Worker 仅恢复租约，任务查询与握手明确反映此边界。跨指标影响索引、检查点、结果发布和完整语义对照属于 P5。

任务续租／完成同时校验 token、state、lease_until，过期 token 不可完成。scope 保留 generation 与 clean_generation；新变更不能被旧任务的完成操作清除。过期任务每次最多重排 20 个，达到 8 次尝试后保留 failed，不扫描事实表重建队列。

## 稳定错误

| HTTP | code | 客户端动作 |
|---|---|---|
| 401 | unauthorized | 保留待办，修复凭证 |
| 403 | dataset_forbidden / read_only_device | 停止越权写入 |
| 404 | stream_not_found / batch_not_found / page_not_found / series_not_found / workout_package_not_found / job_not_found | 核对身份与登记状态，不清 anchor；series_not_found 不重放旧分块，workout_package_not_found 不重放旧包 |
| 409 | dataset_epoch_changed / collector_fenced | 停住该 scope，等待受控恢复 |
| 409 | stream_identity_conflict / batch_identity_conflict / sequence_identity_conflict / sample_identity_conflict | 保留原文件，报告冲突 |
| 409 | page_identity_conflict / page_fragment_gap / page_incomplete / page_sealed | 保留原页与文件，核对片号／数量／绑定；先完成未封口页 |
| 409 | series_identity_conflict / series_parent_unavailable / series_chunk_conflict / series_chunk_gap / series_sealed / series_incomplete / series_manifest_mismatch | 保留本地 series 任务与原文件；核对父样本、分块序号、总点数和 manifest，不新建第二身份绕过 |
| 409 | workout_package_identity_conflict / workout_package_unavailable / workout_package_conflict / workout_package_chunk_conflict / workout_package_chunk_gap / workout_package_sealed / workout_package_incomplete / workout_package_manifest_mismatch | 保留本地超大 Workout 待办与原文件；核对包身份、分块序号、细节行数和 manifest，不拆成普通 Workout 或第二个包绕过 |
| 409 | sequence_gap / previous_batch_mismatch | 查询前批；sequence_gap 带 expectedSeq |
| 413 | batch_too_large / event_too_large / series_chunk_too_large / series_point_too_large / series_too_large / workout_package_chunk_too_large / workout_detail_too_large / workout_package_too_large | 未登记前按预算拆分；已登记批次／series／Workout 包分块不得私自换内容 |
| 415 | unsupported_media_type / unsupported_content_encoding | 修复传输头 |
| 422 | unsupported_payload / payload_hash_mismatch / event_count_mismatch | 整批不提交，不丢弃坏行；行错误带从 0 开始的 eventIndex |
| 429 | device_rate_exceeded / dataset_rate_exceeded / device_upload_capacity_exceeded / upload_capacity_exceeded | 保留原身份及文件，遵循 Retry-After 退避；上传限流可用独立读取预算恢复回执 |
| 503 | temporarily_unavailable | 状态可能未知，先查询 batch 回执 |

工作队列内部另有 `409 lease_lost`，不作为手机事实回执。普通同步不会触发备份、恢复、迁移、全库摘要或全量分析；这些管理能力不在在线依赖图中。
