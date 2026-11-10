# RQrun iOS 端 HealthKit → 云端 上传协议

> 本文档基于对 **RQrun iOS 3.7.2（Build 4）** 的真实运行抓包整理，目标是说明：
> **iPhone 端如何把 Apple Watch 写入 HealthKit 的跑步数据导入并上传到云服务器。**
>
> - 抓包日期：2026-10-31
> - 抓包对象：`cn.runningquotient.RQ-Run` 3.7.2
> - 抓包方式：mitmproxy（HTTPS 解密）+ REA `inspect_web_network_capture` 解析
> - HAR SHA-256：`cd85acea501d6576eef17b1d79c66cf5b17dbd4179a017292c9023b821815b7a`
> - IPA SHA-256：`2fbe1ebd08cec2f71c1ba67a3555db93d601f246b4c5f71cdd40eb2eb748893e`
> - REA Evidence：抓包 `ev_c1a28919…2fb0`；IPA 清单 `ev_cd34a2a1…7a06`

---

## 目录

1. [总体结论](#1-总体结论)
2. [整体链路](#2-整体链路)
3. [认证与公共请求头](#3-认证与公共请求头)
4. [接口清单](#4-接口清单)
5. [上传接口详解](#5-上传接口详解)
6. [上传 JSON 完整字段表](#6-上传-json-完整字段表)
7. [嵌套结构字段表](#7-嵌套结构字段表)
8. [状态查询接口](#8-状态查询接口)
9. [数据源（授权）接口](#9-数据源授权接口)
10. [完整时序](#10-完整时序)
11. [自研 App 实现映射](#11-自研-app-实现映射)
12. [数据量与性能](#12-数据量与性能)
13. [安全与隐私](#13-安全与隐私)
14. [证据、限制与未验证项](#14-证据限制与未验证项)
15. [附录：抓包复现步骤](#15-附录抓包复现步骤)

---

## 1. 总体结论

RQrun **不是**在后台静默同步 HealthKit，而是：

> **用户在 App 里主动进入「健康同步选择」页面（`HealthUploadSelectVC`）勾选记录 → App 读取该条 HKWorkout 及其全部关联样本 → 组装成一个原始 JSON → 以 multipart 上传 → 服务端异步解析 → App 轮询导入状态。**

三个关键事实：

| 事实 | 证据 |
|---|---|
| 一次上传一条运动记录，约 **641 KB**（40 分钟跑步） | `POST /Api/Record/upload_record`，请求体 641,476 B |
| 载荷是**完整原始明细**（逐秒轨迹、逐点心率/功率/步频），不是聚合结果 | `map_position_list` 2283 点、`heart_rates_list` 456 点、`power_list` 887 点 |
| 幂等键是 **HealthKit 的 `HKWorkout.uuid`** | 字段 `apple_health_workout_id` |

---

## 2. 整体链路

```
┌─────────────────────────┐
│ Apple Watch（原生体能训练）│
└───────────┬─────────────┘
            │ 自动同步
            ▼
┌─────────────────────────┐
│ iPhone 健康 App / HealthKit │  HKWorkout + HKWorkoutRoute + HKQuantitySample
└───────────┬─────────────┘
            │ RQrun 读取（用户手动触发）
            ▼
┌─────────────────────────┐
│ RQrun：组装 originalRecode.json │  约 40 个顶层字段
└───────────┬─────────────┘
            │ multipart/form-data（字段名 file）
            ▼
   POST https://api.rq.run/Api/Record/upload_record
            │
            ▼
   {"syscode":200,"sysmsg":"上传成功","data":"<记录ID>"}
            │
            ▼
   GET /Record/code_apple_source_status   ← 轮询每条记录的解析状态
```

### 2.1 关键判定

- 上传参数 `upload_page = "HealthUploadSelectVC"`：说明入口是**健康同步选择页**，由用户勾选后上传。
- 载荷含 `get_data_error`（各指标读取失败原因）与 `get_*_auth`（各指标授权状态）：说明 App 是**逐项从 HealthKit 读取**并对失败/无权限做降级。
- 载荷含 `apple_record_source_bundleIdentifier` / `apple_record_source_name` / `apple_watch_device`：说明 App 记录数据**来源设备**，用于区分"自家 Watch App 录的"与"Apple 原生体能训练录的"。

---

## 3. 认证与公共请求头

### 3.1 普通 API（表单）

```http
POST /Api/User/get_user_info HTTP/1.1
Host: api.rq.run
Content-Type: application/x-www-form-urlencoded; charset=utf-8
Accept: */*
Accept-Language: zh-Hans-CN;q=1.0
Authorization: <应用级固定密钥>
X-USER-TOKEN: <用户 token>
Cookie: think_language=zh-Hans-CN

os_version=27.2&phone_brand=iPhone&phone_model=iPhone13%2C4&request_version=2&token=<用户 token>&user_id=<用户ID>
```

| 凭据 | 位置 | 说明 |
|---|---|---|
| **用户 token** | 请求头 `X-USER-TOKEN` **且** body `token` | 登录态，长期有效（观察期内未变） |
| 用户 ID | body `user_id` | 数字 ID |
| 应用级密钥 | 请求头 `Authorization` | **写死在客户端里的固定值**，所有请求相同 |
| 语言 | `Cookie: think_language=zh-Hans-CN` | 服务端 Laravel/ThinkPHP 风格 |

> ⚠️ 本文档中的 token、应用密钥、邮箱、手机号、设备名均已脱敏。真实值请从你自己的抓包中读取，不要外泄。

### 3.2 上传 API（multipart）

```http
POST /Api/Record/upload_record HTTP/1.1
Host: api.rq.run
Content-Type: multipart/form-data; boundary=alamofire.boundary.<随机串>
Authorization: <应用级固定密钥>
X-USER-TOKEN: <用户 token>
User-Agent: RQ Run/3.7.2 (cn.runningquotient.RQ-Run; build:4; iOS 27.2.0) Alamofire/5.0.0
```

**注意**：上传接口的 body 里 **不包含** `token` 和 `user_id`，身份完全依赖 `X-USER-TOKEN` 头。

### 3.3 服务端响应约定

统一信封：

```json
{ "syscode": 200, "sysmsg": "上传成功", "data": ... }
```

- `syscode = 200` 表示业务成功
- `sysmsg` 为中文提示
- `data` 为业务载荷（字符串 / 对象 / 数组 / null）

---

## 4. 接口清单

本次抓包实际命中的 RQrun 接口（全部 `api.rq.run`，HTTPS）：

| 方法 | 路径 | 响应 | 作用 |
|---|---|---|---|
| POST | `/Api/Tools/log_version` | 200 | 版本检查 / 客户端上报 |
| POST | `/Api/Tools/check_user` | 200 | 登录态校验 |
| POST | `/Api/User/get_user_info` | 200 | 用户信息、单位/偏好设置 |
| POST | `/Api/User/get_attribute_home_v3` | 200 | 首页属性 |
| POST | `/Api/WatchAuth/auth_list` | 200 | **数据源授权列表（含 Apple 健康）** |
| POST | `/Api/Index/runner` | 200 | 首页跑步概览 |
| POST | `/Api/Profile/get_profile_info` | 200 | 个人资料 |
| POST | `/Api/Mission/main` | 200 | 任务/活动 |
| POST | `/Api/Msg/new_msg_count` | 200 | 未读消息数 |
| POST | `/Api/Event/find_events` | 200 | 赛事列表 |
| POST | `/Api/GroupForum/get_user_gt_config` | 200 | 社区配置 |
| POST | `/Api/Spreadimg/get_spread_view` | 200 | 推广位 |
| POST | `/Api/Spreadimg/spreadimg_type_list` | 200 | 推广类型 |
| **POST** | **`/Api/Record/upload_record`** | 200 | **上传运动记录（核心）** |
| **GET** | **`/Record/code_apple_source_status`** | 200 | **查询 Apple 健康记录导入状态** |

> 说明：`/Record/...` 与 `/Api/...` 是两套前缀，服务端可能由不同模块/网关处理。

---

## 5. 上传接口详解

### 5.1 请求

```http
POST https://api.rq.run/Api/Record/upload_record
Content-Type: multipart/form-data; boundary=alamofire.boundary.d97b328352617af1
Authorization: <应用级固定密钥>
X-USER-TOKEN: <用户 token>
User-Agent: RQ Run/3.7.2 (cn.runningquotient.RQ-Run; build:4; iOS 27.2.0) Alamofire/5.0.0

--alamofire.boundary.d97b328352617af1
Content-Disposition: form-data; name="file"; filename="originalRecode.json"
Content-Type: text/plain

{"get_map_auth":"","get_cycle_power_auth":"骑行功率未授权或无数据", ... }
--alamofire.boundary.d97b328352617af1--
```

**要点：**

| 项 | 值 |
|---|---|
| HTTP 方法 | `POST` |
| Content-Type | `multipart/form-data`（Alamofire 生成 boundary） |
| 表单字段名 | **`file`**（固定） |
| 文件名 | **`originalRecode.json`**（固定） |
| 文件 MIME | `text/plain`（虽然内容是 JSON） |
| 编码 | UTF-8，未压缩（明文 JSON） |
| 单次大小 | 40 分钟跑步 ≈ 641 KB |
| 失败重试 | 未观察到（建议实现方自行加重试 + 幂等） |

### 5.2 响应

```json
{ "syscode": 200, "sysmsg": "上传成功", "data": "113467563" }
```

`data` 为服务端生成的**记录 ID**（字符串形式）。

失败形态未在本次抓包中出现（未验证）。

---

## 6. 上传 JSON 完整字段表

顶层共 **40 个字段**（一条户外跑步记录实测）。

### 6.1 标识与来源

| 字段 | 类型 | 说明 | 实测值（脱敏） |
|---|---|---|---|
| `apple_health_workout_id` | string | **HKWorkout 的 UUID**，幂等键 | `8CEA80AE-FC92-470A-95EA-D01F11D53866` |
| `apple_record_source_bundleIdentifier` | string | 数据来源的 bundle id | `com.apple.health.<UUID>` |
| `apple_record_source_name` | string | 来源设备显示名 | `<用户的 Apple Watch>` |
| `apple_watch_device` | object | 设备信息（见 7.4） | — |
| `app_version` | string | RQrun 版本 | `3.7.2(4)` |
| `ios_version` | string | 系统版本 | `27.2` |
| `upload_time_zh` | string | 上传时间（本地时区） | `2026-10-31 16:58:57` |
| `upload_page` | string | 触发上传的页面 | `HealthUploadSelectVC` |

### 6.2 运动概要

| 字段 | 类型 | 单位/取值 | 实测值 |
|---|---|---|---|
| `sport` | int | 运动大类，`1` = 跑步 | `1` |
| `sport_name` | string | 运动名 | `跑步` |
| `activity_name` | string | 细分名称 | `户外跑步` |
| `activity_type` | int | **HKWorkoutActivityType**，`37` = running | `37` |
| `indoor_workout` | int | `0` = 户外，`1` = 室内 | `0` |
| `start_time` | int | Unix 秒 | `1788957364` |
| `end_time` | int | Unix 秒 | `1788959645` |
| `total_time` | int | **秒** | `2280`（38 分钟） |
| `total_distance` | int | **米** | `6140` |
| `total_calories` | int | **千卡** | `374` |
| `total_ascent` | int | **米**（累计爬升） | `3` |
| `avg_heart_rate` | float | **bpm** | `166.07` |
| `swimming_stroke_count` | int | 游泳划水次数（跑步为 0） | `0` |

### 6.3 明细序列

| 字段 | 类型 | 点数（40 分钟跑步） | 说明 |
|---|---|---|---|
| `map_position_list` | array | 2283 | GPS 轨迹点（见 7.1） |
| `heart_rates_list` | array | 456 | 心率采样（见 7.2） |
| `time_distance` | array | 884 | 累计距离-时间曲线（见 7.3） |
| `power_list` | array | 887 | 跑步功率（瓦特） |
| `cadences_list` | array | 887 | 步频（步/采样区间） |
| `stride_length_list` | array | 423 | 步幅（米） |
| `contact_time_list` | array | 428 | 触地时间（秒） |
| `vertical_list` | array | 432 | 垂直振幅（米） |
| `activities_list` | array | 1 | 分段活动（见 7.5） |
| `workout_evnet_list` | array | 11 | 工作段事件（拼写为 `evnet`，见 7.6） |

### 6.4 计划、天气、授权与诊断

| 字段 | 类型 | 说明 |
|---|---|---|
| `apple_workout_plan` | object | Apple WorkoutKit 的训练计划（见 7.7） |
| `weather_temperature` | float | 气温 ℃（`29.06`） |
| `weather_humidity` | int | 湿度 %（`69`） |
| `get_map_auth` | string | 地图权限；空串 = 有权限 |
| `get_power_auth` | string | 功率权限；空串 = 有权限 |
| `get_vertical_auth` | string | 垂直振幅权限 |
| `get_hear_auth` | string | 心率权限（**注意拼写是 `hear` 不是 `heart`**） |
| `get_step_auth` | string | 步数权限 |
| `get_contact_time_auth` | string | 触地时间权限 |
| `get_stride_length_auth` | string | 步幅权限 |
| `get_cycle_power_auth` | string | 骑行功率权限（示例：`"骑行功率未授权或无数据"`） |
| `get_step_count` | string | 步数（跑步时为空） |
| `get_data_error` | object | 各指标读取错误详情（见 7.8） |

> `get_*_auth` 为**空字符串表示已授权/可用**；非空为中文原因说明。

---

## 7. 嵌套结构字段表

### 7.1 `map_position_list[]` — 轨迹点

| 字段 | 类型 | 单位 | 示例 |
|---|---|---|---|
| `position_latitude` | float | 十进制度 | `23.29116333312206` |
| `position_longitude` | float | 十进制度 | `113.59628012693923` |
| `altitude` | float | 米 | `43.16220143530518` |
| `speed` | float | m/s | `6.22456933174405` |
| `timestamp` | int | Unix 秒 | `1788957365` |
| `sec` | int | 采样间隔（秒） | `1` |

### 7.2 `heart_rates_list[]` — 心率点

| 字段 | 类型 | 单位 | 示例 |
|---|---|---|---|
| `heart_rate` | int | bpm | `92` |
| `start_time` | int | Unix 秒 | `1788957366` |
| `end_time` | int | Unix 秒 | `1788957366` |

### 7.3 `time_distance[]` — 累计距离曲线

| 字段 | 类型 | 单位 | 示例 |
|---|---|---|---|
| `startTime` | int | Unix 秒 | `1788957365` |
| `endTime` | int | Unix 秒 | `1788957367` |
| `distance` | float | **米（累计）** | `6.789339212351479` |

### 7.4 `apple_watch_device` — 设备信息

```json
{
  "name": "Apple Watch",
  "model": "Watch",
  "hardwareVersion": "Watch6,2",
  "manufacturer": "Apple Inc.",
  "softwareVersion": "26.1"
}
```

### 7.5 `activities_list[]` — 分段活动

| 字段 | 类型 | 说明 | 示例 |
|---|---|---|---|
| `workout_type` | int | HKWorkoutActivityType | `37` |
| `start_time` | float | Unix 秒（带小数） | `1788957364.587287` |
| `end_time` | float | Unix 秒（带小数） | `1788959645.362057` |
| `duration` | float | 秒 | `2280.7747700214386` |
| `total_distance` | float | 米 | `6141.281994336401` |
| `meters_per_second` | float | 平均速度 m/s | `2.6612969523356518` |
| `avg_heart` | float | 平均心率 | `166.59537173352962` |
| `avg_power` | float | 平均功率 W | `192.55806087936867` |
| `total_step_count` | int | 步数 | `6417` |
| `location_type` | int | `3` = 户外 | `3` |
| `event_list` | array | 段事件（见 7.6） | — |

### 7.6 事件 `event_list[]` / `workout_evnet_list[]`

| 字段 | 类型 | 说明 | 示例 |
|---|---|---|---|
| `startTime` | float | Unix 秒 | `1788957364.587287` |
| `endTime` | float | Unix 秒 | `1788957761.6195564` |
| `type` | int | 事件类型，`7` = 跑步段 | `7` |

> 11 个事件段对应 11 个跑步分段。

### 7.7 `apple_workout_plan` — 训练计划

```json
{
  "display_name": "",
  "workout_type": "goal",
  "goal_data": { "goal_type": "", "goal_value": "", "goal_unit_name": "" }
}
```

### 7.8 `get_data_error` — 读取诊断

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

> 说明 App 对每个指标做**最多 3 次重试**，并记录失败原因（例如无对应样本、谓词无匹配数据）。这是很好的工程参考：**单项失败不应导致整条记录上传失败**。

---

## 8. 状态查询接口

```http
GET /Record/code_apple_source_status?token=<用户 token>&user_id=<用户ID> HTTP/1.1
Host: api.rq.run
X-USER-TOKEN: <用户 token>
Authorization: <应用级固定密钥>
```

响应（`data` 为数组，实测 85 条历史）：

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

| 字段 | 类型 | 说明 |
|---|---|---|
| `apple_health_workout_id` | string | 对应上传时的 HKWorkout UUID |
| `file_ext` | string | `json` / `wjson`（后者疑似 watch 端上传） |
| `status` | int | `1` = 成功（其他取值未验证） |
| `remark` | string | 结果描述 |
| `analytic_msg` | string | 解析/分析结果 |
| `created_at` | string | 服务端记录时间 |

> 用途：App 上传后轮询此接口，把"解析失败"反馈给用户；也用于**避免重复上传**（已成功的 UUID 不再上传）。

---

## 9. 数据源（授权）接口

```http
POST /Api/WatchAuth/auth_list
```

请求：`os_version`、`phone_brand`、`phone_model`、`request_version=2`、`token`、`user_id`

响应关键字段：

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

| 字段 | 说明 |
|---|---|
| `provider_id = 8` | **苹果运动健康（HealthKit）** |
| `provider_id = 10 / 15` | Garmin 国际 / 中国 |
| `def_source` | 默认数据源 = 8（Apple） |
| `can_sync` | 是否支持"云端同步"；Apple 为 `0`，说明 Apple 数据**必须由手机端本地读取后上传**，无法服务端直连拉取 |

**这一条是架构上的关键**：Apple 健康数据没有服务端 API，只能由 iOS 客户端读 HealthKit 再上传——这也正是你自己的 App 必须做的事。

---

## 10. 完整时序

```
App 启动
 ├─ POST /Api/Tools/log_version        版本检查
 ├─ POST /Api/Tools/check_user         登录态校验
 ├─ POST /Api/User/get_user_info       用户信息
 ├─ POST /Api/WatchAuth/auth_list      数据源授权列表
 └─ ... 首页若干接口

用户进入「健康同步选择」页（HealthUploadSelectVC）
 ├─ 读 HealthKit：HKWorkout + 关联样本 + 路线
 ├─ 用户勾选要导入的记录
 └─ POST /Api/Record/upload_record     multipart，file=originalRecode.json
      ← {"syscode":200,"data":"113467563"}

上传后
 └─ GET /Record/code_apple_source_status?token=…&user_id=…
      ← 每条 workout 的 status / remark / analytic_msg
```

---

## 11. 自研 App 实现映射

你要做的：**Apple Watch 原生体能训练 → HealthKit → 你的服务器**。下表把 RQrun 的字段映射到 HealthKit API。

### 11.1 HealthKit 读取

| HealthKit | 用途 | 对应上传字段 |
|---|---|---|
| `HKWorkout`（`HKWorkoutType`） | 一次跑步 | `start_time` / `end_time` / `duration` / `total_distance` / `total_calories` / `activity_type` |
| `HKWorkout.uuid` | 唯一标识 | `apple_health_workout_id` |
| `HKWorkout.sourceRevision.source.bundleIdentifier` | 来源 | `apple_record_source_bundleIdentifier` |
| `HKDevice`（`sourceRevision.device`） | 设备型号 | `apple_watch_device` |
| `HKQuantityTypeIdentifierHeartRate` | 心率 | `heart_rates_list` / `avg_heart_rate` |
| `HKWorkoutRoute` + `HKWorkoutRouteQuery` | GPS 轨迹 | `map_position_list` |
| `HKQuantityTypeIdentifierRunningPower` | 跑步功率 | `power_list` / `avg_power` |
| `HKQuantityTypeIdentifierRunningSpeed` | 速度 | `meters_per_second` / `time_distance` |
| `HKQuantityTypeIdentifierStepCount` | 步数 | `total_step_count` |
| `HKQuantityTypeIdentifierDistanceWalkingRunning` | 距离 | `total_distance` |
| `HKQuantityTypeIdentifierRunningStrideLength` | 步幅 | `stride_length_list` |
| `HKQuantityTypeIdentifierRunningGroundContactTime` | 触地时间 | `contact_time_list` |
| `HKQuantityTypeIdentifierRunningVerticalOscillation` | 垂直振幅 | `vertical_list` |
| `HKMetadataKeyWeatherTemperature` / `…Humidity` | 天气 | `weather_temperature` / `weather_humidity` |
| `HKWorkoutEvent`（`HKWorkoutEventTypeSegment` 等） | 分段 | `workout_evnet_list` / `event_list` |

### 11.2 推荐查询方式

```swift
// 1. 增量拉取（推荐，避免每次全量扫）
let predicate = HKQuery.predicateForSamples(withStart: lastSyncDate, end: nil)
let query = HKAnchoredObjectQuery(
    type: HKObjectType.workoutType(),
    predicate: predicate,
    anchor: savedAnchor,           // 持久化 HKQueryAnchor
    limit: HKObjectQueryNoLimit
) { _, samples, deleted, newAnchor, error in
    // samples: [HKWorkout]
    // 保存 newAnchor
}
```

```swift
// 2. 后台感知新数据
let observer = HKObserverQuery(type: HKObjectType.workoutType(), predicate: nil) { ... }
healthStore.enableBackgroundDelivery(for: .workout, frequency: .immediate) { success, error in }
```

```swift
// 3. 取轨迹
let routeQuery = HKWorkoutRouteQuery(route: route) { _, locations, done, error in
    // locations: [CLLocation] → map_position_list
}
```

```swift
// 4. 取逐点样本（心率等）
let sampleQuery = HKSampleQuery(
    sampleType: HKQuantityType(.heartRate),
    predicate: HKQuery.predicateForObjects(from: workout),
    limit: HKObjectQueryNoLimit,
    sortDescriptors: [NSSortDescriptor(key: HKSampleSortIdentifierStartDate, ascending: true)]
) { ... }
```

### 11.3 你该设计的接口（对应 RQrun 的做法）

| 设计点 | RQrun 的做法 | 建议 |
|---|---|---|
| 上传粒度 | 一条 workout 一个请求（~640KB） | 保持；大文件可 gzip |
| 幂等 | `apple_health_workout_id` | 服务端唯一索引 + `ON CONFLICT DO NOTHING` |
| 触发 | 用户在"健康同步选择"页手动勾选 | 可加"自动同步"开关（`HKObserverQuery`） |
| 状态 | 上传后轮询 `code_apple_source_status` | 用 WebSocket/推送替代轮询更省电 |
| 失败降级 | `get_data_error` 记录每项失败原因 | 单项失败不影响整条记录 |
| 鉴权 | 头 `X-USER-TOKEN` | 用标准 Bearer + 短期 token + refresh |
| 载荷 | 原始明细 JSON | **不要先聚合**，服务端重算才灵活 |

### 11.4 服务端建议表结构

```sql
CREATE TABLE workouts (
  id            BIGINT PRIMARY KEY,
  user_id       BIGINT NOT NULL,
  hk_uuid       UUID NOT NULL,          -- HKWorkout.uuid，幂等键
  activity_type INT,
  start_time    TIMESTAMPTZ,
  end_time      TIMESTAMPTZ,
  distance_m    DOUBLE PRECISION,
  calories_kcal DOUBLE PRECISION,
  avg_hr        DOUBLE PRECISION,
  raw_payload   JSONB,                  -- 原始上传 JSON
  status        SMALLINT DEFAULT 0,     -- 0待处理 1成功 2失败
  created_at    TIMESTAMPTZ DEFAULT now(),
  UNIQUE (user_id, hk_uuid)
);
```

---

## 12. 数据量与性能

以一次 40 分钟户外跑步为例：

| 指标 | 数值 |
|---|---|
| 上传体积 | **641,476 B（约 626 KB）** |
| 轨迹点 | 2,283（约 1 点/秒） |
| 心率点 | 456 |
| 功率点 | 887 |
| 步频点 | 887 |
| 时间-距离点 | 884 |
| 步幅点 | 423 |
| 触地时间点 | 428 |
| 垂直振幅点 | 432 |
| 分段事件 | 11 |

估算：

- 1 小时跑步 ≈ **0.9–1.0 MB**
- 每周 5 次 ≈ 5 MB/周，**250 MB/年/人**（未压缩）
- gzip 后通常可降到 **15–25%**（JSON 数字重复度高）

---

## 13. 安全与隐私

| 问题 | 观察 | 建议 |
|---|---|---|
| token 出现在 URL | `GET /Record/code_apple_source_status?token=…` | 会进入服务器日志/浏览器历史，**不要照抄** |
| 应用级密钥硬编码 | `Authorization: <固定值>` | 客户端密钥可被提取，**只能当标识，不能当安全边界** |
| 明文 HTTP 接口 | 部分接口走 `http://`（如 `116.62.154.166:8888`） | 必须全站 HTTPS |
| 无签名/防重放 | 请求体未见签名或时间戳 | 建议加 HMAC + nonce |
| 健康数据敏感 | 轨迹可还原家庭/工作地点 | 加密存储、脱敏展示、明确授权 |

> 本次抓包文件（`rq.har` / `rq.flows`）包含**你的真实健康数据与登录 token**，建议分析完成后删除。

---

## 14. 证据、限制与未验证项

### 14.1 证据

| 内容 | 标识 |
|---|---|
| HAR 文件 | `cd85acea501d6576eef17b1d79c66cf5b17dbd4179a017292c9023b821815b7a` |
| IPA 文件 | `2fbe1ebd08cec2f71c1ba67a3555db93d601f246b4c5f71cdd40eb2eb748893e` |
| REA 抓包 Evidence | `ev_c1a289197d343b1e27a3c0bb414b012ed03e9f2a2f08368b4616933b88ad2fb0` |
| REA IPA 清单 Evidence | `ev_cd34a2a1b218319e3e703fa701e0cf4ebc06b9da63602ab967ec98c001057a06` |
| 抓包记录数 | 78 条（其中 `api.rq.run` 19 条） |

### 14.2 限制

1. **只覆盖一次户外跑步**。骑行、游泳、室内跑步的字段集合可能不同（如 `swimming_stroke_count`、`get_cycle_power_auth` 表明了其它运动类型的存在，但未实测）。
2. **iOS 二进制被 FairPlay 加密**（`LC_ENCRYPTION_INFO cryptid=1`），无法静态反编译，因此：
   - 字段来源只能靠"抓包 + 与 HealthKit API 对照"推断
   - `event_list.type = 7`、`sport = 1`、`location_type = 3` 等枚举值的语义是**推断**
3. **服务端行为未知**：解析规则、去重策略、失败重试、限流均未验证。
4. **未验证错误码**：只观察到 `syscode = 200`；非 200 的响应结构未知。
5. 抓包未包含登录流程（使用已登录会话），token 获取方式未知。

---

## 15. 附录：抓包复现步骤

环境：Mac（Intel/Apple Silicon 均可）+ iPhone 同一局域网。

```bash
# 1. 安装 mitmproxy（Python 3.12）
python3 -m venv ~/tools/mitm-venv
~/tools/mitm-venv/bin/pip install --only-binary=:all: mitmproxy==12.3.1

# 2. 启动代理（监听 8080，Web UI 8081，写入 flows 文件）
~/tools/mitm-venv/bin/mitmweb \
  --listen-host 0.0.0.0 --listen-port 8080 \
  --web-host 127.0.0.1 --web-port 8081 \
  -w ~/Downloads/rq.flows
```

iPhone：

1. 设置 → 无线局域网 → 当前 Wi-Fi ⓘ → 配置代理 → 手动 → `Mac 的 IP` : `8080`
2. Safari 打开 `http://mitm.it` 安装证书（或从 `http://<Mac IP>:8000/mitmproxy-ca-cert.cer` 直接下载）
3. 设置 → 通用 → 关于本机 → 证书信任设置 → 打开 mitmproxy
4. 打开 RQrun → 进入健康同步 → 勾选记录上传

导出 HAR（本仓库使用的转换逻辑）：

```python
from mitmproxy.io import FlowReader
import json
entries = []
with open('/Users/tp/Downloads/rq.flows','rb') as f:
    for fl in FlowReader(f).stream():
        r = fl.request
        # ... 组装 HAR entry（method/url/headers/postData/response）
```

用 REA 解析：

```
inspect_web_network_capture(
  capture_path="/Users/tp/Downloads/rq.har",
  format="har",
  detail="summary"
)
```

---

## 附：字段拼写注意事项

RQrun 的字段名有几处**非标准拼写**，对接时不要"纠正"：

| 实际字段 | 常见误写 |
|---|---|
| `workout_evnet_list` | `workout_event_list` |
| `get_hear_auth` | `get_heart_auth` |
| `activities_list` 内 `avg_heart` | `avg_heart_rate` |
| `valueStr`（功率/步幅/触地/垂直） | `value` |

---

*文档生成时间：2026-10-31 · 基于 RQrun iOS 3.7.2 实测抓包*
