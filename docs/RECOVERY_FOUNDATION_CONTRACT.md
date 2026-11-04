# M5B Physiological Recovery Foundation v1

Candidate-only; synthetic engineering evidence, no physiological validation. `rai.recovery.physiological/v1` is separate from legacy `recovery/v1` and `readiness/v1`. The active channel, product readers and training generator remain on their existing inputs. Readiness is NOT IMPLEMENTED. No deployment or real-data migration is authorized.

## Inputs and calendar

Only immutable M5A SDNN (ms), source-derived Resting Heart Rate (count/min), and six-category Sleep intervals are accepted. No ordinary heart-rate reconstruction, workout, load, running ability, VO₂max or plan input exists in the pure package. M5A raw identity, anchors, receipts, coverage and delete precedence are unchanged.

Each assignment freezes the dataset IANA timezone in configHash, assignmentID and a timezone-specific day scope. Explicit calendar input contains civil midnight boundaries and civil 00/06/12/18 bucket starts, including 23/25-hour DST days. CalendarHash binds actual absolute boundaries. Event time, database first-received time, capture time, local day and dataset revision remain distinct. A timezone change withdraws old current pointers; an explicit administrative assignment is required to compute under the new timezone. Immutable historical artifacts keep their original timezone and calendar identity.

The loader reads one REPEATABLE READ snapshot, with a 28-day history plus today and two prior days of sleep context. End-time range queries and an 8192 combined fact/tombstone cap prevent lifetime scans. Quantities before the 28-day window are excluded. Future-ending samples are excluded; each historical daily sleep projection separately excludes intervals ending after that day's boundary, so a later episode cannot rewrite earlier baseline features. Revised calculations admit late-arriving historical facts known at capture. Pure `as_known` input additionally requires knownAt≤asOf and revision≤knownThroughRevision. Storage does not claim to reconstruct missing deleted history: `LoadHistoricalRecovery` explicitly fails; historical audit returns the original immutable run, labelled revised/capturedAt, and a withdrawal indicator.

## Daily canonicalization

- **SDNN:** median of unique semantic observations within one source/device/revision/algorithm/acquisition-duration epoch; independent daily weights, no sample-count confidence bonus. Four civil-hour bucket counts, sample count, epoch and last event are retained. This is a **daily SDNN representative**, not the architecture paper's proposed nocturnal/rest-context estimator. No morning label or Apple Watch origin is inferred. Multiple live epochs or contradictory live sync revisions are ambiguous. User-entered samples are excluded without changing raw data.
- **RHR:** source-derived statistic only. Equivalent semantic duplicates collapse for derived support. Multiple different live daily estimates/revisions are ambiguous, never averaged. Replacement requires M5A delete+new UUID. A source/device/algorithm change starts a separate baseline epoch.
- **Sleep:** source-separated episodes join intervals with gaps≤90 minutes. Episodes longer than 24 absolute hours are ambiguous. The end/wake local day anchors the episode. More than 90 asleep minutes qualifies for main-episode selection; shorter episodes are nap diagnostics, not a safety floor or penalty. The longest eligible episode wins, ties by later end; multiple main-eligible source epochs are ambiguous. A 90-minute observation quiet gap prevents an unfinished tail from being called complete. This is an engineering session rule, not evidence that HealthKit discovery is physiologically final.
- Sleep canonicalization sweeps a union timeline. Awake takes precedence over asleep; inBed alone does not become asleep. Stage overlaps count once; stages and overlap counts are diagnostics only. Known awake, unknown gap, asleep duration, continuity and known timeline fraction are retained. At least 80% known timeline is required; lower coverage is unavailable. Unknown or overlapping segments reduce evidence support. No REM/deep/core weights, Sleep Score, 8-hour target or absolute short-sleep safety penalty exists.

Missing input remains missing. An empty upload never creates a normal component. Missing Sleep never creates sleepDeviation=0 or sleepBurden=0.

## Baselines

The existing architecture contract §9.2 specifies **28 days**, overriding the launch request's conditional 42-day suggestion. For target D, only `[D−28,D−1]` features from the same component epoch enter its baseline. At least 14 usable days AND at least 3 of the preceding 7 days are required. State is collecting below that requirement, provisional at 14–27 days, established at 28. Output includes window days, usable/recent days, epoch, median, MAD and floor flag.

SDNN uses `ln(ms)`. Center is median; sigma is `max(1.4826×MAD,floor)`. Floors are 0.10 log-SDNN, 3 bpm RHR, 0.5 hours sleep (sleep sigma is diagnostic). Today's observation never enters its baseline. A single outlier does not move a stable median. A sustained step moves a 28-day median around the 14th–15th new daily observation; there is no extra smoother or claim of a smooth physiological response.

## Formula

All coefficients, thresholds, freshness/quality factors and the timezone are hashed configuration and **engineering hypotheses**. v1 supports only its frozen default config; unsupported configurations fail rather than publishing success.

```text
rawH = (median(log historical SDNN) − log(today SDNN)) / sigmaH
rawR = (today RHR − median(historical RHR)) / sigmaR
clippedH/R = clamp(rawH/R, −7, +7)
aH/R = clamp((clippedH/R − 1)/2, 0, 3)
rawS = (personal median main-sleep hours − today main-sleep hours)/1.5
clippedS = clamp(rawS, −2, +2)
aS = max(0, clippedS)
Baut = 0.25 max(aH,aR) + 0.10 min(aH,aR)
Bsleep = 0.15 aS
Btotal = Baut + Bsleep
P = 100 exp(−Btotal)
```

The max/min autonomic formula and weights preserve architecture §8.2. The midpoint/regularity term is deliberately deferred: M5A does not establish a validated stable main-sleep regularity context. Duration-only deficit is explicit in the policy identity. One hour of deficit multiplies P by exp(−0.1), with other inputs fixed. No positive-deviation rewards exist. Raw/clipped deviations, component burdens, exact max/min-selected HRV/RHR contributions, Sleep contribution and totals are retained for reconstruction. Score 100 means no observed model burden; it does not mean medically perfect or fully repaired tissue.

## Missingness, freshness and quality

Full `score` requires all three fresh components, their eligible baselines, and one caught-up current-collector stream per type. Each latest discovery must be within 24h of evaluation; this only supports received synchronization, not read permission or all-source completeness. There is no fixed morning finalization hour.

When H/R are both supported but Sleep/baseline/coverage is incomplete, full score remains null. A separately named `autonomicOnly` reference may be available with status partial; Sleep deviation/contribution remains absent. One autonomic component cannot produce either full score or a falsely complete trend. Other states include collecting_baseline, insufficient_data, source_conflict and stale. `complete` means the model's evidence contract was met, not physiological correctness.

Each component has its own event time, age, 24h freshness threshold and state. No prior-day component is carried into a missing current day. Freshening decays support, never increases the observed score, and clears score/reference when an observed component becomes stale.

Confidence means **evidence support, not probability**. Full support uses the minimum `min(usableDays/21,1)×exp(−age/72h)` across components, with cap 0.60 (provisional cap 0.45). Incomplete version/device/OS context multiplies support by 0.7; sleep uncertainty multiplies it by 0.8. Partial-reference cap is 0.10, then quality factors, so missingness cannot improve support. These caps and factors are uncalibrated engineering parameters. Duplicate observations do not increase independent days or confidence.

## Persistence, dependencies and fences

Migration 26 extends artifact/config/dependency/result constraints and indexes only. No duplicate raw or Readiness table exists. One daily candidate job atomically stores three immutable artifacts: daily_health_features (29 summaries), daily_health_baseline and physiological_recovery, plus scalar result, publication, run status, clean generation, job and attempt completion. Summaries and bounded typed refs/hashes are stored; raw SDNN/Sleep arrays are never copied. Each artifact is bounded by 2 MiB. The publisher independently recomputes and checks snapshot/run hash and metrics.

The persisted graph has raw→features, the preceding 28 feature days→baseline D, feature D+baseline D→recovery D, and recovery→future readiness. Future readiness receives durable dirtiness with **no job and no handler**. Only existing requested affected output scopes are automatically recomputed; the day poll creates today's scope without scanning raw or filling missing history. Explicit admin compute accepts one date in the preceding 366 days. Exact operation retry is inert.

Quantity end-day D impacts D…D+28. A normal Sleep interval `[s,e]` conservatively impacts `local(s)−1…local(e)+30`, covering session boundary and two-day loader context. An unsupported >24h raw interval uses bounded end-day context, never a lifetime fan-out. Unseen tombstones retain the M5A barrier without inventing a time range. Coverage observations target their observed day; loss of caught-up state withdraws old pointers without automatically rebuilding history. Session/config/timezone changes withdraw old pointers and automatically target only existing scopes in a 30-day live context. Historical revised computations remain explicit.

Fact deletes and revisions withdraw affected pointers and increment generations in the same M5A raw commit transaction. Dataset lock → job/dirty locks → bounded reread rechecks raw/tombstone refs, coverage, baseline context, config, generation, assignment, dataset epoch, collector/device and lease. Old work becomes SUPERSEDED. Final wall-clock lease checks protect the atomic commit. Current admin reads use one stable snapshot and reread the same bounded context, then age freshness. `rai_api` cannot write artifacts/results or assignments; `rai_worker` cannot modify raw health/coverage. Ordinary logs and CLI inspection emit status/count/hash/reason/duration only.

## Acceptance limits

Synthetic Go/PG18/property/fault/role/query-plan evidence is engineering evidence. Real-device behavior, SDNN context/distribution, RHR revision behavior, sleep distributions, source switching, baseline stability, illness/fatigue prediction, population calibration, WAN and production remain NOT VERIFIED. No medical inference or run-today recommendation is produced. M6 is a separate future phase.
