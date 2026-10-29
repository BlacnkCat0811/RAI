#!/usr/bin/env python3
"""Synthetic file/transport fault controls. No real database or cloud input."""
import base64
import errno
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
import uuid

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("remote", ROOT / "scripts/rai-backup-remote.py")
remote = importlib.util.module_from_spec(spec)
spec.loader.exec_module(remote)


class BackupTests(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory(prefix="rai-monthly-synthetic-")
        self.addCleanup(self.work.cleanup)
        self.path = Path(self.work.name).resolve() / "remote"

    def metadata(self, payload=b"synthetic archive"):
        return {"format": remote.FORMAT, "file": "rai-2026-11-01-030000-" + uuid.uuid4().hex + ".dump",
                "bytes": len(payload), "sha256": hashlib.sha256(payload).hexdigest()}

    def call(self, action, payload=b"synthetic archive", metadata=None, path=None):
        c = {"action": action, "directory": str(path or self.path)}
        if action == "backup":
            c["metadata"] = metadata or self.metadata(payload)
        encoded = base64.b64encode(json.dumps(c).encode()).decode()
        p = subprocess.run([sys.executable, str(ROOT / "scripts/rai-backup-remote.py"), encoded],
                           input=payload, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return p, json.loads(p.stdout) if p.returncode == 0 else None

    def seed(self):
        for _ in range(3):
            p, _ = self.call("backup")
            self.assertEqual(p.returncode, 0, p.stderr)
        return self.snapshot()

    def snapshot(self):
        return {p.name: p.read_bytes() for p in self.path.iterdir() if p.is_file()}

    def test_four_successes_keep_latest_three_and_permissions(self):
        first = None
        for n in range(4):
            p, data = self.call("backup")
            self.assertEqual(p.returncode, 0, p.stderr)
            if n == 0:
                first = data["backup"]["file"]
            self.assertEqual(len(data["removed"]), int(n == 3))
        p, data = self.call("check")
        self.assertEqual(p.returncode, 0)
        self.assertEqual(len(data["backups"]), 3)
        self.assertFalse((self.path / first).exists())
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o700)
        for file in self.path.iterdir():
            self.assertEqual(file.stat().st_mode & 0o777, 0o600)

    def test_checksum_mismatch_preserves_all_old_backups(self):
        before = self.seed()
        m = self.metadata()
        m["sha256"] = "0" * 64
        p, _ = self.call("backup", metadata=m)
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.snapshot(), before)

    def test_interrupted_upload_preserves_all_old_backups(self):
        before = self.seed()
        p, _ = self.call("backup", payload=b"short", metadata=self.metadata())
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.snapshot(), before)

    def test_killed_upload_has_no_success_marker_or_retention(self):
        before = self.seed()
        m = self.metadata()
        c = {"action": "backup", "directory": str(self.path), "metadata": m}
        encoded = base64.b64encode(json.dumps(c).encode()).decode()
        p = subprocess.Popen([sys.executable, str(ROOT / "scripts/rai-backup-remote.py"), encoded],
                             stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            for _ in range(100):
                if (self.path / (m["file"] + ".partial")).exists():
                    break
                time.sleep(.01)
            self.assertTrue((self.path / (m["file"] + ".partial")).exists())
        finally:
            p.kill()
            p.wait(timeout=5)
            p.stdin.close()
        self.assertFalse((self.path / (m["file"] + ".json")).exists())
        for name, content in before.items():
            self.assertEqual((self.path / name).read_bytes(), content)
        _, data = self.call("check")
        self.assertEqual(len(data["backups"]), 3)

    def test_enospc_preserves_all_old_backups(self):
        before = self.seed()
        fd = remote.directory(str(self.path), False)
        try:
            with mock.patch.object(remote.sys, "stdin", io.TextIOWrapper(io.BytesIO(b"synthetic archive"))), \
                 mock.patch.object(remote.os, "fsync", side_effect=OSError(errno.ENOSPC, "synthetic disk full")):
                with self.assertRaises(OSError):
                    remote.upload(fd, self.metadata())
        finally:
            os.close(fd)
        self.assertEqual(self.snapshot(), before)

    def test_partial_and_foreign_files_never_enter_retention(self):
        self.seed()
        files = {"other-project.dump": b"foreign", self.metadata()["file"] + ".partial": b"incomplete"}
        for name, content in files.items():
            (self.path / name).write_bytes(content)
        for _ in range(3):
            self.assertEqual(self.call("backup")[0].returncode, 0)
        for name, content in files.items():
            self.assertEqual((self.path / name).read_bytes(), content)

    def test_symlink_directory_and_ancestor_refused(self):
        self.seed()
        alias = Path(self.work.name).resolve() / "link"
        alias.symlink_to(self.path, target_is_directory=True)
        before = self.snapshot()
        self.assertNotEqual(self.call("backup", path=alias)[0].returncode, 0)
        self.assertNotEqual(self.call("backup", path=alias / "child")[0].returncode, 0)
        self.assertFalse((self.path / "child").exists())
        self.assertEqual(self.snapshot(), before)

    def test_symlink_managed_backup_not_followed_or_deleted(self):
        self.seed()
        _, entries = self.call("list")
        name = entries["backups"][0]["file"]
        external = Path(self.work.name) / "foreign"
        external.write_bytes((self.path / name).read_bytes())
        (self.path / name).unlink()
        (self.path / name).symlink_to(external)
        self.assertNotEqual(self.call("check")[0].returncode, 0)
        self.assertEqual(self.call("backup")[0].returncode, 0)
        self.assertTrue((self.path / name).is_symlink())
        self.assertTrue(external.exists())

    def test_existing_nonempty_directory_not_adopted(self):
        self.path.mkdir(mode=0o700)
        (self.path / "foreign").write_text("keep")
        self.assertNotEqual(self.call("backup")[0].returncode, 0)
        self.assertEqual((self.path / "foreign").read_text(), "keep")

    def test_remote_lock_refuses_concurrent_publication(self):
        before = self.seed()
        import fcntl
        with (self.path / ".rai-monthly.lock").open("rb") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assertNotEqual(self.call("backup")[0].returncode, 0)
        self.assertEqual(self.snapshot(), before)

    def test_duplicate_identity_never_overwrites(self):
        m = self.metadata()
        self.assertEqual(self.call("backup", metadata=m)[0].returncode, 0)
        before = self.snapshot()
        self.assertNotEqual(self.call("backup", metadata=m)[0].returncode, 0)
        self.assertEqual(self.snapshot(), before)

    def test_local_dump_ssh_timeout_and_mutex_failures(self):
        before = self.seed()
        private = Path(self.work.name).resolve()
        binary = private / "bin"
        binary.mkdir()
        key = private / "key"
        key.write_text("synthetic private file")
        key.chmod(0o600)
        state = private / "state"
        state.mkdir(mode=0o700)
        env = dict(os.environ, RAI_BACKUP_SSH_HOST="127.0.0.1", RAI_BACKUP_SSH_USER="synthetic",
                   RAI_BACKUP_SSH_KEY=str(key), RAI_BACKUP_REMOTE_DIR=str(self.path),
                   RAI_BACKUP_TIMEZONE="Asia/Shanghai", RAI_BACKUP_STATE_DIR=str(state),
                   RAI_BACKUP_TIMEOUT_SECONDS="5", RAI_PROJECT="synthetic", RAI_SECRETS_DIR=str(private),
                   PATH=str(binary) + os.pathsep + os.environ["PATH"])
        def executable(name, body):
            p = binary / name
            p.write_text("#!/bin/sh\n" + body + "\n")
            p.chmod(0o700)
        def invoke(action="backup"):
            return subprocess.run([sys.executable, str(ROOT / "scripts/rai-backup.py"), action],
                                  env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        executable("docker", 'case "$*" in *server_version_num*) echo 180006;; *psql*) echo 13;; *pg_dump*) exit 42;; esac')
        executable("ssh", "exit 99")
        self.assertIn(b"pg_dump_failed_exit_42", invoke().stderr)
        executable("docker", 'case "$*" in *server_version_num*) echo 180006;; *psql*) echo 13;; *pg_dump*) echo synthetic;; esac')
        self.assertNotEqual(invoke().returncode, 0)  # SSH failure
        executable("ssh", "exec sleep 10")
        env["RAI_BACKUP_TIMEOUT_SECONDS"] = "1"
        p = invoke()
        self.assertIn(b"operation_timeout", p.stderr)
        import fcntl
        with (state / "run.lock").open("rb") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assertEqual(invoke().returncode, 75)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(list(state.glob("rai-monthly-*")), [])
        attempt = json.loads((state / "last-backup-attempt.json").read_text())
        self.assertEqual(attempt["status"], "BACKUP_FAILED")
        self.assertEqual(attempt["error_code"], "ssh_transfer_operation_timeout")
        self.assertGreaterEqual(attempt["duration_seconds"], 1)


if __name__ == "__main__":
    os.umask(0o077)
    unittest.main(verbosity=2)
