# Owner terminal book controls

`mantik book` reads a capped local paper book through the configured service.
It shows cash, settled cash, equity, holdings, sleeve ownership and lifecycle,
current protective controls, and the latest finalized review and allocation
reasons. Latest budgets are historical sizing evidence, not current admission.
Missing or stale holding/pending-order marks make equity **unavailable**. Cash
and controls remain visible. All-cash equity is known without market prices.
This view adds no TWR, SPY comparison, daily brief, broker health or qualification
claim. Those have separate evidence requirements.

## Owner boundary

Provision `MANTIK_API_TOKEN` to the service and the owner's terminal through the
existing private environment mechanism. Do not put a token on the command line,
in a receipt source, in a model prompt, or in logs. This explicitly configured
service bearer credential is the owner session for this single-owner deployment;
there is no new login platform. Missing service configuration returns 503 even
in development; missing or wrong bearer auth refuses requests. Model responses,
model/provider/subscription credentials, generic JSON identity fields and direct
PostgreSQL credentials grant no owner authority. Do not distribute the owner
credential to model runtimes. Existing model tools expose no book-control tool.

The CLI accepts a trusted explicit HTTPS origin or loopback HTTP origin, without
URL userinfo, paths, query or fragments. It forwards the bearer header only to
that chosen origin, follows no redirects, bounds response decoding and sanitizes
remote errors. The origin must be selected by the owner. It does not discover an
endpoint from a model response.

The storage API remains a trusted internal boundary for workers and replay.
`capped-risk` can inspect or record a protective observation; it refuses owner
controls before opening a database. The public owner route supports only
`pause_strategy`, `resume_strategy`, `retire_strategy`, `owner_clear_halt` and
`deposit`. Legacy direct-DB control examples must use the authenticated route
for those supported actions. Broader low-level controls remain internal.
Protective workers and reduce-only exits do not need model availability or a
terminal session. Family admission, applicable decisions and safety contracts
remain enforced by the existing submission/storage boundaries.

## Per-fill cash attribution

The same authenticated `book --action show` readback includes `Execution` with
method `retained-broker-fill-cash-v1`. It lists each original broker activity,
owned client/venue order, symbol, side, fractional quantity, price, fee and raw
source hash. The original fill clock and local recording clock stay separate;
rows are displayed in economic-clock order. Allocation, sleeve and strategy
identities come from the retained order, when that order has those identities.
Broker activity IDs retain the journal's nonempty, at-most-128-byte contract;
punctuation and longer IDs are preserved and escaped for terminal output.

Each row shows gross quantity times price and its signed cash movement: a buy
debits gross plus fee, while a sell credits gross less fee. Exact decimal
arithmetic keeps up to 18 places for the product of nine-place quantity and
price. This is USD, not integer cents or rounded share counts. The aggregate
checks opening cash plus retained deposits plus all fill movements against
the replayed book's exact cash. `Cash` retains its existing nine-place display;
`ReconciledCash` preserves the complete exact amount.

`Status` is `no_fills` or `fills_reconciled`. New empty books report an empty
activity list; pending orders do not become fills or costs. The terminal checks
per-fill and aggregate arithmetic before displaying terminal or JSON output.
An older service may omit this section, which grants no evidence of zero costs.
The readback leaves original journal bytes unchanged and performs no broker or
model call. Gross sale proceeds and cash changes are not realized PnL, TWR,
SPY excess, or qualification evidence. Option and Kalshi books keep their own
accounting contracts.

## Confirmed actions and current readback

`book --help` lists every flag. `--action show` is the default; `--format json`
returns the structured view. Mutations require `--action-id` and the explicit
`--expected-version` read and approved by the owner. Strategy controls also
require a retained `--strategy-id`. `--preview` prints the exact terms and their
`confirm-` SHA-256 pin without sending a mutation. Without `--confirm`, the CLI
requires that exact pin followed by Enter. EOF, an incomplete line, `yes`, a
wrong pin, cancelled context or failed input cancels. Automation must supply
the exact printed pin through `--confirm`; there is no default yes.

The pin covers book, action ID, version and every action term. It proves
concrete intent only; bearer authentication is still mandatory. The service
checks both and records the terms inside the immutable journal event. A new
action against a changed book conflicts rather than choosing a newer version
behind the owner's back. Reread, inspect and confirm a new action if desired.
An exact retry retains its original ID, version and terms. Changing a source,
clock, strategy, action or version under that ID conflicts.

The response separates the original `AppliedVersion` receipt from `Current`.
The service reloads after applying, and the CLI independently GETs again before
printing authoritative current state. A later protective halt is shown even
when retrying a previously successful clear. No readback guarantees state will
not change after that read. A lost response is an unknown outcome: inspect and
retry identical terms, never invent another ID to overcome uncertainty. Failed
post-action GET says the action was recorded but current readback is unavailable.

Hard-halt clear refreshes risk from complete current local marks and requires
positive observed equity, then explicitly resets the loss baseline. It does
not remove permanent retirement, kill or reduce-only controls. Resume cannot
undo retirement. New protective observations can halt again. Retire is permanent
for that strategy version; it does not imply immediate liquidation.

## Real deposits

A deposit records an actual owner-attested USD 500 cash receipt, not a transfer
to a broker. Create a separate book with immutable `Config.FundingMode` set to
`"owner_receipts"`, retaining the fixed USD 2,000 opening, USD 500 every two weeks
configuration. Existing `incubation --action init --input` accepts this field in
its reviewed `Config` manifest; `OpenedAt` records the actual opening clock. No
new initialization command or model authority is introduced.

Omitted funding mode and explicit `"synthetic_schedule"` retain automatic
simulated funding for legacy fixtures. `DefaultCappedConfig` still omits the
field and has exactly the old JSON bytes. A receipt-mode book never automatically
credits elapsed schedule slots through `Fund`, `Rebalance` or `Submit`. Without
a confirmed receipt, due slots add no cash. The ledger rejects unconfirmed
scheduled deposits in receipt mode, and the owner deposit API rejects real
receipts in synthetic mode. Funding mode cannot be changed by recreating a book,
even before its first event. There is no automatic conversion or migration of
legacy/mixed books; historical synthetic cash cannot become real cash by changing
a label. A future conversion would need a separately reviewed, evidence-bound
migration. Legacy reads and confirmed controls remain available.

Use the actual receipt mode when initializing a book that will record real cash.
A new mode does not independently verify the declared opening USD 2,000; that
opening declaration must be backed by the owner's actual capital, never invented
historical deposits. Use the due `deposit-YYYY-MM-DD` action ID of one Friday
06:00 America/Chicago schedule slot. Supply `--amount 500`, `--effective-at` with
the actual receipt RFC3339 clock and `--source` with a unique, non-secret receipt
reference. The receipt must occur at or after that slot and no later than the
service recording time. Missed slots are never applied automatically. One
receipt source cannot be reused for another slot. Original scheduled paper
flows stay replayable and are labelled legacy flows without real-cash evidence.
An owner-recorded receipt remains an owner attestation, not independent bank
verification. It never clears a halt or changes account, leverage or risk limits.
The existing opening configuration is not retroactively relabelled real funding.

## Safe terminal walkthrough and human checkpoint

The following runnable walkthrough creates only an in-memory isolated test book
with `FundingMode: "owner_receipts"`,
uses a local fake owner credential and loopback service, and destroys it when the
test exits. It does not read development databases, contact brokers/models or
record a real cash receipt. Its receipt text is explicitly a fixture. It shows
the initial hard halt, requires a concrete pin plus Enter to clear, retries the
same action, independently reads back, and records one fixture receipt. Pause,
retire, refusal rendering and concurrent readback have separate focused tests.

```sh
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go test ./internal/cli -run '^TestOwnerBookTerminalWalkthrough$' -v -count=1
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go test ./internal/cli -run '^TestOwnerBook(PauseRetireTerminalAndConcurrentReadback|CancellationNeverSendsMutation|AuthOriginAndRedirectBoundary|FailedReadbackDoesNotClaimSuccess)$' -v -count=1
GOFLAGS=-p=2 GOMAXPROCS=2 mise exec -- go test ./internal/service -run '^TestOwnerBook(VersionFenceAndPermanentRetirement|ClearReadbackShowsConcurrentRehalt|MissingValuationKeepsControlsVisibleAndRefusesClear)$' -v -count=1
```

The owner's actual terminal walkthrough remains **outstanding**. On a separately
prepared isolated service/book, the owner should inspect `book --id` for their
chosen test book, use the displayed strategy identity and version, preview and
confirm pause, reread, retire and reread, then confirm clear and inspect the
current halt state. Try EOF and a stale version to confirm refusal. A deposit
walkthrough must use fixture receipt evidence only in that isolated test book;
a real-book receipt must correspond to real received cash. Confirm that the
wording and workflow fit the owner's use. Automated agent tests do not establish
that the owner has performed or accepted this checkpoint.

## HTTP contract and integration boundaries

- `GET /v1/capped-books/{book}/owner-view`: bounded owner readback.
- `POST /v1/capped-books/{book}/owner-controls/{id}`: strict bounded JSON
  `OwnerBookAction` (`ExpectedVersion`, `Action`, `StrategyID`, `Amount`, `Source`,
  `EffectiveAt`, `Confirmation`). No identity selector or model reply is accepted.
  The former unconfirmed `{Control: ...}` HTTP body now refuses with 400.
- Invalid/unconfirmed/body-overflow requests return 400; missing owner config
  503; missing/wrong bearer 401; missing book 404; changed retry/version 409;
  invalid economic/strategy/valuation terms 422. Query parameters are refused.

No schema migration is required: optional owner terms are retained in existing
JSON journal payloads, omitted from legacy events. The optional immutable funding
mode is retained in the existing configuration JSON, omitted from legacy configs.
No hardcoded migration maximum is introduced. The checkpoint and assessment
contracts remain independent of owner authority; this adds no admission or
safety-contract exemption. The coordinator owns combined repository verification
and hosted checks. No host/timer/runtime configuration or deployment is part of
this implementation.

## Logical versions after risk checkpoints

Confirmation pins bind the original-command version, including observations
folded into retained risk checkpoints. The 4096 hot-event limit does not cap the
owner's expected version. Replay validates each action against the logical
prefix, and exact retries return the original logical applied version alongside
current controls. A newer halt therefore stays visible after retrying an older
clear. A stale version under a new action ID requires a fresh confirmation.

The human owner terminal walkthrough remains **UNPERFORMED**. Automated terminal,
authentication, checkpoint and retry contracts do not complete that checkpoint.

Bounded owner views use the same original-command verification as operational
SQL reads. A head with publisher-chosen matching hashes cannot rewrite a prior
owner control in the displayed state. The byte budget is checked in SQL before
configuration or head bytes reach the client, and an unavailable or over-budget
read returns no partial owner view.
