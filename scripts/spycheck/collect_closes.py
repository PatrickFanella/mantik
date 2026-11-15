#!/usr/bin/env python3
"""Retain SPY primary-exchange closing-auction trades using delayed free SIP.

GET only. Original responses and credentials stay outside Git. Queries cover
one second after each exchange-calendar close; missing/conflicting auction
prints are errors at normalization, never filled from the comparison source.
"""
import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from zoneinfo import ZoneInfo


def query_for(day):
    close = dt.datetime.fromisoformat(day["date"]+"T"+day["close"]).replace(
        tzinfo=ZoneInfo("America/New_York")).astimezone(dt.timezone.utc)
    if close + dt.timedelta(seconds=1, minutes=15) > dt.datetime.now(dt.timezone.utc):
        raise ValueError("SIP query end must be at least 15 minutes old")
    return dict(start=close.isoformat(), end=(close+dt.timedelta(seconds=1)).isoformat(),
                feed="sip", sort="asc", limit=10000)


def retained(path, query):
    if not path.exists():
        return None
    page = json.loads(path.read_bytes())
    if page["query"] != query or hashlib.sha256(page["original"].encode()).hexdigest() != page["sha256"]:
        raise ValueError("retained close query/digest mismatch")
    return json.loads(page["original"])


def collect(args):
    info = args.credentials.stat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError("credentials require a regular mode-0600 JSON file")
    creds = json.loads(args.credentials.read_bytes())
    if creds.get("oauth_token") and not creds.get("key") and not creds.get("secret"):
        headers = {"Authorization": "Bearer "+creds["oauth_token"]}
    elif creds.get("key") and creds.get("secret") and not creds.get("oauth_token"):
        headers = {"APCA-API-KEY-ID": creds["key"], "APCA-API-SECRET-KEY": creds["secret"]}
    else:
        raise ValueError("credentials require exactly one mode")
    if not args.output.is_absolute() or args.output.is_symlink():
        raise ValueError("output requires an absolute private directory")
    args.output.mkdir(mode=0o700, parents=True, exist_ok=True)
    if stat.S_IMODE(args.output.stat().st_mode) != 0o700:
        raise ValueError("output requires mode 0700")
    # Serialize resumptions without changing existing retained pages.
    with (args.output / ".collector.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        calendar = json.loads(args.calendar.read_bytes())
        days = [d for d in calendar if args.start <= d["date"] <= args.end]
        if not days or len(days) > 10000 or [d["date"] for d in days] != sorted(set(d["date"] for d in days)):
            raise ValueError("calendar requires ordered unique dates")
        for ordinal, day in enumerate(days, 1):
            query = query_for(day)
            seen = set()
            for number in range(10):
                path = args.output / f'{day["date"]}-{number}.json'
                result = retained(path, query)
                if result is None:
                    started = time.monotonic()
                    url = "https://data.alpaca.markets/v2/stocks/SPY/trades?"+urllib.parse.urlencode(query)
                    request = urllib.request.Request(url, headers=headers)
                    for attempt in range(3):
                        try:
                            # No redirect may forward credential headers elsewhere.
                            opener = urllib.request.build_opener(NoRedirect)
                            with opener.open(request, timeout=30) as response:
                                raw = response.read((4 << 20)+1)
                            break
                        except urllib.error.HTTPError as failure:
                            if failure.code != 429 and not 500 <= failure.code <= 599 or attempt == 2:
                                raise ValueError(f"Alpaca close read failed (HTTP {failure.code}) on {day['date']}") from None
                            delay = failure.headers.get("Retry-After", "1")
                            if not delay.isdigit() or int(delay) > 61:
                                raise ValueError("Alpaca retry delay exceeds bound") from None
                            time.sleep(max(1, int(delay)))
                        except (urllib.error.URLError, TimeoutError):
                            if attempt == 2:
                                raise ValueError("Alpaca close transport failed on "+day["date"]+"; resume retained pages") from None
                            time.sleep(1)
                    if len(raw) > 4 << 20:
                        raise ValueError("Alpaca close response exceeds 4 MiB")
                    result = json.loads(raw)
                    page = dict(query=query.copy(), received_at=dt.datetime.now(dt.timezone.utc).isoformat(),
                                original=raw.decode(), sha256=hashlib.sha256(raw).hexdigest())
                    with tempfile.NamedTemporaryFile(mode="w", dir=args.output, delete=False) as temp:
                        json.dump(page, temp)
                        temporary = Path(temp.name)
                    os.replace(temporary, path)
                    time.sleep(max(0, .35-(time.monotonic()-started)))
                if ("trades" not in result or
                        result["trades"] is not None and not isinstance(result["trades"], list) or
                        "next_page_token" not in result or result.get("symbol") != "SPY"):
                    raise ValueError("invalid SPY trades envelope for "+day["date"])
                token = result.get("next_page_token")
                if not token:
                    break
                if not isinstance(token, str) or token in seen or len(token) > 4096:
                    raise ValueError("invalid or repeated close page token")
                seen.add(token)
                query["page_token"] = token
            else:
                raise ValueError("close query exceeded ten pages")
            print(f'{ordinal}/{len(days)} retained SPY auction window {day["date"]}', flush=True)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--credentials", type=Path, required=True)
    parser.add_argument("--calendar", type=Path, required=True)
    parser.add_argument("--start", default="2016-01-01")
    parser.add_argument("--end", required=True)
    parser.add_argument("--output", type=Path, required=True)
    options = parser.parse_args()
    for text in (options.start, options.end):
        dt.date.fromisoformat(text)
    collect(options)
