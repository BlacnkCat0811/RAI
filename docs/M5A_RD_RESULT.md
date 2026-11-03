# M5A-RD RESULT: PASS WITH RISKS

2026-11-03 / Asia/Shanghai。真实 iPhone + Apple Health 的三类只读事实，已完成 anchored discovery → durable local pending → 隔离 PostgreSQL transaction → durable ACK → committed anchor。真实重启、发现后 SIGKILL、后端不可用、ACK 丢失后 SIGKILL / 同批重试均通过；没有发现 P0/P1 阻塞。后台 callback、自然删除/修订等未观察事件保留风险，不作为已验成功。

机器证据：[m5a-rd-2026-11-03.json](evidence/m5a-rd-2026-11-03.json)。原 [M5A RESULT](M5A_RESULT.md) 与原方案保留；它们的历史“REAL DEVICE: NOT RUN”不改写，由本报告补齐本次范围。

## 1. Device Baseline

`M5A-RD BASELINE`：`main`，HEAD `bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61`。开始执行 `git status --short`、`git rev-parse HEAD`、`git branch --show-current`；已有 10 个 tracked dirty 文件和 3 个 untracked 路径，完整清单与指纹见机器证据。保留既有工作，不 reset / stash / commit / push，不改 `../running`。

| 项目 | 真实证据 |
| --- | --- |
| physical device | devicectl hardware/details 确认 **iPhone 12 Pro Max**，iOS，非 Simulator |
| iOS | **27.2**；wired / paired / tunnel connected；developer mode enabled |
| App | 正式原生 RAI target，**0.6.0 (1) / Debug / development signed**；当前源码构建、安装、启动成功；可执行文件指纹见机器证据 |
| HealthKit available | **true**，由手机进程调用 `HKHealthStore.isHealthDataAvailable()` 记录 |
| Xcode | **26.6 (17F113)**，位于 Downloads；仅本任务进程设置 `DEVELOPER_DIR`，没有切换系统默认 CLT |
| test backend | 本机全新独立 PostgreSQL **18.6** / schema **20**，数据库只监听专属 Unix socket；受限 API 角色通过 RuntimeRoleSafe；新 dataset / 新采集身份 |
| durability | fsync / full_page_writes / synchronous_commit / data_checksums 均 on |

初查仅在 Applications 与当前 CLT 工具目录查找，错误地认为没有 Xcode；用户纠正后通过 Spotlight 找到 Downloads 内安装，已更正。首次无 Team 的构建失败后，从已登录账号找到唯一 Team，写入被 Git 忽略的私有 xcconfig，自动签名成功。未记录设备 serial、UDID 或 Apple ID。

隔离新会话建立前，用户在既有 App 授权界面产生的只读查询/observer 统计只作补充，不计本次隔离闭环；旧会话的本地 pending 保留，未复制为 fixture，也未清除其身份/Keychain。独立会话已通过实际握手验证；其服务器初始为 **0 facts / 0 receipts / 0 coverage**，不复用旧服务或生产库。收尾停止 RAI 与本次 API/PG；测试数据库和凭证在仓库外私有临时目录保留，手机保留隔离会话及 anchor，服务当前不继续采集。

## 2. Authorization

正式 RAI App 内对 HRV SDNN、RestingHeartRate、SleepAnalysis 发起 `toShare: []` 的只读请求，设备报告 **authorization request completed**。没有 `permissionGranted=true`、没有根据空查询推断拒绝读取，没有新增写权限或 HealthKit save/delete。

本轮没有写入/删除/编辑真实健康记录，没有制造 HRV/RHR、sleep replacement 或未知 category，没有改时区/系统时间。WorkoutKit 排程授权不在本阶段。

## 3. Real HealthKit Discovery

90-day bounded bootstrap 使用真实 `HKAnchoredObjectQueryDescriptor`（底层 anchored query），无 macOS/DailyProbe fixture 替代。三类均记录到 **1 次 nil-anchor 初始查询**，随后从 committed anchor 续页。原始 UUID、区间、来源均在内部保留；诊断只记录计数和不可逆指纹。

| 类型 | real query | facts observed | 有事实页 | candidate anchor / ACK / committed anchor / bootstrapComplete |
| --- | --- | ---: | ---: | --- |
| HRV **SDNN / ms** | PASS / nonzero | 3,116 | 25 | 全部 PASS |
| RestingHeartRate / count/min | PASS / nonzero | 83 | 1 | 全部 PASS |
| SleepAnalysis | PASS / nonzero | 1,594 | 13 | 全部 PASS |

最终完整服务器/本地批次链（含空 coverage）为 HRV **48**、RHR **45**、Sleep **48**；与 query 诊断事件数的差异见第 7 节。

每次真实 normalization 的 UUID identity 与原始 interval 比较均通过。sourceRevision 的 version 在 4,793 条上存在并保存；HealthKit 提供的 device context 在 3,116 条上存在并保存。SDNN 不称为 RMSSD、RR interval 或 raw PPG。

Sleep 实际类别只记录名称/数量：asleepUnspecified **7**、awake **351**、core **746**、deep **215**、REM **275**；inBed 本次未观察到。六种 SDK category 的支持、unknown category 拒绝/不推进由本轮 34 项 SDK 回归补充。真实 intervals 的 overlap 在数据库中仍存在，未按天合并、裁剪或消除重叠；没有导出时间区间。

保守来源分类：Apple-origin（有 Watch 来源/设备证据）**3,116**，third-party **15**，unknown **1,662**。不能仅凭类型名把 RHR 或 sleep 当作 Apple-origin。15 条第三方 RHR 原始来源照常进入 Raw，未丢弃；`HKWasUserEntered=true` 未观察到，不据此推断其他记录一定不是手工输入。没有计算 recovery eligibility。

## 4. Anchor Lifecycle

- **Bootstrap：PASS。** candidate archive 与不可变 body 先持久保存；每页的真实 ACK 身份/原字节 SHA/状态/计数验证后，anchor、sequence 与 pending removal 原子保存。三类首次窗口保持 90 天，没有扩为 180 天或整库历史。收尾还停止/重启同一测试 PG 与 API（没有 migrate、reset、restore 或新身份），手机仍仅增量，事实数不变。
- **第二次 query：PASS。** 用户在可见正式“同步日常健康”按钮点击一次；三类均 `usedCommittedAnchor=true`，均 **0 additions / 0 deletions**，查询约 **2.65–5.91 ms**。未重新 90 天 bootstrap；有效事实仍 4,793。
- **正常 restart：PASS。** 对 RAI 使用 devicectl 默认 SIGTERM，重新启动；三类 committed anchor、bootstrapComplete、窗口指纹与未完成 pending 身份一致。再次实际查询仍 incremental / 0 changes。
- **Offline：PASS。** 三类真实 discovery 后，隔离代理使测试后端不可用；手机返回 network_-1005，pending 保留，原 body/batch/candidate 指纹未变，committed anchor 不前进。恢复网络后原 pending 上传成功，随后追平。
- **ACK / retry：PASS。** 原 batch/body 恢复；ACK 丢失测试的已提交服务器回执仍是一条、字节指纹不变，客户端最终应用原 ACK。空页同样生成 durable coverage ACK；无数据不解释为授权拒绝或恢复良好。

最终三类：pending=false、hasMore=false、hasError=false、bootstrapComplete=true；客户端 sequence 分别 **49 / 46 / 49**，恰为服务器对应 last_client_seq **48 / 45 / 48** 加一。

## 5. Real Device Kill

**A：PASS。** 三类 discovery 后、upload 前，已取回脱敏 pending checkpoint，服务器仍 0 facts / 0 receipts；只对 RAI 执行真实 SIGKILL。重启在后端不可用时再次确认同一 batch/body/candidate、sequence=1、committed anchor 为空；恢复后成功交付，没有丢事实。

**B 的受控窗口：PASS。** 代理把一个真实 App 请求交给 API，得到 PostgreSQL 已提交的 200 后丢弃响应，并暂时阻断重试。SQL 只按 body 指纹查询，证明服务器恰有 1 条 committed receipt；手机仍有匹配原 pending、旧 sequence/anchor。在此状态对 RAI SIGKILL；重启恢复同一 pending，服务器仍同一回执，客户端最终提交 candidate。

这覆盖 **server commit + ACK transport loss → client ACK apply 前**。客户端已经接收/验证 ACK、但尚未完成 anchor 文件 rename 的更窄窗口：**NOT DETERMINISTICALLY TESTED ON DEVICE**。不把本次窗口包装成文件系统调用内部的精确 kill。原 synthetic subprocess SIGKILL 证据仅作补充。

## 6. Observer / Background

显式 observer 阶段，真实 `HKObserverQuery` 注册及 `enableBackgroundDelivery` 返回完成；前台收到 **3 次** callback，随后实际转场收到 **6 次 inactive callback**。九次均成功持久保存 wake hint，再进入 anchored discovery；observer 不作为事实来源。

驱动连续发起 **8 次**相同手动入口请求，用户另外完成约 30 秒桌面窗口及多次快速前后台切换。实际记录 **4 次 background / 4 次返回 active**（不计启动 active）。共享 ForegroundSyncScheduler、Daily coordinator、Outbox 和真实上传保持 bounded/idempotent；没有重复 nil-anchor bootstrap、有效事实重复、anchor 损坏或类型饥饿，最终全部追平。

诊断模式严格绑定已验证测试身份，使用现有“同步日常健康”UI/方法和共享 scheduler；前台恢复限定为 Daily 路径，避免本阶段发现/上传不相关 Workout。正常完整 Workout 前台闭环不冒充本次真机范围，其回归由包、SDK 与 Go/PG 用例覆盖。

**BACKGROUND DELIVERY: NOT OBSERVED DURING TEST WINDOW。** 已有 inactive/恢复转场回调，但没有记录到 UIKit 明确为 background 的 callback，不将二者混同。没有为了等系统机会制造健康事实，不声称每 15 分钟后台同步。

## 7. Server Evidence

| 隔离库最终聚合 | 数量 |
| --- | ---: |
| facts / unique (type, UUID) identities | **4,793 / 4,793** |
| source 完整保留且 bundle identity 匹配的事实 | **4,793** |
| committed receipts | **141** |
| 其中有事实批次 / 空 coverage 批次 | **39 / 102** |
| registered 但未 committed 的 batch | **0** |
| coverage / caught-up coverage | **3 / 3** |
| last query 空，但 lastFactAt 存在的 coverage | **3** |
| duplicate receipt identity groups | **0** |
| 最大 event count / batch | **128** |
| 本阶段 Workout / analysis jobs | **0 / 0** |
| 尚待模型实现的 dirty scopes | **4,793** |
| ACK 明示 awaiting_model_implementation | **141** |

102 个空批次包含早期驱动反复制造 manual wake 的校准运行，以及后续增量/observer/前台测试。已修正驱动：续页继续同一 wake，不反复模拟按钮；没有修改 M5A 协议或放宽限流。诊断记录有 138 个 query-staged 事件，服务器/本地 chain 为 141 批，缺少 3 个跨进程边界的空页 query 事件（原因未最终确立）；39 个有事实页及 4,793 个事实的诊断计数完整。诊断不是完整请求账本，最终批次数以数据库和 durable stream chain 为准。不同 wake 的空 coverage 是不同逻辑批次，不算重复有效事实；同批重试回原 receipt。不能把 141 当作 39 个事实页的最小生产请求数或吞吐基准。

Coverage 实测见到 bootstrap 未完成、收到事实、以及完成空增量且历史事实仍存在；fact later deleted 未在真实 HealthKit 自然发生，由本轮真实隔离 PG 的合成 deletion/coverage 用例覆盖。无权限推断、全库摘要或全量重算作为同步依赖，Worker 未伪造分析完成。

工程观察（真实设备，计数/耗时，不是生产 SLO）：

| 类型 | 有事实 query wall 合计 / 最大 | 无变化 query wall 中位数 | normalization 合计 | 最大 batch bytes |
| --- | --- | --- | --- | ---: |
| HRV | 493.79 / 233.56 ms | 8.07 ms | 31.02 ms | 62,196 |
| RHR | 93.41 / 93.41 ms | 4.25 ms | 1.34 ms | 27,215 |
| Sleep | 113.01 / 16.24 ms | 4.36 ms | 9.65 ms | 44,122 |

Delivery wall（文件校验、网络、ACK 和本地提交合计）中位数约 103 / 54 / 53 ms；故障注入期间一次 Sleep delivery 等待约 **90.4 s**，包含 iOS 后台会话恢复，不能称纯上传延迟。初次 bootstrap 混有重启、驱动校准、限流与人工窗口，未给出“干净生产端到端耗时”；自然 hot delta、iPhone memory high-water、功耗未测，不重新扫描健康历史来制造性能结果。

## 8. Privacy Audit

没有输出 HRV/RHR 实际值、sleep start/end/duration、样本完整 UUID、详细 metadata、source 具体 bundle identifier、健康 export 或真实 payload fixture。原始事实只在手机受保护 outbox 与仓库外隔离 PG 中；没有下载手机 state/payload 或数据库 dump。

受控 instrumentation 为 **DEBUG only + 显式 launch flag + 消耗式私有命令文件 + 隔离身份校验**；只记录类型、计数、状态、耗时、来源分类、anchor/batch/body 不可逆指纹。默认启动不启用；Release 编译通过，二进制中验收 flag/config/report/diagnostics 标记均不存在。保留为明确受控 diagnostics，没有隐藏 dump。

所有 devicectl 原始设备枚举/命令 JSON 仅在临时目录内处理并删除，终端只发出脱敏结果。构建日志的账号与签名标识已过滤；初期本机日志在收尾前完成脱敏。真实后端 API 不保存输出日志，代理不记录请求 body/header；PG 日志检查 health/参数/失败行标记均 0、ERROR 计数 0，并显式关闭语句/参数/错误语句日志。收尾报告 JSON 的原始 UUID/email/凭证/健康载荷键扫描无命中。私有 token、Team/地址配置均被 Git 忽略或位于仓库外。

本次新增仓库内容只含脱敏统计、源码和执行记录；既有用户文件逐项保留，IMPLEMENTATION 只追加。审计范围是本次工作，不宣称历史仓库整体无隐私问题。

## 9. Regression

| 实际检查 | 结果 |
| --- | --- |
| `DEVELOPER_DIR=… swift test --package-path ios/RAI` | **119 tests / 15 suites PASS**；XCTest 另 2 PASS、3 个既有 opt-in 入口 skip，不冒充现场测试 |
| Debug 真机签名 build / install / launch | PASS；受控诊断构建实际运行于手机 |
| Release generic iOS，`CODE_SIGNING_ALLOWED=NO` | PASS；诊断标记扫描 0；不是 Release 安装验收 |
| 一次性 iOS 26.5 Simulator，ad hoc signed `-only-testing:RAITests` | **34/34 PASS**，含 SDK categories/units/metadata/anchor/unknown 拒绝、Daily 与 Workout/coordinator/session 回归；自建模拟器已删除，不替代真机证据 |
| `go test -count=1 ./...` | PASS |
| `go test -race -count=1 ./...` | PASS |
| `go vet ./...`（此前另跑 integration tag） | PASS |
| 全量隔离 PG：`go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...` | PASS，integration **419.787 s**；残留 DB=0、cluster removed=1 |
| 真机完成后的 Daily lifecycle/RHR+Sleep/coverage/fencing/rollback/crash/delete-wins/HTTP/受限角色/migration 定向 PG race | PASS，integration **12.992 s** / admin **1.816 s**；残留 DB=0、cluster removed=1 |
| `python3 scripts/check-boundaries.py` | PASS，249 包；online 不达 admin/migrations/goose，analysis/training 不达 I/O |
| gofmt / go mod verify / build / sqlc 1.31.1 generate + dbgen diff | PASS，无生成代码差异 |
| 固定 `@redocly/cli@2.71.1` OpenAPI lint | PASS，**0 errors / 31 既有 warnings** |
| `git diff --check` / baseline preservation / 本次隐私扫描 | PASS，详情见机器证据 |

保留失败与纠正：CLT 首轮 Swift 测试缺 Testing 模块，换为实际完整 Xcode 后通过；首次签名缺 Team，配置后通过；一次诊断编译缺 `try`，已修正；设备 launch 参数位置错误，纠正 CLI 调用后成功；初始 zsh source Bash lib 导致 npm 未启动，改 Bash 后 lint 通过。首轮全量 PG 在默认 10 分钟**测试进程总时限**到达时失败、当时剩 1 个测试 DB，整个临时集群已移除；以历史 30 分钟进程时限串行重跑通过，没有修改生产事务/语句超时。

本轮实际修复的真机诊断问题：为配置私有精确 LAN origin，在被忽略的构建副本中写入字面量 ATS domain，再以 `INFOPLIST_FILE` 传给签名构建。收尾发现 Xcode 不展开字典键，已撤回源模板的变量键实验；仓库原 Debug 模板保持默认 rai-dev.local。实际构建副本 domain 与 configured host 匹配、无 NSAllowsArbitraryLoads，客户端仍只允许精确配置主机；本地 mDNS 连接失败而精确 LAN origin 成功，未确立 mDNS 失败的底层原因，不将其归因于 ATS，实际 mDNS 跨设备可达性未获证据。独立诊断入口缺少主界面 dark theme，补齐后用户确认显示正常并点击按钮。新一次手动同步会清除旧提示，并以当前 flow 错误重新呈现，成功后不残留旧红字。未改 HealthKit 数据、Daily/Workout wire 或数据库事务。

## 10. Risks

- **P0：未发现。** 本次事实/pending/ACK/anchor/重试闭环没有已知数据安全失败。
- **P1：本阶段无未解决阻塞。** 原 M5A 真机核心门禁已补齐；不构成生产发布放行。
- **P2：** 真实 HKDeletedObject、RHR/Sleep 修订、真正 background callback、received-ACK 到 anchor rename 内精确 kill 未观察/未测；保留已有 synthetic/PG/SDK 覆盖。极长离线 deletion retention、真实授权变化后补读、物理断电、设备内存/功耗、长期 outbox 增长仍未验。后台上传故障恢复受 iOS 调度，不能承诺延迟。
- **P3：** 31 个既有 OpenAPI warning；测试使用精确主机 Debug HTTP，TLS/WAN/mDNS 与生产部署未验。仪表开销、人工操作和早期驱动空页使本次性能不作生产 SLO；诊断有 3 个空页 query 事件缺口，不作为完整账本。

## 11. Not Observed / Not Verified

```text
REAL HKDeletedObject: NOT OBSERVED
REAL RHR REVISION: NOT OBSERVED
REAL SLEEP REVISION: NOT OBSERVED
REAL UNKNOWN CATEGORY: NOT OBSERVED
MANUAL FLAG: NOT OBSERVED
BACKGROUND DELIVERY: NOT OBSERVED DURING TEST WINDOW
DST/TRAVEL: NOT EXERCISED
RECEIVED-ACK BEFORE ANCHOR-RENAME EXACT KILL: NOT DETERMINISTICALLY TESTED ON DEVICE
NATURAL HOT DELTA / MEMORY HIGH-WATER / POWER: NOT MEASURED
TLS / WAN / PRODUCTION DEPLOYMENT: NOT RUN
```

第三方 facts 已真实观察并保留；设备侧 ACK loss injection 已运行，不能再写 NOT RUN。后台/删除/修订等非确定事件未自然发生，不伪造、不为此修改 Apple Health 或继续等待随机事件。完整真实 Workout 上传/删除闭环不在本阶段重跑；既有 Workout Protocol 3 包、SDK、Go/PG 回归通过，未发现回归。

## 12. Decision

```text
M5A-RD RESULT: PASS WITH RISKS
M5A CLOSED FOR DEVELOPMENT
Recommended Next Phase:
M1 — Feature Foundation & Publication Substrate
```

没有本阶段 M1 blocker；仅推荐下一阶段，**没有开始 M1**。没有 M5B Recovery、Recovery/HRV/Sleep/Readiness Score、VO₂max 或 Running Ability 模型，没有生产部署、真实数据迁移、切换旧入口或 push。本阶段执行与报告完成后停止。
