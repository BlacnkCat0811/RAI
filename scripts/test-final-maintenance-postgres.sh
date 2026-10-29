#!/usr/bin/env bash
# Synthetic PostgreSQL regression in a unique Compose project, never legacy PG.
# Invoked from apps/server by test-remote.sh; accepts a Go command verbatim.
set -euo pipefail
umask 077
state_dir="$(mktemp -d)"
project="rai-maintenance-pg-$(date +%s)-$$"
compose=(docker compose -p "$project" -f "$state_dir/compose.json")
cleanup(){ rc=$?;trap - EXIT INT TERM;cleanup_rc=0;"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || cleanup_rc=1;rm -rf -- "$state_dir";echo "MAINTENANCE_PG_CLEANUP=$cleanup_rc";if [[ "$cleanup_rc" != 0 ]];then rc=1;fi;exit "$rc"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 - "$state_dir" <<'PY'
import json,pathlib,secrets,sys
p=pathlib.Path(sys.argv[1]);password=secrets.token_hex(24)
(p/'password').write_text(password)
(p/'compose.json').write_text(json.dumps({'services':{'postgres':{'image':'postgres:18.6','environment':{'POSTGRES_USER':'rai_test','POSTGRES_DB':'rai_test','POSTGRES_PASSWORD':password,'POSTGRES_INITDB_ARGS':'--data-checksums'},'ports':['127.0.0.1::5432'],'volumes':['data:/var/lib/postgresql'],'healthcheck':{'test':['CMD-SHELL','pg_isready -h 127.0.0.1 -U rai_test -d rai_test'],'interval':'1s','timeout':'3s','retries':90},'mem_limit':'1g'}},'volumes':{'data':{}}}))
PY
"${compose[@]}" up -d --wait postgres
"${compose[@]}" exec -T postgres psql -X -U rai_test -d rai_test -Atc "SELECT 'MAINTENANCE_PG_VERSION='||current_setting('server_version')"
"${compose[@]}" exec -T postgres psql -X -U rai_test -d rai_test -Atc "SELECT 'MAINTENANCE_PG_SETTINGS='||json_build_object('server_version',current_setting('server_version'),'fsync',current_setting('fsync'),'full_page_writes',current_setting('full_page_writes'),'synchronous_commit',current_setting('synchronous_commit'),'data_checksums',current_setting('data_checksums'))::text"
port="$("${compose[@]}" port postgres 5432 | awk -F: '{print $NF}')"
export RAI_TEST_DATABASE_URL="postgres://rai_test:$(cat "$state_dir/password")@127.0.0.1:$port/rai_test?sslmode=disable"
"$@"
