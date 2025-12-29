-- Ensures only one active (PENDING/RUNNING) task exists per spec_hash.
CREATE UNIQUE INDEX idx_task_runs_unique_active_spec 
ON task_runs (spec_hash) 
WHERE status IN ('PENDING', 'RUNNING', 'RETRYING');
