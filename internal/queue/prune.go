package queue

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"strconv"
	"time"
)

// The same predicate is checked again in the locked raw DELETE. This avoids
// Django Collector/FK races and retains callbacks while another member is active.
const noActiveRelatedTasksSQL = `NOT EXISTS(SELECT 1 FROM public.task_runs child
 WHERE child.parent_id=task.result_id AND child.status NOT IN ('SUCCESSFUL','FAILED','CANCELLED'))
 AND NOT EXISTS(SELECT 1 FROM public.task_runs member WHERE task.workflow_id IS NOT NULL
  AND member.workflow_id=task.workflow_id AND member.result_id<>task.result_id
  AND member.status NOT IN ('SUCCESSFUL','FAILED','CANCELLED'))`

type PruneHistoryOptions struct {
	Statuses  []TaskStatus
	OlderThan *time.Duration
	Field     string
	Limit     int
	DryRun    bool
}

func ValidatePruneHistoryOptions(options PruneHistoryOptions) error {
	if err := ValidatePruneLimit(options.Limit); err != nil {
		return err
	}
	if options.OlderThan != nil && *options.OlderThan < 0 {
		return errors.New("history age must be nonnegative")
	}
	if options.Field != "" && options.Field != "finished_at" && options.Field != "enqueued_at" && options.Field != "updated_at" && options.Field != "started_at" {
		return errors.New("history field must be finished_at, enqueued_at, updated_at or started_at")
	}
	if options.Statuses != nil && len(options.Statuses) == 0 {
		return errors.New("history requires terminal statuses")
	}
	for _, status := range options.Statuses {
		if status != StatusSuccessful && status != StatusFailed && status != StatusCancelled {
			return errors.New("history statuses must be SUCCESSFUL, FAILED or CANCELLED")
		}
	}
	return nil
}

func (s *Service) PruneHistory(ctx context.Context, options PruneHistoryOptions) (int64, error) {
	if err := ValidatePruneHistoryOptions(options); err != nil {
		return 0, err
	}
	if options.Statuses == nil {
		options.Statuses = []TaskStatus{StatusSuccessful, StatusFailed, StatusCancelled}
	}
	if options.Field == "" {
		options.Field = "finished_at"
	}
	return s.pruneTasks(ctx, options.Limit, options.DryRun, false, options.Statuses, options.Field, options.OlderThan)
}

func (s *Service) PruneExpired(ctx context.Context, limit int, dryRun bool) (int64, error) {
	if err := ValidatePruneLimit(limit); err != nil {
		return 0, err
	}
	age := time.Duration(0)
	return s.pruneTasks(ctx, limit, dryRun, true, nil, "expires_at", &age)
}

// A single finite cursor pass can finalize an expired root yet retain it while
// related work is active. It never consumes deletion quota or gets selected
// repeatedly; that history can become eligible on a later pass.
func (s *Service) pruneTasks(ctx context.Context, limit int, dryRun, expired bool, statuses []TaskStatus, field string, olderThan *time.Duration) (int64, error) {
	var upperID int64
	var asOf time.Time
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(result_id),0),clock_timestamp() FROM public.task_runs`).Scan(&upperID, &asOf); err != nil {
		return 0, err
	}
	age := "0"
	if olderThan != nil {
		age = strconv.FormatInt(olderThan.Microseconds(), 10)
	}
	names := make([]string, len(statuses))
	for i, status := range statuses {
		names[i] = string(status)
	}
	// field is exclusively the validated enum above, or the literal expires_at.
	clockField := "task." + field
	eligibility := `task.status=ANY($1::text[]) AND ` + noActiveRelatedTasksSQL
	comparison := "<"
	if expired {
		comparison = "<="
		names = []string{string(StatusReady), string(StatusWaiting), string(StatusSuccessful), string(StatusFailed), string(StatusCancelled)}
		eligibility = `task.status=ANY($1::text[]) AND(task.status IN ('READY','WAITING') OR(` + noActiveRelatedTasksSQL + `))`
	}
	eligibility += ` AND ` + clockField + comparison + `$2::timestamptz-($3::text||' microseconds')::interval
  AND task.result_id<=$4`
	base := []any{names, asOf, age, upperID}
	if dryRun {
		var count int64
		err := s.pool.QueryRow(ctx, `SELECT count(*) FROM(SELECT task.result_id FROM public.task_runs task WHERE `+eligibility+`
   ORDER BY `+clockField+`,task.result_id LIMIT NULLIF($5::bigint,0)) eligible`, append(base, limit)...).Scan(&count)
		return count, err
	}
	var count, cursorID int64
	var cursor time.Time
	hasCursor := false
	for {
		size := transitionBatchSize
		if limit > 0 {
			remaining := limit - int(count)
			if remaining <= 0 {
				break
			}
			if remaining < size {
				size = remaining
			}
		}
		parameters := append(append([]any{}, base...), hasCursor, cursor, cursorID, size)
		rows, err := s.pool.Query(ctx, `SELECT task.result_id,`+clockField+` FROM public.task_runs task WHERE `+eligibility+`
   AND(NOT $5::boolean OR(`+clockField+`,task.result_id)>($6::timestamptz,$7::bigint))
   ORDER BY `+clockField+`,task.result_id LIMIT $8`, parameters...)
		if err != nil {
			return count, err
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			var stamp time.Time
			if err := rows.Scan(&id, &stamp); err != nil {
				rows.Close()
				return count, err
			}
			ids = append(ids, id)
			cursorID = id
			cursor = stamp
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return count, err
		}
		if len(ids) == 0 {
			break
		}
		hasCursor = true
		err = withAvailableTransitionScopes(ctx, s.pool, ids, expired, func(tx pgx.Tx, selected []int64) (func(), error) {
			if expired {
				changed, err := tx.Query(ctx, `UPDATE public.task_runs SET status='FAILED',wait_count=0,
     leased_by=NULL,leased_until=NULL,lease_token=NULL,run_after=NULL,
     finished_at=clock_timestamp(),failed_at=clock_timestamp(),updated_at=clock_timestamp(),
     last_error='expiry: Task expired before execution',
     errors_json=errors_json||jsonb_build_object('kind','expiry','message','Task expired before execution','at',clock_timestamp())
     WHERE result_id=ANY($1::bigint[]) AND status IN ('READY','WAITING')
      AND expires_at IS NOT NULL AND expires_at<=clock_timestamp() RETURNING result_id`, selected)
				if err != nil {
					return nil, err
				}
				fresh := []int64{}
				for changed.Next() {
					var id int64
					if err := changed.Scan(&id); err != nil {
						changed.Close()
						return nil, err
					}
					fresh = append(fresh, id)
				}
				err = changed.Err()
				changed.Close()
				if err != nil {
					return nil, err
				}
				failed, err := tx.Query(ctx, `SELECT result_id FROM public.task_runs WHERE result_id=ANY($1::bigint[])
      AND status IN ('FAILED','CANCELLED') AND expires_at IS NOT NULL AND expires_at<=clock_timestamp()`, selected)
				if err != nil {
					return nil, err
				}
				roots := []int64{}
				for failed.Next() {
					var id int64
					if err := failed.Scan(&id); err != nil {
						failed.Close()
						return nil, err
					}
					roots = append(roots, id)
				}
				err = failed.Err()
				failed.Close()
				if err != nil {
					return nil, err
				}
				if err := finalizeTerminalDependencies(ctx, tx, roots, fresh); err != nil {
					return nil, err
				}
			}
			predicate := `task.status=ANY($2::text[])`
			tag, err := tx.Exec(ctx, `DELETE FROM public.task_runs task WHERE task.result_id=ANY($1::bigint[])
    AND `+predicate+` AND `+noActiveRelatedTasksSQL+`
    AND `+clockField+comparison+`clock_timestamp()-($3::text||' microseconds')::interval`, selected, names, age)
			if err != nil {
				return nil, err
			}
			deleted := tag.RowsAffected()
			return func() {
				count += deleted
				policy := "history"
				if expired {
					policy = "expired"
				}
				prunedTasks.WithLabelValues(policy).Add(float64(deleted))
			}, nil
		})
		if err != nil {
			return count, err
		}
	}
	return count, nil
}
