# RAI V2 — RQrun Phase 1：Workout 数据安全与删除一致性修复

日期：2026-11-01（Asia/Shanghai）。范围仅 RQ-001、RQ-002、RQ-003；正式实现、自动化测试与协议更新。任务附件在 RQ-002 fencing 条目中截断，RQ-003 按任务首部目标、仓库审查报告及既有 P4 合同落实。

## 工作区保护

执行前重新运行了任务指定的五项 Git 检查。HEAD 为 `aeb769c82f6af6c47d7cac82874f48ff37f635fc`，分支 `main`；45 个 tracked 文件已修改，21 个 untracked 路径条目（展开 `docs/audits/` 后为 21 个新增文件），暂存区无改动。逐文件 SHA-256 基线用于核对本轮增量；原有文件均保留。

未执行 reset、clean、checkout/restore、stash、commit 或 push。未修改 `../running`，未读取生产数据库、部署、切换入口或迁移真实数据。测试仅使用合成事实和一次性隔离数据库；现有本地持久库未作为测试目标。

## 修复结果

| 问题 | 原因 | 当前行为 |
|---|---|---|
| RQ-001 | `missingWorkoutChildren` 根据完整快照缺席生成永久子数据 tombstone | 移除该计算及删除写入路径；普通快照只补充事实，缺席保留 Sample、关联、Series、Route/chunk/points；完整性标记仅描述读取覆盖 |
| RQ-002 | 未知 Workout DELETE 返回 404，没有屏障；重复已知删除还使用最新 dataset revision | 所有有效显式 DELETE 都持久化 dataset + Workout UUID 屏障；首次原回执与删除日志同事务保存，重复命令返回原回执和原 revision；旧 bundle 被 `409 workout_deleted` 拒绝 |
| RQ-003 | DELETE 未保存旧时间范围及登记 P4 dirty/job | 删除前读取旧范围并写入 tombstone；同事务调用既有 `rai_analysis_dirty`，立即使 Workout 与 Training State current 失效；Worker 按已删除输入清理 current 指针并传播重算 |

继续保持一次 Workout 一个逻辑包、一次 multipart 请求、现有 dataset 行锁、epoch/collector fencing、不可变事实身份、导入历史回执、Route chunk/seal/完整性校验和 COMMIT 后 ACK。未增加协议入口、逐 Sample HTTP、全量覆盖或全量重算，未改变分析算法。

### 屏障和回执存储

不需要新迁移：复用现有 `entity_tombstones` 和 `change_events`。

- 屏障身份：`entity_tombstones` 现有 PK `(dataset_id, sample_type, sample_uuid)`，Workout 固定 `sample_type=HKWorkoutTypeIdentifier`，即 dataset + Workout UUID 唯一；UUID / bigint / timestamptz 沿用既有类型。屏障只 FK 到 dataset，不 FK 到可能尚不存在或将被删除的 Workout。
- `deleted_rev` 与 dataset 行锁分配的提交 revision 绑定；通过 `(dataset_id, deleted_rev)` 关联 `change_events` 的现有主键，v3 删除 ordinal 为 0。没有 sequence 水位。两张表与事实、dirty/job 在同一事务写入，无需新增约束或索引。
- `change_events.created_at` 保存删除时间；payload 保存有效 dataset/collector epoch、reason 和 typed receipt。回执重放先验证当前 fencing，不受后续 dataset revision、重试 reason 或 bundle hash 影响。
- 已知 Workout 的 `previous_start/previous_end` 保存原范围；未知 Workout 两者为 NULL，不虚构时间或分析任务。
- 原有内部删除屏障若没有 v3 receipt，以其固定 `deleted_rev` 和 `alreadyDeleted=true` 返回稳定 ACK。新命令的重复请求返回首次存储的 `alreadyDeleted`，不会将原始 false 改为 true。
- 所有 migrations、`checksums.json` 和原始架构方案均与任务开始时逐字节一致；schema 继续为 15。已验证全部 migration SHA-256 与清单匹配。

POST 和 DELETE 的第一个业务锁都是 dataset 行锁，随后验证 epoch/collector，再处理屏障。两种并发顺序均由真实 PostgreSQL 锁等待验证；屏障一旦成功提交，任何旧 bundle 的原样重放、改 hash 或改 metadata 均不能复活 UUID。

### P4 与客户端

删除事务只登记失效，不直接改历史 run/result。已有依赖触发器立即使 `athlete/default` dirty；current 查询返回 `analysis_not_ready`。既有 Worker 继续执行 Snapshot → Analysis Run → Result → Publication；删除前已读取输入的 Workout / Training State Worker 会被 generation CAS 标记 SUPERSEDED。删除分析只移除 Workout 的 current publication 指针，Training State 重新发布时不再包含该 Workout。`analysis_publications` 继续采用现有可变 current 指针语义，不新增或改写历史模型。

客户端 `RAIAPIClient.deleteWorkoutBundle` 验证 200 ACK 的 Workout ID、`ingestStatus=deleted`、正数 revision 和布尔 `alreadyDeleted`；`SyncCoordinator` 仅在此验证成功后释放 bundle 并标记本地删除。404 和无效 ACK 抛出错误，不再视作已持久化删除。删除响应丢失应重试原 DELETE；上传状态 GET 的 404 不能证明存在删除屏障。`workout_deleted` 为旧上传的非重试终态错误。

## 回归证据

专项命令在项目根目录通过本地 toolchain 环境运行，数据库由 `scripts/test-postgres.sh` 创建；该脚本忽略外部 DSN/libpq 配置，要求 PG18、新建空集群和每测试独立数据库，不连接持久开发库。

```sh
source scripts/local/lib.sh
rai_go_env
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration ./integration \
  -run 'Test(Workout(Bundle(LateComplete|Concurrent|Explicit|SnapshotStill)|Delete(Before|Fencing|AndImport|Invalidates|Supersedes|Rollback))|HTTPWorkoutDeleteBefore|P7RealLoginBoundariesAndRevocation)' -v
```

专项最终通过，integration 包 8.340 秒（单次本机观测，不是性能 SLO）。PostgreSQL 18.6，`fsync/full_page_writes/synchronous_commit/data_checksums=on`，无 TCP 监听。退出记录 `RAI_TEST_LEFTOVER_DATABASES=0`、`RAI_TEST_CLUSTER_REMOVED=1`。

| 验收场景 | 自动化证据 |
|---|---|
| A+B 先到，A 旧包带完整性标记后到；Sample B / Route B 保留，原 A+B 回执可重放 | `TestWorkoutBundleLateCompleteSnapshotRetainsChildren`；对照完整事实、route/series 点与摘要、revision/job 的数据库快照 |
| 两个内容范围不同的包并发 | `TestWorkoutBundleConcurrentSnapshotsRetainUnion`，最终保留并集且无 tombstone |
| 只有显式子数据删除才建立屏障，原回执不能绕过 | `TestWorkoutBundleExplicitChildDeletionBoundary`，分别调用现有内部 sample 事件和 route 删除路径 |
| Workout / Sample / Series / Route 内容冲突仍拒绝、全事务不变 | `TestWorkoutBundleSnapshotStillRejectsIdentityConflicts` |
| 未知删除、删除时间/fencing 持久化、不虚构范围、另一连接池防复活、原回执稳定、同数据库 dataset 隔离 | `TestWorkoutDeleteBeforeUploadDurableStableAndIsolated` |
| 错误 epoch / collector 对首次删除及屏障重放均失败且不写库 | `TestWorkoutDeleteFencingBeforeBarrierReplay` |
| DELETE 先取得锁 / POST 先取得锁 | `TestWorkoutDeleteAndImportBothLockOrders`，使用 `pg_stat_activity` / `pg_blocking_pids` 观测等待顺序 |
| 真实 HTTP 未知删除 ACK、旧 POST 终态拒绝 | `TestHTTPWorkoutDeleteBeforeUploadAndRetry` |
| 已发布结果立即隐藏、保存旧范围、Worker 清理指针、Training State 重发、历史 run/result 原样保留 | `TestWorkoutDeleteInvalidatesPublishedP4AndKeepsHistory` |
| 删除前已载入快照的 Workout / State Worker 不得发布 | `TestWorkoutDeleteSupersedesInFlightWorkers` |
| barrier / dirty / job / change 写入失败，及已知和未知删除的延迟 COMMIT 失败均无 ACK、所有事实和派生历史/指针/回执整体回滚 | `TestWorkoutDeleteRollbackIncludingCommitFailure` 六个故障点 |
| 实际受限 API LOGIN 账号执行已知/未知删除与回执重放、Training State 失效 | 扩展 `TestP7RealLoginBoundariesAndRevocation`；未放宽数据库权限 |
| 404、空 ACK、错 ID、错状态、零 revision、非布尔标志不得确认；首次和重复删除 ACK 可接受 | Swift `APITests.workoutDeletionRequiresDurableACK` |

首轮测试编写中发现了测试夹具问题（旧 DeleteRoute 签名、多 dataset 初始化限制、空 lineage 的 JSON null、HTTP 第三个请求触发真实 burst 限额）；仅修正合成夹具和测试时序，未修改生产限流、身份、算法或错误处理来迁就测试。

已完成的质量门禁：

- `go test -race -count=1 ./...`、`go vet ./...`、`go vet -tags=integration ./...`、`go build ./...` 均通过。
- `python3 scripts/check-boundaries.py` 通过：online graph 249 packages，不引用 admin/migration/goose；analysis/training 域均不引用数据库、网络或文件系统。
- `swift test` 通过：69 项测试、8 个 suite。
- `xcodebuild -project RAI.xcodeproj -scheme RAI -configuration Debug -destination generic/platform=iOS -derivedDataPath /tmp/rai-phase1-xcode CODE_SIGNING_ALLOWED=NO build` 通过（Xcode 26.6）。未安装到真机。
- Redocly CLI 2.71.1 OpenAPI lint 通过；31 条非阻断 warning，与任务入口的 OpenAPI 同命令检查数量一致，未扩展本阶段处理它们。
- `git diff --check`、全部 migration checksum 与原方案不变检查通过。

- 全量 `PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` 通过；integration 包 164.383 秒，所有包通过，隔离数据库与集群清理成功。

**结论：RQ-001 / RQ-002 / RQ-003 实现与本机自动化验收通过；真机与部署验收未完成。** 机器证据见 [rqrun-phase1-2026-11-01.json](evidence/rqrun-phase1-2026-11-01.json)。原始测试日志位于该证据列出的 `/tmp/rai-phase1-*.log`，仅为本机临时日志。

## 未验证与后续边界

- 本轮未使用真实 HealthKit 数据、未连接真机，未执行设备删除/后台强杀/断网恢复 E2E；Swift 包测试与 iPhoneOS 编译不代表真机验收。
- 尚未接通可靠的 v3 Sample / Route 显式删除捕获；缺席子事实暂时保留。未增加完整 HealthKit anchored discovery。
- 不自动清除历史 tombstone，不自动恢复过去已误删的数据，也不回填旧删除缺失的时间范围或分析失效。这些历史记录无法仅凭快照缺席安全地区分真实删除和误删；本阶段不做真实数据修复。
- 未在 fnOS 或生产环境部署/迁移/验收；未测试物理断电与数据库进程崩溃。新连接池、真实事务约束及延迟 COMMIT 失败测试不能替代这些验收。
- 本阶段新增测试验证当前正确性，不改变现有同步发现范围、容量上限或性能承诺。
