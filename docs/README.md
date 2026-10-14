# Mantik documentation

- [Roadmap](roadmap.md): milestones, issue links, dependencies, and exit gates.
- [Delivery model](delivery.md): board workflow and evidence requirements.
- [Architecture](architecture.md): product model, responsibilities, contracts,
  implemented boundaries, and next capabilities.
- [Development](runbooks/development.md): local service, CLI, API, and checks.
- [First running-app pilot](runbooks/first-pilot.md): the post-M2 checkpoint,
  24-hour observation window, recovery scenarios, and evidence requirements.
- [Architecture decisions](adr/README.md): durable implementation choices.
- [Research references](research/README.md): outside projects and design ideas.
- [Augr corpus assessment](research/augr-corpus-assessment.md): recovered source
  locations, hashes, candidate mappings, and reproduction gaps.
- [Stack reference](reference/stack.md): toolchain and persistence.
- [Dataset contracts](reference/datasets.md): content pins, provenance, replay
  cutoffs, and quality admission.
- [Experiment contracts](reference/experiments.md): baseline methods, controls,
  declared recipes, metrics, and retained attempts.
- [Trigger contracts](reference/triggers.md): persistent conditions, evidence
  windows, case routing, retries, and finalized protection.
- [Timer contracts](reference/timers.md): independent deadlines, missing events,
  fenced recovery, clock changes, and price-unavailable exceptions.
- [Action contracts](reference/actions.md): finalized intent, durable delivery,
  adapter effects, uncertain outcomes, and reconciliation.
- [Operational inspection](reference/operations.md): scoped readiness,
  persistent exceptions, worker evidence, and bounded recovery.
- [Feed collector contracts](reference/feed-collectors.md): immutable feed
  declarations, durable lease ownership, retry receipts, and readiness limits.
- [Open Pilot](open-pilot.md) and [labels](labels.md): bounded task preparation.

Code and runnable evidence take precedence over documentation. Keep observed
provider results and deployment receipts outside source control. Documentation
of a planned capability is not implementation or qualification evidence.
