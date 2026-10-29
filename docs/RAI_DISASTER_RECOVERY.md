# RAI 数据库灾难恢复与 HealthKit 重同步审查

2026-10-29。**DISASTER RECOVERY RESYNC: BLOCKED**。本轮完成合同审查、最小设计和可执行反例；未实现可用于真实设备的 Recovery Resync Mode、代际管理 CLI 或恢复 UI。原因是当前协议缺少可以安全关闭恢复流程的证明，不能把 reset anchor 或成功 HTTP 请求包装成完整恢复。Part A 独立验收，见 [Monthly Backup](RAI_MONTHLY_BACKUP.md)。

## 当前代码与不可恢复缺口

| 状态 | 实际位置及语义 | 旧快照之后的缺口 |
|---|---|---|
| dataset / service / device | `datasets.id/epoch/service_id`、`devices`，`admin.Init` 仅初始化一次 | 整库恢复保持全部旧值；不是恢复事件标识，也可能恢复旧设备撤销状态 |
| collector / stream | `collector_leases`、`sync_streams`；每 type/fingerprint 一个连续流 | 手机 nextSequence100，恢复库可能只接受81；相同 collector 不能从1开始，也不能跳号 |
| batch / page / receipt | `sync_batches`、`sync_pages`；原身份不可变、页封口与事实同事务 | 备份点后耐久回执也会丢失；旧 receipt 不能作为新代际事实的证明 |
| delivered / candidate anchor | `OutboxStore.LocalState.streams`、`PendingPage.candidateAnchor` | 服务端不保存 HealthKit anchor；同 Scope 的握手无法检测数据库倒退 |
| outbox journal | `state.json` 原子替换；先存 immutable gzip；page/cursor 与 background identity 对应 | P7 成功封页后移除本地 receipt、标记并回收载荷；已交付的81–100未必还能 exact replay |
| 永久本地删除集 | `LocalState.tombstones/deletedWorkouts` | 保留已观察并完成的删除，有帮助；没有与备份/恢复代际绑定的跨库完整覆盖证明，不能假设含所有服务端历史屏障 |
| Route | `workout_routes/route_chunks/route_points`、`route_deletions` 与正式 manifest/chunk/seal | 不能只重传 Workout 后直接认定 route 完成；sealed 第一版不支持 replacement |
| P4 / P5 | dirty/jobs→immutable run/publication；Base/override/Effective/execution/feedback | 应复用原发布队列和不可变历史；人工记录不来自 HealthKit |

已审查 `SyncCoordinator` 双车道（type 增量及有界 Workout route/association maintenance）、`BackgroundTransfers` 稳定 page/cursor identity 和成功响应缓存、`OutboxPipeline` 原批次 GET/POST/PUT、`BatchBuilder` 封页校验、服务端 dataset lock/collector fencing 与全部 raw staging 生命周期。

正常 API/Worker/PG 重启依靠原身份恢复，**不应触发灾难模式**。网络失败、token 失效、一次404或sequence gap也不能单独证明数据库恢复。当前 serviceID、datasetEpoch 由初始化产生并进入 Scope；旧备份同时恢复它们，不能作为“本次显式恢复”的独立世代。schema version 也不能替代该身份。

两个实质阻断：

1. `Create → Backup → Delete → Restore`：恢复库重新含旧事实，删除屏障已丢失。`anchor:nil` 重新枚举当前可读对象不能证明全部历史删除；授权不足/查询失败也可能得到空集，不能按“未出现”删除事实。已回收的旧 outbox 不保证能重放全部删除。`Create → Delete → Backup → Restore` 中已进入备份的屏障可由既有 P3 继续保护，不能将后者的通过冒充前者。
2. 当前没有持久 recovery generation、旧身份 fencing、独立恢复 stream/context 及 coverage/route/publication 完成协议。只重置 Anchor 时，本地 `workouts` 缓存还会把已知 Workout 导向 enrichment，而不是重新插入丢失父事实；重建同一旧 stream 又违反连续序号和不可变 batch identity。更换 UUID 或清空整套 Journal 都破坏原合同。

因此保留 BLOCKED。测试证明的是现有边界，不是证明“HealthKit 永远不能补数据”。在**原始不可变载荷仍在、顺序连续且父/屏障状态可证**时，P3 可按原 batch/seq 重放缺失事实并返回重试回执；自动完成条件不能从这个受限条件推导。

## 最小恢复设计（未交付接口，不能调用）

以下是解除 blocker 的评审设计，不是已经存在的表/CLI/API：

- 新增独立持久 recovery generation 与显式恢复事件。只由本地 owner 管理身份在维护窗口、数据库 SQL 恢复后轮换；普通启动不修改。保留 dataset/device 原逻辑身份，保留旧代际审计，不能改写旧 envelope/receipt 或用 schema version 代替。
- 标记恢复和旧 collector fencing 在 dataset 行锁中原子完成；所有事实写入、staging/seal、回执读取及后台重放必须检查代际，不能只做一次握手。旧应用也应 fail closed。管理模块不被 httpapi/application 导入；不新增公开未授权管理 HTTP。
- iOS 持久记录服务/dataset/device/generation，并在任何 drain、cached ACK 或 Delivered Anchor 推进前检查。检测明确事件进入 Recovery Required；保留全部旧 Journal、candidate/delivered/local route/association anchors、删除集及 payload。后台上传和晚到 callback 必须隔离于旧代际。
- 最小入口放“我的→同步与诊断→数据恢复”，沿用 P6 视觉；展示服务器已从较早版本恢复、读取权限限制和手动历史可能丢失，用户点击“重新同步历史数据”后才开始独立可重入恢复上下文。未满足协议前不提供假可用按钮。
- Recovery context 用新流边界，不能重用旧流的 sequence1；必须保留原始 HealthKit UUID/canonical identity。先协调可证明的删除和 pending/unknown ACK，再有界枚举当前可读事实，Route 继续正式 register/chunk/seal。不能绕过不可变 payload、源版本、父关联和 tombstone。
- 用独立恢复查询进度；正常 Delivered Anchor 保持原样。所有 recovery page durable、旧 outbox 有明确处置、stream确认、route确认、server facts发布且 coverage可证之后，单次原子本地提交才可切换正常进度。失败/取消保留原上下文，重启续传。
- 恢复开始后产生的新事件须由恢复 anchor 后的同一写入序列追赶，不能并行两个无协调写流。查询或权限不完整为 Recovery Partial，不推断 absent→delete、不伪造恢复完成；未证明的删除差异必须维持明确阻断/人工处置。
- Health Data Resync Complete 与 Analysis Recalculation Complete 分开。正式 P4/P5仍异步；不增加客户端算法、第二 importer、第二分析或第二 Coach。

解除 blocker 需要实现并真实验证上述完整边界，尤其是明确**恢复期间能证明的删除覆盖范围及无法证明时如何安全结束/保持Partial**。本轮不为了全绿新增未经验证的线上半协议。

## 实际灾难时的步骤

1. 关闭正式入口并停止 API/Worker，保留损坏实例和手机所有本地状态，不删除正式 Anchor/Outbox。记录最后一次成功月度备份和可能丢失区间。不要拿重启、401或404当作恢复事件。
2. 从用户批准的云目录取回三件套，检查来源和 SHA；按 [Monthly Backup](RAI_MONTHLY_BACKUP.md) 恢复到独立 PG18 空实例，全新 DB 登录密码和原 P7 角色授权。
3. 维护验收：schema、FK、原设备/collector/stream/receipt、tombstones、route staging/sealed、P4 current publication 和 P5不可变历史。核对备份点后撤销或轮换的设备 token，避免旧备份重新授权。禁止自动覆盖或切换正式入口。
4. **当前在此停止，恢复重同步 BLOCKED。** 现有版本没有可执行的 `Database Restored` 标记 CLI，也没有安全的新恢复世代；不得用 SQL reset stream、改 dataset epoch、重装App、删 `state.json` 或清空 HealthKit Anchor 替代。
5. 完整恢复协议将来实现并通过隔离 Go/Swift/真机验收后，才可由用户批准真实恢复标记、在手机确认历史重读，核验恢复覆盖和 route，随后恢复正常增量，再观察 P4/P5异步重算。此步骤本轮为 DEFERRED。

## P4 / P5 与数据损失

可重新取得的 HealthKit 事实仍需走 Raw→Dirty Scope→Job→Analysis Run→Published Results，不能在 iOS 重算正式 Fitness/Fatigue/Readiness，也不能直接设置 job succeeded。

已经存在于备份内的 Override、旧 Effective、Base、Execution Snapshot 和 Feedback 必须原样保留；后续重算追加新版本，不能覆盖旧历史。P7 合成 fixture 与本轮真实 dump/restore 对比验证这些对象及其原回执；既有 P4/P5回归验证发布及 override survive regeneration。

**数据库备份之后的新手动修改与执行历史可能永久丢失。** 例如备份内 Monday5km，之后用户改7km，HealthKit workout不是 override命令、处方快照或反馈凭证；不能声称重新跑分析会得到原7km。备份前已保存的7km能随数据库恢复，备份后才写入的部分不在备份中。Generated Base可以基于重新发布的分析追加生成；这不重建丢失的User Override/Execution/Feedback。

真机 HealthKit 授权、删除历史、Route/GPS和background URLSession恢复继续 **DEFERRED / NOT VERIFIED**。普通模拟器、macOS Swift及合成PG测试不能代替它们。

后续简化专项审查见 [Minimal Disaster Recovery Review](RAI_MINIMAL_DISASTER_RECOVERY_REVIEW.md)：本文件的完整generation方案保留为上一轮设计，不作为个人月度恢复的必做实施项。新审查只比较A恢复旧库接续与B新dataset/独立本地会话，推荐以后优先验证B的小范围客户端切换；当前两者真实手机恢复仍BLOCKED，本轮未实现新协议或恢复UI。
