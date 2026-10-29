#!/usr/bin/env bash
# Runs on the fnOS/N100 host; invoked by scripts/test-remote.sh.
# Prefers the host Go 1.27.3 + gcc for a native Linux run. Falls back to a
# throwaway Go container. PostgreSQL always runs in a throwaway container.
set -euo pipefail
umask 077

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

run_id="${RAI_RUN_ID:-$(date +%Y%m%d%H%M%S)-$$}"
net="rai-test-net-$run_id"
pg="rai-test-pg-$run_id"
go_ctr="rai-test-go-$run_id"
pass="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
mode="${RAI_TEST_RUNTIME:-auto}"

pg_image="${RAI_TEST_PG_IMAGE:-postgres:18.6}"
go_image="${RAI_TEST_GO_IMAGE:-golang:1.27.3-bookworm}"
go_mem="${RAI_TEST_GO_MEMORY:-3g}"
go_cpus="${RAI_TEST_CPUS:-2}"
gomaxprocs="${RAI_TEST_GOMAXPROCS:-2}"
goproxy="${RAI_TEST_GOPROXY:-https://proxy.golang.org,direct}"
mod_cache="${RAI_GO_MOD_CACHE:-$HOME/go/pkg/mod}"
build_cache="${RAI_GO_BUILD_CACHE:-$HOME/.cache/go-build}"
mkdir -p "$mod_cache" "$build_cache"

if [[ "$mode" == auto ]]; then
  if command -v go >/dev/null 2>&1 \
    && [[ "$(go env GOVERSION 2>/dev/null || true)" == go1.27.3 ]] \
    && command -v gcc >/dev/null 2>&1; then
    mode=host
  else
    mode=container
  fi
fi
case "$mode" in
  host|container) ;;
  *) echo "RAI_TEST_RUNTIME must be auto, host or container" >&2; exit 2 ;;
esac

network_created=0
pg_created=0
pg_volumes=()
ownership_dir="$(mktemp -d)"
cleanup() {
  rc=$?
  trap - EXIT INT TERM
  if [[ -f "$ownership_dir/go.cid" ]];then
    owned_go_id="$(cat "$ownership_dir/go.cid")"
    if [[ "$(docker inspect -f '{{index .Config.Labels "rai.test.run"}}' "$owned_go_id" 2>/dev/null || true)" == "$run_id" ]];then
      docker rm -fv "$owned_go_id" >/dev/null 2>&1 || rc=1
    fi
  fi
  if [[ "$pg_created" == 1 && "$(docker inspect -f '{{index .Config.Labels "rai.test.run"}}' "$pg" 2>/dev/null || true)" == "$run_id" ]];then
    # Official PG18 declares a VOLUME; -f alone leaves an anonymous data volume.
    # Only this successfully-created, run-labelled container is eligible.
    docker rm -fv "$pg" >/dev/null 2>&1 || rc=1
    for volume in "${pg_volumes[@]}";do
      if docker volume inspect "$volume" >/dev/null 2>&1;then
        echo 'Owned test PostgreSQL volume cleanup failed; no prune attempted' >&2
        rc=1
      fi
    done
  fi
  if [[ "$network_created" == 1 ]]; then
    docker network rm "$net" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$ownership_dir"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ "$mode" == container ]]; then
  for image in "$pg_image" "$go_image"; do
    if ! docker image inspect "$image" >/dev/null 2>&1; then
      echo "Pulling $image ..." >&2
      docker pull "$image" >&2
    fi
  done
else
  if ! docker image inspect "$pg_image" >/dev/null 2>&1; then
    echo "Pulling $pg_image ..." >&2
    docker pull "$pg_image" >&2
  fi
fi

echo "RAI_REMOTE_RUN=$run_id"
echo "RAI_REMOTE_MODE=$mode"
echo "RAI_REMOTE_PG_IMAGE=$pg_image"

if [[ "$mode" == host ]]; then
  docker run -d --name "$pg" \
    --label "rai.test.run=$run_id" \
    --memory "${RAI_TEST_PG_MEMORY:-1g}" \
    -p 127.0.0.1::5432 \
    -e POSTGRES_USER=rai_test \
    -e POSTGRES_PASSWORD="$pass" \
    -e POSTGRES_DB=rai_test \
    -e POSTGRES_INITDB_ARGS=--data-checksums \
    "$pg_image" \
    -c max_connections=100 \
    -c shared_buffers=128MB \
    -c fsync=on \
    -c full_page_writes=on \
    -c synchronous_commit=on >/dev/null
else
  echo "RAI_REMOTE_GO_IMAGE=$go_image"
  docker network create "$net" >/dev/null
  network_created=1
  docker run -d --name "$pg" --network "$net" \
    --label "rai.test.run=$run_id" \
    --memory "${RAI_TEST_PG_MEMORY:-1g}" \
    -e POSTGRES_USER=rai_test \
    -e POSTGRES_PASSWORD="$pass" \
    -e POSTGRES_DB=rai_test \
    -e POSTGRES_INITDB_ARGS=--data-checksums \
    "$pg_image" \
    -c max_connections=100 \
    -c shared_buffers=128MB \
    -c fsync=on \
    -c full_page_writes=on \
    -c synchronous_commit=on >/dev/null
fi

pg_created=1
read -r -a pg_volumes < <(docker inspect -f '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}' "$pg")

ready=false
for _ in $(seq 1 90); do
  # The image starts a temporary socket-only server during initialization.
  # Wait for the final TCP listener, not that transient server.
  if docker exec "$pg" pg_isready -h 127.0.0.1 -U rai_test -d rai_test >/dev/null 2>&1; then
    ready=true
    break
  fi
  if [[ "$(docker inspect -f '{{.State.Running}}' "$pg" 2>/dev/null || echo false)" != true ]]; then
    docker logs "$pg" >&2 || true
    echo 'PostgreSQL container exited before becoming ready' >&2
    exit 1
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  docker logs "$pg" >&2 || true
  echo 'PostgreSQL container did not become ready' >&2
  exit 1
fi

settings="$(docker exec "$pg" psql -U rai_test -d rai_test -Atc "SELECT json_build_object('server_version',current_setting('server_version'),'fsync',current_setting('fsync'),'full_page_writes',current_setting('full_page_writes'),'synchronous_commit',current_setting('synchronous_commit'),'data_checksums',current_setting('data_checksums'),'shared_buffers',current_setting('shared_buffers'))" 2>/dev/null || true)"
if [[ -n "$settings" ]]; then
  echo "RAI_REMOTE_PG_SETTINGS=$settings"
fi

if [[ $# -eq 0 ]]; then
  set -- go test -race -count=1 -tags=integration -p 1 -timeout=30m ./...
fi

status=0
if [[ "$mode" == host ]]; then
  port="$(docker port "$pg" 5432/tcp | awk -F: '{print $NF}' | head -1)"
  if [[ -z "$port" ]]; then
    echo 'Could not determine the PostgreSQL host port' >&2
    exit 1
  fi
  echo "RAI_REMOTE_PG_PORT=$port"
  (
    cd "$root/apps/server"
    export RAI_TEST_DATABASE_URL="postgres://rai_test:$pass@127.0.0.1:$port/rai_test?sslmode=disable"
    export RAI_TEST_PG_CONTAINER="$pg"
    export GOMAXPROCS="$gomaxprocs"
    export CGO_ENABLED=1
    export GOMODCACHE="$mod_cache"
    export GOCACHE="$build_cache"
    export GOFLAGS=-mod=readonly
    export GOPROXY="$goproxy"
    export GOTOOLCHAIN=local
    "$@"
  ) || status=$?
else
  docker run --rm --cidfile "$ownership_dir/go.cid" --name "$go_ctr" --network "$net" \
    --label "rai.test.run=$run_id" \
    --memory "$go_mem" --cpus "$go_cpus" \
    -v "$root:/src" \
    -v "$mod_cache:/go/pkg/mod" \
    -v "$build_cache:/root/.cache/go-build" \
    -w /src/apps/server \
    -e "RAI_TEST_DATABASE_URL=postgres://rai_test:$pass@$pg:5432/rai_test?sslmode=disable" \
    -e GOFLAGS=-mod=readonly \
    -e "GOMAXPROCS=$gomaxprocs" \
    -e CGO_ENABLED=1 \
    -e "GOPROXY=$goproxy" \
    -e GOTOOLCHAIN=local \
    "$go_image" "$@" || status=$?
fi

exit "$status"
