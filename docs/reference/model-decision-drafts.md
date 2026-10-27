# Model-assisted finalized-decision drafts

`paper-decision-draft` requests one advisory explanation for an actual finalized
research decision, retains the attempt privately, and submits its typed factual
claims to the existing service review owner. The model cannot choose a decision,
account, operation, admission or financial effect. It receives only the selected
finalized decision record, with its original source and strategy pins.

This is an explicit explanation/review flow for #23. It does not implement a
general conversational client, prove model quality, or complete that issue.
The deterministic explanation and existing strategy/protection workers remain
independent of this command and its model availability.

## Select actual owner records

The command requires the service receiver, actual decision ID and full record
hash. It reads that record from the authenticated service, validates its envelope
and selected pins, and separately checks the deterministic owner explanation.
A caller-supplied record file or explanation cannot replace these reads.

It also checks the selected review ID before model acquisition. A retained review
or an unavailable owner stops the command. A successful new flow uses the same
`POST /v1/paper-decision-reviews` operation as the supervision/MCP tools, then
reads the retained review back and verifies its command, source binding, hash and
authority flags. Contradictory or unsupported typed claims join the existing
review case for that decision. The original finalized decision is unchanged.

Example, after selecting real owner IDs and configuring a private model route:

```bash
./bin/mantik paper-decision-draft \
  --api-url http://127.0.0.1:40880 \
  --receiver-id ACTUAL_RECEIVER_ID \
  --decision-id ACTUAL_FINALIZED_DECISION_ID \
  --decision-sha256 ACTUAL_FULL_RECORD_SHA256 \
  --review-id UNIQUE_REVIEW_ID \
  --model-config /absolute/private/model.json \
  --capture-dir /absolute/private/new-draft-attempt
```

Use the existing `MANTIK_API_TOKEN` service authentication. An optional
`MANTIK_MODEL_TOKEN` supplies the configured gateway's bearer token; it is sent
only to that model endpoint and is omitted from retained request bodies. The
command does not copy the service token to the model request.

## Model transport and output

The configuration uses the existing private Ollama-routed adapter contract:

```json
{
  "endpoint": "http://127.0.0.1:4000",
  "model": "ollama/OPERATOR_SELECTED_MODEL",
  "max_tokens": 1024
}
```

These are illustrative values, not a verified available route or model. Select
the actual configured gateway and model. This adapter accepts a private literal
IP HTTP(S) origin, an explicit non-cloud `ollama/` route and 64–4,096 requested
tokens. Other provider adapters can have their own contracts; this command does
not select a fallback. It uses one non-streaming `/v1/chat/completions` request,
JSON-object output, temperature zero, no model tools, no environment proxy,
no redirects, a two-second connect timeout and a 60-second request timeout.
The [Ollama compatibility reference](https://docs.ollama.com/api/openai-compatibility)
describes the underlying compatible transport; route naming remains the
configured gateway's responsibility.

Prompt `finalized-decision-advisory-claims-v1` asks for an explanation and one to
16 sorted, unique factual claims. The response must contain exactly one completed
assistant choice. Refusal, truncation, requested tools, malformed/duplicate JSON,
unknown draft fields, invalid claims and responses over 64 KiB fail visibly.
Source records are bounded to 32 KiB. Typed claims use the existing
[decision-review contract](decision-review-cases.md).

The explanation remains unverified prose. Supported typed claims do not prove
that they exhaust the explanation's claims or that arbitrary prose is faithful.
`prose_verified`, execution attestation and qualification remain false in the
local receipt. The service review's `model_used` remains false because the service
checker itself calls no model; the separate local receipt records the model
transport attempt. A successful HTTP response is not model execution attestation,
a profitable result or an admission grant.

## Retained attempts and uncertain outcomes

The capture directory must be a new absolute path. It is created with mode 0700;
artifacts use mode 0600, exclusive creation and file/directory sync. It retains:

- `source.json`: selected service identity, finalized record and deterministic
  explanation, plus intended review ID;
- `model-1.json`: complete prepared request before transport;
- `model-2.json`: request-start intent, retained before sending;
- `model-3.json`: terminal outcome with bounded original response bytes, hashes,
  HTTP status and any accepted draft;
- `review-intent.json` and `review-response.json`: the exact typed command and
  returned owner receipt;
- `complete.json`: the successful retained review readback binding.

Failed, cancelled and oversized responses retain their available original bytes
and completion state. A missing terminal receipt is an unknown attempt, not a
successful draft or permission to reissue. Reusing a capture directory refuses
another model call. A completed review ID is also rejected before acquisition.
These guards do not claim distributed exactly-once model execution across
concurrent processes using different new capture directories.

A review transport failure writes `review-outcome.json` as unverified. Inspect
the actual review owner before retrying the retained command; do not replace the
review ID or issue another model call to resolve that uncertainty. A lost final
readback likewise leaves the returned receipt inspectable. No model or review
request is retried automatically.

## Verification and remaining acceptance

Tests use manufactured model HTTP responses with actual authenticated Memory and
disposable PostgreSQL finalization/review owners. They exercise source pins,
private request retention, supported and contradictory claims, case membership,
owner readback, capture/review reuse, invalid output, provider outage and unchanged
operational state. Model endpoint failures leave deterministic owner inspection
available. No model client or worker starts with the Mantik service.

These tests do not establish behavior of a real model or a configured deployment.
Installed conversational client behavior, faithful natural-language interpretation,
authorized policy/action journeys, model-backed acceptance and review triage remain
separate work. Financial evaluation, admission, durable execution, recovery/release
acceptance and natural qualification also remain incomplete.
