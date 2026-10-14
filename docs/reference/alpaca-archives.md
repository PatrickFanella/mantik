# Alpaca journal archive and isolated restore

These commands copy and verify the private capture journal outside Git. They do
not archive the application database, provision provider access, deploy a service,
submit research inputs, or activate a restored collector. Keep database backup and
receiver-history proof separate from journal byte and contract verification.

## Coherent archive

`alpaca-archive` opens an existing journal and owns its exclusive process lock
throughout the copy. An active collector or submitter causes a busy error. The
command never recreates a missing source. It copies every flat regular file,
including failed responses, submission attempts, session receipts, unknown
private files, and interrupted temporary evidence. Only the process lock is
excluded; a restored opener creates its own lock.

Source and archive directories must be absolute, owned by the caller, and mode
0700; files must be regular, mode 0600, and owned by the caller. Symlinks,
subdirectories within the journal, invalid leaf names, and oversized files are
rejected. Leaf inspection and opened-inode comparison prevent in-journal links
from being accepted as ordinary evidence. These commands run on the Linux host
as the journal owner.

The destination must be new and outside the source directory, with an existing
parent. Existing archives are never overwritten. Files are copied one at a time
with exclusive creation and file sync; the file directory and archive directory
are synced. `archive.json` is written last. It contains the journal identity,
creation time, sorted file names, sizes, per-file SHA-256 values, and total bytes.
The returned SHA-256 identifies the exact archive manifest. Keep that receipt
separately; reading a hash only from the archive being checked cannot establish
an expected identity.

The archive has a 512 MiB total bound, the existing 8 MiB per-file bound, and the
journal's 20,000 directory-entry bound. Commands have a two-minute deadline.
Capacity, privacy, or cancellation failures preserve the source. Interrupted
archive destinations are retained and never automatically resumed or cleaned up.
An absent/invalid completion manifest is not a completed archive.

```bash
umask 077
MANTIK_JOURNAL_ARCHIVE="$HOME/.local/state/mantik/journal-archive-r1"
./bin/mantik alpaca-archive --journal "$MANTIK_CAPTURE_JOURNAL" \
  --destination "$MANTIK_JOURNAL_ARCHIVE" \
  > /private/path/archive-receipt.json
MANTIK_ARCHIVE_SHA256="$(jq -r .sha256 /private/path/archive-receipt.json)"
./bin/mantik alpaca-archive-verify --archive "$MANTIK_JOURNAL_ARCHIVE" \
  --sha256 "$MANTIK_ARCHIVE_SHA256"
```

Check command success and the retained receipt before using its digest. Archive
verification requires exactly the manifest's file set and hashes, with no missing
or extra files and no symlinked directory or leaf. It verifies bytes and privacy,
not journal validity, source authenticity, entitlement, or provider profitability.
This distinction lets a private archive preserve corrupted evidence for inspection
without declaring it ready to run. The archive is not encrypted; move/store it
under the existing private backup policy if an off-host copy is needed.

## Isolated restore and qualification

```bash
MANTIK_RESTORED_JOURNAL="$HOME/.local/state/mantik/journal-restore-r1"
./bin/mantik alpaca-restore --archive "$MANTIK_JOURNAL_ARCHIVE" \
  --sha256 "$MANTIK_ARCHIVE_SHA256" \
  --destination "$MANTIK_RESTORED_JOURNAL" \
  > /private/path/restore-receipt.json
./bin/mantik alpaca-journal --journal "$MANTIK_RESTORED_JOURNAL"
```

Restore verifies the expected archive identity and complete file inventory before
creating a new destination. It copies each file with the expected byte hash and
verifies the written copy again. It preserves the original journal ID, raw bytes,
normalized outputs, input identities, receiver/API-origin binding, submission
attempts, acknowledgements, collection/session history, and temporary evidence.
It never replaces an existing destination or writes inside the archive.

A synced `restore-pending` guard precedes all copied files. Current journal
openers refuse a guarded directory. The guard is removed only after byte checks
and the current journal contract audit pass, with directory sync. Cancellation,
copy failure, malformed captures, normalization drift, orphan delivery receipts,
or invalid collection/session history leave the copy and guard for inspection.
Do not remove the guard to make a failed restore usable. A reviewed recovery needs
a fresh isolated destination after diagnosing the retained failure.

The restore receipt distinguishes `bytes_verified` from `contracts_verified`.
The audit reports capture/input/acknowledgement/pending/uncertain counts, binding,
failed and incomplete collection counts, stopped and incomplete session counts,
and retained temporary-file count. Failed and incomplete histories are preserved
as such; a valid copy does not promote them into successful runs. Unknown regular
files are retained but do not provide execution authority.

Use the archive/restore-capable revision for verification and subsequent journal
commands. Older binaries do not enforce the restore guard and are not qualified
to inspect or run a partial destination. Archive creation leaves the source's
manifest and all retained bytes unchanged; no database migration is added.

A successful isolated restore is a recovery rehearsal. It does not select a new
canonical journal, prove a process is live, or prove current server history.
Keep the original journal and services intact. Before any explicitly authorized
continuation, select one canonical journal, inspect actual runtime ownership, and
verify the compatible receiver's current retained history. Submission keeps the
same target binding and freshly verifies existing acknowledgements. Missing or
changed server history still blocks automatic replay.

## Retention boundary

The policy remains keep all evidence and stop at capacity. These commands provide
a tested coherent-copy and isolated-restore path; they do not delete captures,
rotate journals, reset collection limits, rebind receivers, or schedule recurring
collection. A new collection destination requires an operator decision with its
source and recovery receipts retained. Continuous-feed retention, independently
retrieved off-host backup proof, provider entitlement/readiness, and prospective
profitability qualification remain separate requirements.
