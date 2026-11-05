# UA4 — Shadow Read & Product Activation Acceptance Plan

版本：`UA4-plan/1`；2026-11-05。**DESIGN ONLY；PRODUCT ACTIVATION: NOT AUTHORIZED**。遵循 [UA0 合同](UNIFIED_ACCEPTANCE_CONTRACT.md) 与 [M7 产品合同](ATHLETE_MODEL_PRODUCT_CONTRACT.md)。本文件不启用 shadow、不改 assignment、不部署、不接正式 UI/plan/notification。

## 1. 阶段与当前实现边界

| 步骤 | 前置与允许结果 | 当前状态 |
| --- | --- | --- |
| A engineering / internal preview | M1–M7 candidate 实现和受限 admin preview；工程结论独立 | 已有 PASS WITH RISKS，不是 activation |
| B real device / availability | UA1/UA2 对相应输入、cohort 和模型接受 | DEFERRED / NOT STARTED |
| C model validation | UA3 对每个模型、用途独立接受；M4 保持 external comparator 语义 | NOT VERIFIED |
| D shadow | B/C 均达到各自门禁，隐私/性能/影子实现独立审阅后运行 | NOT STARTED |
| E limited activation | shadow 接受后才设计；实现/UX/隐私/性能/rollback 全通过并明确授权 | NOT AUTHORIZED |
| F full activation | limited cohort 观察和所有产品门禁接受，另行签核 | NOT AUTHORIZED |

当前 active 数据库约束仅 legacy；M7 无正式 candidate endpoint/UI、无 activation/rollback 执行器，promotion-plan 只输出只读计划且五项 promotable=false。未来 shadow 读取的调度、最小权限服务、aggregate 输出、assignment revision、消费者版本适配、真实回滚都需要单独 additive 工程设计/验证；不能直接放开 DB constraint 或把静态 gate 改 true 当验收。UA0 不实现任何一项。

## 2. Shadow contract

正式产品保持 **legacy active**，后台独立读取 candidate。candidate 不影响 UI、training plan、Coach 决策或 notifications；不通过隐藏 endpoint/query flag 给普通 API 角色绕过 preview 权限。读路径只能读取/验证/Freshen/map 已发布结果，不重算模型，不修改 raw、history、assignment 或 legacy 输出。

开始前冻结：cohort/同意和来源范围、observation window、版本/config/evaluationAt、aggregate 周期与最小组大小、availability 标准、latency/failure budgets、技术故障停止线、隐私审阅、负责人/reviewer 和关闭 shadow 方法。数值标准或实现能力缺失均 BLOCKED；未观察够窗口或样本不足不能 PASS。

| Shadow metric | 计算与报告口径 |
| --- | --- |
| availability rate | 完整/fresh candidate 的 n/N，分模型和预登记观察窗口；missing 不填值 |
| state distribution | available/limited/collecting/unavailable/stale 及 sync/source 原因的合并计数；technical failure 独立 |
| legacy-vs-candidate difference | 只对可比较的含义/单位/evaluationAt 计算预登记分箱 aggregate；不同 legacy/new 含义标 non-comparable，不要求数值相等，也不把差异当准确率 |
| latency | 实际 shadow reader/end-to-end p50/p95/p99、样本数、负载、超时；与预登记预算比，不能引用本机 M7 benchmark 当生产 SLO |
| failure rate | technical failure / attempted reads，重试与分母明确；业务缺失单列，不混为成功/技术错误 |

禁止普通日志逐用户输出 health values、scores、UUID、device identifiers、source 用户命名或可追踪稀疏时间序列。只记录经过小组抑制/合并的脱敏 aggregate。线上实现必须先审阅 aggregate 缓存/保留、访问控制及关闭路径；不复用生产日志作研究 dataset。

Shadow PASS 还需实际证明 candidate read 前后 legacy UI/API/plan/notification 行为不变，无 candidate 泄漏、身份/版本混淆或隐私问题；完成冻结窗口、aggregate quality、availability/性能/错误预算并独立签核。Shadow 不能验证模型生理准确性，也不能自行授权 activation。

## 3. Missing UX 与真实产品验证

真实 iOS UI 的验收另行实施，不把 M7 schema/Swift decoding 或 Simulator PASS 当真机 UX 验收。必须覆盖：

| 输入状态 | 用户可见要求 / PASS 条件 |
| --- | --- |
| collecting | 清楚说明收集历史/基线中，不显示伪精确分数或承诺必定何时有值 |
| limited | 保留 partial/证据限制，不能表现为 complete；未知模型 value 不展示 |
| sync incomplete | 说明同步/依赖未齐，不能当低 readiness/低能力 |
| stale | 标注原证据时间/过期限制，不当 fresh current；遵守模型清值规则 |
| unavailable | 无可用值及可解释原因，缺失不显示 0/Recovery 100 |
| source conflict | 保留冲突/显式来源选择语义，不平均、择高或静默切换 |

在实际支持设备上验证文案、本地化、动态字体/VoiceOver、布局、更新时间/asOf/freshness、无值/技术错误的区别和用户理解。对无新历史趋势的 M3/M5B/current snapshot 不编造趋势，对 instant Readiness 不展示为长期能力。unknown load 不填零，latest pending Workout 不用旧 analyzed Workout 冒充。

## 4. 产品语义与禁止宣称

| Metric | 必须保留的产品含义 | 必须验证的限制 |
| --- | --- | --- |
| Training Load | **recorded running load** | RAI_AU/每日 state 单位区分，非全身疲劳、非过度训练诊断 |
| Running Ability | **recent demonstrated running performance** | 证据支持的持续跑表现，不称潜在最大能力/VO₂/健康等级 |
| External VO₂max | **Apple Health estimate** | externalEstimate=true；只有来源分类支持才可称 Apple Watch-derived，非 RAI measured/lab truth |
| Recovery | **personal-baseline physiological recovery proxy** | 三组件与个人基线支持/缺失分开，100 非医学完美状态 |
| Readiness | **running-focused training readiness** | 当前恢复与已记录跑步负荷上下文，非强制训练指令或全身 readiness |

confidence 是 evidence support，不是准确概率；不显示 Confidence 72% 等概率化文案。不得诊断疾病、预测受伤或诊断过度训练；这些需要未来专门研究和模型验证。正式 UI 不能把 legacy readiness/fitness 改标签冒充 candidate 新语义。

## 5. Product Activation Gate

| 门禁 | 接受所需证据 | UA0 当前 |
| --- | --- | --- |
| Engineering accepted | 实际拟发布 transport/reader/assignment/consumer 版本完整验证，未解决 P0/P1=0；A 层范围明确 | M1–M7 PASS WITH RISKS；activation 实现未交付 |
| Real-device accepted | UA1 相关必测范围通过，M6 real catch-up 闭环，真实来源/metadata 行为确认 | DEFERRED |
| Data availability acceptable | UA2 预登记 threshold/精度/目标 cohort 接受，不能靠放宽资格 | NOT VERIFIED |
| Model-specific validation accepted | UA3 对模型/用途独立接受；M4 external comparator provenance/语义审阅，非虚构生理 gate | NOT VERIFIED |
| Product UX accepted | 真实 iOS 含 missing/limited/stale/conflict、本地化、可访问性与文案验证 | NOT VERIFIED |
| Privacy accepted | 最小权限、同意/撤回、日志/aggregate/研究数据边界真实审阅 | NOT VERIFIED |
| Performance accepted | 拟发布部署/设备/网络和目标负载的 latency/failure/资源预算接受 | NOT VERIFIED |
| Rollback exercised | 实际 candidate active → previous active 演练，含消费者和版本兼容，保留 raw/history | NOT EXECUTED |
| Activation authorization | 对明确 model/config/build/cohort、时窗和回滚 owner 的用户授权/发布签核 | NOT AUTHORIZED |

前八项是正式切换的最低接受项，第九项是执行授权；任一未通过不晋级。M7 `physiologicalValidated` 等当前静态字段不包含 UA0 的完整执行证据，不能当通用开关，尤其不把 external estimate 冒充 RAI 生理验证。未来接入 gate 需独立版本化映射/审计设计。工程关闭、合同关闭均不等于 product UX accepted。

## 6. Limited activation 仅在 Shadow 之后设计

未来可选择 internal test account、developer mode 或 explicit opt-in；必须明确 user-visible scope、退出方式、同意、对计划/通知的影响、观察预算、自动停止与回滚 owner，不自动推广到全用户。该 cohort 本身不是权限绕过；ordinary API 不能凭传参选择 candidate。UA0 不创建账号、不切 channel、不使候选影响任何真实决策。

只有 shadow 被独立接受后，才准备这份具体 limited activation 方案；上线/部署/真实账户切换仍需明确授权和已验证实现。limited 期间未达到标准，停止推广并回滚，不就地调 v1。Full activation 必须另行审阅，不能由时间经过自动触发。

## 7. Rollback Exercise

M7 已有 rollback plan，**没有已执行演练**。为避免“先正式切换才能证明能回滚”的循环，未来先在获准的隔离环境/测试账户实际演练拟发布实现；可先用 synthetic 数据执行工程演练，真实产品范围的验收与授权仍独立。不得为 UA0 执行该演练。

1. 明确 previous-active assignment/model/config、消费者兼容版本、待激活 candidate 和控制面权限；记录受控前态与 raw/history/receipt 完整性。不复制健康值到普通日志。
2. 使用实际拟发布控制面和消费者使 **candidate active**，确认读取的 assignment revision、模型身份、缺失状态及版本；仅编辑一份计划或 mock 不算执行。
3. 触发预登记的 rollback 条件（技术失败、误展示、可用性/性能越界、来源/完整性/隐私问题），以新增受审计 assignment revision 切 **previous active**；明确并测量触发到消费者恢复的预算。
4. 验证 in-flight reads、并发 Worker/新版本 publication、客户端本地状态/可能缓存都不会重新泄漏 candidate；计划/通知/Coach 读取 previous active，已经产生的真实副作用如何处置需在未来方案中先定义。
5. 验证 raw facts、tombstones、receipts、candidate artifacts 和 immutable histories 保留；同步 incremental/retry/anchor 仍成立，无 UUID 复活、健康事实或历史删除。
6. 验证不依赖 migration down、备份恢复、全库摘要或全量重算；保留 additive schema，candidate 回到不可见/隔离范围。记实际耗时、结果、异常及独立 reviewer。

PASS 要求两条实际 active 路径均真实执行、消费者恢复在预登记预算内、无数据/历史损坏或 candidate 残留。当前 legacy-only constraint 和未交付执行器使本项仍 **NOT EXECUTED**；计划本身不能设 `rollbackReady=true`。

## 8. 证据和退出

Shadow：`evidence/acceptance/shadow/<run-alias>/`；UX/gates/limited/full/rollback：`evidence/acceptance/product/<release-alias>/`。只存脱敏 manifest、aggregate、决策与经审查无健康值/标识的截图；真实屏幕和细粒度研究值留受控环境。每项明确 executed/observed/accepted/authorized，不能混写。

UA4 未达任何 gate 均保持 legacy active；出现完整性、隐私或医学化误导立即停止对应试验并按获准回滚方案处置。UA0 退出时：shadow NOT STARTED、rollback NOT EXECUTED、limited/full activation NOT AUTHORIZED。后续工作等待独立启动，不自动发生。
