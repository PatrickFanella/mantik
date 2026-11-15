# Wave 6 delivery

The owner requested one combined PR for #76 and #81 to avoid duplicate slow
CI runs. #65 is already merged. This overrides the individual issues' default
one-PR delivery units without adding their neighbouring strategy/execution work.

1. Add the selected MIT go-talib revision through a pure indicator adapter.
   Retain adjusted SPY inputs, their source hash, reference-generation script
   and TA-Lib reference outputs. Check every available output and warm-up index.
2. Read an underlying's retained option observations as of a decision clock.
   Build indicative chains from known contract references and prices, with
   explicit spot/rate/yield inputs and European Black-Scholes IV and Greeks.
   Test pricing against a reference pricer and reject future observations.
3. Expose chains only for strategies declaring the new data kind. Return
   copies and reject a source that returns another clock or future data.
4. Run `mise exec -- make verify`, `make test-race`, disposable
   `make test-integration` for the storage read, and `make compose-build` for
   the dependency change. Review, push one PR and check its exact hosted head.

No migration is needed. Existing stock-only specs and Build callers remain
compatible. Options orders, portfolio Greek limits and strategy ports remain
with their later roadmap issues. Historical acquisition clocks are never
backdated; a reference acquired today cannot populate yesterday's chain.
