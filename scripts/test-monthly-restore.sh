#!/usr/bin/env bash
# Invoked inside the synthetic P7 harness, while its source Worker is stopped.
set -euo pipefail
[[ "${RAI_PROJECT:-}" == rai-p7-test-* && $# == 1 ]] || exit 2
root="$(cd "$(dirname "$0")/.." && pwd)"
monthly_work="$1/monthly"
source_project="$RAI_PROJECT"
source_secrets="$RAI_SECRETS_DIR"
monthly_project="rai-monthly-restore-$(date +%s)-$$"
compose=(docker compose -f "$root/deploy/release/compose.yaml")
created=0
cleanup(){
 rc=$?;trap - EXIT INT TERM
 if [[ "$created" == 1 ]];then RAI_PROJECT="$monthly_project" "${compose[@]}" -f "$monthly_work/target.yaml" down --volumes --remove-orphans >/dev/null 2>&1 || rc=1;fi
 echo "MONTHLY_TARGET_CLEANUP_EXIT=$rc"
 exit "$rc"
}
trap cleanup EXIT
"${compose[@]}" run --rm --no-deps admin ops verify > "$1/monthly-source.json"
python3 "$root/scripts/test-monthly-ssh.py" "$monthly_work"
archive="$(cat "$monthly_work/selected-archive")"
cat > "$monthly_work/target.yaml" <<'YAML'
services:
  postgres:
    labels: {rai.restore-target: 'true'}
YAML
export RAI_PROJECT="$monthly_project" RAI_SECRETS_DIR="$monthly_work/secrets"
python3 "$root/scripts/p7-secrets.py" "$RAI_SECRETS_DIR"
for file in tls_cert tls_key tls_ca;do cp "$source_secrets/$file" "$RAI_SECRETS_DIR/$file";done
docker run --rm -v "$RAI_SECRETS_DIR:/secrets" --entrypoint sh "$RAI_POSTGRES_IMAGE" -c "chown $(id -u):65532 /secrets/*_dsn /secrets/tls_key; chmod 640 /secrets/*_dsn /secrets/tls_key; chmod 644 /secrets/tls_cert /secrets/tls_ca"
created=1
"${compose[@]}" -f "$monthly_work/target.yaml" up -d --wait postgres
"${compose[@]}" exec -T postgres sh /opt/rai/bootstrap.sh >/dev/null
target="$("${compose[@]}" ps -q postgres)"
# Explicit identity confirmation, unlabelled target and bad confirmation refused.
if bash "$root/scripts/rai-restore.sh" "$archive" "$target" --confirm-target wrong >/dev/null 2>&1;then exit 1;fi
source_target="$(RAI_PROJECT="$source_project" "${compose[@]}" ps -q postgres)"
if bash "$root/scripts/rai-restore.sh" "$archive" "$source_target" --confirm-target "$source_target" >/dev/null 2>&1;then exit 1;fi
# A live target connection must stop restore, without terminating that session.
docker exec "$target" psql -X -U rai_bootstrap -d rai_v2 -c 'SELECT pg_sleep(8)' >/dev/null & connection=$!
for _ in $(seq 1 30);do
 if [[ "$(docker exec "$target" psql -X -U rai_bootstrap -d rai_v2 -Atc "SELECT count(*) FROM pg_stat_activity WHERE datname='rai_v2' AND wait_event='PgSleep'")" == 1 ]];then break;fi
 sleep .1
done
if bash "$root/scripts/rai-restore.sh" "$archive" "$target" --confirm-target "$target" >/dev/null 2>&1;then exit 1;fi
wait "$connection"
bash "$root/scripts/rai-restore.sh" "$archive" "$target" --confirm-target "$target"
"${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 -f /opt/rai/roles.sql -f /opt/rai/runtime-grants.sql >/dev/null
"${compose[@]}" run --rm --no-deps admin ops verify > "$1/monthly-target.json"
cmp "$1/monthly-source.json" "$1/monthly-target.json"
if bash "$root/scripts/rai-restore.sh" "$archive" "$target" --confirm-target "$target" >/dev/null 2>&1;then exit 1;fi
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8443 | awk -F: '{print $NF}')"
export RAI_E2E_URL="https://localhost:$api_port" SSL_CERT_FILE="$RAI_SECRETS_DIR/tls_ca"
python3 "$root/scripts/p3-e2e-client.py" retry
python3 "$root/scripts/p7-workload.py" verify
python3 - "$1/monthly-target.json" <<'PY'
import json,sys
m=json.load(open(sys.argv[1]));assert m['schema_version']==13 and m['invariant_violations']==0 and len(m['tables'])==45
print('MONTHLY_SQL_RESTORE='+json.dumps({'status':'PASS','table_count':len(m['tables']),'schema':m['schema_version'],'invariant_violations':0,'all_table_counts_and_hashes_equal':True,'p3_original_receipt':True,'p5_override_effective_execution_feedback':True}))
PY
