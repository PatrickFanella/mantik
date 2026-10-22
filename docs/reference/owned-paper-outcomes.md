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
