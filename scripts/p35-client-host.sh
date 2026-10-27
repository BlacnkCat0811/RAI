#!/usr/bin/env bash
# Hold an isolated fnOS API for the macOS Swift synthetic integration probe.
# Call only through test-remote.sh. Never opens the existing rai database.
set -euo pipefail
umask 077
[[ -n "${RAI_TEST_DATABASE_URL:-}" ]] || exit 2
directory="$(mktemp -d "$PWD/.p35-client-XXXXXX")"
pid=''
cleanup() {
  rc=$?; trap - EXIT INT TERM
  if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  rm -rf -- "$directory"
  exit "$rc"
}
trap cleanup EXIT
export RAI_DATABASE_URL="$RAI_TEST_DATABASE_URL"
go build -o "$directory/rai" ./cmd/rai
"$directory/rai" migrate
"$directory/rai" init > "$directory/init.json"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
export RAI_LISTEN_ADDR="127.0.0.1:$port"
"$directory/rai" api > "$directory/api.log" 2>&1 &
pid=$!
python3 - "$directory" "$port" <<'PY'
import json,pathlib,sys,urllib.request,time
directory=pathlib.Path(sys.argv[1]);port=int(sys.argv[2])
for _ in range(60):
 try:
  with urllib.request.urlopen(f'http://127.0.0.1:{port}/ready',timeout=2) as response:
   assert response.status==200
  break
 except Exception: time.sleep(1)
else: raise SystemExit('isolated API not ready')
init=json.loads((directory/'init.json').read_text())
(directory/'client.json').write_text(json.dumps({'baseURL':f'http://127.0.0.1:{port}','token':init['token'],'collectorEpoch':init['collectorEpoch']}))
print('P35_CLIENT_READY='+json.dumps({'port':port,'configuration':str(directory/'client.json'),'done':str(directory/'done')}),flush=True)
PY
finished=0
for _ in $(seq 1 1200); do
  if [[ -f "$directory/done" ]]; then finished=1; break; fi
  kill -0 "$pid"
  sleep 1
done
[[ "$finished" == 1 ]] || { echo 'Swift probe timed out'; exit 1; }
cat > "$directory/main.go" <<'GO'
package main
import("context";"encoding/json";"fmt";"os";"github.com/jackc/pgx/v5")
func main(){
 ctx:=context.Background(); db,err:=pgx.Connect(ctx,os.Getenv("RAI_TEST_DATABASE_URL")); if err!=nil {panic("isolated database connect failed")}; defer db.Close(ctx)
 expected:=map[string]int64{"health_samples":1,"workouts":1,"workout_routes":1,"route_chunks":6,"route_points":5001,"sync_batches":3,"sync_pages":3,"workout_samples":1,"workout_enrichments":1}
 counts:=map[string]int64{}
 for table,want:=range expected {
  var count int64; if err:=db.QueryRow(ctx,"SELECT count(*) FROM "+table).Scan(&count);err!=nil||count!=want{panic("unexpected synthetic count: "+table)}; counts[table]=count
 }
 var sealed,jobs int64
 if err:=db.QueryRow(ctx,"SELECT count(*) FROM workout_routes WHERE state='sealed'").Scan(&sealed);err!=nil||sealed!=1{panic("route not sealed")}
 if err:=db.QueryRow(ctx,"SELECT count(*) FROM jobs").Scan(&jobs);err!=nil||jobs<5{panic("jobs not durable")}
 counts["sealed_routes"]=sealed;counts["durable_jobs"]=jobs
 output,_:=json.Marshal(counts);fmt.Printf("P35_SWIFT_FNOS_DATABASE=PASS %s\n",output)
}
GO
go run "$directory/main.go"
