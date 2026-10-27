# RAI V2 Phase 1：Infrastructure Foundation 执行入口

状态：FROZEN，2026-10-27。P1 READY: YES。本文是下一轮唯一执行入口，本轮不执行。按 [基线](V2_PHASE0_BASELINE.md) 复用已有资产，只做基础设施和验收，不新增业务 API、迁业务、修改 iOS 或切换生产。P2 上传代码已存在不代表 P2 完成，也不是 P1 清理对象。开始前重新检查 Git 工作区并保护未跟踪内容。

## 固定布局与工具

- 使用已有 `apps/server`（模块 `rai/server`，Go 1.27.3）、`cmd/rai`、`internal/{config,httpapi,jobs,storage/postgres,admin}`、`migrations`、`queries`、`contracts`、`deploy/dev`、`.github/workflows`；按实际缺口创建目录，不重排现有包。API 与 Worker 分别构建和运行可通过同一二进制的 `api`/`worker` 子命令实现，同一 Docker 镜像；管理命令只经 CLI，不由在线包导入。
- PostgreSQL 18.6、pgx/v5 5.11.0、sqlc 1.31.1、goose 3.29.0；使用现有 `rai migrate` 和嵌入的有序 migration。保留 00001–00009 和校验和，新增必要迁移只追加；从空隔离开发库验证，不连接生产、不自动迁移启动时数据库。
- 保持结构化 `slog` JSON；不记录健康值、完整请求、token 或 DSN。`RAI_DATABASE_URL` 与 `RAI_DATABASE_URL_FILE` 二选一且必填；`RAI_LISTEN_ADDR` 默认 `127.0.0.1:8080`，容器设 `0.0.0.0:8080`；开发 Compose 固定或注入 `POSTGRES_DB=rai_dev`、`POSTGRES_USER=rai_dev` 和仅开发密码。测试用 `RAI_TEST_DATABASE_URL` 指向隔离 `rai_test`，不可使用生产凭证；生产使用挂载秘密与最小权限角色。资源参数先用 [基线预算](V2_PHASE0_BASELINE.md#预算与可观测性frozen-为待验证目标不是测量结果) 作为待验证值。

## 执行顺序

1. 核验现有构建、配置、API、Worker、数据库连接和脚本；记录实际缺口，禁止以改写代码消除已有同步合同。
2. API 保留现有 `GET /livez`（进程存活）和 `GET /readyz`（DB 可连接且 schema 匹配），增加 `GET /health` 和 `GET /ready` 分别提供相同语义。健康不探测外部业务，ready 在数据库不可用、schema 不匹配时非 2xx；不要声称已存在这两个别名。增加针对正常、DB 不可用和 schema 不匹配的必要测试。
3. Worker 可独立运行、建立并检查 DB 连接，收到 SIGTERM/SIGINT 停止并释放池；仅保留当前过期租约恢复，不 claim 或伪造完成未实现分析。API 停止接收请求并在有界时间优雅退出。对两进程分别验证正常与连接失败退出。
4. 补齐隔离开发 Compose：`postgres`、`api`、`worker` 使用同一 API/Worker 镜像，挂独立开发数据库卷、最小开发凭证、合理健康检查与资源限制；显式一次性执行 `rai migrate` 后才启动服务，不能在 API 启动时自动迁移或运行 `init` 输出凭证到日志。先用 `docker compose ... config` 检查，隔离空卷运行；先启动 postgres 并等待健康，再以同镜像一次性运行 `migrate`，成功后启动 api 和 worker；测试完成清理本次命名空间，不删除其他卷。验证 PostgreSQL、迁移、API、Worker 实际连接和端点。开发配置不可直接当生产发布。
5. CI 使用 Go 格式检查、依赖校验、`go test -race ./...`、`go vet ./...`、两个子命令构建、边界检查；真实隔离 PostgreSQL 集成覆盖空库迁移与连接。现有 CI OpenAPI lint 使用 Node，只属构建工具；P1 用非 Node 工具替代或明确移出 P1 必需流水线，同时不得静默删除合同校验或声称整个仓库无 Node 引用。远端 CI 实际运行后才标记通过。
6. 根据真实结果更新 `docs/IMPLEMENTATION.md`；记录命令、环境、失败和未验证项；不把已有文件或 mock 当容器、数据库、真机和生产验收。

## P1 验收门槛

以下为下一轮实际执行的清单，当前均未因 Phase 0 文档审核而勾选：

- [ ] API 与 Worker 分别编译及独立启动，同镜像以不同命令运行。
- [ ] PostgreSQL 18 容器启动健康；全新隔离数据库完成全部 migration，重复迁移安全；API 和 Worker 各自连接成功。
- [ ] `/health`、`/livez` 存活正常；`/ready`、`/readyz` 在数据库/schema 正常时成功，在不可用或不匹配时失败。
- [ ] `docker compose up` 在按上文先显式迁移后启动三服务；使用开发专用配置和独立卷。
- [ ] API 与 Worker SIGTERM 优雅退出；故障和超时不会伪装成功。
- [ ] `gofmt`、`go test ./...`、`go test -race ./...`、`go vet ./...`、构建、依赖边界检查通过；真实隔离 PG 集成及远端 CI 通过并记录证据。
- [ ] 运行时与 P1 必需 CI 不依赖 Node.js；不依赖服务端 SQLite、`running` 仓库，也无旧 API/schema 兼容层。保留 iOS 日后本地 outbox 可用 SQLite 的边界。
- [ ] 不做业务扩展、真实数据导入、生产部署或旧入口切换；现有业务和 00001–00009 migration 未被重置。

推荐验证命令：在 `apps/server` 执行 `gofmt -l cmd internal integration migrations/embed.go`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/rai`；在仓库根执行 `python3 scripts/check-boundaries.py`。隔离 PostgreSQL 可使用 `scripts/test-postgres.sh` 或 `scripts/test-remote.sh`，先确认其一次性数据库隔离和所需工具；不得指向已有真实库。实际 Compose 命令按最终配置指定项目名与路径，并记录迁移、端点、停机和清理结果。

当前 CI、镜像、真实 PG 测试的历史记录不自动满足本轮新 Gate；尤其 Compose 三服务、两个新健康路径、实际优雅退出和远程 CI 尚无本轮证据。
