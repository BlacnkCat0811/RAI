# New Dataset + New Session 操作合同

本阶段只完成代码和合成隔离验收，没有生产部署、真实数据库 migration、旧 Dataset 删除、真机全历史导入或 commit/push。协议 3，schema 18。冻结决策为 new / discard / no_migrate / new_session。

## Operator 准备与执行

以下是操作模板，不表示本阶段已经获准连接生产环境。使用管理员数据库身份；普通 API/Worker 身份不能执行管理 DDL/创建入口。没有公开 Dataset 创建 API。

先完成已审核环境的 schema 18 migration，再只读查看来源。新库首次初始化仍使用 `rai init`；非空旧空间替换使用下面的命令。

```sh
rai admin new-dataset --source-dataset <SOURCE_UUID> --dry-run
```

只返回计数、active/retired 状态、collector 存在状态和目标身份生成计划；不会创建身份或文件。正式创建必须提前选定并保留 operation UUID。凭证文件必须在仓库、日志、evidence 和共享目录之外：

```sh
rai admin new-dataset \
  --source-dataset <SOURCE_UUID> \
  --confirm-source <SOURCE_UUID> \
  --confirm-phrase NEW-DATASET \
  --operation-id <OPERATION_UUID> \
  --actor <OPERATOR_NAME> \
  --credential-file /absolute/private/new-session-enrollment.json
```

只在创建时输出一次完整新身份与 token 到 0600 文件，stdout 不输出 token。临时文件 fsync 后以不覆盖的硬链接原子发布，再 fsync 目录；目标文件的旧内容绝不被覆盖。请勿 cat 到日志或复制到 evidence。PostgreSQL 只保存 SHA-256。文件存在而事务未成功时，保留文件、以完全相同 operation/source/actor/file 重试。事务成功而终端丢失响应时，同一操作返回原目标身份。已提交操作且文件缺失时只返回公开身份，不重新生成 token；token 无法从 hash 还原。本阶段没有 credential reissue 管理工具。

同一来源只能退役一次；不同 operation 并发只有一个成功，相同 operation 并发返回同一结果。退役旧库、新建新库和审计记录在同一事务中提交。失败不能留下 target active、source active 的半完成状态。审计只记 operator/operation/source/target/时间/committed 状态，不保存凭证和健康载荷。

## App 绑定与 prepared 恢复

在原生 iOS「同步与诊断」输入新服务地址、新 collector epoch 和新 device token，选择「建立独立同步会话」。App 的身份探测只读管理员已创建的 Dataset，绝不会自行创建。App 从握手获得 Dataset/Epoch/Service/Device，再持久准备 Session，最后再次核验完整握手并原子激活。

`RAIV2Sync/sessions.json` 是 lifecycle 与 active 指针的唯一权威；每个 Session 的 Outbox 位于 `sessions/<sessionID>`。原根目录 `state.json`、旧 bundle/retained、旧 Keychain 保留。新 token 使用独立 Keychain account；旧凭证不可覆盖，新 Session 不继承旧 collector、dataset epoch、anchor、deletedWorkouts、tombstone、recoveryOverrides、receipt、gzip 文件和 pending bundle。ProductJournal 也使用 sessionID 子目录，旧训练命令不重放到新空间。

prepared 后退出 App：下次 recovery 对唯一 prepared Session 使用原 Keychain 身份重新握手，不重新创建身份。若有多个 prepared Session，不能猜测，operator 应在配置界面用所需目标原凭证明确绑定。握手失败保持旧 active 指针；prepared 不 discovery、不 upload。成功后旧 Session 标为 retired，新 active 与两者状态写入同一个原子文件。切换受 busy/loading guard 保护。

后台 identifier 固定映射为 `com.rai.v2.sync.<sessionID>`，taskDescription 含同一 sessionID；迟到 callback 只写原目录。旧固定 identifier 仅连接原根目录，不读取当前 active 指针决定结果归属。新 Session 不消费旧 ACK。ACK 以原始 bundle hash 验证身份，协议没有新增 datasetID 回执字段。

## 数据语义与恢复边界

新 Dataset 的 commit revision 从 0 开始，没有原始事实、训练计划/配置/执行/反馈/生成历史、屏障或回执。旧 Dataset 只读退役，保留这些历史；旧 token 可读旧空间，所有写（包括旧协议、训练命令与 Worker 发布）拒绝，pending jobs 保留暂停。旧 token、旧 collector、旧 epoch、旧 bundle 不能写新空间。

新 Session 从 HealthKit nil Anchor 开始重新发现当前可见 Workout/Samples/Route，这是 fresh HealthKit import。旧服务删除过但当前 HealthKit 仍可见的 Workout 可重新导入新 Dataset；旧 tombstone 不复制。HealthKit 已删除、权限不可见、Apple 不再返回的数据、旧服务器删除历史和训练计划不保证恢复。

`rai-reset-v2-data.sh` 是 `LEGACY_UNSAFE_UPGRADE_PATH`，**DO NOT USE FOR PRODUCTION UPGRADE**，不能用于初始化、部署或灾难恢复。本阶段未执行它。未来灾难恢复使用新服务/新 Dataset/新 Device/新 Collector/新 Session 和 nil Anchor；若来源服务器已不可用，在空白新服务器使用首次 init，再绑定独立新 Session，不恢复旧 revision/token/anchor。本流程的完整故障演练留待 Phase 5B，真机切换和历史重导留待 Phase 5C。
