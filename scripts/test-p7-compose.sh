#!/usr/bin/env bash
# fnOS synthetic release rehearsal. Only resources created below are removed.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
run_id="$(date +%s)-$$"
export RAI_PROJECT="rai-p7-test-$run_id"
source_project="$RAI_PROJECT" restore_project="rai-p7-restore-$run_id"
export RAI_RELEASE_IMAGE="$RAI_PROJECT:local"
export RAI_TLS_SERVER_NAME=localhost RAI_BIND_ADDRESS=127.0.0.1 RAI_HTTPS_PORT=0
export RAI_POSTGRES_IMAGE=docker.m.daocloud.io/library/postgres:18.6-bookworm@sha256:bbf83594798e144bff365a9c191c5d779f181fb3135902064ab8ff11918020fc
work="$(mktemp -d)"
export RAI_SECRETS_DIR="$work/secrets"
source_secrets_dir="$RAI_SECRETS_DIR"
export RAI_BACKUP_KEY_FILE="$work/backup-key"
compose=(docker compose -f "$root/deploy/release/compose.yaml")
created_source=0 created_restore=0
cleanup(){
 rc=$?;trap - EXIT INT TERM
 if [[ "$rc" != 0 && "$created_source" == 1 ]];then
  "${compose[@]}" exec -T postgres psql -X -U rai_bootstrap -d rai_v2 -Atc "SELECT coalesce(json_agg(row_to_json(x)),'[]') FROM (SELECT j.kind,j.state,j.attempts,j.last_error_code,d.generation-j.target_generation AS generation_gap,d.clean_generation-j.target_generation AS clean_gap,count(*) FROM jobs j LEFT JOIN dirty_scopes d ON d.dataset_id=j.dataset_id AND d.scope_key=j.scope_key WHERE j.state IN ('failed','repair_required') GROUP BY 1,2,3,4,5,6) x" 2>/dev/null || true
 fi
 if [[ "$created_restore" == 1 ]];then RAI_PROJECT="$restore_project" "${compose[@]}" -f "$work/restore.yaml" down --volumes --remove-orphans >/dev/null 2>&1 || rc=1;fi
 if [[ "$created_source" == 1 ]];then RAI_PROJECT="$source_project" "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || rc=1;fi
 docker image rm "$RAI_RELEASE_IMAGE" >/dev/null 2>&1 || true
 # Files are all synthetic and created by this run, including private keys.
 docker run --rm -v "$work:/cleanup" --entrypoint sh "$RAI_POSTGRES_IMAGE" -c "chmod -R u+rwX /cleanup; chown -R $(id -u):$(id -g) /cleanup" >/dev/null 2>&1 || true
 rm -rf -- "$work"
 echo "P7_CLEANUP_EXIT=$rc"
 exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
python3 scripts/p7-secrets.py "$RAI_SECRETS_DIR"
openssl rand -hex 32 > "$RAI_BACKUP_KEY_FILE"
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$work/ca.key" -out "$RAI_SECRETS_DIR/tls_ca" -subj '/CN=RAI P7 Synthetic CA' -days 60 >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -keyout "$RAI_SECRETS_DIR/tls_key" -out "$work/server.csr" -subj '/CN=localhost' >/dev/null 2>&1
printf '%s\n' 'subjectAltName=DNS:localhost,IP:127.0.0.1' 'extendedKeyUsage=serverAuth' > "$work/extensions"
openssl x509 -req -in "$work/server.csr" -CA "$RAI_SECRETS_DIR/tls_ca" -CAkey "$work/ca.key" -CAcreateserial -out "$RAI_SECRETS_DIR/tls_cert" -days 30 -extfile "$work/extensions" >/dev/null 2>&1
# Local Compose file secrets are bind mounts: uid/gid settings are not a chmod.
docker run --rm -v "$RAI_SECRETS_DIR:/secrets" --entrypoint sh "$RAI_POSTGRES_IMAGE" -c "chown $(id -u):65532 /secrets/*_dsn /secrets/tls_key; chmod 640 /secrets/*_dsn /secrets/tls_key; chmod 644 /secrets/tls_cert /secrets/tls_ca"
python3 scripts/p7-preflight.py --synthetic
docker build -t "$RAI_RELEASE_IMAGE" --build-arg SOURCE_REVISION="${RAI_SOURCE_COMMIT:-unrecorded}" --build-arg GOPROXY=https://goproxy.cn,direct --build-arg GO_IMAGE=docker.m.daocloud.io/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb apps/server > "$work/build.log" 2>&1 || { tail -40 "$work/build.log";exit 1; }
created_source=1
"${compose[@]}" up -d --wait postgres
"${compose[@]}" exec -T postgres sh /opt/rai/bootstrap.sh >/dev/null
"${compose[@]}" run --rm --no-deps admin migrate >/dev/null
"${compose[@]}" run --rm --no-deps admin migrate >/dev/null
"${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 -f /opt/rai/roles.sql -f /opt/rai/runtime-grants.sql >/dev/null
"${compose[@]}" run --rm --no-deps admin init > "$work/init.json"
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8443 | awk -F: '{print $NF}')"
export RAI_E2E_URL="https://localhost:$api_port" SSL_CERT_FILE="$RAI_SECRETS_DIR/tls_ca" RAI_E2E_STATE_DIR="$work/client"
read -r RAI_E2E_TOKEN RAI_E2E_COLLECTOR < <(python3 - "$work/init.json" <<'PY'
import json,sys
c=json.load(open(sys.argv[1]));print(c['token'],c['collectorEpoch'])
PY
)
export RAI_E2E_TOKEN RAI_E2E_COLLECTOR
"${compose[@]}" exec -T api /rai version
python3 scripts/p3-e2e-client.py upload
python3 scripts/p3-e2e-client.py retry
"${compose[@]}" up -d --wait worker
python3 scripts/p7-workload.py seed
python3 scripts/p7-workload.py performance
for service in api worker;do
 echo "P7_SOURCE_PEAK_${service}=$("${compose[@]}" exec -T "$service" /rai resources)"
done
if [[ "${RAI_P7_SOAK_ONLY:-0}" == 1 ]];then
 python3 scripts/p7-workload.py settle
 bash scripts/p7-backup.sh "$work/soak-backup"
 bash scripts/p7-monitor.sh "$work/soak-backup/backup.json"
 python3 scripts/p7-workload.py soak
 echo 'P7_SOAK_ONLY=PASS (separate benchmark; does not claim recovery/restore gates)'
 exit 0
fi
bash scripts/p7-recovery.sh
api_port="$("${compose[@]}" port api 8443 | awk -F: '{print $NF}')"
export RAI_E2E_URL="https://localhost:$api_port"
"${compose[@]}" run --rm --no-deps monitor > "$work/metrics.json"
python3 - "$work/metrics.json" <<'PY'
import json,sys
s=json.load(open(sys.argv[1]));assert s['database_size_bytes']>0 and s['connections']>0 and s['tables']
print('P7_MONITORING=PASS aggregate database/worker/analysis/training snapshot')
PY
"${compose[@]}" stop worker >/dev/null
python3 scripts/p7-fault-client.py worker
python3 scripts/p7-workload.py capture
[[ "$("${compose[@]}" exec -T postgres psql -X -U rai_bootstrap -d rai_v2 -Atc "SELECT count(*) FROM jobs WHERE state='queued'")" -gt 0 ]]
"${compose[@]}" run --rm --no-deps admin ops verify > "$work/source-verify.json"
start_restore=$SECONDS
bash scripts/p7-backup.sh "$work/backup"
cat > "$work/restore.yaml" <<'YAML'
services:
  postgres:
    labels: {rai.restore-target: 'true'}
YAML
export RAI_PROJECT="$restore_project"
export RAI_SECRETS_DIR="$work/restore-secrets"
python3 scripts/p7-secrets.py "$RAI_SECRETS_DIR"
for file in tls_cert tls_key tls_ca;do cp "$source_secrets_dir/$file" "$RAI_SECRETS_DIR/$file";done
docker run --rm -v "$RAI_SECRETS_DIR:/secrets" --entrypoint sh "$RAI_POSTGRES_IMAGE" -c "chown $(id -u):65532 /secrets/*_dsn /secrets/tls_key; chmod 640 /secrets/*_dsn /secrets/tls_key; chmod 644 /secrets/tls_cert /secrets/tls_ca"
python3 - "$source_secrets_dir" "$RAI_SECRETS_DIR" <<'PY'
import pathlib,sys
a,b=map(pathlib.Path,sys.argv[1:])
assert all((a/('pg_'+role+'_password')).read_bytes()!=(b/('pg_'+role+'_password')).read_bytes() for role in ['bootstrap','owner','api','worker','backup','monitor'])
print('P7_RESTORE_CREDENTIAL_ISOLATION=PASS new database passwords; original device/batch identities come from backup')
PY
python3 scripts/p7-preflight.py --synthetic
created_restore=1
"${compose[@]}" -f "$work/restore.yaml" up -d --wait postgres
"${compose[@]}" -f "$work/restore.yaml" exec -T postgres sh /opt/rai/bootstrap.sh >/dev/null
restore_container="$("${compose[@]}" -f "$work/restore.yaml" ps -q postgres)"
bash scripts/p7-restore.sh "$work/backup" "$restore_container" "$RAI_BACKUP_KEY_FILE"
"${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 -f /opt/rai/roles.sql -f /opt/rai/runtime-grants.sql >/dev/null
"${compose[@]}" run --rm --no-deps admin ops verify > "$work/restore-verify.json"
cmp "$work/source-verify.json" "$work/restore-verify.json"
python3 - "$work" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]);files={'source_inventory':'source-verify.json','restored_inventory':'restore-verify.json','backup_metadata':'backup/backup.json','aggregate_snapshot':'metrics.json'}
print('P7_RESTORE_EVIDENCE='+json.dumps({key:json.loads((p/file).read_text()) for key,file in files.items()},separators=(',',':')))
PY
echo 'P7_QUEUED_RESTORE=PASS queued job identity and state included in exact inventory comparison'
"${compose[@]}" up -d --wait api
api_port="$("${compose[@]}" port api 8443 | awk -F: '{print $NF}')"
export RAI_E2E_URL="https://localhost:$api_port"
python3 scripts/p3-e2e-client.py retry
python3 scripts/p7-workload.py verify
echo "P7_BACKUP_RESTORE=PASS all 45 tables identical; invariants, FK, original receipt and Effective/execution verified; drill_seconds=$((SECONDS-start_restore))"
echo "P7_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$RAI_RELEASE_IMAGE")"
# Recovery and bounded soak use the restored environment, never the source DB.
"${compose[@]}" up -d --wait worker
python3 scripts/p7-workload.py settle
bash scripts/p7-monitor.sh "$work/backup/backup.json"
python3 scripts/p7-workload.py soak
for _ in 1 2 3;do
 "${compose[@]}" stop api worker >/dev/null
 [[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq api)")" == 0 ]]
 [[ "$(docker inspect -f '{{.State.ExitCode}}' "$("${compose[@]}" ps -aq worker)")" == 0 ]]
 "${compose[@]}" up -d --wait api worker
 api_port="$("${compose[@]}" port api 8443 | awk -F: '{print $NF}')"
 export RAI_E2E_URL="https://localhost:$api_port"
 python3 scripts/p3-e2e-client.py retry
done
docker stats --no-stream --format 'P7_RESOURCE={{json .}}' $("${compose[@]}" ps -q api worker postgres)
for service in api worker;do
 echo "P7_CGROUP_${service}=$("${compose[@]}" exec -T "$service" /rai resources)"
done
echo "P7_CGROUP_postgres_memory_peak=$("${compose[@]}" exec -T postgres cat /sys/fs/cgroup/memory.peak)"
free -b
df -B1 "$work"
"${compose[@]}" run --rm --no-deps admin ops verify >/dev/null
bash scripts/p7-disk-full.sh "$work/backup"
echo 'P7_COMPOSE=PASS (isolated TLS/roles/backup/restore/lifecycle/synthetic workload; no production release)'
