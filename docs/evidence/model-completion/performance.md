# Performance status — NOT RUN

No 30-attempt, warmup-3, optimized-build benchmark was executed in this session. The quick/unit timings are not performance evidence.

Required before any performance PASS: ordinary optimized build; real loopback TCP with authentication → handler → PostgreSQL; current snapshot ≥30 attempts with n/P50/P95/max/RSS and SQL plans; 90-point history; 240-minute feature snapshot; VO₂/Recovery/Readiness model latency; no pool/timeout/slot/durability/fixture changes.

Status: `NOT RUN` (exitCode null in acceptance.json). The prior synchronization performance result is not transferred to v2.

## 2026-11-07 Model completion HTTP performance

Command (ordinary optimized integration build, no `-race`):

```text
RAI_MODEL_PERF=1 ./scripts/test-postgres.sh go test -count=1 -tags='integration modelperf' -p 1 -timeout=10m ./integration -run '^TestAthleteProductHTTPPerformance$' -v
```

Transport: `httptest.NewServer` loopback TCP; real Bearer authentication, `httpapi` handler, Store, isolated PostgreSQL 18.6 with `fsync=on`, `full_page_writes=on`, `synchronous_commit=on`, `statement_timeout=2s`; pool/timeout/durability unchanged. Warmup 3, measured attempts 30; requests paced to respect the existing 120/min read admission budget (pacing is outside the timed span).

| target | n | P50 | P95 | max | max SELECT/request | budget |
|---|---:|---:|---:|---:|---:|---|
| current schema2 snapshot | 30 | 1.53 ms | 2.33 ms | 2.52 ms | 2 | ≤6 SELECT, P95≤500ms |
| 90-point v2 trainingLoad history | 30 | 1.95 ms | 2.32 ms | 2.35 ms | 2 | ≤8 SELECT, P95≤750ms |

`MODEL_PERF` raw line is in `.local/logs/model-completion/performance-http.log`; allocated bytes 42,545,000 across both loops, Go 1.27.3, optimized=true. No failures were retried or hidden. F01/F02/F03/F05/F06/F09 PASS; F04/F08 PARTIAL (density and RSS/frontend not measured); F07/F10 NOT RUN.

Pure model P95 (optimized `go test`, n=30, same process, no I/O):

| model | P95 |
|---|---:|
| `rai.vo2max.submax/v1` Aggregate | 0.001 ms |
| `rai.recovery.physiological/v2` CalculateV2 | 1.465 ms |
| `rai.readiness.training/v2` CalculateV2 | 0.047 ms |

Budget 250 ms each; log `.local/logs/model-completion/performance-models.log`. F05 PASS. These are computational timings only, not physiological validation.

Feature snapshot matrix (optimized integration build, `RAI_M1_PERF=1`):

| minutes | load | calculation | total | artifact bytes | series points |
|---:|---:|---:|---:|---:|---:|
| 20 | 12.0 ms | 2.9 ms | 14.9 ms | 8,825 | 120 |
| 45 | 20.4 ms | 5.6 ms | 26.1 ms | 13,268 | 270 |
| 120 | 44.4 ms | 15.3 ms | 59.6 ms | 25,584 | 720 |
| 240 | 79.3 ms | 30.8 ms | 110.1 ms | 42,064 | 1,440 |

240min passes the 20s and 128KiB budgets, but the fixture does not reach the 250k-point density bound, so F04 remains PARTIAL; it is not inflated to PASS.


## F09 — 单 Workout 持久 ACK 回归（2026-11-07）

Command (optimized build, real loopback TCP/auth/handler/PG, 30 attempts, 3 warmups):

```text
RAI_SYNC_SPEED_PERF=1 RAI_SYNC_SPEED_ITERATIONS=30 RAI_SYNC_SPEED_WARMUPS=3 \
  bash scripts/local/test.sh integration -v -run '^TestWorkoutSyncSpeedServerAckMatrix/S60$' ./integration
```

Observed: exit 0, failures=0, ack_total P50=2977.739ms, P95=3932.603ms, max=3936.794ms.
Baseline: `docs/evidence/workout-sync-speed/server/20261106-sync-speed-server-ack-s60-s240.txt`, S60 P95=5646ms, old P06 FAIL retained.
Regression: -30.3% (improvement), within the ≤15% degradation budget. The durable ACK is committed before the independent completeness proof state machine; the measured ACK path does not wait for full-stream proof.
Log: `.local/logs/model-completion/integration-ack-s60-round17.log`.

## Round 29 update — dense F04 and F07

Command (optimized integration build, no race, real loopback PG):
`go test -count=1 -tags=integration,modelperf -run 'TestModelBackfill180DayPerformance|TestDenseFeatureSnapshotPerformance' ./integration`

- F04: 240 minutes / 250,000 points; n=30, P95=1.894872438 s, max=1.905399276 s, artifact=42,127 bytes; passes 20 s and 128 KiB budgets.
- F07: 180 days / 180 synthetic Workouts / 180 jobs / 6 blocks of 31 days; elapsed=22.522589121 s; candidate ready.
- Log: `.local/logs/model-completion-fixes/perf-model-norace-v.log`.
- F08/F10 remain NOT RUN; F09 remains over the legacy 1,000 ms S60 target and is not claimed as a pass.
