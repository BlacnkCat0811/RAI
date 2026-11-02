# M5A RESULT: PASS WITH RISKS

2026-11-02 / Asia/Shanghai。合成端到端闭环与本地验收通过；真实 iPhone + Apple Health 未验，不能标完整 PASS 或视为生产放行。M5A 到 Raw Facts、来源、删除与 coverage 为止；本轮到此停止。

正式协议：[DAILY_HEALTH_SYNC_CONTRACT.md](DAILY_HEALTH_SYNC_CONTRACT.md)。机器证据：[m5a-daily-health-2026-11-02.json](evidence/m5a-daily-health-2026-11-02.json)。原模型架构与原工程方案保留。

## 1. Git Baseline

- HEAD：`6e5451c974a6284cf4aab2e6baf856eee63ac218`；branch：`main`；结束时 HEAD/branch 未变。
- 入场已有 8 个 tracked dirty 文件及 4 个 untracked 路径：

```text
 M docs/IMPLEMENTATION.md
 M ios/RAI/App/ProductViews.swift
 M ios/RAI/App/RAIApp.swift
 M ios/RAI/App/SyncCoordinator.swift
 M ios/RAI/AppTests/NormalizerTests.swift
 M ios/RAI/Sources/SyncCore/RAIAPIClient.swift
 M ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift
 M ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift
?? deploy/cloud-test/
?? docs/RAI_MODEL_ARCHITECTURE.md
?? ios/RAI/Sources/SyncCore/ForegroundSyncScheduler.swift
?? ios/RAI/Tests/SyncCoreTests/ForegroundSyncSchedulerTests.swift
```

未 reset、stash、提交或推送这些改动。共享文件在原修改之上添加 M5A；没有修改 `../running`。

新增/修改范围：Go Daily DTO/严格解码、独立 HTTP endpoint、共享 sample 事务编排、coverage migration 20、sqlc model、runtime grants/Ready/schema 工具门禁；iOS daily normalizer、Outbox flow/delivery、HealthKit 独立查询与 observer、现有 App scheduler 接线；合成/PG/子进程测试、OpenAPI、正式合同及本报告。维护工具只更新当前 schema 版本识别/测试，不被线上同步导入，不成为同步依赖。

## 2. Architecture / M5A BASELINE

编码前实际只读审计结论：

| 问题 | 开工时真实能力 |
| --- | --- |
| 1. Anchored query 用在哪里 | HealthKitClient.read / readWorkout 使用 async HKAnchoredObjectQueryDescriptor；SyncCoordinator 正常发现为 Workout 与其关联 quantity/route |
| 2. Anchor 如何持久化 | HKQueryAnchor secure archive→Data；DiscoveryFlow.anchor / Outbox state.json 原子文件；既有 Workout discovery 在本地 journal 持久后推进 |
| 3. Workout delete 如何同步 | HKDeletedObject→discovery pendingDelete→DELETE workout-bundles；服务端已有永久 tombstone、子项屏障与事务回执 |
| 4. 非 Workout quantity 基础 | syncing.Units、Event、health_samples、generic stream/batch/receipt 已存在；旧 generic HTTP 写路由已关闭，正常 iOS 无独立 daily 发现 |
| 5. Category wire 是否支持 | Event.category 已支持 0–5；但不能据此宣称完整 daily iOS/source/coverage 闭环 |
| 6. Sleep category/value | 可保存数值和绝对区间；当前 SDK 真实枚举需专项核对，iOS 未启用独立读取 |
| 7. SourceRevision/device | Normalizer.source(HKObject) 与 HealthKitSource 已可复用；既有 observation 主要挂在 Workout，普通 Event.Source 只有 bundleID |
| 8. Dataset generation | 现 datasetEpoch、dataset 行锁 commit_rev 和 collector lease 适用于所有事实，可直接复用 |
| 9. Receipt 是否必须 Workout | 不必须；generic sync_batches/Receipt 本来独立于 Workout |
| 10. Generic observation | 可复用通用 raw sample 生命周期；BundleObservation 是 Workout observation，不冒用为 daily coverage |

Missing pieces：daily 类型发现、三套独立 committed anchor、ACK 后提交本地 anchor、不可变 pending page、bootstrap/hot 状态、source-aware daily DTO 与 received-coverage。Migration requirement：新增一个 coverage 表；不创建 HRV/RHR/Sleep 三套 fact 表。Protocol decision：独立 Daily Facts Sync v1，不叫 Protocol 4，不恢复旧上传路由。

实现链：独立 HK discovery → 既有 Outbox file state → bounded daily JSON → Go API → 既有 health_samples/tombstones/stream/batch/log/dirty → coverage 与 receipt 同事务。Raw source 完整写入保留键 `RAI:dailySource` 并参与 immutable identity hash。

## 3. Supported Facts

| 类型 | 本轮状态 |
| --- | --- |
| HRV SDNN / ms | discovery、normalization、batch、PG、delete、coverage 已实现并合成测试；不声称 RMSSD/RR/PPG |
| RestingHeartRate / count/min | 同上；不同 UUID 保留，delete+new 不按天 collapse |
| Sleep Analysis | 0=inBed、1=asleepUnspecified/asleep alias、2=awake、3=core、4=deep、5=REM；保存原始重叠 intervals/source/metadata |
| Apple VO₂max | 延至 M5A.1；未实现本轮 transport/origin_method，不算 implemented |
| HR Recovery | 延至 M5A.1；SDK identifier/count-min 已审查，HKQuantity/device 兼容性 NOT RUN，未启用 |

SDK 证据来自安装的 iPhoneOS 26.5 HealthKit headers，原始 key 还通过本机 SDK 打印核对。未知 category/metadata/series 不映射、不丢弃后继续传；阻止该类型页推进，等待受支持版本。第三方/manual/unknown 原始来源允许保留，模型资格未计算。

## 4. Sync Semantics

初始固定 90 天窗口（模型前 28 天 baseline 需求与修订余量），wire 最大初始 180 天；每类型固定 predicate/fingerprint。后续仅从各自已提交 anchor 增量读取，不每次重读历史。

最多 128 additions+deletions 合计，512 KiB；超 byte 限从同一旧 anchor 缩小 query limit，不截断。每前台机会每类型最多 4 页，续页继续排队。空页也有 durable coverage ACK；caught-up ACK 才标 bootstrapComplete。

顺序：discover → immutable body 与 candidate 落盘 → upload → PG durable commit → 验证 ACK 全身份/原字节 SHA/状态/计数 → 原子保存 ACK+anchor+sequence+pending removal。握手阶段失败同样持久退避，复用既有 recovery 转换，不因未进入 upload 就每秒重试。网络失败、timeout、500、错误 ACK、ACK 丢失不推进 anchor；重启复用原 batch/body。大整数 metadata 避免 Double 丢精度。

sample UUID 是 identity；同 UUID 改值/来源冲突，时间值相同但 UUID 不同保留。删除优先；未知 delete 先立屏障，同批 delete 胜 upsert，旧原 batch 只回原 ACK，新 batch 中旧 UUID 记 suppressed/deleted_precedence。RHR replacement 与 Sleep overlap 均保留真实生命周期。

App launch/foreground、“同步日常健康”手动按钮、observer hint、前台 15 分钟机会均可触发，不依赖 Workout 详情或上传。已追平类型在同代次下15分钟内不重复创建空页，pending/最久未确认类型优先消耗共享上传额度；新的 observer/前台/手动触发会持久增加 wake generation。查询期间的更新代次不会被旧页 ACK 清掉。Observer 先保存 wake hint，completion 不等待网络；后台是否唤醒由 iOS 决定。coverage 只描述服务器收到的 query/count/lastSyncAt/lastFactAt/source context，不推断读授权、覆盖百分比或生理 fresh/stale。future dirty 保存按 UUID 的绝对时间区间；没有 Recovery handler，ACK 明示 awaiting_model_implementation。

## 5. PostgreSQL Evidence

真实隔离 **PostgreSQL 18.6 (Postgres.app)**，无 TCP listener，fsync/full_page_writes/synchronous_commit/data_checksums 均 on。每次新建集群、每测试独立 DB；全部 leftover=0、cluster removed=1。无生产 URL、库或记录。

- Migration `00020_daily_health.sql`，19→20 expand-only 与重复 migrate 保留原事实/commit_rev；1–19 SQL checksum 未改。
- 新 HRV、重复样本/批次、同 UUID 内容冲突、删除、delete-before-create、旧重试、RHR replacement、六类 sleep overlap、空/未完成/存在/删除 coverage、generation/collector/upload fencing 均通过。
- 在事实、屏障、entity log、dirty、coverage、change log、receipt 表逐一注入写失败，事务不留下部分事实/回执；写事实后终止 backend session 同样回滚，重试成功。
- 真实 Swift 子进程 SIGKILL：before-upload / during-upload（服务端已提交但 ACK 尚未到达）/ after-commit-before-anchor，重启皆 1 fact / 1 receipt / anchor restored。最终客户端二进制重新执行三点kill及实际429回归也通过（4.995s）。
- 受限 `rai_api` 权限实际完成写入、删除和 coverage 读取。真实共享上传 429 测试证明三种类型最终各提交一次（3 facts / 3 receipts），Sleep 不饥饿，没有空批次 churn；另测同批 delete 胜重复 upsert，以及所有事实/coverage 写后最终 receipt UPDATE 失败回滚。HTTP 413、typed 422/409、内部异常 500/internal_error 与诊断脱敏通过。
- 全量 `go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 通过（integration 252.519s）；最终 M5A+迁移+进程/权限定向 race 再通过（integration 27.578s / admin 1.488s）。

## 6. iOS Tests

- `swift test --package-path ios/RAI`：Swift Testing 119 tests / 15 suites；普通 XCTest 2 pass，3 个既有 opt-in live/性能入口明确 skip。本轮 DailyProbe 的真实 API/PG 用独立 harness 已执行，不用 skip 冒充。包测试还覆盖查询冷却、pending优先、observer查询中代次变化与握手阶段离线退避。
- `swift build --package-path ios/RAI --product DailyProbe` 后，由 `-tags=integration,m5aclient -run TestDaily` harness 执行真实子进程 kill/replay。
- 全新一次性 iOS 26.5 模拟器，`xcodebuild ... CODE_SIGN_IDENTITY=- CODE_SIGNING_ALLOWED=YES -only-testing:RAITests test`：**34/34 PASS**，模拟器已删除。包括 SDK enum/unit/context、secure anchor archive/restore、三个 type 的离线 pending/重启/ACK/第二次 anchor query、metadata Int64 精度、查询冷却/wake generation 与既有 Workout/coordinator/session 回归。
- 首轮 unsigned 构建两项既有 Keychain 用例报 -34018；完整 ad hoc signed rerun 已通过，没有跳过或放宽断言。
- Anchor 保存中注入文件替换失败证明原状态可恢复；**未声称在 iOS 原子 rename 系统调用内部精确 SIGKILL**。受测进程 kill 是 macOS synthetic client，不能当 iPhone kill。

`generic/platform=iOS` 无签名原生构建也通过；它不是安装/真机验收。

`go test -race ./...`、`go vet ./...`、依赖边界（249 包）、sqlc v1.31.1 generate、OpenAPI lint、git diff --check 均通过。OpenAPI 保留 31 个既有 warning，无 error。

## 7. Real Device

```text
REAL DEVICE: NOT RUN
```

本轮没有访问真实 HealthKit、没有删除/写入用户健康记录，没有将个人值转成 fixture/log/artifact。实际设备 HRV/RHR/Sleep 可见性、代表性 metadata、第二次 anchor、RHR 修订、sleep 修订/删除、observer 后台唤醒仍需用户授权后的只读验收。

## 8. Performance

本地合成 sleep 32 intervals/day（最重首批类型），无真实 HealthKit 查询。以下非 race Store 轮直接调用事务入口（含 lease，不走 HTTP 请求速率预算），不代表整条限流上传耗时：

| History | Facts | Batches | Payload bytes | Parse ms | PG transaction ms | Bootstrap ms | 1-fact hot ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 7d | 224 | 2 | 85,393 | 8.46 | 41.76 | 62.68 | 2.80 |
| 30d | 960 | 8 | 365,735 | 34.50 | 145.41 | 225.86 | 1.93 |
| 90d | 2880 | 23 | 1,096,684 | 103.11 | 433.87 | 671.51 | 1.95 |
| 180d | 5760 | 45 | 2,192,808 | 206.07 | 913.52 | 1371.28 | 2.03 |

PG transaction 是 PhaseTimer 实测累计事务耗时，另有 request/lease/parse 开销；bootstrap 包含测试单独 parse 校验。测试进程累计 alloc：8.94/37.71/112.71/224.34 MB；采样 heap：30.86/2.65/4.47/6.37 MB，包含测试夹具和迁移的全进程状态，**不是生产 RSS/峰值或 iPhone 内存上限证据**。实现用 128/512KiB/每类型单 pending 的硬边界控制工作集。

原生 SDK 合成对象 normalization（最终模拟器轮）：7d=45.20ms / 30d=225.78ms / 90d=837.95ms / 180d=1492.94ms。这是构造/处理 fixture 的时间，不是 HealthKit datastore query 延迟。

1,024 条相同规模比较：32/64/128 分别 32/16/8 请求，253.48/215.89/203.04ms；最大单 PG 事务 11.79/10.74/20.57ms，最大 body 12,635/24,667/48,730 bytes。选 128；没有截断。hot 约 2ms 且随新变化而非历史长度工作。单机单轮结果不作真机、WAN 或生产 SLO。

另用真实 HTTP listener + 完整 auth/DB admission budget（30 requests/min、burst 2）重复全四种历史，遵守 Retry-After、重发原字节，150.034s 全部通过；未关闭或放宽限流：

| History | HTTP bootstrap s | Immediate 1-fact hot s | 429 retries including hot |
| --- | ---: | ---: | ---: |
| 7d | 0.06 | 2.00 | 1 |
| 30d | 12.21 | 2.00 | 7 |
| 90d | 42.60 | 2.00 | 22 |
| 180d | 86.23 | 2.00 | 44 |

此时耗时主要来自请求额度等待。hot 紧接 bootstrap、预算耗尽，因此约2s；不能把 Store 的约2ms称为生产端到端耗时。完整 iPhone HealthKit查询/后台机会/WAN仍未测。

## 9. Risks

- **P0：未发现。** 合成事务/身份/删除/ACK 闭环没有已知失败项；不构成生产放行。
- **P1：真实设备验收门禁未满足。** 真实来源 metadata、HealthKit 修订/删除可见性、后台 callback 与 iPhone kill/suspend 行为尚未验证，完整 PASS/上线前必须补齐。
- **P2：** unknown metadata/category 会有意阻塞该类型页，需合同/客户端升级；长期离线的 HealthKit deletion retention、读权限变化后历史补读政策未获设备证据。原子文件的物理断电耐久性、rename 内精确 OS kill、iOS 实际 peak memory 未验证。body-before-state 的已有模式可能留下 orphan 文件，自动 orphan GC 未扩展。
- **P3：** 本地性能与实际设备/WAN 差异；OpenAPI 31 项既有 warning。VO₂max/HR Recovery 延后，非本轮成功条件。

## 10. Not Verified

真 iPhone/Apple Health、用户授权变化/跨设备真实回填、DST/旅行的真实 HealthKit fixture、后台 delivery 系统调度、极长离线 deletion 保留、真实 datastore/设备内存/功耗/网络性能、精确文件 syscall kill/断电、fnOS/公网/TLS/生产部署或 migration 均未运行。只验证 Raw 保留绝对时间与 timezone context；没有验证或实现 local-day aggregation。

未改 Workout Protocol 3 wire semantics；已有 route/series/analysis/training Go/PG 与 iOS 回归通过。没有部署、push、真实数据迁移，也没有 Recovery、Readiness、HRV/Sleep score 或任何 M5B 算法。

## 11. Recommended Next Phase

先补 **只读真机 M5A 验收**，解除 P1 门禁后推荐 `M1 — Feature Foundation`。本轮不自动进入 M1 或 M5B，不把已有 raw facts 当作生理结论。
