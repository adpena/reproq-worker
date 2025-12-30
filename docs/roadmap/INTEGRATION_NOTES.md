# Integration notes and constraints

This file captures cross-project constraints that should be honored when implementing backlog items.

## Build and test requirements
- Any Go code change requires a rebuild: `go build -o reproq ./cmd/reproq`.
- Any logic change should run: `go test ./internal/...`.

## Versioning
- Increment `Version` in `cmd/reproq/main.go` when logic changes.
- Ensure `reproq --version` matches the stored worker version.

## Release and cross-compilation
When preparing a release, build binaries for:
- macOS: `GOOS=darwin GOARCH=amd64` and `GOOS=darwin GOARCH=arm64`
- Linux: `GOOS=linux GOARCH=amd64` and `GOOS=linux GOARCH=arm64`
- Windows: `GOOS=windows GOARCH=amd64`

## Django integration
- Changes to `internal/queue/models.go` must be mirrored in `reproq-django/src/reproq_django/models.py`.
- The JSON protocol between the Go worker and `python -m reproq_django.executor` is strict; update both sides together.

## Migration discipline
- Schema changes require a new migration file in `migrations/`.
- Update integration tests if the schema changes.

## Docs and CLI parity
- CLI help text, README, and docs should match shipped commands.
- If a command is not implemented, it should be clearly marked as future work.
