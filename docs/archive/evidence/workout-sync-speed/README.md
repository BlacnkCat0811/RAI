# workout-sync-speed evidence

结果与结论在 `docs/WORKOUT_SYNC_SPEED_RESULT.md`。本目录只放原始测量与计划文件，不含健康值、真实 UUID、设备标识或凭证。

## 环境

| 项 | 值 |
| --- | --- |
| 主机 | 本机 Mac（无 SSH、无远端数据库、无远端测试入口） |
| Swift | `xcrun swift test --package-path ios/RAI`，**Debug**，macOS SwiftPM 目标（不是 iPhone、不是 Release） |
| Xcode | `~/Downloads/Xcode.app`（须显式 `DEVELOPER_DIR`） |
| 模拟器 | `RAI Local Tests`，id `5DA14A13-5EFA-4E08-90F4-4B90FAD0A6B9`，iOS 26.5 x86_64 |
| 真机 | iPhone 12 Pro Max（iPhone13,4），iOS 27.2；App 为 **Release（`-O`）** + DEBUG 门控验收仪器；有线控制、WiFi 同步；后端 API/Worker/PG 在本机，库只监听 loopback，每次测量前只重置本机一次性库 |
| Go | `.local/toolchain` 的 Go；`go test -race`，integration build tag |
| PostgreSQL | 18.6（Postgres.app），一次性隔离集群；`fsync=on`、`full_page_writes=on`、`synchronous_commit=on`、`data_checksums=on` 全程未关闭 |
| 网络 | 服务端时延为**进程内** handler→PG（`httpapi.New(...)` 直接调用），不经 socket、不经限流；`ack_total_ms` 从 handler 入口（auth 之后）到响应写完，不含 auth 中间件 |

## fixture manifest

| 用途 | 生成器 | 结构 |
| --- | --- | --- |
| 客户端编码（P11） | `ios/RAI/Tests/SyncCoreTests/WorkoutPerformanceTests.swift` 的合成 bundle | 7 种 quantity + GPS 点；档位 20/40/60/120/240 分钟；`json_bytes` 逐档固定（1378707 / 2757810 / 4135099 / 8270561 / 16539690）。合成负载含随机内容，故 gzip 字节每次可能不同，长度相同**不构成**字节相等证明——字节相等由 `WireEncodingTests` 证明。 |
| 服务端时延（P06/P07） | `apps/server/integration/workout_performance_test.go` 的 `performanceBundle` | scalar density `[]int{456,887,887,884,423,428,432}*minutes/40` + GPS `2283*minutes/40`；每次迭代生成全新 Workout/sample/route 身份（真实首次导入，不是重放）。 |
| 既有矩阵 | `TestPhase4PerformanceMatrix` / `TestPhase4FreshGzipDurationMatrix` | 同上；每档只跑一次，**不能据此签 P50/P95**。 |

## 预算

| 项 | 值 |
| --- | --- |
| 编码矩阵 | 默认测试预算，单进程；受机器负载影响极大（见下） |
| 服务端时延 | `RAI_SYNC_SPEED_ITERATIONS=30`、`RAI_SYNC_SPEED_WARMUPS=3`，warmup 不入样本 |
| 上传/设备限流 | 默认预算（设备 30/min burst 2、dataset 60/min burst 4）**未**在本目录的进程内测量中生效；P08/P09 的端到端批量未测 |

## 文件

| 文件 | 内容 |
| --- | --- |
| `client/20261106-swift-encode-before.txt` | 编码基线（旧编码器）。原始 `PHASE4_SWIFT` 行当时未归档，此文件为当次读数记录；基线期 `peak_rss` 在测试里写死为 `NOT MEASURED`。 |
| `client/20261106-swift-encode-after.txt` | 新编码器，安静机器（原始 `PHASE4_SWIFT` 行，含 `process_peak_rss_bytes`）。 |
| `client/20261106-swift-encode-after-under-load.txt` | 同一份新代码在并发跑模拟器/PG 时的读数，用于说明 Debug 编码耗时对负载的敏感性。 |
| `server/20261106-sync-speed-server-ack-s60.txt` | 早期 S60 n=30 读数（generic plan 修复前），含 S240 被语句超时阻断的记录，保留作对照。 |
| `server/20261106-sync-speed-server-ack-s60-s240.txt` | **最终** S60 与 S240 各 n=30（warmups=3，0 失败）的 `T_server_ack` 与全部 import phase 的 P50/P95/min/max，含读数结论（P06 5.6 倍、P07 7.8 倍）。 |
| `server/20261106-lightweight-throughput-perf.txt` | `scripts/local/test.sh perf`：1/10/100/500 条轻量 bundle 的吞吐（**benchmark 预算**，非默认预算，不能当 P08/P09）。 |
| `server/20261106-phase4-matrix-after.txt` | 服务端 Phase4 矩阵 5 档的 timing 子集（每档单次观测，不是 P50/P95）。原始 JSON 含嵌套 `EXPLAIN`，只抽取字段落盘。 |
| `phase-timings/20261106T062029Z-d966a305-import-phases.txt` | 导入事务内 phase 分解（5000 samples + 1 series + 1 route）。 |
| `phase-timings/20261106T065340Z-3d4a0738-s240-import-phases.txt` | S240 单次导入 phase 分解（26,382 samples），`ack_total_ms=12209.602`。 |
| `phase-timings/20261106T065543Z-5665b6b9-import-phases.txt` | 另一档单次导入 phase 分解。 |
| `sql-plans/20261106T062026Z-796da7f0-*.txt` | 7 个 `EXPLAIN (ANALYZE, BUFFERS, WAL)`：sample 身份集合查询、子删除状态、series 身份、route 身份、sample 写入、关联写入、dirty scope upsert。 |

## 查询 / I/O / 刷盘计数（客户端）

| 指标 | 位置 | 观测 |
| --- | --- | --- |
| `reused_discovery_reads` / `full_sample_reads` | `SyncCoordinator` task metrics | 复用路径 10 / 0；首次路径 0 / 全量一次 |
| `sample_archive_writes` | 同上 | 按 segment 文件计（原为按 sample 计） |
| `series_capture_flushes` / `series_capture_synchronizations` / `series_capture_points` | 同上（来自 `HealthKitClient.captureBatchCounters()`） | 刷盘次数与批数同阶，不随点数增长 |
| `route_capture_flushes` / `route_capture_synchronizations` / `route_capture_points` | 同上 | 同上 |
| `workout_sync_skipped_unchanged` | 同上 | 无变化重复同步时置 1，且 0 次 POST、0 次 HK 读取 |
| `workout_query_ms` / `source_classify_ms` / `healthkit_read_ms` / `series_read_ms` / `route_read_ms` / `bundle_encode_ms` / `bundle_persist_ms` / `bundle_build_ms` / `upload_ms` / `workout_sync_ms` | 同上 | 分段计时，父项包含子项 |

## 测试执行 / 跳过清单

| 命令 | 结果 |
| --- | --- |
| `scripts/local/test.sh swift` | PASS 197 tests / 28 suites |
| `scripts/local/test.sh simulator` | PASS 66 / 63 passed / 0 failed / 3 skipped |
| `scripts/local/test.sh e2e` | PASS（`contract=v2 manifests=2 streams=12 imports=1 lost_ack=true`） |
| `scripts/local/test.sh gate` | PASS |
| `scripts/local/test.sh integration`（服务端子集） | PASS |
| `RAI_PHASE4_PERF=after ... TestPhase4PerformanceMatrix` | FAIL（240 档 gzip 身份重放 503 = 语句超时） |
| `scripts/local/test.sh perf` | NOT RUN |
| iPhone 真机 / Release | NOT RUN |

模拟器 3 项跳过均为显式外部环境依赖：B4 external closure、VO2 end-to-end、R3（需独立 PG 主机）。跳过不是通过。

## 真机（`device/`）

| 文件 | 内容 |
| --- | --- |
| `device/20261106-real-device-s60-first-import.txt` | 58 分钟真实记录，n=20，真实首导入逐样本行 + 中位数/P95（`T_user_ack` P50 19.14s / P95 19.65s）与阶段构成 |
| `device/20261106-real-device-s30-first-import.txt` | 32 分钟真实记录，n=20（P50 9.87s / P95 12.18s） |
| `device/20261106-real-device-long-record-first-import.txt` | 库内唯一 108 分钟记录，n=20（P50 1.97s）；series 型稀疏样本，不构成 S120 密度样本 |
| `device/20261106-real-device-repeat-and-restart.txt` | 已导入后连续 9 次重启进程再同步：每次 404–427ms，0 新 POST/import/job |
| `device/20261106-real-device-baseline-vs-optimised.txt` | 内部回退开关（逐条刷盘 + 旧编码器）与优化路径同机同记录对照；上传字节完全相同 |
| `device/20261106-real-device-completeness-attempt.txt` | 真机完整性 v2 尝试：15 分钟后进程被终止，未产生 receipt |

真机文件只保留数量、耗时、字节数、状态与原因；不含健康值、GPS、时间明细、稳定设备标识、真实 UUID 或原始 metadata。
