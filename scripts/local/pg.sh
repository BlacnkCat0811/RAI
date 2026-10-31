#!/usr/bin/env bash
# Dedicated loopback-only PostgreSQL 18 development cluster.
# Usage: pg.sh init|start|stop|status|reset|psql|backup|restore <dump>|list-backups
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

usage() {
  echo "usage: $0 init|start|stop|status|reset|psql|backup [file]|restore <file>|list-backups" >&2
  exit 2
}

init_cluster() {
  if [[ -s "$rai_pgdata/PG_VERSION" ]]; then
    start_cluster >/dev/null
    if ! "$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d postgres -Atc \
      "SELECT 1 FROM pg_database WHERE datname = '$rai_db_name'" | grep -q 1; then
      "$rai_pgbin/createdb" -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -O "$rai_db_user" "$rai_db_name"
    fi
    echo "cluster and database ready at $rai_pgdata"
    return 0
  fi
  mkdir -p "$rai_pgdata" "$rai_run" "$rai_logs" "$rai_backups"
  pwfile="$(mktemp "$rai_run/pw.XXXXXX")"
  printf '%s\n' "$rai_db_password" >"$pwfile"
  "$rai_pgbin/initdb" -D "$rai_pgdata" \
    --username="$rai_db_user" --pwfile="$pwfile" \
    --auth-local=trust --auth-host=scram-sha-256 \
    --encoding=UTF8 --locale=C --data-checksums >"$rai_logs/initdb.log"
  rm -f "$pwfile"
  "$rai_pgbin/pg_ctl" -D "$rai_pgdata" -l "$rai_logs/postgres.log" \
    -o "-p $rai_db_port -k $rai_run -h 127.0.0.1 -c timezone=UTC -c max_connections=50" -w start
  "$rai_pgbin/createdb" -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -O "$rai_db_user" "$rai_db_name"
  echo "initialized $rai_pgdata on 127.0.0.1:$rai_db_port/$rai_db_name"
}

start_cluster() {
  if rai_pg_running; then
    echo "postgres already running"
    return 0
  fi
  if [[ ! -s "$rai_pgdata/PG_VERSION" ]]; then
    echo "cluster not initialized; run $0 init" >&2
    return 1
  fi
  mkdir -p "$rai_run" "$rai_logs"
  "$rai_pgbin/pg_ctl" -D "$rai_pgdata" -l "$rai_logs/postgres.log" \
    -o "-p $rai_db_port -k $rai_run -h 127.0.0.1 -c timezone=UTC -c max_connections=50" -w start
}

stop_cluster() {
  if ! rai_pg_running; then
    echo "postgres not running"
    return 0
  fi
  "$rai_pgbin/pg_ctl" -D "$rai_pgdata" -m fast -w stop
}

status_cluster() {
  "$rai_pgbin/pg_ctl" -D "$rai_pgdata" status
  "$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$rai_db_name" -Atc \
    "SELECT current_setting('server_version'), current_setting('timezone'), current_setting('data_checksums')"
}

reset_cluster() {
  if rai_pg_running; then
    "$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d postgres -v ON_ERROR_STOP=1 \
      -c "DROP DATABASE IF EXISTS $rai_db_name" >/dev/null
    "$rai_pgbin/createdb" -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -O "$rai_db_user" "$rai_db_name"
    echo "database $rai_db_name recreated empty"
  else
    echo "postgres not running; run $0 start first" >&2
    return 1
  fi
}

psql_cluster() {
  exec "$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$rai_db_name" "$@"
}

backup_cluster() {
  file="${1:-$rai_backups/rai_dev-$(date +%Y%m%d-%H%M%S).dump}"
  mkdir -p "$(dirname "$file")"
  "$rai_pgbin/pg_dump" -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -Fc -f "$file" "$rai_db_name"
  echo "backup: $file"
  shasum -a 256 "$file"
}

restore_cluster() {
  file="${1:?restore requires a dump file}"
  target="${RAI_RESTORE_DB:-${rai_db_name}_restore}"
  if [[ ! -f "$file" ]]; then
    echo "dump not found: $file" >&2
    return 1
  fi
  "$rai_pgbin/psql" -X -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d postgres -v ON_ERROR_STOP=1 \
    -c "DROP DATABASE IF EXISTS $target" >/dev/null
  "$rai_pgbin/createdb" -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -O "$rai_db_user" "$target"
  "$rai_pgbin/pg_restore" -h "$rai_run" -p "$rai_db_port" -U "$rai_db_user" -d "$target" --no-owner --no-privileges "$file"
  echo "restored into $target"
}

list_backups() {
  ls -lh "$rai_backups" 2>/dev/null || true
}

rai_require_toolchain
case "${1:-}" in
  init) init_cluster ;;
  start) start_cluster ;;
  stop) stop_cluster ;;
  status) status_cluster ;;
  reset) reset_cluster ;;
  psql) shift; psql_cluster "$@" ;;
  backup) shift; backup_cluster "${1:-}" ;;
  restore) shift; restore_cluster "${1:-}" ;;
  list-backups) list_backups ;;
  *) usage ;;
esac
