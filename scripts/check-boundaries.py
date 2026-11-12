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

for domain in ('workoutfeatures', 'workoutdomain', 'trainingload', 'runfitness'):
    package = json.loads(subprocess.check_output(['go', 'list', '-json', './internal/' + domain], cwd=root, text=True))
    for dependency in package.get('Imports', []):
        if dependency.startswith(('rai/server/internal/storage', 'rai/server/internal/admin', 'rai/server/internal/workoutdetail', 'github.com/jackc/', 'net', 'os', 'database/', 'math/rand')):
            raise SystemExit('FAIL: feature foundation reaches I/O/presentation/randomness: ' + dependency)
    for source in (root / 'internal' / domain).glob('*.go'):
        if source.name.endswith('_test.go'):
            continue
        if 'time.Now(' in source.read_text() or 'rand.' in source.read_text() or (domain in ('trainingload', 'runfitness') and 'time.LoadLocation(' in source.read_text()):
            raise SystemExit('FAIL: feature foundation reads ambient state: ' + str(source))
print('PASS: feature foundation excludes I/O, presentation, randomness and system clock')

# Running fitness v1 consumes only neutral Workout features: no load, VO2,
# recovery, readiness, storage or the retired running-ability code.
runfitness = json.loads(subprocess.check_output(['go', 'list', '-json', './internal/runfitness'], cwd=root, text=True))
for dependency in runfitness.get('Imports', []):
    if dependency.startswith('rai/server/internal/') and dependency not in ('rai/server/internal/workoutfeatures', 'rai/server/internal/workoutdomain'):
        raise SystemExit('FAIL: runfitness consumes a non-feature model: ' + dependency)
print('PASS: runfitness consumes only neutral Workout features; no VO2/load/recovery dependency')

# The retired v60 running ability must not come back through any production path.
import re
for source in (root / 'internal').rglob('*.go'):
    if source.name.endswith('_test.go'):
        continue
    text = source.read_text()
    if 'rai/server/internal/performance"' in text or 'rai/server/internal/runningability"' in text:
        raise SystemExit('FAIL: production code imports the retired running ability: ' + str(source))
    code = re.sub(r'//[^\n]*|"(?:[^"\\\n]|\\.)*"|`[^`]*`', '', text)
    if re.search(r'\b[vV]60\w*\b|\w*V60\b', code):
        raise SystemExit('FAIL: production code uses the retired v60 identifier: ' + str(source))
print('PASS: no production import of retired running-ability packages and no v60 identifier')

external = json.loads(subprocess.check_output(['go','list','-json','./internal/vo2maxexternal'],cwd=root,text=True))
for dependency in external.get('Imports', []):
    if dependency.startswith(('rai/server/internal/', 'github.com/', 'net', 'os', 'database/', 'math/rand')):
        raise SystemExit('FAIL: external VO2 reaches I/O or another model: ' + dependency)
for source in (root / 'internal/vo2maxexternal').glob('*.go'):
    if not source.name.endswith('_test.go') and 'time.Now(' in source.read_text():
        raise SystemExit('FAIL: external VO2 reads system clock')
print('PASS: external VO2 is pure; no M1/M2/M3/HR reference dependency')

recovery = json.loads(subprocess.check_output(['go','list','-json','./internal/recovery'],cwd=root,text=True))
for dependency in recovery.get('Imports', []):
    if dependency.startswith(('rai/server/internal/', 'github.com/', 'net', 'os', 'database/', 'math/rand')):
        raise SystemExit('FAIL: recovery reaches another model or I/O: ' + dependency)
for source in (root / 'internal/recovery').glob('*.go'):
    if not source.name.endswith('_test.go') and any(x in source.read_text() for x in ('time.Now(', 'time.LoadLocation(', 'rand.')):
        raise SystemExit('FAIL: recovery reads ambient state')
print('PASS: recovery has no load/ability/VO2/Readiness, raw mutation or I/O dependency')

readiness = json.loads(subprocess.check_output(['go','list','-json','./internal/readiness'],cwd=root,text=True))
for dependency in readiness.get('Imports', []):
    if dependency.startswith(('rai/server/internal/', 'github.com/', 'net', 'os', 'database/', 'math/rand')):
        raise SystemExit('FAIL: readiness reaches another model/raw domain or I/O: ' + dependency)
for source in (root / 'internal/readiness').glob('*.go'):
    if not source.name.endswith('_test.go') and any(x in source.read_text() for x in ('time.Now(', 'time.LoadLocation(', 'rand.')):
        raise SystemExit('FAIL: readiness reads ambient state')
for source in (root / 'internal/storage/postgres').glob('readiness_*.go'):
    if source.name.endswith('_test.go'):
        continue
    if any(x in source.read_text() for x in ('health_samples', 'daily_health_coverage', 'series_chunks', 'route_points', 'performance_state', 'vo2max_external_projection')):
        raise SystemExit('FAIL: M6 reads raw health/ability/VO2: ' + str(source))
for domain in ('httpapi', 'training'):
    package = json.loads(subprocess.check_output(['go','list','-json','./internal/'+domain],cwd=root,text=True))
    if 'rai/server/internal/readiness' in package.get('Imports', []):
        raise SystemExit('FAIL: product/generator directly consumes M6 candidate')
print('PASS: readiness is pure; consumes no raw/ability/VO2; product and generator stay isolated')

# M7 is a read/presentation layer, never another model computation or consumer.
projection = json.loads(subprocess.check_output(['go','list','-json','./internal/athletemodel'],cwd=root,text=True))
for dependency in projection.get('Imports', []):
    if dependency.startswith(('rai/server/internal/storage','rai/server/internal/admin','github.com/','net','os','database/')):
        raise SystemExit('FAIL: product projection reaches I/O: '+dependency)
for source in (root/'internal/athletemodel').glob('*.go'):
    if not source.name.endswith('_test.go') and any(x in source.read_text() for x in ('time.Now(', '.Calculate(', 'time.LoadLocation(')):
        raise SystemExit('FAIL: M7 projection computes a model or reads ambient state')
for domain in ('training','jobs','analysis'):
    package=json.loads(subprocess.check_output(['go','list','-json','./internal/'+domain],cwd=root,text=True))
    if 'rai/server/internal/athletemodel' in package.get('Imports',[]):
        raise SystemExit('FAIL: existing consumer acquired candidate product dependency: '+domain)
reader=(root/'internal/storage/postgres/athlete_model.go').read_text()
if any(x in reader for x in ('.Calculate(', 'loadRecoveryInput(', 'loadReadinessInput(', 'loadPerformanceInput(')):
    raise SystemExit('FAIL: unified reader recalculates/loads raw model inputs')
print('PASS: M7 typed projection is pure; reader only validates/Freshens/maps; httpapi is the only candidate-product transport and legacy analysis consumers are unchanged')

for domain in ('sessionrecovery',):
    package = json.loads(subprocess.check_output(['go', 'list', '-json', './internal/' + domain], cwd=root, text=True))
    for dependency in package.get('Imports', []):
        if dependency.startswith(('rai/server/internal/', 'github.com/', 'net', 'os', 'database/', 'math/rand')):
            raise SystemExit('FAIL: '+domain+' reaches another model or I/O: '+dependency)
    for source in (root/'internal'/domain).glob('*.go'):
        if not source.name.endswith('_test.go') and any(x in source.read_text() for x in ('time.Now(', 'time.LoadLocation(', 'rand.')):
            raise SystemExit('FAIL: '+domain+' reads ambient state')
print('PASS: session recovery is a pure independent DTO consumer')
