#!/usr/bin/env bash
# Synthetic-only formal App -> HTTP -> isolated PG18 -> Worker -> candidate.
# Also re-runs the frozen B2 App E2E. Does not touch any configured database.
set -euo pipefail
umask 077
source "$(dirname "$0")/local/lib.sh"
rai_require_toolchain
rai_go_env
: "${DEVELOPER_DIR:?Set DEVELOPER_DIR to Xcode.app/Contents/Developer}"
: "${RAI_M4B4_SIMULATOR:?Select an isolated synthetic iOS Simulator UUID}"
cd "$rai_root"
output="$rai_root/.local/m4b4/derived"
xcodebuild build-for-testing -project ios/RAI/RAI.xcodeproj -scheme RAI \
  -destination "platform=iOS Simulator,id=$RAI_M4B4_SIMULATOR" \
  -derivedDataPath "$output" RAI_DEV_HTTP_HOST=127.0.0.1 \
  CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- -quiet
# Ad-hoc simulator signing is necessary for the actual background file uploader.
set -- "$output/Build/Products/"*.xctestrun
[[ $# == 1 && -f "$1" ]] || { echo 'expected exactly one built xctestrun' >&2; exit 1; }
export RAI_M4B4_XCTESTRUN="$1"
unset RAI_M4B4_RECIPE
for group in 0 1; do
  RAI_M4B4_GROUP="$group" PGBIN="$rai_pgbin" scripts/test-postgres.sh \
    go test -count=1 -timeout=25m -tags=integration,m4b4sim ./integration \
    -run '^TestB4SimulatorVO2FullE2E$' -v
done
RAI_M4B2_XCTESTRUN="$RAI_M4B4_XCTESTRUN" RAI_M4B2_SIMULATOR="$RAI_M4B4_SIMULATOR" \
  PGBIN="$rai_pgbin" scripts/test-postgres.sh go test -race -count=1 -timeout=15m \
  -tags=integration,m4b2sim ./integration -run '^TestVO2SimulatorAppToIsolatedPG18$' -v
