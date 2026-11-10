#!/usr/bin/env bash
# ENOSPC in a separate 256MiB tmpfs database, never a host filesystem.
set -euo pipefail
[[ "${RAI_PROJECT:-}" == rai-p7-restore-* ]] || exit 2
[[ $# == 1 ]] || exit 2
root="$(cd "$(dirname "$0")/.." && pwd)"
export RAI_PROJECT="rai-p7-disk-$(date +%s)-$$"
private="$(mktemp -d)";created=0
compose=(docker compose -f "$root/deploy/release/compose.yaml" -f "$private/tmpfs.yaml")
cleanup(){ rc=$?;trap - EXIT INT TERM;if [[ "$created" == 1 ]];then "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || rc=1;fi;rm -rf -- "$private";exit "$rc"; }
trap cleanup EXIT
prior_secrets="$RAI_SECRETS_DIR"
export RAI_SECRETS_DIR="$private/secrets"
python3 "$root/scripts/p7-secrets.py" "$RAI_SECRETS_DIR"
for file in tls_cert tls_key tls_ca;do cp "$prior_secrets/$file" "$RAI_SECRETS_DIR/$file";done
docker run --rm -v "$RAI_SECRETS_DIR:/secrets" --entrypoint sh "$RAI_POSTGRES_IMAGE" -c "chown $(id -u):65532 /secrets/*_dsn /secrets/tls_key; chmod 640 /secrets/*_dsn /secrets/tls_key; chmod 644 /secrets/tls_cert /secrets/tls_ca"
cat > "$private/tmpfs.yaml" <<YAML
services:
  postgres:
    volumes: !override
      - $root/deploy/postgres:/opt/rai:ro
    tmpfs:
      - /var/lib/postgresql:rw,size=256m,mode=0755
    labels: {rai.restore-target: 'true'}
    mem_limit: 768m
YAML
created=1
if ! "${compose[@]}" up -d --wait postgres;then "${compose[@]}" logs --no-log-prefix --tail 30 postgres >&2;exit 1;fi
pg="$("${compose[@]}" ps -q postgres)"
[[ "$(docker inspect -f '{{index .HostConfig.Tmpfs "/var/lib/postgresql"}}' "$pg")" == *size=256m* ]]
"${compose[@]}" exec -T postgres sh /opt/rai/bootstrap.sh >/dev/null
bash "$root/scripts/p7-restore.sh" "$1" "$pg" "$RAI_BACKUP_KEY_FILE"
"${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 -f /opt/rai/roles.sql -f /opt/rai/runtime-grants.sql >/dev/null
"${compose[@]}" up -d --wait api
port="$("${compose[@]}" port api 8443 | awk -F: '{print $NF}')"
export RAI_E2E_URL="https://localhost:$port" RAI_P7_FAULT_EVENTS=1000
"${compose[@]}" up -d --wait worker
python3 "$root/scripts/p7-fault-client.py" prepare
# This file can consume at most the explicitly mounted 256MiB tmpfs.
docker exec "$pg" sh -c 'dd if=/dev/zero of=/var/lib/postgresql/p7-only-fill bs=1048576 2>/dev/null || true'
[[ "$(docker exec "$pg" df -Pk /var/lib/postgresql | awk 'NR==2 {print $4}')" == 0 ]]
echo 'P7_DISK_FAULT=PASS measured tmpfs free blocks=0'
python3 "$root/scripts/p7-fault-client.py" send
[[ "$(cat "$RAI_E2E_STATE_DIR/fault-observed-status")" == 503 ]]
docker exec "$pg" rm /var/lib/postgresql/p7-only-fill
"${compose[@]}" up -d --wait postgres
python3 "$root/scripts/p7-fault-client.py" retry
"${compose[@]}" up -d --wait worker
"${compose[@]}" run --rm --no-deps admin ops verify >/dev/null
echo 'P7_DISK_FULL=PASS actual ENOSPC in private 256MiB tmpfs; write returned 503; free-space recovery preserved original-identity atomic retry'
