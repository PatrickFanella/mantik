# Terminal supervision acceptance

Audited on 2026-10-19 against source
`393cc1fb44f41bbd4d5bf915c1fcafaf2458acb2` (schema 29), after the portfolio
view and activity/case response validation merged. This audit supports closing
[#22](https://git.subcult.tv/subculture-collective/mantik/issues/22) for the
current paper-fixture application's read-only terminal supervision contract.
Dependencies #7 and #10 were read back as closed during the audit.

The interface uses line commands followed by Enter. It does not require a
full-screen terminal or change terminal mode. Each process selects one view
with `supervise --view VIEW --limit 10`; `activity` is the default. Details are
structured JSON, with stored text escaped before display.

## Requirement audit

| Issue acceptance criterion | Implemented behavior and evidence | Assessment |
| --- | --- | --- |
| Activity, cases, research/library, portfolio and exception views with keyboard navigation and compact status | Six views: `activity`, `cases`, `news`, `library`, `portfolio`, `exceptions`. Current-source rehearsal exercised all six against an owned PostgreSQL service. Separate nonempty news journeys exercised case selection, excerpts, explicit original reads, review history and attempt artifacts. | Delivered for current fixture/research contracts. |
| Drill into structured evidence, checks, finalized safety conditions and outcomes | Activity `d SEQUENCE` prints the retained audit event. The current-source journey inspected two finalized decisions, including observation identity, evaluation checks, safety and validity, and two execution outcomes. Case details showed configured strategy plus entry/exit outcomes. News reads expose retained source material and review artifacts explicitly. Library details expose versioned method definitions and required checks. | Delivered as structured inspection. Configured safety is distinguished from a finalized decision; source presence is not qualification. |
| Stream or paginate without unlimited history | Activity and exceptions retain one page of at most 100 records. Cases retain one bounded catalogue: one fixture and at most 260 routing cases. News retains at most 128 cases/128 recordings; explicit review history and attempt reads have 1 MiB/4 MiB response caps and retained-history capacity limits. Library permits at most 16 methods. Portfolio retains at most 256 compact commitments and enforces 256 KiB capital/16 KiB funding response caps. | Delivered. Bounded catalogues use local pages; audit history is fetched by cursor. No supervisor view requests the full status/event aggregate. |
| Service continues after terminal disconnect; reconnect and stale behavior explicit | Current-source native terminals exited through quit, EOF and SIGINT. The same independently running worker closed the fixture position while no viewer was connected. A new viewer saw the closed case and balances. Transport failures, corrupted capital and unknown endpoints exercised stale handling and recovery. Current regressions also cover malformed activity/case readbacks. | Delivered on owned disposable resources. This does not claim continuous health from an on-demand readback. |
| Mutations use shared application operations | The six supervisor views are read-only and sent only GET requests. Funding, explicit fixture binding and quote submission in the rehearsal used existing public CLI application commands, separately from viewers. Viewing, paging, details and exit did not submit an action, recovery, admission or model request. | No mutation authority is introduced by this interface. Interactive direction remains separate work. |

Detailed command and stale-state contracts are maintained in
[activity](../reference/activity.md), [cases](../reference/cases.md),
[news](../reference/news-cases.md#interactive-news-supervision),
[library](../reference/library.md), [portfolio](../reference/portfolio.md) and
[exceptions](../reference/exceptions.md).

## Current-source runtime evidence

All four successful rehearsals used the same CLI binary SHA-256:
`ea8ee717ac6a2167352abb0ce904ffafb8170e32db0d59c6a2860215c25ff5f3`.
Private evidence remains outside Git under the operator's Mantik state root.
Each named run retains source/binary provenance, terminal output, request traces
and a result receipt; cleanup receipts record owned processes/resources stopped.

| Retained run directory | Scope and observed result |
| --- | --- |
| `terminal-acceptance-rehearsal-1792453178518376669` | New owned PostgreSQL container and native service; eight PTY viewers plus an EOF viewer; all six views; 33 viewer GETs. Two finalized decisions/two outcomes inspected. Entry and fresh-price exit used independent workers. Cash 94,000 cents and synthetic realized PnL -6,000 cents after the exit. Paging/details sent no extra requests where local navigation is promised. |
| `news-supervision-rehearsal-1792453198408444805` | Current handler with recorded synthetic news; seven journey GETs, including one explicitly selected original. Off-page/unselected reads blocked; stale recovery, SIGINT and EOF passed; catalogue unchanged. |
| `news-supervision-reviews-rehearsal-1792453184729308058` | Five journey GETs, including three review-history reads. Completed/pending records and history identity checks exercised; stale recovery and exit passed; catalogue/history unchanged. |
| `news-supervision-attempts-rehearsal-1792453184729201348` | Eight journey GETs, including four attempt requests. Three explicit artifact readbacks, including applicable/inapplicable results; failed request required refresh; catalogue/history unchanged. |

The PostgreSQL rehearsal compared capital readback, immutable funding, case,
strategy, binding, action items, fixture effects, execution-completed audit
events and observation count before/after viewer activity. These financial and
execution records were unchanged by inspection. Operational heartbeat metadata
was not treated as immutable while workers ran. The original three pilot and
two Switchyard container PID/image handles were unchanged. No existing service
HTTP request or provider/model request was made by these rehearsals.

The nonempty exception navigation/recovery-history journey is retained as
`exception-view-terminal-result.json` with its transcript in the private
`autonomous-pilot-20261014` evidence directory. Current-source CLI regression
tests verify its paged validation, stale blocking, exact retry and retained
detail behavior. The combined PostgreSQL run independently exercised an empty
exception page; it did not inject an operational exception to manufacture a
nonempty result.

## Failed attempt and verification limits

The first current-source original-news rehearsal,
`news-supervision-rehearsal-1792453184729218148`, failed its cancellation
assertion. Its harness sent SIGINT then immediately called `communicate`, which
also closes stdin. It did not isolate cancellation from the supported clean EOF
path, and did not retain the cancellation return code. Empty cancellation stderr
and the stopped fixture receipt are preserved with the failed-attempt
classification. The corrected harness held stdin open until the process exited;
the fresh run listed above passed. No application change was made for that
attempt, and it is not counted as passing evidence.

`mise exec -- make verify` and `mise exec -- make test-race` passed at the
audited revision. The PostgreSQL native rehearsal used a new disposable
container; existing databases were not used. This audit adds documentation only,
so it does not require a new schema integration run or container image build.
Hosted PR-head CI, merged-main CI and deployment remain separate proof states.

This closes inspection scope, not general competing-proposal allocation (#19),
realistic paper fills (#20), automatic strategy qualification (#21),
conversational direction (#23), performance attribution (#24), or the full M6
exit gate. Portfolio amounts and PnL here are synthetic, zero-cost fixture
results. No strategy is qualified and no profitability is established. Existing
pilots were not upgraded. The earlier user-curtailed soak remains about
10 hours 30 minutes; its original full 24-hour criterion remains unproven.
