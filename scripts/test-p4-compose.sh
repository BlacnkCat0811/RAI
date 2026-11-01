#!/usr/bin/env bash
# Existing P3 E2E plus actual P4 worker/API in an isolated project and volume.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
export COMPOSE_PROJECT_NAME="rai-p4-$(date +%s)-$$"
export RAI_DEV_API_PORT=0 RAI_DEV_DB_PORT=0
export RAI_DEV_IMAGE="$COMPOSE_PROJECT_NAME:local"
export RAI_DEV_GOPROXY="${RAI_DEV_GOPROXY:-https://goproxy.cn,direct}"
export RAI_DEV_GO_IMAGE="${RAI_DEV_GO_IMAGE:-docker.m.daocloud.io/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb}"
export RAI_DEV_POSTGRES_IMAGE="${RAI_DEV_POSTGRES_IMAGE:-docker.m.daocloud.io/library/postgres:18.6-bookworm@sha256:bbf83594798e144bff365a9c191c5d779f181fb3135902064ab8ff11918020fc}"
compose=(docker compose -f deploy/dev/compose.yaml)
state_dir="$(mktemp -d)"
cleanup(){ rc=$?;trap - EXIT INT TERM;rm -rf -- "$state_dir";"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || rc=1;docker image rm "$RAI_DEV_IMAGE" >/dev/null 2>&1 || true;exit "$rc"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
"${compose[@]}" config --quiet
"${compose[@]}" build api >/dev/null
"${compose[@]}" up -d --wait postgres
"${compose[@]}" run --rm --no-deps api migrate >/dev/null
"${compose[@]}" run --rm --no-deps api migrate >/dev/null
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8080 | awk -F: '{print $NF}')"
rai_init_operation="$(python3 -c 'import uuid; print(uuid.uuid4())')"
"${compose[@]}" run --rm --no-deps --user "$(id -u):$(id -g)" -v "$state_dir:/private" api init --operation-id "$rai_init_operation" --actor synthetic-compose --base-url "http://127.0.0.1:$api_port" --credential-file /private/enrollment.json >/dev/null
read -r dataset_id dataset_epoch collector_epoch token < <(python3 -c 'import json,sys; x=json.load(open(sys.argv[1]))["enrollment"]; print(x["datasetID"],x["datasetEpoch"],x["collectorEpoch"],x["token"])' "$state_dir/enrollment.json")
export RAI_E2E_URL="http://127.0.0.1:$api_port" RAI_E2E_TOKEN="$token" RAI_E2E_COLLECTOR="$collector_epoch" RAI_E2E_STATE_DIR="$state_dir"
python3 scripts/p3-e2e-client.py upload
"${compose[@]}" stop api >/dev/null
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8080 | awk -F: '{print $NF}')"
export RAI_E2E_URL="http://127.0.0.1:$api_port"
python3 scripts/p3-e2e-client.py retry
"${compose[@]}" stop postgres >/dev/null
[[ "$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 3 "$RAI_E2E_URL/ready" || true)" == 503 ]]
"${compose[@]}" up -d --wait postgres
python3 scripts/p3-e2e-client.py retry
# Actual worker starts only after durable sync and restart recovery have passed.
"${compose[@]}" up -d worker
api_image="$(docker inspect -f '{{.Image}}' "$("${compose[@]}" ps -q api)")"
worker_image="$(docker inspect -f '{{.Image}}' "$("${compose[@]}" ps -q worker)")"
[[ "$api_image" == "$worker_image" ]]
ready=false
for _ in $(seq 1 90);do
  publications="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc "SELECT count(*) FROM analysis_publications WHERE metric_family='workout'")"
  if [[ "$publications" -gt 0 ]];then ready=true;break;fi
  sleep 1
done
[[ "$ready" == true ]]
workout_id="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc 'SELECT workout_uuid FROM workouts ORDER BY workout_uuid LIMIT 1')"
export RAI_P4_WORKOUT_ID="$workout_id"
python3 - <<'PY'
import json,os,urllib.request
url=os.environ['RAI_E2E_URL']+'/v2/analysis/workouts/'+os.environ['RAI_P4_WORKOUT_ID']
r=urllib.request.Request(url,headers={'Authorization':'Bearer '+os.environ['RAI_E2E_TOKEN']})
with urllib.request.urlopen(r,timeout=10) as f: value=json.load(f)
assert value['algorithm']=='workout.bundle' and value['algorithmVersion']=='v2'
assert value['generation']>0 and len(value['inputHash'])==64
assert value['metrics'] and all(x['quality'] in ('available','partial','insufficient') for x in value['metrics'])
assert next(x for x in value['metrics'] if x['metric']=='workout_load')['value'] is None
print('P4_CURRENT_API=PASS (nullable missing personal parameters; no invented load)')
PY
# Administrative dry-run is concrete but must not create another job.
before="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc 'SELECT count(*) FROM jobs')"
"${compose[@]}" run --rm --no-deps api analysis recompute --dataset "$dataset_id" --scope workouts --algorithm workout.bundle/v2 --from 2020-01-01 --through 2020-01-02 --dry-run >/dev/null
after="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc 'SELECT count(*) FROM jobs')"
[[ "$before" == "$after" ]]
"${compose[@]}" stop worker >/dev/null
[[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq worker)")" == 0 ]]
"${compose[@]}" up -d worker
"${compose[@]}" stop api worker >/dev/null
[[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq api)")" == 0 ]]
[[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq worker)")" == 0 ]]
echo 'P4_COMPOSE=PASS (P3 restart/worker-offline regression, same image, actual worker, current API, dry-run, SIGTERM)'
