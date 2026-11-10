# RAI Monthly Cloud Backup & HealthKit Disaster Recovery Resync — Result

2026-10-29（Asia/Shanghai）。本专项不是 P8。实际命令与证据见本文件及 [机器证据](evidence/backup-recovery-2026-10-29.json)；未把设计或 mock 记为设备/数据库验收。

## 状态

```text
MONTHLY BACKUP IMPLEMENTATION: PASS WITH RISKS
CLOUD BACKUP INSTALLATION: PENDING CONFIGURATION
DISASTER RECOVERY RESYNC: BLOCKED
REAL DEVICE RECOVERY: DEFERRED / NOT VERIFIED
P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
PRODUCTION RELEASE READY: NO
```

`RECOVERY_BASE_COMMIT=09fb5bb38d32cdbe42c757277ab2431995818ac4`，分支 `main`。入口 `pwd` 为 `/Users/ta/rai`，`git status --short` 和 `git diff --stat` 为空；P7已经包含在基线，没有需保留的既有dirty变更。本轮未reset/clean/commit/push，全部改动保留供审查。P0–P7历史验收、00001–00013、冻结合同和原方案保留。

## 实际交付

- `rai-backup.sh backup|check|list` 与本地/远端 stdlib Python：P7只读整库custom dump、归档检查、双端锁、严格SSH、原子manifest、远端大小/SHA重读核验、最新三份成功副本保留；无额外文件加密。
- 月度环境/唯一cron模板、受保护的定时入口、明确IANA时区及错时拒绝。本轮只读检查fnOS cron active和Asia/Shanghai；没有安装任务或联系真实云备份目的地。
- `rai-restore.sh`：仅labelled、完整ID确认、同project API/Worker已停止、无其他连接、空public schema的PG18隔离目标；检查归档来源/schema/SHA，单事务恢复。禁止在线覆盖。
- 复用P7真实P3/P4/P5 fixture的SSH/新实例恢复演练，以及12项合成故障/文件安全测试。
- Go真实PG恢复边界与人工训练历史损失测试、两项Swift Outbox/Anchor边界测试。
- [Monthly Backup](RAI_MONTHLY_BACKUP.md)、[Disaster Recovery](RAI_DISASTER_RECOVERY.md)，README、Architecture、Implementation真实状态更新。

**未实现的部分**：Recovery generation/本地管理标记CLI、恢复流fencing、独立恢复查询进度、iOS恢复确认/进度/完成界面、自动回到正常增量。没有为未实现功能返回成功；没有第二套同步/分析/Coach，也没有改动正常Go/iOS运行代码。

## 验证证据

月度实际SSH演练通过：4次成功、最终保留3份、最旧移除；SSH失败和真实TCP无SSH banner超时后，远端文件集合及摘要不变。实际custom归档2,673,852字节，SHA256为`f21f4d97771d8eecdbd0751fefd98faa3d365de90babdb87e2c5f68f46cc0261`。另一新凭证/空卷PG18实际单事务恢复通过，45表计数与逐表摘要完全一致、schema13、FK/不变量0异常、P3原回执、P5原Override/Effective/frozen execution/feedback通过；错确认、未标记源实例、其他活动连接、非空目标拒绝。真实loopback SSH不等同真实离机/云端安装。所有数据库、SSH密钥/host key、TLS证书、HealthKit事件和用户训练记录都是合成测试数据。

已完成独立门禁：Python故障/文件安全12项PASS（7.091秒）；Swift41项PASS（含两项新边界）；macOS合成GC进程SIGKILL恢复PASS。最终fnOS Go普通183、race183、真实隔离PG/race命令365个test pass事件、fail/skip均0，integration包393.169秒；gofmt、mod verify、vet（含integration/scale/p7capacity）、build和249包在线/纯算法依赖边界通过。365为含单元及子测试的通过事件，不是365个独立PG场景。定时入口在本轮非月度窗口实测返回75，没有连接数据库或云端。

本轮稳定publication窗口soak已通过：195.507秒、144次读取、6次正式增量上传和6次逐轮确认的计划重生成，errors=0。读取median18.152ms/P95 46.106ms（nearest rank，合成HTTPS、共享fnOS环境），只描述这个有发布屏障的窗口；不是dirty期间可用性、真实手机或生产SLO。

最终完整Compose返回exit0，月度目标和P7总清理均exit0；额外真实256MiB隔离tmpfs ENOSPC、503/释放空间/原身份重试以及三轮API/Worker生命周期通过。没有对宿主盘或正式库注入故障。

已验证边界：

| 场景 | 实际结论 |
|---|---|
| 4次成功备份/第4次删除最旧 | PASS，文件级合成与真实SSH均验证 |
| dump失败、SSH失败、网络超时 | 拒绝并保留旧副本；具体注入与真实链路分开记录 |
| ENOSPC、短上传、SHA不符 | 远端helper注入ENOSPC/合成传输失败，旧三份逐字节保留；不声称实际填满云端磁盘 |
| 上传进程实际SIGKILL | 无成功manifest、旧三份保留，partial不进入成功集合 |
| 目录/祖先/文件symlink、foreign、duplicate、并发锁 | 不跟随、不删除未知文件、不覆盖旧身份，锁忙拒绝 |
| iOS delivered payload已回收 | Swift真实临时文件与重开state证明：delivered进度存在，原载荷/receipt缓存可以不存在 |
| iOS pending/无ACK/部分ACK/重开 | 原page/cursor/candidate/文件保留，未提前提交Delivered进度；未实现recovery UI状态机 |
| 旧PG快照身份及进度 | dataset/service/device/collector身份保持；客户端领先时sequence gap，旧receipt不存在 |
| Create→Backup→Delete→Restore | 旧事实仍在，备份后墓碑已丢失；明确BLOCKER，不根据HealthKit空查询删除 |
| Create→Delete→Backup→Restore | 已备份墓碑继续抑制旧UUID；已知删除的exact原载荷重放仍有效 |
| 原载荷存在的连续重放 | 同batch/seq顺序补入缺失事实，重试回执稳定、重复sample不增殖；不冒充完整recovery stream |
| 备份后5km→7km/执行完成/feedback | 旧快照只有5km/started，后来的7km与完成/反馈丢失；不能由HealthKit承诺重建 |
| 备份内override/Effective/execution重算 | 新Base/Effective追加，原override继续生效、旧Effective和执行snapshot原样保留 |

PG协议边界用真实静止template复制表示state A，然后在源库提交B；这是快照回退反例。**实际pg_dump→SSH→另一新PG实例的恢复由独立Compose演练完成**，两者不混称同一测试。

可复现命令：

```bash
python3 scripts/test-monthly-backup.py
scripts/test-remote.sh bash ../../scripts/check-p7.sh
scripts/test-remote.sh bash ../../scripts/test-monthly-compose.sh
scripts/test-remote.sh bash -c 'go test -race -count=1 -tags=integration -run "^TestDisasterRecovery" -v ./integration && go vet -tags=integration ./...'
cd ios/RAI
swift test
```

`check-p7.sh`包含Go普通/race、完整真实PG/race、vet（含integration/scale/p7capacity）、build、gofmt、模块和249包依赖边界。Compose在唯一项目/新卷上回归P3 ACK/Route/删除、P4真实Worker/publication、P5 override/execution/feedback、P7 TLS/runtime权限/故障/恢复；没有自动部署正式系统。

失败轮保留：初轮macOS临时路径含`/var` symlink，安全校验正确拒绝；修正测试fixture为真实绝对路径，没有放宽O_NOFOLLOW。初轮Go用例误把fixture返回的receipt当wire，触发payload_hash_mismatch，改为明确保留原gzip后通过。首轮真实SSH测试的authorized_keys位于`/tmp`祖先，被StrictModes拒绝；改到新建home私有目录，StrictModes和host key验证保持开启，再重跑。失败轮不计最终PASS。

第二轮月度SSH/SQL恢复通过后，原P7附加soak在`errors==0`断言失败；旧脚本没有保留HTTP错误码，具体原因不能事后伪造。P4 current API按合同会隐藏dirty publication；本轮给合成workload增加脱敏状态/错误码、失败分类，并在每轮真实同步/重生成后明确等待新Base发布、验证7km override仍在、确认P4/P5 current可读后再进行下一稳定读取窗口。保留144次并发读、6次正式增量和6次逐轮核验重生成、零读取错误断言；没有将404当成成功。这个新口径验证stable-publication读取，不证明dirty期间永远可读或生产SLO；历史失败记录保留，不能用新口径消除其未留码的事实。

## 最终门禁

以下是工程验收项；真实云安装/调度和真实手机分别列为DEFERRED，不能从模板或合成链路推导完成。

| BACKUP | 状态 |
|---|---|
| 每月一次配置、pg_dump -Fc、完整业务库、无额外加密 | PASS（实现/模板及隔离演练；实际schedule未安装） |
| SSH、云端不需PG、0700/0600、原子发布、大小/SHA | PASS（实际loopback SSH/新PG恢复） |
| 最新三份、失败不清理、手动backup/check/list | PASS，12项合成测试及真实SSH通过 |
| 隔离新PG实际restore、45表/FK/sync/tombstone/receipt/P4/P5 | PASS（实际loopback SSH/新PG恢复） |
| 真实云服务器目录/SSH身份/权限/取回 | DEFERRED / PENDING CONFIGURATION |
| 正式cron安装及实际月度触发 | DEFERRED；错时拒绝已实测 |

| RECOVERY | 状态 |
|---|---|
| Server Recovery Identity/普通重启不触发/显式恢复检测 | BLOCKED，当前无新代际实现 |
| 阻止旧Anchor错误推进/用户确认恢复/历史重读 | BLOCKED；未添加reset或伪恢复入口 |
| 保留原HealthKit UUID/Workout与Sample去重 | P3原能力回归；自动recovery重新导入BLOCKED |
| Route恢复完整/新代际receipt/stream确认 | BLOCKED；仅原P3协议回归，不冒充恢复通过 |
| Outbox不静默丢失/失败重试/重启 | 原Journal边界PASS；恢复上下文/协调BLOCKED |
| Tombstone已备份屏障/备份后丢失屏障 | 前者原P3 PASS；后者明确BLOCKED |
| 恢复完成回正常增量 | BLOCKED，无可证完成条件 |
| P4/P5原分析/计划再生成 | 原算法及发布回归；recovery触发BLOCKED |
| 已恢复Override继续保留/历史不被改写 | PG快照测试PASS；实际dump恢复另列 |
| 丢失手动修改/执行/反馈已说明 | PASS，明确不能从HealthKit恢复 |

| REGRESSION | 状态 |
|---|---|
| P3/P4/P5/P6 API/P7安全、Go普通/race、vet/build、PG integration | PASS，普通183/race183/真实PG race365；vet/build/gofmt/mod/249包边界通过 |
| Swift | PASS，41 tests；无真实HealthKit验收 |
| Compose | PASS，最终完整命令exit0；月度SSH/SQL、P7 TLS/角色/原加密恢复、实际SIGKILL/网络/ENOSPC、三轮生命周期和有发布屏障soak；资源清理exit0 |
| P6完整UI/设备/性能、恢复确认/进度/取消/完成UI | DEFERRED / NOT VERIFIED；本轮未改UI |

| SAFETY | 状态 |
|---|---|
| 未操作生产库、未清空真实iPhone Anchor、未删除正式Outbox | PASS |
| 未改旧migration/原方案/历史结果、未部署/切入口 | PASS |
| 未访问或改动正式云备份文件、未安装正式schedule | PASS |
| 未commit/push、未改旧running、未迁移真实数据 | PASS |
| 无新常驻服务/复杂备份平台/第二套在线生产PG | PASS；额外PG仅为可清理的隔离演练实例 |

## 剩余风险与停止点

恢复重同步的核心blocker及下一步评审设计见 [Disaster Recovery](RAI_DISASTER_RECOVERY.md)。旧恢复点之后的删除不能从当前可见对象集合完整反推；P7 GC后的exact replay也没有保证。没有为了通过测试清空状态或绕过协议。

真实云安装、远端磁盘/权限/取回、正式cron与生产规模RPO/RTO、HealthKit权限/历史/删除/Route/GPS、iPhone后台/force-quit/网络、iOS18运行及P6真机继续未验收。P7旧宿主监控仍按每日加密metadata/26小时阈值，未接月度JSON；本专项用check/list与退出日志，不声称安装了月度freshness告警。P7 ENGINEERING STATUS保持PASS WITH RISKS，DEPLOYMENT PREPARATION保持PARTIAL，PRODUCTION RELEASE READY保持NO。

完成代码、隔离验证、文档和证据后停止。未自动开始P8、生产恢复/部署、手机状态变更、Git提交或推送。
