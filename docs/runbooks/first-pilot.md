# First running-app pilot

## Selected checkpoint

Run an isolated Mantik service after M2's durable operations are integrated:
trigger routing (#7), independent deadlines (#8), durable action delivery (#9),
and operational exceptions/readiness (#10). The first pilot lasts 24 hours and
uses controlled synthetic inputs with current timestamps.

The purpose is to prove that the application operates continuously, preserves
authority and evidence, and recovers predictably when interrupted. This is an
early development pilot, with operator supervision and fixture economics.
Real-feed behavior, profitability, and autonomous research remain separate
claims with their own acceptance gates.

M2 implementation (#7–#10) is merged. The operational pilot is the next
checkpoint. A full TUI, news/social
ingestion, copytrading, generated strategies, and conversational control are
not prerequisites for this checkpoint. The CLI/API must be sufficient to
inspect the case, action state, protective obligations, exceptions, and results.

See [pilot controls](pilot-controls.md) for the launcher, rehearsal commands,
and the remaining scenario work before starting the window.

## Entry criteria

- Merge #7–#10 and validate their combined current revision. Record the exact
  source SHA, image identity, schema version, and local check results. Read back
  current-head hosted CI when available; record an outage as unverified evidence.
- Prepare a pilot-specific Compose project, database volume, loopback ports,
  configuration, and fixture artifacts. Use Mantik-owned data and credentials.
  Verify that these resources do not belong to another development instance.
- Prepare repeatable current-time fixture scenarios and explicit expected
  outcomes. Keep input IDs stable for retries. The historical fixed demo alone
  cannot demonstrate the service's real-clock protection.
- Prepare state inspection, backup, and isolated restore procedures. Preserve
  the pre-run configuration and artifacts needed to reproduce the pilot.

These are preparation requirements, not completed implementation claims. The
pilot launcher and scenario commands must be made concrete against the final
#9/#10 contracts before starting the observation window. Selecting this
checkpoint does not change the production release dependencies in #25–#27.

## Acceptance scenarios

| Scenario | Required observed result |
| --- | --- |
| Normal fixture lifecycle | Current-time quotes produce a finalized entry and protective exit with linked evidence, actions, outcomes, and declared fixture P&L |
| Duplicate and conflicting input | Exact retries retain one receipt and economic effect; changed content under an existing ID conflicts |
| Independent protection | A holding deadline runs with no model or new quote; usable retained evidence permits the fixture exit, otherwise an explicit exception preserves the open position |
| Missing expected event | A due expectation routes work once; timely matching evidence suppresses the missing-event case |
| Worker interruption | Kill the pilot worker at declared pre/post-enactment points; lease recovery and reconciliation preserve at most one economic effect |
| Expired or changed authority | A pending action cannot execute after its validity or applicability is lost |
| Service restart | Retained decisions, actions, timer generations, receipts, and exceptions survive, and overdue work resumes safely |
| Dependency outage and recovery | Capability readiness reports the affected function; recovery does not erase history or weaken safety |
| Price-unavailable recovery | A later usable quote or supported bounded recovery resolves the exception with retained evidence; no missing price becomes an invented fill |
| Isolated restore | Restore a pilot backup into separate resources and verify retained identities, history, and pending obligations without duplicate effects or changing the original pilot |
| Continuous observation | For the declared 24 hours, collect worker progress, readiness, exceptions, and state readbacks; account for planned interruptions and any unexpected downtime |

The bootstrap supports one executable fixture case per aggregate. Use separate
pilot instances with new isolated databases for scenarios requiring a fresh case;
do not add a destructive reset or pretend that the fixture already supports a
multi-position portfolio. Keep every scenario's retained evidence.

## Evidence and completion

Before starting, declare the window's UTC start/end, scenario order, expected
results, and allowed interruptions. Store observations, logs, readbacks,
backup/restore receipts, and image/source identities outside Git. Link the
evidence location from the pilot report without committing private data.

After the window, compare each scenario's expected and observed results. Trace
an action from its input through evaluations, finalized decision, safety
contract, delivery/reconciliation state, and economic outcome or exception.
A healthy container or closed issue alone is insufficient proof.

Complete this checkpoint when every required scenario has supporting evidence
and unexpected failures are resolved and rerun. If a defect changes behavior,
record the revised source/image and rerun affected scenarios; declare a new
24-hour window when the earlier continuous-operation evidence no longer applies.
Keep failed attempts in the report.

## Following checkpoint

After this pilot, add the bounded price/calendar adapter in #11 and repeat the
relevant routing, outage, freshness, and recovery checks with authorized real
observations and recorded fixtures. Select the provider and coverage as part
of that issue before accessing it.

That establishes real-information operation. Account allocation, realistic
costs and execution, prospective performance, and strategy admission still
need the M5/M7 work. The first pilot does not claim those results or depend on
finishing the entire roadmap before operator feedback begins.

## Snapshot-bound backup data identity

New `backup` receipts use `mantik-backup-v2`. A read-only repeatable-read
transaction exports a PostgreSQL 17 snapshot and computes a sorted public-table
inventory with row counts and SHA-256 hashes of canonical JSON row values. The
custom-format `pg_dump` uses that exact exported snapshot. A concurrent write
after snapshot export cannot enter either the recorded identity or archive. The
receipt also retains the archive SHA-256 and original image/source manifest.
A failed attempt preserves its files and does not create a successful receipt.

For v2 archives, `restore` compares every public table's data identity before
starting application workers. Missing or changed rows, extra tables and changed
original byte values fail the comparison; the target database remains available
for inspection with application workers unstarted. Invalid v2 metadata is rejected
before resource preparation. Legacy receipts remain readable, but their restore
receipt reports `legacy_receipt_unavailable` and
`table_data_identity_verified:false`. They do not acquire the new proof.

This method checks table data, including retained schema-version/checksum rows,
source journals and allocation journals. It does not independently fingerprint
DDL, indexes, roles, sequence state, physical storage layout, provider authority
or pending protective behavior. Retain the matching image/source/schema and the
complete archive. It is not permission to restore over an existing service.
No existing pilot or curtailed observation window is restarted by this change.

`make test-integration` now seeds actual storage-owner copy and allocation
journals using explicitly unqualified fixtures in its new disposable database.
It restores the archive into another fresh database on that disposable server,
compares table data, verifies exclusion of a concurrent source write, and rejects
changed restored bytes. No application workers or external providers run during
this recovery check. An optional `MANTIK_RECOVERY_RECEIPT_DIR` outside Git retains
the archive and JSON receipt; without it, the integration check uses temporary
artifacts. This proves only the stated storage mechanics. Issue #25 still needs
recovery of actual pending economic and protective obligations under their
completed owners; #26/#27 release and natural qualification gates remain open.
