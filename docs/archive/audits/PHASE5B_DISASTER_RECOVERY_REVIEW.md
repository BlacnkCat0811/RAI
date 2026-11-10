# Phase 5B Disaster Recovery Review

## 1. Executive Summary

**PHASE 5B RESULT: PASS WITH RISKS**。合成自动化所要求的 DR-A、DR-B、旧 token/bundle/session fencing、真实 pg_dump/pg_restore、A–G 持久故障点和二次灾难均通过。Protocol 3 不变，schema 19。没有 commit、push、deploy、生产 migration、fnOS 或真机操作。真机/真实 HealthKit/后台进程 kill/公网和生产验收留 Phase 5C。

正式产品选择：`Dataset=new / Training History=discard / Old RAI Data=no_migrate / Disaster Recovery=new_session`。HealthKit 只恢复当前可见原始事实，不是服务器完整备份。

## 2. Git Baseline

入口 HEAD `816e26d1a2eb2e09021eed56594f77d57d5e2a5d`，main 比 origin/main ahead 2；Phase 5A 未提交。已在任何编辑前执行用户指定的 status、rev-parse、log -5、diff stat、cached diff stat；tracked diff 当时34 files / +564 / -90，staged diff 为空。全部当时 modified/untracked 文件 SHA256 与 status 记录在 [workspace baseline](../evidence/phase5b-workspace-baseline.json)。既有修改均保留；部分文件按 Phase 5B 继续增量修改，未使用 reset/clean/restore/checkout/stash。未修改 `../running`。原架构方案、1–18 migration、Phase 5A 原报告/证据和 Session 永久测试保留。

## 3. Recovery Contract

[正式合同](../PHASE5B_DISASTER_RECOVERY.md) 与 [runbook](../RAI_DISASTER_RECOVERY.md) 明确 normal→recovery_required→prepared→完整握手→active；旧 Session retired/archive。导入恢复包是一项明确事件；普通 timeout、404、401、429、500 继续重试，不自动切换。iOS coordinator 仅在成功响应中的完整身份不匹配时持久宣告 recovery_required。

## 4. Source Available Recovery

复用 operator-only `rai admin new-dataset --operation-type disaster_recovery --base-url ORIGIN`。retire source、创建 target、audit committed 同事务；旧 Dataset business snapshot/revision 不变，history 保留。CLI lost stdout 重试同文件/operation，不再创建 target。永久测试覆盖正常路径、rollback、同/不同 operation 并发与旧 POST/DELETE/worker 竞争。旧库只读归档，训练不迁移。

## 5. Source Lost Recovery

全新 schema 0 的 disposable database，无 source Dataset，migrate 1–19 后正式 `rai init`。不调用 new-dataset，不自动注册旧 device/token；独立新 Dataset/Epoch/Device/Token/Collector/Service。初始化后 identity 三表各1；事实/回执/屏障/分析/training 各0，commit_rev=0。A→B→C 三个独立服务端事实空间通过旧凭证/载荷交叉 fencing。

## 6. Fresh Server Bootstrap

init 使用已有全局 advisory transaction lock，非空库禁止第二个首次初始化；prepared private file 是操作恢复依据。admin-only 数据库 owner 权限，不被 httpapi 导入。Audit 与身份原子落库，source=NULL 表达 unavailable；重复同 operation 返回 replay。19 仅将18的 audit operation_type CHECK 扩展为 new_dataset/disaster_recovery、source 可空且普通 new_dataset 仍要求 source。19/19 checksum MATCH；17→18、18→19、fresh 0→19 和 repeated migrate 通过。sqlc 1.31.1 重生成完成。

## 7. Credential Recovery

CLI 改为必须给 operation/actor/base-url/absolute private file；token 不再进 stdout。完整0600文件写入和fsync、不可覆盖原子发布、目录fsync先于DB事务，retry重做fsync。DB只存hash。

故障注入使最后audit INSERT失败：身份/设备/audit全回滚，私有文件保留；移除故障后同文件成功。commit后stdout writer失败：相同文件字节与身份重试，audit/dataset均不重复；缺失原文件不能重生成token。source available credential package也覆盖lost output。包内所有身份、protocol/baseURL被Swift实际解析并用于验证；Keychain独立account在iOS模拟器测试通过。

## 8. Session Recovery

复用既有SyncSessionStore。恢复prepare将旧active标recovery_required并清pointer，与新prepared descriptor同次原子manifest替换；Outbox初始化失败可原身份重启。激活验证protocol3、dataset/epoch/device/collector/service、active状态和capabilities，然后原子切pointer和retire旧Session。Prepared不允许发现HealthKit；retired/recovery_required不能重新激活。新Anchor=nil；旧ACK/Outbox/deleted set/Keychain/ProductJournal不继承。三Session顺序恢复通过。token只进入operator私有文件/客户端凭证输入与Keychain，未写入证据。

## 9. Old Identity Fencing

新服务器上旧token的authenticated GET、POST、DELETE明确401；旧bundle+新token明确dataset_forbidden（403）。只改外部multipart metadata仍在strict payload/identity校验拒绝；外部字段不能覆盖JSON身份。旧epoch/collector fencing及source retirement回归通过。新Dataset重导旧服务器删过但当前HealthKit可见的UUID是产品允许行为；新DELETE建立新tombstone后，新Dataset旧bundle replay明确workout_deleted。旧Session callback固定归档目录，新Session字节不变。

## 10. pg_dump / pg_restore

实际PG18.6 custom dump→另一个空库single-transaction restore，不使用template copy代替本专项。fsync/full_page_writes/synchronous_commit/data_checksums均on，无TCP监听；只用Unix socket。合成fixture包含Device/Collector、Workout/sample/series/route、bundle receipt、tombstone/DELETE receipt、analysis run/publication、training plan/effective history。

T1备份后真实执行B导入、A删除和training新Effective/publication；还原库与备份点所有dataset-scoped业务行完全相同，A仍在，B/T3 tombstone/T4变化缺失。检查schema、47 tables、65 validated FKs、epoch/revision、回执/发布/training。archive SHA256与manifest SHA256验证且篡改manifest拒绝；最终独立演练restore约159ms（不是生产RTO）。每次隔离集群退出leftover databases=0、cluster removed=1。

## 11. Restore Quarantine

恢复库没有API/Worker listener，只作ARCHIVE / FORENSIC SOURCE。加default_transaction_read_only=on辅助防误写；测试旧POST/DELETE明确触发PostgreSQL 25006，commit_rev不变。owner仍可主动解除只读，故无API exposure是强制runbook前提，不能把该设置说成不可绕过安全沙箱。

monthly/P7 restore脚本保留label/empty/checksum校验并设默认只读；原演练中restore后启动API重放旧客户端的步骤移除，普通restart/soak改在原合成源库执行。Monthly backup读取真实schema并支持13–19，避免把新库写成旧schema17。相应Compose init调用改为私有文件交付。这些Docker/远程脚本本轮仅语法与现有本地backup guard单元回归，未执行fnOS/Compose部署验收；不能用其存在替代上述真实本地pg_dump/restore证据。

## 12. Process Kill / Fault Injection

A：prepared file→事务abort / commit→stdout lost，同身份恢复。B：prepared后重启。C：握手已校验/pointer未替换或文件替换失败，prepared与旧recovery_required保留。D：active pointer已切、发现未启动，重启nil anchor。E：staged discovery未promote，重启恢复原stage再promote，无额外generation。F：bundle已落盘未POST，重启exact bytes/hash保留。G：Go实际commit后断HTTP连接丢ACK，Swift查durable receipt完成，不重复事实。

这些是持久边界重开/权限失败/SQL故障/HTTP断连模拟，不冒充真机OS SIGKILL或磁盘断电证明；后者留Phase5C。背景late callback平台测试和固定Session namespace均通过。

## 13. Cross-Language Recovery

永久测试 `TestDisasterRecoverySwiftClientGoPostgres`：Swift Session A handshake→上传→ACK→GC；A存储连接对服务进程关闭，B从schema0 migrate/init；Swift读operator新私有package→prepared→matching handshake→active B，nil anchor且旧ACK为空；由独立当前可见合成facts fixture重新调用Swift assembler生成B bundle；Go提交时故意丢ACK；Swift查回执完成。PostgreSQL核验B只有1Workout/1sample/1import，training=0；真实Worker新job→新analysis→1publication，没有旧Dataset FK。A bundle+新token拒绝，A token401，A旧state字节不变。

这是实际Swift URLSession→Go HTTP→PG18链路，HealthKit reader输入是合成fixture，不是真机全历史读取。

## 14. Regression

完整命令、退出状态、日志hash和摘要见 [machine evidence](../evidence/phase5b-disaster-recovery-2026-11-01.json)。已通过：

- Go `test ./...`、`test -race ./...`、`vet ./...`、`build ./...`，以及integration/swiftclient tagged vet、go mod verify、dependency boundaries。
- PG18.6完整 `go test -race -count=1 -tags=integration,swiftclient -p 1 -timeout=30m ./...`，integration包375.091秒。随后最终专项重跑覆盖新增CLI package与更严格HTTP/25006断言，不修改生产实现。
- Swift Package：109个Swift Testing测试通过；2个普通XCTest通过。3个live/性能入口在独立Package命令中按设计skip，其live正确性场景由Go+PG harness实际运行，非用skip充当跨语言PASS；大性能入口仍受已有显式scale验收配置控制。
- iOS App unit：最终28项通过（独立模拟器，ad hoc签名，含Security Keychain及合成HK对象）。Debug/Release generic iOS device build通过，没有安装到真机。两个本轮一次性模拟器均删除。
- OpenAPI Redocly2.71.1：valid，31项既有warning；protocol3未变。
- 本地backup文件/传输故障单元12项通过；Python编译与修改shell脚本语法通过。Docker/CI wrapper执行未验证，未push触发远端CI。
- migration19/19 MATCH，sqlc重生成，git diff --check通过。

## 15. Remaining Risks

- 真iPhone12ProMax Session切换、真实HealthKit可见历史范围和权限缺口、真实后台kill/late callback：NOT RUN。
- fnOS全服务器损失、公网TLS、生产migration、实际备份交付与设备凭证文件选择流程：NOT RUN。
- 恢复时间仅synthetic local测量，不承诺真实RTO/RPO、容量或完整HealthKit历史。不可见事实标UNRECOVERABLE_FROM_HEALTHKIT，旧训练/手动历史可能永久丢失。
- 同时丢失operator私有文件和Keychain时不能从DB反推token；需后续明确授权处置，不能伪装成功。
- quarantine默认只读不防owner主动解除；必须持续保证无API/Worker exposure。
- 更新的Docker演练/CI工具包装仅本地语法审查，实际运行留独立隔离验收；没有以此作为生产通过证据。

## 16. Phase 5C Readiness

**READY FOR PHASE 5C: YES**，含义是可以进入用户另行授权的真机/fnOS/公网验收，不是自动执行授权。下一阶段按两条runbook验证真实凭证交付、Session切换、nil-anchor当前可见历史、后台生命周期、fencing与新分析发布。生产数据库NOT TOUCHED，reset脚本NOT EXECUTED。当前全部code/tests/docs/evidence保持未提交，等待审查。
