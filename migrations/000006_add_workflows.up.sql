-- Add WAITING state to enum
ALTER TYPE task_status ADD VALUE 'WAITING';

-- Add self-referential parent_id for chaining
ALTER TABLE task_runs 
ADD COLUMN parent_id BIGINT REFERENCES task_runs(id) ON DELETE SET NULL;

-- Index for finding children quickly on parent completion
CREATE INDEX idx_task_runs_parent_id ON task_runs(parent_id);
