import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from prepare import prepare


class PrepareTest(unittest.TestCase):
    def test_pay_date_return_waits_for_payment_and_keeps_hashes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            journal = root / "journal"
            journal.mkdir()
            dates = ["2020-01-02", "2020-01-03", "2020-01-06"]
            raw = json.dumps({"bars": {"SPY": [dict(t=d+"T05:00:00Z", o=100, h=100,
                l=100, c=100, vw=100, v=1, n=1) for d in dates]}})
            (journal / "history-test.json").write_text(json.dumps(dict(
                query=dict(Capability="daily_bars", Symbols=["SPY"]),
                response=dict(Original=raw, SHA256=hashlib.sha256(raw.encode()).hexdigest(),
                              ReceivedAt="2020-02-01T00:00:00Z"))))
            dividends = root / "dividends.json"
            dividends.write_text(json.dumps(dict(status="OK", results=[dict(ticker="SPY",
                currency="USD", dividend_type="CD", ex_dividend_date="2020-01-03",
                pay_date="2020-01-06", cash_amount=1, id="dividend")])) )
            reference = root / "reference.json"
            reference.write_text(json.dumps(dict(chart=dict(result=[dict(
                timestamp=[1577961000, 1578047400, 1578306600],
                indicators=dict(adjclose=[dict(adjclose=[99, 100, 110])],
                                quote=[dict(close=[100, 100, 110])]))]))))
            calendar = root / "calendar.json"
            calendar.write_text(json.dumps([dict(date=d) for d in dates]))
            output = root / "input.json"
            args = SimpleNamespace(journal=journal, dividends=dividends, reference=reference,
                calendar=calendar, output=output, start=dates[0], end=dates[-1], asset_id="spy")
            prepare(args)
            data = json.loads(output.read_text())
            # One $1 entitlement waits until Jan 6's $110 close: value $111.
            self.assertEqual(data["reference_pay_date_return_multiple"], "1.110000000000")
            manifest = json.loads(output.with_suffix(".manifest.json").read_text())
            self.assertEqual(manifest["input_sha256"], hashlib.sha256(output.read_bytes()).hexdigest())
            self.assertEqual(len(manifest["sources"]), 4)
            page = json.loads((journal / "history-test.json").read_text())
            page["response"]["SHA256"] = "0" * 64
            (journal / "history-test.json").write_text(json.dumps(page))
            with self.assertRaisesRegex(ValueError, "digest mismatch"):
                prepare(args)
