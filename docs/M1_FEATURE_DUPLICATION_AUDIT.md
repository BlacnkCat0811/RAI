# M1 FEATURE DUPLICATION AUDIT

2026-11-03；基线 `bcc6ca85fa7eb87e0bc455e564c9d13439cd8e61`，已有工作区改动见 `evidence/m1-baseline.json`。在算法迁移前完成审计，不删除旧版本。下表根据完整函数复核，区分相似用途与完全相同代码。

| 范围 | analysis/workout.go | workoutdetail/calc.go | 处理 |
|---|---|---|---|
| 数值校验 | Finite 排除 NaN/Inf | finite 同公式 | 完全重复，迁入 workoutdomain，保留兼容包装 |
| 球面距离 | haversine，R=6371008.8m | 没有 haversine；route fallback 使用 SpeedMPS | 不是两套完全重复算法；旧 analysis 与新 feature 共用几何原语 |
| 速度/配速 | totalDistance/source duration；s/m | RunningSpeed/route.SpeedMPS；覆盖和 scale 门槛后缩放到 total；s/km | 语义不同，旧版保持兼容，新版原生积分，不读展示输出 |
| HR | 区间加权，重叠即 ambiguous；瞬时点均值但 coverage=0 | WeightedAverage 瞬时点给最多1秒展示权重、重叠可重复；HR zones 另有一致读数去重 | 不是同一算法；v2 与详情合同保留，新版明确 observed/reconstructed 支持 |
| cadence | RunningCadence 名称直接观测 | StepCount/分钟 proxy；全场和窗口内存在相同换算 | 抽取纯 cadence proxy 换算，新版与详情共用；不得称 native cadence |
| route/elevation | 30s/25m，逐点高差累计 | route speed 建轨迹；端点 grade、Minetti 展示调整 | 语义不同，新版15s/20m/10m及完整100m中位邻域，不替换旧数值 |
| segments | 没有持续窗口引擎 | activities/lap/segment、计划标签与目标、展示匹配 | 计划标签是 presentation；新版只消费真实 elapsed 时间支持 |
| environment | 未载入 | metadata→展示气象/海拔惩罚 | 可共享 context facts；展示惩罚不进入新特征和评分 |
| quality/source | coverage/参数门槛 | 品牌 confidence、展示 quality score | 不是生理可信度；新版使用 source epoch 和逐模型 reason codes |

共享 pure domain 首次迁移抽取相同 finite 校验、StepCount÷分钟 proxy 换算；球面距离由旧 analysis 与新 feature 共用。`workoutfeatures` 拥有 canonicalization、pause、source、signal、route、window 原语，后续模型必须读其 artifact，禁止解析详情 DTO。旧 feature/TRIMP/state、详情/API/iOS 仍按旧合同存在，其完整迁移需另立版本和消费者验收，M1 不偷换旧语义。
