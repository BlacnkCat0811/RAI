#!/usr/bin/env bash
# Isolated P3/P4 regression plus the P5 training closed loop through HTTP.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
export COMPOSE_PROJECT_NAME="rai-p5-$(date +%s)-$$"
export RAI_DEV_API_PORT=0 RAI_DEV_DB_PORT=0
export RAI_DEV_IMAGE="$COMPOSE_PROJECT_NAME:local"
export RAI_DEV_GOPROXY="${RAI_DEV_GOPROXY:-https://goproxy.cn,direct}"
export RAI_DEV_GO_IMAGE="${RAI_DEV_GO_IMAGE:-docker.m.daocloud.io/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb}"
export RAI_DEV_POSTGRES_IMAGE="${RAI_DEV_POSTGRES_IMAGE:-docker.m.daocloud.io/library/postgres:18.6-bookworm@sha256:bbf83594798e144bff365a9c191c5d779f181fb3135902064ab8ff11918020fc}"
compose=(docker compose -f deploy/dev/compose.yaml)
state_dir="$(mktemp -d)"
if [[ "${RAI_MAINTENANCE_COMPOSE:-0}" == 1 ]];then
  # Override only this unique synthetic project with fresh credentials.
  umask 077
  python3 - "$state_dir/credentials.json" <<'PY'
import json,os,secrets,sys
password=secrets.token_hex(24)
dsn='postgres://rai_dev:'+password+'@postgres:5432/rai_dev?sslmode=disable'
with open(sys.argv[1],'w') as f:json.dump({'services':{'postgres':{'environment':{'POSTGRES_PASSWORD':password}},'api':{'environment':{'RAI_DATABASE_URL':dsn},'build':{'args':{'SOURCE_REVISION':os.environ.get('RAI_SOURCE_COMMIT','unrecorded')}}},'worker':{'environment':{'RAI_DATABASE_URL':dsn}}}},f)
PY
  compose+=(-f "$state_dir/credentials.json")
fi
cleanup(){ rc=$?;trap - EXIT INT TERM;cleanup_rc=0;"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || cleanup_rc=1;rm -rf -- "$state_dir";docker image rm "$RAI_DEV_IMAGE" >/dev/null 2>&1 || true;echo "P5_COMPOSE_CLEANUP=$cleanup_rc";if [[ "$cleanup_rc" != 0 ]];then rc=1;fi;exit "$rc"; }
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
if [[ "${RAI_MAINTENANCE_COMPOSE:-0}" == 1 ]];then python3 scripts/final-maintenance-compose-client.py prepare;fi
"${compose[@]}" stop api >/dev/null
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8080 | awk -F: '{print $NF}')"
export RAI_E2E_URL="http://127.0.0.1:$api_port"
python3 scripts/p3-e2e-client.py retry
if [[ "${RAI_MAINTENANCE_COMPOSE:-0}" == 1 ]];then python3 scripts/final-maintenance-compose-client.py retry;fi
"${compose[@]}" stop postgres >/dev/null
[[ "$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 3 "$RAI_E2E_URL/ready" || true)" == 503 ]]
"${compose[@]}" up -d --wait postgres
python3 scripts/p3-e2e-client.py retry
if [[ "${RAI_MAINTENANCE_COMPOSE:-0}" == 1 ]];then python3 scripts/final-maintenance-compose-client.py retry;fi
# Actual worker starts only after durable sync and restart recovery have passed.
if [[ "${RAI_MAINTENANCE_COMPOSE:-0}" == 1 ]];then
  [[ "$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc "SELECT (SELECT count(*)=1998 FROM health_samples) AND (SELECT count(*)=1 FROM entity_tombstones) AND (SELECT count(*)=2 FROM jobs WHERE kind='analysis.sample') AND (SELECT count(*)=1 AND min(generation)=2 FROM dirty_scopes WHERE scope_key LIKE 'sample:%')")" == t ]]
  echo 'MAINTENANCE_COMPOSE_DATABASE=PASS facts=1998 tombstones=1 sample_jobs=2 sample_scopes=1 generation=2; old receipt replay did not resurrect'
fi
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
if [[ "${RAI_MAINTENANCE_COMPOSE:-0}" == 1 ]];then
  "${compose[@]}" run --rm --no-deps api version
  "${compose[@]}" run --rm --no-deps api analysis recompute --dataset "$dataset_id" --scope athlete/default --algorithm training_state.bundle/v2 --from 2020-01-01 --through 2020-01-02 --dry-run >/dev/null
  if "${compose[@]}" run --rm --no-deps api analysis recompute --dataset "$dataset_id" --scope workouts --algorithm workout.bundle/v1 --from 2020-01-01 --through 2020-01-02 --dry-run >/dev/null 2>&1;then exit 1;fi
  echo 'MAINTENANCE_CLI=PASS workout.bundle/v2,training_state.bundle/v2 dry-runs; obsolete v1 rejected'
fi
after="$("${compose[@]}" exec -T postgres psql -X -U rai_dev -d rai_dev -Atc 'SELECT count(*) FROM jobs')"
[[ "$before" == "$after" ]]
python3 - <<'PYTEST'
import datetime,json,os,time,urllib.request,uuid
base=os.environ['RAI_E2E_URL'];token=os.environ['RAI_E2E_TOKEN']
def uid():return str(uuid.uuid4())
def call(method,path,value=None):
    body=None if value is None else json.dumps(value).encode()
    req=urllib.request.Request(base+path,data=body,method=method,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    with urllib.request.urlopen(req,timeout=15) as response:return json.load(response)
def wait_plan(predicate):
    for _ in range(90):
        p=call('GET','/v2/training/plans/current')
        if predicate(p):return p
        time.sleep(1)
    raise AssertionError('training publication timeout')
mon=datetime.datetime.now(datetime.timezone.utc).date();mon-=datetime.timedelta(days=mon.weekday())
plan=uid()
request={'commandID':uid(),'planID':plan,'config':{'startDate':str(mon),'weeks':8,'goal':{'type':'running','date':str(mon+datetime.timedelta(days=55)),'distanceM':21097.5},'availableDays':[1,3,5,7],'longRunDay':7,'baselineWeeklyM':30000,'previousLongM':12000}}
first=call('POST','/v2/training/plans',request)
assert call('POST','/v2/training/plans',request)==first
p=wait_plan(lambda p:p['effective'] is not None);eff=p['effective'];sid=eff['sessions'][0]['sessionID']
r={'commandID':uid(),'expectedOverrideRevision':0,'overrideID':uid(),'sessionID':sid,'operation':'edit','path':'distanceM','value':7000}
eff=call('POST','/v2/training/plans/'+plan+'/overrides',r)
assert call('POST','/v2/training/plans/'+plan+'/overrides',r)==eff
execution=uid();ex=call('POST','/v2/training/executions',{'commandID':uid(),'executionID':execution,'planID':plan,'effectiveVersionID':eff['effectiveVersionID'],'sessionID':sid})
snapshot=ex['snapshot']
ex=call('POST','/v2/training/executions/'+execution+'/changes',{'commandID':uid(),'expectedRevision':1,'operation':'complete'})
assert ex['status']=='completed' and ex['feedback'][-1]['status']=='completed'
ex=call('POST','/v2/training/executions/'+execution+'/changes',{'commandID':uid(),'expectedRevision':2,'operation':'associate','workoutID':os.environ['RAI_P4_WORKOUT_ID']})
p=wait_plan(lambda p:p['effective']['baseVersionID']!=eff['baseVersionID'] and p['generationState']=='succeeded')
assert next(s for s in p['effective']['sessions'] if s['sessionID']==sid)['distanceM']==7000
ex=call('GET','/v2/training/executions/'+execution)
assert ex['snapshot']==snapshot and ex['effectiveVersionID']==eff['effectiveVersionID']
assert ex['feedback'][-1].get('sourceRunID')
audit=call('GET','/v2/training/plans/'+plan+'/effective/'+p['effective']['effectiveVersionID'])
assert audit['overrides'] and audit['context']['parameters']['version']=='coach_parameters/v1'
print('P5_HTTP_CLOSED_LOOP=PASS (immutable retry, published base, override survives feedback recalculation, frozen execution, late association)')
PYTEST
"${compose[@]}" stop worker >/dev/null
[[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq worker)")" == 0 ]]
"${compose[@]}" up -d worker
if [[ "${RAI_MAINTENANCE_COMPOSE:-0}" == 1 ]];then
python3 - <<'PY'
import json,os,time,urllib.error,urllib.request,uuid
def call(path,body=None):
 for attempt in range(5):
  request=urllib.request.Request(os.environ['RAI_E2E_URL']+path,data=None if body is None else json.dumps(body).encode(),headers={'Authorization':'Bearer '+os.environ['RAI_E2E_TOKEN'],'Content-Type':'application/json'})
  try:
   with urllib.request.urlopen(request,timeout=15) as response:return json.load(response)
  except urllib.error.HTTPError as error:
   if error.code!=429 or attempt==4:raise
   time.sleep(max(1,int(error.headers.get('Retry-After','1'))))
plan=call('/v2/training/plans/current');old=plan['effective']['baseVersionID']
call('/v2/training/plans/'+plan['planID']+'/changes',{'commandID':str(uuid.uuid4()),'operation':'recalculate','expectedConfigRevision':plan['configRevision']})
for _ in range(90):
 current=call('/v2/training/plans/current')
 if current['effective']['baseVersionID']!=old and current['generationState']=='succeeded':break
 time.sleep(1)
else:raise AssertionError('restarted Worker did not publish a new Base')
print('MAINTENANCE_WORKER_RESTART=PASS real training regeneration after restart')
PY
fi
"${compose[@]}" stop api worker >/dev/null
[[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq api)")" == 0 ]]
[[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq worker)")" == 0 ]]
echo 'P5_COMPOSE=PASS (P3 restart/worker-offline regression, same image, actual worker, current API, dry-run, SIGTERM)'
