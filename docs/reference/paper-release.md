# Isolated paper release preflight

Issue #26 requires an exact source/image/schema identity, configured capabilities,
fresh Mantik-owned data, verified backups, deployment authorization and rollback
evidence. The source/image preflight below implements only the build portion.
It does not inspect a running service or database, read credentials, deploy,
start a container, qualify a strategy or authorize a natural soak.

## Capture one build attempt

Start from a reviewed, committed checkout with no tracked or untracked changes.
Use the repository's pinned Go 1.26.9 toolchain for local verification. Retain
local checks, exact-head PR CI and merged-main CI separately; a green build does
not replace any of them.

```bash
mise exec -- make verify
python3 scripts/release_preflight.py \
  --out-dir "$HOME/.local/state/mantik/release-preflight-$(date -u +%Y%m%dT%H%M%SZ)"
```

The destination must be a new directory outside the repository. Existing
attempts, including failures, are never overwritten. The command:

- Selects the current full Git revision and tree only after a clean-state check.
- Creates `source.tar` with `git archive` from that revision. Ignored local data,
  `.env` files and uncommitted changes are not build inputs.
- Records the archive SHA-256 and every source SQL migration's SHA-256. A missing
  migration in the numbered sequence fails the preflight.
- Sends that archive directly to `docker build --network=default`, with labels
  derived from the selected revision and archive digest. The existing Dockerfile
  downloads Go modules and Docker resolves base images through the normal build
  network. This is a dependency build, with no service or application provider
  or model call. An offline dependency cache is not assumed.
- Retains `build-command.json`, `build.log` and `image.id`, then inspects that
  exact local image ID and checks its source labels, non-root user and Mantik
  entrypoint. It does not inspect container environments.
- Records success or failure in `receipt.json`. A changed checkout or retained
  archive during the attempt fails the final check.

`source_schema_maximum` describes the archived SQL sequence, not a live database.
The migration runner must still verify the database's actual version and
checksums. The Dockerfile uses version tags for base images; these are not
immutable dependency pins. This procedure records the resulting local image ID
and source linkage, not bit-for-bit build reproducibility, base-image provenance,
a signed attestation or a registry publication. The binary currently embeds no
release revision, and labels are meaningful only with the retained build receipt.

No tag is moved and no image is pushed. Keep the exact image ID with the receipt;
do not substitute a mutable Compose tag when reviewing a release candidate.
Failed attempts retain their source and logs. If a build timeout leaves Docker
builder work active, inspect that work before rerunning; use a new receipt
directory for any later attempt. Do not prune unrelated images or caches.

## Gates before an isolated deployment

The receipt always has `deployment_ready: false` and lists unmet gates. Review
each gate against the actual intended target before requesting a deployment:

| Gate | Required evidence |
| --- | --- |
| Capabilities | Actual configuration and enabled worker/timer/provider paths, with source classes and authority limits. A built image does not prove configuration. |
| Fresh data | A separately identified Mantik-owned project, volume and receiver; no Augr data, existing pilot volume or Switchyard changes. Record ports and resource ownership. |
| Credentials | Non-secret readiness through the existing authorized secret mechanism; never copy credentials into the receipt, Git or chat. This preflight reads none. |
| Backup | A pre-change snapshot/dump, source/schema identities and an isolated restore proof from that same snapshot. The [storage-owner recovery contract](storage-owner-recovery.md) proves fixture replay only. |
| Financial recovery | Pending obligations, unknown effects, protection continuity and atomic financial replay proven for the intended capabilities. Current no-grant deferral replay does not satisfy this gate. |
| Authorization | Explicit target and capability scope for deployment. A source/image build and the general development schedule do not supply deployment authorization. |
| Post-release checks | Actual running image ID, receiver/schema checksums, data ownership, readiness and scoped API/terminal behavior, retained outside Git. |
| Rollback | Matching previous image/binary and verified pre-change data, an isolated restore/restart rehearsal, and retained resulting identities. |

The development Compose file declares fixed project `mantik-dev`, development
credential defaults and named data storage. It is not an isolated release plan.
Do not run its `up`, reuse an existing pilot project, or replace a service as part
of this preflight. No deployment command is included here because target,
capabilities and authorization remain separate gates.

## Rollback prerequisites

Before a separately authorized deployment, retain the previous immutable image
ID, source revision, schema checksums, configuration identity and a consistent
pre-change backup. Prove restore into a new isolated database and replay through
the matching binary. Keep the original database and snapshot intact while
reviewing that proof. Never delete migration markers or run an older binary
against a newer schema as a downgrade procedure.

Actual rollback must follow the approved target's stop/restore/start procedure
and record the resulting image, schema, owner and pending-obligation identities.
No production restart or rollback is performed by this document or preflight.
Issue #26 and the prerequisite development/recovery issues remain incomplete;
issue #27's predeclared natural soak must wait for those gates.
