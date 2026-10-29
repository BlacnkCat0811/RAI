#!/usr/bin/env python3
"""Synthetic calendar and P7 adapter controls; no database or cloud access."""
import datetime as dt
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from monthly_backup_policy import health, schedule, timestamp

ROOT = Path(__file__).resolve().parent


class MonthlyPolicyTests(unittest.TestCase):
    def evaluate(self, now, snapshot, **extra):
        inventory = {"backups": [{"snapshot_started_at": snapshot}] if snapshot else [], "invalid": []}
        return health(inventory, timestamp(now), **extra)["status"]

    def test_every_calendar_month_and_leap_year(self):
        for year in (2024, 2025, 2026, 2027, 2028, 2100):
            for month in range(1, 13):
                slot = dt.datetime(year, month, 1, 3, tzinfo=dt.timezone(dt.timedelta(hours=8)))
                previous = (slot - dt.timedelta(days=1)).replace(day=1)
                with self.subTest(year=year, month=month):
                    for delta in (-1, 0, 86399):
                        now = slot + dt.timedelta(seconds=delta)
                        self.assertEqual(self.evaluate(now.isoformat(), previous.isoformat()), "BACKUP_OK")
                    deadline = slot + dt.timedelta(days=1)
                    self.assertEqual(self.evaluate(deadline.isoformat(), previous.isoformat()), "BACKUP_OVERDUE")
                    self.assertEqual(self.evaluate(deadline.isoformat(), slot.isoformat()), "BACKUP_OK")

    def test_grace_does_not_hide_previous_missed_month(self):
        self.assertEqual(self.evaluate("2026-11-01T03:00:00+08:00", "2026-09-01T03:00:00+08:00"), "BACKUP_OVERDUE")

    def test_end_of_long_month_is_healthy(self):
        self.assertEqual(self.evaluate("2026-12-31T23:59:59+08:00", "2026-12-01T03:00:00+08:00"), "BACKUP_OK")

    def test_utc_and_next_schedule(self):
        self.assertEqual(schedule(timestamp("2026-10-31T18:59:59Z"))["next_scheduled_at"], "2026-11-01T03:00:00+08:00")
        self.assertEqual(schedule(timestamp("2026-12-31T19:00:00Z"))["next_scheduled_at"], "2027-02-01T03:00:00+08:00")
        self.assertEqual(schedule(timestamp("2026-10-29T03:00:00Z"))["next_scheduled_at"], "2026-11-01T03:00:00+08:00")

    def test_missing_corruption_future_and_failed_attempt(self):
        now = timestamp("2026-11-15T12:00:00+08:00")
        self.assertEqual(health({}, now)["status"], "NO_BACKUP")
        self.assertEqual(health({"invalid": ["synthetic.dump"]}, now)["status"], "BACKUP_CORRUPTED")
        self.assertEqual(self.evaluate(now.isoformat(), "2026-12-01T03:00:00+08:00"), "BACKUP_CORRUPTED")
        attempt = {"status": "BACKUP_FAILED", "time": "2026-11-10T00:00:00Z"}
        self.assertEqual(self.evaluate(now.isoformat(), "2026-11-01T03:00:00+08:00", last_attempt=attempt), "BACKUP_FAILED")
        self.assertEqual(self.evaluate(now.isoformat(), "2026-11-11T03:00:00+08:00", last_attempt=attempt), "BACKUP_OK")

    def test_reject_ambiguous_timestamp(self):
        for value in ("2026-11-01T03:00:00", None, 123):
            with self.assertRaises(ValueError):
                timestamp(value)

    def test_p7_monthly_statuses_and_stale_evidence(self):
        now = dt.datetime.now(dt.timezone.utc).isoformat()
        with tempfile.TemporaryDirectory(prefix="rai-monthly-policy-") as directory:
            path = Path(directory)
            (path / "snapshot.json").write_text(json.dumps({"observed_at": now, "expired_leases": 0,
                "jobs": {}, "oldest_due_job_seconds": 0, "generation_lag": 0}))
            command = [sys.executable, str(ROOT / "p7-alerts.py"), str(path / "snapshot.json"),
                "--backup-policy", "monthly", "--backup-metadata", str(path / "backup.json"),
                "--disk-free-bytes", str(100 * 1024**3), "--ready", "yes", "--worker-active", "yes", "--tls-valid", "yes"]
            for status in ("BACKUP_OK", "NO_BACKUP", "BACKUP_OVERDUE", "BACKUP_FAILED", "BACKUP_CORRUPTED", "REMOTE_UNREACHABLE"):
                (path / "backup.json").write_text(json.dumps({"health": {"status": status, "checked_at": now}}))
                result = subprocess.run(command, capture_output=True, text=True)
                self.assertEqual(result.returncode, int(status != "BACKUP_OK"), result.stdout)
                self.assertEqual(json.loads(result.stdout)["alerts"], [] if status == "BACKUP_OK" else [status.lower()])
            for evidence in ({"completed_at": now}, {"health": {"status": "BACKUP_OK", "checked_at": "2000-01-01T00:00:00Z"}}):
                (path / "backup.json").write_text(json.dumps(evidence))
                self.assertEqual(subprocess.run(command, capture_output=True).returncode, 2)


if __name__ == "__main__":
    unittest.main(verbosity=2)
