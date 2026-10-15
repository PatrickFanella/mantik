# Current case inspection

`GET /v1/cases`, `GET /v1/cases/{id}` and `mantik cases` expose read-only case
state without returning the complete status aggregate or audit history. They
use the service's existing bearer-token authentication and require no schema
migration or new worker.

## Catalogue

`GET /v1/cases` returns `mode`, one `fixture` execution case, and a `routing`
array sorted by case identity. Routing currently retains at most 128 research,
128 decision and four action cases, including closed generations: 260 total.
The endpoint returns that bounded catalogue, with an empty array when no routed
cases exist. A catalogue exceeding this contract fails rather than truncating.

The fixture case includes its retained state and any entry/exit outcomes.
Routed cases expose owner, behavior, status, generation, match count, latest
input/definition references and any source decision/subject case identity.
Research routing does not grant execution authority. A routed action case is
also distinct from its finalized decision and economic effect.

PostgreSQL projects only the fixture case and routing case array from the
persisted aggregate. It does not read the event ledger, all routing inputs,
collector publications, or provider credentials. Memory storage returns copies
under its existing lock. This is a bounded current-state catalogue rather than
a paginated immutable history.

## One case and its latest references

`GET /v1/cases/{id}` returns a `mode` and explicit `kind`:

- `fixture_execution`: the `fixture` case and its configured `strategy` contract.
  Configured safety conditions are not a replacement for the finalized decision
  audit record; inspect retained decisions/outcomes through the
  [activity readback](activity.md).
- `routing`: the selected `routing` case and its `last_definition`, together with
  the source indicated by `last_input_ref`: `last_input` for `routing:ID`,
  `last_quote` for `quote:ID`, or `last_timer` for `timer:ID`.

A retained routing input includes its original structured input, provenance,
quality, availability time and routing receipt. Fixture quotes come from their
retained observation. Timer jobs include their applicability/routing receipt
when completed. The view returns only the latest source reference retained on
the case, not every earlier match. Combined triggers can depend on additional
facts; the latest source alone is not a complete reconstruction of their cause.

`references_available` is true for the fixture case/configured strategy pair,
or when a routed case's latest definition and selected source are both present.
Missing references are omitted and the flag is false. Presence is not freshness,
quality, eligibility, strategy qualification, finalized authority or profitability.
A missing reference must not be interpreted as an empty or successful source.

PostgreSQL resolves the requested case, definition and selected source in one
statement under one consistent snapshot, without returning unrelated inputs or
history. Memory storage does the same under its lock. The API returns original
structured fields and references; it does not fetch source URLs/private files,
render source bytes, generate explanations or mutate cases.

The catalogue and each detail request are independent current-state readbacks.
A case may change between them; no shared revision or history boundary is
promised. Closed cases remain inspectable. Source occurrence/receipt times and
timer timestamps remain distinct; they are not a timestamp of the operator's
readback.

## CLI

```sh
./bin/mantik cases --api-url http://127.0.0.1:40880
./bin/mantik cases --format json
./bin/mantik cases --id demo-case-v1
```

The table separates fixture execution from routing behavior and quotes all
stored text. `--format json` returns the complete catalogue. `--id` always
returns structured JSON for that one case, including its latest references.
Set `MANTIK_API_TOKEN` as for the other CLI commands. A request failure exits
with an error; the command keeps no cached successful display or retry loop.

Both endpoints reject query parameters with HTTP 400. Invalid path identities
return HTTP 400, unknown cases HTTP 404, missing authentication HTTP 401, and
storage failures HTTP 503. These GET operations use the existing five-second
server context bound and add no collector lease or execution capability.

## Interactive case navigation

```sh
./bin/mantik supervise --view cases --limit 10
```

The viewer reads the bounded current catalogue once, then displays at most the
selected number of rows per page. The fixture execution case comes first,
followed by the catalogue's sorted routed cases. It keeps this bounded catalogue
rather than loading audit history. The default `supervise` view remains activity.

Use these commands followed by Enter:

| Command | Behavior |
| --- | --- |
| `n` / `p` | Next / previous local catalogue page, with no service request |
| `r` | Refresh the catalogue and return to its first page |
| `d CASE_ID` | Fetch current structured details for a case on the displayed page |
| `q` or EOF | Exit without changing the service |

The detail request is a separate current-state read, explicitly labeled as such.
A case can change after the catalogue was loaded. Details preserve the API's
reference-availability and quality fields, including `references_available=false`;
the viewer does not treat a reference as proof of authority or qualification.
It does not retain a history of detail responses or fetch private source bytes.
Off-page identities and unknown commands send no request.

Catalogue or detail failure marks the displayed catalogue `STALE` and disables
navigation and details until `r` successfully refreshes it. An initial connection
failure still permits refresh, quit and EOF. A refresh always starts from the
first page; it does not silently carry an old page offset into a changed
catalogue. Successful reads are labeled as on-demand readbacks, not continuous
health checks. A response exceeding the 260 routed-case capacity is rejected.

The case and activity views share bounded command input and owned stdin
cancellation. Neither changes terminal mode, automatically polls the service,
submits a mutation, or stops workers on exit. See the [activity viewer](activity.md)
for Unix stdin, command-length and cancellation behavior.

This supplies case navigation for issue #22. Exception navigation still needs
a bounded operational readback; the existing operations endpoint internally
loads the full status/event history. Library/portfolio screens, full match-history
inspection and finalized-decision attribution remain separate units.
