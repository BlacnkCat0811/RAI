# RAI V2 Full Project Review

日期：2026-10-29，Asia/Shanghai。审查基线：0308b8658adc304040e445bc7f6ab61d212472d3。

## 1. Executive Summary

当前模块化单体、PostgreSQL 唯一服务端事实库和原生 iPhone 客户端的总体架构健康。
没有发现需要重新设计整个架构的证据。
本轮确认 **P0=0、P1=2、P2=4、P3=1**，共七项 Finding。
两个 P1 分别位于合法 Route 输入与 P4 的衔接，以及 P5 已接受 Override 与新 Base 的衔接。
P3 可以封口等时间戳路线，但 P4 将整场 Workout 分析永久判错并隐藏旧 publication。
合法的按时长 Interval 覆盖，在日程改变后可能使整份新计划发布失败。
四个 P2 涉及 daily_load 缺失传播、Training JSON 校验、训练查询读取范围和客户端响应上限。
没有发现普通同步中幂等失效、旧上传复活 UUID、跨 dataset 读取或 stale Worker 成功发布的反例。
这不构成对所有并发交错和真实 HealthKit 输入的穷尽证明。
P4 正式算法仍由 Go 唯一实现；Swift 没有第二套评分或计划生成器。
P5 用户事实、旧 Effective 和 frozen Execution 在已执行反例中仍被保留。
Go 普通/race、完整真实 PG/race、边界、vet/build 和隔离 Compose 回归全部通过。
原有门禁通过与本轮反例同时成立：现有测试遗漏了合法输入之间的组合边界。
本轮未修改业务源码、测试、migration、OpenAPI、部署文件或历史证据。
真机、正式数据库、真实云安装和 Recovery Resync 的正式状态均未升级。
建议先修复两个 P1，再处理四个 P2，并补充针对这些反例的回归后开展设备验收。
**最终结论：FIXES REQUIRED BEFORE DEVICE ACCEPTANCE。**

## 2. Review Baseline

| 项目 | 本轮事实 |
|---|---|
| Repository | /Users/ta/rai |
| Branch | main |
| FULL_REVIEW_BASE_COMMIT | 0308b8658adc304040e445bc7f6ab61d212472d3 |
| HEAD subject | Add monthly backup and disaster recovery review |
| Remote | origin，https://gitee.com/tpandmr/rai.git |
| Remote state | git fetch origin 成功；origin/main、origin/HEAD 均指向该 HEAD；ahead=0、behind=0 |
| Working tree at entry | git status --short / --porcelain=v1、git diff --stat、git diff HEAD 均为空 |
| macOS toolchain | Xcode 26.5 / 17F42；Apple Swift 6.3.2；x86_64；仅安装 iOS 26.5 runtime |
| 本机限制 | PATH 无 Go、Docker、PostgreSQL；Go/DB/Compose 使用现有 fnOS 隔离入口 |
| fnOS toolchain | Go 1.27.3 linux/amd64；Docker 28.5.2；Compose v5.1.0 |
| Test PostgreSQL | 18.6，独立新容器；fsync/full_page_writes/synchronous_commit/data_checksums 均 on |
| Isolation | test-remote 独立源码快照、随机测试库；Compose 唯一 project、新凭证、新卷 |
| Production | 未建立、未连接正式 V2；未查询旧 rai-postgres，不读取 ../running |

已执行用户要求的 pwd、status、branch、HEAD、最近二十项 log（仓库只有九次提交）、remote、diff、porcelain 和 fetch/ref 比较。没有 pull/reset/clean/rebase/merge/checkout/commit/push。

本轮对入口全部 **566 个 tracked 文件**建立 SHA256 清单，交付前逐一核验未变。只有本报告及配套 JSON 新增。按本轮“只允许两份报告文件”的更具体要求，未更新 docs/IMPLEMENTATION.md；真实证据和未验证项记录于此及配套 JSON。

## 3. Scope

已完整读取用户指定的 AGENTS、README、Architecture、Implementation、P0 基线、P3/P4/P5/P6 合同和结果、P7 结果/部署/运维/备份/安全/容量/发布清单，以及月度备份、灾难恢复和最小恢复审查。**指定文件全部存在，NOT PRESENT=0**。配套 JSON 保存逐文件哈希和行数。

| 领域 | 实际审查内容 |
|---|---|
| A Repository / Build / Dependency | Git 基线、566 文件清单、Go module/deps/graph、Swift package/targets、CI、构建与生成物 |
| B Architecture | 实际 import graph、CLI/HTTP/storage/领域边界、Swift 业务计算搜索 |
| C PostgreSQL | 00001–00013 全部 SQL、FK/CHECK/唯一性/不可变触发器、角色、fresh/upgrade/rebuild、定向 EXPLAIN |
| D P3 Sync | stream/batch/page/receipt、事实事务、删除、series/package/route staging 与 seal、重放和故障测试 |
| E P3.5 Client | anchored query、candidate/delivered、原子 outbox、上传文件、后台回调、重启和 GC |
| F P4 | features/TRIMP/state、缺失值、快照/hash/lineage、bounded loader、publication 和反例 |
| G P5 | generator、StableID、override/revoke/effective、命令回执、execution、feedback、并发与反例 |
| H P6 | 五 Tab、数据来源、命令 journal、严格并发、availability、静态渲染风险 |
| I API / OpenAPI / DTO | 运行路由、schema、Go/Swift DTO、错误与 body/response 限额、Redocly |
| J Jobs | claim、renew、requeue、generation、fencing、事务末尾墙钟检查、单 slot Worker |
| K Operations | 两类备份入口、SSH/权限/SHA/原子发布/三份保留、隔离恢复和故障演练 |
| L Security | Bearer/dataset、DB runtime 身份、SECURITY DEFINER、参数化 SQL、日志、Keychain/TLS/ATS |
| M Performance | 历史 P4–P7 基线和真实口径；只新增可疑训练查询/响应体的定向测量 |
| N Tests / Evidence | 断言内容、mock/E2E 区分、golden、故障点、历史失败、source hash 和当前重跑 |
| O Maintainability | 全仓关键词、引用关系、兼容分支、备用脚本、重复证据和安全删除候选 |

审查不等于逐个 SwiftUI 视觉状态或所有 SQL/并发输入的穷举证明。没有重跑所有极端 scale/p7capacity benchmark；这些 tag 的 vet 已重跑，历史原始口径已审查，新增问题采用独立定向验证。没有创建新客户端、服务或恢复协议。

## 4. Architecture

~~~mermaid
flowchart TD
  HK[HealthKit 只读] --> OB[Swift actors / anchored query / immutable outbox]
  OB --> API[Go HTTP API / Bearer / admission / validation]
  API --> TX[PostgreSQL 事实事务 / dataset 行锁]
  TX --> RAW[Raw facts / tombstones / change events]
  TX --> ACK[durable receipts / stream / page]
  ACK --> OB
  TX --> DIRTY[dirty scopes / jobs]
  DIRTY --> W[同镜像 Worker / 单 slot / renew / fencing]
  RAW --> SNAP[有界 repeatable-read 快照]
  SNAP --> A[纯 Go Analysis]
  W --> SNAP
  A --> PUB[不可变 runs/results / 原子 publication]
  PUB --> T[纯 Go Training]
  T --> E[Base + persistent Override → Effective]
  E --> EX[Frozen Execution / versioned Feedback]
  EX --> DIRTY
  PUB --> READ[只读产品 API]
  E --> READ
  READ --> IOS[原生 SwiftUI 产品]
~~~

管理 CLI/admin、迁移、备份、全表核验与线上同步的调用图分离。没有第二数据库、Redis、消息服务、Web/Watch/桌面产品。Goose 和 pgx 是两个必要的直接依赖；在线图不含 goose/admin/migrations。SQL 是 sqlc 与手写 pgx 混合，不能称全部生成。

analysis/training 核心没有数据库、网络、文件、环境或 time.Now 调用；日期/as-of/参数显式传入。Swift 的单位格式化、柱图归一化和计时属于呈现逻辑。没有发现隐藏的正式算法旁路。

## 5. Findings Summary

| ID | Severity | Domain | Title | Status |
|---|---|---|---|---|
| RAI-FULL-001 | P1 | P3 → P4 | 合法等时间戳路线导致整场分析永久失败 | CONFIRMED / PG18 reproduced |
| RAI-FULL-002 | P1 | P5 | 合法 Override 在日程变化后阻塞整份新计划发布 | CONFIRMED / PG18 reproduced |
| RAI-FULL-003 | P2 | P4 | daily_load 错误继承整个 180 日窗口的缺失状态 | CONFIRMED / pure function reproduced |
| RAI-FULL-004 | P2 | API / P5 | Training body 校验接受与 schema 不符的 JSON | CONFIRMED / HTTP + PG18 reproduced |
| RAI-FULL-005 | P2 | P5 / Capacity | 28 日查询的时间过滤不可利用现有时间索引，实际扫描历史 | CONFIRMED / actual SQL EXPLAIN |
| RAI-FULL-006 | P2 | P5 / P6 API | 合法训练审计/历史响应超过 Swift 1 MiB 上限 | CONFIRMED / actual DTO measured |
| RAI-FULL-007 | P3 | Documentation | 当前 iOS README 仍称所有 delivered payload/receipt 保留 | CONFIRMED / source contradiction |

所有 Finding 均未修复。Severity 描述已证明的影响；未把无设备、无正式数据库或灾难恢复延期计为代码缺陷。

## 6. P0 Findings

**未发现经本轮证据确认的 P0。** 没有重放复活、半事务 durable ACK、跨 dataset 泄漏、用户事实被重生成覆盖或 stale Worker 成功发布的反例。

## 7. P1 Findings

### RAI-FULL-001

- **Severity / Domain / Title：** P1；P3 → P4；合法等时间戳路线导致整场分析永久失败。
- **Evidence：** ios/RAI/Sources/SyncCore/RouteBuilder.swift:85 允许 recordedAtUs ≥ 上一点；apps/server/internal/syncing/route.go:223–267 不要求严格递增；migrations/00011_p3_sync_v2.sql:190–229、289–317 检查范围/sequence/count，却允许相邻点同时间；internal/analysis/workout.go:255–256 对同时间直接返回 Invalid。
- **Observed Behavior：** 三点路线的 sequence 连续、首末范围正确，其中前两点同一时间。P3 register/chunk/seal 全部成功；原本可查询的 Workout 在 seal 后重新分析，job 进入 failed / analysis_invalid_input，旧 publication 被 current 查询隐藏。
- **Expected Contract：** P3 保存来源顺序，不排序/合并/去重；P4_ALGORITHM_CONTRACT.md:30 仅连接时间严格增加且精度合格的点。不能把 P3 接受的重复时间点变成整场 duration/distance/TRIMP 的永久失败。
- **Impact：** 合法同步成功后，整场分析不可读，依赖 state 等待其 clean publication。raw、receipt 与删除屏障仍完整；未证明真实 HealthKit 出现该输入的频率。
- **Root Cause：** P3 与 Swift 使用非严格时间顺序；P4 把不能构成有效连接的相邻点当作整个 Workout 的非法输入，且 FailAnalysis 对 Invalid 不重试。
- **Reproduction：** 配套 JSON 中 TestFullReviewEqualRouteTimestamp；通过正式 Store 上传/封口、实际 jobs.Process 和真实 PG18 重现，输出只记状态和计数。
- **Recommended Fix：** 冻结合规点/连接的质量语义，保留 raw；使等时间连接跳过或明确降级路线质量，仍计算其他有效指标。覆盖跨 chunk 的重复时间、单路线/多路线和缺精度场景。若选择拒绝输入，必须正式协调 P3/Swift 合同，不能只在 P4 末端失败。
- **Regression Risk：** 路线距离/coverage、缺失与零的区分、旧结果重算和算法版本兼容。
- **Suggested Fix Order / Phase：** Wave 1 第一项；P3/P4 正确性维护，设备验收前。

### RAI-FULL-002

- **Severity / Domain / Title：** P1；P5；合法 Override 在日程变化后阻塞整份新计划发布。
- **Evidence：** internal/training/model.go:299–306 允许有时长的 session 距离为 0；generator.go:183–187 生成按时长 Interval，而普通 EASY/RECOVERY 的 duration 为 0；effective.go:90–112 只对缺 slot/REST 使用 anchor，其余新 Base 直接逐字段 ValidateSession；storage/postgres/training_publish.go:93–97 在 materialize 失败后回滚整次发布。
- **Observed Behavior：** 初始 availableDays=[1,3,5,7]、longRunDay=7。第二周周三为 2880 秒 Interval；用户 distanceM=0 覆盖被成功接受。增加周二后，配置 revision=2 被接受，同一稳定 slot 变为 RECOVERY、duration=0。合并原 distance=0 后 invalid_training_input，job 永久 failed；current Base 不更新，旧 Effective 及 overrideRevision=1 保留。
- **Expected Contract：** P5 字段覆盖持久保留；新 Base 无法应用覆盖时应明确保留/提示冲突，日程更新不能接受后才让整份生成永久卡住。
- **Impact：** 用户提交的新日程与继续显示的旧课程不一致；重复 recalculate 仍命中相同组合。不会静默把用户距离改回自动值，frozen Execution 也未受损。
- **Root Cause：** 合并只处理 removed/REST anchor，缺少“仍存在但处方结构变化、覆盖组合不再有效”的冲突分支。
- **Reproduction：** TestFullReviewOverrideScheduleTransition，经真实 Create/Override/ChangeConfig/Worker；两轮复现，并断言旧 Base、覆盖 revision 与距离仍保留。
- **Recommended Fix：** 在同一持久模型内定义这种 rebase 冲突的可用投影或提前拒绝配置并说明受影响课程；保留 override 事实和可解释冲突。避免通过忽略错误、丢覆盖或直接改旧 Effective 解决。
- **Regression Risk：** duration/distance/type/steps 联动、撤销、多字段优先级、新旧版本重放及已开始执行。
- **Suggested Fix Order / Phase：** Wave 1 第二项；P5 正确性维护，设备验收前。

## 8. P2 Findings

### RAI-FULL-003

- **Severity / Domain / Title：** P2；P4；daily_load 错误继承整个窗口的缺失状态。
- **Evidence：** internal/analysis/state.go:54–79 的 valid 由 180 日窗口 unknown 决定，dailyValue 也调用同一 value；合同 P4_ALGORITHM_CONTRACT.md:40 将“该日 load unknown”和“受影响高层值 null”分开。
- **Observed Behavior：** 前一天有 unavailable load、当天全部 load 已知时，当天 daily_load 仍为 null；fitness/fatigue 为 null 是正确的保守行为。
- **Expected Contract：** 当天 load 汇总只取决于当天记录；历史缺失影响高层卷积，不应抹去已知的单日汇总。
- **Impact：** 发布的日负荷缺失范围扩大，历史读取会把已知当天负荷显示为 insufficient；没有伪造 0 或修改 raw。
- **Root Cause：** 日汇总和高层状态共用了一个窗口完整性谓词。
- **Reproduction：** TestFullReviewDailyLoadIsolation，纯函数确定性输入验证；配套证据仅记录 null 判定。
- **Recommended Fix：** 分离 daily 与 window 的可用性，保留对高层缺失的传播；补“旧日 unknown + 当前日 known/无记录”的测试，处理算法版本和已存结果重算。
- **Regression Risk / Suggested Fix Order / Phase：** 缺失与 recorded-zero、日期窗口及 golden；Wave 2 / P4。

### RAI-FULL-004

- **Severity / Domain / Title：** P2；API/P5；Training body 校验接受与 schema 不符的 JSON。
- **Evidence：** internal/httpapi/training.go:26–38 仅用 encoding/json + DisallowUnknownFields；syncing/protocol.go:235 起已有重复键、精确字段拼写和 required/null 检查。OpenAPI:1520 要求 expectedOverrideRevision，:1513 notes 是 string。
- **Observed Behavior：** 首次 Override 省略 expectedOverrideRevision、把 commandID 改成 CommandID、重复 value 键，以及 add 的 notes:null，分别通过真实 HTTP + PG 返回 200。null notes 被解为默认空串，重复字段取后值。
- **Expected Contract：** required/字段拼写/类型一致；不能靠 Go 零值把缺失并发字段或 null 字符串变成合法命令。
- **Impact：** schema 无效请求进入 durable command/override 事实；客户端错误不易发现。未绕过非零 revision 的并发冲突校验，不归为 P0/P1 或认证漏洞。
- **Root Cause：** P5 新建了较弱的独立 JSON 解码路径，没有复用/扩展 P3 严格边界。
- **Reproduction：** TestFullReviewTrainingJSONDrift 四个独立子例，均 200；普通未知字段和非零 revision 约束仍工作。
- **Recommended Fix：** 共用严格 JSON 规则并为 Training DTO 明确 required/null/原始 value 语义，保留 targetHR/pace 的合法清空。现有 exactJSONFields 对 RawMessage 也需适配，不能机械替换调用。
- **Regression Risk / Suggested Fix Order / Phase：** 命令 canonical hash、重试、可选值清空、旧合法客户端；Wave 2 / API/P5。

### RAI-FULL-005

- **Severity / Domain / Title：** P2；P5/Capacity；28 日查询实际扫描历史。
- **Evidence：** storage/postgres/training_input.go:15–28 的 event_start AT TIME ZONE … ::date 谓词和合并的 ID/日期 OR；migrations/00003_workouts.sql:18 的现有 workouts_time 原始时间索引。
- **Observed Behavior：** 对 1825 个合成 Workout 使用实际 TrainingWorkoutsSQL、ANALYZE 和默认 planner：返回 28 行，但 workouts 顺序扫描并过滤 1797 行；dirty_scopes 读取 1825 行，nested-loop join filter 丢弃 51072 对。执行 14.845ms、422 shared hit blocks，是本次单次观测。
- **Expected Contract：** 正常训练上下文为最近 28 日；LIMIT 4097 应是输出保护，不能代替可定位的输入范围。此处是历史读取，不是全历史算法重算。
- **Impact：** 新历史不断增加时读取成本随历史增长；本次尚未触发 timeout，不能据 14.845ms 宣称当前已经有用户可见延迟。
- **Root Cause：** 对索引列执行时区/日期函数，且两个用途共用 OR 查询，无法直接使用现有原始时间范围。
- **Reproduction：** TestFullReviewTrainingQueryBound；配套 JSON 保存实际 SQL 的完整 EXPLAIN(ANALYZE,BUFFERS) 结构及来源哈希，未关闭 seqscan。
- **Recommended Fix：** 按冻结 IANA timezone 先计算绝对日期边界，以 event_start 范围查询；明确分离单 Workout 与最近窗口路径，再用默认 planner 比较。先使用现有索引，不因此引入缓存或新数据库。
- **Regression Risk / Suggested Fix Order / Phase：** DST、日期边界、显式迟到 execution 对旧 Workout 的读取和 clean-publication 条件；Wave 2 / P5。

### RAI-FULL-006

- **Severity / Domain / Title：** P2；P5/P6 API；合法审计/历史响应超过客户端上限。
- **Evidence：** storage/postgres/training_store.go:379–398 审计返回 Base/Effective/Context 与所有 overrides；:401–426 history 最多 50 份完整 Effective；ios/RAI/App/ProductModel.swift:114–123 使用审计接口；Sources/SyncCore/RAIAPIClient.swift:44 对所有响应固定限制 1048576 bytes。
- **Observed Behavior：** 使用真实命令写入 256 次各 2000 字符的合法 notes 覆盖后，P6 使用的 audit DTO 为 **1208155 bytes**。另一 16 周、50 版本的合法 history 为 **1696715 bytes**。客户端在 JSON 解码前必抛 oversized_response；这里实际测量了服务端 DTO，拒绝分支由源码直接确定，没有声称本轮又跑了完整 UI 点击链路。
- **Expected Contract：** 服务器接受的合法计划/审计规模应有明确可读取方式；页数有界不等于字节数有界。
- **Impact：** “调整历史”无法加载，允许的 history 页也可能不可读；该事实不会删除服务端训练记录。相同原则还应检查 execution feedback 和 user-added sessions 增长。
- **Root Cause：** 新产品复用了同步回执的小响应预算，但服务端没有对应的字节预算、审计分页或摘要投影。
- **Reproduction：** TestFullReviewAuditBytes 和 TestFullReviewHistoryBytes；只保存计数、字节数及合成探针源码，不保存响应正文。
- **Recommended Fix：** 定义跨端一致的响应预算和可恢复的审计/历史分页/摘要合同；必要时按 endpoint 使用经过验证的限额。不能简单移除限额或把失败响应当空历史。
- **Regression Risk / Suggested Fix Order / Phase：** 游标、immutable audit 重放、旧客户端和错误恢复；Wave 2 / API/P6，长期使用前。

## 9. P3 Findings

### RAI-FULL-007

- **Severity / Domain / Title：** P3；Documentation；iOS README 的已交付文件保留说明过时。
- **Evidence：** ios/RAI/README.md:40 称 delivered payload/receipt 当前全部保留；Sources/SyncCore/OutboxStore.swift:207–261 在完成页后标记/删除 receipt 并有界 GC；App/SyncCoordinator.swift:121 调用实际 sweep。
- **Observed / Expected：** 当前未必还有已交付原载荷，而活跃运行说明仍给出“保留”的印象；应说明新完成页可回收、未知 orphan/旧缓存仍保留。
- **Impact / Root Cause：** 运维或后续开发者可能高估本地 exact replay 的材料。P7 加入 GC 后，局部 README 段落未同步；灾难恢复专项文档已正确描述此边界。
- **Reproduction：** 对照当前完成页/GC 路径；本轮 Swift 核心、GC SIGKILL 和 RecoveryBoundary 用例通过。
- **Recommended Fix：** 下轮维护只更新活跃说明及相关引用，保持历史阶段报告原文。
- **Regression Risk / Suggested Fix Order / Phase：** 低；勿把保留 orphan 写成可任意清理；Wave 3 / 文档。

## 10. Sync Review

P3 的事实、tombstone、entity/change log、dirty/job、原 receipt、stream/page 在同一事务。dataset 首业务行锁使 commit_rev 与提交串行，不用 sequence 作下载水位。batch 登记与事实提交两个事务的边界明确；registered 不是 durable。

同身份 batch/page/chunk/seal 重试及冲突、response loss 后原 receipt、Create→Delete→旧 Create、未知删除、父 Workout 删除、迟到 sample/association/enrichment 均有真实 PG 断言，完整回归已重跑。route/series/package staging 不作为已发布输入。未知字段、单位和预算在 P3 严格边界拒绝。

HTTP wire/gzip/schema 解析在事务锁外，普通批 2 MiB wire / 8 MiB decoded / 1000 events；进程两并发上传、单设备一项，DB 持久 admission/slot 跨实例。封口路由按单 Route 有界核验点，不是全库核验；百万点上限不等于常数工作量。ACK 不等待分析，Worker 离线测试仍 durable。

**新增缺陷是 RAI-FULL-001 的 P3/P4 合同衔接，而非 P3 事实事务损坏。** 没有发现普通重试 resurrect UUID 的源码反例。pre-v4 page 防重名探针保留历史升级合同，不能仅凭 legacy 关键词删除。

P3.5 的所有 delivered anchor 写入收敛到 OutboxStore.finish；非空页只有严格验证全部 ACK/封页后才进入 finish，空查询走 finishEmptyPage。batch ACK 先推进对应 sequence，page 最终 durable 才推进 anchor。payload 先落盘、state 原子替换后更新内存；中途死亡可能留下 orphan。GC mark 与 anchor/page 移除同次 state 写入，再核验引用并 bounded sweep。Data.write(.atomic) 和合成 SIGKILL 不证明设备断电下的 fsync/存储耐久。

BackgroundTransfers 用稳定 page/cursor、受 Mutex 保护的回调状态和落盘响应恢复；取消前台等待不取消 OS 上传。固定目录/后台 identifier 不能安全换 dataset，属于已知恢复缺口。真实授权、背景生命周期、设备强杀和 HealthKit 覆盖仍未验收。

## 11. Analysis Review

纯 Go 是唯一正式实现：workout.features、TRIMP、180 日有限核 fitness/fatigue、load-only recovery/readiness 与固定 bundle/version。没有 Swift/Python 在线 scorer 或 SQL 数值算法副本。SQL 只构造输入、选择 publication 和管理依赖。

同一快照显式带范围、as-of、timezone、参数版本、排序与 lineage；input hash 不把不相关 dataset revision 当数值输入。无参数/HR 覆盖不足/歧义区间不制造负荷，缺失高层不填 0。当前两个缺陷为等时 Route 导致整场失败和单日负荷缺失传播扩大（001/003）。

快照 REPEATABLE READ，250000 observations/route points 超限明确失败，不截断；state 输出范围和输入回溯有上限。已有 1/3/5 年、golden/property、参数/删除/迟到/缺失和 publication 故障测试不是生理效度验收。HR zones、splits、完整生理恢复等未实现部分没有伪成功。

## 12. Training Review

模型确实为单一 Plan 的 Generated Base + Persistent Override → Effective → Execution → Feedback。StableID 基于 plan+slot，不由日期/类型重新制造 identity；add session 仍属于同一 Plan。

字段级距离覆盖、move、未编辑字段随 Base、revoke、新 Base 缺 slot anchor、两个客户端 revision、用户编辑与 generation 竞争、原 command/override receipt 重放都有实际断言。本轮完整 PG 和 Compose 再次通过。**不能据此称任意覆盖组合都安全，002 是明确反例。**

Base/Effective/override/feedback append-only，publication 在 dataset 锁内读取最新 override；执行冻结当时 Effective/session。历史版本与 started snapshot 在重算/归档后不会被 UPDATE。feedback 用已发布来源并保留 pending/missing，Workout 缺失不自动判 missed，也不自动时间匹配。

训练来源仅读 clean publication 的正式指标，raw 只用于 identity/activity/time 和存在性。问题主要是 rebase 冲突完备性、读取范围与响应预算（002/005/006）。单 plan 最多 16 周并不能约束覆盖、反馈和 user-added 历史数量。

## 13. iOS Product Review

正式 runtime 没有默认 Preview/Mock 健康值、假路线或本地训练生成。Home/Analysis 使用 P4 published API，Training 使用 P5 Effective；Base 仅在审计数据中出现，不直接当当前训练处方。未知状态显示缺失，周统计明确是已同步跑步。

ProductAPI 和 OutboxStore 为 actor，UI model 为 MainActor；production App/Sources 没有 unchecked Sendable、Task.detached 或 DispatchQueue.main.sync。两个 unchecked Sendable 命中仅在 URLProtocol 测试和 ProductProbe 计时 harness。Mutex/Sendable 也不能替代真实后台验证。

iOS 26 glassEffect 有 availability 条件，18–25 Material 分支明确；matched transition 最低 iOS18。图片是缓存的 1280×853 JPEG，无持续 blur/shader/粒子系统；图表和列表读取有界。DateOnly/执行时间 formatter 多次创建是小型静态维护风险，尚无证据将其列为瓶颈。长执行历史列表和并行 loadMore/refresh 的交互规模仍需定向实测。

本轮 Swift 41 tests、Debug Simulator、Release generic arm64/min18 strict concurrency、独立模拟器 8 Normalizer tests 通过。Debug 验收诊断 Text 有本地化插值 deprecated warning，位于 DEBUG+Simulator 条件；无业务编译错误。完整 UI 截图/无障碍回归本轮未重跑，历史 P6 证据保留。所有帧率、GPU/Metal、iOS18 实际运行、12 Pro Max 真机结论仍为 STATIC / SIMULATOR RISK 或 NOT VERIFIED。

## 14. Database Review

实际为 00001–00013，连续无重复编号；13 项 SHA256 全部等于 checksums.json。逐文件 git log 表明旧 migration 未在加入后被回写。44 张应用表加 goose 版本表，共 45 表。

已逐表检查 PK、复合 dataset FK、唯一身份、值范围、状态 CHECK、级联和不可变 trigger。staging 允许尚无 Workout 的 package；execution.workout_id 不做删除级联 FK 是保留已删来源审计的刻意设计，不应擅改成 cascade。运行组不具备 schema CREATE/TEMP/owner 身份；路线 projection 由受控函数/触发器生成。

fresh→13、重建 schema 定义比较、重复 migrate、历史 3/4/5/6/7/8/10/11/12 升级入口和不改原 receipt 的用例均在完整真实 PG 回归中运行。readiness 对非 13 fail closed。down 白名单为空，现有事实迁移不能安全回滚；这是明确门禁。

identity、publication、Effective/history、Workout keyset、route/workout 关系索引有实际使用路径。旧 TestCoreIndexAvailability 用 enable_seqscan=off 且 job SQL 是较早简版，只证明“索引可用”，不能充当当前 ClaimJob 性能证明。P4/P7 大表证据使用实际 SQL/default planner；本轮新测的 TrainingWorkoutsSQL 存在 005。未发现需要整体换 schema 的证据，也没有对全部 FK 做生产规模删除压力验收。

## 15. API Review

当前 net/http 路由与 OpenAPI 的 P3/P4/P5/P6 入口对应；未发现隐藏管理 HTTP、业务 handler 直接执行算法或备份/全库 hash。写 DTO 与 dbgen 分离；Swift 使用动态 JSON，优点是少重复 DTO，代价是读取字段的漂移更依赖运行断言。

Redocly 2.71.1 本轮 exit0，八项逐项为：health/ready/livez/readyz 无 4XX，以及 Event/SeriesPoint/WorkoutDetail/RoutePoint 被 no-unused-components 命中。四个 schema 用于 NDJSON 的外部合同，不能只看 lint 删除。与历史警告 identity 一致，并非只比较数量。

lint 不能发现运行解码漂移。004 证明 Training required、null 和拼写边界较弱；006 证明服务端合法响应与 Swift 限额不一致。Workout 内容冲突当前返回 sample_identity_conflict（store.go:473），而 P3 文档也列 workout_identity_conflict；现有测试固定了通用旧码，客户端均按 409 阻断。本轮将此记录为低风险合同命名维护项，不把它夸大成幂等漏洞。

## 16. Worker / Concurrency Review

真实 Worker 单 processing slot，60 秒 lease、20 秒 renew；失去 renew 取消计算。Claim 使用 SKIP LOCKED、attempt/token 和最新代际优先；训练 queued successor 吸收 generation。旧 queued job 身份保留，完成时标 superseded，而不是删除 receipt 引用。

P4/P5 发布在 dataset→job→scope/plan 的锁内再次检查 generation/token/running/lease，结果、pointer、clean、run/job/attempt 同事务；末尾用 clock_timestamp 及捕获 deadline 再校验，未用固定 transaction now 冒充仍有效。Lease 事务中到期、旧 token、新 generation 先完成、SQL 故障点和真实 Worker SIGKILL/60s 重领本轮都重跑通过。

没有确认 stale Worker 可发布的漏洞。Store.Complete/Fail 的旧通用路径只被集成基础测试调用，production Worker 不调用；Complete 能单独 clean scope 且注释仍称 future publisher，属于应隔离的历史测试 API，不能误报为当前线上分析伪成功。将来清理应迁移测试，而不是直接删除租约覆盖。

## 17. Security Review

Bearer 摘要鉴权、dataset 限定、read-only device、collector 事实事务内复验、撤销 401、cross-dataset 404、PG 参数化查询及 identifier 转义路径经源码和真实角色测试核对。runtime 与 owner/backup/monitor 分开；DB 角色不是 dataset RLS 边界，现有可信 API 单用户模型没有宣称抵抗被攻陷应用进程的租户隔离。

TLS min1.2、CA/hostname/key/expiry 预检与实际私有 CA 链通过；Release 无 ATS 全局放宽，device-only Keychain；Debug HTTP 仅配置 host。HTTP/SQL/body/admission 预算存在，错误/URL 日志脱敏测试有真实断言。Training JSON 弱校验见 004，不能据此推断 SQL 注入。

仓库 secret guard exit0：未发现私钥块、未例外的明文密码 DSN 或不应提交的 secret 文件。该工具是模式扫描，不是凭证泄漏的形式化证明；本轮未打开私人 secret/环境文件。备份 SSH 严格 host key、固定身份、NOFOLLOW、受限文件/目录、明确隔离目标和单事务恢复均有控制测试。

正式 PKI、手机信任、OS/防火墙、凭证安装/轮换和真实云权限仍未验收；没有为本轮审查执行真实变更。

## 18. Backup / Operations Review

当前月度策略与代码一致：每月1日03:00上海时间、SSH、无额外文件加密、最近三份成功副本。源端只读整库 custom dump；远端大小/SHA重读、manifest 最后原子发布、fsync、双端锁，成功后只清受控旧副本。日历用月份计算及 24h grace，不把固定30天/26h当月度规则；daily 旧工具显式保留，月度监控使用 --monthly。

本轮完整 monthly Compose：loopback SSH 五次成功最终三份、断连/超时保留旧集合、失败后手动成功解除和 Worker 告警保留；2673588 bytes 归档在另一新凭证空卷 PG18 实际恢复，45表 count/hash、FK/不变量、原 receipt 和 P5 历史核验通过。没有真实云下载往返。

原 P7 加密恢复也通过（本次 drill 39 秒），错 key/密文损坏/替换 SHA/未标记/非空拒绝控制另跑通过。API/Worker/PG SIGKILL、网络中断、256 MiB 私有 tmpfs 真实 ENOSPC、三轮生命周期和有 publication 屏障的 soak 通过，所有清理 exit0。

Recovery Resync 仍 BLOCKED：旧快照回退 receipt/seq/tombstone，手机可能领先，GC 后未必还有原 payload；方案 B 的独立本地会话仍未实现。这是 **Release / Recovery Risk**，不影响未回退数据库的正常同步协议。用户选择的月度 RPO 和无额外加密不在本轮擅自改策。没有安装正式 cron/监控或通知。

## 19. Performance Review

已有 P4/P5 算法/查询及 P7 1/3/5 年低密度合成历史有场景、样本数和 race/non-race 口径；SQL fixture、真实 P3 上传、单次 Worker 观测和纯算法100次分布没有混为手机 SLO。历史 million-series/大 package/10000页不能证明百万 route points 的分析能力。

本轮不重复全部极端 benchmark。新增两类定向证据：005 的默认 planner 实际历史读取，以及 006 的服务端实际 DTO 字节数。当前查询很快不代表读取范围随数据量有界；合法页数量有界也不保证响应内存/字节有界。

本轮 Compose soak 194.920秒、144 reads、6次增量及逐次确认重生成、零错误，读 median17.934ms / nearest-rank P95 39.296ms。此为 **稳定 publication 窗口**，不证明 dirty 期间 current API 永远可用、长期无泄漏或生产吞吐。

另有可确认的静态 N+1：LoadTraining:132–159 对最多4096 executions 逐项 loadExecution/旧 Workout；loadExecution:28 读各 execution 全部 feedback，其中部分内容随后并未用于 Compare。实际长期延迟/响应规模影响为 Needs Verification；不把未经测量的 timeout 当 Finding。enrichment/override 历史读取、长期 staging/runs/receipt 增长也应继续设定明确运行预算。没有证据需要 Redis/Timescale/Kafka/新服务。

## 20. Tests / Evidence Review

| 类别 | 本轮执行/证据 |
|---|---|
| Unit / Golden / Property | Go普通183个 pass事件；确定性、数值、边界与既有 golden 真正比对期望文件；未设置 RAI_UPDATE_GOLDEN |
| Race | Go race183个 pass事件；完整 PG/race 命令365个 pass事件；fail=0、skip=0 |
| PostgreSQL Integration | integration package 313.591秒，fresh/upgrade/事务/角色/并发/恢复边界均执行 |
| Go质量 | gofmt -l 无输出、mod verify、vet、build、integration/scale/p7capacity vet、276依赖包/249在线包边界通过 |
| Docker Compose | 完整月度/P7 regression exit0，718.796秒；唯一项目/空卷、实际TLS/SQL恢复/故障/清理 |
| Swift Core | Swift Testing 41项，最终 suite pass；初始 XCTest zero discovery 不被当作总数 |
| Swift iOS Unit | 新建 iPhone12ProMax class / iOS26.5 模拟器，8 Normalizer tests，0失败；模拟器已删除 |
| Swift Build | Debug Simulator、Release generic iOS/min18；Swift6 complete strict concurrency；均未签名构建通过 |
| Swift UI | 本轮未重跑完整 UI/截图；引用历史 P6 默认/大字号证据，不称本轮 PASS |
| Synthetic E2E | 本轮 Compose 真HTTP/API/Worker/PG；Swift URLProtocol 仅 unit，macOS GC 实际 SIGKILL 另计 |
| Real Device | NOT RUN / NOT VERIFIED |
| Directed audit probes | 5个首轮 top-level、4个 JSON子例；随后2个 follow-up及1个 route probe，均明确断言当前反例行为 |

新增探针用 **Go overlay + 外部临时文件**运行于隔离源码快照，未编辑仓库测试/业务文件。配套 JSON 嵌入合成探针源码、名称/命令/耗时/退出码及观察摘要，便于后续复现。“探针 PASS”表示反例被复现，不表示该功能正确。

没有发现把 URLProtocol mock 声称为本轮 E2E、无断言测试或静默 skip 冒充完成。ignored cleanup error、测试 fixture Encode error、显式环境 tag 和 golden 更新开关已结合上下文审查；没有把关键词命中直接当 bug。较老小表强制索引测试的证据范围见第14节。

P4/P5/P6/P7/恢复 evidence 均保存了 command、环境和 source hash/fingerprint 或当时 dirty snapshot caveat。阶段基线 SHA 不含当时未提交实现的情况已明示；不能把历史 PASS 直接等于当前 HEAD 的全部正确性。未发现足以另立高严重度 Finding 的虚构验收声明。较早 soak 未留状态码的缺口已在恢复结果保留，后续口径改变没有抹去该事实。

本轮 **sqlc regenerate 未执行**：远端 PATH 无 sqlc。本轮实际 build/查询测试通过；历史固定1.31.1生成一致性证据可查，但不冒充新执行。完整极端 scale/p7capacity、Git远程CI、完整 UI 和真机均不计本轮通过。

## 21. Technical Debt

| 分类 | 具体项 |
|---|---|
| Must Fix Before Real Device | 001 合法 Route → P4 永久失败；002 合法 Override rebase 阻塞。补齐实际 Store/Worker 边界回归，再进设备验收 |
| Should Fix Before Production | 003–006；真实设备/PKI/凭证/云取回/cron/监控门禁；Recovery Resync 按已接受损失边界另立明确人工流程，不能静默接续旧库 |
| Safe Maintenance Backlog | 007 活跃文档；unused AssociationRequest.Canonical；基础测试专用 Complete/Fail 的归属；重复 Compose harness；证据体积和少量 formatter/诊断 warning |
| Do Not Fix / Intentional Complexity | immutable batch/receipt/page、永久 tombstone、dataset commit锁、staging/seal、renew/fencing/generation、Base+Override+Effective、frozen Execution、历史保留、独立管理路径 |

明确的规模债务包括：命令/版本/反馈/暂存长期保留；Swift local state 完整 JSON 重写及 Workout 缓存增长；动态 JSON DTO 缺少自动跨端 schema 校验。没有实际失败证据的项目留在维护/验证清单，不合并成新的 P1。

## 22. Dead Code / Removal Candidates

### Safe Removal Candidates

| 候选 | References found / runtime references | Tests | Removal risk |
|---|---|---|---|
| internal/syncing/association.go:36 的 AssociationRequest.Canonical 方法及仅供它使用的 crypto/sha256、encoding/json imports | 全模块39处 .Canonical 调用逐项核对：调用者为 Envelope/Event/WorkoutPackageRequest/Enrichment；AssociationRequest 方法 runtime=0；该包为 internal，无对外发布 API | route_test 只调用 AssociationRequest.Validate；无该 Canonical 的直接测试；删除后仍须 Go build/受影响测试 | 低。仅删除这个未用方法，不删除 request/Validate 或关系协议；若未来选择给 source 内容加幂等摘要，可另作设计，不保留假调用 |
| ios/RAI/README.md:40 的过时全量保留表述 | 当前说明引用1处；runtime=0；P7/DR 文档已明确可GC | GC/RecoveryBoundary/本轮SIGKILL反证该说明 | 低。用准确说明替换，不删历史阶段证据 |

以下 **不属于现在可以直接安全删除**：

- Store.Complete/Fail：production调用0，基础集成测试分别6/4处调用；应先迁移测试并保留租约/代际覆盖，再决定移出 production。不能只按“未来 publisher”注释删除测试。
- pre-v4/pre-v10 兼容约束、legacy page index/probe：有升级/原回执测试和冻结 migration 依赖；保留。
- P3/P4/P5 分阶段 Compose 脚本：仍有 README/历史可复现引用，且存在不同断言；可将来共用 helper，不直接删历史入口。
- Event/SeriesPoint/WorkoutDetail/RoutePoint：NDJSON 合同引用，Redocly unused 不等于死 schema。
- P7 daily 加密工具：有显式日常兼容入口和负面控制，月度策略没有把它变成不可调用死代码；不得安装两套正式 schedule。
- P6 历史媒体：tracked 总体182089541 bytes；55组逐字节重复媒体、可重复占用71563711 bytes。但 manifest/失败历史/比较基线有引用且用户要求保留，不列为直接删除候选，应先约定归档与引用策略。

未发现可凭充分证据直接删除的生产 Swift View、旧 API client、第二 scorer 或独立 manual plan。

## 23. Architecture Scores

评分针对当前实现及证据成熟度；已延期的设备门禁是验收限制，不自动计为代码 bug。分数不是生产批准。

| 维度 | 0–10 | 扣分依据 |
|---|---:|---|
| Architecture | 8.5 | 边界清晰；跨 P3/P4 与 P5 重生成合同仍有两个合法输入衔接缺口 |
| Data Integrity | 8.5 | 事实/回执/屏障事务可靠；输入到派生的有效性合同不完全一致，恢复旧快照仍有正式阻断 |
| Concurrency | 8.5 | renew/fencing/末尾墙钟/代际真实验证；基础测试专用完成路径仍留在 production storage，不能视为穷尽交错证明 |
| Sync Correctness | 8.0 | P3事实一致性未见新漏洞；Route下游不兼容，设备anchor/后台系统边界未验 |
| Analysis | 7.0 | 确定性与缺失模型明确；001/003，输入250k上限和生理效度边界保留 |
| Training Plan | 6.5 | 核心持久覆盖/执行安全；002可阻塞正常改日程，005/006暴露增长边界 |
| iOS Client | 7.5 | 原生/严格并发/无第二事实；006，完整设备/GPU/iOS18运行门禁仍无证据 |
| Security | 8.0 | 最小角色/TLS/Keychain/日志控制实测；004弱解码与真实入口/凭证现场未验 |
| Operations | 7.5 | 两类备份实际隔离恢复、故障与监控通过；真实云取回/调度和可执行设备恢复未完成 |
| Performance | 7.0 | 现有基线有可信口径；005/006及静态N+1/长期增长需明确预算 |
| Test Quality | 7.5 | 有实PG/故障/golden/负面控制；仍遗漏合法输入组合与跨端响应限额，部分早期索引证明较弱 |
| Maintainability | 7.5 | 模块克制且依赖少；活跃文档漂移、重复harness、未用helper与约182MB证据增加成本 |

## 24. Fix Waves

| Wave | 范围 | 退出证据 |
|---|---|---|
| Fix Wave 0 — Critical correctness | 当前无 P0，不虚构紧急数据修复 | 保留事实/回执/墓碑不变量 |
| Fix Wave 1 — P1 correctness/security | 001、002；先处理会阻断正式派生链路的合法输入 | 新反例成为期望行为回归；真实PG事务/Worker及Go race/vet/boundary通过；不丢raw/覆盖、不改历史execution |
| Fix Wave 2 — P2 architecture/debt | 003–006；日/窗口可用性、严格解码、索引范围、跨端响应预算 | golden/缺失、HTTP invalid/valid matrix、默认EXPLAIN/DST、实际Swift响应读取与分页恢复 |
| Fix Wave 3 — P3 cleanup | 007与经引用证明的低风险未用helper；测试API/harness/证据归档另审 | 文档引用和受影响编译/测试；保留历史报告/不擅删不可变协议状态 |

本轮只描述修复，不执行任何 Wave。

## 25. Known Deferred Gates

~~~text
P3 REAL DEVICE ACCEPTANCE:
DEFERRED / NOT VERIFIED

P6 REAL DEVICE ACCEPTANCE:
DEFERRED / NOT VERIFIED

DISASTER RECOVERY RESYNC:
BLOCKED / NOT IMPLEMENTED

PRODUCTION RELEASE READY:
NO
~~~

统一保留：真实 iPhone、HealthKit 读授权/可见历史/删除/GPS/series/关联、Background URLSession 和 force-quit、真实手机网络、iPhone12ProMax性能、iOS18实际runtime、Liquid Glass/Charts Metal GPU、WorkoutKit独立授权/系统排程、真实云端安装/上传取回、正式cron/通知、生产RPO/RTO、长期vacuum/bloat、宿主断电/存储硬件故障。

当前无正式 V2 DB 是环境事实；旧 rai-postgres 未被当作 V2。恢复协议未实现只按 Release / Recovery Risk 处理，正常数据库未回退时不触发此模式。没有把这些延期拆成多个 P1。

## 26. Final Answers

1. **总体架构是否健康？** 是，核心边界和持久事实模型健康；当前正确性需修复两个 P1，不能沿用无条件“验收可继续”。
2. **是否需要整体重新设计？** 没有这样的证据。无需微服务、Redis、消息平台或换数据库。
3. **P3 Sync 是否存在明显数据一致性漏洞？** 本轮未发现普通事务/幂等/delete-wins 漏洞；001是合法Route与P4衔接缺陷，必须修复。灾难回退不属于正常同步证明范围。
4. **P4 是否只有一套正式算法？** 是，纯Go；Swift/SQL/Python未发现第二套在线评分。001/003仍属该实现内部缺陷。
5. **P5 Override/Effective 是否真正安全？** 用户事实、字段保护、并发与历史snapshot基础安全有真实证据；不能称完全安全，002使合法覆盖阻塞新Base，006影响审计可读性。
6. **P6 是否存在第二套本地业务事实？** 未发现。outbox、命令journal、执行ID/步骤游标是协议与呈现状态，不是第二套训练/分析事实。
7. **PostgreSQL schema 是否有明显整体问题？** 未发现需重做 schema 的设计问题；复合FK/约束/不可变版本有效。005是现有SQL不能充分利用时间索引，长期增长策略仍有债务。
8. **Worker/lease/fencing 是否有竞态风险？** 本轮未确认 stale publish 竞态，真实租约/代际/进程故障回归通过；测试不穷尽所有交错，旧通用Complete不在production调用图。
9. **正常路径是否有无界扫描或明显性能问题？** 有005所示历史扫描，以及006的合法大响应；当前定向查询未超时。N+1/反馈增长有源码风险，规模影响仍Needs Verification；同步主路径未发现全库摘要或全量重算。
10. **现在该删除什么历史兼容/死代码？** 无须大清理。仅 AssociationRequest.Canonical 与过时活跃说明有低风险候选证据；不能删除 tombstone/receipt/fencing、旧migration、NDJSON schema或历史验收材料。
11. **自动化足以支撑 PASS WITH RISKS 吗？** 足以支撑已声明且有界的工程机制，不能覆盖本轮新反例，也不能证明真机/生产通过。两个P1存在时，不支持当前HEAD直接进入设备验收。
12. **无真机/正式DB时下一步最合理是什么？** 单独授权的正确性修复任务先完成Wave1，随后Wave2并补实际跨端回归；再安排真实iPhone验收与按用户损失边界实现最小人工恢复。继续使用隔离PG18合成数据，正式部署/云安装另行批准。

## 27. Final Verdict

**FIXES REQUIRED BEFORE DEVICE ACCEPTANCE**

依据为两个已经在真实隔离 PostgreSQL18 中重现的 P1，不是设备缺席。总体架构可以保留；本轮到报告和机器证据为止，未修复、提交、推送、部署或开始下一阶段。
