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

**Implementation status, 2026-11-15, America/Chicago** (schema 122 on this branch; merge state is tracked in #47).
Wave 1 merged in [PR #118](https://github.com/subculture-collective/mantik/pull/118)
at schema 118. Wave 4 adds [resumable stock backfills](reference/daily-backfill.md),
the [asset-identity universe](reference/stock-universe.md), and
[indicative option observations](reference/option-data.md), in one PR per issue.
Their current merge and check state is tracked in
[roadmap #47](https://github.com/subculture-collective/mantik/issues/47).
These implementations do not establish release or strategy qualification.

Wave 9 adds [recorded gate decisions, sleeve sizing and persistent order risk](reference/portfolio-gates-and-risk.md) for #72–#74 in one combined PR. Gates retain every failed check and skip historical testing for model-driven versions. The distributor sizes from local equity, and the capped order path enforces fixed limits and operator controls. The base-library check records four rejections on retained real SPY prices; synthetic historical clocks and one-ETF coverage earn no admission. Current merge and epic state remain in #47.

Wave 5 adds the opt-in [daily-bar worker](reference/daily-bars-worker.md) for
#95 and the [independent SPY comparison](reference/spy-comparison.md) for #94
in one reviewable change. The connected collector check uses disposable storage.
The follow-up independent check resolves the close-convention mismatch with
retained primary-exchange prices and an independent free reference. All 2,733
sessions pass at the unchanged 5 bps tolerance (one explained SIP gap uses a
separately verified Nasdaq close). The original raw-SIP failure remains a
diagnostic; this is not a silent change to strategy-engine prices. The complete
report and merge state are tracked in [#94](https://github.com/subculture-collective/mantik/issues/94).

Wave 1 now supplies a separate stock-paper adapter (#59), the indicator
library decision (#60), public Kalshi collection (#80), and bounded
subscription research calls (#82). [The adapter references](reference/README.md)
record their commands, evidence and limits. The indicator decision selects
MIT-licensed `markcheno/go-talib` with real adjusted-SPY parity observations;
the pinned adapter and maintained parity tests were delivered in #76.

The new strategy core now has:

| Delivered | Issues and evidence | Boundary |
| --- | --- | --- |
| Free-data coverage assessment | [A1 #58](https://github.com/subculture-collective/mantik/issues/58#issuecomment-6870787280) | Recorded read-only provider observations; no order-enforcement proof. |
| Historical daily-bar client, point-in-time store and adjusted reads | A2–A4, #61–#63; merged PRs #109–#111 | Exact decimal prices and asset identity; the independent SPY check remains #94. |
| Strategy interface and daily engine | B1–B2, #64–#65; merged PRs #112–#113 | Point-in-time decisions, next-session fills, costs and corporate actions; one stock strategy per simulated run. |
| Deposits and time-weighted returns | B4 #67; merged PR #114 | Scheduled capital flows are separate from profit in the simulated book. |
| Daily trial registry | D1 #68; merged PR #117 | Append-only attempts, daily returns, lineage and search totals; Wave 8 adds diagnostics and Wave 9 records gate decisions. |
| Cash and margin account rules | [B3 #66](https://github.com/subculture-collective/mantik/issues/66); merged PR #115 | Settled cash, T+1, ordinary-equity margin and coded refusals in the simulator; broker enforcement remains untested. |

The paper ledger, execution worker, protective exits, feed custody, archives,
retention, prospective evaluation rules and terminal supervision remain as
earlier foundations. Their threshold-specific contracts have not yet been
joined to the new core in a running strategy portfolio. Merged implementation
and fixture tests do not establish real-quote execution or qualification.

The provider assessment, indicator comparison, and connected adapter checks
are real observations with the scope documented in their references. The new
engine and account tests use synthetic fixtures. The SPY historical-accounting exit is recorded in #105, with its synthetic availability-clock limitation. First real-quote paper positions and the natural qualification window remain open; no funded portfolio run is claimed here.

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

| Epic | Implemented work issues | Result still required to close the epic |
| --- | --- | --- |
| [A #48](https://github.com/subculture-collective/mantik/issues/48) Free point-in-time market data | 10/10 | Closed after the explicit primary-close SPY reconciliation; delisted-stock and indicative-option limits remain |
| [B #49](https://github.com/subculture-collective/mantik/issues/49) One strategy interface and engine | 5/5 | Closed after the real-price SPY accounting check in both account types with deposits (#105) |
| [C #50](https://github.com/subculture-collective/mantik/issues/50) Base strategy library for stocks | 6/6 | Closed under the owner-approved recipe ports and pinned reference tests |
| [D #51](https://github.com/subculture-collective/mantik/issues/51) Trial registry and backtest gate | 4/4 on this branch | Four recorded base-library decisions with reasons and declared trade-plan minimums; source gaps remain failed checks |
| [E #52](https://github.com/subculture-collective/mantik/issues/52) Real-quote paper trading on the capped book | 4/6 on this branch | Incubation worker and first protected real-quote positions (#77, #107) |
| [F #53](https://github.com/subculture-collective/mantik/issues/53) Overseer and daily brief | 0/4 | Health admission, daily review and resizing, owner brief and controls |
| [G #54](https://github.com/subculture-collective/mantik/issues/54) Options and multi-leg strategies | 1/5 | Structures with reserves and Greek caps, Alpaca multi-leg orders and strategy recipes |
| [H #55](https://github.com/subculture-collective/mantik/issues/55) Model research desk | 2/5 | Research desk, evolution and model analyst |
| [I #56](https://github.com/subculture-collective/mantik/issues/56) Kalshi decision engine | 3/3 | Calibrated paper trades must settle and feed calibration records; maker intents alone do not meet that exit |
| [J #57](https://github.com/subculture-collective/mantik/issues/57) Forward qualification and promotion | 0/3 | Generalised prospective rules, automatic promotion and demotion, and a recorded forward assessment |

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

#20–#22, #30–#33 are closed. #32 was closed by the owner; #20–#22 and #33
were superseded by roadmap #47 on 2026-11-13. Their closed state does not
complete the replacement epics or their acceptance checks.

## Next steps

After Wave 9 merges, these are the next work units. This describes dependencies,
not pickup approval, deployment or provider access. Check #47 and each issue's
current blockers before starting work.

| Work | Prerequisites and remaining outcome |
| --- | --- |
| [E5 #77](https://github.com/subculture-collective/mantik/issues/77) Real-quote incubation | After #72, #73 and #74: join the strategy view, gate, sizing and risk path to the running paper worker. |
| [G2 #84](https://github.com/subculture-collective/mantik/issues/84) Multi-leg structures | #81 and #66 are merged; #74 supplies the order-risk boundary. Add reserves and Greek limits for option structures. |
| [H3 #86](https://github.com/subculture-collective/mantik/issues/86) Research desk | #82 and #79 are merged; #72 supplies recorded gate feedback. |

Wave 9 completes the ninth issue on the original thirteen-issue critical path.
The remaining chain is #77 → #78 → #92 → #108. The capital branch now reaches
#77 through the capped book, distributor and risk checks. The separate SPY data
and engine exits (#94 and #105) are recorded; they do not establish real-quote
execution or forward qualification.

### Access and evidence still needed

- **Alpaca access:** the separate paper-order adapter supports the existing
  OAuth profile. On 2026-11-15 it placed and canceled one nonmarketable SPY
  paper limit order and reported zero position differences against its
  preflight snapshot. The market-read client still uses API-key headers;
  OAuth market-data backfill support remains to be joined to that client.
- **Data coverage:** [the #58 observations](https://github.com/subculture-collective/mantik/issues/58#issuecomment-6870787280)
  found delisted-symbol bars but incomplete discovery of delisted companies,
  reused tickers and indicative-only option snapshots. #89 must retain those
  universe limits; no survivor-bias-free stock history is claimed.
- **Engine scale:** the [engine reference](reference/engine.md#limits) records
  per-decision view rebuilding and quadratic growth with session count. Its
  documented 11-year single-asset run takes about 45 seconds. The Wave 9 gate exercise is bounded to 520 sessions, two folds and two neighbours per recipe. Larger repeated searches still need profiling.
- **Provider accounts:** social intake and copy-follow (#14, #15) still wait
  on the owner's accounts. They remain in the 1.0 scope.

The SPY data/accounting results (#94, #105) are recorded. The next acceptance results are protected real-quote paper positions (#107) and a recorded forward promotion or rejection (#108); both remain open. The six-month natural window
(#28) follows the isolated release candidate (#27); it has not been credited by
these implementation issues.

## Delivered foundations and historical milestones

The old M0–M7 headings described the original build order. Their issue history is
retained; the active milestone titles now describe the 1.0 exit gates above.
The Historical issues column keeps the original Gitea numbers.

| Foundation | Historical issues | Delivery boundary |
| --- | --- | --- |
| Service, library, corpus assessment, datasets and replay | #1, #3–#6, #28 | Delivered contracts and retained evidence; no automatic market qualification. |
| Durable routing, timers, delivery and capability readiness | #7–#10 | Delivered within their fixture/operational contracts; financial protection still needs #26 acceptance. |
| Price/calendar and news ingestion | #11–#12 | Delivered adapters/recorded contracts; access, cadence and enabled runtime must be verified for the selected release. |
| Structured research, hypotheses, isolated workers and generated candidates | #15–#18 | Delivered recorded/isolated contracts; the new core's research desk and integrated runtime remain in epics H and E. #33 is superseded. |
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
