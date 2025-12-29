# Architecture: Reproq Worker

## 1. Package Layout

The project follows the [Standard Go Project Layout](https://github.com/golang-standards/project-layout).

```text
/
├── cmd/
│   └── worker/           # Main entry point. Bootstraps config, DB, and starts the Runner.
├── internal/
│   ├── config/           # Configuration management (env vars, defaults).
│   ├── db/               # Database connection pool and raw SQL queries (pgx).
│   ├── models/           # Domain models and DB struct definitions.
│   ├── queue/            # Queue operations: Poll, Claim, Heartbeat, Finalize.
│   ├── executor/         # Abstraction for executing external processes (Python).
│   ├── runner/           # Orchestration: ties Queue and Executor together in the worker loop.
│   └── logging/          # Structured logging setup (slog).
├── migrations/           # SQL migration files (schema definition).
├── go.mod
├── go.sum
└── README.md
```

## 2. Package Responsibilities

*   **`cmd/worker`**:
    *   Parses command-line flags.
    *   Loads `internal/config`.
    *   Initializes the `internal/db` connection pool.
    *   Sets up signal handling (SIGTERM/SIGINT) for graceful shutdown.
    *   Instantiates the `Runner` and starts the polling loop.

*   **`internal/config`**:
    *   Reads environment variables (e.g., `DATABASE_URL`, `WORKER_ID`, `POLL_INTERVAL`).
    *   Validates configuration constraints.

*   **`internal/queue`**:
    *   Encapsulates the SQL logic for the queue.
    *   **Poll**: Executes the `SELECT ... FOR UPDATE SKIP LOCKED` query to find eligible tasks.
    *   **Claim**: Atomically updates task state to `RUNNING` and sets lease.
    *   **Heartbeat**: Extends `leased_until` for long-running tasks.
    *   **Complete**: Updates task state to `SUCCESSFUL` or `FAILED` based on execution result.

*   **`internal/executor`**:
    *   Constructs the command line for the external Python executor.
    *   Manages the OS process lifecycle (Start, Wait, Kill/Timeout).
    *   Captures `stdout` and `stderr`.
    *   Returns a `Result` struct containing exit code, output, and JSON envelope.

*   **`internal/runner`**:
    *   The "Brain" of the worker.
    *   Runs the main loop:
        1.  Calls `queue.Fetch()`.
        2.  If task found, starts a heartbeat goroutine.
        3.  Calls `executor.Execute()`.
        4.  Handles the result (Success/Retry/Fail).
        5.  Calls `queue.Complete()`.
    *   Implements backoff/sleep when the queue is empty.

*   **`internal/models`**:
    *   Contains Go structs representing the `task_runs` table.
    *   Defines enums for `TaskStatus` (PENDING, RUNNING, etc.).

## 3. SQL Schema & Strategy

### Strategy
*   **Database**: PostgreSQL 13+ (required for `SKIP LOCKED`).
*   **Driver**: `jackc/pgx` (for performance and pool management).
*   **Migrations**: Uses a simple migration tool (e.g., `golang-migrate` or raw SQL files managed in `migrations/`).

### Schema

```sql
-- Enums for state safety
CREATE TYPE task_status AS ENUM (
    'PENDING',
    'RUNNING',
    'SUCCESSFUL',
    'FAILED',
    'RETRYING',
    'CANCELLED'
);

CREATE TABLE task_runs (
    id BIGSERIAL PRIMARY KEY,
    
    -- Identity & Determinism
    spec_hash TEXT NOT NULL,         -- SHA256 of canonical RunSpec
    queue_name TEXT NOT NULL DEFAULT 'default',
    
    -- State
    status task_status NOT NULL DEFAULT 'PENDING',
    
    -- Scheduling & Concurrency
    priority INTEGER NOT NULL DEFAULT 0,    -- Higher number = Higher priority
    run_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until TIMESTAMPTZ,
    worker_id TEXT,                         -- ID of the worker currently processing
    
    -- Data
    payload_json JSONB NOT NULL,            -- The arguments for the task
    result_json JSONB,                      -- Successful return value
    error_json JSONB,                       -- Structured error info
    
    -- Lifecycle
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 1,
    
    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

-- Indices for Polling Performance
-- This is the critical index for SELECT ... FOR UPDATE SKIP LOCKED
CREATE INDEX idx_task_runs_poll 
ON task_runs (queue_name, status, run_after, priority DESC);

-- Index for Deduplication / Replay checks
CREATE INDEX idx_task_runs_spec_hash 
ON task_runs (spec_hash);
```

## 4. Worker Lifecycle

1.  **Initialization**:
    *   Worker starts up, generates or loads a unique `worker_id` (e.g., hostname + uuid).
    *   Connects to Postgres.

2.  **Poll Loop**:
    *   Worker queries DB:
        ```sql
        SELECT * FROM task_runs
        WHERE queue_name = $1
          AND status = 'PENDING'
          AND run_after <= NOW()
        ORDER BY priority DESC, created_at ASC
        LIMIT 1
        FOR UPDATE SKIP LOCKED;
        ```
    *   If no row found: Sleep for `poll_interval` (e.g., 100ms - 1s).

3.  **Claim (Atomic)**:
    *   If row found:
        *   Update `status` = 'RUNNING'.
        *   Update `worker_id` = current `worker_id`.
        *   Update `started_at` = NOW().
        *   Update `leased_until` = NOW() + `lease_timeout`.
        *   Increment `attempt_count`.
        *   Commit Transaction.

4.  **Execution**:
    *   Start a background goroutine to update `leased_until` every X seconds (Heartbeat).
    *   Prepare command: `python -m myproject.task_executor ...`
    *   Pass `payload_json` as argument.
    *   Wait for process exit.

5.  **Completion**:
    *   Stop Heartbeat.
    *   **Success** (Exit Code 0):
        *   Update `status` = 'SUCCESSFUL'.
        *   Update `result_json` = executor output.
        *   Update `completed_at` = NOW().
    *   **Failure** (Non-zero Exit Code or Timeout):
        *   If `attempt_count` < `max_attempts`:
            *   Update `status` = 'RETRYING' (or 'PENDING' with new `run_after` for backoff).
            *   Calculate backoff (exponential/linear) and update `run_after`.
            *   Update `error_json`.
        *   If `attempt_count` >= `max_attempts`:
            *   Update `status` = 'FAILED'.
            *   Update `error_json`.
            *   Update `completed_at` = NOW().

6.  **Return to Step 2**.
