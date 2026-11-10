# RAI V2 Phase 5 Result

## Status

PASS WITH RISKS

本轮只交付P5后端。验收代码包含原有未提交P4；最终证据见 [fnOS gates](evidence/p5-fnos-2026-10-28.json) 和 [performance baseline](evidence/p5-baseline-2026-10-28.json)。原方案与P3/P4历史结果不改写。

## Repository

`/Users/ta/rai`，`main`。在既有P4 dirty worktree上增量实施；未创建commit、push、生产部署或真实数据迁移，未修改 `../running`。

## Base Commit

`P5_BASE_COMMIT=8e40573e027ac6dd9aac871fb755051cb31c4657`。该HEAD不包含开始时未提交的P4，因此不能把相对HEAD的所有差异视为P5新增。

## P3 Deferred Gate

```text
P3 REAL DEVICE ACCEPTANCE:
DEFERRED / NOT VERIFIED
```

P3.5 client implementation与P4 PASS WITH RISKS保留，不由服务端或合成测试升级成真机PASS。

## Architecture

```
Published clean Analysis → TrainingContext → training_plan/v1
→ immutable Generated Base + Persistent User Overrides
→ immutable Effective → Execution Snapshot
→ explicit Workout association → versioned Feedback
→ same PostgreSQL queue / Recalculation → Overrides still preserved
```

纯领域在 `internal/training`，I/O与事务在storage，HTTP使用独立DTO。P5不读取未发布analysis run或raw健康测量作为处方输入；raw只用于running身份、时间和显式关联存在性校验。无LLM、第二套Manual Plan或第二队列。完整模型与数值规则见 [Coach Contract](P5_COACH_CONTRACT.md)。

## Schema

新增不可逆 `00013_training.sql`；00001–00012内容与P5入口时完全一致，00012仍为 `4cd795926d1b9ba086fb877a7b1cc46ae84c6f38c2e1dca86fc212ac8bea1a5f`。readiness要求schema恰为13，schema12或未知新版本fail closed。11张新表，总计44张应用表（另有goose版本表）：

| 表 | 主要约束/用途 |
|---|---|
| training_parameter_sets | immutable version + 参数JSON |
| training_plans | dataset/plan identity；每dataset只有一份active；current Base/Effective复合且deferred FK |
| training_configs | immutable plan/config revision |
| training_sessions | stable UUID + generated slot / user_added；复合FK、slot唯一 |
| training_generation_runs | context/hash/config/generator/parameters/job/fencing；只能RUNNING→terminal |
| training_base_versions | immutable plan/number/UUID/run lineage |
| training_overrides | immutable UUID/revision/operation/path/value/anchor；content hash与首个command FK |
| training_effective_versions | immutable UUID/number/base/override revision；组合唯一，可重放 |
| training_executions | effective/session FK；INSERT检查处方来源，snapshot与终态时间不可修改 |
| training_feedback | immutable derived snapshot，execution revision/source run唯一（含NULL） |
| training_commands | command hash + 原receipt，与业务同事务 |

plan/version复合主键与unique index支持Effective/history/override查询；active Plan partial unique；generation run、execution history、feedback history另有有界查询索引。未分区、未删除历史。

## Plan Model

Plan identity独立于config/Base/Effective revision。相同目标重算追加Base；新目标Plan可原子归档旧Plan，旧execution/history继续可查。4–16周固定horizon，每天一个稳定generated slot；日期或类型不是identity。generated/user_added属于同一Plan。

Base与Effective均不可UPDATE；override是事件历史，不直接改Base或整个最终JSON。Effective包含用户修改标记、reasonCodes和conflicts。历史audit返回当时Base/context/override revision，可确定性重放。

## Generator

- 名称/版本：`training_plan/v1`；参数：`coach_parameters/v1`；feedback：`training_feedback/v1`。
- 纯函数、无随机、无墙钟、无DB；输入的as-of、timezone、配置与publication lineage显式提供。
- 主要可靠输入为最近28天clean已发布running features及用户明确baseline；fitness/fatigue/readiness/recovery保存nullable value、quality与算法版本。未知版本或unavailable不作为0。
- 透明规则：周距离预算、长跑位置、hard spacing、卸载周、恢复日、load-only advisory和反馈调整。每节课保存reason code。
- 没有HRmax/threshold时不猜HR或配速；默认距离+RPE，interval有有限warmup/repeat/recovery/cooldown模型。
- 同context、同版本输出相同；8/16周golden、merge golden、极端可训练日、DST与合成历史测试通过。

## Safety Constraints

自动计划第一周≤保守baseline；每周按实际取整后的距离最多增长5%，每第四周前周80%，之后仍受5%上限。每周至少一天REST，最多一节TEMPO/INTERVAL；hard与LONG至少间隔48小时；自动每节≤30km。长跑≤周总量35%、上一最长跑×1.05、30km和目标距离。

readiness<50或fatigue>fitness×1.5仅使首周自动预算×0.8、hard改Recovery，保留load-only质量提示。近期under/over/explicit skip反馈使相关未来周预算×0.9，不补偿性突增。用户override保留并给 `user_override_review`。以上是版本化产品规则，未声称个体生理效度。

## Override

edit支持type/duration/distance/effort/HR/pace/notes，move保留同session UUID，add写入同Plan，skip/delete保存标记，revoke追加事件并恢复下一有效overlay或Base。较新active revision胜；expectedOverrideRevision阻止两个客户端静默覆盖。

相同overrideID、相同内容，即使新commandID或expected revision改变，仍返回原Effective receipt；同identity不同内容409。相同commandID也有独立请求hash校验。revoke不会抹去旧Effective或override历史。

**survive regeneration**：规范用例逐值验证Monday 5→用户7→新Base4仍为7；Sunday用户改Saturday，Base12→10后Effective为Saturday10；Wednesday Interval→Recovery接受新Base。纯合并测试使用规范数值；真实PostgreSQL/实际P4 publication E2E使用规则生成器的实际距离，验证完全相同的字段保护语义，非硬编码这组例子为生成器唯一输出。

## Effective Plan

每次Base或override变化原子物化新Effective，记录base UUID和override revision。生成器发布时读取锁内最新override，未编辑字段继续随Base更新。新Base移去有编辑的slot时保留anchor并标明 `base_session_removed_kept`。anchor深拷贝，后续HR/pace修改不能污染历史事件。API默认返回Effective；backend-only阶段未增P6 iOS UI。

## Execution

scheduled/started/completed/skipped；开始需current Effective UUID和session UUID，冻结完整处方。DB检查snapshot确实属于该Effective并保护不可变字段。Base/override/归档后均不改变started execution。

complete可先于Workout；之后显式手动关联dataset内已存在Workout UUID，无自动时间唯一匹配。还支持Workout事实已到但analysis未发布：先记录pending反馈，正式publication后追加反馈。

## Feedback

保存planned/actual duration与distance、delta、ratio、actual load、来源run、算法与execution revision。±10%为on_target，under/over保留数值差异；两指标矛盾为mixed；缺失为completed+analysis_pending，绝不编造0。

明确skip产生skipped；没有Workout不自动推断missed。删除或dirty的Workout publication不进入未来决策。历史feedback保留；新source或execution revision追加，重复同来源不会增殖。

## Recalculation

create、goal/schedule更新、显式请求、完成/跳过/迟到关联，以及Workout/state bundle正式发布标脏 `plan:<UUID>`，使用 `training.plan`。同Plan一个queued successor吸收generation更新，1秒debounce，不按metric或sample整份重算。lease恢复遗留旧queued generation由publisher结束；不删除旧job identity。

## Concurrency

实际PostgreSQL验证：两个客户端同revision只有一个成功；计算快照后用户编辑，再publish得到新Base+最新override；新generation先发布后旧generation只能SUPERSEDED；过期/旧fencing不能发布；事务中途lease过期完整回滚。dataset锁序与plan publication确保current pointers、clean和job状态一致。

## Fault Injection

| 故障点 | 结果 |
|---|---|
| Base INSERT | 整笔回滚，旧current继续可用 |
| current Plan/publish UPDATE | 无半Base或半Effective |
| Effective INSERT | 无部分publication |
| dirty clean / job / job_attempt completion | Base、Effective、run和job一起回滚 |
| override INSERT / Effective INSERT | 无半override或revision推进 |
| feedback INSERT | execution完成、feedback和dirty一起回滚 |
| lease在publish中途到期 | 不能以transaction固定now冒充有效lease |

各故障解除后使用原命令或原run重试成功；不可变表UPDATE/DELETE、跨dataset查询与受限API/Worker角色边界有真实数据库验证。

## Performance

最终单独运行于fnOS/N100、Go native、GOMAXPROCS=2、隔离PostgreSQL18.6、non-race；本任务没有其他测试并行，但未隔离NAS其他负载。单位ms：

| 操作 | 52周fixture | 3年fixture |
|---|---:|---:|
| Generate 8-week（100次 median/p95） | 0.072 / 0.126 | 0.080 / 0.162 |
| Generate 16-week（100次 median/p95） | 0.125 / 0.170 | 0.152 / 0.456 |
| Merge 100 sessions + 100 overrides | 0.553 | 0.849 |
| Complete/feedback/recalculate | 83.884 | 83.986 |
| Effective query | 0.947 | 1.259 |
| History first 50 versions | 22.658 | 24.099 |
| Audit 100 overrides | 5.028 | 5.093 |

除纯Generate为100次固定输入median/p95，其他均为单次observation。query为storage+Go decode，不含iPhone/HTTP/UI；recalculate使用真实handler但测试提前run_after，不包含正式1秒debounce和poll等待。首轮并发测试观测仅用于开发，不作为此表基线。

52周容量fixture含364个合成已发布Workout、100个override、154个Effective；3年fixture含1095 Workout、100 override、258 Effective；计划最大16周/112个generated slots。版本计数包含override版本与feedback后的新版本。历史是合成容量fixture及周频generation数量，不是3年真实持续运行或多目标soak。

## Validation

| fnOS门禁 | 最终证据 |
|---|---|
| gofmt / go mod verify / go test ./... / go test -race ./... / go vet ./... / go build ./... | PASS；最终源码质量门禁再次通过 |
| 完整真实PG `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` | PASS，integration 386.281秒，含P3/P4回归与P5 |
| 最后calendar只读过滤补丁后的全部P5真实PG race | PASS，90.441秒；同轮重跑普通race、vet、build、依赖边界 |
| `go vet -tags=integration,scale ./...` | PASS |
| schema12→13 / fresh→13 / repeat migrate / readiness | PASS，真实隔离PG |
| Compose P3 restart/worker-offline + P4 actual worker/query + P5 HTTP closed loop | PASS，同镜像，SIGTERM正常 |
| sqlc 1.31.1 generate + repeat hash comparison | PASS |
| 单独non-race容量/性能 | PASS，24.336秒 |

完整全仓库及Compose在最后timezone兼容过滤前通过；该只读predicate之后对最终源码再次运行全部P5真实PG与Go质量门禁。验证序列与源码哈希保存在evidence，不把早期失败轮或旧源码结果冒充最终补丁的专项验收。

OpenAPI lint通过，保留8条既有warning：4个健康接口没有4XX、4个未使用旧schema。SQLc 1.31.1已生成新模型并校验；在线图248包排除admin/migrations/goose，analysis/training核心均无I/O。原生iPhone、生产部署、真实健康数据与长期运行未验收。

## Deviations

- 按本阶段冻结backend-only API，不增iOS Training UI/Client或WorkoutKit；P6产品集成未开始。
- horizon固定4–16周且周一开始；同Plan修改goal/schedule，改变horizon或开始日创建新Plan并归档旧Plan。
- 使用distance预算与RPE；未实现比赛日配速、赛前taper、目标时间优化、完整生理Recovery或planned TRIMP。不能以target time已保存宣称已做达标优化。
- 不自动判missed；显式skip与late association已有确定行为。REST转换建议skip/add；单字段修改不隐含清空其他处方字段。interval duration单独修改与结构冲突时，转为明确标记的无steps时长/RPE处方。
- storage仍为sqlc与手写pgx混合，没有宣称全部SQL由sqlc生成。

## Risks

- P3真实iPhone/HealthKit/后台恢复仍未验证；本轮合成服务端验收不能证明它们正确。
- 训练与P4 readiness/recovery均为透明启发式，个体训练效果、完整恢复与生理校准未验证；用户override可能超出自动上限，系统仅提示并保留决定。
- 版本、commands receipt、anchors和feedback暂不retention；容量观测不证明无限增长可持续，长期策略留P7。
- 性能为小样本/单次隔离fixture观测，有共享fnOS环境影响，非生产SLO。TLS、生产角色/secret、备份恢复、断电和真实数据迁移未验收。

## P5 Final Status

```text
P5 STATUS: PASS WITH RISKS
```

## P3 Deferred Status

```text
P3 REAL DEVICE ACCEPTANCE:
DEFERRED / NOT VERIFIED
```

## P6 Entry Gate

```text
P6 DEVELOPMENT READY: YES
```

仅表示P5后端Base、override、Effective、execution、feedback、recalculation与override survival通过自动验收；不授予P6实施或生产部署。按STOP RULE停止于P5。

## Acceptance Gate

以下为本轮自动验收范围。iOS/API默认Effective项仅指P5 backend API合同；真机与P6产品集成不在该勾选内。性能是上述限定样本，Compose不是生产部署。

- [x] 当前仓库 /Users/ta/rai
- [x] 记录 P5_BASE_COMMIT
- [x] P3 真机延期状态保留
- [x] P4 Analysis Engine 未被破坏
- [x] training domain 已建立
- [x] Plan identity 已建立
- [x] immutable Base Plan Version 已建立
- [x] stable Session identity 已建立
- [x] User Override 已建立
- [x] field-level edit 支持
- [x] move 支持
- [x] skip 支持
- [x] user-added session 支持
- [x] revoke 支持
- [x] 自动重算不会覆盖 User Override
- [x] override survive Base regeneration
- [x] user edit vs generator race 通过
- [x] Effective Plan 已建立
- [x] Effective Version immutable
- [x] Effective Version 可重现
- [x] iOS/API 默认使用 Effective Plan
- [x] Generated Base 与 Effective 明确分离
- [x] 不存在第二套 Manual Plan
- [x] Plan Generator versioned
- [x] Training Context 只消费 Published Analysis
- [x] generator deterministic
- [x] generator golden tests 通过
- [x] Cold Start 正确
- [x] Missing analysis 正确
- [x] P4 unavailable metric 不被当作 0
- [x] weekly load constraint 完成
- [x] hard-day spacing 完成
- [x] recovery rule 完成
- [x] long-run rule 完成
- [x] safety parameters versioned
- [x] Coach reason code 完成
- [x] 没有 LLM 作为 correctness authority
- [x] training generation job 复用现有 queue
- [x] fencing 正确
- [x] stale generation 不能覆盖 current plan
- [x] job storm 聚合完成
- [x] Base publication 原子
- [x] Effective publication 正确
- [x] fault injection 通过
- [x] Execution Snapshot 完成
- [x] Execution 绑定 Effective Version
- [x] started Execution 不受后续重算影响
- [x] Workout late association 支持
- [x] Feedback 模型完成
- [x] planned vs actual 完成
- [x] missed/skip 行为明确
- [x] Feedback 可触发新 generation
- [x] Recalculation 后 Override 仍保留
- [x] Training API 完成
- [x] OpenAPI 更新
- [x] DTO 与 DB schema 分离
- [x] migration fresh DB 通过
- [x] schema 12 → P5 schema 通过
- [x] readiness 正确
- [x] unit tests 通过
- [x] golden tests 通过
- [x] race tests 通过
- [x] concurrency tests 通过
- [x] PostgreSQL integration 通过
- [x] fnOS gofmt 通过
- [x] fnOS go vet ./... 通过
- [x] fnOS go test ./... 通过
- [x] fnOS go test -race ./... 通过
- [x] fnOS go build ./... 通过
- [x] Compose regression 通过
- [x] 性能 baseline 完成
- [x] 未引入 Redis
- [x] 未引入外部消息队列
- [x] 未引入第二数据库
- [x] 未接入 LLM
- [x] 未实现独立 Manual Plan
- [x] 未开始完整 P6 UI
- [x] 未开始 Watch App
- [x] 未开始正式 WorkoutKit 排程
