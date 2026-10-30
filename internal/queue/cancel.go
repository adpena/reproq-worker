package queue

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

var ErrTransitionScopeChanged = errors.New("task transition scope changed repeatedly; inspect its current state before retrying")

// Running cancellation remains cooperative. A pending root needs its recursive
// terminal scope; a race changing that decision rolls back rather than upgrading
// locks after a workflow row has already been acquired.
func (s *Service) RequestCancel(ctx context.Context, resultID int64) (int64, error) {
	for attempt := 0; attempt < 3; attempt++ {
		count, outcome, retry, err := s.requestCancelAttempt(ctx, resultID)
		if retry {
			continue
		}
		if err == nil && count != 0 {
			cancellationTransitions.WithLabelValues(outcome).Inc()
		}
		return count, err
	}
	return 0, ErrTransitionScopeChanged
}

func (s *Service) requestCancelAttempt(ctx context.Context, resultID int64) (int64, string, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var preview TaskStatus
	err = tx.QueryRow(ctx, "SELECT status FROM public.task_runs WHERE result_id=$1", resultID).Scan(&preview)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	recursive := preview == StatusReady || preview == StatusWaiting
	if _, err := lockTransitionScope(ctx, tx, []int64{resultID}, recursive, false); err != nil {
		return 0, "", false, err
	}
	var status TaskStatus
	var requested bool
	err = tx.QueryRow(ctx, "SELECT status,cancel_requested FROM public.task_runs WHERE result_id=$1", resultID).Scan(&status, &requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	if !recursive && (status == StatusReady || status == StatusWaiting) {
		return 0, "", true, nil
	}
	if (status != StatusRunning && status != StatusReady && status != StatusWaiting) || (status == StatusRunning && requested) {
		return 0, "", false, nil
	}
	outcome := "requested"
	if status == StatusRunning {
		_, err = tx.Exec(ctx, "UPDATE public.task_runs SET cancel_requested=true,updated_at=clock_timestamp() WHERE result_id=$1", resultID)
	} else {
		outcome = "cancelled"
		_, err = tx.Exec(ctx, `UPDATE public.task_runs SET status='CANCELLED',cancel_requested=true,
   leased_by=NULL,leased_until=NULL,lease_token=NULL,run_after=NULL,wait_count=0,
   finished_at=clock_timestamp(),failed_at=clock_timestamp(),updated_at=clock_timestamp(),
   last_error='cancelled: Cancellation requested before execution',
   errors_json=errors_json||jsonb_build_object('kind','cancelled','message','Cancellation requested before execution','at',clock_timestamp())
   WHERE result_id=$1`, resultID)
		if err == nil {
			err = finalizeTerminalDependencies(ctx, tx, []int64{resultID}, []int64{resultID})
		}
	}
	if err != nil {
		return 0, "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, "", false, err
	}
	return 1, outcome, false, nil
}
