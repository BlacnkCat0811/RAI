# P6 Native iOS Product Contract

2026-10-28（Asia/Shanghai）。P6_BASE_COMMIT=`62511f9b3e5998ccad79396a7e14540074e506e5`；入口工作区干净。原方案和P3–P5历史证据保留。只建设原生iPhone产品；iOS 18+，Swift 6 complete strict concurrency。真机、发布与P7不是本合同的已完成项。

## 视觉基准与设计系统

用户提供的暖暗 Liquid Glass 首页图是视觉基准：Greeting → Today Training Hero → Body Status → Weekly Overview → Recent Activity → 浮动系统Tab。实现是SwiftUI布局、Shape、Swift Charts、SF Symbols和原生控件，没有截图切片、假地图或固定健康值。用户头像使用SF Symbol；称呼可配置，初值为“跑者”。

背景为静态炭黑/暖棕底、橙色环境光与冷蓝阴影，延伸safe area。Hero使用独立生成的日落城市/滨水跑步摄影背景，资源随App打包并由单一入口缓存；不包含界面文字、数字或截图切片。1280×853 JPEG，缺图时退回原生抽象跑道/SF Symbol。人物与城市不与参考图完全相同；生成提示词与来源见[素材记录](evidence/p6-hero-provenance.md)。

统一`RAITheme`：orange≈#FF7A1A、amber≈#FFB35C、charcoal≈#161412、brown≈#2B211B、white、66%secondary；Fitness橙、Fatigue紫、Recovery绿、Readiness蓝橙渐变。大卡圆角28pt，轻控制18pt、CTA胶囊。全部系统字体；大数值使用rounded系统字形和monospacedDigit。字号使用语义text style随Dynamic Type变化，不把业务数字放在固定高度容器。

`RAIGlassSurface`统一shape/tint/material/contrast。iOS26使用经Xcode26.5实际编译的`glassEffect(.regular.tint(...), in: RoundedRectangle(...))`；18–25以及无Metal的模拟器使用ultraThinMaterial、低透明暖色和0.5pt高光。Reduce Transparency直接切换高不透明棕色surface。卡内的标签/进度条/按钮只使用轻量填充，没有卡套卡玻璃。当前没有需要多个玻璃形状融合的交互，因此不额外建立GlassEffectContainer/morph系统。

TabView、NavigationStack、toolbar、sheet、Form沿用系统。系统iOS26提供Liquid Glass Tab；fallback仍是原生Tab。参考：[Apple Applying Liquid Glass](https://developer.apple.com/documentation/SwiftUI/Applying-Liquid-Glass-to-custom-views)。不自绘替代Tab，不使用第三方UI框架。

## 数据与状态边界

Swift只做呈现、命令、单位格式化与图表归一化，不复制TRIMP、Fitness/Fatigue、Recovery/Readiness、coach生成或Effective合并。`ProductCore`的actor封装`RAIAPIClient`；网络/JSON解码在actor或非MainActor async执行器；MainActor只发布UI状态。P3 outbox不改身份/anchor/ACK语义。产品命令使用独立、原子、受文件保护、排除备份的journal，并绑定service/dataset/epoch/origin。

正式App没有Mock默认数据。Preview、unit test和合成E2E必须标明用途。Published Analysis缺失、dirty或insufficient显示`--`。Recovery原值为0..1，展示百分比仅做单位格式化；不是身体医疗诊断。Fitness/Fatigue单位为AU/day，不当作0..100生理分数；两条bar使用当前已知Fitness/Fatigue的共同显示尺度，仅作图表归一化。周统计只代表已同步跑步，不宣称导入覆盖完整。

页面下拉刷新；前台每20秒有界刷新，后台停止刷新。发布新Effective不清空Sheet的本地草稿；新计划保留原版本到服务器返回新版本，列表基于稳定sessionID。Home/Training详情通过独立selection驱动导航，修改课程或移出今日列表不会销毁已打开详情。当前没有全历史下载或全库重算。

## 导航与首页

五个原生Tab：首页、运动、训练、分析、我的。首页一个垂直ScrollView；Greeting显示真实P3同步状态（尚未同步/同步中/待同步/需处理/本轮已同步），不推断HealthKit读权限。Hero读取dataset日历当天Effective Session；没有课程显示空态，不偷偷换成明天课程。

Hero显示服务器实际距离、时长、RPE和修改标记；未给时长不会推测45min。点击以iOS18原生matchedTransitionSource/zoom展开详情，Reduce Motion或无Metal模拟器时关闭zoom。Body Status使用原生Readiness ring和紧凑指标。周卡两个Swift Charts柱图，首次出现250–400ms动画，Reduce Motion无动画。Recent Activity来自只读Workout列表；无路线读取接口时只显示运动图标。

## Activities与Analysis

Workout列表使用每页最多50的keyset cursor，可继续加载；详情显示真实duration、distance和已发布pace/HR/load。没有route point read API，明确显示路线不可用，不将sealed状态当作地图。

Analysis展示当前clean publication和最多90日已发布历史；默认14日。缺失不补零、不用连线跨越缺失点，Swift Charts显示已有点。无Metal模拟器用原生Shape绘制同一批已发布值的柱图，仅做显示比例变换，缺失仍为空白；不据此声称Swift Charts的GPU视觉通过。详情解释load-only和未纳入的sleep/HRV/resting-HR trend。

## Training与Override

只展示Effective Plan。顶部为服务器目标/周数/版本及后台generation状态；日期横向selector，课程保留session identity。无计划时由用户明确输入目标、开始日、周数、每周训练日、长跑日和当前基础，POST现有P5创建接口，不在App生成课程。

原生Sheet支持字段级edit（type/durationS/distanceM/effort/targetHR/paceSPerKM/notes）、move、skip、add。选一个字段只发一个override，不锁整课。可选HR/pace允许显式清空。REST转换沿用P5限制，不暗中清除其他字段；用户可skip/add。调整历史读取Effective audit，active事件提供revoke。

提交前先比较观察到的Effective UUID；版本已变则保留草稿并要求查看最新计划。服务端409同样保留原命令/草稿，显式review后才生成新command/override UUID。网络/未知结果保留不可变原请求，重试原commandID；不修改pending命令内容。422确定未接受时解除被拒命令并保留Sheet草稿供修正。429遵守Retry-After。

服务器返回receipt先验证identity/类型，再原子清理journal。原ACK重放如果落后于已显示Effective version，不倒退当前列表。新Base与override的合并完全由P5执行。

## Execution与Feedback

start明确传planID/effectiveVersionID/sessionID，展示服务器frozen snapshot；后续计划更新不改处方。执行计时来自server startedAt，不提供GPS/实时HR测量或HealthKit写入。有结构化steps时按snapshot展示顺序、时长、RPE、repeat/recovery；手动切换当前/下一步骤，本地按executionID保存游标，不根据未经确认的传感器推断进度。

complete可先于Workout。没有Workout显示“等待运动记录”，用户明确选择列表候选后发associate；不自动唯一匹配。计划/实际/delta/ratio/load来自P5 Feedback，只格式化单位，不重算完成度。反馈刷新仍走同一后端队列。执行ID在设备产品journal保留，重新打开后可查询；当前没有跨设备执行列表API。

## Settings与P3诊断

“我的 → 同步与诊断”复用原P3.5 Form、HealthKit只读授权请求、last sync/pending/ACK/error/retry和device-only Keychain。Release保持HTTPS；Debug仅显式host例外。仅Debug+Simulator、显式acceptance环境变量可读取私有临时E2E配置，仍连接真实API，无Mock runtime注入。

WorkoutKit/Watch系统行为在既有IMPLEMENTATION/基线为deferred，本轮不开放排程或独立Watch产品。后续adapter只能消费Effective，不从Base生成第二套计划；独立授权和真机排程仍需验收。

## Accessibility、交互与性能

按钮约0.98按压缩放、短easeInOut；不持续bounce。关键命令ACK使用light haptic，日期选择selection。Reduce Motion关闭zoom/按压缩放/柱图入场动画；没有parallax。Reduce Transparency增加surface不透明度；Dynamic Type支持问候区、身体指标、周统计和反馈的纵向reflow，图标/图表标签区域随字号缩放。课程编辑/改期/跳过有至少44pt触控区，Accessibility字号时纵向排列。日期可横向滚动，Form保持原生可访问性。

背景无持续动画、shader、粒子或持续自定义blur。图片只在单一缓存入口加载；未放4K素材。OSLog只记耗时、计数、字节与稳定错误码，不记健康值、路线或token。network/response_decode、Home加载、state publication与command ACK分别可观测；UI测试记录截图/操作/内存。模拟器耗时不推导iPhone12ProMax帧率、真机内存或生产SLO。

## P6 READ API GAP

新增最小只读endpoint：

- `GET /v2/workouts[/{id}]`：耐久raw summary与clean published metrics；不返回raw payload/series/GPS；游标按(event_start,UUID)降序，非同步下载水位。
- `GET /v2/product/overview`：dataset IANA时区的两周日统计；running activity37；已同步距离、Workout数和published load。任一必要输入缺失则该日对应sum=null；未来日期null。
- `GET /v2/analysis/history?days=14`：1–90日clean published state，不启动回算。

均复用Bearer、ReadLane、查询超时和dataset隔离；不新增migration或改变P3 ingestion合同。route read、跨设备execution列表、自动historical recompute不在本次补齐范围。

## 2026-10-29 Training read correctness addendum

ProductCore 新增 TrainingAuditReader、TrainingHistoryReader、TrainingVersionPage、TrainingDetailPage/Section；分别读取固定版本 `/audit`、摘要 `/history/versions` 和 `/detail` section。每次仅一页，保留 next、已加载行与不可变 version binding；请求/解码失败不推进游标，空页完成与失败分开，末页不重复请求。服务端/Swift 新页预算均256 KiB，其他现有响应仍1 MiB，没有全局扩容。fitness_fatigue/v2 的已发布数值可呈现，未知版本仍不可用。

课程详情沿用原 Liquid Glass/Material 布局，只增加调整历史的加载状态、加载更多/重试和空态。版本改变时换用新 reader，旧在途结果不得混入；后台刷新不清草稿。rebase 冲突显示字段及原因，并明确原值仍保留、当前使用合法自动处方，可修改或撤销。没有新增主页面、Swift 合并算法或本地训练事实。
