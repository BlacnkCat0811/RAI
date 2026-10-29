#!/usr/bin/env bash
# Local aggregate output + exit status. No external notification is sent.
set -euo pipefail
umask 077
[[ $# == 1 ]] || { echo 'usage: p7-monitor.sh --monthly | LEGACY_DAILY_BACKUP_METADATA' >&2;exit 2; }
: "${RAI_PROJECT:?}" "${RAI_SECRETS_DIR:?}"
root="$(cd "$(dirname "$0")/.." && pwd)"
compose=(docker compose -f "$root/deploy/release/compose.yaml")
private="$(mktemp -d)"
trap 'rm -rf -- "$private"' EXIT
policy=daily
metadata="$1"
if [[ "$1" == --monthly ]];then
 policy=monthly
 metadata="$private/monthly-check.json"
 # check reports operational failures as structured health plus nonzero exit.
 # Invalid/empty output (including a busy local lock) fails closed in alerts.
 "$root/scripts/rai-backup.sh" check > "$metadata" || true
fi
"${compose[@]}" run --rm --no-deps monitor > "$private/snapshot.json"
ready=no worker=no tls=no
if "${compose[@]}" exec -T api /rai probe --ready >/dev/null 2>&1;then ready=yes;fi
id="$("${compose[@]}" ps -q worker)"
if [[ -n "$id" && "$(docker inspect -f '{{.State.Running}}' "$id")" == true ]] && "${compose[@]}" exec -T worker /rai worker-ready >/dev/null 2>&1;then worker=yes;fi
if openssl x509 -in "$RAI_SECRETS_DIR/tls_cert" -checkend 604800 -noout >/dev/null 2>&1;then tls=yes;fi
free_kb="$("${compose[@]}" exec -T postgres df -Pk /var/lib/postgresql | awk 'NR==2 {print $4}')"
"${compose[@]}" logs --no-log-prefix --since 5m api > "$private/requests.log" 2>/dev/null
failures="$(python3 - "$private/requests.log" <<'PY'
import json,sys
n=0
for line in open(sys.argv[1]):
 try:r=json.loads(line)
 except ValueError:continue
 if r.get('msg')=='http_request' and r.get('status',0)>=500 and any(x in r.get('operation','') for x in ['/v2/sync/','/v2/routes']):n+=1
print(n)
PY
)"
cat "$private/snapshot.json"
python3 "$root/scripts/p7-alerts.py" "$private/snapshot.json" --backup-metadata "$metadata" --backup-policy "$policy" --disk-free-bytes "$((free_kb*1024))" --ready "$ready" --worker-active "$worker" --tls-valid "$tls" --sync-failures "$failures"
