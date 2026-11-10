# P0 旧系统语义盘点：第一轮源码基线

日期：2026-10-27（Asia/Shanghai）。这是静态源码盘点和后续验收工作表，**不是完整兼容矩阵、已部署功能清单或算法等价证明**。旧仓库 HEAD 为 `dea7b35452916a1da42483f83e3bd44889be1ada`，但工作区存在未提交修改；本轮读取实际工作区源码。机器清单记录每个扫描文件的 SHA-256，而不是把 HEAD 当作完整工作区版本。未读取旧数据库、运行旧服务或修改旧目录。

[机器清单](evidence/p0-legacy-source-inventory-2026-10-27.json) 扫描 231 个源码文件，提取 92 个 `/v1/` 字面量路由调用、126 处 SQL `CREATE TABLE/VIEW` 文本（115 个不同名称）和 10 个领域包的 141 个顶层导出函数／类。每条候选带路径和行号；这些数量是扫描结果，不是服务当前注册路由、数据库现存表或生产启用算法的数量。建表列表包括历史迁移、视图和临时表；路由提取不覆盖动态注册；算法提取不覆盖变量导出、重导出、应用层与 API 策略。旧源码将来变化时应重新盘点，不把这份静态快照当实时状态。

训练计划接口已进一步按方法核对，见 [训练计划接口工作表](P0-TRAINING-PLAN-INTERFACES.md)：包含注册、协商、客户端源码、退役状态和待实测差异；其余接口族仍需逐项核对。

## 产品与接口去向

下表是按现有设计提出的工作方向；每族的字段、错误码、调用方和验收仍须逐方法核对。除新工程同步合同中明确声明的能力外，下列功能均未实现或未验收。

| 旧功能／接口族 | 源码锚点 | 新工程方向与未决事项 |
|---|---|---|
| 健康采集、导入任务、覆盖探测 | `apps/api/src/routes/import-routes.ts`、`direct-sync-ingest.ts`、`GET /v1/sync/coverage`；iOS `HealthKitSync.swift` | P2/P3 使用新 dataset 和有界 outbox；现有样本、Workout、series 只是载荷子集。记录类型、查询谓词、删除、后台恢复及覆盖语义逐项核对；不得把旧覆盖结果或 anchor 当新数据集交付证明。旧 ZIP 导入与来源管理是否保留为用户功能尚未决定，不能假定 v2 上传等价于旧功能。 |
| Workout 列表、详情、series、路线、分享和导出 | `server.ts` 的 `/v1/workouts*`、`/v1/exports/workout-summary`；`packages/storage-sqlite/src/read/` | P4 先补路线与受版本约束的下发，P5 再提供派生展示；列表过滤、分页、详情字段、坐标隐私、缺失状态、分享及导出合同须单独对照。目前没有这些业务读接口。 |
| 仪表盘、趋势、恢复、训练统计、区间 | `/v1/dashboard`、`/v1/trends`、`/v1/recovery-trends`、`/v1/training-statistics/*`、`/v1/training-zones/*` | P5 对照本地日期、窗口、空值、单位、算法版本和结果新鲜度；profile 的人工修改需要明确保存与权限。当前没有分析结果或相应读写接口。 |
| 训练计划、课程和执行 | `/v1/mobile/training-plan-state`、`/v1/mobile/training-plan/commands*`、`/session-commands*`、`/execution-commands`；`training-plan-*.ts` | P5 先定义命令幂等、预期 revision、人工保护、预览、执行关联与回执原子性；P3 原生端再按能力协商，WorkoutKit 使用独立授权。旧人工计划和回执不迁入新数据集，不能通过 HealthKit 历史恢复。 |
| Coach 闭环及反馈 | `coach-loop-routes.ts`、`coach-loop-*.ts`、`coach-*.ts` | P5 对照计划所有权、反馈、同步回执、失败及可用性；不能将能力位存在视为生产启用，也不能将同步事实成功当成分析或计划完成。 |
| 预测、模型、成绩与标注 | `/v1/predictions`、`/v1/prediction-models*`、`/v1/model-runs*`、`/v1/performance-entries*`、`/v1/label-funnel` | P5 按固定合成事实逐模块对照，核对训练与激活门禁、模型版本、可得时间、回退和原因码；运行时推理及依赖须另行验收。旧模型、手工成绩及激活状态不自动迁入。 |
| 健康、诊断、任务和管理 | `/v1/health`、`/v1/handshake`、`/v1/jobs*`、`/v1/commands*`、`/v1/diagnostics`、`/v1/runtime-status` | 新服务已有独立 health 和部分握手，不等于兼容旧合同。逐项决定诊断与任务可见性、权限和错误；管理工具不能由在线应用导入。 |
| 隐私区和手工覆盖 | `/v1/privacy-zones`、`/v1/workouts/:id/override` | P4/P5 分别设计隐私与人工事实；旧手工设置不会随 HealthKit 重建。不得把无设置解释为用户同意公开路线。 |
| 备份、恢复、删除 | `/v1/backups*`、`DELETE /v1/database` | 旧备份创建与删除分别返回 `410 backup_creation_retired`、`410 backup_deletion_retired`；恢复／下载和数据库删除需逐项核对授权及维护模式。PG 备份恢复是独立运维工作，不将旧在线换库映射成新在线同步；未来若提供 `/v1` 兼容，保持既有退役码，不默默开放。 |

旧训练计划退役表以 **方法 + 路径** 为身份，见 `packages/api-contracts/src/retired-surfaces.ts`：`GET/PUT /v1/mobile/goals`、`GET/POST /v1/mobile/run-coach`、`POST /v1/mobile/workout-plan/{preview,confirm,generate}` 均为 `410 legacy_surface_retired`。`GET /v1/mobile/workout-plan` 和 `POST /v1/mobile/workout-plan/synced` 不在该退役表中。新工程目前没有 `/v1` 兼容实现，不能宣称它已经返回这些 410。旧握手的 `trainingPlanCommandContractVersion` 等受数据域、开关和客户端协商约束，见 `packages/api-contracts/src/handshake-registry.ts`；仓库中存在代码或声明不证明旧生产环境启用。

## 事实与状态的处置

[机器清单](evidence/p0-legacy-source-inventory-2026-10-27.json) 中的每个建表候选可按 `name`、`file`、`line` 复核。ADR-002 已取消旧库数据迁移，下面的“方向”是新系统功能设计，不是逐表拷贝计划。

| 旧状态族及代表名称 | 方向 | 剩余核对 |
|---|---|---|
| `health_records`、`workouts`、`workout_statistics`、`workout_events`、`healthkit_record_facts`、`healthkit_record_series_items` | 从手机当前可读 HealthKit 历史首次采集，再 anchor 增量；schema 9 已支持明确列出的样本／Workout／series 子集 | 全部类型、单位、来源、删除、活动细项和覆盖边界；手机不可见数据不会恢复。 |
| `workout_routes`、`route_points`、`route_versions`、`healthkit_route_sources`、`workout_sample_associations` | P4 按路线分块与完整发布重新设计；关联数据须明确支持后才能声称完整 | 坐标顺序、来源、删除、隐私、部分结果和采集权限；当前不支持路线及显式样本关联。 |
| `import_batches`、`import_entity_tombstones`、`import_generations`、`import_publications`、`native_packages` | 新流、不可变批次、删除屏障与回执使用新身份；旧身份和旧回执不接续 | 逐项对照冲突、删除优先与恢复语义；P2 受控替代链仍未实现。 |
| `feature_runs`、`workout_features`、`daily_training_metrics`、`ability_assessments`、`active_*`、`model_*`、`prediction_*` | P5 生成带输入谱系的不可变结果并有条件发布；不复制旧结果／模型 | 每个依赖、版本、空值、时间和变更传播待固定事实对照；Worker 当前不运行这些算法。 |
| `mobile_training_state*`、`mobile_training_execution*`、`mobile_plan_operations`、`workout_overrides`、`privacy_zones`、`performance_entries` | P5/P4 重新提供明确的用户状态与权限 | 不会由健康事实重建；启动体验和需要用户重新设置的项目须产品确认。 |
| `runtime_meta`、`recompute_jobs`、`restore_intent`、`dataset_epoch`、`mutation_commands` | 新系统独立任务、epoch 和维护面 | 不能假定名字相同即语义相同；恢复 fencing、重试及权限需独立验证。 |

静态建表匹配不能证明最终 schema：需要离线逐个追踪 migration 的创建、重命名和删除、数据库归属及引用，并补足动态 SQL；本轮没有连接旧库确认现状。

## 算法对照工作包

| 顺序 | 源码锚点 | 固定合成事实对照要点 |
|---|---|---|
| 1 | `packages/normalization`；`health-export-parser` canonicalize | 单位、日期／时区、精度、重复、无效和缺失；环境修正的适用范围及 trace。 |
| 2 | `training-load`、`training-zones`、`physiology` | 负荷、心率／配速区间、日指标、参考窗口、缺失证据、版本和边界；增量更新与删除影响。 |
| 3 | `features`、`dynamics` | 单次训练和 series 的顺序、采样覆盖率、可信度、缺失／离群及版本；保留来源顺序。 |
| 4 | `scoring`、`readiness` | CA/TA、置信度、递推及 inactivity、缺失值、reason code；检查点重放不能任意提前收敛。 |
| 5 | `prediction-inference`、`model-eval`、`application/src/prediction-*` | 模型版本、可得时间、门禁、校准、回退和不可用原因；Go／Linux amd64 运行库单独验证。 |
| 6 | `apps/api/src/training-plan-*.ts`、`coach-*.ts` | 命令／保护／执行和发布过程、失败/幂等与算法联动；人工锁定不得被自动刷新覆盖。 |

本轮没有生成或运行旧 TS 与 Go 对照样本，也没有断言所有导出函数均为当前生产算法。各模块在实施前须固定旧代码摘要、合成输入和期望输出，并覆盖缺失、边界、修订、删除、版本、可得时间及错误原因；有意改变业务规则须另立 ADR。机器清单不含 `application`、API 中所有策略入口，需继续补充调用图和实际可达性。

## 现场资源与恢复参数

已有一次性 fnOS 测试的 CPU、内存、Docker、Go、PG 版本和数据库设置见 [实施记录](IMPLEMENTATION.md#fnos-远程测试入口2026-10-27)。它不是生产资源配置或性能验收。仍须由现场核实：生产卷路径和文件系统、UID/GID、可用空间及配额、网络／公网证书、PG/WAL/spool/备份独立位置与容量、CPU/内存/IO 限额、异地加密备份位置、保留策略、实际 RPO/RTO 与恢复演练。原方案中的资源预算和 RPO/RTO 是目标，不是实测结果。不得读取旧生产健康数据库或远端凭证来填表。

## P0 尚未退出

下一轮应将 92 个路由候选与 OpenAPI、Swift 出站调用及动态注册交叉比对，逐个记录方法、状态码、使用方和兼容决策；梳理所有表的最终迁移演化和业务状态归属；建立应用层／API 算法调用图并准备合成对照 fixture；补齐上述现场参数。产品仍需决定旧 ZIP 导入、分享／导出、模型操作等未明示功能的去留，以及旧服务端专属状态如何重新录入。当前清单为可追踪的起点，不能替代这些出口条件。
