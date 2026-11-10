#!/usr/bin/env bash
# Explicit logical backup. The caller selects an already-authorized environment.
set -euo pipefail
umask 077
[[ $# == 1 && "$1" == /* ]] || { echo 'usage: p7-backup.sh NEW_ABSOLUTE_BACKUP_DIRECTORY' >&2; exit 2; }
: "${RAI_PROJECT:?}" "${RAI_SECRETS_DIR:?}" "${RAI_BACKUP_KEY_FILE:?}"
[[ -f "$RAI_BACKUP_KEY_FILE" && ! -L "$RAI_BACKUP_KEY_FILE" ]] || exit 2
root="$(cd "$(dirname "$0")/.." && pwd)"
target="$1"
python3 - "$target" "$RAI_BACKUP_KEY_FILE" <<'PY'
import pathlib,sys,os
p,key=map(pathlib.Path,sys.argv[1:]);p.mkdir(mode=0o700,exist_ok=False)
assert key.stat().st_mode&0o077==0 and key.stat().st_size>=32,'backup key must be private and at least 32 bytes'
assert p not in key.parents,'backup key must be stored separately'
PY
compose=(docker compose -f "$root/deploy/release/compose.yaml")
started="$(date -u +%FT%TZ)"
crypto_dir="$(mktemp -d)"
trap 'gpgconf --homedir "$crypto_dir" --kill gpg-agent >/dev/null 2>&1 || true; rm -rf -- "$crypto_dir"; rm -f -- "$target/database.dump.gpg.part" "$target/crypto.log"' EXIT
# Only a read-only backup role connects over SCRAM. No plaintext dump is saved.
if ! "${compose[@]}" exec -T postgres sh -c 'PGPASSWORD="$(cat /run/secrets/pg_backup_password)"; export PGPASSWORD; exec pg_dump -h 127.0.0.1 -U rai_backup_login -d rai_v2 --format=custom --no-owner --no-acl' \
 | gpg --homedir "$crypto_dir" --no-symkey-cache --batch --yes --pinentry-mode loopback --passphrase-file "$RAI_BACKUP_KEY_FILE" --symmetric --cipher-algo AES256 --output "$target/database.dump.gpg.part" 2>"$target/crypto.log"; then
 echo 'backup failed; no success marker written' >&2; exit 1
fi
mv -- "$target/database.dump.gpg.part" "$target/database.dump.gpg"
python3 - "$target" "$started" "${RAI_SOURCE_COMMIT:-UNRECORDED}" <<'PY'
import datetime,hashlib,json,pathlib,sys
p=pathlib.Path(sys.argv[1]);f=p/'database.dump.gpg'
metadata={'format':'RAI-P7-pg_dump-custom-gpg/v1','started_at':sys.argv[2],'completed_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'source_commit':sys.argv[3],'sha256':hashlib.file_digest(f.open('rb'),'sha256').hexdigest(),'bytes':f.stat().st_size,'restore_tested':False,'off_host_verified':False}
(p/'backup.json').write_text(json.dumps(metadata,indent=2)+'\n')
print('P7_BACKUP=PASS encrypted custom dump; restore still required')
PY
