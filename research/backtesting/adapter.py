"""Offline, fixed-fixture Backtesting.py comparison; no service or broker client."""

import argparse
import contextlib
from decimal import Decimal
import hashlib
from importlib.metadata import version
import json
from pathlib import Path
import re
import sys


SCHEMA = "mantik-backtesting-fixture-comparison-v1"
TOOL = "backtesting-fixture-adapter-v1"
DATASET = "b3f9c5ae2e61f45eda833f0ea2af67565c30aa9885df5e747a696c0e41ac446d"
RECIPE = "9da1c82a0bdbdab99dcf61dd3c59cb2b70b75482639b48f953063cb7752a7d8e"
METHODS = ["demo-threshold-v1", "demo-half-size-v1",
           "no-action-control-v1", "passive-mark-control-v1"]
METRICS = ["observations", "actions", "realized_pnl_cents", "marked_pnl_cents"]
MAX_INPUT = 8 << 20
MAX_OUTPUT = 64 << 10


class Rejected(ValueError):
    """A bounded input does not satisfy this fixture's contract."""


def compact(value):
    try:
        return json.dumps(value, ensure_ascii=False, allow_nan=False,
                          separators=(",", ":")).encode("utf-8")
    except (ValueError, UnicodeError, RecursionError) as error:
        raise Rejected("unsupported_json_encoding") from error


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def object_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise Rejected("duplicate_json_key")
        result[key] = value
    return result


def reject_constant(value):
    raise Rejected("nonfinite_json_number")


def decode(raw):
    if len(raw) > MAX_INPUT:
        raise Rejected("input_limit_exceeded")
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=object_pairs,
                           parse_constant=reject_constant, parse_float=reject_constant)
    except (ValueError, UnicodeError, RecursionError) as error:
        raise Rejected("invalid_json") from error
    if not isinstance(value, dict):
        raise Rejected("expected_json_object")
    return value


def read_pinned(path, expected):
    if not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise Rejected("invalid_artifact_pin")
    if not Path(path).is_file():
        raise Rejected("expected_regular_file")
    with open(path, "rb") as stream:
        raw = stream.read(MAX_INPUT + 1)
    if len(raw) > MAX_INPUT or digest(raw) != expected:
        raise Rejected("artifact_pin_mismatch")
    return decode(raw)


def validate_inputs(dataset, recipe, native):
    # This adapter deliberately accepts only the current Go-exported fixture.
    # Its v1 seal uses Go field order, preserved by json.loads/compact here.
    for value, expected in [(dataset, DATASET), (recipe, RECIPE)]:
        seal = dict(value)
        seal["sha256"] = ""
        if value.get("sha256") != expected or digest(compact(seal)) != expected:
            raise Rejected("unsupported_fixture_identity")
    input_sha = digest(compact(dataset))
    if (set(native) != {"id", "recipe_sha256", "input_sha256", "status",
                        "evidence_class", "qualified", "results"}
            or not isinstance(native["id"], str)
            or not re.fullmatch(r"[a-zA-Z0-9_-]{1,80}", native["id"])
            or native["recipe_sha256"] != RECIPE
            or native["input_sha256"] != input_sha
            or native["status"] != "completed"
            or native["evidence_class"] != "synthetic_unqualified"
            or native["qualified"] is not False
            or not isinstance(native["results"], list)
            or len(native["results"]) != 4):
        raise Rejected("incompatible_native_attempt")
    pins = recipe["methods"] + recipe["benchmarks"]
    for pin, result, method_id in zip(pins, native["results"], METHODS):
        if (not isinstance(result, dict)
                or set(result) - {"method_id", "method_sha256", "metrics", "replay"}
                or pin["id"] != method_id
                or result.get("method_id") != method_id
                or result.get("method_sha256") != pin["sha256"]
                or not isinstance(result.get("metrics"), dict)
                or set(result["metrics"]) != set(METRICS)
                or any(type(result["metrics"][name]) is not int
                       or abs(result["metrics"][name]) > 10**12 for name in METRICS)
                or result["metrics"]["observations"] < 0
                or result["metrics"]["actions"] < 0):
            raise Rejected("incompatible_native_method")
    return pins


def cents(value):
    scaled = Decimal(str(value)) * 100
    integral = scaled.to_integral_value()
    if not scaled.is_finite() or abs(scaled - integral) > Decimal("0.000001"):
        raise Rejected("non_cent_library_value")
    return int(integral)


def compare_metrics(method_id, external, native):
    # The passive native control is mark-only (zero actions). Backtesting.py
    # opens a simulated position to obtain that mark: action counts differ.
    names = [name for name in METRICS
             if not (method_id == METHODS[3] and name == "actions")]
    return {name: {"native": native[name], "external": external[name],
                   "delta": external[name] - native[name],
                   "matches": external[name] == native[name]} for name in names}


def run_library(dataset, recipe):
    # Imports are deferred until all artifact identities have been checked.
    locked = re.findall(r"^([a-z0-9-]+)==([^\s]+)",
                        Path(__file__).with_name("requirements.lock").read_text(), re.M)
    if (sys.version_info[:3] != (3, 13, 9) or not locked
            or any(version(name) != expected for name, expected in locked)):
        raise Rejected("unsupported_environment")
    import pandas as pd
    from backtesting import Backtest, Strategy

    observations = [decode(record["original"].encode())
                    for record in dataset["records"]]
    prices = [o["price_cents"] / 100 for o in observations]
    times = pd.to_datetime([o["received_at"] for o in observations], utc=True)
    # Backtesting.py starts next() on bar 1, and settles trade_on_close orders
    # on the following iteration. Explicit copies permit first-quote decisions
    # and last-quote settlement without future prices or forced final closure.
    index = pd.DatetimeIndex([times[0] - pd.Timedelta(minutes=1), *times,
                              times[-1] + pd.Timedelta(minutes=1)])
    values = [prices[0], *prices, prices[-1]]
    frame = pd.DataFrame({name: values for name in ["Open", "High", "Low", "Close"]},
                         index=index)
    for pin in recipe["methods"] + recipe["benchmarks"]:
        method = pin["id"]
        strategy = pin["definition"].get("strategy")

        class FixtureStrategy(Strategy):
            def init(self):
                self.entered = False

            def next(self):
                bar = len(self.data) - 1
                if bar > len(observations) or method == METHODS[2]:
                    return
                price = observations[bar - 1]["price_cents"]
                if method == METHODS[3]:
                    if not self.entered:
                        self.buy(size=recipe["capital_cents"] // price)
                        self.entered = True
                    return
                if not self.entered and price <= strategy["entry_at_or_below_cents"]:
                    self.buy(size=strategy["quantity"])
                    self.entered = True
                elif self.position and (price <= strategy["safety"]["stop_cents"]
                                        or price >= strategy["safety"]["target_cents"]):
                    self.position.close()

        # Library diagnostics go to stderr; stdout contains exactly one envelope.
        with contextlib.redirect_stdout(sys.stderr):
            stats = Backtest(frame.copy(), FixtureStrategy,
                             cash=recipe["capital_cents"] / 100, commission=0,
                             spread=0, margin=1, trade_on_close=True,
                             hedging=False, exclusive_orders=True,
                             finalize_trades=False).run()
        trades = stats["_strategy"].closed_trades
        active = stats["_strategy"].trades
        actions = 2 * len(trades) + len(active)
        metrics = {"observations": len(observations), "actions": actions,
                   "realized_pnl_cents": sum(cents(t.pl) for t in trades),
                   "marked_pnl_cents": cents(stats["Equity Final [$]"]
                                             - recipe["capital_cents"] / 100)}
        ledger = [{"size": int(t.size), "entry_bar": int(t.entry_bar),
                   "entry_price_cents": cents(t.entry_price),
                   "exit_bar": None if t.exit_bar is None else int(t.exit_bar),
                   "exit_price_cents": None if t.exit_price is None else cents(t.exit_price)}
                  for t in [*trades, *active]]
        yield {"method_id": method, "method_sha256": pin["sha256"],
               "metrics": metrics, "simulated_trades": ledger}


def execute(dataset, recipe, native, pins, library_runner=run_library):
    result = {"schema": SCHEMA, "tool": TOOL,
              "library": "backtesting", "library_version": "0.6.6",
              "python_version": ".".join(map(str, sys.version_info[:3])),
              "recipe_sha256": RECIPE, "dataset_sha256": DATASET,
              "conversion": "quote_to_flat_ohlc_with_copied_boundary_bars_v1",
              "warmup_copy_bars": 1, "settlement_copy_bars": 1,
              "trade_on_close": True, "finalize_trades": False,
              "commission": 0, "spread": 0, "margin": 1,
              "status": "compared", "results": [],
              "authority": "synthetic_external_fixture_comparison_only",
              "qualification_credit": False,
              "image_identity_verified": False, "isolation_verified": False}
    try:
        for index, item in enumerate(library_runner(dataset, recipe)):
            if index >= len(pins):
                raise Rejected("extra_library_results")
            pin, baseline = pins[index], native["results"][index]
            if item["method_id"] != pin["id"] or item["method_sha256"] != pin["sha256"]:
                raise Rejected("library_method_identity_mismatch")
            item["comparisons"] = compare_metrics(pin["id"], item["metrics"], baseline["metrics"])
            item["noncomparable_metrics"] = (["actions"] if pin["id"] == METHODS[3] else [])
            result["results"].append(item)
        if len(result["results"]) != len(pins):
            raise Rejected("incomplete_library_results")
        result["all_comparable_metrics_match"] = all(
            comparison["matches"] for item in result["results"]
            for comparison in item["comparisons"].values())
    except Exception:
        # Preserve previous method results if comparison failed part way through.
        # No traceback or provider/environment values are serialized.
        result["status"] = "failed"
        result["error_code"] = "library_execution_failed"
        result["all_comparable_metrics_match"] = False
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ["dataset", "recipe", "native"]:
        parser.add_argument("--" + name, required=True)
        parser.add_argument("--" + name + "-sha256", required=True)
    args = parser.parse_args()
    try:
        inputs = {name: read_pinned(getattr(args, name), getattr(args, name + "_sha256"))
                  for name in ["dataset", "recipe", "native"]}
        pins = validate_inputs(**inputs)
        result = execute(**inputs, pins=pins)
        result["artifacts"] = {name: getattr(args, name + "_sha256")
                               for name in ["dataset", "recipe", "native"]}
        result["adapter_sha256"] = digest(Path(__file__).read_bytes())
        result["requirements_sha256"] = digest(Path(__file__).with_name("requirements.lock").read_bytes())
        result["sha256"] = digest(compact(result))
        raw = compact(result)
        if len(raw) > MAX_OUTPUT:
            raise Rejected("output_limit_exceeded")
        sys.stdout.buffer.write(raw + b"\n")
        return 0 if result["all_comparable_metrics_match"] else 1
    except (Rejected, OSError, KeyError, TypeError, OverflowError):
        print("backtesting fixture rejected; no comparison produced", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
