# UA1 Result

2026-11-05，Asia/Shanghai；run alias：**ua1-20261105-a**。

**UA1 RESULT: BLOCKED。** 真实 iPhone → HealthKit → Wi-Fi/HTTPS → 云 Go API → PostgreSQL → Worker → candidate → M7 inspection 已实际运行。日常健康增量、断网/API/DB 故障恢复、真实丢 ACK、K02/K04 和真实数据重启持久性通过所列范围；完整验收受 **Workout 重启复跑、VO₂max metadata 兼容性、M6 真实全流完整性** 三项阻塞。

初次网络阻塞在用户放行端口后解除，保留在证据历史中。本轮没有模型公式/参数调整、candidate promotion、产品激活、生产迁移、commit 或 push。最终新旧测试环境均保留可用，UA1 到报告为止。

| 能力 | 本轮结论 |
| --- | --- |
| Workout 首次真实上传、路线关联/投影 | PASS；首条上传网络类型未确认 |
| Workout 重启后无变化复跑 | BLOCKED — workout_core_change_requires_review |
| HRV / RHR / Sleep | PASS WITH RISKS；单设备、固定窗口 |
| 飞行模式断网 / API unavailable / DB unavailable | PASS，详见请求边界范围 |
| Lost ACK / K02 / K04 / 真实数据 PG restart | PASS |
| External VO₂max | 安全 fail closed；真实投影 BLOCKED，已见 24 条样本 |
| Worker / candidate / M7 inspection | 实际执行；模型状态和 reasons 正常保留缺失/冲突 |
| M6 real all-stream completeness | BLOCKED；无 syntheticCoverage override |
| 产品、生理准确性 | NOT AUTHORIZED / NOT VERIFIED |

[机器证据目录](../evidence/acceptance/device/ua1-20261105-a/)；[工程发现](UA1_ENGINEERING_FINDINGS.md)。以下计数按本轮证据截面记录，后续自然 HealthKit 到达不属于同一冻结截面。

## 1. Git Baseline

main；HEAD **9a8839dff0f58fe93ffc7297e50490c0c88279cc**。已记录 git status --short、git rev-parse HEAD、git branch --show-current、git diff --stat。入口已有 IMPLEMENTATION 的 16 行 UA0 追加和六份未跟踪 UA0 文档，全部保留。原方案 SHA-256 为 **6951b1d938133a964cb06d28c3aa744ad05121f25da770e184041211df53e3de**，与 HEAD 一致；未操作旧 running 工程。

实际最高 migration **27**，共 **27** 份；runtime gate **27**。完整 embedded migration 自动执行至当前最高版本，没有手工指定 27。全部 frozen checksum 一致；旧云源码归档 19 份 migration 也逐份一致。Go **1.27.3**。源码、迁移、模型和产品消费者未改；新增执行材料为文档/脱敏证据，私有验收编排留在 Git 忽略目录。[baseline.json](../evidence/acceptance/device/ua1-20261105-a/baseline.json)

## 2. Device / OS / App

真实 **iPhone 12 Pro Max / iPhone13,4**，iOS **27.2 / 24B119**，USB wired，开发者模式开启。完整 Xcode **26.6 / 17F113**；DEVELOPER_DIR 为 /Users/ta/Downloads/Xcode.app/Contents/Developer；Swift **6.3.3**、iPhoneOS SDK **26.5**。

原 bundle **com.rai.v2.ios**，版本 **0.6.0**、build **1**、Debug。签名 build、install、launch 实际通过；首次 DDI 断连后恢复。首次 iOS Security/profile trust 拒绝启动，用户随后明示已信任且能打开，之后 devicectl 启动成功。codesign strict 和 profile 设备匹配通过，不导出签名账号或设备唯一标识。development profile 到期 **2026-11-10 07:12:53 UTC**。

保留 Debug NWError.wifiAware exhaustive-switch warning 和 AppIntents metadata skipped 提示；没有为 UA1 改 UI。未清空 App 旧 session/Outbox/Anchor；新 session 和单 Workout 验收命名空间独立。[device.json](../evidence/acceptance/device/ua1-20261105-a/device.json)

## 3. Cloud Environment

本机实际 SSH alias **rai-server**，BatchMode/StrictHostKeyChecking 验证已知 identity。目标有与仓库一致的历史独立测试目录 /opt/rai-cloud-test-20261101；历史 alias lh-gz 未被猜作当前配置。

实测 hostname **VM-0-12-ubuntu**；Ubuntu **24.04.4 LTS**、x86_64、2 CPU、RAM **1935 MiB**、swap **1987 MiB**；磁盘约 **50 GiB**，**20 GiB** 可用；Docker **29.1.3**、Compose **2.40.3+ds1-0ubuntu1~24.04.1**。部署前旧 RAI 测试 API/Worker/PG 三容器及另两个项目容器已清点；旧测试 schema19/PG18.6，仅检查版本、readiness 和配置，不读取旧健康事实。未发布服务器 IP、其他项目配置或 secrets。

## 4. Deployment

新目录 **/opt/rai-ua1-20261105**，project **rai-ua1-20261105**。沿用 release Compose 与既有 cloud-test 资源覆盖；独立 PG cluster、数据库/角色凭证、volume、内部 DB 网络。API/Worker 同镜像 **rai-ua1:20261105-9a8839d**；本地 Go 静态交叉编译、云 scratch 测试镜像构建，不冒充 registry 发布 digest。

空 public schema table count **0** → owner migrate → roles/runtime grants → 独立 dataset init → API/Worker。实际 **27 migrations / schema27 / 53 tables**；源文件和精确部署 binary/source hash 已核对。Goose ledger 记录版本，没有逐 migration checksum 列，不伪称该字段存在。初次编排漏执行 runtime grants 时 readiness 正确 503、Worker 未启动；按原授权脚本补齐后通过，保留失败记录。

API/Worker login 非 superuser、无 createdb/createrole/bypassrls/public schema CREATE；runtime role enforcement 开启。PG **18.6 (Debian 18.6-1.pgdg12+2)**，fsync、full_page_writes、synchronous_commit、data_checksums **全部 on**。API/DB/schema readiness 通过；Worker 有真实 SUCCEEDED candidate publication 和实际 queue progress。

文件/env secrets 按现有权限方式保存；没有输出 token/DSN/password。旧测试入口和 volume 未替换。丢 ACK 期间临时 API 同镜像 upstream 与 TLS 代理已撤下，标准 API 恢复。回退新入口可停止新 API/Worker，保留 PG/volume/审计文件；未执行 down -v、破坏式部署或真实数据备份导出。[cloud.json](../evidence/acceptance/device/ua1-20261105-a/cloud.json)

## 5. TLS / Network

独立 origin **https://tpandmr.xyz:19443**；PG 无发布端口。初次开发机握手失败、iPhone network_-1001，用户放行云安全组并确认手机 Safari /ready 后，复核外部 readiness **200**、证书 hostname/CA 验证通过；开发机观测 **TLSv1.2**，服务器 loopback **TLSv1.3**。证书实际到期 **2027-01-30 22:21:16 UTC**。手机真实 HTTPS 请求/回执成功，手机具体协商 TLS 版本未采集。

用户明确确认恢复测试使用 **Wi-Fi**；飞行模式+Wi-Fi off 与 Wi-Fi 恢复均有操作确认。没有关闭 TLS validation、使用 USB cloud tunnel、切换旧 443 入口或安排 cellular 测试。

首个 Workout hook 在网络类型确认前完成约 483 KB 上传。收到用户等待 Wi-Fi 指示后立即停止 App/API；该次网络类型 **UNKNOWN**，不记为 Workout Wi-Fi PASS。这是执行顺序风险，不能事后推定为 Wi-Fi。后续日常上传/恢复为已确认 Wi-Fi。[transport.json](../evidence/acceptance/device/ua1-20261105-a/transport.json)

## 6. HealthKit Authorization

真实 HealthKit available，独立 session 握手成功。先完成日常三类型请求，再由已有 hook 请求项目当前使用的完整只读类型；用户确认完成弹窗。toShare=[]，未增加权限，未 save/delete 健康数据。

请求：Workout、Workout Route、heart rate、walking/running distance、active/basal energy、running speed/power、step count、ground contact time、stride length、vertical oscillation、HRV SDNN、resting HR、sleep、VO₂max。无原生 running cadence 的当前 SDK 类型，不派生伪 cadence。授权请求完成与实际可见计数分别留证；**HealthKit 读授权状态仍不可直接判定**，空结果也不等于拒绝。

## 7. Workout Real Sync

既有只读 hook 定位一条已存在 Workout；**1 Workout、5,464 samples、5,464 associations、1 route / 1,768 points、1 import**，持久回执 ACKNOWLEDGED。duration/distance **present**；HR **349**。10 类样本数量与 server stored counts 对齐，client bundle SHA 与 server import SHA 一致；路线源 manifest 与服务端双 manifest 一致。首个 distance 检查因审计查询层级错误误报，按 payload.workout.totalDistance 修正后证实 present，没有改事实。

重启后相同命名空间复跑：discovery 仍 **5,466 events / 12 scoped anchors / 0 out-of-scope anchors**，两次连续读取未创建新 work；重新组包却触发 **workout_core_change_requires_review**，未产生第二个 import 或改变已存 Workout hash。**重复同步 BLOCKED / P1**。

实际持久化 originalWorkout 中有 **12 个整数字面量 quantity value 节点**；Normalizer 新读构造 number，而 JSON decode 先尝试 int，原始 enum 等号会将相同 canonical 数字误判不同。synthetic probe 复现“canonical bytes 相同、持久化后 enum 不相等”；实际字段结构支持此根因，未导出值，也未比较/放宽规则来强行通过。详细真实新旧 HK 字段差异仍待修复复验。

已保存 **1 个 task Workout anchor / 12 个 discovery anchors**；复跑路径在 core guard 前读取 existing Workout anchor。缺少独立 query-usedAnchor 埋点，且整体复跑失败，不给予完整 Workout 增量 PASS。[workout.json](../evidence/acceptance/device/ua1-20261105-a/workout.json)

## 8. Workout Stream Completeness

单条 Workout 的本地完成情况：

| Stream | 本地查询/持久化观察 |
| --- | --- |
| Workout | 1，complete |
| HR | 349，complete |
| Speed / Power | 695 / 667，complete |
| Ground contact / Stride / Vertical oscillation | 322 / 309 / 325，complete |
| Step count | 706，complete；不冒称 cadence |
| Active / Basal energy / Distance | 693 / 694 / 704，complete |
| Route | 1,768 points，complete |
| Quantity series | 本次无记录 |
| 原生 cadence / elevation sample | 当前 unsupported |

路线在当前 protocol3 单 bundle 内执行 manifest/chunk/seal/association，服务端 sealed、1 chunk、parent association present；**1,768 个投影点逐字段比较 mismatch=0**。未把旧分请求协议当作本次 wire 路径。

**REAL WORKOUT ALL-STREAM COMPLETENESS: BLOCKED。** 上述单条本地 coverage 不等于服务端可核验的全流 caught-up。既有 M6 real transport 缺口仍在，没有 syntheticCoverage 或 fixture override。

## 9. HRV / RHR / Sleep

固定 90-day lookback 内，本轮 query additions 与 server semantic counts 一致；每个 staged page 的 identity/interval 保留检查通过。

| 类型 | 实际存储/发现数 | 可见天数 | 来源数 | descriptor/algorithm/duration 分组 |
| --- | --- | --- | --- | --- |
| HRV SDNN | 3,128 | 65 | 3 | 571 |
| RHR | 82 | 71 | 4 | 5 |
| Sleep intervals | 1,529 | 49 | 3 | 4 |

分组是 source epoch 的结构观察，不代表物理设备数，也不替代每个模型的正式 epoch 解释。Sleep：asleepUnspecified **7**、awake **336**、core **715**、deep **204**、REM **267**、inBed **0**；跨日 interval **16**，不导出起止时间。Sleep/RHR 缺 device descriptor 的事实如实保留。

**Sync #2**：三类均 **0 additions / 0 deletions / usedCommittedAnchor=true**；relaunch 后原 anchor/window 指纹一致。其后 observation/recovery 机会自然再收到 HRV additions，不与“第二次空变化”截面混算。最终三类 bootstrapComplete=true、hasMore=false、pending=false、error=null。[daily.json](../evidence/acceptance/device/ua1-20261105-a/daily.json)

## 10. External VO₂max

**24 条真实可见 quantity samples 已观察，真实 projection BLOCKED。** 不是 NO REAL SAMPLE OBSERVED。受保护的阻塞 archive 指纹验证通过；结构检查后临时原始副本删除，公开只保留 schema/counts。

该页有 5 个 sourceRevision archive objects，13 条有 device descriptor。metadata keys 均在已知列表：SyncVersion 24、VO2MaxTestType 24、SyncIdentifier 24、AlgorithmVersion 13；前三类整数语义字段中的 numeric carrier 在 archive 中呈 float，而当前 registry 对这些键要求整数 NSNumber carrier。运行时 unsupported_metadata，旧 anchor 未提交、未上传任何 VO₂事实、blocked archive 保留。synthetic NSNumber(double) probe 同样被拒绝。

这是 typed metadata 兼容性审查材料；native NSNumber objCType/范围和逐字段投影还需修复阶段核对。未改 registry、丢字段、补造样本或猜 Apple/third-party/user-entered/unclassified。**fail-closed safety PASS；分类、同步和 external projection BLOCKED/NOT VERIFIED**。[vo2.json](../evidence/acceptance/device/ua1-20261105-a/vo2.json)

## 11. Recovery

真实 facts 已进入云端，Worker 实际计算，M7 candidate snapshot：

- product state **unavailable**；modelStatus **source_conflict**；
- reasons：baseline_insufficient、hrv_source_conflict、sleep_missing、source_context_incomplete。
- 另对显式验收前一日的 admin inspection 为 **collecting_baseline**、componentCount0，原因是该日组件/基线不足。两个 inspection 的日期选择语义不同，均未输出 score。

不以 raw sample 总量推断当日三组件完整，不合并来源冲突。**真实 facts → 明确缺失/冲突状态已验证；生理有效性未验证**。

## 12. Training Load

M7：**limited / partial**，quality insufficient；reasons **load_unknown、parameter_missing**。HR reference inputs **0**；HRmax/RHR 显式模型 reference 均未配置。RHR raw 样本存在不等于已配置参数来源；没有可比较的参数冲突样本。

未猜 HRmax、使用 220-age/default RHR 或为得到 AU 写入输入。实际 M1/M2 publication SUCCEEDED，历史 analysis_dependency_pending 失败尝试保留，之后重试成功；不把曾失败的 run 改名成功。

## 13. Running Ability

M7：**collecting / collecting**；reason **no_qualified_performance**。真实 Workout 已进入 M1/M3 路径；资格不足按冻结 qualification 保留，未放宽或合成成绩。未验证预测准确性。

## 14. Readiness

M7：**stale / stale**，quality insufficient；reasons **load_baseline_insufficient、load_sync_incomplete、recent_load_unknown、recovery_stale**。admin inspection knownLoadCount0、unknownLoadCount1、baseline collecting。这是实际计算/映射结果，未将先前预期状态冒充实测。

当前不能产生可用 Readiness 的工程/输入原因已显式可见：真实全流证明缺失、近期负荷未知、基线不足和 Recovery stale。未绕过 completeness 或填零。**computation/state mapping 已执行；real completeness capability BLOCKED**。[models.json](../evidence/acceptance/device/ua1-20261105-a/models.json)

## 15. Offline / Retry

真实三类 additions pending durable 后，API container unavailable 与飞行模式+Wi-Fi off 分别执行；batch/body/candidate anchor/committed anchor/sequence 均保持。恢复 Wi-Fi 后同 pending 继续。

丢 ACK 使用既有 M5A-RD 代理机制的临时 TLS 包装：保持真实 origin/证书校验，向同镜像受限 API 转发原 bytes；仅在 200 COMMIT 回执已产生后抑制响应并暂留重试。SQL 恰有 **1** committed batch，手机仍 pending、旧 anchor 未推进；SIGKILL/relaunch 恢复同 batch/body，随后原回执 digest 不变，候选 anchor 在 ACK 后提交。HTTP encoder 的换行与数据库原 receipt hash 按各自层级核对，没有重编码 pending。

Worker 停止时，raw daily receipts 已提交，分析仍排队（该截面 pending jobs **1,471**）；恢复 Worker 后排队工作追平。**ACK 不等待分析** 实证成立。[faults.json](../evidence/acceptance/device/ua1-20261105-a/faults.json)

## 16. App Kill / Relaunch

| 边界 | 真实执行 |
| --- | --- |
| K01 discovery 后、pending 未 durable | NOT EXECUTED — 无 deterministic hook |
| K02 pending durable、upload 前 | PASS — SIGKILL 后原三页恢复 |
| K03 upload 中 | NOT EXECUTED — 未对齐精确时点 |
| K04 server COMMIT 后、client ACK 前 | PASS — TLS hook + 实际 SIGKILL + 原回执恢复 |
| K05 receipt validated/local replacement 前 | NOT EXECUTED — 无 deterministic hook |

日常 relaunch anchor/window 恢复和第二次增量通过；Workout relaunch rescan 单独 BLOCKED。等待 Wi-Fi 时的一次停止不算精确 kill case。未清空旧状态或破坏 HealthKit。

## 17. Server / Worker / DB Restart

在真实 **1 Workout / 10,201 samples / 57 daily receipts / 1 Workout receipt / 1,768 route points / 7 tombstones** 的重启前截面，保存只读、限于新单设备验收库的 counts/hash；它不参与同步或下载水位。

PG acceptance container 停止时，API readiness **503**；真实手机请求失败且原 pending/body/anchor/sequence 保留，没有成功 ACK。此故障发生于 handshake/admission，**未注入事务提交中途断库**。

恢复 PG 并实际 restart API 后，**上述事实、删除屏障、回执和双路线 manifest 的计数/指纹逐项相同**。随后原三 pending 各恰有一条 committed receipt；在首 ACK checkpoint 精确提交原候选 anchor，后续额外空 coverage/自然变化另计。Worker 恢复，有实际 SUCCEEDED publication；最终截面 **1 Workout / 10,203 samples / 63 daily receipts / 1 Workout receipt，pending jobs0**。

没有重启云主机/iPhone、删除卷或制造物理断电。空环境的先期恢复证据另保留，不替代本节真实数据证据。

## 18. Background Delivery

真实注册 HKObserverQuery、background delivery；已有 hook 的 observer_registered 完成，三次 active-state callback 的 wake hint 均 durable。注册失败会显式报错，本轮无该错误。

**自然后台 wake: NOT OBSERVED。** 用户对“退主屏幕约30秒并重开”选择暂未执行；相应显式后台/relaunch **NOT EXECUTED**。不将初始 active callbacks 或手动 burst 叫作自然后台唤醒，也不要求 iOS 固定周期执行。

## 19. Delete / Update

RHR 首轮真实 anchored query 投递 **7 个 deleted objects**；删除屏障实际提交并在 PG restart 后保持。未主动删除或修改 Health 数据。这些是本轮 query 投递的删除，不证明删除行为刚在验收窗口内发生。

Workout 自然 delete/update、完整“已有事实删除→旧 add 重放不复活”及真实同 UUID revision 场景 **NOT OBSERVED / NOT VERIFIED**；未用 synthetic 事件补成真机 PASS。

## 20. Performance

单设备首个 Workout（毫秒；部分阶段包含子阶段，不能相加）：

| 阶段 | 实测 |
| --- | --- |
| HealthKit read | 14,425.23 |
| Route read | 336.49 |
| Bundle build | 16,328.62 |
| Gzip | 35.93 |
| Upload | 4,129.34 |
| Workout client total | 20,494.87 |
| Server import transaction | 1,893.26 |
| Server ACK total / ACK write | 2,779.01 / 0.23 |

JSON **4,330,123 bytes**，gzip multipart **483,243 bytes**。第二次三类空 anchored query 约 **3.19–6.60 ms**；各页 query/normalization/delivery 时延已留证。Worker M1 单次处理壁钟 **626 ms**；其余实际处理区间见 privacy 中 timing aggregates，含部分失败重试。

独立 outbox fsync、receipt validation、anchor replacement、完整连续 candidate E2E、手机 CPU/峰值内存/Instruments **NOT MEASURED**。故障停留、人工操作、准入预算和 Worker 暂停不能解释为正常性能/SLO。USB 断开时用户点击过同步，看到“仍待交付”；稍后本地三类 pending 和自动 Workout work 均0，但未精确关联断线期间的那一条回执，**USB 用例 PARTIAL**。用户未执行 UI responsiveness 观察，不声称不卡顿。

云端一次观测 API **6.72 MiB / 0.00% CPU**、Worker **9.10 MiB / 0.03%**、PG **88.95 MiB / 19.99%**；上限256/384/512MiB，磁盘约20GiB可用。不是峰值、容量结论或 SLO。

## 21. Privacy

公开材料仅型号/OS/build、版本、counts/statuses/hashes/timings/reasons。没有健康值、睡眠起止、GPS、Workout/sample/route UUID、设备唯一标识、签名账户或 secrets。route-specific dictionary keys 已转 hash。必要本地 state/blocked archive 只在仓库外私有临时目录检查结构，随即删除；原保护状态仍在手机，未成为 fixture。

保留主 API/Worker 日志审查：**1,006 JSON events + 4 条标准库 TLS handshake warnings**；未命中所查健康字段、full UUID、source bundle 或真实 enrollment/DB credentials。另审查捕获的临时 upstream 子集；未声称已保留被移除容器的每一条日志。4条非JSON日志已分类，不含所查健康字段；公开不含其中地址。既有 PG 日志抑制健康参数/错误详情的配置保留。[privacy.json](../evidence/acceptance/device/ua1-20261105-a/privacy.json)

## 22. Risks

1. **P1：Workout 重启复跑 false core change**，阻止可靠重复同步；实际持久化数字结构和 synthetic probe 支持 codec/enum 比较根因。先修传输工程并在原命名空间复验，不能清状态绕过。
2. **VO₂ metadata capability BLOCKED**：24 条真实样本受保护地 fail closed；需要 typed registry/native NSNumber 契约审查，不能丢字段或推断来源通过。
3. **M6 all-stream real transport 缺口**仍关闭对应门禁，不能由单条本地 coverage 替代。
4. 第一条 Workout 网络类型未确认；USB 回执时点和 UI responsiveness 未独立验证；K01/K03/K05、自然后台/完整删除重放未覆盖。
5. HR reference 缺失、HRV 来源冲突、缺 device context 和 baseline/freshness 使模型无可用值；不是生理不准确的证据，也不是应放宽资格的理由。
6. development profile 近期到期；Debug SDK warning 保留。历史 dependency_pending 失败 run 与首次 grants/readiness 失败均保留。验收只读比较器已纠正 distance 层级、HTTP receipt 换行和“首 ACK 与后续 discovery checkpoint”的差异，未改变事实以匹配断言。
7. 单设备、有限窗口、独立测试云，不能外推多用户容量、长期存储/电量或生理有效性。未观察到的 P0 不等于未覆盖场景已证明安全。

## 23. Not Observed

自然后台新事件唤醒；Workout 自然 delete/update；完整 raw fact 被删除后旧上传重放的自然机会；参数来源冲突（当前无显式 HR reference）。VO₂max **已经观察到样本**，不能列入无样本项。日常第二次 query 的0变化与之后自然 additions 均按实际各自保留。

## 24. Not Verified

Workout 可成功重启复跑和完整增量；VO₂逐样本 metadata/provenance/origin 的完整投影；M6 real all-stream transport；K01/K03/K05；真实事务中途断库、物理掉电；精确 USB 断线回执关联、显式 background/relaunch、手机 UI/CPU/内存/功耗；长期离线/删除保留期、cellular、多设备/人群、生产、整机重启、生理校准、candidate promotion/产品激活。

已完成最终工程检查：Go1.27.3 **go test -race -count=1 ./...、go vet ./...、258包依赖边界**；真实隔离 PG18.6 的 -race -count=1 -tags=integration ./internal/admin ./internal/storage/postgres，leftover DB0 / cluster removed；Swift **132 tests / 17 suites**；签名 build。诊断 synthetic probe 单独标明，不代替真机证据。无事务/可执行代码变更，未把先期模拟/fixture 历史移作本轮 PASS。[validation.json](../evidence/acceptance/device/ua1-20261105-a/validation.json)

## 25. Decision

    UA1 RESULT: BLOCKED

    REAL-DEVICE ENGINEERING:
    PARTIALLY VERIFIED — WORKOUT REPEAT / VO2 COMPATIBILITY OPEN

    REAL DATA AVAILABILITY:
    OBSERVED ON SINGLE DEVICE ONLY

    REAL WORKOUT ALL-STREAM COMPLETENESS:
    BLOCKED

    PHYSIOLOGICAL VALIDATION:
    NOT VERIFIED

    PRODUCT ACTIVATION:
    NOT AUTHORIZED

真实日常传输、故障恢复、Worker 和 candidate 状态映射有直接证据；完整可靠工程目前不能接受。下一步应先独立修复/复验上述工程问题和 real completeness transport，再评估是否值得启动 UA2。**本轮停止，不自动修规则、调参、启动 UA2/UA3/UA4、shadow 或 activation。**
