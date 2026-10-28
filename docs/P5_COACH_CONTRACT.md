# P5 Coach Contract — training_plan/v1

冻结于 2026-10-28。P5 是后端阶段；iOS 默认合同为 Effective Plan，完整 UI、WorkoutKit、Watch、LLM 和部署不在本阶段。P3 REAL DEVICE ACCEPTANCE: DEFERRED / NOT VERIFIED。

## 模型与事务

一个 dataset 一份 active 当前 Plan。创建新目标 Plan 原子归档前一份，保留其所有历史；归档Plan不能再编辑或开始新执行，已开始执行仍可完成。Plan UUID 独立于 immutable config revision、Base UUID/number 和 Effective UUID/number。目标/日程变化追加 config；4–16 周、明确开始日、目标日、目标距离、可选目标时间、每周可训练日、长跑日和自报当前周跑量/最长跑，不能猜测目标。timezone 在创建时从 dataset 冻结，日期为 local date，执行为 timestamptz。开始日必须是周一；目标日在 horizon 内。

Generated session identity 是 plan UUID + 固定 slot ordinal 的确定性 UUID，不含日期/类型；变动日程仍使用相同 slot lineage。Base 和 Effective、参数、override 事件、feedback 全部 append-only。Effective 保存 base ID、override revision、完整课程和结构化建议。开始执行绑定明确 Effective/session 并冻结处方；同 session 最多一个 execution，状态 scheduled → started → completed 或 scheduled/started → skipped。REST 不可执行。执行完成不要求 Workout 已到。

首次生成 pending；生成失败保留原 publication 和明确 error。dataset → job/plan → dirty 行锁，发布前及结束前验证 lease/fencing、generation、config revision。Base + Effective + pointers + clean + job complete + feedback 同事务。用户编辑与发布共享 dataset/plan 锁，发布读取最新 override。没有第二套 manual plan/queue。

## 参数与确定性规则

`training_plan/v1` 和 `coach_parameters/v1` 为纯确定性函数、无随机、墙钟或 I/O。参数 JSON 持久化不可变，代码拒绝未知版本/变更的同名参数。以下是产品的保守调度上限，不声称个人生理效度已经验证。

- 周距离：取最近 28 天已发布 running features 周均值，与明确自报 baseline 取较保守值；没有两者时 insufficient_context。未发布/dirty Workout 不读取。load/HR 缺失不当作 0。
- baseline 仅接受 3–160 km/week、longest ≤ min(50 km, baseline)。首周 ≤ baseline；每周最多 +5%；每第四周为前周 80%；下一周仍最多 +5%（不跳回卸载前高点）。总距离以向下取整的米表示，下一周按实际已分配总量递进；每节自动课≤30km。
- 强度：每周最多 1 个 TEMPO/INTERVAL，RPE≤7；仅 baseline≥20 km 且每周≥3天可训练、且有一个与其他周长跑/强度相隔至少48小时的 slot 时安排。相邻日不得 hard→hard 或 hard↔LONG。其余非训练日 REST，hard/LONG 次日可训练时 RECOVERY。长跑 RPE≤3、EASY≤3、RECOVERY≤2；没有 HRmax/阈值时不猜 HR/pace。
- 长跑上限：min(周量35%, 之前最长跑×1.05, 30km, 目标距离)，不足时不强行补足目标。后续周最长跑最多 +5%。每周≥1个完全休息日，每周可训练日上限6。
- low readiness：仅接受算法版本v1、quality available/partial、非null；readiness<50 或 fatigue>fitness×1.5 时首周距离×0.8并把 hard 改 RECOVERY。load-only 输入仅降低自动建议，不取消用户决定，保存 `readiness_adjustment`/`load_only_advisory`。
- 已完成/跳过 feedback 在其后最近一周触发保守×0.9（under/over/skip），不增加训练补偿。未来仍遵守周上限。过去日期不据无Workout自动标missed。
- 目标可行性：目标距离>baseline按最大5%增长可达的目标日所在周量×0.6，或目标时间有明确速度要求时给 `goal_risk`/`target_time_not_optimized`，保持有界训练，不为达标硬增量。
- interval 是有限 steps：10min warmup，4×(5min work RPE7+2min recovery RPE2)，10min cooldown。duration为48min；距离仍为周预算内的单独目标，不推算配速。无通用递归DSL。

## Override / Effective

每个 override 稳定 UUID、plan/session、operation、path、value、anchor session、单调 revision、created_at。immutable revoke 事件引用原 override；查询计算 active/revoked，updated_at 等于该事件或 revoke 时间。相同 overrideID+相同内容返回原 receipt（即使commandID或expected revision改变）；同commandID不同request或同overrideID不同内容409。expectedOverrideRevision 检查并发写，后台生成不改变 override revision。字段 edit：type、durationS、distanceM、effort、targetHR、paceSPerKM、notes；move修改date；skip/delete保留课程并标 skipped/cancelled；add显式 user_added。null 仅可清除可选 HR/pace。默认仅保护被修改字段，不锁整课。

合并按 revision+UUID 排序，较新 active field胜；revoke恢复下一有效override或Base。新Base缺失被编辑课程时使用override anchor保留，标记 `base_session_removed_kept`。用户新增无第二plan。取消add但仍有后续edit时同样保留anchor并暴露conflict。人工编辑可超出自动安全上限，Effective提供 `user_override_review` 建议而不静默覆盖。

每次Base/override变化创建immutable Effective；历史可根据base+当时revision重放。计划日期必须在固定horizon内。修改type不会自动隐含改写其他字段；REST要求显式清空非零距离/时长，避免隐式丢字段。REST转换用一次add或多字段事务之外暂不提供；edit REST只接受原处方为零的session。

## Published context / provenance

只读 analysis_publications 指向 SUCCEEDED 且scope clean的v1 results；running features按dataset timezone归日，最多最近28天、4096个Workout；state只读as-of当天且其计算timezone必须匹配Plan冻结timezone；dataset改时区后不兼容state降级为unavailable。记录publication run、generation、metric算法/version/quality、nullable值及context hash。未知算法/quality不用于决策，不能把unavailable当0。参数版本与config revision记录在run/base中。训练history由execution和feedback的结构化摘要提供，不复制raw。

## Recalculation / queue

create、goal/schedule更新、显式recalculate、completed/skipped/late association、已发布Workout/state变化触发plan dirty。`training.plan`、`plan:<UUID>`复用jobs/dirty_scopes/lease/fencing；一个queued job吸收新generation，run_after延迟1秒；running不修改token，保留一个后继queued job。P4每个bundle只标脏一次，非每个metric。旧代际只能SUPERSEDED，无权更新publication/clean。lease恢复同时终止RUNNING generation run。

## Execution / Feedback / missed

明确effectiveVersionID+sessionID开始，后续Base/override绝不改变snapshot。complete保存completed_at和可选主观notes；可暂无线下Workout。associate需用户明确Workout UUID且dataset内事实已存在，不自动时间匹配，不复制raw；迟到可关联。UUID保留以支持删除后的审计，但已删除/dirty来源的feedback不用于当前决策。

feedback/v1：基于snapshot与已发布duration/distance/workout_load，保留planned/actual/delta/ratio、sourceRunID、quality、execution revision；duration或distance至少一个可比才给under/on/over（±10%），两者冲突标mixed，缺值completed/analysis_pending；skip为skipped。每次来源或execution revision变化追加，不覆盖旧feedback。Workout新publication触发同队列刷新feedback和Base。无Workout不自动missed；只有用户显式skip，后到Workout可关联到completed执行。API拒绝从skipped直接completed，用户可用新session执行；不删除历史。

## API / 历史

认证/限流沿用v2。显式DTO独立于dbgen。POST plans、POST plan/{id}/changes、POST overrides、GET current/id/effective、GET effective/{version}、GET history、POST executions、POST execution/{id}/changes、GET execution/{id}。所有命令commandID幂等，目标/执行修改带expected revision。history每页最多50个Effective，after为单调version number，按升序；单版本audit含base/context/overrides，执行查询含feedback历史。错误422、409、404均稳定code，不记录健康payload。

第一版仅用目标日约束有界日历和可行性判断，不提供比赛日配速、赛前taper或目标时间优化；用户需明确确认实际比赛安排。

结构化interval的duration被用户单独改写且与生成结构不同时，Effective转为无steps的interval时长/RPE处方，记录 `duration_override_unstructured`，不覆盖用户时长或携带矛盾的steps；修改成非interval类型也清除不适用的生成steps。
