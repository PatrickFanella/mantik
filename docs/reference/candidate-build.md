# Isolated candidate compile checks

The candidate build protocol checks a single-file Go interface in a separate
owned container. It compiles source and a fixed type assertion into a discarded
archive. Candidate `init`, `Entry` and other functions are never invoked. A pass
is interface evidence, not a behavioral test, strategy admission or qualification.

## Fixed interface and image

`go-entry-price-bool-v1` requires valid Go with package name `candidate`, no
imports and a top-level function named `Entry` with type `func(int64) bool`:

```go
package candidate

func Entry(priceCents int64) bool {
    return priceCents <= 10000
}
```

The application-owned harness is exactly `package candidate` followed by
`var _ func(int64) bool = Entry`, each ending with a newline. The parser checks
source shape; the compiler checks signature and types. This first interface
supports synthetic `DEMO` research. The Boolean is not a finalized decision or
permission to trade. Return values and safety behavior are not tested yet.

Build the dedicated Linux/amd64 image with a unique local tag:

```sh
docker build --platform linux/amd64 -t mantik-candidate-build-review \
  -f research/candidate/Dockerfile .
MANTIK_CANDIDATE_BUILD_IMAGE="$(docker image inspect --format '{{.Id}}' mantik-candidate-build-review)"
```

The Dockerfile pins Go 1.26.9 and Alpine base digests, copies the fixed compiler
to `/compiler/compile`, and runs Mantik as UID/GID 65532. Create the
[source declaration](candidate-source.md) with this image ID. A declaration for
another image keeps its identity; create a new declaration for a changed pin.

## Declare, launch and inspect

Use an existing private artifact directory. Set `MANTIK_CANDIDATE_ATTEMPT` to a
new directory under an existing parent for each physical launch. Retain all
artifacts outside Git.

```sh
./bin/mantik candidate-build-job --candidate candidate.json \
  --candidate-sha256 "$(jq -r .sha256 candidate.json)" > build-job.json
./bin/mantik candidate-build-launch --job build-job.json \
  --job-sha256 "$(jq -r .sha256 build-job.json)" \
  --approved-image-sha256 "$MANTIK_CANDIDATE_BUILD_IMAGE" \
  --out-dir "$MANTIK_CANDIDATE_ATTEMPT"
./bin/mantik candidate-build-reconcile --out-dir "$MANTIK_CANDIDATE_ATTEMPT" \
  --approved-image-sha256 "$MANTIK_CANDIDATE_BUILD_IMAGE"
./bin/mantik candidate-build-result --job build-job.json \
  --job-sha256 "$(jq -r .sha256 build-job.json)" \
  --file "$MANTIK_CANDIDATE_ATTEMPT/stdout.json"
```

Job declaration and result inspection are offline. Launch requires the approved
image to match the candidate's pin; it never pulls an image or accepts compiler
paths, flags, commands or environment from callers. `candidate-build-execute` is
the container-side adapter with fixed `/worker` inputs, not an isolation-enforcing
host command. Existing native/Python commands do not execute candidate source.

`mantik-candidate-build-job-v1` pins the complete immutable candidate, interface,
harness hash, `linux_amd64`, fixed resources and its own hash. Jobs are bounded
to 512 KiB plus 8192 bytes. Resources are 30 host seconds, 256 MiB memory/swap,
one CPU, 64 PIDs and a separate 20-second cooperative compiler budget.

Only job/source/harness files are mounted. Dataset bytes are not read by compile
checks: their pins remain provenance, not proof of usable, unseen or tested data.
The source declaration stays immutable and `declared`; the separate result
records this worker's compile check.

## Results and lifecycle

The adapter checks exact source/harness bytes and requires the fixed compiler's
`-V=full` output to equal `compile version go1.26.9`. It then invokes:

```text
/compiler/compile -complete -p candidate -o /dev/null /worker/candidate.go /worker/interface.go
```

There is no module resolution, dependency download, linking, candidate execution
or binary export. Compiler environment is fixed without caller credentials.
Diagnostics are UTF-8-normalized, hashed and bounded to 64 KiB. Truncation is
explicit and cannot count as completed conformance.

`mantik-candidate-build-result-v1` binds job/candidate/interface identities,
compiler-version check, status/reason, compilation disposition and diagnostics.
Completed status requires `interface_compiled` and `source_compiled=true`.
Failures distinguish input mismatch, interface rejection, unavailable/changed
compiler, type failure, compiler timeout/cancellation and diagnostic limits.
Envelopes are bounded to 512 KiB. Source execution, image/isolation
self-attestation and qualification credit remain false; authority is
`candidate_compile_conformance_only`.

The host uses the existing [owned-worker lifecycle](research-workers.md#explicit-local-host-launcher):
durable acquisition before creation, kernel lock, immutable image, pre-start
inspection, UID 65532, read-only root/input, network `none`, dropped capabilities,
no-new-privileges, fixed limits and no restart. Candidate acquisition/receipt
schemas are `mantik-candidate-build-acquisition-v1` and
`mantik-candidate-build-host-receipt-v1`, with authority
`candidate_compile_host_receipt_only`. Native and Python reconciliation reject
candidate attempts.

Reconciliation verifies actual ownership/profile and retained job/source/harness,
including the mounted job copy. A live worker before its deadline returns a
running error; `--cancel` deliberately stops only that owned attempt. Overdue
workers are stopped after inspection. Timely completion remains recoverable
after the deadline. Terminal receipts return unchanged after actual handle and
artifact checks. Changed ownership/profile or artifacts conflict instead of
causing recreation. A physical retry requires a new directory.

Launch emits a failed build's receipt and exits nonzero; reconciliation/result
inspection can succeed while returning that retained failure. Missing/malformed
output, inconsistent exits and uncertain process outcomes remain incomplete or
conflicted. Bounded logs do not prove earlier daemon output never rotated.

## Remaining work

This supplies compile/interface evidence and retained local physical attempts
for #18. [Fixture plans and recorded comparisons](candidate-fixture-tests.md)
now define expected signal cases for a later behavioral worker, without executing
source or attesting supplied outputs. Behavioral/property/failure replay, service-owned candidate registration
and search lineage, worker dispatch, strategy activation and the full issue gate
remain unfinished. Even functions with panicking initializers can compile here
because they never execute. Host settings supply inspected process-isolation
evidence, not a general sandbox security certification. No result grants
qualification, account access, risk-policy changes or profitability.

## Retained verification

The private `candidate-build-rehearsal-1792455447626361769` run retained 59
commands and nine actual worker handles. It checked a successful compile with a
panicking initializer left unexecuted, equal logical results across distinct
physical retries, retained type/signature failures, import rejection, changed
source and cross-protocol rejection. Two launcher-process losses, explicit
cancellation, late recovery of timely completion, a real 30-second host timeout
and separate 20-second compiler timeout passed. All nine final handles were
inspected stopped with PID 0. Original pilot/Switchyard handles were unchanged;
no provider or service request was made.

The earlier `candidate-build-rehearsal-1792455335509716910` attempt failed its
last timeout assertion because the renamed compiler in that fixture printed
`actual version go1.26.9`. The application correctly rejected this response as a
version mismatch before compiling. Its receipt, raw output, classification and
nine stopped handles remain retained. Only the private timeout fixture was
corrected to return the expected version probe before delaying compilation;
the fresh run above passed. No application version guard was weakened, and the
failed attempt is not counted as a complete pass.

`make verify`, `make test-race`, `make compose-build` and the dedicated pinned
candidate image build passed. There is no storage/schema change in this slice;
database integration was not rerun. Hosted head CI and merge/deployment evidence
remain separate from these local checks and runtime receipts.
