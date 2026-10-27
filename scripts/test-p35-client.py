#!/usr/bin/env python3
"""Drive the Swift synthetic probe against p35-client-host.sh over SSH.

Uses private temporary config; prints only the probe's redacted statistics.
The killed processes are macOS test processes, never a real iPhone App.
"""
import argparse
import json
import os
import pathlib
import selectors
import shlex
import socket
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser()
parser.add_argument('host_log', type=pathlib.Path)
parser.add_argument('--host', default='rai-fnos')
args = parser.parse_args()
metadata = next(json.loads(line.split('=', 1)[1]) for line in args.host_log.read_text().splitlines() if line.startswith('P35_CLIENT_READY='))
ssh = ['ssh', '-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', args.host]
root = pathlib.Path(__file__).resolve().parents[1]
probe = root / 'ios/RAI/.build/debug/SyncProbe'
assert probe.is_file(), 'Build SyncProbe first'
with tempfile.TemporaryDirectory(prefix='rai-p35-synthetic-client-') as directory:
    directory = pathlib.Path(directory)
    os.chmod(directory, 0o700)
    config = json.loads(subprocess.check_output(ssh + ['cat ' + shlex.quote(metadata['configuration'])]))
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    tunnel = subprocess.Popen(['ssh', '-N', '-o', 'BatchMode=yes', '-o', 'ExitOnForwardFailure=yes', '-L', f'127.0.0.1:{port}:127.0.0.1:{metadata["port"]}', args.host], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        for _ in range(100):
            if tunnel.poll() is not None:
                raise RuntimeError('SSH tunnel failed')
            try:
                with socket.create_connection(('127.0.0.1', port), timeout=0.2):
                    break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError('SSH tunnel not listening')
        config['baseURL'] = f'http://127.0.0.1:{port}'
        private = directory / 'client.json'
        private.write_text(json.dumps(config)); os.chmod(private, 0o600)
        for mode in ['prepare-kill', 'resume', 'workout', 'route-kill', 'route-resume', 'relationships', 'empty']:
            command = [str(probe), mode, str(private), str(directory / 'outbox')]
            started = time.monotonic()
            if mode.endswith('-kill'):
                child = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
                selector = selectors.DefaultSelector(); selector.register(child.stdout, selectors.EVENT_READ)
                ready = False
                try:
                    while time.monotonic() - started < 180:
                        if selector.select(timeout=1):
                            line = os.read(child.stdout.fileno(), 65536).decode()
                            print(line, end='', flush=True)
                            if 'PROBE_KILL_READY=' in line:
                                ready = True; break
                            if not line:
                                break
                    assert ready, 'Synthetic kill boundary not reached'
                    child.kill(); assert child.wait(timeout=10) == -9
                    print('PROBE_MACOS_SIGKILL=PASS ' + mode, flush=True)
                finally:
                    selector.close()
                    if child.poll() is None:
                        child.kill(); child.wait()
            else:
                subprocess.run(command, check=True, timeout=240)
            print(json.dumps({'probe': mode, 'elapsed_ms': round((time.monotonic() - started) * 1000, 3)}), flush=True)
    finally:
        tunnel.terminate(); tunnel.wait(timeout=10)
        # Let the isolated host verify counts and tear down its API/database.
        subprocess.run(ssh + ['touch ' + shlex.quote(metadata['done'])], check=True)
