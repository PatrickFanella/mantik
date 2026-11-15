import datetime as dt
import hashlib
import json
from pathlib import Path
import unittest

from close_basis import auction_closes, OUTAGE_DATE, OUTAGE_NOTICE
from collect_closes import query_for


class AuctionTest(unittest.TestCase):
    def setUp(self):
        self.day = dict(date="2020-03-12", close="16:00")
        self.trade = dict(t="2020-03-12T20:00:00.119346965Z", p=248.11,
                          c=[" ", "6"], x="P", z="B", i=123, s=100)

    def page(self, trades, token=None, query=None):
        raw = json.dumps(dict(symbol="SPY", trades=trades, next_page_token=token))
        return dict(query=query or query_for(self.day), original=raw,
                    received_at="2026-11-15T19:00:00Z",
                    sha256=hashlib.sha256(raw.encode()).hexdigest())

    def test_auction_basis_ignores_other_exchange_and_late_regular_print(self):
        regular = dict(self.trade, c=[" ", "F"], p=255.24)
        other = dict(self.trade, x="Q", p=250)
        page = self.page([regular, other, self.trade, dict(self.trade)])
        result = auction_closes(Path("/unused"), [self.day], lambda _: page)
        self.assertEqual(result[0]["price"], "248.11")
        self.assertEqual(result[0]["source_sha256"], page["sha256"])
        self.assertEqual(result[0]["observed_at"], "2026-11-15T19:00:00+00:00")

    def test_primary_official_close_notice_is_a_valid_price_source(self):
        page = self.page([dict(self.trade, c=[" ", "M"])])
        result = auction_closes(Path("/unused"), [self.day], lambda _: page)
        self.assertEqual(result[0]["source_condition"], "M")
        self.assertEqual(result[0]["price"], "248.11")

    def test_invalid_evidence_never_falls_back_to_reference(self):
        cases = {
            "absent": self.page([]),

            "conflict": self.page([self.trade, dict(self.trade, p=249)]),
            "end_plus_nanosecond": self.page([dict(self.trade, t="2020-03-12T20:00:01.000000001Z")]),
            "before_start_nanosecond": self.page([dict(self.trade, t="2020-03-12T19:59:59.999999999Z")]),
            "out_of_range": self.page([dict(self.trade, t="2020-03-12T20:00:02Z")]),
            "wrong_tape": self.page([dict(self.trade, z="C")]),
            "disqualified": self.page([dict(self.trade, c=["6", "T"])]),
            "wrong_query": self.page([self.trade], query=dict(query_for(self.day), feed="iex")),
            "digest": dict(self.page([self.trade]), sha256="0"*64),
            "clock": dict(self.page([self.trade]), received_at="2019-01-01T00:00:00Z"),
            "future": dict(self.page([self.trade]), received_at="2099-01-01T00:00:00Z"),
        }
        for name, page in cases.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                auction_closes(Path("/unused"), [self.day], lambda _: page)

    def test_pagination_chain_is_bound_and_complete(self):
        first = self.page([], token="page-two")
        second = self.page([self.trade], query=dict(query_for(self.day), page_token="page-two"))
        pages = [first, second]
        result = auction_closes(Path("/unused"), [self.day], lambda p: pages[int(p.stem.rsplit("-", 1)[1])])
        self.assertEqual(result[0]["price"], "248.11")
        second["query"]["page_token"] = "different"
        with self.assertRaises(ValueError):
            auction_closes(Path("/unused"), [self.day], lambda p: pages[int(p.stem.rsplit("-", 1)[1])])

    def test_calendar_controls_early_close_and_dst(self):
        for day, expected in [
            (dict(date="2025-11-28", close="13:00"), "2025-11-28T18:00:00+00:00"),
            (dict(date="2026-03-09", close="16:00"), "2026-03-09T20:00:00+00:00"),
            (dict(date="2026-01-09", close="16:00"), "2026-01-09T21:00:00+00:00"),
        ]:
            self.assertEqual(query_for(day)["start"], expected)
        today = dt.datetime.now(dt.timezone.utc).date().isoformat()
        with self.assertRaises(ValueError):
            query_for(dict(date=today, close="23:59"))

    def test_only_documented_empty_trade_gap_uses_independent_nasdaq_close(self):
        self.day = dict(date=OUTAGE_DATE, close="16:00")
        page = self.page(None)
        gap = dict(price="288.07", source_sha256="a"*64, observed_at="2026-11-15T19:00:00+00:00")
        close = auction_closes(Path("/unused"), [self.day], lambda _: page, gap)[0]
        self.assertEqual(close["price"], "288.07")
        self.assertEqual(close["source_basis"], "nasdaq_sip_gap")
        self.assertEqual(close["explanation_url"], OUTAGE_NOTICE)
        self.assertNotIn("trade_id", close)
        self.day = dict(date="2019-08-13", close="16:00")
        page = self.page(None)
        with self.assertRaises(ValueError):
            auction_closes(Path("/unused"), [self.day], lambda _: page, gap)
