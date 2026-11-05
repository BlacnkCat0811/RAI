# UA1 Engineering Findings

2026-11-05；ua1-20261105-a。完整范围和结论见 [UA1 Result](UA1_RESULT.md)。本文件记录修复前的工程发现，**不实现修复、不修改 frozen 规则、不授予 calibration 或 activation**。

## UA1-01 — P1：Workout relaunch repeat 被 core comparison 阻塞

第一次真实上传成功：1 Workout、5,464 samples、1 route、1,768 points、1 immutable import。相同本地验收命名空间重启复跑时，12 个 scoped discovery anchors 保留，journal event count 仍 5,466，但重新组包在客户端报 workout_core_change_requires_review。服务端原语义 hash、import 和回执保持，没有重复写入。

### 直接证据与定位

- [实际 Workout/路线证据](../evidence/acceptance/device/ua1-20261105-a/workout.json)：保护状态中 originalWorkout 有 **12 个整数编码 quantity value 节点 / 34 个浮点节点**，只导出结构/counts，没有值。task Workout anchor1、discovery anchors12 保留。
- [HealthIntegrity.swift](../ios/RAI/Sources/SyncCore/HealthIntegrity.swift) 的 originalWorkout（约128行）去掉 metadata 后使用 JSON enum 合成等号比较。
- [Wire.swift](../ios/RAI/Sources/SyncCore/Wire.swift) 的 JSON decoder 先解 Int64，再解 Double；[HealthKitClient.swift](../ios/RAI/App/HealthKitClient.swift) 的 quantity normalizer 构造 number。整数值的 number 持久化后可解成 int，canonical wire bytes 不变，enum case 已不同。
- Git 忽略的 synthetic probe 对 1200.0 复现：canonicalBytesRemainEqual=true、enumEqualitySurvivesPersistence=false。该 probe 不含任何真实健康样本。

因此 codec/比较缺陷已可复现，实际持久化结构会触发该问题；本轮没有导出逐值新旧 HK payload，也没有断言排除了所有其他真实字段变化。不能把安全拒绝错误解释为“HealthKit 必然改了原始主体”。

### 下一工程任务的验收边界

先明确原始事实的 canonical 数字等价和严格数值/精度边界，再修比较或持久化表示。需要 synthetic 覆盖整数值 Double、分数、Int64 边界、负零、nested activities/statistics、真实内容变化继续拒绝；不能用一般 Double 强转丢失大整数精度，也不能删除字段绕过。

在原保护命名空间重新 build/install 后复验，无变化 rescan 应无新语义事实，原 receipt/hash 保持。不得清 Outbox、丢 anchor、换空命名空间或调整 model qualification 来声称该问题已修复。现有候选模型公式不在修复范围。

## UA1-02 — VO₂max typed metadata compatibility BLOCKED

真实只读首个查询的 protected blocked archive 有 **24 个 HKDiscreteQuantitySample**；并非无样本。运行时 unsupported_metadata，VO₂ anchor 未提交、sequence1、blocked archive retained、server VO₂ fact count0。传输安全 fail closed，投影无法验收。

[脱敏 metadata shape](../evidence/acceptance/device/ua1-20261105-a/vo2.json)：

| metadata key | 出现次数 | archive value class |
| --- | --- | --- |
| HKMetadataKeySyncVersion | 24 | float |
| HKVO2MaxTestType | 24 | float |
| HKMetadataKeySyncIdentifier | 24 | str |
| HKAlgorithmVersion | 13 | float |

没有未知 registry key；有5个 sourceRevision archive objects、13个 device descriptors，不能据此声称分类或物理设备数量。原 archive 只在仓库外私有临时目录读取结构并删除副本；源保护文件和 fingerprint 留在手机。

[VO2Max.swift](../ios/RAI/Sources/SyncCore/VO2Max.swift) 的 metadata adapter 对整数键要求整数 NSNumber objCType；synthetic NSNumber(double) 合法枚举值也被当前实现拒绝。**archive carrier + runtime rejection + synthetic probe** 形成明确兼容性审查材料；修复前仍需在真实 iOS SDK 边界核对 native NSNumber objCType、可精确整数性、Int64/enum 范围及 metadata 同步键约束，不能把归档表示直接等同于全部 native 实现细节。

下一任务应先评审 typed registry/SDK normalizer 合同及必要版本策略，保留 bool/unknown enum/unknown key/非有限/非整数/超界的拒绝。不允许删字段继续、任意接受 float、猜来源或生成 synthetic VO₂替代真实验收。修复后从保留的旧 committed anchor 重新查询/提交，复验来源规则、receipt、增量及 external projection；不修改 external model 公式。

## UA1-03 — M6 real all-stream transport 仍缺

单条 Workout 的 HR/route/power/speed/dynamics 本地查询完成并与所存 counts/hash 相符；这不能证明数据集所有相关流已追平。服务器仍没有 reviewed real all-stream caught-up transport，M7 实际 Readiness reasons 含 load_sync_incomplete、recent_load_unknown 等。

需要独立合同/实现任务定义真实 transport 证据及迟到/新页/删除等失效语义，再以真机复验。当前禁止 syntheticCoverage override、fixture manifest 或降低 completeness 门槛。此项不是模型权重或生理校准问题。

## 其他保留项

- K01/K03/K05 缺精确边界 hook；K02/K04 只按其实际范围通过。
- observer/background delivery 注册和 active callback 已观察；自然后台新事件未观察，显式后台切换由用户暂未执行。
- USB断开时用户点过同步并看到待交付提示；之后三类 pending 与自动 Workout work 均0，但未精确关联断线时段单条 receipt。不能仅凭用户点击将该 case 填 PASS。
- 本轮为单设备/固定窗口；无 physiological validation、population study、UI/Instruments/长期电量或生产容量结论。

**先处理工程问题并复验 UA1，再评估 UA2。** 本轮没有自动开始这些后续任务，没有改变 active=legacy。
