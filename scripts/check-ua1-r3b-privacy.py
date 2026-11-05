#!/usr/bin/env python3
"""UA1-R3B privacy gate.

Scans the public artifacts produced by this stage for identifiers that must
never leave the device: Workout/route/device UUIDs, device identifiers, source
bundle identifiers, raw metadata keys and health values.

Inventory evidence is published as counts, hashes and classification
distribution only; a public file that names a single Workout identity fails the
gate. Protected-local files under .local/ are counted by identifier class and
never published.
"""
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PUBLIC_TARGETS = [
    ROOT / "evidence/acceptance/device/ua1-r3b-20261105",
    ROOT / "docs/UA1_R3B_RESULT.md",
    ROOT / "contracts/workout-source-policy-v1.md",
    ROOT / "contracts/workout-completeness-v2.md",
]
PROTECTED_TARGETS = [ROOT / ".local/ua1-r/r3b-device-reports"]

UUID_RE = re.compile(r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b")
BUNDLE_RE = re.compile(r"\bcom\.[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+\b")
RAW_KEY_RE = re.compile(r"\bWO[A-Z][a-z][A-Za-z0-9]{3,}\b")
DEVICE_ID_RE = re.compile(r"\b[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\b")
HEALTH_VALUE_RE = re.compile(r'"(heartRate|averageHeartRate|distanceM|activeEnergy|quantity|value)"\s*:\s*-?\d')


def scan(path: pathlib.Path, findings: list) -> None:
    text = path.read_text(errors="replace")
    for name, pattern in (("uuid", UUID_RE), ("device_identifier", DEVICE_ID_RE),
                          ("source_bundle", BUNDLE_RE), ("raw_metadata_key", RAW_KEY_RE),
                          ("health_value", HEALTH_VALUE_RE)):
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
    result = {"stage": "ua1-r3b-privacy", "scannedFiles": scanned,
              "findings": findings, "protectedIdentifierClasses": protected_classes,
              "result": "PASS" if not findings else "FAIL"}
    print(json.dumps(result, indent=1))
    return 0 if not findings else 1


if __name__ == "__main__":
    sys.exit(main())
