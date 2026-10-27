#!/usr/bin/env python3
"""Inspect the real Go dependency graph, not source-name matches. Run with Go on PATH."""
import json
import pathlib
import subprocess

root = pathlib.Path(__file__).resolve().parents[1] / 'apps/server'
raw = subprocess.check_output(['go', 'list', '-deps', '-json', './internal/httpapi', './internal/syncing', './internal/jobs', './internal/storage/postgres'], cwd=root, text=True)
decoder = json.JSONDecoder()
packages = []
while raw.strip():
    value, end = decoder.raw_decode(raw.lstrip())
    packages.append(value)
    raw = raw.lstrip()[end:]
for package in packages:
    path = package['ImportPath']
    if path == 'rai/server/internal/admin' or path.startswith('rai/server/migrations') or path.startswith('github.com/pressly/goose'):
        raise SystemExit('FAIL: online graph reaches maintenance package: ' + path)
print('PASS: online import graph excludes admin, migration and goose (%d packages)' % len(packages))
