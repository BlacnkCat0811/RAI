#!/usr/bin/env python3
"""Synthetic RAI P3 HTTP smoke flow. No real health data or credentials are logged."""
import gzip
import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid

BASE = os.environ["RAI_E2E_URL"].rstrip("/")
TOKEN = os.environ["RAI_E2E_TOKEN"]
COLLECTOR = os.environ["RAI_E2E_COLLECTOR"]
STATE = os.environ.get("RAI_E2E_STATE_DIR", "/tmp/rai-p3-e2e")
HR = "HKQuantityTypeIdentifierHeartRate"
WORKOUT = "HKWorkoutTypeIdentifier"
PROTO = 2


def req(method, path, body=None, ctype="application/json", encoding=None):
    headers = {"Authorization": "Bearer " + TOKEN}
    if ctype:
        headers["Content-Type"] = ctype
    if encoding:
        headers["Content-Encoding"] = encoding
    request = urllib.request.Request(BASE + path, data=body, headers=headers, method=method)
    for attempt in range(4):
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as exc:
            payload = exc.read()
            if exc.code == 429 and attempt < 3:
                try:
                    delay = int(json.loads(payload).get("retryAfterSeconds", 1))
                except Exception:
                    delay = 1
                time.sleep(max(1, delay))
                continue
            raise RuntimeError(f"{method} {path} -> {exc.code}: {payload[:300]!r}") from None


def post_json(path, value):
    status, body = req("POST", path, json.dumps(value, separators=(",", ":")).encode())
    if status != 200:
        raise RuntimeError(f"{path} status {status}")
    return body


def gzip_lines(values):
    decoded = b"".join((json.dumps(v, separators=(",", ":")) + "\n").encode() for v in values)
    return gzip.compress(decoded, compresslevel=6), decoded


def fingerprint(label):
    return "sha256:" + hashlib.sha256(label.encode()).hexdigest()


def register_stream(dataset, epoch, sample_type, label):
    stream_id = str(uuid.uuid4())
    fp = fingerprint("stream-" + stream_id)
    post_json("/v2/sync/streams", {"datasetID": dataset, "datasetEpoch": epoch, "collectorEpoch": COLLECTOR,
                                  "streamID": stream_id, "sampleType": sample_type, "queryFingerprint": fp})
    return stream_id


def envelope(dataset, epoch, stream, batch, seq, page, events, wire, decoded):
    return {"protocolVersion": PROTO, "datasetID": dataset, "datasetEpoch": epoch, "collectorEpoch": COLLECTOR,
            "streamID": stream, "batchID": batch, "clientSeq": seq, "previousBatchID": None, "pageID": page,
            "fragmentIndex": 0, "fragmentCount": 1, "queryFingerprint": fingerprint("stream-" + stream),
            "contentEncoding": "gzip", "wireBytes": len(wire), "decodedBytes": len(decoded), "eventCount": len(events),
            "payloadSHA256": hashlib.sha256(wire).hexdigest()}


def upload_batch(dataset, epoch, stream, sample_type, events, seq, previous=None):
    wire, decoded = gzip_lines(events)
    batch = str(uuid.uuid4())
    env = envelope(dataset, epoch, stream, batch, seq, str(uuid.uuid4()), events, wire, decoded)
    env["previousBatchID"] = previous
    # stream identity uses a fixed fingerprint in register_stream; keep it exact.
    # The helper below receives it through a small local lookup.
    return env, wire


def run_upload():
    os.makedirs(STATE, mode=0o700, exist_ok=True)
    status, body = req("GET", "/v2/handshake")
    if status != 200:
        raise RuntimeError("handshake failed")
    handshake = json.loads(body)
    if not handshake["capabilities"]["routes"]:
        raise RuntimeError("routes capability is not enabled")
    dataset, epoch = handshake["datasetID"], handshake["datasetEpoch"]

    sample_stream = register_stream(dataset, epoch, HR, "p3-samples")
    sample_value = 120.0
    sample_start = 1793059200000000
    samples = [{"op": "upsert", "kind": "sample", "type": HR, "uuid": str(uuid.uuid4()),
                "startUs": sample_start + i * 1000000, "endUs": sample_start + i * 1000000,
                "quantity": {"value": sample_value + (i % 20), "unit": "count/min"},
                "source": {"bundleID": "synthetic.p3.e2e"}} for i in range(1000)]
    wire, decoded = gzip_lines(samples)
    batch = str(uuid.uuid4())
    env = envelope(dataset, epoch, sample_stream, batch, 1, str(uuid.uuid4()), samples, wire, decoded)
    post_json("/v2/sync/batches", env)
    status, receipt = req("PUT", f"/v2/sync/batches/{batch}/body", wire,
                          "application/x-rai-health-delta+ndjson", "gzip")
    if status != 200 or json.loads(receipt)["ingestStatus"] != "committed":
        raise RuntimeError("sample batch not durable")
    with open(os.path.join(STATE, "sample-envelope.json"), "wb") as f:
        f.write(json.dumps(env, separators=(",", ":")).encode())
    with open(os.path.join(STATE, "sample-body.gz"), "wb") as f:
        f.write(wire)
    with open(os.path.join(STATE, "sample-receipt.json"), "wb") as f:
        f.write(receipt)

    workout_stream = register_stream(dataset, epoch, WORKOUT, "p3-workout")
    workout_start = 1793059200000000
    duration_us = 45 * 60 * 1000000
    workout_uuid = str(uuid.uuid4())
    workout = {"op": "upsert", "kind": "workout", "type": WORKOUT, "uuid": workout_uuid,
               "startUs": workout_start, "endUs": workout_start + duration_us,
               "source": {"bundleID": "synthetic.p3.e2e"}, "metadata": {"synthetic": True},
               "workout": {"activityType": 37, "durationUs": duration_us}}
    wire, decoded = gzip_lines([workout])
    workout_batch = str(uuid.uuid4())
    env = envelope(dataset, epoch, workout_stream, workout_batch, 1, str(uuid.uuid4()), [workout], wire, decoded)
    post_json("/v2/sync/batches", env)
    status, _ = req("PUT", f"/v2/sync/batches/{workout_batch}/body", wire,
                    "application/x-rai-health-delta+ndjson", "gzip")
    if status != 200:
        raise RuntimeError("workout batch not durable")

    # Route arrives after the Workout ACK, independently.
    route_id = str(uuid.uuid4())
    point_count, end_us = 5000, workout_start + duration_us - 1
    step = max(1, (end_us - workout_start) // point_count)
    last_us = workout_start + (point_count - 1) * step
    manifest = {"protocolVersion": PROTO, "datasetID": dataset, "datasetEpoch": epoch, "collectorEpoch": COLLECTOR,
                "routeID": route_id, "workoutID": workout_uuid, "source": {"bundleID": "synthetic.p3.e2e"},
                "expectedChunkCount": 1, "expectedPointCount": point_count,
                "firstRecordedAtUs": workout_start, "lastRecordedAtUs": last_us}
    post_json("/v2/routes", manifest)
    points = [{"sequence": i, "recordedAtUs": workout_start + i * step, "latitude": 31.23 + i * 0.000001,
               "longitude": 121.47 + i * 0.000001, "altitude": 4.0, "horizontalAccuracy": 5.0,
               "speed": 2.5, "course": float(i % 360)} for i in range(point_count)]
    route_wire, _ = gzip_lines(points)
    status, chunk_body = req("PUT", f"/v2/routes/{route_id}/chunks/0", route_wire,
                             "application/x-rai-health-route+ndjson", "gzip")
    if status != 200:
        raise RuntimeError("route chunk not durable")
    chunk = json.loads(chunk_body)
    seal = {"protocolVersion": PROTO, "chunkCount": 1, "totalPoints": point_count,
            "manifestSHA256": chunk["manifestSHA256"], "payloadManifestSHA256": chunk["payloadManifestSHA256"]}
    status, sealed_body = req("POST", f"/v2/routes/{route_id}/seal",
                              json.dumps(seal, separators=(",", ":")).encode())
    if status != 200 or json.loads(sealed_body)["ingestStatus"] != "sealed":
        raise RuntimeError("route seal failed")

    association = {"protocolVersion": PROTO, "datasetID": dataset, "datasetEpoch": epoch,
                   "collectorEpoch": COLLECTOR, "associationID": str(uuid.uuid4()), "sampleType": HR,
                   "sampleUUID": samples[0]["uuid"], "workoutUUID": workout_uuid,
                   "source": {"bundleID": "synthetic.p3.e2e"}}
    status, assoc_body = req("POST", "/v2/sync/associations",
                             json.dumps(association, separators=(",", ":")).encode())
    if status != 200:
        raise RuntimeError("association failed")
    enrichment = {"protocolVersion": PROTO, "datasetID": dataset, "datasetEpoch": epoch,
                  "collectorEpoch": COLLECTOR, "enrichmentID": str(uuid.uuid4()), "workoutID": workout_uuid,
                  "source": {"bundleID": "synthetic.p3.e2e"}, "sourceVersion": 1,
                  "observedAtUs": workout_start + duration_us + 1000,
                  "routeAvailable": True, "metadata": {"synthetic": True}}
    status, enrich_body = req("POST", "/v2/sync/workout-enrichments",
                              json.dumps(enrichment, separators=(",", ":")).encode())
    if status != 200:
        raise RuntimeError("enrichment failed")
    status, route_body = req("GET", f"/v2/routes/{route_id}")
    if status != 200 or json.loads(route_body)["ingestStatus"] != "sealed":
        raise RuntimeError("route state not published")
    print(json.dumps({"samples": len(samples), "routePoints": point_count, "routeChunks": 1,
                      "association": json.loads(assoc_body)["ingestStatus"],
                      "enrichment": json.loads(enrich_body)["ingestStatus"],
                      "ack": "durable"}, separators=(",", ":")))


def run_retry():
    with open(os.path.join(STATE, "sample-envelope.json"), "rb") as f:
        env = json.loads(f.read())
    with open(os.path.join(STATE, "sample-body.gz"), "rb") as f:
        wire = f.read()
    with open(os.path.join(STATE, "sample-receipt.json"), "rb") as f:
        original = f.read()
    status, body = req("PUT", f"/v2/sync/batches/{env['batchID']}/body", wire,
                       "application/x-rai-health-delta+ndjson", "gzip")
    if status != 200 or body != original:
        raise RuntimeError("retry after restart did not return the original receipt")
    print(json.dumps({"retry": "same-receipt"}, separators=(",", ":")))


if __name__ == "__main__":
    if sys.argv[1:] == ["retry"]:
        run_retry()
    elif sys.argv[1:] == ["upload"]:
        run_upload()
    else:
        raise SystemExit("usage: p3-e2e-client.py upload|retry")
