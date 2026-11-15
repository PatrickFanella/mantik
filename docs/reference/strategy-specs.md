# Typed strategy specifications

`internal/strategy/spec` compiles a reviewed expression tree into the same
`strategy.Strategy` interface used by the daily engine. A definition contains
one numeric `score` tree or one reviewed `library` recipe, a name, gross limit,
long-only choice and whether to restrict ranks to the point-in-time universe.
It cannot name functions, run code, acquire data, read clocks or place orders.

Example, holding positive one-month momentum names in the declared universe:

```json
{
  "name": "positive-month-return",
  "gross": 0.7,
  "long_only": true,
  "universe": true,
  "score": {
    "op": "return",
    "window": 21,
    "args": [{"op": "field", "field": "close"}]
  }
}
```

`Parse` rejects unknown JSON fields, trailing content and inputs over 64 KiB.
`Compile` rejects unknown operators, wrong arity/fields/types, cyclic trees,
forward/zero lags, invalid windows, nonfinite or excessive constants and
excessive cost. Compilation copies the tree. `Spec()` also returns independent
metadata, preventing caller edits from changing the executable definition.

## Reviewed vocabulary

| Operator | Inputs | Output and semantics |
| --- | --- | --- |
| `const` | finite `value`, absolute value ≤ 1,000,000 | Numeric constant. |
| `field` | `field`: open, high, low, close, volume | Adjusted view value at the indexed session. |
| `lag` | one numeric argument, positive `window` | That argument from earlier sessions only. |
| `return` | one numeric argument, positive `window` | Current / lagged value − 1; zero denominator is unavailable. |
| `mean`, `std` | one numeric argument, window ≥ 2 | Rolling mean/population deviation, including current session. |
| `rank` | one numeric argument | Cross-sectional average rank divided by available count, within eligible configured assets. |
| `abs`, `sign` | one numeric argument | Absolute value or −1/0/1. |
| `add`, `sub`, `mul`, `div` | two numeric arguments | Arithmetic; division by zero is unavailable. |
| `gt`, `lt` | two numeric arguments | Boolean comparison. |
| `where` | boolean condition, two numeric branches | Only the selected branch is evaluated. |
| `option_iv`, `option_delta` | no arguments | Mean of available estimates in that underlying's declared indicative chain. |

The root must be numeric. Option terms read the current snapshot only; applying
history offsets to them produces unavailable values, not a fabricated historical
chain. A library definition delegates to the reviewed library's scheduling,
side, universe and turnover policy; its gross must match the library config.
It cannot also contain an expression or override the library's side/universe.

The vocabulary starts from qlib's `Ref`, `Mean`, `Std`, `Rank` and Alpha158 price
features. Semantics and scope are explicit Mantik choices; arbitrary qlib strings,
Python execution and the full Alpha158 feature set are not accepted.
Reference: [`microsoft/qlib`, MIT, commit `54355232463878d2eebb91fe0ee5fa7fa1f5976c`,
`qlib/data/ops.py`](https://github.com/microsoft/qlib/blob/54355232463878d2eebb91fe0ee5fa7fa1f5976c/qlib/data/ops.py).

## Bounds and missing data

Maximums: 64 nodes, depth 12, window 252 sessions, total inferred lookback 504
sessions, and 200,000 evaluation steps. Compile also checks a conservative
per-asset work estimate against the existing 1,000-asset view bound; combinations
can therefore fail the cost limit before reaching a window's individual limit.
Runtime counting includes cached lookups and cross-sectional operations.
Each rank node/offset evaluates and sorts the cross-section once, assigns
average ranks to ties, and caches every asset's result (including unavailable
values). This supports ordinary ranks over the full 1,000-asset view without
repeated quadratic scans; the 200,000-step budget remains enforced.

Warm-up is inferred from the expression. Evaluation copies only the required
recent bars. The builder rejects future source observations, action clocks,
universe references and chain clocks. Missing, invalid, zero-denominator or
nonfinite intermediate values stay unavailable. Rankings exclude unavailable
values. LongOnly clips negative scores to zero; otherwise scores can be signed.
Finite scores normalize to the declared absolute gross, with weights truncated
toward zero at 12 decimals. No available nonzero scores means zero targets.

Operator version `typed-expr-v2` records this rank evaluation behavior.
`Spec.ID()` includes the complete normalized definition's SHA-256, operator
version, inferred warm-up and required data kinds. Library-backed definitions
also bind the library code version. Changing a literal, operator, window,
policy or library parameter changes the identity. Identical inputs run through
the ordinary engine and use its next-session fills and account rules.

`go test ./internal/strategy/spec ./internal/engine` checks typed evaluation,
missing data, parser/type/work bounds, cycles, immutable identities, a compiled
strategy running in the engine, and the no-rebalance contract. These are
mechanics tests, not backtest-gate decisions or qualification evidence.
