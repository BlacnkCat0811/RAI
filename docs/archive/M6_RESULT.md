# M6 Result

**M6 RESULT: PASS WITH RISKS** — 2026-11-04。仅 synthetic / 隔离 PostgreSQL 18；candidate-only。没有产品激活、部署、提交、推送或真实数据迁移。冻结定义见 [Training Readiness Contract](TRAINING_READINESS_CONTRACT.md)，真实证据见 [M6 evidence](evidence/m6/README.md)。

## 1. Git Baseline

`main`，HEAD `380a6fe96d13d1e57f69ceac50e52e72908af2b8`。入口已有 M4/M5B 未提交工作：18 个 tracked 文件，376 insertions / 31 deletions，以及 untracked 实现和证据。最高 migration **26**；本轮增加 **00027_training_readiness.sql**，runtime schema gate=27。基线四项命令和 40 个冻结文件 SHA 记录见 [baseline](evidence/m6/baseline.json)。原方案、M2/M5B 领域源文件、migration 1–26 与既有 checksum 条目保持不变；唯一已有 M5B migration 测试适配为检查 latest，数值语义未变。没有执行 reset/restore/clean/stash/rebase/commit/push/deploy，也未修改 `../running`。

## 2. Readiness Definition

`rai.readiness.training/v1` 是基于当前可见 Recovery 证据与已记录完成跑步负荷的 **instant running training readiness** 工程候选。不是疾病、伤病/过度训练诊断、比赛预测、训练计划、运动能力或 VO₂max；100 不表示适合最大强度、无风险或医学安全。

## 3. Model Boundary

Recovery=身体恢复证据；Training Load=已记录跑步训练刺激；Readiness=Recovery×近期负荷 context。M2/M5B 各自公式、基线、权重、confidence、τ7/τ42 均未修改。M3、M4、计划、pace/power/distance、原始 HRV/RHR/Sleep、HR 与 E7/E42/balance/ratio 没有进入 M6 数值。`internal/readiness` 仅标准库、无 DB/HTTP/文件/系统时钟/random。真实 product DTO 和依赖图边界已检查。

## 4. Physiological Recovery Input

直接读取当日 clean M5B candidate 的 run/hash/generation、完整 score、confidence、component coverage 和 freshness identity，按 M5B 原有 Freshen 计算证据年龄。仅 available+complete+nonnull+fresh 可用于完整 Readiness；autonomicOnly、collecting、source_conflict、缺失和过期均不补值。M6 不解析任何 M5A raw 数据。实际 M2 参数修订后 M5B artifact 身份、hash、P 保持不变。

## 5. Recent Load Burden

`D=Σ Li exp(−(asOf−end_i)/36h)`，Li 只取当前有效 M2 artifact 与正式 daily ledger；结束时间衰减、跨午夜整次贡献一次、多个 Workout 分别衰减。未来 Workout 不进入 D，也不改变 B。

τ=36h 为工程假设。有限窗口取 **180 absolute days**，而非无法无条件证明尾误差的 7 天。M1 最大 24h、M2 HRR≤1，因此有效单次 AU<10,000；结合最多 2^128 个不同 UUID，可得 omitted tail≤`2^128×10000×exp(−120)=2.609165435×10⁻¹⁰ AU`，最大 score 截断误差≤`1.808535666×10⁻⁸`。无 lifetime count/scan，无每日运动量假设。证明与参数均进入合同/artifact。

## 6. Personal Load Baseline

B=前 42 个完整 civil days 的非零、complete、compatible daily ledger 总量 median；今天不进入自身 B。至少 5 training days，首末跨度≥14 civil days，最新有效训练日距今≤14 days。保留 known-zero/unknown 日数、training days、span、median、freshness、floor flag。1 AU floor 只应用于已建立的真实 median，不给新用户默认典型负荷。过旧历史、新运动员及不足基线返回 collecting。离群、持续 step、DST、跨午夜和 unknown≠zero 已验证。

## 7. Readiness Formula

```text
loadMultiplier = exp(−ln(2) × D/B)
Readiness = P × loadMultiplier
```

内部保留 float64；D=0→P，D=B→P/2，D=2B→P/4；始终 `0≤Readiness≤P≤100`。例如显式 synthetic P=82、D/B=0.6，multiplier≈0.659754，Readiness≈54.0998。artifact 可直接解释 P、D、median/floored B、D/B、multiplier 与输出，无重复 acute/chronic 扣分。

## 8. Missing / Unknown

Recovery null 不补 100；unknown/partial/incompatible M2 AU 不补 0。已知但尚无 current ledger 的 Workout 使近期 burden unknown，相关完整历史日 unknown；不同模型/单位/config/calendar 被拒绝。合成伪造 `legacy_TRIMP` 单位的 M2 artifact 真实落库后，M6 仍无 score/source_conflict。无训练仅表示 received recorded running load=0，不证明休息或全身没有负荷。

服务器既有 Workout 协议没有 all-stream catch-up assertion。普通 CLI 不声明覆盖，因此保持 sync_incomplete。数值工程验收使用显式 syntheticOnly、时间范围、identity 与完整当前 Workout stream manifest hash 的版本化 fixture feature；没有线上 setter，不伪装 real completeness。

## 9. Freshness

Artifact 存 explicit asOf/capturedAt；reader 使用独立 evaluationAt。Freshen 只返回 projection，重验依赖后衰减 D，不 UPDATE immutable artifact，不改善 P。到最早 Recovery component/coverage 过期、load coverage 过期、下一 civil day 或已知 future Workout 结束边界即清空 current score。72h 时间推进验证的是固定 P/B 数学 oracle；真实 current 输出遵守更早的证据/day 过期门禁，不能无限回升到 P。

## 10. Confidence / Quality

Confidence≤min(M5B support, independent load support,0.55)，不是概率。Load support 基于已知覆盖、可用独立训练日和 history span；不按 AU 或 Workout 数量奖励 confidence。质量 complete 仅表示 V1 工程所需输入齐全。M6 不生成 partial readiness；partial Recovery/Load 仍 null。Recovery coverage、load knownness 和 baseline maturity 独立保存。

## 11. Dependency Graph

M2 workout load→M6；current-day M5B recovery→M6；versioned load coverage→M6。无 M3/M4/raw daily/raw HR→M6 数值边。每次 artifact 在受限 definer trigger 内从 bounded refs 建立依赖，Worker 无通用 graph-write 权限。已有持久依赖即使刚跨出窗口，也得到可运行新 generation，不会只 dirty 而无 job。旧 M5B future-readiness placeholder 仍不会自行启用 M6。

## 12. Persistence / Publication

复用 analysis_runs/results/artifacts/publications/model_dependencies/dirty_scopes/jobs/attempts。一个 run 原子写 versioned M6 artifact、scalar、publication、clean、run/job/attempt terminal；无可变 score-current 表、独立 burden 状态或 duplicate raw。≤4096 workouts、≤128 current Workout streams、≤2 MiB artifact；day/calendar 和 config/assignment 身份冻结。所有结果 candidate-only，active/legacy `readiness/v1` 和 training generator 不变。

## 13. Transaction / Fencing

REPEATABLE READ 稳定读 M5B pointer、M2 ledger、config、coverage、assignment。dataset→job→dirty 锁序下有界 reread，重新核验 P run/hash/generation、M2 run/ledger/revision/delete、coverage/manifest、M2/M6 config/assignment、dataset/collector/device、generation、lease/fencing token 和原 run snapshot hash；独立重算拒绝伪造输出。变化返回 SUPERSEDED，无旧 artifact 发布。

真实 fault 覆盖 snapshot/run capture、artifact、result、publication、dirty clean、run/job/attempt terminal；三处真实 pg_terminate_backend 在 after artifact / before publication / before clean，证明无部分提交，原 attempt 可安全重放。受限 API 原始 Workout 删除事务失败时，raw delete/dirty/pointer 同回滚；正式删除立即撤销 current。

## 14. PostgreSQL

真实 **PostgreSQL 18.6**，新建隔离 Unix-socket cluster，fsync/full_page_writes/synchronous_commit/checksums 开启。empty→27、26→27、repeat migrate、schema gate、roles、fault/crash、snapshot/fencing 验收。没有读取生产数据库。最终全量 PG 状态：**PASS（integration 957.597s；最后门禁另作专项复验）**。成功证据记录 leftover databases=0、cluster removed=1。

## 15. Golden Athlete / Properties

9 个纯领域顶层测试；14 个 M6 PG 顶层测试加 1 个 migration 测试（含多分支）。覆盖 well/poor recovery×no/heavy load、moderate/typical、new、returning、missing Recovery、unknown load、多次、late/delete，共至少 12 类 Golden 场景。独立 decay/score oracle、2000 次随机单调性/上界、permutation、未知不可改善、cross-midnight、outlier/step、floor、DST、未来边界、stale 和 source/unit conflict 已执行。

18 步确定性随机 insert/late/delete/load revision/Recovery revision+withdrawal/assignment 变化后，incremental 与同 asOf/evaluationAt bounded rebuild 的 semantic state 一致。真实 M2 effective parameter revision 改变 AU、D、B 并生成新 M6，M5B identity/value 不变。M3 assignment 与真实 external VO₂max facts 不改变 M6。同样保持旧 artifact 不可变。

## 16. Performance

全部 **5 histories×5 densities=25** 组合实际运行，含 snapshot、decay、baseline、formula、serialization、publication、reader、Freshen、allocations、bytes；详见 [完整 JSON](evidence/m6/performance.json) 和 [运行输出](evidence/m6/performance.txt)。以下为每组 100 recent entries 的单次观测，含 race detector，且同机另有回归运行；不是生产 SLO。14d 正确处于 load-baseline collecting。长期历史夹具为 sparse，180/365d 的 bounded input 均为 111 条。

| History | Recent entries | Input workouts | Snapshot ms | Compute ms | Publish ms | Reader ms | JSON bytes |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 14d | 100 | 102 | 101.047 | 5.576 | 138.480 | 103.367 | 41,186 |
| 42d | 100 | 106 | 83.408 | 4.260 | 159.880 | 98.014 | 42,771 |
| 90d | 100 | 108 | 90.709 | 4.464 | 122.031 | 92.433 | 43,537 |
| 180d | 100 | 111 | 74.322 | 4.205 | 127.102 | 93.440 | 44,683 |
| 365d | 100 | 111 | 84.892 | 4.289 | 143.217 | 113.805 | 44,679 |

独立 Go benchmark（100 条额外输入）calculate≈0.311ms /142,172B /482 allocs，serialization≈0.083ms，Freshen≈1.66µs /32B /4 allocs；见 [Go checks](evidence/m6/go-checks.txt)。PG JSON bytes 为序列化 artifact，不冒充 jsonb storage size。baseline/formula/decay 的 ns 级单次阶段受计时开销影响，以完整 benchmark 为辅助。

[EXPLAIN evidence](evidence/m6/query-plan.txt) 在明确 `enable_seqscan=off` 下证明 end-time 两个界进入 `readiness_workout_end` index condition；这是索引能力验证，不宣称稀疏小表默认 planner 或生产密集分布已验收。代码查询始终有上下界和行数上限；不读 lifetime Workout/daily health。Reader 当前逐 run 验证 ledger，密集规模仍需后续容量研究。

## 17. Regression

`go test ./...`、`go test -race ./...`、`go vet ./...`、integration vet、boundary、sqlc regeneration、diff check 通过；40 个冻结文件和原 checksum 条目不变。全量 PG18/race：**PASS（integration 957.597s；最后门禁另作专项复验）**；最后单位/future baseline 门禁另有 M6 专项 PG18/race 与 Go/race 复验，最终见 validation.json。

Home/Coach 所用 overview、history/Metrics、workouts/detail、Training Plan、generic training-state DTO 不读 M6；training generator 原输入不变。没有 Swift/DTO/UI 源码修改。完整 `swift test --package-path ios/RAI` 已尝试，因 CommandLineTools 缺 `Testing` module 失败；不能称 Swift full regression 或 Simulator UI 通过。DEVELOPER_DIR 未设置、xcode-select 指向 `/Library/Developer/CommandLineTools`；该问题不阻塞 backend-only 工程。

## 18. Privacy

普通 worker 日志和默认 admin inspect 仅 status/reason/count/hash/duration；无 Recovery/Readiness/AU/raw health/full UUID。inspect 数值字段缺席已测试。所有测试均 synthetic；numerical artifact 仅显式 dataset 的 admin/测试数据库可见。公开 evidence 的 full UUID 扫描为最终 validation 记录；没有凭证输出或提交。

## 19. Risks

- **P0：** 无已知未解决项；missing→100、unknown→0、raw/M3/M4 数值污染、deleted current、old generation、candidate→legacy 等门禁均覆盖。
- **P1：** 无已知未解决项；重复扣分、B=E42、未来 Workout、0/unknown 日混用、lifetime query、score>P、expired P 继续 current 均有边界或 property 验证。
- **P2：** 生理参数/阈值/置信 cap 未校准；真实 Workout catch-up transport 尚缺，普通真实 candidate 保持 sync_incomplete。180d 保守窗口使历史 unknown/partial 阻断可用性；cycling/strength 缺失会低估 running-only 范围外负担。没有 real-device/physiological validity；Swift Testing 工具链缺失；未支持 partial readiness、历史 raw as-known 重构或非默认 config。
- **P3：** sparse+100 recent entries 不代表4096上限、多用户容量、自然 planner 或 WAN。逐 run ledger 验证的 DB round trips、更多密集覆盖分布、基线 median 的 step 响应和生产索引选择需后续研究；现有 timings 不是 SLO。

## 20. Not Verified

```text
REAL DEVICE ACCEPTANCE: DEFERRED
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
```

real Training Load completeness、real Recovery distribution、real Readiness validity、real training response、injury prediction、illness prediction、population calibration、WAN、production 均 NOT VERIFIED。完整 Swift package regression 未通过运行，本阶段没有 Simulator UI 或真机 UI 验收。无部署/旧入口切换/真实数据迁移。

## 21. Decision

**M6 RESULT: PASS WITH RISKS**。最终门禁以 evidence/validation.json 和原始测试输出为准。

Recommended Next Phase: **M7 — Model Integration & Product Contract**。仅建议；未启动 M7。
