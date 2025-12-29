package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"reproq-worker/internal/models"
)

var ErrNoTasks = errors.New("no tasks available")

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Claim polls for a pending task and atomically transitions it to RUNNING.
// It uses a single CTE to claim the task in one network round-trip.
func (s *Service) Claim(ctx context.Context, queueName string, workerID string, leaseDuration time.Duration) (*models.TaskRun, error) {
	now := time.Now()
	leasedUntil := now.Add(leaseDuration)

	query := `
		WITH target AS (
			SELECT id 
			FROM task_runs
			WHERE queue_name = $1
			  AND status = 'PENDING'
			  AND run_after <= NOW()
			ORDER BY priority DESC, created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE task_runs
		SET status = 'RUNNING',
		    worker_id = $2,
		    started_at = $3,
		    leased_until = $4,
		    attempt_count = attempt_count + 1,
		    updated_at = $3
		FROM target
		WHERE task_runs.id = target.id
		RETURNING 
			task_runs.id, spec_hash, queue_name, status, priority, run_after, 
			leased_until, worker_id, payload_json, result_json, error_json, 
			stdout, stderr, exit_code,
			attempt_count, max_attempts, created_at, updated_at, started_at, completed_at
	`

	var task models.TaskRun
	err := s.pool.QueryRow(ctx, query, queueName, workerID, now, leasedUntil).Scan(
		&task.ID, &task.SpecHash, &task.QueueName, &task.Status, &task.Priority, &task.RunAfter,
		&task.LeasedUntil, &task.WorkerID, &task.PayloadJSON, &task.ResultJSON, &task.ErrorJSON,
		&task.Stdout, &task.Stderr, &task.ExitCode,
		&task.AttemptCount, &task.MaxAttempts, &task.CreatedAt, &task.UpdatedAt, &task.StartedAt, &task.CompletedAt,
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

// Heartbeat extends the lease for a running task.
func (s *Service) Heartbeat(ctx context.Context, taskID int64, duration time.Duration) error {
	query := `
		UPDATE task_runs
		SET leased_until = $1,
		    updated_at = NOW()
		WHERE id = $2 AND status = 'RUNNING'
	`
	newLease := time.Now().Add(duration)
	result, err := s.pool.Exec(ctx, query, newLease, taskID)
	if err != nil {
		return fmt.Errorf("heartbeat failed: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("task %d not running or lost", taskID)
	}
	return nil
}

// CompleteSuccess marks a task as SUCCESSFUL and stores the result.
func (s *Service) CompleteSuccess(ctx context.Context, taskID int64, resultJSON json.RawMessage, stdout, stderr string, exitCode int) error {
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
	`
	_, err := s.pool.Exec(ctx, query, resultJSON, stdout, stderr, exitCode, taskID)
	return err
}

// CompleteFailure marks a task as FAILED or schedules a retry.
func (s *Service) CompleteFailure(ctx context.Context, taskID int64, errorJSON json.RawMessage, stdout, stderr string, exitCode int, shouldRetry bool, nextRunAfter time.Time) error {
	var status models.TaskStatus
	var runAfter time.Time
	
	if shouldRetry {
		status = models.StatusPending
		runAfter = nextRunAfter
	} else {
		status = models.StatusFailed
		runAfter = time.Now()
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
		    updated_at = NOW()
		WHERE id = $7
	`
	_, err := s.pool.Exec(ctx, query, status, errorJSON, stdout, stderr, exitCode, runAfter, taskID)
	return err
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
	