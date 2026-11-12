# RAI iOS 客户端

原生 SwiftUI iPhone App `RAI`（bundle `com.rai.v2.ios`，iOS 18+，Swift 6 完整并发检查）。只读 HealthKit，不写入、不删除健康数据。
同步契约见 [`contracts/sync-v6.md`](../../contracts/sync-v6.md)，接口见 [`docs/API.md`](../../docs/API.md)，整体设计见 [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md)。
所有开发与测试都在本机 Mac 执行，没有远端测试入口。

## 目录

| 路径 | 内容 |
| --- | --- |
| `App/` | App target：五个 Tab（首页、运动、训练、统计、我的）、HealthKit 读取、后台上传 |
| `Sources/SyncCore` | 同步核心：Workout protocol-6 打包、gzip multipart、outbox/上传日志、日常健康与 VO2 |
| `Sources/ProductCore` | 产品数据层：API 解码、统计、训练负荷、跑步能力等展示逻辑（无本地分析算法） |
| `Sources/RAICompression` | gzip 的 C 封装 |
| `Tests/` | SwiftPM 测试（Swift Testing），`swift test` 运行 |
| `AppTests/` | App 内 XCTest（需模拟器），含 HealthKit 合成数据转换与实时上传套件 |
| `UITests/` | XCUITest，见下文“UI 测试” |
| `Tools/CompareSnapshots.swift` | 比较两组同尺寸 PNG 截图（变化像素 ≤10%，通道均方误差 ≤12/255），不会自动更新基线 |

## 构建与测试

在仓库根目录用本机脚本（会设置 Xcode 与 Go 环境）：

```bash
scripts/local/test.sh swift          # SwiftPM 单测，可加 --filter
scripts/local/test.sh simulator      # RAITests（AppTests）合成数据 XCTest，专用本机模拟器
scripts/local/test.sh simupload      # 模拟器内真实 App 上传代码 → 本机 Go API + 隔离 PG18
```

只编译（含 UI 测试 target）：

```bash
xcodebuild build-for-testing -project ios/RAI/RAI.xcodeproj -scheme RAI \
  -destination 'generic/platform=iOS Simulator' CODE_SIGNING_ALLOWED=NO
```

- `simupload` 只上传合成 Workout：模拟器没有 HealthKit 数据，由进程内合成 reader 代替；其余协议构建、gzip、TCP、接收、投影和 Worker 分析都是真实的。它不等于真机或真实云端验收。
- 模拟器与未签名构建不能证明签名、安装、HealthKit 授权或真机后台上传。

## Xcode 工程

`RAI.xcodeproj` 已提交；`project.yml` 是 XcodeGen 描述，但本机未安装 xcodegen，**工程文件靠手工维护**：

- 在 `App/` 新增或删除 `.swift` 文件，需要在 `project.pbxproj` 补/删四处（`PBXBuildFile`、`PBXFileReference`、`App` 组、`RAI` target 的 Sources 阶段），可对照 `RAIApp.swift`。
- `AppTests/`、`UITests/` 同理，分别登记到 `RAITests`、`RAIUITests` 的 Sources 阶段。`WorkoutUploadLiveTests.swift` 若从 `RAITests` 里消失，实时上传套件会静默不再运行。
- `Sources/`、`Tests/` 属于 SwiftPM 包，无需改工程。
- 若将来重新用 XcodeGen 生成，`postGenCommand` 会调用 `scripts/fix-p35-capability.py` 修正 HealthKit capability 序列化。

## 签名与服务器配置

- 真机签名：把 `Config/Developer.xcconfig.example` 复制为被忽略的 `Config/Developer.xcconfig` 并填入 Apple Development Team。
- 连接服务器：在 App「我的 → 服务器与健康授权」粘贴 `scripts/connect-link.py` 生成的 `rai://` 链接，或手动填写服务器地址、设备标识与访问令牌；凭证存 Keychain（`AfterFirstUnlockThisDeviceOnly`），握手绑定 service/dataset/epoch/collector，之后不可更改。
- Release 必须 HTTPS。Debug 只对 `RAI_DEV_HTTP_HOST`（`Config/Debug.xcconfig`，默认 `rai-dev.local`）开放 HTTP，没有全局 ATS 豁免。仓库中不含个人 IP，凭证不入日志、不入库。

## 同步要点

- 一条跑步 = 一个 `WorkoutBundle` = 一次 multipart 上传，成功后由服务端回执确认；失败按原标识重试（指数退避，上限 60 秒，遵守 `Retry-After`）。新跑步结束约 1 分钟后自动上传，半小时后补传一次晚到数据。
- 上传日志与 outbox 在本地持久化；同设备同时最多两路 Workout 与一路日常健康上传，额度以握手公告为准。
- 日常健康（HRV、静息心率、睡眠）和外部 VO2 走 `daily-facts`、`vo2max-facts`，使用 HealthKit 锚点查询；删除（`deletedObjects`）照常上传，Workout 不做删除同步。
- 读取授权覆盖 Workout、路线和运行相关量类型；HealthKit 不暴露读取授权状态，界面只显示“已请求”。
- 统计日志只含计数、字节数、重试次数和耗时，不含负载、GPS、心率序列或凭证。

## UI 测试（`UITests/ProductUITests.swift`）

| 测试类 | 前置条件 | 状态 |
| --- | --- | --- |
| `SportStyleOfflineUITests` | 无需账号、后端或 HealthKit | 可独立运行，覆盖统计周期切换与分析页导航 |
| `WorkoutSyncScreenUITests` | 同上 | 可独立运行，覆盖「我的 → 跑步记录同步」页 |
| `ProductUITests` | 需要 `/tmp/rai-p6-native-acceptance/config-app.json` 与一个真实后端驱动（会改写计划、制造并发冲突、丢弃 ACK） | 驱动脚本 `scripts/archive/p6-client-host.sh` 依赖已退役的远端测试入口，**当前没有本机替代**，这组用例近期未实际运行 |
| `ModelStackLiveUITests` | 需环境变量 `RAI_MODEL_E2E_CONFIG`、`RAI_MODEL_E2E_CONTROL`；当前没有活跃脚本提供这套环境（仅见于 `docs/archive/`） | 缺环境时自动跳过，未验证 |

运行示例（`-parallel-testing-enabled NO`，选一个本机模拟器）：

```bash
xcodebuild test -project ios/RAI/RAI.xcodeproj -scheme RAI \
  -destination 'platform=iOS Simulator,id=<SIMULATOR_UUID>' \
  -only-testing:RAIUITests/SportStyleOfflineUITests -parallel-testing-enabled NO RAI_DEV_HTTP_HOST=127.0.0.1
```

`scripts/local/test.sh simulator` 只跑 `RAITests`，不含 UI 测试。UI 测试里的定位全部使用 `accessibilityIdentifier`；改界面时同步这些标识，避免只靠中文文案断言。
