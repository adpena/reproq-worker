package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

type PeriodicTask struct {
	Name         string          `db:"name"`
	CronExpr     string          `db:"cron_expr"`
	TaskPath     string          `db:"task_path"`
	PayloadJSON  json.RawMessage `db:"payload_json"`
	QueueName    string          `db:"queue_name"`
	Priority     int             `db:"priority"`
	MaxAttempts  int             `db:"max_attempts"`
	LastRunAt    *time.Time      `db:"last_run_at"`
	NextRunAt    time.Time       `db:"next_run_at"`
	Enabled      bool            `db:"enabled"`
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
		// We need to calculate a spec_hash. For periodic tasks, we can use name + next_run_at to avoid duplicates if beat runs twice.
		specJSON := fmt.Sprintf(`{"task_path": "%s", "args": %s, "kwargs": {}, "periodic_name": "%s", "scheduled_at": "%s"}`, 
			t.TaskPath, string(t.PayloadJSON), t.Name, t.NextRunAt.Format(time.RFC3339))
		
		// Use a simple hash approach or reuse existing logic if accessible.
		// For now, let's just insert.
		
		insertQuery := `
			INSERT INTO task_runs (spec_hash, queue_name, spec_json, priority, run_after, attempts, status)
			VALUES (encode(digest($1, 'sha256'), 'hex'), $2, $3, $4, $5, 0, 'READY')
			ON CONFLICT (spec_hash) WHERE status IN ('READY', 'RUNNING') DO NOTHING
		`
		_, err = tx.Exec(ctx, insertQuery, specJSON, t.QueueName, specJSON, t.Priority, t.NextRunAt)
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
