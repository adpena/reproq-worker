package queue

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"strconv"
	"time"
)

type ReclaimOptions struct {
	Action           string
	OlderThan        time.Duration
	Limit            int
	IncludeNullLease bool
	DryRun           bool
}
type ReclaimResult struct {
	Count     int64 `json:"count"`
	Requeued  int64 `json:"requeued"`
	Failed    int64 `json:"failed"`
	Cancelled int64 `json:"cancelled"`
	DryRun    bool  `json:"dry_run"`
}

type reclaimCandidate struct {
	id    int64
	owner pgtype.Text
	token pgtype.UUID
	until pgtype.Timestamptz
}

func (s *Service) Reclaim(ctx context.Context) (int64, error) {
	result, err := s.ReclaimWithOptions(ctx, ReclaimOptions{Action: "requeue"})
	return result.Count, err
}

func (s *Service) ReclaimWithOptions(ctx context.Context, options ReclaimOptions) (ReclaimResult, error) {
	result := ReclaimResult{DryRun: options.DryRun}
	if options.Action == "" {
		options.Action = "requeue"
	}
	if (options.Action != "requeue" && options.Action != "fail") || options.OlderThan < 0 || options.Limit < 0 {
		return result, errors.New("reclaim requires action requeue|fail and nonnegative age/limit")
	}
	age := strconv.FormatInt(options.OlderThan.Microseconds(), 10)
	eligibility := `status='RUNNING' AND ((leased_until IS NOT NULL AND leased_until<=clock_timestamp()-($1::text||' microseconds')::interval)
 OR($2::boolean AND leased_until IS NULL))`
	if options.DryRun {
		err := s.pool.QueryRow(ctx, `SELECT count(*) FROM(SELECT result_id FROM public.task_runs WHERE `+eligibility+`
   ORDER BY leased_until NULLS FIRST,result_id LIMIT NULLIF($3::bigint,0)) eligible`, age, options.IncludeNullLease, options.Limit).Scan(&result.Count)
		return result, err
	}
	// A finite start-of-pass boundary excludes new claims and newly inserted work.
	var upperID int64
	var asOf time.Time
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(max(result_id),0),clock_timestamp() FROM public.task_runs`).Scan(&upperID, &asOf); err != nil {
		return result, err
	}
	var cursor pgtype.Timestamptz
	var cursorID int64
	hasCursor := false
	for {
		size := transitionBatchSize
		if options.Limit > 0 {
			remaining := options.Limit - int(result.Count)
			if remaining <= 0 {
				break
			}
			if remaining < size {
				size = remaining
			}
		}
		rows, err := s.pool.Query(ctx, `SELECT result_id,leased_by,lease_token,leased_until FROM public.task_runs
   WHERE status='RUNNING' AND result_id<=$7 AND((leased_until IS NOT NULL AND leased_until<=$8::timestamptz-($1::text||' microseconds')::interval)
    OR($2::boolean AND leased_until IS NULL))
    AND(NOT $4::boolean OR(COALESCE(leased_until,'-infinity'::timestamptz),result_id)>($5::timestamptz,$6::bigint))
   ORDER BY leased_until NULLS FIRST,result_id LIMIT $3`, age, options.IncludeNullLease, size, hasCursor, cursor, cursorID, upperID, asOf)
		if err != nil {
			return result, err
		}
		candidates := map[int64]reclaimCandidate{}
		ids := []int64{}
		for rows.Next() {
			var candidate reclaimCandidate
			if err := rows.Scan(&candidate.id, &candidate.owner, &candidate.token, &candidate.until); err != nil {
				rows.Close()
				return result, err
			}
			candidates[candidate.id] = candidate
			ids = append(ids, candidate.id)
			cursor = candidate.until
			cursorID = candidate.id
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
		if len(ids) == 0 {
			break
		}
		if !cursor.Valid {
			cursor = pgtype.Timestamptz{Valid: true, InfinityModifier: pgtype.NegativeInfinity}
		}
		hasCursor = true
		err = withAvailableTransitionScopes(ctx, s.pool, ids, true, func(tx pgx.Tx, selected []int64) (func(), error) {
			owners := make([]pgtype.Text, len(selected))
			tokens := make([]pgtype.UUID, len(selected))
			leases := make([]pgtype.Timestamptz, len(selected))
			for i, id := range selected {
				candidate := candidates[id]
				owners[i] = candidate.owner
				tokens[i] = candidate.token
				leases[i] = candidate.until
			}
			changed, err := tx.Query(ctx, `WITH selected AS(SELECT * FROM unnest($1::bigint[],$2::text[],$3::uuid[],$4::timestamptz[]) AS claim(result_id,leased_by,lease_token,leased_until))
    UPDATE public.task_runs task SET
     status=CASE WHEN task.cancel_requested THEN 'CANCELLED' WHEN $7::boolean AND task.attempts<task.max_attempts THEN 'READY' ELSE 'FAILED' END,
     finished_at=CASE WHEN NOT task.cancel_requested AND $7::boolean AND task.attempts<task.max_attempts THEN NULL ELSE clock_timestamp() END,
     failed_at=CASE WHEN NOT task.cancel_requested AND $7::boolean AND task.attempts<task.max_attempts THEN NULL ELSE clock_timestamp() END,
     last_error=CASE WHEN NOT task.cancel_requested AND $7::boolean AND task.attempts<task.max_attempts THEN NULL
      WHEN task.cancel_requested THEN 'cancelled: Cancellation finalized after lease expiry' ELSE 'lease_expiry: Worker heartbeat lost or process crashed' END,
     errors_json=task.errors_json||jsonb_build_object('kind',CASE WHEN task.cancel_requested THEN 'cancelled' ELSE 'lease_expiry' END,
      'message',CASE WHEN task.cancel_requested THEN 'Cancellation finalized after lease expiry' ELSE 'Worker heartbeat lost or process crashed' END,'at',clock_timestamp()),
     run_after=NULL,leased_until=NULL,leased_by=NULL,lease_token=NULL,updated_at=clock_timestamp()
    FROM selected WHERE task.result_id=selected.result_id AND task.status='RUNNING'
     AND task.leased_by IS NOT DISTINCT FROM selected.leased_by AND task.lease_token IS NOT DISTINCT FROM selected.lease_token
     AND task.leased_until IS NOT DISTINCT FROM selected.leased_until
     AND((task.leased_until IS NOT NULL AND task.leased_until<=clock_timestamp()-($5::text||' microseconds')::interval)
      OR($6::boolean AND task.leased_until IS NULL)) RETURNING task.result_id,task.status`, selected, owners, tokens, leases, age, options.IncludeNullLease, options.Action == "requeue")
			if err != nil {
				return nil, err
			}
			terminal := []int64{}
			batch := ReclaimResult{}
			for changed.Next() {
				var id int64
				var status TaskStatus
				if err := changed.Scan(&id, &status); err != nil {
					changed.Close()
					return nil, err
				}
				batch.Count++
				switch status {
				case StatusReady:
					batch.Requeued++
				case StatusFailed:
					batch.Failed++
					terminal = append(terminal, id)
				case StatusCancelled:
					batch.Cancelled++
					terminal = append(terminal, id)
				}
			}
			err = changed.Err()
			changed.Close()
			if err != nil {
				return nil, err
			}
			if err := finalizeTerminalDependencies(ctx, tx, terminal, terminal); err != nil {
				return nil, err
			}
			return func() {
				result.Count += batch.Count
				result.Requeued += batch.Requeued
				result.Failed += batch.Failed
				result.Cancelled += batch.Cancelled
				reclaimedTasks.WithLabelValues(string(StatusReady)).Add(float64(batch.Requeued))
				reclaimedTasks.WithLabelValues(string(StatusFailed)).Add(float64(batch.Failed))
				reclaimedTasks.WithLabelValues(string(StatusCancelled)).Add(float64(batch.Cancelled))
			}, nil
		})
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
