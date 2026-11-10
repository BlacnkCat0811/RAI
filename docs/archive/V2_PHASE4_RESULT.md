# RAI V2 Phase 4 Result

## Status

**PASS WITH RISKS**。版本化分析、真实 Worker、原子发布、代际与 fencing 安全、重算、查询、fnOS 自动化门禁已经实现并验证。恢复/readiness 是公开合同定义的 load-only 工程模型，尚无个人生理效度验证；不因为能给出数值就称完整身体状态。

## Repository

`/Users/ta/rai`，分支 `main`。

## Base Commit

`P4_BASE_COMMIT=8e40573e027ac6dd9aac871fb755051cb31c4657`。

开始时 `git status --short`、`git diff --stat` 均为空；没有 P4 前既有未提交修改。本轮 tracked/untracked 变化全部是 P4。未 reset、clean、commit、push、部署、切换旧入口、迁移真实数据或修改 `../running`。00001–00011 和原始架构方案未修改。完整文件清单与源码摘要见 [机器可读证据](evidence/p4-fnos-2026-10-28.json)。

## P3 Deferred Gate

```text
P3 REAL DEVICE ACCEPTANCE: DEFERRED BY USER
P3 REAL DEVICE E2E: NOT VERIFIED
P4 DEVELOPMENT EXCEPTION: AUTHORIZED BY USER
```

历史 [P3 Result](V2_PHASE3_RESULT.md)、P3/P3.5 evidence 的 NOT VERIFIED/BLOCKED 记录保留。实际授权、anchored query、GPS、iPhone force-kill、background URLSession 和 iPhone→fnOS 尚未验收。P4 的合成分析测试不替代它们。

## Architecture

```text
Raw → Dirty → Job → repeatable-read Input Snapshot → Pure Analysis
    → immutable Run → independent Results → atomic Publication
    → generation clean + job/attempt complete + downstream dirty
```

新增 `internal/analysis`；算法不查询 DB，不依赖 Python/ML。postgres storage 构造有序输入和 scope hash，保存 lineage，控制事务；jobs 只负责调度和 handler 生命周期。Raw Facts 不被算法更新，Worker 数据库角色也没有此权限。ACK 仍只等待事实 durable。

P3 两个依赖正确性 blocker 已有最小修复：sample 关联级联删除时重新 dirty Workout；sealed series dirty 已关联 Workout。Workout dirty 在 state scope 存在后同事务传播。sample 新增批次按 type/UTC 日聚合，1000 条同日样本只创建一个依赖任务；旧 receipt 不重写。未修改 anchor、outbox、batch identity、sequence 或 route seal。

## Schema

新增前向 **00012_analysis.sql**：`analysis_parameter_sets`、`analysis_runs`、`analysis_results`、`analysis_publications`；复用 dirty/jobs/attempts。run/result/参数历史有不可变触发器；结果有限值 CHECK；dataset/run/job 复合 FK；终态逻辑 identity 唯一；真实 scope/run/publication/load 查询索引。

空库、完整重建、schema11→12、原 receipt 保持、重复 migrate、新 binary 对旧 schema11 readiness 拒绝已通过真实 PG。旧 binary 源码要求 schema11，对12会 fail readiness；**没有运行旧 binary+新 schema 的进程组合测试**。新 Worker 启动检查 schema12。迁移只前向，down 白名单仍空。

## Algorithms

| name | version | status | inputs | outputs |
|---|---|---|---|---|
| workout.features | v1 | implemented / synthetic verified | durable Workout、enrichment、显式关联 samples、sealed series/routes | duration/distance、speed/pace、HR mean/max、真实 cadence/power、coverage、route distance/有精度条件的高差 |
| training_load.trimp | v1 | implemented / golden verified | duration、≥90%且不歧义的 HR intervals、显式 resting/max HR 与 A/B/provenance | load AU、method、parameter version、missing components |
| fitness_fatigue | v1 | implemented / golden verified | published load、dataset IANA timezone、180日有限指数核 | daily load、fitness、fatigue、balance |
| recovery | v1 | implemented / synthetic verified | 同日 fitness/fatigue | load-only ratio 0..1 |
| readiness | v1 | implemented / synthetic verified | recovery + 组件/缺失来源 | load-only 0..100、解释、quality |
| dependency.invalidate | v1 | implemented / PG verified | sample/series scope、真实关联 | dirty propagation execution，**不生成伪样本评分** |

正式 bundle：`workout.bundle/v1`、`training_state.bundle/v1`。公式、canonical units、quality、窗口、时间轴和重算语义见 [P4_ALGORITHM_CONTRACT](P4_ALGORITHM_CONTRACT.md)。不采用隐藏人口默认参数或隐含 sex coefficient。

Missing ≠ Zero。无个人参数、HR只有瞬时点/覆盖不足、区间重叠、active time 未与 elapsed 对齐等情况，TRIMP unavailable；必要 load 缺失时高层状态也 unavailable。睡眠、HRV 和 resting-HR trend 明示未使用。route seal 不代表 GPS 全覆盖；精度不明不假设可靠。没有实现 zones/splits、Garmin 私有算法、训练处方或 P5。

## Worker

单 processing slot，1s空闲 poll；Claim SKIP LOCKED，60s lease，20s Renew，15s过期恢复与有界 calendar roll-forward；支持 cancellation。按 kind dispatch；优先最新 scope generation，过时 queued 身份保留并 supersede。

retryable DB/dependency 错误沿用5s退避与最多8次；invalid raw/未知版本永久失败。失去 token/lease 立即取消，发布重新验证，末尾使用 clock_timestamp 检查租约。过期恢复同时关闭遗留 RUNNING run。结构化日志包含要求的 ID、scope、generation、algorithm/version、run、duration/status，无 HR/GPS/payload/token。

## Generation Safety

dataset → job → dirty 锁序；result、terminal run、pointer、CAS clean、job complete、attempt complete 和下游 dirty 同事务。输入代际已变化时不冒充历史快照；已计算旧 run 为 SUPERSEDED，不能 clean 新代际或切换 current。

真实 PG 验证 generation1计算中到达generation2、generation2先发布后generation1完成、过期重领新token、旧token发布被拒、发布事务中 lease 过期整笔回滚。删除后即刻从 current 查询隐藏旧 derived，随后 handler 移除 pointer 并重算 state。

## Recompute

日常为单 Workout + 有界 state range；T 影响 T..T+179，截止 as-of，输入再向前179日。日序列按 dataset timezone；scratch 镜像显式嵌入标准库 time/tzdata，避免依赖不存在的 OS zone database。

CLI `rai analysis recompute` 要求 dataset/scope/algorithm/from/through，支持只读 dry-run，每次最多366输出日或1000 Workout，无默认全库。多年历史显式分段。pending state dirty 存在时，显式范围必须覆盖受影响日，防止历史小范围任务清掉范围外待办。

`rai analysis parameters` 追加参数版本及 provenance，有界重新分析当前窗口；不会原地改旧 run。current Workout 查询检查参数版本，旧历史需要 explicit recompute。没有全历史重算进入 HealthKit 热路径。

## Fault Injection

全部通过真实隔离 PostgreSQL，合成数据：

- claim/计算后模拟崩溃：lease 过期、旧 run FAILED、重新领取并成功发布；旧 Worker fenced。
- result INSERT、publication INSERT、dirty UPDATE、job UPDATE、attempt UPDATE 五处触发器故障，均无半发布；原尝试重试成功。
- result 已插入而 publication 失败：结果、run终态和job一起回滚。
- publication事务已经耗过 lease：最后墙钟检查拒绝，所有结果回滚。
- commit 后不依赖外部 ACK：job已成功，不再次 claim 或重复 publication。
- retryable dependency / permanent unsupported version 分流；继承既有8次上限、attempt原子性与并发claim回归。

上述是数据库故障/lease crash 模拟；不声称做了物理断电或计算中真实 SIGKILL Worker 测试。Compose 另验证实际进程停止/重启与持久状态。

## Correctness

固定 v1 golden 包含算法版本、输入和期望值：Workout及TRIMP、双日有限核fitness/fatigue/recovery/readiness。浮点 tolerance；增量与同范围完整重算数值一致。性质测试覆盖非负、coverage/readiness界限、有限值和明确timezone/DST calendar。

覆盖无HR/部分HR/瞬时HR/重叠HR/summary-only/indoor、无cadence/power/route、真实cadence/power及summary max、paused Workout、缺参数、负值/NaN/Inf、取消、删除；真实 late sample/association、enrichment、route pending与seal、series pending与seal、sample删除重算。HTTP DTO、dataset隔离、dry-run、actual Worker loop、daily generation去重与受限角色均验证。

## fnOS Validation

Go1.27.3，PostgreSQL18.6，Linux x86_64，4CPU，约7.76GiB，GOMAXPROCS=2；PG独立容器，fsync/full_page_writes/synchronous_commit/checksums均on。

| 检查 | 证据 |
|---|---|
| gofmt、mod verify、go test ./...、go test -race ./...、vet、build | PASS；最终源码重新执行 |
| 完整 integration/race 回归 | PASS，两轮integration包194.036s、261.859s；后者包括新重算范围门禁 |
| 最终P4 integration/race | PASS，31.610s；包含后续CLI/角色/新旧publication竞争修复和测试 |
| vet integration,scale | PASS；未重新执行既有全部P3 scale大数据套件 |
| 在线依赖图 / analysis I/O边界 | PASS，247包；不达admin/migrations/goose |
| sqlc 1.31.1 generate | PASS；最终生成前后SHA完全一致 |
| OpenAPI / Redocly2.71.1 | PASS，8条非阻断警告（健康响应/未使用schema），详见evidence |
| Compose | PASS；空卷/迁移两次、P3实际HTTP上传及重启原回执、Worker离线ACK、同镜像、真实分析current API、scratch timezone、CLI dry-run、SIGTERM、Worker重开 |
| 清理 | 独立测试容器、Compose项目和卷已删除；保留的临时源码目录在交付前清理 |

最终普通/race/vet/build、P4专项、性能、生成代码与OpenAPI同一验收 run：`20261028093954-3632318`。完整回归后追加的修复通过最终P4专项，不把较早完整回归的测试数量冒充最终新增测试数量。GitHub CI、真机、正式部署未执行。

## Performance

最终 **non-race**、fnOS合成热库；下表每个 state/Worker 场景在每个规模下 **n=1观测**，不是median/P95。state时间含管理排队、claim、input、compute、run与publish；Worker时间不含1s poll等待。历史fixture每天一个Workout及一个已知load，原始/派生历史以set-based SQL种入，仅容量用途；当前单Workout另经真实sync/handler读取1000HR区间+1000sealed route点。

| 历史规模 | Workout数 | 7日state ms | 30日state ms | 365日state ms | Workout claim→publish ms | jobs / runs / results rows |
|---|---:|---:|---:|---:|---:|---:|
| 1年 | 365 | 51.762 | 58.856 | 319.073 | 61.776 | 374 / 369 / 2793 |
| 3年 | 1095 | 68.937 | 98.229 | 430.674 | 63.566 | 1104 / 1099 / 3523 |
| 5年 | 1825 | 69.921 | 79.923 | 339.237 | 61.009 | 1834 / 1829 / 4253 |

固定纯函数fixture为1h、一个完整HR区间、无route；5年场景的独立100次：feature+load median **0.023104ms** / nearest-rank p95 **0.031294ms**；独立TRIMP median **0.004214ms** / p95 **0.005099ms**。一次7个Workout纯load重算 **0.030083ms**。这些不是手机、网络、冷盘或复杂轨迹性能承诺。

实际生产SQL使用默认planner执行 EXPLAIN(ANALYZE,BUFFERS)：Workout主键、publication/run/result索引可用；5年180日load输入使用Workout时间Bitmap路径，读179条而非重算全历史。小表associated HR与route选择顺序扫描（所有1000行都属于目标Workout），state dirty等小表也有一次Seq Scan；未禁用seqscan伪造索引。查询执行观测及完整计划见 [plans](evidence/p4-query-plans-2026-10-28.json)。不能把这个规模推断为百万健康样本或多年全部GPS性能验收。

## Deviations

与冻结算法合同的功能偏差：**NONE**。实现采用两个bundle加依赖handler，而非每metric一job；四张新表包括显式参数历史。选择180日有限核以保证有界、deterministic增量/重放，未声称无限EWMA等价。zones/splits按合同延后。P4不开发UI、Training Plan或Coach。

实施中发现并修复 scratch缺时区库、窄范围重算错误clean风险、partial HR及paused workout误用风险；初始测试fixture/编译及sqlc默认代理失败已修正并重跑，未把失败轮记为PASS。

## Risks

- load-only recovery/readiness与42/7/180参数是RAI合同定义，只有公式和合成正确性，没有个人生理/训练收益校准；quality始终partial。
- 真实HealthKit常见瞬时HR点、暂停活动、个人参数缺失会使TRIMP和上层值unavailable。这是保守输入门槛，需后续真实来源与暂停区间语义验收，不能隐式外推。
- 每次分析最多250000 observations/route points；P3同步允许更大的sealed对象。超限会明确失败，尚未实现百万点流式特征分析。
- 1/3/5年容量fixture以Workout/derived为主，不能替代多年全部HR/GPS、冷盘、长期run增长/retention或生产压力测试；部分小表顺序扫描的更大规模选择尚未验证。
- 历史run/参数/队列长期保留；个人参数或算法更新后的远期历史需显式分段重算。没有旧binary+schema12进程组合验收。

P3真机延期独立保留；生产发布、安全、备份恢复与断电验收仍属于未执行的P7，不将Compose通过称生产完成。

## P4 Final Status

```text
P4 STATUS: PASS WITH RISKS
```

## P3 Deferred Status

```text
P3 REAL DEVICE ACCEPTANCE: DEFERRED
P3 REAL DEVICE E2E: NOT VERIFIED
```

## P5 Entry

```text
P5 DEVELOPMENT READY: YES
```

这是版本化分析/重算/查询与稳定合同的开发入口判断；P5必须正确处理insufficient指标，不得视作生理效度或iPhone验收已经通过。本轮停止于P4，没有实现training_plans、plan_sessions、override/effective、coach、WorkoutKit或Watch execution。
