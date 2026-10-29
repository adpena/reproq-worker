# Reproq Worker 🦀

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://www.apache.org/licenses/LICENSE-2.0)
[![Documentation](https://img.shields.io/badge/Docs-View%20Online-emerald)](https://adpena.github.io/reproq-worker/)

**The high-performance Go-based execution engine for Reproq Tasks.**

Reproq Worker is a production-grade, deterministic background task runner. It works in tandem with [Reproq Django](https://github.com/adpena/reproq-django) to provide a seamless background task experience for Python applications.

---

## ⚡ Quickstart (with Django)

If you're using Reproq Django, the easiest path is:

```bash
uv run python manage.py reproq install
uv run python manage.py migrate reproq_django --database default
uv run python manage.py reproq worker
```

---

## ✅ Compatibility

| Component | Supported |
| :--- | :--- |
| Django | 6.x via `reproq-django` |
| Python (executor env) | 3.12–3.15 with a compatible ReproQ Django environment |
| Schema / executor protocol | `reproq-schema-v2` / protocol `2` |
| Reproq Django | The SDK release declaring that exact schema and executor protocol |

Keep the worker and reproq-django releases aligned; use `python manage.py reproq doctor` to verify the binary, schema, and DSN. `reproq capabilities` reports the worker's JSON compatibility contract without opening a database. Before registering or claiming tasks, a Python worker probes the configured executor's `--capabilities` response and refuses an incompatible environment.

Set `REPROQ_DATABASE_URL` for the Go queue connection. The Python application's
business `DATABASE_URL` remains unchanged when tasks run, so a separate queue
database cannot silently replace the application's default database. This
protocol 2 candidate retires generic `DATABASE_URL` as a Go configuration input;
explicit `--dsn` and file `dsn` settings remain supported. Precedence is file,
then `REPROQ_DATABASE_URL`, then an explicit flag.

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

For a local development binary, use the source toolchain selected in `go.mod`:
```bash
make build
```
This quick build is not a release candidate. Production artifacts use the
canonical builder below, including the `prod` tag that disables inline payload mode.

For reviewable cross-platform candidates from a clean, committed checkout:

```bash
make release-build OUT=dist/candidate
# After a separately reviewed tag already exists and identifies HEAD:
make release-build TAG=v0.0.145 OUT=dist/tagged-candidate
```

The canonical builder produces five production binaries with `CGO_ENABLED=0`,
`-trimpath`, baseline CPU targets, a build manifest, per-asset SHA256 sidecars,
and `SHA256SUMS`. It refuses an existing output directory, dirty source, hidden
Git index flags, or tracked source bytes that differ from committed regular
files. It claims the output directory exclusively and verifies every copied
asset before success; an I/O failure may leave incomplete owned output for
inspection. A
build without `TAG` is explicitly marked `UNRELEASED`; it creates no tag or
release. A tagged candidate must match the single worker `Version` constant
and the tag's commit must equal `HEAD`.

GitHub CI and releases run only through manual dispatch. The release workflow
accepts an existing tag, calls this same builder, and creates an unpublished
draft. Dispatch from the reviewed ref whose `HEAD` the tag identifies. The
workflow refuses an existing release and never replaces its assets. Review
the artifacts and checksums before a separate publication decision; keeping
the same Go toolchain, source, and build policy is required for byte comparisons.

Containers package these same verified artifacts; they do not compile another
worker. See the [deployment guide](DEPLOYMENT.md#runtime-container) for the local
container and Compose commands. The runtime image includes a shell and public
CA bundle, but does not contain your Django project's Python environment.

---

## 🛠 Usage

The `reproq` binary supports several subcommands.

### `worker`
Starts the task processing daemon. It polls the database for `READY` tasks and executes them.

```bash
./reproq worker --dsn "postgres://..." --concurrency 20
```

### `beat`
Starts the periodic task scheduler for definitions assigned to the `go` provider.

```bash
./reproq beat --dsn "postgres://..." --interval 30s
./reproq beat --dsn "postgres://..." --once
```
Use `--once` for cron-driven or low-memory environments.
Beat locks due definitions with `FOR UPDATE SKIP LOCKED`, then calls the single Django migration-owned SQL enqueue function. The occurrence identity is the definition name plus its nominal UTC instant; a completed, failed or cancelled occurrence is never recreated by that identity. Independent occurrences may overlap even when their canonical task hashes match.

An overdue Go definition produces one occurrence for its stored `next_run_at`, then advances to the first cron instant after database time. Unzoned expressions use UTC; `TZ=` / `CRON_TZ=` expressions preserve their named zone and daylight-saving behavior. Optional `pg_cron` definitions call the same SQL function with a UTC trigger-minute identity and use the native provider's late/missed-trigger behavior. Go beat leaves those definitions alone. The SDK owns registration and explicit provider assignment; historical `unassigned` definitions require an operator decision before scheduling.
Set `REPROQ_MEMORY_LOG_INTERVAL=60s` to capture beat memory stats.

Schedules use the shared `portable-cron-v1` contract: five fields, numeric or
three-letter names, lists, ascending ranges, and positive field-bounded steps
on wildcards or explicit ranges. Sunday is 0. Wrapping ranges, macros, L/#/?,
and single-value N/step extensions are rejected consistently with the SDK.

### `replay`
Manually re-enqueues a task by result ID or spec hash (latest match).

```bash
./reproq replay --dsn "..." --id 12345
./reproq replay --dsn "..." --spec-hash <hash>
```

### `cancel`
Cancel pending work immediately, or request cooperative cancellation of a running task.

```bash
./reproq cancel --dsn "..." --id 12345
```
`READY` / `WAITING` tasks become `CANCELLED` atomically, including explicit
retirement of legacy pending work without relabeling its payload/version.
Running cancellation is cooperative and observed on worker heartbeat. A request
wins a concurrent success acknowledgment; the lease token is cleared when the
run leaves `RUNNING`. Pending cancellation, failure and reclaim share workflow
finalization. Repeated or terminal cancellation leaves history unchanged.

### `reclaim`
Inspect or recover expired claims through the same transition used by automatic worker reclaim:

```bash
./reproq reclaim --dry-run --json --older-than 5m --limit 100
./reproq reclaim --action requeue --older-than 5m --limit 100 --json
```

Set `REPROQ_DATABASE_URL` in the process environment. `--action fail` terminally fails eligible claims; `requeue` respects each task's stored attempt budget. Expired cancelled claims become `CANCELLED`. `--include-null-lease` explicitly includes legacy `RUNNING` rows without a lease timestamp. A zero limit is unbounded; dry runs make no row changes. No application startup or Python workflow reimplementation is needed.

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
| `--dsn` | `REPROQ_DATABASE_URL` | - | PostgreSQL connection string. Required unless set in `REPROQ_DATABASE_URL` or the config file. |
| `--worker-id` | `WORKER_ID` | `hostname-pid` | Unique identifier for this worker node. Used for heartbeats. |
| `--process-role` | `REPROQ_PROCESS_ROLE` | `worker` | Role label added to event metadata (for example: `worker`, `beat`). |
| `--queues` | `QUEUE_NAMES` | `default` | Comma-separated list of queue names to poll. |
| `--allowed-task-modules` | `ALLOWED_TASK_MODULES` | `myapp.tasks.,tasks.` | Comma-separated allow-list of task module prefixes (`*` disables validation; dev only). |
| `--logs-dir` | `REPROQ_LOGS_DIR` | - | Directory to persist stdout/stderr logs (updates `logs_uri`). |
| - | `REPROQ_MEMORY_LOG_INTERVAL` | - | Log worker memory stats at the given interval (e.g., `60s`). |
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

When `--metrics-port` or `--metrics-addr` is set, the worker serves `GET /metrics`, `GET /healthz`, and `GET /events` (SSE).
Filter events with query params like `?queue=default&worker_id=worker-1&task_id=123`.
Use `--metrics-addr 127.0.0.1:9090` to bind locally, or set `METRICS_AUTH_TOKEN` (or `--metrics-auth-token` / `metrics.auth_token`) to require auth.
TUI JWTs signed with `METRICS_AUTH_TOKEN` are accepted as bearer tokens for the metrics endpoints.
Set `METRICS_ALLOW_CIDRS` to restrict access by IP or CIDR (for example `127.0.0.1/32,10.0.0.0/8`).
Set `METRICS_TLS_CERT` and `METRICS_TLS_KEY` (or flags) to enable HTTPS; add `METRICS_TLS_CLIENT_CA` to require mTLS.
Unauthorized requests are rate-limited (defaults: 30/min per remote host).
Set `LOW_MEMORY_MODE=1` to skip the metrics/health/events server and the metrics collector.
Set `REPROQ_MEMORY_LOG_INTERVAL=60s` to emit periodic memory stats (includes RSS on Linux).
Set `REPROQ_PROCESS_ROLE` to label events in the `/events` stream (useful when multiple roles share a host).
If `ALLOWED_TASK_MODULES` is unset, the worker defaults to allowing `myapp.tasks.` and `tasks.`.
Set `ALLOWED_TASK_MODULES=*` to disable task module validation in local development.
When `--logs-dir` is set, the worker writes stdout/stderr to a file per attempt and stores the path in `logs_uri`.
The log file is plaintext with `STDOUT:`/`STDERR:` headers; `python manage.py reproq logs --id <result_id>` can read it.
Structured logs redact payload and secret-like fields by key name.
`--payload-mode inline` exposes payload data in process args; prefer `stdin` or `file` for sensitive payloads. Production builds (`-tags prod`) reject `inline`.
Config files can also set fields that do not have CLI flags, including poll backoff, executor module, payload limits, and timeouts.

Executor payload, stdout, and stderr byte limits must be positive. The worker
passes its payload and stdout limits to the Python executor; stdout must contain
one complete UTF-8 JSON result, including its final newline, within the limit.
Overflow, ambiguous results, and a nonzero process exit cannot produce success.
Task logs drain through stderr with bounded retention; truncation records the
exact raw bytes retained and dropped, with a small diagnostic marker in addition
to the retained text. Positive per-task `timeout_seconds` overrides the worker's
default execution timeout. Both infrastructure and task failures consume the
stored attempt budget. Cancellation terminates the owned subprocess group (a
Job Object on Windows), and pipe draining and worker shutdown are bounded.

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
| `--once` | `false` | Enqueue due tasks once and exit. |

---

## 🔒 Security Quick Checklist

- Build with `-tags prod` to disable `--payload-mode inline`.
- Keep `/metrics`, `/healthz`, and `/events` private (bind to localhost or set auth + allow-list).
- Set `ALLOWED_TASK_MODULES` to the minimal allow-list.
- Prefer `stdin`/`file` payload modes and store secrets in a vault, not env files in source control.
- Link: `docs/security/SECURITY_CHECKLIST.md`.

---

## 🧠 Core Concepts

### Claiming Strategy
The worker uses a `FOR UPDATE SKIP LOCKED` query to atomically claim tasks.
1. **Priority**: High priority tasks are claimed first.
2. **FIFO**: Among equal priority, older tasks (`enqueued_at`) are claimed first.
3. **Concurrency Control**: It respects `lock_key` plus `concurrency_key`/`concurrency_limit` caps.
4. **Queue Pauses**: It skips queues marked paused in `reproq_queue_controls`.

A nonempty concurrency key always participates in shared arbitration. Its
effective capacity is the smallest positive limit supplied by the candidate or
any active member. Zero contributes no cap and still counts as an active member;
with no positive cap, the group is unbounded. A lower limit waits until enough
active members finish.

### Heartbeats & Recovery
- **Fencing**: Each claim receives a new UUID token. Heartbeat and completion require that exact token, worker owner, `RUNNING` status and an unexpired lease measured by the database clock.
- **Heartbeat**: While a task runs, the worker updates its `leased_until` timestamp every `heartbeat-seconds`.
- **Reclaim**: Expired claims return to `READY` if their stored attempt budget permits, or become `FAILED` / `CANCELLED`. Every exit from `RUNNING` clears owner, token and lease. Late work from an earlier claim cannot acknowledge a newer attempt.
- **Canonical tasks**: Version 1 runs execute the SDK's stored canonical bytes after hash, semantic JSON and operational projection checks. Historical version 0 runs keep their original payload and hash. This runtime refuses to claim, retry or replay legacy work before spending an attempt or creating a lease. Drain queued legacy work with the old runtime before cutover, or reconcile it through an explicitly audited operator action. Terminal history remains readable; startup never rewrites it.

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
Canonical checks and release builds use the exact patched Go version in
`go.mod` (currently 1.26.10). Build the linter source version selected in
`Makefile` with that compiler so its bundled standard library also receives the
security fixes:
```bash
make lint-install
```
Add that local bin directory to the command's PATH, then bootstrap without
touching a database:
```bash
PATH="$PWD/.local/bin:$PATH" bash scripts/dev_bootstrap.sh
```

### Local Check Gate
```bash
PATH="$PWD/.local/bin:$PATH" make check
```
This runs all module unit tests, `go vet`, and the configured linters. `make test`
uses `-short` and clears application/test database selectors. Hosted CI is manual
only and delegates to these same targets.

Release builds reject any compiler that differs from the exact `go.mod` patch
and record the Go executable hash in the build manifest. Contributors can run
`go test` directly with another compiler, but that does not qualify its security
or release artifacts. Version 0.0.145 is an unpublished security
candidate; the existing v0.0.144 release assets remain unchanged until a separate
reviewed release decision.

### Run Integration Tests
Prepare a dedicated local PostgreSQL database named `reproq_test_<suffix>` and
apply ReproQ Django's committed migrations from a configured Django project.
Its queue alias must select that exact test database. Go owns no schema DDL;
an existing legacy schema requires the SDK's guarded adoption procedure first.
Set `REPROQ_TEST_DATABASE_URL` to that database's connection string, then run:
```bash
make test-integration
make bench BENCHTIME=100x
```
The test guard accepts only explicit loopback hosts and the `reproq_test_`
database prefix, including all connection fallbacks. It never uses ambient
`REPROQ_DATABASE_URL`. Integration tests clear queue tables in this dedicated database;
the claim benchmark creates a unique queue and deletes only its owned fixtures.
Every reported benchmark iteration must claim one owned RUNNING task.

These targets do not create, migrate, start, stop, or delete database services.
They fail if the explicit test connection is absent or unsafe. Queue integration
tests verify real PostgreSQL state transitions; end-to-end Python execution and
whole-service resource qualification require their own workload evidence.

Generate execution workloads through the SDK enqueue APIs so they use the same
canonical producer as the application. The obsolete Go synthetic producer has
been retired. The owned claim benchmark above qualifies claim behavior only;
execution throughput requires completed workload receipts.

## License

Apache License 2.0. See [LICENSE](LICENSE).
