# RAI V2 Phase 0：实施基线冻结

日期：2026-10-27（Asia/Shanghai）；工作根目录 `/Users/ta/rai`。本轮为设计冻结，只修改三份指定文档，不进入 P1。状态用 FROZEN（实施合同）、OPEN（影响当前合同且待解决）、DEFERRED（不阻挡 P1）。原始方案保留原文；本基线和 [实施合同](IMPLEMENTATION.md) 对本轮新路线及旧方案不一致处具有实施优先级。已有的 [同步协议](../contracts/sync-v2.md) 是现有上传实现的详细合同；目标能力不等于已实现能力。

## 仓库现状分类

| 区域 | 当前状态 | 决策 |
|---|---|---|
| 原始方案、AGENTS、README、ADR | 文档存在，方案原文保留；README 和旧 ADR 的阶段号尚为旧路线 | KEEP；路线以新实施合同为准，后续获授权再更新引用 |
| `apps/server` | `rai/server` Go 1.27.3；单二进制 api/worker/migrate/init；pgx/v5、sqlc；已有同步业务和测试 | KEEP；P1 核验基础设施，不清除现有业务 |
| PostgreSQL | goose migrations 00001–00009、角色脚本和真实隔离 PG 测试已有；非完整领域模型 | KEEP；P1 验证从空库运行，不回写历史 migration |
| contracts / fixtures | 上传合同、OpenAPI 和合成 fixture 已有；routes、changefeed、analysis 未实现 | KEEP；新能力先更新合同，禁止伪装成功 |
| Worker | jobs 租约存储和过期恢复存在；不消费或完成分析 | KEEP；P1 只验证骨架，分析属于 P4 |
| iOS / Watch / Web / 桌面 | 当前无 iOS 工程；无独立 Watch、Web、桌面客户端 | iOS NOT_IMPLEMENTED；其他客户端 REMOVE（禁止新增，并非删除现有文件） |
| Docker / CI / scripts | Dockerfile 单镜像；开发 Compose 仅 PostgreSQL；CI 和隔离测试脚本存在 | MODIFY 后续按 P1 补核验；现有 CI 的 OpenAPI lint 使用 Node，非生产依赖，不误称完全没有 Node 工具 |
| 算法、训练、路线、生产部署 | 未完成；无分析执行、训练计划或路线上传 | NOT_IMPLEMENTED；按 P2–P7 |
| 文档、证据 | 原 IMPLEMENTATION 是详尽历史记录；机器可读测试证据存在 | MODIFY 实施合同；保留证据文件及关键失败和限制 |
| Git | main 无提交，HEAD 不存在；初始全部文件未跟踪 | NEED_DECISION：用户之后决定何时提交；不阻挡 P1，不擅自提交 |

## 冻结决策

- **FROZEN 架构**：原生 iPhone SwiftUI + 只读 HealthKit；WorkoutKit 排程单独授权。Apple Watch 可通过系统排程生态参与，但不开发独立 Watch 客户端。Go 模块化单体 API + Worker 同镜像不同进程；PostgreSQL 18 为唯一服务端主要事实库。PVE 上 fnOS VM（4 vCPU、8 GB）使用 Docker；HTTPS、权限与备份单独验收。
- **FROZEN 布局**：沿用 `apps/server/{cmd/rai,internal,migrations,queries}`、`contracts`、`deploy`、`scripts`、`docs`；将来新建 `ios/RAI` 与按需 `ml/`。不为了示例迁移目录。业务边界按 sync、storage、jobs、analysis、training、coach 逐步建立；管理工具不被在线包导入。`rai/server`、Go 1.27.3、pgx/v5 5.11.0、sqlc 1.31.1、goose 3.29.0、PostgreSQL 18.6 为现有固定版本，升级须单独验证。
- **FROZEN API/Worker**：API 验证、鉴权、幂等、事务、事实及待办提交后快速 ACK；Worker 异步标准化、计算和发布结果。分析失败不撤销事实 ACK。现有 `/livez`、`/readyz` 继续保留；P1 增加 `/health`、`/ready` 同语义入口并测试，避免混淆为已实现。
- **FROZEN 数据边界**：raw facts 保留来源标识、时间、单位及已明示支持的完整语义；derived 数据可重算并带算法版本和输入谱系；sync state 保存身份、anchor/outbox、回执、删除屏障及提交顺序；analysis state 保存 dirty generation、jobs、attempt 和结果发布状态。拒绝未知字段、类型和单位，不裁剪后提交。
- **FROZEN 提交**：事实、删除屏障、提交日志、分析待办和最终回执同一事务；dataset 行锁决定提交顺序，sequence 不作为下载水位。只有 commit 后 durable ACK；原始回执与不可变上传重试一致。同步不依赖备份、恢复、迁移、全库摘要或全量重算。
- **FROZEN 基础设施**：首版 PG jobs + Go Worker；无 Redis、Kafka、RabbitMQ、NATS、Temporal、Kubernetes。Python 仅离线研究、训练和评估；有真实模型与验收需求后才引入 ONNX 和 Go 推理。
- **FROZEN 路线**：首版有界 JSON/NDJSON 轨迹点分块，记录时间、坐标、可用高度和精度等来源字段，gzip 传输、不可变分块及显式完整标记；元数据与点有父关系，按来源顺序存储，可分页回放和计算。单个分块有大小和数量限额；具体限额经合成及真机测试后冻结。JSON 单大数组易超内存；protobuf/binary 增加跨语言复杂度；encoded polyline 可能损失时间、精度、高度，均暂不采用。PostGIS 非首版依赖。路线协议及代码尚未实现。

## 生命周期与同步

`RECEIVED` 是接收／验证过程、非持久成功；`DURABLE` 是 PostgreSQL 已提交事实、屏障、日志、待办、最终回执；`QUEUED → PROCESSING → READY` 是分析生命周期，`PROCESSING → FAILED` 可按次数上限、退避和租约安全重试。DURABLE 与 QUEUED 可在同一事务产生，不要求保存所有状态字面量。不可分析的对象须真实显示待处理或失败，不伪造 READY。

HealthKit 增量读取 → 结构化白名单 → 耐久不可变 gzip 文件与批次 → Go API → PostgreSQL；客户端本地按 dataset/collector/type/queryFingerprint 隔离，持久化 captured 与 delivered 双水位，服务端不接收 HealthKit anchor。每个 Workout 或 sample 以 `(dataset, HealthKit type, HealthKit UUID)` 标识；删除事件必须明确来源，空查询或权限变化不推断删除。delete 优先，未知删除也建立屏障，旧上传不能复活 UUID。同批相同事实幂等，不同内容冲突。批以 dataset/stream 下的 batch UUID、device/collector、clientSeq、previousBatchID、createdAt、payload digest 和页身份固定，createdAt 是来源元数据而非排序水位；同身份相同文件重试查到原回执，不同内容冲突。页全体封口后才能推进 delivered；注册、单片回执或任务完成不能证明 HealthKit 查询完整。完整协议及限额以 [同步合同](../contracts/sync-v2.md) 为准。

迟到心率以独立 sample UUID 插入，按影响范围产生新 generation；GPS 作为独立路线子对象未来上传与 seal；已提交 Workout 本体不可变，追加不同 metadata 不能悄悄覆盖或用同 UUID 伪装新事实。**OPEN（不阻挡 P1）**：HealthKit 来源 metadata 真正变化时，需在 P3 在客户端确认语义、定义有版本的独立补充事实及冲突/删除规则并测试，当前先保留本地待办，不能声称该更新已支持。新数据集首次读取手机当前可见历史后增量同步；不迁移旧库，见 [ADR-002](adr/002-fresh-healthkit-dataset.md)。

## PostgreSQL 核心领域（逻辑模型，不是本轮建表指令）

| 领域及属性 | 父关系和唯一身份；幂等／生命周期 |
|---|---|
| datasets、devices：sync state | dataset 是隔离根；设备在 dataset 内唯一，凭证撤销和采集器 epoch 受控 |
| sync_streams、sync_batches、sync_pages、回执、删除屏障、change_events：sync state | dataset/device/stream 下分别按 stream UUID、batch UUID、page UUID、实体 type+UUID、`commit_rev+ordinal` 唯一；不可变重试返回原回执；服务端无 HealthKit anchor |
| workouts、health_samples：raw | dataset + type + HealthKit UUID 唯一；同内容不变，不同内容冲突；明确删除产生永久屏障 |
| workout activities/statistics/events、quantity series/chunks：raw 或耐久 staging | Workout 或 sample 为父；按来源 UUID/序号或 series ID+chunk index 唯一；顺序及内容不可变，seal 才发布完整对象，父删除级联 |
| workout_routes、route_chunks：raw，未来 | 父 Workout + HealthKit route UUID；chunk index 唯一，重复原字节回执，seal 完整；父删除及屏障阻止复活 |
| dirty_scopes、jobs、job_attempts：analysis state | dataset + scope + generation / job ID / fencing token；事务排队，租约重试和代际 fencing，失败可观察 |
| analysis_runs、workout_analysis、load/fitness/fatigue/recovery/readiness：derived，未来 | 父 dataset、scope/Workout、算法版本与输入 revision/generation；结果与发布身份唯一，输入已变化时不发布旧结果，可重新计算 |
| training_plans、training_sessions、training_session_overrides、training_executions：训练事实与派生 | plan → session → 有 provenance 的 override；execution 关联 session、有效版本及可选 Workout；ID/命令幂等，历史不可被重算静默覆盖 |

现有数据库具体表见 migrations，未来逻辑名不暗示已有表。派生训练建议和 effective plan 可重算；用户 override 和执行记录为事实。计划重算生成新 base 版本，保留 override；删除、冲突或无法应用的 override 明确提示并留存，不默默丢失。执行和反馈关联当时 effective 版本，用户无需维护另一套手动计划。

## Worker 与算法

以 PostgreSQL 有界 claim、短事务行锁（`SKIP LOCKED`）、租约到期、attempt、fencing token 和退避重试。领取后在事务外计算，续租和完成校验 token、状态与有效期；结果发布与目标 generation 的 CAS 同事务，新增变更不被旧任务清除。上限失败记录原因并可受控重试；不扫描全部事实重建队列。当前 Worker 仅回收过期租约，**未执行分析**。

Go 负责 pace、HR、cadence、zones、splits、TRIMP、load、fitness、fatigue、recovery、readiness、recommendation、训练及 coach 规则；算法定义、单位、输入缺失、时区、版本与合成测试先于宣称完成。Python 离线研究与模型评估；ONNX 为有明确收益和模型时的可选产物，不建设空模型基础设施。

## 产品与部署

`Generated Base Plan + User Override = Effective Plan → Execution → Workout Result → Coach Feedback`。iPhone 与系统排程使用同一 effective 版本，WorkoutKit 写入需独立授权；不开发独立 Watch App 或第二套手动计划。计划、排程与执行尚未实现。

目标为 N100 → PVE → fnOS VM（4C/8G）→ Docker：同镜像 API/Worker + PostgreSQL；开发环境与生产角色隔离。pg_dump 定时备份、保留策略、异地副本及定期隔离恢复测试是 P7 管理流程，不进入同步请求。RPO/RTO、存储位置、加密和保留期需现场确定，不能将备份存在等同恢复通过。

## 预算与可观测性（FROZEN 为待验证目标，不是测量结果）

在目标 4C/8G，合成负载、单客户端、热数据库，至少 100 次请求，记录 p50/p95/p99、错误与样本数；网络本机或同 LAN 分别记录，不含蜂窝网络。初始目标：`/health` p95 < 100 ms；普通有界查询 p95 < 500 ms；不含上传传输时间的普通批次 ACK p95 < 5 s，超限须显式失败并保留待办。20 分钟、45 分钟、4 小时 Workout 分别合成不同点密度与路线，报告总采集/压缩字节、批次、端到端传输与提交时间，不用时长推测点数；每场景目标完整可恢复，普通批 ACK 仍遵守上述门限。Worker 空闲到领取 p95 < 30 s；单 workout 分析 p95 < 60 s（明确算法、样本点数后测试），不能以当前无算法的 Worker 伪造达标。数据库故障、重试、后台与负载干扰另测。

初始容器上限：API 512 MiB、Worker 1 GiB、PostgreSQL 2 GiB；API 和 Worker 各 1 CPU 配额、PG 最多 2 CPU，Worker 计算并发 1，保留系统余量；此为 P7 调优起点，不代表当前 Compose 已设置或性能已验证。基本结构化日志和轻量指标：API 请求数/延迟/错误率；sync 批大小、耗时、重复数、提交样本数；worker queued/processing/failed 与耗时；DB 连接数、查询耗时、大小。日志不含健康值、token、DSN 或请求正文；不预设大型观测栈。

## Do Not Build（FROZEN）

Node.js/Fastify 后端兼容、SQLite 服务端数据库与多库发布／文件替换／generation pointer、V1/V2 永久双写、旧 API/schema 兼容层、Vite 前端、Windows runtime、移动 Node runtime、Swift 评分引擎、平行手动计划系统、无证据的 Redis、Kafka、RabbitMQ、Kubernetes、微服务拆分。CI 的现有 Node OpenAPI 校验工具与服务端运行时是不同依赖；P1 决定是否替换该工具，不能把它误说成生产后端。SQLite 可用于未来 iOS 本地耐久 outbox，不是服务端事实库。

## 风险与延后

- **OPEN**：上述 Workout metadata 更新语义；未完成不阻挡仅做基础设施的 P1，但阻挡声称支持该 P3 场景。
- **DEFERRED**：路线限额及优化、分析公式与数据缺失策略、ML 模型、WorkoutKit/Watch 系统行为、TLS 与部署现场参数、备份 RPO/RTO、生产资源调优与性能验收。
- 当前仓库无提交 SHA；现有真实 PG 合成证据不是生产部署、真实 HealthKit、断电恢复或全量导入吞吐验收。P1 前无布局、数据库、服务边界、部署模型或实施合同的根本阻塞。P1 READY: YES；见 [P1 计划](V2_PHASE1_PLAN.md)。
