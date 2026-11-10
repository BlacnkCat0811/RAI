#!/usr/bin/env bash
# Temporary API + actual Worker for native simulator acceptance; synthetic only.
set -euo pipefail
umask 077
[[ -n "${RAI_TEST_DATABASE_URL:-}" ]] || exit 2
directory="$(mktemp -d "$PWD/.p6-client-XXXXXX")"
api_pid='' worker_pid=''
cleanup(){ rc=$?;trap - EXIT INT TERM;for pid in "$api_pid" "$worker_pid";do if [[ -n "$pid" ]];then kill "$pid" 2>/dev/null||true;wait "$pid" 2>/dev/null||true;fi;done;rm -rf -- "$directory";exit "$rc";}
trap cleanup EXIT
export RAI_DATABASE_URL="$RAI_TEST_DATABASE_URL"
go build -o "$directory/rai" ./cmd/rai
"$directory/rai" migrate > "$directory/migrate.log" 2>&1
"$directory/rai" init > "$directory/init.json"
dataset="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["datasetID"])' "$directory/init.json")"
"$directory/rai" analysis parameters --dataset "$dataset" --resting-hr 55 --max-hr 190 --coefficient-a 0.64 --coefficient-b 1.92 --provenance 'synthetic P6 acceptance only' > /dev/null
port="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')"
export RAI_LISTEN_ADDR="127.0.0.1:$port"
"$directory/rai" api > "$directory/api.log" 2>&1 & api_pid=$!
"$directory/rai" worker > "$directory/worker.log" 2>&1 & worker_pid=$!
python3 - "$directory" "$port" <<'PY'
import json,pathlib,sys,urllib.request,time
p=pathlib.Path(sys.argv[1]);port=int(sys.argv[2])
for _ in range(60):
 try:
  with urllib.request.urlopen(f'http://127.0.0.1:{port}/ready',timeout=2) as r:assert r.status==200
  break
 except Exception:time.sleep(1)
else:raise SystemExit('isolated API not ready')
c=json.loads((p/'init.json').read_text())
(p/'client.json').write_text(json.dumps({'baseURL':f'http://127.0.0.1:{port}','token':c['token'],'collectorEpoch':c['collectorEpoch']}))
print('P6_CLIENT_READY='+json.dumps({'port':port,'configuration':str(p/'client.json'),'done':str(p/'done')}),flush=True)
PY
for _ in $(seq 1 7200);do if [[ -f "$directory/done" ]];then echo 'P6_ISOLATED_HOST=PASS';exit 0;fi;kill -0 "$api_pid";kill -0 "$worker_pid";sleep 1;done
exit 1
