# Indicative option structures

`engine.OptionBook` is a stateful, offline order simulator for issue #84. It
admits limit orders, reserves collateral, fills retained indicative bars,
maintains cash and filled contract lots, and closes them. It does not submit
orders to Alpaca or join the qualified portfolio. Snapshots carry
`protected-indicative-structures-v1` and `indicative_unqualified`.

## Supported orders

Opening structures contain one to four distinct standard 100-share contracts:

- `long_options`: positive contract quantities only;
- `covered_call`: one short call against already owned, fully paid stock;
- `cash_secured_put`: one short put with full strike cash collateral;
- `vertical`: equal, opposing quantities of one right at different strikes;
- `iron_condor`: long put < short put < short call < long call;
- `iron_butterfly`: long put < short put = short call < long call.

Iron structures require equal quantities on every leg. All legs share one
underlying and expiry. The order's `Multiplier` must explicitly be 100. Chain
references already exclude adjusted deliverables. Invalid quantities, duplicate
contracts, mixed expiries, inconsistent symbols/rights/strikes, unavailable
estimates and invalid Greeks are refused. A zero-valued missing estimate is
not a usable Greek. There are no calendar spreads, naked calls or ratio spreads.

A cash book admits only long options, covered calls and cash-secured puts.
A margin book below its own $2,000 marked equity uses the same permissions.
Stock collateral is imported through `OptionConfig.Stocks`, separately from
settled cash, and must already be fully paid. The simulator never borrows to
buy options or spends unsettled sale credits. Stock remains owned and cannot be
sold through this path. Each covered-call lot claims its shares exclusively,
including while pending. Another order cannot pledge them again.

## Loss and collateral

`options.Analyze` computes expiry payoff at zero and every strike, and checks
the slope beyond the highest strike. A negative terminal slope is unbounded
and refused. Cash flows use each buy's maximum limit and each sell's minimum
limit, with per-contract fees for both entry and exit. Covered-call loss
includes the dedicated stock's current dollar value and its loss to zero.

The reserve is **maximum complete loss plus a bound on worst partial loss**,
rounded upward to cents. Protective long legs must all fill before any short
leg. The partial bound enumerates every permitted filled/unfilled vertex; it
includes the full vertex as a conservative bound on partial contract quantities.
Loss is convex in quantities, so intermediate quantities cannot exceed the
vertices. An arbitrary short-call-first partial fill has unbounded loss; this
engine does not represent it with a finite spread-width reserve.

The simulator holds at least this reserve. Cash-secured puts additionally hold
at least their entire strike obligation plus round-trip fees, without assuming
an unfilled premium will arrive. Filled lots retain the full reserve in
addition to premiums already paid. This intentionally uses more collateral
than the minimum expiry loss. Independently pending orders get no netting,
protection or proceeds credit from one another. The approved hold remains after
entry or cancellation. Covered stock appreciation increases that hold to cover
the added complete and partial loss; falling marks do not release it.

## Fixed Greek limits

Chain Greeks use the [existing per-share conventions](option-chains.md).
For signed contract quantity `q`, multiplier `m = 100` and spot `S`, the
simulator measures these **gross absolute dollar sensitivities**:

| Limit | Dollar measure | Equity ceiling |
| --- | --- | --- |
| Delta | `abs(q*m*delta) * S` | 20% |
| Gamma | `abs(q*m*gamma) * S*S / 2` | 5% |
| Vega | `abs(q*m*vega) * 0.01`, per one volatility percentage point | 10% |
| Theta | `abs(q*m*theta)`, per calendar day | 2% per day |

Gamma uses a full-spot-change stress convention to give its equity fraction
explicit dollar units. These are conservative simulation conventions, not
claims about a venue's risk methodology or calibrated stress probabilities.

For each underlying, each sensitivity **and total reserved dollars** must fit
10% of equity. Owned stock contributes dollar delta. Filled lots and all
remaining independent orders contribute gross exposure; opposing legs do not
cancel it. Two asset IDs cannot map to the same underlying within one book.
Equity is marked from local cash and holdings, reduced by remaining opening
fees and adverse limit-versus-mark gaps. Unreceived short proceeds never fund
another order. Projected free cash also subtracts each independent pending buy
premium at its limit and every pending entry fee, in addition to the retained
hold. The 20% cash floor covers current marked equity before possible execution
losses reduce it.

`risk.CheckOptions` returns every breached cap in a deterministic order:
`risk_option_delta`, `risk_option_gamma`, `risk_option_vega`,
`risk_option_theta`, then `risk_option_underlying:<asset-id>`. Invalid or missing
amounts fail closed. The engine also calls Wave 9's `risk.Check`: gross,
position, sleeve and cash limits and the persistent halt, pause, kill and
strategy breaker apply. For those checks a structure's exposure is its held
reserve; imported stock contributes its marked value. Every admission and
opening fill rechecks the entire book. Ordinary price recovery does not clear
a hard halt.

## Fill and close path

The caller supplies `engine.OptionSource`. Its `OptionChain` method uses the
existing `strategy.ChainSource` signature. Its `OptionBars` method can call
`options.ReadBars(ctx, store, underlying, at)` against the same retained store.
Both Memory and PostgreSQL already implement `options.Store`; no schema change
is required. The source binds stable asset IDs to symbols and supplies the
explicit spot and pricing assumptions required by `options.Read`.

1. `NewOptionBook` takes a source, account type, strategy identity, settled
   local cash, optional fully paid stock, positive fee per contract per side,
   and positive slippage in basis points. It marks the local risk baseline.
2. `Submit(ctx, at, id, assetID, structure)` validates and reserves an opening
   order. Each leg has signed contracts and a positive per-share dollar limit.
   A refusal returns reasons and does not create a pending order.
3. `Step(ctx, laterAt, id)` executes at most one whole leg. Buys pay the daily
   **high plus slippage**; sells receive the **low minus slippage**, each with
   its own fee. Prices round against the trade at 12 decimal places; positive
   fees round upward, and an unrepresentable sell price is refused. Entry
   prices outside the leg limit leave the order pending.
   All protective buys execute before shorts. The entire consumed bar must
   start after both the decision and this lot's prior fill, so each subsequent
   leg needs a later session. No within-bar sequence or liquidity is inferred.
4. `Cancel(id)` releases an untouched order. A partially filled lot retains
   its full reserve and stock collateral until it closes. Cancelling does not
   remove its positions or permit its unfilled shorts to execute.
5. `Close(ctx, laterAt, id)` validates and prices all filled legs before any
   cash changes, unwinds shorts before protection, and cancels the remainder.
   Cancelled, unfilled contracts need no current estimate for snapshots or exits;
   held contracts still need valid prices. No opening Greek/brake permission is
   needed to exit. All closing buy debits
   must fit settled cash after other lots' reservations; same-close sale
   proceeds cannot finance buys. An unfunded close is refused atomically.
6. `Snapshot` returns cash, settled cash, marked equity, reserved dollars,
   pending IDs, filled holdings, the brake and inspectable fee-bearing fills.
   Sale credits settle on the existing US business-day T+1 calendar.

`ReadBars` requires complete positive OHLC, consistent ranges, known
acquisition clocks, the correct session and retained indicative feed labels.
Missing/null prices, future observations and chain/bar disagreements fail
closed. Late corrections do not rewrite recorded fills. Fill records retain
the consumed OHLC and clocks; no source records are written into Git.

## Limits and verification

This is a callable engine order path, separate from the equity-weight
`engine.Run` and its `strategy.Decision`. It owns positions and reservations
within one simulation sleeve; it does not accept or authorize portfolio
allocation, add CLI commands, or persist an order journal. Callers must keep
one owner per book. Source errors are returned, and business refusals carry
reasons. Closed IDs cannot be reused. There is no recovery, expiry settlement,
assignment, exercise, American-option qualification, volume participation or
broker reconciliation here. Expired/missing contracts stop this simulator;
they never become zero liabilities. The separate [multi-leg paper route](alpaca-mleg-paper.md)
for #85 binds `OptionBook.Submit` admission to `alpaca-paper --action mleg-open`
and reconciles closes through `--action mleg-close`. Its synthetic transport
tests do not satisfy the real paper open/close requirement. Qualification and
promotion remain separate work.

The unit tests use synthetic chains and retained in-memory indicative records.
They check worked payoff losses, all supported shapes, partial-fill bounds,
unbounded short-call detection, malformed data, Greek thresholds and gross
aggregation, cash permissions, collateral reuse, fees, adverse fills,
correction visibility, settlement and exits during a kill. They are not a
real-data exercise or evidence of executable pricing or strategy performance.

Focused checks (with the pinned toolchain):

```sh
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go test ./internal/options ./internal/risk ./internal/engine
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go test -race ./internal/options ./internal/risk ./internal/engine
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go vet ./internal/options ./internal/risk ./internal/engine
```

The combined Wave 10 integration runs repository-wide `make verify` and CI.
