# Service-owned forward quote outcomes

`POST /v1/paper-outcomes` assesses an actual service-finalized native paper
decision using retained price/calendar publications. The command accepts only
`id`, `receiver_id`, `decision_id` and the full `decision_sha256`. It rejects
caller prices, clocks, metrics, horizons, account balances, admissions and
effects. Requests require service authentication, are bounded to 4,096 bytes and
reject unknown/duplicate fields, trailing objects and query strings.

This is an evaluation dependency for issues #19/#21. It does not complete either
issue. The result is a decision-level quote observation. It supplies no execution
return, realistic fill, transaction cost, target-hit claim, portfolio attribution,
qualification result or paper-admission authority. All admission, execution and
qualification flags remain false, including for prospective input labels.

## Fixed observation contract

The compiled recipe is `native-threshold-forward-quote-v1`. An entry candidate's
horizon is its strategy's maximum holding duration after actual finalization.
The observation window starts at that due time and ends after the smaller of
60 seconds and the strategy's maximum quote age. Abstentions are
`not_applicable`. A new assessment before the horizon is `waiting_horizon`;
after the horizon it is `waiting_price`, or `price_unavailable` once the window
has ended without an eligible observation. Boundary timestamps are inclusive.

The evaluator walks actual server publication times in order, grouping equal
timestamps before selecting evidence. At each time it uses only raw captures
published by then and input admissions retained by then. It reuses server
normalization, coverage conflicts, freshness and regular-session eligibility.
The selected quote must occur within the observation window, satisfy the
strategy's stricter age bound, and have both quote/calendar evidence available
by the window end and assessment time. Later backfilled capture timestamps do
not repair an observation missed at the service. A newer unpublished input
suppresses an older otherwise usable quote. Conflicting equal-receipt captures
remain conflicts rather than selecting a winner by capture ID.

An `observed` result pins the selected normalized input, raw recording digest,
quote occurrence/publication times and the time at which the required evidence
became available. It records entry and later normalized quote prices in cents
and their signed difference. No quantity multiplication, realized return or
trading success is inferred. Later bad captures do not rewrite an earlier timely
observation. Lease liveness is required by the original decision finalizer;
retrospective outcome reconstruction uses actual historical publication custody
and does not require that the lease still be live at assessment time.

## Immutable assessment history

The service selects and copies the actual decision, strategy, registration,
source captures, normalized inputs and receipts under storage locks. Reads check
that decision and source records still belong to the actual owners, then replay
every retained assessment at its original time. A self-hashed supplied envelope
cannot establish service ownership. Corruption anywhere in the retained prefix
prevents successful reads, new writes and retries.

New `mantik-owned-paper-outcome-v3` assessments use the windowed basis described
in [decisions](owned-paper-decisions.md#windowed-decision-bases), extended to keep
everything published from the decision's clock onward, which is what the forward
observation reads.

New `mantik-owned-paper-outcome-v2` assessments also require every input admission
and raw publication for the selected registration that was available by
`evaluated_at`. Replay compares this membership against the actual storage
owners, including sources admitted after the original decision but before the
assessment. Rehashing an envelope after omitting such a source cannot repair it.
Later legitimate source writes do not enlarge the retained observation cut.
This check covers source membership and custody times; it does not prove complete
lease chronology, provider authenticity, continuous price paths or qualification.
Legacy v1 assessments keep their original bytes, hashes and replay contract;
their membership completeness remains unproven.

Exact command retries return the original assessment without reading a new
clock. To assess again after a waiting/missing result, submit a new ID; the
previous result remains visible. Changed content under an existing ID conflicts.
There are at most 256 assessments with a maximum 4 MiB private envelope each.
Capacity errors preserve history; this registry offers no eviction, deletion,
background scheduling or operational availability guarantee.

New assessments return 201, exact retries 200, malformed commands 422,
identity/capacity conflicts 409, missing selected outcomes or decisions 404 and
storage failures 503. `GET /v1/paper-outcomes` lists replayed assessments;
`GET /v1/paper-outcomes/{id}` selects one. These are authenticated service APIs;
there is no offline result-import or balance-writing path.

## Storage and rollback

Memory uses the aggregate mutex. PostgreSQL schema 40 adds
`mantik_paper_outcomes`, a bounded `bytea` record table. Writes acquire collector,
state and receiver locks in that order, validate the full decision/outcome
prefixes, select sources and commit one immutable assessment. Failed inserts
roll back. Reads use repeatable read. Existing schema-39 data and allocation
ledger semantics are preserved.

Schema 58 fences older writers before v2 outcome issuance. Its transaction updates
a table comment and the schema checksum marker without rewriting retained
outcome payloads. Failed migration rolls both changes back; checksum corruption
prevents startup. A rollback requires a compatible pre-upgrade backup and binary
on separate resources.

An older schema-39 writer refuses schema 40. Rollback restores a compatible
pre-upgrade backup into separate resources with its matching binary. Do not drop
tables in a running service. Synthetic-clock HTTP and disposable database tests
check the contract; they do not establish natural runtime duration, market
qualification or profitability. Local checks, hosted CI, merge, deployment and
qualification remain separate evidence states.

PostgreSQL returns a finalization receipt only after the owner transaction
commits successfully. Statement or commit failures return an error and an empty
receipt. A confirmed rollback retains no new record and leaves the request ID
available for retry. If a transport failure leaves the commit outcome uncertain,
read the retained owner history or retry the exact command with the same ID;
do not infer rollback from the absence of a receipt or allocate a replacement ID.
This acknowledgment rule supplies no admission, execution or qualification.

## Automatic sealed collection

`mantik serve --automatic-paper-outcomes` opts the service into automatic
forward-outcome collection. The default is off. No model or provider connection
is used: the worker only reads and validates actual retained owner strategies,
finalized decisions and source evidence through the existing store contracts.

A cycle runs immediately on startup and then every 30 seconds. It considers entry
candidates after the entire original horizon and observation window have closed.
The window endpoint is inclusive, so automatic collection waits strictly past
it. Abstentions and immature decisions consume no outcome slot. A late-starting
worker can still select the earliest eligible evidence actually published inside
the original window; later publication of an old quote cannot manufacture a
successful observation. Missing or unusable evidence produces the retained
`price_unavailable` result, not a fill.

The worker assigns one `automatic-forward-` ID from the immutable decision hash
and compiled recipe identity. This prefix is reserved from new public POSTs;
automatic records remain available through the normal authenticated GET routes.
Multiple cycles, concurrent instances and restarts use the same identity and
atomic store retry contract. Existing records keep their original clocks and
bases. A pre-existing conflicting reserved ID fails visibly without being
overwritten or assigned an alternative ID.

Each cycle has a 20-second total budget and processes the bounded decision
catalogue. Each assessment reads the service clock inside its owner transaction, so a
publication during the cycle does not invalidate a stale cycle-start timestamp.
A clock rollback before the closed-window boundary is rejected before a canonical
automatic record can be committed.
Each assessment commits separately. If a later assessment fails,
earlier committed results remain retained and exact retries recover them.
Capacity failure is visible; the worker never evicts evidence. Automatic and
manual assessments of the same underlying observation are not independent
samples. The existing 256-record retention bound remains a release/soak planning
constraint, not a protective queue.

Worker progress, failures and clean stop are reported under the separate
`paper_outcome_collection` capability. Inspect
`GET /v1/readiness/paper_outcome_collection` or the existing operations view.
Without an observed worker it is not configured; stopped, stale or failed workers
are not ready. This scope measures collection health. It neither grants strategy
qualification nor changes the existing fixture-operation readiness gates.

The worker uses the existing schema-43 outcome and worker-health formats, with no
new table or wire fields. Prior binaries can preserve the retained records, but
do not expose the new startup flag or readiness scope. Stop the new process before
using a retained prior binary without the flag, or restore its pre-change backup
for an exact rollback. Restart/restore evidence must identify the actual binary
and data selection.

Verification covers the actual authenticated finalizer and outcome evaluator in
Memory and disposable PostgreSQL, concurrent automatic collection, deadline and
publication boundaries, unavailable observations, source pins, exact retry,
abstentions and capacity. Worker tests retain a failed cycle and its recovery,
and check cancellation and stopped health. This is automatic decision-outcome
collection for #21 and a dependency for #19; applicable admission recipes,
retirement, funded allocation and atomic execution/effects remain unfinished.

## Replay cost and evaluation reuse

A catalogue read still validates every retained decision, outcome command,
source basis, result and hash against its actual owners. Within that single
read, repeated pure native evaluations of a completely equal observation basis
and evaluation clock may reuse the derived value. A digest only selects a
bucket; full value equality is required before reuse. Distinct clocks or bases
remain separate, even when JSON encodes them identically. Callback inputs are
detached, failed evaluations are not cached, and the cache ends with the read. Retained cache inputs total at most 4 MiB
of serialized basis/clock bytes; inputs beyond that budget are evaluated
without retention.

Repeated schema/basis/clock populations can also reuse a successful native record
construction within that read. The record cache requires full detached input
equality and the same command scope; only the outcome request ID may differ.
Each row gets its own checked command, recomputed envelope hash and validation
against its retained record. Actual source custody is checked before this reuse.
Unique populations use the ordinary constructor. Record-cache keys and templates
have a separate 4 MiB serialized budget; noncanonical, oversized or failed inputs
are not retained. All caches end with the catalogue read.

Each retained record remains independently validated and retains its original
bytes, ID, timestamp and evidence class. The reuse does not consolidate outcome
attempts, grant sample independence, change the five-second HTTP deadline, skip
source-custody checks or issue admission. This reduces repeated replay work; it
does not establish the cause of any earlier hosted HTTP 503 by itself.

Historical basis validation also checks that the embedded feed receiver matches
the decision's receiver. Altering that receiver and recomputing record hashes
cannot transfer source custody, including for an abstention whose evaluator
does not inspect prices.

## Retained quote observations

The [outcome market-evidence read](owned-outcome-market-evidence.md) projects
bid/ask dimensions and explicit quality gaps from an outcome's retained owner
basis. It preserves separate occurrence and availability clocks without
inferring share units, continuous coverage, fills, costs or qualification.

PostgreSQL outcome catalogue reads return records only after successful COMMIT.
A failed read COMMIT returns an error and a zero catalogue, matching source reads
and exact retries. Retained outcome history remains unchanged; this rule supplies
no admission, execution or qualification authority.

Within one owner-catalogue read, successful source-custody checks may be reused
for an exactly equal decision command, observation basis, historical clock and
source-membership mode. The current strategy/feed snapshot is fixed for that
read. Each row still checks its decision identity, receiver, retained basis,
record envelope, result and hashes. Errors are never reused, keys are detached,
full Go equality guards digest buckets, and retained serialized keys are capped
at 4 MiB. A new read starts with an empty cache; v1 and v2 source-membership modes
cannot reuse each other's result. Historical bytes and API responses are unchanged.

The authenticated full-capacity catalogue path can be measured without external
requests using the retained local lifecycle fixture:

```bash
mise exec -- go test ./internal/cli -run '^$' \
  -bench '^BenchmarkOwnedPaperOutcomeCatalogueHTTP$' -benchtime=3x -benchmem
```

This benchmark measures replay cost, not provider behavior, execution performance
or qualification. Request deadlines, retention limits and financial authority do
not change.
