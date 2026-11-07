# RAI 数据同步链路修复与优化实施方案

日期：2026-11-07（Asia/Shanghai）

状态：**待执行的修复方案，不是实现、性能或验收通过证明。**

基线：`main / 18eab55`，工作区存在未提交改动。执行前必须重新确认 HEAD、`AGENTS.md`、迁移校验和与当前设备状态，以执行时的实际代码为准。

范围：App → 云端的 Workout 发现、捕获、组包、上传、完整性核验，以及云端 → App 的产品读取、详情、分析状态与变更通知。本文只定义方案和验收条件，不修改代码、不部署、不迁移真实数据。

---

## 1. 结论摘要

### 1.1 已确认的根因

这次问题不是单点 SHA-256 过多，而是四类问题叠加：

1. **本地状态模型不适合当前数据量**：所有任务、发现事件、完整性分页、收据都放在一个 `state.json` 里，每次提交全量编码、全量原子写。设备上已观察到约 116 MB 的 `state.json`，单次提交把整份状态重写一遍，导致同步越跑越慢、写放大和内存峰值持续上升。这是现场症状，执行阶段必须用带计量的构建复现并记录，不能直接把该数字当作仓库内证据。
2. **完整性核验按“每轮全量”工作**：每个新周期都重新读取 200 天 inventory、重新采集所有 Workout 的所有流、重新生成全部 proof；首次完整性周期还对每个 Workout `forceNewBundle`。一个 Workout 的元数据问题会让整轮在它之前停住，后续 Workout 无法推进；下次又从同一位置重来。
3. **同一份不可变数据被反复读取和哈希**：multipart 包被当作 JSON 解析（真实缺陷）、包在构建/上传/完整性阶段多次全量解码、完整性分页在 cursor/stage/ack/deliver 多处重复哈希、series 在一次刷新里被读取最多三次。服务端在 dataset 行锁内做规范化、身份比较和大 payload 校验，进一步放大锁持有时间。
4. **回程没有增量通道和本地读缓存**：App 不消费 `/v2/sync/changes`，首页 6 个 GET 串行，详情串行 detail → series → freshness，服务端三个详情接口各自全量装载同一份数据；分析发布没有 change event，客户端只能一次性刷新或等下次恢复。

### 1.2 分期结论

| 阶段 | 目标 | 是否阻塞发布 |
| --- | --- | --- |
| Phase 0 | 建立状态体积、提交字节、哈希次数、完整性读取、锁持有、回程请求数的基线 | 是 |
| P0 | 修正确性缺陷：multipart 解析、`.unsupported` 语义、单 Workout 故障隔离、ACK 清理、无变化提交、分析跟进 | 是 |
| P1 | 用 SQLite 分段持久化替换整份 `state.json`，做事件裁剪、任务压缩和文件 GC | 是 |
| P2 | 完整性增量核验：复用 proof、只读变化 Workout、服务端 proof 缓存、去掉无意义 `forceNewBundle` | 是 |
| P3 | 服务端锁内工作削减、原始 payload 热路径治理、上传准入队列、Worker 分道 | 是 |
| P4 | 回程 changefeed、缓存/ETag、详情轻量装载、分析状态跟进 | 是 |
| P5 | 微优化：重复构造、归档格式、日志与低价值哈希 | 否 |

### 1.3 成功定义

本方案完成时必须同时满足：

- 不丢事实、不复活已删除 UUID、重试仍返回原回执、完整性不靠猜测放行。
- `state.json` 不再位于正常同步路径；本地元数据体积有上界，普通提交不再全量重写。
- 无变化的完整性后续周期不再重读 200 天全部 Workout。
- 同一个 Workout 的同一份不可变字节，在“封存 → 上传 → ACK”链路中不被反复全量哈希/解码。
- 服务端 dataset 行锁只覆盖必须保持提交顺序的数据库工作，不覆盖 JSON 解码、规范化和纯 CPU 哈希。
- 回程有增量失效路径，首页/详情不再靠串行全量请求刷新。
- 原有 R2 性能门槛**不因本文自动视为通过**；未达成的门槛必须以证据记录，不能用新指标替代旧门槛。

---

## 2. 已验证事实与对原审查报告的纠正

### 2.1 证据分级

本文使用四级证据：

| 级别 | 含义 | 可用于 |
| --- | --- | --- |
| E0 | 代码阅读、调用链、静态计数 | 定位问题和确定方案 |
| E1 | 单元测试、Swift 包测试 | 正确性回归 |
| E2 | 隔离 PostgreSQL 18 集成测试、模拟器/e2e | 事务、恢复、端到端路径 |
| E3 | 本机真实后端 + 真机只读验收 | 设备时延、RSS、真机行为 |

“文件存在”“mock 通过”“单元测试通过”不能替代 E2/E3。任何性能结论必须带构建类型（是否 `-race`）、数据档位、样本数、失败数和环境。

### 2.2 客户端已验证问题

| 编号 | 事实 | 位置 | 后果 |
| --- | --- | --- | --- |
| C-01 | 每次提交 `JSONEncoder().encode(next)` 后原子覆写整份 `state.json` | `ios/RAI/Sources/SyncCore/OutboxStore.swift` 的 `commit` | 写放大、主线程/actor 阻塞、内存峰值、状态越大越慢 |
| C-02 | 发现事件只增不裁，任务和 `captureManifest` 在 ACK 后仍保留 | `HealthDiscovery.swift`、`OutboxStore.swift` | `state.json` 无界增长；116 MB 现场症状与此一致 |
| C-03 | `coverageFromBundle` 把 multipart 文件当 JSON 解码 | `ios/RAI/App/SyncCoordinator.swift` 的 `coverageFromBundle`；组包时保存的是 `multipart` | 解码必然失败并静默回退到旧 `task.coverage`；上传的 coverage 可能与包内真实 observation 不一致 |
| C-04 | `WorkoutCoverage.isAvailable` 不包含 `.unsupported`；`missing` 由 `!isAvailable` 计算 | `WorkoutSync.swift`、`SyncCoordinator.swift` 的 `updateTask` | `cadence`、`elevation_sample` 永远进入 `missing`，任务可能永久 `.partial` |
| C-05 | 完整性每轮复用 `flow.inventory` 并重新采集/证明全部 Workout；每个 Workout `forceNewBundle: true` 后还重读 series | `SyncCoordinator.swift` 的 `syncWorkoutCompleteness`、`completenessSeries` | 200 天历史越滚越慢；首次完整性等于把历史全部重传一遍 |
| C-06 | 一个 Workout 抛错会中断整轮完整性；外层只记录失败，不隔离单个对象 | `syncWorkoutCompleteness` | 一个 `metadata_requires_review` 让后面的 Workout 全部排队；下次重来 |
| C-07 | 完整性 `cursor`、`capturePage`、`stage`、`ack`、`deliver` 多处重新读取并哈希分页/series 文件 | `WorkoutCompleteness.swift` | 同一不可变文件被多次全量读取和哈希 |
| C-08 | `releaseBundleFile` 只清 bundle/wire 字段，不清 `captureManifest`、`pendingReadKeys` 等 | `OutboxStore.swift` | ACK 后仍保留大对象；状态持续膨胀 |
| C-09 | `associateDiscoveredFacts` 即使没有新关联也无条件 `commit` | `HealthDiscovery.swift` | 无变化写入放大 |
| C-10 | Recovery 依次串行执行发现、恢复上传、Daily、交付、完整性；完整性期间手动同步被 `running` 挡住 | `RAIApp.swift` 的 `performRecovery`、`SyncCoordinator.swift` | 一个慢阶段阻塞后续；用户感知“卡住” |
| C-11 | `capturedFact` 已构造 source/event，却又调用会重建二者的 `discoveryFingerprint(sample)`；segment 写入时再构造一次 `DiscoveryEvent` | `HealthKitClient.swift`、`CaptureSegments.swift` | 每样本重复 JSON 构造和哈希，属于可消除的 CPU 成本 |
| C-12 | `exactReplay` 会完整解码重建包；`WorkoutBundleDelivery` 还会再完整解码一次做 scope 检查 | `WorkoutBundle.swift`、`WorkoutBundleDelivery.swift` | 同一包最多两次成功全量解码；不是三次，但可以降到一次 |
| C-13 | `ProductAPI.load` 串行 6 个 GET；详情 detail → series → freshness 串行；无本地读缓存 | `ProductCore/Product.swift`、`ProductModel.swift` | 首页和详情首屏被串行 RTT 支配 |
| C-14 | 详情 freshness 轮询固定 `[2,5,10,15]` 秒，最多约 32 秒 | `WorkoutDetailView.swift` | 分析稍慢就停在半成品；再靠用户手动刷新 |

### 2.3 服务端已验证问题

| 编号 | 事实 | 位置 | 后果 |
| --- | --- | --- | --- |
| S-01 | `ImportWorkoutBundle` 在 dataset 行锁内做 workout/sample 规范化与哈希、series/route 已存摘要比较 | `apps/server/internal/storage/postgres/workout_bundles.go`、`bundle_batching.go` | 锁持有时间包含纯 CPU 工作，直接放大每 dataset 串行度 |
| S-02 | 事务上限 45 秒，`statement_timeout=30s`；连接池 `lock_timeout=500ms` | `workout_bundles.go`、`store.go` | 锁冲突快速失败，客户端看到 429/503；实际不是“等 10 秒” |
| S-03 | 完整性 v2 在同一把 dataset 锁内验证 manifest 中所有 Workout 的 proof；每个 proof 读取 `workout_imports.payload` 并反序列化 | `workout_completeness_v2.go`、`workout_completeness.go` | 历史越长，锁内工作越大；200 个 proof 会形成长事务 |
| S-04 | `workout_imports.payload` 保存完整原始包；分析、特征、完整性和 tombstone 检查都读取它 | `analysis_input.go`、`feature_input.go`、`bundle_integrity.go`、`workout_completeness.go` | 双重存储和热路径大对象读取；不能简单删除 |
| S-05 | 全局上传槽 2、单设备 1，被占用时立即 429，没有等待队列 | `httpapi/server.go` 的 `acquireUpload` | 并发恢复/手动同步容易撞 429；客户端只能退避重试 |
| S-06 | Worker 13 种 job 共用一个处理槽 | `jobs/worker.go` | 重模型/回填任务会阻塞 workout 分析，用户看到 `analyzing` 很久 |
| S-07 | `/v2/sync/changes` 存在，但 handshake 明确 `changefeed: false`，iOS 不调用 | `httpapi/server.go`、`ios` | 客户端只能用列表扫描/定时刷新 |
| S-08 | 分析发布事务没有写 `change_events` | `analysis_publish.go` | 即使开启 changefeed，分析发布也没有失效事件 |
| S-09 | `WorkoutDetail`、`WorkoutSeries`、`WorkoutFreshness` 都调用同一份 `loadWorkoutDetailData`，该函数会装载 samples、series chunks、routes、statistics 等 | `storage/postgres/workout_detail.go` | 三个请求各自全量装载；服务端 I/O 放大 |
| S-10 | 全局 `Cache-Control: no-store`，没有 ETag/If-None-Match | `httpapi/server.go` | 无法条件请求；客户端只能全量重取 |

### 2.4 回程已验证问题

- 首页 `ProductAPI.load` 串行 6 个 GET，失败键单独记录；这保证预算但把首屏时延做成 6 个 RTT 之和。
- 详情页先 `detail`，再 `series`，再 `freshness`；三个服务端接口又各自装载同一份完整详情数据。
- `pollFreshness` 最多约 32 秒；分析发布没有事件推送，客户端没有持续的轻量跟进。
- `refreshServerStatuses` 每次做最多 8 页 × 50 条 Workout 列表扫描；没有消费 changes。
- `ProductModel` 只有在 `api == nil` 时才配置/握手；配置或 dataset 切换后的重新握手语义需要单独审查，不与本次同步链路修复混在一起。

### 2.5 对原审查报告的纠正

原报告方向大体正确，但以下说法不能直接作为实施依据：

| 原说法 | 核实结果 | 对方案的影响 |
| --- | --- | --- |
| `PublishAnalysis` 会等 dataset 锁 10 秒 | 不是。10 秒是事务 `transaction_timeout`；锁等待受连接池 `lock_timeout=500ms` 限制 | 不要把“缩短 10 秒锁等待”当目标；应优化事务内工作和重试语义 |
| 一次上传有 3 次完整 bundle AST 解码 | 最多 2 次成功解码（条件 `exactReplay` + delivery）；`coverageFromBundle` 在解码前就失败 | 先修 multipart 解析，再按计数器消除剩余的一次 |
| S60H 302.34 MiB 是完整 bundle 解码直接造成 | 302.34 MiB 是实测，但归因未证明；R2 报告显示 HK 读取是主要时延，内存峰值还受采集/压缩/上传缓冲影响 | 内存优化要分别测量采集、组包、压缩、上传阶段，不能只改 decode |
| `refreshAnalysis` 没有后续跟进 | 不准确。`recoverPending` 会刷新 acknowledged/analyzing，详情也有轮询；缺的是持续的、带退避的自动跟进 | 增加有界退避和 changefeed，不重写已有恢复路径 |
| 详情页把同一份点集下载了三次 | 服务端装载三次，客户端是三个不同响应各下载一次；不是同一份数据重复下载三遍 | 重点是拆分服务端 loader 和减少请求数，不是简单“去重网络包” |
| `workout_imports.payload` 只用于 tombstone 检查 | 错误。分析、特征、完整性和删除检查都读它 | 必须分阶段抽取 observation/provenance，不能直接删列 |
| 每样本 5–6 次 SHA-256，且都在哈希同一份 payload | 不精确。`DiscoveryEvent` 的 ID 是对字符串哈希；segment 滚动哈希是增量的；真正的冗余是同一份事实在组包、segment 写入、发布、stage、promote 多处重复 | 只消除重复全量哈希，保留信任边界校验 |
| 每个 segment 都被完整重读重算 | 不准确。seal 哈希有缓存；per-record 读取/哈希仍有重复 | 用文件身份缓存减少重复校验，而不是取消 segment 完整性 |
| `datasets FOR UPDATE` 是全局锁 | 是 per-dataset 行锁，不同 dataset 可并行 | 优化单 dataset 锁持有时间即可，不必重做锁模型 |
| 服务端重算 `BundleDigest` 是冗余 | 错误。它是服务端信任边界校验，必须保留 | 只在同一请求内发现重复哈希时去重，不删除校验 |
| 80–100 次提交 / 8–10 GB 写放大 | 是合理估算，不是实测 | Phase 0 必须用状态审计和提交计数器测量 |
| 原报告未提 `.unsupported` 永久 `.partial` | 遗漏，已确认 | 纳入 P0 |
| 原报告未提单 Workout 故障阻塞整轮 | 遗漏，已确认 | 纳入 P0 |
| 原报告未提详情轮询只有约 32 秒 | 遗漏，已确认 | 纳入 P4 |

### 2.6 现场症状与待复现项

- 验收设备上曾观察到 `state.json` 约 116 MB、单次 `6cd8` Workout 的完整性与上传超过 10 分钟、`coverageFromBundle` 失败、任务长期 `blocked`。这些是现场症状，必须先由 Phase 0 的计量构建复现并写入证据目录。
- 不能在未复现的情况下把“116 MB”“10 分钟”当作所有设备的固定基线；也不能用一次成功上传证明完整性问题已解决。

---

## 3. 不可破坏的约束

以下约束来自 `AGENTS.md` 和架构方案，修复过程中不得交换：

1. 原生 iOS、只读 HealthKit；不新增 Web、Watch、桌面客户端。
2. Go 模块化单体、PostgreSQL 18、pgx/v5、sqlc；API/Worker 同镜像。
3. 原始事实、删除屏障、提交日志、分析待办和最终回执必须在同一事务提交；dataset 行锁产生提交顺序；**禁止用 sequence 当下载水位**。
4. 上传不可变；同字节重试返回原回执；删除优先，旧上传不得复活 UUID。
5. 仅明示支持的载荷可提交；不丢弃不支持字段；无法描述的 metadata 保留原始恢复材料并阻塞受影响对象，不能伪造空收据。
6. 同步不得依赖备份、恢复、迁移、全库摘要或全量重算；管理工具不能被 application/httpapi 导入。
7. 不关闭 durability、不截断窗口、不降采样、不通过丢弃请求达标。
8. 仅用合成数据做自动化测试；不得读取生产库、记录健康值或提交凭证。
9. 开发、回归、性能测试在本机 Mac；真机验收使用已授权的只读范围；没有授权就列为未验证。
10. 不自动部署、不切换旧入口、不迁移真实数据、不 commit/push。
11. 任何并发/槽位/池大小调整都必须有本机测量和明确授权，不能以“优化”为名扩大资源。

---

## 4. 目标架构

### 4.1 客户端

```text
HealthKit 发现/捕获
  → 规范化事实（一次构造）
  → 不可变 segment/文件（封存时一次哈希）
  → SQLite 元数据（按实体小事务）
  → Workout bundle（原始字节 + 哈希）
  → gzip multipart 上传（复用已存在 wire）
  → ACK（回执 + 发现水位 + GC 授权同一事务）
  → 完整性 proof（跨周期复用）
```

关键变化：

- `state.json` 只保留迁移备份，不再参与正常写入。
- 大数组按实体拆表；大文件留在文件系统，数据库只存路径/哈希/状态。
- 每类不可变产物有“已封存”哈希，后续只验证一次，不反复重读。
- 完整性周期有 per-Workout 状态，单个失败不阻塞其他对象。

### 4.2 服务端

```text
HTTP 读取/解压/严格解码（锁外）
  → 语义校验/规范化/纯 CPU 哈希（锁外）
  → begin tx
  → dataset 行锁
  → 集合化身份/删除/重试检查
  → 多行写入 + job + change_events + 回执
  → commit
```

关键变化：

- 锁外准备所有可准备的数据；锁内只做必须看到最新提交状态的检查和写入。
- 完整性 proof 有验证缓存；无变化的 carried proof 不再读取完整 raw payload。
- 分析发布写 change event；回程可增量失效。
- Worker 分交互/重活两道；上传准入有界等待而不是瞬时拒绝。

### 4.3 回程

```text
App 启动/前台
  → 本地 cache.sqlite 快照（立即可用）
  → /v2/sync/changes?after=cursor（增量失效）
  → 只刷新受影响的 Workout/产品块
  → detail/series/freshness 按需拆分加载
```

---

## 5. Phase 0：基线与计量

### 5.1 必须先建立的计数器

没有这些计数，后续“优化了多少”无法验收。

客户端：

- `state_bytes`、`state_section_bytes{discovery,tasks,completeness,...}`。
- `commit_count`、`commit_logical_bytes`、`commit_duration_ms`。
- `sha256_bytes{stage}`：build/segment_seal/publish/stage/ack/deliver/wire。
- `bundle_json_decode_count`、`bundle_full_bytes_read`。
- `hk_child_queries`、`hk_series_reads`、`completeness_proofs_total/carried/rebuilt`。
- `upload_429_count`、`upload_retry_after_ms`。
- `analysis_refresh_attempts`、`analysis_time_to_visible_ms`。
- `product_requests`、`product_bytes`、`detail_requests`。
- RSS、峰值采集队列字节、峰值上传缓冲。

服务端：

- `import_phase_seconds{phase}`，必须能区分 `decode`、`semantic_validation`、`canonicalize`、`dataset_lock`、`identity_checks`、`writes`、`receipt`、`commit`。
- `completeness_phase_seconds`、`proofs_total/cache_hits/raw_reads`、`payload_bytes_read`。
- `upload_queue_depth`、`upload_wait_ms`、`upload_rejected_total{reason}`。
- `worker_job_wait_seconds{kind}`、`worker_slots_busy`。
- `detail_rows_scanned`、`detail_bytes`、`freshness_rows_scanned`。
- `changes_pages`、`changes_lag`。

日志只能输出计数、哈希前缀、状态码和耗时；不得输出健康值、GPS、完整 UUID、source/device 唯一标识或原始 payload。

### 5.2 固定负载

沿用已有合成负载：S20/S60/S120/S240/S60H；新增/固定一个 200-Workout、200 天 inventory 的完整性 fixture，以及一个“200 个 Workout 中 1 个 metadata 不可描述”的故障 fixture。所有 fixture 使用固定 seed、固定字段覆盖，不复制真实健康值。

### 5.3 基线采集

至少执行并保存：

1. 状态审计：`SyncProbe --state-audit`（新增）输出各段字节数、事件数、任务数、proof 数。
2. 普通构建服务端：`scripts/local/test.sh syncperf --profiles S60,S60H,S240 --iterations 10 --warmups 2 --cold 2`。
3. 完整性：模拟器/e2e 跑 200-Workout fixture 的首轮和第二轮，记录 HK 查询数、series 读取数、上传数、proof 数、耗时。
4. 回程：记录首页 6 个 GET、详情 3 个 GET 的请求数/字节/耗时。
5. 真机（有授权时）：只记录时延、计数、RSS；不导出健康值。

基线证据放到 `docs/evidence/sync-remediation-2026-11-07/`，README 说明命令、构建类型、设备/OS、样本数和失败数。

### 5.4 基线判定规则

- 性能数字必须注明是否 `-race`；`syncperf` 是不带 `-race` 的优化构建，不能替代 race 正确性门禁。
- 有失败请求时，不能只统计成功请求后宣布该档通过。
- 任何百分比优化都相对同一负载、同一构建类型、同一设备/后端比较。

---

## 6. P0：正确性修复

P0 不改变协议和持久化形态，先消除会误导上传、永久阻塞和重复写入的缺陷。

### 6.1 修复 `coverageFromBundle` 的 multipart 解码

**问题**：组包时保存的是 multipart 字节，`coverageFromBundle` 直接 `JSON.decode(data)`，必然失败，然后静默使用旧 `task.coverage`。

**实施**：

- 新增 `coverageFromMultipart(_ data: Data, boundary: String)`：按已保存的 boundary 去掉前后 framing，得到原始 bundle JSON，再解码 `observation.coverage`。
- 调用点同时支持：
  - 旧 pending 任务的 multipart 文件；
  - 未来若改为保存 raw bundle，则直接解码。
- 禁止静默回退。解析失败时：
  - 如果任务有可重建的发现事实，重建 bundle；
  - 否则抛出 `localState("bundle_observation_corrupt")` 并把任务标记为需要重试，而不是用陈旧 coverage 上传。
- 增加计数器 `bundle_coverage_parse_failures`，并为失败路径加测试。

**测试**：

- 单元测试：给定真实格式的 multipart 样本，解析出的 coverage 与包内 observation 完全一致。
- 回归测试：先构造失败尝试留下的陈旧 `task.coverage`，再恢复上传，断言上传使用的是包内 coverage。
- 负例：截断 multipart、错误 boundary、空 observation，都必须显式失败，不得返回旧 coverage。

**验收**：`coverageFromBundle` 对 multipart 的成功解析率 100%；现有 pending bundle 恢复路径不回归；无静默 fallback。

### 6.2 修正 `.unsupported` 的完成语义

**问题**：`WorkoutCoverage.isAvailable` 只认 `.available/.complete`，导致 `cadence`、`elevation_sample` 这类“当前 SDK 不支持”的流进入 `missing`，任务被永久标成 `.partial`。

**实施**：

- 在 `WorkoutCoverage` 增加明确的“是否需要重试”语义，例如：

```swift
public var isSatisfied: Bool {
    switch state {
    case .complete, .noRecordsObserved, .unsupported:
        return true
    case .available:
        return true
    case .observed, .partial, .failed, .error, .unknown, .unavailable, .notQueried:
        return false
    }
}
public var needsRetry: Bool { !isSatisfied }
```

- `task.missing` 改用 `needsRetry`，不再用 `!isAvailable`。
- `pendingReadKeys` 的过滤条件同样使用 `needsRetry`，并继续排除 `metadata` 的既有特例。
- UI 文案区分“当前不支持”和“部分数据待处理”；`.unsupported` 不应单独把任务降级为 `.partial`。
- 确认服务端完整性合同：`WorkoutCompletenessUnsupported = ["native_running_cadence", "elevation_sample"]`，这些键不属于必须证明的 quantity 集合，客户端不重试是正确的。

**测试**：

- 构造只含 `cadence`/`elevation_sample` 为 `.unsupported` 的任务，断言 `missing` 为空、`pendingReadKeys` 为空、ACK 后可进入 `.acknowledged/.published`，不会因这两个键永久 `.partial`。
- 构造真实的 `.partial` 和 `.failed`，断言仍需重试。
- 模拟器测试确认 UI 状态文本正确。

### 6.3 完整性 per-Workout 故障隔离

**问题**：`syncWorkoutCompleteness` 在 inventory 循环内抛错就中断整轮；一个 `metadata_requires_review` 让后面的 Workout 全部无法采集，下次从头重试。

**实施**：

- 在 `WorkoutCompletenessState/Cycle` 增加 per-Workout 阻塞记录（不进入 manifest）：

```text
blocked[workoutID] = { code, failedType?, attempt, nextAttemptAt, lastSeenAt }
```

- 循环内对每个 Workout 使用独立 `do/catch`：
  - 成功：写 proof，清除 blocked。
  - 可重试失败：写 blocked，继续下一个 Workout，不丢弃已完成的 proof。
  - 删除/策略排除：按既有显式路径处理。
  - 不可描述 metadata：blocked 记录具体类型/原因，等待 registry 或显式重试，不伪造成空收据。
- 一轮结束后：
  - 所有 eligible Workout 都有 proof：正常 stage/deliver。
  - 有 blocked：不生成 manifest（v2 合同要求 inventory 全部可证明），但保留周期和所有已完成进度；返回可诊断的 `completeness_blocked` 状态，包含阻塞数量、短哈希和原因码。
- 下一轮只重试 blocked 的 Workout；已完成且未变化的 proof 直接复用（P2 实现后不再重读）。
- 增加指数退避，避免每次启动都重复读取同一个坏对象；用户显式“重试完整性”可以清零退避。

**测试**：

- 200 个 Workout，第 1 个 metadata 不可描述：首轮后 199 个 proof 保留，1 个 blocked；第二轮只对 1 个 Workout 重试，其他 199 个零 HK 子查询。
- blocked 不会导致空 manifest 或假 receipt。
- 删除/策略排除仍走原有显式路径，不影响隔离逻辑。

### 6.4 ACK 清理与回执保留

**问题**：`releaseBundleFile` 只清 bundle 文件字段；`captureManifest`、`pendingReadKeys` 等大对象在 ACK 后仍保留。`captureManifest` 的 `coveredEventIDs` 可能包含整个 Workout 的事实 ID。

**实施**：

- 在 `acknowledgeBundle` 中先完成发现水位计算（当前代码依赖 `task.captureManifest`），再释放：
  - `captureManifest = nil`
  - `pendingReadKeys = nil`（仅当为空/已交付）
  - `bundleDeletionEventIDs = nil`（若删除回执已确认）
  - `stagedAll` 保留兼容字段即可
- 保留：
  - `uploadReceipt`：重试幂等和状态展示需要；
  - `bundleSHA256`：完整性 proof 和重试需要；
  - `originalWorkout`：删除父 Workout 的子删除路径仍需要；它是单个对象，不是大数组；
  - `coverage`：压缩后的状态摘要，供 UI 和完整性检查使用。
- 如果删除路径需要完整原始对象，优先从 `discovery.workoutArchives` 读取；归档回收策略必须保证“已 ACK 但可能被删除”的 Workout 仍有恢复材料，不能提前 GC。

**测试**：

- 单元测试：ACK 前后状态编码字节差有上界（例如 ≤ 2 KiB，不含回执本身），`captureManifest == nil`。
- 重试同一 bundle 仍返回原回执，不因清理而重建不同字节。
- ACK 后删除 Workout，删除-only bundle 仍能构造成功，不出现 `deleted_parent_workout_archive_missing`。

### 6.5 无变化发现关联不提交

**问题**：`associateDiscoveredFacts` 无论是否真的新增关联都 `commit(next)`。

**实施**：

- 先计算是否存在实际变化（新 `parents`、事件 `workoutID/status/generation/ordinal` 变化）。
- 无变化直接返回，不编码、不写文件、不更新 `updatedAt`。
- 增加 `commit_skipped_noop` 计数器，Phase 0 基线对比。

**测试**：连续两次用同一组 keys 调用，第二次提交计数不增加；有变化时行为不变。

### 6.6 分析状态的有界自动跟进

**问题**：ACK 后只调度一次 `refreshAnalysis`；若分析超过这一次，任务停在 `analyzing`，要等下次 recovery。详情轮询只有约 32 秒。

**实施**：

- 新增有界退避刷新：`[2, 5, 15, 30, 60, 120, 300]` 秒，最多约 10 分钟；满足以下任一条件停止：
  - 任务进入 `.published/.partial/.deleted`；
  - App 进入后台；
  - 用户取消/切换 Workout；
  - 达到上限后记录 `analysis_followup_exhausted`，交给 changefeed/下次 recovery。
- 刷新使用轻量状态接口（P4 后为 freshness/changefeed），不要每次都拉完整详情。
- `isAlreadyDelivered` 跳过同步时，如果任务仍 `.analyzing` 且 `analysisCheckedAt` 超过退避窗口，也调度一次刷新。
- 不因一次刷新失败把 durable ACK 降级；错误只记录在任务诊断。

**测试**：

- 模拟服务端先返回 `analysis_not_ready` 再返回可用：验证在退避窗口内自动变为 published。
- 模拟一直 404：验证在 10 分钟后停止，不无限请求。
- 后台切换后停止轮询。

### 6.7 P0 完成门禁

- `scripts/local/test.sh swift` 通过。
- `scripts/local/test.sh simulator` 通过。
- 新增的 P0 用例全部 pass。
- `scripts/local/test.sh quick` 通过。
- 不修改协议；现有 pending 任务仍可恢复。
- 更新 `docs/IMPLEMENTATION.md` 的“已实现/证据/未验证”段，但不得把计划写成结果。

---

## 7. P1：本地持久化与写放大治理

### 7.1 目标

把“整份 `state.json` 全量编码 + 原子覆写”改为“SQLite 元数据 + 文件系统大对象”，使普通提交的成本与变化量成正比。

### 7.2 存储划分

| 存储 | 内容 | 生命周期 |
| --- | --- | --- |
| `sync.sqlite` | 发现工作/事件索引、任务、覆盖状态、回执、完整性周期/proof 索引、GC 队列 | 耐久，WAL，文件保护 |
| 文件系统 | bundle 原包、gzip wire、capture segment、完整性分页归档、series 文件 | 耐久，按引用 GC |
| `cache.sqlite` | 产品快照、详情缓存、列表缓存 | 可删除，dataset 切换可整体清空 |
| `state.json.pre-sqlite` | 迁移前备份 | 保留至少 7 天/一个版本 |

大对象不放进 SQLite BLOB 行，除非单行很小；分页 JSON 可以放 BLOB，但必须保证一次提交只写变化行。所有文件使用 `.completeFileProtectionUntilFirstUserAuthentication`（与现有 `OutboxStore.write` 一致）。

### 7.3 建议 schema

```sql
PRAGMA user_version = 1;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
PRAGMA foreign_keys = ON;

CREATE TABLE meta(key TEXT PRIMARY KEY, value BLOB NOT NULL);

CREATE TABLE scope(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  dataset_id TEXT, dataset_epoch TEXT, collector_epoch TEXT
);

CREATE TABLE discovery_work(
  workout_id TEXT PRIMARY KEY,
  generation INTEGER NOT NULL,
  delivered_generation INTEGER NOT NULL,
  prepared_generation INTEGER,
  selected INTEGER NOT NULL DEFAULT 0,
  pending_delete INTEGER NOT NULL DEFAULT 0,
  error TEXT, attempt INTEGER, next_attempt_at REAL,
  workout_archive TEXT
);

CREATE TABLE discovery_flows(
  query_key TEXT PRIMARY KEY,
  query BLOB NOT NULL,
  anchor BLOB,
  staged BLOB,
  has_more INTEGER NOT NULL DEFAULT 0,
  error TEXT,
  next_attempt_at REAL
);

CREATE TABLE discovery_parents(
  fact_key TEXT PRIMARY KEY,
  workout_id TEXT NOT NULL
);

CREATE TABLE discovery_meta(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  next_ordinal INTEGER NOT NULL,
  workout_archives BLOB
);

CREATE TABLE discovery_events(
  id TEXT PRIMARY KEY,
  workout_id TEXT,
  type TEXT NOT NULL,
  uuid TEXT NOT NULL,
  status TEXT NOT NULL,
  generation INTEGER,
  ordinal INTEGER,
  fact_key TEXT,
  archive_file TEXT,
  archive_sha256 TEXT,
  segment_file TEXT,
  segment_offset INTEGER,
  segment_length INTEGER,
  deleted INTEGER NOT NULL DEFAULT 0,
  payload BLOB
);
CREATE INDEX discovery_events_work ON discovery_events(workout_id, status);
CREATE INDEX discovery_events_status ON discovery_events(status);

CREATE TABLE tasks(
  workout_id TEXT PRIMARY KEY,
  phase TEXT NOT NULL,
  bundle_file TEXT, bundle_sha256 TEXT, bundle_boundary TEXT,
  bundle_wire_file TEXT, bundle_wire_sha256 TEXT,
  acknowledged_at REAL, analysis_checked_at REAL,
  error_code TEXT, attempt_count INTEGER, next_attempt_at REAL,
  upload_receipt BLOB, deletion_receipt BLOB,
  coverage BLOB, metrics BLOB,
  workout_anchor BLOB, discovery_generation INTEGER,
  original_workout BLOB, child_deletion_only INTEGER
);

CREATE TABLE receipts(
  key TEXT PRIMARY KEY,
  payload BLOB NOT NULL,
  created_at REAL NOT NULL
);

CREATE TABLE completeness_flow(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  window_from_us INTEGER NOT NULL,
  generation INTEGER NOT NULL,
  last_manifest_sha256 TEXT,
  last_cutoff_us INTEGER,
  anchors BLOB,
  inventory BLOB,
  proofs BLOB,
  last_receipt BLOB,
  last_query_counts BLOB,
  error_code TEXT
);

CREATE TABLE completeness_cycles(
  id TEXT PRIMARY KEY,
  contract TEXT, protocol_version INTEGER,
  manifest_id TEXT, generation INTEGER,
  previous_manifest_sha256 TEXT,
  window_from_us INTEGER, known_cutoff_us INTEGER,
  inventory BLOB, deleted_roots BLOB,
  classification BLOB, exclusions BLOB,
  series_files BLOB, completed_workouts BLOB,
  committed_anchor_queries INTEGER, staged_cursor_queries INTEGER, bootstrap_queries INTEGER,
  created_at REAL, pending_file TEXT, pending_sha256 TEXT
);

CREATE TABLE completeness_captures(
  cycle_id TEXT NOT NULL REFERENCES completeness_cycles(id) ON DELETE CASCADE,
  query_key TEXT NOT NULL,
  page_index INTEGER NOT NULL,
  page BLOB NOT NULL,
  archive_file TEXT, archive_sha256 TEXT,
  PRIMARY KEY(cycle_id, query_key, page_index)
);

CREATE TABLE completeness_proofs(
  cycle_id TEXT NOT NULL REFERENCES completeness_cycles(id) ON DELETE CASCADE,
  workout_id TEXT NOT NULL,
  proof BLOB NOT NULL,
  source_generation INTEGER,
  checked_at REAL,
  PRIMARY KEY(cycle_id, workout_id)
);

CREATE TABLE completeness_blocked(
  cycle_id TEXT NOT NULL REFERENCES completeness_cycles(id) ON DELETE CASCADE,
  workout_id TEXT NOT NULL,
  code TEXT NOT NULL,
  failed_type TEXT,
  attempt INTEGER NOT NULL,
  next_attempt_at REAL,
  PRIMARY KEY(cycle_id, workout_id)
);

CREATE TABLE reclaim_files(
  path TEXT PRIMARY KEY,
  created_at REAL NOT NULL
);

CREATE TABLE daily_flows(
  key TEXT PRIMARY KEY,
  payload BLOB NOT NULL
);

CREATE TABLE vo2_flow(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  payload BLOB NOT NULL
);

CREATE TABLE legacy_pages(
  id TEXT PRIMARY KEY,
  payload BLOB NOT NULL
);
```

具体列可按现有 Codable 结构裁剪，但必须满足：

- `discovery_events` 按 Workout/状态有索引，裁剪可按 `status + ordinal` 做。
- `tasks` 一行一个 Workout，不再重写整张任务字典。
- `completeness_captures` 一行一个分页，stage 时不需要重新拼接整份 cycle JSON。
- 大 bundle/segment/分页归档仍以文件路径引用，数据库中不复制大字节。

### 7.4 写入 API

- 新增 `LocalStore`（actor 或由 `OutboxStore` 内部持有），提供：
  - `updateDiscoveryWork`
  - `insertDiscoveryEvents`
  - `promoteBatch`
  - `updateTask`
  - `setTaskCoverage`
  - `upsertProof`
  - `appendCompletenessCapture`
  - `enqueueReclaim`
- 每个公开方法在一个 SQLite 事务内完成；失败回滚，不改变内存状态。
- 禁止在正常路径继续调用 `JSONEncoder().encode(next)` 或整份 `commit(next)`。迁移代码和测试可以保留。
- 代码级门禁：生产路径中 `commit(next)` 的调用点为 0；新增代码必须通过 `LocalStore` 写。

### 7.5 迁移

1. App 启动发现 `state.json` 且 `sync.sqlite` 不存在：
   - 解码旧状态（现有版本校验保留）。
   - 在一个 SQLite 事务中插入 scope、discovery、tasks、receipts、completeness、daily、vo2、reclaim。
   - 校验：任务数、事件数、proof 数、回执数与旧状态一致；抽查 bundle 文件路径存在。
   - 写 `meta(migration_state='complete')`，把 `state.json` 重命名为 `state.json.pre-sqlite`。
2. 迁移中断：
   - SQLite 事务保证全有或全无；下次启动重新导入。
   - 只有在 `migration_state='complete'` 后才允许删除/重命名旧文件。
3. 旧版本回退：
   - 新 App 不回写 `state.json`；回退到旧版本前需要保留备份并运行导出工具。
   - 迁移备份至少保留 7 天；超过后由用户显式清理，不自动删除。
4. 不允许“数据库打不开就静默从空状态开始”。必须 fail closed，提示本地同步库需要恢复，避免把 ACK 丢失伪装成新设备。

### 7.6 裁剪、压缩与 GC

- 发现事件：
  - 已交付且被 `deliveredGeneration` 覆盖的事件，超过保留窗口（建议 7 天）后删除。
  - 未交付、待删除、被 proof/task 引用的事件不删除。
  - Workout 根事件和删除事件保留更久，直到删除回执/证明完成。
- 任务：
  - `.published/.deleted` 且超过 30 天：压缩为 `receipt + bundleSHA256 + coverage 摘要`，删除大字段。
  - `.failed` 不自动删除，保留诊断和恢复材料。
- 完整性：
  - 已 ACK 的 cycle 只保留 manifest 哈希、proof 摘要和 inventory 摘要；分页/归档文件进入 reclaim。
  - 可复用 proof 在 cycle 清理前必须写入 `completeness_flow.proofs`；不能只留在即将删除的 cycle 行里。
  - 被新 proof 引用的分页文件不能提前回收。
- 文件 GC：
  - 以 `reclaim_files` + 引用扫描为准；启动时做一次 orphan 扫描。
  - 不允许“删目录了事”；先确认无任务、无 proof、无 pending manifest 引用。
- 无变化保护：
  - 所有 update 方法先比较旧值；无变化不写、不更新时间戳、不产生 WAL。

### 7.7 P1 验收

| 指标 | 门槛 | 方法 |
| --- | --- | --- |
| 正常路径 `state.json` 写入 | 0 次 | 代码搜索 + 运行时计数器 |
| 200 Workout/20k 事件/200 proof 的 `sync.sqlite` | ≤ 16 MiB（不含 bundle/segment 文件） | `SyncProbe --state-audit` |
| 冷启动打开本地库 | P95 ≤ 500 ms（iPhone 12 Pro Max 或同等级模拟器） | 设备/模拟器计时 |
| 普通提交逻辑字节 | ≤ 256 KiB；分页/捕获提交 ≤ 1 MiB | SQLite trace/计数器 |
| 1000 次无变化更新 | 0 次写事务/0 WAL 增长 | 单元测试 |
| 迁移 | 旧状态计数一致、回执/任务可恢复、kill 后重试成功 | 单元 + 模拟器故障注入 |
| 裁剪 | 已交付事件可删，未交付/引用中事件不可删 | 单元 + 属性测试 |
| ACK 后任务 | 大字段清理，重试仍返回原回执 | 单元 + e2e |

P1 不得以“数据库文件更小”作为唯一证据；必须同时证明恢复、幂等、删除和完整性 proof 未被破坏。

---

## 8. P2：完整性增量核验

### 8.1 当前成本

每个新 cycle：

- 根查询后遍历整个 200 天 inventory；
- 对每个 eligible Workout 查询所有 quantity + route；
- `completenessSeries` 读取所有 quantity series；
- 首个 cycle 对每个 Workout `forceNewBundle: true`，重传历史；
- 每个 Workout 至少一次 proof，stage/ack/deliver 再重复读取/哈希分页；
- 服务端在 dataset 锁内逐个读取 raw payload 验证 proof。

### 8.2 客户端：proof 跨周期复用

1. 新 cycle 不再清空 `flow.proofs`。保留上一周期已提交的 proof，并记录：
   - `bundleSHA256`
   - `series` 摘要（sampleType/sha256/pointCount）
   - `routeState`
   - `sourceGeneration`
   - `checkedAt`

   创建新 cycle 时以 `flow.proofs` 为种子，再按“task 仍有 receipt、无 pendingReadKeys、Workout 未变化”过滤；不能只把 inventory 复制进新 cycle 而把 proof 留在旧 cycle。
2. 根查询仍执行（必须发现新增/删除 Workout），但：
   - 如果 Workout 不在任何新增/删除页中，且上一周期 proof 对应的 task 仍有 receipt，且没有 pendingReadKeys，则直接复用 proof。
   - 如果根页显示该 Workout 新增/删除，或 discovery 有未交付子事件，才进入子查询。
   - “是否变化”的判定依据是 cycle 分页 + 本地 discovery 的 `work.generation/deliveredGeneration` + `task.pendingReadKeys` + receipt，不能只看根页；普通同步已交付的子变化也必须使 proof 失效。
3. 子查询只对“变化”的 Workout 执行：
   - 先看根页/子页的 additions/deletions；
   - 没有变化就不调用 `captureCompletenessStream`；
   - 有变化才按类型查询；未变化的 quantity 不重读。
4. `needsRefresh` 的顺序改为：
   - 先由已捕获分页判断 `rootChanged/childrenChanged`；
   - 只有变化或没有 proof 时才调用 `completenessSeries`；
   - 否则复用 stored proof 的 series；
   - `syncWorkout` 使用 `forceNewBundle: false`；只有“确实没有可复用 bundle”或显式修复才强制。
5. `syncWorkout` 返回后不再第二次调用 `completenessSeries`：
   - 新 bundle 构建时已经持有 series 事实/摘要；
   - 将摘要写入 task/proof；
   - 若构建路径无法直接给出摘要，最多从 capture 文件读取一次，禁止再读 HealthKit。
6. proof 更新只在以下情况发生：bundle 哈希变化、series 摘要变化、routeState 变化、receipt 变化、显式修复。

预期效果：无变化后续周期只做根查询 + 读取本地 proof + 生成 manifest；不再读 200 个 Workout 的子数据。

### 8.3 服务端：proof 验证缓存

新增内部表（不对客户端暴露协议）：

```sql
CREATE TABLE workout_completeness_proofs(
  dataset_id uuid NOT NULL,
  workout_uuid uuid NOT NULL,
  bundle_sha256 bytea NOT NULL,
  proof_sha256 bytea NOT NULL,
  route_state text NOT NULL,
  series_digest bytea NOT NULL,
  validated_commit_rev bigint NOT NULL,
  validated_at timestamptz NOT NULL,
  PRIMARY KEY(dataset_id, workout_uuid, bundle_sha256)
);
```

`CommitWorkoutCompletenessV2` 内：

1. 锁外完成 manifest 解码、inventory/stream 结构解析、proof 规范化哈希。
2. 进入事务并锁 dataset 后，按集合查询：
   - 本批 proof 的缓存行；
   - 每个 Workout 的 `max(commit_rev)`（`workout_bundle/workout/workout_child_delete`）；
   - 相关 tombstone。
3. 对每个 proof：
   - 缓存命中、`proof_sha256`/`series_digest`/`route_state` 相同、`max(commit_rev) ≤ validated_commit_rev`、无 tombstone：视为 carried，跳过 raw payload 读取和反序列化。
   - 否则走现有完整校验；成功后 upsert 缓存，`validated_commit_rev = 当前相关最大 commit_rev`。
4. 任何删除/子删除/新 bundle 都会推进 `max(commit_rev)`，从而自动使缓存失效；不能只靠时间 TTL。
5. 缓存行随 dataset 删除/保留策略清理；不做跨 dataset 共享。

这保留了“删除优先”和“receipt 必须当前”的语义，同时把无变化 proof 的锁内成本降为索引查询。

### 8.4 服务端：manifest 解析与锁外准备

- `DecodeWorkoutCompletenessV2(raw)` 已在事务前；继续把 inventory 解析、stream 映射、proof 规范化、哈希计算移到锁外。
- 锁内只保留：dataset/lease 锁定、generation/previousManifest 连续性、inventory 与数据库可见集合比对、proof 缓存/失效检查、写入 receipt/change event。
- 如果某一批 proof 大量失效，允许锁内读取 raw；但必须记录 `raw_reads` 和锁持有时间，不能静默退化。
- 如果 manifest 超过 16 MiB，不能靠抬限额解决；按 8.5 处理。

### 8.5 manifest 体积与 v3 触发条件

当前 v2 manifest 包含所有 stream 分页和所有 proof。200 个 Workout 的密集记录可能接近 16 MiB 上限。先测量，再决定是否引入 v3：

- 触发条件（任一）：
  - 200-Workout fixture 的 manifest P95 > 12 MiB；
  - stage 时间 P95 > 2 s；
  - 服务端解析 manifest 占锁外准备时间的 30% 以上。
- v3 方向（仅在触发时实施）：
  - `contract` 升级，`protocolVersion: 3`；
  - `previousManifestSHA256` 指向已提交 v2/v3；
  - 新增 `carriedProofs`/`carriedStreams`，只携带变化部分；
  - 服务端验证 previous manifest 属于同一 dataset/epoch/collector，且 carried 对象自 `validated_commit_rev` 后无变化；
  - 客户端仅在 handshake 宣告 v3 时使用，服务器同时支持 v2/v3；旧客户端不受影响。
- 不在没有测量前直接改协议；v3 是条件工作，不是默认路径。

### 8.6 哈希与读取次数治理

在 P2 中统一以下规则：

- 不可变文件（bundle、wire、segment、manifest、分页归档、series 文件）在封存时计算一次哈希并持久化。
- 后续校验优先使用“文件身份 + 大小/mtime + 已存哈希”缓存；只有身份变化或发送前才重算。
- 发送前必须验证一次将要发送的字节；这是信任边界，不能省。
- `acknowledgeWorkoutCompleteness` 不再重新遍历并哈希所有分页；只验证 pending manifest 一次、读取其中的摘要，更新状态。
- `WorkoutCompletenessDelivery.deliver` 只验证待发送文件一次，复用 stage 的哈希。
- `completenessCursor` 不重复读取最后一个分页归档来推导 anchor；anchor 以已提交状态为准，必要时只在恢复时校验。

### 8.7 P2 验收

| 指标 | 门槛 | 方法 |
| --- | --- | --- |
| 无变化后续完整性周期 | 0 次 HK 子查询、0 次 series 读取、0 次 bundle POST | 模拟器 200-Workout fixture，计数器 |
| proof 复用率 | ≥ 95%（无变化 fixture 应为 100%） | proof 计数 |
| 服务端 carried proof raw payload 读取 | 0 字节 | PG 集成测试 + 计数器 |
| 200 proof 的完整性锁持有 | P95 ≤ 1 s | 服务端 phase 计时 |
| manifest | ≤ 16 MiB；超过触发 v3 评估 | fixture 实测 |
| 单 Workout 变化 | 只重读该 Workout 的变化类型，其他 Workout 0 读取 | 计数器 |
| 首轮完整性 | 不再对所有 Workout `forceNewBundle`；只上传缺失/变化 | 上传计数 |
| 删除/失效 | 新 bundle/子删除后 proof 缓存必须失效 | PG 集成测试 |
| 无假成功 | 有 blocked 时不提交 manifest、不生成 receipt | 模拟器 + 集成 |

---

## 9. P3：服务端写路径、锁与资源

### 9.1 锁外准备

`ImportWorkoutBundle` 当前在 dataset 锁内做：

- workout/sample `Canonical()` 和哈希；
- series/route 已存 chunk 摘要比较；
- 部分 observation 处理。

改为：

1. HTTP 层完成解压、multipart 解析、严格 JSON 解码、语义校验（保持现有错误码和限额）。
2. 新增 `prepareWorkoutBundle(bundle)`：
   - canonical workout + hash；
   - 每个 sample 的 canonical 字节 + hash；
   - 每个 series 的 chunk canonical/digest；
   - 每个 route 的 chunk canonical/digest；
   - observation summary。
3. 进入事务后只做：
   - dataset/lease 锁定；
   - 重试回执、tombstone、身份/关联冲突检查（使用预计算哈希）；
   - series/route 已存摘要比较（使用预计算 digest）；
   - 多行写入、job、change event、receipt、commit。
4. 用 phase 计时证明 `json_decode`、`semantic_validation`、`canonicalize` 不在 `dataset_lock` 区间内。

注意：**不能提前释放 dataset 行锁**。它承担提交顺序，释放后再写会破坏顺序和删除屏障。优化的是锁内工作量，不是锁的范围。

### 9.2 事务与锁预算

- 保留现有 `statement_timeout=30s`、`transaction_timeout=45s`、`lock_timeout=500ms`，先不擅自放宽。
- 增加锁等待/锁持有指标；如果 P0/P2 后仍频繁 500ms 失败，再基于测量决定是否：
  - 在 Store 内对“纯锁冲突”做一次有界、带 jitter 的重试；
  - 或调整 `lock_timeout`。
- 任何调整都必须证明不会扩大长事务、不会绕过删除屏障。
- `plan_cache_mode='force_custom_plan'` 保留；它是已复现的 generic plan 退化修复。

### 9.3 原始 payload 热路径治理

`workout_imports.payload` 同时承担审计/重试材料和分析输入，不能直接删除。分三步：

1. **抽取**：新增 `workout_observations`（或 `workout_imports` 的附加列），保存：
   - observation（coverage/provenance/observedAtUs 等）；
   - source summary；
   - completeRoutes/routeState；
   - sampleCount/routeCount；
   - series 摘要（如 proof 需要）。
2. **回填**：管理工具从现有 `payload` 批量回填；可重入、分批、可校验计数；不回写历史事实。
3. **切换读者**：
   - `analysis_input.go`、`feature_input.go`、`bundle_integrity.go`、`workout_completeness.go` 改为读抽取后的列/表；
   - `WorkoutImport` 的 tombstone/route 检查改用 normalized 表 + receipt；
   - 保留 `payload` 作为审计/原始恢复材料，暂不自动删除。

可选后续（需要单独授权）：把 `payload` 压缩存储或迁到独立归档表/对象存储；在此之前只做“热路径不读完整 payload”。

验收：

- 普通分析/特征/完整性路径的 `payload` 全量读取为 0（除显式审计/回填）。
- proof 缓存命中时 `workout_imports.payload` 读取为 0。
- 回填前后 receipt、删除、重试、provenance 语义一致。
- 数据库体积变化有测量；不以删除审计材料换取体积。

### 9.4 上传准入

当前行为：全局 2 槽、单设备 1 槽，占用即 429，没有等待。

实施：

- 保留槽位上限（不擅自扩大），增加**有界等待队列**：
  - 请求在队列中等待最多 N 秒（建议 2–5 秒，按测量定）；
  - 超时才返回 429，并始终带 `Retry-After`；
  - 队列长度、等待时间、拒绝原因进入指标；
  - 设备公平性：同一设备仍只占 1 个活动槽，避免一个设备挤占全部。
- 服务端拒绝时返回明确 `code` 和 `Retry-After`；客户端：
  - 尊重 `Retry-After`，使用指数退避 + jitter；
  - 不紧循环重试；
  - 任务保持 durable，不因 429 丢失 bundle。
- 不在没有内存测量前把全局槽从 2 提高；R2 已显示单槽解析/上传有内存峰值。

验收：

- 队列有空位时 0 个“瞬时”429。
- 队列满时的 429 都带 `Retry-After`。
- 客户端 429 后不产生超过预期的重试风暴；恢复后仍使用原 bundle 字节。
- 单设备并发仍为 1；全局峰值内存不超过已测预算。

### 9.5 Worker 分道

当前 13 种 job 共用一个处理槽。实施：

- 将 job 分为至少两类：
  - 交互/短任务：`analysis.workout`、`analysis.sample`、`analysis.series`、训练状态等；
  - 重任务/回填：模型栈、性能曲线、全量重算等。
- 每类独立领取循环和并发上限；底层仍用数据库租约，保证不重复处理。
- 默认并发不盲目提高；先测量连接池、CPU、内存和锁冲突。
- 增加 `worker_job_wait_seconds{kind}`，验收交互任务排队时间。
- 保持 lease renewal 和 fencing 语义不变；分道不能绕过 dataset 行锁。

验收（合成回填负载）：

- 一个重任务运行时，新 `analysis.workout` 的排队 P95 ≤ 2 s（本机隔离环境；若未达到，记录实际值和原因）。
- 无重复领取、无 lease 丢失导致的重复发布。
- 内存/连接数不超过配置预算。

---

## 10. P4：回程与产品读取

### 10.1 启用 changefeed

服务端已有 `/v2/sync/changes`，但 handshake 为 `changefeed: false`，且分析发布不写 change event。

实施：

1. 在 `PublishAnalysis` 的同一事务中写 `change_events`：
   - `entity_kind = 'analysis_publication'`（或现有分析实体族）；
   - `entity_key` 指向 Workout scope；
   - `operation = 'upsert'/'delete'`；
   - payload 只含状态、generation、metric family 等非敏感摘要。
2. handshake 在客户端能力具备时宣告 `changefeed: true`；旧客户端忽略。
3. 客户端新增 change feed：
   - 持久化 `changesCursor`（按 dataset/epoch 校验）；
   - App 前台、上传 ACK 后、分析跟进期间拉取；
   - `limit` 使用服务端上限，分页推进；
   - 只做失效/触发，不把 change payload 当产品数据；
   - 游标失效（422）时回退一次全量刷新，不无限重试。
4. 用 changes 替代 `refreshServerStatuses` 的全量 8×50 扫描；没有变化时只发一次 changes 请求。
5. 分析发布事件到达后，触发对应 Workout 的 freshness/detail 刷新；没有事件时保留有界退避。

验收：

- `changefeed: false` 不再出现在支持该能力的 handshake 中。
- 无变化前台恢复：0 次 8 页列表扫描；1 次 changes 请求。
- 分析发布后 ≤ 5 s 内客户端观察到状态变化（本机 LAN，合成任务）。
- 游标失效时只回退一次全量，不产生循环。

### 10.2 本地读缓存与条件请求

- 新增 `cache.sqlite`（可删除）：
  - `product_snapshot(dataset_id, epoch, fetched_at, payload)`
  - `workout_detail(workout_id, etag, fetched_at, payload)`
  - `workout_series(workout_id, etag, fetched_at, payload)`
  - `workout_freshness(workout_id, etag, fetched_at, payload)`
- 启动/前台：
  - 先展示缓存（标注“刷新中”），再网络刷新；
  - dataset/epoch 不匹配时整体清空，不能跨 dataset 混用；
  - 文件保护与现有本地状态一致。
- 服务端：
  - 对产品读取返回 `ETag`（响应体哈希或 dataset commit_rev + scope 组合）；
  - 支持 `If-None-Match` → 304；
  - 上传/认证/敏感写接口保持 `no-store`；产品读取使用 `private, no-cache` 或等价的可重验证策略；
  - 不把健康值写入日志。
- 客户端对 304 更新时间戳，不重写 payload。

验收：

- 首页热启动先出缓存 ≤ 100 ms（本机/模拟器）。
- 未变化详情请求返回 304，网络字节显著下降。
- dataset 切换后不显示旧 dataset 缓存。
- 缓存损坏时可回退网络，不阻塞启动。

### 10.3 首页请求收敛

现状：6 个 GET 串行。方案按优先级：

1. 先做本地缓存 + 条件请求；首屏不再等待 6 个 RTT。
2. 将互不依赖的产品块用有界并发（建议 2–3）加载，仍遵守 handshake 的请求预算；失败键独立记录，不能用并发绕过限流。
3. 如果测量显示 RTT/请求数仍是瓶颈，新增服务端聚合读取（例如 `/v2/product/snapshot`）：
   - 一次返回 plan/state/overview/workouts/history/athlete 的产品投影；
   - 服务端内部复用现有查询，不改变各块语义；
   - 客户端在能力宣告后使用，旧路径保留兼容；
   - 聚合接口必须有独立预算和分页上限，不能变成新的全量导出。

验收：

- 首页网络请求数 6 → ≤ 2（缓存命中时 0–1）。
- 首页 P95 不高于基线；缓存命中首屏 ≤ 100 ms。
- 单个块失败不拖垮其他块；错误码仍可区分。

### 10.4 详情、series、freshness 拆分

服务端：

- 拆分 `loadWorkoutDetailData` 的装载范围：
  - `WorkoutDetail`：Workout、enrichments、statistics、events、必要的摘要；不装载全部 series points/routes，除非合同要求。
  - `WorkoutSeries`：只装载 series chunks + routes。
  - `WorkoutFreshness`：只查询完整性/分析状态、计数、更新时间；不调用完整 loader。
- 三个 handler 不再各自全量装载同一份数据。
- 如果详情合同要求同时返回 series，提供显式 `include` 参数；默认保持客户端当前所需字段，避免破坏 UI。

客户端：

- `loadWorkoutDetail`：detail 成功后，series 与 freshness 并发（有界 2）或由 detail 携带 freshness 摘要；总请求 ≤ 2。
- 详情轮询只调用 freshness（必要时 series），不重复拉完整 detail。
- 轮询改为自适应：`[2,5,10,15,30,60,120,300]` 秒，最大约 10 分钟；published/partial/后台/离开页面即停止；与 changefeed 去重。

验收：

- 打开详情网络请求 ≤ 2；服务端 `detail_rows_scanned`/`bytes` 较基线下降。
- freshness 查询不扫描 samples/series/routes 全表。
- 分析发布在 ≤ 60 s 内自动显示（合成任务，LAN），轮询在 10 分钟内停止。
- 详情 UI 不出现重复 loading 或旧 Workout 数据混入。

### 10.5 状态列表

- `refreshServerStatuses` 改为 changes 驱动；保留一次有界全量校准（例如启动后首次或游标失效）。
- 列表请求使用 ETag/`since`，不再每次 8×50。
- 状态更新按 Workout 定向刷新，不重建整张表。

验收：无变化恢复时列表请求 ≤ 1；状态变化 ≤ 5 s 可见；全量校准有明确上限。

---

## 11. 哈希与 I/O 治理清单

不是“哈希越少越好”，而是“信任边界保留、重复计算消除”。

| 对象 | 必须保留的校验 | 要消除的重复 |
| --- | --- | --- |
| 上传 bundle 原始字节 | 构建时哈希一次；服务端接收后校验一次；发送前验证一次 | 同一请求内重复 `BundleDigest`；把 multipart 当 JSON 的失败解码 |
| gzip wire | 派生时哈希一次；复用缓存时校验一次 | 每次上传重新压缩/重新哈希未变化的 wire |
| segment | 封存滚动哈希 + seal 哈希；发布时验证一次 | 对同一 segment 的多次完整重读；per-record 重复构造 source/event |
| 完整性分页 | 写入时哈希；stage 校验一次；发送前校验一次 | cursor/stage/ack/deliver 各重读一遍 |
| series 文件 | 持久化时哈希；proof 引用摘要 | 一次刷新里重复读取所有 quantity series |
| manifest | stage 时哈希；发送前校验一次 | ack 时重新解码/重哈希全部分页 |
| 服务端 raw payload | 首次 proof 必须验证 | carried proof 的重复读取；分析/特征热路径的全量读取 |

代码级目标：

- 每个不可变产物的全量哈希次数 ≤ 2（封存 + 发送前验证），增量滚动哈希不计入。
- 每次上传的完整 bundle JSON 解码 ≤ 1 次；`coverageFromBundle` 不再做全量解码。
- 每样本的 `DiscoveryEvent` 构造 1 次；`source`/`event` 复用。
- 服务端 `BundleDigest` 保留；只有发现同字节在同一请求被哈希两次时才去重。

---

## 12. 观测与告警

### 12.1 客户端

- 每次同步输出一条结构化摘要：Workout 短哈希、阶段耗时、HK 查询数、bundle 字节、哈希字节、上传重试、完整性 proof 状态。
- 不输出健康值、GPS、完整 UUID、source/device 标识。
- 本地诊断页可显示：状态库体积、各表行数、未交付事件数、blocked Workout 数、GC 队列长度。

### 12.2 服务端

- 每个导入/完整性请求的 phase 计时。
- 锁等待、锁持有、事务回滚码。
- 上传队列深度、等待时间、拒绝原因。
- proof cache 命中/失效原因（新 bundle、删除、子删除、无缓存）。
- Worker 分道队列长度和等待时间。
- 回程请求的 ETag 命中率、304 比例、changes 游标延迟。

### 12.3 告警阈值（建议，执行时校准）

- 单 dataset 导入锁持有 P95 超过基线的 2 倍。
- 上传 429 比例 > 1% 且持续 5 分钟。
- proof cache 命中率在无变化周期 < 90%。
- 客户端 `state_bytes` 周增长 > 20%。
- changes 游标延迟 > 30 秒。
- Worker 交互任务等待 P95 > 5 秒。

---

## 13. 验收标准

### 13.1 正确性与数据完整性（全部必须通过）

| ID | 验收项 | 方法 | 门槛 |
| --- | --- | --- | --- |
| A-01 | multipart coverage 解析 | Swift 单元 + 模拟器 | 包内 observation 与任务 coverage 完全一致；无静默 fallback |
| A-02 | `.unsupported` 语义 | 单元 + 模拟器 | 不进入 `missing`/`pendingReadKeys`；不单独导致永久 `.partial` |
| A-03 | 单 Workout 故障隔离 | 200-Workout fixture | 199 个进度保留；仅坏对象重试；无假 manifest |
| A-04 | ACK 清理 | 单元 + 状态审计 | `captureManifest=nil`；字节差有界；重试原回执 |
| A-05 | 无变化关联 | 单元 | 第二次调用 0 提交 |
| A-06 | 上传幂等 | PG 集成 | 同字节重试返回原回执；不新增 import/job |
| A-07 | 删除优先 | PG 集成 | 删除后旧 bundle 不能复活 UUID；删除回执正确 |
| A-08 | SQLite 迁移 | 单元 + 模拟器故障注入 | 计数/回执/任务一致；kill 后可重试；不静默空库 |
| A-09 | proof 缓存失效 | PG 集成 | 新 bundle/子删除/父删除后 carried proof 被拒绝或重新验证 |
| A-10 | 分析跟进 | 模拟器 + 合成服务端 | 退避后可见；10 分钟停止；ACK 不降级 |
| A-11 | changes 游标 | 集成 + 模拟器 | 失效只回退一次；不循环；状态最终一致 |
| A-12 | ETag/304 | 集成 | 未变化返回 304；dataset 切换不串缓存 |

### 13.2 资源与性能（必须测量，不能推断）

| ID | 验收项 | 门槛 | 备注 |
| --- | --- | --- | --- |
| B-01 | 正常路径 `state.json` 写入 | 0 | 代码 + 运行时 |
| B-02 | 200 Workout 元数据体积 | `sync.sqlite` ≤ 16 MiB | 不含 bundle/segment 文件 |
| B-03 | 冷启动本地库 | P95 ≤ 500 ms | 同等级设备/模拟器 |
| B-04 | 普通提交 | ≤ 256 KiB；分页/捕获 ≤ 1 MiB | 逻辑字节，非 WAL 页 |
| B-05 | 无变化同步 | 0 新 POST、0 HK 子查询；P95 ≤ 2 s | 现有 P06 口径 |
| B-06 | 无变化完整性后续周期 | 0 HK 子查询、0 series 读取、0 POST；proof 复用 ≥ 95%；耗时 ≤ 基线 20% | 200-Workout fixture |
| B-07 | 服务端 carried proof | raw payload 读取 0 字节；锁 P95 ≤ 1 s | 200 proof |
| B-08 | 服务端锁外准备 | `decode/canonicalize` 不在 `dataset_lock` 区间 | phase 计时 |
| B-09 | 导入 P95 | 不低于 R2 基线；原门槛 S60 ≤1 s、S240 ≤4 s 仍按原口径记录 | 未达成不得宣布通过 |
| B-10 | S60H/S240 RSS | S60H ≤ 256 MiB；S240 ≤ 450 MiB（若执行） | 原 R2 门槛，未达成记录 FAIL |
| B-11 | 上传准入 | 队列有空位时 0 瞬时 429；429 带 Retry-After | 客户端退避 |
| B-12 | Worker 交互等待 | P95 ≤ 2 s（合成回填） | 未达成记录实际值 |
| B-13 | 首页请求 | 6 → ≤ 2；缓存命中首屏 ≤ 100 ms | P95 不回归 |
| B-14 | 详情请求 | 3 → ≤ 2；freshness 不装载全量点集 | 行数/字节测量 |
| B-15 | 分析可见时延 | ≤ 60 s（合成任务，LAN） | 有界轮询/changefeed |
| B-16 | manifest | ≤ 16 MiB；超过触发 v3 评估 | 200-Workout fixture |
| B-17 | 哈希次数 | 每个不可变产物全量哈希 ≤ 2；bundle JSON 解码 ≤ 1/上传 | 计数器 |

### 13.3 原有性能门槛的处理

R2 的以下门槛已经 FAIL，本方案不追溯修改：

- S60 客户端 P50 ≤ 5 s / P95 ≤ 8 s（实测 8.18 s / 11.26 s）。
- S60H 客户端 P50 ≤ 5 s / P95 ≤ 8 s（实测 18.27 s / 24.64 s）。
- S60H RSS ≤ 256 MiB（实测 302.34 MiB）。
- 服务端 S60 P95 ≤ 1 s、S240 P95 ≤ 4 s（实测 4.76 s / 19.72 s）。
- 10×S60 ≤ 60 s（实测 80.22 s）。

修复后必须重新跑同一口径并记录。若仍不达标：

- 不得用“相对提升”替代绝对门槛；
- 不得删除慢请求、只统计成功样本；
- 必须给出分段证据和物理下限，再由用户决定是否调整目标、部署条件或范围。

### 13.4 测试与证据门禁

必须完成：

1. `scripts/local/test.sh quick`。
2. `scripts/local/test.sh swift`。
3. `scripts/local/test.sh simulator`。
4. `scripts/local/test.sh e2e`。
5. `scripts/local/test.sh gate`（Go race/vet/边界）。
6. 受影响的真实隔离 PostgreSQL 18 集成测试（事务/锁/proof 缓存/删除/重试），使用 `scripts/local/test.sh integration` 定向运行。
7. `scripts/local/test.sh full`（完成阶段）。
8. `go test -race ./...`、`go vet ./...` 和依赖边界检查（gate 已包含也必须显式记录）。
9. `scripts/local/test.sh syncperf` 普通构建性能对照。
10. 真机只读验收（有授权时）：S60/S60H/S240 中至少已授权的档位，记录时延/RSS/计数；不得导出健康值。

证据目录建议：

```text
docs/evidence/sync-remediation-2026-11-07/
  README.md
  baseline/
  p0-correctness/
  p1-persistence/
  p2-completeness/
  p3-server/
  p4-return-path/
  gate/
  device/            # 仅在授权时
```

每个证据文件必须包含：命令、构建类型、commit、fixture/seed、样本数、失败数、P50/P95、设备/OS、限制条件。`docs/IMPLEMENTATION.md` 只追加真实结果和未验证项，不改写历史结论。

---

## 14. 发布与回滚

### 14.1 服务端

- 迁移均为附加式：proof 缓存表、observation 抽取表/列、analysis change event。先加结构，再切读路径，最后考虑归档。
- API 与 Worker 同镜像发布；先确保迁移幂等和 checksums 正确。
- proof 缓存逻辑可以先以配置关闭，观察指标后开启；关闭时仍走现有完整校验，语义不变。
- 回滚代码时保留新增表和列（不破坏旧代码）；不自动删数据。
- 任何 schema 变更都必须有迁移验证、回滚说明和隔离 PG 测试。

### 14.2 客户端

- SQLite 迁移是一次性、可恢复的；旧 `state.json` 保留备份。
- 新版本不回写旧格式；降级需要显式导出/恢复流程，不能静默丢 ACK。
- 先在小范围设备/模拟器验证迁移，再扩大；本方案不授权自动部署或真实数据迁移。
- 完整性 proof 复用和 changes 消费都通过 capability/本地版本控制；服务端不支持时保持旧路径。

### 14.3 协议

- P0–P2 不改变 wire protocol。
- 只有 manifest 体积或服务端锁目标无法满足时，才启动 completeness v3；v3 必须 capability 协商，服务器同时支持 v2/v3。
- 任何协议变更都要更新 `contracts/`、OpenAPI（如适用）和相关 contract tests。

---

## 15. 风险与缓解

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| SQLite 迁移丢失 ACK/任务 | 重复上传或删除复活 | 单事务导入、计数校验、备份、kill 注入测试、fail closed |
| proof 复用导致陈旧证明 | 服务端接受已被删除/修改的 Workout | 以 per-Workout `commit_rev` + tombstone 失效；集成测试覆盖新 bundle/子删除/父删除 |
| 减少 payload 读取破坏分析 | 分析/特征缺 provenance | 先抽取/回填/双读验证，再切读；保留 payload 审计 |
| 锁外准备引入 TOCTOU | 身份/删除检查与写入不一致 | 所有最终检查仍在锁内；锁外只做纯计算 |
| 并发/队列增加内存 | RSS 峰值、OOM | 保留槽上限；有界队列；测量峰值；不盲目扩容 |
| ETag/缓存泄露或串 dataset | 隐私/错误数据 | dataset/epoch key；文件保护；切换清空；敏感接口 no-store |
| v3 协议分叉 | 旧客户端不兼容 | capability 协商；服务器双版本；默认不启用 |
| 性能仍不达标 | 不能签“优化完成” | 记录物理下限；不修改门槛；由用户决定部署/范围 |

---

## 16. 非目标

- 不新增 Web、Watch、桌面客户端。
- 不重做 HealthKit 发现协议或改变事实语义。
- 不关闭 durability、不截断窗口、不降采样、不丢弃不支持字段。
- 不删除原始 payload 审计材料（除非单独授权并完成保留策略）。
- 不把 sequence 当下载水位，不绕过 dataset 行锁。
- 不自动部署、不切换旧入口、不迁移真实数据、不 commit/push。
- 不用单元测试/mock 替代 PostgreSQL 集成或真机验收。
- 不为了让指标好看而删除慢请求、失败样本或降低现有门槛。

---

## 17. 工作分解与依赖

### 17.1 顺序

```text
Phase 0 基线/计量
   ├─ P0 正确性（可并行）
   │    ├─ 6.1 multipart
   │    ├─ 6.2 unsupported
   │    ├─ 6.3 故障隔离
   │    ├─ 6.4 ACK 清理
   │    ├─ 6.5 no-op 提交
   │    └─ 6.6 分析跟进
   ├─ P1 SQLite（依赖 P0 的状态模型清理）
   │    └─ P2 客户端 proof 复用（依赖 P1）
   ├─ P2 服务端 proof 缓存（依赖 P3 的 schema/观测）
   ├─ P3 服务端锁外准备（独立）
   ├─ P3 payload 抽取（依赖 P2 proof 缓存切读）
   ├─ P3 Worker 分道（独立）
   └─ P4 回程（可并行；changefeed 依赖服务端分析事件）
```

### 17.2 建议里程碑

| 里程碑 | 内容 | 退出条件 |
| --- | --- | --- |
| M0 | Phase 0 基线 | 所有计数器/证据落盘；现场症状复现或标记未复现 |
| M1 | P0 正确性 | A-01～A-06、A-10 通过；quick/swift/simulator 通过 |
| M2 | P1 持久化 | A-08、B-01～B-04 通过；迁移/裁剪/GC 测试通过 |
| M3 | P2 完整性 | A-03/A-09、B-06/B-07/B-16/B-17 通过 |
| M4 | P3 服务端 | B-08～B-12、payload 抽取测试通过 |
| M5 | P4 回程 | A-11/A-12、B-13～B-15 通过 |
| M6 | 完成门禁 | `gate`/`full`、隔离 PG、性能复测、IMPLEMENTATION 更新 |

工作量为粗估：P0 3–5 人日，P1 5–8，P2 8–12，P3 5–8，P4 5–8，验收 3–5；不含真机等待和协议 v3。实际以执行时拆分为准。

---

## 附录 A：代码定位索引

| 区域 | 文件/函数 |
| --- | --- |
| 本地全量状态 | `ios/RAI/Sources/SyncCore/OutboxStore.swift`：`commit`、`releaseBundleFile`、`acknowledgeBundle` |
| 发现/关联 | `ios/RAI/Sources/SyncCore/HealthDiscovery.swift`：`stageDiscovery`、`promoteDiscovery`、`associateDiscoveredFacts` |
| 组包/覆盖率 | `ios/RAI/App/SyncCoordinator.swift`：`coverageFromBundle`、`performSync`、`uploadAndAcknowledge`、`refreshAnalysis`、`syncWorkoutCompleteness`、`completenessSeries` |
| 覆盖率语义 | `ios/RAI/Sources/SyncCore/WorkoutSync.swift`：`WorkoutCoverage.isAvailable`、`WorkoutSyncTask.missing` |
| 完整性状态 | `ios/RAI/Sources/SyncCore/WorkoutCompleteness.swift`：`beginWorkoutCompleteness`、`captureCompletenessPage`、`stageWorkoutCompleteness`、`acknowledgeWorkoutCompleteness` |
| 上传交付 | `ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift`、`WorkoutBundle.swift` |
| 捕获/哈希 | `ios/RAI/App/HealthKitClient.swift`：`capturedFact`、`discoveryFingerprint`；`CaptureSegments.swift`；`CaptureConcurrency.swift` |
| 恢复/轮询 | `ios/RAI/App/RAIApp.swift`：`performRecovery`、`refreshServerStatuses`；`ProductModel.swift`；`WorkoutDetailView.swift` |
| 产品读取 | `ios/RAI/Sources/ProductCore/Product.swift`：`load` |
| 导入事务 | `apps/server/internal/storage/postgres/workout_bundles.go`：`ImportWorkoutBundle` |
| 批量身份 | `apps/server/internal/storage/postgres/bundle_batching.go`：`seriesIdentity`、`routeIdentity` |
| 完整性 v2 | `apps/server/internal/storage/postgres/workout_completeness_v2.go`、`workout_completeness.go` |
| 详情装载 | `apps/server/internal/storage/postgres/workout_detail.go`：`loadWorkoutDetailData`、`WorkoutDetail`、`WorkoutSeries`、`WorkoutFreshness` |
| 上传准入 | `apps/server/internal/httpapi/server.go`：`acquireUpload` |
| Worker | `apps/server/internal/jobs/worker.go`：`Run` |
| changes | `apps/server/internal/storage/postgres/changes.go`、`httpapi/server.go` 的 `/v2/sync/changes` |
| 分析发布 | `apps/server/internal/storage/postgres/analysis_publish.go`：`PublishAnalysis` |

## 附录 B：验收检查清单（可复制）

```text
[ ] P0-1 multipart coverage 解析通过，无静默 fallback
[ ] P0-2 unsupported 不进入 missing/pendingReadKeys
[ ] P0-3 单 Workout 故障不阻塞其他 Workout，进度保留
[ ] P0-4 ACK 清理大字段，重试仍返回原回执
[ ] P0-5 无变化关联不提交
[ ] P0-6 分析状态有界退避跟进
[ ] P1-1 state.json 退出正常路径
[ ] P1-2 SQLite 迁移/回滚/kill 注入通过
[ ] P1-3 事件裁剪/任务压缩/GC 通过
[ ] P1-4 200 Workout 元数据 ≤16 MiB，冷启动 ≤500ms
[ ] P2-1 无变化完整性后续周期 0 子查询/0 series/0 POST
[ ] P2-2 proof 复用 ≥95%，服务端 carried raw=0
[ ] P2-3 200 proof 完整性锁 P95 ≤1s
[ ] P2-4 manifest ≤16MiB 或触发 v3 评估
[ ] P3-1 规范化/哈希移出 dataset 锁
[ ] P3-2 payload 抽取后分析/特征/完整性热路径不读全量
[ ] P3-3 上传队列/429 Retry-After 通过
[ ] P3-4 Worker 分道，交互等待达标
[ ] P4-1 changes 启用且分析发布有事件
[ ] P4-2 本地缓存/ETag/304 通过
[ ] P4-3 首页请求 6→≤2，详情 3→≤2
[ ] P4-4 详情轮询有界且分析可见
[ ] G-1 quick/swift/simulator/e2e 通过
[ ] G-2 gate/full 通过，隔离 PG 集成通过
[ ] G-3 性能复测记录，不篡改原门槛
[ ] G-4 docs/IMPLEMENTATION.md 更新真实证据与未验证项
```

---

本文是执行计划。任何“已完成”“已优化”“已通过”的结论，必须由对应的 E1/E2/E3 证据支撑；在证据产生前，本文件不构成实现或验收声明。
