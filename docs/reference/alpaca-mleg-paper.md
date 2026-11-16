# Alpaca multi-leg paper lifecycle

Issue #85 adds a manual, durable paper route for two to four standard option
legs. `mantik alpaca-paper --action mleg-open --input` opens a structure;
`--action mleg-close --input` closes the journal-owned fills. Both actions use
the existing required `--journal`, `--credentials-file` and `--account-id`
flags. The endpoint is fixed to Alpaca paper. No model is called.

This is implementation and synthetic transport evidence. No real iron condor
has been opened or closed by this change. The coordinator must retain that
regular-session paper evidence separately before accepting the real-paper AC.
It is not live trading, profitability evidence or forward qualification.

## Opening input and admission

The opening JSON has five fields, matching `alpaca.MLegOpen`:

- `ExecutionPolicy`: exactly `"paper-atomic-net-premium-fee065x3-v2"`
  (`alpaca.PaperAtomicNetV2`). Mandatory for every new lifecycle. This immutable
  paper-only policy binds atomic net premium accounting and a modeled fee
  allowance of $0.65 per requested contract for three rounds (opening, whole
  close and fallback). There are no configurable fee or netting switches.
  Missing and unknown policies cannot submit. Retained legacy/unknown work
  permits GET inspection only: no new whole/fallback POST, cancellation DELETE,
  or automatic closed declaration. An exact retry cannot change the policy;
  adding a policy to retained legacy work conflicts. The earlier v1 contract
  lacks the durable reply-classification protocol and remains inspection only.
- `Order`: `client_order_id`, integer-string strategy `qty`, `type: "limit"`,
  `time_in_force: "day"` or `"gtc"`, signed decimal-string `limit_price`,
  `order_class: "mleg"`, and `legs`. Each leg has `symbol`, positive
  integer-string `ratio_qty`, `side`, and matching `position_intent` of
  `buy_to_open` or `sell_to_open`. Ratios must have GCD one. Parent symbol,
  parent side, fractional/notional quantities and extended hours are absent.
  Positive package limits mean debit; negative limits mean credit. Zero limits
  are refused by this route. Stock limit validation remains positive-only.
  New opening client IDs cannot start with `mleg-`: that namespace is reserved
  for lifecycle controls, custody records and generated close orders. Refusal
  occurs before lifecycle or wire-intent retention and before admission/POST.
  Exact recovery of a retained lifecycle precedes this new-opening restriction;
  existing noncolliding prefixed orders can still reconcile and close.
- `Structure`: the #84 `options.Structure`, with `Kind` and `Legs`. Signed
  `Contracts` must equal wire strategy units times each signed ratio, in the
  same leg order. `Multiplier` is 100. `Limit` is dollars per underlying share.
  The signed package limit times strategy units must not exceed the sum of
  signed contracts times these admitted prices, including for net credits.
  Per-leg prices are model/admission assumptions, not wire-enforced child caps;
  Alpaca receives only a signed package limit.
  Supported kinds are `long_options`, `vertical`, `iron_condor` and
  `iron_butterfly`. Stock/option combinations, covered calls and cash-secured
  single puts are outside this multi-leg route.
- `Chain`: the complete `options.Chain` used for that admission, including spot,
  standard contract identities, source clocks, price observations and Greeks.
  Build it with `options.Read` from retained references and observations, or
  supply an explicitly identified operator-prepared chain. These are indicative
  estimates; the JSON is not a claim of authenticated live quotes. The route
  validates the chain and refuses unavailable Greeks, future evidence and
  evidence older than 24 hours or the declared maximum age. Alpaca must confirm
  options trading level 3 and active, tradable 100-share contracts.
- `Funding`: an `account.CappedState` for a **dedicated** local budget bound to
  the expected paper account. Its opening cash must be `"2000"`, deposit amount
  `"500"` and deposit interval two weeks. Only valid scheduled deposit events
  are accepted. Prior trading, reservations, stock, controls and other event
  kinds are refused. `account.SnapshotCapped` replays the exact events at the
  admission cutoff. This file is an operator-declared local funding record,
  not independent verification of a bank transfer or service authorization.
  Preserve the immutable `FundingMode`: `owner_receipts` requires confirmed
  owner-attested deposits; elapsed slots alone add no cash. Omitted mode and
  `synthetic_schedule` retain legacy simulated funding and supply no real-cash
  or qualification evidence. See [owner receipt rules](owner-book-controls.md#real-deposits).

The callable product entry is `mlegpaper.Open(ctx, trader, input)`. It binds
`PaperTrader.OpenMLeg` to actual `engine.OptionBook.Submit` admission immediately
before the initial POST, after preparatory GETs and durable intent retention.
Use this wrapper for integrations; the adapter's required callback is a trusted
internal boundary, not strategy authority. No simulated fill is treated as a
broker fill. Risk fractions, minimum margin equity, gross/position/sleeve caps,
cash floor, maximum-loss-plus-worst-partial reserve and gross Greek caps all
remain enforced. The admission book still projects gross buy premiums and holds
its conservative reserve; the atomic paper contract does not change
`internal/account` or indicative engine execution/settlement semantics.
The POST clock rechecks contract expiry, spot age and each traded option's
price age against the declared maximum. The original source cutoff and
observation clocks remain retained unchanged.

Each journal owns exactly one lifecycle and one immutable funding snapshot.
Other paper intents or stock abort fences in a new opening journal cause refusal.
The route reserves that budget for the entire lifecycle, including after close;
it does not release or recycle it. Do not allocate the same funding ledger to another journal or
another book. This is a manual dedicated-budget path, not integration into the
shared incubation allocator or a service-side capital authorization protocol.

The initial $2,000 margin book can refuse spreads: pending fees lower projected
equity below the $2,000 margin threshold, invoking cash-account permissions.
Only genuinely recorded scheduled deposits may increase that local funding.
An ample Alpaca paper balance never changes a refusal. Find a structure that
passes the existing caps; do not raise limits or invent deposits to force proof.
For a paper exercise, retain the actual opening clock and declared capital
provenance. Explicit simulated PAPER funding may use `synthetic_schedule`;
`owner_receipts` is the separate mode for actual owner-attested funding. Never
backdate an opening to manufacture elapsed deposit slots or relabel a synthetic
ledger as real funding.

## Intent, recovery and audit custody

The adapter retains the exact lifecycle, wire requests, confirmed refusals and
close plan under `paper-intent-*` names. The existing journal process lock,
write-once publication and fsync fence the only POST for each client ID. Exact
retries are GET-only, including after restart, a 404, rollover, or a crash between
retaining the lifecycle and sending the wire intent. Changed requests conflict.
A refused admission consumes that identity; no subsequent retry can submit it.
The dedicated option journal also refuses stock submissions and generic stock
cancellation, so those actions cannot bypass the lifecycle fences.
An ordinary stock intent with client ID `mleg-lifecycle` is distinguished by
its validated stock payload; its retry and cancellation remain available.

Before each v2 option-order POST or client-ID GET, the adapter retains a
`paper-intent-mleg-reply-pending-*` classification marker. A complete successful
reply clears that marker only after a verified observation or persistent
quarantine is durable. Known transport failures, incomplete responses and
non-success HTTP results provide no execution witness and leave ordinary
GET-only recovery available. If the process stops before classification
finishes, the marker survives restart and rollover. Later GETs can retain raw
inspection evidence but cannot restore POST, DELETE or closed-declaration
authority. Interrupted classification requires operator inspection; this route
provides no automatic fence override, even if the unprocessed reply was valid.

All raw HTTP responses use the existing `paper-response-*` records: method,
path, status, receipt clock, SHA-256, completeness flag and original bytes.
Parent and child venue IDs, symbols, sides, quantities, cumulative filled
quantities and prices must match the retained request. Child ordering may vary;
it is normalized to opening leg order. Duplicate IDs, missing children,
fractional/overfilled quantities and missing positive child fill prices fail
closed. For parent filled units `f`, every child must have exactly `f × ratio`
filled contracts. With nonzero fills, exact decimal arithmetic must establish
`parentAverage × f = Σ(sideSign × childAverage × childFilledQuantity)` and
`parentAverage <= transmittedSignedLimit`. Simple fallback buys must execute at
or below their buy limit; sales at or above their sell limit. The decimal grammar
accepts up to nine fractional digits; no rounding tolerance or inferred reporting
precision is invented. Unreconcilable rounded or missing precision fails closed.
Parent status alone never establishes a fill.

The first observed parent/child IDs and cumulative fills bind subsequent
responses. Changed IDs, quantity regressions, changed averages without new fills,
terminal fill changes, terminal-to-pending regression, malformed custody, net
disagreement and limit
breaches create a persistent quarantine. Later convenient GET replies cannot
restore POST, DELETE or closed-declaration authority, even after restart or
rollover. Raw GET inspection remains available. Transport uncertainty, 404 and
5xx remain ordinary GET-only recovery, not contradictions by themselves.
Normalized observations use retained `paper-intent-mleg-observation-*` records;
unchanged responses add none. At 1,024 observations, new POST and DELETE
operations are refused before transmission. Unchanged GET inspection remains
available; a changed reply that cannot fit stops with quarantine rather than
dropping custody history. Classification markers are removed after ordinary
completion, so unchanged inspection does not accumulate marker records.

Use the existing `fills` action with an explicit lower time bound to retain
original activity records and venue fee fields. Order cumulative fill quantities
control close sizing; this route does not assert complete fee reconciliation or
ledger equity from those order snapshots. Archive the journal and retain its
archive SHA-256 before handing evidence off. Rollover exports raw responses and
keeps every lifecycle, request, refusal, observation, pending classification,
quarantine and close-plan
fence in the active journal. No migration or development/production database is required.

## Whole close and fallback

Closing JSON has `OpenClientOrderID`, signed package `LimitPrice`, and positive
string `LegLimits` in opening leg order. The whole-close and each fallback client
ID are derived from the opening identity. There is one immutable close plan;
limits cannot be changed under a fresh identity to bypass an uncertain order.
The CLI emits `MLegCloseResult` even when reconciliation returns an error, so
operators can inspect known orders and remaining quantities.

1. Recover the opener, cancel if pending, and GET until both parent and children
   are terminal. No polling is hidden: a pending result requires repeating the
   exact command. Only proportional, actually filled opening quantities can
   enter a whole close. An opening with no fills needs no close order.
2. Check the opening and whole-close package allowances against dedicated cash
   **before POST**. A package reserves `100 × requestedUnits × max(signedLimit, 0)`
   plus the independent retained fee pool. A terminal package consumes
   `100 × filledUnits × max(signedLimit, 0)`: price improvements are not reclaimed.
   Unknown outstanding units remain reserved and cannot authorize fallback.
   Opening package credits never grow available cash. The whole close also
   retains the conservative requirement that all proposed separate fallback buy
   limits fit. Other orders' reserves and unowned stock never fund this lifecycle.
3. Submit the reversed mleg with explicit per-leg closing intents. Before any
   new close POST, check exact owned option inventory and overlapping pending
   orders. Other symbols in the account are never adjusted. A full 500-order
   pending-order page is ambiguous and blocks execution.
4. If the whole close remains pending, request cancellation and GET its terminal
   parent and child fills before fallback. Cancellation acknowledgement alone
   is insufficient. A terminal rejection, expiry or cancellation permits
   fallback. A retained HTTP 422 with numeric code 42210000 plus a subsequent
   client-ID GET 404 also establishes a confirmed API validation refusal.
   Mutable message text, timeout, 5xx, malformed/mismatched replies and other
   API codes never establish refusal.
5. Subtract each whole-close child fill from the owned opening quantities.
   Recover any earlier fallback requests before comparing live inventory.
   Before every new short buyback, deduct the opening package LIMIT consumption,
   whole-close package LIMIT consumption, prior simple fallback BUY LIMIT
   consumption for filled contracts, and the entire fixed fee pool from original
   dedicated cash. The next buy must fit at its transmitted positive limit.
   Never add signed lifecycle nets together before taking their positive part:
   a later credit cannot erase an earlier debit. Neither an opening/whole-close
   credit nor a fallback sale funds another order. An insufficient balance stops
   further buys and retains the remaining protection, including after restart.
   Buy all remaining shorts closed, confirming their exact filled quantities
   and terminal status. Only when **every** short liability is zero may the
   route sell remaining long protection. A failed, partial or ambiguous short
   close stops progress with every long untouched.
6. Reconcile all scoped positions to zero before reporting `closed`. Exact
   retries inspect the existing orders and never issue replacement POSTs.

A terminal partially filled or rejected fallback leg cannot be replaced through
this route. The remaining protection stays held and the same command reports
pending; operator reconciliation is required. Changed inventory, another pending
order on these symbols, assignment/exercise, a non-proportional opening fill,
unknown venue state, quarantine or insufficient funding also stops automatic
progress.
Preserve the journal and original IDs when resolving those conditions.

## Atomic premium and modeled-fee limits

Netting is permitted only **within one atomic mleg package** under the explicit
policy. A coherent $3,000 buy/$3,002 sale opening package consumes no positive
premium allowance; a $3,000 buyback/$2,998 sale whole close at a $0.02 unit limit
consumes $2 per filled strategy unit. Both retain the independent fee pool and
all original admission restrictions. Those sales create no spendable credit for
separate orders, and closing never releases or recycles the lifecycle budget.

The $0.65 allowance is a **modeled fee allowance for this paper contract**, not a
universal live-fee guarantee. Live regulatory fees can depend on gross sale
value, which a net package price does not bound. Alpaca paper simulation omits
some actual fees and execution effects. Exact reporting precision, real fills,
actual fee custody and live cash guarantees remain unqualified. A limit can
leave a short buyback unfilled; safe fallback preserves protection and does not
guarantee liquidation. See [paper simulation limits](https://docs.alpaca.markets/us/docs/paper-trading)
and [brokerage fee schedule](https://files.alpaca.markets/disclosures/BrokFeeSched.pdf).

## Coordinator's real-paper preflight

Before the real regular-session test, verify the combined source head and its
checks; inspect the expected paper account, options level, current permissions,
standard contracts, session and current prices. Prepare an attributable chain,
a genuinely funded dedicated capped ledger and immutable opening/close inputs.
Select and retain the explicit execution policy. Check actual OptionBook
admission before attempting execution. An initial $2,000 spread refusal is a
blocker, not permission to bypass the cap. Reserve the whole-close package
allowance, all separate short-buyback limits and modeled fees without spending
credits from other orders.

Retain the exact open request, parent/child venue IDs, raw fills, whole-close
request and result, any cancellation/partial/fallback sequence, fee evidence and
final scoped inventory. Preserve complete account snapshots separately when
other books are present. The implementation tests use only fake transports;
account permissions, actual contract availability, fillability, regular-session
execution and real fee custody remain coordinator checks.

## Callable commands and offline checks

`mantik alpaca-paper --help` lists `mleg-open` and `mleg-close`. The opening
action calls `mlegpaper.Open`; the closing action calls `PaperTrader.CloseMLeg`.
Supply the exact opening or closing JSON described above through `--input` and
retain the same journal and inputs for recovery. Neither action belongs to
the shared `incubation` allocator or the compiled qualification collector.

The focused checks use fake transports and synthetic evidence only:

```sh
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go test -count=1 ./internal/alpaca ./internal/mlegpaper ./internal/options ./internal/engine ./internal/account ./internal/risk ./internal/cli
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go test -race -count=1 ./internal/alpaca ./internal/mlegpaper ./internal/options ./internal/engine ./internal/account ./internal/risk ./internal/cli
```

Provider contracts rechecked November 16, 2026:
[create order](https://docs.alpaca.markets/us/reference/postorder),
[level 3 multi-leg trading](https://docs.alpaca.markets/us/docs/options-level-3-trading),
and [options restrictions](https://docs.alpaca.markets/us/docs/options-trading).
