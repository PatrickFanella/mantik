# Owned paper evaluation assessments

This is the first durable assessment boundary for issues #19 and #21. The
service selects actual retained evidence from a predeclared evaluation plan and
records the result of its compiled necessary checks. It does not complete the
full evaluation recipe or issue admission, execution or qualification authority.

The authenticated service provides:

- `POST /v1/paper-evaluation-assessments`
- `GET /v1/paper-evaluation-assessments`
- `GET /v1/paper-evaluation-assessments/{id}`
- `GET /v1/paper-evaluation-assessments/{id}/corporate-sources`

A POST command contains only `id`, `receiver_id`, `declaration_id` and
`declaration_sha256`. The declaration must belong to the service receiver and
its pin must match the actual stored declaration. Callers cannot provide a
clock, evidence, check results, admission, account selection, balances, snapshot
or effect. Unknown fields, duplicate fields, trailing JSON and query parameters
are rejected. There is no new offline command or financial write route.

The service replays the actual strategy, policy, declaration, finalized-decision
and outcome owners in one consistent snapshot. Issuance takes the existing
collector → state → receiver lock order in PostgreSQL; Memory uses its aggregate
mutex. For original plans, the compiled `serve` owner issues
`mantik-owned-paper-evaluation-assessment-v6` with recipe
`native-predeclared-observed-portfolio-v6`. Records retain the command, service clock,
complete selected evidence, individual check states, native warm-up and
forecast-calibration applicability, a corporate-source custody cut and a SHA256
digest and owner-derived observed portfolio accounting. Existing v1 through v5
records keep their original recipes, bytes and hashes; replay does not attach
later applicability or accounting results. Legacy stores without the compiled
portfolio dependency continue to issue v5 and cannot replay v6 records.

Each predeclared variant receives all eight planned checks. Before the outcome
deadline, sample sufficiency remains `pending`. At or after that deadline, an
observed occurrence upper bound below the predeclared minimum proves the
necessary sample condition has failed. That check is `failed`, and the assessment
is `blocked`. Reaching the count minimum leaves sample sufficiency `unavailable`:
occurrence counts do not establish statistical independence. The compiled native
threshold method's warm-up and forecast-calibration checks are `not_applicable`,
as described below. The remaining five checks are `unavailable` until their applicable evidence and
validated methods exist. An assessment without a proven failure after the deadline is `incomplete`.
Failed and unavailable checks remain inspectable alongside pending attempts.

`evaluation_complete`, `admission_available`, `execution_available` and
`qualification_credit` are always false. Source classes remain explicitly
unqualified. Forward-quote outcomes do not become fills, economic returns,
benchmark results or realized PnL. A blocked assessment is not a retirement
event, and an incomplete assessment cannot authorize an allocation.

## Historical assessment replay

Assessment catalogue reads and exact POST retries resolve each actual assessment
owner's immutable creation epoch. An assessment already sealed by a paper archive
uses its first archive at `created_epoch+1`; an unarchived owner uses the current
native source snapshot. The resolver checks the actual record hash and exact
clock, original archive header/body/membership and the complete original native
decision/outcome catalogues before recomputing the assessment. It grants no
authority based only on a stored result or a self-rehashed summary.

This preserves the original evidence source hash, including issuance-time rows
outside the declared sample window. Later active records and removal of old
paper inputs do not reinterpret an archived assessment. Existing schemas,
recipes, payloads and hashes remain unchanged. All retained assessment owners
are verified before an exact retry returns its original receipt, without reading
a replacement clock. Missing first originals fail instead of selecting later
copies. Reads group owners by their first archive and retain one bounded archive
context at a time, within the existing record and catalogue byte limits.

Corporate-source inspection, page chains and predeclared source selection use
the same assessment replay in the same owner snapshot. They additionally check
the pinned corporate cut against its actual source owner. A legacy assessment
without that cut retains its explicit unavailable status.

Physical paper retirement is still disabled. New assessment issuance still
uses current retained paper inputs; archive-aware selection of all applicable
historical samples and the allocation/admission consumers remain required before
retirement can be enabled. This replay unit does not extend active limits or
qualify an unattended run.

## Retained observed portfolio accounting

Schema 75 is a compatibility fence for v6 records. The migration changes table
metadata and retains historical payloads without rewriting them. The service
injects its compiled evaluator when constructing the store; commands cannot
select a callback, supply books or calculations, or enable financial authority.

For each variant with paired base/stress capital replays, the assessment owner
uses the actual policy opening capital and verified counterfactual entry/exit
books to retain cash, remaining whole-share cost basis, realized net proceeds,
observed bid equity and maximum drawdown over retained event clocks. Spread,
fees, partial positions and shared finite capital remain in the arithmetic.
Entry debits precede exit credits at equal clocks. An unfunded entry fails;
future exit proceeds cannot finance it. Book, input, policy and evidence hashes
bind the calculations to the original owner-selected source cut.

Equity uses usable observed bids within the pinned quote-age limit. A missing or
stale mark leaves equity unavailable. Equal-clock observations use quote-ID order
for a deterministic mark, not an inferred intratick path. An open position stays
open; no terminal liquidation is invented. A fully closed book exposes its
counterfactual closed net proceeds separately from observed marked equity.

Portfolio accounting rejects a study if an entry or exit book contains a quote
received after that quote command's evaluation time. Fill journals can retain
such rejected observations for inspection, but replay validity alone does not
make their prices available for valuation. Rejection returns no portfolio or
benchmark result and preserves the original books. Causal studies retain their
existing method identities and hashes; this guard adds no evaluation or admission
credit.

This accounting does not interpret corporate entitlements, establish complete
coverage, compute passive total return, prove independent samples, or complete
out-of-sample, sensitivity and search controls. Its total-return, evaluation,
admission, execution and qualification flags remain false. Drawdown is an
observed event-path measure and does not satisfy the full evaluation gate.

Exact retries and reads preserve the retained result after later source
publication. Catalogue replay, including allocation, admission-review and
attribution reads, reselects the actual historical sources and reexecutes the
compiled evaluator. A coherently rehashed fabricated result fails replay.
Within one process, a store reuses the evaluator's result when the rederived
inputs hash identically. Replay still reselects and rehashes every source, and
the result must still bind that hash; only the deterministic computation over
identical inputs is skipped. A restarted process recomputes it.
Disposable HTTP tests cover retries, worker recipe identity, PostgreSQL reopen,
tamper rejection, migration failure rollback and closed-database copy/reopen.
These are manufactured fixtures and do not qualify funded recovery or a release.

## Predeclared holdout checks

A plan using `native-threshold-purged-holdout-plan-v2` issues assessment schema
v7 with recipe `native-predeclared-holdout-necessary-checks-v7`. Its retained
`holdout` result binds the declaration and evidence hashes to the actual
strategy horizons, feature input hashes and receipt/admission/publication clocks,
and observed outcome hashes
and availability clocks. Callers cannot submit partition rows, counts or results.
The same owner snapshot derives the result during issuance and catalogue replay,
including allocation, admission-review and attribution reads.

The [prospective plan](paper-evaluation-declarations.md#prospective-training-and-holdout-partitions)
defines the split, embargo and necessary occurrence minima. Straddling horizons,
late-created training labels, quotes carried across the split and embargo rows
cannot count toward the affected partitions. Waiting outcomes are not labels.
A closed partition below its minimum makes the necessary out-of-sample check
`failed` and the assessment `blocked`. Counts meeting both minima leave the check
`unavailable`; no statistical independence or holdout performance is established.
Other failed checks remain failed. Full economic, benchmark and statistical
acceptance is still required before any complete applicable evaluation exists.

The automatic worker derives the v7 identity for these declarations, preserving
old worker identities for original plans. Exact retries return the original
record without sampling a clock. A coherently rehashed horizon or label edit
fails owner replay. Migration 76 changes compatibility metadata without rewriting
historical payloads. These contracts grant no admission, execution or qualification.

## Known source-unavailability leakage

Recipe v4 also fails the necessary temporal-source condition for a variant when
its owner-replayed evidence contains a candidate marked
`source_unavailable_at_decision`. The selector already excludes that row from
sample counts. The assessment now retains `leakage: failed` with reason
`retained_candidate_source_unavailable_at_finalization`, and becomes `blocked`
even before the outcome deadline. An excluded row cannot silently erase a known
failure. Other variants keep their own check results.

This is a failure-only check. An empty sample, excluded abstentions or rows outside
the declaration window, and absence of a known temporal-source failure leave
leakage `unavailable`. They do not establish complete leakage control, training
lineage, independence, or an admission. New v10 allocation comparisons retain
v4 admission readiness with the selected variant's exact failed checks from each
latest owner-replayed assessment at the comparison cut. For selected decision
evidence, a retained necessary failure takes precedence over pending sample
status. Historical v9/v3 comparison/readiness records keep their sample-only
projection and original hashes. Neither format adds a grant, retirement event or
financial effect. See [owned allocation comparisons](owned-allocation-comparisons.md#exact-decision-evidence-in-readiness)
for the selection, replay and schema 62 writer fence.

Schema 61 fences older writers without rewriting earlier assessments. v1-v3
replay and exact retries keep their original check states, bytes and hashes.
Rollback requires the matching verified backup and binary, not marker deletion.

## Native warm-up applicability

The current `native-paper-threshold-v1` predicate compares one eligible quote's
price to the registered fixed threshold. It has no rolling history, indicator
state or learned model. Recipe v2 therefore derives `warm_up: not_applicable`
with reason `compiled_threshold_predicate_has_no_lookback`. This is a method
input requirement, not a measured performance result or a passing full recipe.
It does not depend on having already observed a decision or reaching a sample
minimum, so pending and empty-sample assessments retain the same applicability.

The `warm_up` field records method `native-threshold-stateless-warmup-v1`, zero
required lookback observations, status `not_required`, and the actual registered
strategy records selected by the predeclaration's exact variant IDs and hashes.
Definitions must belong to the receiver, use that compiled method, and have
been registered no later than the declaration. Its own digest and the outer
assessment hash cover these records. An unknown or stateful method cannot
inherit the zero-lookback rule.

Public commands still contain identities only. Callers cannot submit warm-up
results, strategy records or lookback assertions. Catalogue reads and exact
retries rederive applicability from the actual strategy owner under the same
lock or database snapshot. Source-writer clock floors validate the captured
recipe envelope; that envelope is not a substitute for actual owner replay.
Rehashed altered requirements, methods, status, or strategy records fail closed.

Schema 54 is a writer fence for the new recipe. It adds no table and rewrites no
old assessment. A schema-53 binary must refuse the newer database. Historical
v1 assessments remain v1 even when read alongside v2 assessments after restart.
The existing backup/binary rollback contract applies; removing the schema marker
is not a downgrade.

## Native forecast-calibration applicability

The exact `native-paper-threshold-v1` method returns a boolean threshold signal.
It does not emit a probability forecast. Recipe v3 derives
`forecast_calibration: not_applicable` with reason
`compiled_threshold_predicate_has_no_probability_forecast`. This describes the
method's output contract; it is not a measured calibration result, a forecast
quality claim or a passing full evaluation. It does not remove the other
applicable checks or change source classes, sample gates or authority flags.

The retained `forecast_calibration` field uses method
`native-threshold-no-probability-forecast-v1`, output kind
`boolean_threshold_signal`, status `not_applicable`, and each predeclared
strategy ID and exact registered-record hash. The same actual registry replay
that binds the warm-up definition validates these pins before deriving this
result. The nested and outer digests cover the applicability record. Empty and
pending samples do not change the compiled method's output contract.

The service derives this field; callers cannot submit it or assert a forecast
kind. Rehashed output kinds, statuses and strategy pins fail catalogue reads,
exact retries, allocation owner replay and attribution. A future probabilistic
method cannot inherit this exemption and must supply its own calibration method
and evidence.

Schema 55 fences writers that cannot replay v3. It adds no table and rewrites no
historical record. Schema-54 binaries must refuse the newer database. V1 and v2
records retain their original checks and hashes alongside v3 after restart.
Rollback requires a matching verified backup and binary.

## Retention and source ordering

The live [evaluation evidence read](paper-evaluation-declarations.md) can change
as new records arrive. An assessment retains its original complete selection
and replays it against actual owner records at the assessment clock. Later
records are excluded only from historical replay; a new assessment cannot omit
currently retained future-dated source records.

After an assessment is issued, a new declaration, finalized decision or outcome
must have a service timestamp strictly after every retained assessment. The
check runs under the same owner lock as issuance. Equal and earlier timestamps
fail before mutation and leave the source ID reusable. Historical exact retries
keep their original records and do not read a new clock. The ordering rule keeps
later source appends from changing an already issued assessment, including when
issuance races with a source append at the same timestamp.

A new assessment uses a clock at or after the collector source clock and all
previous assessments. The same ID and command return the original receipt with
`duplicate: true`, without consulting a clock. A changed command conflicts.
Catalogue reads and retries validate every retained assessment against actual
sources, including its check states and evidence selection. Rehashing a
fabricated check or omitting evidence does not make it valid.

A SQL or COMMIT failure returns no assessment receipt. A transport failure at
COMMIT may leave the outcome uncertain; read the owner catalogue and retry the
same command identity. Do not create a replacement ID to resolve uncertainty.

Schema 50 adds the assessment table and checksum fence. It changes no existing
assessment, declaration, decision, allocation or operational payload. A schema
49 binary must refuse the newer database. Rollback uses a verified matching
backup and binary; deleting a schema marker is not a downgrade procedure.

The owner retains at most 16 assessments, each at most 8 MiB plus 64 KiB, with a
16 MiB aggregate payload cap. Capacity exhaustion fails closed and does not
evict failed or incomplete attempts. There is no deletion or reset endpoint.

The remaining admission work still includes applicable leakage, warm-up for any
future stateful method, out-of-sample, parameter and cost sensitivity, benchmark, calibration for methods that emit probability forecasts,
independence and search-multiplicity methods; legitimate qualification evidence;
a versioned admission/expiry/reassessment/retirement issuer; and execution-owner
reconciliation with atomic financial postings. Issues #19, #20 and #21 remain
unfinished.

## Allocation owner replay

Allocation comparison writes, catalogue reads and exact retries validate the
retained assessment owner before returning any result. PostgreSQL does this in
the same transaction that selects comparison sources, under the existing write
lock order or read snapshot. Memory uses the same aggregate mutex. Attribution
validates assessments against its already replayed declaration, decision and
outcome catalogues in that snapshot.

The service supplies both compiled decision and outcome evaluators. Internal
storage callers may omit the outcome evaluator only while the assessment
registry is empty. An explicit nil callback, multiple callbacks, a missing
callback with retained assessments, or an evaluator inconsistent with actual
retained outcomes fails closed. Callbacks are trusted service wiring; they are
not public API fields or caller-provided check results.

Rehashed fabricated checks, omitted assessment evidence and other owner replay
failures prevent comparison reads and historical retries as well as new writes.
No comparison receipt is returned on those failures, and retry does not read a
new clock. Restoring valid owner data restores the same original comparison
receipt; adding legitimate later assessments does not change its bytes or hash.

New v8 allocation comparisons now retain compact assessment pins and the complete
assessment-source cut in their versioned proposal context. New assessment clocks
must be strictly later than every retained v8 comparison; new comparisons cannot
predate any actual assessment. Exact retries remain clock-free, and later
assessments do not change historical contexts. The schema-51 startup fence
prevents older binaries from writing across those retained cuts.

These pins do not upgrade an incomplete assessment into an applicable full
recipe or issue admission or financial effects. Older comparison records acquire
no retrospective assessment pins. See [retained assessment context](owned-allocation-comparisons.md#retained-assessment-context)
for the version and replay contract.


Version 9 allocation comparisons also retain [exact decision evidence readiness](owned-allocation-comparisons.md#exact-decision-evidence-in-readiness).
The latest assessment per matching declaration is selected at the historical
comparison clock, with an explicit equal-clock ID tie-break. Readiness preserves
whether the assessment actually selected the finalized decision and its
strategy's necessary sample check. A matching scope alone never supplies a
positive evaluation. Schema 52 fences older writers; no admission or effect
issuer is activated.

[Owned admission reviews](owned-admission-reviews.md) can retain these readiness
outcomes in a service-owned reassessment chain. They grant no admission,
execution or qualification authority and do not turn blocked assessments into
retirement events.


## Automatic necessary-check assessments

`mantik serve --automatic-paper-evaluations` opts the service into retaining the
existing native necessary-check assessment after a declaration's outcome
deadline. It is disabled by default and independent of
`--automatic-paper-outcomes`. Enabling one worker does not enable the other.
This worker neither collects market data nor invokes a model or provider.

A cycle reads the actual declaration owner and waits strictly past the inclusive
outcome deadline. It then selects the declaration ID and hash through the same
atomic assessment owner used by manual requests. The owner samples the live
issuance clock; a rollback before the cycle cut or deadline is rejected without
occupying the automatic identity. Current owner sources are replayed inside the
write, while earlier committed assessments remain immutable.

The reserved ID is `automatic-evaluation-` followed by the hash of the owner
recipe and declaration hash. For original plans the compiled `serve` owner selects recipe v6 and
legacy stores select v5; predeclared holdout plans select recipe v7. Changing the recipe creates a distinct identity and
preserves earlier receipts. Public
assessment POSTs reject that prefix. A committed retry reuses the same record
without sampling a new owner issuance clock. Concurrent cycles cannot create
multiple records, and a lost acknowledgement can be reconciled by reading that
ID and retrying the same owner selection. If an earlier internal producer has
occupied the ID with a pending or differently bound receipt, the worker reports
a conflict and preserves it; it does not overwrite it or treat it as final.

The retained result is still `blocked` or `incomplete`. Necessary sample or known
leakage failures remain failures; unavailable economic, independence and other
recipe components remain unavailable. `evaluation_complete`,
`admission_available`, `execution_available` and `qualification_credit` remain
false. This is automatic retention of the existing necessary checks, not a
complete evaluation recipe, positive admission, retirement or economic effect.

The worker runs immediately on startup and every 30 seconds. Each cycle has a
20-second bound and commits each assessment independently. A source-specific
failure or reserved-ID collision does not prevent later independent declarations
from being attempted. The cycle returns aggregated scoped errors and remains
unhealthy if any failed, while preserving successful commits. Cancellation or a
global declaration-owner failure stops the cycle. Service shutdown cancels the
worker.
Capability `paper_evaluation_assessment` records cycle health, failures, recovery
and stop state separately from outcome collection and protective monitoring.
Its readiness means the worker cycle is available, not that a strategy or
assessment is qualified. Existing fixture-operation readiness does not depend on
this optional worker.


## Corporate-source custody cuts

Schema 72 adds the v5 assessment envelope. New issuance pins every corporate
source retained by this service at the assessment clock, in source sequence
order. `corporate_source_cut` retains the receiver, declaration ID/hash, as-of
clock and each source ID, sequence, record hash, original-response hash and
service retention time. It contains references; originals remain available from
the [source custody owner](corporate-action-source-custody.md).

This is an inventory of reported source custody, not a selection of economically
relevant or complete actions. It chooses no latest revision, merges no pages,
invents no deletion, and establishes no provider knowledge, acquisition
attestation, total-return benchmark or admission. All source-cut authority flags
remain false. Benchmark comparison and the other unfinished checks remain
unavailable even when the inventory is nonempty.

Issuance and source appends share the existing collector/receiver transaction
locks, or Memory's aggregate mutex. A new source must be strictly later than every
retained decision, outcome and assessment and cannot roll back the shared source
clock. It advances that source clock atomically with retention. Equal-clock races
therefore either put the source in the assessment cut or reject its later append.
Exact source and assessment retries remain clock-free and return original bytes.
New assessment issuance also rejects a clock before a source already held by the
owner; it cannot silently omit that source.

Historical v5 replay reconstructs the source prefix from actual owner records at
the retained assessment time. Later appends do not revise the old cut. Missing,
substituted or self-rehashed source pins fail actual owner replay, including
through allocation, admission-review and attribution callers. Clock-floor paths
check retained envelopes only; they do not attest evaluation or economic results.
Legacy v1-v4 assessment payloads keep their old methods, bytes and hashes and do
not acquire a retrospective source inventory. Schema 72 introduced the automatic worker's recipe-v5 identity; schema 75
adds recipe v6 for the compiled portfolio owner. Old worker receipts remain retained.

### Inspecting a retained source cut

`GET /v1/paper-evaluation-assessments/{id}/corporate-sources` resolves an
assessment's custody pins against the actual source owner in one Memory lock or
read-only PostgreSQL repeatable-read transaction. The service replays both
catalogues before returning `mantik-owned-assessment-corporate-sources-v1`.
The response pins the assessment and declaration hashes, assessment clock and
source-cut hash. Each page includes its exact source pin, reported capture and
revision, query contract, process-date range, symbols, evidence class, response
time, cursor metadata and observed record count. The response is bounded to
512 KiB; original response bodies remain in the targeted corporate-source GET.
No caller-selected clock, query parameters or request body are accepted.

Later source appends do not change this inspection. Legacy assessments return
`source_cut_unavailable_for_legacy_assessment` with no pages or source-cut hash;
the service does not construct a retrospective cut for them. A missing assessment
returns 404, and an invalid retained assessment or source fails owner replay.

The status `retained_source_inventory_only` describes custody metadata. It does
not select a latest revision, join pages into a dataset, map process dates to
entitlement dates or establish symbol relevance for an economic event. Acquisition
custody is `caller_reported_response_only_request_and_completion_unavailable`:
the service recording does not contain the journal's retained request/completion
attempts. The authority is `service_owned_assessment_source_inspection_only`.
Provider attestation, complete coverage, economic application, admission,
execution and qualification flags remain false, including for a terminal page.

Imported configured-journal source records remain part of the same custody
prefix. Their versioned record hash also binds the retained acquisition receipt.
Corporate-source inspection includes `acquisition_sha256` and
`service_retained_configured_journal_acquisition` custody for those pages;
legacy caller-reported pages keep their original inspection fields and hash.
This does not change an assessment's recipe or turn custody into a benchmark,
coverage or admission result.

### Inspecting causal corporate page links

`GET /v1/paper-evaluation-assessments/{id}/corporate-page-chains/{source-id}`
inspects one explicit first-page source within that assessment's retained source
set. No caller clock, query parameters or request body are accepted. The service
replays the assessment and source catalogue under one Memory lock or PostgreSQL
read-only repeatable-read transaction before resolving any page.

Schema `mantik-owned-assessment-corporate-chain-v1`, method
`assessment-pinned-causal-corporate-page-chain-v1`, binds the assessment,
declaration and source-cut hashes, assessment clock, root source hash and each
page's source/acquisition pin. Original objects remain available through targeted
source retrieval. The response includes reported query and cursor metadata,
verified acquisition request/availability clocks and observed record counts;
it is bounded to 1 MiB. A response that exceeds the bound fails without truncating
its contents.

A continuation must have the requested cursor, the same journal identity and
full query scope (contract, process-date basis/range, data quality, region,
symbols, evidence class and access reference). Its acquisition request must be
strictly later than the prior page's acquisition completion/availability clock.
Only sources already pinned by the assessment are candidates. Later imports
cannot alter an older inspection. Multiple matching acquisitions remain
`ambiguous_next_page`; the service does not choose a latest revision.

The result preserves `root_unavailable_at_cut`,
`source_cut_unavailable_for_legacy_assessment`, `acquisition_custody_unavailable`,
`next_page_unavailable_at_cut`, `next_page_acquisition_not_causal`,
`ambiguous_next_page`, `overlapping_source_ids`, `cursor_cycle` and
`page_bound_reached` states. A continuation page cannot be selected as a first-page
root. The 32-source owner bound also bounds chain length. Candidate ambiguity
returns source references, and source objects are not merged or deduplicated.
Caller-reported sources cannot supply missing acquisition clocks.

`page_chain_terminated` sets only `pagination_terminated`. The authority remains
`service_owned_assessment_page_links_only`; provider attestation, complete
coverage, economic application, admission, execution and qualification remain
false. The explicit root selects a diagnostic inspection, not a predeclared
economic dataset, benchmark or qualification result. Process dates are not
converted into entitlement dates, absent objects are not interpreted as deleted
or action-free coverage, and original assessments remain unchanged.
### Predeclared corporate source selection

An optional `plan.corporate_sources` array records intended acquisitions before
the observation window. Each item has `symbol`, `journal_id`,
`root_collection_id`, `access_ref`, `start` and `end`. Symbols must be unique,
sorted, and exactly cover the symbols in the retained native variants. Each
query is for one symbol and an inclusive process-date range of at most 31 dates.
Journal and root collection identities use 1–40 ASCII letters, digits, underscores
or hyphens; each journal/root pair must be unique. New declarations require the
`journal-` prefix used by configured acquisition journals. Previously retained
intents without that prefix remain inspectable and exact retries preserve their
original record; they cannot resolve to a configured journal acquisition. This
restriction applies only to new append attempts, without rewriting historical
declarations or changing their replay validation. The access reference is bounded
to 256 bytes and cannot contain control characters. The array accepts 1–10 items.

For example, an item can name `SPY`, journal `journal-study`, root collection
`spy-actions-001`, access reference `paper-research-access`, and process-date
start/end `2026-11-02`. These are intended query parameters, not supplied source
records, acquisition receipts, hashes, results or a claim that the source exists.
New declarations containing this array use declaration schema v3. Declarations
without it keep their v2 representation; legacy v1/v2 payloads and retries retain
their original hashes. An empty array is invalid. A retained declaration cannot
be edited or supplied a replacement source plan after observing results.

`GET /v1/paper-evaluation-assessments/{id}/corporate-selection` accepts no body,
query parameters, caller clock or root selection. One Memory lock or PostgreSQL
read-only repeatable-read transaction replays the actual assessment, declaration
and source owners. The service resolves the predeclared journal/root identities
only within the assessment's source cut, checks the exact single-symbol query
scope and access reference, then derives the causal page chain. Every selected
page request must be strictly after declaration and its verified availability
must be no later than the inclusive outcome deadline. Later imports cannot alter
earlier assessments. Source retention after the outcome deadline does not move
the acquisition deadline or assert prior provider knowledge.

The response schema is `mantik-owned-predeclared-corporate-selection-v1` and is
bounded to 1 MiB; overflow fails without truncation. It binds assessment,
declaration, source-cut and per-plan hashes and retains each inspected chain.
Missing or ambiguous roots, mismatched query scope, acquisitions outside clock
bounds, and the chain's missing/ambiguous/overlap/cycle states remain explicit.
`predeclared_chain_resolved` means only that the intended chain terminated within
those acquisition bounds. Legacy declarations return
`corporate_source_plan_not_predeclared`, without retroactive selection.

Authority remains `service_owned_predeclared_acquisition_selection_only`.
Coverage, economic application, admission, execution and qualification flags stay
false. The compiled corporate query uses `data_quality=all`, US region, ascending
order and process dates. Those dates are not entitlement dates; a terminated
chain is not complete coverage or a correction history. Rates, instrument
mapping, ex/record/payable dates, total-return benchmarks and missing/deleted
actions still need applicable economic rules. Schema 74 is a compatibility fence
for the optional v3 declaration and rewrites no stored payloads.


## Retained observed benchmarks

A prospectively declared `native-threshold-observed-benchmark-plan-v3` plan
issues assessment v8, recipe `native-predeclared-observed-benchmarks-v8`.
The compiled owner derives `base_benchmarks` and `stress_benchmarks` alongside
each available portfolio variant, in the same source snapshot and with its
actual finite opening cash and cost model. The internal accounting selection
preserves legacy v6/v7 source hashes and results. Callers supply no benchmark
book, quote, count, result or coverage assertion. The worker gives the new
recipe its own automatic identity.

Cash uses nominal USD without interest: ending cash equals opening cash and
net return is zero. The passive alternative uses the first usable retained
quote in that model's counterfactual study path as a price reference. Availability
clock and then quote ID fix the ordering. It places
one whole-share limit order one nanosecond after the reference's availability;
the reference cannot fill that order. The fixed limit includes modeled adverse
slippage. Quantity fits opening cash under one-share fee rounding and fixed
commissions, capped by the fill method's share limit. Only subsequent eligible
updates fill the order through the existing shared-occurrence method, retaining
latency, quote freshness, displayed share capacity, partial fills and revisions.
Entry expires at the earlier of 24 hours or the retained source cut. No proceeds
are reinvested, and no terminal sale is invented.

The retained passive book, reference source hash, filled quantity, observed
bid equity and cash differences are reproducible owner results. Missing or stale
terminal marks leave the corresponding difference unavailable. Insufficient
cash, absent later quotes, unfilled/partial orders and bounded source capacity
remain explicit outcomes. Alternative strategy/passive books do not consume
each other's liquidity: they are separate counterfactual studies.

The path is conditional on the selected retained sources and funding, rather
than proof of a full-window buy-and-hold return. Corporate entitlements,
complete-window coverage and total-return reconciliation remain unavailable.
The mandatory `benchmark_comparison` check therefore stays `unavailable`, even
when an observed-price excess is positive. Positive economic acceptance still
requires documented corporate-action coverage and reconciliation evidence,
not REST pagination or these arithmetic differences. Admission, execution and
qualification remain false. Other failed checks remain failed.

Schema 77 fences older writers without rewriting historical payloads. Actual
owner replay reconstructs the source cut, books and benchmarks during issuance,
readback and downstream allocation/admission/attribution. A coherently rehashed
benchmark edit or schema downgrade is rejected. Local fixtures and database
reopen/copy tests remain distinct from deployment and funded recovery evidence.


## Native feature causality

A prospectively declared `native-threshold-causal-feature-plan-v4` plan issues
assessment schema v9 with recipe `native-predeclared-feature-causality-v9`.
The optional declaration field fixes method
`native-threshold-owner-feature-availability-v1` before observations begin.
Existing v1–v8 assessments and their automatic worker identities remain unchanged.

This method applies only to the exact registered `native-paper-threshold-v1`
predicate. Its immutable definition and parameters precede declaration; the
compiled predicate consumes one quote, with no rolling history, training labels
or learned state. Under the assessment owner's existing snapshot, the retained
holdout pins resolve each selected quote to its exact original publication and
maximum receipt/admission/publication availability. Every selected feature must
have occurred within the declared window and been available no later than its
actual finalized decision. The owner rederives these pins on catalogue replay,
including allocation, admission-review and attribution reads.

For a variant with selected research evidence, those validated native inputs
produce `leakage: passed` with reason
`owner_replayed_stateless_native_features_available_at_finalization`. Empty or
excluded-only variants remain `unavailable`. A known
`source_unavailable_at_decision` row still makes that variant's leakage check
`failed` and the assessment `blocked`, even when other rows are causal. The new
check cannot erase a retained failure or treat a synthetic/excluded row as a
selected feature. Unknown or trained/stateful methods cannot use this contract.

This is a passing native feature-use check. Holdout performance, sample
independence, sensitivity, search multiplicity and corporate total-return
coverage retain their separate requirements. The overall assessment remains
pending, incomplete or blocked, with evaluation completion, admission,
execution and qualification flags false. In particular, a causal feature cannot
make an insufficient holdout or unavailable benchmark pass.

HTTP commands still contain only existing owner identities and pins; callers
cannot submit feature clocks, check statuses or results. New automatic
assessments have their own recipe-derived identity. Exact retries remain
clock-free and preserve the original source cut after later publications.
Rehashed feature-publication edits, fabricated check results and schema
downgrades fail owner replay.

Schema 80 fences older writers by updating compatibility metadata; it rewrites
no retained declarations, assessments or source records. Migration failure
rolls back the metadata and marker together. Rollback uses a verified backup
and matching binary in separate resources, preserving the newer evidence;
deleting schema markers from a running database is not a downgrade procedure.


## Disjoint sample design

A plan using `native-threshold-disjoint-sample-plan-v5` issues assessment v10
with recipe `native-predeclared-disjoint-samples-v10`. Its `sample_design` result
binds the declaration, evidence cut, declared method/minimum, variant IDs and
every selected window's decision and feature hashes. HTTP assessment commands
still contain only owner identities and pins; callers cannot supply intervals,
counts, statuses or results.

The design applies to the same fixed native threshold method as feature
causality. It uses the existing owner-replayed holdout pins and actual immutable
strategy safety contract. Each closed interval starts at the selected feature's
occurrence time and ends at decision finalization plus maximum holding seconds
plus `min(60, max_quote_age_seconds)`. This conservatively retains the full
allowed observation window, rather than shortening it after seeing a favorable
outcome. Missing observed labels remain `outcome_unavailable`; an interval ending
after the source cut remains `window_not_closed_at_cut`. Neither can be counted.

For each variant, eligible windows are sorted by earliest finish, then earliest
start and decision ID. Greedy selection retains a maximum-cardinality disjoint
subset. Endpoints are inclusive: a window starting exactly when the preceding
selected window ends overlaps it. A later start must be strictly greater. All
eligible and unavailable windows remain in the result, with
`selected_disjoint_window`, `overlapping_observation_window` or the unavailable
status. Sorting and tie rules make results independent of input ordering.

The design covers selected research episodes across the declared window.
Holdout partitioning remains separate: this count does not move purged rows
into holdout or change its minima. Before the outcome deadline, design status
is `pending`. At and after the deadline, fewer disjoint windows than the
predeclared minimum make the design `failed` and the assessment `blocked`,
including when the raw occurrence minimum was met. Existing necessary failures
keep their original check reasons. Meeting the design minimum leaves sample
sufficiency `unavailable`: nonoverlap does not establish statistical independence,
distributional stability or economic performance.

The result preserves false independence, evaluation-complete, admission,
execution and qualification flags. Feature causality and all economic/statistical
checks retain their own outcomes. Rehashed interval/count edits and method/schema
downgrades fail replay against the actual source owners. Exact retries and
later-source isolation retain the original result. The automatic worker uses
its own recipe-derived identity; legacy identities and v1–v9 results are unchanged.

Schema 81 fences earlier writers through compatibility metadata without rewriting
retained records. Migration failure rolls back its metadata and marker together.
Use the verified backup and matching binary in separate resources for rollback;
keep the newer source and assessment evidence intact.

## Registered search history

A plan using `native-threshold-search-history-plan-v6` issues assessment v11
with recipe `native-predeclared-search-history-v11`. Its `search_history` result
binds the declaration hash, declared bound and assessment clock to every strategy
the receiver registered at or before that clock. Each entry keeps the strategy
ID, record hash, registration time and whether this declaration selected it.
HTTP assessment commands still contain only owner identities and pins; callers
cannot supply the inventory, count, status or result.

The search includes undeclared registered configurations, configurations
declared by other families and registrations after this declaration. Declared
family IDs cannot narrow it. A count above the predeclared bound makes
`search_multiplicity` `failed` with `registered_search_exceeds_predeclared_bound`
and the assessment `blocked`, including before the outcome deadline. The
registry only grows, so a later cut cannot cure the failure. A count within the
bound leaves the check `unavailable` with
`search_count_without_multiplicity_adjusted_statistic`. A count is not a
multiplicity-adjusted test statistic, and searches outside this receiver's
registry remain unobserved.

New issuance rejects a clock earlier than any retained strategy registration,
so it cannot silently omit one. A strategy registration must be strictly later
than every retained v11 assessment. A registration at an equal or earlier clock
returns 409 and leaves its ID available. PostgreSQL serializes both writers on
the receiver row lock; Memory uses its aggregate mutex. Older assessment
versions do not select the registry and do not constrain strategy clocks.

`multiplicity_adjusted`, evaluation-complete, admission, execution and
qualification flags stay false. Other checks keep their own outcomes. Coherently
rehashed omissions and schema downgrades fail replay against the actual strategy
owner. Exact retries and later registrations leave the retained result
unchanged. The automatic worker uses its own recipe-derived identity; legacy
identities and v1–v10 results are unchanged.

Schema 82 fences earlier writers, including strategy writers without the clock
floor, through compatibility metadata. It rewrites no retained record. Use the
verified backup and matching binary in separate resources for rollback; keep
newer source and assessment evidence intact.

## Corporate-action window coverage

A plan using `native-threshold-corporate-coverage-plan-v7` issues assessment v12
with recipe `native-predeclared-corporate-coverage-v12`. Its
`corporate_coverage` result holds, for each declared instrument, the
[window coverage](corporate-window-coverage.md) derived at the assessment clock
from two owners:

- **Alpaca:** each declared root collection is resolved in the assessment's own
  corporate source cut. It is followed with the same page-chain rules as
  [chain inspection](#inspecting-causal-corporate-page-links).
- **Polygon and issuer:** the declared collections come from
  [reference custody](corporate-reference-custody.md), using records retained
  at or before the clock.

The result keeps each chain's status and page pins, the reference record hashes
used, the strategy IDs for the instrument and the reconciliation result. Callers
cannot supply sources, rows or a status.

For a covered window, `corporate_coverage` is `passed` with
`corporate_actions_reconciled_for_window`. Otherwise it is `unavailable` with
`corporate_actions_unreconciled_for_window`, and the result lists the reasons. A
disagreement between providers is not a strategy failure and does not block the
assessment. In v12, `benchmark_comparison` stays `unavailable`. With covered
windows its reason becomes
`reconciled_corporate_actions_not_yet_applied_to_total_return`; v13
[applies them](#reconciled-total-return-benchmarks).
Economic application, admission, execution and qualification flags stay false.

New issuance rejects a clock earlier than any retained reference source. A
reference retained at or before an existing assessment's clock returns 409.
Historical replay therefore keeps the same prefix. A coherently rehashed result
and a schema downgrade both fail replay against the actual owners. Older
assessment versions do not read reference custody.

Schema 84 fences earlier declaration and assessment writers through
compatibility metadata. It rewrites no retained record.

## Reconciled total-return benchmarks

Assessment v13 (recipe `native-predeclared-total-return-benchmarks-v13`)
follows a declaration v10 plan. It derives window coverage first, then gives
the reconciled actions of each covered instrument to the portfolio owner, whose
method becomes `owner-selected-finite-capital-reconciled-total-return-v3`. For
a variant on a covered instrument, the base and stress rows add
`base_total_return` and `stress_total_return`: the
[total-return result](total-return-accounting.md) of the strategy books and of
the passive benchmark book, both with the same actions. Instruments without
coverage get no total-return rows. Observed-price results and benchmarks are
unchanged.

`benchmark_comparison` for each variant is then decided as follows:

- **`passed`** (`total_return_exceeds_cash_and_passive_by_predeclared_minimum`)
  when, in both the base and stress models, the strategy's total-return net is
  at least `minimum_net_excess_cents` above cash (whose net is zero, since cash
  earns no interest) and above the passive benchmark's total-return net.
- **`failed`** (`total_return_excess_below_predeclared_minimum`) when any of
  those four margins is below the minimum. The assessment becomes `blocked`.
- **`pending`** (`predeclared_outcome_window_not_closed`) before the outcome
  deadline.
- **`unavailable`** otherwise, with one reason:
  `observed_price_benchmarks_do_not_establish_total_return_or_corporate_coverage`
  (window not covered), `reconciled_actions_not_applicable_to_total_return`
  (covered values that cannot be applied, such as a payable date before the
  ex-date), `variant_books_unavailable_for_total_return`,
  `strategy_total_return_unavailable`, `passive_benchmark_entry_incomplete`
  (no passive book, or a passive entry that did not fill its whole intended
  quantity and so left most of the benchmark in cash), or
  `passive_total_return_unavailable`.

Margins compare as exact rationals; a strategy net of `1199/4` cents is below a
300-cent minimum. A pass is a declared economic comparison on retained
observations. Admission, execution and qualification flags stay false, and
other failed checks still block.

Replay re-executes coverage and the portfolio owner, so a coherently rehashed
total-return value or check and a downgrade to v12 both fail. The assessment
also refuses total-return rows whose actions hash differs from the covered
instrument's, rows for an uncovered instrument, and a covered variant missing
its rows. Schema 85 fences earlier writers and rewrites no retained record.

## Threshold-neighbor sensitivity

Assessment v14 (recipe `native-predeclared-threshold-sensitivity-v14`) follows
a declaration v11 plan. It derives everything v13 derives, with the same
portfolio method, then decides `parameter_and_cost_sensitivity` for each
variant from the retained strategy total returns:

- **`passed`** (`threshold_neighborhood_meets_predeclared_net_in_base_and_stress`)
  when the center and every declared neighbor have a strategy total-return net
  of at least `minimum_neighbor_net_cents` in both the base and stress models.
- **`failed`** (`threshold_neighborhood_net_below_predeclared_minimum`) when any
  of those nets is below the minimum. The assessment becomes `blocked`.
- **`pending`** (`predeclared_outcome_window_not_closed`) before the outcome
  deadline.
- **`unavailable`** with `threshold_neighborhood_total_return_unavailable` when
  any member lacks an available total return in either model, and with
  `threshold_neighbors_not_declared` for a variant that is not a center.

A member without a modeled book has no total return, even if it simply never
entered. Treating it as flat cash would let a plan pass on neighbors nobody
evaluated, so the check stays unavailable instead. Nets compare as exact
rationals. A pass grants no admission, execution or qualification.

Replay re-executes every owner, so a rehashed sensitivity check and a downgrade
to v13 both fail. Schema 86 fences earlier writers and rewrites no retained
record.

## Holdout returns and day-clustered sign test

Assessment v15 (recipe `native-predeclared-holdout-sign-test-v15`) follows a
declaration v12 plan. It derives everything v14 derives, then decides three
more checks.

### Standalone decision returns

The portfolio method becomes
`owner-selected-finite-capital-decision-total-return-v4`. Besides each
variant's finite-capital book, the owner replays every selected decision
alone: a book with the full opening capital that trades only that decision,
through the same entry, protective-exit and reconciled total-return
accounting. The variant book funds decisions in order until its budget is
used and never reinvests, so later decisions, including most holdout ones, may
never trade there. A standalone book measures each decision on its own.

Each `decisions` row keeps the decision pin, a status, and the base and stress
nets with their total-return hashes. Statuses are `observed_total_return`, a
total-return status such as `terminal_mark_unavailable`, `decision_not_funded`,
`capital_replay_capacity_exceeded` or `instrument_not_covered`. Rows exist only
when the variant's entry replay ran.

### `out_of_sample`

The holdout decisions are the rows of the purged partition marked `holdout`.

- **`passed`** (`holdout_total_return_meets_predeclared_floor_in_base_and_stress`)
  when the sum of their standalone nets reaches `minimum_holdout_net_cents` in
  both models.
- **`failed`** (`holdout_total_return_below_predeclared_floor`) when either sum
  falls short. A partition below its occurrence minimum stays failed with its
  earlier reason. Both make the assessment `blocked`.
- **`unavailable`** (`holdout_decision_total_return_unavailable`) when any
  holdout decision lacks an observed standalone total return.
- **`pending`** before the outcome deadline.

Threshold parameters are fixed before `window_start`, so the whole window is
already prospective. The holdout sum checks that a result is not carried by
the early part of the window.

### `sample_sufficiency`

The units are the selected disjoint windows of the sample design, thinned to
one per New York trading day: the earliest-ending window that starts on each
date. Each unit's stress-model standalone net is positive or not; zero counts
against the variant. With n units and k positive, the one-sided exact binomial
p-value is P(X ≥ k) for X ~ Binomial(n, 1/2), kept as a reduced fraction.

- **`passed`** (`day_clustered_sign_test_significant_at_corrected_alpha`) when
  n reaches `minimum_trading_days` and the p-value is at most alpha divided by
  the plan's search bound. The comparison uses exact integers.
- **`failed`** with `trading_day_windows_below_predeclared_minimum` or
  `sign_test_not_significant_at_corrected_alpha`. A disjoint count below the
  design minimum stays failed with its earlier reason.
- **`unavailable`** (`clustered_window_total_return_unavailable`) when any unit
  lacks an observed standalone total return.
- **`pending`** before the outcome deadline.

The test treats day-clustered windows as exchangeable. The retained
`statistical_tests` object says so with `exchangeability_assumed: true` and
`independence_established: false`. One window per day removes same-day
overlap; it does not prove independence across days.

### `search_multiplicity`

The search history still fails when the registry exceeds the declared bound.
Otherwise, after the deadline, the check passes with
`sign_test_bonferroni_adjusted_for_predeclared_search_bound`: the sign test
divided alpha by that bound, which is at least the number of strategies
searched. Before the deadline it is `pending`.

The `statistical_tests` object is derived from the retained portfolio,
holdout partition and sample design and has no sources of its own. A pass
grants no admission, execution or qualification.

### Completion

After the outcome deadline, an assessment that is not `blocked` becomes
`complete` when at least one variant has every planned check `passed` or
`not_applicable`. Those variants are listed in `complete_strategy_ids`, and
`evaluation_complete` becomes true. Other variants stay incomplete; a
sensitivity neighbor that is not a center, for example, never decides its own
sensitivity check. This is the first recipe that can complete. `admission_available`,
`execution_available` and `qualification_credit` stay false: eligibility and
capital commitments belong to the admission owner (#283). Allocation readiness
accepts a passed sample check but still reports the evaluation gate as
unavailable.

Replay re-executes every owner, so a rehashed decision return and a downgrade
to v14 both fail. Schema 87 fences earlier writers and rewrites no retained
record.

### Known positive fixture

`TestOwnedPositiveEvaluationAndEligibilityHTTP` drives a manufactured plan
through the public handler and actual owners until it completes. A threshold
strategy and two bracketing neighbors fill at a $99 ask and exit at a $100 bid
on five sessions from 2026-11-02, while the passive book ends at a $95 bid.
The test asserts each result exactly:

- 1000 cents base and 980 stress per standalone decision;
- 2000 and 1960 for each finite-capital book, against −8000 for the passive
  book;
- holdout sums of 3000 and 2940;
- p = 1/32 against a corrected alpha of 1/30.

It then admits and reserves a fresh decision. Of two concurrent competing
proposals, exactly one reserves. The Postgres variant replays all of this
after a restart. The captures are synthetic and earn no qualification credit.
The fixture runs in `make verify`, `make test-race` and the main
`make test-integration` run. `BenchmarkOwnedPositiveEvaluation` scales it by
sessions, each adding three decisions and five feed records, and by extra
quotes per session, which add records only.

## Maximum drawdown

Assessment v16 (recipe `native-predeclared-drawdown-v16`) follows a
declaration v13 plan. It derives everything v15 derives, then decides
`maximum_drawdown` for each variant, and only then decides completion.

The measure is the variant book's observed maximum drawdown, as reported by the
owner's portfolio accounting. It is the largest fall in equity, marked at the
observed bid, across the book's retained event clocks. The larger of the base
and stress values counts.

- **`passed`** (`observed_drawdown_within_predeclared_maximum`) when that value
  is at most `maximum_drawdown_cents`.
- **`failed`** (`observed_drawdown_exceeds_predeclared_maximum`) when it is
  larger. The assessment becomes `blocked`.
- **`unavailable`** with `event_marks_unavailable_for_drawdown` when any event
  in either book had no usable mark, since an unseen low could exceed the
  limit. It is also unavailable, with `variant_book_unavailable_for_drawdown`,
  when the variant has no modeled book.
- **`pending`** before the outcome deadline.

The drawdown covers the finite-capital variant book at its retained event
clocks. It is not a continuous intraday path, and it does not include the
standalone decision books. Completion now needs this check too, so the
eligibility issuer accepts complete v15 and v16 assessments. Schema 89 fences
earlier writers.

