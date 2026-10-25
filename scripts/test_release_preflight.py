import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import release_preflight as release


def source_tar(path, migration_names=("0001_bootstrap.sql", "0002_next.sql")):
    with tarfile.open(path, "w") as archive:
        for name in ["Dockerfile", "go.mod", "migrations/embed.go", *["migrations/" + n for n in migration_names]]:
            content = (name + "\n").encode()
            info = tarfile.TarInfo(name)
            info.size = len(content)
            archive.addfile(info, io.BytesIO(content))


class ReleasePreflightTests(unittest.TestCase):
    def test_source_identity_and_missing_migration(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "source.tar"
            source_tar(path)
            identity = release.archive_identity(path)
            self.assertEqual(identity["source_schema_maximum"], 2)
            self.assertEqual(len(identity["migration_sha256"]), 2)
            self.assertEqual(release.archive_identity(path), identity)
            source_tar(path, ("0001_bootstrap.sql", "0003_gap.sql"))
            with self.assertRaisesRegex(ValueError, "sequence"):
                release.archive_identity(path)

    def test_image_must_match_observed_source_and_nonroot_contract(self):
        image = "sha256:" + "a" * 64
        revision, source = "b" * 40, "c" * 64
        row = {"id": image, "user": "65532:65532", "entrypoint": ["/app/mantik"],
               "labels": {"org.opencontainers.image.revision": revision,
                          "io.mantik.source-archive-sha256": source}}
        self.assertEqual(release.validate_image([row], image, revision, source), row)
        for change in [{"id": "sha256:" + "d" * 64}, {"user": "root"},
                       {"entrypoint": ["/bin/sh"]}, {"labels": {}}]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                release.validate_image([row | change], image, revision, source)

    def test_tracked_environment_file_cannot_enter_build_context(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "source.tar"
            source_tar(path)
            with tarfile.open(path, "a") as archive:
                content = b"private fixture"
                info = tarfile.TarInfo(".env.production")
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))
            with self.assertRaisesRegex(ValueError, "private/local"):
                release.archive_identity(path)

    def test_inside_repository_and_existing_receipt_are_never_modified(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo = Path(tmp) / "repo"
            repo.mkdir()
            with patch.object(release.subprocess, "run") as run, patch.object(release, "output") as output:
                with self.assertRaises(ValueError):
                    release.build_preflight(repo, repo / "receipt")
                existing = Path(tmp) / "existing"
                existing.mkdir()
                (existing / "receipt.json").write_text("retained failure")
                with self.assertRaises(FileExistsError):
                    release.build_preflight(repo, existing)
                self.assertEqual((existing / "receipt.json").read_text(), "retained failure")
                run.assert_not_called()
                output.assert_not_called()

    def test_dirty_source_fails_before_archive_or_build(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo, dest = Path(tmp) / "repo", Path(tmp) / "attempt"
            repo.mkdir()
            with patch.object(release, "output", side_effect=[str(repo), " M Dockerfile"]), patch.object(release.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "dirty work"):
                    release.build_preflight(repo, dest)
                run.assert_not_called()
            receipt = json.loads((dest / "receipt.json").read_text())
            self.assertFalse(receipt["passed"])
            self.assertFalse(receipt["deployment_ready"])
            self.assertIn("dirty work", receipt["error"])

    def test_failed_build_retains_source_log_and_no_image_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo, dest = Path(tmp) / "repo", Path(tmp) / "attempt"
            repo.mkdir()
            def run(args, **kwargs):
                if args[:2] == ["git", "archive"]:
                    source_tar(dest / "source.tar")
                    return
                self.assertEqual(args[:3], ["docker", "build", "--network=default"])
                kwargs["stdout"].write("actual failed build output\n")
                raise subprocess.CalledProcessError(1, args)
            with patch.object(release, "output", side_effect=[str(repo), "", "b" * 40, "c" * 40]), patch.object(release.subprocess, "run", side_effect=run):
                with self.assertRaises(subprocess.CalledProcessError):
                    release.build_preflight(repo, dest)
            receipt = json.loads((dest / "receipt.json").read_text())
            self.assertFalse(receipt["passed"])
            self.assertNotIn("image", receipt)
            self.assertTrue((dest / "source.tar").is_file())
            self.assertIn("failed build", (dest / "build.log").read_text())
            self.assertFalse(receipt["services_started"])
            self.assertFalse(receipt["database_inspected"])


class ExecutableCheckTests(unittest.TestCase):
    def exercise(self, directory, failure=None):
        image, cid = "sha256:" + "a" * 64, "b" * 64
        state = {"id": cid, "image": image, "user": "65532:65532", "entrypoint": ["/app/mantik"],
                 "cmd": ["--help"], "network": "none", "readonly": True, "cap_drop": ["ALL"],
                 "security": ["no-new-privileges:true"], "mounts": [], "ports": {},
                 "pids_limit": 64, "memory": 128 * 1024 * 1024, "nano_cpus": 1000000000,
                 "exit": 0, "oom": False, "running": False, "started": "2026-10-25T00:00:00Z"}
        commands = []
        def output(args, cwd):
            commands.append(args)
            if args[:2] == ["docker", "create"]:
                self.assertEqual(args[-2:], [image, "--help"])
                self.assertIn("none", args)
                self.assertNotIn("-e", args)
                self.assertNotIn("-v", args)
                return cid
            return json.dumps(state | ({"network": "bridge"} if failure == "isolation" else {}))
        def run(args, **kwargs):
            commands.append(args)
            if args[:2] == ["docker", "start"]:
                if failure == "timeout":
                    raise subprocess.TimeoutExpired(args, 60)
                kwargs["stdout"].write("wrong binary\n" if failure == "output" else
                                         "Mantik — autonomous paper research bootstrap\nCommands:\nAll execution is synthetic paper.\n")
            if args[:2] == ["docker", "inspect"]:
                return subprocess.CompletedProcess(args, 1, "", "Error: No such object: " + cid)
            return subprocess.CompletedProcess(args, 0, "", "")
        with patch.object(release, "output", side_effect=output), patch.object(release.subprocess, "run", side_effect=run):
            if failure:
                with self.assertRaises((ValueError, subprocess.TimeoutExpired)):
                    release.executable_check(Path(directory), Path(directory) / "attempt", image)
            else:
                release.executable_check(Path(directory), Path(directory) / "attempt", image)
        receipt = json.loads((Path(directory) / "attempt/receipt.json").read_text())
        self.assertTrue(receipt["cleanup_verified"])
        self.assertEqual(receipt["passed"], failure is None)
        self.assertFalse(receipt["services_started"])
        self.assertFalse(receipt["qualification_credit"])
        self.assertIn(["docker", "rm", "-f", cid], commands)
        if failure == "isolation":
            self.assertFalse(any(c[:2] == ["docker", "start"] for c in commands))
        return receipt

    def test_actual_exit_is_required_and_exact_sandbox_is_removed(self):
        for failure in [None, "timeout", "output", "isolation"]:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as tmp:
                self.exercise(tmp, failure)

    def test_mutable_image_is_rejected_before_creating_attempt(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(release, "output") as output:
            dest = Path(tmp) / "attempt"
            with self.assertRaisesRegex(ValueError, "immutable"):
                release.executable_check(Path(tmp), dest, "mantik:latest")
            self.assertFalse(dest.exists())
            output.assert_not_called()


if __name__ == "__main__":
    unittest.main()
