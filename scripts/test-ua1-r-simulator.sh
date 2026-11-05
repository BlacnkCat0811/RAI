#!/usr/bin/env bash
# Synthetic App XCTest regression; preserves all UA0/UA1 evidence.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
: "${DEVELOPER_DIR:?Set the full Xcode Developer directory}"
: "${RAI_UA1_R_SIMULATOR:?Select a synthetic iOS Simulator}"
mkdir -p "$root/.local/ua1-r"
derived="$root/.local/ua1-r/simulator-derived"
result_root="$(mktemp -d "$root/.local/ua1-r/simulator.XXXXXX")"
xcodebuild build-for-testing -project "$root/ios/RAI/RAI.xcodeproj" -scheme RAI \
  -destination "platform=iOS Simulator,id=$RAI_UA1_R_SIMULATOR" -derivedDataPath "$derived" \
  CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- -quiet
python3 - "$derived" "$root/contracts/fixtures/athlete-model" <<'PY'
import pathlib, plistlib, sys
root = pathlib.Path(sys.argv[1])
files = [p for p in (root/'Build/Products').glob('*.xctestrun') if p.name != 'ua1-r.xctestrun']
assert len(files) == 1, 'expected one generated xctestrun'
value = plistlib.loads(files[0].read_bytes())
targets = ([t for c in value.get('TestConfigurations', []) for t in c['TestTargets']]
           if 'TestConfigurations' in value else [t for t in value.values() if isinstance(t, dict)])
matched = False
for target in targets:
    if target.get('BlueprintName') == 'RAITests':
        target.setdefault('EnvironmentVariables', {})['RAI_M7_GOLDEN_DIR'] = sys.argv[2]
        matched = True
assert matched, 'RAITests target not found'
(root/'Build/Products/ua1-r.xctestrun').write_bytes(plistlib.dumps(value))
PY
xcodebuild test-without-building -xctestrun "$derived/Build/Products/ua1-r.xctestrun" \
  -destination "platform=iOS Simulator,id=$RAI_UA1_R_SIMULATOR" \
  -parallel-testing-enabled NO -only-testing:RAITests \
  -resultBundlePath "$result_root/result.xcresult" -quiet
xcrun xcresulttool get test-results summary --path "$result_root/result.xcresult" > "$result_root/summary.json"
xcrun xcresulttool get test-results tests --path "$result_root/result.xcresult" > "$result_root/tests.json"
echo "UA1_R_SIMULATOR_RESULT=$result_root"
