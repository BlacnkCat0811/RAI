# P7 Deployment Runbook

2026-10-29，Asia/Shanghai。此为发布准备；本轮只运行 `rai-p7-test-*` / `rai-p7-restore-*` / `rai-p7-disk-*` 合成环境。真实 fnOS `rai-postgres`、旧入口和旧工程不参与操作。验收状态与实测见 [P7 Result](V2_PHASE7_RESULT.md) 和 [evidence](evidence/p7-fnos-2026-10-29.json)。

## 环境与前置条件

| 环境 | 用途 | 身份与存储 |
|---|---|---|
| macOS 开发 | 源码、Swift、编译 | 不指向 NAS 真实库 |
| 一次性 fnOS 测试 | 合成集成、故障、恢复与容量 | 唯一 project、随机密码、全新卷或有上限 tmpfs |
| 准生产 | 用户批准后安装真实证书并演练 | 独立 project/credentials/volume；不得复用测试数据 |
| 正式生产 | 用户单独授权后启用 | 独立审批、离机备份与真机门禁；当前未建立 |

宿主运维脚本需要Bash、Python≥3.11（hashlib.file_digest）、OpenSSL、GnuPG/gpgconf及Docker/Compose；本轮fnOS为Python3.11.2、GnuPG2.2.40。它们是按需工具，不是常驻后端。

目标 N100/PVE/fnOS VM 4C8G；Docker Compose v2 兼容语法（隔离测试为 v5.1.0）。常驻仅 PostgreSQL、Go API、Go Worker。Go 原生 TLS 终止 HTTPS，因此不增加 proxy；它不提供 ACME，证书由操作者已验证的 PKI/续期流程提供。VPN 负责私网可达性，TLS 负责证书链、主机名与传输保护，两者不能互相替代。现有 VPN/LAN/证书/防火墙尚未完成入口级验收或配置；SSH 测试通道不能证明 iPhone 可访问。

只读宿主清点看到UP的host接口（私网及global地址均存在）与Docker网络；当前PATH未发现wg/tailscale/openvpn/zerotier-cli及nft/iptables/ufw。该结果不能排除路由器VPN、其他namespace或fnOS外部防火墙；未读取现有frpc配置、正式证书或秘密。正式手机入口、防火墙规则与TLS仍NOT VERIFIED，不因SSH可达假定已有安全网络边界。

镜像来自 [Docker 官方 PostgreSQL 文档](https://github.com/docker-library/docs/blob/master/postgres/README.md) 与既有 Go 固定摘要。PG18 的 `PGDATA=/var/lib/postgresql/18/docker`，卷挂载 `/var/lib/postgresql`，已在模板明确；大版本升级须单独审核 pg_upgrade 或逻辑恢复，不能把旧 PG17 卷直接挂到18。Go 与 PG 镜像禁止使用 latest；正式 `RAI_RELEASE_IMAGE` 必须为已审核的 registry digest。本轮本地构建 image ID 仅是测试工件，不是已推送的发布镜像。

## 准备与预检

从仓库根执行，所有值由操作者明确指定。以下有状态命令仅在用户批准的目标执行；**本轮未对真实环境执行**。

1. 将 `deploy/release/environment.example` 的非秘密设置填到 Git 外的配置；向当前 shell 导出所需变量。默认绑定 `127.0.0.1`，不配置路由器、frp、VPN、公网或防火墙规则。手机入口改为已批准的私网地址前须现场审查。
2. `python3 scripts/p7-secrets.py /absolute/restricted/new-secrets` 创建新目录及随机凭证，拒绝覆盖；安装合法 full chain、匹配私钥、信任 CA bundle，分别命名 `tls_cert` / `tls_key` / `tls_ca`。系统公有 CA 也需提供经审核的 CA bundle，勿把 leaf 当作生产 CA。
3. secret 目录0700；密码0600；DSN/私钥由宿主操作者持有、组65532、0640。证书/CA可0644。Compose 本地 secrets 是文件 bind mount，不保证落实 uid/gid/mode 字段；必须在宿主设置并验证。API仅挂 API DSN/TLS；Worker仅挂 Worker DSN；owner DSN仅挂显式 tools profile。
4. `python3 scripts/p7-preflight.py` 只读检查 Compose、镜像 digest、独立身份、文件权限、证书链/主机名/至少7天有效期和 key pair。测试工件才允许 `--synthetic`；不能用该开关批准正式发布。
5. `docker compose -f deploy/release/compose.yaml config --quiet`；检查预期 project、卷、网络、端口与磁盘。确认没有指向现有数据库的数据路径。不要输出秘密文件、Docker 进程环境或完整 DSN。

## 新空库的显式顺序

以下用 `dc` 代表 `docker compose -f deploy/release/compose.yaml`，实际先配置文档中的环境变量。每一步失败即停；禁止 API 启动时自动迁移。

```bash
dc(){ docker compose -f deploy/release/compose.yaml "$@"; }
dc up -d --wait postgres
dc exec -T postgres sh /opt/rai/bootstrap.sh
dc run --rm --no-deps admin migrate
dc run --rm --no-deps admin migrate-status
dc exec -T postgres psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 \
  -f /opt/rai/roles.sql -f /opt/rai/runtime-grants.sql
dc up -d --wait api
dc exec -T api /rai probe --ready
dc up -d --wait worker
dc exec -T worker /rai worker-ready
```

`dc` 需由操作者定义为函数或将其替换为完整 Compose 命令，不是系统自带命令。`bootstrap.sh` 为**新专用 cluster 一次性操作**，不幂等、不对既有 cluster 探测或修改；重复运行会拒绝角色冲突。数据库 owner 是非超级用户 `rai_owner`；`rai_bootstrap` 超级用户仅作容器内管理。迁移命令要求 owner 身份，已应用齐全时也拒绝 runtime 调用。角色变更不属于业务 schema migration；新增真实对象后应重新审核授权。

首次实际采集还需操作者明确执行 `dc run --rm --no-deps admin init`，将一次性设备凭证直接存入受限文件并安全转交手机 Keychain。它不替换现有 dataset；本轮只有合成 init。不能在终端日志、Git、聊天或普通监控中保存输出。以合成数据完成 handshake、批次 ACK/原回执、Worker publication 和 Effective/执行闭环 smoke，再考虑真机门禁。

## 服务生命周期

PG healthcheck 等最终 TCP server；API healthcheck 使用 `/ready` 而非 `/health`；Worker 依赖 API healthy，启动再次验证 schema16/运行角色。`worker-ready` 检查 DB/schema/role；它不是队列有进展的证据，应配合任务/租约监控。Compose unhealthy 本身不会自动重启容器；`restart: unless-stopped` 处理进程退出，外部监控处理持续 unhealthy，避免重启风暴。

停止使用 `dc stop worker api`，最后才 `dc stop postgres`。API SIGTERM 停止接入并给请求10s完成；容器15s grace。Worker取消计算、停止续租，不伪造完成；未结束的lease由后继Worker在到期后回收。PG grace60s。启动失败查稳定错误码、角色与readiness，不能用自动 migration 或 owner runtime 规避错误。

资源上限 API512MiB/1CPU、Worker1GiB/1CPU、PG2GiB/2CPU；PG shared_buffers256MiB/work_mem4MiB/max_connections40；Go memory limit384/768MiB、单 Worker slot。总常驻上限3.5GiB，保留 fnOS/文件缓存与瞬时管理进程余量。具体测量见 [Capacity](P7_CAPACITY.md)。Docker日志每服务10MiB×3，read-only scratch、非root、drop capabilities、no-new-privileges。

## 更新、回退与失败处理

发布清单必须包含 commit、源码差异摘要、Go/Swift/PG版本、migration13、实际镜像digest、protocol2、OpenAPI1.1.0、算法和训练版本；`rai version` 提供服务内版本。未提交工作不能被表示成那个 commit 已包含的代码，本轮 evidence 额外记录内容摘要。

更新前需有已验证备份及离机副本，停止Worker/写入口、审核兼容、显式迁移，再按readiness恢复。00001–00016 forward-only、down白名单为空；旧schema12 binary对13 fail closed。回旧app digest还须确认启动命令、TLS/probe、API及算法兼容；P6镜像不支持P7原生TLS/worker-ready，不能仅凭同为schema16直接回滚。不能混跑不兼容Worker。需回到不兼容schema时，在**新隔离实例**恢复验证，再由用户批准切换；会丢失备份点之后的变更，不是无损普通回滚。

证书或 DSN 文件原子替换后，文件 bind mount 可能仍引用旧 inode。预检新文件后用受控 `up -d --force-recreate api`（DB凭证则还需Worker）装入新挂载，验证证书指纹/hostname/readiness与原身份重试；只 restart 不保证读到新文件。避免同时重启PG。正式入口切换、部署、迁移真实数据、App发布和Git提交均不由这些说明自动授权。
