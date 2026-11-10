# Workout 同步提速与 Apple Watch 来源过滤实施方案

日期：2026-11-06（Asia/Shanghai）

状态：**实施设计与验收标准；优化代码尚未实施，性能目标尚未验证。**

本方案落实两个目标：缩短用户等待一条跑步记录同步的时间；在读取详细健康数据前过滤非 Apple Watch 原生跑步记录。执行顺序是先减少重复采集和存盘，再合并重复校验、优化数据库批处理。保持现有原始事实、删除、幂等和事务回执语义。

本文是后续实施依据，不是实现完成报告。本次编写文档不启动真机读取、部署、真实数据迁移或产品模型激活。原《RAI-Go-PostgreSQL架构重设计与实施方案》与历史验收报告保留原文。

## 1. 依据、现状与证据边界

### 1.1 输入材料

| 材料 | 用途与限制 |
| --- | --- |
| [RQRun 同步接口文档](/Users/ta/rea-captures/rqrun/RQRun-同步接口文档.md) | 两次抓包的行为参考；文中设计建议不是 RAI 的实施指令；没有客户端源码、服务端实现或完整采集耗时证据。 |
| [originalRecode.schema.json](/Users/ta/rea-captures/rqrun/originalRecode.schema.json) | 五份样本反推的结构；`required` 不是经过验证的服务端必填合同；不能把真实样本复制为合成测试 fixture。 |
| [现有 v3 同步合同](../contracts/sync-v3.md) | 当前整包上传、精确字节身份、删除与回执边界。 |
| [来源策略 v1](../contracts/workout-source-policy-v1.md) | 当前冻结的 Apple Watch 原生来源分类。 |
| [完整性 v2](../contracts/workout-completeness-v2.md) | visible / eligible / excluded 清单及完整性证明。 |
| [本机开发与真机结果](LOCAL_DEVELOPMENT_RESULT_2026-11-06.md) | 最近一次本机证据及未验证项。 |
| [历史性能审查](audits/RQrun_PHASE4_PERFORMANCE_REVIEW.md) | 历史分段测量参考；不同日期、硬件与路径的数据不能合并成同条件提速倍数。 |

编写时 checkout 为 `main / 533fa82`，已有多项未提交修改。实施时重新记录 HEAD、已有 diff 和相关文件 hash；保留用户现有工作，不 reset、clean、覆盖或整包提交无关修改。

### 1.2 已确认的差异

| 环节 | RQRun 已观察行为 | RAI 当前行为 | 本方案决定 |
| --- | --- | --- | --- |
| 上传颗粒 | 一条 workout，一个 multipart JSON | 已有一条 WorkoutBundle、一次 POST，并支持 gzip | 沿用，不退回逐 sample 网络请求。 |
| 成功响应 | 返回记录 ID，解析状态另查 | 原始事实与分析待办持久提交后返回回执，Worker 异步处理 | 保持持久回执；同步完成不等待分析或全局证明。 |
| 客户端处理 | 内部归档和校验未知 | discovery 后组包再次读取；样本分别归档；series/路线逐点刷盘 | 优先合并读取、批量持久化。 |
| 来源过滤 | 本次只观察到 Apple 来源，过滤算法未知 | 组包拒绝不合格来源，列表和前置 discovery 未完整提前过滤 | 把来源判定移到详细采集之前。 |
| 数据身份 | 同字节重放得到相同记录 ID | 每个 sample/route 的不可变身份、显式删除与回执 | 保留 RAI 身份能力；优化执行方法。 |

RQRun 上传请求起始时间相隔约 2～3 秒，不等于“从读取 HealthKit 到可靠同步只需 2～3 秒”。不得从抓包推断其没有校验、已经完成可靠落库，或用其缓存命中与 RAI 首次导入比较。

### 1.3 当前性能证据

- 最近本机独立真机 single 路径：总计 **18.421s**；采集/组包累计 **12.050s**，其中 quantity/series 读取及捕获 **10.106s**；上传/确认 **5.676s**。子项有包含关系，不能重复相加。
- 重启后的重复核验 **12.652s**，精确复用原 bundle，服务端无重复事实。说明“最后没有新增写入”仍可能付出较重的重读成本。
- 本机 500 条轻量合成 bundle 共 **20.955s**，每条只有 60 scalar / 3 series points / 3 route points，使用单独有限 benchmark 预算，Worker 未计时。该结果不能代表 500 条密集真实跑步记录。
- 历史关联算法优化已落在当前工作区，约 75 倍收益只属于单独的合成关联步骤，不能作为本方案的新增成果。

以上均是历史报告数据，本次未重新测速。常规手动入口包含额外前置 discovery，因此 P0 必须重新测量完整用户路径。

## 2. 范围与固定约束

### 2.1 交付范围

1. 只读 HealthKit 的原生 iOS 客户端；跑步记录列表、手动同步、增量发现和恢复。
2. Apple Watch 原生 running Workout 的提前筛选；第三方和手工录入的明确排除记录。
3. 同一个 Outbox 内的批量捕获、崩溃恢复、采集复用、编码优化。
4. Go 模块化单体与 PostgreSQL 18 的校验合并、集合式身份检查及批量写入。
5. 普通同步、异步分析、全量完整性证明的独立状态和调度。
6. 本机合成测试、真实隔离 PG 验收、性能基准，以及另行获得授权后的真机验收。

不新增 Web、Watch 或桌面客户端；不改模型公式、健康参数或模型资格；不将第三方记录纳入负荷域；不自动迁移真实数据、部署或切换旧入口。

### 2.2 不变的正确性边界

- 原始事实、明确删除屏障、提交日志、dirty/job 与最终回执同一事务提交，dataset 行锁确定提交顺序；不用 sequence 作下载水位。
- 同 UUID 的事实内容不可被普通上传覆盖；同字节重试返回原回执，不重复增加 revision/job。
- 删除优先于旧上传、缓存、重试与旧回执恢复；快照缺席和空查询不产生删除。
- 不支持的字段和 metadata 留在本地阻塞项中，不静默丢弃，不伪装成功。
- 保留现有 body、解压、数组与点数上限，以及有限的超时、连接池、上传槽和请求预算。
- 不通过备份、恢复、全库摘要、全量重算或管理工具完成同步；application/httpapi 不导入 admin。
- 同步成功不表示分析完整、生理模型有效或产品模型已激活；Worker 不得假装完成分析。

## 3. 目标流程与成功定义

```text
Workout 根记录发现（保留原查询身份与可见清单）
    → 来源分类与最小证据持久化
        → excluded：停止详细采集，记录排除状态
        → unclassified：该条待识别，继续其他合格记录
        → eligible：排队采集
            → 复用完整本地基线 + anchored 增量 / 首次建立完整基线
            → 分批保存 facts、series、route，生成一次完整捕获结果
            → 编码并持久保存不可变 WorkoutBundle
            → gzip 派生文件 → 一次 POST
            → 同事务 durable COMMIT → 验证回执 → 本地确认
                → UI 显示“已同步”；分析结果异步更新

完整性证明：独立低优先级任务，继续原 v2 合同及其专用证据采集
```

三种状态独立表达：

| 状态 | 达成条件 | 不得混淆的情况 |
| --- | --- | --- |
| 单条已同步 | 该包服务端已提交，客户端验证并持久保存对应 ACK | 网络已发送、HTTP 202、旧分析结果均不算。 |
| 分析可用/部分可用 | 对应 generation 的实际分析发布，缺失项如实显示 | ACK 不直接把 analysis 改成成功。 |
| 当前范围完整性已验证 | 原 v2 manifest 经服务器验证并有持久回执 | 单条 ACK、空扫描、排除了第三方均不能代替。 |

一条来源或 metadata 异常不终止其他 eligible Workout；但是不能因此把全量完整性或训练负荷完整性标为已验证。

## 4. Apple Watch 来源过滤

### 4.1 本轮产品范围

沿用 `rai.workout.source_policy.apple_watch_only/v1`：**Apple Watch 系统体能训练产生的 running Workout**。设备昵称不是依据；HealthKit 来源、productType 与设备描述共同参与现有冻结分类器。

当前规则比“所有在 Apple Watch 上运行的 App”更窄：第三方 watchOS App 写入的 Workout 仍属于第三方来源并排除。未来如需支持，另立来源策略版本、测试 vectors 与相应完整性合同；本轮不原地改写 v1。

| 分类 | 列表与采集 | 服务器与完整性 |
| --- | --- | --- |
| `apple_watch` + running | 默认列表可选；允许详细采集 | 既有 v3 原始事实与回执。 |
| `third_party` / `user_entered` | 默认列表隐藏或不可选；提供简单排除说明；不读取详细子数据 | 不创建 Workout import、sample/route 事实或分析任务；v2 保留 exclusion evidence。 |
| `source_unknown` 等未分类 | 作为待识别项，不允许直接上传，其他记录继续 | 不冒充 excluded；存在时 v2 仍不能最终提交。 |
| 明确删除对象 | 按既有父子删除规则处理 | 不因当前来源筛选跳过已有事实所需的删除屏障。 |

sample/route 的 `associated_apple_watch` 仅在已证明的 Apple Watch 父 Workout、匹配的来源与版本关系下沿用当前子事实规则；不能把它当作根 Workout 的 eligible 分类。

### 4.2 提前执行的位置

1. `HealthKitClient.recentWorkouts`：返回可选列表前分类。分页获取根记录并累计合格项，避免先取 100 条再过滤而漏掉后面的合格记录；每次读取有固定预算，未扫完可继续加载，不在主线程无限查全历史。
2. `SyncCoordinator.discoverChanges`：根对象及最小来源证据保存后、任何 quantity/series/route 子查询之前分类；明确排除项不建立新的详细采集任务。
3. 手动提交与 `performSync`：沿用同一分类器作入口保护，处理列表之后来源变化或旧任务；正常情况下不应走到“读完所有子数据才拒绝”。
4. 服务端继续验证来源语义，不能仅接受客户端 `eligible=true` 或排除布尔值。

**不得直接给既有根 anchored query 添加来源谓词。** 完整性 v2 要求看到完整 visible running inventory，现有 anchor 也绑定原 predicate。过滤发生在根清单之后、子数据之前；普通列表的展示筛选不改变控制面可见清单。若确需改根查询范围，必须新 query fingerprint 与受控初始化，不能复用原 anchor。

### 4.3 状态、删除与旧数据

- 最小分类记录包含对象身份、策略版本、descriptor digest、结果及原因；受保护的 source evidence 用于排除证明，不进入普通日志。
- 分类只对相同 descriptor 与策略版本复用；descriptor 改变需重判，不能永久信任旧缓存。
- 已有 pending bundle 按其原身份处理，不因 UI 隐藏而删除文件或改写为成功。新旧分类不一致时保留任务并进入可诊断状态。
- 不清理或自动删除服务器上的既有真实记录。历史域冲突单列为待处理问题，不通过改 count 或隐藏数据库事实让 manifest 通过。
- 未上传的 excluded 记录不制造普通上传错误和无穷自动重试；确有已提交事实需要删除时，仍处理显式 HealthKit 删除。
- Apple Watch 来源不保证心率、GPS 或功率完整。室内跑、缺传感器数据仍允许保存合法 Workout，缺失仅影响相应分析与覆盖状态。

## 5. 一次采集结果复用与增量基线

### 5.1 统一捕获结果

在同一 Outbox 下引入版本化的内部 `PreparedWorkoutCapture`（内部名称，不新增 wire 字段），包含 session scope、query fingerprint、Workout 身份、捕获 generation、事实引用、series/route 引用、原来源、coverage、显式删除及候选 cursor。

发现器、组包器和恢复器消费同一份捕获结果，不再各自读取和归档同一批对象。引用必须绑定范围和完整捕获状态；新 generation 到来不能修改已准备上传的原包。

### 5.2 首次基线与已有基线分开处理

| 本地情况 | 实施行为 |
| --- | --- |
| 新 Workout、无历史 anchor/事实基线 | 分页耗尽现有 scoped anchored query，直接归档和规范化读到的对象，建立完整基线；组包不再发第二次等价的全量 sample query。 |
| 有完整、可验证的同范围基线 | 用已提交 discovery anchor 读取 additions/deletions，应用到本地基线索引，复用未变内容。 |
| 有 anchor，但旧归档不完整或无法证明基线完整 | 保留原 anchor，先做一次有界显式全量补读建立基线；该补读不重设 anchored cursor。完成前不把 delta 当完整快照。 |
| 有未 ACK 的不可变 bundle | 优先恢复原文件/原 hash，不重新读 HealthKit 或替换待上传内容。后续变化排为下一 generation。 |

每个基线必须记录已耗尽的类型、捕获格式版本、范围指纹与删除覆盖。不能因为“有一些归档”就认为已有全量基线；现有每轮最多若干页的扫描结果也不等于耗尽。

### 5.3 series 与路线

- 普通上传中，每个需要读取的 quantity type 每个捕获 generation 最多一次 batched series 查询；失败重读、范围变化和显式强制刷新单独统计。
- 不能从 scalar `count == 1` 或空样本列表伪造一次 series 查询已完成。若未来省掉该查询，必须先证明既有 coverage 语义支持；本轮默认保留一次必要查询。
- 完整性 v2 仍要求自己的 fresh series 证据；只有其查询时点、cutoff、样本父集合及原合同完全满足时才复用捕获，不缓存旧 digest 冒充本轮证明。
- 根路线对象先枚举，再按实际 route UUID 读取点；有变化时读取所需路线，保持原点序、时间精度和所有支持字段。
- 首次采集失败或没有完成信号时，保留 partial/failed；没有路线与路线查询失败分别表达。

### 5.4 采集期间变化

捕获 generation 固定后到来的变化排入下一 generation。当前包不被重写，ACK 只确认本包覆盖的工作；下一轮处理迟到样本与显式删除。并发旧快照缺席不删除新事实，旧 upsert 不穿透 tombstone。所有重试和补读次数进入性能证据，不能从总时间中删除。

## 6. 批量持久化与崩溃恢复

### 6.1 替代逐样本文件和逐点刷盘

- 新捕获按 segment 保存，初始批量阈值为 **512 条记录、256 KiB 或 250ms** 中先到者；单条记录仍受现有字段/载荷上限约束。
- segment 包含完整边界、数量、digest 与对象/父关系索引。封存后不可变；事实成员可用 segment + offset 引用，不为每个 sample 生成一个原子小文件。
- series/route 在内存中积累有界批次，封存时同步；取消、正常结束及达到阈值时刷出完整批次。禁止每个点都 `synchronize()`。
- 小型引用/检查点持久发布后再推进发现 cursor；不为每个点重写整个 `state.json`。引用与捕获索引属于同一 Outbox，不引入第二条上传队列。
- 批量大小是首轮实现参数，P0/P2 实测后可调整；调整须同时报告耗时、内存与可恢复未提交尾部大小。

### 6.2 持久化顺序

```text
写临时 segment → 完整写入并同步 → 校验边界/数量/digest → 发布 sealed 文件
    → 持久保存 journal/索引引用
    → 允许 discovery cursor 前进
    → 组包、保存原 JSON/multipart、发布 pending bundle
    → 请求服务器；只有有效 ACK 后确认事实交付与释放文件
```

原子重命名、文件同步和引用发布各自失败都需注入测试；`.atomic` 不能直接当作物理掉电证明。平台目录同步能力和无法证明的设备掉电行为在验收报告说明。

### 6.3 恢复规则

| 中断位置 | 必须恢复的行为 |
| --- | --- |
| 批次未封存 | 不推进该批 cursor；丢弃不可引用的临时尾部并从旧 cursor 重读，已有封存数据保留。 |
| segment 已封存、引用未发布 | 不把孤儿当已提交事实；重读可去重，GC 仅清理证实未引用且无在途写者的文件。 |
| journal 已保存、cursor 未推进 | 重放 journal 幂等晋升，不重复建立语义事件。 |
| cursor 已推进、尚未组包 | 从持久事实索引继续，不依赖重新获取已不可见的旧对象。 |
| 原包已准备、未发或 ACK 丢失 | 使用原包、原 hash 恢复；5xx/网络异常按现有 GET 回执与退避逻辑处理。 |
| ACK 已收到、本地状态保存失败 | 原包不回收；恢复原回执后再次原子确认。 |

流式 series/route 重启后的重读必须校验已封存前缀。前缀变化不能混合新旧点：保留旧捕获，创建可诊断的新 generation 或冲突状态，不能静默覆盖。

## 7. 编码与校验精简

### 7.1 校验职责收口

| 项目 | 处理方法 |
| --- | --- |
| 会话、认证、dataset/collector fencing | 保留；批量任务可复用同一 session 的能力上下文，session 改变立即失效，每次服务器写仍 fencing。 |
| multipart/gzip/大小、字段形状、必填/重复/未知/null | HTTP 边界保留；优化实现，不减少支持字段或放开无限输入。 |
| 语义校验 | 统一到 Store 导入入口执行一次完整 `ValidateWorkoutBundle`；HTTP 不再重复执行同一语义遍历。直接 Store 调用也不能绕过验证。 |
| UUID/父关联/内容冲突/删除屏障 | 事务内保留，改为批量集合式判断。 |
| 来源分类 | 按唯一 descriptor 在本次包内去重计算；服务器独立计算，不能信任客户端声明。 |
| 已有不可变本地文件 hash | 准备/恢复/实际读取必要边界校验；可复用受保护且不可变的引用，损坏或替换不能绕过。 |
| 生理合理性与缺失指标 | 归分析质量层；不新增“HR 必须在某区间”“户外必须有 GPS”等整包拒绝规则。现有正式载荷约束不在本轮偷偷放宽。 |
| 全量 inventory、page-chain、series 证明 | 从单条用户等待路径中独立调度，继续既有合同；不删除证明逻辑后仍返回完整。 |

减少的是重复执行和用户路径中的额外工作，不是把所有校验集中到每个阶段再跑一遍。不得照抄参考文档建议而额外增加每包 HMAC/nonce、同步前后 `check_user`、固定轮询或全库查重。

### 7.2 JSON 编码

优化 `Wire.swift` 递归 Data/String 转换和重复创建 encoder 的成本，优先采用可复用、有界缓冲的字节写入。必须与现有规范字节逐字节一致，覆盖键排序、字符串转义、UTF-8、整数、浮点、负零及省略规则。

同一输入仍产生同一原 JSON hash；已存在的 pending 包完全不重新编码。不得为了快改用不同的浮点格式、舍入时间、降采样、丢 metadata 或把 hash 改成压缩字节。解压后 JSON 精确字节 hash 与 gzip 派生身份继续分开。

严格 JSON 解码第一轮先缓存字段类型/required 信息，减少逐字段反射查询。仅当 P0 证明确有主要收益，才考虑合并词法扫描和 typed decode；合并后全部拒绝语义必须通过同一矩阵，不能直接换成会静默丢未知字段的宽松 decoder。

## 8. PostgreSQL 导入优化

### 8.1 集合式检查与写入

保持 dataset 行锁 → fencing → 删除优先 → 身份/关联检查 → 写事实 → 日志/job/回执 → COMMIT 的事务顺序。

1. 一次计算各对象 canonical/hash 与数量，复用结果，不在多个循环中重新编码同一事实。
2. 对输入 sample keys 做有界批次的集合查询，统一读取 tombstone、已存核心/内容身份、已有父关联；替代每个 sample 组织独立检查语句。`pgx.Batch` 不等于一条集合 SQL。
3. 新 sample 与新 association 使用多行参数或事务内 staging + `COPY`/`INSERT SELECT`；首选不新增永久业务表的方案。批次初始上限 1,000 行，并有总参数/字节预算。
4. 严格区分同内容重试、允许的 append-only observation enrichment、真实内容冲突；不能用 `ON CONFLICT DO NOTHING` 隐藏冲突。
5. series/route 身份及屏障检查按 key 批量读取；路线原点序、chunk hash、projection 和 seal 保持原语义。
6. 新事实仍只产生应有的 revision、change event、dirty scope、job 与不可变回执，所有批次处于同一事务。

### 8.2 精确重放与删除竞争

命中 `workout_imports` 不允许立即返回旧 ACK。先按同一锁顺序、同一事务检查父与相关子事实的删除屏障，再返回原回执。可批量检查，不能省略。GET 回执恢复继续受现有删除语义约束。

两种数据库实现须以相同合成输入比较：结果、冲突码、raw payload、revision/job、屏障及原回执一致；故障时均无半包。对失败注入检查最终数据库状态，不能只断言 HTTP 出错。

### 8.3 数据库资源

不关闭 `fsync`、`synchronous_commit`、`full_page_writes` 或 checksums；不扩大连接池/上传并发、不移除限流来换速度。SQL 变化提供 `EXPLAIN (ANALYZE, BUFFERS, WAL)`、锁等待、WAL 与参数内存证据；需要索引时按当时最高迁移号新增迁移，不修改冻结迁移和 checksum。

## 9. 调度、网络和用户体验

- 用户选中的 eligible Workout 优先于全局 discovery、Daily/VO₂ 补齐及完整性证明；同一 Outbox 写入仍串行协调，避免两个任务争改状态。
- 原始包 durable ACK 后立即更新“已同步”，分析状态独立；下一条上传不等待前一条分析结果。状态查询合并为按需刷新，禁止每条固定轮询到分析成功。
- 完整性证明低优先级、可暂停、可恢复，暂停不丢候选 cursor、原 manifest 或原 bundle。未完成时模型原有 `load_sync_incomplete`/unknown 语义保留。
- 新 quantity/route/series 到达仍通过 observer wake、anchored 查询与有界轮转扫描发现；不能因 root 未变或已有 ACK 永久跳过子数据。
- 每轮自动任务有记录数/页数预算；排除项与来源待识别项不耗尽重试队列，单条失败不饿死其他条目。
- 单设备上传槽继续 1、进程槽继续 2；会话级握手复用只在同 scope 内有效，401/协议不兼容/epoch 改变按原规则失效和恢复。
- 429 尊重 `Retry-After`，失败重试有持久退避；重试使用原文件，GET 404 不代表可丢文件或删除已成功。
- 默认上传设备预算 **30/min、burst=2**，dataset **60/min、burst=4**。100 次新上传仅设备预算的理论等待下界约 196s，500 次约 996s，实际还要计入采集和提交。不得宣传默认配置下密集 500 条 21s。
- 过滤说明用来源类别与数量，不展示 bundle 标识、哈希、技术堆栈或内部错误名。单条问题显示可理解的原因与重试入口。

## 10. 协议、持久状态兼容与回退

| 层级 | 本轮策略 |
| --- | --- |
| Workout wire | 保持 Protocol 3、当前 endpoint、字段、精确 JSON hash 与 ACK。 |
| 来源/完整性 | 保持来源策略 v1、完整性 v2；不把其 `protocolVersion=2` 与 Workout Protocol 3 混淆。 |
| 本地 capture | 加法式版本化，读取旧 archive 和新 segment；旧 pending 文件及身份保留。 |
| query/anchor | 未变 predicate 继续原 anchor；范围/合同变化必须新 fingerprint 和显式受控流程。 |
| 数据库 | 首选无 schema 变化；索引或 staging 设计确需变更时用加法迁移，保留事实和历史回执。 |

客户端按 P1 来源过滤、P2 批量捕获、P3 采集复用分别设置内部可回退开关。开关不是用户界面中的技术选项，也不能关闭必要校验。服务器新旧集合导入可在同版本内选择，两者语义一致。

回退指**新版本内的兼容路径回退**，不承诺任意旧二进制能读新捕获引用。回退后已准备原包继续交付，新 segment 由兼容读取器读取；只有新任务改用旧采集策略。若要求旧二进制回退，必须先验证双格式兼容或等待在途新格式任务安全结束，不能删状态解决。

不清空 Outbox、不重新绑定原 session、不搬迁云端验收数据、不重置全部 anchor、不覆盖旧回执。新本机真机验收使用独立 namespace 和本机数据库。

## 11. 分阶段实施与退出条件

| 阶段 | 实施内容与主要文件 | 阶段退出条件 |
| --- | --- | --- |
| P0 基线与计时 | `RAIApp.swift`、`SyncCoordinator.swift`、`HealthKitClient.swift`、`SyncMetrics.swift`；服务端 phase timing；合成性能工具 | 完整手动路径、首次/增量/重试分开计时；记录查询/归档/刷盘次数及默认预算，所有新指标仅技术数据。 |
| P1 来源提前过滤 | `HealthKitClient.swift`、`SyncCoordinator.swift`、`HealthDiscovery.swift`、`WorkoutSourcePolicy.swift`、列表 UI | F01–F08 通过；排除项详细查询/上传/job 均为零，根清单与删除不丢。 |
| P2 批量捕获 | `HealthIntegrity.swift`、`HealthDiscovery.swift`、`OutboxStore.swift`，必要时新增内部 capture writer | F09–F15 通过；旧状态可读，所有崩溃点恢复，刷盘次数按批而非按点增长。 |
| P3 采集复用与编码 | `SyncCoordinator.swift`、`HealthKitClient.swift`、`WorkoutBundle.swift`、`Wire.swift` | F16–F20 通过；完整基线与 delta 分清，普通路径无等价重复查询，canonical 字节完全一致。 |
| P4 后端校验/SQL | `httpapi/server.go`、`syncing/bundle_decode.go`、`storage/postgres/workout_bundles.go`、相关 sqlc 查询/加法迁移（如需） | F21–F27 通过；真实隔离 PG 事务/并发/回滚与执行计划证据齐备。 |
| P5 调度与完整性 | `RAIApp.swift`、`SyncCoordinator.swift`、`ForegroundSyncScheduler.swift`、`WorkoutCompleteness.swift` | F28–F32 通过；单条不等待证明/分析，v2 证明语义与模型 unknown 不变。 |
| P6 综合验收 | Go/Swift/App 测试、合成 HTTP→PG、Release 性能、授权后的真机 | 功能硬门禁、性能门槛和证据要求全部按第 12–16 节签核。 |

P0 先完成；P1/P2 后再进行 P3，避免先复用不可靠基线。P4 依据 P0 瓶颈实施，P5 复用前述稳定状态。每阶段先定向测试、后完成门禁，不必每次迭代重跑完整真机历史。

所有文件为实施定位，不表示必须修改全部文件；发现现有实现已满足时保留并给出证据。新增逻辑不能被 mock 测试或文件存在替代验收。

## 12. 功能、完整性与故障验收矩阵

以下 F 项为硬门禁；不适用项须说明前提，不能把未执行写成通过。开发和故障注入仅使用合成数据。

| ID | 场景 | 必须满足的结果 | 最低证据层 |
| --- | --- | --- | --- |
| F01 | Apple Watch 原生跑步、scoped bundle | 与现有 Swift/Go vectors 一致，可同步。 | 双端单元 + App 合成 |
| F02 | 第三方 iPhone writer、第三方手表导入、手工录入 | 默认列表不让用户选作合格记录；详细 quantity/series/route 查询均 0，import/事实/job 增量均 0。 | 查询计数 + HTTP/PG |
| F03 | 来源缺字段、矛盾设备描述 | 待识别，不自证 eligible/excluded；同批其他合格记录成功。 | App + PG |
| F04 | 用户修改设备昵称、名字含 Apple Watch | 不能改变来源资格；按 descriptor 判断。 | 双端单元 |
| F05 | 前 100 条均第三方、后面有合格项 | 分页能继续发现合格项；UI 不卡住；有明确加载边界。 | App 合成 |
| F06 | 全第三方/空清单 | 普通同步无无穷重试；v2 用正确 exclusions/not-applicable 语义，不伪造 receipt。 | Swift + PG |
| F07 | 排除项/未知项与父子删除混合 | 明确删除按既有范围持久处理；无删除丢失、无旧 UUID 复活。 | Swift + PG 并发 |
| F08 | 篡改 exclusion/classification/digest | 服务器重分类拒绝；manifest 与最终回执不落库。 | 真实 PG |
| F09 | 批次写一半、尾部截断 | 旧 cursor 保留，封存前缀可恢复，不丢事实。 | 文件故障注入 |
| F10 | 文件已封存、引用写失败 | 无悬空已提交引用；孤儿清理不删在途或仍引用文件。 | 文件故障注入 |
| F11 | journal 成功、cursor 发布失败 | 重启幂等晋升，不重复语义事件/父关系/generation。 | 重启测试 |
| F12 | 同步失败/磁盘满/保护文件不可读 | 保留 pending 和旧权威状态，不能成功 ACK 或提前 GC。 | 文件故障注入 |
| F13 | 旧 archive 状态、新 segment 状态及混合状态 | 均能恢复；旧 pending 原字节/身份/anchor 不改。 | Swift 重启 |
| F14 | route/series 重读前缀不同 | 不拼接不同版本；保留旧捕获并明确冲突。 | Swift/App 合成 |
| F15 | 10 倍点密度和高频取消 | 分批写入，有界内存；刷盘次数与批数同阶，无逐点刷盘。 | 计数 + 峰值采样 |
| F16 | 首次基线建立 | 每个类型耗尽所有分页；组包不再做等价全量读取。 | App 查询计数 |
| F17 | 有 anchor 但旧基线不完整 | 不把 delta 当全量；受控补读不重设原 anchor。 | Swift/App 合成 |
| F18 | 无变化、迟到样本、路线迟到 | 无变化无新 import/job；迟到数据在后续 generation 提交，不因已有 ACK 永久跳过。 | App + PG |
| F19 | 包准备后出现新变化/删除 | 旧包不变，后续 generation 不被旧 ACK 消掉，删除优先。 | Swift + PG 并发 |
| F20 | 编码器替换 | 所有 golden/数值/转义/metadata 变体字节完全一致，旧原包 hash 不变。 | Swift/Go 共享 fixtures |
| F21 | 缺失/null/重复/未知字段、非法单位/范围、超限 | 错误语义与旧合同一致；无事实、回执、日志或 job 残留。 | HTTP + PG |
| F22 | 同字节重放、plain/gzip 与不同 gzip header | 返回首次原回执，revision/job/import 不增加。 | 真实 HTTP/PG |
| F23 | 同 UUID 内容冲突或跨父关联 | 保持既有冲突码，整包回滚，不被 DO NOTHING 吞掉。 | 真实 PG |
| F24 | 先删除后上传、先上传后删除、子删除与旧重试 | 两种锁顺序均正确；不返回误导性的旧成功回执，不复活事实。 | 真实 PG 并发 |
| F25 | 每个写入阶段故障、COMMIT 失败、连接中断 | 原始事实/屏障/日志/dirty/job/回执全部同成同败，失败没有成功 ACK。 | 隔离 PG 故障注入 |
| F26 | ACK 丢失、GET 404、重复 POST、本地 ACK 保存失败 | 保留原文件/身份，恢复原回执，最终恰好一次语义提交。 | 真实 Swift→HTTP→PG |
| F27 | 429/5xx/断网/超时/401/session 变化 | 预算和退避生效；不交叉写 session，不热循环，不重新生成 pending 包。 | Socket + Swift/PG |
| F28 | Worker 暂停、分析慢、完整性暂停 | 单条 durable ACK 后及时显示已同步；分析仍 pending/partial，下一条可继续。 | App→HTTP→PG |
| F29 | v2 manifest 丢 ACK 与第二代增量 | 原 manifest 身份恢复；第二代用 committed anchors，bootstrap=0；两代不可变记录均保留。 | Simulator→HTTP→PG |
| F30 | 来源不明/required stream 失败/证明过期 | 完整性不通过；模型保持 incomplete/unknown，不填健康参数或零负荷替代。 | PG + 模型消费者 |
| F31 | 缺 HR、无功率、室内无路线、查询失败 | 合法事实可保存；真实缺失与失败分开，指标可用性不伪造，未知字段仍阻塞。 | App + PG/Worker |
| F32 | 手动任务抢占、自动补齐、取消/恢复及列表滚动 | 操作响应及时；原状态保留；无重复上传、无任务饿死；技术信息不进入普通 UI。 | App XCTest + 性能采样 |

## 13. 性能验收口径与目标

### 13.1 计时边界

- `T_user_ack`：用户点击已选记录的同步，到客户端已持久保存该包有效 ACK 且 UI 更新。包括调度、必要握手、实际采集、编码、落盘、限流等待、网络和 COMMIT；不含授权弹窗等待、初次浏览列表与后续分析。
- `T_capture`：任务开始详细采集到完整 capture 可恢复；拆开 HK query、archive/normalize、segment write/sync、index/state commit。
- `T_prepare`：capture 可用到不可变上传文件可用；拆开 JSON、hash、gzip、落盘。
- `T_server_ack`：服务端认证请求到 ACK 写出；另报 body、decode、语义校验、锁等待、事实读写、COMMIT 和 ACK write。
- `T_analysis`：ACK 对应提交到实际分析发布；独立计时，不能从 `T_user_ack` 推导。
- `T_batch`：点击批量同步到最后一条有效 ACK；包含所有重试和限流，已排除数、失败数与成功数同时报告。

同设备单调时钟用于客户端耗时，同进程时钟用于服务端分段；不把跨设备 wall clock 差当网络时延。父阶段包含子阶段，报告注明包含关系。

### 13.2 标准合成负载

| Profile | 运动时长 | scalar 数量级 | GPS 点数量级 | 备注 |
| --- | --- | --- | --- | --- |
| S20 | 20min | 2,200 | 1,200 | 原生来源、正常有界 metadata。 |
| S60 | 60min | 6,600 | 3,600 | 普通跑步主验收档，保留完整支持字段。 |
| S120 | 120min | 13,200 | 7,200 | 长跑档。 |
| S240 | 240min | 26,400 | 14,400 | 超长档；仍在现有上限内。 |

数量级用于说明负载；P0 固定生成器版本、seed、精确数量与内容 digest，前后使用相同结构和字段。另测多值 series（2,200 父/4,400 点）、多路线、10 倍路线密度、50,000 samples 以及合同上限附近的变体；它们不能用轻量单条替代。超限应明确拒绝并保留原状态，不截断成通过。

### 13.3 首轮硬性性能目标

以下是**待实现、待实测的验收门槛**，不是当前成绩。先在 P0 锁定完整基线；任何调整必须说明原因、保留旧目标与新目标，不能测完再默默降门槛。

| ID | 场景与配置 | 通过条件 |
| --- | --- | --- |
| P01 | 单条 S20–S60，Release，iPhone 12 Pro Max 同档或指定基准机，稳定局域网，本机 Mac API+Worker+PG，默认预算，无历史 pending | `T_user_ack` P50 ≤ **5s**、P95 ≤ **8s**；并且同口径 P50 较 P0 下降至少 **50%**（若 P0 已≤5s则不要求此相对降幅）。 |
| P02 | 单条 S120，同上 | P50 ≤ **8s**、P95 ≤ **12s**。 |
| P03 | 单条 S240，同上 | P50 ≤ **15s**、P95 ≤ **22s**；所有支持点与字段无损。 |
| P04 | 已建立完整基线、无新事实的重复同步，单条 S60 | P50 ≤ **2s**、P95 ≤ **4s**；0 新 bundle POST/import/job，必要 anchored 查询可存在，0 等价全量重读。 |
| P05 | 已持久准备原包的网络恢复/重试 | HealthKit 查询次数 **0**；读取/校验/上传原包；恢复耗时与正常传输分别报告，不混作首次导入速度。 |
| P06 | 本机 loopback 新导入，真实 handler→PG，S60 | `T_server_ack` P95 ≤ **1s**；不是重复 POST 命中回执。 |
| P07 | 同上 S240 | `T_server_ack` P95 ≤ **4s**，不扩大 pool/并发、不关闭 durability。 |
| P08 | 默认预算，连续 10 条 S60，无在途旧包 | `T_batch` ≤ **60s**；每条独立失败隔离，成功数与事实完全匹配。 |
| P09 | 默认预算，连续 100 条 S60 的合成 App/端到端任务 | `T_batch` ≤ **360s**；包括 429 等待，无静默丢记录；500 条独立测扩展性与预算等待，不设违背预算的 21s 目标。 |
| P10 | P01–P03 期间滚动、暂停、返回页面 | UI 操作响应 P95 ≤ **100ms**，无持续超过 **250ms** 的主线程同步阻塞。 |
| P11 | 本机合成 Swift 编码及 capture 分段 | S60 编码 P50 较 P0 至少下降 **50%**；归档文件/同步调用数随 segment 数增长，不随每点增长。若该阶段已<100ms，保留数据且只要求不回退。 |
| P12 | 相同 profile、相同环境的内存与稳定性 | iPhone 进程峰值 RSS：S60 ≤ **256MiB**、S240 ≤ **450MiB**；API 进程两上传槽压力峰值 RSS ≤ **512MiB**。与基线差异同时报，内存失败不能用放开上限解决；长循环无持续未回收增长。 |

P09 使用合成 HealthKit reader/模拟器，不用合成数据写入真实用户 HealthKit。P01–P04 的设备性能先用合成 reader 在设备上测 CPU/存储/网络路径，再对另行授权的真实只读记录复核 HealthKit 耗时；两种结果单列。真实复核没有执行时不能签“真机端到端达标”。

`T_analysis` 不设所有算法统一秒数：必须执行实际 handler 并报告各档耗时、结果状态与失败原因。Worker 停止/未实现/缺数据均不得当作成功发布。超过当前分析输入上限的合法原始包可持久保存，但须明确分析不可用。

### 13.4 统计方法与 RQRun 对标

1. 每个合成计时档先 3 次预热，至少 30 次有效新导入，固定种子结构、每次新 dataset/对象身份；重复上传不是新导入。冷启动另取至少 5 次单列。
2. P95 使用排序后的 nearest-rank `ceil(0.95*n)`；记录 n、P50/P95/min/max、失败/取消/429 数量，不剔除正常默认预算等待。故障注入数据单列。
3. Debug 只用于诊断，不能代替 Release 达标。记录手机型号/系统/构建、Mac/PG/Go/Swift 版本、电量与热状态、局域网、上传字节、pool/slot/rate 配置及测量工具。
4. 真机真实记录复核至少覆盖 3 条普通跑步、1 条长跑（可用时）、1 条室内/无路线（可用时）、无变化重复和重启恢复；至少 10 次初次捕获样本用于报告中位数，小样本不签 P95。缺少的类型明确 NOT RUN。
5. 不为凑初次导入反复清手机旧状态。可在独立验收 session/namespace 下重新捕获，服务端隔离空间；原云端、原 session 和真实 HealthKit 不改。
6. 只有另行授权且能测量相同记录、相同首次/缓存状态、完整起止时点和相近网络时，才能签署“达到 RQRun 同等速度”：匹配组中位数不超过 RQRun 的 **1.2 倍**；样本少于 20 对时不宣称 P95 等同。
7. 无法建立公平对照时，以 P01–P12 验收，结论写“达到本方案速度目标；与 RQRun 直接对标未验证”，不能用现有抓包时间线补齐证据。

## 14. 测试执行分层与命令

所有命令在 `/Users/ta/RAI` 本机执行。使用项目已有本机工具链和一次性隔离 PG18；不得调用 SSH、N100/fnOS 测试入口或远端数据库。

### 14.1 日常与定向验证

```bash
# 日常快速反馈；不能替代完成门禁
scripts/local/test.sh quick

# 来源策略、bundle 与严格解码等受影响 Go 单元测试
scripts/local/test.sh unit -count=1 -run 'TestWorkoutSourcePolicy|TestWorkoutBundle' ./internal/syncing

# Swift 来源、捕获、身份、恢复与完整性；新增用例纳入对应 suite
scripts/local/test.sh swift

# 合成 App 回归
scripts/local/test.sh simulator
```

定向名称按实施后实际存在的测试选择；命令输出必须确认测试执行数，零匹配或 opt-in SKIP 不等于通过。

### 14.2 事务与端到端验证

```bash
# SQL/事务修改必跑：真实隔离 PG，脚本已带 race
scripts/local/test.sh integration -run 'TestWorkoutBundle|TestWorkoutDelete|TestPhase3|TestWorkoutCompletenessV2' ./integration

# 实际 Swift Outbox → URLSession → HTTP → 隔离 PG 故障恢复
scripts/local/test.sh integration -tags=integration,swiftclient -run '^TestWorkoutReliabilitySwiftClientPostgres$' ./integration

# Simulator → 本机 HTTP/PG 的 v2 manifest 两代及丢 ACK
scripts/local/test.sh e2e
```

新增集合导入、批量捕获和调度用例须加入定向选择，现有命令不是覆盖所有未来新增场景的保证。结束检查临时数据库、cluster 和测试进程已清理，保留失败证据。

### 14.3 性能与完成门禁

```bash
# 既有合成矩阵，仅作为组成部分；P0 需补新计数/完整用户入口/重复统计
RAI_PHASE4_PERF=after scripts/local/test.sh integration -run '^TestPhase4PerformanceMatrix$|^TestPhase4FreshGzipDurationMatrix$' ./integration
RAI_PHASE4_PERF=after scripts/local/test.sh swift --filter WorkoutPerformanceTests.testDurationMatrix

# 轻量 1/10/100/500 条独立导入；单独报告 benchmark 预算
scripts/local/test.sh perf

# 阶段完成必须运行：go test -race ./...、go vet ./...、依赖边界等
scripts/local/test.sh gate

# 综合签核需要全量集成回归时实际执行；不是每次迭代都运行
scripts/local/test.sh full
```

现有 matrix 部分档位只运行一次，不能凭其输出签署 P50/P95；实施中扩展或新增统计入口，并交付可复现命令。`perf` 高预算成绩与默认预算 P08/P09 分开。修改 API 合同时运行现有 contracts lint；修改 sqlc 查询时生成稳定性检查；修改 iOS App 时做目标最低系统兼容检查与 Release 构建。

### 14.4 真机执行边界

常规开发/回归仍只用合成数据。真实只读 HealthKit 复核是独立验收阶段，需要当次明确的设备、记录范围和操作授权；本文不要求读取生产数据库，不自动启动该阶段。

获授权后沿用本机独立验收控制器和 namespace，手机只读；API/Worker/PG 在 Mac，原云端数据、入口、原手机状态不迁移、不清除。公开证据只留数量、时间、技术字节数、hash、状态和原因，不导出健康值、GPS、时间明细、稳定设备标识、真实 UUID、凭证或原始 metadata。

## 15. 交付物、证据格式与签核

实施必须交付：

1. 分阶段代码及受影响测试，实际修改文件列表、原有 dirty 工作说明、所用开关与回退步骤。
2. `docs/WORKOUT_SYNC_SPEED_RESULT.md`（实施时新建）：结论、实际前后测量、未完成项、失败历史、资源/兼容风险。
3. `docs/evidence/workout-sync-speed/`（实施时新建）：环境与 fixture manifest、默认/benchmark 预算、阶段计时及查询/I/O计数、测试执行/跳过清单、数据库回滚/幂等/删除断言、SQL plans、峰值内存与 UI 响应证据。
4. `docs/IMPLEMENTATION.md` 追加实际结果与 NOT RUN；不能把计划文字改成完成记录。
5. 若协议/索引确有变化，附加法合同/迁移与 OpenAPI/sqlc 检查；未变化明确说明。

每条性能记录最少包含：`runID`（随机技术标识）、构建/环境、fixture digest、首次/重复/恢复模式、记录和点数量、字节数、各阶段耗时、query/segment/sync/state-commit 次数、预算等待、HTTP/回执状态、peak RSS 及测量方法。公开输出不包含健康值或真实对象标识。

最终签核拆开列出：

| 签核项 | PASS 的必要条件 |
| --- | --- |
| SOURCE FILTER | F01–F08，默认列表与采集入口实际过滤，v2 可见清单不丢。 |
| DATA SAFETY | F09–F27，真实隔离 PG 原子性、删除优先、恢复与精确身份通过。 |
| NORMAL SYNC | F28–F32，单条 ACK 与分析/完整性独立，任务不互相饿死。 |
| PERFORMANCE | 相关 P01–P12 全部有同条件、足够样本、默认预算下的测量；失败门槛逐项列出。 |
| REAL DEVICE | 授权范围内真实只读采集、重启、重复、缺失数据实际复核；未执行只能 DEFERRED/NOT VERIFIED。 |
| RQRUN COMPARISON | 第 13.4 节公平配对对照成立；否则 NOT VERIFIED。 |
| DEPLOYMENT | 本方案不执行部署，保持 NOT RUN；本机验收不等于生产验收。 |

任一数据安全项失败，不能以速度达标抵消；某性能档位不达标则保留具体结果继续优化，不把降低采样、缩小窗口、丢 metadata 或扩大预算后的数据改写成原目标通过。真机未执行时可以交付“合成工程通过”，不能交付“真机同步目标达成”。

## 16. 主要风险与处置

| 风险 | 处置与接受条件 |
| --- | --- |
| 主要耗时来自系统 HealthKit，优化 I/O 后仍>目标 | 分开测真实 query 与本地处理；报告瓶颈，先完成可控成本优化，必要时单独评审有限并发；不靠虚假 ACK 或少读指标达标。 |
| 批量刷盘增加未封存尾部 | 旧 cursor 不前进，尾部可重读；未封存数据不作为已捕获事实；故障注入验证。 |
| 历史归档不足以重建全量 | 不滥用已有 anchor，按第 5 节受控补读一次建立可验证基线。 |
| 新 capture 格式影响旧恢复/回退 | 双读兼容，新版本内回退；原 pending 保留；旧二进制回退另行证明。 |
| 未知来源阻塞完整性 | 仅该条待识别，其他同步继续；完整性及模型质量如实保持未验证，不把未知直接排除。 |
| 第三方 watchOS 来源被排除 | 本轮明确为 Apple Watch 原生范围；扩展支持须新策略版本与数据充分性验收。 |
| series fresh 证据无法复用 | 普通同步减少重复，控制面继续必要查询；不弱化 v2 cutoff/证明语义。 |
| 集合 SQL 锁顺序/参数/WAL/内存改变 | 统一 key 顺序与有界批次；真实 PG 并发、故障和计划验证，保留旧语义路径。 |
| 历史真实全量验收尚未完成 | 本方案不继承“真机全量通过”；独立列出真实 inventory、manifest、第二代与模型消费未验证项。 |

## 17. 本文编写时的执行状态

- 文档：本实施方案已编写；实际文档检查与完成门禁结果追加在 `docs/IMPLEMENTATION.md` 对应条目。
- P0–P6 优化实现：**NOT STARTED**。
- 新功能/新 SQL/新 capture 的数据库与 App 验收：**NOT RUN**。
- 本方案性能门槛及 RQRun 公平对标：**NOT VERIFIED**。
- 新真机操作、生产部署、真实数据迁移：**NOT RUN**。

后续实施按各阶段提交实际证据；本文件中的目标、伪流程和验收矩阵不得作为已经实现的证明。
