# Calibrated maker-first weather decisions

`NotionalBook.PlaceWeatherMaker` reads retained public inputs and places a
post-only simulated limit intent in the separate [Kalshi book](kalshi-simulation.md).
It has no venue order client and remains excluded from equity/SPY results.

The exact admitted prediction must be known before close and fresh at decision
time. The recipe validates its payload, evidence hashes and clocks. It also
validates every historical forecast and official settlement used by calibration,
then evaluates one frozen history snapshot for the prediction's model version.
No occupied calibration bucket means no order, with an explicit reason.

The default policy requires 20 settled markets in the raw prediction's tenth-wide
bucket. A Beta(1,1)-smoothed empirical rate, `(wins+1)/(count+2)`, is the traded
probability. This is a reviewed calibration recipe, not a qualification claim
for the underlying forecast model. Earliest predictions per market prevent
polling from overweighting a result. Only outcomes known at the decision enter.
The intent binds the calibration snapshot, prediction, market, book and full
series/event fee-source hashes.

YES and NO are evaluated after a conservative captured taker-fee bound, even
though the first quote is maker-only. The default requires more than two cents
of net edge. It rejects missing depth, crossed/wide spreads, stale evidence,
prices outside $0.10–$0.90 and longshot probabilities outside 0.10–0.90. It
improves the best bid by one cent when possible, otherwise joins a valid bid,
and always stays below the complementary ask.

The paid-cost quarter-Kelly ceiling is `(p-c)/(1-c)/4`, where `c` includes the
conservative fee bound. Whole contracts round down. Aggregate pending reserves
and worst-case $1-per-contract held inventory reduce the permitted stake across
the entire notional book. The default cash-based risk cap is 5%; policy cannot
raise it above 20% or raise the Kelly multiplier. Taker orders cannot spend
cash reserved by maker intents.

`ExpireMakers` cancels elapsed intents, and official settlement cancels that
market's outstanding intents. Displayed depth cannot establish resting queue
position or maker executions. Accordingly these intents stay pending until
expiry/settlement and generate no fabricated fills. The existing depth executor
continues to model taker orders separately; this version supplies no automatic
taker fallback. Simulation version is `kalshi-notional-depth-fees-v3`.

The separate [paper settlement check](kalshi-paper-check.md) retains a public
pre-close forecast and an explicitly operator-selected one-contract diagnostic
taker trade, then joins official settlement to calibration. It records maker
abstention independently and does not turn a diagnostic into a calibrated
strategy fill or relax this policy's evidence threshold.
