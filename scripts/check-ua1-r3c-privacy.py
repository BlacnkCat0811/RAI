#!/usr/bin/env python3
"""UA1-R3C privacy gate.

Scans every public artifact produced by the UA1-R3C stage for identifiers that
must never leave the device: Workout/route/device UUIDs, device identifiers,
source bundle identifiers, raw metadata keys, anchors, receipts and health
values.

Public evidence is published as counts, SHA-256 digests, classification
distributions, reasons, statuses, booleans and durations only. Protected-local
files under .local/ are counted by identifier class and never published.

This gate is specific to this stage: the R3B gate does not cover the R3C
report, the R3C evidence directory or the R3C fault-hook counters.
"""
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PUBLIC_TARGETS = [
    ROOT / "evidence/acceptance/device/ua1-r3c-20261106",
    ROOT / "docs/UA1_R3C_RESULT.md",
    ROOT / "docs/IMPLEMENTATION.md",
]
PROTECTED_TARGETS = [ROOT / ".local/ua1-r3c/private"]
# Shared append-only documents are scanned from this stage's own section only:
# content written by earlier stages is not this gate's product.
SECTION_TARGETS = {ROOT / "docs/IMPLEMENTATION.md": "## UA1-R3C"}

UUID_RE = re.compile(r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b")
BUNDLE_RE = re.compile(r"\bcom\.[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+\b")
RAW_KEY_RE = re.compile(r"\bWO[A-Z][a-z][A-Za-z0-9]{3,}\b")
DEVICE_ID_RE = re.compile(r"\b[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\b")
UDID_RE = re.compile(r"\b[0-9A-F]{8}-[0-9A-F]{16}\b")
HEALTH_VALUE_RE = re.compile(
    r'"(heartRate|averageHeartRate|maxHeartRate|distanceM|activeEnergy|'
    r'quantity|value|bpm|vo2Max|restingHeartRate|hrvMs)"\s*:\s*-?\d')
ANCHOR_RE = re.compile(r'"(anchor|anchors|anchorToken|pageSHA256|receipt|receiptBytes)"\s*:\s*"[^"]{8,}')
LONG_B64_RE = re.compile(r"\b[A-Za-z0-9+/]{120,}={0,2}\b")


def scan(path: pathlib.Path, findings: list) -> None:
    text = path.read_text(errors="replace")
    marker = SECTION_TARGETS.get(path)
    if marker is not None:
        index = text.find(marker)
        if index < 0:
            return
        text = text[index:]
    checks = (
        ("uuid", UUID_RE),
        ("device_identifier", DEVICE_ID_RE),
        ("device_udid", UDID_RE),
        ("source_bundle", BUNDLE_RE),
        ("raw_metadata_key", RAW_KEY_RE),
        ("health_value", HEALTH_VALUE_RE),
        ("anchor_or_receipt", ANCHOR_RE),
        ("opaque_blob", LONG_B64_RE),
    )
    for name, pattern in checks:
        for match in pattern.finditer(text):
            line = text[: match.start()].count("\n") + 1
            findings.append({"file": str(path.relative_to(ROOT)), "line": line, "class": name})


def main() -> int:
    findings: list = []
    scanned = 0
    for target in PUBLIC_TARGETS:
        if not target.exists():
            continue
        paths = [target] if target.is_file() else sorted(p for p in target.rglob("*") if p.is_file())
        for path in paths:
            scan(path, findings)
            scanned += 1
    protected_classes: dict = {}
    for target in PROTECTED_TARGETS:
        if not target.exists():
            continue
        for path in sorted(p for p in target.rglob("*") if p.is_file()):
            local: list = []
            scan(path, local)
            for item in local:
                protected_classes[item["class"]] = protected_classes.get(item["class"], 0) + 1
    result = {
        "schema": "rai.ua1-r3c.privacy/1",
        "stage": "ua1-r3c-privacy",
        "scannedFiles": scanned,
        "publicTargets": [str(t.relative_to(ROOT)) for t in PUBLIC_TARGETS if t.exists()],
        "findings": findings,
        "protectedIdentifierClasses": protected_classes,
        "result": "PASS" if not findings else "FAIL",
    }
    print(json.dumps(result, indent=1))
    return 0 if not findings else 1


if __name__ == "__main__":
    sys.exit(main())
