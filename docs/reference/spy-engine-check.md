# SPY engine accounting check

Issue #105 checks the daily engine on the complete 2016-to-date SPY history
previously compared in #94. It uses the explicit primary-exchange closing basis
and its explained 2019 SIP trade-data gap. The original raw SIP report and every
source discrepancy remain in the combined result. The price tolerance stays
at five basis points; the input manifest verifies all source bytes and auction
prints before the accounting check runs.

Generate the independent reference, then run the combined checker:

```sh
python3 scripts/spycheck/engine_reference.py \
  --input /tmp/mantik-wave5-close/spy-input.json \
  --output /tmp/mantik-wave8-evidence/spy-reference-v2.json
mise exec -- go run ./scripts/spycheck \
  -input /tmp/mantik-wave5-close/spy-input.json \
  -manifest /tmp/mantik-wave5-close/spy-input.manifest.json \
  -close-basis primary-close -tolerance-bps 5 \
  -engine-reference /tmp/mantik-wave8-evidence/spy-reference-v2.json
```

The Python Decimal reference does not call Mantik. It independently applies
$2,000 opening cash, $500 every other Friday at 06:00 Chicago, next-session
opening purchases, and dividends earned on ex-date and paid/reinvested on their
payment date. It generates zero-cost and five-bp slippage references. The Go
checker binds their input hash, convention/version and funding settings, and
compares every daily return and cumulative TWR at a fixed 0.0001-bp arithmetic
tolerance. Final equity must also agree within one cent. Both cash and margin
runs must have exactly equal cash, equity and TWR, no refusals and no cash debit.

The engine's first decision is on the first session; its first purchase uses
the next opening. Accordingly the independent close-to-close SPY total-return
multiple is aligned by `first close / first executable open`. The zero-cost
run must match that aligned SPY reference within five bp. The modeled-cost run
must stay within that tolerance plus its measured slippage drag relative to
the zero-cost run. Entry alignment, independent SPY multiple, errors, tolerances
and drag are explicit report fields; a correct next-session entry is not
silently treated as a first-close fill.

The run includes receivable dividends in marked equity from ex-date, including
short-dividend liabilities. Those amounts stay outside spendable cash and
buying-power checks until payment. This prevents a deposit between ex-date and
pay date from diluting or creating performance. `Unlevered` clips buys to local
settled cash including costs, so full-investment targets do not borrow for fees
or get refused merely because slippage exceeds the cash target.

This harness assigns synthetic historical availability clocks to a separate
computation fixture. Original present-day provider acquisition times remain
untouched. It validates accounting on historical real prices, not the historical
availability of a strategy's signals, protected paper execution or prospective
qualification. Retain the complete JSON and its hashes outside Git and attach
the reviewed summary to #105.
