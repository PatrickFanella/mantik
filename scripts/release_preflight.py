"""Build and check an immutable executable without starting or changing a service."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import time
import uuid


def output(args, cwd):
    return subprocess.check_output(args, cwd=cwd, text=True, timeout=60).strip()


def archive_identity(path):
    migrations = {}
    with tarfile.open(path) as archive:
        members = archive.getmembers()
        names = {m.name for m in members if m.isfile()}
        for name in names:
            leaf = Path(name).name
            if ((leaf == ".env" or leaf.startswith(".env.")) and leaf != ".env.example"
                    or name.startswith(("data/", "tmp/", ".git/"))):
                raise ValueError("private/local inputs must not be tracked release sources")
        if not {"Dockerfile", "go.mod", "migrations/embed.go"} <= names:
            raise ValueError("archive lacks Mantik build inputs")
        for member in members:
            if re.fullmatch(r"migrations/[0-9]{4}_[a-z0-9_]+\.sql", member.name):
                if not member.isfile() or member.name in migrations:
                    raise ValueError("invalid migration archive member")
                migrations[member.name] = hashlib.sha256(archive.extractfile(member).read()).hexdigest()
    versions = sorted(int(n.split("/")[1][:4]) for n in migrations)
    if not versions or versions != list(range(1, versions[-1] + 1)):
        raise ValueError("source migration sequence is incomplete")
    return {
        "archive_sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
        "migration_sha256": dict(sorted(migrations.items())),
        "source_schema_maximum": versions[-1],
    }


def validate_image(rows, image_id, revision, source_sha):
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id) or len(rows) != 1:
        raise ValueError("invalid local image identity")
    row = rows[0]
    labels = row.get("labels") or {}
    if (row.get("id") != image_id or row.get("user") != "65532:65532"
            or row.get("entrypoint") != ["/app/mantik"]
            or labels.get("org.opencontainers.image.revision") != revision
            or labels.get("io.mantik.source-archive-sha256") != source_sha):
        raise ValueError("built image disagrees with this source build attempt")
    return row


def executable_check(repo, destination, image_id):
    """Execute only help in an owned sandbox, retaining failure and cleanup."""
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id):
        raise ValueError("immutable executable image required")
    destination.mkdir(mode=0o700, exist_ok=False)
    name = "mantik-release-help-" + uuid.uuid4().hex
    command = ["docker", "create", "--name", name, "--network", "none",
               "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
               "--pids-limit", "64", "--memory", "128m", "--cpus", "1", image_id, "--help"]
    receipt = {"schema": "mantik-isolated-executable-check-v1", "image_id": image_id,
               "name": name, "passed": False, "cleanup_verified": False,
               "services_started": False, "database_inspected": False,
               "provider_model_requests": 0, "qualification_credit": False}
    (destination / "command.json").write_text(json.dumps(command, indent=2) + "\n")
    container_id = None
    template = ('{"id":{{json .Id}},"image":{{json .Image}},"user":{{json .Config.User}},'
                '"entrypoint":{{json .Config.Entrypoint}},"cmd":{{json .Config.Cmd}},'
                '"network":{{json .HostConfig.NetworkMode}},"readonly":{{json .HostConfig.ReadonlyRootfs}},'
                '"cap_drop":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},'
                '"mounts":{{json .Mounts}},"ports":{{json .HostConfig.PortBindings}},'
                '"pids_limit":{{json .HostConfig.PidsLimit}},"memory":{{json .HostConfig.Memory}},"nano_cpus":{{json .HostConfig.NanoCpus}},'
                '"exit":{{json .State.ExitCode}},"oom":{{json .State.OOMKilled}},'
                '"running":{{json .State.Running}},"started":{{json .State.StartedAt}}}')
    try:
        container_id = output(command, repo)
        if not re.fullmatch(r"[0-9a-f]{64}", container_id):
            raise ValueError("invalid executable sandbox identity")
        receipt["container_id"] = container_id
        observed = json.loads(output(["docker", "inspect", "--format", template, container_id], repo))
        receipt["sandbox"] = observed
        if (observed["id"] != container_id or observed["image"] != image_id
                or observed["user"] != "65532:65532" or observed["entrypoint"] != ["/app/mantik"]
                or observed["cmd"] != ["--help"] or observed["network"] != "none"
                or not observed["readonly"] or observed["cap_drop"] != ["ALL"]
                or "no-new-privileges:true" not in observed["security"]
                or observed["mounts"] or observed["ports"] or observed["pids_limit"] != 64
                or observed["memory"] != 128 * 1024 * 1024 or observed["nano_cpus"] != 1000000000):
            raise ValueError("executable sandbox isolation disagrees with request")
        log_path = destination / "help.log"
        with log_path.open("w") as log:
            subprocess.run(["docker", "start", "-a", container_id], cwd=repo,
                           stdout=log, stderr=subprocess.STDOUT, check=True, timeout=60)
        after = json.loads(output(["docker", "inspect", "--format", template, container_id], repo))
        receipt["exit_state"] = after
        help_text = log_path.read_text()
        receipt["help_sha256"] = hashlib.sha256(log_path.read_bytes()).hexdigest()
        if (after["running"] or after["oom"] or after["exit"] != 0
                or not help_text.startswith("Mantik — autonomous paper research bootstrap\n")
                or "Commands:" not in help_text or "All execution is synthetic paper." not in help_text):
            raise ValueError("built Mantik executable did not produce expected help")
        receipt["passed"] = True
    except Exception as error:
        receipt["error"] = str(error)
        raise
    finally:
        # If create timed out, resolve its unique attempt name before cleanup.
        # Never retry create/start or delete another attempt's container.
        try:
            if container_id is None:
                found = subprocess.run(["docker", "inspect", "--format", "{{.Id}}", name],
                                       cwd=repo, capture_output=True, text=True, timeout=30)
                if found.returncode == 0:
                    container_id = found.stdout.strip()
            if container_id and re.fullmatch(r"[0-9a-f]{64}", container_id):
                removed = subprocess.run(["docker", "rm", "-f", container_id], cwd=repo,
                                         capture_output=True, text=True, timeout=30)
                check = subprocess.run(["docker", "inspect", "--format", "{{.Id}}", container_id],
                                       cwd=repo, capture_output=True, text=True, timeout=30)
                receipt["cleanup"] = {"container_id": container_id, "remove_exit": removed.returncode,
                                      "inspect_exit": check.returncode, "inspection": check.stdout + check.stderr}
                receipt["cleanup_verified"] = (removed.returncode == 0 and check.returncode != 0
                                                and "no such object" in (check.stdout + check.stderr).lower())
            if not receipt["cleanup_verified"]:
                receipt["passed"] = False
                raise ValueError("executable sandbox cleanup unverified; inspect retained attempt")
        except Exception as error:
            receipt["passed"] = False
            receipt["cleanup_error"] = str(error)
            raise
        finally:
            (destination / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return receipt


def build_preflight(repo, destination):
    repo, destination = repo.resolve(), destination.resolve()
    if destination == repo or repo in destination.parents:
        raise ValueError("receipt directory must be outside the repository")
    # A new directory owns exactly one attempt. Never overwrite failed evidence.
    destination.mkdir(mode=0o700, parents=True, exist_ok=False)
    receipt = {"schema": "mantik-source-image-preflight-v1", "started_ns": time.time_ns(),
               "passed": False, "deployment_ready": False, "services_started": False,
               "runtime_inspected": False, "database_inspected": False,
               "provider_model_requests": 0, "qualification_credit": False,
               "unmet_gates": ["configured_capability_inventory", "fresh_data_and_owner_identity",
                               "credential_readiness", "verified_prechange_backup",
                               "financial_and_protective_recovery", "deployment_authorization",
                               "postrelease_runtime_checks", "rollback_rehearsal"]}
    try:
        if output(["git", "rev-parse", "--show-toplevel"], repo) != str(repo):
            raise ValueError("repository root required")
        if output(["git", "status", "--porcelain", "--untracked-files=all"], repo):
            raise ValueError("commit or preserve dirty work before selecting a release source")
        revision = output(["git", "rev-parse", "HEAD"], repo)
        if not re.fullmatch(r"[0-9a-f]{40}", revision):
            raise ValueError("full Git revision required")
        receipt.update(revision=revision, tree=output(["git", "rev-parse", "HEAD^{tree}"], repo))
        source = destination / "source.tar"
        subprocess.run(["git", "archive", "--format=tar", "--output", str(source), revision],
                       cwd=repo, check=True, timeout=60)
        receipt.update(archive_identity(source))
        command = ["docker", "build", "--network=default", "--iidfile", str(destination / "image.id"),
                   "--label", "org.opencontainers.image.revision=" + revision,
                   "--label", "io.mantik.source-archive-sha256=" + receipt["archive_sha256"], "-"]
        (destination / "build-command.json").write_text(json.dumps(command, indent=2) + "\n")
        with source.open("rb") as stdin, (destination / "build.log").open("w") as log:
            subprocess.run(command, cwd=repo, stdin=stdin, stdout=log, stderr=subprocess.STDOUT,
                           check=True, timeout=1800)
        image_id = (destination / "image.id").read_text().strip()
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", image_id):
            raise ValueError("build did not retain an immutable local image ID")
        # Inspect selected metadata only. Never capture container environments.
        template = '{"id":{{json .Id}},"repo_digests":{{json .RepoDigests}},"user":{{json .Config.User}},"entrypoint":{{json .Config.Entrypoint}},"labels":{{json .Config.Labels}}}'
        rows = [json.loads(line) for line in output(["docker", "image", "inspect", "--format", template, image_id], repo).splitlines()]
        receipt["image"] = validate_image(rows, image_id, revision, receipt["archive_sha256"])
        receipt["executable_check"] = executable_check(repo, destination / "executable", image_id)
        if archive_identity(source)["archive_sha256"] != receipt["archive_sha256"]:
            raise ValueError("retained source archive changed during build")
        if output(["git", "rev-parse", "HEAD"], repo) != revision or output(["git", "status", "--porcelain", "--untracked-files=all"], repo):
            raise ValueError("checkout changed during build; retain this attempt and review its source")
        receipt["passed"] = True
    except Exception as error:
        receipt["error"] = str(error)
        raise
    finally:
        receipt["finished_ns"] = time.time_ns()
        (destination / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out-dir", required=True, type=Path, help="new private directory outside Git")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        result = build_preflight(Path(__file__).resolve().parents[1], args.out_dir)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        parser.exit(1, f"release preflight failed: {error}\n")
    print(json.dumps({"passed": result["passed"], "revision": result["revision"],
                      "image_id": result["image"]["id"], "deployment_ready": False}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
