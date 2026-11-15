# Strategy indicators

`internal/indicators` is a pure adapter over `github.com/markcheno/go-talib`
`v0.0.0-20260617004048-4ff5e103edd9`, commit
`4ff5e103edd951315cf0e1bafd8602cb20eb247f`, MIT licensed. See the
[library decision](../research/indicator-library-decision.md) and the
[pinned upstream license](https://github.com/markcheno/go-talib/blob/4ff5e103edd951315cf0e1bafd8602cb20eb247f/LICENSE).

Each function accepts a `marketdata.Series` of adjusted bars already read
at the strategy's decision time. For a view, construct that series with its
stable asset ID, `view.At()` and `view.Bars(asset)`. The adapter validates
positive finite OHLC inputs, ordering, asset identity and availability clocks;
it does not fetch data or establish the correctness of upstream adjustments.
Neither the adapter nor go-talib is an order or clock capability. Strategies
may import the Mantik adapter; importing the dependency directly remains
outside the strategy audit allowlist.

| Function | First available bar index | Meaning |
| --- | ---: | --- |
| `SMA`, `EMA` | period − 1 | EMA is seeded by the first period closes' SMA. |
| `RSI` | period | Wilder smoothing of daily changes. |
| `ATR` | period | Wilder smoothing of true range. |
| `BBands` | period − 1 | SMA middle, two population price standard deviations. |
| `Volatility` | period | Population standard deviation of `close[t]/close[t−1]−1`. |

Periods must be at least 2. Short, invalid or nonfinite inputs return
`ErrInput`. Results carry `Start` and only available `Values`, so `Values[0]`
belongs to bar index `Start`. Leading library zeroes never escape as warmed
values. Legitimate zero results remain available. Volatility is a daily
fraction without annualization, not price volatility or sample deviation.

The [versioned SPY fixture](../../internal/indicators/testdata/README.md)
pins its source hash, reference version, generation procedure and tolerances.
`go test ./internal/indicators` checks every output against retained TA-Lib
0.8.3 values without installing another runtime. This is numerical parity,
not strategy-performance or forward qualification evidence.
