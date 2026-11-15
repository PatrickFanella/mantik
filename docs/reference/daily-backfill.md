# Resumable daily backfill

`bars-backfill` collects raw daily SIP bars from 2016 onward using the free
historical endpoint. It takes a JSON array of reviewed symbol/asset bindings,
`[{"symbol":"SPY","asset_id":"b28f4066-752a-432f-804f-32f42e9c2b7f"}]`,
and these required flags:

- `--assets`: path to that array. Resolve IDs from Alpaca's asset reference and
  review identity continuity across the requested interval. A ticker lookup
  today cannot establish that continuity for a reused ticker.
- `--database-url`: an explicitly selected isolated PostgreSQL database. Ambient
  `DATABASE_URL` is ignored. Opening the store applies Mantik migrations; selecting an isolated database is
  the operator's responsibility, not enforced by a target allowlist.
- `--credentials-file`: current-user mode-0600 JSON, either `key`/`secret` or
  `oauth_token`, never both. The client sends only GET requests.
- `--journal`: absolute private directory, created mode 0700 with private files
  and a process lock. Use a dedicated directory for the historical acquisition.
- `--end`: fixed exclusive UTC date, at least 15 minutes old. `--start` defaults
  to `2016-01-01`. Reuse the same dates when resuming.

The command sorts symbols and fetches calendar-year chunks, following all pages.
Every successful page is written atomically with its original bytes, digest and
receipt time before use. Restarting revalidates and reuses completed pages;
a failed page is requested again. Completed symbols need no network requests.
The current year uses the fixed end, so a later run cannot silently extend the
job. Changed ranges use different cache keys.

Progress prints cumulative bars, inserted, corrected and unchanged counts for
each symbol after each chunk. Replaying the same job inserts nothing. It also
replays into a fresh database: a journal checkpoint never claims that another
database already has its bars. A crash after database insertion is safe because
the append-only store deduplicates equal values.

The existing client paces every bar request, including retries, at 320 ms or
slower (187.5/minute), follows bounded pagination, respects rate-limit reset
headers and interrupts waits on cancellation. This is one foreground collector;
it does not coordinate the account's quota with other processes.

## Evidence limits

Alpaca supplies a session timestamp, not a publication timestamp for these
historical bars. The collector uses the last page's receipt time as the
conservative publication upper bound, observation and availability time for the
chunk. It does **not** backdate today's returned revisions to 2016. Historical
backtest eligibility needs separately reviewed publication assumptions or
archived observations; a successful backfill alone does not supply that proof.
A new dedicated journal is needed to observe provider corrections. Never edit
an existing checkpoint to refresh it. No production service is contacted by the
command unless its database is explicitly selected.

## Connected check on Kvant, 2026-11-15

A new disposable PostgreSQL container received SPY, QQQ and IWM from the real
API, each with 2,733 bars spanning 2016-01-04 through 2026-11-13. The second run
reported 2,733 unchanged for each, with zero insertions or corrections. All
stored revisions remained 1. The container was removed. Private response pages
and receipts are retained outside Git in `/tmp/mantik-wave4-evidence/`.
This proves collection and replay for these ETF bindings, not historical
publication timing, completeness for delisted equities, or strategy performance.
