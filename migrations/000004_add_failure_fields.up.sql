ALTER TABLE task_runs
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS failed_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_task_runs_failed_at
ON task_runs (failed_at)
WHERE status = 'FAILED';
