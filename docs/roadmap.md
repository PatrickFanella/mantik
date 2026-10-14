# Mantik roadmap

Mantik is an autonomous paper-trading research system supervised through a
terminal, with occasional conversational direction. It continuously gathers
information, opens decision cases when conditions warrant, tests hypotheses,
allocates paper capital under explicit rules, and records outcomes.

The goal is dependable, measurable paper operation with evidence of strategy
value. Profitability must be established empirically. Live trading is outside
the roadmap.

## Planning and delivery

- [Project board](https://git.subcult.tv/subculture-collective/mantik/projects/5)
- [Milestones](https://git.subcult.tv/subculture-collective/mantik/milestones)
- [Delivery model](delivery.md)
- [Architecture](architecture.md)

Milestones have capability exit gates and no promised dates. Issue acceptance
criteria and native dependencies are authoritative for implementation. Board
columns show current activity. Initial future issues are unassigned planning
work with no automatic pickup labels.

The bootstrap in [PR #2](https://git.subcult.tv/subculture-collective/mantik/pulls/2)
provides a persistent service, CLI, structured records, and one unqualified
synthetic lifecycle. It has local and hosted checks but is not real-market
qualification. Consult Gitea for its current merge status.

## Design commitments

Research, decision-making, and operations have distinct authority. The library
supplies reusable methods, components, tests, and evidence rather than treating
each idea as a new trading system.

Established rules handle routine cases; LLM tools investigate ambiguity and new
ideas. Mandatory evaluations run independently of model tool selection.
Evaluation carries structured claims, measurements, provenance, uncertainty,
and check outcomes. Original narrative sources remain available for targeted
inspection. Readable explanations are generated after decision finalization.

Decisions define validity, safety limits, stops/exits, and future obligations.
Operations enforces them independently of model availability. Trigger sources
include news, prices, calendars, watched social accounts, copytrading activity,
portfolio/execution state, research milestones, failures/recovery, and human
direction. Missing expected events are conditions too.

Synthetic, stress, historical, and prospective evidence remain distinguishable.
A successful code test, merged PR, healthy service, or attractive backtest does
not qualify a strategy.

## Milestone sequence

### [M0: Foundation](https://git.subcult.tv/subculture-collective/mantik/milestone/155)

A runnable synthetic paper lifecycle with persistent service, CLI, isolated development, and verified delivery.

**Exit gate:** Bootstrap PR #2 merged, issue #1 reconciled, and current-main CI passes. This is not real-market qualification.

- [#1: Bootstrap core](https://git.subcult.tv/subculture-collective/mantik/issues/1), delivered through [PR #2](https://git.subcult.tv/subculture-collective/mantik/pulls/2).
- [#28: Publish the roadmap and project backlog](https://git.subcult.tv/subculture-collective/mantik/issues/28).

### [M1: Library and reproducible research](https://git.subcult.tv/subculture-collective/mantik/milestone/156)

Reusable strategy and evaluation contracts, recoverable research provenance, versioned datasets, and deterministic replay.

**Exit gate:** A registered baseline method can be replayed against pinned observations with identical decisions and retained evaluation results; original research has an assessed inventory.

- [#3: Define reusable strategy, component, and evaluation contracts](https://git.subcult.tv/subculture-collective/mantik/issues/3). Depends on #1.
- [#4: Recover and assess the original Augr research corpus](https://git.subcult.tv/subculture-collective/mantik/issues/4). Depends on #1.
- [#5: Add versioned observation datasets and point-in-time replay](https://git.subcult.tv/subculture-collective/mantik/issues/5). Depends on #3.
- [#6: Register baseline methods and reproducible experiment recipes](https://git.subcult.tv/subculture-collective/mantik/issues/6). Depends on #3, #5, #4.

### [M2: Durable triggers and operations](https://git.subcult.tv/subculture-collective/mantik/milestone/157)

Persistent triggers, independent timers, missing-event detection, durable action delivery, and restart-safe protection.

**Exit gate:** A finalized decision survives restart; its timer and protective actions execute without an LLM or new quote; unknown outcomes are reconciled without duplicate effects.

- [#7: Implement persistent trigger definitions and decision-case routing](https://git.subcult.tv/subculture-collective/mantik/issues/7). Depends on #3.
- [#8: Add independent deadlines and missing-event detection](https://git.subcult.tv/subculture-collective/mantik/issues/8). Depends on #7.
- [#9: Add durable action delivery and uncertain-outcome reconciliation](https://git.subcult.tv/subculture-collective/mantik/issues/9). Depends on #7.
- [#10: Expose operational exceptions and capability-scoped readiness](https://git.subcult.tv/subculture-collective/mantik/issues/10). Depends on #8, #9.

### [M3: Information and case routing](https://git.subcult.tv/subculture-collective/mantik/milestone/158)

Time-aware inputs from prices, calendars, news, watched social accounts, and copytrading observations.

**Exit gate:** At least one authorized real feed and recorded fixtures route through the same provenance-aware contracts; repeats, corrections, late data, and source outages have explicit case behavior.

- [#11: Add a price and calendar ingestion adapter with recorded fixtures](https://git.subcult.tv/subculture-collective/mantik/issues/11). Depends on #5, #7, #10.
- [#12: Normalize news claims, revisions, and related-event routing](https://git.subcult.tv/subculture-collective/mantik/issues/12). Depends on #5, #7.
- [#13: Add watched social-account policies and attributed observations](https://git.subcult.tv/subculture-collective/mantik/issues/13). Depends on #12, #7.
- [#14: Implement copytrading observation and bounded follow policies](https://git.subcult.tv/subculture-collective/mantik/issues/14). Depends on #11, #13, #9.

### [M4: Autonomous research and structured decisions](https://git.subcult.tv/subculture-collective/mantik/milestone/159)

Tool-driven investigation, hypothesis lifecycles, isolated research workers, and generated candidate implementations.

**Exit gate:** An unfamiliar observation opens a bounded investigation that reuses the library, runs mandatory evaluations, and produces a finalized structured decision or abstention; generated methods remain unqualified candidates.

- [#15: Define decision workflow and LLM tool contracts](https://git.subcult.tv/subculture-collective/mantik/issues/15). Depends on #3, #12, #9.
- [#16: Add hypothesis lifecycle and autonomous research-attempt history](https://git.subcult.tv/subculture-collective/mantik/issues/16). Depends on #6, #15.
- [#17: Add an isolated research-worker protocol for reusable tools](https://git.subcult.tv/subculture-collective/mantik/issues/17). Depends on #5, #15.
- [#18: Evaluate generated candidate strategy implementations in isolation](https://git.subcult.tv/subculture-collective/mantik/issues/18). Depends on #16, #17.

### [M5: Portfolio and evaluated paper strategies](https://git.subcult.tv/subculture-collective/mantik/milestone/160)

Allocator-owned capital, realistic execution assumptions, and evidence-based strategy admission and retirement.

**Exit gate:** Competing candidates pass through account-scoped allocation and deterministic gates; paper outcomes include costs and execution limitations; no research workflow bypasses safety.

- [#19: Implement account-scoped portfolio allocation for competing proposals](https://git.subcult.tv/subculture-collective/mantik/issues/19). Depends on #3, #9, #16.
- [#20: Add realistic paper execution and economic reconciliation models](https://git.subcult.tv/subculture-collective/mantik/issues/20). Depends on #11, #19.
- [#21: Add automatic evaluation, paper admission, and retirement gates](https://git.subcult.tv/subculture-collective/mantik/issues/21). Depends on #6, #16, #18, #20.

### [M6: Terminal supervision and direction](https://git.subcult.tv/subculture-collective/mantik/milestone/161)

A TUI, conversational application tools, and evidence-linked explanations and attribution.

**Exit gate:** The operator can inspect and direct the running service from the terminal, trace a decision to its evidence, and recover an exception; narrative is produced only after finalization.

- [#22: Build the terminal supervision interface](https://git.subcult.tv/subculture-collective/mantik/issues/22). Depends on #10, #7.
- [#23: Add conversational supervision and post-decision explanations](https://git.subcult.tv/subculture-collective/mantik/issues/23). Depends on #15, #22, #16.
- [#24: Expose decision and portfolio performance attribution](https://git.subcult.tv/subculture-collective/mantik/issues/24). Depends on #21, #22.

### [M7: Prospective paper qualification](https://git.subcult.tv/subculture-collective/mantik/milestone/162)

Recovery evidence, deployment receipts, and natural paper evaluation against declared baselines.

**Exit gate:** The exact release passes restore/restart checks and a predeclared prospective observation window; incomplete evidence and failed performance gates are reported honestly. Live trading remains outside scope.

- [#25: Qualify backup, restore, restart, and protective recovery](https://git.subcult.tv/subculture-collective/mantik/issues/25). Depends on #10, #20.
- [#26: Prepare an isolated paper release with rollback and capability receipts](https://git.subcult.tv/subculture-collective/mantik/issues/26). Depends on #25, #21, #23.
- [#27: Run a predeclared prospective paper qualification window](https://git.subcult.tv/subculture-collective/mantik/issues/27). Depends on #26, #24.

## First implementation sequence

The first running-app checkpoint is an **isolated operational pilot after M2**,
once #7–#10 are merged and their integrated recovery behavior is verified.
Finish durable action delivery (#9) and operational inspection/readiness (#10),
then run the persistent service with controlled, current-time synthetic inputs
for a declared 24-hour window. Do not defer this feedback loop until the full
research, portfolio, or terminal roadmap is complete.

The pilot must demonstrate a traceable lifecycle, no duplicate effects after
interruption, independent protection or a visible price-unavailable exception,
bounded operator recovery, and preserved state after restart and isolated
restore. See the [first pilot acceptance plan](runbooks/first-pilot.md).
This proves operational behavior within the fixture contract. Real-feed
validation follows through #11; economic and strategy qualification retain
their M5/M7 gates.

After the foundation is accepted, start with the library/evaluation contract
and recovery of the original research corpus. Build versioned datasets and
replay against those contracts. Trigger routing can then proceed alongside
baseline research, followed by independent protective timers and durable action
delivery.

Introduce real information through the same recorded-input contracts, then
add structured model investigation and isolated research tools. Generated
methods remain candidates until the portfolio, execution, and evaluation gates
support admission.

Terminal work starts as soon as the case and exception APIs support it; it does
not need to wait for every market or strategy. Prospective qualification follows
the integrated safety, execution, recovery, and operator workflows.

## Decisions owned by future issues

- First authorized market/calendar provider, data coverage, and adapter limits.
- Where the original research corpus resides and which sources may be retained.
- Initial baseline strategy families, benchmarks, instruments, and dataset scope.
- Watched accounts/traders and whether each supplies research, decision inputs,
  or bounded paper-follow behavior.
- Research-worker transport, isolation, resource policy, and artifact storage.
- Model/tool selection and measurable benefit against deterministic baselines.
- Simulation assumptions and admission, retirement, and prospective criteria.
- A Mantik-owned deployment target, backup storage, and authorized observation
  window.

Go 1.26.9 and PostgreSQL are the current foundation. Reconsider Rust only with a
specific requirement and a representative comparison. Python research tools can
be added through the worker boundary without changing the service language.

## Scope boundaries

Existing Augr implementations and research can be assessed for reuse. Its
services, credentials, and financial data are not a migration target in this
plan. Broad venue expansion and live trading are not prerequisites for the
first qualified paper workflow. Do not activate all inputs or generate a fleet
of strategies merely to demonstrate activity.

Keep the roadmap current when accepted scope changes. Update issue dependencies
and milestone gates together, retaining failed or incomplete evidence rather
than rewriting it into a pass.
