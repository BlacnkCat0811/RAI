#!/usr/bin/env bash
# Restores ONLY into an explicitly labelled, empty isolated target. No --clean.
set -euo pipefail
umask 077
[[ $# == 3 ]] || { echo 'usage: p7-restore.sh BACKUP_DIRECTORY ISOLATED_CONTAINER SEPARATE_KEY_FILE' >&2; exit 2; }
backup="$1" target="$2" key="$3"
[[ "$(docker inspect -f '{{index .Config.Labels "rai.restore-target"}}' "$target")" == true ]] || { echo 'target is not an isolated restore container' >&2; exit 2; }
[[ "$(docker exec "$target" psql -X -U rai_bootstrap -d rai_v2 -Atc "SELECT count(*) FROM pg_tables WHERE schemaname='public'")" == 0 ]] || { echo 'restore target is not empty' >&2; exit 2; }
private="$(mktemp -d)"
mkdir "$private/gnupg"
trap 'gpgconf --homedir "$private/gnupg" --kill gpg-agent >/dev/null 2>&1 || true; rm -rf -- "$private"' EXIT
python3 - "$backup" <<'PY'
import hashlib,json,pathlib,sys
p=pathlib.Path(sys.argv[1]);m=json.loads((p/'backup.json').read_text());f=p/'database.dump.gpg'
assert m['format']=='RAI-P7-pg_dump-custom-gpg/v1'
assert hashlib.file_digest(f.open('rb'),'sha256').hexdigest()==m['sha256'],'encrypted backup integrity mismatch'
PY
# Complete authenticated decryption BEFORE any database statement is executed.
if ! gpg --homedir "$private/gnupg" --no-symkey-cache --batch --yes --pinentry-mode loopback --passphrase-file "$key" --output "$private/database.dump" --decrypt "$backup/database.dump.gpg" 2>"$private/crypto.log"; then
 echo 'backup decryption or authentication failed' >&2; exit 1
fi
docker exec -i "$target" pg_restore -U rai_bootstrap -d rai_v2 --role=rai_owner --single-transaction --exit-on-error --no-owner --no-acl < "$private/database.dump"
echo 'P7_RESTORE_SQL=PASS; reapply reviewed runtime grants, verify invariants, readiness and original identities before acceptance'
