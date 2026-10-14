# Isolated pilot controls

`scripts/pilot.py` is a Python standard-library utility for preparing and
supervising synthetic Mantik stacks. Docker Compose is required. It never uses
`mantik-dev`, publishes no database port, and creates a random Compose project
with a new database volume. The service binds only to the selected loopback port.
The script has no volume deletion command. `stop` retains containers, volumes,
credentials, failed attempts, and evidence.

## Prepare and rehearse

Run from the repository. Evidence directories must be new and outside the
checkout. Choose free ports. Before preparation, ensure the local `postgres:17`
image is available (`docker pull postgres:17` if necessary). Preparation pins its
local image ID and builds Mantik from `git archive HEAD`, so uncommitted source,
local data, and credentials do not enter the application image. Record hosted CI
separately; the launcher does not infer CI success from a build or merge.

```bash
pilot_dir="$HOME/.local/state/mantik/rehearsal-$(date -u +%Y%m%dT%H%M%SZ)"
python3 scripts/pilot.py prepare --dir "$pilot_dir" --port 41880
python3 scripts/pilot.py start --dir "$pilot_dir"
python3 scripts/pilot.py smoke --dir "$pilot_dir"
python3 scripts/pilot.py snapshot --dir "$pilot_dir"
python3 scripts/pilot.py backup --dir "$pilot_dir"
```

Preparation records the source SHA, immutable application/PostgreSQL image IDs,
resource inventory, and build log. Configuration and bearer credentials live in
the private evidence directory (mode 0700; files created under umask 077). Do not
share the directory wholesale or commit it. HTTP evidence excludes Authorization
headers, but state and inputs can still be private.

`start` waits for fixture-operation readiness. Real feed readiness remains
unconfigured. `smoke` requires an untouched aggregate, sends a current-time entry,
waits for its durable action to finish, retries identical content, verifies a
conflicting retry returns 409, and then sends a stop-gap exit. It checks exactly
two observations and two economic outcomes with fixture P&L of -6000 cents.
It retains every HTTP request/response and logs. A failed attempt is retained;
create another directory rather than resetting its database. Never retry a
partially completed smoke against the same aggregate.

## Missing-event routing rehearsal

Use another new directory and free loopback port. This rehearsal leaves the
executable case untouched and opens only a research case.

```bash
routing_dir="$HOME/.local/state/mantik/routing-$(date -u +%Y%m%dT%H%M%SZ)"
python3 scripts/pilot.py prepare --dir "$routing_dir" --port 41891
python3 scripts/pilot.py start --dir "$routing_dir"
python3 scripts/pilot.py routing-smoke --dir "$routing_dir"
```

`routing-smoke` declares a real-time deadline 20 seconds ahead, registers two
missing-event obligations, and supplies timely matching evidence for one.
It checks definition/input retries and conflicts, waits for both timer receipts,
and requires exactly one research case for the absent event and no match for
the observed event. It verifies no economic actions were created, restarts only
this stack's application, and checks that completed timer generations/receipts
and case match counts remain unchanged across further worker cycles. The stack
must have no observations or registered definitions before the command starts.
Failed or partially completed attempts remain in their own directories.

## Controlled action-delivery boundaries

Normal stacks have no delivery gate. Deliberate interruption rehearsals use
`prepare --gate before_submit` or `prepare --gate after_submit` in separate fresh
resources. The application pauses outside database transactions after retaining
write-ahead intent, either before submission or after the independent fixture
ledger commits and before aggregate acknowledgment. `reached.json` records the
stage and original intent; the rehearsal also reads the independent ledger to
establish the economic boundary. A `release` file resumes the worker without
changing its clock, lease, validity, price, or safety checks.

```bash
delivery_dir="$HOME/.local/state/mantik/delivery-$(date -u +%Y%m%dT%H%M%SZ)"
python3 scripts/pilot.py prepare --dir "$delivery_dir" --port 41893 --gate after_submit
python3 scripts/pilot.py start --dir "$delivery_dir"
python3 scripts/pilot.py delivery-smoke --dir "$delivery_dir" --scenario post-crash
python3 scripts/pilot.py stop --dir "$delivery_dir"
```

Run each of the following in a different new directory/stack:

| Scenario | Preparation gate | Expected result |
| --- | --- | --- |
| `pre-crash` | `before_submit` | SIGKILL before the ledger commit; restarted worker waits for the original lease, then expires the original 30-second decision with no economic effect |
| `post-crash` | `after_submit` | SIGKILL after the ledger commit; restarted worker reconciles exactly one entry even after original decision expiry, without submitting it again |
| `changed-authority` | `before_submit` | A new quote changes applicability while the original action is paused; releasing it rejects the original intent with no economic effect |
| `expiry` | `before_submit` | Wait past original validity, release, and retain terminal expiry with no economic effect |

The command checks original intent preservation, terminal action stability,
economic event/ledger counts, and position consistency. The gate has no reset;
its marker is never overwritten. Erroring or crashed attempts remain retained.
The private parent directory stays mode 0700; only the mounted gate directory is
writable by the container UID. Marker content is readable within that private
parent so the host can collect it. Do not put secrets in the gate directory.

These gates deliberately interrupt delivery and can make action readiness
stale or unhealthy. Expired/rejected actions remain visible as exceptions.
`observe` refuses gated stacks. Set neither `MANTIK_FIXTURE_GATE_DIR` nor
`MANTIK_FIXTURE_ACTION_GATE` in an ordinary deployment. Invalid gate configuration
fails startup; enabled gates log a warning and refuse non-fixture intent. Gates
cannot force an effect or widen original authority. The user-selected future
Alpaca live path must not reuse this fixture-only qualification mechanism.

## Backup and isolated restore

`backup` prints the dump path. A single PostgreSQL custom-format snapshot covers
the aggregate, observations, audit events, migration history, and independent
fixture-effect ledger. The adjacent receipt contains its checksum and source
manifest. Do not back up only the aggregate.

Use the exact printed dump path and a new target directory/port:

```bash
python3 scripts/pilot.py restore --dir "$pilot_dir" \
  --dump /absolute/path/to/backup-EXACT.dump \
  --target "$HOME/.local/state/mantik/restore-UNIQUE" --port 41881
```

Restore checks the checksum and source identity before creating resources. It
starts only the new database, restores with error checking, captures the aggregate
before starting workers, and then starts the same application image. Readiness confirms
worker/capability health; it does not mean every pending action has finished.
A restored live lease must expire before a new worker can claim it. Wait for
the declared terminal action state before asserting recovery completion. Inspect
`restored-before-workers.jsonl`, the restore receipt, status, operations, and logs.
For a completed lifecycle, compare input/decision/action/outcome identities,
observation count, audit events, and effect ledger against the backup. For pending
work, compare the restored snapshot before workers start, then account for safe
post-start progress separately. A successful restore command alone is not proof
that every pending obligation behaved correctly. Inspect the original stack again
to establish that the restore did not change it.

## Remaining acceptance scenarios

The automated smoke covers the normal lifecycle and input retry/conflict cases;
`routing-smoke` adds missing-event delivery, timely-event suppression, and
completed timer restart retention.
It does not automate the entire acceptance table in [first-pilot.md](first-pilot.md).
Before starting a qualification window, retain a scenario plan with expected
outcomes, commands, identities, and allowed interruptions for the remaining cases:

- Use a fresh stack for independent holding protection. Enter at current time,
  wait for the completed entry, and retain its `exit_by`. The registered strategy
  has a real one-hour holding limit and a 60-second quote freshness limit. With
  no new quote, the overdue deadline must retain an open position and an explicit
  price-unavailable exception. Do not alter timestamps or database state to speed
  up the test. A later fresh quote or bounded `retry-protection` request must
  resolve protection with evidence under the existing safety contract.
- Run `routing-smoke` in a separate fresh stack and retain its timer/routing
  receipts, retry evidence, and restart readbacks.
- Restart only this directory's project to inspect retained state and overdue
  work. Test a database outage through this project's `postgres` service, inspect
  capability readiness, then restore it and account for the recorded faults.
- Run the four controlled delivery scenarios above and retain their boundary
  markers, independent ledger readbacks, original intent, terminal outcomes, and
  interruption logs. A random restart alone does not prove a specific crash
  boundary. Disposable integration tests remain separate evidence.

For manual Compose operations, use both `--project-name` from `manifest.json` and
`--file /absolute/evidence/directory/compose.json`. Never substitute the development
Compose file, remove volumes, or edit retained intents. Save commands, UTC times,
expected results, readbacks, and failures alongside the HTTP evidence. The CLI's
`operations`, `readiness`, and `recover` commands also support authenticated
inspection; see [development.md](development.md).

## Observation window

Create a separate fresh stack for the observation window. A completed smoke
aggregate is useful for rehearsing restore, but cannot supply another entry.
Resolve the scenario plan and preparation gaps above before qualification.

```bash
python3 scripts/pilot.py observe --dir /absolute/path/to/prepared-window-stack
```

`observe` writes a UTC start/end declaration and samples fixture readiness every
30 seconds for 24 hours in the foreground. Its default allowed-interruption list
is empty; run disruptive scenario rehearsals separately. Keep the supervising
terminal/process alive. Cancellation leaves an incomplete window without a
success receipt. Each sample retains the worker health and capability report;
finish also captures full state, exceptions, and logs. Unhealthy or unavailable
samples and supervision gaps longer than 45 seconds count as failures. Even a clean observation result explicitly leaves
all-scenario qualification false: a human must compare the complete acceptance
table against retained evidence and report any gaps or failures.

Use `snapshot` at scenario boundaries and `stop` after retaining the final
readbacks. To stop during an outage, use the exact project/file Compose command
because `stop` first captures evidence and fails if that capture is unavailable.
No pilot window starts during `prepare`, `start`, `smoke`, `backup`, or `restore`.
