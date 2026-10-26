# Predeclared owner entry replay

`GET /v1/paper-evaluation-declarations/{id}/entry-replay` derives bounded entry
counterfactuals from a retained evaluation declaration. The authenticated read
accepts only the declaration ID. Query strings and request bodies are rejected;
callers cannot supply a model, order, quote, balance, admission or result.

The response uses `mantik-owned-evaluation-entry-replay-v1` and method
`predeclared-shared-entry-counterfactual-v1`. This is an entry-only research
calculation. It does not issue orders, reserve capital, mutate an account or
retain an assessment. Portfolio economics, admission, execution and
qualification remain unavailable.

## Atomic owner selection

Memory holds its aggregate lock; PostgreSQL uses a read-only repeatable-read
transaction. The read replays actual strategy, policy, finalized decision,
outcome, declaration and feed owners before selecting sources. It samples the
service clock once and rejects a clock behind retained collector activity.
The response pins the current evaluation-evidence hash and declared policy hash.

Only canonical `selected_research_evidence` decisions enter replay. Abstentions,
duplicate strategy occurrences, synthetic samples, decisions outside the plan
window and other excluded evidence remain visible in the evidence envelope.
For each selected decision, the latest outcome issued on or before the declared
outcome deadline supplies its historical source cut. Equal-clock ties select the
lexicographically smallest outcome ID. Later outcomes cannot expand this cut.
Missing outcomes remain `outcome_sources_unavailable`. Books remain pending
until the declared outcome deadline has elapsed. A selected outcome cut before
the hypothetical entry expiry remains `outcome_cut_before_entry_expiry`; a
future expiry is not projected from an early source cut.

The base model comes from the declaration's actual pinned policy. The stress
model comes from its predeclared plan. Entry quantity, limit and worst-case debit
are derived with the existing owner cost method from that policy, finalized
proposal and strategy. Missing cost bounds remain explicit. No caller-supplied
execution budget is accepted.

## Counterfactual orders and quotes

Each strategy variant is a separate alternative study. Its selected decisions
share one occurrence-aware fill book for each declared model. Variants are not
summed into portfolio results. Each book uses one Alpaca/provider feed and the
strategy's symbol and evidence class. Orders have stable decision IDs, are sorted
by ID for the fill method's declared priority, and use whole-share buy limits.

The hypothetical order activates one nanosecond after decision finalization and
expires at finalization plus the strategy's maximum holding duration. This
conservative application rule excludes the finalization occurrence itself; the
method's declared latency then applies from activation. This timing is a research
assumption, not evidence of actual order placement or broker latency.

Quote dimensions come from the existing outcome market inspector, which replays
original capture, publication and input-admission custody. Supported quote sizes
use the [pinned REST share contract](alpaca-quote-share-units.md). Quotes must
also fit the fill method's narrower quantity bounds and the strategy's age and
regular-session requirements. Missing originals, unusable dimensions, unknown
sessions, late availability and unsupported values have explicit exclusions.
The response retains each decision's complete market-evidence projection,
including source hashes and separate occurrence, receipt, publication and
admission clocks.

The fill method receives the first owner-usable availability time as its
`ReceivedAt`, rather than the earlier transport receipt. Commands run at that
availability time; they are never moved back to quote occurrence. The original
clocks remain in market evidence. Repeated inputs are deduplicated, conflicting
source representations fail the read, and corrections at the same symbol/time
share conservative occurrence capacity. Neither a new receipt nor a favorable
correction replenishes consumed liquidity.

The method closes every order with a terminal expiry command. More than 32
selected orders or more than 127 usable quote commands produces an explicit
capacity status instead of truncating a supposedly complete replay. The whole
response is bounded by the existing evaluation-evidence byte limit.

## Limits and verification

A modeled entry fill is a counterfactual result under declared assumptions. It
is not a financial effect or proof of provider authenticity or available venue
liquidity. Sparse retained observations do not establish continuous coverage.
This version does not replay protective exits, mark positions, calculate net
returns or drawdown, compare benchmarks, establish independent samples, complete
parameter sensitivity, or authorize admission. Existing immutable assessments
and their recipes remain unchanged.

Verification combines manufactured replay fixtures with actual Memory and
disposable PostgreSQL owner-selection contracts and authenticated HTTP reads.
Fixtures demonstrate arithmetic and refusal behavior; they do not qualify a
strategy or service run.
