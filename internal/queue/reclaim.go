package queue

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
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

// Reclaim uses only the stored attempt budget, shared with the manual CLI.
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
	// One shared predicate is used without dropping expiry/status guards when a
	// caller supplies a limit. The cutoff is always evaluated by PostgreSQL.
	predicate := `status='RUNNING' AND ((leased_until IS NOT NULL AND leased_until<=clock_timestamp()-($1::text||' microseconds')::interval)
	 OR ($2::boolean AND leased_until IS NULL))`
	parameters := []any{strconv.FormatInt(options.OlderThan.Microseconds(), 10), options.IncludeNullLease, options.Limit}
	if options.DryRun {
		err := s.pool.QueryRow(ctx, `SELECT count(*) FROM(SELECT result_id FROM public.task_runs WHERE `+predicate+`
		 ORDER BY leased_until NULLS FIRST,result_id LIMIT NULLIF($3::integer,0)) eligible`, parameters...).Scan(&result.Count)
		return result, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	parameters = append(parameters, options.Action == "requeue")
	rows, err := tx.Query(ctx, `
	 WITH expired AS (
	  SELECT result_id,lease_token,leased_by,leased_until FROM public.task_runs WHERE `+predicate+`
	  ORDER BY leased_until NULLS FIRST,result_id LIMIT NULLIF($3::integer,0) FOR UPDATE SKIP LOCKED
	 )
	 UPDATE public.task_runs tr SET
	  status=CASE WHEN tr.cancel_requested THEN 'CANCELLED' WHEN $4::boolean AND tr.attempts<tr.max_attempts THEN 'READY' ELSE 'FAILED' END,
	  finished_at=CASE WHEN NOT tr.cancel_requested AND $4::boolean AND tr.attempts<tr.max_attempts THEN NULL ELSE clock_timestamp() END,
	  failed_at=CASE WHEN NOT tr.cancel_requested AND $4::boolean AND tr.attempts<tr.max_attempts THEN NULL ELSE clock_timestamp() END,
	  last_error=CASE WHEN NOT tr.cancel_requested AND $4::boolean AND tr.attempts<tr.max_attempts THEN NULL
	   WHEN tr.cancel_requested THEN 'cancelled: Cancellation finalized after lease expiry' ELSE 'lease_expiry: Worker heartbeat lost or process crashed' END,
	  errors_json=tr.errors_json||jsonb_build_object('kind',CASE WHEN tr.cancel_requested THEN 'cancelled' ELSE 'lease_expiry' END,
	   'message',CASE WHEN tr.cancel_requested THEN 'Cancellation finalized after lease expiry' ELSE 'Worker heartbeat lost or process crashed' END,'at',clock_timestamp()),
	  run_after=NULL,leased_until=NULL,leased_by=NULL,lease_token=NULL,updated_at=clock_timestamp()
	 FROM expired WHERE tr.result_id=expired.result_id AND tr.status='RUNNING'
	  AND tr.lease_token IS NOT DISTINCT FROM expired.lease_token AND tr.leased_by IS NOT DISTINCT FROM expired.leased_by
	  AND tr.leased_until IS NOT DISTINCT FROM expired.leased_until
	  AND ((tr.leased_until IS NOT NULL AND tr.leased_until<=clock_timestamp()-($1::text||' microseconds')::interval)
	    OR($2::boolean AND tr.leased_until IS NULL))
	 RETURNING tr.result_id,tr.status
	`, parameters...)
	if err != nil {
		return result, err
	}
	terminal := []int64{}
	for rows.Next() {
		var id int64
		var status TaskStatus
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return result, err
		}
		result.Count++
		switch status {
		case StatusReady:
			result.Requeued++
		case StatusFailed:
			result.Failed++
			terminal = append(terminal, id)
		case StatusCancelled:
			result.Cancelled++
			terminal = append(terminal, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	for _, id := range terminal {
		if err := finalizeFailedWorkflow(ctx, tx, id); err != nil {
			return result, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return result, err
	}
	reclaimedTasks.WithLabelValues(string(StatusReady)).Add(float64(result.Requeued))
	reclaimedTasks.WithLabelValues(string(StatusFailed)).Add(float64(result.Failed))
	reclaimedTasks.WithLabelValues(string(StatusCancelled)).Add(float64(result.Cancelled))
	return result, nil
}
