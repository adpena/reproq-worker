# Reproq Worker 🦀

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Documentation](https://img.shields.io/badge/Docs-View%20Online-emerald)](https://adpena.github.io/reproq-worker/)

**The high-performance Go-based execution engine for Reproq Tasks.**

Reproq Worker is a production-grade, deterministic background task runner. It polls a PostgreSQL database for tasks enqueued by Django and executes them in isolated Python processes.

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

1. **Apply Migrations**: `cat migrations/*.up.sql | psql $DATABASE_URL`
2. **Run Tests**: `go test ./internal/...`
3. **Integration Test**: `make test-integration` (Requires Docker).

## 📜 License
MIT
