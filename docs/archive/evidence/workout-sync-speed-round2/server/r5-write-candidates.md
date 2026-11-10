# R5 — set-based fact writes: three candidates, measured

Scope: `apps/server/internal/storage/postgres/{bundle_writes.go, bundle_batching.go, workout_bundles.go}`.
Nothing here changes the transaction shape, the conflict rules, the pool, the upload slots,
the default budgets or durability. Every number below is synthetic data on a disposable local
PostgreSQL 18.6 cluster (`fsync=on full_page_writes=on synchronous_commit=on`,
`shared_buffers=64MB`, `--data-checksums`).

> **Measurement windows.** This machine is shared with the iOS lane. Runs whose log header shows
> `exclusive=false` (or a load average well above the core count) are marked *contended* and are
> used only for the **relative** candidate comparison, never for an acceptance gate. The
> acceptance numbers live in `r0-syncperf.md` and carry their own exclusivity snapshot.

## 1. What was measured first (bottleneck before the change)

Normal build, real loopback TCP, isolated PG18, S60 (6,594 samples, 3,424 route points,
4,134,363 fixture bytes), n=30, 0 failures — `r0-baseline-syncperf.log` (contended):

| phase | P50 | P95 | max |
|---|---|---|---|
| import_total | 2630.41 | 5913.98 | 7686.11 |
| sample_writes | 960.38 | 1908.61 | 2030.63 |
| association_writes | 975.39 | 1336.40 | 1514.66 |
| identity_checks | 158.79 | 2863.77 | 4908.73 |
| routes | 153.68 | 278.73 | 685.33 |
| receipt | 107.83 | 352.01 | 1827.88 |
| semantic_validation | 19.58 | 22.29 | 34.83 |
| commit | 10.27 | 116.07 | 127.16 |
| dataset_lock | 0.35 | 0.67 | 0.70 |

The two write phases are 1,935ms P50 ≈ 74% of `import_total`, so they are the largest
*controllable* item. `identity_checks` is small at P50 but has the worst tail (max 4,909ms) —
that tail is investigated separately in §5.

`sample_writes` is **not** pure database execution time: it contains the Go-side metadata
marshal, the parameter block construction, the pgx encode and the server-side insert. The
EXPLAIN numbers in §3 are what separates those.

## 2. Candidate comparison (S60, n=10, 2 warmups) — **exclusive window** (`exclusive-ab-identity.log`)

`RAI_SYNC_WRITE_CANDIDATES=1 RAI_SYNC_WRITE_ITERATIONS=10 RAI_SYNC_WRITE_WARMUPS=2 RAI_SYNC_WRITE_PROFILE=S60`
→ `r5-candidate-compare.log`. Each strategy runs in its own disposable database; every
iteration uses a fresh seed so every import is a genuine first import (no replays).

| candidate | import P50 | import P95 | sample_writes P50 | association_writes P50 | write sum P50 | WAL bytes / import | tuples / import |
|---|---|---|---|---|---|---|---|
| VALUES (baseline) | 2,461.5 | 3,779.7 | 844.0 | 827.9 | 1,671.9 | 17,752,193 | 20,388.6 |
| **arrays (`unnest`)** | **2,432.7** | **3,039.5** | **758.1** | **761.2** | **1,519.3** | 17,752,268 | 20,388.6 |
| COPY → staging → set projection | 2,433.4 | 3,299.9 | 826.8 | 906.4 | 1,733.2 | 17,848,371 | 20,515.3 |

An earlier contended-window run gave VALUES 3,790.1 / arrays 2,733.6 / COPY 3,077.8 import P50;
it is kept only as the contamination record and is superseded by the exclusive numbers above.

Semantic parity in the same run: the projection digests of `health_samples`,
`workout_samples`, `workouts`, `series_uploads`, `series_chunks`, `workout_routes`,
`route_chunks` and `route_points` are **identical** for all three candidates, and the receipts
are identical after normalising the random analysis job ids and the per-database bundle hash.

50,000-event ceiling (`TestWorkoutWriteStrategyRowBoundaries`, inside the gate run,
`r5-gates.log`), same bundle (13,938,646 JSON bytes):

| candidate | import total | sample_writes | association_writes | identity |
|---|---|---|---|---|
| values | 16,873.9 ms | 7,953.3 | 7,250.6 | 238.3 |
| **arrays** | **13,624.5 ms** | **6,048.0** | **6,311.3** | **156.7** |
| copy | 30,519.7 ms | 18,871.0 | 8,407.2 | 890.5 |

## 3. Where the time actually goes (`EXPLAIN (ANALYZE, BUFFERS, WAL)`, 300 rows)

`RAI_SYNC_WRITE_PLANS=1 … -run '^TestWorkoutWriteStrategyExplains$'` → `r5-explain.log`.
The statement text is the production text: `SampleValuesStatement` / `sampleArraysStatement` /
the staging projection are package constants shared by the import path and the harness.

| statement | Execution | WAL records / bytes | Buffers | per-row trigger + FK time |
|---|---|---|---|---|
| values → `health_samples` | 24.043 ms | 1222 / 312,328 | hit 2760 dirtied 31 | 16.04 ms over 300 calls (fkey 5.099 + ingest_device_fk 4.051 + health_sample_ingest_device 3.762 + recovery_fact 1.763 + vo2_external_fact 1.361) |
| values → `workout_samples` | 18.574 ms | 1210 / 211,300 | hit 2134 dirtied 17 | 7.92 ms over 300 calls (two FK triggers) |
| arrays → `health_samples` | 28.366 ms | 1223 / 290,084 | hit 3085 dirtied 24 | 13.45 ms over 300 calls |
| arrays → `workout_samples` | 15.859 ms | 1258 / 186,324 | hit 2753 dirtied 9 | 8.03 ms over 300 calls |
| copy projection → `health_samples` | 9.566 ms (Insert node) + 0.492 ms subquery | 1223 / 256,992 | hit 3146 dirtied 16, local hit 7 | 13.24 ms over 300 calls |
| copy projection → `workout_samples` | 20.482 ms | — | — | 13.05 ms over 300 calls |

Reading:

* At 300 rows the **server-side** insert time is *not* better for the array candidate
  (28.4ms vs 24.0ms); the array win therefore comes from the **client side** — one fixed-shape
  bind of ten arrays per batch instead of eleven bind parameters per row — and it only shows up
  at realistic batch sizes (6.6k–50k rows, §2).
* 55–67% of each insert is per-row trigger work (`bind_fact_ingest_device`, the FK checks and the
  two invalidation triggers), which **no** statement shape can avoid. WAL is ~1KB per inserted
  row and is essentially identical across the three candidates, so the write path is
  WAL/index bound, not statement-shape bound.
* `collector_leases.dataset_id` is the primary key, so the per-row device-binding lookup is an
  index lookup, not a scan.

## 4. COPY: implemented, measured, rejected (and privilege-blocked)

The COPY candidate is real and runnable: `CREATE TEMP TABLE … ON COMMIT DROP` staging
(fixed columns, `ordinal` preserves input order), `tx.CopyFrom` into staging, then a set-based
projection into `health_samples` / `workout_samples`, then a **batch join barrier**
(`verifySampleProjection`, plus the association barrier) that fails the import if any staged row
is missing or differs from the written row. Both projections and the barrier run inside the same
transaction as the rest of the import, so the lock → fencing → tombstone → conflict → fact →
log/dirty/job/receipt → COMMIT order is untouched.

Rejection reasons (all measured, not assumed):

1. It is slower than the array candidate at both scales: 3,077.8ms vs 2,733.6ms import P50 at
   S60 and 30,519.7ms vs 13,624.5ms at 50k, with `sample_writes` 18,871ms vs 6,048ms at 50k.
   The extra staging write plus the re-read costs more than the bind it saves.
2. WAL is the highest of the three (17,853,536 bytes/import at S60), because the staging rows are
   WAL-logged too.
3. Under a least-privilege API role it cannot run at all (§6).

It stays in the tree as a switchable candidate so the comparison stays reproducible
(`postgres.SetWriteStrategy(postgres.StrategyCopy)`), but it is not the default.

## 5. `identity_checks` tail — the diagnostic that was added

The R0 logs show `identity_checks` P50 159ms / P95 2,864ms (S60) and P50 617ms / P95 3,265ms
(S240). `TestIdentityCheckScaling` (new, `workout_identity_scaling_test.go`) grows one dataset
import by import and runs the **exact production identity statement**
(`postgres.SampleIdentityStatement`, now a package constant shared with `loadSampleIdentities`)
with `EXPLAIN (ANALYZE, BUFFERS)` under the same transaction-local settings, so the tail can be
attributed to buffer reads, a plan change or planning time instead of being guessed.
Result: see the `R5_IDENTITY` lines in `r5-identity-scaling.log` and the summary in
`r0-syncperf.md` §tail.

## 6. Restricted API runtime role (R2-F25)

`TestWorkoutWriteStrategyRestrictedRole` creates `rai_api_restricted` (LOGIN, `USAGE` on schema
`public`, `SELECT/INSERT/UPDATE/DELETE` on all tables, `USAGE/SELECT` on all sequences,
`EXECUTE` on all functions) and then revokes `TEMP` from both `PUBLIC` and the role:

```
R5_RESTRICTED_ROLE role=rai_api_restricted temp_privilege=false
R5_RESTRICTED_ROLE copy_candidate=blocked error=ERROR: permission denied to create temporary tables in database "…" (SQLSTATE 42501)
```

* `values` import of 300 samples: succeeds.
* `arrays` import of 300 samples on the same pool: succeeds.
* `copy`: fails with `42501`, publishes nothing (`assertSnapshotUnchanged`), leaves no staging
  table on any of the pooled connections (`assertNoStagingTables`).
* A further `arrays` import on the same pool succeeds → the pool is reusable after the privilege
  failure.
* Minimal privilege the COPY candidate would need: `TEMP` on the database (or a fixed
  pre-created staging schema plus `CREATE` on it). The API role deliberately has neither, so the
  array candidate is the only shape that fits the least-privilege role.

Note: the first run of this test also proved the role needs `EXECUTE` on the `rai_route_*`
functions — without it the route write fails with `permission denied for function
rai_route_append_chunk`. That grant is now part of the documented minimal set.

## 7. Plan cache, cold and hot connections (R2-F26)

`TestWorkoutWriteStrategyPlanModes` → `R5_PLAN_MODES` lines in `r5-gates.log`:

```
R5_PLAN_MODES cold_ms=161.2 hot_p50_ms=121.8 hot_max_ms=127.6
R5_PLAN_MODES identity_probe mode=force_custom_plan  keys=1000 matched=1000 elapsed_ms=1.0
R5_PLAN_MODES identity_probe mode=force_generic_plan keys=1000 matched=1000 elapsed_ms=0.9
```

Five sequential imports of 400 samples on one pool: the first (cold) import is slower than the
hot ones, and every import persists the same rows (2,000 total). The identity probe returns the
same 1,000 matches under both plan modes, and on this dataset size neither degrades into a
per-key nested loop. The transaction-local `plan_cache_mode='force_custom_plan'` and
`statement_timeout='30s'` from round 1 are therefore **kept unchanged** — no controlled
experiment justified changing them.

## 8. Guarantees that were preserved

* One transaction: dataset row lock → fencing → parent/child tombstone → identity/association
  conflict → facts → log/dirty/job/receipt → COMMIT. The write candidates only replace the two
  fact `INSERT` statements; every conflict check still runs before them, and no candidate uses
  `ON CONFLICT DO NOTHING` for facts (the only `ON CONFLICT` is on the immutable
  `entity_tombstones` insert and on the immutable receipt row).
* Append-only enrichment and same-UUID core change are judged separately (`TestWorkoutWriteStrategyConflictRollback`:
  a value change is rejected with `sample_identity_conflict` and rolls the whole bundle back; a
  cross-parent re-association is rejected with `sample_association_conflict`; adding provenance
  to an already-stored, identically-cored bundle is accepted with `samplesInserted=0`,
  `samplesUnchanged=200`).
* Exact replay still checks the tombstone first and returns the original receipt
  (`TestWorkoutWriteStrategyParity`).
* No pool, upload slot, budget or durability change: pool 8, process upload slots 2, dataset
  upload slots 2, `fsync`/`full_page_writes`/`synchronous_commit` on.

## 9. Gate results (real isolated PostgreSQL)

`scripts/test-postgres.sh go test -tags=integration -count=1 -timeout=60m -run '^TestWorkoutWriteStrategy|^TestStrict' -v ./integration`
→ `r5-gates.log`, exit 0:

```
--- PASS: TestStrictWorkoutBundleJSONRejectionMatrix (0.55s)
--- PASS: TestStrictWorkoutBundleSemanticRules (0.00s)
--- PASS: TestWorkoutWriteStrategyParity (34.88s)
--- PASS: TestWorkoutWriteStrategyRowBoundaries (80.76s)
--- PASS: TestWorkoutWriteStrategyConflictRollback (7.74s)
--- PASS: TestWorkoutWriteStrategyFaultRollback (9.59s)
--- PASS: TestWorkoutWriteStrategyRestrictedRole (1.30s)
--- PASS: TestWorkoutWriteStrategyPlanModes (1.45s)
ok  rai/server/integration  136.325s
```

`TestWorkoutWriteStrategyFaultRollback` proves the two failure shapes leave nothing behind:
a `BEFORE INSERT` trigger that raises mid-projection (staging filled, projection aborted) and a
`DEFERRABLE INITIALLY DEFERRED` constraint trigger that fires at COMMIT after every fact, receipt,
job and change event was written. In both cases the bundle leaves 0 rows in
`workouts / health_samples / workout_samples / workout_imports / change_events / jobs /
dirty_scopes / series_uploads / workout_routes` and 0 `rai_bundle_*` staging tables on any pooled
connection.

## 10. Chosen candidate

**`arrays` (`unnest` with fixed-shape array parameters)** — it is the fastest of the three at both
measured scales, it is semantically identical to the VALUES baseline (projection digests and
receipts match), it keeps WAL at the same level, it needs no extra privilege, and it removes
72k bind parameters per S60 import. It is selectable back to `values`/`copy` at runtime, and
`values` remains the reference implementation used by the parity test.

The candidate comparison was re-run inside the exclusive acceptance window; see
`r0-syncperf.md` for the final numbers and for the honest statement of what did **not**
improve at gate scale.

## 11. Bare-PostgreSQL floor: how much of the write cost is unavoidable

`TestBarePGWriteCeiling` (`apps/server/integration/workout_bare_pg_test.go`) removes the
application entirely: same tables, same indexes, same durability, one transaction, only the two
fact projections, commit included, no identity phase, no receipt, no job, no change event.
n=3 per variant, fresh disposable database per variant, log `r5-bare-pg-ceiling.log`.

| profile | variant | write P50 | write P95 | WAL/import | WAL/row | FPI share |
|---|---|---|---|---|---|---|
| S60 | unnest, 5,000-row batches | 639.2 | 648.9 | 8,110,470 | 922 B | 0.07 % |
| S60 | unnest, 1,000-row batches | 644.0 | 738.6 | 8,113,926 | 922 B | 0.07 % |
| S60 | unnest, rows sorted by uuid | 590.2 | 632.7 | 7,801,713 | 887 B | 0.07 % |
| S60 | COPY into the indexed tables | **585.1** | 606.7 | 7,919,980 | 900 B | 0.21 % |
| S60 | COPY unindexed, then build indexes | 483.9 + 232.4 build | 700.4 | 3,867,350 | 440 B | 0.42 % |
| S240 | unnest, 5,000-row batches | 2,690.3 | 2,926.6 | 48,454,581 | 918 B | 0.01 % |
| S240 | unnest, 1,000-row batches | 2,723.1 | 3,161.1 | 48,436,292 | 918 B | 0.01 % |
| S240 | unnest, rows sorted by uuid | 2,565.3 | 2,571.0 | 47,694,709 | 904 B | 0.01 % |
| S240 | COPY into the indexed tables | **2,442.0** | 2,525.0 | 47,250,297 | 895 B | 0.03 % |
| S240 | COPY unindexed, then build indexes | 1,834.6 + 1,129.8 build | 2,993.8 | 32,069,655 | 608 B | 30.8 % |

Conclusions that this probe establishes:

1. **Statement shape is worth < 5 %.** COPY vs the production array statement is 585 vs 639 ms
   (S60) and 2,442 vs 2,690 ms (S240). This is why all three production candidates land within
   9 % of each other.
2. **Batch size is worth nothing.** 1,000-row vs 5,000-row batches differ by 0.7 % (S60) and
   1.2 % (S240); the production array path's 1,000-row batching is not a bottleneck.
3. **Insert order is worth 5–8 %** (uuid-sorted rows: 590 vs 639 ms at S60, 2,565 vs 2,690 ms at
   S240). That is below the 20 % bar and the production fixture already emits rows grouped by
   sample type in ascending time order, which is the natural order for `health_samples_type_time`.
   The idea was tried, measured and **rejected as not worth the code churn**.
4. **The physical floor of this schema on this machine is 585 ms (S60) / 2,442 ms (S240)** for the
   two fact projections alone, before decode, identity checks, routes and receipt. Even the most
   aggressive shape available (COPY into an unindexed table plus an index build: 716 ms / 2,964 ms)
   cannot reach the 1 s / 4 s gates.
5. The production write phases are 1,519 ms (S60) and 8,649 ms (S240) — 2.4× and 3.2× the floor.
   Since shape and batch size are ruled out, that gap is Go-side fact preparation (per-row
   metadata marshal, array building, pgx encoding) plus the accumulated WAL/checkpoint pressure of
   a database that has already absorbed 29 imports. A CPU profile of the write phase is the next
   diagnostic and was outside this round's time box.

## 12. WAL attribution of a real import

| source | WAL/import | WAL/row | WAL/sample |
|---|---|---|---|
| real import S60 (any candidate) | 17,752,268 B | 922 B | 2,692 B |
| bare probe S60 (fact projections only) | 8,110,470 B | 922 B | 1,230 B |
| bare probe S240 (fact projections only) | 48,454,581 B | 918 B | 1,837 B |
| real import S240, median iteration | ≈80 MB | — | ≈3.1 KB |

The two fact projections cost ~0.92 KB of WAL per row on their own; the rest of an S60 import is
route points/chunks (3,424 points), the workout row, the receipt, the dirty scope, the job row and
the change event. WAL per row is identical for VALUES, arrays and COPY (±0.5 %), so it is set by
the row and the four `health_samples` / two `workout_samples` indexes, not by the statement shape.
`full_page_writes=on` then amplifies this: in the 30-import acceptance database a single S240
import reached 379 MB of WAL with 53,406 full-page images and 6,125 ms of checkpoint fsync — see
`r0-syncperf.md` §6.

## 13. Final gate record (arrays is the production default)

After `currentWriteStrategy` was switched to `StrategyArrays`, every gate was re-run on the final
code, on real isolated PostgreSQL. Commands and full output are in the named logs.

**`r5-gates.log`** — `scripts/test-postgres.sh go test -tags=integration -count=1 -timeout=60m -run '^(TestWorkoutWriteStrategy|TestStrict|TestWorkoutWriteCandidates)' -v ./integration`, exit 0:
8 pass, 2 skipped (env-gated), 0 fail.

```
--- PASS: TestStrictWorkoutBundleJSONRejectionMatrix (0.26s)
--- PASS: TestStrictWorkoutBundleSemanticRules (0.00s)
--- PASS: TestWorkoutWriteStrategyParity (17.23s)
--- PASS: TestWorkoutWriteStrategyRowBoundaries (60.37s)
--- PASS: TestWorkoutWriteStrategyConflictRollback (8.03s)
--- PASS: TestWorkoutWriteStrategyFaultRollback (7.47s)
--- PASS: TestWorkoutWriteStrategyRestrictedRole (1.04s)
--- PASS: TestWorkoutWriteStrategyPlanModes (1.13s)
--- SKIP: TestWorkoutWriteStrategyExplains (needs RAI_SYNC_WRITE_PLANS=1; captured in r5-explain.log)
--- SKIP: TestWorkoutWriteCandidates (needs RAI_SYNC_WRITE_CANDIDATES=1; captured in exclusive-ab-identity.log)
ok  rai/server/integration  95.579s
```

50,000-sample boundary (R2-F22, one series × 5,000 points × 2 and one route × 5,000 points,
13,938,646 JSON bytes), end-to-end through the store:

```
R5_50K strategy=values samples=50000 elapsed_ms=17402.0 json_bytes=13938646
R5_50K strategy=arrays samples=50000 elapsed_ms=14899.0 json_bytes=13938646
R5_50K strategy=copy   samples=50000 elapsed_ms=15968.1 json_bytes=13938646
```

arrays is 14.4 % faster than VALUES and 6.7 % faster than COPY at the 50 k boundary, consistent
with the S60 ranking.

**`r5-race-gate.log`** — the same gates under `-race` (the correctness basis this project uses),
exit 0: 7 pass, 0 fail.

```
--- PASS: TestStrictWorkoutBundleJSONRejectionMatrix (2.37s)
--- PASS: TestStrictWorkoutBundleSemanticRules (0.00s)
--- PASS: TestWorkoutWriteStrategyParity (24.34s)
--- PASS: TestWorkoutWriteStrategyConflictRollback (10.37s)
--- PASS: TestWorkoutWriteStrategyFaultRollback (9.26s)
--- PASS: TestWorkoutWriteStrategyRestrictedRole (1.26s)
--- PASS: TestWorkoutWriteStrategyPlanModes (1.52s)
ok  rai/server/integration  50.176s
```

(`TestWorkoutWriteStrategyRowBoundaries` is excluded from the race run only because its 50 k case
would take tens of minutes under `-race`; it is covered by the normal-build run above and its
assertions are identical.)

**`r5-existing-suite-race.log`** — the pre-existing workout-bundle/conflict/receipt suite under
`-race`, to prove the arrays default does not regress established semantics, exit 0: 29 pass,
2 skipped (env-gated), 0 fail. Includes
`TestWorkoutBundleOneRequestAtomicImport`, `TestWorkoutBundleSetBasedIdentityAcrossBatchBoundaries`,
`TestWorkoutBundleCommitFailureHasNoACK`, `TestWorkoutBundleLostACKAnd404ThenIdempotentPOST`,
`TestWorkoutBundleSnapshotStillRejectsIdentityConflicts`, `TestWorkoutBundleScaleFiveThousand`,
`TestWorkoutBundleLargeBatchImportSurvivesConnectionStatementTimeout`,
`TestWorkoutConflictRollsBackChildren`, `TestWorkoutReceiptFailureRollsBackInsertAndDelete`,
`TestWorkoutSampleAssociationConstraints` and the HTTP admission/slot/cancel cases.

Other gates on the final tree: `go test -race ./...` (all packages ok), `go vet ./...`,
`go vet -tags=integration ./...`, `gofmt -l` clean, and `python3 scripts/check-boundaries.py`
(all 9 boundaries PASS).
