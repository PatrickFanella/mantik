# Collector-fenced research submission

`POST /v1/collector-inputs` admits attributed research inputs under an existing
[collector registration and lease](feed-collectors.md). The server locks ownership
and routing state in one transaction, checks authority, and commits the input,
routing receipt, and collector admission together. A takeover cannot occur
between the ownership check and commit.

The authenticated endpoint accepts one strict JSON object, at most 16 KiB:

```json
{
  "receiver_id": "receiver-REPLACE_WITH_READBACK",
  "fence": {
    "registration_id": "spy-iex",
    "owner_id": "collector-run-one",
    "generation": 1
  },
  "input": { "id": "REPLACE_WITH_COMPLETE_NORMALIZED_INPUT" }
}
```

Replace the receiver and input placeholders with actual readbacks and a complete
normalized input. The input must satisfy the existing research/provenance
contract and match the immutable registration's symbol, evidence class, access
reference, and journal. Its source must be the registered Alpaca quote feed or
`alpaca-regular-session`, with mapping `alpaca-us-equities-v1` and a journal
capture reference. A newly admitted input's receipt time cannot be in the future
relative to the service clock. Missing or quarantined normalized inputs can be
retained without becoming usable prices.

For new input, the lease must be unstopped, unexpired, and held by the exact
owner/generation. The service samples its clock after acquiring both storage
locks and rejects time earlier than the last retained collector command or
admission. Clients cannot supply admission time.

## Retry and admission history

The input identity and bytes remain immutable. Admission records retain the
first accepting registration, owner, generation, input digest, service time,
and canonical routing receipt. The registry retains at most 2,048 distinct
admissions; it does not prune or reset them.

An exact retry under the original admission fence can return the existing
receipt after expiry, stop, or takeover, with `duplicate: true`. This is readback
of an already accepted input; it creates no input, match, lease extension, or
new admission. Changed bytes conflict. The server also verifies that the
corresponding retained routing input and canonical receipt still agree with the
admission. Missing or inconsistent history blocks replay rather than silently
recreating it.

A successor with current authority can retry an existing input, but cannot
rewrite its first admission. If an operator manually retained the exact input
before collector admission, `input_already_retained: true` records that fact;
the original routing receipt remains authoritative. Receipt time of admission
is not provider occurrence time or proof that the collector fetched the data.

Full admission capacity blocks new input identities, while exact historical
retries remain available. Registration/command capacity has its separate limits.
Success returns HTTP 200; invalid contracts 422; unknown registration 404;
ownership, receiver, history, clock, or capacity conflicts 409; malformed JSON
400; and storage failure 503.

## Bounded journal client

After registering the journal and obtaining a current lease, supply all three
collector options to `alpaca-submit`:

```bash
./bin/mantik alpaca-submit --journal "$MANTIK_CAPTURE_JOURNAL" \
  --api-url http://127.0.0.1:40880 --receiver-id RECEIVER_ID \
  --collector-registration-id spy-iex \
  --collector-owner-id collector-run-one --collector-generation 1 \
  --limit 16
```

The client uses `/v1/collector-inputs` and retains the registration in its
immutable journal target binding. Owner and generation are invocation authority,
so a later explicitly acquired generation can deliver the same retained journal.
Omitting the fence or changing registration conflicts with an existing fenced
binding. There is no fallback to manual submission on endpoint or lease failure.
A journal already bound to manual submission cannot be silently converted;
prepare a new journal for the collector workflow while preserving the old one.
Older clients cannot publish a different serialized binding over the retained
fenced binding.

The existing bounded delivery protocol still records attempts before POST,
verifies receiver readback before acknowledging, and reconciles lost replies
without resubmitting. Readback can finish acknowledgment after the lease expires.
An absent input after a failed attempt still requires `--retry-missing-id`, at
most three total attempts, and current authority for a new admission. Expiry is
not a reason to weaken the fence or change the input.

This command delivers retained data. It does not acquire or renew leases,
contact Alpaca, start a recurring collector, or alter strategy execution.
The separate [finite collector command](finite-collector.md) combines bounded
acquisition with explicit lease lifecycle and this fenced delivery contract.

## Scope and persistence

Schema 9 retains admission history in the collector table, separately from the
fixture aggregate. Its guard rejects older writers that omit admission history;
older startup binaries reject schema 9. Prior migrations are unchanged. Backup
and restore must preserve collector admissions, receiver identity, research
inputs/receipts, and existing trading state together. Restoring history does not
activate a collector.

Manual `/v1/research-inputs` and `/v1/routing-inputs` remain available under the
operator token; this endpoint does not turn lease labels into independent
authentication principals or remove manual operator authority. The server checks
declared provenance and retained input integrity; it does not fetch raw journal
bytes, re-normalize provider responses, or verify provider entitlement.

Process liveness, raw evidence validation, coverage freshness, provider access,
and strategy qualification remain separate checks. Global `feed_coverage`
remains `not_configured`. This implementation grants no profitability credit
and enables no orders or live-money execution.
