#!/usr/bin/env python3
"""Narrow XcodeGen 2.46 workaround: upstream yonaskolb/XcodeGen#1637.

Only repairs its known stringified dictionary; validates the actual plist.
With a fixed generator, verification succeeds without changing the project.
"""
import json
import pathlib
import subprocess

path = pathlib.Path(__file__).resolve().parents[1] / 'ios/RAI/RAI.xcodeproj/project.pbxproj'

def capability():
    project = json.loads(subprocess.check_output(['plutil', '-convert', 'json', '-o', '-', str(path)]))
    target = next(key for key, value in project['objects'].items() if value.get('isa') == 'PBXNativeTarget' and value.get('name') == 'RAI')
    root = project['objects'][project['rootObject']]
    return root['attributes']['TargetAttributes'][target]['SystemCapabilities']

value = capability()
if isinstance(value, str):
    old = r'SystemCapabilities = "[\"com.apple.HealthKit\": [\"enabled\": 1]]";'
    contents = path.read_text()
    if contents.count(old) != 1:
        raise SystemExit('Unexpected generated HealthKit capability; refusing broad rewrite')
    path.write_text(contents.replace(old, 'SystemCapabilities = { com.apple.HealthKit = { enabled = 1; }; };'))
assert str(capability()['com.apple.HealthKit']['enabled']) == '1'
print('PASS: generated native HealthKit capability dictionary')
