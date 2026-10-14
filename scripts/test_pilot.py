"""Resource and evidence guards for the pilot utility (no Docker required)."""
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


if __name__ == '__main__':
    unittest.main()
