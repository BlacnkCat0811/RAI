#!/usr/bin/env bash
# Final quality gates, run in apps/server with an isolated rai_test URL.
set -euo pipefail
[[ -n "${RAI_TEST_DATABASE_URL:-}" ]] || exit 2
[[ -z "$(gofmt -l .)" ]]
echo 'MAINTENANCE_GOFMT=PASS'
run(){
python3 - "$@" <<'PY'
import json,subprocess,sys,tempfile,time
command=sys.argv[1:];start=time.monotonic()
if command[:2]==['go','test']:
 with tempfile.TemporaryFile(mode='w+') as report:
  result=subprocess.run(command,stdout=report,stderr=subprocess.STDOUT)
  report.seek(0)
  rows=[]
  for line in report:
   try:rows.append(json.loads(line))
   except ValueError:print(line.rstrip())
  counts={key:sum(row.get('Action')==key and 'Test' in row for row in rows) for key in ['pass','fail','skip']}
  packages={row['Package']:row.get('Elapsed') for row in rows if row.get('Action') in ['pass','fail'] and 'Test' not in row}
  names=[row['Test'] for row in rows if row.get('Action')=='pass' and row.get('Package','').endswith('/integration') and 'Test' in row]
  print('MAINTENANCE_TESTS='+json.dumps({'command':command,**counts,'package_seconds':packages,'integration_pass_names':names}))
else:result=subprocess.run(command)
print('MAINTENANCE_COMMAND='+json.dumps({'command':command,'duration_seconds':round(time.monotonic()-start,3),'exit_code':result.returncode}),flush=True)
sys.exit(result.returncode)
PY
}
run go version
run go mod verify
run go test -json -count=1 ./...
run go test -json -race -count=1 ./...
run go vet ./...
run go vet -tags=integration,scale,p7capacity ./...
run go build ./...
run go mod tidy -diff
run python3 ../../scripts/check-boundaries.py
run python3 ../../scripts/check-boundaries.py
run env CGO_ENABLED=0 python3 ../../scripts/check-boundaries.py
run env CGO_ENABLED=0 python3 ../../scripts/check-boundaries.py
run go test -json -race -count=1 -tags=integration -p 1 -timeout=30m ./...
echo 'FINAL_MAINTENANCE_GO=PASS'
