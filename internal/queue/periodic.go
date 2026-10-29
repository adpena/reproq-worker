package queue

import (
	"context"
	"fmt"
	"time"
	_ "time/tzdata" // Static runtime images need named cron zones without host tzdata.

	"github.com/jackc/pgx/v5"
	"github.com/robfig/cron/v3"
)

// EnqueueDuePeriodicTasks coalesces one overdue nominal occurrence, then advances
// to the next cron instant after the database clock. Optional pg_cron definitions
// are owned by their native provider and never selected by Go beat.
func (s *Service) EnqueueDuePeriodicTasks(ctx context.Context) (int, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT name,cron_expr,next_run_at FROM public.periodic_tasks
      WHERE enabled AND scheduler_provider='go' AND next_run_at<=$1 FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return 0, err
	}
	type dueDefinition struct {
		name, expression string
		nominal          time.Time
	}
	definitions := []dueDefinition{}
	for rows.Next() {
		var definition dueDefinition
		if err := rows.Scan(&definition.name, &definition.expression, &definition.nominal); err != nil {
			rows.Close()
			return 0, err
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	enqueued, failures, duplicates := 0, 0, 0
	var lastError error
	for index, definition := range definitions {
		inserted := false
		err := withSavepoint(ctx, tx, fmt.Sprintf("periodic_%d", index), func() error {
			next, err := nextPeriodicRun(definition.expression, now)
			if err != nil {
				return fmt.Errorf("invalid cron expression for %s: %w", definition.name, err)
			}
			var id int64
			if err := tx.QueryRow(ctx, `SELECT result_id,inserted FROM public.reproq_enqueue_periodic_occurrence($1,$2,'go')`, definition.name, definition.nominal).Scan(&id, &inserted); err != nil {
				return fmt.Errorf("cannot enqueue periodic definition %s: %w", definition.name, err)
			}
			tag, err := tx.Exec(ctx, `UPDATE public.periodic_tasks SET next_run_at=$1,updated_at=clock_timestamp()
              WHERE name=$2 AND next_run_at=$3 AND enabled AND scheduler_provider='go'`, next, definition.name, definition.nominal)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fmt.Errorf("periodic definition changed before advancement: %s", definition.name)
			}
			return nil
		})
		if err != nil {
			failures++
			lastError = err
			continue
		}
		if inserted {
			enqueued++
		} else {
			duplicates++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	periodicOccurrences.WithLabelValues("inserted").Add(float64(enqueued))
	periodicOccurrences.WithLabelValues("duplicate").Add(float64(duplicates))
	periodicOccurrences.WithLabelValues("rejected").Add(float64(failures))
	if failures > 0 {
		return enqueued, fmt.Errorf("%d periodic definitions require repair; last error: %w", failures, lastError)
	}
	return enqueued, nil
}

func nextPeriodicRun(expression string, now time.Time) (time.Time, error) {
	normalized, err := portableCronExpression(expression)
	if err != nil {
		return time.Time{}, err
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(normalized)
	if err != nil {
		return time.Time{}, err
	}
	// An unzoned expression is UTC regardless of the server session or host
	// timezone. An explicit schedule zone still controls local time and DST.
	next := schedule.Next(now.UTC())
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression has no future occurrence")
	}
	return next, nil
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
