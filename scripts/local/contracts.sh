#!/usr/bin/env bash
# Lint the OpenAPI contract with the pinned redocly CLI.
# This is the only local step that needs Node; the Go quality gate in test.sh
# and the server CI job stay Node-free.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
umask 077

rai_require_node
rai_node_env
mkdir -p "$rai_npm_cache" "$rai_logs"

contract="$rai_root/contracts/openapi-v2.yaml"
log="$rai_logs/contracts-lint.log"

echo "== redocly $rai_redocly_version (node $rai_node_version) =="
if ! npm exec --yes --package="@redocly/cli@$rai_redocly_version" -- \
  redocly lint "$contract" >"$log" 2>&1; then
  cat "$log" >&2
  echo "OpenAPI lint failed; see $log" >&2
  exit 1
fi

warnings="$(grep -oE 'You have [0-9]+ warnings?' "$log" | grep -oE '[0-9]+' || true)"
warnings="${warnings:-0}"
tail -n 2 "$log"
echo "OpenAPI lint: 0 errors, $warnings warnings"

evidence="$rai_root/docs/evidence/local-contracts-lint.json"
python3 - "$evidence" "$contract" "$log" "$warnings" "$rai_redocly_version" "$rai_node_version" <<'PY'
import hashlib, json, pathlib, sys, datetime
evidence, contract, log, warnings, redocly, node = sys.argv[1:7]
contract_path = pathlib.Path(contract)
log_path = pathlib.Path(log)
data = {
    "generated_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "command": "npm exec --yes --package=@redocly/cli@%s -- redocly lint contracts/openapi-v2.yaml" % redocly,
    "node": node,
    "redocly": redocly,
    "contract": "contracts/openapi-v2.yaml",
    "contract_sha256": hashlib.sha256(contract_path.read_bytes()).hexdigest(),
    "errors": 0,
    "warnings": int(warnings),
    "result": "PASS",
    "log_sha256": hashlib.sha256(log_path.read_bytes()).hexdigest(),
    "note": "Static contract lint only. It proves the OpenAPI document is valid and "
            "self-consistent; it does not prove the Go handlers implement it. Warnings are the "
            "pre-existing baseline and are not suppressed or fixed here.",
}
pathlib.Path(evidence).write_text(json.dumps(data, indent=2) + "\n")
print(evidence)
PY

echo "CONTRACTS LINT: PASS"
