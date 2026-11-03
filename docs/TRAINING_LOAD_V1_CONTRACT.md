# M2 — Training Load V1 contract

2026-11-03 (Asia/Shanghai). Candidate-only model implementation; synthetic engineering validation does not establish physiological validity. Scope is **recorded running load**. Real-device acceptance is DEFERRED. This model does not implement ability, VO₂max, fatigue measurement, recovery, readiness, injury risk or training advice.

## Formula, unit and numerical evaluation

Identity: `rai.training_load.hrr_integral/v1`; state identity: `rai.load_state.exp_finite/v1`. Legacy `training_load.trimp/v1` is retained independently.

For explicit reference inputs `R = HRrest_ref`, `M = HRmax_ref`:

```text
r(t) = (HR(t)-R)/(M-R)
f(r) = (r/0.65) exp(1.92(r-0.65))       for 0 < r <= 1
f(r) = 0                              for r <= 0, with M1-qualified HR
L = integral(active f(r(t)) dt) / 60
```

One `RAI_AU` is one model minute at `r=0.65`. It is neither kcal, TSS, original TRIMP units nor any vendor load unit. Workouts are evaluated as time-resolved segments, never `f(meanHR)` when trace support exists. Easy active recovery intervals count; pauses do not.

For a linear segment with endpoint HRR `a,b`, let `m=(a+b)/2`, `δ=(b-a)/2`, `z=1.92δ`. Its mean intensity is

```text
exp(1.92(m-.65))/.65 × [m sinh(z)/z + δ (z cosh(z)-sinh(z))/z²].
```

For `|z|<.01`, evaluate the two analytic functions by their even/odd series through `z^8` / `z^9`; this removes near-equal primitive subtraction. A linear crossing of zero is split analytically; HRR above one is not clamped. Seconds are divided by 60 only once. Production uses no second-by-second sampling. Independent Simpson quadrature verifies the defining integral. Reference vectors: 60 minutes at .65 = 60 AU; 30 minutes at .45 plus 30 minutes at .85 = approximately 71.743050815 AU.

## M1-compatible HR exposure

Frozen `rai.workout_features/1` is unchanged. `rai.hr_exposure/1` is a separately versioned companion in the same feature run, produced only for explicitly M2-enabled candidate graph edges. Sources are dictionary-indexed; each segment uses the exact tuple `[startUs,endUs,HR_start,HR_end,sourceIndex,reconstructed,activeSegment]`. It contains active intervals and deterministic `[start,end)` linear HR segments with source epoch, active-segment index and reconstructed/observed quality. Only exactly constant contiguous segments with identical source/quality/active segment merge. No lossy compression, complete raw database, metadata dump, presentation points, speed, GPS or unrelated samples are persisted in this companion.

The extension calls M1 `BuildPause`, `BuildSignal` and parameter resolution. It adds no private outlier/source/pause implementation in Training Load. M1 interpolation remains same-source, same-active-segment, **gap <=15s**, with no extrapolation or pause crossing. Instant points alone have zero duration. Artifact limits: 20,000 segments and 2 MiB; exceeding them fails the job, without truncation or fake insufficient success. Feature artifact remains <=128 KiB. M1-only jobs do not acquire M2 artifact limits or companion requirements.

## Reference parameters and as-of resolution

Reuse append-only `athlete_model_inputs`, `kind=hr_reference`, schema `rai.hr_reference/1`. Each parameter has explicit resting/max HR, ID/hash, provenance, recorded-at time, effective-from/through, supersedes and revoked fields. Workout start chooses the event-time interval; feature as-of excludes inputs recorded later. Overlapping unresolved references are conflicts. Explicit supersession is required for revised history; the new feature/load run records the new identity and retains old history.

There is no age formula, default RHR, workout-minimum RHR, workout-maximum HRmax, or import of today's legacy parameter into historical workouts. Missing input means `load=null`, `status=insufficient_data`, `parameter_missing`. Observed high HR is conflict/lower-bound evidence, never automatic HRmax calibration. M2 freezes **zero conflict tolerance**: a segment with either endpoint above reference HRmax is excluded; any such positive duration makes the whole load unavailable. This is deliberately more conservative than the permitted nonzero conflict threshold. It cannot be gap scaled or summary-replaced.

## Methods, qualification and missing time

| Method | Conditions | Quality |
|---|---|---|
| `hrr_integral` | reliable M1 trace with complete active support | complete |
| `hrr_integral_gap_scaled` | reconstructed coverage >=95%, each unsupported/native gap <=30s; retained M1 quality gates | partial |
| `summary_hr_approximation` | no supported trace; a unique whole-workout HR statistic, reliable Workout source, elapsed interval proven identical to active interval; no ambiguous pause placement | partial, confidence <=.35 |
| `unavailable` | missing/conflicting references, no qualified HR, uncertain active interval, disqualifying M1 quality, or excessive missing support | insufficient; load=null |

M1's stricter gates remain: ambiguous support rejects; M1 significant outlier exclusion and robust reference conflict qualification reject. Summary HR must have the same interval/unit, and cannot be a condensed parent masquerading as time support. Any supported trace takes precedence even when insufficient; trace and summary are never added. Summary returns an approximation, without observedLoad or trace coverage. Its confidence cap is part of immutable model configuration, not a measured probability.

Gap scaling records observedLoad, estimatedLoad, valid/active seconds, coverage, maxGap and scaleFactor. The explicit assumption is **missing time has the same mean intensity as observed time**. Total `L_est=L_observed*active/valid`. Per-day contributions allocate each day's missing active seconds at the whole-workout observed mean intensity, so an entirely missing short tail after midnight still receives its estimated share. Missing >5% or any gap >30s leaves load null; the optional observed value is only a lower bound. No imputation from plans, pace, running power, duration or session type is permitted. Sessions shorter than 180s retain any qualified small load and carry `short_session_hr_lag`.

External exposure retains active minutes and the M1 preferred distance even if AU is unavailable. No unqualified elevation estimate is manufactured. Power remains diagnostic/future input.

## Daily ledger and calendar

`analysis_load_ledger` is the sole additional model-specific table: dataset, local date, workout identity, immutable load run identity, nullable contribution, observed contribution, active seconds, quality, model version, reference ID and feature run ID. It does not have a second mutable current flag. Current contributions join existing candidate publication pointers and clean scopes. Results and ledger history are append-only; replacing/deleting a Workout removes its current references, not its historical values.

Loaders resolve the IANA location at the I/O boundary and hash its bounded midnight boundaries into the input identity. Pure calculators receive the resolved in-memory calendar and never call filesystem-backed `time.LoadLocation`. Split exact native HR segments at dataset IANA local calendar boundaries. Use calendar `AddDate` rather than UTC+24h. Pause support and partial segment interpolation are retained across boundaries. Tests include cross-midnight, spring 23h and autumn 25h calendars and conservation. Known workouts with unavailable load produce NULL daily contributions. No recorded Workout yields recorded load 0; this does **not** mean rest, complete device coverage or zero total-body load. Daily partial summary/scaled loads preserve partial quality through the finite state window.

## Finite state, unknowns and cold start

For τ=7 and 42 days, `a=exp(-1/τ)` and H=180:

```text
Eτ(d) = (1-a) sum(k=0..179, a^k L(d-k))
Eτ(d) = a Eτ(d-1) + (1-a)L(d) - (1-a)a^180 L(d-180)
```

`Kernel.Advance` maintains a 180-day ring and constant work per daily step. No infinite EWMA seed is carried across a rebuild. Input is the bounded per-workout ledger, never 180 days of HR samples. Known contributions and unknown weight mass use separate finite recurrences; a live unknown day makes complete acute/chronic null. Unknown mass is dimensionless kernel weight, **not a load bound**. When the last unknown/nonzero day exits, counts set the corresponding empty mass/contribution exactly to zero.

Outputs: acute=E7; chronic=E42; balance=chronic-acute (`RAI_AU/day`); ratio=acute/chronic only if chronic>1 `RAI_AU/day`; trend7=(E42(d)-E42(d-7))/7 (`RAI_AU/day²`). Negative balance/trend are valid. No form/freshness/readiness/diagnosis labels. Monotony is not implemented.

`historyDaysObserved` counts calendar days since first recorded ledger history, including retained history after deletion. `initial_state=zero_before_recorded_history` is an engineering initialization assumption. First 42 observed days are partial; earlier unknown training is not claimed absent. Partial daily methods also keep state partial while inside its window.

## Dirty ranges and recomputation

Same fact transaction: raw dirty -> feature candidate invalidation -> workout load invalidation -> athlete load dirty/current-pointer invalidation. Feature publication dirties its real load consumer; load publication dirties state. M1 future performance/VO₂/ability/endurance dependencies remain dirty **without fake jobs or success handlers**. No Recovery handler exists.

The exact acute/chronic contribution of a day ends at **day+179**, and day+180 contributes exactly zero. A mathematical dependency nuance is explicit: the requested 7-day difference trend still depends on that prior chronic state through **day+186**. M2 recomputes/invalidates this additional **trend tail**. This is not a 187-day acute/chronic kernel. Persisted state rows bundle these values, so a tail row can be replaced with unchanged acute/chronic and a corrected trend. Requiring both unchanged full rows after day+179 and exact persisted `trend7` would be inconsistent. Unaffected historical publication pointers outside the bounded output range are retained, while dirty scopes hide candidate reads until completion.

A single event reads at most 186 seed ledger days plus its bounded output range. Maximum output span is 552 calendar days; input ledger <=10,000 rows; explicit recompute dates <=366 days and <=1,000 intersecting workouts. Excess ranges fail explicitly rather than silently truncate. Large sparse pending ranges require bounded administrative batches; there is no unbounded history scan/recompute.

`rai analysis load recompute --dataset ... --from ... --through ... --model-version rai.training_load.hrr_integral/v1 --parameter-version rai.hr_reference/1` revises only intersecting workouts, with their effective as-of input IDs, and the corresponding bounded state range. `parameter-version` selects the supported parameter **schema**, not a forced physiology value/ID across history. To revise parameter identity, append an explicit effective-dated superseding HR input first. Seed history required by a state computation must already be M2-enrolled/current; pending dependencies return retryable pending, never zero. Daily advance dirties existing clean M2 scopes as the local calendar moves forward.

## Persistence, transactions and candidate publication

Reuse `analysis_runs/results/artifacts/publications/model_channels/model_dependencies`, job leases, dataset locking and dirty generation fencing. New scopes use the existing feature candidate stack:

```text
model/<feature-stack>/workout/<uuid>       -> companion exposure
model/<feature-stack>/workout_load/<uuid>  -> training load and daily ledger
model/<feature-stack>/athlete/load        -> finite state
```

M2 config hash is recorded in audited setup, run lineage and model artifacts. No second channel/publication framework. Feature schema/config stays frozen; active remains DB-constrained to legacy. Administrative compute is explicit, audited and idempotent for its operation UUID. The enabled dataset subsequently inherits real load edges for newly synced workouts. Current reads verify dataset/channel/config, clean state, successful run and typed artifact hash. Old runs retain immutable identities.

One existing `PublishAnalysis` transaction writes artifact, ledger/scalar results, publication, downstream dirty, scope clean, run terminal, job terminal and attempt terminal. Dataset/job/dirty locks and a final wall-time lease check fence publication. Generation N finishing after N+1 is SUPERSEDED without artifact/ledger/pointer or cleaning N+1. Faults/backend termination roll back every write and retain retry safety.

Migration 00022 only; migrations 00001–00021 are byte-preserved. Empty/21->22/repeat and migration20 compatibility paths use disposable PG18. API cannot read/write ledger or artifacts; Worker can SELECT/INSERT ledger and append artifacts but cannot update history or physiological inputs; owner/admin performs setup/parameter changes. No deployment, real data migration, iOS contract changes or active consumer change.

## Acceptance limitations

REAL DEVICE: **DEFERRED**. NOT VERIFIED: real Apple Watch HR sampling/gaps/coverage, interpolation suitability, HRmax conflict frequency, summary-only rate, eligibility rate, interval behavior, personal reference quality, physiological validity, relation to actual fatigue, production load, WAN, power loss and real-world calibration. Synthetic tests validate the model implementation and transaction behavior only. Performance evidence is local single observations, not production SLOs.
