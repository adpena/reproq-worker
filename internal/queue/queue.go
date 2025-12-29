package queue

import (
	"context"
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
// It uses SELECT ... FOR UPDATE SKIP LOCKED to handle concurrency safely.
func (s *Service) Claim(ctx context.Context, queueName string, workerID string, leaseDuration time.Duration) (*models.TaskRun, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Poll (Select for update)
	// We select the ID first to keep the lock minimal if possible, but fetching the whole row is fine.
	query := `
		SELECT id, spec_hash, queue_name, status, priority, run_after, 
		       leased_until, worker_id, payload_json, result_json, error_json, 
		       attempt_count, max_attempts, created_at, updated_at, started_at, completed_at
		FROM task_runs
		WHERE queue_name = $1
		  AND status = 'PENDING'
		  AND run_after <= NOW()
		ORDER BY priority DESC, created_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`

	var task models.TaskRun
	err = tx.QueryRow(ctx, query, queueName).Scan(
		&task.ID, &task.SpecHash, &task.QueueName, &task.Status, &task.Priority, &task.RunAfter,
		&task.LeasedUntil, &task.WorkerID, &task.PayloadJSON, &task.ResultJSON, &task.ErrorJSON,
		&task.AttemptCount, &task.MaxAttempts, &task.CreatedAt, &task.UpdatedAt, &task.StartedAt, &task.CompletedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoTasks
		}
		return nil, fmt.Errorf("failed to query task: %w", err)
	}

	// 2. Update (Claim)
	now := time.Now()
	leasedUntil := now.Add(leaseDuration)
	updateQuery := `
		UPDATE task_runs
		SET status = 'RUNNING',
		    worker_id = $1,
		    started_at = $2,
		    leased_until = $3,
		    attempt_count = attempt_count + 1,
		    updated_at = $2
		WHERE id = $4
		RETURNING status, worker_id, started_at, leased_until, attempt_count, updated_at
	`

	err = tx.QueryRow(ctx, updateQuery, workerID, now, leasedUntil, task.ID).Scan(
		&task.Status, &task.WorkerID, &task.StartedAt, &task.LeasedUntil, &task.AttemptCount, &task.UpdatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("failed to update task claim: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return &task, nil
}
