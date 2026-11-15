# Adjusted-SPY parity fixture v1

`spy-adjusted-v1.csv` is the retained 721-row adjusted SIP SPY OHLC input
from the #60 comparison, covering 2024-01-02 through 2026-11-13. This is a
numerical regression fixture, not a point-in-time backtest qualification set.
It uses provider-adjusted OHLC, not the original unadjusted bars and not
Mantik's pay-date total-return series.

- Acquisition: Alpaca Market Data historical stock bars, `SPY`, `1Day`,
  `feed=sip`, `adjustment=all`, start 2024-01-01, end 2026-11-14 UTC;
  retrieved 2026-11-15 in the #60 spike.
- Original provider JSON SHA-256:
  `076ace6b4a2683dd4ee9380ba9eebe7256b8f6ec64c3991bfbf51492d6d11350`.
  The original response remains outside Git.
- Retained CSV SHA-256:
  `5a7293defdb4639fdc3d0fb96b71954e49f27e4b921a75dd884ebbfe776a4755`.
  Columns are provider timestamp and adjusted open/high/low/close; line endings are normalized
  to repository-standard LF. The Go test pins this hash.
- Reference: TA-Lib Python 0.8.3 and C library 0.8.3. Run `generate.py` in
  an isolated environment with `TA-Lib==0.8.3` to reproduce `talib-v1.json`.
  Runtime tests require Go only; TA-Lib and Python are not app dependencies.
- Periods: SMA/EMA/RSI/Wilder ATR 14; Bollinger 20, SMA ±2 population
  price standard deviations; volatility 20 daily simple returns, population
  standard deviation, without annualization.
- Absolute tolerances: `1e-8` for prices/RSI, `1e-12` for return volatility.
  Every available output and its starting bar index is checked.

The maintained test fixtures are intentional source-controlled test inputs
and expected values. Runtime acquisition journals and qualification evidence
remain outside Git.
