# M4-B1 — External VO₂max 实施计划与冻结验收

日期：2026-11-04，Asia/Shanghai。正式合同为 [VO2MAX_EXTERNAL_CONTRACT.md](VO2MAX_EXTERNAL_CONTRACT.md)。本文件冻结 B2/B3/B4 的实施范围与门禁，**不授权自动开始下一阶段**。

```text
Algorithm Decision: B
M4-B1 RESULT: PASS WITH RISKS
B2: NOT STARTED
B3: NOT STARTED
B4: NOT STARTED
REAL DEVICE ACCEPTANCE: DEFERRED
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
RAI OWN VO2MAX MODEL: NOT IMPLEMENTED
```

## 1. 实际 baseline 与证据差异

执行 `git status --short`、`git rev-parse HEAD`、`git branch --show-current`、`git diff --stat` 后确认：

| 项目 | B1 实际结果 |
| --- | --- |
| branch / HEAD | `main` / `46968332380f9145945bc989325b98bc40454d9a` |
| 进入时 tracked diff / untracked | 均为空；无已有用户dirty工作需要覆盖。 |
| migrations | 00001–**00023_performance_v1.sql**；00024/00025不存在。 |
| 原有文件基线 | 849个tracked/nonignored文件，编辑前逐项SHA-256；最终逐项验证，不仅看git diff --stat。 |
| M4-A全文 | 已定位仓库外原报告，与用户补充全文附件字节一致，SHA-256为290c966caf325a6be61f091827dd28c51f437cd02c8c76c28cf0f868bf2ec120；READY WITH RISKS / Decision B。初稿“报告全文缺失”结论更正。 |
| 当前源码对M4-A | HEAD均为46968332380f9145945bc989325b98bc40454d9a；849个原有文件与M4-A离场manifest对应hash全部一致。最高23、Daily三类型、VO₂独立链路未实现的结论不变。 |
| M5A真机范围 | M5A-RD有HRV/RHR/Sleep证据；不作为VO₂真机验收。旧Daily合同的历史NOT RUN不能抹去，也不能错套到本轮VO₂。 |
| SDK | Xcode26.6(17F113)，iPhoneOS26.5，项目iOS18；第4testType在HKMetadataEnums.h中标iOS26。 |
| 原有权限 | API无artifacts/inputs读写，Worker缺daily_health_coverage SELECT；B3须明确追加最小权限。 |

复核源码入口与引用见正式合同§1。Header和常量检查均未打开用户健康库；没有生产数据库访问或真实健康值输出。全局默认CLT未切换；只对本轮常量probe设置DEVELOPER_DIR。

M4-A依据：[完整报告](/Users/ta/.codex/m4a-review-2mxxz5ia/M4A_VO2MAX_MODEL_FEASIBILITY_REVIEW.md)、[Git baseline](/Users/ta/.codex/m4a-review-2mxxz5ia/git-baseline.json)、[离场manifest](/Users/ta/.codex/m4a-review-2mxxz5ia/exit-sha256.json)、[只读验证](/Users/ta/.codex/m4a-review-2mxxz5ia/readonly-verification.json)、[SDK检查](/Users/ta/.codex/m4a-review-2mxxz5ia/sdk-check.json)。原报告及证据不修改，也不复制为第三个仓库交付文件。本次逐项复核报告第1–20节，设计建议与B1冻结差异详列于正式合同§1.1；849文件核对不冒称覆盖M4-A含.git/ignored/symlink的55,065条完整manifest。

M4-A的Simulator26.5与B1 iPhoneOS26.5五个相关Header逐字节一致；其iOS18 swiftc typecheck、503 test/subtest通过/4 skip与Go检查是M4-A历史证据，本次未重跑它们，不能转写为新VO₂实现测试通过。原报告所述14–60/14–65提供者文档范围差异也不转成raw通用拒绝范围或个人校准规则。

与历史总架构的关系：`RAI_MODEL_ARCHITECTURE.md`里的条件VO₂研究方案和M1 future VO₂资格原文保留；Decision B暂缓其数值实现。外部投影不消费这些Feature资格，不能为“利用既有模型”接上HR-reference、M2负荷或M3跑力。`docs/IMPLEMENTATION.md`按本次任务明确要求不修改；本轮真实证据记录在本文件。

## 2. 已冻结的实施决策

| 项目 | B1决定 |
| --- | --- |
| 协议 | 独立`vo2max-facts/v1`、独立端点/capabilities/DTO；本阶段不做Daily Facts v2。 |
| query | 独立anchor/fingerprint、首次固定90×24h下界、overlap-v1、只读vo2Max；不重置任何Daily v1状态。 |
| immutable replay | 原body bytes、batch与sequence链；raw/dirty/coverage/receipt原子；lost ACK取原receipt。 |
| metadata | 显式tagged union六类型；registry v1 allowlist见合同；Int64无Double中转；未知字段/enum/unit整页阻塞。 |
| provenance | 新provenance/2补Device.name及显式null；明确排除稳定设备标识，不声称完整HKDevice档案。 |
| 来源 | 四分类、版本规则与反证；第三方/手动raw可保存，current默认只允许Apple Watch estimate，显式epoch选择。 |
| 修订 | UUID内容不可变，SyncIdentifier不取代UUID；delete/new UUID正常处理，同UUID变内容冲突；真实Apple更新序列DEFERRED。 |
| coverage | VO₂独立流记录，query/sync/visibility/authorization四维分离；不把无可见样本解释为不存在。 |
| 投影 | modelVersion=rai.vo2max.external_projection/v1；只复制源值，confidence/parameterVersion/featureVersion均null；有值DB quality=partial，input schema/view/knownThroughRevision明确。 |
| 有界输入 | 最多1024个sample/tombstone refs、32个epochs、最新tie≤256；current出值仅<90日，域外只能有界说明expired/withdrawal，不用过期前驱补值。 |
| Freshness | <30日available，[30,90)日stale且可保留原值，≥90日expired且root value=null；入configHash；raw无年龄TTL。 |
| candidate | 独立external scope/family，复用共享analysis表；active=legacy；无HTTP产品/训练消费者或promotion。 |
| 自有模型 | `rai.vo2max.estimated/v1`继续NOT IMPLEMENTED；不注册假成功handler。 |

registry v1并不承诺所有现实metadata均已覆盖；例如number tag有正式编码但尚无准入key。未知真实metadata必须停在本地，而不是为了通过验收放宽map或删字段。Apple来源规则是描述符推断，不是官方完整bundle清单或设备认证。

## 3. B2 — HealthKit事实传输、删除与Coverage

目标：只建真实原始事实闭环；ACK明确awaiting_model_implementation；Worker不假装已生成VO₂结果。实施前必须由用户授权B2，重新检查Git/AGENTS/最高migration/已有dirty；不得据本计划直接执行。

### 3.1 文件范围（未来实施）

| 区域 | 允许计划修改/新增的文件 | 具体任务 |
| --- | --- | --- |
| SDK读取 | `ios/RAI/App/HealthKitClient.swift`，新增`VO2MaxNormalizer.swift` | 只读vo2Max授权、独立anchored query与observer；typed metadata、unit/provenance/availability；保留DailyHealthNormalizer原v1路径。 |
| 本地状态 | `ios/RAI/Sources/SyncCore/OutboxStore.swift`，新增`VO2MaxFacts.swift` | 新optional VO₂状态/blocked恢复记录；兼容旧LocalState；pending文件引用纳入GC，独立ACK解析与anchor提交。 |
| 调度/网络 | `ios/RAI/App/SyncCoordinator.swift`，`Sources/SyncCore/{ForegroundSyncScheduler,RAIAPIClient}.swift` | 能力协商与VO₂独立wake/retry；保持Workout/Daily共享预算公平，不因VO₂错误饿死三流；不承诺后台固定频率。 |
| iOS验收 | `ios/RAI/AppTests/NormalizerTests.swift`、新增SDK VO₂测试；`Tests/SyncCoreTests/VO2MaxFactsTests.swift`及必要Daily兼容用例 | 常量、synthetic样本、未知metadata、类型、availability、磁盘pending/ACK/kill/GC；不更改已有expected v1语义。 |
| Go DTO/API | 新增`apps/server/internal/syncing/vo2max.go`、`internal/httpapi/vo2max.go`及测试；既有router/handshake/store接口最小接线 | strict decoding、typed union、limits、保留原bytes、身份链；既有入口拒绝新增reserved key伪造。 |
| 存储 | 新增`apps/server/internal/storage/postgres/vo2max.go`；必要的`store.go/sample_batch.go`适配；`queries/core.sql`与sqlc生成代码 | 复用immutable/hash/delete流水线；删除前捕获source context；新VO₂coverage/dirty/receipt单事务；不enqueue analysis.sample占位成功。 |
| DB/权限 | 提议`migrations/00024_vo2max_facts.sql`；latest/checksums/sqlc模型；`deploy/postgres/roles.sql`的必要coverage适配 | 只做§5的raw增量，不提前加入B3 handler或结果。 |
| wire设计落地 | `contracts/openapi-v2.yaml`显式追加新endpoint/schema/capabilities | 原Daily v1/Workout DTO字段、版本、语义、例子不改写成新协议。 |
| 工程证据 | 新B2结果/机器证据；实施阶段按届时授权维护IMPLEMENTATION | 只记录synthetic/隔离DB/Simulator证据；不得冒称真机/生理通过。 |

表中新增路径是实施建议，不是已存在或已生成文件；B1未创建任何这些源码。iOS AppTests自动包含方式须按当前工程核实；不要为新增单文件无必要重生成整个xcodeproj。

### 3.2 B2顺序与退出门槛

1. 先固定跨Swift/Go合成wire golden vectors、旧LocalState/Daily v1 bytes/anchor兼容断言；定义supported registry，不通过unknown map兜底。
2. 编写DTO/normalizer与independent flow，再做API/storage/SQL；所有未支持输入返回明确错误。pending在network/capability错误后不消失。
3. 在全新临时PG18集群应用实际全部migration，用受限角色验证事务/receipt/删除/coverage；无生产DSN、无已有开发库重置。
4. Simulator synthetic完成query→normalization→immutable pending→HTTP→PG→ACK→anchor；kill/故障注入验证旧或完整新状态，不停在mock。
5. 全量Go race/vet/boundary、受影响Swift/SDK测试与旧Daily/Workout回归通过；生成代码diff、OpenAPI兼容、隐私与Git保留证据齐全。

B2通过只表示transport完整，不表示有external model、current值、真机数据或准确性。若未知metadata或相同UUID真实修订需要新语义，保持blocked并报告；不得静默调整冻结合同。

## 4. B3 — External Projection Candidate

目标：只投影外部事实，不重新估算VO₂，不污染M1/M2/M3，不激活产品。开始前要求B2门禁通过并有独立授权。

### 4.1 文件范围（未来实施）

| 区域 | 计划路径 | 实施内容 |
| --- | --- | --- |
| 纯领域 | 新`apps/server/internal/vo2maxexternal/{model,origin,select,freshness,canonical}.go`及测试 | 无DB/network/filesystem/time.Now/M1/M2/M3 import；typed snapshot→deterministic结果；只复制原值。 |
| 分析接线 | `internal/analysis/{model,input}.go`及新candidate VO₂adapter；`internal/jobs/worker.go` | 明确job identity/dispatch/unsupported错误；完整实现后才enroll；technical failure保持FAILED/retry。 |
| 输入/发布/读取 | 新`internal/storage/postgres/vo2max_input.go`、`vo2max_publish.go`、`vo2max_query.go`；现有`analysis_input.go/analysis_publish.go`最小分支 | repeatable-read、source/tie/SyncIdentifier有界索引查询、publication前重验、independent generation/assignment fencing、读取时Freshen。 |
| 查询生成 | 新`apps/server/queries/vo2max.sql`、sqlc配置/生成代码 | current验证与历史读取；不复用M3 Feature lookup或扫描raw HR/GPS。 |
| 管理入口 | 新`internal/admin/vo2max.go`、对应CLI接线 | explicit synthetic dataset、operation/actor、append-only epoch选择/compute/recompute/inspect/history/promotion-plan；管理员入口不得被httpapi导入。 |
| DB/权限 | 提议`migrations/00025_vo2max_external_projection.sql`、latest/checksums、`deploy/postgres/roles.sql` | narrow constraints/dependencies/artifact allowlist，Worker最小coverage/身份SELECT，active不变。 |
| 故障/角色/性能 | 新integration VO₂projection/roles/race测试；扩展`scripts/check-boundaries.py` | 核实pure领域与M1/M2/M3不互相import；真实隔离PG与API/Worker受限角色，不只mock。 |
| 报告 | 新B3结果/合成证据与届时IMPLEMENTATION记录 | 包括真实失败、未验证和性能，不展示健康值或伪造生理验证。 |

拟管理命令语义（尚不存在，不可在B1运行）：`rai analysis vo2max-external select-source --dataset UUID --input-file <受保护typed JSON> --operation UUID --actor NAME`；`compute|recompute --dataset UUID --operation UUID --actor NAME`；`inspect --dataset UUID`；`history --dataset UUID --run UUID`；`promotion-plan --dataset UUID`。输入文件≤4KiB，不回显source/device.name；inspect只用于显式合成开发dataset。production操作另需授权。

source selection input是版本化控制证据，不是parameterVersion；没有input时不自动选最大值/最近来源。管理操作重试使用同operation原回执，不能创建多份选择或多代作业。selection/revocation/config修改与失效在同事务；对没有candidate槽的synthetic dataset仅初始化通用槽位元数据，不触发M1 compute。external只校验自己的extension entry，不以M1 policy变化触发数值重算。

### 4.2 B3退出门槛

- 九种状态均有可执行decision table和null规则；根value只能在available/stale非空；calibrated true/false/missing均不能产生生理confidence。testType1不能生成laboratory结果。
- 同source epoch最新时间冲突、source-scoped SyncIdentifier多个live内容、选择缺失/切换、来源反证、invalid/future样本、删除最新→立即失效→重算或deleted全部通过。
- publish前重验fact/hash/coverage/selection/assignment/collector，涵盖首次无pointer时的删除；旧generation SUPERSEDED，lost lease不能写artifact或clean新generation。
- raw、source selection和current reader无权限捷径；API仍不能读写candidate artifact/input/channel/pointer；Worker不能改raw/input/channel；active和已有M1/M2/M3结果/作业不变。
- 相同typed输入的Incremental==Rebuild：打乱facts和pages、重复重试、delete先到、迟到历史、来源切换后均一致；run/calculatedAt等执行标识除外。rebuild不是同步启动条件。
- 仅candidate family；promotion-plan必须`promotable=false`并说明缺产品/真机门禁；不接训练或iOS用户指标展示。

## 5. Migration提案与审查清单

以下为设计任务，不含可执行SQL。实施时先核实migration最高号，如被其他任务占用则顺延，不改既有migration/checksum。

| 阶段 / 对象 | 最小增量 | 必须验证的回归 |
| --- | --- | --- |
| B2 health_samples | 复用metadata，服务器构造`RAI:vo2maxFactV1`；含typedMetadata、provenance/2、schema与server-derived sourceEpochID；新增限定类型/schema/size check和VO₂partial indexes | 原Daily reserved RAI:dailySource与hash不变；无新重复raw表；旧VO₂缺schema不可投影，不能回填/覆盖旧UUID。 |
| B2 tombstones | 新nullable provenance_snapshot，known删除前保存provenance/epoch/hash/原区间与typed originInputs（testType/manual/calibration），不存VO₂值；unknown保持null | delete-before-create、重复delete、同批delete-wins；manual删除不能冒充可投影估计删除；所有入口均能invalidate。 |
| B2 coverage | 在daily_health_coverage additive VO₂专属字段；seq/epochs通过共享stream/dataset取得 | 旧v1插入无需新字段，三流coverage响应不变；新endpoint只返回VO₂；空页/旧collector边界正确。 |
| B2 dirty hook | independent事实aggregate dirty，覆盖空页/未知delete；不注册model handler/job | receipt/raw/coverage/dirty/失效原子，旧receipt retry无任何副作用。 |
| B3 model inputs | 追加严格kind/schema配对vo2max_source_selection，保留hr_reference约束 | append-only、supersedes/revoke、重叠选择冲突、operation幂等；无HR-reference读取。 |
| B3 admin operations | 两个严格subject_dataset类型vo2max_external_candidate/vo2max_source_selection，沿用operation身份与actor审计 | 保留原CHECK分支；同operation重试不重复选择/enroll/增代，不记录数值。 |
| B3 dependencies | 仅新增两类external upstream→固定external downstream的精确CHECK分支 | 不能放宽为任意scope；原Feature→load/performance等边与未来vo2 placeholder不变。 |
| B3 artifacts/results | 新artifact单分支与RUNNING algorithm/version/generation校验；复用results/publications，sentinel day 0001-01-01 | terminal run不可追加；hash/schema/object/128KiB检查；不能任意artifact名；单run原子发布。 |
| B3 channels | 仅external extension key采用typed `{configHash,assignmentID}`；原key格式不变 | active=legacy；external更新不使M1/M2/M3重新计算或变更冻结policy；本entry取消即fence/invalidate。 |
| B3 permissions | targeted SECURITY DEFINER失效函数，固定search_path、REVOKE PUBLIC、精确scope；Worker追加必要SELECT | API无candidate内部读写；Worker无raw/input/channel写；restricted-role执行实际事务，不用owner绕过。 |

删除跨入口要求：新endpoint、现有generic delete、Workout关联cascade/其他合法撤回若碰到同VO₂ UUID，都必须在原事务捕获context并使external pointer失效。可在共享fact/tombstone触发点实现，不能只靠新HTTP handler补偿。reserved容器只能内部构造，已有generic wire须明确拒绝该新增reserved key；shape CHECK不能代替完整应用验证。

新索引必须由真实隔离PG18的EXPLAIN/ANALYZE证据证明支持bounded newest/tie/source-scoped sync检查与tombstone lookup；不创建全库summary、源健康值副本或三套结果表。新constraints必须保留所有旧分支；不使用`CHECK true`/任意JSON兜底。B3不得因共享PublishAnalysis方便而授予API artifact SELECT或publication写权限。

## 6. 测试矩阵

所有fixture是synthetic。以下全部为**未来必须运行**，B1没有这些实现或通过证据；现有Go测试只验证baseline回归。

| 层 / 编号 | 场景 | 接受标准 |
| --- | --- | --- |
| SDK S01 | identifier、mL/(kg·min)兼容转换、quantity.count=1，source/device全部属性含name/null | 原identity/interval/provenance保留；series/未知单位阻塞。 |
| SDK S02 | testType1/2/3/4、缺失、0/5，iOS18 deployment编译与iOS26可用符号 | 1–4精确raw值；4不无保护引用新符号；未知不ACK、不映射。 |
| metadata S03 | String/Bool/Int64/Number/Date/Quantity union，Bool对numeric0/1，2^53+1、Int64 min/max/越界 | 无Double整数精度损失；tag/key/unit错即整页拒绝；unknown number key也阻塞。 |
| metadata S04 | SyncIdentifier/Version同在/同缺/只一个；unknown key、nested object、Data、duplicate JSON key、reserved伪造 | 无字段静默丢弃；原页/anchor保持；不能绕过既有入口。 |
| metadata S05 | 事件HKVO2MaxValue、真正VO₂样本带该metadata，source AlgorithmVersion/Date起点 | 事件不产生fact；sample只有根quantity可投影；typed context完整，不替代事件时间。 |
| provenance S06 | Apple有效组合/缺device/未知bundle/矛盾manufacturer或model、非Apple bundle+Watch设备、manual true/false/缺失 | 四分类符合合同；type名/calibrated/maxExercise不推断Watch或laboratory。 |
| compatibility C01 | 旧LocalState/v1 pending/ACK/三anchor加载、新optional VO₂字段、旧服务器缺capability；VO₂与既有change通知交错 | 原三流bytes/fingerprint/sequence不变，VO₂待处理不fallback；原epoch/commit_rev/ordinal游标连续，不触发重置。 |
| discovery C02 | 固定90日、跨下界interval、翻页/空页、重启/时区变化、anchor损坏 | 窗口与独立fingerprint稳定；不重置或重读HRV/RHR/Sleep；corrupt fail-closed。 |
| outbox C03 | pending写后kill、上传前kill、server commit后ACK丢失、ACK验证后状态保存失败、磁盘满/文件损坏/GC | 原bytes/candidate/identity可恢复；old或完整new状态；原receipt重试；GC不回收pending。物理断电另列未验证。 |
| scheduling C04 | shared429/Retry-After、VO₂未知metadata、observer重复/前后台、offline重启 | 其他三流不饥饿，observer持久hint，VO₂不无限积页、不承诺固定后台时限。 |
| PG T01 | 相同UUID同内容/异内容、同batch异bytes、seq缺口/换batch、非法epoch/collector/upload lease | 原子unchanged或明确冲突；无半facts/receipt/coverage/stream。 |
| PG T02 | 同批delete+upsert乱序、delete-before-create、先delete后旧batch retry/新batch upsert、重复delete | 屏障永久优先；原receipt不改写；新upsert suppressed；无UUID复活。 |
| PG T03 | 每个raw写入/日志/dirty/coverage/receipt阶段注入异常或终止backend | 全部rollback或完整commit；新连接核实，不靠mock回调。 |
| PG T04 | 无receipt初始null coverage、空bootstrap、分页未完、追平空增量、最后事实删除、unknown delete、失效collector、新会话 | 独立coverage和sequence/epoch/UTC整数时间正确；授权始终not_determinable；不凭空表删除历史。 |
| PG T05 | v23升级到新schema、旧v1插入/ACK回归、两受限角色、公开函数滥用 | 旧合同语义/权限不变；无迁移真实DB；SECURITY DEFINER无任意scope或search_path逃逸。 |
| model P01 | 九状态、null/value表、confidence/parameter/feature空值、完整modelVersion/input schema/view/known cutoff，第三方/手工/raw legacy-only | 只有available/stale根值非空且DB quality=partial；不产生自有VO₂或laboratory结果；无M1/M2/M3依赖。 |
| model P02 | 同epoch最新不同值、等值多UUID、互斥testType、同source-scoped SyncIdentifier多live内容 | 显式冲突/等值证据组；不UUID破平、不跨源平均或最大版本掩盖。 |
| model P03 | 选择缺失/重叠/撤销/有效时间、来源app/OS升级、新epoch、删除最新/删除全部/unknown或manual delete、被选epoch删尽但另有第三方 | 显式切换记录；当前pointer即时失效；fallback有withdrawal证据；deleted不误判来源或被无关来源遮蔽。 |
| model P04 | age=30d−1µs/30d/90d−1µs/90d、未来时间、calibration缺失/false/true | 正确available/stale/expired；到界不等新worker；raw不因年龄消失；无伪置信度。 |
| model P05 | facts/pages乱序、重试、迟到、delete-first、改source selection，Incremental vs Rebuild | 相同effective input/asOf/fence产生相同语义及canonical hash；不依赖前一次pointer。 |
| worker W01 | snapshot后delete、publish锁前delete、publish先完成后delete、首次无pointer删除 | 两种锁顺序都不留下被删current；captured refs与generation/coverage重验。 |
| worker W02 | old generation、lease过期/续租失败、进程kill/reclaim、candidate entry/selection/collector变化 | SUPERSEDED或FAILED/retry明确；旧worker不写artifact/pointer、不clean N+1。 |
| worker W03 | 发布artifact/result/pointer/clean/run/job/attempt各阶段故障、unsupported job kind | 原子rollback或完整终态；不得产生假成功handler；current保留dirty技术状态。 |
| boundary W04 | 修改HRmax/RHR/M1/M2/M3输入，新external发布/选择反向影响其他模型检查；遍历既有HTTP首页/详情/训练消费者 | 无跨模型job/generation/publication污染；现有active消费者不变，通用publication查询不能泄露candidate family到产品。 |
| performance Q01 | 0/1/128事实页、cold90日、hot空增量、1024refs/32epochs/256tie及各limit+1；删除最新后的90日域内/域外前驱 | 记录requests/bytes/latency/query plan/扫描行数；超限技术失败不截断；域外不补current，无全库/全量recompute同步依赖。 |

性能先报告实际本机合成证据与查询计划，不将单机数值写成生产SLO。历史修订必须覆盖delete/add跨页和相同UUID异内容两条路径；测试能证明合同实现，不能证明Apple实际只采用其中一种。

## 7. B4 — Simulator端到端与风险收敛

本轮条件仅允许Simulator和synthetic；B4不写真实HealthKit、不读取生产库、不部署。完整走正式App支持路径、HTTP和隔离PG，不能用工具页/脚本直接插表替代App→Outbox→ACK。

B4必须记录Simulator OS/SDK、App/后端源码HEAD、实际测试命令、PG版本/临时隔离与清理、角色、各阶段证据、失败及修正。执行simulated background/observer只证明synthetic调度，不声称系统真实后台配送；注明文件`.atomic`进程恢复与物理断电保证的差别。正式device.name/source分布不从synthetic推论。

未来另行真机验收清单（本计划不执行）：真实Apple Watch vo2Max来源/metadata/testType/calibration分布；真实只读授权请求及可见性边界；真实anchored delete/history update的UUID序列与predicate范围；长期离线/删除保留；真实observer/后台/进程重启；功耗/磁盘增长/性能。若需要观察Apple的“Update Your Data”，须单独用户授权，不能为测试主动编辑健康事实。没有自然事件证据就标DEFERRED，不等待随机事件后冒称覆盖。

即使B4工程通过，也保留`REAL DEVICE ACCEPTANCE: DEFERRED`和`PHYSIOLOGICAL VALIDATION: NOT VERIFIED`，直到有对应新证据；外部估计来源可靠性与生理准确性是不同验收，不计算“RAI算法准确度”。

## 8. 风险与未解决事项

| 优先级 | 未解决项 | 处理/放行边界 |
| --- | --- | --- |
| P1实现门禁 | B2/B3协议、DB、权限、Worker均未实现/未测 | 本文只冻结；不能以文档存在或现有Go通过宣称功能ready。 |
| P1真机门禁 | Apple历史update事件、真实metadata、源bundle/设备命名分布未知 | conservative allowlist/unclassified/冲突fail-closed；生产或产品激活前另验。 |
| P2兼容 | 未知metadata、未来testType、新source/app/OS epoch可能阻塞或要求重新选择 | 不丢字段，不自动reset/切源；版本化registry/配置与明确操作。 |
| P2身份 | descriptors不是硬件认证；相同型号无法保证物理设备可分 | 保留raw证据与unclassified边界；不造稳定设备ID。 |
| P2完整性 | 固定bootstrap范围、长离线删除保留、权限窗口变化与本地状态丢失 | coverage只声明观察范围；不承诺全历史，也不依赖备份/恢复/全量重读同步。 |
| P2策略 | 30/90日freshness、source规则、explicit selection尚无真实人群校准 | 写入configHash，candidate-only；不称生理有效期或准确概率。 |
| P2工程 | 新constraints/indexes/roles、所有删除入口失效、shared candidate初始化与其他extension共存 | B2/B3真实隔离PG、受限角色、race/边界门禁必测，不能靠mock。 |
| P3设备 | 物理断电、后台系统时机、WAN/生产SLO、旧二进制跨新schema回滚未验证 | 明示NOT VERIFIED；不部署/回滚演练真实环境，不给自动放行。 |

这些风险不阻止文档冻结，因此B1为PASS WITH RISKS；它们不是自动授权进入B2或产品上线。

已关闭的初稿记录：M4-A报告原文、HEAD及Header证据均已定位并核对，**不再列为未解决风险**。余下风险继续对应原报告的真机、历史Update、来源保真和生理验证边界；不因此将B1改为无风险PASS。

## 9. B1实际验证与原文件保全

实际运行结果：

| 检查 | 本轮证据 |
| --- | --- |
| `go test -race ./...`，cwd=apps/server，使用工程本地Go1.27.3环境 | PASS；既有部分包缓存命中。验证现有代码回归，不是VO₂实现测试。 |
| `go vet ./...` | PASS。 |
| `python3 scripts/check-boundaries.py`，同工程Go环境 | PASS，253 packages；online不达admin/migrations/goose；pure领域边界与M3仅消费M1通过。 |
| SDK Header/常量审查 | PASS；testType1–4、availability、Device.name、metadata实际key核实；常量probe无健康查询。 |
| M4-A补充核对 | PASS；附件/原报告SHA一致；原报告HEAD相同；849原有文件与其离场manifest相同；五个SDK Header跨Simulator/iPhoneOS内容一致；建议差异已显式记录。 |
| 文档检查 | PASS；两份local links、Markdown围栏/表格、隐藏字符、tracked及untracked文档whitespace均通过；十四项验收见§10。 |
| 新VO₂ Swift/Go/HTTP/Worker测试 | NOT RUN / NOT IMPLEMENTED。 |
| 真实隔离PG集成 | NOT RUN（B1仅文档，无事务修改）；B2/B3是强制门禁。 |
| Simulator端到端 / 真机 / 生理 / 部署 | 本轮均NOT RUN；不得借M5A/M1/M2/M3历史证据代替。 |

原文件SHA-256基线如下；结束时再次计算必须完全一致：

| 原文件 | SHA-256 |
| --- | --- |
| WORKOUT_FEATURE_CONTRACT.md | `27f0b9c3189afab61379e93eebe6df6098c4eda7df6b2f979c1dcc49c226b693` |
| TRAINING_LOAD_V1_CONTRACT.md | `c9ce6865bbd0ae7e0316051d606155f3a5ede48254dee38eae038a7ce4bfe08d` |
| RUNNING_ABILITY_V1_CONTRACT.md | `1919640cc033868fdafc8f5fc0ed30aefa21d821d4d7d735ec17de20470294f4` |
| DAILY_HEALTH_SYNC_CONTRACT.md | `ed226db9cb4105c5529129a6f3b49ecd5b61ab19a07098b959cf784ca5e97660` |
| IMPLEMENTATION.md | `d27568dc29c02d155d6a04e10f26bcda702d8e321ac7491b83c10ca9a24dd460` |

上述路径均位于docs；整体原文件manifest SHA-256=`460cef3e940cb1be3b42b5e3baa8d11f7bb174bf214a6c1c2a4ccf02eec2c6bb`。manifest位于仓库外`/tmp/rai-m4b1-baseline.json`，不作为第三个交付文件；交付最终报告另列两份新增文档的完整SHA-256，避免文件自包含hash的递归问题。

## 10. 最终十四项验收

下列“是”均指**设计合同已经冻结**，不表示B2/B3已实现：

| # | 问题 | B1结论 / 合同依据 |
| --- | --- | --- |
| 1 | Daily v1完全兼容？ | 是，独立endpoint/DTO/能力；三流语义/状态/anchor不变，§3。 |
| 2 | 独立anchor？ | 是，VO₂唯一secure archive/query fingerprint/固定bootstrap，§3.1。 |
| 3 | testType1–4完整？ | 是，Header核实原值、iOS26 guard与未知fail-closed，§4.3。 |
| 4 | metadata完整且类型安全？ | 已审allowlist完整保存、六类型合同、Int64精确；未知全部阻塞，不能宣称全现实metadata已验，§4.2。 |
| 5 | 识别Apple Watch/第三方/手动？ | 已冻结保守四分类；有条件识别，缺证据为unclassified，真实分布DEFERRED，§5。 |
| 6 | 避免实验室/外部估计混淆？ | 是，testType1仅声明，不建laboratory；原事实/外部投影/自有模型分层，§2/5。 |
| 7 | 处理删除与历史修订？ | 是，真实delete/new UUID正常、同UUID异内容冲突、更新序列DEFERRED，§6。 |
| 8 | 避免旧样本复活？ | 是，tombstone优先、旧receipt只读、新upsert suppressed，§6/7。 |
| 9 | 独立Coverage？ | 是，VO₂stream与endpoint隔离，不推断读取授权或不存在，§7。 |
| 10 | 独立External Projection？ | 是，独立model/scope/config/lineage/family；不计算新VO₂值，§8–11。 |
| 11 | 自有VO₂保持未实现？ | 是，estimated/v1无handler/job/result，§2。 |
| 12 | 避免M1/M2/M3依赖污染？ | 是，无Feature/load/performance/HR-reference输入或反向边，§8/10。 |
| 13 | Candidate-only？ | 是，active=legacy，无产品promotion/训练消费，§10。 |
| 14 | B2/B3明确可执行？ | 是，文件范围、两个最小migration提案、执行顺序、真实PG/角色/故障矩阵和门禁见本计划§3–6。 |

完成B1后停止。没有开始B2，没有创建migration，没有修改既有文件，没有reset/restore/clean/stash/commit/push/rebase/deploy，没有访问生产库或修改`../running`；下一阶段等待新授权。
