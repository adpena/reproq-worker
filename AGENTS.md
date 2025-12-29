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
Ensure that any changes to `internal/queue/models.go` are reflected in the Django `TaskRun` model in `reproq-django/src/reproq_django/models.py`. The two must remain in sync to prevent serialization errors.
