# RAI 最小人工灾难恢复审查

2026-10-29，Asia/Shanghai。`BACKUP_DEPLOY_BASE_COMMIT=09fb5bb38d32cdbe42c757277ab2431995818ac4`。本轮只读审查与合成隔离验证，未改P3/P4/P5/P6运行代码、协议、migration或手机状态。此文缩小上一轮[完整恢复设计](RAI_DISASTER_RECOVERY.md)的后续选择范围，不实施其recovery generation或第二条恢复流。

**审查结论：当前版本两种方案都不能直接完成真实手机灾难恢复，执行仍为BLOCKED。以后优先评估方案B的显式“新数据集＋独立本地会话”，复用原P3首次导入；不要求为个人使用实现完整旧身份无损接续。** 方案B是待实现设计，不能现在重新配对/重装就宣称安全。方案A目前可以恢复SQL备份并保留其训练历史，但不具备安全继续旧客户端同步的证明。

## 当前障碍与九个问题

| 问题 | 实际实现与结论 |
|---|---|
| 为什么手机可能领先？ | 服务端dataset/service/device/collector身份和stream/receipt随旧快照一起回退；手机Delivered Anchor、nextSequence、candidate/page cursor不会回退。旧握手身份相同，不表达“发生过恢复”。`OutboxStore.bind`不会因此报错。 |
| 再点Sync为什么不够？ | `SyncCoordinator.sync`先bind、drain，再从Delivered Anchor查询；不枚举已交付历史。服务端可能sequence_gap或缺原batch；原始事实也可能无报错地缺失，例如没有新事件时不能依赖sequence_gap检测回退。 |
| 旧Outbox能重放什么？ | 仍保留文件、原batch/envelope、seq/previous连续，且父事实/屏障可证的请求，可按原P3协议提交或读取回执。pending/部分ACK journal在重启后保留；已推进的cursor不会自动倒回去重放服务器丢失的前半页。即使文件尚在，也没有通用“倒带旧journal”操作。 |
| GC后的payload能否从HealthKit重建？ | 当前仍存在、受支持、可读的对象可再次枚举，但重编码不保证恢复原batch文件/页分片/源版本。已删除、不可读对象和过去删除日志没有完整重建保证。不能用当前值生成旧seq的新内容。 |
| 哪些tombstones会丢？ | 备份后提交的entity_tombstones、route_deletions及相应change/receipt会丢；Create→Backup→Delete→Restore会使旧事实重新存在。备份前墓碑仍抑制旧UUID。本地永久删除集有帮助，但不证明跨设备/未观察删除的完整性。 |
| 如何避免重复Workout？ | 保留原HealthKit UUID和来源，复用dataset内原P3不可变身份/幂等；禁止生成替代UUID。已知旧Workout在本地cache中会走enrichment路径，只重置Anchor仍不等于重新提交父事实；Route仍须register/chunk/seal。 |
| 已有安全新设备注册/重新初始化吗？ | `internal/admin/admin.go:Init`仅给空业务库创建首个dataset/device/collector，已有dataset则拒绝。没有给恢复库重新配对设备的CLI/API。`ClientConfiguration.save`只换Keychain配置；固定RAIV2Sync目录和固定background session没有随身份切换。 |
| 首次历史同步能否复用？ | 可以复用代码路径：新Outbox的stream从nil anchor开始，HealthKit查询无日期截断；Workout每页25、quantity每页250，每次sync每type最多8页，Route/association每次维护最多8个Workout，需多轮。首次导入只涵盖当前客户端支持的Workout、六类quantity及route/series/关系，不能声称支持全部HealthKit类型或一次读完多年历史。 |
| 必须由用户确认什么？ | 真正的恢复事件/停机/来源与丢失时间窗；选A保历史或B放弃旧RAI专属历史；新身份及旧会话封存；读取权限/可见历史边界；真实恢复、凭证处理、入口切换。一般401/断网/空结果不能自动触发重建。 |

依据代码：`ios/RAI/App/{SyncCoordinator,HealthKitClient,BackgroundTransfers,ClientConfiguration,RAIApp}.swift`、`Sources/SyncCore/{OutboxStore,BatchBuilder,RAIAPIClient,RouteBuilder}.swift`，以及服务端`syncing`/`storage/postgres`的stream、batch、tombstone与seal事务。`OutboxStore.finish/acknowledge`仅在全部操作及封页确认后持久化Delivered Anchor；P7 GC随后可以回收payload与本地receipt。没有修改这些合同。

Apple说明nil anchor用于首次读取匹配对象，但读授权被拒绝时应用不能据此知道读权限状态；因此“查询为空”不能证明历史数据已删除。[Anchored query](https://developer.apple.com/documentation/healthkit/hkanchoredobjectquery)、[HealthKit授权](https://developer.apple.com/documentation/healthkit/authorizing-access-to-health-data)。这是可见性边界，不是本轮真机完整性证据。

## 方案A：恢复月度备份并保留历史

云端三件套→隔离PG18实际恢复→角色/FK/45表/回执/训练历史核验→保持真实手机入口隔离→审查旧客户端状态。

备份内Base/Override/Effective/Execution/Feedback可保留；旧凭证撤销状态也可能被恢复，需核对。问题是旧服务端事实、stream和receipt回退，而手机仍信任旧交付；备份后的墓碑丢失不能从当前HealthKit集合反推。新设备注册本身不能修复这些问题，当前也无此管理入口。

**当前可执行边界是SQL隔离恢复和人工查看备份内历史，不能恢复正常手机同步。** 仅当每个缺失批次的原始载荷、序号、页、父事实和删除覆盖都能证明时，原P3具备连续exact replay能力；既有测试证明该条件下的服务端幂等，不提供自动客户端倒带工具，也不能保证GC后条件成立。

若要求保留旧dataset并重新同步，仍需旧身份写入隔离、墓碑协调、journal协调和明确完成条件，修改范围触及客户端与服务端。完整generation方案已经超出本次简单恢复目标，暂不建议实施。不能拿不完整的Anchor reset作为“最小修复”。

## 方案B：干净数据库和新会话

新空PG18→原有bootstrap/migrate/Init生成全新dataset/service/device/collector/token→独立本地会话从nil Anchor开始→原P3有界历史页与增量→原P4发布→用户重新配置原P5新计划。

不把旧库的tombstones、旧raw或旧receipt混入新库；因新基线从当前可见对象开始，不需要猜测旧库哪些对象缺席应被删除。之后观察到的删除继续使用原P3永久屏障。原始UUID不变，重试不会制造重复Workout。P4/P5只通过原队列真实分析/生成；参数不足仍unavailable，计划需要用户输入，不伪造旧人工历史。

**当前阻断在iOS身份切换：** 固定目录`RAIV2Sync`，固定URLSession identifier `com.rai.v2.sync.uploads.v1`，`OutboxStore.bind`拒绝不同Scope；Product命令journal也有dataset绑定。只换Keychain会停住旧状态，清空整个Outbox会丢失未确认请求。后台旧回调不能绑定到新会话。无真机证据证明当前首次枚举的长期历史、权限、删除、Route最终收敛。

未来最小增量建议（本轮未实现）：

1. 手机明确确认“手动重建数据”，先暂停前台/重试，封存旧会话descriptor、Anchor、全部Outbox/transfer文件及Product命令；不删除、不改旧序号。
2. 每个新会话使用独立session UUID、目录、Keychain项和background identifier；旧后台回调只能落旧目录，绝不能推进新Anchor。服务器只接受新Init的token；旧服务端继续关闭写入，旧token在全新库应401。不得向新库导入旧devices表。
3. 先耐久写入含新身份的prepared descriptor，再建空Outbox并校验新握手，最后原子切换active session指针；旧会话路径保留。crash前后按同一prepared session恢复，禁止每次启动生成新身份。Product journal/current缓存同时按新dataset命名空间隔离。
4. 复用正常SyncCoordinator/BatchBuilder/RouteBuilder；多轮有界导入。逐type保存读错/待办和是否还满页；没有page错误、无pending、每type完成一次非满页查询、关联/route维护覆盖当前Workout后，只能声明“当前可见受支持历史导入完成”，不能声明HealthKit全历史恢复。交接后的新事件沿已提交Anchor走原增量。
5. 失败保留prepared/active状态和新pending原身份继续重试。取消只暂停。回退须暂停新会话、封存新journal并由用户选择匹配的旧服务/旧descriptor；有新写入后需评估丢失，不能自动回退或交叉重放。旧/新两套数据库不能同时作为在线写入主库；可先保留旧备份为文件，不需要增加常驻数据库。

此设计用**既有新dataset认证边界**，预期无需新增服务端同步协议、表或recovery generation；是否确实足够，须由晚到回调/跨身份/崩溃测试与真机证明后再接受。不是已经实现的安全保证。

## 只比较A/B

| 维度 | A：恢复旧库并接续 | B：新库/新身份 |
|---|---|---|
| 实施复杂度 | SQL恢复已有；正确接续高，须处理丢失墓碑和旧journal | 部署原新库能力已有；客户端一次显式会话切换仍需实现 |
| 需改代码 | 若要安全接续，客户端与服务端恢复协调；不能承诺小补丁 | 优先iOS会话目录、后台路由、配置/命令journal与导入进度；服务端预期复用Init/P3/P4/P5 |
| 保留备份内训练历史 | 保留 | 新在线库不保留；旧备份文件仍可保存，不能自动合并 |
| HealthKit历史 | 可重读可见对象，但无法自动补全旧删除证明 | 可重读当前受支持/可读/仍存在对象；真机覆盖待验 |
| Anchor风险 | 同Scope回退不能检测，客户端可能领先 | 新会话起点清楚；必须隔离旧Anchor和callback |
| Tombstone风险 | 备份后屏障丢失可能复活旧事实 | 不移植旧事实；初始不可见对象不入库，未来删除照常P3 |
| 新增协议 | 无完整证明前不能排除，上一轮完整方案成本高 | 预期无需新wire协议；本地身份切换不是已有能力 |
| 维护成本 | 恢复协调长期跨客户端/服务端 | 会话生命周期及一次人工新建，预计较低 |

选择依据是用户接受RAI专属历史损失和手动操作，且B不必接续旧库删除历史。推荐以后实现B的小范围客户端会话切换，再做真机验收；目前两者的真实灾难恢复都BLOCKED，不能把推荐当作已可执行的手机操作。

## 隔离验证及证据边界

复用既有合成测试而不另造恢复协议：

- Go真实PG `TestDisasterRecoveryExistingContractBoundary`：静止template复制模拟旧快照；客户端位置4、恢复stream2，原身份不变、seq5被拒、后续receipt和墓碑缺失；保留原wire时顺序补3/4并精确重试，重复事实不增殖、已知墓碑抑制。template反例不是pg_dump恢复。
- `TestDisasterRecoveryManualHistoryBoundary`：备份内5km/started保留，备份后7km/完成/feedback丢失；新Base追加仍保留备份内override和frozen execution。
- 既有fresh Compose路径：新PG/全新Init身份→合成历史通过原P3 API→Worker真实P4/P5→后续增量。月度dump/SSH/另一新PG实际恢复另验证45表计数/hash、FK/不变量、原receipt与P5历史。SSH为fnOS loopback，不能冒充真实云端取回。
- Swift临时Outbox：Scope改变会拒绝，已交付页合法GC后原payload可能不存在；pending/部分ACK在重启后保留。新会话切换/后台旧callback/真实HealthKit完整枚举未实现或未验。

本轮执行结果、数量、耗时和失败轮统一见 [部署专项结果](RAI_MONTHLY_BACKUP_DEPLOY_RESULT.md) 与 [证据](evidence/monthly-backup-deploy-2026-10-29.json)。源码存在和历史通过不替代本次实际退出结果。

## 用户现在可以做的步骤

1. 实际损坏时先确认恢复事件并批准停机，保留源卷、手机状态与现有备份；记录恢复点后的损失区间。
2. 批准从确认的云目录取回dump/SHA/manifest，按[月度Runbook](RAI_MONTHLY_BACKUP.md)在独立PG18恢复核验；不覆盖真实卷。先核对恢复后凭证撤销状态。
3. 可人工查看备份内训练记录，但保持真实手机写入关闭。当前到此停止：没有安全的“重新配对/重建”入口，不重装、不删Anchor/Outbox、不改stream SQL。
4. B会话切换将来实现并通过隔离和真机验收后，再明确选择放弃新在线库的旧RAI训练历史，确认可读历史与权限，运行多轮原P3直到当前可见范围交付，再等待原P4/P5；用户最终批准入口切换。第4步本轮不可执行。

无法承诺恢复：A备份后的手动训练修改/执行/反馈、B新库全部旧训练历史、任何方案中HealthKit已删除/缺失/不可读/尚不支持的数据，以及没有独立保存的密码/证书/secret。恢复PG不等于恢复手机状态，重新分析不等于还原人工操作。

后续若开发，仅建议上述B客户端最小增量；真实HealthKit授权、历史分页/增量/删除、Route/series/association、后台/force-quit、跨身份晚到ACK、取消/重启/回退和产品journal隔离必须验收。保持P3/P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED，DISASTER RECOVERY IMPLEMENTATION: NOT STARTED，DISASTER RECOVERY RESYNC: BLOCKED，PRODUCTION RELEASE READY: NO。
