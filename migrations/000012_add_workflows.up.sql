-- Add Workflow support (Chains, Chords, DAGs)
DO $$ 
BEGIN
    ALTER TYPE task_status_mvp ADD VALUE IF NOT EXISTS 'WAITING';
END $$;

ALTER TABLE task_runs 
ADD COLUMN IF NOT EXISTS parent_id BIGINT REFERENCES task_runs(result_id),
ADD COLUMN IF NOT EXISTS workflow_id UUID,
ADD COLUMN IF NOT EXISTS wait_count INTEGER NOT NULL DEFAULT 0;

-- Index for finding dependent tasks quickly
CREATE INDEX IF NOT EXISTS idx_task_runs_parent_id ON task_runs (parent_id) WHERE status = 'WAITING';
CREATE INDEX IF NOT EXISTS idx_task_runs_workflow_id ON task_runs (workflow_id);
