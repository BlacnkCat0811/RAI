# RAI V2 — RQrun Phase 2 协议与回执可靠性验收

日期：2026-11-01（Asia/Shanghai）。范围：RQ-005、RQ-006、RQ-009。以工作区真实源码复核 Phase 1，不依赖旧报告推断实现完成。

## 1. 执行摘要

**PASS WITH RISKS**。RQ-005 / RQ-006 / RQ-009 的实现和本机自动化通过，未发现仍失败的核心反例；Phase 1 数据安全回归仍通过。真实 iPhone/HealthKit/OS 后台恢复、物理断电和部署未验收，因此不宣称完整真机 PASS。

核心不变量：没有验证通过的持久化上传 ACK，不进入已确认状态，不授权清理当前原始上传包。GET 404 不代表删除；409 workout_deleted 不是 POST/DELETE 成功；验证失败、事务回滚均无成功 ACK。

工作区入口 HEAD：`aeb769c82f6af6c47d7cac82874f48ff37f635fc`，`main`，已有大量未提交改动。已执行用户要求的五项 Git 检查，保存 `/tmp/rai-phase2-baseline/{status,diff,cached,untracked,hashes.json}`。未删除任何原有文件/测试，未隐藏或回滚现有修改。所有 migration/校验清单、Phase 1 存储实现及原始架构方案保持入口字节。

## 2. RQ-005 修复

根因：SyncCoordinator 仅捕获 POST 的网络/5xx，然后直接传播 GET 404；外层按非 retryable 4xx 置 FAILED。v3 task 也没有沿用旧 page 队列的持久退避。用户重新同步还可能释放旧包并重建。

复用原 `WorkoutSyncTask` 与同一 `OutboxStore`，未增加第二套 Outbox 或上传协议。PREPARING 为读取/组包，UPLOADING 包含在途和 RetryPending，SERVER_ACKNOWLEDGED 代表验证并持久化的导入成功；FAILED 为确定性待处理/删除冲突，DELETED 仅来自有效删除 ACK。分析仍异步推进，不替代当前包的导入 ACK。

新增可选持久字段：attemptCount、nextAttemptAt、lastUploadError、uploadReceipt、deletionReceipt，旧 state.json 继续解码。请求前提交 attempt 与 60 秒恢复时间，进程死亡后不永久锁住；失败后以失败时间计算 1/2/4…900 秒指数退避，Retry-After 可以更长（支持秒数和 HTTP-date）。同一 Store actor 的 claim 防止两个 Worker 并发提交；App 仍使用单个 Store，无跨进程扩展客户端。前台按持久时间唤醒，后台服从 iOS 调度，不读取 HealthKit。握手/认证阶段失败也落同一退避字段。

| 结果 | 行为 |
|---|---|
| 网络、超时、408、5xx | 原包/原 hash 保留；可 GET 相同 sha256 恢复，否则持久退避重 POST |
| GET 404 | 继续未确认，不设 Deleted/已确认，不释放包；不把 404 改写成 workout_deleted |
| 429 | 尊重 Retry-After 与退避；不立即 GET 消耗更多共享预算 |
| 401 | 可选凭据提供者仅刷新一次；当前静态设备 token 没有刷新端点，缺少提供者时 authentication_required + 至少 60 秒退避，等待现有配置/Keychain 更新 |
| 403、400/422、身份冲突 | FAILED 待处理，保留原文件，不快速循环 |
| 409 workout_deleted | FAILED 删除冲突，保留诊断，不重试、不伪装已确认或 Deleted；旧回执不能覆盖该已知冲突 |
| 准备文件损坏/缺失 | 明确待处理，不重新读取并替换唯一待确认包 |

重试身份不变：绑定 dataset + HKWorkout UUID + bundleSHA256；原 multipart 文件/边界复用，发送前核验内部 JSON 精确哈希。显式 rescan 也不能替换待确认文件。服务端仍以 dataset 行锁、现有事实身份/内容哈希和 workout_imports 主键保护幂等；相同字节原回执，相同 UUID 核心内容冲突，迟到子数据追加各自历史回执。未修改 Phase 1 事务或数据库 schema。

涉及文件：`WorkoutSync.swift`、`WorkoutBundleDelivery.swift`、`OutboxStore.swift`、`RAIAPIClient.swift`、`SyncCoordinator.swift`、`RAIApp.swift`、`BackgroundTransfers.swift` 及新增永久测试。

## 3. RQ-006 修复

原边界使用 DecodeStrictJSON + RejectNulls + Validate，顶层严格但嵌套普通 float64/int64 在缺失时已经成为 0。新增 `DecodeWorkoutBundle`：先按真实 DTO 递归检查原始 JSON 的字段存在性/null/类型，再严格检查大小写/未知/重复字段及原有值、成员和预算规则。零值只有明确存在时才可能进入 typed DTO。没有把所有可选元数据改成必填，没有新增 device/sourceRevision/schema_version 字段。

错误沿用扁平 `syncing.Error`，增加可选 field/reason：`422 {"code":"unsupported_payload","field":"routes[0].points[0].latitude","reason":"required"}`。数组下标从 0 开始，文档级错误为 `$`；不附健康值或数据库诊断。HTTP 写事务之前完成字段校验；真实数据库失败用完整业务快照比对原事实、屏障、日志、dirty/job、回执与分析历史均未改变。

### 实际字段矩阵

| 对象 | 字段 | 合同、零值与省略语义 |
|---|---|---|
| Bundle | protocolVersion | 必填整数，恰为 3 |
| Bundle | datasetID / datasetEpoch / collectorEpoch / workoutID | 必填规范小写非零 UUID；Workout 身份必须一致 |
| Bundle | workout / samples | 必填对象/数组；samples 允许空数组；无样本不生成默认值 |
| Bundle | series / routes | 可选数组，省略等价无此包新增子事实；绝不删除已存事实 |
| Bundle | completeSampleTypes / completeRoutes | 可选数组/布尔；省略为无覆盖声明，false 合法；不授权删除 |
| Workout / Sample Event | op / kind / type / uuid | 必填；upsert、workout/sample、支持的 HealthKit type、规范非零 UUID |
| Workout / Sample Event | startUs / endUs | 必填 int64 Unix 微秒，允许 epoch 0；范围 year 1–9999，start ≤ end；不静默修正 |
| Workout / Sample Event / Route | source.bundleID | 必填 1–255 字节字符串；没有额外设备必填项 |
| Event / Activity / Workout Event | metadata | 可选对象，原有 JSON 内容保留，递归拒绝 null；省略不合成 provenance |
| Workout Event | workout | 必填结构；quantity/category 不允许与 Workout 混用 |
| Workout payload | activityType / durationUs | 必填；activityType 1…int32 max，durationUs ≥0 且不超过原始时间范围，0 时长合法 |
| Workout payload | totalDistance / totalEnergyBurned | 可选 Quantity；存在时 value/unit 必填；非负有限数，单位 m/kcal；0 合法 |
| Workout payload | statistics / events / activities | 可选数组；沿用 128/256/128 及总 detail budget |
| Activity | uuid / startUs / durationUs / configuration | 必填；时间位于父范围，非负 duration；duration=0 合法 |
| Activity | endUs | 可选；无值保持缺失，仅以父 end 作验证上界 |
| Configuration | activityType / locationType / swimmingLocationType | 必填整数；activityType ≥1，另两个原始枚举允许 0 |
| Configuration | lapLength | 可选 Quantity；存在时非负 m，0 合法 |
| Statistic | type / startUs / endUs | 必填；受支持 quantity type，排序唯一，父范围内 |
| Statistic | sum / average / minimum / maximum | 可选 Quantity，至少一个存在；不填补缺席聚合 |
| Workout Event 子对象 | type / startUs / endUs | 必填；type ≥1，父 Workout 范围内 |
| Quantity Sample | quantity.value / quantity.unit | quantity 对象和值/单位必填；有限浮点，0 合法，单位严格匹配 Units 注册表 |
| Category Sample | category | 必填整数 0…5；不允许伪造 quantity |
| Series | sampleType / sampleUUID / points | 必填；必须引用本包 Samples 父对象；points 非空 |
| Series Point | startUs / endUs / value / unit | 全必填；0 数值合法，单位匹配父 type，时间包含于父 sample |
| Route | routeID / source / points | 必填；UUID，非空 points；序列由原数组顺序生成 |
| Route Point | recordedAtUs / latitude / longitude | 全必填；微秒时间范围同上，纬度 [-90,90]，经度 [-180,180]；0、0,0 合法 |
| Route Point | altitude / horizontalAccuracy / verticalAccuracy / speed / course | 可选有限数；altitude 可负，其余 ≥0，course <360；0 合法 |

数量合同保留原 v3 的有限源数值与精确单位：心率/步频 count/min、功率 W、距离 m 等由 Units 注册表逐 type 校验，不统一施加 >0 或临床过滤；恢复心率差可有符号。Workout 总距离/总能量/lapLength 沿用非负约束。NaN/Inf、溢出、错误 JSON 类型拒绝。Sample 自身范围检查、Series 父 Sample 包含、Workout 子明细范围均沿用原合同；不静默修正时间，不扩展 RQ-013。

兼容性：仍 protocolVersion=3；合法原客户端/旧 Outbox 字节不变，可选指标缺失合法，0、0,0 合法。原本不合法的 missing quantity.value/latitude 等现在明确拒绝。OpenAPI 增加解码后的完整 WorkoutBundle schema 和 field/reason；sync-v3 第 10 节记录矩阵和恢复行为。未新增迁移。

## 4. RQ-009 修复

POST/GET 只接受 HTTP 200、非空 application/json（允许 charset）和完整可解析 JSON。WorkoutBundleACK 验证当前 UUID、精确 64 位小写 bundleSHA256、ingestStatus=committed、正 commitRevision、counts.workoutInserted 布尔、其余所有 count 必填非负整数、三个删除计数为 0、analysisStatus=queued/not_required 及必填 job 数组/规范 UUID；queued 不为空，not_required 必须为空。v3 没有独立 receiptID 或 datasetID 响应字段，不能虚构它们；错误 Receipt identity 测试对应实际 bundleSHA256。

原 OpenAPI minimum=0 修正为 1：首次导入创建 Workout 必然推进 revision，已有 Workout 引用的 revision 也为正；没有合法历史服务端回执因此失效。202/204/201、空 JSON、缺字段/null/类型错误、错身份、错状态、500 即使附正常 JSON 都不能直接完成同步。损坏 ACK 可按同 hash GET 恢复；GET 的 ACK 同样严格验证，404 仍保留文件。

Outbox 顺序：验证 ACK → 同一次 state.json 原子替换持久原回执/acknowledged/回收授权 → 独立 GC。状态写失败时内存权威状态不前进、文件仍被引用；写入成功后删文件失败，原回执和回收队列保留；在 unlink 前/后退出均可幂等恢复。重复 ACK 保持原回执，晚到失败回调不降级有效 ACK/Deleted。未确认包不能被 releaseBundle 或 beginTask(rescan) 提前回收；旧无证明的 acknowledged 文件重新核对。旧分析 publication 不证明当前 bundle 已导入。

BackgroundTransfers 将 Content-Type 同响应结果持久化，旧缺头缓存不能默认成功；无效缓存通过状态核对或原文件重传恢复。后台缓存丢弃不等于删除 bundle。恢复失败不更新全局“上次同步”。原 URLSession 稳定身份和重连逻辑保留；真实 OS 后台/杀进程仍为未验证风险。

DELETE 继续独立验证 200、对应 UUID、deleted、正 revision、alreadyDeleted 布尔；新增本地原子保存删除 ACK + Deleted + 回收授权。POST ACK 不能确认删除，DELETE ACK 不能确认上传；workout_deleted 冲突也不是删除回执。Phase 1 服务端屏障、dataset fencing、P4 generation CAS 和回滚路径没有改动。

## 5. 测试证据

所有 Go 命令先在仓库根目录 `source scripts/local/lib.sh`、`rai_go_env`；PG 脚本忽略外部 DSN/libpq 设置，创建新的 PG18 集群和每测试独立数据库，不访问持久开发库。下表每项均未使用真实设备。

```sh
# G0 普通测试 / G1 race / G2 vet（在 apps/server）
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go vet -tags=integration,swiftclient ./...
go build ./...
# G3 全量数据库（仓库根）
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration,swiftclient -p 1 -timeout=30m ./...
# G4 最终跨语言矩阵（15 场景）
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration,swiftclient ./integration -run TestWorkoutReliabilitySwiftClientPostgres -v
# G5 Phase 1 专项 + 跨语言（最终专项日志）
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration,swiftclient ./integration -run 'Test(Workout(Bundle(LateComplete|Concurrent|Explicit|SnapshotStill)|Delete(Before|Fencing|AndImport|Invalidates|Supersedes|Rollback))|HTTPWorkoutDeleteBefore|P7RealLoginBoundariesAndRevocation|WorkoutReliabilitySwift)' -v
# G6 字段存在性（apps/server）
go test -race -count=1 ./internal/syncing -run TestWorkoutBundleNestedPresenceAndLegalZero -v
# S Swift package（仓库根）
swift test --package-path ios/RAI --scratch-path /tmp/rai-phase2-swift-build
# I App tests（本次专用空白模拟器；现已删除）
xcodebuild -project ios/RAI/RAI.xcodeproj -scheme RAI -configuration Debug -destination 'platform=iOS Simulator,id=4CBE9160-1159-4864-9B92-F96DA96CB423' -derivedDataPath /tmp/rai-phase2-xcode-simulator -only-testing:RAITests test
# B Debug / Release iPhoneOS（分别执行）
xcodebuild -project ios/RAI/RAI.xcodeproj -scheme RAI -configuration Debug -destination generic/platform=iOS -derivedDataPath /tmp/rai-phase2-xcode CODE_SIGNING_ALLOWED=NO build
xcodebuild -project ios/RAI/RAI.xcodeproj -scheme RAI -configuration Release -destination generic/platform=iOS -derivedDataPath /tmp/rai-phase2-xcode CODE_SIGNING_ALLOWED=NO build
python3 scripts/check-boundaries.py
npm exec --yes --package=@redocly/cli@2.71.1 -- redocly lint contracts/openapi-v2.yaml
git diff --check
```

G0/G1/G2/构建/边界均 PASS；G3 integration 209.056s，G5 110.791s；之后仅补充测试矩阵及移除恢复失败时的“上次同步”写入，G4 最终 15/15 PASS（46.729s），App/Swift/编译复跑通过。服务端生产实现与 G3 运行时一致。G3 包含 `TestMigrationsIdempotentAndInitializationProtected`、readiness/权限/HTTP/事务回滚及原所有永久回归。PG18.6 的 fsync/full_page_writes/synchronous_commit/data_checksums 全 on、无 TCP 监听，所有最终调用均记录 LEFTOVER_DATABASES=0、CLUSTER_REMOVED=1。

S：79 项 Swift Testing / 9 suites + 2 项普通 XCTest PASS。`WorkoutReliabilityLiveTests` 在没有数据库 harness 的普通 S 中明确跳过 1 项；G4 中由隔离 Go harness 启动同一测试，真实执行 15 次均 PASS，不能把普通 S 的 skip 当作数据库 PASS。`swiftclient` tag 单独标出 macOS/Swift 验收依赖，默认 Go integration 不强制安装 Swift。I：17/17 PASS（16 Normalizer + 1 新协调器测试）；B：最终 Debug/Release 均 BUILD SUCCEEDED，无签名、未安装到真机。OpenAPI lint PASS，31 条既有 warning。

### RQ-005 / 交叉链路：真实 Swift → HTTP → PostgreSQL

每行命令均为 G4，结果均 PASS，真实 DB=是，真实设备=否。初次不确定/拒绝后断言 task 未确认、无 uploadReceipt/acknowledgedAt、原始 multipart 字节仍在；成功则先持久化原回执，GC 前文件仍在、GC 后可回收。证据接口只返回合成数据库计数，不返回健康值。

| 测试名后缀（TestWorkoutReliabilitySwiftClientPostgres/） | 关键断言 |
|---|---|
| before_send | POST 到达 API 前连接丢失；GET 404、0 imports；重启同包重传后 1 import/1 sample/1 job |
| during_send | multipart 读取中断；0 imports，文件保留；重传相同 hash 后只一份事实 |
| before_commit_timeout | 真实 dataset 锁等待 + 请求 deadline 取消，未 COMMIT；GET 404，重启后原包成功 |
| lost_ack | 后端真实 COMMIT 后关闭连接；按同 hash GET 恢复原回执，事实/任务各一份 |
| corrupt_ack | 后端 COMMIT 后损坏响应体；严格 GET 核对后完成，GC 前原包仍在 |
| commit_404 | 已 COMMIT 但故障代理 GET 返回 404；仍待重试，重启同包 POST 返回原回执，无重复 |
| validation_missing | 缺 latitude 返回稳定 422；FAILED 且保留源包，imports/samples/jobs/tombstones 全 0 |
| tombstone | DELETE-before-upload；旧 POST 409 workout_deleted，FAILED；imports/samples/jobs=0，tombstones=1 |
| rate_limit | 429 Retry-After=120；持久 nextAttemptAt 不早于 120 秒；恢复后各一份记录/原回执 |
| status_500 | 500 + JSON 不确认；GET 404，保留文件；重启后成功，无重复 |
| status_502 | 502 同上，DB 0→1，原 hash 不变 |
| status_503 | 503 同上，DB 0→1，原 hash 不变 |
| auth_refresh | 过期合成 token 得 401，刷新一次再 POST；真实 Bearer 认证及数据库提交成功，无丢弃 |
| two_workers | 两个 Delivery 并发只一个取得 claim，HTTP POST 数=1，数据库每项=1 |
| duplicate_post | 重复实际 multipart POST 返回完全相同原回执，重复本地 ACK 幂等；仅一个 import/sample/job |

上述所有需要重试的场景都重新构造 OutboxStore，检查 nextAttemptAt/源 hash 和原文件，覆盖 App 重启恢复 RetryPending。服务端幂等还有 G3 的 `TestWorkoutBundleOneRequestAtomicImport`、`TestWorkoutBundleReceiptHistoryKeepsEveryBundle`、`TestWorkoutBundleLostACKAnd404ThenIdempotentPOST`（完整数据库前后快照不变）。

### RQ-006：字段与事务

下列 HTTP 测试全部由 G3 执行，结果 PASS，真实 DB=是，真实设备=否。每个失败子用例比较完整 `phase1Snapshot`：事实、route/series 点、关联、tombstone、change、dataset revision、dirty/job、导入回执、分析历史和 publication 全部不变；422 field/reason 稳定且没有成功 ACK。

| 测试名 | 关键断言 |
|---|---|
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_workout_uuid | `workout.uuid` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_start | `workout.startUs` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_end | `workout.endUs` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_latitude | `routes[0].points[0].latitude` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_longitude | `routes[0].points[0].longitude` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_timestamp | `routes[0].points[0].recordedAtUs` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_quantity | `samples[0].quantity` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_value | `samples[0].quantity.value` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/null_value | `samples[0].quantity.value` / `null_not_allowed`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/null_optional | `routes[0].points[0].altitude` / `null_not_allowed`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/wrong_type | `routes[0].points[0].latitude` / `invalid_type`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/wrong_unit | `samples[0].quantity.unit` / `unsupported_unit`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/invalid_range | `workout.endUs` / `invalid_time_range`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/bad_latitude | `routes[0].points[0].latitude` / `out_of_range`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/series_missing_value | `series[0].points[0].value` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/distance_missing_value | `workout.workout.totalDistance.value` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/source_missing | `workout.source.bundleID` / `required`；全事务不变 |
| TestWorkoutBundleRequiredFieldsHTTPAtomic/missing_protocol | `protocolVersion` / `required`；全事务不变 |
| TestWorkoutBundleZeroAndLegacyOptionalHTTP | 已存在的合法 v3 请求，省略可选指标；0 latitude、0 longitude、0,0、quantity=0/series=0 真实保存，正回执且重试稳定，无重复 |
| TestWorkoutBundleNonfiniteAndMalformedHTTP/NaN | 无成功 ACK，完整数据库快照不变 |
| TestWorkoutBundleNonfiniteAndMalformedHTTP/Infinity | 同上 |
| TestWorkoutBundleNonfiniteAndMalformedHTTP/1e999 | 数值溢出拒绝，同上 |
| TestWorkoutBundleCommitFailureHasNoACK | 延迟到 COMMIT 的 PostgreSQL constraint trigger 抛错，503；所有写入回滚，无 committed ACK |
| TestWorkoutBundleLostACKAnd404ThenIdempotentPOST | COMMIT 后丢弃 ACK，临时 404 不改变事实；重放原回执及全库业务快照不变 |

G6（PASS，无 DB/真机）对下面每个显式路径分别执行 `/missing` 和 `/null`，共 62 个失败子用例；断言准确 field 和 required/null_not_allowed。基准包含合法 0 与可选 Activity.endUs 省略，不默认补齐；HTTP 原子性由上表真实 PG 补证。

```text
TestWorkoutBundleNestedPresenceAndLegalZero/workoutID/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workoutID/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activityType/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activityType/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.durationUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.durationUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.totalDistance.value/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.totalDistance.value/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.totalDistance.unit/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.totalDistance.unit/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.totalEnergyBurned.value/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.totalEnergyBurned.value/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.type/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.type/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.startUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.startUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.endUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.endUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.sum.value/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.statistics.0.sum.value/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.events.0.type/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.events.0.type/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.events.0.startUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.events.0.startUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.events.0.endUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.events.0.endUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.uuid/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.uuid/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.startUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.startUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.durationUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.durationUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.activityType/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.activityType/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.locationType/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.locationType/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.swimmingLocationType/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.swimmingLocationType/null
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.lapLength.value/missing
TestWorkoutBundleNestedPresenceAndLegalZero/workout.workout.activities.0.configuration.lapLength.value/null
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.sampleType/missing
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.sampleType/null
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.sampleUUID/missing
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.sampleUUID/null
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.startUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.startUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.endUs/missing
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.endUs/null
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.value/missing
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.value/null
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.unit/missing
TestWorkoutBundleNestedPresenceAndLegalZero/series.0.points.0.unit/null
TestWorkoutBundleNestedPresenceAndLegalZero/routes.0.routeID/missing
TestWorkoutBundleNestedPresenceAndLegalZero/routes.0.routeID/null
TestWorkoutBundleNestedPresenceAndLegalZero/routes.0.source.bundleID/missing
TestWorkoutBundleNestedPresenceAndLegalZero/routes.0.source.bundleID/null
TestWorkoutBundleNestedPresenceAndLegalZero/samples.0.quantity.value/missing
TestWorkoutBundleNestedPresenceAndLegalZero/samples.0.quantity.value/null
TestWorkoutBundleNestedPresenceAndLegalZero/samples.0.quantity.unit/missing
TestWorkoutBundleNestedPresenceAndLegalZero/samples.0.quantity.unit/null
```

### RQ-009 / 文件与客户端状态机

以下每项命令 S、结果 PASS；真实 DB=否、真实设备=否。它们操控真实临时 state.json 与 multipart 文件，并注入 transport/ACK/文件故障；数据库结论只取自 G3/G4，绝不以 mock 冒充数据库验收。

| 测试名（WorkoutReliabilityTests） | 参数/关键断言 |
|---|---|
| networkThen404RetriesOriginalBundle | notConnectedToInternet/networkConnectionLost/timedOut；POST→GET404→持久退避→重启→同包 POST；未确认前字节一致且 GC 0 |
| invalidACKPreservesSource | 见下方全部 28 参数；所有异常均无 uploadReceipt/acknowledgedAt、原包存在、GC 0、保持可重试 |
| committedResponseRecoveredByExactHash | lost/corrupt；GET 有效回执才确认，确认前后及 GC 前文件存在，授权 GC 后释放 |
| retryAndBlockedClassification | 408/429/500/502/503/401 可恢复；403/400/422/409 明确阻断；全部保留源文件；429 至少 Retry-After，workout_deleted 不可重新 beginTask |
| authenticationRefreshOnceAndRetryAfterDate | 一次刷新、Bearer 更新、HTTP-date=120 秒；文件直到清理前存在 |
| twoWorkersOneClaimAndRestartReleasesInFlight | 同 Store 双 claim 只有一份，重启保留 60 秒恢复时间、到期可再 claim；源文件仍在 |
| stateWriteFailureDuplicateACKAndCleanupFailure | 真实 state.json 写入故障不确认/不回收；重建 Store 恢复；重复 ACK 幂等；确认后退出、清理失败仍保存 ACK，恢复 GC |
| explicitRescanAndProoflessLegacyACKCannotReleaseBundle | rescan 不替换文件/hash，未确认 release 拒绝；无证明旧 acknowledged 状态重新核对 |
| handshakeFailureBackoffSurvivesRestart | 401 至少 60 秒持久等待；503 attempt 2…12 指数至 900 秒；403 阻断；文件均保留 |
| concurrentDeliveryAndLateDeletionCallbacks | 两 Delivery 只发一次 POST；有效删除 ACK 后晚到失败/POST ACK 不能覆盖 Deleted；GC 前文件存在 |
| WorkoutTaskTests.bundleFileIsDurableAndReclaimedAfterAck | 原旧测试保留并收紧：无 ACK 禁止释放，严格 ACK 后才 GC |
| APITests.workoutDeletionRequiresDurableACK | Phase 1 DELETE 的 404/空/错 UUID/错状态/零 rev/非布尔均拒绝；合法首次/重复 ACK 接受 |
| WorkoutCoordinatorReliabilityTests.testRecovery404KeepsExactBundleAndRetryTimeWithoutHealthReads（I） | 真实 App coordinator + 空 ACK/GET404 保留文件；第二次恢复不重传；HealthKit reads=0；查询分析不能确认当前包，lastACK/lastSync 均为空 |

`invalidACKPreservesSource` 的全部参数：empty、json、trailing_json、null、empty_jobs、bad_job、missing_id、wrong_id、wrong_receipt（实际 hash）、zero_rev、missing_rev、null_rev、type_rev、status、missing_jobs、null_jobs、counts_type、counts_missing、counts_negative、delete_ack、201、202、204、500、502、503、content_type、missing_content_type。每个独立用例均断言原 bundle 存在。ACK 处理过程中退出用 Store 重建验证两个持久边界，未宣称实际 iPhone SIGKILL 或 OS 回调 E2E。

### 失败证据保留

[失败原始记录](../evidence/rqrun-phase2-failed-runs.txt) 保留全部未通过尝试，不覆盖为 PASS：早期工作目录/部分实现编译失败、Swift 增量产物旧 initializer 链接失败（改用独立 scratch，并未清理用户产物）、测试夹具 f.pool/列名错误、XCTUnwrap await/动态 Comment/Sendable 闭包编译错误均已修正。COMMIT 失败用例原预期 500，真实既有 API 合同为 503；修正为精确 503，未放宽“全事务不变/无成功 ACK”断言。源码在 Swift 构建期间变化导致的整套失败保留，冻结后 G3 完整重跑通过；扩展矩阵的夹具编译失败在同一矩阵 15 项全部重跑通过。未删除旧测试、跳过失败测试、放宽安全断言或伪造未运行结果。

原成功完整日志位于机器证据列出的 `/tmp/rai-phase2-*.log`（SHA-256 已记录）；它们是本机证据，临时目录不保证永久保存。永久失败记录与 JSON 测试/摘要随工程交付。


## 6. Phase 1 回归

以真实隔离 PostgreSQL 重跑所有永久回归：普通旧快照不删除、显式子删除、未知删除屏障、两种 POST/DELETE 锁顺序、fencing、P4 Dirty/Job、publication 失效、旧 generation Worker superseded、删除写入和延迟 COMMIT 回滚、受限 API LOGIN 权限。Swift DELETE ACK 回归保留。

| RQ | 永久测试（G3/G5，真实 DB，无真机） | 结果 |
|---|---|---|
| RQ-001 | TestWorkoutBundleLateCompleteSnapshotRetainsChildren；TestWorkoutBundleConcurrentSnapshotsRetainUnion | PASS：旧/并发普通快照保留子事实并集，无 tombstone |
| RQ-001 | TestWorkoutBundleExplicitChildDeletionBoundary；TestWorkoutBundleSnapshotStillRejectsIdentityConflicts | PASS：仅显式删除建屏障，身份冲突及原回执重放不绕过 |
| RQ-002 | TestWorkoutDeleteBeforeUploadDurableStableAndIsolated；TestHTTPWorkoutDeleteBeforeUploadAndRetry | PASS：未知删除持久屏障、dataset 隔离、旧 POST 不复活 |
| RQ-002 | TestWorkoutDeleteFencingBeforeBarrierReplay；TestWorkoutDeleteAndImportBothLockOrders | PASS：两种可观测数据库锁顺序、epoch/collector fencing |
| RQ-003 | TestWorkoutDeleteInvalidatesPublishedP4AndKeepsHistory | PASS：旧范围、Dirty/Job、publication 立即失效和 Worker 清理/State 重发，历史保留 |
| RQ-003 | TestWorkoutDeleteSupersedesInFlightWorkers | PASS：Workout/Training State 两种旧 generation CAS |
| RQ-002/003 | TestWorkoutDeleteRollbackIncludingCommitFailure | PASS：barrier/dirty/job/change/commit/unknown_commit 六故障点整体回滚 |
| RQ-002/003 | TestP7RealLoginBoundariesAndRevocation | PASS：实际受限 API LOGIN 权限，未放宽角色 |
| 客户端 | APITests.workoutDeletionRequiresDurableACK（S，无 DB） | PASS：原严格 DELETE ACK；新迟到回调保护亦 PASS |

RQ-001 / RQ-002 / RQ-003 自动化验收仍通过；真实 HealthKit 删除、Sample/Route 显式删除捕获和真机 E2E 仍未完成。Phase 1 报告/source hash 与当前关键存储文件一致，未改其实现。


## 7. Git 状态与文件范围

以下为完整工作区（包括任务入口继承的修改），不是把全部 diff 归为 Phase 2。原暂存区保持空；没有任何原文件丢失。以入口 660 文件哈希核对，本阶段修改既有文件 14 个，新增 10 个。

**Phase 2 修改的既有文件（其入口部分已是 untracked）**

- `apps/server/internal/httpapi/server.go`
- `apps/server/internal/syncing/protocol.go`
- `contracts/CHANGELOG.md`
- `contracts/openapi-v2.yaml`
- `contracts/sync-v3.md`
- `docs/IMPLEMENTATION.md`
- `ios/RAI/App/BackgroundTransfers.swift`
- `ios/RAI/App/RAIApp.swift`
- `ios/RAI/App/SyncCoordinator.swift`
- `ios/RAI/AppTests/NormalizerTests.swift`
- `ios/RAI/Sources/SyncCore/OutboxStore.swift`
- `ios/RAI/Sources/SyncCore/RAIAPIClient.swift`
- `ios/RAI/Sources/SyncCore/WorkoutSync.swift`
- `ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift`

**Phase 2 新增文件**

- `apps/server/integration/workout_reliability_swift_test.go`
- `apps/server/integration/workout_reliability_test.go`
- `apps/server/internal/syncing/bundle_decode.go`
- `apps/server/internal/syncing/bundle_decode_test.go`
- `docs/audits/RQrun_PHASE2_RELIABILITY_REVIEW.md`
- `docs/evidence/rqrun-phase2-2026-11-01.json`
- `docs/evidence/rqrun-phase2-failed-runs.txt`
- `ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift`
- `ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift`
- `ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift`

```text
$ git status --short --branch
## main...origin/main
 M README.md
 M apps/server/cmd/rai/main.go
 M apps/server/integration/admission_test.go
 M apps/server/integration/p3_sync_test.go
 M apps/server/integration/p7_security_test.go
 M apps/server/integration/page_test.go
 M apps/server/integration/series_test.go
 M apps/server/integration/sync_test.go
 M apps/server/integration/upload_test.go
 M apps/server/integration/workout_package_test.go
 M apps/server/internal/admin/operations.go
 M apps/server/internal/httpapi/server.go
 M apps/server/internal/storage/postgres/store.go
 M apps/server/internal/syncing/protocol.go
 M apps/server/internal/syncing/store.go
 M apps/server/migrations/checksums.json
 M contracts/CHANGELOG.md
 M contracts/openapi-v2.yaml
 M deploy/postgres/roles.sql
 M docs/ARCHITECTURE.md
 M docs/IMPLEMENTATION.md
 M docs/P7_BACKUP_RESTORE.md
 M docs/P7_DEPLOYMENT.md
 M docs/P7_OPERATIONS.md
 M docs/P7_RELEASE_CHECKLIST.md
 M docs/RAI_MONTHLY_BACKUP.md
 M ios/RAI/App/BackgroundTransfers.swift
 M ios/RAI/App/HealthKitClient.swift
 M ios/RAI/App/ProductModel.swift
 M ios/RAI/App/ProductViews.swift
 M ios/RAI/App/RAIApp.swift
 M ios/RAI/App/SyncCoordinator.swift
 M ios/RAI/AppTests/NormalizerTests.swift
 M ios/RAI/README.md
 M ios/RAI/Sources/SyncCore/BatchBuilder.swift
 M ios/RAI/Sources/SyncCore/OutboxStore.swift
 M ios/RAI/Sources/SyncCore/RAIAPIClient.swift
 M ios/RAI/Sources/SyncCore/RouteBuilder.swift
 M ios/RAI/Sources/SyncCore/Wire.swift
 M ios/RAI/Tests/SyncCoreTests/APITests.swift
 M ios/RAI/Tools/SyncProbe/Probe.swift
 M ios/RAI/UITests/ProductUITests.swift
 M scripts/rai-backup.py
 M scripts/rai-restore.py
 M scripts/test-monthly-restore.sh
?? apps/server/integration/per_workout_sync_test.go
?? apps/server/integration/workout_bundle_test.go
?? apps/server/integration/workout_reliability_swift_test.go
?? apps/server/integration/workout_reliability_test.go
?? apps/server/integration/workout_safety_test.go
?? apps/server/internal/httpapi/bundle_admission_test.go
?? apps/server/internal/storage/postgres/workout_bundles.go
?? apps/server/internal/syncing/bundle_decode.go
?? apps/server/internal/syncing/bundle_decode_test.go
?? apps/server/internal/syncing/workout_bundle.go
?? apps/server/internal/syncing/workout_bundle_test.go
?? apps/server/internal/syncing/workout_delete.go
?? apps/server/migrations/00014_workout_imports.sql
?? apps/server/migrations/00015_workout_import_history.sql
?? contracts/sync-v3.md
?? "docs/RQrun-HealthKit-\344\270\212\344\274\240\345\215\217\350\256\256.md"
?? docs/RQrun_PHASE1_RESULT.md
?? docs/V2_PER_WORKOUT_SYNC_AUDIT.md
?? docs/V2_PER_WORKOUT_SYNC_RESULT.md
?? docs/audits/
?? docs/evidence/per-workout-sync-acceptance.json
?? docs/evidence/rqrun-phase1-2026-11-01.json
?? docs/evidence/rqrun-phase2-2026-11-01.json
?? docs/evidence/rqrun-phase2-failed-runs.txt
?? docs/evidence/workout-bundle-2026-10-31.json
?? ios/RAI/Sources/SyncCore/WorkoutBundle.swift
?? ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift
?? ios/RAI/Sources/SyncCore/WorkoutSync.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutBundleTests.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift
?? ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift
?? scripts/rai-reset-v2-data.sh
```

```text
$ git diff --stat
 README.md                                       |    4 +-
 apps/server/cmd/rai/main.go                     |    2 +-
 apps/server/integration/admission_test.go       |  168 ----
 apps/server/integration/p3_sync_test.go         |  210 ----
 apps/server/integration/p7_security_test.go     |   29 +-
 apps/server/integration/page_test.go            |   94 --
 apps/server/integration/series_test.go          |   80 --
 apps/server/integration/sync_test.go            |  166 ----
 apps/server/integration/upload_test.go          |  209 ----
 apps/server/integration/workout_package_test.go |   73 --
 apps/server/internal/admin/operations.go        |    2 +-
 apps/server/internal/httpapi/server.go          |  129 ++-
 apps/server/internal/storage/postgres/store.go  |    2 +-
 apps/server/internal/syncing/protocol.go        |    3 +
 apps/server/internal/syncing/store.go           |    3 +
 apps/server/migrations/checksums.json           |    4 +-
 contracts/CHANGELOG.md                          |   26 +
 contracts/openapi-v2.yaml                       | 1217 +++--------------------
 deploy/postgres/roles.sql                       |    4 +-
 docs/ARCHITECTURE.md                            |   10 +-
 docs/IMPLEMENTATION.md                          |  109 +-
 docs/P7_BACKUP_RESTORE.md                       |    2 +-
 docs/P7_DEPLOYMENT.md                           |    4 +-
 docs/P7_OPERATIONS.md                           |    4 +-
 docs/P7_RELEASE_CHECKLIST.md                    |    2 +-
 docs/RAI_MONTHLY_BACKUP.md                      |    6 +-
 ios/RAI/App/BackgroundTransfers.swift           |   19 +-
 ios/RAI/App/HealthKitClient.swift               |  138 ++-
 ios/RAI/App/ProductModel.swift                  |   16 +
 ios/RAI/App/ProductViews.swift                  |  284 +++++-
 ios/RAI/App/RAIApp.swift                        |  344 +++++--
 ios/RAI/App/SyncCoordinator.swift               |  487 +++++----
 ios/RAI/AppTests/NormalizerTests.swift          |  126 ++-
 ios/RAI/README.md                               |    2 +
 ios/RAI/Sources/SyncCore/BatchBuilder.swift     |    5 +-
 ios/RAI/Sources/SyncCore/OutboxStore.swift      |  308 +++++-
 ios/RAI/Sources/SyncCore/RAIAPIClient.swift     |   86 +-
 ios/RAI/Sources/SyncCore/RouteBuilder.swift     |    5 +-
 ios/RAI/Sources/SyncCore/Wire.swift             |    1 +
 ios/RAI/Tests/SyncCoreTests/APITests.swift      |   93 +-
 ios/RAI/Tools/SyncProbe/Probe.swift             |   50 +
 ios/RAI/UITests/ProductUITests.swift            |   19 +
 scripts/rai-backup.py                           |    4 +-
 scripts/rai-restore.py                          |    2 +-
 scripts/test-monthly-restore.sh                 |    2 +-
 45 files changed, 2029 insertions(+), 2524 deletions(-)
```

```text
$ git diff --cached --stat
(empty)
```

## 8. 剩余风险与范围

真实 iPhone 12 Pro Max 的 HealthKit、后台 URLSession、App 强杀/断网恢复和文件保护未验收；合成测试、空白模拟器、无签名 iPhoneOS 编译均不等于真机 E2E。物理断电/磁盘故障、数据库进程崩溃恢复未做本轮验收。静态设备 token 的实际轮换依赖用户现有配置，不存在可假装成功的服务器刷新 API。

保留 RQ-004（完整 anchored discovery）、RQ-007（metadata/provenance）、RQ-008（超时预算）、RQ-010（历史升级/reset）、RQ-011（coverage）、RQ-012（Route 完整性）、RQ-013（GPS 回跳）、RQ-014（gzip）、RQ-015（HTTP body 完整限制）、RQ-016（4 小时分析 SQL 性能）。Sample/Route 显式删除捕获及历史误删 tombstone 修复仍未完成；不回填真实数据、不重算全库。

## 9. 停止条件

验收证据与报告完成后停止。未执行 commit、push、部署、正式数据库连接或生产迁移；未修改 ../running；测试集群只使用本次新建的一次性 PG18 与合成数据，清理只针对本次测试资源。


机器证据：[rqrun-phase2-2026-11-01.json](../evidence/rqrun-phase2-2026-11-01.json)。所有必需自动化门禁已实际执行；最终结果为 **PASS WITH RISKS**，风险如第 8 节。停止实施，等待下一阶段指令。
