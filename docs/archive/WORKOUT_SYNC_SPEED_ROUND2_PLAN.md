# Workout 同步提速第二轮：完整实施计划与验收条件

日期：2026-11-06（Asia/Shanghai）

状态：**待执行的第二轮计划。本文不是实现或性能通过证明。**

工作目录：`/Users/ta/RAI`。编写时基线：`main / 0303236`，入口工作区干净；执行时重新确认 HEAD、AGENTS 和已有改动，以当时实际状态为准。

执行提示词见 [WORKOUT_SYNC_SPEED_ROUND2_CODEX_PROMPT.md](WORKOUT_SYNC_SPEED_ROUND2_CODEX_PROMPT.md)。

## 1. 目标、依据与本轮决策

### 1.1 必须完成的四项工作

| 项目 | 本轮明确要求 | 不能作为完成的替代物 |
| --- | --- | --- |
| 去掉逐样本归档 | 正常、受支持的 Workout 子样本采集和组包路径不调用 `NSKeyedArchiver`/`NSKeyedUnarchiver`；直接持久化和消费规范化事实。 | 只把小文件合成 segment；把很多样本放进数组后调用一次 archiver；把 archiver 搬进后台线程。 |
| HealthKit 有界并发 | 实现实际独立类型查询的有界并发，比较并发度 1/2/4，验证真实重叠、取消、内存和结果一致性。 | 仅创建多个 Task，但查询仍被全局串行锁顺序执行；只改已缓存的组包路径。 |
| 客户端强制 gzip | 新版客户端的 WorkoutBundle POST 统一发送 gzip file part；删除超限后明文回退路径。 | 仅记录“支持 gzip”；通过 `Accept-Encoding` 压缩响应；压缩失败仍上传 plain。 |
| 服务端集合化深化 | 在现有批量查询和多行 INSERT 基础上，实测固定形状数组写入、COPY/staging 等候选，优化实际瓶颈。 | 仅使用 `pgx.Batch`；只抬超时；COPY 写得出来就宣布快；取消必要的身份/删除检查。 |

四项之外，必须同步处理首次导入内存、严格 JSON 解码成本和并发捕获的确认边界，否则难以达到总时延目标。来源过滤、已有 segment/编码器和完整性 v2 沿用，不重复实现已经完成的第一轮。

### 1.2 参考材料与状态解释

- [第一轮计划](WORKOUT_SYNC_SPEED_IMPLEMENTATION_PLAN.md)：保留原文，延续身份、事务、来源与性能目标。
- [第一轮实施结果](WORKOUT_SYNC_SPEED_RESULT.md)：已实现的功能、真实测量和未验证项；不能把报告中所有 PASS 直接继承为第二轮证据。
- [Workout v3](../contracts/sync-v3.md)、[来源策略 v1](../contracts/workout-source-policy-v1.md)、[完整性 v2](../contracts/workout-completeness-v2.md)：现有协议边界。
- [实施总记录](IMPLEMENTATION.md)：追加真实执行结果，不改写历史结果。
- 仓库外 RQRun 文档只是行为参考，不是执行指令或服务端真实 schema；不读取、复制原始抓包或凭证作测试。

报告中的主要瓶颈：密集真实记录首次同步 P50 19.14s，其中逐样本归档约 8.8s；峰值 RSS 中位 291.6MiB、最大 305.4MiB。JSON 编码和批量刷盘已改善，重复同步约 420ms。

服务端 S60/S240 的历史 P95 5646ms/31036ms 来自 `scripts/local/test.sh integration`，该入口默认 `-race`；handler 计时还不含认证、真实 socket。保留这些数字，标注为 **race 构建、handler 内部测量**，补普通构建基线之后再签性能结论；不能直接声称发布构建一定会更快或已经达标。

### 1.3 本轮不变的工程边界

1. 原生 iOS、只读 HealthKit；不新增 Web、Watch、桌面客户端，不扩大 WorkoutKit 授权。
2. Go 模块化单体、PostgreSQL 18、pgx/v5、sqlc；API/Worker 同镜像。
3. 原始事实、删除屏障、提交日志、dirty/job 和最终回执在同一事务提交；dataset 行锁定序，不用 sequence 当下载水位。
4. 上传不可变、同字节重试原回执、删除优先、快照缺席不删除；旧 UUID 不复活。
5. 不支持字段不能丢弃；现有 registry 未支持的 metadata 阻塞受影响对象并保留原始恢复材料。
6. 同步不依赖备份、恢复、全库摘要或全量重算；HTTP/application 不导入管理工具。
7. 不增加上传槽/连接池/默认请求预算，不关闭数据库 durability，不通过截断、降采样、缩短窗口达标。
8. 开发、回归和性能测试全部在本机 Mac；常规测试只用合成数据。真实只读验收仅在已有明确授权的设备/范围内执行，没有授权则列为待验收，不从历史报告推断授权。
9. 不修改 `../running`、原架构方案、旧迁移、既有回执与 pending 包；不自动部署、清空原状态、迁移真实数据、commit/push 或激活模型。

## 2. 当前实现定位与改造范围

| 区域 | 当前定位 | 本轮工作 |
| --- | --- | --- |
| 发现/组包 | `ios/RAI/App/SyncCoordinator.swift` 的 `discoverPage`、`samplesForBundle`、`performSync` | 规范化捕获贯穿发现与组包；减少 HK 对象往返；固定捕获边界。 |
| HK 查询 | `ios/RAI/App/HealthKitClient.swift`，`HealthKitReading` | 复用现有 descriptor；独立类型并发、分页串行、返回有界结果。 |
| 本地捕获 | `Sources/SyncCore/{CaptureSegments,HealthDiscovery,DiscoveryReuse,OutboxStore,HealthIntegrity}.swift` | 新规范化格式与旧归档双读；单写者、背压、恢复和 GC。 |
| 上传 | `Sources/SyncCore/{WorkoutBundleDelivery,WorkoutBundle,Wire,RAIAPIClient}.swift` | gzip-only 客户端传输、旧 pending 衍生、流式压缩与文件校验。 |
| SQL | `apps/server/internal/storage/postgres/{workout_bundles,bundle_batching,bundle_integrity}.go` | 集合式检查保留，比较固定形状批量写与 COPY/staging。 |
| 解码 | `apps/server/internal/syncing/bundle_decode.go`、`httpapi/{server,bundle_body}.go` | 减少反射/Token/重复解析成本，保持严格 schema 和限额。 |
| 测试工具 | `scripts/local/{test,lib}.sh`，服务端 sync-speed harness，App 测试与性能工具 | 普通构建测速独立入口、真实 HTTP、并发计数、内存与失败统计。 |

以上是允许定位范围，不要求为改文件而修改所有文件。可新增小型内部 capture/scheduler/SQL helper；不要引入第二套 Outbox、旁路上传 API 或大型依赖框架。

## 3. R0：先校准基线与计时

### 3.1 分开正确性与性能构建

- `integration` 与 `gate/full` 的 race 正确性检查保留。
- 新增本机 `syncperf` 或独立同义入口，使用隔离 PG18、普通 Go 优化构建，**不带 `-race`**；不设置 `-N/-l`，也不能让外部 GOFLAGS 意外带入 race。
- 性能入口记录构建信息、实际 race 状态、Go/PG/Swift/Xcode/设备版本、CPU/热状态、预算、pool、上传槽、durability 和 fixture digest。
- 分开输出：普通构建真实 HTTP 总耗时、handler 分段、race 诊断耗时。socket/TLS/认证是否包含必须明示；主验收用真实 TCP HTTP、真实 handler、真实 PG。
- 复用生产入口、认证与限流，不用直接调用 Store 代替端到端。微基准可绕开网络，但单列且不能代签总时延。

### 3.2 固定负载和比较方法

沿用第一轮 S20/S60/S120/S240 生成器及字段覆盖，固定版本/seed，新增一档 **S60H 密集结构**：约 10,824 samples、10,824 series points、3,491 route points，携带全部支持的来源/metadata 字段。它是纯合成负载，用于接近历史密集记录的结构成本，不复制真实健康值或原包。

每次新导入使用新对象身份或新隔离 dataset；前后用相同结构、数量、类型分布和字段内容种子。不能比较 4.13MB 的 S60 与 8.57MB 的密集真实记录后声称提速倍数。原包重试、空增量和首次导入各有独立计数。

每档先 3 次预热，至少 30 次计时新导入，冷启动至少 5 次单列。nearest-rank P95=`ceil(0.95*n)`；成功与失败数量都保留，有失败则不能签“该档通过”。不得排除慢请求、429 等待或超时后只统计成功者并宣布达标。

CPU/heap/SQL profile 使用合成数据生成，公开产物只含技术计数与耗时，不导出健康值、真实 UUID、source/device 唯一标识、凭证或 raw payload。

## 4. R1：先固定捕获与 ACK 确认边界

并发改造之前补充 F19 同类测试：在某类型采集完成后、其他类型尚在采集时注入新 sample、子删除和父删除。

- 不能在组包末尾读取“当前最大 generation”并直接认定所有变化都已进入本包。当前代码存在该读取点，应以测试证明或修正实际覆盖关系。
- 每个 producer 标记本次成功消费的事件身份、类型内 cursor 与捕获 generation；单写者据此发布 immutable capture manifest。
- ACK 只确认 capture manifest 真实包含或明确删除的工作。后续变化继续 pending，不被旧 ACK、重启或 `isAlreadyDelivered` 吞掉。
- 不引入“多个并发任务共享递增整数即可证明完整”的替代假设。可采用逐流边界/事件集合或等价的可验证实现，保持本地格式有界、兼容。
- 父删除仍优先于任何上传；删除竞争必须在真实隔离 PG 运行两种锁顺序，不能仅测 mock。

本阶段通过是开展并发与更激进复用的前置条件。

## 5. R2：正常样本路径彻底退出 NSKeyedArchiver

### 5.1 规范化捕获格式

新增有版本的 `NormalizedWorkoutCapture` 或等价内部表示，写入同一 Outbox 的不可变 segment。每条至少保存：

| 内容 | 要求 |
| --- | --- |
| 身份与范围 | kind/type/UUID、父 Workout、原 query fingerprint、scope/epoch、类型内 ordinal、capture 版本和边界。 |
| 原始事实 | 原有单位、精确时间、数值/类别、已支持 metadata/native carrier 与来源 descriptor；不生成缺失值。 |
| 多值 sample | parent UUID、原始 sample count、series 原始点及顺序、查询是否实际完成。 |
| 删除 | 明确 HKDeletedObject 身份、固定查询范围和已有父关系；不能从空结果补造删除。 |
| 恢复证据 | record/segment 数量、长度、digest、seal、coverage；文件与引用发布顺序。 |

格式是本地持久状态的加法版本，不改变 Workout wire v3。源 HealthKit 对象读取后尽早规范化并释放，组包直接读规范化事实，不还原 HKQuantitySample 再重新规范化。

旧 `archiveFile`、旧 v1 capture segment、旧 pending bundle 继续可读。旧 archive 转换按需、有界进行，不全量重写历史；读取失败保留原文件和错误，不能上传少样本的包。新 segment 应按批次流式读取与校验，避免为每条 record 重新读取、重新计算整个 segment 的 digest；复用校验结果须绑定不可变文件与已封存身份，损坏检测仍必须有效。

### 5.2 “彻底移除”的可检查边界

- 全支持、无异常的正常样本路径，`sample_archive_calls=0`、`sample_unarchive_calls=0`，不因 fallback 改回逐样本 archiver。
- 不得用整页/整数组 NSKeyedArchiver 包住所有样本规避计数；检查其根对象包含的实际样本数，使用调用计数与 Instruments/profile 双重确认。
- HKQueryAnchor 的小对象编码、必要的一条根 HKWorkout 归档和旧格式兼容读取可保留，单独计数；不能把它们扩展成完整样本数组。
- 不支持的 metadata/类型可以保留**受影响的阻塞原始材料**，随后停止该对象提交；这是异常留存路径，不能用作普通成功路径的归档 fallback。
- Daily/VO₂ 不顺带重构，但 shared helper 改动必须保证它们的原有行为和恢复测试不退化。

### 5.3 无损、身份与覆盖

先用合成 fixture 冻结旧 normalizer、发现 fingerprint、canonical event 与 wire 的参考结果，再比较新路径。不能只比较长度；逐字段、逐点、canonical bytes 与 hash 均要对照。

格式变化不等于源事实变化；不能因为 archive hash 换成新格式 hash 就批量制造新语义事件。既有 pending 的 exact JSON/hash 完全不改。新捕获结果按既有 type 顺序与规定的 sample 顺序合并，route/series 点不排序、不去重、不降采样。

空查询、权限不可见、查询失败、unsupported、partial 和完整为空按现有合同分别表达。禁止通过删除 `queryCompleted` 校验或把未查 series 写成 complete 来减少读取。

### 5.4 内存和持久化

规范化→批次封存→有界队列→单写者发布；不能同时保留完整 HK 对象数组、archive Data、解码对象、JSON 字符串和 gzip Data 多份副本。

沿用 512 条/256KiB/250ms 初始批量阈值；批次之间释放临时对象，必要时采用明确生命周期的 autorelease pool。保留 seal、校验、原子引用、旧 cursor 和 GC 在途写者保护。

文件损坏、写满、保护文件不可读、封存/发布失败、ACK 保存失败都必须保持旧权威状态；模拟故障与真实设备故障分别说明。文件原子写通过不能宣称物理掉电通过。

## 6. R3：HealthKit 1/2/4 有界并发读取

### 6.1 调度模型

1. 来源合格和根范围固定后，独立 quantity type 的查询可以并发；同一流的分页和 anchor 链仍串行。
2. 初始生产候选并发度为 **2**，测试 1/2/4 后按收益和内存决定最终值。4 是实验上限，不默认全开。
3. 单个全局 capture 预算约束当前所有活跃 HK 查询，包括 series/route；不能“每类型 4 个”叠加成几十个，也不同时展开全历史所有 Workout。
4. 发现与组包使用同一调度/捕获结果，覆盖实际手动入口的前置 discovery；只并发最终已缓存的组包无效。
5. producer 生成独立查询对象和独立缓冲，结果交给 Outbox 单写者，禁止多个 Task 同写一个 FileHandle/state。
6. producer→writer 队列初始总预算 **2MiB**；达到预算暂停生产或减少活跃任务。预算按实际 payload 字节而非元素个数估计，含在途页和各 producer 缓冲。
7. merge 按固定类型、记录 ordinal、原点序输出，不按完成先后改变 wire 顺序和 identity。

优先复用已有分页 anchored 读取与 series 流式结果；不能同时让多个 fallback 查询各返回完整的50,000样本数组，然后仅限制它们之后的队列。页缓冲、producer持有对象和压缩缓冲都计入资源观测；总samples、series points、route数量/点数沿用包级上限，由汇聚者统一累计，不能按每个worker分别放大限额。

实现前检查本机 SDK 与 Swift 6 的实际 isolation/Sendable 声明和最低 iOS18 兼容性。不通过全局 `@unchecked Sendable`、禁用严格并发检查或多个任务共享非线程安全 query/encoder 来消除编译错误。actor 方法可重入不代表系统查询一定加速，必须测真实查询重叠。

### 6.2 取消、失败与公平性

- 取消停止未开始任务，正确结束/取消在途查询，刷出或保留可恢复边界，释放槽与字节预算；重启能够接续。
- 一个类型失败不能撤销已保存的其他类型事实；其 coverage 明确 failed/partial，原协议能表达的合法部分包继续按合同处理。
- root 删除、session/epoch 改变立即使旧 capture 失去继续提交资格，pending 原包按既有删除/会话规则处理。
- 用户任务可抢占低优先级全局补齐；Daily/VO₂ 不被长期饿死，完整性证明不阻塞普通单条 ACK。

### 6.3 并发是否通过

合成 reader 用可控 barrier 验证 maxActive 与 overlap，测试不能依赖 flaky 的绝对时间 sleep。实际 HealthKit 收益用授权后的只读真机复核。

最终并发设置要求：同一支持 fixture 输出与串行参考一致、active≤配置、取消后 active=0、无新错误，且内存门槛通过。真机若可测，应争取 HK 阶段 P50 较同版串行下降≥20%；没有收益则默认保留 1，并将“并发实现已具备/实机收益未证实或无收益”单列，不能为了表面完成强制 4。

## 7. R4：新版客户端强制 gzip

### 7.1 传输决定

- 新版客户端对 WorkoutBundle 的所有 POST 使用 multipart 的单 `file` part：`Content-Type: application/gzip`，推荐文件名 `workout.json.gz`；顶层仍为 multipart，不是顶层 `Content-Encoding`。
- 包括首次上传、重试、旧 plain pending 恢复和显式子删除 bundle。原 plain JSON/multipart 仍可作为本地权威文件，但不能作为新版客户端的 wire fallback。
- 压缩文件>8MiB、压缩失败、损坏或磁盘错误时停止发送，保留原 JSON、原 bundle hash、恢复状态与明确错误；不拆分原包、不丢字段、不改 UUID 来规避上限。
- 握手未声明既有 gzip 能力时返回可诊断的不兼容状态，保留任务，不静默发 plain。
- 服务器继续接受历史 plain v3，保持旧客户端兼容；本轮不改成服务器拒绝所有 plain，也不新增 wire protocol 版本。

### 7.2 identity、限额与资源

`bundleSHA256` 仍绑定解压后原 JSON 的精确字节；gzip/multipart 自己的 wire hash 独立。压缩重建不得重读 HealthKit、重建另一份 JSON 或修改 observation 时间。

使用有界文件压缩/读取，原文件先持久保存，gzip 派生文件后封存与发布；GC 同时保护原包和派生文件。保留当前 gzip 单成员、CRC/ISIZE、decoded32MiB、compressed8MiB、wire/framing 与 100:1+64KiB 解压比上限。压缩率极高/极低的合成输入也测边界；超限如实失败，不通过尾部填充、串联成员、提高上限或明文回退绕过。

同一 JSON 不同合法 gzip header/压缩实现由服务端得到相同原回执。gzip 不是减少所有服务器计算的保证，报告压缩、网络、解压与导入各自时间。

## 8. R5：SQL 集合化深化与严格解码提速

### 8.1 先测瓶颈

以普通构建基线采样 Go CPU/alloc、参数准备、SQL server duration、锁等待、WAL、COMMIT 和 prepared plan。不能把 `sample_writes` 整个 wall 阶段都称为数据库执行时间。

当前已有≤1000键集合查询与多行 INSERT，继续保持；本轮至少实现并比较以下候选中的两种，其中必须有 COPY/staging 的可运行试验或明确的权限/技术阻断证据：

| 候选 | 比较内容 |
| --- | --- |
| 当前多行参数 INSERT | 同输入普通构建基线；参数数、解析/规划、写入与 WAL。 |
| 固定形状 `unnest`/数组参数 | 降低巨大 VALUES 参数列表和 SQL shape 变化；验证 binary type、时间、nullable 数值和精度。 |
| 事务内 COPY→staging→集合校验/写入 | staging 记录 type/UUID/ordinal/core/hash；批量 join 屏障与身份、sample/association 两投影集合写入。 |

COPY 不是必须进入生产的最终 API；最终采用者必须有同条件收益。若 COPY 未胜出，保留实验数据与拒用原因，不能说“SQL 未使用 COPY 所以没完成”。如果所有候选均无收益，报告实际结果和未达目标，继续处理实测 CPU/解析瓶颈。

### 8.2 事务与权限

- 保持 dataset lock→fencing→父/子屏障→身份/关联→事实→日志/dirty/job/回执→COMMIT。
- COPY/staging、sample 与 association 所有步骤处于**同一事务**，失败或 COMMIT 失败无半包、孤立关系、孤立回执。
- staging 只保存本包有界数据并在事务后释放；临时表使用固定受控 schema/列和连接生命周期，不能残留跨 dataset/请求数据。
- 必须用受限 API 运行角色验证权限；若 TEMP 不允许，选择固定形状数组方案或单独说明所需最小权限。不能用 owner/superuser 通过测试后冒充运行角色可用，也不为临时性能实验放开永久 DDL 权限。
- 不能用 `ON CONFLICT DO NOTHING` 忽略身份冲突；允许的 append-only observation enrichment 与同 UUID 核心变化仍分别判断。
- 精确重放仍先检查父/子 tombstone；不能看到原 receipt 就绕过删除。
- 保留现有事务局部 custom plan、语句/事务预算，除非有明确对照实验支持调整；不得再次仅抬超时处理 generic plan。
- 新索引确有证据时使用当时最高号的加法迁移，不编辑旧迁移与 checksum；本轮同步不依赖管理/迁移工具在线运行。

### 8.3 严格 JSON

先缓存反射 schema/字段信息、减少 Token 与字符串/数字中间分配。必要时实现单遍严格解析+typed binding，但必须保留重复/未知/大小写错误字段、missing/null、类型、数值边界、深度/数量/单位/来源验证；语义验证仍在 Store 一次执行。

不能仅替换为宽松 `json.Unmarshal` 或关闭重复字段检查。新增依赖须证明必要、可维护和最低环境兼容；不为此升级整个 Go 模块栈。

## 9. 功能与故障硬门禁（R2-F01–R2-F32）

第一轮 F01–F32 的合同继续回归；下表是本轮新增/加强的验收，不能仅引用上一轮 PASS。

| ID | 场景 | 通过条件与最低证据 |
| --- | --- | --- |
| R2-F01 | 正常支持样本的发现→组包 | archiver/unarchiver 样本调用数均0；没有整页数组绕过；App 运行路径计数+profile。 |
| R2-F02 | 新旧捕获同源结果 | event、source、metadata、时间/数值/负零、series/route 逐点、canonical/hash 对照一致；合成双端 fixtures。 |
| R2-F03 | 旧 archive/旧segment/新格式混合 | 可恢复，不修改旧 pending hash/回执，不全历史重采；重启测试。 |
| R2-F04 | unsupported metadata/native carrier | 仅受影响对象阻塞并保留原始材料；无丢字段成功包；App+HTTP/PG。 |
| R2-F05 | 批次截断、seal/引用发布失败 | cursor不提前，孤儿不被当事实，在途文件不被GC；文件故障注入。 |
| R2-F06 | ACK 丢失/本地ACK保存失败 | 原包/原hash恢复，事实/revision/job不重复；真实Swift→HTTP→PG。 |
| R2-F07 | 捕获中后到 sample/子删除 | 不把未入包的变化标为delivered；重启后继续交付；App barrier+真实PG。 |
| R2-F08 | 父删除与并发上传 | 两种锁顺序、旧回执恢复均不复活；真实隔离PG并发。 |
| R2-F09 | 缺基线或未耗尽分页 | 不把delta当完整，不重置有效anchor；App合成多页。 |
| R2-F10 | 并发1/2/4结果 | 与串行参考相同，输出不受完成先后影响；受控barrier测试。 |
| R2-F11 | 实际并发与全局限额 | 观察到独立查询重叠，maxActive≤K；scalar/series/route共享预算；App计数。 |
| R2-F12 | 同类型多页anchor | 同流有序耗尽，无页跳过、cursor串用或跨scope复用；App合成。 |
| R2-F13 | 单类型失败 | 其他成功类型不丢，失败coverage准确，不伪造完成；App+PG。 |
| R2-F14 | 取消、抢占与槽泄漏 | 在途查询正确结束、active归0，budget释放，恢复可用；App合成。 |
| R2-F15 | 背压/慢磁盘/大批次 | 队列字节与页缓冲有界、无无限任务与内存增长；计数+峰值采样。 |
| R2-F16 | 全新上传与全部重试 | wire file part始终gzip，0 plain发送，服务器验证解压hash；真实socket。 |
| R2-F17 | 旧plain pending恢复 | 原JSON不变，衍生gzip发送，无HealthKit读取；Swift→HTTP→PG。 |
| R2-F18 | gzip失败/损坏/超8MiB/能力不支持 | 0明文fallback、0错误成功ACK，原包保留，无无穷热重试；故障注入。 |
| R2-F19 | gzip bomb/CRC/截断/串联成员/尾部垃圾 | 保持既有限额与拒绝语义，不进入Import；HTTP+PG。 |
| R2-F20 | 不同gzip字节/旧客户端plain | 同JSON返回原回执，旧plain兼容有效；真实PG。 |
| R2-F21 | 集合插入与旧导入基线 | 同合成输入输出、计数、冲突码、raw/receipt语义一致；保留基线参考，真实PG对照。 |
| R2-F22 | 1000行边界、50k样本、多series/route | 所有事实与关联完整，不按批次丢行/错父；真实PG。 |
| R2-F23 | UUID内容冲突、跨父关联、enrichment | 原冲突/允许规则不变；整包回滚；真实PG。 |
| R2-F24 | COPY中断、staging/两投影写入/COMMIT故障 | 全事务回滚，0半包/孤立job/ACK；真实PG故障注入。 |
| R2-F25 | 受限角色、池复用、TEMP残留 | 不提权，不跨dataset泄漏，临时状态清理；runtime role+真实PG。 |
| R2-F26 | custom/generic plan与冷/热连接 | 重复执行不出现既有大包计划退化；计划/次数/失败全记录。 |
| R2-F27 | 严格JSON全部拒绝矩阵 | missing/null/duplicate/unknown/类型/数值/限额行为不弱化；unit+HTTP/PG。 |
| R2-F28 | 来源过滤与既有删除 | 第三方新记录0详细采集/上传，未知不冒充排除；必要已有删除仍处理；App+PG。 |
| R2-F29 | 缺HR/室内无路线/无功率 | 合法原始事实可保存，分析缺失如实显示；未知字段不顺带放宽。 |
| R2-F30 | 无变化/迟到变化/每日健康流 | 无变化无新POST，迟到重开，不因并发/缓存漏采；Daily/VO₂回归不退化。 |
| R2-F31 | v2证明两代、丢ACK、中断续跑 | 未完成不能伪造receipt；无变化时复用已ACK包，第二代bootstrap0；Simulator→HTTP→PG。 |
| R2-F32 | 性能入口与构建口径 | 普通构建、race、首次/重复/缓存、默认/benchmark预算分别可辨；有失败不能签达标。 |

说明：真实磁盘满、FileProtection、物理掉电未执行时保持 NOT RUN；文件故障注入不能代签。第一轮 P10 只有 watchdog 停顿证据，本轮 UI操作P95另测。

## 10. 性能与资源验收（R2-P01–R2-P14）

### 10.1 计时口径

- `T_user_ack`：点击同步已选记录→必要调度/发现/采集→编码/保存→gzip→网络/限流/认证/数据库COMMIT→本地ACK持久保存与UI更新。授权弹窗、浏览列表与分析发布独立记录。
- `T_server_ack_http`：普通构建、真实TCP请求的首导入完整服务端请求处理；客户端roundtrip与server phase都记录，包含认证/准入和body处理，不把内层handler数字冒充完整HTTP。
- HK阶段分离 query等待、normalization、持久化、series与route；并发后总耗时是实际elapsed，不能将重叠阶段相加。
- 同进程单调时钟计时；不减跨设备wall clock推算网络。所有限流等待保留。

### 10.2 门槛

以下为目标，不是当前成绩。无法达到时记录 FAIL 和剩余瓶颈，不降低目标后改写通过。

| ID | 场景 | 通过条件 |
| --- | --- | --- |
| R2-P01 | 正常S60/S60H捕获 | 样本archiver/unarchiver调用均0；capture normalization+持久化阶段P50较R0下降≥70%；该阶段不是仅统计移除后的空计时器。 |
| R2-P02 | 并发查询1/2/4 | 全部结果一致且资源门槛通过；授权真机能测时HK阶段P50争取下降≥20%。无收益/无真机分别标注，不声称加速。 |
| R2-P03 | S20–S60首次用户等待 | Release、指定iPhone12ProMax同档设备、稳定LAN、本机API+Worker+PG、默认预算：P50≤5s、P95≤8s。 |
| R2-P04 | S60H密集档 | 同配置目标同上；另与同档第一轮/本轮R0比较，不拿稀疏长时记录替代。 |
| R2-P05 | S120/S240 | 同配置分别P50≤8s/P95≤12s、P50≤15s/P95≤22s；缺真实记录时用设备合成reader单列验证。 |
| R2-P06 | 无变化重复S60 | P50≤2s/P95≤4s，0新POST/import/job；必要新变化发现不能被缓存永久跳过。 |
| R2-P07 | 已准备原包恢复 | HK查询0，原JSON/hash一致，gzip传输100%，网络与恢复耗时单列。 |
| R2-P08 | 普通构建HTTP→PG的S60 | n≥30的新导入，P95≤1s，0失败；默认预算等待不能隐藏。 |
| R2-P09 | 同上S240 | n≥30，P95≤4s，0失败；不扩大pool/slot/超时预算。 |
| R2-P10 | SQL候选 | 最终写入阶段P50/P95不回退；若SQL为R0主要瓶颈，目标写入P50下降≥30%。提供当前VALUES、数组与COPY试验比较/阻断证据。 |
| R2-P11 | gzip策略 | 新版客户端所有合法成功POST的gzip比例100%；压缩失败/超限明文发送0；正常S60/S60H压缩P50≤200ms。 |
| R2-P12 | iOS内存 | Release进程峰值RSS：S60与S60H≤256MiB，S240≤450MiB；取每次运行峰值的最大值验收，另报中位数。不得用结束时RSS或平均RSS代峰值。 |
| R2-P13 | API与UI | 两上传槽压力API峰值RSS≤512MiB；真实滚动/暂停/取消响应P95≤100ms、无>250ms持续主线程阻塞；watchdog与手势证据分开。 |
| R2-P14 | 默认预算批量 | 10条S60 `T_batch`≤60s；100条≤360s，包含429等待、所有失败/重试；500条做扩展性报告，不套用高预算轻量21s成绩。 |

默认上传设备预算30/min、burst2；dataset60/min、burst4；设备上传槽1、进程2。普通HTTP时延矩阵可以按默认预算节奏发包或每隔离dataset固定独立预算，必须记录实际方法；不能偷偷改为高预算。吞吐微基准可单独采用显式有限benchmark预算，但不签R2-P14。

### 10.3 足够样本与真机边界

1. CPU/存储/网络结构先在本机模拟器/授权的设备合成reader验证，不向真实HealthKit写入合成数据。
2. 合成每档≥30次；真实只读记录重复测量须明确授权并报告记录结构/字节数，真实P95签核至少20次同口径样本，冷启动与热启动分开。
3. 缺S120/S240真实记录不自行读取更多私人历史，不把稀疏108分钟记录当S120密集档；设备合成与真实HK分别结论。
4. 同条件A/B使用相同构建优化级别、支持字段、数量/结构、网络、预算、数据库durability与基线；允许不同隔离身份，不能使用重放命中假装首导入。
5. 内存须测实际进程峰值及工具采样频率；只在函数末尾采样不能证明P12。长循环检查没有持续增长。
6. RQRun公平对标仍单列：没有同记录/相同首次状态和完整起止计时，不签“已达到RQRun”。先验本方案目标。

## 11. 阶段顺序与交付边界

| 阶段 | 工作 | 退出条件 |
| --- | --- | --- |
| R0 | 复核入口，新增普通构建测速，固定S60H/计数 | 基线和race状态真实可复现，profile齐备。 |
| R1 | 捕获/ACK边界与并发变更测试 | 后到事件不被误确认，删除竞争真实PG通过。 |
| R2 | 规范化捕获、旧格式双读、内存 | 正常路径样本归档0、无损与恢复通过。 |
| R3 | 查询调度1/2/4、背压、取消、确定性merge | 实际并发和限额可证，结果与串行一致，峰值不超标。 |
| R4 | gzip-only客户端、流式派生和所有失败分支 | 0明文回退，旧pending正确恢复，服务器历史兼容。 |
| R5 | SQL候选与解码提速 | 真实PG事务/受限角色/计划/性能对照，保留最优安全方案。 |
| R6 | 综合回归与验收报告 | 功能、性能、真机和未验证项分别签核。 |

按阶段推进无需逐阶段重新请求用户批准。若技术实验失败，保留证据、调整实现并继续可执行工作；不要只写完设计就停止。真实设备不可用不阻塞本机合成、真实隔离PG与构建任务。

本轮普通单条速度验收不要求重新跑完整真实历史completeness；但恢复逻辑与模型incomplete语义必须回归，已有15分钟终止场景的可恢复能力用合成中断验收。真实全量证明仍是单独待验收项，不提前标PASS。

## 12. 测试与可复现命令

在 `/Users/ta/RAI` 执行。以下现有入口可直接使用；新增测速/测试名称必须实现并核对执行数后再报告通过。

```bash
# 日常反馈
scripts/local/test.sh quick

# Swift纯域与恢复/编码回归
scripts/local/test.sh swift

# 合成App测试与真实Simulator→HTTP→PG
scripts/local/test.sh simulator
scripts/local/test.sh e2e

# 事务变更专项；按新增测试名补充选择
scripts/local/test.sh integration -run 'TestWorkoutBundle|TestWorkoutDelete|TestPhase3|TestWorkoutCompleteness' ./integration

# 实际Swift恢复、gzip与故障（沿用现有入口并补本轮场景）
scripts/local/test.sh integration -tags=integration,swiftclient -run '^TestWorkoutReliabilitySwiftClientPostgres$' ./integration

# 必须的完成门禁：race、vet、依赖边界
scripts/local/test.sh gate

# R6综合签核：全量隔离PG及既有完整门禁
scripts/local/test.sh full
```

R0中新增的目标入口示例（**当前不存在，必须实现后运行**）：

```bash
# 普通构建、真实TCP、默认准入、durability全开；参数由新增入口支持
scripts/local/test.sh syncperf --profiles S60,S60H,S240 --iterations 30 --warmups 3
```

新增入口不能改写 `integration` 为默认非race，也不能用cache命中或SKIP冒充30次。COPY候选、解析器/并发基准用同一套fixture及明确模式，保存完整命令与每次状态。

同时检查：Go构建、gofmt、sqlc生成稳定性、受影响OpenAPI/合同lint、iOS18最低目标兼容与正常Release构建。使用DEBUG门控的Release验收仪器时记录宏/Info.plist差异，另证明正常Release不会打开测试seam、合成reader或任意HTTP例外。

### 文档与基线保护

- 只追加 `docs/IMPLEMENTATION.md`；第一轮结果和原方案原文不改。新结果文件记录本轮校准后的解释，不删除历史FAIL。
- 入口记录git status/HEAD；保留dirty和用户文件。旧参考实现可从已提交基线提取至隔离测试参考，不覆盖当前源码，不使用reset/stash恢复整个工作区。
- 本机PG测试清理残留cluster/数据库；controller只处理自身operation ID与namespace，不能清原云端或其他本机验收状态。
- 日志/证据执行现有隐私扫描并增加本轮覆盖；不把凭证、配置全体或原始数据输出到工具/报告。

## 13. 交付物与最终签核

后续执行应新建：

1. `docs/WORKOUT_SYNC_SPEED_ROUND2_RESULT.md`：实际改动、阶段结果、故障历史、性能前后对照、残余风险、回退及未验证项。
2. `docs/evidence/workout-sync-speed-round2/`：入口环境、普通构建/race区分、固定fixture清单、1/2/4对照、归档调用计数、gzip抓取断言、CPU/heap/SQL计划、事务故障、峰值内存与UI证据。
3. 可复现的普通构建测速入口、新测试和最小代码改动；必要的加法式本地格式/合同说明。
4. `docs/IMPLEMENTATION.md` 追加真实结果，包含所有未达标和NOT RUN，不修改历史结论。

逐项标记 `PASS / FAIL / PARTIAL / NOT RUN / NOT VERIFIED`；报告测试名、实际执行数/跳过数、构建口径、测量n与工具、失败原因。安全项任一失败不能由速度抵消。

| 签核 | PASS必要条件 |
| --- | --- |
| NORMALIZED CAPTURE | 正常样本archiver/unarchiver=0；无损、旧状态兼容、恢复通过。 |
| BOUNDED CONCURRENCY | 1/2/4确实实验、正确性/背压/取消无问题，默认值有依据；实机收益单列。 |
| GZIP ONLY CLIENT | 所有新版客户端WorkoutBundle POST为gzip、异常无plain fallback，旧pending不改identity。 |
| SET BASED IMPORT | 实际集合SQL及候选对照、受限角色、同事务/删除/幂等回归通过；COPY是否采用单列。 |
| FUNCTIONAL SAFETY | R2-F01–F32及受影响旧合同通过；未执行项不能签全通过。 |
| PERFORMANCE | 本轮普通构建口径R2-P目标有证据；未达目标继续标FAIL。 |
| REAL HEALTHKIT | 仅实际授权且执行的设备/范围可签；设备合成不代签真实HK并发收益或端到端。 |
| RQRUN / DEPLOYMENT | 公平对标、生产部署本轮默认未执行，分别NOT VERIFIED/NOT RUN。 |

### 回退要求

本轮可用内部策略切回串行读取或旧安全SQL方案，仍必须保持新版客户端gzip-only；回退不能用明文规避压缩失败。新版本内保留旧归档和新规范化格式的读取能力，已准备原包优先恢复。

如果新格式影响旧二进制读取，明确“新版本内策略回退”和“旧二进制回退”的区别。未经验证不承诺旧程序能读新格式，不能通过清Outbox或重置anchor完成回退。

## 14. 本文交付状态

- 第二轮计划及执行提示词：已编写，文档检查和本机完成门禁结果追加到实施总记录。
- R0–R6第二轮代码实现：**NOT STARTED**。
- 第二轮SQL/捕获/并发/gzip强制行为验收与性能目标：**NOT RUN / NOT VERIFIED**。
- 本次文档工作未读取新真实健康数据、未部署、未迁移或清理真实状态、未commit/push。
