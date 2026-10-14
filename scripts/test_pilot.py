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
