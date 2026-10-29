#!/usr/bin/env python3
"""Synthetic operational alert rule controls; no service or notification."""
import datetime
import json
import pathlib
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parent
now = datetime.datetime.now(datetime.timezone.utc).isoformat()
healthy = {'observed_at': now, 'expired_leases': 0, 'jobs': {}, 'oldest_due_job_seconds': 0, 'generation_lag': 0}
with tempfile.TemporaryDirectory(prefix='rai-p7-alert-') as directory:
    p = pathlib.Path(directory)
    base = ['python3', str(root / 'p7-alerts.py'), str(p / 'snapshot.json'), '--backup-metadata', str(p / 'backup.json'), '--disk-free-bytes', str(100 * 1024**3), '--ready', 'yes', '--worker-active', 'yes', '--tls-valid', 'yes']

    def evaluate(changes=None, backup=None, arguments=None, extra=None):
        (p / 'snapshot.json').write_text(json.dumps(dict(healthy, **(changes or {}))))
        (p / 'backup.json').write_text(json.dumps({'completed_at': now, **(backup or {})}))
        command = base.copy()
        for key, value in (arguments or {}).items():
            command[command.index(key) + 1] = value
        result = subprocess.run(command + (extra or []), capture_output=True, text=True)
        return result.returncode, set(json.loads(result.stdout)['alerts'])

    assert evaluate() == (0, set())
    # Preserve raw failed history after a newer clean publication resolves it.
    assert evaluate({'jobs': {'failed': 1}, 'historical_failed_jobs': 1, 'failed_jobs_needing_action': 0}) == (0, set())
    cases = [
        ({'observed_at': '2000-01-01T00:00:00Z'}, None, None, None, {'snapshot_stale'}),
        (None, None, {'--ready': 'no'}, None, {'api_unready'}),
        (None, None, {'--worker-active': 'no'}, None, {'worker_stopped'}),
        (None, None, {'--tls-valid': 'no'}, None, {'tls_expiring_or_invalid'}),
        (None, {'completed_at': '2000-01-01T00:00:00Z'}, None, None, {'backup_stale'}),
        (None, None, {'--disk-free-bytes': '1'}, None, {'disk_low'}),
        ({'expired_leases': 1}, None, None, None, {'leases_expired'}),
        ({'jobs': {'failed': 1}}, None, None, None, {'jobs_failed'}),
        ({'oldest_due_job_seconds': 600}, None, None, None, {'backlog_stale'}),
        ({'oldest_due_job_seconds': 600, 'generation_lag': 3}, None, None, None, {'backlog_stale', 'analysis_generation_lag'}),
        (None, None, None, ['--sync-failures', '5'], {'sync_repeated_failure'}),
    ]
    for changes, backup, arguments, extra, expected in cases:
        assert evaluate(changes, backup, arguments, extra) == (1, expected), expected
    assert evaluate({'jobs': {'repair_required': 1}, 'failed_jobs_needing_action': 1}) == (1, {'jobs_failed'})
    (p / 'backup.json').unlink()
    assert subprocess.run(base, capture_output=True).returncode == 2
print('P7_ALERT_RULES=PASS healthy, each of eleven conditions, historical resolution, repair_required, missing evidence fail closed')
