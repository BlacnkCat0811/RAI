#!/usr/bin/env python3
"""Fail closed on client capabilities that the RAI product contract forbids.

This is a source-level guard, not a substitute for runtime permission review.
It keeps the read-only HealthKit, no-WorkoutKit-mutation, no-notification and
active-only product boundaries visible in every local gate.
"""
from pathlib import Path
import sys

root = Path(__file__).resolve().parent.parent
files = [p for base in (root / "ios/RAI/App", root / "ios/RAI/Sources")
         if base.exists() for p in base.rglob("*.swift")]
problems = []
for p in files:
    rel = p.relative_to(root)
    text = p.read_text(errors="replace")
    for pat in ("HKHealthStore().save", "saveObject(", "deleteObject(",
                "import WorkoutKit", "WKWorkoutScheduler", "WorkoutScheduler",
                "UNUserNotificationCenter", "import UserNotifications",
                "channel=candidate", "/candidate"):
        if pat in text:
            problems.append(f"{rel}: forbidden client API {pat!r}")
    for line in text.splitlines():
        s = line.strip()
        if "requestAuthorization(toShare:" in s and "toShare: []" not in s:
            problems.append(f"{rel}: HealthKit authorization must be read-only: {s}")
if problems:
    print("PRODUCT BOUNDARY FAIL")
    print("\n".join(problems))
    sys.exit(1)
print(f"PRODUCT BOUNDARY PASS files={len(files)} readOnlyHealthKit=true workoutKitMutation=false notifications=false candidateAccess=false")
