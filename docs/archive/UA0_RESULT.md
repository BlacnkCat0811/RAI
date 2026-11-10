# UA0 Result

日期：2026-11-05（Asia/Shanghai）。**UA0 RESULT: PASS WITH RISKS**。本结果仅针对验收合同/计划冻结，真实设备与真实模型结果 **NOT EXECUTED**。未实施新功能、未修改模型或参数、未执行真机/HealthKit、未激活产品。

## 1. Git Baseline

```text
git status --short       → 空，入口工作区干净
git rev-parse HEAD        → 9a8839dff0f58fe93ffc7297e50490c0c88279cc
git branch --show-current → main
git diff --stat          → 空
```

本轮只新增六份 Markdown 合同/计划/报告，并按工程约定追加 `docs/IMPLEMENTATION.md`。原方案、全部既有源码/模型/config、migrations/checksums 和旧证据不变；`../running` 未修改。没有 reset/restore/clean/stash/rebase/commit/push/deploy。

## 2. Current Candidate Stack

M1 Workout Features、M2 Training Load、M3 Running Ability、M5B Recovery、M6 Readiness、M7 Product Contract 均保留 **PASS WITH RISKS**；M4 External VO₂max engineering **CLOSED**（B4 PASS WITH RISKS）。具体身份与历史证据见 [总合同 §2](UNIFIED_ACCEPTANCE_CONTRACT.md#2-当前-candidate-stack)。active=legacy、candidate-only、五个 promotable=false，最高 migration 27。RAI 自有 `rai.vo2max.estimated/v1` 仍 NOT IMPLEMENTED，无综合分。

## 3. Acceptance Layer Model

独立冻结 A Engineering、B Device/Data（UA1+UA2）、C Model Calibration（UA3）、D Product（UA4）。**Engineering PASS ≠ Real-device PASS ≠ Physiological Validation ≠ Product Activation**；各层独立证据/判定/授权。合同见 [UNIFIED_ACCEPTANCE_CONTRACT.md](UNIFIED_ACCEPTANCE_CONTRACT.md)。

## 4. Real-Device Acceptance Contract

[UA1 计划](REAL_DEVICE_ACCEPTANCE_PLAN.md) 覆盖目标 iPhone 12 Pro Max + 实际可用 Apple Watch，OS/build 实施时实录；Workout discovery/samples/route/delete/revision/anchor/lost ACK/relaunch、Daily SDNN/RHR/Sleep、External VO₂来源/metadata、后台、五点 kill、四种离线场景。无自然事件记 NOT OBSERVED，不补造数据或主动修改健康数据。

M6 **真实 Workout all-stream catch-up completeness** 为高优先级前置缺口：现协议无服务器可验证 transport，syntheticCoverage 不得用于真实验收；当前普通 candidate 可保持 sync_incomplete。缺实现/可观察 checkpoint 就阻断对应 case，不以测试计划冒充已交付能力。

## 5. Data Availability Study

[UA2 计划](DATA_AVAILABILITY_STUDY.md) 逐模型定义 athlete/athlete-day/Workout 分母、完整/partial/缺失、来源、冷启动、资格、coverage 和 abstention；纳入无值用户和延迟/版本。执行前预登记目标 cohort、观察窗口、精度和 acceptable availability，空缺 gate 关闭。多数用户长期 unavailable 先 root-cause review，不临时放宽 v1 qualification。

## 6. Training Load Calibration

冻结 v1，研究 HR 参数质量、repeatability、same-session consistency、interval behavior、missing-HR bias 与 subsequent physiological response；不声称 RAI_AU=生理疲劳。无同尺度真值不制造生理 MAE。见 [UA3 §3](MODEL_CALIBRATION_PLAN.md#3-training-load-calibration)。

## 7. Running Ability Calibration

比较已冻结 prediction v60 与后续独立持续表现/另行获准的 time trial 或 race；不同 duration 用独立标签，不循环生成真值。评估 bias、MAE、RMSE、coverage、abstention 和人内/人间误差；demonstrated 不等于 maximal。见 [UA3 §4](MODEL_CALIBRATION_PLAN.md#4-running-ability-calibration)。

## 8. Recovery Calibration

纵向 HRV/RHR/Sleep deviations 对未来主客观结果；独立 outcome、source epoch/28d baseline/缺失分层，不以几个“感觉差”案例验证。保持 personal-baseline physiological recovery proxy，无疾病诊断。见 [UA3 §5](MODEL_CALIBRATION_PLAN.md#5-recovery-calibration)。

## 9. Readiness Validation

训练前冻结 score 对 subsequent training response，保留 running-focused、coverage、baseline 与 unknown-load 限制；不以未来 Workout 回填历史，不预测 injury/illness。split by athlete 且 past→future；current revised 管理重算不能冒充 as-known prediction。见 [UA3 §§2、6](MODEL_CALIBRATION_PLAN.md)。

## 10. External VO₂max Comparator Policy

Apple Health estimate 仅 external comparator，非 RAI ground truth/唯一标签。descriptor 非硬件认证；registry 未支持字段 fail closed，不删字段。未来自有 VO₂max 需要 laboratory gas-analysis VO₂max/VO₂peak 协议、定义和标签；当前 NOT IMPLEMENTED。见 [UA3 §7](MODEL_CALIBRATION_PLAN.md#7-external-vo₂max-comparator-与-laboratory-policy)。

## 11. Shadow Read

UA1/UA2/UA3 达到各自门禁后才运行；legacy active，candidate 不影响 UI/plan/Coach/notification。只报脱敏 availability/state/difference/latency/failure aggregates，不逐人记录 score；不同 legacy/new 语义不能直接相减。Shadow 本轮 NOT STARTED。见 [UA4 §2](PRODUCT_ACTIVATION_PLAN.md#2-shadow-contract)。

## 12. Product Activation

最低门禁为 engineering、real-device、availability、model-specific validation、真实 product UX、privacy、performance、实际 rollback，另需对具体版本/cohort 的明确 activation authorization。missing UX 六类和五项产品文案已冻结，不补 0/100、不医学化。limited activation 仅可在 shadow 接受后另行设计。当前 **NOT AUTHORIZED**。见 [UA4 §§3–6](PRODUCT_ACTIVATION_PLAN.md)。

## 13. Rollback

未来实际演练 candidate active → previous active，包含真实消费者、in-flight/concurrent publication、恢复预算、raw/history/receipt 保留；不依赖 migration rollback、备份恢复或全量重算。M7 只有 plan，执行器与演练未完成，`rollbackReady` 不能当 true。当前 **NOT EXECUTED**。见 [UA4 §7](PRODUCT_ACTIVATION_PLAN.md#7-rollback-exercise)。

## 14. Privacy

真实数据不进 git fixture。普通日志不输出健康值/分数、sleep times、Workout/HealthKit UUID 或 device identifiers。逐个身份/回执核对在独立受控环境；公开证据只脱敏 aggregate/一致性结论，小样本抑制。research export 必须另立 consent/minimal/pseudonymized/access-controlled 协议，不复用生产日志；UA0 不实现。

## 15. Evidence Structure

未来仓库相对路径冻结为 `evidence/acceptance/{device,availability,calibration,shadow,product}/<alias>/`，每层独立 manifest、scope、版本、窗口、expected/observed、状态、review 和 gate。当前不创建空壳 PASS 或真实验收结果。

本轮实际完成（仅 synthetic、本地 Go，无数据库连接）：

| 检查 | 结果 | 本地日志 |
| --- | --- | --- |
| `go test -count=1 ./...` | PASS，exit 0 | `.local/logs/ua0/go-test.log` |
| `go test -race -count=1 ./...` | PASS，exit 0；非缓存重跑 | `.local/logs/ua0/go-race.log` |
| `go vet ./...` | PASS，exit 0 | `.local/logs/ua0/go-vet.log` |
| `python3 scripts/check-boundaries.py` | PASS，258 packages；含领域/reader/legacy consumer 边界 | `.local/logs/ua0/boundaries.log` |

工具链沿用 `scripts/local/lib.sh` 的工程本地 Go 环境。日志为本地忽略文件，不提交测试运行产物。文档审查 PASS：六份新文档 47 个相对链接/锚点、240 个表格行、围栏、18 节报告和 54 项要求追踪；HEAD/暂存区不变，tracked diff 仅 IMPLEMENTATION，untracked 仅六份文档，原方案 SHA-256 不变，`git diff --check` 通过。结果保存在 `.local/logs/ua0/document-review.json`；首次检查器将新文件 diff 的正常 exit 1 误判为失败，修正检查器后通过，首次记录保留，并无文档空白错误。没有事务或可执行代码变更，**本轮未重跑真实隔离 PostgreSQL 集成、Swift/Simulator、真机、部署或生理研究**；历史 M7 数据库证据只按其原范围引用，不充当本轮结果。

## 16. Acceptance Matrix

| Capability | Engineering | Device/Data | Calibration | Product |
| --- | --- | --- | --- | --- |
| Workout Sync | PASS WITH RISKS | DEFERRED | N/A | NOT AUTHORIZED |
| Training Load | PASS WITH RISKS | DEFERRED | NOT VERIFIED | NOT AUTHORIZED |
| Running Ability | PASS WITH RISKS | DEFERRED | NOT VERIFIED | NOT AUTHORIZED |
| Daily Health | PASS WITH RISKS | DEFERRED | N/A | NOT AUTHORIZED |
| External VO₂max | CLOSED / B4 PASS WITH RISKS | DEFERRED | external comparator；NOT VERIFIED | NOT AUTHORIZED |
| Recovery | PASS WITH RISKS | DEFERRED | NOT VERIFIED | NOT AUTHORIZED |
| Readiness | PASS WITH RISKS | DEFERRED | NOT VERIFIED | NOT AUTHORIZED |

## 17. Risks

- UA0 合同审查未发现未解决架构 P0/P1；此结论仅限规划边界，不声称真实设备/生理/产品无风险。
- M6 real all-stream transport、真实 kill 边界观察、shadow/activation/rollback 执行器尚需独立工程任务；计划 ready 不表示实施 ready。M6 180d unknown/partial、M3 长时证据及严格资格、M5B 三组件/基线与 source epochs 可能使现实 availability 低。
- 真实设备/数据/来源/metadata/更新/长期离线、模型误差、population validity、UX/accessibility、部署/WAN/多用户容量均未在 UA0 验证。旧真机记录、synthetic、mock、文件存在互不替代。
- UA2/UA3/UA4 的目标 cohort/时窗/样本精度/用途相关数值门槛需在执行 addendum 中预登记；当前缺失明确关闭 promotion。不能用“合同冻结”掩盖未设定执行门槛，亦不能编造阈值声称 validation-ready。
- 数据完整性失败立即 BLOCKED；多数用户长期无值先 root-cause review；误差不接受则 v1 留历史、v2 另立，不在本轮改公式。

## 18. Decision

```text
UA0 RESULT: PASS WITH RISKS

MODEL ENGINEERING: CLOSED
PRODUCT CONTRACT: CLOSED
REAL-DEVICE ACCEPTANCE: READY BUT DEFERRED
CALIBRATION: READY BUT NOT STARTED
PRODUCT ACTIVATION: NOT AUTHORIZED

REAL DEVICE ACCEPTANCE: DEFERRED
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
REAL DEVICE / REAL MODEL RESULTS: NOT EXECUTED
```

READY 仅指验收框架/计划就绪，执行前置与预登记仍受硬门禁约束。UA0 到此停止；没有自动启动 UA1，等待用户明确恢复真实 iPhone/Apple Watch 验收条件。
