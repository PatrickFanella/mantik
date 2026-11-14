# Daily bar store

`storage.Memory` and `storage.Postgres` keep raw (unadjusted) daily bars with
the times needed to read them as of a past clock. PostgreSQL keeps them in
`mantik_daily_bars` (schema 116). Both pass the same contract tests in
`internal/storage/daily_bars_test.go`.

The store only keeps and serves bars. Fetching, backfill and the stock universe
are separate work (#88, #89). Split and dividend adjustment happens on
[read](adjusted-bars.md).

## A bar

| Field | Meaning |
| --- | --- |
| `AssetID` | The source's stable asset identity, for example Alpaca's asset ID. Part of the key. |
| `Symbol` | The ticker the bar was reported under. Not part of the key. |
| `Date` | Session date, `YYYY-MM-DD`. Part of the key. |
| `Open`, `High`, `Low`, `Close`, `VWAP` | Exact decimal text, up to 12 digits on each side of the point. |
| `Volume`, `TradeCount` | Non-negative integers. |
| `PublishedAt` | When the source published the bar. |
| `ObservedAt` | When Mantik fetched it. |
| `AvailableAt` | The earliest clock at which a reader may use it. Never before `PublishedAt`. |
| `Revision` | Set by the store: 1 for the first bar of a date, one more per correction. |

Bars are keyed by asset and date because tickers are reused: one ticker can
name different companies over time. A caller must resolve a ticker to an asset
before it writes or reads.

Prices are never floating point. Open, high, low and close must be above zero;
VWAP may be zero. Equal decimals compare equal whatever their spelling: `603`
and `603.000` are the same price, and the store keeps `603`. PostgreSQL holds
them as `numeric`. Times are kept in UTC at microsecond precision.

The caller chooses the three times. `AvailableAt` may be earlier than
`ObservedAt`: a bar backfilled today for a 2016 session can be marked available
from 2016, so a backtest can read it. Whoever writes that bar is asserting the
source had published the same values by then. The store cannot check this.

## Writing

`RecordDailyBars(ctx, bars)` takes 1 to 20,000 bars and stores all of them or
none. It returns how many were inserted, corrected and unchanged. For each bar
it looks at the latest stored revision for that asset and date:

- **None:** the bar is stored as revision 1.
- **Same values** (symbol, prices, volume, trade count): nothing changes. The
  times are not compared, so fetching the same bars again is a no-op.
- **Different values:** the bar is stored as the next revision. The earlier
  revisions stay.

A correction must have an `AvailableAt` later than the revision it replaces,
and an `ObservedAt` that is not earlier. Otherwise the write fails with
`ErrDailyBarOrder`, because it would change what earlier reads returned.
A malformed bar, a caller-set `Revision`, an empty write, or two bars for the
same asset and date in one write fails with `ErrInvalidDailyBar`.

In PostgreSQL a write holds one advisory lock per asset for its transaction, so
concurrent writers of the same bars store each once. Rows cannot be updated or
deleted, and a trigger refuses a revision that skips a number or is not later
than its predecessor.

## Reading as of a clock

`DailyBarsAsOf(ctx, assetID, asOf)` returns the asset's bars in date order. For
each date it returns the latest revision whose `AvailableAt` is at or before
`asOf`. A bar or correction that became available after `asOf` is never
returned, so a read as of a past clock returns the same bars it would have
returned then.

## Limits

- Prices are raw. [Adjusted reads](adjusted-bars.md) apply splits and
  dividends on read.
- There is no read by ticker and no date-range filter.
- Nothing retires or archives bars.
- The store does not check `AvailableAt` against the wall clock.
