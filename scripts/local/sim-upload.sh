#!/usr/bin/env bash
# Simulated-cloud upload acceptance: the real App upload code runs inside a local
# Simulator and POSTs protocol-6 Workouts over real TCP to a local Go API backed
# by an isolated PostgreSQL 18 database. Synthetic data only; nothing here reads
# production state, a real cloud deployment or real HealthKit values.
#
# usage: sim-upload.sh start|status|run|stop|all [--drop]
#   start    build rai, ensure the cluster, create+migrate the isolated database,
#            issue a credential with `rai init`, start API and Worker
#   status   print pid files, database, readiness and the advertised capabilities
#   run      build-for-testing the App, inject the credential, run the live tests
#   stop     stop API and Worker; keeps the database and credential for inspection
#            (add --drop to also drop the database and remove the credential)
#   all      start, run, then stop (leaves the database; --drop drops it too)
#
# Environment overrides: RAI_SIM_UPLOAD_DB (default rai_sim_upload),
# RAI_SIM_UPLOAD_PORT (default 18081), RAI_LIVE_SIMULATOR (default: the
# "RAI Local Tests" device, else the first iPhone 12 Pro Max).
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

db_name="${RAI_SIM_UPLOAD_DB:-rai_sim_upload}"
# Per-database state: a credential, pid files and logs belong to exactly one
# isolated database, so a second run never inherits another dataset.
work="$rai_local/testing/sim-upload/$db_name"
derived="$work/derived"
mkdir -p "$work"
api_port="${RAI_SIM_UPLOAD_PORT:-18081}"
base_url="http://127.0.0.1:$api_port"
database_url="postgres://$rai_db_user:$rai_db_password@127.0.0.1:$rai_db_port/$db_name?sslmode=disable"
credential="$work/enrollment.json"
api_log="$work/api.log"
worker_log="$work/worker.log"
api_pid="$work/api.pid"
worker_pid="$work/worker.pid"
rai_bin_path="$rai_bin/rai"

usage() { sed -n '3,24p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }

say() { printf 'SIM_UPLOAD %s\n' "$*"; }

psql_admin() { "$rai_pgbin/psql" "postgres://$rai_db_user:$rai_db_password@127.0.0.1:$rai_db_port/postgres?sslmode=disable" -X -v ON_ERROR_STOP=1 "$@"; }

database_exists() {
  [[ "$(psql_admin -Atc "SELECT 1 FROM pg_database WHERE datname = '$db_name'")" == "1" ]]
}

credential_value() {
  python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['enrollment'][sys.argv[2]])" "$credential" "$1"
}

build_binary() {
  rai_go_env
  ( cd "$rai_root/apps/server" && go build -trimpath -o "$rai_bin_path" ./cmd/rai )
  say "binary=$rai_bin_path version=$("$rai_bin_path" version | python3 -c 'import json,sys;v=json.load(sys.stdin);print("schema=%s openapi=%s sync_protocol=%s"%(v["schema"],v["openapi"],v["sync_protocol"]))')"
}

pid_alive() { [[ -f "$1" ]] && kill -0 "$(cat "$1")" 2>/dev/null; }

stop_processes() {
  local name file
  for name in api worker; do
    file="$work/$name.pid"
    if pid_alive "$file"; then
      kill "$(cat "$file")" 2>/dev/null || true
      for _ in $(seq 1 40); do pid_alive "$file" || break; sleep 0.25; done
      pid_alive "$file" && { kill -9 "$(cat "$file")" 2>/dev/null || true; sleep 0.2; }
      say "stopped $name pid=$(cat "$file" 2>/dev/null || echo -)"
    fi
    rm -f "$file"
  done
}

start_processes() {
  if pid_alive "$api_pid" && pid_alive "$worker_pid"; then
    say "api and worker already running"
    return 0
  fi
  mkdir -p "$work"
  RAI_DATABASE_URL="$database_url" RAI_LISTEN_ADDR="127.0.0.1:$api_port" \
    nohup "$rai_bin_path" api >>"$api_log" 2>&1 &
  echo $! >"$api_pid"
  RAI_DATABASE_URL="$database_url" nohup "$rai_bin_path" worker >>"$worker_log" 2>&1 &
  echo $! >"$worker_pid"
  for _ in $(seq 1 120); do
    curl -sf -o /dev/null "$base_url/ready" && break
    pid_alive "$api_pid" || { echo "api exited; see $api_log" >&2; tail -20 "$api_log" >&2; return 1; }
    sleep 0.5
  done
  curl -sf -o /dev/null "$base_url/ready" || { echo "api not ready; see $api_log" >&2; return 1; }
  say "api=pid $(cat "$api_pid") $base_url worker=pid $(cat "$worker_pid")"
}

start_stack() {
  rai_require_toolchain
  bash "$rai_root/scripts/local/pg.sh" start >/dev/null
  build_binary
  if database_exists; then
    say "database=$db_name (existing)"
  else
    psql_admin -c "CREATE DATABASE $db_name" >/dev/null
    say "database=$db_name (created)"
  fi
  RAI_DATABASE_URL="$database_url" "$rai_bin_path" migrate
  if [[ -f "$credential" ]]; then
    say "credential=$credential (existing dataset $(credential_value datasetID))"
  else
    local datasets
    datasets="$(RAI_DATABASE_URL="$database_url" "$rai_pgbin/psql" "$database_url" -X -Atc 'SELECT count(*) FROM datasets')"
    [[ "$datasets" == "0" ]] || { echo "database $db_name already has datasets but $credential is missing; run stop --drop" >&2; return 1; }
    RAI_DATABASE_URL="$database_url" "$rai_bin_path" init \
      --operation-id "$(uuidgen | tr 'A-Z' 'a-z')" --actor synthetic-simulator-acceptance \
      --base-url "$base_url" --credential-file "$credential" >/dev/null
    say "credential=$credential (new dataset $(credential_value datasetID))"
  fi
  start_processes
  status_stack
}

status_stack() {
  local state="stopped"
  pid_alive "$api_pid" && pid_alive "$worker_pid" && state="running"
  say "state=$state database=$db_name api_port=$api_port"
  [[ "$state" == "running" ]] || return 0
  database_exists && say "rows workouts=$(RAI_DATABASE_URL="$database_url" "$rai_pgbin/psql" "$database_url" -X -Atc 'SELECT count(*) FROM workouts')" \
    ingests="$(RAI_DATABASE_URL="$database_url" "$rai_pgbin/psql" "$database_url" -X -Atc 'SELECT count(*) FROM workout_ingests')"
  curl -sf "$base_url/v2/handshake" -H "Authorization: Bearer $(credential_value token)" | python3 -c '
import json,sys
v = json.load(sys.stdin)
cap = v.get("capabilities", {})
print("SIM_UPLOAD handshake dataset=%s status=%s protocol=%s workoutPayloadVersion=%s ingestAccepting=%s"
      % (v.get("datasetID"), v.get("datasetStatus"), v.get("protocolVersion"),
         cap.get("workoutPayloadVersion"), cap.get("workoutUUIDIngestAccepting")))'
}

resolve_simulator() {
  if [[ -n "${RAI_LIVE_SIMULATOR:-}" ]]; then echo "$RAI_LIVE_SIMULATOR"; return; fi
  python3 - <<'PYSIM'
import json, subprocess
devices = json.loads(subprocess.check_output(['xcrun','simctl','list','devices','available','-j'], text=True))['devices']
flat = [d for group in devices.values() for d in group]
for name in ('RAI Local Tests', 'iPhone 12 Pro Max'):
    for d in flat:
        if d['name'] == name:
            print(d['udid']); raise SystemExit(0)
raise SystemExit('no Simulator device found; create one or set RAI_LIVE_SIMULATOR')
PYSIM
}

run_tests() {
  rai_xcode_env
  pid_alive "$api_pid" || { echo "stack not running; run: $0 start" >&2; exit 1; }
  [[ -f "$credential" ]] || { echo "missing credential; run: $0 start" >&2; exit 1; }
  local simulator token collector
  simulator="$(resolve_simulator)"
  token="$(credential_value token)"
  collector="$(credential_value collectorEpoch)"
  say "simulator=$simulator base_url=$base_url"
  xcodebuild build-for-testing -project "$rai_root/ios/RAI/RAI.xcodeproj" -scheme RAI \
    -destination "platform=iOS Simulator,id=$simulator" -derivedDataPath "$derived" \
    CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- RAI_DEV_HTTP_HOST=127.0.0.1 -quiet
  python3 - "$derived" "$base_url" "$token" "$collector" <<'PY'
import pathlib, plistlib, sys
root, base, token, collector = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3], sys.argv[4]
files = [p for p in (root/'Build/Products').glob('*.xctestrun') if p.name != 'live.xctestrun']
assert len(files) == 1, 'expected exactly one generated xctestrun'
value = plistlib.loads(files[0].read_bytes())
targets = ([t for c in value.get('TestConfigurations', []) for t in c['TestTargets']]
           if 'TestConfigurations' in value else [t for t in value.values() if isinstance(t, dict)])
matched = False
for target in targets:
    if target.get('BlueprintName') == 'RAITests':
        env = target.setdefault('EnvironmentVariables', {})
        env['RAI_LIVE_SYNC_BASE_URL'] = base
        env['RAI_LIVE_SYNC_TOKEN'] = token
        env['RAI_LIVE_SYNC_COLLECTOR_EPOCH'] = collector
        matched = True
assert matched, 'RAITests target not found'
(root/'Build/Products/live.xctestrun').write_bytes(plistlib.dumps(value))
PY
  local result_root
  result_root="$(mktemp -d "$work/simulator.XXXXXX")"
  xcodebuild test-without-building -xctestrun "$derived/Build/Products/live.xctestrun" \
    -destination "platform=iOS Simulator,id=$simulator" -parallel-testing-enabled NO \
    -only-testing:RAITests/WorkoutUploadLiveTests \
    -resultBundlePath "$result_root/result.xcresult" -quiet || true
  xcrun xcresulttool get test-results summary --path "$result_root/result.xcresult" >"$result_root/summary.json"
  say "result=$result_root"
  cat "$result_root/summary.json"
  python3 - "$result_root/summary.json" <<'PY'
import json, sys
v = json.load(open(sys.argv[1]))
failed = v.get('failedTests', 0) or 0
passed = v.get('passedTests', 0) or 0
print("SIM_UPLOAD TESTS passed=%s failed=%s result=%s" % (passed, failed, v.get('result')))
raise SystemExit(1 if failed or not passed else 0)
PY
}

command="${1:-all}"
[[ $# -gt 0 ]] && shift
drop=0
for argument in "$@"; do
  case "$argument" in
    --drop) drop=1 ;;
    *) usage ;;
  esac
done

case "$command" in
  start) start_stack ;;
  status) status_stack ;;
  run) run_tests ;;
  stop)
    stop_processes
    if [[ "$drop" == 1 ]]; then
      database_exists && psql_admin -c "DROP DATABASE $db_name" >/dev/null && say "database=$db_name dropped"
      rm -f "$credential" && say "credential removed"
    else
      say "database=$db_name and credential kept; add --drop to remove them"
    fi
    ;;
  all)
    start_stack
    run_tests
    stop_processes
    if [[ "$drop" == 1 ]]; then
      database_exists && psql_admin -c "DROP DATABASE $db_name" >/dev/null && say "database=$db_name dropped"
      rm -f "$credential" && say "credential removed"
    fi
    ;;
  *) usage ;;
esac
