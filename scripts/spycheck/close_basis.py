"""Validate retained SIP auction windows independently of Yahoo prices."""
import datetime as dt
import hashlib
import json
import re
from decimal import Decimal

from collect_closes import query_for


def instant(text):
    match = re.fullmatch(r"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})", text)
    if not match:
        raise ValueError("invalid trade timestamp")
    base = dt.datetime.fromisoformat(match[1]+match[3]).astimezone(dt.timezone.utc)
    elapsed = base - dt.datetime(1970, 1, 1, tzinfo=dt.timezone.utc)
    return Decimal(elapsed.days*86400+elapsed.seconds) + Decimal("0."+(match[2] or "0"))


OUTAGE_DATE = "2019-08-12"
OUTAGE_NOTICE = "https://www.nasdaqtrader.com/TraderNews.aspx?id=ETA2019-68"


def auction_closes(directory, calendar, load, gap=None):
    closes = []
    for day in calendar:
        query = query_for(day)
        prices = set()
        selected = None
        trade_count = 0
        last_digest = None
        observed = None
        seen = set()
        for number in range(10):
            page = load(directory / f'{day["date"]}-{number}.json')
            if page["query"] != query or hashlib.sha256(page["original"].encode()).hexdigest() != page["sha256"]:
                raise ValueError("auction source query/digest mismatch")
            result = json.loads(page["original"], parse_float=Decimal)
            at = dt.datetime.fromisoformat(page["received_at"])
            end = dt.datetime.fromisoformat(query["end"])
            if at.tzinfo is None or at < end or at > dt.datetime.now(dt.timezone.utc):
                raise ValueError("invalid auction acquisition clock")
            observed = max(observed, at) if observed else at
            if result.get("symbol") != "SPY" or "trades" not in result or result["trades"] is not None and not isinstance(result["trades"], list) or "next_page_token" not in result:
                raise ValueError("invalid SPY auction envelope")
            trades = result["trades"] or []
            trade_count += len(trades)
            last_digest = page["sha256"]
            for trade in trades:
                occurred = instant(trade["t"])
                if not instant(query["start"]) <= occurred <= instant(query["end"]):
                    raise ValueError("auction trade outside requested window")
                if trade.get("x") != "P" or not (set(trade.get("c", [])) & {"6", "M"}):
                    continue
                if trade.get("z") != "B" or set(trade["c"]) - {" ", "6", "M"} or not isinstance(trade.get("i"), int) or trade["i"] <= 0:
                    raise ValueError("unsupported primary auction print")
                price = Decimal(str(trade["p"]))
                if not price.is_finite() or price <= 0:
                    raise ValueError("invalid primary auction price")
                prices.add(price)
                if selected is None:
                    selected = dict(date=day["date"], source_basis="primary_exchange_close", source_condition="M" if "M" in trade["c"] else "6", close_at=query["start"],
                                    occurred_at=trade["t"], price=str(price),
                                    trade_id=str(trade["i"]), source_sha256=page["sha256"])
            token = result["next_page_token"]
            if token is None:
                break
            if not isinstance(token, str) or not token or token in seen or len(token) > 4096:
                raise ValueError("invalid auction pagination")
            seen.add(token)
            query["page_token"] = token
        else:
            raise ValueError("auction source exceeded ten pages")
        if not prices and trade_count == 0 and day["date"] == OUTAGE_DATE and gap:
            selected = dict(date=OUTAGE_DATE, source_basis="nasdaq_sip_gap", close_at=query["start"],
                            price=gap["price"], source_sha256=gap["source_sha256"], gap_source_sha256=last_digest,
                            explanation_url=OUTAGE_NOTICE)
        elif len(prices) != 1:
            raise ValueError("missing or conflicting primary auction price for "+day["date"])
        selected["observed_at"] = gap["observed_at"] if selected["source_basis"]=="nasdaq_sip_gap" else observed.isoformat()
        closes.append(selected)
    return closes
