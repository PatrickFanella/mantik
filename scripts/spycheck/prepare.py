#!/usr/bin/env python3
"""Normalize retained sources for spycheck without network or credentials.

Usage: python3 scripts/spycheck/prepare.py --help
Original responses and the generated input/manifest stay outside Git.
"""
import argparse
import datetime as dt
import hashlib
import json
from decimal import Decimal
from pathlib import Path


def prepare(args):
    sources = []

    def load(path):
        raw = path.read_bytes()
        sources.append({"file": str(path), "sha256": hashlib.sha256(raw).hexdigest()})
        return json.loads(raw)

    bars = {}
    for path in sorted(args.journal.glob("history-*.json")):
        page = load(path)
        query, response = page["query"], page["response"]
        if query["Capability"] != "daily_bars" or query["Symbols"] != ["SPY"]:
            continue
        if hashlib.sha256(response["Original"].encode()).hexdigest() != response["SHA256"]:
            raise ValueError("retained page digest mismatch")
        for b in json.loads(response["Original"])["bars"].get("SPY", []):
            date = b["t"][:10]
            if args.start <= date <= args.end:
                if date in bars:
                    raise ValueError("overlapping retained SPY date")
                at = response["ReceivedAt"]
                bars[date] = dict(asset_id=args.asset_id, symbol="SPY", date=date,
                                  open=str(b["o"]), high=str(b["h"]), low=str(b["l"]),
                                  close=str(b["c"]), vwap=str(b["vw"]), volume=b["v"],
                                  trade_count=b["n"], published_at=at, observed_at=at,
                                  available_at=at, revision=1)
    actions = load(args.dividends)
    if actions.get("next_url") or actions.get("status") != "OK":
        raise ValueError("dividend response is incomplete")
    now = dt.datetime.now(dt.timezone.utc).isoformat()
    dividends = []
    for a in actions["results"]:
        if a["ticker"] != "SPY" or a["currency"] != "USD" or a["dividend_type"] != "CD":
            raise ValueError("unexpected dividend")
        if args.start <= a["ex_dividend_date"] <= args.end:
            dividends.append(dict(source_id=a["id"], kind="cash_dividend",
                                  ex_date=a["ex_dividend_date"], pay_date=a["pay_date"],
                                  amount=str(a["cash_amount"]), known_at=now))
    yahoo = load(args.reference)["chart"]["result"][0]
    refs = []
    raw_reference = {}
    for timestamp, close in zip(yahoo["timestamp"], yahoo["indicators"]["adjclose"][0]["adjclose"], strict=True):
        date = dt.datetime.fromtimestamp(timestamp, dt.timezone.utc).date().isoformat()
        if args.start <= date <= args.end:
            if close is None:
                raise ValueError("missing Yahoo adjusted close")
            refs.append(dict(date=date, close=f"{close:.12f}"))
    for timestamp, close in zip(yahoo["timestamp"], yahoo["indicators"]["quote"][0]["close"], strict=True):
        date=dt.datetime.fromtimestamp(timestamp, dt.timezone.utc).date().isoformat()
        if args.start<=date<=args.end:
            if close is None or close<=0: raise ValueError("missing raw reference close")
            raw_reference[date]=Decimal(str(close))
    # Independently compute cash entitlement on ex-date and reinvestment on the
    # first available pay-date close. SPY has no splits in this interval.
    shares=Decimal(1)
    pending=[]
    remaining=sorted(dividends,key=lambda a:a["ex_date"])
    for date,close in sorted(raw_reference.items()):
        for a in list(remaining):
            if a["ex_date"]<=date:
                if a["ex_date"]>min(raw_reference):
                    pending.append((a["pay_date"],shares*Decimal(a["amount"])))
                remaining.remove(a)
        for pay,cash in list(pending):
            if pay<=date:
                shares+=cash/close
                pending.remove((pay,cash))
    pay_return=shares*raw_reference[max(raw_reference)]/raw_reference[min(raw_reference)]
    calendar = [d["date"] for d in load(args.calendar) if args.start <= d["date"] <= args.end]
    if not bars or not refs or not dividends or not calendar:
        raise ValueError("comparison requires all four source sets")
    args.output.write_text(json.dumps(dict(asset_id=args.asset_id, as_of=now,
        raw=[bars[d] for d in sorted(bars)], actions=dividends, calendar=calendar, reference=refs, reference_pay_date_return_multiple=f"{pay_return:.12f}")))
    manifest = dict(start=args.start, end=args.end, acquired_normalization_at=now,
                    input_sha256=hashlib.sha256(args.output.read_bytes()).hexdigest(), sources=sources)
    args.output.with_suffix(".manifest.json").write_text(json.dumps(manifest, indent=2)+"\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--journal", type=Path, required=True)
    parser.add_argument("--dividends", type=Path, required=True, help="complete Polygon /v3/reference/dividends SPY response")
    parser.add_argument("--reference", type=Path, required=True, help="Yahoo /v8/finance/chart/SPY response")
    parser.add_argument("--calendar", type=Path, required=True, help="Alpaca /v2/calendar response")
    parser.add_argument("--asset-id", required=True)
    parser.add_argument("--start", default="2016-01-01")
    parser.add_argument("--end", required=True)
    parser.add_argument("--output", type=Path, required=True)
    prepare(parser.parse_args())
