# RAI V2 Phase 2 Result

## Status

**PASS WITH RISKS**。核心 schema、批处理、事务和 job 基础已在 fnOS 的隔离 PostgreSQL 与 Compose 验证；路线发布、样本关联写入和 Workout metadata 补充协议仍属于 P3，不能将暂存视为完整同步。

## Repository and execution environment

仓库 `/Users/ta/rai`，`P2_BASE_COMMIT=c99d76f08438dcbc9603e018883339a3cd1d776d`，`main`。开始时工作区已存在大量未提交及未跟踪改动；本轮在其上增量实施，没有提交或清除它们。正式测试在 fnOS/N100，以一次性独立 PostgreSQL 18.6 容器与合成数据执行，不连接现有 `rai` 数据库。

## Schema and identities

本轮新增不可逆前向迁移 `00010_route_foundation.sql`，并更新迁移校验和与 schema readiness 版本。新注册的 stream、batch、样本和 Workout 保存采集设备 ID 并使用设备 FK；这表示负责提交的采集设备，不推断 HealthKit 事实的物理来源设备。旧行无可靠来源时保留 NULL，不做猜测性回填。API 数据库角色仅获路线暂存所需的列级权限，不可设置封口或回执；没有路线上传 API、Worker 路线处理或上线部署。

| 表 | 性质及用途 | identity、关系与索引 |
|---|---|---|
| `workout_samples` | 可迟到的样本关联 | 样本复合主键和样本、Workout 复合 FK；单样本最多关联一个 Workout；按 Workout 索引；父删除级联关联，不删除另一父事实 |
| `workout_routes` | 未发布路线暂存 | `(dataset_id, route_uuid)` 主键；复合 FK 指向 `workouts`，父删除级联；按 dataset、workout、route 索引；pending/sealed 与 receipt 一致性 CHECK。真正 seal 尚未实现 |
| `route_chunks` | 点块暂存 | `(dataset_id, route_uuid, chunk_index)` 主键及路线 FK；点数、JSON 数组长度和 SHA-256 长度约束；按块顺序唯一 |
| `route_points` | 点块的查询投影 | `(dataset_id, route_uuid, sequence)` 主键，复合 FK 指向块；时间索引 `(dataset_id, route_uuid, recorded_at, sequence)`；经纬度、精度、速度、方向与有限值 CHECK |

本轮没有新建平行的设备、批次或任务表：沿用 `datasets`、`devices`、`sync_streams`、`sync_batches`、`sync_pages`、`health_samples`、`workouts`、`entity_tombstones`、`entity_changes`、`change_events`、`dirty_scopes`、`jobs`、`job_attempts`。设备身份为 dataset + device UUID，批次为 dataset + batch UUID 且 stream/client sequence 唯一；Workout 为 dataset + HealthKit UUID，样本为 dataset + type + HealthKit UUID；同内容重试，冲突内容拒绝，删除屏障优先。没有无 UUID 样本的确定性键协议，不能声称支持。作业去重为 dataset + kind + scope + generation，相关实体通过 dataset FK 关联；scope 是逻辑键，不是多态实体 FK。

关键既有索引包括 `workouts_time`、`health_samples_type_time`、排队任务的部分索引 `jobs_ready` 和过期租约的部分索引 `jobs_expired`；设备、批次、Workout、样本和任务去重由主键或唯一约束支持。状态字段沿用 TEXT + CHECK，数据库命名为 snake_case、表名复数。

```text
datasets ─ devices / sync_streams ─ sync_batches / sync_pages
    ├── workouts ─ workout_routes ─ route_chunks ─ route_points
    ├── health_samples ─ workout_samples ─ workouts
    ├── entity_tombstones / entity_changes / change_events
    └── dirty_scopes ─ jobs ─ job_attempts
```

未关联样本可存储，`workout_samples` 允许之后关联；关系的业务写入协议和存储方法尚未实现。服务端不接收 HealthKit anchor；`sync_streams` 的 sequence/batch 指针不是 HealthKit anchor。Workout 主体不可变，迟到样本可独立提交，路线可晚于 Workout 暂存；metadata 补充事实协议仍未定义。

UUID 用于 dataset、设备、外部对象和 job 等身份，不把 sequence 用作下载水位；来源 UUID 与服务端生成的 job UUID 角色不同。绝对时间使用 `timestamptz`，Workout duration 用整数微秒，样本数值及路线坐标使用双精度并检查有限范围；样本类型与规范单位由既有白名单约束，低频来源 metadata 使用 JSONB。路线暂存坐标和 course 使用角度，altitude 与精度使用米，speed 使用米/秒；后续协议需明确缺失值和来源转换。尚未实现类型通用的单位转换，未知单位不得静默接受。路线 chunk 的 JSONB 是结构化点内容，SHA-256 由 storage 计算；点投影与块在同一暂存事务写入。只有 storage 路径保证两者一致，数据库自身尚不能验证投影对应内容，未来不能绕过该路径发布。事实长期保留；来源删除通过既有墓碑屏障，Workout 删除级联路线。没有自动清除原始数据；任务历史暂保留。

## Transaction invariants and job model

既有普通上传通过 dataset 行锁串行化，将事实、删除屏障、变更记录、dirty generation、job、最终回执及 stream 位置在同一事务提交。因此提交后的服务端 stream 位置不能越过未持久化的对应事实，事实与分析待办之间没有内存入队间隙；HealthKit delivered anchor 仍属于客户端后续协议。纯样本新增使用 pgx batch 流水化同一事务内的身份检查、事实和关联写入；混合删除或其他类型沿用已有路径。普通上传已有故障注入覆盖，不能推断所有其他路径也经过相同验收。

已有协议先单独提交不可变 `registered` 批次，再执行事实和最终回执事务：注册事务中插入 batch 后发生失败时，batch 和页预留一起回滚；已成功注册后的事实事务失败时，注册记录有意保留供重试，事实、stream 位置、日志、任务和最终回执回滚。两个边界不能混为一谈。

新路线方法只写 pending 暂存块与点投影，不产生路线发布、最终回执、变更日志或分析任务。它不是 durable route 语义：封口、manifest、删除屏障、重试原回执和原子发布仍需实现及验证后才能用于真实同步。暂存使用 dataset 行锁，按 `(dataset, route, chunk index)` 及内容摘要拒绝冲突，`COPY` 批量插入投影；每块最多 5,000 点是暂存实现限制，尚非已验收的 HealthKit 合同。

既有 jobs 使用 `FOR UPDATE SKIP LOCKED` 领取、60 秒租约、递增 fencing token、attempt 历史和过期回收。新增失败方法仅记录固定错误码，重试延迟 5 秒，最多 8 次后失败；失败更新和 attempt 结束同事务，过期/旧 token 无权更新。Worker 仍只回收租约，没有分析 handler，不伪造结果。未建立没有 P2 使用场景的 `analysis_runs`。

## fnOS integration tests

- 新专项在 PostgreSQL 18.6、真实隔离数据库、race 模式通过：两个并发 claim 恰一成功；过期后回收并重新领取，新 fencing token 拒绝旧 worker；失败重试八次终止；5,000 点暂存、同内容重试、内容冲突、跨块重复序号回滚；路线和样本关联 FK、唯一约束和父删除级联；5 个 1,000 条合成样本批次和幂等重试；事实、删除屏障、change log、job、receipt 的故障注入。
- 20 分钟、45 分钟和 4 小时合成 Workout 分别以 1 Hz 写入 1,200、2,700、14,400 个暂存路线点。全新隔离数据库迁移、删除并同名重建后，列、索引、约束、触发器和函数定义一致。
- 对 Workout identity、样本 identity、路线点顺序和任务 claim 执行 `EXPLAIN`，禁用顺序扫描以检查索引可用性；这不证明真实规模和默认 planner 参数下必然使用索引。首轮测试错误地只接受样本主键索引，实际选择另一个有效样本索引，修正后通过。
- 旧 schema 3–8 的升级专项通过，旧回执、事实和状态保留；新增设备字段对旧行保留 NULL，旧 stream 上新批次能记录当前采集设备。初次全量回归发现新应用 SQL 无法在旧 schema 上准备测试数据；改为在 schema 10 由数据库触发器绑定设备后，专项还发现原逐行快照把新增 NULL 列当作既有数据变化，已仅在升级比较中排除该列并显式验证未伪造来源，再次运行通过。
- 最新数据库迁移和服务代码在 fnOS 通过 gofmt、`go mod verify`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...` 和依赖边界检查（246 包）；`go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 通过，integration 包 133.484 秒。随后新增的 API 最小权限及路线暂存测试在同一类型的独立 PostgreSQL 上专项通过。
- 最终隔离 Compose 使用空卷启动 PostgreSQL、显式迁移两次、同一镜像启动 API/Worker，并在该实例的独立测试库通过队列竞争与恢复、重试、路线批量写入、注册回滚、重建、升级、样本批处理和受限 API 角色专项。还验证 readiness、数据库故障、不可逆 down 拒绝及 SIGTERM，项目容器、网络、卷和测试镜像已清理。
- 未验证路线 seal、路线删除屏障和路线发布事务，这些属于后续同步协议；批次注册回滚语义如上所述。

## P2 acceptance gate

| 检查 | 结果和边界 |
|---|---|
| `/Users/ta/rai`、fnOS、PostgreSQL 18 | 通过；独立测试容器，不连接既有库 |
| 空库迁移和删除后重建 | 通过；00001–00010，列、索引、约束、触发器及函数比较一致；迁移不可逆 |
| 设备、批次、stream、水位、Workout、样本、路线和 job 基础 | 通过；复用 `sync_streams` 和 `jobs`，路线仅 pending 暂存 |
| 唯一身份、幂等、FK、CHECK 和时间语义 | 通过所列合成集成测试；历史设备来源不可恢复，不支持没有稳定 UUID 的样本 |
| 样本和路线批量写入、迟到关系 | 通过纯新增样本批处理和路线点批量暂存；关系表支持独立迟到关联，业务写入协议在 P3 |
| cursor、事实、job 和回执事务 | 普通上传的注册回滚、事实/Workout 回滚及 job/回执故障注入通过；客户端 HealthKit anchor 不上传服务器 |
| 多 worker claim、重试和租约恢复 | 通过竞争、崩溃恢复、旧 token fencing、尝试上限、永久失败及 attempt 更新回滚测试 |
| 合成规模和查询计划 | 三种 Workout 时长、1,000 样本批次及累计 5,000 样本、5,000 点通过；`EXPLAIN` 证明索引可用而非生产计划 |
| Go 格式、测试、race、vet、build、边界 | 在 fnOS 通过；最终补充的受限角色测试独立重跑通过 |
| Compose | 空卷、迁移、API/Worker、同实例集成、停机和清理通过 |
| 阶段范围与依赖 | 未新增 SQLite 服务端、Redis、消息队列、TimescaleDB、PostGIS、HealthKit API、算法、训练计划或 iOS 业务代码 |

## Performance and sizing

最终 fnOS Compose 合成测试中，storage 暂存 5,000 路线点及其 JSONB 块和点投影约 352 ms；1,000 样本完整提交约 0.92 秒，5,000 样本按现有每批上限分五次完整提交约 5.02 秒，一次 job claim 约 11 ms。此前一次合成测试中，数据库内直接生成 5,000 路线点的事务约 117 ms，单个 Workout 完整提交约 42 ms。这是各一次观测，含不同工作量，不是吞吐或生产性能结论；单次 5,000 样本事务未实现或测量。

20 分钟、45 分钟和 4 小时分别为 1,200、2,700、14,400 秒；1 点/秒对应相同数量的点，1 点/5 秒分别为 240、540、2,880 点。例如每周三次 45 分钟、1 点/秒约 42 万点/年。假设每天 5,000 条健康样本约 183 万条/年；若每条含索引及存储开销粗估 200–600 字节，约 0.36–1.10 GB/年，加上路线块与点投影、WAL、膨胀、索引及备份会更大。该估算不是实测。当前单用户规模不证明分区必要；不引入分区、PostGIS 或 TimescaleDB。

## Deviations and risks

- 复用冻结合同中的现有逻辑表和客户端 anchor，而非建立平行 `sync_cursors` 或 `analysis_jobs` 表；保留不可变 Workout，不能提供任意 metadata upsert。
- 路线设计保留 P0 冻结的分块，并增加数值点投影；尚未实现传输协议。P2 新迁移无安全 down，与既有事实迁移相同，受控 down 不会删除它。
- 路线暂存没有完整发布、route tombstone 或一致性自检，不可作为已 durable 的路线使用；这些同步协议能力留待 P3。
- 样本关联目前只有数据库约束，缺少正式写入协议；纯新增样本才走批处理；多类型或删除使用既有逐条路径。新 batch SQL 与部分既有存储查询一样是手写 pgx，尚未转为 sqlc 查询。查询计划仅证明索引可用，性能是少量合成观测；真机与部署未验收。
- 历史事实、stream 和 batch 的采集设备为 NULL；新记录的设备表示提交者，不表示手表等 HealthKit 原始采集硬件。Worker 暂不执行分析，实际处理器的 claim、成功和失败日志随处理器实现验证。

## P3 Entry Gate

**P3 READY: YES**，允许下一阶段开始设计与实施 HealthKit Sync V2。此判断不代表路线 seal、路线删除、Workout metadata 补充、关联写入或真实 HealthKit 已实现或验收；这些必须在 P3 建立完整合同、事务边界和测试。本轮停止于 P2，不进入 P3。
