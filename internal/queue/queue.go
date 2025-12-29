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

// Claim polls for a pending task and atomically transitions it to RUNNING.
// It uses a single CTE to claim the task and update rate limits in one network round-trip.
func (s *Service) Claim(ctx context.Context, queueName string, workerID string, leaseDuration time.Duration) (*TaskRun, error) {
	now := time.Now()
	leasedUntil := now.Add(leaseDuration)

	// We check for two limit keys: 'global' and 'queue:<name>'
	queueLimitKey := "queue:" + queueName

	query := `
		WITH 
		limits AS (
			-- Atomic Token Bucket Update
			UPDATE rate_limits
			SET current_tokens = LEAST(
					burst_size, 
					current_tokens + (tokens_per_second * EXTRACT(EPOCH FROM (NOW() - last_refilled_at)))
				) - 1,
				last_refilled_at = NOW()
			WHERE key IN ('global', $1)
			  AND (
				current_tokens + (tokens_per_second * EXTRACT(EPOCH FROM (NOW() - last_refilled_at)))
			  ) >= 1
			RETURNING key, current_tokens
		),
		can_proceed AS (
			-- We proceed ONLY if we managed to decrement ALL matching limit keys
			-- If only 1 exists, we need 1. If 2 exist, we need 2.
			SELECT count(*) as count FROM limits
			WHERE key IN ('global', $1)
		),
		target AS (
			SELECT t.id 
			FROM task_runs t, can_proceed cp
			WHERE t.queue_name = $2
			  AND t.status = 'PENDING'
			  AND t.run_after <= NOW()
			  AND cp.count >= (SELECT count(*) FROM rate_limits WHERE key IN ('global', $1))
			ORDER BY t.priority DESC, t.created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE task_runs
		SET status = 'RUNNING',
		    worker_id = $3,
		    started_at = $4,
		    leased_until = $5,
		    attempt_count = attempt_count + 1,
		    updated_at = $4
		FROM target
		WHERE task_runs.id = target.id
		RETURNING 
			task_runs.id, parent_id, spec_hash, queue_name, status, priority, run_after, 
			leased_until, worker_id, payload_json, result_json, error_json, 
			stdout, stderr, exit_code,
			attempt_count, max_attempts, created_at, updated_at, started_at, completed_at,
			failed_at, last_error, cancel_requested
	`

	var task TaskRun
	err := s.pool.QueryRow(ctx, query, queueLimitKey, queueName, workerID, now, leasedUntil).Scan(
		&task.ID, &task.ParentID, &task.SpecHash, &task.QueueName, &task.Status, &task.Priority, &task.RunAfter,
		&task.LeasedUntil, &task.WorkerID, &task.PayloadJSON, &task.ResultJSON, &task.ErrorJSON,
		&task.Stdout, &task.Stderr, &task.ExitCode,
		&task.AttemptCount, &task.MaxAttempts, &task.CreatedAt, &task.UpdatedAt, &task.StartedAt, &task.CompletedAt,
		&task.FailedAt, &task.LastError, &task.CancelRequested,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoTasks
		}
		return nil, fmt.Errorf("failed to claim task: %w", err)
	}

	return &task, nil
}

// ReapExpiredLeases finds tasks that are stuck in RUNNING state past their lease and resets them.
func (s *Service) ReapExpiredLeases(ctx context.Context) (int64, error) {
	query := `
		UPDATE task_runs
		SET status = 'PENDING',
		    worker_id = NULL,
		    leased_until = NULL,
		    updated_at = NOW()
		WHERE status = 'RUNNING'
		  AND leased_until < NOW()
	`
	tag, err := s.pool.Exec(ctx, query)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Heartbeat extends the lease for a running task and returns if cancellation is requested.
func (s *Service) Heartbeat(ctx context.Context, taskID int64, duration time.Duration) (bool, error) {
	query := `
		UPDATE task_runs
		SET leased_until = $1,
		    updated_at = NOW()
		WHERE id = $2 AND status = 'RUNNING'
		RETURNING cancel_requested
	`
	newLease := time.Now().Add(duration)
	var cancelRequested bool
	err := s.pool.QueryRow(ctx, query, newLease, taskID).Scan(&cancelRequested)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("task %d not running or lost", taskID)
		}
		return false, fmt.Errorf("heartbeat failed: %w", err)
	}
	return cancelRequested, nil
}

// RequestCancellation marks a task for cancellation.
func (s *Service) RequestCancellation(ctx context.Context, taskID int64) error {
	query := `UPDATE task_runs SET cancel_requested = TRUE WHERE id = $1`
	res, err := s.pool.Exec(ctx, query, taskID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("task %d not found", taskID)
	}
	return nil
}

// CompleteSuccess marks a task as SUCCESSFUL and stores the result.
// It uses fencing and atomically unlocks any child tasks waiting on this task.
func (s *Service) CompleteSuccess(ctx context.Context, taskID int64, workerID string, resultJSON json.RawMessage, stdout, stderr string, exitCode int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	query := `
		UPDATE task_runs
		SET status = 'SUCCESSFUL',
		    result_json = $1,
		    stdout = $2,
		    stderr = $3,
		    exit_code = $4,
		    completed_at = NOW(),
		    updated_at = NOW()
		WHERE id = $5 
		  AND worker_id = $6 
		  AND status = 'RUNNING'
	`
	res, err := tx.Exec(ctx, query, resultJSON, stdout, stderr, exitCode, taskID, workerID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("fencing failure: task %d is no longer owned by worker %s or is not in RUNNING state", taskID, workerID)
	}

	// Unlock children
	childQuery := `
		UPDATE task_runs
		SET status = 'PENDING',
		    run_after = NOW(),
		    updated_at = NOW()
		WHERE parent_id = $1 AND status = 'WAITING'
	`
	_, err = tx.Exec(ctx, childQuery, taskID)
	if err != nil {
		return fmt.Errorf("failed to unlock children: %w", err)
	}

	return tx.Commit(ctx)
}

// CompleteFailure marks a task as FAILED or schedules a retry.
// It uses fencing to ensure only the current lease holder can commit the failure.
func (s *Service) CompleteFailure(ctx context.Context, taskID int64, workerID string, errorJSON json.RawMessage, stdout, stderr string, exitCode int, shouldRetry bool, nextRunAfter time.Time) error {
	var status TaskStatus
	var runAfter time.Time
	var failedAt *time.Time
	
	if shouldRetry {
		status = StatusPending
		runAfter = nextRunAfter
	} else {
		status = StatusFailed
		runAfter = time.Now()
		now := time.Now()
		failedAt = &now
	}

	query := `
		UPDATE task_runs
		SET status = $1,
		    error_json = $2,
		    stdout = $3,
		    stderr = $4,
		    exit_code = $5,
		    run_after = $6,
		    completed_at = CASE WHEN $1 = 'FAILED' THEN NOW() ELSE NULL END,
		    failed_at = $7,
		    last_error = $8,
		    updated_at = NOW()
		WHERE id = $9 
		  AND worker_id = $10 
		  AND status = 'RUNNING'
	`
	// Extract a human readable error from errorJSON if possible
	lastError := "unknown error"
	var errData map[string]interface{}
	if err := json.Unmarshal(errorJSON, &errData); err == nil {
		if msg, ok := errData["message"].(string); ok {
			lastError = msg
		}
	}

	res, err := s.pool.Exec(ctx, query, status, errorJSON, stdout, stderr, exitCode, runAfter, failedAt, lastError, taskID, workerID)
	if err == nil && res.RowsAffected() == 0 {
		return fmt.Errorf("fencing failure: task %d is no longer owned by worker %s or is not in RUNNING state", taskID, workerID)
	}
	return err
}

// ListFailed returns tasks that are in FAILED state.
func (s *Service) ListFailed(ctx context.Context, limit int) ([]TaskRun, error) {
	query := `
		SELECT id, spec_hash, queue_name, status, last_error, failed_at, attempt_count
		FROM task_runs
		WHERE status = 'FAILED'
		ORDER BY failed_at DESC
		LIMIT $1
	`
	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []TaskRun
	for rows.Next() {
		var t TaskRun
		if err := rows.Scan(&t.ID, &t.SpecHash, &t.QueueName, &t.Status, &t.LastError, &t.FailedAt, &t.AttemptCount); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, nil
}

// RetryFailed resets a failed task to PENDING.
func (s *Service) RetryFailed(ctx context.Context, taskID int64) error {
	query := `
		UPDATE task_runs
		SET status = 'PENDING',
		    attempt_count = 0,
		    failed_at = NULL,
		    last_error = NULL,
		    run_after = NOW(),
		    updated_at = NOW()
		WHERE id = $1 AND status = 'FAILED'
	`
	res, err := s.pool.Exec(ctx, query, taskID)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("task %d not found or not in FAILED state", taskID)
	}
	return nil
}

// Requeue creates a new PENDING task based on an existing task's ID.
// It copies the immutable fields (spec_hash, payload, etc.) to a new row.
func (s *Service) Requeue(ctx context.Context, taskID int64) (int64, error) {
	query := `
		INSERT INTO task_runs (
			spec_hash, queue_name, status, priority, run_after, 
			payload_json, max_attempts, created_at, updated_at
		)
		SELECT 
			spec_hash, queue_name, 'PENDING', priority, NOW(),
			payload_json, max_attempts, NOW(), NOW()
		FROM task_runs
		WHERE id = $1
		RETURNING id
	`
	var newID int64
	err := s.pool.QueryRow(ctx, query, taskID).Scan(&newID)
	if err != nil {
		return 0, fmt.Errorf("failed to requeue task %d: %w", taskID, err)
	}
	return newID, nil
}

// RequeueByHash creates a new PENDING task based on a spec_hash.
// It uses the most recent task with that hash as the template.
func (s *Service) RequeueByHash(ctx context.Context, specHash string) (int64, error) {
	query := `
		INSERT INTO task_runs (
			spec_hash, queue_name, status, priority, run_after, 
			payload_json, max_attempts, created_at, updated_at
		)
		SELECT 
			spec_hash, queue_name, 'PENDING', priority, NOW(),
			payload_json, max_attempts, NOW(), NOW()
		FROM task_runs
		WHERE spec_hash = $1
		ORDER BY created_at DESC
		LIMIT 1
		RETURNING id
	`
	var newID int64
	err := s.pool.QueryRow(ctx, query, specHash).Scan(&newID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("no task found with hash %s", specHash)
		}
		return 0, fmt.Errorf("failed to requeue hash %s: %w", specHash, err)
	}
		return newID, nil
	}
	