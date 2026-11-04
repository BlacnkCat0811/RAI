# M4-B1 — Apple Health VO₂max External Data Contract

日期：2026-11-04，Asia/Shanghai。合同版本：`rai.healthkit.vo2max.contract/1`。**DESIGN FROZEN / NOT IMPLEMENTED / CANDIDATE ONLY**。Algorithm Decision **B**：优先接入 Apple Health 外部估计；RAI 自有 VO₂max 数值模型继续暂缓。

本合同只约束后续 B2/B3/B4；本文中的新端点、DTO、数据库约束、命令和 Worker 均为设计，不能作为已有功能或测试通过的声明。本轮只新增本文与 [实施计划](M4B_IMPLEMENTATION_PLAN.md)，不修改既有冻结合同、`IMPLEMENTATION.md`、代码、SQL、OpenAPI 或 migration。

## 1. 基线与复核证据

实际 Git baseline：`main`，HEAD `46968332380f9145945bc989325b98bc40454d9a`；开始时 `git status --short`、`git diff --stat` 均为空。最高 migration 为 **00023_performance_v1.sql**，不是从 M4-A 的 HEAD 推定。849 个原有 tracked/nonignored 文件在编辑前记录 SHA-256，交付时逐项比较。

M4-A 正式依据为 [VO₂max模型架构与可行性审查原文](/Users/ta/.codex/m4a-review-2mxxz5ia/M4A_VO2MAX_MODEL_FEASIBILITY_REVIEW.md)，结论 `READY WITH RISKS`，批准决策为 B。原报告位于仓库外审查目录；本次补充的全文附件与该原文件字节一致，SHA-256均为`290c966caf325a6be61f091827dd28c51f437cd02c8c76c28cf0f868bf2ec120`。B1初稿将未找到报告写成“全文缺失”，现予更正并移除该风险；M4-A原文和原证据均不修改。

| 复核对象 / 当前证据 | 本轮结论及边界 |
| --- | --- |
| [M1](WORKOUT_FEATURE_CONTRACT.md)、`internal/workoutfeatures/{model,compute,exposure}.go` | WorkoutFeatures/qualification 与未来 VO₂ placeholder 存在；不计算 VO₂。外部投影不消费 steady segments 或 Feature artifact。 |
| [M2](TRAINING_LOAD_V1_CONTRACT.md)、`internal/trainingload`、`workoutfeatures/exposure.go` | HR Exposure 是独立 companion；HRR dose 有 HR-reference 输入。外部投影不读取它或其参数。 |
| [M3](RUNNING_ABILITY_V1_CONTRACT.md)、`internal/performance` | Performance 只消费 M1；不能接上 VO₂ 外部事实作为新依赖。 |
| [Daily v1](DAILY_HEALTH_SYNC_CONTRACT.md)、[M5A-RD](M5A_RD_RESULT.md) | 冻结三类 HRV/RHR/Sleep，VO₂ 尚未接入。M5A-RD 的三类真机证据不能转用为 VO₂ 真机证据。 |
| `ios/RAI/App/HealthKitClient.swift` 的 `dailyTypes/readDaily/Normalizer.source` | 只读三类 Daily；通用单位表已有 VO₂，但不是发现/上传实现。`HKDevice.name` 未保存。 |
| `ios/RAI/App/DailyHealthNormalizer.swift` | 三类 allowlist、未知 metadata 拒绝；不是通用 typed metadata 容器。 |
| `ios/RAI/Sources/SyncCore/{DailyHealth,OutboxStore}.swift` | 三流各有固定 90 日窗口、anchor、pending body 和 ACK 原子提交。ACK 目前严格要求 version=1 与 awaiting_model_implementation；不复用成新协议解析器。 |
| `internal/syncing/{daily,bundle_observation}.go` | DailyVersion=1、仅三类型；现有 HealthKitSource 不含 Device.name，不是 HKDevice 无损档案；现有分类不是本合同的四类结果。 |
| `internal/storage/postgres/{daily,store,sample_batch}.go`；migration 00001/00020 | 同事务 facts/barriers/change logs/dirty/coverage/receipt；coverage 仅查询三类；未知删除无时间，现有 Daily interval dirty 不足以驱动 VO₂。 |
| migration 00021–00023；`analysis_{input,publish}.go`、`performance_publish.go`、`jobs/worker.go` | artifact allowlist、依赖 scope 约束和 job dispatch 均不支持外部 VO₂；现有 feature→vo2 placeholder 不能借用为 external scope。 |
| `deploy/postgres/roles.sql`、`runtime_role.go` | API 已能读 runs/results/publications，但不能读写 artifacts/inputs；Worker 当前没有 daily_health_coverage SELECT。不可将这些权限误报为已足够。 |

表中 Go 路径相对 `apps/server/`。已复核M4-A全文第1–20节及其Git/SDK/检查摘要：M4-A与B1/current HEAD相同，均为`46968332380f9145945bc989325b98bc40454d9a`，最高migration=23。将B1原有849文件逐项SHA-256与M4-A离场manifest对应条目比较，**849/849相同**，未发现两次审查之间的源码或既有合同变化。M4-A manifest另含.git/ignored文件和symlink，本次849文件核对不冒称重新验证其全部55,065条目。旧Daily合同内历史`REAL DEVICE: NOT RUN`已有M5A-RD补充，但VO₂仍未真机验收。

SDK 本轮实际检查：`/Users/ta/Downloads/Xcode.app`，Xcode **26.6 (17F113)**，iPhoneOS **26.5** SDK；项目部署目标 **iOS 18.0**。系统默认仍为 CommandLineTools，未执行全局切换。审查 Header：`HKTypeIdentifiers.h`、`HKMetadataEnums.h`、`HKMetadata.h`、`HKObject.h`、`HKDevice.h`、`HKSourceRevision.h`。另仅用 macOS HealthKit 常量打印核对 wire key 字符串，没有查询健康库；这不是 iOS 编译或设备验收。

补核M4-A [SDK结果](/Users/ta/.codex/m4a-review-2mxxz5ia/sdk-check.json)与[probe](/Users/ta/.codex/m4a-review-2mxxz5ia/sdk-probe.swift)：当时使用iPhoneSimulator26.5 SDK、iOS18 target做swiftc typecheck，未运行Simulator App或HealthKit查询。其Simulator SDK与B1 iPhoneOS SDK的HKTypeIdentifiers/HKMetadataEnums/HKMetadata/HKDevice/HKSourceRevision五个Header本次逐字节比较一致。M4-A的503个test/subtest通过、4 skip及race/vet/boundary仅作该阶段历史证据，不改写为B1新VO₂测试或真机验收。

### 1.1 M4-A建议与B1冻结决策对照

M4-A第14/16节明确标注为建议，B1负责将其具体化。以下为设计选择差异，不是源码变化，也不改写M4-A原文：

| 项目 | M4-A建议 | B1冻结 / 本次核对 |
| --- | --- | --- |
| 协议 | 独立版本化Daily扩展，例如dailyFactsVersion=2 | 评估后选择独立vo2max-facts/v1 endpoint/stream；保留旧Daily v1，详见§3。 |
| scope | model/<current-candidate-stack>/athlete/vo2_external | external/vo2max/v1/athlete/current；仍复用candidate extension与共享结果表，减少M1 stack身份耦合，详见§10。 |
| migration | 建议一个00024_vo2_external_projection.sql，实施时重核编号 | 按B2 raw/B3 projection分为00024/00025两个最小提案，均未创建，详见§10。 |
| 选择与冲突 | 显式source epoch、最新事件、无UUID破平/跨源平均 | 采纳；B1进一步固定显式选择input及同endUs完整tie判定，不用startUs掩盖同时间不同值。 |
| 有界输入 | 建议1024 sample refs、32 epochs、rolling90d current域 | 采用1024 refs/32 epochs上限，另收紧最新tie≤256；≥90d绝不返回current值。只允许额外有界最新历史定位说明expired/withdrawal，禁止lifetime扫描或用过期前驱补current，详见§8。 |
| quality与版本字段 | 有值DB quality=partial；modelVersion为完整模型身份 | 本次按原报告对齐：available/stale均映射partial，modelVersion=rai.vo2max.external_projection/v1；DB algorithm_version仍v1。 |
| 时间/新鲜度 | revised current，历史run审查；30/90日为产品策略 | 采纳并冻结边界/configHash；显式view/input schema/knownThroughRevision，不能伪装历史time travel。 |
| 未验证项 | 真机/历史update/生理有效性延期，自有数值模型不批准 | 全部保留；未因报告补齐而升级为真机或生理PASS。 |

## 2. 三层数据身份

| 身份 | 语义 | 本阶段 / 后续边界 |
| --- | --- | --- |
| `apple_health_vo2max` | HealthKit `HKQuantityTypeIdentifierVO2Max` 的外部原始事实族；可能来自 Apple、第三方、手工或未分类来源 | 名称描述接入平台，不承诺所有记录来自 Apple Watch，也不承诺每条均为估计。 |
| `rai.vo2max.external_projection/v1` | 对外部事实做来源资格、选择、新鲜度和状态投影 | 只复制被选原始数值，不产生新的 VO₂max；未来 B3 仅 candidate。 |
| `rai.vo2max.estimated/v1` | RAI 自有预测模型 | **NOT IMPLEMENTED**；无 handler、作业、成功结果或占位数值。 |
| `laboratory_measured_vo2max` | 未来有独立测量来源和验证链的实验室事实 | 当前无来源、无协议入口、无结果；testType=1 不能建立此身份。 |

HealthKit 类型、Apple 估计与实验室测量是不同概念。[Apple VO₂max 类型说明](https://developer.apple.com/documentation/healthkit/hkquantitytypeidentifier/vo2max)允许系统估计和其他应用写入测试结果，不能由类型判断来源。结果和未来 UI 必须注明“Apple Health 外部估计 / 来源分类”，不能称“RAI 算出的 VO₂max”。不与 RAI 或实验室结果平均、合并或互作训练标签。

## 3. 传输选择与兼容

冻结选择：**独立 VO₂max stream，协议 `vo2max-facts/v1`**；不启动 Daily Facts v2。评估 Daily v2 后，考虑本阶段只有一类新事实、metadata/provenance 与 v1 差异大，独立流能最小化兼容面。未来如有 Daily v2，需要另行版本合同，不能回头改写 v1。

拟新增 `POST /v2/sync/vo2max-facts` 与 `GET /v2/sync/vo2max-facts/coverage`。`/v2` 是既有 API namespace，新载荷自己的 `version=1`，不等于 Daily v1。握手另加 `vo2maxFactsVersion=1`、`vo2maxMetadataRegistryVersion=1`、`vo2maxMaxChanges=128`、`vo2maxMaxBytes=524288`；字段不存在即不支持。旧客户端照常使用 Daily v1；新客户端面对旧服务器不得把 VO₂ 降级塞进 Daily/Workout，保留本地待处理状态并提示兼容版本缺失。

既有 Daily endpoint、capabilities、三类数组、fingerprint、receipt、状态及 90 日窗口全部不变。不能增加 DailyHealth.types 的第四元素、复用 DailyFlow 的 version=1 body/ACK，或借 VO₂ 升级重置 HRV/RHR/Sleep anchor。Workout protocolVersion=3 也不变。

### 3.1 Discovery 身份与固定窗口

- 只查询 `HKQuantityTypeIdentifier.vo2Max`，独立 observer/wake hint、独立 secure HKQueryAnchor archive；不查询 lowCardioFitnessEvent，不依赖 Workout 选择或关联。
- 首次启用时持久保存 `flowCreatedAtUs`，`bootstrapStartUs=flowCreatedAtUs−90×86400×10^6`。这是固定经过秒窗口，不随日期、时区、版本或重启移动。两字段均在首次 query 前写入；新流首批必须符合等式，后续必须原样复用。
- predicate：`predicateForSamples(withStart: fixedStart, end: nil, options: [])`，保留跨下界 interval；无滚动下界、全历史扫描或全库摘要。B2 必须用 SDK synthetic 查询验证边界；真实修订是否全部落在 predicate 可见范围仍 DEFERRED。
- fingerprint 精确定义为 `sha256:` + UTF-8 字符串 `vo2max-facts/v1|HKQuantityTypeIdentifierVO2Max|<bootstrapStartUs十进制>|overlap-v1` 的小写 SHA-256。它描述 query，不混入来源选择或 freshness。不能与 `daily-v1|...` 共用。
- stream 唯一约束沿用 `(dataset, collectorEpoch, sampleType, queryFingerprint)`，新 streamID 为随机 UUID。相同 query 不悄悄轮换 stream。anchor 解码失败 fail-closed，禁止自动 nil-anchor bootstrap；丢失状态走明确恢复流程，不属于热同步。
- 所有有权限可见来源都发现，不用 source filter 改 query；第三方、手动与未知分类事实原样保留。授权只新增此类型读取，`toShare=[]`；不增加 HealthKit 写入或 WorkoutKit 排程授权。

### 3.2 Batch、ACK 与错误

新 batch 必须包含以下字段，不复用 Daily DTO；未知 JSON 字段、重复键、缺失必填项及多余 union 分支均拒绝：

```text
contract = "vo2max-facts/v1"; version = 1; metadataRegistryVersion = 1
datasetID, datasetEpoch, collectorEpoch
streamID, batchID, clientSeq, previousBatchID (seq=1 时明确 null)
sampleType = "HKQuantityTypeIdentifierVO2Max"
flowCreatedAtUs, bootstrapStartUs, queryFingerprint, queryCompletedAtUs, hasMore
facts[], deletedUUIDs[]（空也必须是数组）
```

身份为 dataset/epoch/collector/stream/sequence/previous-batch 链加 batchID；batchID 在 dataset 内唯一，body SHA-256 绑定**原请求字节**。同 batch/相同 bytes 返回原 immutable receipt；同 batch/不同 bytes 冲突；同 sequence 换 batch 不得绕过。首次 stream、批次和 receipt 同事务建立。

时间为有符号 UTC epoch **Int64 微秒**，JSON decoder 必须直达 Int64/RawMessage，禁止 float64 中转；范围 year 1…9999。`queryCompletedAtUs≥flowCreatedAtUs`。每 batch additions+deletions≤128，未压缩 JSON≤512 KiB，原始事实事务 context≤5s，继续遵守现有 DB lock/statement timeout。超限从原 committed anchor 减小 query limit 重查；不裁掉页尾或单条 metadata。单条仍过大则阻塞。

上述时间编码也适用于新coverage/result中未带Us后缀的`bootstrapStart/queryCompletedAt/lastSyncAt/lastFactAt/calculatedAt/asOf/evaluationAt`，均为UTC Int64微秒；无证据时明确null。不沿用旧Daily time.Time的JSON文本编码去隐式改变新DTO。计数/sequence/generation为精确整数，datasetGeneration仍为UUID。

状态机：

1. 从 committed anchor 读完整有界页；校验每条事实、metadata 与 deletion，再形成 candidate anchor。
2. immutable body 先落盘；pending 引用、candidate archive、hash、sequence 和 wake generation 在既有 OutboxStore 原子状态中发布。每流最多一页 pending；不能在 ACK 前继续下一页。
3. 每次投递相同 bytes；网络/429/5xx 使用持久 backoff 与 Retry-After。observer 只持久化 wake hint，不等网络 ACK 才返回 completion。
4. 服务器同事务提交 §7 的全部对象，返回 receipt。事实 ACK 不等分析完成；B2 的 `analysisStatus=awaiting_model_implementation`。B3 可在新协议 allowlist 内使用 `queued` 或 `not_enrolled`，均不表示分析成功；Daily v1 receipt 不变。
5. 客户端验证 contract/version/registry、全部身份、exact-body SHA、`ingestStatus=committed`、正 commitRevision、各非负 count、deleteStatus 与已支持的 analysisStatus；任何不匹配不提交 anchor。
6. ACK、candidate→committed anchor、sequence+1、previousBatchID、bootstrapComplete 与 pending removal 共同原子保存；之后才回收 body/传输缓存。

原 receipt 的 `analysisStatus` 不随 Worker 后续运行改写。lost ACK、进程在 server commit 后终止、ACK 持久化失败均重投原 pending，不生成“等价新批次”。dataset/collector/upload lease fencing 优先于 receipt 获取；失效会话不能借旧 receipt 绕过授权。

| 错误族 | 规定动作 |
| --- | --- |
| 422 unsupported_version / unsupported_metadata / unsupported_metadata_type / unsupported_test_type / source_invalid / unsupported_unit / invalid_payload | 整批不提交，不 ACK，不推进 anchor；本地兼容阻塞。 |
| 409 batch_identity_conflict / sample_identity_conflict / stale_batch / stream_identity_conflict | 保留 pending 和身份，显式冲突处理，禁止改 UUID 或清 anchor 自动解围。 |
| 409 generation_conflict / collector_fenced / upload_lease_lost / dataset_retired | 停止旧会话写入，走既有身份恢复；不把未确认页当已上传。 |
| 413 / 429 / 5xx | 分别有界重查、持久退避、重试原批；任何失败不能留下半 receipt/半 facts。 |

### 3.3 协议升级与未知载荷

未知 metadata key、已知 key 的未知类型/enum、嵌套字典/数组/Data/不支持 Quantity unit 均 **fail-closed**。本地持久保存 blocked type、旧 anchor、页候选 archive/hash（若已获得）和错误类；不能形成可提交的“删字段版”。未完成 normalization 时样本仍由 HealthKit 保存，兼容修复从旧 anchor 重查；允许既有受保护本地页档案用于恢复，但不承诺跨年 HealthKit 删除保留完整性，不导出到日志/fixture。

```text
未知字段 → 本地待处理/兼容阻塞 → 不提交 ACK → 不推进 anchor → 等待兼容版本
```

wire union 不变的 registry 追加须显式协商新 registryVersion；语义/编码改变须新 contract version。旧 pending bytes 始终不可变，服务器升级需继续支持旧 receipt 的完整重试。新增 registry 不改变 query，因此正常情况下复用原 VO₂ anchor，无需重读；任何重编码只允许尚未获得服务器 commit 的页，在有明确新版本转换规则、保留旧身份审计且确认未提交后进行。无法确认提交状态时先查/重试旧 receipt，不自动丢 pending。query predicate 改变需另行审核 VO₂ 专属继承/新流方案，不能自动重置其他流或伪造历史完整性。

## 4. HealthKit raw fact 合同

### 4.1 Sample 与 provenance

每条事实 identity 为 `(datasetID, HKQuantityTypeIdentifierVO2Max, sampleUUID)`。同一 dataset 内 UUID 是来源样本身份；不是时间、数值、SyncIdentifier 或来源版本。UUID 小写规范化，保留 HealthKit 原值，不重新生成。startUs/endUs 原区间保留，`end≥start`；不按日聚合、不改成 query 时间。

fact 的完整字段集合为 `uuid/startUs/endUs/quantity/provenance/metadata`；metadata即使为空也用对象，provenance即以下versioned对象；没有category或兼容旧Daily的source别名。其他字段拒绝。`quantity={value: finite binary64, unit:"mL/kg/min"}`。数学单位为 **mL/(kg·min)**；wire 固定 `mL/kg/min`，归一化须先 `is(compatibleWith:)` 再用明确构造的 volume/mass/time HKUnit 转换，不以输入 unitString 文本猜测。必须 `HKQuantitySample.count==1`；series 不 flatten，等待新版本。有限但≤0 的原始值可以保存，投影为 invalid_input；不以未验证的人群上下限裁掉第三方事实。

新 provenance 使用 `schemaVersion="rai.healthkit.provenance/2"`：

| 字段 | 表达与缺失规则 |
| --- | --- |
| `sourceRevision.source.bundleIdentifier` | 非空 String，≤255 UTF-8 bytes，与 health_samples.source_bundle_id 完全一致。 |
| `sourceRevision.source.name` | SDK 提供的 String，≤255 bytes；原样保留，不用于来源认证。 |
| `sourceRevision.version/productType` | 各 String 或明确 null；nil 与空字符串不互相转换。 |
| `sourceRevision.operatingSystemVersion` | `{major:Int64,minor:Int64,patch:Int64}`，均非负；保留 SDK 实际值，0 不替换成当前设备 OS。 |
| `device` | HKObject.device=nil 时明确 null；有对象时包含以下六个属性。 |
| `device.manufacturer/model/hardwareVersion/firmwareVersion/softwareVersion/name` | 各 String 或明确 null，≤255 bytes；HKDevice.name 在已审 SDK 可用，必须纳入新 DTO。 |

SourceRevision 是描述写入源的 revision；Device 是样本关联设备；采集 RAI 的 iPhone/deviceID/collectorEpoch 是接入身份，三者不得相互填补。缺失不推断 Apple Watch、系统版本或“本机”。不抓取 localIdentifier、UDI、serial；这是明确隐私边界，因此**新旧 DTO 均不声称 HKDevice 完整无损存档**。任意未知 metadata 即使涉及这些信息也阻塞，不静默剥除。device.name/source.name 可能含用户命名，不进入诊断日志或分类证据的公开文案。

### 4.2 Typed metadata wire

`metadata` 为以 **SDK 实际字符串值**为 key 的 map；缺失 key 表示 SDK 未提供，不补 false/0/空字符串。存在 key 不允许 null。每个 value 必须为只有相应字段的 tagged union：

| tag | payload | 校验 |
| --- | --- | --- |
| `string` | `{type:"string",value:String}` | UTF-8，无截断；一般≤4096 bytes，指定 key 可更严。 |
| `boolean` | `{type:"boolean",value:Bool}` | NSNumber 必须 CFBoolean 类型；数字 0/1 不冒充 Bool。 |
| `int64` | `{type:"int64",value:Int64}` | JSON 整数 token；无小数/指数；范围 −2^63…2^63−1；绝不经 Double。 |
| `number` | `{type:"number",value:finite binary64}` | NaN/±Inf 拒绝；NSNumber 的整数不能被迫降为浮点；−0 canonical 为0。 |
| `date` | `{type:"date",epochUs:Int64}` | NSDate→UTC 微秒，统一最近微秒转换，记录 microsecond-resolution-v1；不 stringify 成当地日期。 |
| `quantity` | `{type:"quantity",value:finite binary64,unit:String}` | 每个已支持 key 有明确 compatible unit；禁止通用 description 字符串。 |

NSNumber 判别先 CFBoolean、再按 registry 期待类型及底层整数表示处理；Int64 用整数/stringValue 精确解析，不能先 `.doubleValue` 再取整。超 Int64、无法精确表达的整数/decimal、错误 tag 均拒绝。Go 用 typed union + RawMessage/json.Number，Swift 用 Int64 case，PG JSONB 保留整数。B2 必测 2^53+1、Int64 两端、Bool/NSNumber 混淆和跨语言 round trip。Date 的原始 SDK double 时间按上述已声明微秒精度表达，不宣称位级 NSDate archive。

只支持明确 registry 中的 key，支持六种 tag 不代表接受任意 metadata。registry v1 如下；多于64 key 或总 body 超限整页阻塞：

| SDK 常量 | 实际 wire key | registry v1 类型 / 规则 |
| --- | --- | --- |
| HKMetadataKeyVO2MaxTestType | `HKVO2MaxTestType` | int64，**1/2/3/4**；缺失保留缺失，未知值阻塞。 |
| HKMetadataKeyAppleDeviceCalibrated | `HKMetadataKeyAppleDeviceCalibrated` | boolean；read-only source context；缺失=unknown，不当作 false。 |
| HKMetadataKeyWasUserEntered | `HKWasUserEntered` | boolean；true 优先分类手动；false/缺失都不单独证明自动测量。 |
| HKMetadataKeySyncIdentifier | `HKMetadataKeySyncIdentifier` | 非空 string，≤255 bytes；与 SyncVersion 同在或同缺。 |
| HKMetadataKeySyncVersion | `HKMetadataKeySyncVersion` | int64；保留完整有符号整数，不能作 dataset commit watermark。 |
| HKMetadataKeyTimeZone | `HKTimeZone` | 非空有效 IANA string，≤128 bytes；不拿当前时区补历史。 |
| HKMetadataKeyAlgorithmVersion | `HKAlgorithmVersion` | int64；仅 source algorithm context，不是 RAI modelVersion。 |
| HKMetadataKeyDateOfEarliestDataUsedForEstimate | `HKDateOfEarliestDataUsedForEstimate` | date；仅提供者声明的输入起点，不是样本事件时间或 RAI coverage。 |
| HKMetadataKeyVO2MaxValue | `HKVO2MaxValue` | quantity，canonical mL/kg/min；只作该对象的附带 metadata，**不生成另一条 sample**。 |

`number` 已冻结编码与校验，但 registry v1 没有额外批准的 number key；不得为了演示六种类型虚构真实 metadata。未来已审 key 可以通过 registry 升级准入；未知的 finite number 仍阻塞。既有 Daily 其他 integer keys 不自动继承到此 registry；真实源若含额外 metadata，必须先审查，不为解堵而丢字段。

`HKMetadataKeyVO2MaxValue` 通常位于 lowCardioFitnessEvent，代表触发事件的关联数值，见 [Apple metadata 文档](https://developer.apple.com/documentation/healthkit/hkmetadatakeyvo2maxvalue)。本流只接受真正 VO₂ quantity sample；不能遍历事件 metadata 合成 UUID/VO₂ fact。若真正 VO₂ sample 自带该 key，保存它的 typed quantity，但根 quantity 仍唯一数值，二者不平均或互相替代。

### 4.3 TestType 与 availability

| wire Int64 | SDK 语义 | iOS availability / 约束 |
| --- | --- | --- |
| 1 | maxExercise | iOS11；提供者声明最大运动测试，不等于实验室验证。 |
| 2 | predictionSubMaxExercise | iOS11；亚最大运动预测。 |
| 3 | predictionNonExercise | iOS11；非运动预测。 |
| 4 | predictionStepTest | **iOS26**；step test 预测。 |

Header `HKMetadataEnums.h:191–196` 确认连续值与第4项 availability；[Apple 枚举文档](https://developer.apple.com/documentation/healthkit/hkvo2maxtesttype)给出语义。iOS18 target 可以使用经 Header 验证的原始整数 switch；任何 `.predictionStepTest` 符号引用必须在 `if #available(iOS 26.0, *)` 内。不得用 `@unknown default` 把4变成2、删除或当作实验室。大于4的未来值保留阻塞状态，无 ACK/anchor 前进。

## 5. 来源分类与 epoch 选择

分类算法身份 `rai.healthkit.vo2max.origin/1`，规则和 allowlist 纳入 configHash。分类是 **基于 HealthKit 描述符的工程推断，不是设备加密认证或生理验证**；不复用现有 Workout Classification(parent:) 的关联继承捷径。

按以下先后执行，所有条件/缺失/反证记录 reasonCodes：

1. `HKWasUserEntered=true` → **user_entered**，覆盖其他证据。
2. testType 必须在2/3/4中才属于本版可投影的 estimate；testType=1或缺失 → **unclassified**，分别记录 `max_exercise_assertion_only` / `test_type_missing`。不补 laboratory 标签。
3. **apple_watch_estimate**：写入 bundle 必须为 `com.apple.health`、`com.apple.Workout` 或严格 `com.apple.health.<合法UUID>`；productType 必须匹配 `^Watch[0-9]+,[0-9]+$`；device 必须存在，manufacturer 精确为 Apple/Apple Inc.，model 为 Watch/Apple Watch（比较时仅大小写归一，不模糊包含匹配）。其他明确设备反证则拒绝分类。source.name、一个 `com.apple.` 前缀、calibrated=true 均不足以准入。
4. **third_party_estimate**：testType=2/3/4，非手动，bundle 是有效反向域名形式且不属于 `com.apple` namespace；保留其来源描述，即使设备声称 Apple 也不是 Apple 的估计。
5. 其余全部 **unclassified**，保留 raw，不自动猜来源。来源字段缺失、未知 Apple bundle/型号、maxExercise 等不是传输丢弃理由。

这些 Apple bundle/Watch 描述符规则沿用当前源码可见的保守证据范围再收紧，不是 Apple 对完整来源分布的公开保证。必须用 synthetic 正反例实施，并在 B4 检验真实分布；不能承诺目前已能识别所有 Apple Watch 或第三方记录。新设备命名需版本化审核。

`HKMetadataKeyAppleDeviceCalibrated` 是校准上下文，不是准确概率或来源证明；false 标记 `provider_calibration_insufficient`，缺失标记 unknown，v1 不伪造置信度。[Apple 校准 metadata 说明](https://developer.apple.com/documentation/healthkit/hkmetadatakeyappledevicecalibrated)支持此类型语义。

`sourceEpochID=sha256:` + canonical provenance/2 对象（sourceRevision 全字段及 device 全字段，含显式 null）的 SHA-256，canonical version 固定 v1。OS/app/device 描述变化会产生新 epoch，原 facts 不回写。它是**描述符等价组**，缺少稳定设备标识时不能证明是同一只物理表；同型号设备可能不可区分，必须保留这一限制。

canonical v1固定：对象按UTF-8 key字节排序；字符串不做Unicode、大小写或空白归一；保留显式null；整数为精确最短十进制；binary64以可round-trip的最短表示编码、−0归0，不通过JSONB::text再hash。服务器是fact/epoch canonical hash权威；Swift/Go golden vectors必须确认相同canonical值，requestSHA仍仅hash原bytes。分类时的比较归一不改raw/identity。

默认投影过滤器固定为 **apple_watch_estimate only**。B3 初版采用**显式 epoch 选择**，不自动在多来源或版本变化时切换；即使仅一个候选 epoch，也由管理 compute/selection 入口明确写入选择。未选择而有事实时为 source_selection_required，选择不是 promotion。选择以 append-only `vo2max_source_selection` input 记录 epoch、input UUID/hash、记录时间、effectiveFrom、supersedes/revoked 和 actor/operation；禁止修改旧 input。新 app/OS epoch 需要显式新选择，切换前后保留引用与原因；从不因 UUID 排序、更多样本、更高值或不同源平均决定来源。

选择input的有效区间是投影评估时间`[effectiveFrom,effectiveThrough)`，不是样本事件范围；只有recorded-at不晚于capture fence的input可用。一个asOf必须有唯一未撤销、已显式解决supersession的选择；不以“最后写入的一条”消除重叠。选择历史epoch可读取该epoch早于选择时间的事实，但不能声称过去已选择过它。操作身份与actor写入既有admin_operations审计，新增严格operation_type分支，不绕过原约束。

## 6. 原始事实、修订和删除

冻结复用 `health_samples/entity_tombstones/sync_streams/sync_batches/entity_changes/change_events/dirty_scopes`。raw identity hash 覆盖样本 UUID、原区间、canonical quantity、完整准入 provenance 和 typed metadata；不包括 batch、query时间或动态来源分类结果。不同 typed tag、缺失/null、source revision 或 metadata 改变均为内容差异；不使用 JSONB 浮点重解析覆盖整数。

| 场景 | 必须行为 |
| --- | --- |
| 同 UUID / 同 canonical 内容 | unchanged，不复制事实、不覆盖 first_received_at。 |
| 同 UUID / 不同 quantity、interval、provenance 或 metadata | `409 sample_identity_conflict`，整事务回滚；本地保留冲突。不借同步版本更新现有行。 |
| 同批 delete + upsert 同 UUID | collapse delete 优先，不依赖数组或 SQL 执行次序。 |
| delete-before-create | 建立 tombstone、change log、external dirty/失效及 receipt；未知原时间/来源保持 null。 |
| tombstone 后新的 upsert batch | suppressed，成功 receipt 标记 deleted_precedence；不恢复旧 UUID。 |
| 旧 upsert batch 的 lost-ACK retry | 在当前会话 fencing 通过后只返回原 receipt，不重放事实，不改 coverage、commit revision 或 dirty generation。 |
| 新 UUID + 相同时间/值 | 独立事实，不按数值去重；冲突由投影判定。 |
| 删除被选最新事实 | **删除事务内立即移除 current pointer** 并增 generation；不得等 Worker。重算可选择较早仍存活事实，但 lineage 必须说明 withdrawal/fallback，绝不能继续返回已删除 UUID。无可用剩余且有该 epoch 的已知删除证据时为 deleted。 |

`SyncIdentifier/SyncVersion` 是 source-scoped 修订线索，不取代 UUID，不是全局去重键或下载水位。其分组至少包含 source bundle 与 sample type，不能跨提供者合并。Header 与 [Apple SyncIdentifier 文档](https://developer.apple.com/documentation/healthkit/hkmetadatakeysyncidentifier)说明较大同步版本可替换 HealthKit 对象；并未保证所有系统 VO₂ 更新都采用它或承诺固定 UUID 事件序列。

Apple [官方说明](https://support.apple.com/en-au/108790)确认存在更新既往 cardio fitness estimates 的用户流程；**这种更新真实产生何种 UUID/delete/metadata/分页次序，本轮 DEFERRED**。冻结处理矩阵：

- delete旧UUID + add新UUID：各自按真实事件入库；同批原子，跨页按页提交，中间状态可以 sync_incomplete，不能假装替换已完全完成。
- 相同UUID新内容：冲突并阻塞该页；不 in-place overwrite、不自动改 UUID。若系统实际采用此行为，需另审合同，不能在 B2 偷加可变 upsert。
- 新UUID而旧UUID仍可见：保留两者；若同 source-scoped SyncIdentifier 存在不同内容的多个 live UUID，即使版本不同，也为 conflicting_data，直到真实删除或经单独批准的显式修订合同解决。不能自行把低版本标删。
- 只改 metadata、OS 更新后回补旧日期、delete-before-add、delete重复及顺序不确定：同样适用，不按日期窗口伪造事件、不给缺失记录补 delete。
- 固定 bootstrap 窗口以前、HealthKit 删除保留期以外或授权可见性改变导致不可见的历史：不声称完整捕获；不为修补而全量重读或重置其他 anchor。

`health_samples` 是 live raw 集合，明确删除可移除 live 行；既有 sync_batches/change_events、tombstone 和 immutable analysis history 保存原始交付/撤回证据，不覆盖历史。**年龄过期没有 raw TTL，也不删除历史**；它不同于真实删除或未来独立隐私清除政策。历史 result 只可显式按 run 查看并标注 as-of/withdrawn，不能成为 current fallback。

## 7. Raw commit 与独立 Coverage

锁顺序为 dataset → stream → upload slot；dataset 行锁递增 commit_rev，禁止 sequence 作为下载 watermark。一个事务必须包含：会话检查、batch/stream注册、immutable raw事实、tombstone、source-aware撤回上下文、entity_changes/change_events、external dirty及current失效、coverage、stream链、dataset commit_rev和最终receipt。提交前以 wall clock 重验 upload lease。任一步失败均无 ACK、无部分更新；空页也有 coverage/receipt 的有序 commit。

VO₂ 独立 coverage 以其 streamID 为主键复用 `daily_health_coverage`，通过新 endpoint 暴露；旧 Daily endpoint 仍只返回原三类。服务端 snapshot 同时限定当前 datasetEpoch、collectorEpoch、contract/fingerprint，不把旧 collector 的完成状态转为新会话完成。

源事实change notification继续使用既有`sample` upsert/delete；coverage通知复用`daily_coverage`/observed，key为该HealthKit类型，内部payload含新contract身份。共享`GET /v2/sync/changes`仍是bounded通知，不是事实下载；其epoch-bound commit_rev/ordinal cursor与原类型事件交错时不变，不添加另一套水位或让旧客户端为VO₂重置游标。

| 字段 | 精确定义 |
| --- | --- |
| bootstrapStart | 流首次保存的固定下界。 |
| bootstrapComplete | 至少一个 hasMore=false 的页已经 raw commit；后续分页不会擦掉这个历史事实。 |
| queryCompleted / queryCompletedAt | 服务端已接收并提交一页成功 query 的证据及客户端 query 结束时间；不是后台授权成功。 |
| syncCompleteForObservation / hasMore | 最新 committed observation 是否 hasMore=false；仅截至该观察，不能承诺 HealthKit 现在无新变化。 |
| lastSyncAt | 服务端提交该 observation 的时间；不以手机时间替代。 |
| lastFactAt | 已接收 additions 的最大源 endDate，包括后来删除/被屏障抑制者；不等于最新有效事实。 |
| anchorPresent | raw receipt 对应 observation 有可提交的 client candidate anchor；服务端不存/解码 opaque anchor。lost ACK 时不能据此证明客户端已经 durable apply。 |
| sampleCount / deletedCount | 最新已提交页原 additions/deletions 计数，collapse/suppress前；不宣称历史总量。 |
| sourceSummary | 最近非空 additions 页的 sourceEpochID/count，最多128组；空页保留上次并标明 summaryObservationSequence。不是全库、全历史或真实人群来源分布。 |
| streamSequence | 最新 committed clientSeq；不是 dataset 下载水位。 |
| datasetGeneration | **当前 datasetEpoch UUID**，沿用本工程身份语义；另给 datasetCommitRevision，不能混同 scope inputGeneration。 |
| readAuthorization | 固定 `not_determinable`；不生成 granted/denied boolean。 |
| hasLiveFacts / hasDeletedFacts | 在同一 DB snapshot 中对该类型的 indexed existence；删除存在不证明曾看见原样本。 |

coverage state：无当前流receipt或最新hasMore=true → sync_incomplete；追平且有live raw → sample_exists；追平无live但有屏障 → sample_deleted；追平两者皆无 → no_sample_observed。API 不暴露“已确认不存在VO₂max”。

无当前流receipt的coverage仍返回类型与当前datasetGeneration，queryCompleted/anchorPresent/bootstrapComplete/syncCompleteForObservation=false、streamSequence=0、sampleCount/deletedCount=0、sourceSummary=[]；bootstrapStart及所有观察时间为null，hasMore=null（尚无观察）。hasLiveFacts/hasDeletedFacts仍按当前dataset真实集合查询，不能因新collector而擦掉raw。首个成功receipt后hasMore才是明确Bool。

`No sample observed ≠ Confirmed no VO₂max exists`。query完成、raw同步完成、可见样本和授权不可判定是四个维度。不能推断持续佩戴 Apple Watch、所有历史完整、用户没有数据、拒绝授权或生理正常。[Apple 授权文档](https://developer.apple.com/documentation/healthkit/authorizing-access-to-health-data)说明应用不能获知读取授权是否被允许；近期可见窗口也可能受用户选择限制。服务端看不到未上传的 blocked/pending 页，旧 coverage 只能注明它的观察时间，不能冒称已捕获此后变化。

## 8. External Projection 的输入与选择

身份 `rai.vo2max.external_projection/v1`；job kind 拟为 `analysis.vo2max_external_projection`；独立 scope **`external/vo2max/v1/athlete/current`**。不接 `model/<feature-stack>/athlete/vo2`，不消费 M1/M2/M3、Workout、HRmax/RHR、analysis_parameter_sets 或 hr_reference inputs。

一个 REPEATABLE READ、read-only snapshot 固定：dataset/collector身份、commitRevision、scopeGeneration、capture/asOf时间、该VO₂流coverage、被选epoch与选择input、typed raw fact hashes、相关tombstones及已知撤回上下文、projection configHash和external assignmentID。`asOf` 同时是事件资格上限；事实还必须在本次 capture commit fence 内已知。不能用未来才到达的事实冒充过去已知。

本版只计算**当前捕获快照**，历史审查读取 immutable run/artifact；不承诺用被删除的 live 表重建任意过去 as-known 数据集。迟到的旧事件改变新的 revised run，旧 run 的 asOf/lineage 不被改写。禁止把今天 raw 装进旧 generation；Load 时不匹配直接 SUPERSEDED。

冻结选择步骤：

1. 检查当前流 coverage（§9 顺序）；只有明确 source-selection input 可指定 epoch。选择输入冲突/多重有效未显式 supersede 时为 source_selection_required。
2. 只考虑被选 epoch 的 apple_watch_estimate facts；第三方、手动、未分类保留在 raw/evidence 排除计数。任何选择输入都不能把它们绕过默认 origin filter。
3. 对相关 live UUID 检查同 source-scoped SyncIdentifier 的多内容冲突；分组精确为`(datasetID,sampleType,source bundle,SyncIdentifier)`，可跨revision epoch。比较内容时仅排除UUID身份本身，其余时间/quantity/provenance/typed metadata（含SyncVersion）都参与；完全等价的多个UUID只作为等值证据。存在不同内容则 conflicting_data，不能以最大 SyncVersion 静默掩盖。
4. 选择 `endUs≤asOf` 的最近 eventEndUs；同 endUs 的完整候选组一起判定。根 value≤0 为 invalid_input，不退回更老数值掩盖最新错误。future-dated 样本不进入选择，若只有这类候选则 invalid_input/future_dated_input。
5. 最新同时间不同值为 conflicting_data，容差为0，不按UUID排序选胜者；相同值的多个 UUID 可以形成等值 evidence group，不宣称为多次独立测量。interval/context 分歧须记录；不可解释的互斥元数据为 conflicting_data。
6. 输出 selected fact 的原 canonical value，不平滑、取均值、校准或计算氧耗；按真实经过秒数做 freshness。sourceLineage 包含选择input、epoch、fact UUID/hash、源revision/testType/calibration、commit fence和排除/撤回证据。

source epoch inventory≤**32**组，typed snapshot全部sample/tombstone refs合计≤**1024**，最新tie group另限≤256个身份；包括相关SyncIdentifier冲突和撤回引用，不能以分组查询绕过总上限。查询必须 limit+1 检查，超出为 **analysis_input_limit 技术失败**，不截断后发布no_sample/available。用按dataset/type/epoch/end的索引定位候选与tie group，SyncIdentifier冲突用相同source-scoped键的有界检查；不扫描全库健康历史。

current可出值的事件域为`(asOf−90×24h,asOf]`；恰90日及更老样本只可用于expired/撤回状态证据。为区分“从未观察到”与“有过现已过期的样本”，允许在同一snapshot中对所选epoch执行额外索引定位，仍受上述总ref/tie上限约束，不扩大current资格、不遍历lifetime历史。删除后的合法前驱只有同一current域内可以出值；域外前驱不作为current fallback。历史专项重算不属于本版入口。Incremental与显式bounded Rebuild调用同一选择器、同一输入域/上限；排序只用于canonical hashing，不能解决语义冲突。

## 9. Result、状态与 Freshness

artifact key `vo2max_external_projection`，schema `rai.vo2max.external_projection/1`；结果显式包含以下字段（不可用时 null，不省略成默认0）：

```text
value, unit="mL/kg/min", method="external_estimate", provider="apple_health"
originClass, status, confidence=null, quality, evidence
parameterVersion=null, featureVersion=null
modelVersion="rai.vo2max.external_projection/v1", modelIdentity="rai.vo2max.external_projection/v1"
inputSchemaVersion="rai.vo2max.external_input/1", view="revised"
inputGeneration, calculatedAt, asOf, sourceLineage, reasonCodes
configHash, sourceSelectionInputID, datasetGeneration, datasetCommitRevision, knownThroughRevision
```

`provider` 表示接入平台；只有originClass=apple_watch_estimate时才可标Apple Watch估计。无被选来源时originClass=null。quality为结构化工程证据（calibration状态、缺失字段、冲突/排除原因），不是生理概率；confidence始终null，calibrated=true也不例外。parameterVersion/featureVersion明确null，来源选择input不是HR参数；source算法版本保留在lineage，不冒充RAI模型版本。modelVersion是完整逻辑身份，存储时algorithm_name=rai.vo2max.external_projection、algorithm_version=v1；knownThroughRevision精确等于本次capture的datasetCommitRevision，不另造sequence水位。

以下顺序是单一主 status 的决策优先序；其他已知情况进入reasonCodes，不能用Worker错误填这些状态：

| 顺序 / status | 适用条件 | root value |
| --- | --- | --- |
| 1 sync_incomplete | 缺当前身份的coverage、bootstrap未完成或最新hasMore=true | **null**；即使部分页已见值也不冒称完整选择。 |
| 2 no_sample_observed | 当前观察追平，无live事实，也无被选epoch的已知撤回证据 | **null**；未知delete屏障可作附加证据，不宣称不存在。 |
| 3 source_selection_required | 有事实但未选择epoch、选择无效/重叠，或无所选epoch可用候选及其已知撤回证据、只有默认排除来源 | **null**；包括第三方/手动/testType1-only。 |
| 4 conflicting_data | 被选epoch内SyncIdentifier冲突、最新同时间不同值或互斥上下文 | **null**。 |
| 5 invalid_input | 被选最新候选语义无效，如非正值；或候选仅未来时间 | **null**；transport/存储损坏不是此状态。 |
| 6 deleted | 无剩余可投影live候选，且tombstone明确记录该选择epoch曾有符合本origin filter的事实被删 | **null**；unknown或仅manual的delete不捏造来源。 |
| 7 expired | 被选有效样本age≥90×24h | **null**；历史原值仍在raw/历史run，不能作为current返回。 |
| 8 stale | 30×24h≤age<90×24h | **允许且必须保持原值**，明确标旧外部估计。 |
| 9 available | 0≤age<30×24h | **原值非空**。 |

deleted检查针对“已选择且已知撤回”的合格epoch事实，不能被第2/3行误遮蔽；即使另有无关第三方live事实，也继续完成选择/冲突/输入检查后进入deleted。反之，只有unknown屏障或被删manual事实且无live事实时为no_sample_observed。较早存活候选允许形成新的stale/available/expired run，但必须记录撤回与fallback；原pointer先失效。没有生理“0分”填充。校验破坏、DB超时、lease丢失、依赖snapshot异常、input超限是job FAILED/retry或SUPERSEDED，current为dirty/unavailable的技术状态，不伪装生理no_sample/invalid_input。

冻结 M4-A 提议的 **<30日 available、[30,90)日 stale、≥90日 expired**，包含30/90边界与UTC经过秒语义；这是**产品工程展示策略，不是生理有效期**。所有阈值、origin规则、canonical/input schema、source-selection模式和冲突规则均进入configHash；parameterVersion仍null。

时间到界即使没有新事实也必须生效：管理current reader在已发布immutable artifact上以明确evaluationAt做同一Freshen函数，不改数值/历史artifact，不产生训练或新raw。返回persistedAsOf与evaluationAt；evaluationAt早于run.asOf则拒绝该读取。到90日root value立即null，不等待新job；不得直接读analysis_results里的旧标量当current。没有历史原值TTL，无过期删raw。历史按run查询明确为当时asOf结果，若后来被删除则附withdrawn提示。

## 10. 数据库最小扩展合同（仅提案）

在当前最高23基础上，提议B2 `00024_vo2max_facts.sql`、B3 `00025_vo2max_external_projection.sql`；编号仅预留，实施前重新取实际最高版本，**本轮不创建SQL**。不改00001–00023，不为Apple/RAI/Laboratory各建结果表，不增加sequence。

### 10.1 B2 raw与coverage

- `health_samples` 继续共享现有列；新VO₂记录在metadata中保存服务器构造的reserved `RAI:vo2maxFactV1={schemaVersion,provenance,sourceEpochID,typedMetadata}`，不把这些字段混成字符串，不新增重复fact表/双份provenance列。schema为`rai.healthkit.vo2max_fact/1`，provenance为§4.1；sourceEpochID由服务器按固定canonical版本计算并校验，用于partial index；完整对象纳入既有canonical event/hash/change payload。wire禁止客户端直接提供RAI reserved keys；source_bundle_id必须匹配内部provenance。
- 仅对存在该reserved容器的VO₂行增加schema/type/object/size≤64KiB约束；normalizer/DTO也提前实施64KiB单事实context上限，超限整页阻塞。应用严格验证内部allowlist与数值。其他Daily/Workout metadata结构不变。已有其他入口的VO₂行若缺此schema，视为legacy/unclassified，不能自动升级、补provenance或参与本投影；新endpoint遇相同UUID不同内容照样冲突。各既有wire入口必须拒绝客户端提供这个新增reserved key，只有新DTO到内部Event的可信转换可构造它，不能靠DB外层shape check冒充完整准入校验。
- `entity_tombstones` additive nullable `provenance_snapshot`：known VO₂ delete时在删除live行前保存provenance schema/完整描述符/epoch、原content hash、原interval，以及typed testType/WasUserEntered/AppleDeviceCalibrated三项originInputs（缺失仍缺失），不复制VO₂数值；unknown delete=null，不后补虚构context。可按本版规则重判被删者是否原本可投影，不能把该epoch的一条manual删除误作Apple估计删除。所有既有合法删除入口共享此事务触发点，不只新endpoint；与屏障、change日志同事务。
- `daily_health_coverage` additive nullable VO₂字段：flow_created_at_us、query_completed_at_us、anchor_present、source_summary、summary_observation_seq；datasetGeneration/sequence通过当前dataset/stream join取得。原v1列及读写语义不变，旧行不回填、不改变三流查询结果。
- `sync_streams/sync_batches`复用唯一约束、hash、receipt；无需新receipt表。为VO₂候选的epoch/end检索及source-scoped SyncIdentifier冲突设计partial/expression indexes，不给所有health types强加模型索引或完整摘要。
- VO₂每次成功新observation（包括空页、未知delete）都触发独立aggregate dirty source `external/vo2max/facts/v1`。未知delete不经过“有时间才dirty”的Daily分支；dirty控制区间可用本次观察/commit时刻，明确是失效范围标记，不虚构样本时间。

### 10.2 B3 projection与candidate

- 复用analysis_runs/results/artifacts/publications/model_channels/model_dependencies和athlete_model_inputs。追加input kind `vo2max_source_selection` / schema `rai.vo2max.source_selection/1`；保留hr_reference现有约束分支，不放宽为任意kind/schema。source-selection不是生理parameter。
- `admin_operations`仅追加`vo2max_external_candidate`和`vo2max_source_selection`两类subject_dataset操作的严格分支，保留原operation/subject约束；存选择/配置hash和actor/operation身份，不记录健康数值。初始化/选择/重算重试返回原操作结果，不重复增代。
- 扩展dependencies CHECK仅新增精确upstream `external/vo2max/facts/v1` / `external/vo2max/selection/v1` 到downstream `external/vo2max/v1/athlete/current`；原feature/workout边不变。禁止连接M1 future athlete/vo2 placeholder，不建M1/M2/M3反向边。
- 共用candidate行的`extension_configs["rai.vo2max.external_projection/v1"]={configHash,assignmentID}`，该key单独严格校验；不改变既有performance key的string格式或M1 policy。external publisher仅校验自己的entry/assignment、scope/selection/generation，不把M1 stack/config当输入依赖。若candidate槽尚不存在，管理工具仅按既有平台兼容规则建立槽位元数据，不能顺带compute/enroll M1。其他model extension变化不使external数值重算，删除/改本entry则同事务失效。
- 追加一个artifact allowlist分支及对应RUNNING algorithm/version/dataset/inputGeneration校验，payload≤128KiB；不能放开为任意artifact。一个run写一个projection artifact、一个metric `vo2max_external_value`和一个family **vo2max_external_candidate** pointer，采用既有athlete sentinel day `0001-01-01`，不按日重复事实。legacy quality映射：available/stale有值均为partial，其余null为insufficient；available只表示投影数据状态，不认证生理质量。结构化status在artifact中，scalar不作current读取入口。
- active继续由DB约束保持legacy；external只有candidate，正式API/UI/训练消费者不注册该family。没有promotion、active复制、与RAI estimated互写或共用数值。撤销candidate entry应失效pointer、fence jobs，保留immutable历史，不执行schema Down。

### 10.3 权限与即时失效

API维持既有fact/coverage权限；**不增加**artifact/input读写、channel/dependency写入或publication直接写入。专用SECURITY DEFINER trigger/function只可失效本dataset的VO₂ scope及其pointer，固定`search_path=pg_catalog,public`，对象限定，REVOKE PUBLIC EXECUTE；不能通过任意scope/job kind参数成为越权分析写入口。source-selection/admin assignment变更同样立即dirty/invalidate。

Worker仅追加所需coverage/stream/collector身份SELECT（原roles缺coverage SELECT），沿用raw/tombstone/input读取、artifact INSERT、结果/指针发布与job权限；无raw UPDATE/DELETE、input/channel写入。owner/admin管理新constraints、input选择和enrollment。`deploy/postgres/roles.sql`、runtime-grants/readiness、restricted-role tests必须同时审查，不能用owner测试替代受限API/Worker验收。

source-aware失效覆盖：被选UUID删除；被选epoch新增/修订冲突；source selection/config/collector变更；delete-before-create；首次/空页/分页coverage变化。v1采取保守方式：任意本VO₂流变化都使本external scope dirty并移除pointer，记录相关epoch/UUID或unknown barrier；无关第三方也可影响coverage/evidence，不能只监听“当前已选UUID”。这不影响M1/M2/M3或三类Daily；无需全量raw重算。

## 11. Worker、发布与竞争

```text
HealthKit → VO₂ Discovery → Immutable Outbox → Go API
 → [Raw transaction: fact / tombstone / dirty / coverage / change / receipt]
 → External Projection Worker → Analysis Result + Artifact → Candidate Publication
```

沿用同镜像API/Worker、现有jobs/attempts和lease renewal，无第二套队列。B2只留下真实dirty/awaiting_model_implementation，不注册成功handler；B3完整实现选择器、typed输入、发布和故障处理后才可enroll该job。

计算在事务外进行，snapshot见§8；Begin run记录input hash/known-at fence、selection/assignment/config、fact/tombstone references，不复制所有健康trace。发布仍走唯一PublishAnalysis事务，锁顺序沿用dataset→job→dirty，校验：

1. dataset未retired、datasetEpoch与collectorEpoch仍当前；job fencing token/state与lease有效。
2. targetGeneration=snapshotGeneration=current dirty generation，自己的candidate assignment/config与选择input仍一致。
3. 重验完整捕获fact UUID/hash/存活、tombstone状态、coverage stream sequence及相关来源/SyncIdentifier冲突组；新事实、来源或withdrawal不能漏过generation fence。无pointer时的首次计算也必须被删除竞争拦住。
4. atomically artifact + scalar + candidate pointer + clean_generation + run终态 + job/attempt终态；提交前再次用`clock_timestamp()`检查lease，不用事务开始时的now()。

删除先拿dataset锁：立即失效并增代，旧worker只能SUPERSEDED；worker先提交：随后delete事务必移除刚发布pointer。过期lease worker不得写artifact/pointer或clean新代。old generation为SUPERSEDED（不是伪造业务available），回收/新代按现有job恢复流程处理；技术失败保留FAILED/retry证据，不把旧pointer恢复成current。

current reader同一SQL/snapshot验证dataset/collector、external assignment/config、scope clean、成功run与generation、artifact schema/hash以及必要的raw存活证据，再Freshen。分析结果发布不是新的源事实；不得让raw下载commit_rev指向未提交事实，亦不能以job sequence替代dataset水位。

**Incremental == Rebuild**：相同effective facts+tombstones+coverage+source selection+config+asOf/capture fence的typed输入，必有相同语义结果与canonical input/output hash；run UUID/calculatedAt等执行身份可不同。rebuild是当前scope有界纯重算，不是同步前提、不查生产库、不恢复备份、不扫描其他Daily/Workout。source switch/withdrawal说明由显式input和tombstone证据生成，不依赖上一次current pointer作隐式数值状态。

## 12. 验证边界与冻结结论

B2/B3/B4的文件范围、事务测试矩阵和可执行门禁见[实施计划](M4B_IMPLEMENTATION_PLAN.md)。B1仅做当前源码/SDK核对与文档冻结；已有Go回归通过不证明本协议已实现。没有保存真实HealthKit载荷、健康值、生产DSN或凭证，没有访问生产数据库、部署、迁移真实数据或切换旧入口。

```text
REAL DEVICE ACCEPTANCE: DEFERRED
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
RAI OWN VO2MAX MODEL: NOT IMPLEMENTED
M4-B1 RESULT: PASS WITH RISKS
```

M4-A全文及原证据已定位并核对，原“报告缺失”风险已移除。仍存风险：VO₂真实bundle/device/metadata分布与历史更新事件未验；strict allowlist可能阻塞；descriptor epoch不能认证物理设备且OS升级可能要求重新选择；长期离线删除保留、授权窗口和query predicate范围不能承诺历史完整。30/90日是未校准的产品策略。仅Simulator/synthetic可用于下一阶段工程验收，不能声称真实Apple Watch数值已获取或准确性已验证。冻结完成后停止，不自动进入B2。
