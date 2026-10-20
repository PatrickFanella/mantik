# Candidate outcome captures

`internal/candidateevidence` owns the portable intent/outcome/capture values and
pure validators. `internal/workerreceipt` owns the shared acquisition, receipt,
pre-start snapshot and profile checks. Neither package imports the launcher,
replay or storage. Storage can consume this evidence without importing Docker
ownership or creating a replay dependency cycle.

The launcher's existing exported types and reader remain compatibility aliases
and a wrapper. Local journal/filesystem reads and Docker commands stay in
`internal/workerlaunch`. This package separation changes no JSON schema, field
order, content hash, resource profile, CLI command or persistence version.

`candidate-worker-capture` exports a retained receiver-bound local outcome as
`mantik-candidate-outcome-capture-v1`. It reads the existing journal and artifacts;
it does not inspect Docker, reconcile a pending request, launch or stop a worker,
or upload an outcome to the service.

This is a portable caller-reported evidence contract for #18. Validation checks
structure, content hashes and declared links. It grants no execution verification,
current host-state knowledge, qualification, strategy activation or profitability
claim. A caller can construct or reseal consistent artifacts; hashes do not make
them independent host attestations.

## Export and inspect

Use the same full service-intent export and independently inspected pins used by
the [receiver-bound local journal](candidate-attempt-journal.md#receiver-bound-execution).

```sh
umask 077
./bin/mantik candidate-worker-capture \
  --journal /absolute/private/receiver-candidate-attempts \
  --intent service-intent.json --intent-sha256 "$intent_sha" \
  --receiver-id "$receiver_id" --request-id threshold-001 \
  --approved-image-sha256 "$image" > capture.json

# Use the expected capture SHA from the inspected retained export.
./bin/mantik candidate-capture-inspect \
  --capture capture.json --capture-sha256 "$capture_sha" \
  --receiver-id "$receiver_id" --request-id threshold-001 \
  --intent-sha256 "$intent_sha"

./bin/mantik candidate-capture-inspect \
  --capture capture.json --capture-sha256 "$capture_sha" \
  --receiver-id "$receiver_id" --request-id threshold-001 \
  --intent-sha256 "$intent_sha" --artifact stdout > stdout.json
```

`--artifact stderr` exports exact stderr bytes. Raw exports add no newline and
preserve arbitrary bytes, including invalid UTF-8. Both commands reject unknown
flags and positional arguments; neither accepts service URLs or cancellation.
All identities and the complete capture validate before output is emitted.

Successful export or inspection means the retained artifact is consistent. A
capture of a mismatch, failure, cancellation or incomplete result can still be
inspected successfully. Read the nested receipt status; exit zero from an
inspection command does not mean candidate checks passed.

## Contents and validation

A capture contains:

- The original journal root and full local attempt, including its bound service
  intent and original outcome/capture time.
- The acquisition, immutable image/job pins, and exact pre-start evidence bytes.
- Exact stdout and stderr bytes, encoded as base64 in JSON.
- Authority `caller_reported_candidate_outcome_capture_only`, with
  `execution_verified`, `current_host_state_known` and `qualification_credit`
  false, and a SHA-256 over compact Go JSON with its own `sha256` empty.

Export validates the whole journal before selecting a request. Unrelated corrupt
rows or changed retained output block export. The selected full service receipt
must equal the pinned expected receipt. A pending request has no outcome capture:
export returns an unresolved error without collecting or inventing a result.
Unbound legacy journals cannot be adopted through this command.

The portable validator checks the local intent, service/job identity, outcome and
acquisition hashes, declared root/request path, image, timestamps, receipt status,
output sizes and byte hashes. It validates available worker-result bytes against
the job and matches their reported identity/status. A completed, mismatching or
failed typed worker result must satisfy the corresponding receipt constraints.
Malformed output is retained under an incomplete/control-failure receipt and
cannot acquire a valid worker-result identity by omission.

Pre-start evidence is checked against the acquisition, container identity and
fixed worker resource profile. Its consistency must agree with the nested
receipt's reported `profile_verified` flag. An absent pre-start artifact is an
empty byte array; malformed evidence remains opaque and cannot support that flag.
The nested receipt records a historical host assertion. Checking its supplied
snapshot still does not verify that a real host executed the profile.

Stdout is bounded to 16 MiB, stderr and pre-start evidence to 64 KiB each. The
full document is bounded by `workerlaunch.MaxCandidateCaptureBytes`, including
base64 expansion and nested intent/job receipts. Readers reject unknown fields,
case aliases, duplicate keys, trailing documents and oversized captures. Empty
artifact arrays are retained explicitly rather than replaced with null.

No export timestamp is added: repeated reads of the same retained evidence
produce the same capture identity. Portable validation performs no filesystem or
Docker access and remains possible when the original host is unavailable. It
cannot prove that the original owner still retains the journal or all attempts.

Raw dataset bytes are not repeated in this transport. Local export checks them
through the journal's existing source/harness/dataset validation. A receiving
service must compare the embedded service intent with its own retained intent
using `VerifyIntent`, then check the job against its own retained registration and
dataset. Trusting the receipt embedded by the caller is insufficient.

Keep captures private: they contain source, output and local host paths. This
portable contract itself performs no service admission. The
[outcome backends](candidate-worker-outcomes.md) now retain validated reports and
full capture bytes against their own intent/registration/dataset snapshots.
Authenticated outcome submission and pinned capture/output inspection are
available, together with coherent intent/report history. Owner fencing, generated-source
provenance and the final #18 acceptance audit remain separate work. Existing pilot services are unchanged.
