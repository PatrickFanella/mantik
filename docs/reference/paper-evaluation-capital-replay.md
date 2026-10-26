# Finite-capital counterfactual replay

`GET /v1/paper-evaluation-declarations/{id}/capital-replay` applies a finite study
budget to the owner-selected entry and protective path. The authenticated read
accepts only a declaration ID. Query strings and request bodies are rejected;
callers cannot provide balances, snapshots, admissions, orders or effects.

The response uses `mantik-owned-evaluation-capital-replay-v1` and method
`opening-capital-no-reinvestment-counterfactual-v1`. The registered owner account,
policy, strategies, finalized decisions, declared models and historical source
cuts come from the existing atomic source read. Source hashes and original
market projections remain attached through the entry-evidence envelope.

This is a finite-capital study, not a reservation writer or an operational
allocation. It does not create an admission assertion to make the internal
allocation planner accept a candidate. The actual capital owner remains
separate and retains its deferral-only contract.

## Budget and selection

Each native strategy variant is an alternative study with its own copy of the
same registered opening cash. Variant results are not added together. Base and
stress use their respective declared models and source-derived entry costs.
A model mismatch fails the read.

Candidates are processed by original finalization time, then stable decision ID.
Each candidate's required funding includes its whole-share entry debit ceiling
and the model's worst-case exit-fee shortfall. This protects the study budget
when exit fees exceed sale proceeds, including one-share partial fills.

Selection checks the owner's account capital limit, unused opening cash,
strategy budget, symbol budget and declared correlation-group budgets. Group
limits are declared exposure buckets, not estimated correlations. The full
funding amount consumes these cumulative budgets. A candidate that exceeds a
limit has explicit reasons and is not selected for the counterfactual.

No released or earned cash is reused within this study window. This conservative
method makes selection causal without knowing future exits. It also means a
later candidate may remain unselected even if an earlier modeled order never
fills or a position closes. This is a declared study rule, not an assertion
about the eventual ongoing allocation policy or a completed reconsideration
mechanism.

Only selected candidates enter a fresh protective replay. Unfunded candidates
cannot consume displayed liquidity before funded orders are evaluated. The
existing source-availability, session, share-unit, age, latency, partial-fill,
stop/target/timeout and shared occurrence-liquidity rules still apply. Pending
sources, capacity limits and unclosed exposure retain their explicit meanings.

## Cash and cost-basis reconciliation

The model reports cumulative funding and unused opening budget separately from
ending modeled cash. A budget allocation is not an actual entry debit. Actual
counterfactual fill debits and exit proceeds come from replayed book outcomes.

For partially closed positions, closed cost basis uses the same cumulative
whole-share allocation as the internal ledger method: floor the closed fraction,
leaving the final share the remaining rounding cents. The model checks:

```text
ending cash = opening cash - entry debits + exit net proceeds
ending cash + remaining cost basis = opening cash + realized net
```

Funding must fit opening cash, entry debits must fit cumulative funding, cash
cannot become negative, and cash/realized amounts must remain within the owner
projection's representable limits. A failed identity or range check returns no
model result. `conservation_verified` describes this arithmetic; it does not
attest a durable ledger posting or an account mutation.

Remaining cost basis is not market value. Marked equity and drawdown stay
unavailable. `closed_net_cents` and net excess over cash are present only when
all modeled entered quantity has closed. Partial or unclosed exposure has no
invented complete return. A no-transaction study has zero nominal net; that is
not an independent sample or a successful economic evaluation.

The cash comparison uses `nominal-usd-cash-no-interest-v1`: unchanged nominal
USD cash with no interest. The required passive-same-symbol benchmark remains
unavailable. The read cannot complete benchmark comparison, drawdown,
independence, search multiplicity or an applicable admission from the cash
comparison alone. Existing immutable evaluation assessments are unchanged.

## Authority and verification

The response keeps portfolio evaluation completion, admission, execution and
qualification flags false. It writes no account, allocation journal, reservation,
order, position, assessment or financial effect. Replay/ongoing execution
ownership, positive admission, retirement and interruption reconciliation still
require their own implementations and evidence.

Verification uses manufactured funding and fill fixtures plus the existing
actual Memory/PostgreSQL owner read and authenticated HTTP contracts. Scenarios
cover candidate ordering, account/group limits, exit-fee funding, rejected
candidate liquidity, partial cost basis, closed and unresolved exposure,
negative sale proceeds, conservation and model mismatch. These fixtures do not
qualify provider behavior, a strategy or a service release.
