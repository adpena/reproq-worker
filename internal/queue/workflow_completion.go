package queue

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// The same terminal workflow propagation serves completion, reclaim and pending
// operator cancellation. Caller row locks and terminal checks prevent repeats.
func finalizeFailedWorkflow(ctx context.Context, tx pgx.Tx, resultID int64) error {

	workflowFail := `
			WITH wf AS (
				SELECT workflow_id FROM public.task_runs
				WHERE result_id = $1 AND workflow_id IS NOT NULL
			),
			workflow_update AS (
				UPDATE public.workflow_runs
				SET failure_count = failure_count + 1,
				    status = 'FAILED',
				    updated_at = NOW()
				WHERE workflow_id = (SELECT workflow_id FROM wf)
				  AND callback_result_id <> $1
				  AND status <> 'FAILED'
				RETURNING callback_result_id
			),
			workflow_callback AS (
				UPDATE public.workflow_runs
				SET status = 'FAILED',
				    updated_at = NOW()
				WHERE workflow_id = (SELECT workflow_id FROM wf)
				  AND callback_result_id = $1
				RETURNING callback_result_id
			)
			SELECT callback_result_id FROM workflow_update
		`
	var callbackID *int64
	err := tx.QueryRow(ctx, workflowFail, resultID).Scan(&callbackID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to mark workflow failed: %w", err)
	}
	if callbackID != nil {
		_, err = tx.Exec(ctx, `
				UPDATE public.task_runs
				SET status = 'FAILED',
				    finished_at = NOW(),
				    errors_json = errors_json || jsonb_build_object(
						'kind', 'workflow_failed',
						'message', 'Workflow failed due to task failure',
						'failed_result_id', $2::bigint,
						'at', NOW()
					),
				    last_error = 'workflow_failed: Workflow failed due to task failure',
				    failed_at = NOW(),
				    run_after = NULL,
				    leased_by = NULL,
				    leased_until = NULL,
				    lease_token = NULL,
				    wait_count = 0,
				    updated_at = NOW()
				WHERE result_id = $1 AND status IN ('WAITING', 'READY')
			`, *callbackID, resultID)
		if err != nil {
			return fmt.Errorf("failed to fail workflow callback: %w", err)
		}
	}

	return nil
}
