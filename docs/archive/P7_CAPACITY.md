# P7 Capacity & Performance

目标是N100/PVE/fnOS VM 4vCPU/8GB、PostgreSQL18、Go API+单slot Worker。只使用隔离合成数据；既有服务及NAS其他工作负载未隔离，结果不是生产SLO。最终数字、样本数、源码摘要和命令以 [evidence](evidence/p7-fnos-2026-10-29.json) 为准。

## 预算与测量口径

| 服务 | 容器上限 | Go/PG起点 | 证据要求 |
|---|---|---|---|
| API | 512MiB / 1CPU | GOMEMLIMIT384MiB；8连接；GOMAXPROCS1 | Docker当前working set、cgroup current/peak/CPU counters |
| Worker | 1GiB / 1CPU | GOMEMLIMIT768MiB；4连接；1slot | 包括真实analysis/training及探针的container counters |
| PostgreSQL | 2GiB / 2CPU | shared_buffers256MiB、work_mem4MiB、maintenance64MiB、40连接 | 共享内存、表/库/WAL大小、连接与默认planner |
| fnOS/缓存 | 常驻上限之外约4GiB | 不为缓存设虚假固定占用 | VM free/available、disk free与并发任务说明 |

上限不等于实际使用；CPU配额也不预留物理核。admin/backup/monitor是短任务，不额外常驻。资源命令 `rai resources` 只读cgroup v2；短暂probe自身计入计数，不能当成Go进程heap。memory.peak可能在容器stop/start后重置，source性能与恢复后soak分别记录。缺少cgroup证据必须标NOT MEASURED，不用Docker快照冒充peak。

## 场景

- 真实P3 HTTPS：1000 samples一批；10000 samples十批，维持1000-event限额；记录wire/decoded字节及注册→durable ACK时间，包含现有admission/429等待；不把单次或10批观测称P95。
- 合成长GPS：4h/14,400点，5000/5000/4400三块与seal；已有大Workout package/series/页规模专项保持正式分块协议。它们不是HealthKit读取、手机压缩或蜂窝网络吞吐。
- 1/3/5年：每天一个1h Workout，100个HR区间、300个路线点/Workout；365/1095/1825 Workouts，36,500/109,500/182,500 samples，109,500/328,500/547,500 points；两次真实空P3增量页/模拟日产生730/2190/3650 batches/pages/receipts；另有jobs/analysis runs/results与52×year训练重生成。历史raw/route/derived用SQL建立**只读容量fixture**，route synthetic receipt不是P3耐久证据；空页不代表历史非空事实上传，正式非空协议另测。
- default planner `EXPLAIN(ANALYZE,BUFFERS)` 使用实际analysis SQL，记录node/index/rows/buffers与execution时间，不禁用Seq Scan；小表或命中全表时Seq Scan可能合理。
- 各规模product first50/effective current各100次，可报告median及nearest-rank P95；P4 workout/TRIMP/state/recompute、P5 generate/merge/feedback/recalculate另跑现有non-race基线。只读SQL规模不代替API端到端或真实手机性能。
- 有界soak约3分钟：两并发读、144请求、六次正式增量上传与training再生成；之后三轮stop/start与原回执重试。n=144只适用于该有界场景；不宣称证明多年运行或消除所有泄漏。

## 可复现命令

```bash
scripts/test-remote.sh env RAI_SOURCE_COMMIT=<verified-head> bash ../../scripts/test-p7-compose.sh
scripts/test-remote.sh go test -count=1 -tags=integration,p7capacity -p 1 \
  -run '^TestP7CapacityHistories$' -v -timeout=30m ./integration
scripts/test-remote.sh go test -count=1 -tags=integration -p 1 \
  -run '^(TestAnalysisPerformanceAndCapacity|TestTrainingPerformanceAndCapacity)$' -v -timeout=30m ./integration
scripts/test-remote.sh go test -count=1 -tags=integration,scale -p 1 \
  -run '^TestScale' -v -timeout=30m ./integration
```

每条远端命令独立创建PG；大规模/benchmark不要并发运行，避免把NAS资源争用当作产品瓶颈。`test-remote`的PG默认128MiB shared_buffers/1GiB限额，与release Compose256MiB/2GiB不同；分别记录，不能混用延迟。

## 增长与限制

容量fixture采样密度明确低于每秒HR/GPS；真实采样密度、route_chunks canonical JSON与projection双存、WAL、训练/analysis重算频率都会影响增长。规划磁盘时至少用实测表/索引/TOAST大小×真实采样倍率，加备份保留与WAL/maintenance空间，并保留10GiB低水位。逻辑备份本身不能限制WAL；需要看写入与checkpoint/autovacuum，不能删pg_wal。

队列、receipts、tombstones、immutable runs与training历史仍长期保留；不得由短测试推断无限增长已解决。autovacuum与长期bloat在短窗口中只能记录统计，若没有观察到自动vacuum就明确未验证，不以手动ANALYZE代替长期验收。250,000 observations/route points分析上限仍保留，P3可提交更大sealed对象时分析会稳定失败，未加入百万点流式算法。

没有证据表明需要Redis/分区/第二数据库；本轮不引入这些组件。正式容量批准仍需真实iPhone导入密度、生产存储/冷盘、较长运行窗口与离机备份空间，不能只凭合成benchmark批准发布。

## 2026-10-29 最终观测

non-race，fnOS/N100，native Go GOMAXPROCS2；独立测试PG18.6/128MiB shared_buffers/1GiB容器。下列查询为storage+Go decode，各100次，非HTTP/手机延迟。

| 场景 | DB MiB | batches | Effective | Workout首50 median/P95 ms | 当前Effective median/P95 ms |
|---|---:|---:|---:|---:|---:|
| 1年 | 87.56 | 730 | 53 | 13.146 / 14.946 | 0.634 / 0.880 |
| 3年 | 241.84 | 2190 | 157 | 2.682 / 3.511 | 0.705 / 1.007 |
| 5年 | 394.40 | 3650 | 261 | 2.742 / 3.650 | 0.718 / 1.000 |

planner在不同规模选择不同路径，不按年份强行推断单调延迟。5年实际关联读取100 samples/300 route points，有索引路径；未以禁Seq Scan伪造命中。初轮5年maintenance ANALYZE触发2s预算，改为测试专用60s事务；生产查询2s/事实事务5s未放宽。

P4 1/3/5年365日state重算单次334.563/354.708/366.591ms；单Workout claim→publish为104.173/65.654/64.389ms（1000 HR+1000 route点）。纯feature/load固定输入100次与P5 8/16周生成100次的完整值在evidence；不能当HTTP SLO。P5两fixture单次feedback→recalculation 57.389/82.722ms。

规模专项：10000片页229.933s（含连接池重建、末片回滚/恢复）；series 1,000,000点/200 chunks全流程31.443s、seal11.558ms；大Workout 20000 details/78 chunks全流程3.873s、seal511.826ms。这些是协议/事务压力，不是手机导入吞吐或百万点分析。

规模测试PG工作集快照167.1MiB，cgroup peak触及1GiB限制（含文件cache），所有测试通过；不能将peak当进程RSS。发布模板PG预算2GiB。另一次真实HTTPS/同步/Worker窗口15.484s：API/Worker/PG平均1.65%/0.87%/3.27%单核CPU，4个采样的容器总current最大85.6MiB，VM available约4.70GiB，盘余约111.2GiB。该短窗不是整场最大资源或长期无泄漏证明。

发布Compose的正式P3 1000/10000与长Route、最终144读/6增量/6重生成soak统计见evidence。Soak约182秒，P95采用nearest rank；较早演练的floor-rank统计不作为最终P95。短窗所有last_autovacuum均未观测到，长期autovacuum/bloat明确NOT VERIFIED。
