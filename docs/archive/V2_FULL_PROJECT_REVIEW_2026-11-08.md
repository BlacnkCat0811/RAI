# RAI V2 Full Project Review

日期：2026-11-08，Asia/Shanghai。审查基线：`1735a573f5c48dbbed62cb65deee094a76b754d0`（`feat(model,sync): model completion stack and workout sync v4`，2026-11-08 06:48:00 +0800）。

## 1. Executive Summary

总体架构仍然健康：原生 iOS（只读 HealthKit）→ Go 模块化单体 → PostgreSQL 18 唯一事实库的方向没有被破坏；`internal/admin` 没有被 `application/httpapi` 导入，没有 Web/Watch/桌面客户端，没有第二套模型评分器，上传原始事实、删除屏障、提交日志、分析待办与回执仍在同一事务中。

但当前 HEAD **不能作为可发布或可设备验收的版本**：

- **P0=2**：当前 HEAD 的 `scripts/local/test.sh full` 真实 PG 门禁失败（14 FAIL），根因是一个 v4 改造引入的 v3 Workout 分析回归；iOS v4 在 HealthKit 读权限被撤销时会把空读取当作完整读取，生成全量删除并永久 tombstone 旧 sample/route。
- **P1=16**：数据库升级阻断、API 数据库角色越权、模型回滚/回填不可用、模型负荷与 Recovery 正确性、v4 删除/来源/回退安全、门禁与证据真实性问题。
- **P2=36、P3=14**：主要集中在 v4 合同细节、模型研究诊断窗口、迁移 Down 链、权限收窄、OpenAPI/CHANGELOG 与验收证据粒度。
- 测试与证据存在明显的“历史 PASS 被当作当前 PASS”问题：`docs/IMPLEMENTATION.md:1201` 与 `docs/RAI_MODEL_COMPLETION_RESULT.md:38` 仍引用 2026-11-07 的 `full PASS 873s / 648 PASS / 0 FAIL`，而 2026-11-08 当前 HEAD 的 full 实测为 `946s / 646 PASS / 22 SKIP / 14 FAIL`；commit message 还写着 “Guardrails: no tests executed in this commit”，与可复现的 v4 测试产物矛盾。

**原始结论：FIXES REQUIRED BEFORE DEVICE ACCEPTANCE。** 上述 P0/P1 已在本报告的“修复状态”节所列后续提交中修复并通过 full 门禁；真机验收仍未执行。

原始审查未修改业务源码、测试、migration、合同、部署文件或历史证据；后续修复的改动尚未提交；临时调试 overlay 位于 `/tmp/rai-dbg`，未进入仓库。

## 1.1 修复状态（2026-11-08 10:43，后续更新）

原始审查之后已按用户要求完成 P0 与 P1 中的代码/数据缺陷及关键 P2 的修复，原始发现保留在本报告用于追溯；QA-009 仍按“gate 轻量、full 独立执行”的流程处理。

- P0-1 已修：`bundle_integrity.go` 先解码 `protocolVersion`，仅 v4 才按 v4 payload 解码；新增 v3 route 回归测试并通过。
- P0-2/SYNC-001..005 已修：iOS 空读取不再生成删除，服务端增加 complete-coverage/stale-observation 删除校验、v4 source policy 与回退重建 v3。
- DB-001..004、MODEL-001..004/007 已修；DB-002 通过 schema 45 view 收窄 API 读取权限，DB-007 通过 schema 46 将 `rai_training_dirty` 改为 SECURITY DEFINER 并撤销 API 直接更新 jobs 的授权。
- 修复后验证：`scripts/local/test.sh full` PASS（947 s，exit 0，666 PASS / 22 SKIP / 0 FAIL，leftover DB=0、cluster removed，日志 `/tmp/rai-full-fix3.log`）；`go test -race -count=1 ./...`、`go vet ./...`、边界检查、`swift test`（250 tests / 35 suites）、Simulator build 均 PASS。
- 仍未验证：真机 HealthKit 权限撤销/上传、真实设备验收；P2/P3 余项（如 DB-005 channel guard、DB-008 down 链、部分模型边界/展示）按原报告保留。

原始审查未修改业务源码；后续修复修改了源码、迁移、合同、部署授权与文档，尚未提交。

## 2. Review Baseline

| 项目 | 本轮事实 |
|---|---|
| Repository | `/Users/tp/rai` |
| Branch | `main` |
| HEAD | `1735a573f5c48dbbed62cb65deee094a76b754d0` |
| HEAD subject | `feat(model,sync): model completion stack and workout sync v4` |
| HEAD time | 2026-11-08 06:48:00 +0800 |
| Working tree at entry | 干净（`git status --porcelain` 空） |
| Working tree after review | 审查时 full 测试生成 8 个未跟踪的 `docs/evidence/workout-sync-speed/...` 文件；修复阶段已清理，当前工作区仅保留本报告及修复代码 |
| Go toolchain | `/Users/tp/rai/.local/toolchain/go/bin/go`，go1.27.3 darwin/amd64 |
| PostgreSQL | PostgreSQL 18.6，本机 `.local/device-acceptance/socket`、端口 55433，测试库 `rai_local_device` |
| 数据库迁移 | `goose_db_version` 已到 44；`checksums.json` 44 项 SHA-256 全部匹配 |
| 审查方式 | 静态代码/迁移/SQL/合同/文档审查 + 真实隔离 PG18 定向测试 + fresh race + full 门禁复跑 + 临时 overlay trace；未读生产库、未连生产、未执行部署 |

### 2.1 Scope

- `apps/server/migrations/00030`–`00044`、`deploy/postgres/roles.sql`、`runtime-grants.sql`。
- `apps/server/internal/storage/postgres` 的 workout 导入、删除屏障、analysis input、model stack worker、model promotion、backfill、athlete product/history。
- `apps/server/internal/syncing/workout_bundle_v4.go`、`workout_bundle.go`、`bundle_observation.go`、HTTP v4 分派与 handshake。
- `ios/RAI/App/V4SyncEngine.swift`、`SyncCoordinator.swift`、`HealthKitClient.swift`、`ios/RAI/Sources/SyncCore/WorkoutBundleV4.swift`。
- `contracts/sync-v4.md`、`contracts/openapi-v2.yaml`、`contracts/CHANGELOG.md`。
- `docs/IMPLEMENTATION.md`、`docs/WORKOUT_SYNC_V4_RESULT.md`、`docs/RAI_MODEL_COMPLETION_RESULT.md`、`docs/evidence/model-completion/acceptance.json`。
- `scripts/local/test.sh`、`scripts/check-boundaries.py`、`scripts/check-product-boundaries.py`。

### 2.2 Not Covered

- 未重跑真机 D01/D02；未接触 iPhone、HealthKit 真实数据或生产数据库。
- 未执行真实生产部署、v4 灰度、旧入口切换或真实数据迁移。
- 未执行 `goose down` 的真实数据回滚演练；未做并发压力/故障注入的穷举。
- 未重新生成 OpenAPI 客户端；`scripts/local/contracts.sh` 因 pinned Node 22.23.2 缺失未执行。
- 未做 200-Workout/大规模性能与完整 Simulator UI/E2E 回归。

## 3. Gate Results

| 命令 | 结果 | 证据 |
|---|---|---|
| `scripts/local/test.sh quick` | PASS，5.51 s | `.local/review-2026-11-08/quality.md` |
| `scripts/local/test.sh gate` | PASS，15.23 s；不含 integration tag | `.local/review-2026-11-08/quality.md` |
| `go test -count=1 ./...` | PASS，6.67 s，25 个有测试包 | 同上 |
| `cd ios/RAI && swift test` | PASS，250 tests / 35 suites | 同上 |
| `swift test --filter WorkoutBundleV4Tests` | PASS，5 tests / 1 suite | 同上 |
| `go test ./internal/syncing ./internal/httpapi` | PASS（v4 解码/HTTP 包） | 本机复跑 |
| 真实隔离 PG `TestWorkoutBundleV4ImportAndIdempotency` | PASS，1.639 s，leftover=0、cluster removed | `.local/review-2026-11-08/quality.md` |
| **`scripts/local/test.sh full`** | **FAIL，946 s，646 PASS / 22 SKIP / 14 FAIL** | 同上 |
| `go test -race -count=1 ./...` | PASS，exit 0，25 个 ok 包 | `.local/review-2026-11-08/race-fresh-lead.log` |
| `sqlc diff --no-remote` | PASS，exit 0 | 本机复跑 |
| `scripts/check-boundaries.py` | PASS，264 packages；9 条规则 | 同上 |
| `scripts/check-product-boundaries.py` | PASS，files=47 | 同上 |
| `scripts/local/contracts.sh` | 未执行，pinned Node 22.23.2 缺失 | 同上 |
| 真机 D01/D02 | 未重跑；历史记录 D01 PASS、D02 FAIL | `docs/IMPLEMENTATION.md` |

`gate` 输出的 2 s 与 15.23 s 差异来自 Go/race 缓存；两者都 exit 0。关键是 **gate 不含 integration tag**，所以“gate 绿”不能覆盖 full 的 14 个真实 PG 失败。

## 4. P0 Findings

### P0-1 [confirmed] 当前 HEAD full 门禁失败：v3 Workout 分析被 v4 provenance 解码回归拦截

**位置**

- `apps/server/internal/storage/postgres/bundle_integrity.go:250-291`
- `apps/server/internal/storage/postgres/analysis_input.go:94-190`
- `apps/server/internal/analysis/state.go:96-108`
- `apps/server/integration/workout_safety_test.go:304-313`

**事实**

`validateAnalysisProvenance` 在查询每个历史 import 时同时取 `payload->'observation'->'provenance'` 与完整 `payload`，然后**无条件**把 `payloadRaw` 反序列化到 `v4Payload`：

```go
var v4 v4Payload
if json.Unmarshal(payloadRaw, &v4) != nil {
    return analysis.Invalid()
}
if v4.ProtocolVersion == 4 { ... } else { ... }
```

`v4Payload.Routes[].Source` 是 `int`；v3 bundle 的 `routes[].source` 是对象（如 `{"bundleID":"com.apple.health"}`）。因此任何**含 route 的 v3 Workout** 都会在 `json.Unmarshal` 时报：

```text
json: cannot unmarshal object into Go struct field v4Payload.routes.0.source of type int
```

`loadWorkoutInput` 把该错误向上返回，`LoadAnalysis` 得到 `*analysis.Failure{Code:"analysis_invalid_input"}`；Worker 随即 `analysis.workout state=failed code=analysis_invalid_input`。这不是测试断言问题，而是分析输入链路被真实阻断。

**复现**

- 失败测试：`TestWorkoutDeleteInvalidatesPublishedP4AndKeepsHistory`
- 失败点：`integration/workout_safety_test.go:313` 的 `runOne(t, f, "analysis.workout")`
- 临时 overlay trace（未改 worktree）得到：

```text
DBG pre kind="analysis.workout" payload={"routeCount": 1, ...} requested=""
DBG switch kind="analysis.workout"
DBG before loadWorkoutInput scope="workout:HKWorkoutTypeIdentifier:..."
DBG v4 payload unmarshal err=json: cannot unmarshal object into Go struct field v4Payload.routes.0.source of type int
DBG after loadWorkoutInput err=*analysis.Failure analysis_invalid_input workoutnil=false
```

- `git show HEAD^:apps/server/internal/storage/postgres/bundle_integrity.go` 中不存在 `v4Payload` 代码；该回归由 HEAD 的 v4 provenance 改造引入。

**影响**

- full 的 14 个失败全部落在 `analysis.workout` 的 `analysis_invalid_input` 家族，覆盖 Workout 删除、恢复/备份、P7 安全边界、Feature/Load/Performance 等链路。
- v3 是已部署客户端的现网协议；在 v4 灰度尚未开启的情况下，现网 Workout 分析会直接失败。
- `TestP7RealLoginBoundariesAndRevocation` 失败说明安全验收同样被该回归污染，不能作为“安全通过”的证据。

**修复建议**

1. 先只解码 `protocolVersion`：
   ```go
   var version struct{ ProtocolVersion int `json:"protocolVersion"` }
   if json.Unmarshal(payloadRaw, &version) != nil { return analysis.Invalid() }
   if version.ProtocolVersion == 4 {
       var v4 v4Payload
       if json.Unmarshal(payloadRaw, &v4) != nil { return analysis.Invalid() }
       // v4 分支
   } else {
       // v3 provenance 分支，绝不解析 v4Payload
   }
   ```
2. 或者把 v4 route/stream 的 source 字段改成能同时接受 int 与对象的自定义类型，并只在 `protocolVersion==4` 时使用。
3. 增加含 route 的 v3 Workout 分析回归测试，以及 `protocolVersion=3/4` 双分支 provenance 单测。
4. 修复后重新跑 `scripts/local/test.sh full`，确认 14 个失败全部转绿。

---

### P0-2 [confirmed] iOS v4 把 HealthKit 空读取当作完整读取，读权限撤销后可永久删除旧 sample/route

**位置**

- `ios/RAI/App/HealthKitClient.swift:135-137`（`authorize()` 仅 `toShare: []`）
- `ios/RAI/App/HealthKitClient.swift:361-367`、`:443-449`（权限被拒返回空数组）
- `ios/RAI/App/V4SyncEngine.swift:139-143`、`:174-199`、`:285-289`
- `apps/server/internal/storage/postgres/workout_bundles.go:167-193`
- `apps/server/internal/storage/postgres/bundle_integrity.go:196-218`

**事实**

HealthKit 不暴露“读权限被拒绝”的直接状态；`bundleSamples`/`bundleRoutes` 在权限被拒或查询不可用时返回空数组而不是错误。`V4SyncEngine` 的 `readComplete = skipped == 0 && stream.readError == nil`，零样本、无错误仍为 `true`；`completeRoutes = routes.isEmpty ? true : ...` 也把空 route 当作完整读取。随后 `completeTypes`/`currentKeys` 用 `readComplete` 计算，previous index 中存在而 currentKeys 中不存在的 key 会被写入 `deletions`。服务端 `applyBundleDeletions` 对删除项写 `entity_tombstones` 并删除 `health_samples`；重新授权后同 UUID 再上传会被 409 `sample_deleted` 永久拒绝。

**影响**

- 用户撤销/重置 HealthKit 读权限后，一次后台同步即可把该 Workout 的旧 sample/route 全部 tombstone。
- 这是不可逆的用户事实删除，且同步重试无法恢复 UUID；只能换 dataset。
- 违反 `contracts/sync-v4.md:20/158/196` 的“覆盖缺失不得产生删除”语义。

**修复建议**

1. 空读取绝不作为删除授权：要求 previous 非空、当前至少一条、`queryCompleted=true`、anchor 前进；三者任一不满足就不生成 deletion。
2. 优先改用 `HKAnchoredObjectQuery.deletedObjects` 或显式用户确认删除，而不是用“当前集合为空”推断删除。
3. 服务端同时加防御（见 SYNC-002）：deletion 必须有同 type/route 的 `state=complete` 且 `sampleCount>0`。
4. 增加“权限撤销 → 空读取 → 不产生 deletion”的 iOS 单测与真机/模拟器集成测试。

> 两个 P0 是独立的：P0-1 是服务端 v3 分析回归；P0-2 是 v4 客户端删除授权错误。二者都必须修复后才能进入设备验收。

## 5. P1 Findings

### 5.1 汇总表

| ID | 领域 | 标题 | 状态 |
|---|---|---|---|
| SYNC-002 | v4 服务端 | deletion 未要求对应 coverage `complete` | confirmed |
| SYNC-003 | v4 服务端 | 过期 observation 的 deletion 仍删除较新事实 | confirmed |
| SYNC-004 | v4 服务端 | 逐样本/route 第三方 source 绕过 v3 source policy | confirmed |
| SYNC-005 | iOS v4 | 关闭/回退时 prepared v4 bundle 仍上传，永久 422 | confirmed |
| DB-001 | 迁移 | `00030/00032` 缺 `vo2max_source_selection`，29→44 升级阻断 | confirmed / PG18 reproduced |
| DB-002 | 权限 | `rai_api` 可全表 SELECT `analysis_results` | confirmed（DB 角色；HTTP 未证明） |
| DB-003 | 模型运维 | 非 legacy previous 的 A→B→A 回滚必 `ErrGateClosed` | confirmed |
| DB-004 | 回填 | `DiffBackfillOperations` 对真实 backfill 必报 `ErrBackfillConflict` | confirmed |
| MODEL-001 | 模型 | HR<Rest 的负强度被零截断并返回 available | confirmed |
| MODEL-002 | 模型 | Recovery v2 丢失 coverage/tombstone 门 | confirmed |
| MODEL-003 | 模型 | backfill affected 漏 readiness 闭包，overlap/source 窗口过短 | confirmed |
| MODEL-004 | 模型/回填 | research/maximal artifact 被当作未知产品槽，diff 必冲突 | confirmed（与 DB-004 同一根因） |
| MODEL-007 | 验收 | 验收矩阵组级复用 PASS；`validated` 与代码 `unvalidated` 冲突 | confirmed |
| QA-002 | 证据 | `IMPLEMENTATION.md:1201` / `RESULT.md:38` 把历史 full PASS 当作当前门禁 | confirmed |
| QA-003 | 证据 | commit message “no tests executed” 与可核对测试产物矛盾 | confirmed |
| QA-009 | 门禁 | `gate` 不含 integration tag，可与 14 个真实 PG 失败并存 | confirmed |

### 5.2 v4 服务端与 iOS

**SYNC-002（P1）— deletion 未要求 coverage complete**

`workout_bundle_v4.go:620-634` 只检查删除 type 在 Units、UUID 合法、无重复；`workout_bundle.go:181-203` 只检查删除键不与同一 bundle 的 samples/routes 冲突；`bundle_observation.go:270-404` 没有建立 deletion→coverage 的关系；`workout_bundles.go:193` 无条件调用 `applyBundleDeletions`。临时探针确认 `coverage[type]=no_records_observed` 加 deletion 可被解码并被 storage 执行。持有 collector token 的客户端可删除本 dataset 任意已知 sample/route UUID。

修复：服务端要求每个 deletion 对应的 `coverage[type].state=="complete"` 且 `sampleCount>0`；route deletion 对应 `coverage["route"].state=="complete"` 且 route count>0；否则 422。

**SYNC-003（P1）— 过期 deletion 仍生效**

`workout_bundles.go:167-193` 在 `bundleObservationChanged=false`（旧 observedAt）后仍调用 `applyBundleDeletions`；`bundle_integrity.go:196-218` 只表示“不更新 observation”，不阻止 deletion。修复：旧 observation 直接 409 `stale_observation`，或 deletion 必须绑定 observation/revision；不能让旧 bundle 的删除作用于新事实。

**SYNC-004（P1）— 第三方 source 绕过**

`workout_bundle_v4.go:516-549/598-618` 只检查 source 下标范围；`Event.Source` 只取 `BundleID`，没有 `HealthKitSource.Validate`/`Classification`；`bundle_observation.go:344-356` 在 v4 `requireProvenance=false` 时直接返回，跳过 v3 的逐样本来源校验；`bundle_integrity.go:293-322` 直到分析阶段才要求 classification eligible。结果是 ingest 可以 committed，然后分析报 `analysis_source_unclassified`。探针构造 `com.example.thirdparty` 子事实 + Apple 父 workout，被 ACCEPT。

修复：v4 ingest 时必须对每个 stream 的每个 `src`/route 的 source 做 `Classification`，只允许 `apple_watch`/`associated_apple_watch`；否则 422；不能把拒绝推迟到分析阶段。

**SYNC-005（P1）— 回退时 prepared v4 bundle 仍上传**

`SyncCoordinator.swift:856-878` 在 `if syncProtocolV4` 之前，只要存在 `bundleFile+hash+boundary` 就直接 `uploadAndAcknowledge`；`:376-379` 的 `syncDiscovered` 更早直接 `WorkoutBundleDelivery.deliver`。服务端 v4 关闭时返回 422 `unsupported_protocol`；`recovery` 分支又不允许重读 HealthKit，因此任务会永久卡住且不能重建 v3。

修复：按当前握手协议判断 prepared bundle；v4 不可用时丢弃并重建 v3，或标记 `pending_v4_rollout` 停止重试并提示用户。

### 5.3 数据库/迁移

**DB-001（P1）— 29→44 升级被 `vo2max_source_selection` 阻断**

`00030_model_completion.sql:7-11` 与 `00032_model_input_kind_schema.sql:5-13` 重建 kind/schema CHECK 时缺少 `vo2max_source_selection` 与 `rai.vo2max.source_selection/1`；`00025:32-38` 已允许该 kind，`admin/vo2_external.go:147` 会真实写入。审查者在一次性 PG18 上 `goose UpTo(29)`、插入该行、再 `admin.Migrate`，version 30 报：

```text
ERROR: check constraint "athlete_model_inputs_kind_check" ... violated by some row (SQLSTATE 23514)
```

即使只补 00030，00032 还会第二次阻断；00034 的补回发生得太晚。修复：00030/00032 白名单加入该 kind/schema；增加“含 VO2 selection 行的 29→44”升级测试；因 checksums 固定，需新增迁移或按流程更新并同步 checksums。

**DB-002（P1）— `rai_api` 过度读取 `analysis_results`**

`deploy/postgres/roles.sql:52` 给 `rai_api` 全表 SELECT `analysis_runs, analysis_results, analysis_publications`；`analysis_results` 是明细表，`analysis_publish.go:265` 按 run/metric 逐行写入，没有 candidate/published 区分；`analysis_published_artifacts` 只保护 artifact，不保护该表。数据库角色可跨 dataset 读取明细；HTTP 是否直接读取该表尚未证明，但数据库边界已经过宽，违反 `docs/IMPLEMENTATION.md:849` 的“API 不能读 candidate artifacts”。

修复：`REVOKE SELECT ON analysis_results FROM rai_api`；建立只含 SUCCEEDED run + publication + clean generation 的 `analysis_published_results` 视图并只授予该视图；对 `analysis_runs/publications` 同样加视图或 RLS；增加以 `rai_api` 查询其他 dataset 必须被拒绝的权限回归测试。

**DB-003（P1）— 非 legacy previous 回滚不可用**

`model_promotion.go:112-114` 强制 `manifestSHA==CurrentHash()`；`:146` 对 previous 调 `ensureModelStackReady`；`:230-231` 又硬编码 `stack==modelmanifest.StackID()`。当 previous 是上一个 managed stack（非 legacy）时，A→B→A 回滚必然 `ErrGateClosed`；现有 `TestSyntheticPromotionAndRollbackAudits` 只测了 previous=legacy。

修复：读取 previous 自己的 manifest/config 调 `ensureModelStackReady`；把 expected stack 作为参数传入；增加 A→B→A 集成测试。

**DB-004（P1）— backfill diff 对真实完成操作必失败**

`backfill.go:274-295` 的 `readBackfillSnapshot` 没有 artifact key 白名单；`computeModelStackDay` 每天固定生成 5 个非产品 artifact（1 个 maximal + 4 research）；`modelProductMetric` 对它们返回 `""`，`validHistoryArtifact` 走 default 返回 false，`DiffBackfillOperations` 必然 `ErrBackfillConflict`。`admin/backfill.go:134` 的 Diff 因此不可用。

修复：SELECT 只取六个 `model_product_v2/` key（或 `WHERE a.artifact_key IN (...)`）；补 `j.state='succeeded'`、`j.target_generation=r.target_generation`、publication/dirty clean 条件；增加“完成 backfill 后 diff 0 变更”的测试。

### 5.4 模型

**MODEL-001（P1）— HR<Rest 负强度被静默归零**

`trainingload/load.go:134-139/172-179` 与 `integral.go:18-27` 只拒绝 `r>1`；`r<0` 被截断为 0，仍返回 `available`。反例：Rest=50、Max=200、HR=40、HR 全覆盖 → `r=-0.0667`、`load=0`、状态 available；P4 合同要求 `invalid_input`。修复：`r<0` 直接返回 invalid，并补边界单测。

**MODEL-002（P1）— Recovery v2 丢失 v1 的 coverage/tombstone 门**

`recovery/v2.go:286-329,385-394` 没有要求 coverage complete，也没有检查输入已删除；v1 的 `input_deleted` / coverage complete 保护在 v2 丢失，缺数据仍可输出 available 分数。修复：恢复 v1 的覆盖与删除门；缺 coverage 或任一必需输入 tombstone 时返回 insufficient/invalid。

**MODEL-003（P1）— 回填 affected 闭包错误**

`backfill/affected.go:81-132` 的 `workout_load` 漏 readiness +207 天；overlap 漏 VO2/ability 且 readiness +55 过短；source readiness +30 过短；测试把错误窗口写成期望。后果是增量回算留下错误 readiness。修复：按 180 日/207 日闭包统一计算 affected；用 property test 断言每个下游 scope 都被覆盖。

**MODEL-004（P1）— 与 DB-004 同一根因**

`readBackfillSnapshot` / `modelProductMetric` 把 research/maximal artifact 当作未知产品槽；`candidate_ready` 的 diff 必冲突。修复见 DB-004；报告中将两项合并为同一修复任务。

**MODEL-007（P1）— 验收矩阵夸大**

`acceptance.json:1490` 称六个 envelope `validationStatus=validated`，但 `stampArtifact:35-45` 仅把 `raiVO2Max` 标为 `experimental`，其余为 `unvalidated`；V02–V16、R01–R13、A01–A11 大量复用同一包级 observed，逐 ID 不可追溯。这是验收真实性问题，不是模型算法本身。修复：每个验收项独立证据/断言；状态字段与 `stampArtifact` 实际输出一致；不能把组级结果写成逐项 PASS。

### 5.5 门禁与证据

**QA-002（P1）— 历史 full PASS 被当作当前门禁**

`docs/IMPLEMENTATION.md:1201` 与 `docs/RAI_MODEL_COMPLETION_RESULT.md:38` 写 `full PASS 873s / 648 PASS / 0 FAIL / 22 SKIP`，但该日志是 2026-11-07 14:26，早于 HEAD 16 小时；当前 HEAD 的 full 是 `946s / 646 PASS / 22 SKIP / 14 FAIL`。修复：把历史 PASS 标记为“历史证据/不适用于当前 HEAD”；更新当前门禁表；不得以旧日志签收。

**QA-003（P1）— commit message 与测试事实矛盾**

commit message 末尾：“Guardrails: no tests executed in this commit (per user request).” 但 DerivedData 的 `V4SyncEngineTests` xcresult 显示 2026-11-08 02:05 2 tests passed/0 failed；本轮也复现 v4 Go/Swift/PG 测试。修复：明确记录实际执行/未执行项；如果确实按要求未跑，应删除可复现测试产物的“已通过”表述，或补跑后更新 commit 说明。

**QA-009（P1）— gate 不覆盖 integration**

`scripts/local/test.sh gate` 只跑单元/race/vet/边界，不含 `-tags=integration`；因此 gate 可在 14 个真实 PG 失败时仍绿。修复：release gate 增加 integration profile（或另设 `release` profile 并在 CI/验收中强制），文档明确 gate 的边界。

## 6. P2 / P3 Findings

按各报告原始编号统计：**P2=36、P3=14**。以下按领域列出索引；详细证据见 `.local/review-2026-11-08/{sync-api,db-tx,model,quality}.md`。

### 6.1 v4 / API

- **P2 SYNC-006**：同一 `type` 重复 stream 未被拒绝；合同 `sync-v4.md:128` 要求 type 唯一。探针 ACCEPT 两个 heartRate stream。
- **P2 SYNC-007**：v4 sample metadata 使 observation 比较恒 changed；同内容重传也推进 revision/job，metadata 移除无法检测。
- **P2 SYNC-008**：iOS route 部分成功时覆盖 sidecar，旧 route identity 丢失，后续无法生成正确 routeDeletion。
- **P2 SYNC-009**：`contracts/openapi-v2.yaml:585/643-646/695/737` 仍 `protocolVersion const 3`，缺 `bundleCompact`/v4 schema。
- **P2 SYNC-010**：`contracts/CHANGELOG.md:1` 无 2026-11-08 v4/model completion 条目。
- **P3 SYNC-011**：v4 嵌套可选字段显式 null 可接受（`decodeV4Object` 对 `*T`/slice/map null 不报错）。
- **P3 SYNC-012**：`CompleteSampleTypes` 从 `series:<type>` 推导。
- **P3 SYNC-013**：iOS v4 仅 gzip，ratio 超限无 plaintext 回退，合法高压缩 payload 永久重试。
- **P3 SYNC-014**：删除 Workout 回执重放把 `alreadyDeleted=true` 覆盖为旧 false。
- **P3 SYNC-015**：v4 未要求 `observation.provenance.workout.bundleID` 出现在 `sources`。

### 6.2 数据库/权限/迁移

- **P2 DB-005**：`analysis_model_channels` active guard 可被 INSERT/candidate→active UPDATE 绕过。
- **P2 DB-006**：`publishModelStackDay` 的 `clean_generation` 更新无 generation 谓词，`analysis_publications` upsert 无降级保护；正常路径有 dataset 锁，函数级并发为 suspected。
- **P2 DB-007**：`roles.sql:79` 给 `rai_api` 全表 `UPDATE(target_generation,run_after)` on `jobs`，无 dataset/行级限制。
- **P2 DB-008**：00030–00044 Down 链不完整/不安全：00039–42 无 Down，00043/00044/00037 的 Down 在含数据时必失败，00044 Down 无 `OR REPLACE`。
- **P2 DB-009**：`readBackfillSnapshot` 未校验 job 状态/target_generation、publication/dirty clean，可能接受非当前 generation artifact。

### 6.3 模型

- **P2 MODEL-005**：`FreshenV2` 对 Recovery.FreshUntil/Coverage 的防御弱于 v1（复核后降为 P2 suspected；正常日栈 `ValidBefore` 覆盖 FreshUntil）。
- **P2 MODEL-006**：`recoveryID` 每次随机，进入 readiness `Recovery.RunID`/hash，重算不可复现；对用户值不直接可见。
- **P2 MODEL-008**：`PromotionGate.Validate` 对任何非 synthetic 数据集直接 `ErrGateClosed`，真实 promotion/rollback 不可用。
- **P2 MODEL-009**：`publishModelStackDay` 只数 `artifacts>=6`，不校验六个产品 key，research artifact 可凑数（当前 compute 固定 7 产品，latent）。
- **P2 MODEL-010**：VO2 `SpeedCoverage` 误用 HR 覆盖率，`CommonSeconds` 按窗口数×300 高报共同支持。
- **P2 MODEL-011**：VO2 15–28 天无新证据时返回 collecting + null，而不是 aging 保留上次数值。
- **P2 MODEL-012**：Recovery adverse 用“今天”的 baseline 评估最近 3 天，SDNN/RHR 任一缺失同时打断全部组件 persistence。
- **P2 MODEL-013**：`dailyRHRFacts` 忽略 source epoch，跨 epoch 的 RHR 被当成当前 epoch baseline。
- **P2 MODEL-014**：未指定 source 时用最后一个 feature 的 `SourceEpochs` 整 slice 静默筛选，顺序依赖且可能混源。
- **P2 MODEL-015**：TrainingLoad v2 的 `OverlapResolved` 从未读取，非 `OverlapUnknown` 一律标 resolved。
- **P2 MODEL-016**：Performance `Extract` 在距离差缺失时把 `DistanceAgreement` 默认为 1（完美）。
- **P2 MODEL-017**：critical speed 以 `KnownAt` 而非 Workout 日期做 42 天窗口，任意 181–899s duration 会进入后被拒。
- **P2 MODEL-018**：`latestDurability` 额外构造 600s 首段并缺少 terrain/environment，长跑时长与样本被篡改。
- **P2 MODEL-019**：训练 binding 对任何非 legacy stack 直接标 `schema2-v2`，不校验当前 manifest/六槽完整性。
- **P2 MODEL-020**：`LoadTraining` v2 分支仍保留 v1 `training_load.trimp` 作为 Workout.Load，反馈链路可能混用 v1/v2（suspected）。
- **P2 MODEL-029**：训练消费者只查 `ValidBefore`，不查 `Recovery.FreshUntil`/coverage（与 MODEL-005 区分）。
- **P2 MODEL-030**：`hrref.Detect` 对 `ActiveCertain=true` 但 `Active=[]` 的 Workout 仍标 certain；双向 resolution 不报冲突。
- **P2 MODEL-031**：Recovery `nightSDNN` 只要求任意睡眠事实与 00–06 相交，不要求主睡眠/静息上下文；habit target 不按 epoch 过滤。
- **P2 MODEL-032**：Performance `Score` 对非正/非有限 `v60` 没有显式 finite guard；`Freshen` 42 天状态与合同用词不一致（suspected）。
- **P2 MODEL-033**：Backfill request 不校验 `AsOf` 与 From/Through 的关系；Resume 不校验完成状态。
- **P3 MODEL-021**：`CalculateLoadStateV2` 只改 identity，不校验输入 load 的 model/version。
- **P3 MODEL-022**：`latestAerobic` 的 `Terrain` 恒为空，效率诊断无法区分地形。
- **P3 MODEL-023**：`historyTrend` 不检查 point.State，可能用 partial/stale 值计算描述性 delta。
- **P3 MODEL-024**：OpenAPI `AthleteModelHistoryV2.view` 只允许 revised，但 handler 接受 as_known。
- **P3 MODEL-025**：`selectSleepTarget` 不检测多个 active target/同 EffectiveFrom 冲突。
- **P3 MODEL-026**：`Readiness ValidateV2` 不校验 `Recovery.Model`，只靠 `CalculateV2` 内部检查。
- **P3 MODEL-027**：`publishModelStackDay` 的 current 检查不约束 channel 名称/assignment_version，previous stack 的迟到 job 也可能发布。
- **P3 MODEL-028**：`publishModelStackDay` 每次新建 runID，重复计算可能触发 identity 唯一冲突。

### 6.4 质量/证据/文档

- **P2 QA-004**：v4 的 Go/Swift/PG 测试可复现，但仓库内无日志、docs/evidence 无 v4 证据，只有 DerivedData xcresult，属“可复现但不可审计”。
- **P2 QA-005**：`acceptance.json` 的 99 PASS 只有 50 组唯一证据；V02–V16/R/A/P 共用包级日志，PASS 粒度被夸大。
- **P2 QA-006**：`docs/evidence` 存在 6 个 0 字节“vet/diff PASS”证据。
- **P2 QA-007**：`contracts/openapi-v2.yaml` 仍 const 3、缺 v4 schema；`contracts/CHANGELOG.md` 无 2026-11-08 条目，commit message 却称 changelog updates。
- **P2 QA-008**：README/ARCHITECTURE 顶部状态陈旧，schema 18 vs 代码 44。
- **P2 QA-010**：`scripts/local/contracts.sh` 因 pinned Node 22.23.2 缺失无法执行，合同 lint 未纳入门禁。
- **P2 QA-011**：`full` 会向 `docs/evidence/workout-sync-speed/` 写入未跟踪文件，污染工作区。
- **P3 QA-012**：`analysis_invalid_input` 错误缺少上下文，14 个测试只能看到 `<nil>`。

## 7. Architecture / Security / Product Boundaries

总体通过：

- 原生 iOS、HealthKit 只读（`toShare: []`），没有 Web/Watch/桌面客户端，没有 WorkoutKit 写入或通知权限。
- Go 模块化单体、PostgreSQL 18、pgx/v5、sqlc 方向保持；`internal/admin` 没有被 `application/httpapi` 导入。
- 上传的原始事实、tombstone、change events、dirty scopes、jobs、receipt 在同一事务；重试返回原 receipt；tombstone 旧上传不复活 UUID 的主路径静态核对通过。
- model input 函数与 publish lease/fencing 主路径通过；`validAnalysisLease` 锁 dataset 行，00041 invalidate 触发器也先锁 dataset，未发现 model stack 发布的 generation race。
- SQL 参数化、`/v2` 全 auth、错误不泄露凭证/err.Error 的总体检查通过。

需要保留的边界风险：

- **DB-002** 的 `analysis_results` 全表授权是数据库层真实越权面，即使 HTTP 当前未直接读该表，也应先收窄。
- `scripts/check-boundaries.py` 本轮把 `httpapi` 从“不得导入 athletemodel”的列表中移除；当前 `httpapi` 只做 transport，但检查脚本不再能阻止未来 httpapi 直接计算模型。这是有意放宽，建议补一条“httpapi 只能导入 athletemodel 的类型/错误，不得调用 Calculate”的规则。
- `scripts/local/test.sh gate` 的 `sqlc diff --no-remote` 已通过，但 gate 不含 integration；release 门禁必须单独覆盖真实 PG。

## 8. Evidence / Documentation Integrity

| 文档声称 | 当前事实 | 结论 |
|---|---|---|
| `docs/IMPLEMENTATION.md:1201`：full PASS 873s / 648 PASS / 0 FAIL / 22 SKIP | 当前 HEAD full 946s / 646 PASS / 22 SKIP / 14 FAIL | 过期证据，不能作为当前门禁 |
| `docs/RAI_MODEL_COMPLETION_RESULT.md:38`：同一 full PASS | 同上 | 过期证据 |
| `docs/WORKOUT_SYNC_V4_RESULT.md`：v4 Go/Swift/PG/Simulator 全 PASS | v4 Go/Swift/PG 可复现；仓库无 v4 日志，只有 DerivedData xcresult | 可复现但不可审计 |
| commit message：no tests executed | DerivedData 2026-11-08 02:05 有 2 个 v4 tests passed；本轮也复现 v4 测试 | 事实矛盾 |
| acceptance：99 PASS / 41 PARTIAL / 11 NOT RUN / 1 FAIL | 计数一致；99 PASS 仅 50 组唯一证据，6 个 0 字节 vet/diff 文件 | 粒度夸大，不能逐项签收 |
| `contracts/openapi-v2.yaml`：v2 API 完整 | 仍 `protocolVersion const 3`，缺 v4 schema/`bundleCompact` | 合同未更新 |
| `contracts/CHANGELOG.md`：v4/model completion 更新 | 无 2026-11-08 条目 | 合同变更未记录 |

补充：`docs/RAI_MODEL_COMPLETION_RESULT.md` 对真机 D02 FAIL、模型校准 NOT VERIFIED、真实激活 NOT RUN 的记录是诚实的；问题在于把历史 full PASS 当成了当前 HEAD 的门禁结论，以及验收矩阵的组级复用。

## 9. Recommendations

### Must fix before any device acceptance / v4 rollout

1. **P0-1**：修复 `bundle_integrity.go` 的 v3/v4 分支解码，增加含 route 的 v3 回归测试；重跑 `scripts/local/test.sh full`，要求 14 FAIL 全部转绿。
2. **P0-2**：空 HealthKit 读取不得生成删除；同时加服务端 deletion-coverage 防御（SYNC-002）；增加权限撤销测试。
3. **SYNC-003/004/005**：过期 deletion、第三方 source、回退 prepared v4 bundle 三处必须在 v4 灰度前修复。

### Before model completion acceptance

4. **DB-001**：修复 29→44 升级阻断，补真实升级测试；这是部署阻断项。
5. **DB-003 / DB-004 / MODEL-004**：修复回滚与 backfill diff，补 A→B→A 与完成 backfill 的集成测试。
6. **MODEL-001 / MODEL-002 / MODEL-003**：修复负荷负强度、Recovery coverage/tombstone、backfill 闭包。
7. **MODEL-007 / QA-002 / QA-003 / QA-009**：修正验收矩阵与门禁叙述；把 full integration 纳入 release gate；在文档中区分历史证据与当前 HEAD。

### Then

8. **DB-002** 收窄 `analysis_results` 权限；补权限回归测试。
9. **DB-005..009** 修复迁移 Down 链、channel guard、jobs 授权和 backfill snapshot 校验。
10. **SYNC-006..010 / QA-004..011** 修复 v4 合同细节、OpenAPI/CHANGELOG、证据日志和文档状态。
11. P3 项在相关模块下一次迭代中处理；不建议在 P0/P1 未关闭时投入大规模重构。

### Verification after fixes

- `go test -race -count=1 ./...`
- `go vet ./...`
- `scripts/check-boundaries.py` + `scripts/check-product-boundaries.py`
- `scripts/local/test.sh full`（必须真实 PG18，不能只用 gate/quick）
- `swift test` + v4 App tests + Simulator build
- 真机 D01/D02/D03/D05/D06/D07/D09/D10（未跑项按验收矩阵补齐）
- 至少一个“权限撤销 → 空读取 → 不删除”的真机/模拟器场景
- 29→44 含 `vo2max_source_selection` 行的升级演练

## 10. Final Verdict

**FIXES REQUIRED BEFORE DEVICE ACCEPTANCE。**

当前 HEAD 的总体架构和主事务边界没有发现需要重新设计的问题，但两个 P0 分别阻断了当前 full 门禁和 v4 的安全删除语义；16 个 P1 覆盖升级、权限、回滚、回填、模型正确性和证据真实性。修复并完成上述验证前，不应开启 v4 灰度、不应把当前 HEAD 标记为完成，也不应据此进行真机或部署验收。

## Appendix A — 本轮执行的命令与日志

- `scripts/local/test.sh quick` / `gate` / `full`：`.local/review-2026-11-08/quality.md`
- `go test -race -count=1 ./...`：`.local/review-2026-11-08/race-fresh-lead.log`
- `sqlc diff --no-remote`：exit 0（无输出）
- `scripts/check-boundaries.py`：264 packages，9 条规则 PASS
- v4 定向测试：`go test ./internal/syncing ./internal/httpapi` PASS
- 真实 PG `TestWorkoutBundleV4ImportAndIdempotency`：PASS
- full 失败复现：`TestWorkoutDeleteInvalidatesPublishedP4AndKeepsHistory`，`analysis_invalid_input`
- 临时 overlay trace：`/tmp/rai-dbg/`（未修改 worktree；仅用于定位 P0-1）

## Appendix B — 详细报告

- `.local/review-2026-11-08/sync-api.md`（390 行，SYNC-001–015）
- `.local/review-2026-11-08/db-tx.md`（434 行，DB-001–009）
- `.local/review-2026-11-08/model.md`（298 行，MODEL-001–033）
- `.local/review-2026-11-08/quality.md`（383 行，QA-001–012）

这些文件位于 `.local`（gitignored），是本报告的支撑材料；本报告已把需要进入版本库的结论自包含。

## Appendix C — 工作区状态

审查开始时工作区干净。full 测试与并行审查生成了 8 个未跟踪文件（修复阶段已清理）：

- `docs/evidence/workout-sync-speed/phase-timings/20261107T230743Z-b910a07d-import-phases.txt`
- `docs/evidence/workout-sync-speed/sql-plans/20261107T230741Z-28643d88-*.txt`（7 个）

这些文件已在修复阶段清理；`docs/V2_FULL_PROJECT_REVIEW_2026-11-08.md` 与修复代码仍为未提交变更。
