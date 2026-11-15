# Point-in-time stock universe

`stock-assets` loads Alpaca's active **and inactive** `us_equity` lists. It uses
`--credentials-file` (private JSON, key/secret or OAuth) and `--journal` (absolute
private directory). Redirect its JSON output into a retained reference file
outside Git. The complete raw lists are retained with digests and receipt times
in <=1 MiB chunks; each unpaginated provider list is bounded at 16 MiB.

`stock-universe` takes `--reference` pointing to that JSON, an explicit
`--database-url`, and an RFC3339 `--as-of` clock. It emits the screened members,
asset IDs, supporting last session and price, thresholds, reference observation
time and coverage limits. It runs no strategy or order operation.

The defaults are configurable:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--min-price` | `5` | Latest raw closing price, dollars |
| `--min-dollar-volume` | `1000000` | Mean raw close times volume, dollars |
| `--sessions` | `20` | Required number of trailing daily sessions |
| `--max-age-days` | `7` | Maximum age of the latest session, UTC calendar dates |

Session age compares UTC dates, so a prior session stays one calendar day old
throughout the next UTC day. The membership clock's time of day does not make it
stale early.

Prices and liquidity comparisons use exact rational arithmetic. The screen
covers stocks and ETFs in the US equity class with a listed exchange. It
excludes OTC, missing venues and other asset classes without freezing a list of
US exchanges that could omit a new venue.

Each read uses the stable source **asset ID**, never a ticker join. Only bars
and revisions available strictly before the clock, with a prior UTC session
date, participate. Later corrections do not rewrite past memberships. The
member's ticker comes from its bar, so a later reference rename does not change
that ticker. Assets need the full trailing window and a sufficiently recent
session. A stale delisted series cannot remain a member indefinitely.

At or after a reference observation, assets must be active and tradable.
Current inactive and tradability flags do not remove a company
from memberships before that observation. They cannot establish historical listing dates or
broker eligibility. The reference itself may have been fetched after the clock:
this is an explicitly retrospective reference catalogue, with membership
computed from prior available **bars**, not a claim that the whole asset list was
known then. Every report sets `possibly_survivor_biased: true` and
`historical_tradability_verified: false`. Alpaca's incomplete inactive list omits
some delisted companies, and this implementation adds no free delisting source.
A reused ticker must have separate reviewed bar-to-asset bindings, as required by
[backfill](daily-backfill.md); no automatic ticker-based backfill join is made.

Bars collected today use today's availability time. They will not pass a
historical clock merely because their session date is old. This collector does
not manufacture publication history or qualify a backtest.

Provider contract: [Alpaca assets](https://docs.alpaca.markets/us/docs/assets).

On Kvant on 2026-11-15, the real loader retained 14,406 active and 19,150
inactive assets (33,556 total), including TXSE listings. Raw provider responses
and the normalized catalogue remain private outside Git. This count does not
establish complete delisted-company coverage.
