#!/usr/bin/env python3
"""Safety checks for local device tooling; no phone, server or real data access."""
import importlib.util
import json
import os
import pathlib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('local_device', pathlib.Path(__file__).with_name('device.py'))
device = importlib.util.module_from_spec(spec)
spec.loader.exec_module(device)


class DeviceToolSafetyTests(unittest.TestCase):
    def test_settings_come_from_config_file_and_env_overrides_one_value(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'device.json'
            path.write_text(json.dumps({'run': '.local/device-x', 'pgPort': 55434, 'httpPort': 18084}))
            settings = device.load_settings(path, {})
            self.assertEqual(settings['run'], (device.ROOT / '.local/device-x').resolve())
            self.assertEqual(settings['derived'], (device.ROOT / '.local/device-x/DerivedData').resolve())
            self.assertEqual((settings['pgPort'], settings['httpPort']), (55434, 18084))
            override = device.load_settings(path, {'RAI_LOCAL_DEVICE_HTTP_PORT': '18090'})
            self.assertEqual((override['pgPort'], override['httpPort']), (55434, 18090))
            defaults = device.load_settings(pathlib.Path(directory) / 'missing.json', {})
            self.assertEqual((defaults['pgPort'], defaults['httpPort']), (55433, 18080))

    def test_explicit_host_rejects_public_loopback_names_and_invalid_addresses(self):
        for host in ('8.8.8.8', '127.0.0.1', '0.0.0.0', 'rai-fnos', '192.168.1.1.invalid', '10.1.2.300'):
            with self.subTest(host=host), patch.dict(os.environ, {'RAI_LOCAL_DEVICE_HOST': host}), \
                    patch.object(device.subprocess, 'run') as run:
                with self.assertRaises(RuntimeError):
                    device.lan_address()
                run.assert_not_called()

    def test_report_omits_health_values_identifiers_and_unknown_metrics(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(device, 'RUN', pathlib.Path(directory)), \
                patch.object(device, 'copy'):
            raw = {'result': 'SINGLE_WORKOUT_ACKNOWLEDGED', 'wallMs': 12.5, 'healthValuesExported': False,
                   'workoutID': 'synthetic-private-identity', 'heartRate': 123,
                   'reason': 'SYNTHETIC_PRIVATE_VALUE', 'timings': {'upload_ms': 1, 'heart_rate': 123},
                   'queryCounts': {'bootstrap': 0, 'synthetic-private-identity': 99}}
            (device.RUN / 'device-report.json').write_text(json.dumps(raw))
            report = device.report({})
            self.assertEqual(report['timings'], {'upload_ms': 1})
            self.assertEqual(report['queryCounts'], {'bootstrap': 0})
            self.assertNotIn('reason', report)
            self.assertNotIn('heartRate', report)
            self.assertNotIn('workoutID', report)

    def test_retired_completeness_is_rejected_before_device_or_credentials(self):
        with patch.object(device, 'load') as load, patch.object(device, 'device') as phone:
            with self.assertRaisesRegex(RuntimeError, 'retired'):
                device.launch('completeness')
            load.assert_not_called()
            phone.assert_not_called()

    def test_training_and_daily_reports_keep_only_typed_aggregate_fields(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(device, 'RUN', pathlib.Path(directory)), patch.object(device, 'copy'):
            raw = {'result': 'TRAINING_UI_READY', 'trainingPlanPresent': True, 'trainingPlanPublished': True,
                   'trainingReadFailed': False, 'trainingGenerationState': 'succeeded', 'trainingConfigRevision': 2,
                   'trainingEffectiveVersion': 3, 'dailyAnchorCount': 3, 'dailyPendingCount': 0,
                   'dailyBlockedCount': 0, 'dailyBackfillCompleteCount': 3,
                   'readinessScore': 71, 'sessions': [{'notes': 'synthetic-private-value'}], 'token': 'synthetic-secret'}
            (device.RUN / 'device-report.json').write_text(json.dumps(raw))
            result = device.report({})
            self.assertTrue(result['trainingPlanPublished'])
            self.assertEqual(result['trainingConfigRevision'], 2)
            self.assertEqual(result['dailyAnchorCount'], 3)
            self.assertNotIn('readinessScore', result)
            self.assertNotIn('sessions', result)
            self.assertNotIn('token', result)
            raw.update(trainingConfigRevision=True, trainingEffectiveVersion=-1,
                       dailyAnchorCount='synthetic-private-value', trainingPlanPresent='synthetic-private-value',
                       trainingGenerationState='synthetic-private-value')
            (device.RUN / 'device-report.json').write_text(json.dumps(raw))
            result = device.report({})
            for key in ('trainingConfigRevision', 'trainingEffectiveVersion', 'dailyAnchorCount', 'trainingPlanPresent', 'trainingGenerationState'):
                self.assertNotIn(key, result)

    def test_failed_atomic_replace_preserves_previous_state(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / 'run.json'
            device.write(path, {'operation': 'synthetic-original'})
            with patch.object(device.os, 'replace', side_effect=OSError('synthetic disk error')):
                with self.assertRaises(OSError):
                    device.write(path, {'operation': 'synthetic-successor'})
            self.assertEqual(json.loads(path.read_text()), {'operation': 'synthetic-original'})
            self.assertEqual(list(path.parent.iterdir()), [path])
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_foreign_run_identity_is_preserved_and_rejected(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(device, 'STATE', pathlib.Path(directory) / 'run.json'):
            device.STATE.write_text('{"environment":"synthetic-other-run"}')
            before = device.STATE.read_bytes()
            with self.assertRaises(RuntimeError):
                device.load()
            self.assertEqual(device.STATE.read_bytes(), before)


if __name__ == '__main__':
    unittest.main()
