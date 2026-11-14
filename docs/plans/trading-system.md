# Mantik trading system design

Status: approved by the owner on 2026-11-13. This document
replaces the direction of the
[2026-11-13 next steps](../roadmap.md#next-steps-2026-11-13). The
[workflow review](https://claude.ai/artifact/TSXsuuFA5WPgSBAQMb3dJ1) has the
diagrams, the verdict on every Augr workflow and the strategy catalogue this
design draws on.

## Intent

Mantik should trade a portfolio of strategies by itself. Code makes and
enforces every money decision. A model researches, proposes and tests
strategies with tools, and explains results. 1.0 ends with paper trading on
Alpaca. 2.0 is live trading, started only after 1.0 proves profitable. The
owner wants an autonomous real-money portfolio within a year. It should start
small and grow as the owner adds cash.

Mantik replaces Augr, which "got too messy". Augr had most of the pieces but
never joined them into one loop:

- six separate idea sources;
- two sizing authorities and two execution stacks;
- a supervisor that nothing called;
- an allocator that recorded decisions without trading by default;
- model output that set position size and stops.

Its August 2026 audit found 171 "active" strategies, 99.3% HOLD signals, 67%
failed runs and no defensible after-cost edge. Augr then built 40 schemas
against synthetic fixtures that the runtime never used.

Mantik risks the same mistake. At schema 115, its evaluation, allocation and
execution owners are built around a single price-threshold rule, and its
evidence is still synthetic. This design changes direction:

- general strategies on real data first;
- contract depth only where a running loop needs it.

## Owner decisions

These were made by the owner on 2026-11-13:

- **Free data only** to start. No paid feed or subscription.
- **Models through the owner's subscriptions.** Model use does not count
  against "free". Mantik uses the owner's Claude and OpenAI subscriptions.
- **The model may trade.** The multi-agent model analyst may trade as a
  strategy if it passes the same gates as any other strategy.
- **Paper account:** Alpaca's $100,000 paper account.
- **Capital:** the capital distributor manages only about $2,000 of it, the
  amount the owner expects to start live with. Simulated deposits raise that
  cap.
- **Deposits:** a $500 cash deposit every other Friday at 06:00 Chicago time.
- **Account types:** Mantik must trade correctly in both a cash account and a
  margin account.
- **"Proves profitable" means the portfolio beats SPY.** Measured as total
  return after all costs, time-weighted, over a forward paper window of at
  least six months.
- **Options and margin are in 1.0**, including multi-leg strategies such as
  iron condors.
- **Reuse existing code.** Use open-source strategy and indicator
  implementations rather than writing everything.
- **Social intake and copy-follow (#14, #15) stay in 1.0.** The owner is
  sourcing the provider accounts.
- **Fees** are part of each fill's own transaction. Each fill and close also
  records its gross amount, fee and share quantity (#25).
- **Model uncertainty:** "probability scoring not applicable" meets the #25
  criterion.

## Goals

1. **A base library of well-studied strategies.** Each implemented from or
   checked against existing open-source code, citing its source paper.
2. **One strategy interface and one engine.** The same strategy code runs in
   backtests, incubation and paper trading.
3. **A research desk.** A model with tools composes new strategies from
   reviewed parts, backtests them and records every attempt. Strategies evolve
   by mutation and recombination.
4. **One lifecycle for every strategy:**
   idea → backtest → incubation → qualified → demoted or retired.
5. **A capital distributor.** It gives each qualified strategy a budget
   (sleeve), absorbs deposits, and measures returns without counting deposits
   as profit.
6. **An overseer.** It re-evaluates health and performance continuously,
   resizes or retires strategies, and writes the owner a daily brief.
7. **Instruments:**
   - stocks (long and short on margin);
   - options, including multi-leg structures of up to four legs;
   - Kalshi prediction markets.
8. **A 1.0 natural paper window** in which the portfolio beats SPY after all
   costs.

## Non-goals for 1.0

- Live trading.
- Paid data.
- Intraday or latency-sensitive strategies. The free real-time feed is
  IEX-only (about 2% of volume), so decisions use daily or older bars and
  quotes.
- Polymarket. The owner is limited to Kalshi in the US.
- Crypto.

## Anti-goals

These make the result a failure even if it works technically:

- A model sets size, places an order, raises a limit or activates a strategy.
- A strategy trades without passing the gates. That includes library
  strategies, model ideas and the analyst.
- Paper results that flatter: zero fees, instant fills at the decision price,
  midpoint fills, unlimited margin.
- A deposit counted as profit.
- Schema or contract work built ahead of a running loop that needs it.
- Many separate switches that must each be set correctly before anything
  trades. Augr sat idle for 30 days behind them.

## Constraints

### Data

All free:

| Need | Source | Limits |
| --- | --- | --- |
| Stock and ETF daily bars, 2016 onward | Alpaca Basic historical API, SIP feed | Request end at least 15 minutes old; 200 calls per minute |
| Real-time stock quotes | Alpaca IEX feed | Incomplete; daily-horizon decisions only |
| Option bars and trades | Alpaca historical options API | Only from February 2024; indicative feed derived from OPRA, not real OPRA quotes; 15-minute block on recent data |
| Corporate actions | Alpaca corporate actions (already used); Polygon references (already used) | — |
| Fundamentals | SEC EDGAR XBRL company facts | Point in time by filing date |
| Prediction markets | Kalshi public market data API | — |

Sources: Alpaca's [market data API](https://docs.alpaca.markets/us/docs/about-market-data-api),
[market data FAQ](https://docs.alpaca.markets/us/docs/market-data-faq) and
[historical option data](https://docs.alpaca.markets/us/docs/historical-option-data),
checked 2026-11-13.

Consequences:

- Option backtests cover under two years of bars with no real quotes. They can
  only rank options strategies roughly. Forward incubation on Alpaca paper is
  their real test.
- Whether Alpaca's history includes delisted stocks is unverified. Until it is
  confirmed, stock backtests are labelled as possibly biased toward
  survivors.

### Execution

Alpaca paper:

- **Stocks:** margin and short selling.
- **Options:** Level 3, with multi-leg orders of up to four legs
  ([Alpaca](https://docs.alpaca.markets/changelog/multi-leg-level-3-options-trading-in-paper)).
  Uncovered short legs need Level 4, so 1.0 options strategies are defined-risk
  only. A multi-leg order is cancelled as a whole. Closing a multi-leg position
  as one order has a reported problem; the builder must test it.
- **Kalshi:** no paper account. Mantik simulates Kalshi fills against real
  Kalshi books. Augr found the Kalshi demo exchange's listings unrealistic.

### Code and licensing

- Go, PostgreSQL, terminal clients, with the existing verification rules from
  `AGENTS.md`.
- Mantik is public under GPL-3.0. Code under MIT, BSD, Apache-2.0, GPL-3.0 and
  LGPL can be ported with attribution. AGPL code can be combined; its
  network-source obligation is already met by the public repository. Code
  without a license cannot be reused.

### Model independence

Operations, risk and protective exits must keep running when no model is
available. That is an existing Mantik rule.

### Models

The research desk, the model analyst and the daily brief call models through
the owner's Claude and OpenAI subscriptions. They run the `claude -p` and
`codex exec` command-line tools on the host's own logins, as other services on
Kvant already do. API-key environment variables are removed so usage stays on
the subscriptions.

Subscriptions have usage limits:
- the desk works under a daily budget;
- when a model is rate-limited or down, it backs off;
- trading never waits for a model.

Kvant's local models are CPU-only and too slow for this work.

### Account rules

The owner expects to start live with about $2,000 and to add cash over time.
The $25,000 pattern-day-trader rule no longer applies. FINRA retired it on
2026-06-04 and Alpaca replaced it with an intraday margin framework the same
day ([FINRA Notice 26-10](https://www.finra.org/rules-guidance/notices/26-10),
[Alpaca](https://docs.alpaca.markets/us/docs/understanding-finras-new-intraday-margin-rule-and-the-end-of-pdt)).
One Alpaca page mentions a 12-month transition period, so the builder confirms
the current rules on the paper account before relying on them.

Mantik models two account types and applies their rules itself:

- **Cash account.**
  - Buys use settled cash only. Stock and option trades settle the next
    business day.
  - Mantik tracks settled and unsettled cash.
  - Mantik refuses trades that would cause a good-faith or free-riding
    violation, such as selling a position bought with unsettled funds before
    those funds settle.
  - No short selling and no margin.
  - Options are limited to what a cash account allows: covered calls,
    cash-secured puts and long options. Spreads need a margin account.
- **Margin account.**
  - Reg T: 2x overnight buying power and 50% initial margin.
  - Maintenance margin.
  - The intraday margin framework.
  - Short selling.
  - Options spreads, including iron condors.
  - Margin needs at least $2,000 of equity. A $2,000 account sits exactly at
    that line, so if equity falls below $2,000, Mantik automatically switches
    to cash-account behaviour. It stops opening new margin or short positions
    and manages existing ones down.

Paper trading runs on Alpaca's $100,000 paper account, but Mantik only manages
a capped book within it. The cap starts at $2,000, and a simulated deposit
raises it. Alpaca checks orders against the full $100,000, so Mantik's own
account rules are what keep the book honest. Every order must pass the rules
for the configured account type at the book's own equity and settled cash.
The book's account type is configuration. The natural window runs as a margin
account, and backtests replay both types.

## Approaches considered

1. **Port Augr's strategy layer.** Rejected by the owner, and Augr's
   strategies are thin:
   - inputs arrive precomputed;
   - fills happen at the decision instant;
   - none was wired into Augr's runtime;
   - its generated-spec language could only see the current bar.
2. **Keep Mantik's current contracts and add strategies one at a time inside
   them.** Rejected. Every owner assumes the threshold rule:
   - decisions, outcomes and evaluation (all 13 declaration versions);
   - sensitivity, allocation and execution cost.

   The limits also block the strategies the owner wants: holds of at most one
   day, eligibility of at most one day, and at most 16 declarations. Each new
   strategy would need another round of versioned contracts.
3. **Chosen: a general strategy core on real data.**
   - Reuse open-source indicators and strategies.
   - Keep the Mantik parts that are general: ledger, execution, feed custody,
     retention, terminal and explanations.
   - Rebuild evaluation and allocation around a strategy interface.

## Design

### Strategy interface

A strategy is Go code behind one interface:

- **Inputs:** a point-in-time view at its decision time. The view covers bars,
  quotes, option chains, fundamentals and Kalshi books, plus the strategy's own
  positions, fills and state.
- **Outputs:** targets, which are stock positions, option structures (a set of
  legs with quantities) or Kalshi orders.
- **Features:** the strategy computes them itself from the view. It declares
  its data needs, warm-up length and parameters.
- **Identity:** a content hash of code version, parameters and data kinds
  identifies each version. A "current version" pointer selects the active one.

The same strategy and fill model run in backtests and in incubation and
qualified paper trading. Code converts targets into orders. Strategies never
place orders.

### Market data store

One point-in-time store:

- **Data:** raw daily bars, option bars and trades, Kalshi markets and books,
  corporate actions and filings.
- **Timestamps:** each observation keeps when it was published, observed and
  available.
- **Reads:** the store adjusts for corporate actions when read, not when
  written.
- **Collection and backfill** stay within the free rate limits.

The existing Alpaca journal, custody and archive code is reused where it fits.
That code only collects latest quotes today.

### Base library

The 1.0 base set. Each strategy cites its paper and records the open-source
code it came from or was checked against, at an exact commit:

1. **Trend / time-series momentum** on ETFs: sign of 1, 3 and 12-month excess
   return, sized by inverse volatility. Moskowitz, Ooi & Pedersen 2012.
2. **Cross-sectional momentum / rotation:** rank 3 to 12-month return
   skipping the last month, volatility-scaled. Jegadeesh & Titman 1993;
   Barroso & Santa-Clara 2015.
3. **Low volatility, long only.** Frazzini & Pedersen; Baker et al.
4. **Short-term mean reversion** in large caps. Jegadeesh 1990.
5. **Defined-risk options:**
   - put-write and covered calls;
   - vertical spreads;
   - iron condors and iron butterflies.

   Sources: Coval & Shumway 2001; Bakshi & Kapadia 2003.
6. **Kalshi calibrated forecasting:** a probability from external data, traded
   only when the net edge after fees is positive.
7. **Kalshi market making**, avoiding the longshot bias.
8. **The multi-agent model analyst** (the TradingAgents design from Augr), as a
   strategy whose trades code sizes.
9. **Value and quality from EDGAR fundamentals,** if fundamentals work fits in
   1.0.

Indicators come from a maintained Go library rather than Mantik code. TA-Lib
is the reference for checking their outputs. Ported strategies come from
permissively licensed sources such as QuantConnect Lean examples and
`je-suis-tm/quant-trading`.

A port must reproduce its source's trades on the same data before it enters
incubation. Code existing elsewhere proves only that it exists. Mantik's own
gates decide whether it still earns money.

### Research desk

A model works with tools:

- query data;
- read the library, the research journal and past trial results;
- compose a strategy spec;
- run a backtest;
- read the gate results.

**Specs.** Specs are typed expression trees over a reviewed operator set: lags,
rolling windows, ranks, cross-sectional and option-chain terms, and the base
library's building blocks. qlib's operators and Alpha158 features are a
starting vocabulary. Specs cannot express network access, clock reads,
randomness or orders.

**Evolution.** The desk mutates and recombines specs and keeps their lineage.
It penalises specs that are complex or too similar to existing strategies.

**Trial registry.** Every attempt is recorded with its daily returns,
including failed, crashed and abandoned ones. Every trial counts toward the
search total used by the statistical gates.

**Model training data.** A model may have memorised prices up to its training
cutoff. Evidence for a model-proposed idea therefore comes only from data after
that cutoff, or from forward paper trading.

**Triggers:**
- a schedule;
- a news event;
- an overseer finding;
- an owner request through the terminal conversation (#24).

**Output.** The desk can only create an inactive idea. It cannot activate,
size or trade anything.

### Gates and lifecycle

1. **Backtest gate.**
   - Walk-forward with purge and embargo.
   - Deflated Sharpe ratio using the registry's trial count.
   - Probability of backtest overfitting where enough trials exist.
   - Cost stress.
   - Parameter-neighbour sensitivity.
   - Comparison with cash and SPY.
   - A test that the strategy still works at the account's current size.
2. **Incubation.** A small fixed paper budget from the incubation pool, at
   least one strategy at a time. This proves the paper-trading path with real
   quotes; it earns no qualification credit. It replaces the "mechanics grant"
   discussed earlier.
3. **Qualification.** A forward paper window declared in advance. It must
   beat cash and SPY after costs, under Mantik's existing prospective rules:
   - predeclared window and benchmarks;
   - purged holdout;
   - day-clustered sign test, adjusted for the search total.

   These rules are generalised from the threshold rule to any strategy.
4. **Qualified.** The strategy gets a sleeve.
5. **Demotion and retirement.** The overseer cuts or removes the sleeve on
   drawdown beyond budget, edge decay or failed fills. A demoted strategy can
   requalify.

Calls made for the builder (owner may override):

- **Promotion is automatic in paper.** Code moves a strategy through
  incubation, qualification and its sleeve. This replaces the owner-issued
  paper eligibility in today's allocation records. The owner can pause or
  retire any strategy. Only the owner clears a hard halt. In 2.0, a strategy's
  first live sleeve needs owner approval.
- **Model-driven strategies take a forward-only path.** This covers the model
  analyst and any strategy whose decisions depend on a model call at run time.
  A backtest of a model is not trustworthy: the model may remember the period
  it is tested on, and its output is not deterministic. These strategies skip
  the backtest gate and enter incubation with a smaller budget.
- **Each strategy declares its own benchmark.** Long equity strategies use SPY.
  Market-neutral and options-income strategies use cash plus their declared
  risk. Kalshi strategies use cash. A strategy qualifies by beating its
  benchmark after costs.
- **The portfolio has a separate test against SPY.** It is measured across all
  sleeves.
- **1.0 and 2.0 have different bars.** 1.0 ships when the whole system has run
  correctly and reported honestly through the natural window, whether or not
  it beat SPY. That matches the roadmap's rule that reliability and
  performance are separate conclusions. 2.0, live trading, starts only after
  the portfolio beats SPY over that window.

### Capital distributor

- **One sizing authority.** All targets pass through it.
- **Sleeves.** Each qualified strategy gets one. Weights start equal by risk
  and grow or shrink with the overseer's confidence in its forward results.
- **Incubation pool:** a small fixed share. **Cash reserve:** fixed.
- **Rebalance** daily, and when a deposit arrives.
- **Deposits** are recorded as capital flows into unallocated cash. Returns are
  time-weighted, so deposits never count as profit.
- **Small accounts.** Fractional shares, minimum order size and the account
  rules above apply. At about $2,000, some strategies cannot hold a position:
  an iron condor needs room for its maximum loss, and a rotation needs room
  for several names. Such a strategy waits until deposits or gains make room.
  The backtest gate reports the smallest balance at which each strategy can
  trade.
- **The cap limits the book, not the broker.** The distributor's cap is the
  book's equity. A simulated deposit is a capital flow that raises it.
  Alpaca's extra paper cash is never used.
- **Deposit schedule.** The book receives $500 every other Friday at 06:00
  America/Chicago. Wall-clock time is kept across daylight saving, so 06:00
  is CST in winter and CDT in summer.
  - The first deposit is on the first Friday on or after the book opens, and
    then every 14 days.
  - The schedule is configuration on the book: amount, interval, weekday,
    time and time zone.
  - Each deposit is an idempotent capital flow keyed by its scheduled date. A
    restart or a late run applies it once, never twice.
  - A run that missed its time applies the deposit when it next runs. Its
    effective time stays the scheduled one.
  - Deposits land as settled cash before the market opens, so a cash account
    can use them that day.
  - Backtests replay the same schedule from the backtest's start.

### Risk

These hard limits sit in code that no model or strategy can change. The
starting values come from Augr's deleted overhaul plan:

- **Gross exposure:** 70% target, 100% hard limit.
- **Cash reserve:** 20%.
- **Per position:** 5%. **Per sleeve:** 20%.
- **Drawdown:** warn at 5%, pause at 10%, halt at 15%.
- **Daily loss:** 3%.
- **Options:** Greek caps by equity — delta 20%, gamma 5%, vega 10%,
  theta 2% per day, 10% per underlying. A multi-leg trade reserves its maximum
  loss plus the worst case if only some legs fill.
- **Margin** within Alpaca's buying power and a Mantik cap.

Other controls:

- **Kill switch** with one state view.
- **Per-strategy breakers.**
- **Emergency mode** allows reduce-only exits.
- **A hard halt** never resets itself.

### Overseer

**Health admission, every cycle.**
- **Checks:** data freshness, ledger reconciliation, worker liveness and the
  risk brake.
- **When a check fails or is unknown:** new positions stop. Exits, settlement
  and reconciliation keep running. This follows Augr's unwired daily
  supervisor, here enforced.

**Performance review, daily.**
- **Compares** forward returns with the backtest and SPY, drawdown with its
  budget, and fill quality with the fill model, and tests for edge decay.
- **Actions:** grow a sleeve, cut it, demote, retire, or ask the research desk
  to investigate.

**Daily brief.** One brief to the owner, through the terminal and a
notification. Deterministic rules decide; a model may write the brief's
narrative from finalized numbers.

### Kalshi engine

1. Discover open markets.
2. Filter on liquidity, spread and time to close.
3. Estimate a probability from external data, calibrated against outcomes.
4. Check that the net edge after fees is above a minimum.
5. Size at quarter-Kelly within the Kalshi sleeve.
6. Place a limit order, maker first.
7. Settle, then record calibration (Brier score and buckets) and feed it back
   into the probability model.

Model "conviction" never authorises a trade.

Calls made for the builder:
- **A separate notional book.** Kalshi trades are simulated and Alpaca has no
  Kalshi account, so they run in their own book. It is reported on its own and
  left out of the portfolio's SPY comparison.
- **Weather markets first.** Their free forecast and station data (NOAA,
  Open-Meteo) and objective resolution suit calibration. Economic-release
  markets, using FRED data, come next.

### Execution and ledger

**Existing.** Mantik's paper ledger, execution worker and protective exits stay.

**Add:**
- an Alpaca paper adapter for stock and multi-leg option orders;
- a Kalshi fill simulator;
- per-fill fee and quantity fields (#25);
- reconciliation against the Alpaca paper account.

**Order handling.** Raw venue data is recorded before normalization. A
submission whose outcome is ambiguous is recovered by its client ID, never
resubmitted.

**Fill records.** Every Alpaca paper fill is recorded as the venue's result.
Mantik also computes what its own fill model would have given:
- next-bar or quote-based price;
- fees;
- slippage.

Gates, sleeves and the SPY comparison use the less favourable of the two,
because Alpaca's paper fills can be optimistic. The overseer reports the gap
between them as fill quality.

### Owner interface

- **Existing terminal views**, plus:
  - the daily brief;
  - a deposit command;
  - per-strategy lifecycle and sleeve views;
  - the conversation (#24).
- **Every rejection and idle dollar carries a reason** from a fixed list.

## What happens to current Mantik work

- **Kept as is:**
  - paper ledger, execution worker and protective exits;
  - feed custody and archives;
  - retention worker;
  - typed tools and finalized-decision explanations;
  - news investigation;
  - release and verification practice.
- **Generalised from the threshold rule:**
  - evaluation declarations and assessments;
  - eligibility;
  - allocation records;
  - attribution.
- **Stopped:** threshold-specific contract work and the retention-first plan.
  Retention limits are revisited once real strategies run.
- **Re-mapped:** open issues (#20–#28, #33, #34) are mapped onto the build
  order below. #14, #15, #24 and #25 keep their scope.

## Build order

Each phase ends with a check on real data, not a fixture.

1. **Market data store.**
   - Backfill daily bars for the universe since 2016.
   - Collect option bars since February 2024 for liquid underlyings.
   - Collect Kalshi markets.

   Check: SPY's backfilled history matches an independent source.
2. **Strategy interface and engine.**
   - One event-driven engine for backtest and paper.
   - Next-bar fills and costs.

   Check: a buy-and-hold SPY strategy reproduces SPY's total return within
   costs.
3. **Base library, stocks first.**
   - Indicator dependency.
   - Trend, rotation, low volatility and mean reversion, ported and reproduced
     against their sources.
4. **Trial registry and backtest gate.**
5. **Alpaca paper adapter, account rules, capital distributor and
   incubation.** Includes the cash and margin rules and the $2,000 capped
   book.

   Check: the first real-quote paper positions open, are protected and close
   through the ledger.
6. **Overseer and daily brief.**
7. **Options.**
   - Chains.
   - Multi-leg structures: verticals, iron condors, put-write.
   - Greek limits.

   Check: multi-leg open and close work on Alpaca paper.
8. **Research desk** with evolution, and the model analyst as a strategy.
9. **Kalshi engine** and simulator.
10. **Qualification gate**, generalised from Mantik's prospective rules.
11. **Social intake and copy-follow (#14, #15).**
    - Copy-follow is a strategy type.
    - Copy-follow from 13F filings can be backtested on filing history.
    - Social signals take the forward-only path.
    - Social intake feeds the research desk.
    - This phase waits on the owner's provider accounts.
12. **Recovery and release gates.**
    - Funded-state recovery drills (#26).
    - The isolated release candidate with rollback proof (#27).
13. **Natural paper window and 1.0 audit (#28, #34).**
    - At least six months, on the $2,000 margin book with simulated deposits.
    - 1.0 needs a correct, honestly reported run.
    - 2.0 needs the portfolio to beat SPY after costs.

Phases 6 and 7, and phases 8 and 9, can proceed in parallel.

## Left to the builder

- **The indicator library.** `cinar/indicator` (AGPL-3.0, Go, maintained) or
  `markcheno/go-talib` (MIT). Choose by API fit and test parity with TA-Lib.
- **Universe definition.** "Not a penny stock" is proposed as price at or above
  $5 and a minimum average daily dollar volume.
- **Schema layout**, migration order and how the threshold-specific records
  stay readable.
- **Initial sleeve weights and incubation budget size**, within the risk
  limits above.
- **Whether value and quality** from EDGAR fit in 1.0 or follow it.
