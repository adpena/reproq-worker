# Notification-assisted waiting

Worker 0.0.148 and SDK 0.1.536 support this optional capability. Worker 0.0.147 does not implement it. Polling remains the default, and installing the new pair does not enable notifications in existing deployments. Source support, qualified artifacts and publication are separate release stages.

## Selecting a mode

Use `reproq worker --wait-mode notify`, `REPROQ_WAIT_MODE=notify`, or `worker.wait_mode: notify` in YAML (`wait_mode = "notify"` in TOML). Precedence remains defaults, file, environment, explicit flags. Values are case-insensitive and trimmed. Invalid values explain the accepted modes. `reproq capabilities` advertises operation `notification_wait: 1` without connecting to a database. The paired Django command checks that operation before starting notify mode.

Notify mode requires a PostgreSQL session endpoint. It opens one dedicated session outside the query pool. PgBouncer transaction pooling cannot preserve LISTEN; select `poll` for that endpoint. No endpoint is guessed from its hostname or port. A separately configured listener endpoint is not supported by this first version.

In the paired source SDK, enable producer hints with a real boolean:

```python
TASKS = {
    "default": {
        "BACKEND": "reproq_django.backend.ReproqBackend",
        "OPTIONS": {"WAKE_NOTIFICATIONS": True},
    },
}
```

Existing producers and workers remain usable with polling. Hint-enabled producers need PostgreSQL; invalid options or unsupported routes fail before insertion. Hints are sent after commit, once per single/deduplicated enqueue operation or per affected database in a bulk/workflow operation. Mixed-backend workflows send one hint if any included backend opts in. Existing per-result public signals retain their semantics. A failed hint logs a warning and leaves committed work accepted. No thread, timer or another task payload is retained by a producer callback.

## Authority and failure behavior

The fixed channel `reproq_wake_v1` has an empty payload. It contains no task IDs, arguments, results, queue names or credentials. Database users may observe or emit it. A wake only prompts the worker's existing scoped SQL probe/claim; it never authorizes execution. The supported queue authority remains permanent public tables under schema v2. Django aliases do not create separate channel namespaces.

The listener commits LISTEN before the first durable inspection, continuously drains hints into a size-one wake channel and forces inspection after reconnect. Ordinary transport loss reconnects with bounded jitter while recovery polling continues. Initial connection/subscription rejection and subsequent schema, authorization or session-configuration rejection fail visibly with sanitized messages. Shutdown cancels and joins the one reader and closes its session. Hints cannot bypass a database-error backoff.

SQL claims, fencing, fairness, local concurrency limits, task and Worker heartbeats, reclaim and cancellation remain unchanged. Running-task cancellation is still discovered through its heartbeat; idle arrival hints do not make cancellation faster.

## Coverage and limits

Only SDK single, deduplicated, bulk and workflow enqueue emit hints in this first slice. Go completion/dependency release, queue controls, pg_cron, direct SQL and older SDK producers still depend on bounded recovery. No published migration or hashed periodic SQL was changed. A task becoming due, a retry deadline or rate tokens refilling need no new write: polling/timers remain necessary.

The configured polling bounds remain unchanged (defaults 100 ms–5 s). Notification wakes honor the configured minimum delay after an empty/blocked probe, reusing the same timer, so sustained hints cannot cause an unpaced SQL probe loop. An explicitly configured zero minimum removes that floor. Notifications accelerate arrivals, but this slice does not claim a reduction in idle polling frequency. Do not lengthen recovery bounds for uncovered writers. Broadcast can wake many workers for one task; local coalescing bounds buffered hints, not PostgreSQL broadcast traffic. Large deployments should measure claim attempts per started task before adding more workers or changing intervals.

## Observability and measurement

- `reproq_listener_connections`: connected dedicated sessions, outside pool counts.
- `reproq_notification_events_total{event=...}`: fixed labels for subscription, receipt, coalescing, disconnect, reconnect failure, reconnect and rejection.
- `reproq_idle_waits_total{reason=...}`: timer versus notification wakeups.

Low-memory mode does not enable a metrics server or lifecycle broker merely to receive hints. Existing queue/execution metrics remain available when metrics serving is enabled.

Compare idle and burst workloads in both modes: total SQL queries by purpose, query-pool plus listener connections, PostgreSQL backend memory, Worker RSS/PSS/heap/goroutines, enqueue-to-start p50/p95/p99 and cancellation behavior. A listener adds a database backend; lower total memory is not guaranteed. Functional tests alone establish no performance improvement.

## Source references

[PostgreSQL LISTEN](https://www.postgresql.org/docs/current/sql-listen.html) specifies subscribe/commit-before-inspect ordering. [NOTIFY](https://www.postgresql.org/docs/current/sql-notify.html) specifies transaction delivery and visibility. [PgBouncer](https://www.pgbouncer.org/features.html) documents pooling compatibility. [pgx](https://pkg.go.dev/github.com/jackc/pgx/v5@v5.12.0#Conn.WaitForNotification) provides cancellable session waiting. These are mechanism references, not a benchmark of this implementation.
