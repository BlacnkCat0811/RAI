# RAI 运动能力模型补全：Codex 执行提示词

日期：2026-11-07（Asia/Shanghai）。版本：`rai-model-completion-prompt/1`。

将分隔线下的正文完整复制给在本仓库工作的 Codex。本提示词启动工程实现；真实健康读取／研究／部署／产品启用的授权边界见正文，不因复制提示词而自动扩大。

---

请在 `/Users/tp/raI` 实际完成运动能力模型补全工程，交付代码、测试、合同、运行工具和真实执行证据。不要只给建议、再写一遍计划，或完成一个阶段后询问是否继续。若仓库实际挂载路径不同，先核对仓库身份并使用实际根目录，不能因此修改其他同名工程。

先完整读取：

1. 仓库 `AGENTS.md` 及修改范围内适用的指令。
2. `docs/RAI_MODEL_COMPLETION_PLAN.md`：本轮范围、D01–D06、版本和E0–E11实施要求。
3. `docs/RAI_MODEL_COMPLETION_ACCEPTANCE.md`：152个MC验收ID、fixture、数值／数据库／产品／性能／真机／校准标准。
4. `docs/audits/RAI_MODEL_DESIGN_GAP_AUDIT_2026-11-06.md` 和 `docs/RAI_MODEL_ARCHITECTURE.md`：G01–G14及原始需求。
5. 当前Feature／Load／Ability／Recovery／Readiness／Athlete Product、Workout completeness／source policy／sync-v3合同，UA总合同及产品启用计划。

本轮提示词优先于旧阶段报告中“完成该阶段就停止／不进入下一阶段”的历史任务范围限制；那些报告仍是历史证据，不是本轮新PASS。保留AGENTS的数据安全、事务、测试和客户端范围等工程约束。

先检查实际HEAD、git status、工具链、migration和源码。文档编写基线为 `main / 18eab55` 加工作区、migration29；不要强制checkout该版本。保留所有已有用户改动，特别是IMPLEMENTATION、App图标和核查报告；不reset／clean／stash／整区覆盖，不修改 `../running`。优先使用当前合适checkout，不自动新建工作树或提交／推送。

## 本轮目标和版本边界

现有M1／M2／M3／M5A／M5B／M6已有候选实现，必须先确认并复用。后续M4指external VO₂投影，后续M7指产品合同工程，不能把它们当作原设计自有VO₂及现实校准已完成。

按PLAN的E0→E11连续完成全部可执行工程；遇到已有实现就补验证和追踪，不重写。既有v1数值、缺失、单位、窗口、config／model身份及历史保留；改变输出语义的新功能用PLAN规定的新版本／manifest。不要静默升级v1或将legacy指标改名冒充新模型。

本轮默认决策已明确：

- Readiness保留后续已冻结的180日有限残余、36h衰减、前42完整日滚动B、5个训练日／14日跨度／1 AU floor；不回退早期14日／周一冻结方案。
- 保留目前HR支持域及HRmax零冲突默认门槛，不为了出值放宽。
- 夜间HRV／睡眠规律性／目标／持续偏离进入Recovery v2；Load／Ability／Readiness依赖适配用显式v2。
- 新自有VO₂是 `rai.vo2max.submax/v1`，外部VO₂继续独立，不能充当其标签／约束／fallback。
- 新产品schema2／mapping2保留schema1兼容，只有active通道可被正式产品读取。

先完成E0的实际决策和G追踪文档，再继续实现，不停在E0。

## 必须完成的功能

**E1：普通同步完整性。** 将已有completeness v2接入真实普通同步和启动恢复，复用其状态机而非验收专用开关。持久ACK与完整证明解耦；inventory／cutoff／collector／流／receipt绑定；pending、分页、取消、后台到前台、离线、lost ACK和第二增量可恢复。未知来源／metadata不得静默丢弃，真实流程不得写syntheticCoverage。proof失效与readiness unknown同事务传播，不能为证明重新读取全历史或清anchor。

**E2：可信参考、重叠与来源。** 实现hr_reference/2：分别记录测量／确认时间与记录时间，按Workout start检查HRmax365日／RHR90日，合格前28日RHR baseline fallback有lineage；冲突不偷偷fallback，HRmax不自动推断。实现typed输入的幂等／撤销／supersedes、权限、Swift表单和API。重叠Workout跨UUID识别，non-overlap下界与冲突unknown，显式resolution只影响派生、不改raw。来源epoch分池、context shift／跨epoch趋势断开，误标检测不把缺步数当0。删除／替换传播有界依赖。

**E3：完整RAI VO₂估计器。** 新pure domain、真实Worker／loader／publisher／registry／dependency／admin。实现PLAN中的资格、共同时间支持、HR reference、`3.5+0.2v/r`、单Workout中位候选、42日稳健log汇总、同日／单Workout权重cap、20%异常及独立确认、7日target平滑、freshness、confidence、LOO及8角点敏感性。任何角点无定义／宽度>30%如实仅候选，不删不利角点、不用Apple或默认HR求有值。3次证据LOO支持不足按验收合同明确preliminary／null range，不伪造稳定性。保留experimental。

**E4：Recovery v2与Readiness v2。** 同源夜间00–06且有睡眠／明确静息上下文、至少2个SDNN，先ln后median；上下文不足明确不可用。保留28日稳健baseline、RHR替换和sleep union规则，增加圆周中点规律性、typed自报睡眠目标及连续3有效日偏离。v2按原设计组件公式，缺sleep／规律性不能当0或完整P。Readiness显式兼容v2上游，数学保持已决策的180日方案；unknown不归零、无Workout照常日界刷新。

**E5：跑力与研究诊断。** 保留现有demonstrated算法及固定评分，增加来源隔离；实现版本化effort annotation与独立maximal_supported模式，不以HR／课型判最大努力。实现长跑EF／decoupling／paceDecay及42日同类趋势、CS／D′研究拟合／LOO、aerobic_efficiency和load_settling分桶。全部有真实计算、资格、原因和版本；不拼同次窗口冒充独立证据。Economy／CP／无独立阈值／核验跑道距离等future项继续unsupported，不伪造评分。

**E6：统一有界回算。** 实现plan／execute／resume／status／diff／cancel，显式dataset／manifest／日期／asOf／view／operation，1–366输出日、31日或更小块、DAG warmup。中间块不可读、未完成不clean；并发事实使对应块重做。独立AffectedRanges与incremental=bounded rebuild oracle，包含RHR→reference→load→state／readiness闭包和held current引用。revised与as-known分开，历史输入不存在时如实失败，不恢复已删raw。diff默认脱敏，旧active持续服务。

**E7：正式API和共享详情。** 增加 `GET /v2/analysis/athlete` 与 `/v2/analysis/athlete/history`；schema2六个typed metric，RAI与external VO₂独立。真实RR只读active、批量验证lineage／generation／删除／freshness，reader不重算／读raw／导入admin。typed个人输入路由与receipt合同按PLAN实现，不能开放通用JSON口袋。新分析详情读已发布feature，legacy详情保留；历史cursor绑定身份，跨版本／mode／source断线。技术故障与正常缺证据不能混为成功。

**E8：原生iOS和消费者。** 同一正式API接首页／详情／历史、中文reason、缺失／过期／技术错误、VO₂来源与实验标签、单位／舍入、VoiceOver／动态字号。实际App→HTTP→PG→Worker→reader测试，不硬编码fixture冒充。训练新增显式adapter，保存输入assignment／版本／asOf；null／partial／stale保守，不当100，不自动改WorkoutKit排程。旧plan／执行历史不重写，回滚后新请求回旧adapter。

**E9：真实执行器及合成切换回滚。** 实现管理员plan／promote／rollback和versioned gate证据，最小权限，只有实际ready且依赖fresh、消费者兼容、门禁齐全才切。不能只放开CHECK或把静态boolean改true。真实dataset仍legacy；合成harness标记库／dataset，用真实执行器完成legacy→new→previous active，验证API／App／training／缓存／in-flight一致、旧stack有界补齐及raw／receipt／历史不变。synthetic证据不得用于真实目标。shadow工程只跑合成、aggregate隐私规则有效，不影响真实决策。

**E10：现实验收准备。** 交付只用合成数据运行验证的prospective快照、known-at／split／independent outcome／n/N／cluster-aware聚合／撤回工具和逐模型研究addendum。更新真实设备、availability、calibration、product手册；没真实数据也完成这些工程。不能把单人重复性说成人群效度，不能把Apple或同一输入当生理真值。

**E11：综合回归及收尾。** 运行全套必要门禁、性能和兼容验证，完成所有证据与报告。不在某个模块报告写PASS后结束本轮。

## 实现中不可破坏的约束

原始事实、删除屏障、提交日志、dirty/job和最终回执同一dataset行锁事务；dataset锁决定提交水位，不用sequence。上传不可变，同字节重试原receipt，删除优先，不从旧payload／备份／退役dataset复活UUID。

新增迁移仅追加；旧migration／checksum与原方案原文保留。typed inputs、artifacts、manifest、assignment、操作checkpoint用现有结构扩展，必要时按PLAN一张通用操作进度表，不能为每模型再建一套事实／队列／current值来源。API角色不获得candidate／任意artifact／assignment变更权限；Worker不获得raw／用户参数／activation权限，admin不能被在线图导入。

发布前检查generation、manifest、全部上游引用、lease／fencing及删除；原子提交artifact/result/publication/clean/run/job/attempt。不足证据可有完整null原因，未实现／存储／schema故障必须失败，不假成功。

只在本机Mac开发、回归、性能和验收后端运行；不使用SSH、N100／fnOS或远端数据库。自动化测试只用合成数据及新建隔离PG18，不读当前个人库／生产DSN。既有云端／手机pending／namespace不可搬迁或清空。

## 必须执行的验证

按ACCEPTANCE逐项覆盖全部152个ID，保留失败历史和skip。日常使用quick／定向测试；阶段完成gate，事务变更加受影响真实隔离PG/race。最终一次full及Swift／Simulator／正式HTTP→PG E2E，入口需要扩展就先实装再实跑，不能写不存在的命令当证据。

必须有独立oracle和边界／property／golden，至少覆盖：点／interval／series／pause／source／gap、HR reference时效、重叠／resolution、夜间context／sleep圆周中点、VO₂敏感性／LOO／异常确认、独立Workout权重、跑力noise＋step双门槛、日期／DST、删除立即失效、回补乱序重建等价、缺失／unknown、未来泄漏。

真实隔离PG要覆盖fresh／incremental migration、受限角色、事务故障和实际backend termination、两种删除锁顺序、lease／fencing／两Worker、N+1 generation、回算kill／resume、publication／clean／job原子、source／reference变更、promotion与事实竞争及真实rollback。每轮记录残留库0／cluster清理。

性能使用普通优化构建、真实loopback TCP认证→handler→PG，证明没继承race／禁优化。warmup3、至少30尝试，失败保留，n／P50／P95／max／RSS／SQL计划齐全。按MC-F组：当前快照≤6SELECT／P95≤500ms，90点历史≤8SELECT／P95≤750ms，240min feature快照≤20s／SQL≤5s，其他artifact≤2MiB，纯模型和180日回算按各自预算。不能用增大pool／超时／槽、关闭durability、降fixture密度或移动计时范围通过。旧同步性能FAIL不被新reader PASS抹去。

性能未通过时继续定位可控瓶颈并完成所有独立功能。确实受环境／结构限制仍达不到时，以实测证据保留FAIL和具体影响，不能自行降低门槛或称全部验收完成。

## 真实设备、校准和启用的边界

本提示词授权工程实现、本机合成测试、隔离migration和synthetic切换／回滚演练；**不自动授权读取真实健康记录、真实研究导出、生产访问／迁移、部署、切真实入口或激活真实模型。** 不commit／push。

若本轮会话另有明确设备／时间／类型／隔离环境授权，沿用已授权范围，不重复确认；历史报告不是授权。缺授权／设备／真实outcome时，集中说明缺少的范围并把相关MC-D／MC-C真实项标NOT RUN／NOT VERIFIED，继续完成全部独立E工程，不因这些项目暂停其余工作。不得写真实HealthKit造记录，不诱导额外最大运动测试。

现实研究必须预登记、独立outcome、athlete／time留出和样本精度理由。没criterion不去VO₂实验标签，没相应D／C／UX／隐私／性能／rollback／明确发布授权不启用真实dataset。真实shadow也需独立授权，不能借“只读”绕过隐私／cohort要求。

## 最终交付和报告方式

请交付：

1. 全部实现代码／additive migration／sqlc／typed合同／SwiftUI／运行工具／有意义测试；保留既有用户工作。
2. `docs/RAI_MODEL_COMPLETION_DECISIONS.md`：实际基线、D决策、最终版本manifest、权限、DAG、范围及G追踪。
3. `docs/RAI_MODEL_COMPLETION_RESULT.md`：E0–E11逐项、G01–G14及研究模块、全部MC编号、真实执行／数值、失败历史、兼容／回滚、剩余限制。
4. `docs/evidence/model-completion/acceptance.json` 及可复现命令／manifest／oracle／PG fault与role／API与App／性能／合成切换回滚证据。NOT RUN的exitCode=null，不写0。大日志可留`.local/logs/model-completion/`并存摘要。
5. 仅追加`docs/IMPLEMENTATION.md`本轮实际证据，原设计／旧报告的历史未验项与FAIL不改成PASS。公开材料无真实健康值、GPS、对象唯一标识或凭证。

最终分别签核：FUNCTIONAL ENGINEERING、PERFORMANCE、REAL DEVICE、DATA AVAILABILITY、MODEL CALIBRATION、PRODUCT UX、ROLLBACK ENGINEERING、REAL PRODUCT ACTIVATION。工程全部通过可以明确完成工程；没有真实证据的层级必须如实保留未验状态，不能说“全部上线完成”或“生理准确性已验证”。

请持续推进所有已授权且独立可执行的工作，记录进度与依赖。遇到P0／P1先阻断受影响发布并修复，其他安全工作继续；上下文续接时根据已完成清单接着做，不重头开始或遗漏阶段。
