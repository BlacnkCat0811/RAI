# M2 — Training Load V1

日期：2026-11-03（Asia/Shanghai）。本报告仅覆盖合成数据和隔离环境。**M2 RESULT: PASS WITH RISKS。**最终命令计数见 §11 与机器证据；不代表生理校准或产品激活。

## 1. Git Baseline

**M2 BASELINE**：HEAD `bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61`；branch `main`；已有 **32 modified + 23 untracked**。完整状态、diff stat 和已有文件 SHA-256：[m2-baseline.json](evidence/m2-baseline.json)。

M2 保留已有 M1/M5A-RD/iOS/本地工具链工作。新增 `trainingload` pure domain、M1 HR exposure companion、load ledger / state loader/publisher/admin/Worker handlers、migration22/sqlc/权限、测试/本合同和报告。共享发布事务仅增补候选分支；schema readiness 与已有迁移/恢复测试跟到22。没有 reset/stash/commit/push/deploy；未修改 `../running`，未激活新指标或开始 M3。

原方案与 HEAD 字节一致；M1 已有 workoutfeatures 文件与 migrations1–21 相对 M2 baseline SHA-256 均不变。`IMPLEMENTATION.md` 保留原字节前缀，只追加 M2。

## 2. Algorithm

正式身份 `rai.training_load.hrr_integral/v1`。`r=(HR-RHR)/(HRmax-RHR)`；`0<r<=1` 时 `f(r)=(r/.65)exp(1.92(r-.65))`；合理 HR 下 `r<=0` 强度为0。`L=∫active f(r(t))dt/60`。

`RAI_AU` 为 `r=.65` 持续1分钟的模型单位。线性 HR 段使用中心化解析公式；小 `z` 展开处理端点近等导致的 cancellation，不每秒采样。独立 Simpson oracle 对照定义式。正式 trace 路径不使用平均 HR。

## 3. HR Exposure

独立 `rai.hr_exposure/1` companion，M1 `rai.workout_features/1` 不变。复用 M1 BuildPause/BuildSignal/source resolution/outlier/参数解析；不重新过滤 HR。保留 M1 同源、同 active segment、gap≤15s 插值规则，无跨暂停插值/外推。

Exposure 含 active intervals、精确线性端点、source epoch dictionary、reconstructed quality、active segment。固定7项 tuple 减少重复 source hash/字段；只合并完全相同常数的相邻同源/同质量段。限制20,000段/2MiB，超限失败而不截断。4小时、每秒变化的14,400段另有精确 round-trip/hash/负荷测试。M1-only 作业不生成或要求 M2 companion。

## 4. Parameter Model

复用 `athlete_model_inputs / rai.hr_reference/1`：显式 HRmax/RHR、ID/hash、来源、effective interval、recorded-at、supersedes/revoked。Workout start + feature as-of 决定参数；未来有效或未来记录的参数不能偷改旧 run；未明确取代的重叠为 conflict。

不使用220-age、默认60、Workout最小/最大HR推算参数。缺失正式结果 null/insufficient_data/parameter_missing。M2 冻结冲突零容忍：任何正时长 segment endpoint>HRmax 被排除并使完整 load unavailable；不 clamp、不 gap-scale 冲突，不自动提升 HRmax。显式回溯 supersession 产生新 feature/load run，旧历史保留。

## 5. Per-workout Load

| Method | 输出 |
|---|---|
| hrr_integral | 完整合资格 active HR 积分，complete |
| hrr_integral_gap_scaled | coverage≥95%、每gap≤30s且保留M1更严规则；partial |
| summary_hr_approximation | 无trace、可信唯一summary、已证明elapsed=active；partial，confidence≤.35 |
| unavailable | 无参数/HR、大缺口、冲突、暂停不明等；load=null |

保留 observedLoad、estimatedLoad、scaleFactor、coverage、maxGap、valid/active秒、参考和feature lineage、reason/evidence。gap假设为“缺失时间平均强度等于已观测时间”，跨日缺失秒在其真实所属日期分配估计量。trace存在时summary不能替代或相加。极短Workout仍可产生小负荷，并标short_session_hr_lag。未知暂停位置的 active seconds 为null；全程暂停的已知Workout不会从未知账本消失。

缺HR仍保留外部active minutes/距离；不加入计划、pace、power或session type负荷替代。

## 6. Daily Ledger

同一Workout按dataset IANA本地日期切分真实积分，使用calendar boundary/AddDate，跨午夜、春季23小时日、秋季25小时日与总和守恒测试通过。IANA解析位于loader，纯计算接收内存日历；有界日期边界hash进入input身份/lineage。

账本为不可变的 run/day/workout contribution，含parameter ID、feature run、model version、质量和可空active seconds。Current取既有candidate publication，不另建可变current值。无已记录Workout只代表recorded load=0；已知Workout的未知AU使该日null，不代表0或休息。

## 7. Load State

`rai.load_state.exp_finite/v1`：τ=7/42，180日有限指数核；180日ring + O(1) daily recurrence，显式减去出窗项。180日外精确模型贡献为0，不是无限EWMA。

acute/chronic、balance=chronic−acute (`RAI_AU/day`)、chronic>1时ratio、trend7=(E42(d)−E42(d−7))/7 (`RAI_AU/day²`)。Unknown窗口保留knownContribution/unknownWeightMass，但完整数值null；unknownWeightMass不是上界。冷启动标zero_before_recorded_history，historyDaysObserved按日历计数，前42日partial。partial summary/scaled日使有限窗口状态保持partial。

**合同依赖细节**：acute/chronic的删除影响严格为d..d+179；请求的trend7还依赖过去7天chronic，因此d..d+186需要纠正趋势。当前state行打包这些数值，尾部可重发acute/chronic不变的行。不能同时要求“完整state行在d+179以后不变”和精确持久化trend7。本实现保留有限核，明确处理7日趋势尾部，未把核扩为187天。

## 8. Persistence

复用runs/results/artifacts/publications/model_channels/model_dependencies/lease/fencing/dataset lock。只新增确有增量用途的 `analysis_load_ledger`，不按每个metric建表。feature→workout_load→athlete/load真实handler；未来模型保持dirty，没有Recovery或其他dummy成功handler。

一个既有PublishAnalysis事务：artifact/ledger/results/publication/downstream dirty/clean/run/job/attempt terminal，最后重查lease。新fact/删除/HR引用修订在原事实事务即时失效候选。旧generation只SUPERSEDED，不写artifact/ledger/pointer，不clean新dirty。

候选仍在M1的candidate stack；M2独立model/config hash写入审计、lineage、artifact。active被DB限制为legacy。未替换旧training_load、fitness/fatigue、Recovery/Readiness或training generator consumer，未增加用户首页API。

## 9. PostgreSQL

真实一次性 PostgreSQL18.6，Unix socket、listen_addresses空、fsync/full_page_writes/synchronous_commit/data_checksums均on。每个runner最终leftover DB=0、cluster removed=1。无生产DSN/生产数据/远端部署。

migration00022；empty→latest、21→22、20→latest兼容路径、repeat、历史保留与权限验证；公开表53，新sequence=0。migrations1–21不变。Down继续明确拒绝破坏性历史删除，不冒称已做22→21 schema回滚。

七种故障：artifact、load result、ledger、state artifact、publication、dirty clean、job terminal均rollback/retry安全。实际pg_terminate_backend在ledger之后、publication之前、clean之前、state write阶段验证事务原子性；计算后Worker lease过期/requeue重试。load/state两个generation race均SUPERSEDED。受限Worker实际完成feature/load/state；API不能读取/写入ledger/artifacts或修改参数/channel；Worker不能UPDATE/DELETE历史。

## 10. Math Evidence

- 正式60 AU与71.743050815 AU向量，凸性与mean-HRR路径不同。
- 独立8192分段Simpson oracle，200个固定种子随机/近等端点及零交叉向量。
- 400个duration/intensity/split-merge性质；同一线性HR曲线1秒/5秒采样严格容差一致；instant不制造duration。
- 25条480日随机序列，两τ逐日比较直接180项卷积与ring递推；未知权重另算。
- 180轮insert/delete/late/parameter-change形态与直接重建；120轮真实缓存仅替换有界输出区间，再与全450日重建比较。
- beginner、stable intermediate、high-volume、interval-heavy、returning、unknown-load六类450日athlete fixture。
- PG late arrival/delete与直接ledger卷积对照；有界recompute不改数值；回溯参数改ID和结果；本地day advance保留未受影响历史pointer。

## 11. Tests


| 验证 | tests/subtests pass | top-level pass | failures |
|---|---:|---:|---:|
| go test -count=1 -json ./... | 460 | 125 | 0 |
| go test -race -count=1 -json ./... | 460 | 125 | 0 |
| go test -race -count=1 -json -tags=integration ./... | 935 | 350 | 0 |
| go test -race -count=1 -json -tags=integration ./integration ./internal/admin -run '^TestLoad|^TestFeature' | 52 | 23 | 0 |

最终命令、真实pass/skip计数、退出状态和日志SHA-256由 [m2-training-load-v1-2026-11-03.json](evidence/m2-training-load-v1-2026-11-03.json) 给出；计数包含子测试，另列top-level计数。命令：

```sh
go test -count=1 -json ./...
go test -race -count=1 -json ./...
go vet ./...
python3 scripts/check-boundaries.py
PGBIN=<bundled-PG18> scripts/test-postgres.sh go test -race -count=1 -json -tags=integration ./...
PGBIN=<bundled-PG18> scripts/test-postgres.sh go test -race -count=1 -json -tags=integration ./integration ./internal/admin -run '^TestLoad|^TestFeature'
RAI_M2_PERF=1 PGBIN=<bundled-PG18> scripts/test-postgres.sh go test -count=1 -tags=integration ./integration -run '^TestLoadPerformanceMatrix$' -v
go test -run '^$' -bench 'BenchmarkWorkoutLoad|BenchmarkAthleteState' -benchmem ./internal/trainingload
sqlc generate  # second generation must match first byte-for-byte
git diff --check
```

使用已安装本地Go1.27.3/sqlc1.31.1/PG18.6工具链，测试脚本忽略ambient DB配置。M2未改Swift/wire/UI，不把已有Swift改动归为M2；未新做Simulator或真实设备验收。

## 12. Performance

最终本机8项PG矩阵与8项Go benchmark见机器证据performance/benchmarks。Workout：20m、45m、2h、4h，记录feature read、load compute、ledger insert、publication pointer和整体事务、state read/compute/write、allocations、artifact size。Athlete：30d、90d、180d、365d；真实synthetic ingest→feature→load建立ledger后测state，逐项与直接卷积对照。


本次最终PG矩阵（ms；单次观测）：

| Workout | feature read | load compute | ledger insert | pointer | load txn | state read/compute | state publish | load artifact B |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| 20m | 2.498 | 0.057 | 0.818 | 0.222 | 23.704 | 3.106 | 4.874 | 1586 |
| 45m | 2.241 | 0.030 | 0.672 | 0.137 | 4.523 | 2.804 | 4.300 | 1590 |
| 120m | 2.703 | 0.025 | 0.910 | 0.140 | 4.590 | 3.010 | 4.082 | 1592 |
| 240m | 2.800 | 0.035 | 0.658 | 0.120 | 4.170 | 2.930 | 3.627 | 1595 |

| Athlete history | ledger read | state compute | state publish | ledger rows | allocations | artifact B |
|---|---:|---:|---:|---:|---:|---:|
| 30d | 3.242 | 0.135 | 34.341 | 30 | 2225 | 14999 |
| 90d | 5.241 | 0.312 | 66.801 | 90 | 3709 | 43595 |
| 180d | 13.137 | 0.560 | 100.163 | 180 | 5983 | 86637 |
| 365d | 33.688 | 1.495 | 172.412 | 365 | 10640 | 172039 |

Ledger/pointer分段由test-only before/after timing triggers测得，包含插桩开销；总体事务另测。容量数据为单次观测，benchmark为Go多次循环；均不能外推生产SLO。新Workout数值路径读取其feature/exposure和有界ledger，不扫描历史HR/GPS samples。

## 13. Regression

全量Go与真实PG race回归覆盖Workout Sync、Daily Health、M1 Features、旧analysis/detail、training及管理迁移/恢复。M1-only不启用load jobs，原future dependencies无假成功；候选计算不能改变旧active run identity。边界检查验证online图不导入admin/migrations/goose，以及纯domain无DB/HTTP/filesystem/clock/random路径。原方案与旧迁移、M1 frozen文件受hash保护。

## 14. Risks

- P0：已验证范围未发现重复计数、unknown变0、删除current残留、旧generation发布、active覆盖、跨日丢失、参数身份混用或incremental/rebuild不一致。
- P1：已验证范围未发现均值冒充积分、过度补缺口、HRmax clamp、planned/power/pace混分、无限EWMA、DST或summary伪装完整。
- P2：这仍是candidate-only工程模型；覆盖/插值/冲突门槛及summary .35 cap未用真实人群校准。严格零HRmax冲突策略可能降低eligible率。Artifact/ledger/输出有界容量会fail closed：20k segments/2MiB、10k ledger rows、552输出日、显式重算≤366日/1000Workout；超过跨度的pending scopes当前没有分段自动修复器，不能冒称任意多年稀疏待办均可自动完成。已有seed history未enroll/current会pending，需要先明确请求相应有界历史。
- P2：trend7的7日依赖尾部是原请求“所有state严格止于179日”的数学例外，已在合同明示；finite acute/chronic没有变更。
- P3：性能为本机合成观察；未做生产SLO、WAN、真实设备或断电验收；行政重算参数选择当前支持`rai.hr_reference/1`的as-of身份，不支持任意parameter ID强制覆盖历史。

## 15. Not Verified

**REAL DEVICE: DEFERRED. REAL DEVICE ACCEPTANCE: DEFERRED.**

NOT VERIFIED / DEFERRED TO REAL-DEVICE / REAL-WORLD CALIBRATION：真实Apple Watch HR采样与gap/coverage、插值适用性、HRmax冲突频率、summary-only率、真实load eligibility率、interval行为、个体HRmax/RHR质量、生理有效性、负荷与疲劳关系、production、WAN、断电、真实设备/现实世界校准。模拟器和synthetic fixture不证明这些事项。不使用FULL PASS或PHYSIOLOGICALLY VALIDATED结论。

## 16. Decision

**M2 RESULT: PASS WITH RISKS。** model implementation validated; physiological calibration pending。已验证工程范围无P0/P1 blocker；上述容量边界和trend依赖例外明确保留。

**Recommended Next Phase: M3 — Performance & Running Ability。** 未自动开始M3。保持candidate-only，未激活正式指标；M2结束后停止。
