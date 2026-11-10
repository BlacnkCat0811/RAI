# RAI V2 — Phase 1–4 Git Delivery Audit

日期：2026-11-01（Asia/Shanghai）。类型：**READ ONLY Git Delivery Audit**。对象为当前工作区，非仅HEAD。

## 1. Executive Summary

**PASS WITH RISKS**。当前内容可整理为正式交付候选；**不建议直接叠加进入Phase 5**，应先完成交付评审和冻结。本结论不是功能、真机或生产部署验收PASS。

- 无tracked删除/重命名；Phase4入口683文件均存在。Phase3最终160项源码hash全部匹配Phase4入口，Phase4登记65项最终文件hash全部匹配当前。
- 00001～00017完整、连续、无重号，17/17实际SHA匹配registry。前16项未被Phase4改写；00014～00017全部untracked。
- 未发现可确认的Phase4意外覆盖或生产读取evidence。但P4入口只有hash，没有源码副本，不能声称完成逐hunk语义等价证明。
- 候选源码/阶段证据未检出有效secret；ignored本地目录发现凭证配置，均DO_NOT_COMMIT。历史已tracked设备证据另列隐私复核。
- 7个旧集成测试文件共删除13个顶层测试，均早于Phase4。入口退役有依据，通用admission/cancel组合覆盖仍有缺口。

本轮仅生成本报告和机器清单，不修改IMPLEMENTATION.md、源码、测试、migration、checksum、既有evidence或.gitignore；不执行测试、数据库、部署或Git写操作。AGENTS的实施完成门禁不能突破用户本轮只读边界；历史测试记录未冒充本轮重跑。

## 2. Git Baseline

| 项目 | 入口事实 |
| --- | --- |
| HEAD | aeb769c82f6af6c47d7cac82874f48ff37f635fc |
| branch / upstream | main / origin/main（本地缓存） |
| tracked总数 | 636 |
| tracked修改 | 50 |
| untracked（展开目录） | 84 |
| staged / deleted / renamed | 0 / 0 / 0 |
| Git可见文件总数 | 720 |
| hash快照 | 737 = 636 tracked + 84 untracked + 17 ignored |
| 捕获时间 | 2026-11-01T18:33:52.002559+08:00 |
| diff | 50 files changed, 2956 insertions(+), 2568 deletions(-)，不含untracked |

已读取要求的全部Git命令：status、HEAD、branch -vv、log -30、diff stat/numstat/name-status、cached stat/name-status、ls-files及untracked。原始输出见manifest.git_baseline.git_commands和tracked/untracked数组。可见历史14个提交。HEAD与本地origin/main相同，**未fetch/pull，不能推定远端实时没有新提交**。

HEAD为schema13；Phase1入口已是schema15、45 tracked修改+21 untracked，说明存在继承的v3改造。00014/15不是Phase1新建，不能将整个工作区累计diff归Phase4。

快照覆盖apps/server、ios/RAI、contracts、docs、scripts、deploy、migration、AGENTS、README及其余相关Git文件。排除.git、.local、build、DerivedData、node_modules、.build、.swiftpm、tmp、bin、dist、coverage、cache、xcresult等。docs历史截图/视频属于证据而非build artifact，保留hash，未作OCR；个人ignored Xcode状态列TEMP_ARTIFACT/DO_NOT_COMMIT。全部path/status/SHA/size/tracking/category见inventory。

## 3. Phase Ownership

依据：明确阶段新增/修改清单、final/entry SHA、Phase3续跑差异、报告设计与当前源码。整仓status只证明当时存在，不等于阶段增量；filesystem birthtime作为辅助元数据记录，不用时间猜归属。

Phase1读取RQrun_PHASE1_RESULT与JSON；未找到独立的Phase1修复后审计。RQrun_WORKOUT_SYNC_REVIEW是修复前缺陷审查，不能当Phase1验收。Phase2读取review/JSON/failed-runs；Phase3/4读取review、主JSON、device、baseline、git-final与全部登记日志。V2_PER_WORKOUT_SYNC_*属于更早继承背景。

P1/P2入口/tmp基线当前不可用；P3初始670文件快照缺失，续跑681文件快照可用；P4持久化683文件SHA但无源码副本。区间hash证明文件在P2→P3变化，不恢复完整patch。9个可能涉及P3的版本/权限/配置文件单列OWNERSHIP_UNCERTAIN，不强行归入已证实Phase3数组。

### Phase Ownership Matrix

新/改表示该阶段动作；—表示未找到该阶段增量证据，并非该阶段未使用；?为证据不足。覆盖134个dirty/untracked文件和13个Phase3 ignored日志，未改动文件也在JSON快照中。

| 文件 | Git | P1 | P2 | P3 | P4 | 继承 | 当前用途 | 风险 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `README.md` | M | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `apps/server/cmd/rai/main.go` | M | — | — | ? | 改 | 是 | PRODUCTION_CODE / 运行时源码 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |
| `apps/server/integration/admission_test.go` | M | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/p3_sync_test.go` | M | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/p7_security_test.go` | M | 改 | — | 改 | 改 | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `apps/server/integration/page_test.go` | M | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/per_workout_sync_test.go` | ?? | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/series_test.go` | M | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/sync_test.go` | M | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/upload_test.go` | M | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/workout_bundle_test.go` | ?? | 改 | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/workout_device_acceptance_test.go` | ?? | — | — | — | 新 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/integration/workout_integrity_test.go` | ?? | — | — | 新 | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/integration/workout_package_test.go` | M | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/integration/workout_performance_fault_test.go` | ?? | — | — | — | 新 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/integration/workout_performance_test.go` | ?? | — | — | — | 新 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/integration/workout_reliability_swift_test.go` | ?? | — | 新 | 改 | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 跨阶段累计修改 |
| `apps/server/integration/workout_reliability_test.go` | ?? | — | 新 | — | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/integration/workout_safety_test.go` | ?? | 新 | — | 改 | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 跨阶段累计修改 |
| `apps/server/integration/workout_transport_test.go` | ?? | — | — | — | 新 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/internal/admin/operations.go` | M | — | — | ? | 改 | 是 | PRODUCTION_CODE / 显式CLI维护源码 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |
| `apps/server/internal/httpapi/bundle_admission_test.go` | ?? | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/internal/httpapi/bundle_body.go` | ?? | — | — | — | 新 | 否 | PRODUCTION_CODE / HTTP传输/鉴权/请求处理 | 正式必需文件尚未tracked |
| `apps/server/internal/httpapi/bundle_body_test.go` | ?? | — | — | — | 新 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/internal/httpapi/bundle_timeout_test.go` | ?? | — | — | — | 新 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/internal/httpapi/server.go` | M | — | 改 | 改 | 改 | 是 | PRODUCTION_CODE / HTTP传输/鉴权/请求处理 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `apps/server/internal/httpapi/timeouts.go` | ?? | — | — | — | 新 | 否 | PRODUCTION_CODE / HTTP传输/鉴权/请求处理 | 正式必需文件尚未tracked |
| `apps/server/internal/jobs/worker.go` | M | — | — | — | 改 | 否 | PRODUCTION_CODE / 运行时源码 | 未见额外风险 |
| `apps/server/internal/storage/postgres/analysis_input.go` | M | — | — | 改 | 改 | 否 | PRODUCTION_CODE / 持久事实/事务/回执/分析输入 | 跨阶段累计修改 |
| `apps/server/internal/storage/postgres/analysis_publish.go` | M | — | — | — | 改 | 否 | PRODUCTION_CODE / 持久事实/事务/回执/分析输入 | 未见额外风险 |
| `apps/server/internal/storage/postgres/bundle_integrity.go` | ?? | — | — | 新 | — | 否 | PRODUCTION_CODE / 持久事实/事务/回执/分析输入 | 正式必需文件尚未tracked |
| `apps/server/internal/storage/postgres/store.go` | M | — | — | ? | 改 | 是 | PRODUCTION_CODE / 持久事实/事务/回执/分析输入 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |
| `apps/server/internal/storage/postgres/workout_bundles.go` | ?? | 改 | — | 改 | 改 | 是 | PRODUCTION_CODE / 持久事实/事务/回执/分析输入 | 含Phase1之前继承的dirty内容；正式必需文件尚未tracked；跨阶段累计修改 |
| `apps/server/internal/syncing/bundle_decode.go` | ?? | — | 新 | 改 | 改 | 否 | PRODUCTION_CODE / 协议DTO/严格校验/同步域 | 正式必需文件尚未tracked；跨阶段累计修改 |
| `apps/server/internal/syncing/bundle_decode_test.go` | ?? | — | 新 | — | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/internal/syncing/bundle_integrity_test.go` | ?? | — | — | 新 | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `apps/server/internal/syncing/bundle_observation.go` | ?? | — | — | 新 | — | 否 | PRODUCTION_CODE / 协议DTO/严格校验/同步域 | 正式必需文件尚未tracked |
| `apps/server/internal/syncing/phase_timing.go` | ?? | — | — | — | 新 | 否 | PRODUCTION_CODE / 协议DTO/严格校验/同步域 | 正式必需文件尚未tracked |
| `apps/server/internal/syncing/protocol.go` | M | — | 改 | 改 | — | 是 | PRODUCTION_CODE / 协议DTO/严格校验/同步域 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `apps/server/internal/syncing/store.go` | M | — | — | — | — | 是 | PRODUCTION_CODE / 协议DTO/严格校验/同步域 | 含Phase1之前继承的dirty内容 |
| `apps/server/internal/syncing/workout_bundle.go` | ?? | 改 | — | 改 | — | 是 | PRODUCTION_CODE / 协议DTO/严格校验/同步域 | 含Phase1之前继承的dirty内容；正式必需文件尚未tracked；跨阶段累计修改 |
| `apps/server/internal/syncing/workout_bundle_test.go` | ?? | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `apps/server/internal/syncing/workout_delete.go` | ?? | 改 | — | — | — | 是 | PRODUCTION_CODE / 协议DTO/严格校验/同步域 | 含Phase1之前继承的dirty内容；正式必需文件尚未tracked |
| `apps/server/migrations/00014_workout_imports.sql` | ?? | — | — | — | — | 是 | MIGRATION / 维护命令执行的schema迁移 | 含Phase1之前继承的dirty内容；正式必需文件尚未tracked |
| `apps/server/migrations/00015_workout_import_history.sql` | ?? | — | — | — | — | 是 | MIGRATION / 维护命令执行的schema迁移 | 含Phase1之前继承的dirty内容；正式必需文件尚未tracked |
| `apps/server/migrations/00016_route_timestamp_envelope.sql` | ?? | — | — | 新 | — | 否 | MIGRATION / 维护命令执行的schema迁移 | 正式必需文件尚未tracked |
| `apps/server/migrations/00017_analysis_sample_lookup.sql` | ?? | — | — | — | 新 | 否 | MIGRATION / 维护命令执行的schema迁移 | 正式必需文件尚未tracked |
| `apps/server/migrations/checksums.json` | M | — | — | 改 | 改 | 是 | MIGRATION / 迁移SHA登记 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `contracts/CHANGELOG.md` | M | 改 | 改 | 改 | 改 | 是 | CONTRACT / API/同步协议合同 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `contracts/openapi-v2.yaml` | M | 改 | 改 | 改 | 改 | 是 | CONTRACT / API/同步协议合同 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `contracts/sync-v3.md` | ?? | 改 | 改 | 改 | 改 | 是 | CONTRACT / API/同步协议合同 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `deploy/postgres/roles.sql` | M | — | — | ? | — | 是 | DEPLOYMENT / 部署/签名/App构建配置 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |
| `docs/ARCHITECTURE.md` | M | — | — | ? | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |
| `docs/IMPLEMENTATION.md` | M | 改 | 改 | 改 | 改 | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `docs/P7_BACKUP_RESTORE.md` | M | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `docs/P7_DEPLOYMENT.md` | M | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `docs/P7_OPERATIONS.md` | M | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `docs/P7_RELEASE_CHECKLIST.md` | M | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `docs/RAI_MONTHLY_BACKUP.md` | M | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `docs/RQrun-HealthKit-上传协议.md` | ?? | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `docs/RQrun_PHASE1_RESULT.md` | ?? | 新 | — | — | — | 否 | AUDIT_REPORT / 实现结果/审查记录 | 未见额外风险 |
| `docs/V2_PER_WORKOUT_SYNC_AUDIT.md` | ?? | — | — | — | — | 是 | AUDIT_REPORT / 实现结果/审查记录 | 含Phase1之前继承的dirty内容 |
| `docs/V2_PER_WORKOUT_SYNC_RESULT.md` | ?? | — | — | — | — | 是 | AUDIT_REPORT / 实现结果/审查记录 | 含Phase1之前继承的dirty内容 |
| `docs/audits/RQrun_PHASE2_RELIABILITY_REVIEW.md` | ?? | — | 新 | — | — | 否 | AUDIT_REPORT / 实现结果/审查记录 | 未见额外风险 |
| `docs/audits/RQrun_PHASE3_DATA_INTEGRITY_REVIEW.md` | ?? | — | — | 新 | — | 否 | AUDIT_REPORT / 实现结果/审查记录 | 未见额外风险 |
| `docs/audits/RQrun_PHASE4_PERFORMANCE_REVIEW.md` | ?? | — | — | — | 新 | 否 | AUDIT_REPORT / 实现结果/审查记录 | 未见额外风险 |
| `docs/audits/RQrun_WORKOUT_SYNC_REVIEW.md` | ?? | — | — | — | — | 是 | AUDIT_REPORT / 实现结果/审查记录 | 含Phase1之前继承的dirty内容 |
| `docs/evidence/per-workout-sync-acceptance.json` | ?? | — | — | — | — | 是 | EVIDENCE / 摘要/hash/视觉证据 | 含Phase1之前继承的dirty内容 |
| `docs/evidence/rqrun-phase1-2026-11-01.json` | ?? | 新 | — | — | — | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase2-2026-11-01.json` | ?? | — | 新 | — | — | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase2-failed-runs.txt` | ?? | — | 新 | — | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase3-2026-11-01.json` | ?? | — | — | 新 | — | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase3-device-2026-11-01.json` | ?? | — | — | 新 | — | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase3-logs/all.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/app-tests-result.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/boundaries.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/debug-result.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/openapi.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/race.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/release-result.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/signed-result.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/swift.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/targeted-postgres.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/unit.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/vet-tags.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-logs/vet.log` | !! | — | — | 新 | — | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | ignored；普通Git提交不会包含 |
| `docs/evidence/rqrun-phase3-workspace-baseline.json` | ?? | — | — | 新 | — | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase4-2026-11-01.json` | ?? | — | — | — | 新 | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase4-device-2026-11-01.json` | ?? | — | — | — | 新 | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase4-git-final.txt` | ?? | — | — | — | 新 | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/baseline_go.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/baseline_swift.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/boundaries.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/device_isolated_pg.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/fresh_gzip_n100.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/go_race_vet.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/gzip_identity_corruption.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/index_explain.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/ios_app_tests.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/ios_debug_signed.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/ios_release.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/openapi.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/pg_faults.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/phase1_3_full_postgres.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/phase4_n100.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/real_socket_deadline_boundaries.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/real_socket_pauses.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/real_socket_throttling.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/swift_all.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/swift_performance.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-logs/wire_gzip_resource_limits.txt` | ?? | — | — | — | 新 | 否 | TEST_LOG / 历史测试输出（非运行时依赖） | 未见额外风险 |
| `docs/evidence/rqrun-phase4-workspace-baseline.json` | ?? | — | — | — | 新 | 否 | EVIDENCE / 摘要/hash/视觉证据 | 未见额外风险 |
| `docs/evidence/workout-bundle-2026-10-31.json` | ?? | — | — | — | — | 是 | EVIDENCE / 摘要/hash/视觉证据 | 含Phase1之前继承的dirty内容 |
| `ios/RAI/App/BackgroundTransfers.swift` | M | — | 改 | 改 | 改 | 是 | PRODUCTION_CODE / 原生App/HealthKit/UI（含DEBUG验收hunk） | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `ios/RAI/App/HealthKitClient.swift` | M | — | — | 改 | — | 是 | PRODUCTION_CODE / 原生App/HealthKit/UI（含DEBUG验收hunk） | 含Phase1之前继承的dirty内容 |
| `ios/RAI/App/ProductModel.swift` | M | — | — | — | — | 是 | PRODUCTION_CODE / 原生App/HealthKit/UI（含DEBUG验收hunk） | 含Phase1之前继承的dirty内容 |
| `ios/RAI/App/ProductViews.swift` | M | — | — | — | — | 是 | PRODUCTION_CODE / 原生App/HealthKit/UI（含DEBUG验收hunk） | 含Phase1之前继承的dirty内容 |
| `ios/RAI/App/RAIApp.swift` | M | — | 改 | 改 | 改 | 是 | PRODUCTION_CODE / 原生App/HealthKit/UI（含DEBUG验收hunk） | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `ios/RAI/App/SyncCoordinator.swift` | M | 改 | 改 | 改 | 改 | 是 | PRODUCTION_CODE / 原生App/HealthKit/UI（含DEBUG验收hunk） | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `ios/RAI/AppTests/NormalizerTests.swift` | M | — | 改 | 改 | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `ios/RAI/Config/RAI.entitlements` | M | — | — | ? | — | 否 | DEPLOYMENT / 部署/签名/App构建配置 | OWNERSHIP_UNCERTAIN |
| `ios/RAI/README.md` | M | — | — | — | — | 是 | ARCHITECTURE_DOC / 架构/操作说明或原方案 | 含Phase1之前继承的dirty内容 |
| `ios/RAI/Sources/RAICompression/gzip.c` | M | — | — | — | 改 | 否 | PRODUCTION_CODE / 运行时源码 | 未见额外风险 |
| `ios/RAI/Sources/SyncCore/BatchBuilder.swift` | M | — | — | — | — | 是 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 含Phase1之前继承的dirty内容 |
| `ios/RAI/Sources/SyncCore/HealthDiscovery.swift` | ?? | — | — | 新 | 改 | 否 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 正式必需文件尚未tracked；跨阶段累计修改 |
| `ios/RAI/Sources/SyncCore/HealthIntegrity.swift` | ?? | — | — | 新 | — | 否 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 正式必需文件尚未tracked |
| `ios/RAI/Sources/SyncCore/OutboxStore.swift` | M | — | 改 | 改 | 改 | 是 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `ios/RAI/Sources/SyncCore/RAIAPIClient.swift` | M | 改 | 改 | 改 | 改 | 是 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 含Phase1之前继承的dirty内容；跨阶段累计修改 |
| `ios/RAI/Sources/SyncCore/RouteBuilder.swift` | M | — | — | 改 | — | 是 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 含Phase1之前继承的dirty内容 |
| `ios/RAI/Sources/SyncCore/Wire.swift` | M | — | — | — | — | 是 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 含Phase1之前继承的dirty内容 |
| `ios/RAI/Sources/SyncCore/WorkoutBundle.swift` | ?? | 改 | — | 改 | 改 | 是 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 含Phase1之前继承的dirty内容；正式必需文件尚未tracked；跨阶段累计修改 |
| `ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift` | ?? | — | 新 | 改 | 改 | 否 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 正式必需文件尚未tracked；跨阶段累计修改 |
| `ios/RAI/Sources/SyncCore/WorkoutSync.swift` | ?? | — | 改 | 改 | 改 | 是 | PRODUCTION_CODE / iOS同步持久化/组包/发现 | 含Phase1之前继承的dirty内容；正式必需文件尚未tracked；跨阶段累计修改 |
| `ios/RAI/Tests/SyncCoreTests/APITests.swift` | M | 改 | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift` | ?? | — | — | 新 | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `ios/RAI/Tests/SyncCoreTests/HealthIntegrityTests.swift` | ?? | — | — | 新 | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `ios/RAI/Tests/SyncCoreTests/WorkoutBundleTests.swift` | ?? | — | — | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `ios/RAI/Tests/SyncCoreTests/WorkoutPerformanceTests.swift` | ?? | — | — | — | 新 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 未见额外风险 |
| `ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift` | ?? | — | 新 | 改 | — | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 跨阶段累计修改 |
| `ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift` | ?? | — | 新 | — | 改 | 否 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 跨阶段累计修改 |
| `ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift` | ?? | — | 改 | — | — | 是 | TEST_CODE / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `ios/RAI/Tools/SyncProbe/Probe.swift` | M | — | — | — | — | 是 | DEV_TOOL / 开发/测试/依赖工具 | 含Phase1之前继承的dirty内容 |
| `ios/RAI/UITests/ProductUITests.swift` | M | — | — | — | — | 是 | UI_TEST / 永久回归/合成或显式验收测试（非在线源码） | 含Phase1之前继承的dirty内容 |
| `scripts/rai-backup.py` | M | — | — | ? | 改 | 是 | OPS_SCRIPT / 显式离线维护工具 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |
| `scripts/rai-reset-v2-data.sh` | ?? | — | — | — | — | 是 | OPS_SCRIPT / 显式离线维护工具 | 含Phase1之前继承的dirty内容 |
| `scripts/rai-restore.py` | M | — | — | ? | 改 | 是 | OPS_SCRIPT / 显式离线维护工具 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |
| `scripts/test-monthly-restore.sh` | M | — | — | ? | 改 | 是 | OPS_SCRIPT / 显式离线维护工具 | 含Phase1之前继承的dirty内容；OWNERSHIP_UNCERTAIN |

### 独立阶段交付清单

同文件可出现于多个阶段；修改包含对入口untracked文件的增量，不表示首次创建。每项完整SHA、归属依据、置信度及提交建议在各phase数组。

#### Phase1

**新增生产代码**：无已证实增量。

**修改生产代码**：`apps/server/internal/storage/postgres/workout_bundles.go`（modified）；`apps/server/internal/syncing/workout_bundle.go`（modified）；`apps/server/internal/syncing/workout_delete.go`（modified）；`ios/RAI/App/SyncCoordinator.swift`（modified）；`ios/RAI/Sources/SyncCore/RAIAPIClient.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutBundle.swift`（modified）。

**新增测试**：`apps/server/integration/workout_safety_test.go`（added）。

**修改测试**：`apps/server/integration/p7_security_test.go`（modified）；`apps/server/integration/workout_bundle_test.go`（modified）；`ios/RAI/Tests/SyncCoreTests/APITests.swift`（modified）。

**Migration**：无已证实增量。

**Contract**：`contracts/CHANGELOG.md`（modified）；`contracts/openapi-v2.yaml`（modified）；`contracts/sync-v3.md`（modified）。

**Docs**：`docs/IMPLEMENTATION.md`（modified）；`docs/RQrun_PHASE1_RESULT.md`（added）。

**Evidence / Test logs**：`docs/evidence/rqrun-phase1-2026-11-01.json`（added）。

**Operational scripts / Dev tools / Deployment**：无已证实增量。


#### Phase2

**新增生产代码**：`apps/server/internal/syncing/bundle_decode.go`（added）；`ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift`（added）。

**修改生产代码**：`apps/server/internal/httpapi/server.go`（modified）；`apps/server/internal/syncing/protocol.go`（modified）；`ios/RAI/App/BackgroundTransfers.swift`（modified）；`ios/RAI/App/RAIApp.swift`（modified）；`ios/RAI/App/SyncCoordinator.swift`（modified）；`ios/RAI/Sources/SyncCore/OutboxStore.swift`（modified）；`ios/RAI/Sources/SyncCore/RAIAPIClient.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutSync.swift`（modified）。

**新增测试**：`apps/server/integration/workout_reliability_swift_test.go`（added）；`apps/server/integration/workout_reliability_test.go`（added）；`apps/server/internal/syncing/bundle_decode_test.go`（added）；`ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift`（added）；`ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift`（added）。

**修改测试**：`ios/RAI/AppTests/NormalizerTests.swift`（modified）；`ios/RAI/Tests/SyncCoreTests/WorkoutTaskTests.swift`（modified）。

**Migration**：无已证实增量。

**Contract**：`contracts/CHANGELOG.md`（modified）；`contracts/openapi-v2.yaml`（modified）；`contracts/sync-v3.md`（modified）。

**Docs**：`docs/IMPLEMENTATION.md`（modified）；`docs/audits/RQrun_PHASE2_RELIABILITY_REVIEW.md`（added）。

**Evidence / Test logs**：`docs/evidence/rqrun-phase2-2026-11-01.json`（added）；`docs/evidence/rqrun-phase2-failed-runs.txt`（added）。

**Operational scripts / Dev tools / Deployment**：无已证实增量。


#### Phase3

**新增生产代码**：`apps/server/internal/storage/postgres/bundle_integrity.go`（added）；`apps/server/internal/syncing/bundle_observation.go`（added）；`ios/RAI/Sources/SyncCore/HealthDiscovery.swift`（added）；`ios/RAI/Sources/SyncCore/HealthIntegrity.swift`（added）。

**修改生产代码**：`apps/server/internal/httpapi/server.go`（modified）；`apps/server/internal/storage/postgres/analysis_input.go`（modified）；`apps/server/internal/storage/postgres/workout_bundles.go`（modified）；`apps/server/internal/syncing/bundle_decode.go`（modified）；`apps/server/internal/syncing/protocol.go`（modified）；`apps/server/internal/syncing/workout_bundle.go`（modified）；`ios/RAI/App/BackgroundTransfers.swift`（modified）；`ios/RAI/App/HealthKitClient.swift`（modified）；`ios/RAI/App/RAIApp.swift`（modified）；`ios/RAI/App/SyncCoordinator.swift`（modified）；`ios/RAI/Sources/SyncCore/OutboxStore.swift`（modified）；`ios/RAI/Sources/SyncCore/RAIAPIClient.swift`（modified）；`ios/RAI/Sources/SyncCore/RouteBuilder.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutBundle.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutSync.swift`（modified）。

**新增测试**：`apps/server/integration/workout_integrity_test.go`（added）；`apps/server/internal/syncing/bundle_integrity_test.go`（added）；`ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift`（added）；`ios/RAI/Tests/SyncCoreTests/HealthIntegrityTests.swift`（added）。

**修改测试**：`apps/server/integration/p7_security_test.go`（modified）；`apps/server/integration/workout_reliability_swift_test.go`（modified）；`apps/server/integration/workout_safety_test.go`（modified）；`ios/RAI/AppTests/NormalizerTests.swift`（modified）；`ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift`（modified）。

**Migration**：`apps/server/migrations/00016_route_timestamp_envelope.sql`（added）；`apps/server/migrations/checksums.json`（modified）。

**Contract**：`contracts/CHANGELOG.md`（modified）；`contracts/openapi-v2.yaml`（modified）；`contracts/sync-v3.md`（modified）。

**Docs**：`docs/IMPLEMENTATION.md`（modified）；`docs/audits/RQrun_PHASE3_DATA_INTEGRITY_REVIEW.md`（added）。

**Evidence / Test logs**：`docs/evidence/rqrun-phase3-2026-11-01.json`（added）；`docs/evidence/rqrun-phase3-device-2026-11-01.json`（added）；`docs/evidence/rqrun-phase3-logs/all.log`（added）；`docs/evidence/rqrun-phase3-logs/app-tests-result.log`（added）；`docs/evidence/rqrun-phase3-logs/boundaries.log`（added）；`docs/evidence/rqrun-phase3-logs/debug-result.log`（added）；`docs/evidence/rqrun-phase3-logs/openapi.log`（added）；`docs/evidence/rqrun-phase3-logs/race.log`（added）；`docs/evidence/rqrun-phase3-logs/release-result.log`（added）；`docs/evidence/rqrun-phase3-logs/signed-result.log`（added）；`docs/evidence/rqrun-phase3-logs/swift.log`（added）；`docs/evidence/rqrun-phase3-logs/targeted-postgres.log`（added）；`docs/evidence/rqrun-phase3-logs/unit.log`（added）；`docs/evidence/rqrun-phase3-logs/vet-tags.log`（added）；`docs/evidence/rqrun-phase3-logs/vet.log`（added）；`docs/evidence/rqrun-phase3-workspace-baseline.json`（added）。

**Operational scripts / Dev tools / Deployment**：无已证实增量。

**OWNERSHIP_UNCERTAIN（未计入已证实增量）**：`apps/server/cmd/rai/main.go`；`apps/server/internal/admin/operations.go`；`apps/server/internal/storage/postgres/store.go`；`docs/ARCHITECTURE.md`；`deploy/postgres/roles.sql`；`ios/RAI/Config/RAI.entitlements`；`scripts/rai-backup.py`；`scripts/rai-restore.py`；`scripts/test-monthly-restore.sh`。


#### Phase4

**新增生产代码**：`apps/server/internal/httpapi/bundle_body.go`（added）；`apps/server/internal/httpapi/timeouts.go`（added）；`apps/server/internal/syncing/phase_timing.go`（added）。

**修改生产代码**：`apps/server/cmd/rai/main.go`（modified）；`apps/server/internal/admin/operations.go`（modified）；`apps/server/internal/httpapi/server.go`（modified）；`apps/server/internal/jobs/worker.go`（modified）；`apps/server/internal/storage/postgres/analysis_input.go`（modified）；`apps/server/internal/storage/postgres/analysis_publish.go`（modified）；`apps/server/internal/storage/postgres/store.go`（modified）；`apps/server/internal/storage/postgres/workout_bundles.go`（modified）；`apps/server/internal/syncing/bundle_decode.go`（modified）；`ios/RAI/App/BackgroundTransfers.swift`（modified）；`ios/RAI/App/RAIApp.swift`（modified）；`ios/RAI/App/SyncCoordinator.swift`（modified）；`ios/RAI/Sources/RAICompression/gzip.c`（modified）；`ios/RAI/Sources/SyncCore/HealthDiscovery.swift`（modified）；`ios/RAI/Sources/SyncCore/OutboxStore.swift`（modified）；`ios/RAI/Sources/SyncCore/RAIAPIClient.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutBundle.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift`（modified）；`ios/RAI/Sources/SyncCore/WorkoutSync.swift`（modified）。

**新增测试**：`apps/server/integration/workout_device_acceptance_test.go`（added）；`apps/server/integration/workout_performance_fault_test.go`（added）；`apps/server/integration/workout_performance_test.go`（added）；`apps/server/integration/workout_transport_test.go`（added）；`apps/server/internal/httpapi/bundle_body_test.go`（added）；`apps/server/internal/httpapi/bundle_timeout_test.go`（added）；`ios/RAI/Tests/SyncCoreTests/WorkoutPerformanceTests.swift`（added）。

**修改测试**：`apps/server/integration/p7_security_test.go`（modified）；`ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift`（modified）。

**Migration**：`apps/server/migrations/00017_analysis_sample_lookup.sql`（added）；`apps/server/migrations/checksums.json`（modified）。

**Contract**：`contracts/CHANGELOG.md`（modified）；`contracts/openapi-v2.yaml`（modified）；`contracts/sync-v3.md`（modified）。

**Docs**：`docs/IMPLEMENTATION.md`（modified）；`docs/audits/RQrun_PHASE4_PERFORMANCE_REVIEW.md`（added）。

**Evidence / Test logs**：`docs/evidence/rqrun-phase4-2026-11-01.json`（added）；`docs/evidence/rqrun-phase4-device-2026-11-01.json`（added）；`docs/evidence/rqrun-phase4-git-final.txt`（added）；`docs/evidence/rqrun-phase4-logs/baseline_go.txt`（added）；`docs/evidence/rqrun-phase4-logs/baseline_swift.txt`（added）；`docs/evidence/rqrun-phase4-logs/boundaries.txt`（added）；`docs/evidence/rqrun-phase4-logs/device_isolated_pg.txt`（added）；`docs/evidence/rqrun-phase4-logs/fresh_gzip_n100.txt`（added）；`docs/evidence/rqrun-phase4-logs/go_race_vet.txt`（added）；`docs/evidence/rqrun-phase4-logs/gzip_identity_corruption.txt`（added）；`docs/evidence/rqrun-phase4-logs/index_explain.txt`（added）；`docs/evidence/rqrun-phase4-logs/ios_app_tests.txt`（added）；`docs/evidence/rqrun-phase4-logs/ios_debug_signed.txt`（added）；`docs/evidence/rqrun-phase4-logs/ios_release.txt`（added）；`docs/evidence/rqrun-phase4-logs/openapi.txt`（added）；`docs/evidence/rqrun-phase4-logs/pg_faults.txt`（added）；`docs/evidence/rqrun-phase4-logs/phase1_3_full_postgres.txt`（added）；`docs/evidence/rqrun-phase4-logs/phase4_n100.txt`（added）；`docs/evidence/rqrun-phase4-logs/real_socket_deadline_boundaries.txt`（added）；`docs/evidence/rqrun-phase4-logs/real_socket_pauses.txt`（added）；`docs/evidence/rqrun-phase4-logs/real_socket_throttling.txt`（added）；`docs/evidence/rqrun-phase4-logs/swift_all.txt`（added）；`docs/evidence/rqrun-phase4-logs/swift_performance.txt`（added）；`docs/evidence/rqrun-phase4-logs/wire_gzip_resource_limits.txt`（added）；`docs/evidence/rqrun-phase4-workspace-baseline.json`（added）。

**Operational scripts / Dev tools / Deployment**：`scripts/rai-backup.py`（modified）；`scripts/rai-restore.py`（modified）；`scripts/test-monthly-restore.sh`（modified）。

## 4. Migration Audit

源码目标schema **16→17**，WorkoutBundle protocolVersion **仍为3**。main版本、Ready、HTTP handshake、Go bundle校验、Swift bundleFields和合同一致。syncing.ProtocolVersion=2及Scope.fields=2仍供旧内部DTO使用，不能误报v3降级。本轮不查询数据库，不声称运行实例已安装17。

| Migration | File SHA-256 | Registry SHA-256 | Match | Phase |
| --- | --- | --- | --- | --- |
| `00001_core.sql` | ef910464c6ecef03d3bd0d06ea1b6194139a4330bfbf75a22aa0ccd11a40bcfe | ef910464c6ecef03d3bd0d06ea1b6194139a4330bfbf75a22aa0ccd11a40bcfe | YES | historical_inherited |
| `00002_dirty_generations.sql` | da8158c9e6e0088bfeb82db1448ca1245f9cb0e7ee352e0c43dd590571d98a23 | da8158c9e6e0088bfeb82db1448ca1245f9cb0e7ee352e0c43dd590571d98a23 | YES | historical_inherited |
| `00003_workouts.sql` | de7c8f7ade19a2dca20d7c8c378606f927dd688e3f10f3ebe8c731f670a9c880 | de7c8f7ade19a2dca20d7c8c378606f927dd688e3f10f3ebe8c731f670a9c880 | YES | historical_inherited |
| `00004_page_receipts.sql` | 8adcc0a69f746ad77b30aae56b0fb852f0ae0e69f4e89b32c36be5e18e350d75 | 8adcc0a69f746ad77b30aae56b0fb852f0ae0e69f4e89b32c36be5e18e350d75 | YES | historical_inherited |
| `00005_request_budgets.sql` | 513f3b4e2a5912449fd21d10c9698b35a176d543fe1f6674ef643c9c6d3449b0 | 513f3b4e2a5912449fd21d10c9698b35a176d543fe1f6674ef643c9c6d3449b0 | YES | historical_inherited |
| `00006_upload_slots.sql` | cd87bedbe30c3fbff609dff947b95f7fc7f8162f5b84444474902a9744d844d5 | cd87bedbe30c3fbff609dff947b95f7fc7f8162f5b84444474902a9744d844d5 | YES | historical_inherited |
| `00007_workout_activities.sql` | 52edb86ce7bce7b8e25c7212963f89c6aaaa8f3cc90bf433c2c32ca6d95387e6 | 52edb86ce7bce7b8e25c7212963f89c6aaaa8f3cc90bf433c2c32ca6d95387e6 | YES | historical_inherited |
| `00008_quantity_series.sql` | d05dc97c889161553171a36dac632c2fae4c2260b3b7f68709a2de9f5bdfc60d | d05dc97c889161553171a36dac632c2fae4c2260b3b7f68709a2de9f5bdfc60d | YES | historical_inherited |
| `00009_workout_packages.sql` | fa901e84cf2d8f55381e5f3334a23db9b6c8430f305fb5bb32b07d0b33cfabce | fa901e84cf2d8f55381e5f3334a23db9b6c8430f305fb5bb32b07d0b33cfabce | YES | historical_inherited |
| `00010_route_foundation.sql` | c4669f4f90907e673da9f1cddb7dfa88c116dc306f301653aeb65b506680ba56 | c4669f4f90907e673da9f1cddb7dfa88c116dc306f301653aeb65b506680ba56 | YES | historical_inherited |
| `00011_p3_sync_v2.sql` | 398ad10d1e49fbaaa6ddb3133459274a450d129e6eb9c4e56ffc196d29c897f4 | 398ad10d1e49fbaaa6ddb3133459274a450d129e6eb9c4e56ffc196d29c897f4 | YES | historical_inherited |
| `00012_analysis.sql` | 4cd795926d1b9ba086fb877a7b1cc46ae84c6f38c2e1dca86fc212ac8bea1a5f | 4cd795926d1b9ba086fb877a7b1cc46ae84c6f38c2e1dca86fc212ac8bea1a5f | YES | historical_inherited |
| `00013_training.sql` | ce49227ee8ff0257b1433944bdd14e306b4c710de9b3d00c74a634973f0d618b | ce49227ee8ff0257b1433944bdd14e306b4c710de9b3d00c74a634973f0d618b | YES | historical_inherited |
| `00014_workout_imports.sql` | d26a4eddf3b989be79a4fdd01f927ab331a956c5bc96c3dffea1716657937ad8 | d26a4eddf3b989be79a4fdd01f927ab331a956c5bc96c3dffea1716657937ad8 | YES | historical_inherited |
| `00015_workout_import_history.sql` | 92790aae8ad2deaea02b4fdd7eed18f916c1b38cc2b4a13cdcc80a4f57aadee6 | 92790aae8ad2deaea02b4fdd7eed18f916c1b38cc2b4a13cdcc80a4f57aadee6 | YES | historical_inherited |
| `00016_route_timestamp_envelope.sql` | d35999abca59ceb4e2359707bb78fe51cab3ae8af270f6c33c8301278bcac4c8 | d35999abca59ceb4e2359707bb78fe51cab3ae8af270f6c33c8301278bcac4c8 | YES | phase3 |
| `00017_analysis_sample_lookup.sql` | 73bc3b4a1281597e777727c96521bf16a692f8f8968ce09d4d175d1470cbb968 | 73bc3b4a1281597e777727c96521bf16a692f8f8968ce09d4d175d1470cbb968 | YES | phase4 |

无缺号、重复号、missing SQL或孤立registry项。00001～00013与HEAD字节一致；00014/15匹配Phase1保存SHA；00016匹配Phase3 final和P4入口；00001～00016全部匹配P4入口。Phase4仅追加00017登记项。

00017存在，Git=??，编号17、顺序在00016之后，SHA=`73bc3b4a1281597e777727c96521bf16a692f8f8968ce09d4d175d1470cbb968`。

| 属性 | 实际SQL |
| --- | --- |
| index / table | health_samples_analysis_identity / health_samples |
| method / UNIQUE | 默认btree / non-unique |
| keys | dataset_id, sample_uuid, sample_type |
| INCLUDE | event_start, event_end, value_numeric, unit, content_sha256 |
| predicate | 无WHERE |
| sort order | 各key默认ASC NULLS LAST |
| tablespace | 未指定，数据库默认 |
| concurrent | 无CONCURRENTLY |
| Up | 仅CREATE INDEX，无事实表DML/事实语义变更；既有PK和时间索引保留 |
| DROP / TRUNCATE / DELETE | Up均无；Down包含DROP INDEX health_samples_analysis_identity |
| Down授权 | admin.reversibleMigrations为空，CLI不批准17回退 |
| 幂等 | 裸SQL非IF NOT EXISTS；Goose版本记录+withMigrationLock串行执行，重复migrate按版本跳过 |

生产风险：普通CREATE INDEX会阻止health_samples并发写入，并可能等待长事务；消耗I/O、额外磁盘与WAL。Up没有逻辑删事实；Down确实删除schema对象。历史约6MB合成索引观测不能外推生产规模耗时。符合现有事务型Goose框架，本轮未改SQL或执行数据库验证。

## 5. Phase Integrity

P3 final→P4 entry：**160/160一致**。P4入口683文件无缺失，29个既有文件变化均登记于modified_existing_sha256；新增37个Git可见文件（36个hash登记+主JSON自身例外）。P4最终65/65登记hash匹配当前。P1/P2/P3报告和主evidence仍匹配P4入口。

P4证据的protected_phase1_3_and_migrations_1_16_unchanged=true若解释为“所有P1～3源码未变”则不成立：160项中实际27项经P4修改；另有worker.go和gzip.c不在这160项但在入口基线。只能限定解释为保护的安全语义/证据与迁移。以下以实际hash为准。

下表hash显示前12位，完整64位三方与current见manifest.phase_integrity；P4没有单独登记final的文件以current为final-observed，并保留recorded_final=null，不伪造历史记录。

| 文件 | P3 final | P4 entry | P4 final-observed | 判定 |
| --- | --- | --- | --- | --- |
| `apps/server/integration/p7_security_test.go` | 42f8481dea5e | 42f8481dea5e | 99d65e4c934b | EXPECTED_MODIFICATION |
| `apps/server/integration/workout_bundle_test.go` | d898941bbae3 | d898941bbae3 | d898941bbae3 | UNCHANGED |
| `apps/server/integration/workout_integrity_test.go` | dae38d012168 | dae38d012168 | dae38d012168 | UNCHANGED |
| `apps/server/integration/workout_reliability_swift_test.go` | a76c029dfd92 | a76c029dfd92 | a76c029dfd92 | UNCHANGED |
| `apps/server/integration/workout_reliability_test.go` | 3132babbc00c | 3132babbc00c | 3132babbc00c | UNCHANGED |
| `apps/server/integration/workout_safety_test.go` | 60d55094e25f | 60d55094e25f | 60d55094e25f | UNCHANGED |
| `apps/server/internal/storage/postgres/analysis_input.go` | 4e5833d30e9c | 4e5833d30e9c | 9bfae58d7e93 | EXPECTED_MODIFICATION |
| `apps/server/internal/storage/postgres/bundle_integrity.go` | 7d97031bd790 | 7d97031bd790 | 7d97031bd790 | UNCHANGED |
| `apps/server/internal/storage/postgres/workout_bundles.go` | 859d2c8838ed | 859d2c8838ed | 57eb4512888c | EXPECTED_MODIFICATION |
| `apps/server/internal/syncing/bundle_decode.go` | 486b689981cd | 486b689981cd | 96141dd9616f | EXPECTED_MODIFICATION |
| `apps/server/internal/syncing/bundle_decode_test.go` | c85430bc36ba | c85430bc36ba | c85430bc36ba | UNCHANGED |
| `apps/server/internal/syncing/bundle_integrity_test.go` | 98d6f3407bd4 | 98d6f3407bd4 | 98d6f3407bd4 | UNCHANGED |
| `apps/server/internal/syncing/bundle_observation.go` | 82380ca4ea43 | 82380ca4ea43 | 82380ca4ea43 | UNCHANGED |
| `apps/server/internal/syncing/workout_bundle_test.go` | 24fa47ba0be7 | 24fa47ba0be7 | 24fa47ba0be7 | UNCHANGED |
| `apps/server/internal/syncing/workout_delete.go` | 12c7e5822c08 | 12c7e5822c08 | 12c7e5822c08 | UNCHANGED |
| `apps/server/migrations/00016_route_timestamp_envelope.sql` | d35999abca59 | d35999abca59 | d35999abca59 | UNCHANGED |
| `ios/RAI/App/HealthKitClient.swift` | e5dc5fa87eee | e5dc5fa87eee | e5dc5fa87eee | UNCHANGED |
| `ios/RAI/App/RAIApp.swift` | 2870c650db4c | 2870c650db4c | 6c0cbbdb5d9a | EXPECTED_MODIFICATION |
| `ios/RAI/App/SyncCoordinator.swift` | a30b92e1ad68 | a30b92e1ad68 | 0ee661fd6ffe | EXPECTED_MODIFICATION |
| `ios/RAI/Sources/SyncCore/HealthDiscovery.swift` | 1809de0c2f12 | 1809de0c2f12 | a89f168cd7f1 | EXPECTED_MODIFICATION |
| `ios/RAI/Sources/SyncCore/HealthIntegrity.swift` | 30d5cd603292 | 30d5cd603292 | 30d5cd603292 | UNCHANGED |
| `ios/RAI/Sources/SyncCore/OutboxStore.swift` | bbe639ed8447 | bbe639ed8447 | 533f8a2abc50 | EXPECTED_MODIFICATION |
| `ios/RAI/Sources/SyncCore/RAIAPIClient.swift` | 13a3ad930ec0 | 13a3ad930ec0 | 7279108042fe | EXPECTED_MODIFICATION |
| `ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift` | 47e37798c475 | 47e37798c475 | 1d4c253d158e | EXPECTED_MODIFICATION |
| `ios/RAI/Tests/SyncCoreTests/HealthDiscoveryTests.swift` | 3f9bc59407af | 3f9bc59407af | 3f9bc59407af | UNCHANGED |
| `ios/RAI/Tests/SyncCoreTests/HealthIntegrityTests.swift` | 033e3b0948a3 | 033e3b0948a3 | 033e3b0948a3 | UNCHANGED |
| `ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityLiveTests.swift` | fcea5cd45bdd | fcea5cd45bdd | fcea5cd45bdd | UNCHANGED |
| `ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift` | 3ea9bc4f7f76 | 3ea9bc4f7f76 | 015baa75d165 | EXPECTED_MODIFICATION |

### 语义审查

已阅读当前核心源码和可用累计HEAD diff。**HEAD diff不是P4 diff**；untracked文件在HEAD不存在。缺入口源码时无法严格逐hunk比较P4前后，以下为当前约束、固定测试及历史SHA的受限审查。

- **EXPECTED_MODIFICATION / SAFE_REFACTOR（静态受限判断）**：`apps/server/internal/storage/postgres/workout_bundles.go`。dataset行锁/fencing先于屏障及回执；先检查parent/sample/route tombstone再重放；快照缺席不生成删除；Sample身份仍含原时间、单位、float8位模式和source；事实/关联/dirty/job/change/import receipt同一tx，成功COMMIT后ACK。批量pgx/计时与45s预算符合P4目标。DELETE仍保留原范围、rai_analysis_dirty和稳定回执。没有P4入口源码副本，不能证明每条被替换语句等价。

- **EXPECTED_MODIFICATION / SAFE_REFACTOR（当前约束保留）**：`apps/server/internal/syncing/bundle_decode.go`。词法/类型/存在性扫描不构造generic整树；未知字段、重复key、null、缺失required、整数类型、trailing JSON、UTF8与数组点数限制仍拒绝，之后typed decode+值/成员校验。原P2 bundle_decode_test.go字节未变，P3完整性测试字节未变。未见放宽DTO；独立P4源码diff无法重建。

- **EXPECTED_MODIFICATION / 传输派生与原包生命周期仍分离**：`ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift`、`ios/RAI/Sources/SyncCore/OutboxStore.swift`。先核验原multipart和JSON hash；gzip派生文件另存hash，重启校验损坏后重建；ACK仍校验ID/原JSON hash/rev/count/job；GET404保留失败及原包；receipt+GC授权同state提交，回收检查仍引用文件。P4清空继承wire引用避免successor复用旧传输，未观察绕过ACK。

- **EXPECTED_MODIFICATION / 超时与响应完整性**：`ios/RAI/Sources/SyncCore/RAIAPIClient.swift`、`ios/RAI/App/BackgroundTransfers.swift`。bundle POST270s、foreground resource300s、background request270s/resource3600s；严格200/application-json和DELETE ACK检查仍在；身份和退避流程保留。累计HEAD diff还包含P1/P2修改，不能都归P4。

- **EXPECTED_MODIFICATION / gzip封装**：`ios/RAI/Sources/SyncCore/WorkoutBundle.swift`、`ios/RAI/Sources/SyncCore/WorkoutSync.swift`、`ios/RAI/Sources/RAICompression/gzip.c`。可选gzip multipart媒体类型/文件名；bundleWireFile/hash为派生字段；固定header time=0/os=255/level6。原样JSON身份不改，protocol3不改。

- **EXPECTED_MODIFICATION / 原包/代际与传输字段衔接**：`ios/RAI/Sources/SyncCore/HealthDiscovery.swift`、`ios/RAI/App/SyncCoordinator.swift`。stage journal先于promote anchor；仍要求previous anchor相等和档案校验；park原未ACK bundle于retainedBundles，successor清除wire引用。覆盖/来源/route算法文件HealthIntegrity和HealthKitClient与P4入口一致；未见nil-anchor重扫或把coverage用于删除。

- **EXPECTED_MODIFICATION / DEBUG验收分支与计时**：`ios/RAI/App/RAIApp.swift`。DEBUG单Workout验收入口生成脱敏report、读取Documents中的临时验收config，未读取docs/evidence；Release由#if DEBUG排除。不能因为文件包含验收代码就把整个App文件归DEV_TOOL，也不能视为生产依赖仓库证据。

- **EXPECTED_MODIFICATION / 传输边界**：`apps/server/internal/httpapi/server.go`、`apps/server/internal/httpapi/bundle_body.go`、`apps/server/internal/httpapi/timeouts.go`。auth/Admit仍先于handler/body；有限body/processing/ACK deadline；单part/压缩与解码限额；成功Store返回后才ACK。P4前已有路由退役不属于本阶段删除。

- **EXPECTED_MODIFICATION / 预算/计时**：`apps/server/internal/storage/postgres/analysis_input.go`、`apps/server/internal/storage/postgres/analysis_publish.go`、`apps/server/internal/jobs/worker.go`。累计HEAD diff新增source/provenance校验属于P3；P4 20s snapshot/5s statement、10s publication与计时。AnalysisSamplesSQL排序仍sample_type,event_start,sample_uuid；route sequence保留；generation CAS与实际Analyze/Publish未被跳过。

- **EXPECTED_MODIFICATION / 版本断言**：`apps/server/integration/p7_security_test.go`。当前schema17和46表断言；Phase1受限LOGIN删除屏障/失效测试仍存在。当前hash与P4登记一致；无法取得P4前源内容逐hunk对照，不能把整份diff视作P4新测。

- **EXPECTED_MODIFICATION / 版本/入口组装**：`apps/server/cmd/rai/main.go`、`apps/server/internal/admin/operations.go`、`apps/server/internal/storage/postgres/store.go`、`scripts/rai-backup.py`、`scripts/rai-restore.py`、`scripts/test-monthly-restore.sh`。schema17版本引用、备份schema允许集与表数量；main使用共享NewHTTPServer。管理工具未进入online imports；本轮未运行这些脚本。

- **EXPECTED_MODIFICATION / 已知测试名/断言保留**：`ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift`。P3最终hash等于P2最终hash；当前保留Phase2报告列出的10个方法及28种invalid-ACK参数，新增gzip损坏/固定header用例，无skip/disabled。无P4入口源码副本，不能逐字符证明每一旧断言从未调整；未见放宽安全断言。

未发现可确认的删除安全检查、绕过ACK、改变DELETE fencing、nil-anchor重扫、coverage缺席删除、Route点序/原时间语义回归。HealthKitClient、HealthIntegrity、bundle_observation、bundle_integrity、00016及主要P1～3 Go回归相对P4入口字节相同。hash相同不等价测试已运行，静态源码不证明全部并发/真机行为。

## 6. Test Coverage Migration

按HEAD和当前顶层func Test名称集合计算，删除**13个顶层测试**，不把t.Run参数当顶层。7文件全部匹配P3 final/P4 entry，修复前review已列dirty，不能归责P4。

| 旧测试文件 | 删除数量 | 替代测试 | 关键场景是否保留 |
| --- | --- | --- | --- |
| `apps/server/integration/admission_test.go` | 2 | TestBundleRejectsBeforeReadingBodyWhenDeviceSlotBusy；TestAdmissionDurableAtomicBudgets / SharedAcrossConcurrentPools / FailureAndCancellationRollBackBothBuckets | PARTIAL；同进程busy时body零读取保留（unit）；真实持久限流、独立read lane在Store保留。v3完整HTTP链对rate/storage failure先于body、未授权不分配bucket、成功/取消后槽释放的组合断言未完整替代。 |
| `apps/server/integration/p3_sync_test.go` | 2 | TestWorkoutBundleOneRequestAtomicImport；TestHTTPWorkoutBundleStatusByHashAndExplicitDelete；TestPhase3MetadataProvenanceCoverageAppendHistoryAndImmutableCore；Swift lost_ack harness | REPLACED / RETIRED；独立route/association/enrichment HTTP退役。bundle原回执、来源历史、route seal事实与点序有对应断言；独立route seal响应丢失不再适用。 |
| `apps/server/integration/page_test.go` | 1 | TestWorkoutBundleLostACKAnd404ThenIdempotentPOST；TestWorkoutReliabilitySwiftClientPostgres(lost_ack,commit_404) | REPLACED / RETIRED；page收据HTTP已退役；新包hash恢复+重POST不重复事实/job替代。新GET的dataset隔离和撤销由P7保留；并非逐条复制旧page错误矩阵。 |
| `apps/server/integration/series_test.go` | 1 | TestWorkoutBundleOneRequestAtomicImport；TestPhase4PayloadVariantWorkers(many_series)；保留的Store series tests | REPLACED / RETIRED；series register/chunk/seal独立HTTP退役；父子Sample、多值series事务与sealed投影仍覆盖。 |
| `apps/server/integration/sync_test.go` | 2 | TestWorkoutBundleHTTPMultipartAndStatus；TestWorkoutBundleCommitFailureHasNoACK；TestWorkoutBundleLostACKAnd404ThenIdempotentPOST；真实Swift harness；TestP7RealLoginBoundariesAndRevocation | REPLACED；v2批次上传和GET回执替换为v3 bundle ACK。真实TCP丢响应由跨语言harness覆盖，单独Go测试只是丢弃已返回ACK，二者证据区分。 |
| `apps/server/integration/upload_test.go` | 4 | TestBundleTimeoutAndCancellationClassification；TestBundleACKWriteDeadlineAfterCommit；保留TestUploadLeaseSharedDurableAndFenced / DatasetCapacityAndIsolation / LossRollsBackEntireFactTransaction | PARTIAL / RETIRED；跨handler lease获取/替换/释放失败场景对旧入口退役；其Store算法仍有测试，AcquireUpload故障/取消专项被删除且未见同名等价替代。v3使用进程槽而非DB upload lease，不能说跨实例语义被保持；新cancel测试断言Import=0，未断言同设备下次上传成功。 |
| `apps/server/integration/workout_package_test.go` | 1 | TestWorkoutBundleOneRequestAtomicImport；TestWorkoutBundleLateSampleAndConflicts；保留workout package/activities Store tests | REPLACED / RETIRED；独立package register/chunk/seal入口退役；Workout detail/activities内嵌包的原子提交替代。 |

完整被删测试名称在manifest.test_coverage_migration。server.New已移除stream/batch/page/series/package/route/enrichment独立写入口；新bundle测试明确断言旧association入口404。因此删除属于协议入口退役，不只是冗余清理。

仍有覆盖缺口：Store持久预算、busy-device零读取unit与恢复测试分别保留，但v3 HTTP“真实持久预算/storage失败先于body、未授权不分配桶、取消后同设备重入释放槽”的组合断言未完整替代。bundle入口不调用AcquireUpload/ReleaseUpload，跨实例durable lease是旧路径语义，不能用保留Store测试证明v3跨实例容量。变化早于P4，应在交付评审说明适用范围，不宣称每条旧安全反例等价保留。

未见P4删这些旧测试或将P1～3核心回归改成skip的证据。P4 opt-in socket/performance/device及Swift live XCTest仍显式skip，普通套件成功不证明专跑执行。未因新测试很多自动批准删测，也未推断作者删测是为让套件通过。

## 7. Evidence Isolation

静态Go生产local import图从httpapi/syncing/jobs/storage/postgres到9个本地包，无admin/migrations/goose；analysis/training直接依赖无os/net/DB。保守纳入各build-tag源码，未运行go list/新边界门禁，不把它称为重跑249包检查。cmd/rai维护模式导入admin属于同镜像显式CLI，非httpapi依赖管理工具。

生产Go/Swift/C/SQL未命中docs/evidence、docs/audits、rqrun-phase*、/tmp/rai-*、golden evidence或test logs路径。Go embed只嵌SQL/checksums；Docker最终scratch只COPY二进制和CA；iOS Xcode/SwiftPM资源未打包仓库evidence，Fixtures仅属测试target。deploy/local/README提输出evidence属于开发说明，不是部署读测试日志。未发现P0 DELIVERY CONTAMINATION。

RAIApp的#if DEBUG单Workout验收读取设备Documents临时config并生成脱敏report，未读仓库evidence，Release不包含该分支。它是production文件中的开发验收hunk，不把整个RAIApp误判临时文件。

| 对象 | 建议 | 理由 |
| --- | --- | --- |
| 阶段主JSON/baseline/migration hashes/duration/EXPLAIN/device摘要/报告 | KEEP | 永久摘要；无运行时依赖 |
| P4 21个精简.txt / P3 13个.log / P2失败摘录 | KEEP_OPTIONAL | 可复核且hash登记；P3目前被*.log忽略 |
| raw xcodebuild/go test/PG日志、DerivedData、xcresult、重复dump、个人Xcode状态 | DO_NOT_COMMIT | 不建议新增长期交付；本轮未删，已tracked历史不自动清理 |
| 历史设备/网络证据、不确定配置、缺关键行的精简日志 | REVIEW | 隐私/归属/证据强度须交代 |

P1 7个、P2 15个/tmp成功日志均不存在：属于历史临时证据不可用，不是生产源码丢失。P2 failed-runs存在且hash匹配，保留失败事实。P3 13个日志全匹配，但均ignored；P4 21个登记日志全存在且hash匹配。

P4两个Swift performance日志相同66B，仅Swift Testing 0 tests行。XCTest和Swift Testing不同，此行不能证明XCTest未跑；同时缺XCTest执行和PHASE4_SWIFT输出，不能独立证实主JSON五档数值。openapi.txt仅1B换行；Debug/Release日志各22B同一BUILD SUCCEEDED行，不能独立区分设置/签名。主JSON保留命令、数值和报告结论，本轮不补造丢失原始记录。

### .gitignore（只读）

根ignore覆盖.env、*.log、tmp/.local/secrets/bin/dist/coverage；ios/RAI/.gitignore覆盖.build/.swiftpm/DerivedData/*.xcresult、Developer.xcconfig和xcuserdata。check-ignore验证这些产物已忽略。任意test-temp、DeviceAcceptance.xcconfig、signing.p12未通配忽略，目前未见其进入候选交付。建议后续按真实输出路径补局部凭证/设备配置规则，明确永久精简日志白名单或摘要策略；不盲目放开所有.log。本轮未修改。

## 8. Secret / Privacy Scan

298个源码/合同/脚本/部署文本的private-key/JWT/长credential-literal扫描未命中；150个既有evidence/audit文本候选按上下文核对，仅报告路径/类型。198个历史图片/视频/二进制未作内容隐私审查；模式扫描不是任意编码secret的完备证明。

| 状态 | 范围/路径 | 上下文 |
| --- | --- | --- |
| SAFE | Phase3/Phase4 device JSON | 仅计数/状态/型号/字段存在性/hash/耗时；无原始健康值/GPS/token/完整Workout或唯一设备UUID；型号不是唯一ID |
| SAFE | Phase2 failed-runs | 坐标为合成fixture源码摘录；ID为测试/模拟器 |
| SAFE | Phase3/4 baseline UUID命中 | 历史simulator/xcresult路径名，非真实Workout ID |
| REVIEW_REQUIRED | docs/evidence/device-backend-2026-10-31.json；device-acceptance-2026-10-30.json | 已tracked旧设备dataset/collector及本地网络信息，分享前复核；本报告不复印值 |
| REVIEW_REQUIRED | 其他历史evidence候选/媒体 | 合成SQL/模拟器UUID不自动算敏感，未完成全部历史媒体人工审查 |

| 状态 | 本地路径 | 类型 | 建议 |
| --- | --- | --- | --- |
| REVIEW_REQUIRED | `.local/run/fnos-device-credentials.txt` | local_connection_or_credential_candidate | ignored / DO_NOT_COMMIT |
| REVIEW_REQUIRED | `.local/run/device-connection.txt` | local_connection_or_credential_candidate | ignored / DO_NOT_COMMIT |
| SENSITIVE_FOUND | `.local/run/device-enrollment.json` | local_credential_config | ignored / DO_NOT_COMMIT |
| REVIEW_REQUIRED | `.local/run/device-connection-fnos.txt` | local_connection_or_credential_candidate | ignored / DO_NOT_COMMIT |
| REVIEW_REQUIRED | `ios/RAI/Config/Developer.xcconfig` | local_signing_config | ignored / DO_NOT_COMMIT |

本地配置只检查必要字段/路径，不输出值或交付凭证hash；未读取.local/pgdata、备份、iPhone原始健康档案。候选tracked/untracked新增文件未见p12/private key/provisioning/Keychain导出。ignored本地凭证存在不等于正在待提交，但不得强制纳入。

## 9. Recommended Commit Structure

**优先方案A：评审后的一个squash feature提交**，包含继承v3基线、P1～4及不可分migration/合同/安全回归。建议`feat(sync): implement workout-centric HealthKit sync v3`。脱敏文档/证据可另作`docs(sync): record workout sync v3 delivery evidence`。额外性能测试只有验证独立性后才另分test提交，不让feature缺关键回归。

方案B阶段拆分需要真实阶段源码快照、准确hunk和每个中间提交的独立验证；当前不推荐。不能用整文件git add构造虚假阶段边界。以下混合文件需HUNK-LEVEL SEPARATION REQUIRED，现有证据下建议DO NOT SPLIT：

| 文件 | P1 | P2 | P3 | P4 | 是否可拆 |
| --- | --- | --- | --- | --- | --- |
| `ios/RAI/App/SyncCoordinator.swift` | 改 | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `ios/RAI/App/RAIApp.swift` | — | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `ios/RAI/Sources/SyncCore/OutboxStore.swift` | — | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `ios/RAI/Sources/SyncCore/RAIAPIClient.swift` | 改 | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `ios/RAI/Sources/SyncCore/WorkoutSync.swift` | — | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `ios/RAI/Sources/SyncCore/WorkoutBundle.swift` | 改 | — | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `apps/server/internal/storage/postgres/workout_bundles.go` | 改 | — | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `apps/server/internal/httpapi/server.go` | — | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `apps/server/internal/storage/postgres/analysis_input.go` | — | — | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `contracts/sync-v3.md` | 改 | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `contracts/openapi-v2.yaml` | 改 | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |
| `docs/IMPLEMENTATION.md` | 改 | 改 | 改 | 改 | HUNK-LEVEL；目前DO NOT SPLIT |

FILE-LEVEL SEPARABLE示例：00016/00017 SQL、HealthIntegrity、新P4 transport/fault测试、阶段报告；只表示归属单一，不保证脱离依赖可形成可构建提交。checksums.json与migration集合DO NOT SPLIT；事务/DTO/ACK/Outbox默认整体冻结。完整文件建议在JSON；本轮无commit/add操作。

## 10. Full Delivery Manifest

[机器可读交付清单](../evidence/phase1_4_git_delivery_manifest.json)。包含737文件完整hash、阶段独立分类清单、继承/不确定项、三方SHA、17项migration、旧测试名称映射、日志核对、隐私候选路径、风险和Git原始输出。

两项新审计产物不加入自身入口快照；manifest登记报告SHA，自身递归hash省略。

## 11. Risks

- **R1 / P1 阶段补丁不可完全恢复**：Phase1/2 临时入口哈希基线已不可用；Phase3 初始670文件快照缺失，持久化的是续跑681文件。Phase4有683文件入口哈希但无源码副本。可以恢复文件级触达，不能恢复所有阶段独立patch或保证中间提交可构建。

- **R2 / P1 未纳入Git的生产交付**：84个untracked文件包含正式Go/Swift实现与00014～00017四个迁移；只提交tracked diff会漏掉必需实现。00014/15是Phase1之前继承，不能误记为Phase1新建。

- **R3 / P2 历史日志缺口与忽略规则**：Phase1的7个、Phase2的15个/tmp成功日志均已不可用；Phase3的13个日志实际存在且hash匹配，但被根目录*.log忽略。报告和机器摘要仍在，不能把临时日志不可用说成生产文件丢失。

- **R4 / P2 精简日志不足以独立复核全部测量**：Phase4 baseline_swift与swift_performance只有同一条Swift Testing 0 tests摘要，缺XCTest执行行和PHASE4_SWIFT测量；不能据此断言XCTest未运行，但也不能独立证实5档测量。openapi.txt只有换行，两个iOS build日志只有BUILD SUCCEEDED。总JSON保留数值/命令，记录级真实性仍依赖历史执行者。

- **R5 / P2 旧测试退役存在覆盖粒度缺口**：7个旧文件共删除13个顶层HTTP/lease测试，均在Phase4入口之前。旧stream/page/series/package入口已退役，v3事实/回执/删除有替代；真实持久限流在v3 HTTP读取body前失败、取消后重入释放槽的组合场景未见完整等价永久回归。分布式upload lease不再被bundle入口调用，不能用旧Store lease测试证明v3跨实例容量。未发现为Phase4通过而删除这些测试的证据。

- **R6 / P2 本地凭证与历史隐私边界**：ignored .local/run含凭证/连接配置，必须DO_NOT_COMMIT；当前待提交源码/阶段证据未检出有效secret。已tracked的较早设备证据含dataset/collector标识和本地网络信息，属于继承的REVIEW_REQUIRED，未在报告复印值。旧图片/视频未作OCR，不能声称所有历史二进制证据完成隐私审查。

- **R7 / P2 普通CREATE INDEX的部署风险**：00017 Up无事实DML，只建非并发索引；部署时会阻塞health_samples写入并增加I/O/WAL/磁盘。Down含DROP INDEX，但现有CLI rollback allowlist为空，不会授权回退17。本轮未连接数据库或执行迁移。

- **R8 / P2 实际验收剩余项**：真实手机断网/后台强杀、峰值内存、公网/TLS代理、真实4小时Workout、生产迁移均未获本轮独立验证。RQ-010历史升级/reset仍被历史报告列为延期项。

## 12. Final Recommendation

**当前不建议直接进入Phase 5。** 先完成交付评审与选择性commit/freeze；优先真实累计实现的squash，不人为制造四阶段独立历史。

后续获授权整理时应保留00014～00017与必需untracked源码，明确永久摘要/device证据清单及P3 ignored日志归档，复核历史设备隐私与admission/cancel覆盖缺口，按实际配置补.gitignore。raw logs清理可选，不能据本报告删除用户工作；凭证配置DO_NOT_COMMIT。本轮不执行这些操作。

提交前需在获准验证任务中运行受影响Go测试、真实隔离PG集成，以及go test -race ./...、go vet ./...、依赖边界检查；**本轮未重跑**。未验证真机/生产项继续保持未验证。

写入后复核：入口737文件SHA全部不变，HEAD与index SHA不变，staged仍为空；Git状态仅增加本报告和manifest两项。SQL查询源和App图片资源归PRODUCTION_CODE，不当作临时artifact。

停止于本报告与manifest：未add/commit/push/fetch/pull/reset/clean/checkout/restore/stash/rebase/merge/cherry-pick，未删untracked、未修复问题、未触碰../running、数据库或部署。
