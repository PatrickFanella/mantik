# Mantik roadmap to 1.0

Mantik 1.0 is an isolated paper-trading service supervised from the terminal.
It trades a portfolio of strategies on its own, in Alpaca paper trading:
- code makes and enforces every money decision;
- a model researches, proposes and tests strategies with tools.

The book starts at $2,000 and receives $500 every other Friday. It holds a
library of well-studied strategies, including multi-leg options, plus evolved
ones and Kalshi prediction markets. 1.0 ends with a correct, honestly reported
paper run of at least six months. 2.0, live trading, starts only after the
portfolio beats SPY after costs over that run.

The approved design is [the trading system design](plans/trading-system.md).
The work is tracked in
[roadmap #47](https://github.com/subculture-collective/mantik/issues/47).

**Current state, 2026-11-13** (schema 115). Mantik has these working parts:
- a paper ledger, an execution worker and protective exits;
- feed custody and archives, and a retention worker;
- prospective evaluation rules;
- terminal supervision, and finalized-decision explanations.

All of them are built around a single price-threshold rule, and their evidence
is synthetic fixture evidence. No running service has funded a paper position.

On 2026-11-13 the owner changed direction: away from deepening threshold-specific
contracts, toward a general strategy core on real data. Mantik replaces Augr.
Augr's workflows were reviewed and evaluated rather than ported
([review](https://claude.ai/artifact/TSXsuuFA5WPgSBAQMb3dJ1), private).

The [2026-10-27 delivery audit](research/1-0-delivery-audit.md) records the
earlier baseline.

Issues, pull requests and the project board moved to GitHub on 2026-11-10 and
were renumbered. Dated audits under `docs/research/` keep their original Gitea
numbers; each GitHub issue names its Gitea original in its first line.
Three-digit pull request numbers in the plans (#100–#385) are Gitea history;
#1, #2 and #38 onward are GitHub.

## Release definition and scope

The [1.0 release gate, #34](https://github.com/subculture-collective/mantik/issues/34),
is the acceptance index. A safety or functional failure blocks release. Minimum
evidence and stop criteria are declared before the natural run, and an
insufficient or curtailed run remains incomplete.

1.0 and 2.0 have different bars:

- **1.0** needs the whole system to run correctly and report honestly through
  a natural paper window of at least six months, whether or not it beat SPY.
- **2.0 (live trading)** starts only after the portfolio beats SPY. The measure
  is total return after all costs, time-weighted, over that window.

Operational reliability, strategy performance and statistical qualification
stay separate conclusions. Losses and insufficient samples must be visible. A
test fixture never earns qualification credit.

The design's decisions apply to every issue:
- free market data;
- models through the owner's Claude and OpenAI subscriptions;
- a $2,000 capped book inside the $100,000 Alpaca paper account;
- cash and margin account rules;
- options and margin in 1.0;
- reuse of open-source strategy code;
- one lifecycle for every strategy.

The design replaces the earlier rule that the first lifecycle use a single
native baseline. 1.0 now runs a strategy library on purpose, but every strategy
must earn its capital through the same gates.

## Delivery order

[Roadmap #47](https://github.com/subculture-collective/mantik/issues/47) holds
the full order, waves and critical path. Its epics:

| Epic | Result |
| --- | --- |
| [A #48](https://github.com/subculture-collective/mantik/issues/48) Free point-in-time market data | Daily bars since 2016, option bars since February 2024, Kalshi markets; SPY checked against an independent source |
| [B #49](https://github.com/subculture-collective/mantik/issues/49) One strategy interface and engine | Strategy interface, engine, cash and margin rules, biweekly deposits; SPY buy-and-hold reproduced |
| [C #50](https://github.com/subculture-collective/mantik/issues/50) Base strategy library for stocks | Indicators and four ported strategies with replication tests |
| [D #51](https://github.com/subculture-collective/mantik/issues/51) Trial registry and backtest gate | Every trial recorded; walk-forward, stress, deflated Sharpe, overfitting probability |
| [E #52](https://github.com/subculture-collective/mantik/issues/52) Real-quote paper trading on the capped book | Alpaca paper adapter, $2,000 book, capital distributor, risk limits, incubation |
| [F #53](https://github.com/subculture-collective/mantik/issues/53) Overseer and daily brief | Health admission, daily review and resizing, owner brief and controls |
| [G #54](https://github.com/subculture-collective/mantik/issues/54) Options and multi-leg strategies | Chains, structures with reserves and Greek caps, Alpaca multi-leg orders |
| [H #55](https://github.com/subculture-collective/mantik/issues/55) Model research desk | Subscription model adapter, typed specs, research desk, evolution, model analyst |
| [I #56](https://github.com/subculture-collective/mantik/issues/56) Kalshi decision engine | Calibrated weather forecasts, fill simulator, maker-first strategy |
| [J #57](https://github.com/subculture-collective/mantik/issues/57) Forward qualification and promotion | Generalised prospective rules, automatic promotion and demotion |

After the epics, the remaining acceptance issues close in this order:

1. [#25](https://github.com/subculture-collective/mantik/issues/25)
   Attribution, including the gross amount, fee and share quantity on every
   fill.
2. [#24](https://github.com/subculture-collective/mantik/issues/24)
   Conversation.
3. [#14](https://github.com/subculture-collective/mantik/issues/14) and
   [#15](https://github.com/subculture-collective/mantik/issues/15) Social
   intake and copy-follow, once the owner's provider accounts exist.
4. [#26](https://github.com/subculture-collective/mantik/issues/26) Recovery
   drills.
5. [#27](https://github.com/subculture-collective/mantik/issues/27) Release
   candidate.
6. [#28](https://github.com/subculture-collective/mantik/issues/28) Natural
   paper window.
7. [#34](https://github.com/subculture-collective/mantik/issues/34) 1.0 audit.

#20, #21, #22, #32 and #33 are closed. #32 was closed by the owner. The others
were superseded by roadmap #47 on 2026-11-13.

## Next steps, 2026-11-13

These five issues have no blockers and can start in parallel:

- [A1 #58](https://github.com/subculture-collective/mantik/issues/58)
  Confirm what Alpaca's free data actually covers. This is a spike, and it
  heads the critical path.
- [E1 #59](https://github.com/subculture-collective/mantik/issues/59)
  Submit and reconcile stock orders on Alpaca paper.
- [C1 #60](https://github.com/subculture-collective/mantik/issues/60)
  Choose the Go indicator library. This is a spike.
- [A6 #80](https://github.com/subculture-collective/mantik/issues/80)
  Collect Kalshi markets and order books.
- [H2 #82](https://github.com/subculture-collective/mantik/issues/82)
  Call models through the owner's subscriptions.

The critical path runs through:
1. the data spike;
2. bars;
3. adjusted reads;
4. the strategy interface;
5. the engine;
6. account rules;
7. the gate statistics;
8. incubation;
9. qualification.

## Delivered foundations and historical milestones

The old M0–M7 headings described the original build order. Their issue history is
retained; the active milestone titles now describe the 1.0 exit gates above.
The Historical issues column keeps the original Gitea numbers.

| Foundation | Historical issues | Delivery boundary |
| --- | --- | --- |
| Service, library, corpus assessment, datasets and replay | #1, #3–#6, #28 | Delivered contracts and retained evidence; no automatic market qualification. |
| Durable routing, timers, delivery and capability readiness | #7–#10 | Delivered within their fixture/operational contracts; financial protection still needs #26 acceptance. |
| Price/calendar and news ingestion | #11–#12 | Delivered adapters/recorded contracts; access, cadence and enabled runtime must be verified for the selected release. |
| Structured research, hypotheses, isolated workers and generated candidates | #15–#18 | Delivered recorded/isolated contracts; assembled autonomous dispatch remains #33. |
| Read-only terminal supervision | #22 | Delivered inspection views; conversational direction and economic attribution remain #24/#25. |

The [research contract audit](research/research-contract-acceptance.md),
[candidate evaluation audit](research/candidate-evaluation-acceptance.md) and
[terminal supervision audit](research/terminal-supervision-acceptance.md) retain
their original evidence boundaries. The obsolete PR-by-PR roadmap appendices
have been removed; maintained reference contracts and Git/PR history retain that
implementation detail.

## After 1.0

Alpaca remains the intended eventual real-money destination. A later live stage
requires predeclared prospective profitability/risk evidence, separate account
and credential/environment identity, broker order/reconciliation qualification,
independent capital/protection limits and explicit bounded operator activation.
Paper qualification is evidence for that review, never authorization for live
orders. Broad venue expansion and a language rewrite are not 1.0 prerequisites.

Research, evaluation and operations retain separate authority. Operations acts
only on finalized applicable decisions and must work without models. Keep local
checks, exact-head PR CI, merged-main CI, deployment, runtime observations and
natural qualification distinct. Preserve original source/evidence, idempotence,
failures, source hashes and recovery receipts outside Git; do not import or alter
existing Augr financial state or replace current pilots/Switchyard resources.
