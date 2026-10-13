# Mantik architecture

## Product model

Mantik runs autonomous paper-trading research under a human-defined mandate.
The terminal is the primary supervision interface. Conversation may inspect
and direct the system through the same application operations.

Information continuously updates the evidence available to Mantik. Selected
changes open or update a decision case, run an established rule, or enact a
previously authorized action. Research supplies hypotheses and methods when
existing library components cannot adequately handle the situation.

## Responsibilities

Research retrieves literature and prior experiments, formulates testable
hypotheses, composes library methods, and can propose new implementations.
The library should include data transforms, signals, strategies, tests,
allocation policies, execution models, and evidence of their applicability.
A published or correctly implemented method is not automatically profitable.

Decision-making combines deterministic methods with LLM investigation where
interpretation is useful. A decision type declares tools, required evidence,
mandatory evaluations, permitted outcomes, and abstention conditions. Portfolio
allocation owns capital decisions across competing proposals.

Operations monitors authorized conditions, enacts applicable actions, records
outcomes, reconciles state, and handles failures. Protective behavior cannot
depend on a working LLM. Operations reports changed conditions back to the
information stream rather than silently changing the decision's authority.

## Structured evaluation

Evaluation exchanges claims, source references, measurements, uncertainty,
tool results, checks, and candidate actions. It does not feed successive prose
reports between stages. Original narrative sources remain available for
bounded inspection and extraction verification.

Finalize the decision before generating its explanation. The explanation
reads retained evidence; it cannot revise the finalized action. A contradiction
found during explanation opens a review case.

## Contracts

- Observation: source identity, event/receipt times, instrument, content, and
  provenance. The bootstrap supports only a typed fixture quote.
- Trigger: a condition, its behavior (`research`, `decide`, or `act`), owner,
  expiry, and repeated-event policy. The bootstrap records threshold triggers.
- Hypothesis: claim, predicted relationship, refutation conditions, and evidence.
- Strategy version: immutable executable method, required inputs, evaluations,
  and safety policy. The bootstrap version is a fixed synthetic fixture.
- Decision case: the situation being resolved and its lifecycle.
- Evaluation result: versioned check outcome and evidence references.
- Decision: finalized action or abstention, reasons, evaluations, constraints,
  validity period, and conditions for later action or reassessment.
- Execution outcome: what operations actually enacted, with decision identity,
  fill assumptions, and resulting economic effects.

The current Go records are intentionally small. Rich source claims, tool
contracts, multi-instrument identities, and evaluation versions will extend
them through explicit schema changes.

## Trigger scope

Anticipated sources include news, social media, specific watched accounts,
copytrading activity, calendar events, prices, liquidity, thesis changes,
portfolio state, executions, research milestones, data-quality changes,
operational failures/recovery, and human direction.

Underlying mechanisms include event receipt, threshold crossing, state
transition, elapsed time, evidence sufficiency, and an expected event missing.
Several observations may update one case. Define deduplication, reopening,
expiration, and combination rules to avoid repeated workflows.

Social claims, declared trades, observed fills, and observed holdings have
different evidence. Copytrading can be a research input, a decision trigger,
or a bounded strategy. It still needs Mantik's sizing and safety policy.

## Current implementation

`cmd/mantik` assembles signal handling and CLI commands. `internal/cli` offers
`serve`, `demo`, `status`, and `observe`. `internal/service` exposes health,
readiness, status, and fixture ingestion. `internal/workflow` evaluates and
finalizes decisions; `internal/operations` rechecks authority and simulates
fills. `internal/storage` provides memory and PostgreSQL implementations.

A fresh quote at or below 100.00 can open the fixture case if its notional and
protective levels pass. The decision permits 10 units, at most 1,000.00
notional, a 95.00 stop, a 110.00 target, a one-hour holding limit, and quotes
at most 60 seconds old. A fresh quote crossing a protective level or arriving
after the holding deadline closes the position at its observed price.

Stale or out-of-order observations produce abstentions. Malformed observations
are rejected. A closed case does not reopen. Price and money values use integer
cents; other instruments require precision contracts before admission.

Each ingestion atomically stores its observation, receipt, events, and updated
aggregate. The state row lock serializes multiple processes. Duplicate IDs with
identical content return the retained receipt; changed content conflicts.
Status uses a consistent read transaction. Bootstrap migrations run under an
advisory lock and verify their checksum and schema version at startup.

The operations adapter is synchronous and synthetic. External executions will
need durable action delivery, uncertain-outcome handling, and reconciliation;
a database transaction cannot make an external order atomic.

## Next capabilities

- A registered library/component interface with evaluation recipes.
- Independent timers and missing-feed detection for decision obligations.
- Recorded-input replay and research-attempt history, including failures.
- A real feed adapter with provenance and time-aware routing.
- LLM tools and research workers behind explicit application interfaces.
- Multi-case portfolio allocation and paginated/event-stream readbacks.
- Isolated generated-code evaluation and admission.
- TUI supervision and explanations derived from finalized records.

Live trading is outside the product roadmap. A healthy service or successful
fixture creates no real-market qualification evidence.
