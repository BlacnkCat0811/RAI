#!/usr/bin/env python3
"""Restore a monthly archive into a labelled, empty, quiet isolated PG18 only."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

spec = importlib.util.spec_from_file_location("monthly", Path(__file__).with_name("rai-backup.py"))
monthly = importlib.util.module_from_spec(spec)
spec.loader.exec_module(monthly)
require = monthly.remote.require
run = monthly.run


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("isolated_container")
    parser.add_argument("--confirm-target", required=True, help="Full inspected container ID")
    args = parser.parse_args()
    source = monthly.private_file(str(args.archive))
    require(monthly.remote.NAME.fullmatch(source.name), "invalid_archive_name")
    manifest = monthly.private_file(str(source) + ".json")
    checksum = monthly.private_file(str(source) + ".sha256")
    m = json.loads(manifest.read_bytes())
    require(m["format"] == monthly.remote.FORMAT and m["file"] == source.name
            and m["schema_version"] in (13, 14, 15, 16, 17, 18, 19, 20) and m["postgres_major"] == 18
            and m["source_database"] == "rai_v2", "unexpected_backup_source_or_schema")
    require(checksum.read_text() == m["sha256"] + "  " + source.name + "\n", "invalid_sha_file")
    # Keep the verified file descriptor through restore; no path swap afterwards.
    with source.open("rb") as archive:
        require(monthly.remote.digest(archive) == (m["bytes"], m["sha256"]), "archive_integrity_mismatch")
        info = json.loads(run(["docker", "inspect", args.isolated_container], stdout=subprocess.PIPE).stdout)[0]
        target = info["Id"]
        labels = info["Config"].get("Labels") or {}
        require(labels.get("rai.restore-target") == "true" and args.confirm_target == target
                and info["State"]["Running"], "unconfirmed_or_unlabelled_target")
        project = labels.get("com.docker.compose.project")
        require(bool(project), "target_requires_isolated_compose_project")
        for service in ("api", "worker"):
            running = run(["docker", "ps", "-q", "--filter", "label=com.docker.compose.project=" + project,
                           "--filter", "label=com.docker.compose.service=" + service], stdout=subprocess.PIPE).stdout
            require(not running.strip(), "target_api_or_worker_running")
        command = ["docker", "exec", "-i", target]
        psql = command + ["psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "rai_bootstrap", "-d", "rai_v2", "-Atc"]
        actual = run(psql + ["SELECT current_database(),pg_get_userbyid(datdba),current_setting('server_version_num')::int/10000 "
                            "FROM pg_database WHERE datname=current_database()"], stdout=subprocess.PIPE).stdout.strip()
        require(actual == b"rai_v2|rai_owner|18", "unexpected_database_identity")
        quiet = run(psql + ["SELECT (SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend'),"
                           "(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'),"
                           "(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public')"], stdout=subprocess.PIPE).stdout.strip()
        require(quiet == b"0|0|0", "target_not_empty_or_has_connections")
        archive.seek(0)
        run(command + ["pg_restore", "--list"], stdin=archive, stdout=subprocess.DEVNULL)
        archive.seek(0)
        run(command + ["pg_restore", "-U", "rai_bootstrap", "-d", "rai_v2", "--role=rai_owner",
                       "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl"], stdin=archive, stdout=subprocess.DEVNULL)
    run(psql + ["ALTER DATABASE rai_v2 SET default_transaction_read_only=on"])
    print("MONTHLY_RESTORE_SQL=PASS; reapply grants and verify inventory/FK/receipts/publications/Effective/execution; QUARANTINE_ARCHIVE_ONLY; no API/Worker exposure; active recovery requires a new dataset/session")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("MONTHLY_RESTORE_FAILED=" + (str(error) if isinstance(error, ValueError) else type(error).__name__), file=sys.stderr)
        sys.exit(1)
