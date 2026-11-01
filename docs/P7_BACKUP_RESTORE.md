# P7 PostgreSQL Backup / Restore

本轮只使用合成数据与独立实例。实际验收、耗时及完整性证据见 [P7 evidence](evidence/p7-fnos-2026-10-29.json)。备份是显式管理路径，在线同步不导入管理工具、不扫描全库或等待备份。

## 设计目标与范围

首版每日一次 PostgreSQL18 `pg_dump --format=custom --no-owner --no-acl`，整库一致性快照；覆盖44张应用表及goose版本表，包括raw、sync streams/batches/pages、tombstones、receipts、changes、dirty/jobs/attempts、analysis参数/runs/results/publications、training参数/config/base/overrides/effective/execution/feedback/commands。登录角色密码、证书、外部secrets不在数据库dump中；设备token摘要在库中，仍属敏感备份。恢复后必须重新安装已审核角色/授权，不能把owner导入为runtime。

目标 RPO≤24h（相对最近成功备份的**快照起点**，并依赖离机复制成功），RTO≤60min（现场目标，尚未以生产规模证明）。候选时间每天03:15 Asia/Shanghai；没有自动安装fnOS计划任务。普通逻辑dump不是PITR，没有WAL归档或物理备份。需要更小RPO时另评估频率/WAL成本，不更改同步协议。

设计保留14日每日、8周每周、12月每月；最后一次实际恢复通过的副本及其独立副本不得在替代证据建立前删除。当前脚本**不自动删除备份**，更不清理业务事实。实际存储预算需根据生产规模重新核实。

## Secrets、加密与独立副本

`pg_backup_password`仅供只读 `rai_backup_login`；读取所有表和sequence状态，无DDL/写权限。备份在宿主立即进入GnuPG AES256完整性保护的加密流，不保存长期明文dump。密钥为Git外单独受限文件（0600、至少32字节随机内容），不得置于backup目录；私有目录0700。`backup.json`仅含时间/格式/source commit/加密文件SHA256/字节数，不含DSN或健康值。

SHA256检测文件传输损坏；解密的完整性校验在执行任何恢复SQL前完成。GPG临时home与agent本轮清理；恢复明文临时文件0600且退出删除，操作者应选择受保护、必要时加密的临时文件系统。

至少一份加密副本复制到用户批准的**另一设备/独立故障域**，重新计算SHA256并核对metadata；密钥另行离线保管并验证恢复。仅同机另一目录或Docker卷不算离机。当前没有获批目的地，**离机复制与灾难恢复 NOT VERIFIED**；未上传云端或真实健康数据。不得将 `restore_tested=false` / `off_host_verified=false` 的自动metadata改成未经验证的true。

## 备份命令

先导出已批准环境的发布变量及Git外 `RAI_BACKUP_KEY_FILE`，源实例应正常可读，备份工具用与server兼容的18.6版本。

```bash
scripts/p7-backup.sh /absolute/restricted/backups/new-unique-directory
```

脚本拒绝覆盖，pipefail导致任何dump/加密失败都无成功metadata；未完成 `.part` 退出删除。维护期间不要并发schema变更。pg_dump的一致性语义见 [PostgreSQL SQL Dump](https://www.postgresql.org/docs/18/backup-dump.html)；[custom-format选项](https://www.postgresql.org/docs/18/app-pgdump.html) 支持通过pg_restore恢复。备份开始/完成时间与最近成功metadata交给监控，失败不能更新上次成功时间。

## 隔离恢复步骤

1. 新建唯一 project、全新 credentials、网络和卷，PG18空库 `rai_v2`；确认未挂真实/旧数据。仅隔离恢复目标设置容器label `rai.restore-target=true`。运行专用bootstrap建立 `rai_owner` 和待授权logins；不先执行业务migrations，避免restore对象冲突。
2. 核验备份来源、受限权限、文件SHA256、密钥分离和足够磁盘。恢复命令会拒绝无label或含任何public表的目标，没有 `--clean`、DROP或覆盖模式。

```bash
scripts/p7-restore.sh /absolute/backup-dir exact-isolated-container /separate/private/key
```

3. 脚本先完整校验/解密，再 `pg_restore --role=rai_owner --single-transaction --exit-on-error --no-owner --no-acl`。这步只证明SQL恢复；`pg_restore --list`不算验收。FK在恢复时实际建立和验证。
4. 在恢复实例执行 `roles.sql`、`runtime-grants.sql`，运行 `rai ops verify`；它在read-only repeatable-read快照中按PK遍历每张表，计算计数与SHA256，核验schema16、validated FK、receipt/stream连续水位、删除屏障、published run与generation、Base/Effective/override关系。对静止的合成源与目标逐表精确比较；生产备份期间有写入时，不能拿另一个时刻的源计数直接判失败，应使用同一导出快照或业务不变量验证。
5. API使用runtime角色及TLS启动并通过readiness；查询原batch receipt、原override command、Effective、frozen execution、feedback；旧删除UUID重放须suppressed。确认queued/running lease状态保留。旧源Worker必须与实际入口隔离，避免操作错误；恢复库内新Worker按真实lease到期恢复，不能直接把所有job改succeeded或删除。
6. 记录从准备目标、解密、SQL恢复、核验、API smoke到可用的墙钟时间及数据量；按数据量解释，不能把小库恢复观测宣传为生产RTO。

## 失败与恢复边界

错误密钥、损坏密文、SQL失败、授权错误或不变量不一致均停止验收。单事务SQL失败回滚；有损目标不继续覆盖。保留原加密备份和脱敏错误证据，只删除本轮创建的空/合成失败资源。若真实恢复后已有新写入，退回旧备份将丢失这些写入；原客户端delivered anchor可能领先于恢复点，需要单独设计经用户批准的灾难恢复/新dataset流程，**不能承诺旧anchor自动补齐丢失事实**。还须核对备份点后的设备撤销/凭证轮换，避免旧备份恢复旧凭证有效性；在批准的安全入口关闭与凭证处理完成前不切换。备份不参与正常sync和幂等协议。

电源断电、宿主存储损坏、整台NAS丢失、真实离机取回与真实手机在旧恢复点之后的收敛仍是独立门禁，进程SIGKILL不能替代这些验收。
