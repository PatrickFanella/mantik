# Indicator-library decision

**Decision date:** 2026-11-15
**Decision:** use [`github.com/markcheno/go-talib`](https://github.com/markcheno/go-talib) at commit [`4ff5e103edd951315cf0e1bafd8602cb20eb247f`](https://github.com/markcheno/go-talib/tree/4ff5e103edd951315cf0e1bafd8602cb20eb247f) for #76. Do not add it to `go.mod` until that issue implements the adapter and parity tests.

## Why

Mantik's daily strategy view supplies an ordered in-memory bar series. `go-talib` takes `[]float64` series directly, returns full-size `[]float64` outputs, and exposes the required functions: `Sma`, `Ema`, `Rsi`, `Atr`, `BBands`, and `StdDev`. That is a direct fit for a deterministic strategy helper that materializes the already point-in-time-filtered bars from `strategy.View`.

The selected source is MIT-licensed ([license at the pinned commit](https://github.com/markcheno/go-talib/blob/4ff5e103edd951315cf0e1bafd8602cb20eb247f/LICENSE)), which can be incorporated without changing Mantik's GPL-3.0 distribution terms. `cinar/indicator` is AGPL-3.0 ([license at its pinned commit](https://github.com/cinar/indicator/blob/5c05d1e9396288243abc36f643a99fced0ccf139/LICENSE.md)). Selecting MIT avoids introducing AGPL's additional network-source obligations into this service.

`cinar/indicator` is actively maintained and has a broader, generic streaming API, including context-aware channels and typed SMA, EMA, RSI, ATR, Bollinger-band, and moving-volatility components. It is not selected because its default ATR uses an SMA of true range, while TA-Lib uses Wilder smoothing. An adapter could configure a different moving average, but that would make the later parity contract less direct and leave the licensing problem.

## Reproducible comparison

The compared revisions were:

| Candidate | Revision | License | API model |
| --- | --- | --- | --- |
| `markcheno/go-talib` | `4ff5e103edd951315cf0e1bafd8602cb20eb247f` | MIT | Full `[]float64` vectors; zeroes before lookback |
| `cinar/indicator/v2` | `5c05d1e9396288243abc36f643a99fced0ccf139` (`v2.1.46`) | AGPL-3.0 | Generic channels; result starts after `IdlePeriod` |

TA-Lib Python 0.8.3 was installed in an isolated temporary environment and compared with `go-talib` on 721 adjusted SIP daily SPY bars retrieved from Alpaca's Market Data API on 2026-11-15. The interval was 2024-01-01 through 2026-11-14 UTC; the returned bars cover 2024-01-02 through 2026-11-13. The retained raw provider response is outside Git at `/tmp/mantik-indicator-spy-alpaca.json`, with SHA-256 `076ace6b4a2683dd4ee9380ba9eebe7256b8f6ec64c3991bfbf51492d6d11350`.

For 14-period SMA, EMA, RSI, and ATR, and 20-period two-standard-deviation Bollinger bands, the largest absolute differences between `go-talib` and TA-Lib were:

| Output | Largest absolute difference |
| --- | ---: |
| SMA | 0 |
| EMA | `1.1368683772161603e-13` |
| RSI | `4.2632564145606011e-14` |
| ATR | `1.0658141036401503e-14` |
| Bollinger upper/lower | `2.1532287064474076e-10` |
| Bollinger middle | 0 |
| 20-day rolling population standard deviation of daily simple returns | `1.4398204850607499e-16` |

On the same series, `cinar/indicator` agreed with `go-talib` to floating-point roundoff for SMA, EMA, RSI, population moving standard deviation, and Bollinger bands. For the rolling-volatility comparison, both use the 720 daily simple returns `close[t] / close[t-1] - 1`, a 20-session window, and population standard deviation (`ddof=0`); their largest difference was `8.023096076392733e-17`. Its default ATR differed by up to `6.75575580159925`, which is the expected consequence of its documented SMA default instead of TA-Lib's Wilder ATR.

Both pinned repositories' suites passed after the TA-Lib environment was made available: `go test ./...` for `go-talib` and `go test ./...` for `cinar/indicator/v2`. The external source, temporary check module, virtual environment, and generated outputs are under `/tmp` and are not repository evidence.

This is a read-only data observation and a numerical-library comparison. It does not establish source coverage, running-paper behavior, or profitability. #76 must retain a versioned SPY input fixture and repeat these parity checks in Mantik's own test suite before any stock strategy relies on them.

## Required #76 implementation

1. Add the pinned `go-talib` pseudo-version for commit `4ff5e103edd951315cf0e1bafd8602cb20eb247f` to `go.mod` and verify the module checksum in `go.sum`.
2. Add a narrow Mantik indicator adapter that accepts only ordered, adjusted point-in-time bars. It must expose each indicator's lookback and reject short, invalid, or non-finite input rather than letting zero-filled pre-lookback values reach a strategy.
3. Store or assert explicit warm-up semantics: TA-Lib vectors are full length and use leading zeroes, while Mantik must treat those indexes as unavailable.
4. Add parity tests for SMA, EMA, RSI, Wilder ATR, Bollinger bands, and rolling population volatility against TA-Lib 0.8.3 values generated from a retained, versioned SPY input fixture. The test must pin the fixture hash and distinguish adjusted prices from raw OHLC values.
5. Keep the adapter pure and local. It must not read the clock, fetch data, make orders, or change the strategy interface.

This decision selects a library for a later implementation. It supplies no strategy, market-data, paper-trading, or profitability evidence.
