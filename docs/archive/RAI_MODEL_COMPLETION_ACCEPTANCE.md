# RAI 运动能力模型补全验收标准

日期：2026-11-07（Asia/Shanghai）。版本：`rai-model-completion-acceptance/1`。状态：**待执行的标准，不是通过报告**。对应[实施方案](RAI_MODEL_COMPLETION_PLAN.md)和[执行提示词](RAI_MODEL_COMPLETION_CODEX_PROMPT.md)。

## 1. 判定与证据规则

每个ID必须有独立状态、实际命令／场景、观察值、预期、exit code、证据路径／摘要、失败历史、模型／manifest身份和未验证限制。允许一条测试覆盖多个ID，但每个ID仍给明确断言和证据，不能凭文件存在统一填PASS。

状态：PASS、FAIL、PARTIAL、NOT RUN、NOT OBSERVED、NOT VERIFIED、BLOCKED；确实不适用可N/A，但需说明范围和理由，不得将mandatory工程项改N/A逃避实施。编译通过、mock、schema检查、synthetic、隔离PG、Simulator、真实设备、现实模型验证和部署分别留证。

- B／S／I／V／R／A／H／P／X／T／Z组为工程功能硬门禁；除明确真实范围外均需在本机合成环境实际通过。
- F组单独签性能，不因功能通过被覆盖；未达目标保留FAIL，并给定位和影响。功能可以单独PASS，整体不得声称全部验收通过。
- D组为真实设备通路；C01–C04工程工具必须完成，C05–C08为真实研究。无授权／数据／时间时诚实NOT RUN，继续其他工程，不虚构或借机停工。
- 真实产品启用还需独立D／C／UX／隐私／性能／回滚／授权证据，synthetic gates不代替真实门禁。

数值默认误差：纯代数／解析积分 `absError≤1e-9*max(1,abs(expected))`；复杂拟合与独立oracle `relativeError≤1e-7`，score≤1e-6分。出现更差条件数时先解释误差来源并用高精度oracle验证，不能宽松到掩盖模型错误。日历／UUID／计数／状态／原因／hash／精确字节身份按精确匹配。所有非有限数必须显式拒绝或null，不得JSON输出NaN／Inf。

## 2. 合成fixture与独立oracle

固定seed、生成器版本和manifest；fixtures只用合成值，不复制真实健康分布或UUID。至少包括：

| 组 | 覆盖 |
| --- | --- |
| W | 20／45／60／120／240min；HR点／interval／condensed／sealed series；pause／gap／多来源；零距离；无HR／无功率；跨午夜及23／25h DST |
| L | 400日以上随机ledger、完整／partial／unknown、跨日、重叠／相接／链式重复、回补与删除 |
| V | 稳态、温湿度边界、参数年龄／epoch、3／4／5／10独立Workout、极端候选、确认延迟、敏感性可用／过宽／无定义 |
| R | 60日以上日常事实；白天呼吸练习／夜间睡眠／上下文缺失；RHR替换；跨午夜／午睡／awake重叠；规律／移相／缺睡眠／source变更 |
| A | 无历史／短证据／长短齐备／异常PB／连续进步／停更／设备偏差／确认最大努力／CS退化与不可识别 |
| O | 180日、180个Workout的全栈回算；31日分块；中断、并发删除、超限、历史缺失、promotion与rollback |

独立oracle不得只调用被测函数反向生成“真值”。负荷用高精度数值积分＋直接有限卷积；VO₂有手算单候选＋独立稳健统计／water-fill；曲线独立枚举breakpoint与高精度WLS；恢复独立手算组件；影响范围比较增量与从合法有限输入直接重建。生成器包含latent轨迹只能证明工程响应，不能证明生理准确性。

## 3. B组：基线、版本、不变量（E0）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-B01 | 实际HEAD／status／AGENTS／最高migration留存；入口用户改动保留，无reset／clean／覆盖 |
| MC-B02 | 原设计、原架构方案、旧migration／checksum不变；旧v1模型与schema golden字节／数值／缺失行为保持 |
| MC-B03 | G01–G14及可选／future全部追踪到阶段、文件和本矩阵；已存在实现先核实后复用 |
| MC-B04 | D01–D06及实际component／config／manifest／schema／mapping／consumer表冻结；影响结果的变化具有新身份 |
| MC-B05 | 新DAG真实handler有执行路径；未实现kind拒绝，不能伪造success／clean |
| MC-B06 | 上传同字节重试原receipt，删除优先及dataset锁提交水位回归通过；不新增sequence水位 |
| MC-B07 | 模型纯函数及online→admin边界检查通过；API／Worker同镜像，新增管理代码不可在线导入 |
| MC-B08 | 无Web／Watch／桌面／写HealthKit／WorkoutKit自动排程扩展；无生产／远端DB、旧running修改、自动部署／真实迁移 |

## 4. S组：正常路径完整性（E1）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-S01 | 普通App同步和启动恢复实际调用completeness v2；不依赖验收环境变量／手工CLI才进入 |
| MC-S02 | 单Workout durable ACK不等全历史；完整证明异步续跑，App明确区分已上传、分析中、全量未齐 |
| MC-S03 | eligible／excluded／unknown inventory逐项闭合；第三方／不支持字段不被静默丢弃来凑complete |
| MC-S04 | cutoff／collector／流fingerprint／generation绑定；捕获中迟到和删除不能提前确认或污染旧manifest |
| MC-S05 | lost ACK／损坏ACK／offline／重启后复用同一manifest bytes和原receipt，committed anchor不提前推进 |
| MC-S06 | 正常第二增量使用已提交anchor，未变化时无重复import／模型重复计票，不重置bootstrap |
| MC-S07 | 未耗尽分页／unsupported／unknown来源／receipt缺失均保持incomplete；无真实syntheticCoverage写入路径 |
| MC-S08 | proof后新事实、删除、collector／epoch／source policy变化在同事务撤下coverage，readiness回到unknown |
| MC-S09 | App→真实HTTP→隔离PG→Worker，verified receipt后v2 reader实际获得coverage；unknown AU仍为unknown |
| MC-S10 | 取消／后台到前台／pending优先／共用限流安全；保留gzip、capture manifest和原ACK身份回归 |

## 5. I组：输入、参考、重叠和来源（E2）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-I01 | hr_reference/2分别区分measuredAt、recordedAt、effective range；Workout start时HRmax≤365日／显式RHR≤90日，精确边界与未来测量拒绝 |
| MC-I02 | 缺HRmax、冲突／撤销reference、HRmax≤RHR、非finite、未知method／unit明确null或4xx；无年龄公式／默认HR／运动最低HR |
| MC-I03 | 显式RHR优先；显式冲突不fallback；baseline fallback只用前28日同epoch，≥14日且前7日≥3，今日／未来不进入 |
| MC-I04 | input同ID同canonical bytes重试原receipt；不同body冲突；supersedes／revoke同dataset／kind／subject且无环；输入、审计、dirty/job、receipt原子 |
| MC-I05 | baseline／reference删除或替换立即使相关load／VO₂／readiness current失效；不同训练保留当时run/hash，不全换今天参数 |
| MC-I06 | 不同UUID相同active区间、部分重叠、summary、跨日、链式重叠检测到；仅端点相接不误报；输入次序不影响结果 |
| MC-I07 | 重叠部分不双算、不静默选赢家；non-overlap已知下界守恒，full day／state／readiness为unknown；原始每次剂量仍可审计 |
| MC-I08 | resolution选择primary仅改变派生；撤销／删除一方后正确恢复；不得改raw、删receipt或复活UUID |
| MC-I09 | 重叠邻域使用dataset＋time索引；超限明确错误；扩展链不会无界扫描或截断后标完整 |
| MC-I10 | 新source epoch有context_shift／新池；注入固定速度偏差后首次结果不作为进步，无跨epoch趋势；成熟后仍保留未独立校准限制 |
| MC-I11 | 持续高速度＋已知步数／路线证据形成误标诊断；无步数不当0；表现／VO₂拒绝与HR load独立 |
| MC-I12 | HR／睡眠目标／effort／resolution／source selection typed输入API及Swift表单有真实HTTP／PG合同、所有权／越权／未知字段／撤销测试；不写HealthKit |

## 6. V组：RAI VO₂max（E3）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-V01 | 实际新pure domain＋job／loader／publisher／admin；identity为submax，与external完全分离；日志不含VO₂值 |
| MC-V02 | v=156m/min、r=.65单候选=51.5；m/s↔m/min无60倍错误；正反边界／非finite／单位错误通过独立手算 |
| MC-V03 | 25min／10min热身／恢复后3min／5min网格／3段15min边界准确；不跨pause／gap拼稳定段 |
| MC-V04 | HR／speed共同支持≥95%、r／speed范围、CV／trend／drift／grade／pause／distance门槛逐一边界拒绝，缺grade不出值 |
| MC-V05 | 温湿度边界、缺天气cap≤.5、范围外拒绝；不调用天气或旧环境修正 |
| MC-V06 | 缺／过期／冲突reference、混epoch、参数变更、未来known-at均按合同拒绝；不以Apple或Load可用性补值 |
| MC-V07 | 单Workout多段／拆series／重试不增加n_eff；同日初始权重≤1、最终每Workout≤1/3，water-fill对独立oracle |
| MC-V08 | ≥3独立／≥7日／最近14日条件精确；42日池、MAD floor .04、winsorization与独立oracle一致 |
| MC-V09 | >20%异常先隔离，独立±10%／14日内确认；后来的确认不能进入较早asOf／预测 |
| MC-V10 | h−6…h smoothing及3有效target日；重复日目标不增独立支持；无新证据4日值不变，confidence不升 |
| MC-V11 | confidence公式／n_eff／cap／preliminary／established固定；无实验室验证始终experimental，无“准确率百分比” |
| MC-V12 | 8敏感性角点全部计算；无定义则null，宽度>30%则candidate-only；不得丢不利角点。有合格可出值fixture和合法abstention fixture |
| MC-V13 | LOO范围真实按Workout重拟合，原证据仅3次而LOO支持不足时明确null range／loo_support_insufficient／preliminary cap≤.4；不得伪造窄范围 |
| MC-V14 | 14／28／42日aging／stale／expired及current清值正确；删除／reference撤销立即失效，旧artifact不当fallback |
| MC-V15 | 持续同条件改变的合成独立证据在28–42日内显示同方向趋势；单次热天／+30%异常不主导；不因有训练自动上涨 |
| MC-V16 | 合成Apple49与RAI52同时保留不同source／time／assumption；无平均／校准／覆盖。模型输出与DB／API／Swift单位和null一致 |

## 7. R组：Recovery／Readiness v2（E4）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-R01 | 夜间00–06与主睡眠／明确静息交集，≥2 SDNN，同epoch，先ln后median；只有凌晨无context不冒充静息 |
| MC-R02 | 日间呼吸练习、acquisition duration／source／algorithm变化、少于2次、来源冲突均单独原因；不混全天v1baseline |
| MC-R03 | 前28日、14日／近7日3日、median／MAD／floor及今日不入baseline精确；常数／离群／step／缺日oracle通过 |
| MC-R04 | RHR删除＋新增替换、live冲突、provisional／36h freshness处理正确；运动最低HR不可替代 |
| MC-R05 | asleep／awake／generic-stage重叠只计一次；跨午夜／DST／nap／迟到主睡眠尾部归属及knownness不回退 |
| MC-R06 | 23:50和00:10圆周偏差20min；历史medoid、同值／对径歧义、14晚／近7晚3晚规则一致；不以0补规律性 |
| MC-R07 | 自报target有效期／撤销／替换优先，未设置用历史habit；今天／未来目标不改旧run，目标(0,24]输入边界 |
| MC-R08 | 原文新sleep公式、max/min autonomic贡献独立手算；少睡1h且其他相同时P乘exp(−.1)，三组件贡献可重建总分 |
| MC-R09 | sleep／regularity缺失为partial；仅autonomicOnly不能流入完整readiness；任何缺失不变0小时／0burden／100分 |
| MC-R10 | 同组件连续3当地有效日不利偏离才persistent；缺日打断；zH≤−3 context_shift且不奖励；不输出疾病／过训诊断 |
| MC-R11 | Readiness v2明确只消费allowlistedLoad／Recovery v2；刻意混v1／错误AU／config／calendar时fail closed |
| MC-R12 | D02的180日／36h／滚动42日B／5日与14日跨度／floor保持；D=0得P，D=B得P/2；固定P/B增加load不能升分 |
| MC-R13 | unknown／partial load、overlap、coverage过期和Recovery不足均full null；无Workout日刷新／日界／Freshen实际发生且不虚增confidence |
| MC-R14 | 日常事实变动的signal＋baseline＋persistent＋readiness依赖闭包oracle通过，历史v1结果及旧消费者不被偷偷修改 |

## 8. A组：跑力与研究诊断（E5）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-A01 | demonstrated保持90日／长短独立证据／γ网格／80%quantile／评分锚点，v60=3得50；旧v1golden不变 |
| MC-A02 | +30%孤立异常隔离；替换一个正常证据+5%后score变化≤2分；+5%持续独立表现每周≥3且长短齐备时42日内吸收≥60%log速度变化 |
| MC-A03 | 无证据4日score不变；14／28／42日状态／清值；单次5K不补长证据，不因负荷／VO₂／休息加扣分 |
| MC-A04 | maximal需≥3独立明确annotation、duration≥2倍且≥40min；50%quantile与cap≤.7；高HR／计划课型不自动确认 |
| MC-A05 | annotation撤销／晚到／未来可见、Workout删除、模式／source切换不泄漏过去target；不同mode不连trend |
| MC-A06 | 长跑≥60min、热身10min、两半等有效时长、90%支持／CV／来源／地形门槛；不拼接暂停，缺HR仍可有paceDecay |
| MC-A07 | EF、decoupling、paceDecay手算一致；均速差>5%不解释HR drift；42日≥3同类趋势／90日最长时长不冒充最大耐力 |
| MC-A08 | CS 3–15min、≥3Workout／≥3duration／跨度比≥2.5，每Workout权重≤1；同一次10min截3段不能满足独立支持 |
| MC-A09 | CS／D′独立WLS、LOO≤5%、RMSE≤3%及正值／CS<观测均速；病态／负D′／退化／单时长null；不只凭R²通过 |
| MC-A10 | aerobic_efficiency单位m/beat，m/s先×60；无HRmax／RHR可输出合格旁路，同上下文才比较，不叫VO₂／economy |
| MC-A11 | settling D=0、B缺失、P<85、coverage不足、非finite及12／24／36／48h精确分桶；不预测未来睡眠／新增训练 |
| MC-A12 | CS／D′／maximal／settling均研究态、独立version／scope／reasons，失败不阻断无依赖模型；future Economy／CP／阈值不伪装实现 |

## 9. H组：回算、历史和差异（E6）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-H01 | 显式dataset／manifest／日期／asOf／view／operation；1–366日，非法范围／未来／过多输入拒绝；plan零业务写入 |
| MC-H02 | 180日／180Workout／31日分块从事实到全栈candidate完成；必要warmup与缺失标记真实，不用0伪造历史 |
| MC-H03 | 每阶段／块／最终ready前kill并resume，operation身份不变、无重复artifact／receipt／计票；同operation参数变化冲突 |
| MC-H04 | 中间块不可见、未覆盖dirty不能clean；最终manifest ready前核验全部目标／依赖，active原输出一直可读 |
| MC-H05 | 回算中new facts／delete／parameter／source变化只重做受影响块；旧generation SUPERSEDED，不发布半新半旧结果 |
| MC-H06 | AffectedRanges独立oracle覆盖d+179／186、RHR+28再组合、VO₂+47／持有、ability+102／持有、sleep／persistent邻域 |
| MC-H07 | Incremental、乱序到达、删除／回补、bounded rebuild在同view／asOf／合法输入下最终等价；超限不静默截断 |
| MC-H08 | 原历史artifact与withdrawn标记保留；as-known缺输入明确失败／原run审计，禁止备份／退役dataset／旧import复活 |
| MC-H09 | diff有null转移／version／mode／unit界线、支持原因和脱敏默认；不将非同义legacy／new直接相减为准确性 |
| MC-H10 | cancel只结束操作／未发布派生，不删除raw／已ACK事实／旧历史；status／checkpoint／审计可复现且权限受限 |

## 10. P组：API、详情、SwiftUI与训练（E7–E8）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-P01 | 正式GET /v2/analysis/athlete实际通过认证→Store→PG读取；active=legacy时not_product_activated，无candidate参数后门 |
| MC-P02 | schema2六种typed metric、version／source／unit／mode／validation／各时间完整；schema1仍兼容，未知schema值不盲展示 |
| MC-P03 | 单RR快照验证assignment／generation／lineage／delete／freshness；reader不计算模型／读raw／导入admin；越权404／403按合同 |
| MC-P04 | history limit≤90、日期／metric／view／opaque cursor绑定；同版本28日trend有真实两点支持，跨mode／source／config断线 |
| MC-P05 | detail新分析读取发布feature，dirty/pending为null；raw展示降采样不做分析输入；legacy原DTO与行为保留 |
| MC-P06 | App经实际API展示新首页／详情／VO₂分源／历史，合成active切换真实流转；不以硬编码fixture代替该E2E |
| MC-P07 | collecting／limited／unavailable／stale／conflict／sync_incomplete／technical_error逐种UI；null≠0／100，过期不显示fresh |
| MC-P08 | 中文reason／时间／单位／舍入正确；VO₂≤1位小数、score整数、来源与experimental清晰；无概率／医学／潜在最大能力误称 |
| MC-P09 | VoiceOver顺序／标签、最大可访问性字号、深浅色、窄屏及未知版本截图／实际断言无关键信息截断；无raw标识泄漏 |
| MC-P10 | 新训练adapter只消费active兼容fresh模型；低值沿用提示、不自动改排程；unknown／partial／stale保守，不当100 |
| MC-P11 | 新plan保存assignment／模型／asOf／missing lineage；旧plan与执行历史不重写；回滚后新plan回旧adapter |
| MC-P12 | 无HealthKit写入、无额外WorkoutKit授权／排程、无通知副作用；首页／Coach／训练／详情／历史不泄漏candidate |

## 11. X组：受控切换、shadow和回滚（E9）

| ID | 场景及PASS标准 |
| --- | --- |
| MC-X01 | allowlisted manifest＋append-only assignment revision／audit；旧active仅legacy兼容升级，普通API／Worker不能变更 |
| MC-X02 | gate绑定模型／config／source／consumer／dataset／cohort／时效／reviewer／报告身份；改一个字段／过期／缺项必须拒绝 |
| MC-X03 | CLI不能仅传true越过gate；synthetic数据库／dataset标记由harness证明，synthetic证据不能用于真实目标 |
| MC-X04 | dirty／半回算／缺handler／failed／旧upstream／不兼容consumer拒绝promote；合法clean insufficient与未实现失败可区分 |
| MC-X05 | promote与new fact／delete／Worker publish竞争，dataset锁内重验，不读半新assignment，不产生错clean |
| MC-X06 | synthetic真实执行legacy→new active，正式API／SwiftUI／training consumer全部返回同assignment身份，精确记录切换 |
| MC-X07 | previous stack持续有界dirty／warm standby；失去freshness必须补齐再rollback，禁止切旧冻结数值 |
| MC-X08 | synthetic新active→previous active实际回滚，所有消费者／缓存／in-flight结果恢复；旧请求不能覆盖新revision |
| MC-X09 | 两次切换后raw／tombstones／receipt／commit顺序／旧新artifact／plan历史保持；不依赖Down／备份／全量重算 |
| MC-X10 | shadow合成运行真实candidate reader，仅aggregate、组大小抑制／关闭／保留规则有效；不影响legacyUI／plan／通知 |
| MC-X11 | 无真实gate／授权时真实目标保持legacy，默认工具不会发现／修改真实dataset；dry-run无启用副作用 |
| MC-X12 | gate过期／证据撤销／consumer不兼容与rollback失败有明确状态和审计；无隐式自动推广所有账户 |

## 12. T组：真实隔离PostgreSQL／事务／故障

使用本机新建PG18、合成数据、开启fsync／full_page_writes／synchronous_commit／checksums；只允许本机测试入口，不用现有个人库或环境中继承的生产DSN。

| ID | 场景及PASS标准 |
| --- | --- |
| MC-T01 | fresh及从当前最高schema incremental migration均过；旧checksum不改，sqlc二次生成稳定，边界／恢复测试更新 |
| MC-T02 | raw／delete／log／dirty/job／receipt同事务故障；任一点rollback无部分事实／receipt，原字节重试原receipt |
| MC-T03 | 新input／audit／依赖撤销／job／receipt事务故障、跨subject／dataset／supersedes环均正确拒绝 |
| MC-T04 | artifact后、result后、publication前、clean前、job terminal前实际PG backend termination；无半发布，重试一致 |
| MC-T05 | snapshot中N+1、lease过期、两Worker、fencing token变、published前kill，旧run不能发布／clean新代际 |
| MC-T06 | 删除与上传两锁顺序，late child／series／route、RHR替换／sleep跨日／无Workout日更新正确触发所有依赖 |
| MC-T07 | API／Worker／owner/admin真实角色测试，candidate／artifact／input／assignment／ledger越权负例；最小权限不靠superuser掩盖 |
| MC-T08 | 数据集隔离、RR读取／assignment race、corrupt hash／schema／missing ref区分技术失败与业务缺失，不输出错误complete_response |
| MC-T09 | 重算分块、promotion、rollback与事实提交竞争覆盖真实数据库；操作幂等及审计一致 |
| MC-T10 | EXPLAIN ANALYZE覆盖workout/time／reference／overlap／artifact／history范围索引，无lifetime raw扫描或N+1 |
| MC-T11 | App／Swift→真实HTTP→隔离PG→Worker→typed reader／UI E2E，包括lost ACK、unknown、删除失效、两个assignment和rollback |
| MC-T12 | full回归及受影响专项pass；每轮留存exit／pass／fail／skip，残留测试库0／cluster清理；opt-in skip不冒充通过 |

## 13. F组：性能、容量和资源

以下是**本轮拟议工程目标**，并非已测结果或生产SLO。E0运行前冻结同机器fixture／构建／预算和baseline。测量普通优化构建，固定durability／默认pool／上传槽；warmup3、至少30尝试，报告n／failures／P50／nearest-rank P95／max。失败尝试不得删除；有失败时延按单独失败率与终止时间报告，不只给成功子集。

| ID | 必测对象 | 通过阈值 |
| --- | --- | --- |
| MC-F01 | 构建／计时可信度 | 证明无race／禁优化；真TCP认证→handler→PG；硬件／竞争负载／版本／fixture／测量范围齐全 |
| MC-F02 | 六metric当前快照 | ≤6个应用SELECT（不含BEGIN／COMMIT），无逐metricN+1；HTTP P95≤500ms；不计真实WAN |
| MC-F03 | 90点单metric历史 | ≤8个SELECT，HTTP P95≤750ms；cursor续页不扫raw或全生命周期 |
| MC-F04 | 240min、≤250k输入点feature | 单快照≤20s、单SQL≤5s；published feature≤128KiB，其他单artifact≤2MiB；超限拒绝不截断 |
| MC-F05 | 纯VO₂／Recovery／Readiness | 各最大允许bounded fixture计算P95≤250ms；包括敏感性／LOO属于VO₂计算口径，不能移出计时 |
| MC-F06 | 跑力＋CS／durability最大窗 | 各计算P95≤2s；独立benchmark给alloc／B/op，输入翻倍无无解释O(N²)raw重扫 |
| MC-F07 | 180日／180Workout、31日块 | 首次完整全栈回算≤15min；恢复只执行未完成／失效块，无全历史重启；活跃旧reader无技术错误 |
| MC-F08 | 服务资源和切换 | 独立bench API＋Worker峰值RSS≤768MiB；synthetic warm rollback事务≤2s，前端消费者≤10s识别新revision；补算耗时另计不能藏进“瞬时回滚” |
| MC-F09 | 同步／完整性回归 | 相对本轮同环境baseline的单Workout持久ACK P95不退化>15%，ACK路径无额外等待全量proof；历史同步性能FAIL继续保留，不能据此签旧SLO已达标 |
| MC-F10 | 原生UI | 合成Simulator／获准真机分别报告：主线程无>250ms停顿，交互P95≤100ms；RSS／型号／构建／输入密度实测，Simulator不代签真机 |

已知同步旧SLO及密集真机RSS仍未通过时，PRODUCT PERFORMANCE不能总PASS；可以分别记录本轮新reader性能PASS。未达阈值不放宽pool、timeout、耐久性或降低fixture密度。新算法max bound需要超出当前默认容量时必须新config并先验证资源，不能静默扩大。

## 14. D组：真实设备／真实数据通路

执行仅限后续会话明确授权范围；本方案和提示词本身不授权扫描私人历史、真实HealthKit或旧云数据。后端在本机Mac，独立namespace／库，保留既有云端状态。不能向真实HealthKit造合成记录。

| ID | 实际证据要求 |
| --- | --- |
| MC-D01 | 指定设备／OS／Release build／来源和读取时窗，签名安装、只读授权请求及可见范围真实观测；查询空不当授权成功 |
| MC-D02 | 普通入口首轮全量eligible inventory→事实ACK→completeness receipt→committed anchors，非验收专用路径 |
| MC-D03 | 同设备第二增量／无变化重复；0重复import、manifest与anchor单调且不提前确认 |
| MC-D04 | Daily SDNN／RHR／Sleep和external VO₂ metadata／source真实支持／缺失／冲突，夜间context无则如实不可用 |
| MC-D05 | 飞行／API不可用／DB不可用／lost ACK／重开，pending身份与原receipt保留；不记录健康值 |
| MC-D06 | 五处kill：capture持久前后、send前、commit后ACK前、本地ACK落盘前、完整性finalize；没有精确hook的点记NOT EXECUTED |
| MC-D07 | 自然发生的delete／RHR替换／迟到／source变更即时撤引用，不主动篡改健康记录；未观察记NOT OBSERVED |
| MC-D08 | 后台→前台eventual恢复、主线程／内存／耗时；真实系统调度限制单列，不能模拟器替代 |
| MC-D09 | 普通流程verified completeness进入真实readiness；缺reference／合格段时null是正常观察，不补参数／coverage求有值 |
| MC-D10 | 每模型n/N／reason／cold-start／来源密度聚合，无raw值／真实UUID／GPS／凭证进入git；真实UX和模型可用性逐项签核 |

必测实际失败使相应D项FAIL；自然事件未观察不应伪造通过，研究／启用范围须排除未验组合或继续等待获准自然观察。

## 15. C组：现实可用性与校准

| ID | 标准 |
| --- | --- |
| MC-C01 | 合成prospective快照／known-at／撤回／化名映射分离工具真实运行；研究与普通日志隔离 |
| MC-C02 | 合成athlete互斥及past→future split自动检查；同Workout窗口不能既输入又outcome；未来确认／revised不冒充旧prediction |
| MC-C03 | n/N／零分母／abstention／cold-start／within/between-person与cluster-aware区间工具有独立oracle；小组抑制测试通过 |
| MC-C04 | 每模型study addendum含目标、cohort／来源、样本／精度依据、时窗、outcome、版本、阈值、reviewer、授权／撤回／停止线；未签核则真实gate关闭 |
| MC-C05 | 真实4周以上可用性观察：逐模型及用户的eligible／完整有值／partial／null分布，说明采样／reference／夜间context阻断；不能删无值用户或降低模型资格 |
| MC-C06 | 跑力／耐久性未来独立可比表现留出验证，区分demonstrated与maximal；达到预登记误差／覆盖及不确定性门槛才接受对应用途 |
| MC-C07 | 自有VO₂有独立正规gas-analysis criterion才评估准确性；Apple只作comparator。无criterion保持experimental，不能用同公式fixture去实验标签 |
| MC-C08 | Recovery／Readiness纵向独立主客观outcome，训练前冻结prediction、报告在看分数前收集；不以相同HRV／RHR输入自证、不给医学恢复／伤病结论 |

### 15.1 执行前必须固定的现实数值门槛

原设计给出的以下数值是**候选用途门槛**，不是现有成绩或公认生理标准。后续研究应在看结果前接受／另立更合理预登记版本，不能读完结果再改。未完成样本量／精度论证时不能给正式validation PASS。

| 对象 | 本方案推荐的预登记起点 | 不可替代的前置 |
| --- | --- | --- |
| demonstrated表现预测 | 独立留出相对误差中位≤5%、P90≤10%；同时报告signed bias／MAE／RMSE及置信区间、duration／source分层 | 目标必须是可比持续表现；不能用统一指数先制造“真实v60”；可用率需同时接受 |
| RAI VO₂ | 真实criterion MAE≤5mL/kg/min、绝对signed bias≤2；报告Bland–Altman及分层，门槛区间判定需预登记 | VO₂max／VO₂peak定义、时间匹配、criterion质量、独立athlete/time留出；Apple不是criterion |
| 输入／输出可用性 | 不统一编造“80%人人应出值”；按目标用途／cold-start及来源分别冻结最低availability和最大abstention，零分母N/A | 未填写这些数值则此产品用途gate关闭；不能把模型资格阈值改为满足可用率 |
| Recovery／Readiness | 先固定独立outcome、观察延迟、最小有用变化及可接受误报／漏报或预测误差；报告个人纵向及人间差异 | score不是概率／恢复百分比，不可直接设“MAE≤5恢复分”当生理真值；无依据可先descriptive-only，但不能晋级 |

真实研究默认建议8–12周纵向观察。单人只能支持个人重复性／可用性结论，不宣称跨人群。多人的实际数量须按主指标精度、重复测量结构和预期缺失计算并预登记，不用大量单人的Workout凑“多人样本”。样本不足／区间跨接受线记INCONCLUSIVE而不是PASS；失败不靠删除坏例或原地调v1／v2消除，需新候选版本及独立验证。

## 16. Z组：完成门禁与交付

| ID | PASS标准 |
| --- | --- |
| MC-Z01 | 日常quick／定向、阶段gate及事务受影响真实隔离PG实际执行；最终full成功，含Go race／vet／build／sqlc／边界／全量PG；不以quick替代 |
| MC-Z02 | Swift package、App Simulator、正式HTTP／PG E2E实际通过，unknown schema／missing状态与旧合同回归完整；跳过与外部host条件单列 |
| MC-Z03 | DECISIONS／RESULT／机器可读acceptance矩阵／版本manifest／G追踪齐全；每ID证据可定位，失败轮不删除 |
| MC-Z04 | IMPLEMENTATION仅追加真实结果，旧前缀和用户图标等改动保留；原文／migration hash／git diff --check通过 |
| MC-Z05 | 公开证据隐私扫描、权限／实际执行命令复核，无健康值／凭证／真实标识；所有新增脚本可复现且本机入口无SSH回退 |
| MC-Z06 | Functional／Performance／Device／Availability／Calibration／Product UX／Rollback engineering／Real activation分别签核；未执行范围写明，最终无假成功 |

## 17. 机器可读结果格式与最终报告

工程实施交付 `docs/evidence/model-completion/acceptance.json`，建议顶层字段：schemaVersion、startedAt／finishedAt、gitBaseline／finalWorkingTree、toolchain、manifest、syntheticOnly、phases、checks、unresolved、authorizationScope。每check必须有id、status、expected、observed、commands、exitCode、evidence、reason、executedAt、scope和失败历史引用。NOT RUN的exitCode为null，不能写0。

每个phase列实现清单、影响文件／合同、实测、事务／权限证据、未验证项及下一依赖。最终不得仅写“完成M1–M7”；必须按本轮E阶段及MC编号，给G01–G14和研究模块的闭环状态。

本文件共有 **152 个唯一验收ID**；E工程执行者不得删ID缩小范围。真实环境不具备的D／C项目可以保留未运行，仍须把对应工程准备和所有独立可执行部分完成。
