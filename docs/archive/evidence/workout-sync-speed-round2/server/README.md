# Workout sync speed round 2 — server (Go) evidence

Scope: R0 (normal-build performance entry) and R5 (set-based write deepening + strict JSON)
from `docs/WORKOUT_SYNC_SPEED_ROUND2_PLAN.md`. All data is synthetic; no production database
was read, no real health value, dataset identity, device identity, raw metadata or credential
appears in this directory.

## Build basis (read this before comparing any number)

Two different measurement bases exist in this project and they must never be mixed:

| Basis | Entry | Race | Transport | Timing point |
|---|---|---|---|---|
| Correctness gate (unchanged) | `scripts/local/test.sh integration` | **yes** (`-race`) | `httptest` handler call | handler-internal phases |
| Round-1 perf baseline | `RAI_SYNC_SPEED_PERF=1 … test.sh integration` | **yes** (`-race`) | `httptest` handler call | handler-internal phases |
| **Round-2 R0 (this work)** | `scripts/local/test.sh syncperf` | **no** (asserted) | real loopback TCP + `httpapi.NewHTTPServer` | full HTTP round trip + handler phases |

`scripts/local/test.sh syncperf` builds `go test -c` into `.local/testing/syncperf.test`, filters
the ambient `GOFLAGS` token by token (drops `-race`, bare `-N`/`-l`, and any `-gcflags=`/`-asmflags=`
containing `-N`/`-l`), and then asserts with `go version -m` that the produced binary really has no
`-race` build setting, no `-N`/`-l` code-generation flag, and does carry `-tags=integration`. The
run prints `SYNCPERF_GOFLAGS_IN`, `SYNCPERF_GOFLAGS_EFFECTIVE` and the verified build settings.
`quick`, `gate`, `unit`, `integration`, `full`, `perf`, `swift`, `simulator`, `e2e` are unchanged;
`integration` still defaults to `-race`.

The round-1 race-build matrix (`TestWorkoutSyncSpeedServerAckMatrix`) is preserved unchanged and is
still labelled as *race build, handler-internal, no auth socket*. Its numbers are not comparable
with the R0 numbers below.

## Files

| File | Content |
|---|---|
| `r0-baseline-syncperf.log` | R0 **before** run: S60 / S60H / S240, n=30, multi-row VALUES write path, no transport section (harness gap, see `r0-syncperf.md`); contended window, S240 had 6× HTTP 503 |
| `r0-after-arrays-syncperf.log` | arrays strategy, contended window — **superseded, do not cite** |
| `r0-final-syncperf.log` | **accepted R0 run**: exclusive window, arrays, S60 + S60H + S240, n=30, 0 failures, transport section included |
| `r0-tail-diagnostic.log` | same entry with `RAI_SYNC_PERF_ITERATION_STATS=1`: per-iteration WAL / FPI / checkpoint deltas (S60 + S240) |
| `exclusive-ab-identity.log` | exclusive candidate A/B (n=10) + `TestIdentityCheckScaling` diagnostic |
| `r5-bare-pg-ceiling.log` | bare-PostgreSQL write floor probe (COPY / COPY-unindexed / unnest 1k / unnest 5k / uuid order) |
| `r0-s240-failure-probe.log` | short S240 re-probe used to characterise the S240 503s |
| `r5-candidate-compare.log` | `TestWorkoutWriteCandidates`: VALUES vs arrays vs COPY, facts-digest parity + timings |
| `r5-explain.log` | `EXPLAIN (ANALYZE, BUFFERS, WAL)` for the three candidate statements |
| `r5-gates.log` | **final** R2-F21..F27 gate run (normal build, real isolated PG): 8 pass / 2 env-gated skip / 0 fail |
| `r5-race-gate.log` | the same gates under `-race` (the correctness basis), for the tests that fit the deadline |
| `r5-existing-suite-race.log` | pre-existing workout-bundle / conflict / receipt suite under `-race` with the arrays write path as default: 29 pass / 2 env-gated skip / 0 fail |
| `r5-gates-parity.log` | earlier parity iteration kept for the digest-normalisation history |
| `r0-syncperf.md` | R0 entry, environment, acceptance numbers, per-phase tables, **verdicts R2-P08/P09/P10**, WAL/FPI attribution, bare-PG floor |
| `r5-write-candidates.md` | candidate comparison, plans, WAL, restricted-role evidence, rejection reasons |
| `strict-json.md` | §8.3 decode work and the F27 rejection matrix |

## Headline results

* **R2-P08 FAIL** — S60 normal-build HTTP→PG, n=30, 0 failures, P50 2,796.7 ms / **P95 4,758.8 ms**
  against a 1,000 ms gate.
* **R2-P09 FAIL** — S240, n=30, 0 failures, P50 12,035.0 ms / **P95 19,722.3 ms** against a
  4,000 ms gate, with pool 8 / upload slots 2 / default budgets untouched.
* **R2-P10** — no write-phase regression (PASS); the ≥30 % write-P50 reduction target was **not**
  reached (arrays is −9.1 % vs VALUES on an exclusive A/B), because the remaining write cost is
  per-row trigger/FK/index work and WAL, not statement shape.
* Chosen write candidate: **arrays (`unnest`)**; COPY is slower, writes more WAL and is blocked
  under the least-privilege API role.
* The S240 tail is PostgreSQL durability cost: 80 MB WAL/import median rising to 379 MB with
  53,406 full-page images and 6,125 ms of checkpoint fsync in one import, plus stale planner
  statistics for the first three imports. The bare-PostgreSQL floor for the same rows is
  585 ms (S60) / 2,442 ms (S240) even with COPY — see `r0-syncperf.md` §6–§7.
* The six HTTP 503s in the first (contended) S240 baseline were **not** a data-size effect
  (two of them are iterations 0 and 1 of a fresh database). Their historical error code is
  **NOT VERIFIED** because that binary did not yet record response bodies, but the product can
  only answer 503 with four codes and the reachable one is now pinned by a test:
  `TestWorkoutBundleStatementTimeoutCode` reproduces `503 {"code":"database_statement_timeout_or_cancel"}`
  at 30,049 ms (the transaction-local 30 s statement timeout) with a full rollback. See
  `r0-syncperf.md` §8.

## Commands

```sh
export PATH="$PWD/.local/toolchain/go/bin:$PATH"

# R0 (normal build, real TCP, isolated PG18, durability on)
scripts/local/test.sh syncperf --profiles S60,S60H,S240 --iterations 30 --warmups 3

# R5 candidates
RAI_SYNC_WRITE_CANDIDATES=1 RAI_SYNC_WRITE_ITERATIONS=10 RAI_SYNC_WRITE_WARMUPS=2 \
  RAI_SYNC_WRITE_PROFILE=S60 scripts/test-postgres.sh \
  go test -tags=integration -count=1 -run '^TestWorkoutWriteCandidates$' -v ./integration

# R5 plans
RAI_SYNC_WRITE_PLANS=1 scripts/test-postgres.sh \
  go test -tags=integration -count=1 -run '^TestWorkoutWriteStrategyExplains$' -v ./integration

# R0 tail attribution (per-iteration WAL / FPI / checkpoint)
RAI_SYNC_PERF_ITERATION_STATS=1 scripts/local/test.sh syncperf \
  --profiles S60,S240 --iterations 30 --warmups 3

# bare-PostgreSQL write floor (no HTTP, no Go application layer)
RAI_SYNC_BARE_PG=1 RAI_SYNC_BARE_ITERATIONS=3 RAI_SYNC_BARE_PROFILES=S60,S240 \
  scripts/test-postgres.sh go test -tags=integration -count=1 \
  -run '^TestBarePGWriteCeiling$' -v ./integration

# identity-check scaling diagnostic
RAI_SYNC_IDENTITY_SCALING=1 RAI_SYNC_IDENTITY_IMPORTS=12 RAI_SYNC_IDENTITY_PROFILE=S20 \
  scripts/test-postgres.sh go test -tags=integration -count=1 \
  -run '^TestIdentityCheckScaling$' -v ./integration

# R2-F21..F27 gates (real isolated PostgreSQL, race gate untouched)
scripts/test-postgres.sh go test -tags=integration -count=1 -timeout=60m \
  -run '^TestWorkoutWriteStrategy|^TestStrict' -v ./integration

# which 503 a slow-but-valid import produces (burns ~30s of wall clock by design)
RAI_SYNC_TIMEOUT_PROBE=1 scripts/test-postgres.sh go test -tags=integration -count=1 \
  -run '^TestWorkoutBundleStatementTimeoutCode$' -v ./integration
```

Every command above creates a disposable cluster under `/tmp/rai-pg.XXXXXX` with
`fsync=on full_page_writes=on synchronous_commit=on`, `shared_buffers=64MB`, `max_connections=40`,
`--data-checksums`, and drops it afterwards (`RAI_TEST_LEFTOVER_DATABASES=0`,
`RAI_TEST_CLUSTER_REMOVED=1` in each log).
