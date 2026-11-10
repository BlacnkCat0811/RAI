# RAI V2 iPhone 12 Pro Max 模拟器验收结果

2026-10-30，Asia/Shanghai。仅使用合成数据；未读取真实 HealthKit、生产数据库或凭证。

## 状态

```text
SIMULATOR ACCEPTANCE: PARTIAL
DEBUG BUILD / INSTALL / LAUNCH: PASS
iOS UNIT TESTS: 14/14 PASS
UI FLOWS: 7 PASS, 1 BLOCKED BY TEST ENVIRONMENT PRECONDITION
REAL DEVICE / HEALTHKIT / PRODUCTION: NOT VERIFIED
```

设备：`RAI iPhone 12 Pro Max`，型号 `iPhone 12 Pro Max` (`iPhone13,4`)，iOS 26.5 (23F77)，UDID `D44B6925-BB19-4556-B8AE-94B758B3B532`。Xcode 26.6 (17F113)。UI 验收连接 fnOS 上一次性隔离 PostgreSQL 18.6、真实 API 和真实 Worker；数据为合成数据。

## 实际结果

| 项目 | 结果 |
|---|---|
| Debug Simulator 构建 | PASS |
| 安装、启动 | PASS |
| `RAITests` 单元测试 | 14 项通过，0 失败 |
| UI test01 导航/截图/诊断入口 | PASS，28.947s |
| UI test02 编辑、409、改期、跳过、添加、执行、反馈 | PASS，118.666s |
| UI test03 首页滚动与性能采集 | PASS，47.148s |
| UI test04 无障碍布局与文本裁切审计 | BLOCKED，环境前置条件未满足 |
| UI test05 截图候选 | PASS，37.932s |
| UI test06 调整历史分页 | PASS，34.805s |
| UI test07 pending 原命令重试/重启 | PASS，58.249s |
| UI test08 空计划 pending 锁定 | PASS，29.793s |

合成 P6 Swift 验证同时通过：seed、home real API、丢失 ACK、409 冲突、重算保留 override、执行后反馈。完整机器证据见 [results JSON](evidence/simulator-acceptance-2026-10-30.json)。

## 测试修正与失败留痕

- UI test02 首轮在 iPhone 12 Pro Max/iOS 26.5 上失败：`app.swipeDown()` 命中 iOS 26.5 zoom 详情的交互式下滑关闭，页面退回首页，`session.move` 不存在。改为返回后重新点击 `home.hero`，再继续改期；产品源码未改。修正后 test02 通过，随后 test05 通过。
- test08 首轮因复用上一合成 dataset 的本地 ProductJournal 绑定而显示「产品配置暂不可用」，提交未进入 pending。卸载 App、清除旧容器后，在全新空计划 dataset 上通过。该失败属于测试隔离问题，不是业务验收通过。
- test04 连续四次尝试分别设置 `ReduceTransparencyEnabled`、`AXReduceTransparency`、Accessibility 开关并重启模拟器，App 实际读取仍为 `transparency=false`（`motion=true`，`type=accessibility5`），因此测试在审计前即失败；没有执行文本裁切结论，也没有把它记作 App 无障碍失败或通过。该模拟器环境门禁仍需在可正确切换 Reduce Transparency 的模拟器/环境重跑。
- test03 的 7.575s 平均时钟与 66.7 MB 平均物理内存是 XCTest 自动化口径，包含 UI 驱动开销，不是产品性能 SLO 或真机结论。
- 旧 iPhone 12 Pro Max 截图基线来自不同日期的合成数据；本次候选截图又出现已知模拟器合成黑图，故快照像素比较 **NOT ACCEPTED**，不宣称视觉回归通过。

## 未验收

真实 iPhone/HealthKit 授权与读数、GPS/后台/强杀恢复、真实 TLS/生产入口、iOS 18 runtime、Liquid Glass/GPU/帧率、完整无障碍裁切矩阵、Release 归档和签名安装均未验证。
