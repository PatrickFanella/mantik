# Independent SPY comparison

Issue #94 compares retained daily history against independent free sources.
The explicit primary-close check passes for all **2,733** sessions from
2016-01-04 through 2026-11-13. The largest adjusted-close difference is
**1.041302211074 basis points**. The tolerance remains **5 basis points
(0.05%)**, for both prices and comparable pay-date returns.

The combined report also keeps the original raw-SIP comparison and a
Yahoo-only primary-close diagnostic. Those retain their failures rather than
certifying incompatible price conventions. No stored bar is rewritten.

## Close basis

Alpaca aggregates daily bars using SIP trade-condition rules. An eligible late
print can update the daily close after the regular session. State Street
defines its ETF closing price by the primary listing exchange. The SPY check
therefore selects tape B / exchange P records: condition `6` (closing auction)
or `M` (official close notice), without disqualifying conditions.

The first raw-SIP/Yahoo comparison found 81 outliers above 5 bps. All 81 match
Yahoo when using the primary auction price. On 2020-03-12, Arca closing-auction
records with SIP timestamps 16:00:00.119346965 and 16:00:00.129900000 ET are
248.11. Eligible regular/intermarket-sweep records at 16:07:10.941179033 ET are
255.24, the raw daily bar's close. These are different close conventions.

Every session requires a retained SIP window from the calendar close through
one second later, inclusive, including sessions that already agreed. The
calendar controls holidays, early closes and DST. Normalization validates
queries, original bytes, digests, acquisition clocks and complete pagination.
Selected primary records must agree on one positive price. One session,
2025-09-05, has an official-close `M` notice instead of an auction `6` print;
it is counted separately. Missing or conflicting evidence fails except for
the single independently filled source gap below.

The Go check requires explicit `-close-basis primary-close`, the input and its
manifest. It rechecks the input hash, all source-file hashes, selected original
SIP prices, and both sources supporting the explained gap. Edited input prices
or unrelated digests cannot certify a projection. `-close-basis raw-sip` keeps
the original diagnostic; it does not certify a primary-close projection.

## Independent reference

Yahoo's older prices also use a different close convention on some dates.
A Yahoo-only primary-close comparison leaves 11 early-2016 outliers, with a
maximum of 11.374026161135 bps. Contemporary SEC-hosted issuer disclosures
corroborate the primary prices on inspected dates, including 185.81 on
2016-01-20 and 183.03 on 2016-02-11. No undocumented Yahoo change date is assumed.

The independent reference uses the **entire available coverage** of
QuantConnect's free public LEAN SPY daily sample, then Yahoo after its last
sample date, **2021-03-31**. The source boundary comes from sample coverage,
never a price-error threshold. A missing earlier sample date fails instead
of falling back selectively to Yahoo. The ZIP is pinned to LEAN commit
`2b0fd2e6078575fcfd70d4d5b2ff91342f37953c`; raw prices are in 1/10000 USD.
This uses the public repository sample, not QuantConnect's paid download API.

An independent Python Decimal calculation adjusts these reference closes
using the complete free Polygon response: 43 SPY cash dividends with actual
pay dates and no next page. It also calculates pay-date-reinvestment returns.
Mantik's Go adjustment code supplies the other calculation. Yahoo's original
adjusted-close endpoint ratio uses a different reinvestment convention and
remains informational. Both price and comparable-return gates must pass.

## Explained SIP gap

The retained 2019-08-12 close window is empty, as is a separate 15:59–16:05
probe. Nasdaq's dated notice documents an SIAC SIP issue affecting that day.
The daily raw bar remains available, but its 287.93 close is not silently
certified as an auction price.

For this exact date only, the projection uses the independently retained free
Nasdaq history close, **288.07**. The independent LEAN reference close is
**288.10**, within the same 5 bps tolerance. The report labels this
`nasdaq_sip_gap`, records both the empty SIP response and Nasdaq source hashes,
and gives the notice URL. No auction timestamp or trade ID is invented.
Other missing dates or nonempty unexplained windows fail; the Go verifier
refuses any fallback price that differs from the retained Nasdaq row.

## Reproduction on Kvant

All responses and generated evidence stay outside Git. Acquire the public
sample from its pinned URL:

```sh
curl -fLsS \
  https://raw.githubusercontent.com/QuantConnect/Lean/2b0fd2e6078575fcfd70d4d5b2ff91342f37953c/Data/equity/usa/daily/spy.zip \
  -o /tmp/mantik-wave5-close/lean-spy-pinned.zip
curl -fLsS -H 'User-Agent: Mozilla/5.0' -H 'Accept: application/json' \
  'https://api.nasdaq.com/api/quote/SPY/historical?assetclass=etf&fromdate=2016-01-01&todate=2026-11-13&limit=5000' \
  -o /tmp/mantik-wave5-close/nasdaq.json
python3 scripts/spycheck/collect_closes.py \
  --credentials /tmp/mantik-wave4-evidence/credentials.json \
  --calendar /tmp/mantik-wave5/calendar.json \
  --end 2026-11-13 --output /tmp/mantik-wave5-close/auction-pages
python3 scripts/spycheck/prepare.py \
  --journal /tmp/mantik-wave4-evidence/stock-journal-v2 \
  --dividends /tmp/mantik-wave5/polygon.json \
  --reference /tmp/mantik-wave5/yahoo.json \
  --calendar /tmp/mantik-wave5/calendar.json \
  --asset-id b28f4066-5c6d-479b-a2af-85dc1a8f16fb \
  --end 2026-11-13 --auction-pages /tmp/mantik-wave5-close/auction-pages \
  --lean-sample /tmp/mantik-wave5-close/lean-spy-pinned.zip \
  --nasdaq /tmp/mantik-wave5-close/nasdaq.json \
  --output /tmp/mantik-wave5-close/spy-input.json
mise exec -- go run ./scripts/spycheck \
  -input /tmp/mantik-wave5-close/spy-input.json \
  -manifest /tmp/mantik-wave5-close/spy-input.manifest.json \
  -close-basis primary-close -tolerance-bps 5 \
  > /tmp/mantik-wave5-close/spy-report.json
```

The collector is GET-only, rejects redirects, reads a mode-0600 credentials
file and requires a mode-0700 output directory. Requests are paced below the
free limit; recent SIP queries are refused. Ten pages and 4 MiB per response
bound each date's read. Retry count and delays are bounded. Interrupted reads
resume only from matching retained query/digest pairs. Empty responses remain
evidence, not invented auction prices.

## Result and limits

There are **2,732 primary-exchange-backed sessions** (2,731 auction prints and
one official-close notice) and **one independently filled, explained SIP gap**.
No calendar dates are missing or unexpected. No adjusted close exceeds 5 bps.
Mantik's pay-date return multiple is **4.444718438082**; the independent result
is **4.444718409287**, a relative difference of **0.000064784757 bps**.
The complete report, difference lists, coverage boundary and evidence hashes
are attached to #94. Its earlier failed report remains part of the history.

This projection is for the SPY data check. Raw bars, revisions and acquisition
clocks remain intact. The live collector and strategy engine still read raw
SIP bars; #105 must explicitly select and retain its actual close basis.
Historical evidence acquired now does not establish what Mantik knew in 2016.
This check grants no order authority, engine reproduction, strategy
qualification, profitability claim or natural paper window.

Sources: [Alpaca aggregation rules](https://docs.alpaca.markets/us/docs/market-data-faq),
[State Street closing-price definition](https://www.ssga.com/us/en/institutional/etfs/state-street-spdr-sp-500-etf-trust-spy),
[pinned LEAN source](https://github.com/QuantConnect/Lean/tree/2b0fd2e6078575fcfd70d4d5b2ff91342f37953c/Data/equity/usa/daily),
[Yahoo price fields](https://finance.yahoo.com/quote/SPY/history/),
[Polygon dividends](https://polygon.io/docs/rest/stocks/corporate-actions/dividends),
[Nasdaq's SIP-issue notice](https://www.nasdaqtrader.com/TraderNews.aspx?id=ETA2019-68),
[contemporary January 2016 issuer disclosure](https://www.sec.gov/Archives/edgar/data/1114446/000111444616006246/tc3018199i_0fwp.htm),
and [February 2016 disclosure](https://www.sec.gov/Archives/edgar/data/1114446/000111444616006679/tc3065137i_0fwp.htm).
