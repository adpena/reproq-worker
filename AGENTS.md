# Reproq Worker Agent Instructions

You are an AI agent assisting with the development of the Reproq Go Worker.

## Build Requirements
Any modification to the Go source code requires a rebuild of the binary to verify compilation.

- **Local Build**: `go build -o reproq ./cmd/reproq`
- **Verification**: Run `go test ./internal/...` after any logic changes.

## Release & Cross-Compilation
The `reproq-django` project depends on pre-built binaries being available in GitHub releases. When preparing a release, ensure binaries are built for the following targets:

```bash
# macOS (Intel & Apple Silicon)
GOOS=darwin GOARCH=amd64 go build -o reproq-darwin-amd64 ./cmd/reproq
GOOS=darwin GOARCH=arm64 go build -o reproq-darwin-arm64 ./cmd/reproq

# Linux
GOOS=linux GOARCH=amd64 go build -o reproq-linux-amd64 ./cmd/reproq
GOOS=linux GOARCH=arm64 go build -o reproq-linux-arm64 ./cmd/reproq

# Windows
GOOS=windows GOARCH=amd64 go build -o reproq-windows-amd64.exe ./cmd/reproq
```

## Integration Consistency
- **Versioning**: When logic changes, increment the `Version` constant in `cmd/reproq/main.go`.
- **Validation**: Ensure `reproq --version` returns the expected string.
- **Model Sync**: Any changes to `internal/queue/models.go` must be mirrored in `reproq-django/src/reproq_django/models.py`.
- **Protocol**: The JSON contract between the Go worker and `python -m reproq_django.executor` is strict. Changes must be applied to both simultaneously.

## Code/Doc Alignment Notes
- **Worker DSN**: `--dsn` overrides `DATABASE_URL`; docs should reflect that `DATABASE_URL` is optional when flags are provided.
- **Worker Version**: `RegisterWorker` uses the CLI `Version` constant; bump it on logic changes and keep `reproq --version` aligned.
- **Torture Tool**: `cmd/torture` builds a separate binary and is not a `reproq` subcommand.
- **Statuses**: Workflow logic uses `WAITING`; keep status lists and docs in sync.

## Deployment Access
- The agent has access to GitHub (`gh`) and Render CLIs and can use them for releases, CI monitoring, and deployments when requested.
