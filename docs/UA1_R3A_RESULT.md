# UA1-R3A Result

**UA1-R3A RESULT: BLOCKED — 真实 Activity metadata 已按 versioned typed contract 支持并在真机验证；R3 仍阻塞，但原因已从 metadata 换成 source policy / inventory scope。**

本报告记录 2026-11-05 的原 iPhone、原 UA1 测试环境和原验收命名空间。preserved R3 state 未被删除或重置：generation 0、committed anchors 0、无 manifest、无 anchor 推进。历史 UA0/UA1/UA1-R 报告保留原文。

## 1. Git Baseline

`UA1-R3A BASELINE`：main / `b826b36ac986ed9ea57bf7c7e7c42cdb58380ad6`，入口 `git status --short` 与 `git diff --stat` 均为空。未执行 reset/restore/clean/stash/rebase、commit/push、生产部署、旧入口切换或真实数据迁移。唯一的环境变更来自用户当次授权的隔离云测试环境重新部署（`/opt/rai-ua1-20261105`，schema 保持 28）。工作树当前为 7 个修改文件 + 7 个新增文件（Swift/Go 实现、合同、fixture、证据、隐私脚本），未提交。

## 2. Preserved R3 State

续跑前从设备容器读出的状态与 UA1-R 阻塞时逐字段相同：generation 0、committedAnchorCount 0、inventoryCount 44、completedWorkoutCount 4、capturedStreamCount 56、capturedPages 63、capturedAdditions 32,709、capturedDeletions 0、stateBytes 25,005,347、discoveryEvents 38,136、factCaptureFileCount 70、taskPhases `{ANALYZING 2, FAILED 1, PARTIAL 3}`、errorCode `metadata_requires_review`。沿用原 acceptance 命名空间，未新建、未 rebootstrap、未清理云端 dataset。[续跑前状态](../evidence/acceptance/device/ua1-r3a-20261105/r3a.json)。

## 3. Unknown Metadata Observation

两类未知 key 只出现在同一个 Workout 的 **WorkoutActivity** metadata 上，公开输出仅以 SHA-256 与 carrier 表示：

| key SHA-256 | scope | native carrier | 次数 | shape |
| --- | --- | --- | --- | --- |
| `c4ea5dac…180d42` | activity | String | 3 | 非空、长度 ≤ 8、token 形态、UTF-8 往返一致 |
| `58ed5870…f0f27` | activity | `NSNumber:q` | 2 | cfNumberType 4（SInt64）、非 bool、finite、integral、正、幅值 < 2^4、安全整数范围内 |

其余 context（workout / activity_event / workout_event）只有已 allowlist 的 `HKAverageMETs`、`HKElevationAscended`、`HKIndoorWorkout`（`NSNumber:d`）与 `HKTimeZone`。原始 key 名只存在于受保护本地文件与实现源码，未进入任何证据、报告或会话输出。

## 4. SDK / Semantic Review

本机 iPhoneOS 26.5 SDK 的 HealthKit 头文件共 95 个，metadata 常量全部来自 `HKMetadata.h`（70 个 `HKMetadataKey*`），不存在 `WO` 前缀常量；iOS 26.5 模拟器 HealthKit 二进制中 1,847 个 `HK*` 字符串与 79,135 个字符串全量 SHA-256 比对也无匹配；对 39 个已文档化候选（含 SyncIdentifier、SyncVersion、ExternalUUID、AppleFitnessPlusSession、ActivityType、WorkoutBrandName）逐一比对同样无匹配。设备为 iOS 27.2（24B119）。

结论：两类 key 均属 `undocumented_real_metadata`。没有据 `WO`/`IntervalStep` 名称推断业务含义，也没有声称它们是 Apple 文档化语义字段。

## 5. Registry Decision

| key SHA-256 | 决策类 | 理由 |
| --- | --- | --- |
| `c4ea5dac…180d42` | B 支持但 opaque-preserved | 语义未文档化；native 类型稳定（String）；必须进入 canonical preservation；不可解释 |
| `58ed5870…f0f27` | B 支持但 opaque-preserved | 语义未文档化；native 类型稳定（SInt64）；必须进入 canonical preservation；不可解释 |

两类均限定 **activity scope**，不进入 Workout 级 allowlist。opaque-preserved 的约束写入合同：精确 native 类型、精确字节、不解释、不参与模型逻辑、有界、不构成任意 JSON 逃生口。类别 A（typed semantic）因语义未验证被否；ignore / stringify / 放开所有 NSNumber+String 被明确禁止。registry 版本 1 → 2（语义扩张，不是 v1 parser bugfix）。[合同](../contracts/workout-metadata-registry-v2.md)、[共享 golden vectors](../contracts/fixtures/workout-metadata-registry-v2.json)。

## 6. Swift Typed Contract

[WorkoutMetadataRegistry.swift](../ios/RAI/Sources/SyncCore/WorkoutMetadataRegistry.swift) 是唯一的 key/类型真相来源：`Scope`（workout/activity/activityEvent/workoutEvent/route/observation）、`Kind`（`text(maxBytes:)`、`boolean`、`quantity(unit:)`、`signedInteger`）、每 key 的 scope 集合与 `introducedIn`。Normalizer 按 registry 逐 key 校验，不存在 `[String: Any]` 或任意 JSON metadata：

- `signedInteger`：要求 `NSNumber` 且 `objCType == "q"`、排除 `CFBoolean`，只接受精确 SInt64，绝不截断或把 double 转整数。
- `text`：要求 `String`，非空、`utf8.count <= 64`、拒绝 `<0x20`/`0x7f`；不 trim、不 lowercase、不做 Unicode 归一。
- per-object 上限 64 个 key，超限 `workout_metadata_limit`，不截断。

## 7. Go / Wire Contract

[Go](../apps/server/internal/syncing/bundle_observation.go) 与 Swift 同语义、同限额、同版本：`WorkoutMetadataRegistryVersion = 2`、`metadataScope` 区分 observation/workout/activity/activityEvent/workoutEvent、opaque key 仅 activity scope 允许、`metadataInteger` 只接受精确整数（`json.Number` 分数/指数/溢出拒绝，float64 需 finite+integral+安全范围）。wire 新增可选 `metadataRegistryVersion`（缺省=1，其它值 422）。

向后兼容：旧 App → 新服务器按 v1 解释（无字段），无行为变化；新 App → 旧服务器因 `DisallowUnknownFields` 直接拒收 bundle，客户端保留 pending、不丢 metadata、不推进 anchor。未来未知 key 仍 422 `unsupported_payload`。

## 8. Canonical / Identity

metadata 在 `WorkoutCoreIdentity.canonical` 的每一层都被排除，因此新 key 不可能重新制造 `workout_core_change_requires_review`（R1 类缺陷）。两类 opaque 值本身是 HealthKit 已保存事实，不随 OS 更新或显示状态变化；canonical bytes 由排序后的 key 生成，字典输入顺序不影响 hash（Swift 与 Go 各有测试）。completeness fingerprint / `streamSetVersion` 不变：manifest 证明的是流集合的传输完整性，而每个 bundle 自带 registry 版本，旧/新 capture 不会混用。

## 9. Simulator Validation

- `swift test`：**155 tests / 22 suites PASS**（新增 registry 合同测试 5 例 + 原生 carrier 测试 9 例）。
- 模拟器 App XCTest（iPhone 17 Pro / iOS 26.5）：**56 passed / 0 failed / 3 skipped**，result bundle `.local/ua1-r/simulator.SwnJHK`。
- 合成 fixture 覆盖 NSNumber(q) 合法、错误 carrier（double/bool/字符串/数组）、溢出、无符号溢出、错误 scope、未知 key、重复 key、超限 key 数、String 空/超长/控制字符/Unicode 保真。

## 10. PostgreSQL Validation

隔离 PG 18.6 + `-race` + `-tags=integration` 全量：`ok rai/server/integration 1095.623s`，全部包 PASS，`RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`。云端测试环境重新部署后校验：schema 28 未变、`admin migrate-status` 正常、API/Worker ready、镜像 `sha256:29e10b85…d0b5`、`workout_sync_completeness` 授权仍为 `{apiInsert:true, apiUpdate:false, workerSelect:true, workerInsert:false}`。registry v2 不新增表、不新增列（整条 event 以 JSON 文档存储），因此无迁移。

## 11. Real-Device Resume

新 build 安装后容器保留，从 generation 0 / committed anchors 0 / 既有 63 页与 56 个 stream capture 继续，未重置任何状态。原被阻塞的 Workout 通过 metadata 校验并完成：completedWorkoutCount 4 → 5、capturedPages 63 → 74、capturedStreamCount 56 → 67、capturedAdditions 32,709 → 32,712、deletions 0；blocked metadata Workout 归零。随后下一条可见 Workout 以 **`source_policy_pending`** 失败，整轮报 BLOCKED。

## 12. Full Workout Completeness

**未达成。** 44 条可见 running Workout 的来源分布（脱敏类别）：`apple_watch` 31 条、`third_party` 13 条（productType 为 iPhone、无 manufacturer/model）。既有 source policy 只采集 Apple Watch 原生采集，因此这 13 条永远不会产生 v3 receipt；而冻结合同要求「每个可见 inventory Workout 都必须持有当前 collector 的不可变 v3 receipt」。当前合同下 completeness 不可满足。

这不是 metadata 问题，也不是可在本阶段顺手放宽的 parser 修复：改变 inventory 谓词属于 stream/合同语义变更，合同文档已写明需重新评审。因此没有提交 manifest、没有推进 anchor、没有生成第二份 completeness generation，state 原样保留（fail-closed）。

## 13. Second Real Incremental

**NOT EXECUTED**（需要已提交的 manifest，本阶段不存在）。

## 14. Manifest Lost ACK

**NOT EXECUTED**（同上）。既有 acceptance hook 未被伪造使用。

## 15. M6 Re-evaluation

**NOT EXECUTED。** 未在未验证的 completeness 上重跑 M6：`load_sync_incomplete` 保持原状才是正确行为，用 client flag 或合成 coverage 让它消失会构成假 completeness（P0）。

## 16. Regression

| 项目 | 结果 |
| --- | --- |
| `go test ./...` | PASS |
| `go test -race ./...` | PASS |
| `go vet ./...`（含 `-tags=integration`） | PASS |
| 隔离 PG18 集成（`-race -tags=integration`） | PASS，1095.6s，无残留库/集群已移除 |
| `sqlc generate` | 无 diff |
| 依赖边界检查 | PASS |
| `gofmt`（受版本控制 Go 文件） | 无差异 |
| `git diff --check` | clean |
| Swift 包测试 | 155 tests / 22 suites PASS |
| 模拟器 App XCTest（iPhone 17 Pro / iOS 26.5） | 56 passed / 0 failed / 3 skipped |
| R1 真机复跑（同 Workout、同命名空间） | PASS：两次 relaunch bundle hash 一致（`06454e76…`）、`newSemanticEvents=0`、24/24 使用 committed anchor、无 pending discovery |
| R2 VO₂ | 无新增 blocked payload（`vo2_blocked_missing` 表示没有阻塞件），原修复路径未回退 |

R1 的 bundle hash 与 UA1-R 证据不同，仅因为 wire 增加了 `metadataRegistryVersion`；同一 build 两次运行完全一致，核心身份未变。

## 17. Privacy

新增强制门禁 [scripts/check-ua1-r3a-privacy.py](../scripts/check-ua1-r3a-privacy.py)，扫描公开产物（本阶段 evidence 目录、本报告、registry 合同）中的 UUID、设备标识、source bundle、raw metadata key：**5 个公开文件、0 命中 ⇒ PRIVACY GATE: PASS（公开产物）**。

原始 key 名只出现在受保护本地文件 `.local/ua1-r/private/r3a-keys-metadata-keys.json` 与实现源码/共享 fixture 中，从未出现在 stdout、stderr、设备报告或证据里。残留风险：既有 acceptance hook 的原始设备报告含 route 标识（coverage key），因此原始报告只留在本地、不发布；这是既有性质，本阶段未新增泄露。

## 18. Risks

1. **R3 阻塞在合同层**：13/44 可见 Workout 来自第三方写入方，冻结 inventory 谓词与 source policy 冲突，需要 reviewed contract 变更才能继续。
2. metadata 校验仍是 observation-gated（无 observation 的手工载荷不做逐 key 校验）——v1 既有性质，本阶段未加剧，已写入合同。
3. raw key 名存在于实现源码与共享 fixture（实现必需），不能进入证据或会话输出。
4. registry v2 使旧服务器拒收新 bundle（fail-closed 设计），部署顺序必须是服务器先行。
5. R1 bundle hash 与 UA1-R 证据不同（新增 additive 字段），需在后续阶段保持一致说明。
6. K01/K03/K05 仍 NOT EXECUTED；本轮未做后台唤醒、自然删除/更新、长离线、多设备。

## 19. UA1 Re-Acceptance Decision

```text
UA1-R3A RESULT: BLOCKED

R1 WORKOUT REPEAT:                    PASS
R2 REAL VO₂ COMPATIBILITY:            PASS WITH RISKS
R3 REAL WORKOUT ALL-STREAM COMPLETENESS: BLOCKED
  reason: source_policy_pending (13/44 visible running Workouts are third-party sourced)

UA1 RE-ACCEPTANCE:                    NOT GRANTED
REAL-DEVICE ENGINEERING:              NOT ACCEPTED (R3 open)
PHYSIOLOGICAL VALIDATION:             NOT VERIFIED
PRODUCT ACTIVATION:                   NOT AUTHORIZED
```

metadata 合同本身已通过评审、实现、双端一致性与真机验证；阻塞项已转移到一个需要用户决策的合同问题。未进入 UA2/UA3/UA4、Shadow 或激活流程。
