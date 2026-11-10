# RQrun Apple 跑步数据同步：抓包 + 静态分析完整实现

> 本文把两条证据链合并成一份可落地的实现文档：
>
> - **协议事实**：RQrun iOS **3.7.2（Build 4）** 的真实 mitmproxy 抓包（2026-10-31），HAR SHA-256 `cd85acea501d6576eef17b1d79c66cf5b17dbd4179a017292c9023b821815b7a`，IPA SHA-256 `2fbe1ebd08cec2f71c1ba67a3555db93d601f246b4c5f71cdd40eb2eb748893e`
> - **客户端逻辑**：RQrun iOS **3.5.8（bundleVersion 9）** 解密后的 arm64 Mach-O 静态分析，文件 `/Users/tp/rqrun-ipad/dump/RQ_Run_3.5.8_decrypted`，SHA-256 `6bcc8acb42a3fbcfb4e356e17eae5fc4d06d97e8a55204437effce01c701310a`
>
> 结论标记：
>
> - **[实测]**：3.7.2 抓包直接观察到
> - **[静态]**：3.5.8 二进制/Ghidra 直接观察到
> - **[推断]**：由代码/字段关系推导，未动态验证
> - **[建议]**：给自研实现的工程建议，不是 RQrun 原实现

---

## 目录

1. [一页结论](#1-一页结论)
2. [证据边界与版本关系](#2-证据边界与版本关系)
3. [端到端架构](#3-端到端架构)
4. [RQrun 客户端实际实现](#4-rqrun-客户端实际实现)
5. [权限与数据源](#5-权限与数据源)
6. [HealthKit 数据读取](#6-healthkit-数据读取)
7. [上传载荷 originalRecode.json](#7-上传载荷-originalrecodejson)
8. [HTTP 协议](#8-http-协议)
9. [客户端实现蓝图](#9-客户端实现蓝图)
10. [服务端实现](#10-服务端实现)
11. [边界情况与失败处理](#11-边界情况与失败处理)
12. [与 RQrun 的差异和复刻建议](#12-与-rqrun-的差异和复刻建议)
13. [限制与未验证项](#13-限制与未验证项)
14. [附录](#14-附录)

---

## 1. 一页结论

RQrun 的 Apple 跑步数据同步不是“服务端直连 Apple 健康”，而是：

```
Apple Watch 原生体能训练
        │
        ▼
iPhone HealthKit：HKWorkout + HKWorkoutRoute + 各类 HKQuantitySample
        │
        ▼
RQrun：用户在 HealthUploadSelectVC 中勾选记录
        │
        ▼
客户端组装 originalRecode.json（原始明细，不是聚合结果）
        │
        ▼
POST https://api.rq.run/Api/Record/upload_record
multipart/form-data，字段名 file，文件名 originalRecode.json
        │
        ▼
服务端异步解析，返回记录 ID
        │
        ▼
GET /Record/code_apple_source_status
按 apple_health_workout_id 轮询解析状态
```

关键设计：

| 设计点 | RQrun 的做法 | 证据 |
|---|---|---|
| 触发方式 | 用户进入健康同步选择页手动勾选并上传 | `upload_page = "HealthUploadSelectVC"` **[实测]** |
| 上传粒度 | 一条 workout 一个请求 | 单次 40 分钟跑步约 641 KB **[实测]** |
| 幂等键 | `HKWorkout.uuid` → `apple_health_workout_id` | 上传字段与状态接口 **[实测]** |
| 载荷 | 原始 GPS/心率/功率/步频等明细，不是只传汇总 | `map_position_list` 2283 点等 **[实测]** |
| 上传格式 | `multipart/form-data`，字段 `file`，文件名 `originalRecode.json` | 抓包 **[实测]** |
| 解析方式 | 服务端异步解析，客户端轮询状态 | `code_apple_source_status` **[实测]** |
| 单项失败 | 每个指标单独读取、单独记录错误，不阻断整条记录 | `get_data_error`、最多 3 次重试 **[实测]** |
| 去重 | 本地 `DbHealthUploadSuccess` + 服务端已上传列表/状态 | 3.5.8 类名与 3.7.2 状态接口 **[静态/实测]** |
| 自动上传 | 存在 `uploadOn` 通知入口 `autoUploadAppleWorkoutAct:` | 3.5.8 **[静态]** |

一句话：**Apple Watch 记录 → HealthKit 原始数据 → 客户端组装大 JSON → multipart 上传 → 服务端异步解析 → 客户端按 workout UUID 查询结果。**

---

## 2. 证据边界与版本关系

### 2.1 两条证据链

| 项目 | 3.7.2 抓包 | 3.5.8 静态分析 |
|---|---|---|
| 版本 | 3.7.2（Build 4） | 3.5.8（bundleVersion 9） |
| 设备 | iPhone 13,4 / iOS 27.2 | iPad mini 5（iPad11,1）/ iOS 14.6 |
| 产物 | IPA `RQrun.capture.ipa` | 解密 Mach-O `/Users/tp/rqrun-ipad/dump/RQ_Run_3.5.8_decrypted` |
| 方法 | mitmproxy HTTPS 解密 + HAR | bagbak + Frida 解密 + Ghidra |
| 能回答 | 真实 HTTP 请求/响应、字段、大小、时序 | 页面、类、筛选、状态机、上传循环、本地持久化 |
| 不能回答 | 客户端内部方法/筛选逻辑 | 3.5.8 的真实请求体、服务端行为 |

### 2.2 为什么可以合并

3.5.8 二进制字符串表中同时存在：

- `Api/Record/upload_record` **[静态]**
- `originalRecode.json` **[静态]**
- `Record/code_apple_source_status` **[静态]**
- `upload_page` **[静态]**
- `HealthUploadSelectVC` / `HealthUploadVC` / `HealthUploadedVC` **[静态]**

这说明 3.5.8 与 3.7.2 的核心上传链路高度一致：**同一个上传接口、同一个 JSON 文件名、同一个状态查询接口、同一个入口页面**。

### 2.3 必须注意的版本差异

1. **3.5.8 比 3.7.2 旧**：字段集合、筛选条件、自动上传行为可能不同。
2. 3.5.8 还包含 `/Api/Record/add_record`、`/Api/Record/get_apple_source_record_id`、`/Api/Record/add_apple_sleep_record`、`/Api/Sleep/add_sleep_record_v2` 等字符串；这些是**其他记录/睡眠接口**，不能据此认定 Apple 跑步上传走 `add_record`。**[静态]**
3. 3.7.2 的抓包只覆盖一次户外跑步；骑行、游泳、室内跑的字段集合可能不同。**[实测限制]**
4. 3.7.2 的主二进制是 FairPlay 加密的，无法做静态反编译；3.7.2 的字段来源只能靠抓包 + HealthKit API 对照。**[限制]**

> 本文在讲“协议”时以 3.7.2 为准；在讲“客户端流程/筛选/状态机”时以 3.5.8 为准，并明确标注。

---

## 3. 端到端架构

### 3.1 总体数据流

```
┌──────────────────────────────┐
│ Apple Watch                  │
│  原生体能训练 / 第三方 Watch App │
└──────────────┬───────────────┘
               │ HealthKit 同步
               ▼
┌──────────────────────────────┐
│ iPhone HealthKit Store        │
│  HKWorkout                    │
│  HKWorkoutRoute               │
│  HKQuantitySample             │
│  HKWorkoutEvent / Activity     │
└──────────────┬───────────────┘
               │ 读取（需用户授权）
               ▼
┌──────────────────────────────┐
│ RQrun 客户端                  │
│  HealthKitManager             │
│  HealthLoadWorkouts           │
│  HealthKitWorkoutTool         │
│  HealthUploadSelectVC         │
│  Payload Builder              │
│  APIHealthManager             │
└──────────────┬───────────────┘
               │ multipart/form-data
               ▼
┌──────────────────────────────┐
│ api.rq.run                    │
│  /Api/Record/upload_record    │
│  异步解析任务                  │
└──────────────┬───────────────┘
               │ 记录 ID / 状态
               ▼
┌──────────────────────────────┐
│ GET /Record/code_apple_source_status │
│  status / remark / analytic_msg      │
└──────────────┬───────────────┘
               │
               ▼
┌──────────────────────────────┐
│ RQrun UI + 本地 DbHealthUploadSuccess │
│ 已上传 / 失败 / 重传            │
└──────────────────────────────┘
```

### 3.2 客户端职责分层

| 层 | RQrun 3.5.8 类/对象 | 职责 |
|---|---|---|
| 权限 | `HealthKitManager`、`HealthKitTool` | 请求 HealthKit 授权、检查可用性 |
| 数据读取 | `HealthLoadWorkouts`、`HealthKitWorkoutTool`、`HealthKitDownDataTool` | 拉取 workout、路线、样本 |
| 页面 | `HealthSettingVC`、`HealthUploadVC`、`HealthUploadSelectVC`、`HealthUploadedVC` | 设置、容器、待上传、已上传 |
| 列表单元 | `HealthUploadCell`、`HealthUploadedCell`、`HealthAuthStatusCell` | 展示 workout/授权状态 |
| 选择/上传 | `HealthUploadSelectVC` 的 `uploadButtonAct:`、`selectAllAct:` | 勾选、全选、触发上传 |
| 上传编排 | `FUN_10069b320`、`FUN_10069c040`、`FUN_10069c5d0` | 串行上传、PromiseKit |
| API | `APIHealthManager`、`APIHealthRouter` | 组装 API 请求 |
| 持久化 | `DbManagerAuthorizedHealth`、`DbManagerHealthUploadSuccess` | 授权状态、已上传记录 |
| 自动上传 | `AppDelegate.autoUploadAppleWorkoutAct:` | `uploadOn` 通知触发 |

### 3.3 服务端职责

- 接收 multipart 文件；
- 校验用户身份与请求体；
- 以 `(user_id, apple_health_workout_id)` 做幂等；
- 异步解析 JSON，写入运动记录、轨迹、样本、分段；
- 通过 `code_apple_source_status` 暴露解析状态；
- 处理重复上传、解析失败、重试和重分析。

---

## 4. RQrun 客户端实际实现

### 4.1 页面结构

3.5.8 静态分析确认的健康相关类：

| 类 | 作用 |
|---|---|
| `HealthSettingVC` | 健康设置入口，表格式设置项 |
| `HealthUploadVC` | 上传容器页，继承 `XLPagerTabStrip` 的 `ButtonBarPagerTabStripViewController` |
| `HealthUploadSelectVC` | 待上传 workout 列表，支持全选、单条选择、上传 |
| `HealthUploadedVC` | 已上传 workout 列表 |
| `HealthUploadCell` | 待上传单元格 |
| `HealthUploadedCell` | 已上传单元格，支持“重新上传” |
| `HealthAuthStatusCell` | HealthKit 授权状态展示 |
| `HealthNoteVC` | 健康说明/提示页 |
| `HealthAuthMarkXIB` | 授权标记视图 |
| `HealthUploadGuideXIB` | 上传引导视图 |

辅助类：

- `HealthKitManager`
- `HealthKitTool`
- `HealthKitWorkoutTool`
- `HealthKitSleepTool`
- `HealthKitDownDataTool`
- `HealthLoadWorkouts`
- `APIHealthManager`
- `APIHealthRouter`

### 4.2 `HealthUploadVC`：容器页

已知方法：

| 方法 | 地址 | 行为 |
|---|---|---|
| `viewDidLoad` | `0x1001b42c4` | 配置 `XLPagerTabStrip` 分页、导航栏 |
| `historyListAct` | `0x1001b4b2c` | 弹出历史/说明提示 |
| `navButtonAct:` | `0x1001b5710` | 触发上传相关弹窗 |
| `clearWorkoutActWithSender:` | `0x1001b5de8` | 向通知中心发通知 |

`viewDidLoad` 会调用 `FUN_1001b50c0` 设置分页样式，调用 `FUN_1001b42ec` 配置导航按钮。**[静态]**

`HealthUploadVC` 本身是容器，实际待上传列表在子控制器 `HealthUploadSelectVC`，已上传列表在 `HealthUploadedVC`。**[静态/推断]**

### 4.3 `HealthUploadSelectVC`：待上传列表

已知方法（共 28 个 ObjC 方法，以下为关键项）：

| 方法 | 地址 | 行为 |
|---|---|---|
| `viewDidLoad` | `0x1006984cc` | 初始化表格、按钮、通知监听 |
| `tableView:numberOfRowsInSection:` | `0x10069d4f0` | 返回 `willUPloadWorkoutList.count`；空列表返回 1 个占位行 |
| `tableView:cellForRowAtIndexPath:` | `0x10069daec` | 配置 `HealthUploadCell`，绑定 workout、上传状态 |
| `selectAllAct:` | `0x10069b1a0` | 切换全选状态，按规则筛选 |
| `uploadButtonAct:` | `0x10069b7bc` | 上传按钮入口，检查状态并触发上传 |
| `closeUploadWithNoti:` | `0x1006989b0` | 收到关闭通知，`uploadingStatus = 0`，刷新 |
| `updateHealthUploadPageActWithNoti:` | `0x100698a60` | 收到更新通知，设置 `isToBottom = 1`，按上传状态刷新 |
| `reload` | `0x100697fb4` | 重新加载列表 |

状态字段：

- `willUPloadWorkoutList`：待上传 workout 数组
- `selectAllStatus`：全选状态
- `uploadingStatus`：0 空闲，1 上传中
- `uploadingWorkoutId`：当前上传的 workout 标识
- `isReamin`：是否还有剩余记录（代码拼写为 `isReamin`）
- `isToBottom`：是否滚动到底部

### 4.4 全选筛选规则

`selectAllAct:`（`0x10069b1a0`）的逻辑：

1. 切换 `selectAllStatus`。
2. 遍历 `willUPloadWorkoutList`。
3. 对每个 workout：
   - `workoutActivityType == 0x25`（37，Running）→ 候选；
   - 或 `workoutActivityType == 0x2e`（46，Swimming）→ 候选；
   - 或 `workoutActivityType == DAT_101b38b98`（特殊全局类型，静态值未解析，可能是 `Other`）→ 候选；
   - 其他类型强制不选。
4. 候选还必须满足 `totalDistance` 的 `HKUnit.meterUnit` 值 `> 5.0`。
5. 其余 workout 强制不选。

结论：**“全选”只覆盖跑步、游泳和特殊类型，并且距离要大于阈值；不是所有 HealthKit workout。** **[静态]**

> 注意：`> 5.0` 的单位在静态代码中读的是 `HKUnit.meterUnit`；实际业务语义是 5 米还是 5 公里，建议用 3.5.8 动态验证。不要在自研实现里直接照抄这个阈值。

### 4.5 上传按钮状态机

`uploadButtonAct:` 先桥接 sender，再调用 `FUN_10069b204`。分支如下：

| 条件 | 进入 | 行为 |
|---|---|---|
| `uploadingStatus == 1` | `FUN_10069a7fc` | 上传中提示，不重复启动 |
| `(isReamin & 1) == 0` | `FUN_10069b820` | 自定义提示框（空列表/无剩余记录） |
| 其他 | `FUN_10069b320` | 开始上传 |

`FUN_10069b320`（`0x10069b320`）：

- 设置 `startTime = Date()`；
- 读取 SwiftyUserDefaults 的布尔开关；
- 开关为 false 时：
  - 遍历 `willUPloadWorkoutList`；
  - 对已选中且未上传的 workout：
    - 无 workout 对象 → `FUN_10069c2a0`
    - 有 workout 对象 → `FUN_10069c040`
  - 更新 `uploadingWorkoutId`；
  - 刷新表格；
  - 置 `uploadingStatus = 1`；
  - 结束后恢复 `uploadingStatus = 0`；
- 开关为 true 时走提示分支（具体提示文案未完全还原）。**[静态]**

### 4.6 单条上传的 PromiseKit 链

- `FUN_10069c2a0`（`0x10069c2a0`）：构造 workout 记录模型，调用 `FUN_10069c5d0`。
- `FUN_10069c040`（`0x10069c040`）：使用 PromiseKit 的 `firstly`、`done`、`catch` 串行上传。
- `FUN_10069c5d0`（`0x10069c5d0`）：拼装上传模型，调用 `FUN_10074d424`。
- `FUN_10074d424`（`0x10074d424`）：引用以下字段：
  - `distance`
  - `duration`
  - `startTime`
  - `endTime`
  - `lapNum`
  - `stepCount`

这说明上传模型至少包含距离、时长、起止时间、圈数、步数。**[静态]**

### 4.7 通知与页面刷新

- `closeUploadWithNoti:`：
  - `uploadingStatus = 0`
  - 刷新表格
- `updateHealthUploadPageActWithNoti:`：
  - `isToBottom = 1`
  - 上传中只打印日志
  - 非上传中刷新页面
- `HealthUploadedVC`：
  - 同名 `updateHealthUploadPageActWithNoti:`（`0x10075d7b4`）
  - `reload`（`0x10075d308`）
- `HealthUploadedCell.uploadAgainAct:`（`0x100319584`）：
  - 通过 delegate `FUN_1007608c0` 重新上传

这套通知机制的作用：**上传是异步的，待上传页和已上传页通过通知同步刷新，而不是共用一个强耦合的数据源对象。** **[静态/推断]**

### 4.8 自动上传

`AppDelegate.autoUploadAppleWorkoutAct:`（`0x1004b17fc`）：

- 从通知 `object` 解析字符串；
- 与 `uploadOn` 比较；
- 命中后设置 `healthKitDownDataTool` 的布尔属性；
- 通过 PromiseKit resolver 触发自动上传。

相关类：

- `HealthKitDownDataTool`
- `HealthKitWorkoutTool`
- `HealthLoadWorkouts`

结论：RQrun 除了手动选择上传，还存在一个 `uploadOn` 通知驱动的自动上传入口；它可能用于“健康数据自动同步”开关，但 3.5.8 静态分析无法确认它在什么条件下被触发。**[静态/推断]**

### 4.9 本地持久化

3.5.8 字符串/符号表确认：

| 类/模型 | 可能职责 |
|---|---|
| `DbAuthorizedHealth` | HealthKit 授权状态记录 |
| `DbAuthorizedHealthModel` | 授权状态数据模型 |
| `DbManagerAuthorizedHealth` | 授权状态管理器 |
| `DbHealthUploadSuccess` | 已成功上传的 workout 记录 |
| `DbHealthUploadSuccessModel` | 上传成功数据模型 |
| `DbManagerHealthUploadSuccess` | 上传成功管理器 |

用途：

- 避免重复上传；
- 记录 `apple_health_workout_id`；
- 支持“已上传”列表展示；
- 支持失败/重传状态。

自研实现建议使用**唯一索引 + 状态字段**，不要只靠内存数组。**[建议]**

### 4.10 关键地址汇总

| 符号 | 地址 |
|---|---|
| `HealthUploadSelectVC.viewDidLoad` | `0x1006984cc` |
| `HealthUploadSelectVC.selectAllAct:` | `0x10069b1a0` |
| `HealthUploadSelectVC.uploadButtonAct:` | `0x10069b7bc` |
| `HealthUploadSelectVC.closeUploadWithNoti:` | `0x1006989b0` |
| `HealthUploadSelectVC.updateHealthUploadPageActWithNoti:` | `0x100698a60` |
| `FUN_10069b320`（上传循环） | `0x10069b320` |
| `FUN_10069c2a0`（构造模型） | `0x10069c2a0` |
| `FUN_10069c040`（PromiseKit） | `0x10069c040` |
| `FUN_10069c5d0`（拼装上传模型） | `0x10069c5d0` |
| `FUN_10074d424`（记录字段映射） | `0x10074d424` |
| `HealthUploadVC.viewDidLoad` | `0x1001b42c4` |
| `HealthUploadVC.historyListAct` | `0x1001b4b2c` |
| `HealthUploadVC.navButtonAct:` | `0x1001b5710` |
| `HealthUploadedVC.reload` | `0x10075d308` |
| `HealthUploadedCell.uploadAgainAct:` | `0x100319584` |
| `AppDelegate.autoUploadAppleWorkoutAct:` | `0x1004b17fc` |
| `HealthSettingVC.viewDidLoad` | `0x1005b67b0` |
| `HealthSettingVC.tableView:didSelectRowAtIndexPath:` | `0x1005b6b94` |

---

## 5. 权限与数据源

### 5.1 Info.plist 与 entitlement

RQrun 3.7.2 的 Info.plist 中包含：

| Key | 值/作用 |
|---|---|
| `NSHealthShareUsageDescription` | “您的健康资讯将可用来帮助分析” |
| `NSHealthUpdateUsageDescription` | “您的跑步记录将可同步到健康中” |
| HealthKit entitlement | 需要 `com.apple.developer.healthkit` |
| 后台模式 | 如需后台感知，使用 HealthKit background delivery |

自研实现至少要：

```xml
<key>NSHealthShareUsageDescription</key>
<string>用于读取 Apple Watch 跑步记录并同步到云端</string>
<key>NSHealthUpdateUsageDescription</key>
<string>用于将训练记录写回健康（可选）</string>
```

如果只做“读取跑步记录并上传”，`toShare` 可以为空；如果还要写睡眠/训练，才申请写权限。**[建议]**

### 5.2 需要读取的 HealthKit 类型

| HealthKit 类型 | 用途 | 对应上传字段 |
|---|---|---|
| `HKWorkoutType.workoutType()` | 一次运动 | 概要字段 |
| `HKSeriesType.workoutRoute()` | GPS 轨迹 | `map_position_list` |
| `HKQuantityTypeIdentifierHeartRate` | 心率 | `heart_rates_list`、`avg_heart_rate` |
| `HKQuantityTypeIdentifierRunningPower` | 跑步功率 | `power_list` |
| `HKQuantityTypeIdentifierRunningSpeed` | 速度 | `time_distance`、`meters_per_second` |
| `HKQuantityTypeIdentifierStepCount` | 步数 | `total_step_count`、步频推导 |
| `HKQuantityTypeIdentifierDistanceWalkingRunning` | 距离 | `total_distance` |
| `HKQuantityTypeIdentifierRunningStrideLength` | 步幅 | `stride_length_list` |
| `HKQuantityTypeIdentifierRunningGroundContactTime` | 触地时间 | `contact_time_list` |
| `HKQuantityTypeIdentifierRunningVerticalOscillation` | 垂直振幅 | `vertical_list` |
| `HKQuantityTypeIdentifierActiveEnergyBurned` | 活动能量 | `total_calories` |
| `HKQuantityTypeIdentifierSwimmingStrokeCount` | 游泳划水 | `swimming_stroke_count` |

> `RunningPower`、`RunningSpeed`、`RunningStrideLength`、`RunningGroundContactTime`、`RunningVerticalOscillation` 需要 iOS 16 / watchOS 9 及以上；旧系统上这些查询应允许为空。**[建议]**

### 5.3 `auth_list`：Apple 是本地数据源

3.7.2 抓包中的 `POST /Api/WatchAuth/auth_list` 返回：

```json
{
  "data": {
    "auth_count": 3,
    "auth_list": ["8", "10", "15"],
    "def_source": 8,
    "sleep_def_source": 8,
    "check_master_source": 1,
    "auth_detailed_list": [
      { "provider_id": "8",  "provider": "iwatch",          "provider_name": "苹果运动健康", "can_sync": 0 },
      { "provider_id": "10", "provider": "glgarminconnect", "provider_name": "佳明国际",     "can_sync": 1 },
      { "provider_id": "15", "provider": "cngarminconnect", "provider_name": "佳明中国",     "can_sync": 1 }
    ]
  }
}
```

关键点：

- `provider_id = 8`：苹果运动健康（HealthKit）
- `can_sync = 0`：Apple 数据不能由服务端直接拉取，必须由 iOS 客户端读 HealthKit 后上传
- `def_source = 8`：默认数据源是 Apple
- Garmin 的 `can_sync = 1`：服务端可以走云端同步

这解释了为什么 RQrun 要做“手机端读取 + 大 JSON 上传”，而不是服务端直接对接 Apple。**[实测]**

### 5.4 HealthKit 读权限的工程陷阱

HealthKit 的 `authorizationStatus(for:)` **只反映“写权限”**；对只读类型，它通常返回 `.notDetermined`，即使读权限被拒绝也不会明确告诉你。**[建议/平台事实]**

因此：

- 不要用 `authorizationStatus` 判断读权限；
- 通过查询结果判断：空数据、`HKError.errorAuthorizationDenied`、`errorAuthorizationNotDetermined`；
- UI 上显示“未授权或无数据”即可，不要声称“用户拒绝”；
- 记录 `get_*_auth` 的原因字符串，便于诊断。

RQrun 的 `get_*_auth` 字段就是这种“空串=可用，非空=原因”的设计。**[实测]**

---

## 6. HealthKit 数据读取

### 6.1 读取 workout

最简单的方式是 `HKSampleQuery`：

```swift
let store = HKHealthStore()

let predicate = HKQuery.predicateForSamples(
    withStart: startDate,
    end: endDate,
    options: [.strictStartDate]
)

let query = HKSampleQuery(
    sampleType: HKObjectType.workoutType(),
    predicate: predicate,
    limit: HKObjectQueryNoLimit,
    sortDescriptors: [NSSortDescriptor(key: HKSampleSortIdentifierStartDate, ascending: false)]
) { _, samples, error in
    let workouts = samples as? [HKWorkout] ?? []
    // ...
}
store.execute(query)
```

生产环境建议用 `HKAnchoredObjectQuery` 做增量：

```swift
let query = HKAnchoredObjectQuery(
    type: HKObjectType.workoutType(),
    predicate: predicate,
    anchor: savedAnchor,          // 持久化 HKQueryAnchor
    limit: HKObjectQueryNoLimit
) { _, samples, deleted, newAnchor, error in
    // 处理新增 workout
    // 持久化 newAnchor
}
```

注意：

- `HKWorkout` 不可变；用户可能在健康 App 中删除记录；
- 用 `HKAnchoredObjectQuery` 的 `deleted` 处理删除；
- 首次全量拉取后保存 anchor；
- 不要把 `HKQueryAnchor` 存成普通字符串后丢失类型信息，建议 `NSKeyedArchiver` 或数据库 BLOB。

### 6.2 `HKWorkout` 概要字段

```swift
let uuid = workout.uuid.uuidString
let activityType = workout.activityType.rawValue
let start = workout.startDate
let end = workout.endDate
let duration = workout.duration // seconds
let distance = workout.totalDistance?.doubleValue(for: .meter())
let energy = workout.totalEnergyBurned?.doubleValue(for: .kilocalorie())
let sourceBundle = workout.sourceRevision.source.bundleIdentifier
let sourceName = workout.sourceRevision.source.name
let device = workout.sourceRevision.device
```

`HKDevice` 可提供：

- `name`
- `model`
- `hardwareVersion`
- `manufacturer`
- `softwareVersion`

对应上传字段 `apple_watch_device`。**[实测映射/推断]**

### 6.3 路线 `HKWorkoutRoute`

```swift
func loadRoute(for workout: HKWorkout) async throws -> [CLLocation] {
    let routes = try await withCheckedThrowingContinuation { continuation in
        let query = HKSampleQuery(
            sampleType: HKSeriesType.workoutRoute(),
            predicate: HKQuery.predicateForObjects(from: workout),
            limit: HKObjectQueryNoLimit,
            sortDescriptors: nil
        ) { _, samples, error in
            if let error { continuation.resume(throwing: error); return }
            continuation.resume(returning: samples as? [HKWorkoutRoute] ?? [])
        }
        store.execute(query)
    }

    guard let route = routes.first else { return [] }

    return try await withCheckedThrowingContinuation { continuation in
        var locations: [CLLocation] = []
        let query = HKWorkoutRouteQuery(route: route) { _, batch, done, error in
            if let error { continuation.resume(throwing: error); return }
            if let batch { locations.append(contentsOf: batch) }
            if done { continuation.resume(returning: locations) }
        }
        store.execute(query)
    }
}
```

映射到 `map_position_list`：

```swift
struct MapPoint: Codable {
    let position_latitude: Double
    let position_longitude: Double
    let altitude: Double
    let speed: Double
    let timestamp: Int
    let sec: Int
}

func mapPoints(from locations: [CLLocation]) -> [MapPoint] {
    let sorted = locations.sorted { $0.timestamp < $1.timestamp }
    var previous: Date?
    return sorted.map { loc in
        let sec = previous.map { Int(loc.timestamp.timeIntervalSince($0).rounded()) } ?? 1
        previous = loc.timestamp
        return MapPoint(
            position_latitude: loc.coordinate.latitude,
            position_longitude: loc.coordinate.longitude,
            altitude: loc.altitude,
            speed: max(0, loc.speed),
            timestamp: Int(loc.timestamp.timeIntervalSince1970),
            sec: max(1, sec)
        )
    }
}
```

注意事项：

- `CLLocation.speed` 可能为负（无效值），要归零；
- `horizontalAccuracy < 0` 表示无效点，建议过滤；
- 室内跑通常没有 route；
- route 可能还在写入中，刚结束的 workout 可能暂时没有完整 route；
- GPS 点不要做过度抽稀，RQrun 实测约 1 点/秒。

### 6.4 逐点样本

通用查询：

```swift
func loadSamples(
    type: HKQuantityType,
    workout: HKWorkout
) async throws -> [HKQuantitySample] {
    try await withCheckedThrowingContinuation { continuation in
        let query = HKSampleQuery(
            sampleType: type,
            predicate: HKQuery.predicateForObjects(from: workout),
            limit: HKObjectQueryNoLimit,
            sortDescriptors: [
                NSSortDescriptor(key: HKSampleSortIdentifierStartDate, ascending: true)
            ]
        ) { _, samples, error in
            if let error { continuation.resume(throwing: error); return }
            continuation.resume(returning: samples as? [HKQuantitySample] ?? [])
        }
        store.execute(query)
    }
}
```

指标映射：

| 指标 | HK 类型 | 单位 | 上传字段 |
|---|---|---|---|
| 心率 | `.heartRate` | count/min | `heart_rates_list` |
| 跑步功率 | `.runningPower` | W | `power_list` |
| 步数 | `.stepCount` | count | `cadences_list` 推导、`total_step_count` |
| 步幅 | `.runningStrideLength` | m | `stride_length_list` |
| 触地时间 | `.runningGroundContactTime` | ms | `contact_time_list`（如按秒输出，除以 1000） |
| 垂直振幅 | `.runningVerticalOscillation` | cm | `vertical_list`（如按米输出，除以 100） |
| 游泳划水 | `.swimmingStrokeCount` | count | `swimming_stroke_count` |

### 6.5 步频 `cadences_list` 的说明

HealthKit 在公开 API 中没有 `HKQuantityTypeIdentifierRunningCadence`（截至 iOS 17 SDK；只有 `CyclingCadence`）。**[平台事实]**

RQrun 的 `cadences_list` 有 887 个点，几乎与 `power_list` 对齐，说明它很可能是：

1. 从 `HKQuantityTypeIdentifierStepCount` 的区间样本推导：
   ```text
   cadence = steps / interval_seconds * 60
   ```
2. 或从 Apple Watch 写入的 workout metadata / 第三方 Watch App 的扩展字段读取；
3. 或由手机端按固定时间窗对步数做重采样。

**结论**：`cadences_list` 不能直接用一个 HealthKit 类型读取；自研实现应从 `stepCount` 样本按时间窗聚合，或明确标记为“不可用”。**[推断/建议]**

### 6.6 `time_distance` 的生成

`time_distance[]` 是累计距离-时间曲线：

```json
{ "startTime": 1788957365, "endTime": 1788957367, "distance": 6.789339212351479 }
```

推荐生成方式：

- 以 route 点或 `runningSpeed` 样本为时间轴；
- 对相邻点用 `distance += speed * dt` 积分；
- 或直接用 `CLLocation.distance(from:)` 累加相邻 GPS 点；
- `distance` 是**累计米数**，不是区间距离；
- `startTime` / `endTime` 为 Unix 秒。

### 6.7 分段与事件

`HKWorkoutEvent`：

```swift
for event in workout.events ?? [] {
    switch event.type {
    case .segment:
        // event.dateInterval.start / end
    default:
        break
    }
}
```

iOS 16+ 还可以使用 `workout.workoutActivities`（`HKWorkoutActivity`）获取更细的活动段。

RQrun 的 `activities_list[]` 包含：

- `workout_type`
- `start_time` / `end_time`（Float，Unix 秒带小数）
- `duration`
- `total_distance`
- `meters_per_second`
- `avg_heart`
- `avg_power`
- `total_step_count`
- `location_type`
- `event_list`

`workout_evnet_list[]` 是顶层事件列表，字段为 `startTime`、`endTime`、`type`；`type = 7` 在实测中对应跑步段。**[实测/推断]**

### 6.8 天气与设备

天气来自 `HKWorkout.metadata`：

```swift
let temperature = workout.metadata?[HKMetadataKeyWeatherTemperature] as? HKQuantity
let humidity = workout.metadata?[HKMetadataKeyWeatherHumidity] as? HKQuantity

let celsius = temperature?.doubleValue(for: .degreeCelsius())
let humidityPercent = humidity?.doubleValue(for: .percent())
```

室内标志：

```swift
let indoor = (workout.metadata?[HKMetadataKeyIndoorWorkout] as? NSNumber)?.boolValue ?? false
```

### 6.9 单项重试与错误记录

RQrun 的 `get_data_error` 表明：

- 每个指标独立读取；
- 最多尝试 3 次；
- 失败原因写入 JSON；
- 单项失败不阻断整条记录上传。

推荐实现：

```swift
func loadWithRetry<T>(
    _ operation: () async throws -> T,
    attempts: Int = 3,
    label: String,
    errors: inout [String: String]
) async -> T? {
    for attempt in 1...attempts {
        do { return try await operation() }
        catch {
            errors["\(label)_error"] = "第\(attempt)次获取，错误：\(error.localizedDescription)"
        }
    }
    return nil
}
```

注意 Swift 的 `inout` 不能跨 `async` 并发使用；上面的伪代码仅表达逻辑，实际实现可用 `actor` 或返回 `(value, errors)`。**[建议]**

---

## 7. 上传载荷 `originalRecode.json`

### 7.1 编码与命名

| 项 | 值 |
|---|---|
| 文件名 | `originalRecode.json`（注意拼写 `Recode`，不是 `Record`） |
| 编码 | UTF-8 |
| 格式 | 明文 JSON，未压缩 |
| MIME | `text/plain`（抓包中如此，虽然内容是 JSON） |
| 顶层 | 约 40 个字段 |
| 单条 40 分钟跑步 | 约 641 KB |

### 7.2 顶层字段

#### 标识与来源

| 字段 | 类型 | 说明 |
|---|---|---|
| `apple_health_workout_id` | string | `HKWorkout.uuid`，幂等键 |
| `apple_record_source_bundleIdentifier` | string | `workout.sourceRevision.source.bundleIdentifier` |
| `apple_record_source_name` | string | `workout.sourceRevision.source.name` |
| `apple_watch_device` | object | `HKDevice` 信息 |
| `app_version` | string | 例如 `3.7.2(4)` |
| `ios_version` | string | 例如 `27.2` |
| `upload_time_zh` | string | 本地时区 `yyyy-MM-dd HH:mm:ss` |
| `upload_page` | string | 固定 `HealthUploadSelectVC` |

#### 运动概要

| 字段 | 类型 | 单位/取值 | 说明 |
|---|---|---|---|
| `sport` | int | 1 = 跑步 | 业务运动大类 |
| `sport_name` | string | `跑步` | 业务名称 |
| `activity_name` | string | `户外跑步` | 细分名称 |
| `activity_type` | int | `HKWorkoutActivityType`，37 = running | 原始类型 |
| `indoor_workout` | int | 0 户外 / 1 室内 | 来自 metadata |
| `start_time` | int | Unix 秒 | workout.startDate |
| `end_time` | int | Unix 秒 | workout.endDate |
| `total_time` | int | 秒 | workout.duration |
| `total_distance` | int | 米 | totalDistance |
| `total_calories` | int | 千卡 | totalEnergyBurned |
| `total_ascent` | int | 米 | 累计爬升 |
| `avg_heart_rate` | float | bpm | 平均心率 |
| `swimming_stroke_count` | int | 次 | 跑步为 0 |

#### 明细序列

| 字段 | 类型 | 40 分钟跑步点数 | 说明 |
|---|---|---|---|
| `map_position_list` | array | 2283 | GPS 轨迹 |
| `heart_rates_list` | array | 456 | 心率 |
| `time_distance` | array | 884 | 累计距离-时间 |
| `power_list` | array | 887 | 跑步功率 |
| `cadences_list` | array | 887 | 步频 |
| `stride_length_list` | array | 423 | 步幅 |
| `contact_time_list` | array | 428 | 触地时间 |
| `vertical_list` | array | 432 | 垂直振幅 |
| `activities_list` | array | 1 | 分段活动 |
| `workout_evnet_list` | array | 11 | 事件段（拼写 `evnet`） |

#### 计划、天气、授权与诊断

| 字段 | 类型 | 说明 |
|---|---|---|
| `apple_workout_plan` | object | WorkoutKit 计划；没有则空对象 |
| `weather_temperature` | float | 摄氏度 |
| `weather_humidity` | int | 百分比 |
| `get_map_auth` | string | 空串 = 可用；非空 = 原因 |
| `get_power_auth` | string | 功率 |
| `get_vertical_auth` | string | 垂直振幅 |
| `get_hear_auth` | string | 心率（拼写是 `hear`） |
| `get_step_auth` | string | 步数 |
| `get_contact_time_auth` | string | 触地时间 |
| `get_stride_length_auth` | string | 步幅 |
| `get_cycle_power_auth` | string | 骑行功率 |
| `get_step_count` | string | 步数（跑步实测为空） |
| `get_data_error` | object | 各指标读取错误详情 |

### 7.3 嵌套结构

#### `map_position_list[]`

| 字段 | 类型 | 单位 |
|---|---|---|
| `position_latitude` | float | 十进制度 |
| `position_longitude` | float | 十进制度 |
| `altitude` | float | 米 |
| `speed` | float | m/s |
| `timestamp` | int | Unix 秒 |
| `sec` | int | 采样间隔秒 |

#### `heart_rates_list[]`

| 字段 | 类型 | 单位 |
|---|---|---|
| `heart_rate` | int | bpm |
| `start_time` | int | Unix 秒 |
| `end_time` | int | Unix 秒 |

#### `time_distance[]`

| 字段 | 类型 | 单位 |
|---|---|---|
| `startTime` | int | Unix 秒 |
| `endTime` | int | Unix 秒 |
| `distance` | float | 米（累计） |

#### `apple_watch_device`

```json
{
  "name": "Apple Watch",
  "model": "Watch",
  "hardwareVersion": "Watch6,2",
  "manufacturer": "Apple Inc.",
  "softwareVersion": "26.1"
}
```

#### `activities_list[]`

| 字段 | 类型 | 说明 |
|---|---|---|
| `workout_type` | int | HKWorkoutActivityType |
| `start_time` | float | Unix 秒（带小数） |
| `end_time` | float | Unix 秒（带小数） |
| `duration` | float | 秒 |
| `total_distance` | float | 米 |
| `meters_per_second` | float | 平均速度 |
| `avg_heart` | float | 平均心率 |
| `avg_power` | float | 平均功率 |
| `total_step_count` | int | 步数 |
| `location_type` | int | 3 = 户外（实测） |
| `event_list` | array | 段事件 |

#### `event_list[]` / `workout_evnet_list[]`

| 字段 | 类型 | 说明 |
|---|---|---|
| `startTime` | float | Unix 秒 |
| `endTime` | float | Unix 秒 |
| `type` | int | 7 = 跑步段（实测推断） |

#### `apple_workout_plan`

```json
{
  "display_name": "",
  "workout_type": "goal",
  "goal_data": {
    "goal_type": "",
    "goal_value": "",
    "goal_unit_name": ""
  }
}
```

#### `get_data_error`

```json
{
  "get_heart_rates_list_error": "第1次获取",
  "get_contact_time_list_error": "第1次获取",
  "get_stride_length_list_error": "第1次获取",
  "get_avg_heart_rate_error": "第1次获取",
  "get_swimming_stroke_count_error": "第3次获取，错误：No data available for the specified predicate.",
  "get_cycle_power_list_error": "第3次获取，samples数量为0",
  "get_activities_list_error": "第1次获取",
  "get_power_list_error": "第1次获取",
  "get_time_distance_error": "…"
}
```

### 7.4 最小结构示意

> 下面是结构示意，数组用空数组/单元素表示；真实上传时数组会有大量点。不要把它当成完整抓包样本。

```json
{
  "apple_health_workout_id": "8CEA80AE-FC92-470A-95EA-D01F11D53866",
  "apple_record_source_bundleIdentifier": "com.apple.health.<UUID>",
  "apple_record_source_name": "Apple Watch",
  "apple_watch_device": {
    "name": "Apple Watch",
    "model": "Watch",
    "hardwareVersion": "Watch6,2",
    "manufacturer": "Apple Inc.",
    "softwareVersion": "26.1"
  },
  "app_version": "3.7.2(4)",
  "ios_version": "27.2",
  "upload_time_zh": "2026-10-31 16:58:57",
  "upload_page": "HealthUploadSelectVC",
  "sport": 1,
  "sport_name": "跑步",
  "activity_name": "户外跑步",
  "activity_type": 37,
  "indoor_workout": 0,
  "start_time": 1788957364,
  "end_time": 1788959645,
  "total_time": 2280,
  "total_distance": 6140,
  "total_calories": 374,
  "total_ascent": 3,
  "avg_heart_rate": 166.07,
  "swimming_stroke_count": 0,
  "map_position_list": [],
  "heart_rates_list": [],
  "time_distance": [],
  "power_list": [],
  "cadences_list": [],
  "stride_length_list": [],
  "contact_time_list": [],
  "vertical_list": [],
  "activities_list": [],
  "workout_evnet_list": [],
  "apple_workout_plan": {
    "display_name": "",
    "workout_type": "goal",
    "goal_data": { "goal_type": "", "goal_value": "", "goal_unit_name": "" }
  },
  "weather_temperature": 29.06,
  "weather_humidity": 69,
  "get_map_auth": "",
  "get_power_auth": "",
  "get_vertical_auth": "",
  "get_hear_auth": "",
  "get_step_auth": "",
  "get_contact_time_auth": "",
  "get_stride_length_auth": "",
  "get_cycle_power_auth": "",
  "get_step_count": "",
  "get_data_error": {}
}
```

### 7.5 单位与取整

| 字段 | 单位 | 建议 |
|---|---|---|
| `start_time` / `end_time` | Unix 秒 | Int；从 `Date` 取整 |
| `total_time` | 秒 | Int |
| `total_distance` | 米 | Int；四舍五入 |
| `total_calories` | 千卡 | Int；四舍五入 |
| `avg_heart_rate` | bpm | Float，保留 1–2 位 |
| `map_position_list.speed` | m/s | Float；负值归零 |
| `contact_time_list` | 秒（RQrun 字段标注） | HealthKit 原始是 ms，需 `/1000` **[推断]** |
| `vertical_list` | 米（RQrun 字段标注） | HealthKit 原始是 cm，需 `/100` **[推断]** |
| `stride_length_list` | 米 | 原值 |
| `power_list` | W | 原值 |
| `heart_rate` | bpm | Int |
| `time_distance.distance` | 米（累计） | Float |

### 7.6 大小与性能

| 指标 | 40 分钟跑步 |
|---|---|
| JSON 大小 | 641,476 B（约 626 KB） |
| GPS 点 | 2,283 |
| 心率点 | 456 |
| 功率点 | 887 |
| 步频点 | 887 |
| 时间-距离点 | 884 |
| 步幅点 | 423 |
| 触地时间点 | 428 |
| 垂直振幅点 | 432 |
| 事件段 | 11 |

估算：

- 1 小时跑步约 0.9–1.0 MB；
- 每周 5 次约 5 MB；
- 每年约 250 MB（未压缩）；
- gzip 后通常 15–25%。

---

## 8. HTTP 协议

### 8.1 公共认证

3.7.2 抓包的普通 API：

```http
POST /Api/User/get_user_info HTTP/1.1
Host: api.rq.run
Content-Type: application/x-www-form-urlencoded; charset=utf-8
Accept-Language: zh-Hans-CN;q=1.0
Authorization: <应用级固定密钥>
X-USER-TOKEN: <用户 token>
Cookie: think_language=zh-Hans-CN

os_version=27.2&phone_brand=iPhone&phone_model=iPhone13%2C4&request_version=2&token=<用户 token>&user_id=<用户ID>
```

| 凭据 | 位置 |
|---|---|
| 用户 token | `X-USER-TOKEN` + body `token` |
| 用户 ID | body `user_id` |
| 应用级密钥 | `Authorization`（硬编码） |
| 语言 | Cookie `think_language` |

> 这是 RQrun 的历史实现，不建议照搬。**[建议]**

### 8.2 上传接口

```http
POST https://api.rq.run/Api/Record/upload_record HTTP/1.1
Host: api.rq.run
Content-Type: multipart/form-data; boundary=alamofire.boundary.<随机串>
Authorization: <应用级固定密钥>
X-USER-TOKEN: <用户 token>
User-Agent: RQ Run/3.7.2 (cn.runningquotient.RQ-Run; build:4; iOS 27.2.0) Alamofire/5.0.0

--alamofire.boundary.<随机串>
Content-Disposition: form-data; name="file"; filename="originalRecode.json"
Content-Type: text/plain

<原始 JSON>
--alamofire.boundary.<随机串>--
```

| 项 | 值 |
|---|---|
| 方法 | POST |
| 路径 | `/Api/Record/upload_record` |
| Content-Type | `multipart/form-data` |
| 表单字段 | `file`（固定） |
| 文件名 | `originalRecode.json`（固定） |
| MIME | `text/plain` |
| Body 内 token/user_id | 不包含 |
| 单次大小 | 40 分钟约 641 KB |

响应：

```json
{ "syscode": 200, "sysmsg": "上传成功", "data": "113467563" }
```

- `data` 是服务端记录 ID（字符串）；
- 失败响应未在抓包中出现；
- 建议按 `syscode != 200` 处理业务失败。

### 8.3 状态查询

```http
GET /Record/code_apple_source_status?token=<用户 token>&user_id=<用户ID> HTTP/1.1
Host: api.rq.run
X-USER-TOKEN: <用户 token>
Authorization: <应用级固定密钥>
```

响应：

```json
{
  "syscode": 200,
  "sysmsg": "success",
  "data": [
    {
      "apple_health_workout_id": "C679E49E-43B3-4E8B-85AE-49205B5D4053",
      "file_ext": "json",
      "status": 1,
      "remark": "成功",
      "analytic_msg": "成功",
      "created_at": "2023-03-03 17:58:25"
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `apple_health_workout_id` | 对应上传的 HKWorkout UUID |
| `file_ext` | `json` / `wjson`（后者疑似 Watch 端） |
| `status` | 1 = 成功（其他未验证） |
| `remark` | 结果描述 |
| `analytic_msg` | 解析结果 |
| `created_at` | 服务端时间 |

### 8.4 数据源授权

```http
POST /Api/WatchAuth/auth_list
Content-Type: application/x-www-form-urlencoded; charset=utf-8
X-USER-TOKEN: <用户 token>
Authorization: <应用级固定密钥>
```

关键返回：`provider_id = 8`、`provider = iwatch`、`can_sync = 0`。见 5.3。

### 8.5 其他相关接口（静态/未抓包）

3.5.8 字符串表还出现：

- `Api/Workout/get_apple_health_workouts`
- `Api/Workout/get_uploaded_apple_health_workouts`
- `/Api/Record/add_record`
- `/Api/Record/get_apple_source_record_id`
- `/Api/Record/add_apple_sleep_record`
- `/Api/Sleep/add_sleep_record_v2`

这些不一定是 3.7.2 跑步上传主链路；`get_uploaded_apple_health_workouts` 很可能用于“已上传列表/去重”，但抓包未命中，标为 **[静态/未验证]**。

### 8.6 cURL 示例

```bash
curl -X POST 'https://api.rq.run/Api/Record/upload_record' \
  -H 'Authorization: <APP_KEY>' \
  -H 'X-USER-TOKEN: <USER_TOKEN>' \
  -H 'User-Agent: RQ Run/3.7.2 (cn.runningquotient.RQ-Run; build:4; iOS 27.2.0) Alamofire/5.0.0' \
  -F 'file=@originalRecode.json;type=text/plain;filename=originalRecode.json'
```

---

## 9. 客户端实现蓝图

### 9.1 模块划分

```
HealthKitSync/
├── Permissions/
│   └── HealthPermissionManager.swift
├── Reading/
│   ├── WorkoutFetcher.swift
│   ├── RouteFetcher.swift
│   └── SampleFetcher.swift
├── Mapping/
│   ├── AppleWorkoutPayload.swift
│   └── PayloadBuilder.swift
├── Upload/
│   ├── MultipartUploader.swift
│   ├── UploadCoordinator.swift
│   └── StatusPoller.swift
├── Storage/
│   └── SyncStore.swift
└── UI/
    ├── HealthUploadSelectViewController.swift
    └── HealthUploadedViewController.swift
```

### 9.2 状态机

```
idle
  │ user selects / auto trigger
  ▼
preparing
  │ read HealthKit + build payload
  ▼
uploading
  │ HTTP 200 + syscode 200
  ▼
waiting_for_parse
  │ status = 1
  ▼
done

任何阶段失败：
  ▼
failed → 可重试 / 可重新上传
```

建议状态字段：

- `pending`：待上传
- `preparing`：读取 HealthKit
- `uploading`：上传中
- `waiting`：等待解析
- `success`：成功
- `failed`：失败
- `skipped`：已存在/已上传

### 9.3 Swift 数据模型骨架

```swift
import Foundation

struct AppleWorkoutPayload: Codable {
    let appleHealthWorkoutID: String
    let appleRecordSourceBundleIdentifier: String
    let appleRecordSourceName: String
    let appleWatchDevice: AppleWatchDevice?
    let appVersion: String
    let iosVersion: String
    let uploadTimeZH: String
    let uploadPage: String

    let sport: Int
    let sportName: String
    let activityName: String
    let activityType: Int
    let indoorWorkout: Int
    let startTime: Int
    let endTime: Int
    let totalTime: Int
    let totalDistance: Int
    let totalCalories: Int
    let totalAscent: Int
    let avgHeartRate: Double
    let swimmingStrokeCount: Int

    let mapPositionList: [MapPoint]
    let heartRatesList: [HeartRatePoint]
    let timeDistance: [TimeDistancePoint]
    let powerList: [QuantityPoint]
    let cadencesList: [QuantityPoint]
    let strideLengthList: [QuantityPoint]
    let contactTimeList: [QuantityPoint]
    let verticalList: [QuantityPoint]
    let activitiesList: [ActivitySegment]
    let workoutEvnetList: [WorkoutEventPoint]

    let appleWorkoutPlan: AppleWorkoutPlan
    let weatherTemperature: Double
    let weatherHumidity: Int
    let getMapAuth: String
    let getPowerAuth: String
    let getVerticalAuth: String
    let getHearAuth: String
    let getStepAuth: String
    let getContactTimeAuth: String
    let getStrideLengthAuth: String
    let getCyclePowerAuth: String
    let getStepCount: String
    let getDataError: [String: String]

    enum CodingKeys: String, CodingKey {
        case appleHealthWorkoutID = "apple_health_workout_id"
        case appleRecordSourceBundleIdentifier = "apple_record_source_bundleIdentifier"
        case appleRecordSourceName = "apple_record_source_name"
        case appleWatchDevice = "apple_watch_device"
        case appVersion = "app_version"
        case iosVersion = "ios_version"
        case uploadTimeZH = "upload_time_zh"
        case uploadPage = "upload_page"
        case sport, sportName = "sport_name", activityName = "activity_name"
        case activityType = "activity_type"
        case indoorWorkout = "indoor_workout"
        case startTime = "start_time"
        case endTime = "end_time"
        case totalTime = "total_time"
        case totalDistance = "total_distance"
        case totalCalories = "total_calories"
        case totalAscent = "total_ascent"
        case avgHeartRate = "avg_heart_rate"
        case swimmingStrokeCount = "swimming_stroke_count"
        case mapPositionList = "map_position_list"
        case heartRatesList = "heart_rates_list"
        case timeDistance = "time_distance"
        case powerList = "power_list"
        case cadencesList = "cadences_list"
        case strideLengthList = "stride_length_list"
        case contactTimeList = "contact_time_list"
        case verticalList = "vertical_list"
        case activitiesList = "activities_list"
        case workoutEvnetList = "workout_evnet_list"
        case appleWorkoutPlan = "apple_workout_plan"
        case weatherTemperature = "weather_temperature"
        case weatherHumidity = "weather_humidity"
        case getMapAuth = "get_map_auth"
        case getPowerAuth = "get_power_auth"
        case getVerticalAuth = "get_vertical_auth"
        case getHearAuth = "get_hear_auth"
        case getStepAuth = "get_step_auth"
        case getContactTimeAuth = "get_contact_time_auth"
        case getStrideLengthAuth = "get_stride_length_auth"
        case getCyclePowerAuth = "get_cycle_power_auth"
        case getStepCount = "get_step_count"
        case getDataError = "get_data_error"
    }
}

struct AppleWatchDevice: Codable {
    let name: String
    let model: String
    let hardwareVersion: String
    let manufacturer: String
    let softwareVersion: String
}

struct MapPoint: Codable {
    let positionLatitude: Double
    let positionLongitude: Double
    let altitude: Double
    let speed: Double
    let timestamp: Int
    let sec: Int

    enum CodingKeys: String, CodingKey {
        case positionLatitude = "position_latitude"
        case positionLongitude = "position_longitude"
        case altitude, speed, timestamp, sec
    }
}

struct HeartRatePoint: Codable {
    let heartRate: Int
    let startTime: Int
    let endTime: Int

    enum CodingKeys: String, CodingKey {
        case heartRate = "heart_rate"
        case startTime = "start_time"
        case endTime = "end_time"
    }
}

struct TimeDistancePoint: Codable {
    let startTime: Int
    let endTime: Int
    let distance: Double
}

struct QuantityPoint: Codable {
    let startTime: Int
    let endTime: Int
    let value: Double
}

struct ActivitySegment: Codable {
    let workoutType: Int
    let startTime: Double
    let endTime: Double
    let duration: Double
    let totalDistance: Double
    let metersPerSecond: Double
    let avgHeart: Double
    let avgPower: Double
    let totalStepCount: Int
    let locationType: Int
    let eventList: [WorkoutEventPoint]

    enum CodingKeys: String, CodingKey {
        case workoutType = "workout_type"
        case startTime = "start_time"
        case endTime = "end_time"
        case duration
        case totalDistance = "total_distance"
        case metersPerSecond = "meters_per_second"
        case avgHeart = "avg_heart"
        case avgPower = "avg_power"
        case totalStepCount = "total_step_count"
        case locationType = "location_type"
        case eventList = "event_list"
    }
}

struct WorkoutEventPoint: Codable {
    let startTime: Double
    let endTime: Double
    let type: Int
}

struct AppleWorkoutPlan: Codable {
    let displayName: String
    let workoutType: String
    let goalData: GoalData

    enum CodingKeys: String, CodingKey {
        case displayName = "display_name"
        case workoutType = "workout_type"
        case goalData = "goal_data"
    }

    struct GoalData: Codable {
        let goalType: String
        let goalValue: String
        let goalUnitName: String

        enum CodingKeys: String, CodingKey {
            case goalType = "goal_type"
            case goalValue = "goal_value"
            case goalUnitName = "goal_unit_name"
        }
    }
}
```

> 注意：`QuantityPoint` 的 `value` 字段名在 RQrun 抓包中并没有统一的 `value` 结构；功率/步频/步幅/触地/垂直数组的具体元素字段需要按 3.7.2 的原始 HAR 再确认。上面的 `QuantityPoint` 只是自研实现的简化模型，不要声称它是 RQrun 的真实结构。**[建议]**

### 9.4 上传协调器伪代码

```swift
actor UploadCoordinator {
    private let reader: HealthKitWorkoutReader
    private let builder: PayloadBuilder
    private let uploader: MultipartUploader
    private let store: SyncStore

    func sync(workouts: [HKWorkout], token: String) async {
        for workout in workouts {
            let uuid = workout.uuid.uuidString
            if await store.isUploaded(uuid) { continue }

            do {
                await store.mark(uuid, state: .preparing)
                let payload = try await builder.build(from: workout)
                let data = try JSONEncoder().encode(payload)

                await store.mark(uuid, state: .uploading)
                let recordID = try await uploader.upload(data: data, token: token)

                await store.mark(uuid, state: .waiting, recordID: recordID)
                // 交给 StatusPoller 异步轮询
            } catch {
                await store.mark(uuid, state: .failed, error: error.localizedDescription)
            }
        }
    }
}
```

### 9.5 Multipart 上传器

```swift
struct MultipartUploader {
    let endpoint = URL(string: "https://api.rq.run/Api/Record/upload_record")!

    func upload(data: Data, token: String) async throws -> String {
        let boundary = "alamofire.boundary.\(UUID().uuidString)"
        var body = Data()

        body.append("--\(boundary)\r\n")
        body.append("Content-Disposition: form-data; name=\"file\"; filename=\"originalRecode.json\"\r\n")
        body.append("Content-Type: text/plain\r\n\r\n")
        body.append(data)
        body.append("\r\n--\(boundary)--\r\n")

        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.setValue(token, forHTTPHeaderField: "X-USER-TOKEN")
        request.setValue("<APP_KEY>", forHTTPHeaderField: "Authorization")
        request.httpBody = body

        let (responseData, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            throw UploadError.http
        }

        let envelope = try JSONDecoder().decode(UploadEnvelope.self, from: responseData)
        guard envelope.syscode == 200, let recordID = envelope.data else {
            throw UploadError.business(envelope.sysmsg)
        }
        return recordID
    }
}

struct UploadEnvelope: Decodable {
    let syscode: Int
    let sysmsg: String
    let data: String?
}

enum UploadError: Error {
    case http
    case business(String)
}
```

### 9.6 状态轮询

```swift
struct StatusPoller {
    func fetchStatuses(token: String, userID: String) async throws -> [AppleSourceStatus] {
        var components = URLComponents(string: "https://api.rq.run/Record/code_apple_source_status")!
        components.queryItems = [
            URLQueryItem(name: "token", value: token),
            URLQueryItem(name: "user_id", value: userID)
        ]
        var request = URLRequest(url: components.url!)
        request.setValue(token, forHTTPHeaderField: "X-USER-TOKEN")
        request.setValue("<APP_KEY>", forHTTPHeaderField: "Authorization")

        let (data, _) = try await URLSession.shared.data(for: request)
        let envelope = try JSONDecoder().decode(StatusEnvelope.self, from: data)
        return envelope.data
    }
}

struct StatusEnvelope: Decodable {
    let syscode: Int
    let sysmsg: String
    let data: [AppleSourceStatus]
}

struct AppleSourceStatus: Decodable {
    let appleHealthWorkoutID: String
    let fileExt: String
    let status: Int
    let remark: String
    let analyticMsg: String
    let createdAt: String

    enum CodingKeys: String, CodingKey {
        case appleHealthWorkoutID = "apple_health_workout_id"
        case fileExt = "file_ext"
        case status, remark
        case analyticMsg = "analytic_msg"
        case createdAt = "created_at"
    }
}
```

### 9.7 同步状态存储

建议表结构（SQLite/CoreData/Realm 均可）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `hk_uuid` | TEXT UNIQUE | `HKWorkout.uuid` |
| `state` | INTEGER | pending/preparing/uploading/waiting/success/failed |
| `server_record_id` | TEXT | 上传返回的记录 ID |
| `last_error` | TEXT | 错误 |
| `retry_count` | INTEGER | 重试次数 |
| `updated_at` | TIMESTAMP | 更新时间 |
| `payload_hash` | TEXT | 可选，检测同 UUID 内容变化 |

### 9.8 自动同步与后台

- `HKObserverQuery` 监听 workout 变化；
- `enableBackgroundDelivery(for: .workout, frequency: .immediate)`；
- 收到新 workout 后，创建后台任务；
- 上传使用 `URLSession` background configuration（可选）；
- 不要假设 iOS 会无限后台运行；必须有“下次启动补传”的队列。

---

## 10. 服务端实现

### 10.1 API 设计

| 方法 | 路径 | 作用 |
|---|---|---|
| POST | `/v1/apple-workouts` | 上传 `originalRecode.json`（兼容 multipart `file`） |
| GET | `/v1/apple-workouts/status?ids=...` | 批量查询解析状态 |
| POST | `/v1/apple-workouts/{id}/reprocess` | 重新解析 |
| GET | `/v1/apple-workouts` | 分页列表 |

如果要做 RQrun 兼容层，保留：

- `POST /Api/Record/upload_record`
- `GET /Record/code_apple_source_status`

### 10.2 数据库

```sql
CREATE TABLE apple_workouts (
  id              BIGSERIAL PRIMARY KEY,
  user_id         BIGINT NOT NULL,
  hk_uuid         UUID NOT NULL,
  activity_type   INT,
  start_time      TIMESTAMPTZ,
  end_time        TIMESTAMPTZ,
  distance_m      DOUBLE PRECISION,
  duration_s      DOUBLE PRECISION,
  calories_kcal   DOUBLE PRECISION,
  avg_hr          DOUBLE PRECISION,
  source_bundle   TEXT,
  source_name     TEXT,
  raw_payload     JSONB NOT NULL,
  status          SMALLINT NOT NULL DEFAULT 0,
  error_message   TEXT,
  retry_count     INT NOT NULL DEFAULT 0,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, hk_uuid)
);

CREATE INDEX idx_apple_workouts_status ON apple_workouts(status);
CREATE INDEX idx_apple_workouts_start ON apple_workouts(user_id, start_time DESC);
```

状态建议：

| 值 | 含义 |
|---|---|
| 0 | 待处理 |
| 1 | 处理中 |
| 2 | 成功 |
| 3 | 失败 |

> RQrun 的 `status = 1` 表示成功；自研接口不要混用，明确文档。

### 10.3 幂等

```sql
INSERT INTO apple_workouts (user_id, hk_uuid, raw_payload, status)
VALUES ($1, $2, $3, 0)
ON CONFLICT (user_id, hk_uuid)
DO UPDATE SET
  raw_payload = EXCLUDED.raw_payload,
  updated_at = now()
RETURNING id, status;
```

策略选择：

- **First-write-wins**：适合 HealthKit 记录不可变；重复上传直接返回已有 ID。
- **Last-write-wins**：适合用户修正记录；需要版本号/哈希。
- **只允许一次**：重复上传返回 409 或已有 ID。

建议：**默认 first-write-wins；只有显式“重新上传”才更新。**

### 10.4 解析流水线

```
接收 multipart
  → 大小/内容类型校验
  → 解析 JSON
  → 校验必填字段
  → 幂等检查
  → 写 raw_payload
  → 入队异步任务
  → 解析轨迹/心率/功率/分段
  → 写明细表
  → 更新 status / analytic_msg
```

### 10.5 校验

- `apple_health_workout_id` 必须是 UUID；
- `start_time < end_time`；
- `total_distance >= 0`；
- 经纬度范围：lat [-90, 90]，lon [-180, 180]；
- 数组长度上限（例如 200k 点）；
- JSON 大小上限（例如 5 MB）；
- 每用户上传频率限制；
- 拒绝未知/超大字段，避免 JSON 炸弹。

### 10.6 安全

- 不要使用硬编码 `Authorization` 作为安全边界；
- 使用 OAuth2/JWT + 短期 access token；
- 不要把 token 放 URL；用 `Authorization: Bearer`；
- TLS + 证书校验；
- 健康数据加密存储，日志脱敏；
- 明确隐私政策、授权、删除/导出机制；
- 对 GPS 轨迹做访问控制，避免还原用户住址。

---

## 11. 边界情况与失败处理

| 场景 | 现象 | 处理 |
|---|---|---|
| 用户拒绝 HealthKit 读权限 | 查询为空/错误 | 显示“未授权或无数据”，引导设置，不崩溃 |
| 设备锁屏 | HealthKit 受保护数据不可用 | 等解锁后重试；记录 `errorProtectedDataUnavailable` |
| 室内跑 | 无 route | `map_position_list = []`，其余字段正常 |
| 无心率 | 心率数组空 | `avg_heart_rate = 0` 或 0.0，`get_hear_auth` 写原因 |
| 无功率/步幅/触地/垂直 | 旧设备/旧系统 | 空数组 + `get_*_auth` 原因 |
| workout 刚结束 | route 尚未完整 | 延迟重试/下次同步补全 |
| 重复上传 | 同一 UUID | 本地/服务端幂等，返回已有记录 |
| 上传超时但服务端已收到 | 客户端重试可能重复 | 先查 status，再决定是否重传 |
| 服务端 500/429 | 上传失败 | 指数退避；429 尊重 Retry-After |
| token 过期 | 401 | 刷新 token 后重试 |
| 大文件 413 | 1 小时以上 | 压缩/分片/提高限制（需服务端支持） |
| JSON 解析失败 | status 失败/remark | 保存原始 payload，支持重新解析 |
| 用户删除 HealthKit 记录 | 本地已上传但 HealthKit 无 | 不自动删除云端；可标记“源已删除” |
| 时区/DST | 时间偏移 | 所有时间用 Unix 秒；`upload_time_zh` 仅展示 |

---

## 12. 与 RQrun 的差异和复刻建议

### 12.1 可以直接复刻的

- `apple_health_workout_id` 作为幂等键；
- 原始明细 JSON，而不是只传聚合；
- multipart `file` + `originalRecode.json`；
- 上传后按 UUID 查询状态；
- 每指标独立读取、独立错误；
- 本地记录已上传 UUID；
- 待上传/已上传分页 UI。

### 12.2 不应照搬的

| RQrun 做法 | 问题 | 建议 |
|---|---|---|
| `Authorization` 硬编码应用密钥 | 可被提取，不能当安全边界 | 用标准 OAuth/JWT |
| status 接口 token 放 URL | 进入日志/历史 | 放 Header |
| token 长期不变 | 泄漏风险 | 短期 token + refresh |
| 明文 JSON 无签名 | 可篡改/重放 | HMAC + nonce + TLS |
| 单次 640KB multipart | 移动网络成本 | gzip/分片/断点续传 |
| 轮询状态 | 费电 | WebSocket/推送 + 轮询兜底 |
| 全选只选跑步/游泳 | 产品限制 | 按产品需求配置 |

### 12.3 推荐的产品流程

1. 首次进入健康页：请求 HealthKit 读权限；
2. 拉取最近 N 天 workout；
3. 排除已上传 UUID；
4. 列表展示“可上传”；
5. 用户勾选/全选；
6. 逐条上传，显示进度；
7. 上传成功 → 等待解析 → 状态成功；
8. 失败项可重试/重新上传；
9. 设置页提供“自动同步”开关（可选）。

---

## 13. 限制与未验证项

1. **3.7.2 只抓了一次户外跑步**；游泳、骑行、室内跑、力量训练字段可能不同。
2. **3.7.2 主二进制加密**，无法静态确认字段来源；字段映射多为抓包 + HealthKit API 对照。
3. **3.5.8 只做了静态分析**，没有 3.5.8 的请求抓包；上传循环、筛选逻辑来自反编译，部分 Swift 闭包无法还原。
4. `add_record`、`get_apple_health_workouts`、`get_uploaded_apple_health_workouts` 的调用点未全部解析；不能断言它们一定参与 Apple 跑步上传。
5. 服务端解析规则、失败码、限流、去重策略未知。
6. `status` 非 1 的取值未验证。
7. `contact_time_list` / `vertical_list` 的单位转换是根据 HealthKit 原始单位推断，需用真实 payload 核对。
8. `cadences_list` 的生成方式未确认（无公开 RunningCadence 类型）。
9. `apple_workout_plan` 的来源未确认，可能是 WorkoutKit/HealthKit metadata。
10. 本文不包含任何真实 token、应用密钥、用户 ID、GPS 坐标。

---

## 14. 附录

### 14.1 抓包复现

```bash
python3 -m venv ~/tools/mitm-venv
~/tools/mitm-venv/bin/pip install --only-binary=:all: mitmproxy==12.3.1
~/tools/mitm-venv/bin/mitmweb \
  --listen-host 0.0.0.0 --listen-port 8080 \
  --web-host 127.0.0.1 --web-port 8081 \
  -w ~/Downloads/rq.flows
```

iPhone：

1. Wi-Fi → 配置代理 → Mac IP:8080；
2. Safari 打开 `http://mitm.it` 安装证书；
3. 设置 → 通用 → 关于本机 → 证书信任设置 → 信任 mitmproxy；
4. 打开 RQrun → 健康同步 → 勾选记录 → 上传。

### 14.2 原始证据

| 内容 | 标识 |
|---|---|
| 3.7.2 HAR | `cd85acea501d6576eef17b1d79c66cf5b17dbd4179a017292c9023b821815b7a` |
| 3.7.2 IPA | `2fbe1ebd08cec2f71c1ba67a3555db93d601f246b4c5f71cdd40eb2eb748893e` |
| 3.5.8 解密二进制 | `6bcc8acb42a3fbcfb4e356e17eae5fc4d06d97e8a55204437effce01c701310a` |
| 3.5.8 Ghidra 项目 | `/Users/tp/rqrun-ipad/ghidra-test/rqproj` |
| 3.5.8 脚本 | `/Users/tp/rqrun-ipad/ghidra-scripts` |
| 旧协议文档 | `/Users/tp/rai/docs/RQrun-HealthKit-上传协议.md` |

### 14.3 字段拼写黑名单

对接时不要“纠正”这些拼写：

| RQrun 字段 | 常见误写 |
|---|---|
| `workout_evnet_list` | `workout_event_list` |
| `get_hear_auth` | `get_heart_auth` |
| `activities_list[].avg_heart` | `avg_heart_rate` |
| `originalRecode.json` | `originalRecord.json` |
| `isReamin`（3.5.8 代码） | `isRemain` |

### 14.4 术语

| 术语 | 含义 |
|---|---|
| HKWorkout | HealthKit 一次运动记录 |
| HKWorkoutRoute | 运动路线 |
| HKQuantitySample | 带时间区间和单位的样本 |
| HKWorkoutEvent | 运动事件/分段 |
| HKAnchoredObjectQuery | 增量查询 |
| `apple_health_workout_id` | HKWorkout UUID，上传幂等键 |
| `originalRecode.json` | RQrun 上传的原始明细 JSON |

---

*文档生成：2026-11-08 · 证据截至 RQrun 3.7.2 抓包与 3.5.8 解密二进制静态分析*
