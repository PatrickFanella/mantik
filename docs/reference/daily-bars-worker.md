# Automatic daily bars

`serve` can collect a reviewed stock universe without a model or order access.
All four flags are required to enable it:

```sh
./bin/mantik serve --listen 127.0.0.1:40880 \
  --daily-bars-universe /tmp/mantik-wave4-evidence/universe-receipt.json \
  --daily-bars-credentials-file /tmp/mantik-wave4-evidence/credentials.json \
  --daily-bars-journal /tmp/mantik-wave5/daily-journal \
  --daily-bars-start 2026-11-12
```

These paths identify the private connected check on Kvant. Use a dedicated
journal and a `stock-universe` JSON report from the target installation when
configuring another instance. `DATABASE_URL` selects that instance's database.
Credentials follow the existing mode-0600 key/secret or OAuth JSON contract.
The journal's process lock prevents two collectors from using the same owner.

The worker freezes the report's asset IDs and symbols at startup (up to 100,000 distinct assets; the CLI file-size limit also applies).
It does not rerun the liquidity screen against stale data and silently lose a
member. Review a new `stock-universe` report and restart to change membership.
The universe's existing survivorship and ticker-history limits still apply.
Choose the start date within each member's reviewed listing/symbol interval;
a session with no bar, including a suspension, stays missing until explained
outside this collector. An empty response never certifies a trading day.

The first cycle runs immediately; subsequent cycles run once per minute after
the previous cycle finishes. Each cycle has a ten-minute timeout. The exchange
calendar sets the close in America/New_York, including DST, holidays and early
closes. A session becomes due at close plus 15 minutes. No recent SIP request
is sent: its end is fixed at an eligible close. Completed calendar ranges are
cached within the process; the latest date is refreshed each cycle. A restart
rebuilds calendar state and derives progress from durable bars.

Every missing session from the configured start is checked, including holes
before a newer stored bar. A bar observed before its close-plus-15-minute
boundary cannot satisfy coverage. Retained response pages keep their actual
acquisition times; catch-up never backdates availability. Fresh reads retry
empty/provisional responses rather than freezing them as historical checkpoints.
The most recent two eligible sessions are refreshed once per calendar day or
newly due session, and on restart, to append provider corrections. Unchanged
bars retain their revision. Original successful calendar and bar responses are
kept in the private journal; monitor its disk use under the existing private
journal policy.

`mantik readiness --scope daily_bars` and `/v1/readiness/daily_bars` expose
coverage and cycle freshness. `/readyz?scope=daily_bars` returns 503 unless the
capability is ready. Missing bars, failed reads and heartbeats older than three
minutes report `degraded` / `daily_bars_stale`. Readiness expires when a new
session becomes due even before another worker cycle. An unconfigured worker
reports `not_configured`. Database failure reports `unavailable`. This scope
is independent of fixture readiness, paper execution and data qualification.

The unit tests exercise exact embargo boundaries, early closes, DST, weekends,
empty responses, restart refresh, older holes and failed calendar reads.
The connected disposable-database check retrieves real bars and repairs an
older hole before a newer stored session, then checks restart idempotency. It does not establish an unattended natural evening
run or validate the financial accuracy of the feed. SPY accuracy remains the
separate [independent comparison](spy-comparison.md).
