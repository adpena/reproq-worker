# reproq-worker

Postgres-backed Queue and Deterministic Runner for Django 6.0 Tasks.

## Core Features
- **Postgres-Only**: No Redis or Kafka required.
- **Strict Concurrency**: Uses `SKIP LOCKED` for linear horizontal scalability.
- **Leases & Heartbeats**: Safe task claiming with fencing to prevent double execution.
- **External Python Executor**: Invokes Django tasks via a strict JSON protocol.
- **Replay**: Easy re-enqueuing of tasks by `result_id` or `spec_hash`.

## Commands

### Start Worker
```bash
export DATABASE_URL="postgres://user:pass@localhost:5432/reproq"
./reproq worker --concurrency 20
```

### Replay a Task
```bash
./reproq replay --id 12345
```

## Integration Protocol (Python)

The Go worker invokes:
`python3 -m reproq_django.executor --result-id <id> --attempt <n>`

### Input
The Go worker writes the `spec_json` to the process's **stdin**.

### Output (stdout)
The executor must write a single JSON object to **stdout**:

**Success:**
```json
{"ok": true, "return": {"any": "data"}}
```

**Failure:**
```json
{"ok": false, "exception_class": "ValueError", "message": "...", "traceback": "..."}
```

## Configuration

| Flag | Env Var | Default |
| :--- | :--- | :--- |
| `--dsn` | `DATABASE_URL` | **Required** |
| `--concurrency` | - | `10` |
| `--worker-id` | `WORKER_ID` | `hostname-pid` |
| `--lease-seconds` | - | `300` |

## Development

1. Run migrations: `cat migrations/*.up.sql | psql $DATABASE_URL`
2. Build: `go build -o reproq ./cmd/reproq`
3. Run tests: `make test`