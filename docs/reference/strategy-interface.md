# Strategy interface

`internal/strategy` defines the one interface every trading strategy uses, in
backtests, incubation and paper trading. A strategy reads a point-in-time view
at its decision time and returns target weights. Code outside the strategy
turns targets into orders.

```go
type Strategy interface {
	Spec() Spec
	Decide(View) (Decision, error)
}
```

`Decide` must depend only on the view: the same view gives the same decision.

## Spec and identity

| Field | Meaning |
| --- | --- |
| `Name` | Lower-case name, for example `equal-weight`. |
| `Version` | The strategy code's version. Bump it whenever the code's behaviour changes. |
| `Params` | Parameters as text, keyed by lower-case names. |
| `Data` | Declared reads: `daily_bars_adjusted`, `option_chains_indicative`, and `stock_universe`. |
| `Warmup` | Bars needed before the first decision. The engine (#65) waits for them. |

`Spec.ID()` is `strategy-` followed by the SHA-256 of the name, version,
parameters, data kinds and warm-up. Parameter order and repeated or reordered
data kinds do not change it, so the same code version, parameters and data
kinds always give the same ID. Any change to one of them gives a new ID.

## The view

`Build(ctx, spec, source, at, assets, book)` reads the view for one decision:

- **Bars:** for each asset, adjusted daily bars from `source` as of `at`, only
  when the spec declares `daily_bars_adjusted`. The source is typically
  [`marketdata.Read`](adjusted-bars.md) with that asset's corporate actions.
- **Book:** the strategy's own positions, fills and state, kept by the engine.
- **Option chains:** for declared `option_chains_indicative`, the source must
  also implement `ChainSource`. `View.OptionChain(assetID)` returns a copied
  [indicative chain](option-chains.md), including pricing assumptions, source
  clocks and explicit estimate availability. Asset IDs still name underlyings.
- **Universe:** declared `stock_universe` requires `UniverseSource` and exposes
  only eligible configured IDs from `UniverseReport.Members`. The report clock
  must match and its reference observation must already be known; members must
  cite a prior session. The engine's configured set bounds data and holdings,
  while membership can change per decision. A late current reference is refused,
  even if the raw screening helper labels its survivorship limitation.

Build fails with `ErrFutureData` when the source returns a bar available after
`at`, a corporate action known after `at`, or a series read for another clock,
and when the book holds a fill after `at`. A strategy therefore never sees data
from after its decision time, even from a faulty source.

The view's fields are unexported and its methods return copies. `View.At()` is
the decision time the view was built for, not the wall clock.
`RecentBars(asset, count)` copies only the requested latest bars, bounding
indicator/interpreter work independently of the retained history length.

## Decisions

A decision is the complete set of targets plus the state for the next
decision, at most 64 KiB. A target is the weight of the strategy's sleeve to
hold in one asset, as exact decimal text from -1 to 1; negative is short and
an asset left out is held at zero. The capital distributor (#73) turns weights
into quantities.

`strategy.Decide(s, view)` calls the strategy and checks the result: each
target names a view asset once, weights are plain decimals, and the absolute
weights sum to at most 1. Targets come back sorted by asset. A panic in the
strategy becomes `ErrInvalidDecision`.

`KeepPositions=true` is a no-rebalance decision and cannot also contain targets.
The engine preserves shares and existing unfilled orders, carries the new state
and advances the session. This allows monthly/weekly strategies to hold between
their scheduled decisions without daily rebalancing. Empty targets with
`KeepPositions=false` still mean flatten the complete target set.

## What a strategy cannot do

The interface gives a strategy no order, clock or network handle. `Audit(dir)`
also checks the source of a strategy package:

- imports must come from `AllowedImports`: `cmp`, `errors`, `fmt`, `math`,
  `math/big`, `slices`, `sort`, `strconv`, `strings`, `time`, the pure
  `crypto/sha256`, `encoding/hex`, `encoding/json`, `io` packages, and Mantik's
  `marketdata`, `indicators`, `strategy` and `strategy/library` packages;
- the time package's clock functions (`Now`, `Since`, `Until`, `Sleep`,
  `After`, `AfterFunc`, `Tick`, `NewTicker`, `NewTimer`) are refused, under any
  import name, and `time` cannot be dot-imported.

So a strategy cannot reach the network, files, processes or randomness, place
orders, or read the clock. A test runs the audit over every package under
`internal/strategy/`, so a new strategy that breaks a rule fails `make verify`.

The pure [indicator adapter](indicators.md) computes only from supplied adjusted
series. Its dependency is not directly allowed in strategy packages.

[Stock library recipes](stock-library.md) and [typed specifications](strategy-specs.md)
use this same audited interface. Their execution remains subject to engine
costs and account rules; compilation and numerical replication do not promote
a strategy or create a backtest-gate decision.

## Limits

- Targets remain stock weights. Indicative option chains supply research data;
  option structures, execution and Greek risk limits come with later issues.
- `Version` is declared by hand; the audit does not detect a behaviour change
  without a version bump.
