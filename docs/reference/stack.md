# Stack reference

- Module: `git.subcult.tv/subculture-collective/mantik`
- Go: 1.26.9, pinned in `go.mod`, `mise.toml`, and Docker builds.
- Database: PostgreSQL 17, with pgx v5.
- Executable: `bin/mantik`, serving the CLI and persistent service.
- Local Compose project: `mantik-dev`.
- Local bindings: 127.0.0.1:40880 (service), 127.0.0.1:45432 (database).
- Runtime image runs as UID/GID 65532 with a read-only filesystem in Compose.
- CI verifies source, race behavior, disposable PostgreSQL contracts, and image
  builds through the existing Gitea workflow.

The template supplied GPL-3.0-only and Open Pilot forms. Mantik retains them.
`make verify` is the repository's standard check. See the development runbook
for additional storage and concurrency checks.
