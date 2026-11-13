# Contributing to Mantik

Use Go 1.26.9 from `mise.toml`. Read [AGENTS.md](AGENTS.md) and the
[architecture](docs/architecture.md) before changing application contracts.

1. Create or select a bounded issue and work on a feature branch.
2. Implement the smallest complete user-visible flow.
3. Run `mise exec -- make verify`. Run race and disposable database checks
   when their boundaries are affected; run `make compose-build` for image
   changes.
4. Update affected maintained documentation.
5. Open a GitHub PR with behavior, validation, limitations, and the issue link.

Use concise conventional commit subjects. Preserve unrelated changes and do
not commit generated evidence, credentials, local data, or binaries.

The bootstrap has no real-provider or live-trading authorization path. Add
capabilities through explicit contracts, evidence, and appropriate tests.
Open Pilot issue preparation is documented in [docs/open-pilot.md](docs/open-pilot.md).
