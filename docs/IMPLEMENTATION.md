# RAI V2 实施合同与实际状态

更新：2026-10-29（Asia/Shanghai）。工作仓库 `/Users/ta/rai`。本文件是实施主合同；[Phase 0 基线](V2_PHASE0_BASELINE.md) 规定领域、协议、状态、预算及禁止事项，[Phase 1 执行计划](V2_PHASE1_PLAN.md) 是本阶段执行入口。原始[架构方案](../RAI-Go-PostgreSQL架构重设计与实施方案.md)保留原文；本轮用户冻结的 P0–P7 路线与其旧阶段编号、兼容和迁移设想不一致时，按本合同实施，不修改原文或追求旧系统兼容。

## 目标与不变量

原生 iPhone SwiftUI 只读 HealthKit，WorkoutKit 排程另行授权；不建立独立 Watch、Web 或桌面客户端。Go 模块化单体提供 API 与异步 Worker，二者同镜像、不同进程；PostgreSQL 18 是服务端唯一主要事实库；Python 仅离线研究/训练，真正需要时才使用 ONNX 与 Go 推理。目标部署 N100 → PVE → fnOS VM（4 vCPU、8 GB）→ Docker。单用户优先正确性、简单、可维护、可观测，再考虑性能和扩展。

API 完成鉴权、验证、幂等及事务后 ACK，分析异步；原始事实、删除屏障、提交日志、分析待办和最终回执在同一事务提交，dataset 行锁产生提交顺序，不能用 sequence 当下载水位。原始事实保留已支持来源语义；derived 有版本、输入谱系且可重算。上传不可变、重试返回原回执、删除优先、旧上传不能复活 UUID；未知/不支持字段拒绝，不静默丢弃。日常同步不依赖备份、恢复、迁移、全库摘要或全量重算；管理工具不可被在线包导入。

当前数据生命周期目标是 RECEIVED → DURABLE → QUEUED → PROCESSING → READY；DURABLE 表示 PostgreSQL 提交完成，与分析结果无关。失败任务有界重试、租约和 fencing，不伪造完成。健康事实从手机当前可见的 HealthKit 历史首次导入后增量读取；不迁移旧库或沿用旧协议回执，见 [ADR-002](adr/002-fresh-healthkit-dataset.md)。

训练产品合同为 Generated Base Plan + User Override = Effective Plan → Execution → Workout Result → Coach Feedback。保留 override 来源和历史，重算不得静默覆盖；不建立平行手动计划。路线首版计划为有界 JSON/NDJSON 不可变分块、gzip 和 seal；不要求 PostGIS。后续能力仅在合同、实现和验收完成后声明，具体见基线。

## 当前实现与范围

当前工程不是空骨架：`apps/server` 为 Go 1.27.3 的 `rai/server`，已有 api/worker/migrate/init 单二进制模式、pgx/v5、sqlc、goose migrations 00001–00013、合成测试、开发 Compose、Dockerfile 和 CI。`/health`、`/livez` 检查进程存活，`/ready`、`/readyz` 检查数据库与 schema。开发 Compose 包含 PostgreSQL、API 和 Worker，启动前须显式执行 migration；fnOS 隔离验收见下。

已实现的服务端切片包括 v2 数量/睡眠类别样本、有界多活动 Workout、append-only Workout enrichment、sample↔Workout 迟到关联、超大 Workout 分块/seal、数量 series 分块/seal、manifest 绑定的 route 分块/原子 seal/删除屏障、页回执、持久请求预算和上传租约。当前握手声明 `routes=true`、`sampleWorkoutAssociations=true`、`workoutEnrichment=true`；`changefeed` 仍为 false；P4 的 `analysis` 为 true。详细身份、限额及可观察错误以 [P3 同步合同](V2_PHASE3_SYNC_CONTRACT.md)、[P2 同步合同](../contracts/sync-v2.md) 和 [OpenAPI](../contracts/openapi-v2.yaml) 为准。Workout 主体和样本内容不可变；迟到 metadata 只能追加有源版本事实，不能覆盖既有不同值。

当前 Worker 已消费 Workout、sample/series依赖与training-state任务；版本化run/results、原子publication、generation CAS、lease renewal/fencing、当前结果查询和受控重算已实现。Go纯算法与storage分离，参数不足/心率覆盖不足时返回unavailable；recovery/readiness只表达load-only模型，不假装使用睡眠/HRV。P4 fnOS验证与限制见末尾及 [P4 Result](V2_PHASE4_RESULT.md)。

P3.5 原生iPhone同步工程已经实现，但真实授权、HealthKit读取、后台与iPhone kill/GPS/E2E仍NOT VERIFIED。用户本轮授权将真机验收延期，允许P4开发入口例外；保留历史P3/P3.5结果。客户端仍使用原子文件outbox，没有服务端SQLite。完整事实下载与正式部署仍未实现或未验收；P5/P6已实现，真实状态见后文。本轮P7增量见末节。生产不依赖Node；独立OpenAPI lint仍使用Node。

## 阶段路线与出口

| 阶段 | 内容与退出标准 | 实际状态 |
|---|---|---|
| P0 | 冻结架构、领域、同步、Worker、风险与 P1 可执行计划 | 本轮文档冻结；P1 READY: YES |
| P1 | 基础设施：Go、API/Worker、PG、显式 migration、配置、日志、健康、Compose、优雅停止、fnOS 质量门禁 | 本轮状态见下方最新记录；远端 CI 不再是验收条件 |
| P2 | 核心领域、route/jobs/repository 和事务边界 | PASS WITH RISKS；核心模型、路线暂存与 fnOS 验证完成，路线发布协议留待 P3 |
| P3 | iOS HealthKit 增量、批次、幂等、删除、重试、迟到及 route | PASS WITH RISKS；服务端 fnOS 验证通过；P3.5 iOS 工程与 Swift 合成恢复联调已实现，真机 E2E 未完成；本轮用户授权延期，不再阻塞P4开发，历史P3.5 BLOCKED保留 |
| P4 | Go 异步分析、可重算结果、算法与输入版本 | PASS WITH RISKS；fnOS自动门禁通过，load-only模型与输入上限见P4 Result |
| P5 | 训练基础计划、override、effective、执行与反馈闭环 | PASS WITH RISKS，见P5 Result |
| P6 | iOS API、同步、分析查询、训练与执行集成 | PASS WITH RISKS；Debug/Release、两尺寸默认UI/无障碍/截图回归通过，GPU/真机风险见P6 Result |
| P7 | fnOS Docker、TLS、资源、可观测、PG 备份恢复、性能和发布准备 | 本轮实施与最终门禁见P7 Result；不自动部署、切入口或迁移真实数据 |

旧文档/README 中旧 P0–P8 编号是历史表述；这里采用新 P0–P7，不能据编号推断既有工作消失或重复实施。详细责任、逻辑身份、路线比较、目标 SLO、资源预算及 Do Not Build 见 [基线](V2_PHASE0_BASELINE.md)。

## 证据与未验证项

历史记录报告 fnOS/N100 Linux、一次性隔离 PostgreSQL 18.6 和合成数据的 race 集成回归通过，scale 测试通过；机器可读结果见 [fnOS 证据](evidence/fnos-remote-tests-2026-10-27.json) 及 [server 证据](evidence/server-tests-2026-10-27.json)。Workout 包 20,000 合成细节和 series 100 万点有专项证据；series 首轮全量流式核验曾触发 statement timeout，改用逐块读取后通过，不能推断全量流式读取已达标。其他测试证据保留在 `docs/evidence/`。历史结果不代表本轮重跑、不证明生产吞吐、真实 HealthKit、断电恢复、实际多副本或部署通过。

## P1 本轮证据（2026-10-27）

本轮按用户要求在 fnOS/N100 执行 Go 和 Docker 验证，未在本机运行 Go 或 Docker。API/Worker 启动前检查数据库连接，连接失败非零退出；Worker 仍只回收过期租约。`/health` 和 `/ready` 分别与 `/livez`、`/readyz` 共用处理逻辑。没有修改 00001–00009 或现有同步合同；新增健康路径已补入 OpenAPI。

- `scripts/test-remote.sh` 在一次性 PostgreSQL 18.6 容器上运行：`gofmt` 检查、`go mod verify`、`go test ./...`、`go test -race ./...`、`go vet ./...`、API/Worker 构建和 `python3 ../../scripts/check-boundaries.py` 均通过（边界检查 246 个包）。`go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 通过，integration 包 101.145 秒；新测试覆盖两组健康路径的正常、schema 不匹配、数据库不可用及重复迁移。测试使用合成数据、独立容器和随机临时库。远端默认 Go 模块代理首次超时，改用 `https://goproxy.cn,direct` 后通过。
- `scripts/test-remote.sh bash ../../scripts/test-p1-compose.sh` 在唯一 Compose 项目和空卷上运行。`docker compose config --quiet` 通过；PostgreSQL 18.6 容器健康；同一 API 镜像显式运行 `migrate` 两次，再启动 API 和 Worker。测试确认两者镜像 ID 相同、Worker 实际连接数据库；四个端点正常，schema 不匹配和数据库停止时两个 ready 路径返回 503，liveness 保持 200；API/Worker 数据库连接失败均非零退出，SIGTERM 后退出码均为 0。项目容器、网络和该次卷均删除；测试镜像删除。Go 构建镜像及 PostgreSQL 使用 `docker.m.daocloud.io` 上与默认配置相同的固定摘要。首次构建因 fnOS Docker 镜像代理 401 失败，切换镜像源后通过。
- fnOS 上运行 `redocly lint contracts/openapi-v2.yaml` 通过，报告 7 条非阻断警告：四个健康操作缺少 4XX 响应，以及三个未使用的 schema。该检查属于独立 Node 合同工作流。
- fnOS 原有 `rai-postgres` 和 `frpc` 保持运行；未连接现有 `rai` 数据库，也未使用 `init`、导入真实数据、部署、切换入口或修改 `../running`。测试前工作区已有 `docs/FNOS-ACCESS.md`、`docs/evidence/fnos-remote-tests-2026-10-27.json` 和 `scripts/test-remote.sh` 的未提交修改，本轮保留。

当时远端 CI 尚未运行，且原计划将其列为门槛；用户后续已明确取消该门槛，最新状态见下。P1 服务 CI 不使用 Node；原 OpenAPI lint 保留在单独的 `contracts.yml` 工作流，仍使用 Node，不能声称仓库完全不含 Node。开发 Compose 凭证和资源预算仅适用于隔离开发，不构成生产权限、性能、真机、备份恢复或部署验收。P7 的这些验收仍待完成。

## P2 前期审查与增量（历史记录，2026-10-27）

本轮不配置 GitHub 或 CI，也不将 GitHub CI 作为 P2 阻塞项；上述 P1 历史记录保留，不代表本轮验收要求。实施前工作区已有未提交修改，本轮在其基础上增量工作，未修改旧 `running` 工程或 migration 00001–00009。

| 能力 | 状态 | 代码与测试证据 | 尚需修改或验证 |
|---|---|---|---|
| Dataset、样本、睡眠类别、Workout、series、上传与回执 | PARTIAL | `internal/syncing`、`internal/storage/postgres`、00001–00009；已有合成集成测试 | 领域覆盖与 P2 全部场景尚未完成验收 |
| Repository 与事务 | PARTIAL | HTTP 依赖 `syncing.Store`，PostgreSQL 存储层拥有事务；`CommitUpload`、series 和 Workout package seal 锁 dataset 并提交对应事实、日志、任务和回执 | 本轮合成故障注入覆盖普通批次的 fact、tombstone、change log、job、receipt；其他路径与全部并发组合仍待验证 |
| revision 与通知分页 | PARTIAL | `change_events` 主键 `(dataset_id,commit_rev,ordinal)`；新增 `GET /v2/sync/changes` 按该键分页、校验 dataset/epoch/实际游标、支持空页和续读；真实 PG 测试覆盖跨同 revision 分页与隔离 | 只返回标识和操作，不提供完整事实和分块读取；`changefeed` 仍为 false，不作为 P3 完整下载能力 |
| Route | MISSING | 握手为 false；无 route 分块与 seal 实现 | 实施登记、分块、完整性校验、读取、删除屏障及合成故障测试 |
| Jobs / lease / fencing | PARTIAL | `internal/storage/postgres/jobs.go` 已有 claim、续租、过期回收和完成时 fencing；Worker 仅回收 | 缺 lease owner、完整失败重试和可注入处理器；不消费分析任务、不伪造 READY |
| Workout metadata supplement | MISSING | 原始 Workout 不可变，合同不支持迟到 metadata 变更 | 正式协议、不可变补充事实、删除规则和测试 |
| P2 全部故障、恢复和性能 | NOT VERIFIED | 本轮专项结果见下；既有历史记录不能代替本轮验收 | route、job 竞争/崩溃、API/Worker 重启等未完整验证；关键查询实际负载计划及锁争用未验收 |

本轮新增的通知接口无 schema migration，未开放新的握手能力。新通知查询目前与现有部分存储代码一样使用显式手写 SQL，尚未加入 sqlc 查询和生成代码，需在 P2 继续处理。查询使用既有复合主键的范围和顺序；合成测试在禁用顺序扫描时用 `EXPLAIN` 确认该索引可用，但不能证明真实数据规模下优化器一定选择索引，也没有测量生产延迟。通知不含健康事实载荷，客户端不能据此恢复大 Workout 或 series 的内容。

通过 fnOS 一次性隔离 PostgreSQL 18.6 容器、本轮合成数据执行 `go test -race -count=1 -tags=integration -p 1 -run '^TestChangeNotificationsPagingAndIsolation$' -timeout=10m ./integration` 和 `go test -race -count=1 -tags=integration -p 1 -run '^TestCommitFailureInjectionAtomicity$' -timeout=10m ./integration`。前者覆盖跨页、空页、cursor 校验、dataset 隔离、HTTP 参数和索引可用性；后者通过触发器逐项制造五类普通上传失败，检查业务表快照无变化并验证重试。首次通知专项编译因测试中的未使用导入失败，修正后通过。未读取既有 `rai` 库或真实健康数据。

当时的代码在 fnOS 一次性隔离容器上再次通过 `gofmt` 检查、`go mod verify`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`python3 ../../scripts/check-boundaries.py` 和 `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...`；真实 PostgreSQL 集成包用时 105.366 秒。OpenAPI 使用 `npm exec --yes --package=@redocly/cli@2.71.1 -- redocly lint contracts/openapi-v2.yaml` 校验通过，仍有四个健康接口缺少 4XX 与三个未使用 schema 的七条既有警告。未进行生产数据、部署、真机或目标负载验收。当时为 P2 COMPLETE: NO；P3 READY: NO，最新状态见后文。

## P1 基础设施本轮复核（2026-10-27）

本轮基线 `c99d76f08438dcbc9603e018883339a3cd1d776d`，保留已有未提交改动、现有业务和冻结 migration。新增配置及生命周期测试、数据库 URL/HTTP 地址校验、安全的启动错误分类、迁移状态命令、开发环境模板及连接池选项；容器探针改为进程存活检查，ready 仍单独检查数据库和 schema。开发 PG 使用 UTF-8、UTC 和回环端口；没有新增业务 schema 或分析处理。测试脚本不传输 `.env`、秘密目录或临时目录。

经用户指示，在 fnOS 的独立临时目录、一次性 PostgreSQL 18.6 容器和合成数据上，本轮通过 `gofmt`、`go mod verify`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...` 和在线依赖边界检查（246 包）；完整 `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 通过，集成包 105.534 秒。随后仅增加连接池单元测试，再对最终代码重跑上述非集成质量门禁通过。隔离 Compose 测试验证空卷、UTF-8/UTC、两次 up、9 项 applied 状态、相同镜像、Worker 连接、健康和 readiness 的正常及故障语义、连接失败非零和两进程 SIGTERM 退出；测试项目、卷与测试镜像已清理。

当时状态为 **BLOCKED，P2 READY: NO**：冻结的业务 migration 中多项明确无安全 down；原 P1 计划还要求远端 CI。后续用户明确取消 CI 门槛，并要求处理回退安全，最新结果见下及 [Phase 1 结果](V2_PHASE1_RESULT.md)。未验证生产部署、真机、断电恢复或性能。

## P1 migration 安全回退补充（2026-10-27）

新增 `rai migrate-down`，仅允许已逐项审核、完整文件哈希匹配且具有非空 Down SQL 的最高已应用迁移回退；RAI up/down 命令通过 PG advisory lock 串行化。现有 00001–00009 不可逆，生产白名单为空：对它们请求 down 必须非零退出并保留数据及版本。未来按需审核和测试新迁移的回退；既有事实 schema 的修复走经过审查的前向迁移，不能宣称旧事实可逆。外部迁移工具不参与该锁，禁止并发执行。

fnOS 一次性 PostgreSQL 18.6 上验证了独立合成 migration 的 up/down/up、并发 down 串行化，以及拒绝未批准、哈希不匹配、未知版本和对现有不可逆迁移的回退；合成事实和版本保持不变。初次专项运行发生 PostgreSQL 容器初始化时连接重置；测试脚本和开发 Compose 的 PG 健康检查改为等待最终 TCP 监听后，专项与完整回归通过。最终工作区通过 gofmt、依赖校验、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`、依赖边界检查，以及真实隔离 PostgreSQL 的完整 race 集成（integration 包 106.364 秒）。隔离 Compose 再次通过，实际命令拒绝不可逆 down，9 项状态保持 applied，容器和卷已清理。

用户确认 macOS 开发、fnOS 测试、Gitee 管理代码，远端 CI 不属于 P1 门槛；GitHub/CL 待开发基本完成后配置，本轮未配置或运行。**P1: PASS WITH RISKS；P2 READY: YES**，风险和未验证项详见结果文档；没有进入 P2。

## P2 Core Data Model 结果（2026-10-27）

**P2: PASS WITH RISKS；P3 READY: YES。** 本轮基线 `c99d76f08438dcbc9603e018883339a3cd1d776d`，在已有未提交工作上增量实施；完整证据、设计取舍、限制与性能观测见 [Phase 2 结果](V2_PHASE2_RESULT.md)。旧记录保留为历史，不代表其未验证项自动通过。

新增前向迁移 00010：为新 stream、batch 和新事实保存有 FK 的采集设备，旧行不猜测回填；建立 `workout_samples` 关联结构及 `workout_routes`、`route_chunks`、`route_points` 的未发布暂存结构，增补 job 非负约束。样本新增批次通过 pgx batch 流水化既有事务；路线点和块以单事务批量暂存、重试验摘要，受限 API 数据库角色不能直接封口。任务队列增加带 fencing 的有界失败重试；Worker 仍不消费或完成分析。已有 batch 注册和事实提交是两个明确事务：注册事务失败回滚页和 batch；已注册后的事实失败保留注册但回滚事实、stream 位置、日志、任务和最终回执。

fnOS 上的 PostgreSQL 18.6 一次性隔离容器与合成数据通过完整 race 集成（integration 包 133.484 秒）、gofmt、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...`、依赖边界检查；最终空卷 Compose 验证迁移、同镜像 API/Worker、同实例隔离集成、故障、停止与清理。两次全量/升级专项中分别发现旧 schema 注册 SQL 不兼容和新增 NULL 列被快照误判，修正后重跑通过。未连接现有 `rai` 数据库，未部署或迁移真实数据。

P3 可开始，但不能宣称路线已发布：路线 seal、删除屏障、完整回执和原子发布，以及样本关联的写入协议、Workout metadata 的独立补充事实和冲突/删除规则仍须在 P3 定义并验收；现有路线暂存不得代替这些能力。老数据的设备来源未知，新增记录只标记采集设备而不推断 HealthKit 物理源。真实 HealthKit、生产负载、真机、断电恢复和部署仍未验证。详细时序、有限测试样本、手写 SQL 和其他风险见结果文档；不进入 P3。

## P3 HealthKit Sync V2 结果（2026-10-27）

**P3: PASS WITH RISKS；P4 READY: NO。** 完整合同与证据见 [Phase 3 同步合同](V2_PHASE3_SYNC_CONTRACT.md) 和 [Phase 3 结果](V2_PHASE3_RESULT.md)。本轮基线 `67ff445061fc62895fb34d80d2bc45c5571fab05`，工作区开始时干净。

- 新增不可逆迁移 `00011_p3_sync_v2.sql`：route manifest/counts、chunk wire hash、双摘要链、sealed 计数、projection trigger、受控 append/seal/delete 函数、`route_deletions`、`workout_enrichments` 和 `workout_samples` 关联 idempotency 字段。`readyz` 要求 schema 11；旧 schema fail closed。
- 新增 `POST/GET /v2/routes`、`PUT /v2/routes/{id}/chunks/{index}`、`POST /v2/routes/{id}/seal`、`POST /v2/routes/{id}/delete`、`POST /v2/sync/associations`、`POST/GET /v2/sync/workout-enrichments`。P3 `protocolVersion` 保持 2；未知版本返回 `unsupported_protocol`，已知错误单位返回 `unsupported_unit`。
- 普通 batch、Workout package、series 的既有回执/序号语义未改。route chunk 为 staging；只有 seal 事务写 route 发布 receipt、dataset revision、Workout dirty scope、analysis job 和 change event。sealed 后新 chunk/内容拒绝，已提交 chunk 的精确重试返回原 receipt。
- API 数据库角色不能直接写 route chunk/point、不能直接设置 route sealed/receipt、不能直接删除 route；append/seal/delete 通过 `SECURITY DEFINER` 函数执行，projection 由触发器从 canonical chunk JSON 生成；集成测试验证直接路径被拒绝。
- fnOS 上完整 `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 通过，integration 包 158.534 秒；Compose P3 E2E 通过：worker 离线仍 durable ACK、API 重启和 PostgreSQL 重启后同 batch 返回原 receipt、route seal、association、enrichment。OpenAPI lint 通过（8 条非阻断 warning）。
- 合成性能基线：1000 samples 单批 total 约 362 ms、p95 total 约 946 ms（8 次，fnOS N100，storage+parse/register 口径）；2000 samples 分两批 commit 约 741 ms；5000 route points chunk 约 199 ms、seal 约 14 ms。均为单次/小样本观测，不是生产 SLO。
- **当时未验证/风险**：P3 服务端阶段尚无 iOS 工程；真实 iPhone 查询、anchor/outbox、后台/强杀恢复和真实 HealthKit 数据未执行。P3.5 最新实现与验证见下；历史 20/45 分钟和 4 小时记录为合成规模测试。

## P3.5 Client Closure（2026-10-27）

**P3.5: BLOCKED；P3: PASS WITH RISKS；P4 READY: NO。** 用户明确本轮暂时不能提供真机；唯一阶段出口阻塞是真实 iPhone 端到端验收未执行。未开始 P4。完整记录见 [P3 结果的 Client Closure](V2_PHASE3_RESULT.md#client-closure) 与 [脱敏证据](evidence/p35-ios-2026-10-27.json)。

`P35_BASE_COMMIT=67ff445061fc62895fb34d80d2bc45c5571fab05`；开始时已有 P3 的 21 个 tracked 修改和 16 个 untracked 文件。原服务端、migration、OpenAPI、同步合同和旧证据逐文件哈希保持不变；仅更新本主合同和 P3 结果的当前状态。P3.5 新增 `ios/RAI`、合成验收工具及证据，远端同步脚本仅增加 iOS 构建缓存/私有开发配置排除。未提交、推送、部署或接触既有 fnOS `rai` 库。

macOS/Xcode 的最终 Swift/iOS 测试、iPhone target 构建结果和 fnOS 回归统计以证据文件为准。Swift SyncCore 的 macOS 合成进程实际经历两次 SIGKILL：batch COMMIT 后响应丢失，以及第 3 个 route chunk COMMIT 后响应丢失；重启使用原身份/文件恢复，最终 SQL 核验 1 sample、1 Workout、1 association、1 enrichment、1 sealed route / 6 chunks / 5,001 points，5 个 durable jobs，Worker 未启动。此链路是 Swift test harness → SSH loopback tunnel → fnOS API → 隔离 PostgreSQL，**不是 iPhone/HealthKit E2E**。既有 Compose P3 E2E 本轮重跑验证 API/PG restart 和 Worker offline。

真实 20/45 分钟 Workout、实际 GPS、真实权限/删除、iPhone kill/网络中断、后台 URLSession 系统恢复、内存峰值及真机分阶段耗时仍未验证。已实现统计采集入口，但不得用合成测试耗时填写真机性能。终态本地载荷/回执暂保留，长期存储增长需要后续实际测量。签名、provisioning 与安装不以未签名 target build 代替。

## P3.5 本轮复核（2026-10-27）

本轮 `P35_BASE_COMMIT=537b526561d8d9667c0ccafb91928bafd573f25e`，`main`，开始时工作区干净。上述首次实施时的 P3 dirty worktree 和 iOS 工程均已包含在该提交内，不能再计为本轮新增；本轮未创建提交。服务端、migrations、部署配置、OpenAPI、同步合同和 P3 fnOS 证据相对该基线没有差异。

复核发现 Swift `Envelope.validateReceipt` 只检查 `counts`/`analysisJobIDs` 非 null，未验证完整计数、分析状态及类型。新增回归用例在修复前实际复现错误回执推进 sequence；现按冻结 DTO 检查五类非负整数计数及总量上限、非负 revision、合法 analysisStatus 和 job UUID。服务端按实体合并重复事件，因此允许回执计数少于 eventCount。错误回执不能推进 cursor、sequence 或 delivered anchor；原候选 anchor/批次仍可在重启后用正确回执恢复。未改变线上协议或服务端。

本轮 Swift 核心 27 项通过；108 个 route 向量由当前 Go 冻结实现重新生成，与 Swift fixture 一致。Swift/fnOS 合成恢复链路再次通过两次 macOS SIGKILL、提交后抑制响应、原 batch 回执恢复、原 route 第 3 块重传、6 块/5,001 点 seal、迟到关联和 enrichment；隔离 SQL 数量及 5 个 durable jobs 与首次验收一致。隔离 Compose 再次通过 API/PG restart 和 Worker offline。fnOS 普通 149、race 149、integration/race 279 项通过，gofmt、vet、build、模块校验和 246 包依赖边界通过。构建、模拟器测试和逐项 gate 的最终结果见[证据的 latest_revalidation 字段](evidence/p35-ios-2026-10-27.json)。临时测试容器和 Compose 卷已核实清理。

本轮 Xcode 识别到已配对 iPhone，但设备为 `unavailable`；用户随后确认“本轮仍无可用 iPhone，完成代码与自动验收即可”。未安装 App、读取 HealthKit 或执行真实网络验收。唯一阶段出口阻塞仍为可用真实 iPhone 的 HealthKit→fnOS→ACK 及设备恢复验收；签名安装、真实 20/45 分钟 Workout/GPS、后台恢复及性能均未验证。**P3.5: BLOCKED；P3: PASS WITH RISKS；P4 READY: NO。** 原架构方案、旧 `running` 和生产入口未修改，未开始 P4。


## P4 开发入口例外与实现（2026-10-28）

`P4_BASE_COMMIT=8e40573e027ac6dd9aac871fb755051cb31c4657`，`main`，开始时 tracked/untracked 均为0。
**P3 REAL DEVICE ACCEPTANCE: DEFERRED BY USER / NOT VERIFIED。P4 DEVELOPMENT EXCEPTION: AUTHORIZED BY USER。** 前文 P3/P3.5 的 BLOCKED、P4 READY:NO 是当时真实历史，不修改成虚构真机 PASS；本次按用户明确入口例外实施 P4。

新增 schema12、analysis domain、真实 Worker handler、输入快照/hash/lineage、参数版本、不可变 runs/results、原子 publication、generation CAS、删除隐藏、current query 与管理 recompute。Workout Features → TRIMP → fitness/fatigue → load-only recovery/readiness 按顺序实现。没有可靠个人参数或心率覆盖时不提供 TRIMP，也不伪造上层评分。只在已有队列中消费、续租、重试和结束，未引入新运行依赖或 P5。

P3 输入正确性 blocker 的证据与最小修复见 [算法合同](P4_ALGORITHM_CONTRACT.md)：删除 sample 关联需要标脏 Workout，sealed series 需要传播依赖；sample 新增批次按 type/UTC 日聚合任务。上传身份、sequence、seal 和 durable receipt 合同保持，旧回执不重写。00001–00011 checksum 保持。

完整本轮验证、性能/容量观测及未验证项以 [P4 Result](V2_PHASE4_RESULT.md) 为准。真机、生产部署、真实数据迁移和 P5 均未执行。没有 commit、push、入口切换或修改旧 running。

本轮最终：**P4 STATUS: PASS WITH RISKS；P5 DEVELOPMENT READY: YES。P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED。** 停止于P4。


## P5 Training Plan / Coach Closed Loop（2026-10-28）

`P5_BASE_COMMIT=8e40573e027ac6dd9aac871fb755051cb31c4657`，`main`；本轮在既有未提交P4工作上增量实现，未创建commit/push。旧P3/P3.5/P4验收历史与原方案保留。**P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED。** 本轮用户明确允许P5开发，不能据此推导真机通过。

P5新增schema13、纯training domain、versioned generator/parameters、stable session、persistent field overrides、immutable materialized Effective、generation runs/publication、execution snapshot、explicit late Workout association、versioned planned-vs-actual feedback和同队列recalculation。用户edit/move/add/skip/delete/revoke与乐观锁、overrideID及commandID独立幂等已实现；重算发布在dataset锁内合并最新override。新目标可归档旧Plan而保留历史。P5未修改00001–00012，ready严格要求13。

真实P4 publication→low readiness→新Base→用户7km与改期保留、未编辑长跑距离更新→frozen execution→late association/late analysis→feedback→再重算已由PostgreSQL和HTTP/Worker验收。并发双客户端、edit vs generation、旧generation/fencing、lease事务内过期、Base/Effective/publication/job/override/feedback故障回滚、fresh及schema12升级/重复迁移、角色边界和历史重放均通过。实现中实际发现并修复取整增长边界、anchor aliasing、override identity幂等、冻结timezone兼容和受限角色权限；失败轮不记PASS。

最终fnOS完整race PostgreSQL集成386.281秒通过，含P3/P4回归；最后只读timezone过滤补丁后又通过全部P5真实PG race（90.441秒）、race单元、vet/build/依赖边界。最终源码再跑gofmt、普通/race、vet（含integration/scale）、build与边界通过；sqlc生成与重复哈希校验通过。隔离Compose通过P3离线Worker/重启回执、P4真实Worker/current API、P5 HTTP闭环和SIGTERM；OpenAPI lint通过，8条旧warning保留。

最终独立non-race容量/性能24.336秒：52周/3年合成历史364/1095 Workout，100 override，154/258 Effective；16周112 slots。生成8/16周分别100次，其他查询/merge/recalculation为单次storage观测；测试harness排除debounce/poll等待，非HTTP或iPhone延迟，非生产SLO。具体数字、源码哈希与验收序列见 [P5 Result](V2_PHASE5_RESULT.md)、[Coach Contract](P5_COACH_CONTRACT.md)、[fnOS evidence](evidence/p5-fnos-2026-10-28.json) 和 [baseline](evidence/p5-baseline-2026-10-28.json)。

P5为backend-only；未实现P6完整UI、WorkoutKit、Watch、LLM或P7部署。透明规则与load-only readiness/recovery的个体效度、目标时间优化/taper、长期retention、真实iPhone、生产TLS/secret/备份恢复/断电仍未验证。没有连接既有rai数据库、迁移真实健康数据或修改旧running。

**P5 STATUS: PASS WITH RISKS；P6 DEVELOPMENT READY: YES。P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED。** P6 gate只表示P5闭环自动验收成立，按STOP RULE停止于P5，不开始P6。


## P6 Native Product Integration（2026-10-28）

P6_BASE_COMMIT=`62511f9b3e5998ccad79396a7e14540074e506e5`，main，入口干净；P4/P5已在基线。新增原生五Tab、统一Liquid Glass/Material、P4Published/P5Effective读取、训练命令journal、字段调整/冲突/ACK重试、frozen执行与显式Workout关联/Feedback；P3诊断移至我的。新增只读Workout/overview/history，无新migration、P3 ingestion变更或第二套算法/计划。原方案和历史evidence保留。

真实Swift→fnOS隔离PG通过Home读取、原命令ACK重放、409草稿、override跨新Base保留、未修改字段更新、snapshot与Feedback。fnOS普通/race/vet/build、完整PG race（恢复空间后integration302.488秒；初轮311.846秒保留）、249包边界、Compose P3/P4/P5回归、OpenAPI通过。Swift核心35项通过，含原P3同步27项；8项原生Normalizer测试及两尺寸默认UI通过，五页面独立截图比对通过。具体证据见[P6 Result](V2_PHASE6_RESULT.md)、[产品合同](P6_IOS_PRODUCT_CONTRACT.md)与[evidence](evidence/p6-ios-2026-10-28.json)。

磁盘空间已由用户恢复，最终Debug/Release和Swift 6 complete strict concurrency构建通过。独立Hero摄影素材已打包，来源/提示词有记录；导航、生成成功状态、44pt操作区、同步图标和大字号反馈布局的问题已修正。两尺寸完整无障碍专项通过，大屏最终审计为避免宿主照片分析占满CPU而临时隔离该自建模拟器的后台进程；条件和历史失败均保留。真实滚动位置断言、3次时钟/内存、API/解码/状态发布/ACK和CPU采样已记录。宿主没有Metal设备：Material/同值Shape回退可运行，系统Tab及滚动合成存在异常，GPU视觉/帧率及iOS18运行不声称通过。未触碰生产/旧running。

P6 DEVELOPMENT STATUS: PASS WITH RISKS；P7 DEVELOPMENT READY: YES（仅开发入口，未开始P7）；P3/P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED；DEVICE TEST: NOT RUN；PRODUCTION RELEASE READY: NO。

## P7 Production Hardening / Operations（2026-10-29）

**P7 ENGINEERING STATUS: PASS WITH RISKS；DEPLOYMENT PREPARATION: PARTIAL；PRODUCTION RELEASE READY: NO。** 本节是P7最新状态，不将上文历史的“未开始P7”改写成已验收。基线 `d11e7b29e339b3d33c84962611031a3f33a18a7c`，`main`，入口干净，P6已由用户提交；本轮为未提交增量。原方案、00001–00013、P3/P4/P5/P6合同与历史evidence逐字节保持；本轮新增证据见 [P7 Result](V2_PHASE7_RESULT.md)、[fnOS机器验收](evidence/p7-fnos-2026-10-29.json) 与 [逐表恢复](evidence/p7-restore-2026-10-29.json)。

- P7-A/B：新release Compose维持PG/API/Worker同镜像，Go原生TLS、CA/hostname验证、PG18正确卷路径、默认回环/internal网络、显式迁移、readiness、非root/read-only、资源/日志上限与graceful shutdown。独立owner/API/Worker/backup/monitor身份、随机文件secrets与运行角色检查；真实LOGIN越权42501、跨dataset404、撤销401、Route受控投影边界验证。未为iOS放宽ATS或证书信任。
- P7-C：只读整库custom dump→GPG AES256→另一独立新凭证PG实际恢复。45表计数/hash、schema13/FK/屏障/stream/receipt/publication/Effective/execution及原身份API核验通过；错key、损坏密文/替换metadata hash、未标记/非空目标拒绝且无部分写入。小库实测耗时与RPO24h/RTO60min设计目标分开；离机目的地/取回、密钥恢复与定时备份未验收。
- P7-D：显式retention dry-run，不删除服务端facts/tombstones/receipts/active jobs或immutable history；无安全replay horizon的GC延期。仅iOS新完成页，在ACK/全部operations/封页/anchor同一state提交后mark→引用核验→有界回收；未知orphan/取消页/历史文件保留。Swift状态失败/缺载荷/重试/有界GC及macOS真实SIGKILL恢复通过，不等同iPhone强杀。
- P7-E：规范路由日志避免原始URL泄漏，sync durable commit与Worker claim/processing/publication可观察；只读聚合snapshot补队列/lease/lag/训练/DB大小锁，宿主探针补TLS/进程/磁盘/备份，11类告警及真实健康实例通过。历史failed在新代际clean覆盖后解除当前告警，原审计不变；未安装schedule或外部通知。
- P7-F/G：API/Worker/PG实际SIGKILL、真实60s lease恢复、网络断连、三轮SIGTERM/重启、独立256MiB tmpfs ENOSPC和P3/P4/P5原子故障回归通过。1/3/5年低密度合成容量及正式非空P3上传、长Route、大Workout、百万series、默认EXPLAIN、CPU/内存/WAL/磁盘和约3分钟soak有明确口径；不当作多年稳定或真机吞吐。
- P7-H：deployment/security/backup/operations/capacity/release与result七份文档及版本Manifest、预检/回退边界完成；正式registry digest、入口/PKI/secrets/离机备份/通知与生产规模RPO/RTO尚未安装或验收，因此发布准备保守记PARTIAL。

最终fnOS Go普通183项、race183项、完整真实PG/race命令共363个test pass事件（含单元及子测试），失败/跳过为0；integration包336.189s。gofmt、mod verify、vet、build、249包在线依赖边界与纯analysis/training边界通过，integration/scale/p7capacity标签vet通过。容量与故障演练命令、样本、量化结果、镜像ID和源码摘要保存在P7机器证据。Swift39 tests、Release/min18未签名严格并发构建、独立iOS26.5模拟器8项Normalizer回归通过；未新增UI或WorkoutKit功能。

实际差异与失败轮均记录在P7 Result：修复Worker缺goose表权限、原始path日志风险、历史失败告警；测试用ANALYZE预算/secret权限/端口与注入时序问题经重跑，不把失败轮计PASS。remote-test现验证本轮容器label/ID并rm -fv自建匿名卷；无法确认归属的旧卷不prune。线上同步仍不导入admin/migration/备份/全库摘要，不改变事实/回执事务或P4/P5发布算法。

**未验证项**：P3/P6 REAL DEVICE ACCEPTANCE仍DEFERRED / NOT VERIFIED；真实HealthKit/anchor/GPS/background URLSession/iPhone force-quit/网络/训练执行、iOS18 runtime、12ProMax性能、Liquid Glass GPU、WorkoutKit排程继续延期。真实入口/TLS/防火墙/凭证、离机与密钥取回、定时任务/通知、生产规模/长期vacuum/bloat与断电硬件恢复未验收。恢复旧快照会丢失之后ACK事实并可能恢复旧撤销状态，手机anchor/凭证灾难恢复方案是正式切换门禁。未读取真实数据库/健康值，未改旧工程/入口，未迁移、部署、开放公网、发App、commit或push；完成P7后停止。

## Monthly Cloud Backup / HealthKit Disaster Recovery 专项（2026-10-29）

`RECOVERY_BASE_COMMIT=09fb5bb38d32cdbe42c757277ab2431995818ac4`，`main`，入口干净，P7已包含在基线。本专项不是P8；原始方案、00001–00013、P3/P4/P5/P6冻结合同、P0–P7历史结果及证据保留。当前改动未commit/push。

**MONTHLY BACKUP IMPLEMENTATION: PASS WITH RISKS；CLOUD BACKUP INSTALLATION: PENDING CONFIGURATION；DISASTER RECOVERY RESYNC: BLOCKED。** 新月度入口复用P7只读整库custom dump，在不额外加密文件的前提下，通过严格SSH上传，远端大小/摘要重读核验、manifest原子发布后保留三份成功副本；双端文件锁、0700/0600、NOFOLLOW和仅受控身份清理。只准备cron每月1日03:00/Asia-Shanghai配置及错时保护，不安装正式任务；旧P7加密工具保持可用。

真实fnOS合成loopback OpenSSH已完成4次备份/最终3份/最旧移除，并在SSH失败和TCP超时后验证旧集合不变；2,673,852字节的实际归档在另一全新凭证/空卷PG18中完成单事务restore，45表计数/SHA完全一致、schema13、FK/不变量、原receipt/stream/tombstone/P4publication/P5Effective/override/execution/feedback通过。恢复拒绝错确认、未标记源实例、其他活动连接和非空目标；工具没有正式覆盖模式。文件级12项故障/锁/权限/路径安全测试通过；ENOSPC为remote helper显式注入，不能冒充真实云磁盘满验收。

Go/Swift边界用例证明旧快照保留dataset/service/device/collector身份却丢失后续receipt/sequence/tombstone；已完成页原载荷与receipt缓存可能已由P7合法回收。不能据HealthKit空查询推断删除，也不能在同一旧stream重置sequence。当前没有recovery generation、旧代际fencing或coverage完成合同，因此本轮交付审查设计与可重现blocker，未添加假可用恢复CLI/UI、reset Anchor、删除Outbox或第二套importer。保留原payload时连续exact replay及known tombstone抑制通过，不等于完整自动恢复。

真实PG另验证备份后5km→7km及执行完成/feedback会丢失；备份内Override仍生效，新Base/Effective正常追加，旧Effective和frozen execution不被重算覆盖。**数据库备份之后的新手动修改与执行历史可能永久丢失**，HealthKit不能重建原人工命令或处方。

最终Go普通183/race183、完整真实PG/race命令365个test pass事件（fail/skip=0，integration393.169秒）、vet/build/gofmt/mod/249包边界通过。完整Compose最终exit0，包含月度SSH/45表实际恢复、P7 TLS/权限/加密恢复、API/Worker/PG实际SIGKILL、网络/ENOSPC、三轮生命周期及195.507秒/144读/6增量/6逐轮发布的稳定窗口soak，清理exit0。此前soak读取断言失败与未留码事实保留；最终口径加入明确publication屏障，保持零读取错误，不代表dirty期间可用性。最终Go、Swift、Compose命令/数量、失败轮和逐项门禁见 [Backup/Recovery Result](RAI_BACKUP_RECOVERY_RESULT.md) 与 [机器证据](evidence/backup-recovery-2026-10-29.json)。Swift41项（含两项本轮边界）及macOS合成GC实际SIGKILL恢复通过；不声称真实HealthKit/恢复UI已通过。本轮没有生产数据、真实云备份文件、正式cron、真实iPhone状态或旧running变更。**REAL DEVICE RECOVERY与P3/P6 REAL DEVICE ACCEPTANCE继续DEFERRED / NOT VERIFIED；P7 ENGINEERING STATUS保持PASS WITH RISKS、DEPLOYMENT PREPARATION保持PARTIAL、PRODUCTION RELEASE READY保持NO。** 完成证据后停止。

## Monthly Backup Installation / Minimal Manual Recovery Review（2026-10-29）

`BACKUP_DEPLOY_BASE_COMMIT=09fb5bb38d32cdbe42c757277ab2431995818ac4`，main。P7已提交，入口有上一轮5个tracked及18个untracked变更（共23项），本轮保留并增量适配，不沿用上轮“入口干净”。[部署结果](RAI_MONTHLY_BACKUP_DEPLOY_RESULT.md)、[最小恢复审查](RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md)、[本轮机器证据和原始日志](evidence/monthly-backup-deploy-2026-10-29.json)记录独立口径；不改写P0–P7历史结果或上一轮证据。

**MONTHLY BACKUP CODE: PASS WITH RISKS；REAL CLOUD BACKUP: PENDING CONFIGURATION；MONTHLY SCHEDULER: PENDING CONFIGURATION；MONTHLY MONITORING: PASS；MINIMAL DISASTER RECOVERY REVIEW: COMPLETE；DISASTER RECOVERY IMPLEMENTATION: NOT STARTED；DISASTER RECOVERY RESYNC: BLOCKED。** 真实目录/上传/删除受管云副本/正式调度仍按用户任务A4为PENDING USER APPROVAL；代码与隔离验证不代表安装完成。

fnOS只读盘点为Docker28.5.2/Compose5.1.0、旧project rai的rai-postgres及frpc；容器配置DB/role均rai，bind旧项目pgdata，没有正式V2的API/Worker或月度脚本。未连接旧库、读取schema/健康事实或备份它。宿主Asia/Shanghai、cron active，当前用户无crontab，可读系统任务无RAI备份；其他用户任务未全面核实。正式V2源身份和云端host/user/key/专用目录缺失，用户表示不清楚；通用cloud别名没有被当作备份身份。只提供正式部署后可复用的配置/命令与唯一月度cron；未安装或访问真实云备份文件。按当前日历推算下一计划2026-11-01 03:00+08:00，未安装不能声称存在实际next-run。

复用整库custom/严格SSH/双锁/原子manifest/大小SHA/三份保留；补真实source PG18版本核实、结构化操作日志和0600原子最后尝试记录。共享月度日历策略以每月1日03:00上海时间＋24h宽限检查快照起点；正确处理不同月长/闰年/跨年，宽限不掩盖前月漏备份。`rai-backup.sh check`区分无备份/健康/逾期/失败/损坏/远端不可达；`p7-monitor.sh --monthly`及显式monthly alert策略现场调用月度证据，不用26h；旧daily工具保留兼容。没有新增告警服务、自动恢复、额外加密或常驻数据库。

fnOS最终Go普通183/race183、完整PG18 race命令365个pass事件（fail/skip=0，integration314.342s）、gofmt/mod/vet/build、含integration/scale/p7capacity的vet及249包边界通过。Python文件安全12项、日历/状态适配7项（72个月）和旧P7告警控制通过；最终增加时间字段类型拒绝后，又在fnOS重跑12＋7及每日告警控制通过。Swift核心41项通过；无Swift修改、没有重跑P6 UI截图。定时入口仅用临时配置验证错时返回75，不当作真实月度触发。

真实loopback SSH五次备份最终三份，断连/超时保留旧集合，失败check/手动成功解除和P7月度入口保留Worker停止告警通过。2,671,594B归档在全新凭证/空卷PG18实际单事务恢复；45表count/hash一致、schema13/FK/不变量、原P3回执与P5 Override/Effective/frozen Execution/Feedback通过。这里是loopback上传后本地读取归档恢复，**没有真实云端下载往返**。完整Compose退出0，原P7故障/加密恢复、三轮生命周期、私有256MiB真实ENOSPC及195.134s/144读/6增量/6次确认重生成的稳定发布窗口soak零错误；月度/P7清理exit0，事后仅原rai-postgres/frpc运行。不是生产SLO或真机验证。

最小恢复审查只比较A恢复旧库接续与B新空库/新身份。A保留备份内历史，但旧Scope不变、receipt/seq/墓碑回退及GC后payload缺失不能安全协调；B复用原Init与首次历史同步，但固定Outbox/Background session/Product journal尚不能安全切换。当前两者真实恢复都BLOCKED；按用户接受损失的范围建议以后验证B的显式独立本地会话，预期复用现有服务端协议，未实现按钮/会话切换/generation/新恢复流。SQL隔离恢复后的真实手机写入仍须关闭，禁止reset Anchor、清Outbox或静默接续。

入口481个受保护文件（Go/Swift/合同/角色/migration/原方案/历史结果和证据）逐文件SHA一致。未操作生产、切换入口、安装正式调度、提交或推送。真实V2/云身份、目录权限/空间、真实上传→下载→新PG恢复、正式cron/监控及通知、生产规模RPO/RTO、B会话切换、HealthKit可见覆盖/删除/Route/GPS/后台/强杀/真实网络均未验收。P3/P6 REAL DEVICE ACCEPTANCE继续DEFERRED / NOT VERIFIED；PRODUCTION RELEASE READY继续NO。

## Full Review Findings 定向修复（2026-10-29）

`FIX_BASE_COMMIT=0308b8658adc304040e445bc7f6ab61d212472d3`，main；入口已有原全面审查报告及JSON两个untracked，本轮保留。仅修复RAI-FULL-001–007及明确授权的unused AssociationRequest.Canonical。完整逐项Root Cause/实现/Before/After/测试/风险见 [Fix Result](V2_FULL_PROJECT_REVIEW_FIX_RESULT_2026-10-29.md) 与 [机器证据](evidence/full-review-fix-2026-10-29.json)。原方案、00001–00013、历史阶段结果及旧证据未改写，无新migration、commit/push、部署、真实数据迁移或旧running变更。

**FIX RESULT: PASS WITH RISKS；7/7 Finding FIXED。** 原反例先在当前HEAD复现。等时间Route只跳过不适用连接，保留raw与正常Workout publication；daily_load与180日窗口分别判断missing。新Workout/state bundle及features/fitness_fatigue为v2，TRIMP/recovery/readiness仍v1。P5使用training_merge/v2保留原Override事实、合法处方及明确rebase conflict，revoke清冲突，old Effective/started Execution不变；旧merge缺省v1可重放，Generator/parameters/feedback v1不变。

Training wire复用P3严格解码并声明required/null规则，合法HR/pace清空和canonical命令重放保持。28日窗口先按IANA calendar转换绝对边界，SQL用event_start范围，具体Workout独立identity路径；默认planner在1/3/5年2920/8760/14600场合成历史均读取224场，单次10.723/11.736/12.281ms，非SLO。新history summaries、immutable audit/detail section分页跨端256KiB，原snapshot/同步1MiB不变；原聚合入口保留小响应、超限413/Link。256条audit分13页最大92046B，16周50版本summary分4页最大4028B；原事实和lineage可完整逐页重建。iOS当前README正确说明完成材料可GC、anchor持久与exact replay边界。

严格按Wave1→完整回归→Wave2→完整回归→Wave3→Final执行。Wave1 PG/race326.045s；Wave2最新PG/race486.383s；最终gofmt、mod verify、普通/race、vet/build、249包边界及带integration/scale/p7capacity的vet通过，PG/race integration594.182s。最终Go合计872个test pass事件（含多轮重复和子测试，其中integration207），test fail/skip及package fail为0。P3/P4/P5/P7四组唯一项目Compose全部通过，1052.251s；实际TLS/角色、SIGKILL/真实lease、断连、45表恢复、生命周期及ENOSPC回归通过，未重做整套P7 capacity基线。OpenAPI1.1.0/Redocly2.71.1通过，原8项warning按identity逐个确认不变。

Swift Testing45项/6 suites、Normalizer8项、Debug Simulator/Release generic iOS/min18/Swift6 complete strict concurrency均通过。真实Swift→隔离API/Worker/PG逐页读取256audit/13页、257summary/13页、112处方，原ACK重放/409草稿/生成与snapshot/feedback闭环通过；相关audit分页UI1项0失败（68.546s），未重新设计Liquid Glass或跑整套截图基线。失败轮保留：JSON矩阵先遭正常429、密集context Worker setup触发预算、UI初轮缺loopback Debug host及一次Probe构建目录错误，修正测试条件/按既有合同重跑，均未计作PASS。大context读取用明确预构造的已发布合成context，3584条/72页完整，不冒充密集loader/Worker容量验收。

独立复核18个本轮root PG容器、6个Compose项目的容器/卷/网络均清理；Swift私有配置和本轮模拟器移除，fnOS只保留原rai-postgres/frpc。未连接旧库、读取真实健康值、提交凭证或改变真实入口。**P3/P6 REAL DEVICE ACCEPTANCE仍DEFERRED / NOT VERIFIED；DISASTER RECOVERY RESYNC仍BLOCKED / NOT IMPLEMENTED；PRODUCTION RELEASE READY仍NO。** iOS18 runtime、真实HealthKit/后台/强杀/GPS/网络、Metal/GPU、WorkoutKit、生产/真实云恢复和长期容量仍未验收。
