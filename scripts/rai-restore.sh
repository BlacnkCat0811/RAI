#!/usr/bin/env bash
# This task authorizes isolated drills only. Never overwrites an existing DB.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
exec python3 "$root/scripts/rai-restore.py" "$@"
