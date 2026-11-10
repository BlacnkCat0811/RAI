# RAI V2 Phase 1 Result

## Status

**PASS WITH RISKS**。受控单步 down 已实现并以合成 migration 验证；既有 00001–00009 均不可逆、不可由该命令回退。用户已明确远端 CI 不属于 P1 门槛。

## Base Commit

`P1_BASE_COMMIT=c99d76f08438dcbc9603e018883339a3cd1d776d`，工作目录 `/Users/ta/rai`。

## Final Commit / Working Tree

未创建提交；HEAD 仍为上述提交。开始时已有已修改和未跟踪文件，本轮在其基础上工作并保留它们；工作区仍有未提交改动，不能将所有差异归因于本轮。

## Implemented

- 沿用已有 Go 模块和同一 `rai` 二进制的 `api`、`worker`、`migrate` 子命令；新增 `migrate-status` 和受控 `migrate-down`。
- 校验数据库 URL、秘密文件互斥和 HTTP 地址；启动失败记录安全的阶段原因。增加配置、健康、Worker 停止和连接池测试。
- JSON `slog` 日志；HTTP 超时、监听失败处理、信号取消和有界 HTTP shutdown。API 的容器探针检查进程存活。
- 开发配置模板及忽略规则；开发数据库的 UTF-8、UTC 和回环端口；测试同步排除环境文件、秘密和临时目录。
- 不新建业务表或算法，不重写现有业务和 migration。

## Architecture

继续使用冻结的 `apps/server`、`internal/config`、`httpapi`、`jobs`、`storage/postgres` 和仅 CLI 引用的 `admin`。API 和 Worker 使用同镜像不同命令；两者启动时检查数据库。现有业务并非本轮新增，也不代表 P2 已完成。

## API

`/health` 与 `/livez` 返回进程存活；`/ready` 与 `/readyz` 检查数据库及 schema，失败返回 503，不输出数据库错误或连接字符串。真实隔离 Compose 测试覆盖数据库停止和 schema 不匹配。

## PostgreSQL

PostgreSQL 18.6、pgx/v5 5.11.0、goose 3.29.0；API/Worker 默认池上限为 8/4，最小连接默认 0，连接最长生命周期 30 分钟，空闲超时采用 pgx 默认 30 分钟。可通过 pgx URL 的 `pool_max_conns`、`pool_min_conns`、`pool_max_conn_lifetime`、`pool_max_conn_idle_time` 配置。开发卷持久化，编码 UTF-8，服务与连接使用 UTC。

`rai migrate` 显式 up，重复执行已验证；`rai migrate-status` 列出状态。`rai migrate-down` 只执行最高已应用的、明确列入审核白名单且完整文件 SHA-256 匹配、具有非空 Down SQL 的单个 migration；未知已应用版本或不符合条件均拒绝。RAI 的 up/down 命令用 PostgreSQL advisory lock 串行化；外部工具不参加该锁，禁止并发运行。

现有 00001–00009 均无可执行 Down，生产白名单目前为空，因此当前对它们执行 `migrate-down` 会非零退出，不改变事实或版本记录。对既有 schema 的修复应设计并验证前向迁移；不能声称这些历史版本可逆。未来迁移须逐项审核其 Down、数据及协议影响，固定文件哈希并经隔离测试后才可获准回退。

## Worker

启动连接数据库；取消时停止循环并关闭池。当前只恢复过期租约，不消费或完成分析任务。

## Docker

`deploy/dev/compose.yaml` 提供 postgres、api、worker，使用项目隔离卷、开发专用凭证和资源限制；API 与 Worker 使用同一非 root 多阶段镜像。按照 [开发说明](../deploy/dev/README.md) 先显式迁移，再启动服务。PostgreSQL 与 API 端口默认仅绑定回环；开发凭证不是生产配置。

## Tests

2026-10-27 在 fnOS/N100 执行，使用独立临时目录和一次性 PostgreSQL 18.6 容器，全部为合成数据：

| 检查 | 结果 |
|---|---|
| `gofmt` 检查 | 通过 |
| `go mod verify` | 通过 |
| `go vet ./...` | 通过 |
| `go test ./...` | 通过 |
| `go build ./...` | 通过 |
| `go test -race ./...` | 通过 |
| 在线依赖边界 | 通过，246 包 |
| `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` | 通过；integration 包 105.534 秒 |
| 隔离 Compose | 通过：配置、空卷、UTF-8/UTC、迁移两次与 9 项状态、不可逆 down 非零且状态不变、连接、端点、故障、SIGTERM 和清理 |

后续在 fnOS 重新执行全部 Go 质量门禁和完整 race 集成，集成包 106.364 秒；新增的真实 PostgreSQL 专项验证未批准、哈希不匹配和未知版本的拒绝，保留合成事实与版本，以及独立合成 migration 的 up/down/up 和两次并发 down 串行化。首次专项运行遇到容器初始化期间连接重置；测试脚本改为等待最终 TCP 监听后重跑通过。没有运行远端 CI；没有使用既有 `rai` 数据库或真实健康数据。

## Dependencies

本轮没有新增 Go 依赖。既有 `pgx/v5` 提供 PostgreSQL 驱动和连接池，`goose/v3` 提供嵌入式 migration；其余为既有间接依赖。JSON 日志和 HTTP 使用标准库。

## Deviations

- 复用冻结布局、既有业务和 migrations，而非新建 `backend/` 或重置 schema。
- 回退按迁移逐项审核，现有不可逆迁移不支持 down；合成 migration 证明框架可执行真实 down，不代表历史事实可逆。
- 用户明确 macOS 开发、fnOS 测试、Gitee 管理代码；远端 CI 不属于本轮验收，GitHub/CL 待开发基本完成后再配置。
- 标准开发流程先显式执行迁移再启动服务；不自动迁移或运行 `init`。

## Risks

生产白名单为空，00001–00009 无可执行的 down；针对这些版本的任何降级都需另行设计，不能把前向修复等同于回退。没有验证生产部署、真实 HealthKit、断电恢复、真实负载或生产容量；Compose 与本轮数据库测试仅用于隔离开发。SIGTERM 覆盖进程正常退出，未专项证明有长时间在途请求时的表现。

## P2 Entry Gate

**P2 READY: YES**。按上述不可逆迁移保护和已调整的 CI 门槛进入 P2；本轮仍停止于 P1，不实施 P2。
