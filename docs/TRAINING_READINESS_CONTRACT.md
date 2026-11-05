# M6 Training Readiness Candidate v1

`rai.readiness.training/v1` is an **instant snapshot of recorded running training readiness**, calculated from the current M5B candidate and M2 immutable load ledger. Candidate only; no product, Coach, plan generator or active-channel integration. This is an engineering hypothesis, not a diagnosis, injury/illness probability, ability, VO₂max, race prediction or a run-today recommendation. A score of 100 is only high support under this model. Confidence means evidence support, never probability. Real device acceptance remains DEFERRED; physiological validation remains NOT VERIFIED.

## Model boundary

```text
rai.training_load.hrr_integral/v1 ledger ─→ recent burden + personal load scale ─┐
rai.recovery.physiological/v1 current candidate ────────────────────────────────┴→ rai.readiness.training/v1
```

M2 and M5B formulas, parameters, confidence, baseline and time constants are unchanged. M3, M4, plans, raw HRV/RHR/Sleep, pace, power, distance, workout HR, E7/E42/balance/ratio do not enter the typed numerical input. The loader reads Workout identity, activity classification and completion time only to select recorded running facts and detect missing ledger rows; it never reconstructs load. It verifies M2 artifact hash, dataset/workout identity, current clean publication, model/config identity, calendar compatibility and the **formal daily ledger** against its immutable M2 artifact. Different effective-dated HR-reference IDs are compatible when their current clean M2 publications use the same frozen model/config and calendar; their IDs and artifact hashes remain lineage. A superseded parameter context withdraws these current publications through the existing M2 graph. Legacy TRIMP is never mixed in.

## Frozen configuration and mathematics

All of the following enter configHash and the artifact. Unsupported configuration fails; no silent fallback is supported.

| Parameter | v1 |
| --- | --- |
| τload | 36 absolute hours |
| Burden lookback | 180 absolute days, inclusive lower/end≤asOf |
| Baseline | 42 complete prior civil days; today excluded |
| Baseline eligibility | Complete, compatible, nonzero daily ledger sums |
| Maturity | ≥5 training days AND first-to-last civil-day difference≥14 |
| Returning-athlete freshness | Latest usable training day≤14 civil days ago |
| B floor | 1 RAI_AU; only after a measured eligible median exists |
| Confidence cap | 0.55 |
| Coverage observation max age | 24 hours, additionally limited by civil-day boundary |
| Workouts / streams | ≤4096 / ≤128 per snapshot |
| Artifact | ≤2 MiB |

```text
D(asOf) = Σ Li × exp(−(asOf − end_i)/36h)
B = max(1 AU, median(nonzero complete daily loads over prior 42 civil days))
loadMultiplier = exp(−ln(2) × D/B)
Readiness = P × loadMultiplier
```

Each completed Workout contributes its whole M2 AU once, decaying from **end time**; cross-midnight daily M2 ledger splits do not split burden or give separate end times. Same-day workouts decay independently, then sum. Future-ending workouts do not contribute. The 1 AU floor prevents unstable division for an actually observed extremely small median; it does not provide a baseline for a new athlete. The raw median and floor flag remain visible in the artifact. Maturity, history span, unknown and known-zero days are retained separately. A single very large workout does not dominate the median; sustained changes replace old days in the bounded window.

For valid input, `0≤Readiness≤P≤100`; D=0 returns P, D=B returns P/2, D=2B returns P/4. No low-training bonus exists. Recovery itself remains unchanged.

## Certified finite tail

A 7-day cutoff cannot give an unconditional absolute error bound from an exponential weight alone: neither a historic workload rate nor the count before that cutoff is bounded by such a weight. v1 therefore chooses a longer **finite** 180-day window with a conservative structural proof, rather than inventing a daily training ceiling:

1. A valid M1/M2 workout lasts at most 24 hours. Accepted HRR≤1; gap scaling cannot exceed the same full-active-duration maximum. M2 intensity is monotone over [0,1]. Therefore maximum M2 AU is `1440 × (1/0.65) × exp(1.92×0.35) ≈4338.05`, conservatively below **10,000 AU**.
2. Current loads are selected once per Workout UUID. There are at most `2^128` distinct UUIDs, irrespective of history, duplicates or immutable revisions.
3. Each omitted Workout ended more than 180 days ago, so

   `omittedTailAU ≤ 2^128 × 10000 × exp(−180×24/36) < 3×10⁻¹⁰ AU`.

4. Since B≥1, the score error is at most `100 ln(2) × omittedTailAU < 2.1×10⁻⁸ score points`, using `1−exp(−x)≤x`.

This proof concerns valid **recorded M2 AU**, not unrecorded exercise or unknown loads. It needs no lifetime count, lifetime query or population-rate assumption. The artifact records the numeric tail and score-error upper bounds. The cost is a deliberately conservative window: any unknown/partial recorded load within 180 days blocks a complete score, even if old. Shortening this window requires a separately reviewed certified sufficient statistic or a valid stronger bound.

## Knownness and missing input

A full score requires M5B status available, quality complete, nonnull score, current identity and unexpired evidence. `autonomicOnly` is never treated as full Recovery. Missing, collecting, partial, source-conflict and stale Recovery leave score null.

A known running Workout with no current M2 publication, null AU, partial M2 load or incompatible lineage leaves the recent burden unknown and the full score null. Baseline days containing any such contribution are unknown, not zero. A pending cross-midnight workout marks every intersecting bounded baseline day unknown. Complete zero days are counted as known zero and excluded from the nonzero median. No workouts plus reliable coverage means **recorded running load=0**, not proof of rest or whole-body load=0. Unsynchronized cycling/strength and unsupported external exposures are not imputed.

Workout Protocol 3 keeps its fact format unchanged. The independent [WorkoutSyncCompletenessManifest / 1](WORKOUT_SYNC_COMPLETENESS_CONTRACT.md) control plane binds current collector, stream set, anchored page chains, fixed predicate/capture cutoff, inventory and durable fact receipts. M6 consumes only the server-validated immutable receipt while its relevant fact revision remains current. A registered stream, individual upload, receipt or client flag alone is insufficient. Ordinary `rai analysis readiness compute --dataset … --operation … --actor …` enrolls only M6 and asserts no coverage; without this verified transport evidence its candidate stays `sync_incomplete`. It neither enrolls upstream models nor activates product readers.

The optional assignment `syntheticCoverage` remains a **versioned M6 test feature**, accepted only with explicit `syntheticOnly=true`, coverage identity, from/through bounds, caughtUp and a hash of every current-collector Workout stream's ID/fingerprint/client-sequence/batch identity. It is written by isolated synthetic fixtures, has no CLI setter or online API, and remains visibly synthetic in the artifact. A changed stream manifest invalidates it. It proves synthetic engineering behavior only and cannot replace real-device acceptance of the independent completeness transport. Actual real-device results are recorded separately in UA1-R evidence.

## Snapshot, freshness and projection

`ReadinessSnapshot` binds dataset epoch, collector, M6 assignment, M2 candidate stack/config/assignment, generation, capturedAt, explicit asOf, 43 supplied calendar boundaries, typed M5B identity, bounded M2 refs and independent coverage. Civil timezone/DST is resolved outside the pure domain and actual boundaries are hashed. Baseline grouping uses M2 calendar-compatible civil days, while burden uses absolute elapsed microseconds.

M5B is read from today's clean candidate publication in the same REPEATABLE READ transaction as config, coverage, ledger and identity. M6 does not reread M5A raw data. M5B score/support are aged using its existing Freshen policy. Recovery expiry is the earliest of the three M5B component freshness deadlines and its coverage deadlines. No prior day's Recovery is substituted. An explicit earlier asOf cannot consume a later M5B result.

One event creates an immutable anchor; no minute-by-minute jobs exist. `CandidateReadiness(evaluationAt)` revalidates the same bounded dependencies at the original asOf, then returns a deterministic Freshen projection. It never updates the artifact. Burden is recomputed from stored sufficient terms with the finite cutoff; support never increases. Projection stops at Recovery expiry, coverage expiry, the next civil day, or a known future Workout's completion boundary. New facts/revisions/withdrawals/config invalidate the pointer; the reader cannot decay an old artifact indefinitely. Daily polling queues at most one boundary update of an already enrolled clean scope; pure time passage within that scope creates no immutable run.

## Status, quality, support and explanation

Full v1 output has status `available`, quality `complete`. Other supported statuses are `collecting`, `insufficient_data`, `stale`, `sync_incomplete`, `source_conflict`; partial readiness is not implemented. `complete` means only that this engineering input contract was met.

Load support uses independent usable-day and history-span maturity with coverage/knownness hard gates; AU magnitude and workout count do not reward confidence. Full support is capped by `min(recoveryConfidence, loadSupportConfidence,0.55)`. Freshen uses the unchanged M5B 72h support aging convention. Unknown inputs cannot improve full support. No physiological accuracy is implied.

Artifacts expose P, its full pre-load contribution, D, observed median/floored B, D/B, multiplier, final float64 score, separate Recovery coverage/load knownness/baseline maturity, config/hash, asOf/evaluationAt, input refs, generation, lineage and stable reason codes. Codes include `recovery_missing`, `recovery_partial`, `recovery_stale`, `recovery_source_conflict`, `load_baseline_insufficient`, `recent_load_unknown`, `load_sync_incomplete`, `load_parameter_conflict`, `candidate_superseded`. These reasons belong to versioned v1 semantics.

## Persistence, dependency graph and fencing

Migration 27 extends existing artifact/result/family/assignment/dependency allowlists, fixed trigger functions, one bounded Workout end-time index and the worker's fixed invalidation function privilege. No readiness score-current table, duplicate raw store, perpetual daily history or independent recent-burden state is added. M2/M5B schema and formulas remain intact. A restricted artifact trigger derives graph edges from M6 refs, so the Worker gains no general dependency-write permission.

Dataset-locked raw Workout insert/delete, M2 dirty/publication changes, current-day M5B publication/replacement/withdrawal, stream manifest, config/assignment, timezone, dataset epoch/retirement, collector and device validity withdraw current and dirty the enrolled scope in the **same transaction**. Event-time eligibility limits automatic Workout invalidation to the 180-day/current-day context. Existing immutable history and receipts remain unchanged. M5B's old future-readiness dirtiness remains inert unless M6 is separately enrolled.

Publish uses the existing dataset→job→dirty lock order and independently reloads the complete bounded typed snapshot at captured asOf. It checks Recovery run/hash/generation, every current M2 identity and daily ledger, deletes, coverage/manifest, M2 and M6 assignments/configs, dataset/collector/device, generation and lease/fencing token. Changed inputs become SUPERSEDED. It recomputes output/metrics and checks the original run's input hash. Artifact, dependency edges, scalar, publication, clean generation, run terminal, job terminal and attempt terminal commit atomically. Final wall-clock lease checks remain in force. Publication uses captured asOf; a later reader enforces evaluation-time expiry instead of pretending an expired anchor is current.

## Privacy and acceptance limits

Ordinary logs and default admin inspect emit status/reason/count/hash/duration only: no P/readiness/AU/raw health/full UUID values. Numerical artifacts and testing access require an explicit dataset and administrative database access; synthetic proof flags remain visible. The API cannot read artifacts/ledger or write candidate assignments/results; Worker cannot mutate raw, coverage, assignments or immutable history.

Tests and performance evidence are synthetic engineering evidence, not deployment/device/population acceptance. No Swift feature was changed. Strength/cycling completeness, real Workout coverage, real Recovery distribution, physiological readiness validity, real training response, illness/injury prediction, population calibration, WAN and production remain NOT VERIFIED. M7 is a recommendation only and must not start automatically.
