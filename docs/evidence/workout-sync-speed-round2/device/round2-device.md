# 第二轮真机验收 — 原始聚合结果

日期：2026-11-06（Asia/Shanghai）
设备：iPhone 12 Pro Max（iPhone13,4），iOS 27.2，USB wired+paired。
构建：Release（`SWIFT_ACTIVE_COMPILATION_CONDITIONS=DEBUG` 仅打开验收仪器；`Info-Debug.plist` 仅用于本机 HTTP host 精确放行）。
后端：本机 Go API/Worker + 隔离 PostgreSQL 18.6（loopback only），端口 55434/18082；每次首导入前 `reset` 新建一次性 dataset。
隐私：以下只保留时长、字节数、计数、RSS、状态与哈希前缀；**不含**健康值、GPS、Workout UUID、设备标识、凭证或原始 metadata。Workout 以 `W32-A` / `W58-A` 别名表示。
样本：`n=20` nearest-rank P50/P95；S60H 的 n=20 = `s60h-s2` 15 次 + USB 断连后补测 `s60h-s2b` 5 次。每组独立冷启动；同机同 LAN 顺序测量。

## 1. 首导入时延与资源

| 指标 | S60（W32-A，4,330,151 B） | S60H（W58-A，8,565,743 B） |
| --- | ---: | ---: |
| workout_sync_ms P50 | 8,182.27 | 18,274.23 |
| workout_sync_ms P95 | 11,260.71 | 24,637.76 |
| workout_sync_ms min / max | 7,607.42 / 11,379.07 | 15,155.59 / 25,122.66 |
| healthkit_read_ms P50 | 4,995.42 | 11,558.97 |
| healthkit_read_ms P95 | 7,382.12 | 17,203.48 |
| bundle_build_ms P50 | 5,422.13 | — |
| upload_ms P50 / P95 | 2,732.79 / 3,128.38 | — |
| gzip_ms P50 (max) | 39.81 (61.04) | 94.5 (138.13) |
| processPeakRssBytes max | 206.5 MiB | 302.34 MiB |
| mainThreadMaxStallMs max | 4 | 6.6 |
| mainThreadStallsOver250 | 0 | 0 |

判定：R2-P03 **FAIL**（P50 1.64× / P95 1.41× 门槛）；R2-P04 **FAIL**（P50 3.65× / P95 3.08×）；R2-P12 S60 PASS / S60H FAIL。

## 2. 并发 1/2/4（S60H）

| streams | n | workout_sync_ms P50 | P95 | min / max | RSS max |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 10 | 17,638.31 | 17,897.27 | 17,352.08 / 17,897.27 | 297.81 MiB |
| 2 | 20 | 18,274.23 | 24,637.76 | 15,155.59 / 25,122.66 | 302.34 MiB |
| 4 | 10 | 20,194.13 | 21,302.32 | 18,479.96 / 21,302.32 | 291.61 MiB |

2 流比 1 流慢 3.6%，4 流比 1 流慢 14.5%；无 ≥20% 收益。顺序测量存在热漂移，但 4 流的劣化与队列/内存压力方向一致。R2-P02 **PARTIAL**。

## 3. 无变化重复（R2-P06）

`--no-reset`，同一 W32-A，9 次连续 launch：

- 9/9 `SINGLE_WORKOUT_ACKNOWLEDGED`，9/9 `workout_sync_skipped_unchanged=1`。
- report `wallMs`：426.7 / 356.7 / 353.0 / 364.1 / 368.3 / 347.3 / 359.4 / 380.3 / 397.1 ms → P50 **368.3 ms**、P95（nearest-rank）**426.7 ms**。
- API 日志窗口内 **0 次** `POST /v2/sync/workout-bundles`；无导入事务，因此无新 import/job。
- 注意：report `timings` 中的 `workout_sync_ms`/`healthkit_read_ms` 是首次真实同步遗留的持久 metrics，skip 路径不覆盖，不能用作 P06 时延。R2-P06 **PASS**。

## 4. 已备原包恢复（R2-P07）

1. 代理在转发前拒绝首次 `POST /v2/sync/workout-bundles`：wire **483,260 B**，`forwarded=false`，HTTP 503；App 报告 `BLOCKED`/`unexpected_http_status`，wallMs **7,025.99**，任务保留 bundle（4,330,151 B，SHA256 `7cffa820…`），无 receipt。
2. 第二次直接连 API 启动：**SINGLE_WORKOUT_ACKNOWLEDGED**，wallMs **2,907.97**，receipt 校验通过；任务 phase 进入 ANALYZING，bundleFile 清除，receipt bundleSHA256 与失败前一致。
3. DB：workouts=1、workout_imports=1、health_samples=5,464、workout_samples=5,464、route_points=1,768。被拒绝的 POST 没有提交；恢复只产生 1 次 gzip 导入。
4. 恢复路径未重新读取 HealthKit：`healthkit_read_ms` 仍是失败前的 5,090.02 ms（未覆盖），且 2.9 s 的端到端时间不足以重新采集/压缩 8.5 MB bundle。没有显式 `healthkit_query_count=0` 字段，故以 hash 一致 + 旧 metrics 未变 + 时延作行为证据。

gzip 比例：483,260 / 4,330,151 = **11.2%**；全部成功 POST 均为单 gzip file part，明文 fallback 0。R2-P07/R2-P11 **PASS**。

## 5. API RSS 与 UI（R2-P13）

- 单槽 S60H 上传：API baseline ≈10.5 MiB，峰值 **104.8 MiB**（311 个 50 ms 样本），限 512 MiB。
- 设备主线程：S60 max stall 4 ms、S60H 1.1–6.6 ms；>250 ms 次数 0。
- 双槽尝试：同一 device token 两个并发重放得到 `1×200 + 1×429 device_upload_capacity_exceeded`，未形成真正双槽同时解析/导入；因此只签单槽余量（4.9×），双槽并发 **NOT VERIFIED**。

## 6. 批量（R2-P14）

- 20 个 S60 首导入中前 10 次求和 **80,219.01 ms** > 60 s → 10× **FAIL**。
- 设备仅 51 条 eligible 记录，无法凑 100 条不重复首导入；100× **NOT RUN**。

## 7. 复现命令

```bash
export RAI_LOCAL_DEVICE_RUN=$PWD/.local/device-acceptance-speed
export RAI_LOCAL_DEVICE_PG_PORT=55434 RAI_LOCAL_DEVICE_HTTP_PORT=18082
export RAI_LOCAL_DEVICE_CONFIGURATION=Release

python3 scripts/local/device.py prepare
python3 scripts/local/device.py install
python3 scripts/local/device.py probe
python3 scripts/local/device.py list

# 首导入（每次 reset；S60H 用 W58-A，S60 用 W32-A）
python3 scripts/local/device_sample.py --profile s60 --workout <private-id> --samples 20 --streams 2
python3 scripts/local/device_sample.py --profile s60h --workout <private-id> --samples 20 --streams 2

# 无变化重复（不 reset）
python3 scripts/local/device_sample.py --profile s60-repeat --workout <private-id> --samples 9 --no-reset

# 已备原包恢复
python3 scripts/local/device.py failbundle --workout <private-id>

# 单槽 API RSS（示例：先 reset，再读 run.json 的 apiPID，50ms 采样）
```

原始报告 JSON 留在本机 `.local/device-acceptance-speed/reports/`（Git 忽略）；本文件只保留聚合值。
