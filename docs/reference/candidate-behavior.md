# Synthetic candidate behavioral workers

`candidate-behavior-launch` compiles, links and executes a candidate against
its [sealed fixture plan](candidate-fixture-tests.md) in one explicitly approved,
owned Docker container. It compares all expected Boolean annotations and retains
mismatches, build failures, panics, timeouts and partial output. This is a bounded
synthetic example check. It does not activate a strategy or grant qualification.

## Declare and launch

Build the dedicated image and declare a candidate against its immutable image
identity using [candidate-seal](candidate-source.md). A compile-only image is not
a behavioral image: this profile also needs a linker and pinned runtime archives.
Do not replace an existing candidate's image pin or an existing attempt directory.

```sh
umask 077
docker build -f research/behavior/Dockerfile -t mantik-candidate-behavior:local .
image=$(docker image inspect --format '{{.Id}}' mantik-candidate-behavior:local)
# Seal candidate.json against this image, then produce test-plan.json as described
# in the linked candidate-source and candidate-fixture-tests references.
./bin/mantik candidate-behavior-job --plan test-plan.json \
  --plan-sha256 "$(jq -r .sha256 test-plan.json)" \
  --dataset dataset.json > behavior-job.json
./bin/mantik candidate-behavior-launch --job behavior-job.json \
  --job-sha256 "$(jq -r .sha256 behavior-job.json)" \
  --dataset dataset.json --approved-image-sha256 "$image" \
  --out-dir /absolute/private/new-behavior-attempt > host-receipt.json
```

The `mantik-candidate-behavior-job-v1` artifact retains the complete fixture plan,
fixed harness hash, 15-second total build budget, 5-second run budget and existing
30-second host profile. Its hash is compact Go JSON with `sha256` empty. Plan and
candidate identities remain unchanged. The dataset is reverified before creating
an attempt, inside the worker, and during reconciliation. Jobs accept no commands,
compiler options, environment values, mount paths or resource overrides.

## What executes

The first profile accepts valid Go `package candidate` source with no imports and
a top-level `Entry(int64) bool` function. It rejects compiler directives, a source
`main` function, reserved `_mantik` identifiers, references to `println`, and
shadowed `int64`/`bool` identifiers. The worker changes only the package name to
`main`, writes this transformed source and the fixed harness into a temporary
workspace, and verifies compiler and linker versions are `go1.26.9`.

The compiler produces a main archive; the linker uses only the image's fixed
runtime import configuration. The harness calls Entry once per planned price in
retained dataset order, in one process. Candidate initialization runs once and
candidate state persists between calls. Expected annotations are not compiled
into the executable. This is not independent per-case execution, chronological
market replay or a property-testing engine.

The executable runs with a fixed environment containing no caller credentials.
Its standard output must be empty; standard error must contain exactly the fixed
ordered Boolean protocol. Candidate `print` output, malformed/extra lines and
output over the cap fail the check. Panics and nonzero exits retain the valid
prefix of already returned values. Missing values count every remaining case.
The worker retains each subprocess output stream and build diagnostics up to
32 KiB as text, replacing invalid UTF-8 bytes with `?`. It marks truncation
explicitly and kills the process group on cancellation
or timeout. A timeout cannot become a completed run even if a retained prefix
matches all supplied annotations.

## Host isolation and recovery

The dedicated profile keeps the root and input mount read-only, networking off,
all capabilities dropped, no-new-privileges enabled, non-root UID/GID 65532,
256 MiB memory/swap, one CPU, 64 PIDs and no automatic restart. Its added writable
workspace is a 64 MiB executable `/tmp` tmpfs with nosuid/nodev. This exception is
specific to behavioral jobs. Native, Python and compile-only worker profiles
reject added tmpfs mounts. Host inspection verifies this profile before start
and before accepting a result or stopping an owned handle.

The private directory retains the job, exact input artifacts, acquisition intent,
pre-start inspection, stdout/stderr and terminal host receipt. Reconciliation
checks the same job/image/profile and actual container identity; it never starts
or recreates a container. Live handles before their deadline require a later
inspection or explicit `--cancel`. Lost/missing handles remain unresolved.

```sh
./bin/mantik candidate-behavior-reconcile \
  --out-dir /absolute/private/new-behavior-attempt \
  --approved-image-sha256 "$image"
# Add --cancel only to request stopping this owned attempt.
./bin/mantik candidate-behavior-result --job behavior-job.json \
  --job-sha256 "$(jq -r .sha256 behavior-job.json)" \
  --file /absolute/private/new-behavior-attempt/stdout.json
```

Launch emits a host receipt and exits nonzero for mismatches, failures and
incomplete attempts. A successful result-inspection or reconciliation command
means evidence was read; inspect its status. A terminal receipt remains immutable
on repeated reconciliation. A fresh physical retry requires a new directory and
acquisition identity; identical logical results may have the same result hash.
The other worker reconcilers reject behavioral attempts.

## Evidence and remaining limits

`mantik-candidate-behavior-result-v1` retains plan/candidate/job hashes, build/run
stage flags, all returned values, matched/mismatched/missing counts, bounded text
outputs and failure reasons. `completed` requires every annotation to match;
`mismatch` requires a clean complete run with disagreements. `failed` preserves
partial execution and never sets `all_annotations_match`. The result reader
recomputes counters and validates the ordered output prefix and content hash.

Worker authority is `synthetic_candidate_behavior_worker_only`; its image and
isolation verification flags remain false. Host authority is separately
`synthetic_candidate_behavior_host_receipt_only`, backed by actual Docker
inspection and the retained launch intent. A standalone worker result is not a
host attestation. Both artifacts keep qualification credit false. Container
isolation is not a proof against compiler/runtime vulnerabilities or a substitute
for stronger isolation when that becomes a deployment requirement.

The offline recorder/comparator remains caller-reported and separate. Neither
artifact format establishes independently assessed gold labels, unseen data,
complete candidate search history, general property/failure coverage, realistic
fills, financial safety, forecasting skill, strategy admission or profitability.
Issue #18 remains open for its remaining lineage and testing requirements.

Use the [local candidate attempt journal](candidate-attempt-journal.md) to retain
intent before launch and keep request-ID retries and every local outcome in one
bounded history. The lower-level commands remain available for explicit owned
attempts without that journal.

[Sample property checks](candidate-properties.md) can separately assess declared
relations over every eligible pair of retained outputs. They do not replace the
worker's example comparison or establish properties beyond those samples.

[Cutoff replay projections](candidate-replay.md) can derive availability-ordered
inputs for explicit historical fixture cutoffs. Each frame remains a separate
worker run and does not replay service economics.
