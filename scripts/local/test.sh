#!/usr/bin/env bash
# All development checks run on this Mac. No SSH or remote database fallback.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

usage() {
  cat <<'USAGE'
usage: scripts/local/test.sh [quick|gate|unit|integration|full|syncperf|swift|simulator|training-ui|simupload|e2e|perf] [test arguments]
  quick        cached Go unit tests, vet and dependency boundaries (default)
  gate         cached Go race, vet and boundaries plus isolated PG admin migration tests
  unit         Go unit tests; accepts -run and package arguments
  integration  fresh local PostgreSQL 18, race enabled; accepts -run/packages
  full         uncached Go race, vet, build, sqlc, full isolated PG regression
  swift        native Swift package tests; accepts --filter
  simulator    synthetic App XCTest on a dedicated local Simulator
  training-ui  adaptive-plan UI -> local Go API/Worker -> disposable PG18
  simupload    Simulator App upload of protocol-6 Workouts over real TCP to a
               local Go API + isolated PG18. Starts and stops that stack itself;
               add --drop to remove the database and credential afterwards.
  e2e          Simulator -> local Go HTTP -> disposable local PG18 (R3 v2)
  perf         synthetic 1/10/100/500-Workout local HTTP/PG throughput
  syncperf     R0 optimized (no -race) build, real loopback TCP HTTP -> production
               handler -> isolated PG18 with default budgets and durability.
               usage: syncperf --profiles S60,S60H,S240 --iterations 30 --warmups 3
               [--cold 5]; prints build facts, race state, per-profile n/failures
               and nearest-rank P50/P95. Never signs the race correctness gates.
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
  training-ui)
    [[ $# == 0 ]] || { usage >&2; exit 2; }
    rai_require_toolchain
    prepare_simulator
    pg_tests -tags=integration,traininguisim -run '^TestTrainingPlanV4SimulatorHTTPPostgres$' -v ./integration
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
        -run '^(TestWorkoutCompletenessSimulatorHTTPPostgres|TestModelStackSimulatorHTTPPostgres)$' -v ./integration
    fi
    ;;
  simupload)
    bash "$rai_root/scripts/local/sim-upload.sh" all "$@"
    ;;
  unit)
    rai_require_toolchain
    go_tests "$@"
    ;;
  integration)
    rai_require_toolchain
    # The `swiftclient` tag compiles the Swift client inside the Go integration
    # tests, which needs the Testing/XCTest modules that ship with full Xcode.
    # CommandLineTools alone fails at compile time with "no such module 'Testing'".
    if [[ " $* " == *swiftclient* ]]; then rai_xcode_env; fi
    pg_tests "$@"
    ;;
  syncperf)
    # R0: the only entry point that measures the optimized build. It must not
    # inherit race instrumentation or disabled optimizations from an ambient
    # GOFLAGS, and it asserts the effective build settings before measuring.
    rai_require_toolchain
    rai_syncperf_profiles="S60,S60H,S240"
    rai_syncperf_iterations=30
    rai_syncperf_warmups=3
    rai_syncperf_cold=5
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --profiles) rai_syncperf_profiles="${2:?--profiles needs a value}"; shift 2 ;;
        --iterations) rai_syncperf_iterations="${2:?--iterations needs a value}"; shift 2 ;;
        --warmups) rai_syncperf_warmups="${2:?--warmups needs a value}"; shift 2 ;;
        --cold) rai_syncperf_cold="${2:?--cold needs a value}"; shift 2 ;;
        *) echo "unknown syncperf argument: $1" >&2; usage >&2; exit 2 ;;
      esac
    done
    # Exclusivity snapshot: a shared machine inflates the tail, so every perf
    # log carries the load average and any competing build/simulator lane.
    rai_syncperf_machine() {
      local competitors
      competitors="$(pgrep -fl 'xcodebuild|simctl|swiftc|swift-frontend|swift-build' 2>/dev/null | wc -l | tr -d ' ' || true)"
      local top
      top="$(ps -Aro pcpu,comm 2>/dev/null | head -4 | tr '\n' ';' || true)"
      echo "SYNCPERF_MACHINE phase=$1 exclusive=$([[ "$competitors" == "0" ]] && echo true || echo false) competitors=${competitors} load=\"$(uptime)\" top_cpu=\"${top}\""
    }
    rai_syncperf_machine before

    rai_syncperf_goflags_in="${GOFLAGS:-}"
    rai_syncperf_goflags_out=""
    for rai_token in $rai_syncperf_goflags_in; do
      case "$rai_token" in
        -race|-N|-l) continue ;;
        -gcflags=*|-asmflags=*)
          case "$rai_token" in
            *-N*|*-l*) continue ;;
          esac ;;
      esac
      rai_syncperf_goflags_out="${rai_syncperf_goflags_out:+$rai_syncperf_goflags_out }$rai_token"
    done
    export GOFLAGS="$rai_syncperf_goflags_out"
    echo "SYNCPERF_GOFLAGS_IN=${rai_syncperf_goflags_in:-<unset>}"
    echo "SYNCPERF_GOFLAGS_EFFECTIVE=${GOFLAGS:-<empty>}"
    rai_syncperf_bin="$rai_local/testing/syncperf.test"
    mkdir -p "$rai_local/testing"
    (cd "$rai_root/apps/server" && go test -c -tags=integration -o "$rai_syncperf_bin" ./integration)
    rai_syncperf_settings="$(go version -m "$rai_syncperf_bin")"
    printf '%s\n' "$rai_syncperf_settings" | grep -E '^[[:space:]]+build' || true
    if printf '%s\n' "$rai_syncperf_settings" | grep -qE '^[[:space:]]+build[[:space:]]+-race'; then
      echo 'SYNCPERF BUILD FAIL: measured binary was compiled with -race' >&2; exit 1
    fi
    if printf '%s\n' "$rai_syncperf_settings" | grep -qE '^[[:space:]]+build[[:space:]]+-gcflags=.*(-N|-l)'; then
      echo 'SYNCPERF BUILD FAIL: measured binary has disabled optimizations' >&2; exit 1
    fi
    if ! printf '%s\n' "$rai_syncperf_settings" | grep -qE '^[[:space:]]+build[[:space:]]+-tags=.*integration'; then
      echo 'SYNCPERF BUILD FAIL: integration build tag is not in effect' >&2; exit 1
    fi
    echo "SYNCPERF_PROFILES=$rai_syncperf_profiles iterations=$rai_syncperf_iterations warmups=$rai_syncperf_warmups cold=$rai_syncperf_cold"
    echo "SYNCPERF_GO=$(go version)"
    echo "SYNCPERF_PG=$("$rai_pgbin/postgres" --version)"
    export RAI_SYNC_PERF=1
    export RAI_SYNC_PERF_PROFILES="$rai_syncperf_profiles"
    export RAI_SYNC_PERF_ITERATIONS="$rai_syncperf_iterations"
    export RAI_SYNC_PERF_WARMUPS="$rai_syncperf_warmups"
    export RAI_SYNC_PERF_COLD="$rai_syncperf_cold"
    "$rai_root/scripts/test-postgres.sh" "$rai_syncperf_bin" \
      -test.run '^TestWorkoutSyncPerfServerHTTP$' -test.v -test.timeout 60m
    rai_syncperf_status=$?
    rai_syncperf_machine after
    exit "$rai_syncperf_status"
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
      # Admin migration tests need a real isolated PostgreSQL. Keep them in the
      # daily gate so migration/schema drift fails here, not only in a full run.
      pg_tests -v ./internal/admin/
    else
      go mod verify
      # Race executes unit tests too; avoid a redundant uncached unit pass.
      go test -race -count=1 ./...
      go vet -tags=integration ./...
      go build ./...
      rai_require_sqlc
      "$rai_sqlc" diff --no-remote
      pg_tests -v ./...
    fi
    go vet ./...
    python3 "$rai_root/scripts/check-boundaries.py"
    python3 "$rai_root/scripts/check-product-boundaries.py"
    python3 "$rai_root/scripts/local/test_device.py"
    ;;
  *) usage >&2; exit 2 ;;
esac
echo "LOCAL CHECKS: PASS profile=$rai_profile elapsed_seconds=$((SECONDS - rai_started_seconds))"
if [[ "$rai_profile" == quick ]]; then
  echo 'Scope: Go tests/vet/boundaries and controller safety; PostgreSQL/iOS/device acceptance not run.'
elif [[ "$rai_profile" == gate ]]; then
  echo 'Scope: Go race/vet/boundaries plus isolated PG admin migration tests; iOS/device acceptance not run.'
fi
