package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNoTasks = errors.New("no tasks available")

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Claim selects a READY task and transitions it to RUNNING.
func (s *Service) Claim(ctx context.Context, workerID string, queueName string, leaseSeconds int) (*TaskRun, error) {
	now := time.Now()
	leasedUntil := now.Add(time.Duration(leaseSeconds) * time.Second)

	query := `
		WITH target AS (
			SELECT result_id 
			FROM task_runs
			WHERE status = 'READY'
			  AND queue_name = $1
			  AND (run_after IS NULL OR run_after <= NOW())
			  AND (
				lock_key IS NULL 
				OR NOT EXISTS (
					SELECT 1 FROM task_runs
					WHERE lock_key = task_runs.lock_key 
					  AND status = 'RUNNING'
				)
			  )
			ORDER BY priority DESC, enqueued_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE task_runs
		SET status = 'RUNNING',
		    started_at = COALESCE(started_at, NOW()),
		    last_attempted_at = NOW(),
		    attempts = attempts + 1,
		    leased_until = $2,
		    leased_by = $3::text,
		    worker_ids = COALESCE(worker_ids, '[]'::jsonb) || jsonb_build_array($3::text),
		    updated_at = NOW()
		FROM target
		WHERE task_runs.result_id = target.result_id
		RETURNING 
			task_runs.result_id, backend_alias, queue_name, priority, run_after,
			spec_json, spec_hash, status, enqueued_at, started_at,
			last_attempted_at, finished_at, attempts, max_attempts, timeout_seconds,
			lock_key, worker_ids, return_json, errors_json, leased_until, leased_by,
			logs_uri, artifacts_uri, expires_at, created_at, updated_at, cancel_requested
	`

	var t TaskRun
	err := s.pool.QueryRow(ctx, query, queueName, leasedUntil, workerID).Scan(
		&t.ResultID, &t.BackendAlias, &t.QueueName, &t.Priority, &t.RunAfter,
		&t.SpecJSON, &t.SpecHash, &t.Status, &t.EnqueuedAt, &t.StartedAt,
		&t.LastAttemptedAt, &t.FinishedAt, &t.Attempts, &t.MaxAttempts, &t.TimeoutSeconds,
		&t.LockKey, &t.WorkerIDs, &t.ReturnJSON, &t.ErrorsJSON, &t.LeasedUntil, &t.LeasedBy,
		&t.LogsURI, &t.ArtifactsURI, &t.ExpiresAt, &t.CreatedAt, &t.UpdatedAt, &t.CancelRequested,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoTasks
		}
		return nil, err
	}

	return &t, nil
}

// Heartbeat renews the lease.
func (s *Service) Heartbeat(ctx context.Context, resultID int64, workerID string, leaseSeconds int) (bool, error) {
	newLease := time.Now().Add(time.Duration(leaseSeconds) * time.Second)
	query := `
		UPDATE task_runs
		SET leased_until = $1, updated_at = NOW()
		WHERE result_id = $2 AND leased_by = $3 AND status = 'RUNNING'
		RETURNING cancel_requested
	`
	var cancelled bool
	err := s.pool.QueryRow(ctx, query, newLease, resultID, workerID).Scan(&cancelled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("lease lost or task not running")
		}
		return false, err
	}
	return cancelled, nil
}

// CompleteSuccess marks the task as SUCCESSFUL and triggers dependents.
func (s *Service) CompleteSuccess(ctx context.Context, resultID int64, workerID string, returnJSON json.RawMessage) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 1. Finalize the task
	query := `
		UPDATE task_runs
		SET status = 'SUCCESSFUL',
		    finished_at = NOW(),
		    return_json = $1,
		    leased_until = NULL,
		    leased_by = NULL,
		    updated_at = NOW()
		WHERE result_id = $2 AND leased_by = $3 AND status = 'RUNNING'
	`
	res, err := tx.Exec(ctx, query, returnJSON, resultID, workerID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("fencing failure or task not running")
	}

	// 2. Trigger dependents
	// Decrement wait_count for all tasks waiting on this one or in the same workflow
	// If wait_count reaches 0, set status to READY
	triggerQuery := `
		UPDATE task_runs
		SET wait_count = wait_count - 1,
		    updated_at = NOW()
		WHERE (parent_id = $1 OR (workflow_id IS NOT NULL AND workflow_id = (SELECT workflow_id FROM task_runs WHERE result_id = $1)))
		  AND status = 'WAITING'
		RETURNING result_id, wait_count
	`
	// Note: The logic above is a bit simplified. Usually, you'd want a more precise link.
	// Let's stick to parent_id for now for reliability.
	triggerQuery = `
		UPDATE task_runs
		SET wait_count = wait_count - 1,
		    status = CASE
				WHEN wait_count - 1 <= 0 THEN 'READY'
				ELSE 'WAITING'
			END,
		    updated_at = NOW()
		WHERE parent_id = $1 AND status = 'WAITING'
	`
	_, err = tx.Exec(ctx, triggerQuery, resultID)
	if err != nil {
		return fmt.Errorf("failed to trigger dependents: %w", err)
	}

	return tx.Commit(ctx)
}

// CompleteFailure marks the task as FAILED or requeues it.
func (s *Service) CompleteFailure(ctx context.Context, resultID int64, workerID string, errorObj json.RawMessage, shouldRetry bool, nextRunAfter time.Time) error {
	status := StatusFailed
	if shouldRetry {
		status = StatusReady
	}

	query := `
		UPDATE task_runs
		SET status = $1,
		    finished_at = CASE WHEN $1 = 'FAILED' THEN NOW() ELSE NULL END,
		    errors_json = errors_json || $2::jsonb,
		    run_after = $3,
		    leased_until = NULL,
		    leased_by = NULL,
		    updated_at = NOW()
		WHERE result_id = $4 AND leased_by = $5 AND status = 'RUNNING'
	`
	// errorObj should be a single object, we wrap it in a list for the append if it's not already
	res, err := s.pool.Exec(ctx, query, status, errorObj, nextRunAfter, resultID, workerID)
	if err == nil && res.RowsAffected() == 0 {
		return fmt.Errorf("fencing failure")
	}
	return err
}

// Reclaim recovers tasks with expired leases.
func (s *Service) Reclaim(ctx context.Context, maxAttemptsDefault int) (int64, error) {
	query := `
		WITH expired AS (
			SELECT result_id FROM task_runs
			WHERE status = 'RUNNING' AND leased_until < NOW()
			FOR UPDATE SKIP LOCKED
		)
		UPDATE task_runs
		SET status = CASE WHEN attempts < COALESCE(max_attempts, $1) THEN 'READY' ELSE 'FAILED' END,
		    errors_json = errors_json || jsonb_build_object(
				'kind', 'lease_expiry',
				'message', 'Worker heartbeat lost or process crashed',
				'at', NOW()
			),
		    leased_until = NULL,
		    leased_by = NULL,
		    updated_at = NOW()
		FROM expired
		WHERE task_runs.result_id = expired.result_id
	`
	tag, err := s.pool.Exec(ctx, query, maxAttemptsDefault)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Replay creates a new READY task from an existing one.
func (s *Service) Replay(ctx context.Context, resultID int64) (int64, error) {
	query := `
		INSERT INTO task_runs (backend_alias, queue_name, priority, spec_json, spec_hash, status, max_attempts, timeout_seconds, lock_key)
		SELECT backend_alias, queue_name, priority, spec_json, spec_hash, 'READY', max_attempts, timeout_seconds, lock_key
		FROM task_runs WHERE result_id = $1
		RETURNING result_id
	`
	var newID int64
	err := s.pool.QueryRow(ctx, query, resultID).Scan(&newID)
	return newID, err
}

func (s *Service) RegisterWorker(ctx context.Context, id, hostname string, concurrency int, queues []string, version string) error {
	queuesJSON, err := json.Marshal(queues)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO reproq_workers (worker_id, hostname, concurrency, queues, version, started_at, last_seen_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, NOW(), NOW())
		ON CONFLICT (worker_id) DO UPDATE
		SET hostname = EXCLUDED.hostname,
		    concurrency = EXCLUDED.concurrency,
		    queues = EXCLUDED.queues,
		    version = EXCLUDED.version,
		    last_seen_at = NOW()
	`
	_, err = s.pool.Exec(ctx, query, id, hostname, concurrency, queuesJSON, version)
	return err
}

func (s *Service) UpdateWorkerHeartbeat(ctx context.Context, id string) error {
	query := `UPDATE reproq_workers SET last_seen_at = NOW() WHERE worker_id = $1`
	_, err := s.pool.Exec(ctx, query, id)
	return err
}
