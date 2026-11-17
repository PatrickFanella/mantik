# Reviewed option recipes

`library.NewOption(library.OptionConfig)` creates a pure recipe for covered
calls, cash-secured puts, put verticals, iron condors or iron butterflies.
`engine.RunOptionBacktest` runs it through the existing
[OptionBook](option-structures.md), including actual simulated admission,
protected opening fills, cash settlement, holding and closing. This is a
callable Go path, separate from equity-weight decisions, service activation
and the [multi-leg paper execution route](alpaca-mleg-paper.md). It has no provider, model or
broker submission authority.

## Policy and identity

The recipe policy is `reviewed-option-recipes-v1`. `DefaultOption(kind)` fixes:

| Parameter | Default | Meaning |
| --- | --- | --- |
| Expiry window | 21–45 calendar days, target 30 | Days from the decision's New York civil date |
| Short delta | 0.30 absolute | Nearest usable delta among eligible short contracts |
| Wing distance | At least $5 | Nearest known protective strike outside the short |
| Opening limits | Close × 1.10 for buys; close × 0.90 for sells | Decision-time limit scenarios, never promised fills |
| Reserve budget | 5% of marked equity | Also bounded by settled cash; hard risk limits still apply |
| Contract ceiling | 100 whole equal-ratio contracts | Covered calls also require 100 imported paid shares per contract |
| Holding period | 5 distinct retained sessions after the last opening fill | Close is attempted on that session and retried if refused |

The constructor validates all parameters. Reserve fraction cannot exceed 10%,
DTE cannot exceed 365, quantity cannot exceed 100 and holding period cannot
exceed 252 sessions. These bounds do not raise any engine limit. All fields,
including otherwise unused wing/delta parameters for particular kinds, enter
`recipe_sha256` and the standard strategy ID. The spec declares indicative
option chains. Returned config, spec, provenance and selection inputs are copies.

Only known standard contracts with usable estimates can compete. Rank expiries
by distance from the target DTE, breaking ties toward earlier expiry. Within an
expiry, break equal scores by symbol. Try the next known expiry if a complete
structure is unavailable. Never mix expiries or infer missing chain members.

| Kind | Selection |
| --- | --- |
| `covered_call` | Sell a call with strike at or above spot, closest to target delta. Imported paid shares remain dedicated collateral. |
| `cash_secured_put` | Sell a put with strike at or below spot, closest to target absolute delta. Reserve at least the full strike obligation and round-trip fees. |
| `vertical` | Sell an eligible put and buy the nearest put at least the wing distance below it. Rank shorts only when a usable wing exists. |
| `iron_condor` | Rank feasible protected put/call pairs by combined absolute delta-target distance, with put then call symbol tie breaks. Put short must be below call short; buy outer wings. |
| `iron_butterfly` | Sell a put and call at the common strike nearest spot, with usable outer wings. Symbol order breaks an equal-distance central-strike tie. |

A delta target ranks available strikes; it is not a promise of a particular
risk exposure. Wings can be asymmetric or wider than the configured minimum.
Limits need not imply a net credit. These recipes do not infer bid/ask spreads,
liquidity, volume participation or a volatility forecast. They do not reproduce
an exchange index, automatically roll at expiry, exercise, assign or sell the
underlying. The caller must supply sufficient unexpired history for entry and
exit. A missing/expired held contract stops the simulation without erasing its
liability.

## Sizing, execution and safety

`Option.Plan` retains its exact chain cut, assumptions, structure and analyzed
risk. Starting from one contract, it sizes whole quantities within its reserve
budget and settled cash; covered calls are additionally limited by paid shares.
No usable contract returns `option_recipe_chain_incomplete`; insufficient
funding or shares returns `option_recipe_no_funded_contract`. Neither implies a
zero-risk trade or complete historical coverage.

The runner owns one fresh local book for one configured asset and recipe. At
each explicit supplied clock, it:

1. Selects a candidate if no lot is active. It rejects margin-only recipes in
   cash books or margin books below $2,000 equity, including when sizing would
   otherwise produce no candidate.
2. Calls `OptionBook.Submit`, reducing the recipe quantity one contract at a
   time until admission or a one-contract refusal. Every attempted quantity,
   risk result, refusal and snapshot is retained. It never relaxes a cap.
3. Calls `Step` once per subsequent supplied session. All protective long legs
   fill before shorts, at most one whole leg per later bar. Buys use high plus
   slippage and sells use low minus slippage, with positive fees. A limit gap,
   unavailable bar, Greek breach or persistent brake prevents the fill.
4. Counts distinct retained session dates only after every opening leg fills.
   Re-reading the same session cannot advance the holding period. Every holding
   cut rechecks current hard risk and Greek limits. A breach records its exact
   reasons and triggers a protective close, retried even if later marks recover.
   The scheduled holding deadline also calls `Close`, which unwinds shorts before
   protection. Failed closes retain the position and reservation. A subsequent
   supplied session may select a new lot.
5. Cancels only untouched orders at the horizon. An open or partially filled
   lot remains outstanding in the report, with `Complete=false`. It does not
   invent a terminal bar or turn a liability into zero.

The shared engine enforces maximum loss plus worst protected partial-fill
loss, independent reservations, settled cash and the 20% cash floor. Its gross
absolute Greek ceilings remain delta 20%, gamma 5%, vega 10% and daily theta 2%
of equity, with 10% per-underlying sensitivity/reserve limits. Its position,
sleeve and gross limits, hard halts, kills and breakers remain active. Recipe
sizing is a candidate budget; a hard-limit refusal can make it smaller or
prevent it entirely. Many small books cannot fund even one standard contract.

Results retain recipe/structure versions, identity, provenance, complete config,
asset, account mode, declared funding/paid shares, fees, slippage, timeline,
selection chain, admission attempts, fills and marked snapshots. Funding is a
caller scenario, never a broker balance. `Complete=true` means no outstanding
option position/order/reservation; it also occurs when all candidates refused
or an untouched order was cancelled. Imported shares are still owned. It is
not a successful-trade, performance, readiness or qualification flag. Source
errors return partial evidence and an error. Reports are in memory; retain them
privately if needed. No order journal, recovery or service health hook is added.

## Retained point-in-time inputs

`engine.RetainedOptionSource` takes an existing `options.Store`, an
`engine.OptionSpotSource` and explicit rate/yield/freshness assumptions.
`OptionChain` calls `options.Read`; `OptionBars` calls `options.ReadBars` under
the same stable asset/underlying binding. Both use the existing as-of store
contract. Memory and PostgreSQL already implement it; there is no schema change.
The spot source must provide an unadjusted same-session close with original
observation/availability clocks. Adjusted stock prices cannot substitute for it.
The adapter rejects a mismatched asset identity. It opens no database or feed.

For example, this callable wrapper runs a declared cash-book scenario against
caller-supplied retained inputs. The capital and paid shares are simulation
inputs, not the owner's account or a recommended allocation:

```go
package research

import (
    "context"
    "time"

    "git.subcult.tv/subculture-collective/mantik/internal/account"
    "git.subcult.tv/subculture-collective/mantik/internal/engine"
    "git.subcult.tv/subculture-collective/mantik/internal/options"
    "git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

func CoveredCallHistory(ctx context.Context, store options.Store,
    spots engine.OptionSpotSource, assetID string, times []time.Time,
) (engine.OptionBacktest, error) {
    recipe, err := library.NewOption(library.DefaultOption(library.CoveredCall))
    if err != nil {
        return engine.OptionBacktest{}, err
    }
    source := engine.RetainedOptionSource{
        Store: store, Spots: spots,
        Assumptions: options.Assumptions{
            Rate: 0.04, Yield: 0.01, MaxAge: 4 * 24 * time.Hour,
        },
    }
    return engine.RunOptionBacktest(ctx, engine.OptionBacktestConfig{
        Recipe: recipe, AssetID: assetID, Times: times,
        Book: engine.OptionConfig{
            Source: source, Account: account.Cash, SettledCash: "20000000",
            Stocks: map[string]int{assetID: 100},
            FeePerContract: 1, SlippageBps: 5,
        },
    })
}
```

Clocks must be increasing and the timeline is bounded at 10,000 entries. Each
entry represents a supplied session opportunity; the runner neither discovers
nor manufactures trading sessions. OptionBook additionally requires the entire
consumed bar to start after the decision and that lot's previous fill. Repeating
an old retained close at a later clock cannot create a new fill.

## Provenance and explicit adaptations

These are **original Mantik reviewed recipes**, not unchanged-source ports.
There is no external executable option-strategy dependency or claim of parity
with a pinned upstream algorithm. Versioned policy tests fix the declared
selection, quantities, limits and identity; the existing pricing adapter has
its separate pinned QuantLib reference tests. Stock recipes retain their own
[pinned bt/ffn replication evidence](stock-library.md).

The economic references recorded by every option recipe are:

- Coval & Shumway (2001), *Expected Option Returns*, Journal of Finance 56(3),
  983–1009, [doi:10.1111/0022-1082.00352](https://doi.org/10.1111/0022-1082.00352).
  It investigates option returns and priced risks, including evidence from
  index options. It does not supply this covered-call or put-write algorithm.
- Bakshi & Kapadia (2003), *Delta-Hedged Gains and the Negative Market
  Volatility Risk Premium*, Review of Financial Studies 16(2), 527–566,
  [doi:10.1093/rfs/hhg002](https://doi.org/10.1093/rfs/hhg002). It studies
  delta-hedged option gains and volatility risk compensation, not these
  unhedged recipes or their sizing and execution rules.

The DTE/delta choices, minimum wings, deterministic tie breaking, bounded whole
quantities, imported stock collateral, reserve budgets, daily holding schedule,
European BSM Greeks and adverse indicative fills are explicit Mantik
adaptations. The literature motivates research; it does not establish expected
profits here. Neither paper's universe, sample, hedge, prices, transaction costs
or empirical results are replicated.

## Verification and acceptance limits

Unit tests exercise all five recipes through actual OptionBook Submit/Step/Close,
including fully retained **synthetic** references, spots and OHLC in Memory.
They cover cash income recipes, paid-share sizing, reserve budgets, engine
quantity reductions, cash and below-$2,000 refusals, protected sequencing,
Greek rechecks, limit gaps, partial-horizon reservations, later corrections,
identity copying and byte-identical reruns. These do not prove real execution.

A separate offline exercise on 2026-11-16 checked the private retained archives
without credentials, new requests or database access. All 64 archived response
body digests matched. The 2026 SPY sample has one underlying close, one option
session (2026-11-13), two contracts at strike 755 and expiry 2026-11-20, acquired
2026-11-15. Defaults correctly report an incomplete chain because the DTE is
outside their window. A separately identified short-DTE covered-call scenario
(minimum 1, target 5 days) admitted one contract in a declared $20m cash scenario
with 100 paid shares, holding $150,354. The next clock still had only the old
bar: Step refused `option_fill_requires_later_bar`, then untouched cancellation
released the hold. Cash was unchanged and there were **zero real fills**.
The retained put is above spot and ineligible for this put-write policy; the
spread recipes lack protective strikes. No missing member was fabricated.

The older archive contains two 2024-02-01 option bars and 55,028 trades, acquired
in 2026. At a 2024 decision clock the retained store returns no references or
bars. At acquisition those contracts are already expired. This cannot support
a naturally point-in-time 2024-onward backtest, nor establish historical chain
membership. That original naturally point-in-time requirement remains unmet.
The owner subsequently approved a separate [retrospective research policy](option-retrospective.md)
for #101: bounded 2024/2025/2026 archived-price scenarios, with original
acquisition clocks retained and historical availability explicitly assumed.
Both income recipes completed simulated cash-book round trips in that study;
it does not turn the older archive into naturally available historical data.
#102's mechanical backtest/reserve/Greek
criteria are exercised with synthetic fixtures; no real-history performance
or qualification is claimed.

Private receipts are under `/tmp/mantik-option-recipes-101-102/`; they are not
committed. Input SHA-256:
`554652de9d91e834e207b8125f3d71cd4645f620688b7240bdfa8a7535c3f4c8`.
The 64-page hash/metadata manifest digest is
`cfe2cf2987a3b61a1d5b6499ae73f83c32c8e377f0e5ee4dce8abf13b9992f2a`.
The original retained chain digest matches the earlier #84 receipt:
`d5647ffc0c9fd0566dd4e6299204e4df5fd394d4275af04b574eac9789eaca1f`.
These hashes bind observed local inputs; they are not provider authenticity or
qualification attestations.
