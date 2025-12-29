-- Add runtime configuration and TTL to task_runs
ALTER TABLE task_runs 
ADD COLUMN IF NOT EXISTS max_attempts INTEGER NOT NULL DEFAULT 3,
ADD COLUMN IF NOT EXISTS timeout_seconds INTEGER NOT NULL DEFAULT 900,
ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

-- Create worker heartbeat table
CREATE TABLE IF NOT EXISTS reproq_workers (
    worker_id TEXT PRIMARY KEY,
    hostname TEXT,
    concurrency INTEGER,
    queues TEXT[],
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    version TEXT
);
