# RAI 运动能力模型补全——实施决策（E0）

- 文档状态：E0 实施决策；工程状态与最终证据见 `RAI_MODEL_COMPLETION_RESULT.md`。
- 实际仓库：`/Users/tp/rai`，文档基线 `main / 18eab55` 加工作区改动；未强制 checkout、未 reset/clean/stash、未修改 `../running`。
- 数据库基线：migration `00029_workout_completeness_v2.sql`；现有工作区追加至 `00044`。00039–00042 依次收紧 active 产物权限、接入受限输入事务、注册日栈与持久化回算任务；00043 注册 5 个研究态 artifact key，00044 修正回算进度只统计 6 个产品 family。旧迁移和校验和保留。
- 平台：本机 macOS、Go 1.27.3、PostgreSQL 18.6、Swift 6.3.3；仅合成 fixture，不读取个人库或生产 DSN，不使用 SSH/远端库。
- 既有用户改动保留：`docs/IMPLEMENTATION.md`、App 图标、核查/计划文档；本轮向 `IMPLEMENTATION.md` 追加真实证据，当前签核以实施结果与矩阵为准。

## D01–D06 冻结决策

| 决策 | 实际选择 |
| --- | --- |
| D01 版本冻结 | v1 身份、算法、schema、config hash、缺失/单位/窗口语义一律不改；新语义写入新版本/manifest，v1 golden 与历史 artifact 保留。 |
| D02 Readiness 数学 | 保留 180 日 lookback、36h 指数负荷核、前 42 个完整当地日滚动 baseline、≥5 训练日、≥14 日跨度、1 AU floor；不退回 14 日/周一冻结。Recovery 缺失、未知或 partial 时完整分数为 null，不归零；无 Workout 的日界仍刷新候选但不虚增支持。 |
| D03 HR 支持域 | `30 <= HR < 220 bpm`；HRmax 必须有唯一、非冲突、未撤销且 as-of 可见的显式测量；HRmax <= RHR 拒绝；不以年龄公式推断 HRmax；不为出值放宽。 |
| D04 Recovery v2 | 夜间 SDNN 仅取 00–06 civil 与主睡眠/明确静息上下文交集，同 source/algorithm/acquisition epoch，≥2 个 SDNN，先 ln 后 median；28 日稳健 baseline、RHR 替换、sleep union 保留；新增圆周中点规律性、typed 睡眠目标、连续 3 有效日持续偏离，均为 v2。 |
| D05 confidence | 各模型独立 policy，只表达证据支持度，不是概率；未知天气 cap 0.5（VO₂）、LOO 不足 cap 0.4、maximal 未外部校准 cap 0.7、Readiness cap 0.55。 |
| D06 来源 | 只消费已记录、符合 source policy 的跑步；外部 VO₂ 与 RAI VO₂ 独立，不互为标签/约束/fallback；来源 epoch 变化断开趋势。 |

## 最终版本 manifest

`internal/modelmanifest.Current()` 定义当前合成发布栈的合同。canonical SHA-256 如下。

```
b4618722716ba5695161b967436d6c1ff69b14f6b40aae7bf73ad1c6d109e954
```

| 组件 | 身份 | 说明 |
| --- | --- | --- |
| 特征 | `rai.workout_features/1` | Worker 在合成栈的 Workout scope 发布既有 v1 Feature 合同。计划中的独立 v2 Feature 仍待实现。 |
| 单次负荷 | `rai.training_load.hrr_integral/v2` | 显式参考与重叠状态参与剂量计算，当前作为日栈计算输入。 |
| 负荷状态 | `rai.load_state.exp_finite/v2` | 有限核/Tau 不变；v2 身份与 scope |
| 跑力 | `rai.running_ability/v2`（curve/endurance v2） | demonstrated 评分尺度冻结；`maximal_supported` 已作为独立研究 artifact `running_ability_maximal_v2` 发布，未校准 cap 0.7。 |
| RAI VO₂ | `rai.vo2max.submax/v1` | 新条件估计器；`Experimental=true` |
| 外部 VO₂ | `rai.vo2max.external_projection/v1` | Apple/外部来源独立，不进入 RAI 估计 |
| Recovery | `rai.recovery.physiological/v2` | 夜间上下文、规律性、目标、持续偏离 |
| Readiness | `rai.readiness.training/v2` | 只消费 allowlist Load v2/Recovery v2；D02 数学 |
| 诊断 | `rai.workout_durability/v1`、`rai.aerobic_efficiency/v1`、`rai.critical_speed/v1`、`rai.load_settling/v1` | 独立研究态 artifact，family `model_research_v1/<key>`，不占 6 个产品槽位、不阻塞主模型 |
| 产品 | `rai.athlete_model/2`、`rai.product_metric_mapping/2` | 六 typed metric；保留 schema1 解码 |

manifest 固定 component/config/schema/input/source/annotation/calendar/consumer 版本；hash 进入 scope、assignment、run lineage。正式产品只读取 `active` 的 clean 产物。`candidate` 供受控验收，`previous` 供回滚。

## DAG 与输入 registry

下图记录原设计依赖。当前发布使用 Feature v1；日常 RHR 回退已接日栈（同 source epoch 的日常 RHR median），四研究诊断与 maximal 已发布为独立研究 artifact。

```text
Workout raw -> features v2 -> load v2 -> load_state v2 -> readiness v2
Workout raw -> features v2 -> running_ability v2
Workout raw -> features v2 -> vo2max.submax v1
Daily raw -> recovery v2 ------------------------------> readiness v2
hr_reference/2 + daily RHR -> reference v2 -> load/vo2
features v2 -> diagnostics (durability/aerobic/CS/settling)
external VO2 raw -> vo2max.external v1 (独立分支)
```

typed input 路由：`POST /v2/model-inputs/hr-references|sleep-targets|workout-efforts|workout-overlap-resolutions|source-context-selections`（亦接受 canonical kind）；registry：`hr_reference/2`、`sleep_target/1`、`workout_effort/1`、`workout_overlap_resolution/1`、`source_context_selection/1`；撤销为同 subject、指向目标 schema 的 append-only `kind=revocation` 事件，不能 UPDATE/DELETE；`POST /v2/model-inputs/{id}/revocations` 已实现并测试，Swift 表单仍列为 PARTIAL。每类输入在 `athlete_model_inputs` 一张事实表，receipt 在 `model_input_receipts`，dirty/job/receipt/audit 同事务。

## 权限模型

- API/Worker 同镜像；`httpapi`/`jobs`/`analysis` 不得导入 `internal/admin`。API 角色不能写 candidate、任意 artifact 或 assignment；Worker 具备有限分析事实、类型化输入和发布权限，激活由显式管理员入口完成。在线图不导入 admin。
- Worker 在 dataset 行锁内发布同代际产物。管理员在同一锁下重验候选，再提交 active assignment revision 和 append-only audit。
- product reader 只读 active + clean generation，不 Calculate、不读 raw。

## 影响与兼容

- v1 输出、artifact、历史、golden 不改；v2 新 scope/版本并存。
- 外部 VO₂ 独立展示与留存；无真实数据时绝不把 Apple 值或同一输入当 RAI 标签/生理真值。
- 新迁移只追加；旧 migration 原文与校验和保留。真实设备、真实研究、生产启用不在本轮自动授权内。

## G01–G14 追踪

| G | 状态 | 实现/证据入口 |
| --- | --- | --- |
| G01 自有 VO₂ | 合成日栈已接独立槽位，正式数值 oracle 待验收 | `internal/vo2max`；E3/MC-V* |
| G02 正式 API | 正式受限 API 已有隔离 PG 测试 | `internal/httpapi/athlete.go`、`storage/postgres/athlete_product_v2.go`；E7/MC-P* |
| G03 iOS UI | schema2 卡片、历史与详情已接线，测试状态见 RESULT | `ios/RAI` ProductViews/AthleteModel；E8/MC-P06–P09 |
| G04 promotion/rollback | synthetic PG 通过 | `promotion2.go`、`model_promotion.go`、migration 30/31/33/35；E9/MC-X* |
| G05 统一回算 | 计划、存储、CLI 和真实 Worker 日任务已接线 | `internal/backfill`、`storage/postgres/backfill.go`、`admin/backfill.go`；E6/MC-H* |
| G06 detail 已发布 feature | 已接：详情读 publication+SUCCEEDED run+security view+clean generation；dirty null | `workout_detail.go`、migration 38；E7/MC-P05 |
| G07 训练消费者 | plan ModelBinding 落库，active schema2 消费兼容的 v2 Readiness 与 Load，低值仅提示 | `training/model.go`、`training_store.go`、`training_input.go`；E8/MC-P10–P11 |
| G08 历史/趋势 | schema2 history 路由/reader 完成；正式集成不足 | `/v2/analysis/athlete/history`；E7/MC-P04 |
| G09 HR reference/RHR baseline | 日栈消费显式参考；缺失时使用训练日前 28 日同 epoch 日常 RHR median（≥14 日、前 7 日≥3 日） | `internal/hrref`、`storage/postgres/model_stack_inputs.go:dailyRHRFacts`；E2/MC-I* |
| G10 夜间 HRV | 工程实现 | `recovery/v2.go`；E4/MC-R01 |
| G11 睡眠规律性/目标/持续偏离 | 工程实现 | `recovery/v2.go`；E4/MC-R06–R10 |
| G12 长跑诊断 | 纯域完成，已由日栈 worker 发布 4 个研究 artifact；产品详情尚未展示 | `internal/diagnostics`、`storage/postgres/model_stack_research.go`；E5/MC-A06–A07/A12 |
| G13 跨 UUID 重叠 | 工程实现 | `hrref/overlap.go`；E2/MC-I06–I09 |
| G14 source epoch/误标 | 工程实现 | `hrref/source.go`；E2/MC-I10–I11 |

状态含义：工程实现不等于真实设备、真实研究或生理校准通过；D/C 真实项在 RESULT 中单独保留 NOT RUN/NOT VERIFIED。最终逐项证据与失败历史以 `RAI_MODEL_COMPLETION_RESULT.md` 和 `docs/evidence/model-completion/acceptance.json` 为准。
