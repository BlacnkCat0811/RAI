#!/usr/bin/env bash
# New disposable PG18 cluster only; never consumes an existing database URL.
set -euo pipefail
umask 077
# Ignore ambient libpq service/host settings as well as any existing test URL.
# The cluster owner is the current OS user; no password file is consulted.
unset PGHOST PGHOSTADDR PGPORT PGDATABASE PGUSER PGPASSWORD PGPASSFILE PGSERVICE PGSERVICEFILE PGOPTIONS
project_root="$(cd "$(dirname "$0")/.." && pwd)"
: "${PGBIN:?Set PGBIN to a PostgreSQL 18 bin directory}"
PGBIN="$(cd "$PGBIN" && pwd)"
if [[ "$("$PGBIN/postgres" --version)" != *" 18."* ]]; then
  echo 'PostgreSQL 18 is required' >&2
  exit 1
fi
test_root="$(mktemp -d /tmp/rai-pg.XXXXXX)"
touch "$test_root/pgpass"
export PGPASSFILE="$test_root/pgpass"
started=false
cleanup() {
  result=$?
  trap - EXIT INT TERM
  if $started; then
    if leftover="$("$PGBIN/psql" -X -h "$test_root/socket" -d rai_test -Atc "SELECT count(*) FROM pg_database WHERE datname NOT IN ('template0','template1','postgres','rai_test')")"; then
      echo "RAI_TEST_LEFTOVER_DATABASES=$leftover"
      if [[ "$leftover" != 0 ]]; then result=1; fi
    else
      result=1
    fi
    if ! "$PGBIN/pg_ctl" -D "$test_root/data" -m fast -w stop; then
      result=1
      "$PGBIN/pg_ctl" -D "$test_root/data" -m immediate -w stop || true
    fi
  fi
  # Remove only this invocation's fresh directory after PostgreSQL has stopped.
  if [[ ! -e "$test_root/data/postmaster.pid" ]]; then
    if rm -rf -- "$test_root"; then
      echo 'RAI_TEST_CLUSTER_REMOVED=1'
    else
      result=1
    fi
  else
    echo "Test cluster still running; retained $test_root" >&2
    result=1
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir "$test_root/socket"
"$PGBIN/initdb" -D "$test_root/data" --auth-local=trust --auth-host=reject --data-checksums > "$test_root/initdb.log"
"$PGBIN/pg_ctl" -D "$test_root/data" -l "$test_root/postgres.log" \
  -o "-k $test_root/socket -h '' -c max_connections=40 -c shared_buffers=64MB -c fsync=on -c full_page_writes=on -c synchronous_commit=on" -w start
started=true
"$PGBIN/createdb" -h "$test_root/socket" rai_test
"$PGBIN/psql" -X -h "$test_root/socket" -d rai_test -Atc \
  "SELECT json_build_object('server_version',current_setting('server_version'),'fsync',current_setting('fsync'),'full_page_writes',current_setting('full_page_writes'),'synchronous_commit',current_setting('synchronous_commit'),'data_checksums',current_setting('data_checksums'),'listen_addresses',current_setting('listen_addresses'),'shared_buffers',current_setting('shared_buffers'))"
export RAI_TEST_DATABASE_URL="postgres:///rai_test?host=$test_root/socket"
cd "$project_root/apps/server"
if [[ $# == 0 ]]; then
  go test -race -count=1 -tags=integration ./...
else
  "$@"
fi
