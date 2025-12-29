-- Add lock_key for resource-level concurrency control
ALTER TABLE task_runs 
ADD COLUMN IF NOT EXISTS lock_key TEXT;

-- Index for checking active locks during claiming
CREATE INDEX IF NOT EXISTS idx_task_runs_lock_key ON task_runs (lock_key) WHERE status = 'RUNNING';
