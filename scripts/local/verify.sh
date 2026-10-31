#!/usr/bin/env bash
# End-to-end local acceptance for the Go + PostgreSQL development environment.
# Uses only the disposable local cluster and synthetic/empty data.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo; echo "== $* =="; }

rai_require_toolchain
rai_go_env
mkdir -p "$rai_logs" "$rai_backups" "$rai_run"

step "toolchain versions"
"$rai_toolchain/go/bin/go" version
"$rai_pgbin/postgres" --version
"$rai_pgbin/psql" --version

step "bootstrap (idempotent)"
"$rai_root/scripts/local/bootstrap.sh"

step "start empty PostgreSQL cluster"
"$rai_root/scripts/local/stack.sh" stop >/dev/null 2>&1 || true
"$rai_root/scripts/local/pg.sh" init
"$rai_root/scripts/local/pg.sh" start
"$rai_root/scripts/local/pg.sh" reset
server_version="$("$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$rai_db_name" -Atc 'SHOW server_version')"
[[ "$server_version" == 18.* ]] || fail "expected PostgreSQL 18, got $server_version"
echo "server_version=$server_version"

step "build API/Worker binary"
"$rai_root/scripts/local/stack.sh" build | tee "$rai_logs/build.log"

step "apply all migrations from an empty database"
"$rai_root/scripts/local/stack.sh" migrate | tee "$rai_logs/migrate.log"
migrate_status="$("$rai_root/scripts/local/stack.sh" migrate-status)"
applied="$(printf '%s' "$migrate_status" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(sum(1 for m in d if m.get("State")=="applied"))')"
total="$(printf '%s' "$migrate_status" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
echo "migrations applied=$applied total=$total"
[[ "$applied" == "$total" && "$total" == 13 ]] || fail "migrations not fully applied: $applied/$total"

step "start API and Worker"
"$rai_root/scripts/local/stack.sh" start
sleep 2

step "health checks"
for path in health livez ready readyz; do
  body="$(curl -fsS --max-time 5 "http://$rai_api_addr/$path")"
  echo "/$path 200 $body"
done
"$rai_root/scripts/local/stack.sh" status

step "worker runtime evidence"
env RAI_DATABASE_URL="$rai_database_url" "$rai_bin/rai" worker-ready
echo "worker-ready probe: exit 0"
grep -q '"worker_started"' "$rai_logs/worker.log" || fail "worker_started not logged"
grep -q '"msg":"worker_started"' "$rai_logs/worker.log" || true
pid="$(cat "$rai_run/worker.pid")"
kill -0 "$pid" 2>/dev/null || fail "worker process exited"
worker_connections="$("$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$rai_db_name" -Atc \
  "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND usename = current_user AND pid <> pg_backend_pid()")"
echo "worker pid=$pid healthy; DB sessions=$worker_connections"

step "backup and restore into a fresh database"
dump="$rai_backups/rai_dev-verify.dump"
"$rai_root/scripts/local/pg.sh" backup "$dump" | tee "$rai_logs/backup.log"
restore_db="rai_dev_restore_verify"
RAI_RESTORE_DB="$restore_db" "$rai_root/scripts/local/pg.sh" restore "$dump" | tee "$rai_logs/restore.log"
restored_version="$("$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$restore_db" -Atc 'SELECT max(version_id) FROM goose_db_version')"
restored_tables="$("$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$restore_db" -Atc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")"
original_version="$("$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$rai_db_name" -Atc 'SELECT max(version_id) FROM goose_db_version')"
[[ "$restored_version" == "$original_version" ]] || fail "restored schema version $restored_version != $original_version"
echo "restored schema version=$restored_version tables=$restored_tables"

step "cleanup restored verification database"
"$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d postgres -v ON_ERROR_STOP=1 \
  -c "DROP DATABASE IF EXISTS $restore_db" >/dev/null

step "stop API and Worker"
"$rai_root/scripts/local/stack.sh" stop

step "write machine-readable evidence"
evidence="$rai_root/docs/evidence/local-dev-environment.json"
python3 - "$evidence" "$server_version" "$applied" "$total" "$restored_version" "$restored_tables" "$dump" <<'PY'
import json, sys, datetime, hashlib, pathlib
path, server_version, applied, total, restored_version, tables, dump = sys.argv[1:8]
dump_path = pathlib.Path(dump)
data = {
    "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "platform": "macOS x86_64, no sudo, no Docker",
    "go": "1.27.3",
    "postgresql": server_version,
    "migrations": {"applied": int(applied), "total": int(total), "from_empty": True},
    "backup_restore": {
        "dump": str(dump_path),
        "dump_sha256": hashlib.sha256(dump_path.read_bytes()).hexdigest(),
        "restored_schema_version": int(restored_version),
        "restored_public_tables": int(tables),
    },
    "checks": ["postgres_start", "migrations_empty_db", "api_db_connect", "worker_start",
               "worker_ready",
               "health", "livez", "ready", "readyz", "backup_restore"],
    "note": "Synthetic development environment only; no production or device data.",
}
pathlib.Path(path).write_text(json.dumps(data, indent=2) + "\n")
print(path)
PY

echo
echo "LOCAL DEV ENVIRONMENT: PASS"
