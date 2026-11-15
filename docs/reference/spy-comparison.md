# Independent SPY comparison

Issue #94 is the real-data exit check for daily stock history. The comparison
must be allowed to fail. Complete dates alone do not qualify price history.

The offline command uses Mantik's `marketdata.Adjust` on retained raw bars and
cash dividends with actual pay dates. It compares adjusted closes against a
separately acquired Yahoo Finance series and checks both date sets against the
Alpaca exchange calendar. The declared relative price tolerance is **5 basis
points (0.05%)**. Every missing date, unexpected date and difference outside
tolerance appears in the JSON report. A failed comparison exits nonzero.

The private check on 2026-11-15 used these retained inputs:

- The Wave 4 raw SIP SPY pages, 2016-01-04 through 2026-11-13.
- The complete free Polygon dividend response: 43 USD cash dividends with
  ex-dates and pay dates in the interval. It has no next page.
- Yahoo Finance's separately acquired daily adjusted closes.
- Alpaca's calendar from 2016-01-01 through 2026-11-13.

Run the normalization and check from the repository root on Kvant:

```sh
python3 scripts/spycheck/prepare.py \
  --journal /tmp/mantik-wave4-evidence/stock-journal-v2 \
  --dividends /tmp/mantik-wave5/polygon.json \
  --reference /tmp/mantik-wave5/yahoo.json \
  --calendar /tmp/mantik-wave5/calendar.json \
  --asset-id b28f4066-5c6d-479b-a2af-85dc1a8f16fb \
  --end 2026-11-13 --output /tmp/mantik-wave5/spy-input.json
mise exec -- go run ./scripts/spycheck \
  -input /tmp/mantik-wave5/spy-input.json -tolerance-bps 5 \
  > /tmp/mantik-wave5/spy-report.json
```

The normalizer checks retained page digests, refuses incomplete dividends,
duplicate bar dates and missing adjusted closes, and writes a SHA-256 manifest
beside the normalized input. Keep all responses, normalized input and generated
reports outside Git. The checker performs no network calls and needs no secrets.
Source acquisition remains an explicit read-only step; do not copy credentials
into a command or evidence attachment.

## Observed result and limits

All **2,733** calendar sessions are present in both series, with no unexplained
missing or extra dates. **81** adjusted closes exceed 5 basis points; the largest
difference is **287.305282 basis points** on 2020-03-12. That date's raw close is
255.24 in both the retained and freshly re-requested Alpaca response, versus
248.110000610352 in Yahoo. This establishes a provider discrepancy, not its
cause. The complete difference list and input hashes are attached to #94.
The comparison remains **failed**; #94 and the data epic's price-accuracy exit
must remain open until the source differences are resolved or explicitly
reviewed. Do not widen the tolerance or replace raw bars to manufacture a pass.

Mantik's pay-date reinvestment return multiple is **4.444520851096**; Yahoo's
adjusted-close endpoint ratio is **4.462360739624**. These are different return
conventions: Mantik waits for the payable date to reinvest. Their difference
is reported separately and is not a portfolio performance or profitability
claim. An independent Decimal calculation applies the same Polygon pay dates to
Yahoo's raw closes. Its return multiple is **4.444496971583**, only
**0.053728269257 basis points** from Mantik, within the same 5-basis-point
tolerance. Both adjusted-close agreement and this like-for-like return
comparison must pass. The adjusted-close endpoint ratio is informational;
its different reinvestment convention does not set the pass bit.

Historical bars and dividends acquired now do not establish what Mantik knew
in 2016. Actual acquisition clocks remain intact. This check does not establish
an engine SPY reproduction (#105), strategy qualification, or a natural paper
window. It adds no trading authority.

Sources: [Yahoo historical-price field definition](https://finance.yahoo.com/quote/SPY/history/),
[Polygon dividend reference](https://polygon.io/docs/rest/stocks/corporate-actions/dividends),
[Alpaca market calendar](https://docs.alpaca.markets/us/reference/legacycalendar),
and [Alpaca bar aggregation rules](https://docs.alpaca.markets/us/docs/market-data-faq).
