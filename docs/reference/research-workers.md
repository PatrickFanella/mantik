# Research worker protocol and native adapter

The first worker-protocol implementation provides offline job declaration,
native-fixture execution and result validation. It supplies a comparison target
for later isolated external tools. The service does not dispatch these jobs.

Implementation proceeds in three units:

1. Versioned job/result contracts and a working native-fixture adapter, described
   here. This unit pins declarations and checks transport/result identities.
2. The explicit local launcher below enforces a fixed container profile, verifies
   the actual image and retains acquisition, interruption and completion evidence.
3. The separate [Backtesting.py fixture adapter](backtesting-fixture.md) compares
   four synthetic methods in a pinned Python environment. Separate
   `external-worker-*` commands provide versioned job/result validation and
   durable launch/reconciliation with an explicit Python profile. Native worker
   v1 schemas and profile remain unchanged. The
   [current protocol acceptance refresh](../research/research-contract-acceptance.md#external-worker-protocol-refresh-2026-10-19)
   verifies this gate after its dependency was resolved; service integration is
   separate work.

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
CPU and PID values are declarations for the host launcher. Image identity and
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

## Explicit local host launcher

`worker-launch` runs one physical attempt in a new private directory:

```sh
./bin/mantik worker-launch --job "$MANTIK_WORK_DIR/job.json" \
  --job-sha256 "$(jq -r .sha256 "$MANTIK_WORK_DIR/job.json")" \
  --input "$MANTIK_WORK_DIR/dataset.json" \
  --approved-image-sha256 "$MANTIK_WORKER_IMAGE" \
  --out-dir "$MANTIK_WORK_DIR/launch-1"
./bin/mantik worker-reconcile --out-dir "$MANTIK_WORK_DIR/launch-1" \
  --approved-image-sha256 "$MANTIK_WORKER_IMAGE"
```

The launcher is Linux-specific. It uses `/usr/bin/docker` and the local Unix
socket `/var/run/docker.sock`, with a new empty Docker configuration directory
and a scrubbed client environment. It accepts no Docker command, remote daemon,
registry credentials, arbitrary worker command or runtime overrides. The approved
image argument must match the sealed job. Image pulls and fallback images are
disabled. Retain a local tag for the approved image; replacing a shared build tag
can leave an earlier image ID unavailable. A missing image is a retained setup
failure, rather than permission to select another image.

The attempt directory must be new, under an existing parent. It is canonicalized
and created with mode 0700. The launcher atomically retains the job, exact input
and `mantik-worker-acquisition-v1` receipt before Docker creation. The receipt
pins a random invocation identity, absolute directory, job/image hashes, owned
container name and 30-second host deadline. Each file and containing directory
is synced before acknowledgment. A nonblocking kernel file lock protects active
launch/reconciliation; process death releases the lock. Existing directories are
never reused for execution. A deliberate physical retry needs a new directory,
preserving the prior attempt and the original job/result identities.

Before start, host inspection verifies the actual image, invocation/job labels,
entrypoint/arguments, non-root UID 65532, environment containing only the expected
PATH, one read-only input mount, read-only root filesystem, network `none`, dropped
capabilities, no-new-privileges, no host PID/IPC/UTS namespace, no devices or port
bindings, no restart policy, 256 MiB memory with no extra swap, one CPU and 64 PIDs.
It retains a separate pre-start snapshot before starting the verified container.
Docker uses a bounded local log profile: one 20 MiB file with compression disabled.
The [Docker local-driver documentation](https://docs.docker.com/engine/logging/drivers/local/)
describes the log-size, rotation and compression options.

On cancellation, deadline or interrupted wait, the launcher inspects the actual
handle and labels before killing only its owned running container. It then
rechecks terminal state and profile. Missing handles, unavailable Docker or changed
ownership/profile leave an unresolved error; they never cause recreation or a
blind restart. Create/start diagnostics are retained separately when available.

Reconciliation reads the acquisition, locks the attempt and inspects its actual
container. A running worker before deadline returns an explicit running error.
To cancel that owned running attempt deliberately:

```sh
./bin/mantik worker-reconcile --out-dir "$MANTIK_WORK_DIR/launch-1" \
  --approved-image-sha256 "$MANTIK_WORKER_IMAGE" --cancel
```

An overdue running worker is stopped after inspection. A worker already finished
on time can be recovered after the deadline without re-execution. A terminal
receipt is immutable; repeated reconciliation verifies the actual stopped handle
and retained artifact hashes and returns that receipt. A terminal container that
was externally restarted or changed causes a conflict. Moving/copying the attempt
directory does not transfer ownership of a live container.

`mantik-worker-host-receipt-v1` records the acquisition/container/image identity,
actual exit and finish time, host deadline disposition, profile checks, bounded
stdout/stderr hashes and sizes, validated worker result identity when present,
status and reason, collection time and its own hash. Raw streams are retained as
`stdout.json` and `stderr.txt`, bounded to 16 MiB and 64 KiB. `output_complete`
means the bounded Docker log read finished successfully; it does not attest that
the daemon never rotated earlier output. This native adapter emits one bounded
result; arbitrary external tools need a separately reviewed transport policy.
Invalid, missing or truncated results remain incomplete. Native failures, late
results, cancellation and timeouts remain distinct. The launcher prints retained
receipts and exits nonzero unless execution completed; reconciliation can exit
successfully when inspecting a retained failed/cancelled attempt.

Completed status requires a valid worker envelope, matching exit status, retained
pre-start proof, matching final profile and actual finish before the host deadline.
Worker self-attestation flags remain false. Host profile verification is an
observation of the declared settings, not a sandbox security certification or
proof of image computations. Authority is `synthetic_host_launch_receipt_only`,
with qualification credit false. No result is admitted into service trial runs,
and no service scheduler, provider adapter or operational account is used. Stopped
containers and attempt evidence are retained; removal is an operator action.
