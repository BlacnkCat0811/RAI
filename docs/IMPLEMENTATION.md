# RAI V2 实施合同与实际状态

最新专项已按用户要求收尾：2026-10-30真机不再可用，**DEVICE ACCEPTANCE: NOT COMPLETED**。已验证iPhone12ProMax Debug签名安装/启动、Keychain保存、真实6,000条样本/24个durable批页及一条完整跑步的脱敏结构。Maintenance两项pending UI已补齐；跑步动态、Workout metadata和新增4类读取范围的代码/合成回归通过，最终真机重试尚未验证。独立持久 `rai-v2-device` PG18/API/Worker/schema14及volume保留。最终证据、失败轮与未验项见 [Device Acceptance](V2_DEVICE_ACCEPTANCE_RESULT_2026-10-30.md)；下文阶段报告保留当时口径。

2026-11-01 当前工作区：Phase 5A 实现 New Dataset + New Session，schema 18 / protocol 3；旧 Dataset 只读退役，原始事实、屏障、回执和训练历史保留，不迁移旧数据。旧 reset 路径已废弃，禁止用于正式升级与灾难恢复。实际验证结果与未验证项见文末 Phase 5A 和 [审计报告](audits/PHASE5A_NEW_DATASET_SESSION_REVIEW.md)。此前 Phase 1–4 历史段保持原口径，旧 reset 指令不是当前操作要求。

2026-11-01 Phase 5B 当前增量：New Session 灾难恢复与 fencing，schema **19** / protocol **3**。18 的审计约束无法表达 source lost，19 只扩展既有 admin_operations，不改1–18。init 的凭证先持久化到私有0600文件，commit/stdout丢失可用同 operation/原文件恢复。旧备份为无API的只读隔离归档，不能重连旧手机；正式入口见 [DR-A / DR-B](RAI_DISASTER_RECOVERY.md)。真实自动化结果与未验项见文末 Phase 5B。

更新：2026-10-30（Asia/Shanghai）。工作仓库 `/Users/tp/raI`。本文件是实施主合同；[Phase 0 基线](V2_PHASE0_BASELINE.md) 规定领域、协议、状态、预算及禁止事项，[Phase 1 执行计划](V2_PHASE1_PLAN.md) 是本阶段执行入口。原始[架构方案](../RAI-Go-PostgreSQL架构重设计与实施方案.md)保留原文；本轮用户冻结的 P0–P7 路线与其旧阶段编号、兼容和迁移设想不一致时，按本合同实施，不修改原文或追求旧系统兼容。

## 目标与不变量

原生 iPhone SwiftUI 只读 HealthKit，WorkoutKit 排程另行授权；不建立独立 Watch、Web 或桌面客户端。Go 模块化单体提供 API 与异步 Worker，二者同镜像、不同进程；PostgreSQL 18 是服务端唯一主要事实库；Python 仅离线研究/训练，真正需要时才使用 ONNX 与 Go 推理。目标部署 N100 → PVE → fnOS VM（4 vCPU、8 GB）→ Docker。单用户优先正确性、简单、可维护、可观测，再考虑性能和扩展。

API 完成鉴权、验证、幂等及事务后 ACK，分析异步；原始事实、删除屏障、提交日志、分析待办和最终回执在同一事务提交，dataset 行锁产生提交顺序，不能用 sequence 当下载水位。原始事实保留已支持来源语义；derived 有版本、输入谱系且可重算。上传不可变、重试返回原回执、删除优先、旧上传不能复活 UUID；未知/不支持字段拒绝，不静默丢弃。日常同步不依赖备份、恢复、迁移、全库摘要或全量重算；管理工具不可被在线包导入。

当前数据生命周期目标是 RECEIVED → DURABLE → QUEUED → PROCESSING → READY；DURABLE 表示 PostgreSQL 提交完成，与分析结果无关。失败任务有界重试、租约和 fencing，不伪造完成。健康事实从手机当前可见的 HealthKit 历史首次导入后增量读取；不迁移旧库或沿用旧协议回执，见 [ADR-002](adr/002-fresh-healthkit-dataset.md)。

训练产品合同为 Generated Base Plan + User Override = Effective Plan → Execution → Workout Result → Coach Feedback。保留 override 来源和历史，重算不得静默覆盖；不建立平行手动计划。路线首版计划为有界 JSON/NDJSON 不可变分块、gzip 和 seal；不要求 PostGIS。后续能力仅在合同、实现和验收完成后声明，具体见基线。

## 当前实现与范围

当前工程不是空骨架：`apps/server` 为 Go 1.27.3 的 `rai/server`，已有 api/worker/migrate/init 单二进制模式、pgx/v5、sqlc、goose migrations 00001–00016、合成测试、开发 Compose、Dockerfile 和 CI。`/health`、`/livez` 检查进程存活，`/ready`、`/readyz` 检查数据库与 schema。开发 Compose 包含 PostgreSQL、API 和 Worker，启动前须显式执行 migration；fnOS 隔离验收见下。

已实现的服务端切片包括 v2 数量/睡眠类别样本、有界多活动 Workout、append-only Workout enrichment、sample↔Workout 迟到关联、超大 Workout 分块/seal、数量 series 分块/seal、manifest 绑定的 route 分块/原子 seal/删除屏障、页回执、持久请求预算和上传租约。当前握手声明 `workoutBundles=true`、`bundleProtocolVersion=3`、`routes=true`；`changefeed` 仍为 false；P4 的 `analysis` 为 true。详细身份、限额及可观察错误以 [P3 同步合同](V2_PHASE3_SYNC_CONTRACT.md)、[Protocol 3 同步合同](../contracts/sync-v3.md) 和 [OpenAPI](../contracts/openapi-v2.yaml) 为准。Workout 主体和样本内容不可变；迟到 metadata 只能追加有源版本事实，不能覆盖既有不同值。（Protocol 3 起旧上传路径已移除，见文末“Workout Bundle 单请求同步”。）

当前 Worker 已消费 Workout、sample/series依赖与training-state任务；版本化run/results、原子publication、generation CAS、lease renewal/fencing、当前结果查询和受控重算已实现。Go纯算法与storage分离，参数不足/心率覆盖不足时返回unavailable；recovery/readiness只表达load-only模型，不假装使用睡眠/HRV。P4 fnOS验证与限制见末尾及 [P4 Result](V2_PHASE4_RESULT.md)。

P3.5 原生iPhone同步工程已经实现，但真实授权、HealthKit读取、后台与iPhone kill/GPS/E2E仍NOT VERIFIED。用户本轮授权将真机验收延期，允许P4开发入口例外；保留历史P3/P3.5结果。客户端仍使用原子文件outbox，没有服务端SQLite。完整事实下载与正式部署仍未实现或未验收；P5/P6已实现，真实状态见后文。本轮P7增量见末节。生产不依赖Node；独立OpenAPI lint仍使用Node。

当前 Final Maintenance 复验基线为 `1783517900520ed6aedc3c97ef497bc8fe0ef5ca`，该提交已包含五项修复；本轮没有重复修改业务源码。入口干净，当前文档与证据增量未提交。在线依赖图必须按实际构建模式引用：fnOS linux/amd64、Go1.27.3，CGO_ENABLED=0为发布镜像模式，CGO_ENABLED=1为原生/race模式；本轮重复观测见末节和机器证据。下文历史阶段的249保留为当时观测，不批量改写。

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

## Final Maintenance — 2×P2 + 3×P3（2026-10-29）

先前实施基线 `945db85ee5ff2920158a559597e0dabcff4d58c6`，修复已包含在 `1783517`，不再是当前未提交源码。本节保留实现说明；2026-10-30以实际HEAD复验的逐项结果、失败轮及未验证项见 [Maintenance Result](V2_FINAL_MAINTENANCE_FIX_RESULT_2026-10-30.md) 和 [机器证据](evidence/final-maintenance-fix-2026-10-30.json)。范围仅P2-1/P2-2/P3-1/P3-2/P3-3，非P8或新审查。

合法sample混合页和全删除页按type/UTC日聚合，新增仍批量写入，已知删除保留旧范围，未知删除不虚构范围；事实/屏障/日志/dirty/job/receipt/stream/page仍同事务。原生iOS pending时锁定可冲突表单和保存，独立原操作重试保持完整身份，确认成功刷新并关闭已完成表单，失败/409保留草稿；提交层拒绝将新草稿替换成旧payload。Training五类JSON写接口共用HTTP媒体/encoding/有界读取，分别415/413/422，严格JSON及命令hash不变。三类chunk先验collector与upload lease并持有slot锁，再查原receipt；重试返回前及新写COMMIT前复验，有效租约lost-response恢复保持。

Active Architecture、P4/P5/P6合同、开发Worker/schema说明、P7 OpenAPI及Changelog同步当前workout.bundle/features v2、training_state.bundle/fitness_fatigue v2、OpenAPI1.2.0/schema16。原始方案、00001–00013和历史阶段结果/旧evidence不改写。依赖计数按上方两种CGO模式保留实际差别，不强行替换历史249。

先前记录所引用的2026-10-29最终Maintenance报告及JSON在本轮入口不存在，不能据此认定当时验收完成。本轮重新执行真实fnOS隔离PG、Compose及可用的客户端验证，实际结果见以下复验节；源码存在或补充探针通过不能替代Swift Testing、Xcode和UI门禁。未连接旧rai-postgres，未读取生产健康数据、部署、切换旧入口、迁移、commit或push。

**P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED；P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED；DISASTER RECOVERY RESYNC: BLOCKED / NOT IMPLEMENTED；PRODUCTION RELEASE READY: NO。** 架构、P3协议核心、P4算法权威、P5单计划模型和P6产品架构保持。

## Final Maintenance 当前HEAD复验（2026-10-30）

`FINAL_MAINTENANCE_BASE_COMMIT=1783517900520ed6aedc3c97ef497bc8fe0ef5ca`，main，入口干净。该HEAD已包含五项修复，本轮仅更新active文档与结果/证据。旧945db85独立快照真实PG复现混合1000事件→1000jobs及无效chunk lease回执绕过；当前定向PG35.736s通过，混合页1job、跨3日3jobs、24个无效lease均无回执，幂等/墓碑/冲突回滚保持。单页多type或sample/non-sample按原stream合同拒绝，多type使用正式独立stream验证。

当前完整普通PG429.230s、完整PG/race465.678s通过；普通/race单元各221pass事件，PG/race命令507个pass事件（含单元/子测试，integration 284），fail/skip=0。gofmt、mod verify、vet（含integration,scale,p7capacity）、build、tidy -diff最终通过。首次默认模块代理超时、第二次本机变量未转发的失败保留，最终显式远端GOPROXY重跑通过，模块文件未改。CGO_ENABLED=0连续两次248 online packages，CGO_ENABLED=1连续两次249；历史249不改写。

此前首轮完整race有3个失败事件（提交顺序、5年训练查询子例及父测试），原门禁脚本未保留失败明细。两项定向race及最终完整race均在不改源码、断言或超时的条件下通过；首次失败原因仍未确定，失败记录和风险保留，不能以重跑通过将其抹去。

唯一项目/新凭证/新卷Compose279.420s通过，覆盖混合页及三类chunk重放、API/PG重启、Worker离线ACK、P4/P5实际发布与Worker重启后新Base，清理exit0并独立复核。Redocly2.71.1/OpenAPI1.1.0通过，8项warning的rule+pointer与历史证据完全一致。13项migration checksum、原方案、业务源码/测试/部署/合同和历史报告/evidence逐字节保持。未连接旧库、读取真实健康值、commit/push/deploy或迁移。

**本轮STATUS: BLOCKED；P2-1/P3-1/P3-2/P3-3 FIXED；P2-2 PARTIAL。** Swift6.3.3核心严格并发构建、历史intent及当前pending补充探针通过；正式swift test因缺Testing模块编译失败，Debug Simulator/Release generic iOS及SessionEditor/CreatePlan UI因缺完整Xcode未执行。不能用补充探针或历史PASS替代这些门禁。完整逐项closure、命令/耗时/退出码、24格lease及job计数见[本轮报告](V2_FINAL_MAINTENANCE_FIX_RESULT_2026-10-30.md)和[机器证据](evidence/final-maintenance-fix-2026-10-30.json)。

P3/P6 REAL DEVICE ACCEPTANCE继续DEFERRED / NOT VERIFIED；DISASTER RECOVERY RESYNC继续BLOCKED / NOT IMPLEMENTED；PRODUCTION RELEASE READY继续NO。完成本轮可执行验证及报告后停止。


## 本机 Xcode 配置与构建（2026-10-30）

按用户要求配置新本机并构建。当前选中 `/Applications/Xcode.app/Contents/Developer`，Xcode 26.6（17F113）、Swift 6.3.3、iOS 26.5 SDK；运行首次初始化和组件检查，安装 iOS 26.5（23F77）Universal Simulator，并通过 `xcrun simctl runtime scan-and-mount` 恢复运行时识别。沿用已提交 Xcode 工程和本地 Swift package，未修改业务源码或签名团队。

`xcrun swift test` 正式通过：48 tests / 6 suites。RAI scheme 的 Debug 与 Release 均以 `generic/platform=iOS`、`CODE_SIGNING_ALLOWED=NO` 构建成功，保持 iOS18 最低版本与 Swift6 complete strict concurrency。首次构建因平台未安装失败，安装后重试成功；完整日志及命令见[本机构建证据](evidence/local-xcode-build-2026-10-30/result.json)。AppIntents 元数据提取跳过警告保留。

本轮仅补充本机构建证据，不将此前 Maintenance 的 UI 门禁标记完成。模拟器已可识别，但未执行模拟器 App、Normalizer 或 SessionEditor/CreatePlan UI；真机签名安装、HealthKit 和后台恢复继续 NOT VERIFIED。本机缺少 Go，尝试 `go test -race ./...`、`go vet ./...` 与依赖边界检查均因找不到 Go 未执行；未改后端或事务，不以历史回归替代本轮检查。未部署、接触生产数据或修改旧 running。

## 真机开发签名配置（2026-10-30）

按用户要求使用本机 Xcode 已登录的免费 Personal Team 配置真机开发签名。Team ID 只写入被 `.gitignore` 排除的 `ios/RAI/Config/Developer.xcconfig`，不提交到仓库；`project.yml`/pbxproj 保持 Automatic Signing，App bundle 为 `com.rai.v2.ios`，并保留只读 HealthKit entitlement。

`xcodebuild -project RAI.xcodeproj -scheme RAI -configuration Debug -destination 'generic/platform=iOS' -derivedDataPath /tmp/rai-signing-probe -allowProvisioningUpdates build` 实际 exit 0。生成物由 Apple Development 身份签名，`codesign` 验证 `TeamIdentifier`、`application-identifier` 与 `com.apple.developer.healthkit` 均存在；Xcode 自动生成的 iOS Team Provisioning Profile 到期时间为 2026-11-06T08:01:14Z（免费 Personal Team 的 7 天有效期）。命令和脱敏结果见[签名证据](evidence/ios-signing-2026-10-30/result.json)。

当前 `xcrun devicectl list devices` 没有连接设备，因此只完成“可签名构建”，未安装、未启动 App、未触发 HealthKit 授权，也未验证后台 URLSession/强杀恢复。连接并信任 iPhone 后仍需用 `-allowProvisioningDeviceRegistration` 或 Xcode Run 完成设备注册和安装；真机安装及 HealthKit 继续 **NOT VERIFIED**。

## iOS App 图标（2026-10-30）

按用户反馈，放弃字母感抽象玻璃标志，改为液态玻璃跑者与环形跑道；之后依用户配色意见移除橙金色，更新为珍珠银、冰青、淡紫高光和深靛蓝背景。1024×1024 图标已加入 `ios/RAI/App/Assets.xcassets/AppIcon.appiconset`；XcodeGen 配置和现有 Xcode 工程均将其注册为 `AppIcon`。

本机 Xcode 26.6（17F113）Debug generic iOS 未签名构建 exit 0；产物 `Assets.car` 存在，编译后 Info.plist 的 `CFBundleIconName=AppIcon`，`assetutil` 检出4个 AppIcon 图标条目。配置 YAML 解析通过。命令、SHA-256及产物位置见[图标证据](evidence/ios-app-icon-2026-10-30/result.json)。

真机主屏裁切/遮罩效果及深色、染色替代图标未验证；本轮未安装到设备、未运行业务测试，也未修改后端、HealthKit或服务端状态。

2026-11-06 按用户提供的新图（浅色液态玻璃机器人 + RAI 字样）替换 `AppIcon.appiconset/AppIcon.png`：源图 1254×1254 RGB PNG，按图标主体边界裁切后重采样为 1024×1024 sRGB PNG，无 alpha、内嵌 sRGB profile，SHA-256 `8e2e88461a252a8363ffca0724229ee1e5e28996f6e28b6bce8c9e87ef397907`。`xcrun actool` 编译 asset catalog exit 0（生成 `AppIcon60x60@2x.png`、`Assets.car`，partial Info.plist 中 `CFBundleIconName=AppIcon`）；`xcodebuild -project RAI.xcodeproj -scheme RAI -configuration Debug -destination 'generic/platform=iOS' -derivedDataPath /tmp/rai-icon-build CODE_SIGNING_ALLOWED=NO build` 返回 **BUILD SUCCEEDED**。真机主屏裁切/遮罩及深色、染色图标仍未验证，本次未安装到设备。

## iPhone 12 Pro Max 模拟器验收（2026-10-30）

用户要求启动 12 Pro Max 模拟器并执行可在模拟器完成的验收。已创建并启动 `RAI iPhone 12 Pro Max`（iPhone13,4，iOS 26.5/23F77），Xcode 26.6；使用 fnOS 一次性隔离 PostgreSQL 18.6、真实 API/Worker 和合成数据。

Debug 构建、安装、启动通过；`RAITests` 14/14 通过。UI 结果：test01/02/03/05/06/07/08 共 7 项通过，覆盖导航与诊断、编辑/409/改期/跳过/添加/执行/反馈、首页滚动性能采集、截图候选、调整历史分页、pending 原命令重启重试、空计划 pending 锁定；test04 在无障碍审计前因该模拟器无法实际切换 Reduce Transparency 而 **BLOCKED**，未执行裁切审计，不记作无障碍通过。test02 的测试脚本仅将 iOS 26.5 zoom 详情下滑关闭改为返回后重新进入，产品源码未修改；test08 在清理旧 app 容器后通过。旧截图基线日期/数据不同且本机候选截图有已知黑图合成异常，快照像素比较 **NOT ACCEPTED**。

完整命令结果与失败留痕见 [模拟器验收报告](SIMULATOR_ACCEPTANCE_RESULT_2026-10-30.md) 和 [机器证据](evidence/simulator-acceptance-2026-10-30.json)。真实 iPhone/HealthKit、GPS/后台强杀、TLS/生产、iOS18 runtime、GPU/Liquid Glass、完整无障碍矩阵和正式签名发布仍未验证，不能以模拟器结果替代。

## iOS 界面中文化（2026-10-30）

按用户要求，将 App 内可见的英文文案统一改为中文或建立中文映射：五个主 Tab、首页/运动/训练/分析/我的、训练详情与编辑器、执行与反馈、同步与诊断页面均已中文化；跑步距离、时长、配速、心率、训练负荷和自感用力等单位改为中文；体能、疲劳、恢复、准备度指标以及分析状态、训练状态、调整字段、建议原因码和同步错误码均映射为中文。应用名称与健康数据/本地网络权限说明同步中文化，根视图固定中文区域格式，避免系统日期和时间控件回退英文。

本轮实际执行并通过：`swift test`（48 tests / 6 suites）；`xcodebuild -project RAI.xcodeproj -scheme RAI -destination 'generic/platform=iOS' CODE_SIGNING_ALLOWED=NO build`；模拟器 `build-for-testing`；`RAITests` 14/14。未连接隔离 API 进行完整 UI 截图回归，未在真机逐屏视觉验收；服务端返回的用户备注等自由文本不作翻译，真机/生产门禁仍按上文 `DEFERRED / NOT VERIFIED`。

## 真机同步阻塞修复（2026-10-30）

用户反馈真机“最近运动”停在 7 月 15 日、同步长期显示“同步中”、待同步页数约 172 且几乎不下降。只读检查真机 Outbox（不导出健康值、不记录 UUID）后确认四个独立阻塞：iOS 27 的 Workout 元数据新增 `HKAverageSpeed`/`HKMaximumSpeed`/`HKElevationDescended`，客户端旧允许列表抛出 `unsupported_workout_metadata_types`，Workout stream 从该页起永久停读；429 限流页保留旧的 `waitingForFactGeneration`，每轮绕过 backoff 重试全部页，形成限流风暴；多活动 Workout 把父 Workout 的 segment 事件挂在子活动上，服务端按子活动窗口校验导致整批 422（`eventIndex=24`）；单次同步对 Workout 只读 8 页（25 条/页），历史较长时无法在用户一次操作内到达最新记录。

修复：客户端显式支持上述三项元数据；服务端将活动事件边界改为父 Workout 窗口，活动统计仍按子活动窗口；Route 允许 GPS 时间戳相对 Workout 窗口 5 秒时钟偏差；Outbox 在 429 时停止当前 pass 并按 `Retry-After` 续传，非 409 失败清除陈旧等待标记；Workout 单次读取上限提高到 40 页，限流时仍优先推进 Workout stream；关联请求改走后台 URLSession；界面把等待样本的 `association_unavailable` 页单列为“历史关联待补全”，不再占用“待同步页数”和事实同步状态。

实际证据：iPhone 12 Pro Max（iOS 27.2）通过既有隔离 `rai-v2-device` PG18/API/Worker；服务端镜像重建为 `rai-v2-device:device-dynamics`，二进制 source revision `device-activity-parent-20261030`。修复后本地 Outbox 的 Workout 从 50 条增加到 316 条，最新一条为 2026-10-26 19:50（Asia/Shanghai），Workout stream 从 seq 3 推进到 56；6 个 Route 页完成，159 个待同步页中 157 个为等待样本的关联页。Swift package 49 tests / 6 suites、`RAITests` 14/14、Go `go test -race -count=1 ./...`、`go vet ./...`、249 包依赖边界检查均通过；活动校验定向集成测试 12.493s 通过。

本修复只针对隔离真机验收环境，未提交、未推送、未部署生产、未迁移真实数据。初始历史样本仍在按有界页继续补全，关联页会随样本到达自动解除等待；P3/P6 REAL DEVICE ACCEPTANCE 仍不能据此宣布完成，正式发布继续 `NO`。

## 本地 Go + PostgreSQL 开发环境（2026-10-30）

按用户要求，本阶段只处理本机开发环境。本轮开始时本机没有 Go、Docker、PostgreSQL 或 Homebrew，`sudo` 需要密码；因此没有安装系统级软件，而是把固定版本工具链放在工作区内被 `.gitignore` 排除的 `.local/toolchain`，不需要 sudo、Homebrew 或 Docker。此前“本机缺少 Go”的记录是当时事实，本轮已补齐，历史段不改写。

- Go **1.27.3** darwin-amd64 官方 tar.gz，SHA-256 `b6f8e931e4ab497fcde6da8ae4499a58f9d258e55c8a80824a516eae5950a750`。
- PostgreSQL **18.6**，来自 Postgres.app 2.9.6 的 `Postgres-2.9.6-18.dmg`，SHA-256 `9fc7d0dc08cf46dfd94bb32cbaaad81b41b37847a42d6dcb2f9fbd292813defb`；只使用其自带 server/client 二进制。
- 新增 `scripts/local/bootstrap.sh`（下载校验与安装）、`lib.sh`（路径/端口/连接串）、`pg.sh`（初始化、启停、重置、psql、备份、恢复）、`stack.sh`（构建、显式迁移、API/Worker 启停与状态）、`verify.sh`（端到端验收）、`test.sh`（质量门禁）；说明见 [本地原生环境](../deploy/local/README.md)。没有为符合建议目录搬动现有代码；`deploy/dev/compose.yaml` 保留为有 Docker 时的另一条路径。

实际验收使用专用本地 cluster：`127.0.0.1:55432`、UTF-8、`timezone=UTC`、data checksums，仅回环监听；数据库 `rai_dev` 为空库时重建。本地脚本会清除环境中的 `PG*`、`RAI_DATABASE_URL` 和 `RAI_DATABASE_URL_FILE`，固定连接该 cluster，避免误用生产 DSN。

| 验收项 | 实际结果 |
|---|---|
| PostgreSQL 18 启动 | `18.6 (Postgres.app)`，data checksums=on，fsync/full_page_writes/synchronous_commit=on |
| 空库迁移 | goose 00001–00015 全部 `applied`，15/15；schema 版本 15 |
| Go API 连接数据库 | API 进程启动，`/ready` 与 `/readyz` 返回 200 `{"status":"ready"}`；迁移与 readiness 均实际查询该库 |
| Worker 运行 | `rai worker-ready` exit 0；`worker_started`（handlers=5，processing_slots=1）；进程存活，`pg_stat_activity` 见 2 个应用会话 |
| 健康检查 | `/health`、`/livez`、`/ready`、`/readyz` 均 200；前两者 `alive`，后两者 `ready` |
| 备份与恢复 | `pg_dump -Fc` 生成 dump（SHA-256 `20be44392ef8e6c673d7a725f6c40e873d644f17669b84e8b671978868be6c5b`），恢复到新库后 schema 版本 15、public 表 46；临时恢复库已删除 |
| 隔离数据库测试 | `scripts/local/test.sh` PASS：单元、`go vet`、`-race -tags=integration ./...` 全部通过；integration 包 157.860s，0 failed/skip；一次性 `/tmp/rai-pg.*` cluster 使用 Unix socket、结束后 `RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`；依赖边界 249 包通过 |

机器可读证据：[本地环境](evidence/local-dev-environment.json)、[测试结果](evidence/local-dev-tests.json) 与 [原始测试日志](evidence/local-dev-tests.log)。为让既有仓库 gofmt 门禁通过，仅对工作区原有未提交修改 `apps/server/internal/syncing/protocol.go` 做了 gofmt 对齐（纯空白，无语义修改）；其余既有未提交业务/iOS 改动未触碰。

未验证/限制：本机没有 Docker，`deploy/dev` Compose 路径本轮未重跑；未执行 `rai init`、真实设备/HealthKit、TLS/生产角色、离机/加密备份或部署。Worker 的分析正确性由上述真实隔离 PostgreSQL 集成测试覆盖，本轮没有做长时间运行或吞吐浸泡。该环境仅用于合成开发数据，不能替代生产、真机或灾难恢复验收。**LOCAL DEV ENVIRONMENT: PASS；PRODUCTION RELEASE READY: NO（不变）。**

## 原生训练详情页（2026-10-30）

按用户提供的实际 iOS 训练详情页结构，在 `ios/RAI/App/WorkoutDetailView.swift` 实现原生详情页，地图使用 **Apple MapKit**；Go 端新增三个只读投影：

- `GET /v2/workouts/{id}/detail`
- `GET /v2/workouts/{id}/series`
- `GET /v2/workouts/{id}/freshness`

实现与口径详见 [原生训练详情](WORKOUT_DETAIL_NATIVE.md)。摘要包含平均配速、Minetti 等强配速、累计上升、训练负荷（TRIMP 优先，RPE/课型低置信回退）、环境修正与等效用时；时序固定八条并在服务端抽样到 ≤600 点（保留首尾/极值）；路线采样 ≤500 点；公里分段、实测 activities 分段、心率区间、跑姿动态、质量模型与执行快照人工覆盖均有真实读取路径。分析未发布时派生字段为 `null` 并显示“分析中”，不以 0 冒充。

验证：`go test ./...`、`go vet ./...`、真实隔离 PostgreSQL 18.6 的 `TestWorkoutDetailFactsPendingAndFreshness` / `TestWorkoutDetailSeriesRouteAssociationAndSplits`、Swift `xcrun swift test`（51 tests / 7 suites）、iOS Debug 构建、iPhone 12 Pro Max 模拟器 `ProductUITests/test90WorkoutDetailNativeSections`（1 test，0 failures）均通过；截图与机器证据见 [evidence/workout-detail-2026-10-30](evidence/workout-detail-2026-10-30/result.json)。

边界：真实 iPhone/HealthKit/GPS、真实 Apple Watch measured steps、阈值配速区间写入、独立 workout override 表/写入 UI、生产规模、TLS/部署与灾难恢复均未验收；本轮只使用合成数据，未修改 `../running`。

## 真机手动测试后端恢复（2026-10-31）

用户重新连接 iPhone 12 Pro Max 后，要求恢复一个可供手动真机测试的后端；用户选择保留 fnOS 上现有 `rai-v2-device` 数据集并把后端升级到当前工作区源码。检查发现手机 App 本地状态原先绑定的是已被本地重置的旧数据集 `8847a338…`，经用户确认清除 App 本地状态并重新安装；旧本地 Outbox/Anchor 丢弃，fnOS 服务端数据保持不变。本轮只准备隔离测试环境，不改变同步协议、migration、生产配置或旧 `../running`。

- 当前后端源码为 `3cbc5b8`＋既有未提交 workout-detail 改动；以 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 交叉编译，二进制 SHA-256 `dc099fa67f19b7898d84cce81619020eceedcab6835dade99c702e98b2f414a3`，镜像 `rai-v2-device:workout-detail-20261031`（`sha256:68f23d43a29d04df7b9a718ceff6ad7c3ab7b88b378bde7d4b232ffb28d20eb4`）。旧 `rai-v2-device:device-dynamics` 镜像保留，compose 备份为 `compose.yaml.bak-20261031-1435`。
- 只重建并重启 API/Worker，PostgreSQL 容器、`rai-v2-device_pgdata` 卷和数据未动；迁移 schema 仍为 13。API/Worker 均 healthy，`/health`、`/ready` 200，`rai worker-ready` exit 0。
- 原数据集 `d2582bb2-01f8-47a9-b0a1-767326befa23`、设备身份和 token 摘要保持不变；握手 protocol 2，`GET /v2/workouts/{id}/detail` 实际返回 200（1,560 bytes）。升级前后计数不变：1 dataset / 2 devices / 20,750 samples / 316 Workouts / 263 batches / 263 pages / 14 routes / 496 publications / 0 training plans。
- 用当前源码构建并安装 Debug `com.rai.v2.ios` 0.6.0（Apple Development 签名、HealthKit entitlement 保留），Debug HTTP 主机配置为 `192.168.10.25`；为清除旧数据集绑定，已按用户确认卸载并重装 App。fnOS 的采集器周期与令牌保存在本机 `.local/run/fnos-device-credentials.txt`（0600，gitignored），需在 App“开发接口”页手工填入后连接；未新增全局 ATS 放行或关闭 TLS 校验。
- 首次配置后 App 已成功握手并绑定 fnOS 数据集（说明网络、ATS、token 均可达）；但 11 个 stream 注册全部返回 409 `stream_identity_conflict`：重装后 collector epoch 未变，客户端生成了新的 stream ID。已把服务端现有 11 个 stream 的 ID/last sequence/last batch 恢复到 App 本地 `RAIV2Sync/state.json` 并清空失败页，重新启动 App；服务端未因此产生新 batch/fact。等待操作者再次触发同步。
- 第二次同步实际结果：新增 93 个 batch，其中 Workout 92 个（13 个共 316 条事件、79 个空 batch），HeartRate 1 个 250 条事件；所有事件均为 `unchanged`，`health_samples` 仍 20,750、`workouts` 仍 316，没有新增事实行。
- 修复后重新同步：15:12:03 时 `health_samples` 已从 20,750 增至 34,423（+13,673），`sync_batches` 从 356 增至 432；HeartRate 流从 seq 23 推进到 99，Workout 流保持 seq 272，未再产生空 Workout batch。上传仍在继续，`device_rate_exceeded` 是 30 次/分钟设备上传预算的预期节流，不是停滞。
- 用户提供整包导出 `~/Desktop/导出.zip`（352,750,953 bytes export XML）：800,443 条 Record、316 个 Workout、471 个 ActivitySummary、72 个 route、160,979 个 route point；按 App 当前支持的 10 类样本计约 534,839 条。为个人单用户部署新增 `RAI_RATE_BUDGETS` 显式覆盖，默认固定限额不变：read 1200/min·device、2400/min·dataset（burst 120/240），write 600/1200（60/120），upload 600/1200（30/60）。实际值已通过 `/v2/handshake` 的 `limits.requests` 核验。
- 同时把 iOS quantity page 从 250 提到 1000（服务端 `MaxEvents=1000`），Debug 构建/安装通过；预计 534,839 条样本约 535 个 batch，而不是 2,140 个。
- 客户端在 Workout 流末尾反复产生空页/batch，触发服务端 `device_rate_exceeded` 429；15:03:56 观测 read 等待 13.2s、write 2.6s、upload 1.7s。15:04:27 已停止 App，15:05:29 所有预算恢复为负等待（无限流）。服务端限额为 read 120/min·device、240/min·dataset（burst 30/60），write 60/120（10/20），upload 30/60（2/4）。
- 已在客户端 `ios/RAI/App/SyncCoordinator.swift` 增加空页保护：无事件、无 supplement、无删除时只本地推进 anchor，不封空 wire batch；Debug 设备构建/安装通过，待解锁后再次同步验证。该 patch 不改变协议或服务端事务。
- 放宽限流后的实际同步（15:42–15:55）：`health_samples` 从 46,923 增至 118,410（+71,487），`sync_batches` 565，`workout_samples` 415，`workout_routes` 17、`route_points` 30,751；最后一批为 15:48:01 的 1,000 条 HeartRate。服务端无 429，read/write/upload 预算在观测时均已恢复为可用的负等待。
- 随后客户端停在历史运动关联阶段：本地状态最后推进于 15:50:29，服务端自 15:48:01 后无新 batch；Outbox 中 36 页均为 `association_unavailable`（对应样本尚未上传，属于既有“历史关联待补全”路径），不是新的服务端错误。15:56:17 已重启 App 释放客户端等待，服务端事实和本地队列均保留；重启后需再次点“立即同步”继续有界导入。该现象说明客户端等待/关联补全仍可能长时间占用“同步中”状态，后续需要为 HealthKit 读取和后台关联上传增加明确超时与进度展示。
- 针对“太慢”的实测优化（16:07 Debug 构建/安装，Swift 51 tests / 7 suites 通过）：
  - `SyncCoordinator` 将 quantity 流单轮上限从 8 页提高到 500 页（每页 1,000 条）；一次前台同步会连续走到当前流末尾，而不是每 8 页停一次等用户再点。
  - 主样本流尚未补齐时暂停 `association:` 页；`OutboxPipeline` 不再每产生一个新事实批次就重试全部 `association_unavailable` 页，避免 36×N 次 409 和每轮数分钟的关联等待。样本补齐后才恢复关联补全和 Workout/Route maintenance。
  - 同步期间设置 `isIdleTimerDisabled`，避免自动锁屏导致 HealthKit 读取被挂起。
  - 实测服务端批量间隔约 5 秒/1,000 条；当前 `health_samples=152,410`，导出侧 App 支持样本 `534,839`，剩余约 `382,429`，纯上传下限约 32 分钟。优化前的“每轮 8 页 + 关联重试”是小时级。
- 16:08–16:15 仍观察到一次客户端无服务端请求的长等待（最后服务端 batch 16:05:21，客户端状态最后推进 16:08:17）；16:15:10 已再次重启 App，本地状态和服务端事实保留。下一轮前台同步将验证优化后的端到端吞吐；若仍出现无请求长等待，需要在 HealthKit 读取和后台关联上传上加有界超时，不能把“同步中”当成成功。
- 16:16–16:18 用户再次点击后，主数据实际进入高速上传：`health_samples` 152,410 → 188,410、`sync_batches` 599 → 636，约 1 秒/1,000 条；随后再次停止。只读取 Outbox 发现活动页是 **HeartRate 的 909 个 operation 页，cursor=429**，其中 302 个多值 `HKQuantitySample` 每个要发 series 注册、chunk、seal 三次请求，旧 `OutboxPipeline` 逐条串行上传，导致一页要十几分钟。这不是限流，也不是普通样本 batch 卡住。
- 已新增 series 并行上传：独立样本的 registration→chunk→seal 仍保持组内顺序，不同样本最多 8 组并发；整页 series 尾部全部成功后一次提交 page cursor/receipts，失败则保持原 cursor 幂等重放。Debug 构建/安装通过，Swift 51 tests / 7 suites 通过。新构建启动后把上述卡住页剩余的 159 个 series 在约 20 秒内完成，`series_uploads` 从 1,102 增至 1,261，活动页清空；服务端无 429。当前 `health_samples=198,410`、`sync_batches=645`、34 个 parked 关联页、0 个活动页，需要用户再点一次继续下一批。
- 本机 `go test ./...`、`go vet ./...` 通过；本轮未重跑真实 PostgreSQL 集成套件，不能替代数据库回归。

未验证项：真实 HealthKit 读取授权、样本/Workout/Route 增量同步、训练详情页真机渲染、后台 URLSession/强杀/断网恢复、TLS 与生产部署均未验收。未记录 token、健康值、坐标或数据库 dump。证据见 [后端恢复证据](evidence/device-backend-2026-10-31.json)。

## 审查发现的八项修复（2026-10-31）

本轮按审查结果修复当前工作区，不修改旧工程，不读取生产数据库；测试仅使用新建的合成数据和空白模拟器。

| 问题 | 当前行为与回归证据 |
|---|---|
| 路线 ±5 秒容差与分析校验不一致 | `LoadAnalysis` 与详情派生投影只使用 Workout 窗口内的点；原始 route points、manifest 和路线展示事实保留。`TestRouteClockSkewAnalysisPreservesRawFacts` 覆盖两端 ±5 秒可封口并发布分析、原始点/摘要不变、超出容差 1 微秒仍拒绝。 |
| 自动恢复跳过关联页 | 仅在 `readHealth=true` 的样本补齐阶段暂停关联；前台 `readHealth=false` 可正常重试保存页。新增可注入的 URLSession/FileUploader 边界，模拟器 `testSavedAssociationDrainDoesNotRequireHealthReads` 验证无 HealthKit 调用且关联 anchor 正常提交。 |
| 正常数量页无法进入 series 并发 | 并发处理连续 series block，之后独立验证正常 BatchBuilder 页尾的 GET page receipt。每组 registration→chunk→seal 顺序保留，最多 8 组并发；按 operation 持久化已验证的成功回执，cursor 只越过连续已确认前缀。429、取消、重启后跳过已确认请求；最终页回执成功前不推进 delivered anchor。`seriesConcurrencyKeepsReceiptsAndWaitsForPageSeal` 使用正常 BatchBuilder 页、并发峰值观测、429、重启和页回执 503 覆盖这些边界。 |
| 单个活动遮蔽实测 segment | 至少两个有效且连续的活动才提供活动分段；单个整场 activity 回退到事件，不合成活动间隙或整场步骤。纯计算测试覆盖单活动加两个实测事件、有间隙的活动及无真实分段时为空。 |
| 计圈原始枚举错误 | 显式支持 HealthKit lap=3 / segment=7，排除 pause/resume request=8；iOS 也显示事件回退分段，不再只显示 activities 路径。测试同时覆盖两类事件及无关事件。 |
| 心率重叠重复计时 | 用时间边界扫描，同值重叠只计一次，冲突重叠区间从暴露/覆盖中排除并标原因码；完全歧义不产出区间或刺激评分。测试验证 600 秒重复记录不会变成 1200 秒，部分冲突只计有效时间，低于静息心率的有效正值归最低区间。 |
| 未发布分析时显示派生结果 | `detail`/`series` 统一使用干净发布门槛：等强配速、爬升/路线质量、负荷、区间/刺激、环境修正、跑姿动态和可信度不提前出现；原始事实、温湿度、展示时序和实测分段仍可读。环境状态为 pending。存储单元测试覆盖 pending/failed/published；真实数据库详情测试验证发布前为空、Worker 发布后有值。 |
| 计划 Effort 冒充实际 RPE | 快照 Effort 仅作为处方强度估算，来源 `planned_effort_fallback/v1`、置信度 0.4；缺少计划强度则用课型估算。训练负荷与人工覆盖中的实际 RPE 均保持 null，新增 `actual_rpe_unavailable` 原因。测试同时验证两种回退和人工覆盖不伪造 RPE。 |

已完成验证：`go test -race ./...`、`go vet ./...`、`go vet -tags=integration ./...`、249 包依赖边界检查、gofmt 与 `git diff --check` 通过；`xcrun swift test` 52 tests / 7 suites 通过；专用空白 iPhone 16 Pro 模拟器（iOS 26.5）Debug 构建及 RAITests 15/15 通过。模拟器仅运行单元/协调器测试，未连接真实服务或真实 HealthKit；测试日志和 xcresult 位于本机 `.local/review-fixes-xcode.log`、`.local/review-fixes-unit.xcresult`。

完整真实隔离 PostgreSQL 18.6 race 集成套件通过：`PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration ./...`，integration 包 219.505s，包含上述路线时间容差及发布门槛回归；fsync/full_page_writes/synchronous_commit/data_checksums 均 on，仅 Unix socket，无 TCP 监听。结束时 `RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`。本轮专用空白模拟器已关闭并删除。机器证据见 [review-fixes-2026-10-31.json](evidence/review-fixes-2026-10-31.json)。

未验证项：真实 iPhone/Watch/HealthKit 采集、真实 GPS、后台/强杀/断网恢复、UI 截图及无障碍视觉回归、生产规模和部署均未在本轮验收。传输 mock 与空白模拟器不替代真机验收；未新增 migration，不重写历史分析发布，不自动部署或迁移真实数据。

## 单条 Workout 独立同步专项（2026-10-31）

用户要求采用 RQrun 式“一条 Workout 一个同步任务/逻辑上传包”，同时保留 RAI P3 的增量 anchor、Outbox、不可变载荷、Durable ACK、Route chunk/seal、Tombstone 和 P4 异步分析。只读审查见 [V2_PER_WORKOUT_SYNC_AUDIT](V2_PER_WORKOUT_SYNC_AUDIT.md)（结论：不需要修改冻结 wire contract，不需要 migration；服务端生产代码不改）。

实现要点：

- 新增客户端 `WorkoutSyncTask` 投影（`ios/RAI/Sources/SyncCore/WorkoutSync.swift`）与 Outbox task 持久化；每条 Workout 独立 stream key `workout:<uuid>:<sampleType>`，Workout 事件批次只含一个 Workout，样本/关联/路线/补充均归属于该任务。
- 新 UI：`健康数据同步` 页（`ProductViews.swift` / `RAIApp.swift`）列出真实 HealthKit 跑步记录、缺失显示 `--`、多选后逐条独立排队、逐条状态/详情/重试/检查服务端状态；默认不增加静默后台 HealthKit 读取。
- 旧全局 `SyncCoordinator.sync` 最初保留为兼容 drain/回归路径；同日已按“单条任务 + 启动恢复”口径删除，见下节。
- 客户端 `HealthKitClient` 增加 per-Workout anchored query、Workout list 和 Workout 级设备来源 metadata（后改为 enrichment 补充事实，见下节）；`SyncProbe` 增加 synthetic per-workout 模式。

验证：Swift package 58 tests / 8 suites 通过；RAITests 通过；新增空白模拟器 `WorkoutSyncScreenUITests` 通过；新增隔离 PostgreSQL 18.6 race 集成 `TestPerWorkoutIndependentStreamsAndIsolation` 通过；synthetic SyncProbe 对独立验收 API 产生 2 Workout / 2 Sample / 2 Association / 1 sealed Route / 2 个独立 `analysis.workout` scope 和 2 个 Workout publication；Release generic iOS 构建通过。完整 Go race/集成、机器证据和风险见 [V2_PER_WORKOUT_SYNC_RESULT](V2_PER_WORKOUT_SYNC_RESULT.md)。

**真实 iPhone 12 Pro Max HealthKit 验收在收尾时用户明确表示暂时无法进行，因此 `REAL DEVICE: NOT VERIFIED`；不得把 synthetic probe、模拟器或隔离 PostgreSQL 结果称为真机验收。** 未修改旧 `running`、未操作旧 `rai-postgres`、未自动 commit/push/deploy。

## 同步路径收敛：单条任务 + 启动恢复（2026-10-31）

按用户确认的口径，把产品内所有上传入口收敛为两条：用户只能手动同步单条 Workout；App 启动、回前台、后台 `URLSession` 完成时只做“恢复已生成但未传完的页”，不读 HealthKit、不创建页、不启动新任务。删除旧的全局全量同步和全局重试。

实现：

- `OutboxPipeline.drain(taskID:)` 增加任务过滤；`SyncCoordinator.syncWorkout` 及其 route/association/localPage 的所有 drain 都带 `taskID`，一个任务不会上传或完成另一个任务的页。
- 删除 `SyncCoordinator.sync(secret:transfers:readHealth:)` 全量读取分支，替换为 `recoverPending(secret:transfers:)`：只 `drain()` 已 stage 的页，并调用 `reconcileTasks()` 收敛状态；不进入任何 HealthKit 读取。
- `SyncModel` 删除 `start(readHealth:)`、`run(readHealth:)`、`retrySaved()`；新增 `recoverPending()` / `performRecovery()`。`scenePhase == .active`、冷启动 `.task`、`AppDelegate.handleEventsForBackgroundURLSession` 均只调用恢复。
- `SyncView` 移除“重试已保存数据”按钮，改为分开显示“当前任务待恢复页”和“历史遗留页”（旧全局页不再混入任务页计数）；单条重试仍走 `retryBlocked(taskID:)`。
- `OutboxStore.adoptLegacyPage(anchorKey:taskID:)`：旧全局 maintenance 留下的 `route:<workout>` / `association:<workout>:<type>` 页可被对应 Workout 任务收编，避免与新任务同名 anchorKey 互锁。`reconcileTasks()` 现在会把只剩永久 4xx 页的任务收敛为 `FAILED`，不再长期显示“上传中”。
- `syncWorkout` 在 Durable ACK 后调用 `store.synced()` 和 `collectGarbage()`，更新 `lastSync` 并回收已交付文件；此前产品只用单条路径时不会触发 GC。
- **Workout 身份载荷冻结**：`Normalizer.event` 只保留 HealthKit 自身 workout metadata，不再写入 `RAI:source*`；来源/设备 provenance 通过 `POST /v2/sync/workout-enrichments` 作为不可变补充事实发送。`WorkoutReference` 新增可选 `provenanceVersion`，成功后本地记录，重试不重复发送；旧本地状态里残留的 `RAI:*` 在比较时剥离，避免 `immutable_workout_changed`。
- 旧全局遗留页和当前任务页在 `SyncModel.apply` 中分开统计：`pending` 只算带 `taskID` 的任务页，`legacyPending` 单独显示；首页徽标使用两者之和。

验证（本机，2026-10-31）：

| 门禁 | 结果 |
|---|---|
| `swift test`（ios/RAI） | **PASS**：68 tests / 8 suites |
| `xcodebuild test -only-testing:RAITests`（iPhone 17 Pro 模拟器，iOS 26.5） | **PASS**：17/17 |
| `xcodebuild test -only-testing:RAIUITests/WorkoutSyncScreenUITests` | **PASS**：1/1（13.7s） |
| `xcodebuild build -destination 'generic/platform=iOS' CODE_SIGNING_ALLOWED=NO` | **PASS** |

新增测试覆盖：任务限定 `drain(taskID:)` 不影响其他 task 页和 legacy 页；legacy 关系页只能被一个任务收养；永久 409 页在 `reconcileTasks` 后任务收敛为 `FAILED`；恢复路径可在无 HealthKit 读取时清空已保存的 association 页（`NormalizerTests.testSavedAssociationDrainDoesNotRequireHealthReads`）；Workout identity 事件不含 `RAI:*`，provenance 只由独立 helper 生成；旧 `WorkoutReference` 缺少 `provenanceVersion` 仍可解码；旧构建产生的未提交 `sample_identity_conflict` 页可在重建前安全丢弃。

**未验证**：真实 iPhone/HealthKit、后台/强杀/断网恢复未在本轮执行；服务端 Go 未改。`sample_identity_conflict` 的客户端根因已按冻结身份载荷 + enrichment 补充事实修复，但尚未在真机对已存在的 316 条 Workout 做重放验收；“旧全局页首次恢复清零”也只在模拟器/单元层面验证。

## Workout Bundle 单请求同步（Protocol 3，2026-10-31）

用户要求按 RQrun 的导入粒度彻底替换逐 sample 上传。当前实现已改为：一条 HKWorkout → 一个 `WorkoutBundle` JSON → 一次 `multipart/form-data`（字段 `file`，文件名 `workout.json`）→ 服务端一个事务提交 Workout、全部 scalar samples、`workout_samples` 关联、多值 quantity series 和 routes → 一个 `analysis.workout` job。客户端不再发送逐 sample association、page/batch、series 分块、route 分块或 workout package 请求。

- 新接口：`POST /v2/sync/workout-bundles`、`GET /v2/sync/workout-bundles/{workoutID}?sha256=...`、`DELETE /v2/sync/workout-bundles/{workoutID}`；`protocolVersion=3`。
- 新 migration `00014_workout_imports.sql` + `00015_workout_import_history.sql`：按 `(dataset, workout, bundle_sha256)` append-only 保存 bundle 精确 SHA-256、验证后的源 JSONB 和原回执；同一 Workout 的多个 bundle 回执互不覆盖。
- **历史已废弃操作口径，Phase 5A 起禁止执行：** 旧上传路由已从 `httpapi.New` 移除。当时记录 Protocol 3 不兼容已有 v2 事实；升级前必须运行显式 `scripts/rai-reset-v2-data.sh --confirm-delete-v2-facts` 清空旧 fact/analysis/training-derived 行（保留 datasets/devices/collector leases）。不得将该命令作为自动 migration。
- `ImportWorkoutBundle` 在 dataset 行锁下重验身份；同一 Workout 核心内容不同返回 `workout_identity_conflict`，同一 sample/route/series 内容不同返回相应冲突；删除屏障优先。`completeSampleTypes`/`completeRoutes` 仅描述读取覆盖；普通快照缺席不再触发子事实 tombstone 或删除（2026-11-01 RQ-001 修正）。显式 DELETE 只由客户端收到 Workout `HKDeletedObject` 后调用。内容未变化不推进 `commit_rev`、不生成新 job。
- 限额：32 MiB bundle、50,000 samples、16 routes、1,000,000 route points、1,000,000 series points。
- 客户端 `SyncCoordinator` 已改为一次读取、一次上传、持久化 exact bundle 文件/哈希、按哈希状态恢复和 analysis 状态查询；HealthKit series 按类型一次查询并批量放入 bundle，不再逐 sample 查询。启动/回前台只重发已保存 bundle，不重新读取 HealthKit。

历史验证（本机 macOS，2026-10-31；其中缺席删除语义与旧回归已被下述 Phase 1 修正替代）：

- Go 单元测试、`go vet ./...` 通过。
- 隔离 PostgreSQL 18.6 全量 integration 套件通过（`-race`，integration 包 race 145.309s；非 race 复跑 75.038s）；新增 `TestWorkoutBundleOneRequestAtomicImport`、`TestWorkoutBundleLateSampleAndConflicts`、`TestWorkoutBundleHTTPMultipartAndStatus`、`TestWorkoutBundleScaleFiveThousand`、`TestBundleRejectsBeforeReadingBodyWhenDeviceSlotBusy`、`TestWorkoutBundleCompleteSnapshotDeletesMissingChildren`、`TestWorkoutBundleReceiptHistoryKeepsEveryBundle`、`TestWorkoutDeleteBundleIsExplicitAndIdempotent`、`TestHTTPWorkoutBundleStatusByHashAndExplicitDelete`，覆盖单事务事实、series/route、重复/历史回执、迟到 sample、内容冲突、multipart HTTP、显式删除、按哈希状态恢复、缺失子事实 tombstone、旧 association 路由 404 和 5,000 samples 规模；`TestP7RealLoginBoundariesAndRevocation` 通过受限 API 角色实际执行 bundle 导入，验证新表/函数的运行时权限。
- 合成 5,000 samples / 500 series / 2,283 route points bundle（1,617,379 B）在本地隔离 PostgreSQL 18.6 单次导入 1,232 ms（`TestWorkoutBundleScaleFiveThousand`）；这是功能规模观测，不是生产 SLO。
- `RAITests` 与 `WorkoutSyncScreenUITests` 通过；iOS Simulator 与 iPhoneOS Debug 构建通过。
- fnOS 隔离 `rai-v2-device` 记录为 schema 14；schema 15 变更尚未在 fnOS 重新部署验证，API/Worker 镜像 `rai-v2-device:bundle-sync-20261031`，`/ready` 200，handshake 实测 `protocolVersion=3`、`workoutBundles=true`、`bundleProtocolVersion=3`。

未验证：当前连接设备状态 `unavailable`，尚未在真机完成一次真实 HealthKit bundle 上传；因此不能声称真实 iPhone E2E、实际 5,464 sample bundle 时延、后台 URLSession 强杀恢复或生产 SLO 已通过。下一步必须在设备解锁/连接后安装 Debug App，上传一条真实跑步并核对 `workout_imports`、`health_samples`、`workout_samples`、`series_uploads`、`workout_routes` 及 publication。


## RQrun Phase 1 删除安全修复（2026-11-01）

本节替代上文 2026-10-31 的“完整快照缺席可推断删除”行为与旧测试口径。详细设计、回归映射和限制见 [专项验收报告](RQrun_PHASE1_RESULT.md)。范围仅 RQ-001、RQ-002、RQ-003。

- RQ-001：移除 `missingWorkoutChildren` 及导入中的缺席删除路径。`completeSampleTypes` / `completeRoutes` 仅描述读取覆盖；旧包不能删除已保存的 Sample、关联、Series 或 Route。事实身份冲突与现有显式删除屏障继续生效。
- RQ-002：未知 Workout DELETE 同样在 dataset 行锁与 fencing 后写持久屏障；COMMIT 后 ACK。复用 `entity_tombstones.deleted_rev` 与 `change_events` 保存删除时间、有效 epoch/collector 和首次原回执；重复 DELETE 返回原 revision/receipt，不新增事件/工作。客户端只接受验证通过的删除 ACK，404 不再确认本地删除。
- RQ-003：已知删除保存旧时间范围，同事务调用 P4 `rai_analysis_dirty` 登记 Workout dirty/job，现有依赖触发器立即使 Training State 失效。Worker 按已删除输入清理 current publication 指针，历史 run/result 原样保留，删除前载入输入的 Worker 受 generation CAS 约束。
- 无新迁移；schema 15、00014/00015、所有 migration checksum 和原始架构方案保持原字节。一次 Workout / 一次 multipart、原事务和 Route seal 边界保留；未部署、未迁移真实数据、未修改旧系统、未提交或推送。

验证：专项真实隔离 PostgreSQL 18.6 race 集成已通过（8.340s），包含两种可观测数据库锁顺序、跨 dataset、fencing、显式子删除、身份冲突、实际受限 API LOGIN、已发布 P4 失效、Worker superseded、六个写入/COMMIT 失败整体回滚；69 项 Swift 测试及 iPhoneOS Debug 无签名编译通过。Go `go test -race ./...`、`go vet ./...`、integration-tag vet、构建和依赖边界检查通过。全量 `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 在一次性 PG18.6 集群通过（integration 164.383s）；测试库零残留、集群已删除。OpenAPI lint 通过（31 条既有 warning，与入口基线相同）。[机器证据](evidence/rqrun-phase1-2026-11-01.json) 已记录命令、源码摘要与结果。

未验证：真实 iPhone/HealthKit 删除与后台/强杀/断网 E2E、fnOS/生产部署、物理断电恢复。v3 Sample / Route 显式删除捕获仍待后续完善；暂时保留缺席事实。未自动清除历史 tombstone、恢复过去误删数据或回填旧删除分析失效；不能从普通快照安全判定这些历史事实。


## RQrun Phase 2 协议与回执可靠性（2026-11-01）

结论：**PASS WITH RISKS**。范围仅 RQ-005 / RQ-006 / RQ-009；[正式验收报告](audits/RQrun_PHASE2_RELIABILITY_REVIEW.md) 包含字段矩阵、逐测试命令/断言、Phase 1 回归和工作区边界；[机器证据](evidence/rqrun-phase2-2026-11-01.json) 记录当前源码/日志 SHA-256，[失败记录](evidence/rqrun-phase2-failed-runs.txt) 保留未通过的开发/编译/执行尝试及后续复跑说明。

- RQ-005：复用 WorkoutSyncTask 的 UPLOADING 表示待重试，增加兼容旧状态的可选 attemptCount/nextAttemptAt/lastUploadError。原文件/边界/hash 重用，GET 404 永不确认删除或终止上传；网络/408/5xx/异常 ACK 可 GET 核对，429 尊重 Retry-After，握手/认证失败也持久退避；Store actor claim 防双 Worker。当前静态设备 Bearer 无 refresh endpoint；提供者可刷新一次，否则 401 保留原包并等待配置更新。workout_deleted 终态冲突不伪装成功。
- RQ-006：HTTP 入事务前递归检查 missing/null/type，再执行原值、成员和预算验证；quantity/series/route 等合法零值保留，不再以 Go 默认值伪造缺失数据。沿用扁平 Error，增加 field/reason；sync-v3/OpenAPI 补齐真实字段矩阵和完整 bundle schema。protocolVersion=3、合法旧请求/可选指标省略不变。
- RQ-009：仅 200 application/json 的严格当前 UUID/hash/committed/正 revision/counts/jobs 回执可确认；原回执、确认状态和 GC 授权在一次本地状态替换中持久化，随后幂等清理文件。状态写失败保留原文件，清理失败保留原回执。旧分析结果不能确认新包；恢复失败不更新全局“上次同步”。DELETE ACK 独立严格验证并原子提交本地删除状态。Phase 1 服务端存储事务、屏障/fencing/P4 generation CAS 未修改。

验证：Go `go test`、最终 `go test -race ./...`、`go vet`（含 integration,swiftclient）、构建和依赖边界通过。完整一次性隔离 PostgreSQL 18.6 race 套件通过（integration 209.056s），包含 migration/readiness/权限/回滚/HTTP 合同与全部 Phase 1 永久回归；之后扩展测试矩阵的跨语言专项 15/15 通过（46.729s），真实 Swift Outbox → URLSession → Go HTTP → PG18，覆盖断网、COMMIT 前超时、ACK 丢失/损坏、临时 404、429、500/502/503、401 刷新、双 Worker、重启、重复 POST、DTO 拒绝及 tombstone。所有隔离集群 fsync/full_page_writes/synchronous_commit/data_checksums=on，仅 Unix socket，退出零测试库残留、集群已删除。

Swift package：79 项 Swift Testing + 2 项普通 XCTest 通过；数据库驱动 XCTest 在普通 swift test 无 harness 时明确跳过，在上述真实 PG harness 下逐场景实际执行 15 次通过（没有将跳过当作通过）。空白 iPhone 12 Pro Max 模拟器 App 测试 17/17；该测试设备已删除。最终 iPhoneOS Debug/Release 无签名构建均通过。OpenAPI lint 通过（31 条既有 warning），migration 全部 checksum、Phase 1 存储文件和原方案与入口字节一致。

未验证：真实 iPhone/HealthKit、后台 URLSession/强杀/断网 E2E、物理断电/存储故障、数据库进程崩溃恢复、fnOS/生产部署与真实 token 轮换。源码曾在编译期间变更导致一次全量失败，冻结后完整重跑通过；新测试夹具的编译/断言失败及修正均留证据，没有跳过失败用例或放宽安全断言。保留 RQ-004、RQ-007、RQ-008、RQ-010 至 RQ-016 及 Sample/Route 显式删除捕获、历史 tombstone 修复。无新 migration，无生产数据/旧系统操作，无 commit/push/deploy。

## RQrun Phase 3 数据完整性（2026-11-01）

结论：**自动化 PASS；真机指定单 Workout 闭环 PASS，完整真机矩阵 PARTIAL（其余场景 BLOCKED/未验证）；总体 PASS WITH RISKS**。见 [完整报告](audits/RQrun_PHASE3_DATA_INTEGRITY_REVIEW.md)、[机器证据](evidence/rqrun-phase3-2026-11-01.json)、[真机计数与 hash 对比](evidence/rqrun-phase3-device-2026-11-01.json)、[续跑工作区保护](evidence/rqrun-phase3-workspace-baseline.json)。

- RQ-004：按 contract/type/workoutID/initialization 持久独立 HKQueryAnchor；secure archive 原始对象、发现事件、父索引、work intent 先提交 state.json，再独立核验并推进 anchor。每流有界分页；损坏 anchor 隔离，显式受控重新发现保留旧状态；Workout/Sample/Route 显式删除和未知父 unresolved 均持久化。确定性事实摘要与档案校验摘要分离，避免归档对象图差异重复创建 work。
- RQ-007：SourceRevision/HKDevice allowlist、保守 Apple Watch 来源政策、独立子 provenance，不合成缺失设备信息。metadata-only Sample 补充存 append-only bundle/观察历史，原 Sample 行/hash、原回执不改；数值、时间、单位、category、source 核心冲突继续拒绝。正式分析逐项核验保留子事实的当前/历史合格来源证据，父 Workout 不替未知历史子事实背书。
- RQ-011：逐类型 not_queried/observed/no_records_observed/partial/complete/failed/unsupported/unknown；query/local/server/route seal/P4 阶段分开。空结果不推断权限拒绝或绝对无数据，complete 仅对应本次声明范围，coverage 不参与删除。
- RQ-012/013：append-only Route capture 与精确前缀重放；声明点数、耗尽与持久化一致才完整；原序、相同/回跳时间均保留，无排序/修正/丢点；既有5000点chunk、双hash、seal不变。新增00016仅修复 route 微秒包络与投影转换；schema16/protocol3，00014/00015不改。
- 最终自动化：Go unit/race/vet、完整隔离PG18.6 integration+swiftclient（239.882s；数据库/cluster已清理）、96项Swift Testing/11 suites、22项iOS App tests、Debug/Release、OpenAPI lint（31条既有warning）、249包边界检查通过。Live XCTest 在普通 Swift 测试中明确跳过，在真实 Go→Swift→HTTP→PG harness 中实际执行；原Phase1/2安全断言保留。
- 真机：用户已授权2026-10-26约19:50记录；在保留旧Outbox的全新验收命名空间中，5466事件/12 scoped anchors/0范围外anchor，重复空变化不创建work，组包不产生虚假generation，ACK后无待交付generation。一个Workout/一次multipart/一个import、5464条Sample（HR349）、1768个source/bundle/chunk/projection GPS点；双manifest与本地采集一致，逐点projection比较通过。证据只含计数、hash和状态；仅本次隔离后端接收真实授权记录。API/Worker/一次性PG与本地临时凭证已清理，手机原始Outbox保留。

未验证：新建Watch跑步、真实后台delivery、查询中途强杀、断网恢复、真实HealthKit删除、真实ACK丢失、物理掉电、超大路线真机内存/耗时、生产/部署。最后新增的历史子事实来源分析屏障以合成数据库验证，不冒充真机混合来源验收。未知metadata/来源、核心Workout变化和损坏尾行仍保守待审；受控恢复UI与历史升级留待后续。初次670文件临时基线续跑时已不在，不能重新复核该全量快照；续跑681文件基线已持久保存，原方案与00014/15和Phase1 SHA-256一致。无commit/push/deploy、生产库或旧running操作。


## RQrun Phase 4 — 2026-11-01

状态：**PASS WITH RISKS**。仅RQ-008/014/015/016；详见 [性能报告](audits/RQrun_PHASE4_PERFORMANCE_REVIEW.md) 和 [机器证据](evidence/rqrun-phase4-2026-11-01.json)。执行前683文件SHA-256基线已保存，原方案/Phase1–3/00001–00016保持；无用户文件删除、staged/commit/push/deploy或生产操作。

- RQ-015先于gzip：整个HTTP body使用MaxBytesReader（32MiB+64KiB），framing64KiB、gzip file8MiB、decoded32MiB、100:1+64KiB ratio独立限制；严格单file part、chunked/无长度/preamble/epilogue均计费，拒绝不drain大part。词法流式presence/type/duplicate/unknown/null/count检查后一次typed decode，保留领域验证。
- RQ-008：main与真实socket测试共享NewHTTPServer；header5s/body180s/processing60s/handler245s/ACK5s，server185/255s fallback；Import45s transaction/context、statement5s；snapshot5/20s、publication5/10s；iOS270s request/300s foreground resource、后台270/3600s。5/15/30s gzip暂停、160/16KiB/s pacing、179s合法、180s 408、ACK写出超时均实际执行通过；超时保持408/503可重试分类。
- RQ-014：单multipart file可为application/gzip，plain兼容；bundleSHA256继续原JSON精确字节，与gzip header/实现无关。iOS固定level6/mtime0/OS255；同一Outbox保留原JSON权威文件与可重建wire文件/hash，崩溃/损坏/ACK/GC/停放删除回归通过。未新增API/数据库/Outbox或网络chunk。
- RQ-016：原240m AnalysisSamplesSQL plan约3007ms，新增一个有同数据集EXPLAIN证据的覆盖身份索引；schema17、protocol仍3。N100同生成器20–240m plain和fresh gzip全部真实Import→Worker publication，240m样本查询约145ms；原SQL排序和完整输入语义不改。额外索引约6MB及WAL写成本记录在报告，未用无限timeout/并发取得PASS。
- 真实验证：Go race/vet；完整隔离PG18.6 integration+swiftclient（integration269.127s，临时数据库0、cluster清理）；全部18个真实Swift→Go→PG故障模式；Swift98项/11 suites；iOS App22项；签名Debug、Release；OpenAPI31条既有warning；249包边界检查通过。长耗时/性能/真机测试在普通套件中opt-in SKIP，并已分别实际运行，不能拿SKIP或mock代替DB/设备证明。
- 指定真机：用户再次授权2026-10-26约19:50唯一记录。iPhone12 Pro Max、Apple Watch来源、5464 Samples/HR349，source/bundle/chunk/projection各1768点、逐点一致；JSON4330123B→gzip multipart483242B，一个import和Durable ACK。只导出计数/hash/状态，UUID含字典key散列，未导出健康值/GPS/凭证。新验收命名空间保留原Outbox，App清理本轮临时配置，一次性PG/DNS已停止清理。

未验证：真实手机断网/后台强杀、家庭公网/TLS反代/fnOS生产Docker、物理掉电、真实4小时Workout、长期anchor/极端Route容量、生产迁移/部署。iOS/Go/PG峰值RSS及事务峰值 **NOT MEASURED**；原修改前Import细分阶段未测，后补分段不冒称原始baseline。当前仅合成240m性能验收，不伪装长距离真机或生理模型验收。保留既有Raw Facts、DELETE fencing、Provenance、Coverage、点序和COMMIT后ACK不变量；未实施RQ-010/reset或真实数据迁移。

## Phase 5A — New Dataset + New Session（2026-11-01）

**PASS WITH RISKS；schema 18 / protocol 3 / OpenAPI 1.5.0；READY FOR PHASE 5B: YES。** [审计与验收边界](audits/PHASE5A_NEW_DATASET_SESSION_REVIEW.md)、[机器证据](evidence/phase5a-new-dataset-session-2026-11-01.json)、[operator/App 操作合同](PHASE5A_NEW_SESSION.md)。基于冻结 HEAD `816e26d` 的未提交工作区；本阶段未 commit/push/deploy/生产 migrate，没有触碰生产数据库、删除旧 Dataset、执行 reset 或真机全历史导入。

operator-only `rai admin new-dataset` 生成独立 Dataset/Epoch/Device/32-byte token/Collector；source row lock 下创建目标、退役 source、写 admin audit 同事务提交。operation ID 幂等，提交前一次 0600 凭证文件、fsync、原子不覆盖发布和重试复用支持终端响应丢失；DB 只存 hash。dry-run 严格只读。migration 18 只增 nullable retired_at 和管理操作表，旧 00001–00017 不变，18/18 checksum、fresh rebuild、17→18 和 repeated migrate 通过。旧事实/屏障/回执/分析/训练历史保留，所有事实/删除/训练与 Worker 持久写入都在退休状态下拒绝；旧 jobs 保留暂停。

iOS 实现 `SyncSessionDescriptor` 与单原子 manifest，独立 Keychain account、Outbox、ProductJournal 和后台身份；token/descriptor/Outbox 持久后完整握手才切 active。prepared 重启恢复，不重新生成身份；旧 Session 原地归档，旧 callback 只落原目录，新凭证不能发送旧 bundle。新 anchor=nil，重新发现 HealthKit 当前可见历史；旧服务器已删除但仍在 HealthKit 可见的 UUID 可以进入新事实空间。训练历史 discard、旧 RAI no_migrate；没有 SQLite/XML/旧 Running import。

实际验证：Go test/race/vet/build、dependency boundaries、完整隔离 PG18.6 race `integration,swiftclient`（integration 295.045s）、最终定向故障/幂等/并发/migration PG18 race、Swift 106/106、fresh 模拟器 App 26/26、Debug/Release generic device build、OpenAPI lint（31 既有警告）、git diff --check 全部 PASS。PG 四项耐久设置 on、无 TCP、leftover=0、cluster removed=1；测试模拟器已删除。真实 Security Keychain 测试使用本地 ad hoc 签名模拟器，不代表真机签名/安装验收。

旧根 Outbox、legacy Keychain、原始架构方案、历史证据及用户入场未跟踪文件保留。`rai-reset-v2-data.sh` 已标为 `LEGACY_UNSAFE_UPGRADE_PATH / DO NOT USE FOR PRODUCTION UPGRADE`，历史 reset 指令被 New Dataset + New Session 取代。

未验证：真机 Session 切换、真实 HealthKit 全历史重导、iOS 实际后台 App kill/relaunch、fnOS/公网/生产环境及正式灾难恢复；属于 Phase 5B/5C，不能由本地文件、mock 或合成测试替代。若 operator 私有文件和 Keychain 都丢失，不能从 hash 恢复 token，本阶段未实现重新签发工具。

## Phase 5B — Disaster Recovery + Recovery Fencing（2026-11-01）

**PASS WITH RISKS；schema 19 / protocol 3；READY FOR PHASE 5C: YES。** 继续于未提交的Phase5A工作区，入口HEAD `816e26d1a2eb2e09021eed56594f77d57d5e2a5d`；完整modified/untracked哈希基线见 [baseline](evidence/phase5b-workspace-baseline.json)。没有reset/clean/restore/checkout/stash、commit、push、deploy或生产数据库操作。原方案、1–18 migration和Phase5A证据保留。

DR-A复用operator-only `new-dataset --operation-type disaster_recovery`并退役source；DR-B全新schema0库migrate到19后通过正式`rai init`生成独立Dataset/Epoch/Device/Token/Collector/Service。init先发布/fsync0600私有凭证包，再原子提交身份与admin audit；同operation/actor/base-url/原文件可恢复rollback或stdout丢失，非空库不能创建第二个初始Dataset。stdout无token。Migration19只扩展原admin_operations的operation_type/source约束，因18无法表达source lost；无新audit表、不改旧migration。sqlc1.31.1已重生成。

iOS显式导入recovery package，沿用prepared/active/retired/recovery_required状态机；全字段握手通过才原子切pointer。旧Session/ACK/Outbox/deleted set/Keychain/ProductJournal不迁移；新Anchor=nil；旧callback仅影响旧命名空间。实际coordinator测试证明timeout/404/429/500不触发灾难状态；成功身份不匹配才转recovery_required。恢复可重复到第三套身份。未ACK旧bundle保持原字节；已ACK且GC后也不被新空间当作持有证明；不可再读的HealthKit事实为UNRECOVERABLE_FROM_HEALTHKIT。

真实PG18.6合成pg_dump/pg_restore：fsync/full_page_writes/synchronous_commit/data_checksums均on，47tables、65validated FKs及原epoch/revision/事实/receipt/DELETE receipt/analysis publication/training history核验通过，archive/manifest SHA与篡改拒绝通过。T1还原含A，但无之后B导入、A删除屏障和训练修改，证明backup restore≠current truth。恢复库不运行API/Worker并默认只读，旧POST/DELETE明确25006，revision不前进。旧演练脚本恢复后接旧Session的步骤移除；备份仅隔离归档，active恢复走新Dataset/Session。

实际Swift→Go HTTP→PG：A上传ACK并GC，模拟A存储对服务不可用；B从空schema migrate/init；Swift解析新package、nil-anchor新Session、由独立当前可见合成facts重新组装新bundle。Go commit后故意丢ACK，Swift查原回执完成；B只有1Workout/1sample/1import，training=0，新job→新analysis→1publication，无旧Dataset引用；旧bundle/旧token拒绝，旧Session文件不变。

完成验证：Go unit/race/vet/build、tagged vet、mod verify、dependency boundaries；PG完整integration+swiftclient+race（375.091s）和最终专项；Swift Package109个Swift Testing测试与2普通XCTest（3个live/性能入口独立运行按设计skip，live正确性由Go harness实际执行）；iOS模拟器App unit28项；Debug/Release generic iOS device build；OpenAPI valid（31既有warnings）；现有backup guard单元12项；Python编译/shell语法；19/19checksum MATCH；git diff --check。所有测试PG集群leftover databases=0、cluster removed=1；本轮两个一次性模拟器均删除。

真实证据见 [machine evidence](evidence/phase5b-disaster-recovery-2026-11-01.json)，完整边界和故障点见 [audit](audits/PHASE5B_DISASTER_RECOVERY_REVIEW.md)、[contract](PHASE5B_DISASTER_RECOVERY.md)、[runbook](RAI_DISASTER_RECOVERY.md)。A–G为合成持久边界/SQL故障/传输断连验证，不能替代真机SIGKILL或断电。iPhone12ProMax、真HealthKit全历史、真实后台kill、fnOS、公网TLS、生产migration、真实凭证交付均NOT RUN，留Phase5C。更新的Docker演练/CI wrapper仅本地语法审查，未触发远端执行；没有用它们的存在冒充数据库或部署验收。实际生产RTO/RPO与容量未验证；私有凭证文件和Keychain同时丢失不能从hash反推token。reset脚本NOT EXECUTED，production database NOT TOUCHED。

## HealthKit 活动事件编译警告修复（2026-11-01）

`HealthKitClient.integrityEvent` 直接遍历非可选的 `HKWorkoutActivity.workoutEvents`，移除无效的 `?? []`，保留每个事件的 metadata 校验。`HKWorkout.workoutEvents` 的可选处理保持不变。

验证：`xcodebuild -project RAI.xcodeproj -scheme RAI -destination 'generic/platform=iOS' CODE_SIGNING_ALLOWED=NO build` 通过，构建日志 `/tmp/rai-healthkit-warning-build.log` 无 HealthKitClient 编译警告或错误。使用工程本地 Go 工具链运行 `go test -race ./...`（缓存命中）、`go vet ./...` 与 `scripts/check-boundaries.py`（249 包）通过。未运行真机 HealthKit 验收或部署；本次无事务变更。

## 云服务器独立测试环境（2026-11-01）

用户明确授权云端部署及手机使用 tpandmr.xyz 后，实际部署 `lh-gz` 的独立
`rai-cloud-test-20261101` Compose project：PostgreSQL18.6/API/Worker 全部 healthy，
19/19 migration applied，PG data_checksums=on；数据库无宿主发布端口，API仅回环18443。
新增现有 HTTPS server 的 `/v2/`、`/ready`、`/health` 路由，保留旧 `/v1/`；
没有访问旧后端数据库、迁移真实数据或修改 ../running。详细路径、镜像来源、
资源限制、证书续期及回退见 [cloud test runbook](../deploy/cloud-test/README.md)。

实测公网受信 HTTPS `/ready`=200，带私有新令牌的 handshake=200、无令牌=401；
合成 Workout multipart 上传=200/committed，相同字节重试返回原回执，GET回执一致。
首条无来源分类的合成输入导致 Worker `analysis_source_unclassified`，真实记为failed，
没有伪报分析完成；补充完整合成 Watch provenance 的另一记录产生 succeeded/published
及1条 analysis_publications。两条合成记录随后通过认证 DELETE 删除，workouts=0，
删除屏障/提交日志/回执保留。手机凭证保存在 Git 忽略的0700目录、0600文件，未输出令牌。

完成 `go test -race ./...`、`go vet ./...`、依赖边界检查（249包）；另用全新隔离
Postgres.app18.6执行 `go test -race -count=1 -tags=integration ./...`，integration
226.197s，全通过，leftover databases=0、cluster removed=1。新增部署配置无Go逻辑或事务改动。
实测初轮 Nginx→API 因默认证书验证深度返回502，配置深度4后链/主机名验证与公网 smoke通过。
未验证本次 iPhone真机连接、真实HealthKit、重启宿主恢复、长期负载、云端备份/灾难恢复；
当前结果仅为测试部署验收，不能替代正式生产门禁。证书副本需随续期手动更新（当前到期2026-12-01 UTC）。

## 云端上传后“恢复待处理上传 / 待恢复页 0”排查（2026-11-02）

本轮仅诊断，尚未修复客户端。只读检查云端独立测试环境：API、Worker、PostgreSQL 容器均 healthy，API ready 与 Worker readiness 通过；没有读取数据库中的健康事实。API 相关性日志仅提取时间、operation、HTTP status、error_code 和 duration_ms：北京时间 03:00:56.366 最近一次 `POST /v2/sync/workout-bundles` 返回 200（2022ms），03:00:56.628 分析查询为 404/analysis_not_ready，03:01:07.868 分析查询返回 200。该 HTTP 记录未见上传失败；未读取手机 Outbox，不能仅凭日志证明手机已持久化 ACK。

源码确认的客户端问题：`SyncModel.performRecovery`（RAIApp.swift）在整个 HealthKit discoverChanges → recoverPending → syncDiscovered 期间持有全局 busy，并统一显示“恢复待处理上传”；与“只重发已准备 bundle、不会重新读取 HealthKit”的注释及界面提示不符。discoverChanges 无 workoutIDs 时从全部已发现 workoutArchives 轮转，每轮最多 4 条，逐条读 10 类 quantity 与 route，包含未选择上传的历史记录。扫描剩余、hasMore 或尚未建立 scoped flow 时，scheduleRecovery 会在约 2 秒后继续。同步所选/单条/重试按钮均因 busy 禁用，手动同步入口也直接返回，历史发现因此可以长期阻塞独立记录的用户操作。

“当前任务待恢复页”实际只统计 PREPARING/UPLOADING 任务，不统计 discovery 扫描、待交付 generation 或分析查询；且 performRecovery 要整轮完成后才 apply 新快照。因此 pending=0 与 busy=true 可以同时出现，不能据此判断后台空闲、存在遗留页或需要灾难恢复。真机当前具体阻塞在某个 HealthKit 查询、本地归档写入还是后续网络等待尚未采样；不能把代码路径分析当作已测得的真机耗时或死锁证据。

修复方向：将上传恢复与健康变化发现分阶段调度和显示；让显式单条同步可以安全暂停自动发现并优先执行；按真实任务、扫描和待交付工作分别展示数量，并在阶段边界刷新状态。取消时需保留原始日志、anchor、不可变包及上传身份，不能清空 Outbox 或建立新 Session 来绕过。

验证：本轮未改 Go/iOS 业务逻辑或事务；工程本地 Go 工具链 `go test -race ./...`（缓存命中）、`go vet ./...` 与依赖边界检查（249 包）通过。依赖边界初次因 PATH 无 Go 未执行，加载 scripts/local/lib.sh 的 rai_go_env 后复跑通过。没有部署、重启服务、切换会话或修改 ../running；尚未完成客户端修复或真机恢复验收。

## 自动恢复阻塞手动同步修复（2026-11-02）

在上述排查后实现客户端修复：新增可观察的 ForegroundSyncScheduler，自动恢复/发现/变化交付串行执行；显式单条、批量、重试/更新请求同步保留 manual 优先级，取消自动任务并等待其退出清理后才复用 coordinator/Outbox，重复手动请求和自动唤醒不能抢占已保留的手动任务。按钮通过 canStartWorkoutSync 判断，自动扫描期间可以选择并同步记录；其他授权/会话切换操作仍受互斥保护。列表刷新不再自行推进 discovery 或 reconcile 正在准备的任务，授权后扫描和后台传输完成唤醒统一进入调度器。

自动流程保留先发现健康变化（包括删除）、再接续已保存上传/分析状态、交付已选择记录更新的顺序，确保已发现删除继续阻止旧包上传；各阶段分别显示状态并刷新快照。诊断将原“待恢复页”改为“待恢复上传任务”，另列待扫描记录、未完成查询、待交付记录，纠正“前台恢复不读取 HealthKit”的错误提示。原始 anchor、发现日志、Session 身份和不可变 bundle 协议保留，没有清空 Outbox、重建会话或更改服务端事务。

取消从 URLSession、bundle delivery、恢复循环和分析查询向上传播；逐个原始对象归档时检查取消，不把主动暂停标成 discovery stream 错误，不继续下一条 bundle，也不因取消发起 GET 回执查询或丢弃后台响应缓存。手动任务完成后额外安排一次健康变化检查，避免取消发生在读取新变化前、旧 hasMore=false 时遗失自动接续。待上传原字节/hash/边界仍持久保留，可按原身份退避后接续；只有真实有效 ACK 才能确认和授权 GC。

实际验证：Swift Package 112 项 Swift Testing/14 suites 和 2 项普通 XCTest 通过；3 个 opt-in live/性能入口在普通包测试中明确 skip，其中上传 live 测试另由真实 PG harness 实际执行。新增调度器测试覆盖手动优先、等待取消清理、阻止双手动/自动抢占及后台取消；新增 bundle 测试核对原字节/hash、缓存保留、不发 GET 及重启后 ACK。一次性空白 iPhone 12 Pro Max 模拟器 App tests 最终 31/31 通过，包含真实 coordinator 查询取消/anchor 保留、恢复取消停止后续包和 SyncModel 忙碌/按钮判定。首轮 unsigned simulator tests 的 2 项既有真实 Keychain 测试报 -34018；改为本地 ad hoc 签名后全套通过，没有跳过或放宽断言。

真实隔离 PostgreSQL 18.6 `go test -race -count=1 -tags=integration,swiftclient -run TestWorkoutReliabilitySwiftClientPostgres -v ./integration` 通过（18 个 Swift→Go HTTP→PG 场景，79.844s）；覆盖断网、COMMIT 前超时、ACK 丢失/损坏、临时404、429/500/502/503、认证刷新、双 Worker、重复 POST、显式父子删除等。四项耐久设置 on，仅 Unix socket；退出 leftover databases=0、cluster removed=1。工程 Go `go test -race ./...`（缓存命中）、`go vet ./...` 和依赖边界（249 包）通过。没有 Go/SQL/migration 改动。本轮日志在 `/tmp/rai-sync-priority-{swift-final,app-final,postgres,go}.log`，签名复跑保留 `/tmp/rai-sync-priority-app-signed.log`，unsigned 失败保留 `/tmp/rai-sync-priority-app.log`。

最终 generic iOS device Release 无签名构建通过（`/tmp/rai-sync-priority-release-final.log`），Debug 由上述已签名模拟器全套测试构建验证；git diff --check 通过，测试模拟器已删除。

尚未验证真实 iPhone HealthKit 查询取消耗时、iOS 后台 URLSession 强杀/回调以及手机原有待上传包的实际恢复；上述合成测试和模拟器不替代真机验收。本轮没有云端部署、真机安装、生产数据库读取、真实健康数据测试或 ../running 改动。

## 运动能力模型专项设计（2026-11-02）

**RAI MODEL DESIGN RESULT: READY WITH RISKS。** 正式设计见 [RAI_MODEL_ARCHITECTURE](RAI_MODEL_ARCHITECTURE.md)。本轮仅新增该设计文档并在本文件追加真实状态；入口HEAD为 `6e5451c974a6284cf4aab2e6baf856eee63ac218`，现有iOS调度修复、云端测试目录及本文件未提交内容保留。未修改Go/Swift/SQL/同步合同，未读取生产或云端数据库、个人健康值、旧工程，未执行migration、部署或push。原架构方案原文SHA-256保持 `6951b1d938133a964cb06d28c3aa744ad05121f25da770e184041211df53e3de`。

只读核对HealthKit授权/实际10类quantity与Workout关联查询、Normalizer/metadata、protocol 3路由、migrations 1–19相关表、Go analysis/worker/storage publication、API DTO、ProductCore、训练计划消费者和现有测试。实际采集列表不包含日常HRV/RHR/sleep/VO₂/体重，不能用单位映射/服务端白名单证明已有数据。温湿度metadata、跑姿、events/activities可选保留；直接RunningCadence在本机iPhoneOS SDK26.5不存在，当前详情页步频可来自StepCount派生。当前恢复/readiness是load-only，暂无自有VO₂/跑力/CS；详情页请求时计算和持久分析结果有明确边界。真实数据数量、采样分布与个人参数是否存在本轮均NOT VERIFIED。

推荐新栈：统一时间支持/暂停/来源与逐模型qualification；HRR时间积分归一化RAI_AU和180日有限指数负荷核；速度—持续时间曲线支持的60min表现映射固定0–100跑力；有条件稳态速度/HRR VO₂估计，Apple结果独立；个体SDNN/RHR/睡眠基线生成生理proxy，结合已知负荷残余生成readiness。普通训练默认“已展示能力”，缺最大努力/长时证据不伪造最大能力；没有上传不代表休息。参数、质量阈值与恢复评分均标明工程选择及待校准项，未宣称科学效度已验证。

文档包含DAG、公式/单位/门槛/冷启动、异常处理、confidence/evidence、拟议通用artifact/通道/外部输入schema、纯Go接口、迟到/删除依赖闭包、无Workout刷新、历史六个月候选回算/受控publication、SwiftUI DTO和M0–M7实施阶段。复用现有jobs/dirty/run/result/publication，不建立独立模型服务或每算法一套基础设施；旧模型标签、原始事实与上传回执不改。下一阶段为M1 Feature Foundation；日常健康同步扩展与后续实现均未在本轮执行。

实际检查：工程本地Go工具链 `go test -race ./...`通过（既有测试缓存命中）、`go vet ./...`通过、依赖边界通过（249包）。文档22章/JSON/围栏/Markdown表/隐藏格式字符检查通过，`git diff --check`通过。临时合成公式演算验证参考HRR60min=60AU、强度分段71.743050815AU、VO₂候选51.5、跑力示例50、readiness指数关系；400日有限EWMA递推与直接卷积误差≤2.14e-14，线性HR解析积分与独立数值积分误差≤2.23e-16。临时核算记录 `/tmp/rai-model-design-20261102/formula-checks.json`，未引入生产Python或新测试服务。

未验证：本轮无事务改动，未运行真实PG集成；新模型尚未实现，完整unit/golden、噪声/step response、通道迁移/发布竞争、API/iOS合同、真实HealthKit采集和外部生理准确性均NOT RUN。现有Go测试/公式演算/文档存在不能替代上述验收。完整Recovery发布受日常数据链路、睡眠与训练覆盖约束；VO₂出值受可信HRmax/RHR与稳定段约束；最大能力及高精度声明受独立表现/生理验证约束。可以进入分阶段编码，不等于可以上线完整评分。

## 2026-11-02 — M5A Daily HealthKit Facts Sync Foundation

**M5A RESULT: PASS WITH RISKS。REAL DEVICE: NOT RUN。** 详细 baseline、十项代码审计、实现/测试/风险见 [M5A_RESULT.md](M5A_RESULT.md)，正式约定见 [DAILY_HEALTH_SYNC_CONTRACT.md](DAILY_HEALTH_SYNC_CONTRACT.md)，实际机器证据见 [M5A evidence](evidence/m5a-daily-health-2026-11-02.json)。本段为追加记录，之前阶段及原模型方案保留。

Baseline：正常 iOS anchored discovery 围绕 Workout/关联子项；既有 secure anchor、atomic Outbox、generic health_samples/stream/batch/receipt/tombstone/log/dirty 与 dataset/collector fencing 可复用，但没有独立 HRV/RHR/Sleep 的 ACK 后 anchor/coverage 闭环。普通 Event source 只有 bundleID；Workout observation 的 HealthKitSource revision/device 描述可直接复用。没有恢复旧 generic HTTP 写路由。

新增 Daily Facts Sync v1，独立于 Workout Protocol 3：SDNN/ms、RestingHeartRate/count-min、Sleep raw category intervals；第三方/manual/unknown source 与允许 metadata 保留，未知枚举/metadata 不静默丢弃。按 HealthKit UUID immutable identity，RHR delete+new、重叠 sleep、多来源均不按日折叠。iOS 三个固定查询 predicate 与独立 secure anchor，默认首次90天/contract最多180天，128 changes/512KiB、byte 超限缩 query 不截断；原字节 pending 落盘后重试，仅完整 durable ACK 后原子推进 anchor。大整数 syncVersion 保留 Int64 精度。App/foreground/“同步日常健康”手动按钮/observer hint/前台机会触发；已追平类型同代次查询冷却，持久wake generation防止查询中通知被旧ACK清掉，pending/最久未确认优先交付，真实429下3类型恰好3 facts/3 receipts且Sleep不饥饿；observer completion 不等网络，不承诺 iOS 固定后台频率。

Migration 20 只加 daily_health_coverage；facts、删除屏障、commit log、每 UUID 绝对 dirty interval、coverage、receipt 共用 dataset 行锁/同一事务，复用旧表与 upload lease，commit_rev 不用 sequence。source revision/device 写入 raw metadata 保留键并参与 identity hash。未知 delete 不编造时间范围。没有新 Recovery handler，ACK 明示 awaiting_model_implementation。schema Ready、明确 runtime grants、sqlc model 及管理工具当前版本门禁同步更新；管理命令不被同步导入、不成为热路径依赖。

真实验证：隔离 Postgres.app **18.6**，四项耐久设置 on、无 TCP；全量 race integration 通过（integration 252.519s），最终 M5A+迁移/权限/HTTP/故障定向 race 再通过（integration 27.578s、admin 1.488s），leftover=0、cluster removed=1。包括新/重复 HRV、delete-before-create/迟到 retry、同 UUID 冲突、RHR replacement、六种 sleep overlap、coverage、generation/collector/upload fencing、7张表写失败回滚、事实写后 backend termination、内部错误脱敏及受限 API role 实际读写。Swift→HTTP→PG 真实子进程 SIGKILL 三点（上传前/上传中/commit后anchor前）重启均只有1条事实/1张回执，原字节 replay、anchor restored。

`swift test --package-path ios/RAI`：119 Swift Testing / 15 suites，2普通XCTest通过、3既有opt-in入口明确skip；DailyProbe live synthetic另由PG harness执行。全新一次性模拟器 App **34/34 PASS**（本地 ad hoc 签名，含 SDK enum/unit/metadata、anchor secure archive/restore、三类型离线pending/ACK/第二次anchor query、大整数metadata和既有Session/Workout回归），测试模拟器已删除；generic/platform=iOS 无签名构建也通过。首轮 unsigned 的两项既有 Keychain -34018 失败经完整 signed rerun 通过，未跳过断言。anchor save 文件替换故障注入通过；不把 macOS synthetic SIGKILL 当作 iPhone kill。

收尾 `go test -race ./...`、`go vet ./...`、边界检查249包、sqlc v1.31.1 generate、OpenAPI lint（31既有warnings，无error）、git diff --check 通过。非race 7/30/90/180d（每天32条合成sleep）bootstrap为62.68/225.86/671.51/1371.28ms，hot单变化2.80/1.93/1.95/2.03ms；32/64/128 batch 对比支持128边界。完整 payload、parse/PG transaction、fixture processing、进程alloc/heap见证据；没有用本地结果冒充真机query或生产SLO。

未验/风险：P1为真实iPhone HRV/RHR/Sleep、代表性metadata、修订/删除、后台delivery及kill/suspend验收未完成；未知字段有意阻塞该type等待升级。长期离线HealthKit删除保留/授权变化补读政策、原子rename内精确OS kill、断电、设备query/peak memory/功耗、fnOS/公网/生产未验。没有读取/修改生产DB，没有导出个人健康fixture，没有部署/push/真实migration。Apple VO₂max及HR Recovery延至M5A.1；M5B任何评分/算法均未实施。补齐只读真机门禁后再评估M1；本轮在M5A停止。

最终故障轮还明确在全部事实/coverage写入后对最终receipt UPDATE注入失败，并验证同批delete胜重复upsert；完整回滚/重试均通过。新增wake generation/查询冷却/公平交付回归，以及真实Swift→API→PG共享429回归均通过。

最后补齐握手失败阶段的日常pending持久退避（复用既有recovery转换），新增包测试通过；最终客户端二进制三点SIGKILL及真实429复验通过（4.995s），App 34/34与原生iOS构建再次通过。额外真实HTTP/auth/admission性能轮（150.034s）保留30/min、burst2预算：7/30/90/180d bootstrap=0.06/12.21/42.60/86.23s；紧接bootstrap的hot因预算等待约2s。前述毫秒表是Store路径，不是全链路上传SLO。完整数据与实际命令已写入机器证据；tagged Go vet也通过。

## 本地开发环境补齐（2026-11-03）

用户要求在新克隆的 `/Users/ta/RAI` 补齐开发环境。本机仍无 Go、Docker、Homebrew 与 Node，`sudo` 需要密码，因此继续使用工程内、被 `.gitignore` 排除的无 sudo 原生路径，不安装系统级软件。此前 2026-10-30 的环境记录针对 `/Users/tp/rai`，本克隆内 `.local` 为空；本轮按当前源码重新安装并复验，历史段不改写。

补齐与修复项（都以本机实测为准，不以文件存在代替验收）：

- **工具链重装**：`scripts/local/bootstrap.sh` 安装 Go **1.27.3**（`b6f8e931…750`）、PostgreSQL **18.6**（Postgres.app 2.9.6，`9fc7d0dc…defb`）；本轮把 CI 已固定但本机缺失的 **sqlc 1.31.1** 一并纳入 bootstrap，落到 `.local/bin/sqlc`（darwin-amd64 `c5af7677…d519b`、darwin-arm64 `21602158…c71e`）。sqlc 上游不发布校验文件，脚本按 HTTPS 发布资产实测摘要固定，下载变化即失败。
- **集群与栈**：`pg.sh init` 在 `.local/pgdata` 新建仅回环 `127.0.0.1:55432`、UTF-8、`timezone=UTC`、data checksums 的专用 cluster；`stack.sh build|migrate|start` 构建并显式迁移，API/Worker 同源二进制运行，`/health`、`/livez`、`/ready`、`/readyz` 均 200。
- **修复迁移数门禁失效**：`scripts/local/verify.sh` 原先把期望迁移数硬编码为 `13`，而本仓库已有 20 个 migration，验收在本机稳定失败（`migrations applied=20 total=20` 却判 FAIL）。现改为从 `apps/server/migrations/*.sql` 实际计数比较，避免再加 migration 后门禁静默过期。
- **修复版本号漂移**：`rai version` 原输出 `"schema":19`，而当前 migration 已到 20、`admin` 的 `ops verify` 与 P7 集成测试都以 20 为门禁。新增 `migrations.LatestVersion()`（从嵌入 FS 解析最大版本号），`rai version` 改为派生值，本次实测输出 `"schema":20`，不再需要手工维护该常量。
- **本地可校验生成代码**：`scripts/local/test.sh` 增加 `sqlc generate` + `git diff --exit-code -- internal/storage/postgres/dbgen`，与 CI 同一门禁；本次 sqlc 1.31.1 重新生成后工作区无差异，说明提交的 `dbgen` 与 1.31.1 一致。

实际验收结果：

| 验收项 | 实际结果 |
|---|---|
| 空库迁移 | goose 00001–00020 全部 `applied`，20/20；schema 版本 20 |
| API/Worker | API 与 Worker 同镜像二进制启动；`worker-ready` exit 0，`worker_started`（handlers=5，processing_slots=1），进程存活 |
| 健康检查 | `/health`、`/livez` 200 `alive`；`/ready`、`/readyz` 200 `ready` |
| 备份与恢复 | `pg_dump -Fc` 生成 dump（最终一轮 SHA-256 `a08262f8…c110`），恢复到新库后 schema 版本 20、public 表 48；临时恢复库已删除 |
| 质量门禁 | `scripts/local/test.sh` PASS：gofmt、`go mod verify`、单元、`go vet`（含 integration tag）、build、sqlc 生成无差异、`-race -tags=integration ./...`（最终一轮 integration 456.036s）、依赖边界 249 包；一次性 `/tmp/rai-pg.*` cluster `RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1` |
| 版本输出 | `rai version` → `"schema":20`、`sync_protocol:3`、`openapi:1.6.0`、`go:go1.27.3`（openapi 原为 1.5.0，见下方追加修正） |

机器可读证据：[本地环境](evidence/local-dev-environment.json)、[测试结果](evidence/local-dev-tests.json)；原始测试日志为 `docs/evidence/local-dev-tests.log`（`*.log` 被 `.gitignore` 排除，仅本机留存，JSON 内记录了其 SHA-256）。

未验证/限制：本机仍无 Docker，`deploy/dev` Compose 路径本轮未重跑；无 Node，OpenAPI lint 本轮起初未在本机执行（同日追加小节已补装 Node 并实跑）；只有 Command Line Tools、没有 Xcode，iOS 构建与 Swift 测试本轮未运行；未执行 `rai init`、未接触真机/HealthKit、TLS、生产角色、离机或加密备份，未部署、未推送、未迁移真实数据。**LOCAL DEV ENVIRONMENT: PASS（Go/PostgreSQL/sqlc 与合成数据栈）；PRODUCTION RELEASE READY: NO（不变）。**

### 补充：本机 OpenAPI 合同 lint（同日追加）

本机原先没有 Node，因此合同 lint 一直只在远端/CI 执行。本轮把 Node 纳入同一条无 sudo 原生路径，并实跑一次：

- `scripts/local/bootstrap.sh` 增加 Node **22.23.2**（darwin-x64 `58e99022…026`、darwin-arm64 `61130f39…5c6`，取自上游 `SHASUMS256.txt`），安装到 `.local/toolchain/node`；npm 缓存固定在 `.local/npm-cache`。除合同 lint 外，Go 门禁与 `server.yml` 仍不使用 Node。
- 新增 `scripts/local/contracts.sh`：以固定的 `@redocly/cli@2.71.1` 执行 `npm exec --yes --package=@redocly/cli@2.71.1 -- redocly lint contracts/openapi-v2.yaml`，与 `contracts.yml` 同一检查。
- 实测结果：**0 error、31 warning**，与仓库既有基线一致（既有 warning 未屏蔽、未顺手改写）。证据：[合同 lint](evidence/local-contracts-lint.json)，其中记录合同 SHA-256 `13095a88…a450` 与日志摘要；原始日志在 `.local/logs/contracts-lint.log`。
- 同时修正版本号漂移：`rai version` 原报 `"openapi":"1.5.0"`，而 `contracts/openapi-v2.yaml` 与 `contracts/CHANGELOG.md` 的当前条目均为 **1.6.0**（M5A Daily Facts Sync v1）。现输出 `"openapi":"1.6.0"`，实测 `{"schema":20,"openapi":"1.6.0","sync_protocol":3,...}`。该字段只做元数据声明，不改变任何路由或载荷。改动后重跑最终一轮 `scripts/local/verify.sh`（20/20、schema 20、48 表）与 `scripts/local/test.sh`（integration 456.036s）均 PASS，证据文件已按最终树重新生成。

lint 的边界：它证明 OpenAPI 文档合法、自洽，**不证明** Go 处理函数按合同实现；实现一致性仍由真实隔离 PostgreSQL 集成测试与运行时握手元数据覆盖。仍未验证：本机无 Docker，`deploy/dev` Compose 与 `deploy/release` 未重跑；无 Xcode（仅 CLT），iOS 构建与 Swift 测试本轮未运行；未部署、未推送、未迁移真实数据。**LOCAL CONTRACT LINT: PASS（0 error / 31 既有 warning）；PRODUCTION RELEASE READY: NO（不变）。**

## M5A-RD — 只读真机验收（2026-11-03）

**M5A-RD RESULT: PASS WITH RISKS；M5A CLOSED FOR DEVELOPMENT。** 本轮使用实际已连接的 **iPhone 12 Pro Max / iOS 27.2**，Xcode 26.6，正式 RAI **0.6.0 (1) Debug 签名 target**，手机 API 报 HealthKit available=true。原 M5A 的合成 PASS WITH RISKS 与当时 REAL DEVICE: NOT RUN 保持历史原文，由 [本次结果](M5A_RD_RESULT.md) 和 [脱敏机器证据](evidence/m5a-rd-2026-11-03.json) 补齐本阶段门禁，不能据此把旧完整 Workout/生产验收也改为通过。

实际只读链路：HRV SDNN **3,116**、RHR **83**、Sleep **1,594**，合计 **4,793** 个唯一原始事实，**39** 个有事实页。每类型记录到一次 nil-anchor 初始 90 天查询，后续均 committed-anchor 续页；没有扩大历史范围。真实 HK 对象的 identity/interval comparison、sourceRevision、可用 device context 保留通过，全部 4,793 条来源 JSON 与 source identity 匹配；包含 **15** 条第三方 RHR。Sleep 实际五类、重叠区间保留；inBed 和 manual flag 未自然观察到，类型名不用于推断来源/授权/模型资格。

同一正式手动按钮由用户实际点击，三类第二次 query 都是 **0 additions / 0 deletions / usedCommittedAnchor=true**（约 2.65–5.91 ms），事实不重入。真实 SIGTERM 重启恢复 anchor/bootstrap/window/pending；真实 discovery 后 upload 前 SIGKILL 保留同批同内容；后端不可用时 anchor 不前进，恢复后原 pending 交付。隔离代理另让 API/PG 提交一个真实 App 批次后丢 ACK：服务器按 body 指纹恰有一条 committed receipt、客户端尚保留原 pending，再对 RAI SIGKILL；重启后同批/同 body 恢复、原回执字节指纹不变、最终提交 candidate。没有把这个窗口称为已收到 ACK 到 anchor rename 系统调用内部的精确 kill。

真实 HKObserverQuery 注册、enableBackgroundDelivery 返回，显式 observer 阶段 **3 次 active + 6 次 inactive** callback 均先落盘 wake hint，再 anchored query。驱动连续 8 次调用同一手动入口，用户做约 30 秒桌面窗口及快速前后台切换；4 次真实 background/返回 active 后全部追平，无 nil-anchor 重扫、重复有效事实或类型饥饿。诊断只在已验证测试身份上使用现有 Daily 状态机/shared scheduler，排除不相关 Workout 发现。**没有观察到明确 UIKit background callback**，不将 inactive 转场回调当后台交付，不承诺系统定时唤醒。

隔离后端为本轮全新 **PostgreSQL 18.6 / schema 20**、专属 Unix socket、受限 API role（RuntimeRoleSafe 通过），fsync/full_page_writes/synchronous_commit/data_checksums=on。初始 0 facts/0 receipts/0 coverage；最终 **4,793 facts = 4,793 unique identities / 141 receipts / 3 caught-up coverage / 0 uncommitted batches / 0 duplicate receipt groups**。141 包含 39 有事实批次和 102 空 coverage；空页较多含早期驱动重复 manual wake 的校准，驱动已改成同 wake 续页，未修改 M5A 协议或放宽限流，不能作为最小生产请求/性能基准。query-staged 诊断共 138 个，少于 server/本地 chain 的 141 批，3 个跨进程空页 query 事件缺口原因未最终确立；39 个非空页/4,793 事实计数完整，最终批次数以数据库和 durable stream 为准。客户端 sequence **49/46/49** 与服务端 **48/45/48 + 1** 一致。另有 4,793 个 dirty scopes 明确待模型实现，没有真实 Workout 上传或模型任务；141 个 ACK 全部明确 awaiting_model_implementation，不伪装分析完成。

本阶段必要改动限于受控 **DEBUG diagnostics**、私有构建的精确 Debug HTTP host/ATS 配置，以及手动重试旧错误提示清理。初期诊断导航遗漏 ProductTabs 的 dark scheme，白字落白底；修复后用户确认显示正常并实际点击按钮。Debug 控制使用显式 launch flag、消耗式私有配置和身份校验；报告只有计数、分类、耗时、不可逆指纹，没有值/原始 UUID/睡眠时间/详细 metadata。Release 构建成功，四个诊断标记在二进制中均不存在。收尾发现 Xcode 不替换 ATS 字典键，已撤回源模板变量键实验；仓库 Debug 模板保持默认，私有生成 plist 以字面量精确主机通过 INFOPLIST_FILE 供本轮签名构建，实际 domain/host 匹配且无全局 arbitrary loads。同一测试 PG/API 另实跑停机重启，不迁移/reset/restore，不换身份，手机仍增量且事实数不变。设备/App 枚举原始 JSON 临时处理后删除，API 输出禁存、代理无 body/header 日志，PG 日志参数/健康失败行检查均 0；未复制手机 state/payload 或 PG dump 入仓库。

验证：Swift **119 tests / 15 suites** 通过（另 XCTest 2 pass、3 既有 opt-in skip）；最终隔离 iOS 26.5 Simulator **34/34 SDK/App tests** 通过且自建模拟器删除；Debug 真机签名安装运行、Release unsigned 编译通过。`go test`、`go test -race`、`go vet`、249 包边界、sqlc 1.31.1 生成一致性、OpenAPI **0 errors / 31 既有 warnings** 通过。完整 PG race 首轮默认 10 分钟进程时限超时（残留测试 DB 随集群整体移除），以 `-p 1 -timeout=30m` 重跑 integration **419.787s PASS**，残留 DB=0/cluster removed=1；真机结束后的 Daily/coverage/fencing/rollback/HTTP/受限角色/migration 定向 PG 另复验，最终 integration 12.992s / admin 1.822s PASS，残留 DB=0/cluster removed=1，精确命令见机器证据。原失败保留，不调整生产语句或事务超时。

保留 P2/P3 未验证项：自然 HK 删除、RHR/Sleep revision、unknown category、真正 background callback、received-ACK/rename 内精确 kill、DST/travel、长离线 deletion retention、物理断电、自然 hot delta、iPhone memory high-water/功耗、TLS/WAN/生产部署。删除、修订、unknown/DST 等继续由既有 synthetic/PG/SDK 证据补充；没有制造或修改 HealthKit 数据。本次 query/normalization/batch/delivery 工程计时见结果，不宣称生产 SLO（ACK 丢失时后台会话恢复一次约 90.4s）。

收尾停止手机 RAI 和本次 API/PG；真实测试库与凭证仅在仓库外私有临时目录保留，手机保留隔离身份与 anchor，后续使用需显式恢复测试服务。Git baseline `main / bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61` 及既有未提交文件保留，本文件只追加；没有 reset/stash/commit/push、生产部署/迁移、切旧入口或改 `../running`。下一阶段仅推荐 **M1 — Feature Foundation & Publication Substrate**，没有自动开始 M1 或 M5B/任何 Score/模型。

## 2026-11-03 — M1 Feature Foundation & Publication Substrate

**M1 RESULT: PASS WITH RISKS。NO NEW USER METRIC ACTIVATION。** 正式合同：[WORKOUT_FEATURE_CONTRACT.md](WORKOUT_FEATURE_CONTRACT.md)；完整11项结果：[M1_RESULT.md](M1_RESULT.md)；重复审计：[M1_FEATURE_DUPLICATION_AUDIT.md](M1_FEATURE_DUPLICATION_AUDIT.md)；机器证据：[M1 evidence](evidence/m1-feature-foundation-2026-11-03.json)。本段只追加，不改写 M5A/M5A-RD 或原模型方案。

Baseline：`main / bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61`，13 modified + 5 untracked 均先记录 SHA-256。M1 保留开工前所有 iOS、M5A-RD、环境脚本和证据字节；本文件原有字节前缀完整。未 reset/stash/commit/push，未改 `../running`，未接触生产或常驻开发数据库/真实健康数据，未部署或切旧入口。

实现：新增纯 `workoutfeatures`，算法 `rai.workout_features/v1`、schema `rai.workout_features/1`、独立 `features-v1-92522339887e` candidate scope。typed RawWorkoutSnapshot 一次 RR 快照读取 generation/revision/DB AsOf、Workout、quantity/statistics、sealed series/routes、events/activities、metadata/enrichments、完整来源、删除状态和 as-of 参数；所有输入有界，超限明确失败不截断。缺失不补0、不把 unknown indoor 设false。series query未完整证明的 parent 保守为condensed，sealed child只替代自身parent。共享 manual/motion pause 状态机、同源同active且≤15s的HR/speed插值、observed/reconstructed coverage、来源epoch/冲突排除、距离来源/cross-check、完整100m精度过滤中位邻域grade、1s临时分析索引、3/5/10/20/30/40/60/90min持续窗口和非重叠备选、5min steady及long-run halves、cadence proxy/动态摘要和五类qualification。平路比例按共同支持距离计算，长跑各半单独验证地形。非running保留enum与primitives，但不给running栈资格。旧 analysis/detail 只抽取同公式 finite/cadence proxy/haversine 纯原语；旧feature/TRIMP/state/训练消费者语义不变，分析不读presentation输出。

Migration21新增通用 artifacts/model_channels/model_inputs/dependencies，扩展既有admin audit；1–20 SQL与checksum全部不变，尤其M5A20不改。artifact/input append-only，terminal run不能追加；candidate绝不覆盖legacy active。复用现有dataset锁、job lease/fencing和PublishAnalysis事务，原子提交artifact/result/publication/downstream dirty/clean/run/job/attempt。事实变化、staged series/route注册与seal、删除、来源/metadata、相关参数可dirty候选；删除在事实提交时立即撤下current reference。未来模型仅dirty/invalidate，不创建未实现handler或假成功job。新产品stack promotion/rollback仍受明确消费者/发布门槛约束；M1的DB约束只允许active=legacy。无新sequence或第二套生产锁。

真实 PostgreSQL **18.6**：隔离Unix socket、无TCP，fsync/full_page_writes/synchronous_commit/data_checksums全部on；empty→latest、20→latest、重复迁移、旧事实/审计保持、受限API/Worker权限、稳定snapshot穿插新generation、parent/route/workout删除、late seal、参数冲突/失效、旧generation/lease fencing均PASS。artifact/results/publications/runs/dirty/jobs/attempts七点写入失败回滚通过；artifact写后/publication前/dirty clean前三次真实backend termination均无部分结果、无错误clean且可重试。破坏性Down被既有checksum门禁拒绝，不能称作schema21→20/旧二进制回退验收。所有验收runner均leftover DB=0、cluster removed=1。

最终验证：`go test ./...`与`go test -race ./...`分别426实例PASS/0FAIL（含subtests，4既有HTTP production-budget opt-in skip）；`go vet ./...`及integration-tagged vet、go mod verify、gofmt、251包依赖边界、sqlc1.31.1重复生成4文件一致、git diff --check通过。完整PG race `-p 1 -timeout=30m -tags=integration ./...`：876实例PASS/0FAIL、9显式opt-in skip，integration445 PASS/5 skip、469.617s；最终feature/migration/login定向race另35 PASS（integration44.945s），最新序列化/事务路径复验。5s input-order/duplicate fuzz最终290 executions PASS。OpenAPI未改，额外固定Redocly2.71.1 lint为0 errors/31既有warnings，未改写开工前lint证据。

Swift：本机CLT首轮缺Testing；使用已安装的 `/Users/ta/Downloads/Xcode.app`（不改系统选择）复跑119 Swift Testing/15 suites PASS，XCTest2 pass/3既有opt-in skip。隔离 `integration,m5aclient` 补充41 PASS：实际macOS DailyProbe→HTTP→PG上传前/上传中/提交后SIGKILL三点均1 fact/1 receipt/anchor restored；真实429共享预算3 facts/3 receipts、Sleep不饥饿。它不是iPhone kill或新真机M1验收；没有重复读取真实HealthKit。

性能（本机non-race synthetic，非SLO）：PG载入+特征计算20m/45m/2h/4h分别 **26.032/43.797/117.204/227.553ms**，artifact **8,825/13,268/25,584/42,064 bytes**，均<128KiB。dense pure-domain另实测normalization/timeline/route/window/serialization/total及allocations，详见结果与机器证据；4h总计算约426ms、累计分配约336MB，**不是peak RSS**。PG workload为3 scalar parents+每10s HR series+1Hz GPS，dense workload为7 channels每5s+1Hz GPS，不能直接相加或冒充生产负载。

风险/未验：未发现P0/P1阻塞项；P2为真实eligibility/真实GPS grade/生理效度、严格representation和HR trend门槛下的实际可用率、production load/peak memory/并发/WAN/TLS/部署。M2非线性HR剂量必须在共享feature层增补版本化exposure，不能拿meanHR冒充积分或另写raw parser；M1没有提前实现剂量/分数。P3为ultra-long/dense输入上限、legacy detail后续显式版本迁移、schema21发布/角色刷新/二进制回退验收。本轮不把合成/PG通过当真实测量效度或发布许可。仅推荐 **M2 — Training Load**；已在M1停止，没有自动进入M2。

## M2 — Training Load V1（2026-11-03，Asia/Shanghai）

**M2 RESULT: PASS WITH RISKS。** model implementation validated; physiological calibration pending。完整16项报告：[M2_RESULT.md](M2_RESULT.md)；正式合同：[TRAINING_LOAD_V1_CONTRACT.md](TRAINING_LOAD_V1_CONTRACT.md)；机器证据：[m2-training-load-v1-2026-11-03.json](evidence/m2-training-load-v1-2026-11-03.json)。仅合成数据、一次性PG18集群；没有生产/真实设备/远端入口操作，没有commit/push/deploy，未启动M3。

- M2 BASELINE：`bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61` / `main`，已有32 modified + 23 untracked；完整状态及已有文件hash见`evidence/m2-baseline.json`。保留M1/M5A-RD/iOS/本地环境工作；原方案与HEAD一致，migrations1–21及已有workoutfeatures实现相对baseline字节不变。本文件此前字节前缀保留，仅追加本节。
- 新增独立`rai.hr_exposure/1` companion，复用M1 pause/HR support/source/outlier/parameter resolution；不修改冻结`rai.workout_features/1`，M1-only作业不要求新artifact。source dictionary+7项tuple精确表示线性HR段，只合并相同常数段；20k段/2MiB有界，超限明确失败。4小时每秒变化合成fixture=14,400段、778,306 bytes，round-trip/hash/积分通过。
- 新纯函数模型`rai.training_load.hrr_integral/v1`：HRR非线性active解析积分；中心化小z展开避免近等端点差分。单位RAI_AU=HRR .65持续1分钟。HR参数显式、版本化、source-aware、effective-dated/as-of；无age/default/workout extrema推算，缺参数null。HRmax冲突零容忍、无clamp。IANA解析在loader；纯计算接收内存日历，日期边界hash进入input/lineage，不读取clock/DB/HTTP/filesystem/random。
- 明确区分hrr_integral complete、gap_scaled partial、summary approximation partial（cap .35）和insufficient null。保留observed/estimated/coverage/scale/method/lineage/evidence；gap假设显式，summary不能替代已有trace或重复相加；未知active seconds为null，短Workout不归零，外部active/distance独立保留。无plan/pace/power混分。
- 新增唯一专用`analysis_load_ledger`，immutable run/day/workout贡献及参数/feature lineage；current复用既有candidate publication。按真实IANA本地日积分切分，跨午夜/DST 23/25小时及守恒通过。未知AU不变0；无记录0仅为recorded running load，不证明休息。
- `rai.load_state.exp_finite/v1`：7/42日τ、180日ring与O(1)daily recurrence；knownContribution/unknownWeightMass分离，未知窗口完整数值null；acute/chronic/balance/ratio/trend7，首42日partial，historyDaysObserved与zero_before_recorded_history初始化假设明确。finite kernel day180贡献0；**trend7按定义另有7日依赖尾部**，d..d+186可更新打包state行，但acute/chronic仅d..d+179改变，未改成无限EWMA或187日核。
- 复用analysis runs/results/artifacts/publications/channels/dependencies及唯一PublishAnalysis事务：artifact/ledger/result/pointer/downstream dirty/clean/run/job/attempt原子提交。原事实事务即刻失效feature→load→state，旧generation只SUPERSEDED。active保持legacy；future模型保留dirty，无Recovery/Readiness/Ability dummy handler，未改变training generator或正式iOS首页/API。
- 新migration00022（schema22，public tables53，新sequence0），原1–21 checksum不变。真实隔离PG18.6 empty→latest、21→22、20→latest、repeat、runtime/API/Worker/admin权限及历史保留通过；Down明确拒绝破坏性回退，不宣称22→21 schema rollback。fsync/full_page_writes/synchronous_commit/data_checksums均on、无TCP；全部runner leftover DB0 / cluster removed1。
- PG故障注入artifact/load result/ledger/state artifact/publication/dirty clean/job terminal七项均rollback/retry safe；实际pg_terminate_backend四阶段、计算后Worker lease-expiry/requeue、load/state generation race、删除、参数回溯、late arrival、bounded rebuild、本地day advance通过。受限Worker实际完成feature/load/state；API无ledger/artifact访问/参数写权限，Worker不可改历史/原始事实。
- 数学：60 AU、71.743050815 AU正式向量；独立8192-panel Simpson oracle、200随机/近等向量；duration/intensity/split/merge/1s-vs-5s/instant支持性质；25×480日×2核直接卷积比较；180轮事件重建及120轮cached bounded replacement vs完整450日重建；六类450日athlete fixture通过。
- 最终真实命令：`go test -count=1 -json ./...` **460 tests/subtests、125 top-level、0 fail**；`go test -race -count=1 -json ./...`同计数通过；`go vet ./...`通过；全量真实PG18 `go test -race -count=1 -json -tags=integration ./...` **935 tests/subtests、350 top-level、0 fail**；M1/M2 PG定向 **52 tests/subtests、23 top-level、0 fail**。完整日志本地保留`.local/logs/m2/`，机器证据记录计数/skip/退出码/SHA-256。
- 边界检查通过（online依赖252 packages，不达admin/migrations/goose；pure domain禁止IO/clock/random）；sqlc1.31.1二次generate字节一致；`git diff --check`通过。M2未改Swift/wire/UI；未将Simulator/真机或已有iOS工作伪记为本阶段验收。
- 性能8项PG矩阵+8项Go benchmark通过。20m/45m/2h/4h load compute约0.025–0.057ms，feature read约2.24–2.80ms；365日真实synthetic ingest形成ledger后，read33.688ms、state compute1.495ms、publish172.412ms、artifact172,039 bytes。20m事务本次23.704ms（其他4.17–4.59ms），完整原始单次观测均保留，不挑选更快样本。ledger/pointer分段由test-only timing trigger测量；不是生产SLO。
- 风险：验证范围未发现P0/P1。P2保留未校准coverage/interpolation/conflict/summary cap、零冲突策略eligible率、20k段/2MiB/10k ledger rows/552输出日/366日1000Workout显式重算容量限制；超跨度pending scope当前没有分段自动修复器，fail closed，不能宣称任意多年稀疏待办自动完成。seed history需先明确enroll/current；trend7额外7日尾部为原“所有state严格179日”字面要求的数学依赖例外。P3本地单次性能不外推生产；未支持任意parameter ID强制覆盖历史，只使用明确effective-dated/as-of身份。
- **REAL DEVICE: DEFERRED；REAL DEVICE ACCEPTANCE: DEFERRED。** NOT VERIFIED：真实Apple Watch HR采样/gaps/coverage、插值适用性、HRmax冲突频率、summary-only率、实际eligibility率、间歇行为、个体HRmax/RHR质量、生理有效性、负荷与真实疲劳关系、production、WAN、断电、real-device/real-world calibration。没有FULL PASS/PHYSIOLOGICALLY VALIDATED结论。

Recommended Next Phase: **M3 — Performance & Running Ability**。未自动开始M3，M2完成后停止。


## 2026-11-03 — M3 Performance & Running Ability V1

**M3 RESULT: PASS WITH RISKS。** 保持candidate-only。正式合同：[RUNNING_ABILITY_V1_CONTRACT](RUNNING_ABILITY_V1_CONTRACT.md)；17项报告：[M3_RESULT](M3_RESULT.md)；真实命令/计数/日志hash/性能：[m3-performance-v1-2026-11-03.json](evidence/m3-performance-v1-2026-11-03.json)。

- M3 baseline：HEAD `bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61`、main、已有32 modified+43 untracked状态条目，完整状态/hash见m3-baseline.json。保留M1/M2/M5A-RD/iOS工具链改动；不reset/stash/commit/push/deploy，不改`../running`。原方案、migration1–22、M1/M2纯领域与Swift共119个frozen路径经hash比较不变，本文件只追加。
- 定义冻结：可比平路连续跑条件下，近期事实支持的60min demonstrated表现。唯一输入M1 WorkoutFeatures；只取20/30/40/60/90min best eligible/conditional窗口；backup与3/5/10min不参与。typed evidence保存Feature run/generation/version/known-at、Workout/window时间、source epoch、terrain、quality/qualification/evidence。每Workout总权重≤1，重复不增独立数/n_eff/confidence。无M2 load、VO₂、Recovery、HR猜maximal、跑姿/经济性或比赛预测。
- `rai.performance_curve/v1`：D(t)=D30(t/1800)^γ，log域80% weighted quantile，γ=.700…1.000/.001 deterministic grid；tie优先较大γ再较小α。边界.700、median residual>.06拒绝；≥5独立Workout、跨度≥14日、短/长组各≥2、≥40min且最大1.5x外推。v20/v60/retention=3^(γ−1)完整保留；RunningAbility仅由v60固定对数scale映射，3m/s=50，不重复加Endurance，不做population percentile。raw/unclipped/saturation与float64保留。
- 单Workout稳健：>10%孤立高值等待±5%独立确认，整Workout隔离；逐Workout LOO v60变化>5%则unstable/null。未来事件/known-at/确认不得进入过去target。默认current计算明确为revised，as_known限制当时Feature可用时间；历史immutable run不改写。迟到>90日不经smoothing seed进入current；每target90日本地calendar，额外13日smoothing seed，96Workout/480点/2MiB artifact上限fail closed。
- 每个新有效evidence day生成target，h−13…h按exp(−k/14)平滑ln(v60)，≥3有效target days才出值。平滑α/γ给出自洽D30/v20/v60/retention及log速度锚点/耐力扩展解释。无daily M3 target job：4日无证据score不变；14日aging、28日stale隐藏、42日expired/insufficient；不伪造detraining。confidence为support score，含质量和独立性，demonstrated≤.55；maximal_supported明确unavailable。
- 复用runs/results/artifacts/publications/channels/dependencies；无新表/ledger。一个performance scope原子发布5个typed artifacts与3个results，candidate独立于legacy active。原事实删除即刻invalid current；publish在dataset锁内逐项核对Feature run/generation/存活，封住无current pointer时旧seed删除竞态；旧Worker只SUPERSEDED并排新generation修复，不写artifact或clean N+1。inspect/history/promotion-plan已验证，promotable=false；不开放正式UI/HTTP/training consumer。
- migration00023追加manifest、artifact约束/trigger、bounded索引与dirty控制；schema23/53 tables、新sequence0。真实隔离PG18.6 empty→latest、22→latest、repeat、权限、fencing、delete与rollback通过；Down明确拒绝破坏历史。fsync/full_page_writes/synchronous_commit/data_checksums均on、Unix socket无TCP；runner结束leftover DB0、cluster removed1。
- 六阶段fault injection（evidence artifact/curve result/ability result/publication/dirty clean/job terminal）和三处真实pg_terminate_backend（after evidence/before publication/before clean）均rollback/retry安全。受限Worker完成M1→M3，API不能读写artifacts/改candidate config，Worker不可改facts/immutable历史。已发布N+1的Feature、lease expiry及95日旧seed删除race均通过。
- 数学/性质：独立pinball breakpoint oracle、score精确锚点与clipping、γ边界/residual/LOO、固定v20 retention单调、v60 score单调、duplicate/backup/短时/重排、40轮insert/delete/late/revision cache vs bounded rebuild、future leakage、IANA/DST、quality confidence、later-day PB不制造target且可诊断。10类Golden Athlete全部通过；单条+30%隔离，单条+5%变化≤2分；连续+5%在42日吸收100% log-speed变化，超过60%门槛。未知能力不变低分，经济性/CS/long-run diagnostic未伪造。
- 最终Go：`go test -count=1 -json ./...`与`go test -race -count=1 -json ./...`各**503 tests/subtests、142 top-level、0 fail**。真实PG全量race回归**997 tests/subtests、374 top-level、0 fail**；最后M3依赖身份guard补强另跑真实PG M3专项**21 tests/subtests、9 top-level、0 fail**，含old-seed无pointer竞态。最终quarantine诊断保留由数学/race及PG性能pipeline再验证；不把专项重复计入全量。vet普通/integration、build、mod verify、边界253 packages、sqlc1.31.1二次generate字节稳定、Go source格式和git diff --check通过。Swift/DTO未改，未新做Simulator验收。
- 性能8项PG矩阵与7项Go benchmark完成：20m/45m/2h/4h M3完整curve+smoothing+LOO约3.38–6.37ms；365日122次raw Workout仅读30 Feature refs/60点，read20.508ms、extract1.658ms、compute39.402ms、publish15.747ms，state56,066B、全部artifacts119,476B。密集90Workout benchmark约2.075s/34.47MB/69,392 allocations；stage curve fit约2.297ms、curve+LOO约10.120ms。保留全部allocations和观测，不外推production SLO。
- 风险：验证范围无未解决P0/P1。P2：quality/quantile/outlier/LOO/residual/confidence/scale未经真实人群校准，严格M1门槛可能降低eligible率；96Workout/480点/2MiB容量上限与未准备seed Feature会fail closed/pending，没有lifetime自动修复扫描。current revised与历史as_known明确分离，不从current Feature冒充过去已知。P3：本地合成性能、WAN/生产/断电/旧二进制schema23回滚未验证。未部署、切入口或迁移真实数据。
- **REAL DEVICE: DEFERRED；REAL DEVICE ACCEPTANCE: DEFERRED。** NOT VERIFIED：real-world performance validity、真实GPS精度/距离一致性、real GPS/speed eligibility、真实speed support quality、Workout/flat-route qualification rate、实际努力/比赛、maximal effort identification、真实Apple Watch sampling、v20/v60 evidence density、single-workout bias、source epoch effects、实际跑力精度、population calibration、production/WAN。没有生理或真实世界验证结论。

**Recommended Next Phase: M4 — Conditional VO₂max。M3完成后停止，未自动进入M4，未正式promotion或产品激活。**


## M4-B2 — Apple Health VO₂max Raw Facts Transport（2026-11-04）

**PASS WITH RISKS；B2 CLOSED FOR DEVELOPMENT。** 依据本次明确启动授权，保持 B1 两份冻结原文。详细范围、命令、skip/失败历史、性能和风险见 [M4B2_RESULT.md](M4B2_RESULT.md)；机器汇总见 [validation.json](evidence/m4b2/validation.json)、[performance.json](evidence/m4b2/performance.json) 和 [compatibility.json](evidence/m4b2/compatibility.json)。

- 独立 `vo2max-facts/v1`、capability、HealthKit VO₂ read type、anchored query / secure anchor、固定90×24h绝对窗口 / fingerprint、typed metadata registry1、provenance/2 与 Device.name。读取授权保持 toShare=[]，无 HealthKit save/delete。metadata 与 testType1–4 保真；未知 key/type/enum、无效单位/非有限/非正值整页 fail closed。非正值按本次启动指令收紧，B1允许非正值的原文保留并在结果报告明确差异。
- 新 optional VO₂ LocalState 不重置 Workout/HRV/RHR/Sleep。body/candidate/hash先持久化再上传；同字节重试；只有完整有效 durable receipt 才原子提交 anchor/sequence/ACK。blocked页与pending为GC root，孤儿/ACK body/transfer cache可有界回收；共享429/503、握手退避、前台timer和observer wake独立处理；永久fencing/内容冲突保留pending并停止自动写入，坏ACK仅清除响应缓存。
- schema00024共享 health_samples/tombstones/streams/batches/logs/dirty/coverage，不建平行 raw DB、projection表或新index/sequence。服务器内部构造 RAI:vo2maxFactV1；旧generic wire拒绝伪造。known tombstone保存无数值的来源/epoch/hash/interval/origin inputs，unknown为null。单事务包含 raw/屏障/logs/dirty/coverage/stream/receipt/dataset revision；dataset锁产生顺序。empty observation也dirty；不排虚假VO₂分析job，ACK固定awaiting_model_implementation。
- 修正runtime/owner schema23硬编码门禁至24，并加owner侧版本库存防回归；online仍不导入admin/migrations/goose。原migration00001–00023、旧Daily源码/normalizer/合同和M1/M2/M3算法目录字节未改；旧v1写入/receipt、Workout v3、变化游标、empty/23→latest/repeat、受限API/Worker角色均经真实隔离PG18验证。
- 最终Go unit与race各523 PASS / 4 skip / 0 fail；全量独立PG18 race串行回归1049 PASS / 11 skip / 0 fail；VO₂/schema gate PG race专项50 PASS，额外九registry key/Int64边界PG round-trip1 PASS；最后source namespace分类补强后的受影响syncing race264 PASS。重复专项不与全量累加。普通/integration vet、module verify、253 packages边界、sqlc1.31.1六文件重复生成字节稳定、diff check通过。OpenAPI固定Redocly2.71.1：0error/原有31warnings；skip与中间失败/修正历史均显式记录，未隐藏全量schema/timeout/50k queued观察。
- Swift Testing129项/16suites通过；Simulator正式App XCTest39项通过；另真实App→normalizer→outbox→HTTP→PG18→ACK→anchor E2E1项通过，实际丢弃一次已commit ACK后仍1事实/1receipt/1dirty；8阶段DB异常注入和3处真实pg_terminate_backend全回滚/重试通过。macOS6个实际SIGKILL阶段通过，不能冒称iPhone kill。四流真实共享429/VO₂两次503后全部完成，不能冒称五流真机后台验收。全部成功PG runner均socket-only、fsync/full_page_writes/synchronous_commit/data_checksums on；结束leftover DB0 / cluster removed1。
- 用户随后提供USB iPhone12ProMax/iOS27.2，独立签名bundle上的4项合成SDK测试通过（metadata/provenance/pending/blocked/observer hint/overlap predicate/performance），未读取真实健康样本、未写健康库；验收包已清理，正式App sandbox/Outbox/Keychain不复用。未访问生产库、未部署服务、未切旧入口、未迁移真实数据，未修改../running；无真实健康值/完整样本UUID/raw metadata/凭证进入新增repo evidence。
- 本机synthetic性能已记录0/1/128、90日bootstrap、hot empty、各超限显式失败、normalization/payload/parse/PG/ACK/local commit/Go allocations及四类真实索引执行计划。单机race/debug/准入等待不是生产SLO；Swift allocations未测。P0/P1无已知未解决工程blocker；P2保留未知真实metadata、来源描述符非硬件认证、固定窗口/删除保留和50k legacy worker时间预算观察；P3保留断电/WAN/生产/功耗/旧binary回滚等未验证项。

**REAL DEVICE ACCEPTANCE: DEFERRED（真实HealthKit与后台范围）；真实Apple Watch来源/VO₂可见性/history update/delete/后台delivery/长期离线/WAN/production/physiological validity未验证。PHYSIOLOGICAL VALIDATION: NOT VERIFIED；RAI OWN VO2MAX MODEL: NOT IMPLEMENTED。** 本次只有raw facts transport，没有external current/candidate/estimated VO₂max或生理置信度。

**Recommended Next Phase: M4-B3 — External VO₂max Candidate Projection。完成B2后停止；未自动开始B3/B4。**

## M4-B3 — External VO₂max Candidate Projection（2026-11-04）

本阶段新增独立纯领域 `internal/vo2maxexternal`、REPEATABLE READ 有界输入、显式 append-only source selection、共享 PublishAnalysis 原子 external artifact/result/candidate pointer、即时失效和发布前完整重验，以及 admin-only inspect/history/promotion-plan。模型 `rai.vo2max.external_projection/v1` 只复制所选 Apple-origin 外部 raw 值；confidence/parameterVersion/featureVersion=null，30/90d Freshen，active 仍 legacy。未实现 RAI estimated VO₂max 或 laboratory current。

完整 17 项报告与真实验证证据见 [M4B3_RESULT.md](M4B3_RESULT.md) / [validation.json](evidence/m4b3/validation.json)。B1/B2 冻结文档、旧 migrations、B2 wire/raw/receipt helpers 与 Swift 保持字节不变；Swift129项回归通过。PG18 专项含受限角色、7阶段失败回滚、3处真实 backend termination、generation/lease/config/selection/session/delete fencing、bounded rebuild 与索引 EXPLAIN。全量门禁状态以 validation.json 最终记录为准，不能由本段文字或文件存在推定通过。

REAL DEVICE ACCEPTANCE: DEFERRED；real Apple Watch source distribution、real HealthKit update/delete behavior、production、WAN、physiological validity 均 NOT VERIFIED。RAI OWN VO2MAX MODEL: NOT IMPLEMENTED。未自动进入 B4、发布或部署。

M4-B3 最终：**PASS WITH RISKS / B3 CLOSED FOR DEVELOPMENT**。最终树普通 Go test/race/vet/build 全 exit 0；Go test/subtest 552 PASS/4既有skip，真实 PG18 race 全量1142 PASS/11既有skip/0fail，补充0/32epoch/256tie及+1矩阵6 PASS。Swift129项、sqlc字节稳定、六条依赖边界、受保护131文件、diff check均通过。所有临时PG cluster已移除，残留数据库0。skip具体为既有M1/M2/M3性能矩阵、旧Phase4指定设备/性能成本矩阵及真实socket opt-in，不冒称本阶段重新验证。下一阶段仅推荐M4-B4，未启动。

## M4-B4 — Simulator End-to-End & Engineering Closure（2026-11-04）

**M4-B4 RESULT: PASS WITH RISKS；M4 EXTERNAL VO₂MAX ENGINEERING: CLOSED。** 完整18节报告见 [M4B4_RESULT.md](M4B4_RESULT.md)，机器证据见 [validation.json](evidence/m4b4/validation.json) / [manifest.json](evidence/m4b4/manifest.json)。起点保留未提交 B3 树；最高 migration 仍25，冻结 B1/B2/B3 数值、原始身份/回执/coverage/anchor、source selection/status/freshness/domain/family 均未修改。唯一生产修复是普通日志身份改SHA-256 hash，当前日志约定已更新；不改 HTTP 回执身份或 X-Request-ID。

正式 Simulator App + synthetic SDK fixture→Normalizer→SyncCoordinator→独立Outbox→BackgroundTransfers/API client→HTTP→隔离PG18→真实Worker Claim/Process→artifact/result/candidate→admin inspect闭环通过：16 recipes及1 hot-empty checkpoint，2次专用full-E2E XCTest；B2原Simulator E2E重跑通过。普通正式App XCTest40 PASS、Swift129/16 suites另列，不冒充完整E2E。fresh实际丢ACK后原bytes重试、ACK后anchor落盘；非整数binary64贯穿raw/artifact/result/internal reader；显式epoch切换、incomplete隐藏部分值、stale/expired、conflict/equivalent及1024/32/256矩阵均通过。

真实PG18全量race为1161 PASS/11既有opt-in skip/0fail，普通Go test/race各552 PASS/4skip；最终B4以真正LOGIN继承rai_api/rai_worker权限组的专项race17个test/subtest terminal PASS，覆盖HTTP删除即时失效/60d与91d前驱/unknown delete-before-create/replay、raw四点故障、artifact后backend termination与reclaim、权限SQLSTATE42501、日志和全部产品/通用reader隔离、M1/M2/M3双向独立。后补场景与全量编译快照分开记录，不把重复执行相加成独立总数。旧B2六点SIGKILL、Daily三点SIGKILL和429/fairness重跑；B4两点macOS kill接到唯一publication。此处不称iPhone kill。empty/23/24→25、重复迁移/schema readiness、既有七点publication fault/竞态/property也在全量实测；所有最终测试cluster清理、残留database0。

单机synthetic全E2E0/1/128/90日/hot-empty/1024refs/32epochs/256ties、App组件及真实PG/Worker/reader timing均有机器记录；不提出生产SLO。1024facts+128selection refs的合法artifact canonical126384B、PG text128794B，均<128KiB；131073B corruption fixture明确constraint拒绝。Go普通/全部专项tags vet、build、mod verify、sqlc1.31.1双次字节复现、边界、diff check通过。OpenAPI未改，B2 frozen contract unchanged。新增evidence只含counts/status/hash/timing/terminal command results，无健康payload或凭证；原方案和历史M1/M2/M3/M5A结果不改写。

**REAL DEVICE ACCEPTANCE: DEFERRED；PHYSIOLOGICAL VALIDATION: NOT VERIFIED；RAI OWN VO2MAX MODEL: NOT IMPLEMENTED。** real Apple Watch availability/source/metadata、真实授权与HealthKit delete/update/background delivery、long offline、WAN、production及physiological validity仍未验证；active仍legacy，promotion-plan=false（product_not_activated、real_device_acceptance_deferred），candidate未激活到产品。仅推荐M5B — Recovery Model Foundation；没有开始M5B、提交、push或部署。

## M5B — Recovery Model Foundation（2026-11-04）

**M5B RESULT: PASS WITH RISKS**。交付 [20 节结果报告](M5B_RESULT.md)、[冻结合同](RECOVERY_FOUNDATION_CONTRACT.md) 与 [真实证据索引](evidence/m5b/README.md)。新增 `internal/recovery` 纯领域、REPEATABLE READ bounded loader、candidate Worker/admin、migration 26 与 sqlc queries；复用原 analysis/job substrate，无新 raw/Readiness 表。原 migration 1–25、M5A raw 代码/合同和原模型方案 SHA-256 不变，原工作区的 M4 成果保留。

沿用已存在模型合同的 **28 天** personal median/MAD baseline（不是条件性建议的 42 天），14 usable days + recent 3/7 gate；SDNN log domain、RHR source-derived statistic、Sleep union/session/wake-day。三项 fresh 才出 full score；缺失不补正常，缺 Sleep 仅可独立 `autonomicOnly` partial，confidence cap≤0.60。字段、threshold/quality factors/timezone 均进版本/配置与 lineage；不推断 morning HRV，不设 stage score/8h population target。Recovery 与 Readiness 分离；M2/M3/M4、Workout、plan 无数值依赖，active/legacy consumers 未切换。

真实工程证据：全量 Go unit/race/vet + integration vet + import boundary + sqlc/diff 通过；全量隔离 PG18/race 通过，最终 M5B 专项又复验。PG18.6 的 migration 25→26/重复迁移、API/Worker roles、snapshot、generation/config/lease/delete/collector fences、三处真实 backend death、raw commit rollback、12 次随机增量==bounded rebuild、DST/timezone/overlap/missingness/float/oracle 已执行。实际改变 M2 load/state 后 Recovery artifact ID/hash/score 不变；完整 product/training DTO 隔离通过。14–365d matrix 当前输入在 42d 之后保持 89 facts，365d query 实际 index scan 93 raw rows；不构成生产容量承诺。所有隔离库归零且 cluster 移除。

M5B 无 Swift/UI 变更；重建 DailyProbe 后真实 Swift→HTTP→隔离 PG18/race 的三处 SIGKILL、anchor/receipt 与 actual 429/Sleep fairness 回归通过。`swift test --package-path ios/RAI --filter Daily` 因本机 CommandLineTools 缺 `Testing` module 未运行成功，明确保留未验证，不能被 probe 通过替代。

**未验证**：REAL DEVICE ACCEPTANCE: DEFERRED；真实 SDNN/RHR/Sleep/source switching/baseline stability、physiological validity、illness/fatigue prediction、population calibration、WAN、production 和本阶段 Simulator UI 验收。权重、nap/session/coverage/freshness/scale/cap 均为 engineering hypotheses。Sleep regularity/midpoint、安全 floor、非默认 config 和 raw 历史 as-known 重构未启用；历史 audit 只读原 immutable run，并标 withdrawal。无已知未解决 P0/P1；不声明生理验证或 FULL PASS。未部署、切入口、迁真实数据、commit/push；停止在 M5B，M6 仅建议。

## M6 — Training Readiness Integration（2026-11-04）

**M6 RESULT: PASS WITH RISKS**。完整 [21节结果报告](M6_RESULT.md)、[冻结合同](TRAINING_READINESS_CONTRACT.md) 和 [证据索引](evidence/m6/README.md)。最高 migration由26→27；原方案、M2/M5B纯领域和migration1–26的冻结hash不变，原工作区未提交成果保留，无commit/push/deploy/真实迁移。

新增纯领域 `internal/readiness`、typed snapshot、REPEATABLE READ有界loader、共享原子publish、candidate Worker/admin。只组合M5B `rai.recovery.physiological/v1` full/fresh score与M2 `rai.training_load.hrr_integral/v1` 正式ledger；不重新算HRV/RHR/Sleep/HR，不用M3/M4/计划/pace/power/distance/E7/E42。D从每次Workout end以36h衰减，B取前42个完整civil days非零complete load median（≥5训练日、span≥14d、latest≤14d、1AU稳定floor），Readiness=P×2^(−D/B)，内部float64且≤P。180d有限窗的omitted tail≤2.610e−10 AU、最大score误差≤1.809e−8，有严格UUID/M2上限证明，不依赖lifetime scan或假定每日训练上限。

Unknown/partial Workout AU不当0，missing/partial/stale Recovery不补100；分别保存Recovery coverage、load knownness与baseline maturity。当前Workout协议没有可验证全stream caught-up信号；普通候选保持sync_incomplete。数值验收仅用显式syntheticOnly、manifest hash/time bounds/identity的版本化fixture，无线上setter，不冒称真实completeness。Reader Freshen只返回projection、不改旧artifact，到Recovery/coverage/day/已知future Workout边界清空current。全部candidate-only；Home/Coach/Training/Plan/Workout/Metrics和legacy active消费者未启用M6。

真实Go数学oracle/property/Golden、PG18.6 empty→27/26→27/repeat、roles、snapshot/fencing、snapshot/artifact/result/publication/clean/run/job/attempt故障回滚、三处pg_terminate_backend、原始删除事务回滚、18步incremental==bounded rebuild与实际M2参数修订已执行；修订改变AU/D/B/M6而M5B artifact身份和值不变。M3及真实external VO₂max独立、全部product DTO隔离通过。M6最终专项PG18/race通过；全量PG18/race最终状态：**PASS（integration 957.597s；最后门禁另作专项复验）**。go test/race/vet、integration vet、boundary、sqlc一致、冻结hash和diff门禁见validation.json，不以文件存在代替运行。

性能为5个history（14/42/90/180/365d）×5个recent density（0/1/5/20/100）真实PG/race矩阵，记录snapshot/decay/baseline/formula/calculate/serialization/publication/reader/Freshen、allocations与JSON bytes；180/365d bounded input相同。仅稀疏历史与单机观测，不是SLO或生产容量。EXPLAIN明确强制index以证明end-time range能力，不冒称自然planner验收。

无Swift/DTO/UI源码修改；DEVELOPER_DIR未设置，xcode-select仍为CommandLineTools。完整 `swift test --package-path ios/RAI` 已尝试，缺 `Testing` module失败；完整Swift package regression未通过，未宣称Simulator UI测试。

**未验证：REAL DEVICE ACCEPTANCE: DEFERRED；PHYSIOLOGICAL VALIDATION: NOT VERIFIED。** real Training Load completeness、Recovery distribution、Readiness validity、training response、injury/illness prediction、population calibration、WAN、production均未验证。36h/窗口/阈值/floor/cap仍为工程假设；180d unknown阻断可用性、未同步strength/cycling、密集规模/逐run ledger round trips属于风险。仅推荐M7 — Model Integration & Product Contract；未启动M7。

## M7 — Model Integration & Product Contract（2026-11-04）

**M7 RESULT: PASS WITH RISKS**。完整 [23 节结果报告](M7_RESULT.md)、[冻结产品合同](ATHLETE_MODEL_PRODUCT_CONTRACT.md) 和 [真实证据](evidence/m7/README.md)。基线 main / `380a6fe96d13d1e57f69ceac50e52e72908af2b8`，原有 M4–M6 未提交成果保留；79 个冻结源文件/原方案/migrations SHA 未变。highest migration 仍 27，无新 summary/assignment 表，无模型公式变化、Worker computation 变化、commit/push/deploy/真实迁移。

新增 `internal/athletemodel` typed product projection、独立 active/candidate snapshot、纯 versioned state/reason/explanation 映射、promotion-plan 和完整未来 cutover/rollback 合同。`REPEATABLE READ READ ONLY` reader 对六个 current slots（M2 workout/state 分离）验证 run/generation/schema/hash/assignment 与批量 bounded lineage refs，仅 Freshen/map，不拟合/算基线/重算模型、不扫描 history。五个 typed value 保留各自 asOf/freshness/status，root 有显式 evaluationAt/revision/calendar；没有整体健康状态或综合分。业务缺失不补0/100；technical failure 独立并保留其他 metric。单个依赖缺失不能被 SQL aggregate 的 NULL 语义掩盖；最新 Workout 的 load 尚未发布时不能替代成旧 analysed Workout。

active service 只接受现行 legacy；未激活新语义返回 not_product_activated。candidate admin preview 与正式 transport 分开，真实 rai_api / rai_worker 角色均不能调用预览、修改 assignment；API 不能读 candidate artifacts。CLI 默认脱敏，显式 synthetic-test-values 才输出测试数值。没有新增 HTTP endpoint、正式导航或 UI，**UI NOT IN SCOPE**。Home/Coach/Training/Plan/Workout/Metrics 实际 consumer 源码/SQL/DTO 已审核；真实 preview 前后 legacy 输出 byte-identical，候选路由404。现有 raw/模型/历史/产品都未切换到 candidate。

真实验证：全量隔离 PG18.6/race `./...` PASS（integration 1018.755s），随后最终 Go all/race/vet、integration vet 与 M7 PG18/race 专项 PASS（见最终 pg-final.txt；全量编译快照与之后的最终边界复验分开记录）。覆盖 mixed missing/freshness、hash corruption、不回退旧值、upstream withdrawal、assignment+dataset revision+pointer 并发稳定快照、真实角色、actual stored DAG 无环、candidate leakage、pending latest Workout。全量含 Workout/Daily/VO₂ raw/M1–M6/training/migration/roles；全部隔离 cluster 最终清理、残留库0。sqlc1.31.1 再生成字节稳定；dependency boundaries/diff check 通过。

完整 Xcode 实际在 `/Users/ta/Downloads/Xcode.app`，仅命令级 DEVELOPER_DIR，未改全局 xcode-select。Xcode26.6/Swift6.3.3/SDK26.5/runtime26.5：**完整 Swift 132 tests /17 suites PASS**，包括 Daily/Workout/SyncCore 和新增11组 shared typed golden、unknown enum/reason/model/shape/null-state。正式 Simulator App XCTest41 PASS/2专用host测试skip；另行专用 B4双group（9+8 cases）与B2 App→HTTP→隔离PG18→Worker lost-ACK E2E全部PASS，未将skip冒充通过。可记录 **M7 full Swift regression now PASS**，不改 M5B/M6 历史结论。

OpenAPI只追加internal DTO schemas，旧paths/schema逐项不变；独立Draft2020-12校验11golden并比对Swift fixtures同字节。固定Redocly2.71.1为0error/33warnings：旧31单列，新增2为CLI-only schema unused。1athlete/5metrics四种availability/freshness场景每组10次真实reader观测、DBReads（3–5，不含BEGIN/COMMIT）、Freshen/mapping/JSON bytes和allocations，以及重复3次Go benchmarks均有证据，不作为SLO。

**REAL DEVICE ACCEPTANCE: DEFERRED；PHYSIOLOGICAL VALIDATION: NOT VERIFIED；PRODUCT ACTIVATION: NOT AUTHORIZED。** real metric availability/source/load completeness、Ability calibration、Recovery/Readiness validity、WAN、production、UI/accessibility/product activation和rollback exercise未验收。五个promotion gate全部false；未来Stage A–F、统一真机/data acceptance、独立calibration与product acceptance只交付计划。M7完成后停止；Unified Real-Device & Calibration Acceptance仅为建议，未自动启动。

## UA0 — Unified Real-Device & Calibration Acceptance Contract（2026-11-05）

**UA0 RESULT: PASS WITH RISKS**，仅 **PLANNING / CONTRACT FREEZE ONLY**。基线 `main` / `9a8839dff0f58fe93ffc7297e50490c0c88279cc`，入口 `git status --short` 与 `git diff --stat` 均空。新增 [总验收合同](UNIFIED_ACCEPTANCE_CONTRACT.md)、[UA1 真机计划](REAL_DEVICE_ACCEPTANCE_PLAN.md)、[UA2 数据可用性研究](DATA_AVAILABILITY_STUDY.md)、[UA3 校准计划](MODEL_CALIBRATION_PLAN.md)、[UA4 产品激活计划](PRODUCT_ACTIVATION_PLAN.md) 和 [18 节结果报告](UA0_RESULT.md)，按工程约定仅追加本节。原方案、Go/Swift/SQL/config、所有 migrations/checksums、历史证据和 `../running` 不变；无新功能、模型、参数变更、commit/push/deploy 或真实数据迁移。

冻结 A Engineering、B Device/Data（UA1+UA2）、C Calibration（UA3）、D Product（UA4）四层独立留证；任何 PASS 不替代其他层。UA1 覆盖实际设备/OS/build矩阵、Workout 全流/anchor/route/ACK、Daily SDNN/RHR/Sleep、External VO₂max registry/provenance、后台 eventual recovery、真实 iPhone 五点 kill、四种 offline、自然 delete/update；未观察到记 NOT OBSERVED，不人工造数据、不主动改健康数据。丢失/删除复活/提前 anchor/来源误分类/恢复失败立即 BLOCKED。M6 真实 Workout all-stream caught-up transport 仍缺，普通 candidate 仍可 sync_incomplete；syntheticCoverage 不可用于真实验收，UA0 不实现 transport 或 checkpoint 工具。

UA2 固定各模型 n/N、cold start、partial/unknown、来源/密度、coverage/abstention 与阻断原因，不为了 availability 调低资格；多数用户长期 unavailable 先 root-cause review。UA3 冻结 athlete 互斥分割和 past→future、prospective known-at、不使用未来/事后 revised 事实；Load 研究参数/重复性/缺HR偏差及后续响应，Ability 比较后续持续表现，Recovery/Readiness 做独立纵向 outcome 验证。Apple Health VO₂仅 external comparator，非 ground truth/唯一标签；自有 VO₂需实验室 gas-analysis 标签，当前 NOT IMPLEMENTED。任何公式/参数/资格语义变更另立 v2，v1 历史保留。

UA4 要求 UA1/UA2/UA3 达门禁后才 shadow，legacy active，candidate 不影响 UI/plan/Coach/notification，只存脱敏 aggregate。真实 product UX/privacy/performance、全部 activation gates、实际 candidate active→previous active rollback 和明确授权独立成立；M7 只有计划、无激活/回滚执行器，不能把 contract closed 当 product accepted。真实数据不进入 git fixture/普通日志；研究 export 另立 consent/minimal/pseudonymized/access-controlled 协议，UA0 不实现。未来命名 `evidence/acceptance/{device,availability,calibration,shadow,product}/`，本轮不造空壳真机 PASS。

本轮实际检查：加载 `scripts/local/lib.sh` 的 `rai_go_env`，`go test -count=1 ./...`、`go test -race -count=1 ./...`、`go vet ./...` 和 `python3 scripts/check-boundaries.py` 均 exit 0；race 为非缓存重跑，在线 import graph **258 packages**，纯领域/独立模型/M7 reader/legacy consumer 边界均通过。本地原始日志 `.local/logs/ua0/{go-test,go-race,go-vet,boundaries}.log`。文档审查 PASS：六份新文档 **47** 个相对链接/锚点、**240** 个 Markdown 表格行、围栏、18 节报告和54项要求追踪均通过；HEAD/暂存区不变，tracked diff仅本文件，untracked仅六份文档，原方案SHA-256保持 `6951b1d938133a964cb06d28c3aa744ad05121f25da770e184041211df53e3de`，`git diff --check`通过。记录 `.local/logs/ua0/document-review.json`；首次检查器误将新文件diff的正常exit 1算失败，修正检查器后通过，首次输出另存，无文档空白错误。文档存在与 Go 通过均不作为真实设备/生理/产品证据。

**未执行/未验证：** 本轮无事务/可执行代码变更，未重跑 PostgreSQL 集成、Swift/Simulator、真机/HealthKit、真实数据研究、生产/WAN/多用户容量、Shadow、rollback exercise 或产品激活。UA2/UA3/UA4 的 cohort、时窗、精度和用途相关数值门槛需执行前预登记并独立签核；当前未填即 gate 关闭，不虚构通过标准。

最终 **MODEL ENGINEERING: CLOSED；PRODUCT CONTRACT: CLOSED；REAL-DEVICE ACCEPTANCE: READY BUT DEFERRED；CALIBRATION: READY BUT NOT STARTED；PRODUCT ACTIVATION: NOT AUTHORIZED**。READY 仅指计划框架就绪，执行前置缺口仍保留；**PHYSIOLOGICAL VALIDATION: NOT VERIFIED**。UA0 完成即停止，不自动开始 UA1，等待用户明确恢复真机条件。

## UA1 — Real Device & Cloud Acceptance（2026-11-05，BLOCKED）

用户明确授权真实 iPhone/只读 HealthKit 与独立云端测试部署；本轮实际执行链路，未授权产品激活、candidate promotion、生理校准或生产迁移。基线 main / 9a8839dff0f58fe93ffc7297e50490c0c88279cc；入口已有 UA0 本节前的16行追加与六份未跟踪文档，原样保留。原方案 SHA-256 6951b1d938133a964cb06d28c3aa744ad05121f25da770e184041211df53e3de 与 HEAD 一致。没有 reset/restore/clean/stash/rebase/commit/push，未修改旧 running、Go/Swift/SQL、模型公式或产品消费者。完整 [25节结果](UA1_RESULT.md)、[工程发现](UA1_ENGINEERING_FINDINGS.md)、[脱敏证据](../evidence/acceptance/device/ua1-20261105-a/)。

真实 iPhone12 Pro Max / iOS27.2（24B119），完整 Xcode26.6（17F113）、Swift6.3.3、iPhoneOS SDK26.5，DEVELOPER_DIR=/Users/ta/Downloads/Xcode.app/Contents/Developer。当前 Debug com.rai.v2.ios / 0.6.0（1）签名build/install/launch通过；初始DDI断连与开发者trust启动拒绝在重新连接/用户信任后解除。development profile设备匹配、codesign strict通过，2026-11-10 07:12:53 UTC到期；SDK Debug NWError warning保留。新session与单Workout命名空间独立，旧Outbox/Anchor未清空。

已知SSH identity的rai-server对应云端新建 /opt/rai-ua1-20261105 / Compose rai-ua1-20261105，沿用release+cloud-test模板、独立PG cluster/DB/role credentials/volume、同镜像API/Worker、独立 https://tpandmr.xyz:19443，PG无发布端口。空public schema0→完整27份migration→schema27/53表；frozen checksum均一致，旧云源码19份也一致，精确binary/source SHA记录。Goose ledger没有逐迁移checksum列，不冒称其存在。首次遗漏runtime grants时readiness正确503、Worker未启动；补执行原roles/runtime-grants脚本后受限API/Worker/DB/schema ready。PG18.6四项durability均on，未触碰旧测试卷、旧443入口或其他项目。

初次外网TLS/iPhone握手超时在用户放行TCP19443后解除；开发机外部TLSv1.2验证/ready200、服务器loopbackTLSv1.3、证书2027-01-30到期，真实手机HTTPS成功。用户明确确认后续Wi-Fi；首个Workout hook在网络类型确认前已上传，不能记作Wi-Fi PASS，收到等待指示即停App/API。没有关闭TLS validation、用USB cloud tunnel或安排cellular测试。实际只读授权完成/用户确认，HealthKit读权限状态本身不可判定；不扩权、不save/delete Health数据。

首个真实Workout ACK：1 Workout / 5464 samples（HR349）/ 5464 associations / 1768 route points / 1 import；duration/distance present，bundle hash一致，client route manifest=server双manifest，1768投影点逐字段mismatch0。重启后同命名空间discovery仍5466 events/12 scoped anchors/0范围外，但重新组包报workout_core_change_requires_review，未重复写server。实际originalWorkout有12个整数编码quantity节点，Normalizer新读使用number而JSON decode优先int，合成enum等号可能把相同canonical数字误判变化；synthetic probe复现bytes相同/enum不同。保留state、失败和原server回执，不清状态绕过；**Workout relaunch repeat P1 BLOCKED**。保存的task anchor1/discovery anchors12已核对，完整Workout增量仍未通过。

HRV/RHR/Sleep真实90-day窗口同步：3128/82/1529条，与本轮device query additions和server counts一致；可见65/71/49日、3/4/3来源，descriptor+algorithm+duration分组571/5/4（非物理设备数）。Sleep五类计数7/336/715/204/267，cross-day16，不导出时间。Sync#2三类均0 additions/deletions且usedCommittedAnchor=true，relaunch原anchor/window恢复；其后自然HRV additions分开留证。RHR query实际投递7 deleted objects、barrier提交并经PG重启保持，未声称删除刚在窗口内发生。最终三类pending0、hasMore=false、error=null。

真实VO2并非无样本：protected blocked archive中24 quantity samples，5 sourceRevision objects/13 device descriptors；known integer metadata SyncVersion/TestType/AlgorithmVersion的archive carrier呈float，当前registry整数NSNumber carrier规则运行时unsupported_metadata。VO2旧anchor未提交、server0、archive保留；结构检查临时原始副本在仓库外删除，公开仅keys/types/count/hash。synthetic double-carrier probe复现拒绝，native边界/范围仍需独立合同审查。**fail-closed safety通过；VO2 transport/projection BLOCKED**，未丢字段、改registry或猜source classification。

三类真实pending在API unavailable、飞行模式+Wi-Fi off、DB unavailable下batch/body/candidate/committed anchor/sequence均保留。已有M5A-RD丢ACK机制以临时TLS验收代理部署：同bytes→真实cloud COMMIT→抑制200响应，手机pending/旧anchor保留；实际SIGKILL、重开、同batch/body/原receipt恢复后提交candidate。K02/K04通过，K01/K03/K05无精确hook而NOT EXECUTED；代理/临时upstream已撤下、标准API恢复。Worker停止时raw commit+ACK，分析仍1471 pending；恢复后真实candidate publication/queue progress，不把ACK等同分析完成。

真实重启前1 Workout/10201 samples/57 daily receipts/1 Workout receipt/1768 points/7 barriers的计数及事实/回执/双manifest指纹，PG重启+API restart后全部相同。DB down readiness503，手机请求未成功ACK，旧pending保留；故障位于handshake/admission，未注入事务中途断库。恢复后原pending各恰有一条receipt，首ACK checkpoint匹配原candidate，之后自然变化/空coverage另计。最终截面1 Workout/10203 samples/63 daily receipts/1 import，jobs1556/pending0；历史dependency_pending失败attempt保留，后续有效publication为SUCCEEDED，不把队列过期清理冒充1556次模型计算。

M7真实candidate inspection complete_response/dbReads5/technicalError均null：Load limited/partial（parameter_missing/load_unknown；显式HR refs0），Ability collecting（no_qualified_performance），Recovery unavailable/source_conflict（baseline/HRV conflict/sleep missing/context incomplete），Readiness stale（load_sync_incomplete/recent_load_unknown/baseline insufficient/recovery_stale），ExternalVO2 unavailable/sync_incomplete。独立前一日Recovery inspect为collecting_baseline/component0，按不同日期语义记录。active仍legacy，无HRmax/RHR猜值、syntheticCoverage、调参或产品读取candidate；**M6 real all-stream caught-up transport仍BLOCKED**。

Observer/background delivery真实注册、三次active callback wake hint durable；自然后台wake NOT OBSERVED，用户暂未执行显式退后台/reopen。用户拔USB点击同步看到“仍待交付”，稍后本地三类pending和自动Workout work均0，无法精确关联断线期间单条回执，USB case仅PARTIAL；UI responsiveness/Instruments/手机CPU内存未测。首Workout client总20.495s、HealthKit14.425s、upload4.129s、server transaction1.893s、ACK总2.779s；JSON4330123B→gzip multipart483243B；单设备、包含子阶段/受控故障等待，不作SLO。

公开材料仅counts/status/hash/timing/reasons；route-specific keys统一hash；未导出健康值、睡眠时间、GPS、Workout/sample/route UUID、设备唯一标识或credentials。保留主API/Worker日志1006 JSON+4条标准库TLS握手warning、临时upstream捕获子集审查，未命中所查健康字段/full UUID/真实secrets；不声称完整保留已移除容器全部日志。只读审计比较器的distance层级、receipt HTTP换行、首ACK与后续discovery差异均记录修正，不改变事实匹配断言。

完成检查：Go1.27.3 go test -race -count=1 ./...、go vet ./...、258包依赖边界最终重跑通过；真实隔离PG18.6 -race -count=1 -tags=integration ./internal/admin ./internal/storage/postgres通过、leftover DB0/cluster removed；Swift132 tests/17 suites、签名build通过。私有编排/日志均Git忽略；synthetic诊断明确分层，不替代真机。原方案/migration/UA0内容与HEAD保护核对、证据JSON/链接/隐私扫描及diff check见review。

**UA1 RESULT: BLOCKED；REAL-DEVICE ENGINEERING: PARTIALLY VERIFIED / NOT ACCEPTED；REAL DATA AVAILABILITY: OBSERVED ON SINGLE DEVICE ONLY；PHYSIOLOGICAL VALIDATION: NOT VERIFIED；PRODUCT ACTIVATION: NOT AUTHORIZED。** 先独立修复Workout数字持久化比较、VO2 typed metadata兼容并复验；M6真实全流transport亦需独立工程任务。未验证Workout完整重复增量、K01/K03/K05、自然后台/完整删除重放、长期离线/功耗/容量、生理和产品。UA1报告后停止，不自动改规则/公式、启动UA2/UA3/UA4、shadow或activation。


## UA1-R engineering remediation (2026-11-05; final partial result)

Baseline: main / 9a8839dff0f58fe93ffc7297e50490c0c88279cc. Existing UA0/UA1 changes retained. Historical evidence/document protection covers 494 files. No commit, push, production deployment, legacy entrance switch or change to the original architecture plan.

R1 RESULT: PASS. Two relaunches on the original iPhone and namespace reused all committed anchors (24/24 discovery calls each), with zero new semantic events, Workout/import/association/route/point deltas. Original identity and receipt preserved. Four naturally arriving HRV facts were counted separately. [Evidence](../evidence/acceptance/device/ua1-r-20261105/r1.json), [numeric contract](WORKOUT_CORE_NUMERIC_CONTRACT.md).

R2 RESULT: PASS WITH RISKS. Native inspection of the original blocked archive found 24 samples and 61 finite integral double-carrier metadata values. All 24 facts committed; all field counts preserved. Pending retained the old archive and no committed anchor; only durable ACK advanced it, followed by an empty anchored continuation. External projection SUCCEEDED and correctly returned source_selection_required/source_unknown. No source-selection or HR parameter input was added. [Evidence](../evidence/acceptance/device/ua1-r-20261105/r2.json), [native contract](VO2_NATIVE_METADATA_COMPATIBILITY.md).

Before R3: Swift 146 tests passed; Simulator 44 passed / 2 explicit external E2E skips; Go test/race/vet and dependency boundaries passed. Isolated PG18 complete integration: 300 top-level PASS / 8 explicit opt-in SKIP, 1092.376 seconds, zero assertion failures, zero leftover databases, cluster removed. The first attempt hit Go's default 10-minute timeout and is not counted as a pass. R1 targeted PG: 33 PASS / 4 opt-in SKIP. R2 targeted PG: 29 PASS / 0 SKIP.

R3 RESULT: BLOCKED. The independent control-plane implementation, strict receipt validation, atomicity/roles/fencing and M6 consumption passed isolated tests. Additive migration 28 and the same API/Worker image run only in the original authorized UA1 cloud test environment. The original iPhone discovered 44 visible running Workouts and completed 4 before one Workout's Activity metadata failed the frozen v3 allowlist: two unreviewed key digests, 5 occurrences (NSNumber q 2 / String 3). Captures retained: 56 streams / 63 pages / 32,709 additions; completeness generation=0, committed anchors=0, final manifest/receipt rows=0. No fields dropped, state reset, narrower scope or fake completeness. [Real evidence](../evidence/acceptance/device/ua1-r-20261105/r3.json), [contract](WORKOUT_SYNC_COMPLETENESS_CONTRACT.md).

Final regression PASS: full isolated PG18.6/race, serial packages, integration 305 top-level PASS / 8 opt-in SKIP, 959.027 seconds; all 17 tested packages PASS, command exit 0, leftover DBs 0, cluster removed. Earlier full attempts failed on outdated schema/table assertions, synthetic bulk seed timeout and CPU contention; fixes changed test setup/expectations only, not production timeouts/limits. Go all/race/vet, integration+ua1rsim vet, dependency boundaries, mod verify, scoped gofmt, sqlc regeneration stability, original migration 1–27 checksums, OpenAPI existing paths and M7 goldens passed. Swift 150 tests / 21 suites PASS; final serial Simulator 47 PASS / 3 explicit runner SKIP. Separate Simulator → real Go HTTP → PG18 → M6 E2E PASS: lost ACK, immutable retry, 2 generations, 12 committed anchors / 0 bootstrap on repeat, M6 synthetic=false and unknown load preserved. [Validation](../evidence/acceptance/device/ua1-r-20261105/validation.json).

UA1-R / UA1 RE-ACCEPTANCE: BLOCKED, R3 remains open. Real-device engineering PARTIALLY VERIFIED / NOT ACCEPTED. Server has 5 distinct Workouts / 5 imports; original Workout still has exactly 1 import and its original identity/receipt. Real M6 correctly retains load_sync_incomplete/recent_load_unknown/baseline insufficient/recovery_stale; no synthetic assignment, HR references=0. M6 formulas/qualification unchanged; active=legacy, candidate-only. Physiological validation NOT VERIFIED; product activation NOT AUTHORIZED. No UA2/UA3/UA4 work started. [Full 17-section result](UA1_R_RESULT.md). Further real manifest/ACK/incremental/M6 validation requires reviewing the newly observed unsupported Activity metadata; it is not presumed to pass. This round stops after evidence preservation.

Privacy incidents: one tool output included a route UUID in a coverage map key; the mapper was corrected to hash that key. A later lock-state check also printed a device identifier because the whole result object was output instead of a boolean whitelist. Public artifacts exclude both identifiers, but this run cannot claim zero identifier disclosure. No health values, route coordinates, source bundles, sleep times or credentials were exported in public remediation evidence.

## UA1-R3A — 真实 Activity metadata 合同与 completeness 续跑

两类真实 WorkoutActivity metadata（仅以 SHA-256 表示）经本机 iPhoneOS 26.5 SDK/header 与模拟器二进制字符串全量比对确认属 `undocumented_real_metadata`，决策为 **supported opaque-preserved**：activity scope 专用、精确 native carrier（String / `NSNumber:q`→SInt64）、有界（每对象 64 key、文本 64 字节）、不解释、不参与模型逻辑。registry 1 → 2，wire 新增可选 `metadataRegistryVersion`（缺省=1；旧服务器严格解码拒收 ⇒ 客户端保留 pending、不推进 anchor）。[合同](../contracts/workout-metadata-registry-v2.md) · [共享 vectors](../contracts/fixtures/workout-metadata-registry-v2.json)。

回归：Go 全量/race/vet/build/sqlc/边界 PASS；隔离 PG18.6 `-race -tags=integration` `ok rai/server/integration 1095.623s`、无残留库、集群已移除；Swift 155 tests / 22 suites；模拟器 App XCTest 56 passed / 0 failed / 3 skipped；`git diff --check` clean。隔离云测试环境已按用户当次授权重新部署（schema 仍 28，registry v2 无迁移）。

真机：preserved R3 state 原样续跑，原 metadata blocker 消失（该 Workout 完成，blocked metadata Workout 归零；pages 63→74、streams 56→67、additions 32,709→32,712）。随后出现新阻塞 **`source_policy_pending`**：44 条可见 running Workout 中 31 条 apple_watch、13 条 third_party，而冻结合同要求每条可见 inventory Workout 都有 v3 receipt。R3 保持 BLOCKED，无 manifest、无 anchor 推进、无第二 generation。R1 真机复跑两次 bundle hash 一致、`newSemanticEvents=0`、24/24 committed anchor；R2 无新增阻塞件。M6 未在未验证 completeness 上重跑。[17/19 节结果](UA1_R3A_RESULT.md) · [证据](../evidence/acceptance/device/ua1-r3a-20261105/r3a.json) · [隐私门禁](../scripts/check-ua1-r3a-privacy.py)（公开产物 5 文件 0 命中）。

UA1-R3A / UA1 RE-ACCEPTANCE: BLOCKED。下一步需要用户决定 completeness inventory 的 scope（source-eligible 或把第三方 Workout 明确声明为 not-applicable），这是 reviewed contract 变更，未在本轮自行放宽。未进入 UA2/UA3/UA4、Shadow 或激活；active=legacy 未变。

## UA1-R3B — Source Policy & Eligible Inventory Contract

冻结合同变更：新增 `workout-sync-completeness/v2` 与 source policy 身份 `rai.workout.source_policy.apple_watch_only/v1`（摘要 `63713b4f…e0654`，classification version 1），把 inventory 语义从「每条可见 Workout 都需 receipt」改为「每条可见 Workout 都必须被确定解释为 eligible 或 explicitly excluded」。v1 未原地修改，仍被接受。[结果](UA1_R3B_RESULT.md) · [v2 合同](../contracts/workout-completeness-v2.md) · [policy 合同](../contracts/workout-source-policy-v1.md)。

实现：Go 侧 `source_policy.go`（outcome 表、evidence 规范化与摘要、identity/inventory 哈希）、`workout_completeness_v2.go`（严格解码 + 校验，含服务器重跑分类器）、`storage/postgres/workout_completeness_v2.go`（同一事务内 page 链、inventory 不变量、visible/eligible 集合比对、逐条 exclusion evidence 重分类、immutable receipt）、加法迁移 `00029`（schema 28 → 29，六列 + v1/v2 行约束，编号由 `LatestVersion()` 推导）、HTTP 按 `contract` 分派 + handshake 广告 `workoutSyncCompletenessV2Version`/`workoutSourcePolicyVersion`。Swift 侧 `WorkoutSourcePolicy.swift`、`WorkoutCompleteness` 的 v2 staging/双 contract ack/`completenessClassify`、`SyncCoordinator` 的分类与 excluded 短路（不读子流、不上传、不建分析任务）。

验证：Go gofmt/vet(±integration)/build/unit/sqlc 稳定、依赖边界 PASS、OpenAPI lint PASS（0 error/36 warning）、隔离 PG18 `-race -tags=integration` `ok rai/server/integration 1021.528s`（`-timeout 40m`，默认 10m 在该 host 不足属既有环境事实）。Swift 163 tests / 23 suites PASS；模拟器 App XCTest 60 例（57 通过 / 0 失败 / 3 显式外部环境跳过），新增 v2 排除项测试。Simulator → 真实 Go HTTP → 真实 PG18 v2 E2E PASS：`workout_sync_completeness` 落 2 行（generation 1、2），均 `protocol_version = 2`、policy 身份一致、inventory 摘要齐备、exclusions 空。云测试环境重新部署：schema 28 → 29、v2 列 6、`apiInsert=true/apiUpdate=false`、`activeStack=legacy`、handshake 广告 v2。

真机：**未执行**。设备在 `devicectl` 中 `unavailable`（`device info details` 可读，`install`/`lockState` 返回 CoreDevice error 1011），已刷新 device 标识并完成 `generic/platform=iOS` 构建，但无法安装/启动/取证；用户当次决定「先跳过真机，仅完成其余交付」。因此真实 44/31/13 inventory 未复核、eligible 全量补齐未验证、真实 lost-ACK 与第二增量未执行、M6 未重评。

隐私门禁 PASS（3 文件 0 命中）：公开证据只含 count/hash/分类分布/reason。

UA1-R3B / UA1 RE-ACCEPTANCE: BLOCKED（真机侧未验证）。R1 合同未变、R2 未回归；未提交、未 push、未部署生产、未切换 legacy、未进入 UA2，`active=legacy` 未变。

## 本机开发提速与独立真机环境（2026-11-06）

用户要求开发/回归/性能测试全部在本机 Mac 执行，并明确授权建立新的本机真机验收环境。新增 `scripts/local/test.sh` 的 quick/gate/unit/integration/full/swift/simulator/e2e/perf 分层入口，复用 Go/Xcode 构建缓存，数据库仍为同步落盘、checksums 开启的一次性隔离 PG18。旧 remote 两入口在任何连接前直接拒绝；AGENTS.md 已记录本机执行及原完成门禁要求。原 N100/云端数据未读取、搬迁或清理；历史报告、原方案、协议和 migrations 保留。[结果](LOCAL_DEVELOPMENT_RESULT_2026-11-06.md) · [聚合证据](evidence/local-development-2026-11-06.json)。

修复客户端 `associateDiscoveredFacts` 每 sample 扫描全部 discovery events 的 `K×E` 成本，改为一次索引后按原 keys 处理，保留 generation、revision/delete、幂等、父冲突和最终原子状态提交。新增重启/重复 key/冲突回滚回归。相同本机 Swift Debug 合成 10000 events /1000 keys（含状态编码/落盘）由 9328.407ms 降至124.222ms（约75倍）；不等于真机整体75倍。series/route逐点synchronize、fresh series、完整事实与ACK/anchor要求未放宽。

本机 quick 最后7s、gate19s；Swift Testing164 tests/23 suites PASS，另XCTest6项中3通过3显式跳过（含本轮opt-in性能测量）；模拟器60项57通过0失败3外部fixture跳过。真实loopback HTTP→隔离PG18的1/10/100/500条轻量合成bundle核对通过，500条提交20.955s、HTTP中位34.870ms/P95 62.931ms；每条60 scalar/3series/3route，使用显式有限benchmark admission预算，Worker未计时，不冒充密集HealthKit或容量验收，单manifest256 inventory上限未变。

修复既有Simulator HTTP/PG丢ACK钩子在v2 COMMIT后误用v1严格decoder持续返回500的问题；生产协议/事务未改。新增诊断留存、按contract分派和v2断言。修复后本机PG/race专项PASS（package46.125s/入口64s）：2代v2 manifest、12streams、1import、lostACK幂等、第二轮bootstrap0、M6 synthetic=false/unknownLoads1，残留库0/cluster removed；另两项外部fixture未冒充通过。

真机USB实测iPhone12ProMax wired/paired/developer-mode enabled，签名Debug更新安装成功、未卸载。`scripts/local/device.py`建立独立Mac PG18.6/schema29/API/Worker，握手v2；iPhone→Mac认证probe81.933ms，probe前后原sessions manifest hash一致。Debug独立namespace不激活/重绑定普通session，不启动旧产品轮询；数据库仅loopback，API仅私有LAN接口、HTTP仅Debug精确host，Release TLS规则未改，开发owner角色不冒充生产权限验收。首条真实eligible Workout ACK18.421s（采集组包12.050s/上传确认5.676s）；重启后12.652s精确复用原bundle且使用committed Workout anchor，server仍1Workout/1import/5464samples/1768route points；Mac后端stop→prepare后身份与1/1计数保留。常规测试仍仅合成数据，真实验收事实仅私有App/本机DB，公开仅count/timing/status/布尔。

完成门禁：Go race单测、vet（含integration/ua1rsim）、build、sqlc生成一致、依赖边界、shell/Python语法和控制器安全4例通过。**全量PG/race首跑FAIL**：integration1300.994s，仅既有`TestFeatureLoaderLimitsFailWithoutTruncatingOrPublishing`末尾job状态断言失败，其余包通过、清理0残留；该项独立7.33s通过，首跑未记录最终errorCode，具体负载/超时原因未证实。已将synthetic数量边界fixture改成先通过Validate的合法metadata-only observation，仍插入并断言2002行、验证2000上限和禁止发布，新增job必须failed/analysis_input_limit断言；生产实现/上限/超时不变。修复后真实隔离PG18/race连续5次PASS（package14.743s）、清理通过。**整套未再次执行，不把首次失败改写为全绿。**

本轮LOCAL DEVELOPMENT为PASS WITH RISKS；真实全量v2 completeness、真实manifest lostACK、真实第二增量与真实M6重评未执行（新库manifest0），不改变UA1-R3B历史BLOCKED结论。不进入UA2/UA3/UA4，PHYSIOLOGICAL VALIDATION NOT VERIFIED、PRODUCT ACTIVATION NOT AUTHORIZED；无模型资格/公式、生产部署、旧入口切换、commit/push或真实数据迁移。

## Workout 同步提速与 Apple Watch 来源过滤实施方案（2026-11-06，仅文档）

按用户要求新增 [完整实施方案与验收条件](WORKOUT_SYNC_SPEED_IMPLEMENTATION_PLAN.md)。方案包含 P0–P6 七阶段、32 项功能/故障验收（F01–F32）、12 项性能门槛（P01–P12）、默认限流下的统计方法、文件范围、兼容回退与交付签核。普通 20–60 分钟跑步的待验收目标为完整用户路径 P50≤5s/P95≤8s；不是已测成绩。RQRun 抓包起始时间不等于完整采集耗时，公平直接对标另列 NOT VERIFIED。

来源过滤沿用冻结的 Apple Watch 原生 running 策略，提前到列表和子数据采集之前；保留根可见清单、exclusion evidence、明确删除及未知来源语义。提速先处理重复读取、逐样本文件/逐点刷盘和编码，再合并重复语义校验及数据库集合检查；单条持久 ACK 与异步分析、全量完整性证明分开，未完成证明不冒充模型完整。旧 pending、精确字节身份、删除优先、同事务回执、原方案与历史证据不变。

本轮仅新增方案并追加本条记录，未实施 P0–P6。文档本地链接、代码围栏配对、F/P 编号唯一且完整检查 PASS；414 个既有 Go/Swift/SQL 文件相对本轮入口 hash 核对无变化，原方案 SHA-256 仍为 `6951b1d938133a964cb06d28c3aa744ad05121f25da770e184041211df53e3de`。本机 `scripts/local/test.sh gate` PASS（4s，Go race 测试使用缓存，vet、258 包依赖边界、控制器安全 4 例通过）；`git diff --check` PASS。此门禁只证明当前工程检查通过，不证明计划中的优化已实现。

NOT RUN / NOT VERIFIED：新捕获格式、新 SQL/事务、App 改造、性能目标、真实隔离 PG 新方案集成、真机新操作与 RQRun 公平对照均未执行。无生产数据库访问、部署、真实数据迁移、commit/push 或产品模型激活。

## Workout 同步提速与 Apple Watch 来源过滤 — 实施结果（2026-11-06）

按 [实施方案](WORKOUT_SYNC_SPEED_IMPLEMENTATION_PLAN.md) 完成真机之外的部分，并在已连接的 iPhone 12 Pro Max（iOS 27.2，Release 构建，WiFi → 本机 Mac 后端）上执行真机验收；逐项结果、真实数字与未验证项见 [实施结果](WORKOUT_SYNC_SPEED_RESULT.md)，原始证据在 `docs/evidence/workout-sync-speed/`（client 编码前后与负载对照、server S60/S240 ack 各 n=30 全量迭代、Phase4 矩阵 timing 子集、7 个 `EXPLAIN (ANALYZE, BUFFERS, WAL)` 与 phase 分解）。工作树基于 `533fa82` 的既有未提交改动之上，未 reset/clean/覆盖；无 commit/push、无部署、无真实数据迁移、未切换旧入口。

客户端：来源分类提前到 `recentWorkouts`（有界分页，`scan ≤ 400`、4 页、cursor 前进）、`discoverChanges`（根对象与最小证据之后、任何 quantity/series/route 子查询之前）与 `performSync` 入口；`excluded`/`unclassified` 不建任务、不读子数据、不上传，分类记录只含身份 hash 与 descriptor digest、不含 UUID 明文、不写 `state.json`。批量捕获改为 segment（512 条 / 256 KiB / 250ms 先到者，封存后不可变，无 seal 尾部永不可引用），series/route 由逐点 `synchronize()` 改为有界批刷盘且文件字节格式不变；Workout 根记录保留单文件归档，因为 `stageDiscovery` 只对 `archiveFile != nil` 的根事件登记 `workoutArchives`。采集复用新增 `exhaustedDiscoveryFacts`（要求 anchor 已提交、无 staged/error/hasMore、每条事实都有可读归档），组包可直接消费 discovery 归档并跳过二次全量 HK 读取；无变化重复同步由 `isAlreadyDelivered` 判定后不发 POST、不读 HealthKit。期间修掉两个真实缺陷：`setBundle` 使用捕获前的 generation 导致 `deliveredGeneration` 永远落后一代；根记录误入 segment 导致可见清单为空、scoped 发现停摆（被三个模拟器用例同时抓到）。分析刷新与 durable ACK 解耦，单条 ACK 后即可显示已同步。

编码器把递归 `Data`/`String`/`JSONEncoder` 换成单缓冲字节追加与手写转义，数字格式、键排序、省略规则未变；字节一致性由把旧编码器原样复制为 reference 的 `WireEncodingTests` 证明（控制字符、U+2028/2029、24 个 double 边界、Int64 极值等逐字节 + hash），五档 `json_bytes` 与基线完全相同。S60 编码由 3526.48ms 降至 384.77ms（−89.1%，P11 门槛 ≤1763ms 通过）；同一代码在并发负载下 S60 曾达 884.22ms，两值均已归档并标注负载条件。

服务端（server-import 交付）：语义校验收口到 Store 入口一次、HTTP 只留严格解码；身份/删除/series/route 检查与写入改集合式（≤1000 键/批、多行参数、无 `ON CONFLICT DO NOTHING`），判定顺序与冲突码逐条保持；补齐 `semantic_validation`/`dataset_lock`/`commit` 等 11 个 phase。未新增迁移或索引（最高仍 `00029`），durability 全开，未扩池。

测试：`scripts/local/test.sh swift` 197 tests / 28 suites PASS；`simulator` 71 项 68 通过 0 失败 3 显式外部跳过；`e2e` PASS（`contract=v2 manifests=2 streams=12 imports=1 lost_ack=true`，残留库 0、集群已删）；`gate` PASS；`full` PASS（1312s，含 `go test -race -count=1 ./...`、`go vet -tags=integration`、sqlc 生成与 dbgen diff、全部 PG 集成测试 `ok rai/server/integration 1068.843s`）；`perf` PASS（benchmark 预算下 500 条轻量 bundle 19.18s，不能当 P08/P09）。既有断言零放宽，唯一 fixture 调整是给合成 HK 记录补上其本意描述的 Apple Watch 描述符（来源门提前后被正确判为 `source_unknown`）。

性能结论：P11 PASS（S60 编码 3526.48ms → 384.77ms）。**P06 FAIL**——S60 `T_server_ack` P95 **5646.0ms** / P50 4901.3ms 对目标 ≤1000ms（n=30，0 失败，真实 handler→PG；P50 构成：严格 JSON 解码 1649.6ms + `sample_writes` 1068.1ms + `association_writes` 1038.8ms + `routes` 171.1ms + 语义校验 159.6ms + `receipt` 122.2ms + `identity_checks` 116.4ms + `dirty_job` 133.4ms + ACK 写出 0.11ms）。**P07 FAIL**——S240 P95 **31036.3ms** / P50 21080.2ms 对目标 ≤4000ms（同口径 n=30，0 失败）。S240 此前曾因集合式身份查询在热连接上拿到 generic plan（同一批 1000 键 custom 31ms vs generic 3.4s）而撞 2s `statement_timeout`，只抬超时只会把错误码换成整事务超时；已由 server-import 在导入事务内加事务局部 `plan_cache_mode='force_custom_plan'` 与有界 `statement_timeout='30s'` 修复，20k 单包回归用例 PASS，`TestPhase4PerformanceMatrix`/`TestPhase4FreshGzipDurationMatrix` 全档（含 240）转 PASS。

NOT RUN / NOT VERIFIED：iPhone 真机口径的 P01–P03/P10/P12、P08/P09 批量端到端、模型与完整性联动（F30）、F05 的设备端真实历史、真实磁盘满/FileProtection/掉电、`perf` 入口、新旧实现同输入对拍（旧路径已删除，仅按合同语义断言）、部署与 RQRun 公平对标。本机 Debug/macOS 数字不替代真机，服务端时延未达标不因客户端改善而抵消。


### 真机验收结果（iPhone 12 Pro Max，iOS 27.2，Release）

- 真实只读采集，同一条记录每次重置本机一次性数据库后做真实首导入：58 分钟记录（8.57MB canonical JSON）n=20 → `T_user_ack` P50 **19.14s** / P95 19.65s、峰值 RSS 291.6MiB；32 分钟记录 n=20 → P50 9.87s / P95 12.18s、RSS 202.4MiB；库内唯一 108 分钟记录 n=20 → P50 1.97s（series 型稀疏样本，1.67MB，不构成 S120 密度样本）。
- 同机基线对照（内部回退开关还原逐条刷盘与旧编码器，上传字节完全相同 8,565,743B）：P50 25.10s → 19.14s，**−23.7%**。→ **P01 FAIL**（对 ≤5s/≤8s，且未达 −50%），瓶颈是逐样本 `NSKeyedArchiver` 8.8s 与服务端导入 ≈5s。
- 无变化重复同步 + 重启恢复：连续 9 次重启进程后同步，P50 **420ms**，0 次新 POST/import/job/change event → **P04 PASS**。
- 主线程最大停顿 1.8ms（n=20）、无 >250ms 停顿 → **P10 PASS**；RSS 超限 → **P12 S60 FAIL**。
- 来源过滤真机清单：4 页扫描 100 条，51 合格 / 49 排除 / 0 未分类，191ms。
- 完整性 v2 真机尝试未完成：运行 15 分钟后进程被系统终止，已导入 10 条记录，`workout_sync_completeness` 仍为 0 行（未伪造 receipt）→ NOT COMPLETED。
- 未执行：P02（无 S120 密度记录）、P03（无 240 分钟记录）、P08/P09 批量端到端、API 进程 RSS、部署、RQRun 公平对标。

## Workout 同步提速第二轮计划与 Codex 执行提示词（2026-11-06，仅文档）

按用户要求新增 [第二轮完整计划与验收条件](WORKOUT_SYNC_SPEED_ROUND2_PLAN.md) 和 [可直接交给 Codex 的执行提示词](WORKOUT_SYNC_SPEED_ROUND2_CODEX_PROMPT.md)。入口为 `main / 0303236`，工作区干净。第一轮计划、结果和原架构方案保留原文，不把本轮设计当已实现。

第二轮明确四项要求：正常受支持样本的归档/还原调用为零（规范化持久捕获，保留必要anchor/根对象及旧格式兼容）；HealthKit 独立类型查询按1/2/4有界并发对照，单写者、字节背压、取消和capture/ACK边界；新版客户端WorkoutBundle POST强制gzip、失败/超限不回退明文、旧pending身份不变、服务器保留旧plain兼容；现有集合SQL之上实测VALUES/数组/COPY-staging候选并保持受限角色和同事务保证。计划包含R0–R6七阶段、32项R2-F功能/故障验收、14项R2-P性能/资源门槛、回退与证据交付要求。

测速先分离普通构建与race：当前`integration`入口默认`-race`，第一轮对应handler内数字保留为历史诊断，第二轮须新增普通构建真实TCP HTTP/认证/准入/PG口径；不得降低race正确性门禁、扩大默认预算或删除慢/失败样本。S60H仅为合成密集结构，不复制真实值。真实HealthKit并发收益、真实端到端和RQRun公平对标各自单列未验证项。

本轮仅写两份MD并追加本节。文档链接、围栏、示例shell语法、编号完整/唯一检查PASS；入口1193个既有tracked文件已核对，除本文件追加外内容保持不变。本机`scripts/local/test.sh gate` PASS（11s，Go race单测使用缓存，vet、258包依赖边界、控制器安全4例通过）；`git diff --check` PASS。第二轮R0–R6代码、并发/格式/gzip-only/SQL新方案、真实隔离PG新方案集成及新性能/真机验收均为NOT STARTED或NOT RUN；不据门禁宣称本轮提速达成。无新真实健康数据读取、生产数据库访问、部署、状态清理、真实数据迁移或commit/push。

## Workout 同步提速第二轮 — 实施结果（2026-11-06）

按 [第二轮计划](WORKOUT_SYNC_SPEED_ROUND2_PLAN.md) 与 [Codex 执行提示词](WORKOUT_SYNC_SPEED_ROUND2_CODEX_PROMPT.md) 完成实现、合成实测与真机补测；逐项结果、分段数字与签核矩阵见 [第二轮实施结果](WORKOUT_SYNC_SPEED_ROUND2_RESULT.md)，原始证据与复现命令见 `docs/evidence/workout-sync-speed-round2/`（README 索引、服务端 10 份日志与 4 份说明、iOS 捕获 4 份、传输 1 份、真机聚合 1 份、lead 的 `full` 门禁日志）。工作树基于 `main / c864038`（round2 实现提交）之上，未 reset/clean/覆盖历史；无 commit/push、无部署、无真实数据迁移、未切换旧入口、未读取生产数据库。

捕获侧（R1–R3）：新增不可变 `CaptureManifest`（`coveredEventIDs` + `deletionEventIDs`，cursor 仅作证据）与每事件 ordinal，ACK 只确认本包真实覆盖的身份，`isAlreadyDelivered` 增加第二重守卫。这修掉两个**真实缺陷**（pre-fix 用当前测试 + `manifest: nil` 复证）：捕获中迟到的 sample 被 ACK 确认后水位照常前进、第二遍 `uploads == 0` 造成样本永久丢失；捕获中的子删除被 ACK 确认后服务端样本复活。父删除本已由 `claimBundle` 的 `pendingDelete != true` 守卫挡住，仅纳入回归。新增 `NormalizedWorkoutCapture` 与 segment format v2（`encoding:"normalized"`，format 1..2 均可读），正常路径 `sample_archive_calls=0`/`sample_unarchive_calls=0`（timings 与 `SampleArchiveMeter` 双口径，旧路径对照 14,000 次），bundle 与 `Normalizer.integrityEvent` 逐字节相等。新增 `CaptureConcurrency`（2 流/2 MiB 字节背压的 `CaptureGate` + 单写者 `CaptureWriter`），`performSync` 改为 `withThrowingTaskGroup` 并发并按固定顺序合并，实测 peak active `[1:1, 2:2, 4:4]`、peak 队列字节 1,572/1,572/2,370 B、墙钟 87.69/58.04/63.57 ms，默认取 2。**后续真机补测**：真实 HealthKit 并发收益与真机 RSS 已执行并记录于本节末（2 流无收益、4 流更差；S60H RSS 超限）；route 流按字节预留仍未做。

传输侧（R4）：删除"压缩后 > 8 MiB 回退明文"的分支，`WorkoutBundleDelivery.deliver` 现在只有"复用已封存 gzip（校验 hash 与 gzip file part）"或"从本地权威原包重新派生 gzip"两条路径，原 multipart 永不上传；新增 `BundleGzipCapability`/`HandshakeCapabilities`，服务端握手未声明 `bundleGzipPart` 时以 `bundle_gzip_unsupported`(422) 明确失败并保留任务，而不是发明端点或发明明文。计数器 `bundle_plain_fallback`/`bundle_healthkit_reread` 每次尝试恒 0，`bundle_gzip_post_success` 覆盖全部成功 POST。

服务端（R0/R5）：新增 `scripts/local/test.sh syncperf` 普通构建入口（逐 token 过滤 GOFLAGS 的 `-race`/`-N`/`-l`、用 `go version -m` 断言、真实 loopback TCP → 认证/准入 → 真实 handler → 真实隔离 PG18、记录独占性与写策略），并把 `bundle_decode.go` 的两遍反射扫描改为单遍 + schema 缓存，普通构建解码 103.8/263.1/409.0 ms（第一轮的 1,649.6 ms 是 race 放大 ~14×）。写入新增 `bundle_writes.go` 的固定形状 unnest 数组候选，与 VALUES、COPY→staging 同入口 A/B：三候选 projection digest 完全一致，arrays 写合计 1,519.3 ms（values 1,671.9、copy 1,733.2）故采用 arrays；COPY 在受限 API 角色下 `permission denied to create temporary tables`（TEMP 已 REVOKE），**未采用**且如实记录所需权限。dataset 行锁、fencing、父子 tombstone、身份/关联冲突、append-only enrichment、原始事实+日志+dirty/job+回执同事务均未改动，未加 `ON CONFLICT DO NOTHING`、未扩池/槽/预算、未关 durability、未抬超时。

性能实测（普通构建真实 TCP→PG，n=30，0 失败，nearest-rank P95）：S60 P50 2,796.7 / P95 **4,758.8 ms**；S60H 4,277.9 / **5,753.4 ms**；S240 12,035.0 / **19,722.3 ms**。→ **R2-P08 FAIL**（4.8×）、**R2-P09 FAIL**（4.9×）、**R2-P10a PASS**（写阶段不回退）、**R2-P10b FAIL**（−9.1%）。长尾已归因并有证据：每行 WAL ≈ 922 B、S240 单次导入 WAL 峰值 379 MB/53,406 FPI/`ckpt_sync` 6,125 ms、`identity_checks` 长尾是 autoanalyze 统计陈旧（`TestIdentityCheckScaling` 证明计划本身稳定）；裸 PG 天花板探针（同 schema/索引/durability、绕开 HTTP）COPY 直写 585 ms(S60)/2,442 ms(S240)，加其余分段后 S60 ≈1.16 s、S240 ≈4.4 s，**故 S60/S240 门槛在当前 schema 与耐久设置下属物理下限，不是实现缺陷**。客户端 **R2-P01 FAIL**：规范化捕获段 1,530.05 → 1,115.62 ms（**−27.09%**），组件实测显示即使归档成本全消该段上限也只有 ~35%，与第一轮真机"archiver 占 12.78 s 中 8.8 s"的成本结构不同，两个口径已并列写入证据，未改口径去凑 70%。

测试与门禁：`scripts/local/test.sh full` **PASS**（1338 s，`all modules verified`、`go test -race -count=1 ./...` 全绿、`go vet -tags=integration`、build、sqlc/dbgen 无 diff、258 包依赖边界 9 项、`ok rai/server/integration 1099.261s`、`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`）；`gate` PASS；Swift 包测试 220 tests / 29 suites PASS；模拟器 lane 85 passed / 0 failed / 3 skipped；`e2e` PASS（`TestWorkoutCompletenessSimulatorHTTPPostgres` 45.79 s）；`swiftclient` 真实 Swift→HTTP→PG 18/18 场景 PASS；R5 新增 F21–F27 与既有 bundle 套件 `-race` 29 pass/2 skip/0 fail。期间修掉一个本机脚本缺陷：`integration` profile 不导出 `DEVELOPER_DIR`，`swiftclient` 标签在 CommandLineTools 下编译期即死（`no such module 'Testing'`）表现为 18 个子用例全 FAIL，已在参数含 `swiftclient` 时调用 `rai_xcode_env`，修后同入口 18/18 PASS。

签核：NORMALIZED CAPTURE PASS、**BOUNDED CONCURRENCY PARTIAL**（合成一致性 PASS；真机 2 流比 1 流慢 3.6%、4 流慢 14.5%，无收益）、GZIP ONLY CLIENT PASS、SET BASED IMPORT PASS、**FUNCTIONAL SAFETY PASS（有真机覆盖限制）**（F03 重启级用例 PASS、F04 服务端 HTTP/PG 用例 PASS、F29 无心率用例 PASS；F01–F32 无 FAIL，但 S120/S240/室内/无功率真机覆盖缺失）、**PERFORMANCE FAIL**、**REAL HEALTHKIT 已执行但未通过时延**、RQRUN/DEPLOYMENT NOT VERIFIED。

真机验收（2026-11-06，iPhone 12 Pro Max，iOS 27.2，Release+仅验收仪器，USB→本机 API/Worker/PG18，真实只读 Workout，每组前 reset 新 dataset，n=20 nearest-rank）：R2-P03 S60（32 分钟 W32-A，4,330,151 B）**P50 8,182.27 / P95 11,260.71 ms → FAIL**；R2-P04 S60H（58 分钟 W58-A，8,565,743 B）**P50 18,274.23 / P95 24,637.76 ms → FAIL**；R2-P05 S120/S240 **NOT RUN**（库无 120/240 分钟记录）；R2-P06 无变化重复 n=9 **PASS**（wallMs P50 368.3 / P95 426.7 ms，0 bundle POST/import/job）；R2-P07 已备原包恢复 **PASS**（首次 503 拒绝，恢复 2,907.97 ms，wire 483,260 B / bundle 4,330,151 B=11.2% gzip，前后 SHA256 一致，DB 仅 1 import/5,464 samples/1,768 routes，未重读 HK）；R2-P11 gzip P50 S60 39.81 ms / S60H 94.5 ms **PASS**；R2-P12 **S60 max 206.5 MiB PASS、S60H max 302.34 MiB FAIL**、S240 NOT RUN；R2-P13 **PARTIAL**（单槽 API peak 104.8 MiB≤512、主线程 max stall 1.1–6.6/4 ms、>250 ms 0 次；双槽并发因单设备槽限制未形成，1×200+1×429）；R2-P14 10×S60 前 10 次合计 **80,219.01 ms >60 s → FAIL**，100× NOT RUN。并发 1/2/4：P50 17,638.31 / 18,274.23 / 20,194.13 ms，2 流无收益、4 流更差。上述为同机同 LAN 顺序测量，后段热漂移明显，但 P03/P04 中位数超门槛幅度无法由热漂移解释。

NOT RUN / NOT VERIFIED：R2-P05 S120/S240（库无 120/240 分钟记录）、R2-P14 100×S60（仅 51 条 eligible，无法凑 100 条不重复首导入）、R2-P13 真正双上传槽并发（单设备只占一槽，重放 1×200+1×429）、R2-P12 S240 RSS；R2-F03/F04 服务端/F29 已补测 PASS，但室内跑/无功率的真实记录覆盖仍缺失。写阶段 Go CPU profile、导入后统计刷新（ANALYZE/autovacuum 阈值）、50k 用例在 `-race` 下未跑；S240 历史 6 次 503 的确切错误码 NOT VERIFIED（现二进制实测为 `503 {"code":"database_statement_timeout_or_cancel"}` @30,049 ms 且整包回滚）。真机只读记录未导出健康值/GPS/UUID/设备标识；本机 Debug/macOS 数字不替代真机；服务端时延未达标不因客户端改善而抵消。


## 运动能力模型原设计实现差距核查（2026-11-06）

按用户要求，将所附《RAI V2 — 运动能力模型架构与实施设计》与当前 `main` / `18eab55` 加工作区逐项对照；附件与 `docs/RAI_MODEL_ARCHITECTURE.md` SHA-256 相同，原文不改。完整阶段矩阵、14 项主缺口／细项、可选未来能力、规格差异与源码证据见 [核查报告](audits/RAI_MODEL_DESIGN_GAP_AUDIT_2026-11-06.md)。

结论：M1 特征、M2 HRR 负荷、M3 demonstrated 跑力、M5A 日常事实、M5B Recovery 和 M6 Readiness 已有真实候选实现，不能沿用 11 月 2 日“下一步开始 M1”的旧结论。原设计 M4 自有条件式 RAI VO₂max 仍未实现；后续 M4 是 external projection，不能替代它。（2026-11-08 更新：自有条件式 M4 已实现并用合成数据完成真实 PG 数值发布验收，见下文「M4 自有条件式 RAI VO₂max 实施与合成验收」。）后续 M7 是产品合同工程集成，也不等于原设计 M7 现实校准完成。正式新指标 HTTP／UI、训练消费者、历史趋势、统一六个月回算／差异预览、candidate 激活及回滚执行器仍未闭环。夜间 HRV、睡眠规律性／自报目标／持续偏离、baseline 自动供 RHR reference、完整长跑诊断仍缺；跨 UUID 重叠负荷隔离和设备偏移专项未见源码实现，未在本轮数据库或真机复现。Readiness 的 180 日窗及滚动 B、全天 SDNN、保守 HR 支持域等与原文不同，按后续合同记录，未擅改公式。

本机实际执行 `scripts/local/test.sh gate` PASS（elapsed 2s；Go race 使用正常缓存、vet、258 包依赖边界、控制器安全检查）；非缓存定向 `unit -count=1` 对 workoutfeatures／trainingload／performance／vo2maxexternal／recovery／readiness／athletemodel／analysis 八包全部 PASS（elapsed 6s）。日志 `.local/logs/model-design-audit-20261106/{gate,model-tests}.log`。本轮仅增加审计文档和实施记录，无事务／协议／可执行代码变更，未重跑 PG／Swift／Simulator／真机；既有 PG 与设备报告仅作历史证据，不冒充本轮验收。

真实 completeness v2 transport 已有代码及隔离验证，但普通用户同步接入、全量真机证明及 M6 复验仍未闭环；近期实际真机同步测试不等于全部模型验收。现实资格研究、纵向留出校准、生理效度、shadow、产品激活及回滚演练仍未完成。未读取数据库或个人健康记录，未改原设计／原方案／旧 running，未执行 migration／部署／激活／真实数据迁移／commit／push；保留入口既有 IMPLEMENTATION 与图标改动。


## 运动能力模型补全实施方案、验收矩阵与Codex提示词（2026-11-07，仅文档）

按用户要求，在11月6日实现差距核查的基础上新增三份可交给后续Codex实施的文件：[完整实施方案](RAI_MODEL_COMPLETION_PLAN.md)、[验收标准与152项矩阵](RAI_MODEL_COMPLETION_ACCEPTANCE.md)、[完整执行提示词](RAI_MODEL_COMPLETION_CODEX_PROMPT.md)。覆盖G01–G14、E0–E11共12阶段，以及已有定义的maximal_supported／CS-D′／效率／负荷消退研究模块；Economy／CP／无独立证据阈值等future项不伪造实现。

方案明确保留已冻结v1和后续Readiness的180日／36h／滚动42日B等数学；夜间HRV／睡眠规律性／目标／持续偏离进入新版本。补全可信HR reference、独立RHR baseline、重叠Workout未知处理、来源epoch与误标、完整RAI VO₂敏感性／LOO、普通completeness接入、六个月有界回算、active API／历史／共享详情、原生UI／计划adapter、门禁证据与实际synthetic切换回滚。新公式／阈值为拟议工程规格，不表示已校准或已有运行结果。真实健康读取／研究／部署／启用未因交付文档获授权，默认真实active仍legacy。

本轮实际验证：本机 `scripts/local/test.sh gate` PASS（elapsed 2s；Go race正常缓存、vet、258包依赖边界及控制器安全检查）；文档检查PASS：3文件、12实施阶段、G01–G14全覆盖、152个唯一且连续分组的验收ID、15个链接、244个表格行、跨文档MC引用与围栏；原设计／原架构方案SHA未变。证据 `.local/logs/model-completion-plan-20261107/{gate.log,document-review.json}`。本轮只写文档，未实现模型／API／migration／UI，未运行PG／Swift／Simulator／真机或现实校准，未读取数据库或健康记录、部署、迁移真实数据、切入口／激活、commit／push。既有IMPLEMENTATION前缀和用户图标／核查报告改动保留，后续实际实施须重新核对HEAD／状态，不把本计划当PASS报告。

## 运动能力模型补全工程 — 本轮实施结果（2026-11-07）

按 [补全方案](RAI_MODEL_COMPLETION_PLAN.md)、[152 项验收矩阵](RAI_MODEL_COMPLETION_ACCEPTANCE.md) 与用户本轮直接要求，在 `main / 18eab55` 加工作区上完成 E0–E11 的可执行工程部分；未 checkout、未 reset/clean/stash、未修改 `../running`、未 commit/push、未部署、未读取个人健康库或生产 DSN。详细决策与证据见 [DECISIONS](RAI_MODEL_COMPLETION_DECISIONS.md)、[RESULT](RAI_MODEL_COMPLETION_RESULT.md) 和 `docs/evidence/model-completion/`。

本轮新增/接入：
- 纯域：`internal/hrref`（hr_reference/2、跨 UUID 重叠、source epoch、误标）、`internal/vo2max`（`rai.vo2max.submax/v1` 全估计器）、`internal/recovery/v2.go`、`internal/readiness/v2.go`、`internal/trainingload/v2.go`、`internal/performance/v2.go`、`internal/diagnostics`、`internal/backfill`、`internal/research`、`internal/modelmanifest`、`internal/modelinput`。
- 产品/API：`rai.athlete_model/2` 与 `rai.product_metric_mapping/2`，六 typed metric（RAI 自有 VO₂ 与 external 独立）；`GET /v2/analysis/athlete`、`GET /v2/analysis/athlete/history`（opaque cursor 绑定 dataset/metric/assignment revision/view/range，真续页）；`POST /v2/model-inputs/{kind}` 与 `POST /v2/model-inputs/{id}/revocations`；Swift「分析 → 填写个人参考与目标」表单提交心率参考/睡眠目标（服务器追加式 receipt，不本地改写）；active/previous/candidate 通道与 assignment audit；migration 37 允许版本化 v2 artifact 但强制匹配 v2 RUNNING run/schema/generation，`ProjectLoadV2` 保留 v2 身份，v2 scope 改为按 active stack 生成并由 product reader 使用；新增六槽真实 artifact active 投影测试，正式 HTTP 路由返回完整 schema2 六项与 v2 load history 点；schema2 响应状态按可用槽位 truthful 标注（unavailable/partial/complete）；隔离 PG 测试验证显式 synthetic-v2 active 且无 artifact 时 product schema2 返回 typed null、history 空页而不是 candidate/legacy 或假值。
- 执行器：`PromotionGate` 绑定 manifest/config/feature/source/input/mapping2/consumer/synthetic 标记；`PromoteModelStack`/`RollbackModelStack` 在 dataset 行锁内重验，append-only audit；`rai admin backfill plan|execute|resume|status|cancel|diff` 使用通用 `model_backfill_operations` checkpoint，只有全部块完成才 candidate_ready。
- 迁移仅追加 `00030`–`00036`：typed input/receipt/backfill/audit/previous、旧 VO₂ input kind 兼容、active 通道写保护、`datasets.synthetic_test`。migration `00029` 及更早原文/checksum 未改。
- Swift：普通 `performRecovery()`/`runWorkouts()` 复用 completeness v2 状态机；schema1/2 解码、六 metric 卡片、中文缺失原因、RAI VO₂ 实验标签、外部 VO₂ 独立。

真实执行证据：
- `bash scripts/local/test.sh full` **PASS**：822s，exit 0，20 个 Go 包 `ok`，0 FAIL、0 SKIP，`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`；含 race/vet/build/sqlc diff、9 项依赖边界、全量隔离 PG18 集成。
- `bash scripts/local/test.sh quick` PASS；`go vet ./...`、`python3 scripts/check-boundaries.py` PASS。
- 定向隔离 PG PASS：B4/external VO₂ 75.7s 0 FAIL；M6 completeness→readiness；backfill staged 生命周期；typed input 撤销 append-only/幂等；synthetic promotion/rollback（含未标记 dataset 拒绝）；migration 33–36 targeted；typed input 撤销 append-only/幂等隔离 PG 与 httptest PASS。
- `swift test` 235 tests/32 suites PASS；`xcodebuild ... -destination 'id=D44B6925...' build` **BUILD SUCCEEDED**。
- 真实失败历史保留：`.local/logs/model-completion/full-pre-0200.log`（02:00 前 19 个失败测试，主要为 fixture 时间保护、active 通道 guard、legacy VO₂ kind、M6 policy hash 编码）；修复后 full 重跑 PASS，未修改门槛或伪造 fresh 证据。

未验证/未授权边界：未执行 30 次正式性能基准（F01–F10 NOT RUN）、真实设备 D01–D10（NOT RUN）、真实研究 C05–C08（NOT RUN）、正式 App→HTTP→PG→Worker Simulator E2E、真实 dataset 激活/回滚、v2 worker 全链与 backfill 实际模型执行；这些在 RESULT/acceptance 中保留 PARTIAL/NOT RUN，不因 full PASS 或合成切换 PASS 被改写成真实产品/生理验证通过。工程门禁通过不等于模型校准、产品启用或真实数据可用性通过。

### 2026-11-07 追加：正式 API/模型性能与 reader 防护（round 11）

- migration 37 `00037_model_artifact_v2.sql` 仅追加允许版本化 v2 artifact（load v2、state v2、ability/performance v2、vo2max submax、recovery/readiness v2），并重写 artifact insert 触发器校验 RUNNING run、schema/algorithm/version、payload dataset/generation；`RequiredSchemaVersion=37`。v1 migration/checksum 未改。
- `trainingload.ScopeV2For`/`ScopeStateV2For`、`performance.ScopeFor`、`vo2max.ScopeFor` 统一 active stack scope；`athletemodel.ProjectLoadV2` 保留 v2 identity；正式 `GET /v2/analysis/athlete` 在隔离 PG 六槽 artifact 下返回六项 typed metric 与 `complete_response`，`/v2/analysis/athlete/history?metric=trainingLoad` 返回 v2 负载点；reader 对 tampered content hash 返回 `technical_failure`，对 unclean generation 不服务旧 artifact。
- 正式优化构建 loopback TCP 性能（30 attempts，warmup 3）：当前快照 P95=2.33ms、最大 2 SELECT；90 点 v2 load 历史 P95=2.32ms、最大 2 SELECT；预算 500ms/6 SELECT 与 750ms/8 SELECT，均 PASS。纯模型 P95：VO₂ 0.001ms、Recovery 1.465ms、Readiness 0.047ms。240min feature fixture 110.1ms、42,064B，因未达 250k 点密度仅 PARTIAL。日志 `.local/logs/model-completion/{performance-http,performance-models,performance-feature,integration-reader-guard}.log`。
- `bash scripts/local/test.sh full` 最终 **PASS**：916s，exit 0，0 FAIL/0 SKIP，leftover DB=0、cluster removed。此前 full 821s/827s PASS 以及 02:00 前失败快照均保留，不覆盖。
- 未验证边界不变：F06–F10、F04 250k 密度、正式 App→HTTP→PG→Worker Simulator E2E、D01–D10、C05–C08、真实 dataset 激活/rollback 未完成；不能把本段性能子集或合成 active 六槽说成全部性能/真实产品/生理验证通过。

- 注：后续 round 11 已追加 migration `00037_model_artifact_v2.sql`；迁移范围以 `DECISIONS`/`RESULT` 的 `00030`–`00037` 为准。

### 2026-11-07 追加：F06 跑力/诊断 30 次性能（round 13）

- 新增 `apps/server/internal/performance/performance_p95_test.go` 与 `apps/server/internal/diagnostics/performance_p95_test.go`，固定合成 fixture，n=30：跑力 demonstrated P50=33.65ms、P95=35.10ms、max=35.93ms、allocs/op=9554；durability+critical-speed+42 日 trend P50=5.53µs、P95=18.84µs、max=22.66µs、allocs/op=76，均低于 2s 门槛，未放宽 fixture、计时范围或门槛。
- 证据：`.local/logs/model-completion/performance-diagnostics.log`；`acceptance.json` F06 更新为 PASS，矩阵 71 PASS/62 PARTIAL/19 NOT RUN；manifest 80 artifacts/19 commands 且无 stale。
- 仍未执行 F07–F10、F04 250k 点密度、正式 App→HTTP→PG→Worker Simulator E2E、D01–D10、C05–C08、真实 activation/rollback；F06 PASS 不外推为全性能或真实产品验收。

### 2026-11-07 追加：v2 历史标量投影与 cursor 合同（round 14）

- `internal/storage/postgres/athlete_product_v2.go` 的 `ReadAthleteHistory` 不再用统一 `{asOfUs,value}` 猜测所有 payload：新增 `historyPoint` 按六 metric 分别解码 `trainingLoad`（匹配 day 的 acute）、`runningAbility`（ability.score）、`physiologicalRecovery`/`trainingReadiness`（score）、`raiVO2Max`/`externalVO2Max`（value），保留真实单位/状态；合格 `complete` 映射为产品态 `available`，缺值仍为 unavailable，不填 0。
- 新增 `internal/storage/postgres/athlete_history_projection_test.go`（六 metric 标量/单位/状态）与隔离 PG `TestHistoryCursorBindsMetricAndReturnsRealV2Values`：两页 limit=1 返回真实 v2 值 15/12；跨 metric 复用 cursor→409 `history_cursor_mismatch`；limit=91→422。
- 日志：`.local/logs/model-completion/{history-projection,integration-history-cursor}.log`。P04 仍为 PARTIAL：28 日两点 trend 尚未实现；不把 cursor/标量修复外推为完整历史趋势验收。

### 2026-11-07 追加：round 14 全量与历史端到端

- `bash scripts/local/test.sh full` 在新增 history 投影和 F06 测试后 **PASS**：952s，exit 0，0 FAIL/0 SKIP，leftover DB=0，cluster removed；日志 `.local/logs/model-completion/full-round14.log`。
- 这是当前最终门禁证据；此前 822s/916s PASS 与失败历史保留。P04 仍 PARTIAL（28 日两点 trend 未实现），其余验收计数以 `acceptance.json` 为准。

### 2026-11-07 追加：P04 28 日真实趋势（round 15）

- `athletemodel.HistoryPage` 新增 `HistoryTrend`（`kind/fromDate/toDate/days/fromValue/toValue/delta/unit/reasonCodes`），由 `ReadAthleteHistory` 从真实已发布点计算；不复用预测、不插值。
- `historyTrend` 选择距最新点 28 天最近的真实点；要求至少 28 天跨度、两点值非 nil 且 ModelVersion/PayloadVersion/ConfigHash/Mode/Source 完全一致；任一变化返回 `history_trend_context_break` 且 delta=null。`HistoryPoint` 增补 configHash/mode/source 字段。
- 单测 `TestHistoryTrendUsesRealTwentyEightDayPair` 覆盖 10→15 delta=5 及 version/config/mode/source 四种断线；隔离 PG `TestHistoryCursorBindsMetricAndReturnsRealV2Values` 断言两页真实值 15/12、跨 metric 409、limit=91 422、28 日 trend delta=5。
- acceptance P04 更新为 PASS；152 IDs 当前 72 PASS/61 PARTIAL/19 NOT RUN。
- 日志：`.local/logs/model-completion/{history-projection,integration-history-cursor}.log`。
- `bash scripts/local/test.sh full` 在 P04 trend 后 **PASS**：960s，exit 0，0 FAIL/0 SKIP，leftover DB=0、cluster removed；日志 `.local/logs/model-completion/full-round15.log`。此前 952s PASS 保留。

### 2026-11-07 追加：E10 合成研究运行工具（round 16）

- 新增 `apps/server/cmd/rai-research`：读取合成 study JSON，执行 `CreateSnapshot`/future-leak 校验、可选 `FreezeKnownAt`、`TemporalSplit`、`AthleteSplit`、先撤回后 `LinkOutcomes`、`ClusterSummary`、`ValidateAddendum`；输出仅 aggregate counts/CI/抑制组，无 athlete key、prediction 或 outcome 值。
- `main_test.go` 覆盖 3 条合成预测的 2/1 temporal、1/2 athlete split、3 对配对、c2 N=1 抑制与 addendum gate。
- 实际运行 `go run ./cmd/rai-research -input .local/logs/model-completion/research-study-withdraw.json -out ...` exit 0：freeze 后 2 条、撤回 a 后 1 条配对、withdrawn=1；日志 `.local/logs/model-completion/research-cli-withdraw.log`。
- C01–C04 仍 PARTIAL：缺真实导出、化名映射分离、审计落库与数据库级同 Workout 窗口互斥；不把合成工具说成真实研究验证。

### 2026-11-07 追加：E10 合成研究运行工具（round 16）

- 新增 `apps/server/cmd/rai-research`：读取合成 study JSON，执行 `CreateSnapshot`/future-leak 校验、可选 `FreezeKnownAt`、`TemporalSplit`、`AthleteSplit`、先撤回后 `LinkOutcomes`、`ClusterSummary`、`ValidateAddendum`；输出仅 aggregate counts/CI/抑制组，无 athlete key、prediction 或 outcome 值。
- `main_test.go` 覆盖 3 条合成预测的 2/1 temporal、1/2 athlete split、3 对配对、c2 N=1 抑制与 addendum gate。
- 实际运行 `go run ./cmd/rai-research -input .local/logs/model-completion/research-study-withdraw.json -out ...` exit 0：freeze 后 2 条、撤回 a 后 1 条配对、withdrawn=1；日志 `.local/logs/model-completion/research-cli-withdraw.log`。
- C01–C04 仍 PARTIAL：缺真实导出、化名映射分离、审计落库与数据库级同 Workout 窗口互斥；不把合成工具说成真实研究验证。
- research CLI 增加后最终 `bash scripts/local/test.sh full` **PASS**：839s，exit 0，0 FAIL/0 SKIP，leftover DB=0、cluster removed；日志 `.local/logs/model-completion/full-round16.log`。

### 2026-11-07 追加：E9 合成 shadow 聚合工具（round 17）

- 新增 `apps/server/internal/shadow` 与 `apps/server/cmd/rai-shadow`：仅接受 `synthetic=true`；按 cohort 聚合候选/legacy 差异，输出 n、配对 n、meanAbsDiff、bias；低于 `minCohort` 的组只保留 n 并列入 suppressed，不输出个体 row。
- `shadow_test.go` 覆盖小组抑制、`synthetic=false` 拒绝、全组被抑制时失败、输出不含 athlete key；`go test ./internal/shadow` PASS。
- 实际运行合成 10 条（`local-5k` 6 条、`trail` 4 条，其中 1 条 candidate null）：exit 0，paired=9，`local-5k` 可见，`trail` 因 nPaired=3 < 5 被抑制；`synthetic=false` 输入 exit 1 `shadow_requires_synthetic_dataset`。日志 `.local/logs/model-completion/shadow-cli.log`，报告 `shadow-report.json`。
- X10 由 NOT RUN 更新为 PARTIAL：合成/仅 aggregate/组大小抑制/不影响 active 已有真实命令证据；尚未接 candidate reader 或生产 shadow 调度，不能称为真实 shadow 上线。

### 2026-11-07 追加：F08 合成切换延迟（round 17）

- `integration/model_promotion_test.go` 对真实隔离 PG 的 `PromoteModelStack`/`RollbackModelStack` 计时并加 ≤2s 断言：promote 3.221ms，rollback 1.663ms，测试 PASS，leftover=0、cluster removed。
- 日志：`.local/logs/model-completion/integration-rollback-latency.log`。
- F08 更新为 PARTIAL：合成切换延迟满足预算；API+Worker 峰值 RSS ≤768MiB 与前端 ≤10s 尚未测量，不能据此签核整项性能。


### 2026-11-07 追加：P05 新分析详情读取发布 feature（round 18）

- `workoutdetail.Detail` 新增可选 `analysis` 投影：schema/algorithm/version/stackID/publishedAtUs/activeSeconds/HR/speed/route coverage/reasonCodes；legacy 详情字段与行为保持不变。
- `storage/postgres.loadFeatureAnalysis` 只在 publication + SUCCEEDED run + `workout_features` artifact + `dirty_scopes.generation=clean_generation` 全部匹配时读取最新 `metric_family='workout_model'`；校验 feature schema/dataset/workout、按 canonical JSON 复核 content SHA-256；dirty/pending 时不返回分析投影。
- 新增隔离 PG 集成测试 `TestWorkoutDetailReadsPublishedFeatureArtifact`：真实写入 feature artifact/publication/clean generation 后，详情返回 HR 0.9/speed 0.8/route 0.7、activeSeconds 600、missing_hr；HTTP 详情 200；把 dirty generation 提高后分析投影变为 null，raw/legacy 详情仍可用。日志 `.local/logs/model-completion/integration-detail-feature.log`，leftover=0、cluster removed。
- acceptance P05 与 F09 更新为 PASS；152 IDs 当前 74 PASS/62 PARTIAL/16 NOT RUN。

- F09：S60 持久 ACK 以优化构建、真实 loopback TCP/auth/handler/PG、30 attempts+warmup 3 实测：P50=2977.739ms、P95=3932.603ms、failures=0；对比 2026-11-06 baseline P95=5646ms 改善 30.3%，满足≤15%退化预算；历史同步 FAIL 记录保留。日志 `.local/logs/model-completion/integration-ack-s60-round17.log`。

- 追加 migration `00038_published_artifacts_view.sql`：`analysis_published_artifacts` 为 security-barrier 视图，只暴露 SUCCEEDED run + 已发布 + generation=clean_generation 的 artifact；`rai_api` 只获该视图 SELECT，不获 `analysis_artifacts` 基表权限。product/detail reader 改用视图。B4 受限 API 角色真实回归 `TestB4ProductHTTPAndGenericReadIsolation` PASS，日志 `.local/logs/model-completion/integration-p05-b4.log`。

- P02：schema2 envelope 增补 `evidenceAt/computedAt/publishedAt/validationStatus`，只在 v2 projection/reader 输出，v1 golden 不变；`StampPublication` 由 product reader 在 schema/model/generation/artifact 校验成功后写入，ability 等缺少 evidenceAt 时从真实 asOf 复制，不伪造时间。六 metric HTTP 测试断言全部时间和验证字段，mappingVersion 全部为 2。P02 更新为 PASS；152 IDs 当前 75 PASS/61 PARTIAL/16 NOT RUN。swift 235 PASS；最终 full 845s，0 FAIL/0 SKIP，leftover=0，cluster removed（`full-round19.log`）。

- P12/客户端边界：新增 `scripts/check-product-boundaries.py` 并接入 quick/gate 公共边界步骤。扫描 43 个 Swift 文件，强制 HealthKit `toShare:[]`、禁止 save/delete、WorkoutKit 调度、通知 API 与 Swift candidate 入口；输出 `PRODUCT BOUNDARY PASS`。P12 更新为 PASS；152 IDs 当前 76 PASS/60 PARTIAL/16 NOT RUN。最终 full 936s，0 FAIL/0 SKIP，leftover=0，cluster removed（`full-round20.log`）。

- T04：重跑真实隔离 PG18 的 backend termination/崩溃恢复矩阵（features、load、performance、recovery、readiness、VO2 external、completeness、job claim，另含 B4 macOS SIGKILL）：无半发布 artifact/result/publication，重试一致，leftover=0、cluster removed；日志 `integration-t04-crash.log`。T04 更新为 PASS；152 IDs 当前 77 PASS/59 PARTIAL/16 NOT RUN。

- T05：真实隔离 PG18 验证 N+1 generation、lease 过期重领、fencing token 单调、旧 token `lease_lost`、旧 generation 不得覆盖新 publication、并发 claim/crash 无重复发布；日志 `integration-t05-fencing.log`。T05 更新为 PASS；152 IDs 当前 78 PASS/58 PARTIAL/16 NOT RUN。

- T07/T08：真实隔离 PG18 运行全部 15 个角色边界测试（API/Worker/owner/admin、B4、训练、恢复、负荷、性能、完整性、外部 VO2），全部按最小权限拒绝；athlete/model 视图验证 dataset 隔离、RepeatableRead 稳定快照、corrupt hash/schema/missing publication 的技术失败与业务 null 区分、assignment/delete 竞争后完整新视图、新用户不补零。T07、T08 更新为 PASS；152 IDs 当前 80 PASS/56 PARTIAL/16 NOT RUN。

- T02/T06：真实隔离 PG18 重跑删除/上传双锁顺序、commit retry、route delete receipt 回滚、daily rollback/backend crash、analysis publish fault 与 completeness backend crash；另跑 workout delete、late child/series/route、published P4 历史保留、RHR 替换/sleep 跨日、readiness delete/rebuild、无 workout 日依赖、load generation race/delete。全部 PASS，leftover=0、cluster removed。T02、T06 更新为 PASS；152 IDs 当前 82 PASS/54 PARTIAL/16 NOT RUN。

- T03（仍 PARTIAL）：新增 `TestModelInputReceiptFailureRollsBackInputAndDirty`，真实 PG 中在 `model_input_receipts` 注入 trigger 故障，验证 input/receipt/dirty 同事务回滚、无残留；移除故障后同字节重试落一行。日志 `integration-t03-input-fault.log`。dependency 环与跨 subject dataset 故障矩阵仍未单独覆盖。

- T03（仍 PARTIAL）：新增 `TestModelInputCannotCrossDataset`，真实 PG 两 dataset 中跨 dataset 撤销返回 not found，目标 dataset 输入不变；结合 receipt 故障回滚、幂等/撤销、audit/promotion、DependencyDAG 环检测单测。日志 `integration-t03-cross-dataset.log`。
- T10：新增 `TestModelCompletionIndexAvailability` 并重跑 `TestCoreIndexAvailability`：reference/overlap index-only scan、artifact/history 主键、history 带 day 范围、load ledger day 索引、workout/sample/route/job 索引可用；结合性能测试 2 SELECT/请求，无 N+1。T10 更新为 PASS；152 IDs 当前 83 PASS/53 PARTIAL/16 NOT RUN。

- P08：新增 `ProductReasonText` 与格式化单测（score 0 位、VO₂ 1 位、非有限 `--`、中文 reason、实验/独立来源文案），`AthleteModelCard` 改用统一文本；Swift 236 tests PASS，Simulator App build SUCCEEDED。P08 更新为 PASS；152 IDs 当前 84 PASS/52 PARTIAL/16 NOT RUN。

### 运动能力模型补全 — Round 26 训练模型绑定

- `training.Config.ModelBinding` 追加 assignment/version/manifest/model/asOf/adapter/missing lineage；`CreateTrainingPlan` 在隔离事务中读取 active channel 并写入，`ChangeTrainingPlan` 不允许客户端改写。
- 隔离 PG18 测试 `TestTrainingPlanRecordsModelBindingAndRollbackAdapter`：legacy→synthetic-v2→legacy 三次新 plan 的 adapter 分别为 `legacy-v1`、`schema2-v2`、`legacy-v1`，历史 plan 保持 `schema2-v2`。日志 `.local/logs/model-completion/integration-p11-binding.log`。
- `LoadTraining` 仅当 active stack 非 legacy 时读取已发布、clean、fresh/available/complete 的 Readiness v2；Legacy adapter 不消费 v2，partial/stale/unknown 不填 100。`TestTrainingContextUsesV2ReadinessSignal` 与 `TestTrainingContextIgnoresV2ReadinessOnLegacyAdapter` 真实 PG PASS。
- 训练生成器接受 Readiness v2 signal 版本，`TestGeneratorConsumesV2ReadinessSignal` PASS。
- Round 26 最终 `bash scripts/local/test.sh full` **PASS**：915s，exit 0，0 FAIL/0 SKIP，leftover DB=0、cluster removed；日志 `.local/logs/model-completion/full-round26.log`。包含 P11 训练 binding、Readiness v2 adapter、legacy 隔离三组真实 PG 测试。
- Round 26 Simulator E2E：`bash scripts/local/test.sh e2e` **PASS**，105s，`TestWorkoutCompletenessSimulatorHTTPPostgres` 67.03s，contract=v2、manifests=2、streams=12、lost_ack=true、unknown_loads=1，leftover=0、cluster removed；日志 `.local/logs/model-completion/e2e-round26.log`。该证据只覆盖完整性流程，不代表 schema2 首页/详情/历史已 E2E。
- Round 26 Swift：`swift test` 236 tests/32 suites PASS；`xcodebuild` Simulator BUILD SUCCEEDED；P11 binding 四组真实 PG 测试 PASS；证据日志 `swift-test-round26.log`、`xcodebuild-round26.log`、`integration-p11-binding.log`。
- Round 26 Simulator UI：`bash scripts/local/test.sh simulator` **PASS**（90s）；xcresult 汇总 85 passed / 0 failed / 3 skipped；导航视觉基线、文本裁剪 accessibility audit、首页滚动性能用例均通过。3 个 skip 保留（辅助功能动态字号专用矩阵未执行），不冒充 P09 全通过。日志 `.local/logs/model-completion/simulator-round26b.log`、`.local/logs/model-completion/simulator-round26.log`。
- Round 26 P10：低值提示抽为 `ProductReasonText.lowReadinessNotice` 并有单测；训练 adapter stale/partial/unknown 保守测试 PASS；P10/P11 更新 PASS（Load v2 仍未直接作为训练输入，保持文档说明）。

### 运动能力模型补全 Round 27 — E10 化名与输入/outcome 互斥

- `apps/server/internal/research/pseudonym.go`：新增 keyed HMAC-SHA256 athlete pseudonymization，映射与 aggregate 报告分离；secret 至少 16 bytes，短 secret 拒绝。`cmd/rai-research` 增加 `-pseudonym-secret-file` 与 `-pseudonym-map`，映射文件 0600。
- 实际合成运行：`research-study-withdraw.json` → `research-report-pseudonym.json`，报告无 athlete 原键；映射单独写 `research-pseudonym-map.json`；日志 `research-cli-pseudonym.log`，exit 0。
- `research.Prediction` 增加 optional `InputWorkoutIDs/InputFrom/InputTo`，`Outcome` 增加 optional `WorkoutID/WindowFrom/WindowTo`；`LinkOutcomes` 遇到同 Workout 或重叠输入窗口时拒绝链接并计入 `OverlapExcluded`，不把输入窗口当独立 outcome。
- 实际合成运行：`research-study-overlap.json` → `research-report-overlap.json`，`paired=0`、`overlapExcluded=1`、unpaired prediction/outcome 各 1；日志 `research-cli-overlap.log`，exit 0。
- `go test ./internal/research ./cmd/rai-research`、`bash scripts/local/test.sh quick` PASS。C01/C02 工程工具状态更新 PASS；C03/C04 仍 PARTIAL，C05–C08 无真实授权/outcome 保持 NOT RUN。
- Round 27 续：`research.Prediction` 增加输入 Workout/时间窗，`research.Outcome` 增加 outcome Workout/时间窗；`LinkOutcomes` 对同 Workout 或重叠窗口拒绝链接并计数 `OverlapExcluded`。合成 `research-study-overlap.json` 实跑 `paired=0`、`overlapExcluded=1`，日志 `research-cli-overlap.log`，exit 0。C02 工程工具更新 PASS。

### 运动能力模型补全 Round 28 — E10 cluster oracle 与 study addendum gate

- `internal/research/analysis_test.go` 增加 cluster-aware 独立 oracle：单一运动员 block 的 1/2/3 deltas 手算均值 2，bootstrap 全重采样同一 block，CI 下限=上限=2；censored/nil 值作为 abstention，零分母不输出 mean/CI，小组抑制保持计数。
- `internal/research/types.go` 的 `StudyAddendum` 增加 outcome source、observation window、acceptance threshold、authorization basis、withdrawal procedure、stopping rule；`addendum.go` 对缺项/非法时窗/非正阈值关闭 gate。
- `research_test.go` 对六类产品 model identity 逐一验证 gate，并验证删除 stopping rule 后关闭。
- 合成 `rai-research` addendum v2 输入 `research-study.json` 实跑 exit 0，`addendumValid=true`；日志 `research-study-v2addendum.log`。C03/C04 更新 PASS；真实研究/真实 gate 仍未执行。

### 运动能力模型补全 Round 29 — 研究 artifact、RHR 回退、schema2 E2E 与真机验收

- 新增 `apps/server/internal/storage/postgres/model_stack_research.go`：日栈在同一次 REPEATABLE READ 计算中发布 5 个研究 artifact：`running_ability_maximal_v2`、`workout_durability`、`aerobic_efficiency`、`critical_speed`、`load_settling`。四诊断 schema 为 `rai.*/1`，maximal 复用 `rai.running_ability/2`；family 统一为 `model_research_v1/<key>`，不占用 6 个产品槽。
- `model_stack_inputs.go` 增加 `dailyRHRFacts`：缺显式 RHR 时按训练日前 28 日、同 epoch、至少 14 个日常值且前 7 日至少 3 个值取中位数，交给 `hrref.Resolve`；来源 epoch 由现有 recovery fact 映射到 Workout source epoch，限制已记录。
- 迁移 00043 注册 5 个研究 artifact 的 key/schema/algorithm；00044 把 `rai_backfill_progress` 的 6 槽判断限定为 `model_product_v2/%`，避免研究 family 影响产品完成判定。`RequiredSchemaVersion=46`，`migrations/checksums.json` 已更新；00045 新增 `analysis_published_results` 只读视图，API 不再直接读取 `analysis_results`；00046 将 `rai_training_dirty` 收紧为 SECURITY DEFINER，撤销 API 对 jobs 的直接更新授权。
- `TestBackfillOperationLifecycleAndStagedCandidate` 真实隔离 PG PASS：initial + 3 个回算日、candidate_ready、历史 as-known 拒绝；此前研究 family 被计入 6 槽导致 `claim no rows`，00044 修复。日志 `.local/logs/model-completion-fixes/integration-backfill.log`。
- 修复 DEBUG-only 验收配置：`ProductModel.configure()` 仅在 Simulator 且 `#if DEBUG` 下读取 `RAI_ACCEPTANCE_CONFIG`，支持 inline JSON；Simulator 同时等待 `SyncModel.shared.busy` 避免与 `recoverPending()` 的会话切换竞争。`ProductUITests` 通过 test runner 把 `RAI_MODEL_E2E_CONFIG` 文件内容作为 inline JSON 传入 App。App Debug target 补 `SWIFT_ACTIVE_COMPILATION_CONDITIONS=DEBUG`。`scripts/local/test.sh e2e` PASS（90 s）：真实 App→HTTP→PG→Worker→promote/rollback，active_home/workout_detail/history/rollback 均 true，日志 `.local/logs/model-completion-fixes/e2e-race.log`。
- `scripts/local/test.sh full` **2026-11-07 历史基线**：873 s，exit 0，648 个顶层 PASS，0 FAIL，22 个 opt-in skip，leftover DB=0、cluster removed；日志 `.local/logs/model-completion-fixes/full-after-research.log`。该日志早于当前 HEAD，不是当前门禁证据。`scripts/local/test.sh swift` PASS：238 tests / 34 suites，日志 `swift-after-research.log`。
- 性能（非 race，`-tags=integration,modelperf`）PASS：F04 240 min/250,000 点/30 次，P95 1.894872438 s、max 1.905399276 s、artifact 42,127 B；F07 180 日/180 Workout/180 jobs/6 个 31 日块，22.522589121 s；日志 `perf-model-norace-v.log`。F08/F10 未执行，F09 仍为旧 S60 1,000 ms 目标超限。
- 真机（授权只读）：`RAI_LOCAL_DEVICE_CONFIGURATION=Release python3 scripts/local/device.py prepare/install/probe/list`。iPhone 12 Pro Max（iPhone13,4）、iOS 27.2、Release 0.6.0(1)、Apple Development 签名安装成功；probe handshake 70 ms，原 session manifest 未改变；`list` 观测 100 个可见 Workout（51 eligible/49 excluded/0 unclassified），mainThreadMaxStallMs=1.2、peak RSS=573,669,376。证据 `.local/device-acceptance/install-result.json`、`.local/logs/model-completion-fixes/device/workout-list-report.json`。D01 记 PASS。
- 真机 `completeness` 在 17 个 Workout 后 BLOCKED（`completeness_stream_pending`）：一条 Workout 的 HeartRate、DistanceWalkingRunning、RunningVerticalOscillation、RunningGroundContactTime、RunningPower 返回 `metadata_requires_review`，coverage 未完成，客户端按契约不生成 receipt/committed anchors。D02 记 FAIL，未删除字段或伪造空收据；证据 `.local/logs/model-completion-fixes/device/completeness-report.json`、`client-task-state.json`。D04/D08 仅 PARTIAL，D03/D05/D06/D07/D09/D10 未运行。
- 验收矩阵 `docs/evidence/model-completion/acceptance.json` 当前 152 项：99 PASS / 41 PARTIAL / 11 NOT RUN / 1 FAIL；`docs/RAI_MODEL_COMPLETION_RESULT.md` 已按真实证据重写，`docs/RAI_MODEL_COMPLETION_DECISIONS.md` 已补 00043/00044 与研究 artifact/RHR 决策。
- 未完成项保持未通过：Feature `rai.workout_features/v2` 合同未实现（Feature 仍 v1）；研究诊断未在产品详情展示；真实设备 RHR/SDNN/Sleep 来源未出现；完整无障碍矩阵、F08/F10 与 D03/D05/D06/D07/D09/D10 未执行；真实模型校准与真实产品激活仍未授权。

### 同步链修复 — P0 完成、P2 客户端复用被 v2 合同阻塞（2026-11-07）

本轮按 `docs/SYNC_CHAIN_REMEDIATION_PLAN.md` 实施 P0 正确性修复，并尝试 P2 客户端 proof 跨周期复用。真实 App→HTTP→PG18 e2e 证明该复用与冻结的 v2 合同冲突（服务端要求每个 stream 分页和 proof series 都晚于本周期 `knownCutoffUs`），因此复用路径已撤回，不声称 P2 完成；P1、P2 服务端缓存、P3、P4 未实施。

- P0-1：`SyncCoordinator.coverageFromBundle` 改为只解析匹配 boundary 的 multipart，并从 `observation.coverage` 读取；错误 boundary、截断 body、裸 JSON 显式失败，不再静默回退 `task.coverage`。`WorkoutSyncReuseTests.testCoverageIsReadFromMultipartFramingOrNotAtAll` 覆盖成功与三类负例；恢复路径合成 bundle 补齐 observation。
- P0-2：`WorkoutCoverage.isSatisfied/needsRetry` 将 `.unsupported`、`.noRecordsObserved` 与 `.complete` 等同视为完成，`task.missing` 和 `pendingReadKeys` 使用 `needsRetry`。`WorkoutTaskTests.unsupportedAndEmptyStreamsAreSatisfiedButFailureIsNot` PASS。
- P0-3：完整性 inventory 循环按 Workout 隔离失败，写入 `blocked`（原因、类型、次数、退避），保留已完成 proof；存在 blocked 时整轮不 stage manifest。`NormalizerTests.testBlockedWorkoutKeepsOtherProofsAndRetriesAlone` 覆盖 3 个 Workout、1 个 `metadata_requires_review`、2 个 proof 保留、退避期内零 HealthKit 查询。200-Workout fixture 未执行。
- P0-4：`acknowledgeBundle` 在计算发现水位后释放 `captureManifest`、`bundleDeletionEventIDs` 和空 `pendingReadKeys`，保留 `uploadReceipt`、`bundleSHA256`、`coverage`。单元测试断言 ACK 后编码字节下降、重复 ACK 返回原回执；未断言固定 2 KiB 上限。
- P0-5：`associateDiscoveredFacts` 无新变化时不编码、不写文件，`noopCommitSkipCount()` 可观测。`HealthDiscoveryTests.repeatedAssociationWithoutNewFactsDoesNotCommit` PASS。
- P0-6：新增 `AnalysisFollowup` 有界退避 `[2,5,15,30,60,120,300]`（总预算 ≤ 600 s），完成/失败/删除、取消、后台切换停止，耗尽记录 `analysis_followup_exhausted`；`isAlreadyDelivered` 的 analyzing 任务超过 stale 窗口也会刷新。单元测试 PASS；未运行合成服务端自动转 published 的模拟器时序用例。
- 保留的安全优化（不属于 P2 复用）：新 cycle 沿用已提交的 `classification/exclusions`，被排除 Workout 不再做 HealthKit 查询或子流读取；无结构变化时只读一次 series 判断是否需要重建 bundle，series 未变化则不强制新 bundle；不再在 `syncWorkout` 后重复读 series。
- P2 尝试与撤回：曾实现“新 cycle 复用旧 proof、仅查询根流、子流用 no-op 分页补齐、ACK 合并 anchor”。真实 e2e 在第二轮完整性提交返回 `invalid_completeness` / 422；`workout_completeness_v2.go` 要求 12 个 stream 全部存在、每个 proof series 的 `queryCompletedAtUs >= knownCutoffUs`、每个分页 `queryStartedAtUs >= knownCutoffUs`，而 `knownCutoffUs` 又必须严格前进。携带上一周期时间戳的 proof 无法同时满足这些条件。复用代码、`clearCompletenessProof`、carried-stream 测试与相应测试期望已撤回；新 cycle 重新观察全部 12 个流。失败证据见 `docs/evidence/sync-remediation-2026-11-07/contract-blocker/`。
- 证据：`docs/evidence/sync-remediation-2026-11-07/README.md`。`scripts/local/test.sh swift` PASS（245 tests / 34 suites）；`scripts/local/test.sh simulator` PASS（89 passed / 0 failed / 3 skipped，97 s）；`scripts/local/test.sh e2e` PASS（真实 App→本地 Go HTTP→一次性 PG18，`contract=v2 manifests=2 streams=12 imports=1 lost_ack=true`，93 s）；`scripts/local/test.sh gate` PASS（Go tests/vet/边界，2 s 热缓存，不含 PG/iOS/真机）。
- 未实现/未验证：P1 SQLite 持久化、写放大与迁移；P2 服务端 proof 验证缓存、锁外准备、raw payload 热路径；P3 锁预算/上传准入/Worker 分道；P4 changefeed/ETag/cache.sqlite/首页详情收敛；Phase 0 计数器与 200-Workout 固定负载。A-06..A-09、A-11、A-12 与 B-01..B-17 未执行或未测量，不能推断通过；本轮未运行真机、未运行 `go test -race -tags=integration` 全量 PG 回归，也未部署。

### Workout Sync v4 — 单 Workout 列式 Bundle（2026-11-08）

本轮按 `docs/WORKOUT_SYNC_V4_SIMPLE_PLAN.md` 落地 v4；用户已确认 **D1：接受每 Workout coverage、删除 200 天完整性证明**，**D2：保留 sample 级显式删除**。合同见 `contracts/sync-v4.md`，结果与未验证项见 `docs/WORKOUT_SYNC_V4_RESULT.md`。

- 服务端新增 `apps/server/internal/syncing/workout_bundle_v4.go`：`DecodeWorkoutBundleV4JSON` 严格解码 v4 envelope（严格 UTF-8、重复 key 拒绝、未知字段/缺失/null 拒绝、三元组/八元组定长数组），再转换为现有 `WorkoutBundle` 并走原有 `Validate()`/`ImportWorkoutBundle` 单事务写路径；表结构、receipt、幂等 digest、analysis job 未改。
- v4 wire：`sources[]`（≤16，去 device）、`streams[]`（≤32，`samples` 为 `[startUs,endUs,value]`、等长 `uuids[]`、`source`/`src[]`、可选 `metadata[]`）、`series[]`（`[stream,sample]`，总点≤1M）、`routes[]`（8 元组，后 6 项可 null，总点≤1M）、`deletions[]`（≤10k）、`routeDeletions[]`（≤256）、`observation`（provenance.workout + 每 Workout coverage；provenance.samples/routes 必须空数组）。
- `workout_bundle.go`、`bundle_decode.go`、`workout_delete.go` 接受 protocol 3/4；`bundle_observation.go` 增加 v4 coverage key 白名单（workout/route/Units 类型/series:<type>）、计数一致性和空 provenance 校验。
- `httpapi/server.go` 新增 `Options{SyncV4}`/`NewWithOptions`；只有 `SYNC_V4_ENABLED=1` 且请求头 `X-RAI-Sync-Protocol: 4` 时 handshake 返回 `workoutPayloadVersion=4`、`bundleProtocolVersion=4`、`bundleCompact=true`；导入按 `protocolVersion` 分派，关闭时 v4 返回 422 `unsupported_protocol`。`cmd/rai/main.go` 读取 `SYNC_V4_ENABLED`。
- v4 不依赖 200 天完整性问题：`storage/postgres/workout_completeness.go` 对 v4 回执明确返回 `409 completeness_not_supported_v4`，不让旧 proof 扫描误判。旧 v3 completeness/discovery 代码保留给 v3 客户端排空，S6 净删除未执行，不能视为已完成。
- 存储兼容：`storage/postgres/workout_bundles.go` 的 GET tombstone 检查覆盖 v4 `streams[].uuids`；`storage/postgres/bundle_integrity.go` 的 analysis provenance 从 v4 `sources/streams/routes` 下标还原逐样本来源，避免 v4 空 provenance 数组被误报 `analysis_source_unclassified`。
- 客户端新增 `ios/RAI/Sources/SyncCore/WorkoutBundleV4.swift`（v4 编码器、source 表、route 八元组）、`V4Outbox.swift`（按 dataset/collector scope 隔离的 sample/route identity sidecar）、`ios/RAI/App/V4SyncEngine.swift`（HealthKit 样本/series/route 读取、coverage、D2 diff）。`SyncCoordinator.syncV4` 复用共享上传/ACK 路径，仅在 ACK 成功后推进 sidecar；`ClientConfiguration` 增加 `syncProtocolV4`/`RAI_SYNC_V4`，`SyncSession` 允许 handshake 3/4，`RAIAPIClient` 支持自定义 header。
- 测试证据：`go test ./...` PASS；`swift test` 250 tests / 35 suites PASS（含 `WorkoutBundleV4Tests` 5 tests）；`xcodebuild` v4 AppTests `V4SyncEngineTests` 2 tests PASS；真实隔离 PG `TestWorkoutBundleV4ImportAndIdempotency` PASS（receipt committed、samples/associations=1、重复字节幂等）；`swift build --target SyncCore` 与 Simulator build SUCCEEDED。
- 门禁：`go test -race ./...` PASS；`go vet ./...` PASS；`scripts/check-boundaries.py` 全部 PASS；Swift 250 tests / 35 suites、v4 App tests 2 tests PASS。未验证：真机 HealthKit 读取/上传、P1–P8 性能门槛、完整 Simulator E2E/UI；v4 只在本机合成数据验证，不能替代真机/部署验收。

### 审查修复 — P0/P1 与关键 P2（2026-11-08）

- P0-1：修复 `bundle_integrity.go` 对 v3 payload 无条件按 v4 解码的回归；先解 `protocolVersion`，仅 v4 解 `v4Payload`。新增 `TestV3RouteAnalysisProvenanceRegression`（先补 `phase3Observation` 再导入），真实集成 PASS；此前 full 的 14 个 `analysis.workout/analysis_invalid_input` 失败随 root cause 消失。
- P0-2/SYNC-001..005：iOS v4 空读取不再生成 sample/route deletion；empty-with-previous 标记 `empty_read_not_deletion` 并保留 sidecar identity；partial route 保留 previous identity；prepared v4 bundle 在服务端关闭 v4 时丢弃并重建 v3。服务端新增 `validateBundleDeletionEvidence`（complete coverage + freshness/stale observation），v4 source 必须通过 Apple Watch classification，且 workout source 必须出现在 `sources`。
- SYNC-006/007/008：v4 拒绝同 type 重复 stream；v4 `observationBytes` 不再拼接 v3 sample metadata，避免同内容重传恒 changed；partial route sidecar 合并 previous identity。
- DB-001：00030/00032 补齐 `vo2max_source_selection` kind/schema；`TestUpgrade29PreservesVo2MaxSourceSelectionInput` PASS。
- DB-002：新增 schema 46 `analysis_published_results` 只读视图（00045）与 `rai_training_dirty` SECURITY DEFINER 收紧（00046）；API 查询改用该视图，`rai_api` 不再有 `analysis_results` 全表 SELECT；`TestAPIRoleCannotMigrateOrMaintain` PASS。
- DB-003/004/009：回滚使用 previous 自己的 manifest/config；`readBackfillSnapshot` 只取六个产品 artifact，并校验 succeeded job、generation、publication/clean generation。
- MODEL-001/002/003/004/007：HR<Rest 负强度 invalid；Recovery v2 补 coverage/tombstone 门；backfill affected 闭包补 readiness/VO2/ability；backfill 研究 artifact 不再占用产品槽；验收文字改为 `validationStatus=unvalidated`（RAIVO2Max=experimental）。
- 验证：`go test -race -count=1 ./...` PASS；`go vet ./...` PASS；`scripts/check-boundaries.py` PASS；`scripts/local/test.sh gate` PASS（22 s）；`scripts/local/test.sh integration ./integration`（`-race`，797 s）PASS；`scripts/local/test.sh full` PASS（947 s，exit 0，666 PASS/22 SKIP/0 FAIL，leftover DB=0、cluster removed，日志 `/tmp/rai-full-fix3.log`）；`swift test` 250 tests / 35 suites PASS；Simulator build SUCCEEDED；v4 App tests 2 tests PASS（含空读取不删除回归）。
- 未验证：真机 HealthKit 权限撤销/上传、真实设备验收、P2/P3 余项（如 DB-005 channel guard、DB-008 down 链、部分模型边界/展示）仍按原报告保留。

### 跑力 UI（RQrun 风格）— 2026-11-08

- 新增 `ios/RAI/Sources/ProductCore/RunningAbilityDerived.swift`：由 `RunningAbilityValue.v60` 推导 E/M/T/I/R 五区配速、Riegel 5K/10K/半马/全马预测、耐力保持百分比归一；纯函数，无 I/O。
- 新增 `ios/RAI/Tests/ProductCoreTests/RunningAbilityDerivedTests.swift`：5 个 Swift Testing 用例（V60 配速、区间边界、Riegel 单调性、retention 0–1/百分数归一）。
- `ios/RAI/App/ProductViews.swift`：分析页新增「跑力」入口卡；`RunningAbilityDetailView` 展示跑力 hero、能力分解、配速区间、成绩预测、证据与限制；未连接/未激活分别显示空态；`AthleteHistoryView` 支持初始 metric。
- `ios/RAI/App/RAIApp.swift`：根视图固定 `.preferredColorScheme(.dark)`，修正导航标题/卡片在浅色系统下的对比度。
- 验证：`swift test` 255 tests / 36 suites PASS；`xcodebuild -project ios/RAI/RAI.xcodeproj -scheme RAI -configuration Debug -destination 'generic/platform=iOS Simulator' ... build` BUILD SUCCEEDED；模拟器签名构建 + 本地 `rai_dev` API 连接成功，分析页入口与详情页可导航（截图 `/tmp/rai-analysis-uitest.png`、`/tmp/rai-running-detail.png`）。
- 限制：`GET /v2/analysis/athlete` 在当前数据集返回 409 `not_product_activated`（active=legacy；M7 产品激活仍为 NOT AUTHORIZED），跑力数值/区间/预测暂无真实数据，页面显示「待激活」。本轮未修改后端合同、未激活 candidate、未执行真机验收。（2026-11-08 更新：该限制描述的是当时本机真实数据集；独立合成数据集的模拟器验收已激活 candidate 并验证跑力/模型卡/研究诊断，见下文「V2 产品页上线」。真实数据集仍未激活。）

### 训练负荷页（legacy v2）— 2026-11-08

- 新增 `ios/RAI/Sources/ProductCore/TrainingLoadSeries.swift`：把 `/v2/analysis/history` 的 `fitness`、`fatigue`、`balance`、`daily_load` 行按日合并为 `TrainingLoadPoint`，缺失日不补零，未知 metric 忽略。
- `ios/RAI/App/ProductViews.swift`：分析页新增「训练负荷」入口卡；`TrainingLoadDetailView` 展示当前体能/疲劳/平衡、体能-疲劳趋势、近 7 天负荷柱状图与解释；数据来自现行 legacy `training_state.bundle/v2`，不依赖 candidate artifact。
- 新增 `ios/RAI/Tests/ProductCoreTests/TrainingLoadSeriesTests.swift`：3 个 Swift Testing 用例（按日合并/排序、跳过空行、拒绝畸形行）。
- 验证：`swift test` 258 tests / 37 suites PASS；`xcodebuild ... build` BUILD SUCCEEDED。当前本地数据 `/v2/analysis/history` 可返回 2026-10-30 的 10 km 跑步和 2026-11-08 的 fitness/fatigue/balance 行。
- 训练计划页已有创建/编辑/执行流程；当前数据集 `/v2/training/plans/current` 返回 `training_not_found`，因此显示创建计划空态。

### V2 产品页上线 — 跑力、模型卡与研究诊断（2026-11-08，合成模拟器验收）

本轮把 V2 六槽模型的已实现能力接到产品页，并在独立合成数据集上完成 App→HTTP→PG18→Worker→promotion→rollback 的模拟器验收；仅合成数据，不代表真实数据集激活、部署或旧入口切换。

- 跑力/模型卡：合成 candidate 激活后 `GET /v2/analysis/athlete` 返回 `rai.athlete_model/2`，跑力 `available`（7 次有效跑步，score 非空）；模拟器 UI 验证分析页入口、跑力详情（评分、E/M/T/I/R 区间、5K–全马预测）与 `model.card` 均可见，并保留历史入口。
- 新增研究诊断只读 API `GET /v2/analysis/athlete/diagnostics`（read lane）：200 返回 `rai.athlete_diagnostics/1`，包含 5 个研究项 `running_ability_maximal_v2`、`workout_durability`、`aerobic_efficiency`、`critical_speed`、`load_settling`；active=legacy 时 409 `not_product_activated`，带查询参数时 422 `invalid_athlete_query`，其他读取失败 503 `athlete_diagnostics_unavailable`。投影只含展示标量、support/status/reasonCodes，不含 evidence、source epoch 或原始样本。
- 读取实现 `internal/storage/postgres/athlete_diagnostics.go`：15 s 只读 REPEATABLE READ；校验 active assignment 与 manifest hash；按 day scope 前缀取 `p.day<=当天` 的最新发布，要求 SUCCEEDED run 且 clean generation；逐 key 校验 identity/schema/algorithm/version/content hash。`analysis_artifacts.payload` 是 jsonb，直接对 result 原始字节哈希会因对象 key 重排而不匹配，因此解码为 typed envelope 后重新哈希；无 result 的 unavailable/collecting envelope 正常返回空值项。maximal 复用 `rai.running_ability/2` 与 `pf.Hash`。
- 00048 新增独立 security-barrier 视图 `analysis_published_research_artifacts`（active 非 legacy stack、SUCCEEDED run、clean generation、匹配 publication、5 个研究 key），并向 `rai_api`/`rai_worker` 授予只读权限；未放宽六槽 `analysis_published_artifacts` 的行集，现有产品视图行数测试不受影响。`RequiredSchemaVersion=48`，`migrations/checksums.json` 已更新，`deploy/postgres/roles.sql` 同步授权。
- iOS：新增 `AthleteDiagnostics` 解码模型与 `ProductAPI.athleteDiagnostics()`；`ProductModel.loadAthleteDiagnostics()` 校验 assignment 一致，409 与普通失败分别显示未激活/暂不可用；跑力详情新增「研究诊断」区，逐项展示状态、摘要与原因，行标识为 `runningAbility.diagnostic.<key>`。模拟器验收发现 SwiftUI 卡片级 `accessibilityIdentifier` 会传播并覆盖子行标识，已将卡片标识移到标题，行标识保持独立。
- 外部 VO₂ 来源选择公开为追加式输入：00047 扩展 `rai_commit_typed_model_input`，新增 kind `vo2max_source_selection` 与 schema `rai.vo2max.source_selection/1`，payload 仅允许 `sourceEpoch`（`^sha256:[0-9a-f]{64}$`）和 `revoked` bool，上限 128 条；写入时提升 dirty scope 并聚合同日分析 job。`POST /v2/model-inputs/vo2max-source-selections` 走既有 receipt 路径，iOS 个人输入表单新增「VO₂ 来源」类型。`TestVO2SourceSelectionPublicInput` 真实隔离 PG PASS：首次 receipt、字节重放返回原回执、非法 epoch 422、库中仅 1 条。
- Readiness v2 消费已验证：`TestTrainingContextUsesV2ReadinessSignal`、`TestTrainingContextIgnoresV2ReadinessOnLegacyAdapter`、`TestTrainingContextDoesNotUseExpiredV2Readiness` 真实隔离 PG PASS（2.738 s）；v2 信号移除 `readiness_unavailable` 并给出 `load_only_advisory`，训练日程不变，legacy adapter 与过期信号均不消费。
- 证据（本机 2026-11-08）：`scripts/local/test.sh e2e` PASS（110 s，exit 0；`TestWorkoutCompletenessSimulatorHTTPPostgres` 14.06 s、`TestModelStackSimulatorHTTPPostgres` 77.48 s；模型用例输出 `active_home=true workout_detail=true history=true running_ability=true diagnostics=true rollback=true`；leftover DB=0、cluster removed）。`scripts/local/test.sh gate` PASS（16 s）；`scripts/local/test.sh swift` PASS（258 tests / 37 suites）；研究诊断 jsonb 重排、无 result、content hash 不匹配、maximal typed hash 单测 PASS；定向 PG `TestVO2SourceSelectionPublicInput`、`TestAPIRoleCannotMigrateOrMaintain` PASS。日志归档 `.local/logs/model-product-activation-2026-11-08/`（e2e、模型 UI xcode 输出与 summary、readiness、VO₂ 输入、gate、swift）。
- 未验证/未授权：真实数据集激活、回算、rollback executor 与入口切换均未执行；shadow 生产候选读取/调度、真实校准/oracle 仍未实现，RAI VO₂ 保持 experimental；本轮仅模拟器验收，未跑真机；`full` 全量集成回归未在本轮重跑。合成 `running_ability_maximal_v2` 研究项为 collecting（`no_qualified_maximal_effort`），e2e 只断言其展示与身份，不声称数值可用；真实数据集仍为 active=legacy，产品 API 仍会返回 409。

### 云端部署 — rai-coach-20261108（2026-11-08）

- 目标：`lh-gz` 的 `/opt/rai-coach-20261108`，Compose project `rai-coach-20261108`，公网入口 `https://www.tpandmr.xyz`；未改动其他云上项目或旧入口。
- 源码 commit `9b9c7e2`；镜像 `rai-coach:20261108-9b9c7e2`（本地 Go1.27.3 交叉编译 linux/amd64 静态二进制，SHA-256 `baca047a0c7e852a863013706b5765db2568e93b97ff5ddc32c1db16095eeb7b`，`rai version` 报告 schema 48 / source_commit 9b9c7e2）。
- 迁移：迁移前用 `pg_dump -Fc` 备份到 `/opt/rai-coach-20261108/backup-before-schema48-20261108-221605.dump`（5.5 MiB，605 个 TOC 项，容器内 `pg_restore -l` 校验通过）；`admin migrate` 显式应用 00047、00048，`migrate-status` 共 48 份且最后三份 applied；以 owner 查询确认 `analysis_published_research_artifacts` 对 `rai_api`、`rai_worker` 均为 SELECT。
- 运行态：API/Worker 已用新镜像重建并 healthy；外部 `GET /ready` 返回 `{"status":"ready"}`；`GET /v2/analysis/athlete/diagnostics` 未带凭证返回 401，使用已配置设备令牌返回 409 `not_product_activated`（真实数据集仍为 active=legacy，符合预期）。API/Worker 启动日志无 schema 或角色错误。
- 本轮按用户要求未运行任何 Go/Swift/集成测试；上述仅为部署与健康检查证据，不构成测试、真机或产品激活验收。真实数据集激活、candidate promotion、回算与回滚仍未执行。（2026-11-09 更新：该数据集已按用户明确授权、通过独立 owner 授权路径激活；见下节。该操作不改变本段的 2026-11-08 时点结论。）

### M4 自有条件式 RAI VO₂max 实施与合成验收（2026-11-08）

本轮补齐原设计 M4 `rai.vo2max.submax/v1` 的数值发布 oracle，并用本机隔离 PostgreSQL 18.6 上的合成数据完成真实 PG 验收；只使用合成 Workout/HR reference，未读取生产数据库、个人健康值或凭证，未部署、未切换入口、未迁移真实数据，未修改 `../running`。

- 纯领域：`apps/server/internal/vo2max` 已实现 `r=(HR−HRrest)/(HRmax−HRrest)`、`V=3.5+0.2·v/r`、25 分钟下限/前 10 分钟排除/5 分钟窗口、HR 与速度共同支持、r∈[0.55,0.85]、v∈[134,300] m/min、CV/trend/drift/grade/pause/distance/温湿度门槛、42 日池、water-fill、MAD floor、winsorization、异常隔离确认、h−6…h 平滑、8 敏感性角点、真实 LOO 与 14/28/42 日 aging。新增 `TestAggregatePublishesValueWhenSensitivityNarrow`：同一合成证据中心得到可发布值 35.22244897959183，8 角点相对宽度 <0.30，LOO 5 折有范围；既有 abstention fixture 仍保持 candidate-only。
- 日栈输入：`storage/postgres/model_stack_inputs.go` 仅使用 `rai.hr_reference/2` 的 `maxHR/restingHR/measuredAt/restingMeasuredAt`，`workoutReference` 区分 `measuredAt` 与 `created_at`，HRmax 365 日/RHR 90 日、source epoch、conflict fail-closed；`stackVO2Evidence` 在 76 日窗口内读取 sealed HR 系列、速度与路线 grade，并要求 Apple Watch provenance。日栈输出 `vo2max_submax` 产品 artifact，family 为 `model_product_v2/vo2max_submax`。
- 产品与管理端：`ReadAthleteProduct` 读取 `raiVO2Max` 独立槽位，`ProjectRAIVO2` 要求 `experimental=true`、非 candidate-only 且敏感性宽度 ≤0.30，输出 `source=rai_owned_submax`、`evidenceSupport=experimental_candidate`、`validationStatus=experimental`；历史 API 支持 `raiVO2Max`。`model-stack inspect` 现在识别 managed candidate 通道，从基表读取 clean 日栈发布（产品视图仍只暴露 active 通道），默认摘要新增 `raiVO2Max` 信封且不含数值。
- 合成验收（`integration/vo2max_native_test.go` 的 `TestNativeRAIVO2PublishesNumericSyntheticValue`）：7 个 35 分钟户外合成 Workout，速度 134 m/min、HR 182，间隔 4 天结束于昨日；`/2` reference 为 maxHR 209（测于首个 Workout 前 60 日）、restHR 35（前 40 日）、known-at 前 39 日；每个 Workout 的 `first_received_at` 回填为 end+1 分钟以满足历史 cutoff 的 known-at，dataset 标记 `synthetic_test=true`。`PrepareModelStack` + `ProcessModelStack` 后断言：artifact schema/model/version/family 正确，`status=established`，值 35.22244897959183，`candidateOnly=false`，8 角点且宽度 ≤0.30，LOO 7 折有范围，`experimental=true`，单位 `mL/kg/min`；promotion 后产品槽位 `EvidenceCount=7`、历史点有值、HTTP `/v2/analysis/athlete` 返回 `raiVO2Max.value.source=rai_owned_submax`，外部 `externalVO2Max` 槽位保持 unavailable 且不被平均/覆盖；管理端 `--synthetic-test-values` 预览可见同一 RAI 值，默认摘要无 `"value"`。
- 实跑证据：`go test ./internal/admin/... ./internal/vo2max/...` PASS；`scripts/local/test.sh integration -run '^TestNativeRAIVO2PublishesNumericSyntheticValue$' -v ./integration` PASS（2026-11-08 复跑：test 4.18s，总 5.23s，`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`，日志 `.local/logs/model-product-activation-2026-11-08/rai-vo2-m4-integration.log`）；回归 `TestAthleteModelFullAndLeakage`、`TestSyntheticPromotionAndRollbackAudits`、`TestManagedModelStackPublishesWorkoutDetailAndWithdrawsOnDelete` PASS；`TestActiveSchema2ProjectsAllSixTypedMetrics`、`TestSchema2ReaderRejectsTamperedArtifactAndDirtyScope`、`TestProductReadPublicationPaginationAndIsolation` PASS；`scripts/local/test.sh gate` PASS（本轮末复跑 3s，Go race/vet/依赖边界/控制器安全检查；日志 `.local/logs/model-product-activation-2026-11-08/gate-m4.log`）。本轮未改 migration，未跑 Swift/Simulator/真机/full 全量集成回归。
- 未验证/未授权：真实 criterion VO₂max 校准（MAE、bias、Bland–Altman、独立 athlete/time 留出）未执行；合成 oracle 只证明工程发布链与数值身份，不证明生理效度；真实数据集激活、真机 HR reference、天气覆盖和产品入口切换仍未执行。RAI VO₂max 始终标记 experimental，不对外称“准确率”。

### 真实数据集 owner 授权激活 — rai-coach-20261108（2026-11-09）

本轮在用户对云端真实数据集 `6e6775e4-f9cd-402b-a184-34a75ac74db2` 的明确授权下，通过独立 owner 授权路径激活 managed athlete model stack。该路径不伪造合成验收门禁：审计明确 `ownerAuthorized=true`、`standardGatesVerified=false`。本轮只激活该数据集的产品读取，不构成 UA4、真机或模型有效性验收。

- 实现：新增 `internal/athletemodel/owner_activation.go`，schema 为 `rai.owner_activation_authorization/1`，scope 为 `real_dataset_product_activation`，绑定 datasetID/stackID/manifestSHA256，要求 authorizedBy、RFC3339 authorizedAt/expiresAt 和固定 statement；过期、错绑或 schema 不符返回 `owner_activation_authorization_invalid`。store 新增 `PrepareModelStackAuthorized`、`PromoteModelStackAuthorized`、`RollbackModelStackAuthorized`，均在 dataset 行锁内重新校验授权文件并哈希原始字节；原有 synthetic-only `PrepareModelStack`/`PromoteModelStack`/`RollbackModelStack` 不变，真实数据集仍返回 `ErrGateClosed`。owner promotion 审计只声明 owner 授权与未验证标准门禁，不声明 synthetic gate 通过。admin CLI 新增只读 `analysis model-stack identity`，以及 `prepare-real`、`activate-real --confirm-phrase ACTIVATE-REAL-DATASET`、`rollback-real --confirm-phrase ROLLBACK-REAL-DATASET`；授权文件必须是 0600 常规文件、1–64 KiB，错误不泄漏数据库细节。
- 本机测试：`scripts/local/test.sh gate` PASS（66 s；Go race/vet/依赖边界/controller 安全检查）。受影响真实隔离 PostgreSQL 18.6 回归全部 PASS：`TestOwnerAuthorizedRealDatasetActivationAndRollback` 2.44 s、`TestSyntheticPromotionAndRollbackAudits` 2.12 s、`TestManagedModelStackPublishesWorkoutDetailAndWithdrawsOnDelete` 1.96 s、`TestNativeRAIVO2PublishesNumericSyntheticValue` 7.29 s；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`，profile 总计 31 s。新测试覆盖未授权 prepare/promote/rollback 拒绝、错绑授权拒绝、授权 prepare 调度并处理 `analysis.model_stack_v2`、promotion 前产品 409→后 200、owner rollback 后恢复 409、审计恰两行且标记标准门禁未验证。
- 部署：源码 `d7f0eb5` 加未提交 owner 授权改动（工作树 dirty）；Go1.27.3 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` 构建，二进制 SHA-256 `9716fa00eca50687c0614b8c36d1c13662644286788676437be1a7224252447f`（16,908,448 B），镜像 `rai-coach:20261109-owner-activation`（ID `0c01f7fe7bbb`），`rai version` 报告 schema 48、`source_commit=d7f0eb5-owner-activation-dirty`。变更前逻辑备份 `/opt/rai-coach-20261108/backup-before-owner-activation-20261109-012937.dump`（5,709,473 B，617 TOC 项，容器内 `pg_restore -l` 校验通过）；保留 `rai.bak-9b9c7e2` 与 `environment.bak-9b9c7e2`。api/worker 重建为 healthy；`GET /ready` 返回 `{"status":"ready"}`、`GET /health` 返回 200，15 分钟内 API/Worker 无 ERROR 日志。
- 激活身份：stack `rai-v2-b4618722716ba569`、manifest `b4618722716ba5695161b967436d6c1ff69b14f6b40aae7bf73ad1c6d109e954`。授权文件 556 B、0600、uid 65532、SHA-256 `cfe464306247bf25c5d07bfc32ec7dac623ddeba6b021d4bb5b9df91af3b394c`，authorizedBy `tpandmr`，有效至 2026-11-16T01:30:00Z。`prepare-real` 调度 job `a7494c6d-d489-495c-a439-75ed584a2e2e`，Worker 处理为 `succeeded`；11 个算法 run 全部 `SUCCEEDED`，发布 6 个产品 family（load_state、performance_state_v2、physiological_recovery、training_readiness、vo2max_external_projection、vo2max_submax）与 5 个研究 family。`activate-real` 产生 revision 2 `promote`（legacy → managed stack），审计 `ownerAuthorized=true`、`standardGatesVerified=false`；channel active=managed stack、previous=legacy，model dirty scope=0。
- 产品读取：带既有设备令牌请求公网 `GET /v2/analysis/athlete` 由 409 转为 200，`schemaVersion=rai.athlete_model/2`，assignment 为 `active` / `rai-v2-b4618722716ba569` / version 2。因该真实数据集仅 2 次 Workout 且无 Recovery/Readiness 基线，`responseState=unavailable_response`（没有任何槽位 available，不是技术错误）；`runningAbility.envelope.state=collecting`、`modelStatus=collecting`、`reasonCodes=[no_qualified_performance]`、`productReasonCodes=[not_enough_history]`，`raiVO2Max` 为 collecting。验证过程未打印或记录任何健康数值、令牌或 DSN。
- 回滚：`rollback-real` 本轮未执行（目标为激活）；owner 授权回滚已在隔离 PG 集成测试中验证，云端保留授权文件与该命令，可新增受审计 revision 切回 previous legacy，不依赖 migration down、备份恢复或全量重算。旧镜像、二进制和 environment 备份均保留。
- 未验证：跑力仍为 collecting，未产生真实分数、区间或成绩预测；UA4 正式门禁（真机、数据可用性、模型校准、UX/隐私/性能、shadow、云端 rollback 演练）仍未接受，`standardGatesVerified=false` 是准确状态；未跑 `full` 全量集成回归、Swift/Simulator/真机复验；未观察次日 daily job 调度；备份只做归档列表校验，未做恢复演练；2 次 Workout 不能用于评价模型准确性或生理有效性。

### Workout 分析 v3 — 瞬时采样覆盖、步频代理与路线质量（2026-11-09）

本轮修复“完整同步的 Workout 仍显示部分完成”的分析语义缺陷；只改分析代码与文档，未改 migration、未部署、未重算真实数据。根因：HealthKit 的 HR/Power 常为瞬时点（start=end），v2 的 coverage 只累计 End>Start 的区间，完整数据也得到 0 coverage；路线质量硬编码 partial；HealthKit SDK 无跑步步频 quantity，原生缺失时从未使用 StepCount；瞬时点也不支持 TRIMP 的 coverage 门槛。

- 语义：升级 `workout.bundle/v3` / `workout.features/v3`。瞬时点按到下一点 ≤15 s 的间隔重建支持区间，更宽空隙不外推；同一时刻冲突值保持歧义；区间样本语义不变。无原生步频时以 StepCount 区间计算平均步频，details 标注 `method=step_count_proxy`、`derivedFrom=HKQuantityTypeIdentifierStepCount`；有原生值时不代理。`route_distance` 在路线 coverage ≥0.9 时 available，`elevation_gain/loss` 另需 ≥90% 合法连接有合法海拔，否则 partial；`route_coverage` 始终为自身测量值。`training_load.trimp/v1` 公式未改，缺 personal parameters 时仍 insufficient，不引入人口默认值。
- 兼容：`cmd/rai version` manifest 改用 analysis 常量并报告 v3；`training_state.bundle/v2` 的 Workout 输入接受 features v1/v2/v3；显式 CLI 仅接受当前 v3，旧 v1/v2 run/publication 原样保留，不自动全历史重算。P4 冻结合同新增 v3 addendum 保留 v2 原文；ARCHITECTURE 当前 bundle 行同步为 v3。
- 测试：`go test -count=1 ./internal/analysis/... ./internal/storage/postgres/... ./internal/admin/...` PASS。新增 `TestInstantaneousTraceReconstruction`：5 s HR/2.5 s power 全部 available 且 TRIMP 有值；10 分钟空洞 coverage<0.9 且 load 为空；同刻冲突值均值 insufficient。`TestRealCadencePowerAndSummaryMaximum` 覆盖 StepCount 代理（10000 步/3600 s→166.67 spm）与 method 标注；`TestRouteCanonicalAndQuality` 覆盖稀疏 2 点 partial 与覆盖全程的 361 点 available。`scripts/local/test.sh integration ./integration -run 'TestAnalysis|TestTrainingContextExcludesUnpublishedAndDirtyFeatures|TestTrainingFrozenTimezoneAndLateAnalysisFeedback'` PASS（29.787 s，profile 42 s；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`）。`scripts/local/test.sh gate` PASS（18 s；Go race/vet/依赖边界/controller 安全检查）。
- 未验证/未授权：未构建镜像、未部署、未重算真实数据集，云端仍为 v2 publication（App 仍显示部分完成）；`workout_load` 仍需该 dataset 的显式 resting/max HR、A/B 与 provenance（云端 `analysis_parameter_sets` 为 0，App 的“心率参考”输入进入 model pipeline 而非 legacy TRIMP），本轮未伪造参数；未跑 Swift/Simulator/真机/full 全量集成回归，未新增 migration。

### 个人 TRIMP 心率设置入口（2026-11-09）

本轮为 `training_load.trimp/v1` 增加 App 内个人参数入口。此前只有 owner 管理命令能写 `analysis_parameter_sets`，App 的「心率参考」进入 v2 模型 pipeline，不解除 `workout_load` 的 `personal_parameters` 缺失。本轮只改代码、migration 与文档，未部署、未写真实参数、未重算真实数据。

- Migration 00049：`athlete_model_inputs` 允许 kind `trimp_parameters` / schema `rai.trimp_parameters/1`，payload 必须同时包含 `restingHR`、`maxHR`、`coefficientA`、`coefficientB`；`rai_commit_typed_model_input` 校验 30≤RHR<HRmax<220 与系数 (0,10]，拒绝 through/supersedes/撤销，并在同一事务追加新 `analysis_parameter_sets` 版本、为最近 179 天（最多 1000 条）Workout 重排 `analysis.workout` job。trimp 输入不使 v2 模型栈 dirty，也不改动历史参数版本。`RequiredSchemaVersion=49`，frozen checksum 与 `deploy/postgres/roles.sql` 的既有函数授权保持一致。
- 公共 API：`POST /v2/model-inputs/trimp-parameters` 走既有 typed receipt 路径，receipt 仅对 trimp 追加 `parameterVersion` 与 `workoutCount`；`GET /v2/analysis/parameters`（read lane）返回最新显式参数或 `{"parameters":null}`。`rai_api` 仍无 `analysis_parameter_sets` 写权限。
- iOS：新增 `ProfileView → 个人设置 → 静息与最大心率`（`profile.hr-settings` / `screen.hr-settings`）；页面读取最新参数预填，静息/最大心率 30–219 bpm 且 max>rest，Banister A/B 显式展示并可编辑（界面起点 0.64 / 1.92，明确说明服务端不补默认），保存后追加版本并重算最近跑步。「心率参考」表单补充说明其仅用于能力模型。`ProductModel.submitModelInput` 新增 trimp 路径与 `loadAnalysisParameters()`。
- 证据（本机 2026-11-09）：`TestTRIMPParametersPublicInput` 真实隔离 PostgreSQL 18.6 PASS（test 6.46 s，profile 19 s；验证 API 角色直写参数表被 42501 拒绝、receipt 1/2、参数值 52/188/0.64/1.92、两个 Workout scope 各 generation 1 与 1 个 job、GET 回读、2.1 s 后字节重放仅 1 个版本、max≤rest 返回 422、撤销返回 422；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`）。既有 `TestModelInputHTTPIdempotencyAndLegacyProduct` 与 `TestVO2SourceSelectionPublicInput` 同轮 PASS（27.351 s）。`go test ./internal/modelinput ./internal/httpapi ./internal/analysis` PASS；`TestFrozenMigrations`、`TestRuntimeSchemaGateMatchesMigrations` PASS；`scripts/local/contracts.sh` PASS（redocly 0 errors / 36 既有 warnings）；`scripts/local/test.sh gate` PASS（19 s；Go race/vet/依赖边界/controller 安全检查）；iOS `xcodebuild ... build` PASS（仅编译）。OpenAPI 升至 1.8.0，`cmd/rai version` manifest 同步。
- 未验证/未授权：未构建镜像、未部署，云端仍为 schema 48 / analysis v2，新 endpoint 与 App 设置页尚不可用于云端；未保存任何真实参数、未重算真实 Workout，`workout_load` 在云端仍为 insufficient，需部署后由用户显式填写；A/B 只是常用起点，未做个人生理校准验证；未跑 Simulator UI/真机/full 全量集成回归，页面仅通过编译。

### 云端部署 — rai-coach-20261108（2026-11-09，analysis v3 / schema 49）

用户明确要求不运行测试并部署。本轮把已提交的 analysis v3、owner 激活与 TRIMP 参数入口部署到云端；部署提交为 `44136ac`（工作区另有 Xcode 自动改写的 `RAI.xcscheme`，与功能无关，未纳入提交）。

- 构建：Go1.27.3 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`，二进制 SHA-256 `f84a34eabb13891d5138b00f4e3d7241c2670981ed2f3406080b51927993fa2e`（16,941,216 B）；`rai version` 报告 schema 49、`source_commit=44136ac`、OpenAPI 1.8.0、analysis `workout.bundle/v3`。镜像 `rai-coach:20261109-44136ac`（ID `sha256:101c91944e71441f88a962127cdada17cdb4f4e0dc02bfbaa6ee9591b385da1a`），仍由 scratch 构建。
- 迁移：变更前 `pg_dump -Fc` 备份到 `/opt/rai-coach-20261108/backup-before-schema49-20261109-105130.dump`（8,793,596 B，617 TOC 项，容器内 `pg_restore -l` 校验通过）；保留 `rai.bak-owner-activation` 与 `environment.bak-owner-activation`。`admin migrate` 显式应用 `00049_public_trimp_parameters.sql`（applied 2026-11-09T02:51:39Z），`migrate-status` 共 49 份且全部 applied。
- 运行态：api/worker 用新镜像重建并 healthy；容器内 `probe --ready` 与 `worker-ready` 通过；公网 `GET /ready` 返回 `{"status":"ready"}`、`GET /health` 返回 200；`GET /v2/analysis/parameters` 未带凭证返回 401 `unauthorized`；API/Worker 近 5 分钟日志无 schema、角色或 ERROR 条目。既有 v1/v2 分析发布保留，新同步 Workout 才以 v3 计算。
- 未验证/未授权：按用户要求未运行任何 Go/Swift/集成测试，上述仅为构建、迁移与健康检查证据；未写入真实 TRIMP 参数，`workout_load` 在云端仍为 insufficient，需用户在 App 内显式填写；未重算既有真实 Workout，未验证 v3 对真实数据的展示效果；未跑 App/真机验收，部署不代表模型、真机或产品验收。

### Workout Sync v4 原始载荷重写（2026-11-09）

本轮按用户决定把 v4 从“列式可审计投影”原地重写为“原始记录上传”：客户端提交 HealthKit 原始 Workout JSON，服务端只解释已知字段，未知字段保留在 `workout_imports.payload` 的精确字节中，不做删除同步、不做回滚、不迁移旧 v4 数据（v4 此前从未部署）。v3 上传/删除/observation 语义不变。

- 线格式：`protocolVersion=4` + 四个身份 UUID + `startUs/endUs` + 顶层 `source`/`metadata`/`workout` + `streams[]`（每样本对象含内联 `source`/`metadata`）+ `series[]`（`type`/`sampleUUID`/`points`）+ `routes[]`（`routeID`/内联 `source`/8 元组点）。移除 `sources` 表、`kind`、`uuids`/`src` 平行数组、`metadataRegistryVersion`、`observation`/`coverage`、`deletions`/`routeDeletions`。未知字段合法，原始字节不改写。
- 服务端：重写 `internal/syncing/workout_bundle_v4.go`；严格 UTF-8/重复键/包身份/Workout 核心/顶层来源策略（非 `apple_watch` → 422 `source_policy_excluded`），宽容跳过未知流、坏样本、坏点、不合格子来源；重复流/样本保留第一个；样本 50,000、series 50,000、series 点 1,000,000、route 16、route 点 1,000,000 超限返回 413 `bundle_too_large`。新增 `BundleSourceTable` 与 `WorkoutBundleV4SourceTable(payload)`，回执分类、分析和 feature 输入从内联来源还原逐 fact 来源。修复 Workout 核心缺失时返回非 `*Error`（HTTP 层会当作 500）的问题，现统一 422。`SYNC_V4_ENABLED` 已移除，`X-RAI-Sync-Protocol: 4` 直接协商 v4。
- 导入事务不变：原始字节、规范化事实、receipt、change event、dirty scope 与 analysis job 仍同事务提交；同字节重试返回原回执；v4 的 `Observation`、`Complete*`、删除列表恒为空，旧完整性端点不参与 v4。
- iOS：重写 `WorkoutBundleV4.swift`（原始 envelope、逐样本内联来源）与 `V4SyncEngine`（逐类型读取、失败流只标记、无 coverage/sidecar/删除）；`SyncCoordinator` 的 v4 路径只保存 multipart、上传、ACK；删除 `pendingDelete`、sidecar 提升、`deliverDeletedParentChildren` 与自动完整性流程；手动多 Workout 同步按 3 条有界并发，每个 Workout 独立准备/上传/ACK，失败互不影响；`ClientConfiguration.v4Enabled` 默认 true，`syncProtocolV4=false` 可回退 v3。
- 测试（本机 2026-11-09）：`go test -count=1 ./internal/syncing/` PASS；v4 定向单测覆盖原始 envelope、实际 Swift JSON 形状、未知/重复字段、严格身份、宽容跳过、category、route 八元组、metadata 保留与 VO₂ 保留键剥离、413 上限、第三方 Workout 来源 422。真实隔离 PostgreSQL 18.6：`TestWorkoutBundleV4ImportAndIdempotency` PASS（2.208 s，profile 12 s；samples/associations=1，同字节重试返回原回执；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`）。`scripts/local/test.sh gate` PASS（27 s；Go race/vet/依赖边界/controller 安全检查）。Swift：`WorkoutBundleV4Tests` 6/6 PASS、SyncCore 全量 PASS；带签名的模拟器 `RAITests` 94 passed / 0 failed / 4 skipped；Simulator 与 iPhoneOS 构建 PASS。
- 未验证/未授权：未执行真机 HealthKit 上传与真实 Watch 跑步验收；未跑 v4 性能基准；未部署本轮改动，云端仍运行旧版；未跑 `full` 全量集成回归。文档同步：`contracts/sync-v4.md` 全文重写，`contracts/CHANGELOG.md`、`docs/WORKOUT_SYNC_V4_RESULT.md`、本文件与 `AGENTS.md` 更新；保留 `docs/WORKOUT_SYNC_V4_RAW_PLAN.md` 与 `docs/WORKOUT_SYNC_V4_SIMPLE_PLAN.md` 原文。

### 本机合成数据调试环境与 iPhone 12 Pro Max 模拟器（2026-11-09，仅本机）

本轮为用户交互调试建立隔离环境：本机 PostgreSQL 18.6 的 `rai_synthetic_debug`（schema 49，空库全量迁移）、本机 API/Worker、iPhone 12 Pro Max 模拟器 App。全程只用合成数据，未读取生产库、未写入真实健康值、未部署、未改动 `rai_dev` 或云端数据。

- 种子工具：新增 build tag `integration && syntheticseed` 的 `TestSyntheticLocalSeed`（`apps/server/integration/synthetic_local_seed_test.go`），要求空库、`RAI_SEED_DATABASE_URL` 与 `RAI_SEED_CONFIG_OUT`。它创建合成 dataset、7 个 m4 合成跑步（22/18/15/11/8/5/2 天前，1800–2400 s）、完整性 v2、29 天睡眠/HRV/RHR、`rai.hr_reference/2`、外部 VO₂ fact+selection、managed model stack prepare/process/promote（`SyntheticDataset=true` 门禁）与训练计划，并以 0600 写出本机 App 配置（含仅本机设备令牌，未提交）。测试 fixture 修正两处：导入前标记完整 coverage/`CompleteRoutes`；`m4Bundle` 路线距离随 134 m/min 速度缩放，避免 `distance_mismatch`/`speed_gap`。
- 种子证据（本机 2026-11-09）：`rai_synthetic_debug` 全量迁移 49 份；seed PASS 2.11 s；`GET /v2/analysis/athlete` 返回 200、`schemaVersion=rai.athlete_model/2`、`responseState=complete_response`，六项全部 available：trainingLoad、physiologicalRecovery（100.0）、trainingReadiness（81.5）、runningAbility（V60 28.7）、raiVO2Max（35.22，sensitivity width <.30）、externalVO2Max（45.2，`external_fact_selected`）。诊断端点 200、7 个 Workout、训练计划 effective 均经真实 HTTP 校验。
- 模拟器证据：Debug 构建（`RAI_DEV_HTTP_HOST=127.0.0.1`，ad-hoc 签名）安装到 iPhone 12 Pro Max（UDID `F4793F8A-4A9A-4C88-903E-99698BD773B0`），以 `RAI_SYNTHETIC_ACCEPTANCE=1` 和内联 `RAI_ACCEPTANCE_CONFIG` 启动；首页显示今日恢复跑与准备度/跑力，分析页可见准备度 81、跑力 29、生理恢复 100、急性负荷 18 AU、RAI VO₂ 35.2 mL/kg/min、Apple 外部 VO₂ 45.2 mL/kg/min。
- 回归：`TestNativeRAIVO2PublishesNumericSyntheticValue` 真实隔离 PG PASS（9.67 s，profile 32 s；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`），确认 m4 路线修正未改变 35.22244897959183 结果。
- 未验证/未授权：未跑 `gate`/`full` 全量回归与 Swift 全量测试；模拟器 UI 为人工截图核对，未跑 UI XCTest；外部 VO₂ 为单条合成事实，不代表 HealthKit 真机链路；本环境不是 UA4、真机、模型有效性或部署验收。

### 训练详情分段来源修复（2026-11-09）

本轮修复用户反馈的“训练详情分段信息不对”。排查确认界面分段不是 HealthKit 原始对象：边界来自 activities/lap/segment 事件，距离、配速、心率、步频由服务端读时计算，标签与目标来自匹配执行快照。既有真机脱敏结构证据 `docs/evidence/device-workout-structure-2026-10-30.json` 显示同一 Workout 顶层与唯一子活动各有 8 个 segment=7 事件，时长合计 3802.668 s，而 Workout 仅 1903.865 s；其中两组互不重叠的事件分别合计 1901.334 s 与 1901.335 s，说明真实设备会同时提供多套各自覆盖整场训练的分段层级。旧实现把所有事件的起止端点并集后两两切段，会把重叠层级拼成不存在的分段；同时 `flattenPlanSteps` 漏掉重复组最后一次恢复，课表步数与实测活动段数错位时仍按位置套标签，导致“整理放松”等标签落到错误的恢复段。

- 分段选择：`apps/server/internal/workoutdetail/calc.go` 改为“真实连续 activities > lap=3 计圈事件 > 单一不重叠 segment=7 分区 > 空（iOS 回退公里分段）”。重叠的 segment 层级不再取端点并集；只有匹配执行快照的步数与某一套分区完全一致且唯一时才采用该分区，否则返回空分段与原因说明。暂停/恢复请求仍不参与分段。
- 标签：计划只提供标签与目标，不生成边界；`flattenPlanSteps` 按训练模型的总时长口径在每次重复后（含最后一次）补恢复段。只有来源为 activities 或唯一 segment 分区、且计划步数恰好等于实测段数时才套用标签；不一致时保留通用“第 N 段”并写明“计划 N 步，实测 M 段，未套用计划标签”。lap 计圈保持通用标签，不把人工计圈当作课表步骤。
- 事件读取：`apps/server/internal/storage/postgres/workout_detail.go` 的 `measured()` 合并 activity 内嵌事件与顶层 `workout_events`，按 `(type,startUs,endUs)` 去重，避免多活动 Workout 丢失父 Workout 挂在子活动上的事件，也不重复计数。
- iOS：`ios/RAI/App/WorkoutDetailView.swift` 标题改为 `segmentPlan != nil` 才显示“课表分段”，否则按 activities 显示“实测活动分段”或“手表事件分段”；无速度轨迹时服务端在 `segmentNote` 说明“缺少可用速度轨迹，分段距离与配速不可用”，iOS 单段距离显示 `--` 而非 `0.00 公里`；分段为空回退公里分段时显示 `segmentNote`，让“多套重叠事件无法确定唯一分段”等回退原因可见。
- 合成回归：新增重叠 3 段/5 段层级（无计划必须返回空并说明重叠；计划 3 步必须唯一选中 3 段层级并套标签）、非重叠 lap 优先于 segment、重复组末尾恢复展开与 8 段活动对齐、计划 2 步/实测 3 段拒绝部分套标签、嵌套 activity 事件合并去重等测试；既有事件回退测试同步改为“lap 通用标签、唯一 segment 分区可对齐计划”。
- 本机证据（2026-11-09）：`scripts/local/test.sh unit ./...` PASS（9 s）；`scripts/local/test.sh gate` PASS（38 s；Go race/vet/依赖边界/controller 安全检查）；真实隔离 PostgreSQL 18.6 `scripts/local/test.sh integration ./integration -run '^TestWorkoutDetail'` PASS（profile 14 s；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`）；`scripts/local/test.sh swift` PASS（257 tests / 37 suites）；Debug `generic/platform=iOS` `CODE_SIGNING_ALLOWED=NO` 构建 PASS（产物 `com.rai.v2.ios`，MinimumOSVersion 18.0）；`scripts/local/test.sh simulator` PASS（98 tests：95 passed / 0 failed / 3 skipped）。
- 未验证/未授权：未用真实 iPhone/HealthKit 数据重放修复后的详情页，未确认真实设备两套 segment 层级的 start/end 是否严格满足唯一分区对齐（不满足时按设计回退公里分段）；未运行带真实 API 的 WorkoutDetail UI XCTest、未截图复核新标题/`--`/回退说明；未跑 `full` 全量集成回归；未构建镜像、未部署、未读取或改写生产数据；本次无 migration 与事务写入变更。

### 云端部署 — rai-coach-20261108（2026-11-09，训练详情分段修复 + v4 原始载荷）

用户要求不运行测试并部署。本轮把提交 `afb8680` 部署到云端；该提交首次在云端包含 v4 原始载荷重写（`d279070`）与训练详情分段来源修复，无新 migration，schema 保持 49。

- 构建：Go1.27.3 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`，二进制 SHA-256 `cd7773c740d00a89fcc7ceedbaa89ff5722a1b92a9bc946b2782c4602f761528`（16,949,408 B）；`rai version` 报告 schema 49、`source_commit=afb8680`、OpenAPI 1.8.0、analysis `workout.bundle/v3`。镜像 `rai-coach:20261109-afb8680`（ID `sha256:5c94e9cc0669203730f4d7045123b2700d1e9403c809698b6a045dc295a80fb5`）。
- 迁移与备份：部署前 `pg_dump -Fc` 备份到 `/opt/rai-coach-20261108/backup-before-segment-deploy-20261109-155138.dump`（10,151,832 B，617 TOC 项，容器内 `pg_restore -l` 校验通过）；保留 `rai.bak-44136ac` 与 `environment.bak-44136ac`。`admin migrate` 无待应用迁移，`migrate-status` 49 份全部 applied（最后 `00049_public_trimp_parameters.sql`）。
- 运行态：api/worker 用新镜像重建并 healthy；容器内 `probe --ready`、`worker-ready` 通过；公网 `GET /ready` 返回 `{"status":"ready"}`、`GET /health` 200；未认证 `GET /v2/handshake`（带 `X-RAI-Sync-Protocol: 4`）返回 401；API/Worker 近 5 分钟日志无 WARN/ERROR。
- 未验证/未授权：按用户要求未运行任何 Go/Swift/集成测试；未用真实 iPhone/HealthKit 数据验证分段修复与 v4 上传；未读取 enrollment token 做带凭证的 v4 握手实测；未重算既有 Workout。部署不代表真机、模型或产品验收。

### Workout UUID 一次接收 — 后端与 iOS 实施（2026-11-09）

本轮按《RAI 秒回同步实施方案 · 每个 Workout UUID 接收一次》实现服务端与 iOS 部分：同一 dataset 内每个 `HKWorkout.uuid` 只接收一次，首次成功接收的 protocol-4 原始载荷是唯一权威副本。云端未部署、未迁移真实数据，云端仍为 schema 49 / OpenAPI 1.8.0。

- Schema 50：新增 `workout_ingests`，主键 `(dataset_id, workout_uuid)`，无 dataset 外键；保存精确 `raw_json`、`payload_bytes`、接收设备、dataset/collector epoch、状态、`accepted_at`、`commit_revision`、`commit_receipt`、`finished_at`、`error_code`、`attempts`、`next_attempt_at`、`lease_until`。运行期 `RequiredSchemaVersion=50`，冻结 checksum 已更新。
- 接收：`POST /v2/sync/workout-ingests/{workoutID}` 只校验 wire 身份（Bearer、dataset、epochs、UTF-8、重复键、深度、`protocolVersion=4`、大小），不解析样本语义；提交后才返回 `{workoutID, ingestStatus:"accepted", acceptedAt}`。重复 UUID 即使字节不同也重放首次 `acceptedAt`，原始字节不替换；已有 Workout 无接收行返回 `already_imported`；根 tombstone 返回 409 `workout_deleted`；每 dataset 待处理配额 200 行 / 512 MiB，超限 429 并带 `Retry-After`。进程内按 dataset 串行化配额，跨进程用 `ON CONFLICT DO NOTHING` + 读回收敛。
- 状态：`GET /v2/sync/workout-ingests/{workoutID}` 单语句快照，优先级 `deleted` > `accepted`/`committed`/`rejected` > `already_imported` > 404 `workout_ingest_not_found`；`committed` 返回 `commitRevision`/`analysisStatus`/`analysisJobIDs`，`rejected` 返回 `errorCode`；严格 dataset 隔离。GET 使用独立进程槽（8）与每设备 60/min、burst 10 的令牌桶，超限 429。
- Worker：`rai worker` 内单解析循环，90 s 租约、60 s 处理预算、最多 3 次尝试、5 s 起指数退避；解码 v4 原始字节后复用既有事实写核心。事实、历史、分析待办与 `committed` 状态在同一事务提交，更新条件包含 `state='accepted' AND attempts=$n AND lease_until > clock_timestamp()`；0 行即回滚整个事实事务并放弃本次认领。永久错误直接 `rejected`；暂态错误退避重试，末次租约过期由恢复扫描终结为 `processing_attempts_exhausted`。显式 Workout 删除清空待处理原始载荷并终止接收行，保留接收身份与既有回执。
- 模式：`RAI_WORKOUT_UUID_INGEST_MODE=legacy|uuid|uuid_paused`，默认 `legacy`。`uuid` 模式下旧 v4 `workout-bundles` POST 返回 409 `workout_uuid_ingest_required`，`uuid_paused` 返回 503 `workout_uuid_ingest_paused`；v3、GET 回执与管理删除路径不变。握手恒返回 `workoutUUIDIngestVersion=1` 与 `workoutUUIDIngestAccepting`；新端点走 `ingestAuth`，不依赖 `Admit` 的 dataset 行锁。`deploy/postgres/roles.sql` 为 `rai_api`/`rai_worker` 增加最小列级权限。
- iOS 入口：`SyncCoordinator.syncWorkout` 在详细 discovery 之前调用 `usesUUIDIngest`；UUID 会话（`uuidIngestEnabled && syncProtocolV4 && workoutUUIDIngestVersion>=1`）直接进入 `syncIngestWorkout`，不进入旧 bundle 打包与上传。任务以 `ingestMode="uuid"` 粘滞，已完成的旧历史保持原有跳过/刷新，已备好的 v4 原包被接管，v3 原包留在旧入口。UUID 会话跳过旧式明细 discovery 与 `syncDiscovered` 自动补传（Workout 列表与日常健康发现不变）；启动恢复（`recoverPending` → `recoverIngestTask`）、前台自动交付、普通手动同步与暂停恢复共用同一分支。
- iOS 投递：状态优先——先 `GET /v2/sync/workout-ingests/{uuid}`；`accepted`/`committed` 不读 HealthKit、不重发字节，只刷新状态与分析；仅确认未接收时才读取一次 HealthKit、写入会话内按 UUID 固定的成品文件并上传。恢复从不重建，已固定的成品文件按 UUID 重新挂接；接收后服务端 404 记为 `workout_ingest_missing_after_acceptance` 终态，绝不静默重传。接收回执与清理授权在 Outbox 单次原子提交，新路径不使用 uploadID/ingestID/载荷哈希，后台传输身份为 `workout-ingest:<datasetID>:<workoutID>`。
- iOS UI：阶段文案为“正在准备 / 正在上传 / 已接收，等待入库 / 已入库，等待分析 / 分析可查看”；已接收阶段只提供“检查状态”，分析刷新复用既有有界 follow-up，离开页面取消轮询、回到前台接续。
- v3 回归中顺带修复的两处语义问题（同轮模拟器回归暴露，不改变 v4 UUID 的接收规则）：复用已耗尽 discovery 流时，本地已显式删除的样本仍会以归档事实出现；旧比较把该类型标为 partial，非空的 `pendingReadKeys` 又让 ACK 不确认任何事件（包括包内真实携带的子删除）。现在以“排除显式删除后的事实数”衡量读取完整性。另一处是 v3 会话中 `syncWorkout` 在组包前执行 scoped discovery；已备好不可变包的续传不再 discovery，保持“重试零 HealthKit 读取”。
- 本机证据（2026-11-09）：新增 10 个真实隔离 PostgreSQL 18.6 集成用例 `TestWorkoutUUIDIngest*` 全 PASS（8.145 s；覆盖精确字节保留、提交与回执重放、HTTP 收件/状态/路径体不匹配、`already_imported`、dataset 隔离、删除优先与删前/删后竞态、有效租约不可认领、过期末次认领终结、非法原始字节与身份不匹配、collector fencing、受限 API/Worker 角色、暂态重试至耗尽与恢复提交；`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`）。`TestWorkoutBundleV4ImportAndIdempotency` 与 `TestWorkoutDeleteAndImportBothLockOrders` 同轮 PASS（4.392 s）。`internal/httpapi` 5 个新用例与 `internal/config` 模式用例 PASS；`scripts/local/test.sh gate` PASS（6 s；Go race/vet/依赖边界/controller 安全检查）；`scripts/local/contracts.sh` PASS（redocly 0 errors / 36 既有 warnings）。OpenAPI 升至 1.9.0，`contracts/sync-v4.md` 新增第 7 节，`cmd/rai version` manifest 同步。
- iOS 本机证据（2026-11-09）：`scripts/local/test.sh swift` PASS（270 tests / 38 suites，其中 `WorkoutIngestTests` 13 个 UUID 用例）；`scripts/local/test.sh simulator` PASS（98 tests：95 passed / 0 failed / 3 skipped，测试执行 51.5 s；含 `CaptureAckBoundaryTests` 3 个 ACK 边界用例：晚到样本与子删除保持 pending、父删除不阻断已备事实）。
- 性能（普通非 race 构建 + 隔离 PostgreSQL 18.6 + 本机回环 HTTP）：`TestWorkoutUUIDIngestPerf` n=30、warmups=5、failures=0；POST 收件到回执 p50 1.0 ms / p95 1.5 ms（目标 p95 < 300 ms），状态 GET p50 0.7 ms / p95 0.9 ms（状态端点有 60/min、burst 10 限流，测速按 1.1 s 间隔取 30 个样本，测的是请求时延而非限流吞吐），持 dataset `FOR UPDATE` 时完整 POST 15.4 ms（目标 < 500 ms）；合成载荷 753 B。`TestWorkoutUUIDIngestLockIndependence` 在持锁时 POST 7.8 ms / 状态 GET 10.0 ms。
- 未验证/未授权：未在真机/真实 HealthKit 上测量“点击到接收回执”（包含首次读取；本机只测到 HTTP 回执 p95）；未把 UUID 路径纳入 simulator e2e，也未用真实发布链路端到端确认 UUID 接收后的分析刷新（现有证据为 URLProtocol 合成与既有分析刷新代码复用）；批量同步尚未复用同一轮 handshake（每次 `syncWorkout` 仍各自握手，不影响 §7 的零明细查询/零组包/零 POST 条件）；未构建镜像、未部署、未应用 schema 50、未切换云端入口或迁移真实数据；未跑 `full` 全量集成回归与真机 HealthKit 验收。Worker 的 60 s 处理预算、既有 5 s statement timeout 与租约守护仅由代码与短载荷集成测试覆盖，未用大载荷压力验证。
