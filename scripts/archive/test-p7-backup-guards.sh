#!/usr/bin/env bash
# Minimal synthetic cryptographic/restore refusal controls, separate empty PGs.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
private="$(mktemp -d)"
project="rai-p7-crypto-$(date +%s)-$$"
image=docker.m.daocloud.io/library/postgres:18.6-bookworm@sha256:bbf83594798e144bff365a9c191c5d779f181fb3135902064ab8ff11918020fc
compose=(docker compose --project-name "$project" -f "$private/compose.yaml")
created=0
cleanup(){ rc=$?;trap - EXIT INT TERM;if [[ "$created" == 1 ]];then "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || rc=1;fi;gpgconf --homedir "$private/gnupg" --kill gpg-agent >/dev/null 2>&1 || true;rm -rf -- "$private";exit "$rc"; }
trap cleanup EXIT
mkdir "$private/gnupg" "$private/backup"
openssl rand -hex 32 > "$private/pg-source"
openssl rand -hex 32 > "$private/pg-target"
openssl rand -hex 32 > "$private/key"
openssl rand -hex 32 > "$private/wrong-key"
cat > "$private/compose.yaml" <<YAML
x-pg: &pg
  image: $image
  environment:
    POSTGRES_USER: rai_bootstrap
    POSTGRES_DB: rai_v2
    POSTGRES_PASSWORD_FILE: /run/secrets/password
    POSTGRES_INITDB_ARGS: --data-checksums --encoding=UTF8
  healthcheck:
    test: [CMD-SHELL, pg_isready -h 127.0.0.1 -U rai_bootstrap -d rai_v2]
    interval: 1s
    timeout: 2s
    retries: 60
  mem_limit: 512m
services:
  source:
    <<: *pg
    volumes: [source-data:/var/lib/postgresql]
    secrets:
      - {source: source-password, target: password}
  target:
    <<: *pg
    labels: {rai.restore-target: 'true'}
    volumes: [target-data:/var/lib/postgresql]
    secrets:
      - {source: target-password, target: password}
volumes: {source-data: {}, target-data: {}}
networks: {default: {internal: true}}
secrets:
  source-password: {file: $private/pg-source}
  target-password: {file: $private/pg-target}
YAML
created=1
"${compose[@]}" up -d --wait source target >/dev/null
"${compose[@]}" exec -T source psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 -c 'CREATE TABLE synthetic_marker(value int PRIMARY KEY); INSERT INTO synthetic_marker VALUES(1)' >/dev/null
"${compose[@]}" exec -T target psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 -c 'CREATE ROLE rai_owner NOSUPERUSER; ALTER DATABASE rai_v2 OWNER TO rai_owner; ALTER SCHEMA public OWNER TO rai_owner' >/dev/null
"${compose[@]}" exec -T source pg_dump -U rai_bootstrap -d rai_v2 -Fc --no-owner --no-acl \
 | gpg --homedir "$private/gnupg" --no-symkey-cache --batch --yes --pinentry-mode loopback --passphrase-file "$private/key" --symmetric --cipher-algo AES256 --output "$private/backup/database.dump.gpg" 2> "$private/crypto.log"
python3 - "$private/backup" <<'PY'
import pathlib,hashlib,json,sys
p=pathlib.Path(sys.argv[1]);(p/'backup.json').write_text(json.dumps({'format':'RAI-P7-pg_dump-custom-gpg/v1','sha256':hashlib.file_digest((p/'database.dump.gpg').open('rb'),'sha256').hexdigest()}))
PY
target="$("${compose[@]}" ps -q target)" source="$("${compose[@]}" ps -q source)"
if bash "$root/scripts/p7-restore.sh" "$private/backup" "$source" "$private/key" > "$private/refused.log" 2>&1;then exit 1;fi
if bash "$root/scripts/p7-restore.sh" "$private/backup" "$target" "$private/wrong-key" > "$private/refused.log" 2>&1;then exit 1;fi
cp -R "$private/backup" "$private/corrupt"
python3 - "$private/corrupt/database.dump.gpg" <<'PY'
import pathlib,sys
p=pathlib.Path(sys.argv[1]);b=bytearray(p.read_bytes());b[len(b)//2]^=1;p.write_bytes(b)
PY
if bash "$root/scripts/p7-restore.sh" "$private/corrupt" "$target" "$private/key" > "$private/refused.log" 2>&1;then exit 1;fi
# A replaced metadata hash must not bypass GPG's authenticated decryption.
python3 - "$private/corrupt" <<'PY'
import pathlib,hashlib,json,sys
p=pathlib.Path(sys.argv[1]);m=json.loads((p/'backup.json').read_text());m['sha256']=hashlib.file_digest((p/'database.dump.gpg').open('rb'),'sha256').hexdigest();(p/'backup.json').write_text(json.dumps(m))
PY
if bash "$root/scripts/p7-restore.sh" "$private/corrupt" "$target" "$private/key" > "$private/refused.log" 2>&1;then exit 1;fi
[[ "$("${compose[@]}" exec -T target psql -X -U rai_bootstrap -d rai_v2 -Atc "SELECT count(*) FROM pg_tables WHERE schemaname='public'")" == 0 ]]
bash "$root/scripts/p7-restore.sh" "$private/backup" "$target" "$private/key"
[[ "$("${compose[@]}" exec -T target psql -X -U rai_bootstrap -d rai_v2 -Atc 'SELECT count(*) FROM synthetic_marker')" == 1 ]]
if bash "$root/scripts/p7-restore.sh" "$private/backup" "$target" "$private/key" > "$private/refused.log" 2>&1;then exit 1;fi
echo 'P7_BACKUP_GUARDS=PASS wrong key, ciphertext corruption (including replaced SHA metadata), unlabelled/nonempty target refused; failures left target empty; authenticated valid restore succeeded'
