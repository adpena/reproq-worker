# Reproq Worker 🦀

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Documentation](https://img.shields.io/badge/Docs-View%20Online-emerald)](https://adpena.github.io/reproq-worker/)

**The high-performance Go-based execution engine for Reproq Tasks.**

Reproq Worker is a production-grade, deterministic background task runner. It works in tandem with [Reproq Django](https://github.com/adpena/reproq-django) to provide a seamless background task experience for Python applications.

---

## 🤝 Relationship with Reproq Django

Reproq is split into two specialized components:
1. **[Reproq Django](https://github.com/adpena/reproq-django)**: The "Brain." Handles the Python-side API, task definitions, and the monitoring UI.
2. **Reproq Worker (this repo)**: The "Muscle." A standalone Go binary that polls PostgreSQL and executes tasks in isolated processes.

---

## Features

- **High Concurrency**: Efficient Go implementation with goroutines and `SKIP LOCKED` support.
- **Beat Scheduler**: Built-in cron scheduler for periodic tasks.
- **Heartbeat & Leases**: Robust task claiming that prevents double execution even if a worker crashes.
- **Deterministic**: SHA256-based task deduplication.
- **Self-Registering**: Workers register themselves in the database for real-time monitoring.

---

## 🚀 Installation

The recommended way to install the worker is via the Django management command:
```bash
python manage.py reproq install
```
This will download the pre-built binary for your OS/Architecture.

Alternatively, build from source:
```bash
go build -o reproq ./cmd/reproq
```

---

## 🗃 Database Schema Notes

Reproq Worker expects these JSONB-backed columns:
- `task_runs.worker_ids`: JSON array of worker IDs.
- `reproq_workers.queues`: JSON array of queue names.

If you previously ran the legacy SQL migrations that created array columns, apply
`migrations/000013_convert_worker_arrays_to_jsonb.up.sql` to convert them.

---

## 🛠 Usage

### Start a Worker
Processes tasks from the queue.
```bash
./reproq worker --dsn "postgres://user:pass@localhost:5432/db" --concurrency 20
```

### Start the Beat Scheduler
Schedules periodic tasks. Only one instance of `beat` should be running per database.
```bash
./reproq beat --dsn "postgres://user:pass@localhost:5432/db" --interval 30s
```

### Replay a Task
Re-enqueues a task for execution.
```bash
./reproq replay --dsn "..." --id 12345
```

---

## ⚙️ Configuration

| Flag | Env Var | Default | Description |
| :--- | :--- | :--- | :--- |
| `--dsn` | `DATABASE_URL` | - | PostgreSQL connection string. |
| `--concurrency` | - | `10` | Max simultaneous tasks. |
| `--interval` | - | `30s` | Polling interval for `beat`. |
| `--worker-id` | `WORKER_ID` | `hostname-pid` | Unique ID for this worker node. |

---

## 🧪 Development & Testing

Reproq Worker includes a comprehensive test suite covering unit tests, integration tests, and benchmarks.

### Run Unit Tests
Covers the executor, security validator, and mock mode.
```bash
make test
```
*Alternatively: `go test ./internal/executor/... ./internal/runner/...`*

### Run Integration Tests
Requires a running PostgreSQL instance (or uses Docker if configured). These tests verify the full lifecycle of task claiming, heartbeats, and terminal state updates.
```bash
make test-integration
```

### Performance Benchmarks
Test the throughput of the Postgres queue claiming logic.
```bash
go test -bench . ./internal/queue
```

### Torture Test
A specialized command to stress-test the worker under high concurrency and database contention.
```bash
./reproq torture --dsn $DATABASE_URL --concurrency 50 --duration 5m
```

## 🤝 Contributing & Feedback

We welcome contributions of all kinds!

- **Core Logic**: Improvements to the Go polling, heartbeat, or scheduler.
- **Performance**: Optimizations for PostgreSQL queries or process execution.
- **Portability**: Support for more OS/Architecture targets.

Please open an issue to discuss major changes before submitting a PR. We value your feedback on the worker's performance and stability.

## 📜 License
MIT
