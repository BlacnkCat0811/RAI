# RAI

个人自用的 iOS 跑步 App：HealthKit → 服务端（Go + PostgreSQL）→ 分析与训练建议 → App 展示。

界面采用暖白底、深色数字与浅青／柠檬黄点缀，底部为「首页 / 运动 / 训练 / 统计 / 我的」。
首页聚焦跑量、今日训练和最近运动；统计按周、月查看跑量趋势及配速、心率分区的距离占比，运动模型仍可从首页与统计进入。
缺失记录或未覆盖的分区不补成零强度，也不以整次平均配速或平均心率代替分区。

- 当前架构：[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- 心率设置简化（本地改动，尚未部署）：仅填写最大／静息心率，移除日期、依据和模式控件；填写后固定使用，静息留空则自动参考健康数据。跑步最大心率与稳定 HealthKit 静息值可提示采用，保存后从当天生效。
- 心率参考 v3（2026-11-15，已部署并激活，迁移 62）：分开测量日期与历史生效日期，取消手填静息心率的 90 天硬失效，明确自动／手动模式及估计来源。见 [设计与验收规则](docs/ARCHITECTURE.md#心率参考-v3-重设计已部署并激活2026-11-15)；新模型栈 `rai-v2-be2ffee8ea215e14` 已于 2026-11-15 切为 active（见 [部署记录](docs/ARCHITECTURE.md#部署记录--rai-coach-202611082026-11-15镜像-188eeb3迁移-62-与心率参考-v3-新栈激活)）；代码已接入 Go 分析、事务输入和 iOS，新设置页需重新构建安装 App 后才可见。
- 自适应训练计划（`training_plan/v4`，迁移 60／61，已于 2026-11-15 部署；服务端与 `coach_parameters/v3` 就绪，App 内新建计划即用 v4）：参照佳明教练，有比赛目标时按比赛日期倒排基础／提升／巅峰／减量阶段，
  没有目标时按近期完成量、负荷、恢复与负荷焦点逐周自动排课；错过、多跑、准备度低、恢复未结束时周内自动调整；
  每次跑步课不少于 30 分钟。见 [ARCHITECTURE](docs/ARCHITECTURE.md) 与
  [优化方案](RAI-训练计划自适应排课优化方案-2026-11-13.md)。旧计划不迁移，App 中“改用自适应计划”新建。参数为未校准的设计值。迁移 61 用不可变的 `coach_parameters/v3` 保存近 8 个完整周中活跃周的时长中位数基线；迁移 60 与旧参数保留回放。
- 静息心率基线计入跑步当天开跑前的读数（无迁移，新栈 `rai-v2-d015a2025ccc6476`，已于 2026-11-15 部署并激活，同日由心率参考 v3 栈取代为 previous）：近 7 天漏戴手表时，当天晨间读数可补足“近 7 天 ≥3 天”，避免负荷因 `reference_missing` 为空。
- 跑步分析卡住修复（迁移 59，已于 2026-11-12 部署并激活）：支持数千个 HealthKit 单点序列，保留总点数预算；单条失败明确展示。旧模型 previous 停止排程，已失败任务保留审计。
- 生理恢复 v4：已实现最近 7 次有效观测均值及默认基准估计，支持漏戴手表时继续估计准备度；取消缺测扣分及估计模式 75 分封顶，保留估计标注与置信度。需要迁移 58，与训练效果同栈发布，已于 2026-11-12 部署并激活。
- 模型核查与优化方案：[准备度、恢复、跑力与负荷对照](docs/ARCHITECTURE.md#模型对照核查与优化实施方案已实施)（已实施，schema3 栈已于 2026-11-11 部署并激活；2026-11-12 起由 schema4 栈取代）
- 跑力 v1：[单次／长期跑力、四项能力、分距离预测、六区间与历史重算](docs/ARCHITECTURE.md#模型v2-日模型栈schema4-产物)，
  按 [跑力系统重设计方案](RAI-跑力系统重设计方案-2026-11-12.md) 实现于 schema4 栈（迁移 57），已于 2026-11-12 部署并激活
  （`rai-v2-c3441e6edc485d9e`）；参数均为未经真实数据校准的 RAI v1 设计值。iOS 详情展示跑力仪表、四项能力训练配速和基础指标
  （VO₂max、乳酸阈值配速）；乳酸阈心率尚无模型结果，明确显示暂未提供。
- 训练效果 v1：每次跑步的[有氧／无氧训练效果（0–5）](docs/ARCHITECTURE.md#模型v2-日模型栈schema4-产物)，按
  [有氧无氧训练效果模型设计与实施方案](RAI-有氧无氧训练效果模型设计与实施方案-2026-11-12.md) 实现于新栈 `rai-v2-69efefa730b47818`
  （与恢复 v4 合并发布，无迁移），已于 2026-11-12 部署并激活；参数只经合成课校准，真机间歇课对照未做。无氧评分需要同栈
  跑前已发布的跑力参考，换栈后只从激活日起逐日累积，因此历史跑步在无参考时显示不可用（`speed_reference_unavailable`）。
  iOS 详情页新增“训练效果”双仪表卡，原区间刺激卡改名“刺激类型”
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
scripts/local/test.sh training-ui    # 自适应计划 UI → 本机 API/Worker → 一次性 PG18
scripts/local/test.sh integration    # 隔离 PG18 集成测试（改 SQL/事务时跑）
scripts/local/test.sh gate           # 完成门禁：race + vet + 边界 + admin 迁移
```

模型发布兼容性回归（全部使用合成数据，在本机隔离 PostgreSQL 18 执行）：

```bash
scripts/local/test.sh integration -run '^Test(ModelRelease|ModelStackDay|DailyPageInvalidates|DailyHistoryPage|RunningFitness)' ./integration
scripts/local/test.sh swift --filter 'AthleteRefreshTests|AthleteModel|RunningFitness|WorkoutDetail'
```

上述定向检查不替代 `gate`，也不代表已完成真机或线上验收。

当前本机验证（2026-11-13，仅合成数据）：`gate` 已通过（含 18 个 admin 迁移集成测试），
训练／模型发布／日常健康／跑力相关的 20 个隔离 PG18 顶层集成测试通过；Swift 包 203 项通过。
App 模拟器回归 86 项通过（含跑力普通／辅助功能字号布局及独立连接身份／重连保护），6 项跳过；
跳过项为需专用 VO2／外部闭环／上传后端的独立验收测试，本轮未执行。
`training-ui` 两个 UI 场景已通过无目标建计划、设置半马目标、重算发布与课程详情的真实 TCP/API/Worker/PG18 流程；
独立训练入口另用一套测试数据库，验证原会话 manifest 不变、没有向正常后端发请求，报告不输出健康值。
这些结果不代表真实 HealthKit、连续自然日真机、自有数据校准或生产部署通过；v4 与静息心率新栈已于 2026-11-15 部署到云端并激活。

本次交付验证（2026-11-15，仅合成数据）：`gate` 与 `full`（uncached race、vet、依赖边界、`sqlc diff`、
真实隔离 PG18 全量回归）均通过；Swift 包 206 项通过；App 模拟器回归通过。
真机 HealthKit、新心率设置页的真机交互及本轮模型参数校准仍未执行。

本机真机验收准备（2026-11-13）：独立状态为 `.local/device-plan-v4-20261113-b`，PG18 仅监听本机 55434，
API 使用 Mac 私网 18084，schema 61；API/Worker readiness、真机 Debug 包签名及本机主机配置均已验证。
目前无可用 USB iPhone，尚未安装、执行手机连通检查或读取 HealthKit；自然日观察尚未开始。
原云端及旧本机验收状态保持不变；云端已于 2026-11-15 先后部署 `dd5b365`（迁移 61）与 `188eeb3`（迁移 62），
当前 active 模型栈为 `rai-v2-be2ffee8ea215e14`（见
[ARCHITECTURE 部署记录](docs/ARCHITECTURE.md#部署记录--rai-coach-202611082026-11-15镜像-188eeb3迁移-62-与心率参考-v3-新栈激活)），
本机真机验收仍未执行。
继续本次验收时为 `scripts/local/device.py` 设置 `RAI_LOCAL_DEVICE_RUN=$PWD/.local/device-plan-v4-20261113-b`、
`RAI_LOCAL_DEVICE_PG_PORT=55434`、`RAI_LOCAL_DEVICE_HTTP_PORT=18084`，避免使用原验收目录。

真机连接后使用以下独立入口（均需上述环境变量）：

```bash
python3 scripts/local/device.py install
python3 scripts/local/device.py probe    # 仅验证 iPhone → Mac 与数据集身份
python3 scripts/local/device.py daily    # 只读 HealthKit，沿用 90 天回溯；一次有时间预算的同步轮次
python3 scripts/local/device.py report   # 只输出状态、锚点/待办计数和版本计数
python3 scripts/local/device.py training # 打开原有训练页面，使用独立命令日志和缓存，不切换正常会话
```

`training` 本身不请求 HealthKit 授权，也不读正常会话凭证；`daily` 与 `single` 复用独立 Outbox，
真实原始载荷仅保存在受保护的手机状态和本机 PG 中。同步轮次结束不代表回溯已完整，待办会留到下轮继续。
已退役的 Workout `completeness` v2 命令会在读取设备或凭证之前明确拒绝，不可用于当前协议验收。
没有手机时可运行 `scripts/local/test.sh training-ui`，但模拟器通过不替代真机和自然日验收。



本机起一个后端用于手机/模拟器联调：`scripts/local/stack.sh`（见该脚本头部说明）。

USB 真机只读结构诊断：`python3 scripts/local/device.py structure --workout <uuid>`。
开发版报告中的 `heartRateTiming` 直接查询 HealthKit 关联心率样本及序列子点，输出点数、采样间隔、微秒时间戳摘要与舍入误差，
并统计运动时间窗内的样本数；不导出心率值、不推进锚点、不上传运动。摘要按起止微秒排序后，以 `start:end\n` 拼接计算 SHA-256，可与本机验收库时间戳比对。

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

跑步详情配速区间使用个人训练前跑力 Z1–Z6（`pace-zones/2`），支持同源短间隔瞬时速度重建、明确覆盖率与未分类时长；独立于当天分析／心率发布，证据不足不伪装为零。具体口径见 `docs/API.md`。

配速区间 v3 **已部署（2026-11-15，镜像 `720d588`），未真机验收**：取消近 7 天历史发布前置条件，以训练发生时的有效证据生成个人参考；首次训练可使用明确标注的跑后估计，个人依据不足时仍展示实际配速分布。正常使用无需手动重算。见 [设计与验收规则](docs/ARCHITECTURE.md#配速区间-v3-重设计2026-11-15已部署未真机验收) 与 [接口](docs/API.md#配速区间-v3-契约2026-11-15-已部署)。旧 `pace-zones/2` 字段继续服务旧客户端；新字段与旧字段并存，重新安装 App 后使用新卡片。
