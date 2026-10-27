#!/usr/bin/env bash
# Disposable Compose project, synthetic data only. Run on the Docker test host.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
export COMPOSE_PROJECT_NAME="rai-p1-$(date +%s)-$$"
export RAI_DEV_API_PORT=0
export RAI_DEV_DB_PORT=0
export RAI_DEV_IMAGE="$COMPOSE_PROJECT_NAME:local"
export RAI_DEV_GOPROXY="${RAI_DEV_GOPROXY:-https://goproxy.cn,direct}"
export RAI_DEV_GO_IMAGE="${RAI_DEV_GO_IMAGE:-docker.m.daocloud.io/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb}"
export RAI_DEV_POSTGRES_IMAGE="${RAI_DEV_POSTGRES_IMAGE:-docker.m.daocloud.io/library/postgres:18.6-bookworm@sha256:bbf83594798e144bff365a9c191c5d779f181fb3135902064ab8ff11918020fc}"
compose=(docker compose -f deploy/dev/compose.yaml)
cleanup() {
  rc=$?
  trap - EXIT INT TERM
  if ! "${compose[@]}" down --volumes --remove-orphans; then rc=1; fi
  if docker image inspect "$RAI_DEV_IMAGE" >/dev/null 2>&1; then
    if ! docker image rm "$RAI_DEV_IMAGE" >/dev/null; then rc=1; fi
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
"${compose[@]}" config --quiet
"${compose[@]}" build api
"${compose[@]}" up -d --wait postgres
settings="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc "SELECT current_setting('server_encoding') || ',' || current_setting('TimeZone')")"
[[ "$settings" == UTF8,UTC ]]
"${compose[@]}" run --rm --no-deps api migrate
"${compose[@]}" run --rm --no-deps api migrate
status="$("${compose[@]}" run --rm --no-deps api migrate-status)"
printf '%s' "$status" | python3 -c 'import json,sys; rows=json.load(sys.stdin); assert len(rows)==10 and all(row["State"]=="applied" for row in rows)'
if "${compose[@]}" run --rm --no-deps api migrate-down; then
  echo 'irreversible migration unexpectedly rolled back' >&2
  exit 1
fi
status="$("${compose[@]}" run --rm --no-deps api migrate-status)"
printf '%s' "$status" | python3 -c 'import json,sys; rows=json.load(sys.stdin); assert len(rows)==10 and all(row["State"]=="applied" for row in rows)'
"${compose[@]}" up -d --wait api worker
api_id="$("${compose[@]}" ps -q api)"
worker_id="$("${compose[@]}" ps -q worker)"
[[ -n "$api_id" && -n "$worker_id" ]]
[[ "$(docker inspect -f '{{.Image}}' "$api_id")" == "$(docker inspect -f '{{.Image}}' "$worker_id")" ]]
worker_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$worker_id")"
connections="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc "SELECT count(*) FROM pg_stat_activity WHERE client_addr = '$worker_ip'::inet AND datname = 'rai_dev'")"
[[ "$connections" -ge 1 ]]
port="$("${compose[@]}" port api 8080 | awk -F: '{print $NF}')"
check() {
  local path="$1" expected="$2" actual
  actual="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 5 "http://127.0.0.1:$port$path")"
  [[ "$actual" == "$expected" ]] || { echo "$path returned $actual, expected $expected" >&2; return 1; }
}
for path in /health /livez /ready /readyz; do check "$path" 200; done
if [[ "${RAI_P2_COMPOSE_INTEGRATION:-0}" == 1 ]]; then
  # Integration fixtures create and drop isolated databases below this test root.
  "${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -c 'CREATE DATABASE rai_test' >/dev/null
  pg_port="$("${compose[@]}" port postgres 5432 | awk -F: '{print $NF}')"
  (
    cd apps/server
    RAI_TEST_DATABASE_URL="postgres://rai_dev:development-only@127.0.0.1:$pg_port/rai_test?sslmode=disable" \
      go test -race -v -count=1 -tags=integration -p 1 -timeout=10m \
        -run 'TestStageRouteChunkBatchRetryAndRollback|TestConcurrentJobClaimAndCrashRecovery|TestJobFailureRetryAndAttemptLimit|TestPermanentFailureAndAttemptUpdateAtomicity|TestBatchRegistrationFailureRollsBack|TestFreshDatabaseSchemaRebuild|TestSampleBatchPipelineAndIdempotency|TestSchema7UpgradeAddsSeriesWithoutRewritingFacts|TestAPIRoleCannotMigrateOrMaintain' ./integration
  )
fi
"${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -c 'INSERT INTO goose_db_version (version_id, is_applied) VALUES (11, true)' >/dev/null
for path in /health /livez; do check "$path" 200; done
for path in /ready /readyz; do check "$path" 503; done
docker exec "$api_id" /rai probe
"${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -c 'DELETE FROM goose_db_version WHERE version_id = 11' >/dev/null
for path in /ready /readyz; do check "$path" 200; done
# Stop the DB to verify readiness fails without affecting liveness.
"${compose[@]}" stop postgres
for path in /health /livez; do check "$path" 200; done
for path in /ready /readyz; do check "$path" 503; done
docker exec "$api_id" /rai probe
# Both processes must reject a connection failure at startup.
for mode in api worker; do
  if "${compose[@]}" run --rm --no-deps -e 'RAI_DATABASE_URL=postgres://rai_dev:development-only@127.0.0.1:1/rai_dev?sslmode=disable' api "$mode"; then
    echo "$mode unexpectedly started without a database" >&2
    exit 1
  fi
done
"${compose[@]}" stop api worker
for id in "$api_id" "$worker_id"; do
  [[ "$(docker inspect -f '{{.State.ExitCode}}' "$id")" == 0 ]]
  [[ "$(docker inspect -f '{{.State.OOMKilled}}' "$id")" == false ]]
done
echo 'P1_COMPOSE=PASS (migration twice, shared image, connections, endpoints, failure and SIGTERM)'
