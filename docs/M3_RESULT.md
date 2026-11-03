# M3 — Performance & Running Ability V1

日期：2026-11-03（Asia/Shanghai）。仅使用 synthetic fixtures、deterministic/property/Golden Athlete 与真实隔离 PostgreSQL18。**M3 RESULT: PASS WITH RISKS。**当前仍 candidate-only，未正式产品激活。正式数学与状态定义见 [Running Ability V1 Contract](RUNNING_ABILITY_V1_CONTRACT.md)；命令、计数、日志 hash、性能和保留项见 [机器证据](evidence/m3-performance-v1-2026-11-03.json)。

## 1. Git Baseline

HEAD `bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61`，branch `main`。启动时已有32个 modified + 43个 untracked 状态条目（包括目录条目，不冒充独立文件数）。[m3-baseline.json](evidence/m3-baseline.json) 保存原状态及文件 SHA-256。

保留已有 M1/M2/M5A-RD、iOS、本地环境工作；未 reset/stash/commit/push/deploy，未修改 `../running`。M3 新增 performance 纯领域、Worker分支、bounded Feature loader、typed artifacts、发布前依赖身份检查、管理 inspect/history/promotion-plan、migration23/sqlc、测试/合同/本报告。共享 readiness 与旧迁移验收更新到23。原方案、migration1–22、M1/M2纯模型及全部 Swift 文件与 M3 baseline 字节一致；IMPLEMENTATION保留基线前缀，只追加本阶段证据。

## 2. Definition

Running Ability 是**可比平路连续跑条件下，近期事实支持的60分钟持续跑表现能力**。只表示 demonstrated 已展示表现，不表示最大潜力、VO₂max、健康等级或比赛概率。

唯一数值路径是 Performance Evidence + Speed-Duration Relationship。M2 load、VO₂max、Recovery/Readiness、跑姿和经济性不进入 score 或 confidence。没有比赛/马拉松预测、训练建议或 CS/D′ 必需输入。

## 3. Performance Evidence

只消费 M1 WorkoutFeatures 的20/30/40/60/90min best qualified continuous windows；全 Workout 和 window 都需 eligible/conditional。3/5/10min 和 backup 不参与能力拟合、独立计数或 confidence。没有重复读取 raw HR/GPS/speed 寻找PB或第二套质量过滤。

Evidence 具备 Workout、duration、distance/speed、window/Workout times、feature version/run/generation、known-at、source epoch、terrain、qualification、quality components、effort mode 和 evidence digest。全同重复输入去重；冲突 revision 拒绝。每 Workout 总权重≤1，五个窗口不能变成五次独立跑步。来源和字节身份均能追溯至 immutable Feature run。

每 target 是90个本地日历日；14日 smoothing 只额外需要13日 seed。首次收到时已经落在90日外的旧事实不能经 rebuild 偷入 current。默认计算明确标记 revised；as_known 纯模型额外限制当时的 feature publication known-at。未来 Workout、未来 feature 和后来的确认不能回填过去 target。

## 4. Curve Model

`D(t)=D30×(t/1800)^γ`；log域80% weighted quantile regression。γ 从.700到1.000，步长.001，301点穷举；每点 α 是 weighted quantile。固定 loss tie tolerance1e−12，优先较大 γ，再较小 α。无随机 optimizer、固定 Riegel 指数或均值回归。

真实 PG 合成端到端常速例：

| D30 | γ | v20 | v60 |
|---:|---:|---:|---:|
| 5400m | 1.000 | 3m/s | 3m/s |

γ=.700 或 median absolute log-distance residual>.06 → curve_not_identified。≥5次独立 Workout、跨度≥14日、短/长组各≥2次、≥40min长证据，最大60min外推为1.5倍；不填默认 γ。

## 5. Score

`rawScore=100×ln(v60/1.5)/ln(6/1.5)`；`score=clamp(rawScore,0,100)`。

3m/s→50；2、2.5、3.5、4、5m/s均按精确公式回归。固定scale不做人口percentile；0不等于没有能力，100不等于人类极限。内部保留原始v60、unclipped score、saturation及float64，未来display integer单独round，当前不激活UI。

“为什么72”的可审查数学例：D30=7636.753237m、γ=.940、v20=4.347121m/s、v60=4.069813m/s，固定公式得到72。这只是公式构造例，不是实际运动员或真实验证结果。

## 6. Endurance

`retention=v60/v20=3^(γ−1)`。它是 performance endurance indicator，不是乳酸阈、VO₂max或economy。v60已经包含速度锚点与耐力扩展，不另加一份Endurance分数。

平滑后保留自洽 D30/γ/v20/v60/retention；`ln(v60)=ln(D30/1800)+(γ−1)ln2`，输出真实30min速度锚点与耐力扩展项。没有虚构 VO₂/cadence/load/recovery 加减分。可选 HR drift/decoupling 和 CS research 本阶段未实现，不伪装成诊断成功。

## 7. Evidence / Confidence

q来自已有M1 coverage、distance agreement、terrain/source/gap support与qualification；按Workout分摊，另乘45日recency。输出独立数、短/长组、duration coverage、source epochs、history span、Kish n_eff、mean quality、freshness、stability。

confidence是模型证据支持分数，非准确概率；demonstrated最高.55，实际还受count/span/n_eff/mean quality/recency约束，不能证明最大努力。没有confirmed annotation infrastructure，maximal_supported明确unavailable。

同duration高于其他Workout中位数>10%的孤立异常需另一独立Workout在±5%确认；未确认时整次Workout权重0。后续真实多次进步能进入模型。每个Workout的LOO重拟合若使v60变化>5%，则unstable、ability=null。

## 8. Cold Start / Freshness

每个新有效 evidence day产生target。h−13…h有效target在log域按exp(−k/14)平滑；至少3个有效target days且最新target通过才出值。Beginner为collecting；缺长证据为insufficient_long_evidence，均不编造低分。

无daily M3 target job；四天没有新证据分数不变。14日aging；28日stale/ability隐藏；42日insufficient_data/evidence_expired。没有自动扣分或detraining模型；未上传不证明没训练。历史artifact仍可按run ID查询。

Trend仅比较28日前、同model/version/config/evidenceMode/view且有值的结果，否则null。当前管理读取只更新freshness/confidence，不能制造target或增长。

## 9. Persistence / Dependency

复用analysis runs/results/artifacts/publications/channels/dependencies；没有新table、duration表或重复Feature ledger。一个M3原子scope共同发布Evidence→Curve→Endurance/Ability：5个artifact、3个typed result、一处candidate pointer。

原事实事务立即失效相关current。发布事务同时处理artifact/result/publication/clean/run/job/attempt terminal，沿用dataset锁、generation、lease/fencing和candidate config身份。额外在发布前核对全部捕获Feature的run/generation/存活状态，封住“旧seed删除时尚无current pointer”竞态；不匹配只SUPERSEDED，另排新一代修复，不clean N+1。

管理compute/recompute复用已发布M1，只做bounded rebuild；operation retry不增job。inspect/history与promotion-plan已验证。DB仍禁止active非legacy，promotable=false；旧active及immutable历史构成rollback substrate。没有schema Down或旧二进制跨schema23回滚验收。

## 10. PostgreSQL

真实一次性PG18.6（仅Unix socket；fsync/full_page_writes/synchronous_commit/data_checksums均on）。empty→latest、22→23、旧升级路径、repeat、schema23/53 tables、permission、fencing、delete、rollback均通过。migration1–22 checksum不变；新增00023，无新sequence。runner结束均leftover databases=0、cluster removed=1。

受限Worker实际完成M1→M3；API不能读写candidate artifacts或修改config，Worker不能修改facts/immutable历史。M3不触发M2 enrollment，不覆盖legacy active。

六类故障：evidence artifact、curve result、ability result、publication、dirty clean、job terminal；全部rollback/retry。三处实际pg_terminate_backend：after evidence、before curve publication、before dirty clean；均未留下半发布。新generation发布前、租约过期及95日旧seed删除竞态均有真实数据库证据。

## 11. Math & Property Evidence

独立枚举所有α breakpoint的pinball oracle对照301点solver；score anchors/clipping、γ上下边界、residual和LOO failure通过。固定v20提高retention使v60/score非下降；v60→score单调。重复Workout不增加n_eff/confidence；输入/DB次序稳定；backup/短时诊断不能主导；unknown feature version拒绝。

40轮deterministic insert/delete/late/revision事件：缓存的有界Feature集合与独立source-of-truth bounded rebuild结果逐字节相同。PG显式rebuild复用M1后数值一致；迟到/deletion/pending依赖和历史保留通过。future event、future known-at、future confirmation与as_known backfill隔离均通过。IANA/DST calendar及quality support confidence有独立测试。

| Command | pass tests/subtests | top-level pass | failures |
|---|---:|---:|---:|
| go test -count=1 -json ./... | 503 | 142 | 0 |
| go test -race -count=1 -json ./... | 503 | 142 | 0 |
| isolated PG: go test -race -count=1 -json -tags=integration ./... | 997 | 374 | 0 |
| final isolated M3 PG race, integration + admin | 21 | 9 | 0 |

全量PG覆盖旧Workout/Daily/M1/M2/training及schema23。全量回归之后，对最后补强的M3发布前身份核对再运行21项M3专项，包含旧seed竞态；最后的quarantine诊断保留由最终数学/race与PG性能pipeline再验证。记录各命令各自真实计数，不把专项重复加入全量计数。optional performance/scale skips在机器证据单列；性能矩阵已显式运行。

go vet（普通及integration）、go build、go mod verify、依赖边界、sqlc1.31.1二次generate字节一致、gofmt及git diff --check均通过。完整日志在本地`.local/logs/m3/`，证据保存命令/退出码/计数/摘要SHA-256。没有Swift/DTO变更，本阶段未新跑Simulator或真机，不借用旧iOS验收证明M3。

## 12. Golden Athlete

10类全部通过：

| Athlete | Synthetic outcome |
|---|---|
| Beginner | collecting，ability=null |
| Intermediate | v20=3.3、γ=.900、v60=2.956663、score=48.950364 |
| Advanced | v20=5.3、γ=.960、v60=5.072138、score=87.881568 |
| High short-speed / poor endurance | v20=4.5、γ=.760、v60=3.457032、score=60.228575 |
| Moderate speed / strong endurance | v20=3.5、γ=.980、v60=3.423936、score=59.534659 |
| Returning | 42日无证据，expired/current隐藏 |
| Noisy GPS athlete | M1 ineligible被拒，未制造分数 |
| Sparse long-run evidence | insufficient_long_evidence |
| Single-PB outlier athlete | 单条+30%被隔离，score不跳变 |
| True step-improvement athlete | 多次+5%证据，42日吸收100% log-speed变化，超过60%工程门槛 |

替换一条正常evidence为+5%，score变化≤2；另一天孤立PB不制造target、锚点不变但可inspect其confirmation_required。以上均为工程合成夹具，不是实际运动员预测精度。

## 13. Performance

显式PG矩阵：20m/45m/2h/4h，以及30d/90d/180d/365d。记录Feature read、evidence extraction、curve+smoothing+LOO、state publication、artifact bytes、allocations。最终实测逐项数值见机器证据，不从单次观测外推生产SLO。

365日样本真实写入122次synthetic Workout，但M3只读取30个bounded Feature refs、60个selected points；180日也只读取30 refs/60 points。没有千级滑动窗口进入athlete model，也没有读取lifetime raw samples。

本次最终PG观测（ms，单次测量，含读取/序列化/数据库开销）：

| Matrix | Feature read | Extract | Curve+smoothing+LOO | Publish | State B |
|---|---:|---:|---:|---:|---:|
| 20m | 6.829 | .531 | 3.375 | 7.086 | 22573 |
| 45m | 7.607 | .515 | 6.374 | 8.785 | 24284 |
| 2h | 7.756 | .628 | 4.538 | 6.723 | 26025 |
| 4h | 8.492 | .724 | 3.976 | 8.604 | 26014 |
| 30d | 8.207 | .599 | 5.330 | 5.956 | 21873 |
| 90d | 18.389 | 1.815 | 39.333 | 15.772 | 56070 |
| 180d | 19.787 | 1.669 | 38.839 | 16.303 | 56067 |
| 365d | 20.508 | 1.658 | 39.402 | 15.747 | 56066 |

20次Workout的Go stage benchmark：extraction约.444ms、curve fit约2.297ms、curve+LOO约10.120ms；密集90次Workout完整14日target计算约2.075s/34.47MB/69,392 allocations。PG矩阵与Go benchmark负载不同，不能直接互当时延估计。

Go benchmark另测10/30/60/90 Workout密度、extraction、curve fit、curve+LOO。密集90次Workout仍最多450点，CPU成本高于普通频率；96次Workout/480点是显式fail-closed上限。全部原始观测及分配指标保留，不挑最快结果。

## 14. Regression

完整Go、race、真实隔离PG覆盖M1/M2、Workout sync/detail/analysis、Daily Health、legacy state/training及管理恢复/迁移。新candidate没有更改已有active run identity或training消费者。在线依赖图不达admin/migrations/goose；performance只消费M1，不导入load/VO₂/raw-history存储。

原方案原文、旧migration checksum、M1/M2纯领域和Swift文件均经M3 baseline SHA-256比较。没有读取生产数据库/真实健康值、记录凭证、部署、旧入口切换或真实数据迁移。

## 15. Risks

- P0：已验证范围未发现重复evidence涨confidence、删除current残留、未来泄漏、旧generation发布、candidate覆盖active或bounded rebuild不一致。发现的旧seed无pointer竞态已补guard，并有真实PG回归。
- P1：已验证范围未发现均值冒充能力、单次PB主导、短证据默认外推、固定Riegel、load/VO₂/HR猜effort、Endurance重复加权、未知给低分或stale仍显示current ability。
- P2：q、10%/5%异常门槛、.06 residual、5% LOO、80% quantile、confidence≤.55和产品scale未经过真实人群校准。严M1 qualification可能使真实eligible率低。
- P2：96 Workout/480点/2MiB每artifact的显式容量上限；初次enrollment也有上限。seed Feature若尚未建立则pending，需要有界M1准备；没有lifetime自修复扫描器。当前as_known只使用明确的历史Feature refs，不能从current Feature artifact假装重建丢失的过去run。
- P2：current重算明确是revised；旧as-known artifact保持历史身份。90日外迟到不影响current；任意历史专项重算/effort annotation/maximal_supported/long-run诊断/CS research未开放。
- P3：性能为本地合成观测，密集history的LOO CPU成本较高；未验证production/WAN/断电/旧二进制跨schema回滚。Swift和正式UI未激活。

## 16. Not Verified

**REAL DEVICE: DEFERRED。REAL DEVICE ACCEPTANCE: DEFERRED。**

**NOT VERIFIED**：real-world performance validity；real GPS/speed eligibility；真实GPS精度/距离一致性；真实speed support quality；真实Workout/flat-route qualification rate；真实努力/比赛表现；maximal effort identification；真实Apple Watch sampling；real effort distribution；real v20/v60 evidence density；real single-workout bias；real source epoch effects；实际跑力精度；population calibration；production/WAN及真实设备/现实世界校准。

没有生理有效性或真实世界验证结论。Simulator即使可用也只能证明合同/decoding，不能解除这些未验证项。

## 17. Decision

**M3 RESULT: PASS WITH RISKS。**已验证工程范围无未解决P0/P1，全部输出保持candidate，真实校准风险保留。

**Recommended Next Phase: M4 — Conditional VO₂max。**完成M3后停止；未自动进入M4、未正式promotion或产品激活。
