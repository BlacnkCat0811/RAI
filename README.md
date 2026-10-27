# RAI

在本目录从零建设的原生 iOS 跑步与健康分析系统：SwiftUI / HealthKit 客户端、Go 模块化单体、PostgreSQL 持久事实与任务队列。旧目录 `../running` 不参与构建和运行。

架构依据：[原始方案](RAI-Go-PostgreSQL架构重设计与实施方案.md)。实际进度、验收证据与下一步见 [实施记录](docs/IMPLEMENTATION.md)；工程约定见 [AGENTS](AGENTS.md)，边界与回滚见 [ADR-001](docs/adr/001-new-project.md)。

当前可运行的是 **v2 样本、有界多活动 Workout v2、超大 Workout 细节分块包与数量 series 细项上传内核**，包括活动配置及各层统计／事件、series 父样本绑定与不可变分块／显式完整标记、超大 Workout 核心登记与不可变细节分块／seal、事务批回执与页交付封口、删除屏障、提交日志、持久任务及跨进程的 dataset/device 请求预算及上传租约（schema 9）。尚无可发布的 iOS App；路线、结果下载、分析算法和训练计划尚未实现。服务握手明确声明这些能力边界；分析任务保持 queued。

按 [ADR-002](docs/adr/002-fresh-healthkit-dataset.md)，本次不迁移旧数据库。开发、真机与部署验收完成后，由新 iOS 客户端首次读取手机可见的 HealthKit 历史，随后走 anchor 增量同步；旧服务端计划、人工修改和旧回执不随健康事实重建。

## 本地启动

需要 Go **1.27.3**、Docker Compose，或单独的 PostgreSQL **18.6**。所有示例只用于新建开发数据库。生产必须配置独立最小权限角色，不能使用开发凭证。

```bash
# 当前工程根目录
docker compose -f deploy/dev/compose.yaml up -d

cd apps/server
export RAI_DATABASE_URL='postgres://rai_dev:development-only@127.0.0.1:55432/rai_dev?sslmode=disable'
go run ./cmd/rai migrate

# 仅首次执行。输出一次性设备凭证，安全保存，不要提交或写进普通日志。
go run ./cmd/rai init
go run ./cmd/rai api
```

API 默认只监听 `127.0.0.1:8080`。`RAI_LISTEN_ADDR` 可更改监听地址。挂载秘密文件时使用 `RAI_DATABASE_URL_FILE`，不能同时设置两种数据库 URL 来源。API 不自动执行 migration 或创建数据集。

另一个终端使用相同数据库配置运行 `go run ./cmd/rai worker`。当前 Worker 仅恢复过期租约，不消费尚未移植的分析任务，不能用它宣称分析完成。

`GET /livez` 检查进程；`GET /readyz` 检查数据库和 schema 版本。所有 `/v2` 接口都需要 `Authorization: Bearer <init 返回的 token>`。上传流程、单位及错误见 [同步合同](contracts/sync-v2.md)、[OpenAPI](contracts/openapi-v2.yaml)、[合同变更记录](contracts/CHANGELOG.md)，跨语言固定输入见 [fixtures](contracts/fixtures/envelope.json)。

NAS 连接见 [fnOS 访问说明](docs/FNOS-ACCESS.md)。远端现有库不作为开发／测试默认数据库。

## 验证

```bash
cd apps/server
go test ./...
go vet ./...

# 在隔离开发 PostgreSQL 上显式创建 rai_test 根测试库。
# 测试连接用户需要 CREATEDB；每个用例另建随机临时数据库并在结束时删除。
export RAI_TEST_DATABASE_URL='postgres://rai_dev:development-only@127.0.0.1:55432/rai_test?sslmode=disable'
go test -race -count=1 -tags=integration ./...
go vet -tags=integration ./...

cd ../..
python3 scripts/check-boundaries.py
```

本机负载较高时，可把同一套测试放到 fnOS/N100 上执行（需要免密 SSH 和远端 Docker）：

```bash
scripts/test-remote.sh
```

每次运行使用远端一次性 PostgreSQL 18 容器；优先用远端 Go 1.27.3 原生执行，缺少 Go/gcc 时回退 Go 容器。脚本不连接现有 `rai` 库；参数、镜像和资源限额见 [fnOS 访问说明](docs/FNOS-ACCESS.md#在-fnosn100-上跑测试)。

不带 `integration` tag 只运行单元测试。带 tag 时，缺少测试 URL 或根库名不是 `rai_test` 会失败，不会静默跳过。不要配置真实健康数据库。

若本机已有 PostgreSQL 18 工具，可从工程根目录执行 `PGBIN=/path/to/postgresql/bin scripts/test-postgres.sh`。脚本新建仅 Unix socket、启用 checksums 与同步落盘的临时 cluster，覆盖测试连接地址，退出时检查临时库残留、关闭并删除 cluster；不连接现有或远端数据库。

最大页及合成大表查询计划使用显式 `scale` tag，CI 单独执行：

```bash
PGBIN=/path/to/postgresql/bin scripts/test-postgres.sh \
  go test -race -count=1 -tags=integration,scale -run '^TestScale' -timeout=10m ./integration
```

此套件提交真实 10000 分片页，验证中途连接池重建、最终片故障回滚、封口与回执重试；在每表 1 万／10 万行合成数据上对实际 sqlc 查询执行 `EXPLAIN (ANALYZE, BUFFERS)`，保持默认扫描选择，不通过禁用顺序扫描制造索引命中。它验证本地正确性与查询读取预算，不能代替百万／千万规模、N100、多副本、手机导入吞吐或生产恢复验收。

SQL 代码由 **sqlc 1.31.1** 生成，在 `apps/server` 执行 `sqlc generate`。迁移工具固定为 **goose 3.29.0**，通过 `rai migrate` 显式运行。迁移 checksum 已冻结；新增变更追加新 migration。CI 同时验证格式、race、真实 PG、vet、生成代码和在线依赖边界；本地通过不代表远程 CI 已运行。

## 构建

```bash
docker build --platform linux/amd64 -t rai:dev apps/server
```

一个镜像分别以 `api` / `worker` / `migrate` / `init` 模式执行，默认非 root。开发 Compose 仅提供隔离 PostgreSQL，不是 fnOS 生产发布配置。生产网络、TLS/frp、备份恢复和真机验收仍按方案 P3–P8 推进；P6 旧数据迁移按 ADR-002 不在本次上线范围。
