package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type FailedTaskSummary struct {
	ResultID        int64
	QueueName       string
	TaskPath        string
	Status          TaskStatus
	Attempts        int
	MaxAttempts     int
	LastError       *string
	FailedAt        *time.Time
	LastAttemptedAt *time.Time
	FinishedAt      *time.Time
}

type FailedTaskDetail struct {
	ResultID        int64
	QueueName       string
	TaskPath        string
	Status          TaskStatus
	Attempts        int
	MaxAttempts     int
	LastError       *string
	FailedAt        *time.Time
	LastAttemptedAt *time.Time
	FinishedAt      *time.Time
	SpecJSON        json.RawMessage
	ErrorsJSON      json.RawMessage
	ReturnJSON      json.RawMessage
}

// ListFailedTasks returns recent failed tasks for triage.
func (s *Service) ListFailedTasks(ctx context.Context, limit int, queueName string) ([]FailedTaskSummary, error) {
	if limit <= 0 {
		limit = 50
	}

	baseQuery := `
		SELECT
			result_id,
			queue_name,
			spec_json->>'task_path' AS task_path,
			status,
			attempts,
			max_attempts,
			last_error,
			failed_at,
			last_attempted_at,
			finished_at
		FROM task_runs
		WHERE status = 'FAILED'
	`

	args := []any{limit}
	if queueName != "" {
		baseQuery += " AND queue_name = $2"
		args = append(args, queueName)
	}

	query := baseQuery + " ORDER BY failed_at DESC NULLS LAST, finished_at DESC NULLS LAST, result_id DESC LIMIT $1"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []FailedTaskSummary
	for rows.Next() {
		var item FailedTaskSummary
		var taskPath sql.NullString
		var lastError sql.NullString
		var failedAt sql.NullTime
		var lastAttemptedAt sql.NullTime
		var finishedAt sql.NullTime

		if err := rows.Scan(
			&item.ResultID,
			&item.QueueName,
			&taskPath,
			&item.Status,
			&item.Attempts,
			&item.MaxAttempts,
			&lastError,
			&failedAt,
			&lastAttemptedAt,
			&finishedAt,
		); err != nil {
			return nil, err
		}

		item.TaskPath = taskPath.String
		item.LastError = nullStringPtr(lastError)
		item.FailedAt = nullTimePtr(failedAt)
		item.LastAttemptedAt = nullTimePtr(lastAttemptedAt)
		item.FinishedAt = nullTimePtr(finishedAt)

		items = append(items, item)
	}

	return items, rows.Err()
}

// InspectFailedTask returns full details for a failed task.
func (s *Service) InspectFailedTask(ctx context.Context, resultID int64) (*FailedTaskDetail, error) {
	query := `
		SELECT
			result_id,
			queue_name,
			spec_json->>'task_path' AS task_path,
			status,
			attempts,
			max_attempts,
			last_error,
			failed_at,
			last_attempted_at,
			finished_at,
			spec_json,
			errors_json,
			return_json
		FROM task_runs
		WHERE result_id = $1 AND status = 'FAILED'
	`

	var detail FailedTaskDetail
	var taskPath sql.NullString
	var lastError sql.NullString
	var failedAt sql.NullTime
	var lastAttemptedAt sql.NullTime
	var finishedAt sql.NullTime

	if err := s.pool.QueryRow(ctx, query, resultID).Scan(
		&detail.ResultID,
		&detail.QueueName,
		&taskPath,
		&detail.Status,
		&detail.Attempts,
		&detail.MaxAttempts,
		&lastError,
		&failedAt,
		&lastAttemptedAt,
		&finishedAt,
		&detail.SpecJSON,
		&detail.ErrorsJSON,
		&detail.ReturnJSON,
	); err != nil {
		return nil, err
	}

	detail.TaskPath = taskPath.String
	detail.LastError = nullStringPtr(lastError)
	detail.FailedAt = nullTimePtr(failedAt)
	detail.LastAttemptedAt = nullTimePtr(lastAttemptedAt)
	detail.FinishedAt = nullTimePtr(finishedAt)

	return &detail, nil
}

// RetryFailedTask resets a failed task back to READY for execution.
func (s *Service) RetryFailedTask(ctx context.Context, resultID int64) (int64, error) {
	query := `
		UPDATE task_runs
		SET status = 'READY',
			run_after = NOW(),
			attempts = 0,
			started_at = NULL,
			last_attempted_at = NULL,
			finished_at = NULL,
			return_json = NULL,
			last_error = NULL,
			failed_at = NULL,
			leased_until = NULL,
			leased_by = NULL,
			cancel_requested = FALSE,
			updated_at = NOW()
		WHERE result_id = $1 AND status = 'FAILED'
	`
	result, err := s.pool.Exec(ctx, query, resultID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// RetryAllFailedTasks resets all failed tasks back to READY.
func (s *Service) RetryAllFailedTasks(ctx context.Context) (int64, error) {
	query := `
		UPDATE task_runs
		SET status = 'READY',
			run_after = NOW(),
			attempts = 0,
			started_at = NULL,
			last_attempted_at = NULL,
			finished_at = NULL,
			return_json = NULL,
			last_error = NULL,
			failed_at = NULL,
			leased_until = NULL,
			leased_by = NULL,
			cancel_requested = FALSE,
			updated_at = NOW()
		WHERE status = 'FAILED'
	`
	result, err := s.pool.Exec(ctx, query)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
