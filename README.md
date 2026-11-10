# RAI

个人自用的 iOS 跑步 App：HealthKit → 服务端（Go + PostgreSQL）→ 分析与训练建议 → App 展示。

- 当前架构：[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 接口清单：[docs/API.md](docs/API.md)
- 工程约定：[AGENTS.md](AGENTS.md)
- 原始设计方案：[RAI-Go-PostgreSQL架构重设计与实施方案.md](RAI-Go-PostgreSQL架构重设计与实施方案.md)（保留原文）
- 历史阶段文档、验收证据与旧 README：`docs/archive/`、`scripts/archive/`（只读参考，以代码为准）

## 本机跑起来

首次准备工具链（Go、PostgreSQL 18 都装在 `.local/` 下，不需要 sudo）：

```bash
scripts/local/bootstrap.sh
```

日常检查：

```bash
scripts/local/test.sh quick          # Go 单测 + vet + 依赖边界
scripts/local/test.sh swift          # iOS 包单测（SyncCore / ProductCore）
scripts/local/test.sh integration    # 隔离 PG18 集成测试（改 SQL/事务时跑）
scripts/local/test.sh gate           # 完成门禁：race + vet + 边界 + admin 迁移
```

本机起一个后端用于手机/模拟器联调：`scripts/local/stack.sh`（见该脚本头部说明）。

## 把手机连上服务器

服务器用 `rai init`（或 `rai admin new-dataset`）生成凭证文件后，在电脑上运行：

```bash
python3 scripts/connect-link.py /绝对路径/凭证文件.json
```

把输出的 `rai://connect?…` 链接用 AirDrop / 备忘录发到 iPhone 并点开，确认服务器域名即可。
链接里含设备令牌，用完即删，不要贴到聊天或工单里。也可以在 App「我的 → 同步与诊断 → 手动连接」填写。

连接后：授权读取健康数据，新的跑步记录会在跑完约 10 分钟后自动上传；更早的历史在
「同步状态」页点「补传历史记录」。

## 部署

发布模板在 `deploy/release/`（compose：postgres + api + worker + admin），本机/NAS 自部署说明见
`deploy/local/README.md`。`rai` 二进制模式：`api` / `worker` / `migrate` / `init` / `admin` / `ops`。
API 与 Worker 同一镜像；API 不自动迁移、不自动创建数据集。
