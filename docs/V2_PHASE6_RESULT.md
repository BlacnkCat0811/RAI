# RAI V2 Phase 6 Result

## Status

**PASS WITH RISKS。** P6开发与可执行门禁完成：原生产品、真实后端闭环、两尺寸默认UI/大字号审计及截图回归通过。原生Liquid Glass的GPU视觉、真实iOS18运行与真机行为仍未验证，不是生产验收PASS。

## Repository

`/Users/ta/rai`，`main`。没有commit、push、部署、P7实施或修改旧`../running`。

## Base Commit

`P6_BASE_COMMIT=62511f9b3e5998ccad79396a7e14540074e506e5`；初轮入口工作区干净，P4/P5已包含在基线。空间恢复后继续的基线为`3ffed27aabfdbc9af962e59da297e7e2eea77c4d`，入口同样干净（用户已保存前轮实现）。原始架构方案、00001–00013和历史evidence未改。

## Visual Reference

用户提供的Liquid Glass首页图为Visual Source of Truth。实现Greeting→Hero→Body Status→Weekly→Recent→系统Tab。Hero采用独立生成的日落城市/滨水跑步摄影，文本、指标、按钮和卡片由原生绘制，不使用截图切片或假路线。人物/城市不同；素材来源与完整提示词见[记录](evidence/p6-hero-provenance.md)。宿主没有Metal设备，不能据此声明原生Liquid Glass GPU视觉通过。

## Design System

`RAITheme`/`RAIGlassSurface`统一暖暗背景、橙/琥珀/白色层级、28pt圆角、系统字体和monospacedDigit。iOS26原生glassEffect已编译；iOS18–25 Material fallback保留最低版本，原生Tab/Sheet。无Metal模拟器使用Material和同值Shape柱图回退；系统Tab仍保留原生实现。详情见[产品合同](P6_IOS_PRODUCT_CONTRACT.md)。

## Home

真实P4Published、dataset当天P5Effective、两周跑步统计、recent Workout和P3同步状态。Missing显示`--`；缺时长不猜45min。Recovery/Readiness明确load-only训练状态估算。Swift与原生模拟器均已读取真实API；Keychain保存使用本地ad hoc签名，不绕过Keychain。

## Activities

Workout分页/详情展示duration、distance与published pace/HR/load；无route read API明确不可用。新增最小只读Workout/overview/history，页≤50，history≤90日。真实PG验证分页、missing、删除隐藏、dataset隔离与HTTP参数。

## Analysis

当前指标及Swift Charts历史点，缺失不补零或跨空值连线。只读clean publication，不启动全历史重算。

## Training

唯一视图为Effective；目标创建、日期selector、Session Detail与edit/move/skip/add/revoke Sheet/audit。Swift不生成或合并计划；后台更新不清空草稿。验收发现撤销后详情会返回首页，已将Home/Training导航移到独立selection，避免课程卡片刷新或改期移出列表销毁详情。成功generation不再把`SUCCEEDED`哨兵显示成错误。

## Override

真实Swift→fnOS→隔离PG：Monday用户7km在新Base后仍7km；Sunday移Saturday后日期保留，未修改距离跟随新Base。规范5→7→Base4仍7、Sunday12→10且改Saturday的Go merge fixture在完整回归中通过；Swift只验证最终投影展示，不另做合并。

实际409保留原草稿，GET最新后显式review再提交新命令。实际COMMIT响应不交给journal，重开journal后同commandID重试取得逐值相同receipt；这是合成应用边界响应抑制，不是iPhone网络故障。未知/错误回执保留原请求；明确业务422才允许修正。

## Execution

实际start绑定Effective/session；新Base发布后snapshot完全不变。UI展示服务器处方、startedAt计时与原始steps；手动切换步骤，不建GPS引擎或HealthKit写入。

## Feedback

实际complete先于Workout关联；用户明确associate后取得P5 planned/actual/delta/ratio/load。UI不重算Feedback，不自动唯一匹配。执行ID本地保存，跨设备执行列表尚缺。

## Sync

P3.5 outbox、anchor、ACK、后台URLSession与Keychain保持；诊断移至“我的”。RAIAPIClient仅增加默认空query和decode耗时。全部原27项Swift同步测试通过；没有读取真实HealthKit/生产数据库。

## WorkoutKit

既有主合同/基线将系统排程与Watch行为deferred，本轮未开放WorkoutKit或独立Watch。未来adapter只能消费Effective并独立授权；没有真机排程证据。

## Accessibility

两种模拟器尺寸均验证系统Light外观、应用Dark主题、Accessibility XXXL字号、Reduce Motion和Reduce Transparency；测试读取真实SwiftUI环境并断言，未注入假的环境值。首页、课程详情/操作、周统计、训练、运动、分析趋势、执行及反馈的textClipped审计通过；不等于全量VoiceOver/对比度认证。

人工截图发现的图标重叠已用ScaledMetric修正；问候区、周统计和反馈大字号纵向重排，反馈单元格保留完整文字高度。滚动测试断言Hero实际坐标改变，周统计定位后才审计。大屏宿主照片分析进程曾占用约270% CPU，造成XCTest -56超时；最终仅暂停本轮自建模拟器的该进程完成审计，EXIT trap恢复，随后关闭测试模拟器。性能测量未使用此暂停环境。真实裁切失败与超时记录保留，未忽略裁切问题；只对无结果的-56基础设施超时重试一次。iOS18 runtime未安装，回退分支运行不冒充iOS18运行。

## Performance

**SIMULATOR ONLY / synthetic harness。** 最后一组macOS Swift→SSH→fnOS各3次独立读取：Home五API总计中位294.690ms（268.517–352.721）；Training 77.180ms、Analysis 33.659ms、Activities 84.063ms、Weekly 30.326ms、History 79.474ms。一次闭环505.276ms、初轮348.313ms及初次性能样本中的18.640s异常值保留，不据小样本声明SLO。解码样本、原生launch/操作、状态发布/命令ACK及CPU调用图见[evidence目录](evidence/p6-acceptance-2026-10-28)。

有实际滚动位置断言的3次往返：大屏XCTest时钟平均10.180s、进程物理内存平均66,605kB、peak平均69,523kB；12ProMax class平均11.731s、67,831kB、peak平均70,950kB。手势有指定速度且包含大量XCTest查询开销，不能解释为App帧耗时。10秒Apple sample记录71.6M footprint/88.7M peak，调用图包含大量自动化查询，不作纯App CPU评价。

Time Profiler未生成可读取trace，导出返回Document Missing Template Error，已停止该次工具进程并保留失败记录。没有Metal设备，系统Tab的合成存在异常，部分滚动截图的合成位置也不可靠；截图仅为受限回退环境的布局回归基线。GPU渲染、blur hitch、Swift Charts原生绘制和真实帧率均NOT VERIFIED。真机性能和生产SLO未建立。

## Tests

- fnOS普通/race单元、vet、build、模块校验：PASS。
- 完整隔离PG race：PASS，恢复空间后最终integration302.488秒（初轮311.846秒保留），含P3/P4/P5与P6只读查询。
- 249包依赖边界、analysis/training无I/O：PASS。
- Compose P3重启/离线ACK、P4 Worker/publication、P5 HTTP闭环与SIGTERM：PASS。
- OpenAPI Redocly2.71.1：PASS，8条既有warning。
- Swift核心35项通过（原SyncCore27项与ProductCore8项）。
- 最终Debug Simulator/UI test build与Swift6 strict concurrency通过。
- 最终Release arm64 generic iOS 18 minimum target构建通过；这是未签名编译，未安装真机。
- 两尺寸分别8项原生Normalizer单元测试及3项UI测试通过；两尺寸各一次独立五页面截图比对通过，错换首页的negative control被拒绝。
- 两尺寸完整大字号/系统开关/textClipped审计通过；大屏最终功能审计的宿主后台分析隔离条件有单独记录。
- 历史失败保留：磁盘Code28、未签名Simulator Keychain失败、详情导航返回与Skip小触控区导致的失败均未计PASS。
- DEVICE TEST: NOT RUN。

## Deviations

人物/城市摄影与参考图不同；原生GPU玻璃视觉与完整滚动合成未验收。route read、跨设备执行列表和WorkoutKit未开放。Swift命令行闭环、原生UI闭环、系统审计、编译和真机证据分别记录，不互相替代。

## Risks

磁盘阻塞已解除。剩余风险是无Metal宿主的GPU/系统控件合成限制、iOS18运行、iPhone12ProMax真实硬件性能、HealthKit/后台/网络恢复及load-only模型个体效度。截图基线不是原生Liquid Glass真机视觉批准；需要有Metal的环境建立独立基线。没有读取生产数据、迁移真实数据或部署。

## P6 Final Status

```text
P6 DEVELOPMENT STATUS:
PASS WITH RISKS
```

## Device Status

```text
P3 REAL DEVICE ACCEPTANCE:
DEFERRED / NOT VERIFIED

P6 REAL DEVICE ACCEPTANCE:
DEFERRED / NOT VERIFIED

DEVICE TEST:
NOT RUN
```

## P7 Entry

```text
P7 DEVELOPMENT READY:
YES — development only; no P7 work started
```

## Production

```text
PRODUCTION RELEASE READY:
NO
```
