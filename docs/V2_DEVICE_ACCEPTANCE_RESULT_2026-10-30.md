# RAI V2 iPhone 12 Pro Max Device Acceptance Result

2026-10-30，Asia/Shanghai。用户最后明确表示真机已不在，要求更新文档并结束本次工作；已停止设备操作。本轮完成了部分真实同步、结构盘点和最小修复，**没有完成最终真机验收**。

## 最终状态

```text
DEVICE_ACCEPTANCE_BASE_COMMIT=1783517900520ed6aedc3c97ef497bc8fe0ef5ca
MAINTENANCE 2xP2 + 3xP3: CLOSED WITH SUPPLEMENTAL CLIENT EVIDENCE
DEVICE ACCEPTANCE: NOT COMPLETED — DEVICE UNAVAILABLE AT WRAP-UP
P3 REAL SAMPLE INGEST / DURABLE ACK: PARTIAL VERIFIED
P3 INITIAL / INCREMENTAL / ROUTE / DELETE / FAULT CLOSURE: NOT VERIFIED
P4/P5 REAL WORKOUT CLOSED LOOP: NOT VERIFIED
P6 DEBUG DEVICE INSTALL / LAUNCH: PASS
P6 PRODUCT / LIQUID GLASS GPU ACCEPTANCE: NOT VERIFIED
TLS DEVICE ACCEPTANCE: NOT RUN — USER SELECTED SCOPED DEBUG HTTP
PRODUCTION RELEASE READY: NO
```

## Git 基线、已有工作与范围

用户给定 `/Users/ta/rai` 不存在；使用实际 `/Users/tp/rai`（环境亦写作 `/Users/tp/raI`），main。开始已执行 pwd、两种 status、branch、HEAD、log -10、remote 和 diff stat。

入口已有 `docs/ARCHITECTURE.md`、`docs/FNOS-ACCESS.md`、`docs/IMPLEMENTATION.md`、`scripts/test-remote.sh` 修改，以及 maintenance 报告/JSON、ios-signing、local-xcode-build 证据目录。期间另有图标工作修改 Assets.xcassets、project.pbxproj、project.yml 并新增 ios-app-icon 证据；这些不是本任务实施，不覆盖或归为本轮修复。当前 dirty tree 不等于本轮全部改动；没有 commit/push。

16份指定合同/结果均存在并已阅读。原架构方案、migrations、历史 phase/review 结果及原 maintenance 证据共30个保护文件，收尾与初始指纹比较均未变化。未 reset/clean、修改旧 `../running`、查询旧 `rai-postgres`、迁移真实旧数据、切旧入口或正式发布。

## Maintenance gate

`1783517` 已包含五项修复。原 [Maintenance Result](V2_FINAL_MAINTENANCE_FIX_RESULT_2026-10-30.md) 的 P2-2 PARTIAL 保留为当时真实结论；本轮补齐其客户端门禁。

| Finding | 本轮结论 |
|---|---|
| P2-1 mixed sample amplification | 当前实现与完整隔离 PG/race 回归通过；FIXED |
| P2-2 pending stale payload | Swift48项、Debug Simulator、Release/min18、CreatePlan及SessionEditor实际后端UI补证；FIXED |
| P3-1 training wire | 完整隔离PG/race包含HTTP边界矩阵；FIXED |
| P3-2 chunk upload lease retry | 完整隔离PG/race包含lease/replay矩阵；FIXED |
| P3-3 documentation drift | 原版本修复保留，本次状态与历史延期分开；FIXED |

两项UI均合成数据、独立模拟器、真实隔离API/Worker/PG：test08 30.252s、test07 59.085s，各1项0失败。原 `.any` 保存控件查询在签名修正后仍报告 enabled；使用 `app.buttons[...]` 精确查询后通过，断言预期和pending业务逻辑未放宽。它们不能代替HealthKit真机验收。

## 保留的 Real Device Acceptance 环境

| 项目 | 实际值 |
|---|---|
| Host / directory | rai-fnos / `/home/tpandmr/rai-v2-device` |
| Compose project | `rai-v2-device` |
| Database container / DB | `rai-v2-device-postgres-1` / `rai_v2`，独立新cluster |
| Roles | `rai_owner`、`rai_api_login`、`rai_worker_login`，另有bootstrap/backup/monitor |
| Persistent volume | `rai-v2-device_pgdata`，未删除，不属于测试清理 |
| API / Worker | `rai-v2-device-api-1` / `rai-v2-device-worker-1` |
| Current image | 二者同为 `rai-v2-device:device-dynamics` |
| Current local image config digest | `sha256:3e0292bf9073c80fe09cb886e30fa1f97db40b32f4c3b1d60ee48a727ab8fb85` |
| Server source | 基线HEAD + 明确记录的running-dynamics补丁，source-patch SHA256见JSON；不是干净HEAD镜像 |
| Build | fnOS Go1.27.3，CGO0、trimpath、scratch；无registry push，RepoDigests为空 |
| Schema | 从当前HEAD实际读取13个migration；全部applied，与frozen manifest及远端文件SHA256逐项相同 |
| PG settings | PostgreSQL18.6；fsync/full_page_writes/synchronous_commit/data_checksums=on |
| Readiness | `/health`200为进程存活；`/ready`200为DB/schema；Worker实际运行且worker-ready成功 |
| Privacy | 三服务logging driver=none，避免既有Worker scope日志持久化完整HealthKit UUID；PG不记录语句/参数 |

初始镜像为 `:1783517`，中间为 `:device-gct`，随后更新为当前同镜像API/Worker。local image config digest不冒充已发布registry manifest digest。启动过程中修正了二进制权限、bootstrap角色脚本调用身份及脚本stdin执行方式；没有删除/重建volume或再次创建dataset。

收尾时三服务均healthy；数据库、配置和秘密目录保留，方便下次接续。没有运行 `docker compose down -v`、清空Anchor/Outbox或重建数据集。

## 真机、网络与实际提交证据

实际连接iPhone12ProMax，iOS27.2，开发者模式enabled、wired/tunnel connected。Xcode26.6（17F113）、Swift6.3.3、iOS26.5 SDK，Debug真机签名构建、安装、启动均成功。设备序列号/完整标识不进入Git。

用户选择 `http://192.168.10.25:18087` 的现有精确主机Debug通道；未增加全局ATS放行、Trust All或关闭TLS验证。手机Last ACK及服务端真实事实证明可达，HTTPS/TLS未验收。

| 收尾聚合快照 | 数量 |
|---|---:|
| datasets | 1 |
| samples | 6000 |
| registered/committed batches | 24 / 24 |
| pages/sealed pages | 24 / 24 |
| workouts / routes / associations | 0 / 0 / 0 |
| analysis publications | 0 |
| training plans / overrides / executions / feedback | 0 / 0 / 0 / 0 |

47个 `analysis.sample` 任务为依赖传播任务；45个run中44 SUCCEEDED、1 SUPERSEDED。这不是Workout分析或健康评分发布证明。样本ACK、原待提交第8批恢复及后续计数增长是部分设备证据，不能据此宣布初始可见历史全部导入、稳定增量、删除/网络故障或完整端到端通过。

## 完整真实跑步结构盘点

用户要求先完整检查一条实际跑步，随后在手机主动执行Debug只读结构检查。已直接取回 [脱敏结构报告](evidence/device-workout-structure-2026-10-30.json)，没有读取同步Anchor或推进Outbox。

- Workout及唯一子活动各有10类统计：活动能量、基础能量、距离、心率、触地时间、功率、速度、步幅、垂直振幅、步数。保存了聚合种类的存在性，不导出其测量值。
- 顶层与子活动各有8个segment事件；不据事件时长之和重写Workout。
- 1条手机可见route，声明1768个点；未读取GPS坐标，服务端尚无sealed route。
- 6个metadata键：HKTimeZone、HKIndoorWorkout、HKAverageMETs、HKElevationAscended、HKWeatherHumidity、HKWeatherTemperature。后4项在盘点时未被原Workout映射保留。
- 6类已请求类型有可见关联样本；基础能量与3类跑步动态查询返回code5，不能解释为不存在。查询上限1001样本/100路线，此记录均未触顶。
- preferredDisplayUnit在首份报告中均为null；canonicalUnitInBuild是代码映射，不冒充HealthKit单位偏好读取证明。

此报告完整列出所选Workout对象结构；关联查询有界，不是整库数据dump，也不是全量时序/坐标导出。只保留计数、时长、字段/类型、状态及SHA256指纹。路线sync metadata尚无上传字段，不据此声称复制了整个HealthKit对象。后来新增的实际Metal设备探测尚未取回第二份报告，故Metal runtime/trace仍NOT VERIFIED。

## 最小修复与未验证部分

| 项目 | 实现及证据 | 真机剩余项 |
|---|---|---|
| 配置保存诊断 | 去除复制行尾空白；稳定origin/collector/token错误和数字Keychain OSStatus；绿色成功提示；用户确认保存成功 | 原笼统错误原因未建立，不能断言原来一定是某字段或Keychain值 |
| 跑步动态统计 | Swift/Go显式支持触地时间ms、步幅m、垂直振幅m；保留sum/average/minimum/maximum；未知类型一次列出并停止 | 最终三类型合并修复未完成手机重试 |
| Workout metadata | 已观察4项通过既有JSON `{value,unit}` 保存；单位为kcal/(kg*hr)、m、1、degC；未知Workout/event/activity键或不兼容单位停止 | 未证明真实Workout成功提交这些metadata |
| 原始时序读取范围 | 基础能量、触地时间、步幅、垂直振幅加入只读授权及原有stream/association机制；旧6条stream身份/anchor保持 | 用户收尾前未确认新增4项授权，未取回授权后报告 |
| 结构检查 | Debug限定、只读、脱敏文件；不写HealthKit或同步状态 | 仅一条真实跑步盘点，不覆盖全部历史来源 |

独立sample既有metadata映射不重写，避免已提交不可变样本身份冲突；不把新增Workout metadata支持称为全类型/全metadata复制。没有新增算法、migration、队列、协议版本、客户端、WorkoutKit、Watch或灾难恢复实现。P4缺个人参数时仍unavailable，不制造默认HRmax/resting HR或评分。

## 回归范围与失败保留

| 门禁 | 实际证据 |
|---|---|
| Swift package | 新scratch path：48 tests / 6 suites通过；包核心后续未修改 |
| iOS最终定向单元 | 14项0失败，包括单位、配置、Workout metadata保留、未知键/错误单位拒绝及脱敏 |
| Pending UI | test08和test07均通过，范围如上 |
| 最后Debug设备/Release generic iOS | 均构建通过，保持min18/strict concurrency；Debug实际安装启动，Release未签名编译 |
| Go最终源码 | `go test -race -count=1 ./...`、`go vet ./...`、249包在线/纯analysis/training依赖边界通过 |
| 完整隔离PG/race | 基线复跑493.906s；GCT修复阶段499.397s，均完整check-p6.sh通过 |
| 最新running-dynamics/metadata隔离PG/race | 最终定向用例2.817s，通过原receipt重放、3类统计/6项metadata原样保留、既有analysis发布和delete→旧replay屏障 |

最新Stride/Vertical/metadata补丁后运行了完整race单元/vet/边界与定向真实PG；**没有再宣称整套PG已在最终补丁上重跑**。当前源码与build/log指纹见JSON。

保留失败：首次完整PG/race的5年查询SQLSTATE57014 timeout（513.831s），原样重跑通过但原因未确立；旧raI/rai Swift缓存重复模块导致Clang崩溃，改scratch后通过；未签名Simulator Keychain失败，改本地签名；`.any`按钮查询断言失败，精确Button查询后通过。无跳过失败或放宽生产超时来取得PASS。

通用secret guard命中已有 `scripts/test-p5-compose.sh` 的合成凭证URI，该文件与HEAD相同，不在本轮改动范围；未把这次扫描写作PASS。另行逐字节检查当前验收token未进入仓库。没有复制原始私有日志、截图/视频、完整健康数据、GPS、HR samples、凭证、Keychain secret或DB dump进Git。

## 收尾与下次入口

[机器证据](evidence/device-acceptance-2026-10-30.json) 保存基线、初始dirty状态、环境/镜像/schema、文件指纹、分阶段测试、失败及聚合快照。旧报告和原方案保留。

已清理本任务合成host/proxy/tunnel/私有配置和自建模拟器；两个host源码目录及带本次label的测试PG容器均确认不存在。第一次host的重复清理信号返回1，因为它早已清理；事后独立核验源目录和容器均不存在。未触及用户原模拟器、其他图标工作或持久验收project/volume。

下次有设备后沿用现有身份、Keychain、数据库和Outbox：先完成新增4类只读授权及再次脱敏检查，再Retry Saved Work/有界Sync；闭合真实Workout与route/association，验证增量不重增及安全删除/网络/后台恢复；随后验证P4 publication和P5目标、override、frozen execution、显式Workout关联、feedback及再生成。Metal/Liquid Glass/Charts、内存/帧率、无障碍及TLS仍需各自证据。不得用合成PASS替代这些门禁。

**本次工作已按用户要求结束；DEVICE ACCEPTANCE未完成，不是PASS。**

## 2026-10-31 接续环境

用户重新连接 iPhone 后选择保留 fnOS 服务端数据集，仅把隔离后端升级到当前工作区源码。`rai-v2-device:device-dynamics` 已由 `rai-v2-device:workout-detail-20261031` 替代（旧镜像保留），PostgreSQL、volume、dataset、设备 token 摘要和计数未变；API/Worker 重启后 healthy，详情接口 `GET /v2/workouts/{id}/detail` 返回 200。检查发现手机 App 当时绑定的是本机已重置的旧数据集 `8847a338…`，不是 fnOS；经用户确认已卸载重装并清除旧本地状态，需手工填入 fnOS 的采集器/令牌后连接。真实 HealthKit、详情 UI、后台恢复和 TLS 仍未验证，DEVICE ACCEPTANCE 状态不变。证据见 [device-backend-2026-10-31.json](evidence/device-backend-2026-10-31.json)。
