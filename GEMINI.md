# Gemini Project Context: reproq-worker

## Project Overview
`reproq-worker` is a production-grade, deterministic background task execution service written in Go. It is designed to act as a scalable backend for Django 6.0's Tasks API, replacing traditional brokers like Celery or RQ with a Postgres-backed queue.

### Core Technologies
- **Language**: Go 1.22+
- **Database**: PostgreSQL 15+ (Required for `SKIP LOCKED` support)
- **Database Driver**: `jackc/pgx/v5`
- **Executor**: External Python processes (invoked via `python -m`)
- **Isolation**: Sub-process isolation with size-limited log buffers and security module validation.

### Architecture
The project follows a modular Go structure:
- `cmd/reproq`: Unified CLI entry point for all subcommands.
- `internal/queue`: Core domain logic for the Postgres queue, including atomic claiming (CTE + `SKIP LOCKED`), fencing tokens, and lease management.
- `internal/runner`: Orchestration layer that manages the worker lifecycle, heartbeats, and graceful shutdowns.
- `internal/executor`: Abstraction for executing tasks, including a security validator and a mock mode for benchmarking.
- `internal/config`: Centralized configuration management via environment variables and CLI flags.
- `internal/db`: Postgres connection pool configuration and health checks.
- `internal/logging`: Structured logging setup (slog).
- `internal/web`: HTTP health/metrics server (enabled via metrics flags).

## Building and Running

### Key Commands
- **Build**: `go build -o reproq ./cmd/reproq`
- **Run Worker**: `./reproq worker --dsn "postgres://..."`
- **Start Scheduler**: `./reproq beat`
- **Replay Task**: `./reproq replay --dsn "postgres://..." --id 123` or `--spec-hash <hash>`
- **Set Rate Limit**: `./reproq limit set --key "global" --rate 10`
- **Prune Expired**: `./reproq prune expired --dsn "postgres://..."`
- **Version**: `./reproq --version`

### Torture Tool (Separate Binary)
- **Run**: `go run ./cmd/torture --dsn "postgres://..." --count 1000`
- **Build**: `go build -o reproq-torture ./cmd/torture`

### Development & Testing
Automated via `Makefile`:
- `make test`: Runs unit tests for executor and validator.
- `make test-integration`: Spins up a Dockerized Postgres, applies migrations, and runs full lifecycle integration tests.
- `make up`: Starts the Postgres container for local development.

## Development Conventions

### Deployment & Release Requirements
To support the automated installation via `reproq-django`, releases must include pre-built binaries following this naming convention:
- `reproq-{os}-{arch}` (e.g., `reproq-darwin-arm64`, `reproq-linux-amd64`, `reproq-windows-amd64.exe`)

### Build Command for AI Agents
When major changes are made to the Go worker logic (internal/queue, internal/runner), agents should remind the user to rebuild or run:
```bash
go build -o reproq ./cmd/reproq
```

### Current Behavioral Notes
- Queue polling is round-robin across configured queues (`--queues` / `QUEUE_NAMES`).
- Worker registration uses the CLI `Version` constant.
- Config reads `DATABASE_URL`, `WORKER_ID`, `QUEUE_NAMES`, `ALLOWED_TASK_MODULES`, `REPROQ_LOGS_DIR`, and `PRIORITY_AGING_FACTOR`; metrics settings come from `METRICS_ADDR`/`METRICS_AUTH_*` or flags.
- `HEALTH_ADDR` is a legacy env var and is not wired to the metrics server (use `--metrics-addr` / `METRICS_ADDR`).

## Reliability Invariants
- **Fencing**: Every terminal state update (`SUCCESSFUL`, `FAILED`) must verify the `worker_id` and `RUNNING` status to prevent zombie commits.
- **Heartbeat-Execution Link**: If a heartbeat fails to renew a lease, the task execution context must be cancelled immediately.
- **Lease Reaper**: A background process must periodically return expired `RUNNING` tasks to `READY`.
- **Concurrency Guard**: Dedupes via `spec_hash` checks before enqueue; a partial unique index can reinforce this in Postgres.

### Security Posture
- **Validation**: All task module paths must be validated against an allow-list in `internal/executor/validator.go`.
- **Resource Capping**: `stdout` and `stderr` capture is capped at 1MB to prevent memory exhaustion.
- **Log Hygiene**: Sensitive payloads in `payload_json` are redacted from standard structured logs.

### Coding Style
- **Structured Logging**: Use `log/slog` for all logging, ensuring `worker_id` and `task_id` are included in context.
- **Error Handling**: Use sentinel errors (`ErrNoTasks`) and proper wrapping with `%w` for database operations.
- **Performance**: Favor single-trip atomic queries (CTEs) over multiple transactional round-trips.
