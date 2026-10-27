# fnOS SSH 与数据库隧道

本机 macOS 的 `~/.ssh/config` 已配置独立别名，未保存密码，已有 cloud 配置保留。

```bash
# 登录 NAS：<SSH_HOST>:8022，用户 <SSH_USER>
ssh rai-fnos

# 开启隧道，保持终端打开，Ctrl-C 关闭
ssh rai-fnos-db

# 另一个终端检查本地端口（仅证明端口监听）
nc -vz 127.0.0.1 15433
```

首次连接先通过可信渠道核对 NAS SSH 主机指纹，再接受主机密钥。在终端输入 fnOS 密码，不发到聊天、不写入仓库。隧道只监听回环地址，端口被占用时启动失败；断线后重新运行，没有后台自启。

本机为 fnOS 使用专用密钥 `~/.ssh/id_ed25519_rai_example`（`IdentitiesOnly yes`），并在核对后将 ED25519 主机密钥固定为 `<VERIFIED_HOST_KEY_FINGERPRINT>`。免密登录须先把该公钥加入 NAS 的 `~/.ssh/authorized_keys`；未安装时 `BatchMode` 会直接失败，不会回退到密码。

| 连接方式 | 客户端 SSH 页 | 数据库页 |
|---|---|---|
| 使用上述外部隧道 | 关闭客户端 SSH | Host `127.0.0.1`，Port `15433` |
| 客户端内建隧道 | Host `<SSH_HOST>`，Port `8022`，User `<SSH_USER>`，密码认证 | Host `127.0.0.1`，Port `5433` |

用户提供的库名和数据库用户均为 `rai`。密码由用户自行从 NAS 秘密配置获取，不读取或输出整个 `.env`。两种方式选一种。认证、转发和数据库版本需实际连接验证，配置通过不代表连接成功。

远端 `rai` 库按可能包含真实健康数据处理，不设为项目默认连接，不执行查询、migrate、init 或测试。开发仍按 [README](../README.md) 使用本机 `55432` 的独立 `rai_dev`／`rai_test` 合成数据库。NAS 开发库须另行隔离库、角色和合成数据。

本地 SSH 配置不随仓库分发。Windows 可执行：

```powershell
ssh -p 8022 -N -o ExitOnForwardFailure=yes -L 127.0.0.1:15433:127.0.0.1:5433 <SSH_USER>@<SSH_HOST>
# 另一个 PowerShell
Test-NetConnection 127.0.0.1 -Port 15433
```

## 在 fnOS/N100 上跑测试

SSH 隧道只把数据库端口带到本机，`go test` 仍在本机执行，不能降低本机 CPU 负载。要把测试放到 fnOS 上，需要免密 SSH（BatchMode）和远端可用的 Docker：

```bash
# 本机工程根目录；默认主机别名 rai-fnos
scripts/test-remote.sh

# 只跑指定用例；参数原样传给远端命令，工作目录是 apps/server
scripts/test-remote.sh go test -race -count=1 -tags=integration -p 1 -run '^TestCommitRetryAndDeletionBarrier$' ./integration

# 最大页/规模专项；N100 比本机慢，放宽超时
scripts/test-remote.sh go test -race -count=1 -tags=integration,scale -p 1 -run '^TestScale' -timeout=30m ./integration
```

每次运行都会在远端新建独立临时目录和一次性 PostgreSQL 18 容器；默认优先使用远端的 Go 1.27.3 + gcc 直接跑原生 Linux 测试，缺少 Go 或 gcc 时才回退到一次性 Go 容器。测试结束删除容器与目录。并发运行使用不同的运行 ID，不会共用数据库。脚本**不连接**现有的 `rai-postgres`／`rai` 库，也不读取 `/home/<SSH_USER>/rai/.env`；测试数据全部是合成数据。

N100 实测（2026-10-27）：完整 `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 的 integration 包 101.450 秒；`TestScale` 专项 300.714 秒；`go vet -tags=integration,scale ./...` 与依赖边界检查通过（246 个包）。PostgreSQL 容器实查 `server_version=18.6`、`fsync=on`、`full_page_writes=on`、`synchronous_commit=on`、`data_checksums=on`、`shared_buffers=128MB`。

N100 是 4 核低功耗机器（约 8 GB 内存），默认 `GOMAXPROCS=2`；同时跑两个 `-race` 套件已接近上限，`scale` 专项建议单独运行，不要用它做并发压测。

首次运行只需拉取 `postgres:18.6`；若回退到 Go 容器，还会拉取 `golang:1.27.3-bookworm`。可用环境变量覆盖：

| 变量 | 默认 | 用途 |
|---|---|---|
| `RAI_REMOTE_HOST` | `rai-fnos` | SSH 主机别名 |
| `RAI_REMOTE_BASE` | `rai-remote-test` | 远端临时目录根（相对 home 或绝对路径） |
| `RAI_TEST_RUNTIME` | `auto` | `host` 用远端 Go，`container` 用 Go 容器 |
| `RAI_TEST_PG_IMAGE` | `postgres:18.6` | 一次性 PostgreSQL 镜像 |
| `RAI_TEST_GO_IMAGE` | `golang:1.27.3-bookworm` | 仅容器模式的 Go 镜像 |
| `RAI_TEST_CPUS`／`RAI_TEST_GO_MEMORY` | `2`／`3g` | 仅容器模式的 Go 限额 |
| `RAI_TEST_GOMAXPROCS` | `2` | Go 的 GOMAXPROCS |
| `RAI_TEST_GOPROXY` | `https://proxy.golang.org,direct` | 模块代理；网络受限时可改为可用镜像 |
| `RAI_TEST_PG_MEMORY` | `1g` | PostgreSQL 容器内存限额 |
| `RAI_USE_RSYNC` | `1` | 设为 `0` 时改用 tar 同步 |
| `RAI_KEEP_REMOTE` | `0` | 设为 `1` 保留失败现场，便于排查 |

前置条件：本机公钥已安装到 `<SSH_USER>@<SSH_HOST>:8022`，远端用户可执行 `docker`；host 模式还要求远端 `go version` 为 `go1.27.3` 且有 `gcc`。SSH 主机密钥按首次核对结果固定；不要用 `StrictHostKeyChecking=no` 绕过。

