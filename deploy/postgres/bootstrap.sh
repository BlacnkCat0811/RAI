#!/bin/sh
# Explicit first initialization of a NEW, dedicated database/cluster only.
# Called inside the release PostgreSQL container, never by its entrypoint.
set -eu
export OWNER_PASSWORD="$(cat /run/secrets/pg_owner_password)"
export API_PASSWORD="$(cat /run/secrets/pg_api_password)"
export WORKER_PASSWORD="$(cat /run/secrets/pg_worker_password)"
export BACKUP_PASSWORD="$(cat /run/secrets/pg_backup_password)"
export MONITOR_PASSWORD="$(cat /run/secrets/pg_monitor_password)"
psql -X -v ON_ERROR_STOP=1 -U rai_bootstrap -d rai_v2 <<'SQL'
\getenv owner_password OWNER_PASSWORD
\getenv api_password API_PASSWORD
\getenv worker_password WORKER_PASSWORD
\getenv backup_password BACKUP_PASSWORD
\getenv monitor_password MONITOR_PASSWORD
BEGIN;
CREATE ROLE rai_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD :'owner_password';
CREATE ROLE rai_api_login LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 10 PASSWORD :'api_password';
CREATE ROLE rai_worker_login LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 6 PASSWORD :'worker_password';
CREATE ROLE rai_backup_login LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2 PASSWORD :'backup_password';
CREATE ROLE rai_monitor_login LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 2 PASSWORD :'monitor_password';
ALTER DATABASE rai_v2 OWNER TO rai_owner;
ALTER SCHEMA public OWNER TO rai_owner;
REVOKE ALL ON DATABASE rai_v2 FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE rai_v2 TO rai_owner,rai_api_login,rai_worker_login,rai_backup_login,rai_monitor_login;
COMMIT;
SQL
