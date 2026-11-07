# 运动能力模型设计实现差距核查

核查日期：2026-11-06（Asia/Shanghai）。代码基线：`main` / `18eab55` 加当前工作区。本文是功能实现核查，不是模型效度、数据库、真机或上线验收报告。

## 1. 结论与核查口径

**特征、HRR 训练负荷、已展示跑力、日常健康事实、生理恢复和训练准备度已有候选实现；自有 RAI VO₂max 尚未实现，新模型尚未接入正式产品。** 原文不能继续整体按“下一步开始 M1”理解，也不能因为后续阶段报告写了 PASS 就认为原设计全部交付。

用户附件与仓库 [RAI_MODEL_ARCHITECTURE.md](../RAI_MODEL_ARCHITECTURE.md) 字节一致，SHA-256 均为 `d5c26bd27b3de4e4f32392431869bb1da04e09426da0ea381e667c34cc4346af`。以该文件原始章节和功能为需求基线，再核对当前源码、路由、Worker 注册、迁移、测试及后续合同。原文保留，不覆写历史结论。

区分四种情况：

- **已实现（候选）**：有领域计算及实际存取、Worker、发布路径；不代表正式 App 使用。
- **部分实现／实现有差异**：部分支撑已具备，或后续冻结合同改变了原规格。
- **未实现**：没有完成对应计算、接口或执行流程；占位字段、schema、计划不能算完成。
- **未验收**：代码存在，但缺真实设备、现实模型或产品证据。与未实现分别记录。

入口已有 `docs/IMPLEMENTATION.md` 和 App 图标未提交改动，本轮保留。最高 migration 为 29；这是源码状态，不代表任何现有数据库已执行到该版本。未读取数据库、个人健康值或旧 `../running`。

## 2. 按原设计阶段对照

| 原设计阶段 | 当前状态 | 已具备 | 仍缺什么 |
| --- | --- | --- | --- |
| M0 数据审计与合同 | 设计已完成，现实分布未全面验证 | 原设计、后续模型和验收合同 | 不能以文档证明真实资格率及生理有效性 |
| M1 特征与发布底座 | **主要已实现，候选** | typed snapshot、事件／暂停／来源／环境 loader、时间支持、窗口／稳态／长跑前后半、逐模型资格、artifact／input／channel／依赖 | 正式详情页迁移到共享发布特征未完成；部分异常策略与原文不同 |
| M2 训练负荷 | **主要已实现，候选** | HRR 解析积分、summary／gap 分级、分日 ledger、有限 7/42 日状态、unknown、删除与有界重算 | baseline 自动提供 RHR reference 未接通；跨 UUID 重叠 Workout 隔离未见实现 |
| M3 跑力 | **已展示能力主路径已实现，候选** | 速度持续曲线、v60、固定评分、γ／retention、独立证据权重、异常确认、留一检查、平滑与过期 | maximal_supported、CS／D′、完整长跑诊断与用户历史趋势尚缺 |
| M4 条件式自有 VO₂ | **未实现** | M1 有稳态窗口与 VO₂ 资格；另有外部 VO₂ 同步／投影 | 原文 §6 的 RAI 估计器及跨 Workout 汇总、敏感性区间等整条主路径 |
| M5 日常数据→baseline→Recovery | **有候选实现，但不完全等同原规格** | SDNN／RHR／Sleep 独立只读同步、anchor／delete、28 日 median／MAD baseline、Recovery | 夜间／静息上下文 SDNN、睡眠规律性、自报睡眠目标、持续偏离提示等未按原文实现 |
| M6 集成与受控发布 | **部分实现** | 真正 Worker、原子发布／失效／fencing、日界刷新、Go／Swift typed contract、CLI 候选快照 | 正式 HTTP／UI、计划消费者、新模型激活／回滚执行器、统一六个月回算闭环仍缺 |
| M7 现实校准 | **未完成** | UA2／UA3／UA4 研究与产品验收计划 | 现实资格研究、纵向留出验证、精度／适用性结论、影子运行、激活验收 |

**阶段编号已发生变化。** 后续仓库的 M4 是 `External VO₂max`，并非原设计 M4 的自有条件估计；后续 `M7_RESULT.md` 是产品合同工程集成，并非原设计 M7 的现实校准。因此“已有 M4／M7 结果文件”不能据此销项。

## 3. 尚未实现或未接完的功能

### A. 主功能及产品闭环

| 编号 | 原文要求 | 当前缺口与影响 | 实现证据 |
| --- | --- | --- | --- |
| G01 | §6 RAI 条件式 VO₂max | **未实现估计器**。没有 `3.5+0.2v/r` 的模型路径、42 日鲁棒池、跨日平滑、独立证据 cap、异常二次确认、留一范围及 8 角点敏感性区间。外部 Apple 值不补这个缺口。 | [外部模型明确只选择已有事实](../../apps/server/internal/vo2maxexternal/model.go)、[算法注册](../../apps/server/internal/analysis/model.go)、[Worker kinds](../../apps/server/internal/jobs/worker.go)、[M4B3 结论](../M4B3_RESULT.md) |
| G02 | §16 新指标正式只读 API | **没有正式新模型 HTTP 入口**。原文拟议 `/v2/analysis/athlete` 未注册；后续 `/v2/athlete-model` 也未开放。有内部 typed snapshot、schema、管理员 CLI，不等于手机可请求。 | [实际路由](../../apps/server/internal/httpapi/server.go)、[内部 reader](../../apps/server/internal/storage/postgres/athlete_model.go) |
| G03 | §1／16 跑力＋恢复首页、VO₂ 来源对照详情 | **新模型 UI 尚未接入**。Swift Codable 已有，正式 ProductAPI 仍请求旧 overview／state。新指标的中文展示、缺失／过期状态、原因说明、Apple／RAI 对照及产品无障碍验收未闭环；当前也没有 RAI 值可对照。 | [Swift typed DTO](../../ios/RAI/Sources/ProductCore/AthleteModel.swift)、[正式产品请求](../../ios/RAI/Sources/ProductCore/Product.swift)、[M7 消费者与 UI 边界](../M7_RESULT.md) |
| G04 | §17 candidate→active、回滚 | **只有只读计划，没有执行器**。active 数据库约束仍限 legacy；promotion plan 全部不可发布。不能直接把新结果用于正式产品，也不能把“旧 active 未变”当成已实现新模型切回。 | [active CHECK](../../apps/server/migrations/00021_workout_feature_substrate.sql)、[promotion plan](../../apps/server/internal/athletemodel/promotion.go)、[CLI 只允许 inspect／plan](../../apps/server/internal/admin/model_stack.go) |
| G05 | §17.2 六个月全栈 candidate 回算及变更预览 | **尚无统一编排闭环**。已有各模型有界 compute／recompute／inspect／history 和 M2 范围重算，但不等于显式 stack／日期范围／warmup／dry-run／可恢复分段／全模型差异预览的一体流程。M3 的 recompute 走当前有界窗口，CLI 不接受 from／through；M2 超预算 dirty span 无自动分段修复器。 | [M3 命令](../../apps/server/internal/admin/performance.go)、[旧通用重算仅认 legacy 算法](../../apps/server/internal/admin/analysis.go)、[M2 容量限制](../M2_RESULT.md) |
| G06 | §4／21 详情统一读取已发布共享特征 | **只完成少量共同原语抽取**。正式 detail 仍加载 raw 并按旧规则请求时计算，未切到 M1 artifact；新旧速度、HR 支持和展示规则仍有不同语义。 | [detail loader](../../apps/server/internal/storage/postgres/workout_detail.go)、[detail 计算](../../apps/server/internal/workoutdetail/calc.go)、[重复审计](../M1_FEATURE_DUPLICATION_AUDIT.md) |
| G07 | §8／21 训练计划显式支持新模型版本 | **消费者仍是旧版**。生成器只识别旧 fitness_fatigue／readiness，不消费 `rai.readiness.training/v1`、新负荷或跑力；缺新模型的版本升级及不确定性传递。 | [训练生成器](../../apps/server/internal/training/generator.go)、[M7 consumer 隔离说明](../M7_RESULT.md) |
| G08 | §7.3／16 同版本 28 日趋势及历史展示 | **有比较纯函数，没有新产品历史读取闭环**。当前 M7 reader 不加载历史；跑力 trend 保持 no_comparable_result，Recovery 也没有接入历史比较。原模型历史 artifact／CLI 已存在，不应误记成完全没有历史。 | [投影及趋势函数](../../apps/server/internal/athletemodel/projection.go)、[当前快照 reader](../../apps/server/internal/storage/postgres/athlete_model.go) |

### B. 模型能力的剩余细项

| 编号 | 原文要求 | 当前缺口与影响 | 实现证据 |
| --- | --- | --- | --- |
| G09 | §5／6／15 可信 HR reference 及 baseline RHR 接入 | 当前只解析显式、版本化 `hr_reference`；**日常 RHR baseline 自动供给 Workout reference 未接通**。HRmax≤365 日、显式 RHR≤90 日的各自测量年龄规则也未完整编码，当前主要依赖人为给定有效期／provenance。不是完全没有个人参数机制。 | [参数解析](../../apps/server/internal/workoutfeatures/normalize.go)、[loader 只查 hr_reference](../../apps/server/internal/storage/postgres/feature_input.go)、[参数管理](../../apps/server/internal/admin/features.go)、[Feature 合同参数限制](../WORKOUT_FEATURE_CONTRACT.md) |
| G10 | §9.1 夜间／静息上下文 HRV | **当前为同 epoch 的全天 SDNN 中位代表值**，并保留时段计数；没有按夜间 00–06、睡眠／静息交集、至少两次观测实现原文 estimator。后续合同已明确改口径，不能称原规格完成。 | [quantity 日聚合](../../apps/server/internal/recovery/model.go)、[后续 Recovery 合同](../RECOVERY_FOUNDATION_CONTRACT.md) |
| G11 | §8.2／9.2 睡眠规律性、自报目标、持续偏离 | **睡眠中点／圆周时间规律性项 deferred**；当前睡眠 deficit 相对历史时长 baseline，没有自报目标输入。未见“连续第 3 日 persistent_deviation”的状态逻辑。主睡眠并集、跨日、nap 分离及稳健 baseline 已实现。 | [Recovery 计算](../../apps/server/internal/recovery/calculate.go)、[睡眠分段](../../apps/server/internal/recovery/sleep.go)、[duration-only config](../../apps/server/internal/recovery/model.go)、[M5B 差异说明](../M5B_RESULT.md) |
| G12 | §7.4 长跑 decoupling／pace_decay 与 42 日趋势 | **只完成特征前置支持**：最长连续时间、去热身后的前后半速度／HR／质量。没有正式 EF decoupling／pace_decay 诊断输出和至少 3 次同类长跑的 42 日中位趋势。γ 和 v60/v20 endurance 已实现，不能一起记成没做。 | [LongRun 结构](../../apps/server/internal/workoutfeatures/model.go)、[ExtractLongRun](../../apps/server/internal/workoutfeatures/windows.go)、[M3 范围](../M3_RESULT.md) |
| G13 | §12 同时间重叠 Workout 保护 | **未见跨 UUID 的重叠训练识别、possible_duplicate 标记及重叠负荷隔离**。当前 load state 只按 WorkoutID＋day 去重，对不同 UUID 的已知贡献求和；其输入 ledger 不带训练起止，不能在那里完成重叠判断。此项为源码差距，未在本轮数据库或真机复现。 | [state 聚合](../../apps/server/internal/trainingload/state.go)、[LedgerLoad 输入](../../apps/server/internal/trainingload/model.go)、[load loader](../../apps/server/internal/storage/postgres/load_input.go) |
| G14 | §12／18 设备切换与误标专项 | 有 source epoch／来源冲突、速度和路线异常过滤；**未见完整“新设备固定偏差→context shift→进步暂缓”专用监控**，也未见结合高速度、步数支持的 `activity_mismatch_suspected` 多信号分类。保留来源字段及通用异常隔离不等于这两项已完成。 | [信号过滤](../../apps/server/internal/workoutfeatures/signal.go)、[跑力 evidence](../../apps/server/internal/performance/evidence.go)、[跑力计算](../../apps/server/internal/performance/calculate.go) |

### C. 原文明确可选／未来的能力

下列确实未实现，但原文没有要求它们阻塞 V1 主路径，不应与 G01 等主缺口混为一谈：

| 功能 | 当前状态 | 证据 |
| --- | --- | --- |
| §7.2 maximal_supported、比赛／接近最大努力确认 | 只有 demonstrated；MaximalSupported 固定 unavailable。缺版本化 effort annotation 与确认流程。 | [模型 Mode](../../apps/server/internal/performance/model.go)、[结果初始化](../../apps/server/internal/performance/calculate.go) |
| §7.5 CS／D′ 研究模型 | 有 3／5／10 分钟窗口与 CS 资格，没有 CS／D′ 拟合／留一诊断／handler。 | [Feature CS 资格](../../apps/server/internal/workoutfeatures/compute.go)、[Worker 注册](../../apps/server/internal/jobs/worker.go) |
| §8.4 load_settling_window | 没有候选分桶输出；现有 readiness 的 36h 衰减不等于该功能。原文禁止的精确“身体恢复小时数”也没有必要补做。 | [Readiness 输出](../../apps/server/internal/readiness/model.go) |
| §6.3 aerobic_efficiency fallback | 未见独立同上下文 v/HR 趋势输出；有 mean speed／HR 原料，不等于提供了该诊断。 | [Feature 输出](../../apps/server/internal/workoutfeatures/model.go)、[M7 输出](../../apps/server/internal/athletemodel/contract.go) |
| §7.1／7.5 Economy、CP、独立实测阈值、核验跑道／跑步机距离 | 保持 future／unsupported；未实现对应独立证据通路。属于设计保留项，不是用功率／跑姿现有字段就能销项。 | [逐模型资格](../../apps/server/internal/workoutfeatures/compute.go)、[原文 §7](../RAI_MODEL_ARCHITECTURE.md) |

## 4. 已实现但与原文不一致的规则

这部分不是“没有实现”，而是**后续规格取代或收窄了原设计**。本轮只记录差异，不自行判断需要回退或修改公式。

| 项目 | 原文 | 当前实现／后续合同 |
| --- | --- | --- |
| Readiness 负荷残余窗 | 最近 14 日 | 180 日有限窗，36h 衰减；有省略尾部误差证明，但 unknown／partial 历史阻断范围更长 |
| Readiness 个人尺度 B | 周一冻结，使用之前第 8–49 日，至少 10 个训练日 | 前 42 个完整当地日的非零负荷中位数，至少 5 日、跨度≥14 日，成熟后设 1 AU floor；没有原文每周冻结 |
| HRV 代表值 | 夜间／静息上下文 | 全天同 epoch 的 SDNN 日代表值，保存四时段计数并降低未知上下文支持 |
| HR 异常规则 | 220 先结合局部中位／MAD、持续性和参数判断；默认域 30–240 | M1 候选支持域 `[30,220)`，≤5s 且跳变>30bpm 的连接排除；更保守，不能说照原文实现了局部 MAD 判别 |
| HRmax 冲突容忍 | 超过 1% active 才使完整 load 不可用 | M2 零容忍：任何正时长冲突都会令完整 load 不可用 |
| confidence | §10 建议统一组成与 freshness 参数 | M3 用自己的 q 乘积、成熟度和 45 日 freshness；M5B／M6 有单独 policy。cap／独立证据原则保留，但不能把原始统一公式当作当前算法说明 |

证据：[Readiness config](../../apps/server/internal/readiness/model.go)、[B 计算](../../apps/server/internal/readiness/calculate.go)、[后续 Readiness 合同](../TRAINING_READINESS_CONTRACT.md)、[Feature signal](../../apps/server/internal/workoutfeatures/signal.go)、[负荷合同](../TRAINING_LOAD_V1_CONTRACT.md)、[M3 config](../../apps/server/internal/performance/model.go)。

## 5. 已做工程不能代替的验收

1. **真实完整性与普通路径仍缺闭环。** completeness v2 客户端、服务端验证、回执及 M6 消费已实现，不能继续说“完全没有 transport”。但 App 目前调用 `syncWorkoutCompleteness` 的入口位于验收流程；普通同步尚未自动接入全量证明。11 月 6 日第一轮真机完整性尝试未完成；第二轮真实测试主要是同步时延／资源与恢复，不构成全量完整性及 M6 复验通过。[实际调用](../../ios/RAI/App/RAIApp.swift)、[协调器](../../ios/RAI/App/SyncCoordinator.swift)、[第一轮结果](../WORKOUT_SYNC_SPEED_RESULT.md)、[第二轮结果](../WORKOUT_SYNC_SPEED_ROUND2_RESULT.md)。
2. **有真机测试，不等于模型真机验收全过。** 近期有真实只读 Workout／Daily／外部 VO₂ 路径观测，早期笼统 DEFERRED 已不能描述全部现实；但完整 kill／后台／自然删除等矩阵、各模型输入覆盖及稳定性仍未全面闭环。不能把缺资料／缺参数造成的 null 当作算法未实现，也不能反过来算模型可用性已通过。[UA1](../UA1_RESULT.md)、[UA1-R](../UA1_R_RESULT.md)、[后续来源策略结果](../UA1_R3B_RESULT.md)。
3. **原文 M7 现实校准仍未完成。** 未见 UA2 可用性研究、UA3 纵向留出验证结果、跑力预测误差报告、自有 VO₂ 实验室 criterion 或 Recovery／Readiness 独立 outcome 验证。研究计划不能代替结果。[可用性计划](../DATA_AVAILABILITY_STUDY.md)、[校准计划](../MODEL_CALIBRATION_PLAN.md)。
4. **影子／产品激活／回滚演练未完成。** 包含 UX／中文原因／accessibility／privacy／性能、limited activation、实际 rollback exercise。现有计划和 schema 不代表这些流程已交付。[产品计划](../PRODUCT_ACTIVATION_PLAN.md)。
5. **性能仍有未达标。** 11 月 6 日第二轮同步结果明确保留服务端和真机时延、密集记录 RSS 的 FAIL；这不是本轮新测量，也不能改写成模型功能缺失，但影响真实数据闭环及最终产品验收。[最新性能结果](../WORKOUT_SYNC_SPEED_ROUND2_RESULT.md)。

## 6. 本轮实际验证与边界

- 本机执行 `bash scripts/local/test.sh gate`：**PASS**，elapsed 2s。包含 `go test -race ./...`、`go vet ./...`、依赖边界及本机控制器安全检查。Go 测试使用正常缓存；不称非缓存全量回归。日志：`.local/logs/model-design-audit-20261106/gate.log`。
- 本机非缓存定向执行 `bash scripts/local/test.sh unit -count=1 ./internal/workoutfeatures ./internal/trainingload ./internal/performance ./internal/vo2maxexternal ./internal/recovery ./internal/readiness ./internal/athletemodel ./internal/analysis`：**8 个包全部 PASS**，elapsed 6s。日志：`.local/logs/model-design-audit-20261106/model-tests.log`。
- 已核对 M1／M2／M3／Recovery／Readiness／AthleteModel 真实 PG 集成测试源码，以及后续阶段 PG 运行记录；这些属于既有实现／历史运行证据。**本轮没有重跑 PostgreSQL、Swift、Simulator 或真机**。本轮仅新增审计文档及实施记录，无事务、协议或可执行代码变更。
- 未访问生产／远端数据库、未读取健康记录、未执行 migration、未改公式／门槛／授权、未部署／激活／切旧入口，未修改原设计、原架构方案或 `../running`。

## 7. 建议的剩余工作次序

1. 先确认“原规格”与后续冻结合同的差异，以后续实施合同为准还是另立新版本；不能静默修改现有 v1。
2. 完整性普通路径和模型输入资格／HR reference 闭环，连同重叠训练、设备 epoch 偏移专项核查，先解决现实中为什么无值。
3. 单列自有 RAI VO₂max 项目；已有 external VO₂ 分支继续独立。
4. 补历史趋势、共享详情、新指标 API／UI／计划消费者，以及六个月回算、受控切换／回滚执行器。
5. 按独立授权执行现实校准和产品验收，达到门槛后才激活。CS／D′、最大努力模式、经济性等可选项不应阻塞已定义的 demonstrated V1。

本次核查不自动启动上述实施、真实数据研究或产品激活。
