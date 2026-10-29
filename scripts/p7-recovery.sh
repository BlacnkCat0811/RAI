#!/usr/bin/env bash
# Destructive fault injection ONLY for this run's synthetic P7 project.
set -euo pipefail
[[ "${RAI_PROJECT:-}" == rai-p7-test-* || "${RAI_PROJECT:-}" == rai-p7-restore-* ]] || exit 2
root="$(cd "$(dirname "$0")/.." && pwd)"
compose=(docker compose -f "$root/deploy/release/compose.yaml")
sql(){ "${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 "$@"; }
await_sleep(){
 for _ in $(seq 1 80);do
  if [[ "$(sql -Atc "SELECT count(*) FROM pg_stat_activity WHERE datname='rai_v2' AND wait_event='PgSleep'")" -gt 0 ]];then return;fi
  sleep .1
 done
 echo 'fault checkpoint not reached' >&2;return 1
}
pause_batches(){
 sql >/dev/null <<'SQL'
CREATE FUNCTION p7_pause() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_sleep(12); RETURN NEW; END$$;
CREATE TRIGGER p7_pause BEFORE UPDATE ON sync_batches FOR EACH ROW WHEN(NEW.state='committed') EXECUTE FUNCTION p7_pause();
SQL
}
clear_batches(){ sql -c 'DROP TRIGGER p7_pause ON sync_batches; DROP FUNCTION p7_pause();' >/dev/null; }
# API death while a transaction is paused before final receipt publication.
python3 "$root/scripts/p7-fault-client.py" prepare
before="$(sql -Atc 'SELECT count(*) FROM health_samples')"
pause_batches
python3 "$root/scripts/p7-fault-client.py" send & sender=$!
await_sleep
docker kill --signal KILL "$("${compose[@]}" ps -q api)" >/dev/null
wait "$sender"
clear_batches
[[ "$(sql -Atc 'SELECT count(*) FROM health_samples')" == "$before" ]]
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8443 | awk -F: '{print $NF}')"
export RAI_E2E_URL="https://localhost:$api_port"
python3 "$root/scripts/p7-fault-client.py" retry
echo 'P7_API_CRASH=PASS in-flight transaction rolled back; original retry recovered'
# Real Worker SIGKILL after run creation, during publication transaction.
"${compose[@]}" stop worker >/dev/null
python3 "$root/scripts/p7-fault-client.py" worker
job="$(cat "$RAI_E2E_STATE_DIR/worker-job")"
sql >/dev/null <<'SQL'
CREATE FUNCTION p7_pause() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN
 IF EXISTS(SELECT 1 FROM analysis_runs WHERE id=NEW.run_id AND job_id=TG_ARGV[0]::uuid) THEN PERFORM pg_sleep(12); END IF;
 RETURN NEW; END$$;
SQL
sql -c "CREATE TRIGGER p7_pause BEFORE INSERT ON analysis_results FOR EACH ROW EXECUTE FUNCTION p7_pause('$job');" >/dev/null
"${compose[@]}" up -d worker
await_sleep
killed_attempt="$(sql -Atc "SELECT attempts FROM jobs WHERE id='$job'")"
docker kill --signal KILL "$("${compose[@]}" ps -q worker)" >/dev/null
sql -c 'DROP TRIGGER p7_pause ON analysis_results; DROP FUNCTION p7_pause();' >/dev/null
[[ "$(sql -Atc "SELECT count(*) FROM analysis_results r JOIN analysis_runs a ON a.dataset_id=r.dataset_id AND a.id=r.run_id WHERE a.job_id='$job'")" == 0 ]]
"${compose[@]}" up -d --wait worker
recovered=0
for _ in $(seq 1 100);do
 if [[ "$(sql -Atc "SELECT count(*) FROM jobs WHERE id='$job' AND state='succeeded' AND attempts=$((killed_attempt+1))")" == 1 ]];then recovered=1;break;fi
 sleep 1
done
if [[ "$recovered" != 1 ]];then
 sql -Atc "SELECT json_build_object('state',state,'attempts',attempts,'error_code',last_error_code) FROM jobs WHERE id='$job'"
 sql -Atc "SELECT status,error_code FROM analysis_runs WHERE job_id='$job'"
 echo 'P7_WORKER_CRASH=FAIL recovery deadline exceeded' >&2
 exit 1
fi
[[ "$(sql -Atc "SELECT count(*) FROM analysis_runs WHERE job_id='$job' AND status='SUCCEEDED'")" == 1 ]]
[[ "$(sql -Atc "SELECT count(*) FROM analysis_runs WHERE job_id='$job' AND status='FAILED' AND error_code='lease_expired'")" == 1 ]]
echo "P7_WORKER_CRASH=PASS actual SIGKILL of attempt=$killed_attempt; real 60-second lease expiry; rollback then one successful run"
# Database process death with an open transaction. Durable prior facts survive.
python3 "$root/scripts/p7-fault-client.py" prepare
before="$(sql -Atc 'SELECT count(*) FROM health_samples')"
pause_batches
python3 "$root/scripts/p7-fault-client.py" send & sender=$!
await_sleep
docker kill --signal KILL "$("${compose[@]}" ps -q postgres)" >/dev/null
wait "$sender"
python3 "$root/scripts/p7-fault-client.py" unavailable
"${compose[@]}" up -d --wait postgres
clear_batches
[[ "$(sql -Atc 'SELECT count(*) FROM health_samples')" == "$before" ]]
python3 "$root/scripts/p7-fault-client.py" retry
python3 "$root/scripts/p3-e2e-client.py" retry
echo 'P7_DATABASE_CRASH=PASS WAL recovery; committed receipt retained; in-flight facts rolled back; pool reconnected'
# Keep HTTPS ingress while severing only API -> DB connectivity.
api="$("${compose[@]}" ps -q api)"
docker network disconnect "${RAI_PROJECT}_database" "$api"
python3 "$root/scripts/p7-fault-client.py" unavailable
docker network connect "${RAI_PROJECT}_database" "$api"
python3 "$root/scripts/p3-e2e-client.py" retry
echo 'P7_NETWORK_RECOVERY=PASS synthetic API/database disconnect and reconnect'
