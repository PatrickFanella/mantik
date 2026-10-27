# Typed supervision calls

Authenticated conversation adapters can call a fixed set of in-process service
owners through `POST /v1/supervision/calls`. `GET /v1/supervision/tools` lists the
available tools and argument contracts. Both use the service's existing bearer
authentication. They make no network or model requests.

This HTTP adapter and the stdio MCP bridge below are bounded parts of #23.
Conversation UI, model interpretation of prose and review-case triage remain
open. Existing strategies and protection do not depend on these adapters.
Issue #23 remains open pending its remaining acceptance checks.

Each call is one JSON object with exactly `tool` and `arguments`. Arguments must
be an object. The complete call is at most 20 KiB; research, watch, inspection and
recheck arguments are at most 4 KiB, and claim-review arguments at most 16 KiB.
Unknown or duplicate fields, trailing JSON, query strings, unknown tools and
unavailable write owners return 422 before dispatch.

| Tool | Arguments | Owner |
| --- | --- | --- |
| `inspect_record` | `kind` and `id`; kinds `outcome`, `outcome_market_evidence`, `evaluation_declaration`, `evaluation_evidence`, `evaluation_assessment`, `decision`, `decision_explanation`, `decision_market_evidence`, `decision_attribution`, `allocation_comparison`, `admission_review`, `review_case`, `news_record`, `research_review`, `watch`, `copy_watch`, `social_record`, `copy_record`, `social_research_request`, `social_applicability`, `copy_applicability` | Existing targeted GET route for that kind |
| `request_social_research` | `id`, `receiver_id`, `capture_id`, `capture_sha256`; exact retained source, current research watch | `POST /v1/social-research-requests`; pending request only |
| `request_research` | `receiver_id`, `request_id`, `case_id`, `version`, optional `type_id` | `POST /v1/news-reviews` |
| `change_watch_policy` | Existing policy command: `id`, `receiver_id`, optional `previous_sha256`, `policy` | `POST /v1/social-watches` |
| `change_copy_watch_policy` | Same policy command; research, review or inactive bounded follow intent | `POST /v1/copy-watches` |
| `recheck_exception` | `exception_id`, `request_id` | Existing recovery route with operation fixed to `recheck` |
| `review_decision_claims` | Existing `id`, `receiver_id`, `decision_id`, `decision_sha256`, optional `explanation`, `claims` | `POST /v1/paper-decision-reviews` |

For example, request research against a retained news case and its current
evidence version:

```json
{"tool":"request_research","arguments":{"receiver_id":"receiver-abcdefghijklmnop","request_id":"operator-review-1","case_id":"news-case-example","version":1}}
```

The example IDs must be replaced with actual owner records. This starts a retained
review intent; it does not complete a review, invoke a model or qualify a strategy.
Inspect the returned review using its request ID:

```json
{"tool":"inspect_record","arguments":{"kind":"research_review","id":"operator-review-1"}}
```

IDs contain only ASCII letters, digits, underscores and hyphens, begin with a
letter or digit, and are at most 160 characters. The target owner may impose a
stricter limit. Missing or unsupported records keep that owner's error status.
`copy_watch` reads the current copy watch. `social_applicability` and
`copy_applicability` read the existing targeted source-applicability route at the
service clock, including policy changes and source revisions. Their IDs cannot
supply paths, query strings or an alternate evaluation time.

`decision_explanation` reads `/v1/paper-decisions/{id}/explanation`, a
deterministic explanation of the actual finalized decision. It invokes no model
and accepts no caller prose. `decision_market_evidence` reads
`/v1/paper-decisions/{id}/market-evidence`, which replays the retained source
owners and exposes the recorded quote dimensions and provenance. The original
unqualified evidence class and unavailable share-quantity semantics remain
visible; a raw size is not asserted to be shares or execution liquidity.

These inspections forward empty-body GETs with no inherited chunked framing.
They accept only `kind` and `id`, preserve owner errors, and neither finalize a
decision nor change admissions, account balances or operations. The stdio MCP
schema exposes the same read-only selections. An unavailable source owner
returns its error; an absent optional owner retains its 404 response.

Tool calls return the owner's status and JSON body directly, including duplicate
receipts, receiver conflicts, evidence-version conflicts, capacity failures and
storage failures. Owner request IDs and watch-policy compare-and-swap checks
remain authoritative; this adapter has no separate retry journal or clock.

Watch changes allow only `research` and `decision_review` behavior. See
[social watch policies](owned-social-watches.md) for policy fields and immutable history.
The separate `change_copy_watch_policy` tool also accepts `bounded_paper_follow`
with all four independent follow bounds. Its MCP schema requires those bounds
only for bounded follow; the service owner checks order/exposure limits, receiver,
request identity and the current predecessor. The existing social tool remains
restricted to research and review. See [copy-source records](owned-copy-sources.md).
These are inactive policy declarations, with no sizing, reservation, admission,
execution or qualification. A configured-rule activation cannot be requested
through either tool. Exception
rechecks retain an attempt and inspect existing state; the tool cannot select
`reconcile-action`, `retry-protection` or a caller-defined operation. A changed
request with an existing owner request ID still conflicts.

[Decision claim reviews](decision-review-cases.md) preserve the original explanation
and compare explicit typed claims with retained finalized facts. They can open a
contradiction case but cannot change the decision, supply admission, allocate
balances, issue effects or establish qualification. This tool does not verify
arbitrary prose or infer omitted claims. No tool accepts arbitrary URLs, headers,
paths, quotes, balances, admission assertions or caller-supplied review results.

## Stdio MCP bridge

`mantik supervision-mcp --api-url http://127.0.0.1:40880 --receiver-id RECEIVER`
serves the existing supervision tools through standard input/output. Build the
binary with the pinned toolchain and configure the conversation client's stdio
MCP connection to launch that binary and those arguments. It inherits the
existing `MANTIK_API_TOKEN` environment variable; no token is accepted as a
command-line argument or printed. No client registration, runtime service or
credential configuration is changed by adding this command.

The bridge implements the pinned [2025-06-18 MCP lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle)
and [stdio framing](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports):
individual UTF-8 JSON-RPC messages separated by newlines. Initialize first, then
send `notifications/initialized`. The bridge advertises only tools, with no
list-change notifications. `ping`, `tools/list` and `tools/call` are supported;
unknown methods fail. It does not implement an HTTP MCP endpoint, prompts,
sampling, resources, batch messages or a model/chat client.

At initialization, it authenticates against the chosen HTTP origin, verifies
`/v1/research-receiver` against the required receiver ID and discovers the actual
available supervision tools. Tool schemas describe the existing owner command
fields; unavailable tools are absent. The social watch schema allows research
and decision review; the separate copy schema also describes bounded follow
intent with required limits. Each call rechecks receiver identity before forwarding exactly once
to `/v1/supervision/calls`. This receiver check is an HTTP preflight, not an
atomic lease on a remote process; mutations still require the owner's receiver,
request ID, evidence version and compare-and-swap checks as applicable.

Tool results contain a text JSON object with `http_status` and the owner's
unchanged JSON `body`. HTTP failures set `isError: true` and preserve the owner
error. Transport failures, cancellations and malformed/oversized responses
also return tool errors, explicitly leaving mutation outcomes uncertain.
There is no automatic retry: inspect the relevant owner record before deciding
whether to retry the same request ID. Client cancellation propagates to the
in-flight HTTP request; it does not roll back a mutation that already committed.

Frames are limited to 24 KiB, HTTP responses to 2 MiB and requests to ten seconds.
At most eight tool calls can be pending. Active duplicate JSON-RPC IDs and calls
beyond that bound fail before dispatch. Request IDs are nonempty strings or
signed 64-bit integers. Malformed/duplicate JSON fields, unsupported cursors and
invalid initialization fail; an oversized frame closes the transport with an
error. EOF drains accepted calls; process cancellation closes stdin and cancels
HTTP work. Standard output contains protocol messages only.

Local contracts exercise real service watch creation/CAS retries and inspection,
copy bounds and source-applicability inspection through stdio on Memory and
disposable PostgreSQL, unchanged capital/operations,
owner failures, restricted actions, receiver mismatch, malformed input, frame
limits, cancellation and duplicate active IDs. A subprocess contract verifies
CLI dispatch and stdio-only output. These checks establish local transport
behavior, not installed-client integration, model quality or live provider
qualification. The bridge itself makes only requests to the configured Mantik
service and contains no provider/model request or balance/effect API.


## Terminal decision inspection

`mantik supervise --view decisions --limit 10` reads the existing authenticated
paper-decision API. Set `MANTIK_API_TOKEN` through the existing private environment;
the command does not accept a token argument. No model or external provider is
called. This is a command-driven inspection view; free-form conversation and
model interpretation remain unfinished.

The view lists finalized research decisions in bounded pages. Use `n` and `p` to
navigate locally, `r` to refresh the catalogue, and `q` to quit. On the displayed
page, `d ID` prints the retained finalized record, `e ID` reads its deterministic
post-decision explanation, `m ID` reads the retained market dimensions and
source fences, and `a ID` reads the decision's complete retained
[phase attribution](paper-attribution.md), including allocation choices and
non-grant admission-review history. These inspections use existing service endpoints;
original source bytes remain available through their existing inspection paths.

Inspection responses must match the displayed receiver, decision hash, historical
basis, finalization time and registration. Explanations also match the retained
strategy and evaluation facts. Response hashes, schema and authority flags are
checked before display. Attribution also passes the existing bounded row validator
and must contain the exact displayed finalized decision, including its evidence
basis and stored evaluation. Later outcomes or reviews can change the attribution
row while that immutable decision stays the same; this is a current read, not a
claim that downstream history was present at finalization. Funding ceilings remain
bounds, economics remain unavailable, and reviews never become grants. JSON output escapes source strings instead of printing
source text as terminal commands. These checks bind the service readback; they
do not independently attest imported records or replace the service's owner
replay.

A failed catalogue refresh marks the retained page stale and blocks navigation
and inspection until a successful refresh. A changed receiver is rejected for
the rest of that session; quit and reconnect to select another owner. Failed or
mismatched inspections produce an unavailable message without retaining a prior
inspection as a fresh result. Retry explicitly. Unknown commands, IDs outside
the displayed page and free-form requests send no inspection request. There is
no mutation, admission, allocation, execution or qualification action in this
view.

Memory and disposable PostgreSQL owner contracts compare terminal inspection
responses with the existing explanation, market-evidence and attribution responses.
The attribution cases also retain actual admission reviews across service reopen. Local
checks also cover stale reads, receiver changes, page restrictions, evidence
substitution, authority flags and cancellation. They do not restart or qualify
the curtailed terminal scenarios or establish conversational model behavior.

The `allocation_comparison`, `admission_review`, and `decision_attribution`
inspection kinds read the targeted owner routes
`/v1/allocation-comparisons/{id}`, `/v1/admission-reviews/{id}`, and
`/v1/paper-attribution/{id}`. They return the retained comparison, non-grant
review, or per-decision attribution from actual owner replay. They accept no
account choice, balance, admission, effect, replacement evidence or caller clock.
The comparison route rejects query parameters and GET bodies. Reads do not
append records or sample a service clock.

The stdio bridge keeps its existing 2 MiB response limit. An oversized owner
response fails rather than becoming a truncated success. These tools establish
no new admission or economic authority and do not qualify conversational model
behavior.

`inspect_record` kinds `social_record` and `copy_record` select the authenticated
original-capture GET routes. They return the same retained record as direct HTTP,
with complete source-owner replay and strict manifest-kind selection. These
clock-free reads preserve originals after watch changes. They accept only the
fixed kind and capture ID: no path, cutoff, admission or effect assertions.
Missing captures or absent owners retain HTTP 404; owner failures retain 503.
Original text and activity fields remain unverified source claims. Applicability
selectors continue to use their separate clock-dependent contract. No model call,
extraction attestation, economic effect or qualification follows from inspection.

`request_social_research` selects already-retained plain social or copy source material;
it does not re-record it or accept caller policy, source-class, admission or
execution assertions. The owner rechecks source and canonical-request
applicability before committing. `inspect_record` kind `social_research_request`
reads the returned canonical request ID. The tool is available only when the
social journal owner is available; it uses the same authentication, idempotence
and atomic commit path as the direct API. Model generation is not required to
invoke or verify this typed tool.


`inspect_record` also selects retained observation and evaluation records through
these fixed owner GET routes:

| Kind | Owner route |
| --- | --- |
| `outcome` | `/v1/paper-outcomes/{id}` |
| `outcome_market_evidence` | `/v1/paper-outcomes/{id}/market-evidence` |
| `evaluation_declaration` | `/v1/paper-evaluation-declarations/{id}` |
| `evaluation_evidence` | `/v1/paper-evaluation-declarations/{id}/evidence` |
| `evaluation_assessment` | `/v1/paper-evaluation-assessments/{id}` |

Each selector accepts only `kind` and a record ID. These reads use the existing
authentication, bounded owner replay and retained record clocks. Caller-supplied
cutoffs, replacement quotes, assessments, admissions, balances and effects are
rejected. Missing records or absent owners return 404; unavailable owners retain
503. The MCP schema lists these kinds with the same read-only annotations.

An outcome is a forward quote observation, not a fill or realized PnL. Its market
evidence preserves original capture, publication and admission identities without
verifying share quantities. A declaration is an operator plan, and an assessment
retains pending, failed or incomplete checks; inspecting either cannot complete
a recipe or grant admission, execution or qualification. Inspection invokes no
model or provider and does not create or update owner records.

`evaluation_evidence` reads the current owner-selected evidence for a declaration,
including excluded rows, outcome availability and necessary sample-count bounds.
The owner samples its service clock and replays its retained sources. The response
pins its `as_of` cutoff and source catalogue; it can change on a later read and
is separate from an immutable assessment. The adapter supplies no cutoff and
returns the owner response unchanged. No assessment, source record or economic
state is appended. Counts still do not establish independent samples, economic
performance, a completed evaluation or admission. HTTP and typed MCP use the
same fixed route, authentication and owner error behavior.

## Explicit model-assisted decision drafts

[Model-assisted decision drafts](model-decision-drafts.md) are a separate explicit
CLI flow using the same finalized-decision reads and typed claim-review owner.
The model receives no tools or financial authority. The MCP bridge itself still
makes no provider/model request. Retained typed findings do not verify arbitrary
prose or complete conversational acceptance.
