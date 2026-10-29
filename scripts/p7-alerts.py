#!/usr/bin/env python3
"""Evaluate aggregate snapshots. Exit 1 on actionable alerts, 2 on invalid data.
No network delivery; schedule with the operator's approved fnOS task mechanism.
"""
import argparse,datetime,json,pathlib,sys
p=argparse.ArgumentParser();p.add_argument('snapshot');p.add_argument('--backup-metadata',required=True);p.add_argument('--disk-free-bytes',type=int,required=True);p.add_argument('--ready',choices=['yes','no'],required=True);p.add_argument('--worker-active',choices=['yes','no'],required=True);p.add_argument('--tls-valid',choices=['yes','no'],required=True);p.add_argument('--sync-failures',type=int,default=0);a=p.parse_args()
try:
 s=json.loads(pathlib.Path(a.snapshot).read_text());b=json.loads(pathlib.Path(a.backup_metadata).read_text())
 now=datetime.datetime.now(datetime.timezone.utc)
 age=(now-datetime.datetime.fromisoformat(b['completed_at'].replace('Z','+00:00'))).total_seconds()
 observed=(now-datetime.datetime.fromisoformat(s['observed_at'].replace('Z','+00:00'))).total_seconds()
 alerts=[]
 failed=s.get('failed_jobs_needing_action',s['jobs'].get('failed',0)+s['jobs'].get('repair_required',0))
 for code,condition in [('snapshot_stale',observed>600 or observed< -60),('api_unready',a.ready=='no'),('worker_stopped',a.worker_active=='no'),('tls_expiring_or_invalid',a.tls_valid=='no'),('backup_stale',age>26*3600 or age< -60),('disk_low',a.disk_free_bytes<10*1024**3),('leases_expired',s['expired_leases']>0),('jobs_failed',failed>0),('backlog_stale',s['oldest_due_job_seconds']>300),('analysis_generation_lag',s['generation_lag']>0 and s['oldest_due_job_seconds']>300),('sync_repeated_failure',a.sync_failures>=5)]:
  if condition:alerts.append(code)
 print(json.dumps({'alerts':alerts,'status':'attention' if alerts else 'ok'}));sys.exit(bool(alerts))
except (OSError,ValueError,KeyError,TypeError):
 print('{"status":"monitoring_failed","alerts":["invalid_or_missing_evidence"]}');sys.exit(2)
