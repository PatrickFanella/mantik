# Stock strategy library

`internal/strategy/library.New(Config)` creates a pure, immutable strategy
recipe for the existing daily engine. `Default(kind)` supplies the initial
paper-research parameters below. Strategies cannot place orders, acquire data,
read the clock or activate themselves. They remain unqualified candidates.

## Recipes and safety conditions

| Kind | Signal | Selection and sizing | Schedule |
| --- | --- | --- | --- |
| `trend` | Mean sign of 21-, 63- and 252-session excess returns. | Signed score divided by 63-session return volatility; normalize absolute weights to gross. Uses the configured ETF asset set. | Monthly |
| `momentum` | Mean 63-, 126- and 252-session return ending 21 sessions ago. | Top five eligible names; inverse 63-session return volatility. | Monthly |
| `reversal` | Negative mean 5- and 21-session returns. | Top five losers; equal target weights, limited to 0.25 L1 weight turnover at scheduled changes. | Weekly |
| `lowvol` | Negative 252-session population standard deviation of daily simple returns. | Lowest-volatility five eligible names; equal weights, long only. | Monthly |

All initial recipes use gross 0.7. CashRate defaults to an explicit zero
scenario; trend subtracts `(1+CashRate)^(sessions/252)-1` before taking signs.
These are trading-session approximations to the stated month horizons. The
return windows, skip, volatility window, selection count, cadence, gross,
cash-rate scenario and reversal turnover budget are part of the version hash.
Invalid parameters are refused. A successful construction copies its inputs.

The three cross-sectional recipes declare `stock_universe` and rank only its
eligible **configured** stable asset IDs. The source must provide a universe
screened at that decision clock with reference data already known then.
For the large-cap reversal recipe, the caller must supply its reviewed
large-cap subset; the liquidity screen alone is not evidence of historical
market capitalization. The library does not invent missing cap observations.
It also does not add assets outside the engine's configured data/holding set.

A name needs enough ordered adjusted bars, positive finite prices and usable
positive return volatility. Histories older than seven calendar days are unavailable. Candidates must
share the latest eligible bar date, so a delayed member cannot compete using
a different session. Unavailable signals are excluded. The engine
waits for the declared warm-up. Ranks break ties by stable asset ID. Normalized
weights truncate toward zero at 12 decimals so rounding cannot exceed gross.
Account admission, cash settlement, sleeve budgets and hard risk limits remain
outside strategy authority.

Between scheduled rebalances the strategy returns `KeepPositions`, preserving
shares rather than emitting weights that would cause daily rebalancing.
Membership exits force an immediate replacement decision. An actual ineligible
holding is retried even if its prior target had already reached zero. Reversal turnover
limits use the L1 change over available holdings, entrants and healthy rank
exits. On a scheduled rebalance, delayed/unavailable holdings are forced to zero
before applying the turnover limit to the remaining changes. A membership exit
takes precedence over the turnover limit. No missing asset remains held merely
to conserve turnover. The book state carries the prior recipe targets and
schedule period; it does not grant allocation or execution authority.

## Source provenance and adaptations

The executable source building blocks are:

- `pmorissette/bt`, MIT, commit
  [`763ae01047fce2d09411faeba436855ecf8b679c`](https://github.com/pmorissette/bt/tree/763ae01047fce2d09411faeba436855ecf8b679c):
  `StatTotalReturn`, `StatMultiPeriodReturn`, `SelectN`, `WeighInvVol`,
  `WeighEqually` in `bt/algos.py`.
- `pmorissette/ffn`, MIT, commit
  [`eb9354c5c82dbada5645569dc34e5861d156e471`](https://github.com/pmorissette/ffn/tree/eb9354c5c82dbada5645569dc34e5861d156e471):
  total-return and inverse-volatility primitives used by bt in `ffn/core.py`.

Code version `bt-763ae010-ffn-eb9354c5-recipes-v2` binds these sources to the
reviewed Mantik recipe adapter. License notices accompany the reference
fixtures. The [reference generator](../../internal/strategy/library/testdata/generate.py)
executes the pinned upstream algorithms, not the Go implementation.

The recipes compose and adapt those building blocks. Explicit adaptations are
multi-horizon signs and a cash-rate scenario for trend, skipped-session return
windows for rotation, a reversal turnover budget, point-in-time membership,
stable-ID tie breaking, gross 0.7, 12-place weight truncation and Mantik's
next-session execution. Integer session lookbacks are translated into exact
calendar offsets at each fixture date for bt's return operators. bt uses
sample deviation for inverse-volatility sizing; Mantik uses the tested
population-deviation indicator. For equal-length windows this differs by the
same factor for every asset, which cancels in normalized weights and rankings.

These are **adapted recipe ports**, not unchanged Lean algorithms or empirical
replications of the papers' original universes, funding, risk targets or fills.
The research basis is Moskowitz, Ooi & Pedersen (2012), *Time Series Momentum*;
Jegadeesh & Titman (1993), *Returns to Buying Winners and Selling Losers*;
Barroso & Santa-Clara (2015), *Momentum Has Its Moments*;
Jegadeesh (1990), *Evidence of Predictable Behavior of Security Returns*;
Frazzini & Pedersen (2014), *Betting Against Beta*; and Baker, Bradley & Wurgler
(2011), *Benchmarks as Limits to Arbitrage: Understanding the Low-Volatility
Anomaly*. Low volatility here means return volatility, not a claim to reproduce
the full beta-neutral or leveraged portfolios in those papers.

## Replication evidence

`go test ./internal/strategy/library` checks every configured source decision
and every next-session trade against the retained upstream-generated reference
on the same adjusted OHLC input. The reference binds the full config/hash,
library version, input hash and upstream revisions, and covers all four shipped
defaults plus separate two-name selection cases. It covers membership exit/re-entry, unchanged
hold intervals, state/identity copying, invalid parameters and next-open fills.
Target tolerance is `2e-11` absolute; share-delta tolerance is `1e-6`. Counts,
assets and dates must match exactly. The reference execution adapter uses
fractional shares, zero costs and next-session opens to match this engine
configuration; it does not assert equivalence to bt's default execution model.
Real execution costs and account refusals remain in the existing engine suites.

The fixture contains real adjusted SPY, QQQ and IWM prices, acquired solely for
numerical replication. Its availability clocks, ETF-symbol fixture IDs and membership transitions are
synthetic. It proves recipe/engine parity, not a historical point-in-time
backtest, a large-cap universe, profitability, or eligibility for incubation.
