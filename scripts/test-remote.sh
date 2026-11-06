#!/usr/bin/env bash
# Historical entry point; remote execution is no longer authorized.
set -euo pipefail
echo 'Remote tests are disabled. Use scripts/local/test.sh quick|integration|full|e2e|perf on this Mac.' >&2
exit 2
