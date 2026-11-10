#!/usr/bin/env bash
# Historical entry point; fail before contacting SSH, Docker or a database.
set -euo pipefail
echo 'fnOS/N100 test execution is disabled. Use scripts/local/test.sh on this Mac.' >&2
exit 2
