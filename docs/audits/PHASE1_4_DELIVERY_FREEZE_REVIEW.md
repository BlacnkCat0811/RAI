# RAI V2 Phase 1–4 交付冻结评审

- 日期：2026-11-01（Asia/Shanghai）
- 基线 HEAD：`aeb769c82f6af6c47d7cac82874f48ff37f635fc`
- 分支：`main`（本地 `origin/main` 同 SHA；未 fetch）
- 审计输入：`docs/audits/PHASE1_4_GIT_DELIVERY_AUDIT.md`、`docs/evidence/phase1_4_git_delivery_manifest.json`
- 结论：**READY_WITH_RISKS**

本阶段只做交付冻结、回归、分类、脱敏复核与 staging plan。未执行 `git add`、commit、push、deploy、生产 migration，未连接生产数据库。

## 1. Git 基线对照

| 项 | 审计时 | 本次冻结时 | 判定 |
|---|---:|---:|---|
| HEAD | `aeb769c82f6af6c47d7cac82874f48ff37f635fc` | 同左 | 未变化 |
| branch | `main` | `main` | 未变化 |
| tracked 修改 | 50 | 50 | 未变化 |
| untracked（不含 ignored） | 84 | 87 → 89 | 87 是新增测试后、本报告与 freeze manifest 生成前的计数；两个新产物写盘后为 89 |
| staged | 0 | 0 | 无 staged 内容 |

`git diff --cached --stat` 为空。`docs/audits/PHASE1_4_GIT_DELIVERY_AUDIT.md` 与 `docs/evidence/phase1_4_git_delivery_manifest.json` 存在。`apps/server/migrations/00014`～`00017` 均存在。

## 2. 正式未跟踪文件识别

以下列表是本次冻结的权威分类。`??` 状态不代表临时文件。

### 2.1 REQUIRED_PRODUCTION_UNTRACKED（14）

```text
apps/server/internal/httpapi/bundle_body.go
apps/server/internal/httpapi/timeouts.go
apps/server/internal/storage/postgres/bundle_integrity.go
apps/server/internal/storage/postgres/workout_bundles.go
apps/server/internal/syncing/bundle_decode.go
apps/server/internal/syncing/bundle_observation.go
apps/server/internal/syncing/phase_timing.go
apps/server/internal/syncing/workout_bundle.go
apps/server/internal/syncing/workout_delete.go
ios/RAI/Sources/SyncCore/HealthDiscovery.swift
ios/RAI/Sources/SyncCore/HealthIntegrity.swift
ios/RAI/Sources/SyncCore/WorkoutBundle.swift
ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift
ios/RAI/Sources/SyncCore/WorkoutSync.swift
```

覆盖：Workout bundle import、strict decode、provenance/integrity、HTTP body/timeout、performance timing、DELETE semantics、HealthKit discovery/integrity。

### 2.2 REQUIRED_TEST_UNTRACKED（24）

```text
apps/server/integration/per_workout_sync_test.go
apps/server/integration/workout_admission_http_test.go   # 本次新增
apps/server/integration/workout_bundle_test.go
apps/server/integration/workout_device_acceptance_test.go
apps/server/integration/workout_integrity_test.go
apps/server/integration/workout_performance_fault_test.go
apps/server/integration/workout_performance_test.go
apps/server/integration/workout_reliability_swift_test.go
apps/server/integration/workout_reliability_test.go
apps/server/integration/workout_safety_test.go
apps/server/integration/workout_transport_test.go
apps/server/internal/httpapi/bundle_admission_test.go
apps/server/internal/httpapi/bundle_body_test.go
apps/server/internal/httpapi/bundle_timeout_test.go
apps/server/internal/syncing/bundle_decode_test.go
apps/server/internal/syncing/bundle_integrity_test.go
apps/server/internal/syncing/workout_bundle_test.go
ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift
ios/RAI/Tests/SyncCoreTests/HealthIntegrityTests.swift
ios/RAI/Tests/SyncCoreTests/WorkoutBundleTests.swift
ios/RAI/Tests/SyncCoreTests/WorkoutPerformanceTests.swift
ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift
ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift
ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift
```

### 2.3 REQUIRED_MIGRATION_UNTRACKED（4）

```text
apps/server/migrations/00014_workout_imports.sql
apps/server/migrations/00015_workout_import_history.sql
apps/server/migrations/00016_route_timestamp_envelope.sql
apps/server/migrations/00017_analysis_sample_lookup.sql
```

`apps/server/migrations/checksums.json` 是 tracked 修改，必须与四个 migration 同一提交。

### 2.4 REQUIRED_CONTRACT_UNTRACKED（1）

```text
contracts/sync-v3.md
```

### 2.5 OPTIONAL_EVIDENCE_UNTRACKED（21）

`docs/evidence/rqrun-phase4-logs/` 下 21 个 `.txt` raw log。保留本地，默认不进入提交。

### 2.6 DO_NOT_COMMIT（5 + 目录）

```text
.local/run/device-enrollment.json
.local/run/fnos-device-credentials.txt
.local/run/device-connection.txt
.local/run/device-connection-fnos.txt
ios/RAI/Config/Developer.xcconfig
```

以及 `.local/` 整个本地工具链/运行目录。上述文件均实际存在、均被 ignore、均不在 Git index；未读取或输出其内容。

## 3. 测试覆盖缺口关闭

新增 `apps/server/integration/workout_admission_http_test.go`，只补审计指出的 v3 HTTP admission/cancel 组合缺口，未改 admission 架构。

| 场景 | 测试 | 断言 |
|---|---|---|
| A 未授权 | `TestWorkoutBundleHTTPUnauthorizedRejectsBeforeBody` | 真实 v3 multipart 请求、无效 Bearer → 401；handler 读取字节数 0；`sync_request_budgets` 0 行；无 Import/health facts；同设备后续合法请求 200 |
| B1 durable budget 拒绝 | `TestWorkoutBundleHTTPDurableRejectBeforeBody` | 预耗尽 device burst → 429 `device_rate_exceeded`；body 读取 0；无 facts；2.1s 后合法请求 200 |
| B2 admission storage failure | `TestWorkoutBundleHTTPAdmissionStorageFailureBeforeBody` | trigger 注入 `sync_request_budgets` 写入失败 → 503；body 读取 0；预算表回滚为 0；无 facts；移除 trigger 后 200 |
| B3 device slot busy | `TestWorkoutBundleHTTPDeviceSlotBusyBeforeBody` | 流式请求占槽；第二请求 429 `device_upload_capacity_exceeded` 且 body 读取 0；释放后同设备 200 |
| C client cancel | `TestWorkoutBundleHTTPClientCancelReleasesDeviceSlot` | body 读取中取消；handler 退出；无 Import/partial facts；同设备立即重试 200 且收到 `BundleSHA256` 非空回执 |

D. 多实例范围：`contracts/sync-v3.md` 新增 §7.1，明确当前保证为 **single API process / per-process admission**（`processUploads=2`、`processDeviceUploads=1`）；`sync_request_budgets` 仅限请求速率，不是上传槽；未声称跨实例全局协调，未重新引入 `AcquireUpload/ReleaseUpload`。

最终专项运行：

```text
--- PASS: TestWorkoutBundleHTTPUnauthorizedRejectsBeforeBody (0.43s)
--- PASS: TestWorkoutBundleHTTPDurableRejectBeforeBody (2.51s)
--- PASS: TestWorkoutBundleHTTPAdmissionStorageFailureBeforeBody (0.48s)
--- PASS: TestWorkoutBundleHTTPDeviceSlotBusyBeforeBody (2.53s)
--- PASS: TestWorkoutBundleHTTPClientCancelReleasesDeviceSlot (0.82s)
ok  	rai/server/integration	8.140s
RAI_TEST_LEFTOVER_DATABASES=0
RAI_TEST_CLUSTER_REMOVED=1
```

## 4. Secret / Privacy 提交门禁

候选集合为 feature 85 + docs 31 个文件（本报告与 freeze manifest 生成后 docs 增至 33）。扫描结果：

| 检查 | 结果 |
|---|---|
| private key / PEM | 0 |
| Bearer 长字面量 | 0 |
| password/passwd 赋值 | 0 |
| DSN 内嵌密码 | 0 |
| AWS/GitHub/OpenAI 类 token | 0 |
| JWT 形态 | 0 |
| `.p12` / `.mobileprovision` / `.cer` / `.key` 文件 | 0 |
| evidence 中 credential 命名字段 | 0 |
| GPS 数值字面量 | 4 处，均在集成测试；为固定上海市中心合成坐标，不是设备采集 |
| 原始健康值 | 候选 evidence 未包含 |

本地敏感文件：

```text
.local/run/device-enrollment.json            exists, ignored by .gitignore:8 (.local/), not in index
.local/run/fnos-device-credentials.txt       exists, ignored by .gitignore:8 (.local/), not in index
.local/run/device-connection.txt             exists, ignored by .gitignore:8 (.local/), not in index
.local/run/device-connection-fnos.txt        exists, ignored by .gitignore:8 (.local/), not in index
ios/RAI/Config/Developer.xcconfig            exists, ignored by ios/RAI/.gitignore:3, not in index
```

历史 tracked evidence（不修改 Git 历史，仅登记）：

- `docs/evidence/device-backend-2026-10-31.json`：含 5 个真实 dataset UUID 与 2 个内网地址，已存在于历史 tracked 内容，属于 REVIEW_REQUIRED。
- `docs/evidence/device-acceptance-2026-10-30.json`：含 3 个内网地址，已存在于历史 tracked 内容，属于 REVIEW_REQUIRED。

本阶段未对历史 tracked 文件做改写；未发现候选提交集合中的 `BLOCKED_SECRET`。

## 5. Migration 冻结

| 编号 | 文件 | 文件 SHA-256 | registry | 判定 |
|---|---|---|---|---|
| 00014 | `00014_workout_imports.sql` | `d26a4eddf3b989be79a4fdd01f927ab331a956c5bc96c3dffea1716657937ad8` | 同左 | match |
| 00015 | `00015_workout_import_history.sql` | `92790aae8ad2deaea02b4fdd7eed18f916c1b38cc2b4a13cdcc80a4f57aadee6` | 同左 | match |
| 00016 | `00016_route_timestamp_envelope.sql` | `d35999abca59ceb4e2359707bb78fe51cab3ae8af270f6c33c8301278bcac4c8` | 同左 | match |
| 00017 | `00017_analysis_sample_lookup.sql` | `73bc3b4a1281597e777727c96521bf16a692f8f8968ce09d4d175d1470cbb968` | 同左 | match |

- 17 个 migration 连续（00001～00017），无缺失、无重复。
- 17/17 文件 SHA 与 `checksums.json` 一致，registry 无 orphan。
- schema target = 17（`apps/server/internal/admin/operations.go` 的迁移后校验）。
- v3 bundle protocolVersion = 3（握手与 `bundle_decode.go` 均校验 3）。
- `00017_analysis_sample_lookup.sql` 使用普通 `CREATE INDEX`，非 `CONCURRENTLY`；本阶段不修改、不执行生产 migration，blocking 风险已登记在 `contracts/CHANGELOG.md` 顶部的 `2026-11-01 Phase 4 Delivery Freeze` 条目。

## 6. 提交边界

采用审计推荐的累计交付模型：一个 squash feature commit + 一个 docs/evidence commit。

### Commit Group A — Feature（85 文件）

- 生产代码 35：Go API/PostgreSQL/httpapi/syncing、iOS App/SyncCore、gzip。
- 永久测试 34：Go 集成/单测 + iOS SyncCoreTests/AppTests。
- 开发工具 1：`ios/RAI/Tools/SyncProbe/Probe.swift`。
- UI 测试 1：`ios/RAI/UITests/ProductUITests.swift`。
- Migrations 5：00014～00017 + `checksums.json`。
- Contracts 3：`contracts/sync-v3.md`、`contracts/openapi-v2.yaml`、`contracts/CHANGELOG.md`。
- Deployment 2：`deploy/postgres/roles.sql`、`ios/RAI/Config/RAI.entitlements`。
- Ops 4：`scripts/rai-backup.py`、`rai-restore.py`、`test-monthly-restore.sh`、`rai-reset-v2-data.sh`。

合计 35 + 34 + 1 + 1 + 5 + 3 + 2 + 4 = 85。

建议标题：

```text
feat(sync): implement workout-centric HealthKit sync v3
```

### Commit Group B — Documentation / Evidence（31 + 本报告 + freeze manifest = 33）

- 架构/实施文档 9：README、ARCHITECTURE、IMPLEMENTATION、P7 文档、月度备份、iOS README。
- Phase 1–4 报告/审查 9：`RQrun_PHASE1_RESULT`、`V2_PER_WORKOUT_SYNC_AUDIT`、`V2_PER_WORKOUT_SYNC_RESULT`、4 份 Phase 2–4 审查、`RQrun_WORKOUT_SYNC_REVIEW`、HealthKit 协议原文。
- Git Delivery Audit 与 manifest 2。
- concise evidence 11：phase1/2/3/4 JSON、device evidence、workspace baseline、git-final、acceptance、workout-bundle。
- failed-run summary 1：`rqrun-phase2-failed-runs.txt`。
- 本次新增：`docs/audits/PHASE1_4_DELIVERY_FREEZE_REVIEW.md`、`docs/evidence/phase1_4_delivery_freeze_manifest.json`。

建议标题：

```text
docs(sync): record workout sync v3 delivery evidence
```

### Mixed / hunk-level 文件

审计列出的 12 个混合文件（`SyncCoordinator.swift`、`RAIApp.swift`、`OutboxStore.swift`、`RAIAPIClient.swift`、`WorkoutSync.swift`、`WorkoutBundle.swift`、`workout_bundles.go`、`server.go`、`analysis_input.go`、`sync-v3.md`、`openapi-v2.yaml`、`IMPLEMENTATION.md`）保持整文件提交，不按 hunk 拆分；拆分会破坏可编译/可测试状态。

## 7. 证据分类

| Path | Decision |
|---|---|
| `docs/evidence/rqrun-phase1-2026-11-01.json` | KEEP |
| `docs/evidence/rqrun-phase2-2026-11-01.json` | KEEP |
| `docs/evidence/rqrun-phase3-2026-11-01.json` | KEEP |
| `docs/evidence/rqrun-phase4-2026-11-01.json` | KEEP |
| `docs/evidence/rqrun-phase3-device-2026-11-01.json` | KEEP（已脱敏摘要） |
| `docs/evidence/rqrun-phase4-device-2026-11-01.json` | KEEP（已脱敏摘要） |
| `docs/evidence/phase1_4_git_delivery_manifest.json` | KEEP |
| `docs/evidence/phase1_4_delivery_freeze_manifest.json` | KEEP |
| `docs/audits/PHASE1_4_GIT_DELIVERY_AUDIT.md` | KEEP |
| `docs/audits/PHASE1_4_DELIVERY_FREEZE_REVIEW.md` | KEEP |
| `docs/evidence/per-workout-sync-acceptance.json` | KEEP |
| `docs/evidence/workout-bundle-2026-10-31.json` | KEEP |
| `docs/evidence/rqrun-phase3-workspace-baseline.json` | KEEP（含临时测试 DB 路径 UUID，无健康数据） |
| `docs/evidence/rqrun-phase4-workspace-baseline.json` | KEEP |
| `docs/evidence/rqrun-phase4-git-final.txt` | KEEP |
| `docs/evidence/rqrun-phase2-failed-runs.txt` | KEEP（重要 failed-run summary） |
| `docs/evidence/rqrun-phase4-logs/*.txt`（21 个） | KEEP_OPTIONAL |
| raw Go logs（其它） | KEEP_OPTIONAL |
| raw xcodebuild logs | DO_NOT_COMMIT |
| `DerivedData/`（仓库外 `~/Library/...`） | DO_NOT_COMMIT |
| `*.xcresult`（仓库外 Test 日志） | DO_NOT_COMMIT |
| 临时 PG logs（`.local/`） | DO_NOT_COMMIT |
| `.local/run/*` 本地凭证 | DO_NOT_COMMIT |
| `ios/RAI/Config/Developer.xcconfig` | DO_NOT_COMMIT |
| 重复 performance logs | DO_NOT_COMMIT / REVIEW（未进入默认集合） |

未删除任何 DO_NOT_COMMIT 文件。

## 8. .gitignore 检查

实际检查（`git check-ignore -v`）：

```text
.gitignore:8:.local/                          .local/run/device-enrollment.json
.gitignore:8:.local/                          .local/run/fnos-device-credentials.txt
.gitignore:8:.local/                          .local/run/device-connection.txt
.gitignore:8:.local/                          .local/run/device-connection-fnos.txt
ios/RAI/.gitignore:3:Config/Developer.xcconfig ios/RAI/Config/Developer.xcconfig
```

- `ios/RAI/.gitignore` 已覆盖 `DerivedData/`、`*.xcresult`、`.build/`、`.swiftpm/`。
- 根 `.gitignore` 已覆盖 `*.log`、`.local/`、`secrets/`。
- 未发现真实临时/敏感路径缺少 ignore；本阶段未修改 `.gitignore`。
- 未使用 `*.json`、全局 evidence 之类宽规则。

## 9. 全量回归

| 门禁 | 命令 | 结果 |
|---|---|---|
| Go tests | `go test -count=1 ./...` | PASS（9 个测试包；4 个 no test files） |
| Go race | `go test -race -count=1 ./...` | PASS（11.99s） |
| Go vet | `go vet ./...` | PASS |
| Go vet integration/swiftclient | `go vet -tags=integration,swiftclient ./...` | PASS |
| Go build | `go build ./...` | PASS |
| Boundary | `python3 scripts/check-boundaries.py` | 3/3 PASS |
| PostgreSQL integration | `PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration,swiftclient -p 1 -timeout=30m ./...` | PASS（integration 349.284s；leftover DB 0；cluster removed） |
| Swift package | `cd ios/RAI && xcrun swift test` | PASS（98 tests / 11 suites，0.643s） |
| iOS Debug | `xcodebuild ... -configuration Debug -destination 'generic/platform=iOS' CODE_SIGNING_ALLOWED=NO build` | BUILD SUCCEEDED（clean build） |
| iOS Release | 同上 `-configuration Release` | BUILD SUCCEEDED（clean build） |
| iOS app tests | `xcodebuild test ... -only-testing:RAITests` | PASS（22 tests，0.181s） |
| OpenAPI | `npm exec --yes --package=@redocly/cli@2.71.1 -- redocly lint contracts/openapi-v2.yaml` | valid；31 warnings，0 errors |
| Git whitespace | `git diff --check` | PASS（exit 0） |

说明：iOS app tests 首次运行因 DerivedData 中旧 `SyncCore` module cache 报 `UploadResponse`/`retryAfter` 缺失；执行 `xcodebuild clean` 后重新构建并 22/22 通过。没有修改 iOS 源码。该失败是本地构建缓存问题，不是源码回归。

PostgreSQL 测试只使用 `scripts/test-postgres.sh` 的 mktemp 一次性集群（PostgreSQL 18.6），未连接持久开发库或生产库；结束时 `RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`。

OpenAPI 的 31 条 warning 均为非阻断：27 条 `no-required-schema-properties-undefined`，4 条路径级 warning；与早期文档记录的 7 条不同，已登记为已知风险。

## 10. 已知风险

| ID | 风险 | 处置 |
|---|---|---|
| R1 | `00017_analysis_sample_lookup.sql` 普通 `CREATE INDEX` 会阻塞 `health_samples` 写入 | 发布说明登记维护窗口；本阶段不改、不执行生产 migration |
| R2 | v3 上传槽为进程内状态，多 API instance 无全局协调 | 文档明确 single API process / per-process admission；跨实例 NOT VERIFIED |
| R3 | 历史 tracked device evidence 含真实 dataset UUID 与内网地址 | 不改写 Git 历史；单独登记 REVIEW_REQUIRED |
| R4 | Phase 1–3 源码快照缺失，历史 evidence 不能完整重建当时代码 | 保留现状；以本次冻结 commit 为可回滚点 |
| R5 | Phase 2 `/tmp` 日志不可用、Phase 3 `.log` 被 root `*.log` ignore；Phase 4 raw logs 仅本地 | 21 个 Phase 4 `.txt` 以 KEEP_OPTIONAL 保留 |
| R6 | OpenAPI 31 warnings（原文档记 7） | 非阻断；后续 Phase 5 清理 |
| R7 | 真机 HealthKit / 生产部署 / 目标负载未在本阶段重跑 | 保持未验证声明；需用户另行授权 |
| R8 | 首次 iOS 单测受 stale DerivedData 影响 | clean 后通过；记录为环境问题 |
| R9 | 工作区含 P7 文档/脚本等累计修改，非纯 v3 文件 | 按审计推荐累计交付模型纳入两个 commit；不按 Phase 强拆 |

## 11. 冻结问题回答

1. **是否还有正式生产文件处于未识别状态？** 没有。14 个 untracked production 与 21 个 tracked modified production 全部进入 feature 候选；新增 `workout_admission_http_test.go` 已补入 test 候选。
2. **00014～00017 是否完整？** 是。00001～00017 连续、无缺失、无重复。
3. **checksum 是否匹配？** 是。17/17 文件 SHA 与 `checksums.json` 一致，四个新 migration 单独复核为 match。
4. **admission/cancel 覆盖缺口是否关闭？** 是（A/B/C 及 B1/B2/B3 全部有真实 HTTP + 隔离 PostgreSQL 测试并通过）；D 的多实例范围已明确为 per-process，未虚假声称全局协调。
5. **Phase 1～4 回归是否全部通过？** 最终状态下全部通过：Go test/race/vet/build、boundaries、PostgreSQL integration、Swift package、iOS Debug/Release、iOS app tests、OpenAPI lint、`git diff --check`。
6. **是否存在 secret 候选进入提交集合？** 没有。0 private key/Bearer/password/DSN/JWT/cloud token；4 处 GPS 数值为合成测试 fixture；历史 tracked evidence 的隐私问题单独登记，不在候选集合。
7. **evidence 是否已分类？** 是。KEEP / KEEP_OPTIONAL / DO_NOT_COMMIT 表见第 7 节。
8. **是否建议 squash feature commit？** 是。按审计建议一个 `feat(sync): implement workout-centric HealthKit sync v3` squash commit，测试与源码同提交；docs/evidence 单独一个 commit。
9. **是否可以开始正式 staging？** 可以，结论 `READY_WITH_RISKS`；必须先按第 12 节显式路径 staging，再做 staged secret scan 与 migration checksum 复核。本任务未执行 `git add`。
10. **完成提交后进入 Phase 5？** 可以，在完成两个 commit、确认未 push、并接受 R1/R2 等已知风险后进入 Phase 5；生产 migration 需单独授权与维护窗口。

## 12. 建议 staging 命令（未执行）

Feature（85 个显式路径）：

```bash
git add -- \
  apps/server/cmd/rai/main.go \
  apps/server/integration/admission_test.go \
  apps/server/integration/p3_sync_test.go \
  apps/server/integration/p7_security_test.go \
  apps/server/integration/page_test.go \
  apps/server/integration/series_test.go \
  apps/server/integration/sync_test.go \
  apps/server/integration/upload_test.go \
  apps/server/integration/workout_package_test.go \
  apps/server/integration/per_workout_sync_test.go \
  apps/server/integration/workout_admission_http_test.go \
  apps/server/integration/workout_bundle_test.go \
  apps/server/integration/workout_device_acceptance_test.go \
  apps/server/integration/workout_integrity_test.go \
  apps/server/integration/workout_performance_fault_test.go \
  apps/server/integration/workout_performance_test.go \
  apps/server/integration/workout_reliability_swift_test.go \
  apps/server/integration/workout_reliability_test.go \
  apps/server/integration/workout_safety_test.go \
  apps/server/integration/workout_transport_test.go \
  apps/server/internal/admin/operations.go \
  apps/server/internal/httpapi/server.go \
  apps/server/internal/httpapi/bundle_body.go \
  apps/server/internal/httpapi/bundle_body_test.go \
  apps/server/internal/httpapi/bundle_admission_test.go \
  apps/server/internal/httpapi/bundle_timeout_test.go \
  apps/server/internal/httpapi/timeouts.go \
  apps/server/internal/jobs/worker.go \
  apps/server/internal/storage/postgres/analysis_input.go \
  apps/server/internal/storage/postgres/analysis_publish.go \
  apps/server/internal/storage/postgres/store.go \
  apps/server/internal/storage/postgres/bundle_integrity.go \
  apps/server/internal/storage/postgres/workout_bundles.go \
  apps/server/internal/syncing/protocol.go \
  apps/server/internal/syncing/store.go \
  apps/server/internal/syncing/bundle_decode.go \
  apps/server/internal/syncing/bundle_decode_test.go \
  apps/server/internal/syncing/bundle_integrity_test.go \
  apps/server/internal/syncing/bundle_observation.go \
  apps/server/internal/syncing/phase_timing.go \
  apps/server/internal/syncing/workout_bundle.go \
  apps/server/internal/syncing/workout_bundle_test.go \
  apps/server/internal/syncing/workout_delete.go \
  apps/server/migrations/checksums.json \
  apps/server/migrations/00014_workout_imports.sql \
  apps/server/migrations/00015_workout_import_history.sql \
  apps/server/migrations/00016_route_timestamp_envelope.sql \
  apps/server/migrations/00017_analysis_sample_lookup.sql \
  contracts/CHANGELOG.md \
  contracts/openapi-v2.yaml \
  contracts/sync-v3.md \
  deploy/postgres/roles.sql \
  ios/RAI/App/BackgroundTransfers.swift \
  ios/RAI/App/HealthKitClient.swift \
  ios/RAI/App/ProductModel.swift \
  ios/RAI/App/ProductViews.swift \
  ios/RAI/App/RAIApp.swift \
  ios/RAI/App/SyncCoordinator.swift \
  ios/RAI/AppTests/NormalizerTests.swift \
  ios/RAI/Config/RAI.entitlements \
  ios/RAI/Sources/RAICompression/gzip.c \
  ios/RAI/Sources/SyncCore/BatchBuilder.swift \
  ios/RAI/Sources/SyncCore/HealthDiscovery.swift \
  ios/RAI/Sources/SyncCore/HealthIntegrity.swift \
  ios/RAI/Sources/SyncCore/OutboxStore.swift \
  ios/RAI/Sources/SyncCore/RAIAPIClient.swift \
  ios/RAI/Sources/SyncCore/RouteBuilder.swift \
  ios/RAI/Sources/SyncCore/Wire.swift \
  ios/RAI/Sources/SyncCore/WorkoutBundle.swift \
  ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift \
  ios/RAI/Sources/SyncCore/WorkoutSync.swift \
  ios/RAI/Tests/SyncCoreTests/APITests.swift \
  ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift \
  ios/RAI/Tests/SyncCoreTests/HealthIntegrityTests.swift \
  ios/RAI/Tests/SyncCoreTests/WorkoutBundleTests.swift \
  ios/RAI/Tests/SyncCoreTests/WorkoutPerformanceTests.swift \
  ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift \
  ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift \
  ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift \
  ios/RAI/Tools/SyncProbe/Probe.swift \
  ios/RAI/UITests/ProductUITests.swift \
  scripts/rai-backup.py \
  scripts/rai-restore.py \
  scripts/rai-reset-v2-data.sh \
  scripts/test-monthly-restore.sh
```

Docs/evidence（33 个显式路径）：

```bash
git add -- \
  README.md \
  docs/ARCHITECTURE.md \
  docs/IMPLEMENTATION.md \
  docs/P7_BACKUP_RESTORE.md \
  docs/P7_DEPLOYMENT.md \
  docs/P7_OPERATIONS.md \
  docs/P7_RELEASE_CHECKLIST.md \
  docs/RAI_MONTHLY_BACKUP.md \
  ios/RAI/README.md \
  'docs/RQrun-HealthKit-上传协议.md' \
  docs/RQrun_PHASE1_RESULT.md \
  docs/V2_PER_WORKOUT_SYNC_AUDIT.md \
  docs/V2_PER_WORKOUT_SYNC_RESULT.md \
  docs/audits/PHASE1_4_GIT_DELIVERY_AUDIT.md \
  docs/audits/PHASE1_4_DELIVERY_FREEZE_REVIEW.md \
  docs/audits/RQrun_PHASE2_RELIABILITY_REVIEW.md \
  docs/audits/RQrun_PHASE3_DATA_INTEGRITY_REVIEW.md \
  docs/audits/RQrun_PHASE4_PERFORMANCE_REVIEW.md \
  docs/audits/RQrun_WORKOUT_SYNC_REVIEW.md \
  docs/evidence/per-workout-sync-acceptance.json \
  docs/evidence/phase1_4_git_delivery_manifest.json \
  docs/evidence/phase1_4_delivery_freeze_manifest.json \
  docs/evidence/rqrun-phase1-2026-11-01.json \
  docs/evidence/rqrun-phase2-2026-11-01.json \
  docs/evidence/rqrun-phase2-failed-runs.txt \
  docs/evidence/rqrun-phase3-2026-11-01.json \
  docs/evidence/rqrun-phase3-device-2026-11-01.json \
  docs/evidence/rqrun-phase3-workspace-baseline.json \
  docs/evidence/rqrun-phase4-2026-11-01.json \
  docs/evidence/rqrun-phase4-device-2026-11-01.json \
  docs/evidence/rqrun-phase4-git-final.txt \
  docs/evidence/rqrun-phase4-workspace-baseline.json \
  docs/evidence/workout-bundle-2026-10-31.json
```

未给出的命令（禁止）：任何全量/通配 staging（`-A`、`--all` 或 `.` 形式）、`git clean`、`git reset --hard`、`git restore`、`git checkout` 回滚、`git stash`。

## 13. 停止位置

已完成：覆盖缺口测试、privacy/secret review、migration freeze、全量回归、staging plan、freeze manifest、本报告。

未执行：`git add`、commit、push、deploy、生产 migration、生产 DB 连接。

下一步需用户明确授权后，按第 12 节 staging、检查 staged diff、secret scan staged content、migration checksum，再执行两个 commit；不 push，除非再次明确授权。
