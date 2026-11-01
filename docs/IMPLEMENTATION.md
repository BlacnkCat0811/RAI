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
