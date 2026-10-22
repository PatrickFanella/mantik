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

Exact command retries return the original assessment without reading a new
clock. To assess again after a waiting/missing result, submit a new ID; the
previous result remains visible. Changed content under an existing ID conflicts.
There are at most 64 assessments with a maximum 4 MiB private envelope each.
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

An older schema-39 writer refuses schema 40. Rollback restores a compatible
pre-upgrade backup into separate resources with its matching binary. Do not drop
tables in a running service. Synthetic-clock HTTP and disposable database tests
check the contract; they do not establish natural runtime duration, market
qualification or profitability. Local checks, hosted CI, merge, deployment and
qualification remain separate evidence states.

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
samples. The existing 64-record retention bound remains a release/soak planning
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
