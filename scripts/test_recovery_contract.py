"""Reject empty/skipped readback proofs and non-owned replay targets."""
import json
from pathlib import Path
import runpy
import unittest

MODULE = runpy.run_path(str(Path(__file__).with_name('test-integration.py')))


class OwnerReplayProofTests(unittest.TestCase):
    def test_complete_integration_scope_has_one_recovery_producer(self):
        scope = MODULE['test_scope']
        command, producer = scope([])
        self.assertTrue(producer)
        self.assertEqual(command[-2:], ['./internal/storage', './internal/cli'])
        self.assertIn('-first-only=TestPostgresContractsRecoverySeed', command)
        dedicated = '-dedicated=TestPostgresContractsFeedRotationCrossesActiveCapacity'
        self.assertIn(dedicated, command)
        producers = []
        for package in ('storage', 'cli'):
            for shard in range(3 if package == 'storage' else 2):
                command, producer = scope(['--package', package, '--shard', str(shard)])
                self.assertEqual(command[-1], './internal/' + package)
                self.assertIn('-index=' + str(shard), command)
                self.assertIn('-race', command)
                self.assertIn('-parts=2', command)
                self.assertIn('-run=^TestPostgresContracts', command)
                self.assertEqual(dedicated in command, package == 'storage')
                if producer:
                    producers.append((package, shard))
        self.assertEqual(producers, [('storage', 0)])

    def test_invalid_scopes_fail_before_creating_containers(self):
        for argv in (['--shard', '0'], ['--package', 'other'],
                     ['--package', 'storage', '--shard', '3'],
                     ['--package', 'cli', '--shard', '2']):
            with self.subTest(argv=argv), self.assertRaises(SystemExit):
                MODULE['test_scope'](argv)

    def test_actual_test_execution_required(self):
        name = 'TestPostgresContractsRecoveryReplay'
        check = MODULE['require_replay_pass']
        good = [{'Test': name, 'Action': 'run'}, {'Test': name, 'Action': 'pass'}, {'Action': 'pass'}]
        check('\n'.join(map(json.dumps, good)))
        for events in [[], [{'Action': 'pass'}], [{'Test': name, 'Action': 'skip'}],
                       good + [good[1]], [{'Test': 'Other', 'Action': 'run'}, {'Test': 'Other', 'Action': 'pass'}],
                       good + [{'Action': 'fail'}]]:
            with self.subTest(events=events), self.assertRaises(ValueError):
                check('\n'.join(map(json.dumps, events)))

    def test_replay_target_must_match_disposable_owner(self):
        replay = MODULE['replay_restored_owner']
        base = 'mantik-test-123456abcdef'
        target = 'mantik_test_123456abcdef_restore'
        for container, source, database in [('existing', base + '-go', target),
                                             (base, 'foreign-go', target),
                                             (base, base + '-go', 'production'),
                                             (base, base + '-go', 'mantik_test_abcdef123456_restore')]:
            with self.subTest(database=database), self.assertRaises(ValueError):
                replay(container, source, database)


if __name__ == '__main__':
    unittest.main()
