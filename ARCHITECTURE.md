# Architecture: Reproq Worker

## 1. Package Layout

The project follows a standard Go project layout, optimized for a single-binary multi-command CLI.

```text
/
├── cmd/
│   ├── reproq/           # Unified CLI entry point (worker, beat, replay).
├── internal/
│   ├── config/           # Configuration management (env vars, defaults).
│   ├── db/               # Database connection pool.
│   ├── queue/            # Queue operations (Claim, Heartbeat, Complete, Periodic).
│   ├── executor/         # External process execution (Python).
│   ├── runner/           # Worker orchestration loop and heartbeat management.
│   ├── logging/          # Structured logging setup (slog).
│   └── web/              # Health/metrics HTTP server (enabled via metrics flags).
├── go.mod
├── go.sum
└── README.md
```

## 2. Core Components

The committed migrations in `reproq-django` own the PostgreSQL schema. This
worker checks their versioned contract and actual public relation bindings;
it contains no independent schema initializer or migration runner.

### Unified CLI (`cmd/reproq`)
The binary supports multiple subcommands:
*   `worker`: The primary execution node. Polls the database for READY tasks and executes them.
*   `beat`: The scheduler. Enqueues `PeriodicTask` entries into the `task_runs` table based on cron expressions.
*   `replay`: Utility to re-enqueue a specific task by its ID.
*   `cancel`: Marks a running task as cancellation requested.
*   `triage`: Lists, inspects, and retries failed tasks.
*   `limit`: Manages rate limits in the database.
*   `prune`: Deletes expired tasks based on `expires_at`.
*   `reclaim`: Performs explicit, bounded or dry-run recovery of expired claims.
*   `capabilities`: Reports schema and executor protocol compatibility as JSON without a database connection.

### Queue Service (`internal/queue`)
Encapsulates all PostgreSQL interaction using `pgx`.
*   **Claiming**: Uses `FOR UPDATE SKIP LOCKED`, ordered key-scoped transaction locks, then a fresh READ COMMITTED capacity check to enforce lock and concurrency keys without a global claim lock. Every nonempty key participates, including limit 0. Effective capacity is the smallest positive candidate/active-member limit; no positive limit means unbounded.
*   **Fencing**: Heartbeat and completion require the worker owner, per-claim UUID token, `RUNNING` status and an unexpired database-clock lease. Cancellation wins a late success acknowledgment. Leaving `RUNNING` clears the token.
*   **Periodic Tasks**: Go owns cron advancement for definitions assigned to `go`. Both Go and optional `pg_cron` call the migration-owned SQL enqueue function; neither serializes or hashes a periodic spec. Occurrence identity is `(periodic_name, scheduled_for)` across all statuses, independent of spec-hash dedupe.

### Runner (`internal/runner`)
Orchestrates the lifecycle of a worker:
*   **Concurrency**: Uses a buffered channel as a semaphore to limit simultaneous tasks.
*   **Heartbeat**: Spawns a dedicated goroutine for every running task to renew its lease in the database.
*   **Exponential Backoff**: Implements a `2^attempt * base_delay` backoff for failed tasks.

### Executor (`internal/executor`)
Invokes the Django task executor:
*   **Protocol**: Communicates via `stdin` (JSON payload) and `stdout` (JSON result).
*   **Compatibility**: Probes the configured Python provider before registration or claims and requires schema v2 / executor protocol 2.
*   **Database roles**: Go reads `REPROQ_DATABASE_URL` for its queue. Python inherits its original business `DATABASE_URL`; choosing a separate queue database does not retarget application data access.
*   **Isolation**: Tasks run in a separate process, protected by the worker's timeout and resource limits.

### Executor Contract
The worker sends a canonical spec JSON to `python -m reproq_django.executor` and expects a JSON result envelope.
For the exact schema, error payloads, and `logs_uri` semantics, see:
https://github.com/adpena/reproq-django/blob/main/docs/payloads.md

## 3. SQL Schema

### `task_runs`
The central queue table.
*   `result_id`: BIGSERIAL primary key.
*   `spec_hash`: SHA256 of the task specification. Used for deduplication.
*   `status`: Enum (`READY`, `RUNNING`, `WAITING`, `SUCCESSFUL`, `FAILED`, `CANCELLED`).
*   `run_after`: Scheduling timestamp.
*   `leased_until`/`leased_by`/`lease_token`: Expiry, owner and per-claim fencing.
*   `canonical_spec_text`/`spec_version`: Immutable SDK byte source for version 1. Version 0 retains its original historical payload/hash.
*   `periodic_name`/`scheduled_for`/`periodic_provider`: Occurrence identity and provider provenance; ad hoc replay preserves provider provenance without reusing occurrence identity.
*   `worker_ids`: JSON array of worker IDs that have claimed the task.

### `periodic_tasks`
Stores cron-based task definitions.
*   `cron_expr`: Standard cron syntax.
*   `next_run_at`: Next scheduled execution time.
*   `scheduler_provider`: Explicit `go`, `pg_cron` or inactive historical `unassigned` ownership.
*   `canonical_spec_text`/`canonical_spec_hash`/`spec_version`: Stored SDK source copied unchanged into each new occurrence.

### `workflow_runs`
Tracks group/chord workflow state and callback readiness.

### `reproq_workers`
Registry of active worker nodes. Used for monitoring and telemetry in the Django Admin.
*   `queues`: JSON array of queue names handled by the worker.

## 4. Reliability Invariants

1.  **Lease Protection**: A task can only be finalized if the worker still holds the current token and a valid, non-expired lease.
2.  **Occurrence Identity**: The database enforces one row per periodic definition and nominal UTC instant across all statuses. Separate occurrences with the same canonical hash remain separate runs.
3.  **Atomic Scheduling**: `beat` enqueues and updates the `next_run_at` in a single database transaction.
4.  **Provider Semantics**: Go coalesces one overdue nominal tick and advances after database time while preserving configured cron zones. Native `pg_cron` uses UTC trigger-minute identity and its own late/missed-trigger behavior.
5.  **Single DDL Authority**: Django migration 0003 owns the SQL primitive and schema v2 marker. Runtime checks exact public relations, raw migration-history cardinality and immutable function identity before queue use; the worker performs no DDL.
6.  **Legacy Cutover**: Selected READY version 0 runs are refused before claim mutation. Replay/retry refuse legacy sources atomically. Drain legacy queued work with the old runtime or an explicitly audited action before deploying; terminal history stays readable and is never relabeled.
