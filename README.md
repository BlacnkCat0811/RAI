# RAI

在本目录从零建设的原生 iOS 跑步与健康分析系统：SwiftUI / HealthKit 客户端、Go 模块化单体、PostgreSQL 持久事实与任务队列。旧目录 `../running` 不参与构建和运行。

架构依据：[原始方案](RAI-Go-PostgreSQL架构重设计与实施方案.md)。实际进度、验收证据与下一步见 [实施记录](docs/IMPLEMENTATION.md)；工程约定见 [AGENTS](AGENTS.md)，边界与回滚见 [ADR-001](docs/adr/001-new-project.md)。

P6原生产品与P3/P4/P5历史合同和验收保留，见 [P6 Result](docs/V2_PHASE6_RESULT.md)。本轮 **Phase 7 Production Hardening** 增加三服务发布模板、原生HTTPS、独立运行角色/文件secrets、加密备份与真实隔离恢复、聚合运维/告警、故障注入、容量工具与安全outbox GC；最终状态和未验证项见 [P7 Result](docs/V2_PHASE7_RESULT.md)。P3/P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED；PRODUCTION RELEASE READY: NO。WorkoutKit、独立Watch和正式发布未开放，无Metal/GPU、iOS18及真机风险不由P7测试解除。

按 [ADR-002](docs/adr/002-fresh-healthkit-dataset.md)，本次不迁移旧数据库。开发、真机与部署验收完成后，由新 iOS 客户端首次读取手机可见的 HealthKit 历史，随后走 anchor 增量同步；旧服务端计划、人工修改和旧回执不随健康事实重建。

后续独立专项 [Monthly Backup](docs/RAI_MONTHLY_BACKUP.md) 提供每月1日03:00、无额外文件加密的整库 SSH 备份、最近三份成功副本保留及隔离恢复入口；保留原 P7 工具。正式云端/cron 尚未安装。[灾难恢复审查](docs/RAI_DISASTER_RECOVERY.md) 明确当前 HealthKit recovery resync 为 BLOCKED，不能通过 reset Anchor 或清空 Outbox 处理旧数据库回退；本轮验证和逐项门禁见 [Backup/Recovery Result](docs/RAI_BACKUP_RECOVERY_RESULT.md)。

后续部署准备及月度监控适配见 [Monthly Deploy Result](docs/RAI_MONTHLY_BACKUP_DEPLOY_RESULT.md)；只比较恢复旧库与新建数据集的简化方案见 [Minimal Recovery Review](docs/RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md)。真实V2/云备份身份与正式调度仍待配置和明确批准，灾难恢复实现未开始。

## 本地启动

开发环境使用 Go **1.27.3** 和 PostgreSQL **18.6**。三服务 Compose 的显式迁移与启动步骤见 [开发 Compose](deploy/dev/README.md)。该配置仅用于合成开发数据，生产必须配置独立最小权限角色和秘密。

API 默认只监听 `127.0.0.1:8080`；容器配置监听 `0.0.0.0:8080`。`RAI_LISTEN_ADDR` 可更改监听地址。使用挂载秘密文件时配置 `RAI_DATABASE_URL_FILE`，不能同时设置两种数据库 URL 来源。API 不自动执行 migration 或创建数据集。仅在确需创建开发数据集时手动运行 `rai init`；其一次性设备凭证必须安全保存，不提交或写入普通日志。

Worker 默认单 processing slot，消费 Workout、依赖、training-state 与 training.plan 任务，20秒续租；结果写入、publication、generation clean 和 job complete 原子提交。`GET /health` 和 `/livez` 检查进程；`GET /ready` 和 `/readyz` 检查数据库和 schema 版本。所有 `/v2` 接口都需要 `Authorization: Bearer <init 返回的 token>`。上传流程、单位及错误见 [同步合同](contracts/sync-v2.md)、[Phase 3 同步合同](docs/V2_PHASE3_SYNC_CONTRACT.md)、[OpenAPI](contracts/openapi-v2.yaml)、[合同变更记录](contracts/CHANGELOG.md)，跨语言固定输入见 [fixtures](contracts/fixtures/envelope.json)。

NAS 连接见 [fnOS 访问说明](docs/FNOS-ACCESS.md)。远端现有库不作为开发／测试默认数据库。

## 验证

```bash
cd apps/server
go test ./...
go vet ./...

# 在隔离开发 PostgreSQL 上显式创建 rai_test 根测试库。
# 测试连接用户需要 CREATEDB；每个用例另建随机临时数据库并在结束时删除。
export RAI_TEST_DATABASE_URL='<独立隔离的 rai_test PostgreSQL URI>'
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

一个镜像分别以 `api` / `worker` / `migrate` / `init` / `analysis` / 显式`ops`模式执行，默认非root；`version`输出发布版本，`resources`读取容器cgroup v2。开发Compose仅用于合成开发，发布准备使用 [独立模板](deploy/release/compose.yaml) 与 [部署步骤](docs/P7_DEPLOYMENT.md)，不自动migration/init。P7配置与隔离验收不等于正式上线；真实入口、离机备份、证书/secret安装、真机与用户发布批准仍是门禁。旧数据迁移按ADR-002不在本次范围。

## P7 运维与验证

见 [Operations](docs/P7_OPERATIONS.md)、[Backup/Restore](docs/P7_BACKUP_RESTORE.md)、[Security](docs/P7_SECURITY.md)、[Capacity](docs/P7_CAPACITY.md) 与 [Release Gate](docs/P7_RELEASE_CHECKLIST.md)。`rai ops snapshot`为受限聚合运维，不新增公开metrics；`ops retention-dry-run`不删除事实、回执或屏障；`ops verify`为显式管理核验，不在同步路径运行。正式任务、证书续期和通知渠道尚需现场批准配置。

```bash
scripts/test-remote.sh bash ../../scripts/check-p7.sh
scripts/test-remote.sh env RAI_SOURCE_COMMIT=<verified-head> bash ../../scripts/test-p7-compose.sh
python3 scripts/test-p7-alerts.py
python3 scripts/test-p7-gc.py
python3 scripts/p7-secret-audit.py
```

这些脚本使用新建合成实例、唯一项目/credentials/volume；故障测试只清理本次资源。生产DSN和既有数据库不作为测试输入，禁止prune、覆盖真实卷或自动部署。容量/规模命令及统计口径见Capacity文档。

## 分析管理

参数必须显式提供来源；不默认推测个体 HRmax、resting HR 或 sex coefficient。命令说明见 [算法合同](docs/P4_ALGORITHM_CONTRACT.md)。有界重算示例（需要操作者填入明确 dataset）：

```bash
rai analysis recompute --dataset <uuid> --scope athlete/default \
  --algorithm training_state.bundle/v2 --from 2026-10-01 --through 2026-10-28 --dry-run
```

去掉 `--dry-run` 才会耐久排队。每次最多366输出日或1000 Workout；不默认全库，不进入 HealthKit 热路径。
