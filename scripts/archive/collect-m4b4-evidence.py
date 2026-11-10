#!/usr/bin/env python3
"""Retain status/count/hash/timing evidence only; never copy raw runtime logs."""
import collections
import hashlib
import json
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
RAW = ROOT / '.local/m4b4'
OUT = ROOT / 'docs/evidence/m4b4'
UUID = re.compile(r'(?i)\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b')
OUT.mkdir(parents=True, exist_ok=True)


def write(name, data):
    (OUT / name).write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n')


def json_run(file, postgres=False):
    raw = (RAW / file).read_text()
    counts = collections.Counter()
    packages = collections.Counter()
    skipped = []
    terminal = []
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        action = event.get('Action')
        if action not in ('pass', 'fail', 'skip'):
            continue
        if event.get('Test'):
            counts[action] += 1
            if action == 'skip':
                skipped.append(event['Test'])
        else:
            packages[action] += 1
        # Keep event identities as hashes. Raw test output can contain SQL or
        # assertion payloads and must never be copied to the evidence directory.
        terminal.append({'action': action, 'package': event.get('Package'),
                         'testHash': hashlib.sha256(event['Test'].encode()).hexdigest() if event.get('Test') else None,
                         'elapsedSeconds': event.get('Elapsed')})
    assert counts['fail'] == packages['fail'] == 0, file + ' failed'
    assert packages['pass'] > 0, file + ' incomplete'
    cleanup = {'leftoverDatabasesZero': 'RAI_TEST_LEFTOVER_DATABASES=0' in raw,
               'clusterRemoved': 'RAI_TEST_CLUSTER_REMOVED=1' in raw}
    if postgres:
        assert all(cleanup.values()), file + ' missing PG cleanup'
    write(file.replace('.jsonl', '-events.json'), terminal)
    return {'rawRunSHA256': hashlib.sha256(raw.encode()).hexdigest(),
            'testTerminalEvents': dict(counts), 'packageTerminalEvents': dict(packages),
            'skippedTests': [UUID.sub('[synthetic-id]', x) for x in skipped],
            'postgresCleanup': cleanup if postgres else None}


def text_run(file, clusters=0):
    raw = (RAW / file).read_text()
    assert not re.search(r'^\s*--- FAIL:|^FAIL(?:\s|$)|\*\* TEST .*FAILED', raw, re.M), file + ' failed'
    assert re.search(r'^PASS$', raw, re.M) or '** TEST EXECUTE SUCCEEDED **' in raw, file + ' incomplete'
    if clusters:
        assert raw.count('RAI_TEST_LEFTOVER_DATABASES=0') == clusters, file + ' DB cleanup'
        assert raw.count('RAI_TEST_CLUSTER_REMOVED=1') == clusters, file + ' cluster cleanup'
    timings = []
    # Whitelist only the explicit synthetic count/status/timing lines.
    for line in raw.splitlines():
        match = re.search(r'\b(B4_(?:TIMING|WORKER_TIMING|PG_TIMING|SIMULATOR_CASE|SIMULATOR_HOT_EMPTY|ARTIFACT_MAX|ARTIFACT_LIMIT|MACOS_SIGKILL)\b.*|M4B4_(?:SYNTHETIC|COMPONENT|SIMULATOR_VO2_FULL_E2E)\b.*|M4B2_SIMULATOR_APP_E2E\b.*)', line)
        if match:
            item = match.group(1)
            assert not UUID.search(item)
            timings.append(item)
    write(file.replace('.log', '-observations.json'), timings)
    return {'rawRunSHA256': hashlib.sha256(raw.encode()).hexdigest(),
            'passedTestTerminalEvents': len(re.findall(r'^\s*--- PASS:', raw, re.M)),
            'clustersRemoved': clusters,
            'simulatorFullE2EGroups': re.findall(r'M4B4_SIMULATOR_VO2_FULL_E2E=PASS cases=(\d+)', raw),
            'appXCTestCounts': re.findall(r'Executed (\d+) tests, with 0 failures', raw)}


runs = {
    'goTest': json_run('go-final-test.jsonl'),
    'goRace': json_run('go-final-race.jsonl'),
    'postgresFullRace': json_run('pg-full-race.jsonl', True),
    'b4RealLoginRace': text_run('pg-b4-real-login.log', 1),
    'simulatorFullClosure': text_run('simulator-real-login-closure.log', 3),
    'clientCrashRegression': text_run('client-real-login-closure.log', 1),
    'formalAppXCTest': text_run('simulator-app-xctest.log'),
}
swift = (RAW / 'swift-xcode.log').read_text()
assert re.search(r'Test run with 129 tests in 16 suites passed', swift)
sqlc = json.loads((OUT / 'sqlc.json').read_text())
assert sqlc['byteStable'] and sqlc['firstExit'] == sqlc['secondExit'] == 0
for file in ['go-final-vet.log', 'go-final-vet-integration.log', 'go-vet-all-tags.log', 'go-build.log', 'diff-check.log']:
    assert (RAW / file).read_text().strip() == '', file
assert 'PASS: external VO2 is pure' in (RAW / 'boundaries-final.log').read_text()
assert 'all modules verified' in (RAW / 'go-mod-verify.log').read_text()
base = json.loads((OUT / 'baseline.json').read_text())
changed = []
for name, digest in base['sourceSHA256'].items():
    path = ROOT / name
    if path.exists() and hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        changed.append(name)
protected = [n for n in base['sourceSHA256'] if n.startswith(('apps/server/migrations/', 'apps/server/internal/vo2maxexternal/', 'apps/server/internal/syncing/vo2', 'ios/RAI/Sources/SyncCore/VO2', 'ios/RAI/App/VO2', 'docs/VO2MAX_', 'docs/M4B2_', 'docs/M4B3_')) or 'openapi' in n.lower()]
assert not set(protected).intersection(changed), 'frozen contract changed'
source_files = [ROOT / f for f in subprocess.check_output(['git', 'ls-files', '--cached', '--others', '--exclude-standard'], cwd=ROOT, text=True).splitlines() if not f.startswith('docs/evidence/')]
source_hashes = {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in source_files if p.is_file()}
plan = ROOT / 'RAI-Go-PostgreSQL架构重设计与实施方案.md'
original = subprocess.check_output(['git', 'show', 'HEAD:' + plan.name], cwd=ROOT)
assert hashlib.sha256(plan.read_bytes()).digest() == hashlib.sha256(original).digest(), 'original plan changed'
write('freeze-scan.json', {'originalPlanPreserved': True, 'changedBaselineFiles': changed, 'protectedFiles': len(protected),
                           'changedProtectedFiles': [], 'highestMigration': 25, 'openAPIChanged': False,
                           'executableOwnOrLaboratoryMatches': []})
# Verify the executable source scan rather than trusting a prefilled status.
for path in source_files:
    name = str(path.relative_to(ROOT))
    if not (name.startswith('apps/server/') or name.startswith('ios/RAI/App/') or name.startswith('ios/RAI/Sources/')):
        continue
    if path.suffix not in ('.go', '.swift', '.sql') or path.name.endswith('_test.go') or '/integration/' in name:
        continue
    assert not any(term in path.read_text() for term in ('rai.vo2max.estimated/v1', 'laboratory_measured_vo2max')), 'unexpected executable model'
write('validation.json', {'schemaVersion': 'rai.m4b4.validation/1', 'date': '2026-11-04',
      'timezone': 'Asia/Shanghai', 'baselineHEAD': base['head'], 'runs': runs,
      'swift': {'tests': 129, 'suites': 16, 'failed': 0}, 'highestMigration': 25,
      'sqlcByteStable': True, 'active': 'legacy', 'promotion': False,
      'commands': {'go test ./...': 0, 'go test -race ./...': 0, 'go vet ./...': 0,
                   'go vet (integration and all special tags)': 0, 'go build ./...': 0,
                   'go mod verify': 0, 'dependency boundaries': 0, 'sqlc generate twice': 0,
                   'swift test --package-path ios/RAI': 0, 'Simulator App XCTest': 0,
                   'Simulator B4 full E2E groups and B2 regression': 0, 'git diff --check': 0},
      'sourceSHA256': source_hashes, 'P0': [], 'P1': [],
      'result': 'PASS WITH RISKS', 'externalEngineering': 'CLOSED',
      'realDeviceAcceptance': 'DEFERRED', 'physiologicalValidation': 'NOT VERIFIED',
      'ownVO2MaxModel': 'NOT IMPLEMENTED',
      'fullRaceScopeNote': 'Full PG race snapshot plus final B4 real-LOGIN race cases; do not add repeated runs as unique tests.',
      'notVerified': ['real Apple Watch availability', 'real source/metadata distribution',
                      'real HealthKit authorization/delete/update/background delivery',
                      'long offline behavior', 'WAN', 'production', 'physiological validity']})
manifest = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in OUT.iterdir() if p.is_file() and p.name != 'manifest.json'}
write('manifest.json', {'schemaVersion': 'rai.m4b4.evidence/1', 'files': manifest,
                       'privacy': 'Only synthetic counts, status, command results, hashes and timings. No health payload or credentials.'})
print(json.dumps({'status': 'PASS', 'runs': runs}, ensure_ascii=False, indent=2))
