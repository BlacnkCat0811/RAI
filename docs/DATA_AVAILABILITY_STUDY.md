# UA2 — Real Data Availability & Eligibility Study

版本：`UA2-plan/1`；2026-11-05。**READY BUT NOT STARTED**。这是 B 层真实数据可用性研究设计，回答“冻结 v1 在现实中有多少可用输出、为何没有”；不是调参或生理准确性验证。总门禁见 [UA0](UNIFIED_ACCEPTANCE_CONTRACT.md)，输入管道前置见 [UA1](REAL_DEVICE_ACCEPTANCE_PLAN.md)。

## 1. 启动门禁与预登记

UA1 对研究所依赖的输入范围必须先接受；NOT OBSERVED 的来源/更新/设备组合不能自动进入已接受范围。原始数据完整性缺陷或 M6 缺 real catch-up transport 时，相应资格只能报告受阻，不进入 UA3。数据仅来自未来明确同意的隔离验收/研究环境，真实值不写 git fixture 或生产日志。

执行前冻结有版本的 study addendum，至少填写：目标人群与纳入排除、设备/OS/source 分层、招募与退出方法、athlete 数和精度理由、开始/结束与最短随访、冷启动/成熟期边界、timezone、模型/config/来源 registry/产品映射版本、各比例分母、置信区间方法、acceptable availability 数值及独立 reviewer。**当前均未实填/未签核，不能据本计划判 UA2 PASS。**

门槛须在读取研究结果前冻结，不从观察结果倒推；availability 门槛是用途/产品接受标准，不是 v1 输入 qualification。没有足够依据选数值时，先进行明确标记为 descriptive-only 的观察协议；它不能给出 promotion PASS，后续确认研究必须独立预登记。

## 2. 统计单位与共同口径

- 同时报告 athlete、athlete-day、unique Workout 三种分母；每项写 `n/N`、排除数和原因，零分母为 N/A，不报 100%。同一 Workout 的多窗口/重试/revision 不增加独立 Workout 数。
- 主要 availability 为预登记 eligible observation days 中有当前、fresh、完整 v1 输出的 athlete-day 比例；给每位 athlete 的比例及 cohort 分布。不能只给活跃高频用户加权后的一个总数。冷启动、成熟期、stale、同步缺失、source conflict 单列；不能删掉 unavailable 用户提高比例。
- 固定 evaluationAt/本地 calendarDate；区分事件时间、known-at、同步到达和模型发布延迟。只在计划时点观察 candidate，禁止事后重算把当时 unavailable 改写成 available。
- state 主分类互斥，reason 允许多重并注明占比之和可超过 100%；技术失败、未 enrolled、pending 与真实无样本分开。summary-only/partial 不计 complete。
- 报样本密度、分位数、missingness 连续长度、首次 eligible 时间、source/OS 变更前后；不把缺失当 0 或正常。跨午夜/DST 按冻结 calendar，不用 UTC+24h 代替日历日。
- 比例区间方法预先冻结；重复日/Workout 需按 athlete 聚类估计不确定性。小样本或无覆盖分层报告 INCONCLUSIVE；“无人有值”不能删去该组。所有公开分组需小样本抑制。

## 3. Training Load availability

| 指标 | 分母/观察定义 |
| --- | --- |
| Workout count | 目标窗口所有实际可见 running Workout；另列协议不支持/未完成同步数量 |
| HR coverage | 各 Workout 的 M1 支持 active seconds / active seconds；分布及 unknown 数，不从 sample count 推 coverage |
| gap distribution | native/reconstructed gap 时长和最大 gap 分布；无 trace 与大 gap 分开 |
| parameter availability | 对相应 event time 有唯一合法 HRmax/RHR 参数身份的 Workout 比例；标来源与冲突，不能默认补参数 |
| HRmax conflicts | HR 与冻结参数边界冲突的 Workout 数 / 被评估 Workout 数；保留零容忍策略原因 |
| summary-only rate | 只有合格 whole-workout summary 而没有 supported trace 的数量 / 全部 Workout；另列 summary 不合格 |
| complete load rate | complete hrr_integral 的 Workout 数 / 全部目标 Workout |
| partial load rate | gap_scaled/summary approximation 的 Workout 数 / 全部目标 Workout；方法分别列出 |
| unavailable rate | null load 的 Workout 数 / 全部目标 Workout，按 missing HR/reference/interval/conflict/pending 等分原因 |

complete/partial/unavailable 加上明确技术/待处理类别必须能对齐总数；不将 partial 视为完整日负荷。Load state 另报 known-zero、unknown、partial 和成熟期分布；没有 recorded Workout 不能证明 rest/全身负荷为零。

## 4. Running Ability availability

| 指标 | 冻结口径 |
| --- | --- |
| performance eligible workouts | 通过 M1 Workout/window qualification 的独立 Workout / 所有候选 running Workout，eligible/conditional 分列 |
| duration groups | 20/30/40/60/90 min 各 duration 的独立支持数；同一 Workout 多窗口不冒充多人/多次 |
| 20–30m support | 短组支持达冻结 M3 要求的 athlete-evaluation 比例 |
| 40–90m support | 长组及至少一次 ≥40m 的支持比例；无长证据不默认 γ |
| terrain rejection | M1 terrain/flat-comparability 拒绝数/被评估 Workout；unknown 与明确不合格分开 |
| speed coverage rejection | speed support/gap/coverage 拒绝数及分布 |
| route/grade rejection | route/grade 不足/冲突拒绝数，不能再用另一套 GPS 修补资格 |
| outlier quarantine rate | confirmation_required Workout / 已评估 performance Workout；解除需独立后续证据 |
| 90d demonstrated availability | 各 evaluation 的冻结 90d evidence、独立 Workout/history span、短长组、稳定性、平滑支持与 freshness 均满足时的非 null demonstrated ability 比例 |

同时报告 evidenceMode、as_known/revised、n_eff、source epochs、stale/expired、input-limit 与 pending。不能把 retrospective revised 结果标记成当时 prediction。96 Workout/480 points 等现有上限触发率单列，不删用户或静默截断。

## 5. Recovery availability

| 指标 | 分母/观察定义 |
| --- | --- |
| HRV usable days | 同源 epoch/日上下文满足 M5B 的日数 / 观察 athlete-days；raw count 和 usable 分开 |
| RHR usable days | 具有唯一受支持 RHR 的日数；多来源/多修订冲突单列，不平均 |
| Sleep usable days | session/主睡眠/timeline coverage 等冻结规则通过的日数；nap/缺尾/unknown 分开 |
| 3-component complete days | 三组件与各 baseline、coverage/freshness 同时满足的完整日比例；autonomicOnly 不计完整 |
| baseline collecting duration | 从观察/enrollment 起至每组件 provisional/established 的日历时长，未达到者按右删失报告 |
| source conflicts | 冲突日 / 各类可评估日，source epoch 切换与 baseline 重建分开 |
| sleep episode coverage | known/asleep/awake/unknown 支持比例、迟到与重叠/episode 歧义率；精确时刻不公开 |

M5B 使用冻结的过去 28 日（今日不入 baseline），≥14 usable days 且前 7 日 ≥3 日等规则保持原样。此研究不改成 42 日，不以“让分数出现”补 Sleep 或放宽 coverage。source epoch 是描述符等价，不当物理设备认证。

## 6. Readiness availability

| 指标 | 冻结口径 |
| --- | --- |
| Recovery available rate | current complete/non-null/fresh M5B / 观察 athlete-evaluations |
| load baseline established rate | 满足 M6 42 prior civil days 的非零完整训练日、≥5日且跨度≥14日、最新训练日 freshness 等条件的比例 |
| Workout catch-up completeness | 服务器可核验的 real all-stream coverage 有效率；缺 transport 报 implementation-blocked，不用 syntheticCoverage 顶替 |
| unknown-load blocking rate | 180d burden 范围内 missing/partial/incompatible M2 引用阻断的比例；再按 age/reason 分箱 |
| full Readiness availability | M6 全部门槛满足且非 null 的完整输出 / 全部目标 athlete-evaluations；同时给每人比例和连续缺失长度 |

将 Recovery、load baseline、coverage、unknown load 的交叉阻断列出，防止一个高分项掩盖另一硬门槛。`sync_incomplete` 是如实结果，不是“用户 readiness=0”。running-only 范围外 cycling/strength 不补估，也不能称 whole-body readiness。

## 7. External VO₂max availability

统计 users/days with sample（分别给所有同意参与者及有可见样本者分母）、sample frequency、sample age、source types、metadata variants、source conflicts。区分 raw 可见样本、协议成功接收、Apple Watch estimate default-eligible、选定 epoch 和最终 available/stale/expired projection；third-party/manual/unclassified 不消失于报告。

metadata variants 只报告 registry key/type 的脱敏合并兼容类别，未知 payload 留受控环境；不输出用户命名或设备标识。无样本、无读可见性、无来源选择、冲突和未 projection 独立列出。此项只研究外部数据可用性，不验证 RAI VO₂max，也不建立实验室标签。

## 8. PASS、停止与输出

UA2 接受须同时满足：受依赖输入 UA1 已接受；数据/分母/排除可审计；预登记 cohort/观察长度/精度达标；每模型可用率的不确定性界限满足预先确定的 acceptable threshold；独立 reviewer 接受局限；无未关闭隐私/完整性问题。任一必需 addendum 数值空缺、长随访不足或 cluster 支持不足，不能 PASS。

如果多数用户长期 unavailable：先核查授权可见性、采样密度、source/registry、cold start、同步追平、M2 参数与 partial、M3 长证据、M5B 组件/epoch、M6 180d unknown、实现上限和真实使用场景；形成 **root-cause review**。不为了 availability 临时放宽门槛，不把缺失填 0/100。资格/参数变更须独立审阅及版本决策，改变 v1 模型语义必须 v2。

输出 `evidence/acceptance/availability/<study-alias>/` 的脱敏 manifest、预登记协议摘要、n/N/区间和状态分布、缺失原因、受阻项、scope-limited decision。真实 individual-level dataset 留在独立受控研究存储，不进入仓库。UA2 即使 PASS 也不能替代 UA3 或产品激活。
