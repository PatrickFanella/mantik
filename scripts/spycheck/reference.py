"""Independent Decimal reference from a public LEAN sample then Yahoo closes."""
import csv
import datetime as dt
from decimal import Decimal
import io
import zipfile


def lean_closes(raw):
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        if archive.namelist() != ["spy.csv"] or archive.getinfo("spy.csv").file_size > 10 << 20:
            raise ValueError("expected bounded LEAN SPY daily sample")
        rows = csv.reader(archive.read("spy.csv").decode().splitlines())
        result = {}
        prior = ""
        for row in rows:
            if len(row) != 6 or not row[4].isdigit():
                raise ValueError("invalid LEAN daily row")
            date = dt.datetime.strptime(row[0], "%Y%m%d %H:%M").date().isoformat()
            if date <= prior or int(row[4]) <= 0:
                raise ValueError("invalid LEAN close/date order")
            result[date] = Decimal(row[4])/10000
            prior = date
    if not result:
        raise ValueError("empty LEAN sample")
    return result


def pay_date_return(prices, dividends):
    shares = Decimal(1)
    pending = []
    remaining = sorted(dividends, key=lambda a: a["ex_date"])
    first = min(prices)
    for date, close in sorted(prices.items()):
        for action in list(remaining):
            if action["ex_date"] <= date:
                if action["ex_date"] > first:
                    pending.append((action["pay_date"], shares*Decimal(action["amount"])))
                remaining.remove(action)
        for pay, cash in list(pending):
            if pay <= date:
                shares += cash/close
                pending.remove((pay, cash))
    return shares*prices[max(prices)]/prices[first]


def stitched_reference(sample, yahoo, calendar, dividends):
    # Precedence is fixed by the public sample's coverage, never price distance.
    cutoff = max(sample)
    prices = {}
    for date in calendar:
        source = sample if date <= cutoff else yahoo
        if date not in source:
            raise ValueError("independent reference missing date "+date)
        prices[date] = source[date]
    factors = []
    for action in dividends:
        prior = [d for d in prices if d < action["ex_date"]]
        if not prior:
            continue
        factor = 1-Decimal(action["amount"])/prices[max(prior)]
        if factor <= 0:
            raise ValueError("invalid reference dividend factor")
        factors.append((action["ex_date"], factor))
    adjusted = []
    for date, close in prices.items():
        for ex, factor in factors:
            if date < ex:
                close *= factor
        adjusted.append(dict(date=date, close=f"{close:.12f}"))
    return adjusted, f"{pay_date_return(prices, dividends):.12f}", cutoff


def nasdaq_gap(raw):
    if raw["status"]["rCode"] != 200 or raw["data"]["symbol"] != "SPY":
        raise ValueError("invalid Nasdaq SPY response")
    rows = raw["data"]["tradesTable"]["rows"]
    if len(rows) != raw["data"]["totalRecords"]:
        raise ValueError("incomplete Nasdaq history")
    matches = [r for r in rows if r["date"] == "08/12/2019"]
    if len(matches) != 1:
        raise ValueError("Nasdaq gap close missing or duplicate")
    price = Decimal(matches[0]["close"].replace(",", "").removeprefix("$"))
    if not price.is_finite() or price <= 0:
        raise ValueError("invalid Nasdaq close")
    return str(price)
