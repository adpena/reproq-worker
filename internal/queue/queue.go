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
			ORDER BY priority DESC, enqueued_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE task_runs
		SET status = 'RUNNING',
		    started_at = COALESCE(started_at, NOW()),
		    last_attempted_at = NOW(),
		    leased_until = $2,
		    leased_by = $3,
		    worker_ids = array_append(worker_ids, $3),
		    updated_at = NOW()
		FROM target
		WHERE task_runs.result_id = target.result_id
		RETURNING 
			task_runs.result_id, backend_alias, queue_name, priority, run_after,
			spec_json, spec_hash, status, enqueued_at, started_at,
			last_attempted_at, finished_at, attempts, worker_ids,
			return_json, errors_json, leased_until, leased_by,
			logs_uri, artifacts_uri, created_at, updated_at, cancel_requested
	`

	var t TaskRun
	err := s.pool.QueryRow(ctx, query, queueName, leasedUntil, workerID).Scan(
		&t.ResultID, &t.BackendAlias, &t.QueueName, &t.Priority, &t.RunAfter,
		&t.SpecJSON, &t.SpecHash, &t.Status, &t.EnqueuedAt, &t.StartedAt,
		&t.LastAttemptedAt, &t.FinishedAt, &t.Attempts, &t.WorkerIDs,
		&t.ReturnJSON, &t.ErrorsJSON, &t.LeasedUntil, &t.LeasedBy,
		&t.LogsURI, &t.ArtifactsURI, &t.CreatedAt, &t.UpdatedAt, &t.CancelRequested,
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

// CompleteSuccess marks the task as SUCCESSFUL.
func (s *Service) CompleteSuccess(ctx context.Context, resultID int64, workerID string, returnJSON json.RawMessage) error {
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
	res, err := s.pool.Exec(ctx, query, returnJSON, resultID, workerID)
	if err == nil && res.RowsAffected() == 0 {
		return fmt.Errorf("fencing failure")
	}
	return err
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
		    attempts = attempts + 1,
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
		SET status = CASE WHEN attempts + 1 < max_attempts THEN 'READY'::task_status_mvp ELSE 'FAILED'::task_status_mvp END,
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
	tag, err := s.pool.Exec(ctx, query)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Replay creates a new READY task from an existing one.
func (s *Service) Replay(ctx context.Context, resultID int64) (int64, error) {
	query := `
		INSERT INTO task_runs (backend_alias, queue_name, priority, spec_json, spec_hash, status)
		SELECT backend_alias, queue_name, priority, spec_json, spec_hash, 'READY'
		FROM task_runs WHERE result_id = $1
		RETURNING result_id
	`
	var newID int64
	err := s.pool.QueryRow(ctx, query, resultID).Scan(&newID)
	return newID, err
}