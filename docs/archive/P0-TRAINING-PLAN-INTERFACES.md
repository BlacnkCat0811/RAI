# P0 训练计划接口逐方法工作表

日期：2026-10-27（Asia/Shanghai）。本表对照旧工作区的路由注册、OpenAPI 源码、合同定义、处理器和 Swift APIClient；源码摘要见 [证据](evidence/p0-training-plan-sources-2026-10-27.json)。旧工作区有未提交修改，因此这些结论仅适用于所读文件快照；不代表部署配置、实际调用量或运行时验收。这里的“保留语义”是 P5 的实施要求，不意味着新服务已有旧 `/v1` 接口或保证新端使用相同 URL。

## 当前源码中仍注册的接口

| 方法和路径 | 旧源码所示行为与调用方 | 新工程去向／未验证事项 |
|---|---|---|
| GET `/v1/mobile/training-plan-state` | 无扩展时旧读合同；`X-RAI-Training-Contract: 2` 请求统一状态。Swift 有明确 v2 请求；不存在 v2 错误后悄悄回退的分支。扩展 `X-RAI-Training-Sessions: 1`、`X-RAI-Training-Updates: 1` 受状态与版本约束。 | P5 定义新读合同和一致性快照；未实现。状态、版本、新鲜度、课程及执行视图需合成对照。 |
| GET `/v1/mobile/plan-operations/:id` | Swift 在持久发布的不确定结果恢复时查询。合同列出 `committed`、`notFound`、`scopeMismatch`、`expired` 四种状态；`notFound` 与 HTTP 路由 404 不能混同。 | P5 需要持久操作查询和明确的重放／过期行为；未实现，保留查询结果与网络错误的区别。 |
| POST `/v1/mobile/training-plan/commands/preview` | 合同 2，动作 `set_race_goal`、`clear_race_goal`、`pause`、`resume`；预览不写库，候选绑定数据域、内容、状态 revision、策略和期限，源码期限 120 秒。Swift 带 `X-RAI-Client-Training-Commands: 2`。 | P5 预览不可发布；固定合成输入核对拒绝、候选过期、状态变化及时间边界。 |
| POST `/v1/mobile/training-plan/commands` | 提交须提供 operationId、scope、expectedStateRevision、candidateId 和动作；已提交操作先尝试重放，才执行新写入门禁。状态、历史、旧目标镜像及操作结果在控制库发布事务内写入。 | P5 在 PostgreSQL 中重新设计并验证原子性、重复与身份冲突；旧控制库实现不是新系统事务证据。人工保护及与事实变更竞争须验证。 |
| POST `/v1/mobile/training-plan/commands/refresh` | 当前状态 revision 和持久 operationId；事务外生成候选、事务内重新检查 revision／输入指纹后发布。暂停状态拒绝刷新；已提交操作可重放。 | P5 以输入谱系与 CAS 防过期发布；需验证竞态、删除传播和故障回滚。 |
| POST `/v1/mobile/training-plan/session-commands/preview` | 合同 1 的 move/edit/add/cancel/set_rest/unlock；不写入，候选期限 120 秒，绑定数据域、计划实例、状态及相关课程 revision。Swift 显式协商 session command 1。 | P5 保留预览、用户保护及课程身份；需建立固定输入与边界对照。 |
| POST `/v1/mobile/training-plan/session-commands` | 请求含原草稿、candidateId、持久 operationId；源码先查已提交重放，再检查新写能力。课程、投影、冻结历史和操作结果按发布事务处理。 | P5 需验证同身份同内容重放、冲突、过期候选、并发更新与原子性。 |
| GET `/v1/mobile/training-plan/sessions/:sessionId/execution-candidates` | 只读候选，不自动确认；默认课程日期前后各 2 天，可扩大范围上限 31 天，每页 50；Swift 传 planInstanceId、可选日期和 cursor，并协商执行合同。 | P5 需要明确候选范围、分页与来源事实版本；不能把候选当已确认执行。 |
| POST `/v1/mobile/training-plan/execution-commands` | confirm/correct/retract/skip，携带范围、状态／课程／执行 revision 和来源事实摘要；先查已提交重放再检查写能力。Swift 对明确 `training_session_not_found` 与缺失路由 404 分类不同。 | P5 保留显式确认、CAS、来源事实变化时拒绝及可恢复操作结果；需真实数据库竞态测试。 |
| GET `/v1/mobile/workout-plan` | 当前注册为稳定读取的可执行周计划；Swift 有调用。在 coach 管理状态下，全局 preHandler 可先返回 `409 coach_loop_contract_required`。 | P5 重新提供可执行视图；未实现。不能仅依据注册或 OpenAPI 说明假定任意数据域都可读取。 |
| POST `/v1/mobile/workout-plan/synced` | Swift 将本机系统排程核验后的回执提交；合同 2 验证已发布处方／摘要，并在同一控制事务计算 `matchesCurrent` 和写回执。源码明确系统读回不代表配对 Watch 已接收。 | P5 与原生 WorkoutKit 独立授权及设备回执设计衔接；需要设备核验、失效／撤回、并发发布及真实设备证据。旧 OpenAPI 摘要称“推送到 Apple Watch”，比处理器和 Swift 注释所能证明的范围更宽；验收以实际证据定义为准。 |

## 精确退役方法

旧共享表与路由注册均列出以下 7 项，处理器返回 `410 legacy_surface_retired`、消息和 replacement；全局恢复门禁对这些精确方法放行至退役处理器。此处仍可能受更早的安全／鉴权层影响，不能推断任意请求必得 410。新服务没有 `/v1` 兼容，尚不返回该响应；若将来决定提供兼容，应按方法和路径保留退役语义。

| 方法 | 精确路径 | 旧表中的替代面 |
|---|---|---|
| GET | `/v1/mobile/goals` | `/v1/mobile/training-plan-state`、`/v1/mobile/training-plan/commands` |
| PUT | `/v1/mobile/goals` | 同上 |
| GET | `/v1/mobile/run-coach` | 同上 |
| POST | `/v1/mobile/run-coach` | 同上 |
| POST | `/v1/mobile/workout-plan/preview` | `/v1/mobile/training-plan/commands/preview` |
| POST | `/v1/mobile/workout-plan/confirm` | `/v1/mobile/training-plan/commands` |
| POST | `/v1/mobile/workout-plan/generate` | `/v1/mobile/training-plan/commands/refresh` |

前缀匹配会错误覆盖仍注册的 GET `/v1/mobile/workout-plan` 和 POST `/v1/mobile/workout-plan/synced`。Swift 源码仍保留部分旧路径方法／持久操作重放分支；其存在不是当前 UI 必然调用或生产流量证据，也不能将测试文件中的旧模拟响应当服务器现状。

## 协商与错误边界

旧握手为命令能力按服务器开关、存储就绪及精确客户端头协商；课程写与执行写分别有能力位，读能力不自动授予写权限。握手响应设置 `no-store` 和相关 `Vary`。统一读请求不支持版本时返回 `409 training_contract_unsupported`；状态缺失 `409 training_plan_not_migrated`；读失败 `503 training_plan_state_unavailable`。课程／执行合同还声明 revision 冲突、候选过期、源事实变化、课程不存在和存储不可用等具体错误，见证据所列共享合同。以上是选定处理器路径，不是完整状态码清单：安全层、恢复门禁和其他验证可先返回不同结果。

持久操作查询须区分业务状态与 HTTP 错误。旧共享合同和 OpenAPI 的说明称过期返回 HTTP 410，但当前 `publicationResult()` 对查询返回 `{status: "expired"}`，GET 路由直接返回该结果，没有显式设置 410；重放过期操作的代码则抛出 `410 plan_operation_expired`。这是静态源码中的疑点，需通过隔离运行时测试核对实际 HTTP 响应，不能假定文档和实现一致。旧共享合同写明结果保留 90 天；新实现需要独立定义保留期、身份／负载冲突及恢复策略，再用故障注入验证，不能把查询失败一律当未提交。原文中的旧库迁移提示只描述旧程序的行为；本工程遵循 ADR-002，不迁移旧数据。

## 后续验收与未决项

本工作表只覆盖训练计划的 18 个方法和所列源码，非全站路由盘点。P5 应为配置、课程、执行、查询和回执分别固定合成请求／响应、状态修订、负载冲突、能力关闭后的已提交重放、发布中断和并发结果；再对新实现运行真实隔离 PostgreSQL 故障注入。还需核对完整错误映射、旧 iOS 实际调用图、操作保留期以及计划重建与手工状态重新录入的产品体验。未运行旧服务或真机，也未读取旧数据库、配置或凭证。
