# Retained local extraction captures

`news-local-import` retains checked local extraction receipts in the service.
It reads an existing private capture directory and never contacts its model
endpoint. The service independently replays the request, source/contract pins,
HTTP capture, quote anchoring and extraction result before committing a record.

This ledger is separate from recorded-fixture extraction attempts. Its mode is
`caller_reported_local_capture`, its authority is
`caller_reported_local_capture_only`, and both `execution_attested` and
`qualification_credit` are false. An import proves which consistent bytes the
service received at `imported_at`. It cannot attest that the caller contacted
the provider, that the source is true, or that reported timing/cancellation
metadata describes an actual event. It is not a service-owned execution intent.

## Import and inspect

Use the intended service's receiver ID from `news-cases`; supply its API token
through `MANTIK_API_TOKEN`. Keep full capture output private: targeted reads and
import receipts include the original source inside the request and captured
response/model/candidate bytes.

```sh
./bin/mantik news-local-import \
  --api-url http://127.0.0.1:40880 --receiver-id RECEIVER \
  --id local-capture-1 --dir /private/existing-local-capture \
  > /private/import-receipt.json

./bin/mantik news-local-capture \
  --api-url http://127.0.0.1:40880 --id local-capture-1 \
  > /private/retained-capture.json

./bin/mantik news-local-captures --api-url http://127.0.0.1:40880
```

The import command uses the same private-file checks as `news-local-inspect`:
absolute clean directory path, current-user ownership, no group/other access,
regular bounded files, no receipt symlinks/FIFOs, and strict JSON. `prepared.json`
is required. Missing `finished.json` produces `prepared_only` with disposition
`completion_unknown` and no result. A malformed present completion is rejected.
Consistent finished captures can include extracted candidates, explicit
abstention, HTTP errors, rejected/truncated output or reported transport failure.
Legacy v1 and unknown versions are rejected without rewriting their files.

Each import ID is immutable. An exact retry returns the original receipt,
including its server import time, without updating a row or sampling another
clock. A different prepared or finished receipt under that ID conflicts. To
retain a later finished capture, use a new ID; matching `prepared_sha256` values
link the two records. The earlier prepared-only import keeps its unknown
completion. Distinct IDs remain distinct even when their evidence matches.

A receiver permits at most 32 captures, including failures and unknown outcomes.
Exact retries remain available at capacity. There is no pruning, completion,
retry, model execution, news admission or operations action in this API. The
recorded-fixture `news-extraction-admit` path cannot read these captures.
Approval of local candidates remains separate development work.

## HTTP and evidence contracts

| Endpoint | Command or response |
| --- | --- |
| `POST /v1/news-local-captures` | `receiver_id`, `request_id`, `prepared`, optional `finished` |
| `GET /v1/news-local-captures/ID` | Full `mantik-local-extraction-capture-v1` record |
| `GET /v1/news-local-captures` | Compact `mantik-local-extraction-capture-history-v1` history |

All routes require bearer authentication. Commands reject unknown, duplicate or
mis-cased fields and query parameters. Each receipt is bounded to 8 MiB; the
command/full record limit is 16 MiB plus 64 KiB. Wrong receiver, identity conflicts
and capacity return HTTP 409; malformed JSON returns 400; invalid or inconsistent
evidence returns 422; missing captures return 404. Commands use the existing
loopback HTTP service transport.

The service assigns receiver, ID and import time; the caller supplies the two
local receipts. The server recomputes the inspection, then hashes the capture
using compact Go JSON with its own `sha256` empty. Original source/request,
HTTP/model content, derived artifact and result remain in their local receipt
structures, with their existing byte hashes. Receipt JSON whitespace is not
preserved; the encoded source/request/response/artifact bytes are preserved.

History sorts all captures by ID and reports complete state counts. It omits
source/response bodies while retaining endpoint/model configuration and hashes.
The service replays every retained capture before returning history; one corrupt
row fails the request instead of disappearing from a partial success. CLI
history checks the compact shape and hashes; full replay requires a targeted
capture read. Targeted reads and import responses receive full replay validation.

## Persistence and recovery

Schema 23 adds only `mantik_local_extraction_captures`. Migration checksums and
receiver identity remain guarded. Existing recorded attempts, admissions,
research and operations are not rewritten. A schema-22 binary refuses a
schema-23 database; rollback requires the previous binary plus a verified
pre-upgrade backup on separate resources. Do not downgrade the schema ledger or
delete imported captures to make an older binary start.

Back up the entire database with its receiver identity and migration ledger.
After restoring to an isolated service, compare full records and compact history,
including failures and unknown outcomes, before any further work. Import time is
a server receipt time, not evidence of when a model ran. A healthy restore or a
valid imported candidate does not establish extraction accuracy or profitability.
