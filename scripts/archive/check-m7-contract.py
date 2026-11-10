#!/usr/bin/env python3
"""Validate shared synthetic DTOs and additive-only OpenAPI compatibility.

Run with .local/m7-validation/bin/python after installing the pinned validation
tools listed in docs/evidence/m7/README.md. Does not contact any server.
"""
import json
import pathlib
import subprocess
import jsonschema
import yaml

root = pathlib.Path(__file__).resolve().parents[1]
schema = json.loads((root / 'contracts/athlete-model.schema.json').read_text())
jsonschema.Draft202012Validator.check_schema(schema)
validator = jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker())
fixtures = sorted((root / 'contracts/fixtures/athlete-model').glob('*.json'))
assert len(fixtures) == 11
for path in fixtures:
    validator.validate(json.loads(path.read_text()))
    client = root / 'ios/RAI/Tests/ProductCoreTests/Fixtures/AthleteModel' / path.name
    assert path.read_bytes() == client.read_bytes(), path.name

before = subprocess.check_output(['git', 'show', 'HEAD:contracts/openapi-v2.yaml'], cwd=root, text=True)
after = (root / 'contracts/openapi-v2.yaml').read_text()
assert after.startswith(before), 'existing OpenAPI content changed'
baseline, current = yaml.safe_load(before), yaml.safe_load(after)
assert baseline['paths'] == current['paths'], 'a formal endpoint was changed/enabled'
for key, value in baseline['components']['schemas'].items():
    assert current['components']['schemas'][key] == value, key
for key, value in schema['$defs'].items():
    expected = json.loads(json.dumps(value).replace('#/$defs/', '#/components/schemas/'))
    assert current['components']['schemas'][key] == expected, key
print('PASS: 11 synthetic golden schemas; exact Swift fixture identity; unchanged existing paths/schemas; new typed OpenAPI definitions match standalone schema')
