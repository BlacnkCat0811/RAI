# RAI

个人自用的 iOS 跑步 App：HealthKit → 服务端（Go + PostgreSQL）→ 分析与训练建议 → App 展示。

界面采用暖白底、深色数字与浅青／柠檬黄点缀，底部为「首页 / 运动 / 训练 / 统计 / 我的」。
首页聚焦跑量、今日训练和最近运动；统计按周、月查看跑量趋势及配速、心率分区的距离占比，运动模型仍可从首页与统计进入。
缺失记录或未覆盖的分区不补成零强度，也不以整次平均配速或平均心率代替分区。

- 当前架构：[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 模型核查与优化方案：[准备度、恢复、跑力与负荷对照](docs/ARCHITECTURE.md#模型对照核查与优化实施方案已实施)（已实施，schema3 栈已于 2026-11-11 部署并激活）
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

模型发布兼容性回归（全部使用合成数据，在本机隔离 PostgreSQL 18 执行）：

```bash
scripts/local/test.sh integration -run '^Test(ModelRelease|ModelStackDay|DailyPageInvalidates|DailyHistoryPage|ModelOptimization)' ./integration
scripts/local/test.sh swift --filter 'AthleteRefreshTests|AthleteModel|RunningAbilityDerived|WorkoutDetail'
```

上述定向检查不替代 `gate`，也不代表已完成真机或线上验收。

本机起一个后端用于手机/模拟器联调：`scripts/local/stack.sh`（见该脚本头部说明）。

## 把手机连上服务器

服务器用 `rai init`（或 `rai admin new-dataset`）生成凭证文件后，在电脑上运行：

```bash
python3 scripts/connect-link.py /绝对路径/凭证文件.json
```

把输出的 `rai://connect?…` 链接用 AirDrop / 备忘录发到 iPhone 并点开，确认服务器域名即可。
链接里含设备令牌，用完即删，不要贴到聊天或工单里。也可以复制链接后在 App「我的 → 服务器与健康授权」点「粘贴连接链接」，或在同一页「手动填写」。
首次打开 App 时首页的「开始使用」卡片会依次引导：连接服务器 → 授权读取健康数据 → 填写最大心率。

连接后：授权读取健康数据，新的跑步记录会在跑完约 1 分钟后自动上传（约 30 分钟后再补传一次晚到的数据）；更早的历史在
「我的 → 跑步记录同步」页点「补传历史记录」。跑步正文通过后台 URLSession 文件上传；历史最多并发 2 条（按服务端握手额度），
短期握手缓存供列表查询、跑步及日常健康通道共用。已交给系统的上传可在 App 挂起后继续，尚未构建的历史仍需 App 获得执行时间。

## 部署

更新后端镜像不会自动激活新模型栈。模型升级需先准备并验证候选栈，再按既有授权流程切换；支持的旧栈结果按冻结发布版本读取。
版本范围、激活与回滚约束见 [模型发布兼容性](docs/ARCHITECTURE.md#模型发布兼容性)。
“结果尚未就绪”“模型需要更新”“模型版本需升级”均有各自的判定条件，不能据此判断模型未激活；详见
[运动模型状态](docs/API.md#运动模型状态)。

发布模板在 `deploy/release/`（compose：postgres + api + worker + admin），本机/NAS 自部署说明见
`deploy/local/README.md`。`rai` 二进制模式：`api` / `worker` / `migrate` / `init` / `admin` / `ops`。
API 与 Worker 同一镜像；API 不自动迁移、不自动创建数据集。
