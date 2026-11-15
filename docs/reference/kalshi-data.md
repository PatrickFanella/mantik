# Kalshi public weather-market data

`mantik kalshi-collect` reads one page of open markets for an explicit weather
series, its settlement sources and each returned market's current order book.
It sends no credentials and exposes no order endpoint. Supported public paths
are `/series/{ticker}`, `/markets?status=open&series_ticker=...` and
`/markets/{ticker}/orderbook` on the fixed public origin.

The command requires `--series-ticker`, `--journal` (an absolute private
mode-0700 directory) and an isolated Mantik `--database-url`. It returns a
cursor when more market pages exist; supply it with `--cursor` for the next
page. Run the command periodically to collect new book observations. The
command is finite and opt-in; no automatic collection worker is enabled.

The client fsyncs bounded original HTTP responses, including malformed, error
and partial bodies, to the private journal before parsing. Captures retain
path, HTTP status, observation time, partial flag, raw bytes and SHA-256.
Capture failure stops collection. Responses and requests are bounded by 1 MiB
and a ten-second timeout; there are no hidden request retries.

Markets retain their series and event identity, close and expiration times,
status, title, resolution rules and settlement-source name/URL. The complete
original series and market responses retain all declared sources. The public
API returns `event_ticker` on markets; `series_ticker` is the explicit collection
scope. Current books use `orderbook_fp` decimal price/quantity strings. Those
bytes stay exact; no float conversion, ask inference or fill simulation occurs.

Schema 118 stores append-only market revisions and book observations with
identity `(ticker, observed_at)` or `(market_ticker, observed_at)`. Identical
identity/content retries are no-ops, and changed content at an existing
identity conflicts. Separate observations of an unchanged book keep their
separate timestamps. Hashes are checked against the originals. Each storage
batch is atomic; a command retains its successful market page and earlier
book writes if a later book request fails. PostgreSQL writers serialize
before checking identities. Updates, deletes and truncation are refused.

`KalshiMarketsAsOf` returns each market's latest visible revision;
`KalshiBooksAsOf` returns its ordered visible snapshots. Historical queries
cannot see later observations, including when records arrive out of order.
These are observation times, not a claim that the venue published the data at
that instant. No historical book coverage before collection is invented.

The two new tables must travel together in backups. Migration is additive;
an older binary refuses schema 118. Rollback needs a compatible binary or an
isolated restored database, rather than deleting collected observations.

Direct public reads on 2026-11-15 confirmed weather series `KXHIGHCHI` and
`KXHIGHNY`, their declared settlement sources, market fields and the
fixed-point book response. A connected CLI collection stored 12 Chicago
weather markets and 12 book observations in a new disposable PostgreSQL
container, then read back both counts. The disposable storage contract
(including concurrent retries and immutable-table guards) passed with race
detection. The container was removed afterwards; public raw captures and the
receipt remain outside Git at `/tmp/mantik-wave1-kalshi-evidence/`.
Full collection and storage contracts are checked
with loopback fakes and disposable PostgreSQL.
[Weather probability and calibration](kalshi-weather.md) is a separate opt-in
research path. Trading authority and a separate notional fill simulator remain
later work.

Provider contracts: [public market-data quick start](https://docs.kalshi.com/getting_started/quick_start_market_data)
and [market order book](https://docs.kalshi.com/api-reference/market/get-market-orderbook).
