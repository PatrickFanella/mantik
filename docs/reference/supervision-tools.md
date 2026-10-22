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
| `inspect_record` | `kind` and `id`; kinds `decision`, `review_case`, `news_record`, `research_review`, `watch` | Existing targeted GET route for that kind |
| `request_research` | `receiver_id`, `request_id`, `case_id`, `version`, optional `type_id` | `POST /v1/news-reviews` |
| `change_watch_policy` | Existing policy command: `id`, `receiver_id`, optional `previous_sha256`, `policy` | `POST /v1/social-watches` |
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
Tool calls return the owner's status and JSON body directly, including duplicate
receipts, receiver conflicts, evidence-version conflicts, capacity failures and
storage failures. Owner request IDs and watch-policy compare-and-swap checks
remain authoritative; this adapter has no separate retry journal or clock.

Watch changes allow only `research` and `decision_review` behavior. See
[social watch policies](owned-social-watches.md) for policy fields and immutable history.
A configured-rule activation cannot be requested through this adapter. Exception
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
fields; unavailable tools are absent. Watch schemas allow research and decision
review only. Each call rechecks receiver identity before forwarding exactly once
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
owner failures, restricted actions, receiver mismatch, malformed input, frame
limits, cancellation and duplicate active IDs. A subprocess contract verifies
CLI dispatch and stdio-only output. These checks establish local transport
behavior, not installed-client integration, model quality or live provider
qualification. The bridge itself makes only requests to the configured Mantik
service and contains no provider/model request or balance/effect API.
