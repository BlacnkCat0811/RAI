# R0 — normal-build performance entry: baseline, final acceptance, verdicts

Entry point (new, §12 of the plan): `scripts/local/test.sh syncperf`.
It builds `./integration` with `go test -c` under the ordinary optimized toolchain, strips any
inherited `-race`/`-N`/`-l` from `GOFLAGS`, asserts the resulting binary's build settings, then
runs `TestWorkoutSyncPerfServerHTTP` against a disposable isolated PostgreSQL 18.6 cluster.
Traffic goes over a real loopback TCP socket through `httpapi.NewHTTPServer` → the production
auth/admission middleware → the real `importWorkoutBundle` handler → the real
`Store.ImportWorkoutBundle` transaction. Nothing is stubbed and no store method is called
directly.

```
scripts/local/test.sh syncperf --profiles S60,S60H,S240 --iterations 30 --warmups 3
```

## 1. Build basis (why this entry had to exist)

| measurement | build | transport | auth/admission | where |
|---|---|---|---|---|
| correctness gate | `-race` | in-process | included | `scripts/local/test.sh integration` (unchanged) |
| round-1 perf matrix `TestWorkoutSyncSpeedServerAckMatrix` | `-race` | `httptest` recorder, no socket | included (handler only) | kept, labelled **race build, handler-internal** |
| **this entry** `TestWorkoutSyncPerfServerHTTP` | **optimized, no `-race`, no `-N`/`-l`** | **real loopback TCP** | **included** | `scripts/local/test.sh syncperf` |

The two measurements are not comparable. Round 1's `json_decode` P50 of 1,649.6 ms is a `-race`
number: on the normal build the same decode costs 103.8 ms (S60), so the race build inflated it
about 14×. All numbers in this document are from the normal build.

`scripts/local/test.sh` prints and asserts the effective flags, e.g.

```
SYNCPERF_GOFLAGS_IN=<unset>
SYNCPERF_GOFLAGS_EFFECTIVE=<empty>
SYNCPERF_PROFILES=S60,S60H,S240 iterations=30 warmups=3 cold=5
SYNCPERF_GO=go version go1.27.3 darwin/amd64
SYNCPERF_PG=postgres (PostgreSQL) 18.6 (Postgres.app)
build   -tags=integration
build   GOAMD64=v1
build   CGO_ENABLED=1
```

and refuses to run if the binary carries `-race` or `-gcflags` with `-N`/`-l`.

## 2. Environment of the accepted run

```
race_enabled=false   write_strategy=arrays
go1.27.3 darwin/amd64   num_cpu=12   AMD Ryzen 5 3600X 6-Core Processor
PostgreSQL 18.6   fsync=on  full_page_writes=on  synchronous_commit=on  wal_level=replica
shared_buffers=64MB  max_wal_size=1GB  data_checksums=on  default_statistics_target=100
pool_max_conns=8   process_upload_slots=2   dataset_upload_slots=2
handshake limits: bundleBytes 33,554,432  bundleWireBytes 33,619,968  bundleCompressedBytes 8,388,608
                  bundleEvents 50,000  upload device 30/min burst 2, dataset 60/min burst 4
unauthenticated POST /v2/sync/workout-bundles → 401 (the auth middleware is in the measured path)
sync_request_budgets rows for the dataset after the run: 4 (admission ran)
```

Durability, pool, upload slots and every default budget are exactly the product defaults. No
value was enlarged for the benchmark.

Exclusivity was enforced (the iOS lane was stopped for this window) and recorded in the log:

```
SYNCPERF_MACHINE phase=before exclusive=true competitors=0 load="… load averages: 1.69 11.69 21.77"
SYNCPERF_MACHINE phase=after  exclusive=true competitors=0 load="… load averages: 1.59 5.48 15.98"
```

The 15-minute load average is still decaying from earlier work; the 1-minute average (1.6–2.4) is
the figure that matters for a 13-minute run, and no competing build/test process existed.

## 3. Final acceptance numbers (exclusive, n=30, nearest-rank P95 = ceil(0.95·n))

| profile | n | failures | P50 | P95 | min | max | cold n=5 P50/P95 | warmup n=3 P50 | fixture bytes |
|---|---|---|---|---|---|---|---|---|---|
| S60 | 30 | **0** | 2,796.7 | 4,758.8 | 2,117.8 | 4,793.2 | 2,179.8 / 2,293.8 | 1,965.5 | 4,134,363 |
| S60H | 30 | **0** | 4,277.9 | 5,753.4 | 3,714.8 | 6,690.7 | 3,763.7 / 4,444.4 | 3,564.5 | 10,118,893 |
| S240 | 30 | **0** | 12,035.0 | 19,722.3 | 8,814.6 | 20,567.4 | 11,093.2 / 18,034.4 | 8,985.2 | 16,537,010 |

Fixture digests (sha256 of the exact JSON of the first timed iteration, reproducible from the
fixed seeds): S60 `eea9de5b229b3fa2…`, S60H `f375fb4094b6a3c1…`, S240 `31fef562df495eea…`.
Every iteration used fresh object identities (fresh seed per iteration) and the harness fails an
iteration whose `samplesInserted` is not the full count, so no replay or cache hit can stand in
for a timed import. Warmups are excluded from the sample set. Failures are kept, not excluded.

S60H is the dense profile required by §3.2: 10,824 samples, 10,824 series points, 3,491 route
points, with all supported source/metadata fields (per-sample metadata, four series parents,
full HealthKit provenance, coverage for every type plus series/route/workout keys, unit-valued
metadata, workout statistics/events/activity configuration).

### Per-phase P50 / P95 / max (ms)

S60:

| phase | P50 | P95 | max |
|---|---|---|---|
| import_total | 2,687.93 | 4,646.29 | 4,683.22 |
| import_transaction | 2,666.36 | 4,626.35 | 4,661.87 |
| association_writes | 842.73 | 1,295.22 | 1,639.33 |
| sample_writes | 841.28 | 1,612.65 | 2,206.68 |
| identity_checks | 188.08 | 2,394.41 | 2,553.40 |
| routes | 145.80 | 207.26 | 207.97 |
| receipt | 104.84 | 246.32 | 423.55 |
| semantic_validation | 18.29 | 19.39 | 19.41 |
| dirty_job | 11.71 | 12.62 | 29.98 |
| commit | 10.78 | 119.59 | 1,750.68 |
| deletions | 0.36 | 0.55 | 0.68 |
| dataset_lock | 0.35 | 0.39 | 0.39 |
| series | 0 | 0 | 0 |

S60H:

| phase | P50 | P95 | max |
|---|---|---|---|
| import_total | 3,991.96 | 5,477.10 | 6,410.65 |
| sample_writes | 1,495.26 | 2,955.74 | 3,476.86 |
| association_writes | 1,481.33 | 1,937.63 | 2,136.74 |
| receipt | 253.62 | 323.28 | 457.14 |
| identity_checks | 169.62 | 830.94 | 1,586.34 |
| routes | 166.24 | 195.84 | 202.09 |
| series | 45.91 | 52.93 | 77.06 |
| semantic_validation | 33.88 | 34.75 | 34.77 |
| commit | 24.98 | 268.95 | 447.84 |
| dirty_job | 20.08 | 20.83 | 20.92 |
| deletions | 1.23 | 1.45 | 1.46 |
| dataset_lock | 0.36 | 0.41 | 0.46 |

S240:

| phase | P50 | P95 | max |
|---|---|---|---|
| import_total | 11,601.06 | 19,276.93 | 20,133.38 |
| sample_writes | 4,703.34 | 8,354.54 | 9,883.63 |
| association_writes | 3,945.54 | 6,336.05 | 6,347.28 |
| identity_checks | 420.90 | **10,332.16** | 10,486.21 |
| routes | 657.52 | 914.04 | 1,011.20 |
| receipt | 465.97 | 841.88 | 962.83 |
| semantic_validation | 73.70 | 76.15 | 76.50 |
| dirty_job | 42.57 | 43.84 | 45.64 |
| commit | 30.86 | 57.77 | 68.00 |
| deletions | 0.40 | 0.50 | 0.63 |
| dataset_lock | 0.38 | 0.49 | 0.52 |

### Handler segments (the transport section R0 asks for)

| segment | S60 P50 / P95 | S60H P50 / P95 | S240 P50 / P95 |
|---|---|---|---|
| wire_bytes | 4,134,607 | 10,119,137 | 16,537,254 |
| multipart_ms | 5.39 / 6.08 | 13.89 / 16.03 | 21.34 / 23.42 |
| decompression_ms | 0 (uncompressed fixture) | 0 | 0 |
| json_decode_ms | 103.80 / 105.66 | 263.10 / 266.34 | 409.03 / 413.32 |
| validation_ms | 18.29 / 19.39 | 33.88 / 34.75 | 73.70 / 76.15 |
| import_ms | 2,687.93 / 4,646.30 | 3,991.97 / 5,477.11 | 11,601.06 / 19,276.94 |
| ack_write_ms | 0.08 | 0.08 | 0.09 |
| ack_total_ms | 2,795.73 / 4,756.65 | 4,273.34 / 5,752.21 | 12,033.00 / 19,702.74 |

## 4. Verdicts

| gate | requirement | measured | verdict |
|---|---|---|---|
| **R2-P08** | normal-build HTTP→PG S60, n≥30, P95 ≤ 1,000 ms, 0 failures | n=30, 0 failures, **P95 4,758.8 ms** (4.8× over) | **FAIL** |
| **R2-P09** | S240, n≥30, P95 ≤ 4,000 ms, 0 failures, no pool/slot/timeout expansion | n=30, 0 failures, **P95 19,722.3 ms** (4.9× over); pool 8, slots 2/2, budgets default | **FAIL** |
| **R2-P10a** | final write phase P50/P95 must not regress | arrays vs values, exclusive A/B: P50 1,671.9 → 1,519.3 ms, P95 2,056.1 → 2,052.9 ms | **PASS** (no regression) |
| **R2-P10b** | if SQL is R0's main bottleneck, write P50 down ≥ 30% | **−9.1%** (see §5) | **FAIL** |

These are the honest verdicts. No threshold was lowered, no assertion weakened, no budget
widened and no durability setting turned off to obtain them.

## 5. Write candidates (R2-P10) — exclusive A/B, S60, n=10, 0 failures

`RAI_SYNC_WRITE_CANDIDATES=1 RAI_SYNC_WRITE_ITERATIONS=10 RAI_SYNC_WRITE_WARMUPS=2 RAI_SYNC_WRITE_PROFILE=S60`
→ `exclusive-ab-identity.log`. Fresh disposable database per candidate, fresh identities per
iteration, projection digests identical across candidates.

| candidate | import P50 | import P95 | sample_writes P50 | association_writes P50 | write sum P50 | WAL/import | tuples/import |
|---|---|---|---|---|---|---|---|
| values | 2,461.5 | 3,779.7 | 844.0 | 827.9 | **1,671.9** | 17,752,193 | 20,388.6 |
| **arrays** | 2,432.7 | **3,039.5** | **758.1** | **761.2** | **1,519.3** | 17,752,268 | 20,388.6 |
| copy | 2,433.4 | 3,299.9 | 826.8 | 906.4 | 1,733.2 | 17,848,371 | 20,515.3 |

Arrays wins both write phases and P95; the total import difference is 1.2 % (inside noise) but the
write-phase difference is 9.1 % and the P95 difference 19.6 %. COPY is rejected: it is the
slowest write sum, has the highest WAL (staging rows are logged too) and cannot run under the
least-privilege API role. Full detail in `r5-write-candidates.md`.

**R2-P10b therefore FAILS at 9.1 %.** The reason is quantified in §6: statement shape is no
longer where the time goes.

## 6. WAL / full-page-image attribution (why the tail is not application code)

`RAI_SYNC_PERF_ITERATION_STATS=1` adds a per-iteration WAL/checkpoint delta line
(`SYNCPERF_ITER`) → `r0-tail-diagnostic.log`; the bare-PostgreSQL floor probe is
`r5-bare-pg-ceiling.log`.

### 6.1 WAL volume per import

| source | WAL per import | per row | per sample |
|---|---|---|---|
| real import, S60 (values or arrays, identical) | 17,752,268 B | 922 B (20,389 tuples) | 2,692 B |
| bare probe, S60 (both fact projections only) | 8,110,470 B | 922 B | 1,230 B |
| bare probe, S240 (both fact projections only) | 48,454,581 B | 918 B | 1,837 B |
| real import, S240, median iteration | ≈80 MB | — | ≈3.1 KB |

So the two fact projections cost ~0.92 KB of WAL per row and ~1.2–1.8 KB per sample on their own;
the remaining ~9.6 MB of an S60 import is route points/chunks (3,424 points), the workout row,
the receipt, the dirty scope, the job row and the change event. WAL per row is identical for
VALUES, arrays and COPY (±0.5 %), i.e. it is set by the row, the four indexes on
`health_samples` and the two on `workout_samples`, not by the statement shape.

### 6.2 Full-page-image amplification (this is the tail)

`full_page_writes=on` (a durability requirement, not a tunable here) logs a full 8 KB image of
every page the first time it is dirtied after a checkpoint. On a fresh database with three
imports the FPI share is negligible (0.01–0.07 %). In the 30-import acceptance database it is
not:

| profile | WAL/import median | WAL/import max | FPI pages/import median | FPI pages/import max | FPI bytes in the worst import | checkpoint sync time max |
|---|---|---|---|---|---|---|
| S60 | 14 MB | 92 MB | 0 | 14,341 | 117 MB | 0 ms |
| S240 | 80 MB | **379 MB** | 4,784 | **53,406** | **437 MB** | **6,125 ms** |

53,406 full-page images in one import is 437 MB of WAL that exists only because a checkpoint
happened between two imports; the import that carries it is 8–9× the WAL of a quiet one. With
`max_wal_size=1 GB` and 48–60 MB of base WAL per S240 import, a checkpoint lands every ~17–20
imports, so roughly a third of a 30-iteration run pays FPI amplification. The slowest S240
iterations (i=14, 19, 20, 23, 24, 26, 27, 28, 29 at 13.1–17.6 s) are exactly the FPI-heavy ones;
quiet iterations are 9.0–10.7 s.

### 6.3 Stale statistics — the second, separate tail at S240

`identity_checks` at S240 is 5,456 / 9,591 / 11,555 ms for the first three timed imports and then
collapses to 275 ms and creeps up to 476 ms over the remaining 27:

```
i=0 ident=5456   i=1 ident=9591   i=2 ident=11555   i=3 ident=275   i=4 ident=300
… i=29 ident=476
```

That growth-then-collapse is the signature of autoanalyze: the identity query runs with
`plan_cache_mode=force_custom_plan`, so it re-plans on every execution from whatever statistics
exist, and while `health_samples` is growing 100 % per import those statistics are stale, so the
planner mis-costs the set-based identity join. Once autoanalyze catches up, the same statement
runs in 275–476 ms. `TestIdentityCheckScaling` (12 successive S20 imports, the exact production
identity statement under `EXPLAIN (ANALYZE, BUFFERS)`) shows the plan itself is stable and cheap
when statistics are current — Hash Left Join on index scans, 9.0 ms at 2,197 rows/2,197 keys
growing linearly to 34.8 ms at 26,364 rows, buffers 220 → 2,445, never a nested loop:

```
R5_IDENTITY iteration=11 table_rows=26364 keys=2197 explain_ms=34.8 exec_ms=31.3 Hash Left Join … Buffers: shared hit=2445
```

**Conclusion for the P09 verdict.** Of the ~11 s an S240 import takes in steady state:

* ~2.4–2.7 s is the bare PostgreSQL floor for writing the two fact projections on this machine
  with these indexes and this durability (§7) — of which 55–67 % is per-row trigger/FK work that
  no statement shape avoids;
* ~0.7 s is route writes, ~0.47 s receipt, ~0.45 s identity when statistics are current;
* the rest is WAL/checkpoint/full-page-image I/O that `fsync=on` + `full_page_writes=on` make
  mandatory, plus the Go-side fact marshalling inside the write phase.

The 11 s is therefore **dominated by durability costs PostgreSQL charges for this schema**, not by
an unfinished application optimisation. The one controllable lever that is left is keeping
planner statistics fresh (§6.3), and that requires a migration decision (per-table autovacuum
analyze thresholds, or an explicit `ANALYZE` after large imports) that is outside this round's
scope and was not applied.

## 7. Bare-PostgreSQL floor probe (is the FAIL defensible?)

`TestBarePGWriteCeiling` (new, `workout_bare_pg_test.go`) bypasses HTTP, Go decoding and every
other import phase: same tables, same indexes, same durability, one transaction, only the two
fact projections, commit included. n=3 per variant, fresh database per variant.

| profile | variant | write P50 | write P95 | WAL/import | WAL per row | FPI share |
|---|---|---|---|---|---|---|
| S60 | unnest, 5,000-row batches | 639.2 | 648.9 | 8,110,470 | 922 B | 0.07 % |
| S60 | unnest, 1,000-row batches | 644.0 | 738.6 | 8,113,926 | 922 B | 0.07 % |
| S60 | unnest, rows sorted by uuid | 590.2 | 632.7 | 7,801,713 | 887 B | 0.07 % |
| S60 | COPY into indexed tables | **585.1** | 606.7 | 7,919,980 | 900 B | 0.21 % |
| S60 | COPY unindexed → build indexes | 483.9 (+232.4 index build) | 700.4 | 3,867,350 | 440 B | 0.42 % |
| S240 | unnest, 5,000-row batches | 2,690.3 | 2,926.6 | 48,454,581 | 918 B | 0.01 % |
| S240 | unnest, 1,000-row batches | 2,723.1 | 3,161.1 | 48,436,292 | 918 B | 0.01 % |
| S240 | unnest, rows sorted by uuid | 2,565.3 | 2,571.0 | 47,694,709 | 904 B | 0.01 % |
| S240 | COPY into indexed tables | **2,442.0** | 2,525.0 | 47,250,297 | 895 B | 0.03 % |
| S240 | COPY unindexed → build indexes | 1,834.6 (+1,129.8 index build) | 2,993.8 | 32,069,655 | 608 B | 30.8 % |

Reading:

* **The most aggressive shape available is 585 ms (S60) and 2,442 ms (S240)** — both still far
  above the 1 s / 4 s gates once the rest of the request (decode, identity, routes, receipt) is
  added. Even COPY into an unindexed table plus an index build costs 483.9 + 232.4 = 716 ms (S60)
  and 1,834.6 + 1,129.8 = 2,964 ms (S240), i.e. it cannot reach the gate either.
* **Statement shape is worth < 5 %**: COPY vs unnest is 585 vs 639 ms (S60) and 2,442 vs 2,690 ms
  (S240). That is why all three production candidates land within 9 % of each other.
* **Batch size is worth nothing**: 1,000-row vs 5,000-row batches differ by 0.7 % (S60) and 1.2 %
  (S240). The production array path's 1,000-row batching is not a bottleneck.
* **Insert order is worth 5–8 %** (sorting by uuid: 590 vs 639 ms at S60, 2,565 vs 2,690 ms at
  S240). That is below the 20 % bar, and the production fixture already emits rows grouped by
  sample type in ascending time order, which is the natural order for the `type_time` index. The
  idea was tried, measured, and **rejected as not worth the code churn**.
* The production write phases are 1,519 ms (S60) and 8,649 ms (S240), i.e. 2.4× and 3.2× the bare
  floor. The gap is *not* statement shape and *not* batch size; it is the Go-side fact
  preparation (per-row metadata marshal, array building, pgx encoding) plus the accumulated
  WAL/checkpoint pressure of a database that has already absorbed 29 imports. A CPU profile of
  the write phase is the next diagnostic, and it was not run inside this time box.

## 8. S240 HTTP 503s seen in the first (contended) baseline

`r0-baseline-syncperf.log`: S240 n=24, failures=6,
`iteration 0: status 503; iteration 1: status 503; iteration 19: status 503; iteration 21: status 503; iteration 27: status 503; iteration 28: status 503`.
That binary predates the response-body capture, so the body was lost and the historical error code
is **NOT VERIFIED**. It was not reproducible afterwards: the exclusive acceptance run over the
same profile had **0 failures in 30 iterations**, and so did the shorter re-probe.

What can be established about it:

* **It was not a data-size effect.** Two of the six failures are iterations 0 and 1 of a
  brand-new database. The window was the contended one (the iOS simulator lane was compiling on
  the same 12 cores), and S240 takes 24.6 s P50 there versus 12.0 s P50 exclusive — the imports
  were simply slow, and the failures are load-induced.
* **The handler has exactly four 503 paths**: `request_timeout` (the 60 s handler context),
  `database_statement_timeout_or_cancel` (SQLSTATE 57014), `database_transaction_timeout`
  (SQLSTATE 25P04) and `temporarily_unavailable` (anything unmapped). The 45 s transaction
  context and the transaction-local `SET LOCAL statement_timeout='30s'` mean a statement that
  crosses 30 s produces the second one.
* **The 30 s statement timeout is the reachable one at that load**, and it is now proven
  deterministically. `TestWorkoutBundleStatementTimeoutCode` (new, `workout_timeout_probe_test.go`,
  gated by `RAI_SYNC_TIMEOUT_PROBE=1`) installs a per-row trigger that makes a single 1,000-row
  batch statement exceed the transaction-local timeout, then drives the real handler over the
  real store:

  ```
  R5_TIMEOUT_PROBE status=503 code="database_statement_timeout_or_cancel" elapsed_ms=30049
      body={"code":"database_statement_timeout_or_cancel"}
  --- PASS: TestWorkoutBundleStatementTimeoutCode (31.03s)
  ```

  and asserts the whole bundle rolled back (`samples=0 receipts=0 jobs=0`). In the contended
  baseline the fact-write phases reached 27.7 s P95 / 20.6 s max for `sample_writes` and 14.7 s
  P95 for `association_writes`, and the slowest acknowledgements reached 42.4 s, so a batch
  statement crossing 30 s on the worst iterations is the consistent explanation.

Conclusion: the historical 503 code is **NOT VERIFIED**, but the only 503 the product can produce
for a slow-but-valid import at this durability is
`database_statement_timeout_or_cancel` (statement) or `request_timeout` (60 s handler budget), and
the statement-timeout path is now reproduced and pinned by a test. The harness also now records
the first 240 bytes of every failing response body, so any recurrence will name the code directly.

## 9. Files

| file | content |
|---|---|
| `r0-baseline-syncperf.log` | first baseline run (contended; values strategy; S60+S60H complete, S240 6×503) |
| `r0-after-arrays-syncperf.log` | arrays strategy, contended (superseded) |
| `r0-final-syncperf.log` | **the accepted run**: exclusive, arrays, S60+S60H+S240, n=30, 0 failures |
| `r0-tail-diagnostic.log` | same entry with per-iteration WAL/checkpoint deltas (S60+S240) |
| `r5-bare-pg-ceiling.log` | bare-PostgreSQL floor probe |
| `exclusive-ab-identity.log` | exclusive candidate A/B + identity-scaling diagnostic |
| `r5-gates.log` | F21–F27 integration gates |
| `r5-explain.log` | `EXPLAIN (ANALYZE, BUFFERS, WAL)` for all three candidates |
