#!/usr/bin/env bash
# Explicit environment, same release Compose and read-only backup role as P7.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
exec python3 "$root/scripts/rai-backup.py" "$@"
