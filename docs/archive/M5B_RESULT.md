# M5B Result

**M5B RESULT: PASS WITH RISKS** — 2026-11-04。仅 synthetic / 隔离 PostgreSQL 18；candidate-only。Readiness 未实现，未激活产品、未部署、未提交或推送。冻结定义见 [Recovery Foundation Contract](RECOVERY_FOUNDATION_CONTRACT.md)，真实运行证据见 [索引](evidence/m5b/README.md)。

## 1. Git Baseline

`main`，HEAD `380a6fe96d13d1e57f69ceac50e52e72908af2b8`。入口已有 M4-B3/B4 未提交修改：18 个 tracked 文件、306 insertions / 31 deletions，另有 untracked M4 实现与证据。最高 migration 为 **00025**；本轮新增 **00026_recovery_foundation.sql**。完整 status/HEAD/branch/stat 记录为 [M5B BASELINE](evidence/m5b/baseline.json)。保留现有成果及原架构方案；未执行 reset/restore/clean/stash/rebase/commit/push/deploy，未接触 `../running`。原 migration 1–25、M5A raw 路径/合同与模型架构文档的 SHA-256 均不变。

## 2. Recovery Definition

`rai.recovery.physiological/v1` 只表示 SDNN、source-derived RHR、主睡眠相对个人近期基线的恢复压力 proxy；不能判断原因、疾病、组织修复比例或今天是否应该跑步。Readiness 保持 NOT IMPLEMENTED。Load / Running Ability / VO₂max / Workout / Plan 没有进入纯领域输入或公式。旧 `recovery/v1` / `readiness/v1`、active channel 与训练生成器保持既有消费路径。

## 3. Daily Health Features

新增纯领域 `DailyHealthFeatures`、baseline、deviation、freshness 和 typed Recovery result；无 I/O、系统时钟或其他模型 import。使用 dataset IANA civil day；冻结 timezone、assignment、configHash 与实际 calendarHash，明确 event / first-received / captured / asOf / revision。覆盖 23h/25h DST 与换时区；换时区先撤销 current，必须显式新 assignment，历史 artifact 不重解释。features 保留 summary、quality、reasons、input hash；root artifact/lineage 保留有界 refs/hash，不复制 raw arrays。

## 4. HRV

明确为 **HRV SDNN**，不是 RMSSD、RR、PPG 或模糊的 HRV score。同 provenance/algorithm/acquisition-duration epoch 的独立 semantic observations 取 median，保留 count、civil 00/06/12/18 分布与 last event。跨 epoch、冲突 sync revisions 不平均；manual 排除。没有晨间/夜间/Apple Watch origin 的无依据推断。该 daily representative 是本阶段明确限定的候选输入，不能冒充原方案中尚未建立的夜间静息上下文估计器。

## 5. RHR

只消费 M5A 的 HealthKit/source-derived Resting Heart Rate。多个不同 live daily estimates/revisions 标冲突；不取均值，也不从普通 HR、睡眠最低 HR 或 Workout 最低 HR 重构。delete+new UUID 修订会生成新结果，旧 artifact 保留并报告 withdrawal。原 UUID、原 raw 值与重试回执不变。

## 6. Sleep

保留六类 raw interval。派生层按 source epoch sessionize：gap≤90min，最大 episode 24h，wake/end local day 归属，最长有效 episode 为 main，≤90 asleep minutes 为 nap diagnostics。Union sweep 中 awake 优先，stage 重叠只计一次，inBed/gap 不推断为 asleep；保存 continuity、known awake、unknown gap、coverage、overlap diagnostics。低于 80% known timeline 不可用；未过 episode quiet gap 的尾部不冒充完成。23→07、01→08、nap、DST、overlap、revision、换时区均有合成验证。每个历史日单独排除该日之后结束的 sleep interval，防止未来 episode 改写之前 baseline 日。没有 stage 权重或 Sleep Score。

## 7. Baseline Model

审查原 `RAI_MODEL_ARCHITECTURE.md` §9.2 后沿用 **28 天**，没有采用条件性建议的 42 天。D 仅用 `[D−28,D−1]`、同 component epoch 的可用日；至少 14 日且最近 7 日至少 3 日，14–27 provisional，28 established。SDNN 使用 log-domain median/MAD；sigma=`max(1.4826 MAD,floor)`，floor 为 log-SDNN 0.10、RHR 3 bpm、sleep 0.5h（diagnostic）。今天不参与自身 baseline。离群、noise、随机/连续缺失、source switch、late revision 和 step response 已验证。28 日 median 的新稳定水平在约第 14–15 个持续日切换，不宣称额外平滑或已验证的生理适应速度。

## 8. Recovery Formula

保留原方案 §8.2 的 max/min autonomic 权重，避免完全双扣相关信号：

```text
zH = (baseline_logSDNN − today_logSDNN) / sigmaH
zR = (today_RHR − baseline_RHR) / sigmaR
aH/R = clamp((clamp(zH/R,−7,+7)−1)/2, 0, 3)
aS = max(0, clamp((personalMedianSleepHours−todaySleepHours)/1.5,−2,+2))
Baut = 0.25 max(aH,aR) + 0.10 min(aH,aR)
Bsleep = 0.15 aS
P = 100 exp(−Baut−Bsleep)
```

保存 raw/clipped deviation、真实 max/min 分配的三项 contribution 和 burdens，可重建解释。不奖励异常“好”，不设统一 8h 或绝对短睡眠扣分。原方案的 midpoint/regularity 项因本阶段尚无可信稳定上下文而明确 deferred；duration-only policy 已进入 config identity。全部参数是 engineering hypotheses。100 仅表示模型未观察到明显 burden，不能称 medically perfect。

## 9. Missing / Partial Data

缺失不等于正常；无上传不产生 100。完整 `score` 必须三项 fresh 且各有 baseline、每 type 单一 caught-up current-collector stream。缺 Sleep 时 `score=null`、sleep deviation/burden 不存在；仅可另名返回 `autonomicOnly` partial reference，不能混成完整趋势。只有一个 autonomic component 不出完整值。collecting_baseline / available / partial / insufficient_data / stale / source_conflict 及稳定 reason codes 已实现。

## 10. Confidence / Quality

Confidence 为 evidence support，**不是概率**；独立 baseline 日数、source consistency/context、component availability 与 freshness 共同限制。完整 cap 0.60，provisional 0.45，partial reference 0.10；缺 version/device/OS context ×0.7，sleep uncertainty ×0.8。每项分别保存 event/age/24h threshold/state；支持按 72h 时间常数衰减，不改善 observed score，过期清空 current score/reference。重复事实不增加 confidence；缺失组件不抬高 full score 或 support。`complete` 只代表输入合同完整。

## 11. Persistence / Dependency

复用 analysis_runs/results/artifacts/publications/model_dependencies、jobs/attempts 与 dirty_scopes。单 run 原子发布 features / baseline / recovery 三个 immutable、versioned、lineaged artifacts，各≤2 MiB；无新 raw 或 Readiness 表。graph 明确 28 个 prior feature days→baseline、today feature+baseline→Recovery；future Readiness 只记 dirty，不排作业、不注册 handler。只重算已请求且受影响的输出日，day poll 仅创建 today，不填全历史。量值 end-day D 影响 D…D+28；正常 Sleep `[s,e]` 保守影响 `local(s)−1…local(e)+30`，包含两日 context。未知墓碑不编造日期；长异常 interval 不形成 lifetime fan-out。历史 revised 重算必须显式请求。

## 12. Transaction / Fencing

Loader REPEATABLE READ 稳定读取 raw / tombstones / coverage / source / config / baseline context。发布在 dataset→job→dirty 锁序下重新读取有界输入，校验 raw hash/liveness、delete、coverage、generation、lease/fencing token、assignment/config、dataset/collector/device 身份；旧工作 SUPERSEDED，不写 artifacts。末尾 wall-clock lease 检查保护整个提交。三处真实 pg_terminate_backend 故障证明 artifact/result/publication/clean/job 不会部分提交。额外 raw commit fault injection 证明：在 coverage 写入前失败会同时回滚 fact delete、候选撤销、dirty、receipt；原 current 保持可读。

## 13. PostgreSQL

真实 **PostgreSQL 18.6**，新隔离 Unix-socket cluster，fsync / full_page_writes / synchronous_commit / checksums 开启。25→26 migration 与重复 migrate 通过；runtime schema gate=26，1–25 checksum 不变。真实 rai_api / rai_worker 权限通过：API 无 candidate 写权限，Worker 不可改 raw/coverage/history。sqlc v1.31.1 重新生成完全一致。每轮数据库数归零并清理 cluster；没有读取生产数据库。

## 14. Golden Athlete / Property Tests

14 个纯领域顶层测试、17 个 PG 顶层测试（含多分支及 migration）实际执行。覆盖 stable/low SDNN/high RHR/short sleep/combined stress、三类 missing、new/sparse/no upload、DST/travel、source conflict/switch、RHR 与 Sleep revision/delete、future leakage、permutation、semantic duplicates、noise/outlier/step、随机/连续缺失、freshness、caps、浮点异常、8192 cap、独立数学 oracle，以及 12 次真实随机修订的 incremental==bounded rebuild。Fencing 包括 revision/delete/coverage/config/assignment/timezone/collector/device/expired lease/takeover；另有伪造输出拒绝与三处真实数据库进程终止测试。

## 15. Performance

[最终 PG matrix](evidence/m5b/performance-final.log) 为 synthetic + race detector；单次观测，不是生产延迟保证。aggregate/baseline/Recovery 在 Go benchmark 中另有分阶段与 allocations 数据。PG 数字如下，stored bytes 是三个实际 jsonb artifact 的合计：

| History | Input facts | Load ms | Compute ms | Publish ms | Stored bytes |
| --- | ---: | ---: | ---: | ---: | ---: |
| 14d | 45 | 10.646 | 6.048 | 37.741 | 47,301 |
| 28d | 87 | 17.068 | 6.549 | 50.074 | 56,834 |
| 42d | 89 | 15.810 | 7.497 | 48.411 | 57,180 |
| 90d | 89 | 15.906 | 7.054 | 55.085 | 57,180 |
| 180d | 89 | 16.472 | 6.328 | 48.591 | 57,180 |
| 365d | 89 | 16.712 | 6.440 | 52.866 | 57,180 |

[最终 Go benchmark](evidence/m5b/final-domain.log)：29-day aggregate 0.234ms / 418,587 B / 2,144 allocs；baseline 4.247µs / 4,104 B / 33 allocs；完整 bounded recompute 0.850ms / 1,052,663 B / 3,946 allocs。14–365d matrix 的计算 artifact JSON 40,912–48,485 bytes，包含 JSON 序列化的测量约 0.70–1.04ms。SQL EXPLAIN/ANALYZE 证明 end-time 上下界进入 index condition，365d fixture 只读 93 raw rows，约 0.301ms，无 lifetime sequential scan。该 sparse fixture 不代表真实高密度分布或 WAN。

## 16. Regression

`go test ./...`、`go test -race ./...`、`go vet ./...`、integration vet、boundary、sqlc、diff check 通过。实现期间的全量 PG18/race 集成回归通过（integration package 792.209s）；边界完善后最终 M5B/migration 又作专项 race 复验，另单独验证 raw rollback、实际 M2 和全部 product DTO 隔离。Daily/Workout/M1/M2/M3/M4/legacy training Go 回归未发现退化。

关键边界测试实际运行两次 M2 load/state pipeline，并用显式 HR reference 修订改变真实 M2 load：SDNN/RHR/Sleep 不变时，Recovery artifact ID、hash 与 score 均不变。VO₂/Workout/legacy 参数变化也不影响 Recovery；Home、history、workouts/detail、plan、generic training-state readers 未泄漏 candidate。

没有 M5B Swift/DTO/UI 修改。DailyProbe 已从原 Swift source 重新构建，真实 HTTP→PG18/race 的 before-upload / during-upload / after-commit SIGKILL、同回执/anchor 恢复及真实 429 下 Sleep 公平调度通过。`swift test --package-path ios/RAI --filter Daily` 因当前 CommandLineTools 缺 `Testing` 模块而未运行成功；不能把 probe 通过等同于完整 Swift package 或 Simulator 验收。

## 17. Privacy

所有数据均为 synthetic。普通 Worker 日志与 candidate inspect 只输出 status/count/hash/duration/reason；不打印健康值、Sleep 时间/时长、full UUID、source bundle 或凭证。原始样本仅在数据库有界快照中消费；artifacts 保留摘要与 refs/hash。CLI inspect 的脱敏内容已测试。公开日志 UUID 扫描与冻结文件证据在 [validation](evidence/m5b/validation.json)。

## 18. Risks

- **P0：无已知未解决项。** raw 修改、未来泄漏、deleted current、旧 generation 发布、candidate 覆盖 legacy、M2/M3/M4 数值污染或缺失当正常的门禁已覆盖。
- **P1：无已知未解决项。** 未推断 morning HRV / RHR，sleep 不重复求和，无任意 stage score/8h 目标；MAD=0、today contamination、outlier、missing-score increase 有针对性验证。
- **P2：** 全部权重、threshold/cap/quality factor、SDNN daily-context、sleep episode/nap/session rules 是工程候选；真实人群、来源、硬件与生理有效性尚未验证。Provenance epoch 是已接收字段的等价类，不能证明两个完全同描述设备是同一物理设备。Swift package 测试工具链缺失；没有本阶段 Simulator/真机 UI 验收。Sleep midpoint/regularity、绝对 safety floor、非默认 config、历史 raw as-known 重构明确未启用。
- **P3：** median step 的非平滑响应、高密度接近 8192 上限、长期多用户容量及分配优化仍需后续合成/校准研究；现有 sparse 性能矩阵不构成生产容量验收。

## 19. Not Verified

`REAL DEVICE ACCEPTANCE: DEFERRED`；`PHYSIOLOGICAL VALIDATION: NOT VERIFIED`。real HRV distribution、real RHR behavior、real sleep source/stage distribution、real source switching、real baseline stability、real physiological validity、illness/fatigue prediction validity、population calibration、WAN、production 均 NOT VERIFIED。完整 Swift package Daily 回归因缺 Testing 模块未通过运行；本阶段无正式 Simulator UI 验收。没有部署、入口切换或真实数据迁移。

## 20. Decision

**M5B RESULT: PASS WITH RISKS**。Recovery Foundation 已有可执行、pure、deterministic、explainable、bounded、lineaged、candidate-only 的领域与事务链路；缺失保持 unknown，Recovery 与 Readiness 分离。

Recommended Next Phase: **M6 — Training Readiness Integration**。本轮已停止在 M5B，不自动进入 M6。
