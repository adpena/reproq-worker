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
For production builds, prefer:
```bash
go build -tags prod -o reproq ./cmd/reproq
```
This disables `--payload-mode inline` to avoid leaking payload data via process args.

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

### `cancel`
Request cancellation of a running task.

```bash
./reproq cancel --dsn "..." --id 12345
```

### `triage`
List, inspect, and retry failed tasks.

```bash
# List failed tasks
./reproq triage list --dsn "..." --limit 50

# Inspect a failed task
./reproq triage inspect --dsn "..." --id 12345

# Retry a failed task
./reproq triage retry --dsn "..." --id 12345

# Retry all failed tasks
./reproq triage retry --dsn "..." --all
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
Defaults: global rate limiting is disabled until you set a positive rate.

---

## ⚙️ Configuration

The worker is configured via CLI flags. Environment variables are also supported for some options.

### General Flags
| Flag | Env Var | Default | Description |
| :--- | :--- | :--- | :--- |
| `--dsn` | `DATABASE_URL` | - | **Required**. PostgreSQL connection string. |
| `--worker-id` | `WORKER_ID` | `hostname-pid` | Unique identifier for this worker node. Used for heartbeats. |
| `--queues` | `QUEUE_NAMES` | `default` | Comma-separated list of queue names to poll. |
| `--allowed-task-modules` | `ALLOWED_TASK_MODULES` | `myapp.tasks.,tasks.` | Comma-separated allow-list of task module prefixes. |
| `--metrics-port` | - | `0` (Disabled) | Port to serve Prometheus metrics and health (e.g., `9090`). |
| `--metrics-addr` | `METRICS_ADDR` | - | Full address for health/metrics (overrides `--metrics-port`). |
| `--metrics-auth-token` | `METRICS_AUTH_TOKEN` | - | Require `Authorization: Bearer <token>` for health/metrics. |
| `--metrics-allow-cidrs` | `METRICS_ALLOW_CIDRS` | - | Comma-separated IP/CIDR allow-list for health/metrics. |
| `--metrics-auth-limit` | `METRICS_AUTH_LIMIT` | `30` | Max unauthorized requests per window. |
| `--metrics-auth-window` | `METRICS_AUTH_WINDOW` | `1m` | Rate limit window for unauthorized requests. |
| `--metrics-auth-max-entries` | `METRICS_AUTH_MAX_ENTRIES` | `1000` | Max tracked remote hosts for auth rate limiting. |

When `--metrics-port` or `--metrics-addr` is set, the worker serves `GET /metrics` and `GET /healthz`.
Use `--metrics-addr 127.0.0.1:9090` to bind locally, or set `METRICS_AUTH_TOKEN` to require auth.
Set `METRICS_ALLOW_CIDRS` to restrict access by IP or CIDR (for example `127.0.0.1/32,10.0.0.0/8`).
Unauthorized requests are rate-limited (defaults: 30/min per remote host).
If `ALLOWED_TASK_MODULES` is unset, the worker defaults to allowing `myapp.tasks.` and `tasks.`.
Structured logs redact payload and secret-like fields by key name.
`--payload-mode inline` exposes payload data in process args; prefer `stdin` or `file` for sensitive payloads. Production builds (`-tags prod`) reject `inline`.

### Worker Tuning
| Flag | Default | Description |
| :--- | :--- | :--- |
| `--concurrency` | `10` | Maximum number of concurrent tasks to execute. |
| `--lease-seconds` | `300` | Duration of the lease acquired on a task. Worker must heartbeat before this expires. |
| `--heartbeat-seconds` | `60` | Frequency at which the worker updates the lease for running tasks. |
| `--reclaim-interval-seconds` | `60` | Frequency at which the worker checks for and resets expired leases (zombie tasks). Set to `0` to disable. |
| `--payload-mode` | `stdin` | How the payload is passed to the Python process (`stdin`, `file`, `inline`). |
| `--priority-aging-factor` | `60` | Seconds of waiting per priority point (0 disables aging). |

`PRIORITY_AGING_FACTOR` can also set the default for `--priority-aging-factor`.

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

## ⚖️ Scaling: Workers vs Concurrency

You can scale in two ways:

- **Increase concurrency** (`--concurrency`): More goroutines in a single worker process. Best for I/O-bound tasks and lower overhead (one process, shared cache).
- **Run multiple workers**: Multiple processes (or machines). Best for CPU-bound workloads, fault isolation, and horizontal scaling.

Tradeoffs:
- **Concurrency** shares memory and can amplify one bad task (e.g., memory leak).
- **Multiple workers** add overhead (more processes, more DB connections) but isolate failures.

Rule of thumb:
- Start with 1-2 workers per host and tune `--concurrency` to available CPU cores and workload type.
- If you see DB or CPU saturation, add workers instead of only increasing concurrency.

---

## 🚧 Feature Status

- **Dynamic Priority (Aging)**: Implemented. The worker applies priority aging in the claim query based on `PRIORITY_AGING_FACTOR`.
- **Rate Limiting**: Implemented. Token bucket limits are enforced during claiming via the `rate_limits` table.
- **Workflows**: Chains, groups, and chords are supported via the Django library. The worker handles dependency resolution (`parent_id` and workflow callbacks).

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
go run ./cmd/torture --dsn $DATABASE_URL --count 1000
```
