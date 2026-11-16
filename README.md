# RAI

个人自用的 iOS 跑步 App：HealthKit → 服务端（Go + PostgreSQL）→ 分析与训练建议 → App 展示。

底部为「首页 / 运动 / 训练 / 统计 / 我的」。首页聚焦跑量、今日训练和最近运动；统计按周、月看跑量与配速、心率分区；
运动模型（准备度、跑力、训练负荷、恢复）从首页与统计进入。数据不足时留空，不补成 0。

- 现在怎么工作：[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 接口：[docs/API.md](docs/API.md)
- 每次上线了什么：[CHANGELOG.md](CHANGELOG.md)
- 工程约定：[AGENTS.md](AGENTS.md)
- 原始设计方案（保留原文）：[RAI-Go-PostgreSQL架构重设计与实施方案.md](RAI-Go-PostgreSQL架构重设计与实施方案.md)；
  各专题方案为根目录 `RAI-*.md`；历史资料在 `docs/archive/`、`scripts/archive/`（只读）

所有模型参数都是未经真实数据校准的设计值。

## 本机跑起来

首次准备工具链（Go、PostgreSQL 18 都装在 `.local/` 下，不需要 sudo）：

```bash
scripts/local/bootstrap.sh
```

日常检查：

```bash
scripts/local/test.sh quick          # Go 单测 + vet + 依赖边界
scripts/local/test.sh swift          # iOS 包单测（SyncCore / ProductCore）
scripts/local/test.sh simulator      # App 模拟器回归
scripts/local/test.sh training-ui    # 自适应计划 UI → 本机 API/Worker → 一次性 PG18
scripts/local/test.sh integration    # 隔离 PG18 集成测试（改 SQL/事务时跑）
scripts/local/test.sh gate           # 完成门禁：race + vet + 边界 + admin 迁移
scripts/local/test.sh full           # 全量：uncached race、sqlc diff、全量隔离 PG18 回归
```

本机起一个后端供手机／模拟器联调：`scripts/local/stack.sh`（见脚本头部说明）。

## 真机验收

设置写在 `.local/device.json`（不入库；样例 [scripts/local/device.example.json](scripts/local/device.example.json)），
可用键：`run`（独立状态目录）、`pgPort`、`httpPort`、`host`、`derived`、`configuration`。临时改某一项时，
对应的 `RAI_LOCAL_DEVICE_*` 环境变量仍可覆盖。连上 USB iPhone 后直接运行：

```bash
python3 scripts/local/device.py install
python3 scripts/local/device.py probe    # 仅验证 iPhone → Mac 与数据集身份
python3 scripts/local/device.py daily    # 只读 HealthKit，一次有时间预算的日常健康同步
python3 scripts/local/device.py report   # 只输出状态、锚点/待办计数和版本计数
python3 scripts/local/device.py training # 打开训练页面，使用独立命令日志和缓存
```

`training` 不请求 HealthKit 授权，也不读正常会话凭证；真实原始载荷只保存在受保护的手机状态和本机 PG 中。
同步轮次结束不代表回溯已完整，待办会留到下轮继续。只读结构诊断：`python3 scripts/local/device.py structure --workout <uuid>`
（只输出点数、采样间隔与时间戳摘要，不导出心率值）。没有手机时用 `scripts/local/test.sh training-ui`，但模拟器不替代真机验收。

## 把手机连上服务器

服务器用 `rai init`（或 `rai admin new-dataset`）生成凭证文件后，在电脑上运行：

```bash
python3 scripts/connect-link.py /绝对路径/凭证文件.json
```

把输出的 `rai://connect?…` 链接用 AirDrop / 备忘录发到 iPhone 并点开，确认服务器域名即可。
链接里含设备令牌，用完即删，不要贴到聊天或工单里。也可以复制链接后在 App「我的 → 服务器与健康授权」点「粘贴连接链接」，或在同一页「手动填写」。
首次打开 App 时首页的「开始使用」卡片会依次引导：连接服务器 → 授权读取健康数据 → 填写最大心率。

连接后，新的跑步在跑完约 1 分钟后自动上传（约 30 分钟后再补传一次晚到的数据）；更早的历史在
「我的 → 跑步记录同步」点「补传历史记录」。

## 部署

发布模板在 `deploy/release/`（compose：postgres + api + worker + model-activate + admin），本机／NAS 自部署见
`deploy/local/README.md`。API 与 Worker 同一镜像；API 不自动迁移、不自动创建数据集。部署后在 CHANGELOG 加一行。

模型在部署后自动激活：一次性服务 `model-activate`（owner 角色）在 Worker 健康后运行
`rai analysis model-stack auto-activate`，登记镜像内置的模型栈，等当天结果和近 365 天历史回填完成后切换（保留 previous）；
已是该栈时什么都不做。默认最多等 2 小时，可再次运行，进度保留。超时、计算或回填失败
（`history_backfill_paused`，用 `rai analysis backfill resume` 重试）、或检测到旧镜像回部署时退出码非零，旧栈继续生效。
部署命令只启动了指定服务（如 `up -d --wait api worker`）时，再执行一次 `docker compose run --rm model-activate`。
回滚仍需手动 `rollback-real`。约束见 [模型发布兼容性](docs/ARCHITECTURE.md#模型发布兼容性)。
