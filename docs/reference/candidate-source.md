# Candidate source declarations

`candidate-seal` creates an offline, content-addressed research source package.
It retains caller-supplied source before any compilation or evaluation, including
source that will fail compilation. It is the first unit of
[#18](https://git.subcult.tv/subculture-collective/mantik/issues/18); isolated
compilation, interface checks, replay, failed-worker retention and service-owned
candidate registration remain unfinished.

## Declare and inspect

Use a new private directory and retain the original dataset alongside the
package. Select the intended Go toolchain image separately and set
`MANTIK_CANDIDATE_TOOLCHAIN_IMAGE_SHA256` to its immutable `sha256:...` identity.
These commands declare that identity; they do not pull or inspect an image.

```sh
umask 077
./bin/mantik hypothesis-declare --export-fixture > hypothesis.json
./bin/mantik replay --export-dataset > dataset.json
./bin/mantik candidate-seal --source candidate.go \
  --hypothesis hypothesis.json --dataset dataset.json \
  --dataset-sha256 "$(jq -r .sha256 dataset.json)" \
  --toolchain-image-sha256 "$MANTIK_CANDIDATE_TOOLCHAIN_IMAGE_SHA256" \
  > candidate.json
./bin/mantik candidate-inspect --file candidate.json \
  --sha256 "$(jq -r .sha256 candidate.json)" --dataset dataset.json
```

The commands read only explicit local regular files and write JSON to stdout.
There is no API origin, output-directory overwrite, database, compiler invocation,
source execution, model request, image pull or service mutation. Store the output
outside Git. The CLI requires the expected canonical dataset hash at creation
and the expected candidate hash at inspection; failed checks emit no package.
Normal shell redirection remains the caller's responsibility.

## Versioned package

`mantik-candidate-source-v1` retains:

- Exact UTF-8 Go source as `source.text`, its byte count and SHA-256, with the
  fixed logical reference `candidate.go`. Paths and commands are not accepted
  inside the package. Source is 1..65,536 bytes, cannot contain NUL, and is not
  parsed, formatted or rewritten. Invalid Go is deliberately retained.
- A complete sealed hypothesis declaration, including mechanism, prediction,
  null/refutation, horizon, origins and existing native method pins. Creation
  validates and detaches the declaration. The current supported evidence class
  is `synthetic_unqualified` for instrument `DEMO`.
- `dataset.json`'s exact byte count/SHA-256 and separate canonical typed-dataset
  SHA-256. Creation checks strict JSON shape and the existing sealed synthetic
  dataset contract. Dataset bytes remain a separate retained input artifact.
- Declared runtime `go1.26.9` and an immutable toolchain image hash. No actual
  process, compiler installation or image identity is verified at this stage.

The package hash is SHA-256 over compact Go JSON with `id` and `sha256` empty;
the candidate ID is `candidate-` followed by that same hash. Equal inputs produce
the same package. Any source-byte, hypothesis-declaration, raw dataset-byte or
toolchain change creates a different identity, including whitespace-only edits.
The package is detached from caller-owned hypothesis slices.

Inspection rejects changed hashes, unknown/duplicate/case-aliased fields,
trailing JSON, invalid UTF-8 and envelopes over 512 KiB. It reconstructs the
hypothesis seal and validates source, dataset-pin and toolchain shape. With
`--dataset`, it additionally verifies exact input bytes, typed content and
canonical identity. Without that flag it verifies the retained declaration
only; a hash does not attest that the dataset still exists or is trustworthy.

## Authority and remaining work

Status remains `declared`, authority is `caller_supplied_research_source_only`,
and qualification credit is false. `source_compiled`, `source_executed`,
`image_identity_verified` and `service_binding_verified` must all remain false,
even if a caller recomputes the hash after editing them. No generator identity
or actual model authorship is inferred from supplied source.

The hypothesis input is a local declaration, not an authoritative publication
or an admission receipt. Its source origins remain unverified. The package has
no strategy activation, account-selection or risk-policy capability. A later
service-owned candidate registration must recheck authoritative publication and
dataset/recipe bindings before scheduling an evaluation.

This contract establishes immutable source provenance, not Go interface
conformance, isolation, reproducible compilation, usable strategy behavior or
profitability. The existing native and Python worker profiles cannot execute
this source. A separately versioned worker contract must define the candidate
interface, compiler/runtime image, resource bounds, credential-free isolation,
expected checks/results and interruption/retry behavior before execution exists.
