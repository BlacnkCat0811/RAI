#!/usr/bin/env python3
"""UA1-R3A privacy gate.

Scans public artifacts produced by this stage for identifiers that must never
leave the device: UUIDs (workout/route/device namespaces), device identifiers,
raw metadata keys, source bundle identifiers and route identifiers.

Protected-local files that are allowed to hold raw key names live under
.local/ and are never part of public evidence; they are listed, not scanned.
"""
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
PUBLIC_TARGETS = [
    ROOT / "evidence/acceptance/device/ua1-r3a-20261105",
    ROOT / "docs/UA1_R3A_RESULT.md",
    ROOT / "contracts/workout-metadata-registry-v2.md",
]
# Raw device reports and the protected key inventory never leave this machine.
# They are counted by identifier class only, never published.
PROTECTED_TARGETS = [ROOT / ".local/ua1-r/r3a-device-reports"]

UUID_RE = re.compile(r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b")
BUNDLE_RE = re.compile(r"\bcom\.[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+\b")
RAW_KEY_RE = re.compile(r"\bWO[A-Z][a-z][A-Za-z0-9]{3,}\b")
DEVICE_ID_RE = re.compile(r"\b[0-9A-F]{8}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{4}-[0-9A-F]{12}\b")


def scan(path: pathlib.Path, findings: list) -> None:
    text = path.read_text(errors="replace")
    for name, pattern in (("uuid", UUID_RE), ("device_identifier", DEVICE_ID_RE),
                          ("source_bundle", BUNDLE_RE), ("raw_metadata_key", RAW_KEY_RE)):
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
    protected = sorted(str(p.relative_to(ROOT)) for p in (ROOT / ".local/ua1-r/private").glob("*metadata-keys*.json"))
    result = {"stage": "ua1-r3a-privacy", "scannedFiles": scanned,
              "findings": findings, "protectedLocalOnly": protected,
              "protectedIdentifierClasses": protected_classes,
              "result": "PASS" if not findings else "FAIL"}
    print(json.dumps(result, indent=1))
    return 0 if not findings else 1


if __name__ == "__main__":
    sys.exit(main())
