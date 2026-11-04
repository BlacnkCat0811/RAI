# M4-B4 Result

日期：2026-11-04（Asia/Shanghai）。**M4-B4 RESULT: PASS WITH RISKS**。**M4 EXTERNAL VO₂MAX ENGINEERING: CLOSED**。仅合成数据与隔离本机 PG18；没有部署、提交、push、切换旧入口或迁移真实数据。本阶段完成后停止，不进入 M5B。

## 1. Git Baseline

`M4-B4 BASELINE`：`main`，HEAD `380a6fe96d13d1e57f69ceac50e52e72908af2b8`。开始时已有 B3 tracked/untracked 改动，全部保留；原始 status/diff stat、540 个起始文件 SHA-256 在 [baseline.json](evidence/m4b4/baseline.json)。最高 migration 开始/结束均 **00025**；无新 migration，旧 checksum 不变。B1 frozen contracts、B2 raw/metadata/provenance/receipt/coverage/anchor、B3 selection/30–90d/status/family/domain 均无语义改动；[freeze-scan.json](evidence/m4b4/freeze-scan.json) 对比起始树，而不是拿未提交 B3 与 HEAD 混算成本轮改动。原方案、`../running` 和历史阶段结果保持原样。

B4 唯一生产行为修复为普通日志身份脱敏：request/job/run/scope 及 training plan/base/effective identity 改为 SHA-256 hash；回执、HTTP `X-Request-ID` 和数据库身份保持不变。当前日志字段约定同步更新到 `P7_OPERATIONS.md` / `ARCHITECTURE.md`。不改模型或上传协议来配合测试。

## 2. End-to-End Architecture

实跑路径：synthetic SDK HKQuantitySample → App `VO2MaxNormalizer` → 正式 `SyncCoordinator` → 独立 VO₂ Outbox → 正式 `RAIAPIClient` / `BackgroundTransfers` → HTTP `vo2max-facts/v1` → PG raw/tombstone/coverage/receipt → validated ACK/committed anchor → external dirty → 真实 Worker Claim/Process → repeatable-read snapshot → owner/admin 显式 selection → external projection → 同事务 artifact/result/candidate publication → admin inspect。

fixture 注入止于 `HealthKitReading`；SDK 描述符是内存中明确合成的数据，不查询 HealthKit、不写健康库。HTTP、storage、Worker、publisher、reader 均使用正式实现。主闭环没有 direct SQL fact/result insert 或手工 patch。额外 SQL 仅用于初始化权限、故障/竞态注入、只读断言，以及独立的超限损坏测试。

## 3. Simulator E2E

**Simulator VO₂ full E2E: PASS**。iOS26.5 / Xcode26.6，正式 RAI target，本地 ad-hoc Simulator 签名；共 **16 recipes + 1 hot-empty checkpoint**，分两组 9/8 checkpoints，不能把它们等同于 17 个独立 XCTest。实际执行 2 次专用 full-E2E XCTest；另重新执行原 B2 Simulator E2E。普通正式 App XCTest **40 PASS / 0 failure**，与 full E2E 分列。

16 recipes：fresh、empty、stale、expired、third_party、manual、unclassified、epochs、incomplete、conflict、equivalent、facts128、bootstrap90、refs1024、epochs32、ties256。每个 checkpoint 验证真实 PG receipt 与事实计数、Worker 和 candidate/admin read。双 epoch 场景实际通过 admin A→B 切换，先确认旧 pointer 立即失效，再确认新 publication 指向 B。

可复现入口：[test-m4b4-simulator.sh](../scripts/test-m4b4-simulator.sh)。需要显式指定隔离 Simulator 和 `DEVELOPER_DIR`，不消费已有数据库 URL。最初未签名构建遇到系统 background URLSession XPC 拒绝；改用 B2 已验证的 Simulator ad-hoc 签名后全套通过，不用 mock uploader 绕过。

## 4. Raw Transport

fresh SDK quantity 采用非整数 binary64；Swift quantity bit pattern、wire decode、PG raw、artifact/result、candidate internal read 精确一致；不经 float32、舍入、二次标准化或 external value 修改。valid metadata/provenance 与大于 2^53 的 Int64 metadata 回归继续通过。

Simulator 的 fresh 首次 raw commit 后实际断开 ACK；原 pending bytes/hash/batch 保留，重试得到原 receipt，anchor 在 validated ACK 后落盘并可 reopen。初次 publication 只有一个 semantic artifact/current pointer。128 facts 满页后额外空页保守完成，1024 refs 共 9 个合法 receipt；没有把满页假装 caught-up。coverage incomplete 的已提交部分不显示值。

## 5. External Projection

`rai.vo2max.external_projection/v1` 保持纯外部选择/复制：`external_estimate`、有值 provider=`apple_health`、confidence/parameterVersion/featureVersion=null、有值 quality=`partial`。根值等于 selected external raw fact。原 artifact/result/run 只追加；技术失败不变成业务成功。

全仓 executable source 扫描未发现 `rai.vo2max.estimated/v1` 或 `laboratory_measured_vo2max` 活动路径。没有新增 RAI 自有 VO₂max、VDOT、ACSM/HRR VO₂、calibration、Recovery、Readiness 或 Apple/RAI 混合模型。

## 6. Source / Selection

沿用 B1 的每个 epoch 均须显式选择规则。无有效 selection 的多个 epoch 输出 `source_selection_required/null`，不因看起来像 Apple 自动选择。第三方、user-entered、unclassified 即使显式选择也不能成为 Apple current。testType=1 不等于 laboratory measurement。

same epoch / latest same-time 异值输出 `conflicting_data/null`；等值等 support 的不同 UUID 不冲突、不增加 confidence。完整领域 permutation 与真实 PG 随机 insert/delete/late-arrival/replay/selection switch/revoke → bounded rebuild 回归通过。

## 7. Freshness / Status

真实 candidate reader 验证 **30d−1µs / 30d / 90d−1µs / 90d**，结果依次 available/stale/stale/expired。stale 原值保留，expired root=null，raw 和历史 artifact 仍保留。Simulator 另实际形成 stale 和 expired publication。

caught-up 空数据为 `no_sample_observed/null`，不推断权限拒绝；128 facts bootstrap incomplete 为 `sync_incomplete/null`。各状态沿用冻结 decision table，不改规则让夹具通过。

## 8. Delete / Replay

正式 HTTP B2 delete → tombstone commit → candidate 立即不可读 → Worker 重算通过。无前驱输出 deleted/null；60d 合法前驱输出 stale 原值；仅 91d 前驱保持 deleted/null，不扫描域外恢复值。实际 HTTP unknown delete→ACK→later create 被永久 tombstone 抑制，输出 no_sample_observed/null；unknown tombstone 不捏造 Apple withdrawal。

原 bytes 多次 replay 返回 byte-equivalent receipt，不增代、不重复 artifact/current。known delete 后 stale create 也不能复活。selection/control exact operation replay 和 immutable projection-input fencing 在完整回归中通过。

## 9. Crash / Fault Recovery

真实 macOS SIGKILL 的 before-upload、server-commit-before-local-anchor 两点接到 candidate publication：pending 恢复、原 batch replay、最终 1 raw semantic fact / 1 receipt / 1 publication。此合成 probe 的第三方 provenance 按规则保持 null state；不冒充 iPhone kill 或 Apple source。B2 原 6 个 kill boundaries、Daily 原 3 个 kill boundaries 及 429/fairness 也重新通过。

真实 API LOGIN raw transaction 注入 raw/tombstone/coverage/receipt 故障，全部 rollback 后原请求可恢复到 candidate。B3 七个 publication 阶段（artifact/result/publication/dirty clean/run/job/attempt terminal）及 backend termination 全量回归通过。B4 另实跑 HTTP raw→受限 Worker→artifact INSERT 后 barrier→owner `pg_terminate_backend`→无半结果→retry/reclaim→唯一完整 publication。

## 10. Transaction / Fencing

首次无 pointer 的 delete race、compute 中新 raw、selection switch/revoke、config/assignment、collector/device session、lease expiry/takeover 及 control effective boundary 均通过真实 PG 回归。旧 Worker 不 publish、不 clean 下一代；删除事务即时移除 current。raw/屏障/coverage/receipt 与 dirty 在 dataset 锁下保持原事务边界。

双向独立性实际运行 M1 features / M2 Training Load / M3 Running Ability 与 HR reference：external run/value/generation/job 数不变；VO₂ insert/delete 后上述模型 job semantic snapshot 不变。pure-domain 边界检查继续禁止 HTTP/DB/training/presentation/M1/M2/M3 依赖。

## 11. PostgreSQL / Roles

真实 **PostgreSQL18.6**，新建 socket-only disposable cluster，fsync/full_page_writes/synchronous_commit/data_checksums 均 on。empty→25、23→latest、24→25、重复 migrate 和 schema readiness 均在真实回归通过，旧 migration bytes/checksum 不变。

最终 B4 使用独立 **LOGIN** 会话继承原 `rai_api` / `rai_worker` 权限组（为并行隔离加随机 suffix）；断言 `session_user=current_user`，不是 owner 连接 SET ROLE。owner/admin 仅做初始化、受控 selection、inspect 和故障注入。API SELECT artifact / INSERT selection / UPDATE channel / INSERT publication 均实际 SQLSTATE42501；Worker raw UPDATE / tombstone DELETE / input 或 channel UPDATE 同样拒绝。所有最终集群清理完成、残留数据库为0。没有生产部署或生产权限验收。

## 12. Candidate Isolation

真实受限 API HTTP 实跑 Home overview、Metrics history、Workout list/summary/detail、generic workout analysis、Training/Coach state、current/saved plan/history。candidate model/metric/family/artifact 均不可见。Training 与 Coach 共用 `/v2/analysis/state`，没有另造一个 Coach endpoint 冒充验证。

实际 generic metrics/latest GET 不存在而返回404/405；现有 history/current/workout readers 有明确 legacy scope/family filters，不能无 family query candidate。public selection POST 不存在。active=`legacy`；实际执行 `promotion-plan`，promotable=false，含 product_not_activated、real_device_acceptance_deferred；不自动 promotion。

## 13. Regression

| 门禁 | 实际结果 |
| --- | --- |
| `go test ./...` | 552 PASS / 4 既有 opt-in skip / 0 fail |
| `go test -race ./...` | 552 PASS / 4 skip / 0 fail |
| 全量隔离 PG18 integration race | 1161 PASS / 11 既有 opt-in skip / 0 fail |
| 最终 B4 real-LOGIN race | 17 test/subtest terminal PASS events / 0 fail |
| B4 + B2 + Daily macOS recovery | PASS，单列，重复场景不加入独立总数 |
| `swift test --package-path ios/RAI` | 129 tests / 16 suites PASS |
| 正式 Simulator App XCTest | 40 PASS，full E2E 另计 |
| Go vet（普通及全部专项 tags）/ build / mod verify | exit0 |
| sqlc1.31.1 两次 generate | 与起始生成文件逐字节相同 |
| boundary / migration / fault / property / diff check | PASS |

全量 PG 编译快照之后增加的 artifact capacity / hard limit / role denial / unknown delete 场景由最终 real-LOGIN B4 race 全部覆盖，不将重复测试相加称独立用例总数。11 个 skip 为既有 M1/M2/M3/Phase4 opt-in 性能、指定真机及 production socket deadline 场景；不冒称重新验证这些范围。OpenAPI 未改：**B2 frozen contract unchanged**。具体机器事件与命令结果以 [validation.json](evidence/m4b4/validation.json) 为准。

## 14. Performance

**single-machine synthetic observation**，非分位数、生产 SLO 或 WAN。正式 Simulator total 从 fixture discovery 到 admin/candidate 验证，含 local persistence、HTTP/准入等待、ACK/anchor、selection 与 Worker；组件观测另列，不把 synthetic ACK micro-observation 算作 E2E。

| Simulator 路径 | total E2E |
| --- | ---: |
| 0 facts | 172.289 ms |
| 1 fresh + lost ACK + hot empty | 2545.606 ms |
| hot empty 子段 | 1179.636 ms |
| 128 facts | 727.136 ms |
| 90-day bootstrap | 532.944 ms |
| 1024 refs | 16203.806 ms |
| 32 source epochs | 308.777 ms |
| 256 ties | 3493.672 ms |

1024-ref 正式 Worker snapshot 143.810ms / projection 0.881ms / publication 196ms / candidate reader 224.994ms。duration-only PG phase observer 记录 parse/raw transaction；App 另有 normalization+Outbox、HTTP/ACK/anchor、queue/reader/total。40项 App XCTest 内的独立 component observations 分别记录 actual SDK normalization、Outbox persistence、receipt validation 和 validated anchor commit（后者含正常重复校验）；该组件 ACK 是 synthetic，不冒充 HTTP。

合法组合 1024 refs + 128 selection refs：canonical artifact **126,384 bytes**、PG jsonb text **128,794 bytes**，均 **<128 KiB**；**131,073-byte** supplemental corruption fixture 被 DB constraint fail closed，无 artifact/publication。1025 refs / 33 epochs / 257 ties 在 PG 全量回归 fail closed。全部计时与上下文见 evidence observations，不据此提出性能改架构或生产 SLA。

## 15. Privacy

全部 fixtures synthetic，不读取真实 HealthKit、不接触生产库。API/Worker 普通结构化日志实际捕获断言：没有根健康值、完整 UUID、source bundle、metadata 或 token。仅保留 count/status/hash/duration/error category；系统 Simulator 诊断不当作产品日志证据拷贝。

[evidence manifest](evidence/m4b4/manifest.json) 仅保存 synthetic counts、command terminal results、SHA-256、timings、status；不拷贝 raw runtime/SQL/assertion output、health payload 或临时凭证。临时 token 仅存在 private xctestrun/运行态并在完成后删除。重新整理命令为 `python3 scripts/collect-m4b4-evidence.py`。

## 16. Risks

- **P0：无已知未解决 engineering blocker。**
- **P1：无已知未解决 engineering blocker。**
- **P2：** 真实来源/metadata/授权/删除及后台可见性尚未验收；描述符分类不等于硬件认证，30/90d 是冻结工程策略，不是生理有效期。
- **P3：** 单机 debug/race、局部队列与准入等待不能推断 WAN/production SLA、断电耐久、功耗或长期离线表现。普通日志关联字段从 *_id 改 *_hash，运维查询应采用更新的 runbook。

## 17. Not Verified

**REAL DEVICE ACCEPTANCE: DEFERRED**。

real Apple Watch VO₂max availability；real source distribution；real metadata distribution；real HealthKit authorization 与 delete/update behavior；real background delivery；long offline behavior；WAN；production；physiological validity。Simulator 与 macOS SIGKILL 不证明真实 Watch/iPhone 行为。**PHYSIOLOGICAL VALIDATION: NOT VERIFIED**。

## 18. Decision

```text
M4-B4 RESULT: PASS WITH RISKS
M4 EXTERNAL VO₂MAX ENGINEERING: CLOSED
REAL DEVICE ACCEPTANCE: DEFERRED
PHYSIOLOGICAL VALIDATION: NOT VERIFIED
RAI OWN VO2MAX MODEL: NOT IMPLEMENTED
Recommended Next Phase: M5B — Recovery Model Foundation
```

完成 B4 后停止。现有工程具备 Workout Features、Training Load、Running Ability 与 external Apple Health VO₂max candidate；candidate 尚未产品激活。没有 RAI estimated VO₂max、Recovery 或 Readiness。External 永远保持 external_estimate，不成为 RAI prediction 或 laboratory truth。
