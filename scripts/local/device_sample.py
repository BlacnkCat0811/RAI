#!/usr/bin/env python3
"""Repeat one real-device first-import measurement and collect sanitized samples.

Each sample resets only the disposable local dataset, so the iPhone performs a
genuine first capture and first import of the same real, read-only Workout. No
HealthKit record, phone state or cloud data is touched, and the saved reports
contain counts, durations and byte sizes only.

Example:
  RAI_LOCAL_DEVICE_RUN=$PWD/.local/device-acceptance-speed \
  RAI_LOCAL_DEVICE_PG_PORT=55434 RAI_LOCAL_DEVICE_HTTP_PORT=18082 \
  RAI_LOCAL_DEVICE_CONFIGURATION=Release \
  python3 scripts/local/device_sample.py --profile s60 --workout <uuid> --samples 10
"""
import argparse
import json
import pathlib
import statistics
import subprocess
import sys
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
RUN = pathlib.Path(__import__('os').environ.get('RAI_LOCAL_DEVICE_RUN') or (ROOT / '.local/device-acceptance')).resolve()
DEVICE = pathlib.Path(__file__).with_name('device.py')


def call(*arguments, timeout=600):
    result = subprocess.run([sys.executable, str(DEVICE), *arguments], cwd=ROOT, capture_output=True, text=True,
                            timeout=timeout, env=__import__('os').environ)
    if result.returncode:
        raise RuntimeError(f'device.py {arguments[0]} failed: {result.stderr.strip()[:200]}')
    return result.stdout.strip()


def parse(text):
    for line in reversed(text.splitlines()):
        line = line.strip()
        if line.startswith('{'):
            return json.loads(line)
    raise RuntimeError('no JSON result')


def percentile(values, fraction):
    if not values:
        return None
    ordered = sorted(values)
    rank = max(1, min(len(ordered), int(-(-fraction * len(ordered) // 1))))
    return ordered[rank - 1]


def measure(profile, workout, samples, reset, poll_seconds, streams=None):
    target = RUN / 'samples' / profile
    target.mkdir(parents=True, exist_ok=True)
    collected = []
    for index in range(samples):
        if reset:
            call('reset', timeout=300)
        extra = ['--streams', str(streams)] if streams is not None else []
        call('single', '--workout', workout, *extra, timeout=120)
        deadline = time.time() + poll_seconds
        report = {}
        while time.time() < deadline:
            time.sleep(2)
            report = parse(call('report', timeout=120))
            if report.get('result') != 'RUNNING':
                break
        path = target / f'{index:02d}.json'
        path.write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
        collected.append(report)
        timings = report.get('timings') or {}
        print(json.dumps({'sample': index, 'result': report.get('result'), 'reason': report.get('reason'),
                          'workout_sync_ms': timings.get('workout_sync_ms'),
                          'healthkit_read_ms': timings.get('healthkit_read_ms'),
                          'bundle_build_ms': timings.get('bundle_build_ms'),
                          'upload_ms': timings.get('upload_ms'),
                          'bundle_bytes': timings.get('bundle_bytes'),
                          'peak_rss': report.get('processPeakRssBytes'),
                          'max_stall_ms': report.get('mainThreadMaxStallMs')}), flush=True)
    return collected


def summarize(profile, collected):
    ok = [r for r in collected if r.get('result') == 'SINGLE_WORKOUT_ACKNOWLEDGED']
    print(f'# profile={profile} samples={len(collected)} acknowledged={len(ok)}')
    for key in ('workout_sync_ms', 'healthkit_read_ms', 'bundle_build_ms', 'upload_ms'):
        values = [r['timings'][key] for r in ok if isinstance(r.get('timings', {}).get(key), (int, float))]
        if not values:
            continue
        print(json.dumps({'metric': key, 'n': len(values), 'median': round(statistics.median(values), 2),
                          'min': round(min(values), 2), 'max': round(max(values), 2),
                          'p95': round(percentile(values, 0.95), 2) if len(values) >= 20 else 'NOT SIGNED (n<20)'}))
    rss = [r['processPeakRssBytes'] for r in ok if isinstance(r.get('processPeakRssBytes'), int)]
    if rss:
        print(json.dumps({'metric': 'processPeakRssBytes', 'n': len(rss), 'median': int(statistics.median(rss)),
                          'max': max(rss), 'medianMiB': round(statistics.median(rss) / 1048576, 1)}))
    stalls = [r['mainThreadMaxStallMs'] for r in ok if isinstance(r.get('mainThreadMaxStallMs'), (int, float))]
    if stalls:
        print(json.dumps({'metric': 'mainThreadMaxStallMs', 'n': len(stalls), 'max': max(stalls),
                          'over250Samples': sum(1 for r in ok if (r.get('mainThreadStallsOver250') or 0) > 0)}))
    failures = [r for r in collected if r.get('result') != 'SINGLE_WORKOUT_ACKNOWLEDGED']
    for failure in failures:
        print(json.dumps({'failure': failure.get('result'), 'reason': failure.get('reason'),
                          'stage': failure.get('stage'), 'systemCode': failure.get('systemCode')}))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--profile', required=True)
    parser.add_argument('--workout', required=True)
    parser.add_argument('--samples', type=int, default=10)
    parser.add_argument('--no-reset', action='store_true')
    parser.add_argument('--poll-seconds', type=int, default=240)
    parser.add_argument('--streams', type=int, choices=(1, 2, 4))
    args = parser.parse_args()
    collected = measure(args.profile, args.workout, args.samples, not args.no_reset, args.poll_seconds, args.streams)
    summarize(args.profile, collected)


if __name__ == '__main__':
    main()
