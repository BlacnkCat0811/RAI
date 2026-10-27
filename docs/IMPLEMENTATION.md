# RAI V2 实施合同与实际状态

更新：2026-10-27（Asia/Shanghai）。工作仓库 `/Users/ta/rai`。本文件是实施主合同；[Phase 0 基线](V2_PHASE0_BASELINE.md) 规定领域、协议、状态、预算及禁止事项，[Phase 1 执行计划](V2_PHASE1_PLAN.md) 是下一轮唯一入口。原始[架构方案](../RAI-Go-PostgreSQL架构重设计与实施方案.md)保留原文；本轮用户冻结的 P0–P7 路线与其旧阶段编号、兼容和迁移设想不一致时，按本合同实施，不修改原文或追求旧系统兼容。

## 目标与不变量

原生 iPhone SwiftUI 只读 HealthKit，WorkoutKit 排程另行授权；不建立独立 Watch、Web 或桌面客户端。Go 模块化单体提供 API 与异步 Worker，二者同镜像、不同进程；PostgreSQL 18 是服务端唯一主要事实库；Python 仅离线研究/训练，真正需要时才使用 ONNX 与 Go 推理。目标部署 N100 → PVE → fnOS VM（4 vCPU、8 GB）→ Docker。单用户优先正确性、简单、可维护、可观测，再考虑性能和扩展。

API 完成鉴权、验证、幂等及事务后 ACK，分析异步；原始事实、删除屏障、提交日志、分析待办和最终回执在同一事务提交，dataset 行锁产生提交顺序，不能用 sequence 当下载水位。原始事实保留已支持来源语义；derived 有版本、输入谱系且可重算。上传不可变、重试返回原回执、删除优先、旧上传不能复活 UUID；未知/不支持字段拒绝，不静默丢弃。日常同步不依赖备份、恢复、迁移、全库摘要或全量重算；管理工具不可被在线包导入。

当前数据生命周期目标是 RECEIVED → DURABLE → QUEUED → PROCESSING → READY；DURABLE 表示 PostgreSQL 提交完成，与分析结果无关。失败任务有界重试、租约和 fencing，不伪造完成。健康事实从手机当前可见的 HealthKit 历史首次导入后增量读取；不迁移旧库或沿用旧协议回执，见 [ADR-002](adr/002-fresh-healthkit-dataset.md)。

训练产品合同为 Generated Base Plan + User Override = Effective Plan → Execution → Workout Result → Coach Feedback。保留 override 来源和历史，重算不得静默覆盖；不建立平行手动计划。路线首版计划为有界 JSON/NDJSON 不可变分块、gzip 和 seal；不要求 PostGIS。后续能力仅在合同、实现和验收完成后声明，具体见基线。

## 当前实现与范围

当前工程不是空骨架：`apps/server` 为 Go 1.27.3 的 `rai/server`，已有 api/worker/migrate/init 单二进制模式、pgx/v5、sqlc、goose migrations 00001–00009、合成测试、开发 PostgreSQL Compose、Dockerfile 和 CI。现有 `/livez`、`/readyz`；P1 计划新增 `/health`、`/ready`，目前不能当作已存在。开发 Compose 目前仅启动 PostgreSQL，未验证三服务 Compose。

已实现的服务端切片包括 v2 数量/睡眠类别样本、有界多活动 Workout、超大 Workout 分块/seal、数量 series 分块/seal、页回执、持久请求预算和上传租约。当前握手声明对应能力；路线、changefeed、分析仍为 false。详细身份、限额及可观察错误以 [同步合同](../contracts/sync-v2.md) 和 [OpenAPI](../contracts/openapi-v2.yaml) 为准。已有业务代码保留；它的存在不是 P2/P3 完整验收。Workout 和样本内容不可变；对既有 Workout metadata 的迟到变化尚无补充事实协议，必须保留客户端待办，不能覆盖或声称支持。

当前 Worker 只回收过期租约，不消费或完成分析。原生 iOS、HealthKit 真机同步、路线、结果下载、分析算法、训练计划和生产部署未实现或未验收。SQLite 可用于未来客户端耐久 outbox，禁止用作服务端数据库。当前 CI 的 OpenAPI lint 使用 Node 工具，生产服务不依赖 Node；P1 须消除必需 CI 的 Node 依赖并保留合同校验。没有旧 API 或旧 schema 兼容层。

## 阶段路线与出口

| 阶段 | 内容与退出标准 | 实际状态 |
|---|---|---|
| P0 | 冻结架构、领域、同步、Worker、风险与 P1 可执行计划 | 本轮文档冻结；P1 READY: YES |
| P1 | 基础设施：Go、API/Worker、PG、显式 migration、配置、日志、健康、Compose、优雅停止、CI | 已有部分基础；完整门槛见 [P1 计划](V2_PHASE1_PLAN.md)，尚未通过 |
| P2 | 核心领域、route/jobs/repository 和事务边界 | 上传内核部分已实现与测试；完整领域、路线未完成 |
| P3 | iOS HealthKit 增量、批次、幂等、删除、重试、迟到及 route | 未端到端实现；metadata 更新语义待定 |
| P4 | Go 异步分析、可重算结果、算法与输入版本 | 未实现；Worker 不假装完成 |
| P5 | 训练基础计划、override、effective、执行与反馈闭环 | 未实现 |
| P6 | iOS API、同步、分析查询、训练与执行集成 | 未实现 |
| P7 | fnOS Docker、TLS、资源、可观测、PG 备份恢复、性能和用户明确授权的发布 | 未验收；不自动部署、切入口或迁移真实数据 |

旧文档/README 中旧 P0–P8 编号是历史表述；这里采用新 P0–P7，不能据编号推断既有工作消失或重复实施。详细责任、逻辑身份、路线比较、目标 SLO、资源预算及 Do Not Build 见 [基线](V2_PHASE0_BASELINE.md)。

## 证据与未验证项

历史记录报告 fnOS/N100 Linux、一次性隔离 PostgreSQL 18.6 和合成数据的 race 集成回归通过，scale 测试通过；机器可读结果见 [fnOS 证据](evidence/fnos-remote-tests-2026-10-27.json) 及 [server 证据](evidence/server-tests-2026-10-27.json)。Workout 包 20,000 合成细节和 series 100 万点有专项证据；series 首轮全量流式核验曾触发 statement timeout，改用逐块读取后通过，不能推断全量流式读取已达标。其他测试证据保留在 `docs/evidence/`。历史结果不代表本轮重跑、不证明生产吞吐、真实 HealthKit、断电恢复、实际多副本或部署通过。

P1 未验证三服务 Compose、`/health` 和 `/ready`、独立优雅停止和远程 CI；P7 尚需真实设备、备份恢复、资源干扰和安全部署验收。`main` 尚无提交、无可引用 HEAD；所有文件在本轮开始均为未跟踪。源文件存在、mock 通过或此前隔离 PG 通过均不能替代上述验收。

本轮只改三份指定文档；未修改 Go、Swift、SQL、Docker、CI、脚本或原方案，未访问旧仓库、生产数据库，也未部署或迁移。阶段检查结果在本轮结束时以实际命令结果补记于下方。

Phase 0 文档变更的本轮检查：在 `apps/server` 使用既有 Go 1.27.3 执行 `go test -race ./...` **PASS（缓存）**、`go vet ./...` **PASS**；根目录执行 `python3 scripts/check-boundaries.py` **PASS（245 个包）**。初次边界检查因 shell PATH 无 `go` 未能启动；将 `/tmp/rai-go-pg-tools/go/bin` 加入 PATH 后重跑通过。只改文档，无事务变更，本轮未运行 PostgreSQL 集成测试、Compose、远程 CI、真机或部署测试。历史 fnOS 连接和测试环境信息保留在 [访问说明](FNOS-ACCESS.md) 与上述证据，不表示本轮连接过远端。
