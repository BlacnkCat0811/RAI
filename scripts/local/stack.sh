#!/usr/bin/env bash
# Build and run the API and Worker against the local PostgreSQL cluster.
# Migration is explicit: run `stack.sh migrate` before `stack.sh start`.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

usage() {
  echo "usage: $0 build|migrate|migrate-status|start|stop|restart|status|logs" >&2
  exit 2
}

build_binary() {
  rai_go_env
  mkdir -p "$rai_bin" "$rai_logs"
  ( cd "$rai_root/apps/server" && go build -trimpath -o "$rai_bin/rai" ./cmd/rai )
  "$rai_bin/rai" version
}

require_pg() {
  if ! rai_pg_running; then
    echo "postgres is not running; run scripts/local/pg.sh start" >&2
    return 1
  fi
}

run_rai() {
  env RAI_DATABASE_URL="$rai_database_url" "$rai_bin/rai" "$@"
}

migrate() {
  require_pg
  run_rai migrate
  run_rai migrate-status
}

api_pid_alive() {
  [[ -s "$rai_run/api.pid" ]] && kill -0 "$(cat "$rai_run/api.pid")" 2>/dev/null
}

worker_pid_alive() {
  [[ -s "$rai_run/worker.pid" ]] && kill -0 "$(cat "$rai_run/worker.pid")" 2>/dev/null
}

wait_ready() {
  i=0
  until curl -fsS "http://$rai_api_addr/ready" >/dev/null 2>&1; do
    i=$((i + 1))
    if ((i > 60)); then
      echo "API did not become ready; see $rai_logs/api.log" >&2
      return 1
    fi
    sleep 0.5
  done
}

start_stack() {
  require_pg
  if [[ ! -x "$rai_bin/rai" ]]; then
    echo "binary missing; run $0 build" >&2
    return 1
  fi
  if api_pid_alive || worker_pid_alive; then
    echo "stack already running; use $0 restart" >&2
    return 1
  fi
  mkdir -p "$rai_run" "$rai_logs"
  env RAI_DATABASE_URL="$rai_database_url" RAI_LISTEN_ADDR="$rai_api_addr" \
    nohup "$rai_bin/rai" api >>"$rai_logs/api.log" 2>&1 &
  echo $! >"$rai_run/api.pid"
  if ! wait_ready; then
    stop_stack || true
    return 1
  fi
  env RAI_DATABASE_URL="$rai_database_url" \
    nohup "$rai_bin/rai" worker >>"$rai_logs/worker.log" 2>&1 &
  echo $! >"$rai_run/worker.pid"
  sleep 1
  if ! worker_pid_alive; then
    echo "worker exited during startup; see $rai_logs/worker.log" >&2
    stop_stack || true
    return 1
  fi
  echo "api pid $(cat "$rai_run/api.pid"), worker pid $(cat "$rai_run/worker.pid")"
}

stop_process() {
  name="$1"
  pidfile="$rai_run/$name.pid"
  if [[ ! -s "$pidfile" ]]; then
    return 0
  fi
  pid="$(cat "$pidfile")"
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid"
    i=0
    while kill -0 "$pid" 2>/dev/null; do
      i=$((i + 1))
      if ((i > 100)); then
        echo "$name pid $pid did not exit after SIGTERM" >&2
        return 1
      fi
      sleep 0.1
    done
  fi
  rm -f "$pidfile"
}

stop_stack() {
  result=0
  stop_process api || result=1
  stop_process worker || result=1
  return "$result"
}

status_stack() {
  if api_pid_alive; then echo "api: running (pid $(cat "$rai_run/api.pid"))"; else echo "api: stopped"; fi
  if worker_pid_alive; then echo "worker: running (pid $(cat "$rai_run/worker.pid"))"; else echo "worker: stopped"; fi
  for path in health livez ready readyz; do
    code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 2 "http://$rai_api_addr/$path" 2>/dev/null || true)"
    echo "/$path: $code"
  done
}

show_logs() {
  echo "=== api.log ==="; tail -n 30 "$rai_logs/api.log" 2>/dev/null || true
  echo "=== worker.log ==="; tail -n 30 "$rai_logs/worker.log" 2>/dev/null || true
}

case "${1:-}" in
  build) build_binary ;;
  migrate) migrate ;;
  migrate-status) require_pg; run_rai migrate-status ;;
  start) start_stack ;;
  stop) stop_stack ;;
  restart) stop_stack; start_stack ;;
  status) status_stack ;;
  logs) show_logs ;;
  *) usage ;;
esac
