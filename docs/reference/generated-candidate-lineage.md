# Recorded generation to candidate registration

`internal/generatedcandidate` defines a pure bridge from a complete retained
[generation outcome](candidate-generation-outcomes.md) to a new
[candidate registration](candidate-lineage.md). It adds no backend, schema,
HTTP/CLI admission, provider call, compiler, worker launch or strategy activation.
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
A future backend must validate every full owned generation artifact, publication,
parent and exact dataset in one coherent snapshot before reads, writes or retries.
It must persist the returned legacy registration and link atomically; it must not
call the ordinary registration route first and attach a link afterwards.

Pure tests cover legacy wire/hash identity, exact original source, parent chains,
all 32 slots, clock-free retry at capacity, mixed manual/linked discovery,
retrofit refusal, rejected output, changed bindings and times, strict readers,
self-hashed forgeries and detached values. Durable bridge retention, authenticated
access, provider adapter and final #18 acceptance audit remain unfinished.
