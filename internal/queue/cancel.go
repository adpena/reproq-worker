package queue

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// RequestCancel is the sole operator cancellation transition. Pending work is
// terminally cancelled; running work receives a cooperative request. Explicit
// cancellation may retire pending legacy work without relabeling or executing it.
func (s *Service) RequestCancel(ctx context.Context, resultID int64) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status TaskStatus
	var requested bool
	err = tx.QueryRow(ctx, "SELECT status,cancel_requested FROM public.task_runs WHERE result_id=$1 FOR UPDATE", resultID).Scan(&status, &requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if (status != StatusRunning && status != StatusReady && status != StatusWaiting) || (status == StatusRunning && requested) {
		return 0, nil
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
          errors_json=errors_json||jsonb_build_object('kind','cancelled',
            'message','Cancellation requested before execution','at',clock_timestamp())
          WHERE result_id=$1`, resultID)
		if err == nil {
			err = finalizeFailedWorkflow(ctx, tx, resultID)
		}
	}
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	cancellationTransitions.WithLabelValues(outcome).Inc()
	return 1, nil
}
