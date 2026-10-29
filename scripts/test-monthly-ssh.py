#!/usr/bin/env python3
"""Real loopback OpenSSH + real source pg_dump, never the user's cloud server.

Called only by the synthetic Compose harness. Host sshd runs unprivileged with
fresh keys/config/authorized_keys and listens only on a random loopback port.
"""
import hashlib
import json
import os
from pathlib import Path
import pwd
import socket
import subprocess
import sys
import time
import tempfile
import importlib.util

ROOT = Path(__file__).resolve().parents[1]


def main():
    assert os.environ["RAI_PROJECT"].startswith("rai-p7-test-")
    os.umask(0o077)
    work = Path(sys.argv[1]).resolve()
    work.mkdir(mode=0o700)
    for name in ("host-key", "client-key"):
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(work / name)], check=True)
    authorized = work / "authorized_keys"
    authorized.write_bytes((work / "client-key.pub").read_bytes())
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
    user = pwd.getpwuid(os.getuid()).pw_name
    (work / "known_hosts").write_text("[127.0.0.1]:" + str(port) + " " + (work / "host-key.pub").read_text())
    config = work / "sshd_config"
    config.write_text(f"""Port {port}
ListenAddress 127.0.0.1
HostKey {work / 'host-key'}
PidFile {work / 'sshd.pid'}
AuthorizedKeysFile {authorized}
AllowUsers {user}
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
UsePAM no
StrictModes yes
AllowAgentForwarding no
AllowTcpForwarding no
X11Forwarding no
PermitTunnel no
LogLevel ERROR
""")
    env = dict(os.environ, RAI_BACKUP_SSH_HOST="127.0.0.1", RAI_BACKUP_SSH_PORT=str(port),
        RAI_BACKUP_SSH_USER=user, RAI_BACKUP_SSH_KEY=str(work / "client-key"),
        RAI_BACKUP_KNOWN_HOSTS=str(work / "known_hosts"), RAI_BACKUP_REMOTE_DIR=str(work / "backups"),
        RAI_BACKUP_STATE_DIR=str(work / "state"), RAI_BACKUP_TIMEZONE="Asia/Shanghai")
    # sshd StrictModes checks ancestor modes too. /tmp is intentionally shared,
    # so store authorized_keys under a fresh private home directory, never turn
    # StrictModes off to make this test pass.
    auth_dir = tempfile.TemporaryDirectory(prefix="rai-monthly-auth-", dir=Path.home())
    authorization = Path(auth_dir.name) / "authorized_keys"
    authorization.write_bytes(authorized.read_bytes())
    config.write_text(config.read_text().replace(str(authorized), str(authorization)))
    def call(action, extra=None, expect=0):
        p = subprocess.run([str(ROOT / "scripts/rai-backup.sh"), action], env=env | (extra or {}),
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        audit = json.loads(p.stderr.decode().splitlines()[-1])
        assert all(k in audit for k in ("time", "operation", "status", "duration_seconds", "backup_filename", "backup_size", "error_code"))
        if expect == 0:
            assert p.returncode == 0, p.stderr.decode()
            return json.loads(p.stdout)
        assert p.returncode != 0
    with (work / "sshd.log").open("wb") as log:
        daemon = subprocess.Popen(["/usr/sbin/sshd", "-D", "-e", "-f", str(config)], stdout=log, stderr=log)
        try:
            for _ in range(100):
                assert daemon.poll() is None, (work / "sshd.log").read_text()
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=.2):
                        break
                except OSError:
                    time.sleep(.05)
            spec = importlib.util.spec_from_file_location("monthly", ROOT / "scripts/rai-backup.py")
            monthly = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(monthly)
            previous_env = os.environ.copy()
            try:
                os.environ.update(env)
                preflight = monthly.ssh_command({"action": "list", "directory": str(work / "backups")})
            finally:
                os.environ.clear()
                os.environ.update(previous_env)
            preflight[-1] = "true"
            connection = subprocess.run(preflight, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            assert connection.returncode == 0, connection.stderr.decode() + (work / "sshd.log").read_text()
            if os.environ.get("RAI_MONTHLY_SSH_PREFLIGHT_ONLY") == "1":
                print("MONTHLY_SSH_PREFLIGHT=PASS")
                return
            first = None
            for index in range(4):
                result = call("backup")
                if first is None:
                    first = result["backup"]["file"]
                assert len(result["removed"]) == int(index == 3)
            result = call("check")
            assert len(result["backups"]) == 3
            assert not (work / "backups" / first).exists()
            listed = call("list")
            assert listed["backups"] == result["backups"] and listed["invalid"] == result["invalid"]
            assert result["health"]["status"] == "BACKUP_OK"
            last = result["backups"][-1]
            archive = work / "backups" / last["file"]
            assert hashlib.sha256(archive.read_bytes()).hexdigest() == last["sha256"]
            before = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in archive.parent.iterdir()}
            call("backup", {"RAI_BACKUP_SSH_PORT": "1"}, expect=1)
            # A real TCP peer accepts but never sends an SSH banner; timeout
            # terminates the client. The real remote backup set stays intact.
            with socket.socket() as blackhole:
                blackhole.bind(("127.0.0.1", 0))
                blackhole.listen(1)
                call("backup", {"RAI_BACKUP_SSH_PORT": str(blackhole.getsockname()[1]), "RAI_BACKUP_TIMEOUT_SECONDS": "1"}, expect=1)
            after = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in archive.parent.iterdir()}
            assert before == after
            failed_check = subprocess.run([str(ROOT / "scripts/rai-backup.sh"), "check"], env=env,
                                          capture_output=True, text=True)
            assert failed_check.returncode == 1
            assert json.loads(failed_check.stdout)["health"]["status"] == "BACKUP_FAILED"
            assert (work / "state/last-backup-attempt.json").stat().st_mode & 0o777 == 0o600
            # A successful manual retry clears the local failure indication.
            recovered = call("backup")
            assert call("check")["health"]["status"] == "BACKUP_OK"
            last = recovered["backup"]
            archive = work / "backups" / last["file"]
            monitor = subprocess.run([str(ROOT / "scripts/p7-monitor.sh"), "--monthly"], env=env,
                                     capture_output=True, text=True)
            assert monitor.returncode == 1, monitor.stderr  # Source Worker is deliberately stopped.
            alerts = json.loads(monitor.stdout.splitlines()[-1])["alerts"]
            assert "worker_stopped" in alerts and not any("backup" in code or "remote" in code for code in alerts), alerts
            (work / "selected-archive").write_text(str(archive))
            print("MONTHLY_REAL_SSH=" + json.dumps({"status": "PASS", "successes": 5, "retained": 3,
                "oldest_removed": True, "strict_host_key": True, "ssh_failure_preserved": True,
                "network_timeout_preserved": True, "manual_retry_clears_failure": True,
                "p7_monthly_monitor_preserves_worker_alert": True,
                "archive_bytes": last["bytes"], "sha256": last["sha256"]}))
        finally:
            daemon.terminate()
            daemon.wait(timeout=10)
            auth_dir.cleanup()


if __name__ == "__main__":
    main()
