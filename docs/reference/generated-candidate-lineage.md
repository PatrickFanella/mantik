# Recorded generation to candidate registration

`internal/generatedcandidate` defines a pure bridge from a complete retained
[generation outcome](candidate-generation-outcomes.md) to a new
[candidate registration](candidate-lineage.md). Memory/PostgreSQL backends retain
that explicit link atomically with the registration and exact dataset. This adds
no provider call, compiler, worker launch or strategy activation. Authenticated
HTTP/CLI commands expose the retained registration and evidence.
Legacy candidate registration JSON, schema and hashes are unchanged.

## Explicit source selection

A command names an expected receiver, new registration request, optional earlier
parent, generation request and exact intent/outcome/recording/plan/candidate
hashes. It contains no substitute source text or caller-supplied publication.
An owner must select its own retained generation artifact and authoritative
publication and dataset.

`Append` validates the supplied complete candidate prefix and compact link prefix,
then compares the full generation artifact, authoritative publication binding and
exact dataset. Rejected recordings cannot be registered through this bridge.
For source-bearing recordings, it derives the legacy registration command from
the exact recorded candidate, including source, hypothesis, dataset and toolchain.
Invalid Go can remain a draft for separate isolated-worker failure checks.

Registration cannot predate the outcome's server retention time. Parent pins,
monotonic registration order, full history and the 32-registration bound retain
the legacy contract. Distinct request IDs remain separate declarations even when
they select identical output. There is no replacement or activation action.

An exact linked retry returns the original registration and link without reading
a new clock, including at capacity. A changed generation reference, source pin or
parent conflicts. A registration already present without a generation link cannot
be adopted by `Append`, even if its source and times happen to match. New linked
registration must be explicit when the owner first retains it.

## Portable links and artifacts

`mantik-generated-candidate-registration-link-v1` contains the complete command,
registration ordinal/hash, publication ID/hash, outcome retention time and
registration time. Its authority is `recorded_generation_registration_link_only`.
Provider call, model authorship, compilation, execution and qualification flags
remain false. Its own hash uses compact Go JSON with its `sha256` empty.

`VerifyRegistration` compares the link with the complete legacy registration.
`NewLink` additionally compares the full generation artifact, including exact
candidate equality and publication/intent/plan/recording/outcome pins.
`mantik-generated-candidate-registration-artifact-v1` carries all three values:
link, legacy registration and full generation artifact. Validation rederives the
link from these complete typed values. Construction detaches nested arrays and
raw output bytes; strict bounded readers reject ambiguous JSON.

These constructors do not know what a service retained. A portable artifact alone
cannot establish authoritative publication ownership, raw dataset availability,
parent ownership, completeness or when an external model ran. An owner must use
the complete-prefix `Append` rule and validate full authoritative dependencies;
constructing `NewLink` alone cannot authorize adopting an existing registration.

## Coherent discovery

`mantik-generated-candidate-registry-v1` includes the complete legacy candidate
registry, links in registration order and the exact ordered complement
`unlinked_registration_ids`. Manual declarations remain visible in the same
snapshot. An unlinked declaration does not prove that no external model was used.
Null arrays, duplicate/out-of-order links, changed registration pins and an
incorrect complement are invalid. Authority is
`retained_generation_registration_links_only`, with false provider/authorship/
qualification flags and an own content hash.

Compact link metadata cannot independently rederive source provenance from hashes.
Backends validate every full owned generation artifact, publication, parent and
exact dataset in one coherent snapshot before reads, writes or retries. They
persist the returned legacy registration and link atomically; they never call the
ordinary registration route first and attach a link afterwards.

Pure tests cover legacy wire/hash identity, exact original source, parent chains,
all 32 slots, clock-free retry at capacity, mixed manual/linked discovery,
retrofit refusal, rejected output, changed bindings and times, strict readers,
self-hashed forgeries and detached values. The [#18 acceptance audit](../research/candidate-evaluation-acceptance.md) maps
these checks to written criteria and separate native evidence. Provider requests
and model authorship are not established by recorded fixture evidence.

## Durable retention (schema 35)

`GeneratedCandidateStore` exposes `RegisterGeneratedCandidate`,
`GeneratedCandidateRegistry` and `GeneratedCandidateEvidence`. A registration
command selects the owner's retained generation request and expected hashes;
source, raw dataset and authoritative publication are read from storage. Returned
artifacts include the complete legacy registration, link and recorded generation.

Memory holds the complete operation under the existing receiver mutex. PostgreSQL
uses the receiver row lock shared by ordinary registration writers and commits the
registration, copied exact dataset, compact link and marker in one transaction.
Reads use a repeatable-read snapshot. Full generation recordings are checked one
at a time; only selected artifacts survive validation. Returned values detach.

Registration reuses the complete generation rows already validated under that
operation's lock and transaction, rather than rescanning the same history. This
reuse ends with the operation; later reads, new registrations and exact retries
validate the full prefix again, including unrelated damaged dependencies. The
five-second HTTP backend deadline and all evidence checks remain unchanged.

Schema 35 adds `generation_link_sha256` to registration rows and the bounded
`mantik_generated_candidate_links` table. Prior and manual registrations have a
NULL marker; their JSON and hashes are unchanged. Reciprocal deferred foreign keys
require each non-NULL marker and link hash to exist together at commit. Deleting a
linked row or clearing its marker alone fails, and a failed link insert rolls back
the registration and dataset. Application checks additionally rederive every link
from full retained generation evidence; matching database hashes alone do not
prove provenance.

Ordinary candidate registry, selected registration/dataset reads, exact retries
and worker declarations also validate the complete retained link prefix and full
generation dependencies. Unrelated corruption fails these operations rather than
silently dropping a link or hiding damaged evidence behind a selected ID. Existing
worker intents and evidence formats remain unchanged. Generation history can still
be inspected separately without interpreting a registration as provider proof.

The 34-to-35 migration preserves legacy registrations, datasets, generation
intents/outcomes/recordings and runtime state without inventing links. The schema
version and checksum guards refuse incompatible or changed migration histories.
Older binaries refuse schema 35; rollback requires restoring a compatible private
backup into separate resources, not dropping retained links in place. This source
change does not deploy or migrate an existing pilot database.

## Authenticated registration and inspection

All routes require the existing bearer token, reject query parameters and use a
bounded backend context. `POST /v1/generated-candidates` accepts only a strict
`Command` (at most 4096 bytes); it returns the full coherent `Artifact`.
`GET /v1/generated-candidates` returns the complete `Registry`, including manual
registrations and the exact unlinked complement. `GET /v1/generated-candidates/{id}`
returns one full `Artifact`; an existing manual registration without a generation
link returns 404. Missing references return 404, invalid input 422, and changed
bindings, receiver conflicts or capacity conflicts 409. There is no launch route.

```sh
./bin/mantik generated-candidate-register --api-url "$api" \
  --receiver-id "$receiver" --id "$registration_id" \
  --generation-id "$generation_id" --intent-sha256 "$intent_sha" \
  --outcome-sha256 "$outcome_sha" --recording-sha256 "$recording_sha" \
  --plan-sha256 "$plan_sha" --candidate-sha256 "$candidate_sha" \
  > generated-registration.json
./bin/mantik generated-candidates --api-url "$api" --receiver-id "$receiver"
./bin/mantik generated-candidate --api-url "$api" --receiver-id "$receiver" \
  --id "$registration_id" --link-sha256 "$link_sha" \
  --registration-sha256 "$registration_sha" --artifact source > candidate.go
```

Registration optionally accepts `--parent` and returns the full artifact. It has
no substitute source or dataset input: the backend selects its own retained bytes.
The client validates the complete response and requires the exact submitted command.
An exact retry returns the original link, registration and generation evidence.

Inspection requires both link and registration identities. The link binds the
full command's generation/intent/outcome/recording/plan/candidate pins; the client
rederives the complete artifact before emitting any selected value. Supported
`--artifact` values are `link` (default), `artifact`, `registration`, `generation`,
`recording`, `candidate`, `source` and `output`. Source and recorded output export
exact bytes without an added newline. These reads do not attest model authorship
or execute the source. Use the existing pinned `candidate-dataset` command for
exact retained dataset bytes. The ordinary registration wire and commands remain
compatible.
