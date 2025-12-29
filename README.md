# Reproq Worker

A deterministic Go worker and scalable queue backend for Django 6.0 Tasks.
This service acts as the execution engine, replacing tools like Celery or RQ with a Postgres-backed, strictly deterministic runner.

## Features

*   **Postgres-based Queue**: Uses `SELECT ... FOR UPDATE SKIP LOCKED` for safe, high-performance concurrency without external brokers (Redis/RabbitMQ).
*   **Deterministic Execution**: Tasks are identified by an immutable `spec_hash`.
*   **Language Agnostic Execution**: Spawns external processes (e.g., Python) to run tasks, ensuring isolation.
*   **Safety**: Handles timeouts, heartbeats, and retries automatically.
*   **Replayability**: Built-in tools to re-run tasks based on their specification hash.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for detailed design documentation.

### High-Level Flow

1.  **Enqueue**: Your Django app inserts a row into `task_runs` with a JSON payload and a `spec_hash`.
2.  **Poll**: The Go worker polls the DB for `PENDING` tasks.
3.  **Claim**: The worker atomically claims a task, setting status to `RUNNING` and establishing a lease.
4.  **Execute**: The worker spawns `python -m <your_executor> --payload <json>`.
5.  **Result**:
    *   **Success**: The worker captures stdout/JSON result and updates the DB.
    *   **Failure**: The worker captures stderr/error JSON and schedules a retry or marks as `FAILED`.

## Getting Started

### Prerequisites

*   Go 1.22+
*   PostgreSQL 13+
*   Python environment with your Django project (for execution)

### Configuration

Environment variables:

| Variable | Description | Default |
| :--- | :--- | :--- |
| `DATABASE_URL` | Postgres connection string | **Required** |
| `WORKER_ID` | Unique identifier for this worker instance | `worker-<hostname>-<timestamp>` |
| `POLL_INTERVAL` | Duration to sleep when queue is empty | `1s` |
| `QUEUE_NAME` | Queue to poll | `default` |
| `PYTHON_PATH` | Path to python interpreter | `python3` |

### Running the Worker

```bash
# Build
go build -o worker ./cmd/worker

# Run
export DATABASE_URL="postgres://user:pass@localhost:5432/db"
./worker
```

### Re-enqueueing Tasks (CLI)

You can manually re-trigger tasks using the worker binary:

```bash
# Re-run a specific task ID (creates a new PENDING copy)
./worker --requeue-id 123

# Re-run the latest task with a specific spec hash
./worker --requeue-hash "sha256-hash-of-spec"
```

## Integration with Django

This worker expects a `task_runs` table in your Postgres database. 

**Python Executor Interface:**

Your Python task executor must accept a `--payload` argument containing the JSON payload:

```python
# example_executor.py
import argparse
import json
import sys

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--payload', required=True)
    args = parser.parse_args()

    payload = json.loads(args.payload)
    
    # ... Execute your logic ...
    result = {"status": "ok", "data": "processed"}
    
    # Print JSON result to stdout
    print(json.dumps(result))

if __name__ == "__main__":
    main()
```

The Go worker will invoke this command (configured via `PYTHON_PATH` and hardcoded flags currently, adaptable in `config.go`).

## Development

1.  Start Postgres.
2.  Run migrations: `cat migrations/*.up.sql | psql $DATABASE_URL`.
3.  Start the worker.