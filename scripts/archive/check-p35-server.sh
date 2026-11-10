#!/usr/bin/env bash
# Invoked inside scripts/test-remote.sh; only its isolated synthetic DB is used.
set -euo pipefail
[[ -n "${RAI_TEST_DATABASE_URL:-}" ]] || { echo 'Requires isolated test-remote database'; exit 2; }
[[ -z "$(gofmt -l .)" ]] || { echo 'gofmt differences'; exit 1; }
go mod verify
go vet ./...
go build ./...
python3 ../../scripts/check-boundaries.py
for mode in unit race integration; do
  report="$(mktemp)"
  case "$mode" in
    unit) go test -json -count=1 ./... > "$report" ;;
    race) go test -json -race -count=1 ./... > "$report" ;;
    integration) go test -json -race -count=1 -tags=integration -p 1 -timeout=30m ./... > "$report" ;;
  esac
  python3 - "$mode" "$report" <<'PY'
import json,sys
rows=[json.loads(line) for line in open(sys.argv[2]) if line.startswith('{')]
counts={state:sum(row.get('Action')==state and 'Test' in row for row in rows) for state in ['pass','fail','skip']}
elapsed=sum(row.get('Elapsed',0) for row in rows if row.get('Action')=='pass' and 'Test' not in row)
assert counts['pass']>0 and not counts['fail'],counts
print(json.dumps({'mode':sys.argv[1],**counts,'package_elapsed_sum_seconds':elapsed}))
PY
  rm -- "$report"
done
