-- Periodic tasks definitions
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

CREATE INDEX idx_periodic_tasks_next_run ON periodic_tasks (next_run_at) WHERE enabled = TRUE;
