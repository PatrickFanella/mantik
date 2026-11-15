"""Regenerate maintained numeric references; requires TA-Lib Python 0.8.3."""
import csv
import hashlib
import json
from pathlib import Path

import numpy as np
import talib

assert talib.__version__ == "0.8.3"
root = Path(__file__).parent
source = root / "spy-adjusted-v1.csv"
assert hashlib.sha256(source.read_bytes()).hexdigest() == "5a7293defdb4639fdc3d0fb96b71954e49f27e4b921a75dd884ebbfe776a4755"
with source.open() as stream:
    rows = list(csv.DictReader(stream))
high, low, close = (np.array([float(r[k]) for r in rows]) for k in ("High", "Low", "Close"))
upper, middle, lower = talib.BBANDS(close, 20, 2, 2, 0)
outputs = dict(sma=talib.SMA(close, 14), ema=talib.EMA(close, 14),
               rsi=talib.RSI(close, 14), atr=talib.ATR(high, low, close, 14),
               upper=upper, middle=middle, lower=lower,
               volatility=np.r_[np.nan, talib.STDDEV(close[1:]/close[:-1]-1, 20, 1)])
ref = {k: dict(start=int(np.flatnonzero(np.isfinite(v))[0]),
               values=[float(x) for x in v[np.isfinite(v)]]) for k, v in outputs.items()}
(root / "talib-v1.json").write_text(json.dumps(ref, indent=2, allow_nan=False) + "\n")
