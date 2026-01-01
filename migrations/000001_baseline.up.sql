-- Baseline schema (squashed). Status stored as text, no enums.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE task_runs (
    result_id BIGSERIAL PRIMARY KEY,
    backend_alias TEXT NOT NULL DEFAULT 'default',
    queue_name TEXT NOT NULL DEFAULT 'default',
    priority INTEGER NOT NULL DEFAULT 0,
    run_after TIMESTAMPTZ,
    spec_json JSONB NOT NULL,
    spec_hash CHAR(64) NOT NULL,
    task_path TEXT,
    status TEXT NOT NULL DEFAULT 'READY',
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    last_attempted_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    timeout_seconds INTEGER NOT NULL DEFAULT 900,
    lock_key TEXT,
    concurrency_key TEXT,
    concurrency_limit INTEGER NOT NULL DEFAULT 0,
    worker_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    return_json JSONB,
    errors_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_error TEXT,
    failed_at TIMESTAMPTZ,
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

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_claim
ON task_runs (queue_name, status, priority DESC, enqueued_at ASC)
WHERE status = 'READY';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_spec_unique
ON task_runs (spec_hash)
WHERE status IN ('READY', 'RUNNING');

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_lock_key
ON task_runs (lock_key)
WHERE status = 'RUNNING';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_concurrency_active
ON task_runs (concurrency_key, leased_until)
WHERE status = 'RUNNING' AND concurrency_key IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_parent_id
ON task_runs (parent_id)
WHERE status = 'WAITING';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_workflow_id
ON task_runs (workflow_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_failed_at
ON task_runs (failed_at)
WHERE status = 'FAILED';

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_task_path
ON task_runs (task_path);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_task_runs_claim_due
ON task_runs (queue_name, COALESCE(run_after, '-infinity'::timestamptz), priority DESC, enqueued_at ASC)
WHERE status = 'READY';

CREATE TABLE periodic_tasks (
    name TEXT PRIMARY KEY,
    cron_expr TEXT NOT NULL,
    task_path TEXT NOT NULL,
    payload_json JSONB NOT NULL DEFAULT '{}',
    queue_name TEXT NOT NULL DEFAULT 'default',
    priority INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    concurrency_key TEXT,
    concurrency_limit INTEGER NOT NULL DEFAULT 0,
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_periodic_tasks_next_run
ON periodic_tasks (next_run_at)
WHERE enabled = TRUE;

CREATE TABLE reproq_queue_controls (
    queue_name TEXT PRIMARY KEY,
    paused BOOLEAN NOT NULL DEFAULT FALSE,
    paused_at TIMESTAMPTZ,
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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

INSERT INTO rate_limits (key, tokens_per_second, burst_size, current_tokens, last_refilled_at)
VALUES ('global', 0, 1, 0, NOW())
ON CONFLICT (key) DO UPDATE SET
    tokens_per_second = EXCLUDED.tokens_per_second,
    burst_size = EXCLUDED.burst_size,
    current_tokens = EXCLUDED.current_tokens,
    last_refilled_at = EXCLUDED.last_refilled_at;

CREATE TABLE workflow_runs (
    workflow_id UUID PRIMARY KEY,
    expected_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL DEFAULT 0,
    failure_count INTEGER NOT NULL DEFAULT 0,
    callback_result_id BIGINT,
    status TEXT NOT NULL DEFAULT 'RUNNING',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_runs_callback
ON workflow_runs (callback_result_id);

CREATE OR REPLACE FUNCTION reproq_task_path_from_spec()
RETURNS trigger AS $$
BEGIN
    IF NEW.task_path IS NULL OR NEW.task_path = '' THEN
        NEW.task_path := NEW.spec_json->>'task_path';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_task_runs_task_path ON task_runs;
CREATE TRIGGER trg_task_runs_task_path
BEFORE INSERT OR UPDATE OF spec_json, task_path ON task_runs
FOR EACH ROW
EXECUTE FUNCTION reproq_task_path_from_spec();

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'task_runs_task_path_not_empty'
    ) THEN
        ALTER TABLE task_runs
        ADD CONSTRAINT task_runs_task_path_not_empty CHECK (task_path IS NOT NULL AND task_path <> '') NOT VALID;
    END IF;
END $$;
