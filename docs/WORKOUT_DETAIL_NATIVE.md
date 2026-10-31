# 原生训练详情（iOS + Go 只读投影）

2026-10-30（Asia/Shanghai）。本轮按用户提供的实际 iOS 训练详情页结构，在 `/Users/tp/rai` 实现同等原生详情页；只读参考 `../running/running-main`，未修改旧工程。

## 范围与入口

新页面入口仍为运动列表中的 `WorkoutDetail`（`ios/RAI/App/WorkoutDetailView.swift`），地图使用 **Apple MapKit**（SwiftUI `Map` / `MapPolyline`），不引入高德或自定义瓦片。页面从上到下：

1. 补齐状态横幅
2. GPS 路线（起点、终点、公里标记；最多 500 个展示点）
3. 训练摘要（事实、环境观测、等强配速、爬升、负荷、环境修正；可展开全部指标）
4. 训练效果（有匹配课表和心率区间时显示目标完成度）
5. 八条固定指标时序（配速、心率、步频、步幅、功率、触地时间、垂直振幅、海拔）
6. 心率/配速区间分布
7. 课表分段（手表实测 activities）或公里分段
8. 跑姿动态
9. 质量与模型
10. 人工覆盖（仅当匹配执行快照确实被人工修改或带备注时）

## 三个只读接口

新增并注册于 `apps/server/internal/httpapi/server.go`：

- `GET /v2/workouts/{id}/detail`：事实头、环境修正、摘要、训练负荷、区间、采样路线、人工覆盖。
- `GET /v2/workouts/{id}/series`：八条展示时序、公里分段、课表分段、坡度、跑姿动态。
- `GET /v2/workouts/{id}/freshness`：事实/分析/逐指标到达状态。

三个接口均要求 Bearer token、显式 dataset 范围，拒绝未知 query；无干净分析发布时保留事实和原始展示时序，等强配速、爬升/路线质量、负荷、区间/刺激、环境修正、跑姿动态及可信度保持 `null`（计数为 0 不表示评分），环境温湿度观测仍可显示；环境状态为 `pending`。响应契约见 `contracts/openapi-v2.yaml`，纯计算模型见 `apps/server/internal/workoutdetail/`，数据库读取见 `apps/server/internal/storage/postgres/workout_detail.go`。

## 数据口径

- **路线**：只读 `workout_routes.state='sealed'` + `route_points`，按 route/sequence 排序；展示采样 500 点，保留首尾。注册允许首尾相对 Workout ±5 秒偏差；分析和派生计算仅使用 Workout 窗口内的点，原始点、时间戳与 manifest 不改写。
- **时序**：显式关联的 `workout_samples`；sealed series 替代其父 sample 的 summary。每项最多 600 个展示点，保留首尾与极值；`totalSamples` 为采样前数量。图头平均来自 `WorkoutStatistics` 或时间加权原始样本，不是展示采样点均值。
- **配速**：把 `RunningSpeed` 的 `m/s`、`km/h`、`mph` 换算为 `s/km`，不是独立原始通道。
- **步频**：优先 `RunningCadence`；缺失时把 `StepCount` 按记录时长折算为 `spm`。平均步频没有手表汇总值时用步数总量 ÷ 运动分钟。
- **单位**：步幅 `cm→m`，垂直振幅 `m→cm`，触地时间 `s→ms`；汇总统计和原始时序都做同样换算。
- **平均配速**：`durationSeconds / distanceMeters`；距离优先原始 `totalDistance`，其次最新一致的 append-only enrichment。
- **平均等强配速**：按公里分段坡度用 Minetti (2002) 能耗多项式折算平地等效配速，再按已覆盖分段距离加权。`grade_adjustment.minetti/v1`，只按坡度，不是实测值；坡度超过 ±45% 截断并给出原因码。
- **累计上升**：GPS 海拔相邻边上升，过滤单边 >30 m 的尖峰；有垂直精度时要求 ≤10 m。
- **环境修正**：热湿惩罚 + 爬升惩罚，按 `environment.correction/v1`；温度/湿度/爬升任一缺失则 `correction=null`、`status=unavailable`，不把部分输入当完整修正。等效用时 = `duration/(1+total)`。
- **公里分段**：由官方距离与速度轨迹切分；轨迹需覆盖 ≥80% 且缩放系数在 0.75–1.25，否则返回空分段而不是编造。每段有实测配速、平均心率、平均步频、坡度明细。
- **课表分段**：至少两个连续且有效的 activities 才作为活动分段；单个整场 activity 不代表训练步骤，回退到 HealthKit lap=3 / segment=7 事件，忽略暂停/恢复请求。没有真实内部边界时不生成整场伪分段。活动分段可用匹配执行快照补标签/目标；事件分段保留通用实测标签，并在 iOS 显示。
- **心率区间**：相同值的重叠区间只计一次；不同值重叠的歧义区间不计入区间暴露和覆盖率，并给出原因码。完全歧义时返回不可用，不产生完成度；低于静息心率的有效正值归最低区间。
- **训练效果**：只有存在匹配课表目标与心率覆盖时才计算 `score=qualifyingSeconds/targetSeconds×confidence`；无课表时不编造目标完成度。当前无阈值配速，配速区间明确显示不可用。
- **训练负荷**：优先已发布的 `training_load.trimp/v1`；发布结果缺失负荷时可按执行快照中的计划目标强度或课型估算，标记 `planned_effort_fallback/v1` / `session_type_fallback/v1`，置信度 0.4。快照 Effort 是训练前处方，不能当作实际 RPE；load 与 override 的实际 RPE 保持 `null`。
- **跑姿动态**：前后半程按可比配速（中位配速 ±5%）比较，输出平均、漂移、覆盖率、样本、原因码和异常 flags；`running_dynamics/v1`，明确是手表估计值。
- **质量分**：心率/步频/功率覆盖与路线质量的均值 × 来源可信度，0–1，是特征质量分，不是训练表现分。分析未发布时为空。
- **人工覆盖**：当前数据模型没有独立的 workout override 表；只把确实被人工修改或带备注的匹配执行快照映射为覆盖面板，字段不虚构。

## 验证证据

本轮实际执行：

- Go：`go test -race ./...`、`go vet ./...`、`go vet -tags=integration ./...`、249 包依赖边界检查通过；
- 真实隔离 PostgreSQL 18.6：完整 `go test -count=1 -tags=integration ./...` 通过（integration 79.700s），含 `TestWorkoutDetailFactsPendingAndFreshness`、`TestWorkoutDetailSeriesRouteAssociationAndSplits` 与针对两项的 race 复验（真实 migration、事务提交、sealed series/route、association、publication）；
- Swift package：`xcrun swift test` 通过（含新增 `WorkoutDetailTests` 契约解码/空数组测试）；
- iOS Debug/Release 构建：`xcodebuild ... generic/platform=iOS CODE_SIGNING_ALLOWED=NO build` 均成功；模拟器 signed build 成功；`RAITests` 14/14；
- iPhone 12 Pro Max 模拟器（iOS 26.5）：`ProductUITests/test90WorkoutDetailNativeSections` 通过（1 test / 0 failures），截图见 `docs/evidence/workout-detail-2026-10-30/`；
- 本地 API + 合成数据：详情、series、freshness 实际 HTTP 200；地图显示 Apple MapKit 底图、起点/终点与 1–9 公里标记；图表、分段、跑姿动态、质量面板均实际渲染。

## 未验证 / 边界

- 未使用真实 iPhone/HealthKit/GPS 数据；模拟器合成数据不能替代真机、真实路线、真实传感器与生产规模验收。
- 没有 Apple Watch 原始 workout steps 的合成样本；`workout_activities` 路径由合成活动覆盖，旧 `RAIWorkoutStructureDiagV1` 自定义元数据未在新客户端写入。
- 配速区间需要阈值配速配置，本数据集没有，因此页面明确显示不可用；未实现区间档案写入。
- 独立 workout override 表与写入 UI 未实现；只有执行快照覆盖映射。
- 时序/路线是展示采样，不是全量原始导出；极值按展示采样点计算，图头平均来自摘要字段。
- 未运行真实设备、后台/强杀、TLS/生产、长期容量或灾难恢复验收。
