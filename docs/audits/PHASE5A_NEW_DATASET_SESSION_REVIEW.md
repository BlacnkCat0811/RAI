# Phase 5A — New Dataset + New Session Review

日期：2026-11-01，Asia/Shanghai。结论：**PASS WITH RISKS**。这是本地代码、合成数据和隔离 PostgreSQL 18 验收，不能替代真机、生产部署或灾难恢复验收。[机器证据](../evidence/phase5a-new-dataset-session-2026-11-01.json)；[操作合同](../PHASE5A_NEW_SESSION.md)。

## 1. Executive Summary

按冻结决策 `new / discard / no_migrate / new_session` 实施。operator CLI 创建全新 Dataset/Epoch/Device/Token/Collector，并在同一事务退役旧 Dataset、写 committed 管理审计。旧事实、删除屏障、回执、分析和训练历史原地保留；新空间初始为空。iOS 独立 Session 持久化、Keychain、Outbox、ProductJournal、nil Anchor、成功握手后原子激活及后台回调隔离已实现。

未执行 reset、生产数据库操作、生产 migration、部署、push、commit、旧 Dataset 删除、真机全历史导入或旧 RAI 数据迁移。没有新增 Web/Watch/桌面客户端、公开 Dataset 创建 API 或虚假 Worker 成功路径。

## 2. Git baseline

- branch：`main...origin/main [ahead 2]`。
- HEAD / Documentation：`816e26d1a2eb2e09021eed56594f77d57d5e2a5d`。
- Feature：`dfbc2774986bb2c404f8d442e53dff2725507ced`。
- 入场 staged/unstaged diff 均空；原有未跟踪 `docs/audits/RQ010_HISTORICAL_DATA_RESET_AUDIT.md`、`docs/evidence/rq010-historical-data-audit-2026-11-01.json`、`docs/evidence/rqrun-phase4-logs/` 保留，未覆盖、stash、reset、clean 或 restore。
- 起点 schema 17，protocol 3；本工作区 schema 18，protocol 3，OpenAPI 1.5.0。没有自动提交。

## 3. Migration changes

只新增 `00018_dataset_sessions.sql`：`datasets.retired_at timestamptz NULL`，以及 `admin_operations`（operation/type/source/target/actor/created/committed/status）。source/target 唯一约束表达一次来源切换的约束，不增加原始事实索引。

Expand-only，无 DELETE/TRUNCATE、大表回填、Raw Facts 改动。00001–00017 与冻结 HEAD 逐字节一致；00001–00018 连续，checksum **18/18 MATCH**。`TestDatasetMigration17To18AndRepeatedMigrate` 验证旧 revision 42 原样保留、active 默认状态、无自动操作记录及重复 migrate；`TestFreshDatabaseSchemaRebuild` 验证两次从空库构建相同 schema。Down 保持不可逆。

## 4. New Dataset design

`rai admin new-dataset` 仅 CLI 可达，要求数据库 owner/operator 身份。dry-run 使用 REPEATABLE READ READ ONLY，只有计数/状态/生成计划，不分配目标身份或写入文件/记录。正式命令要求 source、confirm-source、NEW-DATASET 短语、operation ID、actor、私有绝对 credential-file。

凭证先输出一次到 0600 文件：CSPRNG token 为 32 bytes；文件完成/fsync 后原子发布且不覆盖，再 fsync 目录，恢复时再次同步文件与目录。DB 只保存 SHA-256；stdout 返回身份且 token 为空。事务为 operation advisory lock → source row lock → create target/device/lease → retire source → audit → COMMIT。新 revision 默认 0，无旧业务数据复制。提交后的相同 operation/source/actor 返回原身份；不匹配的 prepared 凭证被拒绝。

## 5. Dataset retirement

NULL 为 active；retired_at 非 NULL 为 retired。鉴权保留旧 token 的只读身份，握手公开 datasetStatus。写请求返回稳定 `409 dataset_retired`，事务内在 Dataset 锁下重查，覆盖鉴权后并发退役。旧的已移除写协议遇有效 retired 凭证也返回该生命周期错误，活动空间仍无旧协议写能力。

退役只改变 Dataset 元数据，不撤销/删除事实、tombstone、receipt/import、analysis/publication、training history。pending/running jobs 保留暂停，不伪造成功；后续 worker claim/requeue/renew/start/publish/failure bookkeeping 与训练命令都不能重新写该业务空间。operator 分析重算/参数写同样检查退役。

## 6. Device / collector fencing

新旧 Dataset ID、Epoch、Device ID、Token、Collector Epoch 全部独立。bearer 的 Dataset 与 bundle Dataset 不同返回 dataset_forbidden；新空间旧 Epoch 返回 dataset_epoch_changed；旧 collector 返回 collector_fenced；旧空间原 token/collector 请求返回 dataset_retired。

客户端事实锁顺序仍为 Dataset → 下游事实/回执/dirty/job。Worker claim/requeue 先锁 active Dataset，再锁 job，SKIP LOCKED；publisher/renew/start/failure 也先锁 Dataset。与 retire 共用锁，旧事务只可能在退役前完整提交或退役后完整拒绝，不能写到新 Dataset。

## 7. Session architecture

`SyncSessionDescriptor` 包含 sessionID、datasetID、serviceID、datasetEpoch、deviceID、collectorEpoch、protocolVersion、baseURL、createdAt 和 status，不包含 token。prepared/active/retired/recovery_required 为可持久状态，只有 active 可 discovery/upload。

`RAIV2Sync/sessions.json` 单原子文件同时提交 active 指针及 Session 状态。prepare 持久 Keychain/descriptor，初始化专属 Outbox，验证完整握手后才 activate。App 切换受 busy/loading guard 保护。prepared 重启使用原身份；多个 prepared 时不猜测目标，允许在配置界面明确选择原凭证。历史根 Outbox 没有自动迁移或恢复上传入口。

ProductJournal 也按 sessionID 分目录，ProductModel 在切换后重新配置；旧训练计划/命令不继承到新空间。

## 8. Keychain isolation

新 service `RAI.V3.SyncSession`，account 为 sessionID，`AfterFirstUnlockThisDeviceOnly`。每个 account 不可用另一 token 覆盖；新 Session 拒绝复用其他 Session token。旧 legacy Keychain 原地保留，App 绑定还拒绝复用 legacy token/collector。模拟器实际 Security API 独立账户、原账户不可覆盖测试通过；没有将测试凭证保存到 evidence。

## 9. Outbox isolation

每 Session 使用 `RAIV2Sync/sessions/<sessionID>/state.json` 及自己的 bundle、retained、gzip wire、receipt、discovery、anchor、deletedWorkouts、tombstone、recoveryOverrides。旧目录原地保留，禁止重命名或移动旧 bundle 到新空间。新 state 除 scope 外从空白初始化，nil Anchor。

即使旧 bundle 被误复制进新 Outbox，发送前按原 JSON 核对 Dataset/Epoch/Collector，不会用新凭证重写旧 bundle。服务端再次按 bearer/identity 拒绝。原始 JSON hash、gzip 派生文件和 Durable ACK 的 Phase 2/4 行为保持。

## 10. HealthKit discovery behavior

nil Anchor 为 fresh HealthKit import，不是服务器迁移。App 测试用合成 HKWorkout/HKQuantitySample 和真实 secure archive，通过实际 SyncCoordinator global/scoped discovery 验证新会话 nil Anchor、当前可见历史重导和重复发现幂等；retired/prepared Coordinator 被禁止发现。

旧 Dataset 已删除但当前 HealthKit 仍可见的同 UUID 可以进入新 Dataset，这是冻结产品决策的结果。旧 tombstone 不复制。HealthKit 已删、权限不可见、Apple 不再返回的对象，以及旧训练计划/服务器删除历史不保证恢复。没有 SQLite/XML/旧 Running import。

## 11. Background callback isolation

identifier：`com.rai.v2.sync.<sessionID>`（52 字符），taskDescription 同时带 sessionID，delegate 固定自己的目录。AppDelegate 解析原 identifier，不能用当前 activeSession 决定 callback 目录。旧固定 identifier 只映射原根目录，旧 completion 不触发新 Session 的回执恢复。

App 平台测试验证迟到 callback 只写旧目录、即使误送新 delegate 也按前缀拒绝，旧文件不能交给新 transfer；新 state 字节不变。补齐 events 完成先于 completion 注册时的回调竞态，测试通过。真实 iOS 后台进程杀死/重启/系统调度仍待 Phase 5C。

## 12. Failure recovery

| 故障点 | 合成验收结果 |
|---|---|
| 凭证文件准备后、DB COMMIT 前注入 audit trigger 异常 | 全事务回滚，source active、无 target/audit；文件保留，原操作/原文件恢复，凭证字节不变 |
| DB COMMIT 后 stdout 断开 | target 已存在、source retired；原 operation 返回原目标，不创建第三个 Dataset |
| 错误 operation/source/actor 或 unrelated prepared credential | 拒绝冲突，无重复 Dataset |
| App prepared 后重开 | 原 descriptor/token 保留，握手后启用，不重新生成身份 |
| Outbox 初始化失败 | prepared descriptor/Keychain 保留，移除合成 blocker 后原身份恢复 |
| active manifest 替换写失败 | 旧 active 指针与状态保持，成功重试后整体切到新 Session |
| 旧 callback / bundle / ACK | 固定原目录；旧 bundle 身份拒绝；新 ACK 的 hash 绑定新 Dataset/Epoch/Collector JSON |

数据库不保存明文 token。若 operator 私有文件和 App Keychain 同时丢失，不能从 hash 恢复 token；本阶段没有凭证重新签发工具，不能用 operation 重试伪造原 token。模拟故障点与文件写失败不等于已完成真机电源中断验收。

## 13. Integration tests

`new_dataset_session_test.go` 的独立身份/空间、退役/保留、幂等/故障/并发各组，加上 `dataset_migration_integration_test.go`，覆盖需求中的 Dataset/device/epoch/revision、零复制、训练保留、Workout/child delete/旧协议拒绝、旧 collector fencing、响应丢失和不同 operation 不建第三空间。

完整合成 OLD 含 Workout/Samples/Route、tombstone、receipt/import、分析发布和生成后的训练历史。切换后 NEW 初始所有业务表为空，只存在新 device/collector；随后用新 token 导入 HealthKit 当前可见、曾在 OLD 删除的 UUID，ACK/job/publication 只属于 NEW，OLD 所有业务行逐表快照不变（仅排除 admission budgets/upload slots 等操作计数，Dataset 本身预期增加 retirement 标记且 revision 不变）。

最终计数（仅合成；不导出身份、数值、GPS、凭证）：

| 表 | OLD，切换前后相同 | NEW，初始 | NEW，fresh import + publication 后 |
|---|---:|---:|---:|
| workouts | 1 | 0 | 1 |
| health_samples | 4 | 0 | 1 |
| workout_routes | 1 | 0 | 1 |
| entity_tombstones | 2 | 0 | 0 |
| workout_imports | 1 | 0 | 1 |
| training_plans | 1 | 0 | 0 |
| analysis_publications | 1 | 0 | 1 |

并发测试同时运行 NewDataset、旧 POST、旧 DELETE 和旧 Worker publish；允许退役前原子完成或退役后 dataset_retired，NEW 业务表仍为空，无半退役、丢失回执/屏障或跨空间写入。

## 14. Phase 1–4 regression

| 验证 | 结果 |
|---|---|
| go test ./... | PASS |
| go test -race ./... | PASS |
| go vet ./... / go build ./... | PASS |
| 完整隔离 PG18 + integration,swiftclient，race、count=1、p=1 | PASS；integration 295.045s |
| 最终 NewDataset / migration / 故障注入定向 PG18 race | PASS |
| Swift Package | PASS，106 tests / 12 suites |
| App unit tests，fresh iOS 26.5 模拟器 | PASS，26/26；本地 ad hoc 签名支持系统 Keychain 测试 |
| Debug / Release generic iOS device build | PASS；未签名、不安装到真机 |
| OpenAPI lint 2.71.1 | PASS，31 条既有警告 |
| dependency boundary | PASS，在线图排除 admin/migrations/goose；analysis/training 保持纯计算边界 |
| migration / frozen prefix / rebuild / repeat | PASS，18/18；旧 17 个文件不变 |
| git diff --check | PASS |

隔离 PG18.6：fsync/full_page_writes/synchronous_commit/data_checksums 均 on，Unix socket、无 TCP 监听；结束 leftover databases=0、cluster removed=1。独立测试模拟器已关闭并删除。

初次完整回归发现旧 schema fixture 提前鉴权时引用新列、锁等待观测匹配旧 SELECT；现鉴权/事实锁在单行 Dataset 元数据中兼容缺失 nullable 字段，但 Ready 仍严格要求 schema18。历史快照仅规范化 NULL retirement 字段，不移除非 NULL retirement、事实/receipt/revision 断言；锁测试仍观察真实 blocker 数量和两种锁顺序。App 旧 404 恢复测试的简化 bundle 增加合成 scope 身份，原有“不假 ACK/文件保留/重试次数/零 HealthKit 读取”断言全部保留。首次未签名 Keychain 平台测试不可访问，改用本地模拟器 ad hoc 签名后通过。未删除测试或降低验收。

## 15. Remaining risks

- 未验证真机 Session 切换、iOS 后台系统调度/迟到 callback 跨真实 App kill、真实 HealthKit 全历史重导及权限变化。
- 未验证 fnOS/公网/生产环境、生产 migration、真实服务器丢失/损坏或正式灾难恢复。
- operator 私有凭证文件需自行安全保留；不复制到日志、证据或仓库，明文 token 无 DB 恢复路径。
- 多个 prepared Session 不能自动判定，需明确选定身份；没有凭证重签发或通用 Session 管理 UI。
- 旧待办/训练历史保留但不再执行；它们不代表新 Dataset 的进度。旧 reset 脚本仅历史保留且已标记禁用用途。
- OpenAPI 原有 31 警告保持；N100/fnOS 新 Session 容量与灾难恢复 RTO/RPO 不由本机合成测试证明。

## 16. Phase 5B readiness

**YES，基础已就绪；正式恢复验收尚未完成。** 后续应使用新 Dataset/Device/Collector/Session 与 nil Anchor 演练服务器丢失/损坏；来源已不存在时，在空白新服务器首次 init 后绑定独立 Session。不依赖旧 revision/token/anchor/outbox，不迁移旧训练历史。Phase 5C 再进行获批真机切换/可见历史导入。本阶段止于代码、migration、tests、docs、audit、evidence，等待用户下一步授权。
