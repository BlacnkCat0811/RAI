# P7 Production Release Gate

2026-10-29。只供用户后续批准发布前逐项签收，当前 **PRODUCTION RELEASE READY: NO**。工程测试状态见 [Result](V2_PHASE7_RESULT.md)，不是自动发布指令。

## 可复现工程门禁

以下勾选只表示合成隔离环境的工程验收；结果、量化口径和命令见Result/evidence。

### Infrastructure

- [x] 当前实际HEAD/P7_BASE_COMMIT、P6已提交、入口干净已记录；dirty增量源码摘要另存。
- [x] release Compose已准备，开发/隔离测试/准生产/正式环境职责分开。
- [x] Go/PG基础镜像固定版本与摘要；本地测试image ID明确未发布。
- [x] PostgreSQL18 PGDATA/volume路径及大版本升级边界明确。
- [x] health/readiness分别验证；显式migration13，无启动自动迁移。
- [x] restart/start_period/timeout与Worker依赖顺序验证。
- [x] SIGTERM graceful shutdown、三轮重启、原回执恢复通过。
- [x] 4C8G常驻资源预算、实测CPU/current/peak/VM余量和磁盘记录。

### Security

- [x] 真实隔离HTTPS/TLS链/hostname/key/有效期与失败路径验证，无证书/ATS绕过。
- [x] PG无映射端口；测试HTTPS回环绑定，未开放公网。
- [x] API/Worker独立非owner runtime LOGIN，owner/migration/admin单独管理。
- [x] 实际角色越权DDL/TEMP/Route写/角色切换与runtime migration被拒绝。
- [x] cross-dataset404、revoked device401及受限P4/P5闭环通过。
- [x] 本机Git secret扫描通过；无默认生产密码，凭证仅受限文件。
- [x] DB密码/token撤销/TLS续期/secret恢复流程完成；未操作真实凭证。
- [x] body/admission/认证/超时/错误脱敏/path/权限审查，日志不记原始URL或健康载荷。

### Backup / Restore

- [x] 只读角色整库pg_dump custom机制与加密/权限/密钥分离完成。
- [x] SHA与完整解密认证验证；错key/损坏/替换hash/未标记/非空目标拒绝且无SQL部分写入。
- [x] 另一全新凭证/卷实例实际SQL恢复，非仅list检查。
- [x] 45表hash/count、schema13/FK/raw/sync/屏障/回执/current publication验证。
- [x] Training Effective/override/frozen execution/feedback与原身份API验证。
- [x] queued job身份/状态与lease历史保留。
- [x] off-host策略、保留和加密设计明确；实际离机另列未完成门禁。
- [x] RPO/RTO目标与小库实测分开，Restore Runbook与恢复后数据丢失边界完成。

### Retention

- [x] facts/永久屏障/运维/派生/暂存/客户端文件分类与dry-run完成。
- [x] tombstones/receipts/streams身份长期保留，幂等性不被年龄清理破坏。
- [x] active job/live lease/fencing/必要lineage不删，terminal历史策略明确。
- [x] analysis run/训练Base/Override/Effective/Execution/Feedback历史保留。
- [x] staging生命周期审查，无replay证明的服务端GC明确DEFERRED。
- [x] iOS Outbox durable/全部操作/封页/anchor原子mark后有界GC。
- [x] state失败/缺文件/引用保护/orphan保留/原身份重试与macOS真实SIGKILL恢复通过。
- [x] iPhone强杀、旧缓存/取消页/未知orphan清理未伪装完成。

### Observability

- [x] 结构化日志字段、sync阶段和Worker claim/processing/publication耗时可观察。
- [x] API availability/readiness、Worker backlog/failed/lease/retries、analysis lag/发布年龄可观察。
- [x] training generation/backlog/feedback及HTTP override冲突/执行错误可观察。
- [x] PG/table size/连接/锁/rollback/deadlock与宿主disk/TLS/backup状态可观察。
- [x] 11类告警positive/negative/missing-evidence控制及真实健康实例验证。
- [x] 新代际仅排队不掩盖失败；实际clean后历史失败解除当前告警，审计保留。
- [x] Operations Runbook完成；调度/真实通知未安装的缺口明确。

### Reliability

- [x] API事务中实际SIGKILL，原身份重试不重复事实。
- [x] Worker发布中实际SIGKILL，真实60s lease恢复并只成功一次。
- [x] PG事务中实际SIGKILL/WAL恢复，receipt/stream/facts一致。
- [x] Route chunk/seal失败/重试/删除与pending/sealed边界回归。
- [x] P4分析原子发布/旧fence拒绝回归。
- [x] P5 Base/Effective原子回滚、人工override跨重生成保留。
- [x] 独立256MiB tmpfs实际ENOSPC/503/空间恢复/Worker与原身份重试。
- [x] API↔DB合成网络断连/重连及三轮start/stop/restart通过。

### Capacity

- [x] 1/3/5年低密度SQL只读规模，真实空P3历史与jobs/runs/training版本增长覆盖。
- [x] 正式非空P3 1000/10000 samples、长GPS、large Workout/million series规模通过。
- [x] P4特征/load/state/recompute与P5生成/解析/override/执行/feedback/recalc基线完成。
- [x] 默认EXPLAIN/index/关联查询/WAL/连接与锁指标审查，生产预算未为benchmark放宽。
- [x] CPU/内存/current/peak/VM余量/盘余与数据增长有量化样本、median/P95口径。
- [x] 约3分钟144读/6增量/6再生成有界soak无错误，三轮生命周期验证。
- [x] 正常同步无全库摘要/无界重算；全库verify仅显式维护路径。
- [ ] 长期资源无泄漏、vacuum/bloat/高密度真实历史已证明（NOT VERIFIED；短窗不能替代）。

### Release

- [x] Deployment/Operations/Security/Backup/Capacity/Result与Release Gate文档完成。
- [x] 测试Manifest有基线+dirty源码摘要、Go/Swift/PG/migration/合同/算法/训练版本与本地image ID。
- [x] 00001–00013 checksum和历史合同/evidence不变；forward-only/app兼容/restore损失边界明确。
- [x] preflight/config validate、migrate-status、backup完整性、retention dry-run可复现。
- [x] Go普通183/race183/完整真实PG race363，gofmt/mod/vet/build/249包边界通过。
- [x] P3/P4/P5回归；Swift39 tests、未签名Release/min18构建与独立iOS26.5模拟器8项通过。
- [x] 未改正式数据、未部署/迁移/开放公网/发App/commit/push；只清理确认归属的测试资源。
- [x] P3/P6真机延期、WorkoutKit独立授权及Production Release Ready NO保留。

## 真实环境发布前必须另外完成

- [ ] 用户明确批准目标主机/project/时间/真实有状态操作/入口切换；没有把本轮开发授权当成发布授权。
- [ ] 正式构建的reviewed commit与registry image digest、Go/Swift/PG、migration/API/算法/训练版本Manifest完整；没有把dirty tree或本地image ID称为已发布镜像。
- [ ] 私网/VPN/LAN入口、DNS、合法证书链、iPhone hostname验证、续期与防火墙实查；PG不对外，HTTPS不被绕过。
- [ ] 正式credentials安装及runtime身份现场核验；secret权限、轮换与恢复流程已演练；无默认密码。
- [ ] 真实备份任务/失败告警已安装、独立目的地获批并实际复制/取回校验；密钥另存且可恢复；生产规模RPO/RTO签收。
- [ ] 真实磁盘、容量/资源余量、监控计划与通知交付已验证；无未处理blocking jobs。
- [ ] 迁移compatibility与app rollback复核；forward-only/恢复后数据丢失边界及客户端anchor灾难恢复方案明确。
- [ ] P3真机：HealthKit授权/anchored incremental/删除/GPS、background URLSession、force-quit、真实网络与首次历史导入。
- [ ] P6真机：iOS18实际runtime、iPhone12ProMax性能、原生Liquid Glass GPU/可用Metal基线、真实训练执行闭环。
- [ ] WorkoutKit仍deferred；若未来开启，需独立授权与真实系统排程验收，不因P7自动解锁。

任一真实发布阻塞未完成都保持NO。没有自动commit/push、生产部署、真实库迁移、开放公网、App发布或真机门禁解除。
