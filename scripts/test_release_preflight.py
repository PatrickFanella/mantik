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


if __name__ == "__main__":
    unittest.main()
