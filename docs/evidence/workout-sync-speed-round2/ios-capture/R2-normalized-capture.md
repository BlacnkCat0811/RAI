# R2 规范化捕获（捕获侧）— 证据

日期：2026-11-06 · 机器：本机 Mac（模拟器 "RAI Local Tests"，iPhone-12-Pro-Max）
构建口径：**Debug**（模拟器 lane / SwiftPM 包测试）+ **优化构建 `-O`**（R2-P01 门禁测量，见文末专节）
数据：全部合成（合成 HKQuantitySample / 合成 sourceRevision / 合成 HKDevice 描述符 / 合成 metadata），
无真机、无真实健康值、无真实设备标识。

## 命令

```
# 包测试（SyncCore 层）
scripts/local/test.sh swift --scratch-path .local/swift-build/capture

# 定向
scripts/local/test.sh swift --scratch-path .local/swift-build/capture --filter NormalizedCaptureSegmentTests

# App 层（@testable import RAI，含真实 performSync + uploadAndAcknowledge）
scripts/local/test.sh simulator
```

## 核心验收：逐样本归档归零

计数器 `SampleArchiveMeter`（`ios/RAI/Sources/SyncCore/NormalizedWorkoutCapture.swift`）是纯测量，
不是开关：它只在生产代码真的调用归档/反归档的位置 +1。`performSync` 把 delta 写进 timings：
`sample_archive_calls`、`sample_unarchive_calls`、`object_archive_calls`。

| 断言 | 测试 | 实测 | 结果 |
| --- | --- | --- | --- |
| 正常路径 `sample_archive_calls == 0` | `NormalizedCaptureTests.testNormalizedCaptureNeverArchivesASample` | 0 | PASS |
| 正常路径 `sample_unarchive_calls == 0` | 同上 | 0 | PASS |
| meter delta archive/unarchive 均为 0 | 同上 | 0 / 0 | PASS |
| 规范化 segment 真的写了 | 同上 | `normalized_segment_writes >= 1` | PASS |
| 旧 archive 段仍需反归档（且只 1 次） | `testLegacyArchiveSegmentStillFeedsTheBundle` | `sample_unarchive_calls == 1`、`sample_archive_calls == 0` | PASS |
| 组包不读 HealthKit | `testBundleIsAssembledFromNormalizedSegments` | `reused_discovery_reads == 10`、`full_sample_reads == 0` | PASS |

保留并单独计数的归档对象（明确允许）：根 Workout、HKQueryAnchor、删除墓碑、旧格式兼容对象，
以及 completeness/VO2 两个**非本次范围**的整页归档路径（各 +1 `object_archive_calls`）。

## 对拍：规范化事实与旧路径语义等价

`NormalizedCaptureTests.testNormalizedFactsMatchTheArchivedSemantics`：
同输入下，bundle 里的 `samples` 数组与旧路径的 `Normalizer.integrityEvent(sample)`
**逐字节 `.data()` 相等**（不是"字段看起来一样"），并逐字段核对 uuid/startUs/endUs/quantity.value/
quantity.unit/source.bundleID；另有独立 spot check（`count/min` 120、`m` 1000）。
同测试还断言 `NormalizedFact.identityHash == Normalizer.discoveryFingerprint(sample)`，
即 discovery 身份（去重键）与旧路径一致。

存储层对拍 `NormalizedCaptureSegmentTests`（7 个测试全过）：

| 测试 | 断言 |
| --- | --- |
| `normalizedFactRoundTripsThroughASegment` | 写入→读回 canonical bytes 相同，字段/identityHash/series/metadata 相同 |
| `canonicalBytesAreStable` | 两次 encode 相同；decode→encode 不变；120.5 往返精确相等 |
| `tamperedSegmentIsRejected` | 改写段内一个载荷字节后读取抛错（digest 绑定不可变文件） |
| `archivedRecordStillReadsAsArchive` | 旧 `.archive` 记录 → `discoveryNormalizedFact == nil`，`capturedArchive` 原字节返回 |
| `deletedFactRequiresATombstone` | 删除事实写入抛 `deleted_fact_requires_tombstone` |
| `segmentPolicyAndFormatAreAsRequired` | 512 条 / 262144 字节 / 0.25s；seal format == 2；readableFormats 含 1 与 2 |
| `oldSealFormatStaysReadable` | 读取器接受 format 1 与 2，旧段仍可校验 |

一个真实的口径细节（记录在此避免误判）：`JSON` 把 `120.0` 与 `120` 写成同样的字节 `"120"`，
所以 `.number(120.0)` 经 canonical JSON 往返会解码成 `.int(120)`。**对拍口径是 canonical bytes**，
不是 Swift `Equatable`（bundle 字节逐字节相等已由上面的 App 测试证明）。

## 旧数据兼容

| 对象 | 处理 | 证据 |
| --- | --- | --- |
| 旧 capture segment（seal format 1，载荷为 archive） | 仍可校验、仍可读，`discoveryNormalizedFact` 返回 nil → 走旧反归档路径 | `archivedRecordStillReadsAsArchive`、`oldSealFormatStaysReadable`、`testLegacyArchiveSegmentStillFeedsTheBundle` |
| 旧 pending / state.json | 新字段全部 Optional（`CaptureManifest?`、`ordinal?`、`captureManifest?`），解码不需要迁移 | 既有 `GarbageCollectionTests` / `RecoveryBoundaryTests` / `SyncSessionTests` 全过；`WorkoutSyncReuseTests` 的"未变更重复同步 skip"仍成立 |
| 旧 pending 的 JSON/hash/回执 | 未改动 Wire、bundle 组装字段、回执校验（`Wire.swift`/`WorkoutBundleDelivery.swift` 本轮未改） | `WireEncodingTests`（队友文件）与 App 层回执校验测试全过 |

## 性能：规范化 + 持久化阶段

`NormalizedCapturePerformanceTests`（App 层，Debug，合成 fixture，2000 个 heart rate 样本，
含 sourceRevision + HKDevice + 3 个 allowlist metadata，重复 3 次取最小值）。

### 每样本 CPU 工作量（不含磁盘，稳定可比，已断言）

| 路径 | 每样本 | 构成 | 最小值（2000 样本） |
| --- | --- | --- | --- |
| 旧路径 | **0.517 ms** | `NSKeyedArchiver` + `Wire.hash(archive)` + `discoveryFingerprint` | 1033.9 ms |
| 新路径 | **0.383 ms** | `Normalizer.normalizedFact` + `NormalizedFact.encoded()` | 767.0 ms |

比值 **1.35x**（每样本 CPU 降约 26%）。断言：`testPerSampleNormalizationCostDrops` PASS。

顺带做的一处真实优化：`Normalizer.normalizedFact` 原先构造 `integrityEvent`/`source` 两次
（一次给事实、一次在 `discoveryFingerprint` 内部），现在复用同一对 JSON 计算身份 hash，
`discoveryFingerprint(event:source:)` 保证 hash 与旧路径逐字节一致（有断言）。

### 端到端捕获阶段（含 segment 持久化，**只报告不断言**）

| 路径 | 最小值 | P50 | 三次运行 |
| --- | --- | --- | --- |
| 旧路径 | 2283 ms | 3255 ms | 6029 / 3255 / 2283 |
| 新路径 | 1486 ms | 2990 ms | 4943 / 2990 / 1486 |

**如实说明**：模拟器上该数字被文件系统停顿主导，同一份代码三次运行相差 2.6 倍，
因此它**不能**用来声称提速。可以声称的是上面的每样本 CPU 降低。
第一轮真机记录的 8.8s/样本归档无法在本会话复现或归因（**NOT RUN**，无真机授权）；
本机合成样本的 `NSKeyedArchiver` 本身只要约 0.34 ms/样本（组件分解实测），
说明真机那 8.8s 里占大头的很可能不是 archiver 本身，而是每样本 JSON 构造 + hash + 落盘，
这部分 R2 已按上面的比例降低，但**真机口径仍需 lead 侧真机测量确认**。

### 组件分解（单次运行，指示性）

`R2-PERF component=...`：`nssecurearchive_only` 673ms、`archive_bytes_hash` 337ms、
`discovery_fingerprint` 710ms、`normalized_fact_build` 655ms、`normalized_fact_encode` 282ms、
`normalized_capture_and_persist`（含落盘）3469ms（噪声大）。
字节量：archive 3.79 MB → normalized 2.16 MB（2000 样本，−43%）。


## R2-P01 门禁：优化构建下的「规范化 + 持久化」P50（**FAIL 27.09%**）

lead 要求：不能用 Debug 单样本比值交差，必须用优化构建、同进程同 fixture 对比
「旧路径（逐样本 `NSKeyedArchiver` + 写 segment）」vs「新路径（`NormalizedFact` 直写 segment）」，
N 次取 P50，**≥70% 才 PASS**。

### 构建口径（可复现）

App 测试目标无法直接 `-configuration Release`：`NormalizerTests.swift` 等用了只在 `#if DEBUG`
下存在的测试钩子（`WorkoutInspection`、`HealthKitClient.setWorkoutPageSource`、`workoutListEvidence`、
`VO2MaxNormalizer.blockedNativeShape`），Release 下报
`unable to resolve Swift module dependency to a compatible module: 'RAI'` / `cannot find 'WorkoutInspection' in scope`。
因此采用 **`-O` 优化 + 保留 DEBUG 条件（仅为测试钩子）**：

```
export DEVELOPER_DIR=$HOME/Downloads/Xcode.app/Contents/Developer
xcodebuild test-without-building -project ios/RAI/RAI.xcodeproj -scheme RAI \
  -destination "platform=iOS Simulator,id=5DA14A13-5EFA-4E08-90F4-4B90FAD0A6B9" \
  -derivedDataPath .local/testing/simulator-derived-release \
  -configuration Release ENABLE_TESTABILITY=YES \
  SWIFT_ACTIVE_COMPILATION_CONDITIONS="DEBUG RAI_PERF_RELEASE" \
  -only-testing:RAITests/NormalizedCapturePerformanceTests -parallel-testing-enabled NO
```

perf 断言用 `#if RAI_PERF_RELEASE` 打标签（不是 `#if !DEBUG`），这样优化构建里也会真的断言。

### 结果（N=2000 合成 heart rate 样本，同进程，7 次取 P50）

```
R2-P01 samples=2000 repetitions=7 profile=Release(-O) + DEBUG conditions (test hooks)
R2-P01 legacy_p50_ms=1530.05 normalized_p50_ms=1115.62 drop_percent=27.09
runs_legacy=[2059,3626,2374,1329,1370,1528,1530]
runs_normalized=[1856,1719,973,1009,1032,1116,1289]
R2-P01 legacy_sample_archive_calls=14000 normalized_meter_archive_calls=0
```

`XCTAssertGreaterThanOrEqual(drop, 70)` → **失败**。门禁结论：**R2-P01 FAIL（实测 27.09%）**。
唯一达标的子项是"逐样本归档调用归零"（14000 → 0）。

### 为什么达不到 70%（组件实测，不是猜测）

同构建下的 `testCaptureComponentCostBreakdown`（ms/样本）：

```
archive=0.2657  fingerprint=0.2208  normalized_fact=0.2215  fact_encode=0.0235  archive_bytes_total=3794000
```

- 旧路径每样本 ≈ `archive`(0.266) + `fingerprint`(0.221) = **0.49 ms**
- 新路径每样本 ≈ `normalized_fact`(0.221) + `encode`(0.024) = **0.24 ms**
- 即使把归档成本**全部**消掉，本阶段上限也只是 0.765 → 0.499 ms，即 **~35%**：
  因为 `identityHash`/JSON 序列化两条路径都必须做，且它与归档同量级。

因此 70% 在"规范化 + 持久化"这一段对本 fixture **不可达**，不是实现没做到位：
把归档归零这件事本身已经完成（14000 → 0），剩下的成本是两条路径共有的身份 hash + JSON 编码。
第一轮真机的 8.8 s/15k ≈ 0.59 ms/样本比本机合成样本更贵，真机口径能否达到 70% 需要真机测量 → **NOT RUN**。

已知还能继续压缩、但本轮**未做**的点（会改格式或 hash，需单独评估）：
1. `Normalizer.discoveryFingerprint(event:source:)` 仍会再序列化一次 event 生成 wrapper JSON，可用预编码片段拼接（必须逐字节等价，否则 identityHash/事件 id 会变）。
2. `NormalizedFact` 的 `quantity`/`metadata`/`source` 与 `event` 字段重复，去掉可省一次序列化，但会改 v1 canonical 格式。


### R2-P01 口径与差距归因（lead 决定：不改口径，记 FAIL）

两个口径的成本结构**不同**，必须并列看，不能互相替代：

| 口径 | 该阶段总量 | 归档占比 | 去掉归档后的理论上限 |
| --- | --- | --- | --- |
| 第一轮真机（S60，约 15k 样本） | 12.78 s | `NSKeyedArchiver` 8.8 s ≈ **69%** | 约 31% 残留 → 70% 有希望 |
| 本机合成 fixture（2000 样本，优化构建） | 1.53 s | archive 0.266 ms/样本 ≈ **35%**（fingerprint 0.221 + normalized_fact 0.221 + encode 0.024） | **~35%** → 70% 不可达 |

也就是说：**真机上归档是绝对大头（69%），本机合成 fixture 上不是**。原因是本机 `NSKeyedArchiver` 对
纯合成 `HKQuantitySample`（1 个 quantity、3 个 metadata、合成 sourceRevision/device）只要 0.266 ms/样本，
而真机样本的归档贵得多（第一轮 8.8 s / 15k ≈ 0.59 ms/样本，且真机还叠加了真实 metadata 规模与主线程竞争）。
本机 fixture 里两条路径都必须做的 `identityHash`（0.221 ms/样本）因此显得和归档同量级，
把降幅上限压到 ~35%。

结论与边界：
- 本机口径实测 **27.09%**，门禁 **FAIL**，不移动门禁、不换口径达标。
- 「真机同口径复测」= **NOT RUN**（本会话无真机授权）。真机上能否达到 ≥70% 目前**未验证**，
  只能说真机的成本结构（归档 69%）比本机 fixture 更有利于这条门禁。
- 归档调用归零这个子目标是硬事实：**14000 → 0**（`legacy_sample_archive_calls` / `normalized_meter_archive_calls`）。

后续可压缩方向（本轮**未做**，因为会改 hash 或 canonical 格式，需要单独评估）：
1. `Normalizer.discoveryFingerprint(event:source:)` 目前会再序列化一次 event 生成 wrapper JSON；
   可用预编码片段拼接，省一次序列化 —— 但必须逐字节等价，否则 identityHash / 事件 id 会变。
2. `NormalizedFact` 的 `quantity`/`metadata`/`source` 与 `event` 字段重复，去掉可省一次序列化 ——
   但会改 v1 canonical 格式与现有对拍断言（需要 v2 版本号与迁移）。

## 未完成 / 未验证

- **真机 / 真 HealthKit**：NOT RUN（本会话无真机授权）。
- **RSS**：未测量。模拟器不提供可信的进程 RSS 归因，且本轮没有真机；因此不给数字。
- **端到端提速**：仅每样本 CPU 有可断言结论；端到端被模拟器磁盘噪声主导，未声称提速。
- completeness / VO2 两条整页归档路径仍是旧式整页 `NSKeyedArchiver`（R2 范围外，已单独计数）。

## 改动文件

- 新增 `ios/RAI/Sources/SyncCore/NormalizedWorkoutCapture.swift`（`NormalizedFact`、`NormalizedSeries`、`SampleArchiveMeter`）
- 改 `ios/RAI/Sources/SyncCore/CaptureSegments.swift`（payload 编码标记、seal format 2、规范化读写 API）
- 改 `ios/RAI/Sources/SyncCore/OutboxStore.swift`（`captureNormalizedFacts`、`discoveryNormalizedFact`）
- 改 `ios/RAI/App/HealthKitClient.swift`（`Normalizer.normalizedFact`/`capturedFact`/`series(of:)`/`discoveryFingerprint(event:source:)`）
- 改 `ios/RAI/App/SyncCoordinator.swift`（`factsForBundle`/`freshFacts`，去掉逐样本归档；计数器）
- 新增 `ios/RAI/Tests/SyncCoreTests/NormalizedCaptureSegmentTests.swift`
- 新增 `ios/RAI/AppTests/NormalizedCaptureTests.swift`
- 新增 `ios/RAI/AppTests/NormalizedCapturePerformanceTests.swift`
- 改 `ios/RAI/RAI.xcodeproj/project.pbxproj`（注册两个新 App 测试文件）
