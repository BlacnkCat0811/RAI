#!/usr/bin/env bash
# Shared paths and settings for the sudo-free macOS local development stack.
# Sourced, never executed. Compatible with the system bash 3.2.

rai_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
rai_local="$rai_root/.local"
rai_toolchain="$rai_local/toolchain"
rai_pgbin="$rai_toolchain/Postgres.app/Contents/Versions/18/bin"
rai_pgdata="$rai_local/pgdata"
rai_run="$rai_local/run"
rai_logs="$rai_local/logs"
rai_backups="$rai_local/backups"
rai_bin="$rai_local/bin"

rai_go_version="1.27.3"
rai_pg_version="18.6"
rai_sqlc_version="1.31.1"
rai_sqlc="$rai_bin/sqlc"
rai_node_version="22.23.2"
rai_node_bin="$rai_toolchain/node/bin"
rai_npm_cache="$rai_local/npm-cache"
rai_redocly_version="2.71.1"
rai_db_port="${RAI_LOCAL_DB_PORT:-55432}"
rai_db_name="${RAI_LOCAL_DB_NAME:-rai_dev}"
rai_db_user="${RAI_LOCAL_DB_USER:-rai_dev}"
rai_db_password="${RAI_LOCAL_DB_PASSWORD:-development-only}"
rai_api_addr="${RAI_LISTEN_ADDR:-127.0.0.1:8080}"

# Local scripts never consume ambient database credentials or libpq settings.
# This keeps a production DSN in the shell from redirecting a destructive reset.
unset PGHOST PGHOSTADDR PGPORT PGDATABASE PGUSER PGPASSWORD PGPASSFILE PGSERVICE PGSERVICEFILE PGOPTIONS
unset RAI_DATABASE_URL RAI_DATABASE_URL_FILE
rai_database_url="postgres://$rai_db_user:$rai_db_password@127.0.0.1:$rai_db_port/$rai_db_name?sslmode=disable"

rai_go_env() {
  export GOTOOLCHAIN=local
  export GOPATH="$rai_local/gopath"
  export GOCACHE="$rai_local/gocache"
  export GOMODCACHE="$rai_local/gopath/pkg/mod"
  export PATH="$rai_toolchain/go/bin:$PATH"
}

# Node is only needed for the separate OpenAPI contract lint; the server CI job
# and every other local script stay Node-free.
rai_node_env() {
  export PATH="$rai_node_bin:$PATH"
  export npm_config_cache="$rai_npm_cache"
  export npm_config_update_notifier=false
  export npm_config_fund=false
  export npm_config_audit=false
}

rai_require_node() {
  if [[ ! -x "$rai_node_bin/node" ]]; then
    echo "Node toolchain missing; run scripts/local/bootstrap.sh" >&2
    return 1
  fi
}

rai_require_toolchain() {
  if [[ ! -x "$rai_toolchain/go/bin/go" ]]; then
    echo "Go toolchain missing; run scripts/local/bootstrap.sh" >&2
    return 1
  fi
  if [[ ! -x "$rai_pgbin/postgres" ]]; then
    echo "PostgreSQL toolchain missing; run scripts/local/bootstrap.sh" >&2
    return 1
  fi
}

rai_require_sqlc() {
  if [[ ! -x "$rai_sqlc" ]]; then
    echo "sqlc toolchain missing; run scripts/local/bootstrap.sh" >&2
    return 1
  fi
}

rai_pg_running() {
  "$rai_pgbin/pg_ctl" -D "$rai_pgdata" status >/dev/null 2>&1
}
