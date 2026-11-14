# Daily engine

`internal/engine` runs one [strategy](strategy-interface.md) through trading
days. `engine.Run(ctx, config)` returns every session's book, every fill and
the decision left unfilled at the end. The same code is meant to drive paper
trading later.

## A run

| Field | Meaning |
| --- | --- |
| `Strategy` | The strategy to run. |
| `Source` | Adjusted bars as of a clock, such as [`marketdata.Read`](adjusted-bars.md) with each asset's corporate actions. |
| `Assets` | The assets the strategy trades. |
| `Start`, `End` | Sessions whose decision time falls in this range run. Earlier bars still count toward warm-up. |
| `InitialCash` | Starting cash, as a plain decimal. |
| `Cost` | The cost model. |
| `Deposits` | Optional deposit schedule. The book opens at `Start`. |

The engine reads each asset as of `End` to lay out sessions, raw prices and
corporate actions. A session is a bar date. Its decision time is fixed at
16:30 New York on that date, half an hour after the regular close and past the
SIP feed's 15-minute embargo, so a later correction to a bar never moves a
decision. A bar that becomes available after 16:30 is not in that session's
view. The strategy itself only ever sees a view built as of its decision time,
and every fill happens after the decision it carries out.

## Each session

1. **Corporate actions** that went ex since the previous session apply to the
   holdings in ex-date order, dividends before splits on a shared date. A
   dividend is earned on the shares held before its ex-date, including any
   earlier split between the two sessions.
2. **Dividends** whose pay date has arrived are added to cash.
3. **Deposits** scheduled at or before this session's open are added to cash.
4. **Fills.** The previous session's decision fills at this session's raw
   open. Each asset's target shares are its weight times the equity at the
   open, divided by the open. An asset without a bar this session keeps its
   order for the next one. No fill ever uses a price from the session that
   made the decision.
5. **Mark.** Equity is cash plus shares at the close, or at the last close for
   an asset without a bar. The daily return is time-weighted (see below).
6. **Decision.** Once every asset has at least `Warmup` bars (at least one),
   the strategy decides. A decision is the complete target set: held assets
   it leaves out are sold.

Fills happen at 09:30 New York on their session, which is the time the
strategy sees them in its book.

## Deposits

`DepositSchedule{Amount, EveryWeeks}` adds `Amount` of cash every
`EveryWeeks` weeks on Fridays at 06:00 America/Chicago, by wall-clock time, so
the UTC time moves with daylight saving. The first deposit is the first Friday
06:00 at or after the book opens. `DefaultDeposits` is $500 every other
Friday. `DepositSchedule.Due(opened, through)` lists the deposits in a range.

Each deposit's ID is its scheduled date, such as `deposit-2026-01-09`. The
engine applies each one exactly once, as settled cash before the open of the
first session at or after its scheduled time. A deposit that falls on a
holiday or in a gap between bars arrives before the next session's open. Its
`EffectiveAt` stays the scheduled time and `AppliedOn` names the session.
Replaying a run applies the same deposits on the same sessions. A deposit
scheduled after the last session's open is not applied.

## Returns

A session's return is time-weighted and split at the open, where deposits
arrive. The book is valued at the open before the deposits, using each held
asset's open, or its last close if it has no bar. The return from the previous
close to that value is chained with the return from that value plus the
deposits to the close. `TWR` chains the daily returns from the first session. A deposit
therefore never counts as profit: on a day when prices do not move, the
return is zero with or without one, and an overnight gap is not diluted by
the deposit. Dividends are income, not deposits, and do
count.

## Costs

`CostModel` has a `Version`, `FeeBps` and `SlippageBps`. A fill's price is the
open moved against the trade by `SlippageBps`; its fee is `FeeBps` of the
fill's notional. Every result records the cost model that produced it.
`DefaultCost` is `next-open-bps-v1`: no commission, as on Alpaca, and five
basis points of slippage each way.

## Results

The engine keeps cash, shares, split-adjusted prices and the chained return at
a fixed 18 decimal places, rounded half away from zero after every step. Exact
fractions would grow without bound over a long run. Results are rendered to
12 decimal places. Assets and dates are processed in sorted order. The same
inputs therefore give byte-identical JSON. An 11-year daily run of one asset
with rebalancing every session takes about 45 seconds.

## Limits

- One strategy per run, with no sleeves or account rules. Cash can go slightly
  negative by the fees of a full-weight buy. Cash and margin rules (#66) and
  the capital distributor (#73) come later.
- Fractional shares, with no minimum order size.
- Fills use the latest stored revision of each bar and the corporate actions
  known at `End`.
- Each decision rebuilds the view from the source, so run time grows with the
  square of the number of sessions.
