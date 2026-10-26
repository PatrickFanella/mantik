# Owner-selected protective counterfactual replay

`GET /v1/paper-evaluation-declarations/{id}/protective-replay` adds protective
transitions to the retained entry study. This authenticated read accepts only a
declaration ID. Query strings and request bodies are rejected. Callers cannot
provide prices, orders, clocks, balances, effects or evaluation results.

The response uses `mantik-owned-evaluation-protective-replay-v1` and method
`first-fill-clock-shared-protective-counterfactual-v1`. It includes the existing
[entry evidence](paper-evaluation-entry-replay.md), original market projections,
and separate base/stress books. It does not change the entry-replay route or
immutable assessment recipes.

## Owner sources and horizon

The existing atomic owner read selects the actual declaration, policy, strategy,
canonical finalized decisions and on-time outcome sources. All source owners
are replayed under one Memory lock or PostgreSQL read-only repeatable-read
snapshot. Missing entry sources, pending declaration deadlines and existing
capacity limits keep the protective books unavailable.

For each variant, the protective path unions the selected historical outcome
market rows. The source cut is the latest selected on-time outcome assessment,
not the request time or a new future deadline. Outcome-specific selection and
temporal labels do not change a quote's source identity. Conflicting retained
representations fail the read. Original projections preserve those labels.

Quotes must retain supported share dimensions, fit the fill method's quantity
range, match the strategy's symbol/feed/evidence class, satisfy its age bound,
and have regular-session evidence at owner availability. Every excluded input
has an explicit reason. Quote commands run at publication/admission availability,
never back at occurrence. The model's own latency and age limits also apply.
Sparse observations remain sparse; this method does not establish continuous
coverage or authenticate an unqualified evidence class.

## Declared method assumptions

Each variant is an alternative study, with no aggregation across variants.
The base model comes from the declared policy; stress uses the declared stress
model. Entry orders retain the existing owner-derived quantity, limit, debit
ceiling, strict post-finalization activation and hypothetical entry expiry.
These are unfunded counterfactual orders: account cash, allocation admission and
reservations are not simulated by this version.

The method processes retained quote events in deterministic availability order.
At the same clock, entry expiries and holding timers precede quote events.
Equal-clock order transitions follow stable order-ID priority.

A position exists only after a modeled entry fill. Its holding deadline is the
first fill's model clock plus the strategy's maximum holding duration. Later
partials do not restart that clock. No fill produces no position or holding
timer. Base and stress each derive their own fill clocks.

For an existing position, a usable quote occurrence strictly after its first
fill can trigger a stop when bid is at or below the strategy stop, or a target
when bid is at or above the strategy target. A holding timeout takes priority
when it has already elapsed at that quote's availability. Older recaptures
cannot trigger a position from a pre-fill occurrence.

The first trigger seals any working entry remainder before that quote can add
more entry quantity. The sealed quantity determines one market sell order,
activated one nanosecond after the trigger. Stop, target and timeout all select
this market-exit rule. The trigger quote cannot fill that exit; a later eligible
occurrence must satisfy the model's placement latency, age and liquidity rules.
A target trigger therefore does not guarantee a target execution price.

Exit orders expire 24 hours after activation, within the existing fill method's
maximum order duration. An order whose activation is beyond the retained cut
is not manufactured into an exit book. No later replacement or retry order is
inferred after an expired exit. Unclosed quantity remains visible.

## Shared liquidity and results

Entry orders share ask-side occurrence capacity. After entry transitions are
sealed, all selected exit orders replay together in one occurrence-aware book,
sharing bid-side capacity and stable ID priority. Repeated captures and favorable
corrections cannot replenish consumed occurrence liquidity. Alternative models
and variants remain separate counterfactuals.

Each model response retains its exact entry book/projection, optional exit
book/projection, source cut and positions. Position records pin the finalized
decision, first fill, holding deadline, trigger clock/reason, quantities, entry
debits and modeled exit proceeds. `closed_net_cents` is present only for a fully
closed modeled position; partial or unclosed exposure has no invented net result.
Entry and exit fees, including proceeds below fees, remain in model arithmetic.
This does not establish funded exit protection or available account cash.

An early cut can precede the first fill's full holding interval. It then leaves
the position open. A trigger without a later eligible quote leaves an unfilled
or partial exit. Neither case becomes a completed protective result merely
because an entry order expired or the declaration deadline elapsed. Old source
cuts are never expanded to make a position close.

The existing per-book command and order limits apply to quote, cancellation,
expiry and cut commands. Capacity exhaustion returns an explicit variant status
with no partial model result. The whole response also retains the evaluation
byte limit. Cancellation, timeout, capacity and error paths do not write owner
state or financial effects.

## Acceptance boundary

These are modeled results under a versioned application method. They do not
complete portfolio economics, benchmark comparison, independent-sample or search
multiplicity checks, or an applicable admission. A future complete economic
assessment must pin its complete method prospectively; this read does not
retroactively qualify an existing declaration or assessment.

The response keeps portfolio economics, admission, execution and qualification
flags false. No account balance, allocation journal, order/fill owner, position
owner, immutable assessment or durable cash effect is written. Natural service
qualification remains a separate predeclared release and observation run.

Verification combines manufactured method fixtures with the existing actual
Memory/PostgreSQL owner read and authenticated HTTP contracts. Fixtures cover
partial sealing, first-fill clocks, base/stress latency, exact-time timeout
priority, shared exit liquidity, fees, unclosed exposure, old occurrences and
capacity refusal. They are not observations of venue behavior.
