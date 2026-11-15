# Wave 7 combined delivery

The owner requested one PR for all remaining Wave 7 issues to avoid six slow
CI runs. #66, #67 and #68 are already merged. Remaining scope: #79, #91,
#96, #97, #98 and #106. Existing CI requirements remain intact.

Source choice and the exact meaning of configured-source replication are
recorded in [the source decision](../research/stock-source-decision.md).

1. Review and pin permissive upstream strategy building blocks and numerical
   conventions. Implement trend, skipped-month momentum, short reversal and
   long-only low volatility. Replicate their configured reference targets and
   next-session trades on identical inputs; record every adaptation explicitly.
2. Extend views with declared point-in-time stock universe membership. Support
   a no-rebalance decision between scheduled strategy updates.
3. Compile bounded typed expression trees into the strategy interface. Refuse
   unknown operators, forward lags, cycles, excessive work and nonfinite inputs.
4. Simulate Kalshi fills from recorded depth with explicit fee policy and
   official binary settlements, in a separate notional book. Expose no Alpaca
   or SPY portfolio integration.
5. Check real retained inputs separately from synthetic stress fixtures. Run
   local verification and focused/race checks; run integration if storage
   changes are needed. Review the final diff before the single push/PR.

No gate decision, live order, strategy promotion or performance qualification
is authorized by this implementation. Runtime observations and qualification
evidence stay outside Git; maintained numerical reference fixtures are tests.
