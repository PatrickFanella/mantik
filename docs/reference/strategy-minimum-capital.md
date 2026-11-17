# Frozen-snapshot minimum capital

Issue #51's original $572.41 figure priced one SPY share. The separate
`scripts/capitalcheck` reporter now derives each default stock recipe's full
cold-start target basket, sizes fractional shares through `allocation.Distribute`,
and reserves the resulting orders together through the existing account and
whole-book hard-risk checks. It has no broker, database, gate-write or model
client. It cannot activate a strategy.

The pinned reviewed input supplies stable asset identities, current tradability
and fractionability observations, raw and adjusted price provenance, a captured
exchange calendar, explicit large-cap subset review, marks, limits and fees.
Every scoped asset's entire retained series must match the original calendar
body exactly. Each recipe additionally needs its complete warmup and usable
features for every configured member, including members it does not select.
Future source clocks, incomplete scope and missing sessions are refused.

The report joins the four original gate receipts from the published #72 report
by exact strategy ID. It retains their declaration/family hashes, every failed
check and the source-document hash. These are the published historical receipts,
not newly recovered copies of the deleted detached SQL export. All four remain
`failed / idea`. The new snapshot minimum does not cure their historical-data,
SPY-excess or deflated-Sharpe failures or grant incubation.

## Search contract

`backtest.StrategyMinimum` supports a fresh ordinary incubation roster, no
holdings or pending orders, fixed complete targets, zero predeclared minimums,
and gross target weight at most 70% per recipe. Qualified rosters, model caps,
reviews, reused positions and changing decisions are outside this proof.

Within this scope, absolute fractional quantities increase with the distributor's
fee/gap-adjusted equity and truncate toward zero at nine decimal places. A binary
search locates the first cent at which **every** nonzero roster target meets the
existing $1 order floor. Passing requires the full basket, not one order from a
partially sized decision. The ordinary account and risk code then reserves all
orders and rechecks every pending submission against the final whole-book state.

A cash roster containing a short is explicitly infeasible. A short margin
roster requires an equal-mark/limit scenario. Its lower bound is the greater of
the complete-basket sizing boundary and $2,000; its bounded fee band is searched
cent by cent. The report keeps the passing boundary and the actual predecessor
refusal. It does not assume account/risk acceptance is monotone. An unexplained
financial refusal is `account_boundary_unavailable`, not a fabricated minimum
or a proof that no larger balance can work.

`WholeBookEquity` and the subject's sleeve budget are separate fields. The
report includes one independently funded cold-start case per recipe and an
additional joint four-sleeve case. Neither is the owner's operational book.
The joint case selects an active recipe as its subject and retains idle sleeves
in the allocation roster, so one idle recipe cannot suppress the other baskets.
The synthetic financial books receive no deposits, gate authority or
qualification credit. All outputs are `snapshot_financial_diagnostic_unqualified`.

## November 16, 2026 source exercise

The decision is after the November 16 close, at an actual November 17 UTC
acquisition/review clock. All ten histories contain the same 470 captured
exchange sessions from January 2, 2025 through November 16, 2026. Raw and
provider-adjusted OHLC are separate retained captures; original raw fields and
conservative actual acquisition clocks remain in the normalized input. The
adjusted values are the provider's current split/dividend-adjusted snapshot,
not a reconstruction of historical publication vintages.

The complete configured trend universe is SPY, QQQ and IWM. The complete
configured cross-sectional universe is AAPL, MSFT, NVDA, AMZN, JPM, LLY and V.
The seven-name subset has current broker identity/tradability and an agent
source review using SEC DEI share-count filings times the retained raw close.
Those capitalization proxies exceed $10b; their filing vintages and share-class
limitations are recorded per member. They are not exact current market-cap
observations or evidence of historical large-cap membership. GOOGL/META share
reads failed and BRK.B's class interpretation was excluded before forming this
declared subset. No claim covers the entire market's eligible universe.

The financial scenario freezes limits equal to marks and a $0.01 fee bound
per generated order, with the ordinary 5% incubation pool and 70% recipe gross.
Reversal also applies its default 0.25 initial turnover bound; its actual
cold-start target gross is therefore 0.25 rather than the configured 0.70.
These are scenario bounds, not fresh executable quotes or actual venue charges.
All other account, cash-floor, aggregate position/sleeve and hard-risk rules run
unchanged. Every recipe uses its complete shipped default configuration.

| Cold-start recipe | Whole-book margin equity | Cash-book result | Prior cent |
| --- | ---: | --- | ---: |
| Trend | $2,000.03 | Infeasible: the observed basket includes shorts | $2,000.02 refuses margin equity |
| Momentum | $226.28 | $226.28 | $226.27 cannot size all five targets |
| Reversal | $400.06 | $400.06 | $400.05 cannot size all five targets |
| Low volatility | $142.91 | $142.91 | $142.90 cannot size all five targets |

The additional joint four-sleeve margin scenario needs $2,000.18. Per-sleeve
budgets, exact fractional orders and all final reservations are retained in the
report. These are **snapshot-specific minima** under the declared scope,
state, roster, limits and costs; they are not interval-wide guarantees or
operational admission balances. Rerun when any of those inputs change.

Private source captures, membership review, exact inputs and reports are under
`/home/onnwee/.local/state/mantik-epic51-56-20261116/`. Replay from the repository:

```sh
input=/home/onnwee/.local/state/mantik-epic51-56-20261116/capital-final-input.json
mise exec -- go run ./scripts/capitalcheck -input "$input" \
  -sha256 "$(sha256sum "$input" | cut -d ' ' -f 1)"
```

Input is bounded to 32 MiB and 64 scoped assets. It is an explicitly reviewed
normalization manifest; source hashes establish identity, not independent
provider authenticity. Original captures must remain available for inspection.
Unit tests use separately identified synthetic snapshots and fixed cent/fee,
fractional-order, missing-history, source-clock and margin-fallback boundaries.
