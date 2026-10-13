# Mantik agent instructions

Mantik is a Go/PostgreSQL autonomous paper-research service with terminal
clients. Its bootstrap is a synthetic, unqualified lifecycle fixture.

## Boundaries

- Keep research, decision evaluation, and operations separate.
- Evaluation exchanges structured evidence. Generate narrative explanations
  only from finalized decisions and their evidence. Preserve original source
  material for targeted inspection.
- Operations acts only on finalized, applicable decisions and enforces their
  safety contract. It must work independently of model availability.
- Every executable strategy version needs safety conditions and evaluations.
- Keep real observations, synthetic fixtures, and qualification evidence
  distinct. Never imply profitability from the demonstration.
- Do not connect to existing Augr data or production services during bootstrap.
- Preserve the Open Pilot forms and PR template. Draft issues are not pickup
  approval; add pickup labels only to reviewed, bounded work.
- Keep secrets, local data, binaries, and generated evidence out of Git.

## Verification

Use the pinned Go 1.26.9 toolchain (`mise exec -- …`). Run `make verify` for
changes. Run `make test-race` for concurrent behavior and `make test-integration`
for storage/schema changes. Integration uses new disposable containers and
must never use a development or production database. Run `make compose-build`
when application dependencies or container behavior change.

Update README and maintained docs when contracts, commands, or boundaries
change. Use ordinary feature branches and reviewable PRs on the Gitea origin.
