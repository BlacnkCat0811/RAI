#!/usr/bin/env python3
"""Synthetic mixed page and three staging retries against the real Compose API.

Private replay material lives only in the disposable test state directory.
Output is counts and status; no payloads or credentials enter evidence.
"""
import gzip
import importlib.util
import json
import pathlib
import sys
import uuid

spec = importlib.util.spec_from_file_location('p3_client', pathlib.Path(__file__).with_name('p3-e2e-client.py'))
p3 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(p3)
state = pathlib.Path(p3.STATE)
uid = lambda: str(uuid.uuid4())


def put(path, wire, media):
    status, body = p3.req('PUT', path, wire, media, 'gzip')
    assert status == 200
    return body


if sys.argv[1] == 'prepare':
    env = json.loads((state / 'sample-envelope.json').read_text())
    samples = [json.loads(line) for line in gzip.decompress((state / 'sample-body.gz').read_bytes()).splitlines()]
    events = [dict(samples[1], uuid=uid()) for _ in range(999)]
    events.append({'op': 'delete', 'kind': 'sample', 'type': p3.HR, 'uuid': samples[0]['uuid']})
    wire, decoded = p3.gzip_lines(events)
    mixed = p3.envelope(env['datasetID'], env['datasetEpoch'], env['streamID'], uid(), 2, uid(), events, wire, decoded)
    mixed['previousBatchID'] = env['batchID']
    p3.post_json('/v2/sync/batches', mixed)
    path = '/v2/sync/batches/' + mixed['batchID'] + '/body'
    receipt = put(path, wire, 'application/x-rai-health-delta+ndjson')
    parsed = json.loads(receipt)
    assert parsed['counts']['inserted'] == 999 and parsed['counts']['deleted'] == 1 and len(parsed['analysisJobIDs']) == 1
    retries = [{'kind': 'mixed', 'path': path, 'media': 'application/x-rai-health-delta+ndjson', 'wire': wire.hex(), 'receipt': receipt.hex()}]
    base = {k: env[k] for k in ['datasetID', 'datasetEpoch', 'collectorEpoch']}
    series, package, route = uid(), uid(), uid()
    stamp = samples[1]['startUs']
    p3.post_json('/v2/sync/series', dict(base, seriesID=series, sampleType=p3.HR, sampleUUID=samples[1]['uuid']))
    series_wire, _ = p3.gzip_lines([{'startUs': stamp, 'endUs': stamp, 'value': 120, 'unit': 'count/min'}])
    package_start, package_end = stamp, stamp + 60_000000
    p3.post_json('/v2/sync/workout-packages', dict(base, workoutID=package, startUs=package_start, endUs=package_end,
                                                source={'bundleID': 'synthetic.maintenance'}, workout={'activityType': 37, 'durationUs': package_end-package_start}))
    package_wire, _ = p3.gzip_lines([{'kind': 'event', 'ordinal': 0, 'event': {'type': 1, 'startUs': package_start, 'endUs': package_start}}])
    for kind, identity, body, media in [('series', series, series_wire, 'application/x-rai-health-series+ndjson'), ('workout-packages', package, package_wire, 'application/x-rai-health-workout-detail+ndjson')]:
        path = '/v2/sync/' + kind + '/' + identity + '/chunks/0'
        receipt = put(path, body, media)
        assert put(path, body, media) == receipt
        r = json.loads(receipt)
        seal = {'chunkCount': 1, 'manifestSHA256': r['manifestSHA256']}
        seal['totalPoints' if kind == 'series' else 'totalDetails'] = 1
        p3.post_json('/v2/sync/' + kind + '/' + identity + '/seal', seal)
        retries.append({'kind': kind, 'path': path, 'media': media, 'wire': body.hex(), 'receipt': receipt.hex()})
    p3.post_json('/v2/routes', dict(base, protocolVersion=2, routeID=route, workoutID=package, source={'bundleID':'synthetic.maintenance'},
                                 expectedChunkCount=1, expectedPointCount=2, firstRecordedAtUs=stamp, lastRecordedAtUs=stamp+1_000000))
    route_wire, _ = p3.gzip_lines([{'sequence': i, 'recordedAtUs': stamp+i*1_000000, 'latitude':31.23+i*.000001, 'longitude':121.47, 'horizontalAccuracy':5} for i in range(2)])
    path = '/v2/routes/' + route + '/chunks/0'
    receipt = put(path, route_wire, 'application/x-rai-health-route+ndjson')
    assert put(path, route_wire, 'application/x-rai-health-route+ndjson') == receipt
    r = json.loads(receipt)
    p3.post_json('/v2/routes/' + route + '/seal', {'protocolVersion':2, 'chunkCount':1, 'totalPoints':2, 'manifestSHA256':r['manifestSHA256'], 'payloadManifestSHA256':r['payloadManifestSHA256']})
    retries.append({'kind':'route', 'path':path, 'media':'application/x-rai-health-route+ndjson', 'wire':route_wire.hex(), 'receipt':receipt.hex()})
    (state / 'maintenance-retries.json').write_text(json.dumps(retries))
    print('MAINTENANCE_COMPOSE_PREPARE=PASS events=1000 sample_jobs=1 types=route,series,package pending_retries=PASS sealed=PASS')
elif sys.argv[1] == 'retry':
    for retry in json.loads((state / 'maintenance-retries.json').read_text()):
        assert put(retry['path'], bytes.fromhex(retry['wire']), retry['media']) == bytes.fromhex(retry['receipt'])
    print('MAINTENANCE_COMPOSE_RETRY=PASS mixed,route,series,package original receipts with new valid leases')
else:
    raise SystemExit('usage: prepare|retry')
