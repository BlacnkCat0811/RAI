# Local development environment (macOS, no Docker)

This directory documents the native development path for this machine. It is
deliberately not a second Compose file: the existing
[`deploy/dev/compose.yaml`](../dev/compose.yaml) remains the containerized
option for machines with Docker. Nothing here is used in production.

The native path installs pinned toolchains **inside the workspace** under
`.local/toolchain` and `.local/bin` (gitignored), so it needs no `sudo`,
Homebrew or Docker. The first bootstrap downloads the Go archive (~72 MB), the
signed Postgres.app image (~123 MB), the sqlc archive (~16 MB) and the Node
archive (~48 MB); later runs reuse `.local/downloads`:

- Go 1.27.3
- PostgreSQL 18.6 (from the signed Postgres.app 2.9.6 image, only the bundled
  server and client binaries are used)
- sqlc 1.31.1 (`.local/bin/sqlc`, the generator version pinned by CI)
- Node 22.23.2 (`.local/toolchain/node`, only for the OpenAPI contract lint; the
  Go gate and the server CI job never use it)

## First-time setup

```sh
scripts/local/bootstrap.sh
scripts/local/pg.sh init
scripts/local/pg.sh start
scripts/local/stack.sh build
scripts/local/stack.sh migrate
```

`pg.sh init` creates a dedicated cluster at `.local/pgdata` listening only on
`127.0.0.1:55432`, with UTF-8, `timezone=UTC`, data checksums, and a
development-only password. `stack.sh migrate` applies the embedded goose
migrations explicitly; API and Worker never migrate on startup.

## Run the stack

```sh
scripts/local/stack.sh start
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/ready
scripts/local/stack.sh status
scripts/local/stack.sh logs
scripts/local/stack.sh stop
```

`start` waits for `/ready` before launching the Worker. Logs and PID files stay
under `.local/logs` and `.local/run`. Stop the database separately with
`scripts/local/pg.sh stop`; the cluster is preserved.

## Acceptance and tests

```sh
scripts/local/verify.sh     # empty DB -> migrations -> API/Worker -> health -> backup/restore
scripts/local/test.sh quick # cached Go unit tests, vet, dependency boundaries
scripts/local/test.sh gate  # cached Go race, vet, boundaries; transaction changes also need PG
scripts/local/test.sh full  # uncached race, vet, sqlc, full disposable PG regression
scripts/local/test.sh swift # native Swift package regression
scripts/local/test.sh e2e   # Simulator -> local Go HTTP -> disposable local PG18
scripts/local/test.sh perf  # synthetic 1/10/100/500-Workout throughput
scripts/local/contracts.sh  # OpenAPI contract lint with pinned redocly + Node
```

`verify.sh` recreates only the local `rai_dev` database, restores the backup into
a temporary database, validates that the restored schema version matches the
newest committed migration (20 at the time of writing, counted from
`apps/server/migrations/*.sql` so the gate cannot go stale) and drops that
temporary database. It writes `docs/evidence/local-dev-environment.json`.

`test.sh full` calls the existing [`scripts/test-postgres.sh`](../../scripts/test-postgres.sh),
which starts a throwaway Unix-socket-only PostgreSQL 18 cluster, runs the full
integration suite with `-race`, verifies no temporary databases were left, and
removes the cluster. It never reads ambient `PG*` variables or a remote DSN.
Before that it runs `sqlc generate` with the pinned 1.31.1 binary and fails if
`apps/server/internal/storage/postgres/dbgen` would change, matching the CI gate.

All development tests run on this Mac. The historical remote runners now fail
before making any SSH, Docker or database call. `quick` is the default and does
not replace completion gates. `gate` satisfies the existing Go race/vet/boundary
requirements; transaction changes must also execute the affected real PG tests.
Use `full` when a complete integration regression is required, rather than for
every edit. Go caches and Xcode build products persist;
integration databases are fresh for each invocation, with checksums and synchronous
durability enabled. `integration` keeps race enabled and uses a 40-minute test
budget; this does not change product timeouts.

For a small change, target the affected scope:

```sh
scripts/local/test.sh unit -run 'TestWorkout' ./internal/syncing
scripts/local/test.sh integration -run 'TestWorkoutCompleteness' ./integration
scripts/local/test.sh swift --filter HealthDiscoveryTests
RAI_LOCAL_ASSOCIATION_PERF=1 scripts/local/test.sh swift --filter WorkoutPerformanceTests/testDiscoveryAssociationScaling
```

The 500-Workout benchmark uses 60 scalar samples, three series points and three
route points per Workout, a real loopback HTTP server and durable PG commits.
It reports its finite, benchmark-only upload admission policy (burst 1024,
60000/minute), then restores the defaults. It measures transaction throughput,
not real HealthKit reads, dense long runs, Worker execution or phone performance.
The separate existing `TestPhase4PerformanceMatrix` remains the dense workload.
The completeness v2 manifest's existing 256-Workout bound is unchanged; 500
independent bundle imports do not claim a single 500-Workout manifest is supported.

`simulator` and `e2e` reuse a dedicated device named `RAI Local Tests` and a local
build directory; no physical iPhone is selected. E2E executes the existing R3 v2
HTTP/PG/lost-ACK test. Other optional external-host tests explicitly remain skipped
unless their own local fixture setup is provided. Neither this entry nor `full`
claims all historical Compose/capacity/physical-device scenarios were run.

## USB iPhone / Mac 独立验收

The user authorized a new local real-device acceptance environment on 2026-11-06.
It is separate from synthetic regression and from the old cloud acceptance run:

```sh
python3 scripts/local/device.py prepare  # build; isolated native PG18 + API/Worker
python3 scripts/local/device.py install  # update Debug app over USB, preserve container
python3 scripts/local/device.py probe    # iPhone -> Mac authenticated handshake; no HealthKit read
python3 scripts/local/device.py single   # real latest eligible Workout; retain selection for replay
python3 scripts/local/device.py report   # count/status/timing only
python3 scripts/local/device.py completeness # explicitly start full v2 catch-up when needed
python3 scripts/local/device.py status
python3 scripts/local/device.py stop     # stop processes; preserve data and credentials
```

Run commands sequentially. State, credentials, logs, PostgreSQL data and Xcode
products live under gitignored `.local/device-acceptance` (directory 0700; files
0600). Its PostgreSQL listens only on loopback port 55433; API/Worker use the same
native binary and the API binds only the selected Mac private LAN address on
port 18080. These development services use the dedicated database owner role;
production role/HTTPS/deployment acceptance is not implied.

USB is used for installation, launch and redacted reports. Phone-to-Mac API
traffic uses the reachable private LAN address; allow the App's local-network
permission. Set `RAI_LOCAL_DEVICE_HOST` before the first prepare only if automatic
LAN selection is unsuitable. The private-LAN HTTP path is Debug-only and limited
to the exact build-configured host; Release TLS behavior is unchanged.

The Debug local acceptance screen suppresses product polling and normal-session
startup. A separate `local-acceptance-<namespace>` Outbox is used without activating
or rebinding the normal Keychain/session. Original phone state and cloud data are
not migrated, deleted or reused as a completed local run. `probe` proves only
connectivity; `single` does not prove all-stream completeness. Full-history device
acceptance is an explicit occasional gate, not the daily development loop.

## Contract lint

`contracts.sh` runs the same check as the separate
[`contracts.yml`](../../.github/workflows/contracts.yml) workflow:

```sh
scripts/local/contracts.sh
```

It executes `redocly lint contracts/openapi-v2.yaml` with the pinned
`@redocly/cli@2.71.1` through `npm exec`, with the npm cache kept inside
`.local/npm-cache`, and writes `docs/evidence/local-contracts-lint.json`. Only
errors fail the script; the 31 warnings are the pre-existing baseline and are
neither suppressed nor silently fixed. The lint proves the document is valid and
self-consistent — it does not prove the Go handlers implement it, which is what
the integration tests and `rai version` metadata cover.

## Backup and restore

```sh
scripts/local/pg.sh backup
scripts/local/pg.sh list-backups
scripts/local/pg.sh restore .local/backups/<file>.dump
```

`restore` targets `${RAI_RESTORE_DB:-rai_dev_restore}` and never overwrites
`rai_dev` by default. These are development backups only; they are not encrypted
and do not replace the production backup procedure in
[`docs/P7_BACKUP_RESTORE.md`](../../docs/P7_BACKUP_RESTORE.md).

## Boundaries

- Synthetic development data only. The scripts unset ambient `PG*`,
  `RAI_DATABASE_URL` and `RAI_DATABASE_URL_FILE`, and always target the local
  `127.0.0.1:55432` cluster. Do not add a production or legacy `../running`
  database override.
- Do not run `rai init` for an infrastructure check; it creates device
  credentials that must be stored safely.
- `pg.sh reset` drops and recreates only the local `rai_dev` database. It is
  destructive by design and must not be repurposed for shared environments.
- The API listens on loopback by default; this configuration has no TLS,
  production roles, off-host backup, or deployment approval.
