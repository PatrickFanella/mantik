# Service-owned paper proposals and abstentions

The service finalizes a research proposal from its own retained native strategy
and price/calendar history. It does not accept a caller's decision, account,
balance, quote, evaluation, admission receipt or execution effect. This is an
issue #19 dependency; admission, account allocation and execution remain separate
unfinished owners. A finalized `entry_candidate` is not an executable decision.

## Selection and evaluation

`POST /v1/paper-decisions` takes only an immutable request `id`, expected
`receiver_id`, `strategy_id`, full `strategy_sha256` record pin, and
`registration_id`. The authenticated service selects the actual records and its
clock under storage locks. Requests are limited to 4,096 bytes and reject unknown
fields, duplicate or differently cased names, trailing objects and query strings.

The selected collector must match the strategy's symbol, feed and intended input
class. Evaluation uses the existing server coverage calculation: raw captures,
normalized retained inputs and their admission/publication receipts must agree.
Newer unusable or unpublished evidence suppresses an older usable quote. The
collector lease must be current, regular session open and quote eligible. Existing
coverage limits remain 60 seconds for quote age, 300 seconds for capture age and
86,400 seconds for calendar age. A stricter strategy quote-age limit also applies.
A quote above the native threshold or at/below the safety stop abstains. A usable
quote within the threshold and above the stop yields an `entry_candidate` with
`native_threshold_met_admission_required`.

Finalized records retain the complete selection command, server time, evaluation,
coverage, evidence-basis hash and complete record hash. Authority is
`service_finalized_research_proposal_only`. Admission availability, execution
availability and qualification credit remain false for every result. A hash or
an entry candidate cannot substitute for the future admission/allocation gates.
Prospective input labels and fixture tests do not establish profitability or
qualification.

## Retention and retries

The private retained basis copies the selected strategy, collector entry, relevant
published raw captures, admitted inputs and immutable receipts at finalization.
Reads replay the evaluator at the original time and check source continuity
against actual retained owner records. Historical lease receipts remain available
for that check. Later inputs or lease renewal/stop do not rewrite the earlier
basis; a new request ID evaluates the new state. Source corruption, missing owner
records or a changed result prevents a successful catalogue read or retry.

Exact command retries return the original record without reading a new clock.
Changed content under the same ID conflicts. The full retained prefix is checked
before new writes or retries. There are at most 256 records, each with a maximum
2 MiB retained envelope; capacity errors preserve existing history. No eviction,
replacement or deletion route exists. This bounded research registry must not be
used as an operational ledger or protective-exit queue.

PostgreSQL returns a finalization receipt only after the owner transaction
commits successfully. Statement or commit failures return an error and an empty
receipt. A confirmed rollback retains no new record and leaves the request ID
available for retry. If a transport failure leaves the commit outcome uncertain,
read the retained owner history or retry the exact command with the same ID;
do not infer rollback from the absence of a receipt or allocate a replacement ID.
This acknowledgment rule supplies no admission, execution or qualification.

## Automatic decisions

`serve --automatic-paper-decisions` runs a decision worker that finalizes and
compares without a caller sequencing either stage. It runs immediately and
then every 15 seconds:

1. Every strategy a retained evaluation declaration names, at its current
   record, is paired with each feed registration that matches its symbol,
   feed and evidence class. A superseded strategy record no longer decides.
2. When a pair's registration has published a quote recording no decision has
   seen, the worker finalizes one decision against it. The identity,
   `automatic-decision-` and a hash of the strategy record, registration and
   latest quote, means a cycle without a newer quote retains nothing. Entry
   candidates, abstentions and rejections are all retained, with one exception:
   an abstention for the same reason as the pair's last retained one, within 15
   minutes of it, is not retained again. A changed reason and every entry
   candidate are retained at once.
3. Every entry candidate that no allocation comparison pins gets a comparison
   of its own, `automatic-comparison-` and a hash of the decision. The
   allocation owner reserves, defers or rejects it as for any caller. At most
   one candidate per strategy record, registration and evaluated quote is
   compared, counting comparisons any caller made. A quote published between
   the worker's feed read and finalization can give two decisions the same
   evaluated quote, and this keeps one signal from reserving twice.

Both prefixes are reserved: the decision and comparison routes reject them
with 422. Each commit is atomic, and a failure in one pair or comparison does
not stop the others. Progress, failures and clean stop are reported under
the `paper_decision` readiness scope; the evidence reference names the
failing decision or comparison. Schema 98 fences writers that reject this
worker's health reports.

Retention limits still apply. Decisions stop at 256 and published feed
evidence at 2048 records. Collector admissions (2048) and commands (4096) stop
sooner in practice, after about 2.4 trading days at one quote per 30 seconds,
and nothing is archived yet, so the worker supports a bounded multi-day run; once a
limit is reached, every cycle fails visibly with the capacity error. See
#285 for the retention work continuous operation needs.

## API and terminal client

New finalizations return 201, exact retries 200, invalid commands 422, identity or
capacity conflicts 409, missing selected records 404 and storage failures 503.
`GET /v1/paper-decisions` and `GET /v1/paper-decisions/{id}` replay retained results.
The client verifies strict response envelopes, command/receiver identity and
selected record pins before emitting output. Public envelope validation alone
cannot establish ownership of the private basis.

```sh
./bin/mantik paper-decision-finalize --api-url "$api" \
  --receiver-id "$receiver" --id proposal-001 \
  --strategy-id baseline-v1 --strategy-sha256 "$strategy_record_sha256" \
  --registration-id spy-iex
./bin/mantik paper-decisions --api-url "$api" --receiver-id "$receiver"
./bin/mantik paper-decision --api-url "$api" --receiver-id "$receiver" \
  --id proposal-001 --record-sha256 "$decision_sha256"
```

## Storage and rollback

Memory serializes selection, evaluation and retention under its aggregate mutex.
PostgreSQL schema 38 adds `mantik_paper_decisions`; writes lock the collector,
state and receiver in that order before selecting inputs and retaining a result
atomically. Catalogue reads use a repeatable-read transaction. The bounded JSON
envelope uses `bytea` so JSONB field reordering cannot rewrite the captured basis
hash. Failed inserts roll back without changing economic or research input state.

The migration preserves schema-37 state. Older binaries refuse the new schema.
Rollback uses a compatible private backup restored into separate resources with
its matching binary. Dropping tables in a running service is not a rollback.
Local tests, hosted CI, deployment, runtime and qualification are distinct checks.

The lock order follows both existing feed writers (collector before state) and
capital writers (state before receiver). The PostgreSQL regression holds the
capital owner's state row, observes finalization blocked on that row through
`pg_blocking_pids`, then acquires receiver and commits. Both operations must finish
without deadlock; the former receiver-first order fails with SQLSTATE `40P01`.

## Grounded explanations

`GET /v1/paper-decisions/{id}/explanation` renders a deterministic explanation of
a retained finalized decision. The existing bearer authentication is required.
The endpoint accepts only the selected path identity and rejects query parameters,
including prompts, model selection, replacement actions and caller clocks.

The service first replays the complete decision catalogue against actual retained
strategy/feed records. It then renders the selected finalized envelope. Hash
validation of a caller-supplied envelope alone is insufficient; this route accepts
no imported decision, prose, evaluation, admission or effect assertion.

The response pins the receiver, decision, private evidence basis, strategy record,
collector registration and original finalization time. It includes the retained
action, reason, reference price, stored signal flag and full coverage report with
quote/calendar capture and input identities. `record_path` selects the retained
decision detail. Original source material stays in the existing feed custody; the
explanation does not fetch or rewrite it. A source capture identity is a targeted
inspection reference, not independent evidence credit.

Statements distinguish a research entry candidate from an abstention. A positive
reference price is labeled as USD cents and explicitly distinguished from a fill.
Missing prices are not narrated as zero-price observations. Coverage statements
use only retained statuses; later market observations, lease changes and request
time do not rewrite the original explanation. Unsupported free-form claims or
proposed narratives are not accepted by this endpoint.

The method is `deterministic-retained-decision-facts-v1`, with schema
`mantik-finalized-decision-explanation-v1` and authority
`finalized_research_decision_explanation_only`. Model usage, admission,
execution and qualification flags remain false. No model request or fresh
operational clock is needed. The explanation hash binds all returned facts and
statements, so the same retained decision produces the same response after new
observations arrive.

This is a read-only foundation for issue #23. Conversation-driven research,
authorized watch changes, scoped operator actions and contradiction-review cases
still require typed owner bindings. This endpoint does not implement those actions
or close the issue. It changes no database schema, account, order, operation,
worker or protection state.

Explicit factual claims about these records can be checked through
[finalized decision claim reviews](decision-review-cases.md). Contradictory or
unsupported claims open a review case without changing the decision. Original
prose remains unverified, including when supplied claims match.

## Retained market dimensions

`GET /v1/paper-decisions/{id}/market-evidence` reads the market dimensions behind
an actual finalized decision. It uses the service bearer token and accepts no
query string, body, caller clock, replacement source, quantity or authority.
Memory validates the complete decision catalogue under its aggregate mutex;
PostgreSQL uses one repeatable-read, read-only transaction. Selected source data
is detached before the service inspects it. A missing decision returns 404;
invalid owner history or inconsistent recording dimensions returns 503.

The response pins the receiver, decision hash, basis hash, finalization time,
provider/feed, collector registration, raw capture digest, publication key and
publication/admission fences. Here admission means the collector's retained
research-input receipt, not paper strategy admission. The inspector revalidates
all captured recordings, their normalized inputs and collector receipts. The
selected usable quote must match the decision's input and reference price and
must have been admitted and published by finalization. The bid and ask are
whole cents rederived from original recording bytes. The provider size values
are preserved exactly as `provider_bid_size` and `provider_ask_size`.

Version 3 retains the candidate `session_evidence` introduced in version 2. The service derives it from the exact quote
and owner-retained calendar sources at the immutable finalization clock, using
method `owned-decision-quote-session-at-finalization-v1`. Calendar captures pin
original digests, publication/admission clocks and fences. A calendar available
after quote availability but by finalization can contribute; a later calendar
cannot. A newer admitted calendar with no published original suppresses an older
calendar. Missing, stale, conflicting, holiday and preopen evidence retains the
shared session selector's result; no caller-supplied market-open flag is accepted.
Abstentions omit session evidence. The report does not establish continuous
market coverage, admission or execution authority.
Existing outcome readbacks keep their quote-availability clock and representation.

Version 3 (`mantik-owned-decision-market-evidence-v3`) selects the compiled
[REST share-size contract](alpaca-quote-share-units.md) from the actual retained
recording scope. Supported occurrences and integer ranges expose exact bid/ask
share counts and pin the source document, feed and method. Older/transition-day
quotes and out-of-range values retain original sizes with shares unavailable.
This is a current versioned interpretation of recorded bytes, not a rewrite or
authentication of historical captures. Legacy raw inspection stays unchanged.
A raw digest proves byte identity in this owner history; it does not attest
provider access or qualify the evidence.

This is a historical readback, with no new clock. Later quotes, missing-price
captures and lease changes do not replace the selected finalized basis.
Abstentions return `not_applicable_abstention` and a null quote, including when
a reference price happened to be present. Candidates with documented share quantities return
`dimensions_retained_documented_share_units`; unsupported quotes retain
`dimensions_retained_share_units_unverified`. The report hashes its exact
contents and writes no records, balances or operational state. Admission,
execution and qualification remain false; this does not complete #19 or #20.

Verification covers original-byte/normalized-input disagreement, unavailable and
quarantined quotes, foreign provider and capture identities, future publication
or input admission, complete-catalogue corruption, detached reads, historical
stability, authentication, rejected caller assertions and disposable PostgreSQL
reconnect retention. These are fixture contracts, not live market observations.

## Source custody times

Finalization and historical replay enforce service custody times independently
of the compiled evaluator. The captured collector clock, selected registration,
every retained input admission and every retained raw publication must be no
later than the finalized decision time. Zero custody timestamps fail closed.
Equality is allowed; provider occurrence timestamps remain separate from service
custody times.

A source record's presence in the current owner catalogue cannot make it
available to an earlier decision. Rehashing a captured basis containing later
owner records fails catalogue reads, source reads and exact retries. The same
guard protects the basis used by outcome evaluation. Legitimate later source
appends remain available in their owner while the earlier decision keeps its
original basis and receipt, including after restart.

This is a custody check. It does not establish that the captured basis contains
every historical source record, pass the full leakage recipe, attest provider
quality, or grant admission, execution or qualification. Existing record schemas
and valid historical bytes remain unchanged.

## Windowed decision bases

New `mantik-owned-paper-decision-v3` records no longer copy the registration's
whole feed history. The basis keeps the window an evaluation reads: from the
older of the latest quote and the latest calendar by receipt clock onward, ties
included, with the admissions and routed inputs at least that new. `prefix`
binds the rest: a count and a chained digest over every publication in
`(published_at, key)` order, and the same over every admission with its routed
input in `(at, id)` order, each through the cut.

A read recomputes both digests from the current owner records and checks that
the basis holds exactly the window those records select, so omitting or adding
a source fails as before. Each registration's chain is built once per read, not
once per decision. Records from before v3 keep their full copies and their
original checks. Schema 99 fences writers that cannot read v3 bases.

## Versioned decision source membership

New `mantik-owned-paper-decision-v2` records retain every admitted input and raw
publication for their selected collector registration that was available to the
service at finalization. Replay checks membership against the actual current
owner using admission and publication custody times. Rehashing a basis after
removing an available input or publication cannot pass v2 source readback,
catalogue replay or an exact finalization retry. Later legitimate source writes
remain outside the earlier cut, enforced by the shared source clock fence.

Retained v1 records keep their original envelope, hashes and retry behavior.
Their legacy membership completeness is unproven; reopening does not upgrade
historical decisions. The v2 membership check does not establish complete lease
chronology, provider authenticity, outcome-window coverage, leakage evaluation,
admission, financial execution or qualification.

Schema 57 fences older writers before v2 issuance. The migration updates a
compatibility comment and marker, without rewriting decision payloads. Failed
migration transactions preserve the previous marker/comment and source bytes;
checksum mismatches fail startup. Rollback still requires a compatible reader or
a private pre-upgrade backup restored into separate resources.

PostgreSQL catalogue reads return records only after their read transaction
commits successfully. A failed COMMIT returns an error and a zero catalogue,
matching the source-read and exact-retry acknowledgment contracts. Cancellation
at this boundary does not change retained decisions or confer allocation
authority.
