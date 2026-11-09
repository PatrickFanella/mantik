#!/usr/bin/env python3
"""Prepare or run an isolated SPY/IEX rehearsal with a real Switchyard model."""

import argparse
from datetime import datetime, timedelta, timezone
from decimal import Decimal
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import subprocess
import threading
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
import uuid

from rehearsal_model_proxy import create_proxy, private_origin


def stamp():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def save(path, value):
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as output:
        json.dump(value, output, indent=2)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())


def digest(path):
    with open(path, "rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def empty_port():
    # The actual server must still acquire the port; a bind collision fails the
    # setup rather than changing another listener or silently selecting a peer.
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


class Rehearsal:
    def __init__(self, root, binary, credentials, upstream, model):
        self.root, self.binary = root, binary
        self.credentials, self.upstream, self.model = credentials, upstream, model
        self.processes, self.logs = [], []
        self.container, self.proxy = None, None
        self.origin = f"http://127.0.0.1:{empty_port()}"
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith("MANTIK_") and k not in {"DATABASE_URL", "PGPASSWORD"}}
        self.env.update(MANTIK_API_TOKEN=secrets.token_urlsafe(32), MANTIK_PAPER_EXECUTION="on")
        self.receipt = {"schema": "mantik-runtime-rehearsal-receipt-v1", "started_at": stamp(),
                        "binary_sha256": digest(binary), "model": model, "model_upstream": upstream,
                        "evidence_class": "prospective_market_unqualified", "qualification_credit": False,
                        "phases": [], "not_covered": ["natural multi-day qualification", "profitability",
                            "passing all mandatory evaluations from a fresh short window",
                            "protection of an established position unless an eligible position is observed",
                            "archive retention beyond the rehearsal window"]}

    def command(self, *args, timeout=120):
        result = subprocess.run([str(self.binary), *map(str, args)], env=self.env,
                                capture_output=True, timeout=timeout)
        save(self.root / f"command-{time.time_ns()}.json",
             {"command": args[0], "exit_code": result.returncode,
              "stdout": result.stdout.decode(errors="replace"),
              "stderr": result.stderr.decode(errors="replace")})
        if result.returncode:
            raise RuntimeError(f"{args[0]} failed; inspect private command capture")
        return json.loads(result.stdout)

    def start(self, label, *args):
        log = open(self.root / f"{label}-{time.time_ns()}.log", "xb")
        os.chmod(log.name, 0o600)
        self.logs.append(log)
        process = subprocess.Popen([str(self.binary), *map(str, args)], env=self.env,
                                   stdout=log, stderr=subprocess.STDOUT)
        self.processes.append(process)
        self.receipt.setdefault("processes", []).append({"role": label, "pid": process.pid, "started_at": stamp()})
        return process

    @staticmethod
    def stop(process):
        if process and process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=10)

    def api(self, path, body=None, allow_unready=False):
        data = json.dumps(body).encode() if body is not None else None
        request = Request(self.origin + path, data=data,
                          headers={"Authorization": "Bearer " + self.env["MANTIK_API_TOKEN"],
                                   "Content-Type": "application/json"})
        try:
            response = urlopen(request, timeout=30)
        except HTTPError as error:
            response = error
        with response:
            raw = response.read(32 * 1024 * 1024 + 1)
            if len(raw) > 32 * 1024 * 1024:
                raise RuntimeError("rehearsal readback exceeded private capture bound")
            value = json.loads(raw)
            if response.code not in {200, 201} and not (allow_unready and response.code == 503):
                save(self.root / f"http-failure-{time.time_ns()}.json", {"path": path, "status": response.code, "body": value})
                raise RuntimeError(f"rehearsal API {path} returned {response.code}; inspect private capture")
            return value

    def serve(self):
        args = ["serve", "--listen", self.origin.removeprefix("http://"),
                "--automatic-paper-decisions", "--automatic-paper-outcomes", "--automatic-paper-evaluations"]
        if (self.root / "feed-policy.json").exists():
            args += ["--feed-readiness-policy", str(self.root / "feed-policy.json")]
        process = self.start("service", *args)
        for _ in range(100):
            if process.poll() is not None:
                raise RuntimeError("rehearsal service exited; inspect its private log")
            try:
                if self.api("/readyz").get("ready"):
                    return process
            except (URLError, OSError):
                pass
            time.sleep(0.2)
        raise RuntimeError("rehearsal service did not become ready")

    def prepare(self):
        password = secrets.token_urlsafe(24)
        database = "mantik_test_rehearsal_" + uuid.uuid4().hex[:12]
        env_file = self.root / "postgres.env"
        with os.fdopen(os.open(env_file, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as output:
            output.write(f"POSTGRES_USER=mantik\nPOSTGRES_PASSWORD={password}\nPOSTGRES_DB={database}\n")
        result = subprocess.run(["docker", "run", "-d", "--rm", "--label", "mantik.rehearsal=" + database,
                                 "--env-file", str(env_file), "-p", "127.0.0.1::5432", "postgres:17"],
                                capture_output=True, text=True, check=True, timeout=60)
        self.container = result.stdout.strip()
        save(self.root / "database-handle.json", {"container_id": self.container, "database": database})
        for _ in range(150):
            ready = subprocess.run(["docker", "exec", self.container, "pg_isready", "-h", "127.0.0.1",
                                    "-U", "mantik", "-d", database], capture_output=True)
            if ready.returncode == 0:
                break
            time.sleep(0.2)
        else:
            raise RuntimeError("new rehearsal database did not become ready")
        info = json.loads(subprocess.check_output(["docker", "inspect", self.container]))[0]
        port = info["NetworkSettings"]["Ports"]["5432/tcp"][0]["HostPort"]
        self.env["DATABASE_URL"] = f"postgres://mantik:{password}@127.0.0.1:{port}/{database}?sslmode=disable"
        self.receipt.update(database_image_id=info["Image"], database_container_id=self.container)
        self.service = self.serve()
        schema = subprocess.check_output(["docker", "exec", self.container, "psql", "-U", "mantik", "-d", database,
                                          "-Atc", "SELECT max(version) FROM mantik_schema"], text=True, timeout=10).strip()
        if not schema.isdigit():
            raise RuntimeError("rehearsal schema identity is unavailable")
        self.receipt["schema_version"] = int(schema)
        self.receipt["service_flags"] = ["--automatic-paper-decisions", "--automatic-paper-outcomes",
                                          "--automatic-paper-evaluations", "--feed-readiness-policy"]
        self.receipt["paper_execution"] = "on"
        self.owner = self.api("/v1/feed-collectors")["receiver_id"]
        self.receipt["receiver_id"] = self.owner
        journal = self.command("alpaca-journal", "--journal", self.root / "journal", "--init")
        self.receipt["journal_id"] = journal["journal_id"]
        self.api("/v1/feed-collectors", {"receiver_id": self.owner, "command": {
            "request_id": "rehearsal-register", "operation": "register", "registration_id": "rehearsal-spy",
            "registration": {"id": "rehearsal-spy", "provider": "alpaca", "feed": "iex", "symbol": "SPY",
                "evidence_class": "prospective_market_unqualified", "journal_id": journal["journal_id"],
                "access_ref": "alpaca-read-access-check", "lease_seconds": 300}}})
        seed = {"id": "rehearsal-seed", "capability": "latest_quotes", "feed": "iex", "symbols": ["SPY"],
                "access_ref": "alpaca-read-access-check", "max_quote_age_seconds": 60}
        save(self.root / "seed-spec.json", seed)
        self.command("alpaca-collect", "--journal", self.root / "journal", "--spec", self.root / "seed-spec.json",
                     "--credentials-file", self.credentials)
        captures = list((self.root / "journal").glob("capture-rehearsal-seed-*.json"))
        if len(captures) != 1:
            raise RuntimeError("seed quote capture was not retained exactly once")
        recording = json.loads(captures[0].read_text())["recording"]
        quote = json.loads(recording["original"], parse_float=Decimal)["quotes"]["SPY"]
        ask_cents = int(Decimal(str(quote["ap"])) * 100)
        if not 100 <= ask_cents <= 10000000:
            raise RuntimeError("seed quote is outside this rehearsal's declared price range")
        # Select both configurations before the window. One permits a candidate
        # near the seed quote; the other should abstain. No input is rewritten.
        variants, rules = [], []
        for name, threshold in [("rehearsal-candidate", ask_cents + 100), ("rehearsal-no-action", ask_cents // 2)]:
            definition = {"id": name, "method": "native-paper-threshold-v1", "symbol": "SPY", "feed": "iex",
                "input_evidence_class": "prospective_market_unqualified", "entry_at_or_below_cents": threshold,
                "quantity": 1, "safety": {"max_notional_cents": threshold + 1000,
                    "stop_cents": threshold // 2, "target_cents": threshold + 1000,
                    "max_holding_seconds": 60, "max_quote_age_seconds": 60}}
            record = self.api("/v1/paper-strategies", {"receiver_id": self.owner, "definition": definition})["record"]
            variants.append({"id": name, "sha256": record["sha256"]})
            rules.append({"id": name, "artifact_sha256": record["definition_sha256"],
                          "limit_cents": 1000000, "priority": 0})
        account = self.api("/v1/paper-accounts", {"receiver_id": self.owner, "config": {
            "id": "rehearsal-paper", "currency": "USD", "opening_cash_cents": 1000000, "capital_limit_cents": 1000000}})["account"]
        model = {"method": "usd-equity-displayed-quote-v1", "liquidity_bps": 1000, "max_quote_age_seconds": 60}
        policy = self.api("/v1/allocation-policies", {"receiver_id": self.owner, "account_id": "rehearsal-paper",
            "execution_model": model, "policy": {"id": "rehearsal-policy", "account_sha256": account["sha256"],
                "max_snapshot_age_seconds": 60, "strategies": rules,
                "symbols": [{"id": "SPY", "limit_cents": 1000000, "groups": []}], "groups": []}})["record"]
        start = datetime.now(timezone.utc) + timedelta(seconds=30)
        end = start + timedelta(minutes=15)
        deadline = end + timedelta(minutes=2)
        iso = lambda value: value.isoformat().replace("+00:00", "Z")
        stress = {**model, "fee_bps": 1}
        self.api("/v1/paper-evaluation-declarations", {"id": "rehearsal-plan", "receiver_id": self.owner,
            "recipe": "native-threshold-paper-evaluation-plan-v1", "policy_id": "rehearsal-policy",
            "policy_sha256": policy["sha256"], "plan": {"family_id": "rehearsal-family", "variants": variants,
                "window_start": iso(start), "window_end": iso(end), "outcome_deadline": iso(deadline),
                "min_independent_decisions": 30, "minimum_net_excess_cents": 0, "maximum_drawdown_cents": 10000,
                "admission_ttl_seconds": 3600, "benchmarks": ["cash_no_action", "passive_same_symbol"], "stress_model": stress}})
        save(self.root / "session.json", {"id": "rehearsal", "count": 8, "interval_seconds": 30,
            "collection": {**seed, "id": ""}})
        save(self.root / "feed-policy.json", {"schema": "mantik-feed-readiness-policy-v1", "receiver_id": self.owner,
            "required_registrations": ["rehearsal-spy"]})
        self.stop(self.service)
        self.service = self.serve()
        self.proxy = create_proxy(self.upstream, self.root / "model-proxy")
        threading.Thread(target=self.proxy.serve_forever, daemon=True).start()
        save(self.root / "model-proxy" / "model.json", {"endpoint": f"http://127.0.0.1:{self.proxy.server_port}",
            "model": self.model, "max_tokens": 1024})
        self.receipt["declaration_window"] = {"start": iso(start), "end": iso(end), "outcome_deadline": iso(deadline)}

    def collector(self):
        return self.start("collector", "alpaca-collector-loop", "--api-url", self.origin, "--receiver-id", self.owner,
            "--collector-registration-id", "rehearsal-spy", "--journal", self.root / "journal",
            "--archive-root", self.root / "journal-archives", "--spec", self.root / "session.json",
            "--credentials-file", self.credentials, "--regular-session-seconds", "240")

    def snapshot(self, phase):
        result = {"at": stamp(), "phase": phase, "readbacks": {}}
        for path in ["/v1/feed-collectors", "/v1/feed-collectors/rehearsal-spy/coverage",
                     "/v1/paper-decisions", "/v1/paper-outcomes", "/v1/paper-evaluation-assessments",
                     "/v1/allocation-comparisons", "/v1/paper-attribution", "/v1/status",
                     "/readyz?scope=feed_coverage", "/readyz?scope=paper_decision"]:
            result["readbacks"][path] = self.api(path, allow_unready=True)
        save(self.root / f"snapshot-{phase}-{time.time_ns()}.json", result)
        self.receipt["phases"].append({"phase": phase, "at": result["at"]})
        return result

    def run(self, preflight, duration):
        self.prepare()
        self.snapshot("prepared")
        if preflight:
            request = Request(f"http://127.0.0.1:{self.proxy.server_port}/v1/chat/completions",
                data=json.dumps({"model": self.model, "messages": [{"role": "user", "content": "Reply with exactly: ok"}],
                                 "stream": False, "max_tokens": 16, "temperature": 0}).encode(),
                headers={"Content-Type": "application/json"})
            with urlopen(request, timeout=70) as response:
                result = json.load(response)
            save(self.root / "model-smoke.json", result)
            if result.get("choices", [{}])[0].get("message", {}).get("content", "").strip() != "ok":
                raise RuntimeError("real model smoke did not answer ok; inspect private response")
            self.receipt["mode"] = "preflight_only"
            self.receipt["not_covered"] += ["regular-session autonomous collection", "real Mantik model-review contract",
                "model outage recovery", "feed outage recovery", "post-decision service restart"]
            return
        self.receipt["mode"] = "market_rehearsal"
        collector = self.collector()
        self.start("research", "paper-research-loop", "--api-url", self.origin, "--receiver-id", self.owner,
            "--model-config", self.root / "model-proxy" / "model.json", "--capture-root", self.root / "reviews",
            "--interval-seconds", "30")
        began, phase = time.monotonic(), "baseline"
        transitions = [(240, "model_outage"), (360, "model_recovery"), (480, "feed_outage"),
                       (630, "feed_recovery"), (750, "service_restart")]
        while time.monotonic() - began < duration:
            elapsed = time.monotonic() - began
            if transitions and elapsed >= transitions[0][0]:
                _, phase = transitions.pop(0)
                if phase == "model_outage":
                    (self.root / "model-proxy" / "outage").touch(mode=0o600)
                elif phase == "model_recovery":
                    (self.root / "model-proxy" / "outage").unlink()
                elif phase == "feed_outage":
                    self.stop(collector)
                elif phase == "feed_recovery":
                    collector = self.collector()
                elif phase == "service_restart":
                    self.stop(collector)
                    before = self.snapshot("before_restart")
                    self.stop(self.service)
                    self.service = self.serve()
                    after = self.snapshot("after_restart")
                    for path in ["/v1/paper-decisions", "/v1/paper-outcomes", "/v1/paper-evaluation-assessments"]:
                        original = {r["command"]["id"]: r for r in before["readbacks"][path]["records"]}
                        retained = {r["command"]["id"]: r for r in after["readbacks"][path]["records"]}
                        if any(retained.get(key) != record for key, record in original.items()):
                            raise RuntimeError("restart changed a previously retained record")
                    self.receipt["restart_retained_records_match"] = True
                    collector = self.collector()
            self.snapshot(phase)
            if self.service.poll() is not None:
                raise RuntimeError("rehearsal service stopped unexpectedly")
            time.sleep(min(30, max(0, duration - (time.monotonic() - began))))
        final = self.snapshot("finished")
        self.receipt["decisions"] = [{"id": r["command"]["id"], "sha256": r["sha256"],
            "action": r["evaluation"]["action"], "registration_id": r["command"]["registration_id"],
            "quote_input_id": r["evaluation"]["coverage"].get("quote", {}).get("input_id")}
            for r in final["readbacks"]["/v1/paper-decisions"]["records"]]
        self.receipt["comparisons"] = [{"id": r["command"]["id"], "sha256": r["sha256"],
            "decision_pins": r["command"]["decisions"],
            "choices": [{"decision_id": c["proposal"]["id"], "status": c["status"], "reasons": c["reasons"]}
                        for c in r["entry"]["plan"]["decisions"]]}
            for r in final["readbacks"]["/v1/allocation-comparisons"]["records"]]

    def cleanup(self):
        failures = []
        for process in reversed(self.processes):
            try:
                self.stop(process)
            except (OSError, subprocess.TimeoutExpired) as error:
                failures.append(type(error).__name__)
        if self.proxy:
            try:
                self.proxy.shutdown()
                self.proxy.server_close()
            except OSError:
                failures.append("proxy_cleanup_failed")
        if self.container:
            try:
                dump = self.root / "database.dump"
                with open(dump, "xb") as output:
                    os.chmod(dump, 0o600)
                    result = subprocess.run(["docker", "exec", self.container, "pg_dump", "-U", "mantik", "-Fc",
                                             "--dbname", self.env["DATABASE_URL"].split("/")[-1].split("?")[0]],
                                            stdout=output, stderr=subprocess.DEVNULL, timeout=120)
                    if result.returncode:
                        failures.append("database_dump_failed")
                    else:
                        self.receipt["database_dump_sha256"] = digest(dump)
            except (KeyError, OSError, subprocess.TimeoutExpired):
                failures.append("database_dump_failed")
            finally:
                try:
                    result = subprocess.run(["docker", "rm", "-f", self.container], capture_output=True, timeout=30)
                    absent = subprocess.run(["docker", "inspect", self.container], capture_output=True, timeout=30)
                    if result.returncode or absent.returncode == 0 or b"no such object" not in absent.stderr.lower():
                        failures.append("database_cleanup_unverified")
                except (OSError, subprocess.TimeoutExpired):
                    failures.append("database_cleanup_unverified")
        for log in self.logs:
            log.close()
        self.receipt["cleanup"] = {"all_recorded_processes_exited": all(p.poll() is not None for p in self.processes),
                                   "failures": failures}
        if failures:
            self.receipt["state"] = "failed"
        self.receipt["finished_at"] = stamp()
        save(self.root / "receipt.json", self.receipt)
        if failures:
            raise RuntimeError("rehearsal cleanup needs inspection at " + str(self.root))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--credentials-file", type=Path, required=True)
    parser.add_argument("--upstream", required=True, type=private_origin)
    parser.add_argument("--model", required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--preflight", action="store_true")
    parser.add_argument("--duration-seconds", type=int, default=1200)
    args = parser.parse_args()
    if not 1200 <= args.duration_seconds <= 4500:
        parser.error("market rehearsal duration must be 1200–4500 seconds")
    repo = Path(__file__).resolve().parents[1]
    root = args.root.resolve()
    if not args.root.is_absolute() or root == repo or repo in root.parents:
        parser.error("use a new absolute private state directory outside Git")
    if not args.binary.is_absolute() or not args.credentials_file.is_absolute():
        parser.error("binary and credential paths must be absolute")
    if not args.model.startswith("ollama/") or "cloud" in args.model.lower():
        parser.error("use an explicit local ollama/ model")
    if not re.fullmatch("[a-f0-9]{40}", args.source_commit):
        parser.error("source commit must be the full immutable source hash")
    if args.credentials_file.stat().st_mode & 0o777 != 0o600:
        parser.error("credential file must have mode 0600")
    if shutil.disk_usage(root.parent).free < 2 * 1024**3:
        parser.error("less than 2 GiB free for isolated rehearsal evidence")
    root.mkdir(mode=0o700, exist_ok=False)
    rehearsal = Rehearsal(root, args.binary, args.credentials_file, args.upstream, args.model)
    rehearsal.receipt["source_commit"] = args.source_commit
    rehearsal.receipt["harness_sha256"] = digest(Path(__file__))
    rehearsal.receipt["proxy_sha256"] = digest(Path(__file__).with_name("rehearsal_model_proxy.py"))
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    try:
        rehearsal.run(args.preflight, args.duration_seconds)
        rehearsal.receipt["state"] = "completed"
    except BaseException as error:
        rehearsal.receipt["state"] = "failed"
        rehearsal.receipt["error_type"] = type(error).__name__
        raise
    finally:
        rehearsal.cleanup()
        print(json.dumps({"receipt": str(root / "receipt.json"), "state": rehearsal.receipt.get("state"),
                          "qualification_credit": False}), flush=True)


if __name__ == "__main__":
    main()
