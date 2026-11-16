# Wave 9 implementation

One combined PR completes #72, #73 and #74 at the owner's request. Start from
Wave 8 merge `c4c46a9`; preserve the earlier research and paper-ledger contracts.

1. Record immutable backtest declarations and decisions using retained registry
   runs, walk-forward/stress/neighbour results and statistics. List every failed
   check. Model-driven versions take a forward-only incubation path. Add CLI
   inspection and attach decisions for all four base recipes on retained real
   prices, without concealing unavailable universe or availability evidence.
2. Add one target-to-quantity distributor: inverse-volatility initial sleeve
   weights, a fixed incubation pool, retained cash, fractional quantities and
   minimum-order/balance waits. Rebalance against current book equity, including
   deposits, and bind generated orders to their retained plan.
3. Put hard exposure and loss checks in the capped-book reservation path. Retain
   risk observations, kill/reduce-only controls, strategy breakers and hard halts
   in the existing append-only journal. Deposits are flows, not profits. Pending
   orders cannot release capacity before execution; reducing exits remain usable.

Verify focused numerical, bypass, concurrency and restart cases; then run pinned
`make verify`, `make test-race` and disposable `make test-integration`. Additive
gate storage needs migration/replay tests. Keep generated inputs and reports out
of Git. Publish one reviewed PR after local validation, retain hosted CI gates,
and finish tracker reconciliation only after the final head passes and merges.

The older threshold-specific allocation system remains separate. This wave
does not start incubation workers, place venue orders, deploy a service, or
grant forward qualification. Those outcomes have their own roadmap issues.
