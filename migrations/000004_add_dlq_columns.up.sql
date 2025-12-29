-- Add failure tracking columns for DLQ
ALTER TABLE task_runs 
ADD COLUMN failed_at TIMESTAMPTZ,
ADD COLUMN last_error TEXT;
