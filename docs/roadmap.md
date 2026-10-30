# Mantik roadmap to 1.0

Mantik 1.0 is a complete, isolated paper-research and paper-operation service
supervised from the terminal. It must turn attributed inputs into research,
applicable evaluation, finalized decisions or abstentions, owner-issued paper
eligibility, finite capital allocation, reconciled paper execution, protection
and inspectable outcomes. Social/copy-follow, conversational direction,
attribution and recovery remain part of 1.0.

**Current state:** the provenance, replay, research and terminal foundations are
substantial; the positive funded paper lifecycle is unfinished. There is no
accepted complete paper release or natural qualification run. Successful tests,
merged PRs, healthy workers and closed foundation issues do not establish those
outcomes. The [2026-10-27 delivery audit](research/1-0-delivery-audit.md) records
what exists and what remains at merged main `897a3b433afc599835ae60b74c4db9166a5ee67c`
(schema 74).

## Release definition and scope

The [1.0 release gate, #286](https://git.subcult.tv/subculture-collective/mantik/issues/286),
is the acceptance index. Release requires the development, isolated runtime,
recovery and fresh natural-run gates below. A safety or functional gate failure
blocks release. Minimum evidence and stop criteria are declared before the run;
an insufficient or curtailed run remains incomplete.

Operational reliability, strategy performance and statistical qualification are
separate conclusions. A paper system can reveal losses or insufficient strategy
samples; those results must be visible and must not be represented as profitability
or a qualified strategy. A test fixture never receives natural qualification
credit. Live trading is outside 1.0.

The first complete lifecycle uses an explicit supported native baseline,
instruments, feed scope and versioned cost/evaluation methods. Do not expand to a
fleet of strategies, providers, venues or languages to demonstrate activity.
Generated and copy-follow methods use the same applicable evaluation, admission,
allocation and execution authorities; their existing isolation/source contracts
remain useful foundations.

## Delivery order

Milestones are acceptance gates with no promised dates. The
[project board](https://git.subcult.tv/subculture-collective/mantik/projects/5)
and [native milestones](https://git.subcult.tv/subculture-collective/mantik/milestones)
track this sequence. New planning issues have no automatic pickup labels,
assignment or deadline. Each implementation PR must deliver behavior needed by
its issue; another inspection endpoint is justified only by a concrete acceptance
blocker.

### RC1: Complete one evaluated paper lifecycle

| Order | Delivery issue | Observable result |
| --- | --- | --- |
| 1 | [#282: Economics and applicable evaluation](https://git.subcult.tv/subculture-collective/mantik/issues/282) | Source-bound cash/passive benchmarks, corporate-action treatment, equity/drawdown and all applicable evaluation checks produce reproducible complete/pass, fail or insufficient results. No caller-supplied results or retroactive criteria. |
| 2 | [#283: Paper eligibility and atomic commitments](https://git.subcult.tv/subculture-collective/mantik/issues/283) | Actual retained decision/evaluation/policy/account evidence produces a bounded owner-issued eligibility decision and one durable reservation/commitment under competing capital/exposure limits. Expiry, reassessment and retirement are enforced. |
| 3 | [#284: Ongoing paper effects](https://git.subcult.tv/subculture-collective/mantik/issues/284) | Only allocator commitments can create paper orders/effects. Fills, costs, cash, positions, releases and protection reconcile atomically through partial/unknown outcomes and restart. Missing quotes cannot invent a fill. |
| 4 | [#285: Autonomous input-to-ledger integration](https://git.subcult.tv/subculture-collective/mantik/issues/285) | Owned workers actually collect, dispatch research, evaluate, finalize, admit/defer, allocate, execute and reconcile. Runtime evidence proves scheduling rather than a test harness driving every step; model/feed outages retain safe behavior. |

These issues complete the implementation units underlying the still-open parent
acceptance issues [#19](https://git.subcult.tv/subculture-collective/mantik/issues/19),
[#20](https://git.subcult.tv/subculture-collective/mantik/issues/20) and
[#21](https://git.subcult.tv/subculture-collective/mantik/issues/21). Parent issues
close only after their complete requirement-by-requirement audit. Issue #19 was
reopened: its earlier closure covered deferral-oriented groundwork, not funded
allocation.

The applicable recipe must address leakage, warm-up, out-of-sample behavior,
parameter/cost sensitivity, benchmark comparison, calibration, sample sufficiency
and dependence, and search multiplicity. A `not_applicable` result requires the
compiled method's actual contract. Crossing an occurrence count does not prove
independent samples. Original inputs, failed attempts, exact numeric terms,
source availability and correction/unsupported-action states remain retained.

Three states must stay distinct:

1. **Evaluation complete:** the applicable method and evidence windows have a
   complete retained result, which can pass or fail.
2. **Paper-operationally eligible:** an owner-derived decision permits the
   isolated paper lifecycle under a bounded strategy/account/policy/version and
   expiry. It does not prove profitability.
3. **Qualification credit:** supplied only by the later fresh natural-run report
   when its predeclared criteria are actually met.

Issue #27 cannot be a prerequisite to the paper operation needed to collect its
own prospective evidence. This distinction does not permit a synthetic fixture,
caller assertion or unavailable evaluation to issue a grant.

### RC2: Complete source and operator journeys

| Acceptance issue | Remaining result |
| --- | --- |
| [#13: Verified social intake and dispatch](https://git.subcult.tv/subculture-collective/mantik/issues/13) | Bind an authorized provider/account, retain original posts/edits/deletions/reposts and scoped failures, and dispatch applicable observations into actual owned research/cases. A social claim is not an observed fill. |
| [#14: Verified bounded copy-follow](https://git.subcult.tv/subculture-collective/mantik/issues/14) | Verify source activity, instrument/quantity semantics and delay; derive independently sized owner-bound proposals with common evaluation/admission/protection. Repeated sources, reversals and mismatches cannot multiply exposure. |
| [#23: Conversational direction](https://git.subcult.tv/subculture-collective/mantik/issues/23) | Complete a running-service conversation through existing typed operations: inspect, request research, change authorized watches and perform scoped actions with readback. Finalized-evidence explanations and contradiction reviews cannot change financial authority. Model outage leaves protection running. |
| [#24: Economic and delay attribution](https://git.subcult.tv/subculture-collective/mantik/issues/24) | Trace actual forecast/decision/allocation/execution outcomes and costs against cash/passive baselines, including social/copy delay. CLI/TUI expose uncertainty, missing evidence, abstention and insufficient samples; quote deltas and cost ceilings are not P&L. |

These workflows can be developed alongside RC1 where their owners already exist,
but all four must be accepted before the complete release's natural run. Recorded
manifests, typed tools and deterministic explanations are delivered foundations,
not substitutes for these end-to-end journeys.

### Release, recovery and natural qualification

| Gate | Acceptance issue | Required evidence |
| --- | --- | --- |
| Financial recovery | [#25](https://git.subcult.tv/subculture-collective/mantik/issues/25) | Exact-release backup/isolated restore and interruption drills with actual commitments, orders, fills, positions, cash, protection and unknown outcomes; no duplicate effect or lost obligation. Current nonfinancial-owner restore tests are supporting evidence. |
| Complete isolated candidate | [#26](https://git.subcult.tv/subculture-collective/mantik/issues/26) | All remaining development criteria accepted; exact source/image/schema/config/capabilities, new Mantik-owned data, configured workers, integrated rehearsal, running readbacks, pre-change backup and rollback proof. A build/help preflight is only a sub-gate. |
| Fresh natural window | [#27](https://git.subcult.tv/subculture-collective/mantik/issues/27) | Predeclare the exact release, strategies/evaluator/account/policy/source scope, metrics, baselines/costs, minimum evidence/window and stop criteria. Observe natural behavior; retain failures and report reliability, economics and qualification separately. |
| Verification reliability | [#299](https://git.subcult.tv/subculture-collective/mantik/issues/299) | Local `make verify` and `make test-race` pass while other local processes, such as T3 port detection, connect to loopback test servers. Strict test fakes reject that traffic without weakening boundary assertions. Reproduces on main `1a64b13`; hosted CI is unaffected. Supporting verification only, not release evidence. |
| 1.0 acceptance | [#286](https://git.subcult.tv/subculture-collective/mantik/issues/286) | Audit the complete gates, then publish the release's verified source/artifact identities, acceptance/soak report, capabilities and known limitations. No unsupported profitability or live-admission claim. |

The [paper-soak delivery plan](plans/paper-soak.md) defines the assembled rehearsal
and natural-run evidence. No previous curtailed observation is credited as the
new run. The old overnight schedule stays disabled; this development task has no
inherited overnight cutoff.

## Delivered foundations and historical milestones

The old M0–M7 headings described the original build order. Their issue history is
retained; the active milestone titles now describe the 1.0 exit gates above.

| Foundation | Historical issues | Delivery boundary |
| --- | --- | --- |
| Service, library, corpus assessment, datasets and replay | #1, #3–#6, #28 | Delivered contracts and retained evidence; no automatic market qualification. |
| Durable routing, timers, delivery and capability readiness | #7–#10 | Delivered within their fixture/operational contracts; financial protection still needs #25 acceptance. |
| Price/calendar and news ingestion | #11–#12 | Delivered adapters/recorded contracts; access, cadence and enabled runtime must be verified for the selected release. |
| Structured research, hypotheses, isolated workers and generated candidates | #15–#18 | Delivered recorded/isolated contracts; assembled autonomous dispatch remains #285. |
| Read-only terminal supervision | #22 | Delivered inspection views; conversational direction and economic attribution remain #23/#24. |

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
