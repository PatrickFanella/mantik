# Recorded candidate generation

These offline commands retain a generation plan and exact recorded output before
compilation, execution or service admission. They are part of #18 and make no
provider request. A caller-declared model label such as `gpt-6.1-sol` is metadata;
it does not establish model availability, identity or authorship.

## Plan and record

Keep inputs and generated evidence in a private directory outside Git. Declare
the synthetic hypothesis and exact dataset as for [candidate source](candidate-source.md).
Choose the immutable Go image separately. Write the bounded requirement into a
UTF-8 file, then create a plan with an explicit request time:

```sh
mantik candidate-generation-plan \
  --hypothesis hypothesis.json --dataset dataset.json \
  --dataset-sha256 DATASET_CANONICAL_SHA \
  --toolchain-image-sha256 sha256:IMAGE_SHA \
  --model-label gpt-6.1-sol --requirement requirement.txt \
  --requested-at 2026-01-01T00:00:00Z > generation-plan.json
```

`mantik-candidate-generation-plan-v1` pins the sealed hypothesis, exact dataset
byte count/SHA-256 and canonical identity, declared Go runtime/image, fixed
`go-entry-price-bool-v1` interface, original requirement, caller model label,
explicit request time and complete deterministic prompt. The prompt includes
structured hypothesis/dataset/toolchain context and requests exactly this output
shape, without Markdown:

```json
{"schema":"mantik-candidate-generation-output-v1","source":"package candidate\nfunc Entry(p int64) bool { return p <= 10000 }\n"}
```

The requirement is 1..4,096 UTF-8 bytes without NUL. The model label is a bounded
identifier, never a command or endpoint. A requirement can describe a claimed gap
in existing methods; retaining it does not prove those methods are inadequate.
The plan hash uses compact Go JSON with its own `sha256` empty. It contains no
provider URL, credential, compiler command or service authority.

After obtaining an output artifact under separately authorized scope, record its
exact bytes and independently expected raw SHA-256:

```sh
mantik candidate-generation-record \
  --plan generation-plan.json --plan-sha256 PLAN_SHA \
  --dataset dataset.json \
  --output recorded-output.json --output-sha256 RAW_OUTPUT_SHA \
  --captured-at 2026-01-01T00:00:01Z > generation-recording.json
```

`mantik-candidate-generation-recording-v1` retains the full plan, exact output
bytes as base64, byte count/raw hash, explicit capture time, structured outcome,
optional complete candidate package and its own content hash. The capture time
cannot predate the request time. Both times are caller declarations, not proof
of when a provider ran. Recording first checks the plan and exact retained dataset.

| Output | Retained outcome |
| --- | --- |
| Strict supported object with nonempty valid source text | `candidate_recorded` / `source_payload_retained`, with an immutable candidate. |
| Empty file | `output_rejected` / `missing_output`, without a candidate. |
| Partial, binary/invalid UTF-8, Markdown, unknown/duplicate/mis-cased fields, trailing JSON or unsupported schema | `output_rejected` / `invalid_output`, preserving exact bytes. |
| Supported object with empty, oversized or NUL source | `output_rejected` / `invalid_source`, without a candidate. |

Source is decoded from the JSON string and retained without formatting or
rewriting, including CRLF. Invalid Go syntax, imports and unsupported behavior
are deliberately retained for later [isolated build](candidate-build.md) and
[behavior](candidate-behavior.md) checks. Recording source does not mean it will
compile, satisfy the interface, pass safety restrictions or execute successfully.

A successful record command means that the envelope is valid, including when its
output was rejected. It never hides a failed response behind a chosen candidate.
Within bounds, repeating identical inputs and explicit times produces identical
content. Changing request/output/source/hypothesis/dataset/image or time changes
the corresponding identity. These offline commands do not implement an attempt
journal or prevent a caller from discarding a file; retain every attempt.

The output bound is `6 * candidate.MaxSourceBytes + 4,096` bytes, accommodating
JSON escapes for the 64 KiB source limit. Larger files are refused without
truncation; preserve the original separately. Plans are bounded to
`candidate.MaxPackageBytes + 32,768` bytes. Recording bounds include the full plan,
base64 output, candidate package and envelope overhead. Strict JSON readers reject
ambiguous, altered or oversized plan/recording envelopes rather than repairing them.

## Inspect and export

```sh
mantik candidate-generation-inspect \
  --recording generation-recording.json --recording-sha256 RECORDING_SHA \
  --plan-sha256 PLAN_SHA --dataset dataset.json --artifact recording
```

Artifact choices are `recording` (default), `plan`, `prompt`, `output`, `candidate`
and `source`. Prompt/output/source exports write exact bytes without an added
newline. Candidate and source exports refuse rejected output before writing data.
All exports validate the entire envelope and expected recording/plan hashes.
With `--dataset`, inspection also verifies exact dataset presence/content; without
it, inspection checks the retained pins and internal evidence only. No original
file is overwritten by the command; shell redirection is the caller's responsibility.
The commands accept only explicit local regular files and no API URL or positional
arguments. They do not contact a provider, read credentials, compile code or launch
processes.

An exported candidate is exactly the existing `mantik-candidate-source-v1` package
for that source/hypothesis/dataset/toolchain. Its schema and identity rules are
unchanged, and it can enter the existing finite worker checks. Exporting the
candidate alone does not automatically attach the generation recording to a
service registration. Authenticated durable generation lineage and a provider
adapter remain separate work.

Authority is `recorded_candidate_generation_plan_only` for the plan and
`caller_recorded_candidate_generation_only` for the recording. Provider-called,
model-identity/authorship, compilation, execution, service-binding and qualification
claims remain false. A consistent caller recording can be fabricated; it does not
independently attest model authorship. No result activates a strategy or implies
profitability. This slice does not close #18 or authorize new model calls.

Tests cover deterministic bindings, malformed/partial/binary/empty responses,
retained invalid Go, self-hashed substitutions, timestamp and raw-dataset refusal,
strict bounds/JSON and exact CLI artifact export. Separate native service/worker
rehearsals prove those existing paths under their recorded source/image identities;
they do not qualify this offline generation path or establish provider behavior.
