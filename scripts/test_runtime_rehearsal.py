import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

from runtime_rehearsal import Rehearsal


class RehearsalPreparationTests(unittest.TestCase):
    def test_unexpected_worker_exit_stops_the_observation(self):
        for worker in ["collector", "research"]:
            with self.subTest(worker=worker), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                binary = root / "mantik"
                binary.write_bytes(b"test fixture binary")
                rehearsal = Rehearsal(root, binary, root / "unused-credentials",
                                      "http://127.0.0.1:11434", "ollama/local")
                rehearsal.owner = "fixture-receiver"
                rehearsal.service = Mock(poll=Mock(return_value=None))
                collector = Mock(poll=Mock(return_value=1 if worker == "collector" else None))
                research = Mock(poll=Mock(return_value=1 if worker == "research" else None))
                with patch.object(rehearsal, "prepare"), patch.object(rehearsal, "snapshot"), \
                        patch.object(rehearsal, "collector", return_value=collector), \
                        patch.object(rehearsal, "start", return_value=research), \
                        patch("runtime_rehearsal.time.monotonic", return_value=0), \
                        patch("runtime_rehearsal.time.sleep", side_effect=AssertionError("worker exit was ignored")):
                    with self.assertRaisesRegex(RuntimeError, "rehearsal " + worker + ".* stopped unexpectedly"):
                        rehearsal.run(False, 1200)

    def test_seed_capture_leaves_registered_collector_journal_empty(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binary = root / "mantik"
            binary.write_bytes(b"test fixture binary")
            rehearsal = Rehearsal(root, binary, root / "unused-credentials",
                                  "http://127.0.0.1:11434", "ollama/local")
            (root / "model-proxy").mkdir()
            journals, registrations, definitions, starts = {}, [], [], []

            def command(name, *args):
                journal = Path(args[args.index("--journal") + 1])
                if name == "alpaca-journal":
                    self.assertIn("--init", args)
                    journal.mkdir()
                    journals[journal] = {"journal_id": "fixture-" + str(len(journals)), "pending": 0}
                    return journals[journal]
                if name == "alpaca-collect":
                    journals[journal]["pending"] += 1
                    (journal / "capture-rehearsal-seed-fixture.json").write_text(json.dumps({
                        "recording": {"original": json.dumps({"quotes": {"SPY": {"ap": 123.45}}})}}))
                    return {}
                self.fail("unexpected command")

            def api(path, body=None):
                if path == "/v1/feed-collectors":
                    if body:
                        registrations.append(body["command"]["registration"])
                    return {"receiver_id": "fixture-receiver"}
                if path == "/v1/paper-strategies":
                    definitions.append(body["definition"])
                    return {"record": {"sha256": "a" * 64, "definition_sha256": "b" * 64}}
                if path == "/v1/paper-accounts":
                    return {"account": {"sha256": "c" * 64}}
                if path == "/v1/allocation-policies":
                    return {"record": {"sha256": "d" * 64}}
                if path == "/v1/paper-evaluation-declarations":
                    return {}
                self.fail("unexpected API request")

            def check_output(argv, **kwargs):
                if argv[1] == "inspect":
                    return json.dumps([{"Image": "fixture-image", "NetworkSettings": {
                        "Ports": {"5432/tcp": [{"HostPort": "54320"}]}}}]).encode()
                self.assertIn("psql", argv)
                return "105\n"

            def start(label, *args):
                starts.append((label, args))
                return Mock()

            with patch.object(rehearsal, "command", side_effect=command), \
                    patch.object(rehearsal, "api", side_effect=api), \
                    patch.object(rehearsal, "serve", return_value=Mock()), \
                    patch.object(rehearsal, "stop"), \
                    patch.object(rehearsal, "start", side_effect=start), \
                    patch("runtime_rehearsal.create_proxy", return_value=Mock(server_port=12345)), \
                    patch("runtime_rehearsal.threading.Thread"), \
                    patch("runtime_rehearsal.subprocess.run", return_value=subprocess.CompletedProcess([], 0, stdout="fixture-owned-container")), \
                    patch("runtime_rehearsal.subprocess.check_output", side_effect=check_output):
                rehearsal.prepare()
                rehearsal.collector()

            collector_args = starts[0][1]
            working = Path(collector_args[collector_args.index("--journal") + 1])
            self.assertEqual(registrations[0]["journal_id"], journals[working]["journal_id"])
            self.assertEqual(journals[working]["pending"], 0,
                             "seed acquisition left an unacknowledged input in the continuous collector journal")
            self.assertEqual(sum(j["pending"] for j in journals.values()), 1)
            self.assertEqual([d["entry_at_or_below_cents"] for d in definitions], [12445, 6172])


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
