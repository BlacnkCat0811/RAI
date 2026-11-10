# UA3 — Model Calibration & Validation Plan

版本：`UA3-plan/1`；2026-11-05。**READY BUT NOT STARTED / PHYSIOLOGICAL VALIDATION: NOT VERIFIED**。本阶段设计如何评价冻结 v1 在现实中的合理性；UA0 不招募、不收集真实数据、不执行研究、不调公式。依赖 [UA0 总合同](UNIFIED_ACCEPTANCE_CONTRACT.md)、[UA1 输入验收](REAL_DEVICE_ACCEPTANCE_PLAN.md) 与 [UA2 资格研究](DATA_AVAILABILITY_STUDY.md)。

## 1. 逐模型启动和预登记门禁

UA1/UA2 必须对相应输入和目标 cohort 达到门禁；raw loss、delete resurrection、early anchor、source misclassification 或 recovery failure 未解决时不得进入 calibration。每个模型单独协议、标签、样本支持与结论，不能用一个“model stack validated”掩盖某模型未验证。

执行 addendum 必须在读取 outcome/holdout 前冻结：模型/config/feature/映射版本；研究问题与目标用途；cohort、来源/设备、纳入排除和样本量/精度理由；主/次 outcome 定义、测量协议与质量要求；prediction 时点和预测 horizon；athlete/time split；缺失/删失/偏差与重复测量处理；主指标/比较基准；bias/MAE/RMSE 等用途相关数值容限、coverage/abstention 下限上限、分层与不确定性判定；分析代码版本；负责人/reviewer；停止条件和隐私/同意流程。

**UA0 没有真实误差分布、标签或公认用途容限，不编造数值阈值。上述执行项未实填/未独立签核时，UA3 promotion gate 关闭。** exploratory 分析必须标 exploratory，后续 confirmatory holdout 另立冻结协议。不得查看结果后补写“预登记”。

## 2. 数据分割和时间泄漏防线

1. 必须 **split by athlete**：calibration、开发选择、validation/test 的 athlete 集合互斥；同一人的 Workout 不跨训练/验证集合。任何超参数探索仅限 calibration 集合。
2. 同时做时间外验证：past → calibration，future → evaluation。固定全局时间 cutoff；保留 held-out athletes 在未来窗口的 evaluation，不能随机打散 Workout。纵向 within-person 分析可用 test athlete 自己在 prediction 之前的历史构建已冻结个人 baseline，不能用该人的 outcome 选择模型参数。
3. 每个 prediction 冻结 `asOf/evaluationAt`、数据 known-at、当前 source/config/lineage 和可见输入。future Workout/Health/outcome、迟到/修订事实、未来 quarantine confirmation 均不得进入历史 prediction。
4. 保存不可变 prospective candidate prediction 是首选。M3 当前管理重算为 revised，不能把今天重建的历史 curve 称为过去预测；M5B/M6 没有任意历史 raw as-known 重构能力。没有可证明当时可见快照时只能标 retrospective/exploratory，不能通过 prospective 门禁。
5. outcome 与输入分离；不以同次训练的同一窗口既作模型 evidence 又作“未来真实表现”。划分、数据核对和标签质量在揭盲前完成，锁定评估代码；holdout 使用后不能继续调 v1 再重复声称独立验证。

## 3. Training Load calibration

对象：`rai.training_load.hrr_integral/v1` 及冻结 load state。研究问题是已记录 running stimulus 的稳定性、参数/采样质量与后续反应关系；**不试图证明 RAI_AU = 生理疲劳**，不把 acute/chronic/balance 变成过度训练诊断。

| 研究项 | 设计与可判定输出 |
| --- | --- |
| HR parameter quality | 独立记录 HRmax/RHR 参数取得方法、时效、冲突与不确定性；来源描述不作真实生理参数认证 |
| load repeatability | 在预登记可比 session 条件下分析重复性/within-person 差异；自然差异与测量误差分开，不能要求所有 session AU 相同 |
| same-session consistency | 对同 session 的独立获准参考/处理路径比对；同 bytes 重放相等只是工程一致性，不当生理验证 |
| interval behavior | 按已独立定义的 interval 场景分层，检查短段 HR lag、pause/gap 和完整 trace/summary 方法差异；不新增强度公式 |
| missing HR bias | 比较 complete/partial/unavailable 的选择偏差；对获准完整记录可在受控副本作预登记缺失敏感性分析，不改原始事实或把模拟缺失当真实 sampling |
| subsequent physiological response | 预先指定后续主客观反应、测量时点与混杂因素，对未来反应的关联/误差和支持范围作评估；不据观察关联声称因果 |

没有 AU 的直接 ground truth 时，不把 proxy 与 AU 的差称绝对生理 MAE；MAE/RMSE 用于同单位参考/重复性或明确冻结的 outcome 评估。报告缺失机制、方法间偏差、within/between-person error、coverage/abstention 和不确定性。参数冲突造成大量 abstention 先回 UA2/root-cause，不能偷偷 clamp HRmax 或提升 summary cap。

## 4. Running Ability calibration

对象：`rai.running_ability/v1`，含其 frozen demonstrated/evidence-supported 语义。主要比较 **prediction 时已冻结的 v60 vs actual later sustained performance**；使用自然发生的独立可比持续表现，或另获明确同意/安全方案的 time trial/race。UA0 不安排运动。

预登记独立 outcome 的实际持续时长、距离/速度参考精度、flat/terrain 可比性、环境、时间间隔、努力程度证据。正式 race 也不能只凭标题推断 maximal；没有可靠 maximal evidence，结果只能支持 demonstrated performance，不变为潜在最大能力。

优先直接比较可比 60min 持续表现。20/30/40/90min race/time trial 可按冻结 curve 比较对应 duration；不得用被评估 v1 自己的 duration 转换先制造“真实 v60”再验证自己。不同 outcome 时长须分别报告单位和目标。

至少报告 signed bias、MAE、RMSE（速度单位及相对误差）、实际与预测分箱关系、coverage、abstention rate、within-person 与 between-person error。按来源、duration、evidence support、freshness、terrain 与不同能力范围分层；样本不足不宣称广泛有效。demonstrated v60 与 future maximal race 的目标差异必须明确，不能仅报 correlation 或把 score 0/100 当无能力/人类极限。

## 5. Recovery calibration

对象：`rai.recovery.physiological/v1`，**personal-baseline physiological recovery proxy**。需要纵向 HRV/RHR/Sleep deviations 与 future subjective / objective outcomes；不能凭几例“感觉差”宣告正确。

独立预登记未来 outcome，例如获准的主观恢复报告和有明确定义/重复测量质量的客观反应；规定在查看分数前取得报告，降低 expectation bias。时间、source epoch、baseline collecting/provisional/established、睡眠可见性、缺失和 confounders 单列；不得把同一 HRV/RHR/Sleep deviation 既作 predictor 又作唯一 outcome。

保留三组件完整与 autonomicOnly partial 的区别，不用 partial 填完整 score；个人 28d baseline、方向和参数保持原样。分析同一人纵向变化、人与人差异、延迟效应、缺失选择偏差及不确定性。proxy 无直接同单位真值时，以冻结 outcome 的方向/分箱/预测误差标准判定，不造 score=真实恢复百分比。

Recovery 不诊断疾病、损伤或过度训练；100 只代表该模型未观察到负担。疾病/受伤若将来成为研究目标，必须另立协议和验证模型，不能纳入本 v1 已获准目标。

## 6. Readiness validation

对象：`rai.readiness.training/v1`，running-focused instant snapshot。比较 **训练前已冻结 Readiness vs subsequent training response**；记录 prediction 时点、随后 session/反应窗口、outcome 质量和实际训练暴露，避免用未来 Workout 回填预测。

研究设计不让 candidate 影响 UI/plan/notification；按预登记方案观察自然训练响应，控制/报告 session 类型、既有能力、训练暴露、时间与来源混杂。可预登记与 Recovery-only 等冻结参考的增量比较，但该参考只是分析对照，不实现新模型、不产生新产品输出。

分别报告 Recovery availability、load baseline/coverage、unknown load 和 abstention；180d unknown 阻断不能从分析中删掉。验证 score 分箱与未来反应的关系、within/between-person error 和不确定性。当前 Readiness 不是概率，不绘制“受伤概率校准曲线”；injury/illness 不属于 v1 批准预测目标，未来研究必须另立协议。

## 7. External VO₂max comparator 与 laboratory policy

Apple Health VO₂max 始终是 **external comparator / Apple Health estimate**。可以探索与其他独立表现/测量的一致性，按来源、testType、age、metadata/source conflict 分层；不作为 RAI 模型 ground truth，也不作为唯一标签，不据 descriptor 或 calibrated=true 推断准确率/硬件认证。

若未来研究 RAI 自有 VO₂max，需要独立的 **laboratory gas-analysis VO₂max / VO₂peak** 标签、测试协议和定义，明确 VO₂max 与 VO₂peak 的区分、单位、质量控制、时间匹配与授权；Apple Health estimate 或 testType=1 不能替代。没有实验室标签及单独研发/验收前，`rai.vo2max.estimated/v1` 保持 **NOT IMPLEMENTED**。本计划不启动该模型。

## 8. 共同指标与统计计划

| 指标 | 必须说明 |
| --- | --- |
| bias | 同单位 signed prediction−outcome 均值/区间；非同尺度 proxy 不强行相减 |
| MAE / RMSE | 指标单位、预先定义的目标、样本数和不确定性；不只报拟合集误差 |
| calibration curve | 对冻结的连续 outcome 用 predicted-vs-observed 分箱；score 非概率时只称 outcome 关系/分箱表现，不能造概率解释 |
| coverage | 同时报告数据覆盖、模型有值率、参考标签可用率；若无 prediction interval，不能把 availability 称区间覆盖率 |
| abstention rate | null/collecting/stale/conflict/pending 的总比例与原因，包含无法评价的人群 |
| within-person error | 人内重复测量/变化误差；相应未来窗口和 baseline 仅用 prediction 前历史 |
| between-person error | 人间系统偏差与目标 cohort 的代表性；不能用总体 correlation 掩盖个体误差 |

独立 athlete 数是支持单位；大量单人的 Workout 不代替多人验证。冻结 cluster-aware intervals、样本量/精度依据、primary endpoint、subgroup 与多重比较策略、missing outcome 和删失处理。报告 effect size/区间和实际意义，不只报 correlation/p-value。高 abstention 的“低误差”必须连同 availability 解释；失访与仅有标签子集可能选择偏差，不能外推到无数据用户。

## 9. 研究数据与证据

research export 另行设计/授权，要求 consent、最小字段、化名、访问控制、映射分离、保留/撤回流程；不复用生产日志，不实现于 UA0。精确 prediction/health/outcome 可在经同意的受控研究存储内关联，git 仅脱敏协议、方法、聚合指标、区间、版本与 decision。

命名：`evidence/acceptance/calibration/<protocol-alias>/`。需要证明 split 无 athlete 交叉、时间 cutoff/known-at 无泄漏、分析代码/模型版本冻结、outcome 独立、排除与 n/N 可审计；公开材料不暴露 athlete/device/sample/Workout identifiers、个体分数和稀疏时间序列。

## 10. PASS 与停止条件

每模型仅在相应 UA1/UA2 接受、预登记完整、标签可信独立、athlete/time split 与 prospective 证据成立、样本/精度充分、指标和分层满足冻结标准、隐私及独立 reviewer 接受后，才能给 scope-limited validation PASS。实验性分析或不确定区间跨门槛应 INCONCLUSIVE，不能通过；一个模型通过不替代其他模型。

现实误差/偏差高于冻结标准 → 拒绝该 v1 推广、停止对应 activation；不原地更改 v1 参数保结果。**v1 remains historical；v2 candidate research** 需独立 review、新模型/config/合同及新的独立验证。任何未来标签泄漏、受试者重叠或数据完整性失败使相关验证无效，修正设计并重做，不靠删除失败样本补 PASS。

UA3 通过只回答相应模型/用途的现实有效性，不能自动授权 UA4 激活。当前真实 Load、Ability、Recovery、Readiness 均 **NOT VERIFIED**；External VO₂max 仍 external comparator。
