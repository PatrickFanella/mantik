# Daily strategy oversight

Policy `daily-sleeve-review-v1` evaluates capped-book strategies without a model
or broker call. Authenticated service operations import the frozen comparison
and daily observations, then append a recomputable review event to the existing
capped-book journal. No schema migration or qualification credit is involved.
The service does not schedule reviews or fetch observations; the operator's
collector calls it once per observed session. Inputs and original source bytes
stay outside Git.

## Service contract

All four routes require the configured service bearer credential. They refuse
writes when authentication is unconfigured, including development mode. These
are operator operations, separate from the strategy target API:

| Method and path | Body and result |
| --- | --- |
| `POST /v1/capped-books/{book}/review-declaration` | `account.ReviewDeclaration`; freezes one comparison per retained sleeve. Exact retries preserve the declaration clock. |
| `POST /v1/capped-books/{book}/reviews/{id}` | `account.ReviewInput`; computes and retains `account.StrategyReview`. The caller cannot supply an action or budget. |
| `GET /v1/capped-books/{book}/reviews` | Returns the retained declarations and reviews, including inactive research requests and evidence. |
| `POST /v1/capped-books/{book}/owner-controls/{id}` | An object with `Control`, containing `Action` and `StrategyID`. Supports `pause_strategy`, `resume_strategy`, `retire_strategy`. |

The path variables identify the existing book and an immutable command ID; they
are not credentials. Bodies use Go JSON field names, are bounded to 128 KiB,
reject unknown fields and trailing values, and accept no query parameters. A new
review's `At` must be within the preceding minute of the server clock. Exact
retries can recover an old result; changed retries and second IDs for the same
New York review date conflict. A concurrent book write requires a new snapshot;
the service retries at most four times. Events retain original observation and
receipt clocks, the policy, declaration hash, journal hash and version, every
computed metric, and the action's triggering reasons.

A declaration binds `ID`, `SleeveID`, canonical `StrategyID`, retained `GateID`,
`Policy`, `Benchmark` (`cash` or `spy`), and a positive `DrawdownBudget` no larger
than 0.15. This is a strategy-specific budget, not daily volatility `Risk`.
`Backtest` contains dated decimal net returns; `BacktestEnd` precedes the
review declaration and cannot follow the gate decision. `BacktestSourceSHA256`
must match the gate's retained data identity at the service boundary. This is
an operator-reviewed historical import, not independent replication of registry
results. A hash does not attest that supplied returns are true or complete.
Absent history requires `BacktestUnavailable`; forward-only/model versions
cannot import historical performance. Twenty historical samples are required
for growth.

A review supplies `DeclarationID`, `At`, `Mark` and an optional `Benchmark`.
`Mark` binds `Prices`, `SourceSHA256`, `EvidenceClass`, `ObservedAt` and
`ReceivedAt`. It must be prospective, received by `At` and no older than one
minute, with a positive mark for every owned holding. `Benchmark` binds decimal
`Return`, its source hash and evidence class, `StartAt`, `EndAt` and
`ReceivedAt`. Its interval must exactly span the preceding review and the
current review. Both evidence classes must be
`prospective_market_unqualified`. No cash zero, SPY return or missing daily
sample is invented. Intervals outside 20–96 hours are unavailable; this permits
weekends while rejecting compressed or long missing intervals.

## Attribution and policy

Attribution uses only retained orders owned by the sleeve and their actual
filled quantities, fees and marks. It subtracts the existing worse-of-model
penalty without changing venue accounting. Other sleeves, opening cash and
external deposits do not become strategy profits. Forward return is the change
in cumulative attributed net P&L divided by the preceding review's retained
sleeve budget. It is return on prior budget, **not strategy TWR**. Drawdown is
the current loss from the peak of the cumulative sum of these normalized
increments; `MaximumDrawdown` retains the largest historical loss for inspection. The first observation is only a baseline. Missing attribution
breaks the comparison window rather than supplying a zero.

The following rules run in order. Reductions can use observed losses before the
full growth window exists; missing comparison evidence can never grow risk.

| Trigger | Action and prospective budget scale |
| --- | --- |
| Already retired | Retain terminal retirement, scale zero. |
| Drawdown at least twice the declared sleeve budget | Retire, zero. |
| Drawdown at least 1.5 times budget | Demote to review quarantine, at most 0.25. |
| Drawdown at least budget | Cut, at most 0.5. |
| Benchmark or attribution unavailable | Investigate; retain scale. |
| Fewer than 20 consecutive complete intervals or five owned fill observations | Hold; retain scale. Partial fills count as execution observations, not independent decisions. |
| Frozen history unavailable or shorter than 20 samples | Investigate; retain scale. |
| Cumulative adverse venue/model cash gap at least 10 bps of filled notional | Investigate; retain scale. |
| Negative mean benchmark excess and forward mean below half the frozen backtest mean | Demote to review quarantine, at most 0.25. |
| Mean benchmark lag at least 10 bps | Cut, at most 0.5. |
| Last ten intervals' mean excess trails first ten by at least 10 bps | Investigate edge decay; retain scale. |
| Owner breaker, kill, pause, reduce-only or hard halt | Hold; retain scale. |
| Mean excess at least 5 bps, nonnegative backtest mean, and forward mean at least half backtest mean | Grow scale by 0.25, capped at 1. |
| No threshold crossed | Hold. |

The fixed thresholds are a conservative operational policy, not a statistical
qualification or a profitability claim. Grow restores previously cut capacity;
its maximum is the existing admitted-stage budget. Quarantine demotion leaves
the immutable admitted stage and ownership intact and reduces its capacity.
The [prospective assessor](forward-assessments.md) implements automatic paper
qualification. A later overseer demotion invalidates that grant and gives its
subsequent allocations a zero budget until a separate qualification window.
`ResearchRequested` records an inactive request to inspect the cited evidence.
It does not invoke the research desk, activate a model, or grant sizing,
execution or owner-control authority.

## Next rebalance and owner precedence

A review changes sizing only for a rebalance whose decision clock is strictly
later than the review. Incubation uses the retained plan's `DecidedAt`; direct
rebalances use `At`. A plan decided earlier keeps its exact distribution and
orders even if review occurs before sizing completes. Historical plans,
requests, weights and orders are never rewritten. The journal's version fence
serializes controls, reviews and rebalances; restart reconstructs the same
transition from retained events.

Hold, investigate and reduction events also retain an absolute dollar budget
ceiling. Deposits, rising equity or pool redistribution cannot expand risk while
comparison evidence is missing or insufficient; only a complete grow review can
release that ceiling. Each later non-grow review preserves or tightens the
previous ceiling, even when an older decision sizes later with a larger budget.

The distributor applies the scale and ceiling after the fixed pool and sleeve caps. Freed
capacity stays cash: 5% aggregate incubation, 1% per model version, the 65%
qualified pool, 20% sleeve cap and independent hard risk checks still apply.
Retirement produces explicit flat targets on a later runner decision and does
not evaluate the retired strategy. Pending or partially filled orders still
own their exposure and block replacement orders until reconciled. Active
sleeves cannot be omitted or reassigned. Existing gate freshness, forward
eligibility, holding expiries and the retained-owner flat-exit exception remain
in force.

For sleeves with a retained review declaration, owner pauses preserve held
quantities for non-flat targets on subsequent sizing and block new risk at
submission. The declaration is the compatibility boundary: older journals
without it keep their original reduction targets and sizing hashes. Explicit protective flat targets, including
forward holding-expiry exits, still execute. Owner retirement is a separate permanent risk state: resume,
unkill and loss-baseline reset cannot clear it. Retirement committed after a
review or after an order was retained still blocks new risk on the existing
submission check, repeated after adapter preparation at the actual pre-POST
callback. Protective reductions and reconciliation remain possible.
Review never clears an owner pause, kill, retirement, reduce-only or hard halt.
An already pending opening order is not automatically cancelled by review;
reconciliation must resolve it before retirement can flatten the final holding.

The [owner daily brief](daily-brief.md) reports retained actions and budgets separately. Its book TWR requires exact cash-flow-boundary valuations; review returns are never relabeled as TWR.
