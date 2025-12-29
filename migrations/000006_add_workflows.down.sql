DROP INDEX IF EXISTS idx_task_runs_parent_id;
ALTER TABLE task_runs DROP COLUMN IF EXISTS parent_id;
-- Note: 'WAITING' remains in the type as Postgres does not support easy removal of enum values.
