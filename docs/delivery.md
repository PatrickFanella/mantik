# Mantik delivery model

## Sources of truth

[The roadmap](roadmap.md) records product direction, sequencing, and milestone
exit gates. Gitea issues own acceptance criteria and implementation evidence.
Native dependencies describe blocking work. The
[project board](https://git.subcult.tv/subculture-collective/mantik/projects/5)
shows current activity; do not copy its changing status into a second tracker.

Architecture decisions belong in `docs/adr/`. Maintained procedures belong in
`docs/runbooks/`. Provider observations, research outputs, prospective ledgers,
release fingerprints, and recovery receipts stay in their designated external
artifact storage with references from the relevant issue.

## Board workflow

- Backlog: planned work whose scope, dependencies, or inputs still need review.
- Ready: bounded work with a selected owner, satisfied dependencies, and an
  executable verification command. Board placement alone does not queue an agent.
- In Progress: active implementation with one accountable owner.
- Review: a concrete PR or evidence package awaiting review or acceptance.
- Blocked: active work stopped by an identified dependency or external condition.
- Done: the issue's acceptance criteria and applicable evidence have been
  accepted; code is merged where required.

A planned dependency is not an incident. Leave unstarted dependent issues in
Backlog. Use Blocked when work has actually begun and cannot progress.

All initial delivery issues are unassigned planning work. They carry
`status:needs-review` and a type label. They have no due dates or automatic
pickup labels. Use the [Open Pilot contract](open-pilot.md) only after reviewing
scope, dependency completion, inputs, and verification. Assignment to
`open-pilot` or adding `ready-for-agent` may authorize automatic pickup.

## Milestone gates

Milestones represent verifiable capabilities, not date promises. Their order
expresses architectural prerequisites. Independent work can proceed in parallel
when its issue dependencies permit it; terminal supervision can start after the
case and exception APIs are available rather than waiting for every strategy.

Closing all issues is necessary but does not by itself satisfy a milestone's
exit gate. Inspect the integrated workflow and its evidence. Keep a gate open
when provider coverage, restart behavior, qualification, or accepted performance
remains unverified. Failed or incomplete research is a valid recorded outcome.

## Issue completion

Before implementation, refine the issue into a small reviewable change while
retaining its intended acceptance criteria. Add acceptance-specific regression
scenarios instead of relying solely on the baseline suite. Update maintained
documentation when contracts or operator behavior change.

A PR should state resulting behavior, concrete verification, material limits,
and the issue link. Hosted CI must be read back for the current commit. A local
pass, pushed branch, merged PR, deployment, and prospective paper qualification
are separate evidence states.

Complete an issue when its own accepted result is delivered. A candidate
implementation may be complete while still unqualified for trading. Preserve
that distinction in its library/evidence status and in later admission gates.

## Safety and scope

Every executable strategy needs complete safety and evaluation contracts.
Operations enforces finalized decisions independently of models. Research and
conversation cannot self-grant capital or rewrite evidence classifications.

Synthetic/stress results do not become scored or prospective evidence. Existing
Augr source can be assessed for reuse, but its services, credentials, and
financial data are outside this plan's development scope. Mantik remains a
paper-only implementation. Alpaca real-money execution is a planned stage after
paper profitability qualification and an explicit bounded live-admission review.
Recording this direction does not activate a live account or authorize orders;
existing paper issue acceptance gates remain unchanged. See
[the planned live stage](roadmap.md#planned-m8-alpaca-live-admission-and-bounded-rollout).
