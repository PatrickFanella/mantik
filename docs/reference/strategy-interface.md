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
| `Data` | The data kinds the strategy reads. Only `daily_bars_adjusted` exists so far. |
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

Build fails with `ErrFutureData` when the source returns a bar available after
`at`, a corporate action known after `at`, or a series read for another clock,
and when the book holds a fill after `at`. A strategy therefore never sees data
from after its decision time, even from a faulty source.

The view's fields are unexported and its methods return copies. `View.At()` is
the decision time the view was built for, not the wall clock.

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

## What a strategy cannot do

The interface gives a strategy no order, clock or network handle. `Audit(dir)`
also checks the source of a strategy package:

- imports must come from `AllowedImports`: `cmp`, `errors`, `fmt`, `math`,
  `math/big`, `slices`, `sort`, `strconv`, `strings`, `time`, and Mantik's
  `marketdata` and `strategy` packages;
- the time package's clock functions (`Now`, `Since`, `Until`, `Sleep`,
  `After`, `AfterFunc`, `Tick`, `NewTicker`, `NewTimer`) are refused, under any
  import name, and `time` cannot be dot-imported.

So a strategy cannot reach the network, files, processes or randomness, place
orders, or read the clock. A test runs the audit over every package under
`internal/strategy/`, so a new strategy that breaks a rule fails `make verify`.

## Limits

- Only stock targets and daily bars. Option structures, Kalshi orders and
  other data kinds come with their epics.
- `Version` is declared by hand; the audit does not detect a behaviour change
  without a version bump.
