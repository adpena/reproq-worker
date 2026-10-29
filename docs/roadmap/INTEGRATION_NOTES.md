# Integration notes and constraints

This file captures cross-project constraints that should be honored when implementing backlog items.

## Build and test requirements
- Use `make build` for a local development binary and `make check` for the canonical local gate.
- Follow [DEPLOYMENT.md](../../DEPLOYMENT.md) for reviewed production artifacts and containers.

## Versioning
- Increment `Version` in `cmd/reproq/main.go` when logic changes.
- Ensure `reproq --version` matches the stored worker version.

## Release and cross-compilation
The canonical builder in `scripts/build_releases.sh` owns the platform list and
build flags. Its manifest records the selected platforms and source identity;
do not maintain another compilation loop here.

## Django integration
- Changes to `internal/queue/models.go` must be mirrored in `reproq-django/src/reproq_django/models.py`.
- The JSON protocol between the Go worker and `python -m reproq_django.executor` is strict; update both sides together.

## Migration discipline
- Schema changes require a forward migration in `reproq-django`, the sole DDL authority.
- Update integration tests if the schema changes.

## Docs and CLI parity
- CLI help text, README, and docs should match shipped commands.
- If a command is not implemented, it should be clearly marked as future work.
