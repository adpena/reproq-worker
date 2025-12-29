-- One-time conversion for legacy array columns to JSONB
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'task_runs'
          AND column_name = 'worker_ids'
          AND data_type = 'ARRAY'
    ) THEN
        ALTER TABLE task_runs
            ALTER COLUMN worker_ids DROP DEFAULT,
            ALTER COLUMN worker_ids TYPE jsonb
                USING COALESCE(to_jsonb(worker_ids), '[]'::jsonb),
            ALTER COLUMN worker_ids SET DEFAULT '[]'::jsonb;
    END IF;
END $$;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_name = 'reproq_workers'
          AND column_name = 'queues'
          AND data_type = 'ARRAY'
    ) THEN
        ALTER TABLE reproq_workers
            ALTER COLUMN queues DROP DEFAULT,
            ALTER COLUMN queues TYPE jsonb
                USING COALESCE(to_jsonb(queues), '[]'::jsonb),
            ALTER COLUMN queues SET DEFAULT '[]'::jsonb;
    END IF;
END $$;
