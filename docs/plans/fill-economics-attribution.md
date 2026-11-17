# Per-fill financial attribution (#25)

Complete the financial accounting portion while regular-session and settlement
checks wait. Keep the full acceptance issue open for its remaining source and
running-service evidence.

1. Add an explicit financial ledger version that retains the original modeled
   fill alongside each entry/close effect. Validate gross, fee, quantity and
   debit/proceeds arithmetic. Old ledger versions keep their hashes and do not
   acquire inferred details. New execution comparisons select the new version;
   existing policy ledgers retain their original contract.
2. Project those retained fills into decision attribution and paged allocation
   contributions. Update terminal validation/rendering and aggregate fees and
   entry quantities only when every applicable effect has complete details.
   Preserve explicit unknowns for older records and distinguish entry quantity
   from close quantity.
3. Expose the capped book's already-retained broker fills in its authenticated
   owner readback, with exact fractional gross, fee and cash delta. Reconcile
   opening cash, retained deposits and fills with the actual book cash. Keep
   original activity/order/source identities and economic/application clocks.
4. Check partial fills, closes, fees exceeding proceeds, historical replay,
   unchanged retries, malformed/self-rehashed readbacks, fractional precision,
   durable PostgreSQL reconstruction and terminal display. Exercise retained
   real-source input separately from synthetic contract fixtures where available.
5. Update maintained contracts and roadmap status, run pinned `make verify`,
   affected race tests and disposable `make test-integration`, review and ship
   one PR. No protected paper-session state or scheduled task is changed.

No historical migration or rewritten receipt is needed. Optional retained
fields are omitted from old wire shapes. New behavior is selected by explicit
schemas. This adds readback evidence, not an order, funding, promotion or
qualification grant.
