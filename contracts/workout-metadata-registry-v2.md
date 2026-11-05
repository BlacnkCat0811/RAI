# Workout metadata registry v2（UA1-R3A）

状态：**已实现，真实设备验收见 `docs/UA1_R3A_RESULT.md`**。
控制面版本：`workout-sync-completeness/v1`（不变）；Workout Protocol 3（不变，仅新增一个可选字段）。
Schema：不变（workout event 整体作为单个 JSON 文档保存，不新增列、不新增 migration）。

## 1. 为什么有版本

`metadataRegistryVersion` 声明生成该 bundle 的 Workout metadata 合同版本：

- 缺失 = 版本 1（原有九个 HealthKit key 白名单），旧客户端的行为与字节完全不变。
- `2` = 版本 1 + 两个真实 WorkoutActivity metadata key（opaque-preserved）。
- 其它值、或版本 < 2 却携带版本 2 的 key ⇒ 整包 `422 unsupported_payload`，服务端不降级、不丢弃。

服务端严格解码（`DisallowUnknownFields`），因此**不认识该字段的旧服务端会拒绝整个 bundle**：客户端保留 pending、不丢 metadata、不推进 completeness anchor（失败关闭，而不是静默降级）。

## 2. 决策分类

| key（SHA-256） | 原生载体 | 出现位置 | 类别 | 语义 |
| --- | --- | --- | --- | --- |
| `c4ea5dacadae2a3ab3f08a3ad60a4588803500fb71fbfb914b5d831abc180d42` | `String`（非空、`≤8` 字节、token 形状、UTF-8 往返一致） | WorkoutActivity metadata | **B: opaque-preserved** | 未文档化 |
| `58ed587001beb4224eb848f1d8904b9dd7d4bba8bdce7d5c1b8db5551c2f0f27` | `NSNumber:q`（`kCFNumberSInt64Type`，非 boolean，有限、整数、safe range 内） | WorkoutActivity metadata | **B: opaque-preserved** | 未文档化 |

判定依据：两个 key 均不存在于 iPhoneOS 26.5 SDK 的 HealthKit 头文件、也不存在于 HealthKit 二进制字符串表（1,847 个 `HK*`、79,135 个字符串全量哈希比对，含 39 个已文档化 metadata 常量）。因此按 `undocumented_real_metadata` 处理，**不从名称推断业务含义**，也**不把它升级为 A 类语义 metadata**。

类别 B 的合同要求（全部满足）：保留精确原生载体与精确值；不做任何解释；不参与任何模型/分析逻辑；不成为任意 JSON 逃生口；有明确上限。原始 key 字面量只出现在客户端与服务端源码的注册表里（`WorkoutMetadataRegistry.entries` / `bundle_observation.go`），证据与报告中只使用 SHA-256。

## 3. 作用域分离

| scope | 版本 1 的九个 key | 版本 2 的两个 key |
| --- | --- | --- |
| `workout` | 允许 | 拒绝 |
| `activity` | 允许 | 允许 |
| `activityEvent` | 允许 | 拒绝 |
| `workoutEvent` | 允许 | 拒绝 |
| `route` | 仅 `HKTimeZone` / `HKIndoorWorkout` | 拒绝 |
| `observation` | 版本 1 的九个 key | 拒绝 |

错误作用域一律 `metadata_requires_review`（客户端）/ `422`（服务端），不静默接受、不静默丢弃。Workout 级 allowlist 从未被 Activity key 污染。

## 4. 类型与上限

- 文本：`1...64` 字节（`HKTimeZone` 仍为 `1...128`），必须是合法 UTF-8，禁止控制字符（`<0x20`、`0x7f`）；**不 trim、不做大小写折叠、不做 Unicode 归一化**。
- 有符号整数：只接受精确 `int64`。boolean 载体、浮点载体、小数、超出 `int64` 的值一律拒绝，**不截断、不强制转换**。
- 单对象上限 64 个 key（不变）；bundle 字节/事件/数组上限不变；`limit+1` 失败关闭。

## 5. 身份与确定性

- metadata 不进入 `WorkoutCoreIdentity.canonical`（每个层级都剔除 `metadata`），因此新增 key 不会制造 `workout_core_change_requires_review`（R1 不回退）。
- 但 metadata 进入 fact 字节与 discovery fingerprint，所以它是**被保留的事实**：HealthKit 保存后不可变 ⇒ 跨重启确定。
- 规范化与输入字典顺序无关：同一语义 metadata ⇒ 同一 canonical 字节与哈希（两侧各有测试）。
- `streamSetVersion`、completeness fingerprint、manifest 结构不变：每个 bundle 自描述 registry 版本，新旧 capture 不会被混用。

## 6. 共享黄金向量

`contracts/fixtures/workout-metadata-registry-v2.json`（合成，不含真实值）同时被 Swift（`Tests/SyncCoreTests/WorkoutMetadataRegistryTests.swift`）与 Go（`internal/syncing/workout_metadata_registry_test.go`）读取，覆盖：合法整数、负值原样保留、错误载体（double/boolean/string/数组）、`int64` 溢出、`uint64` 溢出、合法文本、空文本、超长文本、控制字符、Unicode 原样保留、错误作用域、未知 key、重复 key、对象上限。

## 7. 未知 key 仍然失败关闭

两个真实 key 被接受之后，任何**新增**未知 key 在任意作用域仍然是 `metadata_requires_review` / `422`。新增语义 key 必须独立审查并再次 bump 版本；本轮修复不会变成“接受任意未来 metadata”。

## 8. 已知残余缺口

服务端 metadata 校验与版本 1 一样随 observation 记录执行（受支持客户端始终携带 observation）。不含 observation 的手工 payload 不会触发逐 key 校验——这是既有行为，不是本轮引入，已在 `docs/UA1_R3A_RESULT.md` 的风险项中记录；真实客户端路径不经过该分支。
