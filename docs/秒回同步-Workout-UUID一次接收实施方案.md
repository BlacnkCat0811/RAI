# RAI 秒回同步实施方案 · 每个 Workout UUID 接收一次

**状态为实施方案，尚未实现或验收。** 本文基于 2026-11-09 仓库中的 schema 49 和原始 Workout v4，作为[原异步回执提案](秒回同步-异步接收回执实施方案.md)的修订版。后续按下列步骤在本机实施和验收。

## 1. 产品规则

1. 一次跑步对应一个 `HKWorkout.uuid`。服务端用 `(dataset_id, workout_uuid)` 唯一识别一条上传，**以首次成功接收并提交的原始快照为准**。
2. 首次同步读取 Workout、当时可读的关联样本和轨迹，序列化成现有 `protocolVersion: 4` 原始 JSON。iOS 在发送前把上传文件写入 Outbox。服务端持久化解压后的原始 JSON 字节，并保留未知字段及未投影内容。
3. 断网、响应丢失或 App 退出时复用已保存的文件。服务端已接收时返回原接收回执。收到接收回执后，同一 Workout 的普通同步只跳过或查状态。
4. 首包接受后，后来到达的心率、轨迹和其他 HealthKit 数据不会自动补进这一条。某个可选流读取失败时沿用 v4 的降级行为。后台解析可以重试同一原包。
5. 新接口直接用 Workout UUID 发送、去重、恢复和查询。新接口与客户端 Outbox 的这一条路径使用 UUID，不增加 `uploadID`、`ingestID` 或载荷哈希字段。复用导入核心时，现有事实身份检查、`workout_imports` 历史索引和分析依赖仍使用它们已有的内部摘要。服务端接收阶段和客户端状态轮询无需计算载荷摘要。

用户可见状态依次是「正在准备」「正在上传」「已接收，等待入库」「已入库，等待分析」「分析可查看」。上传动作在**接收回执已保存到本地**时结束，事实入库和分析按各自状态更新。解析失败单独展示。

## 2. 仓库现状与改动边界

| 位置 | 当前行为与改动 |
|---|---|
| [v4 合同](../contracts/sync-v4.md)与[上传 handler](../apps/server/internal/httpapi/server.go) | 现有上传在事实事务提交后返回 `committed`。新增独立能力和接收接口，载荷、multipart、gzip 限额及投影规则继续复用。 |
| [iOS 手动入口](../ios/RAI/App/RAIApp.swift)与[同步协调器](../ios/RAI/App/SyncCoordinator.swift) | 手动入口先做 scoped discovery，再强制准备；已有文件的 v4 恢复会复用原包。把 UUID 判断移到详细 HealthKit 查询之前，并让自动恢复走同一个判断。 |
| [现有导入](../apps/server/internal/storage/postgres/workout_bundles.go) | 现有历史表按 Workout 与摘要保存回执和 `jsonb`。复用事实投影，把精确字节保存在新接收表。 |
| [数据库限流](../apps/server/migrations/00005_request_budgets.sql) | 旧 handler 的 `Admit` 首次建限流桶有 `datasets` 外键，可能在导入行锁下等待。新接收与状态入口需要自己的无该行锁依赖的准入路径。 |
| [运行角色](../deploy/postgres/roles.sql)与 [schema 门禁](../apps/server/internal/storage/postgres/schema.go) | Worker 当前缺少部分事实写权限，旧二进制只接受 schema 49。实现时要用真实角色覆盖写入，回滚使用兼容新 schema 的版本。 |

首版按个人部署设计，单 API 实例、单接收解析循环，使用现有 Go API 与 Worker 镜像及 PostgreSQL。健康日常同步、VO₂、分析发布和 WorkoutKit 继续走各自现有入口。RQrun 抓包可参考其一条 Workout 一个文件的交互，本文的幂等与持久化规则由 RAI 自行定义。见 [RQrun 上传协议](RQrun-HealthKit-上传协议.md)和[工程约定](../AGENTS.md)。

## 3. 接口合同

在现有 handshake 中新增 `workoutUUIDIngestVersion: 1`，另提供 `workoutUUIDIngestAccepting` 表示是否接收新包。载荷仍为 v4。新客户端只有在已协商 UUID 模式且本地启用该功能时，才对新任务选择新接口。已开始的 UUID 任务始终按已记录的入口恢复。

### POST `/v2/sync/workout-ingests/{workoutID}`

- 使用现有单 `file` gzip multipart。首次接收时校验路径 UUID 与原始 JSON 的 `workoutID` 一致，验证 Bearer principal、dataset、dataset epoch、collector epoch、UTF-8、重复键、深度、版本和现有请求大小限制。完整 Workout 核心、来源策略与事实投影由 Worker 检查。
- 首次接收成功返回 HTTP 200，响应包含 `workoutID`、`ingestStatus: "accepted"` 和 `acceptedAt`。响应必须晚于数据库提交。接收回执以首次记录的字段值为准，后来转为 `committed` 或 `rejected` 仍可重放该回执。
- 同一 UUID 再次提交时，先在已授权的 dataset 内查墓碑、接收行和既有 Workout，再考虑新接收配额。已有接收行时直接返回原接收回执；即使客户端递交了不同内容，也以首包为准。UUID 只在同一 dataset 内去重。
- 已有 Workout 但没有接收行时返回 `already_imported`。这是兼容状态，由现有事实证明，适用于升级前已经入库的记录，包括只有根事实的历史记录。无需补写新接收行或虚构旧回执。
- Workout 根墓碑优先，返回 `409 workout_deleted`。已撤销或被 fencing 的设备不能用重试绕过授权。暂时不可用返回 503，接收并发或积压超限返回 429 并带 `Retry-After`。格式错误或过大在接收前返回 422 或 413。

### GET `/v2/sync/workout-ingests/{workoutID}`

按 principal 的 dataset 查询。读取当前根墓碑、接收行和既有 Workout 时使用同一数据库快照，按以下优先级返回。响应包含 `workoutID` 与状态，跨 dataset 的 UUID 不暴露其他数据集记录。

| 优先级 | 状态 | 客户端动作 |
|---|---|---|
| 1 | `deleted` | 结束此任务，显示已删除。 |
| 2 | `accepted` | 已接收，继续等入库。 |
| 2 | `committed` | 读取 `commitRevision`、`analysisStatus` 和 `analysisJobIDs`，接续现有分析查询。无新增事实时沿用 `analysisStatus: not_required`。 |
| 2 | `rejected` | 显示稳定错误码，停止自动重传。 |
| 3 | `already_imported` | 显示已入库，使用现有 Workout 分析查询；该响应无需新回执字段。 |
| 4 | `404 workout_ingest_not_found` | 仅在确认服务端支持该接口且当前会话匹配时，允许继续准备或发送原包。 |

网络超时、通用 404、服务端 5xx、权限或会话错误均不能当成「Workout 未接收」。GET 只返回状态与必要回执信息。

## 4. 服务端设计

### 4.1 存储与接收事务

以 schema 50 为目标，新增 `workout_ingests`，主键 `(dataset_id, workout_uuid)`。一行同时承担原始字节存储、接收回执和解析待办。建议字段为 `raw_json bytea`、`payload_bytes`、认证得到的 `device_id`、`dataset_epoch`、`collector_epoch`、`state`、`accepted_at`、`commit_receipt`、`finished_at`、`error_code`、`attempts`、`next_attempt_at`、`lease_until`。`accepted_at` 与 UUID 即可确定接收回执。索引覆盖待解析状态、到期时间及按 dataset 统计的积压。

精确原文保存在 `raw_json`，正常入库及解析失败后继续保留。现有 `workout_imports.payload` 的 JSONB 供已有分析和事实读取路径消费。历史 JSONB 无法补出当初丢失的格式字节。接收行作为去重与状态依据长期保留，状态与日志均不输出原文。

接收顺序如下。

1. 复用 Bearer 验证、会话限制和 HTTP 超时。新 POST 复用现有进程内上传槽。新 GET 使用有界的进程内状态查询限流。新端点的准入跳过带外键写入的 `Admit`，现有端点继续使用其既有预算。
2. 用普通 `SELECT` 验证设备仍持有当前 collector 授权，并查询墓碑、同 UUID 行和既有事实。重复请求优先返回已存在结果。
3. 对新包读取并验证信封。在短事务内检查待解析配额，建议初始上限每 dataset 200 条或 512 MiB，计算时计入本次解压字节。新包写入加同 dataset 的进程内单槽，覆盖配额计数与插入，避免并发超额；每设备上传槽负责限制网络与解压开销。
4. 只写 `workout_ingests`，表中 `dataset_id` 不建外键，接收事务不锁 dataset 行。以 `INSERT ... ON CONFLICT DO NOTHING` 配合回读解决同 UUID 并发，禁止用冲突更新覆盖原字节。事务提交后才能回 200。

普通读可能与管理删除、撤销或 dataset 退役并发。接收回执只证明字节已入库，最终写事实前由 Worker 在 dataset 锁下作权威复核。接收阶段不分配 `commit_rev`。

显式 Workout 删除在原有墓碑事务内清除该 UUID 的 `raw_json`，仍保留回执和去重身份。待解析行一并终结。若接收插入与删除交错，Worker 看到已提交的根墓碑后终结并清除该行原文。任何路径都按墓碑返回删除状态。

### 4.2 Worker 与事实事务

1. 在现有 `rai worker` 进程启动一个解析循环。短事务通过 `FOR UPDATE SKIP LOCKED` 认领到期任务，写入 `lease_until`，递增 `attempts`。以该次 `attempts` 作为认领令牌，固定租约应长于单次完整处理时限。首版建议租约 90 秒，单次处理总预算 60 秒，现有事实事务继续受 45 秒上限约束。
2. 解码原始 v4 JSON，按现有流程生成投影。复用导入核心时使用已认证的 `device_id` 和原会话 epoch，先在事实事务内取得 dataset 行锁，复核设备、epoch、退役与 Workout、样本及 route 墓碑，再执行身份检查。
3. 在**同一事实事务**写入事实、提交日志、dirty scope、必要的 `analysis.workout` job、现有历史回执及新接收行的 `committed` 状态。最后更新接收行时匹配当前 `attempts`、`state = 'accepted'` 和未到期租约。影响行数不是 1 时整笔回滚。分析继续由现有循环真实执行并发布。
4. 永久错误回滚事实后写 `rejected`。数据库暂态错误指数退避，初始总上限 3 次。错误更新同样验证认领令牌及有效租约。Worker 崩溃后可在租约过期时重新认领，最后一次认领崩溃的任务在租约过期后终结为 `rejected`。客户端在首版仅展示最终错误，人工重排接口留待实际需要时评估。

重构 `ImportWorkoutBundle` 时让旧同步入口和新 Worker 共用事实写入核心，由各自的外层事务控制提交。旧路径的精确重试、删除优先、`DeletionOnly` 和原回执行为需要回归覆盖。新 Worker 按实际 SQL 列出需要的事实写入和 route 函数权限，采用现有受限角色；接收 API 只取得接收表所需权限。用部署同款的受限角色运行隔离 PG 集成测试。

## 5. iOS 实现

### 5.1 在采集前按 UUID 判断

把判断放在 `RAIApp.syncOne` 的 scoped discovery 之前。批量操作复用本轮 handshake，按 Workout 分别处理。启用 UUID 模式的会话跳过旧式 Workout 明细 discovery 与 `syncDiscovered` 自动补传，Workout 列表发现和日常健康发现继续按各自入口执行。当前批次的上传结果在接收回执落盘后就能展示，随后异步刷新服务端状态。

| 本地状态 | 行为 |
|---|---|
| 已入库或历史记录已同步 | 直接跳过，已有分析状态仍可刷新。 |
| 已接收、待入库 | 按 UUID 查询状态，复用已有的前台退避调度。 |
| 有已保存文件，尚无接收回执 | 先查当前 UUID。确认未接收才发送原文件。 |
| 无文件也无回执，例如首次同步或重装 | 先查当前 UUID。确认未接收后读取 HealthKit 一次，写原始文件并上传。 |
| 已删除或解析拒绝 | 保持终态并展示原因。 |

### 5.2 Outbox 和进程恢复

- 准备前保存 UUID 模式标记、Workout UUID、会话作用域和 multipart boundary。文件写入临时路径，再原子改名为该会话内对应 UUID 的固定上传文件，最后提交待上传状态。若在改名后、状态更新前退出，恢复可从已记录的 UUID 找回成品。未完成的临时文件不参与上传。旧状态字段采用可选解码，旧任务的待上传文件继续可读。
- 存量待上传 v4 原包在启用 UUID 模式后先查服务端，确认为未接收且作用域和载荷版本匹配时，复用原文件或从其中已有的 JSON 生成 gzip 成品。会话失配单独报错，避免把旧 dataset 的包送到新会话。
- 把「保存接收状态」和「授权清理本地文件」做成一个 Outbox 更新，实际删文件由现有清理流程执行。收到回执与本地落盘之间被杀时，重启仍可按 UUID 查询并恢复。
- 按当前会话作用域和 Workout UUID 绑定上传及查询回调。背景任务身份使用会话加 Workout UUID，复用现有后台传输管理，UUID 模式无需重复读取文件来核对载荷哈希。
- UUID 模式不以 `deliveredGeneration`、capture manifest 或 `forceNewBundle` 判定是否重传。启动恢复、前台自动交付、普通手动同步和暂停恢复应共用这一分支。关闭页面时取消轮询，回到前台接续，入库后复用现有分析刷新。

## 6. 实施步骤与完成条件

### 第一步 · 固化合同与本机基线

1. 更新 `contracts/sync-v4.md`、`contracts/openapi-v2.yaml` 与变更记录，明确首次接收、历史兼容、删除优先及终态留存。
2. 在本机隔离环境用合成 Workout 记录现有 HTTP 时延和重复同步的 HealthKit 明细查询、组包及 POST 次数。已有文档中的单样本时间只作对照。
3. 先验证完整 HTTP 锁竞争。持有 dataset `FOR UPDATE` 时，用真实鉴权覆盖首次限流桶场景，证明新准入的 POST 和状态 GET 可独立完成。没有达到目标时先修这一段，再继续接入客户端。

### 第二步 · 实现接收与 Worker

1. 新增迁移、迁移清单校验及 schema readiness，登记 API 与 Worker 的最小权限。
2. 实现状态读、接收事务、HTTP 端点和能力协商。上线前的默认模式使用旧入口，新能力由显式配置打开。
3. 抽出事实事务核心并加入单解析循环。用定向真实 PG 用例覆盖 UUID 并发去重、原文留存、崩溃重认领、fencing、删除竞争、旧入口及受限权限。此步完成以端到端出现真实 `accepted → committed` 为准。

### 第三步 · 接入 iOS 入口和恢复

1. 在详细读取之前接入 UUID 状态判断，加入新的 Outbox 状态和恢复分支，再接入原包重发。
2. 接入接收状态 UI 与分析 follow-up。按测试用例验证手动重复点击、自动恢复、已接收时被杀、旧状态升级和会话切换。
3. 用模拟器端到端确认分析结果来自现有真实发布路径。条件允许时，用已授权测试 iPhone、合成测试记录和本机隔离后端验证首次及重复同步。

### 第四步 · 完成门禁与记录证据

- 日常使用 `scripts/local/test.sh quick` 和定向用例。完成阶段运行 `scripts/local/test.sh gate`，覆盖 Go race、vet 和依赖边界。事务变更另运行 `scripts/local/test.sh integration ./integration -run 'TestWorkoutUUIDIngest|TestWorkoutDeleteAndImportBothLockOrders|TestWorkoutBundleV4ImportAndIdempotency'`，新集成用例统一使用 `TestWorkoutUUIDIngest` 前缀。
- iOS 运行 `scripts/local/test.sh swift` 与 `scripts/local/test.sh simulator`。如现有 `e2e` profile 未包含新路径，先把新用例纳入入口再运行；仅通过原有 `e2e` 用例不能作为本次闭环证据。
- 用普通构建和真实本机 HTTP 到隔离 PG 测性能，分开记录「请求体收完到接收回执」「点击到接收回执」「接收到入库」。正常代表包的第一项目标为 p95 < 300 ms，dataset 行锁竞争下完整 POST 的目标为 500 ms 内。记录样本大小、轮数、p50/p95 和失败数。点击到回执包含首次 HealthKit 读取，单列实测结果。
- 将真实运行结果、构建版本、证据位置与未验证项写入 `docs/IMPLEMENTATION.md`。本机门禁使用合成数据，不读生产库或记录健康值。代码、测试、迁移及发布在本方案文档交付时均处于待实施状态。

## 7. 必须通过的验收场景

| 场景 | 通过条件 |
|---|---|
| 首次同步 | 一次详细采集、一次文件准备、一次接收。接收时事实尚可为空，最终以事实事务和分析真实状态为准。 |
| 无变化重复点击 | 已接收 UUID 在发送前被识别。该 Workout 的明细 HealthKit 查询、组包与 POST 次数均为 0。 |
| 响应丢失或同 UUID 并发请求 | 始终只有一个原始快照和接收行，后续 POST 回原接收时间；只有一次事实投影生效。分别覆盖字节相同和字节不同。 |
| App 在接收前后被杀 | 复用原文件或按 UUID 查状态；已有接收记录时零重新组包、零新 POST。 |
| 原始字节 | 用含未知字段、特殊空白和键顺序的合成包，直接逐字节比对终态 `raw_json`。可识别事实照常入库。 |
| 删除、撤销和退役 | 接收后、事实提交前发生删除或会话失效时，Worker 不得写入该包的事实或新增分析 job。覆盖删除先于接收插入提交的交错，以及已入库后显式删除，确认原文清除与墓碑生效。 |
| Worker 崩溃、租约过期与事务故障 | 能重新认领，旧认领无法提交，事实与最终状态同生共死。重试耗尽能终结。 |
| 历史记录及权限 | 已有 Workout 直接显示已入库，跨 dataset 不可查，读设备不能上传，真实受限角色可以完成整条合法链路。 |

## 8. 启用与停用

使用三种运行模式。`legacy` 继续使用既有上传入口；`uuid` 对本次启用的 dataset 仅接受 UUID 模式的 Workout POST；`uuid_paused` 停收新 UUID，仍允许查状态、重放接收回执和处理已接收任务。旧同步上传在 `uuid` 和 `uuid_paused` 下对该 dataset 返回明确的升级或暂停错误。保留 v3 与管理删除路径。用入口隔离来保证两个不同的幂等规则不会同时作用于同一 dataset。

启用前先用 schema 50 兼容版本保持 `legacy`，确认 API 与 Worker ready。先在旧模式处理客户端未完成的 v3 包。切换时暂停并排空旧 Workout 上传，再为目标 dataset 启用 `uuid` 并启用新客户端。客户端一旦发过 UUID 请求，即使没拿到 200，也按该模式查状态或重试，避免超时触发旧入口重复上传。

停用时切到 `uuid_paused`，状态查询与 Worker 继续工作。需要回退应用版本时选择支持 schema 50、接收表及状态查询的构建；现有 schema 49 镜像无法直接接管。云端启用、旧入口切换和生产迁移另行授权。
