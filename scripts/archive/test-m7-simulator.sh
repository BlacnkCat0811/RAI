#!/usr/bin/env bash
# Formal iOS App XCTest; synthetic fixtures only. No device or server connection.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
: "${DEVELOPER_DIR:?Set full Xcode path for this command only}"
: "${RAI_M7_SIMULATOR:?Select a synthetic iOS Simulator}"
derived="$root/.local/m7-derived"
xcodebuild build-for-testing -project "$root/ios/RAI/RAI.xcodeproj" -scheme RAI \
 -destination "platform=iOS Simulator,id=$RAI_M7_SIMULATOR" -derivedDataPath "$derived" \
 CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- -quiet
python3 - "$derived" "$root/contracts/fixtures/athlete-model" <<'PY'
import pathlib,plistlib,sys
root=pathlib.Path(sys.argv[1]); files=[p for p in (root/'Build/Products').glob('*.xctestrun') if p.name!='m7-contract.xctestrun']
assert len(files)==1, 'expected one generated xctestrun'
p=plistlib.loads(files[0].read_bytes())
targets=[t for config in p.get('TestConfigurations',[]) for t in config['TestTargets']] if 'TestConfigurations' in p else [t for t in p.values() if isinstance(t,dict)]
for target in targets:
 if target.get('BlueprintName')=='RAITests':
  target.setdefault('EnvironmentVariables',{})['RAI_M7_GOLDEN_DIR']=sys.argv[2]
(root/'Build/Products/m7-contract.xctestrun').write_bytes(plistlib.dumps(p))
PY
result_root="$(mktemp -d "$root/.local/m7-xctest.XXXXXX")"
xcodebuild test-without-building -xctestrun "$derived/Build/Products/m7-contract.xctestrun" \
 -destination "platform=iOS Simulator,id=$RAI_M7_SIMULATOR" \
 -parallel-testing-enabled NO -only-testing:RAITests -resultBundlePath "$result_root/result.xcresult" -quiet
xcrun xcresulttool get test-results summary --path "$result_root/result.xcresult" > "$result_root/summary.json"
xcrun xcresulttool get test-results tests --path "$result_root/result.xcresult" > "$result_root/tests.json"
python3 - "$result_root" "$root/docs/evidence/m7/simulator-summary.json" <<'PYRESULT'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]); summary=json.loads((root/'summary.json').read_text()); tests=json.loads((root/'tests.json').read_text())
for item in summary.get('devicesAndConfigurations',[]):item.get('device',{}).pop('deviceId',None)
selected=[]
def visit(value):
 if isinstance(value,dict):
  if 'M7' in value.get('name','') or value.get('result')=='Skipped':selected.append({k:value[k] for k in ('name','result') if k in value})
  for item in value.values():visit(item)
 elif isinstance(value,list):
  for item in value:visit(item)
visit(tests)
summary['m7AndSkippedTests']=selected
assert any('M7' in x['name'] and x['result']=='Passed' for x in selected)
pathlib.Path(sys.argv[2]).write_text(json.dumps(summary,indent=2)+'\n')
PYRESULT
