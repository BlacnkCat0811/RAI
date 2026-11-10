# Workout 同步提速与 Apple Watch 来源过滤 — 实施结果

日期：2026-11-06（Asia/Shanghai）
依据：《Workout 同步提速与 Apple Watch 来源过滤实施方案》（`docs/WORKOUT_SYNC_SPEED_IMPLEMENTATION_PLAN.md`）
基线提交：`533fa82`（工作区含大量未提交改动，未 reset / clean / 覆盖用户现有工作）

本文记录**实际执行**的代码改动、真实测量数字、以及未执行项。计划中的目标、伪流程与验收矩阵不因本文存在而视为通过；未执行项在 §8 逐条列出。

---

## 1. 结论摘要

| 项 | 结果 |
| --- | --- |
| P1 来源提前过滤 | **已实现**：列表、discovery、手动同步三处前置分类；排除项零详细查询、零上传。**F01–F08 全部通过**（F06 覆盖全第三方场景，空清单变体走同一路径未单独跑）。 |
| P2 批量捕获 | **已实现**：segment 批量封存 + 批量刷盘，替代逐 sample 文件与逐点 `synchronize()`。F09–F15 文件级故障注入通过；真实掉电/磁盘满 NOT RUN。 |
| P3 采集复用与编码 | **已实现**：耗尽流归档复用、无变化重复同步跳过、JSON 编码器重写。**P11 通过：S60 编码 3526.48ms → 384.77ms（−89.1%）**，canonical 字节逐字节一致。 |
| P4 后端校验/SQL | **由 server-import 交付**：语义校验收口 Store 一次、身份/删除/写入集合式；S240 的 generic plan 阻断已修（事务局部 `force_custom_plan`）。服务端 unit/集成/gate 全绿，SQL 计划与 phase 分解已归档。 |
| P5 调度与完整性 | **部分实现**：分析刷新与 durable ACK 解耦。完整性证明仍未接入普通用户路径（原状），如实记录。 |
| P06 服务端时延 | **FAIL**：S60 `ack_total_ms` P95 **5646ms** / P50 4901ms（n=30，0 失败），目标 ≤1000ms（约 5.6 倍）。构成已分解到 phase。 |
| P07 S240 服务端时延 | **FAIL**：S240 `ack_total_ms` P95 **31036ms** / P50 21080ms（n=30，0 失败），目标 ≤4000ms（约 7.8 倍）。测得前先修好 generic plan 导致的 503（§4.3）。 |
| P01 设备端用户等待 | **FAIL**：真机 Release（iPhone 12 Pro Max，WiFi→本机 Mac）S60 类真实记录 P50 **19.14s** / P95 19.65s 对 ≤5s/≤8s；S30 类 P50 9.87s。同机基线 25.10s → 优化后 19.14s（**−23.7%**，未达 −50%）。瓶颈：逐样本 `NSKeyedArchiver` 8.8s + 服务端导入 ≈5s。 |
| P02 / P03 | **NOT VERIFIED / NOT RUN**：库内无 S120 密度记录（唯一 ≥100 分钟记录是 series 型稀疏样本，1.67MB），无 240 分钟记录。 |
| P04 无变化重复同步 | **PASS**：真机 9 次重启进程后重复同步，P50 **420ms**，0 新 POST/import/job。 |
| P10 / P12 真机 | P10 **PASS**（最大主线程停顿 1.8ms）；P12 S60 **FAIL**（291.6MiB 对 ≤256MiB）、S30 PASS（202.4MiB）。 |
| 真机完整性 v2 | **未完成**：运行 15 分钟后进程被终止，已导入 10 条记录，`workout_sync_completeness` 0 行（未伪造 receipt）。 |
| 部署 / RQRun 对标 | **NOT RUN / NOT VERIFIED**（本文不宣称）。 |

一句话：**来源过滤、批量捕获与编码器已实质改善并可验证；服务端时延与真机 `T_user_ack` 都已按口径实测且明确未达标（P06 5.6 倍、P07 7.8 倍、P01 约 3.8 倍，均已定位到具体阶段）；真机缺口与 RQRun 对标如实列为未验证。**

---

## 2. 测量环境与口径

- 机器：本机 Mac（macOS），无远程入口、无 SSH、无远端数据库。
- Swift：`xcrun swift test --package-path ios/RAI`，**Debug 配置、macOS SwiftPM 目标**（不是 iPhone、不是 Release）。因此所有客户端耗时只用于**同环境前后对比**，不能直接换算成 iPhone 上的 `T_user_ack`。
- 模拟器：iPhone 模拟器 `RAI Local Tests`（`5DA14A13-5EFA-4E08-90F4-4B90FAD0A6B9`，iOS 26.5 x86_64），Xcode 在 `~/Downloads/Xcode.app`，须显式 `DEVELOPER_DIR`。
- Go/PG：`.local/toolchain` 的 Go 与 PostgreSQL **18.6**（Postgres.app）；`fsync=on`、`full_page_writes=on`、`synchronous_commit=on`、`data_checksums=on` 全程未关闭；每次跑完残留库 0、集群已删。
- 服务端时延口径：进程内 `httpapi.New(...)` handler → 真实 `storage/postgres` → 一次性隔离 PG，**不经 socket、不经限流**；`ack_total_ms` 从 handler 入口（auth 之后）到响应写完，**不含 auth 中间件耗时**（证据文件内已注明）。
- 真机：iPhone 12 Pro Max（iPhone13,4），iOS 27.2，有线连接用于控制、WiFi 用于同步；App 为 **Release（`-O`）** 构建并带 DEBUG 门控的验收仪器（`SWIFT_ACTIVE_COMPILATION_CONDITIONS=DEBUG` + `Config/Info-Debug.plist`，后者只提供局域网 ATS 例外、本地网络用途说明与 `RAIDevelopmentHTTPHost`）。后端 API/Worker/PostgreSQL 全在本机，数据库只监听 loopback，每次测量前只重置这台机器上的一次性库。
- 统计：最近秩 `ceil(p*n)`；warmup 单独跑不入样本；失败不计入样本、单独计数。

---

## 3. 客户端结果

### 3.1 P11 JSON 编码（PASS）

同一合成负载（`performanceBundle`，7 种 quantity + GPS 点），同一进程内编码前后对比：

| 档 | 基线 encode_ms | 现在 encode_ms | 降幅 | json_bytes（两版相同） | gzip_ms（现在） | 进程峰值 RSS |
| --- | --- | --- | --- | --- | --- | --- |
| 20min | 1145.84 | **126.69** | −88.9% | 1,378,707 | 12.84 | 22.7 MB |
| 40min | 2347.74 | **267.63** | −88.6% | 2,757,810 | 26.23 | 37.9 MB |
| **60min** | **3526.48** | **384.77** | **−89.1%** | 4,135,099 | 37.95 | **54.4 MB** |
| 120min | 7073.38 | **765.79** | −89.2% | 8,270,561 | 76.66 | 93.6 MB |
| 240min | 14118.39 | **1623.45** | −88.5% | 16,539,690 | 154.34 | 172.2 MB |

- P11 门槛：S60 编码 P50 较基线下降 ≥50%（即 ≤1763ms）。实测 384.77ms，**余量 4.6 倍**。
- **字节一致性**：`json_bytes` 逐档与基线完全相同，但长度相同不等于字节相同。字节相等由 `WireEncodingTests` 证明：把旧编码器原样复制为 reference，对 0x00–0x1F 全控制字符、引号/反斜杠、U+2028/U+2029、emoji、5000 字符长串、24 个 double 边界（含 `-0.0`、`5e-324`、`1e-7`）、Int64 极值、嵌套与键排序逐字节 + hash 比对，**5 个用例全部通过**。
- 编码器替换只改了字节写入路径（单缓冲 `[UInt8]` 追加 + 手写转义），未改数字格式、舍入、排序或省略规则；`goNumber` 未动。
- **机器负载影响很大**：同一份 after 代码在并发跑模拟器/PG 时实测 S60 达 884.22ms、S240 达 2537.03ms。安静机器值与负载值都已归档（`client/20261106-swift-encode-after.txt`、`...-after-under-load.txt`），引用时必须注明是哪一份。

### 3.2 采集复用（P3）

- 新增 `OutboxStore.exhaustedDiscoveryFacts(workoutID:type:)`：仅当该 scoped discovery 流 `anchor != nil && staged == nil && error == nil && hasMore == false`，且每个未删除事件都有可读归档（单文件或 segment 引用）时，返回该批事实；否则返回 `nil`，组包回落一次全量读取。**不完整基线不会被当成完整快照。**
- `SyncCoordinator.samplesForBundle`：能复用则从归档还原 `HKQuantitySample`（按 `startDate, uuid` 排序，与 HK 查询顺序一致），**跳过二次全量 HK 读取，也跳过再次归档**；任一事实读不出就整体回落，绝不静默上传缺样本的包。
- 无变化重复同步：`isAlreadyDelivered` 要求「有回执 + 无待上传包 + 无 pendingReadKeys + phase ∈ {acknowledged, analyzing, published, partial} + 该 workout 的 discovery generation 已全部交付 + 没有更新代次的删除事件」，满足则**不发 POST、不读 HealthKit**，只记 `workout_sync_skipped_unchanged`。
- 实测（`WorkoutSyncReuseTests`，3 用例通过）：
  - 首次同步：`uploads == 1`、`bundleSampleReads == []`（复用 discovery 归档，10 条事实）、`full_sample_reads == 0`；
  - 第二次同步：`uploads` 仍为 1、`childReads`/`seriesReads`/`routeReads` 全部不变、`workout_sync_skipped_unchanged == 1`；
  - 迟到样本：注入新代次 discovery 事件后再次同步 → `uploads == 2`（不会被旧 ACK 吞掉）；
  - 已备原包重试：首次上传失败后 `bundleFile` 保留、`uploadReceipt == nil`；重试成功时 **HealthKit 查询 0 次**、`bundleSHA256` 未变。
- 期间修掉两个真实缺陷：`isAlreadyDelivered` 的可选值解包；`setBundle` 用捕获前的 generation 导致 `deliveredGeneration` 永远落后一代（实测 `work.generation=2, deliveredGeneration=0`），改为在组包后重读当前 generation。

### 3.3 批量捕获（P2）

- 新增 `CaptureSegments.swift`：segment 记录格式 `uint32 BE headerLen | header JSON | archive bytes` 重复 + `seal JSON | uint32 BE sealLen`；无 seal 尾部即未封存、**永不可引用**。策略阈值 512 条 / 256 KiB / 250ms 先到者。
- `captureFactSegment` 的提交体与 `recordCapturedFacts` 完全一致（同 generation、父绑定、`discovery_association_conflict`、幂等、1000 上限），因此语义事件不因换存储形态而改变。
- series 采集改为有界批缓冲，每批一次 `write` + `synchronize()`（原先每点一次），`defer` 保证取消/失败也刷出已缓冲点；route 同样批量刷盘，**文件字节格式逐字节不变**。
- Workout **根记录仍用单文件归档**（`savePayload` + `archiveFile`）：`stageDiscovery` 只对 `type == HKWorkoutTypeIdentifier && archiveFile != nil` 的事件登记 `workoutArchives`，把根记录也塞进 segment 会让可见清单变空、scoped 发现完全停摆（本轮曾引入该回归，被三个模拟器用例同时抓到，已修）。
- 崩溃点语义（F09–F15）在 `CaptureSegmentsTests`（17 用例）与 `RouteCaptureBatchTests` 中逐条覆盖：批次未封存、已封存未发布、journal 已存 cursor 未进、cursor 已进未组包、原包已备未发/ACK 丢失、ACK 已收本地保存失败。
- `collectGarbage` 只新增三处：把 `segmentFile` 纳入引用集、后缀白名单加 `.capture-segment`/`.capture-segment.tmp`、在途写者守卫（`gc_inflight_file`）。新增的独立文件不会被误删。

### 3.4 来源提前过滤（P1）

- 分类器沿用冻结的 `rai.workout.source_policy.apple_watch_only/v1`（`HealthSourcePolicy.classification`），**未修改**其判定规则、证据归一化或版本号。
- 三个执行点：
  1. `HealthKitClient.recentWorkouts`：`scan = min(max(bounded*4, bounded), 400)` 为总扫描预算，最多 4 页，`cursor = last.startDate`，页未满即停 —— 前面全是第三方时仍能继续发现后面的合格项，同时不在主线程查全历史。
  2. `SyncCoordinator.discoverChanges`：拿到根对象与最小来源证据后立即分类并持久化分类记录，**在任何 quantity/series/route 子查询之前**；`excluded`/`unclassified` 且本地没有已存在任务时直接跳过该条，其余记录继续。
  3. `performSync`：入口再判一次（覆盖列表之后来源变化或旧任务），`excluded` → `source_policy_excluded`，`unclassified` → `source_policy_pending`；两者都不会发起上传。
- 分类记录（`SourceClassification.swift`）只含身份 hash、策略版本、descriptor digest、结果与原因、受保护 source evidence；**不含原始 UUID 明文，不写 `state.json`，不进 GC 回收队列**。同 descriptor + 同策略版本不重写；descriptor 变化即重判。
- 服务端继续独立重算来源，不接受客户端声明。
- UI：默认隐藏排除项，提供「已隐藏 N 条…」说明与查看开关；非 eligible 行不可选、显示「已排除」/「待识别」与可理解文案；`source_policy_excluded` 有用户文案映射。**技术标识、hash、内部错误名不进普通界面。**
- 实测（`SourcePolicyGateTests`，4 用例通过）：三条合成记录（原生 / 第三方 / iPhone 写 Apple 健康）中，只有 eligible 的那条发生子查询；三条分类记录分别落盘；`workoutArchives` 三条都在（**可见清单不丢**）；排除项与待识别项的 `uploads == 0` 且没有任何 `workout-bundles` 请求。第四个用例覆盖「已有本地任务的 Workout 来源改成第三方后仍继续采集」（F07），分类记录更新为 `excluded`。

---

### 3.5 真机首次导入（iPhone 12 Pro Max，Release，WiFi → 本机 Mac 后端）

方法：每个样本只重置本机一次性数据库，iPhone 对同一条真实只读 Workout 做一次真实首次捕获与首次导入（新 namespace = 新 Outbox，故每次都是真实首导入而非重放）。n=20，无剔除。构建为 Release（`-O`）并带 DEBUG 门控的验收仪器；后端 API/Worker/PostgreSQL 18.6 全在本机，数据库仅 loopback。证据见 `docs/evidence/workout-sync-speed/device/`。

| 真实记录 | canonical JSON | `T_user_ack` P50 | P95 | 峰值 RSS | 主线程最大停顿 |
| --- | --- | --- | --- | --- | --- |
| 58 分钟（S60 类，10,824 samples / 10,824 series 点 / 3,491 route 点） | 8,565,743 B | **19.14s** | 19.65s | 291.6MiB | 1.8ms |
| 32 分钟（S30 类） | 3.0MB 级 | **9.87s** | 12.18s | 202.4MiB | 1.3ms |
| 108 分钟（库内唯一 ≥100 分钟记录，series 型稀疏样本） | 1,669,722 B | 1.97s | 2.07s | 125.2MiB | 3.7ms |

58 分钟记录的 19.14s 构成（20 个样本结构一致）：HealthKit 读取与捕获 12.78s，其中**逐样本 `NSKeyedArchiver` 8.8s**、series 2.32s、route 0.38s、根查询 0.02s；canonical JSON 编码 0.13s；gzip 0.07s；落盘 0.10s；上传 + 服务端导入 + ACK 5.61s。series 10,824 点只用 26 次 flush、route 3,491 点只用 7–8 次 flush（批量刷盘生效）。

真机基线对照：构建内含两个内部回退开关（`AcceptanceSwitches`，仅 DEBUG 读环境变量），分别还原「逐条/逐点刷盘」与「旧 `JSONEncoder` 写字节」。两态 `bundle_bytes` 完全相同（8,565,743），即上传字节一致，只差客户端成本。同机同记录同构建：基线 P50 **25.10s**（n=5）→ 优化后 **19.14s**（n=20），**−23.7%**。

真机无变化重复与重启恢复：同一记录在已导入后连续 9 次重新启动 App 进程再同步，每次 `workout_sync_skipped_unchanged=1`，端到端 404–427ms（P50 420ms），服务端保持 `workouts=1 / samples=10824 / imports=1 / jobs=2 / change_events=1`。

真机来源过滤（P1/F02/F05）：`list` 命令在真实库上 4 页扫描 100 条可见记录，51 条合格、49 条第三方或手工录入、0 条未分类，191ms；全部 `indoor=false`，库内无 240 分钟记录、无室内跑。

真机完整性 v2：一次尝试在 15 分钟后被系统终止，已导入 10 条可见记录，`workout_sync_completeness` 仍为 0 行 —— **未产生 receipt**，未完成。

---

## 4. 服务端结果（P4，由 server-import 交付）

### 4.1 改动

- 语义校验收口到 Store 导入入口**一次**（`ValidateWorkoutBundle` 不再在 HTTP 与 Store 各跑一遍）；HTTP 侧只保留 wire 层严格解码 + 15s decode 守卫。
- 身份检查、删除屏障、series/route 身份、关联写入改为**集合式**（有界批次 ≤1000 键，多行参数写入，无 `ON CONFLICT DO NOTHING`），判定顺序与冲突码逐条保持。
- 补齐阶段计时：`semantic_validation`、`import_transaction`、`dataset_lock`、`identity_checks`、`deletions`、`sample_writes`、`association_writes`、`series`、`routes`、`dirty_job`、`receipt`、`commit`，外加包裹 `import_total`。
- 未新增迁移/索引（最高仍 `00029`），未关闭任何 durability 设置，未扩大连接池或上传并发。

### 4.2 实测（S60 与 S240，各 n=30，全新身份的真实首次导入）

命令：`RAI_SYNC_SPEED_PERF=1 RAI_SYNC_SPEED_ITERATIONS=30 RAI_SYNC_SPEED_WARMUPS=3 scripts/local/test.sh integration -v -run '^TestWorkoutSyncSpeedServerAckMatrix$' ./integration` → **PASS（966.61s）**，两个 profile 各 30 次有效样本、**0 失败**。每次迭代都生成全新的 Workout/sample/route 身份（重放不算新导入），3 次 warmup 不计入样本；`ack_total_ms` 从 handler 入口（auth 之后）到响应写完，即 `T_server_ack`（**不含 auth 中间件**）；百分位用最近秩 `ceil(p*n)`。完整数据见 `server/20261106-sync-speed-server-ack-s60-s240.txt`。

| 指标 | S60 P50 | S60 P95 | S240 P50 | S240 P95 |
| --- | --- | --- | --- | --- |
| `ack_total_ms`（T_server_ack） | **4901.3** | **5646.0** | **21080.2** | **31036.3** |
| `client_roundtrip_ms` | 4936.0 | 5667.2 | 21133.6 | 31097.1 |
| `json_decode_ms` | 1649.6 | 1802.8 | 6571.7 | 7039.0 |
| `import_ms` | 3151.3 | 3873.3 | 14130.5 | 24141.9 |
| `semantic_validation` | 159.6 | 201.8 | 656.4 | 756.8 |
| `sample_writes` | 1068.1 | 1335.1 | 5412.7 | 13888.7 |
| `association_writes` | 1038.8 | 1379.8 | 4824.9 | 8392.0 |
| `routes` | 171.1 | 381.7 | 777.7 | 1009.1 |
| `receipt` | 122.2 | 138.5 | 562.4 | 951.2 |
| `dirty_job` | 133.4 | 149.5 | 546.8 | 666.1 |
| `identity_checks` | 116.4 | 1036.1 | 533.8 | 2324.1 |
| `commit` | 9.8 | 74.1 | 32.2 | 96.3 |
| `dataset_lock` | 0.45 | 0.53 | 0.49 | 0.67 |
| `ack_write_ms` | 0.11 | 0.14 | 0.11 | 0.19 |

**P06 判定：FAIL**（S60 P95 5646ms 对目标 ≤1000ms，约 5.6 倍）。**P07 判定：FAIL**（S240 P95 31036ms 对目标 ≤4000ms，约 7.8 倍）。

构成：两档的最大单项都是严格 JSON 解码（1650ms / 6572ms）；`sample_writes` + `association_writes` 合计 2107ms / 10238ms；ACK 写出本身不是瓶颈（0.11ms）。`identity_checks` 的 P95 远高于 P50（1036 vs 116，2324 vs 534），说明 §4.3 末尾的冷启动方差在 generic plan 修复后仍然存在。

### 4.3 曾经的 S240 阻断：generic plan（已修复）

26,382 样本的单包导入曾失败（`503 database_statement_timeout_or_cancel`），`TestPhase4PerformanceMatrix/240` 的 gzip 身份重放也随之失败。**根因不是「写入变慢了」，而是集合式身份查询在热连接上拿到 generic plan**：

- 连接池复用同一个 prepared statement，PostgreSQL 执行若干次后切到 generic plan；generic plan 看不到 `unnest($2::text[], $3::text[])` 的基数，为每批 1000 个键选了逐键 nested loop。同一事务、同一批键、同一数据的实测对比：**custom plan 31ms vs generic plan 3.4s**（差 100 倍以上）。
- 连接默认 `statement_timeout=2000` 只是**把问题暴露成干净错误**的那一层：把语句预算抬到 30s 后，它只会变成整事务 45s 超时（`25P04`）。只抬超时不能修好，只会换一个错误码。
- 修复：导入事务内 `SET LOCAL plan_cache_mode='force_custom_plan'`，连同有界的 `statement_timeout='30s'` 与既有 `transaction_timeout='45s'`，三条都是**事务局部**，全局 2s 默认与其它上限一律未动。对照实验（只加 `plan_cache_mode`、保持默认 2s）20k 单包同样通过，说明 30s 是余量而不是承重墙。
- 回归证据：新用例 `TestWorkoutBundleLargeBatchImportSurvivesConnectionStatementTimeout`（20,000 samples，真实 HTTP handler + gzip）**PASS 14.49s**，断言 `SamplesInserted = AssociationsInserted = 20000`、`health_samples = workout_samples = 20000`；同包 gzip 重放**响应字节与首次完全相同**、`jobs=1`、`workout_imports=1`。
- 修复后 `TestPhase4PerformanceMatrix` 与 `TestPhase4FreshGzipDurationMatrix` 全部档位 **PASS**（103.08s + 58.29s，240 档首发 200、重放 200、`worker_state=succeeded`）。
- 首次迭代的 `identity_checks` 仍会到 12–18 秒（冷缓存/计划选择），稳态约 100ms。warmup 已单列，但 iteration 0 计入样本，故 `max` 远离 P95。`dirty_job` 的 145ms 抖动只被证明「不是 N+1、不是多余 scope 写」（固定 4 条语句），根因未定位，如实记为未验证。

### 4.4 SQL 计划证据

`docs/evidence/workout-sync-speed/sql-plans/`（runID `20261106T062026Z-796da7f0`）：sample 身份集合查询 1000 键 10.1ms 走 `health_samples_analysis_identity` Bitmap Index Scan；子删除状态 4.9ms；series 身份 0.06ms；route 身份 0.05ms；sample 写入 44.4ms（WAL 4066 records / 1,039,674 bytes）；关联写入 53.8ms（WAL 4038 / 718,352）；dirty scope upsert 0.30ms。

---

## 5. 测试执行结果（真实）

| 命令 | 结果 |
| --- | --- |
| `scripts/local/test.sh swift` | **PASS 197 tests / 28 suites（21.3s）** |
| `scripts/local/test.sh simulator` | **PASS：71 tests / 68 passed / 0 failed / 3 skipped**（skip = B4 external closure、VO2 end-to-end、R3，均需独立 PG 主机） |
| `scripts/local/test.sh e2e` | **PASS**：`UA1_R_SIMULATOR_HTTP_PG=PASS contract=v2 manifests=2 streams=12 imports=1 lost_ack=true`；残留库 0、集群已删 |
| `scripts/local/test.sh integration`（服务端子集） | PASS（server-import：必需子集 110.9s 含新增 20k 单包用例、Phase4/故障注入子集 84.1s、SQL 计划用例 7.1s） |
| `TestWorkoutBundleLargeBatchImportSurvivesConnectionStatementTimeout`（20,000 samples，真实 handler + gzip） | **PASS 14.49s**：counts 20000/20000、`health_samples = workout_samples = 20000`；同包 gzip 重放响应字节与首次完全相同、`jobs=1`、`workout_imports=1` |
| `scripts/local/test.sh gate` | **PASS**（本轮收尾跑 6s：Go race 走缓存 + vet + 258 包依赖边界 + 控制器安全 4 例；server-import 侧独立跑为 18s） |
| `RAI_PHASE4_PERF=after ... TestPhase4PerformanceMatrix` + `TestPhase4FreshGzipDurationMatrix` | **PASS**（103.08s + 58.29s，含 240 档；修复 §4.3 的 generic plan 后从 FAIL 转为 PASS）。每档只跑一次，不是 P50/P95。 |
| `RAI_SYNC_SPEED_PERF=1 ... TestWorkoutSyncSpeedServerAckMatrix`（P06/P07 harness） | **PASS 966.61s**：S60 与 S240 各 30 次有效新导入、0 失败 |
| `scripts/local/test.sh perf`（benchmark 预算，轻量 60/3/3） | **PASS 34s**：1/10/100/500 条分别 47.4ms / 420.6ms / 4100ms / 19180ms（26.1 条/s，吞吐平坦）；每行 `immutableReplay=true`、`rowCountsVerified=true`。**这是 benchmark 预算（burst 1024、60000/min），不是默认预算，不能当 P08/P09 读数** |
| `scripts/local/test.sh full`（全量完成门禁） | **PASS 1312s**：`go mod verify` + `go test -race -count=1 ./...` + `go vet -tags=integration ./...` + `go build ./...` + `sqlc generate` 与 `dbgen` diff 检查 + **全部** PG 集成测试（`ok rai/server/integration 1068.843s`），残留库 0、集群已删 |

本轮新增测试：`WireEncodingTests`（5）、`SourceClassificationTests`（4）、`DiscoveryReuseTests`（6+1）、`SourcePolicyGateTests`（4）、`WorkoutSyncReuseTests`（3）、`WorkoutListPagingTests`（4），以及 ios-capture 的 `CaptureSegmentsTests`（17）、`RouteCaptureBatchTests`；**既有断言零放宽**。唯一的 fixture 调整是给合成 HK 记录补上它本意描述的 Apple Watch 描述符（`productType Watch7,1` + `com.apple.health`），因为来源门提前后这些记录被正确判为 `source_unknown`。

---

## 6. 验收矩阵 F01–F32 逐项状态

| ID | 状态 | 依据 / 缺口 |
| --- | --- | --- |
| F01 | **PASS** | Swift `WorkoutSourcePolicyTests` 与 Go `TestWorkoutSourcePolicyGoldenVectors` 共用冻结 vectors；`SourcePolicyGateTests` 证明原生跑步可同步。 |
| F02 | **PASS（App 层）** | `SourcePolicyGateTests`：第三方/手工录入零详细查询、零上传。服务端 import/事实/job 增量为 0 由「未发起上传」推出，无独立 PG 计数断言。 |
| F03 | **PASS** | 缺字段/矛盾描述 → `source_unknown`（`SourceClassificationTests`）；同批其他记录照常同步；服务端 v2 仍不因未知而最终提交。 |
| F04 | **PASS** | `displayNamesNeverChangeClassification`：名称/设备昵称任意值不改判，第三方 App 名叫 "Apple Watch" 仍是 `third_party`。 |
| F05 | **PASS（App 层）** | `WorkoutListPagingTests`（4 例，通过 DEBUG 页源 seam）：100 条第三方更新时仍找到后面的合格项（2 页）；全第三方 1000 条时扫描被限制在 ≤400 行 / ≤4 页且 `eligibleWorkouts=0`；游标严格递减、无重复记录；首屏不满即停（1 页）。`HKHealthStore` 本身仍不可注入，真实 400 条历史未在设备上跑。 |
| F06 | **PASS（全第三方场景）** | App 层：全第三方清单不建任务、不上传、不进重试队列（`SourcePolicyGateTests` + 分页用例 `testScanStaysBoundedWhenEverythingIsExcluded`）。服务端：`TestWorkoutCompletenessV2ThirdPartyOnly`（真实 PG）5 条全排除 → `receipt.EligibleCount=0 / ExcludedCount=5 / WorkoutCount=0`，exclusion evidence 与 `change_events` 计数均落库，未伪造 receipt。空清单变体走同一条 eligible=0 路径，未单独跑。 |
| F07 | **PASS（App 层）** | `testExistingWorkKeepsCollectingAfterItsSourceChanges`：已有本地任务的 Workout 在 descriptor 改成第三方后**仍继续采集**（子查询次数增加），分类记录更新为 `excluded`，删除屏障不被当前来源筛选跳过。真实 PG 并发侧的父子删除由 F24 覆盖。 |
| F08 | **PASS（服务端，真实 PG）** | `TestWorkoutCompletenessV2RejectsUnexplainedInventory` 逐项拒绝：`forged_evidence`（篡改 `evidenceSHA256`）、`evidence_reclassifies`（把第三方证据改成 Apple Watch 描述符）、`policy_version`（换策略版本）、`classification_version`、`count_only`/`missing_exclusion`/`unclassified`/`excluded_imported`，并断言失败 manifest **未落库**（`workout_sync_completeness = 0`）；`TestWorkoutCompletenessV2HTTPAndImmutability` 另证已提交的 exclusion evidence 不可 UPDATE/DELETE。 |
| F09 | **PASS** | `CaptureSegmentsTests`：未封存尾部不可引用、旧 cursor 不前进、封存前缀可恢复。 |
| F10 | **PASS** | 已封存未发布 → 孤儿；`reclaimOrphanCaptureSegments` 不删在途/仍引用文件。 |
| F11 | **PASS** | journal 已存 cursor 未进 → 重启幂等晋升，不重复语义事件。 |
| F12 | **PASS（文件级）** | 失败/取消保留 pending 与旧权威状态，不提前 GC。真实磁盘满、FileProtection 拒绝 NOT RUN。 |
| F13 | **PASS** | 旧 archive、新 segment、混合状态均可恢复；`LocalState` 结构未变，`DiscoveryEvent` 只加可选字段。 |
| F14 | **PASS** | route/series 前缀变化 → 保留旧捕获、标冲突，不拼接不同版本。 |
| F15 | **PASS** | 10 倍密度 + 高频取消：刷盘次数与批数同阶（计数断言），无逐点刷盘。 |
| F16 | **PASS** | 首次路径全量读一次并归档；`WorkoutSyncReuseTests` 证明复用路径 `bundleSampleReads == []`。 |
| F17 | **PASS** | 基线不完整 → `exhaustedDiscoveryFacts` 返回 nil → 受控全量补读，不重设 anchor。 |
| F18 | **PASS** | 无变化 0 上传 0 子读；迟到样本重开同步（`testNewDiscoveryGenerationReopensTheSync`）。 |
| F19 | **PARTIAL** | 包准备后新变化不改旧包、删除事件阻止跳过（代码 + 用例）；先删除后上传的 **PG 并发**断言 NOT RUN（属服务端 F24）。 |
| F20 | **PASS** | `WireEncodingTests` 5 例逐字节一致；五档 `json_bytes` 与基线完全相同。 |
| F21 | **PASS（服务端）** | 服务端 unit/集成子集全绿；语义校验单次执行后错误语义与冲突码逐条保持。 |
| F22 | **PASS** | plain/gzip 重放返回首次原回执，revision/job/import 不增加（集成 + e2e）。 |
| F23 | **PASS（语义断言）** | 冲突码与整包回滚由断言覆盖；**新旧实现同输入对拍 NOT RUN**（旧路径已删除）。 |
| F24 | **PASS（语义断言）** | 两种锁顺序由断言覆盖；同输入对拍 NOT RUN（同上）。 |
| F25 | **PASS** | 各写入阶段故障注入 + COMMIT 失败 + 连接中断：同成同败，无成功 ACK。 |
| F26 | **PASS** | e2e `lost_ack=true`：保留原文件/身份、恢复原回执、恰好一次语义提交。 |
| F27 | **PASS** | 429/5xx/断网/超时/401/session 变化的预算与退避（既有用例 + 客户端退避实现）。 |
| F28 | **PASS** | durable ACK + `store.synced()` 即用户可见完成点，分析刷新改为后台任务，不阻塞下一条。 |
| F29 | **PASS** | e2e `manifests=2`：原 manifest 身份恢复，第二代用 committed anchors、`bootstrap=0`。 |
| F30 | **PASS（既有覆盖，真实 PG + readiness 消费者）** | `workout_completeness_test.go`：stale generation 与旧 collector 重放均被拒；此后 `readiness` 读出 `Coverage.CaughtUp=false` 且 reasons 含 `load_sync_incomplete`——即模型侧保持 unknown/incomplete，不填零负荷。`workout_completeness_simulator_test.go` 亦断言证明通过前不含该 reason、通过后才 `CaughtUp=true`。本轮未改动模型代码。 |
| F31 | **PARTIAL** | 缺 HR/无功率/室内无路线的合法保存与缺失区分由既有用例覆盖；本轮未新增用例。 |
| F32 | **PARTIAL** | 手动任务抢占/取消/恢复有既有 `ForegroundSyncSchedulerTests`；UI 响应 P95 未测（见 P10）。 |

---

## 7. 性能矩阵 P01–P12

| ID | 状态 | 数字 / 缺口 |
| --- | --- | --- |
| P01 | **FAIL** | 真机 Release：S60 类（58 分钟，8.57MB）P50 **19.14s** / P95 19.65s 对 ≤5s/≤8s；S30 类（32 分钟）P50 9.87s / P95 12.18s 同样 FAIL。相对同机基线（25.10s）仅 **−23.7%**，未达「较 P0 下降 ≥50%」。瓶颈是逐样本 `NSKeyedArchiver` 8.8s 与服务端导入 ≈5s。 |
| P02 | **NOT VERIFIED** | 库内唯一 ≥100 分钟记录是 series 型稀疏样本（bundle 1.67MB，约为 S120 profile 的 1/5），不构成 S120 密度样本；其观测 P50 1.97s / P95 2.07s 仅作真实记录观察，不签署 P02。 |
| P03 | **NOT RUN** | 真实库无 240 分钟记录（最长 108 分钟）；不合成写入真实 HealthKit。 |
| P04 | **PASS** | 真机端到端：已导入记录连续 9 次重启进程再同步，P50 **420ms**（404–427ms）对 ≤2s/≤4s；0 次新 POST、0 次新 import/job/change event，`workout_sync_skipped_unchanged=1`。 |
| P05 | **PASS（客户端）** | 已备原包重试 HealthKit 查询 **0 次**、bundle hash 不变。恢复耗时未单独测。 |
| P06 | **FAIL** | P95 **5646.0ms** / P50 4901.3ms 对 ≤1000ms（n=30，S60，真实 handler→PG，0 失败）。约 5.6 倍。 |
| P07 | **FAIL** | P95 **31036.3ms** / P50 21080.2ms 对 ≤4000ms（n=30，S240，0 失败；先修好 §4.3 的 generic plan 才能测）。约 7.8 倍。 |
| P08 | **NOT MEASURED** | 需 10 条端到端 + 限流等待。 |
| P09 | **NOT MEASURED** | 需 100 条 S60 + 默认预算的端到端（含 429 等待）。仅有 benchmark 预算下的轻量 100 条 4.10s / 500 条 19.18s（吞吐平坦，见 `server/20261106-lightweight-throughput-perf.txt`），二者不可互换。默认预算理论下界 196s（100 条）/996s（500 条）已按方案记录。 |
| P10 | **PASS** | 真机同步期间 main-queue watchdog：最大停顿 **1.8ms**（58 分钟记录，n=20）、3.7ms（108 分钟记录），无任何样本出现 >250ms 停顿。滚动/暂停手势本身未做 UI 自动化，停顿以同步期间的 tick 延迟衡量。 |
| P11 | **PASS** | S60 编码 **384.77ms** 对基线 3526.48ms（−89.1%），目标 ≤1763ms。 |
| P12 | **PARTIAL** | 真机进程峰值 RSS：S60 类 **291.6MiB（中位）/305.4MiB（最大）** 对 ≤256MiB → **FAIL**；S30 类 202.4MiB、108 分钟记录 125.2MiB 在限内。S240 无真实记录（NOT RUN）。API 进程两上传槽 RSS 仍 NOT MEASURED。 |

---

## 8. 未执行项（NOT RUN / NOT VERIFIED）

1. **真机缺口（已执行的部分见 §3.5）**：P02 无 S120 密度真实记录（NOT VERIFIED）；P03 无 240 分钟记录（NOT RUN）；P12 的 S240 与 API 进程 RSS 未测；真机完整性 v2 未完成（运行 15 分钟后进程被终止，未产生 receipt）；UI 手势（滚动/暂停/取消）未做自动化，P10 以同步期间主线程停顿衡量。
2. **RQRun 公平对标**：NOT VERIFIED。本次未获得可配对的 RQRun 同记录、同首次/缓存状态、完整起止时点与相近网络条件，因此不签署「达到 RQRun 同等速度」。
3. **批量端到端（P08/P09）**：NOT RUN。默认预算 + S60 的组合未跑；只有 benchmark 预算下的轻量吞吐（§5），二者不可互换。
3. **部署、真实数据迁移、RQRun 公平对标**：NOT RUN / NOT VERIFIED。抓包里的 2–3 秒是上传请求间隔，不是端到端时间，不用于对标。
4. **真实磁盘满 / FileProtection 拒绝 / 掉电**：未注入，只有文件级故障注入与 `.atomic` 语义。

已覆盖但仍有明确缺口（不写成完整通过）：

5. **F23/F24 的新旧实现同输入对拍**：旧路径已删除，只有按合同语义的逐条断言。
6. **F06 的空清单变体**：与全第三方走同一条 eligible=0 路径，未单独跑。
7. **F05 的设备端真实历史**：分页规则已由合成历史覆盖，400 条真实历史的扫描耗时未测。
8. **F30 的真机联动**：由既有真实 PG + readiness 用例覆盖，未做真机端到端。
9. **F32 的 UI 响应**：沿用既有调度器用例，P95 未测。

---

## 9. 残余风险

| 风险 | 现状 |
| --- | --- |
| 服务端 P95 距目标约 5 倍 | 已分解到 phase：严格 JSON 解码 1.8s + 两条多行写入 1.25s 是主项。属于后续优化范围，本轮不达标即如实记录。 |
| S240 单包 26k 样本曾撞语句超时 | **已修复**（generic plan → 事务内 `plan_cache_mode='force_custom_plan'` + 有界 `statement_timeout='30s'`，见 §4.3），240 档与 20k 单包用例均通过。50k 单包仍会撞既有 15s decode 守卫（实测 decode ≈12.4s），非本轮引入，未改动，单列风险。 |
| `force_custom_plan` 的规划开销 | 导入事务内每条语句约 +1.5ms 规划（S240 约 +80ms，可忽略）。它是事务局部设置；若将来有语句依赖 generic plan 省规划时间，需在该处单独评估。 |
| 首次导入 `identity_checks` 冷启动 12–18s | 未定论（冷缓存/计划翻转/IO），稳态约 100ms；已在证据中保留原始迭代日志。generic plan 修复后仍需复核该冷启动是否消失。 |
| `dirty_job` 145ms 抖动 | 只被证明「不是 N+1、不是多余 scope 写」（固定 4 条语句：dirty_scopes upsert → jobs insert → UPDATE datasets → change_events insert），根因未定位，记为未验证。 |
| 客户端耗时受机器负载影响巨大 | 同一代码 S60 编码 384.77ms 与 884.22ms 都真实；引用必须带负载条件。 |
| 根记录归档形态变化 = 新 bundle hash | 服务端按新导入处理（新增 revision/job），是预期行为，不是冲突。 |
| 完整性证明未接入普通用户路径 | 现状未变，本轮未引入降级对象；模型 `load_sync_incomplete`/unknown 语义保持。 |

---

## 10. 复现命令

```bash
cd /Users/ta/RAI

# 客户端编码（P11）：before/after 各跑一次，注意机器负载
RAI_PHASE4_PERF=before scripts/local/test.sh swift --filter WorkoutPerformanceTests.testDurationMatrix
RAI_PHASE4_PERF=after  scripts/local/test.sh swift --filter WorkoutPerformanceTests.testDurationMatrix

# 字节一致性（P20/F20）
scripts/local/test.sh swift --filter WireEncodingTests

# 来源过滤与采集复用
scripts/local/test.sh swift --filter 'SourceClassificationTests|DiscoveryReuseTests|CaptureSegments'

# App 层类型检查与模拟器回归
export DEVELOPER_DIR=$HOME/Downloads/Xcode.app/Contents/Developer
scripts/local/test.sh e2e          # 仅构建：RAI_UA1_R_BUILD_ONLY=1
scripts/local/test.sh simulator

# 服务端时延（P06/P07）
RAI_SYNC_SPEED_PERF=1 RAI_SYNC_SPEED_ITERATIONS=30 RAI_SYNC_SPEED_WARMUPS=3 \
  scripts/local/test.sh integration -run '^TestWorkoutSyncSpeedServerAckMatrix$' ./integration

# 既有性能矩阵
RAI_PHASE4_PERF=after scripts/local/test.sh integration \
  -run '^TestPhase4PerformanceMatrix$|^TestPhase4FreshGzipDurationMatrix$' ./integration
```

---

## 11. 签核

| 签核项 | 结论 |
| --- | --- |
| SOURCE FILTER | **通过（App 层 + 服务端）**：F01–F08 全部通过。F05 分页规则、F06 全第三方零上传零任务、F07 已有任务继续采集、F08 篡改证据被拒且不落库。默认列表与采集入口确实过滤，可见清单与删除屏障不丢。 |
| DATA SAFETY | **通过（文件级 + 服务端）**：F09–F27 中除 F19 的 PG 并发子项与 F23/F24 的同输入对拍外均已覆盖；真实隔离 PG 原子性、删除优先、精确身份、故障注入通过。 |
| NORMAL SYNC | **通过（合成）**：F28–F30 通过（F30 由既有真实 PG + readiness 用例覆盖），F31/F32 沿用既有用例（F32 的 UI 响应 P95 未测）。 |
| PERFORMANCE | **未通过**：P11 PASS；P04、P10 PASS；P06/P07 实测 **FAIL**（5.6 倍 / 7.8 倍）；P01 真机实测 **FAIL**（19.14s 对 5s，且相对基线仅 −23.7%）；P12 **FAIL**（S60 291.6MiB）；P02 NOT VERIFIED、P03/P08/P09 NOT RUN。 |
| REAL DEVICE | **部分执行，未达标**：已在 iPhone 12 Pro Max（iOS 27.2，Release，WiFi→本机 Mac）完成真实只读采集（58/32/108 分钟三条记录，各 n=20）、9 次重启后重复同步、来源过滤 100 条真实清单、主线程与 RSS 采样；**P01/P12 未达标**，P02 NOT VERIFIED，P03 NOT RUN，完整性 v2 未完成。真机未覆盖项逐条列在 §8。 |
| RQRUN COMPARISON | **NOT VERIFIED** |
| DEPLOYMENT | **NOT RUN** |

本文可交付的是「合成工程通过 + 服务端时延已量化到 phase + 客户端编码大幅改善」，**不是**「真机同步目标达成」，也**不是**「服务端时延达标」。

---

## 12. 改动文件清单与回退

工作树在 `533fa82` 之上**本来就带着未提交改动**（`AGENTS.md`、`README.md`、`deploy/local/README.md`、`scripts/local/*`、`scripts/*`、`docs/IMPLEMENTATION.md`、`ios/RAI/App/RAIApp.swift` 等来自前序本机开发轮次）。本轮没有 reset / clean / checkout / stash，也没有覆盖这些改动；下面是本轮实际触碰的文件。

| 阶段 | 文件 |
| --- | --- |
| P1 来源过滤 | `ios/RAI/Sources/SyncCore/SourceClassification.swift`（新增）、`AppTests/WorkoutListPagingTests.swift`（新增，F05）、`WorkoutSync.swift`（`HealthWorkoutSummary` 加 `sourceClassification`/`sourceOutcome`/`isEligible`/`isExcluded`/`isUnclassified`）、`App/HealthKitClient.swift`（`recentWorkouts` 有界分页 + `summary(_:)` 分类；分页循环抽出的 `runningWorkoutPage` 与 **仅 DEBUG** 的 `setWorkoutPageSource` 测试 seam，App 运行时不设置，出厂路径不变）、`App/SyncCoordinator.swift`（`discoverChanges` 门、`performSync` 入口判定）、`App/ProductViews.swift`（隐藏/说明/不可选/文案）、`App/ProductModel.swift`（`source_policy_excluded` 文案）、`AppTests/SourcePolicyGateTests.swift`（新增）、`Tests/SyncCoreTests/SourceClassificationTests.swift`（新增） |
| P2 批量捕获 | `Sources/SyncCore/CaptureSegments.swift`（新增）、`HealthIntegrity.swift`（`RouteCaptureFile` 批量刷盘）、`HealthDiscovery.swift`（`DiscoveryEvent` 可选 segment 字段）、`OutboxStore.swift`（segment 提交/封存/孤儿回收、GC 引用与白名单）、`App/SyncCoordinator.swift`（`discoverPage` 与 `performSync` 走 segment，根记录保留单文件）、`App/HealthKitClient.swift`（series 批缓冲）、`Tests/SyncCoreTests/CaptureSegmentsTests.swift`（新增） |
| P3 复用与编码 | `Sources/SyncCore/Wire.swift`（单缓冲编码 + 手写转义）、`Sources/SyncCore/DiscoveryReuse.swift`（新增）、`App/SyncCoordinator.swift`（`samplesForBundle`、`isAlreadyDelivered`、`setBundle` 代次、分段 timings）、`Tests/SyncCoreTests/WireEncodingTests.swift`、`DiscoveryReuseTests.swift`（新增）、`WorkoutPerformanceTests.swift`（真实 RSS）、`AppTests/WorkoutSyncReuseTests.swift`（新增） |
| P4 服务端 | `apps/server/internal/{httpapi/server.go, storage/postgres/{workout_bundles,bundle_integrity,bundle_batching}.go, syncing/{bundle_decode,phase_timing}.go}`、`apps/server/integration/{workout_bundle_batching_test.go, workout_performance_test.go}`（由 server-import 交付） |
| P5 调度 | `App/SyncCoordinator.swift`（`scheduleAnalysisRefresh`，分析刷新不再阻塞 ACK 之后的用户可见完成点） |
| 测量与文档 | `apps/server/integration/workout_sync_speed_test.go`（新增，P06/P07 harness）、`docs/WORKOUT_SYNC_SPEED_RESULT.md`、`docs/evidence/workout-sync-speed/**`、`docs/IMPLEMENTATION.md`（追加结果节）、`ios/RAI/RAI.xcodeproj/project.pbxproj`（登记 3 个新 App 测试文件） |

**协议与合同未变**：`contracts/` 未修改，Workout Protocol 3、来源策略 v1、完整性 v2 均按原文；未新增迁移或索引（最高仍 `00029`）；`state.json` 结构未变，`DiscoveryEvent` 只加可选字段，旧 Outbox 可原样读取。

**回退**：本轮没有引入用户可见开关，也没有引入新的协议分支，因此回退是代码级 + 运行时兼容两条：

- 运行时：采集复用失败即回落一次全量读取（`exhaustedDiscoveryFacts` 返回 nil）；旧 pending 包与其原 hash 原样保留并优先恢复；旧 `archiveFile` 单文件与 segment 两种表示并存，读取器都支持；`Wire` 新旧编码器字节一致，回退不改变任何已有包的 hash。
- 代码级：按上表逐阶段回退即可，各阶段之间没有隐式依赖（P3 依赖 P2 的 segment 表示，但 P2 的 legacy 单文件路径始终保留）。已准备的 pending 包不因回退被删除，也不因回退被改写为成功。

### 真机验收新增/修改（本轮）

- `scripts/local/device.py`：新增 `reset`（只替换本机一次性数据库；把上一条 prepared credential 按 operation id 归档而不是覆盖）、`list` 命令、`start_stack`/`stop_stack` 抽取、`--workout` 私有选择器、`RUN`/`DERIVED`/`CONFIGURATION`/端口环境变量、报告白名单（含主线程停顿与 RSS）、回退开关转发。
- `scripts/local/device_sample.py`（新增）：真机重复首导入采样驱动（每样本重置本机库、逐样本归档、中位数与最近秩 P95，n<20 时不签 P95）。
- `ios/RAI/Sources/SyncCore/AcceptanceSwitches.swift`（新增）：仅 DEBUG 读环境变量的两个回退开关（`RAI_ACCEPTANCE_LEGACY_CAPTURE_IO`、`RAI_ACCEPTANCE_LEGACY_ENCODER`），非 DEBUG 构建恒为 false。
- `ios/RAI/Sources/SyncCore/Wire.swift`：新增 `legacyData()`（旧 `JSONEncoder` 写字节路径）与 `Wire.object(_:legacy:)`，仅由回退开关使用；`WireEncodingTests.legacySwitchPathMatchesReference` 证明两路径字节相同。
- `ios/RAI/Sources/SyncCore/CaptureSegments.swift`：`CaptureSegmentPolicy.standard` 改为按回退开关返回逐条策略。
- `ios/RAI/App/RAIApp.swift`：DEBUG 验收命令新增 `list`、主线程 watchdog、进程峰值/当前 RSS、`stateBytes` 容忍缺失。
- 证据：`docs/evidence/workout-sync-speed/device/`（6 个文件）。
