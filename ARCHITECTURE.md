# Architecture: Reproq Worker

## 1. Package Layout

The project follows a standard Go project layout, optimized for a single-binary multi-command CLI.

```text
/
├── cmd/
│   ├── reproq/           # Unified CLI entry point (worker, beat, replay).
│   └── torture/          # Load test helper binary (not a reproq subcommand).
├── internal/
│   ├── config/           # Configuration management (env vars, defaults).
│   ├── db/               # Database connection pool.
│   ├── queue/            # Queue operations (Claim, Heartbeat, Complete, Periodic).
│   ├── executor/         # External process execution (Python).
│   ├── runner/           # Worker orchestration loop and heartbeat management.
│   ├── logging/          # Structured logging setup (slog).
│   └── web/              # Health/metrics HTTP server (present, not wired by default).
├── migrations/           # SQL migration files.
├── go.mod
├── go.sum
└── README.md
```

## 2. Core Components

### Unified CLI (`cmd/reproq`)
The binary supports multiple subcommands:
*   `worker`: The primary execution node. Polls the database for READY tasks and executes them.
*   `beat`: The scheduler. Enqueues `PeriodicTask` entries into the `task_runs` table based on cron expressions.
*   `replay`: Utility to re-enqueue a specific task by its ID.

### Queue Service (`internal/queue`)
Encapsulates all PostgreSQL interaction using `pgx`.
*   **Claiming**: Uses `FOR UPDATE SKIP LOCKED` for atomic, high-concurrency task selection.
*   **Fencing**: All updates (`Heartbeat`, `CompleteSuccess`, `CompleteFailure`) verify the `leased_by` worker ID and the `RUNNING` status to prevent zombie executions from committing results.
*   **Periodic Tasks**: Handles cron parsing and atomic enqueuing with deduplication (via `spec_hash` and `status` checks).

### Runner (`internal/runner`)
Orchestrates the lifecycle of a worker:
*   **Concurrency**: Uses a buffered channel as a semaphore to limit simultaneous tasks.
*   **Heartbeat**: Spawns a dedicated goroutine for every running task to renew its lease in the database.
*   **Exponential Backoff**: Implements a `2^attempt * base_delay` backoff for failed tasks.

### Executor (`internal/executor`)
Invokes the Django task executor:
*   **Protocol**: Communicates via `stdin` (JSON payload) and `stdout` (JSON result).
*   **Isolation**: Tasks run in a separate process, protected by the worker's timeout and resource limits.

## 3. SQL Schema

### `task_runs`
The central queue table.
*   `result_id`: BIGSERIAL primary key.
*   `spec_hash`: SHA256 of the task specification. Used for deduplication.
*   `status`: Enum (`READY`, `RUNNING`, `WAITING`, `SUCCESSFUL`, `FAILED`).
*   `run_after`: Scheduling timestamp.
*   `leased_until`/`leased_by`: Concurrency control and heartbeat tracking.
*   `worker_ids`: JSON array of worker IDs that have claimed the task.

### `periodic_tasks`
Stores cron-based task definitions.
*   `cron_expr`: Standard cron syntax.
*   `next_run_at`: Next scheduled execution time.

### `workflow_runs`
Tracks group/chord workflow state and callback readiness.

### `reproq_workers`
Registry of active worker nodes. Used for monitoring and telemetry in the Django Admin.
*   `queues`: JSON array of queue names handled by the worker.

## 4. Reliability Invariants

1.  **Lease Protection**: A task can only be finalized if the worker still holds a valid, non-expired lease.
2.  **Deduplication**: The `beat` enqueue path checks for existing active tasks with the same `spec_hash` before inserting; a partial unique index can reinforce this on Postgres.
3.  **Atomic Scheduling**: `beat` enqueues and updates the `next_run_at` in a single database transaction.
