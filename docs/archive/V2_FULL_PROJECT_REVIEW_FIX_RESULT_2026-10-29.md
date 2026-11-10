# RAI V2 Full Review Findings Fix Result

2026-10-29，Asia/Shanghai。范围仅 RAI-FULL-001–007 及明确授权的 AssociationRequest.Canonical 清理。

## 1. Status

**PASS WITH RISKS**。RAI-FULL-001–007 已修复，分 Wave 门禁、最终 Go/真实 PG/race、四组隔离 Compose、Swift/Xcode/相关 UI 验证均通过。真机、生产和灾难恢复的延期门禁保持。

## 2. Repository

- Repository：`/Users/ta/rai`；branch：`main`。
- `FIX_BASE_COMMIT=0308b8658adc304040e445bc7f6ab61d212472d3`；开始前实际执行 pwd、status、branch、HEAD、log -10、diff --stat、remote -v。
- 入口仅两个 untracked：原全面审查报告及 `docs/evidence/full-review-2026-10-29.json`。二者保留，不纳入本轮“新增审查”或改写。
- 最终工作树为38个tracked修改、14个untracked（含入口原有2份审查文件）；源码摘要/原文保护见机器证据。没有 commit、push、reset、clean、部署、切换旧入口或修改 `../running`。
- 无正式 V2 DB；只使用一次性隔离 PG18.6、随机密码、合成数据。未查询旧 `rai-postgres`。
- 测试镜像 source_commit 是上述基线，包含尚未提交的本轮增量；实际源码哈希另外记录，不能把基线 SHA 当成已提交修复版本。

## 3. Findings Closure Summary

| ID | Severity | Before | After | Status |
|---|---|---|---|---|
| RAI-FULL-001 | P1 | 等时 Route 封口后整场 invalid，旧结果隐藏 | 跳过无效连接、明确路线质量，新的 Workout publication 可读 | FIXED |
| RAI-FULL-002 | P1 | 合法历史覆盖使新 Base 无法发布 | 事件保留，合法 Effective 与明确 rebase conflict 一起发布 | FIXED |
| RAI-FULL-003 | P2 | 旧日 unknown 抹去当天已知 daily_load | 当天与180日窗口分别判断可用性 | FIXED |
| RAI-FULL-004 | P2 | 缺 required、拼写变体、重复键、非法 null 返回200 | 严格 wire 422；合法 HR/pace 清空及原命令重放通过 | FIXED |
| RAI-FULL-005 | P2 | 时间函数/OR 造成历史扫描 | 绝对时间范围和独立 identity 查询；默认 planner 读取窗口 | FIXED |
| RAI-FULL-006 | P2 | 合法 audit/history 超过 Swift1 MiB | 摘要、固定版本审计/详情分页，跨端字节预算；旧入口有迁移路径 | FIXED |
| RAI-FULL-007 | P3 | 活跃 README 声称全部 delivered 材料保留 | 正确说明可GC、anchor持久、pending/orphan保护和 replay 边界 | FIXED |

所有原反例先在当前 HEAD 复现再修改；没有一项被标成 NOT REPRODUCIBLE。原探针通过代表复现缺陷，区别于本轮正确行为测试通过。

## 4. RAI-FULL-001

**Status:** FIXED。

**Root Cause:** P3 保留非严格时间顺序；P4 将一个不可计算的连接当成整个 Workout 输入非法。

**Implementation:** 保留 raw/chunk/point/order/timestamp/count/manifest/seal/receipt。相邻同 route 时间不递增时不连接，不跨 route；其他合法边继续计算。route distance 无合法边时 null/insufficient，有边时 partial；details 给 validConnections/skippedNonIncreasingTimeConnections。duration、原始 distance、HR/TRIMP 的独立处理不因该连接失败。

**Tests Added:** 正式 `TestFullReviewEqualRouteTimestamp`，真实 PG + Store + jobs.Process：严格递增、首对相等、末对相等、全部相等、跨chunk相等、多个route、无水平精度、坏精度点相邻。逐点比较 canonical raw，重复 seal receipt 相同，确认新 run/version/current publication 与非路线指标可用。

**Regression:** Wave1 定向及完整 P3/P4/P5 race/PG/Worker publication 通过；最终门禁见第14节。

**Behavior Before:** upload/seal 成功后 failed/analysis_invalid_input，current 隐藏。

**Behavior After:** 新 v2 analysis 正常发布，路线 partial 或 insufficient，Workout 可读。dirty 期间仍按原合同隐藏旧 pointer；新 publication 成功后恢复。旧 failed run/result 不重写，需要显式有界 recompute 或后续事实变化建立新代际，不自动全历史重算。

**Remaining Risk:** 真实 GPS/HealthKit 未验证；原250000点/observations分析上限及坐标合法性检查保留，不声称百万点分析已实现。

## 5. RAI-FULL-002

**Status:** FIXED。

**Root Cause:** 仅处理 removed/REST anchor，未处理 stable slot 存在但历史字段覆盖与新处方不兼容。

**Implementation:** 复用 reasonCodes/conflicts/modifiedByUser。最新 active 字段胜；在原 anchor 上合法、在新处方上非法的已接受字段保留事实，当前 Effective 保留合法值，暴露 `<path>_override_incompatible_with_session_structure/v1` 和 `override_rebase_conflict/v1`。新非法编辑仍拒绝，不能伪装成冲突。移除/REST的原 anchor 规则保留；从 anchor 恢复时重新推导冲突，避免已 revoke 标记被后续 anchor 带回。

**Tests Added:** 正式 `TestFullReviewOverrideScheduleTransition` 经过真实配置/Override/Worker：Interval距离0→Recovery，Base/Effective正常发布、原事件逐值不变、旧Effective及started Execution不变，revoke恢复Base并清冲突。纯函数结构矩阵覆盖 distance/duration/move、Recovery→Interval、REST；latest-field/revoke/legacy replay/anchor purity；新增撤销后slot移除的旧冲突标记回归。

**Regression:** 原双客户端 revision、edit during generation、stale generation/fence、removed anchor、execution/feedback/事务故障回归通过。最终门禁见第14节。

**Behavior Before:** accepted config revision2，随后整份计划永久 invalid_training_input，仍展示旧Base。

**Behavior After:** 新Base发布、稳定Session UUID保留，合法Effective显示冲突；revoke后冲突消失。没有drop事件、改旧历史或假成功。

**Remaining Risk:** 单字段REST编辑仍有原合同限制；冲突值保存在事件内，并不宣称已经正常应用。

## 6. RAI-FULL-003

**Status:** FIXED。

**Root Cause:** daily_load 误用整个180日窗口的 valid 谓词。

**Implementation:** dailyValue 只由当天 unknown 决定；历史窗口继续保守传播 missing。无记录是0 recorded load，不等于导入完整；完全无历史不生成readiness/recovery。

**Tests Added:** `TestFullReviewDailyLoadIsolation` 覆盖旧日缺失/当天已知、当天缺失、当天无记录、空历史和完整窗口；`TestDailyLoadResolvedHistoryMatchesFullRecompute` 验证缺失解决后的单日/完整重算一致。

**Regression:** P4 golden、有限核、缺失、增量和正式PG发布回归通过。

**Behavior Before:** yesterday unknown + today100 → daily_load=null。

**Behavior After:** today daily_load=100，fitness/fatigue等仍null；当天unknown保持null。

**Remaining Risk:** 原有限180日核、load-only模型及import coverage未校准的边界保持。

## 7. RAI-FULL-004

**Status:** FIXED。

**Root Cause:** 独立 encoding/json 解码接受 case-insensitive key、duplicate last-wins、null→零值，未声明required。

**Implementation:** 提取并复用P3严格词法/字段边界，RawMessage由其所属字段继续验证；Training独立声明required/null规则。config/goal/manual session/step递归检查；expectedOverrideRevision缺省拒绝，0对首次写合法；config/execution revision>=1。合法targetHR/paceSPerKM清空保留。没有改变typed command hash。

**Tests Added:** 正式 `TestFullReviewTrainingJSONDrift` 真实HTTP/PG验证原四例及unknown/type/null/nested case均422、无事件落库，合法clear200、格式变化原回执相同、同command异内容409。单元矩阵还覆盖nested duplicate、缺step字段、trailing/root null、所有request required fields。

**Regression:** P3严格解码及P5命令/幂等/并发完整回归通过；当前Swift请求通过实际API。

**Behavior Before:** schema无效JSON可以200并进入耐久命令事实。

**Behavior After:** 无效wire拒绝，合法nullable和canonical重放成立。

**Remaining Risk:** 依赖旧宽松解码的非法客户端需要修正请求；旧已存事件/回执不重写。

## 8. RAI-FULL-005

**Status:** FIXED。

**Root Cause:** 对event_start做timezone/date函数，再与specific ID共用OR，限制输出行数却未限制历史读取。

**Implementation:** Go按Plan冻结IANA时区、calendar AddDate计算首日与次日午夜的绝对边界。Recent SQL直接 event_start >= start AND < end；specific identity独立SQL。aggregate LATERAL以选中Workout定位clean publication/run/dirty/results。保持dataset、参数版本、排序和4096输入上限，未加索引/缓存/migration。

**Tests Added:** `TestTrainingWindowCalendarBoundaries` 覆盖常规、春季23h、秋季25h、Apia偏移跳变和非法日期；正式 `TestFullReviewTrainingQueryBound` 默认planner/真实SQL，1/3/5年每日本8场合成历史及旧Workout迟到execution association。

**Regression:** 时间/DST、迟到identity读取、真实PG及完整P5/race回归通过；默认EXPLAIN与原始计划入JSON。

**Behavior Before:** 原1825行返回28却过滤1797。

**Behavior After:** 绝对边界定位最近窗口，独立non-race定向观测如下（单次、非SLO）：

| 年数 | fixture rows | returned / scanned Workouts | shared hits | execution ms |
|---|---:|---:|---:|---:|
| 1 | 2920 | 224 / 224 | 2736 | 10.723 |
| 3 | 8760 | 224 / 224 | 3184 | 11.736 |
| 5 | 14600 | 224 / 224 | 3184 | 12.281 |

未使用 enable_seqscan=off。时间索引条件实际出现，读取不随全部历史线性增加；旧identity路径仍能取得窗口外显式关联来源。

**Remaining Risk:** 这不是冷盘/生产密度/长期SLO。另一次3584场密集加载在并发验收下触发查询预算，不记为Worker容量通过；006大context读测试使用预构造的已发布合成context隔离字节合同，不借此宣称密集loader验收。

## 9. RAI-FULL-006

**Status:** FIXED。

**Root Cause:** 限版本数未限制响应字节；完整Base/Effective/context/all overrides组合超过客户端固定1MiB。

**Implementation:** 新summary `/history/versions`、固定版本 `/audit` 与五种 `/detail` section；版本号、override revision、不可变数组ordinal游标，无SQL OFFSET。保留identity、revision、Base/context lineage及完整事件，可逐页重建和按mergeVersion重放。旧聚合端点保留小响应旧形状，Deprecation/Link及413明确引导迁移。

Server/Swift新页均256KiB；其余snapshot/command/同步仍1MiB。条数limit1..50且按实际JSON bytes缩页。新event32KiB、Effective960KiB及receipt1MiB在COMMIT前检查，超限拒绝不保存半命令。没有全局扩容、空列表吞错或删除审计。

**Tests Added:** 正式 `TestFullReviewAuditResponseBudget`、`TestFullReviewHistoryResponseBudget`，真实PG/HTTP：256×2000字符notes、16周/50版本、旧入口413/Link、所有页完整、完整detail/context重建及Effective重放、后续revoke不改旧版本。另覆盖转义字符促使缩页、3584条context分72页和跨dataset404。

**Regression:** 完整P5/P6 core、PG/race、Swift和相关UI回归通过。

**Behavior Before:** 原audit约1,208,155B、history1,696,715B，超过Swift1MiB限制。

**Behavior After:** 新audit limit20共13页，实测最大92,046B；history summary limit13共4页，最大4,028B，cursor最终0。单个大context通过section分页完整读取。

真实Swift→隔离API/Worker/PG读取256条audit/13页、257个16周版本summary/13页、112节处方；与URLProtocol单元区分。Swift reader失败/超限保留cursor和已有行，空/末页停止，版本变化隔离在途结果。课程详情仅增加加载更多/重试/空态；相关UI test通过，Liquid Glass无重设计。

**Remaining Risk:** 旧客户端遇413须迁移到新入口；长期版本/事件retention不在本轮。大context字节fixture不代表3584场密集Worker吞吐验收。

## 10. RAI-FULL-007

**Status:** FIXED。

**Root Cause:** P7加入GC后活跃iOS README未同步。

**Implementation:** 仅更正当前README对应段落：完成页payload/transfer及receipt cache可GC，delivered anchor持久；pending/unfinished保留；128默认/512硬上限、引用复验、有界marked队列；未知orphan/旧未标记缓存不任意清理；exact replay材料不能保证永久存在。P3/P6/P7历史结果原文保留。

**Tests Added:** 不为说明文字新增镜像测试；核对真实OutboxStore mark/finish/sweep与既有GC/RecoveryBoundary断言。

**Regression:** Swift45核心（含GC/恢复边界）通过；最终Go编译/测试见第14节。

**Behavior Before:** 误导为所有delivered载荷与receipt一直保留。

**Behavior After:** 活跃说明与实际GC生命周期一致。

**Remaining Risk:** GC后无法承诺完整exact replay；Recovery Resync继续BLOCKED。

## 11. Algorithm Versioning

workout.bundle/features v2处理路线连接质量；training_state.bundle/fitness_fatigue v2改变daily_load可用性。TRIMP/recovery/readiness v1公式未改。新run、result、Worker日志、CLI/version manifest均匹配实际版本；P5/Swift接受新已发布组件，保留旧v1读取。旧golden、run/result原文不改；当前CLI仅接受当前v2，不用新语义伪装v1重算，不自动全历史升级。

## 12. Training Merge Versioning

新Effective JSON记录training_merge/v2。历史缺省表示v1；ResolveEffectiveVersion保留v1投影语义，历史不重写。冲突规则、最新字段选择、撤销和anchor冲突重新推导均在v2。training_plan/v1、coach_parameters/v1、training_feedback/v1及稳定Session identity保持；生成规则和参数未改，无第二份生成器或manual plan。

## 13. API Contract

OpenAPI1.1.0、Go/Swift分页DTO与读写预算同步。P3 protocol2不变。所有新读取复用Bearer/dataset/ReadLane；没有管理HTTP。旧history/audit入口未删除，小响应兼容，大响应有明确413/Link迁移路径。完整请求/响应/游标语义见P5/P6当前addendum及OpenAPI。

Redocly2.71.1 exit0，warning identity逐项比较保持8项：operation-4xx-response针对health/ready/livez/readyz；no-unused-components针对Event/SeriesPoint/WorkoutDetail/RoutePoint。没有为了减少warning删外部NDJSON合同schema。

## 14. Full Regression

| 阶段 | 实际证据 |
|---|---|
| Baseline reproduction | 原全部代码反例22.053s；007静态来源核对 |
| Wave1 directed | PG12.120s；结构/replay纯函数矩阵通过 |
| Wave1 full | Go/race/PG integration326.045s，vet/build/boundaries，P3/P4/P5/Worker通过 |
| Wave2 directed | PG44.862s；JSON矩阵修正后通过；默认EXPLAIN及预算 |
| Wave2 budget extension | PG26.242s；转义字节与large-context section读 |
| Anchor revoke follow-up | PG4.829s及纯函数回归通过 |
| Wave2 latest full | Go/race/PG integration486.383s，vet/build/boundaries通过，包含最后anchor修正 |
| Final Go | PASS；gofmt/mod verify/普通/race/vet/build/边界/带integration,scale,p7capacity的vet；最终PG/race integration594.182s，总命令698.364s。872个test pass事件（含三轮重复单元及子测试，非872个独立PG场景），其中integration207；test fail/skip和package fail均0 |
| P3/P4/P5/P7 Compose | 全部PASS，1052.251s；唯一project、新密码/空卷，同镜像、P3重放/重启、P4/P5发布闭环、TLS/角色、实际API/Worker/PG SIGKILL、断连、45表备份恢复、三轮生命周期、实际256MiB ENOSPC；P7 cleanup exit0且独立复核通过 |
| Swift core | Swift Testing45项/6 suites通过，含ProductCore/分页/超限/错误恢复和原SyncCore |
| Actual Swift HTTP | 256audit/257summary/112prescriptions逐页完整读取通过；原命令/409/生成/执行/feedback闭环通过 |
| Xcode | Debug Simulator、Release generic iOS/min18，Swift6 complete strict concurrency通过；Release未签名 |
| Related UI | iOS26.5独立12ProMax-class，test06AuditPagination 68.546s，1项0失败 |
| iOS unit | Normalizer8项0失败 |
| OpenAPI | exit0，8项warning identity不变 |

失败轮保留，不计PASS：JSON矩阵先被正常429限流阻断，测试改为遵守Retry-After；大context密集Worker setup触发预算，读字节测试改为明确预构造published context；首轮UI漏传loopback Debug host被精确主机校验拒绝，按原合同补齐构建参数后通过。另一次Probe构建误在repo根运行（无Package.swift），在正确package目录重跑通过。

所有数据库与HTTP/Worker测试合成，URLProtocol只是unit；进程/网络/恢复Compose与真机门禁分开。未重新运行整套P7 capacity benchmark或全套P6截图基线。

## 15. Data Correctness Gates

最终872个通过事件覆盖原batch重复同回执/同identity冲突、Create→Delete→旧Create无复活、route staging无部分发布；P4 generation/fencing/lease与publication故障原子性；P5双客户端revision、用户编辑与生成竞争、stale generation拒绝、override保留、started execution不可变。具体test名字和最终pass事件见JSON；没有删除Complete/Fail、legacy probes或历史兼容覆盖。

## 16. Low-Risk Cleanup / Migrations

AssociationRequest.Canonical runtime引用0；仅删除该方法和仅为它服务的sha256/json imports。Request、Validate、relation协议及其他Canonical不变。00001–00013逐字节不变，无00014或schema新增。原方案、历史阶段结果、原review及旧证据保留。管理/备份/恢复边界未进入在线依赖。

## 17. Cleanup

独立SSH只读复核：18个本轮root测试PG容器均不存在；P3/P4/P5、P7 source/restore/disk共6个project的container/volume/network均为空。两个本轮Swift host源码目录、私有配置/临时凭证及本轮新建模拟器均已移除。fnOS当前仅原rai-postgres/frpc运行；没有连接旧库或清理未知资源。P3/P4/P5用临时私有Compose override分别生成新密码，原开发配置未更改；P7 source/restore各自新凭证。该测试override及清理复核方法保存在机器证据命令中。

## 18. Deferred Gates

```text
P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
DISASTER RECOVERY RESYNC: BLOCKED / NOT IMPLEMENTED
PRODUCTION RELEASE READY: NO
```

真实HealthKit/授权/anchor/GPS/后台/iPhone强杀/网络、iOS18 runtime、Metal/Liquid Glass GPU、硬件性能、WorkoutKit、真实入口/PKI/云取回/正式调度、生产RPO/RTO与长期存储仍未验收。本轮代码和模拟器/PG通过不升级这些状态。

## 19. Evidence

机器证据：`docs/evidence/full-review-fix-2026-10-29.json`。保存FIX_BASE_COMMIT、源码/保护文件摘要、各轮命令/退出码、完整默认EXPLAIN、读取预算/游标/页数、Swift/Xcode/UI、warning identity、失败轮和清理事实，不保存凭证或响应健康正文。
