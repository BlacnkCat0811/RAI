#!/usr/bin/env bash
# Final backend gate in the existing disposable fnOS PostgreSQL harness.
set -euo pipefail
[[ -n "${RAI_TEST_DATABASE_URL:-}" ]] || exit 2
[[ -z "$(gofmt -l .)" ]]
go mod verify
go vet ./...
go build ./...
python3 ../../scripts/check-boundaries.py
for mode in unit race integration;do
 report="$(mktemp)"
 case "$mode" in
  unit) args=(-count=1) ;;
  race) args=(-race -count=1) ;;
  integration) args=(-race -count=1 -tags=integration -p 1 -timeout=30m) ;;
 esac
 if ! go test -json "${args[@]}" ./... > "$report";then cat "$report";rm -f "$report";exit 1;fi
 python3 - "$mode" "$report" <<'PY'
import json,sys
rows=[json.loads(line) for line in open(sys.argv[2]) if line.startswith('{')]
counts={state:sum(r.get('Action')==state and 'Test' in r for r in rows) for state in ['pass','fail','skip']}
elapsed={r['Package']:r.get('Elapsed',0) for r in rows if r.get('Action')=='pass' and 'Test' not in r}
assert counts['pass'] and not counts['fail']
print('P7_GO_GATE='+json.dumps({'mode':sys.argv[1],**counts,'package_seconds':elapsed}))
PY
 rm -f "$report"
done
go vet -tags=integration,scale,p7capacity ./...
python3 ../../scripts/test-p7-alerts.py
echo 'P7_FINAL_GO=PASS'
