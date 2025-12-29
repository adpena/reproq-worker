-- Enums for state safety
CREATE TYPE task_status AS ENUM (
    'PENDING',
    'RUNNING',
    'SUCCESSFUL',
    'FAILED',
    'RETRYING',
    'CANCELLED'
);

CREATE TABLE task_runs (
    id BIGSERIAL PRIMARY KEY,
    
    -- Identity & Determinism
    spec_hash TEXT NOT NULL,         -- SHA256 of canonical RunSpec
    queue_name TEXT NOT NULL DEFAULT 'default',
    
    -- State
    status task_status NOT NULL DEFAULT 'PENDING',
    
    -- Scheduling & Concurrency
    priority INTEGER NOT NULL DEFAULT 0,    -- Higher number = Higher priority
    run_after TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_until TIMESTAMPTZ,
    worker_id TEXT,                         -- ID of the worker currently processing
    
    -- Data
    payload_json JSONB NOT NULL,            -- The arguments for the task
    result_json JSONB,                      -- Successful return value
    error_json JSONB,                       -- Structured error info
    
    -- Lifecycle
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 1,
    
    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

-- Indices for Polling Performance
-- This is the critical index for SELECT ... FOR UPDATE SKIP LOCKED
CREATE INDEX idx_task_runs_poll 
ON task_runs (queue_name, status, run_after, priority DESC);

-- Index for Deduplication / Replay checks
CREATE INDEX idx_task_runs_spec_hash 
ON task_runs (spec_hash);
