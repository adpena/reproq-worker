package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

type PeriodicTask struct {
	Name        string          `db:"name"`
	CronExpr    string          `db:"cron_expr"`
	TaskPath    string          `db:"task_path"`
	PayloadJSON json.RawMessage `db:"payload_json"`
	QueueName   string          `db:"queue_name"`
	Priority    int             `db:"priority"`
	MaxAttempts int             `db:"max_attempts"`
	LastRunAt   *time.Time      `db:"last_run_at"`
	NextRunAt   time.Time       `db:"next_run_at"`
	Enabled     bool            `db:"enabled"`
}

type periodicSpec struct {
	TaskPath     string          `json:"task_path"`
	Args         json.RawMessage `json:"args"`
	Kwargs       map[string]any  `json:"kwargs"`
	PeriodicName string          `json:"periodic_name"`
	ScheduledAt  string          `json:"scheduled_at"`
}

// EnqueueDuePeriodicTasks finds tasks where next_run_at <= now, enqueues them, and schedules the next run.
func (s *Service) EnqueueDuePeriodicTasks(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// 1. Find due tasks and lock them
	query := `
		SELECT name, cron_expr, task_path, payload_json, queue_name, priority, max_attempts, next_run_at
		FROM periodic_tasks
		WHERE enabled = TRUE AND next_run_at <= NOW()
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var dueTasks []PeriodicTask
	for rows.Next() {
		var t PeriodicTask
		if err := rows.Scan(&t.Name, &t.CronExpr, &t.TaskPath, &t.PayloadJSON, &t.QueueName, &t.Priority, &t.MaxAttempts, &t.NextRunAt); err != nil {
			return 0, err
		}
		dueTasks = append(dueTasks, t)
	}
	rows.Close()

	if len(dueTasks) == 0 {
		return 0, nil
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

	for _, t := range dueTasks {
		// 2. Enqueue the task
		argsJSON := t.PayloadJSON
		if len(argsJSON) == 0 {
			argsJSON = json.RawMessage("[]")
		}
		spec := periodicSpec{
			TaskPath:     t.TaskPath,
			Args:         argsJSON,
			Kwargs:       map[string]any{},
			PeriodicName: t.Name,
			ScheduledAt:  t.NextRunAt.Format(time.RFC3339),
		}
		specBytes, err := json.Marshal(spec)
		if err != nil {
			return 0, fmt.Errorf("failed to build spec for %s: %w", t.Name, err)
		}
		specJSON := string(specBytes)
		specHashBytes := sha256.Sum256(specBytes)
		specHash := hex.EncodeToString(specHashBytes[:])
		queueName := t.QueueName
		if queueName == "" {
			queueName = "default"
		}
		maxAttempts := t.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = 3
		}
		insertQuery := `
			WITH params AS (
				SELECT
					$1::varchar(64) AS spec_hash,
					$2::text AS queue_name,
					$3::jsonb AS spec_json,
					$4::int AS priority,
					$5::timestamptz AS run_after,
					$6::text AS backend_alias,
					$7::int AS max_attempts,
					$8::int AS timeout_seconds
			)
			INSERT INTO task_runs (
				backend_alias,
				queue_name,
				priority,
				run_after,
				spec_json,
				spec_hash,
				status,
				enqueued_at,
				attempts,
				max_attempts,
				timeout_seconds,
				wait_count,
				worker_ids,
				errors_json,
				cancel_requested,
				created_at,
				updated_at
			)
			SELECT
				params.backend_alias,
				params.queue_name,
				params.priority,
				params.run_after,
				params.spec_json,
				params.spec_hash,
				'READY',
				NOW(),
				0,
				params.max_attempts,
				params.timeout_seconds,
				0,
				'[]'::jsonb,
				'[]'::jsonb,
				FALSE,
				NOW(),
				NOW()
			FROM params
			WHERE NOT EXISTS (
				SELECT 1 FROM task_runs
				WHERE spec_hash = params.spec_hash AND status IN ('READY', 'RUNNING')
			)
		`
		_, err = tx.Exec(
			ctx,
			insertQuery,
			specHash,
			queueName,
			specJSON,
			t.Priority,
			t.NextRunAt,
			"default",
			maxAttempts,
			900,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to enqueue periodic task %s: %w", t.Name, err)
		}

		// 3. Schedule next run
		sched, err := parser.Parse(t.CronExpr)
		if err != nil {
			return 0, fmt.Errorf("invalid cron expr for %s: %w", t.Name, err)
		}
		nextRun := sched.Next(time.Now())

		updateQuery := `
			UPDATE periodic_tasks
			SET last_run_at = next_run_at,
			    next_run_at = $1,
			    updated_at = NOW()
			WHERE name = $2
		`
		_, err = tx.Exec(ctx, updateQuery, nextRun, t.Name)
		if err != nil {
			return 0, fmt.Errorf("failed to update next run for %s: %w", t.Name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	return len(dueTasks), nil
}

// UpsertPeriodicTask creates or updates a periodic task.
func (s *Service) UpsertPeriodicTask(ctx context.Context, t PeriodicTask) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(t.CronExpr)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	nextRun := sched.Next(time.Now())

	query := `
		INSERT INTO periodic_tasks (name, cron_expr, task_path, payload_json, queue_name, priority, max_attempts, next_run_at, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (name) DO UPDATE
		SET cron_expr = EXCLUDED.cron_expr,
		    task_path = EXCLUDED.task_path,
		    payload_json = EXCLUDED.payload_json,
		    queue_name = EXCLUDED.queue_name,
		    priority = EXCLUDED.priority,
		    max_attempts = EXCLUDED.max_attempts,
		    next_run_at = EXCLUDED.next_run_at,
		    enabled = EXCLUDED.enabled,
		    updated_at = NOW()
	`
	_, err = s.pool.Exec(ctx, query, t.Name, t.CronExpr, t.TaskPath, t.PayloadJSON, t.QueueName, t.Priority, t.MaxAttempts, nextRun, t.Enabled)
	return err
}
