#!/usr/bin/env python3
"""Independent Mac backend and USB iPhone Debug acceptance. No SSH/migration of data.

Only prepare/install/probe are infrastructure checks. single/completeness read
real HealthKit with the user's authorization; they are never synthetic tests.
Private state/credentials/logs stay in the gitignored, mode-0700 run directory.
"""
import argparse
import fcntl
import hashlib
import ipaddress
import json
import os
import pathlib
import re
import secrets
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[2]
RUN = pathlib.Path(os.environ.get('RAI_LOCAL_DEVICE_RUN') or (ROOT / '.local/device-acceptance')).resolve()
DERIVED = pathlib.Path(os.environ.get('RAI_LOCAL_DEVICE_DERIVED') or (RUN / 'DerivedData')).resolve()
CONFIGURATION = os.environ.get('RAI_LOCAL_DEVICE_CONFIGURATION', 'Debug')
PG_PORT = int(os.environ.get('RAI_LOCAL_DEVICE_PG_PORT', '55433'))
HTTP_PORT = int(os.environ.get('RAI_LOCAL_DEVICE_HTTP_PORT', '18080'))
PG = ROOT / '.local/toolchain/Postgres.app/Contents/Versions/18/bin'
GO = ROOT / '.local/toolchain/go/bin/go'
BINARY = RUN / 'rai'
APP_ID = 'com.rai.v2.ios'
STATE = RUN / 'run.json'
# Personal single-phone backend: the default upload lane (30/min, burst 2 per
# device) throttles the HRV/sleep history backfill into 15-minute steps. Read and
# write keep the server defaults; only the upload lane is widened.
RATE_BUDGETS = {
    'read': {'device': {'perMinute': 120, 'burst': 30}, 'dataset': {'perMinute': 240, 'burst': 60}},
    'write': {'device': {'perMinute': 60, 'burst': 10}, 'dataset': {'perMinute': 120, 'burst': 20}},
    'upload': {'device': {'perMinute': 120, 'burst': 12}, 'dataset': {'perMinute': 240, 'burst': 24}},
}
os.umask(0o077)


def write(path, value):
    with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as stream:
        temporary = pathlib.Path(stream.name)
        try:
            stream.write(json.dumps(value, indent=2) + '\n')
            stream.flush()
            os.fsync(stream.fileno())
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)


def environment():
    env = {k: v for k, v in os.environ.items() if not k.startswith(('PG', 'RAI_', 'DEVICECTL_CHILD_'))}
    env.update(GOTOOLCHAIN='local', GOPATH=str(ROOT / '.local/gopath'),
               GOCACHE=str(ROOT / '.local/gocache'), GOMODCACHE=str(ROOT / '.local/gopath/pkg/mod'))
    for candidate in (os.environ.get('DEVELOPER_DIR', ''), '/Applications/Xcode.app/Contents/Developer',
                      str(pathlib.Path.home() / 'Downloads/Xcode.app/Contents/Developer')):
        if candidate and pathlib.Path(candidate, 'usr/bin/xcodebuild').is_file():
            env['DEVELOPER_DIR'] = candidate
            break
    return env


def execute(args, *, env=None, capture=False, timeout=180, cwd=ROOT):
    with (RUN / 'commands.log').open('ab') as log:
        result = subprocess.run([str(a) for a in args], cwd=cwd, env=env or environment(),
                                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE if capture else log,
                                stderr=log, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'{pathlib.Path(str(args[0])).name} failed (exit {result.returncode}); private commands.log retained')
    return result.stdout.decode() if capture else None


def lan_address():
    specified = os.environ.get('RAI_LOCAL_DEVICE_HOST')
    if specified:
        candidates = [specified]
    else:
        route = subprocess.run(['route', '-n', 'get', 'default'], capture_output=True, text=True)
        interfaces = [line.split()[-1] for line in route.stdout.splitlines() if line.strip().startswith('interface:')]
        candidates = []
        for interface in dict.fromkeys(interfaces + ['en0', 'en1']):
            result = subprocess.run(['ipconfig', 'getifaddr', interface], capture_output=True, text=True)
            if result.returncode == 0:
                candidates.append(result.stdout.strip())
    for candidate in candidates:
        try:
            address = ipaddress.IPv4Address(candidate)
        except ipaddress.AddressValueError:
            continue
        ranges = ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '169.254.0.0/16')
        if any(address in ipaddress.ip_network(network) for network in ranges):
            return str(address)
    raise RuntimeError('No private Mac LAN IPv4 address; set RAI_LOCAL_DEVICE_HOST to its reachable LAN address')


def load():
    if not STATE.exists():
        raise RuntimeError('Run prepare first')
    state = json.loads(STATE.read_text())
    if state.get('environment') != 'local-mac-device/v1':
        raise RuntimeError('Unexpected private run identity; preserved without changes')
    return state


def server_env(state):
    env = environment()
    env['RAI_DATABASE_URL_FILE'] = str(RUN / 'database-url')
    env['RAI_LISTEN_ADDR'] = state['listenAddr']
    env['RAI_RATE_BUDGETS'] = json.dumps(RATE_BUDGETS, separators=(',', ':'))
    return env


def running(pid, mode):
    if not isinstance(pid, int):
        return False
    result = subprocess.run(['ps', '-p', str(pid), '-o', 'command='], capture_output=True, text=True)
    return result.returncode == 0 and result.stdout.strip() == f'{BINARY} {mode}'


def request(state, path, authenticated=False):
    headers = {}
    if authenticated:
        package = json.loads((RUN / 'enrollment.json').read_text())
        headers['Authorization'] = 'Bearer ' + package['enrollment']['token']
    req = urllib.request.Request(state['baseURL'] + path, headers=headers)
    # Do not send private-LAN credentials through an ambient HTTP proxy.
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=3) as response:
        return json.load(response)


def prepare():
    host = lan_address()
    if STATE.exists():
        state = load()
        if state['host'] != host:
            raise RuntimeError('Mac LAN address changed; preserve this run and restore its address before reuse')
    else:
        state = dict(environment='local-mac-device/v1', host=host, pgPort=PG_PORT,
                     listenAddr=f'{host}:{HTTP_PORT}', baseURL=f'http://{host}:{HTTP_PORT}',
                     operationID=str(uuid.uuid4()), outboxNamespace=str(uuid.uuid4()))
        write(STATE, state)
    api_running = running(state.get('apiPID'), 'api')
    worker_running = running(state.get('workerPID'), 'worker')
    if api_running or worker_running:
        if not (api_running and worker_running):
            raise RuntimeError('Local stack is partially running; stop it before prepare; data is preserved')
        request(state, '/ready')
        hello = request(state, '/v2/handshake', authenticated=True)
        if hashlib.sha256(hello['datasetID'].encode()).hexdigest() != state.get('datasetSHA256'):
            raise RuntimeError('Running local stack identity mismatch')
        print(json.dumps({'result': 'ALREADY_RUNNING', 'environment': state['environment'], 'handshakeVerified': True}))
        return
    execute([GO, 'build', '-trimpath', '-o', BINARY, './cmd/rai'], cwd=ROOT / 'apps/server', timeout=300)
    password_file = RUN / 'database-password'
    if not password_file.exists():
        password_file.write_text(secrets.token_hex(24) + '\n')
        password_file.chmod(0o600)
    password = password_file.read_text().strip()
    data, sock = RUN / 'pgdata', RUN / 'socket'
    sock.mkdir(exist_ok=True)
    if not (data / 'PG_VERSION').exists():
        execute([PG / 'initdb', '-D', data, '--username=rai_local_device', f'--pwfile={password_file}',
                 '--auth-local=trust', '--auth-host=scram-sha-256', '--encoding=UTF8', '--locale=C', '--data-checksums'])
    result = subprocess.run([str(PG / 'pg_ctl'), '-D', str(data), 'status'], env=environment(),
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if result.returncode:
        execute([PG / 'pg_ctl', '-D', data, '-l', RUN / 'postgres.log', '-o',
                 f'-p {state["pgPort"]} -k {sock} -h 127.0.0.1 -c timezone=UTC -c max_connections=50', '-w', 'start'])
    base = [PG / 'psql', '-X', '-h', sock, '-p', str(state['pgPort']), '-U', 'rai_local_device']
    exists = execute(base + ['-d', 'postgres', '-Atc', "SELECT 1 FROM pg_database WHERE datname='rai_local_device'"], capture=True)
    if exists.strip() != '1':
        execute([PG / 'createdb', '-h', sock, '-p', str(state['pgPort']), '-U', 'rai_local_device', 'rai_local_device'])
    start_stack(state, password)
    hello = request(state, '/v2/handshake', authenticated=True)
    print(json.dumps({'result': 'LOCAL_BACKEND_READY', 'environment': state['environment'],
                      'schema': json.loads(execute([BINARY, 'version'], capture=True))['schema'],
                      'completenessV2': hello['capabilities'].get('workoutSyncCompletenessV2Version') == 2,
                      'configuration': CONFIGURATION, 'databaseLoopbackOnly': True,
                      'originalCloudAccessed': False}))


def start_stack(state, password):
    """Migrate, enrol a fresh dataset, then run API and Worker until ready."""
    url_file = RUN / 'database-url'
    url_file.write_text(f'postgres://rai_local_device:{password}@127.0.0.1:{state["pgPort"]}/rai_local_device?sslmode=disable\n')
    url_file.chmod(0o600)
    env = server_env(state)
    execute([BINARY, 'migrate'], env=env)
    execute([BINARY, 'init', '--operation-id', state['operationID'], '--actor', 'local-device-development',
             '--base-url', f'http://127.0.0.1:{HTTP_PORT}', '--credential-file', RUN / 'enrollment.json'], env=env)
    package = json.loads((RUN / 'enrollment.json').read_text())
    state['datasetSHA256'] = hashlib.sha256(package['enrollment']['datasetID'].encode()).hexdigest()
    state['sourceRevision'] = execute(['git', 'rev-parse', 'HEAD'], capture=True).strip()
    state['sourceDiffSHA256'] = hashlib.sha256(execute(['git', 'diff', 'HEAD'], capture=True).encode()).hexdigest()
    for mode in ('api', 'worker'):
        with (RUN / f'{mode}.log').open('ab') as log:
            process = subprocess.Popen([str(BINARY), mode], env=env, stdin=subprocess.DEVNULL,
                                       stdout=log, stderr=log, start_new_session=True)
        state[mode + 'PID'] = process.pid
        write(STATE, state)
    for _ in range(60):
        try:
            request(state, '/ready')
            hello = request(state, '/v2/handshake', authenticated=True)
            if hashlib.sha256(hello['datasetID'].encode()).hexdigest() != state['datasetSHA256']:
                raise RuntimeError('Local handshake identity mismatch')
            execute([BINARY, 'worker-ready'], env=env)
            return
        except (OSError, ValueError):
            time.sleep(0.5)
    raise RuntimeError('Local API not ready; private logs retained')


def stop_stack(state):
    for mode in ('api', 'worker'):
        pid = state.get(mode + 'PID')
        if isinstance(pid, int) and running(pid, mode):
            os.kill(pid, signal.SIGTERM)
    for mode in ('api', 'worker'):
        for _ in range(80):
            if not running(state.get(mode + 'PID'), mode):
                break
            time.sleep(0.25)
        else:
            raise RuntimeError(f'Local {mode} did not stop; private logs retained')
        state.pop(mode + 'PID', None)
        write(STATE, state)


def reset():
    """Fresh local dataset and namespace; the previous run is preserved on disk.

    Only the disposable local database is replaced, so a repeat measurement is a
    genuine first import. No phone state, HealthKit record or cloud data is touched.
    """
    state = load()
    previous = {'operationID': state['operationID'], 'outboxNamespace': state['outboxNamespace'],
                'datasetSHA256': state.get('datasetSHA256')}
    stop_stack(state)
    sock = RUN / 'socket'
    base = [PG / 'psql', '-X', '-h', sock, '-p', str(state['pgPort']), '-U', 'rai_local_device']
    execute(base + ['-d', 'postgres', '-c', 'DROP DATABASE IF EXISTS rai_local_device WITH (FORCE)'], timeout=120)
    execute([PG / 'createdb', '-h', sock, '-p', str(state['pgPort']), '-U', 'rai_local_device', 'rai_local_device'])
    # The prepared recovery credential is never overwritten: it is the only way
    # back to a dataset, so a reset retires it under its own operation id.
    enrollment = RUN / 'enrollment.json'
    if enrollment.exists():
        enrollment.rename(RUN / f'enrollment-{state["operationID"]}.json')
    state['operationID'] = str(uuid.uuid4())
    state['outboxNamespace'] = str(uuid.uuid4())
    write(STATE, state)
    start_stack(state, (RUN / 'database-password').read_text().strip())
    hello = request(state, '/v2/handshake', authenticated=True)
    print(json.dumps({'result': 'LOCAL_DATASET_RESET', 'environment': state['environment'],
                      'newDataset': True, 'previousDatasetSHA256': previous['datasetSHA256'],
                      'datasetSHA256': state['datasetSHA256'],
                      'completenessV2': hello['capabilities'].get('workoutSyncCompletenessV2Version') == 2,
                      'originalCloudAccessed': False}))


def device(state):
    output = RUN / 'device-discovery.json'
    execute(['xcrun', 'devicectl', 'list', 'devices', '--json-output', output], timeout=40)
    devices = json.loads(output.read_text())['result']['devices']
    selected = [d for d in devices if d.get('connectionProperties', {}).get('transportType') == 'wired'
                and d.get('connectionProperties', {}).get('pairingState') == 'paired']
    if len(selected) != 1:
        raise RuntimeError('Exactly one paired USB iPhone is required; unlock and trust the device')
    identifier = selected[0]['identifier']
    if state.get('deviceIdentifier') not in (None, identifier):
        raise RuntimeError('USB device differs from this run; previous identity preserved')
    state['deviceIdentifier'] = identifier
    write(STATE, state)
    return identifier


def install():
    state = load()
    identifier = device(state)
    # A Release acceptance build must still contain the DEBUG-gated local acceptance
    # instrumentation, so the condition is added on top of Release's -O optimisation.
    # Only diagnostics are gated by it; optimisation and assertion config stay Release.
    # The Debug Info.plist carries the LAN ATS exception, the local-network usage
    # string and the configured Mac host; Release ships without them, so the
    # acceptance build reuses that plist instead of changing the shipped one.
    conditions = ['SWIFT_ACTIVE_COMPILATION_CONDITIONS=DEBUG',
                  f'INFOPLIST_FILE={ROOT / "ios/RAI/Config/Info-Debug.plist"}'] if CONFIGURATION != 'Debug' else []
    execute(['xcodebuild', '-project', ROOT / 'ios/RAI/RAI.xcodeproj', '-scheme', 'RAI',
             '-configuration', CONFIGURATION, '-destination', 'generic/platform=iOS', '-derivedDataPath', DERIVED,
             f'RAI_DEV_HTTP_HOST={state["host"]}'] + conditions + ['-allowProvisioningUpdates', 'build', '-quiet'], timeout=1800)
    app = DERIVED / f'Build/Products/{CONFIGURATION}-iphoneos/RAI.app'
    execute(['xcrun', 'devicectl', 'device', 'install', 'app', '--device', identifier, app,
             '--json-output', RUN / 'install-result.json'], timeout=180)
    print(json.dumps({'result': 'APP_UPDATED', 'configuration': CONFIGURATION,
                      'existingAppDataPreserved': True, 'httpHostScopedToMac': True}))


def copy(state, direction, source, destination):
    execute(['xcrun', 'devicectl', 'device', 'copy', direction, '--device', state['deviceIdentifier'],
             '--domain-type', 'appDataContainer', '--domain-identifier', APP_ID,
             '--source', source, '--destination', destination, '--json-output', RUN / 'copy-result.json'], timeout=40)


def structure(workout=None):
    state = load()
    launch('structure', workout)
    for _ in range(90):
        time.sleep(2)
        result = report(state)
        if result.get('result') == 'RUNNING':
            continue
        if result.get('result') != 'STRUCTURE_INSPECTED':
            raise RuntimeError('structure inspection failed: ' + str(result.get('reason') or result.get('result')))
        copy(state, 'from', 'Library/Caches/RAIWorkoutStructure.json', RUN / 'workout-structure.json')
        raw = json.loads((RUN / 'workout-structure.json').read_text())
        keys = 0
        for row in raw.get('associatedSamples', []):
            if isinstance(row, dict):
                keys += len(row.get('metadataKeys', []))
        print(json.dumps({'result': 'STRUCTURE_INSPECTED', 'sampleTypeRows': len(raw.get('associatedSamples', [])),
                          'metadataKeyRows': keys, 'healthValuesExported': False}, ensure_ascii=False))
        return
    raise RuntimeError('structure inspection still pending')


def report(state):
    copy(state, 'from', 'Documents/local-device-report.json', RUN / 'device-report.json')
    raw = json.loads((RUN / 'device-report.json').read_text())
    allowed = ('environment', 'result', 'stage', 'healthValuesExported', 'configuration',
               'mainThreadMaxStallMs', 'mainThreadStallsOver250', 'mainThreadTicks',
               'processPeakRssBytes', 'residentAfterSyncBytes', 'listEvidence',
               'originalSessionManifestPresent', 'originalSessionManifestUnchanged',
               'handshakeVerified', 'command', 'wallMs', 'workoutReceiptVerified', 'stateBytes',
               'generation', 'committedAnchorCount', 'systemCode',
               'dailyAnchorCount', 'dailyPendingCount', 'dailyBlockedCount', 'dailyBackfillCompleteCount',
               'trainingPlanPresent', 'trainingPlanPublished', 'trainingReadFailed',
               'trainingGenerationState', 'trainingConfigRevision', 'trainingEffectiveVersion',
               # Counts and digests only: never a Workout identity or health value.
               'visibleWorkoutCount', 'eligibleWorkoutCount', 'excludedWorkoutCount',
               'unclassifiedWorkoutCount', 'capturedWorkoutCount', 'processedWorkoutCount',
               'committedWorkoutCount', 'cyclePending', 'manifestPending', 'manifestSHA256',
               'cutoffSHA256', 'sourcePolicyVersion', 'sourceClassificationCounts')
    result = {k: raw[k] for k in allowed if k in raw}
    for key in ('dailyAnchorCount', 'dailyPendingCount', 'dailyBlockedCount', 'dailyBackfillCompleteCount',
                'trainingConfigRevision', 'trainingEffectiveVersion'):
        if key in result and (type(result[key]) is not int or result[key] < 0):
            result.pop(key)
    for key in ('trainingPlanPresent', 'trainingPlanPublished', 'trainingReadFailed'):
        if key in result and type(result[key]) is not bool:
            result.pop(key)
    if result.get('trainingGenerationState') not in ('pending', 'queued', 'running', 'succeeded', 'failed', 'dirty'):
        result.pop('trainingGenerationState', None)
    if re.fullmatch(r'[a-z0-9_:-]{1,128}', raw.get('reason', '')):
        result['reason'] = raw['reason']
    if isinstance(raw.get('workoutList'), list):
        # Duration buckets and indoor flag only: no identity, date or health value.
        result['workoutList'] = [{k: row[k] for k in ('durationMinutes', 'indoor', 'outcome') if k in row}
                                 for row in raw['workoutList'] if isinstance(row, dict)]
    if isinstance(raw.get('timings'), dict):
        timing_names = {'healthkit_read_ms', 'route_read_ms', 'bundle_build_ms', 'bundle_bytes', 'upload_ms',
                        'workout_sync_ms', 'workout_query_used_committed_anchor', 'workout_reused_exact_bundle',
                        'publication_ms', 'gzip_ms', 'wire_bytes',
                        'workout_query_ms', 'source_classify_ms', 'reused_discovery_reads', 'full_sample_reads',
                        'sample_archive_ms', 'sample_archive_writes', 'series_read_ms', 'bundle_encode_ms',
                        'bundle_persist_ms', 'series_capture_flushes', 'series_capture_synchronizations',
                        'series_capture_points', 'route_capture_flushes', 'route_capture_synchronizations',
                        'route_capture_points', 'workout_sync_skipped_unchanged',
                        'capture_max_active_streams', 'capture_stream_limit', 'capture_peak_queue_bytes',
                        'capture_writer_batches', 'capture_writer_records', 'normalized_capture_ms',
                        'normalized_segment_writes', 'sample_archive_calls', 'sample_unarchive_calls',
                        'object_archive_calls', 'bundle_gzip_derive_ms', 'bundle_gzip_wire_bytes',
                        'bundle_gzip_post_attempt', 'bundle_gzip_post_success'}
        result['timings'] = {k: v for k, v in raw['timings'].items()
                             if k in timing_names and isinstance(v, (int, float))}
    if isinstance(raw.get('queryCounts'), dict):
        result['queryCounts'] = {k: v for k, v in raw['queryCounts'].items()
                                 if k in ('committedAnchor', 'stagedCursor', 'bootstrap') and isinstance(v, int)}
    if result.get('result') not in (None, 'RUNNING'):
        reports = RUN / 'reports'
        reports.mkdir(exist_ok=True, mode=0o700)
        digest = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
        archived = reports / (digest + '.json')
        if not archived.exists():
            write(archived, result)
    return result


def launch(command, workout=None, streams=None, port=None):
    if command == 'completeness':
        raise RuntimeError('Workout completeness v2 is retired; use single for a Workout, daily for daily health, or training for the isolated plan screen')
    if command not in ('probe', 'single', 'list', 'structure', 'daily', 'training'):
        raise RuntimeError('Unsupported local device command')
    state = load()
    identifier = device(state)
    package = json.loads((RUN / 'enrollment.json').read_text())
    enrollment = package['enrollment']
    base_url = f'http://{state["host"]}:{port}' if port else state['baseURL']
    config = {'environment': state['environment'], 'baseURL': base_url,
              'datasetSHA256': state['datasetSHA256'], 'outboxNamespace': state['outboxNamespace']}
    config.update(command=command, collectorEpoch=enrollment['collectorEpoch'], token=enrollment['token'])
    if workout:
        # Private selector; the pulled report never contains a Workout identity.
        config['workoutID'] = workout
    if streams is not None:
        # Acceptance-only policy selector; the shipped product default stays 2.
        config['captureConcurrency'] = streams
    write(RUN / 'device-config.json', config)
    write(RUN / 'report-sentinel.json', {'result': 'RUNNING', 'command': command})
    copy(state, 'to', RUN / 'report-sentinel.json', 'Documents/local-device-report.json')
    copy(state, 'to', RUN / 'device-config.json', 'Documents/local-device-config.json')
    # Rollback switches are forwarded only when the operator set them, so a normal
    # acceptance launch is always the optimised path.
    launch_environment = {'RAI_LOCAL_DEVICE_ACCEPTANCE': '1'}
    for name in ('RAI_ACCEPTANCE_LEGACY_CAPTURE_IO', 'RAI_ACCEPTANCE_LEGACY_ENCODER'):
        if os.environ.get(name) == '1':
            launch_environment[name] = '1'
    execute(['xcrun', 'devicectl', 'device', 'process', 'launch', '--device', identifier, '--terminate-existing',
             '--environment-variables', json.dumps(launch_environment), APP_ID,
             '--json-output', RUN / 'launch-result.json'], timeout=45)
    if command == 'probe':
        for _ in range(20):
            time.sleep(2)
            result = report(state)
            if result.get('result') != 'RUNNING':
                print(json.dumps(result, ensure_ascii=False))
                if result.get('result') != 'PROBE_VERIFIED':
                    raise RuntimeError('iPhone -> Mac probe not verified; check phone local-network permission and LAN reachability')
                return
        raise RuntimeError('iPhone probe still pending; use report (no HealthKit reads were requested)')
    print(json.dumps({'result': 'STARTED', 'command': command, 'environment': state['environment'],
                      'next': 'python3 scripts/local/device.py report', 'fullCompletenessClaimed': False}))


PROXY_PORT = 18081
MANIFEST_PATH = '/v2/sync/workout-completeness'


def _headers(block):
    parsed = {}
    for line in block.split(b'\r\n')[1:]:
        if b':' in line:
            key, _, value = line.partition(b':')
            parsed[key.strip().lower().decode('latin-1')] = value.strip().decode('latin-1')
    return parsed


def _read_until(sock, buffer, marker):
    while marker not in buffer:
        chunk = sock.recv(65536)
        if not chunk:
            return None, buffer
        buffer += chunk
    head, _, rest = buffer.partition(marker)
    return head + marker, rest


def _read_exact(sock, buffer, length):
    while len(buffer) < length:
        chunk = sock.recv(min(65536, length - len(buffer)))
        if not chunk:
            break
        buffer += chunk
    return buffer[:length], buffer[length:]


def _read_chunked(sock, buffer):
    raw = b''
    while True:
        line, buffer = _read_until(sock, buffer, b'\r\n')
        if line is None:
            return raw, buffer
        raw += line
        size = int(line.strip().split(b';')[0] or b'0', 16)
        body, buffer = _read_exact(sock, buffer, size + 2)
        raw += body
        if size == 0:
            return raw, buffer


def _read_body(sock, buffer, headers):
    if headers.get('transfer-encoding', '').lower() == 'chunked':
        return _read_chunked(sock, buffer)
    return _read_exact(sock, buffer, int(headers.get('content-length') or 0))


class ManifestACKDropper:
    """Transport-level lost ACK: relay every byte, then withhold exactly one
    committed manifest response. The API and the App protocol are untouched;
    the server still commits durably before this proxy closes the client side."""

    def __init__(self, listen, upstream):
        self.upstream = upstream
        self.dropped = threading.Event()
        self.facts = {}
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(listen)
        self.listener.listen(8)
        self.connections = []

    def serve(self):
        while not self.dropped.is_set():
            try:
                client, _ = self.listener.accept()
            except OSError:
                return
            self.connections.append(client)
            threading.Thread(target=self.relay, args=(client,), daemon=True).start()

    def relay(self, client):
        try:
            upstream = socket.create_connection(self.upstream, timeout=120)
        except OSError:
            client.close()
            return
        self.connections.append(upstream)
        client.settimeout(300); upstream.settimeout(300)
        cbuf = ubuf = b''
        try:
            while True:
                head, cbuf = _read_until(client, cbuf, b'\r\n\r\n')
                if head is None:
                    return
                method, path = (head.split(b'\r\n', 1)[0].split(b' ') + [b'', b''])[:2]
                request_headers = _headers(head)
                body, cbuf = _read_body(client, cbuf, request_headers)
                upstream.sendall(head + body)
                response, ubuf = _read_until(upstream, ubuf, b'\r\n\r\n')
                if response is None:
                    return
                status = int(response.split(b'\r\n', 1)[0].split(b' ')[1])
                response_headers = _headers(response)
                payload, ubuf = _read_body(upstream, ubuf, response_headers)
                complete = response + payload
                if (method == b'POST' and path.decode('latin-1').startswith(MANIFEST_PATH)
                        and status == 200 and not self.dropped.is_set()):
                    # Durable facts and the manifest transaction are already
                    # committed upstream; the client receives none of these bytes.
                    self.facts = {'upstreamStatus': status, 'responseBytes': len(complete),
                                  'responseSHA256': hashlib.sha256(complete).hexdigest(),
                                  'responseBytesRelayed': 0, 'path': MANIFEST_PATH}
                    self.dropped.set()
                    return
                client.sendall(complete)
                if (response_headers.get('connection', '').lower() == 'close'
                        or request_headers.get('connection', '').lower() == 'close'):
                    return
        except OSError:
            return
        finally:
            for sock in (client, upstream):
                try:
                    sock.close()
                except OSError:
                    pass

    def shutdown(self):
        try:
            self.listener.close()
        except OSError:
            pass
        for sock in self.connections:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                sock.close()
            except OSError:
                pass



class BundleRejectProxy:
    """Reject bundle uploads before they reach the API, leaving a durable
    prepared bundle on the phone. Every other request is relayed unchanged."""

    def __init__(self, listen, upstream):
        self.upstream = upstream
        self.rejected = threading.Event()
        self.facts = {}
        self.listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(listen)
        self.listener.listen(8)
        self.connections = []

    def serve(self):
        while not self.rejected.is_set():
            try:
                client, _ = self.listener.accept()
            except OSError:
                return
            self.connections.append(client)
            threading.Thread(target=self.relay, args=(client,), daemon=True).start()

    def relay(self, client):
        try:
            upstream = socket.create_connection(self.upstream, timeout=120)
        except OSError:
            client.close()
            return
        self.connections.append(upstream)
        client.settimeout(300); upstream.settimeout(300)
        cbuf = ubuf = b''
        try:
            while True:
                head, cbuf = _read_until(client, cbuf, b'\r\n\r\n')
                if head is None:
                    return
                method, path = (head.split(b'\r\n', 1)[0].split(b' ') + [b'', b''])[:2]
                request_headers = _headers(head)
                body, cbuf = _read_body(client, cbuf, request_headers)
                if method == b'POST' and path == b'/v2/sync/workout-bundles':
                    self.facts = {'method': 'POST', 'path': '/v2/sync/workout-bundles',
                                  'requestBytes': len(body), 'responseStatus': 503,
                                  'forwarded': False}
                    self.rejected.set()
                    payload = b'{"error":"injected_bundle_upload_failure"}'
                    client.sendall(b'HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\n'
                                   b'Content-Length: ' + str(len(payload)).encode() + b'\r\nConnection: close\r\n\r\n' + payload)
                    return
                upstream.sendall(head + body)
                response, ubuf = _read_until(upstream, ubuf, b'\r\n\r\n')
                if response is None:
                    return
                response_headers = _headers(response)
                payload, ubuf = _read_body(upstream, ubuf, response_headers)
                client.sendall(response + payload)
                if (response_headers.get('connection', '').lower() == 'close'
                        or request_headers.get('connection', '').lower() == 'close'):
                    return
        except OSError:
            return
        finally:
            for sock in (client, upstream):
                try:
                    sock.close()
                except OSError:
                    pass

    def shutdown(self):
        try:
            self.listener.close()
        except OSError:
            pass
        for sock in self.connections:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            try:
                sock.close()
            except OSError:
                pass


class CaptureBundleProxy(BundleRejectProxy):
    """Relay normally while saving the first bundle multipart body for a
    repeatable two-slot server-side replay measurement."""

    def __init__(self, listen, upstream):
        super().__init__(listen, upstream)
        self.captured = threading.Event()
        self.capture_path = RUN / 'captured-bundle-request.bin'
        self.capture_headers = {}

    def serve(self):
        while not self.captured.is_set():
            try:
                client, _ = self.listener.accept()
            except OSError:
                return
            self.connections.append(client)
            threading.Thread(target=self.relay, args=(client,), daemon=True).start()

    def relay(self, client):
        try:
            upstream = socket.create_connection(self.upstream, timeout=120)
        except OSError:
            client.close()
            return
        self.connections.append(upstream)
        client.settimeout(300); upstream.settimeout(300)
        cbuf = ubuf = b''
        try:
            while True:
                head, cbuf = _read_until(client, cbuf, b'\r\n\r\n')
                if head is None:
                    return
                method, path = (head.split(b'\r\n', 1)[0].split(b' ') + [b'', b''])[:2]
                request_headers = _headers(head)
                body, cbuf = _read_body(client, cbuf, request_headers)
                if method == b'POST' and path == b'/v2/sync/workout-bundles' and not self.captured.is_set():
                    self.capture_path.write_bytes(body)
                    self.capture_headers = {k: v for k, v in request_headers.items()}
                    self.captured.set()
                upstream.sendall(head + body)
                response, ubuf = _read_until(upstream, ubuf, b'\r\n\r\n')
                if response is None:
                    return
                response_headers = _headers(response)
                payload, ubuf = _read_body(upstream, ubuf, response_headers)
                client.sendall(response + payload)
                if (response_headers.get('connection', '').lower() == 'close'
                        or request_headers.get('connection', '').lower() == 'close'):
                    return
        except OSError:
            return
        finally:
            for sock in (client, upstream):
                try:
                    sock.close()
                except OSError:
                    pass


def capturebundle(workout=None):
    state = load(); identifier = device(state)
    proxy = CaptureBundleProxy((state['host'], PROXY_PORT + 3), (state['host'], HTTP_PORT))
    threading.Thread(target=proxy.serve, daemon=True).start()
    try:
        launch('single', workout, None, port=PROXY_PORT + 3)
        result = {}
        for _ in range(180):
            time.sleep(2)
            result = report(state)
            if result.get('result') != 'RUNNING':
                break
        if not proxy.captured.is_set():
            raise RuntimeError('no workout bundle POST was captured')
        meta = {'bytes': proxy.capture_path.stat().st_size,
                'contentType': proxy.capture_headers.get('content-type', ''),
                'authorization': proxy.capture_headers.get('authorization', '')}
        write(RUN / 'captured-bundle-meta.json', meta)
        print(json.dumps({'result': 'BUNDLE_CAPTURED', 'bytes': meta['bytes'], 'acceptance': result.get('result'),
                          'path': str(proxy.capture_path)}, ensure_ascii=False))
    finally:
        proxy.shutdown()


def failbundle(workout=None):
    """Inject one bundle-upload rejection, then retry the prepared bundle in a
    second launch with a healthy direct API."""
    state = load()
    identifier = device(state)
    proxy = BundleRejectProxy((state['host'], PROXY_PORT + 2), (state['host'], HTTP_PORT))
    threading.Thread(target=proxy.serve, daemon=True).start()
    try:
        launch('single', workout, None, port=PROXY_PORT + 2)
        failed = {}
        for _ in range(180):
            time.sleep(2)
            failed = report(state)
            if failed.get('result') != 'RUNNING':
                break
        if failed.get('result') != 'BLOCKED':
            raise RuntimeError('Bundle rejection did not produce a blocked acceptance report')
        write(RUN / 'bundle-failure-report.json', failed)
        before = taskstate_summary()
        write(RUN / 'bundle-failure-task.json', before)
    finally:
        proxy.shutdown()
    launch('single', workout, None)
    recovered = {}
    for _ in range(180):
        time.sleep(2)
        recovered = report(state)
        if recovered.get('result') != 'RUNNING':
            break
    after = taskstate_summary()
    write(RUN / 'bundle-recovery-task.json', after)
    print(json.dumps({'result': 'PREPARED_BUNDLE_RECOVERY',
                      'rejection': proxy.facts, 'failure': failed, 'recovered': recovered,
                      'before': before, 'after': after}, ensure_ascii=False))


def taskstate_summary():
    state = load(); device(state)
    namespace = state['outboxNamespace']
    destination = RUN / 'client-task-state.json'
    copy(state, 'from', f'Library/Application Support/RAIV2Sync/local-acceptance-{namespace}/state.json', destination)
    raw = json.loads(destination.read_text())
    rows = []
    for workout, task in (raw.get('tasks') or {}).items():
        metrics = task.get('metrics') or {}
        receipt = task.get('uploadReceipt') or {}
        rows.append({
            'workoutIDSHA256': hashlib.sha256(workout.encode()).hexdigest(),
            'phase': task.get('phase'),
            'bundleFilePresent': bool(task.get('bundleFile')),
            'bundleBytes': metrics.get('bundle_bytes'),
            'bundleSHA256': task.get('bundleSHA256'),
            'receiptPresent': bool(task.get('uploadReceipt')),
            'receiptBundleSHA256': receipt.get('bundleSHA256'),
            'gzipPostAttempt': metrics.get('bundle_gzip_post_attempt'),
            'gzipPostSuccess': metrics.get('bundle_gzip_post_success'),
            'gzipWireBytes': metrics.get('bundle_gzip_wire_bytes'),
        })
    return rows


def lostack():
    state = load()
    identifier = device(state)
    dropper = ManifestACKDropper((state['host'], PROXY_PORT), (state['host'], HTTP_PORT))
    threading.Thread(target=dropper.serve, daemon=True).start()
    package = json.loads((RUN / 'enrollment.json').read_text())['enrollment']
    config = {k: state[k] for k in ('environment', 'datasetSHA256', 'outboxNamespace')}
    config.update(command='completeness', baseURL=f'http://{state["host"]}:{PROXY_PORT}',
                  collectorEpoch=package['collectorEpoch'], token=package['token'])
    write(RUN / 'device-config.json', config)
    write(RUN / 'report-sentinel.json', {'result': 'RUNNING', 'command': 'completeness'})
    copy(state, 'to', RUN / 'report-sentinel.json', 'Documents/local-device-report.json')
    copy(state, 'to', RUN / 'device-config.json', 'Documents/local-device-config.json')
    execute(['xcrun', 'devicectl', 'device', 'process', 'launch', '--device', identifier, '--terminate-existing',
             '--environment-variables', '{"RAI_LOCAL_DEVICE_ACCEPTANCE":"1"}', APP_ID,
             '--json-output', RUN / 'launch-result.json'], timeout=45)
    try:
        if not dropper.dropped.wait(3600):
            raise RuntimeError('No committed manifest POST observed; lost-ACK injection did not fire')
    finally:
        # Close the listener first so no retry in this run can obtain the ACK.
        dropper.shutdown()
    write(RUN / 'lost-ack.json', {'injected': True, 'serverCommitted': True, **dropper.facts})
    for _ in range(60):
        time.sleep(2)
        try:
            result = report(state)
        except (RuntimeError, OSError, ValueError):
            continue
        if result.get('result') != 'RUNNING':
            print(json.dumps({'result': 'LOST_ACK_INJECTED', **dropper.facts,
                              'client': result, 'fullCompletenessClaimed': False}, ensure_ascii=False))
            return
    print(json.dumps({'result': 'LOST_ACK_INJECTED', **dropper.facts,
                      'client': 'RUNNING', 'fullCompletenessClaimed': False}, ensure_ascii=False))


def clientstate():
    """Counts and digests of the App's private completeness state. No identity
    or health value leaves the private run directory."""
    state = load()
    device(state)
    namespace = state['outboxNamespace']
    destination = RUN / 'client-state.json'
    copy(state, 'from', f'Library/Application Support/RAIV2Sync/local-acceptance-{namespace}/state.json', destination)
    raw = json.loads(destination.read_text())
    flow = raw.get('workoutCompleteness') or {}
    cycle = flow.get('cycle') or {}
    manifest = cycle.get('manifestID')
    classifications = {}
    for value in (cycle.get('classification') or {}).values():
        classifications[value] = classifications.get(value, 0) + 1
    summary = {
        'generation': flow.get('generation'),
        'lastManifestSHA256': flow.get('lastManifestSHA256'),
        'committedAnchorCount': len(flow.get('anchors') or {}),
        'committedInventoryCount': len(flow.get('inventory') or []),
        'committedProofCount': len(flow.get('proofs') or {}),
        'lastReceiptPresent': bool(flow.get('lastReceipt')),
        'lastQueryCounts': flow.get('lastQueryCounts'),
        'errorCode': flow.get('errorCode'),
        'cyclePresent': bool(flow.get('cycle')),
        'cycleGeneration': cycle.get('generation'),
        'cycleManifestIDSHA256': hashlib.sha256(manifest.encode()).hexdigest() if manifest else None,
        'cyclePendingFile': bool(cycle.get('pendingFile')),
        'cyclePendingSHA256': cycle.get('pendingSHA256'),
        'cycleInventoryCount': len(cycle.get('inventory') or []),
        'cycleCompletedWorkoutCount': len(cycle.get('completedWorkouts') or []),
        'cycleExclusionCount': len(cycle.get('exclusions') or {}),
        'cycleClassificationCounts': classifications,
        'cycleQueryCounts': {k: cycle.get(k) for k in ('committedAnchorQueries', 'stagedCursorQueries', 'bootstrapQueries')},
        'taskCount': len(raw.get('tasks') or {}),
    }
    print(json.dumps(summary, indent=2, ensure_ascii=False))


def stop():
    state = load()
    for mode in ('api', 'worker'):
        pid = state.get(mode + 'PID')
        if running(pid, mode):
            os.kill(pid, signal.SIGTERM)
            for _ in range(100):
                if not running(pid, mode):
                    break
                time.sleep(0.1)
            if running(pid, mode):
                raise RuntimeError('Managed local process did not stop; preserved for inspection')
    probe = subprocess.run([str(PG / 'pg_ctl'), '-D', str(RUN / 'pgdata'), 'status'], env=environment(),
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if probe.returncode == 0:
        execute([PG / 'pg_ctl', '-D', RUN / 'pgdata', '-m', 'fast', '-w', 'stop'])
    print(json.dumps({'result': 'STOPPED', 'databaseAndCredentialsPreserved': True}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare', 'reset', 'install', 'probe', 'list', 'single', 'completeness', 'daily', 'training',
                                            'workouts', 'failbundle', 'capturebundle', 'taskstate', 'lostack', 'clientstate', 'structure', 'report', 'status', 'stop'])
    parser.add_argument('--workout', help='private Workout selector for the single command')
    parser.add_argument('--streams', type=int, choices=(1, 2, 4),
                        help='acceptance-only capture concurrency for the single command')
    args = parser.parse_args()
    RUN.mkdir(parents=True, exist_ok=True, mode=0o700)
    RUN.chmod(0o700)
    # Installation, enrollment and launch mutate one controller identity. Do not
    # race them; read-only progress remains available while a build is running.
    lock = (RUN / 'controller.lock').open('a')
    if args.command not in ('report', 'status'):
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError('Another local device command is running; wait for it before changing this run')
    if args.command == 'prepare':
        prepare()
    elif args.command == 'reset':
        reset()
    elif args.command == 'install':
        install()
    elif args.command in ('probe', 'list', 'single', 'completeness', 'daily', 'training'):
        launch(args.command, getattr(args, 'workout', None), getattr(args, 'streams', None))
    elif args.command == 'structure':
        structure(getattr(args, 'workout', None))
    elif args.command == 'failbundle':
        failbundle(getattr(args, 'workout', None))
    elif args.command == 'capturebundle':
        capturebundle(getattr(args, 'workout', None))
    elif args.command == 'taskstate':
        print(json.dumps(taskstate_summary(), indent=2, ensure_ascii=False))
    elif args.command == 'workouts':
        state = load()
        copy(state, 'from', 'Documents/eligible-workouts.json', RUN / 'eligible-workouts.json')
        raw = json.loads((RUN / 'eligible-workouts.json').read_text())
        print(json.dumps([{k: row[k] for k in ('workoutID', 'durationMinutes', 'indoor')}
                          for row in raw.get('workouts', []) if isinstance(row, dict)], ensure_ascii=False))
    elif args.command == 'lostack':
        lostack()
    elif args.command == 'clientstate':
        clientstate()
    elif args.command == 'report':
        print(json.dumps(report(load()), ensure_ascii=False))
    elif args.command == 'stop':
        stop()
    else:
        state = load()
        print(json.dumps({'environment': state['environment'], 'apiRunning': running(state.get('apiPID'), 'api'),
                          'workerRunning': running(state.get('workerPID'), 'worker'), 'originalCloudAccessed': False}))


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.TimeoutExpired) as exc:
        # Subprocess stdout/stderr is private; never echo credentials or a raw
        # device object in an error traceback.
        print(json.dumps({'result': 'BLOCKED', 'reason': str(exc) if isinstance(exc, RuntimeError) else type(exc).__name__}), file=sys.stderr)
        sys.exit(1)
