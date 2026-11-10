# UA1 — Real Device & Real HealthKit Acceptance Plan

版本：`UA1-plan/1`；2026-11-05。**READY BUT DEFERRED / NOT EXECUTED**。此处只有未来执行设计，不连接 iPhone/Watch、不打开 HealthKit、不读取真实健康数据，不表示任何用例已通过。总门禁与隐私遵循 [UA0 合同](UNIFIED_ACCEPTANCE_CONTRACT.md)。

## 1. 目标、授权与执行准备

只证明真实输入能产生、读取、同步，删除/修订行为与协议假设相符，离线、后台、kill 后能恢复。UA1 不证明 Load 科学准确、Ability 已校准、Recovery/Readiness 有效。

启动前需要用户明确恢复真机验收条件、数据访问授权和隔离验收环境；记录 owner/tester/reviewer、case scope、观察窗口、退出方案。只读 HealthKit；不安排专门运动、不手动补样本、不修改/删除用户健康数据。任何未来人工 delete/update 或 time trial 都另需独立明确授权和安全方案。WorkoutKit 排程授权独立，不能借读权限扩权。

优先使用隔离 backend/dataset，不连接生产库、不切旧入口、不做真实数据迁移。校验当前构建/部署由另行授权任务完成；UA1 的计划不授予部署权限。只使用现有已实现受控观察能力；缺少精确 kill checkpoint 或安全观察工具时 BLOCKED 该 case，另立工程任务，不能假装已有 fault hook。

## 2. 设备与版本矩阵

| 字段 | UA0 记录 | UA1 实施时要求 |
| --- | --- | --- |
| iPhone | 目标至少 iPhone 12 Pro Max | 实际型号；无设备唯一标识 |
| Apple Watch | 实际可用 Apple Watch，型号未知 | 实际型号、与 iPhone 配对状态 |
| iOS version/build | 未记录，不假设版本 | 从该次设备实测记录 |
| watchOS version/build | 未记录，不假设版本 | 从该次设备实测记录 |
| RAI app build | 尚未选择验收构建 | 版本、build、源码 revision、构建配置 |
| backend HEAD / image revision | UA0 HEAD 仅是文档基线 | 当次实际运行 HEAD 与镜像 revision |
| schema version | 源码基线最高 27；非部署证明 | 当次隔离库实际 applied 版本 |
| 协议 / registry / 模型 | 遵循冻结 v1 | 实际支持版本/config、collector 状态、timezone |
| 网络 / app 状态 | 未执行 | 网络类型、前台/后台/suspend/kill、恢复触发 |

该组合是最低目标，不代表所有硬件/OS 均覆盖。每个运行用匿名 run alias 串联；不收集 serial、UDI、device localIdentifier 等。OS/app/backend/schema 变化需标新 run；不能把旧设备报告自动套用。

## 3. 观察和留证方式

每 case 先冻结前态，再执行获准操作，最后核对 HealthKit 可见页、protected pending、server durable commit、receipt 及 local committed anchor 的关系。仅在受控环境内查看必要真实值/身份；公开证据只记数量、身份/bytes/回执一致性布尔、顺序和状态，遵循最小化。

空结果不能证明已获 HealthKit 读权限，也不能证明某用户无该数据。分别记录授权请求是否成功、用户确认的设置和实际查询可见性；不推断不可见数据。新增样本可能延迟到达，必须预先给每个 case 规定观察时长、网络恢复/前台重试机会与允许重试次数。该时长是试验观察预算，**不是 iOS 每 N 分钟执行保证**。到期无足够可见机会为 INCONCLUSIVE；在有效恢复机会后仍不推进则按失败处理。

所有 mutation/replay 只针对受控服务器验收数据；精确 UUID/receipt/body 不写普通日志或 git。常规测试仍只用 synthetic，不把真实验收数据变成永久 fixture。

## 4. Workout Sync 用例

| ID | 如何验证 | PASS 条件 / 失败处理 |
| --- | --- | --- |
| W01 discovery | 对正常产生且已可见的 running Workout，观察 incremental discovery 与分页 | 可见且受支持事实均入 durable pending/commit，重复 discovery 不新增语义事实；空页不等于完整历史 |
| W02 samples | 核对关联 samples/series、迟到、分页、summary-only 边界 | 支持字段保留，缺流/等待可解释；不把 summary 或瞬时 count 冒充全流完整 |
| W03 route | 对自然存在 route 的 Workout 跟踪页/chunk/seal/association | route 完整关联，无遗漏、重复语义或未经支持字段裁剪；无 route 为 NOT OBSERVED |
| W04 incremental anchor | 多页、app 重启、相同页重试，对照本地 committed 与 candidate anchor | 仅在全部对应 durable ACK 验证后提交 anchor，跨类型/stream 不串位 |
| W05 delete / revision | 自然出现合法事件时，记录 UUID/同步版本事件次序及旧页重放 | 按当前不可变协议处理，删除屏障保留，旧 add 不复活，受影响 current pointer 撤回；未出现记 NOT OBSERVED |
| W06 lost ACK | 在受控验收传输中，server COMMIT 后抑制/断开响应，再重启重放原 pending | 相同 bytes、batch 和原 receipt；只有一次语义提交，本地 anchor 最终正确 |
| W07 relaunch / offline | 结合 kill/offline 矩阵核对恢复前后 | 无丢失、不提前 anchor、不跳过 pending，增量继续 |
| W08 all-stream catch-up（最高优先级） | 列出 current collector 的所有 Workout discovery/sample/route/association 相关流与 pending/hasMore，验证服务器可核验的完整性声明及失效 | 有 reviewed real transport，全部相关流按其合同追平，manifest 变化/迟到/新页使旧证明失效；缺 transport 时 BLOCKED，普通 M6 维持 sync_incomplete |

**已知工程前置缺口：** [M6 合同](TRAINING_READINESS_CONTRACT.md) 明确现行 discovery 的 hasMore/anchor 留在客户端，服务端没有可验证 all-stream caught-up assertion。现有 receipt、registered stream、query fingerprint、一次上传成功均不足以证明完整。`syntheticCoverage` 只允许隔离 synthetic fixture，不能用于真实 UA1、UA2 或 product。UA0 不实现该 transport，也不承诺 UA1 仅靠测试就能关闭缺口；需单独评审并实施后复验。

## 5. Daily Health

| ID | 类型 | 观察项目 | PASS 范围 |
| --- | --- | --- | --- |
| D01 | HRV SDNN | real sample counts、来源分布、每天采样密度、缺失、source epoch、delete/update | 可见受支持样本与 durable fact 一致；不推断晨间/夜间上下文，不把缺失补正常 |
| D02 | Resting Heart Rate | 每日可见条数、同源版本、跨来源冲突、来源变更 | 不平均冲突；revision 按不可变 UUID/删除语义，不猜唯一真实值 |
| D03 | Sleep | source-separated intervals、stage/awake/inBed、缺口/重叠、跨午夜、迟到尾段 | 原始区间受支持字段完整；episode/coverage 限制如实暴露，不能把发现 quiet gap 称生理最终完成 |

数量/来源/密度只在受控观察中计算，公开证据用合并计数/区间；sleep 精确起止时间不得进入日志/仓库。缺少某类真实样本只能记 unavailable/NOT OBSERVED，不人工补造来让模型有值。模型资格比例交给 UA2；UA1 管道通过不等于三组件完整日已足够。

## 6. External VO₂max 与来源分类

V01 逐项核对：HealthKit authorization / 实际可见性、sample availability、actual sourceRevision、device descriptors、testType、metadata registry version/实际 key+typed tag、provenance 分类、来源选择、delete/update sequence。受控原始核对与公开脱敏结果分开；device.name/source.name 可能带用户命名，不公开。

| 现实类别 | 如何验收 |
| --- | --- |
| Apple Watch estimate | 将实际 descriptors 与 `rai.healthkit.vo2max.origin/1` 冻结规则核对，不仅看 source.name 或 Apple 前缀；不称硬件认证 |
| third-party estimate | 保留来源事实，核对第三方规则和默认 projection 排除行为；device 声称 Apple 不变为 Apple 来源 |
| user-entered | 自然存在时验证显式 manual 标志优先，不人为创建样本 |
| unclassified | 信息不足、未知组合、testType 缺失等保持未分类，不能猜 Watch/laboratory |

某类别现实未出现记 NOT OBSERVED，禁止把 synthetic 用例换成 real PASS。未知 metadata key/type/enum 或单条超限：**FAIL CLOSED**，保留 pending/旧 committed anchor，不静默删除字段、不重编码已提交 pending，不在日志打印未知值；另立 compatibility review。有效但未分类可按现有 raw 协议保存，与 unsupported metadata 阻塞区分。classifier 与可信上下文不符视为 source misclassification，BLOCKED。

自然更新时只记录真实事件顺序；testType=1 不证明 laboratory ground truth。来源 epoch 只是描述符等价组，不能识别同型号的物理设备。多来源选择/冲突必须按 [M4 合同](VO2MAX_EXTERNAL_CONTRACT.md) 保持显式/null，不平均、择高或自动切源。

## 7. Background / 生命周期矩阵

| ID | 状态 | 执行与观察 | PASS 条件 |
| --- | --- | --- | --- |
| B01 | foreground sync | 有正常可见新页时运行 | 最终推进；无重复语义提交 |
| B02 | background observer | 用户授权环境下正常退后台，记录可观察回调/进度 | 系统提供机会时可推进；无机会为 NOT OBSERVED，不要求固定周期 |
| B03 | app suspended | 恢复后台/前台后检查 durable state | 从 protected pending/anchor 恢复，不丢页 |
| B04 | app terminated | 区分用户 force quit 与系统终止，不假设 force quit 后自动唤醒 | 记录实际机会，用户重新启动后可恢复；不能承诺系统必唤醒 |
| B05 | network unavailable → restored | 后台断网后恢复，再给真实执行机会 | 重试原 bytes 并最终推进，无提前提交 anchor |

只验收 eventual progress / recovery；没有 iOS 每 N 分钟必运行条款。无新样本/无系统执行机会不冒充后台成功。故障停留条件、观察预算和前台恢复尝试在 run 前冻结；有效恢复机会后仍无法恢复属于 BLOCKED。

## 8. App Kill 五点矩阵

在真实 iPhone 上逐点观测，必须能够证明 kill 发生在相应边界；只凭“杀了几次 App”不能通过。

| ID | kill 边界 | 重启后必须成立 |
| --- | --- | --- |
| K01 | discovery 后，pending body 尚未 durable | committed anchor 未前移；重查原页，不丢事实 |
| K02 | pending body durable 后 | 从原 immutable pending 恢复，不改变内容/身份 |
| K03 | upload 中 | 查明未提交或已提交；原 pending 安全重试，无重复语义事实 |
| K04 | server commit 后、ACK 前 | 原批次返回原回执，不重复 commit |
| K05 | ACK 已收到验证后、local commit 前 | 原 pending/旧 anchor 可恢复；重放得到同 receipt，随后原子推进 |

统一 PASS：no data loss、no duplicate semantic fact、anchor not advanced early。需要受控边界工具时先评审隐私和只读 HealthKit 约束；本计划不声称现有 iPhone 已具备所有 checkpoint。macOS SIGKILL/Simulator/隔离 PG 测试只作 A 层支持，不能替代真实 iPhone 这五点。

## 9. Offline 矩阵

| ID | 场景 | 恢复后核对 |
| --- | --- | --- |
| O01 | airplane mode | pending bytes/batch 保持，重连返回同 receipt 或首次合法 commit |
| O02 | server unavailable | 失败与 retry 状态可恢复，不跳页、不重建已提交 body |
| O03 | Wi-Fi/cellular switch | 传输中断时遵守幂等，增量继续 |
| O04 | longer offline period | run 前冻结离线时长，跨 app relaunch 后重放、追平，保留删除可见性/系统保留期限制 |

对未 commit 的页不存在“原 receipt”；报告必须区分首次 commit 与已 commit 重放。同一已提交批次必须同 receipt。长期离线不能据一次观察承诺 HealthKit 永久删除保留或任意历史完整性。

## 10. Delete/update 观察与总退出门禁

未来真实合法 delete/update 才执行观察核对；无事件记 **NOT OBSERVED**。更新不能原地改同 UUID；现实若表现为 delete+new UUID、同 UUID 不同内容、SyncIdentifier/Version、分页乱序等，分别对照当前协议，身份冲突应 fail closed，不能悄悄覆盖。协议无法表达的现实合法行为交 separate review，保留未完成状态。

UA1 完整范围 PASS 必须：设备矩阵实填、所有必测 case 有直接真实证据、零丢失/复活/提前 anchor/来源误分类、恢复成立、W08 real transport 已接受、隐私审阅通过；缺样本/自然事件/kill 可观察性只能形成有限范围结论，相应 gate 关闭。任何 raw data loss、delete resurrection、early anchor commit、source misclassification、background recovery failure → **BLOCKED**，保存脱敏故障证据，停止 UA2→UA3 晋级，不自动改公式或数据。

证据命名：`evidence/acceptance/device/<run-alias>/`；记录逐 case expected/observed/status/reviewer，模型值不进普通日志。当前全部用例 **NOT EXECUTED**，UA0 完成后不启动本计划。
