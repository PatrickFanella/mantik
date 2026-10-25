"""Reject empty/skipped readback proofs and non-owned replay targets."""
import json
from pathlib import Path
import runpy
import unittest

MODULE = runpy.run_path(str(Path(__file__).with_name('test-integration.py')))


class OwnerReplayProofTests(unittest.TestCase):
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
