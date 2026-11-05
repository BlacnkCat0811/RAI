# UA0 — Unified Real-Device & Calibration Acceptance Contract

日期：2026-11-05（Asia/Shanghai）。版本：`UA0/1`。范围：**PLANNING / CONTRACT FREEZE ONLY**。本合同冻结后续测什么、如何留证、什么才算通过、什么阻止晋级；不启动 UA1–UA4，不读取真实健康数据、不实施 research export、不激活产品、不修改模型公式。

## 1. 基线与适用范围

入口四项 Git 命令及原始结果：

```text
$ git status --short
（空）
$ git rev-parse HEAD
9a8839dff0f58fe93ffc7297e50490c0c88279cc
$ git branch --show-current
main
$ git diff --stat
（空）
```

本轮仅新增验收设计/结果文档，并按 [AGENTS.md](../AGENTS.md) 追加 [IMPLEMENTATION.md](IMPLEMENTATION.md) 的实际检查和未验证项。原方案原文、Go/Swift/SQL、migrations/checksums、模型参数、现有证据均保持原样；不修改 `../running`。禁止 reset、restore、clean、stash、rebase、commit、push、deploy。不新增 Web、Watch 或桌面客户端；Apple Watch 仅作为未来输入设备，RAI 仍是原生 iOS、只读 HealthKit，WorkoutKit 独立授权。

## 2. 当前 candidate stack

| 阶段 | 冻结身份 / 范围 | 已有工程结论与证据 |
| --- | --- | --- |
| M1 Workout Features | `rai.workout_features/v1`；M1 shared substrate | [PASS WITH RISKS](M1_RESULT.md) |
| M2 Training Load | `rai.training_load.hrr_integral/v1`、`rai.load_state.exp_finite/v1` | [PASS WITH RISKS](M2_RESULT.md)；[v1 合同](TRAINING_LOAD_V1_CONTRACT.md) |
| M3 Running Ability | `rai.running_ability/v1`；performance/endurance 同一模型链 | [PASS WITH RISKS](M3_RESULT.md)；[v1 合同](RUNNING_ABILITY_V1_CONTRACT.md) |
| M4 External VO₂max | `rai.vo2max.external_projection/v1` | [ENGINEERING CLOSED；B4 PASS WITH RISKS](M4B4_RESULT.md)；[来源合同](VO2MAX_EXTERNAL_CONTRACT.md) |
| M5B Physiological Recovery | `rai.recovery.physiological/v1` | [PASS WITH RISKS](M5B_RESULT.md)；[冻结合同](RECOVERY_FOUNDATION_CONTRACT.md) |
| M6 Training Readiness | `rai.readiness.training/v1` | [PASS WITH RISKS](M6_RESULT.md)；[冻结合同](TRAINING_READINESS_CONTRACT.md) |
| M7 Integration / Product Contract | `rai.athlete_model/1`、`rai.product_metric_mapping/1`；read/validate/Freshen/map | [PASS WITH RISKS](M7_RESULT.md)；[产品合同](ATHLETE_MODEL_PRODUCT_CONTRACT.md) |

这些是历史工程结论，不是本轮重做全部验收。M7 记录的全量隔离 PG18/race、Swift/Simulator 证据见 [M7 validation](evidence/m7/validation.json)，不推导真实设备或生理通过。后端基线最高 migration 为 27。当前 active=legacy，五个 candidate promotion gate 均 false，无正式 candidate HTTP endpoint；未来 transport/activation/rollback 执行能力仍需独立工程任务。

## 3. 四层模型与阶段对应

**Engineering PASS ≠ Real-device PASS ≠ Physiological Validation ≠ Product Activation。** 每层独立 decision、证据、适用范围、审阅人和日期；前层通过不是后层授权。

| 层 | 阶段 | 要回答的问题 | 不能据此宣称 |
| --- | --- | --- | --- |
| A Engineering Acceptance | 已有 M1–M7 | synthetic/隔离数据库中，公式实现、事务、边界和合同是否成立 | 现实输入完整、模型准确、产品获准 |
| B Real-Device / Real-Data | UA1 + UA2 | 真实输入能可靠进入，且有多少用户/日/Workout 满足冻结 v1 资格 | 生理有效、可激活 |
| C Model Calibration / Validation | UA3，逐模型 | 冻结 v1 与独立未来结果的误差、支持和限制 | 所有模型通用通过、产品自动通过 |
| D Product Activation Acceptance | UA4 shadow + 独立 activation gate | UX、隐私、性能、隔离、回滚和授权是否齐备 | 未签核即 cutover |

阶段顺序：UA1 输入管道 → UA2 availability → UA3 model-specific validation → UA4 shadow → 可单独设计 limited activation → full activation。UA0 只冻结设计。UA1 的来源/样本不足不能通过人工补造数据修饰；某模型失败只阻断其能力及依赖消费者，不允许用其他模型的 PASS 替代。系统性数据完整性或隐私失败阻断整条后续链。

## 4. 状态和判定规则

| 状态 | 必须满足的含义 |
| --- | --- |
| PASS | 该项在已声明设备、版本、cohort、窗口及预登记标准内实际执行并满足全部条件；附直接证据 |
| PASS WITH RISKS | 适用范围内无未解决 P0/P1；明确限制、风险和剩余门禁。不能绕过必测项 |
| DEFERRED / NOT EXECUTED | 当前条件/授权未到；不是失败，也不是通过 |
| NOT OBSERVED | 已观察但相应现实事件/来源未发生；保留观察窗口和覆盖缺口，不计 PASS |
| NOT VERIFIED / INCONCLUSIVE | 证据、独立结果、精度或样本支持不足；晋级关闭 |
| BLOCKED | 硬停止条件或必需的实现/授权/预登记项缺失；修复、复验或正式补充协议前不得晋级 |
| N/A | 结构上不适用且记录理由；不能把未观察/难执行改成 N/A |
| NOT AUTHORIZED | 不具备该行动授权；工程通过不能改变它 |

UA0 最终结果只允许 PASS / PASS WITH RISKS / BLOCKED。未来阶段若必测项 NOT OBSERVED，不得给全范围 PASS；可以签有限范围观察结论，但相应晋级门禁继续关闭。研究门槛不从试验结果反推：UA2/UA3/UA4 执行前必须冻结数值标准、目标人群、观察长度和审阅责任人。UA0 不虚构未经证据支持的临床误差界限；任何未填写的执行协议项默认 fail closed，详见各计划。

## 5. 冻结不变量与停止条件

1. 数据完整性：原始事实、删除屏障、提交日志、分析待办和最终回执同事务；dataset 行锁给出提交顺序，不用 sequence 作下载水位。重试 immutable bytes 并返回原回执；删除优先，旧上传不复活 UUID；不丢未知字段、不给未实现 handler 假成功。
2. 模型边界：不新增 Load/Ability/VO₂/Recovery/Readiness 公式，不新增 Athlete/Health/Fitness 综合分。UA2 不放宽资格，UA3 不原地调整 v1 参数/阈值/config。若需改变，保留 v1 历史，另立 **v2 candidate research + separate review**。
3. 不泄漏：历史 prediction 不使用未来 Workout、Health data、future outcome 或事后 revised publication。无真实 as-known 快照不能假装 prospective prediction。
4. 产品：legacy active；candidate 不影响 UI、plan、Coach 或 notification。真实 activation、assignment 放开、HTTP/UI 接入和 rollback 执行另需工程验证与明确授权。
5. UA1 发现 raw data loss、delete resurrection、early anchor commit、source misclassification 或满足观察/恢复条件后仍无法恢复的 background failure，立即 BLOCKED，停止进入 calibration。
6. UA2 若目标 cohort 中超过半数用户在预登记长期窗口内低于预登记 availability 门槛，先 root-cause review；小样本/不确定时 INCONCLUSIVE，不调低 qualification。UA3 超出冻结误差/效用标准，拒绝该 v1 推广，另立 v2；不能在 holdout 上调参后沿用旧验证。

同步不依赖备份、恢复、迁移、全库摘要或全量重算。rollback 不删除 raw/history，不依赖 migration rollback。

## 6. Acceptance Matrix：UA0 冻结时点

| Capability | Engineering | Device/Data | Calibration | Product |
| --- | --- | --- | --- | --- |
| Workout Sync | PASS WITH RISKS（既有工程回归） | DEFERRED | N/A（输入管道） | NOT AUTHORIZED |
| Training Load | PASS WITH RISKS | DEFERRED | NOT VERIFIED | NOT AUTHORIZED |
| Running Ability | PASS WITH RISKS | DEFERRED | NOT VERIFIED | NOT AUTHORIZED |
| Daily Health | PASS WITH RISKS（既有 M5A 管道） | DEFERRED | N/A（原始输入） | NOT AUTHORIZED |
| External VO₂max | CLOSED；B4 PASS WITH RISKS | DEFERRED | external comparator only；NOT VERIFIED | NOT AUTHORIZED |
| Recovery | PASS WITH RISKS | DEFERRED | NOT VERIFIED | NOT AUTHORIZED |
| Readiness | PASS WITH RISKS | DEFERRED（real coverage 缺口） | NOT VERIFIED | NOT AUTHORIZED |

Workout/Daily 的工程范围参见 [M1 回归](M1_RESULT.md)、[M5A-RD 历史报告](M5A_RD_RESULT.md) 和 [M7 回归](M7_RESULT.md)。仓库中旧设备报告只保留其原有范围，不覆盖当前统一模型栈；不改写旧结论，也不移植为本矩阵的 Device/Data PASS。

## 7. Evidence 与隐私合同

未来统一仓库相对命名（UA0 不创建真实验收结果）：

```text
evidence/acceptance/device/<run-alias>/
evidence/acceptance/availability/<study-alias>/
evidence/acceptance/calibration/<protocol-alias>/
evidence/acceptance/shadow/<run-alias>/
evidence/acceptance/product/<release-alias>/
```

每阶段的脱敏 manifest 至少包含 protocol/version、case ID、执行或观察窗口、设备型号与实际 OS/build（无序列号）、backend HEAD/schema、模型/config/映射版本、执行者/审阅角色、expected/observed、状态、例外、证据引用和 gate decision。UA1 精确比对所需的原始身份、anchor/body/receipt 仅在独立受控本地验收存储内核对；git 只保存一致性布尔、合并计数、非识别性 case alias 和审阅结论。不把原始样本 hash/稳定 UUID hash 当作匿名证据；精确时间与稀疏分组需删减或抑制。

**真实验收数据不得进入 git fixture。** 普通日志不得输出 HRV、RHR、sleep times、VO₂max、Readiness、Recovery、Workout UUID、HealthKit UUID、device identifiers；也不输出其他健康数值/score、route、source/device 用户命名、原始 body/anchor/receipt。Shadow 仅脱敏 aggregate；差异必须分箱/合并并检查小组再识别风险，不记录逐用户 legacy/candidate score。

未来 research export 必须另立协议：user-consented、minimal、pseudonymized、access-controlled，明确用途/字段/访问者/保留期/撤回删除及备份处理；身份映射分离，健康值留在受控研究环境。不能复用生产日志，不能把化名化等同匿名化。UA0 仅设计，不实现采集或导出。

## 8. 计划索引和退出状态

- [UA1 Real Device & Real HealthKit Acceptance](REAL_DEVICE_ACCEPTANCE_PLAN.md)
- [UA2 Real Data Availability & Eligibility Study](DATA_AVAILABILITY_STUDY.md)
- [UA3 Model Calibration & Validation](MODEL_CALIBRATION_PLAN.md)
- [UA4 Shadow Read & Product Activation](PRODUCT_ACTIVATION_PLAN.md)
- [UA0 18 节结果报告](UA0_RESULT.md)

UA0 完成后的状态：MODEL ENGINEERING=CLOSED（candidate development）；PRODUCT CONTRACT=CLOSED（M7 engineering）；REAL-DEVICE ACCEPTANCE=READY BUT DEFERRED（计划就绪，非实现门禁全就绪）；CALIBRATION=READY BUT NOT STARTED（协议框架就绪，执行 addendum 未签核）；PRODUCT ACTIVATION=NOT AUTHORIZED。完成即停止，等待用户明确恢复真实设备验收条件，不自动启动 UA1。

## 9. 启动要求追踪

下表对照用户启动要求的 54 节，仅声明合同覆盖；不表示其中任何未来执行项已通过。

| 原要求 | 冻结位置 |
| --- | --- |
| 1–2 目的 / 四层独立 | 本文 §3–4 |
| 3 禁止新增模型 / v1 不原地修改 | 本文 §5；UA3 §10 |
| 4 Git baseline / 禁止操作 | 本文 §1；UA0 Result §1 |
| 5–6 UA1 目标 / 设备矩阵 | UA1 §1–2 |
| 7 Workout / all-stream catch-up | UA1 §4 |
| 8 Daily Health | UA1 §5 |
| 9–10 External VO₂max / 来源分类 | UA1 §6 |
| 11 Background | UA1 §7 |
| 12 App kill 五点 | UA1 §8 |
| 13 Offline | UA1 §9 |
| 14 Delete/update / NOT OBSERVED | UA1 §10 |
| 15 UA1 非模型准确率 | UA1 §1、10；本文 §3 |
| 16 UA2 目的 | UA2 §1–2 |
| 17 Training Load availability | UA2 §3 |
| 18 Running Ability availability | UA2 §4 |
| 19 Recovery availability | UA2 §5 |
| 20 Readiness availability | UA2 §6 |
| 21 External VO₂max availability | UA2 §7 |
| 22 UA2 不调门槛 | UA2 §8；本文 §5 |
| 23 UA3 目的 | UA3 §1 |
| 24 Training Load calibration | UA3 §3 |
| 25 Running Ability calibration | UA3 §4 |
| 26 Recovery calibration | UA3 §5 |
| 27 Readiness validation | UA3 §6 |
| 28–29 External comparator / laboratory / own VO₂未实现 | UA3 §7 |
| 30–31 Athlete/time split / 不泄漏 | UA3 §2 |
| 32 Calibration metrics | UA3 §8 |
| 33–35 UA4 前置 / Shadow / aggregates | UA4 §1–2 |
| 36 Limited activation | UA4 §6 |
| 37 Activation gates | UA4 §5 |
| 38 Rollback exercise | UA4 §7 |
| 39 Missing UX | UA4 §3 |
| 40–41 产品语义 / 禁止医学化 | UA4 §4 |
| 42 Acceptance Matrix | 本文 §6；UA0 Result §16 |
| 43–45 Evidence / privacy / research export | 本文 §7；UA3 §9；UA4 §8 |
| 46 真机停止 | UA1 §10；本文 §5 |
| 47 Availability 停止 | UA2 §8 |
| 48 Calibration 停止 | UA3 §10 |
| 49–50 UA0 planning-only / 交付 | 本文开头、§8 |
| 51–52 十八节报告 / 结果范围 | UA0 Result §1–18 |
| 53–54 完成即停止 / 最终状态 | 本文 §8；UA0 Result §18 |

UA1/UA2/UA3/UA4 分别指 §8 中链接的对应计划；UA0 Result 为 [结果报告](UA0_RESULT.md)。
