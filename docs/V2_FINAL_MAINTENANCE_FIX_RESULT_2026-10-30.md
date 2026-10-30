# RAI V2 Final Maintenance Fix Result

2026-10-30，Asia/Shanghai。范围仅 P2-1、P2-2、P3-1、P3-2、P3-3 与当前依赖计数补证。

## 1. Status

**BLOCKED**：服务端、真实 PostgreSQL、隔离 Compose 和文档验证通过；P2-2 的完整 Swift Testing、iOS Debug/Release 与相关 UI 门禁未完成。当前机器只有 Command Line Tools，缺少 Testing 模块和完整 Xcode。补充 Swift 可执行探针通过不能替代这些门禁，因此不能宣布五项全部关闭。

## 2. Repository

- `FINAL_MAINTENANCE_BASE_COMMIT=1783517900520ed6aedc3c97ef497bc8fe0ef5ca`，branch `main`，仓库 `/Users/tp/raI`。
- 入口 `git status --short`、`--porcelain=v1` 与 diff stat 均为空；已执行 pwd、branch、HEAD、log -10、remote -v。
- 任务给定基线为 `945db85ee5ff2920158a559597e0dabcff4d58c6`；实际 HEAD 已包含 `Fix final maintenance findings`。本轮核验既有实现，没有重复修改业务代码或回退工作区。
- 修复前比较使用 `git archive 945db85` 的独立临时源码快照，附加当前提交已有的定向测试；测试驱动的 baseline 模式明确断言旧行为。没有 reset、clean、rebase、merge 或改写当前 checkout。
- 交付变更仅本报告、机器证据、ARCHITECTURE 和 IMPLEMENTATION。入口586个tracked文件逐一核对，业务源码、测试、脚本、部署配置、合同、原方案、13项migration、历史报告及旧evidence均保持原字节。
- 没有 commit、push、deploy、真实数据迁移或修改旧 `../running`。

## 3. Finding Summary

| ID | Severity | Before（945db85） | After（1783517及本轮文档增量） | Status |
|---|---|---|---|---|
| P2-1 | P2 | 999新增+1删除产生1000个sample jobs | 同type/UTC日1个job，回执与删除屏障稳定 | FIXED |
| P2-2 | P2 | 不同距离/日期/目标/手动课程被视为同intent | 核心层拒绝新草稿替换pending；UI门禁未运行 | PARTIAL |
| P3-1 | P3 | 无效媒体/encoding返回200，超限422 | 五类写入口415/413/422一致 | FIXED |
| P3-2 | P3 | 24个无效lease场景中20个返回旧receipt | 当前24个全部拒绝，有效lease原receipt保持 | FIXED |
| P3-3 | P3 | 主文档版本/schema/Worker职责漂移 | 现有版本修复核验；当前基线、日期、缺失结果链接补齐 | FIXED |

代码类反例在当前HEAD的已执行探针中为 **NOT REPRODUCIBLE ON CURRENT HEAD**；修复前行为是在明确标识的945db85历史快照上复现，不能冒充本轮修改前的当前HEAD仍有缺陷。P2-2的UI实际行为仍未验证。

## 4. P2-1 Mixed Sample Batching

**Finding / Status:** P2-1 / FIXED。

**Reproduced Before Fix:** YES，历史945db85，fnOS + fresh PostgreSQL18.6 + unique Compose project；当前HEAD反例不再出现。

**Root Cause:** 原路径仅在整批都是sample upsert时批量聚合；遇到delete回退逐事件dirty generation/job。

**Implementation:** 1783517先对collapsed事件分类，sample upsert走批量事实路径，其他事件保持applyEvent；新增与已知sample删除在同一事实事务中累积sample type/UTC start day范围，最后统一enqueue。未知删除不虚构范围。facts、tombstones、change events、dirty/jobs、receipt、stream/page仍同事务，ACK不等待分析。

**Tests:** TestFinalMaintenanceMixedSamples的A/B/D/E/G/H；StreamTypeBoundary、MultipleTypeStreams、MixedIdentityConflictRollsBack；完整普通PG与PG/race；Compose数据库实际计数及重启回执。

**Behavior Before / After:** 混合1000事件的job与generation增量1000→1；全删除4→1；跨3日1000→3。原receipt字节不变，重试不增job；Create→Delete→旧Create被屏障抑制，同identity异payload冲突整笔回滚。

**Remaining Risk:** 仅合成样本；耗时是单次storage事务观测，非手机吞吐、生产SLO或长期容量证明。sample依赖之外真正的Workout传播任务仍按原合同产生。

## 5. P2-2 Pending Command Draft

**Finding / Status:** P2-2 / PARTIAL（代码已在1783517实现，客户端完整验收阻塞）。

**Reproduced Before Fix:** YES。独立Swift可执行探针直接编译945db85的Product.swift，确认distance、move date、revoke target、ManualSession变化仍被旧hasSameIntent接受。

**Root Cause:** 旧intent只比较operation/sessionID/path，提交时可能选择旧pending而忽略新payload。

**Implementation:** 当前hasSameIntent复用项目JSON.data的canonical语义表示，涵盖完整payload，仅排除新commandID/overrideID；不比较任意JSON字符串。提交层resolvingPending只接受完整原命令；ProductJournal继续保留不可变身份。SessionEditor/CreatePlan在pending时锁定表单和保存，独立“重试原操作”；成功刷新，失败/409保留草稿，显式review后才释放待确认状态。

**Tests:** 当前ProductCore补充可执行探针通过：原命令重试、不同距离/日期/revoke目标、ManualSession距离/类型/日期、CreatePlan配置、journal重启、确认原操作后用新identity提交、409不视为可自动丢弃。ProductProbe以Swift6 complete strict concurrency成功构建。正式swift test在测试编译时因缺Testing失败，执行测试数0；定向UI test07/test08及Debug/Release因缺完整Xcode无法运行。

**Behavior Before / After:** 旧probe接受不同草稿为同intent；当前核心层抛resolve_pending_command_first，原journal/payload保持，确认后新草稿身份不同。UI禁用、恢复与刷新行为仅静态核对，未在本轮模拟器证明。

**Remaining Risk:** 必须在完整Xcode环境运行swift test、SessionEditor/CreatePlan两项UI，以及Debug Simulator/Release generic iOS严格并发构建。补充probe不是Swift Testing、UIKit集成或真机验收，不计完整门禁PASS。

## 6. P3-1 Training HTTP Wire

**Finding / Status:** P3-1 / FIXED。

**Reproduced Before Fix:** YES，历史快照真实HTTP/PG matrix；当前HEAD正确矩阵通过。

**Root Cause:** trainingBody原来独立读取body，缺少readJSON的媒体/encoding边界，超限被归入语义422。

**Implementation:** 现有readJSONBytes共用HTTP边界，Training保持64KiB预算及独立严格JSON/required/nullability校验。Content-Type允许标准参数，encoding必须为空。命令canonical hash不变。

**Tests:** 五类入口create/change/override/start/execution_change各11例，共55个子例；另每类验证未认证401。application/json和charset合法200；缺失类型/text/plain/octet-stream、gzip/br为415；大于65536B为413；malformed/duplicate/unknown为422。完整PG/race亦覆盖先前required、大小写、null及同command异内容409。

**Behavior Before / After:** 旧媒体/encoding错误200→415；超限422→413；严格JSON仍422，合法重试200。

**Remaining Risk:** 非法旧客户端需满足现行请求合同；已存命令和历史receipt不改写。未弱化严格JSON来获得通过。

## 7. P3-2 Upload Lease Retry

**Finding / Status:** P3-2 / FIXED。

**Reproduced Before Fix:** YES，历史快照真实PG；series/package的pending和sealed、route的sealed共20/24个无效lease场景泄露旧receipt。旧pending route已在末尾检查lease，不能把它也描述成成功绕过。

**Root Cause:** 原exact-receipt分支位于upload lease验证前。

**Implementation:** 按当前P3“每次PUT都有collector及upload lease”的合同，在dataset/device/collector验证后锁定并校验upload slot，再进行exact receipt查找。公共uploadRetryReceipt在返回前复验，新的写入在COMMIT前复验；锁持有至事务结束。

**Tests:** 三类staging × pending/sealed × missing/stale/expired/replaced，共24个拒绝场景；全部返回upload_lease_lost且无receipt。有效原lease与replacement lease均返回原字节；同identity异payload保留现有冲突码，不增chunk/job。Compose通过真实HTTP在API/PG重启后用新有效lease找回原receipt。

**Behavior Before / After:** 无效lease获得旧receipt的场景20→0；有效lease的lost-response恢复保留。

**Remaining Risk:** 矩阵不穷尽所有并发交错；不宣称COMMIT响应一定早于墙钟到期。没有改变lease TTL、队列或P3身份。

## 8. P3-3 Active Documentation

**Finding / Status:** P3-3 / FIXED。

**Reproduced Before Fix:** YES，git show945db85确认主表features/fitness_fatigue和recompute仍v1、OpenAPI1.0.0、dev Worker仅reclaim/schema11、P5正文仅v1，与当时v2实现不符。1783517已更正这些版本。本轮入口另外存在旧路径/基线/“未提交修复”表述及不存在的最终结果链接。

**Root Cause:** correctness addendum与active主文档没有同步，随后提交状态和证据引用也未更新。

**Implementation:** 核对当前源码、实际version manifest、13项migration及合成DB上的安全recompute dry-run；本轮只修active文档实际日期、仓库、HEAD、提交状态与结果/证据链接。P4/P5/P6合同、dev README、P7版本本轮核验后保留既有正确内容。

**Tests:** manifest为workout.bundle/v2、training_state.bundle/v2、dependency.invalidate/v1、training_merge/v2、OpenAPI1.1.0、schema13；两个v2 dry-run不增加job，旧workout.bundle/v1被拒绝。全部migration checksum匹配。Redocly与重复依赖边界通过。

**Behavior Before / After:** 旧版本漂移已由1783517修复；本轮active元数据与链接指向2026-10-30真实复验，并明确客户端未验。历史阶段报告、历史249计数、旧run和原方案不重写。

**Remaining Risk:** 历史PASS只表示其当时范围，不能代替当前HEAD验收；当前客户端门禁继续阻塞。

## 9. PostgreSQL Regression

全部数据库均fnOS上的fresh isolated PostgreSQL18.6、随机凭证、唯一项目/卷、合成数据；未连接既有rai-postgres。fsync、full_page_writes、synchronous_commit、data_checksums均on。

| 门禁 | 实际结果 |
|---|---|
| 当前定向PG | PASS，integration35.736s，77个pass事件 |
| 历史反例PG | 复现成功，integration37.149s，76个pass事件；此PASS表示旧缺陷存在 |
| 当前完整普通PG | PASS，integration 429.230s |
| 当前完整PG/race | PASS，integration 465.678s；命令含507个pass事件（含单元及子测试），integration 284个；fail/skip=0 |
| Go普通/race单元 | 各221个pass事件，fail/skip=0 |
| gofmt/mod verify/vet/build/tidy -diff | 最终均PASS；带integration,scale,p7capacity的vet亦PASS |
| 在线依赖及纯领域边界 | PASS；CGO1连续[249, 249]，CGO0连续[248, 248] |

首次完整race有3个失败事件（提交顺序测试、5年查询子例及父测试），原门禁未保留明细；两项定向race复跑通过，最终全量race保留原始JSON事件。未改代码、断言或超时，原失败和未确定原因保留于JSON。

完整回归包括same batch/receipt、同identity冲突、删除屏障、故障回滚、P4旧generation/fence/lease不能发布、P5用户编辑竞争及stale generation不能发布。测试名、命令、耗时与退出码保存于JSON，不把多个pass事件当作同数量独立PG场景。

## 10. Compose Regression

**PASS**，总命令279.420s，清理exit0。唯一project、新随机凭证、新卷；API/Worker同镜像，source_commit为实际1783517。

实际覆盖P3正常上传、1000事件混合页/1个新增sample job、batch和三类chunk原回执重放；API/PG停止重启、readiness503、Worker离线ACK；P4实际publication、P5创建/override/执行/反馈/再生成、frozen execution，以及Worker重启后的新Base发布。数据库实际核对1998 sample facts、1 tombstone、两页共2个sample jobs、1 scope/generation2。没有以调用次数代替耐久计数。

本轮未重跑P7完整capacity、备份恢复矩阵、SIGKILL/ENOSPC专项或P6截图/无障碍基线；这些不在本轮要求的新增范围。

## 11. Swift / Xcode

Apple Swift6.3.3，x86_64 macOS26。ProductCore/SyncCore/ProductProbe完整并发构建通过；补充pending探针及历史intent探针均通过各自断言。

**swift test BLOCKED**：no such module 'Testing'，测试编译失败，没有测试实际执行。**Xcode Debug Simulator、Release generic iOS、相关UI BLOCKED**：xcodebuild requires Xcode，active developer directory为CommandLineTools；未发现可用完整Xcode。没有把SDK/单元工具链故障改成业务PASS，也没有借用历史模拟器结果。

## 12. OpenAPI

Redocly2.71.1，OpenAPI1.1.0，exit0，0 errors，8 warnings；与full-review-fix-2026-10-29.json中rule+pointer逐项完全相同：

- operation-4xx-response：#/paths/~1health/get/responses、~1ready/get/responses、~1livez/get/responses、~1readyz/get/responses。
- no-unused-components：#/components/schemas/Event、SeriesPoint、WorkoutDetail、RoutePoint。

没有为了减少warning删除外部NDJSON schema。两次pnpm调用方式错误保留为setup失败，最终显式指定redocly binary通过。

## 13. Performance / Job Amplification

non-race、各一次storage commit观测；generation增量排除seed，dirty scopes为目标页后的scope数量。旧/新在不同时间、部分并行负载下测得，耗时不作为P95、稳定加速比或生产SLO。

| Case | input events | 新增 / 删除facts | 当前type/day scopes | jobs旧→新 | generation增量旧→新 | 旧 / 新ms |
|---|---:|---:|---:|---|---|---:|
| A_all_upsert | 1000 | 1000 / 0 | 1 | 1 → 1 | 1 → 1 | 275.450 / 417.820 |
| B_mixed | 1000 | 999 / 1 | 1 | 1000 → 1 | 1000 → 1 | 2172.981 / 341.227 |
| D_multiple_deletes | 1000 | 996 / 4 | 1 | 1000 → 1 | 1000 → 1 | 2156.719 / 363.048 |
| E_all_deletes | 4 | 0 / 4 | 1 | 4 → 1 | 4 → 1 | 121.381 / 55.505 |
| G_multiple_days | 1000 | 996 / 4 | 3 | 1000 → 3 | 1000 → 3 | 3541.253 / 433.384 |
| H_collapsed | 11 | 4 / 2 | 1 | 6 → 1 | 6 → 1 | 67.965 / 97.657 |

C（sample+non-sample）和F（同页不同sample type）在既有stream合同中不合法：两基线均unsupported_payload，事实/job均0。本轮不为迎合例子扩展协议；多type使用两条正式stream验证独立scope，多个UTC日使用同一合法stream。H同时覆盖重复identity、冲突upsert与同页delete-wins。

## 14. Documentation Consistency

Active文档指向当前HEAD和本报告/JSON；current发布模式CGO0为248 online packages，原生/race CGO1为249，均连续两次一致。248/249差别来自实际构建模式，不能无条件替换所有249。历史阶段数据与原方案保留。

本报告机器证据：[final-maintenance-fix-2026-10-30.json](evidence/final-maintenance-fix-2026-10-30.json)。不含健康payload、token、DB密码、SSH密码/私钥或个人健康数据。

## 15. Deviations

1. 当前路径和HEAD与提示词不同；接受实际HEAD并核验既有修复，不重复改写或伪造新修复。
2. 没有2026-10-29最终Maintenance报告/JSON可引用；本轮使用实际执行日2026-10-30补证。
3. 代码已修复，不能按未修状态重新实施Wave A/B；先验当前，再对明确历史快照做反例对比，最终执行完整PG与Compose回归。
4. stream不允许的混合载荷按正式合同拒绝；多类型不通过新增协议放入单页。
5. 初轮go mod tidy -diff因默认proxy.golang.org超时失败；随后本机RAI_TEST_GOPROXY未被SSH包装器转发，第二轮仍失败。最终在远端命令显式env GOPROXY=https://goproxy.cn,direct后全部通过；没有修改go.mod/go.sum或关闭校验。两次网络失败保留，不计PASS。首次完整race另有3个失败事件（提交顺序、5年查询子例及其父测试）；原脚本未保留明细，定向race两项均通过，随后不改代码/断言/超时重跑完整race。首次失败原因未确定，不能据重跑通过抹去这项风险。
6. 缺完整Xcode/Testing；仅完成可用Swift构建与补充核心探针，所需iOS/UI门禁继续BLOCKED。

## 16. Remaining Risks

P2-2仍需要完整Swift Testing、两个pending UI用例及iOS Debug/Release严格并发构建；这是本轮不能关闭全部finding的门禁。合成性能、Compose重启和核心probe不证明真实HealthKit/iPhone后台、GPS、网络、GPU、长期生产吞吐或设备强杀。新旧业务源码和schema未额外改变。另保留首次全量race的3个失败事件；定向与最终全量重跑通过，但首次日志缺明细，失败原因未建立，不宣称已证明只是机器负载。

独立清理复核确认本轮PG容器、Compose容器/卷/网络均不存在，原rai-postgres与frpc仍运行。测试密码仅在一次性私有状态中，未进入报告；本轮SSH认证复用临时控制连接，结束时关闭并移除task-owned认证socket/config。

## 17. Deferred Gates

```text
P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
P6 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED
DISASTER RECOVERY RESYNC: BLOCKED / NOT IMPLEMENTED
PRODUCTION RELEASE READY: NO
```

ARCHITECTURE、P3 PROTOCOL CORE、P4 ALGORITHM AUTHORITY、P5 SINGLE PLAN MODEL、P6 PRODUCT ARCHITECTURE保持现状。没有新服务/数据库/队列/客户端、恢复协议或算法。

## 18. Final Verdict

**BLOCKED**。

四项finding有本轮关闭证据；P2-2核心修复已验证但完整客户端门禁未完成，状态PARTIAL。不能声称FINAL MAINTENANCE FINDINGS CLOSED。完成本轮可执行验证、报告与证据后停止；不自动commit、push、deploy、开启新审查、P8或真机验收。
