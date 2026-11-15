from decimal import Decimal
import io
import unittest
import zipfile

from reference import lean_closes, stitched_reference, nasdaq_gap


class ReferenceTest(unittest.TestCase):
    def test_sample_coverage_sets_boundary_without_price_selection(self):
        sample = {"2016-01-04": Decimal(100), "2016-01-05": Decimal(101)}
        yahoo = {"2016-01-04": Decimal(900), "2016-01-05": Decimal(999), "2016-01-06": Decimal(102)}
        dividends = [dict(ex_date="2016-01-06", pay_date="2016-01-06", amount="1")]
        adjusted, total, cutoff = stitched_reference(sample, yahoo, list(yahoo), dividends)
        self.assertEqual(cutoff, "2016-01-05")
        self.assertEqual(adjusted[0]["close"], "99.009900990099")
        self.assertEqual(adjusted[1]["close"], "100.000000000000")
        self.assertEqual(total, "1.030000000000")
        del sample["2016-01-04"]
        with self.assertRaisesRegex(ValueError, "missing date"):
            stitched_reference(sample, yahoo, list(yahoo), dividends)

    def test_sample_scale_and_date_validation(self):
        def archive(text):
            out = io.BytesIO()
            with zipfile.ZipFile(out, "w") as z:
                z.writestr("spy.csv", text)
            return out.getvalue()
        self.assertEqual(lean_closes(archive("20160104 00:00,1,2,1,1000000,1\n")),
                         {"2016-01-04": Decimal(100)})
        with self.assertRaises(ValueError):
            lean_closes(archive("20160104 00:00,1,2,1,-1,1\n"))
        with self.assertRaises(ValueError):
            lean_closes(archive("20160104 00:00,1,2,1,100,1\n"*2))

    def test_nasdaq_gap_requires_complete_independent_response(self):
        source = dict(status=dict(rCode=200), data=dict(symbol="SPY", totalRecords=1,
            tradesTable=dict(rows=[dict(date="08/12/2019", close="288.07")])))
        self.assertEqual(nasdaq_gap(source), "288.07")
        for text, expected in [("$288.07", "288.07"), ("$1,288.07", "1288.07")]:
            source["data"]["tradesTable"]["rows"][0]["close"] = text
            self.assertEqual(nasdaq_gap(source), expected)
        source["data"]["totalRecords"] = 2
        with self.assertRaises(ValueError):
            nasdaq_gap(source)
