#!/usr/bin/env python3
"""Create new random credentials in an empty operator-selected private folder.
No certificate/key is generated here. Never overwrites existing credentials.
"""
import os,pathlib,secrets,sys
if len(sys.argv)!=2:raise SystemExit('usage: p7-secrets.py NEW_ABSOLUTE_DIRECTORY')
p=pathlib.Path(sys.argv[1])
if not p.is_absolute():raise SystemExit('absolute secret directory required')
p.mkdir(mode=0o700,parents=False,exist_ok=False)
passwords={name:secrets.token_hex(32) for name in ['bootstrap','owner','api','worker','backup','monitor']}
for name,value in passwords.items():
 f=p/('pg_'+name+'_password');f.write_text(value+'\n');f.chmod(0o600)
for name,user in [('owner','rai_owner'),('api','rai_api_login'),('worker','rai_worker_login'),('monitor','rai_monitor_login')]:
 f=p/(name+'_dsn');f.write_text(f'postgres://{user}:{passwords[name]}@postgres:5432/rai_v2?sslmode=disable\n');f.chmod(0o640)
print('New credential files created; install ownership and TLS files before preflight. No secret values emitted.')
