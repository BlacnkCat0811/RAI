# Athlete Model Product Contract — M7

版本：`rai.athlete_model/1`；状态/原因映射：`rai.product_metric_mapping/1`。本合同只投影既有模型，不增加公式、合成总分或模型计算依赖。当前全部 candidate-only，正式 active=legacy。**Model engineering complete ≠ Product activated**。

## 独立产品语义

| Metric | Model identity | 冻结含义 | Typed value / 单位 |
| --- | --- | --- | --- |
| Training Load | `rai.training_load.hrr_integral/v1`；state `rai.load_state.exp_finite/v1` | 已记录跑步训练刺激 | latest workout `RAI_AU`；acute/chronic/balance `RAI_AU/day`；M2 trend7 `RAI_AU/day²`；knownness |
| Running Ability | `rai.running_ability/v1` | 近期实际表现支持的持续跑能力 | score；v60 `m/s`；gamma；enduranceRetention；evidenceMode/count；stability |
| External VO₂max | `rai.vo2max.external_projection/v1` | 外部 Apple Health VO₂max estimate | value `mL/kg/min`；provider/origin；sampleDate；externalEstimate=true |
| Physiological Recovery | `rai.recovery.physiological/v1` | HRV SDNN / RHR / Sleep 相对个人基线的恢复压力 proxy | score 0–100；baselineState；三个 component 的 availability/relativeToBaseline |
| Training Readiness | `rai.readiness.training/v1` | Recovery × recent recorded running-load context | score 0–100；recoveryScore；loadMultiplier；recentRecordedLoad `RAI_AU`；running_focused |

不声称乳酸阈值、医学诊断、伤病/疾病/过度训练识别、whole-body completeness 或强制训练指令。External VO₂max 标注 **Apple Health estimate**；不称 RAI measured，也不直接称 Apple Watch measured。仅在真实 provenance 支持时才能显示 Apple Watch-derived；本轮默认来源标签采用 Apple Health / RAI-derived，不推断具体设备。

## Snapshot / 时间 / 一致性

`AthleteModelSnapshot` 包含 schemaVersion、asOf、evaluationAt、datasetRevision、timezone、calendarDate、assignment 与五个独立 typed metric。展示顺序 Readiness→Recovery→Load→Ability→VO₂，不表达计算依赖。

所有时间戳为 **UTC epoch microseconds**。根 asOf 是这次 dataset-view 的显式 evaluation anchor，不表示五个模型同时生成。每项 envelope 保留其模型 asOf、publication updatedAt、evaluationAt 和 freshness；Training Load 额外保留 latestWorkoutAsOf。M2 artifact 没有模型级 asOf，使用该 run 的 started_at 表达状态计算捕获时点，不能改称训练发生时点。

同一 reader 的 assignment、dataset revision、calendar、current pointers、run、artifact 与必要引用均来自一个 `REPEATABLE READ READ ONLY` 事务。统一 evaluationAt 必须传入；领域模型不能读取系统时钟。计时器仅用于性能测量，不影响 DTO。没有缓存或第二 summary source of truth。

Reader 只 read / validate / Freshen / map。每次最多读取六个 current slots（M2 workout/state 独立），不扫描 analysis history、不重新计算基线/拟合/公式。引用验证批量读取 identity/hash；daily raw 表只读取被明确引用的主键/hash，不读取任何 raw health value。完整 lineage 留在受限 artifact，产品 DTO 只留来源摘要。最多 20,000 个 validation refs，来源上限继承各模型；超过上限为 technical error。

## State / missingness

| 原始状态 | ProductMetricState | 原因语义 |
| --- | --- | --- |
| available / complete | available | 数值由既有模型提供 |
| partial / limited / unstable | limited | 保留原 quality、reason；不冒充 complete |
| collecting / collecting_baseline / insufficient_long_evidence | collecting | 历史或个人基线仍在收集 |
| insufficient_data | unavailable | 证据不足，不补值 |
| sync_incomplete / pending | unavailable | 同步、current publication 或依赖尚未齐备 |
| source_conflict / source_selection_required | unavailable | 来源冲突或需要显式选择 |
| deleted / no_sample | unavailable | 保留删除/已观察无样本的区别 |
| stale / expired，或相应 model freshness | stale | 保留原始过期种类；禁止 stale→fresh |
| unsupported model version | limited | value 不供展示，保留版本与 payload schema |
| 未识别状态 | unavailable | 保存原 status/reason，客户端 generic fallback |
| hash/run/schema/generation integrity failure | unavailable + technicalError | partial_response，不能伪装业务缺失 |

真正 DB/事务失败返回 technical error。Candidate preview 中单项 artifact corruption 仅撤下受损 metric 及已验证依赖它的 Readiness，其他独立 metric 可以返回。不能取旧值或回退 candidate。正式 active service 只接受 legacy assignment，未激活的五种新语义返回 `not_product_activated`，不把 legacy readiness/fitness 数字改名为新模型。

正常业务缺失不改变 responseState=complete_response。partial_response 只表达技术返回情况，绝不是整体健康状态。无 overallStatus、Athlete/Fitness/Health/RAI 总分。

新用户 value=null，时间/置信缺失也保留 null。未进行 VO₂ projection 时返回 sync_incomplete，不能宣称已经查询并确认无样本。已 enrolled 模型缺 current publication 表示 sync_incomplete。空 score 不补 0、Recovery 不补 100、unknown load 不补 0。

原因码同时提供原 reasonCodes 和 ProductReasonCodes；V1 按字典序去重排序，顺序与 DB 行序无关。映射版本独立于模型版本。服务端不保存/返回固定中文或英文最终句子。

## Quality / confidence / freshness

quality 表示模型输入合同完整性；confidence 表示 evidence support。两者独立，不能合并。M3 无独立输入 quality 字段，产品 envelope 使用 `not_provided`，另保留 stability。M4 confidence=null。V1 不生成百分比文案，只提供 limited_evidence（<0.2）、moderate_evidence（≥0.2）、stronger_evidence（≥0.4）标签，非校准准确概率；没有 evidence 时不展示。

fresh/aging/stale/expired 来自模型既有 Freshen 结果，不新增年龄阈值。unknown 表示无已知 freshness，绝不解释为 fresh。M3 14/28/42 日和 M4 30/90 日沿用模型；M5B 使用其 component freshness；M6 使用其 evidence/coverage/day boundary。M2 无按年龄 Freshen：只读取 evaluation calendarDate 对应已发布 state，历史 workout 事实不推断已过期；缺 current-day state 时 freshness=unknown、load knownness=unknown，保留可用的 latest workout fact。

## Trend / explainability

- M2 直接传递原 trend7，不在 UI 重算。
- M3 趋势必须同 model/version、config、evidenceMode、view 且 valid/fresh；当前 reader 不加载 history，返回 null 与 no_comparable_result。纯 eligibility 函数可供未来有界历史服务复用，不代表已经交付该 endpoint。
- M5B 仅 complete+available、同 model/config/timezone 的有效不同 daily result 可比较；当前不加载历史，趋势 null。
- M6 为 instant state，V1 无长期趋势。
- M4 只允许 external source value history；本轮 current snapshot 不传历史或 improvement 数字。

`BuildMetricExplanation(envelope, typed facts, limitation codes)` 为纯函数，仅消费已有投影结果。headline / facts / limitations 均为 localization keys。Load 解释 recorded running；Ability 解释 v60/evidence；VO₂解释 Apple Health estimate；Recovery 解释三个 component 相对基线的方向；Readiness 解释 Recovery、recent recorded running load、multiplier。没有新 raw query、替代公式或诊断文字。简洁 notices 支持 informational_fitness_metrics / not_medical_diagnosis。

## Swift / compatibility

Swift 五个 metric 使用 typed generic envelope 和独立 value struct；单位有 `ProductUnit`，所有 server vocabulary 为保留原字符串的 Codable 类型。Unknown enum/reason/quality 不导致 decode failure；known metric + unknown model 显示 limited/unsupported，typed value 不用于展示。未来 value shape 改变可在 unsupportedPayload 中保留 typed JSON sum，禁止 `[String: Any]` 产品 DTO。

schemaVersion 与 modelVersion/payloadVersion/mappingVersion 分离。新旧客户端不会发起新增 HTTP 调用，因为本轮没有正式 endpoint、导航或 capability 激活。未来正式 endpoint 上线前须单独增加 handshake capability，旧 server 无 capability 时不请求并保留 legacy；未知 root schema 不展示。新增算法语义必须 /v2，v1 history 不覆盖；需要未来 reviewed additive schema/handler/scopes 支持，不宣称目前 v2 已实现。

**UI NOT IN SCOPE**：本轮没有新 view 或导航，不宣称完成新增 UI 的 Dynamic Type / VoiceOver / missing UX 验收。DTO 提供文本状态/解释，无颜色依赖；真正 UI 本地化、可访问性和产品 activation 仍需独立验收。

## Candidate isolation / API

`ReadAthleteModelSnapshot(principal, evaluationAt)` 仅 active。`ReadAthleteModelCandidateSnapshot(dataset, evaluationAt)` 为不同 transport，强制数据库 owner/admin 权限；受限 rai_api / rai_worker 不能调用。

CLI：

```text
rai analysis model-stack inspect --dataset <explicit> --evaluation-at <RFC3339>
rai analysis model-stack inspect --dataset <synthetic> --evaluation-at <RFC3339> --synthetic-test-values
rai analysis model-stack promotion-plan
```

默认 inspect 只有 metric identity、state、quality、freshness、reason、technical error 和 DB read count；不输出 score、confidence、raw、完整 UUID 或内部参数。测试数值需要显式 synthetic-test-values。不得对真实数据使用该测试开关。

OpenAPI 仅增加 internal typed schemas；没有 `/v2/athlete-model`、candidate/admin HTTP route。既有 paths/schema 保持不变。新 schemas 的两个 no-unused-components warning 是 CLI-only 合同带来的已列明 warning，不伪造 endpoint 来消除它们。

## Promotion / cutover / rollback

五个 ModelPromotionGate 均 promotable=false。EngineeringComplete 仅指 M2–M6 候选工程完成；realDeviceAccepted、physiologicalValidated、productContractAccepted、rollbackReady 当前均 false。External VO₂max 的 model acceptance 是来源/外部 estimate 语义，不要求或冒充 RAI 对外部公式的生理验证。

独立门禁为 Engineering、Real-device/Data、Model/Physiological、Product Activation Acceptance，不能互相替代。

| Stage | 条件/动作（仅计划） |
| --- | --- |
| A | 内部 candidate preview，持续维护隔离和 golden |
| B | 统一真机/data acceptance；真实来源、覆盖、删除与同步生命周期 |
| C | 模型特定 calibration/validation，预注册评价方案 |
| D | legacy active + candidate shadow product read；只做差异/availability/state distribution 报告，不影响用户决策 |
| E | 用户另行授权、最小群体限定 activation；reviewed schema/channel 约束变更、可审计 assignment revision、回滚演练 |
| F | 产品/隐私/容量/回滚验收通过后再单独授权 full activation |

回滚只追加/选择 previous-active 的 assignment revision，恢复旧 active reader；保留 candidate history、v1/v2 artifact 与全部 raw facts。不得删除健康事实、回滚 raw migration 或篡改 v1 公式。现行 DB CHECK 仍只允许 active stack=legacy；本轮不提供 promotion/cutover/rollback 执行器，不修改该约束。

真机统一计划包括 Workout、Daily HRV/RHR/Sleep、External VO₂max、background sync、delete/update、offline/retry、app kill/relaunch、source provenance，覆盖授权拒绝/撤销、来源选择、跨时区/日界与重复 ACK。必须只在另行授权的数据环境执行。

模型验收独立定义：Training Load 的真实 HR coverage 与参数有效性；Running Ability 的真实性能 eligibility 和 prediction/calibration evaluation；Recovery 的纵向个体校准；Readiness 的纵向训练响应与 load completeness。Apple Health VO₂max 不能当实验室 ground truth。

产品验收独立覆盖 UI semantics、missing UX、localization、accessibility、性能、隐私、shadow不影响决策、assignment audit 与 rollback。M7 完成后停止，不自动进入上述下一阶段。
