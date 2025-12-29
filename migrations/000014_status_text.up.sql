-- Use text for task status to avoid enum mismatches between deployments.
ALTER TABLE IF EXISTS task_runs
    ALTER COLUMN status TYPE TEXT USING status::text;
