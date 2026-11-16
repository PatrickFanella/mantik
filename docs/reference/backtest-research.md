# Walk-forward research and statistics

`internal/backtest.WalkForward` runs reviewed candidates through the shared
engine and the existing [trial registry](strategy-trials.md). It accepts an
explicit ordered exchange calendar, engine settings, a fold design, a harsher
cost model, and fresh-strategy factories for the base and declared neighbours.
It returns the complete experiment; it does not activate a strategy or issue
the later backtest gate decision.

`Design{Train, Test, Purge, Embargo}` uses sessions. Training expands forward
and ends before the test's purge window. Embargo sessions after earlier tests
are excluded from every later training set. The training source physically
filters those dates. Set purge to cover the family's longest holding/label
horizon; the runner cannot infer that horizon from arbitrary Go strategies.
Only complete equal-length test folds run. A bounded manifest retains exact
training/test list hashes, start/end dates, counts, design and calendar hash.
The returned report includes full date lists; retain it with the input manifest.

Every candidate gets independent training, OOS and stressed OOS attempts per
fold. Each gets an immutable registry start, returns and terminal outcome.
Per-run factory/engine failures count and do not hide remaining neighbours.
Invalid preflight declarations are rejected before execution; cancellation or
an unavailable registry can prevent further attempts. Stress requires every
fee/slippage component to be at least its base, with at least one strictly
higher. A harsher model need not yield a lower strategy return on every path.

## Diagnostics

`Assess` enumerates the complete retained family through `FamilyReader`.
Failed, unfinished and repeated attempts remain in the search total. Comparable
completed configurations have the same engine settings, dates and data hash;
the caller cannot choose a favourable subset. Repeated identical configurations
contribute once to the return matrix/dispersion, while every start counts in N.
Changed returns for an identical configuration make diagnostics unavailable.

DSR uses nonannualized Sharpe, sample return variance, population standardized
skewness and Pearson kurtosis. Cross-configuration Sharpe dispersion comes from
all comparable completed configurations. Treating the complete search total as
independent trials is conservative and explicit. The implementation checks the
published 2014 numerical example (N=46 gives about 0.9505). See Bailey and López
de Prado's [DSR paper](https://davidhbailey.com/dhbpapers/deflated-sharpe.pdf).

CSCV uses synchronous daily returns, every combination of half the declared
even partitions, Sharpe selection, stable IS tie-breaking, and average OOS tie
ranks. It requires at least four configurations and four partitions with at
least two observations each. Incomplete comparable trials, constant series,
unbalanced/ragged matrices or exceeded work bounds return a null value and a
reason. See the authors' [CSCV algorithm](https://www.davidhbailey.com/dhbpapers/backtest-prob.pdf).

Cash/SPY benchmarks carry names, source hashes, dates and decimal returns.
Every benchmark date must match the retained trial; equal array length alone
is insufficient. These are descriptive excess returns, not qualification.

## Minimum tradable balance

Every candidate declares its immutable `Spec` and a `TradeabilityPlan` before
execution. A failed initial factory is recorded under that declared identity. It binds the
strategy's content identity, account type, required minimum quantities, marked
prices/fees, fixed structure reserve and mark time. `WalkForward` retains that
declaration in every run. `Assess` reports its minimum balance per strategy,
using the existing account rules and a cent-resolution monotone search.

The answer applies to the reviewed units and prices in that declaration.
It does not infer contract sizes, option maximum loss or required asset counts
from arbitrary code, and it does not grant allocation authority. Missing or
post-execution declarations cannot silently become a strategy minimum. The
later gate must review those sizing requirements alongside its strategy.
