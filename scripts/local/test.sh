#!/usr/bin/env bash
# All development checks run on this Mac. No SSH or remote database fallback.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

usage() {
  cat <<'USAGE'
usage: scripts/local/test.sh [quick|gate|unit|integration|full|swift|simulator|e2e|perf] [test arguments]
  quick        cached Go unit tests, vet and dependency boundaries (default)
  gate         cached Go race, vet and boundaries; add affected PG tests for transactions
  unit         Go unit tests; accepts -run and package arguments
  integration  fresh local PostgreSQL 18, race enabled; accepts -run/packages
  full         uncached Go race, vet, build, sqlc, full isolated PG regression
  swift        native Swift package tests; accepts --filter
  simulator    synthetic App XCTest on a dedicated local Simulator
  e2e          Simulator -> local Go HTTP -> disposable local PG18 (R3 v2)
  perf         synthetic 1/10/100/500-Workout local HTTP/PG throughput
Quick checks do not replace required completion gates or real-device acceptance.
USAGE
}

rai_profile="${1:-quick}"
if [[ $# -gt 0 ]]; then shift; fi
rai_started_seconds=$SECONDS
trap 'rai_exit=$?; if [[ "$rai_exit" != 0 ]]; then echo "LOCAL CHECKS: FAIL profile=$rai_profile elapsed_seconds=$((SECONDS - rai_started_seconds)) exit_code=$rai_exit" >&2; fi' EXIT
rai_go_env
unset RAI_TEST_DATABASE_URL
export PGBIN="$rai_pgbin"

go_tests() {
  if [[ $# == 0 ]]; then set -- ./...; fi
  (cd "$rai_root/apps/server" && go test "$@")
}

pg_tests() {
  if [[ $# == 0 ]]; then set -- ./...; fi
  "$rai_root/scripts/test-postgres.sh" go test -race -count=1 -tags=integration -p 1 -timeout=40m "$@"
}

prepare_simulator() {
  rai_xcode_env
  if [[ -z "${RAI_UA1_R_SIMULATOR:-}" ]]; then
    RAI_UA1_R_SIMULATOR="$(python3 - <<'PYSIM'
import json, subprocess
def run(*args):
    return subprocess.check_output(['xcrun', 'simctl', *args], text=True)
devices = json.loads(run('list', 'devices', 'available', '-j'))['devices']
matches = [d['udid'] for group in devices.values() for d in group if d['name'] == 'RAI Local Tests']
if matches:
    print(matches[0])
else:
    runtimes = [r for r in json.loads(run('list', 'runtimes', '-j'))['runtimes']
                if r.get('isAvailable') and r['identifier'].startswith('com.apple.CoreSimulator.SimRuntime.iOS-')]
    if not runtimes:
        raise SystemExit('No installed iOS Simulator runtime; install it in Xcode.')
    runtime = max(runtimes, key=lambda r: tuple(int(n) for n in r['version'].split('.')))
    print(run('create', 'RAI Local Tests', 'com.apple.CoreSimulator.SimDeviceType.iPhone-12-Pro-Max', runtime['identifier']).strip())
PYSIM
)"
    export RAI_UA1_R_SIMULATOR
  fi
  export RAI_UA1_R_DERIVED="$rai_local/testing/simulator-derived"
}

case "$rai_profile" in
  -h|--help|help) usage; exit 0 ;;
  swift)
    rai_xcode_env
    xcrun swift test --package-path "$rai_root/ios/RAI" "$@"
    ;;
  simulator|e2e)
    [[ $# == 0 ]] || { usage >&2; exit 2; }
    prepare_simulator
    export RAI_UA1_R_BUILD_ONLY=0
    if [[ "$rai_profile" == e2e ]]; then export RAI_UA1_R_BUILD_ONLY=1; fi
    bash "$rai_root/scripts/test-ua1-r-simulator.sh"
    if [[ "$rai_profile" == e2e ]]; then
      rai_require_toolchain
      export RAI_UA1_R_XCTESTRUN="$RAI_UA1_R_DERIVED/Build/Products/ua1-r.xctestrun"
      export RAI_UA1_R_DIAGNOSTICS_DIR="$(mktemp -d "$rai_local/testing/e2e.XXXXXX")"
      "$rai_root/scripts/test-postgres.sh" go test -race -count=1 -tags=integration,ua1rsim -timeout=20m \
        -run '^TestWorkoutCompletenessSimulatorHTTPPostgres$' -v ./integration
    fi
    ;;
  unit)
    rai_require_toolchain
    go_tests "$@"
    ;;
  integration)
    rai_require_toolchain
    pg_tests "$@"
    ;;
  perf)
    rai_require_toolchain
    [[ $# == 0 ]] || { usage >&2; exit 2; }
    export RAI_LOCAL_WORKOUT_PERF=1
    "$rai_root/scripts/test-postgres.sh" go test -count=1 -tags=integration -timeout=40m \
      -run '^TestLocalWorkoutThroughput$' -v ./integration
    ;;
  quick|gate|full)
    rai_require_toolchain
    [[ $# == 0 ]] || { usage >&2; exit 2; }
    cd "$rai_root/apps/server"
    rai_unformatted="$(gofmt -l cmd internal integration migrations/embed.go)"
    if [[ -n "$rai_unformatted" ]]; then
      printf '%s\n' "$rai_unformatted" >&2
      echo 'gofmt required' >&2; exit 1
    fi
    if [[ "$rai_profile" == quick ]]; then
      go test ./...
    elif [[ "$rai_profile" == gate ]]; then
      go test -race ./...
    else
      go mod verify
      # Race executes unit tests too; avoid a redundant uncached unit pass.
      go test -race -count=1 ./...
      go vet -tags=integration ./...
      go build ./...
      rai_require_sqlc
      "$rai_sqlc" generate
      git -C "$rai_root" diff --exit-code -- apps/server/internal/storage/postgres/dbgen
      pg_tests
    fi
    go vet ./...
    python3 "$rai_root/scripts/check-boundaries.py"
    python3 "$rai_root/scripts/local/test_device.py"
    ;;
  *) usage >&2; exit 2 ;;
esac
echo "LOCAL CHECKS: PASS profile=$rai_profile elapsed_seconds=$((SECONDS - rai_started_seconds))"
if [[ "$rai_profile" == quick || "$rai_profile" == gate ]]; then
  echo 'Scope: Go tests/vet/boundaries and controller safety; PostgreSQL/iOS/device acceptance not run.'
fi
