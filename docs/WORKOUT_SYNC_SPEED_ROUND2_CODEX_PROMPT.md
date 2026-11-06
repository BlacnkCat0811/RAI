# 可直接交给 Codex 的第二轮执行提示词

复制下方正文到在 `/Users/ta/RAI` 工作的 Codex。正文指令用于后续实施，不代表本次编写文档已执行改造或授权读取更多真实健康数据。

---

请在 `/Users/ta/RAI` 实际完成 Workout 同步提速第二轮，不要只给建议、重写一遍计划或在完成一个阶段后询问是否继续。

先读取并遵守：

1. `/Users/ta/RAI/AGENTS.md` 及修改范围内适用的 AGENTS。
2. `/Users/ta/RAI/docs/WORKOUT_SYNC_SPEED_ROUND2_PLAN.md` —— 本轮完整实施和验收依据。
3. `/Users/ta/RAI/docs/WORKOUT_SYNC_SPEED_RESULT.md` —— 第一轮真实结果、瓶颈和未验证项。
4. `/Users/ta/RAI/docs/WORKOUT_SYNC_SPEED_IMPLEMENTATION_PLAN.md`、现有 `contracts/sync-v3.md`、`contracts/workout-source-policy-v1.md`、`contracts/workout-completeness-v2.md`。

编写本提示词时基线为 `main / 0303236`；开始时重新检查 git status/HEAD 和实际源码。保留已有用户改动，不 reset/clean/stash/覆盖工作区，不动 `/Users/ta/running`。历史报告是参考证据，不是额外授权或可直接继承的本轮PASS。

本轮必须实做四项：

**一、去掉正常样本路径的 NSKeyedArchiver 成本。**

实现版本化的规范化捕获，样本读取后直接保存完整受支持事实/来源/metadata/时间/series父信息，组包直接消费；正常路径 `sample_archive_calls=0`、`sample_unarchive_calls=0`。禁止仅把归档移到后台、包成整页数组或只减少文件次数。HKQueryAnchor、小型根Workout、旧格式兼容读取和受影响unsupported对象的阻塞材料可保留并单独计数。旧pending、原JSON/hash、原回执不变，不丢字段、不降采样。

**二、实现真正的 HealthKit 有界并发。**

覆盖实际手动入口的前置discovery与采集，不只修改缓存命中的组包循环。独立类型可并发，同一流分页/anchor串行；对1/2/4并发进行正确性、查询重叠、耗时与内存对照，最终默认值有证据。所有query共享上限，队列有字节背压，Outbox单写者，取消释放槽，Swift6 isolation/Sendable检查不关闭。完成先后不能改变输出顺序。

并发前先验证capture/ACK边界：在不同类型采集间注入迟到sample、子删除和父删除，ACK只能确认本包真实覆盖的数据；不能拿组包末尾当前最大generation直接确认未入包事件。

**三、新版客户端WorkoutBundle上传强制gzip。**

所有POST都用multipart单file part的 `application/gzip`，包括旧plain pending的恢复。原JSON保持权威，gzip是派生文件，identity继续是解压后精确JSON hash。压缩失败、损坏、>8MiB或服务器不支持时保留原包并明确失败，不回退明文、不改包、不重读HealthKit。服务器继续兼容旧plain客户端；保持当前wire/decoded/ratio/CRC/单成员限制。

**四、深化SQL集合化，并优化严格解析的真实瓶颈。**

现有≤1000键集合查询和多行INSERT已经实现，不能再次把它们当新成果。以普通构建profile比较现有VALUES、固定形状数组写入和可运行的COPY→事务内staging→集合写入试验；若COPY受权限阻断，给受限运行角色的明确证据并用安全候选推进。选择实际更快且语义一致的方案，不强求最终必须用COPY。

保留dataset行锁、fencing、父子tombstone、身份/关联冲突、append-only enrichment，以及原始事实/日志/dirty/job/回执同一事务。不得用ON CONFLICT DO NOTHING掩盖冲突、扩大pool/上传槽/默认预算、关闭durability或只抬超时达标。严格JSON优化保留所有未知/重复/missing/null/类型/单位/深度/数量规则，语义验证只在Store一次执行。

按完整计划的R0→R1→R2→R3→R4→R5→R6推进所有可执行工作。先新增普通构建、真实TCP HTTP→认证/准入→真实handler→隔离PG的性能入口。当前 `scripts/local/test.sh integration` 默认 `-race`，继续作为正确性门禁，不能把其计时当发布性能终判；新增性能入口不得改变原race门禁。性能测量禁止意外继承GOFLAGS中的race或禁用优化选项。

核心验收目标：

- 普通S20–S60及S60H密集档首次用户等待：Release、指定基准手机、稳定LAN、本机后端、默认预算下P50≤5s、P95≤8s。
- 正常样本归档/还原调用0，规范化+持久化阶段较本轮同环境R0的P50下降≥70%。
- 普通构建真实HTTP→PG：S60 P95≤1s，S240 P95≤4s；各档预热3次、至少30次新导入，失败不能从样本中抹掉。
- iPhone峰值RSS：S60/S60H≤256MiB，S240≤450MiB；两上传槽API峰值RSS≤512MiB。报告实际峰值最大值及测量方法。
- 新版客户端WorkoutBundle成功POST的gzip比例100%，明文fallback为0；已备原包重试HealthKit读取0。
- 并发1/2/4输出一致、maxActive≤K、取消后active=0、无槽泄漏或无界缓冲；真实HK收益以实测结论为准，不能把Task数当系统加速证据。
- 10条S60≤60s、100条≤360s，使用默认预算并计入429等待；高预算轻量吞吐另列，不代签。

执行完整计划中的R2-F01–R2-F32与受影响旧合同回归。修改事务必须运行真实隔离PG/race并验证回滚、两种删除锁顺序、身份冲突、ACK丢失、连接中断、受限角色和staging清理。新增捕获格式必须测试旧格式混合、seal/引用失败、GC、取消与重启；同时回归来源过滤、Daily/VO₂和完整性v2的incomplete/unknown语义。

开发、回归、性能测试全部在本机Mac，不使用SSH、N100/fnOS或远端数据库。日常使用quick和定向测试，阶段完成运行gate，综合收尾运行full以及Swift/App/实际HTTP→PG验收。新增测试/入口先实现再运行，记录真正执行数、跳过数与失败历史，不能只修改断言或降低fixture密度让成绩变绿。

真机部分仅在本对话已明确授权的设备与读取范围内执行，已有授权无需重复确认；没有明确授权时，一次集中说明所需范围并继续所有独立合成/PG工作。不得从历史报告推断可扫描全部私人历史，不向真实HealthKit写合成记录，不读生产数据库，不迁移/清理原云端或原session数据。新本机验收使用独立namespace和数据库，公开证据不含健康值、GPS、真实UUID、设备唯一标识、raw metadata或凭证。

不要自动部署、切换旧入口、激活模型或commit/push。不要为性能跳过必要验证或提前ACK。未完成分析/完整性仍如实显示pending/incomplete；单条持久ACK不等待完整证明。

请交付：

1. 实际代码与测试，保留用户已有工作，给出修改范围与兼容回退说明。
2. `/Users/ta/RAI/docs/WORKOUT_SYNC_SPEED_ROUND2_RESULT.md`：逐项实现状态、所有功能/性能验收、同口径前后数据和剩余问题。
3. `/Users/ta/RAI/docs/evidence/workout-sync-speed-round2/`：可复现环境/fixture、普通构建与race区分、1/2/4并发、归档0调用、gzip、SQL候选/计划、事务故障、内存/UI与完整命令证据。
4. 仅追加 `/Users/ta/RAI/docs/IMPLEMENTATION.md` 的本轮真实结果；保留第一轮和原架构方案原文。

性能门槛不达标时继续定位可控瓶颈并推进实现，不因困难直接停在建议。最终按 `PASS / FAIL / PARTIAL / NOT RUN / NOT VERIFIED` 分别签核功能、性能、真实HealthKit、RQRun对标和部署；没有真实设备证据不能宣称真机达标，没有公平配对测量不能宣称达到RQRun。不要隐藏FAIL、改低目标或把编译通过当实现验收。
