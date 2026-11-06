# Workout 同步提速第二轮 — 证据索引

对应交付文档：`docs/WORKOUT_SYNC_SPEED_ROUND2_RESULT.md`。
执行环境：本机 Mac（AMD Ryzen 5 3600X，12 逻辑核，macOS 26.7.2）、Go 1.27.3、PostgreSQL 18.6（`.local/toolchain`）、Swift 6.3.3、Xcode（`~/Downloads/Xcode.app`）。
所有数字均在本机实跑产生；未执行项在文档 §8 与 §9 中显式标注 NOT RUN / NOT VERIFIED。

## 目录结构

```
server/            Go 服务端：性能入口、集合化写入候选、严格 JSON
ios-capture/       iOS 捕获侧：捕获/ACK 边界、规范化捕获、有界并发、门禁映射
ios-transport/     iOS 传输侧：gzip-only 上传
lead/              跨域门禁与最终校验（full gate 日志）
```

## server/

| 文件 | 内容 |
| --- | --- |
| `README.md` | 服务端证据总览：入口用法、fixture、口径 |
| `r0-syncperf.md` | R0 性能入口说明（普通构建、真实 TCP→handler→PG、独占性快照、A/B 策略开关） |
| `r0-baseline-syncperf.log` | 基线（受污染窗口，仅存档，不用于验收） |
| `r0-after-arrays-syncperf.log` | arrays 策略首测（受污染窗口，仅存档） |
| `r0-final-syncperf.log` | **验收用**：独占窗口、n=30、0 失败，S60/S60H/S240 全量分段 |
| `r0-tail-diagnostic.log` | 长尾归因（WAL/FPI/ckpt_sync、identity_checks 与 autoanalyze） |
| `r0-s240-failure-probe.log` | S240 历史 503 探针与 statement_timeout 错误码复现 |
| `exclusive-ab-identity.log` | 三写策略同入口 A/B 对照（values / arrays / copy→staging） |
| `r5-write-candidates.md` | R5 候选说明：设计、权限、回滚、结论 |
| `r5-candidate-compare.log` | 三候选 S60 对照原始输出 |
| `r5-bare-pg-ceiling.log` | 裸 PG 天花板探针（同 schema/索引/durability，绕开 HTTP） |
| `r5-explain.log` | 计划与索引使用证据 |
| `r5-gates.log` / `r5-gates-parity.log` | R2-F21–F27 门禁运行输出 |
| `r5-race-gate.log` | R5 相关测试 `-race` 运行输出 |
| `r5-existing-suite-race.log` | 既有 bundle 套件 `-race` 回归 |
| `strict-json.md` | 严格 JSON 解析：两遍扫描 → 单遍 + schema 缓存，拒绝矩阵与语义规则 |

## ios-capture/

| 文件 | 内容 |
| --- | --- |
| `R1-capture-ack-boundary.md` | R2-F07：迟到 sample / 子删除 / 父删除的 pre-fix 复证与修复（CaptureManifest） |
| `R2-normalized-capture.md` | R2-F01/F02/F03、segment format v2、`sample_archive_calls=0`，以及「R2-P01 口径与差距归因」小节 |
| `R3-bounded-concurrency.md` | R2-F10–F15、`CaptureGate`/`CaptureWriter`、1/2/4 实测数据与默认值依据 |
| `gates.md` | 门禁逐项映射表（含 e2e、swiftclient 真实 PG、F08 两种锁顺序）+ 实际命令与输出摘要 |

## ios-transport/

| 文件 | 内容 |
| --- | --- |
| `README.md` | R4 gzip-only：改动点、能力协商、计数器、测试与残留 PARTIAL |

## lead/

| 文件 | 内容 |
| --- | --- |
| `full-gate.log` | 完成门禁 `scripts/local/test.sh full` 完整输出（go mod verify / race / vet / build / sqlc diff / 全量隔离 PG） |

## 复现要点

```bash
# Go 服务端性能（独占机器，普通构建）
scripts/local/test.sh syncperf --profiles S60,S60H,S240 --iterations 30 --warmups 3

# Go 完成门禁（含全量隔离 PG）
scripts/local/test.sh full

# iOS 包测试（各自 scratch，避免并发链接冲突）
scripts/local/test.sh swift --scratch-path .local/swift-build/capture

# Swift 客户端 → 真实 HTTP → 真实隔离 PG
scripts/local/test.sh integration -tags=integration,swiftclient -count=1 \
  -run '^TestWorkoutReliabilitySwiftClientPostgres$' ./integration

# 模拟器端到端（完整性 v2 两代）
scripts/local/test.sh e2e
```

注意：
- 性能测量必须独占机器（`SYNCPERF_MACHINE exclusive=` 必须为 `true`）；与 PG 集成测试或 Xcode 构建并行会产生不可用数字。
- 每个 PG 运行结束须确认 `RAI_TEST_LEFTOVER_DATABASES=0` 与 `RAI_TEST_CLUSTER_REMOVED=1`。
- `integration` profile 在参数含 `swiftclient` 时会自动导出 `DEVELOPER_DIR`（本轮修复的脚本缺陷，见结果文档 §6）。
