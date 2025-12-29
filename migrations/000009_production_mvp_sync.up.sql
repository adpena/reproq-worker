-- Sync schema to Production MVP specification
DO $$ 
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'task_status_mvp') THEN
        CREATE TYPE task_status_mvp AS ENUM ('READY', 'RUNNING', 'FAILED', 'SUCCESSFUL');
    END IF;
END $$;

-- Migrate existing table if it exists, or create new one
CREATE TABLE IF NOT EXISTS task_runs_new (
    result_id BIGSERIAL PRIMARY KEY,
    backend_alias TEXT NOT NULL DEFAULT 'default',
    queue_name TEXT NOT NULL DEFAULT 'default',
    priority INTEGER NOT NULL DEFAULT 0,
    run_after TIMESTAMPTZ,
    
    spec_json JSONB NOT NULL,
    spec_hash CHAR(64) NOT NULL,
    
    status task_status_mvp NOT NULL DEFAULT 'READY',
    
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    last_attempted_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    
    attempts INTEGER NOT NULL DEFAULT 0,
    worker_ids TEXT[] DEFAULT '{}',
    
    return_json JSONB,
    errors_json JSONB DEFAULT '[]',
    
    leased_until TIMESTAMPTZ,
    leased_by TEXT,
    
    logs_uri TEXT,
    artifacts_uri TEXT,
    
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    cancel_requested BOOLEAN NOT NULL DEFAULT FALSE
);

-- Index for claiming
CREATE INDEX IF NOT EXISTS idx_task_runs_mvp_claim 
ON task_runs_new (queue_name, status, priority DESC, enqueued_at ASC)
WHERE status = 'READY';

-- Index for spec hash uniqueness (active tasks)
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_runs_mvp_spec_unique
ON task_runs_new (spec_hash)
WHERE status IN ('READY', 'RUNNING');

-- Drop old and rename new (assuming fresh start or safe migration)
-- For this MVP, we will migrate to this new structure.
DROP TABLE IF EXISTS task_runs CASCADE;
ALTER TABLE task_runs_new RENAME TO task_runs;
