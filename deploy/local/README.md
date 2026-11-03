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
scripts/local/test.sh       # gofmt, unit, vet, sqlc, race + integration on a disposable PG cluster
scripts/local/contracts.sh  # OpenAPI contract lint with pinned redocly + Node
```

`verify.sh` recreates only the local `rai_dev` database, restores the backup into
a temporary database, validates that the restored schema version matches the
newest committed migration (20 at the time of writing, counted from
`apps/server/migrations/*.sql` so the gate cannot go stale) and drops that
temporary database. It writes `docs/evidence/local-dev-environment.json`.

`test.sh` calls the existing [`scripts/test-postgres.sh`](../../scripts/test-postgres.sh),
which starts a throwaway Unix-socket-only PostgreSQL 18 cluster, runs the full
integration suite with `-race`, verifies no temporary databases were left, and
removes the cluster. It never reads ambient `PG*` variables or a remote DSN.
Before that it runs `sqlc generate` with the pinned 1.31.1 binary and fails if
`apps/server/internal/storage/postgres/dbgen` would change, matching the CI gate.

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
