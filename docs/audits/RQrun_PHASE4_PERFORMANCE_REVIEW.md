# RQrun Phase 4 — Performance Review

日期：2026-11-01。范围：RQ-008 / RQ-014 / RQ-015 / RQ-016。

## 1. Executive Summary

**PASS WITH RISKS**。

- 四项实现及自动化资源边界通过；plain 与 fresh gzip 的20/40/60/120/240分钟均真实完成 Import → Dirty/Job → LoadAnalysis → Analyze → Publish。没有截断、降采样或跳过子事实。
- N100 的240m AnalysisSamplesSQL 从修改前单次3,007.076ms降至本次 144.574ms；一个有执行计划依据的覆盖索引，没有 SQL 语义改写或新增算法。
- 真实生产 `http.Server` 参数的5/15/30秒暂停、179秒合法上传、180秒终止和ACK写出deadline通过。长测试不是仅检查配置或 sleep-only 替身。
- iPhone 12 Pro Max 仅使用本轮再次明确授权的2026-10-26约19:50记录。gzip、Durable ACK、一个import、5,464 Samples（HR349）、source/bundle/chunk/projection各1,768 GPS点，数据库逐点比较通过。
- 峰值内存、物理手机断网/后台强杀、公网/TLS代理和生产部署仍未验证；修改前基线没有原地细分 Import 内部阶段。不能把这些项目当作PASS。

机器证据：[总证据](../evidence/rqrun-phase4-2026-11-01.json)、[真机](../evidence/rqrun-phase4-device-2026-11-01.json)、[执行前保护](../evidence/rqrun-phase4-workspace-baseline.json)。所有持久证据只保留合成测量、计数、状态和hash；真机UUID（含coverage字典key）已散列，不保存健康值、坐标或token。

## 2. Baseline 与方法

执行前先保存五个Git命令和683文件SHA-256：HEAD `aeb769c82f6af6c47d7cac82874f48ff37f635fc`、分支`main`、无staged、schema16、protocol3。未reset/clean/checkout/restore/stash/commit/push/deploy，未读取生产数据库。原方案、00001–00016、Phase1–3报告/evidence保持原SHA-256；没有删除基线文件。

生产代码修改前，先运行新建的测量测试。Go：Intel N100、Go1.27.3、GOMAXPROCS2、独立PostgreSQL18.6容器（1GiB、shared_buffers128MiB、fsync/full_page_writes/synchronous_commit/checksums均on）。Swift：macOS x86_64 Debug package。CPU共4核、内存约8.33GB；原生Go runner没有施加容器内存quota，不能宣称限制为3GiB。

生成器沿用40m七类标量456/887/887/884/423/428/432、GPS2283比例，包含Phase3完整来源观察。20→240m样本2,197/4,397/6,594/13,191/26,382、GPS1,141/2,283/3,424/6,849/13,698。StepCount只是原始步数，不冒充原始步频。前后使用同一生成器、固定数值/时间/点序；UUID/epoch各隔离运行新建，非逐字节相同的统计A/B试验。plain/gzip身份测试另外对**同一原JSON字节**执行。

| Workout | JSON B | offline gzip B | strict decode+validation ms | HTTP ACK ms | LoadAnalysis ms | Worker ms |
|---|---|---|---|---|---|---|
| 20m | 1,378,403 | 140,211 | 172.66 | 883.30 | 159.66 | 223.57 |
| 40m | 2,757,290 | 277,506 | 400.95 | 1,557.57 | 146.31 | 247.19 |
| 60m | 4,134,363 | 413,724 | 545.13 | 2,498.72 | 660.59 | 825.56 |
| 120m | 8,269,177 | 827,241 | 1,025.91 | 5,892.68 | 1,959.78 | 2,191.59 |
| 240m | 16,537,010 | 1,651,012 | 2,187.93 | 12,580.92 | 687.06 | 1,645.61 |

基线gzip仅离线对照，当时上传为plain。当前源码含Phase3来源信息，240m约16.54MB；不能直接套用旧Phase0约7.59MB包。基线240m这次Worker实际成功（LoadAnalysis687ms），但原SQL的EXPLAIN已超过默认2s。EXPLAIN后autovacuum统计/缓存可能改变计划；不把旧报告的失败伪装成本轮失败。原始EXPLAIN和实际Worker结果都保留。

原基线另记录Go marshal/gzip/gunzip/Validate、Swift build/encode/gzip及multipart/temp文件大小，见JSON。**原Import的Samples/Route/dirty内部阶段 NOT MEASURED**；后续补充计时与索引对照不冒称原始基线。所有时长为单次wall-clock，未声明p50/p95或真实家庭网络吞吐。

## 3. RQ-008 — Timeout Budget

| 层 | Before | After |
|---|---:|---:|
| Server header / TLS初始握手 | 5s | 5s |
| Server ReadTimeout | 15s | 185s fallback；Upload body绝对deadline180s |
| Server WriteTimeout | 25s | 255s fallback；body后processing65s含ACK，COMMIT后ACK另5s |
| UploadLane context | 120s | 245s；body后processing60s |
| JSON / validation | 无分段检测 | 15s / 5s，结束检测；超时不进入Store |
| gzip计算段 | 不支持 | 5s；扣除压缩源读取等待，避免把弱网暂停当解压CPU超时 |
| Import | context120s / statement5s | context及PG transaction45s / statement5s / lock500ms |
| API fast query | 2s statement | 2s statement |
| Snapshot | 2s statement | statement5s / transaction及context20s |
| Publication | 5s statement | statement5s / transaction及context10s |
| iOS POST request / foreground resource | request60s / shared session默认 | 270s / 显式300s |
| Background URLSession | resource3600s | request270s / resource3600s（含系统排程） |

`NewHTTPServer`被main和真实TCP测试共用；认证路由用ResponseController分阶段设置有限read/write deadline。CPU同步JSON/validation预算是结束检测，不宣称可抢占任意指令。解压逐块检查，网络读有独立180s界限。

`upload_read_timeout`408、context/decode/validation/decompression超时503；数据库57014和25P04分别分类为可重试503。408/429/5xx/URLError继续既有Outbox退避，不落入永久payload_invalid400。ACK write失败有单独日志，COMMIT事实/回执保留，GET或重POST仍可恢复。

| 实际网络/故障测试 | 结果 |
|---|---|
| gzip body暂停5/15/30s（生产server设置） | 全部200；15/30s超过旧socket限额仍成功 |
| moderate约160KiB/s / slow约16KiB/s，真实TCP+chunked | PASS，约1.65/16.27s |
| 179s后完成合法body | 200，约179.002s |
| body一直不完成 | 408 upload_read_timeout，约180.001s；无Import |
| 缩短phase deadline的read-timeout与client cancel | PASS，超时正确分类，未Import |
| ACK阶段延迟6s | 写出失败被记录，已返回Store的提交结果不被撤销 |
| PG COMMIT后断开 / ACK lost / GET404 / duplicate POST | Phase2真实Swift→Go→PG永久场景PASS |

TCP单元测试使用计数Store来隔离socket行为；真实事务/回执/重试保证来自隔离PG集成，二者不互相替代。TLS、fnOS反向代理和家庭公网**NOT RUN**。

## 4. RQ-015 — Body 与解压资源边界

| 独立边界 | 值 |
|---|---:|
| 整个HTTP wire（含multipart所有内容） | 32MiB + 64KiB |
| framing/preamble/epilogue合计 | 64KiB |
| gzip file | 8MiB |
| decoded JSON / plain file | 32MiB |
| expansion ratio | 100:1 + 64KiB启动余量 |
| samples / series entries | 各50,000 |
| routes / route points / series points | 16 / 1,000,000 / 1,000,000 |
| JSON深度 / 每个object字段 | 64 / 256 |

`http.MaxBytesReader`先包住整个body，Content-Length超限不读body；无长度/chunked仍受实际读取上限保护。至多一个额外探测字节用于检测超限。拒绝时不Close/drain任意part，并关闭连接/收紧读deadline；未知/重复/空file、非法disposition/header和malformed multipart拒绝。NextRawPart不做隐式Content-Transfer-Encoding转换；额外part只读必要头部，不读取其大内容。epilogue也读入有限预算，framing最终核验。

严格schema改为流式词法/反射presence与类型检查，再一次typed decode。未知字段（含大小写）、重复字段、必填缺失、null、非法类型仍拒绝；样本/系列/点数量尽量在typed数组分配前检查。不再为整个包递归构建generic JSON树/RawMessage副本，然后再次执行同样树检查。metadata后续既有allowlist/领域验证仍执行。

永久测试：Content-Length快速拒绝、无长度和chunked大尾部、巨大extra part不drain、重复/空/未知part、malformed multipart；gzip截断/CRC/串联成员、100MiB bomb、压缩内但decoded超限、独立9MiB随机压缩文件超8MiB。全部PASS。HTTP整个body、file、decoded、domain限额没有相互替代。

## 5. RQ-014 — Gzip、Hash 与 Outbox

压缩层选择单个multipart `file` part的`application/gzip`（推荐workout.json.gz），顶层Content-Encoding仍拒绝；plain application/json及旧octet-stream兼容。没有新增API、第二个Outbox或网络chunk。

`bundleSHA256`冻结为**原解压后JSON精确字节hash**，继续现有payload identity；不改成gzip/multipart hash，也不偷偷重新canonicalize。plain/gzip、不同mtime/name的gzip均返回同一原回执；CRC/ISIZE必须读到EOF验证，拒绝串联成员/尾随垃圾。损坏gzip不进入Import。

iOS使用level6、mtime0、OS255、无随机header。原plain multipart是权威文件，新增同一task下的可恢复gzip派生文件与独立wire SHA256。原包先持久化；重启校验原JSON hash，再校验/复用派生文件；损坏时从原包重建，不读HealthKit。gzip失败保留原包，超8MiB压缩产物可用有界plain兼容路径。严格ACK/显式DELETE ACK与两种文件GC授权同一次state提交；retainedBundles携带两种引用，后继删除task不继承旧wire引用。

新增永久Swift用例覆盖重启、损坏派生文件重建、原包保留、GC与ACK，固定header/相同字节；原Phase2/3生命周期回归继续运行。普通Swift suite98项/11 suites PASS；另有 opt-in XCTest/性能用例，不能把SKIP冒充执行。

## 6. RQ-016 — SQL Execution Plan 与索引

每档均对实际样本、路线、Workout、series读取运行 `EXPLAIN (ANALYZE, BUFFERS, WAL, VERBOSE, FORMAT JSON)`；完整plan（planning/execution、每个node rows/loops、shared hit/read、temp、sort/memory、index/seq/bitmap）保存在总JSON。导入后buffer通常warm，未声称冷启动磁盘测量。

| Workout | SQL | Before exec ms | After exec ms | rows | root loops | shared hit | read | temp R/W blocks |
|---|---|---|---|---|---|---|---|---|
| 20m | samples | 49.32 | 11.16 | 2197 | 1 | 12957 | 0 | 0/0 |
| 20m | route | 1.13 | 1.20 | 1141 | 1 | 29 | 0 | 0/0 |
| 20m | workout | 0.03 | 0.02 | 1 | 1 | 2 | 0 | 0/0 |
| 20m | series | 2.22 | 2.22 | 0 | 1 | 6372 | 0 | 0/0 |
| 40m | samples | 30.09 | 22.34 | 4397 | 1 | 26179 | 0 | 0/0 |
| 40m | route | 2.38 | 2.46 | 2283 | 1 | 53 | 0 | 0/0 |
| 40m | workout | 0.04 | 0.03 | 1 | 1 | 2 | 0 | 0/0 |
| 40m | series | 4.54 | 4.78 | 0 | 1 | 12982 | 0 | 0/0 |
| 60m | samples | 259.99 | 32.40 | 6594 | 1 | 39399 | 0 | 0/0 |
| 60m | route | 3.42 | 3.26 | 3424 | 1 | 78 | 0 | 0/0 |
| 60m | workout | 0.04 | 0.03 | 1 | 1 | 2 | 0 | 0/0 |
| 60m | series | 6.86 | 6.62 | 0 | 1 | 19611 | 0 | 0/0 |
| 120m | samples | 836.60 | 70.41 | 13191 | 1 | 92283 | 0 | 0/0 |
| 120m | route | 6.84 | 6.79 | 6849 | 1 | 152 | 0 | 0/0 |
| 120m | workout | 0.03 | 0.03 | 1 | 1 | 2 | 0 | 0/0 |
| 120m | series | 13.65 | 13.52 | 0 | 1 | 39525 | 0 | 0/0 |
| 240m | samples | 3,007.08 | 144.57 | 26382 | 1 | 184892 | 0 | 0/0 |
| 240m | route | 13.99 | 12.25 | 13698 | 1 | 300 | 0 | 0/0 |
| 240m | workout | 0.03 | 0.03 | 1 | 1 | 2 | 0 | 0/0 |
| 240m | series | 27.75 | 27.41 | 0 | 1 | 79358 | 0 | 0/0 |

样本原计划在新导入统计不足时误选 `health_samples_type_time`：每个member按dataset/type/uuid查找，但中间event_start未限定，240m重复lookup 26,382次、约1.445M shared hits。新增单个`health_samples_analysis_identity(dataset_id,sample_uuid,sample_type) INCLUDE(event_start,event_end,value_numeric,unit,content_sha256)`；旧PK和时间索引保留。新计划走覆盖身份索引，保留原 `ORDER BY sample_type,event_start,sample_uuid`、完整series和Route sequence。没有降采样、删点或改变lineage。

另一个**本地PG18.6同一已导入数据集**的候选索引实验：原query 2,212.530ms / candidate108.615ms，直接对照约20.4倍。它是Mac本地实验，不能伪装N100结果。SQL读取语义及排序保持不变。

N100额外write/storage对照（各1次、不同隔离UUID，wall受统计/调度影响，不宣称写入更快的因果结论）：

| Index | Import ms | WAL delta B | Index B |
|---|---|---|---|
| absent | 9,217.94 | 48,596,600 | 0 |
| present | 7,078.49 | 57,221,576 | 6,012,928 |

增加约6MB索引及额外WAL是明确代价；indexed Import wall仍同时包含身份查询收益。SELECT EXPLAIN各档temp R/W=0、无SELECT WAL；完整sort memory见plan。基础时长矩阵没有多值series，series查询rows=0不能冒充其最坏性能；另有2,197个sealed父/4,394点的many-series真实Worker成功回归。

只有确认索引与计划收益后，才将snapshot statement从2s独立调整为5s，并加20s context/transaction；API fast query维持2s。真实表锁、query cancel、statement timeout、连接丢失、事务回滚与Worker退避/重领/发布通过；不存在无限占连接或以单纯加timeout替代优化。

## 7. Duration Matrix

以下为同一生成器的**fresh plain Import**与实际既有P4算法。`Store.ImportWorkoutBundle`、LoadAnalysis、Algorithm、Publication均单独计时；Total Worker还包含StartRun/input hash等开销。

| Workout | Raw JSON B | Gzip file B | Import ms | LoadAnalysis ms | Analyze ms | Publish ms | Total Worker ms |
|---|---|---|---|---|---|---|---|
| 20m | 1,378,403 | 140,269 | 572.10 | 72.50 | 0.23 | 29.60 | 139.42 |
| 40m | 2,757,290 | 277,527 | 1,156.90 | 125.64 | 0.35 | 41.26 | 226.41 |
| 60m | 4,134,363 | 414,012 | 2,204.04 | 183.65 | 0.47 | 51.31 | 313.47 |
| 120m | 8,269,177 | 827,089 | 4,440.70 | 392.42 | 0.81 | 85.36 | 620.23 |
| 240m | 16,537,010 | 1,650,815 | 7,558.20 | 708.53 | 1.59 | 177.63 | 1,188.53 |

以下另用全新数据库执行**fresh gzip**，不是将duplicate POST的低成本当作新导入性能。全部完成真实Worker publication：

| Workout | Gzip wire B | Decompress ms | JSON decode ms | Validate ms | Fresh Import ms | HTTP ACK ms | Worker |
|---|---|---|---|---|---|---|---|
| 20m | 140,444 | 4.49 | 62.50 | 9.36 | 641.12 | 717.75 | succeeded |
| 40m | 277,658 | 12.30 | 125.26 | 18.00 | 1,218.25 | 1,374.22 | succeeded |
| 60m | 414,258 | 12.85 | 178.44 | 22.61 | 1,781.01 | 1,995.22 | succeeded |
| 120m | 827,296 | 23.77 | 358.31 | 48.28 | 4,278.47 | 4,709.33 | succeeded |
| 240m | 1,651,120 | 46.79 | 779.81 | 118.35 | 7,251.76 | 8,197.51 | succeeded |

20–240m plain/gzip均一个Workout一个HTTP请求；大文件未截断。精确重试同一hash另验证原ACK，duplicate不增加import/revision/job。

240m首次Import内部细分（与后续duplicate的阶段严格分开）：

| 240m first import phase | wall ms |
|---|---|
| samples_and_associations | 5,128.79 |
| route_projection | 293.15 |
| dirty_job | 70.05 |
| snapshot_receipt_commit | 1,186.37 |
| import_transaction | 7,477.77 |
| import_total | 7,558.20 |

Samples计时包含参数准备、batch insert和association写入，并非只计服务器SQL执行；原始JSON/receipt commit阶段含JSONB持久化与COMMIT。没有将这些wall时间伪装成纯SQL duration。

## 8. Resource Usage

Swift package（macOS Debug，非iPhone峰值测试）：

| Workout | JSON B | gzip B | gzip multipart/temp B | Build ms | Encode ms | Gzip ms |
|---|---|---|---|---|---|---|
| 20m | 1,378,679 | 144,727 | 144,886 | 9.46 | 704.30 | 10.51 |
| 40m | 2,757,782 | 286,311 | 286,470 | 18.65 | 1,397.34 | 21.09 |
| 60m | 4,135,071 | 426,647 | 426,806 | 27.67 | 2,121.90 | 31.08 |
| 120m | 8,270,533 | 850,624 | 850,783 | 56.57 | 4,433.69 | 64.45 |
| 240m | 16,539,662 | 1,695,621 | 1,695,780 | 118.16 | 9,061.16 | 130.78 |

对应修改前Swift JSON/gzip对照、build/encode/time与plain临时文件大小见总JSON。压缩率约90%字节减少；不是声称所有HealthKit包都具备同样比率。gzip/encode数值为wall时间，不是精确进程CPU计数器。

- iOS：指定真机JSON4,330,123B，gzip file483,025B / multipart及background upload file483,242B；gzip35.94ms、HealthKit read13.32s、route read304.34ms、build14.91s、upload1.65s。build包含HealthKit读取，不能当纯组装/encode。峰值RSS、route capture峰值和gzip峰值 **NOT MEASURED**。
- Go：总wire先限，流式有界解压，无完整compressed副本/generic树；raw JSON、typed对象和有界DB参数仍存在。进程最多两个解析上传、同device一个，DB API pool8 / Worker pool4、Worker单slot未提高并发。heap_alloc_after是测量时快照，**不是peak RSS**；每阶段峰值 **NOT MEASURED**。
- PG：实际WAL delta/index size如上，EXPLAIN记录buffer/temp/sort。PG per-process peak RSS、事务峰值与长时间pool saturation **NOT MEASURED**。没有以无限work_mem/连接/Worker并发取得PASS。

## 9. Phase 1–3 Regression 与故障矩阵

完整隔离PG18.6 `integration,swiftclient` race套件PASS，integration269.127s；临时DB数0、cluster移除。Go unit/race/vet、依赖边界249包、Swift98项、iOS App22项、Debug签名构建/Release构建、OpenAPI lint通过（31条既有warning）。真实Go→Swift→URLSession→HTTP→PG全部18场景执行；普通Swift跳过live case不替代该集成。

| RQ | 结果与真实覆盖 |
|---|---|
| 001/002/003 | PASS：旧快照不删新子事实、未知DELETE永久屏障、dirty/job/publication失效 |
| 005/006/009 | PASS：GET404/ACK lost/duplicate POST、严格DTO、严格ACK、原包/退避/崩溃恢复/GC |
| 004 | PASS：anchor crash replay、持久日志与代际；不重设nil全历史 |
| 007 | PASS：provenance/metadata历史、未知子来源屏障，不给旧事实补造来源 |
| 011 | PASS：coverage独立、失败/partial不冒充complete |
| 012/013 | PASS：route completeness、显式子删除、原GPS点序/回退/跨chunk时间包络 |
| 新故障 | PASS：body/gzip边界、真实socket deadline、PG statement timeout/cancel/lock、connection loss rollback与Worker retry |
| 载荷变体 | PASS：no Route、normal、10倍密度11,410点、多Route、50,000 Samples、2,197 Series父/4,394点 |

命令及精简日志：

| 验证 | 结果 | 证据 |
|---|---|---|
| baseline_go | PASS | [baseline_go](../evidence/rqrun-phase4-logs/baseline_go.txt) |
| baseline_swift | PASS | [baseline_swift](../evidence/rqrun-phase4-logs/baseline_swift.txt) |
| index_explain | PASS | [index_explain](../evidence/rqrun-phase4-logs/index_explain.txt) |
| phase4_n100 | PASS | [phase4_n100](../evidence/rqrun-phase4-logs/phase4_n100.txt) |
| fresh_gzip_n100 | PASS | [fresh_gzip_n100](../evidence/rqrun-phase4-logs/fresh_gzip_n100.txt) |
| swift_performance | PASS | [swift_performance](../evidence/rqrun-phase4-logs/swift_performance.txt) |
| phase1_3_full_postgres | PASS | [phase1_3_full_postgres](../evidence/rqrun-phase4-logs/phase1_3_full_postgres.txt) |
| go_race_vet | PASS | [go_race_vet](../evidence/rqrun-phase4-logs/go_race_vet.txt) |
| swift_all | PASS | [swift_all](../evidence/rqrun-phase4-logs/swift_all.txt) |
| real_socket_pauses | PASS | [real_socket_pauses](../evidence/rqrun-phase4-logs/real_socket_pauses.txt) |
| real_socket_deadline_boundaries | PASS | [real_socket_deadline_boundaries](../evidence/rqrun-phase4-logs/real_socket_deadline_boundaries.txt) |
| real_socket_throttling | PASS | [real_socket_throttling](../evidence/rqrun-phase4-logs/real_socket_throttling.txt) |
| wire_gzip_resource_limits | PASS | [wire_gzip_resource_limits](../evidence/rqrun-phase4-logs/wire_gzip_resource_limits.txt) |
| gzip_identity_corruption | PASS | [gzip_identity_corruption](../evidence/rqrun-phase4-logs/gzip_identity_corruption.txt) |
| pg_faults | PASS | [pg_faults](../evidence/rqrun-phase4-logs/pg_faults.txt) |
| ios_app_tests | PASS | [ios_app_tests](../evidence/rqrun-phase4-logs/ios_app_tests.txt) |
| ios_debug_signed | PASS | [ios_debug_signed](../evidence/rqrun-phase4-logs/ios_debug_signed.txt) |
| ios_release | PASS | [ios_release](../evidence/rqrun-phase4-logs/ios_release.txt) |
| device_isolated_pg | PASS | [device_isolated_pg](../evidence/rqrun-phase4-logs/device_isolated_pg.txt) |
| boundaries | PASS | [boundaries](../evidence/rqrun-phase4-logs/boundaries.txt) |
| openapi | PASS WITH 31 EXISTING WARNINGS | [openapi](../evidence/rqrun-phase4-logs/openapi.txt) |

历史失败没有隐藏：首轮ACK换行/错误字段路径回归已修复；新增fixture对HTTP换行的比较、burst429、锁测试计数时机、测量测试编译/Go PATH、验收host/port配置错误也分别修复并复跑。失败原因及最终结果保存在JSON的historical_failed_runs；最终没有未解决FAILED。

## 10. 真机结果

用户本轮再次指定同一Workout，并在手机上完成HealthKit读取授权；未读/导出全历史。使用新的验收Outbox命名空间，保留旧Outbox；Debug验收guard抑制正常自动恢复，不改用户持久连接Keychain。一次性PG只接收该指定记录；凭证只通过0600临时配置传入，最终App删除本轮phone配置，测试DB/cluster与DNS广告已清理。手机原有数据和已授权采集档案保留。

- iPhone 12 Pro Max / Apple Watch source / 1 Workout / 5,464 Samples / HR349。
- 5466 discovery events、12 scoped anchors、0范围外anchor；重复无变化无新work，ACK后无pending generation。
- gzip单multipart / 1 import / Durable ACK；bundle hash与服务端一致。
- source1768 = bundle1768 = chunk1768 = projection1768；manifest hash一致；PG逐点比较含原timestamp、sequence和所有坐标/属性，证据只导出计数和比较布尔。
- 真机未测断网/后台强杀；4小时只用合成数据。Simulator22项App测试不冒充HealthKit或真机性能。

## 11. Remaining Risks / NOT RUN

1. **NOT MEASURED**：iPhone/Go峰值内存、超大路线capture峰值、PG进程/事务峰值；不能由文件大小或heap快照外推。
2. **NOT RUN**：手机真实断网、系统后台强杀/调度、物理掉电；已有合成真实HTTP/PG回归仅证明协议恢复路径。
3. **NOT RUN**：家庭公网、TLS反代、fnOS生产Docker链路；代理需要body至少180s、upstream response至少255s，当前没有部署验收。
4. 原始基线内部Import分段缺失；前后是同一合成器/密度/点序但新UUID，不是严格统计A/B。SQL同数据集候选实验补充索引证据。
5. 极端Route/series仍受明确decoded/domain与既有analysis250k点上限；超限显式失败并保留事实/Outbox，不通过截断伪装成功。长期anchor数量、重复导入的身份检查成本、磁盘故障留下派生孤儿文件的容量仍需长期验证。
6. 无新TRIMP个人参数；缺失生理指标按既有算法unavailable。Worker成功不表示新的生理模型验收。
7. schema17只新增索引，可能带来写入WAL/存储成本；生产迁移/部署、RQ-010历史升级/reset与真实迁移均未执行。

## 12. Git 交付与边界

新增migration：`00017_analysis_sample_lookup.sql`。schema16→17，protocolVersion仍3，OpenAPI1.4.0。备份/恢复/ops中的schema引用同步为17，仅兼容版本标识；同步链路不依赖管理工具，未执行备份/恢复/历史迁移。

Phase4相对执行前基线修改的既有文件（包括原本untracked的Phase1–3源码）：

- `apps/server/cmd/rai/main.go`
- `apps/server/integration/p7_security_test.go`
- `apps/server/internal/admin/operations.go`
- `apps/server/internal/httpapi/server.go`
- `apps/server/internal/jobs/worker.go`
- `apps/server/internal/storage/postgres/analysis_input.go`
- `apps/server/internal/storage/postgres/analysis_publish.go`
- `apps/server/internal/storage/postgres/store.go`
- `apps/server/internal/storage/postgres/workout_bundles.go`
- `apps/server/internal/syncing/bundle_decode.go`
- `apps/server/migrations/checksums.json`
- `contracts/CHANGELOG.md`
- `contracts/openapi-v2.yaml`
- `contracts/sync-v3.md`
- `docs/IMPLEMENTATION.md`
- `ios/RAI/App/BackgroundTransfers.swift`
- `ios/RAI/App/RAIApp.swift`
- `ios/RAI/App/SyncCoordinator.swift`
- `ios/RAI/Sources/RAICompression/gzip.c`
- `ios/RAI/Sources/SyncCore/HealthDiscovery.swift`
- `ios/RAI/Sources/SyncCore/OutboxStore.swift`
- `ios/RAI/Sources/SyncCore/RAIAPIClient.swift`
- `ios/RAI/Sources/SyncCore/WorkoutBundle.swift`
- `ios/RAI/Sources/SyncCore/WorkoutBundleDelivery.swift`
- `ios/RAI/Sources/SyncCore/WorkoutSync.swift`
- `ios/RAI/Tests/SyncCoreTests/WorkoutReliabilityTests.swift`
- `scripts/rai-backup.py`
- `scripts/rai-restore.py`
- `scripts/test-monthly-restore.sh`

Phase4新增文件（精简测试日志另在evidence目录）：

- `docs/evidence/rqrun-phase4-2026-11-01.json`（不递归记录自身SHA-256）
- `apps/server/integration/workout_device_acceptance_test.go`
- `apps/server/integration/workout_performance_fault_test.go`
- `apps/server/integration/workout_performance_test.go`
- `apps/server/integration/workout_transport_test.go`
- `apps/server/internal/httpapi/bundle_body.go`
- `apps/server/internal/httpapi/bundle_body_test.go`
- `apps/server/internal/httpapi/bundle_timeout_test.go`
- `apps/server/internal/httpapi/timeouts.go`
- `apps/server/internal/syncing/phase_timing.go`
- `apps/server/migrations/00017_analysis_sample_lookup.sql`
- `docs/audits/RQrun_PHASE4_PERFORMANCE_REVIEW.md`
- `docs/evidence/rqrun-phase4-device-2026-11-01.json`
- `docs/evidence/rqrun-phase4-workspace-baseline.json`
- `ios/RAI/Tests/SyncCoreTests/WorkoutPerformanceTests.swift`

完整文件hash、原staged状态和最终五项Git证据见总JSON。当前整个工作区仍包含用户既有大量未提交修改，不将其全部归为Phase4。最终 `git status --short --branch`、`git diff --stat`、`git diff --cached --stat` 另保存于 [Git交付快照](../evidence/rqrun-phase4-git-final.txt)。无commit/push/deploy/生产数据库操作；停止等待下一阶段。
