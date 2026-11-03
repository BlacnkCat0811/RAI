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

algorithms = json.loads(subprocess.check_output(['go', 'list', '-json', './internal/analysis'], cwd=root, text=True))
for dependency in algorithms.get('Imports', []):
    if dependency.startswith(('rai/server/internal/storage', 'rai/server/internal/admin', 'github.com/jackc/', 'net', 'os', 'database/')):
        raise SystemExit('FAIL: analysis core reaches I/O: ' + dependency)
print('PASS: pure analysis domain excludes database, network and filesystem')

algorithms = json.loads(subprocess.check_output(['go', 'list', '-json', './internal/training'], cwd=root, text=True))
for dependency in algorithms.get('Imports', []):
    if dependency.startswith(('rai/server/internal/storage', 'rai/server/internal/admin', 'github.com/jackc/', 'net', 'os', 'database/')):
        raise SystemExit('FAIL: training core reaches I/O: ' + dependency)
print('PASS: pure training domain excludes database, network and filesystem')

for domain in ('workoutfeatures', 'workoutdomain', 'trainingload', 'performance'):
    package = json.loads(subprocess.check_output(['go', 'list', '-json', './internal/' + domain], cwd=root, text=True))
    for dependency in package.get('Imports', []):
        if dependency.startswith(('rai/server/internal/storage', 'rai/server/internal/admin', 'rai/server/internal/workoutdetail', 'github.com/jackc/', 'net', 'os', 'database/', 'math/rand')):
            raise SystemExit('FAIL: feature foundation reaches I/O/presentation/randomness: ' + dependency)
    for source in (root / 'internal' / domain).glob('*.go'):
        if source.name.endswith('_test.go'):
            continue
        if 'time.Now(' in source.read_text() or 'rand.' in source.read_text() or (domain in ('trainingload', 'performance') and 'time.LoadLocation(' in source.read_text()):
            raise SystemExit('FAIL: feature foundation reads ambient state: ' + str(source))
print('PASS: feature foundation excludes I/O, presentation, randomness and system clock')

# M3 cannot acquire a hidden dependency on load, VO2, recovery or raw samples.
performance = json.loads(subprocess.check_output(['go', 'list', '-json', './internal/performance'], cwd=root, text=True))
for dependency in performance.get('Imports', []):
    if dependency.startswith('rai/server/internal/') and dependency != 'rai/server/internal/workoutfeatures':
        raise SystemExit('FAIL: performance consumes a non-feature model: ' + dependency)
for source in (root / 'internal/storage/postgres').glob('performance_*.go'):
    if source.name.endswith('_test.go'):
        continue
    text = source.read_text()
    if any(table in text for table in ('health_samples', 'route_points', 'series_chunks', 'analysis_load_ledger')):
        raise SystemExit('FAIL: M3 reads raw signals/load: ' + str(source))
print('PASS: performance consumes only M1 features; no M2/VO2/raw-history dependency')
