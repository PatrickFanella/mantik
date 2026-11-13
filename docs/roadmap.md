# Mantik roadmap to 1.0

Mantik 1.0 is a complete, isolated paper-research and paper-operation service
supervised from the terminal. It must turn attributed inputs into research,
applicable evaluation, finalized decisions or abstentions, owner-issued paper
eligibility, finite capital allocation, reconciled paper execution, protection
and inspectable outcomes. Social/copy-follow, conversational direction,
attribution and recovery remain part of 1.0.

**Current state, 2026-11-13** (main `1d12217`, schema 115): economics, applicable
evaluation, paper eligibility, atomic commitments and the ongoing paper execution
worker are merged. Their evidence is synthetic fixture evidence. One live-feed
rehearsal on 2026-11-09 produced decisions and deferred comparisons but no
funded position and no successful model review. Continuous retention is the
active implementation unit. There is no accepted complete paper release or
natural qualification run. Successful tests, merged PRs, healthy workers and
closed foundation issues do not establish those outcomes. The
[2026-10-27 delivery audit](research/1-0-delivery-audit.md) records the earlier
baseline at main `897a3b433afc599835ae60b74c4db9166a5ee67c` (schema 74).

Issues, pull requests and the project board moved to GitHub on 2026-11-10 and
were renumbered. Links below use the GitHub numbers. Dated audits under
`docs/research/` keep their original Gitea numbers; each GitHub issue names its
Gitea original in its first line. Three-digit pull request numbers in the plans
(#100–#385) are Gitea history; #1, #2 and #38 onward are GitHub.

## Release definition and scope

The [1.0 release gate, #34](https://github.com/subculture-collective/mantik/issues/34),
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
[project board](https://github.com/orgs/subculture-collective/projects/43)
and [milestones](https://github.com/subculture-collective/mantik/milestones)
track this sequence. New planning issues have no automatic pickup labels,
assignment or deadline. Each implementation PR must deliver behavior needed by
its issue; another inspection endpoint is justified only by a concrete acceptance
blocker.

### RC1: Complete one evaluated paper lifecycle

| Order | Delivery issue | Observable result | State, 2026-11-13 |
| --- | --- | --- | --- |
| 1 | [#30: Economics and applicable evaluation](https://github.com/subculture-collective/mantik/issues/30) | Source-bound cash/passive benchmarks, corporate-action treatment, equity/drawdown and all applicable evaluation checks produce reproducible complete/pass, fail or insufficient results. No caller-supplied results or retroactive criteria. | Closed |
| 2 | [#31: Paper eligibility and atomic commitments](https://github.com/subculture-collective/mantik/issues/31) | Actual retained decision/evaluation/policy/account evidence produces a bounded owner-issued eligibility decision and one durable reservation/commitment under competing capital/exposure limits. Expiry, reassessment and retirement are enforced. | Closed |
| 3 | [#32: Ongoing paper effects](https://github.com/subculture-collective/mantik/issues/32) | Only allocator commitments can create paper orders/effects. Fills, costs, cash, positions, releases and protection reconcile atomically through partial/unknown outcomes and restart. Missing quotes cannot invent a fill. | Open. The 2026-11-08 audit against main `4eade6d` found every development criterion met with fixture evidence; closure is the owner's call |
| 4 | [#33: Autonomous input-to-ledger integration](https://github.com/subculture-collective/mantik/issues/33) | Owned workers actually collect, dispatch research, evaluate, finalize, admit/defer, allocate, execute and reconcile. Runtime evidence proves scheduling rather than a test harness driving every step; model/feed outages retain safe behavior. | Open. Decision, execution and retention workers are merged; continuous retention is in progress ([plan](plans/continuous-paper-runtime.md)) |

These issues complete the implementation units underlying the still-open parent
acceptance issues [#20](https://github.com/subculture-collective/mantik/issues/20),
[#21](https://github.com/subculture-collective/mantik/issues/21) and
[#22](https://github.com/subculture-collective/mantik/issues/22). Parent issues
close only after their complete requirement-by-requirement audit. Issue #20 was
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

Issue #28 cannot be a prerequisite to the paper operation needed to collect its
own prospective evidence. This distinction does not permit a synthetic fixture,
caller assertion or unavailable evaluation to issue a grant.

### RC2: Complete source and operator journeys

| Acceptance issue | Remaining result | State, 2026-11-13 |
| --- | --- | --- |
| [#14: Verified social intake and dispatch](https://github.com/subculture-collective/mantik/issues/14) | Bind an authorized provider/account, retain original posts/edits/deletions/reposts and scoped failures, and dispatch applicable observations into actual owned research/cases. A social claim is not an observed fill. | Open; social worker runtime rehearsed with fixtures on 2026-10-29; no authorized provider account bound |
| [#15: Verified bounded copy-follow](https://github.com/subculture-collective/mantik/issues/15) | Verify source activity, instrument/quantity semantics and delay; derive independently sized owner-bound proposals with common evaluation/admission/protection. Repeated sources, reversals and mismatches cannot multiply exposure. | Open; depends on verified observations from #14 |
| [#24: Conversational direction](https://github.com/subculture-collective/mantik/issues/24) | Complete a running-service conversation through existing typed operations: inspect, request research, change authorized watches and perform scoped actions with readback. Finalized-evidence explanations and contradiction reviews cannot change financial authority. Model outage leaves protection running. | Open; typed tools and explanations exist, no running-service conversation |
| [#25: Economic and delay attribution](https://github.com/subculture-collective/mantik/issues/25) | Trace actual forecast/decision/allocation/execution outcomes and costs against cash/passive baselines, including social/copy delay. CLI/TUI expose uncertainty, missing evidence, abstention and insufficient samples; quote deltas and cost ceilings are not P&L. | Open; the 2026-11-10 audit lists three owner decisions and a missing funded-position runtime receipt |

These workflows can be developed alongside RC1 where their owners already exist,
but all four must be accepted before the complete release's natural run. Recorded
manifests, typed tools and deterministic explanations are delivered foundations,
not substitutes for these end-to-end journeys.

### Release, recovery and natural qualification

| Gate | Acceptance issue | Required evidence |
| --- | --- | --- |
| Financial recovery | [#26](https://github.com/subculture-collective/mantik/issues/26) | Exact-release backup/isolated restore and interruption drills with actual commitments, orders, fills, positions, cash, protection and unknown outcomes; no duplicate effect or lost obligation. Current nonfinancial-owner restore tests are supporting evidence. |
| Complete isolated candidate | [#27](https://github.com/subculture-collective/mantik/issues/27) | All remaining development criteria accepted; exact source/image/schema/config/capabilities, new Mantik-owned data, configured workers, integrated rehearsal, running readbacks, pre-change backup and rollback proof. A build/help preflight is only a sub-gate. |
| Fresh natural window | [#28](https://github.com/subculture-collective/mantik/issues/28) | Predeclare the exact release, strategies/evaluator/account/policy/source scope, metrics, baselines/costs, minimum evidence/window and stop criteria. Observe natural behavior; retain failures and report reliability, economics and qualification separately. |
| Verification reliability | [#35](https://github.com/subculture-collective/mantik/issues/35) | Closed. Loopback test fakes are wrapped with `loopbacktest.GoClientOnly`, so local `make verify` and `make test-race` tolerate T3 port detection. Supporting verification only, not release evidence. |
| 1.0 acceptance | [#34](https://github.com/subculture-collective/mantik/issues/34) | Audit the complete gates, then publish the release's verified source/artifact identities, acceptance/soak report, capabilities and known limitations. No unsupported profitability or live-admission claim. |

The [paper-soak delivery plan](plans/paper-soak.md) defines the assembled rehearsal
and natural-run evidence. No previous curtailed observation is credited as the
new run. The old overnight schedule stays disabled; this development task has no
inherited overnight cutoff.

## Next steps, 2026-11-13

Work proceeds in four tracks. Track A needs no owner input and comes first.

**A. Finish continuous retention (#33).** The retention worker retires decisions,
outcomes and claim reviews and rotates feed sources, but a service running for
weeks still fills two working limits and has never run past the real ones.

1. Retire paper evaluation assessments (working limit 16) once nothing active
   reads them, with the same archive-membership, epoch and custody rules as
   decisions. Until then, assessments cap continuous operation.
2. Let the retention worker trigger on the decision-review count (limit 64), not
   only on decisions and outcomes.
3. Run a disposable PostgreSQL service past every real working limit with the
   worker on, a restart and concurrent archival. Measure active append and read
   cost against archive size. This run is the acceptance check for the unit.
4. Page the complete attribution catalogue and compact the v1 checkpoint
   identities, so that no complete read loads the whole archive.

**B. Owner decisions.** These block acceptance, not implementation:

1. Close #32, or name a missing criterion.
2. On #25: whether fees and executed quantities must be retained separately in
   ledger effects (a schema and replay change), and whether "probability scoring
   not applicable" meets the model-uncertainty criterion.
3. Schedule a regular-session rehearsal that can reach a funded position and a
   successful model review. It supplies the running-service evidence #25, #32
   and #33 still lack. Before it, find why the 2026-11-09 model reviews timed
   out on the Switchyard route.
4. On #14: which social provider and account Mantik may bind. #15 waits on it.

**C. Operator journey (#24).** Conversational direction uses typed operations
that already exist and needs no new provider. It can start once track A no
longer needs the same storage owners.

**D. Release.** After A and B3: funded-state recovery drills (#26), the isolated
candidate with rollback proof (#27), then a fresh predeclared natural window
(#28) and the 1.0 audit (#34).

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
