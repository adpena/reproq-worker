package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
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
			COALESCE(task_path, spec_json->>'task_path') AS task_path,
			status,
			attempts,
			max_attempts,
			last_error,
			failed_at,
			last_attempted_at,
			finished_at
		FROM public.task_runs
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
			COALESCE(task_path, spec_json->>'task_path') AS task_path,
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
		FROM public.task_runs
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

// RetryFailedTask resets a failed version 1 task back to READY for execution.
func (s *Service) RetryFailedTask(ctx context.Context, resultID int64) (int64, error) {
	return s.retryFailed(ctx, &resultID)
}

// RetryAllFailedTasks is all-or-nothing: a legacy source refuses the operation
// without resetting any attempts or consuming historical failure evidence.
func (s *Service) RetryAllFailedTasks(ctx context.Context) (int64, error) {
	return s.retryFailed(ctx, nil)
}

var ErrWorkflowRetry = errors.New("terminal workflow tasks cannot be retried in place; enqueue a fresh workflow or explicitly replay a detached single task")

func (s *Service) retryFailed(ctx context.Context, resultID *int64) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	predicate := "status='FAILED'"
	arguments := []any{}
	if resultID != nil {
		predicate += " AND result_id=$1"
		arguments = append(arguments, *resultID)
	}
	// Materialize and lock targets in the database, then derive the gate and
	// update from that exact set. Large queues do not allocate one Go ID per row.
	var legacy, unsupported, workflowMembers, changed int64
	err = tx.QueryRow(ctx, `WITH targets AS MATERIALIZED (
  SELECT result_id,spec_version,workflow_id FROM public.task_runs WHERE `+predicate+` ORDER BY result_id FOR UPDATE
 ), changed AS (
  UPDATE public.task_runs SET status='READY',run_after=clock_timestamp(),
   attempts=0,started_at=NULL,last_attempted_at=NULL,finished_at=NULL,return_json=NULL,
   last_error=NULL,failed_at=NULL,leased_until=NULL,leased_by=NULL,lease_token=NULL,
   cancel_requested=FALSE,updated_at=clock_timestamp()
  WHERE result_id IN (SELECT result_id FROM targets) AND status='FAILED'
   AND NOT EXISTS(SELECT 1 FROM targets WHERE spec_version<>1)
   AND NOT EXISTS(SELECT 1 FROM targets WHERE workflow_id IS NOT NULL)
  RETURNING result_id
 ) SELECT (SELECT count(*) FROM targets WHERE spec_version=0),
  (SELECT count(*) FROM targets WHERE spec_version NOT IN(0,1)),
  (SELECT count(*) FROM targets WHERE workflow_id IS NOT NULL),
  (SELECT count(*) FROM changed)`, arguments...).Scan(&legacy, &unsupported, &workflowMembers, &changed)
	if err != nil {
		return 0, err
	}
	if legacy > 0 {
		return 0, ErrLegacySpec
	}
	if unsupported > 0 {
		return 0, errors.New("unsupported task spec_version; install a compatible runtime before retrying")
	}
	if workflowMembers > 0 {
		return 0, ErrWorkflowRetry
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return changed, nil
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
