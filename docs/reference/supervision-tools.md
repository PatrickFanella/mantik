# Typed supervision calls

Authenticated conversation adapters can call a fixed set of in-process service
owners through `POST /v1/supervision/calls`. `GET /v1/supervision/tools` lists the
available tools and argument contracts. Both use the service's existing bearer
authentication. They make no network or model requests.

This HTTP adapter is a bounded part of #23. It does not implement an MCP server,
a conversation UI, model interpretation of prose, or review-case triage. Existing
strategies and protection do not depend on its availability. Issue #23 remains
open pending those acceptance checks.

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
