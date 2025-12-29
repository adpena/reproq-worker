-- Add cancellation flag
ALTER TABLE task_runs 
ADD COLUMN cancel_requested BOOLEAN NOT NULL DEFAULT FALSE;
