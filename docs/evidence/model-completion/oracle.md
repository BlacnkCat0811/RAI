# Independent numerical oracles

All values are synthetic. Expected values are hand-derived, not copied from the implementation.

| Model | Oracle | Test |
| --- | --- | --- |
| VO₂ | `V = 3.5 + 0.2 * 156 / 0.65 = 51.5` mL/kg/min | `internal/vo2max/calculate_test.go` |
| VO₂ sensitivity | 8 corners HRrest±5, HRmax±10, coefficient 0.18/0.22; relative full width >0.30 → value null, candidate-only | same |
| VO₂ LOO | With 3 evidence units, leave-one-out has <3 fits → range null + `loo_support_insufficient`, confidence ≤0.4 | same |
| Recovery | `P=100*exp(-(Baut+0.15*aS))`; expected 88.25 for zH=2, no other burden | `internal/recovery/v2_test.go` |
| Sleep midpoint | 23:50 vs 00:10 circular distance = 20 min, not 23h40m | same |
| Readiness | D=0 → P=100; D=B → P=50; adding load at fixed P cannot raise P | `internal/readiness/v2_test.go` |
| Durability | constant EF halves → decoupling 0; pace decay is second-half/first-half speed ratio −1 | `internal/diagnostics/diagnostics_test.go` |
| Aerobic efficiency | `EF = speed / HR`; ratio of two steady segments equals inverse HR ratio when speed is fixed | same |
| Maximal ability | only confirmed/independently verified maximal annotations; ≥3 efforts, longest ≥2400 s and ≥2× shortest; uncalibrated cap 0.7 | `internal/performance/v2_test.go` |
| Critical speed | WLS `D = CS*t + D′`; LOO CS change ≤5%, relative RMSE ≤3% | `internal/diagnostics/diagnostics_test.go` |
| Load settling | `h = 36*max(0, ln(D/(0.25B)))`; buckets <12, [12,24), [24,36), [36,48), >48h | same |
| Backfill ranges | load d→d+179, trend d→d+186, RHR d→d+28, VO₂ d→d+47 hold d+74, ability d→d+102 hold d+129 | `internal/backfill/backfill_test.go` |
| Research CI | athlete-block bootstrap with deterministic seed; cluster smaller than minCluster suppressed | `internal/research/research_test.go` |
| Research pseudonym | HMAC-SHA256 keyed pseudonym; mapping length equals distinct athlete keys and never appears in report | `internal/research/pseudonym_test.go` |
| Research outcome independence | same Workout or overlapping input window → `overlapExcluded=1`, `paired=0` | `internal/research/analysis_test.go` |
| Research cluster | single athlete block deltas 1/2/3 → mean 2, CI 2/2; censored/zero denominator suppressed | same |
| Study addendum gate | six product model IDs open only with source/window/threshold/authorization/withdrawal/stopping rule | `internal/research/research_test.go` |

Property/boundary coverage includes point/interval/series/pause/source/gap, HR reference 365/90-day edges, overlap endpoint/no-overlap, DST/calendar date validation, deletion invalidation, future known-at rejection, and missing/null distinction.

Command:

```bash
cd apps/server && go test -count=1 -cover ./internal/vo2max ./internal/recovery ./internal/readiness ./internal/diagnostics ./internal/backfill ./internal/research
cd apps/server && go run ./cmd/rai-research -input ../../.local/logs/model-completion/research-study-overlap.json -out ../../.local/logs/model-completion/research-report-overlap.json
```
