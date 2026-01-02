package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
)

type PeriodicTask struct {
	Name             string          `db:"name"`
	CronExpr         string          `db:"cron_expr"`
	TaskPath         string          `db:"task_path"`
	PayloadJSON      json.RawMessage `db:"payload_json"`
	QueueName        string          `db:"queue_name"`
	Priority         int             `db:"priority"`
	MaxAttempts      int             `db:"max_attempts"`
	ConcurrencyKey   *string         `db:"concurrency_key"`
	ConcurrencyLimit int             `db:"concurrency_limit"`
	LastRunAt        *time.Time      `db:"last_run_at"`
	NextRunAt        time.Time       `db:"next_run_at"`
	Enabled          bool            `db:"enabled"`
}

type execSpec struct {
	TimeoutSeconds int `json:"timeout_seconds"`
	MaxAttempts    int `json:"max_attempts"`
}

type periodicSpec struct {
	TaskPath         string          `json:"task_path"`
	Args             json.RawMessage `json:"args"`
	Kwargs           json.RawMessage `json:"kwargs"`
	QueueName        string          `json:"queue_name"`
	Priority         int             `json:"priority"`
	ConcurrencyKey   *string         `json:"concurrency_key"`
	ConcurrencyLimit int             `json:"concurrency_limit"`
	PeriodicName     string          `json:"periodic_name"`
	ScheduledAt      string          `json:"scheduled_at"`
	Exec             execSpec        `json:"exec"`
}

// EnqueueDuePeriodicTasks finds tasks where next_run_at <= now, enqueues them, and schedules the next run.
func (s *Service) EnqueueDuePeriodicTasks(ctx context.Context) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// 1. Find due tasks and lock them
	query := `
		SELECT name, cron_expr, task_path, payload_json, queue_name, priority, max_attempts,
		       concurrency_key, concurrency_limit, next_run_at
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
		if err := rows.Scan(
			&t.Name,
			&t.CronExpr,
			&t.TaskPath,
			&t.PayloadJSON,
			&t.QueueName,
			&t.Priority,
			&t.MaxAttempts,
			&t.ConcurrencyKey,
			&t.ConcurrencyLimit,
			&t.NextRunAt,
		); err != nil {
			return 0, err
		}
		dueTasks = append(dueTasks, t)
	}
	rows.Close()

	if len(dueTasks) == 0 {
		return 0, nil
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	failures := 0
	var lastErr error
	enqueued := 0

	for idx, t := range dueTasks {
		err := withSavepoint(ctx, tx, fmt.Sprintf("periodic_%d", idx), func() error {
			// 2. Enqueue the task
			argsJSON, kwargsJSON, err := parsePeriodicPayload(t.PayloadJSON)
			if err != nil {
				return fmt.Errorf("failed to parse periodic payload for %s: %w", t.Name, err)
			}
			maxAttempts := t.MaxAttempts
			if maxAttempts <= 0 {
				maxAttempts = 3
			}
			resolvedQueueName := t.QueueName
			if resolvedQueueName == "" {
				resolvedQueueName = "default"
			}
			spec := periodicSpec{
				TaskPath:         t.TaskPath,
				Args:             argsJSON,
				Kwargs:           kwargsJSON,
				QueueName:        resolvedQueueName,
				Priority:         t.Priority,
				ConcurrencyKey:   t.ConcurrencyKey,
				ConcurrencyLimit: t.ConcurrencyLimit,
				PeriodicName:     t.Name,
				ScheduledAt:      t.NextRunAt.UTC().Format(time.RFC3339),
				Exec: execSpec{
					TimeoutSeconds: 900,
					MaxAttempts:    maxAttempts,
				},
			}
			specBytes, err := json.Marshal(spec)
			if err != nil {
				return fmt.Errorf("failed to build spec for %s: %w", t.Name, err)
			}
			specHashBytes := sha256.Sum256(specBytes)
			specHash := hex.EncodeToString(specHashBytes[:])
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
					$8::int AS timeout_seconds,
					$9::text AS concurrency_key,
					$10::int AS concurrency_limit,
					$11::text AS task_path
			)
			INSERT INTO task_runs (
				backend_alias,
				queue_name,
				priority,
				run_after,
				spec_json,
				spec_hash,
				task_path,
				status,
				enqueued_at,
				attempts,
				max_attempts,
				timeout_seconds,
				concurrency_key,
				concurrency_limit,
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
				params.task_path,
				'READY',
				NOW(),
				0,
				params.max_attempts,
				params.timeout_seconds,
				params.concurrency_key,
				params.concurrency_limit,
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
				resolvedQueueName,
				specBytes,
				t.Priority,
				t.NextRunAt,
				"default",
				maxAttempts,
				900,
				t.ConcurrencyKey,
				t.ConcurrencyLimit,
				t.TaskPath,
			)
			if err != nil {
				return fmt.Errorf("failed to enqueue periodic task %s: %w", t.Name, err)
			}

			// 3. Schedule next run
			sched, err := parser.Parse(t.CronExpr)
			if err != nil {
				return fmt.Errorf("invalid cron expr for %s: %w", t.Name, err)
			}
			nextRun := sched.Next(time.Now().UTC())

			updateQuery := `
			UPDATE periodic_tasks
			SET last_run_at = next_run_at,
			    next_run_at = $1,
			    updated_at = NOW()
			WHERE name = $2
		`
			_, err = tx.Exec(ctx, updateQuery, nextRun, t.Name)
			if err != nil {
				return fmt.Errorf("failed to update next run for %s: %w", t.Name, err)
			}
			return nil
		})
		if err != nil {
			failures++
			lastErr = err
			continue
		}
		enqueued++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	if failures > 0 {
		return enqueued, fmt.Errorf("failed to enqueue %d periodic tasks (last error: %w)", failures, lastErr)
	}
	return enqueued, nil
}

func parsePeriodicPayload(raw json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("[]"), json.RawMessage("{}"), nil
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err == nil {
		_, hasArgs := envelope["args"]
		_, hasKwargs := envelope["kwargs"]
		if hasArgs || hasKwargs {
			args := envelope["args"]
			if len(args) == 0 {
				args = json.RawMessage("[]")
			}
			kwargs := envelope["kwargs"]
			if len(kwargs) == 0 {
				kwargs = json.RawMessage("{}")
			}
			if err := ensureJSONArray(args); err != nil {
				return nil, nil, fmt.Errorf("invalid periodic args: %w", err)
			}
			if err := ensureJSONObject(kwargs); err != nil {
				return nil, nil, fmt.Errorf("invalid periodic kwargs: %w", err)
			}
			return args, kwargs, nil
		}
	}

	if err := ensureJSONArray(raw); err == nil {
		return raw, json.RawMessage("{}"), nil
	}

	if err := ensureJSONObject(raw); err == nil {
		return json.RawMessage("[]"), raw, nil
	}

	return nil, nil, fmt.Errorf("invalid payload JSON")
}

// UpsertPeriodicTask creates or updates a periodic task.
func (s *Service) UpsertPeriodicTask(ctx context.Context, t PeriodicTask) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(t.CronExpr)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	nextRun := sched.Next(time.Now().UTC())

	query := `
		INSERT INTO periodic_tasks (name, cron_expr, task_path, payload_json, queue_name, priority, max_attempts, concurrency_key, concurrency_limit, next_run_at, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (name) DO UPDATE
		SET cron_expr = EXCLUDED.cron_expr,
		    task_path = EXCLUDED.task_path,
		    payload_json = EXCLUDED.payload_json,
		    queue_name = EXCLUDED.queue_name,
		    priority = EXCLUDED.priority,
		    max_attempts = EXCLUDED.max_attempts,
		    concurrency_key = EXCLUDED.concurrency_key,
		    concurrency_limit = EXCLUDED.concurrency_limit,
		    next_run_at = EXCLUDED.next_run_at,
		    enabled = EXCLUDED.enabled,
		    updated_at = NOW()
	`
	_, err = s.pool.Exec(
		ctx,
		query,
		t.Name,
		t.CronExpr,
		t.TaskPath,
		t.PayloadJSON,
		t.QueueName,
		t.Priority,
		t.MaxAttempts,
		t.ConcurrencyKey,
		t.ConcurrencyLimit,
		nextRun,
		t.Enabled,
	)
	return err
}

func withSavepoint(ctx context.Context, tx pgx.Tx, name string, fn func() error) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT "+name); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+name)
		_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT "+name)
		return err
	}
	_, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+name)
	return err
}

func ensureJSONArray(raw json.RawMessage) error {
	var decoded []any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	return nil
}

func ensureJSONObject(raw json.RawMessage) error {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	return nil
}
