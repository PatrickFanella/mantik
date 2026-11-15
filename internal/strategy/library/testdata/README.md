# Stock recipe references v1

Input: 721 adjusted SIP daily OHLC bars each for IWM, QQQ and SPY, from
2024-01-02 through 2026-11-13. Read from Alpaca on 2026-11-15, request
`timeframe=1Day`, `feed=sip`, `adjustment=all`, start 2024-01-01 UTC, exclusive
end 2026-11-14 UTC. Original response stays outside Git.

- Original response SHA-256:
  `23c98dbd75a592b52af7da377bacf447f46cd9d9d6f1aa547a0931f5604d775a`.
- `adjusted-v1.csv` SHA-256:
  `f6fa15c42a70121716541e962d1a5d27bd48f41d63bd980428702a69d5ccd391`.
  LF endings, sorted by asset/date, columns asset/date/OHLC/volume.

`generate.py` checks the CSV hash and the installed bt/ffn Git revisions before
executing upstream return, ranking and sizing algorithms. Install the exact
Git revisions recorded in [the library reference](../../../../docs/reference/stock-library.md)
in a temporary Python environment to regenerate `recipes-v1.json`. Its seven cases cover all four shipped defaults and separate `Count=2`
cross-sectional selection variants. Each case binds the complete config and
its SHA-256; the root also binds code version, input hash and upstream revisions.
Generation used Python 3.13, numpy 2.5.5 and pandas 3.1.1. Runtime/CI tests
require Go only and compare all available decisions and executed trade deltas.

Membership removes QQQ for input session indexes 420–479, then reintroduces it.
That membership, bar availability clocks and the zero-cost fractional-share
next-open execution adapter are deliberate synthetic test conditions. They are
not real historical universe membership or point-in-time acquisition evidence.
This fixture supplies no performance or qualification claim.

The executable upstream algorithms are MIT licensed by Philippe Morissette;
retain the accompanying license notices when copying these references.

To regenerate in an isolated environment (not during CI):

```sh
uv venv --python 3.13 /tmp/mantik-stock-reference
uv pip install --python /tmp/mantik-stock-reference/bin/python 'bt @ git+https://github.com/pmorissette/bt@763ae01047fce2d09411faeba436855ecf8b679c' 'ffn @ git+https://github.com/pmorissette/ffn@eb9354c5c82dbada5645569dc34e5861d156e471' 'numpy==2.5.5' 'pandas==3.1.1'
/tmp/mantik-stock-reference/bin/python internal/strategy/library/testdata/generate.py
```

The filename identifies fixture format v1; the root `Version` identifies the
recipe code version, currently `bt-763ae010-ffn-eb9354c5-recipes-v2`.
