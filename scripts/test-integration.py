#!/usr/bin/env python3
"""Run contracts on a uniquely named, disposable PostgreSQL container."""
import subprocess
import time
import uuid


def run(*args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def main():
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
        # Package concurrency shares this one database server and CPU budget.
        # Keep packages sequential; each package still exercises its own
        # concurrent transactions and the race detector remains enabled.
        run("docker", "create", "--name", runner, "--network", "container:" + name,
            "-w", "/src", "-e", "MANTIK_TEST_DATABASE_URL=" +
            f"postgres://mantik:fixture-only@127.0.0.1:5432/{database}?sslmode=disable",
            "golang:1.26.9", "go", "test", "-race", "-p", "1", "./internal/storage", "./internal/cli",
            "-run", "TestPostgresContracts", "-count=1", stdout=subprocess.DEVNULL)
        runner_created = True
        for path in ("go.mod", "go.sum", "internal", "migrations"):
            run("docker", "cp", path, runner + ":/src/" + path)
        run("docker", "start", "-a", runner)
        exit_code = run("docker", "inspect", "--format", "{{.State.ExitCode}}", runner,
                        capture_output=True).stdout.strip()
        if exit_code != "0":
            raise RuntimeError("PostgreSQL contract test container failed: " + exit_code)
    finally:
        if runner_created:
            run("docker", "rm", "-f", runner, stdout=subprocess.DEVNULL)
        if created:
            run("docker", "rm", "-f", name, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
