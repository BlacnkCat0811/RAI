# Phase 5B — Disaster Recovery + Recovery Fencing

2026-11-01。产品合同固定为 `Dataset=new / Training History=discard / Old RAI Data=no_migrate / Disaster Recovery=new_session`。本文件替代旧库接续方案；Phase 1–4 原方案和历史证据不改写。自动化结果与限制见 [验收证据](evidence/phase5b-disaster-recovery-2026-11-01.json) 和 [审查](audits/PHASE5B_DISASTER_RECOVERY_REVIEW.md)。

## 生命周期与触发

| 阶段 | 服务端 | iOS |
|---|---|---|
| Normal | active DB / Dataset | active Session |
| Disaster detected | operator 声明，或已成功取得的握手身份不匹配 | active → recovery_required；active pointer 清空，原目录保留 |
| Recovery prepared | 新 Dataset/Epoch/Device/Token/Collector/Service；durable audit | 凭证进入独立 Keychain account；新 descriptor prepared，独立 Outbox |
| Recovery active | 新身份的 active Dataset | 完整握手通过后，单次原子 manifest 替换切换 pointer，并将旧 recovery_required Session retired |
| Old state | retired 原 Dataset，或隔离只读恢复库 | 原 Session/Keychain/Outbox/ProductJournal 归档，不能自动切回 |

不新增第二套 Session 状态机。`prepared / active / retired / recovery_required` 是唯一生命周期。显式导入 recovery package 时，新 prepared descriptor 和旧 active→recovery_required 在同一 manifest 原子替换内持久化。仅成功取得且身份不匹配的握手调用 `validateActiveHandshake`；HTTP 404、401、429、500、timeout、断网都不自动宣告灾难。Phase 2 retry 与 `404 != durable deletion/recovery proof` 保持。

## Recovery A：源可用

使用原有 `rai admin new-dataset`，显式 `--operation-type disaster_recovery --base-url ORIGIN`。source dataset 行锁内原子创建全新身份、retire source、写入 admin_operations；不复制/删除任何事实、删除屏障、回执、分析或训练历史。source commit_rev 不变，target commit_rev=0。源库上的 retired 凭证可以读取归档元数据，但不能 POST/DELETE/发布或续租工作。

普通 new-dataset 仍可使用默认 `operation_type=new_dataset`。恢复操作的 `source_dataset IS NOT NULL` 表示 source available。不是新 recovery CLI。

## Recovery B：源完全丢失

独立 PostgreSQL 18.6，schema 0 / datasets 0 → 显式 migrate 1–19 → `rai init`。它不接受 source 参数；空库初始化使用原先 init 的全局 advisory transaction lock。只有持有数据库 owner 的离线管理入口能执行。

`rai init --operation-id UUID --actor NAME --base-url ORIGIN --credential-file ABSOLUTE_PRIVATE_PATH` 不再向 stdout 输出 token。stdout 仅含脱敏 identity/operation/replayed。无参数旧用法会拒绝，不能用 stdout 重定向替代 credential-file。正常 API 启动不隐式初始化。

在插入任何身份前，先向同目录临时文件写完整 package，0600、fsync 文件、原子 hard-link 发布且不覆盖、fsync 目录。重试重新 fsync 原文件及目录。数据库只持有 token SHA256。身份与 committed audit 同事务；audit 的 operation ID 是 durable 幂等键。并发相同 operation 返回同身份；不同 operation 不能在非空库创建第二个初始 Dataset。

DB commit 后 stdout 丢失或进程退出：用同 operation/actor/base-url/原 credential-file 重试，校验 Dataset/Epoch/Service/Device/Collector/Token digest 后返回 replay。token 不从 DB 反推。已提交而私有文件丢失时拒绝再生成或假装恢复；必须寻找原文件或已安装的 Keychain。两者都丢失属于操作凭证损失，需要后续独立授权处置，不能重用旧 token。

## Schema 19 与 audit

18 的 `operation_type='new_dataset'`、source NOT NULL 无法表达 source lost。19 只扩展现有表 CHECK 和 source nullability，保留 source/target UNIQUE、FK、committed status、时间戳，禁止普通 new_dataset 无 source。旧 CHECK `source <> target` 对非空源继续生效。无第二张恢复表，1–18 字节不变。

- operation_type=disaster_recovery；source 有值/NULL 分别表示 source_available/source_unavailable mode。
- operation_id、actor、target_dataset、created_at、committed_at、status=committed 都持久保存。
- 不存 token 明文、HealthKit 值、GPS、bundle payload 于 audit；普通事实表继续按既有合同存事实。

## 凭证包与客户端绑定

operator 私有 JSON envelope：`operationID / operationType / sourceDataset / actor / baseURL / protocolVersion=3 / enrollment`。enrollment 内含 `datasetID / datasetEpoch / deviceID / collectorEpoch / serviceID / token`。不在文档展示可用凭证。文件不得进入 repo、evidence、终端长期日志或监控。iOS“同步与诊断 → 灾难恢复 → 导入新恢复会话凭证”经系统文件选择器读取，由独立 Session Keychain account 保存 token；应用不复制 package 到 Outbox。交付副本应由 operator 按私有凭证管理，不能放公共目录。

operationID 用作本次新 sessionID，使重复导入恢复同一 prepared Session。新身份每个字段独立校验，不从当前 server hello 猜测目标身份。激活必须匹配 protocol 3、Dataset ID/Epoch、Device、Collector、Service、datasetStatus=active 和 bundle capabilities。任一不符保持 prepared，旧 Session recovery_required；不自动回退。Release iOS 仍要求 HTTPS。

Session 独立命名空间包括 Keychain、Outbox、Anchor、ACK、deleted set、background identifier、ProductJournal。旧 callback 通过固定 background identifier 和路径解析，最多更改自己的归档目录。不能复制、移动、清空或重写旧 pending bundle。

## 数据损失与重新发现

HealthKit 只重新提供当前仍可见的原始事实，**不是完整服务器备份**。已删除、无权限可见或 Apple 不再返回的 Workout、服务器 tombstone/receipt、旧 training history、analysis lineage 无法从 HealthKit 保证重建。

- 未 ACK bundle：旧 Session 的 exact bytes 留原处。新 Dataset 必须重读当前可见 HealthKit，构建全新身份的 bundle；不能改写旧 JSON identity。读取不到即 `UNRECOVERABLE_FROM_HEALTHKIT`。
- 已 durable ACK 且原 bundle 已 GC：ACK 仅证明旧 Session/旧服务器的交付。新 Session 不继承 ACK，从 nil anchor 再发现并生成新 bundle。
- 旧服务器删过 X，但当前 HealthKit 仍有 X：X 可以出现在新 Dataset，这是 New Dataset 产品合同。旧 tombstone 没有跨事实空间继承。新 Dataset DELETE X 后的新 tombstone 继续拒绝本 Dataset 的旧 bundle，错误为 workout_deleted。
- NEW 的 training_* 开始为空。不从旧 DB、备份或 ProductJournal 迁移计划；归档也丢失时接受永久损失。
- 新原始事实按 Raw→Dirty→Job→Analysis→Publication 正常重算；run/job/publication 不引用旧 Dataset。Worker 不假装成功。

## 备份、校验、隔离

真实 synthetic `pg_dump -Fc` / `pg_restore --exit-on-error --single-transaction` 测试固定 PG18.6：fsync/full_page_writes/synchronous_commit/data_checksums=on。清理后 leftover databases=0 / cluster removed=1。

T0 A exists → T1 dump → T2 B import → T3 delete A → T4 training edit/publication → 恢复 T1：A 存在、B 不存在、T3 tombstone 不存在、T4 变化不存在。恢复验证 schema、19 个 migration/checksum、47 tables、FK、dataset epoch/revision、facts、receipts、DELETE receipt、analysis publication、training rows，以及 archive/manifest SHA256（含篡改拒绝）。通过仅表示“备份点可读取”，不表示“可接续同步”。

恢复库必须位于无 iOS/API 入口的独立环境。不启动 API/Worker、不转发公网或局域网端口、不接旧手机。恢复脚本设置 `default_transaction_read_only=on` 作为防误写辅助；owner 可显式改变它，因此真正隔离保证仍是无 API exposure，而不是该参数被当成安全边界。仅人工只读查询、校验、导出历史证据。

恢复后的 active 路线是 clean active server → 新 Dataset → 新 Session → nil anchor；旧备份只作 ARCHIVE / FORENSIC SOURCE。禁止 restore backup + old token + old anchor 接续。现存历史反例测试在直接 Store/模板副本上展示旧方案为何不安全，不是线上恢复入口。

## 故障点与可重复恢复

| 点 | 故障注入/持久边界 | 重启行为 |
|---|---|---|
| A | private package 写好，事务 audit 失败；或 commit 后 stdout 丢失 | 回滚全无身份；同文件重试同凭证；已 commit 则 replay |
| B | App prepared 后未握手 | 从原 manifest/Keychain 恢复同 descriptor |
| C | 校验握手通过，pointer 替换尚未执行/替换失败 | prepared 保留；重新握手后再原子激活 |
| D | pointer 已切，discovery 未开始 | 新 active Session，nil anchor / empty Outbox |
| E | discovery staged，anchor 未 promote | 原 stage 在新目录可恢复，promote 后同 generation |
| F | bundle 落盘，POST 未完成 | 重启保留 exact bytes/hash，按原 Session retry |
| G | 服务端 commit，ACK 丢失 | Swift 走既有 exact receipt 状态查询；不生成新事实空间 |

这些为合成持久边界/进程替身与传输故障注入，并非真机 OS SIGKILL 验收。A→B→C 三个独立服务端身份和三个客户端 Session 可重复建立，无“一生只能恢复一次”的常量。真实 iPhone、HealthKit 全可见历史、OS 管理后台 kill、fnOS、公网 TLS、生产迁移均留 Phase 5C。
