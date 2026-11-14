# Alpaca price and calendar adapter

Alpaca is the selected first market-data provider. This slice normalizes retained
recordings and supplies a bounded GET-only client for explicit collection. `alpaca-normalize` remains an offline command. Separate journal commands
now perform explicit bounded collection, finite sessions, and fenced submission;
none starts with the service. Calendar inputs do not install session timers.
Existing pilot services keep their pinned revisions.

## Supported provider contracts

- Latest US equity quotes: `GET https://data.alpaca.markets/v2/stocks/quotes/latest`,
  explicit `symbols`, `feed`, and `currency=USD`. Supported feeds are `iex`,
  `sip`, and `delayed_sip`. Selection never falls back silently. IEX covers one
  exchange; SIP provides consolidated coverage subject to entitlement.
  [Quote contract](https://docs.alpaca.markets/us/reference/stocklatestquotes-1),
  [coverage FAQ](https://docs.alpaca.markets/us/docs/market-data-faq).
- Regular-session calendar: `GET https://paper-api.alpaca.markets/v2/calendar`,
  inclusive `start`/`end` with `date_type=TRADING`. Dates and regular open/close
  times account for early closes.
  [Calendar contract](https://docs.alpaca.markets/us/v1.1/reference/getcalendar-1).

Provider response bytes remain in the recording's `original` string with a
verified SHA-256. Unknown provider fields remain raw evidence; normalization
extracts the supported contract. Recording-envelope fields are strict.
Corporate-action [REST snapshots](alpaca-corporate-action-snapshots.md) can also
be retained through the same client and journal. Their source projection grants
no economic handling: corporate actions, expiry, settlement, and execution remain
explicitly unsupported in output. Any settlement or extended-session fields in a response remain raw
evidence and do not establish those capabilities.

## Recording envelope and offline command

`mantik-alpaca-recording-v1` requires a capture ID (1–40 identifier characters),
capability, feed, USD currency, 1–10 unique symbols, revision (1–1,000,000),
receipt time, evidence class, access reference, raw reference, original response,
raw SHA-256, and maximum quote age (1–86,400 seconds). Calendar recordings also
require an inclusive range of at most 31 dates and `feed: regular_session`.
Raw responses are limited to 1 MiB; envelopes to 2 MiB. Metadata is explicit
input, not independently verified account or access proof.

Create this manufactured contract fixture outside Git:

```bash
mkdir -p data
python3 - <<'PY'
import hashlib, json, pathlib
raw = '{"quotes":{"SPY":{"t":"2026-10-14T15:00:00Z","ap":100.01,"bp":100.00,"as":10,"bs":12}}}'
record = {
    "schema": "mantik-alpaca-recording-v1", "capture_id": "manufactured-quote-v1",
    "capability": "latest_quotes", "feed": "iex", "currency": "USD",
    "symbols": ["SPY"], "revision": 1,
    "received_at": "2026-10-14T15:00:01Z",
    "evidence_class": "synthetic_unqualified",
    "access_ref": "manufactured-contract-fixture-v1",
    "raw_ref": "artifact:manufactured-quote-v1", "original": raw,
    "sha256": hashlib.sha256(raw.encode()).hexdigest(), "max_quote_age_seconds": 60
}
path = pathlib.Path("data/alpaca-quote.json")
with path.open("x") as stream:
    json.dump(record, stream)
path.chmod(0o600)
print(record["sha256"])
PY
./bin/mantik alpaca-normalize --recording data/alpaca-quote.json --sha256 RAW_SHA256
```

Use the printed digest in place of `RAW_SHA256`. Output contains normalized
inputs, coverage, session schedules, and unsupported capabilities. Manufactured
fixtures remain `synthetic_unqualified`. Real recordings use the corresponding
unqualified market class under their collection plan.

Inspect an input before submitting through authenticated `route-input`, which
expects one input object rather than the entire normalization result. Research
routing requires a compatible development service and an explicit trigger
matching source, symbol and evidence class. This example does not upgrade or
mutate the preserved pilot stacks.

## Quote normalization and coverage

Price normalization uses **whole-cent ask price**, version `whole_cent_ask_v1`,
as a research threshold measure. It does not establish a trade, executable fill,
midpoint, or complete market coverage. Bid and ask must be positive, exactly
representable in bounded integer cents, with positive integral sizes and an
uncrossed spread. Unsupported precision is rejected rather than rounded.

Freshness compares the declared receipt time and maximum quote age to the
provider timestamp. Missing symbols, invalid/future quotes, unsupported precision,
empty liquidity, crossed quotes and stale quotes emit missing/degraded coverage
and an unusable provenance-bearing state input with no price. These cannot
match conditions or suppress missing-event obligations. Their occurrence time
is the receipt-time observation of a coverage problem. Ready quotes retain the
provider quote timestamp.

Sources include the feed: `alpaca-iex`, `alpaca-sip`, or `alpaca-delayed-sip`.
Delayed feeds do not automatically receive a more permissive freshness limit.
One feed cannot satisfy another feed's trigger.

## Calendar normalization and revisions

Session times use `America/New_York` and convert to UTC, with embedded timezone
data for containers. Duplicate dates, out-of-range dates, invalid times, and
close-before-open schedules fail the capture. Empty arrays fabricate no sessions.

Each session emits `market_session_schedule` evidence at receipt time. Future
open/close timestamps appear in a separate session record linked to its input.
These are schedules, not observed opens or closes. No durable timer is installed
or revised automatically.

Record keys bind capture, symbol, and quote/session identity. Re-normalizing a
retained capture converges. Changed raw bytes or metadata at the same capture
and revision conflict when submitted. A new revision gets a separate identity;
it cannot undo a matched case or replace a timer. Repeated retrieval is a new
capture with its own receipt, not a rewrite of the original. Calendar revision
review and timer supersession remain subsequent work.

## Read client and remaining collection work

`alpaca.NewClient` requires explicit credentials and feed but makes no request
at construction. It uses fixed Market Data and paper-calendar origins, only GET,
no arbitrary path, a five-second HTTP timeout, no redirects, and at most 1 MiB
per response. Unsupported capabilities fail before a request.

One client admits one in-flight operation; concurrent calls receive backpressure.
Attempts start at least one second apart, with at most three attempts. HTTP 429
and 5xx retry within bounds. `Retry-After` is honored up to 30 seconds; invalid
or excessive delays fail. Authentication, forbidden, and redirect responses do
not retry. Cancellation interrupts pacing and retry waits. Errors omit provider
bodies and credentials. These limits apply per client; a collector must own one
client and respect account-wide entitlements and limits too.

A 429 without `Retry-After` waits until the `X-RateLimit-Reset` time that
Market Data reports, up to 61 seconds.

Local HTTP tests use manufactured credentials and responses. They establish
contracts, not real access or coverage. Those tests made no real provider requests.

## Historical daily bars

`Client.FetchDailyBars(ctx, symbols, start, end)` reads raw daily bars from
`/v2/stocks/bars` for 1–100 symbols. It always asks for the SIP feed, whatever
feed the client uses for quotes, because the free plan serves SIP history once
a request ends at least 15 minutes ago. Other fixed parameters are
`timeframe=1Day`, `adjustment=raw`, `sort=asc` and `limit=4000`, a limit shared
across the request's symbols.

- **Embargo.** An end later than 15 minutes ago is clamped to that point, and
  the result reports `Clamped`. A range that lies entirely inside the embargo
  fails without a request. A `daily_bars` query sent directly through `Fetch`
  with a recent end is refused before it is sent.
- **Paging.** Page tokens are followed to the last page, at most 1,000 pages.
  A repeated or invalid token fails the call.
- **Pacing.** Bar requests, retries included, start at least 320 ms apart:
  187.5 per minute, under the plan's 200. Other capabilities keep the
  one-second spacing.
- **Raw bytes.** `Pages` keeps every successful response's original bytes and
  SHA-256 in request order, for the archive.
- **Bars.** Each symbol's bars come back in strictly increasing time order.
  Prices and VWAP stay exact decimal text, because many are sub-cent. Volume
  and trade count are integers. A page must carry both the `bars` object and
  `next_page_token`; an empty range is `{"bars":{},"next_page_token":null}`,
  so `null`, `{}` or another object fails. A page fails the whole call if it names a
  symbol that was not requested, repeats or reorders a bar time, falls outside
  the range, or carries a zero, negative or exponent-form price.

Bars are keyed by symbol only. Alpaca reuses tickers, so storage must add asset
identity before joining bars across companies (#62, #89).

A real check on 2026-11-14 used the paper account's read-only token, with GET
requests to Market Data only. Six symbols from 2016-01-01 took four pages; the
largest was 451 KB. SPY, QQQ, IWM, AAPL and MSFT each returned 2,733 bars from
2016-01-04 to 2026-11-13. TWTR returned 1,718 bars ending at its 2022-10-27
delisting. The end was clamped, and every sub-cent price parsed exactly.

[The private capture journal](alpaca-journal.md) now retains validated recordings
and supports explicit collection, finite sessions, fenced submission, and isolated
archive/restore checks. The [first real-feed rehearsal](../runbooks/first-real-feed.md)
verified paper-account and IEX read access, real calendar routing, and unusable
quote suppression. Credentials and raw evidence remain private. Continuous
acquisition still needs an approved cadence and qualified retention policy.
The service retains at most 4,096 manual inputs; collection must stop or retain pending work externally at
capacity. Continuous ingestion also needs retention and pagination policy.
Real-feed readiness requires an explicit [startup policy](feed-readiness.md)
and validated server evidence. Profitable paper operation remains a
later qualification gate.

## Recorded quote share units

The explicit [REST quote share-size contract](alpaca-quote-share-units.md) binds
current owner-derived dimensions to the reviewed provider schema and a
conservative post-transition occurrence date. The legacy raw inspector is
unchanged. Interpreting recorded size fields does not authenticate a capture,
qualify an evidence class or grant execution authority.
