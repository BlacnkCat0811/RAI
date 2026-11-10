# RAI V2 Phase 7 Result

2026-10-29（Asia/Shanghai）。本轮只做生产工程开发、合成隔离演练与发布准备；没有上线。机器证据：[fnOS P7](evidence/p7-fnos-2026-10-29.json)、[逐表恢复](evidence/p7-restore-2026-10-29.json)。历史阶段合同、验收文件及原架构方案保留。

## 1. Status

**PASS WITH RISKS**。P7-A→H 的工程实现、真实隔离 PostgreSQL、TLS/runtime 角色、加密备份恢复、故障注入、容量与回归完成。正式环境现场准备未执行，部署准备为 **PARTIAL**；真机仍延期，生产发布为 **NO**。

## 2. Repository

`/Users/ta/rai`，`main`。开始时工作区干净，P6 已由用户提交。本轮改动留在工作区供审查；未 commit、push、部署、修改旧入口、迁移真实数据、开放公网或发布 App。`../running` 和既有 fnOS 数据库未读取或修改。

## 3. Base Commit

`P7_BASE_COMMIT=d11e7b29e339b3d33c84962611031a3f33a18a7c`，不是旧 P6_BASE_COMMIT。测试镜像的 source_commit 指向此基线；**P7 是该基线上的未提交增量**。evidence 额外记录运行源码/迁移/合同的 SHA256，不能把这个 commit 或本地 image ID 称为已发布 P7 版本。

00001–00013 最新版本为13，无新业务 migration，旧文件与基线逐字节相同；OpenAPI/P3/P4/P5/P6 合同和原方案未修改，旧 evidence 未改写。

## 4. Architecture

保持原生 iOS → HTTPS Go API → PostgreSQL18 ← 单 slot Go Worker；API/Worker 同镜像、模块化单体、pgx/v5。没有 Web/Watch/桌面客户端，没有增加缓存、消息系统、第二数据库或常驻 Python 服务。Python/SQL/shell 仅用于显式管理和合成测试。

同步仍以 dataset 行锁产生提交顺序，facts/删除屏障/提交日志/dirty/job/receipt 同事务；不依赖备份、全库摘要或全历史重算。`ops verify` 的全表 hash 只用于显式维护/恢复验收，未进入 application/httpapi 的依赖图。P4 fencing/原子发布、P5 Base/Override/Effective/Execution/Feedback 均保留。

## 5. Deployment

[Deployment Runbook](P7_DEPLOYMENT.md)、`deploy/release/compose.yaml`、`environment.example` 已准备并在独立测试 project 执行。

| 项目 | 实际实现与验证 |
|---|---|
| 服务 | 常驻 PG/API/Worker；admin/monitor 为按需 tools profile；Go 原生 TLS，无额外 proxy |
| 网络 | HTTPS 默认绑定回环；PG 无宿主端口，仅 internal database 网络；Worker 无入口 |
| 卷 | PG18 明确 PGDATA=/var/lib/postgresql/18/docker，volume=/var/lib/postgresql；独立项目/新随机凭证/新卷 |
| 权限/容器 | 应用非 root、read-only、drop ALL、no-new-privileges；日志每服务10MiB×3 |
| 顺序 | PG最终TCP ready→显式bootstrap/migrate13/grants→API TLS readiness→Worker；启动不自动迁移 |
| 探针 | health/live仅进程；ready检查DB/schema；Worker探针检查DB/schema/role，进展另由jobs监控 |
| 生命周期 | unless-stopped、独立start_period/timeout；API10s drain、容器15s，PG60s；三轮SIGTERM退出0及重启/原回执验证 |
| 资源 | API512MiB/1CPU，Worker1GiB/1CPU，PG2GiB/2CPU；常驻上限3.5GiB，保留系统/缓存余量 |

fnOS 实测 Docker28.5.2 / Compose5.1.0，PG18.6，fsync/full_page_writes/synchronous_commit/data_checksums 均开启。Compose 通过不代表正式入口已经可用。

## 6. Security

[Security](P7_SECURITY.md)。TLS最低1.2，证书链/hostname/key pair/期限预检；探针仍验证主机名和CA，没有 trust-all、InsecureSkipVerify 或 ATS 放宽。隔离私有CA的真实HTTPS链路通过；真实手机信任/正式PKI/续期未验收。

API、Worker、owner/migration、backup、monitor 使用独立身份；运行角色拒绝superuser/owner/CREATE/TEMP，迁移命令即使无pending也拒绝runtime身份。Route保留正式SECURITY DEFINER函数，禁止API直接写projection/sealed/receipt。实际LOGIN的42501越权测试、cross-dataset404、撤销后401及P4/P5受限角色闭环通过。备份角色只读整库，monitor无raw表读权限。

秘密通过Git外受限文件安装；随机新凭证，无默认生产密码。Compose本地secret bind mount的真实权限已核验，DSN/私钥为操作者owner、gid65532、0640，目录0700。Git源码secret审计通过；PG错误不记录语句/参数，HTTP日志改用规范路由模板，未知URL不记录用户原路径/query。既有body/admission/HTTP与SQL预算未放宽。轮换、撤销、续期、遗失恢复有可执行流程，但没有操作真实凭证。

## 7. Backup / Restore

[Backup/Restore Runbook](P7_BACKUP_RESTORE.md)。只读backup角色 `pg_dump -Fc --no-owner --no-acl` 的整库一致性快照覆盖45张表；立即经GPG AES256完整性保护加密，key独立0600文件，无长期明文dump。普通逻辑备份不称PITR。

最终演练在另一独立PG18空实例、**全新六类DB密码**上实际解密/单事务恢复，重新授予runtime权限；原device/batch/command身份来自备份。45表计数/SHA256完全一致，schema13、FK、tombstone、receipt/stream连续性、analysis publication、Base/Effective/override和execution均验证。恢复API以受限身份通过TLS readiness，原batch/override receipt、frozen execution与旧UUID suppression通过；queued job身份完整保留。

最终加密备份 **2320937 bytes**；恢复演练 **41s**，口径包含dump/加密、新实例启动、SQL恢复、核验及API smoke。错误key、密文损坏、即使替换SHA metadata后的损坏、未标记/非空目标都拒绝；失败目标仍为空。没有 --clean、覆盖真实库或自动数据回退。

设计RPO≤24h（成功快照起点且离机完成）、RTO≤60min；小合成库观测不能证明生产RTO。14日/8周/12月保留策略已说明，脚本不自动删备份。独立设备/故障域的加密副本与密钥分离策略明确，**离机复制/取回/定时任务 NOT VERIFIED**。原backup metadata的restore_tested=false保留，另附与密文SHA绑定的真实恢复attestation。

## 8. Retention

[Operations inventory](P7_OPERATIONS.md)。`ops retention-dry-run` 只读盘点、automatic_deletion=false。raw事实、tombstone/route deletion屏障、原batch/command回执、stream身份长期保留。active/live lease/fencing历史不删；jobs/analysis runs/训练版本/执行/feedback保留可追溯性。staging与服务端历史压缩没有正式replay horizon，**RETENTION DEFERRED**。

iOS仅新增已完成页的mark→verify→bounded sweep：全部operations ACK、sealed proof与delivered anchor在同一次state原子替换后才标记payload/transfer文件；每次默认128、硬上限512，不枚举全目录。pending引用/state.json/目录/symlink受保护；unlink后队列提交失败可重入。新完成页的本地receipt缓存可移除，服务端回执不删。旧state optional字段兼容，未知orphan/取消页/历史缓存保留。

Swift合成测试覆盖bad ACK、state写失败、dry-run、引用保护、missing payload不推进anchor、原身份重试和重复GC；macOS子进程实际SIGKILL在有界GC批次间，重开后anchor不丢、marked文件继续回收。该证据不替代iPhone后台/force-quit。

## 9. Monitoring

[Operations Runbook](P7_OPERATIONS.md)。结构化request_id/component/operation/duration_ms/status/error_code，sync登记/上传/COMMIT/receipt恢复/route chunk/seal可观察；不记录HR/GPS正文。Worker记录claim/processing/publication耗时、稳定错误码和既有受限运维lineage。

`ops snapshot` 聚合jobs/leases/retries/backlog、generation lag/publication age、training backlog/feedback pending、DB/table size/connections/locks/rollback/deadlock/autovacuum。`p7-monitor.sh`补齐进程、TLS、磁盘、备份与5xx；11个规则验证健康/异常/证据缺失路径，真实TLS实例实时监控返回ok。失败采集非零退出。

失败job审计始终保留；只有更高代际已clean覆盖旧失败target，才归类historical并解除当前故障告警。当前/未恢复失败和repair_required继续告警；真实PG验证新代际仅排队不会解除。计划任务与外部通知渠道未安装、未发送消息；阈值需真实运行校准。

## 10. Reliability

| 故障 | 实际验证/边界 |
|---|---|
| API SIGKILL | 正式上传事务在最终receipt前暂停后杀进程；未提交facts回滚，原身份重试一次提交、原receipt稳定 |
| Worker SIGKILL | run已创建、publication事务中杀进程；真实60s lease到期，旧attempt失败保留，新attempt成功一次，无半发布 |
| PostgreSQL SIGKILL | 未提交事务WAL回滚；此前耐久receipt仍可恢复，pgx重连、原身份重试 |
| Route | 完整PG回归覆盖chunk失败/重试、seal失败/恢复、delete屏障；pending不作为published输入；大规模专项复测 |
| P4 publication/fencing | SQL故障点与lease/token竞争回归，旧token/旧generation不能发布，无部分结果 |
| P5 publication | Base/Effective故障点整事务回滚，旧Effective完整；重新生成后人工override与frozen execution保留 |
| 网络中断 | 仅隔离API↔DB断连；HTTPS liveness可用，readiness/认证安全失败，重连后原receipt；非手机网络测试 |
| ENOSPC | 另一新凭证PG实例256MiB tmpfs实际free blocks=0；写返回503，释放自建填充文件后原身份恢复、Worker readiness/不变量通过；该阶段未单独断言所有排队job已发布 |
| 生命周期 | 三轮stop/start、API/Worker SIGTERM exit0、每轮原receipt核验；仅清理本轮project/volume |

宿主断电、存储硬件损坏、NAS丢失及真实蜂窝/后台生命周期未验收。进程kill不能当成这些验收。

## 11. Capacity

[完整口径与预算](P7_CAPACITY.md)。N100 VM 4vCPU、8,331,579,392 bytes RAM；非race容量/benchmark串行执行，既有NAS工作负载未隔离。独立测试PG为128MiB shared_buffers/1GiB limit，与release PG256MiB/2GiB分开记录。

| 合成历史 | Workout | Samples | Route points | 正式空P3 batches | DB MiB | 首50Workout median/P95 ms | 当前Effective median/P95 ms |
|---|---:|---:|---:|---:|---:|---:|---:|
| 1年 | 365 | 36,500 | 109,500 | 730 | 87.56 | 13.146 / 14.946 | 0.634 / 0.880 |
| 3年 | 1,095 | 109,500 | 328,500 | 2,190 | 241.84 | 2.682 / 3.511 | 0.705 / 1.007 |
| 5年 | 1,825 | 182,500 | 547,500 | 3,650 | 394.40 | 2.742 / 3.650 | 0.718 / 1.000 |

历史事实/派生是明确标注的SQL只读规模fixture（每日1h Workout、100 HR区间/300 GPS点），不是这些非空事实的P3上传证明；另以真实空页验证receipt/batch增长与52×year训练重生成。各查询100样本、P95 nearest rank；默认planner有索引路径，未强制关闭Seq Scan，WAL增长与plans纳入evidence。

正式HTTPS/P3：1000/10000 samples、4h14,400点三chunk GPS；单次/10批不报告P95。million series/200chunk全流程31.443s、seal11.558ms；20000-detail Workout/78chunk全流程3.873s、seal511.826ms；10000片页229.933s含故障/连接池重建。不是百万点分析或手机吞吐。

P4 365日state单次334.563/354.708/366.591ms，Workout claim→publication104.173/65.654/64.389ms；P5两fixture反馈→重算57.389/82.722ms，纯算法100次分布另存。无证据要求引入缓存/分区/第二DB。

资源窗口15.484s、4采样：API/Worker/PG平均1.65%/0.87%/3.27%单核CPU，总current最大85.6MiB；VM available约4.70GiB、盘余111.2GiB。独立scale PG working set167.1MiB、cgroup peak1GiB含cache；release预算2GiB。最终source/restore cgroup peak分别保存，不把stop/start后的计数当整场peak。短窗未发现明显异常增长，**不承诺长期无泄漏**。

最终有界soak **182.537s / 144 reads / 6增量 / 6训练再生成 / errors=0**，读median/P95 **19.608/36.626ms**；不是多年耐久证明。autovacuum未观察到，长期bloat、生产密度/冷盘和离机备份空间仍未验证。

## 12. Tests

最终Go：普通 **183**、race **183**、完整真实PG/race命令共 **363** 项通过，fail/skip=0（按Go JSON的test pass事件计，含重复执行的单元测试及子测试，并非363个独立PG场景）；gofmt/mod verify/vet/build、integration/scale/p7capacity标签vet、249包依赖边界通过。集成包 **336.189s**。P3幂等/删除/route、P4分析/fencing、P5训练/人工覆盖闭环均重跑；新P7安全/运维专项真实PG通过。

Docker真实构建/新卷/migration两次/受限身份TLS启动/备份恢复/故障/三轮重启/停止清理均通过；crypto负面控制通过。Swift6.3.2合成包39 tests通过，Xcode26.5 Release generic iOS/min18未签名严格并发build成功；独立iPhone12ProMax-class iOS26.5模拟器8 Normalizer测试通过，模拟器已删除。无iOS18 runtime/Metal或真机证据。

可复现命令（仓库根；每条创建自己的隔离PG，不指向真实库）：

```bash
scripts/test-remote.sh bash ../../scripts/check-p7.sh
scripts/test-remote.sh env RAI_SOURCE_COMMIT=d11e7b29e339b3d33c84962611031a3f33a18a7c \
  bash ../../scripts/test-p7-compose.sh
scripts/test-remote.sh bash ../../scripts/check-p7-capacity.sh
scripts/test-remote.sh bash ../../scripts/test-p7-backup-guards.sh
python3 scripts/test-p7-alerts.py
python3 scripts/test-p7-gc.py
python3 scripts/p7-secret-audit.py
cd ios/RAI
swift test
xcodebuild -project RAI.xcodeproj -scheme RAI -configuration Release \
  -destination generic/platform=iOS CODE_SIGNING_ALLOWED=NO build
```

模拟器命令使用本轮新建UDID、仅NormalizerTests，创建/删除与结果路径摘要写入evidence；不连接用户现有模拟器。所有验收数据合成；没有读取生产健康值或保存真实秘密。原始临时测试日志不作为长期唯一证据，脱敏JSON进入仓库。

## 13. Deviations

- 原IMPLEMENTATION当前概述仍称P5未实现；以真实源码/P5/P6结果校正概述，保留历史章节原口径。
- Worker角色缺goose版本表读权限，真实受限启动暴露后补足；API原日志使用可控path，改为规范路由，未重写同步。
- 全量恢复时最新scope已clean，旧terminal failed仍使告警常亮；增加actionable/historical分类与真实PG回归，未重写失败历史。
- 早期演练有secret bind权限、随机端口重启、Worker故障注入等待超时、tmpfs根目录权限问题；修正后整套重跑，失败轮不计PASS。第一轮5年ANALYZE触发2s预算，只放宽测试maintenance事务到60s，生产2s/5s未改。
- 较早源/恢复演练共用合成DB密码，最终重跑改为各实例独立新密码；早期floor-rank P95不作最终统计，最终nearest-rank实测另存。
- remote-test清理只rm容器时会遗留PG image声明的匿名卷；改为核验自建容器label/ID后rm -fv并验证。无归属证据的既有匿名卷未删除，未运行prune。native host路径实测，Go容器备用路径未单独演练。
- 远端源码快照无.git，secret审计不能在那里证明Git状态；改为要求checkout根并在本机真实Git执行通过。registry推送、正式环境、离机副本与通知均未假装完成。

## 14. Risks

真实iPhone HealthKit/anchor/GPS/后台/force-quit/网络/训练执行、iOS18、12ProMax性能和原生Liquid Glass GPU均未验证，WorkoutKit继续deferred。正式LAN/VPN/DNS/合法TLS/续期、现场secrets/端口/防火墙、reviewed commit/registry digest、独立备份目的地/密钥取回/定时与通知仍待用户批准后验收。

恢复旧快照会丢失之后已ACK事实，手机delivered anchor可能领先恢复点，不能承诺自动补齐；备份也可能恢复旧device撤销状态，切换前须安全入口关闭并核对轮换/撤销。需正式灾难恢复/新dataset操作方案，不以reset stream临时处理。

服务端历史/暂存增长未压缩；单Workout分析250,000 observations/route points上限保留，P3更大sealed对象不会被截断假分析。短soak、低采样历史、cache含量和非独占NAS限制容量结论；长期vacuum/bloat/电源与硬件恢复未验收。runtime数据库组无RLS，对被攻陷API没有租户隔离承诺。

## 15. P7 Final Status

**P7 ENGINEERING STATUS: PASS WITH RISKS**。具备启动、最小权限、数据保护、故障恢复、可观测性和有界容量验证的工程基础；不推导生产验收。

## 16. Deployment Preparation

**DEPLOYMENT PREPARATION: PARTIAL**。可执行模板、工具、Runbooks与隔离演练已完成；真实入口/PKI、正式secret安装、已审核commit与registry digest、离机备份/调度/通知及灾难恢复现场验收尚未完成。[Release Checklist](P7_RELEASE_CHECKLIST.md) 分开标记工程通过和真实发布阻塞。

## 17. Device Status

```text
P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
WorkoutKit REAL SCHEDULING: DEFERRED
```

## 18. Production

**PRODUCTION RELEASE READY: NO**。没有用户正式生产变更授权；没有更改生产数据、现有入口、公网端口或App发布状态。本轮完成文档与证据后停止。

## 19. Next Required Actions

仅列真实发布阻塞，不自动实施：

1. 用真实iPhone完成P3/P6门禁，补iOS18/12ProMax性能、GPU与真实网络/后台/训练执行证据；WorkoutKit仍独立延期。
2. 审核本轮代码并由用户决定提交/发布镜像；建立含reviewed commit、registry digest和全部合同/算法版本的正式Manifest。
3. 用户批准目标环境与维护窗口后，核验私网/VPN/DNS、防火墙、正式TLS链/续期和独立凭证安装/轮换恢复，不自动切换旧入口。
4. 批准独立备份目的地并验证复制/取回与密钥恢复，安装定时备份/监控和真实通知交付，以正式数据规模签收RPO/RTO。
5. 审核恢复点之后ACK/anchor与撤销凭证的灾难恢复方案、兼容回退和数据丢失边界；核验实际磁盘/增长余量、无未处理blocking jobs后，由用户单独批准上线。
