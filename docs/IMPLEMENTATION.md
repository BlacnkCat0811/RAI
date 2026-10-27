# RAI V2 实施合同与实际状态

更新：2026-10-27（Asia/Shanghai）。工作仓库 `/Users/ta/rai`。本文件是实施主合同；[Phase 0 基线](V2_PHASE0_BASELINE.md) 规定领域、协议、状态、预算及禁止事项，[Phase 1 执行计划](V2_PHASE1_PLAN.md) 是本阶段执行入口。原始[架构方案](../RAI-Go-PostgreSQL架构重设计与实施方案.md)保留原文；本轮用户冻结的 P0–P7 路线与其旧阶段编号、兼容和迁移设想不一致时，按本合同实施，不修改原文或追求旧系统兼容。

## 目标与不变量

原生 iPhone SwiftUI 只读 HealthKit，WorkoutKit 排程另行授权；不建立独立 Watch、Web 或桌面客户端。Go 模块化单体提供 API 与异步 Worker，二者同镜像、不同进程；PostgreSQL 18 是服务端唯一主要事实库；Python 仅离线研究/训练，真正需要时才使用 ONNX 与 Go 推理。目标部署 N100 → PVE → fnOS VM（4 vCPU、8 GB）→ Docker。单用户优先正确性、简单、可维护、可观测，再考虑性能和扩展。

API 完成鉴权、验证、幂等及事务后 ACK，分析异步；原始事实、删除屏障、提交日志、分析待办和最终回执在同一事务提交，dataset 行锁产生提交顺序，不能用 sequence 当下载水位。原始事实保留已支持来源语义；derived 有版本、输入谱系且可重算。上传不可变、重试返回原回执、删除优先、旧上传不能复活 UUID；未知/不支持字段拒绝，不静默丢弃。日常同步不依赖备份、恢复、迁移、全库摘要或全量重算；管理工具不可被在线包导入。

当前数据生命周期目标是 RECEIVED → DURABLE → QUEUED → PROCESSING → READY；DURABLE 表示 PostgreSQL 提交完成，与分析结果无关。失败任务有界重试、租约和 fencing，不伪造完成。健康事实从手机当前可见的 HealthKit 历史首次导入后增量读取；不迁移旧库或沿用旧协议回执，见 [ADR-002](adr/002-fresh-healthkit-dataset.md)。

训练产品合同为 Generated Base Plan + User Override = Effective Plan → Execution → Workout Result → Coach Feedback。保留 override 来源和历史，重算不得静默覆盖；不建立平行手动计划。路线首版计划为有界 JSON/NDJSON 不可变分块、gzip 和 seal；不要求 PostGIS。后续能力仅在合同、实现和验收完成后声明，具体见基线。

## 当前实现与范围

当前工程不是空骨架：`apps/server` 为 Go 1.27.3 的 `rai/server`，已有 api/worker/migrate/init 单二进制模式、pgx/v5、sqlc、goose migrations 00001–00009、合成测试、开发 Compose、Dockerfile 和 CI。`/health`、`/livez` 检查进程存活，`/ready`、`/readyz` 检查数据库与 schema。开发 Compose 包含 PostgreSQL、API 和 Worker，启动前须显式执行 migration；fnOS 隔离验收见下。

已实现的服务端切片包括 v2 数量/睡眠类别样本、有界多活动 Workout、超大 Workout 分块/seal、数量 series 分块/seal、页回执、持久请求预算和上传租约。当前握手声明对应能力；路线、changefeed、分析仍为 false。详细身份、限额及可观察错误以 [同步合同](../contracts/sync-v2.md) 和 [OpenAPI](../contracts/openapi-v2.yaml) 为准。已有业务代码保留；它的存在不是 P2/P3 完整验收。Workout 和样本内容不可变；对既有 Workout metadata 的迟到变化尚无补充事实协议，必须保留客户端待办，不能覆盖或声称支持。

当前 Worker 只回收过期租约，不消费或完成分析。原生 iOS、HealthKit 真机同步、路线、结果下载、分析算法、训练计划和生产部署未实现或未验收。SQLite 可用于未来客户端耐久 outbox，禁止用作服务端数据库。P1 服务 CI 不使用 Node；保留独立 OpenAPI lint 工作流，该合同工作流使用 Node。生产服务不依赖 Node。没有旧 API 或旧 schema 兼容层。

## 阶段路线与出口

| 阶段 | 内容与退出标准 | 实际状态 |
|---|---|---|
| P0 | 冻结架构、领域、同步、Worker、风险与 P1 可执行计划 | 本轮文档冻结；P1 READY: YES |
| P1 | 基础设施：Go、API/Worker、PG、显式 migration、配置、日志、健康、Compose、优雅停止、fnOS 质量门禁 | 本轮状态见下方最新记录；远端 CI 不再是验收条件 |
| P2 | 核心领域、route/jobs/repository 和事务边界 | PASS WITH RISKS；核心模型、路线暂存与 fnOS 验证完成，路线发布协议留待 P3 |
| P3 | iOS HealthKit 增量、批次、幂等、删除、重试、迟到及 route | 未端到端实现；metadata 更新语义待定 |
| P4 | Go 异步分析、可重算结果、算法与输入版本 | 未实现；Worker 不假装完成 |
| P5 | 训练基础计划、override、effective、执行与反馈闭环 | 未实现 |
| P6 | iOS API、同步、分析查询、训练与执行集成 | 未实现 |
| P7 | fnOS Docker、TLS、资源、可观测、PG 备份恢复、性能和用户明确授权的发布 | 未验收；不自动部署、切入口或迁移真实数据 |

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
