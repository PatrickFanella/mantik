# Indicative option data

`options-backfill` stores Alpaca contract listings, daily bars and trades for an
explicit list of liquid underlyings. The command makes GET requests only and
runs independently of strategy or execution workers. Schema 120 adds the
append-only `mantik_option_data` store; both Memory and PostgreSQL implement
`RecordOptionData` and `OptionDataAsOf`.

Required flags are `--database-url` (explicitly selected isolated database),
`--journal` (dedicated absolute private directory), `--credentials-file`
(current-user mode-0600 key/secret or OAuth JSON), `--underlyings` (comma-separated,
at most ten), `--end` (fixed exclusive UTC date) and `--expiry-end` (inclusive
expiration date). `--start` and `--expiry-start` default to `2024-02-01`.
`--min-strike` and `--max-strike` optionally bound a collection in exact dollars.
Selecting an isolated database is the operator's responsibility. The CLI ignores
ambient `DATABASE_URL` and applies migrations to the explicitly supplied target.

For example, the real check uses underlying `SPY`, start `2024-02-01`, end
`2024-02-03`, expiration bounds both `2024-02-02` and strike bounds both `490`.
That is a bounded two-day historical sample, not a complete SPY-chain backfill.
Omitting strike bounds collects the configured expiration range without a
strike filter. Large full-history jobs can require many requests and substantial
private storage; the command is resumable and has no hidden background worker.

## Contracts and observations

Both active and inactive listings are requested with explicit expiration bounds,
so Alpaca's default upcoming-expiration filter cannot silently omit old contracts.
Each retained contract carries its source ID, symbol, underlying, expiry, strike
and call/put type in the original object. Standard symbols are collected;
nonstandard/adjusted symbols such as `1SPY…` remain as reference observations but
are counted as skipped for market data, because the bars endpoint rejects them.

Daily bars use `timeframe=1Day` and `sort=asc`; trades use the same time bounds
and ascending order. Internal `OptionSpec` ranges preserve nanosecond precision:
the inclusive provider end is the exclusive end minus one nanosecond, and both
request boundaries use RFC3339Nano. The CLI still takes UTC dates. Historical
checkpoints bind those exact request boundaries, so a less precise old request
is not reused as evidence for an expanded range.

The historical endpoints reject a `feed` query parameter
(confirmed HTTP 400 on the real API), unlike snapshots. They use the provider
default. Each market record carries conservative `feed: indicative` classification
and `feed_selection: provider_default_unverified`, never an assertion that the
server confirmed which feed supplied it.
Data pages are limited to 1,000 points, listing pages to 100 contracts, each
response to 1 MiB and each paginated stream to 1,000 pages. The collector checks
page envelopes, requested symbols, contract metadata, decimal prices, integral
volume and trade sizes, order and time bounds. Invalid or repeated pagination
fails the run rather than silently accepting a partial history.

The shared client starts requests, including retries, at least 320 ms apart:
187.5/minute under the 200/minute free allowance. It keeps bounded retry and 429
reset handling, rejects redirects and interrupts waits on cancellation. One
collector owns one process-locked journal; account-wide coordination with other
processes is not supplied.

Every successful page is durably retained with its query, original bytes,
digest and receipt before parsing or storage. On restart, completed pages,
contracts and symbols replay without API fetches. Exact replay inserts nothing,
and the same journal can populate another database. An interrupted page may be
retried, but successful pages are not discarded. Use a new journal to observe
later provider changes; historical checkpoints are immutable.

`OptionRecord` keeps kind, contract symbol, underlying, source key, occurrence,
observation and availability clocks, feed and the complete source object.
Contracts are `reference`; bars and trades are always `indicative`. A source
trade key uses timestamp and ID when present. Real indicative history omits IDs;
then the key uses timestamp, a canonical object digest, and the identical-event
occurrence number across the retained stream. This preserves multiplicity and
replay identity within a job; different captures with changed stream contents
are not asserted to be globally deduplicated trade events. Daily bars use
session date. The store appends different receipt clocks, deduplicates exact replay,
rejects changed content at an existing identity, and refuses UPDATE, DELETE and
TRUNCATE. A read at a clock returns the latest available version per source key.

## Evidence limits

[Alpaca documents](https://docs.alpaca.markets/us/docs/historical-option-data)
history since February 2024. Its free indicative feed derives prices and delayed
trades from OPRA; these are **not actual OPRA quotes** or evidence of executable
liquidity. Historical bars/trades are not quote observations, and the historical endpoint
does not expose feed selection. The collector records that uncertainty and
grants no OPRA entitlement or verified OPRA provenance. It makes no alternative
request for a paid feed.

Availability is the actual response receipt, including for historical bars and
trades. Occurrence time does not establish publication time; the collector does
not manufacture earlier availability. Contract observations are current
reference snapshots, not proof that the full chain was visible historically.
These data grant no backtest, option-chain, Greek, execution, assignment,
settlement or qualification capability. Those are separate roadmap units.

Provider contracts: [listings](https://docs.alpaca.markets/us/reference/get-options-contracts),
[daily bars](https://docs.alpaca.markets/us/reference/optionbars),
[trades](https://docs.alpaca.markets/us/reference/optiontrades).

## Connected check on Kvant, 2026-11-15

A new disposable PostgreSQL container passed the option-store contract test,
including concurrent replay, atomic conflict refusal, point-in-time revisions,
migration replay and immutable-table guards. The real API sample described
above stored two SPY contracts, two daily bars and 55,028 trades. A second run
inserted zero records and reported all 55,032 observations unchanged. Direct SQL
counts matched: 2 reference contracts, 2 indicative bars, 55,028 indicative
trades. The container was removed; credentials, pages and receipts remain
private outside Git under `/tmp/mantik-wave4-evidence/`.

The live check also established that the historical endpoints reject a `feed`
parameter and indicative trades omit IDs. The implementation and regression
fixtures follow those observed contracts. This sample does not establish
complete SPY option-history coverage, historical chain availability, verified
feed provenance or executable prices.

A later boundary check on the same date requested SPY call trades in
`[2024-02-01T14:30:00.1Z, 2024-02-01T14:30:00.5Z)`, using provider inclusive
end `14:30:00.499999999Z`. The real API accepted the nanosecond boundary and
returned three trades, all within the requested interval, with no further page.
This supplements the earlier larger sample; its private receipt is
`/tmp/mantik-wave4-evidence/option-boundary-probe.json`.
