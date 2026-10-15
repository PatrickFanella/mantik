import copy
from decimal import InvalidOperation
import json
from pathlib import Path
import tempfile
import unittest

import adapter


class AdapterTests(unittest.TestCase):
    def test_strict_bounded_json(self):
        for raw in [b'{"a":1,"a":2}', b'{"a":NaN}', b'{"a":Infinity}', b'{"a":1e999}',
                    b'{}{}', b'[]', b'"string"', b'\xff',
                    b'[' * 2000 + b']' * 2000, b' ' * (adapter.MAX_INPUT + 1)]:
            with self.subTest(raw=raw[:20]), self.assertRaises(adapter.Rejected):
                adapter.decode(raw)
        with self.assertRaises(adapter.Rejected):
            adapter.compact({"bad": "\ud800"})

    def test_raw_artifact_pin_and_size(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "input.json"
            path.write_bytes(b'{"value":1}\n')
            raw_pin = adapter.digest(path.read_bytes())
            self.assertEqual(adapter.read_pinned(path, raw_pin), {"value": 1})
            path.write_bytes(b'{ "value":1}\n')
            with self.assertRaises(adapter.Rejected):
                adapter.read_pinned(path, raw_pin)
            with self.assertRaises(adapter.Rejected):
                adapter.read_pinned(path, "not-a-pin")
            path.write_bytes(b' ' * (adapter.MAX_INPUT + 1))
            with self.assertRaises(adapter.Rejected):
                adapter.read_pinned(path, adapter.digest(path.read_bytes()))

    def test_money_conversion_requires_cents(self):
        self.assertEqual(adapter.cents(-60), -6000)
        self.assertEqual(adapter.cents(-72.00000000000001), -7200)
        for value in [1.234, float("inf"), float("nan")]:
            with self.subTest(value=value), self.assertRaises((adapter.Rejected, InvalidOperation)):
                adapter.cents(value)

    def test_passive_actions_are_explicitly_not_comparable(self):
        native = dict(zip(adapter.METRICS, [4, 0, 0, -7200]))
        external = native | {"actions": 1}
        compared = adapter.compare_metrics(adapter.METHODS[3], external, native)
        self.assertNotIn("actions", compared)
        self.assertTrue(all(c["matches"] for c in compared.values()))
        external["marked_pnl_cents"] = 7200
        self.assertFalse(adapter.compare_metrics(adapter.METHODS[3], external, native)
                         ["marked_pnl_cents"]["matches"])

    def test_mismatch_failure_and_partial_results_are_retained(self):
        pins = [{"id": name, "sha256": str(i)}
                for i, name in enumerate(adapter.METHODS)]
        metrics = dict(zip(adapter.METRICS, [4, 2, -6000, -6000]))
        native = {"results": [{"metrics": metrics.copy()} for _ in pins]}
        external = [{"method_id": p["id"], "method_sha256": p["sha256"],
                     "metrics": metrics.copy(), "simulated_trades": []} for p in pins]
        external[1]["metrics"]["marked_pnl_cents"] = -5999
        result = adapter.execute({}, {}, native, pins, lambda *_: copy.deepcopy(external))
        self.assertEqual(result["status"], "compared")
        self.assertFalse(result["all_comparable_metrics_match"])
        self.assertEqual(result["results"][1]["comparisons"]["marked_pnl_cents"]["delta"], 1)
        self.assertFalse(result["qualification_credit"])

        def fails(*_):
            yield copy.deepcopy(external[0])
            raise RuntimeError("canary must not appear in output")

        result = adapter.execute({}, {}, native, pins, fails)
        self.assertEqual(result["status"], "failed")
        self.assertEqual(len(result["results"]), 1)
        self.assertNotIn("canary", json.dumps(result))
        result = adapter.execute({}, {}, native, pins, lambda *_: [])
        self.assertEqual(result["status"], "failed")


if __name__ == "__main__":
    unittest.main()
