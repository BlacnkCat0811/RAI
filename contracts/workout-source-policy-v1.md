# RAI Workout Source Policy V1

身份：`rai.workout.source_policy.apple_watch_only/v1`

策略文档：`contracts/fixtures/workout-source-policy-v1.json`（canonical JSON，排序键、无多余空白）

策略文档 SHA-256：`63713b4f82a33c6e9544de37fbd0701c42a29b71f2ca54588c7fc9cf463e0654`

分类版本：`1`

本文件只描述语义。具体分类所需的字面量（bundle 前缀、产品型号模式、厂商名）以冻结的
golden vectors 为准，不在文档中重复抄写；Swift 与 Go 都读取同一份 vectors 做一致性验证。

## 1. 结论表

分类结果到处置的映射就是全部策略，不存在其它分支：

| classification | outcome | reason |
| --- | --- | --- |
| `apple_watch` | `eligible` | `eligible_source_policy` |
| `third_party` | `excluded` | `excluded_by_source_policy` |
| `user_entered` | `excluded` | `excluded_by_source_policy` |
| `associated_apple_watch` | `unclassified` | `unclassified_source` |
| `source_unknown` | `unclassified` | `unclassified_source` |
| 任何未列出的值 | `unclassified` | `unclassified_source` |

未列出的值一律 `unclassified`：分类表之外永远不产生 `eligible`，也永远不产生 `excluded`。

## 2. 分类输入

分类只读取最小 descriptor：`bundleID`、`name`、`version`、`productType`、
`operatingSystemVersion`、`device{manufacturer,model,hardwareVersion,firmwareVersion,softwareVersion}`。

空字符串等同于“未上报”。Swift 与 Go 在这一点上完全一致（历史差异已在本阶段统一），
否则同一份 evidence 会在两端得到不同分类。

不为了分类上传额外 HealthKit metadata，也不放宽既有 source identity 规则：
`productType` 含 `Watch` 本身不构成 eligible 理由。

## 3. 新增分类 `user_entered`

`user_entered` 是本阶段唯一新增分类，用于用户手动录入的 Workout：

- bundle 为系统健康应用本身；
- `productType` 未上报；
- `device` 缺失或全空。

它是**确定性 exclusion**，不是 `unclassified`。其余既有分类（`apple_watch`、
`associated_apple_watch`、`third_party`、`source_unknown`）的判定顺序与规则不变。

## 4. Golden vectors

`contracts/fixtures/workout-source-policy-v1.json` 的 `vectors` 共 11 条，覆盖：

- Apple Watch native（含 scoped bundle）
- third party（iPhone 写入者、无 descriptor 的第三方）
- user entered
- 缺 bundle、缺 productType、model 不匹配、scoped bundle 缺 device、其它 Apple 来源、
  非 Apple 厂商 —— 全部 `unclassified`

Swift `WorkoutSourcePolicyTests` 与 Go `TestWorkoutSourcePolicyGoldenVectors` 都必须
逐条得到相同 classification / outcome，并校验策略文档自身的 digest。

## 5. 服务器不信任 `excluded=true`

客户端上传的每条 exclusion 都带 `sourceEvidence` 与 `evidenceSHA256`。服务器：

1. 用同一冻结规则在 evidence 上重新分类；
2. 要求重算结果等于声明的 `classification`；
3. 要求 `evidenceSHA256` 等于 evidence 的 canonical digest；
4. 要求 `outcome` / `reason` / `classificationVersion` / `sourcePolicyVersion` 全部匹配。

任一项不成立即 `422 exclusion_policy_mismatch` 或 `422 exclusion_evidence_invalid`，
manifest 不落库。

## 6. 版本演进

`apple_watch_only/v1` 是冻结身份，不得原地修改。若未来产品决定支持第三方来源，
必须新增 `source_policy/v2`，并重新执行相应的 availability / model acceptance。
