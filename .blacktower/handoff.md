# Handoff: Mantik foundation and delivery plan

Date: 2026-10-13; live repository/Gitea readback at approximately 21:20 UTC.
Worktree: `/home/onnwee/Work/Subcult/mantik`
Local branch at capture: `feat/mantik-bootstrap`
Local HEAD at capture: `17fdcef1160dac961370f93ec1e0d3cb64640f45`
Remote main: `4c8511d1ebac3536c08e92cc33cc86e0af0f8941`
Status: in-progress — foundation merged; subsequent delivery work not started.

This continuation record is published on `docs/mantik-handoff`, based on the
merged main revision below. Its observations describe the capture time; verify
current source and Gitea state before acting.

## Goal and scope

Build Mantik as a new terminal-first autonomous paper-trading research system.
The user supervises an autonomous service and occasionally directs it through
conversation. Mantik replaces Augr conceptually; existing Augr services, data,
and credentials are not part of the current development scope.

The immediate authorized work was bootstrap, roadmap/issues/project planning,
and this handoff. Do not interpret this record as a request to queue every
future issue, deploy a service, or activate live trading.

## Completed and verified

- Repository: https://git.subcult.tv/subculture-collective/mantik
- Template: `project-template-go-worker-postgres-docker`.
- Bootstrap commit: `60d53c714e9417675437eeccfd5495e0589adca2`.
- Roadmap commit: `17fdcef1160dac961370f93ec1e0d3cb64640f45`.
- [PR #2](https://git.subcult.tv/subculture-collective/mantik/pulls/2) is merged.
  Merge commit is the remote-main SHA above. Issues #1 and #28 are closed.
- Exact-head PR CI passed in
  [run 15190](https://git.subcult.tv/subculture-collective/mantik/actions/runs/15190).
- Exact merged-main CI passed in
  [run 15191](https://git.subcult.tv/subculture-collective/mantik/actions/runs/15191),
  context `CI / verify (push)`.
- Local `make verify`, race checks, disposable PostgreSQL contracts, and image
  build passed with Go 1.26.9. The built-image smoke verified authentication,
  CLI ingestion, stop-gap execution, service restart, and identical retry
  readback. No runtime checks were rerun solely to create this document.
- Documentation links and issue/milestone references passed validation.
- No persistent Mantik service was deployed or left running. Temporary test
  containers were removed at the end of bootstrap verification.

The executable foundation has `serve`, `demo`, `status`, and `observe` commands;
typed records; memory/PostgreSQL storage; deterministic evaluation; finalized
actions; and a separate synthetic operations adapter. Ingestion commits the
observation, receipt, events, and aggregate atomically. Identical retries are
idempotent; changed payloads conflict. Schema startup checks version/checksum.

The fixture processes four quotes, enters 10 DEMO units at 100.00, then exits
at the observed 94.00 after crossing a 95.00 stop. It produces ten events and
an illustrative loss of 60.00. Its zero-cost synthetic fill model provides no
strategy qualification evidence.

## Delivery plan and current Gitea state

- [Roadmap](../docs/roadmap.md) and [delivery model](../docs/delivery.md) are
  maintained source documents, now included in main through PR #2.
- [Project #5](https://git.subcult.tv/subculture-collective/mantik/projects/5)
  contains 28 cards: 25 open delivery issues plus the bootstrap issue, PR, and
  planning issue.
- Eight milestones have capability exit gates and no promised due dates.
- Issues #3–#27 have acceptance criteria, verification scenarios, milestone
  assignments, and 53 verified native dependency edges.
- All future issues are unassigned, have no due dates or pickup labels, and
  remain in Backlog with `status:needs-review` plus their type label.
- Backlog column ID: 25; Ready: 26; In Progress: 27; Review: 28; Blocked: 29;
  Done: 30. Backlog is the default column.

A small lifecycle reconciliation remains: milestone M0 (ID 155) is still open,
although its three issue/PR records are closed and merged-main CI passed.
Cards #1, #2, and #28 still sit in Review; Done is empty. Do not repeat the
bootstrap or reopen those issues. Re-read evidence, then reconcile the board
and milestone when continuing delivery management.

## Decisions and rationale

- Keep research, decision-making, and operations distinct. Portfolio allocation
  owns capital; research and conversation cannot self-grant execution authority.
- Use a mixture of established executable rules and LLM investigation. The
  library includes reusable methods, components, tests, execution models, and
  evidence of applicability; it does not label published methods profitable.
- Keep narrative out of evaluation artifacts. Carry structured claims, source
  references, measurements, uncertainty, checks, and candidate actions. Retain
  original narrative sources for targeted interpretation. Produce readable
  explanations after finalization; contradictions open a review case.
- Treat observations, hypotheses, decision cases, triggers, and finalized
  decisions as different objects. Decisions create future obligations.
- Anticipate news, prices, calendars, specific social accounts, copytrading,
  portfolio/execution changes, research milestones, failures/recovery, human
  direction, and expected events that never arrive.
- Distinguish social claims, announced trades, observed fills, and holdings.
  Bounded copying still passes Mantik allocation and safety controls.
- Every executable strategy needs complete safety limits and exit policies.
  Protective operations must not depend on a functioning LLM.
- Use Go 1.26.9/PostgreSQL now. Python tools may run behind a research-worker
  contract. Rust remains an evidence-dependent future choice, not current work.
- Retain synthetic/stress/historical/prospective evidence distinctions and
  failed research attempts. Live trading is outside the roadmap.

## Remaining work

First implementation candidates, after reviewing their exact scope:

1. [#3: reusable strategy/component/evaluation contracts](https://git.subcult.tv/subculture-collective/mantik/issues/3).
2. [#4: recover and assess the original Augr research](https://git.subcult.tv/subculture-collective/mantik/issues/4).
3. #5: versioned observations and point-in-time replay, dependent on #3.
4. #7: trigger definitions and case routing, dependent on #3; then #8 independent
   deadlines/missing events and #9 durable action delivery.

Follow the native issue graph for later work. Terminal issue #22 can begin
once #7 and #10 provide the required case/exception operations; it need not
wait for every research or market capability.

## Blockers, risks, and open questions

No current repository or CI blocker was observed. Future issues still require
scope/input review; do not add agent pickup labels indiscriminately.

The foundation supports one fixed synthetic case. Stop, target, and holding
checks require fresh incoming quotes. Independent timers and missing-feed
monitoring do not exist. There are no real feeds, LLM tools, generated-code
execution, TUI, multi-strategy allocator, or realistic cost/fill models yet.
Status returns the small fixture's full history, so pagination is needed before
continuous ingestion. Integer cents are only an equity-like fixture precision.

The original research corpus was not recovered. Current Augr source contains
strategy catalog, hypothesis, experiment/replay, generative specification,
evaluation, and strategy implementations, but operational completeness and
profitability were not established by this session. Source reference:
`/home/onnwee/.t3/worktrees/augr/t3-f7c89172` (verify it still exists).

Future issues own provider selection/entitlements, baseline methods/datasets,
watched sources, worker isolation/transport, model benefit, evaluation thresholds,
and the deployment/qualification target. Do not fill these gaps with assumed
credentials or existing Augr financial records.

## Key files and unrelated changes

- `AGENTS.md`: project boundaries and required checks.
- `docs/architecture.md`, `docs/roadmap.md`, `docs/delivery.md`: maintained design.
- `docs/runbooks/development.md`: native/container operation and verification.
- `internal/domain/model.go`: current record contracts.
- `internal/workflow/engine.go`: deterministic fixture evaluation.
- `internal/operations/paper.go`: finalized-authority checks and simulated fills.
- `internal/storage/`: transactional persistence and contract tests.
- `internal/cli/`, `internal/service/`, `cmd/mantik/`: application surfaces.
- `migrations/`, `mise.toml`, `Makefile`, `Dockerfile`, `docker-compose.yml`.

At capture, the checkout was clean before creating `.blacktower/handoff.md`,
and no unrelated dirty/staged work was observed. The original local bootstrap
branch tip was an ancestor of merged main. Publication uses a new documentation
branch from merged main; the bootstrap branch has been removed remotely.

Ephemeral planning receipts are in
`/tmp/mantik-planning-20261013T205846Z/`: `plan.json`, `state.json`,
`dependencies.json`, `metadata-readback.json`, and `board-readback.json`.
They may disappear and some pre-merge snapshots are stale; Gitea and source docs
are authoritative. The API helper there uses the installed tea login and stores
no credential values.

## Exact continuation steps

1. Refresh source and hosted state before acting:

   ```bash
   cd /home/onnwee/Work/Subcult/mantik
   rtk git status --short
   rtk git fetch origin
   rtk proxy git log -3 --oneline origin/main
   rtk proxy tea api -i --login subcult-agent /user
   rtk proxy tea api -i --login subcult-agent /repos/subculture-collective/mantik/pulls/2
   rtk proxy tea api -i --login subcult-agent /repos/subculture-collective/mantik/branches/main
   ```

   Require `login: subcult-agent` for writes. Parse HTTP status and JSON shape;
   tea can exit zero on an API error. Re-read current-main commit status through
   `/repos/subculture-collective/mantik/commits/<main-sha>/status`.

2. If merged-main CI remains green and closure state is unchanged, reconcile
   M0 and move #1/#2/#28 to Done. Resolve each record's global `id` from the
   issues API first; the project endpoint uses global IDs, not local numbers.
   Use POST `/repos/subculture-collective/mantik/projects/5/issues/<global-id>/move`
   with `{"column_id":30}` and PATCH
   `/repos/subculture-collective/mantik/milestones/155` with `{"state":"closed"}`.
   Preserve other cards and read back the resulting state.

3. For authorized issue #3 implementation, create a new branch from fresh main
   while preserving this handoff:

   ```bash
   rtk git switch -c feat/library-contracts origin/main
   rtk proxy tea api -i --login subcult-agent /repos/subculture-collective/mantik/issues/3
   rtk proxy tea api -i --login subcult-agent /repos/subculture-collective/mantik/issues/3/dependencies
   rtk proxy mise exec -- make verify
   ```

   Refine acceptance-specific tests before editing. Keep unrelated changes out
   of feature staging and do not queue unrelated backlog issues. Review #4
   separately for read-only recovery.

4. Use the existing Gitea PR workflow for implementation. Commit only reviewed
   paths, ordinary-push, read back the exact SHA, register the PR with T3's
   `link_pull_request`, and distinguish local checks from current-head hosted CI.
   Use T3 `watch_pull_request` if asked to monitor; do not create a polling timer.

Relevant skills for the next session:
`/home/onnwee/.codex/skills/unslop/SKILL.md`,
`/home/onnwee/.codex/skills/implementation-plan/SKILL.md`,
`/home/onnwee/.codex/skills/golang-pro/SKILL.md`, and
`/home/onnwee/.codex/skills/gitea-repo-overhaul/SKILL.md`.
Read global RTK, HOMELAB, and Gitea identity references plus Mantik's AGENTS.md
before workspace work. Keep Gitea's `subcult-agent` identity and preserve dirty
state on every continuation.
