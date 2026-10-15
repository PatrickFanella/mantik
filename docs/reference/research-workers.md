# Research worker protocol and native adapter

The first worker-protocol implementation provides offline job declaration,
native-fixture execution and result validation. It supplies a comparison target
for later isolated external tools. The service does not dispatch these jobs.

Implementation proceeds in three units:

1. Versioned job/result contracts and a working native-fixture adapter, described
   here. This unit pins declarations and checks transport/result identities.
2. A host-controlled launcher that enforces isolation and resources, verifies the
   actual image, and retains acquisition, interruption and completion receipts.
3. A pinned external research-library adapter, compared with the native fixture,
   followed by explicit service integration. Issue #17 remains open.

## Offline commands

Use the built CLI and a new private artifact directory. The example image ID is
read from an existing local build; these commands do not launch that image.

```sh
umask 077
MANTIK_WORK_DIR="$(mktemp -d)"
MANTIK_WORKER_IMAGE="$(docker image inspect --format '{{.Id}}' mantik-dev-mantik:latest)"
./bin/mantik experiment --export-recipe > "$MANTIK_WORK_DIR/recipe.json"
./bin/mantik replay --export-dataset > "$MANTIK_WORK_DIR/dataset.json"
./bin/mantik worker-job --id native-worker-demo \
  --image-sha256 "$MANTIK_WORKER_IMAGE" \
  --recipe "$MANTIK_WORK_DIR/recipe.json" \
  --recipe-sha256 "$(jq -r .sha256 "$MANTIK_WORK_DIR/recipe.json")" \
  --input "$MANTIK_WORK_DIR/dataset.json" > "$MANTIK_WORK_DIR/job.json"
./bin/mantik worker-execute --job "$MANTIK_WORK_DIR/job.json" \
  --job-sha256 "$(jq -r .sha256 "$MANTIK_WORK_DIR/job.json")" \
  --input "$MANTIK_WORK_DIR/dataset.json" > "$MANTIK_WORK_DIR/result-1.json"
./bin/mantik worker-result --job "$MANTIK_WORK_DIR/job.json" \
  --job-sha256 "$(jq -r .sha256 "$MANTIK_WORK_DIR/job.json")" \
  --file "$MANTIK_WORK_DIR/result-1.json"
```

The commands accept no API origin, credentials, arbitrary command, environment
variables or executable path in the job. `worker-execute` runs the compiled
native engine in independent memory stores. It does not initialize the service,
load a database, contact a provider or submit observations/actions to a running
service.
It checks that the running Go runtime matches the declared `go1.26.9` pin.

## Job contract

`mantik-research-worker-job-v1` declares:

- Caller-chosen job ID and fixed tool `native-fixture-adapter-v1`.
- Runtime version and immutable image ID in `sha256:hex` form.
- Full sealed native recipe, including dataset identity, methods, strategy
  configurations, cutoffs, cost assumptions, benchmarks and planned metrics.
- Logical `dataset.json` input reference, JSON media type, exact file byte count
  and SHA-256, plus the separate canonical typed-dataset SHA-256 used by native
  execution. Whitespace changes alter the raw artifact pin.
- Fixed resource profile: 30 cooperative seconds, 256 MiB memory, one CPU,
  64 PIDs, 8 MiB input and 16 MiB result envelope.
- Logical `native-attempt.json` output reference and JSON media type.
- Authority `research_worker_declaration_only`, false qualification credit and
  a hash of the complete Go JSON declaration with its own hash field empty.

Job JSON is bounded to 1 MiB plus 4096 bytes; the recipe itself is at most 1 MiB.
Unknown, duplicate and case-aliased fields, invalid UTF-8, excessive nesting and
trailing JSON are rejected. Existing v1 library entries retain their capitalized
Go field names. Artifact references are fixed logical names, not host paths.
Declaring an input binds its bytes and shape; native dataset admission still
determines whether it is usable. No executable code or provider access is added.

## Result contract and evidence limits

`mantik-research-worker-result-v1` binds the job ID/hash, input-verification
disposition, cooperative deadline disposition and exact native result JSON.
The native artifact has its own byte count and SHA-256 and is bounded to 8 MiB.
Its attempt ID is `worker-` plus the SHA-256 of the job ID. Result validation
checks native identity, canonical input and recipe pins, ordered method pins,
required integer metrics, synthetic evidence class and false qualification.
Complete results require all planned methods and benchmarks. Failed results
may retain a prefix of completed methods. Replay frames remain raw evidence;
the protocol validator does not certify their computations or source truth.

Wrong raw input identity is rejected without a result. After input verification,
decode, canonical identity and output-limit failures produce bounded failure
envelopes. Native failures preserve their error code and complete native artifact.
A context already cancelled before input access produces `cancelled_before_input`
with `input_verified: false` and no native artifact. CLI execution prints valid
failure envelopes and exits nonzero. Result inspection validates evidence and
can exit successfully when inspecting a retained failed result.

Enforcement scope is `artifact_bounds_and_cooperative_context_only`. The adapter
enforces artifact bounds and uses a cooperative context for native work. It does
not kill an uncooperative process or interrupt a blocked input reader. Memory,
CPU and PID values are declarations for the future launcher. Image identity and
isolation verification remain false; a worker cannot attest its own container.
Specifying an image hash does not select or verify the current process's image.
Independent host evidence must establish those properties.

Successful repeated computations of the same job/input produce the same result
and hash. This is deterministic execution, not durable job acquisition or
idempotent dispatch. The adapter creates no journal or result file itself;
callers must retain original jobs and each physical attempt's output separately.
No existing service trial intent/run receipt is modified or imported automatically.
Process loss may leave no result, and automatic retry/recovery is not implemented.

Result authority is `synthetic_worker_protocol_only`, with qualification credit
always false. Protocol validation supplies neither statistical qualification,
paper profitability, execution authority nor a sandbox security attestation.
Schema 16 and existing running services are unchanged.
