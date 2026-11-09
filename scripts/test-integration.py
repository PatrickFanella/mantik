#!/usr/bin/env python3
"""Run contracts on a uniquely named, disposable PostgreSQL container."""
import subprocess
import argparse
import json
import os
import re
import hashlib
from pathlib import Path
import tempfile
import time
import uuid

from recovery_contract import recovery_contract


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def require_runner_exit(output):
    """An attached client can exit zero while its test container still runs."""
    state = json.loads(output)
    if (not isinstance(state, dict) or state.get("Status") != "exited"
            or state.get("Running") is not False or type(state.get("ExitCode")) is not int
            or state["ExitCode"] != 0 or state.get("OOMKilled") is not False):
        raise RuntimeError("Test container did not finish successfully")


def require_replay_pass(output):
    """A successful package exit or skipped test does not prove owner replay."""
    events = [json.loads(line) for line in output.splitlines() if line.strip()]
    if any(not isinstance(e, dict) for e in events):
        raise ValueError("Invalid Go test event")
    name = "TestPostgresContractsRecoveryReplay"
    runs = [e for e in events if e.get("Test") == name and e.get("Action") == "run"]
    passes = [e for e in events if e.get("Test") == name and e.get("Action") == "pass"]
    if len(runs) != 1 or len(passes) != 1 or any(e.get("Action") in {"skip", "fail"} for e in events):
        raise ValueError("Restored owner replay did not execute and pass exactly once")


def replay_restored_owner(container, source_runner, database, expect_rejection=False):
    if (not re.fullmatch("mantik-test-[a-f0-9]{12}", container)
            or source_runner != container + "-go"
            or database != "mantik_test_" + container.removeprefix("mantik-test-") + "_restore"):
        raise ValueError("Owner replay requires this runner's exact restored database")
    # The stopped original runner owns the exact source that seeded this dump.
    # No host workspace edits can replace those bytes during recovery.
    suffix = "-reject-owner" if expect_rejection else "-replay-owner"
    runner = container + suffix
    created = False
    identity = None
    source_sha = None
    receipt = {"container": runner, "database": database, "expected_rejection": expect_rejection,
               "test": "TestPostgresContractsRecoveryReplay", "passed": False,
               "application_workers_started": False, "qualification_credit": False}
    configured = os.environ.get("MANTIK_RECOVERY_RECEIPT_DIR")
    receipt_path = None
    if configured:
        root = Path(configured).resolve()
        repo = Path(__file__).resolve().parents[1]
        if root == repo or repo in root.parents:
            raise ValueError("Recovery evidence must stay outside Git")
        root.mkdir(parents=True, exist_ok=True)
        receipt_path = root / (runner + "-" + str(time.time_ns()) + ".json")
    try:
        run("docker", "create", "--name", runner, "--network", "container:" + container,
            "-w", "/src", "-e", "MANTIK_TEST_DATABASE_URL=" +
            f"postgres://mantik:fixture-only@127.0.0.1:5432/{database}?sslmode=disable",
            "-e", "MANTIK_RECOVERY_REPLAY=1",
            "-e", "MANTIK_RECOVERY_EXPECT_REJECTION=" + ("1" if expect_rejection else "0"),
            "golang:1.26.9", "go", "test", "-race", "-json", "./internal/storage",
            "-run", "^TestPostgresContractsRecoveryReplay$", "-count=1", "-timeout=3m",
            stdout=subprocess.DEVNULL)
        created = True
        with tempfile.TemporaryFile() as archive:
            subprocess.run(["docker", "cp", source_runner + ":/src/.", "-"], stdout=archive, check=True, timeout=60)
            archive.seek(0)
            source_sha = hashlib.file_digest(archive, "sha256").hexdigest()
            archive.seek(0)
            subprocess.run(["docker", "cp", "-", runner + ":/src"], stdin=archive, check=True, timeout=60)
        identity = run("docker", "inspect", "--format", "{{.Id}}", runner, capture_output=True).stdout.strip()
        receipt.update(id=identity, source_archive_sha256=source_sha)
        if receipt_path:
            receipt_path.write_text(json.dumps(receipt, indent=2) + "\n")
        result = subprocess.run(["docker", "start", "-a", runner], text=True, capture_output=True, timeout=600)
        receipt.update(test_output=result.stdout, test_stderr=result.stderr, attach_exit=result.returncode)
        state = run("docker", "inspect", "--format", "{{json .State}}", runner, capture_output=True).stdout
        require_runner_exit(state)
        if result.returncode != 0:
            raise RuntimeError("Restored owner test attachment failed\n" + result.stdout + result.stderr)
        require_replay_pass(result.stdout)
        receipt["passed"] = True
        return receipt
    except Exception as error:
        receipt["error"] = repr(error)
        raise
    finally:
        try:
            if created:
                run("docker", "rm", "-f", runner, stdout=subprocess.DEVNULL)
                check = subprocess.run(["docker", "inspect", "--format", "{{.Id}}", identity or runner],
                                       text=True, capture_output=True, timeout=30)
                receipt["removed"] = check.returncode != 0 and "no such object" in (check.stdout + check.stderr).lower()
                if not receipt["removed"]:
                    raise RuntimeError("Restored owner replay container cleanup unverified")
        finally:
            if receipt_path:
                receipt_path.write_text(json.dumps(receipt, indent=2) + "\n")



def test_scope(argv):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package", choices=("storage", "cli"))
    parser.add_argument("--shard", type=int, choices=(0, 1, 2))
    args = parser.parse_args(argv)
    if args.shard is not None and args.package is None:
        parser.error("--shard requires --package")
    if args.shard == 2 and args.package != "storage":
        parser.error("the dedicated capacity group requires --package storage")
    packages = ["./internal/" + args.package] if args.package else ["./internal/storage", "./internal/cli"]
    storage = "./internal/storage" in packages
    producer = storage and args.shard in (None, 0)
    command = ["go", "run", "./testshard", "-race", "-parts=2", "-run=^TestPostgresContracts"]
    if args.shard is not None:
        command.append("-index=" + str(args.shard))
    if storage:
        command.append("-first-only=TestPostgresContractsRecoverySeed")
        command.append("-dedicated=TestPostgresContractsFeedRotationCrossesActiveCapacity")
    return command + packages, producer


def main(argv=None):
    command, producer = test_scope(argv)
    suffix = uuid.uuid4().hex[:12]
    name = "mantik-test-" + suffix
    database = "mantik_test_" + suffix
    created = False
    runner_created = False
    runner = name + "-go"
    try:
        run("docker", "run", "-d", "--rm", "--name", name,
            "-e", "POSTGRES_USER=mantik", "-e", "POSTGRES_PASSWORD=fixture-only",
            "-e", "POSTGRES_DB=" + database,
            "postgres:17", stdout=subprocess.DEVNULL)
        created = True
        deadline = time.monotonic() + 60
        while True:
            ready = subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "mantik", "-d", database],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if ready.returncode == 0:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError("disposable PostgreSQL did not become ready")
            time.sleep(0.5)
        # Share only the disposable database network namespace. Copy source
        # instead of mounting a host path: Gitea job paths may not exist on the
        # Docker daemon host. No ports, shared volumes, or existing DB are used.
        # Each test binary/group runs sequentially against this owned database.
        # Splitting complete top-level cases bounds each 30-minute suite alarm;
        # individual request/transaction deadlines and race checks are unchanged.
        run("docker", "create", "--name", runner, "--network", "container:" + name,
            "-w", "/src", "-e", "MANTIK_TEST_DATABASE_URL=" +
            f"postgres://mantik:fixture-only@127.0.0.1:5432/{database}?sslmode=disable",
            "-e", "MANTIK_RECOVERY_SEED=" + ("1" if producer else "0"),
            "golang:1.26.9", *command, stdout=subprocess.DEVNULL)
        runner_created = True
        for path in ("go.mod", "go.sum", "internal", "migrations"):
            run("docker", "cp", path, runner + ":/src/" + path)
        run("docker", "cp", "scripts/testshard", runner + ":/src/testshard")
        run("docker", "start", "-a", runner)
        state = run("docker", "inspect", "--format", "{{json .State}}", runner,
                    capture_output=True).stdout
        require_runner_exit(state)
        if producer:
            recovery_contract(name, database, lambda target, rejected: replay_restored_owner(name, runner, target, rejected))
        else:
            print("Snapshot-bound recovery proof is required separately in storage group zero")
    finally:
        if runner_created:
            run("docker", "rm", "-f", runner, stdout=subprocess.DEVNULL)
        if created:
            run("docker", "rm", "-f", name, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
