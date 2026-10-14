# Durable Alpaca capture journal and research submission

The journal retains validated Alpaca recordings, normalized research inputs,
submission attempts, and acknowledgements in a private directory outside Git.
It supports bounded manual submission to a compatible development service and
resume after interruption. `alpaca-collect` adds one explicit bounded acquisition
into the same journal; it does not run a continuous collector.

## Retention and ownership

`alpaca-retain` creates a journal directory with mode 0700, owned by its caller.
Artifacts have mode 0600. The directory must have an absolute path and an
existing parent. Existing permissions and ownership are checked, not weakened.
The journal uses a non-blocking process lock; a second writer fails explicitly.
Journal commands run on the Linux host as the directory owner.

Capture publication writes a temporary file, syncs it, links it to a new final
name without overwriting, and syncs the directory. Interrupted temporary files
remain evidence and are not interpreted as captures. Exact retention retries
converge; changed metadata or bytes at the same capture/revision conflict.
Malformed recordings are rejected; their original supplied files remain the
caller's evidence. Explicit collection retains failed provider attempts separately
from validated captures; see the acquisition contract below.

A capture contains original response bytes, their verified SHA-256, the original
artifact reference, capture metadata, normalized output, and a full capture
digest. Routing provenance points to `journal:JOURNAL_ID:capture-ID-rN.json`.
That reference resolves to the named capture in this journal, whose recording
contains the raw bytes. Validation detects corruption and normalization drift
before delivery. Inputs already submitted with a different raw reference cannot
be silently adopted; retention should precede their first submission.

Limits are 128 captures, 128 collection intents, 8 MiB per journal artifact,
and 20,000 directory entries,
including attempts and interrupted temporary files. Capacity failure preserves
existing evidence and pending work. No automatic deletion or pruning is added.
The service's separate 4,096 routing-input bound remains in force.

## Local commands

Create a manufactured recording using [the adapter example](alpaca.md), then
retain it using its printed raw digest:

```bash
mkdir -p "$HOME/.local/state/mantik"
MANTIK_CAPTURE_JOURNAL="$HOME/.local/state/mantik/alpaca-recordings-r1"
./bin/mantik alpaca-retain --journal "$MANTIK_CAPTURE_JOURNAL" \
  --recording data/alpaca-quote.json --sha256 RAW_SHA256
./bin/mantik alpaca-journal --journal "$MANTIK_CAPTURE_JOURNAL"
```

Status reports local capture/input/acknowledgement/pending/uncertain counts, the
target binding, and at most 16 pending identities with attempt counts. It does
not prove server liveness, feed coverage, or completed application work. Status
and submission require an existing journal; they never recreate a missing one.
Authentication environment variables are not copied into journal metadata.

## One bounded acquisition

`alpaca-collect` reads one declared quote or calendar request using the existing
fixed Alpaca endpoints. It requires an explicit spec and a separate private JSON
credentials file. The credentials file must be a regular file owned by the caller
with mode 0600, at most 4 KiB, containing only `key` and `secret` string fields.
Leaf symlinks are rejected. Credentials are not searched for in the environment,
printed, or copied into journal metadata. Provision this file through private
credential delivery; do not send credentials in chat or add the file to Git.
No provider access has been configured by this implementation.

A quote spec contains:

```json
{
  "id": "quotes-run-001",
  "capability": "latest_quotes",
  "feed": "iex",
  "symbols": ["SPY"],
  "access_ref": "private-access-receipt-v1",
  "max_quote_age_seconds": 60
}
```

The access reference points to a separately retained access/licensing receipt;
its presence does not prove entitlement. For calendar collection, use
`capability: market_calendar`, provide explicit `start` and `end` dates (at most
31 inclusive dates), and retain the symbols to which the schedule applies. Feed
selection remains explicit; the calendar recording uses `regular_session`.
These contracts follow Alpaca's [latest quotes reference](https://docs.alpaca.markets/us/reference/stocklatestquotes-1)
and [calendar reference](https://docs.alpaca.markets/us/v1.1/reference/getcalendar-1).

After private provider access is configured, the explicit command is:

```bash
./bin/mantik alpaca-collect --journal "$MANTIK_CAPTURE_JOURNAL" \
  --spec /private/path/collection-spec.json \
  --credentials-file /private/path/alpaca-credentials.json
./bin/mantik alpaca-collection --journal "$MANTIK_CAPTURE_JOURNAL" \
  --collection-id quotes-run-001
```

The command has a two-minute deadline and makes at most three HTTP attempts,
with one request in flight, at least one second between starts, and bounded
rate-limit waits. Request intent is synced before acquisition, and each attempt
start is synced before HTTP. Each response is synced before retry, normalization,
or capture publication. Failed HTTP responses, transport-failure markers, and
malformed successful payloads remain separate private evidence. Response bodies
are stored as base64 bytes with an artifact hash; invalid UTF-8 is preserved.
Oversized or interrupted bodies retain at most the first 1 MiB with `partial`
set. A partial prefix is never admitted as a complete response or valid capture.
No credentials, response headers, or transport error text are included in these
artifacts. Terminal output shows status and counts without raw response bodies.

Valid captures are revision 1 with `prospective_market_unqualified` attribution;
this means newly retrieved observations, without profitability or release credit.
Missing/stale/subcent-price records retain the normalizer's unusable coverage.
The command does not submit inputs, create economic effects, or install timers.
Use the separately fenced submission command after inspecting the capture.

`alpaca-collection` verifies the intent, hashed attempt evidence, terminal receipt,
and successful capture linkage. It reports `retained`, `failed`, or `incomplete`,
along with attempt/response counts, last HTTP status, and an unfinished-attempt
flag. These files do not prove a process is live. Repeating a completed collection
identity with the exact spec returns its verified receipt without another read.
A changed spec conflicts. A failed or interrupted identity never refetches: latest
quotes would be different evidence. Inspect the retained artifacts and actual
runtime first; a deliberately new retrieval requires a new collection identity.
There is no automatic salvage, replay, deletion, rotation, or scheduler.

Acquisition checks capture/collection capacity and existing journal integrity
before HTTP. It stops if publication fails, preserving partial work. A full journal
requires a reviewed archive/retention policy before continuing; creating an
unbounded chain of new journals is not continuous-feed readiness.

## Receiver identity and bounded readback

Schema 7 adds a persistent, opaque research-receiver identity for each logical
database. Restart preserves it; separately initialized databases have distinct
identities. This identity is separate from fixture state and replay results.

Authenticated endpoints are:

| Endpoint | Contract |
| --- | --- |
| `GET /v1/research-receiver` | Receiver ID and an empty input selection |
| `GET /v1/research-receiver?input_id=ID` | Receiver ID and retained inputs for at most 16 requested IDs |
| `POST /v1/research-inputs` | Strict `receiver_id` plus an attributed market `input`; matching receiver required in the mutation transaction |

Guarded submission rejects fixture inputs. Wrong receiver IDs return 409; invalid
contracts return 422. Existing manual routing endpoints retain their contracts.
Readback identifies only selected records instead of returning full history.

On a compatible development service, with its existing `MANTIK_API_TOKEN`:

```bash
MANTIK_RESEARCH_RECEIVER_ID="$(curl -sS \
  -H "Authorization: Bearer $MANTIK_API_TOKEN" \
  http://127.0.0.1:40880/v1/research-receiver | jq -r .id)"
./bin/mantik alpaca-submit --journal "$MANTIK_CAPTURE_JOURNAL" \
  --api-url http://127.0.0.1:40880 \
  --receiver-id "$MANTIK_RESEARCH_RECEIVER_ID" --limit 16
```

The journal permanently binds the exact API origin and expected receiver ID
before mutation. Changing either fails. Each invocation handles at most 16
pending inputs, checks prior acknowledgements in pages of 16, and has a two-minute
command deadline. The API enforces identity again at submission, so replacement
between readback and POST cannot receive an input under the old identity.

Do not run these commands against the preserved schema-5 pilot instances. Their
revision is unchanged and they do not expose this receiver contract.

## Interrupted and unknown submissions

An immutable, synced attempt record precedes each POST. A server acknowledgement
is written only after a fresh read confirms the exact input and receipt under
the expected receiver. A lost response or interrupted local acknowledgement can
be reconciled from retained server history without another POST. Existing local
acknowledgements are also checked against current server history before new work.
Missing or changed acknowledged history blocks delivery rather than replaying it.

If an attempted input has no server receipt, automatic retry stops with an
unknown outcome. Inspect the actual receiver and retained evidence first. A
reviewed retry uses the same pending identity shown in local status:

```bash
./bin/mantik alpaca-submit --journal "$MANTIK_CAPTURE_JOURNAL" \
  --api-url http://127.0.0.1:40880 \
  --receiver-id "$MANTIK_RESEARCH_RECEIVER_ID" --limit 1 \
  --retry-missing-id RETAINED_INPUT_ID
```

Recovery cannot change capture metadata, attribution, target binding, or input
identity. There are at most three total attempted submissions per input. A
later confirmed server receipt can still reconcile after that bound; no fourth
POST is admitted. Missing prices and unusable evidence remain unusable, and
research submission creates no finalized decision or economic authority.

## Backup, restore, and compatibility

Journal collection intents/responses/receipts, captures, manifest, binding,
submission attempts, and acknowledgements form one
recovery set. Take a coherent copy while no journal writer owns it. Keep private
raw/license references and credentials out of Git. Restoring a database preserves
its logical receiver identity; a separately exposed restore must remain isolated.
API-origin binding prevents automatic redirection to that copy, and acknowledged
history is reverified rather than trusted from local files.

Schema 7 preserves migration checksums 1–6 and original fixture/routing history.
Older binaries reject the newer schema at startup. Deployment still needs a
coherent backup and compatible rollback revision. Normalizer changes require an
explicit journal compatibility review; retained output is not regenerated into
new effects silently.

Credential delivery, provider entitlement/account proof, continuous acquisition,
calendar revision/timer reconciliation, retention policy for continuous runs,
real-feed readiness, and prospective qualification remain subsequent work.
