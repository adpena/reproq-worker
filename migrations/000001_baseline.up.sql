-- Baseline schema (status stored as text, no enums).
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE task_runs (
    result_id BIGSERIAL PRIMARY KEY,
    backend_alias TEXT NOT NULL DEFAULT 'default',
    queue_name TEXT NOT NULL DEFAULT 'default',
    priority INTEGER NOT NULL DEFAULT 0,
    run_after TIMESTAMPTZ,
    spec_json JSONB NOT NULL,
    spec_hash CHAR(64) NOT NULL,
    status TEXT NOT NULL DEFAULT 'READY',
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    last_attempted_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    timeout_seconds INTEGER NOT NULL DEFAULT 900,
    lock_key TEXT,
    worker_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    return_json JSONB,
    errors_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    leased_until TIMESTAMPTZ,
    leased_by TEXT,
    logs_uri TEXT,
    artifacts_uri TEXT,
    expires_at TIMESTAMPTZ,
    parent_id BIGINT REFERENCES task_runs(result_id) ON DELETE SET NULL,
    workflow_id UUID,
    wait_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX idx_task_runs_claim
ON task_runs (queue_name, status, priority DESC, enqueued_at ASC)
WHERE status = 'READY';

CREATE UNIQUE INDEX idx_task_runs_spec_unique
ON task_runs (spec_hash)
WHERE status IN ('READY', 'RUNNING');

CREATE INDEX idx_task_runs_lock_key
ON task_runs (lock_key)
WHERE status = 'RUNNING';

CREATE INDEX idx_task_runs_parent_id
ON task_runs (parent_id)
WHERE status = 'WAITING';

CREATE INDEX idx_task_runs_workflow_id
ON task_runs (workflow_id);

CREATE TABLE periodic_tasks (
    name TEXT PRIMARY KEY,
    cron_expr TEXT NOT NULL,
    task_path TEXT NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}',
    queue_name TEXT NOT NULL DEFAULT 'default',
    priority INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_periodic_tasks_next_run
ON periodic_tasks (next_run_at)
WHERE enabled = TRUE;

CREATE TABLE reproq_workers (
    worker_id TEXT PRIMARY KEY,
    hostname TEXT,
    concurrency INTEGER,
    queues JSONB NOT NULL DEFAULT '[]'::jsonb,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    version TEXT
);

CREATE TABLE rate_limits (
    key TEXT PRIMARY KEY,
    tokens_per_second REAL NOT NULL,
    burst_size INTEGER NOT NULL,
    current_tokens REAL NOT NULL,
    last_refilled_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO rate_limits (key, tokens_per_second, burst_size, current_tokens)
VALUES ('global', 100, 200, 200);
