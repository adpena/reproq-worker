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

## ✅ Compatibility

| Component | Supported |
| :--- | :--- |
| Django | 6.x via `reproq-django` |
| Python (executor env) | 3.12+ |
| Reproq Django | Latest release (install/upgrade via `python manage.py reproq install` or `upgrade`) |

Keep the worker and reproq-django releases aligned; use `python manage.py reproq doctor` to verify the binary, schema, and DSN.

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
Manually re-enqueues a task by result ID or spec hash (latest match).

```bash
./reproq replay --dsn "..." --id 12345
./reproq replay --dsn "..." --spec-hash <hash>
```

### `cancel`
Request cancellation of a running task.

```bash
./reproq cancel --dsn "..." --id 12345
```
Cancellation is enforced on the next worker heartbeat; cancelled runs are recorded as `FAILED` with a `kind=cancelled` error payload.

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

### `prune`
Delete expired tasks (where `expires_at` is in the past and status is not `RUNNING`).

```bash
./reproq prune expired --dsn "..." --dry-run
./reproq prune expired --dsn "..." --limit 1000
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

The worker and beat commands can read configuration from CLI flags, environment variables, and a YAML/TOML config file.
Config precedence is: defaults < config file < environment variables < CLI flags.
Config files are discovered in this order: `--config`, `REPROQ_CONFIG`, then `reproq.yaml`, `reproq.yml`, `reproq.toml`, `.reproq.yaml`, `.reproq.yml`, `.reproq.toml` in the working directory.
See `reproq.example.yaml` and `reproq.example.toml` for full templates.

### General Flags
| Flag | Env Var | Default | Description |
| :--- | :--- | :--- | :--- |
| `--config` | `REPROQ_CONFIG` | - | Path to a YAML/TOML config file. |
| `--dsn` | `DATABASE_URL` | - | PostgreSQL connection string. Required unless set in `DATABASE_URL` or the config file. |
| `--worker-id` | `WORKER_ID` | `hostname-pid` | Unique identifier for this worker node. Used for heartbeats. |
| `--queues` | `QUEUE_NAMES` | `default` | Comma-separated list of queue names to poll. |
| `--allowed-task-modules` | `ALLOWED_TASK_MODULES` | `myapp.tasks.,tasks.` | Comma-separated allow-list of task module prefixes (`*` disables validation; dev only). |
| `--logs-dir` | `REPROQ_LOGS_DIR` | - | Directory to persist stdout/stderr logs (updates `logs_uri`). |
| `--metrics-port` | - | `0` (Disabled) | Port to serve Prometheus metrics and health (e.g., `9090`). |
| `--metrics-addr` | `METRICS_ADDR` | - | Full address for health/metrics (overrides `--metrics-port`). |
| `--metrics-auth-token` | `METRICS_AUTH_TOKEN` | - | Require `Authorization: Bearer <token>` for health/metrics. |
| `--metrics-allow-cidrs` | `METRICS_ALLOW_CIDRS` | - | Comma-separated IP/CIDR allow-list for health/metrics. |
| `--metrics-tls-cert` | `METRICS_TLS_CERT` | - | TLS certificate path for health/metrics. |
| `--metrics-tls-key` | `METRICS_TLS_KEY` | - | TLS private key path for health/metrics. |
| `--metrics-tls-client-ca` | `METRICS_TLS_CLIENT_CA` | - | Optional client CA bundle to require mTLS for health/metrics. |
| `--metrics-auth-limit` | `METRICS_AUTH_LIMIT` | `30` | Max unauthorized requests per window. |
| `--metrics-auth-window` | `METRICS_AUTH_WINDOW` | `1m` | Rate limit window for unauthorized requests. |
| `--metrics-auth-max-entries` | `METRICS_AUTH_MAX_ENTRIES` | `1000` | Max tracked remote hosts for auth rate limiting. |

When `--metrics-port` or `--metrics-addr` is set, the worker serves `GET /metrics` and `GET /healthz`.
Use `--metrics-addr 127.0.0.1:9090` to bind locally, or set `METRICS_AUTH_TOKEN` to require auth.
Set `METRICS_ALLOW_CIDRS` to restrict access by IP or CIDR (for example `127.0.0.1/32,10.0.0.0/8`).
Set `METRICS_TLS_CERT` and `METRICS_TLS_KEY` (or flags) to enable HTTPS; add `METRICS_TLS_CLIENT_CA` to require mTLS.
Unauthorized requests are rate-limited (defaults: 30/min per remote host).
If `ALLOWED_TASK_MODULES` is unset, the worker defaults to allowing `myapp.tasks.` and `tasks.`.
Set `ALLOWED_TASK_MODULES=*` to disable task module validation in local development.
When `--logs-dir` is set, the worker writes stdout/stderr to a file per attempt and stores the path in `logs_uri`.
The log file is plaintext with `STDOUT:`/`STDERR:` headers; `python manage.py reproq logs --id <result_id>` can read it.
Structured logs redact payload and secret-like fields by key name.
`--payload-mode inline` exposes payload data in process args; prefer `stdin` or `file` for sensitive payloads. Production builds (`-tags prod`) reject `inline`.
Config files can also set fields that do not have CLI flags, including poll backoff, executor module, payload limits, and timeouts.

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

## 🔒 Security Quick Checklist

- Build with `-tags prod` to disable `--payload-mode inline`.
- Keep `/metrics` and `/healthz` private (bind to localhost or set auth + allow-list).
- Set `ALLOWED_TASK_MODULES` to the minimal allow-list.
- Prefer `stdin`/`file` payload modes and store secrets in a vault, not env files in source control.
- Link: `docs/security/SECURITY_CHECKLIST.md`.

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

### Quick Local Bootstrap
```bash
bash scripts/dev_bootstrap.sh
```

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
