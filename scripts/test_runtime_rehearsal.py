import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from runtime_rehearsal import Rehearsal


class RehearsalCleanupTests(unittest.TestCase):
    def test_dump_failure_still_removes_exact_owned_container_and_retains_failure(self):
        for failure in ["exit", "timeout"]:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                binary = root / "mantik"
                binary.write_bytes(b"test fixture binary")
                rehearsal = Rehearsal(root, binary, root / "unused-credentials",
                                      "http://127.0.0.1:11434", "ollama/local")
                rehearsal.container = "only-this-owned-container-id"
                rehearsal.env["DATABASE_URL"] = "postgres://mantik:private@127.0.0.1:9999/mantik_test_fixture"
                calls = []

                def run(argv, **kwargs):
                    calls.append(argv)
                    self.assertEqual(argv[0], "docker")
                    if argv[1] == "exec":
                        if failure == "timeout":
                            raise subprocess.TimeoutExpired(argv, 120)
                        return subprocess.CompletedProcess(argv, 1)
                    if argv[1] == "rm":
                        self.assertEqual(argv, ["docker", "rm", "-f", rehearsal.container])
                        return subprocess.CompletedProcess(argv, 0)
                    if argv[1] == "inspect":
                        return subprocess.CompletedProcess(argv, 1, stderr=b"error: no such object: owned")
                    self.fail("unexpected external action")

                with patch("runtime_rehearsal.subprocess.run", side_effect=run):
                    with self.assertRaisesRegex(RuntimeError, "cleanup needs inspection"):
                        rehearsal.cleanup()
                self.assertEqual([argv[1] for argv in calls], ["exec", "rm", "inspect"])
                receipt = json.loads((root / "receipt.json").read_text())
                self.assertEqual(receipt["cleanup"]["failures"], ["database_dump_failed"])
                self.assertFalse(receipt["qualification_credit"])
                self.assertNotIn("private", (root / "receipt.json").read_text())

    def test_docker_inspect_transport_failure_does_not_prove_removal(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "mantik"
            binary.write_bytes(b"test fixture binary")
            rehearsal = Rehearsal(root, binary, root / "unused", "http://127.0.0.1:11434", "ollama/local")
            rehearsal.container = "owned-container"
            rehearsal.env["DATABASE_URL"] = "postgres://mantik@127.0.0.1/mantik_test_fixture"
            results = [subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 0),
                       subprocess.CompletedProcess([], 1, stderr=b"Cannot connect to Docker daemon")]
            with patch("runtime_rehearsal.subprocess.run", side_effect=results):
                with self.assertRaisesRegex(RuntimeError, "cleanup needs inspection"):
                    rehearsal.cleanup()
            receipt = json.loads((root / "receipt.json").read_text())
            self.assertEqual(receipt["cleanup"]["failures"], ["database_cleanup_unverified"])


if __name__ == "__main__":
    unittest.main()
