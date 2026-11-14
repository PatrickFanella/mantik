# Adjusted daily bars

`internal/marketdata` reads an asset's stored [daily bars](daily-bars.md) as of
a clock and adjusts them for splits and cash dividends. Adjustment happens only
on read. Stored bars stay raw, and a read never writes.

## Reading

`marketdata.Read(ctx, store, assetID, actions, asOf)` reads the bars available
at `asOf` through `DailyBarsAsOf` and passes them to `Adjust`. `Adjust` does the
same for bars the caller already holds. The caller supplies the asset's
corporate actions. Tickers are reused, so matching actions to an asset is the
caller's job, like resolving a ticker before reading bars.

An action is used only when both hold:

- its `KnownAt` is at or before `asOf`;
- its ex-date is on or before `asOf`'s date in New York.

A split announced ahead of its ex-date changes nothing until that date. An
action that became known later than its ex-date is left out of reads before
it became known, so a read as of a past clock returns what Mantik could have
computed then.

When one source ID has several versions, the read first picks the latest
version known at `asOf`, then checks that version's ex-date. A known correction
that postpones an action therefore withdraws its earlier date. Two different
versions known at the same time fail the read.

## Actions

| Field | Meaning |
| --- | --- |
| `SourceID` | The provider's action ID. |
| `Kind` | `split` or `cash_dividend`. Any other kind fails with `ErrUnsupportedAction`. |
| `ExDate` | First session without the entitlement, `YYYY-MM-DD`. |
| `From`, `To` | A split turns `From` shares into `To` shares. A 4-for-1 split is 1 → 4; a 1-for-10 reverse split is 10 → 1. |
| `Amount` | Cash per share held before the ex-date. |
| `PayDate` | When the dividend is paid. Required, and not before the ex-date. |
| `KnownAt` | The earliest clock at which a reader may use the action. |

Amounts and ratios are plain positive decimals. Exponents and floats are refused.

## Adjusted prices

Each bar's prices are scaled by every used action whose ex-date is after it,
which puts every bar in the share basis of the read clock:

- A split scales open, high, low, close and VWAP by `From/To` and volume by
  `To/From`.
- A dividend scales the prices by `1 - Amount/C`, where `C` is the raw close
  of the last bar before the ex-date, scaled by any split that went ex between
  that bar and the dividend. A dividend at or above `C` fails with
  `ErrInvalidAction`.

Volume carries only split adjustment. Values are computed exactly with
rational arithmetic and rounded to 12 decimal places, half away from zero, when
they are rendered. Each adjusted bar keeps its raw bar.

## Total return

`TotalReturn` on each bar is the value at that bar's close of one share bought
at the first bar's close, as a multiple of the cost:

- Actions apply in ex-date order, including several that fall between two
  stored bars.
- On an ex-date, the shares held before it earn the dividend. A split on the
  same ex-date does not change that amount, because it is per share held
  before the ex-date.
- The cash arrives on the pay date, or the first bar after it, and buys more
  shares at that bar's close.
- Between the ex-date and the pay date the series dips, because the price has
  dropped and the cash has not arrived.
- A dividend whose pay date is after the last bar is owed but not counted.

An action with an ex-date on or before the first bar changes nothing: the
share was not held before it.

## Alpaca corporate actions

`marketdata.AlpacaActions(snapshots, symbol)` turns retained
[Alpaca corporate-action snapshots](alpaca-corporate-action-snapshots.md),
from `Journal.CorporateActionSnapshots(asOf)`, into actions for one ticker.
Each action is known from its snapshot's `AvailableAt`.

- `cash_dividends` become dividends from `rate`, `ex_date` and `payable_date`.
- `forward_splits` and `reverse_splits` become splits from `old_rate` →
  `new_rate`.
- `name_changes` are skipped because they do not change prices.
- Any other kind that names the ticker in a symbol field, such as a spin-off,
  merger or stock dividend, fails with `ErrUnsupportedAction`. Prices are never
  left silently unadjusted.

A retained snapshot only shows what one page said when it was fetched. It does
not prove Alpaca's coverage is complete, so a read built from snapshots is
only as complete as the snapshots it was given.

## Limits

- Splits and cash dividends only. Spin-offs, mergers, stock dividends and
  cash in lieu are not modelled.
- Polygon references and issuer documents are not converted yet.
- There is no stored mapping from tickers to assets; the universe (#89) adds it.
- A dividend cancelled by the provider is not recognised. Only a corrected
  version of the same source ID replaces an earlier one.
