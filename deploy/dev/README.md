# Isolated development Compose

This configuration is for synthetic development only. It uses a project-scoped
PostgreSQL volume and a development-only password, not production secrets or
roles. Pick a unique Compose project name and an unused loopback API port.
The database listens on loopback port 55432 by default; set `RAI_DEV_DB_PORT`
to another free port when running multiple projects. Its initial encoding is
UTF-8 and its server timezone is UTC.
Migration is an explicit one-time command, never part of API or worker startup.

```sh
export COMPOSE_PROJECT_NAME=rai-dev-example
export RAI_DEV_API_PORT=18080
docker compose -f deploy/dev/compose.yaml config
docker compose -f deploy/dev/compose.yaml build api
docker compose -f deploy/dev/compose.yaml up -d --wait postgres
docker compose -f deploy/dev/compose.yaml run --rm --no-deps api migrate
docker compose -f deploy/dev/compose.yaml run --rm --no-deps api migrate-status
docker compose -f deploy/dev/compose.yaml up -d --wait api worker
curl -fsS http://127.0.0.1:18080/health
curl -fsS http://127.0.0.1:18080/ready
# Only for a disposable project whose volume may be destroyed:
docker compose -f deploy/dev/compose.yaml down --volumes --remove-orphans
```

Do not use `init` for this infrastructure check: it creates credentials. API and
worker share the same image and use different subcommands. Worker only recovers
expired leases; no analysis handler is implemented. The worker is monitored for
process exit, while the API healthcheck checks process liveness. Use `/ready`
to check database and schema readiness separately.

`migrate` applies the embedded goose migrations; `migrate-status` reports
their status. `migrate-down` requests a single rollback and exits nonzero
unless the current SQL migration has been explicitly reviewed and its complete
file hash added to the allowlist in `internal/admin/admin.go`. It rejects empty
Down sections and unknown applied migrations. The existing 00001–00010
migrations are not approved: they contain durable facts, receipts, or fencing
state, so the command refuses to downgrade them and preserves their version
records. Correct those schemas through a reviewed forward migration; do not
use a raw goose command to bypass this policy. Future approved Down SQL must be
tested against synthetic data and must not discard durable facts or weaken
protocol safety. RAI migration commands serialize against each other with a
PostgreSQL advisory lock; external migration tools do not participate and
must not be run concurrently. Stop API/Worker before schema maintenance.

For host development, start `postgres`, run the explicit migration, and set
`RAI_DATABASE_URL` as shown in `.env.example`. In `apps/server`, run
`go run ./cmd/rai api` or `go run ./cmd/rai worker`. API and worker use a
maximum of 8 and 4 connections by default, respectively; pgx URL parameters
`pool_max_conns`, `pool_min_conns`, `pool_max_conn_lifetime` and
`pool_max_conn_idle_time` can override the pool settings. Avoid logging URLs,
which may contain passwords.
