# M5B engineering evidence

Client date: 2026-11-04. All data synthetic; all PostgreSQL runs use a new disposable socket-only PG18 cluster. Production, WAN and real HealthKit/device physiology are not verified. [Result](../../M5B_RESULT.md), [contract](../../RECOVERY_FOUNDATION_CONTRACT.md), [machine summary](validation.json).

Final gates:

- `baseline.json`: required M5B BASELINE; preexisting worktree changes preserved.
- `frozen-and-sqlc.json`: migrations 1–25 / M5A raw path & contract / original model document unchanged; sqlc regeneration identical; diff check.
- `gates.log`: full Go unit + race, vet + integration vet, real import boundary checks, diff check.
- `go-pg-race.log`: complete `go test -race -count=1 -timeout=20m -tags=integration ./...` during implementation; no failed packages; all isolated databases and cluster removed.
- `pg-acceptance.log`: final-source M5B/migration focused PG18/race revalidation, roles, snapshot, old-worker fencing, actual backend termination, incremental/rebuild, boundaries and plan. Minor later additions are test-only and run separately below.
- `isolation-final.log`: raw-fact transaction rollback, two actual different M2 load pipelines producing identical physiological Recovery, all product/training DTO isolation.
- `performance-final.log`: final synthetic 14/28/42/90/180/365-day PG18/race matrix; includes actual persisted jsonb bytes.
- `final-domain.log`: final pure-domain race tests, integration vet and allocation/stage benchmarks.
- `swift-daily-probe-build.log`: rebuilt DailyProbe from unchanged Swift source.
- `swift-daily-live.log`: rebuilt Swift probe -> actual HTTP -> PG18/race, SIGKILL at three boundaries, original anchor/receipt recovery, actual shared 429 and Sleep fairness.
- `swift-daily.log`: failed attempt to run package Daily tests; current CommandLineTools has no `Testing` module. **Unverified**, not a pass.

Other logs are development iterations. In particular, `pg-recovery-final.log` contains an early test assertion that expected an error instead of the valid SUPERSEDED/no-artifact outcome; the final test accepts the explicitly fenced outcome, and the final acceptance logs pass. Earlier benchmark logs are superseded by `final-domain.log` and `performance-final.log`.

Reproduce from repository root (uses the bundled local toolchain, never an ambient database URL):

```bash
source scripts/local/lib.sh
rai_go_env
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -timeout=20m -tags=integration ./...
```

Current candidate inspect is CLI-only and redacted. `rai analysis recovery compute --dataset <explicit> --day YYYY-MM-DD --operation <explicit> --actor <explicit>` creates one bounded candidate request; `rai analysis recovery inspect --dataset <explicit> --day YYYY-MM-DD` reports status/count/hash/reasons. No promotion action exists. Do not point these engineering acceptance commands at production data.
