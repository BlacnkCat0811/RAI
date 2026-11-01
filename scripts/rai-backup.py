#!/usr/bin/env python3
"""Monthly full PostgreSQL custom dump, validation, SSH publication, keep three."""
import argparse
import base64
import contextlib
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat
import subprocess
import sys
import tempfile
import time
import uuid
from zoneinfo import ZoneInfo
from monthly_backup_policy import ZONE, health

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import importlib.util
spec = importlib.util.spec_from_file_location("monthly_remote", ROOT / "scripts/rai-backup-remote.py")
remote = importlib.util.module_from_spec(spec)
spec.loader.exec_module(remote)


def required(name):
    value = os.environ.get(name, "")
    remote.require(bool(value), "missing_" + name)
    return value


def private_file(path):
    p = Path(path)
    remote.require(p.is_absolute(), "private_file_requires_absolute_path")
    info = p.lstat()
    remote.require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid()
                   and info.st_nlink == 1 and info.st_mode & 0o077 == 0, "unsafe_private_file")
    return p


def ssh_command(config):
    host, user = required("RAI_BACKUP_SSH_HOST"), required("RAI_BACKUP_SSH_USER")
    remote.require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]*", host) is not None
                   and re.fullmatch(r"[a-zA-Z_][a-zA-Z0-9_-]*", user) is not None, "invalid_ssh_address")
    port = int(os.environ.get("RAI_BACKUP_SSH_PORT", "22"))
    remote.require(1 <= port <= 65535, "invalid_ssh_port")
    key = private_file(required("RAI_BACKUP_SSH_KEY"))
    command = ["ssh", "-T", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes",
               "-o", "StrictHostKeyChecking=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes",
               "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
               "-p", str(port), "-i", str(key)]
    known = os.environ.get("RAI_BACKUP_KNOWN_HOSTS")
    if known:
        remote.require(Path(known).is_absolute() and Path(known).is_file(), "invalid_known_hosts")
        command += ["-o", "UserKnownHostsFile=" + known]
    encoded = base64.b64encode(json.dumps(config).encode()).decode()
    program = (ROOT / "scripts/rai-backup-remote.py").read_text()
    command += [user + "@" + host, shlex.join(["python3", "-c", program, encoded])]
    return command


def run(command, phase="external_command", accepted=(0,), **kwargs):
    try:
        result = subprocess.run(command, stderr=subprocess.DEVNULL,
                                timeout=int(os.environ.get("RAI_BACKUP_TIMEOUT_SECONDS", "3600")), **kwargs)
        if result.returncode not in accepted:
            raise subprocess.CalledProcessError(result.returncode, command)
        return result
    except subprocess.TimeoutExpired:
        raise ValueError(phase + "_operation_timeout") from None
    except subprocess.CalledProcessError as error:
        raise ValueError(phase + "_failed_exit_" + str(error.returncode)) from None


def error_code(error):
    message = str(error) if isinstance(error, ValueError) else type(error).__name__
    return message if re.fullmatch(r"[A-Za-z0-9_]{1,100}", message) else "invalid_backup_evidence"


def save_attempt(fd, audit):
    audit["completed_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    audit["duration_seconds"] = round((datetime.datetime.fromisoformat(audit["completed_at"]) -
                                       datetime.datetime.fromisoformat(audit["time"])).total_seconds(), 3)
    name = "attempt-" + uuid.uuid4().hex + ".partial"
    try:
        remote.write_new(fd, name, (json.dumps(audit, sort_keys=True) + "\n").encode())
        os.rename(name, "last-backup-attempt.json", src_dir_fd=fd, dst_dir_fd=fd)
        os.fsync(fd)
    finally:
        try:
            os.unlink(name, dir_fd=fd)
        except FileNotFoundError:
            pass


def execute(action, audit):
    timezone = required("RAI_BACKUP_TIMEZONE")
    remote.require(timezone == ZONE, "schedule_requires_Asia_Shanghai")
    ZoneInfo(timezone)
    config = {"action": action, "directory": required("RAI_BACKUP_REMOTE_DIR")}
    ssh_command(config)  # Validate connection configuration before any dump.
    state_dir = Path(required("RAI_BACKUP_STATE_DIR"))
    # Same no-follow directory/lock rules locally and remotely.
    fd = remote.directory(str(state_dir), create=True)
    try:
        with remote.regular(fd, "run.lock", os.O_RDWR | os.O_CREAT) as lock:
            try:
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                print("MONTHLY_BACKUP_SKIPPED=already_running", file=sys.stderr)
                return 75
            try:
                with tempfile.TemporaryDirectory(prefix="rai-monthly-", dir=state_dir) as scratch:
                    dump = Path(scratch) / "database.dump"
                    started = datetime.datetime.now(datetime.timezone.utc)
                    if action == "backup":
                        required("RAI_PROJECT")
                        required("RAI_SECRETS_DIR")
                        compose = ["docker", "compose", "-f", str(ROOT / "deploy/release/compose.yaml")]
                        version = run(compose + ["exec", "-T", "postgres", "sh", "-c",
                            'PGPASSWORD="$(cat /run/secrets/pg_backup_password)"; export PGPASSWORD; '
                            'exec psql -X -h 127.0.0.1 -U rai_backup_login -d rai_v2 -Atc '
                            '"SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1"'], phase="source_schema", stdout=subprocess.PIPE).stdout.strip()
                        remote.require(version in (b"13", b"14", b"15", b"16", b"17", b"18", b"19"), "unsupported_source_schema")
                        pg_version = run(compose + ["exec", "-T", "postgres", "sh", "-c",
                            'PGPASSWORD="$(cat /run/secrets/pg_backup_password)"; export PGPASSWORD; '
                            'exec psql -X -h 127.0.0.1 -U rai_backup_login -d rai_v2 -Atc '
                            '"SHOW server_version_num"'],
                            phase="source_postgres", stdout=subprocess.PIPE).stdout.strip()
                        remote.require(pg_version.isdigit() and int(pg_version) // 10000 == 18,
                                       "unsupported_source_postgres")
                        # Reuses P7's read-only role and secret mount; no DSN or
                        # password in argv/logs, and no schema/table filters.
                        command = compose + ["exec", "-T", "postgres", "sh", "-c",
                            'PGPASSWORD="$(cat /run/secrets/pg_backup_password)"; export PGPASSWORD; '
                            'exec pg_dump -h 127.0.0.1 -U rai_backup_login -d rai_v2 -Fc --no-owner --no-acl --lock-wait-timeout=10s']
                        with dump.open("xb") as file:
                            run(command, phase="pg_dump", stdout=file)
                            file.flush()
                            os.fsync(file.fileno())
                        remote.require(dump.stat().st_size > 0, "empty_dump")
                        with dump.open("rb") as file:
                            run(compose + ["exec", "-T", "postgres", "pg_restore", "--list"],
                                stdin=file, stdout=subprocess.DEVNULL, phase="archive_validation")
                        with dump.open("rb") as file:
                            size, sha = remote.digest(file)
                        name = "rai-" + started.strftime("%Y-%m-%d-%H%M%S-") + uuid.uuid4().hex + ".dump"
                        audit.update(backup_filename=name, backup_size=size, sha256=sha,
                                     postgres_version_num=int(pg_version), schema_version=int(version))
                        config["metadata"] = {"format": remote.FORMAT, "file": name, "sha256": sha, "bytes": size,
                            "snapshot_started_at": started.isoformat(), "schedule_timezone": timezone,
                            "source_commit": os.environ.get("RAI_SOURCE_COMMIT", "UNRECORDED"),
                            "source_database": "rai_v2", "schema_version": int(version), "postgres_major": 18,
                            "postgres_version_num": int(pg_version),
                            "archive_list_validated": True, "restore_tested": False}
                    with contextlib.ExitStack() as stack:
                        source = stack.enter_context(dump.open("rb")) if action == "backup" else subprocess.DEVNULL
                        response = run(ssh_command(config), stdin=source, stdout=subprocess.PIPE, phase="ssh_transfer",
                                       accepted=(0,) if action == "backup" else (0, 1))
                    result = json.loads(response.stdout)
                    if action == "check":
                        try:
                            with remote.regular(fd, "last-backup-attempt.json") as file:
                                attempt = json.load(file)
                        except FileNotFoundError:
                            attempt = None
                        checked = datetime.datetime.now(datetime.timezone.utc)
                        try:
                            result["health"] = health(result, checked, attempt)
                        except (KeyError, TypeError, ValueError):
                            result["health"] = {"status": "BACKUP_CORRUPTED", "checked_at": checked.isoformat()}
                        audit["status"] = result["health"]["status"]
                    else:
                        remote.require(result.get("status") == "success", "remote_check_failed")
                        audit["status"] = "BACKUP_OK"
                    # Contains controlled filenames, sizes/digests and timestamps,
                    # never raw facts, credentials or machine addresses.
                    print(json.dumps(result, sort_keys=True))
                    if action == "backup":
                        audit["completed_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
                        save_attempt(fd, audit)
                    return 0 if audit["status"] == "BACKUP_OK" else 1
            except BaseException as error:
                if action == "backup":
                    audit.update(status="BACKUP_FAILED", error_code=error_code(error))
                    save_attempt(fd, audit)
                raise
    finally:
        os.close(fd)


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("backup", "check", "list"))
    args = parser.parse_args()
    audit = {"time": datetime.datetime.now(datetime.timezone.utc).isoformat(),
             "operation": args.action, "status": "BACKUP_FAILED", "duration_seconds": 0,
             "backup_filename": None, "backup_size": None, "error_code": None}
    start = time.monotonic()
    try:
        code = execute(args.action, audit)
        if code == 75:
            audit.update(status="SKIPPED", error_code="already_running")
        return code
    except (Exception, KeyboardInterrupt) as error:
        audit["error_code"] = error_code(error)
        if args.action == "check":
            audit["status"] = "REMOTE_UNREACHABLE" if audit["error_code"].startswith("ssh_transfer_") else "BACKUP_FAILED"
            print(json.dumps({"status": "check_failed", "health": {
                "status": audit["status"], "checked_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}}))
        print("MONTHLY_BACKUP_FAILED=" + audit["error_code"], file=sys.stderr)
        return 1
    finally:
        audit["duration_seconds"] = round(time.monotonic() - start, 3)
        audit["completed_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat()
        print(json.dumps(audit, sort_keys=True), file=sys.stderr)


if __name__ == "__main__":
    sys.exit(main())
