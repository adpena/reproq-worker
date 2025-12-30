# Reproq Worker 🦀

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Documentation](https://img.shields.io/badge/Docs-View%20Online-emerald)](https://adpena.github.io/reproq-worker/)

**The high-performance Go-based execution engine for Reproq Tasks.**

Reproq Worker is a production-grade, deterministic background task runner. It works in tandem with [Reproq Django](https://github.com/adpena/reproq-django) to provide a seamless background task experience for Python applications.

---

## ⚡ Quickstart (with Django)

If you're using Reproq Django, the easiest path is:

```bash
python manage.py reproq install
python manage.py reproq migrate-worker
python manage.py reproq worker
```

---

## ✅ Why Reproq (vs Celery, RQ, Huey)

- **No extra broker**: Postgres only; no Redis/RabbitMQ to operate.
- **Deterministic dedupe**: Identical tasks are coalesced via spec hashing.
- **Operationally lean**: One worker binary, predictable schema.

If you need complex routing, multi-broker support, or a large existing Celery ecosystem, Celery may still be the better fit. Reproq optimizes for low overhead and determinism.

---

## 🚀 Installation

The recommended way to install the worker is via the Django management command, which handles versioning and platform detection:
```bash
python manage.py reproq install
```

Alternatively, build from source:
```bash
go build -o reproq ./cmd/reproq
```

---

## 🛠 Usage

The `reproq` binary supports several subcommands.

### `worker`
Starts the task processing daemon. It polls the database for `READY` tasks and executes them.

```bash
./reproq worker --dsn "postgres://..." --concurrency 20
```

### `beat`
Starts the periodic task scheduler. **Run only one instance per database.**

```bash
./reproq beat --dsn "postgres://..." --interval 30s
```

### `replay`
Manually re-enqueues a specific task result ID.

```bash
./reproq replay --dsn "..." --id 12345
```

### `limit`
Manage rate limits in the database.

```bash
# Set a queue limit
./reproq limit set --key queue:default --rate 5 --burst 10

# List limits
./reproq limit ls

# Remove a limit
./reproq limit rm --key queue:default
```

---

## ⚙️ Configuration

The worker is configured via CLI flags. Environment variables are also supported for some options.

### General Flags
| Flag | Env Var | Default | Description |
| :--- | :--- | :--- | :--- |
| `--dsn` | `DATABASE_URL` | - | **Required**. PostgreSQL connection string. |
| `--worker-id` | `WORKER_ID` | `hostname-pid` | Unique identifier for this worker node. Used for heartbeats. |
| `--metrics-port` | - | `0` (Disabled) | Port to serve Prometheus metrics (e.g., `9090`). |

### Worker Tuning
| Flag | Default | Description |
| :--- | :--- | :--- |
| `--concurrency` | `10` | Maximum number of concurrent tasks to execute. |
| `--lease-seconds` | `300` | Duration of the lease acquired on a task. Worker must heartbeat before this expires. |
| `--heartbeat-seconds` | `60` | Frequency at which the worker updates the lease for running tasks. |
| `--reclaim-interval-seconds` | `60` | Frequency at which the worker checks for and resets expired leases (zombie tasks). Set to `0` to disable. |
| `--payload-mode` | `stdin` | How the payload is passed to the Python process (`stdin`, `file`, `inline`). |
| `--priority-aging-factor` | `60` | Seconds of waiting per priority point (0 disables aging). |

### Beat Tuning
| Flag | Default | Description |
| :--- | :--- | :--- |
| `--interval` | `30s` | How often the scheduler checks for due periodic tasks. |

---

## 🧠 Core Concepts

### Claiming Strategy
The worker uses a `FOR UPDATE SKIP LOCKED` query to atomically claim tasks.
1. **Priority**: High priority tasks are claimed first.
2. **FIFO**: Among equal priority, older tasks (`enqueued_at`) are claimed first.
3. **Concurrency Control**: It respects `lock_key`. If a task with `lock_key="A"` is `RUNNING`, no other task with `lock_key="A"` will be claimed.

### Heartbeats & Recovery
- **Heartbeat**: While a task runs, the worker updates its `leased_until` timestamp every `heartbeat-seconds`.
- **Reclaim**: If a worker crashes, its tasks will eventually expire (`leased_until < NOW`). Another worker (the "reclaimer") will detect this and reset the task status to `READY` (if attempts remain) or `FAILED`.

---

## 🚧 Feature Status

- **Dynamic Priority (Aging)**: Implemented. The worker applies priority aging in the claim query based on `PRIORITY_AGING_FACTOR`.
- **Rate Limiting**: Implemented. Token bucket limits are enforced during claiming via the `rate_limits` table.
- **Workflows**: Chains are supported via the Django library. The worker handles dependency resolution (`parent_id`), but group/chord callbacks are not implemented yet.

---

## 🧪 Development & Testing

### Run Unit Tests
```bash
make test
```

### Run Integration Tests
Requires a running PostgreSQL instance. These tests verify the full lifecycle: claim -> execute -> complete.
```bash
make test-integration
```

### Torture Test
A specialized command to stress-test the worker under high concurrency.
```bash
./reproq torture --dsn $DATABASE_URL --concurrency 50
```
