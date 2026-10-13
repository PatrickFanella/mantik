# Go Worker Postgres Docker Template

Go worker/service template with Postgres, Docker Compose, migrations, tests, and Open Pilot conventions.

Created from `subculture-collective/project-template`.

## Quick start

```bash
make verify
```

Rename package or module identifiers, CLI names, and this README after creating your repository. Bootstrap labels on the new repository, using its owner and name.

## Open Pilot

This repository includes draft, bug, feature, documentation, and reviewed ready-for-pickup issue forms, plus the PR template. Draft forms do not approve automation.
Bootstrap labels after creating a repo from this template:

```bash
open-pilot labels bootstrap OWNER/REPO
```

## License

GPL-3.0-only. See `LICENSE`.

## Local containers

Compose is a local development configuration. Copy `.env.example` to `.env` and set local values before starting services. `docker compose config --quiet` validates configuration without starting containers. The Postgres password default is for local development; choose deployment credentials before exposing services.

Run `make compose-build` to check image builds without starting services. CI runs this after `make verify`.
