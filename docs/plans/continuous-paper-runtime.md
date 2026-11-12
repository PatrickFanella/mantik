# Continuous paper runtime delivery

The owner requested the complete archive, the #285 rehearsal, and the remaining
#24 attribution work on 2026-11-09 UTC. The earlier capacity stretch is merged
in #347. It permits bounded runs; it does not replace this archive work.

## Delivery order

| Unit | Result and boundary | Verification | State |
| --- | --- | --- | --- |
| Rehearsal prerequisites | Read all 2,048 supported publications in PostgreSQL, including exact retries beyond the former 128-record bound. | Reproduce the 129-of-140 truncation, then run Memory/PostgreSQL contracts, `make verify`, race and disposable integration. | #348 merged |
| Rehearsal preparation | Start only new PostgreSQL and Mantik processes; register SPY/IEX declarations; forward advisory requests to Switchyard; interrupt only that connection; retain phases, failure attempts, restart readbacks and exact build/config identities. | Closed-market preflight, proxy outage/recovery tests, then a scheduled regular-session run. | #349 merged; live-feed rehearsal retained from #361 original head; successful model review/recovery still unproven |
| Feed and collector archive | Seal immutable raw publications, admissions, lease commands and their routed inputs together. Keep current leases, latest coverage inputs, pending publications and routing facts available. Preserve exact historical retries and original source bytes. | Cross the active bounds, verify old retries and conflicts, audit seals against original records, fail on missing/changed source, exercise concurrent writes and rollback/restart/restore. | #350 custody, #351 source lookup, #353 prefix points and #354 paper-source checkpoints merged; #355 explicit rotation and archive-aware replay merged |
| Paper archive | Archive finalized decisions, completed outcomes and assessments with their full source dependencies. Retain open execution books, pending outcome windows and still-applicable evaluation inputs. Historical assessment replay must use the original cut. | Compare pre/post-archive replay and attribution, test pending and open references, inspect historical records, inject corruption and failed commits, verify isolated backup/restore. | Paper source checkpoints #354, custody #356, historical owner/scoped custody/assessment replay #359, and scope/paging #360/#362 merged; combined reader work in #367 covers original/native pages, versioned window assessments, original-ID reads and active-owner/review dependencies; allocation history pages, decision references, legacy index maintenance and per-decision contribution totals follow in #377 to #380, and the targeted attribution row reads indexed history; feed rotation, paper archives and custody checkpoints accept approved retirement by owner epoch; schema 112 adds the retirement owner (`POST /v1/paper-record-retirements`) for sealed decisions with their outcomes; complete-catalogue paging and assessment/review retirement pending |
| Continuous retention | Run owner-controlled archival automatically before active limits are reached. Retain advisory review and allocation cross-references too; inspection must page or target history without loading every archived record. | Run beyond collector/decision/review limits with restart and concurrent archival; measure active append/read cost against growing archive history. | Pending |
| #24 attribution | Complete source/copy-delay analysis, applicable signal/forecast-quality readbacks and readable interactive attribution. | Known-result timing/outcome fixtures, no-action and missing-evidence cases, CLI validation, interactive navigation, race and disposable integration. | Readable terminal attribution merged in #352; strategy outcomes, retained evaluation applicability and unique source-delay analysis implemented, with qualification and acceptance audit pending |

Each unit should land as a reviewable feature branch and PR. Merge only after
checks pass on its exact head. Keep local checks, hosted CI, a running-service
receipt and natural qualification as separate evidence. Update this plan as each
unit changes the remaining implementation.

The feed archive unit first delivered immutable source cuts without removing
active copies. Schema 104 adds the complete explicit rotation transaction and
archive-aware window readers. Automatic rotation and archival for other routing
owners remain required; source custody alone does not complete the full archive.

PR #350 contains schema-100 source custody. The source-index unit adds schema-101 lookup
by original command/input/admission/publication identity, atomic source manifests,
upgrade backfill and a full source/index audit. These reads precede pruning;
source-prefix and pending-decision/outcome replay still need archive access
before any active source may be removed. Schema-102 work persists matching v3
prefix points and nanosecond source cuts in the archive transaction, with bounded
historical-cut reads and complete source-backed audit. The subsequent replay
reader can use those points instead of loading unbounded historical feeds.

Schema 103 prepares paper source custody: the feed archive transaction replays
retained decisions and outcomes against original sources and seals their
membership. Inspection rederives the complete closed cut from actual owners;
new source/paper writes cannot backdate into it. Schema 104 uses that custody to
retire feed sources while preserving current coverage, lease proof, unpublished
admissions and routing references. New v4 paper records retain original source
windows across bounded archive segments; existing records remain unchanged.
An explicit full audit replays actual original bytes, including records with
post-retirement source seals. Physical paper archival remains a separate unit.

Schema 105 seals decisions, outcomes, assessments and decision reviews with their
native replay context and immutable original identity metadata. Creation epochs
close actual snapshot membership independently of clocks and prepare bounded
historical owner selection after active records move. New paper/source custody
checkpoints bind the actual archive epoch; earlier envelopes stay unchanged.
This unit stores originals but does not prune active paper records. Assessment,
allocation and review consumers still need archive-aware owner views before
physical retirement can extend their limits. Schema 106 adds indexed owner
intervals and bounded original payload resolution to historical paper/source
checkpoint replay. Its retirement metadata remains immutable; physical removal
and the remaining assessment/allocation/review consumers are separate work.
Source-custody readers now select indexed seals for active owner identities or
the historical owner view rather than loading all historical seals into the
512-record working budget. Existing assessment inspection and exact retries now rederive their original
first-archive native context, including legacy recipes, complete source hashes
and corporate readbacks. New assessment sample selection, allocation/admission
consumers and growing-history end-to-end retirement remain pending.
Schema 107 adds immutable strategy/decision scope and exact decision-clock
metadata to original creation rows, with native-envelope backfill, archive
cross-checks and a window-selection index. Bounded window paging and new
archive-backed assessment samples remain the next consumer work; this metadata
alone does not extend the active paper limit.
Schema 108 adds native byte ordering and bounded private window pages over that
metadata. The reader selects exact declared strategy versions, decision windows
and record availability cuts across active and archived owners. Original payload
pages recheck actual owner metadata and resolve approved retired rows from their
first archives with header/basis integrity, scope and byte-budget checks.
Missing or altered originals fail without partial results. Native source pages replay each selected owner and its actual decision dependencies.
Version 4 evidence and new assessments bind a complete declared window through its
original creation epoch and preserve version 3 complete-catalogue receipts.
Archive, allocation, admission-review and attribution inspection dispatch by the
actual assessment evidence version. Physical retirement, pending/open references
and automatic scheduling remain required; these reads grant no qualification.

## Archive contract

Increasing limits or keeping unbounded JSON in the active aggregate is not an
archive. A sealed record must remain inspectable by its original identity and
hash. Archive moves, owner checkpoints and removal from active state must commit
atomically. No old identity becomes reusable after a move. A failed move retains
the original state. Integrity checks must detect omitted, reordered or changed
records; full audit must re-execute the archived evidence rather than accept a
self-rehashed summary as proof.

Routing inputs are coupled to collector admissions and source-prefix checks.
Never prune one without retaining the other and its exact receipt. Active
decisions and outcomes currently verify original source membership, while
assessments rederive their declared evidence from owner records. Those consumers
need archive-aware reads before any source can leave their active catalogues.
Open protection must remain independent of models and of archival availability.
The existing allocation checkpoints are a useful precedent, but their identity
indexes and complete-history reads also require a growing-history audit.
Targeted comparison inspection now replays its original indexed allocation
segment, including approved retired decision dependencies. Active targets use
their working journal and original checkpoint; unindexed legacy targets retain
the complete-history path. Historical attribution and catalogue paging, compact
checkpoint identities and automatic retention remain separate required work.

Sealed allocation-history pages expose original comparisons and standalone
execution records in committed sequence, with a cursor bound to its initial
checkpoint. Each selected segment replays its native owner inputs and rebuilds
its original end checkpoint. Schema 111 adds complete original comparison/effect
decision references, atomic seal guards and native index checks in the full
audit. Decision-filtered history binds its original receiver, decision and
checkpoint cut; unfiltered v1 envelopes remain unchanged. Decision
contribution pages (`GET /v1/allocation-history/contributions`,
`mantik allocation-contribution`) sum one decision's reserved funding and
execution effects from those indexed segments plus the working journal, and
match the full attribution replay for that decision. The targeted attribution
row (`GET /v1/paper-attribution/{decision_id}`) is built from those segments,
the working journal and the active owners, and equals the catalogue row. The
complete attribution catalogue still loads the whole archive; it now resolves
approved retired decision dependencies and keeps its active rows unchanged
after retirement.
Explicit
`allocation-history-index` maintenance rebuilds missing legacy indexes through
the complete native audit and recorded seal boundaries, atomically with no
change to original record bytes. New checkpoint envelopes are native
reconstructions; already stored envelopes must match exactly. The maintenance
scan takes owner write locks and grows with history. Compact identities and
automatic retirement remain required.

Schema 112 installs the retirement owner. It retires a sealed decision with its
outcomes at the current paper archive epoch once no worker, working allocation
record or custody checkpoint at that epoch still reads it from the active set;
the [reference](../reference/paper-record-archives.md#retirement-owner) lists
the conditions. Nothing calls it on a schedule yet, and assessments and decision
reviews keep their working limits. That is the continuous-retention unit.

Schema changes must preserve existing envelopes and hashes and fence older
writers. Backup includes source owners, archive rows, manifests/checkpoints,
receiver identity and schema history. Rollback uses a matching restored backup
in isolated resources; deleting migration rows or archives is unsupported.

## Rehearsal scope

The scheduled run is Monday, 2026-11-09 at 14:40 UTC / 09:40 New York time.
Use SPY on Alpaca IEX and the existing read-only credential file under
`~/.local/state/mantik/alpaca-access-20261014/`. Never copy its values into Git,
logs or receipts. The owner authorized Switchyard's existing model route for this
rehearsal; no Switchyard service, workflow data or configuration is changed.

A fresh short run may produce entry proposals and deferred allocations without
any eligible funded position. Record that outcome. Do not manufacture a passing
evaluation, weaken minimum samples, import old pilot observations or rewrite a
quote to force execution. Model transport availability and a schema-valid draft
are separate from review quality. Synthetic positive lifecycle tests remain
separate evidence from this live-feed run. Issue #285 is not closed merely by
successful collection or a model outage demonstration.

The first market run stopped because a pending seed capture shared the live
collector journal. The corrected 20-minute run used #361 original head
`3929c3b537acb26d3acbd50c76dd266565a9acaf`, whose CI was pending at launch,
and schema 105. Its retained results include 32 decisions, 19 outcomes, one
blocked assessment and 20 deferred comparisons. Three decisions were retained
during the controlled model outage. Feed pause/resume and restart readbacks were
retained, and owned runtime resources were removed. Other model attempts timed
out; a successful model review and recovery were not observed. The retained dump
was not restored by that run. These observations grant no qualification or
funded-position/profitability claim. See the [actual rehearsal report](https://git.subcult.tv/subculture-collective/mantik/issues/285#issuecomment-49868)
for results and exclusions; the scheduled run is fulfilled and is not repeated
as a preparation step.

## #24 scope

The native threshold emits a boolean predicate, so probability calibration is
not applicable. Present the actual outcome and applicability evidence; do not
invent probabilities to calculate a score. Copy-source timestamps currently
describe post-decision review sources. Delay analysis must preserve that
provenance and distinguish measured latency from a verified causal input or a
counterfactual financial return. Keep insufficient samples, missing observations,
failed workflows and valid abstention visibly distinct in CLI and TUI.
