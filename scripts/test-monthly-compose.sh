#!/usr/bin/env bash
# Reuse the full P7 synthetic TLS/P3/P4/P5 fixture and regression.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
export RAI_MONTHLY_REHEARSAL=1
exec bash "$root/scripts/test-p7-compose.sh"
