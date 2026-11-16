# Wave 8 implementation

One combined PR completes #69, #70, #71, #104 and #105, at the owner's request
to reduce separate hosted CI runs. Base: Wave 7 merge `8b58060`.

1. Add a walk-forward runner over the existing daily engine and trial registry.
   Retain exact training/test dates, purge and embargo counts, role and candidate
   in each attempt. Run every declared neighbour, base test and cost stress;
   compare every cost component before admitting a stress model.
2. Add descriptive statistics from retained returns: DSR with the registry search
   total, synchronous CSCV with explicit unavailable reasons, cash/SPY comparison,
   and minimum capital for declared trade units under the shared account rules.
   These diagnostics do not issue the later #72 gate decision.
3. Add an immutable capped-book configuration and append-only PostgreSQL events.
   Reserve against local capital before the existing paper adapter can POST;
   retain pending reservations after ambiguous outcomes, apply scheduled funding
   once, and reconcile only fills bound to the book's own venue order IDs.
4. Add calibrated weather-market decisions, net of captured fees, with a
   quarter-Kelly ceiling and aggregate inventory/pending-order limits. The first
   order is post-only. Displayed depth does not prove a resting maker fill.
5. Extend the retained SPY comparison with engine cash/margin runs and the
   configured deposit schedule, using the manifest-verified primary close basis.
   Attach the real-input report and hashes to #105; retain raw SIP discrepancies.

Verify focused regressions, `mise exec -- make verify`, `make test-race` and
`make test-integration` on new disposable PostgreSQL containers. The new schema
is additive; history is immutable and old records stay readable. A rollback
stops the new caller and retains its configuration/events for reconciliation.
No dependency or container changes are planned. Review the combined diff before
one initial publication; retain all hosted CI gates on the final head.

The real-data probes read retained/public sources. Loopback paper fixtures test
submission and reconciliation without issuing venue orders. Historical results,
reference fixtures and later prospective qualification remain distinct.
