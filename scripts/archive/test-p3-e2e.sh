#!/usr/bin/env bash
# Disposable fnOS Docker E2E: actual API -> PostgreSQL -> durable ACK, worker offline.
# All data is synthetic. Never prints the device token, DSN or health values.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
export COMPOSE_PROJECT_NAME="rai-p3-$(date +%s)-$$"
export RAI_DEV_API_PORT=0
export RAI_DEV_DB_PORT=0
export RAI_DEV_IMAGE="$COMPOSE_PROJECT_NAME:local"
export RAI_DEV_GOPROXY="${RAI_DEV_GOPROXY:-https://goproxy.cn,direct}"
export RAI_DEV_GO_IMAGE="${RAI_DEV_GO_IMAGE:-docker.m.daocloud.io/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb}"
export RAI_DEV_POSTGRES_IMAGE="${RAI_DEV_POSTGRES_IMAGE:-docker.m.daocloud.io/library/postgres:18.6-bookworm@sha256:bbf83594798e144bff365a9c191c5d779f181fb3135902064ab8ff11918020fc}"
compose=(docker compose -f deploy/dev/compose.yaml)
state_dir="$(mktemp -d)"
cleanup() {
  rc=$?
  trap - EXIT INT TERM
  rm -rf -- "$state_dir"
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || rc=1
  docker image rm "$RAI_DEV_IMAGE" >/dev/null 2>&1 || true
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

"${compose[@]}" config --quiet
"${compose[@]}" build api >/dev/null
"${compose[@]}" up -d --wait postgres
"${compose[@]}" run --rm --no-deps api migrate >/dev/null
"${compose[@]}" run --rm --no-deps api migrate >/dev/null
status="$("${compose[@]}" run --rm --no-deps api migrate-status)"
printf '%s' "$status" | python3 -c 'import json,sys; rows=json.load(sys.stdin); assert len(rows)==19 and all(row["State"]=="applied" for row in rows)'
# Start API without worker: sync must still commit and enqueue durable jobs.
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8080 | awk -F: '{print $NF}')"
rai_init_operation="$(python3 -c 'import uuid; print(uuid.uuid4())')"
"${compose[@]}" run --rm --no-deps --user "$(id -u):$(id -g)" -v "$state_dir:/private" api init --operation-id "$rai_init_operation" --actor synthetic-compose --base-url "http://127.0.0.1:$api_port" --credential-file /private/enrollment.json >/dev/null
read -r dataset_id dataset_epoch collector_epoch token < <(python3 -c 'import json,sys; x=json.load(open(sys.argv[1]))["enrollment"]; print(x["datasetID"],x["datasetEpoch"],x["collectorEpoch"],x["token"])' "$state_dir/enrollment.json")
[[ -n "$dataset_id" && -n "$token" ]]
export RAI_E2E_URL="http://127.0.0.1:$api_port"
export RAI_E2E_TOKEN="$token"
export RAI_E2E_COLLECTOR="$collector_epoch"
export RAI_E2E_STATE_DIR="$state_dir"
python3 scripts/p3-e2e-client.py upload
jobs="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc 'SELECT count(*) FROM jobs')"
[[ "$jobs" -ge 4 ]]
# API process restart: same immutable batch body must return the original receipt.
"${compose[@]}" stop api >/dev/null
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8080 | awk -F: '{print $NF}')"
export RAI_E2E_URL="http://127.0.0.1:$api_port"
python3 scripts/p3-e2e-client.py retry
# PostgreSQL restart: readiness fails, then the same batch retry still gets the
# original durable receipt after recovery.
"${compose[@]}" stop postgres >/dev/null
code="$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 3 "http://127.0.0.1:$api_port/ready" || true)"
[[ "$code" == 503 ]]
"${compose[@]}" up -d --wait postgres
python3 scripts/p3-e2e-client.py retry
echo "P3_FNOS_E2E=PASS (API restart, PostgreSQL restart, worker-offline durable ACK, route seal, association, enrichment; synthetic data)"
