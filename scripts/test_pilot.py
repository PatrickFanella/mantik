"""Resource and evidence guards for the pilot utility (no Docker required)."""
import datetime as dt
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import pilot


class PilotGuards(unittest.TestCase):
    def test_evidence_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'receipt.json'
            pilot.save(path, {'original': True})
            with self.assertRaises(FileExistsError):
                pilot.save(path, {'original': False})
            self.assertEqual(json.loads(path.read_text()), {'original': True})

    def test_reject_repository_artifacts_before_docker(self):
        with patch.object(pilot, 'run') as run:
            with self.assertRaises(ValueError):
                pilot.prepare(pilot.REPO / 'data' / 'pilot', 41880)
            run.assert_not_called()

    def test_moved_manifest_cannot_target_original_project(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pilot.save(root / 'manifest.json', {'root': '/different/path', 'project': 'mantik-pilot-example'})
            with patch.object(pilot, 'run') as run:
                with self.assertRaises(ValueError):
                    pilot.compose(root, 'stop')
                run.assert_not_called()

    def test_nonpilot_project_cannot_be_controlled(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pilot.save(root / 'manifest.json', {'root': str(root), 'project': 'mantik-dev'})
            with self.assertRaises(ValueError):
                pilot.load(root)

    def test_changed_config_cannot_control_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'compose.json').write_text('{"services": {}}')
            pilot.save(root / 'manifest.json', {'root': str(root), 'project': 'mantik-pilot-test',
                       'config_sha256': 'different'})
            with patch.object(pilot, 'run') as run:
                with self.assertRaises(ValueError):
                    pilot.compose(root, 'stop')
                run.assert_not_called()

    def test_gated_stack_cannot_claim_continuous_qualification(self):
        with patch.object(pilot, 'load', return_value={'fixture_gate': 'after_submit'}):
            with patch.object(pilot, 'api') as api:
                with self.assertRaises(ValueError):
                    pilot.observe(Path('/private/pilot'))
                api.assert_not_called()

    def test_bad_backup_cannot_create_restore_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dump = root / 'backup.dump'
            dump.write_bytes(b'corrupt')
            pilot.save(root / 'manifest.json', {'root': str(root), 'project': 'mantik-pilot-test'})
            pilot.save(dump.with_suffix('.json'), {'source_manifest': pilot.load(root), 'sha256': 'wrong'})
            with patch.object(pilot, 'prepare') as prepare:
                with self.assertRaises(ValueError):
                    pilot.restore(root, root / 'restore', 41881, dump)
                prepare.assert_not_called()


class RoutingEvidenceGuards(unittest.TestCase):
    def fixture(self):
        return {'observation_count': 0, 'state': {'case': {'status': 'waiting'}, 'actions': {'items': {}},
            'routing': {'cases': [{'case_key': 'pilot-missing-case', 'match_count': 1}], 'timers': {'jobs': {
                'pilot-missing-v1': {'status': 'completed', 'receipt': {'status': 'matched', 'routing': {'matches': [{}]}}},
                'pilot-observed-v1': {'status': 'completed', 'receipt': {'status': 'event_observed', 'routing': {'matches': []}}}}}}}}

    def test_suppression_must_have_no_match(self):
        status = self.fixture()
        status['state']['routing']['timers']['jobs']['pilot-observed-v1']['receipt']['routing']['matches'] = [{}]
        with self.assertRaises(RuntimeError):
            pilot.check_routing_scenario(status)

    def test_multiple_deliveries_cannot_pass(self):
        status = self.fixture()
        status['state']['routing']['cases'][0]['match_count'] = 2
        with self.assertRaises(RuntimeError):
            pilot.check_routing_scenario(status)

    def test_research_case_cannot_hide_an_economic_effect(self):
        status = self.fixture()
        status['state']['actions']['items'] = {'unexpected-action': {}}
        with self.assertRaises(RuntimeError):
            pilot.check_routing_scenario(status)


class ObservationClockGuards(unittest.TestCase):
    def setUp(self):
        self.start = dt.datetime(2026, 10, 14, tzinfo=dt.timezone.utc)

    def test_host_suspend_is_not_hidden_by_monotonic_clock(self):
        gap = pilot.observation_gap(100, 130, self.start, self.start + dt.timedelta(hours=3))
        self.assertIsNotNone(gap)
        self.assertEqual(gap['reason'], 'supervision_gap')
        self.assertEqual(gap['wall_gap_seconds'], 10800)

    def test_scheduler_stall_is_a_gap(self):
        gap = pilot.observation_gap(100, 160, self.start, self.start + dt.timedelta(seconds=60))
        self.assertEqual(gap['reason'], 'supervision_gap')

    def test_clock_step_cannot_be_hidden_inside_sample_interval(self):
        gap = pilot.observation_gap(100, 130, self.start, self.start + dt.timedelta(seconds=20))
        self.assertEqual(gap['reason'], 'clock_discontinuity')

    def test_backwards_wall_clock_is_a_failure(self):
        gap = pilot.observation_gap(100, 130, self.start, self.start - dt.timedelta(seconds=1))
        self.assertEqual(gap['reason'], 'clock_backwards')

    def test_ordinary_sampling_jitter_is_allowed(self):
        self.assertIsNone(pilot.observation_gap(100, 131, self.start, self.start + dt.timedelta(seconds=31.1)))


class WindowFaultEvidence(unittest.TestCase):
    def setUp(self):
        self.start = dt.datetime(2026, 10, 14, tzinfo=dt.timezone.utc)

    def test_fault_resolved_between_samples_still_fails_window(self):
        fault = {'id': 'database-outage', 'status': 'resolved', 'at': '2026-10-14T00:00:05Z'}
        self.assertEqual(pilot.window_faults([fault], self.start), [fault])

    def test_old_resolved_history_does_not_fail_new_window(self):
        fault = {'id': 'old-outage', 'status': 'resolved', 'at': '2026-10-13T23:00:00Z',
                 'last_observed_at': '2026-10-13T23:00:05Z'}
        self.assertEqual(pilot.window_faults([fault], self.start), [])

    def test_recurrence_under_retained_identity_is_detected(self):
        fault = {'id': 'recurrent-outage', 'status': 'resolved', 'at': '2026-10-13T23:00:00Z',
                 'last_observed_at': '2026-10-14T00:00:05Z'}
        self.assertEqual(pilot.window_faults([fault], self.start), [fault])


class ObservationLoopContracts(unittest.TestCase):
    def run_window(self, first_clock_step=0, fault=False):
        real_datetime = dt.datetime
        wall = real_datetime(2026, 10, 14, tzinfo=dt.timezone.utc)
        monotonic = 0
        slept = False

        class FakeDatetime(real_datetime):
            @classmethod
            def now(cls, tz=None):
                return wall

        def sleep(seconds):
            nonlocal wall, monotonic, slept
            monotonic += seconds
            wall += dt.timedelta(seconds=seconds + (first_clock_step if not slept else 0))
            slept = True

        def api(root, path):
            if path == '/v1/readiness/fixture_operation':
                return {'ready': True}
            if path == '/v1/operations':
                return {'exceptions': [{'id': 'brief-outage', 'status': 'resolved',
                    'at': '2026-10-14T00:00:10Z'}] if fault and monotonic >= 30 else []}
            return {}

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.object(pilot, 'load', return_value={'fixture_gate': None}), \
                 patch.object(pilot, 'wait_ready'), patch.object(pilot, 'snapshot'), \
                 patch.object(pilot, 'api', side_effect=api), \
                 patch.object(pilot.dt, 'datetime', FakeDatetime), \
                 patch.object(pilot.time, 'monotonic', side_effect=lambda: monotonic), \
                 patch.object(pilot.time, 'sleep', side_effect=sleep):
                pilot.observe(root)
            return json.loads((root / 'window-result.json').read_text())

    def test_suspend_cannot_produce_a_success_receipt(self):
        result = self.run_window(first_clock_step=3600)
        self.assertFalse(result['continuous_readiness_passed'])
        self.assertGreater(result['failure_count'], 0)
        self.assertGreaterEqual(result['monotonic_elapsed_seconds'], 86400)

    def test_backward_step_waits_for_both_declared_durations(self):
        result = self.run_window(first_clock_step=-10)
        self.assertFalse(result['continuous_readiness_passed'])
        self.assertGreaterEqual(result['wall_elapsed_seconds'], 86400)
        self.assertGreaterEqual(result['monotonic_elapsed_seconds'], 86410)

    def test_resolved_outage_fails_even_when_every_readiness_sample_is_healthy(self):
        result = self.run_window(fault=True)
        self.assertEqual(result['unhealthy_samples'], 0)
        self.assertEqual(result['observed_fault_ids'], ['brief-outage'])
        self.assertFalse(result['continuous_readiness_passed'])

    def test_full_healthy_virtual_window_does_not_claim_all_scenarios(self):
        result = self.run_window()
        self.assertTrue(result['continuous_readiness_passed'])
        self.assertFalse(result['all_scenarios_qualified'])
        self.assertEqual(result['failure_count'], 0)
        self.assertGreaterEqual(result['wall_elapsed_seconds'], 86400)


if __name__ == '__main__':
    unittest.main()


class BackupIdentityGuards(unittest.TestCase):
    def identity(self):
        return {'method': 'postgres17-public-table-json-sha256-v1',
                'tables': [{'table': 'public.mantik_state', 'rows': 1, 'sha256': 'a' * 64}]}

    def test_invalid_identity_and_duplicate_tables(self):
        from snapshot_identity import validate_identity, require_identity
        good = self.identity()
        self.assertEqual(validate_identity(good), good)
        for bad in [None, {}, dict(good, method='unknown'), dict(good, tables=good['tables'] * 2),
                    dict(good, tables=[dict(good['tables'][0], rows=True)]),
                    dict(good, tables=[dict(good['tables'][0], sha256='bad')])]:
            with self.assertRaises(ValueError):
                validate_identity(bad)
        with self.assertRaises(ValueError):
            require_identity(good, dict(good, tables=[]))

    def restore_fixture(self, directory, identity=True):
        import hashlib
        root = Path(directory)
        dump = root / 'backup.dump'
        dump.write_bytes(b'fixture archive')
        manifest = {'root': str(root), 'project': 'mantik-pilot-test', 'image': 'fixture-image', 'source_sha': 'fixture-sha'}
        receipt = {'source_manifest': manifest, 'sha256': hashlib.sha256(dump.read_bytes()).hexdigest()}
        if identity:
            receipt.update(schema='mantik-backup-v2', table_data_identity=self.identity())
        pilot.save(dump.with_suffix('.json'), receipt)
        return root, dump, manifest

    def test_mismatch_never_starts_application_workers(self):
        with tempfile.TemporaryDirectory() as directory:
            root, dump, manifest = self.restore_fixture(directory)
            with patch.object(pilot, 'load', return_value=manifest), patch.object(pilot, 'prepare'), \
                    patch.object(pilot, 'compose') as compose, patch.object(pilot, 'Snapshot') as snapshot, \
                    patch.object(pilot, 'compose_command', return_value=['owned-psql']):
                snapshot.return_value.__enter__.return_value.identity = dict(self.identity(), tables=[])
                with self.assertRaises(ValueError):
                    pilot.restore(root, root / 'restored', 41881, dump)
                self.assertEqual([call.args[1:3] for call in compose.call_args_list], [('up', '-d'), ('exec', '-T')])
                self.assertEqual(compose.call_args_list[0].args[-1], 'postgres')

    def test_invalid_v2_identity_cannot_prepare_resources(self):
        with tempfile.TemporaryDirectory() as directory:
            root, dump, manifest = self.restore_fixture(directory)
            receipt = json.loads(dump.with_suffix('.json').read_text())
            receipt['table_data_identity']['tables'][0]['sha256'] = 'bad'
            dump.with_suffix('.json').write_text(json.dumps(receipt))
            with patch.object(pilot, 'load', return_value=manifest), patch.object(pilot, 'prepare') as prepare:
                with self.assertRaises(ValueError):
                    pilot.restore(root, root / 'restored', 41881, dump)
                prepare.assert_not_called()

    def test_legacy_restore_records_unavailable_comparison(self):
        with tempfile.TemporaryDirectory() as directory:
            root, dump, manifest = self.restore_fixture(directory, identity=False)
            target = root / 'restored'
            with patch.object(pilot, 'load', return_value=manifest), \
                    patch.object(pilot, 'prepare', side_effect=lambda *args: target.mkdir()), \
                    patch.object(pilot, 'compose'), patch.object(pilot, 'wait_ready'), \
                    patch.object(pilot, 'snapshot'), patch.object(pilot, 'Snapshot') as identity:
                pilot.restore(root, target, 41881, dump)
                identity.assert_not_called()
            receipt = json.loads((target / 'restore-receipt.json').read_text())
            self.assertFalse(receipt['table_data_identity_verified'])
            self.assertEqual(receipt['table_data_identity_status'], 'legacy_receipt_unavailable')
            self.assertFalse(receipt['qualification_credit'])


class StackTeardown(unittest.TestCase):
    def owned(self, directory):
        import hashlib
        root = Path(directory)
        (root / 'compose.json').write_text('{"services": {}}')
        pilot.save(root / 'manifest.json', {'root': str(root), 'project': 'mantik-pilot-test',
                   'config_sha256': hashlib.sha256((root / 'compose.json').read_bytes()).hexdigest()})
        return root

    def test_teardown_removes_only_owned_project_with_volumes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.owned(directory)
            with patch.object(pilot, 'run') as run:
                pilot.teardown(root)
            run.assert_called_once_with('docker', 'compose', '--project-name', 'mantik-pilot-test',
                                        '--file', str(root / 'compose.json'),
                                        'down', '--volumes', '--remove-orphans')
            receipts = list(root.glob('teardown-*.json'))
            self.assertEqual(len(receipts), 1)
            self.assertEqual(json.loads(receipts[0].read_text())['project'], 'mantik-pilot-test')

    def test_teardown_without_manifest_touches_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(pilot, 'run') as run:
                pilot.teardown(Path(directory))
            run.assert_not_called()

    def test_changed_config_cannot_be_torn_down(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.owned(directory)
            (root / 'compose.json').write_text('{"services": {"other": {}}}')
            with patch.object(pilot, 'run') as run:
                with self.assertRaises(ValueError):
                    pilot.teardown(root)
            run.assert_not_called()

    def test_disposable_tears_down_on_success_failure_and_interrupt(self):
        root = Path('/private/pilot')
        with patch.object(pilot, 'teardown') as teardown:
            with pilot.disposable(root):
                pass
            with self.assertRaises(RuntimeError):
                with pilot.disposable(root):
                    raise RuntimeError('scenario failed')
            with self.assertRaises(KeyboardInterrupt):
                with pilot.disposable(root):
                    raise KeyboardInterrupt
            with self.assertRaises(SystemExit):
                with pilot.disposable(root):
                    raise SystemExit(143)  # SIGTERM handler in main()
        self.assertEqual(teardown.call_count, 4)
        teardown.assert_called_with(root)

    def test_teardown_error_does_not_hide_original_failure(self):
        with patch.object(pilot, 'teardown', side_effect=RuntimeError('docker unavailable')):
            with self.assertRaisesRegex(RuntimeError, 'scenario failed'):
                with pilot.disposable(Path('/private/pilot')):
                    raise RuntimeError('scenario failed')

    def test_keep_retains_resources_in_every_case(self):
        with patch.object(pilot, 'teardown') as teardown:
            with pilot.disposable(Path('/private/pilot'), keep=True):
                pass
            with self.assertRaises(RuntimeError):
                with pilot.disposable(Path('/private/pilot'), keep=True):
                    raise RuntimeError('inspect me')
        teardown.assert_not_called()

    def test_failure_only_mode_keeps_a_started_stack(self):
        with patch.object(pilot, 'teardown') as teardown:
            with pilot.disposable(Path('/private/pilot'), on_success=False):
                pass
            teardown.assert_not_called()
            with self.assertRaises(RuntimeError):
                with pilot.disposable(Path('/private/pilot'), on_success=False):
                    raise RuntimeError('not ready')
            teardown.assert_called_once()

    def test_keep_is_opt_in(self):
        with patch.dict(pilot.os.environ, {}, clear=True):
            self.assertFalse(pilot.keep_requested())
            self.assertTrue(pilot.keep_requested(True))
        for value, expected in [('1', True), ('true', True), ('0', False), ('', False)]:
            with patch.dict(pilot.os.environ, {pilot.KEEP_ENV: value}):
                self.assertEqual(pilot.keep_requested(), expected)

    def main(self, *args, env=None):
        with patch.object(pilot.sys, 'argv', ['pilot.py', *args, '--dir', '/private/pilot']), \
             patch.dict(pilot.os.environ, env or {}, clear=True), \
             patch.object(pilot.signal, 'signal'), patch.object(pilot.os, 'umask'), \
             patch.object(pilot, 'load'):
            return pilot.main()

    def test_scenario_commands_tear_down_after_success_and_failure(self):
        for command, function, extra in [('smoke', 'smoke', []), ('routing-smoke', 'routing_smoke', []),
                                          ('delivery-smoke', 'delivery_smoke', ['--scenario', 'expiry']),
                                          ('observe', 'observe', [])]:
            for error in [None, RuntimeError('failed'), KeyboardInterrupt()]:
                with self.subTest(command=command, error=error), \
                     patch.object(pilot, function, side_effect=error), \
                     patch.object(pilot, 'teardown') as teardown:
                    if error is None:
                        self.main(command, *extra)
                    else:
                        with self.assertRaises(type(error)):
                            self.main(command, *extra)
                    teardown.assert_called_once_with(Path('/private/pilot'))

    def test_scenario_command_keep_flag_and_environment(self):
        for args, env in [(['--keep'], None), ([], {pilot.KEEP_ENV: '1'})]:
            with self.subTest(args=args), patch.object(pilot, 'smoke'), \
                 patch.object(pilot, 'teardown') as teardown:
                self.main('smoke', *args, env=env)
                teardown.assert_not_called()

    def test_start_keeps_ready_stack_and_removes_failed_one(self):
        with patch.object(pilot, 'compose'), patch.object(pilot, 'wait_ready'), \
             patch.object(pilot, 'snapshot'), patch.object(pilot, 'teardown') as teardown:
            self.main('start')
            teardown.assert_not_called()
        with patch.object(pilot, 'compose'), patch.object(pilot, 'snapshot'), \
             patch.object(pilot, 'wait_ready', side_effect=RuntimeError('not ready')), \
             patch.object(pilot, 'teardown') as teardown:
            with self.assertRaises(RuntimeError):
                self.main('start')
            teardown.assert_called_once()

    def test_stop_removes_stack_after_final_evidence(self):
        order = []
        with patch.object(pilot, 'snapshot', side_effect=lambda root: order.append('snapshot')), \
             patch.object(pilot, 'compose') as compose, \
             patch.object(pilot, 'teardown', side_effect=lambda root: order.append('teardown')):
            self.main('stop')
        self.assertEqual(order, ['snapshot', 'teardown'])
        compose.assert_not_called()

    def test_stop_removes_stack_even_when_capture_fails(self):
        with patch.object(pilot, 'snapshot', side_effect=OSError('service down')), \
             patch.object(pilot, 'teardown') as teardown:
            with self.assertRaises(OSError):
                self.main('stop')
            teardown.assert_called_once()

    def test_stop_keep_only_stops_containers(self):
        with patch.object(pilot, 'snapshot'), patch.object(pilot, 'compose') as compose, \
             patch.object(pilot, 'teardown') as teardown:
            self.main('stop', '--keep')
        compose.assert_called_once_with(Path('/private/pilot'), 'stop')
        teardown.assert_not_called()

    def test_failed_restore_removes_only_the_new_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root, dump, manifest = BackupIdentityGuards.restore_fixture(None, directory, identity=False)
            target = root / 'restored'
            with patch.object(pilot, 'load', return_value=manifest), \
                 patch.object(pilot, 'prepare', side_effect=lambda *args: target.mkdir()), \
                 patch.object(pilot, 'compose', side_effect=[None, RuntimeError('pg_restore failed')]), \
                 patch.object(pilot, 'teardown') as teardown:
                with self.assertRaises(RuntimeError):
                    pilot.restore(root, target, 41881, dump)
            teardown.assert_called_once_with(target)

    def test_restore_into_existing_directory_is_refused_without_teardown(self):
        with tempfile.TemporaryDirectory() as directory:
            root, dump, manifest = BackupIdentityGuards.restore_fixture(None, directory, identity=False)
            target = root / 'existing'
            target.mkdir()
            with patch.object(pilot, 'load', return_value=manifest), patch.object(pilot, 'prepare') as prepare, \
                 patch.object(pilot, 'teardown') as teardown:
                with self.assertRaises(ValueError):
                    pilot.restore(root, target, 41881, dump)
            prepare.assert_not_called()
            teardown.assert_not_called()

    def test_restore_keep_retains_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root, dump, manifest = BackupIdentityGuards.restore_fixture(None, directory, identity=False)
            target = root / 'restored'
            with patch.object(pilot, 'load', return_value=manifest), \
                 patch.object(pilot, 'prepare', side_effect=lambda *args: target.mkdir()), \
                 patch.object(pilot, 'compose'), patch.object(pilot, 'wait_ready'), \
                 patch.object(pilot, 'snapshot'), patch.object(pilot, 'teardown') as teardown:
                pilot.restore(root, target, 41881, dump, keep=True)
            teardown.assert_not_called()
