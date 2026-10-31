#!/usr/bin/env bash
# Run the full Go test suite: unit, vet, race and real isolated PostgreSQL.
# The database is a fresh throwaway cluster created by scripts/test-postgres.sh.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

rai_require_toolchain
rai_go_env
cd "$rai_root/apps/server"

echo "== gofmt =="
unformatted="$(gofmt -l .)"
if [[ -n "$unformatted" ]]; then
  echo "$unformatted" >&2
  echo "gofmt required" >&2
  exit 1
fi

echo "== go mod verify =="
go mod verify

echo "== go test (unit) =="
go test -count=1 ./...

echo "== go vet =="
go vet ./...
go vet -tags=integration ./...

echo "== go build =="
go build ./...

echo "== go test -race against an isolated PostgreSQL 18 cluster =="
cd "$rai_root"
PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -tags=integration ./...

echo "== dependency boundaries =="
python3 scripts/check-boundaries.py

echo "LOCAL TESTS: PASS"
