"""Retain a finite offline adapter rehearsal in new, owned Docker containers."""

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time
import uuid


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--mantik", required=True)
    parser.add_argument("--image-sha256", required=True)
    parser.add_argument("--out-dir", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", args.image_sha256):
        parser.error("an existing immutable local image ID is required")
    os.umask(0o077)
    root = Path(args.out_dir).absolute()
    root.mkdir(mode=0o700)  # Never reuse an earlier attempt.
    root = root.resolve()
    if "," in str(root) or "\n" in str(root):
        raise ValueError("unsupported mount path")
    binary = str(Path(args.mantik).resolve(strict=True))
    owner = uuid.uuid4().hex
    config = root / "docker-config"
    config.mkdir()
    env = {"PATH": "/usr/local/bin:/usr/bin:/bin",
           "DOCKER_HOST": "unix:///var/run/docker.sock", "DOCKER_CONFIG": str(config)}
    records, handles = [], []

    def command(argv, timeout=10, command_env=None):
        index = len(records)
        record = {"argv": argv, "started_monotonic": time.monotonic()}
        records.append(record)
        try:
            process = subprocess.run(argv, capture_output=True, timeout=timeout,
                                     env=command_env, check=False)
            record["returncode"] = process.returncode
            stdout, stderr = process.stdout, process.stderr
        except subprocess.TimeoutExpired as error:
            record["timed_out"] = True
            stdout, stderr = error.stdout or b"", error.stderr or b""
            raise
        finally:
            record["finished_monotonic"] = time.monotonic()
            if "stdout" in locals():
                (root / f"command-{index:03}.stdout").write_bytes(stdout)
                (root / f"command-{index:03}.stderr").write_bytes(stderr)
            (root / "commands.json").write_text(json.dumps(records, indent=2))
        return process

    def docker(*argv, timeout=10):
        return command(["/usr/bin/docker", *argv], timeout, env)

    def inspect(handle):
        process = docker("container", "inspect", handle)
        assert process.returncode == 0, "container inspection unavailable"
        state = json.loads(process.stdout)[0]
        assert state["Id"] == handle and state["Image"] == args.image_sha256
        assert state["Config"]["Labels"]["tv.subcult.mantik.comparison"] == owner
        return state

    def run_case(label, artifacts, expected_exit, pin_overrides=None):
        directory = root / label
        directory.mkdir(mode=0o755)
        directory.chmod(0o755)
        worker_args = []
        for name, raw in artifacts.items():
            path = directory / (name + ".json")
            path.write_bytes(raw)
            path.chmod(0o444)
            pin = (pin_overrides or {}).get(name, sha(raw))
            worker_args.extend(["--" + name, "/input/" + path.name,
                                "--" + name + "-sha256", pin])
        created = docker("create", "--pull=never", "--name", f"mantik-bt-{owner}-{label}",
                         "--label", "tv.subcult.mantik.comparison=" + owner,
                         "--read-only", "--network", "none", "--cap-drop", "ALL",
                         "--security-opt", "no-new-privileges:true", "--pids-limit", "64",
                         "--memory", "268435456", "--memory-swap", "268435456",
                         "--cpus", "1", "--user", "65532:65532", "--restart", "no",
                         "--log-driver", "local", "--log-opt", "max-size=20m",
                         "--log-opt", "max-file=1", "--log-opt", "compress=false",
                         "--mount", f"type=bind,src={directory},dst=/input,readonly",
                         args.image_sha256, *worker_args)
        assert created.returncode == 0, "create failed; inspect retained diagnostics"
        handle = created.stdout.decode().strip()
        assert re.fullmatch(r"[0-9a-f]{64}", handle)
        handles.append(handle)
        # Retain actual host settings before start. This is a test driver, not
        # a replacement for the durable production launcher or its ownership API.
        state = inspect(handle)
        host = state["HostConfig"]
        assert not state["State"]["Running"] and state["State"]["Pid"] == 0
        assert host["ReadonlyRootfs"] and host["NetworkMode"] == "none"
        assert host["CapDrop"] == ["ALL"] and not host["Privileged"]
        assert host["SecurityOpt"] == ["no-new-privileges:true"]
        assert host["Memory"] == host["MemorySwap"] == 268435456
        assert host["NanoCpus"] == 1000000000 and host["PidsLimit"] == 64
        assert state["Config"]["User"] == "65532:65532"
        assert state["Config"]["Entrypoint"] == ["python", "-I", "/adapter/adapter.py"]
        assert state["Config"]["Cmd"] == worker_args
        assert len(state["Mounts"]) == 1 and not state["Mounts"][0]["RW"]
        assert state["Mounts"][0]["Source"] == str(directory)
        assert state["Mounts"][0]["Destination"] == "/input"
        (directory / "pre-start.json").write_text(json.dumps(state, indent=2))
        started = docker("start", handle)
        assert started.returncode == 0
        waited = docker("wait", handle, timeout=30)
        assert waited.returncode == 0
        state = inspect(handle)
        assert not state["State"]["Running"] and state["State"]["Pid"] == 0
        assert state["State"]["ExitCode"] == expected_exit
        output = docker("logs", handle)
        assert output.returncode == 0
        assert len(output.stdout) <= 64 << 10 and len(output.stderr) <= 64 << 10
        (directory / "stdout.json").write_bytes(output.stdout)
        (directory / "stderr.txt").write_bytes(output.stderr)
        (directory / "terminal.json").write_text(json.dumps(state, indent=2))
        if expected_exit == 2:
            assert output.stdout == b"", "rejected input produced a comparison"
            return None
        result = json.loads(output.stdout)
        result_pin = result.pop("sha256")
        raw = json.dumps(result, ensure_ascii=False, separators=(",", ":")).encode()
        assert sha(raw) == result_pin
        assert result["qualification_credit"] is False
        assert result["isolation_verified"] is False
        assert result["image_identity_verified"] is False
        assert result["adapter_sha256"] == sha(Path(__file__).with_name("adapter.py").read_bytes())
        assert result["requirements_sha256"] == sha(Path(__file__).with_name("requirements.lock").read_bytes())
        assert result["library_version"] == "0.6.6" and result["python_version"] == "3.13.9"
        assert result["artifacts"] == {name: sha(raw) for name, raw in artifacts.items()}
        return output.stdout, result

    try:
        dataset = command([binary, "replay", "--export-dataset"])
        recipe = command([binary, "experiment", "--export-recipe"])
        native = command([binary, "experiment", "--attempt", "backtesting-reference",
                          "--out-dir", str(root / "native")], timeout=30)
        assert all(p.returncode == 0 for p in [dataset, recipe, native])
        artifacts = {"dataset": dataset.stdout, "recipe": recipe.stdout, "native": native.stdout}
        first, result = run_case("completed", artifacts, 0)
        second, _ = run_case("repeated", artifacts, 0)
        assert first == second, "physical retries changed logical result identity"
        assert result["status"] == "compared" and result["all_comparable_metrics_match"]
        assert [r["metrics"]["marked_pnl_cents"] for r in result["results"]] == [-6000, -3000, 0, -7200]
        for index, quantity in enumerate([10, 5]):
            assert result["results"][index]["simulated_trades"] == [{
                "size": quantity, "entry_bar": 2, "entry_price_cents": 10000,
                "exit_bar": 4, "exit_price_cents": 9400}]
        assert result["results"][2]["simulated_trades"] == []
        assert result["results"][3]["simulated_trades"] == [{
            "size": 9, "entry_bar": 1, "entry_price_cents": 10200,
            "exit_bar": None, "exit_price_cents": None}]
        assert result["results"][3]["noncomparable_metrics"] == ["actions"]
        assert result["results"][3]["metrics"]["actions"] == 1
        # A native mismatch must be retained as a complete, unsuccessful comparison.
        changed = json.loads(native.stdout)
        changed["results"][0]["metrics"]["marked_pnl_cents"] += 1
        changed_raw = json.dumps(changed).encode()
        _, mismatch = run_case("mismatch", artifacts | {"native": changed_raw}, 1)
        assert mismatch["status"] == "compared" and len(mismatch["results"]) == 4
        assert not mismatch["all_comparable_metrics_match"]
        assert mismatch["results"][0]["comparisons"]["marked_pnl_cents"]["delta"] == -1
        run_case("wrong-pin", artifacts, 2, {"dataset": "0" * 64})
        bad_dataset = copy.deepcopy(json.loads(dataset.stdout))
        bad_dataset["records"][0]["original"] += " "
        run_case("changed-fixture", artifacts | {"dataset": json.dumps(bad_dataset).encode()}, 2)
        run_case("duplicate-json", artifacts | {"native": b'{"id":"x","id":"y"}'}, 2)
        for label, mutate in [
            ("qualified", lambda n: n.update(qualified=True)),
            ("missing-method", lambda n: n["results"].pop()),
            ("null-metric", lambda n: n["results"][0]["metrics"].update(actions=None)),
            ("failed-native", lambda n: n.update(status="failed")),
            ("changed-method", lambda n: n["results"].reverse()),
        ]:
            value = json.loads(native.stdout)
            mutate(value)
            run_case(label, artifacts | {"native": json.dumps(value).encode()}, 2)
        receipt = {"status": "passed", "owner": owner, "image_sha256": args.image_sha256,
                   "mantik_sha256": sha(Path(binary).read_bytes()),
                   "cases": len(handles), "commands": len(records),
                   "qualification_credit": False, "containers": handles}
    except BaseException as error:
        (root / "failed-attempt.json").write_text(json.dumps({"error_type": type(error).__name__,
                                                            "error": str(error)}))
        raise
    finally:
        terminal = []
        for handle in handles:
            state = inspect(handle)
            if state["State"]["Running"]:
                stopped = docker("kill", handle)
                assert stopped.returncode == 0
                state = inspect(handle)
            assert not state["State"]["Running"] and state["State"]["Pid"] == 0
            terminal.append({"id": handle, "image": state["Image"],
                             "running": state["State"]["Running"], "pid": state["State"]["Pid"]})
        (root / "terminal-handles.json").write_text(json.dumps(terminal, indent=2))

    # A passing receipt is written only after actual terminal handle checks.
    receipt["commands"] = len(records)
    (root / "rehearsal-result.json").write_text(json.dumps(receipt, indent=2))
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
