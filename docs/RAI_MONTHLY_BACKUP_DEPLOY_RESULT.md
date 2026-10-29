# RAI 月度云备份部署准备与最小恢复审查结果

2026-10-29，Asia/Shanghai；本轮不是P8。实际HEAD与上一轮相同，但工作区状态不同，不能把上一轮的“入口干净”沿用到本轮。

```text
BACKUP_DEPLOY_BASE_COMMIT: 09fb5bb38d32cdbe42c757277ab2431995818ac4
BRANCH: main
MONTHLY BACKUP CODE: PASS WITH RISKS
REAL CLOUD BACKUP: PENDING CONFIGURATION
MONTHLY SCHEDULER: PENDING CONFIGURATION
MONTHLY MONITORING: PASS
MINIMAL DISASTER RECOVERY REVIEW: COMPLETE
DISASTER RECOVERY IMPLEMENTATION: NOT STARTED
DISASTER RECOVERY RESYNC: BLOCKED
P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
PRODUCTION RELEASE READY: NO
```

云端目录创建/真实上传/删除旧云副本/正式cron另为 **PENDING USER APPROVAL（用户任务A4）**；本轮没有提出猜测目标的写入命令、安装正式任务或操作真实数据库。MINIMAL REVIEW COMPLETE表示审查完成，当前两种路径的真实手机恢复执行仍BLOCKED。

## Git与保护边界

- `pwd=/Users/ta/rai`，已检查status/branch/HEAD/diff/log5；P7已提交为09fb5bb，上一轮备份专项未提交。入口已有README、Architecture、Implementation、p7-workload及test-p7-compose五个tracked修改，以及月度备份/恢复脚本、文档、Go/Swift边界用例等untracked工作。
- 记录入口文件SHA，保留已有工作；本轮增量集中在备份check/版本核实/操作日志、月度日历策略、P7监控适配、对应脚本测试与文档。没有猜测这些未提交文件的作者，也不声称已确认没有其他人编辑。
- Go/Swift/合同/00001–00013/原始方案没有本轮修改；不重启Recovery Resync开发，不添加generation、第二同步流、P4/P5改造或P6 UI。P0–P7历史结果和上一轮机器证据未改。未reset/clean/commit/push；旧`../running`未访问或修改。

## TASK A：真实环境只读检查与准备

fnOS通过已有固定host key的`rai-fnos`连接只读检查，2026-10-29 10:48上海时间：Docker28.5.2、Compose5.1.0；只有`rai-postgres`（project `rai`、service `postgres`、镜像18.6）和`frpc`。容器配置仅提取DB/role名字：均为`rai`；数据挂载是bind `/home/tpandmr/rai/pgdata`→`/var/lib/postgresql`，不是本轮新V2卷。**没有连接该旧库、查询其schema或健康事实，也没有pg_dump它。** 正式V2的database/role/schema/volume身份尚未建立，不能把P7隔离实例或此旧容器当作正式源。

`/home/tpandmr/rai/scripts/rai-backup.sh`不存在，`/home/tpandmr/rai-v2`不存在。实际源码脚本在本仓库`scripts/`；没有正式安装路径。home分区119,114,891,264B、`/vol1`885,667,864,576B可用，仅为检查时刻观测。旧系统其他备份路径未全面盘点，不读取旧`.env`或猜测目录。

cron active、宿主Asia/Shanghai；当前用户`crontab -l`返回无任务，可读系统crontab/cron.d和timer未见RAI备份。其他用户/不可读任务未完全验证，安装前管理员仍须检查重复任务。只采用cron一种模板；没有安装systemd timer。模板`0 3 1 * *`，预期下一计划 **2026-11-01 03:00:00+08:00**；这是按日历推算，未安装任务没有实际调度next-run证据。

本机与fnOS本次非交互环境均无RAI_BACKUP_SSH_HOST/PORT/USER/KEY/REMOTE_DIR；本机虽有通用cloud/cloud-u别名，但用途、远端专用目录、权限、空间、备份身份和真实源均未确认。用户答复“不清楚”。没有用这些别名猜测目的地，未读取私钥、密码或token；真实云连接/host key/账户/目录权限/空间/上传/下载均 **NOT VERIFIED**。

已准备的[月度Runbook](RAI_MONTHLY_BACKUP.md)包括正式V2身份核对、受限环境/SSH配置、只读连接、目录归属/磁盘检查、backup/check/list、取回三件套、隔离恢复、人工批准及单一cron安装/事后检查命令。真实目录和凭证缺失，命令使用明确绝对路径占位符，未伪造可直接执行的生产配置。

月度入口仍复用P7只读角色整库`pg_dump -Fc`与既有双锁、NOFOLLOW、0700/0600、严格SSH、远端大小/SHA重读、manifest最后原子发布、成功后三份保留。未引入备份加密、新数据库或常驻平台。新增真实source PG18校验并记录server_version_num，避免仅凭metadata写死18。归档包括全部业务表，不只Workout。

本轮隔离SSH/SQL已完成，完整Compose还包含后续回归与资源清理；真实云恢复保持未执行，不从合成链路推导真实验收。

## TASK B：月度监控适配

原P7 daily metadata/26h规则已审查。新增共享月度日历策略，固定上海每月1日03:00＋24h宽限；宽限内仍要求满足上月计划，按成功归档的snapshot起点检查，不用30天。check输出NO_BACKUP/BACKUP_OK/BACKUP_OVERDUE/BACKUP_FAILED/BACKUP_CORRUPTED/REMOTE_UNREACHABLE，异常非零；本地最后尝试原子记0600文件，手动成功重试解除失败。

`p7-monitor.sh --monthly`现场调用月度check，再聚合既有API/Worker/TLS/磁盘/队列规则；`p7-alerts.py --backup-policy monthly`不套26h且拒绝10分钟前的check证据。旧显式daily入口保持兼容，月度运维仅使用--monthly。合成控制证明六类状态、过旧/缺证据、所有旧告警仍fail closed。无新监控服务、通知或自动恢复。大备份的完整SHA检查建议低频/手动，未安装任何监控任务。

JSON日志含time/operation/status/duration_seconds/backup_filename/backup_size/error_code；没有健康载荷、DSN、私钥或token。manifest发布后的响应/本地日志失败可能已完成三份保留，必须check/list澄清，不能宣称所有历史文件未变。进程强杀的未确认尝试仍需人工看日志/文件，不承诺完整持久故障监测。

## TASK C：只读比较A/B

[完整最小审查](RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md)逐项回答Anchor、candidate/delivered、Outbox/GC、设备/数据集、seq/batch/receipt、墓碑与Route seal。A保留备份内训练历史，但旧身份接续缺删除/回执/进度协调，不能用小补丁安全接续。B的原新库Init/P3/P4/P5能力可复用，但当前iOS固定Outbox/背景session/Product journal不能直接安全换身份。

按用户接受损失与简单维护的偏好，建议以后优先验证B的显式独立客户端会话切换，预计无需新服务端wire协议；本轮不实施。当前两种真实恢复都BLOCKED；SQL恢复到隔离实例可执行，之后必须保持真实手机写入隔离。旧完整generation设计不是本轮必做项。

## 本轮验证

| 项目 | 本轮真实结果 |
|---|---|
| 文件/锁/权限/原子发布/SHA/三份/失败不清理 | Python12项通过；fnOS同样重跑 |
| 月度日历/28–31天/闰年/跨年/宽限/状态/P7适配 | Python7项通过；逐月覆盖2024–2028及2100年，72个月；fnOS重跑 |
| 原P7每日11类告警、历史失败解除、缺证据 | 通过；每日兼容与月度显式入口分开 |
| 定时错窗与下一计划 | 只运行隔离受控配置，非月度窗口返回75；未连接云/数据库；不算真实月度触发 |
| Go普通/race | fnOS 183/183项通过，fail/skip=0 |
| 真实PG18/race integration | 365个pass事件、fail/skip=0；integration包314.342s（包含单元/子测试事件，不是365个PG场景） |
| gofmt/mod verify/vet/build/边界 | 通过，integration/scale/p7capacity vet及249包边界通过 |
| Swift核心（无UI截图） | 41项通过；含Scope拒绝、GC后payload缺失、pending/部分ACK重启；无Swift代码变更 |
| 真实loopback SSH与月度P7监控 | 5次成功、最终3份；断连/超时保留旧集合；失败check与成功重试解除、原Worker停止告警保留且无备份误报通过 |
| 全新独立PG18实际月度SQL恢复 | PASS：2,671,594B，SHA256 `24037c8bd98804615aa27db21f2234a24a424bdf9c97105b7921fd504b1c8103`；45表count/hash相同，schema13、FK/不变量、原P3回执和P5 Override/Effective/Execution/Feedback通过；目标清理exit0 |
| 完整Compose/故障/soak/资源清理 | PASS/exit0；原P7故障/加密恢复、195.134s/144读/6增量/6次确认重生成、零错误、三轮生命周期、私有256MiB ENOSPC通过；月度/P7清理exit0，事后仅原rai-postgres/frpc保留 |
| 真实云上传→下载→新PG恢复 | 未执行，PENDING CONFIGURATION及USER APPROVAL |
| 正式cron/真实月度触发/真实iPhone恢复 | 未执行 |

开发失败轮保留：首次本机fake docker使用全命令1s超时，进程启动超预算导致source_schema timeout；将正常fixture命令预算设5s，仅真实timeout注入仍1s，重跑通过。一次编辑缩进错误由py_compile/本地测试发现，修正后再进行fnOS验收；不计入最终PASS。没有放宽SSH验证、文件保护、receipt/seq/tombstone或真实数据库验证。

可复现命令与机器结果见 [evidence](evidence/monthly-backup-deploy-2026-10-29.json)。主要命令：

```bash
python3 scripts/test-monthly-backup.py
python3 scripts/test-monthly-policy.py
python3 scripts/test-p7-alerts.py
scripts/test-remote.sh bash -c 'set -e; python3 ../../scripts/test-monthly-backup.py; python3 ../../scripts/test-monthly-policy.py; bash ../../scripts/check-p7.sh; bash ../../scripts/test-monthly-compose.sh'
# 仅Swift核心，不重跑P6 UI
cd ios/RAI
swift test
```

## 未完成项与安装批准清单

1. 正式V2部署/审核版本与安装目录、project、完整容器ID、DB/role/schema/volume确认。
2. 云端host/port/user/key/可信host key/专用非公开目录/剩余空间，仓库外0600环境文件；不可沿用猜测值。
3. 用户任务A4要求的真实目录、首次上传和受管旧副本清理批准；获批后才执行手动backup/check/list。
4. 获批真实取回并实际新PG隔离恢复/不变量/API验收；生产规模数据量、时间和RPO/RTO签收。
5. 真链路通过后，审核唯一cron与日志路径、明确批准安装，再查实际条目/身份/时区/cron服务/下一计划；不改其他任务。
6. 两种方案当前真实恢复仍BLOCKED；B客户端会话切换只建议未来单独开发及真机验收。原P3/P6真机、HealthKit覆盖/删除/GPS/后台/强杀/真实网络继续未验证，发布保持NO。

未覆盖生产库、未创建/删除真实云文件、未安装正式任务、未新增常驻服务、未迁移真实数据、未改历史migration/P3/P4/P5/P6、未自动commit/push。
